package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service/upstreamratesync"

	"github.com/stretchr/testify/require"
)

// ListByPlatform 覆盖：同步适配器按平台逐个加载账号。
func (r *upstreamBillingProbeAccountRepo) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Account, 0)
	for _, account := range r.accounts {
		if account.Platform == platform {
			out = append(out, *account)
		}
	}
	return out, nil
}

func TestUpstreamRateSyncAccountGateway_ListScopedAccountsAllPlatforms(t *testing.T) {
	rate1 := 1.0
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
		1: {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, RateMultiplier: &rate1,
			Credentials: map[string]any{"api_key": "sk-openai-1", "base_url": "https://upstream.example.com/"}},
		2: {ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk-claude-1", "base_url": "https://upstream.example.com"}},
		3: {ID: 3, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
			Credentials: map[string]any{"api_key": "sk-oauth-skip", "base_url": "https://upstream.example.com"}},
		4: {ID: 4, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk-other-site", "base_url": "https://other.example.com"}},
		5: {ID: 5, Platform: PlatformGemini, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "sk-gemini-1", "base_url": "https://upstream.example.com"}},
	}}

	gateway := NewUpstreamRateSyncAccountGateway(repo)
	scoped, err := gateway.ListScopedAccounts(context.Background(), "https://upstream.example.com")
	require.NoError(t, err)

	byID := make(map[int64]upstreamratesync.ScopedAccount, len(scoped))
	for _, account := range scoped {
		byID[account.ID] = account
	}
	require.Len(t, scoped, 3, "openai/anthropic/gemini 的 apikey 账号全部纳入匹配")
	require.Equal(t, "sk-openai-1", byID[1].APIKey)
	require.Equal(t, "sk-claude-1", byID[2].APIKey)
	require.Equal(t, "sk-gemini-1", byID[5].APIKey)
	require.NotContains(t, byID, int64(3), "oauth 账号不参与")
	require.NotContains(t, byID, int64(4), "其他 base_url 的账号被作用域隔离")
}
