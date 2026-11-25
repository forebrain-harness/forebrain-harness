import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { Connection, type ConnectionDeps } from './connection';
import { startRelay, type RelayOptions, type RelayTarget } from './relay';
import { TOKEN_KEY, type SecretStore } from './token';
import type { GatewayConfig } from './gatewayOrigin';
import type { PanelState } from './panelHtml';
import { startTestServer } from './testing/httpServer';

const SPAWNED_TOKEN = 'spawned-token-from-the-banner';

class MemorySecrets implements SecretStore {
  readonly values = new Map<string, string>();
  async get(key: string): Promise<string | undefined> {
    return this.values.get(key);
  }
  async store(key: string, value: string): Promise<void> {
    this.values.set(key, value);
  }
  async delete(key: string): Promise<void> {
    this.values.delete(key);
  }
}

type Verdict = 'ok' | 'rejected' | 'unreachable';

/** A gateway the extension started, counting the stop it may or may not need. */
function spawnedGateway(origin: string, token: string | null) {
  const calls = { stops: 0 };
  let stopping: Promise<void> | undefined;
  return {
    calls,
    gateway: {
      token,
      origin,
      stop: () => {
        stopping ??= (async () => {
          calls.stops += 1;
        })();
        return stopping;
      },
    },
  };
}

interface Harness {
  readonly connection: Connection;
  readonly emitted: PanelState[];
  readonly logs: string[];
  readonly calls: { target: RelayTarget; options: RelayOptions }[];
  readonly ports: number[];
  readonly secrets: MemorySecrets;
}

interface HarnessOptions {
  origin: string;
  /** A token already in secret storage. */
  stored?: string;
  probe?: (token: string) => Promise<Verdict>;
  healthy?: () => Promise<boolean>;
  startGateway?: ConnectionDeps['startGateway'];
  /** `false` makes nonce, boot and port come back empty every time (a fresh origin per connect). */
  sticky?: boolean;
  /** How many relay starts reject before one is allowed to bind. */
  relayFailures?: number;
  /** The relay observer the extension-side plumbing would pass in. */
  onRequest?: ConnectionDeps['onRequest'];
}

function harness(options: HarnessOptions): Harness {
  const secrets = new MemorySecrets();
  if (options.stored !== undefined) secrets.values.set(TOKEN_KEY, options.stored);
  const emitted: PanelState[] = [];
  const logs: string[] = [];
  const calls: Harness['calls'] = [];
  const ports: number[] = [];
  const saved = { nonce: undefined as string | undefined, boot: undefined as string | undefined, port: undefined as number | undefined };
  const sticky = options.sticky ?? true;
  let relayFailures = options.relayFailures ?? 0;
  const config: GatewayConfig = { origin: options.origin, binaryPath: '', autoStartGateway: true, relayLogRequests: false };
  const deps: ConnectionDeps = {
    config,
    loadNonce: async () => (sticky ? saved.nonce : undefined),
    saveNonce: async (nonce) => {
      saved.nonce = nonce;
    },
    loadBoot: async () => (sticky ? saved.boot : undefined),
    saveBoot: async (boot) => {
      saved.boot = boot;
    },
    loadPort: async () => (sticky ? saved.port : undefined),
    savePort: async (port) => {
      saved.port = port;
    },
    secrets,
    env: {},
    probe: options.probe ?? (async () => 'rejected'),
    healthy: options.healthy ?? (async () => false),
    startGateway: options.startGateway ?? (async () => null),
    startRelay: async (target, relayOptions) => {
      calls.push({ target, options: relayOptions });
      if (relayFailures > 0) {
        relayFailures -= 1;
        // The text names the token on purpose: fail() must redact it.
        throw new Error(`relay could not bind to 127.0.0.1 with ${target.token}`);
      }
      const relay = await startRelay(target, relayOptions);
      ports.push(relay.port);
      return relay;
    },
    log: (line) => logs.push(line),
    emit: (state) => emitted.push(state),
    onRequest: options.onRequest,
  };
  return { connection: new Connection(deps), emitted, logs, calls, ports, secrets };
}

/**
 * One GET straight to the relay's loopback port with the relay origin as Host:
 * the relay is reached without asking the system to resolve `*.localhost`,
 * which macOS answers only intermittently.
 */
