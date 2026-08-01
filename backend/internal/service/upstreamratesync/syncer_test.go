package upstreamratesync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newUpstreamMock 搭建完整上游 mock：login 颁发 token，keys 单页返回给定条目。
func newUpstreamMock(t *testing.T, items []any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeJSON(t, w, http.StatusOK, successEnvelope(tokenPairData("access-1", "refresh-1", 86400)))
		case "/api/v1/auth/refresh":
			writeJSON(t, w, http.StatusOK, successEnvelope(tokenPairData("access-2", "refresh-2", 86400)))
		case "/api/v1/keys":
			require.Equal(t, "Bearer access-1", r.Header.Get("Authorization"))
			writeJSON(t, w, http.StatusOK, keysPageEnvelope(items, len(items), 1, 1))
		default:
			writeJSON(t, w, http.StatusNotFound, map[string]any{"code": 404, "message": "not found"})
		}
	}))
}

type syncFixture struct {
	syncer   *Syncer
	connRepo *stubConnectionRepo
	runRepo  *stubRunRepo
	gateway  *stubAccountGateway
	conn     *Connection
	server   *httptest.Server
}

func newSyncFixture(t *testing.T, items []any, accounts []ScopedAccount) *syncFixture {
	t.Helper()
	server := newUpstreamMock(t, items)
	t.Cleanup(server.Close)

	connRepo := newStubConnectionRepo()
	runRepo := &stubRunRepo{}
	gateway := &stubAccountGateway{accounts: accounts, failOn: map[int64]error{}}
	syncer := NewSyncer(connRepo, runRepo, gateway, stubEncryptor{})

	conn := passwordConnection(t, stubEncryptor{}, 1, server.URL)
	connRepo.add(conn)
	return &syncFixture{syncer: syncer, connRepo: connRepo, runRepo: runRepo, gateway: gateway, conn: conn, server: server}
}

func floatPtr(v float64) *float64 { return &v }

func TestSyncConnection_Updated(t *testing.T) {
	fixture := newSyncFixture(t,
		[]any{keyItem("sk-alpha-0001", "group-a", 0.5)},
		[]ScopedAccount{{ID: 11, APIKey: "sk-alpha-0001", RateMultiplier: floatPtr(1.0)}},
	)
	run, err := fixture.syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, SyncStatusSuccess, run.Status)
	require.Equal(t, 1, run.KeysFetched)
	require.Equal(t, 1, run.AccountsMatched)
	require.Equal(t, 1, run.AccountsUpdated)
	require.Equal(t, 0, run.AccountsUnchanged)
	require.Equal(t, 0, run.AccountsUnmatched)

	require.Len(t, run.Details, 1)
	detail := run.Details[0]
	require.Equal(t, DetailActionUpdated, detail.Action)
	require.Equal(t, int64(11), detail.AccountID)
	require.Equal(t, "sk-alpha", detail.KeyPrefix, "key_prefix 只允许前 8 位级别标识")
	require.Equal(t, "group-a", detail.GroupName)
	require.NotNil(t, detail.OldRate)
	require.Equal(t, 1.0, *detail.OldRate)
	require.NotNil(t, detail.NewRate)
	require.Equal(t, 0.5, *detail.NewRate)

	// 写回：rate + 同构快照。
	require.Equal(t, 1, fixture.gateway.writeCount())
	write := fixture.gateway.writes[0]
	require.Equal(t, int64(11), write.accountID)
	require.Equal(t, 0.5, write.rate)
	require.Equal(t, 0.5, write.snapshot.Data["group_rate_multiplier"])
	require.Equal(t, 0.5, write.snapshot.Data["resolved_rate_multiplier"], "产品决策：resolved 直接等于分组倍率")

	// 连接级结果回写。
	require.Len(t, fixture.connRepo.syncResults, 1)
	require.Equal(t, SyncStatusSuccess, fixture.connRepo.syncResults[0].status)
	require.Empty(t, fixture.connRepo.syncResults[0].lastError)
}

