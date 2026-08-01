package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

// account_upstream 计价模式（openspec add-upstream-rate-sync design.md Decisions 9）：
// 公共 helper resolveBillingBaseMultiplier 的两模式/回退/专属覆盖/高峰顺序，
// 以及 Anthropic（recordUsageCore）与 OpenAI（RecordUsage）两条入口的行为一致性。

func TestResolveBillingBaseMultiplier(t *testing.T) {
	groupRate := 1.5
	accountRate := 2.5

	newGroup := func(mode string) *Group {
		return &Group{RateMultiplier: groupRate, BillingMode: mode}
	}

	cases := []struct {
		name    string
		group   *Group
		account *Account
		want    float64
	}{
		{"group_multiplier 忽略账号倍率", newGroup(GroupBillingModeGroupMultiplier), &Account{RateMultiplier: f64p(accountRate)}, groupRate},
		{"空 billing_mode 按 group_multiplier", newGroup(""), &Account{RateMultiplier: f64p(accountRate)}, groupRate},
		{"未知 billing_mode 按 group_multiplier", newGroup("bogus"), &Account{RateMultiplier: f64p(accountRate)}, groupRate},
		{"account_upstream 命中账号倍率", newGroup(GroupBillingModeAccountUpstream), &Account{RateMultiplier: f64p(accountRate)}, accountRate},
		{"account_upstream 账号倍率 0 有效", newGroup(GroupBillingModeAccountUpstream), &Account{RateMultiplier: f64p(0)}, 0},
		{"account_upstream 账号为 nil 回退分组", newGroup(GroupBillingModeAccountUpstream), nil, groupRate},
		{"account_upstream 账号未同步倍率回退分组", newGroup(GroupBillingModeAccountUpstream), &Account{}, groupRate},
		{"account_upstream 负倍率脏数据回退分组", newGroup(GroupBillingModeAccountUpstream), &Account{RateMultiplier: f64p(-1)}, groupRate},
		{
			"account_upstream 影子账号（无自身倍率）回退分组",
			newGroup(GroupBillingModeAccountUpstream),
			&Account{ParentAccountID: i64p(99)},
			groupRate,
		},
		{"分组为 nil 落系统默认 1.0", nil, &Account{RateMultiplier: f64p(accountRate)}, 1.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, resolveBillingBaseMultiplier(c.group, c.account))
		})
	}
}

func TestResolveBillingBaseMultiplier_PeakAppliesAfterAccountRate(t *testing.T) {
	// 高峰因子最后叠加：account_upstream 命中账号倍率 2.0，高峰 ×3 → token 倍率 6.0；
	// 图片按次倍率基于 base 现算、不受高峰影响。
	group := &Group{
		SubscriptionType:   SubscriptionTypeSubscription,
		RateMultiplier:     1.5,
		BillingMode:        GroupBillingModeAccountUpstream,
		PeakRateEnabled:    true,
		PeakStart:          "00:00",
		PeakEnd:            "23:59",
		PeakRateMultiplier: 3.0,
	}
	account := &Account{RateMultiplier: f64p(2.0)}
	apiKey := &APIKey{Group: group}

	base := resolveBillingBaseMultiplier(apiKey.Group, account)
	require.Equal(t, 2.0, base)

	text, image := computePeakAwareMultipliers(apiKey, base, timezone.Now())
	require.Equal(t, 6.0, text)
	require.Equal(t, 2.0, image)
}

func TestResolveBillingBaseMultiplier_UserSpecificRateAlwaysOverrides(t *testing.T) {
	// 用户专属倍率始终覆盖 helper 算出的 base（无论是账号倍率还是分组兜底）。
	groupID := int64(21)
	userRate := 0.7
	rateRepo := &openAIUserGroupRateRepoStub{rate: &userRate}
	resolver := newUserGroupRateResolver(rateRepo, nil, time.Minute, nil, "service.billing_multiplier.test")

	group := &Group{ID: groupID, RateMultiplier: 1.5, BillingMode: GroupBillingModeAccountUpstream}
	account := &Account{RateMultiplier: f64p(2.5)}

	base := resolveBillingBaseMultiplier(group, account)
	require.Equal(t, 2.5, base)
	require.Equal(t, userRate, resolver.Resolve(context.Background(), 7, groupID, base))
	require.Equal(t, 1, rateRepo.calls)
}

