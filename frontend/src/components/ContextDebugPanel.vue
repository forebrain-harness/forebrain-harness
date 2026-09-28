<template>
  <section class="context-debug-shell">
    <div class="context-debug-panel">
      <header class="context-debug-header">
        <div class="min-w-0">
          <div class="flex flex-wrap items-center gap-2">
            <span class="context-debug-eyebrow">{{ t('chat.contextDebug') }}</span>
            <span class="context-debug-signal" :class="signalClass">
              <component :is="signalIcon" class="size-3.5" aria-hidden="true" />
              {{ signalLabel }}
            </span>
          </div>
          <div class="mt-1 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-[var(--forebrain-muted-text)]">
            <span class="min-w-0 truncate">{{ t('chat.contextMode') }} {{ view.mode }}</span>
            <span>{{ t('chat.contextEstimate') }} {{ compactNumber(view.estimate) }}</span>
            <span>{{ t('chat.contextRemaining') }} {{ compactNumber(view.remaining) }}</span>
          </div>
        </div>
        <button
          type="button"
          class="context-debug-refresh"
          :disabled="loading"
          :title="t('common.refresh')"
          @click="$emit('refresh')"
        >
          <RefreshCw class="size-3.5" :class="{ 'animate-spin': loading }" aria-hidden="true" />
          <span>{{ loading ? t('common.loading') : t('common.refresh') }}</span>
        </button>
      </header>

      <div v-if="error" class="p-3">
        <div class="context-debug-error">
          <CircleAlert class="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          <span class="min-w-0 break-words">{{ error }}</span>
        </div>
      </div>

      <div v-else-if="!data" class="context-debug-empty">
        <Gauge class="size-5 text-[var(--forebrain-muted-text)]" aria-hidden="true" />
        <span>{{ t('chat.noContextDebug') }}</span>
      </div>

      <template v-else>
        <section class="context-debug-budget">
          <div class="min-w-0">
            <div class="text-[10px] font-semibold uppercase text-[var(--forebrain-muted-text)]">{{ t('chat.contextBudget') }}</div>
            <div class="mt-1 flex items-end gap-2">
              <div class="text-[30px] font-semibold leading-none text-[var(--forebrain-text)]">{{ usageLabel }}</div>
              <div class="pb-0.5 text-[11px] text-[var(--forebrain-muted-text)]">{{ t('chat.contextOfWindow') }}</div>
            </div>
          </div>
          <div class="context-debug-budget-grid">
            <MetricCell :label="t('chat.contextEstimate')" :value="compactNumber(view.estimate)" tone="accent" />
            <MetricCell :label="t('chat.contextWindow')" :value="compactNumber(view.window)" />
            <MetricCell :label="t('chat.contextAvailableWindow')" :value="compactNumber(view.availableWindow)" />
            <MetricCell :label="t('chat.contextReserve')" :value="compactNumber(view.reserve)" />
          </div>
        </section>

        <section class="context-debug-track" aria-label="context budget usage">
          <div class="context-debug-track-rail">
            <div class="context-debug-track-fill" :class="usageBarClass" :style="{ width: usageWidth }" />
            <span
              v-if="view.warningRatio != null"
              class="context-debug-threshold context-debug-threshold-warn"
              :style="{ left: `${view.warningRatio * 100}%` }"
            />
            <span
              v-if="view.blockingRatio != null"
              class="context-debug-threshold context-debug-threshold-block"
              :style="{ left: `${view.blockingRatio * 100}%` }"
            />
          </div>
          <div class="context-debug-track-labels">
            <span>{{ t('chat.contextConversationTokens') }} {{ compactNumber(view.conversationTokens) }}</span>
            <span>{{ t('chat.contextRunId') }} {{ data.runId || t('common.none') }}</span>
          </div>
        </section>

        <section class="context-debug-metrics">
          <MetricCell :label="t('chat.contextItems')" :value="formatNumber(view.itemCount)" tone="accent" />
          <MetricCell :label="t('chat.contextEvictions')" :value="formatNumber(view.evictionCount)" :tone="view.evictionCount > 0 ? 'warn' : 'neutral'" />
          <MetricCell :label="t('chat.contextRecoveries')" :value="formatNumber(view.recoveryCount)" :tone="view.recoveryCount > 0 ? 'warn' : 'neutral'" />
          <MetricCell :label="t('chat.contextBoundaries')" :value="formatNumber(view.boundaryCount)" />
          <MetricCell :label="t('chat.contextSpills')" :value="formatNumber(view.spillCount)" />
        </section>
      </template>
    </div>

    <template v-if="data">
      <div class="context-debug-grid">
        <section class="context-debug-section">
          <div class="context-debug-section-head">
            <div>
              <div class="context-debug-section-title">{{ t('chat.contextWorkingSet') }}</div>
              <div class="context-debug-section-subtitle">{{ t('chat.contextWorkingSetHint') }}</div>
            </div>
            <span class="context-debug-count">{{ workingSet.length }}</span>
          </div>
          <div v-if="workingSet.length" class="context-debug-chip-row">
            <span v-for="entry in workingSet" :key="entry" class="context-debug-chip">
              <FileText class="size-3.5 shrink-0" aria-hidden="true" />
              <span class="truncate">{{ entry }}</span>
            </span>
          </div>
          <div v-else class="context-debug-muted">{{ t('common.empty') }}</div>
        </section>

        <section class="context-debug-section">
          <div class="context-debug-section-head">
            <div>
              <div class="context-debug-section-title">{{ t('chat.contextRecoveryTimeline') }}</div>
              <div class="context-debug-section-subtitle">{{ t('chat.contextRecoveryHint') }}</div>
            </div>
            <span class="context-debug-count">{{ recoveryEvidence.length }}</span>
          </div>
          <ul v-if="recoveryEvidence.length" class="context-debug-timeline">
            <li v-for="(item, idx) in recoveryEvidence" :key="`${item.trigger}-${idx}`" class="context-debug-timeline-item">
              <span class="context-debug-timeline-dot" :class="recoveryToneClass(item.tone)" />
              <div class="min-w-0 flex-1">
                <div class="flex min-w-0 items-center justify-between gap-3">
                  <span class="truncate text-[12px] font-semibold text-[var(--forebrain-text)]">{{ item.trigger }}</span>
                  <span class="shrink-0 text-[11px] text-[var(--forebrain-muted-text)]">
                    {{ item.replacedItems ?? 0 }} {{ t('chat.contextItemsShort') }}
                  </span>
                </div>
                <div v-if="item.reason" class="mt-1 text-[12px] leading-snug text-[var(--forebrain-text-2)]">{{ item.reason }}</div>
                <div v-if="item.summary" class="mt-1 line-clamp-2 text-[11px] leading-snug text-[var(--forebrain-muted-text)]">{{ item.summary }}</div>
                <div v-if="item.boundaryId || item.spilledPath" class="mt-2 flex flex-wrap gap-1.5">
                  <span v-if="item.boundaryId" class="context-debug-code-pill">{{ t('chat.contextBoundaryId') }} {{ item.boundaryId }}</span>
                  <span v-if="item.spilledPath" class="context-debug-code-pill">{{ t('chat.contextSpillPath') }} {{ item.spilledPath }}</span>
                </div>
              </div>
            </li>
          </ul>
          <div v-else class="context-debug-muted">{{ t('common.empty') }}</div>
        </section>
      </div>

      <section class="context-debug-section context-debug-spill-ledger">
        <div class="context-debug-section-head">
          <div>
            <div class="context-debug-section-title">{{ t('chat.contextToolOutputLedger') }}</div>
            <div class="context-debug-section-subtitle">{{ t('chat.contextToolOutputLedgerHint') }}</div>
          </div>
          <span class="context-debug-count">{{ spillEvidence.length }}</span>
        </div>
        <div v-if="spillEvidence.length" class="context-debug-spill-list">
          <article v-for="(item, idx) in spillEvidence" :key="`${item.path}-${idx}`" class="context-debug-spill-row">
            <div class="context-debug-spill-main">
              <div class="min-w-0">
                <div class="flex min-w-0 flex-wrap items-center gap-1.5">
                  <span class="truncate text-[12px] font-semibold text-[var(--forebrain-text)]">{{ item.toolName }}</span>
                  <span v-if="item.callId" class="context-debug-mini-pill">{{ item.callId }}</span>
                </div>
                <div class="mt-1 min-w-0 truncate font-mono text-[11px] text-[var(--forebrain-muted-text)]" :title="item.path">
                  {{ item.pathLabel || item.path }}
                </div>
              </div>
              <div class="context-debug-spill-stats">
                <span>{{ item.bytesLabel }}</span>
                <span>{{ item.linesLabel }}</span>
                <span>{{ item.omittedLabel }}</span>
              </div>
            </div>
            <div class="context-debug-spill-meta">
              <span class="truncate">{{ item.path }}</span>
              <span v-if="item.createdAtLabel" class="shrink-0">{{ item.createdAtLabel }}</span>
            </div>
          </article>
        </div>
        <div v-else class="context-debug-muted">{{ t('common.empty') }}</div>
      </section>

      <section class="context-debug-section">
        <div class="context-debug-section-head">
          <div>
            <div class="context-debug-section-title">{{ t('chat.contextTimeline') }}</div>
            <div class="context-debug-section-subtitle">{{ t('chat.contextTimelineHint') }}</div>
          </div>
          <span class="context-debug-count">{{ timelineEvidence.length }}</span>
        </div>
        <ul v-if="timelineEvidence.length" class="context-debug-timeline">
          <li v-for="(item, idx) in timelineEvidence" :key="`${item.kind}-${idx}`" class="context-debug-timeline-item">
            <span class="context-debug-timeline-dot" :class="recoveryToneClass(item.tone)" />
            <div class="min-w-0 flex-1">
              <div class="truncate text-[12px] font-semibold text-[var(--forebrain-text)]">{{ item.title }}</div>
              <div v-if="item.detail" class="mt-1 text-[12px] leading-snug text-[var(--forebrain-text-2)]">{{ item.detail }}</div>
              <div v-if="item.meta" class="mt-1 truncate text-[11px] text-[var(--forebrain-muted-text)]">{{ item.meta }}</div>
            </div>
          </li>
        </ul>
        <div v-else class="context-debug-muted">{{ t('common.empty') }}</div>
      </section>

      <section class="context-debug-section">
        <div class="context-debug-section-head">
          <div>
            <div class="context-debug-section-title">Compact checkpoints</div>
            <div class="context-debug-section-subtitle">compact window and active boundary</div>
          </div>
        </div>
        <div class="context-debug-metrics">
          <MetricCell label="window number" :value="formatNumber(view.windowNumber)" tone="accent" />
          <MetricCell label="boundary" :value="view.activeBoundaryId || t('common.none')" />
          <MetricCell label="diffs" :value="formatNumber(view.compactDiffCount)" />
        </div>
      </section>

      <section v-if="tokenAttribution" class="context-debug-section">
        <div class="context-debug-section-head">
          <div>
            <div class="context-debug-section-title">Token attribution</div>
            <div class="context-debug-section-subtitle">checkpoint, tool references, and freed context</div>
          </div>
        </div>
        <div class="context-debug-metrics">
          <MetricCell label="checkpoint" :value="formatNumber(tokenAttribution.checkpointTokens)" />
          <MetricCell label="tool references" :value="formatNumber(tokenAttribution.toolReferenceTokens)" />
          <MetricCell label="freed" :value="formatNumber(tokenAttribution.freedTokens)" tone="warn" />
        </div>
      </section>

      <details v-if="compactExportJson" class="context-debug-details">
        <summary class="context-debug-summary">
          <span class="inline-flex items-center gap-2">
            <FileText class="size-4 text-[var(--forebrain-brand-1)]" aria-hidden="true" />
            Compact export
          </span>
          <span class="context-debug-count">{{ view.compactAuditAvailable ? 'ready' : t('common.none') }}</span>
        </summary>
        <pre class="context-debug-content mt-3">{{ compactExportJson }}</pre>
      </details>

      <div class="context-debug-detail-grid">
        <details class="context-debug-details" open>
          <summary class="context-debug-summary">
            <span class="inline-flex items-center gap-2">
              <Layers class="size-4 text-[var(--forebrain-brand-1)]" aria-hidden="true" />
              {{ t('chat.contextInjectedItems') }}
            </span>
            <span class="context-debug-count">{{ injectedItems.length }}</span>
          </summary>
          <ul v-if="injectedItems.length" class="mt-3 space-y-2">
            <li v-for="(item, idx) in injectedItems" :key="`${item.sourceId ?? 'item'}-${idx}`" class="context-debug-item">
              <div class="context-debug-item-main">
                <div class="min-w-0">
                  <div class="truncate text-[12px] font-semibold text-[var(--forebrain-text)]">{{ item.title || item.sourceId || 'Context Item' }}</div>
                  <div class="mt-0.5 flex min-w-0 flex-wrap items-center gap-1.5 text-[11px] text-[var(--forebrain-muted-text)]">
                    <span class="min-w-0 max-w-full truncate">{{ item.sourceId || t('common.none') }}</span>
                    <span v-if="item.layer" class="context-debug-mini-pill">{{ item.layer }}</span>
                    <span v-if="item.pinned" class="context-debug-mini-pill">{{ t('chat.contextPinned') }}</span>
                  </div>
                </div>
                <span class="context-debug-token">{{ compactNumber(item.estimatedTokens) }}</span>
              </div>
              <div v-if="item.debugReason" class="mt-2 text-[11px] text-[var(--forebrain-brand-1)]">{{ item.debugReason }}</div>
              <div v-if="item.content" class="context-debug-content">{{ compactContent(item.content) }}</div>
            </li>
          </ul>
          <div v-else class="mt-3 context-debug-muted">{{ t('common.empty') }}</div>
        </details>

        <details class="context-debug-details" :open="evictedItems.length > 0">
          <summary class="context-debug-summary">
            <span class="inline-flex items-center gap-2">
              <ArchiveX class="size-4 text-[rgb(216,160,70)]" aria-hidden="true" />
              {{ t('chat.contextEvictionDetails') }}
            </span>
            <span class="context-debug-count">{{ evictedItems.length }}</span>
          </summary>
          <ul v-if="evictedItems.length" class="mt-3 space-y-2">
            <li v-for="(item, idx) in evictedItems" :key="`${item.sourceId ?? 'eviction'}-${idx}`" class="context-debug-item">
              <div class="context-debug-item-main">
                <span class="truncate text-[12px] font-semibold text-[var(--forebrain-text)]">{{ item.sourceId || 'evicted' }}</span>
                <span class="context-debug-token">{{ compactNumber(item.estimatedTokens) }}</span>
              </div>
              <div class="mt-1 text-[11px] text-[var(--forebrain-muted-text)]">{{ item.reason || t('common.none') }}</div>
            </li>
          </ul>
          <div v-else class="mt-3 context-debug-muted">{{ t('common.empty') }}</div>
        </details>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, defineComponent, h } from 'vue'
