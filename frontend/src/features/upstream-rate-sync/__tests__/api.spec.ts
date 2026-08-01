import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { UpstreamConnectionSubmit } from '../types'

const client = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), post: vi.fn(), delete: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: client }))

import upstreamRateSyncAPI from '../api'

const baseSubmit: UpstreamConnectionSubmit = {
  name: 'Main upstream',
  base_url: 'https://upstream.example.com',
  auth_mode: 'password',
  email: 'ops@example.com',
  password: 'secret-canary',
  token: '',
  enabled: true,
  interval_minutes: 30,
}

describe('Upstream Rate Sync API', () => {
  beforeEach(() => Object.values(client).forEach((mock) => mock.mockReset()))

  it('unwraps the paginated connections envelope', async () => {
    client.get.mockResolvedValue({
      data: { items: [{ id: 1, name: 'Main upstream' }], total: 1, page: 1, page_size: 20, pages: 1 },
    })
    const connections = await upstreamRateSyncAPI.listConnections()
    expect(client.get).toHaveBeenCalledWith('/admin/upstream-rate-sync/connections')
    expect(connections).toHaveLength(1)
    expect(connections[0].id).toBe(1)

    client.get.mockResolvedValue({ data: { items: null, total: 0, page: 1, page_size: 20, pages: 0 } })
    await expect(upstreamRateSyncAPI.listConnections()).resolves.toEqual([])
  })

  it('uses the /admin/upstream-rate-sync namespace for connection CRUD', async () => {
    client.get.mockResolvedValue({ data: { items: [], total: 0, page: 1, page_size: 20, pages: 0 } })
    await upstreamRateSyncAPI.listConnections()
    expect(client.get).toHaveBeenCalledWith('/admin/upstream-rate-sync/connections')

    client.post.mockResolvedValue({ data: { id: 1 } })
    await upstreamRateSyncAPI.createConnection(baseSubmit)
    expect(client.post).toHaveBeenCalledWith(
      '/admin/upstream-rate-sync/connections',
      expect.objectContaining({
        name: 'Main upstream',
        base_url: 'https://upstream.example.com',
        auth_mode: 'password',
        email: 'ops@example.com',
        password: 'secret-canary',
        enabled: true,
        interval_minutes: 30,
      }),
    )

    client.put.mockResolvedValue({ data: { id: 1 } })
    await upstreamRateSyncAPI.updateConnection(7, baseSubmit)
    expect(client.put).toHaveBeenCalledWith(
      '/admin/upstream-rate-sync/connections/7',
      expect.objectContaining({ name: 'Main upstream' }),
    )

    client.delete.mockResolvedValue({ data: null })
    await upstreamRateSyncAPI.deleteConnection(7)
    expect(client.delete).toHaveBeenCalledWith('/admin/upstream-rate-sync/connections/7')
  })

  it('omits blank credentials so the backend keeps the saved secret', async () => {
    client.put.mockResolvedValue({ data: { id: 1 } })
    await upstreamRateSyncAPI.updateConnection(3, { ...baseSubmit, email: '', password: '' })
    const body = client.put.mock.calls[0][1] as Record<string, unknown>
    expect(body).not.toHaveProperty('email')
    expect(body).not.toHaveProperty('password')
    expect(body).not.toHaveProperty('token')
  })

  it('sends only the token field in token mode', async () => {
    client.post.mockResolvedValue({ data: { id: 2 } })
    await upstreamRateSyncAPI.createConnection({
      ...baseSubmit,
      auth_mode: 'token',
      email: 'ignored@example.com',
      password: 'ignored',
      token: 'access-token-canary',
    })
    const body = client.post.mock.calls[0][1] as Record<string, unknown>
    expect(body).toMatchObject({ auth_mode: 'token', token: 'access-token-canary' })
    expect(body).not.toHaveProperty('email')
    expect(body).not.toHaveProperty('password')
  })

  it('posts to the test and sync endpoints for a connection', async () => {
    client.post.mockResolvedValue({ data: { keys_found: 4, accounts_matched: 2 } })
    const testResult = await upstreamRateSyncAPI.testConnection(9)
    expect(client.post).toHaveBeenCalledWith('/admin/upstream-rate-sync/connections/9/test')
    expect(testResult).toEqual({ keys_found: 4, accounts_matched: 2 })

    client.post.mockResolvedValue({ data: { id: 11, status: 'success' } })
    await upstreamRateSyncAPI.syncConnection(9)
    expect(client.post).toHaveBeenCalledWith('/admin/upstream-rate-sync/connections/9/sync')
  })

  it('builds run list params and omits empty filters', async () => {
    client.get.mockResolvedValue({ data: { items: [], total: 0, page: 1, page_size: 20, pages: 0 } })

    await upstreamRateSyncAPI.listRuns({ connection_id: '', status: '' }, 2, 50)
    expect(client.get).toHaveBeenCalledWith('/admin/upstream-rate-sync/runs', {
      params: { page: 2, page_size: 50 },
    })

    await upstreamRateSyncAPI.listRuns({ connection_id: '5', status: 'failed' }, 1, 20)
    expect(client.get).toHaveBeenCalledWith('/admin/upstream-rate-sync/runs', {
      params: { page: 1, page_size: 20, connection_id: '5', status: 'failed' },
    })
  })
})
