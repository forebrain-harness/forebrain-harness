<template>
  <!-- Decided record: the line the surface printed for this decision, replayed
       verbatim, so the web reads exactly what the terminal said. When the
       surface printed none - a gate still open, a question whose overlay draws
       no line, an approval answered somewhere that keeps no line - there is
       nothing here either: the gate is the tool call, and its card already
       says what state it is in. Inventing a sentence would put words on screen
       the user never saw. -->
  <div
    v-if="block && line"
    class="flex items-start gap-2 px-1 text-[12px] leading-relaxed"
    :class="toneClass"
    :data-approval-id="block.actionId"
    :data-approval-status="block.status"
  >
    <span class="min-w-0 whitespace-pre-wrap">{{ line }}</span>
  </div>
  <!-- What the thread's tail still holds (plan 138): the agent's questions,
       answered here as they always were, and — for a request no tool card of
       this conversation carries — the waiting card's own frame, so nothing
       that parks the run is ever left without a way to answer it. The gates
       that belong to calls hang on their cards in the timeline; the plan
       approval waits where the input is. -->
  <div v-else-if="!block && store && (loadError || pendingAsk.length || fallbackApprovals.length)" class="my-2 space-y-2">
    <Alert v-if="loadError" variant="destructive" data-testid="pending-load-error">
      <CircleAlert class="size-4" />
      <AlertTitle>{{ t('chat.approvalsLoadFailed') }}</AlertTitle>
      <AlertDescription class="space-y-2">
        <div class="whitespace-pre-line">{{ loadError }}</div>
        <Button
          type="button"
          class="forebrain-btn forebrain-btn-ghost h-auto text-xs"
          :disabled="loading"
          @click="load"
        >{{ t('chat.retry') }}</Button>
      </AlertDescription>
    </Alert>
    <div v-for="a in fallbackApprovals" :key="a.id" class="gate-fallback" data-testid="pending-approval" :data-action-id="a.id">
      <!-- The frame of a waiting card for a call this conversation draws no
           card for: the brand border, the question as the head, the facts and
           the footer the gated cards carry — one shape for one ask. -->
      <div class="gate-fallback-head">{{ approvalGate(a).question }}</div>
      <ToolGateFacts
        :action="a"
        :gate="approvalGate(a)"
        :requester="requesterLabel(a)"
        :can-open-agent="canOpenAgent"
        @open-agent="(agentId) => emit('open-agent', agentId)"
      />
      <ToolGateFooter
        :action="a"
        :options="approvalDecisionOptions(a)"
        :decision-label="(option) => approvalDecisionLabel(option, a, approvalGate(a).facts)"
        :submit="(option) => submitApprovalDecision(a, option)"
        :refuse-with-words="refuseWithWords"
        :refuse-and-stop="refuseAndStop"
        :submitting="!!submitting[a.id]"
        :error="errors[a.id] ?? ''"
      />
    </div>
    <div v-for="qa in pendingAsk" :key="qa.id" class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-2 space-y-2" data-testid="pending-question" :data-action-id="qa.id">
      <!-- The ask card's head (:3299, :1270⑤): what the card is doing. The
           questions themselves are the tab row's business, one line below, and
           the head prints no count of them — the design has none either, and a
           count that ran past its own total (the review screen sits past the
           last question) said nothing true. -->
      <div class="xp-head">
        <MessageSquareText class="xp-head-icon" aria-hidden="true" />
        <span class="xp-head-title">{{ t('chat.askRunning') }}</span>
      </div>
      <button
        v-if="requesterLabel(qa) && canOpenAgent"
        type="button"
        class="block max-w-full truncate text-left text-xs text-[var(--forebrain-text-2)] hover:text-[var(--forebrain-brand-1)] disabled:pointer-events-none"
        :disabled="!qa.agentId"
        @click="qa.agentId && emit('open-agent', qa.agentId)"
      >{{ requesterLabel(qa) }}</button>
      <div v-else-if="requesterLabel(qa)" class="truncate text-xs text-[var(--forebrain-text-2)]">{{ requesterLabel(qa) }}</div>
      <!-- The questions, one per screen behind the tab bar the terminal's
           overlay asks with; the answers live in the shared store below, so
           the workshop's panel shows the same ones answered. -->
      <AskTabs
        :questions="qa.form.questions"
        :answers="answers[qa.id] ?? {}"
        :others="others[qa.id] ?? {}"
        :submitting="!!submitting[qa.id]"
        @set-answer="(questionId, value) => setAskAnswer(qa.id, questionId, value)"
        @set-other="(questionId, value) => setAskOther(qa.id, questionId, value)"
        @submit="submitAsk(qa.id)"
        @cancel="cancelAsk(qa.id)"
        @chat-about="chatAboutQuestion(qa.id)"
      />
      <div v-if="errors[qa.id]" class="text-xs text-[var(--forebrain-danger)]" role="alert">{{ errors[qa.id] }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The conversation's approval surface, in two states from one component:
 *
 * - a decided record replays the one status line the terminal printed under
 *   the call it gated, verbatim - deliberately not a card of its own, because
 *   in the terminal the gate *is* the tool call;
 * - the thread's tail holds what no tool card carries (plan 138): the agent's
 *   questions, and a request with no card of its own in the waiting card's
 *   own frame. Everything reads the one pending-actions store the page
 *   provides, so a gate answered here is answered on the card it belongs to
 *   and everywhere else it shows.
 */
import { computed } from 'vue'
import { CircleAlert, MessageSquareText } from 'lucide-vue-next'
import { Alert, AlertDescription, AlertTitle } from '@repo/shadcn-vue/components/ui/alert'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { normalizeApprovalDecision } from '@/composables/useChatStream'
import { injectPendingActions } from '@/composables/providePendingActions'
import type { ActionRow } from '@/composables/usePendingActions'
import type { ApprovalFacts } from '@/lib/approvalGates'
import type { ApprovalDecisionOption } from '@/lib/approvalSuggestions'
import ToolGateFacts from './ToolGateFacts.vue'
import ToolGateFooter from './ToolGateFooter.vue'
import AskTabs from './AskTabs.vue'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  /** A decided approval record from the timeline; a card holding one renders
   *  its printed line and nothing else. */
  block?: {
    actionId: string
    actionKind: string
    status: string
    confirmation?: string
    message?: string
  }
  /** The ids whose gates hang on a tool card of this view: the tail's frame
   *  holds only what is not one of them and belongs to this view's agent. */
  anchoredActionIds?: string[] | Set<string>
  /** Whether a subagent's request can open that subagent's own view here. */
  canOpenAgent?: boolean
  /** Which agent's tail this is: the conversation's own (empty) or one
   *  subagent's — the fallback frame then holds only that agent's requests. */
  agentId?: string
}>(), { block: undefined, anchoredActionIds: () => [], canOpenAgent: false, agentId: '' })