// --- 两条入口行为一致 ---

func newBillingModeGatewayServiceForTest(usageRepo UsageLogRepository, userRepo UserRepository, subRepo UserSubscriptionRepository, rateRepo UserGroupRateRepository) *GatewayService {
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1.1
	return NewGatewayService(
		nil,
		nil,
		usageRepo,
		nil,
		userRepo,
		subRepo,
		rateRepo,
		nil,
		cfg,
		nil,
		nil,
		NewBillingService(cfg, nil),
		nil,
		&BillingCacheService{},
		nil,
		nil,
		&DeferredService{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil, // userPlatformQuotaRepo
	)
}

func recordAnthropicUsageForBillingMode(t *testing.T, svc *GatewayService, group *Group, account *Account) {
	t.Helper()
	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "billing_mode_anthropic",
			Usage:     ClaudeUsage{InputTokens: 10, OutputTokens: 6},
			Model:     "claude-sonnet-4",
			Duration:  time.Second,
		},
		APIKey: &APIKey{
			ID:      501,
			Quota:   100,
			GroupID: i64p(group.ID),
			Group:   group,
		},
		User:    &User{ID: 601},
		Account: account,
	})
	require.NoError(t, err)
}

func recordOpenAIUsageForBillingMode(t *testing.T, svc *OpenAIGatewayService, group *Group, account *Account) {
	t.Helper()
	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "billing_mode_openai",
			Usage:     OpenAIUsage{InputTokens: 10, OutputTokens: 6},
			Model:     "gpt-5.1",
			Duration:  time.Second,
		},
		APIKey: &APIKey{
			ID:      1001,
			Quota:   100,
			GroupID: i64p(group.ID),
			Group:   group,
		},
		User:    &User{ID: 2001},
		Account: account,
	})
	require.NoError(t, err)
}

