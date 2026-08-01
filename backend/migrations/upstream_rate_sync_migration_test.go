package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 迁移 192：groups 新增 billing_mode，要求幂等（IF NOT EXISTS + 约束存在性检查）
// 且默认值锁定升级前行为（group_multiplier）。
func TestMigration192GroupsBillingModeIsIdempotent(t *testing.T) {
	content, err := FS.ReadFile("192_groups_billing_mode.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS billing_mode TEXT NOT NULL DEFAULT 'group_multiplier'")
	// 不带 IF NOT EXISTS 的加列写法会导致重复执行失败
	require.NotContains(t, sql, "ADD COLUMN billing_mode")
	// CHECK 约束幂等：先查 pg_constraint 再加
	require.Contains(t, sql, "pg_constraint")
	require.Contains(t, sql, "groups_billing_mode_check")
	require.Contains(t, sql, "CHECK (billing_mode IN ('group_multiplier', 'account_upstream'))")
}

// 迁移 193：upstream_connections / upstream_sync_runs 两张新表，
// 要求幂等建表/建索引，并覆盖设计要求的约束与索引。
func TestMigration193UpstreamRateSyncTables(t *testing.T) {
	content, err := FS.ReadFile("193_upstream_rate_sync_tables.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")

	// 幂等建表与索引
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS upstream_connections")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS upstream_sync_runs")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS idx_upstream_connections_base_url")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_upstream_sync_runs_connection_started ON upstream_sync_runs (connection_id, started_at DESC)")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_upstream_sync_runs_status_started ON upstream_sync_runs (status, started_at DESC)")

	// 连接表：凭证密文列 + 默认关闭 + 默认间隔 30
	require.Contains(t, sql, "credentials_encrypted TEXT")
	require.Contains(t, sql, "access_token_encrypted TEXT")
	require.Contains(t, sql, "refresh_token_encrypted TEXT")
	require.Contains(t, sql, "enabled BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "interval_minutes INT NOT NULL DEFAULT 30")

	// run 表：级联删除 + 五计数默认值 + details JSONB
	require.Contains(t, sql, "connection_id BIGINT NOT NULL REFERENCES upstream_connections(id) ON DELETE CASCADE")
	require.Contains(t, sql, "keys_fetched INT NOT NULL DEFAULT 0")
	require.Contains(t, sql, "accounts_matched INT NOT NULL DEFAULT 0")
	require.Contains(t, sql, "accounts_updated INT NOT NULL DEFAULT 0")
	require.Contains(t, sql, "accounts_unchanged INT NOT NULL DEFAULT 0")
	require.Contains(t, sql, "accounts_unmatched INT NOT NULL DEFAULT 0")
	require.Contains(t, sql, "details JSONB NOT NULL DEFAULT '[]'::jsonb")

	// 枚举与非负计数 CHECK 约束（幂等：pg_constraint 存在性检查）
	require.Contains(t, sql, "CHECK (auth_mode IN ('password', 'token'))")
	require.Contains(t, sql, "CHECK (last_status IS NULL OR last_status IN ('success', 'partial', 'failed'))")
	require.Contains(t, sql, "CHECK (status IN ('success', 'partial', 'failed'))")
	require.Contains(t, sql, "upstream_sync_runs_counters_nonnegative_check")
	require.Contains(t, sql, "keys_fetched >= 0")
	require.Contains(t, sql, "accounts_unmatched >= 0")
}
