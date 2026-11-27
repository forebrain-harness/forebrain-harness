// The RPC surface the native chat webview talks to (plan vscode-native-style
// chat workbench, Steps 2–3). The webview never sees the gateway origin's
// token and holds no session cookie: every REST call and every chat WebSocket
// it needs is proxied here, with the bearer header added on this side only.
// Event reduction stays in the webview on the parsers it already shares with
// the SPA — this file only carries bytes, batches them, and answers the few
// questions a webview cannot answer for itself (trust, file ranges, which
// conversation it is on).
//
// Frame vocabulary, webview → host:
//   {type:'rpc', rpc:<id>, method, params}      one whitelisted call
//   {type:'rpcCancel', rpc:<id>}                abandon a call still in flight
// Host → webview:
//   {type:'rpc', rpc:<id>, ok, result?, error?} the answer to one call
//   {type:'wsFrame', socket:<id>, frames:[…]}   chat frames, batched per turn
//   {type:'wsState', socket:<id>, up, error?}   link transitions of one socket
import { openableDeliverables, fileRangeRequestOf } from './deliverableGate';
import { redact } from './token';
import WebSocket from 'ws';

export type FetchLike = (input: string, init: RequestInit) => Promise<Response>;

/** The slice of the `ws` socket this class drives (kept narrow for tests). */
export interface RpcSocket {
  send(data: string): void;
  close(): void;
  on(event: 'message', listener: (raw: unknown) => void): void;
  on(event: 'open', listener: () => void): void;
  on(event: 'close', listener: () => void): void;
  on(event: 'error', listener: (err: Error) => void): void;
}

export type RpcSocketFactory = (url: string, headers: Record<string, string>) => RpcSocket;

/** The strings the webview sends with a trust question; the host's modal shows them verbatim. */
export interface TrustStrings {
  readonly title: string;
  readonly body: string;
  readonly confirm: string;
  readonly once: string;
  readonly cancel: string;
}

export interface WebviewRpcDeps {
  /** postMessage to the webview this dispatcher serves. */
  readonly post: (message: unknown) => void;
  /** The gateway origin; paths arrive starting with `/api/`. */
  readonly origin: string;
  /** Read per request: the token may have been re-entered. */
  readonly token: () => string;
  readonly fetchImpl: FetchLike;
  readonly log: (line: string) => void;
  /** The trust boundary, from the same pure gate delivered files answer to. */
  readonly workspaceRoots: () => readonly string[];
  readonly workspaceTrusted: () => boolean;
  /** Writes the trust record the terminal writes: `POST /api/host/trust`. */
  readonly trustSet: (root: string, trusted: boolean) => Promise<void>;
  /** The native modal; resolves with the picked item, or undefined on dismissal. */
  readonly showWarningModal: (
    message: string,
    detail: string,
    confirmLabel: string,
    cancelLabel: string,
  ) => Promise<string | undefined>;
  /** The session the webview just bound: what the relay's request log used to sniff. */
  readonly bindSession: (sessionId: string) => Promise<void>;
  /** Opens one conversation in the chat view and reveals it — the Sessions
   *  view's pick. */
  readonly openChatSession: (sessionId: string) => Promise<void>;
  /** Records which conversation the chat view is on, so the Sessions view can
   *  mark the row. */
  readonly reportActiveSession: (sessionId: string) => Promise<void>;
  /** The proxied gateway call's ceiling; the default is 60s and the tests
   *  shrink it. */
  readonly httpTimeoutMs?: number;
  /** Opens a workspace file at a line range — `Deliverables.openRange`, unchanged. */
  readonly openFileRange: (request: unknown) => Promise<void>;
  /** Injected so the tests hand over a fake socket. */
  readonly socketFactory?: RpcSocketFactory;
}

interface Pending {
  readonly abort: AbortController;
}

interface BufferedSocket {
  readonly socket: RpcSocket;
  /** Frames that arrived since the last flush: one postMessage carries them all, in order. */
  batch: string[];
  flushScheduled: boolean;
  up: boolean;
}

/** Which of the three trust branches a picked folder lands in. The order is
 *  the verdict's safety: an untrusted workspace refuses before any boundary is
 *  even read, and the boundary is the same one delivered files go through —
 *  a real path-separator boundary, so `/a/proj` never matches `/a/project`. */
export type TrustBranch = 'restricted' | 'inside' | 'outside';

