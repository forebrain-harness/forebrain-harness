// What the native surfaces (checklist tree, status bar, approval notification,
// delivery notification) show, folded out of the frames the host's own
// WebSocket receives.
//
// Pure: no `vscode`, no sockets, no clock. Every field of every frame is
// optional from this side's point of view — a missing field takes its default
// and nothing here throws — because the frames come from a gateway that may be
// a version ahead of this reducer.
//
// Two channels feed it, and the reducer takes both: `run_event` frames, which
// every bound connection receives (the observer path: the host socket watches a
// session the panel is running), and the initiator-only ops
// (`requires_action`, `action_*`, `run_completed|cancelled|error`), which the
// gateway writes only to the connection that started the run.

export interface PlanItem {
  id: string;
  content: string;
  status: string;
  active: string;
}

export interface Checklist {
  /** Roster key of the agent whose list this is; 'primary' when the payload's agent_id is empty. */
  readonly agent: string;
  readonly title: string;
  readonly explanation: string;
  readonly completed: number;
  readonly total: number;
  readonly items: readonly PlanItem[];
}

export interface PendingApproval {
  readonly actionId: string;
  readonly runId: string;
  readonly kind: string; // data.kind ?? data.action_kind ?? ''
  readonly toolName: string; // data.permission_suggestion?.permission_tool_name ?? ''
  readonly agent: string; // data.agent_id ?? ''
  /** `permission_suggestion.permission_input`, for the notification's second line. */
  readonly permissionInput: unknown;
}

export interface DeliveredFile {
  readonly path: string;
  readonly title: string;
}

export interface RunEnd {
  readonly op: 'run_completed' | 'run_cancelled' | 'run_error';
  readonly error: string;
  readonly planDone: number;
  readonly planTotal: number;
}

export interface SessionState {
  readonly runActive: boolean;
  readonly toolsInFlight: number;
  readonly subagents: number;
  readonly checklists: readonly Checklist[]; // primary first, then by agent key
  readonly pending: readonly PendingApproval[]; // by actionId, insertion order
  readonly lastDelivery: { readonly runId: string; readonly files: readonly DeliveredFile[] } | null;
  readonly lastRunEnd: RunEnd | null;
}

export const EMPTY_SESSION_STATE: SessionState = {
  runActive: false,
  toolsInFlight: 0,
  subagents: 0,
  checklists: [],
  pending: [],
  lastDelivery: null,
  lastRunEnd: null,
};

type Json = Record<string, unknown>;

function asObject(value: unknown): Json {
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? (value as Json) : {};
}

function asArray(value: unknown): readonly unknown[] {
  return Array.isArray(value) ? value : [];
}

