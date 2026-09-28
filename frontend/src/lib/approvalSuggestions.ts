export interface NetworkApprovalContext {
  host: string
  protocol: string
}

export interface NetworkPolicyAmendment {
  host: string
  action: 'allow' | 'deny'
}

export type ApprovalDecisionName =
  | 'accept'
  | 'accept_for_session'
  | 'accept_and_remember'
  | 'accept_with_execpolicy_amendment'
  | 'apply_network_policy_amendment'
  | 'grant_for_turn'
  | 'grant_for_turn_with_strict_auto_review'
  | 'grant_for_session'
  | 'decline'
  | 'cancel'

// CommandApprovalScope is the server's description of what remembering a shell
// command covers. The engine derives it; every surface renders it in its own
// words, which is why the wire carries the scope and not a sentence.
export type CommandApprovalScopeKind =
  | 'prefix'
  | 'command'
  | 'command_with_variants'

export interface CommandApprovalScope {
  kind: CommandApprovalScopeKind
  prefixes?: string[]
}

export interface ApprovalDecisionOption {
  decision: ApprovalDecisionName
  execpolicyAmendment?: string[]
  commandScope?: CommandApprovalScope
  networkPolicyAmendment?: NetworkPolicyAmendment
}

export interface PermissionSuggestionRecord {
  permissionToolName?: string
  permissionInput?: string
  permissionMode?: string
  permissionReason?: string
  bypassSandbox?: boolean
  availableDecisions?: unknown[]
  proposedExecpolicyAmendment?: string[]
  proposedNetworkPolicyAmendments?: NetworkPolicyAmendment[]
  networkApprovalContext?: NetworkApprovalContext
  networkPort?: number
  oneShotOnly?: boolean
}

export interface ApprovalActionLike {
  id: string
  kind: string
  status: string
  payloadJson: string
  createdAt?: number
  updatedAt?: number
  permissionSuggestion?: PermissionSuggestionRecord | null
}

export function permissionSuggestionFromAction(action: ApprovalActionLike): PermissionSuggestionRecord | null {
  const direct = normalizeSuggestion(action.permissionSuggestion)
  if (direct) return direct
  try {
    const payload = JSON.parse(action.payloadJson) as Record<string, unknown>
    return normalizeSuggestion(readField(payload, 'permissionSuggestion', 'permission_suggestion') as PermissionSuggestionRecord | undefined)
  } catch {
    return null
  }
}

export function approvalDecisions(suggestion: PermissionSuggestionRecord | null | undefined): ApprovalDecisionOption[] {
  const raw = Array.isArray(suggestion?.availableDecisions) ? suggestion.availableDecisions : []
  return raw.map(normalizeDecision).filter((item): item is ApprovalDecisionOption => item !== null)
}

function normalizeDecision(input: unknown): ApprovalDecisionOption | null {
  if (typeof input === 'string') {
    const decision = normalizeDecisionName(input)
    return decision ? { decision } : null
  }
  if (!input || typeof input !== 'object') return null
  const record = input as Record<string, unknown>
  const execValue = readField(record, 'acceptWithExecpolicyAmendment', 'accept_with_execpolicy_amendment')
  if (execValue && typeof execValue === 'object') {
    const body = execValue as Record<string, unknown>
    const amendment = commandTokenArray(readField(body, 'execpolicyAmendment', 'execpolicy_amendment'))
    if (amendment.length) {
      const scope = normalizeCommandScope(readField(body, 'commandScope', 'command_scope'))
      return {
        decision: 'accept_with_execpolicy_amendment',
        execpolicyAmendment: amendment,
        ...(scope ? { commandScope: scope } : {}),
      }
    }
  }
  const rememberValue = readField(record, 'acceptAndRemember', 'accept_and_remember')
  if (rememberValue && typeof rememberValue === 'object') {
    const scope = normalizeCommandScope(readField(rememberValue as Record<string, unknown>, 'commandScope', 'command_scope'))
    return { decision: 'accept_and_remember', ...(scope ? { commandScope: scope } : {}) }
  }
  const networkValue = readField(record, 'applyNetworkPolicyAmendment', 'apply_network_policy_amendment')
  if (networkValue && typeof networkValue === 'object') {
    const value = networkValue as Record<string, unknown>
    const nested = readField(value, 'networkPolicyAmendment', 'network_policy_amendment')
    const amendment = normalizeNetworkAmendment((nested && typeof nested === 'object' ? nested : value) as Record<string, unknown>)
    if (amendment) {
      return { decision: 'apply_network_policy_amendment', networkPolicyAmendment: amendment }
    }
  }
  return null
}

