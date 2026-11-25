import { randomBytes } from 'node:crypto';
import { startRelay, type Relay } from './relay';
import { resolveGatewayToken, redact, TOKEN_KEY, type OwnedGateway, type SecretStore } from './token';
import type { GatewayConfig } from './gatewayOrigin';
import type { PanelState } from './panelHtml';

export interface ConnectionDeps {
  readonly config: GatewayConfig;
  /** Stable per install: the capability half of the relay's hostname. */
  loadNonce(): Promise<string | undefined>;
  saveNonce(nonce: string): Promise<void>;
  /** Stable per install, never logged: the secret a navigation must carry. */
  loadBoot(): Promise<string | undefined>;
  saveBoot(boot: string): Promise<void>;
  /** The port the last relay used, so the origin stays stable. */
  loadPort(): Promise<number | undefined>;
  savePort(port: number): Promise<void>;
  readonly secrets: SecretStore;
  readonly env: NodeJS.ProcessEnv;
  readonly probe: (token: string) => Promise<'ok' | 'rejected' | 'unreachable'>;
  readonly healthy: () => Promise<boolean>;
  /** Starts a gateway, or resolves null when not attempted or when it failed (after logging why). */
  readonly startGateway: () => Promise<{ token: string | null; origin: string; stop(): Promise<void> } | null>;
  readonly startRelay: typeof startRelay;
  readonly log: (line: string) => void;
  readonly emit: (state: PanelState) => void;
  /**
   * Every request the relay admits, with its full path (query included) — how
   * the extension learns which conversation the Web UI is looking at.
   */
  readonly onRequest?: ((method: string, path: string) => void) | undefined;
}

export class Connection {
  private relay?: Relay;
  private gateway?: OwnedGateway;
  private token = '';
  /** The boot secret of the current relay: `showSession` re-emits the ready state with it. */
  private boot?: string;
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
      if (force || !this.relay) await this.connectOnce();
    });
    this.chain = run.catch(() => {});
    // A failed start leaves no relay, and must leave no gateway child either.
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
    const nonce = (await this.deps.loadNonce()) ?? randomBytes(16).toString('hex');
    await this.deps.saveNonce(nonce);
    const boot = (await this.deps.loadBoot()) ?? randomBytes(16).toString('hex');
    await this.deps.saveBoot(boot);
    this.boot = boot;
    this.relay = await this.deps.startRelay(
      { origin: this.deps.config.origin, token: this.token },
      {
        nonce,
        boot,
        preferredPort: (await this.deps.loadPort()) ?? 0,
        // Method and path, in two forms for two readers: the log line drops the
        // query (it carries the boot secret of a navigation), while the
        // observer gets the whole path, because a session id lives in the path.
        onRequest: (method, path) => {
          if (this.deps.config.relayLogRequests) this.deps.log(`relay: ${method} ${path.split('?')[0]}`);
          this.deps.onRequest?.(method, path);
        },
      },
    );
    await this.deps.savePort(this.relay.port);
    this.deps.log(`relay: ${this.relay.origin} -> ${this.deps.config.origin}`);
    this.deps.emit({ kind: 'ready', relayOrigin: this.relay.origin, boot });
  }

  /**
   * Points the panel at one conversation: the next document load opens that
   * session instead of whatever the Web UI last showed. Only while connected —
   * there is no panel to steer otherwise.
   */
  async showSession(sessionId: string): Promise<void> {
    if (!this.relay || !this.boot) throw new Error('the panel is not connected');
    this.deps.emit({ kind: 'ready', relayOrigin: this.relay.origin, boot: this.boot, session: sessionId });
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
    this.boot = undefined;
    await this.relay?.close();
    this.relay = undefined;
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