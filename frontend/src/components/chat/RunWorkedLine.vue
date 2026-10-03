<template>
  <div
    v-if="line.label"
    class="mt-3 flex items-center gap-3 text-[11px] text-[var(--forebrain-muted-text)]"
    aria-label="run duration"
    data-testid="run-worked-line"
  >
    <span class="h-px w-3 bg-[var(--forebrain-divider-strong)]" />
    <span class="whitespace-nowrap font-mono">{{ line.label }}</span>
    <span
      v-if="line.plan"
      class="inline-flex items-center whitespace-nowrap font-mono"
      :aria-label="t('chat.checklistProgress', { done: line.plan.done, total: line.plan.total })"
    >
      <SquareCheck class="worked-check" aria-hidden="true" />{{ line.plan.done }}/{{ line.plan.total }}
    </span>
    <span v-if="line.plan?.active" class="whitespace-nowrap">{{ line.plan.active }}</span>
    <span v-if="line.time" class="whitespace-nowrap font-mono">{{ line.time }}</span>
    <span class="h-px flex-1 bg-[var(--forebrain-divider-strong)]" />
  </div>
</template>

<script setup lang="ts">
/**
 * The line that closes a run, as in the terminal: how long it worked, the
 * checklist progress when the run had one, then when it finished. Every page
 * that shows a conversation closes its runs with this.
 */
import { computed } from 'vue'
import { SquareCheck } from 'lucide-vue-next'
import { workedLineOf, type ChatMessage } from '@/composables/useChatStream'
import { useI18n } from '@/locales'

const props = defineProps<{ message: ChatMessage }>()

const { t } = useI18n()
const line = computed(() => workedLineOf(props.message, t))
</script>

<style scoped>
/* The checklist mark on the worked line: sized to the digits it counts and
   optically centred on them (a middle/middle alignment sits too low against
   digits, whose visual centre sits above the baseline). */
.worked-check {
  width: 1em;
  height: 1em;
  display: inline-block;
  vertical-align: -0.14em;
  stroke-width: 2.25;
  color: var(--forebrain-success);
  margin-right: 0.2em;
}
</style>
