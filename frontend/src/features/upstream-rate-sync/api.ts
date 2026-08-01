import { apiClient } from '@/api/client'
import type {
  UpstreamConnection,
  UpstreamConnectionPage,
  UpstreamConnectionSubmit,
  UpstreamConnectionTestResult,
  UpstreamRunFilters,
  UpstreamSyncRun,
  UpstreamSyncRunPage,
} from './types'

const basePath = '/admin/upstream-rate-sync'

/**
 * 凭证提交只携带非空字段：留空 = 保持不变（后端三态语义）。
 * password 模式提交 email + password；token 模式提交 token。
 */
function credentialsPayload(payload: UpstreamConnectionSubmit): Record<string, string> {
  const out: Record<string, string> = {}
  if (payload.auth_mode === 'password') {
    if (payload.email) out.email = payload.email
    if (payload.password) out.password = payload.password
  } else if (payload.token) {
    out.token = payload.token
  }
  return out
}

function submitBody(payload: UpstreamConnectionSubmit): Record<string, unknown> {
  return {
    name: payload.name,
    base_url: payload.base_url,
    auth_mode: payload.auth_mode,
    enabled: payload.enabled,
    interval_minutes: payload.interval_minutes,
    ...credentialsPayload(payload),
  }
}

export async function listConnections(): Promise<UpstreamConnection[]> {
  const { data } = await apiClient.get<UpstreamConnectionPage>(`${basePath}/connections`)
  return data.items ?? []
}

export async function createConnection(
  payload: UpstreamConnectionSubmit,
): Promise<UpstreamConnection> {
  const { data } = await apiClient.post<UpstreamConnection>(
    `${basePath}/connections`,
    submitBody(payload),
  )
  return data
}

export async function updateConnection(
  id: number,
  payload: UpstreamConnectionSubmit,
): Promise<UpstreamConnection> {
  const { data } = await apiClient.put<UpstreamConnection>(
    `${basePath}/connections/${id}`,
    submitBody(payload),
  )
  return data
}

export async function deleteConnection(id: number): Promise<void> {
  await apiClient.delete(`${basePath}/connections/${id}`)
}

export async function testConnection(id: number): Promise<UpstreamConnectionTestResult> {
  const { data } = await apiClient.post<UpstreamConnectionTestResult>(
    `${basePath}/connections/${id}/test`,
  )
  return data
}

export async function syncConnection(id: number): Promise<UpstreamSyncRun> {
  const { data } = await apiClient.post<UpstreamSyncRun>(`${basePath}/connections/${id}/sync`)
  return data
}

export async function listRuns(
  filters: UpstreamRunFilters,
  page: number,
  pageSize: number,
): Promise<UpstreamSyncRunPage> {
  const params: Record<string, unknown> = { page, page_size: pageSize }
  if (filters.connection_id) params.connection_id = filters.connection_id
  if (filters.status) params.status = filters.status
  const { data } = await apiClient.get<UpstreamSyncRunPage>(`${basePath}/runs`, { params })
  return { ...data, items: data.items ?? [] }
}

export const upstreamRateSyncAPI = {
  listConnections,
  createConnection,
  updateConnection,
  deleteConnection,
  testConnection,
  syncConnection,
  listRuns,
}

export default upstreamRateSyncAPI