function relayGet(relayOrigin: string, path = '/health'): Promise<number> {
  const url = new URL(relayOrigin);
  return new Promise((resolve, reject) => {
    const req = http.request(
      // `agent: false`: no pooled socket, so a closed relay always shows as ECONNREFUSED.
      { host: '127.0.0.1', port: Number(url.port), path, headers: { host: url.host }, agent: false },
      (res) => {
        res.resume();
        resolve(res.statusCode ?? 0);
      },
    );
    req.on('error', reject);
    req.end();
  });
}

function refused(err: unknown): boolean {
  return (err as NodeJS.ErrnoException).code === 'ECONNREFUSED';
}

function lastState(states: PanelState[]): PanelState {
  const state = states.at(-1);
  assert.ok(state, 'a state was emitted');
  return state;
}

function readyState(states: PanelState[]): { kind: 'ready'; relayOrigin: string; boot: string; session?: string } {
  const state = lastState(states);
  assert.equal(state.kind, 'ready');
  if (state.kind !== 'ready') throw new Error('not ready');
  return state;
}

function unavailableState(states: PanelState[]): { kind: 'unavailable'; reason: string; detail: string } {
  const state = lastState(states);
  assert.equal(state.kind, 'unavailable');
  if (state.kind !== 'unavailable') throw new Error('not unavailable');
  return state;
}

test('with no token anywhere and no gateway to start, the panel asks for a token', async (t) => {
  const gateway = await startTestServer({ routes: { '/health': 200 } });
  const h = harness({ origin: gateway.origin, probe: async () => 'rejected' });
  t.after(async () => {
    await h.connection.dispose();
    await gateway.close();
  });
  await h.connection.start();
  const state = unavailableState(h.emitted);
  assert.equal(state.reason, 'no-token');
  assert.equal(h.calls.length, 0, 'no relay is started without a token');
});

test('a stored token gives a ready panel whose nonce and boot secret survive a reconnect', async (t) => {
  const gateway = await startTestServer({ routes: { '/health': 200 } });
  const h = harness({ origin: gateway.origin, stored: 'stored-token', probe: async () => 'ok' });
  t.after(async () => {
    await h.connection.dispose();
    await gateway.close();
  });
  await h.connection.start();
  const first = readyState(h.emitted);
  assert.match(first.boot, /^[0-9a-f]{32}$/);
  assert.equal(h.calls.length, 1);
  assert.equal(h.calls[0].options.boot, first.boot, 'the panel and the relay agree on the boot secret');
  assert.equal(h.calls[0].options.preferredPort, 0, 'nothing was saved before the first connect');

  await h.connection.reconnect();
  await h.connection.reconnect();
  const again = readyState(h.emitted);
  assert.equal(h.calls.length, 3);
  assert.equal(h.calls[2].options.nonce, h.calls[0].options.nonce, 'the nonce is generated once, so the origin half is stable');
  assert.equal(h.calls[2].options.boot, h.calls[0].options.boot, 'the boot secret is generated once');
  assert.equal(h.calls[2].options.preferredPort, h.ports[0], 'the port of the last relay comes back as the preferred one');
  assert.equal(again.boot, first.boot);
});

test('a reconnect replaces the relay, and the old origin stops answering', async (t) => {
  const gateway = await startTestServer({ routes: { '/health': 200 } });
  const h = harness({ origin: gateway.origin, stored: 'stored-token', probe: async () => 'ok', sticky: false });
  t.after(async () => {
    await h.connection.dispose();
    await gateway.close();
  });
  await h.connection.start();
  const first = readyState(h.emitted);
  await h.connection.reconnect();
  const second = readyState(h.emitted);
  assert.notEqual(second.relayOrigin, first.relayOrigin, 'a fresh nonce means a fresh origin');
  await assert.rejects(relayGet(first.relayOrigin), refused, 'the old relay is closed');
  assert.equal(await relayGet(second.relayOrigin), 200, 'the new relay reaches the gateway');
});

test('dispose stops the gateway it started and releases the relay port', async (t) => {
  const gateway = await startTestServer({ routes: { '/health': 200 } });
  const spawned = spawnedGateway(gateway.origin, SPAWNED_TOKEN);
  const h = harness({
    origin: gateway.origin,
    probe: async () => 'rejected',
    startGateway: async () => spawned.gateway,
  });
  t.after(() => gateway.close());
  await h.connection.start();
  const ready = readyState(h.emitted);
  await h.connection.dispose();
  assert.equal(spawned.calls.stops, 1, 'the gateway this extension started is stopped once');
  await assert.rejects(relayGet(ready.relayOrigin), refused, 'the relay port is released');
});