import {
  ArchiveX,
  CheckCircle2,
  CircleAlert,
  FileText,
  Gauge,
  Layers,
  RefreshCw,
  ShieldAlert,
  TriangleAlert,
} from 'lucide-vue-next'

import { useI18n } from '@/locales'
import type { SessionContextDebug } from '@/lib/api'
import {
  buildCompactExportJSON,
  buildContextDebugModel,
  buildTokenAttribution,
  buildRecoveryEvidence,
  buildSpillEvidence,
  buildTimelineEvidence,
  compactTokenCount,
  formatTokenCount,
  type ContextSignalTone,
} from '@/lib/contextDebug'

const props = defineProps<{
  data: SessionContextDebug | null
  loading?: boolean
  error?: string | null
}>()

defineEmits<{
  refresh: []
}>()

const { t } = useI18n()

const view = computed(() => buildContextDebugModel(props.data))
const recoveryEvidence = computed(() => buildRecoveryEvidence(props.data))
const spillEvidence = computed(() => buildSpillEvidence(props.data))
const timelineEvidence = computed(() => buildTimelineEvidence(props.data))
const tokenAttribution = computed(() => buildTokenAttribution(props.data))
const compactExportJson = computed(() => buildCompactExportJSON(props.data))
const workingSet = computed(() => props.data?.workingSet ?? [])
const injectedItems = computed(() => props.data?.topItems ?? props.data?.items ?? [])
const evictedItems = computed(() => props.data?.topEvictions ?? props.data?.evictionDetails ?? [])

