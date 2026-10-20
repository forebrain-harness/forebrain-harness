import axios, { type AxiosError } from 'axios'

import { toCamelCase, toSnakeCase } from './case'
import type { ForebrainTokenBudget } from './forebrainGatewayRuntime'
import type { LspRecommendationChoice } from './lspRecommendation'
import { reportGatewayUnauthorized } from './gatewaySession'

export function getErrorMessage(error: unknown): string {
  const err = error as AxiosError
  if (err?.response?.data != null) {
    const data = err.response.data
    if (typeof data === 'string') return data
    if (typeof data === 'object' && data !== null) {
      // The gateway answers with {"error": "..."}; other backends use
      // {"message": "..."}.
      for (const key of ['error', 'message'] as const) {
        if (key in data) {
          const text = String((data as Record<string, unknown>)[key])
          if (text) return text
        }
      }
    }
  }
  return err?.message ? String(err.message) : 'Request failed'
}

const API_BASE = '/api'

/**
 * One page of a server-paged list (plan 071): every list endpoint the web
 * pages page through applies the search, the filters and the order on the
 * server and answers its own list key plus these three fields. `page` is the
 * page actually served, which is the last one when the request asked past it.
 */
export interface ListPageMeta {
  total: number
  page: number
  pageSize: number
}

/** One list request: the search, the order and the page a table is on. */
export interface ListQuery {
  q?: string
  sort?: string
  order?: 'asc' | 'desc'
  page: number
  pageSize: number
  /** The endpoint's own filters, camelCased here and snake_cased on the wire. */
  [filter: string]: unknown
}

/**
 * The query string every list endpoint reads: `q`, `sort`, `order`, `page`,
 * `page_size` and the endpoint's own filters (snake_cased). Unset values are
 * left out, so an untouched filter never reaches the server as "undefined".
 */
export function listParams(query: ListQuery): URLSearchParams {
  const { q, sort, order, page, pageSize, ...rest } = query
  const params = new URLSearchParams()
  if (q) params.set('q', q)
  if (sort) params.set('sort', sort)
  if (order) params.set('order', order)
  if (page) params.set('page', String(page))
  if (pageSize) params.set('page_size', String(pageSize))
  for (const [key, value] of Object.entries(toSnakeCase(rest) as Record<string, unknown>)) {
    if (value === undefined || value === null || value === '') continue
    params.set(key, String(value))
  }
  return params
}

/** A request the gateway answered with an error status. */
export class GatewayHttpError extends Error {
  constructor(readonly status: number, body: string) {
    super(body || `HTTP ${status}`)
    this.name = 'GatewayHttpError'
  }
}

/**
 * One thing a user message attached: an upload, by the file id the page opens
 * it with, or a workspace image, by the path it was picked at.
 */
export interface ChatAttachmentRecord {
  fileId?: string
  path?: string
  name: string
  mediaType?: string
}

export interface ChatMessageRecord {
  id?: string
  rowId?: number
  role: string
  content: string
  partsJson?: string | null
  toolStepId?: string | null
  toolMetaJson?: string | null
  createdAt?: number
	 runId?: string
  planJson?: string | null
  /** What a user message attached, in the order it was attached. */
  attachments?: ChatAttachmentRecord[] | null
  /** Who wrote a user message on the person's behalf ('heartbeat'). */
  origin?: string
  /**
   * A tool row's or a `!cmd` row's own execution window; on a "worked" row —
   * the line that closes a run, after the last row it wrote — the run's clock.
   */
  runStartedAt?: string
  runFinishedAt?: string
  workedDurationMs?: number
  /**
   * How long the run waited for the model's first output, on its "worked" row
   * (plan 015). Absent for a run that never measured it.
   */
  firstTokenMs?: number
  /** The run's final checklist state, on its "worked" row. */
  planDone?: number
  planTotal?: number
  planActive?: string
  /** A finished compaction the server placed in the history; its row role is "compaction". */
  compaction?: Record<string, unknown> | null
  /** A line of a /goal the server placed in the history; its row role is "goal". */
  goal?: Record<string, unknown> | null
  /**
   * A tool row's subagent_* card facts, as the gateway derived them from the
   * transcript — the reloaded card says what the live one said.
   */
  subagentCall?: import('@/composables/useChatStream').SubagentCall | null
  memoryCitation?: MemoryCitation | null
}

export interface MemoryCitationEntry {
  path: string
  lineStart: number
  lineEnd: number
  note: string
}

export interface MemoryCitation {
  entries: MemoryCitationEntry[]
  rolloutIds: string[]
}

export interface PendingInputPreview {
  pendingSteers: string[]
  rejectedSteers: string[]
  queuedMessages: string[]
}

export interface RunInputResponse {
  accepted: boolean
  message?: string
  attachments?: string[]
  mentionImages?: string[]
  preview: PendingInputPreview
}

/** Where a message the user sent a subagent went (plan 005's delivery). */
export type SubagentDelivery = 'started' | 'steered' | 'queued'

export interface SubagentInputResponse {
  delivery: SubagentDelivery
  preview: PendingInputPreview
}

/** A message taken back out of a subagent before it answered, whole. */
export interface SubagentWithdrawnInput {
  message?: string
  attachments?: string[]
  mentionImages?: string[]
}

export interface SubagentWithdrawResponse {
  withdrawn: SubagentWithdrawnInput[]
  any?: boolean
  preview: PendingInputPreview
}

export interface ModelRecord {
  modelId: string
  modelName: string
  provider?: string
  apiModel?: string
  contextWindow?: number
  defaultMaxTokens?: number
  costPer1mIn?: number
  costPer1mOut?: number
  canReason?: boolean
  supportsAttachments?: boolean
  supportedReasoningEfforts?: string[]
  defaultReasoningEffort?: string
  isDefault?: boolean
  source?: string
  fetchedAt?: string
}

// Per-provider listing status: a provider block can be fresh (source +
// fetchedAt) or missing with a reason (error), and the two must not collapse
// into "no models".
export interface ModelProviderStatus {
  provider: string
  source?: string
  fetchedAt?: string
  error?: string
}

export interface ModelCatalogListing {
  records: ModelRecord[]
  status?: ModelProviderStatus[]
}

export interface TaskBoardCard {
  id: string
  column: string
  status: string
  kind?: string
  title: string
  sessionId?: string
  channelId?: string
  ownerAgent?: string
  progress: number
  updatedAt: number
  createdAt: number
  blockedReason?: string
  workspacePath?: string
  repositoryPath?: string
  runId?: string
}

export interface TaskBoard {
  columns: Record<string, TaskBoardCard[]>
  records: TaskBoardCard[]
}

export interface MemorySettings {
  enabled: boolean
  useMemories: boolean
  generateMemories: boolean
}

export interface SessionContextBudget {
  limitTokens?: number
  usedTokens?: number
  totalItems?: number
  usedItems?: number
}

export interface SessionContextItem {
  sourceId?: string
  layer?: string
  title?: string
  content?: string
  priority?: number
  estimatedTokens?: number
  pinned?: boolean
  ttlSeconds?: number
  lastUsedAt?: string
  debugReason?: string
}

export interface SessionContextProvenance {
  sourceId?: string
  included?: boolean
  reason?: string
  estimatedTokens?: number
}

export interface SessionToolResultSpill {
  sessionId?: string
  runId?: string
  toolName?: string
  callId?: string
  path?: string
  originalBytes?: number
  omittedBytes?: number
  totalLines?: number
  createdAtUtc?: string
}

export interface SessionContextTimelineEntry {
  kind?: string
  sessionId?: string
  runId?: string
  createdAtUtc?: string
  trigger?: string
  strategy?: string
  reason?: string
  summarySource?: string
  scope?: string
  boundaryId?: string
  replacedItems?: number
  windowNumber?: number
  summary?: string
  tokensBefore?: number
  tokensAfter?: number
  reactive?: boolean
  toolName?: string
  callId?: string
  path?: string
  originalBytes?: number
  omittedBytes?: number
  totalLines?: number
}

export interface SessionContextDebug {
  sessionId?: string
  mode?: string
  budget?: SessionContextBudget
  contextPressure?: string
  generatedAt?: string
  itemCount?: number
  topItems?: SessionContextItem[]
  provenanceCount?: number
  evictionCount?: number
  topEvictions?: SessionContextProvenance[]
  toolResultSpillCount?: number
  toolResultSpills?: SessionToolResultSpill[]
  contextTimeline?: SessionContextTimelineEntry[]
  runId?: string
  items?: SessionContextItem[]
  provenance?: SessionContextProvenance[]
  evictionDetails?: SessionContextProvenance[]
  modelContextTokens?: number
  effectiveContextTokens?: number
  availableWindowTokens?: number
  reserveTokens?: number
  warningThresholdTokens?: number
  blockingThresholdTokens?: number
  remainingContextTokens?: number
  conversationTokens?: number
  projectedContextTokens?: number
  tokenAttribution?: Record<string, unknown>
  windowNumber?: number
  activeBoundaryId?: string
  compactAudit?: Record<string, unknown>
  compactDiff?: Record<string, unknown>[]
  compactExport?: Record<string, unknown>
}

export interface SubagentHistoryRecord {
  taskId?: string
  agentId?: string
  runId?: string
  parentRunId?: string
  parentToolCallId?: string
  taskIndex?: number
  executionId?: string
  workerSessionId?: string
  sessionId?: string
  task?: string
  status?: string
  output?: string
  error?: string
  startedAt?: number
  updatedAt?: number
  finishedAt?: number
}

export interface SessionEventPage {
  sessionId: string
  events: import('./forebrainGatewayRuntime').ForebrainRunEvent[]
  nextCursor: number
  highWater: number
  hasMore: boolean
  schemaVersion: number
}

export interface SlashCommandRecord {
  canonicalName: string
  name: string
  description: string
  category: string
  argumentHint: string
  actionKind: string
  allowedModes: string[]
  supportsInlineArgs: boolean
  availableDuringRun: boolean
  availableInSideConversation: boolean
  visibility: string
}

export interface PermissionRuleValue {
  toolName: string
  ruleContent?: string
  commandPrefix?: string[]
  command?: string
  bypassSandbox?: boolean
}

export interface PermissionRuleRecord {
  source: string
  behavior: string
  toolName: string
  ruleContent?: string
  /** The rule exactly as stored; removing a listed rule sends this back. */
  rule: PermissionRuleValue
}

export interface PermissionRulesResponse {
  mode: string
  rules: PermissionRuleRecord[]
}

export interface PermissionDecision {
  behavior: string
  mode?: string
  reason?: string
  matchedRule?: {
    behavior?: string
    source?: string
    value?: { toolName?: string; ruleContent?: string }
  }
}

/** One tool the active primary agent's runtime exposes to the model. */
export interface ToolMetaRecord {
  name: string
  description?: string
  category?: string
  readOnly?: boolean
  destructive?: boolean
  concurrencySafe?: boolean
  interruptBehavior?: string
  alwaysLoad?: boolean
  inputSchema?: unknown
  mcpServer?: string
}

/**
 * One server's startup state, as the gateway reports it.
 *
 * These are the four states the runtime distinguishes. They are not a nuance of
 * the configured record above: "connecting", "error" with a reason, and
 * "cancelled" because the operator skipped the server are three different
 * answers to "why are its tools missing", and a UI that collapses them into
 * "not connected" leaves the reader with no way to tell.
 */
export type McpConnStatus = 'unknown' | 'connecting' | 'connected' | 'error' | 'cancelled' | 'disconnected'

export interface McpServerRecord {
  name: string
  transport?: string
  urlSet?: boolean
  oauthConfigured?: boolean
  oauthOverlay?: boolean
  officialUrl?: boolean
  scope?: string
  /**
   * Present only when the gateway had a runtime view to report.
   *
   * A state this client does not know is still a state: the vocabulary belongs
   * to the runtime, and a closed union here would make a server-side addition a
   * compile error instead of a row the UI renders as itself.
   */
  connStatus?: McpConnStatus | (string & {})
  /** The server's own failure text, kept verbatim. */
  error?: string
  toolCount?: number
  /** The run cannot do without this server's tools. */
  required?: boolean
  authStatus?: string
  generation?: string
  pending?: boolean
  /** The config file the entry came from. */
  source?: string
  /** False for a server the agent's disable store kept out of this session:
   * it was never started and has no tools. */
  running?: boolean
  /** True when a new session will leave the server out. */
  disabledNextSession?: boolean
  /** The tools this session's frozen table actually exposes, one entry per
   * mcp__<server>__<tool> attribution, each with its parameter table. */
  tools?: McpToolSummary[]
}

/** One row of a tool's parameter table; nested object properties are children. */
export interface McpToolParam {
  name: string
  type?: string
  description?: string
  required?: boolean
  enum?: string[]
  default?: string
  children?: McpToolParam[]
}

export interface McpToolSummary {
  name: string
  description?: string
  params?: McpToolParam[]
}

/**
 * The three layers an MCP entry can live in — the shared list every primary
 * agent inherits, one primary agent's own list, and a project's file. A higher
 * layer replaces a same-named entry whole.
 */
export type McpLayer = 'global' | 'agent' | 'project'

/** One entry's OAuth block as the read side answers it: a credential the
 * surface may not read comes back as `[REDACTED]`, and sending that placeholder
 * back means "leave it alone". */
export interface McpServerOAuthRecord {
  mode: string
  accessToken: string
  tokenType: string
  clientId: string
  clientSecret: string
  tokenUrl: string
  authorizationUrl: string
  redirectUrl: string
  refreshToken: string
  scopes: string[]
}

/**
 * One MCP entry as a list or a drawer shows it: the whole entry as a client
 * edits it (`readMCPServerWrite`'s fields, so a row can be sent straight back),
 * plus the layer it came from and, when the answering view knows them, what the
 * runtime is doing with it.
 *
 * The runtime half is absent rather than false when a view has no runtime to
 * ask: "we could not ask" and "nothing is running" are different facts.
 */
/** One entry of a project's MCP file that could not be applied, and why. */
export interface McpNotAppliedRow {
  name: string
  reason: string
}

export interface McpServerRow {
  name: string
  /** `stdio` or `streamable_http` — the configured spelling, not the short one
   * a table shows. */
  transport: string
  /** The command line for a stdio entry, the URL for an http one. */
  target: string
  required: boolean
  enabled: boolean
  startupTimeout: number
  requestTimeout: number
  /** Key names travel, values are `[REDACTED]` unless they are a single
   * `${ENV_NAME}` reference. */
  headers: Record<string, string>
  env: Record<string, string>
  inheritParentEnv: boolean
  disableStandaloneSse: boolean
  expectedCommandSha256: string
  defaultToolsApprovalMode: string
  oauth: McpServerOAuthRecord
  scope: McpLayer
  /** The config file the entry came from. */
  source?: string
  urlSet?: boolean
  /** An OAuth block this entry has; the exact rule is the runtime's
   * `oauthConfigured`, and this is its answer. */
  oauthConfigured?: boolean
  /** A stored credential overlay exists for this entry. */
  oauthOverlay?: boolean
  /** False for an entry that is configured but not in this session's list: a
   * higher layer shadows it, or the disable store kept it out. */
  running?: boolean
  /** `enabled: false` in the configuration: no session loads it until the file
   * changes, which no local switch can do. */
  disabledByConfig?: boolean
  /** The agent's disable store keeps it out of a new session. */
  disabledNextSession?: boolean
  authStatus?: string
  tools?: McpToolSummary[]
  officialUrl?: boolean
  connStatus?: McpConnStatus | (string & {})
  toolCount?: number
  generation?: string
  pending?: boolean
  error?: string
  /** Project view: the entry's confirmation was recorded. */
  allowed?: boolean
  /** Project view: this project declined the entry. It does not load, and the
   * row keeps a way back — allowing it records a new decision. */
  declined?: boolean
  /** Shared layer: the layer whose same-named entry overrides this one, so this
   * entry does not run. Only `agent` is reported today. */
  shadowedBy?: string
  /** The primary agent that overrides this entry, when `shadowedBy` is
   * `agent`. */
  shadowedByName?: string
  /** Project view: the one-line summary of an entry awaiting confirmation. */
  summary?: string
  /** Project view: the fingerprint of the entry as this row was rendered
   * from it. The per-row 允许 / 拒绝 sends it back so the server records the
   * decision against exactly the entry the operator was shown. */
  fingerprint?: string
}

