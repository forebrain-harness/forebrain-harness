// The host's own WebSocket to the gateway: the extension host as one more
// client of the chat endpoint, beside the Web UI's own connection inside the
// panel.
//
// It only ever observes and (for a command that sends a selection) starts a
// turn. Approval decisions do not go through here: the receipt of an
// `approve_action` op goes back only to the connection that sent it, and the
// REST endpoint answers in the same act, so the approval path is HTTP (see
// approvals.ts).
//
// The gateway keeps every connection's subscription independent, so this one
// and the panel's can be bound to the same session without either being
// dropped. One `SessionSocket` instance is one "connected" period: the
// `Connection` creates it when it becomes ready and closes it when it stops
// being ready. Within a period a dropped link reconnects by itself, resuming
// from the last sequence it saw.
import WebSocket from 'ws';

export const WS_PROTOCOL_VERSION = '2026-04-18';

const HANDSHAKE_TIMEOUT_MS = 10_000;
const REQUEST_TIMEOUT_MS = 15_000;
const FIRST_BACKOFF_MS = 500;
const MAX_BACKOFF_MS = 8_000;

export interface ServerMessage {
  op: string;
  [k: string]: unknown;
}

export type Listener = (msg: ServerMessage) => void;

export interface SessionSocketOptions {
  /** The gateway's http(s) origin. */
  readonly origin: string;
  /** Read on every (re)connect: the token may have been re-entered. */
  readonly getToken: () => string;
  readonly log: (line: string) => void;
  /** Every parsed frame, before this class looks at it. */
  readonly listener: Listener;
  /** Link transitions for the status bar: true on the `connected` frame, false when the socket closes. */
  readonly onLink?: (up: boolean) => void;
}

/** A `start_run` the gateway refused, with the code it refused with when it gave one. */
export class TurnRefusedError extends Error {
  constructor(
    message: string,
    readonly code: string,
  ) {
    super(message);
  }
}

interface Waiter<T> {
  readonly resolve: (value: T) => void;
  readonly reject: (err: Error) => void;
  readonly timer: NodeJS.Timeout;
}

function asObject(value: unknown): Record<string, unknown> {
  return typeof value === 'object' && value !== null ? (value as Record<string, unknown>) : {};
}

