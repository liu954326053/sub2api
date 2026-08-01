## ADDED Requirements

### Requirement: 上游连接必须通过管理 API 进行 CRUD
系统 SHALL 提供 `/admin/upstream-rate-sync/*` 管理 API，复用现有管理员鉴权与管理操作审计，对 `upstream_connections` 提供创建、读取、更新、删除能力。`base_url` MUST 在保存前归一化（去尾部斜杠、统一 scheme/host 大小写），归一化结果 MUST 全局唯一；`interval_minutes` MUST 默认为 30 且大于 0；`enabled` MUST 默认为 false。

#### Scenario: 创建连接
- **WHEN** 管理员提交 name、base_url、auth_mode 与对应凭证
- **THEN** 系统 MUST 归一化 base_url 并写入 `upstream_connections`
- **THEN** enabled MUST 默认为 false，interval_minutes MUST 默认为 30

#### Scenario: base_url 归一化后重复
- **WHEN** 新连接的 base_url 归一化后与既有连接相同
- **THEN** 后端 MUST 返回 409 和稳定错误码，不得创建重复连接

#### Scenario: 非法配置被拒绝
- **WHEN** auth_mode 不是 `password` 或 `token`，或 interval_minutes 小于等于 0
- **THEN** 后端 MUST 返回 400 和稳定错误码

#### Scenario: 删除连接
- **WHEN** 管理员删除一个连接
- **THEN** 系统 MUST 删除该连接及其加密凭证
- **THEN** 该连接的历史 `upstream_sync_runs` 日志 MUST 保留至定期清理

### Requirement: 连接凭证必须加密存储且永不回显明文
系统 SHALL 使用现有 SecretEncryptor（AES-256-GCM）加密保存 `credentials_encrypted`、`access_token_encrypted`、`refresh_token_encrypted`。任何管理 API 响应、日志、错误消息和前端状态 MUST 不包含明文或密文凭证，只允许返回 has_credentials/token_expires_at 等派生状态。

#### Scenario: 保存账密凭证
- **WHEN** 管理员以 auth_mode=password 保存用户名和密码
- **THEN** 系统 MUST 用 SecretEncryptor 加密后写入 `credentials_encrypted`
- **THEN** 数据库 MUST 不出现明文密码列

#### Scenario: 读取连接详情
- **WHEN** 管理员查询连接详情或列表
- **THEN** 响应 MUST 不包含明文或密文凭证
- **THEN** 响应 MUST 包含 has_credentials 与 token_expires_at

#### Scenario: 更新时保留原凭证
- **WHEN** 管理员更新连接但未提交新凭证
- **THEN** 系统 MUST 保留原密文不变
- **WHEN** 管理员显式清除凭证
- **THEN** 系统 MUST 清空对应密文字段

### Requirement: password 模式必须自动完成 JWT 登录、刷新与失效重登
系统 SHALL 对 auth_mode=password 的连接调用上游 `POST /api/v1/auth/login` 获取 access token（24h）与 refresh token（30d），加密持久化最新 token。refresh token 严格一次性轮转，刷新 MUST 串行执行并在成功后立即持久化新 token 对；refresh 失效时 MUST 自动用账密重新登录。同步服务 MUST 使用固定 User-Agent 并从稳定出口 IP 发起，以满足上游会话绑定（IP+UA 指纹）校验。

#### Scenario: 首次登录成功
- **WHEN** 连接以有效账密执行同步且本地无可用 token
- **THEN** 系统 MUST 调用上游 `/api/v1/auth/login` 并加密保存 access/refresh token 与 token_expires_at

#### Scenario: access token 过期后串行刷新
- **WHEN** access token 过期或临近过期
- **THEN** 系统 MUST 用 refresh token 串行刷新并立即持久化轮转后的新 token 对
- **THEN** 同一连接的并发同步 MUST 共享同一次刷新结果，不得并行消费一次性 refresh token

#### Scenario: refresh token 失效后重新登录
- **WHEN** 上游拒绝 refresh token
- **THEN** 系统 MUST 用已保存账密重新登录
- **THEN** 重新登录也失败时本次同步 MUST 记为 failed，不得继续使用旧 token

#### Scenario: token 模式跳过登录自动化
- **WHEN** 连接 auth_mode=token
- **THEN** 系统 MUST 直接使用管理员粘贴的 access token
- **THEN** token 失效时本次同步 MUST 记为 failed 并在 last_error 中提示需要手动更新，不得尝试账密登录