/** What a create or an update answers: the entry as it now reads on disk. */
export interface McpServerWriteResult {
  ok: boolean
  /** The file that was written. */
  path: string
  scope: McpLayer
  applied: boolean
  removed: boolean
  server: McpServerRow
}

/** What one connection test found. A server that would not connect is a
 * result, not an error: `error` is its own text, verbatim. */
export interface McpTestResult {
  ok: boolean
  toolCount: number
  tools?: Array<{ name: string; description?: string }>
  error?: string
  elapsedMs: number
}

/** Disable (or enable) a server for the next session; the YAML is untouched. */
export async function mcpSetServerDisabled(name: string, disabled: boolean, sessionId?: string) {
  const payload = toSnakeCase({ name, sessionId: sessionId ?? '' })
  const path = disabled ? '/v1/mcp/servers/disable' : '/v1/mcp/servers/enable'
  return postJson<{ ok: boolean; reply: string }, Record<string, string>>(path, payload)
}

/**
 * One language server as the runtime reports it. `state` stays an open
 * vocabulary for the same reason MCP's connection status does: a state this
 * client does not know renders as itself, not as whichever state happens to
 * be the fallback.
 */
export interface LspServerStatus {
  id: string
  displayName?: string
  languages?: string[]
  role: string
  scope: string
  enabled: boolean
  state: string
  command?: string
  binaryPath?: string
  version?: string
  installCommand?: string
  roots?: string[]
  pids?: number[]
  openDocuments?: number
  errors?: number
  warnings?: number
  indexingPercent?: number
  lastError?: string
  logPath?: string
  note?: string
  projectWrites?: string[]
  installing?: boolean
  installLog?: string[]
  installError?: string
}

/** Everything /lsp shows for one runner: the project, the servers, the switches. */
export interface LspSnapshot {
  projectRoot?: string
  trusted: boolean
  featureEnabled: boolean
  toolRegistered: boolean
  recommendationsDisabled: boolean
  recommendationsDisabledReason?: string
  projectNotes?: string[]
  servers: LspServerStatus[]
}

/**
 * One page of the language-server table plus the snapshot's own fields: the
 * same answer the snapshot carries, with the servers cut to the page.
 */
export interface LspSnapshotPage extends ListPageMeta, Omit<LspSnapshot, 'servers'> {
  rows: LspServerStatus[]
}

/** The language-server snapshot the /lsp surfaces render. */
export async function lspSnapshot(sessionId?: string): Promise<LspSnapshot> {
  const params = sessionId ? { sessionId } : undefined
  const data = await api.get<Record<string, unknown>>('/v1/lsp', { params: toSnakeCase(params ?? {}) })
  const snap = toCamelCase(data.data) as unknown as LspSnapshot
  if (!Array.isArray(snap.servers)) snap.servers = []
  return snap
}

/**
 * One page of the settings page's language-server table: searched over the id
 * and the languages the table lists, counted on the server. The snapshot's own
 * fields — the project, the switches, the project-file notes — answer on top of
 * the page, so a caller that showed them still can.
 */
export async function lspPage(query: ListQuery): Promise<LspSnapshotPage> {
  const res = await api.get<Record<string, unknown>>('/v1/lsp', { params: listParams(query) })
  const snap = toCamelCase(res.data) as unknown as Omit<LspSnapshot, 'servers'> & ListPageMeta & {
    servers?: LspServerStatus[]
  }
  return {
    rows: Array.isArray(snap.servers) ? snap.servers : [],
    total: Number(snap.total ?? 0),
    page: Number(snap.page ?? 1),
    pageSize: Number(snap.pageSize ?? query.pageSize),
    projectRoot: snap.projectRoot,
    trusted: Boolean(snap.trusted),
    featureEnabled: Boolean(snap.featureEnabled),
    toolRegistered: Boolean(snap.toolRegistered),
    recommendationsDisabled: Boolean(snap.recommendationsDisabled),
    recommendationsDisabledReason: snap.recommendationsDisabledReason,
    projectNotes: Array.isArray(snap.projectNotes) ? snap.projectNotes : [],
  }
}

/** Enable or disable one server; its instances in this project follow. */
export async function lspSetEnabled(id: string, enabled: boolean, sessionId?: string) {
  const payload = toSnakeCase({ id, sessionId: sessionId ?? '' })
  const path = enabled ? '/v1/lsp/servers/enable' : '/v1/lsp/servers/disable'
  return postJson<{ ok: boolean }, Record<string, string>>(path, payload)
}

/** Restart one server's instances in this project. */
export async function lspRestart(id: string, sessionId?: string) {
  return postJson<{ ok: boolean }, Record<string, string>>('/v1/lsp/servers/restart', toSnakeCase({ id, sessionId: sessionId ?? '' }))
}

/**
 * Start the server's install recipe. The gateway answers 202 at once; the
 * command only runs after the user saw it whole and confirmed.
 */
export async function lspInstall(id: string, sessionId?: string) {
  return postJson<{ ok: boolean; started: boolean }, Record<string, string>>('/v1/lsp/servers/install', toSnakeCase({ id, sessionId: sessionId ?? '' }))
}

/** Turn language-server recommendations back on for this agent. */
export async function lspResetRecommendations(sessionId?: string) {
  return postJson<{ ok: boolean }, Record<string, string>>('/v1/lsp/recommendations/reset', toSnakeCase({ sessionId: sessionId ?? '' }))
}

/**
 * Apply the user's answer to one recommendation. The card's buttons and the
 * terminal modal's actions run the same decision.
 */
export async function decideLspRecommendation(id: string, choice: LspRecommendationChoice, sessionId?: string) {
  return postJson<{ ok: boolean }, Record<string, string>>(
    `/v1/lsp/recommendations/${encodeURIComponent(id)}/decision`,
    toSnakeCase({ choice, sessionId: sessionId ?? '' }),
  )
}

/** McpServersResponse is the envelope, including which Runner answered. */
export interface McpServersResponse {
  servers: McpServerRecord[]
  project: McpProjectStatus
  /** 'session' when the caller named a session, 'primary' otherwise. */
  runtimeScope?: string
  runtimeAvailable?: boolean
  generation?: string
  pending?: boolean
}

/** One outbound mcp_status_event: the current generation's servers. */
export interface McpStatusEvent {
  generation?: string
  pending?: boolean
  servers: Array<{
    name: string
    connStatus: McpConnStatus | (string & {})
    transport?: string
    error?: string
    toolCount?: number
    required?: boolean
    generation?: string
  }>
}

export interface McpProjectStatus {
  projectRoot?: string
  pendingReload?: boolean
  overriddenGlobal?: string[]
  notApplied?: Array<{ name: string; reason: string }>
}

export interface ProjectRecord {
  id: string
  name: string
  icon: string
  description: string
  instructions: string
  root: string
  projectKey: string
  memoryScope: string
  resourceAccess: boolean
  pinned: boolean
  /** The directory the "save to" dialog last uploaded into, relative to the
   * root ('' is the root). */
  lastAttachDir?: string
  createdAt: number
  updatedAt: number
  trustRecorded?: boolean
  /** The persisted trust decision for this project's root (the detail read carries it). */
  trusted?: boolean
  /** The directory's trust decision: 'trusted' | 'untrusted' | 'unknown'. */
  trustLevel?: TrustLevel
  /** True when the project root is no longer an existing directory. */
  missing?: boolean
  /** How many conversations this project holds; the project table's 对话 column. */
  sessionCount?: number
}

/** One directory's persisted trust decision, shared by the terminal and the web. */
export type TrustLevel = 'trusted' | 'untrusted' | 'unknown'

/** One subdirectory in a host-directory listing. */
export interface HostDirEntry {
  name: string
  mtime: number
  /** The primary agent's project when this directory is exactly a project root. */
  project?: { id: string; name: string } | null
  /** True for the forebrain home and anything inside it: not selectable. */
  blocked: boolean
}

/** A directory listing for the folder picker, plus its shortcuts and ancestry. */
export interface HostDirListing {
  path: string
  /** The parent directory; empty at the filesystem root. */
  parent: string
  /** The directory and each of its ancestors up to the filesystem root. */
  ancestors: string[]
  /** The picker's fixed sidebar: home, Desktop, Documents and Downloads. */
  places: Array<{ name: string; path: string }>
  entries: HostDirEntry[]
}

/** One subdirectory of a project directory, for the "save to" dialog. */
export interface ProjectDirEntry {
  name: string
  /** Whether drawing this directory would find anything: a chevron, or none. */
  hasChildren: boolean
}

/** A project directory listing: the relative directory and its subdirectories. */
export interface ProjectDirListing {
  /** The listed directory relative to the project root ('' is the root). */
  path: string
  entries: ProjectDirEntry[]
}

export interface ProjectSessionRecord {
  id: string
  title: string
  updatedAt: number
}

/**
 * One row of a project's conversation table (plan 075): the conversation plus
 * the two facts the table derives — the model it last ran with and how many
 * messages it holds.
 */
export interface ProjectSessionRow {
  id: string
  title: string
  createdAt: number
  updatedAt: number
  /** Empty when the session never pinned a model; the table draws "—". */
  model: string
  /** The visible user and assistant messages, counted on the server. */
  messageCount: number
  /** The conversation's heartbeat, absent when it has none. */
  heartbeat?: { intervalSeconds: number; paused: boolean }
}

/** One row of a project's rule-file table: the directory layer, whose
 * `FOREBRAIN.md` is the file ('' is the project root). */
export interface ProjectRuleFileRow {
  dir: string
  exists: boolean
  sizeBytes?: number
  updatedAt?: number
}

/** One row of a project's permission table: the rule the project's own
 * settings hold, in the shape the agent's permission page shows. */
export type ProjectPermissionRuleRow = PermissionRuleRecord

/** One row of a project's language-server table: pending, allowed and denied
 * entries in one list, since they are one table on the page. */
export interface ProjectLspRow {
  id: string
  /** What the entry would do; empty for an allowed or denied one, which the
   * server only knows by id. */
  summary: string
  status: 'pending' | 'allowed' | 'denied'
  /** The fingerprint of the entry as this row was rendered from it. The
   * per-row 允许 / 拒绝 sends it back so the server records the decision
   * against exactly the entry the operator was shown. */
  fingerprint: string
}

/** One row of a project's channel table. */
export interface ProjectChannelRow {
  kind: string
  labelZh: string
  labelEn: string
  enabled: boolean
  /** A configuration is saved for this kind — the form was filled in once. */
  configured: boolean
  /** The saved fields, keys in the channel's own snake_case spelling and
   * secrets masked with the placeholder. */
  fields: Record<string, unknown>
  /** Which of those keys carry a secret. */
  secretKeys: string[]
  /** `••••` plus the last four characters, per secret key that has a value. */
  secretHints: Record<string, string>
}

/** One row of a project's scheduled-task table: the job as the engine stores
 * it, with what became of its last fire. */
export type CronJobRow = CronJobRecord

/** One row of a scheduled task's run history. */
export type CronRunRow = CronRunRecord

export interface ProjectMcpRecord {
  servers: Array<{ name: string; transport: string; urlSet: boolean; scope: string }>
  overriddenGlobal: string[]
  notApplied: Array<{ name: string; reason: string }>
  pendingConsent: Array<{ name: string; summary: string }>
}

/** The project-level language-server view: what <root>/.forebrain/lsp_servers.yaml declares and its per-entry decisions. */
export interface ProjectLspRecord {
  trusted: boolean
  pending: Array<{ id: string; summary: string }>
  allowed: string[]
  denied: string[]
  notes: string[]
}



export interface PermissionExplainRule {
  source?: string
  behavior?: string
  toolName?: string
  ruleContent?: string
  matched?: boolean
  reason?: string
}

export interface PermissionExplainResponse {
  decision: PermissionDecision
  rules?: PermissionExplainRule[]
}

export interface CronJobRecord {
  id: string
  name?: string
  schedule: string
  prompt: string
  deliver?: string
  enabled: boolean
  repeatLimit?: number
  projectId?: string
  runCount?: number
  nextRunAt?: number
  lastRunAt?: number
  lastStatus?: string
  lastError?: string
  /** Stable classifier of the last error, for wording in the viewer's language. */
  lastErrorCode?: string
  lastOutput?: string
  failureStreak?: number
  createdAt?: number
  updatedAt?: number
}

export interface CronRunRecord {
  id: number
  jobId: string
  sessionId?: string
  /** The fire's session holds a transcript; fires recorded before fires became conversations have none. */
  hasConversation?: boolean
  trigger?: string
  status: string
  output?: string
  error?: string
  /** Stable classifier of the error, for wording in the viewer's language. */
  errorCode?: string
  deliveredTo?: string
  startedAt: number
  finishedAt?: number
}

export interface CronJobBody {
  name?: string
  schedule?: string
  prompt?: string
  deliver?: string
  enabled?: boolean
  repeat_limit?: number
  projectId?: string
}

export interface CronPreviewResponse {
  raw: string
  valid: boolean
  kind?: string
  next?: string[]
  error?: string
}

/**
 * The install's scheduled-task settings. RetentionDays is the value in force
 * (the default when the file carries no key), and minDays/maxDays bound what
 * a save may carry — the form checks them before any request.
 */
export interface CronSettings {
  retentionDays: number
  configured: boolean
  defaultDays: number
  minDays: number
  maxDays: number
}

export interface HeartbeatRecord {
  sessionId: string
  agentId?: string
  intervalSeconds: number
  prompt: string
  paused: boolean
  lastFiredAt?: number
  nextRunAt?: number
  /** The conversation's title, on a listed heartbeat; empty when unnamed. */
  sessionTitle?: string
}

export interface HookCommandRecord {
  type?: string
  command?: string
  prompt?: string
  url?: string
  if?: string
  timeout?: number
  shell?: string
}

export interface HookMatcherRecord {
  matcher?: string
  hooks?: HookCommandRecord[]
}

export type HooksSettingsRecord = Record<string, HookMatcherRecord[]>

/**
 * One row of the hooks table (plan 076): one command under one matcher of one
 * event. The two indices locate it inside HooksSettings, which is what an edit
 * or a delete addresses — both write the whole table back.
 */
export interface HookRowRecord {
  event: string
  matcher: string
  /** The hook's command, or its prompt or URL, whichever its kind carries. */
  command: string
  matcherIdx: number
  commandIdx: number
}

/** What the gateway knows about itself for the settings page's runtime tab. */
export interface RuntimeInfo {
  status: string
  /** The control plane's auth mode; absent when no configuration is bound. */
  auth?: string
  /** How many primary agents are configured. */
  agents: number
  home: string
  logsDir: string
  stateDir: string
}

