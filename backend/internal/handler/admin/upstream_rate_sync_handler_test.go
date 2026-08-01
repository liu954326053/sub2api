package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service/upstreamratesync"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// --- fakes ---

type fakeUpstreamConnRepo struct {
	mu     sync.Mutex
	nextID int64
	items  map[int64]*upstreamratesync.Connection
}

func newFakeUpstreamConnRepo() *fakeUpstreamConnRepo {
	return &fakeUpstreamConnRepo{nextID: 1, items: map[int64]*upstreamratesync.Connection{}}
}

func cloneUpstreamConn(conn *upstreamratesync.Connection) *upstreamratesync.Connection {
	copied := *conn
	return &copied
}

func (r *fakeUpstreamConnRepo) Create(_ context.Context, conn *upstreamratesync.Connection) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.items {
		if existing.BaseURL == conn.BaseURL {
			return upstreamratesync.ErrConnectionConflict
		}
	}
	conn.ID = r.nextID
	r.nextID++
	conn.CreatedAt = time.Now()
	conn.UpdatedAt = conn.CreatedAt
	r.items[conn.ID] = cloneUpstreamConn(conn)
	return nil
}

func (r *fakeUpstreamConnRepo) GetByID(_ context.Context, id int64) (*upstreamratesync.Connection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	conn, ok := r.items[id]
	if !ok {
		return nil, upstreamratesync.ErrConnectionNotFound
	}
	return cloneUpstreamConn(conn), nil
}

func (r *fakeUpstreamConnRepo) Update(_ context.Context, conn *upstreamratesync.Connection) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[conn.ID]; !ok {
		return upstreamratesync.ErrConnectionNotFound
	}
	for id, existing := range r.items {
		if id != conn.ID && existing.BaseURL == conn.BaseURL {
			return upstreamratesync.ErrConnectionConflict
		}
	}
	conn.UpdatedAt = time.Now()
	r.items[conn.ID] = cloneUpstreamConn(conn)
	return nil
}

func (r *fakeUpstreamConnRepo) Delete(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[id]; !ok {
		return upstreamratesync.ErrConnectionNotFound
	}
	delete(r.items, id)
	return nil
}

func (r *fakeUpstreamConnRepo) List(_ context.Context, params upstreamratesync.ConnectionListParams) ([]*upstreamratesync.Connection, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := make([]*upstreamratesync.Connection, 0, len(r.items))
	for _, conn := range r.items {
		all = append(all, cloneUpstreamConn(conn))
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	page, pageSize := params.Page, params.PageSize
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	start := (page - 1) * pageSize
	if start > len(all) {
		start = len(all)
	}
	end := start + pageSize
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], int64(len(all)), nil
}

func (r *fakeUpstreamConnRepo) ListEnabled(_ context.Context) ([]*upstreamratesync.Connection, error) {
	return nil, nil
}

func (r *fakeUpstreamConnRepo) UpdateSyncResult(_ context.Context, id int64, syncedAt time.Time, status string, lastError string) error {
	return nil
}

func (r *fakeUpstreamConnRepo) UpdateBalance(_ context.Context, id int64, balance float64) error {
	return nil
}

func (r *fakeUpstreamConnRepo) UpdateTokens(_ context.Context, id int64, accessTokenEncrypted string, refreshTokenEncrypted *string, tokenExpiresAt time.Time) error {
	return nil
}

type fakeUpstreamRunRepo struct {
	runs []*upstreamratesync.SyncRun
}

func (r *fakeUpstreamRunRepo) Create(_ context.Context, run *upstreamratesync.SyncRun) error {
	run.ID = int64(len(r.runs) + 1)
	r.runs = append(r.runs, run)
	return nil
}

func (r *fakeUpstreamRunRepo) Finish(_ context.Context, run *upstreamratesync.SyncRun) error {
	return nil
}

func (r *fakeUpstreamRunRepo) GetByID(_ context.Context, id int64) (*upstreamratesync.SyncRun, error) {
	for _, run := range r.runs {
		if run.ID == id {
			return run, nil
		}
	}
	return nil, upstreamratesync.ErrSyncRunNotFound
}

