<template>
  <div class="mx-auto max-w-4xl space-y-4">
    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]" role="alert" data-testid="tasks-error">{{ error }}</p>

    <ListToolbar>
      <template #left>
        <Input
          v-model="q"
          type="search"
          :placeholder="t('tasks.searchPlaceholder')"
          class="forebrain-field h-[34px] w-auto"
          style="max-width: 240px"
          data-testid="tasks-search"
        />
        <Select :model-value="statusChoice" @update:model-value="setStatus">
          <SelectTrigger class="forebrain-field h-[34px] w-[128px]" :aria-label="t('tasks.colStatus')" data-testid="tasks-status">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="option in statusOptions" :key="option.value || 'all'" :value="option.value || ALL_STATUSES">
              {{ option.label }}
            </SelectItem>
          </SelectContent>
        </Select>
      </template>
      <template #actions>
        <Button
          type="button"
          class="forebrain-btn forebrain-btn-primary h-auto gap-1.5 text-xs"
          data-testid="tasks-new"
          @click="newOpen = true"
        >
          <Plus class="size-3.5" aria-hidden="true" />{{ t('tasks.newTask') }}
        </Button>
      </template>
    </ListToolbar>

    <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <DataTable
      v-else-if="rows.length"
      :columns="columns"
      :rows="rows"
      :row-key="(row) => row.id"
      :row-attrs="(row) => ({ 'data-task-row': row.id })"
    >
      <template #cell-title="{ row }">
        <RouterLink
          :to="taskPath(row)"
          class="forebrain-cell-1 block font-medium text-[var(--forebrain-text)] hover:text-[var(--forebrain-brand-1)]"
          v-tip:top.trunc="row.title || row.id"
          :data-testid="`task-open-${row.id}`"
        >{{ row.title || row.id }}</RouterLink>
        <span class="forebrain-cell-1 block font-mono text-[11px] text-[var(--forebrain-muted-text)]">{{ row.id }}</span>
      </template>
      <template #cell-source="{ row }">
        <span class="forebrain-cell-1 text-[var(--forebrain-text-2)]" v-tip:top.trunc="taskSourceLabel(row.origin, row.originRef)">
          {{ taskSourceLabel(row.origin, row.originRef) }}
        </span>
      </template>
      <template #cell-status="{ row }">
        <BadgeComponent :variant="taskStatusVariant(row.latest.status)" :data-testid="`task-status-${row.id}`">
          {{ taskStatusLabel(row.latest.status) }}
        </BadgeComponent>
      </template>
      <template #cell-executions="{ row }">{{ row.executions }}</template>
      <template #cell-artifacts="{ row }">{{ row.artifacts }}</template>
      <template #cell-updated="{ row }">{{ formatDateTime(row.updatedAt) }}</template>
      <template #cell-actions="{ row }">
        <RowActions>
          <RowAction
            :icon="Eye"
            :label="t('tasks.view')"
            :name="row.title || row.id"
            :data-testid="`task-view-${row.id}`"
            @click="openTask(row)"
          />
          <RowAction
            v-if="row.latest.status === 'stopped'"
            :icon="Play"
            :label="t('tasks.continue')"
            :name="row.title || row.id"
            :disabled="busyId === row.id"
            :data-testid="`task-continue-${row.id}`"
            @click="continueRun(row)"
          />
          <RowAction
            v-if="row.latest.status === 'stopped' || row.latest.status === 'failed'"
            :icon="RotateCw"
            :label="t('tasks.rerun')"
            :name="row.title || row.id"
            :disabled="busyId === row.id"
            :data-testid="`task-rerun-${row.id}`"
            @click="rerun(row)"
          />
          <RowActionSlot />
          <RowActionSlot />
        </RowActions>
      </template>
    </DataTable>
    <div v-else class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center" data-testid="tasks-empty">
      <b class="block text-sm text-[var(--forebrain-text-2)]">{{ t('tasks.emptyTitle') }}</b>
      <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('tasks.emptyHint') }}</span>
    </div>

    <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
    <ListNote>{{ t('tasks.listNote') }}</ListNote>

    <TaskNewDialog
      v-if="newOpen"
      :project-id="projectId"
      @close="closeNew"
      @created="openCreated"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * A batch project's tasks (plan 153 A10): one row per task, the execution it
 * ended on, and only the operations this moment allows — open it, continue a
 * stopped execution, or run a failed or stopped one again. A task's process is
 * not drawn here: each execution is its own conversation, and its page is the
 * only place it is read.
 *
 * The table is the server's: the search reads the title, the external reference
 * and the id, and the status filter keeps tasks whose newest execution holds it.
 */
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Eye, Play, Plus, RotateCw } from 'lucide-vue-next'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import RowActionSlot from '@/components/common/RowActionSlot.vue'
import TablePager from '@/components/common/TablePager.vue'
import TaskNewDialog from '@/components/project/TaskNewDialog.vue'
import { useServerList } from '@/composables/useServerList'
import { forebrainApi, getErrorMessage, type ProjectTaskRow } from '@/lib/api'
import { BatchStartRefusal, startBatchRun } from '@/lib/batchRun'
import { batchRefusalMessage, taskSourceLabel, taskStatusFilterOptions, taskStatusLabel, taskStatusVariant } from '@/lib/tasks'
import { formatDateTime } from '@/lib/time'
import { useI18n } from '@/locales'

