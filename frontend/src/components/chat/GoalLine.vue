<template>
  <div class="goal-line" :class="`is-${tone}`" role="group" :aria-label="title">
    <div class="goal-head">
      <component :is="icon" class="goal-icon" aria-hidden="true" />
      <span class="goal-title">{{ title }}</span>
      <span v-if="detail" class="goal-detail">{{ detail }}</span>
      <button
        v-if="goal.checkAgentId"
        type="button"
        class="goal-check"
        @click="emit('open-check', goal.checkAgentId)"
      >
        {{ t('chat.goal.openCheck') }}
      </button>
    </div>
    <p v-if="body" class="goal-body">{{ body }}</p>
  </div>
</template>

<script setup lang="ts">
/**
 * One line of a /goal, drawn from the same events and rows the terminal draws
 * it from: the objective it opened with, each continuation round with the
 * check's reason for it, and how it ended. A round or an end the check decided
 * opens that check's view, where what it read to decide is.
 */
import { computed, type Component } from 'vue'
import { Check, CircleSlash, CircleStop, RefreshCw, Target, X } from 'lucide-vue-next'
import { useI18n } from '@/locales'
import type { ForebrainGoalLine } from '@/lib/forebrainGatewayRuntime'

const props = defineProps<{ goal: ForebrainGoalLine }>()
const emit = defineEmits<{ (e: 'open-check', agentId: string): void }>()
const { t } = useI18n()

type Tone = 'progress' | 'done' | 'stopped' | 'muted' | 'failed'

const round = computed(() => Math.max(props.goal.round ?? props.goal.rounds ?? 1, 1))

const tone = computed<Tone>(() => {
  if (props.goal.phase !== 'completed') return 'progress'
  switch (props.goal.status) {
    case 'done':
      return 'done'
    case 'stuck':
    case 'capped':
      return 'stopped'
    case 'interrupted':
      return 'muted'
    default:
      return 'failed'
  }
})

const icon = computed<Component>(() => {
  if (props.goal.phase === 'started') return Target
  if (props.goal.phase === 'round') return RefreshCw
  return { done: Check, stopped: CircleStop, muted: CircleSlash, failed: X, progress: RefreshCw }[tone.value]
})

const title = computed(() => {
  if (props.goal.phase === 'started') return t('chat.goal.started')
  if (props.goal.phase === 'round') return t('chat.goal.round', { round: round.value })
  switch (props.goal.status) {
    case 'done':
      return t('chat.goal.done')
    case 'stuck':
      return t('chat.goal.stuck')
    case 'capped':
      return t('chat.goal.capped')
    case 'interrupted':
      return t('chat.goal.interrupted')
    default:
      return t('chat.goal.failed')
  }
})

const detail = computed(() => {
  if (props.goal.phase === 'started') return t('chat.goal.startedDetail')
  if (props.goal.phase === 'round') return t('chat.goal.roundDetail')
  const rounds = Math.max(props.goal.rounds ?? 1, 1)
  if (props.goal.status === 'interrupted') return t('chat.goal.interruptedDetail', { round: rounds })
  if (tone.value === 'failed') {
    return props.goal.why ? t('chat.goal.failedDetail', { round: rounds }) : t('chat.goal.roundFailedDetail', { round: rounds })
  }
  const duration = formatGoalDuration(props.goal.durationMs ?? 0)
  return rounds === 1 ? t('chat.goal.endedDetailOne', { duration }) : t('chat.goal.endedDetail', { rounds, duration })
})

const body = computed(() => (props.goal.phase === 'started' ? props.goal.objective : props.goal.why)?.trim() ?? '')

/** A goal's duration the way the working line counts it: 7s, 1m 12s, 1h 02m 05s. */
function formatGoalDuration(ms: number): string {
  const total = Math.max(Math.floor(ms / 1000), 0)
  if (total < 60) return `${total}s`
  const seconds = String(total % 60).padStart(2, '0')
  const minutes = Math.floor(total / 60)
  if (minutes < 60) return `${minutes}m ${seconds}s`
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, '0')}m ${seconds}s`
}
</script>

<style scoped>
.goal-line {
  --goal-accent: var(--forebrain-warning);
  border-left: 3px solid var(--goal-accent);
  border-radius: 0 10px 10px 0;
  background: color-mix(in srgb, var(--goal-accent) 6%, var(--forebrain-surface));
  padding: 8px 12px;
}

.goal-line.is-done {
  --goal-accent: var(--forebrain-success);
}

.goal-line.is-failed {
  --goal-accent: var(--forebrain-danger);
}

.goal-line.is-muted {
  --goal-accent: var(--forebrain-muted-text);
}

.goal-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  column-gap: 8px;
  row-gap: 2px;
  min-width: 0;
  font-size: 13px;
  line-height: 1.5;
}

.goal-icon {
  width: 15px;
  height: 15px;
  flex-shrink: 0;
  stroke-width: 2.4;
  color: var(--goal-accent);
}

.goal-title {
  flex-shrink: 0;
  font-weight: 650;
  color: var(--forebrain-text);
}

/* The detail wraps under the title when the column is narrow; it is never
   cut short. */
.goal-detail {
  min-width: 0;
  overflow-wrap: anywhere;
  color: var(--forebrain-text-2);
  font-variant-numeric: tabular-nums;
}

.goal-check {
  margin-left: auto;
  flex-shrink: 0;
  border-radius: 6px;
  padding: 1px 8px;
  font-size: 12px;
  color: var(--goal-accent);
  border: 1px solid color-mix(in srgb, var(--goal-accent) 35%, transparent);
  background: transparent;
  transition: background 0.15s ease;
}

.goal-check:hover {
  background: color-mix(in srgb, var(--goal-accent) 10%, transparent);
}

.goal-body {
  margin: 4px 0 0 23px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font-size: 13px;
  line-height: 1.6;
  color: var(--forebrain-text);
}
</style>
