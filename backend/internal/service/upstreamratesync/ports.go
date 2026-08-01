// Package upstreamratesync 定义“上游倍率同步”能力的领域类型与端口（ports）接口。
//
// 本包只包含类型、常量、错误与接口定义，不含同步引擎实现。
// 同步引擎（client / syncer / service / runner）在后续任务中实现；
// repository 层（internal/repository）实现本包声明的
// ConnectionRepository 与 SyncRunRepository 接口。
//
// 设计来源：openspec add-upstream-rate-sync（design.md）。
package upstreamratesync

import (
	"context"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// AuthMode 连接鉴权模式。
const (
	// AuthModePassword 账密自动化登录 + refresh 一次性轮转。
	AuthModePassword = "password"
	// AuthModeToken 管理员手动粘贴 access token（无自动刷新，到期需重新粘贴）。
	AuthModeToken = "token"
)

// SyncStatus 同步结果状态（连接 last_status 与 run status 共用取值）。
const (
	SyncStatusSuccess = "success"
	SyncStatusPartial = "partial"
	SyncStatusFailed  = "failed"
)

// DetailAction 单账号明细动作（run details[].action 取值）。
const (
	DetailActionUpdated          = "updated"
	DetailActionUnchanged        = "unchanged"
	DetailActionUnmatched        = "unmatched"
	DetailActionThresholdSkipped = "threshold_skipped"
	DetailActionManualOverride   = "manual_override"
)

// 同步间隔边界（分钟），仿 upstream billing probe 的 5–1440。
const (
	MinIntervalMinutes     = 5
	MaxIntervalMinutes     = 1440
	DefaultIntervalMinutes = 30
)

// run 保留策略：每连接保留最近 KeepRunsPerConnection 条且 RunRetentionDays 天内的 run，
// 超出部分由 SyncRunRepository.Prune 分批物理删除。
const (
	RunRetentionDays      = 30
	KeepRunsPerConnection = 200
)

// 分组计价模式（groups.billing_mode，迁移 192）。
const (
	// GroupBillingModeGroupMultiplier 默认：按分组统一倍率计费（升级前行为）。
	GroupBillingModeGroupMultiplier = "group_multiplier"
	// GroupBillingModeAccountUpstream 按账号级上游同步倍率计费，分组倍率降级为兜底。
	GroupBillingModeAccountUpstream = "account_upstream"
)

// 业务错误（统一在此声明，避免散落）。
var (
	ErrConnectionNotFound = infraerrors.NotFound(
		"UPSTREAM_CONNECTION_NOT_FOUND", "upstream connection not found",
	)
	ErrConnectionConflict = infraerrors.Conflict(
		"UPSTREAM_CONNECTION_CONFLICT", "upstream connection with the same base_url already exists",
	)
	ErrSyncRunNotFound = infraerrors.NotFound(
		"UPSTREAM_SYNC_RUN_NOT_FOUND", "upstream sync run not found",
	)
	// ErrSyncInProgress 同一连接已有同步在进行（连接级互斥）。
	ErrSyncInProgress = infraerrors.Conflict(
		"UPSTREAM_SYNC_IN_PROGRESS", "a sync for this connection is already in progress",
	)
	// ErrUpstreamUnauthorized 上游返回 401：access token 失效（password 模式触发
	// refresh/re-login，token 模式提示管理员重新粘贴）。
	ErrUpstreamUnauthorized = infraerrors.Unauthorized(
		"UPSTREAM_UNAUTHORIZED", "upstream rejected the access token",
	)
	// ErrUpstreamTurnstile 上游开启 Turnstile，账密自动化失效（死穴）。
	// 前端按 reason 包含 "turnstile"（不区分大小写）识别并提示降级为 token 模式。
	ErrUpstreamTurnstile = infraerrors.BadRequest(
		"UPSTREAM_TURNSTILE_REQUIRED", "upstream requires Turnstile verification; switch the connection to token auth mode",
	)
	// ErrUpstreamTOTPRequired 上游账号开启 TOTP，账密自动化登录不可用。
	ErrUpstreamTOTPRequired = infraerrors.BadRequest(
		"UPSTREAM_TOTP_REQUIRED", "upstream account has TOTP enabled; disable TOTP or switch to token auth mode",
	)
	// ErrUpstreamUnreachable 上游网络不可达（DNS/连接/超时等传输层失败）。
	ErrUpstreamUnreachable = infraerrors.ServiceUnavailable(
		"UPSTREAM_UNREACHABLE", "upstream is unreachable",
	)
)

// LastSyncedRateExtraKey 是 accounts.extra 中记录"本服务上次写入的倍率"的键，
// 用于写回前的三方比对（防覆盖管理员手改）。
const LastSyncedRateExtraKey = "last_synced_rate"

// SecretEncryptor 凭证加解密端口（生产实现为 repository 的 AES-256-GCM
// encryptor，通过构造函数注入，禁止全局取用）。
type SecretEncryptor interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
}

