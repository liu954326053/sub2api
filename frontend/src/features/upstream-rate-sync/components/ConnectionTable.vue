<template>
  <section aria-labelledby="upstream-connections-title">
    <div class="flex items-center justify-between gap-3">
      <h2 id="upstream-connections-title" class="sr-only">
        {{ t('admin.upstreamRateSync.connections.title') }}
      </h2>
      <div class="ml-auto flex items-center gap-2">
        <button
          type="button"
          class="btn btn-secondary btn-sm"
          :disabled="loading"
          data-test="refresh-connections"
          @click="$emit('refresh')"
        >
          <svg
            class="h-4 w-4"
            :class="{ 'animate-spin': loading }"
            fill="none"
            viewBox="0 0 24 24"
            stroke-width="1.5"
            stroke="currentColor"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              d="M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0 3.181 3.183a8.25 8.25 0 0 0 13.803-3.7M4.031 9.865a8.25 8.25 0 0 1 13.803-3.7l3.181 3.182m0-4.991v4.99"
            />
          </svg>
          {{ t('admin.upstreamRateSync.actions.refresh') }}
        </button>
        <button type="button" class="btn btn-primary btn-sm" data-test="add-connection" @click="$emit('create')">
          {{ t('admin.upstreamRateSync.connections.add') }}
        </button>
      </div>
    </div>

    <!-- 空态 -->
    <div
      v-if="!loading && connections.length === 0"
      class="mt-4 flex flex-col items-center rounded-xl border border-dashed border-gray-300 py-12 dark:border-dark-600"
    >
      <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('admin.upstreamRateSync.connections.empty') }}</p>
    </div>

    <!-- 加载骨架 -->
    <div v-else-if="loading" class="mt-4 grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
      <div v-for="i in 3" :key="i" class="card h-40 animate-pulse bg-gray-100 dark:bg-dark-700/50" />
    </div>

    <!-- 连接卡片网格 -->
    <div v-else class="mt-4 grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
      <article
        v-for="row in connections"
        :key="row.id"
        class="card flex flex-col gap-3 p-5"
        :data-test="`connection-card-${row.id}`"
      >
        <!-- 标题行：名称 + 状态徽章 + 启用开关 -->
        <div class="flex items-start justify-between gap-3">
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <h3 class="truncate font-medium text-gray-900 dark:text-white" :title="row.name">{{ row.name }}</h3>
              <span
                v-if="row.last_status"
                class="shrink-0 rounded-full px-2 py-0.5 text-xs font-medium"
                :class="statusBadgeClass(row.last_status)"
                :data-test="`last-status-${row.id}`"
              >
                {{ t(`admin.upstreamRateSync.status.${row.last_status}`) }}
              </span>
            </div>
            <p class="mt-1 truncate font-mono text-xs text-gray-500 dark:text-dark-300" :title="row.base_url">
              {{ row.base_url }}
            </p>
          </div>
          <Toggle
            :model-value="row.enabled"
            :aria-label="t('admin.upstreamRateSync.connections.toggleEnabled', { name: row.name })"
            :data-test="`toggle-enabled-${row.id}`"
            @update:model-value="$emit('toggle', row, $event)"
          />
        </div>

        <!-- 元信息行 -->
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-gray-500 dark:text-dark-300">
          <span class="rounded-full bg-gray-100 px-2 py-0.5 font-medium text-gray-700 dark:bg-dark-700 dark:text-dark-200">
            {{ t(`admin.upstreamRateSync.authMode.${row.auth_mode}`) }}
          </span>
          <span
            v-if="row.last_balance != null"
            class="rounded-full bg-emerald-50 px-2 py-0.5 font-mono font-medium text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300"
            :data-test="`balance-${row.id}`"
          >
            {{ t('admin.upstreamRateSync.connections.balance') }} ${{ row.last_balance.toFixed(2) }}
          </span>
          <span>{{ t('admin.upstreamRateSync.connections.columns.interval') }}: {{ row.interval_minutes }}m</span>
          <span>
            {{ t('admin.upstreamRateSync.connections.columns.lastSyncAt') }}:
            {{ row.last_sync_at ? formatDate(row.last_sync_at) : t('admin.upstreamRateSync.connections.neverSynced') }}
          </span>
        </div>

        <!-- 错误提示 -->
        <p
          v-if="row.last_error"
          class="truncate rounded-lg bg-red-50 px-2 py-1 text-xs text-red-600 dark:bg-red-950/30 dark:text-red-400"
          :title="row.last_error"
        >
          {{ row.last_error }}
        </p>

        <!-- 操作行 -->
        <div class="mt-auto flex items-center justify-end gap-1 border-t border-gray-100 pt-3 dark:border-dark-700">
          <button type="button" class="btn btn-ghost btn-sm" @click="$emit('edit', row)">
            {{ t('common.edit') }}
          </button>
          <button
            type="button"
            class="btn btn-ghost btn-sm"
            :disabled="testingIds.includes(row.id)"
            :data-test="`test-${row.id}`"
            @click="$emit('test', row)"
          >
            {{ testingIds.includes(row.id) ? t('admin.upstreamRateSync.actions.testing') : t('admin.upstreamRateSync.actions.test') }}
          </button>
          <button
            type="button"
            class="btn btn-ghost btn-sm"
            :disabled="syncingIds.includes(row.id)"
            :data-test="`sync-${row.id}`"
            @click="$emit('sync', row)"
          >
            {{ syncingIds.includes(row.id) ? t('admin.upstreamRateSync.actions.syncing') : t('admin.upstreamRateSync.actions.syncNow') }}
          </button>
          <button
            type="button"
            class="btn btn-ghost btn-sm text-red-600 dark:text-red-400"
            :data-test="`delete-${row.id}`"
            @click="$emit('delete', row)"
          >
            {{ t('common.delete') }}
          </button>
        </div>
      </article>
    </div>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import type { UpstreamConnection, UpstreamSyncStatus } from '../types'

const { t, locale } = useI18n()

defineProps<{
  connections: UpstreamConnection[]
  loading: boolean
  testingIds: number[]
  syncingIds: number[]
}>()

defineEmits<{
  create: []
  refresh: []
  toggle: [connection: UpstreamConnection, value: boolean]
  edit: [connection: UpstreamConnection]
  test: [connection: UpstreamConnection]
  sync: [connection: UpstreamConnection]
  delete: [connection: UpstreamConnection]
}>()

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

function formatDate(value: string): string {
  return new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))
}
</script>
