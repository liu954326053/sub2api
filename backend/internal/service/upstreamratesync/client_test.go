package upstreamratesync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---- 测试共享 stub ----

// stubEncryptor 可逆前缀“加密”，断言密文与明文可区分且不泄露。
type stubEncryptor struct{}

func (stubEncryptor) Encrypt(plaintext string) (string, error) { return "enc:" + plaintext, nil }
func (stubEncryptor) Decrypt(ciphertext string) (string, error) {
	return strings.TrimPrefix(ciphertext, "enc:"), nil
}

type updateTokensCall struct {
	id            int64
	accessEnc     string
	refreshEnc    *string
	tokenExpireAt time.Time
}

type syncResultCall struct {
	id        int64
	syncedAt  time.Time
	status    string
	lastError string
}

type stubConnectionRepo struct {
	mu                sync.Mutex
	conns             map[int64]*Connection
	updateTokensCalls []updateTokensCall
	syncResults       []syncResultCall
}

func newStubConnectionRepo() *stubConnectionRepo {
	return &stubConnectionRepo{conns: make(map[int64]*Connection)}
}

func (r *stubConnectionRepo) add(conn *Connection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *conn
	r.conns[conn.ID] = &cp
}

func (r *stubConnectionRepo) Create(_ context.Context, conn *Connection) error {
	r.add(conn)
	return nil
}

func (r *stubConnectionRepo) GetByID(_ context.Context, id int64) (*Connection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	conn, ok := r.conns[id]
	if !ok {
		return nil, ErrConnectionNotFound
	}
	cp := *conn
	return &cp, nil
}

func (r *stubConnectionRepo) Update(_ context.Context, conn *Connection) error {
	r.add(conn)
	return nil
}

func (r *stubConnectionRepo) Delete(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.conns, id)
	return nil
}

func (r *stubConnectionRepo) List(_ context.Context, _ ConnectionListParams) ([]*Connection, int64, error) {
	return nil, 0, nil
}

func (r *stubConnectionRepo) ListEnabled(_ context.Context) ([]*Connection, error) {
	return nil, nil
}

func (r *stubConnectionRepo) UpdateSyncResult(_ context.Context, id int64, syncedAt time.Time, status string, lastError string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.syncResults = append(r.syncResults, syncResultCall{id: id, syncedAt: syncedAt, status: status, lastError: lastError})
	if conn, ok := r.conns[id]; ok {
		conn.LastSyncAt = &syncedAt
		conn.LastStatus = status
		conn.LastError = lastError
	}
	return nil
}

func (r *stubConnectionRepo) UpdateBalance(_ context.Context, id int64, balance float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if conn, ok := r.conns[id]; ok {
		conn.LastBalance = &balance
	}
	return nil
}

func (r *stubConnectionRepo) UpdateTokens(_ context.Context, id int64, accessTokenEncrypted string, refreshTokenEncrypted *string, tokenExpiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var refreshCopy *string
	if refreshTokenEncrypted != nil {
		value := *refreshTokenEncrypted
		refreshCopy = &value
	}
	r.updateTokensCalls = append(r.updateTokensCalls, updateTokensCall{
		id:            id,
		accessEnc:     accessTokenEncrypted,
		refreshEnc:    refreshCopy,
		tokenExpireAt: tokenExpiresAt,
	})
	if conn, ok := r.conns[id]; ok {
		conn.AccessTokenEncrypted = accessTokenEncrypted
		if refreshCopy != nil {
			conn.RefreshTokenEncrypted = *refreshCopy
		} else {
			conn.RefreshTokenEncrypted = ""
		}
		conn.TokenExpiresAt = &tokenExpiresAt
	}
	return nil
}

type stubRunRepo struct {
	mu       sync.Mutex
	created  []*SyncRun
	finished []*SyncRun
	nextID   int64
}

func (r *stubRunRepo) Create(_ context.Context, run *SyncRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	run.ID = r.nextID
	r.created = append(r.created, run)
	return nil
}

func (r *stubRunRepo) Finish(_ context.Context, run *SyncRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished = append(r.finished, run)
	return nil
}

func (r *stubRunRepo) GetByID(_ context.Context, _ int64) (*SyncRun, error) {
	return nil, ErrSyncRunNotFound
}

