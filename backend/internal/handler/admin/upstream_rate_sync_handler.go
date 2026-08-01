package admin

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/service/upstreamratesync"

	"github.com/gin-gonic/gin"
)

var (
	errUpstreamRateSyncCredentialsIncomplete = infraerrors.BadRequest(
		"UPSTREAM_RATE_SYNC_CREDENTIALS_INCOMPLETE", "credentials are incomplete for the selected auth_mode",
	)
	errUpstreamRateSyncInvalidAuthMode = infraerrors.BadRequest(
		"UPSTREAM_RATE_SYNC_INVALID_AUTH_MODE", "auth_mode must be password or token",
	)
)

// UpstreamRateSyncEngine 同步引擎端口（*upstreamratesync.Syncer 实现）。
// 独立成接口以便 handler 测试注入 fake。
type UpstreamRateSyncEngine interface {
	TestConnection(ctx context.Context, connectionID int64) (*upstreamratesync.TestResult, error)
	SyncConnection(ctx context.Context, connectionID int64) (*upstreamratesync.SyncRun, error)
}

// UpstreamRateSyncHandler 上游倍率同步管理后台 handler（openspec add-upstream-rate-sync）。
//
// 脱敏边界：任何响应路径不输出 credentials/access_token/refresh_token 的明文或密文，
// 只暴露 has_credentials/has_access_token 布尔与 token_expires_at。
type UpstreamRateSyncHandler struct {
	connRepo  upstreamratesync.ConnectionRepository
	runRepo   upstreamratesync.SyncRunRepository
	engine    UpstreamRateSyncEngine
	encryptor upstreamratesync.SecretEncryptor
}

// NewUpstreamRateSyncHandler 创建 handler（wire 注入）。
func NewUpstreamRateSyncHandler(
	connRepo upstreamratesync.ConnectionRepository,
	runRepo upstreamratesync.SyncRunRepository,
	syncer *upstreamratesync.Syncer,
	encryptor service.SecretEncryptor,
) *UpstreamRateSyncHandler {
	return newUpstreamRateSyncHandler(connRepo, runRepo, syncer, encryptor)
}

func newUpstreamRateSyncHandler(
	connRepo upstreamratesync.ConnectionRepository,
	runRepo upstreamratesync.SyncRunRepository,
	engine UpstreamRateSyncEngine,
	encryptor upstreamratesync.SecretEncryptor,
) *UpstreamRateSyncHandler {
	return &UpstreamRateSyncHandler{connRepo: connRepo, runRepo: runRepo, engine: engine, encryptor: encryptor}
}

// --- Request / Response DTO ---

type createUpstreamRateSyncConnectionRequest struct {
	Name            string `json:"name" binding:"required,max=100"`
	BaseURL         string `json:"base_url" binding:"required,max=500"`
	AuthMode        string `json:"auth_mode" binding:"required,oneof=password token"`
	Email           string `json:"email" binding:"omitempty,max=200"`
	Password        string `json:"password" binding:"omitempty,max=200"`
	Token           string `json:"token" binding:"omitempty,max=2000"`
	Enabled         *bool  `json:"enabled"`
	IntervalMinutes int    `json:"interval_minutes" binding:"omitempty,min=5,max=1440"`
}

// updateUpstreamRateSyncConnectionRequest 凭证字段留空 = 保持不变（不做显式清除语义）。
type updateUpstreamRateSyncConnectionRequest struct {
	Name            *string `json:"name" binding:"omitempty,max=100"`
	BaseURL         *string `json:"base_url" binding:"omitempty,max=500"`
	AuthMode        *string `json:"auth_mode" binding:"omitempty,oneof=password token"`
	Email           *string `json:"email" binding:"omitempty,max=200"`
	Password        *string `json:"password" binding:"omitempty,max=200"`
	Token           *string `json:"token" binding:"omitempty,max=2000"`
	Enabled         *bool   `json:"enabled"`
	IntervalMinutes *int    `json:"interval_minutes" binding:"omitempty,min=5,max=1440"`
}

