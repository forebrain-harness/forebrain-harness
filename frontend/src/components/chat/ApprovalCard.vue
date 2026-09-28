<template>
  <!-- The line the surface printed for this decision, replayed verbatim, so the
       web reads exactly what the terminal said. When the surface printed none -
       a gate still open, a question whose overlay draws no line, an approval
       answered somewhere that keeps no line - there is nothing here either: the
       gate is the tool call, and its card already says what state it is in.
       Inventing a sentence would put words on screen the user never saw. -->
  <div
    v-if="line"
    class="flex items-start gap-2 px-1 text-[12px] leading-relaxed"
    :class="toneClass"
    :data-approval-id="block.actionId"
    :data-approval-status="block.status"
  >
    <span class="min-w-0 whitespace-pre-wrap">{{ line }}</span>
  </div>
</template>

<script setup lang="ts">
/**
 * The approval line: one status row, the same one the terminal prints under the
 * call it gated ("✔ You approved …" / "✗ You canceled …"). It is deliberately
 * not a card of its own: in the terminal the gate *is* the tool call, so a box
 * here would show the same thing twice.
 */
import { computed } from 'vue'
import { normalizeApprovalDecision } from '@/composables/useChatStream'

const props = defineProps<{
  block: {
    actionId: string
    actionKind: string
    status: string
    confirmation?: string
    message?: string
  }
}>()

const line = computed(() => String(props.block.confirmation ?? '').trim())

const decision = computed(() => normalizeApprovalDecision(props.block.status))

const toneClass = computed(() => {
  switch (decision.value) {
    case 'denied':
    case 'cancelled':
    case 'canceled':
      return 'text-[var(--forebrain-danger)]'
    default:
      return 'text-[var(--forebrain-text-2)]'
  }
})
</script>
