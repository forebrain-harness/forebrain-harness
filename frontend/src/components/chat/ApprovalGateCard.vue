<template>
  <!-- One approval question, drawn from its family: the question says what is
       about to happen, the facts carry only what this decision turns on, and
       every button says its verb and how far the yes reaches. No tool name, no
       action id, no permission-memory box — the button that remembers already
       names what it remembers. -->
  <!-- The card's shape is the library's confirmation: it decides *when* the
       request is on screen (`ConfirmationRequest` renders it only while the
       decision is outstanding), and it draws the title and the verdict row.
       This card supplies the question, the facts and the engine's own decision
       options. -->
  <Confirmation
    :state="'approval-requested'"
    :approval="{ id: action.id }"
    :class="['approval-gate', '!gap-0', '!rounded-lg', '!px-3', '!py-2']"
    :data-testid="`approval-gate-${gate.facts.family}`"
  >
    <ConfirmationRequest>
      <ConfirmationActions class="!mt-0 !w-full !justify-between !self-stretch !gap-3">
        <ConfirmationTitle class="min-w-0 !text-sm !font-medium !text-[var(--forebrain-text)]">
          {{ gate.question }}
        </ConfirmationTitle>
        <div class="flex shrink-0 flex-wrap items-center justify-end gap-2">
          <ConfirmationAction
            v-for="(option, index) in options"
            :key="`${action.id}-${option.decision}-${index}`"
            :class="isPrimary(option, index) ? 'forebrain-btn forebrain-btn-primary text-xs' : 'forebrain-btn forebrain-btn-ghost text-xs'"
            :disabled="submitting"
            :data-testid="`pending-decision-${option.decision}`"
            @click="submit(option)"
          >
            {{ decisionLabel(option) }}
          </ConfirmationAction>
        </div>
      </ConfirmationActions>
    <button
      v-if="requester && canOpenAgent"
      type="button"
      class="mt-1 block max-w-full truncate text-left text-xs text-[var(--forebrain-text-2)] hover:text-[var(--forebrain-brand-1)] disabled:pointer-events-none"
      :disabled="!action.agentId"
      @click="action.agentId && emit('open-agent', action.agentId)"
    >{{ requester }}</button>
    <div v-else-if="requester" class="mt-1 truncate text-xs text-[var(--forebrain-text-2)]">{{ requester }}</div>
    <div
      v-for="(fact, index) in gate.facts.facts"
      :key="`${action.id}-fact-${index}`"
      class="mt-1 break-words pl-4 text-[12px] text-[var(--forebrain-text-2)]"
      data-testid="approval-gate-fact"
    >{{ fact }}</div>
    <!-- The note's own body, rendered (prototype .memo, :490): a memory note is
         Markdown, so its marks are drawn rather than printed. -->
    <div
      v-if="gate.facts.bodyMarkdown"
      class="mt-2 max-h-[132px] overflow-y-auto break-words rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] px-3 py-2 pl-4 text-[13px] leading-relaxed text-[var(--forebrain-text-2)]"
      data-testid="approval-gate-body"
    ><MessageResponse :content="gate.facts.bodyMarkdown" /></div>
    <div
      v-if="diffRows.length"
      class="gate-diff mt-2 max-h-64 overflow-auto rounded-lg border border-[var(--forebrain-divider)] font-mono text-[12px] leading-relaxed"
      data-testid="approval-gate-diff"
    >
      <div
        v-for="(row, index) in diffRows"
        :key="`${action.id}-diff-${index}`"
        :class="row[0] === '+' ? 'gate-diff-add' : row[0] === '-' ? 'gate-diff-del' : 'gate-diff-ctx'"
      >{{ row[1] === '' ? ' ' : row[0] + row[1] }}</div>
    </div>
    <div v-if="error" class="mt-2 text-xs text-[var(--forebrain-danger)]" role="alert">{{ error }}</div>
    </ConfirmationRequest>
  </Confirmation>
</template>

<script setup lang="ts">
/**
 * The card every approval family shares: the thread's ApprovalCard hands it
 * the action and the decision plumbing, so a gate is asked the same way
 * wherever it is read. The family's facts and question come from
 * approvalGates; the buttons are the engine's own decision options in its own
 * order, each labelled by the shared composable, and exactly one of them is
 * primary.
 */
import { MessageResponse } from '@repo/elements/message'
import {
  Confirmation, ConfirmationAction, ConfirmationActions, ConfirmationRequest, ConfirmationTitle,
} from '@repo/elements/confirmation'
import { computed } from 'vue'
import { approvalFacts, approvalFamily, approvalQuestion, parseApprovalPayload } from '@/lib/approvalGates'
import { permissionSuggestionFromAction, type ApprovalDecisionOption } from '@/lib/approvalSuggestions'
import type { ActionRow } from '@/composables/usePendingActions'

const props = withDefaults(defineProps<{
  /** The pending action this card decides. */
  action: ActionRow
  /** The engine's decision options, in its order. */
  options: ApprovalDecisionOption[]
  /** Button copy per option, localized to the family by the shared composable. */
  decisionLabel: (option: ApprovalDecisionOption) => string
  /** Submits one decision through the shared composable. */
  submit: (option: ApprovalDecisionOption) => void
  /** Whether a decision for this action is in flight. */
  submitting?: boolean
  /** The last failed decision's error, when one landed. */
  error?: string
  /** Who is blocked on this approval, when a subagent raised it. */
  requester: string
  /** Whether the requester line can open that subagent's own view. */
  canOpenAgent?: boolean
}>(), { submitting: false, error: '', requester: '', canOpenAgent: false })

const emit = defineEmits<{ (e: 'open-agent', agentId: string): void }>()

const gate = computed(() => {
  const payload = parseApprovalPayload(props.action.payloadJson)
  const suggestion = permissionSuggestionFromAction(props.action)
  const family = approvalFamily(props.action.kind, payload, suggestion)
  return { facts: approvalFacts(family, payload, suggestion), question: approvalQuestion(family, props.action.kind, payload) }
})

// Forty rows is the most one approval asks anyone to read; past that the diff
// scrolls inside its box instead of growing the card.
const diffRows = computed(() => (gate.value.facts.diff ?? []).slice(0, 40))

// One primary: the family's own yes — accept, or the grant a permission
// request starts with, or the session scope when that is all it offers.
function isPrimary(option: ApprovalDecisionOption, index: number): boolean {
  if (option.decision === 'accept' || option.decision === 'grant_for_turn' || option.decision === 'accept_for_session') {
    return !props.options.slice(0, index).some((earlier) =>
      earlier.decision === 'accept' || earlier.decision === 'grant_for_turn' || earlier.decision === 'accept_for_session')
  }
  return false
}
</script>

<style scoped>
.gate-diff > div {
  padding: 0 8px;
  white-space: pre;
}
.gate-diff-add {
  background: var(--forebrain-success-soft);
  color: var(--forebrain-success);
}
.gate-diff-del {
  color: var(--forebrain-danger);
}
.gate-diff-ctx {
  color: var(--forebrain-text-2);
}
</style>
