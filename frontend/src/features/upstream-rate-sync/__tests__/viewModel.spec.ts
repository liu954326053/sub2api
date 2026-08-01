import { describe, expect, it } from 'vitest'
import type { UpstreamConnectionForm } from '../types'
import {
  connectionToForm,
  emptyConnectionForm,
  formToSubmit,
  isTurnstileError,
  validateConnectionForm,
} from '../viewModel'

const validForm = (overrides: Partial<UpstreamConnectionForm> = {}): UpstreamConnectionForm => ({
  ...emptyConnectionForm(),
  name: 'Conn',
  base_url: 'https://up.example.com',
  email: 'ops@example.com',
  password: 'secret',
  ...overrides,
})

describe('upstream-rate-sync viewModel', () => {
  it('builds a submit payload carrying only the active auth mode credentials', () => {
    const passwordSubmit = formToSubmit(validForm({ token: 'stray-token' }))
    expect(passwordSubmit).toMatchObject({ auth_mode: 'password', email: 'ops@example.com', password: 'secret' })
    expect(passwordSubmit.token).toBe('')

    const tokenSubmit = formToSubmit(validForm({ auth_mode: 'token', token: ' tok ', password: 'stray' }))
    expect(tokenSubmit).toMatchObject({ auth_mode: 'token', token: 'tok', email: '', password: '' })
  })

  it('validates required fields, interval bounds, and create-vs-edit credential rules', () => {
    expect(validateConnectionForm(validForm())).toBe('')
    expect(validateConnectionForm(validForm({ name: ' ' }))).toBe('admin.upstreamRateSync.form.errors.nameRequired')
    expect(validateConnectionForm(validForm({ base_url: '' }))).toBe('admin.upstreamRateSync.form.errors.baseUrlRequired')
    expect(validateConnectionForm(validForm({ interval_minutes: 4 }))).toBe('admin.upstreamRateSync.form.errors.intervalRange')
    expect(validateConnectionForm(validForm({ interval_minutes: 1441 }))).toBe('admin.upstreamRateSync.form.errors.intervalRange')

    // 新建 password 模式：账密必填
    expect(validateConnectionForm(validForm({ email: '', password: '' }))).toBe(
      'admin.upstreamRateSync.form.errors.credentialsRequired',
    )
    // 编辑已有凭证：留空表示保持不变
    expect(
      validateConnectionForm(validForm({ id: 9, has_credentials: true, email: '', password: '' })),
    ).toBe('')
    // 新建 token 模式：token 必填
    expect(validateConnectionForm(validForm({ auth_mode: 'token', token: ' ' }))).toBe(
      'admin.upstreamRateSync.form.errors.tokenRequired',
    )
  })

  it('maps an existing connection to a form with blank credentials and flags', () => {
    const form = connectionToForm({
      id: 3,
      name: 'Saved',
      base_url: 'https://saved.example.com',
      auth_mode: 'token',
      enabled: true,
      interval_minutes: 60,
      last_sync_at: null,
      last_status: null,
      last_error: null,
      has_credentials: true,
      token_expires_at: null,
      created_at: '2026-07-01T00:00:00Z',
    })
    expect(form).toMatchObject({
      id: 3,
      name: 'Saved',
      auth_mode: 'token',
      enabled: true,
      interval_minutes: 60,
      has_credentials: true,
      email: '',
      password: '',
      token: '',
    })
  })

  it('detects Turnstile-related error codes case-insensitively', () => {
    expect(isTurnstileError('upstream_turnstile_required')).toBe(true)
    expect(isTurnstileError('TURNSTILE_CHALLENGE')).toBe(true)
    expect(isTurnstileError('invalid_credentials')).toBe(false)
    expect(isTurnstileError('')).toBe(false)
  })
})