func (r *fakeUpstreamRunRepo) List(_ context.Context, params upstreamratesync.SyncRunListParams) ([]*upstreamratesync.SyncRun, int64, error) {
	filtered := make([]*upstreamratesync.SyncRun, 0, len(r.runs))
	for _, run := range r.runs {
		if params.ConnectionID != nil && run.ConnectionID != *params.ConnectionID {
			continue
		}
		if params.Status != "" && run.Status != params.Status {
			continue
		}
		filtered = append(filtered, run)
	}
	page, pageSize := params.Page, params.PageSize
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[start:end], int64(len(filtered)), nil
}

func (r *fakeUpstreamRunRepo) Prune(_ context.Context, retentionDays int, keepPerConnection int) (int64, error) {
	return 0, nil
}

type fakeUpstreamSyncEngine struct {
	testResult *upstreamratesync.TestResult
	testErr    error
	syncRun    *upstreamratesync.SyncRun
	syncErr    error
}

func (e *fakeUpstreamSyncEngine) TestConnection(_ context.Context, connectionID int64) (*upstreamratesync.TestResult, error) {
	return e.testResult, e.testErr
}

func (e *fakeUpstreamSyncEngine) SyncConnection(_ context.Context, connectionID int64) (*upstreamratesync.SyncRun, error) {
	return e.syncRun, e.syncErr
}

// fakeUpstreamEncryptor 可逆假加密：密文带可辨识前缀，便于断言密文不外泄。
type fakeUpstreamEncryptor struct{}

const fakeUpstreamCipherPrefix = "enc:"

func (fakeUpstreamEncryptor) Encrypt(plaintext string) (string, error) {
	return fakeUpstreamCipherPrefix + plaintext, nil
}

func (fakeUpstreamEncryptor) Decrypt(ciphertext string) (string, error) {
	return strings.TrimPrefix(ciphertext, fakeUpstreamCipherPrefix), nil
}

// --- test harness ---

type upstreamRateSyncTestEnv struct {
	router   *gin.Engine
	connRepo *fakeUpstreamConnRepo
	runRepo  *fakeUpstreamRunRepo
	engine   *fakeUpstreamSyncEngine
}

func setupUpstreamRateSyncRouter() *upstreamRateSyncTestEnv {
	gin.SetMode(gin.TestMode)
	env := &upstreamRateSyncTestEnv{
		connRepo: newFakeUpstreamConnRepo(),
		runRepo:  &fakeUpstreamRunRepo{},
		engine:   &fakeUpstreamSyncEngine{},
	}
	handler := newUpstreamRateSyncHandler(env.connRepo, env.runRepo, env.engine, fakeUpstreamEncryptor{})

	router := gin.New()
	base := "/admin/upstream-rate-sync"
	router.GET(base+"/connections", handler.ListConnections)
	router.POST(base+"/connections", handler.CreateConnection)
	router.GET(base+"/connections/:id", handler.GetConnection)
	router.PUT(base+"/connections/:id", handler.UpdateConnection)
	router.DELETE(base+"/connections/:id", handler.DeleteConnection)
	router.POST(base+"/connections/:id/test", handler.TestConnection)
	router.POST(base+"/connections/:id/sync", handler.SyncConnection)
	router.GET(base+"/runs", handler.ListRuns)
	env.router = router
	return env
}

func doUpstreamRateSyncRequest(t *testing.T, router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// --- tests ---

func TestUpstreamRateSyncCreateConnectionPasswordMode(t *testing.T) {
	env := setupUpstreamRateSyncRouter()
	recorder := doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections",
		`{"name":"上游A","base_url":"HTTPS://Example.COM:443/","auth_mode":"password","email":"a@b.c","password":"s3cret","enabled":true,"interval_minutes":15}`)

	require.Equal(t, http.StatusCreated, recorder.Code)
	body := recorder.Body.String()
	// 脱敏断言：响应不含密码明文与密文
	require.NotContains(t, body, "s3cret")
	require.NotContains(t, body, fakeUpstreamCipherPrefix)
	require.Contains(t, body, `"has_credentials":true`)
	require.Contains(t, body, `"interval_minutes":15`)
	// base_url 已归一化（小写 scheme/host、去默认端口、去尾斜杠）
	require.Contains(t, body, `"base_url":"https://example.com"`)

	stored := env.connRepo.items[1]
	require.NotNil(t, stored)
	require.Equal(t, "https://example.com", stored.BaseURL)
	// 落库为密文，且明文为 password 契约 JSON
	require.True(t, strings.HasPrefix(stored.CredentialsEncrypted, fakeUpstreamCipherPrefix))
	plain, err := fakeUpstreamEncryptor{}.Decrypt(stored.CredentialsEncrypted)
	require.NoError(t, err)
	require.JSONEq(t, `{"email":"a@b.c","password":"s3cret"}`, plain)
}

