<template>
  <div class="bench-tab">
    <p v-if="!sessionId" class="bench-hint">{{ t('chat.benchContextEmpty') }}</p>
    <template v-else>
      <div class="bench-card">
        <div class="bench-card-head">
          <span class="bench-card-title"><Gauge class="size-3.5" aria-hidden="true" />{{ t('chat.benchContextUsage') }}</span>
          <button type="button" class="bench-icon-btn" :aria-label="t('chat.benchRefresh')" @click="load()">
            <RotateCw class="size-3.5" aria-hidden="true" />
          </button>
        </div>

        <p v-if="loading" class="bench-hint">{{ t('common.loading') }}</p>
        <p v-else-if="error" class="bench-hint bench-hint--error">{{ error }}</p>

        <template v-else>
          <div class="ctx-big">
            <b>{{ percentLabel }}</b>
            <span>{{ t('chat.benchContextOfWindow') }}</span>
            <span class="bench-badge" :class="near ? 'bench-badge--warn' : 'bench-badge--ok'">
              {{ near ? t('chat.benchContextNear') : t('chat.benchContextNormal') }}
            </span>
          </div>
          <ContextMeter :segments="segments" />
          <p v-if="near" class="bench-note">{{ t('chat.benchContextCompactNote') }}</p>
          <div class="bench-kvs"><span class="k">{{ t('chat.benchContextEstimate') }}</span><span class="v">{{ compact(view.estimate) }} tokens</span></div>
          <div class="bench-kvs"><span class="k">{{ t('chat.benchContextAvailable') }}</span><span class="v">{{ availableLabel }}</span></div>
          <div class="bench-kvs"><span class="k">{{ t('chat.benchContextRemaining') }}</span><span class="v">{{ compact(view.remaining) }} tokens</span></div>
          <div class="bench-kvs"><span class="k">{{ t('chat.benchContextMode') }}</span><span class="v">{{ view.mode }}</span></div>
        </template>
      </div>

      <div v-if="data" class="bench-card bench-card--acc">
        <ContextAccordion
          :title="t('chat.benchContextItems')"
          :count="view.itemCount"
          :open="open.items"
          testid="ctx-acc-items"
          @toggle="open.items = !open.items"
        >
          <template v-if="injected.length">
            <div v-for="(item, idx) in injected" :key="`${item.sourceId ?? 'i'}-${idx}`" class="bench-kvs">
              <span class="k">{{ item.title || item.sourceId || t('common.none') }}</span>
              <span class="v">{{ compact(item.estimatedTokens) }}</span>
            </div>
          </template>
          <p v-else class="bench-hint">{{ t('chat.benchContextEmpty') }}</p>
        </ContextAccordion>

        <ContextAccordion
          :title="t('chat.benchContextEvicted')"
          :count="view.evictionCount"
          :open="open.evicted"
          testid="ctx-acc-evicted"
          @toggle="open.evicted = !open.evicted"
        >
          <template v-if="evicted.length">
            <!-- What left the context is a saving, not a size: it reads with the
                 minus the prototype prints (:4220). -->
            <div v-for="(item, idx) in evicted" :key="`${item.sourceId ?? 'e'}-${idx}`" class="bench-kvs">
              <span class="k">{{ item.sourceId || t('common.none') }}</span>
              <span class="v">−{{ compact(item.estimatedTokens) }}</span>
            </div>
          </template>
          <p v-else class="bench-hint">{{ t('chat.benchContextEmpty') }}</p>
        </ContextAccordion>

        <ContextAccordion
          :title="t('chat.benchContextCompaction')"
          :count="view.recoveryCount"
          :open="open.compaction"
          testid="ctx-acc-compaction"
          @toggle="open.compaction = !open.compaction"
        >
          <template v-if="recovery.length">
            <div v-for="(item, idx) in recovery" :key="`r-${idx}`" class="bench-kvs">
              <span class="k">{{ item.trigger }} {{ t('chat.benchContextReplaced', { n: item.replacedItems ?? 0 }) }}</span>
              <span class="v">−{{ compact(Math.max((item.tokensBefore ?? 0) - (item.tokensAfter ?? 0), 0)) }}</span>
            </div>
          </template>
          <p v-else class="bench-hint">{{ t('chat.benchContextEmpty') }}</p>
        </ContextAccordion>

        <ContextAccordion
          :title="t('chat.benchContextSpills')"
          :count="view.spillCount"
          :open="open.spills"
          testid="ctx-acc-spills"
          @toggle="open.spills = !open.spills"
        >
          <template v-if="spills.length">
            <div v-for="(item, idx) in spills" :key="`s-${idx}`" class="bench-kvs">
              <span class="k">{{ item.toolName || item.path }}</span>
              <span class="v">{{ item.bytesLabel }}</span>
            </div>
          </template>
          <p v-else class="bench-hint">{{ t('chat.benchContextEmpty') }}</p>
        </ContextAccordion>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * The workbench's context tab: how full the conversation's context window is,
 * what fills it, and what was evicted, compacted or spilled. It reads the
 * same `/context` report the subagent view uses, and draws the parts the
 * preview shows through the shared meter and accordion components.
 */
