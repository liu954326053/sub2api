package handler

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// AvailableChannelHandler 处理用户侧「可用渠道」查询。
//
// 用户侧接口委托 ChannelService.ListAvailable，并在返回前做三层过滤：
//  1. 行过滤：只保留状态为 Active 且与当前用户可访问分组有交集的渠道；
//  2. 分组过滤：渠道的 Groups 只保留用户可访问的那些；
//  3. 平台过滤：渠道的 SupportedModels 只保留平台在用户可见 Groups 中出现过的模型，
//     防止"渠道同时挂在 antigravity / anthropic 两个平台的分组上，用户只访问
//     antigravity，却看到 anthropic 模型"这类跨平台信息泄漏；
//  4. 字段白名单：仅返回用户需要的字段（省略 BillingModelSource / RestrictModels
//     / 内部 ID / Status 等管理字段）。
type AvailableChannelHandler struct {
	channelService *service.ChannelService
	apiKeyService  *service.APIKeyService
	gatewayService *service.GatewayService
	settingService *service.SettingService
}

// NewAvailableChannelHandler 创建用户侧可用渠道 handler。
func NewAvailableChannelHandler(
	channelService *service.ChannelService,
	apiKeyService *service.APIKeyService,
	gatewayService *service.GatewayService,
	settingService *service.SettingService,
) *AvailableChannelHandler {
	return &AvailableChannelHandler{
		channelService: channelService,
		apiKeyService:  apiKeyService,
		gatewayService: gatewayService,
		settingService: settingService,
	}
}

// featureEnabled 返回 available-channels 开关是否启用。默认关闭（opt-in）。
func (h *AvailableChannelHandler) featureEnabled(c *gin.Context) bool {
	if h.settingService == nil {
		return false
	}
	return h.settingService.GetAvailableChannelsRuntime(c.Request.Context()).Enabled
}

// userAvailableGroup 用户可见的分组概要（白名单字段）。
//
// 前端据此区分专属 vs 公开分组（IsExclusive）、订阅 vs 标准分组（SubscriptionType，
// 订阅视觉加深），并用 RateMultiplier 作为默认倍率；用户专属倍率前端走
// /groups/rates，和 API 密钥页面保持一致。
type userAvailableGroup struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	Platform         string  `json:"platform"`
	SubscriptionType string  `json:"subscription_type"`
	RateMultiplier   float64 `json:"rate_multiplier"`
	IsExclusive      bool    `json:"is_exclusive"`
}

// userSupportedModelPricing 用户可见的定价字段白名单。
type userSupportedModelPricing struct {
	BillingMode      string                   `json:"billing_mode"`
	InputPrice       *float64                 `json:"input_price"`
	OutputPrice      *float64                 `json:"output_price"`
	CacheWritePrice  *float64                 `json:"cache_write_price"`
	CacheReadPrice   *float64                 `json:"cache_read_price"`
	ImageOutputPrice *float64                 `json:"image_output_price"`
	PerRequestPrice  *float64                 `json:"per_request_price"`
	Intervals        []userPricingIntervalDTO `json:"intervals"`
}

// userPricingIntervalDTO 定价区间白名单（去掉内部 ID、SortOrder 等前端不渲染的字段）。
type userPricingIntervalDTO struct {
	MinTokens       int      `json:"min_tokens"`
	MaxTokens       *int     `json:"max_tokens"`
	TierLabel       string   `json:"tier_label,omitempty"`
	InputPrice      *float64 `json:"input_price"`
	OutputPrice     *float64 `json:"output_price"`
	CacheWritePrice *float64 `json:"cache_write_price"`
	CacheReadPrice  *float64 `json:"cache_read_price"`
	PerRequestPrice *float64 `json:"per_request_price"`
}

// userSupportedModel 用户可见的支持模型条目。
type userSupportedModel struct {
	Name     string                     `json:"name"`
	Platform string                     `json:"platform"`
	Pricing  *userSupportedModelPricing `json:"pricing"`
}

// userChannelPlatformSection 单渠道内某个平台的子视图：用户可见的分组 + 该平台
// 支持的模型。按 platform 聚合后让前端可以把渠道名作为 row-group 一次渲染，
// 后面的平台行按 sections 顺序铺开。
type userChannelPlatformSection struct {
	Platform        string               `json:"platform"`
	Groups          []userAvailableGroup `json:"groups"`
	SupportedModels []userSupportedModel `json:"supported_models"`
}