### Requirement: 上游防护限制必须显式降级而非静默失败
系统 SHALL 明确处理上游开启 Turnstile 或同步账号开启 TOTP 的场景。上游开启 Turnstile 时账密自动化 MUST 判定为不可用，文档与错误提示 MUST 引导管理员改用 auth_mode=token 手动粘贴；同步所用上游账号 MUST 不开启 TOTP。

#### Scenario: 上游开启 Turnstile
- **WHEN** 上游登录接口要求 Turnstile 校验
- **THEN** 密码登录 MUST 失败并返回可识别的错误状态
- **THEN** last_error MUST 提示降级为手动粘贴 token，不得反复重试密码登录

#### Scenario: 上游账号开启 TOTP
- **WHEN** 上游登录接口返回需要二次验证
- **THEN** 系统 MUST 将该连接标记为认证失败
- **THEN** last_error MUST 提示同步账号须关闭 TOTP 或改用 token 模式

### Requirement: 同步必须分页拉取上游 keys 并按 api_key 精确匹配本地账号
系统 SHALL 使用连接的有效 token 调用上游 `GET /api/v1/keys`（用户 JWT 鉴权），分页拉取该用户全部 key，读取每把 key 内嵌 group 对象的 rate_multiplier、platform 与 peak 配置。本地账号 MUST 以其 credentials 中的 api_key（即上游 sk- key）与上游 key 做字符串精确匹配，不做名称映射。本地有而上游无的账号 MUST 记为 unmatched 且 MUST NOT 清除或修改其现有倍率。

#### Scenario: 分页拉取全部 keys
- **WHEN** 上游 keys 超过单页大小
- **THEN** 系统 MUST 逐页拉取直至取完
- **THEN** 任一页失败 MUST 中止本次同步并记为 failed，不得用部分数据写回

#### Scenario: key 精确匹配到本地账号
- **WHEN** 上游某 key 的 key 字符串与本地账号 credentials.api_key 完全一致
- **THEN** 系统 MUST 取该 key 内嵌 group.rate_multiplier 作为该账号的候选倍率
- **THEN** accounts_matched MUST 计数加一

#### Scenario: 本地账号在上游不存在
- **WHEN** 本地账号的 api_key 未出现在上游返回中
- **THEN** 该账号 MUST 记为 unmatched
- **THEN** 其 rate_multiplier 与 extra 快照 MUST 保持原值

### Requirement: 倍率写回必须有值变化判断与跳变护栏
系统 SHALL 只在候选倍率与账号当前 `rate_multiplier` 不一致时执行写回，并沿用现有语义：nil 与负数按 1.0 处理。单次同步中候选值相对当前值跳变超过 50% 时 MUST 跳过该账号并以 action=threshold_skipped 记录，不得写回。写回 MUST 同时更新 `extra.upstream_billing_probe` 同构快照（status/data{group_rate_multiplier, billing_scope:"token", peak_rate_enabled 等 peak 字段}/received_at/fresh_until/next_probe_at），使调度排序、调度缓存、列表排序 SQL、前端 UpstreamBillingRateCell 与 CRS 同步保护五个现有消费方零改动复用。

#### Scenario: 倍率无变化
- **WHEN** 候选倍率与账号当前 rate_multiplier 相等
- **THEN** 系统 MUST NOT 执行写库
- **THEN** 明细 action MUST 为 unchanged，accounts_unchanged 计数加一

#### Scenario: 倍率正常更新
- **WHEN** 候选倍率与当前值不同且跳变不超过 50%
- **THEN** 系统 MUST 写回 accounts.rate_multiplier 并刷新 extra.upstream_billing_probe 快照
- **THEN** 明细 MUST 记录 account_id、key_prefix、group_name、old_rate、new_rate 与 action=updated

#### Scenario: 跳变超过 50% 被跳过
- **WHEN** 候选倍率相对当前值跳变超过 50%
- **THEN** 系统 MUST NOT 写回该账号
- **THEN** 明细 action MUST 为 threshold_skipped 并包含 old_rate 与 new_rate

#### Scenario: 非法候选值归一
- **WHEN** 上游返回的 rate_multiplier 为 null 或负数
- **THEN** 系统 MUST 按 1.0 计算候选值后再进入变化判断与跳变护栏

### Requirement: 同步必须保护管理员手动修改不被覆盖
系统 SHALL 在账号 extra 中记录 `last_synced_rate`。写回前 MUST 做三方比对：当前 rate_multiplier 与 last_synced_rate 不一致时视为管理员已手动修改，本次 MUST 跳过并以 action=manual_override 记录，不得覆盖手改值。

#### Scenario: 管理员手改后跳过同步
- **WHEN** 账号当前 rate_multiplier 与 extra.last_synced_rate 不一致
- **THEN** 系统 MUST NOT 写回该账号
- **THEN** 明细 action MUST 为 manual_override

