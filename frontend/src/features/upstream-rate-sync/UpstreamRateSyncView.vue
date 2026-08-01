<template>
  <AppLayout>
    <div class="w-full pb-8">
      <div
        v-if="loadErrors.connections"
        role="alert"
        class="mb-4 rounded-xl border border-red-200 bg-red-50 p-5 dark:border-red-900 dark:bg-red-950/30"
      >
        <p class="text-sm text-red-700 dark:text-red-300">{{ loadErrors.connections }}</p>
        <button type="button" class="btn btn-secondary btn-sm mt-3" @click="loadConnections">
          {{ t('admin.upstreamRateSync.actions.retry') }}
        </button>
      </div>

      <main class="flex flex-col gap-6">
        <ConnectionTable
          :connections="connections"
          :loading="loading.connections"
          :testing-ids="testingIds"
          :syncing-ids="syncingIds"
          @create="openCreateDialog"
          @refresh="refreshAll"
          @toggle="toggleEnabled"
          @edit="openEditDialog"
          @test="testFromTable"
          @sync="syncNow"
          @delete="requestDelete"
        />

        <div class="card px-4 sm:px-6 lg:px-8">
          <SyncRunTable
            :runs="runs.items"
            :total="runs.total"
            :page="runs.page"
            :page-size="runs.page_size"
            :connections="connections"
            :filters="runFilters"
            :loading="loading.runs"
            :error="loadErrors.runs"
            @search="applyRunFilters"
            @refresh="loadRuns"
            @page="changeRunPage"
            @page-size="changeRunPageSize"
          />
        </div>
      </main>
    </div>

    <ConnectionEditDialog
      :show="showDialog"
      :connection="editingConnection"
      :saving="loading.saving"
      :testing="loading.testing"
      :test-result="dialogTestResult"
      :test-error="dialogTestError"
      :test-error-code="dialogTestErrorCode"
      @close="closeDialog"
      @save="saveConnection"
      @test="testFromDialog"
      @validation-error="showValidationError"
    />

    <ConfirmDialog
      :show="deleteTarget !== null"
      :title="t('admin.upstreamRateSync.delete.title')"
      :message="t('admin.upstreamRateSync.delete.message', { name: deleteTarget?.name ?? '' })"
      :confirm-text="t('common.delete')"
      danger
      @confirm="confirmDelete"
      @cancel="deleteTarget = null"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorCode, extractApiErrorMessage } from '@/utils/apiError'
import ConnectionTable from './components/ConnectionTable.vue'
import ConnectionEditDialog from './components/ConnectionEditDialog.vue'
import SyncRunTable from './components/SyncRunTable.vue'
import upstreamRateSyncAPI from './api'
import type {
  UpstreamConnection,
  UpstreamConnectionSubmit,
  UpstreamConnectionTestResult,
  UpstreamRunFilters,
  UpstreamSyncRunPage,
} from './types'
import { emptyRunFilters, isTurnstileError } from './viewModel'

const { t } = useI18n()
const appStore = useAppStore()

