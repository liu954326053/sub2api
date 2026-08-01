<template>
  <BaseDialog
    :show="show"
    :title="isEditing ? t('admin.upstreamRateSync.form.editTitle') : t('admin.upstreamRateSync.form.createTitle')"
    width="normal"
    @close="$emit('close')"
  >
    <form class="space-y-4" data-test="connection-form" @submit.prevent="submit">
      <label class="block text-sm text-gray-700 dark:text-dark-200">
        <span>{{ t('admin.upstreamRateSync.form.name') }}</span>
        <input
          v-model="form.name"
          type="text"
          class="input mt-1 w-full"
          :aria-label="t('admin.upstreamRateSync.form.name')"
          :placeholder="t('admin.upstreamRateSync.form.namePlaceholder')"
          data-test="field-name"
        />
      </label>

      <label class="block text-sm text-gray-700 dark:text-dark-200">
        <span>{{ t('admin.upstreamRateSync.form.baseUrl') }}</span>
        <input
          v-model="form.base_url"
          type="text"
          class="input mt-1 w-full font-mono"
          :aria-label="t('admin.upstreamRateSync.form.baseUrl')"
          placeholder="https://upstream.example.com"
          data-test="field-base-url"
        />
      </label>

      <div class="block text-sm text-gray-700 dark:text-dark-200">
        <span>{{ t('admin.upstreamRateSync.form.authMode') }}</span>
        <div class="mt-2 flex gap-2" role="radiogroup" :aria-label="t('admin.upstreamRateSync.form.authMode')">
          <button
            v-for="mode in authModes"
            :key="mode"
            type="button"
            class="btn btn-sm flex-1"
            :class="form.auth_mode === mode ? 'btn-primary' : 'btn-secondary'"
            :aria-pressed="form.auth_mode === mode"
            :data-test="`auth-mode-${mode}`"
            @click="setAuthMode(mode)"
          >
            {{ t(`admin.upstreamRateSync.authMode.${mode}`) }}
          </button>
        </div>
      </div>

      <template v-if="form.auth_mode === 'password'">
        <label class="block text-sm text-gray-700 dark:text-dark-200">
          <span>{{ t('admin.upstreamRateSync.form.email') }}</span>
          <input
            v-model="form.email"
            type="email"
            class="input mt-1 w-full"
            :aria-label="t('admin.upstreamRateSync.form.email')"
            :placeholder="credentialPlaceholder"
            data-test="field-email"
          />
        </label>
        <label class="block text-sm text-gray-700 dark:text-dark-200">
          <span>{{ t('admin.upstreamRateSync.form.password') }}</span>
          <input
            v-model="form.password"
            type="password"
            class="input mt-1 w-full"
            :aria-label="t('admin.upstreamRateSync.form.password')"
            :placeholder="credentialPlaceholder"
            autocomplete="new-password"
            data-test="field-password"
          />
        </label>
        <p class="text-xs text-gray-500 dark:text-dark-400">
          {{ t('admin.upstreamRateSync.form.passwordHint') }}
        </p>
      </template>

      <template v-else>
        <label class="block text-sm text-gray-700 dark:text-dark-200">
          <span>{{ t('admin.upstreamRateSync.form.token') }}</span>
          <input
            v-model="form.token"
            type="password"
            class="input mt-1 w-full font-mono"
            :aria-label="t('admin.upstreamRateSync.form.token')"
            :placeholder="credentialPlaceholder"
            autocomplete="off"
            data-test="field-token"
          />
        </label>
        <p class="text-xs text-gray-500 dark:text-dark-400" data-test="token-hint">
          {{ t('admin.upstreamRateSync.form.tokenHint') }}
        </p>
      </template>

      <div class="grid grid-cols-2 gap-4">
        <label class="block text-sm text-gray-700 dark:text-dark-200">
          <span>{{ t('admin.upstreamRateSync.form.interval') }}</span>
          <input
            v-model.number="form.interval_minutes"
            type="number"
            min="5"
            max="1440"
            class="input mt-1 w-full"
            :aria-label="t('admin.upstreamRateSync.form.interval')"
            data-test="field-interval"
          />
        </label>
        <label class="flex items-end gap-2.5 pb-1 text-sm text-gray-700 dark:text-dark-200">
          <Toggle
            :model-value="form.enabled"
            :aria-label="t('admin.upstreamRateSync.form.enabled')"
            data-test="field-enabled"
            @update:model-value="form.enabled = $event"
          />
          <span>{{ t('admin.upstreamRateSync.form.enabled') }}</span>
        </label>
      </div>

      <div
        v-if="showTurnstileHint"
        role="alert"
        class="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-900/70 dark:bg-amber-950/30 dark:text-amber-200"
        data-test="turnstile-hint"
      >
        {{ t('admin.upstreamRateSync.form.turnstileHint') }}
      </div>
      <div
        v-else-if="testError"
        role="alert"
        class="rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300"
        data-test="test-error"
      >
        {{ testError }}
      </div>

      <div
        v-if="testResult"
        class="rounded-lg bg-green-50 px-4 py-3 text-sm text-green-700 dark:bg-green-950/30 dark:text-green-300"
        data-test="test-result"
      >
        {{ t('admin.upstreamRateSync.form.testResult', { keys: testResult.keys_found, matched: testResult.accounts_matched }) }}
      </div>
    </form>

    <template #footer>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <button
            v-if="isEditing"
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="testing || saving"
            data-test="dialog-test"
            @click="$emit('test')"
          >
            {{ testing ? t('admin.upstreamRateSync.actions.testing') : t('admin.upstreamRateSync.actions.test') }}
          </button>
          <span v-else class="text-xs text-gray-400 dark:text-dark-500" data-test="test-after-save-hint">
            {{ t('admin.upstreamRateSync.form.testAfterSave') }}
          </span>
        </div>
        <div class="flex items-center gap-2">
          <button type="button" class="btn btn-secondary" :disabled="saving" @click="$emit('close')">
            {{ t('common.cancel') }}
          </button>
          <button type="button" class="btn btn-primary" :disabled="saving" data-test="dialog-save" @click="submit">
            {{ saving ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Toggle from '@/components/common/Toggle.vue'
import type {
  UpstreamAuthMode,
  UpstreamConnection,
  UpstreamConnectionForm,
  UpstreamConnectionSubmit,
  UpstreamConnectionTestResult,
} from '../types'
import {
  connectionToForm,
  emptyConnectionForm,
  formToSubmit,
  isTurnstileError,
  validateConnectionForm,
} from '../viewModel'

const { t } = useI18n()

const props = defineProps<{
  show: boolean
  connection: UpstreamConnection | null
  saving: boolean
  testing: boolean
  testResult: UpstreamConnectionTestResult | null
  testError: string
  testErrorCode: string
}>()

const emit = defineEmits<{
  close: []
  save: [payload: UpstreamConnectionSubmit]
  test: []
  validationError: [messageKey: string]
}>()

const authModes: UpstreamAuthMode[] = ['password', 'token']
const form = reactive<UpstreamConnectionForm>(emptyConnectionForm())

const isEditing = computed(() => form.id !== null)

const credentialPlaceholder = computed(() =>
  isEditing.value && props.connection?.has_credentials
    ? t('admin.upstreamRateSync.form.keepCredential')
    : '',
)

const showTurnstileHint = computed(() => Boolean(props.testErrorCode) && isTurnstileError(props.testErrorCode))

watch(
  () => [props.show, props.connection] as const,
  ([show]) => {
    if (!show) return
    Object.assign(form, props.connection ? connectionToForm(props.connection) : emptyConnectionForm())
  },
  { immediate: true },
)

function setAuthMode(mode: UpstreamAuthMode) {
  form.auth_mode = mode
}

function submit() {
  const errorKey = validateConnectionForm(form)
  if (errorKey) {
    emit('validationError', errorKey)
    return
  }
  // 深拷贝快照，避免父组件保存期间弹窗内的表单继续被编辑污染提交内容
  emit('save', formToSubmit({ ...form }))
}
</script>