func (r *stubRunRepo) List(_ context.Context, _ SyncRunListParams) ([]*SyncRun, int64, error) {
	return nil, 0, nil
}

func (r *stubRunRepo) Prune(_ context.Context, _ int, _ int) (int64, error) {
	return 0, nil
}

type writeCall struct {
	accountID int64
	rate      float64
	snapshot  *AccountSnapshot
}

type stubAccountGateway struct {
	mu         sync.Mutex
	accounts   []ScopedAccount
	listScopes []string
	writes     []writeCall
	failOn     map[int64]error
}

func (g *stubAccountGateway) ListScopedAccounts(_ context.Context, scopeBaseURL string) ([]ScopedAccount, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.listScopes = append(g.listScopes, scopeBaseURL)
	out := make([]ScopedAccount, len(g.accounts))
	copy(out, g.accounts)
	return out, nil
}

func (g *stubAccountGateway) WriteSyncedRate(_ context.Context, accountID int64, rate float64, snapshot *AccountSnapshot) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err, ok := g.failOn[accountID]; ok {
		return err
	}
	g.writes = append(g.writes, writeCall{accountID: accountID, rate: rate, snapshot: snapshot})
	for i := range g.accounts {
		if g.accounts[i].ID == accountID {
			g.accounts[i].RateMultiplier = &rate
			g.accounts[i].LastSyncedRate = &rate
		}
	}
	return nil
}

func (g *stubAccountGateway) writeCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.writes)
}

// ---- 上游 httptest 辅助 ----

func writeJSON(t *testing.T, w http.ResponseWriter, status int, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func successEnvelope(data any) map[string]any {
	return map[string]any{"code": 0, "message": "success", "data": data}
}

func tokenPairData(accessToken, refreshToken string, expiresIn int) map[string]any {
	return map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"expires_in":    expiresIn,
		"token_type":    "Bearer",
	}
}

func keyItem(key, groupName string, rate float64) map[string]any {
	return map[string]any{
		"id":  1,
		"key": key,
		"group": map[string]any{
			"name":            groupName,
			"platform":        "openai",
			"rate_multiplier": rate,
		},
	}
}

func keysPageEnvelope(items []any, total, page, pages int) map[string]any {
	return successEnvelope(map[string]any{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": upstreamKeysPageSize,
		"pages":     pages,
	})
}

// passwordConnection 构造 password 模式连接（credentials 为 JSON 密文）。
func passwordConnection(t *testing.T, enc SecretEncryptor, id int64, baseURL string) *Connection {
	t.Helper()
	credEnc, err := enc.Encrypt(`{"email":"ops@example.com","password":"s3cret"}`)
	require.NoError(t, err)
	return &Connection{
		ID:                   id,
		Name:                 fmt.Sprintf("conn-%d", id),
		BaseURL:              baseURL,
		AuthMode:             AuthModePassword,
		CredentialsEncrypted: credEnc,
		IntervalMinutes:      DefaultIntervalMinutes,
	}
}

// ---- client 测试 ----

func TestClientLoginSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/auth/login", r.URL.Path)
		require.Equal(t, upstreamClientUserAgent, r.Header.Get("User-Agent"))
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "ops@example.com", body["email"])
		require.Equal(t, "s3cret", body["password"])
		writeJSON(t, w, http.StatusOK, successEnvelope(tokenPairData("access-1", "refresh-1", 86400)))
	}))
	defer server.Close()

	client := newUpstreamClient(server.URL, server.Client())
	pair, err := client.login(context.Background(), "ops@example.com", "s3cret")
	require.NoError(t, err)
	require.Equal(t, "access-1", pair.AccessToken)
	require.Equal(t, "refresh-1", pair.RefreshToken)
	require.Equal(t, 86400, pair.ExpiresIn)
}

func TestClientLoginTurnstile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]any{
			"code":    400,
			"reason":  "TURNSTILE_VERIFICATION_FAILED",
			"message": "turnstile verification failed",
		})
	}))
	defer server.Close()

	client := newUpstreamClient(server.URL, server.Client())
	_, err := client.login(context.Background(), "ops@example.com", "s3cret")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUpstreamTurnstile)
}

