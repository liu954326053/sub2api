package upstreamratesync

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const syncLogPrefix = "service.upstream_rate_sync"

// 写回护栏阈值：单次跳变 |new-old|/old > 0.5 跳过写回（threshold_skipped）。
// old 为 0（或 nil 折算前值）时按绝对阈值处理：|new-old| > 0.5 同样跳过。
const thresholdRelativeJump = 0.5

// maxRunDetails 单 run 明细条数上限（design Open Questions 1：超出仅计数不记明细）。
const maxRunDetails = 5000

// Syncer 同步引擎：拉取上游 keys → 精确匹配本地账号 → 护栏写回 + 同构快照
// + run 记录。公共入口 SyncConnection / TestConnection 供后台 runner 与
// 管理 API（立即同步/连接测试）复用。
type Syncer struct {
	connRepo   ConnectionRepository
	runRepo    SyncRunRepository
	accounts   AccountGateway
	tokens     *tokenManager
	httpClient *http.Client
	now        func() time.Time

	mu        sync.Mutex
	connLocks map[int64]*sync.Mutex
}

func NewSyncer(
	connRepo ConnectionRepository,
	runRepo SyncRunRepository,
	accounts AccountGateway,
	encryptor SecretEncryptor,
) *Syncer {
	return &Syncer{
		connRepo:   connRepo,
		runRepo:    runRepo,
		accounts:   accounts,
		tokens:     newTokenManager(connRepo, encryptor, nil),
		httpClient: &http.Client{Timeout: upstreamRequestTimeout},
		now:        time.Now,
		connLocks:  make(map[int64]*sync.Mutex),
	}
}

// SetHTTPClient 注入自定义 HTTP client（测试用 httptest）。
func (s *Syncer) SetHTTPClient(client *http.Client) {
	if client != nil {
		s.httpClient = client
	}
}

// SetNow 注入时钟（测试用）。
func (s *Syncer) SetNow(now func() time.Time) {
	if now != nil {
		s.now = now
		s.tokens.now = now
	}
}

func (s *Syncer) connLock(connectionID int64) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, ok := s.connLocks[connectionID]
	if !ok {
		lock = &sync.Mutex{}
		s.connLocks[connectionID] = lock
	}
	return lock
}

