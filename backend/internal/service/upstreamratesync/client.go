package upstreamratesync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 上游 sub2api 实例的 HTTP 客户端约束。
const (
	// 固定 UA：上游会话绑定 = IP + UA 指纹，UA 漂移会导致 token 失效（design Decisions 5）。
	upstreamClientUserAgent = "sub2api-rate-sync/1.0"
	upstreamRequestTimeout  = 30 * time.Second
	upstreamMaxBodyBytes    = 1 << 20 // 1MB
	upstreamKeysPageSize    = 100
	upstreamMaxKeysPages    = 1000
	// 单页拉取失败（网络错误/5xx）的有界重试次数与退避基数。
	upstreamPageMaxAttempts = 3
	upstreamPageRetryBase   = 200 * time.Millisecond
)

// UpstreamError 上游返回的业务/HTTP 错误（脱敏：只含 status/reason/message，
// 不含任何 token 或凭证）。Turnstile/Unauthorized 通过 Is 匹配 sentinel 错误。
type UpstreamError struct {
	StatusCode int
	Reason     string
	Message    string
	Turnstile  bool
}

func (e *UpstreamError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("upstream error: status=%d reason=%q message=%q", e.StatusCode, e.Reason, e.Message)
}

func (e *UpstreamError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch target {
	case ErrUpstreamTurnstile:
		return e.Turnstile
	case ErrUpstreamUnauthorized:
		return e.StatusCode == http.StatusUnauthorized
	case ErrUpstreamUnreachable:
		return e.StatusCode == 0 && e.Reason == "UNREACHABLE"
	}
	return false
}

// tokenPair 上游 login/refresh 返回的令牌对。
type tokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int // access token 有效期（秒）
}

// upstreamKeyGroup 是上游 GET /api/v1/keys 每把 key 内嵌的 group 对象
// （字段名与 handler/dto.Group 一致；keys DTO 不含 timezone）。
type upstreamKeyGroup struct {
	Name               string  `json:"name"`
	Platform           string  `json:"platform"`
	RateMultiplier     float64 `json:"rate_multiplier"`
	PeakRateEnabled    bool    `json:"peak_rate_enabled"`
	PeakStart          string  `json:"peak_start"`
	PeakEnd            string  `json:"peak_end"`
	PeakRateMultiplier float64 `json:"peak_rate_multiplier"`
}

// upstreamKey 上游 key 条目；Key 为完整 sk- 字符串（仅用于精确匹配与前缀记录，
// 禁止落入日志/明细）。
type upstreamKey struct {
	ID    int64             `json:"id"`
	Key   string            `json:"key"`
	Group *upstreamKeyGroup `json:"group"`
}

// upstreamKeysPage 分页信封的 data 部分（与 response.PaginatedData 同构）。
type upstreamKeysPage struct {
	Items    []upstreamKey `json:"items"`
	Total    int64         `json:"total"`
	Page     int           `json:"page"`
	PageSize int           `json:"page_size"`
	Pages    int           `json:"pages"`
}

// upstreamEnvelope 统一响应信封 {code,message,reason,data}。
type upstreamEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Reason  string          `json:"reason"`
	Data    json.RawMessage `json:"data"`
}

// upstreamClient 上游 sub2api 兼容实例的 HTTP 客户端。
type upstreamClient struct {
	baseURL    string
	httpClient *http.Client
	userAgent  string
}

func newUpstreamClient(baseURL string, httpClient *http.Client) *upstreamClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: upstreamRequestTimeout}
	}
	return &upstreamClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
		userAgent:  upstreamClientUserAgent,
	}
}

// login POST /api/v1/auth/login（账密，不依赖邮箱验证）。
// 上游开启 Turnstile 时返回匹配 ErrUpstreamTurnstile 的错误；
// 上游账号开启 TOTP 时返回 ErrUpstreamTOTPRequired（design 死穴，降级 token 模式）。
func (c *upstreamClient) login(ctx context.Context, email, password string) (*tokenPair, error) {
	body := map[string]string{"email": email, "password": password}
	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Requires2FA  bool   `json:"requires_2fa"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/auth/login", body, "", &data); err != nil {
		return nil, err
	}
	if data.Requires2FA {
		return nil, ErrUpstreamTOTPRequired
	}
	if data.AccessToken == "" {
		return nil, &UpstreamError{StatusCode: http.StatusOK, Reason: "INVALID_RESPONSE", Message: "login response missing access_token"}
	}
	return &tokenPair{AccessToken: data.AccessToken, RefreshToken: data.RefreshToken, ExpiresIn: data.ExpiresIn}, nil
}

// refresh POST /api/v1/auth/refresh（严格一次性轮转：返回的新 refresh token
// 必须立即持久化，旧的随即失效）。
func (c *upstreamClient) refresh(ctx context.Context, refreshToken string) (*tokenPair, error) {
	body := map[string]string{"refresh_token": refreshToken}
	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/auth/refresh", body, "", &data); err != nil {
		return nil, err
	}
	if data.AccessToken == "" {
		return nil, &UpstreamError{StatusCode: http.StatusOK, Reason: "INVALID_RESPONSE", Message: "refresh response missing access_token"}
	}
	return &tokenPair{AccessToken: data.AccessToken, RefreshToken: data.RefreshToken, ExpiresIn: data.ExpiresIn}, nil
}

// fetchBalance GET /api/v1/user/profile（用户 JWT 鉴权），返回上游账号余额（USD）。
func (c *upstreamClient) fetchBalance(ctx context.Context, accessToken string) (float64, error) {
	var data struct {
		Balance float64 `json:"balance"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/user/profile", nil, accessToken, &data); err != nil {
		return 0, err
	}
	return data.Balance, nil
}