func TestSyncConnection_Unchanged(t *testing.T) {
	fixture := newSyncFixture(t,
		[]any{keyItem("sk-alpha-0001", "group-a", 1.0)},
		[]ScopedAccount{{ID: 11, APIKey: "sk-alpha-0001", RateMultiplier: floatPtr(1.0)}},
	)
	run, err := fixture.syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, SyncStatusSuccess, run.Status)
	require.Equal(t, 0, run.AccountsUpdated)
	require.Equal(t, 1, run.AccountsUnchanged)
	require.Equal(t, DetailActionUnchanged, run.Details[0].Action)
	require.Equal(t, 1, fixture.gateway.writeCount(), "倍率不变也要刷新快照时间戳（保鲜）")
}

func TestSyncConnection_UnchangedHealsMissingSnapshot(t *testing.T) {
	// 快照被账号编辑的身份失效清除后，下一轮 unchanged 同步同样会重写快照，
	// 账号不会停留在"未探测"。
	fixture := newSyncFixture(t,
		[]any{keyItem("sk-alpha-0001", "group-a", 1.0)},
		[]ScopedAccount{{ID: 11, APIKey: "sk-alpha-0001", RateMultiplier: floatPtr(1.0)}},
	)
	run, err := fixture.syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, SyncStatusSuccess, run.Status)
	require.Equal(t, 0, run.AccountsUpdated, "倍率未变化不计入 updated")
	require.Equal(t, 1, run.AccountsUnchanged)
	require.Equal(t, DetailActionUnchanged, run.Details[0].Action)
	require.Equal(t, 1, fixture.gateway.writeCount())
}

func TestSyncConnection_NilOldRateTreatedAsOne(t *testing.T) {
	// nil → 1.0 折算：上游 1.0 与 nil 账号相等，记 unchanged。
	fixture := newSyncFixture(t,
		[]any{keyItem("sk-alpha-0001", "group-a", 1.0)},
		[]ScopedAccount{{ID: 11, APIKey: "sk-alpha-0001"}},
	)
	run, err := fixture.syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, run.AccountsUnchanged)
	require.Equal(t, 1, fixture.gateway.writeCount(), "倍率不变也要刷新快照时间戳（保鲜）")
}

func TestSyncConnection_ThresholdSkipped(t *testing.T) {
	// 已建立同步基线后 1.0 → 1.6 跳变 60% > 50%，跳过并降级 partial。
	fixture := newSyncFixture(t,
		[]any{keyItem("sk-alpha-0001", "group-a", 1.6)},
		[]ScopedAccount{{ID: 11, APIKey: "sk-alpha-0001", RateMultiplier: floatPtr(1.0), LastSyncedRate: floatPtr(1.0)}},
	)
	run, err := fixture.syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, SyncStatusPartial, run.Status)
	require.Equal(t, DetailActionThresholdSkipped, run.Details[0].Action)
	require.Equal(t, 0, run.AccountsUpdated)
	require.Equal(t, 0, fixture.gateway.writeCount())
	require.Equal(t, SyncStatusPartial, fixture.connRepo.syncResults[0].status)
}

func TestSyncConnection_FirstSyncLargeJumpAllowed(t *testing.T) {
	// 首次同步（无 last_synced_rate）：默认值 1.0 与真实上游倍率 0.06 的
	// 差距不算"跳变"，必须写回，否则默认 1.0 的账号永远同步不上低倍率。
	fixture := newSyncFixture(t,
		[]any{keyItem("sk-alpha-0001", "group-a", 0.06)},
		[]ScopedAccount{{ID: 11, APIKey: "sk-alpha-0001", RateMultiplier: floatPtr(1.0)}},
	)
	run, err := fixture.syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, SyncStatusSuccess, run.Status)
	require.Equal(t, DetailActionUpdated, run.Details[0].Action)
	require.Equal(t, 1, run.AccountsUpdated)
	require.Equal(t, 1, fixture.gateway.writeCount())
}

func TestSyncConnection_ThresholdBoundary50PercentAllowed(t *testing.T) {
	// 恰好 50% 不触发跳过（>50% 才跳过）。
	fixture := newSyncFixture(t,
		[]any{keyItem("sk-alpha-0001", "group-a", 0.5)},
		[]ScopedAccount{{ID: 11, APIKey: "sk-alpha-0001", RateMultiplier: floatPtr(1.0), LastSyncedRate: floatPtr(1.0)}},
	)
	run, err := fixture.syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, SyncStatusSuccess, run.Status)
	require.Equal(t, 1, run.AccountsUpdated)
}