// userAvailableChannel 用户可见的渠道条目（白名单字段）。
//
// 每个渠道聚合为一条记录，内嵌 platforms 子数组：每个 section 对应一个平台，
// 包含该平台的 groups 和 supported_models。
type userAvailableChannel struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Platforms   []userChannelPlatformSection `json:"platforms"`
}

// cursorModelSquareResponse is a compact desktop-client view built from the
// current user's Cursor dedicated OpenAI/Claude keys.
type cursorModelSquareResponse struct {
	Keys   []cursorModelSquareKey   `json:"keys"`
	Models []cursorModelSquareModel `json:"models"`
}

type cursorModelSquareKey struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	GroupID  int64  `json:"group_id"`
	Group    string `json:"group"`
	Platform string `json:"platform"`
}

type cursorModelSquareModel struct {
	Name     string                     `json:"name"`
	Platform string                     `json:"platform"`
	GroupID  int64                      `json:"group_id"`
	Group    string                     `json:"group"`
	Pricing  *userSupportedModelPricing `json:"pricing"`
}

// List 列出当前用户可见的「可用渠道」。
// GET /api/v1/channels/available
func (h *AvailableChannelHandler) List(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	// Feature 未启用时返回空数组（不暴露渠道信息）。检查放在认证之后，
	// 保持与未开关前的 401 行为一致：未登录先 401，登录后再按开关决定。
	if !h.featureEnabled(c) {
		response.Success(c, []userAvailableChannel{})
		return
	}

	userGroups, err := h.apiKeyService.GetAvailableGroups(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	allowedGroupIDs := make(map[int64]struct{}, len(userGroups))
	for i := range userGroups {
		allowedGroupIDs[userGroups[i].ID] = struct{}{}
	}

	channels, err := h.channelService.ListAvailable(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]userAvailableChannel, 0, len(channels))
	for _, ch := range channels {
		if ch.Status != service.StatusActive {
			continue
		}
		visibleGroups := filterUserVisibleGroups(ch.Groups, allowedGroupIDs)
		if len(visibleGroups) == 0 {
			continue
		}
		sections := buildPlatformSections(ch, visibleGroups)
		if len(sections) == 0 {
			continue
		}
		out = append(out, userAvailableChannel{
			Name:        ch.Name,
			Description: ch.Description,
			Platforms:   sections,
		})
	}

	response.Success(c, out)
}

// CursorModels returns model names and prices for the current user's Cursor
// dedicated OpenAI/Claude keys. It does not depend on the available-channels
// feature flag because the desktop client needs this data as part of its
// account dashboard.
// GET /api/v1/channels/cursor-models
func (h *AvailableChannelHandler) CursorModels(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	keys, err := h.apiKeyService.ListCursorDedicated(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	allowedGroups := make(map[int64]service.Group)
	outKeys := make([]cursorModelSquareKey, 0, len(keys))
	for i := range keys {
		key := keys[i]
		if key.GroupID == nil || key.Group == nil {
			continue
		}
		platform := normalizeCursorModelPlatform(key.Group.Platform)
		if platform != "openai" && platform != "anthropic" && platform != "claude" {
			continue
		}
		allowedGroups[*key.GroupID] = *key.Group
		outKeys = append(outKeys, cursorModelSquareKey{
			ID:       key.ID,
			Name:     key.Name,
			GroupID:  *key.GroupID,
			Group:    key.Group.Name,
			Platform: platform,
		})
	}

	if len(allowedGroups) == 0 {
		response.Success(c, cursorModelSquareResponse{Keys: outKeys, Models: []cursorModelSquareModel{}})
		return
	}

	groupMultipliers, err := h.cursorModelGroupMultipliers(c.Request.Context(), subject.UserID, allowedGroups)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	seen := make(map[string]struct{})
	models := make([]cursorModelSquareModel, 0)

	for groupID, group := range allowedGroups {
		platform := normalizeCursorModelPlatform(group.Platform)
		if platform != service.PlatformOpenAI && platform != service.PlatformAnthropic {
			continue
		}
		modelNames := h.cursorModelNamesForGroup(c.Request.Context(), groupID, platform)
		for _, modelName := range modelNames {
			key := fmt.Sprintf("%d:%s:%s", groupID, platform, strings.ToLower(modelName))
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			models = append(models, buildCursorModelSquareModel(
				modelName,
				platform,
				groupID,
				group.Name,
				toUserPricing(applyPricingMultiplier(h.channelService.DisplayPricingForModel(modelName), groupMultipliers[groupID])),
			))
		}
	}

	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Platform != models[j].Platform {
			return models[i].Platform < models[j].Platform
		}
		return strings.ToLower(models[i].Name) < strings.ToLower(models[j].Name)
	})

	response.Success(c, cursorModelSquareResponse{Keys: outKeys, Models: models})
}

