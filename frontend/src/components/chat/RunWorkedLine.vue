<template>
  <div
    v-if="line.label"
    class="mt-3 flex items-center gap-3 text-[11px] text-[var(--forebrain-muted-text)]"
    aria-label="run duration"
    data-testid="run-worked-line"
  >
    <span class="h-px w-3 bg-[var(--forebrain-divider-strong)]" />
    <span class="whitespace-nowrap font-mono">{{ line.label }}</span>
    <PlanProgressSegments :plan="line.plan" />
    <span v-if="line.time" class="whitespace-nowrap font-mono">{{ line.time }}</span>
    <span class="h-px flex-1 bg-[var(--forebrain-divider-strong)]" />
  </div>
</template>

<script setup lang="ts">
/**
 * The line that closes a run, as in the terminal: how long it worked, the
 * checklist progress when the run had one, then when it finished. Every page
 * that shows a conversation closes its runs with this — the conversation with
 * the line its own message derives, a subagent view with the line its
 * execution's worked block carries.
 */
import { computed } from 'vue'
import PlanProgressSegments from './PlanProgressSegments.vue'
import { workedLineOf, type ChatMessage, type WorkedLine } from '@/composables/useChatStream'
import { useI18n } from '@/locales'

const props = defineProps<{ message?: ChatMessage; line?: WorkedLine }>()

const { t } = useI18n()
const line = computed<WorkedLine>(() => props.line ?? (props.message ? workedLineOf(props.message, t) : { label: '' }))
</script>