func TestSyncConnection_ManualOverride(t *testing.T) {
	// last_synced=1.0 但当前值 1.2（管理员手改）→ manual_override，保留手改值。
	fixture := newSyncFixture(t,
		[]any{keyItem("sk-alpha-0001", "group-a", 0.9)},
		[]ScopedAccount{{ID: 11, APIKey: "sk-alpha-0001", RateMultiplier: floatPtr(1.2), LastSyncedRate: floatPtr(1.0)}},
	)
	run, err := fixture.syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, SyncStatusPartial, run.Status)
	require.Equal(t, DetailActionManualOverride, run.Details[0].Action)
	require.Equal(t, 0, run.AccountsUpdated)
	require.Equal(t, 0, fixture.gateway.writeCount(), "管理员手改 MUST 保留")
}

func TestSyncConnection_ManualOverrideRecoveredWhenConsistent(t *testing.T) {
	// 当前值与 last_synced 一致（未被手改）→ 正常写回。
	fixture := newSyncFixture(t,
		[]any{keyItem("sk-alpha-0001", "group-a", 1.1)},
		[]ScopedAccount{{ID: 11, APIKey: "sk-alpha-0001", RateMultiplier: floatPtr(1.2), LastSyncedRate: floatPtr(1.2)}},
	)
	run, err := fixture.syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, SyncStatusSuccess, run.Status)
	require.Equal(t, 1, run.AccountsUpdated)
	require.Equal(t, 1, fixture.gateway.writeCount())
}

func TestSyncConnection_UnmatchedKeepsValue(t *testing.T) {
	// 本地有账号、上游无对应 key → unmatched，MUST NOT 清值。
	fixture := newSyncFixture(t,
		[]any{},
		[]ScopedAccount{{ID: 11, APIKey: "sk-alpha-0001", RateMultiplier: floatPtr(0.8)}},
	)
	run, err := fixture.syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, SyncStatusSuccess, run.Status, "unmatched 不降级 partial")
	require.Equal(t, 0, run.AccountsMatched)
	require.Equal(t, 1, run.AccountsUnmatched)
	require.Len(t, run.Details, 1)
	require.Equal(t, DetailActionUnmatched, run.Details[0].Action)
	require.Nil(t, run.Details[0].NewRate)
	require.Equal(t, 0, fixture.gateway.writeCount())

	// 明细序列化：unmatched 的 new_rate 必须为 null。
	raw, err := json.Marshal(run.Details[0])
	require.NoError(t, err)
	require.Contains(t, string(raw), `"new_rate":null`)
}

func TestSyncConnection_BaseURLScopeIsolation(t *testing.T) {
	// 连接 B 与连接 A 持有相同 api_key 字符串；同步 A 时 gateway 只按 A 的
	// base_url 作用域返回账号，B 的账号不进入匹配、不被写回。
	server := newUpstreamMock(t, []any{keyItem("sk-shared-001", "group-a", 0.7)})
	defer server.Close()

	connRepo := newStubConnectionRepo()
	runRepo := &stubRunRepo{}
	// stub 模拟作用域过滤：仅当 scope 命中连接 A 的归一化 base_url 时返回账号。
	gateway := &stubAccountGateway{
		accounts: []ScopedAccount{{ID: 21, APIKey: "sk-shared-001", RateMultiplier: floatPtr(1.0)}},
		failOn:   map[int64]error{},
	}
	syncer := NewSyncer(connRepo, runRepo, gateway, stubEncryptor{})

	connA := passwordConnection(t, stubEncryptor{}, 1, server.URL)
	connRepo.add(connA)

	run, err := syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, run.AccountsUpdated)

	expectedScope, err := NormalizeBaseURL(server.URL)
	require.NoError(t, err)
	require.Equal(t, []string{expectedScope}, gateway.listScopes, "账号匹配必须按连接 base_url 归一化作用域")

	// 连接 B（另一 base_url）的同步互不影响：scope 不同则账号集为空。
	gateway.accounts = nil
	connB := passwordConnection(t, stubEncryptor{}, 2, "https://other-upstream.example.com")
	connRepo.add(connB)
	_, errB := syncer.SyncConnection(context.Background(), 2)
	require.Error(t, errB, "连接 B 上游不可达 → failed，与连接 A 互不影响")
	require.Equal(t, SyncStatusFailed, connRepo.syncResults[len(connRepo.syncResults)-1].status)
}