// upstreamRateSyncConnectionResponse 脱敏视图：绝不含凭证/token 明文或密文。
type upstreamRateSyncConnectionResponse struct {
	ID              int64      `json:"id"`
	Name            string     `json:"name"`
	BaseURL         string     `json:"base_url"`
	AuthMode        string     `json:"auth_mode"`
	HasCredentials  bool       `json:"has_credentials"`
	HasAccessToken  bool       `json:"has_access_token"`
	TokenExpiresAt  *time.Time `json:"token_expires_at,omitempty"`
	Enabled         bool       `json:"enabled"`
	IntervalMinutes int        `json:"interval_minutes"`
	LastSyncAt      *time.Time `json:"last_sync_at,omitempty"`
	LastStatus      string     `json:"last_status,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type upstreamRateSyncRunResponse struct {
	ID                int64                            `json:"id"`
	ConnectionID      int64                            `json:"connection_id"`
	StartedAt         time.Time                        `json:"started_at"`
	FinishedAt        *time.Time                       `json:"finished_at,omitempty"`
	Status            string                           `json:"status"`
	KeysFetched       int                              `json:"keys_fetched"`
	AccountsMatched   int                              `json:"accounts_matched"`
	AccountsUpdated   int                              `json:"accounts_updated"`
	AccountsUnchanged int                              `json:"accounts_unchanged"`
	AccountsUnmatched int                              `json:"accounts_unmatched"`
	Details           []upstreamratesync.SyncRunDetail `json:"details"`
	Error             string                           `json:"error,omitempty"`
}

func toUpstreamRateSyncConnectionResponse(conn *upstreamratesync.Connection) upstreamRateSyncConnectionResponse {
	return upstreamRateSyncConnectionResponse{
		ID:              conn.ID,
		Name:            conn.Name,
		BaseURL:         conn.BaseURL,
		AuthMode:        conn.AuthMode,
		HasCredentials:  conn.HasCredentials(),
		HasAccessToken:  conn.HasAccessToken(),
		TokenExpiresAt:  conn.TokenExpiresAt,
		Enabled:         conn.Enabled,
		IntervalMinutes: conn.IntervalMinutes,
		LastSyncAt:      conn.LastSyncAt,
		LastStatus:      conn.LastStatus,
		LastError:       conn.LastError,
		CreatedAt:       conn.CreatedAt,
		UpdatedAt:       conn.UpdatedAt,
	}
}

func toUpstreamRateSyncRunResponse(run *upstreamratesync.SyncRun) upstreamRateSyncRunResponse {
	details := run.Details
	if details == nil {
		details = []upstreamratesync.SyncRunDetail{}
	}
	return upstreamRateSyncRunResponse{
		ID:                run.ID,
		ConnectionID:      run.ConnectionID,
		StartedAt:         run.StartedAt,
		FinishedAt:        run.FinishedAt,
		Status:            run.Status,
		KeysFetched:       run.KeysFetched,
		AccountsMatched:   run.AccountsMatched,
		AccountsUpdated:   run.AccountsUpdated,
		AccountsUnchanged: run.AccountsUnchanged,
		AccountsUnmatched: run.AccountsUnmatched,
		Details:           details,
		Error:             run.Error,
	}
}

// encryptCredentials 按 ports.go 契约加密凭证：password 模式明文为
// JSON {"email","password"}，token 模式明文为裸 token 字符串。
func (h *UpstreamRateSyncHandler) encryptCredentials(authMode, email, password, token string) (string, error) {
	switch authMode {
	case upstreamratesync.AuthModePassword:
		if strings.TrimSpace(email) == "" || password == "" {
			return "", errUpstreamRateSyncCredentialsIncomplete
		}
		plain, err := json.Marshal(map[string]string{"email": strings.TrimSpace(email), "password": password})
		if err != nil {
			return "", err
		}
		return h.encryptor.Encrypt(string(plain))
	case upstreamratesync.AuthModeToken:
		if strings.TrimSpace(token) == "" {
			return "", errUpstreamRateSyncCredentialsIncomplete
		}
		return h.encryptor.Encrypt(strings.TrimSpace(token))
	}
	return "", errUpstreamRateSyncInvalidAuthMode
}

// --- Handlers ---

// ListConnections GET /admin/upstream-rate-sync/connections
func (h *UpstreamRateSyncHandler) ListConnections(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	connections, total, err := h.connRepo.List(c.Request.Context(), upstreamratesync.ConnectionListParams{Page: page, PageSize: pageSize})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	items := make([]upstreamRateSyncConnectionResponse, 0, len(connections))
	for _, conn := range connections {
		items = append(items, toUpstreamRateSyncConnectionResponse(conn))
	}
	response.Paginated(c, items, total, page, pageSize)
}

// CreateConnection POST /admin/upstream-rate-sync/connections
func (h *UpstreamRateSyncHandler) CreateConnection(c *gin.Context) {
	var req createUpstreamRateSyncConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	baseURL, err := upstreamratesync.NormalizeBaseURL(req.BaseURL)
	if err != nil {
		response.BadRequest(c, "Invalid base_url: "+err.Error())
		return
	}
	credentialsEncrypted, err := h.encryptCredentials(req.AuthMode, req.Email, req.Password, req.Token)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	interval := req.IntervalMinutes
	if interval == 0 {
		interval = upstreamratesync.DefaultIntervalMinutes
	}
	enabled := false
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	conn := &upstreamratesync.Connection{
		Name:                 strings.TrimSpace(req.Name),
		BaseURL:              baseURL,
		AuthMode:             req.AuthMode,
		CredentialsEncrypted: credentialsEncrypted,
		Enabled:              enabled,
		IntervalMinutes:      interval,
	}
	if err := h.connRepo.Create(c.Request.Context(), conn); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, toUpstreamRateSyncConnectionResponse(conn))
}

// GetConnection GET /admin/upstream-rate-sync/connections/:id
func (h *UpstreamRateSyncHandler) GetConnection(c *gin.Context) {
	id, ok := parseUpstreamRateSyncID(c)
	if !ok {
		return
	}
	conn, err := h.connRepo.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, toUpstreamRateSyncConnectionResponse(conn))
}

// UpdateConnection PUT /admin/upstream-rate-sync/connections/:id
func (h *UpstreamRateSyncHandler) UpdateConnection(c *gin.Context) {
	id, ok := parseUpstreamRateSyncID(c)
	if !ok {
		return
	}
	var req updateUpstreamRateSyncConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	conn, err := h.connRepo.GetByID(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if req.Name != nil {
		conn.Name = strings.TrimSpace(*req.Name)
	}
	if req.BaseURL != nil {
		baseURL, normalizeErr := upstreamratesync.NormalizeBaseURL(*req.BaseURL)
		if normalizeErr != nil {
			response.BadRequest(c, "Invalid base_url: "+normalizeErr.Error())
			return
		}
		conn.BaseURL = baseURL
	}
	if req.AuthMode != nil {
		conn.AuthMode = *req.AuthMode
	}
	if req.Enabled != nil {
		conn.Enabled = *req.Enabled
	}
	if req.IntervalMinutes != nil {
		conn.IntervalMinutes = *req.IntervalMinutes
	}
	// 凭证字段留空 = 保持不变；任一非空则按（可能更新后的）auth_mode 重建并整体替换。
	email, password, token := stringValue(req.Email), stringValue(req.Password), stringValue(req.Token)
	if email != "" || password != "" || token != "" {
		credentialsEncrypted, encErr := h.encryptCredentials(conn.AuthMode, email, password, token)
		if encErr != nil {
			response.ErrorFrom(c, encErr)
			return
		}
		conn.CredentialsEncrypted = credentialsEncrypted
		// 凭证替换后旧的 access/refresh token 一并失效，下轮同步重新登录。
		conn.AccessTokenEncrypted = ""
		conn.RefreshTokenEncrypted = ""
		conn.TokenExpiresAt = nil
	}
	if err := h.connRepo.Update(c.Request.Context(), conn); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, toUpstreamRateSyncConnectionResponse(conn))
}

// DeleteConnection DELETE /admin/upstream-rate-sync/connections/:id
func (h *UpstreamRateSyncHandler) DeleteConnection(c *gin.Context) {
	id, ok := parseUpstreamRateSyncID(c)
	if !ok {
		return
	}
	if err := h.connRepo.Delete(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"id": id})
}

// TestConnection POST /admin/upstream-rate-sync/connections/:id/test
// 错误映射：ErrUpstreamTurnstile → 400 + reason 含 TURNSTILE；
// ErrUpstreamTOTPRequired → 400 明确错误；ErrSyncInProgress → 409。
// 三者均为 infraerrors 业务错误，由 response.ErrorFrom 统一映射。
func (h *UpstreamRateSyncHandler) TestConnection(c *gin.Context) {
	id, ok := parseUpstreamRateSyncID(c)
	if !ok {
		return
	}
	result, err := h.engine.TestConnection(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// SyncConnection POST /admin/upstream-rate-sync/connections/:id/sync
func (h *UpstreamRateSyncHandler) SyncConnection(c *gin.Context) {
	id, ok := parseUpstreamRateSyncID(c)
	if !ok {
		return
	}
	run, err := h.engine.SyncConnection(c.Request.Context(), id)
	if err != nil && run == nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, toUpstreamRateSyncRunResponse(run))
}

// ListRuns GET /admin/upstream-rate-sync/runs?connection_id&status&page&page_size
func (h *UpstreamRateSyncHandler) ListRuns(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	params := upstreamratesync.SyncRunListParams{Page: page, PageSize: pageSize}
	if raw := strings.TrimSpace(c.Query("connection_id")); raw != "" {
		connectionID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || connectionID <= 0 {
			response.BadRequest(c, "Invalid connection_id")
			return
		}
		params.ConnectionID = &connectionID
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		switch status {
		case upstreamratesync.SyncStatusSuccess, upstreamratesync.SyncStatusPartial, upstreamratesync.SyncStatusFailed:
			params.Status = status
		default:
			response.BadRequest(c, "Invalid status")
			return
		}
	}
	runs, total, err := h.runRepo.List(c.Request.Context(), params)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	items := make([]upstreamRateSyncRunResponse, 0, len(runs))
	for _, run := range runs {
		items = append(items, toUpstreamRateSyncRunResponse(run))
	}
	response.Paginated(c, items, total, page, pageSize)
}

func parseUpstreamRateSyncID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid connection ID")
		return 0, false
	}
	return id, true
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
