-- upstream_connections 增加上游余额快照列（同步时经 GET /api/v1/user/profile 读取）。
ALTER TABLE upstream_connections
    ADD COLUMN IF NOT EXISTS last_balance DOUBLE PRECISION;
