<template>
  <div
    class="compaction-card"
    :class="`is-${data.status}`"
    role="group"
    :aria-label="title"
  >
    <template v-if="data.status === 'running'">
      <div class="compaction-head">
        <span class="compaction-spinner" aria-hidden="true" />
        <span class="compaction-title">{{ title }}</span>
        <span v-if="runningDetail" class="compaction-detail">{{ runningDetail }}</span>
      </div>
      <!-- The bar has a line of its own under the header, so it can be read
           at a glance and never competes with the text for room. -->
      <div class="compaction-progress">
        <div
          class="compaction-track"
          role="progressbar"
          :aria-label="t('chat.compaction.progressAria')"
          aria-valuemin="0"
          aria-valuemax="100"
          :aria-valuenow="data.percent"
        >
          <div class="compaction-fill" :style="{ clipPath: `inset(0 ${100 - data.percent}% 0 0 round 999px)` }" />
        </div>
        <span class="compaction-percent">{{ data.percent }}%</span>
      </div>
    </template>

    <details v-else-if="data.status === 'done' && data.summary" class="compaction-done" :open="open" @toggle="onToggle">
      <summary class="compaction-head is-clickable">
        <Check class="compaction-icon" aria-hidden="true" />
        <span class="compaction-title">{{ title }}</span>
        <span v-if="doneDetail" class="compaction-detail">{{ doneDetail }}</span>
        <ChevronDown class="compaction-chevron" aria-hidden="true" />
      </summary>
      <div class="compaction-body">
        <div class="compaction-body-label">{{ t('chat.compaction.summary') }}</div>
        <MessageResponse :content="data.summary" />
        <p v-if="data.strategy === 'local'" class="compaction-warning">{{ t('chat.compaction.warning') }}</p>
      </div>
    </details>

    <template v-else>
      <div class="compaction-head">
        <Check v-if="data.status === 'done'" class="compaction-icon" aria-hidden="true" />
        <X v-else-if="data.status === 'failed'" class="compaction-icon" aria-hidden="true" />
        <CircleSlash v-else class="compaction-icon" aria-hidden="true" />
        <span class="compaction-title">{{ title }}</span>
        <span v-if="data.status === 'done' && doneDetail" class="compaction-detail">{{ doneDetail }}</span>
      </div>
      <p v-if="data.status === 'failed' && data.error" class="compaction-error">{{ data.error }}</p>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * One compaction, as a card that lives through it: a header and a progress bar
 * of its own while it runs, then the before → after result with the checkpoint
 * it left behind — or why it replaced nothing. The terminal draws the same card
 * from the same events.
 */
import { computed, ref } from 'vue'
import { Check, ChevronDown, CircleSlash, X } from 'lucide-vue-next'
import { MessageResponse } from '@repo/elements/message'
import { useI18n } from '@/locales'
import { formatTokenCount, type ForebrainCompaction } from '@/lib/forebrainGatewayRuntime'

const props = defineProps<{ data: ForebrainCompaction }>()
const { t } = useI18n()

const title = computed(() => {
  switch (props.data.status) {
    case 'running':
      return t('chat.compaction.running')
    case 'done':
      return t('chat.compaction.done')
    case 'failed':
      return t('chat.compaction.failed')
    default:
      return t('chat.compaction.cancelled')
  }
})

const runningDetail = computed(() => {
  const phase = {
    reading: t('chat.compaction.phaseReading'),
    summarizing: t('chat.compaction.phaseSummarizing'),
    saving: t('chat.compaction.phaseSaving'),
  }[props.data.phase ?? ''] ?? t('chat.compaction.phaseStarting')
  const tokens = props.data.tokensBefore
    ? t('chat.compaction.tokens', { count: formatTokenCount(props.data.tokensBefore) })
    : ''
  return [phase, tokens].filter(Boolean).join(' · ')
})

const doneDetail = computed(() => {
  const parts: string[] = []
  const before = props.data.tokensBefore ?? 0
  const after = props.data.tokensAfter ?? 0
  if (before > 0) {
    parts.push(after < before
      ? t('chat.compaction.saved', { before: formatTokenCount(before), after: formatTokenCount(after), percent: Math.floor(((before - after) * 100) / before) })
      : t('chat.compaction.unchanged', { before: formatTokenCount(before), after: formatTokenCount(after) }))
  }
  if (props.data.duration) parts.push(props.data.duration)
  return parts.join(' · ')
})

const open = ref(false)
function onToggle(event: Event) {
  open.value = (event.target as HTMLDetailsElement).open
}
</script>

<style scoped>
.compaction-card {
  --compaction-accent: #0ea5e9;
  border: 1px solid var(--forebrain-divider);
  border-radius: 12px;
  background: var(--forebrain-surface-soft);
  padding: 10px 14px;
  animation: compaction-in 0.24s ease-out;
}