#### Scenario: 未被手改的账号正常同步
- **WHEN** 当前 rate_multiplier 与 last_synced_rate 一致且候选值不同
- **THEN** 系统 MUST 写回新值并同步更新 last_synced_rate

#### Scenario: 首次同步无 last_synced_rate
- **WHEN** 账号 extra 中不存在 last_synced_rate
- **THEN** 系统 MUST 视为未被手改并正常执行变化判断与跳变护栏
- **THEN** 写回成功后 MUST 写入 last_synced_rate

### Requirement: 每次同步必须记录结构化日志并定期清理
系统 SHALL 为每次同步写入 `upstream_sync_runs`：connection_id、started_at、finished_at、status（success|partial|failed）、keys_fetched、accounts_matched、accounts_updated、accounts_unchanged、accounts_unmatched、details JSONB 明细数组与 error。系统 MUST 提供按连接、状态、时间筛选的日志查询 API，并按配置定期清理过期日志。

#### Scenario: 全部成功
- **WHEN** 本次同步所有匹配账号均写回、unchanged 或合法跳过
- **THEN** run 的 status MUST 为 success，五个计数字段 MUST 与实际明细一致

#### Scenario: 部分账号失败
- **WHEN** 个别账号写回失败但同步整体完成
- **THEN** run 的 status MUST 为 partial
- **THEN** 失败账号 MUST 出现在 details 中且 error 字段 MUST 汇总失败原因

#### Scenario: 日志筛选查询
- **WHEN** 管理员按连接、状态和时间范围查询同步日志
- **THEN** 系统 MUST 返回分页结果
- **THEN** 单条日志 MUST 可展开 details 明细数组

#### Scenario: 过期日志清理
- **WHEN** 定期清理任务执行
- **THEN** 系统 MUST 删除超过保留期的 upstream_sync_runs
- **THEN** 清理 MUST 不影响连接本身与最近一条日志

### Requirement: 多连接必须按 base_url 作用域隔离
系统 SHALL 支持配置多个上游连接。每个连接 MUST 只更新其返回 keys 精确匹配到的账号；同一本地账号被多个连接匹配时 MUST 按各连接独立执行并各自记录 run，后执行的连接受 last_synced_rate 三方比对约束，不得互相污染匹配作用域。

#### Scenario: 两个连接覆盖不同账号
- **WHEN** 连接 A 与连接 B 的上游 keys 匹配到不同的本地账号集合
- **THEN** 每个连接 MUST 只写回自己匹配到的账号
- **THEN** 两个连接 MUST 各自产生独立的 upstream_sync_runs

#### Scenario: 两个连接匹配同一账号
- **WHEN** 同一账号先后被连接 A 与连接 B 同步且候选值不同
- **THEN** 后执行的连接 MUST 因 last_synced_rate 三方比对判定为 manual_override 或按最新值正常写回
- **THEN** 两条 run 的明细 MUST 各自完整可追溯

### Requirement: 定时同步必须单实例执行且失败安全降级
系统 SHALL 仿 UpstreamBillingProbeService 提供 Start/Stop/runLoop/ticker 后台任务，按每个启用连接的 interval_minutes 调度，并使用 LeaderLockCache Redis 锁、Redis 不可用时回退 PostgreSQL advisory lock，保证多实例部署下同一连接同一时刻只有一个同步在执行。任何同步失败 MUST 只影响本次 run，不得修改未确认的数据，不得阻断主网关请求路径。

#### Scenario: 多实例定时调度
- **WHEN** 多个实例同时到达某连接的调度时刻
- **THEN** 只有一个实例 MUST 获得锁并执行同步
- **THEN** 未获锁实例 MUST 跳过本轮且不产生 run

#### Scenario: Redis 不可用
- **WHEN** Redis LeaderLockCache 不可用
- **THEN** 系统 MUST 回退 PostgreSQL advisory lock 保证单实例执行
- **THEN** 锁也不可获得时本轮 MUST 跳过并记录降级日志

#### Scenario: 上游整体不可达
- **WHEN** 上游连接失败或登录/刷新均失败
- **THEN** 本次 run MUST 记为 failed
- **THEN** 所有账号的 rate_multiplier 与 extra 快照 MUST 保持原值
- **THEN** 连接的 last_status/last_error MUST 更新，主网关请求路径 MUST 不受影响

#### Scenario: 停用连接
- **WHEN** 连接 enabled 被保存为 false
- **THEN** 调度器 MUST 不再为该连接发起新同步
- **THEN** 进行中的本轮同步 MAY 完成后退出，不得启动下一轮
