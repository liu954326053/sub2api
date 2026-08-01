import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import ConnectionTable from '../components/ConnectionTable.vue'
import ConnectionEditDialog from '../components/ConnectionEditDialog.vue'
import SyncRunTable from '../components/SyncRunTable.vue'
import type { UpstreamConnection, UpstreamConnectionSubmit, UpstreamSyncRun } from '../types'
import { emptyRunFilters } from '../viewModel'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      locale: { value: 'en' },
      t: (key: string, params?: Record<string, unknown>) =>
        key.replace(/\{(\w+)\}/g, (_, token) => String(params?.[token] ?? `{${token}}`)),
    }),
  }
})

const DialogStub = defineComponent({
  props: ['show', 'title'],
  emits: ['close'],
  template: '<div v-if="show" data-test="dialog"><slot /><slot name="footer" /></div>',
})
const PaginationStub = defineComponent({
  props: ['total', 'page', 'pageSize'],
  emits: ['update:page', 'update:pageSize'],
  template: '<div data-test="pagination" />',
})

const connection = (overrides: Partial<UpstreamConnection> = {}): UpstreamConnection => ({
  id: 1,
  name: 'Main upstream',
  base_url: 'https://upstream.example.com',
  auth_mode: 'password',
  enabled: true,
  interval_minutes: 30,
  last_sync_at: '2026-07-16T08:00:00Z',
  last_status: 'success',
  last_error: null,
  has_credentials: true,
  token_expires_at: '2026-07-17T08:00:00Z',
  created_at: '2026-07-01T00:00:00Z',
  ...overrides,
})

const run = (overrides: Partial<UpstreamSyncRun> = {}): UpstreamSyncRun => ({
  id: 1,
  connection_id: 1,
  started_at: '2026-07-16T08:00:00Z',
  finished_at: '2026-07-16T08:01:00Z',
  status: 'partial',
  keys_fetched: 4,
  accounts_matched: 3,
  accounts_updated: 1,
  accounts_unchanged: 1,
  accounts_unmatched: 1,
  details: [
    { account_id: 42, key_prefix: 'sk-abcd1234', group_name: 'vip', old_rate: 1.0, new_rate: 1.2, action: 'updated' },
    { account_id: 43, key_prefix: 'sk-ffff0000', group_name: 'default', old_rate: null, new_rate: null, action: 'unmatched' },
  ],
  error: null,
  ...overrides,
})

const mountDialog = (props: Record<string, unknown>) =>
  mount(ConnectionEditDialog, {
    props: {
      show: true,
      connection: null,
      saving: false,
      testing: false,
      testResult: null,
      testError: '',
      testErrorCode: '',
      ...props,
    },
    global: { stubs: { BaseDialog: DialogStub } },
  })

