import { t } from '@/locales'
import type { PermissionSuggestionRecord } from '@/lib/approvalSuggestions'

/**
 * The families of question an approval can ask, in the engine's own order of
 * evaluation (pkg/safety/engine.go): which family a pending action belongs to
 * decides the question, the fact lines, and the verb of each decision button.
 * The set of tools that ask lives in the engine; this file only names the
 * question each one asks, so the card and the workbench panel can share one
 * rendering.
 */
export type ApprovalFamily =
  | 'exit_plan_mode'        // already has its own card; forwarded, not redrawn
  | 'web_fetch'             // already shipped; copy untouched
  | 'web_search'
  | 'memory_note'
  | 'file_write'
  | 'file_edit'
  | 'apply_patch'
  | 'shell'
  | 'shell_outside_sandbox'
  | 'network_host'
  | 'request_permissions'
  | 'enter_plan_mode'
  | 'mcp_tool'
  | 'protected_read'
  | 'fallback'

export interface ApprovalFacts {
  family: ApprovalFamily
  /** The diff rows an edit-shaped family shows (sign, text); absent = no diff. */
  diff?: ['' | '+' | '-', string][]
  /** The one or two facts this decision turns on, as plain lines in order. */
  facts: string[]
  /** The file family's path; the thread's call line already names it. */
  path?: string
  /** Lines added / removed, when the engine can count them. */
  added?: number
  removed?: number
  /** The host a network approval is about. */
  host?: string
  protocol?: string
  port?: number
  /** The entries a permission request is asking for. */
  permissions?: { access: 'read' | 'write'; path: string }[]
  networkHosts?: string[]
  /** The memory note's own body, shown scrollable and never truncated. */
  bodyMarkdown?: string
}

/** A pending action's payload as the engine wrote it: snake_case keys, raw. */
export function parseApprovalPayload(payloadJson: string): Record<string, unknown> {
  try {
    const parsed = JSON.parse(payloadJson) as unknown
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>
    }
  } catch {
    // An unparseable payload still has a family (the kind) and a question.
  }
  return {}
}

/**
 * Which family a pending approval belongs to. The order is the engine's
 * evaluation order and must not be reshuffled: a network approval rides on a
 * shell payload, and a sandbox-denied retry can also be a shell action, so
 * those checks come before the kind's own branch.
 */
export function approvalFamily(kind: string, payload: Record<string, unknown>, suggestion: PermissionSuggestionRecord | null): ApprovalFamily {
  const k = String(kind ?? '').trim().toLowerCase()
  if (k === 'exit_plan_mode') return 'exit_plan_mode'
  // user_interaction is a question, not an approval; a caller that reaches
  // this function with one has miscalled it, and fallback is the two-choice
  // card that says only what it truly knows.
  if (k === 'user_interaction') return 'fallback'
  if (suggestion?.networkApprovalContext) return 'network_host'
  if (payload.approval_reason === 'sandbox_denied' || payload.sandbox_permissions === 'require_escalated' || payload.profile_elevation === true) {
    return 'shell_outside_sandbox'
  }
  if (k === 'request_permissions') return 'request_permissions'
  if (k === 'apply_patch') return 'apply_patch'
  if (k === 'write_file') return 'file_write'
  if (k === 'edit_file') return 'file_edit'
  // A read_file that asks is the protected-path read; an ordinary read never
  // parks the run, so anything else here is not a read approval.
  if (k === 'read_file' && payload.force_tool_approval === true) return 'protected_read'
  if (k === 'web_search') return 'web_search'
  if (k === 'memories_add_ad_hoc_note') return 'memory_note'
  if (k.startsWith('mcp__')) return 'mcp_tool'
  if (k === 'shell') return 'shell'
  if (k === 'enter_plan_mode') return 'enter_plan_mode'
  if (k === 'web_fetch') return 'web_fetch'
  return 'fallback'
}

/** Lines the way every surface counts them: a trailing newline is not a line. */
function textLines(text: string): number {
  if (text === '') return 0
  return text.split('\n').length - (text.endsWith('\n') ? 1 : 0)
}