// SyncConnection 执行一次完整同步：拉全量 keys → 匹配 → 护栏写回 → run 落库
// + 连接 last_sync_at/last_status/last_error 更新。
func (s *Syncer) SyncConnection(ctx context.Context, connectionID int64) (*SyncRun, error) {
	conn, err := s.connRepo.GetByID(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	lock := s.connLock(connectionID)
	if !lock.TryLock() {
		return nil, ErrSyncInProgress
	}
	defer lock.Unlock()

	run := &SyncRun{
		ConnectionID: connectionID,
		StartedAt:    s.now().UTC(),
		Status:       SyncStatusSuccess,
		Details:      []SyncRunDetail{},
	}
	if err := s.runRepo.Create(ctx, run); err != nil {
		return nil, fmt.Errorf("create upstream sync run: %w", err)
	}

	status, runErr := s.sync(ctx, conn, run)
	run.Status = status
	if runErr != nil {
		run.Error = sanitizeRunError(runErr)
	}
	finishedAt := s.now().UTC()
	run.FinishedAt = &finishedAt
	if err := s.runRepo.Finish(ctx, run); err != nil {
		logger.LegacyPrintf(syncLogPrefix, "finish_run_failed: connection_id=%d run_id=%d err=%v", connectionID, run.ID, err)
	}
	if err := s.connRepo.UpdateSyncResult(ctx, connectionID, finishedAt, run.Status, run.Error); err != nil {
		logger.LegacyPrintf(syncLogPrefix, "update_sync_result_failed: connection_id=%d err=%v", connectionID, err)
	}
	logger.LegacyPrintf(syncLogPrefix,
		"sync_finished: connection_id=%d status=%s keys=%d matched=%d updated=%d unchanged=%d unmatched=%d",
		connectionID, run.Status, run.KeysFetched, run.AccountsMatched, run.AccountsUpdated, run.AccountsUnchanged, run.AccountsUnmatched)
	return run, runErr
}

// sync 同步主体；返回 run 状态与整体错误（拉取失败 → failed）。
func (s *Syncer) sync(ctx context.Context, conn *Connection, run *SyncRun) (string, error) {
	client := newUpstreamClient(conn.BaseURL, s.httpClient)

	var keys []upstreamKey
	err := s.withReauth(ctx, conn, client, func(accessToken string) error {
		fetched, err := client.listAllKeys(ctx, accessToken)
		if err != nil {
			return err
		}
		keys = fetched
		return nil
	})
	if err != nil {
		logger.LegacyPrintf(syncLogPrefix, "fetch_keys_failed: connection_id=%d err=%v", conn.ID, err)
		return SyncStatusFailed, err
	}
	run.KeysFetched = len(keys)

	scope, err := NormalizeBaseURL(conn.BaseURL)
	if err != nil {
		return SyncStatusFailed, fmt.Errorf("normalize connection base_url: %w", err)
	}
	accounts, err := s.accounts.ListScopedAccounts(ctx, scope)
	if err != nil {
		return SyncStatusFailed, fmt.Errorf("list scoped accounts: %w", err)
	}

	byAPIKey := make(map[string][]int, len(accounts))
	for i := range accounts {
		if accounts[i].APIKey == "" {
			continue
		}
		byAPIKey[accounts[i].APIKey] = append(byAPIKey[accounts[i].APIKey], i)
	}

	interval := time.Duration(conn.IntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = DefaultIntervalMinutes * time.Minute
	}
	matchedAccount := make(map[int64]bool, len(accounts))
	partial := false

	for i := range keys {
		key := &keys[i]
		if key.Key == "" || key.Group == nil || !validSyncedRate(key.Group.RateMultiplier) {
			continue
		}
		for _, accountIdx := range byAPIKey[key.Key] {
			account := &accounts[accountIdx]
			matchedAccount[account.ID] = true
			run.AccountsMatched++
			detail, writeErr := s.applyWriteback(ctx, conn, account, key, interval)
			if detail != nil {
				appendRunDetail(run, *detail)
				switch detail.Action {
				case DetailActionUpdated:
					run.AccountsUpdated++
				case DetailActionUnchanged:
					run.AccountsUnchanged++
				case DetailActionThresholdSkipped, DetailActionManualOverride:
					partial = true
				}
			}
			if writeErr != nil {
				partial = true
				appendRunError(run, fmt.Sprintf("account %d: write_failed", account.ID))
				logger.LegacyPrintf(syncLogPrefix, "writeback_failed: connection_id=%d account_id=%d err=%v", conn.ID, account.ID, writeErr)
			}
		}
	}

	// 本地有账号、上游无对应 key → unmatched，MUST NOT 清值（design Decisions 7）。
	for i := range accounts {
		account := &accounts[i]
		if account.APIKey == "" || matchedAccount[account.ID] {
			continue
		}
		run.AccountsUnmatched++
		appendRunDetail(run, SyncRunDetail{
			AccountID: account.ID,
			KeyPrefix: keyPrefix(account.APIKey),
			OldRate:   account.RateMultiplier,
			NewRate:   nil,
			Action:    DetailActionUnmatched,
		})
	}

	if partial {
		return SyncStatusPartial, nil
	}
	return SyncStatusSuccess, nil
}

// applyWriteback 单账号护栏写回（design Decisions 7）：
// 值不变跳过 → 三方比对防手改覆盖 → 跳变 >50% 跳过 → 写 rate + 同构快照 + last_synced_rate。
func (s *Syncer) applyWriteback(ctx context.Context, conn *Connection, account *ScopedAccount, key *upstreamKey, interval time.Duration) (*SyncRunDetail, error) {
	newRate := key.Group.RateMultiplier
	oldEffective := effectiveSyncedRate(account.RateMultiplier)
	detail := SyncRunDetail{
		AccountID: account.ID,
		KeyPrefix: keyPrefix(key.Key),
		GroupName: key.Group.Name,
		OldRate:   account.RateMultiplier,
		NewRate:   &newRate,
	}

	// 三方比对：当前值 ≠ last_synced_rate 且 last_synced_rate 存在 → 管理员手改过，保留手改值。
	if account.LastSyncedRate != nil && !equalSyncedRate(oldEffective, *account.LastSyncedRate) {
		detail.Action = DetailActionManualOverride
		logger.LegacyPrintf(syncLogPrefix, "manual_override_skip: connection_id=%d account_id=%d", conn.ID, account.ID)
		return &detail, nil
	}

	// 值不变跳过（nil→1.0、负数→1.0 折算后比较）。
	// 例外：快照缺失（曾被账号编辑的身份失效清除）时自愈重写一次快照，
	// 倍率不变、不计入 updated，避免账号永远停留在"未探测"。
	if equalSyncedRate(oldEffective, newRate) {
		if !account.HasSnapshot {
			snapshot := buildAccountSnapshot(key.Group, s.now(), interval)
			if err := s.accounts.WriteSyncedRate(ctx, account.ID, newRate, snapshot); err != nil {
				return nil, err
			}
		}
		detail.Action = DetailActionUnchanged
		return &detail, nil
	}

	// 单次跳变 >50% 跳过；old 折算后为 0 时按绝对阈值处理。
	// 首次同步（LastSyncedRate 为 nil）不做跳变判断：此时当前值只是默认值 1.0
	// 而非真实上游观测值，默认值与真实倍率的正常差距会被阈值误杀。
	jump := math.Abs(newRate - oldEffective)
	if account.LastSyncedRate != nil &&
		((oldEffective > 0 && jump/oldEffective > thresholdRelativeJump) || (oldEffective <= 0 && jump > thresholdRelativeJump)) {
		detail.Action = DetailActionThresholdSkipped
		logger.LegacyPrintf(syncLogPrefix, "threshold_skip: connection_id=%d account_id=%d old=%.4f new=%.4f", conn.ID, account.ID, oldEffective, newRate)
		return &detail, nil
	}

	snapshot := buildAccountSnapshot(key.Group, s.now(), interval)
	if err := s.accounts.WriteSyncedRate(ctx, account.ID, newRate, snapshot); err != nil {
		return nil, err
	}
	detail.Action = DetailActionUpdated
	return &detail, nil
}

// TestConnection 连接测试：登录（或校验 token）+ 拉一页 keys，返回发现数与
// 预计匹配数。不触发任何写回、不产出 run、不更新 token 之外的状态。
func (s *Syncer) TestConnection(ctx context.Context, connectionID int64) (*TestResult, error) {
	conn, err := s.connRepo.GetByID(ctx, connectionID)
	if err != nil {
		return nil, err
	}
	lock := s.connLock(connectionID)
	if !lock.TryLock() {
		return nil, ErrSyncInProgress
	}
	defer lock.Unlock()

	client := newUpstreamClient(conn.BaseURL, s.httpClient)
	var page *upstreamKeysPage
	err = s.withReauth(ctx, conn, client, func(accessToken string) error {
		data, err := client.listKeysPage(ctx, accessToken, 1)
		if err != nil {
			return err
		}
		page = data
		return nil
	})
	if err != nil {
		logger.LegacyPrintf(syncLogPrefix, "test_connection_failed: connection_id=%d err=%v", connectionID, err)
		return nil, publicSyncError(err)
	}

	result := &TestResult{KeysFound: int(page.Total)}
	if result.KeysFound == 0 {
		result.KeysFound = len(page.Items)
	}

	scope, err := NormalizeBaseURL(conn.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("normalize connection base_url: %w", err)
	}
	accounts, err := s.accounts.ListScopedAccounts(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("list scoped accounts: %w", err)
	}
	firstPageKeys := make(map[string]struct{}, len(page.Items))
	for i := range page.Items {
		firstPageKeys[page.Items[i].Key] = struct{}{}
	}
	for i := range accounts {
		if accounts[i].APIKey == "" {
			continue
		}
		if _, ok := firstPageKeys[accounts[i].APIKey]; ok {
			result.AccountsMatched++
		}
	}
	return result, nil
}

// withReauth 执行需要 access token 的操作；password 模式遇上游 401 时
// 强制重新 login 后重试一次（token 模式无自动恢复路径，直接报错）。
func (s *Syncer) withReauth(ctx context.Context, conn *Connection, client *upstreamClient, fn func(accessToken string) error) error {
	accessToken, err := s.tokens.ensureAccessToken(ctx, conn, client)
	if err != nil {
		return err
	}
	if err := fn(accessToken); err != nil {
		if errors.Is(err, ErrUpstreamUnauthorized) && conn.AuthMode == AuthModePassword {
			logger.LegacyPrintf(syncLogPrefix, "relogin_after_unauthorized: connection_id=%d", conn.ID)
			accessToken, loginErr := s.tokens.forceLogin(ctx, conn, client)
			if loginErr != nil {
				return loginErr
			}
			return fn(accessToken)
		}
		return err
	}
	return nil
}

// NormalizeBaseURL 归一化 base_url：小写 scheme/host、去默认端口、去尾斜杠。
// 保存连接与账号作用域判定共用同一实现，保证多连接严格按作用域隔离。
func NormalizeBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("base_url is empty")
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse base_url: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", fmt.Errorf("base_url missing host")
	}
	port := parsed.Port()
	if port != "" && !((scheme == "http" && port == "80") || (scheme == "https" && port == "443")) {
		host += ":" + port
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	return scheme + "://" + host + path, nil
}

