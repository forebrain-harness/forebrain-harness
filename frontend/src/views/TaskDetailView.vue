<template>
  <!-- The task's own page (plan 153 A11): its facts, the operations this
       moment allows, and its execution history. The history is an index — the
       process lives one level down, in the execution's own read-only
       conversation. Nothing about the history belongs in a workbench. -->
  <div class="mx-auto w-full max-w-[1180px] px-4 pb-10 pt-5 sm:px-6">
    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]" role="alert" data-testid="task-error">{{ error }}</p>

    <div v-if="!task && historyLoading" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div
      v-else-if="!task"
      class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]"
      data-testid="task-not-found"
    >{{ t('tasks.notFound') }}</div>

    <template v-else>
      <div class="mb-3 flex flex-wrap items-center gap-x-[18px] gap-y-2" data-testid="task-meta">
        <div class="flex flex-wrap items-baseline gap-x-3.5 gap-y-1 text-[12px] text-[var(--forebrain-muted-text)]">
          <span>{{ taskSourceLabel(task.origin, task.originRef) }}</span>
          <span v-if="task.externalRef">
            {{ t('tasks.externalRefLabel') }}
            <span class="font-mono text-[var(--forebrain-text-2)]">{{ task.externalRef }}</span>
          </span>
        </div>
        <div class="ml-auto flex items-center gap-2">
          <Button
            v-if="latest?.status === 'running'"
            type="button"
            class="forebrain-btn forebrain-btn--danger h-auto bg-transparent text-xs hover:bg-transparent"
            data-testid="task-stop"
            :disabled="busy"
            @click="stop"
          >
            <Square class="size-3.5" aria-hidden="true" />{{ t('tasks.stopRun') }}
          </Button>
          <Button
            v-if="latest?.status === 'failed' || latest?.status === 'stopped'"
            type="button"
            class="forebrain-btn forebrain-btn-primary h-auto gap-1.5 text-xs"
            data-testid="task-rerun"
            :disabled="busy"
            @click="rerun"
          >
            <RotateCw class="size-3.5" aria-hidden="true" />{{ t('tasks.rerun') }}
          </Button>
          <Button
            type="button"
            class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs"
            data-testid="task-new-run"
            :disabled="busy"
            @click="newExecution"
          >
            <Plus class="size-3.5" aria-hidden="true" />{{ t('tasks.newExecution') }}
          </Button>
        </div>
      </div>

      <div class="mb-[22px] grid grid-cols-2 gap-[18px] rounded-[10px] border border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] px-4 py-[13px] md:grid-cols-4" data-testid="task-facts">
        <div class="grid min-w-0 gap-[3px]">
          <span class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('tasks.factRuns') }}</span>
          <b class="truncate text-[14px] font-semibold text-[var(--forebrain-text)]">{{ total }}</b>
        </div>
        <div class="grid min-w-0 gap-[3px]">
          <span class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('tasks.factDone') }}</span>
          <b class="truncate text-[14px] font-semibold text-[var(--forebrain-text)]">{{ completedRuns }}</b>
        </div>
        <div class="grid min-w-0 gap-[3px]">
          <span class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('tasks.factLatest') }}</span>
          <b class="truncate text-[14px] font-semibold text-[var(--forebrain-text)]">{{ latest ? taskStatusLabel(latest.status) : '—' }}</b>
        </div>
        <div class="grid min-w-0 gap-[3px]">
          <span class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('tasks.factWorkspace') }}</span>
          <b class="truncate font-mono text-[14px] font-semibold text-[var(--forebrain-text)]">{{ latest?.workspaceRel || `tasks/${task.id}/` }}</b>
        </div>
      </div>

      <section aria-label="task-history">
        <div class="mb-2.5 grid gap-[3px]">
          <div class="flex items-baseline gap-2.5">
            <b class="text-[13px] text-[var(--forebrain-text)]">{{ t('tasks.historyTitle') }}</b>
            <span class="text-[11.5px] text-[var(--forebrain-muted-text)]">{{ t('tasks.historyCount', { n: total }) }}</span>
          </div>
          <p class="m-0 text-[11.5px] text-[var(--forebrain-muted-text)]">{{ t('tasks.historyHint') }}</p>
        </div>

        <div v-if="historyLoading && !rows.length" class="py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
        <DataTable
          v-else-if="rows.length"
          :columns="columns"
          :rows="rows"
          :row-key="(row) => String(row.seq)"
          :row-attrs="(row) => ({ 'data-task-run': String(row.seq) })"
        >
          <template #cell-seq="{ row }">
            <RouterLink
              :to="runPath(row)"
              class="font-mono text-[12px] font-semibold text-[var(--forebrain-brand-1)] hover:underline"
              :data-testid="`task-run-open-${row.seq}`"
            >{{ t('tasks.runN', { n: row.seq }) }}</RouterLink>
          </template>
          <template #cell-status="{ row }">
            <BadgeComponent :variant="taskStatusVariant(row.status)">{{ runStatusLabel(row.status) }}</BadgeComponent>
          </template>
          <template #cell-started="{ row }">{{ formatDateTime(row.startedAt) }}</template>
          <template #cell-duration="{ row }">{{ formatRunDuration(row.startedAt, row.finishedAt) }}</template>
          <template #cell-artifacts="{ row }">{{ row.artifacts }}</template>
          <template #cell-result="{ row }">
            <span
              class="forebrain-cell-1"
              :class="row.errorText ? 'text-[var(--forebrain-danger)]' : 'text-[var(--forebrain-muted-text)]'"
              v-tip:top.trunc="row.errorText || runOutcomeLabel(row.status)"
            >{{ row.errorText || runOutcomeLabel(row.status) }}</span>
          </template>
          <template #cell-actions="{ row }">
            <RowActions>
              <RowAction
                :icon="Eye"
                :label="t('tasks.viewRun')"
                :name="t('tasks.runN', { n: row.seq })"
                :data-testid="`task-run-view-${row.seq}`"
                @click="openRun(row)"
              />
              <RowAction
                v-if="row.status === 'running'"
                :icon="Square"
                tone="danger"
                :label="t('tasks.stopRun')"
                :name="t('tasks.runN', { n: row.seq })"
                :disabled="busy"
                :data-testid="`task-run-stop-${row.seq}`"
                @click="stop"
              />
              <RowAction
                v-if="row.status === 'stopped' || row.status === 'failed'"
                :icon="RotateCw"
                :label="t('tasks.rerun')"
                :name="t('tasks.runN', { n: row.seq })"
                :disabled="busy"
                :data-testid="`task-run-rerun-${row.seq}`"
                @click="rerun"
              />
              <RowActionSlot />
            </RowActions>
          </template>
        </DataTable>
        <div v-else class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]" data-testid="task-history-empty">
          {{ t('tasks.emptyTitle') }}
        </div>

        <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * One task and its execution history (plan 153 A11). The page names itself in
 * the shell's top bar — 任务 › 任务名, the task's id, its newest status — and
 * carries only what a task can be asked: run it again, stop the run in flight,
 * open one more execution.
 *
 * A batch execution is read in its own conversation, never inline here, so a
 * row opens that page instead of expanding.
 */
