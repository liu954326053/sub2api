<template>
  <section aria-labelledby="upstream-runs-title" class="py-6">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h2 id="upstream-runs-title" class="text-base font-semibold text-gray-950 dark:text-white">
          {{ t('admin.upstreamRateSync.runs.title') }}
        </h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">
          {{ t('admin.upstreamRateSync.runs.description') }}
        </p>
      </div>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" data-test="refresh-runs" @click="$emit('refresh')">
        {{ t('common.refresh') }}
      </button>
    </div>

    <form class="mt-5 flex flex-wrap items-end gap-3" @submit.prevent="applyFilters">
      <label class="text-xs text-gray-600 dark:text-dark-200">
        <span>{{ t('admin.upstreamRateSync.runs.filters.connection') }}</span>
        <select
          v-model="localFilters.connection_id"
          class="input mt-1 w-full sm:w-56"
          :aria-label="t('admin.upstreamRateSync.runs.filters.connection')"
          data-test="filter-connection"
        >
          <option value="">{{ t('common.all') }}</option>
          <option v-for="connection in connections" :key="connection.id" :value="String(connection.id)">
            {{ connection.name }}
          </option>
        </select>
      </label>
      <label class="text-xs text-gray-600 dark:text-dark-200">
        <span>{{ t('admin.upstreamRateSync.runs.filters.status') }}</span>
        <select
          v-model="localFilters.status"
          class="input mt-1 w-full sm:w-40"
          :aria-label="t('admin.upstreamRateSync.runs.filters.status')"
          data-test="filter-status"
        >
          <option value="">{{ t('common.all') }}</option>
          <option value="success">{{ t('admin.upstreamRateSync.status.success') }}</option>
          <option value="partial">{{ t('admin.upstreamRateSync.status.partial') }}</option>
          <option value="failed">{{ t('admin.upstreamRateSync.status.failed') }}</option>
        </select>
      </label>
      <div class="flex items-end gap-2">
        <button type="submit" class="btn btn-primary btn-sm" data-test="filter-apply">{{ t('common.search') }}</button>
        <button type="button" class="btn btn-ghost btn-sm" data-test="filter-reset" @click="resetFilters">
          {{ t('common.reset') }}
        </button>
      </div>
    </form>

    <div v-if="error" role="alert" class="mt-4 rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/30 dark:text-red-300">
      {{ error }}
    </div>

    <div class="mt-5 overflow-x-auto rounded-xl border border-gray-200 dark:border-dark-700/60">
      <table class="min-w-[960px] w-full text-left text-sm">
        <thead class="bg-gray-50 text-xs uppercase tracking-wide text-gray-500 dark:bg-dark-900/70 dark:text-dark-400">
          <tr>
            <th class="w-10 px-3 py-3"></th>
            <th class="px-3 py-3 font-medium">{{ t('admin.upstreamRateSync.runs.columns.startedAt') }}</th>
            <th class="px-3 py-3 font-medium">{{ t('admin.upstreamRateSync.runs.columns.connection') }}</th>
            <th class="px-3 py-3 font-medium">{{ t('admin.upstreamRateSync.runs.columns.status') }}</th>
            <th class="px-3 py-3 font-medium">{{ t('admin.upstreamRateSync.runs.columns.counts') }}</th>
            <th class="px-3 py-3 font-medium">{{ t('admin.upstreamRateSync.runs.columns.error') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-700 dark:bg-transparent">
          <tr v-if="loading">
            <td colspan="6" class="px-4 py-12 text-center text-gray-500" aria-busy="true">{{ t('common.loading') }}</td>
          </tr>
          <tr v-else-if="runs.length === 0">
            <td colspan="6" class="px-4 py-12 text-center text-gray-500">{{ t('admin.upstreamRateSync.runs.empty') }}</td>
          </tr>
          <template v-for="run in runs" v-else :key="run.id">
            <tr class="align-top hover:bg-gray-50/70 dark:hover:bg-dark-800/70" :data-test="`run-${run.id}`">
              <td class="px-3 py-3">
                <button
                  type="button"
                  class="rounded p-1 text-gray-400 transition-colors hover:text-gray-600 dark:hover:text-dark-200"
                  :aria-expanded="expandedIds.has(run.id)"
                  :aria-label="t('admin.upstreamRateSync.runs.toggleDetails')"
                  :data-test="`expand-${run.id}`"
                  @click="toggleExpanded(run.id)"
                >
                  <svg
                    class="h-4 w-4 transition-transform"
                    :class="{ 'rotate-90': expandedIds.has(run.id) }"
                    fill="none"
                    viewBox="0 0 24 24"
                    stroke="currentColor"
                    stroke-width="2"
                  >
                    <path stroke-linecap="round" stroke-linejoin="round" d="m8.25 4.5 7.5 7.5-7.5 7.5" />
                  </svg>
                </button>
              </td>
              <td class="whitespace-nowrap px-3 py-3 text-xs text-gray-600 dark:text-dark-300">
                {{ formatDate(run.started_at) }}
              </td>
              <td class="px-3 py-3 text-gray-700 dark:text-dark-200">{{ connectionName(run) }}</td>
              <td class="px-3 py-3">
                <span class="rounded-full px-2 py-0.5 text-xs font-medium" :class="statusBadgeClass(run.status)">
                  {{ t(`admin.upstreamRateSync.status.${run.status}`) }}
                </span>
              </td>
              <td class="px-3 py-3 text-xs text-gray-600 dark:text-dark-300">
                {{ t('admin.upstreamRateSync.runs.countsSummary', {
                  fetched: run.keys_fetched,
                  matched: run.accounts_matched,
                  updated: run.accounts_updated,
                  unchanged: run.accounts_unchanged,
                  unmatched: run.accounts_unmatched,
                }) }}
              </td>
              <td class="max-w-56 px-3 py-3">
                <p v-if="run.error" class="truncate text-xs text-red-600 dark:text-red-400" :title="run.error">{{ run.error }}</p>
                <span v-else class="text-xs text-gray-400 dark:text-dark-500">—</span>
              </td>
            </tr>
            <tr v-if="expandedIds.has(run.id)" :data-test="`run-details-${run.id}`">
              <td colspan="6" class="bg-gray-50/70 px-6 py-4 dark:bg-dark-800/40">
                <template v-if="run.details && run.details.length > 0">
                  <table class="w-full text-left text-xs">
                    <thead class="text-gray-500 dark:text-dark-400">
                      <tr>
                        <th class="px-2 py-1.5 font-medium">{{ t('admin.upstreamRateSync.runs.details.accountId') }}</th>
                        <th class="px-2 py-1.5 font-medium">{{ t('admin.upstreamRateSync.runs.details.keyPrefix') }}</th>
                        <th class="px-2 py-1.5 font-medium">{{ t('admin.upstreamRateSync.runs.details.groupName') }}</th>
                        <th class="px-2 py-1.5 font-medium">{{ t('admin.upstreamRateSync.runs.details.rateChange') }}</th>
                        <th class="px-2 py-1.5 font-medium">{{ t('admin.upstreamRateSync.runs.details.action') }}</th>
                      </tr>
                    </thead>
                    <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                      <tr v-for="(detail, index) in run.details" :key="`${run.id}-${detail.account_id}-${index}`">
                        <td class="px-2 py-1.5 text-gray-700 dark:text-dark-200">{{ detail.account_id }}</td>
                        <td class="px-2 py-1.5 font-mono text-gray-600 dark:text-dark-300">{{ detail.key_prefix }}</td>
                        <td class="px-2 py-1.5 text-gray-700 dark:text-dark-200">{{ detail.group_name || '—' }}</td>
                        <td class="px-2 py-1.5 text-gray-700 dark:text-dark-200">
                          {{ formatRate(detail.old_rate) }} → {{ formatRate(detail.new_rate) }}
                        </td>
                        <td class="px-2 py-1.5">
                          <span class="rounded-full px-2 py-0.5 font-medium" :class="actionBadgeClass(detail.action)">
                            {{ t(`admin.upstreamRateSync.detailActions.${detail.action}`) }}
                          </span>
                        </td>
                      </tr>
                    </tbody>
                  </table>
                </template>
                <p v-else class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.upstreamRateSync.runs.noDetails') }}</p>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
      <Pagination
        :total="total"
        :page="page"
        :page-size="pageSize"
        @update:page="$emit('page', $event)"
        @update:page-size="$emit('page-size', $event)"
      />
    </div>
  </section>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Pagination from '@/components/common/Pagination.vue'
import type {
  UpstreamConnection,
  UpstreamRunFilters,
  UpstreamSyncAction,
  UpstreamSyncRun,
  UpstreamSyncStatus,
} from '../types'

const { t, locale } = useI18n()

const props = defineProps<{
  runs: UpstreamSyncRun[]
  total: number
  page: number
  pageSize: number
  connections: UpstreamConnection[]
  filters: UpstreamRunFilters
  loading: boolean
  error: string
}>()

const emit = defineEmits<{
  search: [filters: UpstreamRunFilters]
  refresh: []
  page: [page: number]
  'page-size': [pageSize: number]
}>()

const localFilters = reactive<UpstreamRunFilters>({ ...props.filters })
const expandedIds = ref<Set<number>>(new Set())

watch(
  () => props.filters,
  (value) => Object.assign(localFilters, value),
)

function applyFilters() {
  emit('search', { ...localFilters })
}

function resetFilters() {
  localFilters.connection_id = ''
  localFilters.status = ''
  applyFilters()
}

function toggleExpanded(id: number) {
  const next = new Set(expandedIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expandedIds.value = next
}

function connectionName(run: UpstreamSyncRun): string {
  if (run.connection_name) return run.connection_name
  const connection = props.connections.find((item) => item.id === run.connection_id)
  return connection ? connection.name : `#${run.connection_id}`
}

function statusBadgeClass(status: UpstreamSyncStatus): string {
  switch (status) {
    case 'success':
      return 'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300'
    case 'partial':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
    case 'failed':
      return 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
    default:
      return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
  }
}

function actionBadgeClass(action: UpstreamSyncAction): string {
  switch (action) {
    case 'updated':
      return 'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300'
    case 'unchanged':
      return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
    case 'unmatched':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
    case 'threshold_skipped':
    case 'manual_override':
      return 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
    default:
      return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
  }
}

function formatRate(value: number | null): string {
  return value === null || value === undefined ? '—' : String(value)
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}
</script>
