package upstreamratesync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEnsureAccessToken_TokenMode(t *testing.T) {
	enc := stubEncryptor{}
	tokenEnc, err := enc.Encrypt("pasted-access-token")
	require.NoError(t, err)

	serverHits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		serverHits++
		writeJSON(t, w, http.StatusInternalServerError, map[string]any{"code": 500, "message": "should not be called"})
	}))
	defer server.Close()

	repo := newStubConnectionRepo()
	conn := &Connection{
		ID:                   1,
		BaseURL:              server.URL,
		AuthMode:             AuthModeToken,
		CredentialsEncrypted: tokenEnc,
		IntervalMinutes:      DefaultIntervalMinutes,
	}
	repo.add(conn)

	tm := newTokenManager(repo, enc, nil)
	token, err := tm.ensureAccessToken(context.Background(), conn, newUpstreamClient(server.URL, server.Client()))
	require.NoError(t, err)
	require.Equal(t, "pasted-access-token", token)
	require.Equal(t, 0, serverHits, "token 模式不得触发任何 HTTP 调用")
	require.Empty(t, repo.updateTokensCalls, "token 模式不自动刷新/持久化")
}

func TestEnsureAccessToken_PasswordMode_ValidToken(t *testing.T) {
	enc := stubEncryptor{}
	repo := newStubConnectionRepo()
	conn := passwordConnection(t, enc, 1, "https://upstream.example.com")
	accessEnc, err := enc.Encrypt("access-current")
	require.NoError(t, err)
	expiresAt := time.Now().Add(12 * time.Hour) // 剩余 50%，未临期
	conn.AccessTokenEncrypted = accessEnc
	conn.TokenExpiresAt = &expiresAt
	repo.add(conn)

	tm := newTokenManager(repo, enc, nil)
	token, err := tm.ensureAccessToken(context.Background(), conn, nil)
	require.NoError(t, err)
	require.Equal(t, "access-current", token)
	require.Empty(t, repo.updateTokensCalls)
}

func TestEnsureAccessToken_RefreshRotation(t *testing.T) {
	enc := stubEncryptor{}
	repo := newStubConnectionRepo()

	var refreshCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/auth/refresh", r.URL.Path)
		refreshCalls++
		writeJSON(t, w, http.StatusOK, successEnvelope(tokenPairData("access-new", "refresh-new", 86400)))
	}))
	defer server.Close()

	conn := passwordConnection(t, enc, 1, server.URL)
	accessEnc, err := enc.Encrypt("access-old")
	require.NoError(t, err)
	refreshEnc, err := enc.Encrypt("refresh-old")
	require.NoError(t, err)
	expiresAt := time.Now().Add(30 * time.Minute) // 剩余 <10%×24h，临期
	conn.AccessTokenEncrypted = accessEnc
	conn.RefreshTokenEncrypted = refreshEnc
	conn.TokenExpiresAt = &expiresAt
	repo.add(conn)

	tm := newTokenManager(repo, enc, nil)
	token, err := tm.ensureAccessToken(context.Background(), conn, newUpstreamClient(server.URL, server.Client()))
	require.NoError(t, err)
	require.Equal(t, "access-new", token)
	require.Equal(t, 1, refreshCalls)

	// 严格一次性轮转：新 token 对必须先加密持久化再使用。
	require.Len(t, repo.updateTokensCalls, 1)
	call := repo.updateTokensCalls[0]
	require.Equal(t, int64(1), call.id)
	persistedAccess, err := enc.Decrypt(call.accessEnc)
	require.NoError(t, err)
	require.Equal(t, "access-new", persistedAccess)
	require.NotNil(t, call.refreshEnc)
	persistedRefresh, err := enc.Decrypt(*call.refreshEnc)
	require.NoError(t, err)
	require.Equal(t, "refresh-new", persistedRefresh)
	require.WithinDuration(t, time.Now().Add(24*time.Hour), call.tokenExpireAt, time.Minute)

	// 内存连接状态同步更新，后续调用不再轮转。
	updated, err := repo.GetByID(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "enc:access-new", updated.AccessTokenEncrypted)
}