const signalLabel = computed(() => {
  if (view.value.signal === 'warn') return t('chat.contextSignalWarn')
  if (view.value.signal === 'block') return t('chat.contextSignalBlock')
  return t('chat.contextSignalOk')
})

const signalIcon = computed(() => {
  if (view.value.signal === 'block') return ShieldAlert
  if (view.value.signal === 'warn') return TriangleAlert
  return CheckCircle2
})

const signalClass = computed(() => {
  if (view.value.tone === 'warning') return 'context-debug-signal-warn'
  if (view.value.tone === 'danger') return 'context-debug-signal-danger'
  return 'context-debug-signal-ok'
})

const usageBarClass = computed(() => {
  if (view.value.tone === 'warning') return 'context-debug-track-fill-warn'
  if (view.value.tone === 'danger') return 'context-debug-track-fill-danger'
  return 'context-debug-track-fill-ok'
})

const usageLabel = computed(() => {
  if (view.value.usagePercent == null) return '—'
  return `${view.value.usagePercent}%`
})

const usageWidth = computed(() => {
  if (view.value.usagePercent == null) return '0%'
  return `${Math.max(3, Math.min(100, view.value.usagePercent))}%`
})

function formatNumber(value?: number) {
  return formatTokenCount(value)
}

function compactNumber(value?: number) {
  return compactTokenCount(value)
}

