<template>
  <!-- exit_plan_mode while it waits (plan 138): the terminal paints no card
       for it and the web follows — the plan and its verdicts take the input's
       place, the way the terminal's overlay takes its composer. Head: the plan
       and its file, the eye opening the whole plan in the drawer's plan tab.
       Body: the plan itself, scrolled and fading. Footer: the question and
       the four verdicts, the same tinted footer every approval closes on. -->
  <div
    class="xp-dock"
    role="region"
    :aria-label="t('chat.exitPlanAria')"
    data-testid="exit-plan-approval"
    @keydown.esc.stop.prevent="onEsc"
  >
    <div class="xp-dock-h">
      <Map class="size-3.5 shrink-0" aria-hidden="true" />
      <b>{{ t('chat.plan') }}</b>
      <span v-if="planPath" class="mono">{{ planPath }}</span>
      <span class="grow" />
      <button
        v-if="canOpenPlan"
        type="button"
        class="xp-eye"
        :aria-label="t('chat.showAll')"
        v-tip:top="t('chat.showAll')"
        data-testid="exit-plan-open-plan"
        @click="openPlan"
      >
        <Eye class="size-[15px]" aria-hidden="true" />
      </button>
    </div>
    <div v-if="planText.trim()" class="xp-doc" data-testid="exit-plan-text">
      <MessageResponse :content="planText" />
    </div>
    <div class="gate-ask">
      <!-- A review in flight leaves the footer one line and its stop; the
           review's own progress is the subagent card above, in the thread. -->
      <div v-if="reviewActive" class="xp-run">
        <LoaderCircle class="size-3.5 animate-spin" aria-hidden="true" />
        <span>{{ t('chat.exitPlanReviewRunning', { model: activeReviewLabel(action) }) }}</span>
        <span class="grow" />
        <button
          type="button"
          class="gate-btn"
          data-testid="exit-plan-stop-review"
          :disabled="submitting"
          @click="stopReview(action)"
        >{{ t('chat.exitPlanReviewStop') }}</button>
      </div>
      <!-- Choosing a reviewer: the list opens on the footer, the current one
           marked; Esc folds it back. -->
      <div v-else-if="picking" class="xp-pick">
        <span class="cap">{{ t('chat.exitPlanReviewChoose') }}</span>
        <button
          v-for="model in reviewModels"
          :key="`${action.id}-model-${model.provider}-${model.model}`"
          type="button"
          class="xp-model"
          :disabled="submitting"
          data-testid="plan-review-model"
          @click="requestReview(action, model)"
        >
          <span class="m">{{ modelLabel(model) }}</span>
          <span v-if="model.current" class="cur">{{ t('chat.exitPlanReviewCurrent') }}</span>
        </button>
      </div>
      <template v-else>
        <p class="q">{{ t(officeSession ? 'chat.officePlanQuestion' : 'chat.exitPlanQuestion') }}</p>
        <div class="gate-acts">
          <!-- Office creative work answers the plan in its own four words
               (plan 153): running it, revising it, keeping it without running,
               and a second opinion. Clearing the context is a coding habit —
               an office deliverable keeps its conversation. -->
          <template v-if="officeSession">
            <button
              type="button"
              class="gate-btn gate-btn-pri"
              data-testid="office-plan-execute"
              :disabled="submitting"
              @click="approveExitPlan(action, false)"
            >{{ t('chat.officePlanExecute') }}</button>
            <button
              type="button"
              class="gate-btn"
              :class="feedbackOpen ? 'gate-btn-on' : ''"
              :aria-expanded="feedbackOpen"
              data-testid="office-plan-revise"
              :disabled="submitting"
              @click="keepPlanningChosen"
            >{{ t('chat.officePlanRevise') }}</button>
            <button
              type="button"
              class="gate-btn"
              data-testid="office-plan-keep-only"
              :disabled="submitting"
              @click="keepPlanOnly(action)"
            >{{ t('chat.officePlanKeepOnly') }}</button>
            <button
              v-if="reviewModels.length"
              type="button"
              class="gate-btn"
              data-testid="exit-plan-ask-review"
              :disabled="submitting"
              @click="askReviewChosen"
            >{{ t('chat.exitPlanAskReview') }}</button>
          </template>
          <template v-else>
            <button
              type="button"
              class="gate-btn gate-btn-pri"
              data-testid="exit-plan-approve-clear"
              :disabled="submitting"
              @click="approveExitPlan(action, true)"
            >{{ clearContextLabel }}</button>
            <button
              type="button"
              class="gate-btn"
              data-testid="exit-plan-approve"
              :disabled="submitting"
              @click="approveExitPlan(action, false)"
            >{{ t('chat.exitPlanApprove') }}</button>
            <button
              v-if="reviewModels.length"
              type="button"
              class="gate-btn"
              data-testid="exit-plan-ask-review"
              :disabled="submitting"
              @click="askReviewChosen"
            >{{ t('chat.exitPlanAskReview') }}</button>
            <!-- Keep planning is the refusal here: a toggle that opens the words
                 the planner will read, the same pressed state every refusal's
                 toggle takes. -->
            <button
              type="button"
              class="gate-btn"
              :class="feedbackOpen ? 'gate-btn-on' : ''"
              :aria-expanded="feedbackOpen"
              data-testid="exit-plan-keep-planning"
              :disabled="submitting"
              @click="keepPlanningChosen"
            >{{ t('chat.exitPlanKeepPlanning') }}</button>
          </template>
        </div>
        <!-- The words are required: the send stays gray until there are some,
             and Esc is the wordless refusal (it stops the turn, 129). -->
        <div v-if="feedbackOpen" class="gate-fb" data-testid="exit-plan-feedback">
          <input
            :ref="bindFeedback"
            v-model="words"
            class="gate-fb-input"
            type="text"
            autocomplete="off"
            :disabled="submitting"
            :aria-label="t('chat.exitPlanFeedbackPlaceholder')"
            :placeholder="t('chat.exitPlanFeedbackPlaceholder')"
            data-testid="exit-plan-feedback-input"
            @keydown.enter.prevent="sendFeedback"
          />
          <button
            type="button"
            class="gate-btn gate-btn-pri"
            data-testid="exit-plan-send-feedback"
            :disabled="submitting || !words.trim()"
            @click="sendFeedback"
          >{{ t('chat.exitPlanSendFeedback') }}</button>
          <span class="esc">
            <Kbd>Esc</Kbd>{{ t('chat.refuseAndStop') }}
          </span>
        </div>
      </template>
      <div v-if="errorLine" class="gate-error" role="alert">{{ errorLine }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The plan approval where the input was (plan 138): waiting on an exit-plan
 * decision, the plan and its verdicts replace the composer — the terminal
 * paints no card for this gate, and neither does the thread. The verdicts and
 * the review flow are the ones the old thread card carried, moved here whole;
 * keep planning's words are required, and Esc on them is the wordless refusal.
 */
import { computed, nextTick, onMounted, ref, withDefaults } from 'vue'
import { Eye, LoaderCircle, Map } from 'lucide-vue-next'
import { Kbd } from '@repo/shadcn-vue/components/ui/kbd'
import { MessageResponse } from '@repo/elements/message'
import { forebrainApi } from '@/lib/api'
import type { ActionRow } from '@/composables/usePendingActions'
import { injectPendingActions } from '@/composables/providePendingActions'
import type { SessionPlan } from '@/composables/useSessionPlans'
import { t } from '@/locales'

const props = withDefaults(defineProps<{
  /** The pending exit-plan approval the input is parked on. */
  action: ActionRow
  /** The conversation the plan belongs to — the dock sits where its input was. */
  sessionId: string
  /** The kind the conversation was born with (plan 153): an office plan is
   *  answered in the office's own four words, a coding plan keeps the four it
   *  always had, unchanged. */
  sessionType?: string
  /** Opens one of the conversation's plan documents as a drawer tab. */
  onOpenPlan?: (plan: SessionPlan) => void
  /** Whether the eye has somewhere to open the whole plan: the conversation's
   *  drawer takes it; a surface with no plan tab of its own hides the eye. */
  canOpenPlan?: boolean
}>(), { sessionType: '', onOpenPlan: undefined, canOpenPlan: true })

/** Whether this plan is answered with the office creative verdicts. */
const officeSession = computed(() => String(props.sessionType ?? '').trim().toLowerCase() === 'office')

// The dock renders only under the conversation that provides the store; every
// fact it reads and every verdict it sends is the store's own.
const store = injectPendingActions()!

// The store's pieces this dock reads; its refs unwrap here so the template
// binds plain values, keyed by the action the input is parked on.
const {
  exitPlanRequest, approveExitPlan, keepPlanning, keepPlanOnly, openPlanFeedback, closePlanFeedback,
  toggleModelPicker, requestReview, stopReview, modelLabel, activeReviewLabel, clearContextLabel,
  refuseAndStop,
} = store

/** The plan this approval is about, from the typed request the gate served. */
const request = computed(() => exitPlanRequest(props.action))
const planText = computed(() => String(request.value?.planText ?? ''))

const reviewActive = computed(() => Boolean(request.value?.planReviewActive))
const reviewModels = computed(() => request.value?.planReviewModels ?? [])
const feedbackOpen = computed(() => Boolean(store.planFeedbackOpen.value[props.action.id]))
const picking = computed(() => Boolean(store.modelPicking.value[props.action.id]))
const submitting = computed(() => Boolean(store.submitting.value[props.action.id]))
const errorLine = computed(() => store.errors.value[props.action.id] ?? '')
const words = computed({
  get: () => store.denyFeedback.value[props.action.id] ?? '',
  set: (value: string) => {
    store.denyFeedback.value = { ...store.denyFeedback.value, [props.action.id]: value }
  },
})

/** The plan file's path, from the newest plan the session recorded; without
 *  one the head names the plan alone and the eye opens the request's text. */
const latestPlan = ref<SessionPlan | null>(null)

onMounted(async () => {
  const sid = String(props.sessionId ?? '').trim()
  if (!sid) return
  try {
    const plans = await forebrainApi.sessionPlans(sid)
    latestPlan.value = plans.reduce<SessionPlan | null>((newest, plan) => (
      newest == null || Number(plan.updatedAt ?? 0) >= Number(newest.updatedAt ?? 0) ? plan : newest
    ), null) ?? null
  } catch {
    latestPlan.value = null
  }
})

const planPath = computed(() => String(latestPlan.value?.path ?? '').trim())

/** The plan the eye opens: the session's newest, else the request's own text
 *  as a tab of its own — the reader still gets the whole document. */
function openPlan() {
  if (!props.onOpenPlan) return
  if (latestPlan.value) {
    props.onOpenPlan(latestPlan.value)
    return
  }
  const text = planText.value.trim()
  if (!text) return
  props.onOpenPlan({ name: 'plan.md', path: '', updatedAt: Date.now(), markdown: text })
}

/** 「继续规划」first opens the field for the planner's words; the same verdict
 *  again sends them. Any other decision folds it away (in the composable). */
const feedbackField = ref<HTMLInputElement | null>(null)

function bindFeedback(el: unknown) {
  feedbackField.value = (el as HTMLInputElement | null) ?? null
}

function keepPlanningChosen() {
  if (feedbackOpen.value) {
    sendFeedback()
    return
  }
  openPlanFeedback(props.action)
  void nextTick(() => feedbackField.value?.focus())
}

function sendFeedback() {
  if (!words.value.trim()) return
  void keepPlanning(props.action)
}

function askReviewChosen() {
  closePlanFeedback(props.action)
  toggleModelPicker(props.action)
}

/** Esc backs the model picker out; on the words it is the wordless refusal —
 *  the plan approval's stop, not the field's collapse (129). */
async function onEsc() {
  if (picking.value) {
    toggleModelPicker(props.action)
    return
  }
  if (feedbackOpen.value) await refuseAndStop(props.action)
}
</script>

<style scoped>
/* The prototype's .xp-dock (:490-500): the input's own width and frame, the
   brand's border, the page's white, and a shadow that lifts it off the run
   the way the input's own sits. */
.xp-dock {
  flex: none;
  width: min(760px, 100% - 48px);
  margin: 0 auto 16px;
  border: 1px solid var(--forebrain-brand-border);
  border-radius: 12px;
  background: var(--forebrain-page-bg);
  overflow: hidden;
  display: grid;
  box-shadow: 0 -8px 24px rgba(16, 24, 40, 0.06);
}

.xp-dock-h {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  padding: 8px 10px 8px 14px;
  border-bottom: 1px solid var(--forebrain-divider);
  font-size: 13px;
  color: var(--forebrain-text);
}

.xp-dock-h b {
  font-weight: 600;
  flex: none;
}

.xp-dock-h .mono {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
  color: var(--forebrain-muted-text);
}

.xp-dock-h .grow {
  flex: 1;
}

.xp-dock-h > svg.lucide {
  color: var(--forebrain-brand-1);
}

/* The eye (prototype .tt-all): a quiet 24px square, the brand's tint only
   while it is pointed at. */
.xp-eye {
  all: unset;
  flex: none;
  box-sizing: border-box;
  width: 24px;
  height: 24px;
  border-radius: 6px;
  display: grid;
  place-items: center;
  cursor: pointer;
  color: var(--forebrain-muted-text);
  transition: background 0.15s, color 0.15s;
}

.xp-eye:hover {
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}

.xp-eye:focus-visible {
  outline: 2px solid var(--forebrain-brand-1);
  outline-offset: 1px;
}

/* The plan itself, scrolled and fading (prototype .xp-dock .xp-doc, :497). */
.xp-doc {
  padding: 12px 14px;
  max-height: min(240px, 30vh);
  overflow: auto;
  -webkit-mask-image: linear-gradient(#000 calc(100% - 20px), transparent);
  mask-image: linear-gradient(#000 calc(100% - 20px), transparent);
  font-size: 13px;
  line-height: 1.7;
  color: var(--forebrain-text);
}

/* The footer every approval closes on (prototype .gate-ask) with the plan's
   own top edge. */
.gate-ask {
  display: grid;
  gap: 9px;
  padding: 10px 12px 11px;
  background: var(--forebrain-brand-soft);
  border-top: 1px solid var(--forebrain-brand-border);
}

.gate-ask .q {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
  line-height: 1.5;
  color: var(--forebrain-text);
}

.gate-acts {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}

/* The prototype's .btn.sm (:179) and .btn.pri, the pressed refusal's .on. */
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

.gate-btn-pri {
  border-color: var(--forebrain-brand-1);
  background: var(--forebrain-brand-1);
  color: #f5ffff;
}

.gate-btn-pri:hover {
  background: var(--forebrain-brand-hover);
  border-color: var(--forebrain-brand-hover);
}

.gate-btn-on {
  background: var(--forebrain-page-bg);
  border-color: var(--forebrain-brand-1);
  color: var(--forebrain-brand-1);
  box-shadow: inset 0 0 0 1px var(--forebrain-brand-1);
}

/* The words field (prototype .gate-fb, :685-691). */
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

.gate-fb .esc {
  flex: none;
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  white-space: nowrap;
}

.gate-fb .esc :deep(kbd) {
  font-family: var(--forebrain-mono-font);
  font-size: 10.5px;
  line-height: 1.6;
  padding: 0 4px;
  margin-right: 4px;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 4px;
  background: var(--forebrain-page-bg);
  color: var(--forebrain-text-2);
}

/* A review in flight: one line, its spinner, its stop (prototype .xp-run). */
.xp-run {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: var(--forebrain-text);
}

.xp-run .grow {
  flex: 1;
}

.xp-run > svg.lucide {
  color: var(--forebrain-brand-1);
}

/* The reviewer list on the footer (prototype .xp-pick). */
.xp-pick {
  display: grid;
  gap: 4px;
}

.xp-pick .cap {
  font-size: 12.5px;
  color: var(--forebrain-text-2);
}

.xp-model {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  padding: 5px 9px;
  background: var(--forebrain-page-bg);
  font-size: 12.5px;
  color: var(--forebrain-text);
  text-align: left;
  cursor: pointer;
}

.xp-model:hover {
  background: var(--forebrain-bg-alt);
}

.xp-model .m {
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
}

.xp-model .cur {
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
}

.gate-error {
  font-size: 12px;
  color: var(--forebrain-danger);
}
</style>
