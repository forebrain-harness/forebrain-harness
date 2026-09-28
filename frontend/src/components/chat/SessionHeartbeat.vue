<template>
  <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-glass)] p-3">
    <div class="mb-2 flex items-center justify-between gap-2">
      <div class="text-sm font-medium text-[var(--forebrain-text)]">{{ t('heartbeat.title') }}</div>
      <span v-if="current" class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[11px] text-[var(--forebrain-muted-text)]">
        {{ current.paused ? t('heartbeat.paused') : t('heartbeat.every', { minutes: Math.round(current.intervalSeconds / 60) }) }}
      </span>
    </div>
    <p class="mb-2 text-[11px] leading-relaxed text-[var(--forebrain-muted-text)]">{{ t('heartbeat.description') }}</p>

    <p v-if="!sessionId" class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('heartbeat.needsSession') }}</p>
    <template v-else>
      <div class="flex items-center gap-2">
        <input v-model.number="minutes" type="number" min="1" class="forebrain-field w-20" :aria-label="t('heartbeat.minutes')" />
        <span class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('heartbeat.minutes') }}</span>
      </div>
      <textarea v-model="prompt" rows="2" :placeholder="t('heartbeat.promptPlaceholder')" class="forebrain-field mt-2 w-full" />
      <div class="mt-2 flex flex-wrap gap-2">
        <button type="button" class="forebrain-btn forebrain-btn-primary h-7 px-3 text-[11px]" :disabled="busy || !prompt.trim()" @click="save(false)">
          {{ current ? t('heartbeat.update') : t('heartbeat.start') }}
        </button>
        <button v-if="current && !current.paused" type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-3 text-[11px]" :disabled="busy" @click="save(true)">
          {{ t('heartbeat.pause') }}
        </button>
        <button v-if="current?.paused" type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-3 text-[11px]" :disabled="busy" @click="save(false)">
          {{ t('heartbeat.resume') }}
        </button>
        <button v-if="current" type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-3 text-[11px] text-[var(--forebrain-danger)]" :disabled="busy" @click="clear">
          {{ t('heartbeat.clear') }}
        </button>
      </div>
      <p v-if="error" class="mt-2 text-[11px] text-[var(--forebrain-danger)]">{{ error }}</p>
    </template>
  </section>
</template>

<script setup lang="ts">
/**
 * The recurring instruction for this conversation. Unlike a cron job it has no
 * session of its own: it fires back into this thread when it has been idle for
 * the interval, so it sees everything the conversation has built up.
 */
import { ref, watch } from 'vue'
import { getErrorMessage, forebrainApi, type HeartbeatRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const props = defineProps<{ sessionId: string | null }>()

const { t } = useI18n()
const current = ref<HeartbeatRecord | null>(null)
const minutes = ref(10)
const prompt = ref('')
const busy = ref(false)
const error = ref('')

async function load() {
  const sid = String(props.sessionId ?? '').trim()
  current.value = null
  if (!sid) return
  try {
    const hb = await forebrainApi.heartbeat(sid)
    current.value = hb
    if (hb) {
      minutes.value = Math.max(1, Math.round(hb.intervalSeconds / 60))
      prompt.value = hb.prompt
    }
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

async function save(paused: boolean) {
  const sid = String(props.sessionId ?? '').trim()
  if (!sid) return
  busy.value = true
  error.value = ''
  try {
    current.value = await forebrainApi.saveHeartbeat({
      sessionId: sid,
      intervalSeconds: Math.max(1, Number(minutes.value) || 1) * 60,
      prompt: prompt.value.trim(),
      paused,
    })
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    busy.value = false
  }
}

async function clear() {
  const sid = String(props.sessionId ?? '').trim()
  if (!sid) return
  busy.value = true
  error.value = ''
  try {
    await forebrainApi.clearHeartbeat(sid)
    current.value = null
    prompt.value = ''
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    busy.value = false
  }
}

watch(() => props.sessionId, load, { immediate: true })
</script>