const props = defineProps<{ project: unknown; projectId: string }>()

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

/** A Select may not carry an empty string, so "no filter" travels as this. */
const ALL_STATUSES = '__all__'

const { rows, total, page, pageSize, q, filters, loading, error: listError, reload } = useServerList<ProjectTaskRow>(
  (query) => forebrainApi.listProjectTasks(props.projectId, query),
  { pageSize: 10, filters: { status: '' } },
)

const writeError = ref('')
const error = computed(() => listError.value || writeError.value)
const busyId = ref('')
const newOpen = ref(false)

const statusOptions = computed(() => taskStatusFilterOptions())
const statusChoice = computed(() => String(filters.value.status ?? '') || ALL_STATUSES)

function setStatus(value: unknown) {
  const next = String(value ?? '')
  filters.value = { ...filters.value, status: next === ALL_STATUSES ? '' : next }
}

const columns: DataColumn[] = [
  { id: 'title', header: t('tasks.colTask') },
  { id: 'source', header: t('tasks.colSource'), size: 180 },
  { id: 'status', header: t('tasks.colStatus'), size: 104 },
  { id: 'executions', header: t('tasks.colExecutions'), size: 88, headerClass: 'text-right', cellClass: 'forebrain-num text-right' },
  { id: 'artifacts', header: t('tasks.colArtifacts'), size: 80, headerClass: 'text-right', cellClass: 'forebrain-num text-right' },
  { id: 'updated', header: t('tasks.colUpdated'), size: 150, cellClass: 'forebrain-num font-mono text-[var(--forebrain-text-2)]' },
  { id: 'actions', header: t('projects.colActions'), size: 128, headerClass: 'text-center', cellClass: 'text-right' },
]

function taskPath(row: ProjectTaskRow) {
  return { name: 'project-task', params: { id: props.projectId, taskId: row.id } }
}

function runPath(taskId: string, seq: number, sessionId: string) {
  return { name: 'project-task-run', params: { id: props.projectId, taskId, seq: String(seq) }, query: { session: sessionId } }
}

function openTask(row: ProjectTaskRow) {
  void router.push(taskPath(row))
}

/** The one sentence a refused turn gets: the engine's code, in this language. */
function sayFailure(cause: unknown) {
  writeError.value = cause instanceof BatchStartRefusal
    ? batchRefusalMessage(cause.code, cause.message)
    : getErrorMessage(cause)
}

/** Continuing opens the execution's own conversation again — the same session,
 *  whose input was fixed by the turn that started it. */
async function continueRun(row: ProjectTaskRow) {
  if (busyId.value) return
  busyId.value = row.id
  writeError.value = ''
  try {
    await startBatchRun(row.latest.sessionId)
    await router.push(runPath(row.id, row.latest.seq, row.latest.sessionId))
  } catch (cause) {
    sayFailure(cause)
    await reload()
  } finally {
    busyId.value = ''
  }
}

/** Running it again opens one more execution of the same task: a new
 *  conversation that starts on the input the task already carries. */
async function rerun(row: ProjectTaskRow) {
  if (busyId.value) return
  busyId.value = row.id
  writeError.value = ''
  try {
    const created = await forebrainApi.createTaskSession(props.projectId, { taskId: row.id })
    await startBatchRun(created.id)
    await router.push(runPath(row.id, created.execution.seq, created.id))
  } catch (cause) {
    sayFailure(cause)
    await reload()
  } finally {
    busyId.value = ''
  }
}

/** A task created here runs its first execution at once; the page opens that
 *  execution, where its process and its deliverables are. */
function openCreated(created: { taskId: string; seq: number; sessionId: string }) {
  newOpen.value = false
  clearNewFlag()
  void router.push(runPath(created.taskId, created.seq, created.sessionId))
}

/** The flag only opens the dialog once: an address that kept it would reopen
 *  the dialog on every visit. */
function clearNewFlag() {
  if (!route.query.new) return
  void router.replace({ query: { ...route.query, new: undefined } })
}

function closeNew() {
  newOpen.value = false
  clearNewFlag()
}

onMounted(() => {
  // The new-chat page's 新建任务 arrives here with the dialog already open.
  if (String(route.query.new ?? '') === '1') newOpen.value = true
  void reload()
})
</script>
