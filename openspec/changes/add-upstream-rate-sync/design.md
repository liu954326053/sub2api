## Context

### 当前系统

sub2api 的用户计费按分组倍率（`Group.rate_multiplier`）计算，同时支持按账号的 `accounts.rate_multiplier`（成本口径，`decimal(10,4)`，默认 1.0）和用户专属分组倍率。当本实例的"上游"也是 sub2api 实例时，管理员需要手工逐账号核对上游分组倍率并回填本地账号，无法自动化，且随上游调整很快失真。

现有相关机制：

- 上游（同为 sub2api）`GET /api/v1/keys`（用户 JWT 鉴权）返回该用户全部 key，每把 key 内嵌 `group` 对象，含 `rate_multiplier`/`platform`/peak 配置。本地实现路径：`backend/internal/repository/api_key_repo.go:447`（`WithGroup`）→ `backend/internal/dto/mappers.go:177`。
- 本地账号凭证存 `accounts.credentials` JSONB（`api_key` + `base_url`，见 `backend/internal/service/account.go:1274/1368`）。本地账号的 `api_key` 就是上游签发的 `sk-` key，可按字符串精确匹配，不需要名称映射。
- `accounts.rate_multiplier` 语义为账号成本口径；`Account.BillingRateMultiplier()` 约定：nil → 1.0，负数 → 1.0。
- 已有 `UpstreamBillingProbeService`（`backend/internal/service/upstream_billing_probe.go`）通过 `/v1/sub2api/billing` 端点逐账号探测上游倍率，并把同构快照写入 `accounts.extra.upstream_billing_probe`。该服务提供了可复用的后台任务范式：`Start`/`Stop`/`runLoop`/ticker/`SetLeaderLock`（Redis `LeaderLockCache` 锁回退 PG advisory lock）、wire 注册（`backend/internal/service/wire.go:729`、`backend/cmd/server/wire.go:113/329` cleanup）。
- 快照已有 5 个消费方：调度排序（`backend/internal/repository/openai_account_scheduler.go:2700-2721`）、调度缓存（`backend/internal/repository/scheduler_cache.go:1015-1038`）、列表排序 SQL（`backend/internal/repository/account_repo.go:1036-1068`）、前端 `UpstreamBillingRateCell.vue`、CRS 同步保护（`backend/internal/service/crs_sync_service.go:1157-1185`）。
- 两条计费入口分别位于 `backend/internal/handler/gateway_usage_billing.go:677-688` 与 `backend/internal/handler/openai_gateway_usage.go:154-166`，各自实现倍率解析，存在重复。

### 目标项目约束

- PostgreSQL SQL migrations 是 schema 的事实源，Ent 自动迁移不是生产建表入口；当前最大迁移序号为 191，本变更使用 192（groups 字段）与 193（两张新表），若实施时顺序需要可调换。
- 后端 Go + Gin + Wire；前端 Vue 3 + TypeScript + pnpm；Redis 已是运行基础设施。
- 新功能默认不影响升级前行为：未创建连接、分组仍为 `group_multiplier` 时，计费路径结果必须与升级前一致。
- 上游凭证与 token 必须使用现有 `SecretEncryptor`（AES-256-GCM）加密存储，明文不得进入日志、API 响应或前端状态。

## Goals / Non-Goals

**Goals:**

- 支持多上游连接管理：每个连接保存一个上游 sub2api 实例的地址与鉴权，独立定时同步。
- 自动把上游分组倍率同步到本地账号：写回 `accounts.rate_multiplier`，并写入与现有 probe 同构的 `extra.upstream_billing_probe` 快照，使 5 个既有消费方零改动复用。
- 分组新增 `billing_mode` 计价模式开关：`group_multiplier`（默认，现行为）与 `account_upstream`（按账号上游倍率计费，分组倍率降级为兜底）。
- 两条计费入口抽取公共 helper，优先级链统一：系统默认 →（account_upstream 模式读命中账号 `BillingRateMultiplier()`，否则分组默认）→ 用户专属分组倍率始终覆盖 → ×高峰最后叠加。
- 提供连接测试（登录 + 拉一页 keys，返回发现数/预计匹配数）、立即同步、同步日志查询与定期清理。
- 同步过程可审计：每次同步生成 run 记录与逐账号明细（old_rate/new_rate/action）。
- 三阶段退役旧 `UpstreamBillingProbeService` runner，保留 `/v1/sub2api/billing` 服务端点。

