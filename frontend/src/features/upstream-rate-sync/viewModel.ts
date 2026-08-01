import type {
  UpstreamAuthMode,
  UpstreamConnection,
  UpstreamConnectionForm,
  UpstreamConnectionSubmit,
  UpstreamRunFilters,
} from './types'

export const MIN_INTERVAL_MINUTES = 5
export const MAX_INTERVAL_MINUTES = 1440

export function emptyRunFilters(): UpstreamRunFilters {
  return { connection_id: '', status: '' }
}

export function emptyConnectionForm(): UpstreamConnectionForm {
  return {
    id: null,
    name: '',
    base_url: '',
    auth_mode: 'password',
    email: '',
    password: '',
    token: '',
    enabled: false,
    interval_minutes: 30,
    has_credentials: false,
  }
}

export function connectionToForm(connection: UpstreamConnection): UpstreamConnectionForm {
  return {
    id: connection.id,
    name: connection.name,
    base_url: connection.base_url,
    auth_mode: connection.auth_mode,
    email: '',
    password: '',
    token: '',
    enabled: connection.enabled,
    interval_minutes: connection.interval_minutes,
    has_credentials: connection.has_credentials,
  }
}

export function formToSubmit(form: UpstreamConnectionForm): UpstreamConnectionSubmit {
  return {
    name: form.name.trim(),
    base_url: form.base_url.trim(),
    auth_mode: form.auth_mode,
    email: form.auth_mode === 'password' ? form.email.trim() : '',
    password: form.auth_mode === 'password' ? form.password : '',
    token: form.auth_mode === 'token' ? form.token.trim() : '',
    enabled: form.enabled,
    interval_minutes: form.interval_minutes,
  }
}

/**
 * 校验编辑弹窗表单，返回 i18n 错误 key；空串表示通过。
 * 新建时凭证必填；编辑时留空表示保持不变。
 */
export function validateConnectionForm(form: UpstreamConnectionForm): string {
  if (!form.name.trim()) return 'admin.upstreamRateSync.form.errors.nameRequired'
  if (!form.base_url.trim()) return 'admin.upstreamRateSync.form.errors.baseUrlRequired'
  if (
    !Number.isInteger(form.interval_minutes) ||
    form.interval_minutes < MIN_INTERVAL_MINUTES ||
    form.interval_minutes > MAX_INTERVAL_MINUTES
  ) {
    return 'admin.upstreamRateSync.form.errors.intervalRange'
  }
  const creating = form.id === null
  if (form.auth_mode === 'password') {
    if (creating || !form.has_credentials) {
      if (!form.email.trim() || !form.password) {
        return 'admin.upstreamRateSync.form.errors.credentialsRequired'
      }
    }
  } else if (creating || !form.has_credentials) {
    if (!form.token.trim()) return 'admin.upstreamRateSync.form.errors.tokenRequired'
  }
  return ''
}

/** 后端在连接测试/同步中以上游 Turnstile 错误码返回时，前端降级提示手动粘贴 token。 */
export function isTurnstileError(code: string): boolean {
  return code.toLowerCase().includes('turnstile')
}

export function isValidAuthMode(value: unknown): value is UpstreamAuthMode {
  return value === 'password' || value === 'token'
}
