<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6">
    <div class="mx-auto w-full max-w-4xl">
      <PageTopRow :title="t('subagents.title')" :description="t('subagents.description')">
        <template #actions>
          <button
            type="button"
            class="forebrain-btn forebrain-btn-ghost text-xs text-[var(--forebrain-danger)]"
            :disabled="!runningRows.length || cancellingAll"
            @click="cancelAll"
          >
            {{ cancellingAll ? t('common.loading') : t('subagents.cancelAll') }}
          </button>
        </template>
      </PageTopRow>

      <p v-if="error" class="mt-4 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error || actionError }}</p>

      <div v-if="loading" class="mt-6 text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!runningRows.length" class="mt-6 rounded-lg border border-dashed border-[var(--forebrain-divider)] px-6 py-12 text-center">
        <Users class="mx-auto size-10 text-[var(--forebrain-muted-text)]" aria-hidden="true" />
        <p class="mt-3 text-sm text-[var(--forebrain-muted-text)]">{{ t('subagents.empty') }}</p>
      </div>

      <div v-else class="forebrain-table-wrap mt-6">
        <table class="forebrain-table">
          <thead>
            <tr>
              <th>{{ t('subagents.agent') }}</th>
              <th>{{ t('subagents.session') }}</th>
              <th>{{ t('subagents.status') }}</th>
              <th>{{ t('subagents.startedAt') }}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in runningRows" :key="row.id">
              <td>
                <div class="font-medium">{{ row.label || row.id }}</div>
                <div v-if="row.title" class="mt-0.5 max-w-[280px] truncate text-[11px] text-[var(--forebrain-muted-text)]" :title="row.title">{{ row.title }}</div>
              </td>
              <td class="font-mono text-[11px] text-[var(--forebrain-text-2)]">{{ row.sessionId || '—' }}</td>
              <td>
                <span class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[11px] text-[var(--forebrain-text-2)]">{{ row.status }}</span>
              </td>
              <td class="text-[12px] text-[var(--forebrain-text-2)]">{{ formatStarted(row) }}</td>
              <td class="text-right">
                <button v-if="row.sessionId" type="button" class="forebrain-btn forebrain-btn-ghost mr-2 text-xs" @click="jumpToSession(row)">{{ t('subagents.openSession') }}</button>
                <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs text-[var(--forebrain-danger)]" :disabled="cancelling.has(row.id)" @click="cancelOne(row)">
                  {{ cancelling.has(row.id) ? t('common.loading') : t('subagents.stop') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Users } from 'lucide-vue-next'
import PageTopRow from '@/components/common/PageTopRow.vue'
import { agentRosterViewTarget, useAgentRoster } from '@/composables/useAgentRoster'
import { getErrorMessage, type AgentRosterRow } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The primary agent's running subagents, across all of its sessions. Rows
 * link back into the conversation that owns them; stopping a row stops the
 * run, exactly as the workbench's roster does.
 */
const { t } = useI18n()
const router = useRouter()
const { records, loading, error, loadRoster, cancelRoster, cancelAll: cancelAllRoster } = useAgentRoster()
const actionError = ref('')

const cancelling = ref(new Set<string>())
const cancellingAll = ref(false)
let refreshTimer: ReturnType<typeof setInterval> | null = null

const runningRows = computed(() => records.value.filter((row) => String(row.status ?? '').toLowerCase() === 'running'))

function formatStarted(row: AgentRosterRow): string {
  if (!row.elapsedSeconds && row.elapsedSeconds !== 0) return '—'
  return t('subagents.elapsed', { seconds: row.elapsedSeconds })
}

async function refresh() {
  try {
    await loadRoster()
  } catch {
    // loadRoster records the failure on the shared store; the page shows it.
  }
}

function jumpToSession(row: AgentRosterRow) {
  const target = agentRosterViewTarget(row)
  if (!target) return
  void router.push(target)
}

async function cancelOne(row: AgentRosterRow) {
  cancelling.value.add(row.id)
  try {
    await cancelRoster(row)
    await refresh()
  } catch (cause) {
    actionError.value = getErrorMessage(cause)
  } finally {
    cancelling.value.delete(row.id)
  }
}

async function cancelAll() {
  cancellingAll.value = true
  try {
    await cancelAllRoster()
    await refresh()
  } catch (cause) {
    actionError.value = getErrorMessage(cause)
  } finally {
    cancellingAll.value = false
  }
}

onMounted(() => {
  void refresh()
  refreshTimer = setInterval(() => void refresh(), 5000)
})

onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
})
</script>
