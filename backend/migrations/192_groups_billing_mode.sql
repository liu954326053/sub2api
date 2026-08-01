-- Migration: 192_groups_billing_mode
-- groups 新增计价模式 billing_mode：
--   group_multiplier（默认，升级前行为：按分组统一倍率计费）
--   account_upstream（按账号级上游同步倍率计费，rate_multiplier 降级为兜底倍率）
-- 默认 group_multiplier 保证升级前行为不变；CHECK 约束限定枚举取值。
-- 设计见 openspec add-upstream-rate-sync（design.md Decisions 2）。

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS billing_mode TEXT NOT NULL DEFAULT 'group_multiplier';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM pg_constraint c
          JOIN pg_class t ON t.oid = c.conrelid
         WHERE t.relname = 'groups'
           AND c.conname = 'groups_billing_mode_check'
    ) THEN
        ALTER TABLE groups
            ADD CONSTRAINT groups_billing_mode_check
            CHECK (billing_mode IN ('group_multiplier', 'account_upstream'));
    END IF;
END $$;