func TestEnsureAccessToken_RefreshFailsFallsBackToLogin(t *testing.T) {
	enc := stubEncryptor{}
	repo := newStubConnectionRepo()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			writeJSON(t, w, http.StatusUnauthorized, map[string]any{"code": 401, "message": "refresh token expired"})
		case "/api/v1/auth/login":
			writeJSON(t, w, http.StatusOK, successEnvelope(tokenPairData("access-login", "refresh-login", 86400)))
		default:
			writeJSON(t, w, http.StatusNotFound, map[string]any{"code": 404, "message": "not found"})
		}
	}))
	defer server.Close()

	conn := passwordConnection(t, enc, 1, server.URL)
	refreshEnc, err := enc.Encrypt("refresh-old")
	require.NoError(t, err)
	expiresAt := time.Now().Add(-time.Hour) // 已过期
	conn.RefreshTokenEncrypted = refreshEnc
	conn.TokenExpiresAt = &expiresAt
	repo.add(conn)

	tm := newTokenManager(repo, enc, nil)
	token, err := tm.ensureAccessToken(context.Background(), conn, newUpstreamClient(server.URL, server.Client()))
	require.NoError(t, err)
	require.Equal(t, "access-login", token)
	require.Len(t, repo.updateTokensCalls, 1)
	persisted, err := enc.Decrypt(repo.updateTokensCalls[0].accessEnc)
	require.NoError(t, err)
	require.Equal(t, "access-login", persisted)
}

func TestEnsureAccessToken_RefreshIsSerialized(t *testing.T) {
	enc := stubEncryptor{}
	repo := newStubConnectionRepo()

	var mu sync.Mutex
	refreshCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/auth/refresh", r.URL.Path)
		mu.Lock()
		refreshCalls++
		mu.Unlock()
		// 拉长处理窗口，放大并发竞争。
		time.Sleep(20 * time.Millisecond)
		writeJSON(t, w, http.StatusOK, successEnvelope(tokenPairData("access-new", "refresh-new", 86400)))
	}))
	defer server.Close()

	conn := passwordConnection(t, enc, 1, server.URL)
	refreshEnc, err := enc.Encrypt("refresh-old")
	require.NoError(t, err)
	expiresAt := time.Now().Add(-time.Hour)
	conn.RefreshTokenEncrypted = refreshEnc
	conn.TokenExpiresAt = &expiresAt
	repo.add(conn)

	tm := newTokenManager(repo, enc, nil)
	client := newUpstreamClient(server.URL, server.Client())

	const workers = 8
	var wg sync.WaitGroup
	tokens := make([]string, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			token, err := tm.ensureAccessToken(context.Background(), conn, client)
			tokens[i] = token
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i := range errs {
		require.NoError(t, errs[i])
		require.Equal(t, "access-new", tokens[i])
	}
	// 连接级互斥 + 持锁重读：并发下只允许一次 refresh 轮转。
	require.Equal(t, 1, refreshCalls)
	require.Len(t, repo.updateTokensCalls, 1)
}

func TestPersistTokens_ClearsRefreshWhenAbsent(t *testing.T) {
	enc := stubEncryptor{}
	repo := newStubConnectionRepo()
	conn := passwordConnection(t, enc, 1, "https://upstream.example.com")
	repo.add(conn)

	tm := newTokenManager(repo, enc, nil)
	token, err := tm.persistTokens(context.Background(), 1, &tokenPair{AccessToken: "access-only", ExpiresIn: 3600})
	require.NoError(t, err)
	require.Equal(t, "access-only", token)
	require.Len(t, repo.updateTokensCalls, 1)
	require.Nil(t, repo.updateTokensCalls[0].refreshEnc, "无新 refresh token 时应清除旧值")
}

func TestEnsureAccessToken_UnsupportedAuthMode(t *testing.T) {
	tm := newTokenManager(newStubConnectionRepo(), stubEncryptor{}, nil)
	_, err := tm.ensureAccessToken(context.Background(), &Connection{ID: 9, AuthMode: "oauth"}, nil)
	require.Error(t, err)
}
