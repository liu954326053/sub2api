//go:build unit

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUserAvailableChannel_Unauthenticated401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AvailableChannelHandler{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/channels/available", nil)

	h.List(c)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestFilterUserVisibleGroups_IntersectionOnly(t *testing.T) {
	groups := []service.AvailableGroupRef{
		{ID: 1, Name: "g1", Platform: "anthropic"},
		{ID: 2, Name: "g2", Platform: "anthropic"},
		{ID: 3, Name: "g3", Platform: "openai"},
	}
	allowed := map[int64]struct{}{1: {}, 3: {}}

	visible := filterUserVisibleGroups(groups, allowed)

	require.Len(t, visible, 2)
	ids := []int64{visible[0].ID, visible[1].ID}
	require.ElementsMatch(t, []int64{1, 3}, ids)
}

func TestToUserSupportedModels_FiltersByAllowedPlatforms(t *testing.T) {
	src := []service.SupportedModel{
		{Name: "claude-sonnet-4-6", Platform: "anthropic", Pricing: nil},
		{Name: "gpt-4o", Platform: "openai", Pricing: nil},
	}
	allowed := map[string]struct{}{"anthropic": {}}

	out := toUserSupportedModels(src, allowed)

	require.Len(t, out, 1)
	require.Equal(t, "claude-sonnet-4-6", out[0].Name)
}

func TestToUserSupportedModels_NilAllowedPlatformsKeepsAll(t *testing.T) {
	src := []service.SupportedModel{
		{Name: "a", Platform: "anthropic"},
		{Name: "b", Platform: "openai"},
	}

	require.Len(t, toUserSupportedModels(src, nil), 2)
}

func TestUserAvailableChannel_FieldWhitelist(t *testing.T) {
	row := userAvailableChannel{
		Name:        "ch",
		Description: "d",
		Platforms: []userChannelPlatformSection{
			{
				Platform:        "anthropic",
				Groups:          []userAvailableGroup{{ID: 1, Name: "g1", Platform: "anthropic"}},
				SupportedModels: []userSupportedModel{},
			},
		},
	}
	raw, err := json.Marshal(row)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	for _, key := range []string{"id", "status", "billing_model_source", "restrict_models"} {
		_, exists := decoded[key]
		require.Falsef(t, exists, "user DTO must not expose %q", key)
	}
	for _, key := range []string{"name", "description", "platforms"} {
		_, exists := decoded[key]
		require.Truef(t, exists, "user DTO must expose %q", key)
	}

	rawSection, err := json.Marshal(row.Platforms[0])
	require.NoError(t, err)
	var sectionDecoded map[string]any
	require.NoError(t, json.Unmarshal(rawSection, &sectionDecoded))
	for _, key := range []string{"platform", "groups", "supported_models"} {
		_, exists := sectionDecoded[key]
		require.Truef(t, exists, "platform section must expose %q", key)
	}

	rawGroup, err := json.Marshal(row.Platforms[0].Groups[0])
	require.NoError(t, err)
	var groupDecoded map[string]any
	require.NoError(t, json.Unmarshal(rawGroup, &groupDecoded))
	for _, key := range []string{"id", "name", "platform", "subscription_type", "rate_multiplier", "is_exclusive"} {
		_, exists := groupDecoded[key]
		require.Truef(t, exists, "group DTO must expose %q", key)
	}

	pricing := toUserPricing(&service.ChannelModelPricing{
		BillingMode: service.BillingModeToken,
		Intervals: []service.PricingInterval{
			{ID: 7, MinTokens: 0, MaxTokens: nil, SortOrder: 3},
		},
	})
	require.NotNil(t, pricing)
	require.Len(t, pricing.Intervals, 1)
	rawIv, err := json.Marshal(pricing.Intervals[0])
	require.NoError(t, err)
	var ivDecoded map[string]any
	require.NoError(t, json.Unmarshal(rawIv, &ivDecoded))
	for _, key := range []string{"id", "pricing_id", "sort_order"} {
		_, exists := ivDecoded[key]
		require.Falsef(t, exists, "user pricing interval must not expose %q", key)
	}
}

func TestBuildPlatformSections_GroupsByPlatform(t *testing.T) {
	ch := service.AvailableChannel{
		Name: "ch",
		SupportedModels: []service.SupportedModel{
			{Name: "claude-sonnet-4-6", Platform: "anthropic"},
			{Name: "gpt-4o", Platform: "openai"},
		},
	}
	visible := []userAvailableGroup{
		{ID: 1, Name: "g-openai", Platform: "openai"},
		{ID: 2, Name: "g-ant", Platform: "anthropic"},
		{ID: 3, Name: "g-empty", Platform: ""},
	}

	sections := buildPlatformSections(ch, visible)

	require.Len(t, sections, 2)
	require.Equal(t, "anthropic", sections[0].Platform)
	require.Equal(t, "openai", sections[1].Platform)
	require.Len(t, sections[0].Groups, 1)
	require.Equal(t, int64(2), sections[0].Groups[0].ID)
	require.Len(t, sections[0].SupportedModels, 1)
	require.Equal(t, "claude-sonnet-4-6", sections[0].SupportedModels[0].Name)
}

func TestCursorModelDefaultNames_ReusesGatewayDefaultCatalogs(t *testing.T) {
	openAIModels := cursorModelDefaultNames("openai")
	require.Len(t, openAIModels, len(openai.DefaultModels))
	require.Contains(t, openAIModels, "gpt-5.4")
	require.Contains(t, openAIModels, "gpt-image-2")

	claudeModels := cursorModelDefaultNames("claude")
	require.Len(t, claudeModels, len(claude.DefaultModels))
	require.Contains(t, claudeModels, "claude-sonnet-4-6")
	require.Contains(t, claudeModels, "claude-sonnet-4-5-20250929")
}

func TestApplyPricingMultiplier_MultipliesAllDisplayedFields(t *testing.T) {
	pricing := &service.ChannelModelPricing{
		BillingMode:      service.BillingModeToken,
		InputPrice:       testFloat64Ptr(1),
		OutputPrice:      testFloat64Ptr(2),
		CacheWritePrice:  testFloat64Ptr(3),
		CacheReadPrice:   testFloat64Ptr(4),
		ImageOutputPrice: testFloat64Ptr(5),
		PerRequestPrice:  testFloat64Ptr(6),
		Intervals: []service.PricingInterval{{
			InputPrice:      testFloat64Ptr(7),
			OutputPrice:     testFloat64Ptr(8),
			CacheWritePrice: testFloat64Ptr(9),
			CacheReadPrice:  testFloat64Ptr(10),
			PerRequestPrice: testFloat64Ptr(11),
		}},
	}

	got := applyPricingMultiplier(pricing, 1.5)

	require.NotSame(t, pricing, got)
	require.InDelta(t, 1.5, *got.InputPrice, 1e-12)
	require.InDelta(t, 3.0, *got.OutputPrice, 1e-12)
	require.InDelta(t, 4.5, *got.CacheWritePrice, 1e-12)
	require.InDelta(t, 6.0, *got.CacheReadPrice, 1e-12)
	require.InDelta(t, 7.5, *got.ImageOutputPrice, 1e-12)
	require.InDelta(t, 9.0, *got.PerRequestPrice, 1e-12)
	require.InDelta(t, 10.5, *got.Intervals[0].InputPrice, 1e-12)
	require.InDelta(t, 12.0, *got.Intervals[0].OutputPrice, 1e-12)
	require.InDelta(t, 13.5, *got.Intervals[0].CacheWritePrice, 1e-12)
	require.InDelta(t, 15.0, *got.Intervals[0].CacheReadPrice, 1e-12)
	require.InDelta(t, 16.5, *got.Intervals[0].PerRequestPrice, 1e-12)
	require.InDelta(t, 1.0, *pricing.InputPrice, 1e-12)
}

func testFloat64Ptr(v float64) *float64 {
	return &v
}