/** One model-service row as the web surface edits it. The key never travels
 * back: apiKeySet says whether one is stored and apiKeyHint is its masked
 * tail; a save carries a key only when the user typed one. */
export interface ProviderRecord {
  provider: string
  models: string[]
  baseUrl?: string
  apiPath?: string
  /** The provider request params as JSON text, keys verbatim. */
  params?: string
  apiKeySet?: boolean
  apiKeyHint?: string
  /**
   * The row's position in the whole fallback order (0 is the primary). A paged
   * table holds one page, so "move up" acts on this and never on the page.
   */
  index?: number
}

/** The PUT shape: apiKeyPlain is a freshly typed key (stored as an ${ENV}
 * reference server-side); apiKey carries an explicit reference for advanced
 * use; both empty means "keep the stored key". */
export interface ProviderSaveRecord extends ProviderRecord {
  model?: string
  apiKey?: string
  apiKeyPlain?: string
}

export interface PermissionUpdateBody {
  type: 'addRules' | 'replaceRules' | 'removeRules' | 'setMode'
  destination: string
  behavior?: string
  rules?: PermissionRuleValue[]
  mode?: string
}

export interface ActionRecord {
  id: string
  kind: string
  status: string
  payloadJson: string
  answerJson?: string
  createdAt: number
  updatedAt: number
  permissionSuggestion?: import('./approvalSuggestions').PermissionSuggestionRecord | null
  // Set when a subagent raised the approval rather than the primary agent.
  // Several fanout children can be waiting at once, so the card says which one.
  agentId?: string
  subagentType?: string
  sessionId?: string
}

export interface RequestPermissionsResponse {
  permissions: Record<string, unknown>
  scope: 'turn' | 'session'
  strictAutoReview?: boolean
}

export interface ActionApprovalBody {
  reason?: string
  update?: PermissionUpdateBody
  clearContext?: boolean
  decision?: import('./approvalSuggestions').ApprovalDecisionName
  execpolicyAmendment?: string[]
  networkPolicyAmendment?: import('./approvalSuggestions').NetworkPolicyAmendment
  requestPermissionsResponse?: RequestPermissionsResponse
}

/** One model a pending exit-plan approval may be handed to for a review. */
export interface PlanReviewModelOption {
  provider: string
  model: string
  label?: string
  current?: boolean
}

/** One completed review a pending exit-plan approval already collected. */
export interface PlanReviewNote {
  provider: string
  model: string
  text: string
  durationMs?: number
}

/** The review a pending exit-plan approval is waiting on, from its events. */
export interface PlanReviewInFlight {
  reviewId: string
  provider?: string
  model: string
  label?: string
  /** The reviewer subagent's roster key; cancelling it stops the review. */
  agentId?: string
}

/**
 * The typed approval request a session is parked on, as the terminal's
 * overlay sees it. For a parked exit-plan approval it carries the plan
 * itself, the models a review may be handed to, the reviews already
 * collected, and the review currently running.
 */
export interface SessionApprovalRequest {
  actionId: string
  kind: string
  toolName?: string
  planText?: string
  planReviewModels?: PlanReviewModelOption[]
  planReviews?: PlanReviewNote[]
  planReviewActive?: PlanReviewInFlight | null
}

export interface ChatSessionRecord {
  id: string
  title: string | null
  createTime: string
  updateTime: string
  source?: string
  /** The project the conversation belongs to; '' on a project-less
   *  workshop task. The drawer's rows carry it. */
  projectName?: string
}

export interface ChatSessionsResponse {
  records: ChatSessionRecord[]
  /** The pager fields a paged drawer request answers with; the workshop
   *  list's legacy answer carries none of them. */
  total?: number
  page?: number
  pageSize?: number
}

/** One conversation's own facts: its title, the project it belongs to when it
 *  belongs to one, and — through that project — whether the directory the
 *  conversation runs in still exists. */
export interface ChatSessionInfo {
  id: string
  title: string
  project: { id: string; name: string; root?: string; missing?: boolean; lastAttachDir?: string } | null
}

export interface SessionTodosResponse {
  items: { id: string; content: string; status: string; updatedAt: number }[]
}

export interface SessionModeResponse {
  mode: string
  phase?: string
  updatedAt?: number
}

/** One model the conversation may be switched to, with the reasoning levels
 *  the shared catalog reports for it (empty when the model cannot reason). */
export interface SessionModelOption {
  provider: string
  model: string
  label: string
  efforts: string[]
  /** True when the catalog knows this model takes image input; an unknown
   *  model reports false, so a picker marks only what it knows. */
  supportsImages?: boolean
}

/** The conversation's effective model choice. An empty effort is the explicit
 *  "model default" pin. */
export interface SessionModelSelection {
  provider: string
  model: string
  effort: string
}

export interface SessionModelResponse extends SessionModelSelection {
  options: SessionModelOption[]
}

export interface SlashCommandsResponse {
  surface: string
  query: string
  options: { duringRun: boolean; sideConversation: boolean }
  records: SlashCommandRecord[]
}

export interface MentionCandidate {
  path: string
  isDir: boolean
}

export interface MentionSearchResponse {
  query: string
  records: MentionCandidate[]
}

/**
 * Result of accepting an @ mention. The server owns these semantics so the web
 * composer and the terminal composer behave identically: a picked file becomes
 * a bare path, a picked image is attached instead of named, and a picked
 * directory keeps the token open for drill-down.
 */
export interface MentionAcceptResponse {
  draft: string
  /** Cursor position after the replacement, as a rune offset. */
  cursor: number
  /** Workspace-relative path to attach, set only for image selections. */
  imagePath?: string
  keepOpen: boolean
}

export interface ToolAuditRow {
  /** The id of the tool-call event this row is projected from. */
  id: string
  runId: string
  sessionId: string
  toolName: string
  detailJson: string
  createdAt: number
}


/** What a session spent, in the figures /status reports. */
export interface SessionCostSummary {
  sessionId: string
  /** Everything the requests sent: uncached input plus cache reads and writes. */
  inputTokens: number
  outputTokens: number
  cacheRead: number
  cacheWritten: number
  uncached: number
  requests: number
  cacheHitPercent: number
  toolCalls: number
  byTool: Record<string, number>
}

export interface SessionSubagentHistoryResponse {
  sessionId: string
  records: SubagentHistoryRecord[]
}

export interface RunSubagentsResponse {
  runId: string
  runs: Record<string, unknown>[]
  records: SubagentHistoryRecord[]
}

export interface RewindFile {
  path: string
  added: number
  removed: number
  changedAfter: boolean
  created: boolean
}

export interface RewindPreview {
  anchorMessageId: number
  anchorText: string
  files: RewindFile[]
}

export interface RewindResult {
  anchorText: string
  mode: 'both' | 'chat' | 'code'
  restored: string[]
}

/** One file the conversation changed with a file tool (workbench 017 reads it). */
export interface RewindChangedFile {
  path: string
  added: number
  removed: number
  created: boolean
}

export interface SessionMessageSearchRecord {
  sessionId: string
  role: string
  content: string
  createdAt: number
}

export interface ActionAnswerBody {
  answers: { questionId: string; optionIds?: string[]; otherText?: string }[]
}

export interface WorkspaceTreeNode {
  name: string
  path: string
  isDir: boolean
}

export interface WorkspaceTreeResponse {
  path: string
  records: WorkspaceTreeNode[]
}

/**
 * The conversation a file-view request belongs to: a session the workbench has
 * open, or — for a conversation not started yet — the project its composer's
 * pill has chosen. The tree, snippet and @ picker all resolve their root from
 * it, so one of the two is always named.
 */
export interface WorkspaceScope {
  sessionId?: string
  projectId?: string
}

/** A scope as the wire's query params; a file view names at most one of them. */
function workspaceScopeParams(scope?: WorkspaceScope): Record<string, string> {
  const params: Record<string, string> = {}
  if (scope?.sessionId) params.session_id = scope.sessionId
  if (scope?.projectId) params.project_id = scope.projectId
  return params
}

export interface FileUploadResult {
  fileId: string
}

/** What the gateway recorded about an uploaded file. */
export interface FileInfo {
  id: string
  originalName: string
  mediaType: string
}

export type SkillOrigin = 'project' | 'agent' | 'shared' | 'builtin' | 'cross-tool'

export interface SkillRecord {
  name: string
  description: string
  allowedTools?: string
  rootPath?: string
  enabled: boolean
  /** Which layer owns this row: project, agent, shared, builtin, cross-tool. */
  origin?: SkillOrigin
  /** True when the row belongs to the layer the listing page manages. */
  editable?: boolean
  downloadUrl?: string
  /** Directories offering the same name that this row takes precedence over. */
  shadows?: string[]
  /** The current published version, 0 when the row carries no version history. */
  version?: number
  /** draft | published | pending, or '' for an unversioned skill. */
  state?: string
  /** Upstream has moved since the current version was recorded. */
  update?: SkillUpdateInfo
  /** A row that exists only as a draft: nothing loads until it is published. */
  draftOnly?: boolean
}

/** One published version's metadata, as the manifest records it. */
export interface SkillVersionRecord {
  v: number
  at: string
  kind: string
  source?: string
  upstreamRev?: string
  note?: string
  stat?: { files: number; added: number; removed: number }
}

/** The upstream marker on a shared skill that has an update to pull. */
export interface SkillUpdateInfo {
  rev: string
  date?: string
  files?: number
}

/** One skill's draft/version state. */
export interface SkillVersionState {
  name: string
  current: number
  versions: SkillVersionRecord[]
  hasDraft: boolean
  draftBase?: number
  draftDir?: string
  update?: SkillUpdateInfo
}

/** The version state a skill's lifecycle route answers with. */
export interface SkillVersionResponse {
  name: string
  origin?: SkillOrigin
  rootPath?: string
  state: SkillVersionState
}

/** One file's line-level diff between two snapshots. */
export interface SkillFileDiff {
  path: string
  status: 'added' | 'removed' | 'modified'
  added: number
  removed: number
}

export interface SkillDiffResponse {
  name: string
  from: string
  to: string
  files: SkillFileDiff[]
}

/** One evaluation configuration's grading for one case. */
export interface WorkshopEvalConfig {
  present: boolean
  passed: number
  total: number
  passRate: number
  output: string
  timeSeconds: number
}

export interface WorkshopEvalCase {
  id: string
  name: string
  prompt: string
  expected: string
  runId: string
  feedback: string
  configs: Record<string, WorkshopEvalConfig>
}

export interface WorkshopEvalIteration {
  name: string
  order: number
  modified: number
  cases: WorkshopEvalCase[]
}

export interface WorkshopEvalsResponse {
  name: string
  workspace: string
  iterations: WorkshopEvalIteration[]
}

export interface WorkshopTriggerQuery {
  query: string
  shouldTrigger: boolean
}

export interface WorkshopTriggerResultRow {
  query: string
  shouldTrigger: boolean
  pass: boolean
  triggerRate?: number
}

export interface WorkshopTriggerResults {
  currentDescription?: string
  currentScore: number
  bestDescription?: string
  bestScore: number
  queries: WorkshopTriggerResultRow[]
}

export interface WorkshopTriggerResponse {
  name: string
  queries: WorkshopTriggerQuery[]
  results: WorkshopTriggerResults | null
}

/** One candidate card the discovery flow renders from the model's reply. */
export interface SkillCandidate {
  name: string
  description: string
  source: string
  installed: boolean
}

export interface SkillInspectResponse {
  skill: {
    name: string
    description: string
    origin: SkillOrigin
    path: string
    allowedTools?: string
  }
  content: string
}

export interface SkillsOverviewResponse {
  installed: SkillRecord[]
  actions: string[]
}

export interface SkillsToggleResponse {
  status: string
  skills: SkillRecord[]
}

export interface SkillInstallResult {
  name: string
  destScope: string
  source: string
  sourceRef: string
  skill?: string
  skillPath: string
  metadata?: SkillRecord
  installed?: Array<{
    name: string
    skillPath: string
    metadata?: SkillRecord
  }>
  count?: number
}

export interface SkillInstallResponse {
  status: string
  taskId: string
  sessionId: string
  installed: SkillInstallResult
}

/** One instruction file as the rules editor reads it. */
export interface RuleFileContent {
  name?: string
  dir?: string
  exists: boolean
  content: string
}

export interface RuleFileSaveResponse {
  bytes: number
  warning?: string
}

/** One row of the rule-file table: the file's name and what is on disk. */
export interface RuleFileSummary {
  name: string
  exists: boolean
  sizeBytes?: number
  updatedAt?: number
}

export interface SkillUploadResponse {
  status: string
  installed: string[]
  skills: SkillRecord[]
}

/** A downloaded archive: the bytes plus the file name the server attached. */
export interface SkillDownloadBlob {
  blob: Blob
  filename: string
}

/** One row of the memory file listing. */
export interface MemoryFileRecord {
  path: string
  sizeBytes: number
  createdAt: number
  updatedAt: number
  core: boolean
  /** The first non-empty line, cut by the server to one table cell. */
  preview?: string
}

export interface MemoryFilesResponse {
  total: number
  page: number
  pageSize: number
  files: MemoryFileRecord[]
}

export interface MemoryFileContent {
  path: string
  content: string
  core: boolean
}

export interface SkillFileRecord {
  path: string
  sizeBytes: number
  modTime: number
  isDir: boolean
}

export interface SkillFilesResponse {
  name: string
  readOnly?: boolean
  files: SkillFileRecord[]
}

export interface SkillFileContent {
  name: string
  path: string
  content: string
  readOnly?: boolean
}

export interface MemoryFilesDeleteResponse {
  ok: boolean
  deleted: number
  results: Array<{ path: string; ok: boolean; error?: string }>
}

export interface SkillMutationResponse {
  status: string
  skill: {
    name: string
    description: string
    source: string
    trust: string
    path: string
    allowedTools?: string
  }
}

export type PrimaryAgentRecord = {
  id: string
  name?: string
  description?: string
  agentDefinition?: string
  workspaceRoot: string
  privateSkillsRoot: string
  sharedSkillsRoots: string[]
  active: boolean
  status?: string
}

export type PrimaryAgentsResponse = {
  activeId: string
  records: PrimaryAgentRecord[]
}

export type AgentRosterRow = {
  id: string
  kind: 'primary' | 'subagent'
  parentId?: string
  label: string
  status: string
  /** Short name of what the agent was dispatched to do; shown on the row. */
  title?: string
  /** The full dispatch prompt, kept for the agent's own view. */
  task?: string
  sessionId?: string
  /** The conversation a subagent ran in — its parent. A primary agent row has
   * none: its own session is the conversation. The client's camel-case
   * interceptor turns the wire's `parent_session_id` into this. */
  parentSessionId?: string
  runId?: string
  /** The title of the conversation this row ran in — a subagent's parent, a
   * primary agent's own session; '' when there is none. */
  sessionTitle?: string
  elapsedSeconds?: number
  tokenCount?: number
  toolCount?: number
  fileCount?: number
}

export type AgentRosterResponse = {
  records: AgentRosterRow[]
}

/** What kind of subagent a row is: a built-in the model may dispatch, one this
 *  primary agent defines itself, or a built-in the system dispatches at its own
 *  occasions and nobody configures. */
export type SubagentKind = 'builtin' | 'custom' | 'system'

/** A subagent's tool tier: reading only, everything an edit may do, the tools
 *  it picks one by one, or none at all. */
export type SubagentTools = 'read_only' | 'edit' | 'pick' | 'none'

/** One row of the subagent page: a subagent as this primary agent configures
 *  it, which is a configuration fact and not a runtime one. */
