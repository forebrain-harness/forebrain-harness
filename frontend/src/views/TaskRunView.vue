<template>
  <!-- One execution, read as the conversation it was (plan 153 A11): the same
       thread, the same activity line and cards, no input. The conversation
       itself is ChatView — this page only loads the run's facts and answers the
       three acts a viewer may ask for. -->
  <div class="flex h-full min-h-0 w-full flex-1 flex-col overflow-hidden">
    <div v-if="error" class="mx-4 mt-3 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-2 text-[12px] text-[var(--forebrain-danger)]" role="alert" data-testid="task-run-error">
      {{ error }}
    </div>
    <!-- The line carrying this run's frames is down. Saying so is the whole
         point: the thread keeps the last frame it drew, which looks exactly
         like a run that has stopped moving. A reconnect replays what was
         missed, so the only act offered is the one read a person can ask for
         while the line is still out. -->
    <div
      v-if="ready && liveState !== 'live'"
      class="mx-4 mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-2 text-[12px] text-[var(--forebrain-muted-text)]"
      role="status"
      data-testid="task-run-live"
    >
      <span>{{ liveState === 'offline' ? t('tasks.liveOffline') : t('tasks.livePaused') }}</span>
      <Button
        v-if="liveState === 'offline'"
        type="button"
        class="forebrain-btn forebrain-btn-ghost h-auto text-xs"
        data-testid="task-run-live-refresh"
        :disabled="refreshing"
        @click="refresh"
      >{{ t('tasks.liveRefresh') }}</Button>
    </div>
    <div v-if="!ready && loading" class="flex flex-1 items-center justify-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div
      v-else-if="!ready"
      class="flex flex-1 flex-col items-center justify-center gap-2 text-sm text-[var(--forebrain-muted-text)]"
      data-testid="task-run-not-found"
    >
      {{ t('tasks.runNotFound') }}
      <RouterLink class="text-[var(--forebrain-brand-1)] hover:underline" :to="taskPath">{{ t('tasks.backToTasks') }}</RouterLink>
    </div>
    <ChatView
      v-else
      :key="viewKey"
      class="min-h-0 flex-1"
      :task-run="facts"
      @batch-stop="stop"
      @batch-continue="continueRun"
      @batch-rerun="rerun"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * The page of one task execution (plan 153 A11). It is a conversation page with
 * a fixed task mode: the message thread is ChatView's own — the activity line,
 * the cards, the deliverables — and what differs is only what is missing (the
 * composer, the queue, the mode switch) and what the read-only bar offers.
 *
 * The page reads the run once: the task's history, which names the task, which
 * run of it this is and how it stood when the page was opened. Everything after
 * that is the engine's own reporting: an execution's state is published as it
 * moves (a `batch_execution` frame the conversation receives and keeps), so the
 * page is told the run started, ended, failed or was stopped instead of asking
 * the task every few seconds whether anything changed. The state belongs to the
 * engine; a page polling for it is the page guessing.
 */
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import ChatView from '@/views/ChatView.vue'
import { forebrainApi, getErrorMessage, type TaskExecutionRow, type TaskRecord } from '@/lib/api'
import { BatchStartRefusal, startBatchRun, stopBatchRun } from '@/lib/batchRun'
import { liveBatchExecution, sessionBindCount } from '@/lib/batchExecution'
import { liveStateFor } from '@/lib/liveConnection'
import { batchRefusalMessage, type BatchRunFacts } from '@/lib/tasks'
import { useI18n } from '@/locales'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()

const projectId = computed(() => String(route.params.id ?? ''))
const taskId = computed(() => String(route.params.taskId ?? ''))
const seq = computed(() => Number(route.params.seq ?? 0))

const HISTORY_PAGE = 10

const execution = ref<TaskExecutionRow | null>(null)
const task = ref<TaskRecord | null>(null)
const loading = ref(true)
const error = ref('')
const busy = ref(false)
/** The mounted conversation's identity: it changes when the address names
 *  another run, and the thread is rebuilt on that run's own history. */
const viewKey = ref('')
/** How many binds of this conversation's socket the page had at its own read:
 *  a later one is a reconnect, the one moment it may catch up by reading. */
let bindsAtLoad = 0
/** Whether the attach that follows this page's own read has happened yet: that
 *  attach is the read's own state replayed, so it owes no reading of its own. */
let attachPending = true

const taskPath = computed(() => `/projects/${encodeURIComponent(projectId.value)}/tasks/${encodeURIComponent(taskId.value)}`)

/** The state the engine last reported for this execution, if it has at all. */
const live = computed(() => liveBatchExecution(String(execution.value?.sessionId ?? '')))

/** Whether the conversation's own frames are arriving: the run's state and the
 *  answer still being written both travel on that socket. */
const liveState = computed(() => liveStateFor(String(execution.value?.sessionId ?? '')))
const refreshing = ref(false)

/** What the read-only view is told about this run. The status is the engine's
 *  last word on it — the frame when one has arrived, the read that opened the
 *  page otherwise. */
const facts = computed<BatchRunFacts | null>(() => (execution.value
  ? {
    projectId: projectId.value,
    taskId: taskId.value,
    title: task.value?.title || taskId.value,
    seq: execution.value.seq,
    status: live.value?.status || execution.value.status,
    model: '',
  }
  : null))

const ready = computed(() => Boolean(execution.value?.sessionId))

/** One execution out of a page of the history, by its number. */
function rowOf(rows: TaskExecutionRow[], want: number): TaskExecutionRow | null {
  return rows.find((row) => row.seq === want) ?? null
}

