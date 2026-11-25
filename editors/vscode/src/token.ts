/** Structural subset of vscode.SecretStorage, so tests pass an in-memory store. */
export interface SecretStore {
  get(key: string): Promise<string | undefined>;
  store(key: string, value: string): Promise<void>;
  delete(key: string): Promise<void>;
}

export const TOKEN_KEY = 'forebrain.gatewayToken';

export type TokenSource = 'secret-storage' | 'environment' | 'spawned-gateway';

/** A gateway this extension started and therefore stops itself. */
export interface OwnedGateway {
  readonly origin: string;
  stop(): Promise<void>;
}

export type TokenResolution =
  | { kind: 'token'; token: string; source: TokenSource; gateway: OwnedGateway | null }
  | { kind: 'none'; reason: 'not-found' | 'configured-token' | 'gateway-unreachable'; detail: string };

export interface TokenDeps {
  secrets: SecretStore;
  env: NodeJS.ProcessEnv;
  /** The gateway's origin, for messages only. */
  gatewayOrigin: string;
  /** What the gateway said about a token (Step 3's `ProbeResult`). */
  probe: (token: string) => Promise<'ok' | 'rejected' | 'unreachable'>;
  /**
   * Starts a gateway and returns its token, or null when it was not attempted
   * or failed (disabled, no binary, port taken). Resolves to `{ token: null }`
   * when the gateway started but has a configured token the banner does not print.
   */
  startGateway: () => Promise<{ token: string | null; origin: string } & OwnedGateway | null>;
}

const NONE_FOUND: TokenResolution = {
  kind: 'none', reason: 'not-found',
  detail: 'No usable gateway token was found. Paste one with "Forebrain: Enter Gateway Token", or start a gateway and run "Forebrain: Reconnect".',
};

export async function resolveGatewayToken(deps: TokenDeps): Promise<TokenResolution> {
  let unreachable = false;
  const stored = (await deps.secrets.get(TOKEN_KEY))?.trim();
  if (stored) {
    const verdict = await deps.probe(stored);
    if (verdict === 'ok') {
      return { kind: 'token', token: stored, source: 'secret-storage', gateway: null };
    }
    // Rejected: a generated token died with its process, or the configured one
    // changed — forget it. Unreachable: the gateway is not listening, and the
    // stored token may still be the right one once it is.
    if (verdict === 'rejected') await deps.secrets.delete(TOKEN_KEY);
    else unreachable = true;
  }
  const fromEnv = deps.env.FOREBRAIN_GATEWAY_TOKEN?.trim();
  if (fromEnv) {
    const verdict = await deps.probe(fromEnv);
    if (verdict === 'ok') {
      await deps.secrets.store(TOKEN_KEY, fromEnv);
      return { kind: 'token', token: fromEnv, source: 'environment', gateway: null };
    }
    if (verdict === 'unreachable') unreachable = true;
  }
  const started = await deps.startGateway();
  if (started === null) {
    if (unreachable) {
      return {
        kind: 'none', reason: 'gateway-unreachable',
        detail: `A token is on file but ${deps.gatewayOrigin} did not answer. Start the gateway, then run "Forebrain: Reconnect".`,
      };
    }
    return NONE_FOUND;
  }
  if (started.token === null) {
    // A gateway this extension started but cannot authenticate to is an orphan
    // nobody asked for: stop it before asking the person for the token.
    await started.stop();
    return { kind: 'none', reason: 'configured-token', detail: `A gateway is running at ${started.origin} with a token from forebrain.yaml; enter it with "Forebrain: Enter Gateway Token".` };
  }
  return { kind: 'token', token: started.token, source: 'spawned-gateway', gateway: started };
}

/** Replaces every occurrence of the token in text meant for a log. */
export function redact(text: string, token: string): string {
  return token ? text.split(token).join('‹token›') : text;
}