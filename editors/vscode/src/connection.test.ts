import test from 'node:test';
import assert from 'node:assert/strict';
import { Connection, type ConnectionDeps } from './connection';
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
  readonly secrets: MemorySecrets;
}

interface HarnessOptions {
  origin: string;
  /** A token already in secret storage. */
  stored?: string;
  probe?: (token: string) => Promise<Verdict>;
  healthy?: () => Promise<boolean>;
  startGateway?: ConnectionDeps['startGateway'];
}

function harness(options: HarnessOptions): Harness {
  const secrets = new MemorySecrets();
  if (options.stored !== undefined) secrets.values.set(TOKEN_KEY, options.stored);
  const emitted: PanelState[] = [];
  const logs: string[] = [];
  const config: GatewayConfig = { origin: options.origin, binaryPath: '', autoStartGateway: true };
  const deps: ConnectionDeps = {
    config,
    secrets,
    env: {},
    probe: options.probe ?? (async () => 'rejected'),
    healthy: options.healthy ?? (async () => false),
    startGateway: options.startGateway ?? (async () => null),
    log: (line) => logs.push(line),
    emit: (state) => emitted.push(state),
  };
  return { connection: new Connection(deps), emitted, logs, secrets };
}

function lastState(states: PanelState[]): PanelState {
  const state = states.at(-1);
  assert.ok(state, 'a state was emitted');
  return state;
}

function readyState(states: PanelState[]): { kind: 'ready'; session?: string } {
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
});

test('a stored token that probes ok gives a ready panel and survives a reconnect', async (t) => {
  const gateway = await startTestServer({ routes: { '/health': 200 } });
  const h = harness({ origin: gateway.origin, stored: 'stored-token', probe: async () => 'ok' });
  t.after(async () => {
    await h.connection.dispose();
    await gateway.close();
  });
  await h.connection.start();
  readyState(h.emitted);
  assert.equal(h.connection.currentToken(), 'stored-token');

  await h.connection.reconnect();
  await h.connection.reconnect();
  readyState(h.emitted);
  assert.equal(h.connection.currentToken(), 'stored-token', 'the token is the one the host proxies with');
});

test('dispose stops the gateway it started, once', async (t) => {
  const gateway = await startTestServer({ routes: { '/health': 200 } });
  const spawned = spawnedGateway(gateway.origin, SPAWNED_TOKEN);
  const h = harness({
    origin: gateway.origin,
    probe: async () => 'rejected',
    startGateway: async () => spawned.gateway,
  });
  t.after(() => gateway.close());
  await h.connection.start();
  readyState(h.emitted);
  assert.equal(h.connection.currentToken(), SPAWNED_TOKEN);
  await h.connection.dispose();
  assert.equal(spawned.calls.stops, 1, 'the gateway this extension started is stopped once');
});

test('a start that throws reports an error, and the next reconnect recovers', async (t) => {
  const gateway = await startTestServer({ routes: { '/health': 200 } });
  let failures = 1;
  const h = harness({
    origin: gateway.origin,
    stored: 'stored-token',
    probe: async () => {
      if (failures > 0) {
        failures -= 1;
        throw new Error('the health probe blew up');
      }
      return 'ok';
    },
  });
  t.after(async () => {
    await h.connection.dispose();
    await gateway.close();
  });
  await h.connection.start();
  const failed = unavailableState(h.emitted);
  assert.equal(failed.reason, 'error');
  assert.ok(failed.detail.includes('the health probe blew up'));

  await h.connection.reconnect();
  assert.equal(h.emitted.at(-2)?.kind, 'connecting', 'the chain is not poisoned by the failure');
  readyState(h.emitted);
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

test('the token never reaches the log on the ready path', async (t) => {
  const gateway = await startTestServer({ routes: { '/health': 200 } });
  const h = harness({ origin: gateway.origin, stored: 'stored-token', probe: async () => 'ok' });
  t.after(async () => {
    await h.connection.dispose();
    await gateway.close();
  });
  await h.connection.start();
  readyState(h.emitted);
  assert.ok(!h.logs.some((line) => line.includes('stored-token')), `no log line carries the token: ${JSON.stringify(h.logs)}`);
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
  readyState(h.emitted);
  await h.connection.showSession('s1');
  const after = readyState(h.emitted);
  assert.equal(after.session, 's1');
});

test('enterToken stores only a token the gateway accepts, then reconnects', async (t) => {
  const gateway = await startTestServer({ routes: { '/health': 200 } });
  const h = harness({ origin: gateway.origin, probe: async (token) => (token === 'good' ? 'ok' : 'rejected') });
  t.after(async () => {
    await h.connection.dispose();
    await gateway.close();
  });
  assert.equal(await h.connection.enterToken('bad'), 'rejected');
  assert.equal(await h.secrets.get(TOKEN_KEY), undefined, 'a rejected token is not stored');
  assert.equal(await h.connection.enterToken('good'), 'ok');
  assert.equal(await h.secrets.get(TOKEN_KEY), 'good');
  readyState(h.emitted);
});