const emit = defineEmits<{
  (e: 'open-agent', agentId: string): void
  /** 「和它聊聊」was picked: the question is dropped and the composer is the
   *  place to say it in the reader's own words. */
  (e: 'focus-composer'): void
}>()

const { t } = useI18n()

const line = computed(() => String(props.block?.confirmation ?? '').trim())

const decision = computed(() => normalizeApprovalDecision(props.block?.status ?? ''))

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

// The page's one store; a card drawn outside a provider keeps the decided
// line, which needs none of it. The tail's bindings unwrap it here, so the
// template reads plain values and a missing store reads as "nothing pending".
const store = injectPendingActions()

const pendingAsk = computed(() => store?.pendingAsk.value ?? [])
const answers = computed(() => store?.answers.value ?? {})
const others = computed(() => store?.others.value ?? {})
const submitting = computed(() => store?.submitting.value ?? {})
const errors = computed(() => store?.errors.value ?? {})
const loading = computed(() => store?.loading.value ?? false)
const loadError = computed(() => store?.loadError.value ?? null)

function load() {
  void store?.load()
}

function submitAsk(actionId: string) {
  if (store) void store.submitAsk(actionId)
}

function cancelAsk(actionId: string) {
  if (store) void store.cancelAsk(actionId)
}

function setAskAnswer(actionId: string, questionId: string, value: string | string[]) {
  store?.setAskAnswer(actionId, questionId, value)
}

