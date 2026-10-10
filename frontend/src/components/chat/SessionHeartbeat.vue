<template>
  <div ref="rootRef" class="relative" data-testid="heartbeat-control">
    <button
      ref="triggerRef"
      type="button"
      class="heartbeat-trigger"
      :class="{ 'heartbeat-trigger--running': running }"
      aria-haspopup="dialog"
      :aria-expanded="open"
      data-testid="heartbeat-toggle"
      @click="toggle"
    >
      <Activity class="heartbeat-trigger-ic size-3.5" aria-hidden="true" />
      {{ t('heartbeat.title') }}
      <span v-if="record" class="heartbeat-trigger-meta" data-testid="heartbeat-status">{{ statusOf(record) }}</span>
    </button>

    <div
      v-if="open"
      class="heartbeat-pop"
      role="dialog"
      :aria-label="t('heartbeat.title')"
      data-testid="heartbeat-popover"
    >
      <div class="flex items-start justify-between gap-3">
        <div class="text-sm font-medium text-[var(--forebrain-text)]">{{ t('heartbeat.title') }}</div>
        <SwitchComponent
          v-if="record"
          :model-value="running"
          :disabled="busy"
          :aria-label="t('heartbeat.switch')"
          data-testid="heartbeat-switch"
          @update:model-value="(on: boolean) => setRunning(record!, on)"
        />
      </div>
      <p class="mt-1 text-[11px] leading-relaxed text-[var(--forebrain-muted-text)]">{{ t('heartbeat.description') }}</p>
      <p v-if="record" class="heartbeat-schedule" data-testid="heartbeat-schedule">{{ scheduleText }}</p>

      <label class="heartbeat-label" :for="minutesId">{{ t('heartbeat.interval') }}</label>
      <div class="flex items-center gap-2">
        <input :id="minutesId" v-model.number="minutes" type="number" min="1" class="forebrain-field w-20" />
        <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('heartbeat.minutes') }}</span>
      </div>
      <label class="heartbeat-label" :for="promptId">{{ t('heartbeat.promptLabel') }}</label>
      <textarea
        :id="promptId"
        ref="promptRef"
        v-model="prompt"
        rows="3"
        :placeholder="t('heartbeat.promptPlaceholder')"
        class="forebrain-field w-full"
      />
      <p v-if="error" class="mt-2 text-[11px] text-[var(--forebrain-danger)]" role="alert">{{ error }}</p>

      <div class="mt-3 flex items-center justify-between gap-2">
        <button
          v-if="record"
          type="button"
          class="forebrain-btn forebrain-btn-ghost h-7 px-2.5 text-[12px] text-[var(--forebrain-danger)]"
          :disabled="busy"
          data-testid="heartbeat-clear"
          @click="remove"
        >{{ t('heartbeat.clear') }}</button>
        <span v-else />
        <button
          type="button"
          class="forebrain-btn forebrain-btn-primary h-7 px-3 text-[12px]"
          :disabled="busy || !canSave"
          data-testid="heartbeat-save"
          @click="submit"
        >{{ record ? t('heartbeat.update') : t('heartbeat.start') }}</button>
      </div>

      <!-- A heartbeat keeps asking while nobody looks: the others are listed
           here so one left running elsewhere is never out of sight. -->
      <section v-if="others.length" class="heartbeat-others" data-testid="heartbeat-others">
        <div class="heartbeat-others-title">{{ t('heartbeat.others') }} · {{ others.length }}</div>
        <ul class="mt-1 space-y-0.5">
          <li v-for="beat in others" :key="beat.sessionId" class="heartbeat-other">
            <button
              type="button"
              class="heartbeat-other-open"
              :title="t('heartbeat.openConversation')"
              @click="openConversation(beat.sessionId)"
            >
              <Activity
                class="size-3 flex-none"
                :class="beat.paused ? 'text-[var(--forebrain-muted-text)]' : 'text-[var(--forebrain-brand-1)]'"
                aria-hidden="true"
              />
              <span class="truncate">{{ beat.sessionTitle || t('chatDrawer.untitled') }}</span>
            </button>
            <span class="heartbeat-other-status">{{ statusOf(beat) }}</span>
            <button
              type="button"
              class="heartbeat-other-action"
              :disabled="busy"
              :aria-label="beat.paused ? t('heartbeat.resume') : t('heartbeat.pause')"
              :title="beat.paused ? t('heartbeat.resume') : t('heartbeat.pause')"
              @click="setRunning(beat, beat.paused)"
            >
              <Play v-if="beat.paused" class="size-3" aria-hidden="true" />
              <Pause v-else class="size-3" aria-hidden="true" />
            </button>
          </li>
        </ul>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The open conversation's heartbeat, in the conversation's own header: a
 * recurring instruction that has no session of its own and fires back into
 * this thread once it has been idle for the interval, so it sees everything
 * the conversation has built up. The popover edits this conversation's and
 * lists the agent's other heartbeats, each one click from pausing.
 */
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Activity, Pause, Play } from 'lucide-vue-next'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import { useHeartbeats } from '@/composables/useHeartbeats'
import type { HeartbeatRecord } from '@/lib/api'
import { formatAutoContinueTime } from '@/lib/autoContinue'
import { currentLocale, useI18n } from '@/locales'

