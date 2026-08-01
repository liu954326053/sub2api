package upstreamratesync

import (
	"strconv"
	"strings"
	"time"
)

// 快照同构约束：Data 的字段名与 service/upstream_billing_probe.go 中
// parseUpstreamBillingProbeResponse 产出的 map 完全一致，使调度排序、调度缓存、
// 列表排序 SQL、前端 Cell、CRS 同步保护五个消费方零改动复用（design Decisions 8）。
//
// 产品决策：只写分组倍率，resolved_rate_multiplier 直接等于分组倍率。
// keys DTO 的 group 不含 timezone，peak 字段按 DTO 实有配置填写；
// 无 peak 配置时 peak_rate_enabled 为 false。

// buildAccountSnapshot 构造一次写回的同构快照。
// interval 为连接同步间隔：fresh_until = received + 2×interval，
// next_probe_at = received + interval（与旧 probe 的"到期"语义对齐）。
func buildAccountSnapshot(group *upstreamKeyGroup, now time.Time, interval time.Duration) *AccountSnapshot {
	received := now.UTC()
	return &AccountSnapshot{
		Data:        buildAccountSnapshotData(group, received),
		ReceivedAt:  received,
		FreshUntil:  received.Add(2 * interval),
		NextProbeAt: received.Add(interval),
	}
}

func buildAccountSnapshotData(group *upstreamKeyGroup, observedAt time.Time) map[string]any {
	appliedPeak := 1.0
	if group.PeakRateEnabled && withinPeakWindow(group.PeakStart, group.PeakEnd, observedAt) {
		appliedPeak = group.PeakRateMultiplier
	}
	data := map[string]any{
		"object":                    "sub2api.key_billing",
		"schema_version":            1,
		"billing_scope":             "token",
		"group_rate_multiplier":     group.RateMultiplier,
		"resolved_rate_multiplier":  group.RateMultiplier,
		"peak_rate_enabled":         group.PeakRateEnabled,
		"applied_peak_multiplier":   appliedPeak,
		"effective_rate_multiplier": group.RateMultiplier * appliedPeak,
		"observed_at":               observedAt.UTC().Format(time.RFC3339Nano),
	}
	if group.PeakRateEnabled {
		data["peak_start"] = group.PeakStart
		data["peak_end"] = group.PeakEnd
		data["peak_rate_multiplier"] = group.PeakRateMultiplier
	}
	return data
}

// withinPeakWindow 判断 now 是否落在 "HH:MM" 起止窗口内（按 UTC 折算分钟；
// keys DTO 无 timezone 字段，消费方读取快照时会按其自身逻辑现算）。
// 配置无法解析时保守返回 false（applied_peak_multiplier 记 1）。
func withinPeakWindow(start, end string, now time.Time) bool {
	startMinute, okStart := parsePeakMinutes(start)
	endMinute, okEnd := parsePeakMinutes(end)
	if !okStart || !okEnd || startMinute >= endMinute {
		return false
	}
	utc := now.UTC()
	minute := utc.Hour()*60 + utc.Minute()
	return minute >= startMinute && minute < endMinute
}

// parsePeakMinutes 解析 "HH:MM" 为当日分钟数。
func parsePeakMinutes(value string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, false
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, false
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}
