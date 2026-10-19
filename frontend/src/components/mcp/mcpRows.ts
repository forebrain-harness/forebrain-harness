import type { McpLayer, McpServerRow } from '@/lib/api'
import { t } from '@/locales'

/**
 * What the three MCP tables and their drawer show, as pure functions: the
 * status badge one row carries, the transport label, where a row that belongs
 * to another layer is edited, and the payload a save submits. Keeping them here
 * — no Vue, no store — is what lets the rules be tested directly, and what keeps
 * the three pages' table and drawer rendering one row the same way.
 *
 * The rules come from the prototype (`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/
 * app-preview.html`: `mcpState`, `mcpStatusCell`, `mcpType`, `mcpEditElsewhere`,
 * `mcpFormBody`) and from the gateway's own read side, so a badge says the same
 * thing the runtime does.
 */

/** The tone a badge is drawn in: green, amber, grey, red. */
export type McpTone = 'ok' | 'warn' | 'gray' | 'err'

/** One row's status badge: the one label the column shows, and what its hover
 * tip adds — the failure's own text, why it is off, or an OAuth state. */
export interface McpStatus {
  tone: McpTone
  label: string
  tip: string
}

/** The value a surface sends back in place of a secret it was not allowed to
 * read: the gateway keeps what is stored when it sees this. */
export const MCP_REDACTED = '[REDACTED]'

/** A row awaiting the operator's confirmation: a project entry with no recorded
 * decision. Only the project view produces such rows, and it never stamps one
 * `allowed`. */
function awaitingDecision(row: McpServerRow): boolean {
  return row.pending === true && row.scope === 'project' && row.allowed !== true
}

/** The tool-count suffix a connected or allowed badge carries, or nothing when
 * the count is zero or was never reported: "0 个工具" is not a fact about the
 * server, it is a fact about a view that could not ask. */
function toolSuffix(row: McpServerRow): string {
  const count = Number(row.toolCount ?? 0)
  return count > 0 ? ` · ${t('mcp.toolsCount', { n: count })}` : ''
}

/** The OAuth note a tip carries. A row whose credentials need the operator is
 * not failing — it is waiting — so it rides the tip instead of the label. */
function oauthNote(row: McpServerRow): string {
  switch (row.authStatus) {
    case 'needs-auth':
      return t('mcp.status.oauthNeeded')
    case 'failed':
      return t('mcp.status.oauthFailed')
    default:
      return ''
  }
}

/**
 * mcpStatus is the one badge a row shows, in precedence order:
 *
 *   待确认 (a project entry nobody decided) → 已拒绝 (this project declined it) →
 *   未运行 · 被覆盖 (a higher layer's same-named entry wins) → 配置里已禁用
 *   (`enabled: false`) → 已禁用 (the disable store keeps it out of the next
 *   session) → 连接失败 (the server's own text, verbatim) → 已允许 / 已连接
 *   (with the tool count when there is one) → 连接中 → 未运行.
 *
 * The connection states are the runtime's own (`pkg/mcp/registry.go`,
 * `ConnStatus`): `unknown`, `connecting`, `connected`, `disconnected`, `error`
 * and `cancelled`. Only `connected` — and a row a view could not ask, which
 * carries no state at all — reads as 已连接; `connecting` says so, and every
 * other one, including a connection that dropped and one the operator skipped,
 * is 未运行. A row is never called connected because it merely exists.
 *
 * The failures and the OAuth state are what the tip is for: the column carries a
 * badge, and the words behind it are one hover away.
 */
