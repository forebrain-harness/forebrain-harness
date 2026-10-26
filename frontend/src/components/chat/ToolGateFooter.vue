<template>
  <!-- The question and its answers, closing the waiting card on a tinted footer
       (prototype .gate-ask / .gate-acts / .gate-fb, :676-691): the question
       first, the engine's own decisions in its own order, and — for the cancel
       family — the refusal's words field under the verdict that opened it. The
       shell is the library's confirmation, as every gate's has been; it decides
       when the request is on screen, this card supplies the rest. -->
  <Confirmation
    state="approval-requested"
    :approval="{ id: action.id }"
    class="gate-ask"
    :data-testid="`approval-gate-${gate.facts.family}`"
  >
    <ConfirmationRequest>
      <ConfirmationTitle class="q">{{ gate.question }}</ConfirmationTitle>
      <ConfirmationActions class="gate-acts">
        <template v-for="(option, index) in options" :key="`${action.id}-${option.decision}-${index}`">
          <!-- The allowing answers: the first is the primary one. -->
          <ConfirmationAction
            v-if="isAllow(option)"
            class="gate-btn"
            :class="isPrimary(option, index) ? 'gate-btn-pri' : ''"
            :disabled="submitting"
            :data-testid="`pending-decision-${option.decision}`"
            @click="allow(option)"
          >{{ decisionLabel(option) }}</ConfirmationAction>
          <!-- decline: the turn goes on without the grant, so it acts at once. -->
          <ConfirmationAction
            v-else-if="option.decision === 'decline'"
            class="gate-btn"
            :disabled="submitting"
            :data-testid="`pending-decision-${option.decision}`"
            @click="submit(option)"
          >{{ decisionLabel(option) }}</ConfirmationAction>
          <!-- cancel: the refusal, in its family's own verb. A toggle that
               opens the words field — written, it refuses this one call and
               the turn carries on; empty, or Esc, it refuses and stops. -->
          <ConfirmationAction
            v-else-if="option.decision === 'cancel'"
            class="gate-btn"
            :class="refusalOpen ? 'gate-btn-on' : ''"
            :disabled="submitting"
            :aria-expanded="refusalOpen"
            :data-testid="`pending-decision-${option.decision}`"
            @click="toggleRefusal"
          >{{ decisionLabel(option) }}</ConfirmationAction>
        </template>
      </ConfirmationActions>
      <div v-if="refusalOpen" class="gate-fb" data-testid="gate-feedback">
        <input
          :ref="bindField"
          v-model="words"
          class="gate-fb-input"
          type="text"
          autocomplete="off"
          :disabled="submitting"
          :aria-label="t('chat.refuseWordsPlaceholder')"
          :placeholder="t('chat.refuseWordsPlaceholder')"
          data-testid="gate-feedback-input"
          @keydown.enter.prevent="sendRefusal"
          @keydown.esc.stop.prevent="refuseAndStop(action)"
        />
        <button
          type="button"
          class="gate-btn gate-btn-pri gate-fb-send"
          :disabled="submitting"
          v-tip:top="sendTip"
          data-testid="gate-feedback-send"
          @click="sendRefusal"
        >{{ sendLabel }}</button>
      </div>
      <div v-if="error" class="gate-error" role="alert">{{ error }}</div>
    </ConfirmationRequest>
  </Confirmation>
</template>

<script setup lang="ts">
/**
 * The one component that answers a waiting call (plan 138): the facts sit in
 * the card's panel above (ToolGateFacts), and this footer holds the question
 * and the decision. The refusal follows the engine's decision for the family —
 * `cancel` opens the words field (Claude Code's "No" row: written, refuse this
 * one call and the turn carries on; empty, or Esc, refuse and stop the turn),
 * `decline` is a plain button that acts at once — so a new family only has to
 * offer one of the two and the footer picks the right interaction by itself.
 */
import { computed, nextTick, ref } from 'vue'
import {
  Confirmation, ConfirmationAction, ConfirmationActions, ConfirmationRequest, ConfirmationTitle,
} from '@repo/elements/confirmation'
import { approvalGateOf } from '@/lib/approvalGates'
import type { ApprovalDecisionOption } from '@/lib/approvalSuggestions'
import type { ActionRow } from '@/composables/usePendingActions'
import { t } from '@/locales'

const props = withDefaults(defineProps<{
  /** The pending action this footer decides. */
  action: ActionRow
  /** The engine's decision options, in its order. */
  options: ApprovalDecisionOption[]
  /** Button copy per option, localized to the family by the shared composable. */
  decisionLabel: (option: ApprovalDecisionOption) => string
  /** Submits one decision through the shared composable. */
  submit: (option: ApprovalDecisionOption) => void
  /** A refusal with the user's words: this one call, the turn carries on. */
  refuseWithWords: (action: ActionRow, words: string) => void | Promise<unknown>
  /** A refusal without words: the call is refused and the turn stops. */
  refuseAndStop: (action: ActionRow) => void | Promise<unknown>
  /** Whether a decision for this action is in flight. */
  submitting?: boolean
  /** The last failed decision's error, when one landed. */
  error?: string
}>(), { submitting: false, error: '' })

