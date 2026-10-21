<template>
  <div
    v-if="line.label"
    class="flex items-center gap-1.5 text-[11.5px] text-[var(--forebrain-muted-text)]"
    :aria-label="t('chat.workedAria')"
    data-testid="run-worked-line"
  >
    <span class="h-px w-3 flex-none bg-[var(--forebrain-divider-strong)]" />
    <span class="whitespace-nowrap font-mono">{{ line.label }}</span>
    <template v-if="line.firstToken">
      <span class="worked-dot" aria-hidden="true">·</span>
      <span class="whitespace-nowrap font-mono">{{ line.firstToken }}</span>
    </template>
    <template v-if="line.plan">
      <span class="worked-dot" aria-hidden="true">·</span>
      <PlanProgressSegments class="font-mono" :plan="line.plan" />
    </template>
    <template v-if="line.time">
      <span class="worked-dot" aria-hidden="true">·</span>
      <span class="whitespace-nowrap font-mono">{{ line.time }}</span>
    </template>
    <span class="h-px flex-1 bg-[var(--forebrain-divider-strong)]" />
  </div>
</template>

<script setup lang="ts">
/**
 * The line that closes a run, as in the terminal: how long it worked, the
 * checklist progress when the run had one, then when it finished — every
 * segment separated the way the running line separates its own. Every page
 * that shows a conversation closes its runs with this — the conversation with
 * the line its own message derives, a subagent view with the line its
 * execution's worked block carries.
 *
 * The checklist's task in flight is the running line's business, not this
 * one's: the prototype prints the tally alone here, because the task that is
 * in flight is already named above the composer.
 */
import { computed } from 'vue'
import PlanProgressSegments from './PlanProgressSegments.vue'
import { workedLineOf, type ChatMessage, type WorkedLine } from '@/composables/useChatStream'
import { useI18n } from '@/locales'

const props = defineProps<{ message?: ChatMessage; line?: WorkedLine }>()

const { t } = useI18n()
const line = computed<WorkedLine>(() => {
  const source = props.line ?? (props.message ? workedLineOf(props.message, t) : { label: '' })
  if (!source.plan) return source
  return { ...source, plan: { done: source.plan.done, total: source.plan.total } }
})
</script>

<style scoped>
.worked-dot {
  flex: none;
  color: var(--forebrain-muted-text);
}
</style>
