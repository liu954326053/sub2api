## 1. 数据基础（ent schema、迁移、Repository）

- [x] 1.1 新增 `backend/ent/schema/upstream_connection.go`：id/name/base_url（归一化后唯一）/auth_mode（枚举 `password|token`）/credentials_encrypted/access_token_encrypted/refresh_token_encrypted/token_expires_at/enabled/interval_minutes（默认 30）/last_sync_at/last_status/last_error，凭证字段只允许密文入库
- [x] 1.2 新增 `backend/ent/schema/upstream_sync_run.go`：id/connection_id/started_at/finished_at/status（枚举 `success|partial|failed`）/keys_fetched/accounts_matched/accounts_updated/accounts_unchanged/accounts_unmatched/details（JSONB 明细数组 `{account_id,key_prefix,group_name,old_rate,new_rate,action: updated|unchanged|unmatched|threshold_skipped|manual_override}`）/error
- [x] 1.3 修改 `backend/ent/schema/group.go` 新增 `billing_mode` 字段（string 枚举，默认 `group_multiplier`，可选 `account_upstream`）
- [x] 1.4 新增迁移 `backend/migrations/192_groups_billing_mode.sql`（groups 加列+默认值+枚举约束）与 `193_upstream_rate_sync_tables.sql`（两张新表+索引+非负计数约束；若与实施时最大序号冲突则顺延并同步更新本文档）
- [x] 1.5 运行 `cd backend && go generate ./ent`，确认生成代码入库且不产生无关 diff
- [x] 1.6 实现 `backend/internal/repository/upstream_connection_repo.go`：CRUD、按 enabled 列出到期连接、last_sync_at/last_status/last_error 更新
- [x] 1.7 实现 `backend/internal/repository/upstream_sync_run_repo.go`：创建/结束 run、按连接/状态/时间范围分页查询、超保留期的定期批量清理
- [x] 1.8 添加迁移幂等与回滚测试、两表 Repository 集成测试（含 details JSONB 往返、唯一约束冲突、清理边界）

## 2. 上游客户端与同步引擎

- [x] 2.1 新增 `backend/internal/service/upstreamratesync/client.go`：封装 `POST /api/v1/auth/login`（账密，不依赖邮箱验证）、`GET /api/v1/keys` 翻页拉取、固定 User-Agent；HTTP client 带超时与响应大小上限
- [x] 2.2 实现 token 生命周期：access 24h/refresh 30d，refresh 严格一次性轮转，单连接内串行刷新并立即持久化最新 token（写回 `upstream_connections` 加密列与 token_expires_at）；上游启用 Turnstile 时识别失败并返回可区分错误码（降级为手动粘贴 token 流程）
- [x] 2.3 凭证存储全部走现有 SecretEncryptor（AES-256-GCM）：password 模式的账密、token 模式的手贴 token、刷新后的 access/refresh token，任何日志/错误/详情不落明文
- [x] 2.4 实现同步引擎 `syncer.go`：拉取上游 keys → 以 key 内嵌 group 的 rate_multiplier/peak 配置为准 → 按 api_key 字符串精确匹配本地 accounts（credentials JSONB 中的 api_key，参照 `service/account.go:1274/1368` 的读写方式）
- [x] 2.5 写回 `accounts.rate_multiplier`（沿用现有语义：nil→1.0、负数→1.0，`Account.BillingRateMultiplier()`），并同步写 `extra.upstream_billing_probe` 同构快照（status/data{group_rate_multiplier, billing_scope:"token", peak 字段}/received_at/fresh_until/next_probe_at），保证 `openai_account_scheduler.go:2700-2721`、`scheduler_cache.go:1015-1038`、`account_repo.go:1036-1068`、`UpstreamBillingRateCell.vue`、`crs_sync_service.go:1157-1185` 五个消费方零改动复用
- [x] 2.6 实现写回护栏：仅在值变化时写库；单次跳变 >50% 跳过并记录 `threshold_skipped`；以 `extra.last_synced_rate` 做三方比对，管理员手改过的账号记录 `manual_override` 不覆盖；本地有上游无 → `unmatched` 且不清值；多连接按 base_url 作用域隔离
- [x] 2.7 每次同步生成一条 upstream_sync_run，填齐五个计数字段与 details 明细数组，连接级 last_status/last_error 同步更新
- [x] 2.8 添加单测：login/refresh/keys 翻页（httptest mock）、refresh 串行轮转、匹配写回、阈值跳过、manual_override 保护、unmatched 不清值、base_url 隔离、快照字段与旧探测结构一致性

## 3. 后台 Runner 与管理 API

- [x] 3.1 实现 `UpstreamRateSyncService`（`backend/internal/service/upstreamratesync/service.go`）：Start/Stop/runLoop/ticker，仿 `upstream_billing_probe.go` 的生命周期与错误处理风格
- [x] 3.2 复用 LeaderLockCache：Redis 分布式锁优先、Redis 不可用时回退 PG advisory lock，保证多实例只有一个 runner 执行
- [x] 3.3 wire 注册仿 `service/wire.go:729` 的模式，并在 `cmd/server/wire.go` 的 cleanup 中挂 Stop；runner 启动失败不得阻塞主 API
- [x] 3.4 新增 `backend/internal/handler/admin/upstream_rate_sync_handler.go`，路由前缀 `/admin/upstream-rate-sync`：连接 CRUD（创建/更新时 base_url 归一化与唯一性校验、enabled/interval_minutes 校验）
- [x] 3.5 实现连接测试端点：执行登录+拉一页 keys，返回发现数与预计匹配数，不触发写回
- [x] 3.6 实现立即同步端点：单连接触发一次完整同步，返回 run 结果摘要
- [x] 3.7 实现日志查询端点：upstream_sync_runs 按连接/状态/时间筛选分页，含 details 明细
- [x] 3.8 响应 DTO 只返回脱敏字段（has_credentials/token 状态/过期时间），任何路径不输出密文或明文 token
- [x] 3.9 添加 handler 测试：CRUD 边界、连接测试成功/失败映射、立即同步、日志筛选分页、脱敏断言