**Non-Goals:**

- 不同步用户专属倍率（`user_rate_multiplier`）：只写分组倍率，这是产品决策（见 Decisions 8）。
- 不实现上游名称 → 本地分组的映射配置：本地账号直接用上把 key 内嵌的 group 倍率。
- 不改变用户专属分组倍率、高峰叠加、账号成本口径以外的计费语义。
- 不在本变更中删除旧 probe runner（P3 阶段才删），不改动 `content_moderation`、调度主逻辑或网关协议 envelope。
- 不为非 sub2api 上游（OpenAI 官方、其他网关）实现倍率同步；连接测试和同步只针对 sub2api 兼容实例。

## 总体架构

```text
┌─────────────────────────── 本地 sub2api ───────────────────────────┐
│                                                                    │
│  upstream_connections ──► UpstreamRateSyncService (runLoop/ticker) │
│         │                     │ leader lock (Redis→PG advisory)    │
│         │                     ▼                                    │
│         │              JWT 会话管理 (login/refresh 串行, 加密持久化)│
│         │                     ▼                                    │
│         │              翻页 GET 上游 /api/v1/keys (JWT 鉴权)        │
│         │                     ▼                                    │
│         │              key 匹配: credentials.api_key 字符串精确匹配 │
│         │              (按 base_url 归一化作用域隔离)               │
│         │                     ▼                                    │
│         │        写回 accounts.rate_multiplier (护栏: 变化才写/    │
│         │        跳变>50%跳过/last_synced_rate 三方比对/unmatched 不清)│
│         │                     ▼                                    │
│         │        写 extra.upstream_billing_probe 同构快照           │
│         │                     ▼                                    │
│         │        5 个既有消费方零改动复用 (调度排序/调度缓存/        │
│         │        列表排序 SQL/前端 Cell/CRS 同步保护)               │
│         │                     ▼                                    │
│  upstream_sync_runs ◄── 每次同步生成 run + 明细, 定期清理           │
│                                                                    │
│  groups.billing_mode ──► 计费公共 helper                            │
│  (gateway_usage_billing.go / openai_gateway_usage.go 两处接入)      │
│                                                                    │
│  /admin/upstream-rate-sync/* ──► 前端 features/upstream-rate-sync/  │
└────────────────────────────────────────────────────────────────────┘
```

## Decisions

### 1. 新增 `upstream_connections` 与 `upstream_sync_runs` 两张表（迁移 193）

`upstream_connections`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| id | bigserial PK | |
| name | text not null | 管理员可读名 |
| base_url | text not null | 归一化后唯一（见 Decisions 3 的作用域规则），建唯一索引 |
| auth_mode | text not null | `password` \| `token` |
| credentials_encrypted | bytea | password 模式下的账密，SecretEncryptor 密文；token 模式存粘贴的 access token |
| access_token_encrypted | bytea | 当前 access token 密文 |
| refresh_token_encrypted | bytea | 当前 refresh token 密文（password 模式） |
| token_expires_at | timestamptz | access token 到期时间，提前刷新 |
| enabled | bool not null default false | 定时同步开关；默认关闭 |
| interval_minutes | int not null default 30 | 同步间隔，边界校验仿 probe（5–1440） |
| last_sync_at | timestamptz | 最近一次同步时间 |
| last_status | text | 最近一次结果：success \| partial \| failed |
| last_error | text | 最近一次错误摘要（脱敏，不含 token/密码） |
| created_at / updated_at | timestamptz | |