function compactContent(content?: string) {
  const text = String(content ?? '').trim()
  if (text.length <= 320) return text
  return `${text.slice(0, 320)}...`
}

function recoveryToneClass(tone: ContextSignalTone) {
  if (tone === 'danger') return 'context-debug-dot-danger'
  if (tone === 'warning') return 'context-debug-dot-warn'
  return 'context-debug-dot-ok'
}

const MetricCell = defineComponent({
  name: 'MetricCell',
  props: {
    label: { type: String, required: true },
    value: { type: String, required: true },
    tone: { type: String, default: 'neutral' },
  },
  setup(metricProps) {
    return () => h('div', {
      class: [
        'context-debug-metric',
        metricProps.tone === 'accent'
          ? 'context-debug-metric-accent'
          : metricProps.tone === 'warn'
            ? 'context-debug-metric-warn'
            : 'context-debug-metric-neutral',
      ],
    }, [
      h('div', { class: 'context-debug-metric-label' }, metricProps.label),
      h('div', { class: 'context-debug-metric-value' }, metricProps.value),
    ])
  },
})
</script>

<style scoped>
.context-debug-shell {
  display: grid;
  gap: 12px;
  font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}

.context-debug-panel,
.context-debug-section,
.context-debug-details {
  overflow: hidden;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  background:
    linear-gradient(180deg, color-mix(in srgb, var(--forebrain-surface) 88%, transparent), color-mix(in srgb, var(--forebrain-bg-alt) 54%, transparent));
  box-shadow: var(--forebrain-inset-shadow);
}

