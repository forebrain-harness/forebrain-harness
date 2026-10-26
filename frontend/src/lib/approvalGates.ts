import { t, type I18nKey } from '@/locales'
import type { PermissionSuggestionRecord } from '@/lib/approvalSuggestions'
import { permissionSuggestionFromAction } from '@/lib/approvalSuggestions'
import { parseUnifiedDiff, type DiffRow } from '@/lib/unifiedDiff'
import type { ShellCardFile } from '@/composables/useChatStream'

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
  /** The diff rows an edit-shaped family shows; absent = no diff. The rows
   *  are the same shape every card's diff draws (DiffBlock), line numbers
   *  only where the source carried them — a patch's bare `@@` carries none,
   *  and numbers invented there would be numbers nobody counted. */
  diff?: DiffRow[]
  /** The files an apply_patch touches, one row each (the changed-files list
   *  every card shares); present only for a multi-file patch. */
  patchFiles?: ShellCardFile[]
  /** The one or two facts this decision turns on, as plain lines in order. */
  facts: string[]
  /** Lines added / removed, when the engine can count them. */
  added?: number
  removed?: number
  /** The host a network approval is about. */
  host?: string
  protocol?: string
  port?: number
  /** The entries a permission request is asking for. */
  permissions?: { access: 'read' | 'write'; path: string }[]
  /** Whether a permission request would open the network. The engine's profile
   * carries no host list, so this is the only thing the wire can say. */
  network?: boolean
  /** The engine's own sentence for this decision, already first in `facts`. */
  rationale?: string
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
  // A read that asks is the protected-path read, and the engine says so in the
  // payload: markProtectedApproval (pkg/tool/permissions.go) tags every
  // authorizeRead caller — read_file, lsp, and whatever reads through them —
  // with the protected_path reason. Reading the tag rather than the tool name is
  // what keeps an lsp read from falling through to the two-choice card. A forced
  // approval that names a resolved file is the same shape without the tag.
  if (payload.approval_reason === 'protected_path' ||
    (payload.force_tool_approval === true && String(payload.resolved_file_path ?? '').trim() !== '')) {
    return 'protected_read'
  }
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

/** A line count the engine wrote into the payload, or null when it wrote none.
 * The distinction matters: an absent field means "this payload predates the
 * field" and the caller must fall back, while a written 0 is the engine saying
 * nothing changed — 0 and 0 is a fact, not a missing value. */