// effectiveSyncedRate 账号倍率折算：nil→1.0、负数→1.0（BillingRateMultiplier 语义）。
func effectiveSyncedRate(rate *float64) float64 {
	if rate == nil || *rate < 0 {
		return 1.0
	}
	return *rate
}

func validSyncedRate(rate float64) bool {
	return rate >= 0 && !math.IsNaN(rate) && !math.IsInf(rate, 0)
}

// equalSyncedRate 与 probe 的 equalBillingMultiplier 同款相对容差比较。
func equalSyncedRate(left, right float64) bool {
	if math.IsNaN(left) || math.IsNaN(right) || math.IsInf(left, 0) || math.IsInf(right, 0) {
		return false
	}
	scale := math.Max(1, math.Max(math.Abs(left), math.Abs(right)))
	return math.Abs(left-right) <= 1e-9*scale
}

// keyPrefix 只保留 key 前 8 位级别标识，完整 key 不得落入明细/日志。
func keyPrefix(key string) string {
	if len(key) > 8 {
		return key[:8]
	}
	return key
}

// publicSyncError 把引擎内部错误翻译为对外可映射的 sentinel 业务错误，
// 保证 handler 的 response.ErrorFrom 能落到正确的 HTTP 状态码而非 500。
func publicSyncError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrUpstreamUnreachable):
		return ErrUpstreamUnreachable
	case errors.Is(err, ErrUpstreamTurnstile):
		return ErrUpstreamTurnstile
	case errors.Is(err, ErrUpstreamTOTPRequired):
		return ErrUpstreamTOTPRequired
	case errors.Is(err, ErrUpstreamUnauthorized):
		return ErrUpstreamUnauthorized
	default:
		return err
	}
}

// sanitizeRunError run 错误摘要（脱敏）：上游错误只留 reason，其余归一类。
func sanitizeRunError(err error) string {
	if err == nil {
		return ""
	}
	var upErr *UpstreamError
	if errors.As(err, &upErr) {
		if upErr.Turnstile {
			return "upstream_turnstile_required"
		}
		if upErr.StatusCode == http.StatusUnauthorized {
			return "upstream_unauthorized"
		}
		return "upstream_" + strings.ToLower(upErr.Reason)
	}
	return "sync_failed"
}

// appendRunError 聚合部分失败摘要（脱敏，不含 token/凭证/完整 key）。
func appendRunError(run *SyncRun, message string) {
	if run.Error == "" {
		run.Error = message
		return
	}
	// 摘要上限，避免极端情况下 run error 膨胀。
	if len(run.Error) >= 512 {
		return
	}
	run.Error += "; " + message
}

// appendRunDetail 追加明细，超出 maxRunDetails 截断（仅计数不记明细）。
func appendRunDetail(run *SyncRun, detail SyncRunDetail) {
	if len(run.Details) >= maxRunDetails {
		return
	}
	run.Details = append(run.Details, detail)
}