func TestSyncConnection_FetchFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusInternalServerError, map[string]any{"code": 500, "message": "boom"})
	}))
	defer server.Close()

	connRepo := newStubConnectionRepo()
	runRepo := &stubRunRepo{}
	gateway := &stubAccountGateway{failOn: map[int64]error{}}
	syncer := NewSyncer(connRepo, runRepo, gateway, stubEncryptor{})
	conn := passwordConnection(t, stubEncryptor{}, 1, server.URL)
	connRepo.add(conn)

	run, err := syncer.SyncConnection(context.Background(), 1)
	require.Error(t, err)
	require.Equal(t, SyncStatusFailed, run.Status)
	require.NotEmpty(t, run.Error)
	require.NotNil(t, run.FinishedAt)
	require.Len(t, fixtureConnResults(connRepo), 1)
	require.Equal(t, SyncStatusFailed, fixtureConnResults(connRepo)[0].status)
	require.Equal(t, 0, gateway.writeCount())
}

func fixtureConnResults(repo *stubConnectionRepo) []syncResultCall {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	out := make([]syncResultCall, len(repo.syncResults))
	copy(out, repo.syncResults)
	return out
}

func TestSyncConnection_PartialWriteFailure(t *testing.T) {
	fixture := newSyncFixture(t,
		[]any{keyItem("sk-alpha-0001", "group-a", 0.5), keyItem("sk-beta-00002", "group-a", 0.6)},
		[]ScopedAccount{
			{ID: 11, APIKey: "sk-alpha-0001", RateMultiplier: floatPtr(1.0)},
			{ID: 12, APIKey: "sk-beta-00002", RateMultiplier: floatPtr(1.0)},
		},
	)
	fixture.gateway.failOn[12] = fmt.Errorf("db write failed")

	run, err := fixture.syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err, "个别账号写失败不整体失败")
	require.Equal(t, SyncStatusPartial, run.Status)
	require.Equal(t, 2, run.AccountsMatched)
	require.Equal(t, 1, run.AccountsUpdated)
	require.Contains(t, run.Error, "write_failed")
	require.Equal(t, 1, fixture.gateway.writeCount())
}

