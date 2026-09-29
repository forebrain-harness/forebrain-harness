import axios, { type AxiosError } from 'axios'

import { toCamelCase, toSnakeCase } from './case'

export type AuthUser = { clientId: string; lastSessionId?: string | null }

let authUser: AuthUser | null = null
let authToken: string | null = null

export function getUser(): AuthUser | null {
  return authUser
}

export function setUser(u: AuthUser | null) {
  authUser = u
}

export function getToken(): string | null {
  return authToken
}

export function setToken(t: string | null) {
  authToken = t
}

export function getErrorMessage(error: unknown): string {
  const err = error as AxiosError
  if (err?.response?.data != null) {
    const data = err.response.data
    if (typeof data === 'string') return data
    if (typeof data === 'object' && data !== null && 'message' in data) {
      return String((data as { message?: unknown }).message) || 'Request failed'
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
  runStartedAt?: string
  runFinishedAt?: string
  workedDurationMs?: number
  /** A finished compaction the server placed in the history; its row role is "compaction". */
  compaction?: Record<string, unknown> | null
  /** A line of a /goal the server placed in the history; its row role is "goal". */
  goal?: Record<string, unknown> | null
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

export interface PermissionRuleRecord {
  source: string
  behavior: string
  toolName: string
  ruleContent?: string
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
  runCount?: number
  nextRunAt?: number
  lastRunAt?: number
  lastStatus?: string
  lastError?: string
  lastOutput?: string
  failureStreak?: number
  createdAt?: number
  updatedAt?: number
}

export interface CronRunRecord {
  id: number
  jobId: string
  sessionId?: string
  trigger?: string
  status: string
  output?: string
  error?: string
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

export interface ProviderRecord {
  provider?: string
  model?: string
  apiKey?: string
  baseUrl?: string
  apiPath?: string
}

export interface PermissionUpdateBody {
  type: 'addRules' | 'replaceRules' | 'removeRules' | 'setMode'
  destination: string
  behavior?: string
  rules?: { toolName: string; ruleContent?: string; commandPrefix?: string[]; bypassSandbox?: boolean }[]
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

export interface ChatSessionsResponse {
  records: { id: string; title: string | null; createTime: string; updateTime: string }[]
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


export interface SessionCostSummary {
  sessionId: string
  toolCalls: number
  byTool: Record<string, number>
  note?: string
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
  path: string
  isDir: boolean
}

export interface WorkspaceTreeResponse {
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

export interface SkillRecord {
  name: string
  description: string
  allowedTools?: string
  rootPath?: string
  source?: string
  trust?: string
  enabled: boolean
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

api.interceptors.request.use((config) => {
  const t = getToken()
  if (t) {
    config.headers = config.headers ?? {}
    config.headers.Authorization = `Bearer ${t}`
  }
  return config
})

api.interceptors.response.use((response) => {
  response.data = toCamelCase(response.data)
  return response
})

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

  chatSessions(current = 1, size = 100) {
    return api.get<ChatSessionsResponse>('/chat/sessions', {
      params: { current, size },
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

  chatSessionCreate(title?: string) {
    return postJson<{ id: string; title: string }, { title: string }>('/chat/sessions', { title: title ?? '' })
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

  slashCommands(surface = 'webchat', q = '', opts?: { duringRun?: boolean; sideConversation?: boolean }) {
    return api.get<SlashCommandsResponse>('/slash/commands', {
      params: {
        surface,
        q,
        ...(opts?.duringRun ? { during_run: 1 } : {}),
        ...(opts?.sideConversation ? { side: 1 } : {}),
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

  runInput(runId: string, body: { message: string; attachments?: string[] }) {
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

  sessionRewindLast(sessionId: string) {
    return fetch(`/api/chat/sessions/${encodeURIComponent(sessionId)}/rewind-last`, {
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

  resetMemories() {
    return postJson<{ ok: boolean }, Record<string, never>>('/memories/reset', {})
  },

  actionsList(status?: string, sessionId?: string, agentId?: string) {
    const params = new URLSearchParams()
    if (status) params.set('status', status)
    if (sessionId) params.set('session_id', sessionId)
    if (agentId) params.set('agent_id', agentId)
    const query = params.toString()
    return fetch(`/api/actions${query ? `?${query}` : ''}`, {
      method: 'GET',
    }).then(async (r) => {
      if (!r.ok) throw new GatewayHttpError(r.status, (await r.text()).trim())
      return toCamelCase(await r.json()) as ActionRecord[]
    })
  },

  actionsAnswer(id: string, body: ActionAnswerBody) {
    return fetch(`/api/actions/${encodeURIComponent(id)}/answer`, {
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
    return fetch(`/api/actions/${encodeURIComponent(id)}/approve`, {
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
    return fetch(`/api/actions/${encodeURIComponent(id)}/deny`, {
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

  workspaceTree() {
    return api
      .get<WorkspaceTreeResponse>('/workspace/tree')
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

  saveProviders(providers: ProviderRecord[]) {
    return api.put<{ applied: boolean }>('/providers', toSnakeCase({ providers })).then((res) => res.data)
  },

  hooks() {
    return api.get<{ hooks: HooksSettingsRecord; knownTypes: string[]; events: string[] }>('/hooks')
      .then((res) => res.data)
  },

  saveHooks(hooks: HooksSettingsRecord) {
    return api.put<{ applied: boolean }>('/hooks', toSnakeCase({ hooks })).then((res) => res.data)
  },

  cronJobs() {
    return api.get<{ agentId?: string; records: CronJobRecord[] }>('/cron')
      .then((res) => (Array.isArray(res.data?.records) ? res.data.records : []))
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

  /** Stops the continuation a session is waiting to run once a usage limit resets. */
  cancelAutoContinue(sessionId: string) {
    return api.delete<{ cancelled: boolean }>('/auto-continue', { params: { session_id: sessionId } }).then((res) => res.data)
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

  authLogin(username: string, _password: string): Promise<string> {
    const id = (username ?? '').trim() || 'user'
    const tok = `local-${id}`
    setToken(tok)
    setUser({ clientId: id })
    return Promise.resolve(tok)
  },

  authRegister(username: string, password: string): Promise<string> {
    return forebrainApi.authLogin(username, password)
  },

  authCurrent(): Promise<AuthUser | null> {
    return Promise.resolve(getUser())
  },

  authLogout(): Promise<void> {
    setToken(null)
    setUser(null)
    return Promise.resolve()
  },

  fileInfo(fileId: string) {
    return api.get<FileInfo>(`/files/${encodeURIComponent(fileId)}`).then((res) => res.data)
  },

  filesUpload(file: File, sessionId?: string): Promise<FileUploadResult> {
    const form = new FormData()
    form.append('file', file)
    if (sessionId) form.append('session_id', sessionId)
    const headers: Record<string, string> = {}
    const t = getToken()
    if (t) {
      headers.Authorization = `Bearer ${t}`
    }
    return fetch(`${API_BASE}/files`, {
      method: 'POST',
      headers,
      body: form,
    }).then(async (r) => {
      if (!r.ok) throw new Error(await r.text())
      return toCamelCase(await r.json()) as FileUploadResult
    })
  },
}

export default forebrainApi