func (h *AvailableChannelHandler) cursorModelNamesForGroup(ctx context.Context, groupID int64, platform string) []string {
	if h.gatewayService != nil {
		modelNames := h.gatewayService.GetAvailableModels(ctx, &groupID, platform)
		if len(modelNames) > 0 {
			return modelNames
		}
	}
	return cursorModelDefaultNames(platform)
}

func (h *AvailableChannelHandler) cursorModelGroupMultipliers(ctx context.Context, userID int64, groups map[int64]service.Group) (map[int64]float64, error) {
	userRates, err := h.apiKeyService.GetUserGroupRates(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]float64, len(groups))
	for groupID, group := range groups {
		multiplier := group.RateMultiplier
		if multiplier <= 0 {
			multiplier = 1
		}
		if userRate, ok := userRates[groupID]; ok && userRate > 0 {
			multiplier = userRate
		}
		out[groupID] = multiplier
	}
	return out, nil
}

func normalizeCursorModelPlatform(platform string) string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "claude" {
		return "anthropic"
	}
	return platform
}

func cursorModelDefaultNames(platform string) []string {
	switch normalizeCursorModelPlatform(platform) {
	case service.PlatformOpenAI:
		out := make([]string, 0, len(openai.DefaultModels))
		for _, model := range openai.DefaultModels {
			out = append(out, model.ID)
		}
		return out
	case service.PlatformAnthropic:
		out := make([]string, 0, len(claude.DefaultModels))
		for _, model := range claude.DefaultModels {
			out = append(out, model.ID)
		}
		return out
	default:
		return nil
	}
}

func applyPricingMultiplier(p *service.ChannelModelPricing, multiplier float64) *service.ChannelModelPricing {
	if p == nil {
		return nil
	}
	if multiplier <= 0 {
		multiplier = 1
	}
	out := p.Clone()
	out.InputPrice = multiplyFloatPtr(out.InputPrice, multiplier)
	out.OutputPrice = multiplyFloatPtr(out.OutputPrice, multiplier)
	out.CacheWritePrice = multiplyFloatPtr(out.CacheWritePrice, multiplier)
	out.CacheReadPrice = multiplyFloatPtr(out.CacheReadPrice, multiplier)
	out.ImageOutputPrice = multiplyFloatPtr(out.ImageOutputPrice, multiplier)
	out.PerRequestPrice = multiplyFloatPtr(out.PerRequestPrice, multiplier)
	for i := range out.Intervals {
		out.Intervals[i].InputPrice = multiplyFloatPtr(out.Intervals[i].InputPrice, multiplier)
		out.Intervals[i].OutputPrice = multiplyFloatPtr(out.Intervals[i].OutputPrice, multiplier)
		out.Intervals[i].CacheWritePrice = multiplyFloatPtr(out.Intervals[i].CacheWritePrice, multiplier)
		out.Intervals[i].CacheReadPrice = multiplyFloatPtr(out.Intervals[i].CacheReadPrice, multiplier)
		out.Intervals[i].PerRequestPrice = multiplyFloatPtr(out.Intervals[i].PerRequestPrice, multiplier)
	}
	return &out
}

func multiplyFloatPtr(value *float64, multiplier float64) *float64 {
	if value == nil {
		return nil
	}
	out := *value * multiplier
	return &out
}

func buildCursorModelSquareModel(name, platform string, groupID int64, group string, pricing *userSupportedModelPricing) cursorModelSquareModel {
	return cursorModelSquareModel{
		Name:     name,
		Platform: platform,
		GroupID:  groupID,
		Group:    group,
		Pricing:  pricing,
	}
}