function setAskOther(actionId: string, questionId: string, value: string) {
  store?.setAskOther(actionId, questionId, value)
}

function submitApprovalDecision(action: ActionRow, option: ApprovalDecisionOption) {
  if (store) void store.submitApprovalDecision(action, option)
}

function refuseWithWords(action: ActionRow, words: string) {
  if (store) void store.refuseWithWords(action, words)
}

function refuseAndStop(action: ActionRow) {
  if (store) void store.refuseAndStop(action)
}

function approvalDecisionOptions(action: ActionRow): ApprovalDecisionOption[] {
  return store?.approvalDecisionOptions(action) ?? []
}

function approvalDecisionLabel(option: ApprovalDecisionOption, action?: ActionRow, facts?: ApprovalFacts | null): string {
  return store?.approvalDecisionLabel(option, action, facts) ?? ''
}

function approvalGate(action: ActionRow) {
  return store?.approvalGate(action) ?? { facts: { family: 'fallback' as const, facts: [] }, question: '' }
}

function requesterLabel(a: Pick<ActionRow, 'agentId' | 'subagentType'>): string {
  return store?.requesterLabel(a) ?? ''
}

/** The requests this view's tail answers: not one anchored to a card above,
 *  and this view's own — the conversation's own tail holds its own requests
 *  (its plan approval waits where the input is, never here), a subagent's
 *  tail holds that subagent's, plan approval included. */
const fallbackApprovals = computed<ActionRow[]>(() => {
  if (!store) return []
  const anchored = new Set(props.anchoredActionIds)
  return store.pendingApprovals.value.filter((a) => {
    if (anchored.has(a.id)) return false
    if (props.agentId) return String(a.agentId ?? '').trim() === props.agentId
    if (store.isExitPlan(a)) return false
    return !String(a.agentId ?? '').trim()
  })
})

/** 「和它聊聊」: the question is withdrawn, and the composer takes the turn. One
 *  gesture, two halves — the run must stop waiting before the reader can say
 *  anything, so the cancel goes out first and the caret follows it. */
function chatAboutQuestion(actionId: string) {
  void cancelAsk(actionId)
  emit('focus-composer')
}
</script>

<style scoped>
/* The gate's head, in the tool card's shape (prototype :1276): the card's name
   on the alt surface, its state on the right, one hairline under it. */
.xp-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 11px;
  margin: -8px -12px 0;
  background: var(--forebrain-bg-alt);
  border-bottom: 1px solid var(--forebrain-divider);
  font-size: 12px;
}

.xp-head-icon {
  width: 14px;
  height: 14px;
  flex: none;
  color: var(--forebrain-muted-text);
}

.xp-head-title {
  flex: 1;
  min-width: 0;
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
  color: var(--forebrain-text-2);
}

/* The waiting card's own frame for a request no card carries (plan 138,
   Step 1.3): the brand border of a waiting call, the question as its head,
   the facts as its panel, the footer closing it — the same three parts a
   gated call's card draws, in one frame. */
.gate-fallback {
  border: 1px solid var(--forebrain-brand-border);
  border-radius: 10px;
  background: var(--forebrain-page-bg);
  display: grid;
}

.gate-fallback-head {
  padding: 8px 11px;
  border-bottom: 1px solid var(--forebrain-divider);
  font-size: 13px;
  font-weight: 500;
  color: var(--forebrain-text);
}

.gate-fallback > :deep(.gate-ask) {
  border-radius: 0 0 9px 9px;
}
</style>