func TestSyncConnection_SnapshotShape(t *testing.T) {
	peakGroup := map[string]any{
		"id":  1,
		"key": "sk-peak-00001",
		"group": map[string]any{
			"name":                 "group-peak",
			"platform":             "openai",
			"rate_multiplier":      0.8,
			"peak_rate_enabled":    true,
			"peak_start":           "10:00",
			"peak_end":             "12:00",
			"peak_rate_multiplier": 1.5,
		},
	}
	fixture := newSyncFixture(t,
		[]any{peakGroup},
		[]ScopedAccount{{ID: 11, APIKey: "sk-peak-00001", RateMultiplier: floatPtr(1.0)}},
	)
	now := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC) // 落在 peak 窗口内
	fixture.syncer.SetNow(func() time.Time { return now })

	run, err := fixture.syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, run.AccountsUpdated)

	snapshot := fixture.gateway.writes[0].snapshot
	// 与旧 probe 同构：按 decodeUpstreamBillingProbeSnapshot 消费的字段逐一断言。
	require.Equal(t, "sub2api.key_billing", snapshot.Data["object"])
	require.Equal(t, 1, snapshot.Data["schema_version"])
	require.Equal(t, "token", snapshot.Data["billing_scope"])
	require.Equal(t, 0.8, snapshot.Data["group_rate_multiplier"])
	require.Equal(t, 0.8, snapshot.Data["resolved_rate_multiplier"], "消费方要求 resolved 字段存在且等于分组倍率")
	require.Equal(t, true, snapshot.Data["peak_rate_enabled"])
	require.Equal(t, "10:00", snapshot.Data["peak_start"])
	require.Equal(t, "12:00", snapshot.Data["peak_end"])
	require.Equal(t, 1.5, snapshot.Data["peak_rate_multiplier"])
	require.Equal(t, 1.5, snapshot.Data["applied_peak_multiplier"], "11:00 UTC 落在 10:00-12:00 窗口")
	require.InDelta(t, 1.2, snapshot.Data["effective_rate_multiplier"], 1e-9)
	require.Equal(t, now.Format(time.RFC3339Nano), snapshot.Data["observed_at"])

	interval := time.Duration(fixture.conn.IntervalMinutes) * time.Minute
	require.Equal(t, now, snapshot.ReceivedAt)
	require.Equal(t, now.Add(2*interval), snapshot.FreshUntil, "fresh_until = 2×interval")
	require.Equal(t, now.Add(interval), snapshot.NextProbeAt, "next_probe_at = interval")

	// 快照序列化为 extra JSON 时字段名与旧 probe 完全一致（消费方按名解析）。
	raw, err := json.Marshal(snapshot.Data)
	require.NoError(t, err)
	for _, field := range []string{
		`"billing_scope"`, `"group_rate_multiplier"`, `"resolved_rate_multiplier"`,
		`"peak_rate_enabled"`, `"peak_start"`, `"peak_end"`, `"peak_rate_multiplier"`,
		`"applied_peak_multiplier"`, `"effective_rate_multiplier"`, `"observed_at"`,
	} {
		require.Contains(t, string(raw), field)
	}
}

func TestSyncConnection_SnapshotNoPeak(t *testing.T) {
	fixture := newSyncFixture(t,
		[]any{keyItem("sk-alpha-0001", "group-a", 0.5)},
		[]ScopedAccount{{ID: 11, APIKey: "sk-alpha-0001", RateMultiplier: floatPtr(1.0)}},
	)
	_, err := fixture.syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)

	data := fixture.gateway.writes[0].snapshot.Data
	require.Equal(t, false, data["peak_rate_enabled"], "无 peak 配置时 peak_rate_enabled=false")
	require.Equal(t, 1.0, data["applied_peak_multiplier"])
	require.Equal(t, 0.5, data["effective_rate_multiplier"])
	_, hasStart := data["peak_start"]
	require.False(t, hasStart, "无 peak 配置不写 peak 窗口字段")
}

func TestSyncConnection_TokenMode(t *testing.T) {
	server := newUpstreamMock(t, []any{keyItem("sk-alpha-0001", "group-a", 0.5)})
	defer server.Close()

	enc := stubEncryptor{}
	tokenEnc, err := enc.Encrypt("access-1")
	require.NoError(t, err)

	connRepo := newStubConnectionRepo()
	runRepo := &stubRunRepo{}
	gateway := &stubAccountGateway{
		accounts: []ScopedAccount{{ID: 11, APIKey: "sk-alpha-0001", RateMultiplier: floatPtr(1.0)}},
		failOn:   map[int64]error{},
	}
	syncer := NewSyncer(connRepo, runRepo, gateway, enc)
	conn := &Connection{
		ID:                   1,
		BaseURL:              server.URL,
		AuthMode:             AuthModeToken,
		CredentialsEncrypted: tokenEnc,
		IntervalMinutes:      DefaultIntervalMinutes,
	}
	connRepo.add(conn)

	run, err := syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, SyncStatusSuccess, run.Status)
	require.Equal(t, 1, run.AccountsUpdated)
	require.Empty(t, connRepo.updateTokensCalls, "token 模式不自动刷新")
}