func TestUpstreamRateSyncCreateConnectionTokenMode(t *testing.T) {
	env := setupUpstreamRateSyncRouter()
	recorder := doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections",
		`{"name":"t","base_url":"https://up.example.com","auth_mode":"token","token":"tok-plain-123"}`)

	require.Equal(t, http.StatusCreated, recorder.Code)
	body := recorder.Body.String()
	require.NotContains(t, body, "tok-plain-123")
	require.NotContains(t, body, fakeUpstreamCipherPrefix)
	// 默认 interval
	require.Contains(t, body, fmt.Sprintf(`"interval_minutes":%d`, upstreamratesync.DefaultIntervalMinutes))

	stored := env.connRepo.items[1]
	require.Equal(t, fakeUpstreamCipherPrefix+"tok-plain-123", stored.CredentialsEncrypted)
}

func TestUpstreamRateSyncCreateConnectionValidation(t *testing.T) {
	env := setupUpstreamRateSyncRouter()
	cases := []struct {
		name string
		body string
	}{
		{"缺 name", `{"base_url":"https://a.com","auth_mode":"token","token":"x"}`},
		{"缺 base_url", `{"name":"a","auth_mode":"token","token":"x"}`},
		{"非法 auth_mode", `{"name":"a","base_url":"https://a.com","auth_mode":"oauth","token":"x"}`},
		{"interval 越界", `{"name":"a","base_url":"https://a.com","auth_mode":"token","token":"x","interval_minutes":3}`},
		{"password 模式缺密码", `{"name":"a","base_url":"https://a.com","auth_mode":"password","email":"a@b.c"}`},
		{"token 模式缺 token", `{"name":"a","base_url":"https://a.com","auth_mode":"token"}`},
		{"非法 base_url", `{"name":"a","base_url":"://","auth_mode":"token","token":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections", tc.body)
			require.Equal(t, http.StatusBadRequest, recorder.Code)
		})
	}
}

func TestUpstreamRateSyncCreateConnectionConflict(t *testing.T) {
	env := setupUpstreamRateSyncRouter()
	body := `{"name":"a","base_url":"https://dup.example.com","auth_mode":"token","token":"x"}`
	require.Equal(t, http.StatusCreated, doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections", body).Code)
	// 归一化后同地址（不同写法）同样冲突
	conflict := doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections",
		`{"name":"b","base_url":"https://dup.example.com/","auth_mode":"token","token":"y"}`)
	require.Equal(t, http.StatusConflict, conflict.Code)
}

func TestUpstreamRateSyncListAndGetRedacted(t *testing.T) {
	env := setupUpstreamRateSyncRouter()
	require.Equal(t, http.StatusCreated, doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections",
		`{"name":"a","base_url":"https://a.example.com","auth_mode":"password","email":"a@b.c","password":"pw"}`).Code)
	require.Equal(t, http.StatusCreated, doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections",
		`{"name":"b","base_url":"https://b.example.com","auth_mode":"token","token":"tk"}`).Code)

	list := doUpstreamRateSyncRequest(t, env.router, http.MethodGet, "/admin/upstream-rate-sync/connections?page=1&page_size=10", "")
	require.Equal(t, http.StatusOK, list.Code)
	var listResp struct {
		Data struct {
			Items []map[string]any `json:"items"`
			Total int64            `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &listResp))
	require.Equal(t, int64(2), listResp.Data.Total)
	require.Len(t, listResp.Data.Items, 2)
	// 脱敏断言：任何元素不含密文/明文字段
	for _, item := range listResp.Data.Items {
		for _, key := range []string{"credentials_encrypted", "access_token_encrypted", "refresh_token_encrypted", "credentials", "token", "password"} {
			require.NotContains(t, item, key)
		}
	}
	require.NotContains(t, list.Body.String(), fakeUpstreamCipherPrefix)
	require.NotContains(t, list.Body.String(), "a@b.c")

	get := doUpstreamRateSyncRequest(t, env.router, http.MethodGet, "/admin/upstream-rate-sync/connections/1", "")
	require.Equal(t, http.StatusOK, get.Code)
	require.NotContains(t, get.Body.String(), fakeUpstreamCipherPrefix)

	missing := doUpstreamRateSyncRequest(t, env.router, http.MethodGet, "/admin/upstream-rate-sync/connections/999", "")
	require.Equal(t, http.StatusNotFound, missing.Code)

	badID := doUpstreamRateSyncRequest(t, env.router, http.MethodGet, "/admin/upstream-rate-sync/connections/abc", "")
	require.Equal(t, http.StatusBadRequest, badID.Code)
}