function str(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function wsUrl(origin: string): string {
  return `${origin.replace(/^http/, 'ws')}/ws/chat`;
}

export class SessionSocket {
  private socket?: WebSocket;
  private ready = false;
  private closed = false;
  private backoff = FIRST_BACKOFF_MS;
  private retry?: NodeJS.Timeout;
  private session?: string;
  private sequence = 0;
  private requestCounter = 0;
  private readonly opening: { resolve: () => void; reject: (err: Error) => void }[] = [];
  private readonly binds = new Map<string, Waiter<void>>();
  private readonly runs = new Map<string, Waiter<string>>();

  constructor(private readonly options: SessionSocketOptions) {}

  /** The session this socket is bound to (set as soon as `bind` is called, not when it is acknowledged). */
  get boundSession(): string | undefined {
    return this.session;
  }

  /** Highest `data.sequence` seen on a `run_event`: what a rebind resumes from. */
  get lastSequence(): number {
    return this.sequence;
  }

  /** Resolves once the server's `connected` frame has arrived. Rejects if called after `close()`. */
  open(): Promise<void> {
    if (this.closed) return Promise.reject(new Error('the session socket is closed'));
    if (this.ready) return Promise.resolve();
    const opened = new Promise<void>((resolve, reject) => this.opening.push({ resolve, reject }));
    if (!this.socket && !this.retry) this.connect();
    return opened;
  }

  /**
   * Binds to a session (re-binding another one is allowed) and resolves on
   * `session_bound` — not on the end of the history replay, which the server
   * sends after it.
   */
  bind(sessionId: string, cursor = 0): Promise<void> {
    if (this.closed) return Promise.reject(new Error('the session socket is closed'));
    if (!this.ready) return Promise.reject(new Error('the gateway link is not up'));
    this.session = sessionId;
    const requestId = this.nextRequestId();
    const bound = this.expect(this.binds, requestId);
    this.send({ op: 'bind_session', protocol_version: WS_PROTOCOL_VERSION, request_id: requestId, session_id: sessionId, cursor });
    return bound;
  }

  /**
   * Sends `start_run` and resolves with the run id on `run_started`; rejects on
   * `turn_withdrawn` / `turn_refused` / `run_error` for that request, or after
   * 15 s.
   */
  startRun(sessionId: string, content: string): Promise<string> {
    if (this.closed) return Promise.reject(new Error('the session socket is closed'));
    if (!this.ready) return Promise.reject(new Error('the gateway link is not up; send from the panel instead'));
    const requestId = this.nextRequestId();
    const started = this.expect(this.runs, requestId);
    this.send({
      op: 'start_run',
      protocol_version: WS_PROTOCOL_VERSION,
      request_id: requestId,
      session_id: sessionId,
      message: { content, role: 'user' },
    });
    return started;
  }

  /** Stops for good: no reconnect, and no further `open()`. Idempotent. */
  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    this.ready = false;
    if (this.retry) {
      clearTimeout(this.retry);
      this.retry = undefined;
    }
    const closing = new Error('the session socket is closed');
    this.abandon(closing);
    this.rejectOpening(closing);
    const socket = this.socket;
    this.socket = undefined;
    if (!socket) return;
    await new Promise<void>((resolve) => {
      socket.once('close', () => resolve());
      socket.on('error', () => {});
      socket.close();
    });
  }

  private nextRequestId(): string {
    this.requestCounter += 1;
    return `fs-${this.requestCounter}`;
  }

  private connect(): void {
    const url = wsUrl(this.options.origin);
    const socket = new WebSocket(url, {
      // No `origin`: the gateway upgrades a handshake that carries none
      // (gorilla's default CheckOrigin), and one that carries a browser origin
      // would be rejected. Authentication is the bearer token.
      headers: { authorization: `Bearer ${this.options.getToken()}` },
      handshakeTimeout: HANDSHAKE_TIMEOUT_MS,
    });
    this.socket = socket;
    socket.on('message', (raw: WebSocket.RawData) => this.receive(raw.toString()));
    socket.on('error', (err: Error) => this.options.log(`session socket: ${err.message}`));
    socket.on('close', () => this.lost(socket));
  }

  private lost(socket: WebSocket): void {
    // A socket this instance already let go of (close(), or a reconnect that
    // replaced it) has nothing left to say.
    if (socket !== this.socket) return;
    this.socket = undefined;
    this.ready = false;
    this.options.onLink?.(false);
    // Nothing waits for a dead link: a 15 s timeout would only delay the news.
    this.abandon(new Error('the gateway link closed'));
    if (this.closed) return;
    const delay = this.backoff;
    this.backoff = Math.min(this.backoff * 2, MAX_BACKOFF_MS);
    this.options.log(`session socket: reconnecting in ${delay} ms`);
    this.retry = setTimeout(() => {
      this.retry = undefined;
      if (!this.closed) this.connect();
    }, delay);
  }

  private receive(raw: string): void {
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch {
      this.options.log('session socket: dropped a frame that is not JSON');
      return;
    }
    if (typeof parsed !== 'object' || parsed === null) {
      this.options.log('session socket: dropped a frame that is not an object');
      return;
    }
    const message = parsed as ServerMessage;
    // The listener first: the reducer is the only reader that sees every frame.
    this.options.listener(message);
    const data = asObject(message.data);
    switch (message.op) {
      case 'connected':
        this.ready = true;
        this.backoff = FIRST_BACKOFF_MS;
        this.options.onLink?.(true);
        for (const waiter of this.opening.splice(0)) waiter.resolve();
        // A reconnect resumes the session it was bound to, from the last
        // sequence it saw; `bind` records the session before any wait.
        if (this.session !== undefined) {
          this.bind(this.session, this.sequence).catch((err: unknown) => {
            this.options.log(`session socket: rebind failed: ${err instanceof Error ? err.message : String(err)}`);
          });
        }
        return;
      case 'run_event': {
        const sequence = data.sequence;
        if (typeof sequence === 'number' && sequence > this.sequence) this.sequence = sequence;
        return;
      }
      case 'session_bound':
        this.settle(this.binds, str(message.request_id));
        return;
      case 'run_started':
        this.succeed(this.runs, str(message.request_id), str(message.run_id));
        return;
      case 'turn_withdrawn':
      case 'turn_refused':
      case 'run_error': {
        const error = str(message.error) || `the gateway refused the turn (${message.op})`;
        this.fail(this.runs, str(message.request_id), new TurnRefusedError(error, str(data.code)));
        return;
      }
      case 'run_warning':
        // The history snapshot this bind asked for is not available; waiting
        // for its replay would be waiting for frames that never come.
        if (str(message.message) === 'session_event_snapshot_unavailable') {
          this.rejectAll(this.binds, new Error('the session history snapshot is unavailable'));
        }
        return;
      default:
        return;
    }
  }

  private send(frame: Record<string, unknown>): void {
    const socket = this.socket;
    if (!socket || socket.readyState !== socket.OPEN) throw new Error('the gateway link is not up');
    socket.send(JSON.stringify(frame));
  }

  private expect<T>(waiters: Map<string, Waiter<T>>, requestId: string): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      const timer = setTimeout(() => {
        waiters.delete(requestId);
        reject(new Error('the gateway did not answer in time'));
      }, REQUEST_TIMEOUT_MS);
      waiters.set(requestId, { resolve, reject, timer });
    });
  }

  private settle<T>(waiters: Map<string, Waiter<T>>, requestId: string): void {
    this.take(waiters, requestId)?.resolve(undefined as T);
  }

  private succeed<T>(waiters: Map<string, Waiter<T>>, requestId: string, value: T): void {
    this.take(waiters, requestId)?.resolve(value);
  }

  private fail<T>(waiters: Map<string, Waiter<T>>, requestId: string, err: Error): void {
    this.take(waiters, requestId)?.reject(err);
  }

  private take<T>(waiters: Map<string, Waiter<T>>, requestId: string): Waiter<T> | undefined {
    const waiter = waiters.get(requestId);
    if (!waiter) return undefined;
    waiters.delete(requestId);
    clearTimeout(waiter.timer);
    return waiter;
  }

  /** Rejects every outstanding request of every kind. */
  private abandon(err: Error): void {
    this.rejectAll(this.binds, err);
    this.rejectAll(this.runs, err);
  }

  private rejectAll<T>(waiters: Map<string, Waiter<T>>, err: Error): void {
    for (const requestId of [...waiters.keys()]) this.fail(waiters, requestId, err);
  }

  private rejectOpening(err: Error): void {
    for (const waiter of this.opening.splice(0)) waiter.reject(err);
  }
}