export function mcpStatus(row: McpServerRow): McpStatus {
  const tip = withOAuth(row)
  if (awaitingDecision(row)) return { tone: 'warn', label: t('mcp.status.pending'), tip: '' }
  // A declined entry does not load, and the row stays: allowing it later is the
  // way back, so its tip says how.
  if (row.declined === true && row.scope === 'project') {
    return { tone: 'gray', label: t('mcp.status.declined'), tip: t('mcp.status.declinedTip') }
  }
  // A shared entry a higher layer's same-named one overrides never runs: the
  // badge says what it is doing instead of naming the entry that does run.
  if ((row.shadowedBy ?? '') !== '') {
    return { tone: 'gray', label: t('mcp.status.shadowed'), tip: t('mcp.status.shadowedTip', { name: row.shadowedByName || '—' }) }
  }
  if (row.disabledByConfig === true) return { tone: 'gray', label: t('mcp.status.disabledByConfig'), tip: withOAuth(row, 'enabled: false') }
  if (row.disabledNextSession === true) return { tone: 'gray', label: t('mcp.status.disabledNextSession'), tip: withOAuth(row, t('mcp.status.disabledNextSessionTip')) }
  // The runtime spells a failed connection `error`; `failed` is tolerated for
  // any older payload still in flight.
  if (row.connStatus === 'error' || row.connStatus === 'failed' || (row.error ?? '').trim() !== '') {
    return { tone: 'err', label: t('mcp.status.failed'), tip: withOAuth(row, (row.error ?? '').trim() || t('mcp.status.failed')) }
  }
  if (row.scope === 'project' && row.allowed === true) {
    return { tone: 'ok', label: `${t('mcp.status.allowed')}${toolSuffix(row)}`, tip }
  }
  if (row.connStatus === 'connecting') return { tone: 'warn', label: t('mcp.status.connecting'), tip }
  // A view that could ask no runner reports no state at all: a configured entry
  // that is in the session is connected, and that is what `running` alone says.
  const state = row.connStatus ?? ''
  if (row.running === true && (state === '' || state === 'connected')) {
    return { tone: 'ok', label: `${t('mcp.status.connected')}${toolSuffix(row)}`, tip }
  }
  return { tone: 'gray', label: t('mcp.status.idle'), tip }
}

/** The OAuth half of a badge's tip, when the runtime reported one: a row whose
 * credentials need the operator is not failing, it is waiting, so it rides the
 * tip rather than the label. */
function withOAuth(row: McpServerRow, base = ''): string {
  return [base, oauthNote(row)].filter((part) => part !== '').join(' · ')
}

/** One row's transport, in the short form the table's 类型 column shows: the
 * column says only which transport an entry uses, and everything else about it
 * — its OAuth flow, its headers — is the drawer's to show. */
export type McpTransportName = 'stdio' | 'http'

/** mcpTypeLabel names the transport the table column shows. */
export function mcpTypeLabel(row: McpServerRow): McpTransportName {
  return row.transport === 'streamable_http' ? 'http' : 'stdio'
}

/** Where a row that belongs to another layer is edited: the page's own route and
 * the link's words. */
export interface McpEditPlace {
  to: string
  label: string
}

/**
 * mcpEditPlace answers where one layer's entries are edited: a project's in its
 * own space (the caller passes the id), the shared list in the settings tab, a
 * primary agent's on the menu bar's MCP page.
 */
export function mcpEditPlace(scope: McpLayer, projectId = ''): McpEditPlace {
  if (scope === 'project') {
    return {
      to: projectId ? `/projects/${encodeURIComponent(projectId)}/mcp` : '',
      label: t('mcp.editPlace.project'),
    }
  }
  if (scope === 'global') return { to: '/settings?tab=mcp', label: t('mcp.editPlace.global') }
  return { to: '/mcp', label: t('mcp.editPlace.agent') }
}

/** The drawer's OAuth fields: the ones a form may edit. The stored access and
 * refresh tokens are not among them — they are credentials the runtime writes,
 * and the form sends them back as they were read. */
export interface McpOAuthForm {
  mode: string
  authorizationUrl: string
  tokenUrl: string
  clientId: string
  clientSecret: string
  /** Whitespace-separated, the way the drawer's one input holds them. */
  scopes: string
  redirectUrl: string
}

/** The drawer's form: one entry's fields as the form holds them. Numbers are
 * strings because a form field that may be left empty to mean "the default"
 * cannot be a number. */
export interface McpServerForm {
  name: string
  transport: 'stdio' | 'streamable_http'
  target: string
  required: boolean
  enabled: boolean
  /** Seconds; empty means the default. */
  startupTimeout: string
  /** Seconds; empty means no limit on a single call. */
  requestTimeout: string
  inheritParentEnv: boolean
  expectedCommandSha256: string
  disableStandaloneSse: boolean
  env: McpKeyValueRow[]
  headers: McpKeyValueRow[]
  oauth: McpOAuthForm
  defaultToolsApprovalMode: string
}