const gate = computed(() => approvalGateOf(props.action))

// The allowing answers, in the order the engine offered them.
function isAllow(option: ApprovalDecisionOption): boolean {
  return option.decision !== 'cancel' && option.decision !== 'decline'
}

// One primary: the family's own yes — the first allowing answer.
function isPrimary(option: ApprovalDecisionOption, index: number): boolean {
  return isAllow(option) && !props.options.slice(0, index).some(isAllow)
}

/** The words field opens on the refusal's verdict and closes on any other
 *  decision, taking the unfinished draft with it. */
const refusalOpen = ref(false)
const words = ref('')
const field = ref<HTMLInputElement | null>(null)

function bindField(el: unknown) {
  field.value = (el as HTMLInputElement | null) ?? null
}

function toggleRefusal() {
  refusalOpen.value = !refusalOpen.value
  if (refusalOpen.value) void nextTick(() => field.value?.focus())
}

/** Any allowing answer folds the words field away: the verdict came first. */
function allow(option: ApprovalDecisionOption) {
  refusalOpen.value = false
  words.value = ''
  props.submit(option)
}

/** The send names what it will do — written, send the words; empty, refuse and
 *  stop the turn — so no sentence explains it (prototype :4082-4086). */
const sendLabel = computed(() => (words.value.trim() ? t('chat.refuseSend') : t('chat.refuseAndStop')))
const sendTip = computed(() => (words.value.trim() ? t('chat.refuseSendTipWords') : t('chat.refuseSendTipStop')))

function sendRefusal() {
  const typed = words.value.trim()
  if (!typed) {
    void props.refuseAndStop(props.action)
    return
  }
  void props.refuseWithWords(props.action, typed)
}
</script>

<style scoped>
/* The prototype's .gate-ask (:676-677): a grid 9px apart on the brand's tint,
   its own top edge against the facts panel above it. The confirmation shell's
   own frame, gap and radius step aside for it. */
.gate-ask {
  display: grid;
  gap: 9px;
  padding: 10px 12px 11px;
  background: var(--forebrain-brand-soft);
  border: 0;
  border-radius: 0;
  border-top: 1px solid var(--forebrain-brand-border);
}

.gate-ask :deep(.q) {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
  line-height: 1.5;
  color: var(--forebrain-text);
}

/* The prototype's .gate-acts (:679-681): left-aligned, wrapping, 6px apart —
   not the confirmation's own right-aligned row. */
.gate-acts {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-start;
  align-self: stretch;
  width: 100%;
  gap: 6px;
}

/* The prototype's .btn.sm (:179): 26px, 9px of side padding, 12px type. */
.gate-btn {
  flex: none;
  height: 26px;
  padding: 0 9px;
  border-radius: 6px;
  border: 1px solid var(--forebrain-divider-strong);
  background: var(--forebrain-page-bg);
  color: var(--forebrain-text);
  font-size: 12px;
  font-weight: 500;
  box-shadow: none;
  white-space: nowrap;
  cursor: pointer;
}

.gate-btn:hover {
  background: var(--forebrain-bg-alt);
}

.gate-btn:disabled {
  opacity: 0.6;
  cursor: default;
}

/* The family's own yes (prototype .btn.pri). */
.gate-btn-pri {
  border-color: var(--forebrain-brand-1);
  background: var(--forebrain-brand-1);
  color: #f5ffff;
}

.gate-btn-pri:hover {
  background: var(--forebrain-brand-hover, var(--forebrain-brand-1));
  border-color: var(--forebrain-brand-hover, var(--forebrain-brand-1));
}

/* The refusal's pressed state (prototype .app .gate-acts .btn.on, :681): the
   page's own white, the brand's border and word, an inner keyline. */
.gate-btn-on {
  background: var(--forebrain-page-bg);
  border-color: var(--forebrain-brand-1);
  color: var(--forebrain-brand-1);
  box-shadow: inset 0 0 0 1px var(--forebrain-brand-1);
}

/* The refusal's words field (prototype .gate-fb, :685-691): a 28px input, its
   own frame, and the send standing apart at 6px. */
.gate-fb {
  display: flex;
  align-items: center;
  gap: 6px;
}

.gate-fb-input {
  flex: 1;
  min-width: 0;
  height: 28px;
  box-sizing: border-box;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 6px;
  padding: 0 9px;
  background: var(--forebrain-page-bg);
  font: inherit;
  font-size: 12.5px;
  color: var(--forebrain-text);
}

.gate-fb-input::placeholder {
  color: var(--forebrain-muted-text);
}

.gate-fb-input:focus-visible {
  outline: 2px solid var(--forebrain-brand-1);
  outline-offset: 1px;
}

.gate-error {
  font-size: 12px;
  color: var(--forebrain-danger);
}
</style>
