package upstreamratesync

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// accessTokenTTL 是上游 access token 的标称有效期（24h，见 design Decisions 5）。
// 临期判定：剩余有效期 < 标称的 10%（或已过期）即触发轮转。
const accessTokenTTL = 24 * time.Hour

// passwordCredentials 是 password 模式 credentials_encrypted 解密后的明文格式。
// 该 JSON 约定同时被管理 API（创建/更新连接时加密写入）遵守。
type passwordCredentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// tokenManager 管理单连接的 JWT 生命周期：
//   - token 模式：直接使用手贴 access token，不自动刷新；
//   - password 模式：临期先 refresh（严格一次性轮转，成功后先持久化再使用），
//     refresh 失败则重新 login；同一连接的刷新经连接级 mutex 串行。
//
// 任何日志/错误不落明文凭证与 token。
type tokenManager struct {
	connRepo  ConnectionRepository
	encryptor SecretEncryptor
	now       func() time.Time

	mu    sync.Mutex
	locks map[int64]*sync.Mutex
}

func newTokenManager(connRepo ConnectionRepository, encryptor SecretEncryptor, now func() time.Time) *tokenManager {
	if now == nil {
		now = time.Now
	}
	return &tokenManager{
		connRepo:  connRepo,
		encryptor: encryptor,
		now:       now,
		locks:     make(map[int64]*sync.Mutex),
	}
}

func (m *tokenManager) connLock(connectionID int64) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lock, ok := m.locks[connectionID]
	if !ok {
		lock = &sync.Mutex{}
		m.locks[connectionID] = lock
	}
	return lock
}

// ensureAccessToken 返回可用的 access token（明文仅存内存）。
// password 模式在临期/过期时自动 refresh 或 re-login 并持久化新 token 对。
func (m *tokenManager) ensureAccessToken(ctx context.Context, conn *Connection, client *upstreamClient) (string, error) {
	switch conn.AuthMode {
	case AuthModeToken:
		if conn.CredentialsEncrypted == "" {
			return "", fmt.Errorf("upstream connection %d: token credentials not configured", conn.ID)
		}
		return m.decrypt(conn.CredentialsEncrypted)
	case AuthModePassword:
		if conn.AccessTokenEncrypted != "" && !m.tokenExpiringSoon(conn) {
			return m.decrypt(conn.AccessTokenEncrypted)
		}
		return m.refreshOrLogin(ctx, conn, client, false)
	default:
		return "", fmt.Errorf("upstream connection %d: unsupported auth_mode %q", conn.ID, conn.AuthMode)
	}
}

// forceLogin 跳过 refresh 直接重新登录（上游 401 后的兜底重试用），同样串行。
func (m *tokenManager) forceLogin(ctx context.Context, conn *Connection, client *upstreamClient) (string, error) {
	if conn.AuthMode != AuthModePassword {
		return "", ErrUpstreamUnauthorized
	}
	return m.refreshOrLogin(ctx, conn, client, true)
}

// refreshOrLogin 在连接级互斥下完成 refresh → login 链路。
// forceReLogin 为 true 时跳过 refresh 直接 login。
func (m *tokenManager) refreshOrLogin(ctx context.Context, conn *Connection, client *upstreamClient, forceReLogin bool) (string, error) {
	lock := m.connLock(conn.ID)
	lock.Lock()
	defer lock.Unlock()

	// double-check：持锁后重读连接，其他 goroutine 可能已完成轮转。
	fresh, err := m.connRepo.GetByID(ctx, conn.ID)
	if err != nil {
		return "", err
	}
	if !forceReLogin && fresh.AccessTokenEncrypted != "" && !m.tokenExpiringSoon(fresh) {
		return m.decrypt(fresh.AccessTokenEncrypted)
	}

	if !forceReLogin && fresh.RefreshTokenEncrypted != "" {
		refreshToken, decErr := m.decrypt(fresh.RefreshTokenEncrypted)
		if decErr != nil {
			return "", fmt.Errorf("upstream connection %d: decrypt refresh token: %w", conn.ID, decErr)
		}
		pair, refreshErr := client.refresh(ctx, refreshToken)
		if refreshErr == nil {
			return m.persistTokens(ctx, conn.ID, pair)
		}
		// refresh 失败（含 refresh token 已失效）→ 降级重新 login。
	}
	return m.login(ctx, conn.ID, fresh, client)
}

func (m *tokenManager) login(ctx context.Context, connectionID int64, conn *Connection, client *upstreamClient) (string, error) {
	if conn.CredentialsEncrypted == "" {
		return "", fmt.Errorf("upstream connection %d: password credentials not configured", connectionID)
	}
	plain, err := m.decrypt(conn.CredentialsEncrypted)
	if err != nil {
		return "", fmt.Errorf("upstream connection %d: decrypt credentials: %w", connectionID, err)
	}
	var creds passwordCredentials
	if err := json.Unmarshal([]byte(plain), &creds); err != nil || creds.Email == "" || creds.Password == "" {
		return "", fmt.Errorf("upstream connection %d: credentials payload has unexpected shape", connectionID)
	}
	pair, err := client.login(ctx, creds.Email, creds.Password)
	if err != nil {
		return "", err
	}
	return m.persistTokens(ctx, connectionID, pair)
}

// persistTokens 严格一次性轮转：先加密并持久化最新 token 对，成功后再放行使用，
// 禁止并发刷新导致旧 refresh 已失效的新 token 丢失（自残）。
func (m *tokenManager) persistTokens(ctx context.Context, connectionID int64, pair *tokenPair) (string, error) {
	accessEnc, err := m.encryptor.Encrypt(pair.AccessToken)
	if err != nil {
		return "", fmt.Errorf("upstream connection %d: encrypt access token: %w", connectionID, err)
	}
	var refreshEnc *string
	if pair.RefreshToken != "" {
		value, err := m.encryptor.Encrypt(pair.RefreshToken)
		if err != nil {
			return "", fmt.Errorf("upstream connection %d: encrypt refresh token: %w", connectionID, err)
		}
		refreshEnc = &value
	}
	expiresIn := pair.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = int(accessTokenTTL / time.Second)
	}
	expiresAt := m.now().Add(time.Duration(expiresIn) * time.Second)
	if err := m.connRepo.UpdateTokens(ctx, connectionID, accessEnc, refreshEnc, expiresAt); err != nil {
		return "", fmt.Errorf("upstream connection %d: persist rotated tokens: %w", connectionID, err)
	}
	return pair.AccessToken, nil
}

// tokenExpiringSoon 剩余有效期 < 标称 10%（或已过期、无过期记录）视为临期。
func (m *tokenManager) tokenExpiringSoon(conn *Connection) bool {
	if conn.TokenExpiresAt == nil {
		return true
	}
	remaining := conn.TokenExpiresAt.Sub(m.now())
	return remaining <= 0 || remaining < accessTokenTTL/10
}

func (m *tokenManager) decrypt(ciphertext string) (string, error) {
	plain, err := m.encryptor.Decrypt(ciphertext)
	if err != nil {
		return "", fmt.Errorf("decrypt upstream secret: %w", err)
	}
	return plain, nil
}
