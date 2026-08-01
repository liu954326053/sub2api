## Why

当前用户计费统一按分组倍率（`Group.rate_multiplier`）折算。当一个分组同时挂载多个上游账号、而这些上游本身也是 Sub2API 实例且各账号上游成本不同时，分组统一倍率无法表达账号级成本差异，造成计费口径不精准。

现有 `upstream_billing_probe.go` 的上游计费探测能力存在结构性缺陷：仅支持 OpenAI 账号类型；只把探测结果存为 `extra.upstream_billing_probe` 快照、不写回 `accounts.rate_multiplier`，因此无法参与实际计费；没有多上游连接管理，一个实例只能对一个上游做固定探测；也没有同步日志，探测成功、失败、跳过均无记录可查。以上缺陷决定了该能力无法直接扩写，需要以新的“上游倍率同步”能力并列引入，旧探测按三阶段退役。

## What Changes

- 新增多上游连接管理：连接记录保存归一化唯一 `base_url`、`auth_mode: password|token`、AES-256-GCM 加密凭证、同步间隔（默认 30 分钟）、启停开关、最近同步状态与错误。
- 新增自动倍率同步：通过上游 `GET /api/v1/keys`（用户 JWT 鉴权）拉取该用户全部 key 及其内嵌 group 对象，按本地账号 `credentials` 中的 `api_key` 与上游 `sk-` key 字符串精确匹配，把上游 `rate_multiplier` 写回 `accounts.rate_multiplier`；只同步分组倍率，不同步用户专属倍率（产品决策）。
- 新增同构快照写回：同步同时写 `extra.upstream_billing_probe`（status/data{group_rate_multiplier, billing_scope:"token", peak 字段}/received_at/fresh_until/next_probe_at），使调度排序、调度缓存、列表排序 SQL、前端 `UpstreamBillingRateCell.vue`、CRS 同步保护五个现有消费方零改动复用。
- 新增同步日志：每次同步生成 `upstream_sync_runs` 记录（状态 success|partial|failed、拉取/匹配/更新/未变/未匹配五类计数、JSONB 明细数组），并配定期清理。
- 新增写回护栏：仅在值变化时写入；单次跳变超过 50% 跳过并记录 `threshold_skipped`；`extra.last_synced_rate` 三方比对防覆盖管理员手改（记录 `manual_override`）；本地有而上游无的账号记 `unmatched` 且不清值；多连接按 `base_url` 作用域隔离。
- 新增后台同步 runner：仿 `UpstreamBillingProbeService` 的 Start/Stop/runLoop/ticker 模式，Redis Leader 锁回退 PG advisory lock，Wire 注册与 `cmd/server` cleanup 接线。
- 新增管理 API `/admin/upstream-rate-sync/*`：连接 CRUD、连接测试（登录并拉一页 keys，返回发现数与预计匹配数）、立即同步、日志查询（按连接/状态/时间筛选）。
- 新增 JWT 自动化登录：账密模式调用上游 `POST /api/v1/auth/login`（不校验邮箱验证）；access token 24h、refresh token 30d，refresh 严格一次性轮转必须串行刷新并持久化最新 token；固定 UA 与稳定出口 IP 以满足 IP+UA 会话指纹绑定。
- 新增分组计价模式开关：`groups.billing_mode` 枚举字段，默认 `group_multiplier`（现状），可选 `account_upstream`（账号级倍率计费）。
- 计费入口改造：`gateway_usage_billing.go:677-688` 与 `openai_gateway_usage.go:154-166` 两条路径抽公共 helper；优先级链为系统默认 →（`account_upstream` 模式读命中账号 `BillingRateMultiplier()`，否则分组默认）→ 用户专属分组倍率始终覆盖 → 高峰倍率最后叠加；OpenAI 路径须先 `resolveCredentialAccount` 再读倍率；账号无同步值时回退分组倍率；认证缓存快照 `APIKeyAuthGroupSnapshot` 增加 `billing_mode` 并 bump 缓存版本。
- 新增前端“上游倍率同步”菜单 `/admin/upstream-rate-sync`，`features/upstream-rate-sync/` 自包含目录（连接表格、编辑弹窗含连接测试、同步日志表格与明细展开），接线 router、`AppSidebar.vue` 与 zh/en i18n；分组表单新增“计价模式”选择器，选 `account_upstream` 时 `rate_multiplier` 降级为兜底倍率并给出提示。
- 旧探测三阶段退役：P1 新同步上线、旧 runner 默认关闭但可手动开启（双生产者互斥规则明示）；P2 前端按钮/设置切换到新端点；P3 删除旧 runner/handler/repo 方法与 `extra.upstream_billing_probe_enabled` 死字段；`/v1/sub2api/billing` 服务端点保留（上游生态依赖）。
- 实施分期：P1 连接 CRUD + 手动同步 + 日志（闭环）；P2 定时 runner + 护栏 + 分组 `billing_mode`；P3 旧探测退役。
- 所有新增能力默认关闭，不改变升级前的计费与探测行为。

