## ADDED Requirements

### Requirement: 分组必须提供计价模式字段并使用安全默认值
系统 SHALL 为 groups 增加 `billing_mode` 字段（string 枚举），合法值为 `group_multiplier` 与 `account_upstream`，缺省与历史数据 MUST 归一为 `group_multiplier`。管理 API 保存分组时 MUST 校验枚举值，非法值 MUST 返回 400。

#### Scenario: 历史分组默认模式
- **WHEN** 迁移完成后读取既有分组
- **THEN** billing_mode MUST 为 group_multiplier
- **THEN** 计费行为 MUST 与升级前完全一致

#### Scenario: 保存非法模式
- **WHEN** 管理员保存分组时提交非枚举的 billing_mode
- **THEN** 后端 MUST 返回 400 和稳定错误码，不得写入

#### Scenario: account_upstream 模式下分组倍率的语义
- **WHEN** 分组 billing_mode=account_upstream
- **THEN** 分组 rate_multiplier MUST 仅作为账号无有效同步倍率时的兜底倍率
- **THEN** 分组管理表单 MUST 向管理员提示该降级语义

### Requirement: 两条计费入口必须使用统一的计价 helper
系统 SHALL 将 gateway_usage_billing.go 与 openai_gateway_usage.go 两条计费入口的分组倍率读取逻辑抽取为公共 helper，按固定优先级链计算：系统默认 →（account_upstream 模式读命中账号 BillingRateMultiplier()，否则分组默认倍率）→ 用户专属分组倍率始终覆盖 → 高峰倍率最后叠加。OpenAI 路径 MUST 先 resolveCredentialAccount 再读取账号倍率。

#### Scenario: group_multiplier 模式
- **WHEN** 分组 billing_mode=group_multiplier
- **THEN** helper MUST 使用分组 rate_multiplier 作为基础倍率
- **THEN** 两条计费入口的结果 MUST 一致

#### Scenario: account_upstream 模式
- **WHEN** 分组 billing_mode=account_upstream 且命中账号存在有效同步倍率
- **THEN** helper MUST 使用账号 BillingRateMultiplier() 作为基础倍率
- **THEN** nil 或负数的账号倍率 MUST 按 1.0 处理

#### Scenario: OpenAI 计费路径
- **WHEN** OpenAI 兼容请求进入计费
- **THEN** 系统 MUST 先 resolveCredentialAccount 确定命中账号
- **THEN** helper MUST 读取该账号的倍率而不是按分组统一取值

### Requirement: 账号无有效同步倍率时必须回退分组倍率
系统 SHALL 在 account_upstream 模式下，对从未同步成功、同步值缺失或同步账号未匹配的情况，回退使用分组 rate_multiplier 作为基础倍率，不得阻断请求或按 0 计费。

#### Scenario: 账号从未同步
- **WHEN** 分组为 account_upstream 且账号无同步写入的倍率
- **THEN** 计费 MUST 使用分组 rate_multiplier 兜底

#### Scenario: 账号在上游 unmatched
- **WHEN** 账号在最近同步中被记为 unmatched 而无有效同步值
- **THEN** 计费 MUST 回退分组 rate_multiplier

#### Scenario: 账号倍率非法
- **WHEN** 账号 rate_multiplier 为 nil 或负数
- **THEN** BillingRateMultiplier() MUST 归一为 1.0 后再进入优先级链

### Requirement: 用户专属分组倍率必须始终覆盖模式基础倍率
系统 SHALL 保持用户专属分组倍率在优先级链中的最高覆盖优先级：无论分组使用哪种 billing_mode，只要存在用户专属分组倍率，MUST 以该值替代模式基础倍率。

#### Scenario: account_upstream 模式下存在用户专属倍率
- **WHEN** 分组为 account_upstream、账号有同步倍率且用户对该分组配置了专属倍率
- **THEN** 计费 MUST 使用用户专属倍率
- **THEN** 账号同步倍率 MUST NOT 参与本次计费

#### Scenario: group_multiplier 模式下存在用户专属倍率
- **WHEN** 分组为 group_multiplier 且用户配置了专属倍率
- **THEN** 计费 MUST 使用用户专属倍率而非分组默认倍率

### Requirement: 高峰倍率必须在优先级链最后叠加
系统 SHALL 在确定基础倍率（含用户专属覆盖）之后、最终计费之前叠加高峰倍率。高峰配置 MUST 继续来自现有分组 peak 配置来源，两种 billing_mode 下叠加语义 MUST 一致。

#### Scenario: 高峰时段计费
- **WHEN** 请求命中分组高峰时段配置
- **THEN** 系统 MUST 在基础倍率或用户专属倍率之上最后乘以高峰倍率

#### Scenario: 非高峰时段计费
- **WHEN** 请求未命中高峰时段
- **THEN** 最终倍率 MUST 等于优先级链确定的基础倍率，不得额外叠加

### Requirement: 模式切换必须通过缓存版本 bump 即时生效
系统 SHALL 在认证缓存快照 APIKeyAuthGroupSnapshot 中加入 billing_mode 字段，并在该结构变化时 bump 缓存版本。保存分组 billing_mode 后，后续请求 MUST 使用新模式计费，不得继续消费旧快照中的模式。

#### Scenario: 切换模式后的首个请求
- **WHEN** 管理员把分组从 group_multiplier 切换为 account_upstream 并保存
- **THEN** 缓存 MUST 失效或按新版本重建
- **THEN** 后续请求 MUST 立即按 account_upstream 优先级链计费

#### Scenario: 旧版本缓存快照
- **WHEN** 实例持有 bump 前生成的旧版本 APIKeyAuthGroupSnapshot
- **THEN** 系统 MUST 视其为无效并重新加载
- **THEN** 不得用缺失 billing_mode 的旧快照计算倍率

#### Scenario: 多实例生效
- **WHEN** 多实例部署下保存新模式
- **THEN** 其他实例 MUST 通过现有缓存失效机制最终获得含 billing_mode 的新快照

### Requirement: usage_logs 必须记录可复核的计费快照
系统 SHALL 在 usage_logs 中保留足以复核本次计费来源的快照信息，至少包括最终应用的倍率及其来源（分组默认、账号同步、用户专属、高峰叠加），使管理员事后可以区分计费来自哪种 billing_mode 与哪一级优先级。

#### Scenario: 复核 account_upstream 计费
- **WHEN** 查询一条 account_upstream 模式下产生的 usage_log
- **THEN** 记录 MUST 能还原基础倍率来自命中账号的同步倍率
- **THEN** 记录 MUST 能还原高峰是否叠加及叠加后的最终倍率

#### Scenario: 复核用户专属覆盖
- **WHEN** 查询一条被用户专属倍率覆盖的 usage_log
- **THEN** 记录 MUST 能还原最终倍率来自用户专属配置而非分组或账号
