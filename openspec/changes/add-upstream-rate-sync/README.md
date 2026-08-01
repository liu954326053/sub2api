# add-upstream-rate-sync

新增多上游连接管理与分组倍率自动同步：本地作为下游定时登录上游 Sub2API 实例拉取 `GET /api/v1/keys` 内嵌的分组 `rate_multiplier` 与高峰配置，按 api_key 字符串精确匹配写回本地 `accounts.rate_multiplier` 与 `extra.upstream_billing_probe` 同构快照，并为分组新增 `billing_mode`（group_multiplier / account_upstream）计价模式开关。

## 文档索引

| 文档 | 内容 |
| ---- | ---- |
| `proposal.md` | Why / What Changes / Capabilities / Impact |
| `design.md` | 同步机制、JWT 自动化、数据模型、计费优先级链、写回护栏、旧探测退役方案 |
| `tasks.md` | 分期实施清单（数据模型与 CRUD → 同步闭环 → 定时 runner 与计价模式 → 旧探测退役） |
| `specs/upstream-rate-sync/spec.md` | 连接管理、同步写回、护栏、日志与管理 API 需求 |
| `specs/group-billing-mode/spec.md` | 分组 `billing_mode` 与两条计费入口公共 helper 的优先级链需求 |

## 实施分期

- **P1 同步闭环**：迁移 193 建 `upstream_connections` / `upstream_sync_runs` 两表；连接 CRUD + 连接测试（登录 + 拉一页 keys 返回发现数/预计匹配数）+ 手动触发同步 + 日志查询（按连接/状态/时间筛选）；管理 API 前缀 `/admin/upstream-rate-sync/*`；前端 `features/upstream-rate-sync/` 自包含目录与 `/admin/upstream-rate-sync` 菜单。
- **P2 定时 runner 与计价模式**：仿 `UpstreamBillingProbeService` 的后台任务（Start/Stop/runLoop/ticker/LeaderLockCache Redis 锁回退 PG advisory lock，wire 注册仿 `service/wire.go:729`）；写回护栏（仅变化才写、单次跳变 >50% 跳过并记录、`last_synced_rate` 三方比对、unmatched 不清值）；迁移 192 为 `groups` 增加 `billing_mode`；两条计费入口（`gateway_usage_billing.go:677-688`、`openai_gateway_usage.go:154-166`）抽公共 helper；认证缓存快照 `APIKeyAuthGroupSnapshot` 加 `billing_mode` 并 bump 缓存版本；分组表单加“计价模式”选择器。
- **P3 旧探测退役**：前端按钮/设置切换到新端点；删除旧 runner/handler/repo 方法与 `extra.upstream_billing_probe_enabled` 死字段；`/v1/sub2api/billing` 服务端点保留（上游生态依赖）。P1 阶段旧 runner 默认关、可手动开，双生产者互斥必须在 UI 与文档中明示。

## 关键设计决策速览

- **匹配键**：本地账号 credentials JSONB 中的 `api_key` 即上游 sk- key（`service/account.go:1274/1368`），按字符串精确匹配，不做名称映射；多连接间按归一化 `base_url` 作用域隔离。
- **写回目标**：只写分组倍率到 `accounts.rate_multiplier`（账号成本口径，nil/负数回退 1.0），不处理用户专属倍率；同时写 `extra.upstream_billing_probe` 同构快照，使调度排序（`openai_account_scheduler.go:2700-2721`）、调度缓存、列表排序 SQL（`account_repo.go:1036-1068`）、前端 `UpstreamBillingRateCell.vue`、CRS 同步保护（`crs_sync_service.go:1157-1185`）五个消费方零改动复用。
- **计费优先级链**：系统默认 →（`account_upstream` 模式读命中账号 `BillingRateMultiplier()`，否则分组默认）→ 用户专属分组倍率始终覆盖 → 高峰倍率最后叠加；OpenAI 路径先 `resolveCredentialAccount` 再读倍率；账号无同步值时回退分组倍率。
- **JWT 自动化**：账密走 `POST /api/v1/auth/login`；access 24h / refresh 30d，refresh 严格一次性轮转、必须串行刷新并持久化最新 token；会话绑定 IP+UA 指纹，同步服务固定 UA + 稳定出口 IP；凭证用现有 SecretEncryptor（AES-256-GCM）加密存储；上游开 Turnstile 时降级为手动粘贴 token（文档明示），同步账号不得开 TOTP。
- **写回护栏**：仅在值变化时写；单次跳变 >50% 跳过并记录 `threshold_skipped`；`extra.last_synced_rate` 三方比对防覆盖管理员手改（记 `manual_override`）；本地有、上游无 → `unmatched` 不清值；`upstream_sync_runs` 记录五类计数与 details JSONB 明细并定期清理。
