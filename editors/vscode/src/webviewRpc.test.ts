import test from 'node:test';
import assert from 'node:assert/strict';
import { trustBranchOf, WebviewRpc, type RpcSocket, type WebviewRpcDeps } from './webviewRpc';

const TOKEN = 'sekrit-token';
const ORIGIN = 'http://127.0.0.1:8123';

/** A `ws`-shaped fake the tests drive by hand. */
class FakeSocket implements RpcSocket {
  readonly listeners = new Map<string, ((...args: unknown[]) => void)[]>();
  sent: string[] = [];
  closed = false;

  send(data: string): void {
    this.sent.push(data);
  }

  close(): void {
    this.closed = true;
  }

  on(event: 'message', listener: (raw: unknown) => void): void;
  on(event: 'open' | 'close', listener: () => void): void;
  on(event: 'error', listener: (err: Error) => void): void;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  on(event: string, listener: (...args: any[]) => void): void {
    const list = this.listeners.get(event) ?? [];
    list.push(listener);
    this.listeners.set(event, list);
  }

  emit(event: string, ...args: unknown[]): void {
    for (const listener of this.listeners.get(event) ?? []) listener(...args);
  }
}

interface Harness {
  rpc: WebviewRpc;
  posted: unknown[];
  sockets: FakeSocket[];
  logs: string[];
}

function makeHarness(overrides: Partial<WebviewRpcDeps> = {}): Harness {
  const posted: unknown[] = [];
  const sockets: FakeSocket[] = [];
  const logs: string[] = [];
  const deps: WebviewRpcDeps = {
    post: (message) => posted.push(message),
    origin: ORIGIN,
    token: () => TOKEN,
    fetchImpl: async () => new Response('{"ok":true}', { status: 200, headers: { 'content-type': 'application/json' } }),
    log: (line) => logs.push(line),
    workspaceRoots: () => ['/ws/repo'],
    workspaceTrusted: () => true,
    trustSet: async () => {},
    showWarningModal: async () => undefined,
    bindSession: async () => {},
    openChatSession: async () => {},
    reportActiveSession: async () => {},
    openFileRange: async () => {},
    socketFactory: () => {
      throw new Error('the tests inject their own sockets');
    },
    ...overrides,
  };
  const rpc = new WebviewRpc({
    ...deps,
    socketFactory: (): RpcSocket => {
      const socket = new FakeSocket();
      sockets.push(socket);
      // The `ws` socket reports its own open asynchronously; do it now.
      queueMicrotask(() => socket.emit('open'));
      return socket;
    },
  });
  return { rpc, posted, sockets, logs };
}

/** The rpc replies posted so far, as {rpc, ok, ...} records. */
function replies(posted: unknown[]): { rpc: number; ok: boolean; result?: unknown; error?: string }[] {
  return posted.filter((m) => (m as { type?: string }).type === 'rpc') as {
    rpc: number;
    ok: boolean;
    result?: unknown;
    error?: string;
  }[];
}

test('the rest proxy adds the bearer header, drops client auth attempts and answers the body', async () => {
  let seen: { input: string; init: RequestInit } | undefined;
  const { rpc, posted, logs } = makeHarness({
    fetchImpl: async (input, init) => {
      seen = { input, init };
      return new Response('{"records":[]}', {
        status: 200,
        headers: { 'content-type': 'application/json', 'set-cookie': 'session=x', authorization: 'Bearer nope' },
      });
    },
  });
  await rpc.handleRpc(1, 'http', {
    method: 'GET',
    path: '/api/chat/sessions/s1/events?cursor=0',
    headers: { authorization: 'Bearer forged', 'content-type': 'application/json' },
  });

  assert.ok(seen);
  assert.equal(seen.input, `${ORIGIN}/api/chat/sessions/s1/events?cursor=0`);
  assert.equal(seen.init.method, 'GET');
  const headers = seen.init.headers as Record<string, string>;
  assert.equal(headers.authorization, `Bearer ${TOKEN}`, 'the host adds the only bearer header');
  assert.ok(!('cookie' in headers));
  const reply = replies(posted)[0];
  assert.equal(reply.ok, true);
  const result = reply.result as { status: number; headers: Record<string, string>; body: string };
  assert.equal(result.status, 200);
  assert.equal(result.body, '{"records":[]}');
  // Response headers the page reads come through; credential-ish ones do not.
  assert.equal(result.headers['content-type'], 'application/json');
  assert.equal(result.headers['set-cookie'], undefined);
  assert.equal(result.headers.authorization, undefined);
  // The log carries the path without its query and never the token.
  assert.ok(logs.some((line) => line.includes('GET /api/chat/sessions/s1/events -> 200')));
  assert.ok(!logs.some((line) => line.includes(TOKEN)));
});

