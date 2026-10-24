<template>
  <!-- One transport failure of this stream's LLM and the retry about to run:
       spinner while it waits, a live countdown, and the n/total progress —
       the same card the conversation's own timeline and every subagent view
       draw, from the same event. Frozen (stopped) when the retries ran out;
       removed by the retried attempt's first streamed words. -->
  <div role="status" class="transport-retry-card" data-testid="transport-retry">
    <Loader2 v-if="!block.stopped" class="transport-retry-spinner" aria-hidden="true" />
    <span class="transport-retry-text">{{ label }}</span>
  </div>
</template>

<script setup lang="ts">
/**
 * The transport-retry card, drawn wherever a stream died on the network and a
 * retry is pending: the conversation's timeline and each subagent's view, from
 * the same TimelineBlock. The wording never restates the failure — the error
 * card above it already does — only the action, the countdown and the
 * progress. The countdown walks once a second from the block's deadline and
 * the interval dies with the component, so a card removed by recovery or the
 * turn's end leaves no timer behind.
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { Loader2 } from 'lucide-vue-next'
import { useI18n } from '@/locales'
import type { TransportRetryBlock } from '@/composables/useChatStream'

const props = defineProps<{ block: TransportRetryBlock }>()

const { t, locale } = useI18n()
const now = ref(Date.now())
let timer: number | undefined

onMounted(() => {
  if (props.block.stopped || !props.block.deadline) return
  timer = window.setInterval(() => {
    now.value = Date.now()
  }, 1000)
})

onBeforeUnmount(() => {
  if (timer !== undefined) window.clearInterval(timer)
})

const remainingMs = computed(() => (props.block.deadline ?? 0) - now.value)

/**
 * The countdown reads never shorter than the true wait: the value is ceiled
 * to the unit it shows. Large configured retry caps genuinely reach the hour
 * and day tiers. Tense follows the wording: waiting is future ("retry in"),
 * in-flight is present ("Retrying").
 */
function formatCountdown(ms: number, lang: string): string {
  if (ms < 0) ms = 0
  const zh = lang.startsWith('zh')
  if (ms < 60_000) {
    const v = Math.max(1, Math.ceil(ms / 1000))
    return zh ? `${v} 秒` : `${v}s`
  }
  if (ms < 3_600_000) {
    const v = Math.ceil(ms / 60_000)
    return zh ? `${v} 分钟` : `${v}m`
  }
  if (ms < 86_400_000) {
    const v = Math.ceil(ms / 3_600_000)
    return zh ? `${v} 小时` : `${v}h`
  }
  const v = Math.ceil(ms / 86_400_000)
  return zh ? `${v} 天` : `${v}d`
}

const label = computed(() => {
  if (props.block.stopped) {
    return t('chat.transportRetry.stopped', { n: props.block.retry, total: props.block.total })
  }
  if (remainingMs.value > 0) {
    return t('chat.transportRetry.retrying', {
      countdown: formatCountdown(remainingMs.value, locale.value),
      n: props.block.retry,
      total: props.block.total,
    })
  }
  return t('chat.transportRetry.retryingNow', { n: props.block.retry, total: props.block.total })
})
</script>

<style scoped>
/* The working-status line's visual language: a small brand spinner, muted
   compact text, no gradient, no card chrome. */
.transport-retry-card {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}

.transport-retry-spinner {
  width: 13px;
  height: 13px;
  flex: none;
  color: var(--forebrain-brand-1);
  animation: transport-retry-spin 1s linear infinite;
}

.transport-retry-text {
  white-space: nowrap;
  min-width: 0;
}

@keyframes transport-retry-spin {
  from {
    transform: rotate(0deg);
  }

  to {
    transform: rotate(360deg);
  }
}

@media (prefers-reduced-motion: reduce) {
  .transport-retry-spinner {
    animation: none;
  }
}
</style>