const props = defineProps<{ sessionId: string }>()

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { records, refresh, forSession, save, clear, errorMessage } = useHeartbeats()

const open = ref(false)
const minutes = ref(10)
const prompt = ref('')
const busy = ref(false)
const error = ref('')
const rootRef = ref<HTMLElement | null>(null)
const triggerRef = ref<HTMLButtonElement | null>(null)
const promptRef = ref<HTMLTextAreaElement | null>(null)
const minutesId = `heartbeat-minutes-${Math.random().toString(36).slice(2, 8)}`
const promptId = `heartbeat-prompt-${Math.random().toString(36).slice(2, 8)}`

const record = computed(() => forSession(props.sessionId))
const running = computed(() => Boolean(record.value && !record.value.paused))
const others = computed(() => records.value.filter((beat) => beat.sessionId !== props.sessionId))

const intervalSeconds = computed(() => Math.max(1, Math.round(Number(minutes.value) || 0)) * 60)
const dirty = computed(() => {
  const current = record.value
  if (!current) return true
  return current.intervalSeconds !== intervalSeconds.value || current.prompt !== prompt.value.trim()
})
const canSave = computed(() => Boolean(prompt.value.trim()) && Number(minutes.value) >= 1 && dirty.value)

function minutesOf(beat: HeartbeatRecord): number {
  return Math.max(1, Math.round(beat.intervalSeconds / 60))
}

function statusOf(beat: HeartbeatRecord): string {
  return beat.paused ? t('heartbeat.paused') : t('heartbeat.every', { minutes: minutesOf(beat) })
}

function timeOf(unixSeconds: number): string {
  return formatAutoContinueTime(new Date(unixSeconds * 1000), currentLocale.value)
}

const scheduleText = computed(() => {
  const current = record.value
  if (!current) return ''
  const parts = current.paused
    ? [t('heartbeat.pausedHint')]
    : [statusOf(current), ...(current.nextRunAt ? [t('heartbeat.nextAt', { time: timeOf(current.nextRunAt) })] : [])]
  if (current.lastFiredAt) parts.push(t('heartbeat.lastAt', { time: timeOf(current.lastFiredAt) }))
  return parts.join(' · ')
})

/** The form shows what is saved, or a fresh default when nothing is. */
function resetForm() {
  const current = record.value
  minutes.value = current ? minutesOf(current) : 10
  prompt.value = current?.prompt ?? ''
}

async function load() {
  try {
    await refresh()
  } catch (cause) {
    // The trigger stays quiet on a failed read; an open popover says why.
    if (open.value) error.value = errorMessage(cause)
  }
}

async function toggle() {
  if (open.value) {
    close(false)
    return
  }
  open.value = true
  error.value = ''
  resetForm()
  const shown = { minutes: minutes.value, prompt: prompt.value }
  // Opening re-reads the list: the next fire moves each time a beat fires,
  // and another tab may have changed any of them. What the re-read brings
  // replaces the form only while nobody has started editing it.
  await load()
  if (!open.value) return
  if (!busy.value && minutes.value === shown.minutes && prompt.value === shown.prompt) resetForm()
  await nextTick()
  if (!record.value) promptRef.value?.focus()
}

function close(returnFocus: boolean) {
  if (!open.value) return
  open.value = false
  if (returnFocus) triggerRef.value?.focus()
}

async function run(action: () => Promise<unknown>) {
  busy.value = true
  error.value = ''
  try {
    await action()
  } catch (cause) {
    error.value = errorMessage(cause)
  } finally {
    busy.value = false
  }
}

function submit() {
  const sessionId = props.sessionId
  return run(async () => {
    await save({
      sessionId,
      intervalSeconds: intervalSeconds.value,
      prompt: prompt.value.trim(),
      // Saving an edit keeps the beat as it was: running stays running,
      // paused stays paused. A new one starts running.
      paused: Boolean(record.value?.paused),
    })
    if (props.sessionId === sessionId) resetForm()
  })
}