func TestRecordUsage_BillingModeAccountUpstreamBothGateways(t *testing.T) {
	// 两条入口：account_upstream 命中账号倍率时按账号倍率计费，结果一致。
	group := &Group{ID: 31, RateMultiplier: 1.5, BillingMode: GroupBillingModeAccountUpstream}
	account := &Account{ID: 3001, RateMultiplier: f64p(2.5)}

	anthropicUsageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	anthropicSvc := newBillingModeGatewayServiceForTest(anthropicUsageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	recordAnthropicUsageForBillingMode(t, anthropicSvc, group, account)
	require.NotNil(t, anthropicUsageRepo.lastLog)
	require.Equal(t, 2.5, anthropicUsageRepo.lastLog.RateMultiplier)

	openAIUsageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	openAISvc := newOpenAIRecordUsageServiceForTest(openAIUsageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	recordOpenAIUsageForBillingMode(t, openAISvc, group, account)
	require.NotNil(t, openAIUsageRepo.lastLog)
	require.Equal(t, 2.5, openAIUsageRepo.lastLog.RateMultiplier)
}

func TestRecordUsage_BillingModeAccountUpstreamFallsBackToGroupRate(t *testing.T) {
	// 两条入口：账号未同步到倍率（nil）时回退分组兜底倍率，结果一致。
	group := &Group{ID: 32, RateMultiplier: 1.5, BillingMode: GroupBillingModeAccountUpstream}
	account := &Account{ID: 3002}

	anthropicUsageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	anthropicSvc := newBillingModeGatewayServiceForTest(anthropicUsageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	recordAnthropicUsageForBillingMode(t, anthropicSvc, group, account)
	require.NotNil(t, anthropicUsageRepo.lastLog)
	require.Equal(t, 1.5, anthropicUsageRepo.lastLog.RateMultiplier)

	openAIUsageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	openAISvc := newOpenAIRecordUsageServiceForTest(openAIUsageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	recordOpenAIUsageForBillingMode(t, openAISvc, group, account)
	require.NotNil(t, openAIUsageRepo.lastLog)
	require.Equal(t, 1.5, openAIUsageRepo.lastLog.RateMultiplier)
}

func TestRecordUsage_BillingModeGroupMultiplierIgnoresAccountRate(t *testing.T) {
	// 升级前行为回归：group_multiplier 模式下即使账号带倍率也按分组倍率计费。
	group := &Group{ID: 33, RateMultiplier: 1.5, BillingMode: GroupBillingModeGroupMultiplier}
	account := &Account{ID: 3003, RateMultiplier: f64p(2.5)}

	anthropicUsageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	anthropicSvc := newBillingModeGatewayServiceForTest(anthropicUsageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	recordAnthropicUsageForBillingMode(t, anthropicSvc, group, account)
	require.NotNil(t, anthropicUsageRepo.lastLog)
	require.Equal(t, 1.5, anthropicUsageRepo.lastLog.RateMultiplier)

	openAIUsageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	openAISvc := newOpenAIRecordUsageServiceForTest(openAIUsageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	recordOpenAIUsageForBillingMode(t, openAISvc, group, account)
	require.NotNil(t, openAIUsageRepo.lastLog)
	require.Equal(t, 1.5, openAIUsageRepo.lastLog.RateMultiplier)
}

func TestRecordUsage_BillingModeUserSpecificRateOverridesAccountRate(t *testing.T) {
	// 用户专属倍率覆盖 account_upstream 的账号倍率（两条入口之一验证叠加顺序即可，
	// 另一入口共用同一 helper 与 resolver，由 helper 单测锁定）。
	userRate := 0.7
	group := &Group{ID: 34, RateMultiplier: 1.5, BillingMode: GroupBillingModeAccountUpstream}
	account := &Account{ID: 3004, RateMultiplier: f64p(2.5)}

	anthropicUsageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	rateRepo := &openAIUserGroupRateRepoStub{rate: &userRate}
	anthropicSvc := newBillingModeGatewayServiceForTest(anthropicUsageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, rateRepo)
	recordAnthropicUsageForBillingMode(t, anthropicSvc, group, account)
	require.NotNil(t, anthropicUsageRepo.lastLog)
	require.Equal(t, userRate, anthropicUsageRepo.lastLog.RateMultiplier)
}

func TestOpenAIRecordUsage_BillingModeAccountUpstreamShadowAccount(t *testing.T) {
	// OpenAI 路径：影子账号经 resolveCredentialAccount 解析后，读母账号的上游同步倍率。
	group := &Group{ID: 35, RateMultiplier: 1.5, BillingMode: GroupBillingModeAccountUpstream}
	shadow := &Account{ID: 3005, ParentAccountID: i64p(3006)}
	parent := &Account{ID: 3006, Platform: PlatformOpenAI, Type: AccountTypeOAuth, RateMultiplier: f64p(2.5)}

	openAIUsageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	openAISvc := newOpenAIRecordUsageServiceForTest(openAIUsageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	openAISvc.accountRepo = &openAIRecordUsageAccountRepoStub{account: parent}
	recordOpenAIUsageForBillingMode(t, openAISvc, group, shadow)
	require.NotNil(t, openAIUsageRepo.lastLog)
	require.Equal(t, 2.5, openAIUsageRepo.lastLog.RateMultiplier)
}

func TestOpenAIRecordUsage_BillingModeAccountUpstreamShadowParentWithoutRate(t *testing.T) {
	// OpenAI 路径：影子账号解析出的母账号未同步倍率时回退分组兜底。
	group := &Group{ID: 36, RateMultiplier: 1.5, BillingMode: GroupBillingModeAccountUpstream}
	shadow := &Account{ID: 3007, ParentAccountID: i64p(3008)}
	parent := &Account{ID: 3008, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	openAIUsageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	openAISvc := newOpenAIRecordUsageServiceForTest(openAIUsageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	openAISvc.accountRepo = &openAIRecordUsageAccountRepoStub{account: parent}
	recordOpenAIUsageForBillingMode(t, openAISvc, group, shadow)
	require.NotNil(t, openAIUsageRepo.lastLog)
	require.Equal(t, 1.5, openAIUsageRepo.lastLog.RateMultiplier)
}