export interface SubagentConfigRow {
  name: string
  kind: SubagentKind
  description: string
  /** The model the subagent runs on; both empty when it follows the
   *  conversation's model. */
  modelProvider: string
  model: string
  reasoningEffort: string
  /** Where the model above came from: this agent's own subagent configuration,
   *  the legacy per-type `agents.definitions.<type>.llm_providers` entry, or —
   *  empty — the conversation's model. */
  modelSource: 'agent' | 'global' | ''
  tools: SubagentTools
  allowedTools: string[]
  continuable: boolean
  /** This row's own switch; null for a system subagent, which has none. */
  enabled: boolean | null
  /** A built-in whose system prompt this primary agent rewrote. */
  promptOverridden: boolean
}

/** One row as the drawer opens it: the row plus the prompt that will actually
 *  be injected, and — for a built-in or a system subagent — the harness's own
 *  text it restores to. */
export interface SubagentConfigDetail extends SubagentConfigRow {
  systemPrompt: string
  defaultPrompt?: string
}

/** One page of the subagent page's table, with the two switches the toolbar
 *  shows beside it: this agent's own, and the global default it follows until
 *  the agent sets one. */
export interface SubagentConfigPage {
  rows: SubagentConfigRow[]
  total: number
  page: number
  pageSize: number
  switch: boolean
  global: boolean
}

/**
 * What a subagent write carries, in the page's own spelling. A custom subagent
 * is written whole; a built-in takes only the model, the effort, the prompt and
 * the switch, and `resetPrompt` is its "restore the default" — it clears the
 * rewrite and leaves the pinned model alone.
 */
export interface SubagentConfigBody {
  name?: string
  description?: string
  model?: string
  reasoningEffort?: string
  systemPrompt?: string
  tools?: SubagentTools
  allowedTools?: string[]
  continuable?: boolean
  enabled?: boolean
  resetPrompt?: boolean
}

/** The route one primary agent's subagents live on; `tail` names the one entry
 *  a write addresses, empty for the list itself. */
function subagentScopedPath(agentId: string, tail: string): string {
  const base = `/agents/primary/${encodeURIComponent(agentId)}/subagents`
  return tail ? `${base}/${encodeURIComponent(tail)}` : base
}

const api = axios.create({
  baseURL: API_BASE,
  timeout: 600000,
  headers: { 'Content-Type': 'application/json' },
})

export const __apiClient = api

// The browser's gateway session is a cookie, so axios requests carry their
// credentials without any per-request work. A 401 means the session is gone:
// report it so the router can offer the sign-in page again.
api.interceptors.response.use(
  (response) => {
    response.data = toCamelCase(response.data)
    return response
  },
  (error) => {
    if ((error as AxiosError)?.response?.status === 401) reportGatewayUnauthorized()
    return Promise.reject(error)
  },
)

/**
 * Native fetch with the session handling axios has: same-origin requests
 * carry the session cookie automatically, and a 401 is reported to the
 * router. Used where axios does not fit (streams, uploads).
 */
async function gatewayFetch(input: string, init?: RequestInit): Promise<Response> {
  const res = await fetch(input, init)
  if (res.status === 401) reportGatewayUnauthorized()
  return res
}

/**
 * The words a failed fetch answered with: the gateway's {"error": …} reply
 * (with the names a batch could not find, when it lists them), or the plain
 * text http.Error writes. Never the raw JSON body.
 */
async function gatewayErrorText(res: Response): Promise<string> {
  const body = await res.text()
  try {
    const data: unknown = JSON.parse(body)
    if (data !== null && typeof data === 'object' && 'error' in data) {
      const { error, missing } = data as { error?: unknown; missing?: unknown }
      const names = Array.isArray(missing) ? missing.map(String).filter(Boolean) : []
      const message = String(error ?? '').trim() || `HTTP ${res.status}`
      return names.length ? `${message}: ${names.join(', ')}` : message
    }
  } catch {
    // Not JSON: the plain-text reply is already the message.
  }
  return body.trim() || `HTTP ${res.status}`
}

/** The file name an attachment header carried, unquoted; empty when absent. */
function filenameFromContentDisposition(header: string | null): string {
  if (!header) return ''
  const match = header.match(/filename\*?=(?:UTF-8'')?"?([^";]+)"?/i)
  return match ? decodeURIComponent(match[1]) : ''
}