// ScopedAccount 是同步引擎所需的本地账号最小视图（base_url 作用域内）。
type ScopedAccount struct {
	ID             int64
	APIKey         string   // credentials.api_key 明文（账号凭证本身非加密存储）
	RateMultiplier *float64 // 当前 accounts.rate_multiplier（nil 语义为 1.0）
	// LastSyncedRate 是 extra.last_synced_rate：本服务上次写入的值。
	// 非 nil 且与当前 RateMultiplier 不一致 → 管理员手改过（manual_override）。
	LastSyncedRate *float64
}

// AccountSnapshot 是待写入 accounts.extra.upstream_billing_probe 的同构快照
// （结构与 service.UpstreamBillingProbeSnapshot status="ok" 完全一致）。
type AccountSnapshot struct {
	Data        map[string]any
	ReceivedAt  time.Time
	FreshUntil  time.Time // = ReceivedAt + 2×interval
	NextProbeAt time.Time // = ReceivedAt + interval
}

// AccountGateway 本地账号读/写回端口，由 service 层适配器实现
// （复用 account repo 的 BulkUpdate 与 UpdateUpstreamBillingProbeSnapshot 路径）。
//
// 写回语义：WriteSyncedRate 在护栏判定通过后调用，写入
// accounts.rate_multiplier + extra.last_synced_rate + extra.upstream_billing_probe
// 同构快照；单账号失败由引擎记为 partial，不整体失败。
type AccountGateway interface {
	// ListScopedAccounts 返回 credentials.base_url 归一化后等于 scopeBaseURL
	// 的 OpenAI apikey 账号最小视图（scopeBaseURL 由调用方归一化）。
	ListScopedAccounts(ctx context.Context, scopeBaseURL string) ([]ScopedAccount, error)
	// WriteSyncedRate 写回倍率 + last_synced_rate + 同构快照。
	WriteSyncedRate(ctx context.Context, accountID int64, rate float64, snapshot *AccountSnapshot) error
}

// TestResult 连接测试结果（对应管理 API 的 keys_found/accounts_matched）。
type TestResult struct {
	KeysFound       int      `json:"keys_found"`
	AccountsMatched int      `json:"accounts_matched"`
	Balance         *float64 `json:"balance,omitempty"` // 上游账号余额（USD），获取失败为 nil
}

// Connection 上游连接：保存一个上游 sub2api 实例的地址与鉴权（密文）。
//
// 安全边界：CredentialsEncrypted / AccessTokenEncrypted / RefreshTokenEncrypted
// 只存 SecretEncryptor（AES-256-GCM）密文；任何日志、API 响应、同步明细
// 不得输出明文或密文，对外只暴露 has_credentials / has_token 布尔。
type Connection struct {
	ID       int64
	Name     string
	BaseURL  string // 归一化后（小写 scheme/host、去尾斜杠、去默认端口）全局唯一
	AuthMode string // password | token

	CredentialsEncrypted  string // 密文；password 模式明文为 JSON {"email","password"}，token 模式明文即 access token 字符串；空表示未配置
	AccessTokenEncrypted  string // 当前 access token 密文
	RefreshTokenEncrypted string // 当前 refresh token 密文（password 模式）
	TokenExpiresAt        *time.Time

	Enabled         bool
	IntervalMinutes int

	LastSyncAt *time.Time
	LastStatus string // success | partial | failed，空表示尚未同步
	LastError  string // 最近一次错误摘要（脱敏，不含 token/密码）

	LastBalance *float64 // 最近一次同步读取到的上游账号余额（USD），nil 表示未获取过

	CreatedAt time.Time
	UpdatedAt time.Time
}

// HasCredentials 是否已配置凭证（脱敏视图用）。
func (c *Connection) HasCredentials() bool { return c.CredentialsEncrypted != "" }

