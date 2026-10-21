<template>
  <ForebrainModal :open="true" size="md" :title="t('cron.runsTitle', { name: jobName })" @close="emit('close')">
    <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-3 py-2 text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

    <div v-if="loading && !rows.length" class="py-8 text-center text-[12px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-6 text-center text-[12px] text-[var(--forebrain-muted-text)]">
      {{ t('cron.noRuns') }}
    </div>
    <DataTable
      v-else
      :columns="columns"
      :rows="rows"
      :row-key="(row) => String(row.id)"
      :row-attrs="(row) => ({ 'data-cron-run': String(row.id) })"
      :sub-row="(row) => expanded === row.id"
    >
      <template #cell-status="{ row }">
        <BadgeComponent :variant="stateOf(row.status).variant">{{ stateOf(row.status).label }}</BadgeComponent>
      </template>
      <template #cell-startedAt="{ row }">{{ formatDateTime(row.startedAt) }}</template>
      <template #cell-trigger="{ row }">
        <span class="forebrain-cell-1" v-tip:top.trunc="row.trigger ?? ''">{{ row.trigger || '—' }}</span>
      </template>
      <template #cell-deliveredTo="{ row }">
        <span class="forebrain-cell-1 font-mono" v-tip:top.trunc="row.deliveredTo ?? ''">{{ row.deliveredTo || '—' }}</span>
      </template>
      <template #cell-actions="{ row }">
        <RowActions>
          <!-- A fire is a conversation of its own; only a record whose
               session holds a transcript has one to open. -->
          <RowAction
            v-if="openable(row)"
            :icon="MessageSquare"
            :label="t('cron.openConversation')"
            :name="startedName(row)"
            data-testid="cron-run-open"
            @click="openConversation(row)"
          />
          <RowActionSlot v-else />
          <RowAction
            :icon="expanded === row.id ? ChevronUp : ChevronDown"
            :label="expanded === row.id ? t('cron.hideOutput') : t('cron.viewOutput')"
            :name="startedName(row)"
            :data-testid="`cron-run-output-${row.id}`"
            @click="toggleOutput(row)"
          />
        </RowActions>
      </template>
      <template #sub-row="{ row }">
        <div :data-cron-run-output="String(row.id)" class="whitespace-normal">
          <p v-if="row.output" class="whitespace-pre-wrap text-[12px] text-[var(--forebrain-text-2)]" data-testid="cron-run-output-text">{{ row.output }}</p>
          <p
            v-if="row.error"
            class="mt-1 whitespace-pre-wrap text-[11px] text-[var(--forebrain-danger)]"
            data-testid="cron-run-error"
          >{{ runErrorText(row) }}</p>
          <p v-if="!row.output && !row.error" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('common.empty') }}</p>
        </div>
      </template>
    </DataTable>

    <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
  </ForebrainModal>
</template>

<script setup lang="ts">
/**
 * One scheduled task's history (plan 075): every fire the engine recorded for
 * it, newest first, paged on the server. The record carries what became of the
 * fire and the door to its conversation — a fire runs in a session of its own —
 * and the output stays folded under its own row until it is asked for.
 */
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ChevronDown, ChevronUp, MessageSquare } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActionSlot from '@/components/common/RowActionSlot.vue'
import RowActions from '@/components/common/RowActions.vue'
import TablePager from '@/components/common/TablePager.vue'
import { useServerList } from '@/composables/useServerList'
import { forebrainApi, type CronJobRow, type CronRunRow } from '@/lib/api'
import { formatProviderError } from '@/lib/providerError'
import { formatDateTime } from '@/lib/time'
import { useI18n } from '@/locales'

const props = defineProps<{ projectId: string; job: CronJobRow }>()
const emit = defineEmits<{ close: [] }>()

const { t, locale } = useI18n()
const router = useRouter()

const { rows, total, page, pageSize, loading, error, reload } = useServerList<CronRunRow>(
  (query) => forebrainApi.cronRunsPage(props.projectId, props.job.id, query),
  { pageSize: 10 },
)

/**
 * The table's columns, at the design's widths: the trigger takes the room the
 * others leave. Nothing sorts — the list endpoint ignores `sort`.
 */
const columns: DataColumn[] = [
  { id: 'status', header: t('cron.colStatus'), size: 84 },
  { id: 'startedAt', header: t('cron.colStarted'), size: 132, cellClass: 'forebrain-num font-mono text-[var(--forebrain-text-2)]' },
  { id: 'trigger', header: t('cron.colTrigger'), cellClass: 'text-[var(--forebrain-text-2)]' },
  { id: 'deliveredTo', header: t('cron.colDeliver'), size: 120, cellClass: 'text-[var(--forebrain-text-2)]' },
  { id: 'actions', header: t('projects.colActions'), size: 80, headerClass: 'text-center', cellClass: 'text-right' },
]

/** The fire whose output is open under its own row; an id, never a position. */
const expanded = ref<number | null>(null)

const jobName = computed(() => props.job.name || t('cron.untitled'))

type BadgeVariant = 'default' | 'gray' | 'success' | 'warning' | 'error'

// The statuses the engine writes, named in the viewer's language and toned the
// way the badge palette reads. The name is asked for on each render, so a
// language switched while the modal is open moves with it.
function stateOf(status: string): { label: string; variant: BadgeVariant } {
  switch (status) {
    case 'ok':
      return { label: t('cron.statusOk'), variant: 'success' }
    case 'failed':
      return { label: t('cron.statusFailed'), variant: 'error' }
    case 'delivery_failed':
      return { label: t('cron.statusDeliveryFailed'), variant: 'error' }
    case 'running':
      return { label: t('cron.statusRunning'), variant: 'default' }
    default:
      return { label: status, variant: 'gray' }
  }
}

function startedName(run: CronRunRow): string {
  return `${jobName.value} ${formatDateTime(run.startedAt)}`
}

function openable(run: CronRunRow): boolean {
  return Boolean(run.sessionId && run.hasConversation)
}

function openConversation(run: CronRunRow) {
  if (!run.sessionId) return
  void router.push({ path: '/', query: { session: run.sessionId } })
}

function toggleOutput(run: CronRunRow) {
  expanded.value = expanded.value === run.id ? null : run.id
}

// The runtime stores an error code beside the English sentence it kept; the
// sentence is written here in the viewer's language — following a switch made
// while the record is on screen — and the stored one shows only when the code
// is not one this build knows.
function runErrorText(run: CronRunRow): string {
  const shown = run.error
    ? formatProviderError({ code: run.errorCode ?? '', providerMessage: run.error }, locale.value)
    : null
  return shown ?? run.error ?? ''
}

onMounted(() => {
  void reload()
})
</script>
