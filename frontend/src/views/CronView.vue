<template>
  <div class="w-full space-y-4">
    <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

    <ListToolbar>
      <template #left>
        <input
          v-model="q"
          type="search"
          :placeholder="t('cron.searchPlaceholder')"
          class="forebrain-field h-[34px]"
          style="max-width: 240px"
          data-testid="cron-search"
        />
      </template>
      <template #actions>
        <button type="button" class="forebrain-btn forebrain-btn-primary gap-1.5 text-xs" data-testid="cron-new" @click="openEditor()">
          <Plus class="size-3.5" aria-hidden="true" />{{ t('cron.new') }}
        </button>
      </template>
    </ListToolbar>

    <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
      {{ t('cron.empty') }}
    </div>
    <div v-else class="forebrain-table-wrap">
      <table class="forebrain-table forebrain-table--fixed">
        <colgroup>
          <col />
          <col style="width: 140px" />
          <col style="width: 200px" />
          <col style="width: 110px" />
          <col style="width: 96px" />
          <col style="width: 64px" />
          <col style="width: 56px" />
          <col style="width: 140px" />
        </colgroup>
        <thead>
          <tr>
            <th>{{ t('cron.colName') }}</th>
            <th>{{ t('cron.colSchedule') }}</th>
            <th>{{ t('cron.colDeliver') }}</th>
            <th>{{ t('cron.colNext') }}</th>
            <th>{{ t('cron.colLast') }}</th>
            <th class="text-right">{{ t('cron.colRuns') }}</th>
            <th>{{ t('cron.colEnabled') }}</th>
            <th class="text-center">{{ t('projects.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="job in rows" :key="job.id" :data-cron-job="job.name || job.id">
            <td>
              <span class="forebrain-cell-1 font-medium" v-tip:top.trunc="nameOf(job)">{{ nameOf(job) }}</span>
            </td>
            <td>
              <span class="forebrain-cell-1 font-mono text-[var(--forebrain-text-2)]" v-tip:top.trunc="job.schedule">{{ job.schedule }}</span>
            </td>
            <td>
              <span
                v-if="job.deliver"
                class="forebrain-cell-1 font-mono text-[var(--forebrain-text-2)]"
                v-tip:top.trunc="job.deliver"
              >{{ job.deliver }}</span>
              <span v-else class="text-[var(--forebrain-muted-text)]">{{ t('cron.recordOnlyShort') }}</span>
            </td>
            <td class="forebrain-num">
              <BadgeComponent v-if="!job.enabled" variant="warning">{{ t('cron.paused') }}</BadgeComponent>
              <span v-else class="text-[var(--forebrain-text-2)]">{{ formatMonthDayTime(job.nextRunAt) }}</span>
            </td>
            <td>
              <BadgeComponent
                v-if="job.lastStatus"
                :variant="lastStateOf(job).variant"
                v-tip:top="jobErrorText(job)"
              >{{ lastStateOf(job).label }}</BadgeComponent>
              <span v-else class="text-[var(--forebrain-text-2)]">—</span>
            </td>
            <td class="forebrain-num text-right">{{ t('cron.times', { count: job.runCount ?? 0 }) }}</td>
            <td>
              <SwitchComponent
                :model-value="job.enabled"
                :aria-label="t('cron.switchLabel', { name: nameOf(job) })"
                :data-testid="`cron-toggle-${job.id}`"
                @update:model-value="togglePause(job)"
              />
            </td>
            <td class="text-right">
              <RowActions>
                <RowAction
                  :icon="Play"
                  :label="t('cron.runNow')"
                  :name="nameOf(job)"
                  :data-testid="`cron-run-${job.id}`"
                  @click="runNow(job)"
                />
                <RowAction
                  :icon="Pencil"
                  :label="t('common.edit')"
                  :name="nameOf(job)"
                  :data-testid="`cron-edit-${job.id}`"
                  @click="openEditor(job)"
                />
                <RowAction
                  :icon="History"
                  :label="t('cron.history')"
                  :name="nameOf(job)"
                  :data-testid="`cron-history-${job.id}`"
                  @click="openRuns(job)"
                />
                <RowAction
                  :icon="Trash2"
                  :label="t('common.delete')"
                  :name="nameOf(job)"
                  tone="danger"
                  :data-testid="`cron-delete-${job.id}`"
                  @click="remove(job)"
                />
              </RowActions>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
    <ListNote>{{ t('cron.projectNote') }}</ListNote>
  </div>

  <!-- A task's fires are a history of their own: a modal over the table, so the
       list underneath stays where it was. -->
  <CronRunsDialog
    v-if="runsJob"
    :project-id="projectId"
    :job="runsJob"
    @close="runsJob = null"
  />

  <!-- Job editor -->
  <div
    v-if="editorOpen"
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4"
    data-testid="cron-editor"
    @click.self="closeEditor"
  >
    <div class="max-h-[85vh] w-full max-w-2xl overflow-y-auto rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-5 shadow-lg">
      <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ editingId ? t('cron.editJob') : t('cron.newJob') }}</div>

      <label class="mt-3 block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('cron.namePlaceholder') }}</span>
        <input v-model="editorName" type="text" class="forebrain-field h-9" data-testid="cron-name" />
      </label>

      <div class="mt-3">
        <ScheduleBuilder v-model="editorSchedule" :project-id="projectId" @validated="scheduleValid = $event" />
      </div>

      <label class="mt-3 block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('cron.promptPlaceholder') }}</span>
        <textarea v-model="editorPrompt" rows="3" class="forebrain-field w-full" data-testid="cron-prompt" />
      </label>

      <div class="mt-3 grid gap-3 sm:grid-cols-2">
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('cron.deliverLabel') }}</span>
          <select v-model="editorDeliver" class="forebrain-field h-9" data-testid="cron-deliver">
            <option value="">{{ t('cron.recordOnly') }}</option>
            <option v-for="channel in enabledChannels" :key="channel" :value="channel">{{ channel }}</option>
          </select>
        </label>
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('cron.repeatLimitLabel') }}</span>
          <input v-model.number="editorRepeatLimit" type="number" min="0" class="forebrain-field h-9" data-testid="cron-repeat-limit" />
          <span class="mt-1 block text-[11px] text-[var(--forebrain-muted-text)]">{{ t('cron.repeatLimitHint') }}</span>
        </label>
      </div>

      <p v-if="editorError" class="mt-3 text-[12px] text-[var(--forebrain-danger)]">{{ editorError }}</p>

      <div class="mt-4 flex justify-end gap-2">
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="closeEditor">{{ t('common.cancel') }}</button>
        <button
          type="button"
          class="forebrain-btn forebrain-btn-primary text-xs"
          :disabled="saving || !scheduleValid || !editorPrompt.trim()"
          data-testid="cron-save"
          @click="save"
        >
          {{ saving ? t('common.loading') : t('common.save') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * A project's standing work on a schedule. Scheduled tasks belong to a
 * project, so this view is always scoped to one: it lists that project's jobs
 * and fires them in the project root. Schedules come from the builder — never
 * typed by hand — and the preview under them is the engine's own parse of what
 * will be saved.
 *
 * The list is a server-paged table (plan 075): the search narrows it on the
 * server, the enabled switch is the pause/resume it replaced, and what became
 * of the last fire sits in its own column — the failure's own sentence is that
 * cell's tip. A job's history is a modal (`CronRunsDialog`), and editing opens
 * the builder over the table.
 */
import { computed, onMounted, ref } from 'vue'
import { History, Pencil, Play, Plus, Trash2 } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import TablePager from '@/components/common/TablePager.vue'
import CronRunsDialog from '@/components/cron/CronRunsDialog.vue'
import ScheduleBuilder from '@/components/cron/ScheduleBuilder.vue'
import { useServerList } from '@/composables/useServerList'
import { getErrorMessage, forebrainApi, type CronJobRow } from '@/lib/api'
import { formatProviderError } from '@/lib/providerError'
import { formatMonthDayTime } from '@/lib/time'
import { useI18n } from '@/locales'

const props = defineProps<{ projectId: string }>()

const { t, locale } = useI18n()

const { rows, total, page, pageSize, q, loading, error: listError, reload } = useServerList<CronJobRow>(
  (query) => forebrainApi.cronJobsPage(props.projectId, query),
  { pageSize: 10 },
)

const writeError = ref('')
const error = computed(() => listError.value || writeError.value)

const runsJob = ref<CronJobRow | null>(null)
const saving = ref(false)

const editorOpen = ref(false)
const editingId = ref('')
const editorName = ref('')
const editorSchedule = ref('')
const scheduleValid = ref(false)
const editorPrompt = ref('')
const editorDeliver = ref('')
const editorRepeatLimit = ref(0)
const editorError = ref('')

// The delivery dropdown lists only the channels this project has enabled; "no
// delivery" stays an explicit first option rather than a blank input. The
// channel map is read raw — a kind is the delivery value itself (`wecom_callback`
// is not `wecomCallback`), and the camelCase interceptor would rename it.
const enabledChannels = ref<string[]>([])

async function loadChannels() {
  try {
    const data = await forebrainApi.projectChannelsRaw(props.projectId)
    enabledChannels.value = Object.entries(data.channels ?? {})
      .filter(([, value]) => Boolean((value as { enabled?: boolean } | null)?.enabled))
      .map(([kind]) => kind)
  } catch {
    enabledChannels.value = []
  }
}

function nameOf(job: CronJobRow): string {
  return job.name || t('cron.untitled')
}

function openEditor(job?: CronJobRow) {
  editingId.value = job?.id ?? ''
  editorName.value = job?.name ?? ''
  // Editing an existing job starts from its stored expression; the builder's
  // cron mode shows it as-is and any mode re-selection rebuilds it.
  editorSchedule.value = job?.schedule ?? ''
  scheduleValid.value = Boolean(job)
  editorPrompt.value = job?.prompt ?? ''
  editorDeliver.value = job?.deliver ?? ''
  editorRepeatLimit.value = job?.repeatLimit ?? 0
  editorError.value = ''
  editorOpen.value = true
}

function closeEditor() {
  editorOpen.value = false
}

async function save() {
  saving.value = true
  editorError.value = ''
  try {
    const body = {
      name: editorName.value.trim(),
      schedule: editorSchedule.value.trim(),
      prompt: editorPrompt.value.trim(),
      deliver: editorDeliver.value,
      repeat_limit: editorRepeatLimit.value > 0 ? editorRepeatLimit.value : 0,
    }
    if (editingId.value) {
      await forebrainApi.cronUpdate(props.projectId, editingId.value, body)
    } else {
      await forebrainApi.cronCreate(props.projectId, body)
    }
    editorOpen.value = false
    await reload()
  } catch (e: unknown) {
    editorError.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

// The switch in the 启用 column is the pause and the resume the buttons used to
// be: a paused job keeps its schedule and stops firing.
async function togglePause(job: CronJobRow) {
  writeError.value = ''
  try {
    await forebrainApi.cronUpdate(props.projectId, job.id, { enabled: !job.enabled })
    await reload()
  } catch (e: unknown) {
    writeError.value = getErrorMessage(e)
  }
}

// A fire starts now; the reload is what moves the row's own last-run column.
async function runNow(job: CronJobRow) {
  writeError.value = ''
  try {
    await forebrainApi.cronRunNow(props.projectId, job.id)
    await reload()
  } catch (e: unknown) {
    writeError.value = getErrorMessage(e)
  }
}

async function remove(job: CronJobRow) {
  writeError.value = ''
  try {
    await forebrainApi.cronDelete(props.projectId, job.id)
    if (runsJob.value?.id === job.id) runsJob.value = null
    await reload()
  } catch (e: unknown) {
    writeError.value = getErrorMessage(e)
  }
}

function openRuns(job: CronJobRow) {
  runsJob.value = job
}

// The statuses the engine writes, named in the viewer's language and toned the
// way the badge palette reads: a success is green, a failure red, a fire still
// running brand. The name is asked for on each render, so a language switched
// while the table is on screen moves with it.
type BadgeVariant = 'default' | 'gray' | 'success' | 'warning' | 'error'

function lastStateOf(job: CronJobRow): { label: string; variant: BadgeVariant } {
  switch (job.lastStatus) {
    case 'ok':
      return { label: t('cron.statusOk'), variant: 'success' }
    case 'failed':
      return { label: t('cron.statusFailed'), variant: 'error' }
    case 'delivery_failed':
      return { label: t('cron.statusDeliveryFailed'), variant: 'error' }
    case 'running':
      return { label: t('cron.statusRunning'), variant: 'default' }
    default:
      return { label: job.lastStatus ?? '—', variant: 'gray' }
  }
}

// The runtime stores an error code beside the English sentence it kept; the
// sentence is written here in the viewer's language — following a switch made
// while the record is on screen — and the stored one shows only when the code
// is not one this build knows.
function jobErrorText(job: CronJobRow): string {
  const shown = job.lastError
    ? formatProviderError({ code: job.lastErrorCode ?? '', providerMessage: job.lastError }, locale.value)
    : null
  return shown ?? job.lastError ?? ''
}

onMounted(() => {
  void reload()
  void loadChannels()
})
</script>