// HasAccessToken 是否已持有 access token（脱敏视图用）。
func (c *Connection) HasAccessToken() bool { return c.AccessTokenEncrypted != "" }

// SyncRunDetail 单账号同步明细（details JSONB 数组元素）。
// KeyPrefix 只允许 sk- 前 8 位级别标识，不得记录完整 key。
type SyncRunDetail struct {
	AccountID int64    `json:"account_id"`
	KeyPrefix string   `json:"key_prefix"`
	GroupName string   `json:"group_name"`
	OldRate   *float64 `json:"old_rate"`
	NewRate   *float64 `json:"new_rate"`
	Action    string   `json:"action"` // updated|unchanged|unmatched|threshold_skipped|manual_override
}

// SyncRun 一次同步运行的记录（五个计数 + JSONB 明细）。
type SyncRun struct {
	ID           int64
	ConnectionID int64
	StartedAt    time.Time
	FinishedAt   *time.Time // nil 表示进行中
	Status       string     // success | partial | failed

	KeysFetched       int
	AccountsMatched   int
	AccountsUpdated   int
	AccountsUnchanged int
	AccountsUnmatched int

	Details []SyncRunDetail
	Error   string // 失败原因摘要（脱敏）
}

// ConnectionListParams 连接列表查询参数（管理 API）。
type ConnectionListParams struct {
	Page     int
	PageSize int
}

// SyncRunListParams 同步日志查询参数：按连接/状态/时间范围筛选，分页。
type SyncRunListParams struct {
	ConnectionID *int64
	Status       string
	StartedFrom  *time.Time // started_at >= StartedFrom
	StartedTo    *time.Time // started_at <= StartedTo
	Page         int
	PageSize     int
}

// ConnectionRepository 上游连接仓储端口。
//
// 凭证字段只接受密文；base_url 由调用方归一化后传入，唯一冲突返回
// ErrConnectionConflict。
type ConnectionRepository interface {
	Create(ctx context.Context, conn *Connection) error
	GetByID(ctx context.Context, id int64) (*Connection, error)
	Update(ctx context.Context, conn *Connection) error
	Delete(ctx context.Context, id int64) error
	// List 按 id 倒序分页返回连接列表与总数。
	List(ctx context.Context, params ConnectionListParams) ([]*Connection, int64, error)
	// ListEnabled 返回全部 enabled=true 的连接（runner 按 interval 判定 due）。
	ListEnabled(ctx context.Context) ([]*Connection, error)
	// UpdateSyncResult 同步结束后更新 last_sync_at / last_status / last_error（脱敏摘要，空串清除）。
	UpdateSyncResult(ctx context.Context, id int64, syncedAt time.Time, status string, lastError string) error
	// UpdateBalance 更新上游余额快照（nil 表示清除/获取失败时保持旧值由调用方决定）。
	UpdateBalance(ctx context.Context, id int64, balance float64) error
	// UpdateTokens refresh 轮转成功后持久化最新 token：access token 密文 + 到期时间；
	// refreshTokenEncrypted 为 nil 表示清除 refresh token（refresh 严格一次性轮转，
	// 必须串行刷新并立即持久化，禁止并发刷新导致新 token 丢失）。
	UpdateTokens(ctx context.Context, id int64, accessTokenEncrypted string, refreshTokenEncrypted *string, tokenExpiresAt time.Time) error
}

// SyncRunRepository 同步日志仓储端口。
type SyncRunRepository interface {
	// Create 创建一条 run（started_at 缺省取当前时间，status 缺省 success，
	// details 缺省空数组），生成后回写 run.ID / run.StartedAt。
	Create(ctx context.Context, run *SyncRun) error
	// Finish 结束 run：按 run.ID 更新 status/finished_at/五个计数/details/error。
	Finish(ctx context.Context, run *SyncRun) error
	GetByID(ctx context.Context, id int64) (*SyncRun, error)
	// List 按连接/状态/时间范围筛选，按 started_at、id 倒序分页。
	List(ctx context.Context, params SyncRunListParams) ([]*SyncRun, int64, error)
	// Prune 清理超保留期的 run：每连接仅保留最近 keepPerConnection 条，
	// 且删除 started_at 早于 retentionDays  cutoff 的记录；分批物理删除，
	// 返回累计删除行数。
	Prune(ctx context.Context, retentionDays int, keepPerConnection int) (int64, error)
}