/** One key/value row the drawer is editing. A blank key is a row nobody filled
 * in and is not sent; a blank value on a key that is stored removes it. */
export interface McpKeyValueRow {
  key: string
  value: string
}

/** The form an untouched entry starts as: what the drawer opens with. */
export function mcpFormOf(row: McpServerRow | null): McpServerForm {
  const oauth = row?.oauth
  return {
    name: row?.name ?? '',
    transport: row?.transport === 'streamable_http' ? 'streamable_http' : 'stdio',
    target: row?.target ?? '',
    required: row?.required === true,
    enabled: row?.enabled !== false,
    startupTimeout: timeoutField(row?.startupTimeout),
    requestTimeout: timeoutField(row?.requestTimeout),
    inheritParentEnv: row?.inheritParentEnv === true,
    expectedCommandSha256: row?.expectedCommandSha256 ?? '',
    disableStandaloneSse: row?.disableStandaloneSse === true,
    env: keyValueRows(row?.env),
    headers: keyValueRows(row?.headers),
    oauth: {
      mode: oauth?.mode ?? '',
      authorizationUrl: oauth?.authorizationUrl ?? '',
      tokenUrl: oauth?.tokenUrl ?? '',
      clientId: oauth?.clientId ?? '',
      clientSecret: oauth?.clientSecret ?? '',
      scopes: (oauth?.scopes ?? []).join(' '),
      redirectUrl: oauth?.redirectUrl ?? '',
    },
    defaultToolsApprovalMode: row?.defaultToolsApprovalMode ?? '',
  }
}

function timeoutField(seconds: number | undefined): string {
  const value = Number(seconds ?? 0)
  return value > 0 ? String(value) : ''
}

/** One stored map as table rows, in the file's own key order. */
function keyValueRows(map: Record<string, string> | undefined): McpKeyValueRow[] {
  return Object.entries(map ?? {}).map(([key, value]) => ({ key, value }))
}

/** A form's seconds as the number the wire carries: an empty field is zero,
 * which is how the configuration spells "use the default". */
function timeoutSeconds(raw: string): number {
  const value = Number(raw.trim())
  return Number.isFinite(value) && value > 0 ? value : 0
}

function scopesOf(raw: string): string[] {
  return raw
    .split(/\s+/)
    .map((scope) => scope.trim())
    .filter((scope) => scope !== '')
}

/**
 * mcpKeyValuePayload turns one table into the map a save submits, or null when
 * the table asks for no change at all.
 *
 * The gateway merges key by key: a key the body omits is untouched, the
 * placeholder keeps the stored value, an empty value removes the key. So an
 * untouched key travels back as `[REDACTED]` (the drawer showed exactly that and
 * nothing else is knowable about it), a key the operator deleted travels as an
 * empty value, and a new or edited one travels as itself.
 */
export function mcpKeyValuePayload(
  stored: Record<string, string> | undefined,
  rows: McpKeyValueRow[],
): Record<string, string> | null {
  const original = stored ?? {}
  const form = new Map<string, string>()
  for (const row of rows) {
    const key = row.key.trim()
    if (key === '') continue
    form.set(key, row.value)
  }
  const payload: Record<string, string> = {}
  let changed = false
  for (const [key, value] of form) {
    const before = original[key]
    if (before !== undefined && before === value) {
      // Untouched: keep whatever is stored, which is what the placeholder says.
      payload[key] = MCP_REDACTED
      continue
    }
    if (before === undefined && value === '') continue
    payload[key] = value
    changed = true
  }
  for (const key of Object.keys(original)) {
    if (form.has(key)) continue
    // Gone from the table: an empty value is how the gateway spells "remove".
    payload[key] = ''
    changed = true
  }
  if (!changed) return null
  return payload
}

/**
 * mcpOAuthPayload builds the OAuth block a save submits, or null when the form
 * asks for no change. The block is the whole block — the gateway replaces it —
 * so the credentials the form does not edit are sent back exactly as they were
 * read (`[REDACTED]` keeps them) instead of being dropped.
 */