test('the rest proxy carries a binary body through and returns array buffers', async () => {
  let seen: { init: RequestInit } | undefined;
  const { rpc, posted } = makeHarness({
    fetchImpl: async (_input, init) => {
      seen = { init };
      return new Response(new Uint8Array([1, 2, 3]), { status: 200, headers: { 'content-type': 'application/zip' } });
    },
  });
  const upload = new TextEncoder().encode('PK zip bytes').buffer;
  await rpc.handleRpc(2, 'http', { method: 'POST', path: '/api/files', body: upload, binary: true });

  const body = seen?.init.body as ArrayBuffer;
  assert.ok(body instanceof ArrayBuffer);
  const reply = replies(posted)[0];
  const result = reply.result as { body: ArrayBuffer };
  assert.ok(result.body instanceof ArrayBuffer);
  assert.equal(new Uint8Array(result.body).length, 3);
});

test('rpcCancel unwinds a request still in flight', async () => {
  const { rpc, posted } = makeHarness({
    fetchImpl: (_input, init) =>
      new Promise<Response>((_resolve, reject) => {
        init.signal?.addEventListener('abort', () => reject(new Error('This operation was aborted')));
      }),
  });
  const pending = rpc.handleRpc(3, 'http', { method: 'GET', path: '/api/chat/sessions/s1/events' });
  rpc.onMessage({ type: 'rpcCancel', rpc: 3 });
  await pending;

  const reply = replies(posted)[0];
  assert.equal(reply.rpc, 3);
  assert.equal(reply.ok, false);
  assert.match(reply.error ?? '', /abort/i);
});

test('an unknown method answers with an error and nothing else', async () => {
  const { rpc, posted } = makeHarness();
  await rpc.handleRpc(4, 'gateway.deleteEverything', {});
  const reply = replies(posted)[0];
  assert.equal(reply.ok, false);
  assert.match(reply.error ?? '', /unknown rpc method/);
});

test('a path outside /api/ is refused before anything dials, so the bearer goes nowhere', async () => {
  const dialed: string[] = [];
  const { rpc, posted } = makeHarness({
    fetchImpl: async (input) => {
      dialed.push(String(input));
      return new Response('{}', { status: 200 });
    },
  });
  await rpc.handleRpc(21, 'http', { method: 'GET', path: '/health' });

  const reply = replies(posted)[0];
  assert.equal(reply.ok, false);
  assert.match(reply.error ?? '', /not a gateway path/);
  assert.deepEqual(dialed, [], 'no dial, no credentials');
});

test('a gateway that never answers is cut off, not waited on forever', async () => {
  // AbortSignal.timeout's timer is unref'ed: without a held handle the test
  // runner would drain the event loop and cancel the wait instead of timing
  // the gateway out.
  const keepAlive = setTimeout(() => {}, 5_000);
  const { rpc, posted } = makeHarness({
    httpTimeoutMs: 50,
    fetchImpl: (_input, init) => new Promise<Response>((_resolve, reject) => {
      init.signal?.addEventListener('abort', () => reject(new Error('This operation was aborted')));
    }),
  });
  await rpc.handleRpc(25, 'http', { method: 'GET', path: '/api/chat/sessions/s1/events' });
  clearTimeout(keepAlive);

  const reply = replies(posted)[0];
  assert.equal(reply.ok, false);
  assert.match(reply.error ?? '', /abort/i);
});

test('a chat socket opens, carries sends and announces its states', async () => {
  const { rpc, posted, sockets } = makeHarness();
  await rpc.handleRpc(5, 'ws.open', {});
  assert.equal(sockets.length, 1);
  const state = posted.find((m) => (m as { type?: string }).type === 'wsState') as { socket: number; up: boolean };
  assert.equal(state.up, true);

  await rpc.handleRpc(6, 'ws.send', { socket: state.socket, data: '{"op":"bind_session"}' });
  assert.deepEqual(sockets[0].sent, ['{"op":"bind_session"}']);

  sockets[0].emit('close');
  const closing = posted.filter((m) => (m as { type?: string }).type === 'wsState') as { up: boolean }[];
  assert.deepEqual(closing.map((m) => m.up), [true, false]);

  await rpc.handleRpc(7, 'ws.close', { socket: state.socket });
  assert.equal(sockets[0].closed, true);
});