## 4. 前端页面

- [x] 4.1 创建自包含目录 `frontend/src/features/upstream-rate-sync/`：`UpstreamRateSyncView.vue`、`api.ts`、`types.ts`、`components/`
- [x] 4.2 实现连接表格组件：名称/base_url/auth_mode/enabled/interval/last_sync_at/last_status，行内启停、编辑、连接测试、立即同步入口
- [x] 4.3 实现连接编辑弹窗组件：password/token 两种 auth_mode 表单切换、内嵌连接测试按钮与结果展示（发现数/预计匹配数）、Turnstile 失败时的手动粘贴 token 提示
- [x] 4.4 实现同步日志表格组件：按连接/状态/时间筛选，行展开渲染 details 明细（account_id/key_prefix/group_name/old_rate→new_rate/action）
- [x] 4.5 接线 `frontend/src/router/index.ts`：新增 `/admin/upstream-rate-sync` 路由（管理员守卫）
- [x] 4.6 接线 `AppSidebar.vue`：新增"上游倍率同步"菜单项
- [x] 4.7 接线 i18n：zh/en 的 nav key 与 admin 模块全部文案
- [x] 4.8 添加 vitest：api.ts 请求构造、连接表格渲染与操作回调、编辑弹窗两种 auth_mode 切换与连接测试结果展示、日志明细展开

## 5. 分组计价模式（billing_mode）

- [x] 5.1 从 `gateway_usage_billing.go:677-688` 与 `openai_gateway_usage.go:154-166` 抽取公共计费倍率 helper，优先级链：系统默认 →（`account_upstream` 模式读命中账号 `BillingRateMultiplier()`，无同步值回退分组倍率 / 否则分组默认）→ 用户专属分组倍率始终覆盖 → ×高峰最后叠加
- [x] 5.2 OpenAI 路径在 helper 前先 `resolveCredentialAccount`，确保读到的是命中账号而非候选账号
- [x] 5.3 认证缓存快照 `APIKeyAuthGroupSnapshot` 增加 `billing_mode` 字段并 bump 缓存版本，防止旧快照缺字段被当作 `account_upstream`
- [x] 5.4 groups DTO/mapper 增加 billing_mode：创建/更新请求校验枚举值，列表/详情返回
- [x] 5.5 分组表单（GroupsView 相关组件）新增"计价模式"选择器；选 `account_upstream` 时 rate_multiplier 标注为兜底倍率并给出提示文案（zh/en）
- [x] 5.6 添加计费测试：两条入口共用 helper 的行为一致、account_upstream 命中/回退分组、用户专属倍率覆盖、高峰叠加顺序、旧快照兼容
- [x] 5.7 运行并保存基线：`cd backend && go test ./internal/service -run Billing -count=1` 及相关 gateway 计费测试全绿

## 6. 旧探测退役（upstream_billing_probe）

- [x] 6.1 P1：旧 runner 默认关闭、保留手动开启开关；在设置界面与文档明示双生产者互斥（新旧同时写 `extra.upstream_billing_probe` 时以新同步为准，禁止同时启用）
- [ ] 6.2 P2：前端账号列表的探测按钮/设置切换到新同步端点，删除指向旧 probe 端点的调用
- [ ] 6.3 P3：删除旧 runner/handler/repo 方法与 `extra.upstream_billing_probe_enabled` 死字段，清理对应测试与 i18n key
- [ ] 6.4 保留 `/v1/sub2api/billing` 服务端点（上游生态依赖），添加回归测试防止误删
- [ ] 6.5 添加互斥测试与删除后的编译/路由守卫测试：旧端点 404、新端点可用、快照消费方不受影响

## 7. 验证与灰度

- [ ] 7.1 端到端集成测试：mock 上游 Sub2API（login/refresh/keys），跑通连接创建 → 连接测试 → 立即同步 → run 日志 → 计费读取新倍率全链路
- [ ] 7.2 多实例集成测试：leader lock 下仅一个实例执行同步，另一个实例跳过且不产生重复 run
- [ ] 7.3 手动验证清单：真实上游实例的 password/token 两种模式、refresh 过期轮转、阈值跳过、管理员手改保护、unmatched、Turnstile 降级提示、分组 billing_mode 切换后的实际计费金额
- [ ] 7.4 灰度策略：P1 连接 CRUD+手动同步+日志先闭环上线，P2 定时 runner+护栏+billing_mode，P3 旧探测退役；每期独立可回滚
- [ ] 7.5 文档更新：部署/运维文档补充环境要求（稳定出口 IP、同步账号不得开 TOTP、上游开 Turnstile 时降级为手动 token）、迁移 192/193 说明、旧探测退役时间表
- [x] 7.6 全量回归：`cd backend && go build ./... && go test ./internal/... -count=1`、`cd frontend && pnpm vitest run && pnpm build`
