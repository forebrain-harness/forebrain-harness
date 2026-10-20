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
  <!-- Pending requests, in the conversation's message flow where the run is
       parked on them - the same place the terminal's approval card sits, so
       the decision is made where the work stopped. Everything carries over
       from the panel the workshop still uses: same composable, same
       endpoints, same testids. -->
  <div v-else-if="!block && (loadError || pendingAsk.length || pendingApprovals.length)" class="my-2 space-y-2">
    <Alert v-if="loadError" variant="destructive" data-testid="pending-load-error">
      <CircleAlert class="size-4" />
      <AlertTitle>{{ t('chat.approvalsLoadFailed') }}</AlertTitle>
      <AlertDescription class="space-y-2">
        <div class="whitespace-pre-line">{{ loadError }}</div>
        <button
          type="button"
          class="forebrain-btn forebrain-btn-ghost text-xs"
          :disabled="loading || !sessionId"
          @click="load"
        >{{ t('chat.retry') }}</button>
      </AlertDescription>
    </Alert>
    <div v-for="a in pendingApprovals" :key="a.id" class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-2" data-testid="pending-approval" :data-action-id="a.id">
      <template v-if="isExitPlan(a)">
        <div data-testid="exit-plan-approval">
          <div class="flex items-center justify-between gap-3">
            <div class="text-sm font-medium text-[var(--forebrain-text)]">{{ t('chat.exitPlanMode') }}</div>
            <div class="text-xs text-[var(--forebrain-muted-text)]">{{ t('chat.exitPlanWaiting') }}</div>
          </div>
          <div
            v-if="exitPlanRequest(a)?.planText?.trim()"
            class="mt-2 max-h-56 overflow-y-auto rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] px-3 py-2 text-[13px] leading-relaxed"
            data-testid="exit-plan-text"
          >
            <MessageResponse :content="exitPlanRequest(a)!.planText!.trim()" />
          </div>
          <div
            v-for="review in exitPlanRequest(a)?.planReviews ?? []"
            :key="`${a.id}-review-${review.model}`"
            class="mt-2 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-2"
          >
            <div class="text-xs text-[var(--forebrain-muted-text)]">
              {{ t('chat.exitPlanReviewBy', { model: reviewModelLabel(review), duration: formatToolDuration(review.durationMs ?? 0) }) }}
            </div>
            <div class="mt-1 text-[13px] leading-relaxed"><MessageResponse :content="review.text" /></div>
          </div>
          <div class="mt-2">
            <div v-if="exitPlanRequest(a)?.planReviewActive" class="space-y-1">
              <div class="flex items-center gap-2 text-[13px] text-[var(--forebrain-text)]">
                <span class="h-1.5 w-1.5 animate-pulse rounded-full bg-[var(--forebrand-1,var(--forebrain-brand-1))]" />
                <span>{{ t('chat.exitPlanReviewRunning', { model: activeReviewLabel(a) }) }}</span>
                <span class="flex-1" />
                <button
                  class="forebrain-btn forebrain-btn-ghost text-xs"
                  type="button"
                  data-testid="exit-plan-stop-review"
                  @click="stopReview(a)"
                >{{ t('chat.exitPlanReviewStop') }}</button>
              </div>
              <div class="text-xs text-[var(--forebrain-muted-text)]">{{ t('chat.exitPlanReviewHint') }}</div>
            </div>
            <div v-else class="space-y-2">
              <div class="flex flex-wrap gap-2">
                <button
                  class="forebrain-btn forebrain-btn-primary text-xs"
                  type="button"
                  :disabled="submitting[a.id]"
                  data-testid="exit-plan-approve-clear"
                  @click="approveExitPlan(a, true)"
                >{{ clearContextLabel }}</button>
                <button
                  class="forebrain-btn forebrain-btn-ghost text-xs"
                  type="button"
                  :disabled="submitting[a.id]"
                  data-testid="exit-plan-approve"
                  @click="approveExitPlan(a, false)"
                >{{ t('chat.exitPlanApprove') }}</button>
                <button
                  class="forebrain-btn forebrain-btn-ghost text-xs"
                  type="button"
                  :disabled="submitting[a.id]"
                  data-testid="exit-plan-keep-planning"
                  @click="keepPlanning(a)"
                >{{ t('chat.exitPlanKeepPlanning') }}</button>
                <button
                  v-if="(exitPlanRequest(a)?.planReviewModels ?? []).length"
                  class="forebrain-btn forebrain-btn-ghost text-xs"
                  type="button"
                  :disabled="submitting[a.id]"
                  data-testid="exit-plan-ask-review"
                  @click="toggleModelPicker(a)"
                >{{ t('chat.exitPlanAskReview') }}</button>
              </div>
              <input
                v-model="denyFeedback[a.id]"
                class="w-full rounded-md border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-2 py-1 text-xs text-[var(--forebrain-text-2)]"
                :disabled="submitting[a.id]"
                :placeholder="t('chat.exitPlanFeedbackPlaceholder')"
              />
              <div
                v-if="modelPicking[a.id]"
                class="overflow-hidden rounded-lg border border-[var(--forebrain-divider)]"
              >
                <button
                  v-for="model in exitPlanRequest(a)?.planReviewModels ?? []"
                  :key="`${a.id}-model-${model.provider}-${model.model}`"
                  type="button"
                  class="flex w-full items-center justify-between border-t border-[var(--forebrain-divider)] px-3 py-1.5 text-left text-[13px] text-[var(--forebrain-text)] first:border-t-0 hover:bg-[var(--forebrain-bg-alt)] disabled:opacity-60"
                  :disabled="submitting[a.id]"
                  data-testid="plan-review-model"
                  @click="requestReview(a, model)"
                >
                  <span>{{ modelLabel(model) }}</span>
                  <span v-if="model.current" class="text-xs text-[var(--forebrain-muted-text)]">{{ t('chat.exitPlanReviewCurrent') }}</span>
                </button>
              </div>
            </div>
          </div>
          <div v-if="errors[a.id]" class="mt-2 text-xs text-[var(--forebrain-danger)]" role="alert">{{ errors[a.id] }}</div>
        </div>
      </template>
      <template v-else-if="isWebFetchAction(a)">
        <!-- A fetch gate asks one thing about one page, so the card is the
             question, the page, and the three ways out - no title layer, no
             second line falling back to an id, no permission-memory box: the
             button that remembers this approval already names the host it
             remembers. -->
        <div data-testid="web-fetch-approval">
          <div class="flex items-center justify-between gap-3">
            <div class="min-w-0 text-sm font-medium text-[var(--forebrain-text)]">{{ t('chat.webFetchQuestion') }}</div>
            <div class="flex shrink-0 items-center gap-2">
              <button
                v-for="(option, index) in approvalDecisionOptions(a)"
                :key="`${a.id}-${option.decision}-${index}`"
                :class="option.decision === 'accept' ? 'forebrain-btn forebrain-btn-primary text-xs' : 'forebrain-btn forebrain-btn-ghost text-xs'"
                type="button"
                :disabled="submitting[a.id]"
                :data-testid="`pending-decision-${option.decision}`"
                @click="submitApprovalDecision(a, option)"
              >
                {{ approvalDecisionLabel(option, a) }}
              </button>
            </div>
          </div>
          <button
            v-if="requesterLabel(a) && canOpenAgent"
            type="button"
            class="mt-1 block max-w-full truncate text-left text-xs text-[var(--forebrain-text-2)] hover:text-[var(--forebrain-brand-1)] disabled:pointer-events-none"
            :disabled="!a.agentId"
            @click="a.agentId && emit('open-agent', a.agentId)"
          >{{ requesterLabel(a) }}</button>
          <div v-else-if="requesterLabel(a)" class="mt-1 truncate text-xs text-[var(--forebrain-text-2)]">{{ requesterLabel(a) }}</div>
          <div class="mt-2 break-all pl-4 font-mono text-[12px] text-[var(--forebrain-text-2)]" data-testid="web-fetch-page">{{ webFetchPage(a) }}</div>
        </div>
      </template>
      <template v-else>
        <div class="flex items-center justify-between gap-3">
          <div class="min-w-0">
            <div class="text-sm font-medium text-[var(--forebrain-text)] truncate">{{ approvalKindLabel(a.kind) }}</div>
            <button
              v-if="requesterLabel(a) && canOpenAgent"
              type="button"
              class="block max-w-full truncate text-left text-xs text-[var(--forebrain-text-2)] hover:text-[var(--forebrain-brand-1)] disabled:pointer-events-none"
              :disabled="!a.agentId"
              @click="a.agentId && emit('open-agent', a.agentId)"
            >{{ requesterLabel(a) }}</button>
            <div v-else-if="requesterLabel(a)" class="truncate text-xs text-[var(--forebrain-text-2)]">{{ requesterLabel(a) }}</div>
            <div class="text-xs text-muted-foreground truncate">{{ approvalHint(a) }}</div>
          </div>
          <div v-if="!approvalSuggestion(a)" class="flex shrink-0 items-center gap-2">
            <button class="forebrain-btn forebrain-btn-ghost" type="button" :disabled="submitting[a.id]" @click="denyAction(a.id)">{{ t('chat.deny') }}</button>
            <button class="forebrain-btn forebrain-btn-primary" type="button" :disabled="submitting[a.id]" data-testid="pending-approve" @click="approveAction(a.id)">{{ t('chat.approveOnce') }}</button>
          </div>
        </div>
        <input
          v-if="!approvalSuggestion(a)"
          v-model="denyFeedback[a.id]"
          class="mt-2 w-full rounded-md border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-2 py-1 text-xs text-[var(--forebrain-text-2)]"
          :disabled="submitting[a.id]"
          :placeholder="t('chat.otherOptional')"
        />
      </template>
      <div v-if="!isExitPlan(a) && !isWebFetchAction(a) && approvalSuggestion(a)" class="mt-2 space-y-2 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] p-2 text-xs text-[var(--forebrain-text-2)]">
        <div class="flex flex-wrap items-center gap-2">
          <span class="font-medium text-[var(--forebrain-text)]">{{ t('chat.permissionMemory') }}</span>
          <span class="font-mono text-[11px] text-[var(--forebrain-muted-text)]">{{ approvalSuggestionLabel(a) }}</span>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <button
            v-for="(option, index) in approvalDecisionOptions(a)"
            :key="`${a.id}-${option.decision}-${index}`"
            :class="option.decision === 'accept' ? 'forebrain-btn forebrain-btn-primary text-xs' : 'forebrain-btn forebrain-btn-ghost text-xs'"
            type="button"
            :disabled="submitting[a.id]"
            :data-testid="`pending-decision-${option.decision}`"
            @click="submitApprovalDecision(a, option)"
          >
            {{ approvalDecisionLabel(option) }}
          </button>
        </div>
      </div>
      <div v-if="errors[a.id]" class="mt-2 text-xs text-[var(--forebrain-danger)]" role="alert">{{ errors[a.id] }}</div>
    </div>
    <div v-for="qa in pendingAsk" :key="qa.id" class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-2 space-y-2" data-testid="pending-question" :data-action-id="qa.id">
      <button
        v-if="requesterLabel(qa) && canOpenAgent"
        type="button"
        class="block max-w-full truncate text-left text-xs text-[var(--forebrain-text-2)] hover:text-[var(--forebrain-brand-1)] disabled:pointer-events-none"
        :disabled="!qa.agentId"
        @click="qa.agentId && emit('open-agent', qa.agentId)"
      >{{ requesterLabel(qa) }}</button>
      <div v-else-if="requesterLabel(qa)" class="truncate text-xs text-[var(--forebrain-text-2)]">{{ requesterLabel(qa) }}</div>
      <div v-for="q in qa.form.questions" :key="q.id" class="space-y-1">
        <div class="text-sm font-medium text-[var(--forebrain-text)]">{{ q.prompt }}</div>
        <div class="flex flex-wrap gap-2">
          <label v-for="opt in q.options" :key="opt.id" class="inline-flex items-center gap-1 rounded-md border px-2 py-1 text-xs">
            <input
              :type="q.allowMultiple ? 'checkbox' : 'radio'"
              :name="`ask-${qa.id}-${q.id}`"
              :value="opt.id"
              v-model="answers[qa.id][q.id]"
            />
            <span>{{ opt.label }}</span>
          </label>
        </div>
        <div v-if="q.allowOther" class="mt-1">
          <input
            class="w-full rounded-md border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-2 py-1 text-xs text-[var(--forebrain-text-2)]"
            :placeholder="t('chat.otherOptional')"
            v-model="others[qa.id][q.id]"
          />
        </div>
      </div>
      <div class="flex justify-end">
        <button class="forebrain-btn forebrain-btn-primary" type="button" :disabled="submitting[qa.id]" @click="submitAsk(qa.id)">{{ t('common.submit') }}</button>
      </div>
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
 * - the requests the run is parked on, answered in the message flow itself:
 *   tool approvals with their permission-memory suggestions, the agent's
 *   questions, and the exit-plan approval with its review flow, loaded and
 *   submitted through the same pending-actions composable the workshop's
 *   panel uses.
 */