test('frames cross byte-identical: the webview reducer sees what onmessage saw', async () => {
  const { rpc, posted, sockets } = makeHarness();
  await rpc.handleRpc(8, 'ws.open', {});
  const runEvent = JSON.stringify({
    op: 'run_event',
    protocol_version: '2026-04-18',
    request_id: 'req-9',
    session_id: 's1',
    data: { sequence: 41, type: 'delta', payload: { text: '增量‹›' } },
  });
  sockets[0].emit('message', runEvent);
  await new Promise<void>((resolve) => setImmediate(resolve));

  const batch = posted.find((m) => (m as { type?: string }).type === 'wsFrame') as { frames: string[] };
  assert.deepEqual(batch.frames, [runEvent]);
});

test('a 5k-frame burst arrives complete and in order inside one postMessage', async () => {
  const { rpc, posted, sockets } = makeHarness();
  await rpc.handleRpc(9, 'ws.open', {});
  for (let i = 0; i < 5000; i += 1) {
    sockets[0].emit('message', JSON.stringify({ op: 'run_event', data: { sequence: i, type: 'delta' } }));
  }
  await new Promise<void>((resolve) => setImmediate(resolve));

  const batches = posted.filter((m) => (m as { type?: string }).type === 'wsFrame') as { frames: string[] }[];
  const frames = batches.flatMap((batch) => batch.frames);
  assert.equal(frames.length, 5000, 'nothing dropped');
  assert.deepEqual(
    frames.map((frame) => (JSON.parse(frame) as { data: { sequence: number } }).data.sequence),
    Array.from({ length: 5000 }, (_v, i) => i),
    'arrival order preserved',
  );
  assert.equal(batches.length, 1, 'one turn of the event loop, one postMessage');
});

test('bind follows the webview to the conversation it bound', async () => {
  const bound: string[] = [];
  const { rpc, posted } = makeHarness({
    bindSession: async (sessionId) => {
      bound.push(sessionId);
    },
  });
  await rpc.handleRpc(10, 'bind', { sessionId: 's-42' });
  assert.deepEqual(bound, ['s-42']);
  assert.equal(replies(posted)[0].ok, true);

  await rpc.handleRpc(11, 'bind', { sessionId: '   ' });
  assert.equal(replies(posted)[1].ok, false);
});

test('openSession hands the pick to the host; reportActiveSession records the screen', async () => {
  const opened: string[] = [];
  const reported: string[] = [];
  const { rpc, posted } = makeHarness({
    openChatSession: async (sessionId) => {
      opened.push(sessionId);
    },
    reportActiveSession: async (sessionId) => {
      reported.push(sessionId);
    },
  });
  await rpc.handleRpc(22, 'openSession', { sessionId: 's-7' });
  await rpc.handleRpc(23, 'reportActiveSession', { sessionId: 's-7' });
  assert.deepEqual(opened, ['s-7']);
  assert.deepEqual(reported, ['s-7']);
  assert.equal(replies(posted)[0].ok, true);
  assert.equal(replies(posted)[1].ok, true);

  // An empty pick is the "new chat" button: open chat, steer nothing.
  await rpc.handleRpc(24, 'openSession', { sessionId: '  ' });
  assert.equal(replies(posted)[2].ok, true);
  assert.deepEqual(opened, ['s-7', '']);

  await rpc.handleRpc(25, 'reportActiveSession', { sessionId: '  ' });
  assert.equal(replies(posted)[3].ok, false, 'an empty active session is refused');
});

test('openFileRange reuses the host opener behind the shape check', async () => {
  const opened: unknown[] = [];
  const { rpc, posted } = makeHarness({
    openFileRange: async (request) => {
      opened.push(request);
    },
  });
  await rpc.handleRpc(12, 'openFileRange', { path: 'pkg/a.go', startLine: 3, endLine: 7 });
  assert.deepEqual(opened, [{ path: 'pkg/a.go', startLine: 3, endLine: 7 }]);

  await rpc.handleRpc(13, 'openFileRange', { path: 'pkg/a.go', startLine: 7, endLine: 3 });
  assert.equal(replies(posted)[1].ok, false, 'an inverted range is refused before anything opens');
});