import { computed, reactive, ref, watch } from 'vue'
import { Gauge, RotateCw } from 'lucide-vue-next'

import ContextAccordion from '@/components/workbench/ContextAccordion.vue'
import ContextMeter, { type ContextMeterSegment } from '@/components/workbench/ContextMeter.vue'
import { getErrorMessage, forebrainApi, type SessionContextDebug, type WorkspaceScope } from '@/lib/api'
import { buildContextDebugModel, buildRecoveryEvidence, buildSpillEvidence, compactTokenCount } from '@/lib/contextDebug'
import { useI18n } from '@/locales'

const props = defineProps<{ scope: WorkspaceScope }>()
const { t } = useI18n()

const sessionId = computed(() => String(props.scope.sessionId ?? '').trim())
const data = ref<SessionContextDebug | null>(null)
const loading = ref(false)
const error = ref('')
const open = reactive({ items: true, evicted: false, compaction: false, spills: false })

const view = computed(() => buildContextDebugModel(data.value))
const recovery = computed(() => buildRecoveryEvidence(data.value))
const spills = computed(() => buildSpillEvidence(data.value))
const injected = computed(() => data.value?.topItems ?? data.value?.items ?? [])
const evicted = computed(() => data.value?.topEvictions ?? data.value?.evictionDetails ?? [])

const near = computed(() => (view.value.usagePercent ?? 0) >= 80)
const percentLabel = computed(() => (view.value.usagePercent == null ? '—' : `${view.value.usagePercent}%`))

const injectedTokens = computed(() => injected.value.reduce((sum, item) => sum + Math.max(0, item.estimatedTokens ?? 0), 0))
const segments = computed<ContextMeterSegment[]>(() => [
  { label: t('chat.benchContextHistory'), value: view.value.conversationTokens ?? 0, color: 'var(--forebrain-brand-1)' },
  { label: t('chat.benchContextInjected'), value: injectedTokens.value, color: 'var(--forebrain-brand-border)' },
  { label: t('chat.benchContextReserve'), value: view.value.reserve ?? 0, color: 'var(--forebrain-divider-strong)' },
])
const availableLabel = computed(() => {
  const available = view.value.availableWindow
  if (available == null) return t('common.none')
  const window = view.value.window
  const reserve = view.value.reserve
  if (window == null || reserve == null) return `${compactTokenCount(available)} tokens`
  return `${compactTokenCount(available)}（${compactTokenCount(window)} − ${compactTokenCount(reserve)}）`
})

function compact(value?: number) {
  return compactTokenCount(value)
}

let generation = 0
async function load() {
  const sid = sessionId.value
  if (!sid) {
    data.value = null
    return
  }
  const gen = ++generation
  loading.value = true
  error.value = ''
  try {
    const res = await forebrainApi.sessionContext(sid, { scope: 'full' })
    if (gen === generation) data.value = res
  } catch (cause) {
    if (gen === generation) {
      data.value = null
      error.value = getErrorMessage(cause)
    }
  } finally {
    if (gen === generation) loading.value = false
  }
}

watch(sessionId, () => void load(), { immediate: true })
</script>

<style scoped>
.bench-tab {
  display: grid;
  gap: 12px;
  align-content: start;
}
.bench-card {
  display: grid;
  gap: 8px;
  padding: 12px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-surface);
}
.bench-card--acc {
  gap: 0;
  padding: 4px 12px;
}
.bench-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.bench-card-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--forebrain-text);
}
.bench-icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border-radius: 6px;
  color: var(--forebrain-muted-text);
}
.bench-icon-btn:hover {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
}
.ctx-big {
  display: flex;
  align-items: baseline;
  gap: 8px;
}
.ctx-big b {
  font-size: 22px;
  line-height: 1.2;
  font-weight: 650;
  font-variant-numeric: tabular-nums;
  color: var(--forebrain-text);
}
.ctx-big span {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.bench-badge {
  margin-left: auto;
  font-size: 11px;
  font-weight: 500;
  padding: 2px 8px;
  border-radius: 999px;
  border: 1px solid var(--forebrain-divider);
}
.bench-badge--ok {
  color: var(--forebrain-success);
  background: var(--forebrain-success-soft);
  border-color: var(--forebrain-success-soft);
}
.bench-badge--warn {
  color: var(--forebrain-warning);
  background: var(--forebrain-warning-soft);
  border-color: var(--forebrain-warning-soft);
}
.bench-note {
  font-size: 12px;
  color: var(--forebrain-warning);
  background: var(--forebrain-warning-soft);
  border-radius: 6px;
  padding: 6px 8px;
  margin: 0;
}
.bench-kvs {
  display: flex;
  justify-content: space-between;
  gap: 8px;
  font-size: 12px;
}
.bench-kvs .k {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--forebrain-text-2);
}
.bench-kvs .v {
  flex: none;
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  font-variant-numeric: tabular-nums;
}
.bench-hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
  margin: 0;
}
.bench-hint--error {
  color: var(--forebrain-danger);
}
</style>
