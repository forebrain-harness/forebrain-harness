import test from 'node:test';
import assert from 'node:assert/strict';
import { GatewayClient, GatewayHttpError, GatewayUnreachableError } from './gateway';
import { closedPort, startTestServer } from './testing/httpServer';

const SESSION = '/api/auth/session';

test('probe sends the token in an Authorization Bearer header on a GET', async (t) => {
  const gateway = await startTestServer({ routes: { [SESSION]: 200 } });
  t.after(() => gateway.close());
  await new GatewayClient(gateway.origin).probe('t0k');
  assert.equal(gateway.seen.length, 1);
  assert.equal(gateway.seen[0].method, 'GET');
  assert.equal(gateway.seen[0].path, SESSION);
  assert.equal(gateway.seen[0].headers.authorization, 'Bearer t0k');
});

test('probe answers ok when the gateway accepts the token', async (t) => {
  const gateway = await startTestServer({ routes: { [SESSION]: 200 } });
  t.after(() => gateway.close());
  assert.equal(await new GatewayClient(gateway.origin).probe('t0k'), 'ok');
});

test('probe answers rejected when the gateway answers 401', async (t) => {
  const gateway = await startTestServer({ routes: { [SESSION]: 401 } });
  t.after(() => gateway.close());
  assert.equal(await new GatewayClient(gateway.origin).probe('t0k'), 'rejected');
});

test('probe throws GatewayHttpError for a status it has no verdict for', async (t) => {
  const gateway = await startTestServer({ routes: { [SESSION]: 500 }, body: 'boom' });
  t.after(() => gateway.close());
  await assert.rejects(
    new GatewayClient(gateway.origin).probe('t0k'),
    (err: unknown) => err instanceof GatewayHttpError && err.status === 500,
  );
});

test('health is true for 200 and false for 503', async (t) => {
  const healthy = await startTestServer({ routes: { '/health': 200 } });
  const unhealthy = await startTestServer({ routes: { '/health': 503 } });
  t.after(async () => {
    await healthy.close();
    await unhealthy.close();
  });
  assert.equal(await new GatewayClient(healthy.origin).health(), true);
  assert.equal(await new GatewayClient(unhealthy.origin).health(), false);
});

test('probe reports unreachable and health is false when nothing listens', async () => {
  const origin = `http://127.0.0.1:${await closedPort()}`;
  const client = new GatewayClient(origin);
  assert.equal(await client.probe('t0k'), 'unreachable');
  assert.equal(await client.health(), false);
});

test('probe gives up after the request timeout when the gateway never answers', async (t) => {
  const gateway = await startTestServer({ hang: [SESSION] });
  t.after(() => gateway.close());
  const started = Date.now();
  assert.equal(await new GatewayClient(gateway.origin).probe('t0k'), 'unreachable');
  const elapsed = Date.now() - started;
  assert.ok(elapsed >= 4_000, `waited for the timeout (${elapsed}ms)`);
  assert.ok(elapsed < 7_000, `gave up before 7s (${elapsed}ms)`);
});
test('getJson sends the token and turns a non-2xx answer into a GatewayHttpError', async (t) => {
  const gateway = await startTestServer({ routes: { '/api/v1/projects': 401 }, body: 'no' });
  t.after(() => gateway.close());
  await assert.rejects(
    new GatewayClient(gateway.origin).getJson('/api/v1/projects', 't0k'),
    (err: unknown) => err instanceof GatewayHttpError && err.status === 401,
  );
  assert.equal(gateway.seen[0].headers.authorization, 'Bearer t0k');
});

test('postJson sends a JSON body and content type, and no answer is unreachable', async (t) => {
  const gateway = await startTestServer({ routes: { '/api/chat/sessions': 201 }, body: '{"id":"s9"}' });
  t.after(() => gateway.close());
  const created = await new GatewayClient(gateway.origin).postJson<{ id: string }>('/api/chat/sessions', 't0k', {
    title: 'VS Code: a.ts',
    folder: '/ws',
  });
  assert.deepEqual(created, { id: 's9' });
  assert.equal(gateway.seen[0].method, 'POST');
  assert.equal(gateway.seen[0].headers['content-type'], 'application/json');
  assert.equal(gateway.seen[0].headers.authorization, 'Bearer t0k');
  assert.deepEqual(JSON.parse(gateway.seen[0].body), { title: 'VS Code: a.ts', folder: '/ws' });

  const origin = `http://127.0.0.1:${await closedPort()}`;
  await assert.rejects(
    new GatewayClient(origin).postJson('/api/chat/sessions', 't0k', {}),
    (err: unknown) => err instanceof GatewayUnreachableError,
  );
});
