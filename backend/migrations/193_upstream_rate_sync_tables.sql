-- Migration: 193_upstream_rate_sync_tables
-- 上游倍率同步（upstream rate sync）数据基础：
--   upstream_connections：多上游连接管理，凭证与令牌只存 AES-256-GCM 密文列
--   upstream_sync_runs：每次同步一条 run 记录（五个计数 + JSONB 明细数组）
-- 保留策略：每连接保留最近 200 条且 30 天内的 run，超出部分由同步 runner 定期分批删除。
-- 设计见 openspec add-upstream-rate-sync（design.md Decisions 1）。

CREATE TABLE IF NOT EXISTS upstream_connections (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    base_url TEXT NOT NULL,
    auth_mode TEXT NOT NULL,
    credentials_encrypted TEXT,
    access_token_encrypted TEXT,
    refresh_token_encrypted TEXT,
    token_expires_at TIMESTAMPTZ,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    interval_minutes INT NOT NULL DEFAULT 30,
    last_sync_at TIMESTAMPTZ,
    last_status TEXT,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- base_url 归一化结果全局唯一（多连接按 base_url 作用域隔离）
CREATE UNIQUE INDEX IF NOT EXISTS idx_upstream_connections_base_url
    ON upstream_connections (base_url);

CREATE TABLE IF NOT EXISTS upstream_sync_runs (
    id BIGSERIAL PRIMARY KEY,
    connection_id BIGINT NOT NULL REFERENCES upstream_connections(id) ON DELETE CASCADE,
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    status TEXT NOT NULL DEFAULT 'success',
    keys_fetched INT NOT NULL DEFAULT 0,
    accounts_matched INT NOT NULL DEFAULT 0,
    accounts_updated INT NOT NULL DEFAULT 0,
    accounts_unchanged INT NOT NULL DEFAULT 0,
    accounts_unmatched INT NOT NULL DEFAULT 0,
    details JSONB NOT NULL DEFAULT '[]'::jsonb,
    error TEXT
);

-- 日志页按连接/状态/时间筛选
CREATE INDEX IF NOT EXISTS idx_upstream_sync_runs_connection_started
    ON upstream_sync_runs (connection_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_upstream_sync_runs_status_started
    ON upstream_sync_runs (status, started_at DESC);

-- 枚举与非负计数约束（幂等：已存在则跳过）
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint c
          JOIN pg_class t ON t.oid = c.conrelid
         WHERE t.relname = 'upstream_connections'
           AND c.conname = 'upstream_connections_auth_mode_check'
    ) THEN
        ALTER TABLE upstream_connections
            ADD CONSTRAINT upstream_connections_auth_mode_check
            CHECK (auth_mode IN ('password', 'token'));
    END IF;

    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint c
          JOIN pg_class t ON t.oid = c.conrelid
         WHERE t.relname = 'upstream_connections'
           AND c.conname = 'upstream_connections_last_status_check'
    ) THEN
        ALTER TABLE upstream_connections
            ADD CONSTRAINT upstream_connections_last_status_check
            CHECK (last_status IS NULL OR last_status IN ('success', 'partial', 'failed'));
    END IF;

    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint c
          JOIN pg_class t ON t.oid = c.conrelid
         WHERE t.relname = 'upstream_sync_runs'
           AND c.conname = 'upstream_sync_runs_status_check'
    ) THEN
        ALTER TABLE upstream_sync_runs
            ADD CONSTRAINT upstream_sync_runs_status_check
            CHECK (status IN ('success', 'partial', 'failed'));
    END IF;

    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint c
          JOIN pg_class t ON t.oid = c.conrelid
         WHERE t.relname = 'upstream_sync_runs'
           AND c.conname = 'upstream_sync_runs_counters_nonnegative_check'
    ) THEN
        ALTER TABLE upstream_sync_runs
            ADD CONSTRAINT upstream_sync_runs_counters_nonnegative_check
            CHECK (keys_fetched >= 0
               AND accounts_matched >= 0
               AND accounts_updated >= 0
               AND accounts_unchanged >= 0
               AND accounts_unmatched >= 0);
    END IF;
END $$;