export function mcpOAuthPayload(
  original: McpServerRow['oauth'] | null | undefined,
  form: McpOAuthForm,
): Record<string, unknown> | null {
  const scopes = scopesOf(form.scopes)
  const stored = original ?? null
  const unchanged =
    stored !== null &&
    stored.mode === form.mode.trim() &&
    stored.authorizationUrl === form.authorizationUrl.trim() &&
    stored.tokenUrl === form.tokenUrl.trim() &&
    stored.clientId === form.clientId.trim() &&
    stored.clientSecret === form.clientSecret.trim() &&
    stored.redirectUrl === form.redirectUrl.trim() &&
    sameScopes(stored.scopes ?? [], scopes)
  if (unchanged) return null
  const empty =
    form.mode.trim() === '' &&
    form.authorizationUrl.trim() === '' &&
    form.tokenUrl.trim() === '' &&
    form.clientId.trim() === '' &&
    form.clientSecret.trim() === '' &&
    form.redirectUrl.trim() === '' &&
    scopes.length === 0 &&
    (stored === null ||
      (stored.accessToken === '' &&
        stored.refreshToken === '' &&
        stored.tokenType === '' &&
        (stored.scopes ?? []).length === 0))
  if (empty) return null
  return {
    mode: form.mode.trim(),
    access_token: stored?.accessToken ?? '',
    token_type: stored?.tokenType ?? '',
    client_id: form.clientId.trim(),
    client_secret: form.clientSecret.trim(),
    token_url: form.tokenUrl.trim(),
    authorization_url: form.authorizationUrl.trim(),
    redirect_url: form.redirectUrl.trim(),
    refresh_token: stored?.refreshToken ?? '',
    scopes,
  }
}

function sameScopes(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((scope, i) => scope === b[i])
}

/**
 * mcpWritePayload is the body a save submits: the fields the operator changed,
 * plus the two the wire always needs — the name, which is the entry's identity,
 * and the transport, which decides how the target is read. Everything the form
 * did not touch is left out, because the gateway leaves an omitted field as it
 * is; that is what keeps the per-tool approval table (which has no form field)
 * intact across a save.
 *
 * With no original this is a create, and every field the form holds is sent.
 */
export function mcpWritePayload(
  original: McpServerRow | null,
  form: McpServerForm,
): Record<string, unknown> {
  const created = original === null
  const name = form.name.trim()
  const payload: Record<string, unknown> = { name, transport: form.transport }

  const target = form.target
  if (created || target !== (original.target ?? '')) payload.target = target
  if (created || form.required !== original.required) payload.required = form.required
  if (created || form.enabled !== original.enabled) payload.enabled = form.enabled

  const startup = timeoutSeconds(form.startupTimeout)
  if (created || startup !== Number(original.startupTimeout ?? 0)) payload.startup_timeout = startup
  const request = timeoutSeconds(form.requestTimeout)
  if (created || request !== Number(original.requestTimeout ?? 0)) payload.request_timeout = request

  if (created || form.inheritParentEnv !== original.inheritParentEnv) {
    payload.inherit_parent_env = form.inheritParentEnv
  }
  if (created || form.disableStandaloneSse !== original.disableStandaloneSse) {
    payload.disable_standalone_sse = form.disableStandaloneSse
  }
  const sha = form.expectedCommandSha256.trim()
  if (created || sha !== (original.expectedCommandSha256 ?? '')) payload.expected_command_sha256 = sha
  const approval = form.defaultToolsApprovalMode.trim()
  if (created || approval !== (original.defaultToolsApprovalMode ?? '')) {
    payload.default_tools_approval_mode = approval
  }

  const env = mcpKeyValuePayload(created ? undefined : original.env, form.env)
  if (env) payload.env = env
  const headers = mcpKeyValuePayload(created ? undefined : original.headers, form.headers)
  if (headers) payload.headers = headers

  const oauth = mcpOAuthPayload(created ? null : original.oauth, form.oauth)
  if (oauth) payload.oauth = oauth
  return payload
}
