export type ApprovalDecisionName =
  | 'accept'
  | 'accept_for_session'
  | 'accept_and_remember'
  | 'accept_with_execpolicy_amendment'
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
}

export interface PermissionSuggestionRecord {
  permissionToolName?: string
  permissionInput?: string
  /** The rule remembering this approval writes. For a fetch it is
   *  `domain:<host>`, which is what lets a button name its own scope. */
  prefixRuleContent?: string
  permissionMode?: string
  permissionReason?: string
  bypassSandbox?: boolean
  availableDecisions?: unknown[]
  proposedExecpolicyAmendment?: string[]
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
  return null
}

function normalizeSuggestion(input: PermissionSuggestionRecord | null | undefined): PermissionSuggestionRecord | null {
  if (!input || typeof input !== 'object') return null
  const record = input as PermissionSuggestionRecord & Record<string, unknown>
  const toolName = String(readField(record, 'permissionToolName', 'permission_tool_name') ?? '').trim()
  if (!toolName) return null
  const prefixRuleContent = optionalString(readField(record, 'prefixRuleContent', 'prefix_rule_content'))
  return {
    permissionToolName: toolName,
    permissionInput: String(readField(record, 'permissionInput', 'permission_input') ?? '').trim(),
    ...(prefixRuleContent ? { prefixRuleContent } : {}),
    permissionMode: optionalString(readField(record, 'permissionMode', 'permission_mode')),
    permissionReason: optionalString(readField(record, 'permissionReason', 'permission_reason')),
    bypassSandbox: readField(record, 'bypassSandbox', 'bypass_sandbox') === true,
    availableDecisions: arrayField(record, 'availableDecisions', 'available_decisions'),
    proposedExecpolicyAmendment: commandTokenArray(readField(record, 'proposedExecpolicyAmendment', 'proposed_execpolicy_amendment')),
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