`upstream_sync_runs`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| id | bigserial PK | |
| connection_id | bigint not null FK → upstream_connections ON DELETE CASCADE | |
| started_at / finished_at | timestamptz | finished_at 可空表示进行中 |
| status | text not null | success \| partial \| failed |
| keys_fetched | int not null default 0 | 上游拉到的 key 总数 |
| accounts_matched | int not null default 0 | 匹配到本地账号数 |
| accounts_updated | int not null default 0 | 实际写回数 |
| accounts_unchanged | int not null default 0 | 值未变化跳过数 |
| accounts_unmatched | int not null default 0 | 本地 key 在上游找不到数 |
| details | JSONB | 明细数组：`{account_id, key_prefix, group_name, old_rate, new_rate, action}`，action ∈ updated \| unchanged \| unmatched \| threshold_skipped \| manual_override |
| error | text | 失败原因摘要 |

索引：`upstream_sync_runs(connection_id, started_at desc)`、`upstream_sync_runs(status, started_at desc)`，支撑日志页按连接/状态/时间筛选。

清理策略：每连接保留最近 200 条且 30 天内的 run；清理随 runLoop 周期执行，超出部分按 id 分批删除。

### 2. `groups` 新增 `billing_mode` 字段（迁移 192）

- `groups.billing_mode text not null default 'group_multiplier'`，枚举：`group_multiplier` | `account_upstream`，加 CHECK 约束。
- 默认 `group_multiplier` 即升级前行为；`account_upstream` 时 `rate_multiplier` 语义降级为"账号无同步值时的兜底倍率"。
- Ent schema 同步加字段与常量；管理 API/前端分组表单加"计价模式"选择器，选 `account_upstream` 时提示 rate_multiplier 降级为兜底。

### 3. base_url 归一化与连接作用域隔离

- 保存连接时归一化 base_url：小写 scheme/host、去尾斜杠、去默认端口；归一化结果作为唯一约束键。
- 账号归属判定：本地账号 `credentials.base_url` 经同一归一化后等于连接 base_url，才属于该连接的作用域。
- 多连接间严格按作用域隔离：连接 A 的同步 MUST NOT 写作用域外的账号，即使 api_key 字符串碰巧相同。

### 4. 同步引擎：仿 UpstreamBillingProbeService 的后台任务模式

新增 `UpstreamRateSyncService`（`backend/internal/service/upstream_rate_sync*.go`）：

- 生命周期 `Start()`/`Stop()`/`runLoop()`，ticker 每分钟检查 due 连接（enabled 且 `last_sync_at + interval <= now`）；构造函数不得启动 goroutine。
- leader lock：`SetLeaderLock(LeaderLockCache, *sql.DB)`，Redis 锁回退 PG advisory lock，仿 `upstream_billing_probe.go:501` 的 `tryAcquireLeaderLock`，保证多实例单活。
- 每次同步在事务外逐账号写回（单账号失败不影响整批），run 记录最后落库。
- Wire 注册仿 `service/wire.go:729` 的 provider 与 `cmd/server/wire.go` 的 cleanup 接线；服务启动失败不得阻断主 API。

### 5. JWT 生命周期管理

- 登录：password 模式用账密 `POST {上游}/api/v1/auth/login`（不校验邮箱验证）；token 模式直接使用管理员粘贴的 access token，自动刷新不可用。
- access token 有效期 24h，refresh token 30d；refresh 为严格一次性轮转，MUST 串行刷新（单连接单 flight + 连接级互斥锁），刷新成功后 MUST 立即加密持久化最新 access/refresh token 与 token_expires_at，禁止并发刷新导致旧 refresh 已失效的新 token 丢失（自残）。
- 会话绑定 = IP + UA 指纹：同步服务使用固定 UA 字符串与稳定出口 IP（部署约束写入文档），避免 token 因指纹漂移失效。
- 死穴：上游开启 Turnstile 时账密自动化失效。连接测试检测到 Turnstile 时必须返回明确错误，文档明示降级路径：auth_mode=token，由管理员手动粘贴 token（无自动刷新，到期需重新粘贴）。
- 同步所用上游账号 MUST 关闭 TOTP；开启 TOTP 的账号无法账密自动化登录，同样降级为 token 模式。

### 6. 翻页拉取与匹配规则