export function trustBranchOf(
  root: string,
  workspaceRoots: readonly string[],
  workspaceTrusted: boolean,
): TrustBranch {
  if (!workspaceTrusted) return 'restricted';
  const inside = openableDeliverables([{ path: root, title: '' }], workspaceRoots).length > 0;
  return inside ? 'inside' : 'outside';
}

/** Response headers the webview may see: the ones a page reads, nothing that
 *  could carry credentials or hop-by-hop noise. */
const RESPONSE_HEADER_ALLOWLIST = new Set(['content-type', 'content-length', 'content-disposition']);

/** How long one proxied gateway call may take before the webview is told the
 *  request failed. Long enough for an attachment upload, short enough that a
 *  dead gateway never leaves a panel hanging. */
const HTTP_TIMEOUT_MS = 60_000;

/** Request headers a webview may set; the bearer header is never one of them. */
function clientHeaders(headers: unknown): Record<string, string> {
  const result: Record<string, string> = {};
  if (typeof headers !== 'object' || headers === null) return result;
  for (const [name, value] of Object.entries(headers as Record<string, unknown>)) {
    const lower = name.toLowerCase();
    if (lower !== 'authorization' && lower !== 'cookie' && typeof value === 'string') {
      result[name] = value;
    }
  }
  return result;
}

export class WebviewRpc {
  private readonly pending = new Map<number, Pending>();
  private readonly sockets = new Map<number, BufferedSocket>();
  private socketCounter = 0;

  constructor(private readonly deps: WebviewRpcDeps) {}

  /** One message from the webview. Never throws: a bad frame is logged, not fatal. */
  onMessage(raw: unknown): void {
    if (typeof raw !== 'object' || raw === null) return;
    const frame = raw as { type?: unknown; rpc?: unknown; method?: unknown; params?: unknown };
    if (frame.type === 'rpcCancel' && typeof frame.rpc === 'number') {
      this.pending.get(frame.rpc)?.abort.abort();
      return;
    }
    if (frame.type !== 'rpc' || typeof frame.rpc !== 'number' || typeof frame.method !== 'string') return;
    void this.handleRpc(frame.rpc, frame.method, frame.params);
  }

  /** Dispatches one whitelisted call; anything else answers with an error. */
  async handleRpc(rpc: number, method: string, params: unknown): Promise<void> {
    try {
      const result = await this.call(method, params, rpc);
      this.deps.post({ type: 'rpc', rpc, ok: true, result });
    } catch (err) {
      const message = redact(err instanceof Error ? err.message : String(err), this.deps.token());
      this.deps.post({ type: 'rpc', rpc, ok: false, error: message });
    } finally {
      this.pending.delete(rpc);
    }
  }

  private async call(method: string, params: unknown, rpc: number): Promise<unknown> {
    switch (method) {
      case 'http':
        return this.httpRequest(params, rpc);
      case 'ws.open':
        return this.openSocket();
      case 'ws.send':
        return this.socketSend(params);
      case 'ws.close':
        return this.closeSocket(params);
      case 'bind':
        return this.bind(params);
      case 'openSession':
        return this.openChatSession(params);
      case 'reportActiveSession':
        return this.reportActiveSession(params);
      case 'trust.ask':
        return this.askTrust(params);
      case 'openFileRange':
        return this.fileRange(params);
      default:
        throw new Error(`unknown rpc method: ${method}`);
    }
  }

