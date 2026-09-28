<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6">
    <div class="mx-auto w-full max-w-3xl">
      <header class="mb-5 flex items-end justify-between gap-4">
        <div>
          <h1 class="font-serif text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">{{ t('cron.title') }}</h1>
          <p class="mt-1 text-[13px] leading-relaxed text-[var(--forebrain-text-2)]">{{ t('cron.description') }}</p>
        </div>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
          {{ t('common.refresh') }}
        </button>
      </header>

      <p v-if="error" class="mb-4 rounded-xl border border-[rgba(160,70,70,0.36)] bg-[rgba(160,70,70,0.08)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <section class="mb-5 rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4">
        <h2 class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('cron.newJob') }}</h2>
        <p class="mt-1 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('cron.scheduleHint') }}</p>
        <div class="mt-3 grid gap-2 sm:grid-cols-2">
          <input v-model="draft.name" :placeholder="t('cron.namePlaceholder')" class="forebrain-field" />
          <input v-model="draft.schedule" :placeholder="t('cron.schedulePlaceholder')" class="forebrain-field font-mono" />
        </div>
        <textarea v-model="draft.prompt" rows="3" :placeholder="t('cron.promptPlaceholder')" class="forebrain-field mt-2 w-full" />
        <div class="mt-2 flex flex-wrap items-center gap-2">
          <input v-model="draft.deliver" :placeholder="t('cron.deliverPlaceholder')" class="forebrain-field flex-1" />
          <button type="button" class="forebrain-btn forebrain-btn-primary h-9 px-4 text-[12px]" :disabled="creating || !draft.prompt.trim() || !draft.schedule.trim()" @click="create">
            {{ t('cron.create') }}
          </button>
        </div>
      </section>

      <div v-if="loading && !jobs.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <ul v-else-if="jobs.length" class="space-y-2">
        <li v-for="job in jobs" :key="job.id" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3">
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
                <span v-if="job.deliver"> · {{ t('cron.deliversTo', { target: job.deliver }) }}</span>
                <span v-if="job.runCount"> · {{ t('cron.runCount', { count: job.runCount }) }}</span>
              </p>
              <p v-if="job.lastError" class="mt-1 text-[11px] text-[var(--forebrain-danger)]">{{ job.lastError }}</p>
            </div>
            <div class="flex shrink-0 flex-col items-end gap-1">
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" @click="runNow(job)">{{ t('cron.runNow') }}</button>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" @click="togglePause(job)">
                {{ job.enabled ? t('cron.pause') : t('cron.resume') }}
              </button>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" @click="openRuns(job)">{{ t('cron.history') }}</button>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px] text-[var(--forebrain-danger)]" @click="remove(job)">{{ t('common.delete') }}</button>
            </div>
          </div>
          <div v-if="openRunsFor === job.id" class="mt-3 border-t border-[var(--forebrain-divider)] pt-2">
            <p v-if="!runs.length" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('cron.noRuns') }}</p>
            <ul v-else class="space-y-1">
              <li v-for="row in runs" :key="row.id" class="rounded-lg px-2 py-1.5 text-[12px] odd:bg-[var(--forebrain-surface-soft)]">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="rounded-full border px-2 py-0.5 text-[11px]" :class="statusClass(row.status)">{{ statusLabel(row.status) }}</span>
                  <span class="text-[var(--forebrain-muted-text)]">{{ formatTime(row.startedAt) }}</span>
                  <span v-if="row.trigger" class="text-[var(--forebrain-muted-text)]">· {{ row.trigger }}</span>
                  <span v-if="row.deliveredTo" class="text-[var(--forebrain-muted-text)]">· {{ row.deliveredTo }}</span>
                </div>
                <p v-if="row.output" class="mt-1 whitespace-pre-wrap text-[12px] text-[var(--forebrain-text-2)]">{{ row.output }}</p>
                <p v-if="row.error" class="mt-1 whitespace-pre-wrap text-[11px] text-[var(--forebrain-danger)]">{{ row.error }}</p>
              </li>
            </ul>
          </div>
        </li>
      </ul>
      <p v-else class="rounded-xl border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
        {{ t('cron.empty') }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * Standing work for the current primary agent. A job fires its prompt in a
 * fresh session on a schedule and delivers the answer; the history below each
 * one is what it actually did, which is the only honest way to tell a schedule
 * that works from one that silently never fires.
 */
import { onMounted, reactive, ref } from 'vue'
import { getErrorMessage, forebrainApi, type CronJobRecord, type CronRunRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()
const jobs = ref<CronJobRecord[]>([])
const runs = ref<CronRunRecord[]>([])
const openRunsFor = ref('')
const loading = ref(false)
const creating = ref(false)
const error = ref('')
const draft = reactive({ name: '', schedule: '', prompt: '', deliver: '' })

async function load() {
  loading.value = true
  error.value = ''
  try {
    jobs.value = await forebrainApi.cronJobs()
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

async function create() {
  creating.value = true
  error.value = ''
  try {
    await forebrainApi.cronCreate({
      name: draft.name.trim(),
      schedule: draft.schedule.trim(),
      prompt: draft.prompt.trim(),
      deliver: draft.deliver.trim(),
    })
    draft.name = ''
    draft.schedule = ''
    draft.prompt = ''
    draft.deliver = ''
    await load()
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    creating.value = false
  }
}

async function togglePause(job: CronJobRecord) {
  error.value = ''
  try {
    await forebrainApi.cronUpdate(job.id, { enabled: !job.enabled })
    await load()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

async function runNow(job: CronJobRecord) {
  error.value = ''
  try {
    await forebrainApi.cronRunNow(job.id)
    await openRuns(job)
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

async function remove(job: CronJobRecord) {
  error.value = ''
  try {
    await forebrainApi.cronDelete(job.id)
    if (openRunsFor.value === job.id) openRunsFor.value = ''
    await load()
  } catch (e) {
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
  } catch (e) {
    error.value = getErrorMessage(e)
  }
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
      return 'border-[rgba(160,70,70,0.36)] text-[var(--forebrain-danger)]'
    default:
      return 'border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]'
  }
}

function formatTime(seconds?: number): string {
  if (!seconds) return '—'
  return new Date(seconds * 1000).toLocaleString()
}

function nextRunLabel(job: CronJobRecord): string {
  if (!job.nextRunAt) return t('cron.spent')
  return t('cron.nextRun', { time: formatTime(job.nextRunAt) })
}

onMounted(load)
</script>
