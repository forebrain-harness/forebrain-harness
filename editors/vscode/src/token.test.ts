import test from 'node:test';
import assert from 'node:assert/strict';
import { redact, resolveGatewayToken, TOKEN_KEY, type OwnedGateway, type SecretStore } from './token';

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

function probeOf(verdicts: Record<string, Verdict>) {
  return async (token: string): Promise<Verdict> => verdicts[token] ?? 'rejected';
}

/** A gateway the extension started, counting the stop it may or may not need. */
function ownedGateway(origin = 'http://127.0.0.1:6060') {
  const calls = { stops: 0 };
  let stopping: Promise<void> | undefined;
  const gateway: OwnedGateway = {
    origin,
    stop: () => {
      stopping ??= (async () => {
        calls.stops += 1;
      })();
      return stopping;
    },
  };
  return { gateway, calls };
}

test('a stored token the gateway accepts is used as is', async () => {
  const secrets = new MemorySecrets();
  await secrets.store(TOKEN_KEY, 'stored');
  let started = 0;
  const resolution = await resolveGatewayToken({
    secrets,
    env: {},
    gatewayOrigin: 'http://127.0.0.1:6060',
    probe: probeOf({ stored: 'ok' }),
    startGateway: async () => {
      started += 1;
      return null;
    },
  });
  assert.deepEqual(resolution, { kind: 'token', token: 'stored', source: 'secret-storage', gateway: null });
  assert.equal(started, 0, 'a working stored token does not start anything');
  assert.equal(await secrets.get(TOKEN_KEY), 'stored', 'a working stored token is left alone');
});

test('a rejected stored token is forgotten and the environment token takes over', async () => {
  const secrets = new MemorySecrets();
  await secrets.store(TOKEN_KEY, 'stale');
  const resolution = await resolveGatewayToken({
    secrets,
    env: { FOREBRAIN_GATEWAY_TOKEN: 'from-env' },
    gatewayOrigin: 'http://127.0.0.1:6060',
    probe: probeOf({ 'from-env': 'ok' }),
    startGateway: async () => null,
  });
  assert.equal(resolution.kind === 'token' && resolution.source, 'environment');
  assert.equal(await secrets.get(TOKEN_KEY), 'from-env', 'the accepted environment token is stored');
});

test('an unreachable gateway keeps the stored token and a spawned token stays in memory', async () => {
  const secrets = new MemorySecrets();
  await secrets.store(TOKEN_KEY, 'on-file');
  const spawned = ownedGateway();
  const resolution = await resolveGatewayToken({
    secrets,
    env: {},
    gatewayOrigin: 'http://127.0.0.1:6060',
    probe: async () => 'unreachable',
    startGateway: async () => ({ token: 'generated', ...spawned.gateway }),
  });
  assert.equal(resolution.kind === 'token' && resolution.token, 'generated');
  assert.equal(resolution.kind === 'token' && resolution.source, 'spawned-gateway');
  assert.equal(resolution.kind === 'token' && resolution.gateway?.stop, spawned.gateway.stop);
  assert.equal(await secrets.get(TOKEN_KEY), 'on-file', 'an unreachable gateway must not cost the stored token');
  assert.ok(![...secrets.values.values()].includes('generated'), 'a generated token dies with its process, so it is never stored');
});

test('a spawned gateway supplies the token when nothing else has one', async () => {
  const secrets = new MemorySecrets();
  const spawned = ownedGateway();
  const resolution = await resolveGatewayToken({
    secrets,
    env: {},
    gatewayOrigin: 'http://127.0.0.1:6060',
    probe: async () => 'rejected',
    startGateway: async () => ({ token: 'generated', ...spawned.gateway }),
  });
  assert.equal(resolution.kind === 'token' && resolution.source, 'spawned-gateway');
  assert.equal(secrets.values.size, 0, 'nothing is written for a generated token');
});

test('a spawned gateway with a token from forebrain.yaml is stopped and its token asked for', async () => {
  const secrets = new MemorySecrets();
  const spawned = ownedGateway();
  const resolution = await resolveGatewayToken({
    secrets,
    env: {},
    gatewayOrigin: 'http://127.0.0.1:6060',
    probe: async () => 'rejected',
    startGateway: async () => ({ token: null, ...spawned.gateway }),
  });
  assert.equal(resolution.kind, 'none');
  assert.equal(resolution.kind === 'none' && resolution.reason, 'configured-token');
  assert.equal(spawned.calls.stops, 1, 'the orphan gateway is stopped, not left running');
});

test('nothing stored, no environment token and no gateway to start reports not-found', async () => {
  const resolution = await resolveGatewayToken({
    secrets: new MemorySecrets(),
    env: {},
    gatewayOrigin: 'http://127.0.0.1:6060',
    probe: async () => 'rejected',
    startGateway: async () => null,
  });
  assert.equal(resolution.kind, 'none');
  assert.equal(resolution.kind === 'none' && resolution.reason, 'not-found');
});

test('a stored token with an unreachable gateway and nothing to start reports gateway-unreachable', async () => {
  const secrets = new MemorySecrets();
  await secrets.store(TOKEN_KEY, 'on-file');
  const resolution = await resolveGatewayToken({
    secrets,
    env: {},
    gatewayOrigin: 'http://127.0.0.1:6060',
    probe: async () => 'unreachable',
    startGateway: async () => null,
  });
  assert.equal(resolution.kind, 'none');
  assert.equal(resolution.kind === 'none' && resolution.reason, 'gateway-unreachable');
  assert.equal(await secrets.get(TOKEN_KEY), 'on-file', 'the stored token survives an unreachable gateway');
});

test('redact replaces every occurrence of the token and leaves text alone without one', () => {
  assert.equal(redact('a SECRET b SECRET', 'SECRET'), 'a ‹token› b ‹token›');
  assert.equal(redact('unchanged', ''), 'unchanged');
});