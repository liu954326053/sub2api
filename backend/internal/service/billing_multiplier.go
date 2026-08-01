package service

// resolveBillingBaseMultiplier 解析计费基础倍率的"分组级"部分（优先级链见
// openspec add-upstream-rate-sync design.md Decisions 9）：
//   - 分组 billing_mode == account_upstream 且命中账号带有效倍率
//     （RateMultiplier 非 nil 且 >= 0）时，base 取账号倍率（账号成本口径，由上游同步写回）；
//   - 其他情况（group_multiplier 模式、账号为 nil、账号未同步到倍率、倍率为负的脏数据）
//     base 回退分组 rate_multiplier（account_upstream 模式下它充当兜底倍率）。
//
// 用户专属分组倍率由调用方在此 base 上覆盖（ResolveUserGroupRateMultiplier 的
// "分组默认值"入参换成本函数返回值，专属值始终覆盖）；高峰因子最后经
// computePeakAwareMultipliers 叠加；image/video 独立倍率语义不变。
// group_multiplier 模式下返回值与升级前逐字节一致（恒为分组 rate_multiplier）。
//
// gateway_service.recordUsageCore（Anthropic）与 openai_gateway_service.RecordUsage
// （OpenAI，需先 resolveCredentialAccount 再传入解析后的账号）共用本函数。
func resolveBillingBaseMultiplier(apiKeyGroup *Group, account *Account) float64 {
	if apiKeyGroup == nil {
		return 1.0
	}
	if apiKeyGroup.BillingMode == GroupBillingModeAccountUpstream &&
		account != nil && account.RateMultiplier != nil && *account.RateMultiplier >= 0 {
		return *account.RateMultiplier
	}
	return apiKeyGroup.RateMultiplier
}