func TestUpstreamRateSyncUpdateKeepsCredentialsWhenBlank(t *testing.T) {
	env := setupUpstreamRateSyncRouter()
	require.Equal(t, http.StatusCreated, doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections",
		`{"name":"a","base_url":"https://a.example.com","auth_mode":"token","token":"tk-original","interval_minutes":30}`).Code)
	before := env.connRepo.items[1].CredentialsEncrypted

	recorder := doUpstreamRateSyncRequest(t, env.router, http.MethodPut, "/admin/upstream-rate-sync/connections/1",
		`{"name":"a2","enabled":true,"interval_minutes":60}`)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotContains(t, recorder.Body.String(), "tk-original")

	after := env.connRepo.items[1]
	require.Equal(t, "a2", after.Name)
	require.True(t, after.Enabled)
	require.Equal(t, 60, after.IntervalMinutes)
	// 凭证字段留空 = 保持不变
	require.Equal(t, before, after.CredentialsEncrypted)
}

func TestUpstreamRateSyncUpdateReplacesCredentials(t *testing.T) {
	env := setupUpstreamRateSyncRouter()
	require.Equal(t, http.StatusCreated, doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections",
		`{"name":"a","base_url":"https://a.example.com","auth_mode":"token","token":"tk-original"}`).Code)

	recorder := doUpstreamRateSyncRequest(t, env.router, http.MethodPut, "/admin/upstream-rate-sync/connections/1", `{"token":"tk-new"}`)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, fakeUpstreamCipherPrefix+"tk-new", env.connRepo.items[1].CredentialsEncrypted)

	// 凭证不完整的替换请求 → 400
	bad := doUpstreamRateSyncRequest(t, env.router, http.MethodPut, "/admin/upstream-rate-sync/connections/1",
		`{"auth_mode":"password","email":"only-email@b.c"}`)
	require.Equal(t, http.StatusBadRequest, bad.Code)
}

func TestUpstreamRateSyncDeleteConnection(t *testing.T) {
	env := setupUpstreamRateSyncRouter()
	require.Equal(t, http.StatusCreated, doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections",
		`{"name":"a","base_url":"https://a.example.com","auth_mode":"token","token":"tk"}`).Code)

	require.Equal(t, http.StatusOK, doUpstreamRateSyncRequest(t, env.router, http.MethodDelete, "/admin/upstream-rate-sync/connections/1", "").Code)
	require.Equal(t, http.StatusNotFound, doUpstreamRateSyncRequest(t, env.router, http.MethodDelete, "/admin/upstream-rate-sync/connections/1", "").Code)
}

func TestUpstreamRateSyncTestConnectionMapping(t *testing.T) {
	env := setupUpstreamRateSyncRouter()

	env.engine.testResult = &upstreamratesync.TestResult{KeysFound: 12, AccountsMatched: 3}
	ok := doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections/1/test", "")
	require.Equal(t, http.StatusOK, ok.Code)
	require.Contains(t, ok.Body.String(), `"keys_found":12`)
	require.Contains(t, ok.Body.String(), `"accounts_matched":3`)

	// Turnstile → 400，reason 含 TURNSTILE
	env.engine.testResult = nil
	env.engine.testErr = upstreamratesync.ErrUpstreamTurnstile
	turnstile := doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections/1/test", "")
	require.Equal(t, http.StatusBadRequest, turnstile.Code)
	require.Contains(t, strings.ToUpper(turnstile.Body.String()), "TURNSTILE")

	// TOTP → 400 明确错误
	env.engine.testErr = upstreamratesync.ErrUpstreamTOTPRequired
	totp := doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections/1/test", "")
	require.Equal(t, http.StatusBadRequest, totp.Code)
	require.Contains(t, totp.Body.String(), "TOTP")

	// 同步进行中 → 409
	env.engine.testErr = upstreamratesync.ErrSyncInProgress
	conflict := doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections/1/test", "")
	require.Equal(t, http.StatusConflict, conflict.Code)
}