- 翻页调用上游 `GET /api/v1/keys`（用户 JWT 鉴权），页大小取上游分页上限，累计至全部 key；单页失败按可重试错误重试（有界次数+退避），整批失败则 run=failed。
- 每把 key 取内嵌 `group.rate_multiplier`、`group.name`、`group.platform` 及 peak 配置字段。
- 匹配：在连接 base_url 作用域内，按本地账号 `credentials.api_key` 与上游 key 字符串精确匹配；不需要名称映射。
- 同一 key 匹配多个本地账号（理论上同一 sk 不应重复）时，全部写回并在 details 各记一条。

### 7. 写回护栏

- 只在值变化时写：`new_rate == old_rate` 记 `unchanged`，不更新。
- 单次跳变 >50%（`|new-old|/old > 0.5`，old 为 0 或 nil 时按绝对阈值处理）跳过写回，details 记 `threshold_skipped`，run 状态降为 partial。
- 三方比对防覆盖管理员手改：`accounts.extra` 记 `last_synced_rate`（本服务上次写入的值）。写回前比较：当前 `rate_multiplier != last_synced_rate` 且 `last_synced_rate` 存在，说明管理员手改过，本次跳过并在 details 记 `manual_override`；管理员手改值 MUST 保留，直到其与上游新值一致或管理员清除标记。
- 本地有账号、上游无对应 key → `unmatched`，MUST NOT 清空 `rate_multiplier` 或快照（可能是上游删 key 或临时分页问题）。
- 写回成功同时更新 `extra.last_synced_rate = new_rate` 与 Decisions 8 的快照。

### 8. 快照兼容层：`extra.upstream_billing_probe` 同构写入

同步写回时在 `accounts.extra.upstream_billing_probe` 写入与现有 probe 完全同构的快照（结构见 `upstream_billing_probe.go:76-82`）：

```text
status: "ok"
data: {
  billing_scope: "token",
  group_rate_multiplier: <上游 group.rate_multiplier>,
  resolved_rate_multiplier: <同 group_rate_multiplier>,
  peak_rate_enabled, peak_start, peak_end, peak_rate_multiplier,
  applied_peak_multiplier, effective_rate_multiplier, timezone,
  observed_at
}
received_at, fresh_until, next_probe_at (= received_at + interval)
```

产品决策：**只写分组倍率**。`user_rate_multiplier` 不入快照；`resolved_rate_multiplier` 直接等于分组倍率，即快照口径 = 分组口径而非上游 resolved 口径。由此 5 个既有消费方零改动复用：

1. 调度排序 `openai_account_scheduler.go:2700-2721`
2. 调度缓存 `scheduler_cache.go:1015-1038`
3. 列表排序 SQL `account_repo.go:1036-1068`
4. 前端 `UpstreamBillingRateCell.vue`
5. CRS 同步保护 `crs_sync_service.go:1157-1185`

`next_probe_at` 由同步服务按连接 interval 写入，使"到期"语义对消费方保持一致；旧 probe runner 关闭后（见 Decisions 10）该字段的唯一生产者即本服务。

### 9. 计价模式：公共 helper 与优先级链

抽取公共 helper（如 `service` 或 `handler` 层的 `resolveBillingRate(...)`），供两条计费入口 `gateway_usage_billing.go:677-688` 与 `openai_gateway_usage.go:154-166` 共同调用，消除重复实现。

优先级链（高优先级覆盖低优先级，高峰最后叠加）：

1. 系统默认 1.0。
2. 分组 `billing_mode == account_upstream`：读命中账号 `BillingRateMultiplier()`（nil/负数 → 回退分组 `rate_multiplier`）；否则使用分组 `rate_multiplier`。
3. 用户专属分组倍率始终覆盖 2（与现行为一致）。
4. 高峰倍率最后叠加（与现行为一致）。

约束：

- OpenAI 路径 MUST 先 `resolveCredentialAccount` 解析出影子账号，再读账号倍率；未解析到账号时按分组兜底。
- 认证缓存快照 `APIKeyAuthGroupSnapshot` MUST 增加 `billing_mode` 字段，并 bump 缓存版本，避免旧快照被误读为新语义。
- 账号无同步值（`rate_multiplier` 为 nil）时 MUST 回退分组倍率，不得按 0 或报错处理。
- `group_multiplier` 模式下 helper 结果 MUST 与升级前逐字节一致，用回归测试锁定。

