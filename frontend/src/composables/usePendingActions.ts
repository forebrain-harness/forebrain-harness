import { computed, ref, watch, type Ref } from 'vue'
import { parseJsonCamelCase } from '@/lib/case'
import { GatewayHttpError, getErrorMessage, forebrainApi, type PlanReviewModelOption, type PlanReviewNote, type SessionApprovalRequest } from '@/lib/api'
import { useI18n, type I18nKey } from '@/locales'
import {
  approvalDecisions,
  approvalJustification,
  permissionSuggestionFromAction,
  type ApprovalDecisionOption,
  type CommandApprovalScope,
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

  function reviewModelLabel(review: PlanReviewNote): string {
    return [review.provider, review.model].filter(Boolean).join('/')
  }

  function modelLabel(model: PlanReviewModelOption): string {
    return [model.provider, model.model].filter(Boolean).join('/')
  }

  function activeReviewLabel(a: ActionRow): string {
    const active = exitPlanRequest(a)?.planReviewActive
    return [active?.provider, active?.model ?? ''].filter(Boolean).join('/') || active?.label || ''
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
    loadError.value = null
  }

  /**
   * An ask's form as the questions the card draws, or null when the payload is
   * not that shape. The card reads each question through these fields alone,
   * the way the lsp recommendation parser reads its own whitelist.
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
      questions.push(question as AskAction['form']['questions'][number])
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

  async function approveAction(id: string) {
    await submitAction(id, () => forebrainApi.actionsApprove(id, {}))
  }

  async function submitApprovalDecision(action: ActionRow, option: ApprovalDecisionOption) {
    await submitAction(action.id, () => forebrainApi.actionsApprove(action.id, {
      reason: `web:${option.decision}`,
      decision: option.decision,
      ...(option.execpolicyAmendment ? { execpolicyAmendment: option.execpolicyAmendment } : {}),
      ...(option.networkPolicyAmendment ? { networkPolicyAmendment: option.networkPolicyAmendment } : {}),
    }))
  }

  async function denyAction(id: string) {
    const reason = String(denyFeedback.value[id] ?? '').trim()
    await submitAction(id, () => forebrainApi.actionsDeny(id, reason ? { reason } : {}))
  }

  /** The exit-plan card's two approvals: with and without clearing context. */
  async function approveExitPlan(a: ActionRow, clearContext: boolean) {
    await submitAction(a.id, () => forebrainApi.actionsApprove(a.id, clearContext ? { clearContext: true } : {}))
  }

  /** Keeping planning denies the approval; the typed feedback is the user's own
   * words for the planner, exactly the sentence the terminal's overlay sends. */
  async function keepPlanning(a: ActionRow) {
    const reason = String(denyFeedback.value[a.id] ?? '').trim()
    await submitAction(a.id, () => forebrainApi.actionsDeny(a.id, reason ? { reason } : {}))
  }

  function toggleModelPicker(a: ActionRow) {
    modelPicking.value = { ...modelPicking.value, [a.id]: !modelPicking.value[a.id] }
  }

  /** Asking for a review does not decide anything: the approval stays pending,
   * the review runs in the background, and the card learns its progress from
   * the conversation's events. */
  async function requestReview(a: ActionRow, model: PlanReviewModelOption) {
    modelPicking.value = { ...modelPicking.value, [a.id]: false }
    await submitAction(a.id, () => forebrainApi.actionPlanReview(a.id, { provider: model.provider, model: model.model }))
  }

  /** Stopping a review is stopping the reviewer subagent, the same way any
   * other subagent is stopped. */
  async function stopReview(a: ActionRow) {
    const agentId = String(exitPlanRequest(a)?.planReviewActive?.agentId ?? '').trim()
    if (!agentId) return
    await submitAction(a.id, () => forebrainApi.subagentCancel(agentId))
  }

  function approvalSuggestion(action: ActionRow): PermissionSuggestionRecord | null {
    return permissionSuggestionFromAction(action)
  }

  function approvalDecisionOptions(action: ActionRow): ApprovalDecisionOption[] {
    return approvalDecisions(permissionSuggestionFromAction(action))
  }

  // commandScopeLabel says what remembering a shell command covers. The server
  // decides the scope; this only puts it into the reader's language, so the web
  // and the terminal never disagree about what a row grants.
  function commandScopeLabel(scope: CommandApprovalScope | undefined, fallback: I18nKey): string {
    if (!scope) return t(fallback)
    const prefixes = scope.prefixes ?? []
    switch (scope.kind) {
      case 'prefix':
        return prefixes.length ? t('chat.approveCommandPrefix', { prefix: prefixes[0] }) : t(fallback)
      case 'command_with_variants':
        // Past a few prefixes the list outgrows the row, and naming some of them
        // would describe less than what is granted.
        if (!prefixes.length || prefixes.length > 3) return t('chat.approveCommandAnyVariant')
        return t('chat.approveCommandVariants', { prefixes: prefixes.join('、') })
      default:
        // The command is shown in full above the choices and the rules
        // authorize it alone, so the button says that once.
        return t('chat.approveCommandOnly')
    }
  }

  function approvalDecisionLabel(option: ApprovalDecisionOption): string {
    switch (option.decision) {
      case 'accept': return t('chat.approveOnce')
      case 'accept_for_session': return t('chat.approveSession')
      case 'accept_and_remember': return commandScopeLabel(option.commandScope, 'chat.approveRemember')
      case 'accept_with_execpolicy_amendment': return commandScopeLabel(option.commandScope, 'chat.approveCommandRule')
      case 'apply_network_policy_amendment': return option.networkPolicyAmendment?.action === 'deny'
        ? t('chat.denyHostRemember')
        : t('chat.allowHostRemember')
      case 'grant_for_turn': return t('chat.grantTurn')
      case 'grant_for_turn_with_strict_auto_review': return t('chat.grantTurnStrict')
      case 'grant_for_session': return t('chat.grantSession')
      case 'decline': return t('chat.deny')
      case 'cancel': return t('chat.cancelRun')
    }
  }

  function approvalSuggestionLabel(action: ActionRow): string {
    const suggestion = permissionSuggestionFromAction(action)
    if (!suggestion) return ''
    const input = suggestion.permissionInput ? ` · ${suggestion.permissionInput}` : ''
    const reason = suggestion.permissionReason ? ` · ${suggestion.permissionReason}` : ''
    return `${suggestion.permissionToolName ?? ''}${input}${reason}`
  }

  function approvalKindLabel(kind: string): string {
    const k = String(kind ?? '').trim().toLowerCase()
    if (k === 'enter_plan_mode') return t('chat.enterPlanMode')
    if (k === 'exit_plan_mode') return t('chat.exitPlanMode')
    return kind
  }

  // A subagent's request names the worker that is blocked. The primary agent's
  // own requests carry no such line: it is the agent the conversation is with.
  function requesterLabel(a: Pick<ActionRow, 'agentId' | 'subagentType'>): string {
    const type = String(a.subagentType ?? '').trim()
    if (type) return t('chat.approvalRequestedBy', { type })
    if (String(a.agentId ?? '').trim()) return t('chat.approvalRequestedBySubagent')
    return ''
  }

  function approvalHint(a: ActionRow): string {
    const k = String(a.kind).trim().toLowerCase()
    let payload: Record<string, unknown> | null = null
    try {
      payload = parseJsonCamelCase<Record<string, unknown>>(a.payloadJson)
    } catch {
      payload = null
    }
    if (k === 'enter_plan_mode') {
      const action = String(payload?.action ?? 'enter').trim().toLowerCase()
      if (action === 'exit') return t('chat.exitPlanAfterApproval')
      return t('chat.enterPlanAfterApproval')
    }
    // The reason the runtime raised this approval is what the row has to say -
    // an action id tells the user nothing they can act on.
    const reason = approvalJustification(payload)
    if (reason) return reason
    return a.id
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
    approvalSuggestionLabel,
    approvalKindLabel,
    requesterLabel,
    approvalHint,
  }
}