function str(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function num(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0;
}

/** Applies one server message (already JSON-parsed). Returns the same object when nothing changed. */
export function reduceSessionMessage(state: SessionState, msg: unknown): SessionState {
  const message = asObject(msg);
  const data = asObject(message.data);
  switch (str(message.op)) {
    case 'run_event':
      return reduceRunEvent(state, data);
    case 'requires_action':
      return addPending(state, approvalOf(data, str(message.run_id)));
    case 'action_approved':
    case 'action_denied':
    case 'action_cancelled':
      return dropPending(state, str(data.action_id) || str(message.action_id));
    case 'run_completed':
    case 'run_cancelled':
    case 'run_error': {
      const op = str(message.op);
      return endRun(state, str(message.run_id), op as RunEnd['op'], message);
    }
    case 'turn_withdrawn':
    case 'turn_refused':
      // Neither carries a run: the turn never started.
      return state.runActive ? { ...state, runActive: false } : state;
    default:
      return state;
  }
}

function reduceRunEvent(state: SessionState, data: Json): SessionState {
  switch (str(data.type)) {
    case 'turn_started':
      return { ...state, runActive: true, lastRunEnd: null };
    case 'turn_completed':
    case 'turn_cancelled':
    case 'turn_error': {
      const op = `run_${str(data.type).slice(5)}` as RunEnd['op'];
      return endRun(state, str(data.run_id), op, asObject(data.payload));
    }
    case 'turn_failed':
      return { ...state, runActive: false, toolsInFlight: 0 };
    case 'tool_call_started':
      return { ...state, toolsInFlight: state.toolsInFlight + 1 };
    case 'tool_call_completed':
      return { ...state, toolsInFlight: Math.max(0, state.toolsInFlight - 1) };
    case 'subagent_spawned':
      return { ...state, subagents: state.subagents + 1 };
    case 'subagent_ended':
      return { ...state, subagents: Math.max(0, state.subagents - 1) };
    case 'plan_updated':
      return { ...state, checklists: withChecklist(state.checklists, checklistOf(asObject(data.payload))) };
    case 'approval_requested':
      // The observer's main path: the same fields a requires_action carries,
      // and the run id off the event.
      return addPending(state, approvalOf(asObject(data.payload), str(data.run_id)));
    default:
      // mode_changed (the panel already shows the mode) and anything this
      // extension does not consume.
      return state;
  }
}

/**
 * A run ended. The same run can be announced twice — the `turn_*` event on the
 * observer path and the initiator-only op on the other — so a delivery already
 * recorded for this run id is kept rather than notified again.
 */
function endRun(state: SessionState, runId: string, op: RunEnd['op'], source: Json): SessionState {
  const files = filesOf(source.artifacts);
  const lastDelivery =
    files.length > 0 && state.lastDelivery?.runId !== runId ? { runId, files } : state.lastDelivery;
  return {
    ...state,
    runActive: false,
    toolsInFlight: 0,
    // The run is over, so every approval it was waiting on is void.
    pending: [],
    lastRunEnd: { op, error: str(source.error), planDone: num(source.plan_done), planTotal: num(source.plan_total) },
    lastDelivery,
  };
}

function filesOf(artifacts: unknown): readonly DeliveredFile[] {
  return asArray(artifacts).map((entry) => {
    const artifact = asObject(entry);
    return { path: str(artifact.path), title: str(artifact.title) || str(artifact.name) };
  });
}

function checklistOf(payload: Json): Checklist {
  return {
    agent: str(payload.agent_id) || 'primary',
    title: str(payload.title),
    explanation: str(payload.explanation),
    completed: num(payload.completed),
    total: num(payload.total),
    items: asArray(payload.items).map((entry) => {
      const item = asObject(entry);
      return { id: str(item.id), content: str(item.content), status: str(item.status), active: str(item.active) };
    }),
  };
}

function withChecklist(checklists: readonly Checklist[], next: Checklist): readonly Checklist[] {
  const kept = checklists.filter((c) => c.agent !== next.agent);
  return [...kept, next].sort((a, b) => {
    if (a.agent === b.agent) return 0;
    if (a.agent === 'primary') return -1;
    if (b.agent === 'primary') return 1;
    return a.agent < b.agent ? -1 : 1;
  });
}

function approvalOf(source: Json, runId: string): PendingApproval {
  const suggestion = asObject(source.permission_suggestion);
  return {
    actionId: str(source.action_id) || str(source.id),
    runId,
    kind: str(source.kind) || str(source.action_kind),
    toolName: str(suggestion.permission_tool_name),
    agent: str(source.agent_id),
    permissionInput: suggestion.permission_input,
  };
}

function addPending(state: SessionState, approval: PendingApproval): SessionState {
  if (state.pending.some((p) => p.actionId === approval.actionId)) return state;
  return { ...state, pending: [...state.pending, approval] };
}

function dropPending(state: SessionState, actionId: string): SessionState {
  const pending = state.pending.filter((p) => p.actionId !== actionId);
  return pending.length === state.pending.length ? state : { ...state, pending };
}

/** Removes every approval whose action_id is no longer in `pendingIds` (the REST reconcile). */
export function reconcilePending(state: SessionState, pendingIds: readonly string[]): SessionState {
  const live = new Set(pendingIds);
  const pending = state.pending.filter((p) => live.has(p.actionId));
  return pending.length === state.pending.length ? state : { ...state, pending };
}

/**
 * The progress the status bar shows: null when no checklist exists.
 *
 * The active title follows the harness's own rule (`PlanProgressOf`): among the
 * items in progress, the shortest label wins, the item's own `active` label
 * before its content, ties keeping the first.
 */
export function checklistProgress(state: SessionState): { done: number; total: number; active: string } | null {
  const primary = state.checklists.find((c) => c.agent === 'primary');
  if (!primary) return null;
  return { done: primary.completed, total: primary.total, active: activeTitle(primary.items) };
}

function activeTitle(items: readonly PlanItem[]): string {
  let best = '';
  for (const item of items) {
    if (item.status.trim() !== 'in_progress') continue;
    const title = item.active.trim() || item.content.trim();
    if (title === '') continue;
    if (best === '' || title.length < best.length) best = title;
  }
  return best;
}