/**
 * Loads the run: the history's first page names the task and how many runs it
 * has, which is enough to work out which page this run is on — the rows are
 * newest first and their numbers are contiguous, so the page of run N is the
 * one holding position (total − N) of that order.
 */
async function load() {
  loading.value = true
  error.value = ''
  try {
    const first = await forebrainApi.getTask(taskId.value, { page: 1, pageSize: HISTORY_PAGE })
    task.value = first.task ?? null
    let found = rowOf(first.rows, seq.value)
    if (!found && first.total > first.rows.length) {
      const wantedPage = Math.max(1, Math.ceil((first.total - seq.value + 1) / HISTORY_PAGE))
      if (wantedPage > 1) {
        const page = await forebrainApi.getTask(taskId.value, { page: wantedPage, pageSize: HISTORY_PAGE })
        found = rowOf(page.rows, seq.value)
      }
    }
    execution.value = found
  } catch (cause) {
    error.value = getErrorMessage(cause)
    execution.value = null
  } finally {
    loading.value = false
  }
  if (execution.value) {
    // The conversation's identity is settled before anything awaits: the page
    // renders the thread the moment `ready` turns true, and a key filled in
    // after the address bar is written would mount a second conversation (and
    // have it read everything the first one had just read).
    viewKey.value = `${execution.value.sessionId}:${execution.value.seq}`
    bindsAtLoad = sessionBindCount(execution.value.sessionId)
    attachPending = true
    // The conversation is named by the address bar, as every conversation on
    // this surface is: ChatView reads the session from it.
    if (String(route.query.session ?? '') !== execution.value.sessionId) {
      await router.replace({ query: { ...route.query, session: execution.value.sessionId } })
    }
  }
}

/**
 * Re-reads the run's facts without remounting the thread.
 *
 * This is not a clock: an execution's state is the engine's, and the
 * conversation's frames carry it. Two things may ask for a read anyway. One is
 * a rebind of this conversation's observation socket: a reconnect is where
 * frames may have been missed, and a run whose snapshot carried no execution
 * frame (one opened before the engine published them) would otherwise have
 * nothing driving it. The other is the person, pressing the refresh button
 * while the socket is out — that read is `forced`, and it is the only one that
 * runs against a state the page already has. */
async function resyncFacts(forced = false) {
  if (live.value && !forced) return
  try {
    const first = await forebrainApi.getTask(taskId.value, { page: 1, pageSize: HISTORY_PAGE })
    task.value = first.task ?? null
    const current = execution.value
    if (!current) return
    const latest = first.rows[0] ?? null
    const fresh = latest && latest.seq === current.seq ? latest : rowOf(first.rows, current.seq) ?? current
    execution.value = { ...current, ...fresh }
  } catch {
    // A re-read that fails leaves the last known state on screen; the page's
    // own acts report their own failures.
  }
}

/**
 * The one read a person can ask for while the line is out. It is not a clock:
 * it happens once, when the button is pressed, and the state it brings is the
 * same read the page would have made on a reconnect. The socket is what keeps
 * the page current, and it says so above this button.
 */
async function refresh() {
  if (refreshing.value) return
  refreshing.value = true
  try {
    await resyncFacts(true)
  } finally {
    refreshing.value = false
  }
}

watch(
  () => sessionBindCount(String(execution.value?.sessionId ?? '')),
  (bound) => {
    if (bound <= bindsAtLoad) return
    bindsAtLoad = bound
    if (attachPending) {
      // The attach that follows this page's read: the snapshot it replays is
      // the state that read just brought.
      attachPending = false
      return
    }
    void resyncFacts()
  },
)

function sayFailure(cause: unknown) {
  error.value = cause instanceof BatchStartRefusal
    ? batchRefusalMessage(cause.code, cause.message)
    : getErrorMessage(cause)
}

/** Stopping asks the engine to end the run; it ends the execution and the
 *  frame it publishes carries the state the page then shows. */
async function stop() {
  if (busy.value || !execution.value) return
  busy.value = true
  error.value = ''
  try {
    // A stop that found no run to stop has nothing to end: this execution
    // already ended, on another surface, and its own frame said so.
    if (!(await stopBatchRun(execution.value.sessionId))) error.value = t('tasks.errNoRun')
  } catch (cause) {
    sayFailure(cause)
  } finally {
    busy.value = false
  }
}

/** Continuing resumes the same session on the input it was fixed with: the
 *  bar sends nothing, and the engine supplies the continue prompt. */
async function continueRun() {
  if (busy.value || !execution.value) return
  busy.value = true
  error.value = ''
  try {
    await startBatchRun(execution.value.sessionId)
  } catch (cause) {
    sayFailure(cause)
  } finally {
    busy.value = false
  }
}

/** Running it again is one more execution of the same task, so the page moves
 *  to that execution rather than replaying this one. */
async function rerun() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    const created = await forebrainApi.createTaskSession(projectId.value, { taskId: taskId.value })
    await startBatchRun(created.id)
    // The address names the new execution; the shell keys a page on its path,
    // so the page is rebuilt on that run and reads it from the start.
    await router.replace({
      name: 'project-task-run',
      params: { id: projectId.value, taskId: taskId.value, seq: String(created.execution.seq) },
      query: { session: created.id },
    })
  } catch (cause) {
    sayFailure(cause)
  } finally {
    busy.value = false
  }
}

onMounted(async () => {
  await load()
})
</script>
