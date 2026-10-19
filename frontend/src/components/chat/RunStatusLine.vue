<template>
  <div role="status" class="run-status-line">
    <Loader2 class="run-status-spinner" aria-hidden="true" />
    <span class="run-status-seg run-status-label">{{ label }}</span>
    <template v-if="plan">
      <span class="run-status-dot" aria-hidden="true">·</span>
      <PlanProgressSegments class="run-status-seg" :plan="plan" />
    </template>
    <template v-if="tail">
      <span class="run-status-dot" aria-hidden="true">·</span>
      <span class="run-status-seg run-status-tail">{{ tail }}</span>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * The line above the composer while something runs, on both surfaces that have
 * one: the conversation and a subagent's tab. It reads in the terminal's order
 * — the state, then its checklist, then whatever else the run measured — and
 * every segment but the last one stays whole, so a long task name is the only
 * thing ever cut off.
 */
import { Loader2 } from 'lucide-vue-next'
import PlanProgressSegments from './PlanProgressSegments.vue'
import type { WorkedPlanProgress } from '@/composables/useChatStream'

defineProps<{
  /** The state in words, e.g. "Working (1m 12s • esc to interrupt)". */
  label: string
  /** The checklist the run is working through, when it has one. */
  plan?: WorkedPlanProgress
  /** One more segment after the checklist: the conversation's first-token wait. */
  tail?: string
}>()
</script>

<style scoped>
.run-status-line {
  display: flex;
  flex: none;
  align-items: center;
  gap: 6px;
  min-width: 0;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}

.run-status-spinner {
  width: 13px;
  height: 13px;
  flex: none;
  color: var(--forebrain-brand-1);
  animation: run-status-spin 1s linear infinite;
}

.run-status-seg {
  white-space: nowrap;
  min-width: 0;
}

.run-status-label,
.run-status-tail {
  font-family: var(--forebrain-mono-font);
}

/* The last segment yields its overflow: everything before it stays whole. */
.run-status-tail {
  overflow: hidden;
  text-overflow: ellipsis;
}

.run-status-dot {
  flex: none;
  color: var(--forebrain-muted-text);
}

@keyframes run-status-spin {
  to {
    transform: rotate(360deg);
  }
}

@media (prefers-reduced-motion: reduce) {
  .run-status-spinner {
    animation: none;
  }
}
</style>