/** Hand a downloaded archive to the browser's own save flow. */
export function saveSkillDownload(download: SkillDownloadBlob) {
  const url = URL.createObjectURL(download.blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = download.filename
  document.body.append(anchor)
  anchor.click()
  anchor.remove()
  URL.revokeObjectURL(url)
}

function postJson<TResponse, TBody>(url: string, body: TBody) {
  return api.post<TResponse>(url, toSnakeCase(body)).then((res) => res.data)
}

// The body is already in the shape the server expects, so it is sent as-is.
function putRaw<TResponse>(url: string, body: unknown) {
  return api.put<TResponse>(url, body).then((res) => res.data)
}

// Sent as-is for the same reason putRaw is: an MCP entry's env and header maps
// carry the operator's own key spellings, and a key rewrite (X-Api-Key →
// x_api_key) would rename a request header.
function postRaw<TResponse>(url: string, body: unknown) {
  return api.post<TResponse>(url, body).then((res) => res.data)
}

/**
 * A GET whose JSON keeps the gateway's own spelling all the way down. The
 * shared interceptor camelCases every key, which is right for a field name and
 * wrong for user data: an MCP entry's `env` and `headers` maps are keyed by the
 * user's own variable names, and renaming `CODEGRAPH_HOME` to `codegraphHome`
 * would send a bogus key back on the next save.
 */
async function getRawJSON<T>(url: string, params: URLSearchParams): Promise<T> {
  const query = params.toString()
  const res = await gatewayFetch(`${API_BASE}${url}${query ? `?${query}` : ''}`)
  if (!res.ok) throw new GatewayHttpError(res.status, await gatewayErrorText(res))
  return (await res.json()) as T
}

/** One tool read through `getRawJSON`. The tool table reads raw because its
 * `input_schema` is not a field name but a document: a parameter called
 * `file_path` has to stay `file_path`, or the required list stops matching the
 * properties and every underscore-named parameter reads as optional. */
function toolMetaFromWire(wire: Record<string, unknown>): ToolMetaRecord {
  const row: ToolMetaRecord = {
    name: String(wire.name ?? ''),
    description: String(wire.description ?? ''),
    category: String(wire.category ?? ''),
    readOnly: wire.read_only === true,
    destructive: wire.destructive === true,
    concurrencySafe: wire.concurrency_safe === true,
    interruptBehavior: String(wire.interrupt_behavior ?? ''),
    alwaysLoad: wire.always_load === true,
  }
  if (wire.input_schema !== undefined) row.inputSchema = wire.input_schema
  if (wire.mcp_server !== undefined) row.mcpServer = String(wire.mcp_server)
  return row
}

/** One MCP row read through `getRawJSON`: the wire's snake_case field names,
 * translated here and only here, with the user-keyed maps (`env`, `headers`)
 * passed through untouched. */
function mcpRowFromWire(wire: Record<string, unknown>): McpServerRow {
  const text = (key: string): string => String(wire[key] ?? '')
  const flag = (key: string): boolean => wire[key] === true
  const record = (key: string): Record<string, string> => {
    const value = wire[key]
    if (!value || typeof value !== 'object') return {}
    return Object.fromEntries(Object.entries(value as Record<string, unknown>).map(([name, entry]) => [name, String(entry ?? '')]))
  }
  const oauth = (wire.oauth ?? {}) as Record<string, unknown>
  const row: McpServerRow = {
    name: text('name'),
    transport: text('transport'),
    target: text('target'),
    required: flag('required'),
    enabled: flag('enabled'),
    startupTimeout: Number(wire.startup_timeout ?? 0),
    requestTimeout: Number(wire.request_timeout ?? 0),
    headers: record('headers'),
    env: record('env'),
    inheritParentEnv: flag('inherit_parent_env'),
    disableStandaloneSse: flag('disable_standalone_sse'),
    expectedCommandSha256: text('expected_command_sha256'),
    defaultToolsApprovalMode: text('default_tools_approval_mode'),
    oauth: {
      mode: String(oauth.mode ?? ''),
      accessToken: String(oauth.access_token ?? ''),
      tokenType: String(oauth.token_type ?? ''),
      clientId: String(oauth.client_id ?? ''),
      clientSecret: String(oauth.client_secret ?? ''),
      tokenUrl: String(oauth.token_url ?? ''),
      authorizationUrl: String(oauth.authorization_url ?? ''),
      redirectUrl: String(oauth.redirect_url ?? ''),
      refreshToken: String(oauth.refresh_token ?? ''),
      scopes: Array.isArray(oauth.scopes) ? oauth.scopes.map(String) : [],
    },
    scope: text('scope') as McpLayer,
  }
  // The runtime half exists only on a view that could ask a runner; a field
  // the answer never carried stays absent rather than false.
  if ('source' in wire) row.source = text('source')
  if ('url_set' in wire) row.urlSet = flag('url_set')
  if ('oauth_configured' in wire) row.oauthConfigured = flag('oauth_configured')
  if ('oauth_overlay' in wire) row.oauthOverlay = flag('oauth_overlay')
  if ('running' in wire) row.running = flag('running')
  if ('disabled_by_config' in wire) row.disabledByConfig = flag('disabled_by_config')
  if ('disabled_next_session' in wire) row.disabledNextSession = flag('disabled_next_session')
  if ('auth_status' in wire) row.authStatus = text('auth_status')
  if (Array.isArray(wire.tools)) row.tools = wire.tools as McpToolSummary[]
  if ('official_url' in wire) row.officialUrl = flag('official_url')
  if ('conn_status' in wire) row.connStatus = text('conn_status')
  if ('tool_count' in wire) row.toolCount = Number(wire.tool_count ?? 0)
  if ('generation' in wire) row.generation = text('generation')
  if ('pending' in wire) row.pending = flag('pending')
  if ('error' in wire) row.error = text('error')
  if ('allowed' in wire) row.allowed = flag('allowed')
  if ('declined' in wire) row.declined = flag('declined')
  if ('shadowed_by' in wire) row.shadowedBy = text('shadowed_by')
  if ('shadowed_by_name' in wire) row.shadowedByName = text('shadowed_by_name')
  if ('summary' in wire) row.summary = text('summary')
  if ('fingerprint' in wire) row.fingerprint = text('fingerprint')
  return row
}

/** One channel row read through `getRawJSON`: the wire's snake_case field
 * names, translated here and only here, with the `fields` map passed through
 * untouched because its keys are the channel's own (`app_id`, `app_secret`). */
function projectChannelRowFromWire(wire: Record<string, unknown>): ProjectChannelRow {
  const fields = (value: unknown): Record<string, unknown> =>
    value && typeof value === 'object' && !Array.isArray(value) ? { ...(value as Record<string, unknown>) } : {}
  const hints: Record<string, string> = {}
  for (const [key, value] of Object.entries(fields(wire.secret_hints))) {
    hints[key] = String(value ?? '')
  }
  return {
    kind: String(wire.kind ?? ''),
    labelZh: String(wire.label_zh ?? ''),
    labelEn: String(wire.label_en ?? ''),
    enabled: wire.enabled === true,
    configured: wire.configured === true,
    fields: fields(wire.fields),
    secretKeys: Array.isArray(wire.secret_keys) ? wire.secret_keys.map(String) : [],
    secretHints: hints,
  }
}

/**
 * The route set one MCP layer's writes land on: the shared layer's routes carry
 * no id, a primary agent's carry its id, a project's carry the project's.
 */
function mcpScopedPath(scope: McpLayer, id: string, tail: string): string {
  if (scope === 'agent') return `/agents/primary/${encodeURIComponent(id)}/mcp/${tail}`
  if (scope === 'project') return `/v1/projects/${encodeURIComponent(id)}/mcp/${tail}`
  return `/v1/mcp/${tail}`
}

export const forebrainApi = {
  primaryAgents() {
    return api.get<PrimaryAgentsResponse>('/agents/primary').then((res) => res.data)
  },

  primaryAgentSwitch(agentId: string) {
    return postJson<PrimaryAgentsResponse, { agentId: string }>('/agents/primary/switch', { agentId })
  },

  createPrimaryAgent(body: { id: string; name?: string; description?: string; workspaceRoot?: string }) {
    return postJson<PrimaryAgentsResponse, { id: string; name?: string; description?: string; workspaceRoot?: string }>('/agents/primary', body)
  },

  deletePrimaryAgent(id: string) {
    return api.delete<PrimaryAgentsResponse>(`/agents/primary/${encodeURIComponent(id)}`).then((res) => res.data)
  },

  agentRoster() {
    return api.get<AgentRosterResponse>('/agents/roster').then((res) => res.data)
  },

  /** One page of the subagent table: filtered (`running`), searched and
   * ordered longest-running first on the server. Each row names the session it
   * runs in. */
  agentRosterPage(query: ListQuery) {
    return api
      .get<{ records?: AgentRosterRow[] } & ListPageMeta>('/agents/roster', { params: listParams(query) })
      .then((res) => ({
        rows: Array.isArray(res.data?.records) ? res.data.records : [],
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
      }))
  },

  primaryAgentCancel(agentId: string) {
    return postJson<{ ok: boolean; cancelled: boolean }, Record<string, never>>(
      `/agents/${encodeURIComponent(agentId)}/cancel`,
      {},
    )
  },

  subagentCancel(id: string) {
    return postJson<{ ok: boolean; cancelled: boolean }, Record<string, never>>(
      `/subagents/${encodeURIComponent(id)}/cancel`,
      {},
    )
  },

  agentsCancelAll() {
    return postJson<{ ok: boolean; main: number; subagents: number }, Record<string, never>>(
      '/agents/cancel-all',
      {},
    )
  },

  /** One page of one primary agent's subagents: its custom ones first, then the
   * built-ins the model may dispatch, then the ones the system dispatches. The
   * search, the `kind` filter, the order and the slice are the server's, and the
   * answer carries the agent's own subagent switch beside the global one. */
  subagentConfigPage(
    agentId: string,
    query: ListQuery & { kind?: SubagentKind | '' },
  ): Promise<SubagentConfigPage> {
    return api
      .get<{ rows?: SubagentConfigRow[]; switch?: boolean; global?: boolean } & ListPageMeta>(
        subagentScopedPath(agentId, ''),
        { params: listParams(query) },
      )
      .then((res) => ({
        rows: Array.isArray(res.data?.rows) ? res.data.rows : [],
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
        switch: res.data?.switch === true,
        global: res.data?.global === true,
      }))
  },

  /** One subagent with the prompt it will actually run on. */
  subagentConfig(agentId: string, name: string) {
    return api
      .get<SubagentConfigDetail>(subagentScopedPath(agentId, name))
      .then((res) => res.data)
  },

  /** Create a subagent of this primary agent's own; it goes to the front of the
   * list. The answer names what was written — the page reloads to show it. */
  createSubagentConfig(agentId: string, body: SubagentConfigBody) {
    return postRaw<{ name: string }>(subagentScopedPath(agentId, ''), toSnakeCase(body))
  },

  /** Write one subagent: a custom one whole, or the settings a built-in takes.
   * The name is the entry's identity and comes from the route, so an update
   * never renames. The body is in the file's own spelling — `system_prompt`,
   * `reasoning_effort`, `reset_prompt` — and its values, a prompt or a tool
   * name among them, are never rewritten. */
  updateSubagentConfig(agentId: string, name: string, body: SubagentConfigBody) {
    return putRaw<{ name: string }>(subagentScopedPath(agentId, name), toSnakeCase(body))
  },

  deleteSubagentConfig(agentId: string, name: string) {
    return api
      .delete<{ name: string }>(subagentScopedPath(agentId, name))
      .then((res) => res.data)
  },

  /** One row's switch. The answer is the agent's switch after the write: a row
   * switched on turns the feature on here, and the last row switched off turns
   * it off, so the toolbar reads the server's answer rather than predicting it. */
  setSubagentEnabled(agentId: string, name: string, enabled: boolean) {
    return putRaw<{ switch: boolean }>(`${subagentScopedPath(agentId, name)}/enabled`, { enabled })
  },

  /** This primary agent's own subagent switch, and every row with it. */
  setAgentSubagentSwitch(agentId: string, enabled: boolean) {
    return putRaw<{ switch: boolean }>(`/agents/primary/${encodeURIComponent(agentId)}/subagent-switch`, { enabled })
  },

  /** The global subagent switch (agents.defaults.enable_subagent), which every
   * primary agent follows until it sets its own. */
  subagentDefaults() {
    return api
      .get<{ enabled?: boolean }>('/agents/defaults/subagents')
      .then((res) => ({ enabled: res.data?.enabled === true }))
  },

  setSubagentDefaults(enabled: boolean) {
    return putRaw<{ enabled: boolean }>('/agents/defaults/subagents', { enabled })
  },

  /** The models this agent may pin a subagent to: the same list the
   * conversation's own model menu offers. */
  subagentModelOptions(agentId: string) {
    return api
      .get<{ options?: SessionModelOption[] }>(`/agents/primary/${encodeURIComponent(agentId)}/model-options`)
      .then((res) => (Array.isArray(res.data?.options) ? res.data.options : []))
  },

  /** The agent's conversations of one purpose: '' the drawer lists — every
   *  conversation across every project, searched and paged on the server —
   * 'workshop' the skill workshop lists whole, the way it always has. The
   * filter is the server's. Omit page for the unpaged window. */
  chatSessions(source: '' | 'workshop' = '', opts?: { page?: number; pageSize?: number; q?: string }) {
    const params = listParams({
      page: opts?.page ?? 0,
      pageSize: opts?.pageSize ?? 0,
      q: opts?.q,
      source,
    })
    return api.get<ChatSessionsResponse>('/chat/sessions', { params }).then((res) => res.data)
  },

  chatMessages(sessionId: string, limit = 0) {
    return api
      .get<ChatMessageRecord[]>(`/chat/sessions/${sessionId}/messages`, {
        params: { limit },
      })
      .then((res) => res.data)
  },

  sessionEvents(sessionId: string, params?: { cursor?: number; highWater?: number; limit?: number }) {
    return api
      .get<SessionEventPage>(`/chat/sessions/${encodeURIComponent(sessionId)}/events`, {
        params: {
          cursor: params?.cursor ?? 0,
          ...(params?.highWater != null ? { high_water: params.highWater } : {}),
          limit: params?.limit ?? 1000,
        },
      })
      .then((res) => res.data)
  },

  /**
   * Open a conversation. Since plan 029 every ordinary conversation belongs to
   * a project: name one by `projectId`, or pass a `folder` and the gateway
   * creates (or reuses) the project bound to it. A primary-agent-level
   * skill-workshop task (`source: 'workshop'`) is the one shape that may carry
   * neither.
   */
  chatSessionCreate(
    opts: { title?: string; source?: '' | 'workshop'; projectId?: string; folder?: string } = {},
  ) {
    return postJson<
      { id: string; title: string; project?: ProjectRecord; created?: boolean },
      { title: string; source?: string; project_id?: string; folder?: string }
    >('/chat/sessions', {
      title: opts.title ?? '',
      ...(opts.source ? { source: opts.source } : {}),
      ...(opts.projectId ? { project_id: opts.projectId } : {}),
      ...(opts.folder ? { folder: opts.folder } : {}),
    })
  },

  /** One conversation by id, named for the page that has it open: the
   * drawer's list never carries a project's session, so the page reads its
   * title and project straight from the session. */
  chatSession(sessionId: string) {
    return api
      .get<ChatSessionInfo>(`/chat/sessions/${encodeURIComponent(sessionId)}`)
      .then((res) => res.data)
  },

  chatSessionTitle(sessionId: string, title: string) {
    return postJson<{ ok: boolean; id: string; title: string }, { title: string }>(
      `/chat/sessions/${sessionId}/title`,
      { title },
    )
  },

  sessionTodos(sessionId: string) {
    return api
      .get<SessionTodosResponse>(`/chat/sessions/${sessionId}/todos`)
      .then((res) => res.data)
  },

  sessionPlans(sessionId: string) {
    return api.get<{ plans: { name: string; path: string; updatedAt: number; markdown: string }[] }>(
      `/chat/sessions/${sessionId}/plans`,
    ).then((res) => res.data.plans ?? [])
  },

  sessionContext(sessionId: string, opts?: { scope?: 'summary' | 'full'; runId?: string }) {
    return api.get<SessionContextDebug>(`/chat/sessions/${sessionId}/context`, {
      params: {
        ...(opts?.scope ? { scope: opts.scope } : {}),
        ...(opts?.runId ? { run_id: opts.runId } : {}),
      },
    }).then((res) => res.data)
  },

  sessionMode(sessionId: string) {
    return api.get<SessionModeResponse>(`/chat/sessions/${sessionId}/mode`).then((res) => res.data)
  },

  /** The conversation's own model choice, and the models it may be switched
   *  to — the same options /model offers, each with the reasoning levels the
   *  shared catalog reports for it. */
  sessionModel(sessionId: string) {
    return api.get<SessionModelResponse>(`/chat/sessions/${encodeURIComponent(sessionId)}/model`).then((res) => res.data)
  },

  /** Records the conversation's own model choice. An empty effort is the
   *  explicit "model default" pin; the change rides the next turn. */
  saveSessionModel(sessionId: string, choice: { provider: string; model: string; effort: string }) {
    return putRaw<SessionModelSelection>(`/chat/sessions/${encodeURIComponent(sessionId)}/model`, choice)
  },

  slashCommands(surface = 'webchat', q = '', opts?: { duringRun?: boolean; sideConversation?: boolean; subagentView?: boolean }) {
    return api.get<SlashCommandsResponse>('/slash/commands', {
      params: {
        surface,
        q,
        ...(opts?.duringRun ? { during_run: 1 } : {}),
        ...(opts?.sideConversation ? { side: 1 } : {}),
        ...(opts?.subagentView ? { view: 'subagent' } : {}),
      },
    }).then((res) => res.data)
  },

  mentionSearch(q = '', scope?: WorkspaceScope) {
    return api
      .get<MentionSearchResponse>('/workspace/mentions', { params: { q, ...workspaceScopeParams(scope) } })
      .then((res) => res.data)
  },

  mentionAccept(payload: {
    draft: string
    token_start: number
    token_end: number
    path: string
    is_dir: boolean
  }, scope?: WorkspaceScope) {
    return api
      .post<MentionAcceptResponse>('/workspace/mentions/accept', { ...payload, ...workspaceScopeParams(scope) })
      .then((res) => res.data)
  },

  sessionToolAudit(sessionId: string, limit = 80) {
    return api
      .get<ToolAuditRow[]>(`/chat/sessions/${sessionId}/tool-audit`, { params: { limit } })
      .then((res) => (Array.isArray(res.data) ? res.data : []))
  },

  sessionCostSummary(sessionId: string) {
    return api.get<SessionCostSummary>(
      `/chat/sessions/${sessionId}/cost-summary`,
    ).then((res) => res.data)
  },

  sessionSubagentHistory(sessionId: string) {
    return api.get<SessionSubagentHistoryResponse>(
      `/chat/sessions/${sessionId}/subagent-history`,
    ).then((res) => res.data)
  },

  /**
   * A subagent's own view, talked to through the conversation it belongs to.
   * The engine owns every decision (plan 005); these calls are its transport.
   */
  subagentInput(sessionId: string, agentId: string, body: {
    message: string
    attachments?: string[]
    mentionImages?: string[]
    mode?: 'steer' | 'follow_up'
  }) {
    return postJson<SubagentInputResponse, typeof body>(
      `/chat/sessions/${encodeURIComponent(sessionId)}/subagents/${encodeURIComponent(agentId)}/input`,
      body,
    )
  },

  subagentQueuedInput(sessionId: string, agentId: string, body: { action: 'edit_last' }) {
    return postJson<RunInputResponse, typeof body>(
      `/chat/sessions/${encodeURIComponent(sessionId)}/subagents/${encodeURIComponent(agentId)}/queued-input`,
      body,
    )
  },

  subagentInterruptSend(sessionId: string, agentId: string) {
    return postJson<{ interrupted: boolean }, Record<string, never>>(
      `/chat/sessions/${encodeURIComponent(sessionId)}/subagents/${encodeURIComponent(agentId)}/interrupt-send`,
      {},
    )
  },

  subagentWithdraw(sessionId: string, agentId: string) {
    return postJson<SubagentWithdrawResponse, Record<string, never>>(
      `/chat/sessions/${encodeURIComponent(sessionId)}/subagents/${encodeURIComponent(agentId)}/withdraw`,
      {},
    )
  },

  /** Compact the subagent's own context; its events draw the card in its view. */
  subagentCompact(sessionId: string, agentId: string) {
    return postJson<Record<string, unknown>, Record<string, never>>(
      `/chat/sessions/${encodeURIComponent(sessionId)}/subagents/${encodeURIComponent(agentId)}/compact`,
      {},
    )
  },

  /** /context for a subagent, in the same shape as the conversation's. */
  subagentContext(sessionId: string, agentId: string) {
    return api.get<SessionContextDebug>(
      `/chat/sessions/${encodeURIComponent(sessionId)}/subagents/${encodeURIComponent(agentId)}/context`,
    ).then((res) => res.data)
  },

  /** The gauge a subagent's view opens with: its own context window. */
  subagentBudget(sessionId: string, agentId: string) {
    return api.get<ForebrainTokenBudget>(
      `/chat/sessions/${encodeURIComponent(sessionId)}/subagents/${encodeURIComponent(agentId)}/budget`,
    ).then((res) => res.data)
  },

  runInput(runId: string, body: { message: string; attachments?: string[]; mentionImages?: string[] }) {
    return postJson<RunInputResponse, typeof body>(
      `/runs/${encodeURIComponent(runId)}/input`,
      body,
    )
  },

  runQueuedInput(runId: string, body: { message?: string; attachments?: string[]; mentionImages?: string[]; action?: 'edit_last' }) {
    return postJson<RunInputResponse, typeof body>(
      `/runs/${encodeURIComponent(runId)}/queued-input`,
      body,
    )
  },

  runCancel(runId: string) {
    return postJson<{ ok: boolean; cancelled: boolean; preview?: PendingInputPreview }, Record<string, never>>(
      `/runs/${encodeURIComponent(runId)}/cancel`,
      {},
    )
  },

  runSubagents(runId: string, params?: { limit?: number; status?: string }) {
    return api.get<RunSubagentsResponse>(
      `/runs/${runId}/subagents`,
      { params },
    ).then((res) => res.data)
  },

  permissionsRules(params?: { source?: string; behavior?: string }) {
    return api.get<PermissionRulesResponse>('/permissions/rules', { params }).then((res) => ({
      mode: String(res.data?.mode ?? 'on-request'),
      rules: Array.isArray(res.data?.rules) ? res.data.rules : [],
    }))
  },

  /** One page of the permission table: searched by tool and rule text, counted
   * on the server. The approval mode stays on every answer. */
  permissionsRulesPage(query: ListQuery) {
    return api
      .get<{ rules?: PermissionRuleRecord[]; mode?: string } & ListPageMeta>('/permissions/rules', {
        params: listParams(query),
      })
      .then((res) => ({
        rows: Array.isArray(res.data?.rules) ? res.data.rules : [],
        mode: String(res.data?.mode ?? 'on-request'),
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
      }))
  },

  permissionsEvaluate(body: { toolName: string; input?: string }) {
    return postJson<PermissionDecision, { toolName: string; input?: string }>('/permissions/evaluate', body)
  },

  permissionsUpdate(body: PermissionUpdateBody) {
    return postJson<{ ok: boolean }, PermissionUpdateBody>('/permissions/updates', body)
  },

  skillsOverview() {
    return api.get<SkillsOverviewResponse>('/skills').then((res) => ({
      installed: Array.isArray(res.data?.installed) ? res.data.installed : [],
      actions: Array.isArray(res.data?.actions) ? res.data.actions : [],
    }))
  },

  /** One page of the agent's skill table: searched by name and description,
   * filtered by layer (`origin`), counted on the server. The actions the page
   * offers come along on every answer. */
  skillsPage(query: ListQuery) {
    return api
      .get<{ installed?: SkillRecord[]; actions?: string[] } & ListPageMeta>('/skills', { params: listParams(query) })
      .then((res) => ({
        rows: Array.isArray(res.data?.installed) ? res.data.installed : [],
        actions: Array.isArray(res.data?.actions) ? res.data.actions : [],
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
      }))
  },

  skillInspect(name: string) {
    return api.get<SkillInspectResponse>(`/skills/${encodeURIComponent(name)}`).then((res) => res.data)
  },

  skillCreate(body: { name: string; content: string }) {
    return postJson<SkillMutationResponse, { name: string; content: string }>('/skills', body)
  },

  skillUpdate(name: string, body: { content: string }) {
    return api
      .put<SkillMutationResponse>(`/skills/${encodeURIComponent(name)}`, toSnakeCase(body))
      .then((res) => res.data)
  },

  skillsToggle(enabledPaths: string[]) {
    return postJson<SkillsToggleResponse, { enabledPaths: string[] }>('/skills/toggle', { enabledPaths })
  },

  // Project-scoped skill lifecycle. The global methods above act on the
  // project the gateway process was launched in; these act on one specific
  // project, which is what a gateway serving several projects needs. The
  // routes mirror the global ones operation for operation.
  projectSkillsOverview(projectId: string) {
    return api.get<SkillsOverviewResponse>(`/v1/projects/${encodeURIComponent(projectId)}/skills`).then((res) => ({
      installed: Array.isArray(res.data?.installed) ? res.data.installed : [],
      actions: Array.isArray(res.data?.actions) ? res.data.actions : [],
    }))
  },

  /** One page of a project space's skill table, paged the same way the
   * agent's own table is. */
  projectSkillsPage(projectId: string, query: ListQuery) {
    return api
      .get<{ installed?: SkillRecord[]; actions?: string[] } & ListPageMeta>(
        `/v1/projects/${encodeURIComponent(projectId)}/skills`,
        { params: listParams(query) },
      )
      .then((res) => ({
        rows: Array.isArray(res.data?.installed) ? res.data.installed : [],
        actions: Array.isArray(res.data?.actions) ? res.data.actions : [],
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
      }))
  },

  projectSkillInspect(projectId: string, name: string) {
    return api
      .get<SkillInspectResponse>(`/v1/projects/${encodeURIComponent(projectId)}/skills/${encodeURIComponent(name)}`)
      .then((res) => res.data)
  },

  projectSkillCreate(projectId: string, body: { name: string; content: string }) {
    return postJson<SkillMutationResponse, { name: string; content: string }>(
      `/v1/projects/${encodeURIComponent(projectId)}/skills`,
      body,
    )
  },

  projectSkillUpdate(projectId: string, name: string, body: { content: string }) {
    return api
      .put<SkillMutationResponse>(
        `/v1/projects/${encodeURIComponent(projectId)}/skills/${encodeURIComponent(name)}`,
        toSnakeCase(body),
      )
      .then((res) => res.data)
  },

  projectSkillsToggle(projectId: string, enabledPaths: string[]) {
    return postJson<SkillsToggleResponse, { enabledPaths: string[] }>(
      `/v1/projects/${encodeURIComponent(projectId)}/skills/toggle`,
      { enabledPaths },
    )
  },

  projectSkillInstall(projectId: string, body: {
    sourceRef: string
    dest: 'project' | 'workspace' | 'global'
    skill?: string
    name?: string
    ref?: string
    sessionId?: string
  }) {
    return postJson<SkillInstallResponse, {
      sourceRef: string
      dest: 'project' | 'workspace' | 'global'
      skill?: string
      name?: string
      ref?: string
      sessionId?: string
    }>(`/v1/projects/${encodeURIComponent(projectId)}/skills/install`, body)
  },

  skillInstall(body: {
    sourceRef: string
    dest: 'project' | 'workspace' | 'global'
    skill?: string
    name?: string
    ref?: string
    sessionId?: string
  }) {
    return postJson<SkillInstallResponse, {
      sourceRef: string
      dest: 'project' | 'workspace' | 'global'
      skill?: string
      name?: string
      ref?: string
      sessionId?: string
    }>('/skills/install', body)
  },

  // Offline install: an uploaded .zip/.tar.gz/.tgz/.tar package. dest must be
  // explicit — the three skill pages each own one destination.
  skillInstallUpload(file: File, dest: 'project' | 'workspace' | 'global', projectId?: string) {
    const form = new FormData()
    form.append('file', file)
    form.append('dest_scope', dest)
    const base = projectId ? `/v1/projects/${encodeURIComponent(projectId)}/skills` : '/skills'
    return api.post<SkillUploadResponse>(`${base}/install/upload`, form, {
      headers: { 'Content-Type': 'multipart/form-data' },
    }).then((res) => res.data)
  },

  // Downloads stream as archives, so they go through fetch (same cookie
  // handling as axios) and come back as a blob plus the server's file name.
  skillDownload(url: string): Promise<SkillDownloadBlob> {
    return gatewayFetch(url).then(async (res) => {
      if (!res.ok) throw new Error(await gatewayErrorText(res))
      return {
        blob: await res.blob(),
        filename: filenameFromContentDisposition(res.headers.get('Content-Disposition')) || 'skill.zip',
      }
    })
  },

  skillDownloadBatch(names: string[], projectId?: string): Promise<SkillDownloadBlob> {
    const base = projectId ? `/api/v1/projects/${encodeURIComponent(projectId)}/skills` : '/api/skills'
    return gatewayFetch(`${base}/download`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ names }),
    }).then(async (res) => {
      if (!res.ok) throw new Error(await gatewayErrorText(res))
      return {
        blob: await res.blob(),
        filename: filenameFromContentDisposition(res.headers.get('Content-Disposition')) || 'skills.zip',
      }
    })
  },

  skillFilesList(name: string) {
    return api.get<SkillFilesResponse>(`/skills/${encodeURIComponent(name)}/files`).then((res) => res.data)
  },

  skillFileRead(name: string, path: string) {
    const query = new URLSearchParams({ path })
    return api
      .get<SkillFileContent>(`/skills/${encodeURIComponent(name)}/file?${query.toString()}`)
      .then((res) => res.data)
  },

  skillFileWrite(name: string, path: string, content: string) {
    const query = new URLSearchParams({ path })
    return api
      .put<{ ok: boolean; path: string }>(`/skills/${encodeURIComponent(name)}/file?${query.toString()}`, { content })
      .then((res) => res.data)
  },

  skillDelete(name: string, scope: 'agent' | 'shared' | 'project', projectId?: string) {
    const base = projectId ? `/v1/projects/${encodeURIComponent(projectId)}/skills` : '/skills'
    return api
      .delete<SkillsToggleResponse>(`${base}/${encodeURIComponent(name)}?scope=${scope}`)
      .then((res) => res.data)
  },

  // Drafts and versions. The agent-level methods act on the agent's own layers;
  // the project ones take a project id and act on that project's route set.
  // The two families mirror each other operation for operation.
  skillVersions(name: string, projectId?: string) {
    const base = projectId ? `/v1/projects/${encodeURIComponent(projectId)}/skills` : '/skills'
    return api.get<SkillVersionResponse>(`${base}/${encodeURIComponent(name)}/versions`).then((res) => res.data)
  },

  skillPublish(name: string, note = '', projectId?: string) {
    const base = projectId ? `/v1/projects/${encodeURIComponent(projectId)}/skills` : '/skills'
    return postJson<{ status: string; version: number }, { note: string }>(
      `${base}/${encodeURIComponent(name)}/publish`,
      { note },
    )
  },

  skillDiscardDraft(name: string, projectId?: string) {
    const base = projectId ? `/v1/projects/${encodeURIComponent(projectId)}/skills` : '/skills'
    return postJson<{ status: string }, Record<string, never>>(
      `${base}/${encodeURIComponent(name)}/draft/discard`,
      {},
    )
  },

  skillRestoreVersion(name: string, version: number, replaceDraft = false, projectId?: string) {
    const base = projectId ? `/v1/projects/${encodeURIComponent(projectId)}/skills` : '/skills'
    return postJson<{ status: string; version: number }, { replaceDraft: boolean }>(
      `${base}/${encodeURIComponent(name)}/versions/${version}/restore`,
      { replaceDraft },
    )
  },

  skillPull(name: string, replaceDraft = false, projectId?: string) {
    const base = projectId ? `/v1/projects/${encodeURIComponent(projectId)}/skills` : '/skills'
    return postJson<{ status: string }, { replaceDraft: boolean }>(
      `${base}/${encodeURIComponent(name)}/pull`,
      { replaceDraft },
    )
  },

  skillDiff(name: string, from: string, to: string, projectId?: string) {
    const base = projectId ? `/v1/projects/${encodeURIComponent(projectId)}/skills` : '/skills'
    const query = new URLSearchParams({ from, to })
    return api.get<SkillDiffResponse>(`${base}/${encodeURIComponent(name)}/diff?${query.toString()}`).then((res) => res.data)
  },

  // The URL a version's zip streams from, for a plain anchor/fetch download.
  skillVersionDownloadUrl(name: string, version: number, projectId?: string) {
    const base = projectId
      ? `/api/v1/projects/${encodeURIComponent(projectId)}/skills`
      : '/api/skills'
    return `${base}/${encodeURIComponent(name)}/versions/${version}/download`
  },

  // Workshop artifacts: what an evaluation or trigger run left behind.
  skillWorkshopEvals(name: string, projectId?: string) {
    const base = projectId ? `/v1/projects/${encodeURIComponent(projectId)}/skills` : '/skills'
    return api.get<WorkshopEvalsResponse>(`${base}/${encodeURIComponent(name)}/workshop/evals`).then((res) => res.data)
  },

  skillWorkshopFeedback(name: string, iteration: string, runId: string, feedback: string, projectId?: string) {
    const base = projectId ? `/v1/projects/${encodeURIComponent(projectId)}/skills` : '/skills'
    return api
      .put<{ status: string }>(
        `${base}/${encodeURIComponent(name)}/workshop/evals/${encodeURIComponent(iteration)}/feedback`,
        { run_id: runId, feedback },
      )
      .then((res) => res.data)
  },

  skillWorkshopTrigger(name: string, projectId?: string) {
    const base = projectId ? `/v1/projects/${encodeURIComponent(projectId)}/skills` : '/skills'
    return api.get<WorkshopTriggerResponse>(`${base}/${encodeURIComponent(name)}/workshop/trigger`).then((res) => res.data)
  },

  rewindPreview(sessionId: string, messageId: number) {
    const query = new URLSearchParams({ message_id: String(messageId) })
    return gatewayFetch(
      `/api/chat/sessions/${encodeURIComponent(sessionId)}/rewind-preview?${query.toString()}`,
    ).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return toCamelCase(await r.json()) as RewindPreview
    })
  },

  rewind(sessionId: string, body: { messageId: number; mode: 'both' | 'chat' | 'code'; keepPaths?: string[] }) {
    return gatewayFetch(`/api/chat/sessions/${encodeURIComponent(sessionId)}/rewind`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        message_id: body.messageId,
        mode: body.mode,
        keep_paths: body.keepPaths ?? [],
      }),
    }).then(async (r) => {
      if (r.status === 409) throw new Error('session_busy')
      if (!r.ok) throw new Error(await r.text())
      return toCamelCase(await r.json()) as RewindResult
    })
  },

  rewindUndo(sessionId: string) {
    return gatewayFetch(`/api/chat/sessions/${encodeURIComponent(sessionId)}/rewind/undo`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
    }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return toCamelCase(await r.json()) as { status: string }
    })
  },

  sessionFileChanges(sessionId: string) {
    return gatewayFetch(`/api/chat/sessions/${encodeURIComponent(sessionId)}/file-changes`).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      const data = (await r.json()) as { files?: RewindChangedFile[] }
      return data.files ?? []
    })
  },

  sessionFileChangeDiff(sessionId: string, path: string) {
    const query = new URLSearchParams({ path })
    return gatewayFetch(
      `/api/chat/sessions/${encodeURIComponent(sessionId)}/file-changes/diff?${query.toString()}`,
    ).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return (await r.json()) as { path: string; diff: string }
    })
  },

  sessionFileChangeRevert(sessionId: string, path: string) {
    return gatewayFetch(`/api/chat/sessions/${encodeURIComponent(sessionId)}/file-changes/revert`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path }),
    }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return (await r.json()) as { status: string }
    })
  },

  sessionMessageSearchApi(sessionId: string, q: string, limit = 30) {
    return api
      .get(`/chat/sessions/${sessionId}/search`, { params: { q, limit } })
      .then((res) => res.data as SessionMessageSearchRecord[])
  },

  async getModels(params?: { q?: string; provider?: string; limit?: number }): Promise<ModelCatalogListing> {
    const res = await api.get<{ records?: ModelRecord[]; status?: ModelProviderStatus[] }>('/models', { params })
    return {
      records: Array.isArray(res.data?.records) ? res.data.records : [],
      status: Array.isArray(res.data?.status) ? res.data.status : [],
    }
  },

  memorySettings() {
    return api.get<{ settings: MemorySettings }>('/memories/settings').then((res) => res.data.settings)
  },

  updateMemorySettings(body: {
    featureEnabled?: boolean
    useMemories?: boolean
    generateMemories?: boolean
    threadId?: string
  }) {
    return postJson<{ settings: MemorySettings }, typeof body>('/memories/settings', body).then((res) => res.settings)
  },

  resetMemories(body?: { scope?: 'session' | 'all' | 'global' | 'project'; projectId?: string }) {
    // An absent body keeps the endpoint's default (the calling session's
    // project); the memory pages name their scope explicitly.
    return postJson<{ ok: boolean; scope?: string }, { scope?: string; project_id?: string } | Record<string, never>>(
      '/memories/reset',
      body && body.projectId ? { scope: body.scope, project_id: body.projectId } : body?.scope ? { scope: body.scope } : {},
    )
  },

  memoryFilesList(query: {
    scope: 'global' | 'project'
    projectId?: string
    page?: number
    pageSize?: number
    sort?: 'created' | 'updated'
    order?: 'asc' | 'desc'
    q?: string
  }) {
    const params = new URLSearchParams()
    params.set('scope', query.scope)
    if (query.projectId) params.set('project_id', query.projectId)
    if (query.page) params.set('page', String(query.page))
    if (query.pageSize) params.set('page_size', String(query.pageSize))
    if (query.sort) params.set('sort', query.sort)
    if (query.order) params.set('order', query.order)
    if (query.q) params.set('q', query.q)
    return api.get<MemoryFilesResponse>(`/memories/files?${params.toString()}`).then((res) => res.data)
  },

  /** One page of the memory file table, in the shape every table's list takes
   * (plan 071/073): the search, the time column ordered by and the page are the
   * server's. `scope` — and `projectId` for a project scope — ride the query. */
  memoryFilesPage(query: ListQuery) {
    return api.get<MemoryFilesResponse>('/memories/files', { params: listParams(query) }).then((res) => ({
      rows: Array.isArray(res.data?.files) ? res.data.files : [],
      total: Number(res.data?.total ?? 0),
      page: Number(res.data?.page ?? 1),
      pageSize: Number(res.data?.pageSize ?? query.pageSize),
    }))
  },

  memoryFileRead(scope: 'global' | 'project', path: string, projectId?: string) {
    const params = new URLSearchParams({ scope, path })
    if (projectId) params.set('project_id', projectId)
    return api.get<MemoryFileContent>(`/memories/file?${params.toString()}`).then((res) => res.data)
  },

  memoryFileWrite(scope: 'global' | 'project', path: string, content: string, projectId?: string) {
    const params = new URLSearchParams({ scope, path })
    if (projectId) params.set('project_id', projectId)
    return api.put<{ ok: boolean; path: string }>(`/memories/file?${params.toString()}`, { content }).then((res) => res.data)
  },

  memoryFilesDelete(scope: 'global' | 'project', paths: string[], projectId?: string) {
    const params = new URLSearchParams({ scope })
    if (projectId) params.set('project_id', projectId)
    return postJson<MemoryFilesDeleteResponse, { paths: string[] }>(`/memories/files/delete?${params.toString()}`, { paths })
  },

  actionsList(status?: string, sessionId?: string, agentId?: string) {
    const params = new URLSearchParams()
    if (status) params.set('status', status)
    if (sessionId) params.set('session_id', sessionId)
    if (agentId) params.set('agent_id', agentId)
    const query = params.toString()
    return gatewayFetch(`/api/actions${query ? `?${query}` : ''}`, {
      method: 'GET',
    }).then(async (r) => {
      if (!r.ok) throw new GatewayHttpError(r.status, (await r.text()).trim())
      return toCamelCase(await r.json()) as ActionRecord[]
    })
  },

  actionsAnswer(id: string, body: ActionAnswerBody) {
    return gatewayFetch(`/api/actions/${encodeURIComponent(id)}/answer`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(toSnakeCase(body)),
    }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return toCamelCase(await r.json()) as ActionRecord
    })
  },

  actionsApprove(id: string, body?: ActionApprovalBody) {
    return gatewayFetch(`/api/actions/${encodeURIComponent(id)}/approve`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(toSnakeCase({
        reason: body?.reason ?? '',
        ...(body?.update ? { update: body.update } : {}),
        ...(body?.clearContext ? { clearContext: true } : {}),
        ...(body?.decision ? { decision: body.decision } : {}),
        ...(body?.execpolicyAmendment ? { execpolicyAmendment: body.execpolicyAmendment } : {}),
        ...(body?.networkPolicyAmendment ? { networkPolicyAmendment: body.networkPolicyAmendment } : {}),
        ...(body?.requestPermissionsResponse ? { requestPermissionsResponse: body.requestPermissionsResponse } : {}),
      })),
    }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return toCamelCase(await r.json()) as ActionRecord
    })
  },

  actionsDeny(id: string, body?: { reason?: string }) {
    return gatewayFetch(`/api/actions/${encodeURIComponent(id)}/deny`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(toSnakeCase({ reason: body?.reason ?? '' })),
    }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return toCamelCase(await r.json()) as ActionRecord
    })
  },

  /** The approval a session is parked on, with everything its card needs.
   * Null when nothing is parked (the gateway answers 204). */
  sessionApprovalRequest(sessionId: string) {
    return gatewayFetch(`/api/chat/sessions/${encodeURIComponent(sessionId)}/approval-request`, {
      method: 'GET',
    }).then(async (r) => {
      if (r.status === 204) return null
      if (!r.ok) throw new GatewayHttpError(r.status, (await r.text()).trim())
      return toCamelCase(await r.json()) as SessionApprovalRequest
    })
  },

  /** Ask one configured model to review a pending exit-plan approval. The
   * review runs in the background; its events are its progress. */
  actionPlanReview(id: string, body: { provider: string; model: string }) {
    return gatewayFetch(`/api/actions/${encodeURIComponent(id)}/plan-review`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(toSnakeCase(body)),
    }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return r.status === 202
    })
  },

  workspaceTree(path = '', scope?: WorkspaceScope) {
    return api
      .get<WorkspaceTreeResponse>('/workspace/tree', {
        params: { ...(path ? { path } : {}), ...workspaceScopeParams(scope) },
      })
      .then((res) => res.data)
  },

  /** Read one text file of a conversation's file view (workbench 017). */
  workspaceFile(scope: WorkspaceScope, path: string) {
    const params = new URLSearchParams({ path, ...workspaceScopeParams(scope) })
    return gatewayFetch(`/api/workspace/file?${params.toString()}`).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return (await r.json()) as { path: string; content: string }
    })
  },

  /**
   * The URL an image's bytes are fetched from: the same file view, served as
   * pixels. A surface puts it straight in an <img src>, so the image never
   * travels through an event or a JSON response as base64. The endpoint serves
   * only images; a non-image path is refused (415).
   */
  workspaceFileRawUrl(scope: WorkspaceScope, path: string): string {
    const params = new URLSearchParams({ path, ...workspaceScopeParams(scope) })
    return `/api/workspace/file/raw?${params.toString()}`
  },

  /** Write or create a file; with create, an existing path is refused (409). */
  workspaceWrite(scope: WorkspaceScope, body: { path: string; content: string; create?: boolean }) {
    const params = new URLSearchParams(workspaceScopeParams(scope))
    return gatewayFetch(`/api/workspace/file?${params.toString()}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return (await r.json()) as { ok: boolean; path: string }
    })
  },

  /** Rename a file or directory within a file view. */
  workspaceRename(scope: WorkspaceScope, body: { from: string; to: string }) {
    const params = new URLSearchParams(workspaceScopeParams(scope))
    return gatewayFetch(`/api/workspace/rename?${params.toString()}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return (await r.json()) as { ok: boolean; path: string }
    })
  },

  /** Delete one file, or one directory and everything under it. */
  workspaceDelete(scope: WorkspaceScope, path: string) {
    const params = new URLSearchParams({ path, ...workspaceScopeParams(scope) })
    return gatewayFetch(`/api/workspace/entry?${params.toString()}`, { method: 'DELETE' }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return (await r.json()) as { ok: boolean }
    })
  },

  /** The tool table the active primary agent's runtime actually exposes. */
  toolsList() {
    return api.get<ToolMetaRecord[]>('/tools').then((res) => (Array.isArray(res.data) ? res.data : []))
  },

  /** One page of the tool table: searched by name and description, filtered by
   * category and type, ordered A→Z on the server. `categories` lists every
   * category the runtime exposes whatever the filters leave, so the dropdown
   * can always offer the way back. */
  toolsPage(query: ListQuery) {
    return getRawJSON<{ tools?: Record<string, unknown>[]; categories?: string[]; total?: number; page?: number; page_size?: number }>(
      '/tools',
      listParams(query),
    ).then((answer) => ({
      rows: Array.isArray(answer.tools) ? answer.tools.map(toolMetaFromWire) : [],
      categories: Array.isArray(answer.categories) ? answer.categories.map(String) : [],
      total: Number(answer.total ?? 0),
      page: Number(answer.page ?? 1),
      pageSize: Number(answer.page_size ?? query.pageSize),
    }))
  },

  /** The MCP servers configured for the active primary agent. */
  mcpServers(params?: { sessionId?: string }) {
    return api
      .get<McpServersResponse>('/v1/mcp/servers', { params: toSnakeCase(params ?? {}) })
      .then((res) => (Array.isArray(res.data?.servers) ? res.data.servers : []))
  },

  mcpServersWithStatus(params?: { sessionId?: string }) {
    return api
      .get<McpServersResponse>('/v1/mcp/servers', { params: toSnakeCase(params ?? {}) })
      .then((res) => ({
        servers: Array.isArray(res.data?.servers) ? res.data.servers : [],
        project: res.data?.project ?? {},
        runtimeScope: res.data?.runtimeScope,
        runtimeAvailable: res.data?.runtimeAvailable ?? false,
        generation: res.data?.generation,
        pending: res.data?.pending ?? false,
      }))
  },

  /**
   * One page of an MCP table. `view` names which layers the page shows and is
   * what the server answers with — one layer for 设置, the agent's on top of the
   * shared one for the menu bar's page, all three for a project space — so the
   * search, the `source` layer filter, the order and the slice are all the
   * server's, and each row says which layer it came from.
   */
  mcpPage(view: McpLayer, query: ListQuery & { projectId?: string; source?: string }) {
    const { projectId, ...rest } = query
    const params = listParams(rest)
    if (view === 'global') params.set('scope', 'global')
    if (view === 'agent') params.set('view', 'agent')
    const path = view === 'project' ? `/v1/projects/${encodeURIComponent(projectId ?? '')}/mcp` : '/v1/mcp/servers'
    return getRawJSON<{ servers?: Record<string, unknown>[]; not_applied?: McpNotAppliedRow[]; total?: number; page?: number; page_size?: number }>(path, params).then((answer) => ({
      rows: Array.isArray(answer.servers) ? answer.servers.map(mcpRowFromWire) : [],
      total: Number(answer.total ?? 0),
      page: Number(answer.page ?? 1),
      pageSize: Number(answer.page_size ?? query.pageSize),
      // A project file's entries that did not apply: the preview reported them
      // beside the list, and the table says so under it (the old tab did).
      notApplied: Array.isArray(answer.not_applied) ? answer.not_applied : [],
    }))
  },

  /**
   * Create one MCP server in one layer. `id` is the primary agent's id for the
   * agent layer and the project's id for a project; the global layer has none.
   * `body` is `mcpWritePayload`'s answer, already in the wire's own spelling.
   */
  mcpCreate(scope: McpLayer, id: string, body: Record<string, unknown>) {
    return postRaw<McpServerWriteResult>(mcpScopedPath(scope, id, 'servers'), body)
  },

  /** Update one entry. The name is the entry's identity and comes from the
   * route; an update never renames. Fields the body omits are left alone. */
  mcpUpdate(scope: McpLayer, id: string, name: string, body: Record<string, unknown>) {
    return putRaw<McpServerWriteResult>(`${mcpScopedPath(scope, id, 'servers')}/${encodeURIComponent(name)}`, body)
  },

  mcpDelete(scope: McpLayer, id: string, name: string) {
    return api
      .delete<McpServerWriteResult>(`${mcpScopedPath(scope, id, 'servers')}/${encodeURIComponent(name)}`)
      .then((res) => res.data)
  },

  /** Try one entry without saving it: the same start a new session would make,
   * so "the test passed" and "a session can connect" are the same statement. */
  mcpTest(scope: McpLayer, id: string, body: Record<string, unknown>) {
    return postRaw<McpTestResult>(`${mcpScopedPath(scope, id, 'servers')}/test`, body)
  },

  /** The subdirectories of a host directory (or the user's home when path is
   * empty), for the new-chat page's folder picker. */
  hostDirs(path?: string) {
    return api
      .get<HostDirListing>('/host/dirs', { params: path ? { path } : undefined })
      .then((res) => res.data)
  },

  /** Create one subdirectory under parent. A name already taken answers 409 {
   * error: 'exists' }. */
  hostDirCreate(parent: string, name: string) {
    return postJson<{ path: string }, { parent: string; name: string }>('/host/dirs', { parent, name })
  },

  /** The subdirectories of one directory inside a project, for the "save to"
   * dialog. `path` is relative to the project root; empty is the root. */
  projectDirs(id: string, path = '') {
    return api
      .get<ProjectDirListing>(`/v1/projects/${encodeURIComponent(id)}/dirs`, { params: path ? { path } : undefined })
      .then((res) => res.data)
  },

  /** Create one subdirectory inside a project (`parent` is relative to the
   * root; empty is the root). A name already taken answers 409 {
   * error: 'exists' }. */
  projectDirCreate(id: string, parent: string, name: string) {
    return postJson<{ path: string }, { parent: string; name: string }>(
      `/v1/projects/${encodeURIComponent(id)}/dirs`,
      { parent, name },
    )
  },

  /** One directory's trust decision. */
  hostTrust(path: string) {
    return api.get<{ level: TrustLevel }>('/host/trust', { params: { path } }).then((res) => res.data)
  },

  /** Record a directory's trust decision — the same record the terminal writes. */
  hostTrustSet(path: string, trusted: boolean) {
    return postJson<{ ok: boolean; level: TrustLevel }, { path: string; trusted: boolean }>('/host/trust', { path, trusted })
  },

  projectsList(params?: { search?: string; sort?: string; limit?: number; offset?: number }) {
    return api
      .get<{ projects?: ProjectRecord[] }>('/v1/projects', { params: toSnakeCase(params ?? {}) })
      .then((res) => (Array.isArray(res.data?.projects) ? res.data.projects : []))
  },

  /** One page of the project table: searched, ordered (the pinned ones first,
   * then the sort key) and counted on the server. */
  projectsPage(query: ListQuery) {
    return api
      .get<{ projects?: ProjectRecord[] } & ListPageMeta>('/v1/projects', { params: listParams(query) })
      .then((res) => ({
        rows: Array.isArray(res.data?.projects) ? res.data.projects : [],
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
      }))
  },

  projectCreate(body: {
    name: string
    root: string
    icon?: string
    description?: string
    instructions?: string
    trust?: boolean
    memoryScope?: string
    resourceAccess?: boolean
  }) {
    return postJson<ProjectRecord, Record<string, unknown>>('/v1/projects', toSnakeCase(body))
  },

  projectGet(id: string) {
    return api.get<ProjectRecord>(`/v1/projects/${encodeURIComponent(id)}`).then((res) => res.data)
  },

  projectUpdate(id: string, body: Partial<{ name: string; icon: string; description: string; instructions: string; memoryScope: string; resourceAccess: boolean; trust: boolean }>) {
    return api
      // axios reads the second type argument as the whole response, not the
      // request body, so naming the body there left res.data as unknown.
      .patch<ProjectRecord>(`/v1/projects/${encodeURIComponent(id)}`, toSnakeCase(body))
      .then((res) => res.data)
  },

  projectPin(id: string, pinned: boolean) {
    return postJson<{ ok: boolean }, { pinned: boolean }>(`/v1/projects/${encodeURIComponent(id)}/pin`, { pinned })
  },

  /** What deleting this project would take with it: its conversations and cron jobs, and whether a run is in flight. */
  projectDeletePreview(id: string) {
    return api
      .get<{ sessions: number; cron_jobs: number; running: boolean }>(`/v1/projects/${encodeURIComponent(id)}/delete-preview`)
      .then((res) => res.data)
  },

  projectDelete(id: string) {
    return api.delete<{ ok: boolean }>(`/v1/projects/${encodeURIComponent(id)}`).then((res) => res.data)
  },

  projectSessions(id: string) {
    return api
      .get<{ sessions?: ProjectSessionRecord[] }>(`/v1/projects/${encodeURIComponent(id)}/sessions`)
      .then((res) => (Array.isArray(res.data?.sessions) ? res.data.sessions : []))
  },

  /** One page of the project's conversation table: searched by title, ordered
   * by creation or update time on the server, with each row's model, message
   * count and heartbeat. */
  projectSessionsPage(id: string, query: ListQuery) {
    return api
      .get<{ sessions?: ProjectSessionRow[] } & ListPageMeta>(`/v1/projects/${encodeURIComponent(id)}/sessions`, {
        params: listParams(query),
      })
      .then((res) => ({
        rows: Array.isArray(res.data?.sessions) ? res.data.sessions : [],
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
      }))
  },

  projectSessionCreate(id: string, title: string) {
    return postJson<{ id: string; title: string }, { title: string }>(`/v1/projects/${encodeURIComponent(id)}/sessions`, { title })
  },

  projectMcp(id: string) {
    return api.get<ProjectMcpRecord>(`/v1/projects/${encodeURIComponent(id)}/mcp`).then((res) => res.data)
  },

  /** Record the decision for the project entries named in `decide` — allow when
   * the name is also in `allow`, decline otherwise — and leave every other
   * entry as it was; this is also how a declined entry is allowed again. Without
   * `decide` the endpoint decides the whole undecided set, which is what the
   * terminal's startup prompt does.
   *
   * `fingerprints` carries each decided entry's fingerprint as the page showed
   * it; the server records nothing and answers 409 when any entry no longer
   * matches. It travels as an array — postJson snake-cases object keys, which
   * would corrupt an object keyed by entry name (`my-server` → `my_server`). */
  projectMcpConsent(id: string, allow: string[], decide?: string[], fingerprints?: Array<{ name: string; fingerprint: string }>) {
    const body: { allow: string[]; decide?: string[]; fingerprints?: Array<{ name: string; fingerprint: string }> } =
      decide ? { allow, decide, fingerprints } : { allow }
    return postJson<{ ok: boolean }, typeof body>(`/v1/projects/${encodeURIComponent(id)}/mcp/consent`, body)
  },

  projectLsp(id: string) {
    return api.get<ProjectLspRecord>(`/v1/projects/${encodeURIComponent(id)}/lsp`).then((res) => res.data)
  },

  /** One page of the project's language-server table: pending, allowed and
   * denied entries as one list (a pending entry first), searched by id and
   * summary on the server. A pending entry is the only one the server has a
   * summary for. */
  projectLspPage(id: string, query: ListQuery) {
    return api
      .get<{ rows?: ProjectLspRow[]; trusted?: boolean; notes?: string[] } & ListPageMeta>(
        `/v1/projects/${encodeURIComponent(id)}/lsp`,
        { params: listParams(query) },
      )
      .then((res) => ({
        rows: Array.isArray(res.data?.rows) ? res.data.rows : [],
        trusted: Boolean(res.data?.trusted),
        notes: Array.isArray(res.data?.notes) ? res.data.notes : [],
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
      }))
  },

  /** Same two shapes as the MCP consent above: `decide` names the one entry
   * this click decides, and a declined entry has no other way back.
   * `fingerprints` (id in `name`) binds the decision to the entry the page
   * showed; array for the same key-rewrite reason as above. */
  projectLspConsent(id: string, allow: string[], decide?: string[], fingerprints?: Array<{ name: string; fingerprint: string }>) {
    const body: { allow: string[]; decide?: string[]; fingerprints?: Array<{ name: string; fingerprint: string }> } =
      decide ? { allow, decide, fingerprints } : { allow }
    return postJson<{ ok: boolean; allowed: string[] }, typeof body>(`/v1/projects/${encodeURIComponent(id)}/lsp/consent`, body)
  },

  agentRuleFiles() {
    return api.get<{ files: Array<{ name: string; exists: boolean; sizeBytes?: number; updatedAt?: number }> }>('/rules/agent').then((res) => res.data)
  },

  /** One page of the rule-file table: searched by file name and counted on
   * the server. */
  agentRuleFilesPage(query: ListQuery) {
    return api
      .get<{ files?: RuleFileSummary[] } & ListPageMeta>('/rules/agent', { params: listParams(query) })
      .then((res) => ({
        rows: Array.isArray(res.data?.files) ? res.data.files : [],
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
      }))
  },

  // Rule files travel as their raw text on save; a read always answers one
  // JSON shape, so a file that does not exist yet is never mistaken for one
  // whose text happens to look like a status reply.
  agentRuleFile(name: string) {
    return api.get<RuleFileContent>(`/rules/agent/${encodeURIComponent(name)}`).then((res) => res.data)
  },

  saveAgentRuleFile(name: string, content: string) {
    return api
      .put<RuleFileSaveResponse>(`/rules/agent/${encodeURIComponent(name)}`, content, { headers: { 'Content-Type': 'text/plain' } })
      .then((res) => res.data)
  },

  projectRuleFiles(projectId: string) {
    return api.get<{ files: Array<{ dir: string; exists: boolean; sizeBytes?: number; updatedAt?: number }>; create: string[] }>(`/rules/project/${encodeURIComponent(projectId)}`).then((res) => res.data)
  },

  /** One page of the project's rule-file table: searched by file path and
   * counted on the server. `create` still names the directory layers that hold
   * no file yet, so the create dialog can offer them whatever the search
   * leaves. */
  projectRuleFilesPage(projectId: string, query: ListQuery) {
    return api
      .get<{ files?: ProjectRuleFileRow[]; create?: string[] } & ListPageMeta>(
        `/rules/project/${encodeURIComponent(projectId)}`,
        { params: listParams(query) },
      )
      .then((res) => ({
        rows: Array.isArray(res.data?.files) ? res.data.files : [],
        create: Array.isArray(res.data?.create) ? res.data.create : [],
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
      }))
  },

  projectRuleFile(projectId: string, dir: string) {
    return api
      .get<RuleFileContent>(`/rules/project/${encodeURIComponent(projectId)}/file`, { params: dir ? { dir } : undefined })
      .then((res) => res.data)
  },

  saveProjectRuleFile(projectId: string, dir: string, content: string) {
    return api
      .put<RuleFileSaveResponse>(`/rules/project/${encodeURIComponent(projectId)}/file`, content, {
        params: dir ? { dir } : undefined,
        headers: { 'Content-Type': 'text/plain' },
      })
      .then((res) => res.data)
  },

  // A project space's own permission rules: read, changed and probed against
  // that project's settings, whichever project the gateway was launched in.
  projectPermissionRules(projectId: string) {
    return api
      .get<{ applies: boolean; rules: PermissionRuleRecord[] }>(`/v1/projects/${encodeURIComponent(projectId)}/permissions/rules`)
      .then((res) => ({ applies: Boolean(res.data?.applies), rules: Array.isArray(res.data?.rules) ? res.data.rules : [] }))
  },

  /** One page of the project's permission table: searched over the tool a rule
   * names and the rule's own text, counted on the server. */
  projectPermissionRulesPage(projectId: string, query: ListQuery) {
    return api
      .get<{ rules?: ProjectPermissionRuleRow[]; applies?: boolean } & ListPageMeta>(
        `/v1/projects/${encodeURIComponent(projectId)}/permissions/rules`,
        { params: listParams(query) },
      )
      .then((res) => ({
        rows: Array.isArray(res.data?.rules) ? res.data.rules : [],
        applies: Boolean(res.data?.applies),
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
      }))
  },

  projectPermissionUpdate(projectId: string, body: Omit<PermissionUpdateBody, 'destination'>) {
    return postJson<{ ok: boolean; rules: PermissionRuleRecord[] }, Omit<PermissionUpdateBody, 'destination'>>(
      `/v1/projects/${encodeURIComponent(projectId)}/permissions/updates`,
      body,
    )
  },

  projectPermissionExplain(projectId: string, params: { toolName: string; input?: string }) {
    return api
      .get<PermissionExplainResponse>(`/v1/projects/${encodeURIComponent(projectId)}/permissions/explain`, {
        params: { tool_name: params.toolName, input: params.input ?? '' },
      })
      .then((res) => res.data)
  },

  sessionPresetCurrent(sessionId: string) {
    return api
      .get<{ current: string | null; description?: string }>('/permissions/session-preset', { params: { session_id: sessionId } })
      .then((res) => res.data)
  },

  approvalDefault() {
    return api.get<{ current: string | null; description?: string }>('/permissions/approval-default').then((res) => res.data)
  },

  saveApprovalDefault(preset: string) {
    return putRaw<{ current: string; description?: string }>('/permissions/approval-default', { preset })
  },

  sessionPreset(sessionId: string, preset: string) {
    return postJson<{ ok: boolean; description?: string }, { sessionId: string; preset: string }>('/permissions/session-preset', { sessionId, preset })
  },

  configFile() {
    return api.get<{ path: string; agentId?: string; yaml: string }>('/config').then((res) => res.data)
  },

  saveConfigFile(yaml: string) {
    return postJson<{ path: string; applied: boolean }, { yaml: string }>('/config', { yaml })
  },

  // Channels belong to a project, so they are read and written through the
  // project's own route.
  projectChannels(projectId: string) {
    return api.get<{ projectId?: string; kinds?: string[]; channels: Record<string, unknown> }>(
      `/v1/projects/${encodeURIComponent(projectId)}/channels`,
    ).then((res) => res.data)
  },

  /**
   * Every channel kind of the project as the server stores it, read through
   * `getRawJSON`: the keys of a channel's field map are the channel's own
   * (`app_id`, `app_secret`) and the camelCase interceptor would rename them
   * into keys the channel driver never reads. Writing one channel back means
   * sending the whole map, so this is the read a save starts from.
   */
  async projectChannelsRaw(projectId: string) {
    const answer = await getRawJSON<{ kinds?: string[]; channels?: Record<string, Record<string, unknown>> }>(
      `/v1/projects/${encodeURIComponent(projectId)}/channels`,
      new URLSearchParams(),
    )
    return {
      kinds: Array.isArray(answer.kinds) ? answer.kinds.map(String) : [],
      channels: answer.channels && typeof answer.channels === 'object' ? answer.channels : {},
    }
  },

  /**
   * One page of the project's channel table. Raw for the same reason
   * `projectChannelsRaw` is: each row's `fields` map is keyed by the channel's
   * own spelling, so the camelCase interceptor would rename the values a user
   * typed.
   */
  async projectChannelsPage(projectId: string, query: ListQuery) {
    const answer = await getRawJSON<{ rows?: Record<string, unknown>[]; total?: number; page?: number; page_size?: number }>(
      `/v1/projects/${encodeURIComponent(projectId)}/channels`,
      listParams(query),
    )
    return {
      rows: Array.isArray(answer.rows) ? answer.rows.map(projectChannelRowFromWire) : [],
      total: Number(answer.total ?? 0),
      page: Number(answer.page ?? 1),
      pageSize: Number(answer.page_size ?? query.pageSize),
    }
  },

  saveProjectChannels(projectId: string, channels: Record<string, unknown>) {
    return api.put<{ applied: boolean }>(
      `/v1/projects/${encodeURIComponent(projectId)}/channels`,
      toSnakeCase(channels),
    ).then((res) => res.data)
  },

  providers() {
    return api.get<{ agentId?: string; providers: ProviderRecord[] }>('/providers')
      .then((res) => (Array.isArray(res.data?.providers) ? res.data.providers : []))
  },

  /** One page of the model-services table: searched over service, models and
   * endpoint, in the stored fallback order, counted on the server. Each row
   * keeps its position in the whole order. */
  providersPage(query: ListQuery) {
    return api
      .get<{ providers?: ProviderRecord[]; agentId?: string } & ListPageMeta>('/providers', {
        params: listParams(query),
      })
      .then((res) => ({
        rows: Array.isArray(res.data?.providers) ? res.data.providers : [],
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
      }))
  },

  saveProviders(providers: Array<Record<string, unknown>>) {
    return putRaw<{ applied: boolean }>('/providers', { providers })
  },

  hooks() {
    return api.get<{ hooks: HooksSettingsRecord; knownTypes: string[]; events: string[] }>('/hooks')
      .then((res) => res.data)
  },

  /** One page of the hooks table: the stored map flattened to one row per
   * command, searched over event, matcher and command. An edit or a delete
   * writes the whole map back, so it reads `hooks()` first and locates its row
   * by the two indices this page carries. */
  hooksPage(query: ListQuery) {
    return api
      .get<{ rows?: HookRowRecord[]; knownTypes?: string[]; events?: string[] } & ListPageMeta>('/hooks', {
        params: listParams(query),
      })
      .then((res) => ({
        rows: Array.isArray(res.data?.rows) ? res.data.rows : [],
        knownTypes: Array.isArray(res.data?.knownTypes) ? res.data.knownTypes : [],
        events: Array.isArray(res.data?.events) ? res.data.events : [],
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
      }))
  },

  saveHooks(hooks: HooksSettingsRecord) {
    return api.put<{ applied: boolean }>('/hooks', toSnakeCase({ hooks })).then((res) => res.data)
  },

  /** What the gateway knows about itself for the settings page's runtime tab:
   * its state, the auth mode it reads requests under, how many primary agents
   * are configured and where its data lives. The version and the address the
   * browser reached are the browser's own. */
  runtimeInfo() {
    return api.get<RuntimeInfo>('/runtime').then((res) => res.data)
  },

  // Retention is the install's configuration, so it lives apart from the
  // per-project cron routes. null removes the key: the default applies.
  cronSettings() {
    return api.get<CronSettings>('/cron-settings').then((res) => res.data)
  },

  saveCronSettings(retentionDays: number | null) {
    return api
      .put<{ applied: boolean; path: string }>('/cron-settings', { retention_days: retentionDays })
      .then((res) => res.data)
  },

  cronJobs(projectId: string) {
    return api.get<{ agentId?: string; records: CronJobRecord[] }>(
      `/v1/projects/${encodeURIComponent(projectId)}/cron/jobs`,
    ).then((res) => (Array.isArray(res.data?.records) ? res.data.records : []))
  },

  /** One page of the project's scheduled-task table: searched over the name
   * and the prompt, in the order the server keeps them. */
  cronJobsPage(projectId: string, query: ListQuery) {
    return api
      .get<{ records?: CronJobRow[] } & ListPageMeta>(`/v1/projects/${encodeURIComponent(projectId)}/cron/jobs`, {
        params: listParams(query),
      })
      .then((res) => ({
        rows: Array.isArray(res.data?.records) ? res.data.records : [],
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
      }))
  },

  // The schedule preview is the engine's own parse, so what the builder shows
  // is exactly what the scheduler will later fire.
  cronPreview(projectId: string, schedule: string) {
    return postJson<CronPreviewResponse, { schedule: string }>(
      `/v1/projects/${encodeURIComponent(projectId)}/cron/preview`,
      { schedule },
    )
  },

  cronCreate(projectId: string, body: CronJobBody) {
    return postJson<CronJobRecord, CronJobBody>(
      `/v1/projects/${encodeURIComponent(projectId)}/cron/jobs`,
      body,
    )
  },

  cronUpdate(projectId: string, id: string, body: CronJobBody) {
    return api.put<CronJobRecord>(
      `/v1/projects/${encodeURIComponent(projectId)}/cron/jobs/${encodeURIComponent(id)}`,
      toSnakeCase(body),
    ).then((res) => res.data)
  },

  cronDelete(projectId: string, id: string) {
    return api.delete<{ deleted: string }>(
      `/v1/projects/${encodeURIComponent(projectId)}/cron/jobs/${encodeURIComponent(id)}`,
    ).then((res) => res.data)
  },

  cronRunNow(projectId: string, id: string) {
    return postJson<{ started: string }, Record<string, never>>(
      `/v1/projects/${encodeURIComponent(projectId)}/cron/jobs/${encodeURIComponent(id)}/run`,
      {},
    )
  },

  cronRuns(projectId: string, id: string, limit = 20) {
    return api.get<{ records: CronRunRecord[] }>(
      `/v1/projects/${encodeURIComponent(projectId)}/cron/jobs/${encodeURIComponent(id)}/runs`,
      { params: { limit } },
    ).then((res) => (Array.isArray(res.data?.records) ? res.data.records : []))
  },

  /** One page of a scheduled task's run history, newest fire first, counted on
   * the server. */
  cronRunsPage(projectId: string, id: string, query: ListQuery) {
    return api
      .get<{ runs?: CronRunRow[] } & ListPageMeta>(
        `/v1/projects/${encodeURIComponent(projectId)}/cron/jobs/${encodeURIComponent(id)}/runs`,
        { params: listParams(query) },
      )
      .then((res) => ({
        rows: Array.isArray(res.data?.runs) ? res.data.runs : [],
        total: Number(res.data?.total ?? 0),
        page: Number(res.data?.page ?? 1),
        pageSize: Number(res.data?.pageSize ?? query.pageSize),
      }))
  },

  /** Every heartbeat on the active agent's conversations, newest set up first. */
  heartbeats() {
    return api.get<{ records: HeartbeatRecord[] }>('/heartbeats')
      .then((res) => (Array.isArray(res.data?.records) ? res.data.records : []))
  },

  heartbeat(sessionId: string) {
    return api.get<{ heartbeat: HeartbeatRecord | null }>('/heartbeat', { params: { session_id: sessionId } })
      .then((res) => res.data?.heartbeat ?? null)
  },

  saveHeartbeat(body: { sessionId: string; intervalSeconds: number; prompt: string; paused?: boolean }) {
    return putRaw<HeartbeatRecord>('/heartbeat', {
      session_id: body.sessionId,
      interval_seconds: body.intervalSeconds,
      prompt: body.prompt,
      paused: Boolean(body.paused),
    })
  },

  clearHeartbeat(sessionId: string) {
    return api.delete<{ cleared: string }>('/heartbeat', { params: { session_id: sessionId } }).then((res) => res.data)
  },

  /**
   * Stops the continuation a session is waiting to run once a usage limit
   * resets. With agentId, it stops that subagent's own continuation instead of
   * the conversation's.
   */
  cancelAutoContinue(sessionId: string, agentId?: string) {
    const id = String(agentId ?? '').trim()
    const params: Record<string, string> = { session_id: sessionId }
    if (id) params.agent_id = id
    return api.delete<{ cancelled: boolean }>('/auto-continue', { params }).then((res) => res.data)
  },

  permissionsExplain(params: { toolName: string; input?: string; sessionId?: string }) {
    return api
      .get<PermissionExplainResponse>('/permissions/explain', {
        params: {
          tool_name: params.toolName,
          input: params.input ?? '',
          session_id: params.sessionId ?? '',
        },
      })
      .then((res) => res.data)
  },

  fileInfo(fileId: string) {
    return api.get<FileInfo>(`/files/${encodeURIComponent(fileId)}`).then((res) => res.data)
  },

  filesUpload(file: File, sessionId?: string, dir?: string): Promise<FileUploadResult> {
    const form = new FormData()
    form.append('file', file)
    if (sessionId) form.append('session_id', sessionId)
    if (dir) form.append('dir', dir)
    return gatewayFetch(`${API_BASE}/files`, {
      method: 'POST',
      body: form,
    }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return toCamelCase(await r.json()) as FileUploadResult
    })
  },
}

export default forebrainApi