describe('Upstream Rate Sync components', () => {
  beforeEach(() => vi.restoreAllMocks())

  it('renders connection rows and emits row actions', async () => {
    const wrapper = mount(ConnectionTable, {
      props: { connections: [connection()], loading: false, testingIds: [], syncingIds: [] },
    })
    expect(wrapper.text()).toContain('Main upstream')
    expect(wrapper.text()).toContain('https://upstream.example.com')
    expect(wrapper.get('[data-test="last-status-1"]').text()).toBe('admin.upstreamRateSync.status.success')

    await wrapper.get('[data-test="test-1"]').trigger('click')
    expect(wrapper.emitted('test')?.[0]?.[0]).toMatchObject({ id: 1 })

    await wrapper.get('[data-test="sync-1"]').trigger('click')
    expect(wrapper.emitted('sync')?.[0]?.[0]).toMatchObject({ id: 1 })

    await wrapper.get('[data-test="delete-1"]').trigger('click')
    expect(wrapper.emitted('delete')?.[0]?.[0]).toMatchObject({ id: 1 })

    await wrapper.get('[data-test="toggle-enabled-1"]').trigger('click')
    expect(wrapper.emitted('toggle')?.[0]).toEqual([expect.objectContaining({ id: 1 }), false])

    const edit = wrapper.findAll('button').find((button) => button.text() === 'common.edit')
    await edit!.trigger('click')
    expect(wrapper.emitted('edit')?.[0]?.[0]).toMatchObject({ id: 1 })

    await wrapper.get('[data-test="refresh-connections"]').trigger('click')
    expect(wrapper.emitted('refresh')).toHaveLength(1)
  })

  it('switches auth_mode between password fields and a single token field', async () => {
    const wrapper = mountDialog({})
    expect(wrapper.find('[data-test="field-email"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="field-password"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="field-token"]').exists()).toBe(false)

    await wrapper.get('[data-test="auth-mode-token"]').trigger('click')
    expect(wrapper.find('[data-test="field-token"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="token-hint"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="field-email"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="field-password"]').exists()).toBe(false)

    await wrapper.get<HTMLInputElement>('[data-test="field-name"]').setValue('Token conn')
    await wrapper.get<HTMLInputElement>('[data-test="field-base-url"]').setValue('https://up.example.com')
    await wrapper.get<HTMLInputElement>('[data-test="field-token"]').setValue('token-canary')
    await wrapper.get('[data-test="dialog-save"]').trigger('click')

    const payload = wrapper.emitted('save')?.[0]?.[0] as UpstreamConnectionSubmit
    expect(payload).toMatchObject({
      name: 'Token conn',
      base_url: 'https://up.example.com',
      auth_mode: 'token',
      token: 'token-canary',
      enabled: false,
      interval_minutes: 30,
    })
  })

  it('blocks save with a validation error when required credentials are missing', async () => {
    const wrapper = mountDialog({})
    await wrapper.get<HTMLInputElement>('[data-test="field-name"]').setValue('No creds')
    await wrapper.get<HTMLInputElement>('[data-test="field-base-url"]').setValue('https://up.example.com')
    await wrapper.get('[data-test="dialog-save"]').trigger('click')
    expect(wrapper.emitted('save')).toBeUndefined()
    expect(wrapper.emitted('validationError')?.[0]?.[0]).toBe(
      'admin.upstreamRateSync.form.errors.credentialsRequired',
    )
  })

  it('shows the test result and the Turnstile downgrade hint inside the dialog', async () => {
    const editing = mountDialog({ connection: connection() })
    const testButton = editing.get('[data-test="dialog-test"]')
    await testButton.trigger('click')
    expect(editing.emitted('test')).toHaveLength(1)

    const withResult = mountDialog({
      connection: connection(),
      testResult: { keys_found: 5, accounts_matched: 3 },
    })
    expect(withResult.get('[data-test="test-result"]').text()).toContain(
      'admin.upstreamRateSync.form.testResult',
    )

    const withTurnstile = mountDialog({
      connection: connection(),
      testError: 'upstream requires captcha',
      testErrorCode: 'upstream_turnstile_required',
    })
    expect(withTurnstile.find('[data-test="turnstile-hint"]').exists()).toBe(true)
    expect(withTurnstile.find('[data-test="test-error"]').exists()).toBe(false)
  })

  it('keeps saved credentials with a blank-secret placeholder when editing', () => {
    const wrapper = mountDialog({ connection: connection() })
    const password = wrapper.get<HTMLInputElement>('[data-test="field-password"]')
    expect(password.element.value).toBe('')
    expect(password.attributes('placeholder')).toContain('admin.upstreamRateSync.form.keepCredential')
  })

  it('renders runs and expands per-account detail rows', async () => {
    const wrapper = mount(SyncRunTable, {
      props: {
        runs: [run()],
        total: 1,
        page: 1,
        pageSize: 20,
        connections: [connection()],
        filters: emptyRunFilters(),
        loading: false,
        error: '',
      },
      global: { stubs: { Pagination: PaginationStub } },
    })
    expect(wrapper.text()).toContain('Main upstream')
    expect(wrapper.find('[data-test="run-details-1"]').exists()).toBe(false)

    await wrapper.get('[data-test="expand-1"]').trigger('click')
    const details = wrapper.get('[data-test="run-details-1"]')
    expect(details.text()).toContain('sk-abcd1234')
    expect(details.text()).toContain('vip')
    expect(details.text()).toContain('1 → 1.2')
    expect(details.text()).toContain('admin.upstreamRateSync.detailActions.updated')
    expect(details.text()).toContain('admin.upstreamRateSync.detailActions.unmatched')

    await wrapper.get('[data-test="expand-1"]').trigger('click')
    expect(wrapper.find('[data-test="run-details-1"]').exists()).toBe(false)
  })

  it('applies connection and status filters', async () => {
    const wrapper = mount(SyncRunTable, {
      props: {
        runs: [],
        total: 0,
        page: 1,
        pageSize: 20,
        connections: [connection()],
        filters: emptyRunFilters(),
        loading: false,
        error: '',
      },
      global: { stubs: { Pagination: PaginationStub } },
    })
    await wrapper.get<HTMLSelectElement>('[data-test="filter-connection"]').setValue('1')
    await wrapper.get<HTMLSelectElement>('[data-test="filter-status"]').setValue('failed')
    await wrapper.get('form').trigger('submit')
    expect(wrapper.emitted('search')?.[0]?.[0]).toEqual({ connection_id: '1', status: 'failed' })

    await wrapper.get('[data-test="filter-reset"]').trigger('click')
    expect(wrapper.emitted('search')?.at(-1)?.[0]).toEqual({ connection_id: '', status: '' })
  })
})