import { computed, h, onMounted, ref, watch, withDirectives } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import { ChevronRight, Eye, Plus, RotateCw, Square } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import RowActionSlot from '@/components/common/RowActionSlot.vue'
import TablePager from '@/components/common/TablePager.vue'
import { useServerList } from '@/composables/useServerList'
import { useTopbar } from '@/composables/useTopbar'
import tip from '@/directives/tip'
import { forebrainApi, getErrorMessage, type TaskExecutionRow, type TaskRecord } from '@/lib/api'
import { BatchStartRefusal, startBatchRun, stopBatchRun } from '@/lib/batchRun'
import {
  batchRefusalMessage, formatRunDuration, runOutcomeLabel, runStatusLabel, taskSourceLabel,
  taskStatusLabel, taskStatusVariant,
} from '@/lib/tasks'
import { formatDateTime } from '@/lib/time'
import { useI18n } from '@/locales'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()

const projectId = computed(() => String(route.params.id ?? ''))
const taskId = computed(() => String(route.params.taskId ?? ''))

const task = ref<TaskRecord | null>(null)
const writeError = ref('')
const busy = ref(false)

/** The history is the server's page; the facts above it count the same rows. */
const { rows, total, page, pageSize, loading: historyLoading, error: historyError, reload } = useServerList<TaskExecutionRow>(
  async (query) => {
    const answer = await forebrainApi.getTask(taskId.value, query)
    task.value = answer.task ?? null
    return answer
  },
  { pageSize: 10 },
)

