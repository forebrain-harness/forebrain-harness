<template>
  <span v-if="plan" class="inline-flex items-center gap-[0.35em] whitespace-nowrap font-mono">
    <span
      class="inline-flex items-center whitespace-nowrap"
      :aria-label="t('chat.checklistProgress', { done: plan.done, total: plan.total })"
    >
      <SquareCheck class="plan-check" aria-hidden="true" />{{ plan.done }}/{{ plan.total }}
    </span>
    <span v-if="plan.active" class="whitespace-nowrap font-sans">{{ plan.active }}</span>
  </span>
</template>

<script setup lang="ts">
/**
 * The checklist facts of a run's status lines: the checkbox, done/total, and
 * the task in flight. The conversation's working line, every worked line, and
 * a subagent view's working line all draw these as the same segments, the way
 * the terminal draws ☑N/M · task on both of its lines.
 */
import { SquareCheck } from 'lucide-vue-next'
import { useI18n } from '@/locales'
import type { WorkedPlanProgress } from '@/composables/useChatStream'

defineProps<{ plan?: WorkedPlanProgress }>()

const { t } = useI18n()
</script>

<style scoped>
/* The checklist mark: sized to the digits it counts and optically centred on
   them (a middle/middle alignment sits too low against digits, whose visual
   centre sits above the baseline). */
.plan-check {
  width: 1em;
  height: 1em;
  display: inline-block;
  vertical-align: -0.14em;
  stroke-width: 2.25;
  color: var(--forebrain-success);
  margin-right: 0.2em;
}
</style>