// buildPlatformSections 把一个渠道按 visibleGroups 的平台集合拆成有序的 section 列表：
// 每个 section 对应一个平台，只包含该平台的 groups 和 supported_models。
// 输出按 platform 字母序稳定排序，便于前端等效比较与回归测试。
func buildPlatformSections(
	ch service.AvailableChannel,
	visibleGroups []userAvailableGroup,
) []userChannelPlatformSection {
	groupsByPlatform := make(map[string][]userAvailableGroup, 4)
	for _, g := range visibleGroups {
		if g.Platform == "" {
			continue
		}
		groupsByPlatform[g.Platform] = append(groupsByPlatform[g.Platform], g)
	}
	if len(groupsByPlatform) == 0 {
		return nil
	}

	platforms := make([]string, 0, len(groupsByPlatform))
	for p := range groupsByPlatform {
		platforms = append(platforms, p)
	}
	sort.Strings(platforms)

	sections := make([]userChannelPlatformSection, 0, len(platforms))
	for _, platform := range platforms {
		platformSet := map[string]struct{}{platform: {}}
		sections = append(sections, userChannelPlatformSection{
			Platform:        platform,
			Groups:          groupsByPlatform[platform],
			SupportedModels: toUserSupportedModels(ch.SupportedModels, platformSet),
		})
	}
	return sections
}

// filterUserVisibleGroups 仅保留用户可访问的分组。
func filterUserVisibleGroups(
	groups []service.AvailableGroupRef,
	allowed map[int64]struct{},
) []userAvailableGroup {
	visible := make([]userAvailableGroup, 0, len(groups))
	for _, g := range groups {
		if _, ok := allowed[g.ID]; !ok {
			continue
		}
		visible = append(visible, userAvailableGroup{
			ID:               g.ID,
			Name:             g.Name,
			Platform:         g.Platform,
			SubscriptionType: g.SubscriptionType,
			RateMultiplier:   g.RateMultiplier,
			IsExclusive:      g.IsExclusive,
		})
	}
	return visible
}

// toUserSupportedModels 将 service 层支持模型转换为用户 DTO（字段白名单）。
// 仅保留平台在 allowedPlatforms 中的条目，防止跨平台模型信息泄漏。
// allowedPlatforms 为 nil 时不做平台过滤（保留全部，供测试或明确无过滤场景使用）。
func toUserSupportedModels(
	src []service.SupportedModel,
	allowedPlatforms map[string]struct{},
) []userSupportedModel {
	out := make([]userSupportedModel, 0, len(src))
	for i := range src {
		m := src[i]
		if allowedPlatforms != nil {
			if _, ok := allowedPlatforms[m.Platform]; !ok {
				continue
			}
		}
		out = append(out, userSupportedModel{
			Name:     m.Name,
			Platform: m.Platform,
			Pricing:  toUserPricing(m.Pricing),
		})
	}
	return out
}

// toUserPricing 将 service 层定价转换为用户 DTO；入参为 nil 时返回 nil。
func toUserPricing(p *service.ChannelModelPricing) *userSupportedModelPricing {
	if p == nil {
		return nil
	}
	intervals := make([]userPricingIntervalDTO, 0, len(p.Intervals))
	for _, iv := range p.Intervals {
		intervals = append(intervals, userPricingIntervalDTO{
			MinTokens:       iv.MinTokens,
			MaxTokens:       iv.MaxTokens,
			TierLabel:       iv.TierLabel,
			InputPrice:      iv.InputPrice,
			OutputPrice:     iv.OutputPrice,
			CacheWritePrice: iv.CacheWritePrice,
			CacheReadPrice:  iv.CacheReadPrice,
			PerRequestPrice: iv.PerRequestPrice,
		})
	}
	billingMode := string(p.BillingMode)
	if billingMode == "" {
		billingMode = string(service.BillingModeToken)
	}
	return &userSupportedModelPricing{
		BillingMode:      billingMode,
		InputPrice:       p.InputPrice,
		OutputPrice:      p.OutputPrice,
		CacheWritePrice:  p.CacheWritePrice,
		CacheReadPrice:   p.CacheReadPrice,
		ImageOutputPrice: p.ImageOutputPrice,
		PerRequestPrice:  p.PerRequestPrice,
		Intervals:        intervals,
	}
}