test('trust branch one: inside the workspace writes the record and resolves yes', async () => {
  const written: [string, boolean][] = [];
  const { rpc, posted } = makeHarness({
    workspaceRoots: () => ['/ws/repo'],
    trustSet: async (root, trusted) => {
      written.push([root, trusted]);
    },
  });
  const yes = await rpc.handleRpc(14, 'trust.ask', { root: '/ws/repo/sub/dir' }).then(
    () => replies(posted)[0].result,
  );
  assert.equal(yes, true);
  assert.deepEqual(written, [['/ws/repo/sub/dir', true]]);
});

test('trust branch one still resolves yes when the record write fails', async () => {
  const { rpc, posted } = makeHarness({
    workspaceRoots: () => ['/ws/repo'],
    trustSet: async () => {
      throw new Error('gateway down');
    },
  });
  await rpc.handleRpc(15, 'trust.ask', { root: '/ws/repo' });
  const reply = replies(posted)[0];
  assert.equal(reply.ok, true);
  assert.equal(reply.result, true, 'the prompt is an added gate, not the mechanism');
});

test('trust branch two: outside the workspace asks in a native modal', async () => {
  const written: [string, boolean][] = [];
  const modals: { message: string; detail: string; confirm: string; cancel: string }[] = [];
  const strings = { title: '信任这个文件夹吗？', body: '信任后可编辑这里的文件。', confirm: '信任并继续', once: '只问一次。', cancel: '取消' };
  const { rpc, posted } = makeHarness({
    workspaceRoots: () => ['/ws/repo'],
    trustSet: async (root, trusted) => {
      written.push([root, trusted]);
    },
    showWarningModal: async (message, detail, confirmLabel, cancelLabel) => {
      modals.push({ message, detail, confirm: confirmLabel, cancel: cancelLabel });
      return '信任并继续';
    },
  });
  await rpc.handleRpc(16, 'trust.ask', { root: '/elsewhere/proj', strings });
  assert.equal(replies(posted)[0].result, true);
  assert.deepEqual(written, [['/elsewhere/proj', true]]);
  assert.equal(modals.length, 1);
  assert.ok(modals[0].message.includes(strings.title) && modals[0].message.includes(strings.body));
  assert.ok(modals[0].detail.includes('/elsewhere/proj') && modals[0].detail.includes(strings.once));
  assert.equal(modals[0].confirm, strings.confirm);

  // A dismissal answers no and writes nothing.
  await rpc.handleRpc(17, 'trust.ask', {
    root: '/elsewhere/proj',
    strings: { ...strings, confirm: 'Trust and continue' },
  });
  assert.equal(replies(posted)[1].result, false);
  assert.equal(written.length, 1);
});

test('trust branch three: an untrusted workspace refuses without asking', async () => {
  const modals: unknown[] = [];
  const written: unknown[] = [];
  const { rpc, posted } = makeHarness({
    workspaceTrusted: () => false,
    trustSet: async (root, trusted) => {
      written.push([root, trusted]);
    },
    showWarningModal: async () => {
      modals.push(true);
      return '信任并继续';
    },
  });
  await rpc.handleRpc(18, 'trust.ask', { root: '/ws/repo' });
  assert.equal(replies(posted)[0].result, false);
  assert.deepEqual(modals, []);
  assert.deepEqual(written, []);
});

test('the sibling-prefix trap: /a/proj is not inside /a/project, and the reverse holds', () => {
  assert.equal(trustBranchOf('/a/proj', ['/a/project'], true), 'outside');
  assert.equal(trustBranchOf('/a/project', ['/a/proj'], true), 'outside');
  assert.equal(trustBranchOf('/a/proj/sub', ['/a/proj'], true), 'inside');
  assert.equal(trustBranchOf('/a/proj', ['/a/proj'], true), 'inside');
  // A `..` climb out of the folder is refused by the same gate.
  assert.equal(trustBranchOf('/a/proj/../secret', ['/a/proj'], true), 'outside');
});

test('dispose closes every socket the webview left open', async () => {
  const { rpc, sockets } = makeHarness();
  await rpc.handleRpc(19, 'ws.open', {});
  await rpc.handleRpc(20, 'ws.open', {});
  rpc.dispose();
  assert.deepEqual(sockets.map((socket) => socket.closed), [true, true]);
});
