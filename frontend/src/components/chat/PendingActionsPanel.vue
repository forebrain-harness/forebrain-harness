<template>
  <div v-if="loadError || pendingAsk.length || pendingApprovals.length" class="mb-3 space-y-2" data-testid="pending-actions">
    <Alert v-if="loadError" variant="destructive">
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
    <div v-if="pendingApprovals.length" class="space-y-2">
      <div class="text-sm font-medium text-[var(--forebrain-text)]">{{ t('chat.pendingApprovals') }}</div>
      <div v-for="a in pendingApprovals" :key="a.id" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-2" data-testid="pending-approval" :data-action-id="a.id">
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
        <div v-if="approvalSuggestion(a)" class="mt-2 space-y-2 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] p-2 text-xs text-[var(--forebrain-text-2)]">
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
    </div>
    <div v-if="pendingAsk.length" class="space-y-2">
      <div class="text-sm font-medium text-[var(--forebrain-text)]">{{ t('chat.pendingQuestions') }}</div>
      <div v-for="qa in pendingAsk" :key="qa.id" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-2 space-y-2" data-testid="pending-question" :data-action-id="qa.id">
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
  </div>
</template>

<script setup lang="ts">
/**
 * The requests one conversation's run is parked on — tool approvals and the
 * agent's questions — with everything needed to decide them in place. Any
 * page that runs a conversation shows this panel, so a gate is answered where
 * the run was started rather than on another page.
 */
import { ref, watch } from 'vue'
import { CircleAlert } from 'lucide-vue-next'
import { Alert, AlertDescription, AlertTitle } from '@repo/shadcn-vue/components/ui/alert'
import { parseJsonCamelCase } from '@/lib/case'
import { GatewayHttpError, getErrorMessage, forebrainApi } from '@/lib/api'
import { useI18n, type I18nKey } from '@/locales'
import {
  approvalDecisions,
  approvalJustification,
  permissionSuggestionFromAction,
  type ApprovalDecisionOption,
  type CommandApprovalScope,
  type PermissionSuggestionRecord,
} from '@/lib/approvalSuggestions'

const props = withDefaults(defineProps<{
  sessionId: string | null
  /** Bumped by the stream whenever the set of pending requests changed. */
  version: number
  /** Whether a subagent's request can open that subagent's own view here. */
  canOpenAgent?: boolean
}>(), { canOpenAgent: false })

const emit = defineEmits<{ (e: 'open-agent', agentId: string): void }>()

const { t } = useI18n()

type ActionRow = {
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

type AskAction = {
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

const pendingApprovals = ref<ActionRow[]>([])
const pendingAsk = ref<AskAction[]>([])
const answers = ref<Record<string, Record<string, string[] | string>>>({})
const others = ref<Record<string, Record<string, string>>>({})
const submitting = ref<Record<string, boolean>>({})
const errors = ref<Record<string, string>>({})
const denyFeedback = ref<Record<string, string>>({})
const loading = ref(false)
const loadError = ref<string | null>(null)
let generation = 0
let loadedSession = ''

function currentSession(): string {
  return String(props.sessionId ?? '').trim()
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
  loadError.value = null
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
          const form = JSON.parse(a.payloadJson) as AskAction['form']
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

watch(() => [props.sessionId, props.version] as const, () => {
  void load()
}, { immediate: true })
</script>
