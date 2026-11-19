import { computed, ref, watch, type Ref } from 'vue'
import { GatewayHttpError, getErrorMessage, forebrainApi, type PlanReviewModelOption, type SessionApprovalRequest } from '@/lib/api'
import { approvalGateOf, type ApprovalFacts } from '@/lib/approvalGates'
import { EFFORT_LEVELS, effortLevelLabel } from '@/lib/effortLabel'
import { useI18n } from '@/locales'
import {
  approvalDecisions,
  permissionSuggestionFromAction,
  type ApprovalDecisionOption,
  type PermissionSuggestionRecord,
} from '@/lib/approvalSuggestions'

/**
 * The requests one conversation's run is parked on — tool approvals, the
 * agent's questions, and the exit-plan approval with everything needed to
 * decide them — loaded from the same actions endpoint and answered through the
 * same endpoints for every surface that shows them. Both containers the web
 * has draw from here: the panel above the workshop's composer and the card in
 * the conversation's own message flow, so a gate is answered identically
 * wherever it is read.
 *
 * A parked exit-plan approval reads its typed request — the plan, the models a
 * review may be handed to, the reviews already collected, and the one running
 * — so its card is drawn from the same facts the terminal's overlay prompts
 * with.
 */
export type ActionRow = {
  id: string
  kind: string
  status: string
  payloadJson: string
  answerJson?: string
  createdAt: number
  updatedAt: number
  permissionSuggestion?: PermissionSuggestionRecord | null
  agentId?: string
  subagentType?: string
}

/** One row of the picker's second screen: the level's id, whether it is the
 *  one the model row is configured with (the gear), and the localized label. */
export type PlanEffortOption = { id: string; fromSetting: boolean; label: string }

/**
 * The thinking levels the approval card offers for one model, in the one order
 * every effort picker draws (EFFORT_LEVELS, the seven known ones first, then
 * any level this build does not rank), mirroring the web effort chip's own
 * list: a row that configures a level opens on it and marks it with the
 * setting gear, and the bare 「默认」 entry leads only when the row configures
 * none — the approval card has no session-pinned level to duplicate, so that is
 * the only case where the empty level is a real choice.
 */
export function planEffortOptions(model: PlanReviewModelOption | null): PlanEffortOption[] {
  const efforts = model?.efforts ?? []
  const known = EFFORT_LEVELS.filter((level) => efforts.includes(level))
  const extra = efforts.filter((level) => !(EFFORT_LEVELS as readonly string[]).includes(level))
  const configured = String(model?.defaultEffort ?? '')
  const ids = configured === '' ? ['', ...known, ...extra] : [...known, ...extra]
  return ids.map((id) => ({
    id,
    fromSetting: id !== '' && id === configured,
    label: effortLevelLabel(id),
  }))
}

export type AskAction = {
  id: string
  kind: string
  status: string
  agentId?: string
  subagentType?: string
  sessionId?: string
  form: {
    title?: string
    questions: {
      id: string
      /** The short tab label the engine keys answers by; a payload whose id is
       *  an opaque key carries none and the card falls back to the prompt. */
      header?: string
      prompt: string
      options: { id: string; label: string; description?: string; preview?: string; recommended?: boolean }[]
      allowMultiple?: boolean
      allowOther?: boolean
    }[]
  }
}