test('a relay that fails to start reports a redacted error, stops its gateway, and the next reconnect recovers', async (t) => {
  const gateway = await startTestServer({ routes: { '/health': 200 } });
  const spawned = spawnedGateway(gateway.origin, SPAWNED_TOKEN);
  const h = harness({
    origin: gateway.origin,
    probe: async () => 'rejected',
    startGateway: async () => spawned.gateway,
    relayFailures: 1,
  });
  t.after(async () => {
    await h.connection.dispose();
    await gateway.close();
  });
  await h.connection.start();
  const failed = unavailableState(h.emitted);
  assert.equal(failed.reason, 'error');
  assert.ok(failed.detail.includes('‹token›'), `the panel detail is redacted: ${failed.detail}`);
  assert.ok(!failed.detail.includes(SPAWNED_TOKEN), 'the token never reaches the panel');
  assert.ok(h.logs.some((line) => line.includes('‹token›')), 'the failure is logged, redacted');
  assert.ok(!h.logs.some((line) => line.includes(SPAWNED_TOKEN)), 'no log line carries the token');
  assert.equal(spawned.calls.stops, 1, 'the failure path stops the gateway it started');

  await h.connection.reconnect();
  assert.equal(h.emitted.at(-2)?.kind, 'connecting', 'the chain is not poisoned by the failure');
  readyState(h.emitted);
  await h.connection.dispose();
});

test('a stored token with an unreachable gateway is kept and reported as gateway-unreachable', async (t) => {
  const gateway = await startTestServer({ routes: { '/health': 200 } });
  const h = harness({ origin: gateway.origin, stored: 'on-file', probe: async () => 'unreachable' });
  t.after(async () => {
    await h.connection.dispose();
    await gateway.close();
  });
  await h.connection.start();
  const state = unavailableState(h.emitted);
  assert.equal(state.reason, 'gateway-unreachable');
  assert.equal(await h.secrets.get(TOKEN_KEY), 'on-file', 'an unreachable gateway must not cost the stored token');
});

test('the boot secret never reaches the log', async (t) => {
  const gateway = await startTestServer({ routes: { '/health': 200 } });
  const h = harness({ origin: gateway.origin, stored: 'stored-token', probe: async () => 'ok' });
  t.after(async () => {
    await h.connection.dispose();
    await gateway.close();
  });
  await h.connection.start();
  const ready = readyState(h.emitted);
  assert.ok(h.logs.length > 0, 'the relay origin is logged so a person can see it');
  assert.ok(!h.logs.some((line) => line.includes(ready.boot)), `no log line carries the boot secret: ${JSON.stringify(h.logs)}`);
});
test('the relay hands every admitted request to the observer with its full path', async (t) => {
  const gateway = await startTestServer({ routes: { '/health?limit=0': 200 } });
  const seen: [string, string][] = [];
  const h = harness({
    origin: gateway.origin,
    stored: 'stored-token',
    probe: async () => 'ok',
    onRequest: (method, path) => seen.push([method, path]),
  });
  t.after(async () => {
    await h.connection.dispose();
    await gateway.close();
  });
  await h.connection.start();
  const ready = readyState(h.emitted);
  assert.equal(await relayGet(ready.relayOrigin, '/health?limit=0'), 200);
  assert.deepEqual(seen, [['GET', '/health?limit=0']], 'the query survives: the session id lives in the path');
  assert.ok(!h.logs.some((line) => line.includes('limit=0')), 'the log line still drops the query');
});

test('showSession points the panel at one conversation, and refuses when there is no panel', async (t) => {
  const gateway = await startTestServer({ routes: { '/health': 200 } });
  const h = harness({ origin: gateway.origin, stored: 'stored-token', probe: async () => 'ok' });
  t.after(async () => {
    await h.connection.dispose();
    await gateway.close();
  });
  await assert.rejects(h.connection.showSession('s1'), /not connected/);
  await h.connection.start();
  const before = readyState(h.emitted);
  await h.connection.showSession('s1');
  const after = readyState(h.emitted);
  assert.equal(after.session, 's1');
  assert.equal(after.boot, before.boot, 'the boot secret is the one the relay already knows');
  assert.equal(after.relayOrigin, before.relayOrigin, 'the same relay, a different conversation');
});