.context-debug-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  border-bottom: 1px solid var(--forebrain-divider);
  padding: 12px;
  background:
    linear-gradient(90deg, color-mix(in srgb, var(--forebrain-brand-1) 8%, transparent), transparent 48%),
    var(--forebrain-card-gradient);
}

.context-debug-eyebrow,
.context-debug-section-title {
  color: var(--forebrain-text);
  font-size: 12px;
  font-weight: 700;
  line-height: 1.2;
}

.context-debug-eyebrow {
  text-transform: uppercase;
}

.context-debug-signal {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  border: 1px solid;
  border-radius: 999px;
  padding: 3px 8px;
  font-size: 10px;
  font-weight: 700;
  line-height: 1;
  text-transform: uppercase;
}

.context-debug-signal-ok {
  border-color: rgba(70, 150, 110, 0.3);
  background: rgba(70, 150, 110, 0.12);
  color: rgb(70, 150, 110);
}

.context-debug-signal-warn {
  border-color: rgba(216, 160, 70, 0.38);
  background: rgba(216, 160, 70, 0.14);
  color: rgb(216, 160, 70);
}

.context-debug-signal-danger {
  border-color: rgba(160, 70, 70, 0.42);
  background: rgba(160, 70, 70, 0.12);
  color: var(--forebrain-danger);
}

.context-debug-refresh {
  display: inline-flex;
  height: 30px;
  flex-shrink: 0;
  align-items: center;
  gap: 6px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 7px;
  background: var(--forebrain-button-alt-bg);
  padding: 0 10px;
  color: var(--forebrain-text);
  font-size: 11px;
  font-weight: 600;
}

.context-debug-refresh:hover:not(:disabled) {
  background: var(--forebrain-button-alt-hover-bg);
}

.context-debug-refresh:disabled {
  cursor: not-allowed;
  opacity: 0.62;
}

