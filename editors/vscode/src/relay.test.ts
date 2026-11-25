import test from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import net from 'node:net';
import { WebSocket, WebSocketServer } from 'ws';
import { startRelay } from './relay';

const TOKEN = 'unit-test-token';
const NONCE = '0123456789abcdef0123456789abcdef';
const BOOT = 'ffffffffffffffffffffffffffffffff';

function startTestRelay(gatewayPort: number, options: { preferredPort?: number; onRequest?: (method: string, path: string) => void } = {}) {
  return startRelay({ origin: `http://127.0.0.1:${gatewayPort}`, token: TOKEN }, { nonce: NONCE, boot: BOOT, ...options });
}

interface Seen {
  path: string;
  headers: http.IncomingHttpHeaders;
}

// A stand-in for the gateway: it authenticates exactly the way
// pkg/gateway/controlplane.go does for a machine client — the Bearer header —
// and records every request it admitted, so a test can assert on what crossed
// the relay rather than on what the relay claims.
function startMockGateway() {
  const seen: Seen[] = [];
  const wss = new WebSocketServer({ noServer: true });
  const server = http.createServer((req, res) => {
    seen.push({ path: req.url ?? '', headers: req.headers });
    if (req.headers.authorization !== `Bearer ${TOKEN}`) {
      res.writeHead(401, { 'content-type': 'application/json' });
      res.end('{"error":"unauthorized"}');
      return;
    }
    if (req.url === '/stream') {
      res.writeHead(200, { 'content-type': 'text/event-stream', 'set-cookie': 'fb_session=1; SameSite=Strict' });
      res.write('first');
      setTimeout(() => {
        res.write('second');
        res.end();
      }, 60);
      return;
    }
    let body = '';
    req.setEncoding('utf8');
    req.on('data', (chunk: string) => {
      body += chunk;
    });
    req.on('end', () => {
      res.writeHead(201, { 'content-type': 'application/json' });
      res.end(JSON.stringify({
        body,
        auth: req.headers.authorization ?? null,
        cookie: req.headers.cookie ?? null,
        origin: req.headers.origin ?? null,
        referer: req.headers.referer ?? null,
      }));
    });
  });
  wss.on('connection', (ws: WebSocket) => {
    ws.on('message', (data) => ws.send(`echo:${String(data)}`));
  });
  server.on('upgrade', (req, socket, head) => {
    seen.push({ path: req.url ?? '', headers: req.headers });
    if (req.headers.authorization !== `Bearer ${TOKEN}`) {
      socket.destroy();
      return;
    }
    wss.handleUpgrade(req, socket as net.Socket, head, (ws) => wss.emit('connection', ws, req));
  });
  return {
    seen,
    wss,
    listen: () => new Promise<number>((resolve) => server.listen(0, '127.0.0.1', () => {
      resolve((server.address() as net.AddressInfo).port);
    })),
    close: () => new Promise<void>((resolve) => {
      wss.close();
      server.close(() => resolve());
      server.closeAllConnections();
    }),
  };
}

function request(port: number, path: string, headers: Record<string, string>, method = 'GET', body?: string) {
  return new Promise<{ status: number; headers: http.IncomingHttpHeaders; chunks: string[] }>((resolve, reject) => {
    const req = http.request({ host: '127.0.0.1', port, path, method, headers }, (res) => {
      const chunks: string[] = [];
      res.setEncoding('utf8');
      res.on('data', (c: string) => chunks.push(c));
      res.on('end', () => resolve({ status: res.statusCode ?? 0, headers: res.headers, chunks }));
    });
    req.on('error', reject);
    if (body !== undefined) req.write(body);
    req.end();
  });
}

function relayPort(relayOrigin: string): number {
  return Number(new URL(relayOrigin).port);
}

test('the relay adds the bearer token and drops what the browser sent', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const relay = await startTestRelay(gatewayPort);
  t.after(async () => {
    await relay.close();
    await gateway.close();
  });
  const host = new URL(relay.origin).host;
  const res = await request(relayPort(relay.origin), '/echo', {
    host,
    cookie: 'fb_session=1',
    origin: relay.origin,
    referer: `${relay.origin}/`,
    'content-type': 'application/json',
  }, 'POST', '{"token":"x"}');
  assert.equal(res.status, 201);
  const echoed = JSON.parse(res.chunks.join('')) as Record<string, unknown>;
  assert.deepEqual(echoed, { body: '{"token":"x"}', auth: `Bearer ${TOKEN}`, cookie: null, origin: null, referer: null });
});

test('the relay refuses a request whose Host is not its own origin', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const relay = await startTestRelay(gatewayPort);
  t.after(async () => {
    await relay.close();
    await gateway.close();
  });
  const port = relayPort(relay.origin);
  const res = await request(port, '/', { host: `127.0.0.1:${port}` });
  assert.equal(res.status, 404);
  assert.equal(gateway.seen.length, 0, 'nothing may reach the gateway under a wrong Host');
});

test('the relay streams a response unbuffered and never forwards Set-Cookie', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const relay = await startTestRelay(gatewayPort);
  t.after(async () => {
    await relay.close();
    await gateway.close();
  });
  const host = new URL(relay.origin).host;
  const res = await request(relayPort(relay.origin), '/stream', { host, 'sec-fetch-site': 'same-origin' });
  assert.equal(res.status, 200);
  assert.equal(res.headers['set-cookie'], undefined);
  assert.deepEqual(res.chunks, ['first', 'second'], 'two writes arrive as two reads');
});

