<template>
  <div :class="scope === 'project' ? '' : 'flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6'">
    <div :class="scope === 'project' ? 'w-full' : 'mx-auto w-full max-w-3xl'">
      <header class="mb-5 flex items-end justify-between gap-4">
        <div>
          <div class="flex items-center gap-2">
            <h1 class="text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">{{ t('cron.title') }}</h1>
            <span class="scope-badge">{{ scope === 'project' ? t('scope.project') : t('scope.agent') }}</span>
          </div>
          <p class="mt-1 text-[13px] leading-relaxed text-[var(--forebrain-text-2)]">{{ scope === 'project' ? t('cron.projectDescription') : t('cron.description') }}</p>
        </div>
        <div class="flex gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
            {{ t('common.refresh') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" data-testid="cron-new" @click="openEditor()">
            {{ t('cron.newJob') }}
          </button>
        </div>
      </header>

      <p
        v-if="retentionDays !== null"
        class="mb-4 text-[12px] text-[var(--forebrain-muted-text)]"
        data-testid="cron-retention-note"
      >
        {{ t('cron.retentionNote', { days: retentionDays }) }}
        <RouterLink to="/settings?tab=cron" class="ml-1 text-[var(--forebrain-brand-1)] hover:underline" data-testid="cron-retention-edit">
          {{ t('cron.retentionEdit') }}
        </RouterLink>
      </p>

      <p v-if="error" class="mb-4 rounded-xl border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <div v-if="loading && !jobs.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <ul v-else-if="jobs.length" class="space-y-2">
        <li v-for="job in jobs" :key="job.id" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3" :data-cron-job="job.name || job.id">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <span class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ job.name || t('cron.untitled') }}</span>
                <span class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 font-mono text-[11px] text-[var(--forebrain-muted-text)]">{{ job.schedule }}</span>
                <span v-if="job.lastStatus" class="rounded-full border px-2 py-0.5 text-[11px]" :class="statusClass(job.lastStatus)">{{ statusLabel(job.lastStatus) }}</span>
              </div>
              <p class="mt-1 line-clamp-2 text-[12px] leading-relaxed text-[var(--forebrain-text-2)]">{{ job.prompt }}</p>
              <p class="mt-1 text-[11px] text-[var(--forebrain-muted-text)]">
                {{ job.enabled ? nextRunLabel(job) : t('cron.paused') }}
                <span> · {{ job.deliver ? t('cron.deliversTo', { target: job.deliver }) : t('cron.recordOnly') }}</span>
                <span v-if="job.runCount"> · {{ t('cron.runCount', { count: job.runCount }) }}</span>
              </p>
              <p v-if="job.lastError" class="mt-1 text-[11px] text-[var(--forebrain-danger)]">{{ jobErrorText(job) }}</p>
            </div>
            <div class="flex shrink-0 flex-col items-end gap-1">
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" @click="runNow(job)">{{ t('cron.runNow') }}</button>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" @click="togglePause(job)">
                {{ job.enabled ? t('cron.pause') : t('cron.resume') }}
              </button>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" @click="openEditor(job)">{{ t('common.edit') }}</button>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" @click="openRuns(job)">{{ t('cron.history') }}</button>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px] text-[var(--forebrain-danger)]" @click="remove(job)">{{ t('common.delete') }}</button>
            </div>
          </div>
          <div v-if="openRunsFor === job.id" class="mt-3 border-t border-[var(--forebrain-divider)] pt-2">
            <p v-if="!runs.length" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('cron.noRuns') }}</p>
            <ul v-else class="space-y-1">
              <li v-for="row in runs" :key="row.id" class="rounded-lg px-2 py-1.5 text-[12px] odd:bg-[var(--forebrain-surface)]">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="rounded-full border px-2 py-0.5 text-[11px]" :class="statusClass(row.status)">{{ statusLabel(row.status) }}</span>
                  <span class="text-[var(--forebrain-muted-text)]">{{ formatTime(row.startedAt) }}</span>
                  <span v-if="row.trigger" class="text-[var(--forebrain-muted-text)]">· {{ row.trigger }}</span>
                  <span v-if="row.deliveredTo" class="text-[var(--forebrain-muted-text)]">· {{ row.deliveredTo }}</span>
                  <button
                    v-if="row.sessionId && row.hasConversation"
                    type="button"
                    class="forebrain-btn forebrain-btn-ghost h-6 px-2 text-[11px]"
                    data-testid="cron-run-open"
                    @click="openConversation(row)"
                  >{{ t('cron.openConversation') }}</button>
                </div>
                <p v-if="row.output" class="mt-1 whitespace-pre-wrap text-[12px] text-[var(--forebrain-text-2)]">{{ row.output }}</p>
                <p v-if="row.error" data-testid="cron-run-error" class="mt-1 whitespace-pre-wrap text-[11px] text-[var(--forebrain-danger)]">{{ runErrorText(row) }}</p>
              </li>
            </ul>
          </div>
        </li>
      </ul>
      <p v-else class="rounded-xl border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
        {{ t('cron.empty') }}
      </p>
    </div>

    <!-- Job editor -->
    <div
      v-if="editorOpen"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4"
      data-testid="cron-editor"
      @click.self="closeEditor"
    >
      <div class="max-h-[85vh] w-full max-w-2xl overflow-y-auto rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-5 shadow-lg">
        <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ editingId ? t('cron.editJob') : t('cron.newJob') }}</div>

        <label class="mt-3 block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('cron.namePlaceholder') }}</span>
          <input v-model="editorName" type="text" class="forebrain-field h-9" data-testid="cron-name" />
        </label>

        <div class="mt-3">
          <ScheduleBuilder v-model="editorSchedule" @validated="scheduleValid = $event" />
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
  </div>