export function usePendingActions(options: {
  sessionId: Ref<string | null>
  /** Bumped by the stream whenever the set of pending requests changed. */
  version: Ref<number>
  /** How full the context window is; the clear-context choice says what it
   *  would free. Null when unknown. */
  contextUsedPercent: Ref<number | null>
}) {
  const { t } = useI18n()

  const pendingApprovals = ref<ActionRow[]>([])
  const pendingAsk = ref<AskAction[]>([])
  const answers = ref<Record<string, Record<string, string[] | string>>>({})
  const others = ref<Record<string, Record<string, string>>>({})
  const submitting = ref<Record<string, boolean>>({})
  const errors = ref<Record<string, string>>({})
  const denyFeedback = ref<Record<string, string>>({})
  /** The typed approval requests behind parked exit-plan approvals. */
  const exitPlans = ref<Record<string, SessionApprovalRequest | null>>({})
  /** Whether a card's review-model picker is open, by action id. */
  const modelPicking = ref<Record<string, boolean>>({})
  /** Why a model picker is open: picking a reviewer, or picking the model a
   *  plan is handed to (plan 283). The list is the same one either way. */
  const modelPickPurpose = ref<Record<string, 'review' | 'handoff'>>({})
  /** Which of the picker's two screens is open (plan 310): the model list, or
   *  the thinking levels of the row chosen on it. */
  const modelPickStep = ref<Record<string, 'model' | 'effort'>>({})
  /** The row carried from the first screen to the second, and the level the
   *  second opens on. Nothing is sent until the level screen answers. */
  const pickedModel = ref<Record<string, PlanReviewModelOption | null>>({})
  const pickedEffort = ref<Record<string, string>>({})
  /** Whether an exit-plan card's feedback field is open, by action id. The
   *  field appears only after「继续规划」is chosen, the way the terminal's
   *  overlay asks for feedback after the verdict, and folds away again on any
   *  other decision. Both surfaces share this state, so a field opened in the
   *  thread is open above the composer too. */
  const planFeedbackOpen = ref<Record<string, boolean>>({})
  const loading = ref(false)
  const loadError = ref<string | null>(null)
  let generation = 0
  let loadedSession = ''

  /** The typed request behind one parked exit-plan approval, when it arrived. */
  function exitPlanRequest(a: ActionRow): SessionApprovalRequest | null {
    return exitPlans.value[a.id] ?? null
  }

  function isExitPlan(a: ActionRow): boolean {
    return String(a.kind ?? '').trim().toLowerCase() === 'exit_plan_mode'
  }

  function modelLabel(model: PlanReviewModelOption): string {
    return [model.provider, model.modelName || model.model].filter(Boolean).join('/')
  }

  function activeReviewLabel(a: ActionRow): string {
    const active = exitPlanRequest(a)?.planReviewActive
    return [active?.provider, active?.modelName || (active?.model ?? '')].filter(Boolean).join('/')
  }

  /** The clear-context choice names what it frees when the number is known,
   * exactly the sentence the terminal's overlay offers. */
  const clearContextLabel = computed(() => {
    const percent = options.contextUsedPercent.value
    if (percent == null || !Number.isFinite(percent) || percent <= 0) {
      return t('chat.exitPlanApproveClearPlain')
    }
    return t('chat.exitPlanApproveClear', { percent: Math.round(percent) })
  })

  function currentSession(): string {
    return String(options.sessionId.value ?? '').trim()
  }

  async function submitAction(actionId: string, request: () => Promise<unknown>) {
    if (submitting.value[actionId]) return
    const submittingSession = currentSession()
    submitting.value = { ...submitting.value, [actionId]: true }
    const nextErrors = { ...errors.value }
    delete nextErrors[actionId]
    errors.value = nextErrors
    try {
      await request()
      if (currentSession() === submittingSession) await load()
    } catch (err) {
      if (currentSession() !== submittingSession) return
      errors.value = { ...errors.value, [actionId]: getErrorMessage(err) }
      // A competing tab may have resolved the action. Refresh without erasing
      // the visible error if the action is still pending.
      await load()
    } finally {
      if (currentSession() === submittingSession) {
        const next = { ...submitting.value }
        delete next[actionId]
        submitting.value = next
      }
    }
  }

  function ensureModels(actionId: string, form: AskAction['form']) {
    if (!answers.value[actionId]) answers.value[actionId] = {}
    if (!others.value[actionId]) others.value[actionId] = {}
    for (const q of form.questions) {
      if (answers.value[actionId][q.id] == null) {
        answers.value[actionId][q.id] = q.allowMultiple ? [] : ''
      }
      if (others.value[actionId][q.id] == null) {
        others.value[actionId][q.id] = ''
      }
    }
  }

  function reset() {
    pendingAsk.value = []
    pendingApprovals.value = []
    submitting.value = {}
    errors.value = {}
    denyFeedback.value = {}
    exitPlans.value = {}
    modelPicking.value = {}
    modelPickPurpose.value = {}
    planFeedbackOpen.value = {}
    loadError.value = null
  }

  /**
   * An ask's form as the questions the card draws, or null when the payload is
   * not that shape. The card reads each question through these fields alone,
   * the way the lsp recommendation parser reads its own whitelist. The stored
   * payload is snake_case ("allow_multiple"); the card's fields are the
   * camelCase ones here, and the engine's question header travels as the
   * question id, so the parser is where both become what the card reads.
   */
  function parseAskForm(raw: unknown): AskAction['form'] | null {
    if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null
    const form = raw as Record<string, unknown>
    if (!Array.isArray(form.questions)) return null
    const questions: AskAction['form']['questions'] = []
    for (const q of form.questions) {
      if (!q || typeof q !== 'object') return null
      const question = q as Record<string, unknown>
      if (typeof question.id !== 'string' || typeof question.prompt !== 'string' || !Array.isArray(question.options)) return null
      questions.push({
        id: question.id,
        header: String(question.header ?? question.id ?? '').trim(),
        prompt: question.prompt,
        options: question.options as AskAction['form']['questions'][number]['options'],
        allowMultiple: question.allow_multiple === true || question.allowMultiple === true,
        allowOther: question.allow_other === true || question.allowOther === true,
      })
    }
    return { questions }
  }

  async function load() {
    const sid = currentSession()
    const current = ++generation
    if (!sid) {
      loadedSession = ''
      reset()
      loading.value = false
      return
    }
    if (loadedSession !== sid) {
      loadedSession = sid
      reset()
    }
    loading.value = true
    try {
      const list = await forebrainApi.actionsList('pending', sid)
      const asks: AskAction[] = []
      const approvals: ActionRow[] = []
      for (const a of list) {
        if (a.kind === 'user_interaction') {
          try {
            const form = parseAskForm(JSON.parse(a.payloadJson))
            if (form) {
              asks.push({
                id: a.id,
                kind: a.kind,
                status: a.status,
                form,
                agentId: a.agentId,
                subagentType: a.subagentType,
                sessionId: a.sessionId,
              })
              ensureModels(a.id, form)
            }
          } catch {
            //
          }
        } else {
          approvals.push(a as ActionRow)
        }
      }
      if (current !== generation || currentSession() !== sid) return
      pendingAsk.value = asks
      pendingApprovals.value = approvals
      loadError.value = null
      // A parked exit-plan approval reads its typed request — the plan, the
      // models a review may be handed to, the reviews already collected, and
      // the one running — so its card is drawn from the same facts the
      // terminal's overlay prompts with. A request that fails leaves the card
      // on its three verdict choices and says so; the action itself is fine.
      const nextExitPlans: Record<string, SessionApprovalRequest | null> = {}
      const nextErrors = { ...errors.value }
      for (const a of approvals) {
        if (!isExitPlan(a)) continue
        try {
          nextExitPlans[a.id] = await forebrainApi.sessionApprovalRequest(sid)
        } catch (cause) {
          nextExitPlans[a.id] = null
          nextErrors[a.id] = getErrorMessage(cause)
        }
        if (current !== generation || currentSession() !== sid) return
      }
      exitPlans.value = nextExitPlans
      errors.value = nextErrors
    } catch (cause) {
      if (current !== generation || currentSession() !== sid) return
      // Keep this session's already-rendered requests. A transient refresh
      // failure must not make a still-pending approval disappear or leave the
      // user without a way to retry. What failed is a request, not an action:
      // the user is told that, and that retrying is how it recovers, never
      // handed the transport's text.
      loadError.value = cause instanceof GatewayHttpError
        ? t('chat.approvalsLoadFailedStatus', { status: cause.status })
        : t('chat.approvalsLoadFailedOffline')
    } finally {
      if (current === generation && currentSession() === sid) {
        loading.value = false
      }
    }
  }

  async function submitAsk(actionId: string) {
    const a = pendingAsk.value.find((x) => x.id === actionId)
    if (!a) return
    const payload = a.form.questions.map((q) => {
      const raw = answers.value?.[actionId]?.[q.id]
      const optionIds = Array.isArray(raw) ? raw : (raw ? [String(raw)] : [])
      const otherText = others.value?.[actionId]?.[q.id] ?? ''
      return {
        questionId: q.id,
        optionIds,
        ...(String(otherText).trim() ? { otherText: String(otherText).trim() } : {}),
      }
    })
    await submitAction(actionId, () => forebrainApi.actionsAnswer(actionId, { answers: payload }))
  }

  /** The web's Esc: the same verdict the terminal's overlay returns for an asked
   *  form (see decideAsk in pkg/turn/approval.go), so the run stops waiting
   *  instead of hanging on a question nobody will answer. It drops this one
   *  question — not the run. */
  async function cancelAsk(actionId: string) {
    await submitAction(actionId, () => forebrainApi.actionsApprove(actionId, { decision: 'cancel' }))
  }

  /** 「和它聊聊」: this question is not cancelled — the engine records a refusal
   *  carrying its own clarify instruction (composed in Go, tool.AskClarifyReason),
   *  so the turn carries on and the model asks what to clarify. */
  async function clarifyAsk(actionId: string) {
    await submitAction(actionId, () => forebrainApi.actionsApprove(actionId, { decision: 'clarify' }))
  }

  /** One question's answer, written by whichever surface is asking it — the
   *  tabbed card and any other presentation of the same action share this
   *  store, so an answer made in one place is answered in the other too. */
  function setAskAnswer(actionId: string, questionId: string, value: string | string[]) {
    if (!answers.value[actionId]) answers.value[actionId] = {}
    answers.value[actionId][questionId] = value
  }

  /** One question's「其他」text, the twin of setAskAnswer. */
  function setAskOther(actionId: string, questionId: string, value: string) {
    if (!others.value[actionId]) others.value[actionId] = {}
    others.value[actionId][questionId] = value
  }

  async function submitApprovalDecision(action: ActionRow, option: ApprovalDecisionOption) {
    await submitAction(action.id, () => forebrainApi.actionsApprove(action.id, {
      reason: `web:${option.decision}`,
      decision: option.decision,
      ...(option.execpolicyAmendment ? { execpolicyAmendment: option.execpolicyAmendment } : {}),
    }))
  }

  /** A refusal with the user's words: this one call is refused, the turn
   *  carries on, and the words reach the model (plan 129). The engine stores
   *  the reason on the denial and the resume composes it into the refusal the
   *  model reads (`To tell you how to proceed, the user said:`), so the words
   *  typed here are the words that steer the turn. */
  async function refuseWithWords(action: ActionRow, words: string) {
    const reason = String(words ?? '').trim()
    if (!reason) return
    await submitAction(action.id, () => forebrainApi.actionsApprove(action.id, { decision: 'deny', reason }))
  }

  /** A refusal without words (empty send, or Esc): the call is refused and the
   *  turn stops, waiting for the user (plan 129's cancel). The refused call
   *  reads the engine's stop sentence; calls behind it never start. */
  async function refuseAndStop(action: ActionRow) {
    await submitAction(action.id, () => forebrainApi.actionsApprove(action.id, { decision: 'cancel', reason: 'web:cancel' }))
  }

  /**
   * The pending request one tool card is waiting on, or null. The timeline's
   * `approval` block names the call the gate holds (`toolStepId`); the action
   * it names must still be pending — a decided gate leaves its block behind
   * while its card settles, and re-offering a settled question would answer
   * nothing. The blocks are the caller's own: the conversation collects them
   * from its messages, a subagent from its own transcript, so a subagent's
   * gate never matches a card of the conversation's.
   */
  function pendingForStep(stepId: string, approvalBlocks: { actionId: string; toolStepId?: string }[]): ActionRow | null {
    const id = String(stepId ?? '').trim()
    if (!id) return null
    for (const block of approvalBlocks) {
      if (String(block.toolStepId ?? '').trim() !== id) continue
      const found = pendingApprovals.value.find((a) => a.id === block.actionId)
      if (found) return found
    }
    return null
  }

  /** The exit-plan card's two approvals: with and without clearing context.
   *  Either one is a decision, so the feedback field folds away with its
   *  draft. */
  async function approveExitPlan(a: ActionRow, clearContext: boolean) {
    closePlanFeedback(a)
    await submitAction(a.id, () => forebrainApi.actionsApprove(a.id, clearContext ? { clearContext: true } : {}))
  }

  /** Keeping planning denies the approval; the typed feedback is the user's own
   * words for the planner, exactly the sentence the terminal's overlay sends. */
  async function keepPlanning(a: ActionRow) {
    const reason = String(denyFeedback.value[a.id] ?? '').trim()
    await submitAction(a.id, () => forebrainApi.actionsDeny(a.id, reason ? { reason } : {}))
  }

  /** 「只要计划，先不执行」(plan 153): the approval is granted with `plan_only`,
   *  so the turn ends with the plan kept as this turn's deliverable instead of
   *  starting the work. It is a decision, so the feedback draft goes with it. */
  async function keepPlanOnly(a: ActionRow) {
    closePlanFeedback(a)
    await submitAction(a.id, () => forebrainApi.actionsApprove(a.id, { planOnly: true }))
  }

  /** The feedback field opens on the「继续规划」verdict and closes on any other
   *  decision or its own cancel, taking the unfinished draft with it. */
  function openPlanFeedback(a: ActionRow) {
    planFeedbackOpen.value = { ...planFeedbackOpen.value, [a.id]: true }
  }

  function closePlanFeedback(a: ActionRow) {
    planFeedbackOpen.value = { ...planFeedbackOpen.value, [a.id]: false }
    if (String(denyFeedback.value[a.id] ?? '') !== '') {
      denyFeedback.value = { ...denyFeedback.value, [a.id]: '' }
    }
  }

  function toggleModelPicker(a: ActionRow, purpose: 'review' | 'handoff' = 'review') {
    const open = !modelPicking.value[a.id] || modelPickPurpose.value[a.id] !== purpose
    modelPicking.value = { ...modelPicking.value, [a.id]: open }
    modelPickPurpose.value = { ...modelPickPurpose.value, [a.id]: purpose }
    // Opening always starts on the model list, with nothing carried over from
    // a previous pass.
    modelPickStep.value = { ...modelPickStep.value, [a.id]: 'model' }
    clearPickedEffort(a)
  }

  /** Esc closes whichever picker is open without changing what it was for:
   *  re-entering toggleModelPicker with the default 'review' would reopen the
   *  list and silently turn a hand-off into a review (plan 289). */
  function closeModelPicker(a: ActionRow) {
    modelPicking.value = { ...modelPicking.value, [a.id]: false }
    clearPickedEffort(a)
  }

  /** Drops what the second screen was carrying: the row it was about and the
   *  level it opened on. The key stays on 'model' — the screen the picker
   *  returns to. */
  function clearPickedEffort(a: ActionRow) {
    pickedModel.value = { ...pickedModel.value, [a.id]: null }
    pickedEffort.value = { ...pickedEffort.value, [a.id]: '' }
    modelPickStep.value = { ...modelPickStep.value, [a.id]: 'model' }
  }

  /** Esc on the level screen goes back one step, not out of the picker: the
   *  model list is exactly as it was, and what it was open for is unchanged
   *  (289's rule — Esc never rewrites the purpose). */
  function backToModels(a: ActionRow) {
    const purpose = modelPickPurpose.value[a.id] ?? 'review'
    pickedModel.value = { ...pickedModel.value, [a.id]: null }
    pickedEffort.value = { ...pickedEffort.value, [a.id]: '' }
    modelPickStep.value = { ...modelPickStep.value, [a.id]: 'model' }
    modelPickPurpose.value = { ...modelPickPurpose.value, [a.id]: purpose }
  }

  /** Picking a model opens the level screen when the row offers levels, and
   *  decides straight away when it offers none — the one-step pick a row
   *  without levels has always had (plan 283). */
  function choosePlanModel(a: ActionRow, model: PlanReviewModelOption) {
    // Whether the second screen opens is decided by the levels the row offers,
    // not by the length of the drawn list: a row that configures no default
    // still gets a bare 「默认」 entry, so the two counts differ.
    if ((model.efforts ?? []).length === 0) {
      if (pickPurposeFor(a) === 'handoff') {
        void handOffTo(a, model)
        return
      }
      void requestReview(a, model)
      return
    }
    pickedModel.value = { ...pickedModel.value, [a.id]: model }
    pickedEffort.value = { ...pickedEffort.value, [a.id]: String(model.defaultEffort ?? '') }
    modelPickStep.value = { ...modelPickStep.value, [a.id]: 'effort' }
  }

  function pickPurposeFor(a: ActionRow): 'review' | 'handoff' {
    return modelPickPurpose.value[a.id] ?? 'review'
  }

  /** Answering the level screen is the decision: the model carried from the
   *  first screen travels with the level, and the empty level (the bare
   *  「默认」 row) leaves the model row's own configuration in charge. */
  function choosePlanEffort(a: ActionRow, effort: string) {
    const model = pickedModel.value[a.id] ?? null
    const purpose = pickPurposeFor(a)
    clearPickedEffort(a)
    if (!model) return
    if (purpose === 'handoff') {
      void handOffTo(a, model, effort)
      return
    }
    void requestReview(a, model, effort)
  }

  /** Asking for a review does not decide anything: the approval stays pending,
   * the review runs in the background, and the card learns its progress from
   * the conversation's events. `effort` is the level picked on the second
   * screen; omitted leaves the row's own configuration in charge. */
  async function requestReview(a: ActionRow, model: PlanReviewModelOption, effort?: string) {
    modelPicking.value = { ...modelPicking.value, [a.id]: false }
    // The row the user picked goes with the pair: two rows of one
    // provider/model are two reviewers, and the row decides endpoint and key.
    await submitAction(a.id, () =>
      forebrainApi.actionPlanReview(a.id, {
        provider: model.provider,
        model: model.model,
        modelId: model.id,
        ...(effort ? { effort } : {}),
      }),
    )
  }

  /** Handing the plan to another model is a decision: the engine approves the
   *  exit-plan request with `hand_off_model`, so the primary agent dispatches
   *  one executor subagent on that row (plan 283). The level picked on the
   *  second screen rides along; omitted leaves the row's own. */
  async function handOffTo(a: ActionRow, model: PlanReviewModelOption, effort?: string) {
    closePlanFeedback(a)
    modelPicking.value = { ...modelPicking.value, [a.id]: false }
    await submitAction(a.id, () =>
      forebrainApi.actionsApprove(a.id, {
        // The engine hands off by configured row id — two rows of one
        // provider/model are different executors — and refuses an empty one.
        handOffModel: {
          id: String(model.id ?? ''),
          provider: model.provider,
          model: model.model,
          ...(effort ? { effort } : {}),
        },
      }),
    )
  }

  /** 「请 <模型> 再评审」continues the previous reviewer in its own conversation
   *  instead of dispatching a new one: the engine reads which review that is
   *  from the gate, so nothing goes with the request but the flag (plan 283). */
  async function reviewAgain(a: ActionRow) {
    modelPicking.value = { ...modelPicking.value, [a.id]: false }
    await submitAction(a.id, () => forebrainApi.actionPlanReview(a.id, { again: true }))
  }

  /** Stopping a review is stopping the reviewer subagent, the same way any
   * other subagent is stopped. */
  async function stopReview(a: ActionRow) {
    const agentId = String(exitPlanRequest(a)?.planReviewActive?.agentId ?? '').trim()
    if (!agentId) return
    await submitAction(a.id, () => forebrainApi.subagentCancel(agentId))
  }

  function approvalDecisionOptions(action: ActionRow): ApprovalDecisionOption[] {
    return approvalDecisions(permissionSuggestionFromAction(action))
  }

  // One gate's family, facts, and question, read from the action the card
  // holds: the payload is the engine's own record of what it wants, and the
  // suggestion carries the rule an approval writes. Both surfaces ask through
  // here, so a gate reads identically in the thread
  // and above the workshop's composer.
  function approvalGate(a: ActionRow): { facts: ApprovalFacts; question: string } {
    return approvalGateOf(a)
  }

  // A fetch gate is about one page on the public web, and the engine already
  // says which: the facts below are the same ones the terminal's block reads,
  // so the two surfaces cannot disagree about what is being asked.
  function isWebFetchAction(a: ActionRow): boolean {
    const name = permissionSuggestionFromAction(a)?.permissionToolName ?? ''
    return name.trim().toLowerCase() === 'webfetch'
  }

  // The page a fetch is about - permission_input is the URL itself.
  function webFetchPage(a: ActionRow): string {
    return String(permissionSuggestionFromAction(a)?.permissionInput ?? '').trim()
  }

  // What remembering this approval covers: prefix_rule_content is "domain:<host>"
  // for a fetch, which is why the button that remembers it can state its scope.
  function webFetchHost(a: ActionRow): string {
    const rule = String(permissionSuggestionFromAction(a)?.prefixRuleContent ?? '').trim()
    return rule.toLowerCase().startsWith('domain:') ? rule.slice('domain:'.length) : ''
  }

  // A decision button says what it does in its own family's verb — write,
  // run, call — and every scope button says how far the yes reaches. The
  // fetch gate keeps its shipped copy; every other family reads the table the
  // approval cards were designed against, so the verb cannot drift between
  // the thread's card and the workbench's panel.
  function approvalDecisionLabel(option: ApprovalDecisionOption, action?: ActionRow, facts?: ApprovalFacts | null): string {
    const gate = facts ?? (action ? approvalGate(action).facts : null)
    const family = gate?.family ?? 'fallback'
    if (family === 'web_fetch') {
      if (option.decision === 'accept') return t('chat.webFetchAccept')
      if (option.decision === 'cancel') return t('chat.webFetchDecline')
      if (option.decision === 'accept_and_remember') {
        const host = action ? webFetchHost(action) : ''
        if (host) return t('chat.webFetchRemember', { host })
      }
    }
    switch (option.decision) {
      case 'accept':
        switch (family) {
          case 'file_write': return (gate?.removed ?? 0) > 0 ? t('chat.approvalYesOverwrite') : t('chat.approvalYesWrite')
          case 'file_edit': return t('chat.approvalYesEdit')
          case 'apply_patch': return t('chat.approvalYesApply')
          case 'shell': return t('chat.approvalYesRun')
          case 'shell_outside_sandbox': return t('chat.approvalYesRunOutside')
          case 'enter_plan_mode': return t('chat.approvalYesEnterPlan')
          case 'mcp_tool': return t('chat.approvalYesCall')
          case 'protected_read': return t('chat.approvalYesRead')
          case 'web_search': return t('chat.approvalSearchYes')
          case 'memory_note': return t('chat.approvalMemoryYes')
          default: return t('chat.approvalYesAllow')
        }
      case 'accept_for_session':
        return family === 'shell' || family === 'shell_outside_sandbox' ? t('chat.approvalScopeThisCommand')
          : t('chat.approvalScopeThisSession')
      case 'accept_and_remember': return t('chat.approvalScopeAlways')
      case 'accept_with_execpolicy_amendment': return t('chat.approvalScopeThisCommand')
      case 'grant_for_turn': return t('chat.approvalYesGrantTurn')
      case 'grant_for_session': return t('chat.approvalYesGrantSession')
      case 'decline': return family === 'memory_note' ? t('chat.approvalMemoryNo') : t('chat.approvalNoGrant')
      case 'cancel':
        switch (family) {
          case 'file_write': return t('chat.approvalNoWrite')
          case 'file_edit': return t('chat.approvalNoEdit')
          case 'apply_patch': return t('chat.approvalNoApply')
          case 'shell':
          case 'shell_outside_sandbox': return t('chat.approvalNoRun')
          case 'mcp_tool': return t('chat.approvalNoCall')
          case 'protected_read': return t('chat.approvalNoRead')
          case 'enter_plan_mode': return t('chat.approvalNoEnter')
          case 'web_search': return t('chat.approvalSearchNo')
          default: return t('chat.approvalNo')
        }
    }
  }

  // A subagent's request names the worker that is blocked. The primary agent's
  // own requests carry no such line: it is the agent the conversation is with.
  function requesterLabel(a: Pick<ActionRow, 'agentId' | 'subagentType'>): string {
    const type = String(a.subagentType ?? '').trim()
    if (type) return t('chat.approvalRequestedBy', { type })
    if (String(a.agentId ?? '').trim()) return t('chat.approvalRequestedBySubagent')
    return ''
  }

  watch(() => [options.sessionId.value, options.version.value] as const, () => {
    void load()
  }, { immediate: true })

  return {
    pendingApprovals,
    pendingAsk,
    answers,
    others,
    submitting,
    errors,
    denyFeedback,
    modelPicking,
    modelPickPurpose,
    planFeedbackOpen,
    loading,
    loadError,
    load,
    exitPlanRequest,
    isExitPlan,
    modelLabel,
    activeReviewLabel,
    clearContextLabel,
    submitAsk,
    cancelAsk,
    clarifyAsk,
    setAskAnswer,
    setAskOther,
    submitApprovalDecision,
    refuseWithWords,
    refuseAndStop,
    pendingForStep,
    approveExitPlan,
    keepPlanning,
    keepPlanOnly,
    openPlanFeedback,
    closePlanFeedback,
    toggleModelPicker,
    closeModelPicker,
    modelPickStep,
    pickedModel,
    pickedEffort,
    planEffortOptions,
    choosePlanModel,
    choosePlanEffort,
    backToModels,
    requestReview,
    handOffTo,
    reviewAgain,
    stopReview,
    approvalDecisionOptions,
    approvalDecisionLabel,
    approvalGate,
    isWebFetchAction,
    webFetchPage,
    requesterLabel,
  }
}
