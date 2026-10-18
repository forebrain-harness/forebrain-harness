<template>
  <ForebrainModal :open="true" size="md" :title="t('cron.runsTitle', { name: jobName })" @close="emit('close')">
    <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-3 py-2 text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

    <div v-if="loading && !rows.length" class="py-8 text-center text-[12px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-6 text-center text-[12px] text-[var(--forebrain-muted-text)]">
      {{ t('cron.noRuns') }}
    </div>
    <div v-else class="forebrain-table-wrap">
      <table class="forebrain-table forebrain-table--fixed">
        <colgroup>
          <col style="width: 84px" />
          <col style="width: 132px" />
          <col />
          <col style="width: 120px" />
          <col style="width: 80px" />
        </colgroup>
        <thead>
          <tr>
            <th>{{ t('cron.colStatus') }}</th>
            <th>{{ t('cron.colStarted') }}</th>
            <th>{{ t('cron.colTrigger') }}</th>
            <th>{{ t('cron.colDeliver') }}</th>
            <th class="text-right">{{ t('projects.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="run in rows" :key="run.id">
            <tr :data-cron-run="run.id">
              <td>
                <BadgeComponent :variant="stateOf(run.status).variant">{{ stateOf(run.status).label }}</BadgeComponent>
              </td>
              <td class="forebrain-num font-mono text-[var(--forebrain-text-2)]">{{ formatDateTime(run.startedAt) }}</td>
              <td class="text-[var(--forebrain-text-2)]">
                <span class="forebrain-cell-1" v-tip:top.trunc="run.trigger ?? ''">{{ run.trigger || '—' }}</span>
              </td>
              <td class="text-[var(--forebrain-text-2)]">
                <span class="forebrain-cell-1 font-mono" v-tip:top.trunc="run.deliveredTo ?? ''">{{ run.deliveredTo || '—' }}</span>
              </td>
              <td class="text-right">
                <RowActions>
                  <!-- A fire is a conversation of its own; only a record whose
                       session holds a transcript has one to open. -->
                  <RowAction
                    v-if="openable(run)"
                    :icon="MessageSquare"
                    :label="t('cron.openConversation')"
                    :name="startedName(run)"
                    data-testid="cron-run-open"
                    @click="openConversation(run)"
                  />
                  <RowActionSlot v-else />
                  <RowAction
                    :icon="expanded === run.id ? ChevronUp : ChevronDown"
                    :label="expanded === run.id ? t('cron.hideOutput') : t('cron.viewOutput')"
                    :name="startedName(run)"
                    :data-testid="`cron-run-output-${run.id}`"
                    @click="toggleOutput(run)"
                  />
                </RowActions>
              </td>
            </tr>
            <tr v-if="expanded === run.id" :data-cron-run-output="run.id">
              <td colspan="5" class="!whitespace-normal">
                <p v-if="run.output" class="whitespace-pre-wrap text-[12px] text-[var(--forebrain-text-2)]" data-testid="cron-run-output-text">{{ run.output }}</p>
                <p
                  v-if="run.error"
                  class="mt-1 whitespace-pre-wrap text-[11px] text-[var(--forebrain-danger)]"
                  data-testid="cron-run-error"
                >{{ runErrorText(run) }}</p>
                <p v-if="!run.output && !run.error" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('common.empty') }}</p>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>

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