.context-debug-error,
.context-debug-empty {
  display: flex;
  align-items: center;
  gap: 9px;
  color: var(--forebrain-muted-text);
  font-size: 12px;
}

.context-debug-error {
  align-items: flex-start;
  border: 1px solid rgba(160, 70, 70, 0.32);
  border-radius: 8px;
  background: rgba(160, 70, 70, 0.08);
  padding: 10px;
  color: var(--forebrain-danger);
}

.context-debug-empty {
  justify-content: center;
  min-height: 112px;
  padding: 18px;
}

.context-debug-budget {
  display: grid;
  grid-template-columns: minmax(0, 0.75fr) minmax(0, 1.5fr);
  gap: 12px;
  padding: 12px;
}

.context-debug-budget-grid,
.context-debug-metrics {
  display: grid;
  gap: 8px;
}

.context-debug-budget-grid {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.context-debug-metrics {
  grid-template-columns: repeat(5, minmax(0, 1fr));
  border-top: 1px solid var(--forebrain-divider);
  padding: 12px;
}

.context-debug-metric {
  min-width: 0;
  border: 1px solid;
  border-radius: 8px;
  padding: 8px 9px;
}

.context-debug-metric-neutral {
  border-color: var(--forebrain-divider);
  background: color-mix(in srgb, var(--forebrain-bg-alt) 56%, transparent);
}

.context-debug-metric-accent {
  border-color: var(--forebrain-brand-border);
  background: var(--forebrain-brand-soft);
}

.context-debug-metric-warn {
  border-color: rgba(216, 160, 70, 0.32);
  background: rgba(216, 160, 70, 0.09);
}

.context-debug-metric-label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--forebrain-muted-text);
  font-size: 10px;
  font-weight: 650;
  line-height: 1.2;
  text-transform: uppercase;
}

.context-debug-metric-value {
  margin-top: 5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--forebrain-text);
  font-size: 14px;
  font-weight: 750;
  line-height: 1.1;
}

.context-debug-track {
  padding: 0 12px 12px;
}

.context-debug-track-rail {
  position: relative;
  height: 10px;
  overflow: hidden;
  border-radius: 999px;
  background: color-mix(in srgb, var(--forebrain-surface-control) 80%, var(--forebrain-code-bg) 20%);
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--forebrain-divider) 70%, transparent);
}

.context-debug-track-fill {
  height: 100%;
  min-width: 0;
  border-radius: 999px;
  transition: width 220ms ease;
}

.context-debug-track-fill-ok {
  background: linear-gradient(90deg, rgb(70, 150, 110), var(--forebrain-brand-1));
}

.context-debug-track-fill-warn {
  background: linear-gradient(90deg, rgb(70, 150, 110), rgb(216, 160, 70));
}

.context-debug-track-fill-danger {
  background: linear-gradient(90deg, rgb(216, 160, 70), rgb(160, 70, 70));
}

.context-debug-threshold {
  position: absolute;
  top: -3px;
  bottom: -3px;
  width: 1px;
}

.context-debug-threshold-warn {
  background: rgba(216, 160, 70, 0.85);
}

.context-debug-threshold-block {
  background: rgba(160, 70, 70, 0.9);
}

.context-debug-track-labels {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 14px;
  margin-top: 8px;
  color: var(--forebrain-muted-text);
  font-size: 11px;
}

.context-debug-grid,
.context-debug-detail-grid {
  display: grid;
  gap: 12px;
}

.context-debug-grid,
.context-debug-detail-grid {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}

.context-debug-section {
  padding: 12px;
}

.context-debug-section-head,
.context-debug-summary {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}

.context-debug-section-subtitle {
  margin-top: 3px;
  color: var(--forebrain-muted-text);
  font-size: 11px;
  line-height: 1.3;
}

.context-debug-count {
  display: inline-flex;
  min-width: 22px;
  height: 22px;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--forebrain-divider);
  border-radius: 999px;
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text-2);
  font-size: 11px;
  font-weight: 700;
}

.context-debug-chip-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 12px;
}

.context-debug-chip {
  display: inline-flex;
  max-width: 100%;
  align-items: center;
  gap: 6px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 7px;
  background: color-mix(in srgb, var(--forebrain-bg-alt) 64%, transparent);
  padding: 6px 8px;
  color: var(--forebrain-text-2);
  font-size: 11px;
}