</template>

<script setup lang="ts">
/**
 * Standing work on a schedule. The two surfaces share this view: the agent
 * page lists agent-wide jobs, a project tab passes its id and lists its own.
 * Schedules come from the builder — never typed by hand — and the preview
 * under them is the engine's own parse of what will be saved.
 */
import { computed, onMounted, ref } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import ScheduleBuilder from '@/components/cron/ScheduleBuilder.vue'
import { getErrorMessage, forebrainApi, type CronJobRecord, type CronRunRecord } from '@/lib/api'
import { formatProviderError } from '@/lib/providerError'
import { useI18n } from '@/locales'

const props = defineProps<{
  /** Empty on the agent page; a project id in the project space tab. */
  projectId?: string
  scope?: 'agent' | 'project'
}>()

const { t, locale } = useI18n()
const router = useRouter()

const jobs = ref<CronJobRecord[]>([])
const runs = ref<CronRunRecord[]>([])
const openRunsFor = ref('')
const loading = ref(false)
const saving = ref(false)
const error = ref('')

const editorOpen = ref(false)
const editingId = ref('')
const editorName = ref('')
const editorSchedule = ref('')
const scheduleValid = ref(false)
const editorPrompt = ref('')
const editorDeliver = ref('')
const editorRepeatLimit = ref(0)
const editorError = ref('')

// The delivery dropdown lists only the channels this agent has enabled; "no
// delivery" stays an explicit first option rather than a blank input.
const enabledChannels = ref<string[]>([])

// Retention is the install's, not this agent's, so the note under the header
// reads the global settings once and links to their tab in the settings page.
const retentionDays = ref<number | null>(null)

const scope = computed(() => props.scope ?? 'agent')

async function loadRetentionDays() {
  try {
    retentionDays.value = (await forebrainApi.cronSettings()).retentionDays
  } catch {
    retentionDays.value = null
  }
}

async function loadChannels() {
  try {
    const data = await forebrainApi.channels()
    const section = (data.channels ?? {}) as Record<string, unknown>
    enabledChannels.value = Object.entries(section)
      .filter(([, value]) => Boolean((value as { enabled?: boolean } | null)?.enabled))
      .map(([key]) => key)
  } catch {
    enabledChannels.value = []
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    jobs.value = await forebrainApi.cronJobs(props.projectId)
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

function openEditor(job?: CronJobRecord) {
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
      projectId: props.projectId ?? '',
    }
    if (editingId.value) {
      await forebrainApi.cronUpdate(editingId.value, body)
    } else {
      await forebrainApi.cronCreate(body)
    }
    editorOpen.value = false
    await load()
  } catch (e: unknown) {
    editorError.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

async function togglePause(job: CronJobRecord) {
  error.value = ''
  try {
    await forebrainApi.cronUpdate(job.id, { enabled: !job.enabled })
    await load()
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

async function runNow(job: CronJobRecord) {
  error.value = ''
  try {
    await forebrainApi.cronRunNow(job.id)
    await openRuns(job)
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

async function remove(job: CronJobRecord) {
  error.value = ''
  try {
    await forebrainApi.cronDelete(job.id)
    if (openRunsFor.value === job.id) openRunsFor.value = ''
    await load()
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

async function openRuns(job: CronJobRecord) {
  if (openRunsFor.value === job.id) {
    openRunsFor.value = ''
    return
  }
  error.value = ''
  try {
    runs.value = await forebrainApi.cronRuns(job.id)
    openRunsFor.value = job.id
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

// A fire is a conversation of its own; its record is the door to it. Only a
// record whose session holds a transcript has one to open.
function openConversation(row: CronRunRecord) {
  if (!row.sessionId) return
  void router.push({ path: '/', query: { session: row.sessionId } })
}

// The runtime stores an error code beside the English sentence it kept; the
// sentence is written here in the viewer's language — following a switch made
// while the record is on screen — and the stored one shows only when the code
// is not one this build knows.
function runErrorText(row: CronRunRecord): string {
  const shown = row.error
    ? formatProviderError({ code: row.errorCode ?? '', providerMessage: row.error }, locale.value)
    : null
  return shown ?? row.error ?? ''
}

function jobErrorText(job: CronJobRecord): string {
  const shown = job.lastError
    ? formatProviderError({ code: job.lastErrorCode ?? '', providerMessage: job.lastError }, locale.value)
    : null
  return shown ?? job.lastError ?? ''
}

function statusLabel(status: string): string {
  switch (status) {
    case 'ok':
      return t('cron.statusOk')
    case 'failed':
      return t('cron.statusFailed')
    case 'delivery_failed':
      return t('cron.statusDeliveryFailed')
    case 'running':
      return t('cron.statusRunning')
    default:
      return status
  }
}

function statusClass(status: string): string {
  switch (status) {
    case 'ok':
      return 'border-[var(--forebrain-brand-border-strong)] text-[var(--forebrain-brand-1)]'
    case 'failed':
    case 'delivery_failed':
      return 'border-[var(--forebrain-danger)] text-[var(--forebrain-danger)]'
    default:
      return 'border-[var(--forebrain-divider)] text-[var(--forebrain-text-2)]'
  }
}

function nextRunLabel(job: CronJobRecord): string {
  if (!job.nextRunAt) return t('cron.paused')
  return t('cron.nextRun', { time: formatTime(job.nextRunAt) })
}

function formatTime(unix?: number): string {
  if (!unix) return '—'
  return new Date(unix * 1000).toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US')
}

onMounted(() => {
  void load()
  void loadChannels()
  void loadRetentionDays()
})
</script>
