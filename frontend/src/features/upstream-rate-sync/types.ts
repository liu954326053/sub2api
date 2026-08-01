export type UpstreamAuthMode = 'password' | 'token'
export type UpstreamSyncStatus = 'success' | 'partial' | 'failed'
export type UpstreamSyncAction =
  | 'updated'
  | 'unchanged'
  | 'unmatched'
  | 'threshold_skipped'
  | 'manual_override'

/**
 * 连接对象的 API 形态。响应只做脱敏：has_credentials 布尔 + 状态字段，
 * 任何路径都不会返回密文或明文 token。
 */
export interface UpstreamConnection {
  id: number
  name: string
  base_url: string
  auth_mode: UpstreamAuthMode
  enabled: boolean
  interval_minutes: number
  last_sync_at?: string | null
  last_status?: UpstreamSyncStatus | null
  last_error?: string | null
  last_balance?: number | null
  has_credentials: boolean
  token_expires_at?: string | null
  created_at: string
}

/**
 * 创建/更新连接的提交体。凭证字段三态：非空替换 / 空则保持不变。
 * password 模式提交 email + password；token 模式提交 token。
 */
export interface UpstreamConnectionSubmit {
  name: string
  base_url: string
  auth_mode: UpstreamAuthMode
  email?: string
  password?: string
  token?: string
  enabled: boolean
  interval_minutes: number
}

export interface UpstreamConnectionTestResult {
  keys_found: number
  accounts_matched: number
  balance?: number | null
}

export interface UpstreamSyncRunDetail {
  account_id: number
  key_prefix: string
  group_name: string
  old_rate: number | null
  new_rate: number | null
  action: UpstreamSyncAction
}

export interface UpstreamSyncRun {
  id: number
  connection_id: number
  connection_name?: string
  started_at: string
  finished_at?: string | null
  status: UpstreamSyncStatus
  keys_fetched: number
  accounts_matched: number
  accounts_updated: number
  accounts_unchanged: number
  accounts_unmatched: number
  details?: UpstreamSyncRunDetail[]
  error?: string | null
}

export interface UpstreamSyncRunPage {
  items: UpstreamSyncRun[]
  total: number
  page: number
  page_size: number
  pages: number
}

/** 连接列表的分页响应（后端统一分页信封）。 */
export interface UpstreamConnectionPage {
  items: UpstreamConnection[]
  total: number
  page: number
  page_size: number
  pages: number
}

/** 日志筛选条件；空字符串表示不限制（全部）。 */
export interface UpstreamRunFilters {
  connection_id: string
  status: string
}

/** 编辑弹窗的本地表单状态；凭证字段留空表示保持不变。 */
export interface UpstreamConnectionForm {
  id: number | null
  name: string
  base_url: string
  auth_mode: UpstreamAuthMode
  email: string
  password: string
  token: string
  enabled: boolean
  interval_minutes: number
  has_credentials: boolean
}
