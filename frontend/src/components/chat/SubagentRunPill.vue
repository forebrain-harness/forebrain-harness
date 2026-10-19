<template>
  <div v-if="percent != null || model" class="subagent-run-host">
    <button
      type="button"
      class="subagent-run-pill"
      :class="{ 'is-warn': warn }"
      data-testid="subagent-run-pill"
      aria-haspopup="dialog"
      :aria-expanded="open"
      :aria-label="t('chat.subagentRunPillAria')"
      v-tip:top="t('chat.subagentRunPillTip')"
      @click="open = !open"
    >
      <template v-if="percent != null">
        <svg class="subagent-run-ring is-small" viewBox="0 0 18 18" aria-hidden="true">
          <circle cx="9" cy="9" r="7" fill="none" stroke="var(--forebrain-divider-strong)" stroke-width="2.2" />
          <circle
            cx="9" cy="9" r="7" fill="none"
            :stroke="warn ? 'var(--forebrain-warning)' : 'var(--forebrain-brand-1)'"
            stroke-width="2.2"
            stroke-linecap="round"
            :stroke-dasharray="`${(circumference * percent / 100).toFixed(1)} ${circumference.toFixed(1)}`"
            transform="rotate(-90 9 9)"
          />
        </svg>
        <span class="subagent-run-pc" data-testid="subagent-budget">{{ percent }}%</span>
      </template>
      <span v-if="percent != null && model" class="subagent-run-dv" aria-hidden="true" />
      <template v-if="model">
        <span class="subagent-run-model">{{ model }}</span>
        <span v-if="effort" class="subagent-run-effort">· {{ effort }}</span>
      </template>
    </button>

    <!-- The whole reading, above the button and right-aligned: the ring and the
         percentage, the window it is measured against, the meter, and the four
         facts the spawn fixed. -->
    <div
      v-if="open"
      ref="cardRef"
      class="subagent-run-card"
      role="dialog"
      data-testid="subagent-run-card"
      :aria-label="t('chat.subagentRunPillAria')"
    >
      <div v-if="percent != null" class="subagent-run-head">
        <svg class="subagent-run-ring is-large" viewBox="0 0 18 18" aria-hidden="true">
          <circle cx="9" cy="9" r="7" fill="none" stroke="var(--forebrain-divider-strong)" stroke-width="2.2" />
          <circle
            cx="9" cy="9" r="7" fill="none"
            :stroke="warn ? 'var(--forebrain-warning)' : 'var(--forebrain-brand-1)'"
            stroke-width="2.2"
            stroke-linecap="round"
            :stroke-dasharray="`${(circumference * percent / 100).toFixed(1)} ${circumference.toFixed(1)}`"
            transform="rotate(-90 9 9)"
          />
        </svg>
        <div class="subagent-run-head-col">
          <b class="subagent-run-big" :class="{ 'is-warn': warn }">{{ percent }}%</b>
          <span class="subagent-run-used">{{ t('chat.subagentCtxUsed', { used, window }) }}</span>
        </div>
      </div>
      <div v-if="percent != null" class="subagent-run-meter" :class="{ 'is-warn': warn }" aria-hidden="true">
        <i :style="{ width: `${percent}%` }" />
      </div>
      <div v-if="warn" class="subagent-run-near" data-testid="subagent-run-near-full">
        <TriangleAlert aria-hidden="true" />
        <span>{{ t('chat.subagentCtxNearFull') }}</span>
      </div>
      <dl class="subagent-run-kv">
        <template v-if="modelLine">
          <dt>{{ t('chat.subagentRunModel') }}</dt>
          <dd class="is-mono">{{ modelLine }}</dd>
        </template>
        <template v-if="effort">
          <dt>{{ t('chat.subagentRunEffort') }}</dt>
          <dd>{{ effort }}</dd>
        </template>
        <template v-if="window">
          <dt>{{ t('chat.subagentRunWindow') }}</dt>
          <dd class="is-mono" data-testid="subagent-context-window">{{ window }}</dd>
        </template>
        <template v-if="usage">
          <dt>{{ t('chat.subagentRunUsage') }}</dt>
          <dd class="is-mono">{{ usage }}</dd>
        </template>
      </dl>
      <div class="subagent-run-foot">{{ t('chat.subagentRunFixed') }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * A subagent's model and context, as one quiet pill in its tab's composer bar —
 * the ring with its percentage always written, then the model and the effort —
 * and the whole reading in a card above it. Everything here is read-only: the
 * spawn fixed the model and the effort, so the pill carries no caret and the
 * card carries no control.
 */
import { computed, onUnmounted, ref, watch } from 'vue'
import { TriangleAlert } from 'lucide-vue-next'
import type { SubagentTranscript } from '@/composables/useChatStream'
import { formatTokenCount } from '@/lib/forebrainGatewayRuntime'
import { effortLevelLabel } from '@/lib/effortLabel'
import { useI18n } from '@/locales'

const props = defineProps<{ record: SubagentTranscript }>()

const { t } = useI18n()
const open = ref(false)
const cardRef = ref<HTMLElement | null>(null)
const circumference = 2 * Math.PI * 7

/** How much of its own window is used; absent when the budget never arrived. */
const percent = computed(() => {
  const left = props.record.tokenBudget?.percentLeft
  if (typeof left !== 'number' || !Number.isFinite(left)) return null
  return Math.round(100 - left)
})

const used = computed(() => formatTokenCount(props.record.tokenBudget?.tokenUsage))
const window = computed(() => formatTokenCount(props.record.tokenBudget?.contextWindow))
const model = computed(() => String(props.record.model ?? '').trim())
const provider = computed(() => String(props.record.modelProvider ?? '').trim())
/** The effort only when the spawn reported one: an empty field is unknown, not "default". */
const effort = computed(() => {
  const value = String(props.record.reasoningEffort ?? '').trim()
  return value ? effortLevelLabel(value) : ''
})
const modelLine = computed(() => (provider.value ? `${provider.value}/${model.value}` : model.value))
const usage = computed(() => {
  const input = formatTokenCount(props.record.inputTokens)
  const output = formatTokenCount(props.record.outputTokens)
  if (!input && !output) return ''
  return t('chat.subagentRunUsageValue', { input: input || '0', output: output || '0' })
})
const warn = computed(() => percent.value != null && percent.value >= 80)

function onDocPointerDown(event: PointerEvent) {
  if (!open.value) return
  const target = event.target as Node | null
  if (target && cardRef.value && !cardRef.value.contains(target) && !(target as HTMLElement).closest?.('.subagent-run-pill')) {
    open.value = false
  }
}

// Escape closes the card and nothing else: preventing the default here is what
// tells the drawer's own Escape handler that this key was already spent.
function onDocKeydown(event: KeyboardEvent) {
  if (!open.value || event.key !== 'Escape') return
  open.value = false
  event.preventDefault()
}

// The listeners exist only while the card does, so a closed pill never eats a
// click or a key meant for the composer behind it.
watch(open, (isOpen) => {
  if (typeof document === 'undefined') return
  if (isOpen) {
    document.addEventListener('pointerdown', onDocPointerDown)
    document.addEventListener('keydown', onDocKeydown)
  } else {
    document.removeEventListener('pointerdown', onDocPointerDown)
    document.removeEventListener('keydown', onDocKeydown)
  }
})
onUnmounted(() => {
  if (typeof document === 'undefined') return
  document.removeEventListener('pointerdown', onDocPointerDown)
  document.removeEventListener('keydown', onDocKeydown)
})
</script>

<style scoped>
/* No box of its own: the pill and its card are the composer bar's own items,
   and the card positions itself against the composer (which is relative). */
.subagent-run-host {
  display: contents;
}

.subagent-run-pill {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 28px;
  min-width: 0;
  padding: 0 9px 0 7px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 999px;
  background: var(--forebrain-surface);
  color: var(--forebrain-text-2);
  font-size: 12px;
  cursor: pointer;
}

.subagent-run-pill:hover,
.subagent-run-pill[aria-expanded='true'] {
  background: var(--forebrain-bg-alt);
  border-color: var(--forebrain-divider-strong);
}

.subagent-run-ring {
  display: block;
  flex: none;
}

.subagent-run-ring.is-small {
  width: 16px;
  height: 16px;
}

.subagent-run-ring.is-large {
  width: 34px;
  height: 34px;
}

.subagent-run-pc {
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  font-variant-numeric: tabular-nums;
  color: var(--forebrain-text);
}

.subagent-run-pill.is-warn .subagent-run-pc {
  color: var(--forebrain-warning);
}

.subagent-run-dv {
  width: 1px;
  height: 12px;
  flex: none;
  background: var(--forebrain-divider-strong);
}

.subagent-run-model {
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  min-width: 0;
  max-width: 130px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.subagent-run-effort {
  flex: none;
  color: var(--forebrain-muted-text);
}

.subagent-run-card {
  position: absolute;
  right: 0;
  bottom: calc(100% + 6px);
  width: 300px;
  padding: 14px;
  display: grid;
  gap: 10px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 12px;
  background: var(--forebrain-surface);
  box-shadow: var(--forebrain-shadow-pop);
  text-align: left;
  z-index: 6;
}

.subagent-run-head {
  display: flex;
  align-items: center;
  gap: 12px;
}

.subagent-run-head-col {
  display: grid;
  gap: 1px;
  min-width: 0;
}

.subagent-run-big {
  font-size: 20px;
  font-weight: 600;
  line-height: 1.2;
  font-variant-numeric: tabular-nums;
  color: var(--forebrain-text);
}

.subagent-run-big.is-warn {
  color: var(--forebrain-warning);
}

.subagent-run-used {
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
  color: var(--forebrain-muted-text);
}

.subagent-run-meter {
  height: 6px;
  border-radius: 999px;
  background: var(--forebrain-divider);
  overflow: hidden;
}

.subagent-run-meter i {
  display: block;
  height: 100%;
  border-radius: 999px;
  background: var(--forebrain-brand-1);
}

.subagent-run-meter.is-warn i {
  background: var(--forebrain-warning);
}

.subagent-run-near {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 5px 8px;
  border-radius: 6px;
  font-size: 12px;
  color: var(--forebrain-warning);
  background: var(--forebrain-warning-soft);
}

.subagent-run-near svg {
  width: 13px;
  height: 13px;
  flex: none;
}

/* The prototype's .ic-kv is 76px of label, which is what its Chinese labels
   measure; a label longer than that — "Context window" in English — widens its
   own column instead of folding onto two lines, so the value stays beside the
   label it belongs to in every language. */
.subagent-run-kv {
  display: grid;
  grid-template-columns: minmax(76px, max-content) 1fr;
  gap: 6px 10px;
  margin: 0;
  font-size: 12.5px;
}

.subagent-run-kv dt {
  color: var(--forebrain-muted-text);
}

.subagent-run-kv dd {
  margin: 0;
  min-width: 0;
  overflow-wrap: anywhere;
  color: var(--forebrain-text);
}

.subagent-run-kv dd.is-mono {
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
}

.subagent-run-foot {
  border-top: 1px solid var(--forebrain-divider);
  padding-top: 9px;
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
}
</style>