func TestClientLoginTOTPRequired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusOK, successEnvelope(map[string]any{
			"requires_2fa": true,
			"temp_token":   "tmp",
		}))
	}))
	defer server.Close()

	client := newUpstreamClient(server.URL, server.Client())
	_, err := client.login(context.Background(), "ops@example.com", "s3cret")
	require.ErrorIs(t, err, ErrUpstreamTOTPRequired)
}

func TestClientRefreshSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/auth/refresh", r.URL.Path)
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "refresh-old", body["refresh_token"])
		writeJSON(t, w, http.StatusOK, successEnvelope(tokenPairData("access-2", "refresh-2", 86400)))
	}))
	defer server.Close()

	client := newUpstreamClient(server.URL, server.Client())
	pair, err := client.refresh(context.Background(), "refresh-old")
	require.NoError(t, err)
	require.Equal(t, "access-2", pair.AccessToken)
	require.Equal(t, "refresh-2", pair.RefreshToken)
}

func TestClientListAllKeysPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/keys", r.URL.Path)
		require.Equal(t, "Bearer access-1", r.Header.Get("Authorization"))
		switch r.URL.Query().Get("page") {
		case "1":
			writeJSON(t, w, http.StatusOK, keysPageEnvelope([]any{keyItem("sk-aaa", "g1", 1.0)}, 3, 1, 3))
		case "2":
			writeJSON(t, w, http.StatusOK, keysPageEnvelope([]any{keyItem("sk-bbb", "g1", 1.0)}, 3, 2, 3))
		case "3":
			writeJSON(t, w, http.StatusOK, keysPageEnvelope([]any{keyItem("sk-ccc", "g2", 0.5)}, 3, 3, 3))
		default:
			writeJSON(t, w, http.StatusBadRequest, map[string]any{"code": 400, "message": "bad page"})
		}
	}))
	defer server.Close()

	client := newUpstreamClient(server.URL, server.Client())
	keys, err := client.listAllKeys(context.Background(), "access-1")
	require.NoError(t, err)
	require.Len(t, keys, 3)
	require.Equal(t, "sk-aaa", keys[0].Key)
	require.Equal(t, "sk-ccc", keys[2].Key)
	require.Equal(t, 0.5, keys[2].Group.RateMultiplier)
}

func TestClientListAllKeysRetriesTransientFailure(t *testing.T) {
	var mu sync.Mutex
	pageOneCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			mu.Lock()
			pageOneCalls++
			calls := pageOneCalls
			mu.Unlock()
			if calls == 1 {
				writeJSON(t, w, http.StatusBadGateway, map[string]any{"code": 502, "message": "bad gateway"})
				return
			}
		}
		writeJSON(t, w, http.StatusOK, keysPageEnvelope([]any{keyItem("sk-aaa", "g1", 1.0)}, 1, 1, 1))
	}))
	defer server.Close()

	client := newUpstreamClient(server.URL, server.Client())
	keys, err := client.listAllKeys(context.Background(), "access-1")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Equal(t, 2, pageOneCalls)
}

func TestClientUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "invalid token"})
	}))
	defer server.Close()

	client := newUpstreamClient(server.URL, server.Client())
	_, err := client.listKeysPage(context.Background(), "expired", 1)
	require.ErrorIs(t, err, ErrUpstreamUnauthorized)
}

func TestClientResponseTooLarge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("x", upstreamMaxBodyBytes+16)))
	}))
	defer server.Close()

	client := newUpstreamClient(server.URL, server.Client())
	_, err := client.listKeysPage(context.Background(), "access-1", 1)
	require.Error(t, err)
	var upErr *UpstreamError
	require.ErrorAs(t, err, &upErr)
	require.Equal(t, "RESPONSE_TOO_LARGE", upErr.Reason)
}

func TestClientInvalidEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer server.Close()

	client := newUpstreamClient(server.URL, server.Client())
	_, err := client.listKeysPage(context.Background(), "access-1", 1)
	require.Error(t, err)
	var upErr *UpstreamError
	require.ErrorAs(t, err, &upErr)
	require.Equal(t, "INVALID_RESPONSE", upErr.Reason)
}