func TestUpstreamRateSyncSyncNow(t *testing.T) {
	env := setupUpstreamRateSyncRouter()
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	env.engine.syncRun = &upstreamratesync.SyncRun{
		ID: 7, ConnectionID: 1, StartedAt: started, Status: upstreamratesync.SyncStatusPartial,
		KeysFetched: 5, AccountsMatched: 2, AccountsUpdated: 1, AccountsUnchanged: 1, AccountsUnmatched: 3,
		Details: []upstreamratesync.SyncRunDetail{{AccountID: 9, KeyPrefix: "sk-abcd", GroupName: "g", Action: upstreamratesync.DetailActionUpdated}},
	}
	recorder := doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections/1/sync", "")
	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	require.Contains(t, body, `"accounts_updated":1`)
	require.Contains(t, body, `"status":"partial"`)
	require.Contains(t, body, `"key_prefix":"sk-abcd"`)

	// 引擎失败且无 run → 错误映射
	env.engine.syncRun = nil
	env.engine.syncErr = upstreamratesync.ErrConnectionNotFound
	missing := doUpstreamRateSyncRequest(t, env.router, http.MethodPost, "/admin/upstream-rate-sync/connections/999/sync", "")
	require.Equal(t, http.StatusNotFound, missing.Code)
}

func TestUpstreamRateSyncListRunsFilters(t *testing.T) {
	env := setupUpstreamRateSyncRouter()
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		env.runRepo.runs = append(env.runRepo.runs, &upstreamratesync.SyncRun{
			ID: int64(i + 1), ConnectionID: 1, StartedAt: started, Status: upstreamratesync.SyncStatusSuccess,
			Details: []upstreamratesync.SyncRunDetail{},
		})
	}
	env.runRepo.runs = append(env.runRepo.runs, &upstreamratesync.SyncRun{
		ID: 4, ConnectionID: 2, StartedAt: started, Status: upstreamratesync.SyncStatusFailed, Error: "sync_failed",
		Details: []upstreamratesync.SyncRunDetail{},
	})

	all := doUpstreamRateSyncRequest(t, env.router, http.MethodGet, "/admin/upstream-rate-sync/runs", "")
	require.Equal(t, http.StatusOK, all.Code)
	var resp struct {
		Data struct {
			Items []map[string]any `json:"items"`
			Total int64            `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(all.Body.Bytes(), &resp))
	require.Equal(t, int64(4), resp.Data.Total)
	require.Contains(t, resp.Data.Items[0], "details")

	byConn := doUpstreamRateSyncRequest(t, env.router, http.MethodGet, "/admin/upstream-rate-sync/runs?connection_id=2", "")
	require.Equal(t, http.StatusOK, byConn.Code)
	require.NoError(t, json.Unmarshal(byConn.Body.Bytes(), &resp))
	require.Equal(t, int64(1), resp.Data.Total)
	require.Equal(t, "failed", resp.Data.Items[0]["status"])

	byStatus := doUpstreamRateSyncRequest(t, env.router, http.MethodGet, "/admin/upstream-rate-sync/runs?status=success&page=2&page_size=2", "")
	require.Equal(t, http.StatusOK, byStatus.Code)
	require.NoError(t, json.Unmarshal(byStatus.Body.Bytes(), &resp))
	require.Equal(t, int64(3), resp.Data.Total)
	require.Len(t, resp.Data.Items, 1)

	require.Equal(t, http.StatusBadRequest, doUpstreamRateSyncRequest(t, env.router, http.MethodGet, "/admin/upstream-rate-sync/runs?connection_id=abc", "").Code)
	require.Equal(t, http.StatusBadRequest, doUpstreamRateSyncRequest(t, env.router, http.MethodGet, "/admin/upstream-rate-sync/runs?status=bogus", "").Code)
}
