<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6">
    <div class="mx-auto flex w-full max-w-4xl flex-col gap-4">
      <p v-if="error" class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <ListToolbar>
        <template #left>
          <input
            v-model="q"
            type="search"
            :placeholder="t('subagents.searchPlaceholder')"
            class="forebrain-field h-[34px]"
            style="max-width: 240px"
            data-testid="subagents-search"
          />
        </template>
        <template #actions>
          <button
            type="button"
            class="forebrain-btn forebrain-btn-ghost text-xs text-[var(--forebrain-danger)]"
            :disabled="!total || cancellingAll"
            data-testid="subagents-cancel-all"
            @click="cancelAll"
          >
            {{ t('subagents.cancelAll') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" :disabled="loading" @click="refresh">
            <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
          </button>
        </template>
      </ListToolbar>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-6 py-12 text-center">
        <Users class="mx-auto size-10 text-[var(--forebrain-muted-text)]" aria-hidden="true" />
        <p class="mt-3 text-sm text-[var(--forebrain-muted-text)]">{{ t('subagents.empty') }}</p>
      </div>
      <div v-else class="forebrain-table-wrap">
        <table class="forebrain-table forebrain-table--fixed">
          <colgroup>
            <col style="width: 140px" />
            <col />
            <col style="width: 200px" />
            <col style="width: 96px" />
            <col style="width: 90px" />
            <col style="width: 76px" />
          </colgroup>
          <thead>
            <tr>
              <th>{{ t('subagents.colAgent') }}</th>
              <th>{{ t('subagents.colTask') }}</th>
              <th>{{ t('subagents.colSession') }}</th>
              <th>{{ t('subagents.status') }}</th>
              <th>{{ t('subagents.startedAt') }}</th>
              <th class="text-right">{{ t('subagents.colActions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in rows" :key="row.id" :data-subagent-row="row.id">
              <td class="font-mono">{{ row.label || row.id }}</td>
              <td>
                <span class="forebrain-cell-1" v-tip:top.trunc="row.title || ''">{{ row.title }}</span>
              </td>
              <td>
                <!-- 所在会话 is the dialogue the subagent ran in — its parent
                     conversation, which is the one an operator recognises. The
                     action column keeps the way into the subagent's own session. -->
                <button
                  v-if="conversationTarget(row)"
                  type="button"
                  class="max-w-full truncate text-[var(--forebrain-brand-1)] hover:underline"
                  :data-testid="`subagent-session-${row.id}`"
                  @click="jumpToConversation(row)"
                >
                  {{ row.sessionTitle || row.parentSessionId || row.sessionId }}
                </button>
                <span v-else class="text-[var(--forebrain-muted-text)]">—</span>
              </td>
              <td><BadgeComponent variant="gray">{{ statusLabel(row.status) }}</BadgeComponent></td>
              <td class="font-mono text-[var(--forebrain-text-2)]">{{ elapsedLabel(row) }}</td>
              <td class="text-right">
                <RowActions>
                  <RowActionSlot v-if="!sessionTarget(row)" />
                  <RowAction
                    v-else
                    :icon="MessageSquare"
                    :label="t('subagents.openSession')"
                    :name="row.label || row.id"
                    :data-testid="`subagent-open-${row.id}`"
                    @click="jumpToSession(row)"
                  />
                  <RowAction
                    :icon="CircleStop"
                    :label="t('subagents.stop')"
                    :name="row.label || row.id"
                    tone="danger"
                    :disabled="cancelling.has(row.id)"
                    :data-testid="`subagent-stop-${row.id}`"
                    @click="cancelOne(row)"
                  />
                </RowActions>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      <ListNote>{{ t('subagents.note') }}</ListNote>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { CircleStop, MessageSquare, RotateCw, Users } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import RowActionSlot from '@/components/common/RowActionSlot.vue'
import TablePager from '@/components/common/TablePager.vue'
import { useServerList } from '@/composables/useServerList'
import { agentRosterConversationTarget, agentRosterViewTarget } from '@/composables/useAgentRoster'
import { forebrainApi, getErrorMessage, type AgentRosterRow } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The primary agent's running subagents, across all of its sessions. 所在会话
 * links back into the conversation a row ran in — a subagent's parent, the one
 * an operator recognises — while the action column opens the subagent's own
 * session; stopping a row stops the run, exactly as the workbench's roster
 * does.
 *
 * The list is the server's (plan 073): `running=1` is the page's filter, the
 * longest-running row comes first there, and each row names its session. The
 * page keeps the clock moving between loads, so 已运行 counts on instead of
 * freezing at the second the answer was built.
 */
const { t } = useI18n()
const router = useRouter()

const {
  rows,
  total,
  page,
  pageSize,
  q,
  loading,
  error: listError,
  reload,
} = useServerList<AgentRosterRow>(forebrainApi.agentRosterPage, { pageSize: 10, filters: { running: 1 } })

const actionError = ref('')
const error = computed(() => actionError.value || listError.value)

const cancelling = ref(new Set<string>())
const cancellingAll = ref(false)

/** When the answer on screen was built, and the second it is now. */
const loadedAt = ref(Date.now())
const now = ref(Date.now())

let refreshTimer: ReturnType<typeof setInterval> | null = null
let clockTimer: ReturnType<typeof setInterval> | null = null

/**
 * 已运行 as mm:ss: the server reports how long a row has run when it answers,
 * so the row's start is that answer's moment minus the elapsed time.
 */
function elapsedLabel(row: AgentRosterRow): string {
  const start = loadedAt.value - Math.max(0, row.elapsedSeconds ?? 0) * 1000
  const seconds = Math.max(0, Math.floor((now.value - start) / 1000))
  return `${String(Math.floor(seconds / 60)).padStart(2, '0')}:${String(seconds % 60).padStart(2, '0')}`
}

/** What a row is doing, in the viewer's language; an unknown status as-is. */
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
      return String(status ?? '')
  }
}

function sessionTarget(row: AgentRosterRow) {
  return agentRosterViewTarget(row)
}

function conversationTarget(row: AgentRosterRow) {
  return agentRosterConversationTarget(row)
}

async function refresh() {
  await reload()
  loadedAt.value = Date.now()
  now.value = loadedAt.value
}

function jumpToSession(row: AgentRosterRow) {
  const target = sessionTarget(row)
  if (!target) return
  void router.push(target)
}

function jumpToConversation(row: AgentRosterRow) {
  const target = conversationTarget(row)
  if (!target) return
  void router.push(target)
}

async function cancelOne(row: AgentRosterRow) {
  cancelling.value.add(row.id)
  actionError.value = ''
  try {
    if (row.kind === 'primary') await forebrainApi.primaryAgentCancel(row.id)
    else await forebrainApi.subagentCancel(row.id)
    await refresh()
  } catch (cause) {
    actionError.value = getErrorMessage(cause)
  } finally {
    cancelling.value.delete(row.id)
  }
}

async function cancelAll() {
  cancellingAll.value = true
  actionError.value = ''
  try {
    await forebrainApi.agentsCancelAll()
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
  clockTimer = setInterval(() => {
    now.value = Date.now()
  }, 1000)
})

onUnmounted(() => {
  if (refreshTimer) clearInterval(refreshTimer)
  if (clockTimer) clearInterval(clockTimer)
})
</script>