.compaction-card.is-running {
  border-color: color-mix(in srgb, var(--compaction-accent) 38%, var(--forebrain-divider));
  box-shadow: 0 0 0 1px color-mix(in srgb, var(--compaction-accent) 10%, transparent),
    0 8px 24px color-mix(in srgb, var(--compaction-accent) 10%, transparent);
  padding-bottom: 12px;
}

.compaction-card.is-failed {
  --compaction-accent: #e5484d;
  border-color: color-mix(in srgb, var(--compaction-accent) 40%, var(--forebrain-divider));
}

@keyframes compaction-in {
  from {
    opacity: 0;
    transform: translateY(-4px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

.compaction-head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  column-gap: 8px;
  row-gap: 2px;
  min-width: 0;
  font-size: 13px;
  line-height: 1.5;
}

.compaction-head.is-clickable {
  cursor: pointer;
  list-style: none;
  user-select: none;
}

.compaction-head.is-clickable::-webkit-details-marker {
  display: none;
}

.compaction-title {
  flex-shrink: 0;
  font-weight: 650;
  color: var(--forebrain-text);
}

.is-running .compaction-title {
  color: var(--compaction-accent);
}

/* The detail wraps under the title when the column is narrow; it is never
   cut short. */
.compaction-detail {
  min-width: 0;
  overflow-wrap: anywhere;
  color: var(--forebrain-text-2);
  font-variant-numeric: tabular-nums;
}

.compaction-icon {
  width: 15px;
  height: 15px;
  flex-shrink: 0;
  stroke-width: 2.4;
}

.is-done .compaction-icon {
  color: #16a34a;
}

.is-failed .compaction-icon {
  color: var(--compaction-accent);
}

.is-cancelled .compaction-icon {
  color: var(--forebrain-muted-text);
}

.compaction-chevron {
  width: 14px;
  height: 14px;
  margin-left: auto;
  flex-shrink: 0;
  color: var(--forebrain-muted-text);
  transition: transform 0.2s ease;
}

details[open] .compaction-chevron {
  transform: rotate(180deg);
}

.compaction-spinner {
  width: 14px;
  height: 14px;
  flex-shrink: 0;
  border-radius: 999px;
  border: 2px solid color-mix(in srgb, var(--compaction-accent) 22%, transparent);
  border-top-color: var(--compaction-accent);
  animation: compaction-spin 0.8s linear infinite;
}

@keyframes compaction-spin {
  to {
    transform: rotate(360deg);
  }
}

.compaction-progress {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 10px;
}

.compaction-track {
  position: relative;
  flex: 1;
  height: 8px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--compaction-accent) 13%, var(--forebrain-surface));
  box-shadow: inset 0 1px 2px rgba(9, 52, 74, 0.12);
  overflow: hidden;
}

/* The fill is the whole gradient, revealed from the left as the compaction
   advances, so each colour stays where it sits on the bar. */
.compaction-fill {
  position: absolute;
  inset: 0;
  border-radius: 999px;
  background: linear-gradient(90deg, #1d4ed8 0%, #2563eb 22%, #0ea5e9 58%, #22d3ee 100%);
  transition: clip-path 0.45s cubic-bezier(0.22, 1, 0.36, 1);
}

/* A highlight sweeping through the fill keeps the bar visibly alive while the
   percentage holds. */
.compaction-fill::after {
  content: '';
  position: absolute;
  inset: 0;
  background: linear-gradient(100deg, transparent 20%, rgba(255, 255, 255, 0.55) 50%, transparent 80%);
  transform: translateX(-100%);
  animation: compaction-sweep 1.8s cubic-bezier(0.4, 0, 0.2, 1) infinite;
}

@keyframes compaction-sweep {
  to {
    transform: translateX(100%);
  }
}

.compaction-percent {
  min-width: 4ch;
  text-align: right;
  font-size: 14px;
  font-weight: 700;
  font-variant-numeric: tabular-nums;
  color: var(--compaction-accent);
}

.compaction-body {
  margin-top: 10px;
  padding-top: 10px;
  border-top: 1px solid var(--forebrain-divider);
  font-size: 13px;
}

.compaction-body-label {
  margin-bottom: 6px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--forebrain-muted-text);
}

.compaction-warning {
  margin-top: 10px;
  font-size: 12px;
  line-height: 1.5;
  color: #b7791f;
}

.compaction-error {
  margin-top: 6px;
  padding-left: 23px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--forebrain-text-2);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

@media (prefers-reduced-motion: reduce) {
  .compaction-card,
  .compaction-spinner,
  .compaction-fill::after {
    animation: none;
  }
  .compaction-fill {
    transition: none;
  }
}
</style>