test('the relay admits only the panel iframe load, and only with the boot secret', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const relay = await startTestRelay(gatewayPort);
  t.after(async () => {
    await relay.close();
    await gateway.close();
  });
  const host = new URL(relay.origin).host;
  const port = relayPort(relay.origin);
  const panelLoad = await request(port, `/?boot=${BOOT}`, {
    host, 'sec-fetch-site': 'cross-site', 'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'iframe',
  });
  assert.equal(panelLoad.status, 201, 'the webview embedding the panel is cross-site');
  const withoutBoot = await request(port, '/', {
    host, 'sec-fetch-site': 'cross-site', 'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'iframe',
  });
  assert.equal(withoutBoot.status, 404, 'another page that learned the origin cannot embed the Web UI');
  const wrongBoot = await request(port, '/?boot=00000000000000000000000000000000', {
    host, 'sec-fetch-site': 'cross-site', 'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'iframe',
  });
  assert.equal(wrongBoot.status, 404, 'a guessed boot secret is refused');
  const topLevel = await request(port, '/', {
    host, 'sec-fetch-site': 'none', 'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'document',
  });
  assert.equal(topLevel.status, 404, 'the origin is for the panel, not for a browser tab');
  const crossSiteFetch = await request(port, '/api/chat/sessions', { host, 'sec-fetch-site': 'cross-site' });
  assert.equal(crossSiteFetch.status, 404, 'another page cannot fetch through the relay');
});

test('the relay carries a WebSocket handshake with the token and without Origin', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const relay = await startTestRelay(gatewayPort);
  t.after(async () => {
    await relay.close();
    await gateway.close();
  });
  const ws = new WebSocket(`${relay.origin.replace(/^http/, 'ws')}/ws/chat`, { headers: { origin: relay.origin } });
  t.after(() => ws.terminate());
  const reply = new Promise<string>((resolve, reject) => {
    ws.once('error', reject);
    ws.on('message', (data) => resolve(String(data)));
  });
  await new Promise<void>((resolve, reject) => {
    ws.once('open', resolve);
    ws.once('error', reject);
  });
  ws.send('ping');
  assert.equal(await reply, 'echo:ping');
  const handshake = gateway.seen.find((s) => s.path === '/ws/chat');
  assert.ok(handshake, 'the handshake reached the gateway');
  assert.equal(handshake.headers.authorization, `Bearer ${TOKEN}`);
  assert.equal(handshake.headers.origin, undefined);
});

test('the relay refuses a WebSocket whose Origin is not its own', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const relay = await startTestRelay(gatewayPort);
  t.after(async () => {
    await relay.close();
    await gateway.close();
  });
  const ws = new WebSocket(`${relay.origin.replace(/^http/, 'ws')}/ws/chat`, { headers: { origin: 'http://evil.example' } });
  const outcome = await new Promise<'error' | 'open'>((resolve) => {
    ws.once('error', () => resolve('error'));
    ws.once('open', () => resolve('open'));
  });
  t.after(() => ws.terminate());
  assert.equal(outcome, 'error');
  assert.equal(gateway.seen.length, 0);
});

test('close stops the listener and releases the port', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const relay = await startTestRelay(gatewayPort);
  t.after(() => gateway.close());
  const port = relayPort(relay.origin);
  await relay.close();
  await relay.close();
  await assert.rejects(request(port, '/', { host: new URL(relay.origin).host }));
});

test('startRelay rejects a gateway origin that is not a bare http(s) origin, an empty token, a malformed nonce or boot secret', async () => {
  const good = { origin: 'http://127.0.0.1:6060', token: TOKEN };
  await assert.rejects(startRelay({ ...good, origin: 'http://127.0.0.1:6060/login' }, { nonce: NONCE, boot: BOOT }));
  await assert.rejects(startRelay({ ...good, origin: 'ftp://127.0.0.1' }, { nonce: NONCE, boot: BOOT }));
  await assert.rejects(startRelay({ ...good, token: '   ' }, { nonce: NONCE, boot: BOOT }));
  await assert.rejects(startRelay(good, { nonce: 'not-hex', boot: BOOT }));
  await assert.rejects(startRelay(good, { nonce: NONCE, boot: 'nope' }));
});

test('a free preferred port is reused and a taken one falls back to another port', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const first = await startTestRelay(gatewayPort);
  t.after(() => gateway.close());
  const second = await startTestRelay(gatewayPort, { preferredPort: first.port });
  t.after(() => second.close());
  assert.equal(first.port, relayPort(first.origin));
  assert.notEqual(second.port, first.port, 'the port the first relay holds cannot be taken twice');
  const freed = first.port;
  await first.close();
  const third = await startTestRelay(gatewayPort, { preferredPort: freed });
  t.after(() => third.close());
  assert.equal(third.port, freed, 'a freed preferred port is reused, so the origin stays stable');
});

test('onRequest observes admitted requests by method and path only', async (t) => {
  const gateway = startMockGateway();
  const gatewayPort = await gateway.listen();
  const observed: string[] = [];
  const relay = await startTestRelay(gatewayPort, { onRequest: (method, path) => observed.push(`${method} ${path}`) });
  t.after(async () => {
    await relay.close();
    await gateway.close();
  });
  await request(relayPort(relay.origin), '/api/chat/sessions?project_id=p1', { host: new URL(relay.origin).host });
  await request(relayPort(relay.origin), '/', { host: `127.0.0.1:${relayPort(relay.origin)}` });
  assert.deepEqual(observed, ['GET /api/chat/sessions?project_id=p1']);
});