### 10. 旧 probe 三阶段退役，`/v1/sub2api/billing` 端点保留

- **P1**：新同步上线（连接 CRUD + 手动同步 + 日志，闭环）。旧 runner 默认关闭、可手动开启；双生产者互斥必须明示：同一账号不得同时启用 `extra.upstream_billing_probe_enabled` 与被任一启用连接的作用域覆盖，开启其一 MUST 提示/禁止另一个。
- **P2**：定时 runner + 写回护栏 + 分组 `billing_mode` 上线；前端探测按钮/设置切换到新端点。
- **P3**：删除旧 runner/handler/repo 方法与 `extra.upstream_billing_probe_enabled` 死字段（读取处保留对历史 extra key 的兼容忽略）。
- `/v1/sub2api/billing` 服务端点 MUST 保留：上游生态（其他下级实例的旧 probe）依赖该端点，删除会破坏跨实例兼容。

### 11. 管理 API 设计

路由前缀 `/admin/upstream-rate-sync`，复用现有管理员鉴权与管理操作审计：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/connections` | 连接列表（含 last_status/last_sync_at，不返回任何密文） |
| POST | `/connections` | 创建连接（base_url 归一化唯一校验） |
| PUT | `/connections/:id` | 更新（token 字段三态：非空替换/空且 clear=false 保留/clear=true 清除，仿 prompt audit 密文语义） |
| DELETE | `/connections/:id` | 删除（级联删除 runs） |
| POST | `/connections/:id/test` | 连接测试：登录（或校验 token）+ 拉一页 keys，返回发现数与预计匹配数；Turnstile/凭据错误返回明确错误码 |
| POST | `/connections/:id/sync` | 立即同步（不受 interval 限制，受 leader lock 与单连接互斥约束） |
| GET | `/runs` | 日志查询：按 connection_id/status/时间范围筛选，分页 |
| GET | `/runs/:id` | run 详情含 details 明细数组 |

响应 DTO 只暴露 `has_credentials`/`has_token` 布尔与状态字段，任何序列化路径不得输出密文或明文 token。

### 12. 前端页面设计

- 新菜单"上游倍率同步"，路由 `/admin/upstream-rate-sync`；新建自包含目录 `frontend/src/features/upstream-rate-sync/`（`View.vue`、`api.ts`、`types.ts`、`components/`）。
- 页面三区块：
  1. **连接表格**：名称、base_url、auth_mode、enabled、interval、last_sync_at、last_status、操作（编辑/测试/立即同步/删除）。
  2. **编辑弹窗**：base_url、auth_mode 切换（password 账密 / token 粘贴）、interval、enabled，内嵌"连接测试"按钮与结果展示（发现数/预计匹配数）；token 模式下提示 Turnstile/TOTP 限制与到期需重新粘贴。
  3. **同步日志表格**：按连接/状态/时间筛选，行展开显示 details 明细（key_prefix、group_name、old_rate → new_rate、action）。
- 接线：`router/index.ts`、`AppSidebar.vue`、i18n zh/en（nav key + admin 模块词条）。
- 分组表单新增"计价模式"选择器：选 `account_upstream` 时 `rate_multiplier` 输入降级标注为"兜底倍率"并附提示文案。

### 13. 敏感信息与日志边界

- 密文字段（账密、access/refresh token）只在 `upstream_connections` 表与服务内存中存在；日志、run error、details、API 响应、前端状态中只允许出现 `key_prefix`（sk- 前 8 位）级别的标识。
- `details.key_prefix` 用于管理员核对，不得记录完整 key。
- 结构化日志覆盖：连接创建/更新/删除、测试成功/失败、同步开始/完成/失败、逐动作计数、跳变跳过、手改跳过、unmatched、token 刷新成功/失败。

## Risks / Trade-offs

- [上游开启 Turnstile 则账密自动化失效（死穴）] → 连接测试显式探测并给出明确错误；文档明示降级为 auth_mode=token 手动粘贴，到期需人工更新；不为 Turnstile 实现绕过。
- [refresh 严格一次性轮转下并发刷新导致 token 丢失（自残）] → 单连接互斥 + 单 flight 串行刷新，刷新成功立即持久化最新 token；持久化失败记错误并保留旧密文一致性。
- [上游 key 轮换（删旧发新）导致本地账号全部 unmatched] → unmatched 不清值、只记录；管理员更换本地账号 api_key 后下一轮自动恢复匹配。
- [跳变阈值 >50% 误杀真实调价] → 只跳过并记 `threshold_skipped`、run 降 partial 并在前端明细可见；管理员可通过立即同步 + 明细复核后手工改值（手改受 last_synced_rate 三方比对保护）。
- [双生产者（旧 probe runner 与新同步）同时写同一账号] → P1 阶段互斥约束明示并强制：同一账号不可同时启用两种生产者；P3 删除旧 runner 后唯一生产者收敛为新同步。
- [resolved vs group 口径选择] → 已记录为产品决策：只用分组倍率，`resolved_rate_multiplier` 快照字段写为分组口径；若未来需要用户专属倍率，另起 change 扩展快照与 helper。
- [同步写回与 CRS 同步/管理员并发写账号] → 复用现有账号更新路径与 optimistic 语义；三方比对保证本服务不覆盖手改，管理员手改也不会被静默回写。
- [会话绑定（IP+UA）在出口 IP 不稳定的部署下 token 频繁失效] → 固定 UA + 文档要求稳定出口 IP；token 失效后 password 模式自动重新登录，token 模式告警提示人工更新。
- [上游接口或分页行为变化] → 连接测试与 run 失败状态给出明确错误；`/api/v1/keys` 为 sub2api 自有接口，跨版本兼容由上游生态约定约束。

## Migration Plan

### 阶段 P1：连接 CRUD + 手动同步 + 日志（闭环）

1. 迁移 192/193 建表与 groups 字段；Ent schema 同步。
2. `UpstreamRateSyncService`：JWT 登录/串行刷新/持久化、翻页拉取、匹配、写回 + 快照 + run 记录（先只支持手动触发）。
3. 管理 API 连接 CRUD/测试/立即同步/日志查询。
4. 前端页面三区块与路由/侧栏/i18n 接线。
5. 旧 probe runner 默认关闭、双生产者互斥提示。

### 阶段 P2：定时 runner + 护栏 + 分组 billing_mode

1. runLoop/ticker/leader lock 定时同步与 runs 定期清理。
2. 写回护栏全套：变化才写、跳变跳过、last_synced_rate 三方比对、unmatched 不清值。
3. `groups.billing_mode`、公共 helper 抽提、两条计费入口接入、`APIKeyAuthGroupSnapshot` 加字段 + bump 缓存版本、分组表单选择器。
4. 前端探测按钮/设置切换到新端点。

### 阶段 P3：旧探测退役

1. 删除旧 runner/handler/repo 方法与 `extra.upstream_billing_probe_enabled` 死字段。
2. 保留 `/v1/sub2api/billing` 服务端点。
3. 回归测试：调度排序、列表排序、前端 Cell、CRS 保护对新同步写入的快照行为不变。

### 回滚

- 关闭所有连接的 enabled 即停止写回；`billing_mode` 保持默认 `group_multiplier` 时计费行为与升级前一致。
- 已写回的 `rate_multiplier` 不清空（管理员可手工改回）；回滚不删除表、不回退已应用 migration。
- P3 前的任意阶段可单独停用新同步而不影响旧 probe。

## Open Questions

1. `details` 明细数组的上限：单连接账号数极大时 details 可能膨胀。暂按单 run 最多 5000 条明细截断（超出仅计数），实施时确认是否需要只记录非 unchanged 明细。
2. 连接测试的"预计匹配数"是否只统计第一页（当前设计）还是全量翻页（更准但更慢）；倾向保持一页 + 明示"预计"。
3. `threshold_skipped` 是否需要管理员确认后强制写回的 API，或仅文档指引手工改值；第一版倾向后者。