/** The diff rows of a full-content write: every line is an added line. */
function writeDiff(content: string): ['+' | '-', string][] {
  if (content === '') return []
  const lines = content.split('\n')
  if (lines[lines.length - 1] === '') lines.pop()
  return lines.map((line) => ['+', line] as ['+' | '-', string])
}

/** The diff rows of a replacement with no unified diff to hand: all the old
 * lines, then all the new ones — the same shape the terminal's overlay falls
 * back to. */
function editDiff(oldText: string, newText: string): ['+' | '-', string][] {
  const rows: ['+' | '-', string][] = []
  for (const text of [oldText, newText]) {
    if (text === '') continue
    const lines = text.split('\n')
    if (lines[lines.length - 1] === '') lines.pop()
    for (const line of lines) rows.push([text === oldText ? '-' : '+', line])
  }
  return rows
}

/** The first hunk of a patch, verbatim: its @@ header plus that hunk's lines,
 * at most eight body rows — one representative place, not the whole patch. */
function patchHunk(patch: string): ['' | '+' | '-', string][] {
  const lines = patch.split('\n')
  const start = lines.findIndex((line) => line.startsWith('@@'))
  if (start < 0) return []
  const rows: ['' | '+' | '-', string][] = [['', lines[start]]]
  for (const line of lines.slice(start + 1, start + 9)) {
    if (line.startsWith('***') || line.startsWith('@@')) break
    // The sign lives in the row's own slot; the text is the line without it,
    // the same shape a write's or an edit's diff rows carry.
    if (line.startsWith('+')) rows.push(['+', line.slice(1)])
    else if (line.startsWith('-')) rows.push(['-', line.slice(1)])
    else rows.push(['', line])
  }
  return rows
}

/** Added / removed line counts across a whole patch body. The section headers
 * start with `***`, so a body line is told by its + or - alone. */
function patchDelta(patch: string): { added: number; removed: number } {
  let added = 0
  let removed = 0
  for (const line of patch.split('\n')) {
    if (line.startsWith('+')) added++
    else if (line.startsWith('-')) removed++
  }
  return { added, removed }
}

// The engine has exactly one dangerous-command rule — `rm` carrying a force
// flag (pkg/safety/shell.go, Rule "forced_rm") — and this is the sentence that
// rule reads as. No second rule may be invented here: when the engine grows
// one, its sentence grows with it. The path is the rm's first operand that is
// not a flag; when the command names none, the sentence says only what it can.
function forcedRmFact(command: string): string | null {
  const segments = String(command ?? '').split(/&&|\|\||;|\|/g)
  for (const segment of segments) {
    const sentence = forcedRmSegmentFact(segment)
    if (sentence) return sentence
  }
  return null
}