  /**
   * The REST proxy. Every gateway call the webview can make lives under
   * `/api/` — the axios instance's baseURL is `/api` and the fetch helpers
   * write it literally — so the prefix check is what bounds where the bearer
   * header travels: a path outside it is refused before anything dials, and
   * the credentials go nowhere. The log carries the path without its query,
   * an abort from an `rpcCancel` unwinds the fetch, and a gateway that never
   * answers is cut off at `HTTP_TIMEOUT_MS` — the webview waits bounded, and
   * its surfaces say the request failed instead of hanging.
   */
  private async httpRequest(params: unknown, rpc: number): Promise<unknown> {
    if (typeof params !== 'object' || params === null) throw new Error('http: params required');
    const request = params as {
      method?: unknown;
      path?: unknown;
      headers?: unknown;
      body?: unknown;
      binary?: unknown;
    };
    const verb = typeof request.method === 'string' ? request.method.toUpperCase() : '';
    const path = typeof request.path === 'string' ? request.path : '';
    if (!['GET', 'POST', 'PUT', 'DELETE', 'HEAD', 'PATCH'].includes(verb) || !path.startsWith('/api/')) {
      throw new Error(`http: ${verb} ${path.split('?')[0]} is not a gateway path (expected /api/…)`);
    }
    const abort = new AbortController();
    this.pending.set(rpc, { abort });
    const token = this.deps.token();
    const res = await this.deps.fetchImpl(`${this.deps.origin}${path}`, {
      method: verb,
      headers: { ...clientHeaders(request.headers), ...(token ? { authorization: `Bearer ${token}` } : {}) },
      body: request.body === undefined ? undefined : (request.body as string | ArrayBuffer),
      // `any` covers both directions: an `rpcCancel` unwinds the fetch at
      // once, and a gateway that never answers is cut off at the timeout.
      signal: AbortSignal.any([abort.signal, AbortSignal.timeout(this.deps.httpTimeoutMs ?? HTTP_TIMEOUT_MS)]),
    });
    const headers: Record<string, string> = {};
    res.headers.forEach((value, name) => {
      if (RESPONSE_HEADER_ALLOWLIST.has(name.toLowerCase())) headers[name] = value;
    });
    this.deps.log(`webview rpc: ${verb} ${path.split('?')[0]} -> ${res.status}`);
    const binary = request.binary === true;
    return {
      status: res.status,
      statusText: res.statusText,
      headers,
      body: binary ? await res.arrayBuffer() : await res.text(),
    };
  }

  /** One chat WebSocket, dialed here with the bearer header and no Origin —
   *  the handshake the gateway accepts from a host client (see SessionSocket). */
  private openSocket(): { socket: number } {
    const id = ++this.socketCounter;
    const token = this.deps.token();
    const factory = this.deps.socketFactory ?? defaultSocketFactory;
    const socket = factory(`${this.deps.origin.replace(/^http/, 'ws')}/ws/chat`, {
      ...(token ? { authorization: `Bearer ${token}` } : {}),
    });
    const buffered: BufferedSocket = { socket, batch: [], flushScheduled: false, up: false };
    this.sockets.set(id, buffered);
    socket.on('message', (raw: unknown) => this.queueFrame(id, buffered, typeof raw === 'string' ? raw : String(raw)));
    socket.on('open', () => {
      buffered.up = true;
      this.deps.post({ type: 'wsState', socket: id, up: true });
    });
    socket.on('close', () => {
      buffered.up = false;
      this.flush(id, buffered);
      this.deps.post({ type: 'wsState', socket: id, up: false });
    });
    socket.on('error', (err: Error) => {
      this.deps.log(`webview rpc socket ${id}: ${redact(err.message, this.deps.token())}`);
    });
    return { socket: id };
  }

  private socketSend(params: unknown): void {
    if (typeof params !== 'object' || params === null) throw new Error('ws.send: params required');
    const request = params as { socket?: unknown; data?: unknown };
    if (typeof request.data !== 'string') throw new Error('ws.send: string data required');
    this.sockets.get(typeof request.socket === 'number' ? request.socket : -1)?.socket.send(request.data);
  }

  private closeSocket(params: unknown): void {
    if (typeof params !== 'object' || params === null) return;
    const request = params as { socket?: unknown };
    const id = typeof request.socket === 'number' ? request.socket : -1;
    const buffered = this.sockets.get(id);
    if (!buffered) return;
    this.sockets.delete(id);
    this.flush(id, buffered);
    try {
      buffered.socket.close();
    } catch {
      // A socket that never opened has nothing to close.
    }
  }

  /**
   * Coalesces the frames a burst produced into one postMessage, in arrival
   * order. The first frame of a turn schedules the flush; everything the same
   * turn delivers joins it. A 5k-event replay therefore crosses the boundary
   * as one message, and nothing is reordered or dropped on the way.
   */
  private queueFrame(id: number, buffered: BufferedSocket, data: string): void {
    buffered.batch.push(data);
    if (buffered.flushScheduled) return;
    buffered.flushScheduled = true;
    setImmediate(() => this.flush(id, buffered));
  }

  private flush(id: number, buffered: BufferedSocket): void {
    buffered.flushScheduled = false;
    if (buffered.batch.length === 0) return;
    const frames = buffered.batch;
    buffered.batch = [];
    this.deps.post({ type: 'wsFrame', socket: id, frames });
  }