## Capabilities

### New Capabilities

- `upstream-rate-sync`: 定义多上游连接管理、凭证加密存储、JWT 登录与 refresh 轮转、`/api/v1/keys` 拉取与 key 精确匹配、`accounts.rate_multiplier` 写回与 `extra.upstream_billing_probe` 同构快照、写回护栏、后台 runner、连接测试、立即同步和同步日志查询。
- `group-billing-mode`: 定义分组 `billing_mode` 字段、两条计费入口的公共倍率解析 helper、优先级链与回退规则、认证缓存快照版本变更，以及分组表单上的计价模式选择与兜底提示。

### Modified Capabilities

无。仓库当前没有已发布的 OpenSpec capability 基线；现有 upstream billing probe 行为在本变更中作为兼容基线，不修改其正式需求语义，仅按三阶段计划退役。

## Impact

- **后端模块**：新增上游同步服务模块（连接管理、登录/刷新、同步执行、日志、runner）；`gateway_usage_billing.go` 与 `openai_gateway_usage.go` 抽公共倍率 helper；`APIKeyAuthGroupSnapshot` 加 `billing_mode`；Wire 注册仿 `service/wire.go:729`，`cmd/server/wire.go` 加 cleanup。
- **数据库**：迁移 192 为 `groups` 新增 `billing_mode` 字段（默认 `group_multiplier`）；迁移 193 新建 `upstream_connections` 与 `upstream_sync_runs` 两张表（顺序需要时可调换）；`upstream_sync_runs` 配定期清理任务。
- **Redis**：同步 runner 使用 LeaderLockCache 分布式锁，Redis 不可用时回退 PG advisory lock；认证缓存版本 bump 以使 `billing_mode` 快照生效；无新增热路径 Redis 数据。
- **管理 API**：新增 `/admin/upstream-rate-sync/*`（连接 CRUD、连接测试、立即同步、日志筛选查询），复用现有管理员鉴权。
- **前端**：新增 `frontend/src/features/upstream-rate-sync/` 与菜单 `/admin/upstream-rate-sync`；修改 `router/index.ts`、`AppSidebar.vue`、zh/en i18n；分组表单新增计价模式选择器；`UpstreamBillingRateCell.vue` 等现有消费组件零改动。
- **兼容性**：无外部 API breaking change；新功能默认关闭。`account_upstream` 模式仅影响显式开启的分组；未同步到值的账号回退分组倍率。`/v1/sub2api/billing` 服务端点保留。旧 probe 在 P1 默认关闭，P3 才删除代码。
- **安全与隐私**：连接凭证（账密/token）与刷新后的 access/refresh token 必须使用现有 SecretEncryptor（AES-256-GCM）加密存储，任何 API 响应、日志、同步明细不得输出明文或密文；同步服务固定 UA 并使用稳定出口 IP；上游开启 Turnstile 时账密自动化失效，文档必须明示降级为手动粘贴 token；用于同步的上游账号不得开启 TOTP。
