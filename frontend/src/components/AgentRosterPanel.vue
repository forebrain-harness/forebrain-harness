<template>
  <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-glass)] p-3 shadow-[var(--forebrain-doc-shadow)]">
    <div class="mb-3 flex items-center justify-between gap-3">
      <div class="min-w-0">
        <div class="truncate text-sm font-semibold text-[var(--forebrain-text)]">{{ title }}</div>
        <div class="text-[11px] text-[var(--forebrain-muted-text)]">{{ agentCountLabel }}</div>
      </div>
      <button
        v-if="hasRunningRows"
        class="forebrain-btn forebrain-btn-ghost h-8 shrink-0 px-2 text-xs"
        type="button"
        @click="$emit('cancel-all')"
      >
        {{ t('agents.stopAll') }}
      </button>
    </div>
    <div v-if="loading" class="rounded-xl border border-dashed border-[var(--forebrain-divider)] px-3 py-5 text-center text-xs text-[var(--forebrain-muted-text)]">
      {{ t('common.loading') }}
    </div>
    <div v-else-if="error" class="rounded-xl border border-[rgba(160,70,70,0.36)] bg-[rgba(160,70,70,0.08)] px-3 py-3 text-xs text-[var(--forebrain-danger)]">
      {{ error }}
    </div>
    <div v-else-if="!records.length" class="rounded-xl border border-dashed border-[var(--forebrain-divider)] px-3 py-5 text-center text-xs text-[var(--forebrain-muted-text)]">
      {{ t('agents.noLiveAgents') }}
    </div>
    <ul v-else class="space-y-2">
      <li
        v-for="row in records"
        :key="`${row.kind}-${row.id}-${row.sessionId || row.runId || ''}`"
        class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] px-3 py-2"
      >
        <button
          class="w-full text-left disabled:cursor-default"
          type="button"
          :disabled="!row.sessionId"
          @click="$emit('view', row)"
        >
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="flex min-w-0 items-center gap-2">
                <span class="truncate text-sm font-medium text-[var(--forebrain-text)]">{{ row.label }}</span>
                <span class="shrink-0 rounded-full border border-[var(--forebrain-divider)] px-1.5 py-0.5 text-[9px] uppercase text-[var(--forebrain-muted-text)]">
                  {{ kindLabel(row.kind) }}
                </span>
              </div>
              <div class="mt-1 truncate text-[11px] text-[var(--forebrain-muted-text)]">{{ row.title || row.task || row.sessionId || row.runId || row.kind }}</div>
            </div>
            <span
              class="shrink-0 rounded-full border px-2 py-0.5 text-[10px] uppercase tracking-wide"
              :class="statusClass(row.status)"
            >
              {{ statusLabel(row.status) }}
            </span>
          </div>
        </button>
        <div class="mt-2 flex items-center justify-between gap-2 text-[11px] text-[var(--forebrain-muted-text)]">
          <span class="min-w-0 truncate">{{ metricsLabel(row) }}</span>
          <button
            v-if="isRunning(row)"
            class="forebrain-btn forebrain-btn-ghost h-7 shrink-0 px-2 text-xs"
            type="button"
            @click="$emit('cancel', row)"
          >
            {{ t('agents.stop') }}
          </button>
        </div>
      </li>
    </ul>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { AgentRosterRow } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  title: string
  records: readonly AgentRosterRow[]
  loading?: boolean
  error?: string
}>(), {
  loading: false,
  error: '',
})

defineEmits<{
  view: [row: AgentRosterRow]
  cancel: [row: AgentRosterRow]
  'cancel-all': []
}>()

const { t } = useI18n()

const agentCountLabel = computed(() => (props.records.length === 1 ? t('agents.countOne') : t('agents.count', { count: props.records.length })))
const hasRunningRows = computed(() => props.records.some((row) => isRunning(row)))

function isRunning(row: AgentRosterRow): boolean {
  return String(row.status ?? '').toLowerCase() === 'running'
}

/** What an agent is doing, in words: working, idle, or how it ended. */
function statusLabel(status: string): string {
  switch (String(status ?? '').toLowerCase()) {
    case 'running':
      return t('agents.status.running')
    case 'idle':
      return t('agents.status.idle')
    case 'available':
    case 'done':
    case 'completed':
      return t('agents.status.done')
    case 'failed':
      return t('agents.status.failed')
    case 'cancelled':
    case 'canceled':
      return t('agents.status.stopped')
    default:
      return String(status ?? '').trim() || t('common.none')
  }
}

function kindLabel(kind: AgentRosterRow['kind']): string {
  return kind === 'primary' ? t('agents.primary') : t('agents.subagent')
}

function metricsLabel(row: AgentRosterRow): string {
  return t('agents.metrics', {
    seconds: Math.max(0, Math.round(row.elapsedSeconds ?? 0)),
    tools: Math.max(0, row.toolCount ?? 0),
    files: Math.max(0, row.fileCount ?? 0),
  })
}

function statusClass(status: string): string {
  const normalized = String(status ?? '').toLowerCase()
  if (normalized === 'running') return 'border-[rgba(13,145,176,0.35)] bg-[rgba(13,145,176,0.10)] text-[var(--forebrain-brand-1)]'
  if (normalized === 'failed') return 'border-[rgba(160,70,70,0.36)] bg-[rgba(160,70,70,0.08)] text-[var(--forebrain-danger)]'
  if (normalized === 'cancelled' || normalized === 'canceled') return 'border-[var(--forebrain-divider)] bg-[var(--forebrain-button-alt-bg)] text-[var(--forebrain-muted-text)]'
  return 'border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] text-[var(--forebrain-text-2)]'
}
</script>