// listKeysPage GET /api/v1/keys?page=N&page_size=100（用户 JWT 鉴权）。
func (c *upstreamClient) listKeysPage(ctx context.Context, accessToken string, page int) (*upstreamKeysPage, error) {
	path := fmt.Sprintf("/api/v1/keys?page=%d&page_size=%d", page, upstreamKeysPageSize)
	var data upstreamKeysPage
	if err := c.do(ctx, http.MethodGet, path, nil, accessToken, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

// listAllKeys 翻页累计全部 key；单页失败（网络错误/5xx）按有界次数+退避重试，
// 重试耗尽或非可重试错误则整批失败（run=failed）。
func (c *upstreamClient) listAllKeys(ctx context.Context, accessToken string) ([]upstreamKey, error) {
	var all []upstreamKey
	for page := 1; page <= upstreamMaxKeysPages; page++ {
		data, err := c.listKeysPageWithRetry(ctx, accessToken, page)
		if err != nil {
			return nil, err
		}
		all = append(all, data.Items...)
		pages := data.Pages
		if pages <= 0 {
			// 上游缺 pages 字段时按短页终止。
			if len(data.Items) < upstreamKeysPageSize {
				break
			}
			continue
		}
		if page >= pages {
			break
		}
	}
	return all, nil
}

func (c *upstreamClient) listKeysPageWithRetry(ctx context.Context, accessToken string, page int) (*upstreamKeysPage, error) {
	var lastErr error
	for attempt := 0; attempt < upstreamPageMaxAttempts; attempt++ {
		if attempt > 0 {
			delay := upstreamPageRetryBase * time.Duration(1<<(attempt-1))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		data, err := c.listKeysPage(ctx, accessToken, page)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if !isRetryableUpstreamError(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// isRetryableUpstreamError 仅网络层错误与 5xx 可重试；401/4xx 业务错误不可重试。
func isRetryableUpstreamError(err error) bool {
	var upErr *UpstreamError
	if errors.As(err, &upErr) {
		return upErr.StatusCode >= 500
	}
	return true
}

// do 统一请求执行：固定 UA、响应体上限 1MB、统一 {code,message,reason,data} 解包。
// accessToken 非空时带 Bearer 鉴权。任何路径不把 token/凭证写入错误。
func (c *upstreamClient) do(ctx context.Context, method, path string, body any, accessToken string, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal upstream request: %w", err)
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build upstream request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// 传输层失败（DNS/连接/超时）：归为 UNREACHABLE，细节只留日志侧。
		return &UpstreamError{StatusCode: 0, Reason: "UNREACHABLE", Message: "upstream is unreachable"}
	}
	if resp == nil || resp.Body == nil {
		return &UpstreamError{StatusCode: 0, Reason: "EMPTY_RESPONSE", Message: "upstream returned an empty response"}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, upstreamMaxBodyBytes+1))
	if err != nil {
		return &UpstreamError{StatusCode: resp.StatusCode, Reason: "RESPONSE_READ_FAILED", Message: "failed to read upstream response"}
	}
	if len(raw) > upstreamMaxBodyBytes {
		return &UpstreamError{StatusCode: resp.StatusCode, Reason: "RESPONSE_TOO_LARGE", Message: "upstream response exceeded 1MB limit"}
	}

	var envelope upstreamEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return &UpstreamError{StatusCode: resp.StatusCode, Reason: "INVALID_RESPONSE", Message: "upstream response is not a valid envelope"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || envelope.Code != 0 {
		return classifyUpstreamError(resp.StatusCode, &envelope)
	}
	if out == nil || len(envelope.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return &UpstreamError{StatusCode: resp.StatusCode, Reason: "INVALID_RESPONSE", Message: "upstream response data has unexpected shape"}
	}
	return nil
}

// classifyUpstreamError 把上游错误信封映射为可区分错误：
// reason/message 含 "turnstile"（不区分大小写）→ 匹配 ErrUpstreamTurnstile；
// HTTP 401 → 匹配 ErrUpstreamUnauthorized。
func classifyUpstreamError(statusCode int, envelope *upstreamEnvelope) error {
	reason := envelope.Reason
	if reason == "" {
		reason = "UPSTREAM_ERROR"
	}
	upErr := &UpstreamError{StatusCode: statusCode, Reason: reason, Message: envelope.Message}
	haystack := strings.ToUpper(reason + " " + envelope.Message)
	if strings.Contains(haystack, "TURNSTILE") {
		upErr.Turnstile = true
	}
	return upErr
}