import { computed } from 'vue'
import { CircleAlert } from 'lucide-vue-next'
import { Alert, AlertDescription, AlertTitle } from '@repo/shadcn-vue/components/ui/alert'
import { MessageResponse } from '@repo/elements/message'
import { normalizeApprovalDecision, formatToolDuration } from '@/composables/useChatStream'
import { usePendingActions } from '@/composables/usePendingActions'
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
  /** The conversation whose pending requests this card answers. Without a
   *  block prop, the card hosts them in the thread. */
  sessionId?: string | null
  /** Bumped by the stream whenever the set of pending requests changed. */
  version?: number
  /** Whether a subagent's request can open that subagent's own view here. */
  canOpenAgent?: boolean
  /** How full the context window is; the clear-context choice says what it
   *  would free. Null when unknown. */
  contextUsedPercent?: number | null
}>(), { block: undefined, sessionId: null, version: 0, canOpenAgent: false, contextUsedPercent: null })

const emit = defineEmits<{ (e: 'open-agent', agentId: string): void }>()

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

const {
  pendingApprovals,
  pendingAsk,
  answers,
  others,
  submitting,
  errors,
  denyFeedback,
  modelPicking,
  loading,
  loadError,
  load,
  exitPlanRequest,
  isExitPlan,
  reviewModelLabel,
  modelLabel,
  activeReviewLabel,
  clearContextLabel,
  submitAsk,
  approveAction,
  submitApprovalDecision,
  denyAction,
  approveExitPlan,
  keepPlanning,
  toggleModelPicker,
  requestReview,
  stopReview,
  approvalSuggestion,
  approvalDecisionOptions,
  approvalDecisionLabel,
  isWebFetchAction,
  webFetchPage,
  approvalSuggestionLabel,
  approvalKindLabel,
  requesterLabel,
  approvalHint,
} = usePendingActions({
  sessionId: computed(() => props.sessionId ?? null),
  version: computed(() => props.version ?? 0),
  contextUsedPercent: computed(() => props.contextUsedPercent ?? null),
})
</script>