/** Pause or resume a beat with what is saved, leaving unsaved edits alone. */
function setRunning(beat: HeartbeatRecord, on: boolean) {
  return run(() => save({
    sessionId: beat.sessionId,
    intervalSeconds: beat.intervalSeconds,
    prompt: beat.prompt,
    paused: !on,
  }))
}

function remove() {
  const sessionId = props.sessionId
  return run(async () => {
    await clear(sessionId)
    if (props.sessionId === sessionId) resetForm()
  })
}

function openConversation(sessionId: string) {
  void router.push({ path: '/', query: { ...route.query, session: sessionId } })
}

function onDocPointerDown(event: PointerEvent) {
  if (open.value && !rootRef.value?.contains(event.target as Node)) close(false)
}

// Escape is heard on the document: a save disables the button that had focus,
// which drops focus to the page body, outside the popover.
function onDocKeyDown(event: KeyboardEvent) {
  if (open.value && event.key === 'Escape') close(true)
}

// Another conversation opened — from the drawer, or from the list above —
// brings its own heartbeat into the form; an open popover stays open on it.
watch(() => props.sessionId, () => {
  error.value = ''
  resetForm()
})

onMounted(() => {
  document.addEventListener('pointerdown', onDocPointerDown)
  document.addEventListener('keydown', onDocKeyDown)
  void load()
})

onUnmounted(() => {
  document.removeEventListener('pointerdown', onDocPointerDown)
  document.removeEventListener('keydown', onDocKeyDown)
})
</script>

<style scoped>
.heartbeat-trigger {
  display: inline-flex;
  height: 28px;
  align-items: center;
  gap: 6px;
  border-radius: 8px;
  border: 1px solid var(--forebrain-divider);
  padding: 0 10px;
  font-size: 12px;
  color: var(--forebrain-text-2);
  white-space: nowrap;
}
.heartbeat-trigger:hover {
  background: var(--forebrain-button-alt-bg);
}
.heartbeat-trigger--running {
  border-color: var(--forebrain-brand-border);
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}
.heartbeat-trigger--running .heartbeat-trigger-ic {
  animation: heartbeat-beat 2.4s ease-in-out infinite;
}
.heartbeat-trigger-meta {
  color: var(--forebrain-muted-text);
}
.heartbeat-trigger--running .heartbeat-trigger-meta {
  color: inherit;
  opacity: 0.8;
}
@media (max-width: 639px) {
  .heartbeat-trigger-meta {
    display: none;
  }
}
@keyframes heartbeat-beat {
  0%, 70%, 100% { transform: scale(1); }
  80% { transform: scale(1.25); }
  90% { transform: scale(0.95); }
}
@media (prefers-reduced-motion: reduce) {
  .heartbeat-trigger--running .heartbeat-trigger-ic {
    animation: none;
  }
}
.heartbeat-pop {
  position: absolute;
  top: calc(100% + 8px);
  right: 0;
  z-index: 30;
  width: 340px;
  max-width: calc(100vw - 32px);
  padding: 14px;
  background: var(--forebrain-surface);
  border: 1px solid var(--forebrain-divider);
  border-radius: 12px;
  box-shadow: var(--forebrain-shadow-pop);
}
.heartbeat-schedule {
  margin-top: 8px;
  border-radius: 8px;
  background: var(--forebrain-button-alt-bg);
  padding: 6px 8px;
  font-size: 11px;
  color: var(--forebrain-text-2);
}
.heartbeat-label {
  display: block;
  margin: 10px 0 4px;
  font-size: 11px;
  font-weight: 500;
  color: var(--forebrain-text-2);
}
.heartbeat-others {
  margin-top: 14px;
  border-top: 1px solid var(--forebrain-divider);
  padding-top: 10px;
}
.heartbeat-others-title {
  font-size: 11px;
  font-weight: 500;
  color: var(--forebrain-muted-text);
}
.heartbeat-other {
  display: flex;
  align-items: center;
  gap: 6px;
}
.heartbeat-other-open {
  display: flex;
  min-width: 0;
  flex: 1;
  align-items: center;
  gap: 6px;
  border-radius: 6px;
  padding: 4px 6px;
  text-align: left;
  font-size: 12px;
  color: var(--forebrain-text);
}
.heartbeat-other-open:hover {
  background: var(--forebrain-button-alt-bg);
}
.heartbeat-other-status {
  flex: none;
  font-size: 11px;
  color: var(--forebrain-muted-text);
}
.heartbeat-other-action {
  display: inline-flex;
  flex: none;
  height: 22px;
  width: 22px;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  color: var(--forebrain-muted-text);
}
.heartbeat-other-action:hover:not(:disabled) {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
}
.heartbeat-other-action:disabled {
  opacity: 0.5;
}
</style>