const connections = ref<UpstreamConnection[]>([])
const runs = reactive<UpstreamSyncRunPage>({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
const runFilters = ref<UpstreamRunFilters>(emptyRunFilters())

const showDialog = ref(false)
const editingConnection = ref<UpstreamConnection | null>(null)
const dialogTestResult = ref<UpstreamConnectionTestResult | null>(null)
const dialogTestError = ref('')
const dialogTestErrorCode = ref('')
const deleteTarget = ref<UpstreamConnection | null>(null)
const testingIds = ref<number[]>([])
const syncingIds = ref<number[]>([])

const loading = reactive({ connections: false, runs: false, saving: false, testing: false, deleting: false })
const loadErrors = reactive({ connections: '', runs: '' })

function errorMessage(error: unknown, fallbackKey: string): string {
  const code = extractApiErrorCode(error)
  if (code) {
    const key = `admin.upstreamRateSync.errors.${code}`
    const translated = t(key)
    if (translated !== key) return translated
  }
  return extractApiErrorMessage(error, t(fallbackKey))
}

async function loadConnections() {
  loading.connections = true
  loadErrors.connections = ''
  try {
    connections.value = await upstreamRateSyncAPI.listConnections()
  } catch (error) {
    loadErrors.connections = errorMessage(error, 'admin.upstreamRateSync.errors.loadConnections')
  } finally {
    loading.connections = false
  }
}

async function loadRuns() {
  loading.runs = true
  loadErrors.runs = ''
  try {
    const result = await upstreamRateSyncAPI.listRuns(runFilters.value, runs.page, runs.page_size)
    Object.assign(runs, result)
  } catch (error) {
    loadErrors.runs = errorMessage(error, 'admin.upstreamRateSync.errors.loadRuns')
  } finally {
    loading.runs = false
  }
}

function refreshAll() {
  void Promise.allSettled([loadConnections(), loadRuns()])
}

function applyRunFilters(filters: UpstreamRunFilters) {
  runFilters.value = { ...filters }
  runs.page = 1
  void loadRuns()
}
function changeRunPage(page: number) {
  runs.page = page
  void loadRuns()
}
function changeRunPageSize(pageSize: number) {
  runs.page_size = pageSize
  runs.page = 1
  void loadRuns()
}

function resetDialogTestState() {
  dialogTestResult.value = null
  dialogTestError.value = ''
  dialogTestErrorCode.value = ''
}

function openCreateDialog() {
  editingConnection.value = null
  resetDialogTestState()
  showDialog.value = true
}
function openEditDialog(connection: UpstreamConnection) {
  editingConnection.value = connection
  resetDialogTestState()
  showDialog.value = true
}
function closeDialog() {
  showDialog.value = false
  editingConnection.value = null
  resetDialogTestState()
}

function showValidationError(messageKey: string) {
  appStore.showError(t(messageKey))
}

async function saveConnection(payload: UpstreamConnectionSubmit) {
  loading.saving = true
  try {
    if (editingConnection.value) {
      await upstreamRateSyncAPI.updateConnection(editingConnection.value.id, payload)
    } else {
      await upstreamRateSyncAPI.createConnection(payload)
    }
    appStore.showSuccess(t('admin.upstreamRateSync.messages.saved'))
    closeDialog()
    await loadConnections()
  } catch (error) {
    appStore.showError(errorMessage(error, 'admin.upstreamRateSync.errors.save'))
  } finally {
    loading.saving = false
  }
}

async function toggleEnabled(connection: UpstreamConnection, value: boolean) {
  try {
    await upstreamRateSyncAPI.updateConnection(connection.id, {
      name: connection.name,
      base_url: connection.base_url,
      auth_mode: connection.auth_mode,
      enabled: value,
      interval_minutes: connection.interval_minutes,
    })
    appStore.showSuccess(
      t(value ? 'admin.upstreamRateSync.messages.enabled' : 'admin.upstreamRateSync.messages.disabled'),
    )
    await loadConnections()
  } catch (error) {
    appStore.showError(errorMessage(error, 'admin.upstreamRateSync.errors.save'))
  }
}

function describeTestResult(result: UpstreamConnectionTestResult): string {
  return t('admin.upstreamRateSync.messages.testSucceeded', {
    keys: result.keys_found,
    matched: result.accounts_matched,
  })
}

async function runTest(id: number): Promise<UpstreamConnectionTestResult | null> {
  try {
    return await upstreamRateSyncAPI.testConnection(id)
  } catch (error) {
    const code = extractApiErrorCode(error) ?? ''
    if (code && isTurnstileError(code)) {
      appStore.showError(t('admin.upstreamRateSync.form.turnstileHint'))
    } else {
      appStore.showError(errorMessage(error, 'admin.upstreamRateSync.errors.test'))
    }
    return null
  }
}

async function testFromTable(connection: UpstreamConnection) {
  if (testingIds.value.includes(connection.id)) return
  testingIds.value = [...testingIds.value, connection.id]
  try {
    const result = await runTest(connection.id)
    if (result) appStore.showSuccess(describeTestResult(result))
  } finally {
    testingIds.value = testingIds.value.filter((id) => id !== connection.id)
  }
}

async function testFromDialog() {
  const connection = editingConnection.value
  if (!connection || loading.testing) return
  loading.testing = true
  resetDialogTestState()
  try {
    const result = await upstreamRateSyncAPI.testConnection(connection.id)
    dialogTestResult.value = result
  } catch (error) {
    dialogTestErrorCode.value = extractApiErrorCode(error) ?? ''
    dialogTestError.value = dialogTestErrorCode.value && isTurnstileError(dialogTestErrorCode.value)
      ? ''
      : errorMessage(error, 'admin.upstreamRateSync.errors.test')
  } finally {
    loading.testing = false
  }
}

async function syncNow(connection: UpstreamConnection) {
  if (syncingIds.value.includes(connection.id)) return
  syncingIds.value = [...syncingIds.value, connection.id]
  try {
    const run = await upstreamRateSyncAPI.syncConnection(connection.id)
    appStore.showSuccess(
      t('admin.upstreamRateSync.messages.syncFinished', {
        status: t(`admin.upstreamRateSync.status.${run.status}`),
        updated: run.accounts_updated,
      }),
    )
    await Promise.allSettled([loadConnections(), loadRuns()])
  } catch (error) {
    appStore.showError(errorMessage(error, 'admin.upstreamRateSync.errors.sync'))
  } finally {
    syncingIds.value = syncingIds.value.filter((id) => id !== connection.id)
  }
}

function requestDelete(connection: UpstreamConnection) {
  deleteTarget.value = connection
}

async function confirmDelete() {
  const target = deleteTarget.value
  deleteTarget.value = null
  if (!target || loading.deleting) return
  loading.deleting = true
  try {
    await upstreamRateSyncAPI.deleteConnection(target.id)
    appStore.showSuccess(t('admin.upstreamRateSync.messages.deleted'))
    await Promise.allSettled([loadConnections(), loadRuns()])
  } catch (error) {
    appStore.showError(errorMessage(error, 'admin.upstreamRateSync.errors.delete'))
  } finally {
    loading.deleting = false
  }
}

onMounted(() => {
  void Promise.allSettled([loadConnections(), loadRuns()])
})
</script>