.context-debug-spill-ledger {
  min-width: 0;
}

.context-debug-spill-list {
  display: grid;
  gap: 8px;
  margin-top: 12px;
}

.context-debug-spill-row {
  min-width: 0;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  background:
    linear-gradient(90deg, color-mix(in srgb, var(--forebrain-brand-1) 6%, transparent), transparent 56%),
    color-mix(in srgb, var(--forebrain-bg-alt) 58%, transparent);
  padding: 9px;
}

.context-debug-spill-main {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 12px;
  align-items: start;
  min-width: 0;
}

.context-debug-spill-stats {
  display: grid;
  grid-template-columns: repeat(3, minmax(52px, auto));
  gap: 6px;
}

.context-debug-spill-stats span {
  min-width: 0;
  overflow: hidden;
  text-align: center;
  text-overflow: ellipsis;
  white-space: nowrap;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: var(--forebrain-button-alt-bg);
  padding: 3px 6px;
  color: var(--forebrain-text-2);
  font-size: 11px;
  font-weight: 700;
}

.context-debug-spill-meta {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 10px;
  margin-top: 7px;
  color: var(--forebrain-muted-text);
  font-size: 10px;
}

.context-debug-muted {
  margin-top: 10px;
  color: var(--forebrain-muted-text);
  font-size: 12px;
}

.context-debug-timeline {
  display: grid;
  gap: 10px;
  margin-top: 12px;
}

.context-debug-timeline-item {
  display: flex;
  gap: 9px;
  min-width: 0;
}

.context-debug-timeline-dot {
  width: 8px;
  height: 8px;
  margin-top: 5px;
  flex-shrink: 0;
  border-radius: 999px;
  box-shadow: 0 0 0 3px rgba(128, 128, 128, 0.12);
}

.context-debug-dot-ok {
  background: rgb(70, 150, 110);
}

.context-debug-dot-warn {
  background: rgb(216, 160, 70);
}

.context-debug-dot-danger {
  background: var(--forebrain-danger);
}

.context-debug-code-pill,
.context-debug-mini-pill,
.context-debug-token {
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: color-mix(in srgb, var(--forebrain-code-surface) 8%, var(--forebrain-bg-alt));
  color: var(--forebrain-muted-text);
}

.context-debug-code-pill {
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  padding: 3px 6px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", monospace;
  font-size: 10px;
}

.context-debug-details {
  padding: 11px 12px 12px;
}

.context-debug-summary {
  cursor: pointer;
  color: var(--forebrain-text);
  font-size: 12px;
  font-weight: 700;
  list-style: none;
}

.context-debug-summary::-webkit-details-marker {
  display: none;
}

.context-debug-item {
  min-width: 0;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  background: color-mix(in srgb, var(--forebrain-bg-alt) 58%, transparent);
  padding: 9px;
}

.context-debug-item-main {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: start;
  gap: 10px;
  min-width: 0;
}

.context-debug-mini-pill {
  flex-shrink: 0;
  padding: 1px 5px;
  font-size: 10px;
}

.context-debug-token {
  flex-shrink: 0;
  padding: 3px 6px;
  font-size: 11px;
  font-weight: 700;
}

.context-debug-content {
  margin-top: 8px;
  max-height: 96px;
  overflow: hidden;
  white-space: pre-wrap;
  border-top: 1px solid var(--forebrain-divider);
  padding-top: 8px;
  color: var(--forebrain-text-2);
  font-size: 11px;
  line-height: 1.45;
}

@media (max-width: 860px) {
  .context-debug-budget,
  .context-debug-grid,
  .context-debug-detail-grid {
    grid-template-columns: minmax(0, 1fr);
  }

  .context-debug-metrics {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .context-debug-spill-main,
  .context-debug-spill-meta {
    grid-template-columns: minmax(0, 1fr);
  }

  .context-debug-spill-stats {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (max-width: 560px) {
  .context-debug-header {
    flex-direction: column;
  }

  .context-debug-refresh {
    width: 100%;
    justify-content: center;
  }

  .context-debug-budget-grid,
  .context-debug-metrics {
    grid-template-columns: minmax(0, 1fr);
  }

  .context-debug-details,
  .context-debug-section {
    padding: 10px;
  }

  .context-debug-token {
    padding-inline: 5px;
  }
}
</style>