  /** The webview bound a conversation: the host follows it there, the way it
   *  used to follow the relay's request log. */
  private bind(params: unknown): Promise<void> {
    if (typeof params !== 'object' || params === null) throw new Error('bind: params required');
    const sessionId = String((params as { sessionId?: unknown }).sessionId ?? '').trim();
    if (!sessionId) throw new Error('bind: a session id is required');
    return this.deps.bindSession(sessionId);
  }

  /** The Sessions view picked a conversation: open it in the chat view. An
   *  empty id opens the chat view on its fresh-chat page (the "new chat"
   *  button), with nothing to steer. */
  private openChatSession(params: unknown): Promise<void> {
    if (typeof params !== 'object' || params === null) throw new Error('openSession: params required');
    const sessionId = String((params as { sessionId?: unknown }).sessionId ?? '').trim();
    return this.deps.openChatSession(sessionId);
  }

  /** The chat view reports the conversation on screen. */
  private reportActiveSession(params: unknown): Promise<void> {
    if (typeof params !== 'object' || params === null) throw new Error('reportActiveSession: params required');
    const sessionId = String((params as { sessionId?: unknown }).sessionId ?? '').trim();
    if (!sessionId) throw new Error('reportActiveSession: a session id is required');
    return this.deps.reportActiveSession(sessionId);
  }

  /**
   * The trust question, in three branches. Branch one writes the record the
   * terminal writes and resolves even when that write fails — the prompt is an
   * added gate, not the mechanism. Branch two asks in a native modal with the
   * strings the webview carries (they are its locale's words). Branch three
   * refuses without asking. A dismissal answers no, never yes.
   */
  private async askTrust(params: unknown): Promise<boolean> {
    if (typeof params !== 'object' || params === null) throw new Error('trust.ask: params required');
    const request = params as { root?: unknown; strings?: unknown };
    const root = typeof request.root === 'string' ? request.root : '';
    const strings = this.trustStringsOf(request.strings);
    const branch = trustBranchOf(root, this.deps.workspaceRoots(), this.deps.workspaceTrusted());
    this.deps.log(`webview rpc: trust ${branch} ${root}`);
    if (branch === 'restricted') return false;
    if (branch === 'outside') {
      const answer = await this.deps.showWarningModal(
        `${strings.title}\n${strings.body}`,
        `${root}\n${strings.once}`,
        strings.confirm,
        strings.cancel,
      );
      if (answer !== strings.confirm) return false;
    }
    try {
      await this.deps.trustSet(root, true);
    } catch (err) {
      // A failure to record the decision still lets the send proceed: the
      // prompt is an added gate, not the mechanism (the web's confirmTrust
      // catches the same way).
      this.deps.log(
        `webview rpc: trust record failed, letting the send proceed: ${redact(
          err instanceof Error ? err.message : String(err),
          this.deps.token(),
        )}`,
      );
    }
    return true;
  }

  private trustStringsOf(value: unknown): TrustStrings {
    const strings = (typeof value === 'object' && value !== null ? value : {}) as Record<string, unknown>;
    const str = (key: string) => (typeof strings[key] === 'string' ? (strings[key] as string) : '');
    return { title: str('title'), body: str('body'), confirm: str('confirm'), once: str('once'), cancel: str('cancel') };
  }

  /** The same shape check the panel's iframe path runs, then the same opener. */
  private async fileRange(params: unknown): Promise<null> {
    const request = fileRangeRequestOf(params);
    if (!request) throw new Error('openFileRange: not a file range request');
    await this.deps.openFileRange(request);
    return null;
  }

  /** The webview went away: drop its sockets so nothing pumps into the void. */
  dispose(): void {
    for (const [id, buffered] of [...this.sockets]) {
      this.sockets.delete(id);
      this.flush(id, buffered);
      try {
        buffered.socket.close();
      } catch {
        // A socket that never opened has nothing to close.
      }
    }
  }
}

function defaultSocketFactory(url: string, headers: Record<string, string>): RpcSocket {
  // No `origin` on the handshake: the gateway upgrades one that carries none,
  // and authentication is the bearer header (SessionSocket dials the same way).
  return new WebSocket(url, { headers }) as unknown as RpcSocket;
}
