<template>
  <Tool
    :open="open ?? undefined"
    :default-open="open == null ? defaultOpen : undefined"
    @update:open="emit('update:open', Boolean($event))"
  >
    <ToolHeader type="tool-function_call" :state="toolState" :title="title" />
    <ToolContent>
      <!-- A skill card's display_body is the whole user-facing text ("Loaded
           from …" / "Failed to load from …" plus the raw error), so the
           separate error line would repeat what the body already says. -->
      <ToolOutput
        :output="step.output ?? ''"
        :error-text="isSkill ? '' : step.error"
      />
    </ToolContent>
  </Tool>
</template>

<script setup lang="ts">
/**
 * One tool call, drawn the same way for the conversation and for a subagent: a
 * call is a call, and the two views had better agree about what it says. The
 * caller owns the open state (the subagent view persists it per transcript, the
 * conversation does not), so the card works both controlled and not.
 */
import { computed } from 'vue'
import { Tool, ToolHeader, ToolContent, ToolOutput } from '@repo/elements/tool'
import { t } from '@/locales'
import { skillCardTitle, type SubagentToolStep } from '@/composables/useChatStream'

const props = withDefaults(defineProps<{
  step: SubagentToolStep
  open?: boolean
  defaultOpen?: boolean
}>(), {
  open: undefined,
  defaultOpen: false,
})

const emit = defineEmits<{
  (e: 'update:open', value: boolean): void
}>()

/**
 * isSkill marks a skill load: its display_body is the whole user-facing text
 * ("Loaded from …" / "Failed to load from …" plus the raw error), so the
 * separate error line would repeat what the body already says.
 */
const isSkill = computed(() => String(props.step.category ?? '').trim().toLowerCase() === 'skill')

/**
 * toolState maps the runtime's own status onto the badge states the card
 * understands. A canceled or denied call never produced output, which is what
 * the denied state says; the alternative painted it as an error.
 */
const toolState = computed(() => {
  const status = String(props.step.status ?? '').trim().toLowerCase()
  if (status === 'canceled' || status === 'cancelled' || status === 'denied') return 'output-denied'
  if (props.step.error || status === 'failed' || status === 'error') return 'output-error'
  if (!status || status === 'running' || status === 'pending' || status === 'awaiting approval') return 'input-streaming'
  return 'output-available'
})

/**
 * The title of a skill load comes from the structured identity via
 * skillCardTitle, never from the argument summary: the invocation carried no
 * arguments worth printing, and "review-agent {"explicit":true}" is exactly
 * the noise this card exists to prevent.
 */
const title = computed(() => {
  const skill = skillCardTitle(props.step)
  if (skill !== undefined) return skill
  if (String(props.step.status ?? '').trim().toLowerCase().startsWith('cancel')) {
    // A canceled call never ran, so the card says so rather than naming what it
    // would have done as though it had.
    return props.step.summary || t('chat.toolCanceled')
  }
  return props.step.summary || props.step.toolName
})
</script>