function engineLineCount(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

/** The diff rows of a full-content write: every line is an added line, and
 *  the new file's numbers are the only ones there are. Only an overwrite
 *  shows them — a new file's size is the fact line's own sentence (prototype
 *  ① draws no panel), an overwrite's body is what would replace what is there
 *  (②). */
function writeDiff(content: string, overwrite: boolean): DiffRow[] {
  if (!overwrite || content === '') return []
  const lines = content.split('\n')
  if (lines[lines.length - 1] === '') lines.pop()
  return lines.map((line, index) => ({ mark: '+' as const, line: index + 1, text: line }))
}

/** The diff rows of a replacement with no unified diff to hand: all the old
 * lines, then all the new ones — the shape the terminal's overlay falls back to
 * when its own diff build fails. No line numbers exist for this shape. Only a
 * historical action reaches this: one recorded before the engine put its hunk
 * in the payload. */
function editDiff(oldText: string, newText: string): DiffRow[] {
  const rows: DiffRow[] = []
  for (const text of [oldText, newText]) {
    if (text === '') continue
    const lines = text.split('\n')
    if (lines[lines.length - 1] === '') lines.pop()
    for (const line of lines) rows.push({ mark: text === oldText ? '-' : '+', line: null, text: line })
  }
  return rows
}

/** The body rows an edit card draws: the payload carries exactly what can be
 * shown. */
const editHunkBodyRows = 40

/** The body rows a patch card draws: one representative place in the patch,
 * not the whole patch. */
const patchHunkBodyRows = 8

/** The first hunk of a real unified diff, as at most maxBodyRows rows. The
 *  engine's file_edit hunk carries its counters, so the rows carry the line
 *  numbers the diff itself counted — the same rows every card's diff draws. */
function unifiedHunk(text: string, maxBodyRows: number): DiffRow[] {
  const rows = parseUnifiedDiff(text)
  const fold = rows.findIndex((row) => row.mark === '…')
  return (fold >= 0 ? rows.slice(0, fold) : rows).slice(0, maxBodyRows)
}

/** The first hunk of an apply_patch body: its bare `@@` carries no counters,
 *  so the rows carry no line numbers rather than numbers invented here.
 *  Section headers (`***`) end the hunk; so does the caller's bound. */
function bareHunk(text: string, maxBodyRows: number): DiffRow[] {
  const lines = String(text ?? '').split('\n')
  const start = lines.findIndex((line) => line.startsWith('@@'))
  if (start < 0) return []
  const rows: DiffRow[] = []
  for (const line of lines.slice(start + 1)) {
    if (line.startsWith('***') || line.startsWith('@@')) break
    if (rows.length >= maxBodyRows) break
    if (line.startsWith('+')) rows.push({ mark: '+', line: null, text: line.slice(1) })
    else if (line.startsWith('-')) rows.push({ mark: '-', line: null, text: line.slice(1) })
    else rows.push({ mark: ' ', line: null, text: line })
  }
  return rows
}

/** The files an apply_patch touches, one row each with its own verb and its
 *  own +/− counts, read from the patch body the engine wrote. A `*** Move to:`
 *  retargets the section in force, the way the engine reads it. */
function patchFileRows(patch: string): ShellCardFile[] {
  const files: ShellCardFile[] = []
  let index = -1
  for (const raw of String(patch ?? '').split('\n')) {
    const section = /^\*\*\* (Update|Add|Delete) File: (.*)$/.exec(raw)
    if (section) {
      files.push({
        verb: section[1] === 'Add' ? 'created' : section[1] === 'Delete' ? 'deleted' : 'updated',
        path: section[2].trim(),
        added: 0,
        deleted: 0,
      })
      index = files.length - 1
      continue
    }
    const move = /^\*\*\* Move to: (.*)$/.exec(raw)
    if (move && index >= 0) {
      files[index] = { ...files[index], path: move[1].trim() }
      continue
    }
    if (index < 0) continue
    if (raw.startsWith('+')) files[index].added++
    else if (raw.startsWith('-')) files[index].deleted++
  }
  return files
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

// The engine names the dangerous rule it matched (pkg/safety/shell.go,
// `forced_rm` for an rm carrying a force flag) and pkg/tool/shell_tool.go puts
// that name in the payload. This table is the sentence each name reads as — the
// engine's conclusion, said once. A rule this table has no sentence for shows
// no fact line: a tokenizer of our own could decide an `rm -f *.log` was
// dangerous when the engine's own parser had given up on the command, and then
// the card would assert something the engine never concluded.
const dangerousRuleFacts: Record<string, I18nKey> = {
  forced_rm: 'chat.approvalFactsForcedRm',
}

function dangerousRuleFact(rule: string): string {
  const key = dangerousRuleFacts[rule]
  return key ? t(key) : ''
}

/** A payload key as a label: underscores read as spaces, the first letter
 * capitalised — the rule the terminal's approval block applies
 * (pkg/tui/overlays.go approvalFieldLabel). */
function mcpFieldLabel(key: string): string {
  const label = String(key ?? '').replace(/_/g, ' ').split(/\s+/).filter(Boolean).join(' ')
  if (!label) return 'Value'
  return label.charAt(0).toUpperCase() + label.slice(1)
}

/** A payload value on one line, cut at 160 characters. A multi-line value is
 * its first line only: a diff or a document body belongs to the tool card that
 * runs it, not to the line naming what this call sends. */
function mcpFieldValue(value: string | number | boolean): string {
  const text = String(value)
  const firstLine = text.includes('\n') ? text.slice(0, text.indexOf('\n')) : text
  const single = firstLine.replace(/\s+/g, ' ').trim()
  return single.length > 160 ? `${single.slice(0, 160)}…` : single
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : []
}

/**
 * One permission entry's location as a sentence, in the shapes
 * pkg/safety/permission_types.go marshals: `path` carries the filesystem path,
 * `pattern` carries a glob, and `value` carries a named location (kind plus an
 * optional subpath). A bare string is the legacy shape the same profile still
 * writes when every entry is a plain path, and it stays readable here so an
 * older row keeps its line.
 *
 * This is the only place the three shapes are told apart: reading the entry's
 * path object as a string is what printed a stringified object where a path
 * belongs.
 */
function entryLocation(raw: unknown): string {
  if (typeof raw === 'string') return raw.trim()
  if (!raw || typeof raw !== 'object') return ''
  const entry = raw as Record<string, unknown>
  const type = String(entry.type ?? '').trim()
  if (type === 'glob_pattern') return String(entry.pattern ?? '').trim()
  if (type === 'special') {
    const value = entry.value && typeof entry.value === 'object' ? entry.value as Record<string, unknown> : {}
    const kind = String(value.kind ?? '').trim()
    const subpath = String(value.subpath ?? '').trim()
    if (!kind) return ''
    return subpath ? `${kind}/${subpath}` : kind
  }
  // An untyped entry with a path is the normalised `path` shape.
  return String(entry.path ?? '').trim()
}

/** One permissions profile's entries, in the wire's two shapes: the entries
 * list, or the legacy read/write path arrays the same profile still marshals
 * to. Only read and writes are asked about; a deny entry grants nothing, so it
 * has no line, and an entry whose location cannot be read drops out rather
 * than printing an empty line. */
function permissionEntries(permissions: unknown): { access: 'read' | 'write'; path: string }[] {
  const profile = permissions && typeof permissions === 'object' ? permissions as Record<string, unknown> : null
  const fileSystem = profile?.file_system && typeof profile.file_system === 'object' ? profile.file_system as Record<string, unknown> : null
  const entries: { access: 'read' | 'write'; path: string }[] = []
  if (Array.isArray(fileSystem?.entries)) {
    for (const raw of fileSystem.entries) {
      if (!raw || typeof raw !== 'object') continue
      const entry = raw as Record<string, unknown>
      const path = entryLocation(entry.path)
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

/** Whether a permissions profile asks for network access at all. The engine's
 * profile has no host list (pkg/safety/permission_types.go
 * NetworkPermissionProfile is `enabled` alone), so a "reach <host>" line could
 * only ever be invented here; the fact is that the grant opens the network. */
function permissionNetworkEnabled(permissions: unknown): boolean {
  const profile = permissions && typeof permissions === 'object' ? permissions as Record<string, unknown> : null
  const network = profile?.network && typeof profile.network === 'object' ? profile.network as Record<string, unknown> : null
  return network?.enabled === true
}

/**
 * The facts one decision turns on, extracted for a family `approvalFamily`
 * already settled. Nothing here invents a sentence the engine did not ground:
 * a family with no rule to quote (an unknown command, a fallback, an
 * enter-plan with no reason) shows no fact line at all.
 *
 * The engine's own sentence for the decision leads every family. The payload's
 * `justification` is written by the runtime — a protected path's reason, a
 * plan-mode command that could not be proven read-only, the model's own words
 * for an escalation — and it is the one line on the card the reader can act on.
 * The test is whether the payload carries one, not which reason tag accompanies
 * it, for the reason pkg/tui/overlays.go:1842-1848 records: enumerating tags
 * means every new reason silently renders as a bare question.
 */
export function approvalFacts(family: ApprovalFamily, payload: Record<string, unknown>, suggestion: PermissionSuggestionRecord | null): ApprovalFacts {
  return withRationale(familyFacts(family, payload, suggestion), payload)
}

/** The engine's sentence, once. The sandbox branch already renders the denial
 * reason the engine gave, so the sentence wrapping that same reason is skipped
 * there — the reader gets it once, not twice. A shell escalation the engine
 * never refused has no denial reason, and its sentence is the only thing saying
 * what is being asked, so it stays. */
function withRationale(facts: ApprovalFacts, payload: Record<string, unknown>): ApprovalFacts {
  const rationale = String(payload.justification ?? '').trim()
  if (!rationale) return facts
  if (facts.facts.includes(rationale)) return facts
  if (facts.family === 'shell_outside_sandbox' && String(payload.sandbox_denial_reason ?? '').trim() !== '') return facts
  return { ...facts, rationale, facts: [rationale, ...facts.facts] }
}

function familyFacts(family: ApprovalFamily, payload: Record<string, unknown>, suggestion: PermissionSuggestionRecord | null): ApprovalFacts {
  switch (family) {
    case 'file_write': {
      const content = String(payload.content ?? '')
      const added = textLines(content)
      const removed = Number(payload.existing_lines ?? 0) || 0
      return {
        family,
        added,
        removed,
        facts: [removed > 0 ? t('chat.approvalFactsLines', { added, removed }) : t('chat.approvalFactsNewLines', { added })],
        // The panel is the overwrite's; a new file's size is the fact line.
        diff: writeDiff(content, removed > 0),
      }
    }
    case 'file_edit': {
      const oldText = String(payload.old_string ?? '')
      const newText = String(payload.new_string ?? '')
      // The engine (pkg/event.Build) counted the lines it changes and shipped
      // the first hunk of its own unified diff, so this card draws the same
      // summary the terminal does. Only an action recorded before those fields
      // existed falls back to reading the two texts, which is what produced
      // "all the old lines, then all the new ones" — a card that called a
      // thousand-row wall a diff.
      const added = engineLineCount(payload.added_lines)
      const removed = engineLineCount(payload.removed_lines)
      const hunk = unifiedHunk(String(payload.unified_diff ?? ''), editHunkBodyRows)
      return {
        family,
        added: added ?? textLines(newText),
        removed: removed ?? textLines(oldText),
        facts: [t('chat.approvalFactsLines', {
          added: added ?? textLines(newText),
          removed: removed ?? textLines(oldText),
        })],
        diff: hunk.length ? hunk : editDiff(oldText, newText),
      }
    }
    case 'apply_patch': {
      const patch = String(payload.patch ?? '')
      const paths = stringList(payload.resolved_paths)
      const delta = patchDelta(patch)
      const fileRows = patchFileRows(patch)
      return {
        family,
        added: delta.added,
        removed: delta.removed,
        // One line of facts, as the prototype prints it (:3503): how many
        // files and how much the patch moves, separated by the same dot every
        // other fact row uses.
        facts: [[
          paths.length ? t('chat.approvalFactsFiles', { count: paths.length }) : '',
          `+${delta.added} −${delta.removed}`,
        ].filter(Boolean).join(' · ')],
        // Several files: one row each (the changed-files list every card
        // shares), then one representative hunk.
        ...(fileRows.length > 1 ? { patchFiles: fileRows } : {}),
        diff: bareHunk(patch, patchHunkBodyRows),
      }
    }
    case 'shell': {
      // The engine's own conclusion, by the name it wrote into the payload; a
      // command it found nothing dangerous in has no line here.
      const sentence = dangerousRuleFact(String(payload.dangerous_rule ?? '').trim().toLowerCase())
      return { family, facts: sentence ? [sentence] : [] }
    }
    case 'shell_outside_sandbox': {
      // A scoped retry is about the scope it would gain, not the sandbox it
      // left; everything else is the sandbox's own boundary, plus the reason it
      // gave, verbatim: the denial can be about the network as readily as about
      // the filesystem, so a sentence naming files would be a claim the engine
      // never made. The engine writes it in English and the terminal shows it
      // the same way; translating it here would be inventing a second wording.
      if (payload.scoped_sandbox_retry === true) {
        const facts = permissionEntries(payload.additional_permissions)
          .filter((entry) => entry.access === 'write')
          .map((entry) => t('chat.approvalFactsWrite', { path: entry.path }))
        return { family, facts }
      }
      const denial = String(payload.sandbox_denial_reason ?? '').trim()
      return {
        family,
        facts: denial
          ? [t('chat.approvalFactsSandboxBlocked'), denial]
          : [t('chat.approvalFactsSandboxBlocked')],
      }
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
      const network = permissionNetworkEnabled(payload.permissions)
      return {
        family,
        permissions,
        ...(network ? { network } : {}),
        facts: [
          ...permissions.map((entry) => entry.access === 'read'
            ? t('chat.approvalFactsRead', { path: entry.path })
            : t('chat.approvalFactsWrite', { path: entry.path })),
          ...(network ? [t('chat.approvalFactsNetwork')] : []),
        ],
      }
    }
    case 'enter_plan_mode': {
      // The reason is the model's own sentence; an empty one stays empty.
      const reason = String(payload.reason ?? '').trim()
      return { family, facts: reason ? [reason] : [] }
    }
    case 'mcp_tool': {
      // The first few scalar inputs under the labels their own keys read as, in
      // key order so the same call always shows the same lines. A key is not a
      // sentence: `repo` is "Repo", and a body collapsed into a 500-line wall is
      // not a fact — the card states what is being sent, in the fewest words
      // that say it (pkg/tui/overlays.go:1780-1788 does the same labelling for
      // the terminal's block).
      const facts: string[] = []
      for (const key of Object.keys(payload).sort()) {
        if (facts.length >= 3) break
        const value = payload[key]
        if (typeof value !== 'string' && typeof value !== 'number' && typeof value !== 'boolean') continue
        facts.push(`${mcpFieldLabel(key)}: ${mcpFieldValue(value)}`)
      }
      return { family, facts }
    }
    case 'web_search': {
      const query = String(payload.query ?? '').trim()
      const rawLimit = Number(payload.results_limit)
      // The tool's own ceiling, so the card never promises more results than
      // one call can ask for (prototype :3418, Math.min(limit, 10)).
      const limit = Math.min(Number.isFinite(rawLimit) && rawLimit > 0 ? rawLimit : 5, 10)
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
    case 'web_fetch': {
      // The card's own question is drawn by the fetch gate; these are the two
      // facts the engine's payload adds on top, and each appears only when the
      // payload says it: a routed provider fetches server-side, and an opened
      // private space changes what "private URLs fail" means.
      const provider = String(payload.routed_provider ?? '').trim()
      const facts: string[] = []
      if (provider) facts.push(t('chat.webFetchRoutedFact', { provider }))
      if (payload.allow_private_ip === true) facts.push(t('chat.webFetchPrivateFact'))
      return { family, facts }
    }
    default:
      // exit_plan_mode draws its own card; fallback carries no fact, because
      // the only thing it truly knows is the question.
      return { family: family === 'exit_plan_mode' ? family : 'fallback', facts: [] }
  }
}

/** The one-line question a family asks, in the card's own header. */
export function approvalQuestion(family: ApprovalFamily, kind: string, payload: Record<string, unknown>): string {  switch (family) {
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
    case 'web_fetch':
      return t('chat.webFetchQuestion')
    case 'exit_plan_mode':
      // The plan approval asks the one question it always asked, wherever it
      // is drawn — the dock where the input is, a subagent's fallback frame.
      return t('chat.exitPlanQuestion')
    default:
      return t('chat.approvalFallbackQuestion')
  }
}

/**
 * One pending action's family, facts, and question, read from the action row
 * alone — the payload is the engine's own record of what it wants, and the
 * suggestion carries the network approval the payload alone cannot name. The
 * pure reader behind the store's `approvalGate`, so the card that hangs on the
 * gated call reads the same gate wherever it is drawn, provider or none.
 */
export function approvalGateOf(action: { kind: string; payloadJson: string; permissionSuggestion?: unknown }): { facts: ApprovalFacts; question: string } {
  const payload = parseApprovalPayload(action.payloadJson)
  const suggestion = permissionSuggestionFromAction(action as Parameters<typeof permissionSuggestionFromAction>[0])
  const family = approvalFamily(action.kind, payload, suggestion)
  return { facts: approvalFacts(family, payload, suggestion), question: approvalQuestion(family, action.kind, payload) }
}