const error = computed(() => writeError.value || historyError.value)
const latest = computed(() => rows.value[0] ?? null)
const completedRuns = computed(() => rows.value.filter((row) => row.status === 'completed').length)

const columns: DataColumn[] = [
  { id: 'seq', header: t('tasks.colExecution'), size: 92 },
  { id: 'status', header: t('tasks.colStatus'), size: 104 },
  { id: 'started', header: t('tasks.colStarted'), size: 140, cellClass: 'forebrain-num font-mono text-[var(--forebrain-text-2)]' },
  { id: 'duration', header: t('tasks.colDuration'), size: 96, cellClass: 'forebrain-num font-mono text-[var(--forebrain-text-2)]' },
  { id: 'artifacts', header: t('tasks.colArtifacts'), size: 88, headerClass: 'text-right', cellClass: 'forebrain-num text-right' },
  { id: 'result', header: t('tasks.colResult') },
  { id: 'actions', header: t('tasks.colExecutionActions'), size: 120, headerClass: 'text-center', cellClass: 'text-right' },
]

function runPath(row: TaskExecutionRow) {
  return {
    name: 'project-task-run',
    params: { id: projectId.value, taskId: taskId.value, seq: String(row.seq) },
    query: { session: row.sessionId },
  }
}

function openRun(row: TaskExecutionRow) {
  void router.push(runPath(row))
}

function sayFailure(cause: unknown) {
  writeError.value = cause instanceof BatchStartRefusal
    ? batchRefusalMessage(cause.code, cause.message)
    : getErrorMessage(cause)
}

/** Stopping is stopping the run in flight; the roster says which one it is. */
async function stop() {
  if (busy.value || !latest.value) return
  busy.value = true
  writeError.value = ''
  try {
    if (!await stopBatchRun(latest.value.sessionId)) writeError.value = t('tasks.errNoRun')
    await reload()
  } catch (cause) {
    sayFailure(cause)
  } finally {
    busy.value = false
  }
}

/** Running it again opens one more execution of this task. */
async function rerun() {
  if (busy.value) return
  busy.value = true
  writeError.value = ''
  try {
    const created = await forebrainApi.createTaskSession(projectId.value, { taskId: taskId.value })
    await startBatchRun(created.id)
    void router.push({
      name: 'project-task-run',
      params: { id: projectId.value, taskId: taskId.value, seq: String(created.execution.seq) },
      query: { session: created.id },
    })
  } catch (cause) {
    sayFailure(cause)
    await reload()
  } finally {
    busy.value = false
  }
}

/** 新建执行 is the same act as 重新执行: the new row is the difference. */
async function newExecution() {
  await rerun()
}

/**
 * The page's row in the shell's top bar (plan 153): 任务 › 任务名, the task's
 * id and the state its newest execution is in — the same three facts the
 * prototype puts there. Everything is a factory so the bar follows a reload.
 */
const { set: setTopbar } = useTopbar()

setTopbar({
  leading: () => {
    const name = task.value?.title || taskId.value
    return [
      h('div', { class: 'forebrain-topbar-crumb' }, [
        h(RouterLink, {
          to: `/projects/${encodeURIComponent(projectId.value)}/tasks`,
          class: 'font-medium text-[var(--forebrain-brand-1)] hover:underline',
          'data-testid': 'task-crumb-back',
        }, { default: () => t('tasks.backToTasks') }),
        h(ChevronRight, { class: 'size-3.5 text-[var(--forebrain-muted-text)]', 'aria-hidden': 'true' }),
        withDirectives(
          h('span', { class: 'forebrain-topbar-name', 'data-testid': 'task-crumb-name' }, name),
          [[tip, name, 'bottom', { trunc: true }]],
        ),
      ]),
      h('span', { class: 'forebrain-topbar-meta font-mono', 'data-testid': 'task-crumb-id' }, taskId.value),
      ...(latest.value
        ? [h(BadgeComponent, {
          variant: taskStatusVariant(latest.value.status),
          'data-testid': 'task-crumb-status',
        }, { default: () => taskStatusLabel(latest.value.status) })]
        : []),
    ]
  },
})

onMounted(() => {
  void reload()
})
watch(taskId, () => {
  task.value = null
  void reload()
})
</script>
