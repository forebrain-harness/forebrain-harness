import { resolveGatewayToken, redact, TOKEN_KEY, type OwnedGateway, type SecretStore } from './token';
import type { GatewayConfig } from './gatewayOrigin';
import type { PanelState } from './panelHtml';

export interface ConnectionDeps {
  readonly config: GatewayConfig;
  readonly secrets: SecretStore;
  readonly env: NodeJS.ProcessEnv;
  readonly probe: (token: string) => Promise<'ok' | 'rejected' | 'unreachable'>;
  readonly healthy: () => Promise<boolean>;
  /** Starts a gateway, or resolves null when not attempted or when it failed (after logging why). */
  readonly startGateway: () => Promise<{ token: string | null; origin: string; stop(): Promise<void> } | null>;
  readonly log: (line: string) => void;
  readonly emit: (state: PanelState) => void;
}

/**
 * The host's hold on a gateway: a token that probes ok, and the gateway child
 * this install started when none was answering. The panel reaches the gateway
 * through the RPC proxy with this token; nothing is relayed and no request is
 * sniffed — the conversation on screen is the one the webview binds.
 */
export class Connection {
  private gateway?: OwnedGateway;
  private token = '';
  /** The stored chain never stays rejected: a failed start must not poison the next reconnect. */
  private chain: Promise<unknown> = Promise.resolve();

  constructor(private readonly deps: ConnectionDeps) {}

  /** Serialised: a reconnect never races the start it replaces. */
  start(): Promise<void> { return this.connect(false); }
  reconnect(): Promise<void> { return this.connect(true); }

  /** The gateway origin this connection was built for. */
  get origin(): string { return this.deps.config.origin; }

  private connect(force: boolean): Promise<void> {
    const run = this.chain.then(async () => {
      if (force || !this.token) await this.connectOnce();
    });
    this.chain = run.catch(() => {});
    // A failed start leaves no token, and must leave no gateway child either.
    // Capture the token BEFORE teardown clears it: fail() must redact with the
    // token that was live when the error happened, or the raw token reaches the
    // log and the panel detail.
    return run.catch(async (err) => {
      const token = this.token;
      await this.teardown();
      this.fail(err, token);
    });
  }

  private async connectOnce(): Promise<void> {
    await this.teardown();
    this.deps.emit({ kind: 'connecting' });
    const resolution = await resolveGatewayToken({
      secrets: this.deps.secrets,
      env: this.deps.env,
      gatewayOrigin: this.deps.config.origin,
      probe: this.deps.probe,
      startGateway: this.deps.config.autoStartGateway
        ? async () => {
            if (await this.deps.healthy()) return null;
            return this.deps.startGateway();
          }
        : async () => null,
    });
    if (resolution.kind === 'none') {
      this.deps.emit({
        kind: 'unavailable',
        reason: resolution.reason === 'gateway-unreachable' ? 'gateway-unreachable' : 'no-token',
        detail: resolution.detail,
      });
      return;
    }
    this.token = resolution.token;
    this.gateway = resolution.gateway ?? undefined;
    this.deps.emit({ kind: 'ready' });
  }

  /**
   * Points the panel at one conversation: the next document load opens that
   * session. Only while connected — there is no panel to steer otherwise.
   */
  async showSession(sessionId: string): Promise<void> {
    if (!this.token) throw new Error('the panel is not connected');
    this.deps.emit({ kind: 'ready', session: sessionId });
  }

  /** The token for the sign-in link the user opens in their own browser; empty when not connected. */
  currentToken(): string { return this.token; }

  /** What the gateway said about an entered token; only `'ok'` stores it and reconnects. */
  async enterToken(entered: string): Promise<'ok' | 'rejected' | 'unreachable'> {
    const token = entered.trim();
    if (!token) return 'rejected';
    const verdict = await this.deps.probe(token);
    if (verdict !== 'ok') return verdict;
    await this.deps.secrets.store(TOKEN_KEY, token);
    await this.reconnect();
    return 'ok';
  }

  private async teardown(): Promise<void> {
    // The gateway child gets its signal first, before anything is awaited: on
    // shutdown the extension host may not live for another turn of the event
    // loop, and an orphaned gateway is the one thing a person would notice.
    const gatewayStop = this.gateway?.stop();
    this.gateway = undefined;
    this.token = '';
    await gatewayStop;
  }

  private fail(err: unknown, token: string): void {
    const message = err instanceof Error ? err.message : String(err);
    this.deps.log(redact(`connection failed: ${message}`, token));
    this.deps.emit({ kind: 'unavailable', reason: 'error', detail: redact(message, token) });
  }

  async dispose(): Promise<void> {
    // SIGTERM to a known gateway child before any await: the host may not live
    // for another event-loop turn. A start still in flight is not reachable
    // here — see Maintenance notes.
    const stop = this.gateway?.stop();
    await this.chain;
    await this.teardown();
    await stop;
  }
}