function normalizeSuggestion(input: PermissionSuggestionRecord | null | undefined): PermissionSuggestionRecord | null {
  if (!input || typeof input !== 'object') return null
  const record = input as PermissionSuggestionRecord & Record<string, unknown>
  const toolName = String(readField(record, 'permissionToolName', 'permission_tool_name') ?? '').trim()
  if (!toolName) return null
  const proposedNetwork = arrayField(record, 'proposedNetworkPolicyAmendments', 'proposed_network_policy_amendments')
    .map((item) => normalizeNetworkAmendment(item))
    .filter((item): item is NetworkPolicyAmendment => item !== null)
  const networkContextRaw = readField(record, 'networkApprovalContext', 'network_approval_context')
  const networkApprovalContext = normalizeNetworkContext(networkContextRaw)
  const networkPort = Number(readField(record, 'networkPort', 'network_port'))
  return {
    permissionToolName: toolName,
    permissionInput: String(readField(record, 'permissionInput', 'permission_input') ?? '').trim(),
    permissionMode: optionalString(readField(record, 'permissionMode', 'permission_mode')),
    permissionReason: optionalString(readField(record, 'permissionReason', 'permission_reason')),
    bypassSandbox: readField(record, 'bypassSandbox', 'bypass_sandbox') === true,
    availableDecisions: arrayField(record, 'availableDecisions', 'available_decisions'),
    proposedExecpolicyAmendment: commandTokenArray(readField(record, 'proposedExecpolicyAmendment', 'proposed_execpolicy_amendment')),
    proposedNetworkPolicyAmendments: proposedNetwork,
    ...(networkApprovalContext ? { networkApprovalContext } : {}),
    ...(Number.isInteger(networkPort) && networkPort > 0 && networkPort <= 65535 ? { networkPort } : {}),
    oneShotOnly: readField(record, 'oneShotOnly', 'one_shot_only') === true,
  }
}

function normalizeDecisionName(input: string): ApprovalDecisionName | null {
  const value = input.trim().toLowerCase() as ApprovalDecisionName
  switch (value) {
    case 'accept':
    case 'accept_for_session':
    case 'accept_and_remember':
    case 'grant_for_turn':
    case 'grant_for_turn_with_strict_auto_review':
    case 'grant_for_session':
    case 'decline':
    case 'cancel':
      return value
    default:
      return null
  }
}

function normalizeCommandScope(input: unknown): CommandApprovalScope | null {
  if (!input || typeof input !== 'object') return null
  const record = input as Record<string, unknown>
  const kind = String(record.kind ?? '').trim()
  if (kind !== 'prefix' && kind !== 'command' && kind !== 'command_with_variants') {
    return null
  }
  const prefixes = commandTokenArray(record.prefixes)
  return {
    kind,
    ...(prefixes.length ? { prefixes } : {}),
  }
}

function normalizeNetworkAmendment(input: unknown): NetworkPolicyAmendment | null {
  if (!input || typeof input !== 'object') return null
  const record = input as Record<string, unknown>
  const host = String(record.host ?? '').trim().toLowerCase().replace(/\.$/, '')
  const action = String(record.action ?? '').trim().toLowerCase()
  if (!host || (action !== 'allow' && action !== 'deny')) return null
  return { host, action }
}

function normalizeNetworkContext(input: unknown): NetworkApprovalContext | null {
  if (!input || typeof input !== 'object') return null
  const record = input as Record<string, unknown>
  const host = String(record.host ?? '').trim().toLowerCase().replace(/\.$/, '')
  const protocol = String(record.protocol ?? '').trim()
  return host && protocol ? { host, protocol } : null
}

function readField(record: Record<string, unknown>, camel: string, snake: string): unknown {
  return record[camel] ?? record[snake]
}

function arrayField(record: Record<string, unknown>, camel: string, snake: string): unknown[] {
  const value = readField(record, camel, snake)
  return Array.isArray(value) ? value : []
}

function commandTokenArray(input: unknown): string[] {
  if (!Array.isArray(input)) return []
  if (input.length === 0 || input.some((item) => typeof item !== 'string')) return []
  const tokens = input as string[]
  if (!tokens[0].trim() || tokens.some((token) => /[\0\r\n]/.test(token))) return []
  return [...tokens]
}

function optionalString(input: unknown): string | undefined {
  const value = String(input ?? '').trim()
  return value || undefined
}

// approvalJustification returns the sentence an approval has to show when the
// runtime supplied one. The question is not always "may this write happen?": a
// protected path, a plan-mode command whose effect could not be proven
// read-only, and an escalation the sandbox already refused each carry their own
// reason, and that reason is the only thing on the row the user can act on - an
// action id tells them nothing. What is tested is whether a reason is present,
// not which tag accompanies it: matching on one known tag means the next reason
// the runtime adds silently renders as a bare action id.
export function approvalJustification(payload: Record<string, unknown> | null | undefined): string {
  return String(payload?.justification ?? '').trim()
}