function forcedRmSegmentFact(segment: string): string | null {
  const tokens = segment.trim().split(/\s+/).filter(Boolean)
  const base = (token: string) => token.slice(token.lastIndexOf('/') + 1).toLowerCase()
  let i = 0
  // Unwrap the wrappers the engine's rule recurses into: sudo and env hand the
  // command on, and `sh -c` holds it in the next operand.
  while (i < tokens.length) {
    const name = base(tokens[i])
    if (name === 'sudo' || name === 'env') {
      i++
      continue
    }
    if (name === 'sh' || name === 'bash' || name === 'zsh' || name === 'dash' || name === 'ksh') {
      if (tokens[i + 1] === '-c' && tokens[i + 2]) return forcedRmSegmentFact(tokens[i + 2])
      i++
      continue
    }
    break
  }
  if (i >= tokens.length || base(tokens[i]) !== 'rm') return null
  let forced = false
  let path = ''
  for (let j = i + 1; j < tokens.length; j++) {
    const arg = tokens[j]
    if (arg === '--') break
    if (arg.startsWith('--')) {
      if (arg === '--force') forced = true
      continue
    }
    if (arg.startsWith('-')) {
      if (arg.slice(1).includes('f')) forced = true
      continue
    }
    if (!path) path = arg.replace(/^['"]|['"]$/g, '')
  }
  if (!forced) return null
  return path ? t('chat.approvalFactsForcedRm', { path }) : t('chat.approvalFactsForcedRmNoPath')
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : []
}

/** One permissions profile's entries, in the wire's two shapes: the entries
 * list, or the legacy read/write path arrays the same profile still marshals
 * to. Only read and access writes are asked about; a deny entry grants
 * nothing, so it has no line. */
function permissionEntries(permissions: unknown): { access: 'read' | 'write'; path: string }[] {
  const profile = permissions && typeof permissions === 'object' ? permissions as Record<string, unknown> : null
  const fileSystem = profile?.file_system && typeof profile.file_system === 'object' ? profile.file_system as Record<string, unknown> : null
  const entries: { access: 'read' | 'write'; path: string }[] = []
  if (Array.isArray(fileSystem?.entries)) {
    for (const raw of fileSystem.entries) {
      if (!raw || typeof raw !== 'object') continue
      const entry = raw as Record<string, unknown>
      const path = String(entry.path ?? '').trim()
      const access = String(entry.access ?? '').trim().toLowerCase()
      if (!path || (access !== 'read' && access !== 'write')) continue
      entries.push({ access, path })
    }
  }
  if (!entries.length && fileSystem) {
    for (const path of stringList(fileSystem.read)) entries.push({ access: 'read', path })
    for (const path of stringList(fileSystem.write)) entries.push({ access: 'write', path })
  }
  return entries
}

function permissionNetworkHosts(permissions: unknown): string[] {
  const profile = permissions && typeof permissions === 'object' ? permissions as Record<string, unknown> : null
  const network = profile?.network && typeof profile.network === 'object' ? profile.network as Record<string, unknown> : null
  return stringList(network?.hosts)
}

/**
 * The facts one decision turns on, extracted for a family `approvalFamily`
 * already settled. Nothing here invents a sentence the engine did not ground:
 * a family with no rule to quote (an unknown command, a fallback, an
 * enter-plan with no reason) shows no fact line at all.
 */
export function approvalFacts(family: ApprovalFamily, payload: Record<string, unknown>, suggestion: PermissionSuggestionRecord | null): ApprovalFacts {
  switch (family) {
    case 'file_write': {
      const content = String(payload.content ?? '')
      const added = textLines(content)
      const removed = Number(payload.existing_lines ?? 0) || 0
      return {
        family,
        path: String(payload.resolved_file_path ?? payload.file_path ?? ''),
        added,
        removed,
        facts: [removed > 0 ? t('chat.approvalFactsLines', { added, removed }) : t('chat.approvalFactsNewLines', { added })],
        diff: writeDiff(content),
      }
    }
    case 'file_edit': {
      const oldText = String(payload.old_string ?? '')
      const newText = String(payload.new_string ?? '')
      return {
        family,
        path: String(payload.resolved_file_path ?? payload.file_path ?? ''),
        added: textLines(newText),
        removed: textLines(oldText),
        facts: [t('chat.approvalFactsLines', { added: textLines(newText), removed: textLines(oldText) })],
        diff: editDiff(oldText, newText),
      }
    }
    case 'apply_patch': {
      const patch = String(payload.patch ?? '')
      const paths = stringList(payload.resolved_paths)
      const delta = patchDelta(patch)
      return {
        family,
        added: delta.added,
        removed: delta.removed,
        facts: [
          ...(paths.length ? [t('chat.approvalFactsFiles', { count: paths.length })] : []),
          `+${delta.added} −${delta.removed}`,
        ],
        diff: patchHunk(patch),
      }
    }
    case 'shell': {
      const sentence = forcedRmFact(String(payload.command ?? ''))
      return { family, facts: sentence ? [sentence] : [] }
    }
    case 'shell_outside_sandbox': {
      // A scoped retry is about the scope it would gain, not the sandbox it
      // left; everything else is the sandbox's own boundary, said once.
      if (payload.scoped_sandbox_retry === true) {
        const facts = permissionEntries(payload.additional_permissions)
          .filter((entry) => entry.access === 'write')
          .map((entry) => t('chat.approvalFactsWrite', { path: entry.path }))
        return { family, facts }
      }
      const facts = [String(payload.sandbox_denial_reason ?? '').trim()
        ? t('chat.approvalFactsSandboxOnlyProject')
        : t('chat.approvalFactsSandboxBlocked')]
      return { family, facts }
    }
    case 'network_host': {
      const context = suggestion?.networkApprovalContext
      const port = suggestion?.networkPort
      const parts = [context?.host ?? '', context ? context.protocol.toUpperCase() : '', port ? String(port) : '']
        .filter(Boolean)
      return {
        family,
        host: context?.host,
        protocol: context?.protocol,
        ...(port ? { port } : {}),
        facts: [parts.join(' · ')],
      }
    }
    case 'request_permissions': {
      const permissions = permissionEntries(payload.permissions)
      const networkHosts = permissionNetworkHosts(payload.permissions)
      return {
        family,
        permissions,
        networkHosts,
        facts: [
          ...permissions.map((entry) => entry.access === 'read'
            ? t('chat.approvalFactsRead', { path: entry.path })
            : t('chat.approvalFactsWrite', { path: entry.path })),
          ...networkHosts.map((host) => t('chat.approvalFactsReach', { host })),
        ],
      }
    }
    case 'enter_plan_mode': {
      // The reason is the model's own sentence; an empty one stays empty.
      const reason = String(payload.reason ?? '').trim()
      return { family, facts: reason ? [reason] : [] }
    }
    case 'mcp_tool': {
      // The first few scalar inputs, named the way the model named them.
      const facts: string[] = []
      for (const [key, value] of Object.entries(payload)) {
        if (facts.length >= 3) break
        if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
          facts.push(`${key} ${String(value)}`)
        }
      }
      return { family, facts }
    }
    case 'web_search': {
      const query = String(payload.query ?? '').trim()
      const rawLimit = Number(payload.results_limit)
      const limit = Number.isFinite(rawLimit) && rawLimit > 0 ? rawLimit : 5
      return { family, facts: [t('chat.approvalSearchFacts', { query, n: limit })] }
    }
    case 'memory_note': {
      const scope = String(payload.scope ?? '').trim().toLowerCase() === 'global' ? 'global' : 'project'
      return {
        family,
        facts: [t('chat.approvalMemoryFacts', {
          file: String(payload.filename ?? ''),
          scope: scope === 'global' ? t('chat.approvalMemoryScopeGlobal') : t('chat.approvalMemoryScopeProject'),
        })],
        bodyMarkdown: String(payload.note ?? ''),
      }
    }
    case 'protected_read': {
      return { family, facts: [String(payload.resolved_file_path ?? payload.file_path ?? '')] }
    }
    default:
      // exit_plan_mode and web_fetch draw their own cards; fallback carries no
      // fact, because the only thing it truly knows is the question.
      return { family: family === 'exit_plan_mode' || family === 'web_fetch' ? family : 'fallback', facts: [] }
  }
}

/** The one-line question a family asks, in the card's own header. */
export function approvalQuestion(family: ApprovalFamily, kind: string, payload: Record<string, unknown>): string {
  switch (family) {
    case 'file_write':
      return (Number(payload.existing_lines ?? 0) || 0) > 0
        ? t('chat.approvalWriteOverQuestion')
        : t('chat.approvalWriteNewQuestion')
    case 'file_edit':
      return t('chat.approvalEditQuestion')
    case 'apply_patch':
      return t('chat.approvalPatchQuestion')
    case 'shell':
      return t('chat.approvalShellQuestion')
    case 'shell_outside_sandbox':
      return t('chat.approvalShellOutsideQuestion')
    case 'network_host':
      return t('chat.approvalNetworkQuestion')
    case 'request_permissions':
      return t('chat.approvalPermissionsQuestion')
    case 'enter_plan_mode':
      return t('chat.approvalEnterPlanQuestion')
    case 'mcp_tool': {
      const parts = String(kind ?? '').trim().toLowerCase().split('__')
      const server = parts[1] ?? ''
      const tool = parts.slice(2).join('__')
      return t('chat.approvalMCPQuestion', { server, tool })
    }
    case 'web_search':
      return t('chat.approvalSearchQuestion')
    case 'memory_note':
      return t('chat.approvalMemoryQuestion')
    case 'protected_read':
      return t('chat.approvalProtectedReadQuestion')
    default:
      return t('chat.approvalFallbackQuestion')
  }
}
