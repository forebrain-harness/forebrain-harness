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
  workingSet?: string[]
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

/** The language-server snapshot the /lsp surfaces render. */
export async function lspSnapshot(sessionId?: string): Promise<LspSnapshot> {
  const params = sessionId ? { sessionId } : undefined
  const data = await api.get<Record<string, unknown>>('/v1/lsp', { params: toSnakeCase(params ?? {}) })
  const snap = toCamelCase(data.data) as unknown as LspSnapshot
  if (!Array.isArray(snap.servers)) snap.servers = []
  return snap
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
  archivedAt: number
  createdAt: number
  updatedAt: number
  trustRecorded?: boolean
  /** The persisted trust decision for this project's root (the detail read carries it). */
  trusted?: boolean
}

export interface ProjectSessionRecord {
  id: string
  title: string
  updatedAt: number
}

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

export interface ChatSessionsResponse {
  records: { id: string; title: string | null; createTime: string; updateTime: string; source?: string }[]
}

/** One conversation's own facts: its title, and the project it belongs to
 * when it belongs to one. */
export interface ChatSessionInfo {
  id: string
  title: string
  project: { id: string; name: string } | null
}

export interface SessionTodosResponse {
  items: { id: string; content: string; status: string; updatedAt: number }[]
}

export interface SessionModeResponse {
  mode: string
  phase?: string
  updatedAt?: number
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

export interface SessionRewindResult {
  status: string
  absPath?: string
  snapshot?: string
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
  source?: string
  trust?: string
  enabled: boolean
  /** Which layer owns this row: project, agent, shared, builtin, cross-tool. */
  origin?: SkillOrigin
  /** True when the row belongs to the layer the listing page manages. */
  editable?: boolean
  downloadUrl?: string
  /** Directories offering the same name that this row takes precedence over. */
  shadows?: string[]
  shadowedBy?: string[]
}

export interface SkillInspectResponse {
  skill: {
    name: string
    description: string
    source: string
    trust: string
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
  runId?: string
  elapsedSeconds?: number
  tokenCount?: number
  toolCount?: number
  fileCount?: number
}

export type AgentRosterResponse = {
  records: AgentRosterRow[]
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

  updatePrimaryAgent(id: string, body: { name?: string; description?: string }) {
    return putRaw<PrimaryAgentsResponse>(`/agents/primary/${encodeURIComponent(id)}`, body)
  },

  deletePrimaryAgent(id: string) {
    return api.delete<PrimaryAgentsResponse>(`/agents/primary/${encodeURIComponent(id)}`).then((res) => res.data)
  },

  agentRoster() {
    return api.get<AgentRosterResponse>('/agents/roster').then((res) => res.data)
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

  /** The agent's conversations of one purpose: '' the drawer lists,
   * 'workshop' the skill workshop lists. The filter is the server's. */
  chatSessions(source: '' | 'workshop' = '') {
    return api.get<ChatSessionsResponse>('/chat/sessions', {
      params: { source },
    }).then((res) => res.data)
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

  chatSessionCreate(title?: string, source?: '' | 'workshop') {
    return postJson<{ id: string; title: string }, { title: string; source?: string }>('/chat/sessions', {
      title: title ?? '',
      ...(source ? { source } : {}),
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

  sessionPlanMd(sessionId: string) {
    return api.get<{ markdown: string }>(`/chat/sessions/${sessionId}/plan-md`).then((res) => res.data)
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

  mentionSearch(q = '') {
    return api
      .get<MentionSearchResponse>('/workspace/mentions', { params: { q } })
      .then((res) => res.data)
  },

  mentionAccept(payload: {
    draft: string
    token_start: number
    token_end: number
    path: string
    is_dir: boolean
  }) {
    return api
      .post<MentionAcceptResponse>('/workspace/mentions/accept', payload)
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

  sessionRewindLast(sessionId: string) {
    return gatewayFetch(`/api/chat/sessions/${encodeURIComponent(sessionId)}/rewind-last`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
    }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return toCamelCase(await r.json()) as SessionRewindResult
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

  workspaceTree(path = '') {
    return api
      .get<WorkspaceTreeResponse>('/workspace/tree', { params: path ? { path } : undefined })
      .then((res) => res.data)
  },

  /** The tool table the active primary agent's runtime actually exposes. */
  toolsList() {
    return api.get<ToolMetaRecord[]>('/tools').then((res) => (Array.isArray(res.data) ? res.data : []))
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

  projectsList(params?: { search?: string; sort?: string; archived?: boolean; limit?: number; offset?: number }) {
    return api
      .get<{ projects?: ProjectRecord[] }>('/v1/projects', { params: toSnakeCase(params ?? {}) })
      .then((res) => (Array.isArray(res.data?.projects) ? res.data.projects : []))
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

  projectArchive(id: string, archived: boolean) {
    return postJson<{ ok: boolean }, { archived: boolean }>(`/v1/projects/${encodeURIComponent(id)}/archive`, { archived })
  },

  projectDelete(id: string) {
    return api.delete<{ ok: boolean }>(`/v1/projects/${encodeURIComponent(id)}`).then((res) => res.data)
  },

  projectSessions(id: string) {
    return api
      .get<{ sessions?: ProjectSessionRecord[] }>(`/v1/projects/${encodeURIComponent(id)}/sessions`)
      .then((res) => (Array.isArray(res.data?.sessions) ? res.data.sessions : []))
  },

  projectSessionCreate(id: string, title: string) {
    return postJson<{ id: string; title: string }, { title: string }>(`/v1/projects/${encodeURIComponent(id)}/sessions`, { title })
  },

  projectMcp(id: string) {
    return api.get<ProjectMcpRecord>(`/v1/projects/${encodeURIComponent(id)}/mcp`).then((res) => res.data)
  },

  projectMcpConsent(id: string, allow: string[]) {
    return postJson<{ ok: boolean }, { allow: string[] }>(`/v1/projects/${encodeURIComponent(id)}/mcp/consent`, { allow })
  },

  projectLsp(id: string) {
    return api.get<ProjectLspRecord>(`/v1/projects/${encodeURIComponent(id)}/lsp`).then((res) => res.data)
  },

  projectLspConsent(id: string, allow: string[]) {
    return postJson<{ ok: boolean; allowed: string[] }, { allow: string[] }>(`/v1/projects/${encodeURIComponent(id)}/lsp/consent`, { allow })
  },

  agentRuleFiles() {
    return api.get<{ files: Array<{ name: string; exists: boolean; sizeBytes?: number; updatedAt?: number }> }>('/rules/agent').then((res) => res.data)
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

  channels() {
    return api.get<{ agentId?: string; channels: Record<string, unknown> }>('/channels').then((res) => res.data)
  },

  saveChannels(channels: Record<string, unknown>) {
    return api.put<{ applied: boolean }>('/channels', toSnakeCase(channels)).then((res) => res.data)
  },

  providers() {
    return api.get<{ agentId?: string; providers: ProviderRecord[] }>('/providers')
      .then((res) => (Array.isArray(res.data?.providers) ? res.data.providers : []))
  },

  saveProviders(providers: Array<Record<string, unknown>>) {
    return putRaw<{ applied: boolean }>('/providers', { providers })
  },

  hooks() {
    return api.get<{ hooks: HooksSettingsRecord; knownTypes: string[]; events: string[] }>('/hooks')
      .then((res) => res.data)
  },

  saveHooks(hooks: HooksSettingsRecord) {
    return api.put<{ applied: boolean }>('/hooks', toSnakeCase({ hooks })).then((res) => res.data)
  },

  // Retention is the install's configuration, so these live apart from the
  // agent-scoped /cron routes. null removes the key: the default applies.
  cronSettings() {
    return api.get<CronSettings>('/cron-settings').then((res) => res.data)
  },

  saveCronSettings(retentionDays: number | null) {
    return api
      .put<{ applied: boolean; path: string }>('/cron-settings', { retention_days: retentionDays })
      .then((res) => res.data)
  },

  cronJobs(projectId?: string) {
    const query = projectId ? `?project_id=${encodeURIComponent(projectId)}` : ''
    return api.get<{ agentId?: string; records: CronJobRecord[] }>(`/cron${query}`)
      .then((res) => (Array.isArray(res.data?.records) ? res.data.records : []))
  },

  // The schedule preview is the engine's own parse, so what the builder shows
  // is exactly what the scheduler will later fire.
  cronPreview(schedule: string) {
    return postJson<CronPreviewResponse, { schedule: string }>('/cron/preview', { schedule })
  },

  cronCreate(body: CronJobBody) {
    return postJson<CronJobRecord, CronJobBody>('/cron', body)
  },

  cronUpdate(id: string, body: CronJobBody) {
    return api.put<CronJobRecord>(`/cron/${encodeURIComponent(id)}`, toSnakeCase(body)).then((res) => res.data)
  },

  cronDelete(id: string) {
    return api.delete<{ deleted: string }>(`/cron/${encodeURIComponent(id)}`).then((res) => res.data)
  },

  cronRunNow(id: string) {
    return postJson<{ started: string }, Record<string, never>>(`/cron/${encodeURIComponent(id)}/run`, {})
  },

  cronRuns(id: string, limit = 20) {
    return api.get<{ records: CronRunRecord[] }>(`/cron/${encodeURIComponent(id)}/runs`, { params: { limit } })
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

  filesUpload(file: File, sessionId?: string): Promise<FileUploadResult> {
    const form = new FormData()
    form.append('file', file)
    if (sessionId) form.append('session_id', sessionId)
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