func TestSyncConnection_ReauthOn401(t *testing.T) {
	var loginCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			loginCalls++
			writeJSON(t, w, http.StatusOK, successEnvelope(tokenPairData("access-fresh", "refresh-fresh", 86400)))
		case "/api/v1/keys":
			if r.Header.Get("Authorization") == "Bearer access-stale" {
				writeJSON(t, w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "token expired"})
				return
			}
			require.Equal(t, "Bearer access-fresh", r.Header.Get("Authorization"))
			writeJSON(t, w, http.StatusOK, keysPageEnvelope([]any{}, 0, 1, 1))
		default:
			writeJSON(t, w, http.StatusNotFound, map[string]any{"code": 404, "message": "not found"})
		}
	}))
	defer server.Close()

	enc := stubEncryptor{}
	connRepo := newStubConnectionRepo()
	runRepo := &stubRunRepo{}
	gateway := &stubAccountGateway{failOn: map[int64]error{}}
	syncer := NewSyncer(connRepo, runRepo, gateway, enc)

	conn := passwordConnection(t, enc, 1, server.URL)
	staleEnc, err := enc.Encrypt("access-stale")
	require.NoError(t, err)
	expiresAt := time.Now().Add(12 * time.Hour) // 本地认为未过期，上游实际已失效
	conn.AccessTokenEncrypted = staleEnc
	conn.TokenExpiresAt = &expiresAt
	connRepo.add(conn)

	run, err := syncer.SyncConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, SyncStatusSuccess, run.Status)
	require.Equal(t, 1, loginCalls, "401 后 password 模式强制重新 login 并重试")
	require.Len(t, connRepo.updateTokensCalls, 1)
}

func TestSyncConnection_ConcurrentSameConnectionRejected(t *testing.T) {
	fixture := newSyncFixture(t, []any{}, nil)
	lock := fixture.syncer.connLock(1)
	lock.Lock()
	defer lock.Unlock()

	_, err := fixture.syncer.SyncConnection(context.Background(), 1)
	require.ErrorIs(t, err, ErrSyncInProgress)
}

func TestTestConnection_NoWriteback(t *testing.T) {
	fixture := newSyncFixture(t,
		[]any{keyItem("sk-alpha-0001", "group-a", 0.5), keyItem("sk-gamma-0003", "group-b", 2.0)},
		[]ScopedAccount{
			{ID: 11, APIKey: "sk-alpha-0001", RateMultiplier: floatPtr(1.0)},
			{ID: 12, APIKey: "sk-other-0009", RateMultiplier: floatPtr(1.0)},
		},
	)
	result, err := fixture.syncer.TestConnection(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 2, result.KeysFound)
	require.Equal(t, 1, result.AccountsMatched, "预计匹配数只统计第一页命中的账号")

	// 零写回断言：不产 run、不写账号、不更新连接同步状态。
	require.Empty(t, fixture.runRepo.created)
	require.Empty(t, fixture.runRepo.finished)
	require.Equal(t, 0, fixture.gateway.writeCount())
	require.Empty(t, fixture.connRepo.syncResults)
}

func TestTestConnection_TurnstileError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]any{
			"code":    400,
			"reason":  "TURNSTILE_VERIFICATION_FAILED",
			"message": "turnstile verification failed",
		})
	}))
	defer server.Close()

	connRepo := newStubConnectionRepo()
	syncer := NewSyncer(connRepo, &stubRunRepo{}, &stubAccountGateway{failOn: map[int64]error{}}, stubEncryptor{})
	conn := passwordConnection(t, stubEncryptor{}, 1, server.URL)
	connRepo.add(conn)

	_, err := syncer.TestConnection(context.Background(), 1)
	require.ErrorIs(t, err, ErrUpstreamTurnstile, "连接测试必须把 Turnstile 识别为可区分错误")
}

func TestNormalizeBaseURL(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"https://Example.COM/", "https://example.com"},
		{"https://example.com:443/", "https://example.com"},
		{"http://example.com:80", "http://example.com"},
		{"http://example.com:8080/", "http://example.com:8080"},
		{"example.com", "https://example.com"},
		{"https://example.com/sub2api/", "https://example.com/sub2api"},
	}
	for _, tc := range cases {
		got, err := NormalizeBaseURL(tc.raw)
		require.NoError(t, err, tc.raw)
		require.Equal(t, tc.want, got, tc.raw)
	}
	_, err := NormalizeBaseURL(" ")
	require.Error(t, err)
}
