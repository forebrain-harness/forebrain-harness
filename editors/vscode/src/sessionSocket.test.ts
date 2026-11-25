import test from 'node:test';
import assert from 'node:assert/strict';
import { SessionSocket, TurnRefusedError, WS_PROTOCOL_VERSION, type ServerMessage } from './sessionSocket';
import { startMockChatServer, type MockChatServer, type MockChatServerOptions } from './testing/mockChatServer';

type Json = Record<string, unknown>;

const TOKEN = 'test-token';
const BUSY = 'This conversation is already running a turn; send again when it finishes.';

interface Harness {
  readonly socket: SessionSocket;
  readonly logs: string[];
  readonly links: boolean[];
  /** Resolves on the first frame the listener sees that matches, or rejects after `timeoutMs`. */
  waitFor(predicate: (msg: ServerMessage) => boolean, timeoutMs?: number): Promise<ServerMessage>;
}

function client(origin: string, overrides: { getToken?: () => string } = {}): Harness {
  const logs: string[] = [];
  const links: boolean[] = [];
  const waiting: { predicate: (msg: ServerMessage) => boolean; resolve: (msg: ServerMessage) => void }[] = [];
  const socket = new SessionSocket({
    origin,
    getToken: overrides.getToken ?? (() => TOKEN),
    log: (line) => logs.push(line),
    listener: (msg) => {
      for (const waiter of [...waiting]) {
        if (!waiter.predicate(msg)) continue;
        waiting.splice(waiting.indexOf(waiter), 1);
        waiter.resolve(msg);
      }
    },
    onLink: (up) => links.push(up),
  });
  return {
    socket,
    logs,
    links,
    waitFor(predicate, timeoutMs = 2_000) {
      return new Promise<ServerMessage>((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error('no matching frame arrived in time')), timeoutMs);
        waiting.push({
          predicate,
          resolve: (msg) => {
            clearTimeout(timer);
            resolve(msg);
          },
        });
      });
    },
  };
}

/** Polls a condition the server side can only answer by recording what it received. */
async function waitUntil(predicate: () => boolean, timeoutMs: number, what: string): Promise<number> {
  const start = Date.now();
  while (!predicate()) {
    if (Date.now() - start > timeoutMs) throw new Error(`timed out after ${Date.now() - start} ms waiting for ${what}`);
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  return Date.now() - start;
}

async function pairs(
  t: { after(fn: () => Promise<void> | void): void },
  options: MockChatServerOptions = {},
): Promise<{ server: MockChatServer; h: Harness }> {
  const server = await startMockChatServer(options);
  const h = client(server.origin);
  t.after(async () => {
    await h.socket.close();
    await server.close();
  });
  return { server, h };
}

function bindFrames(server: MockChatServer): readonly Json[] {
  return server.frames.filter((f) => f.op === 'bind_session');
}

test('the handshake carries the bearer token and no origin, and open() waits for the connected frame', async (t) => {
  const { server, h } = await pairs(t);
  let opened = false;
  const opening = h.socket.open().then(() => {
    opened = true;
  });
  await opening;
  assert.equal(opened, true);
  assert.deepEqual(h.links, [true], 'the link is up exactly when the connected frame arrived');
  assert.equal(server.handshakes.length, 1);
  assert.equal(server.handshakes[0].authorization, `Bearer ${TOKEN}`);
  assert.equal(server.handshakes[0].origin, undefined, 'no browser origin: the gateway rejects one');
});

test('a bind frame is exactly the four fields the protocol names, plus its request id', async (t) => {
  const { server, h } = await pairs(t);
  await h.socket.open();
  await h.socket.bind('s1');
  const [frame] = bindFrames(server);
  assert.ok(frame, 'the server received a bind_session');
  assert.equal(typeof frame.request_id, 'string');
  assert.deepEqual(frame, {
    op: 'bind_session',
    protocol_version: WS_PROTOCOL_VERSION,
    request_id: frame.request_id,
    session_id: 's1',
    cursor: 0,
  });
  assert.deepEqual(Object.keys(frame).sort(), ['cursor', 'op', 'protocol_version', 'request_id', 'session_id']);
  assert.equal(h.socket.boundSession, 's1');
});

test('the history replay reaches the listener, and the last sequence is what a rebind would resume from', async (t) => {
  const replay = [
    { id: 'e1', sequence: 4, type: 'turn_started', run_id: 'r1' },
    { id: 'e2', sequence: 7, type: 'tool_call_started', run_id: 'r1' },
  ];
  const { h } = await pairs(t, { replay });
  await h.socket.open();
  // Not "already arrived when bind resolved": the server sends session_bound
  // first and replays after it, so the assertion waits for the frame itself.
  const last = h.waitFor((m) => m.op === 'run_event' && (m.data as Json).sequence === 7);
  await h.socket.bind('s1');
  const event = await last;
  assert.deepEqual((event.data as Json).type, 'tool_call_started');
  assert.equal(h.socket.lastSequence, 7);
});

test('start_run carries the turn text and resolves with the run id the gateway assigned', async (t) => {
  const { server, h } = await pairs(t, { role: 'initiator', runId: 'run-42' });
  await h.socket.open();
  assert.equal(await h.socket.startRun('s1', 'hello there'), 'run-42');
  const frame = server.frames.find((f) => f.op === 'start_run');
  assert.ok(frame, 'the server received a start_run');
  assert.deepEqual(frame, {
    op: 'start_run',
    protocol_version: WS_PROTOCOL_VERSION,
    request_id: frame.request_id,
    session_id: 's1',
    message: { content: 'hello there', role: 'user' },
  });
});

test('a refused turn rejects its own start_run with the gateway text and code', async (t) => {
  const { h } = await pairs(t, {
    role: 'initiator',
    startRunReply: (requestId) => [
      { op: 'turn_withdrawn', request_id: requestId, error: BUSY, data: { code: 'session_running' } },
    ],
  });
  await h.socket.open();
  await assert.rejects(
    h.socket.startRun('s1', 'again'),
    (err: unknown) =>
      err instanceof TurnRefusedError && err.message === BUSY && err.code === 'session_running',
  );
});

test('an observer is told about an approval through a run_event, and asks for nothing', async (t) => {
  const { server, h } = await pairs(t);
  await h.socket.open();
  await h.socket.bind('s1');
  const announced = h.waitFor((m) => m.op === 'run_event' && (m.data as Json).type === 'approval_requested');
  server.send({
    op: 'run_event',
    data: { id: 'e3', sequence: 9, type: 'approval_requested', run_id: 'r1', payload: { action_id: 'a9', action_kind: 'shell' } },
  });
  const event = await announced;
  assert.deepEqual((event.data as Json).payload, { action_id: 'a9', action_kind: 'shell' });
  assert.deepEqual(
    server.frames.map((f) => f.op),
    ['bind_session'],
    'binding is all this connection ever sends: no requires_action, no approval op',
  );
});

test('a dropped link reconnects by itself and rebinds from the last sequence it saw', async (t) => {
  const { server, h } = await pairs(t, { replay: [{ id: 'e1', sequence: 7, type: 'turn_started', run_id: 'r1' }] });
  await h.socket.open();
  const first = h.waitFor((m) => m.op === 'run_event');
  await h.socket.bind('s1');
  await first;
  assert.equal(h.socket.lastSequence, 7);

  server.drop();
  await waitUntil(() => bindFrames(server).length === 2, 1_500, 'the automatic rebind');
  assert.equal(bindFrames(server)[1].cursor, 7, 'the rebind resumes from the last sequence seen');
  assert.equal(bindFrames(server)[1].session_id, 's1');
  assert.deepEqual(h.links, [true, false, true], 'the link goes down before it comes back up');
});

test('close() is final: no reconnect, no second error, no further open()', async (t) => {
  const { server, h } = await pairs(t);
  await h.socket.open();
  await h.socket.bind('s1');
  const handshakes = server.handshakes.length;
  await h.socket.close();
  await new Promise((resolve) => setTimeout(resolve, 900)); // longer than the first backoff
  assert.equal(server.handshakes.length, handshakes, 'a closed socket never reconnects');
  assert.ok(!h.logs.some((line) => line.includes('reconnecting in')), 'close() schedules no retry');
  await h.socket.close();
  await assert.rejects(h.socket.open(), /closed/);
});

test('a connection the server drops, and then refuses, comes back within the backoff budget', async (t) => {
  const { server, h } = await pairs(t, { replay: [{ id: 'e1', sequence: 3, type: 'turn_started', run_id: 'r1' }] });
  await h.socket.open();
  const first = h.waitFor((m) => m.op === 'run_event');
  await h.socket.bind('s1');
  await first;
  assert.deepEqual(h.links, [true]);

  // A server that hangs up and then refuses two handshakes: 0.5 s, then 1 s.
  server.refuseNext(2);
  server.drop();
  const elapsed = await waitUntil(() => h.links.at(-1) === true && h.links.length > 1, 8_500, 'the link to come back');
  assert.ok(elapsed <= 8_500, `recovered in ${elapsed} ms`);
  assert.deepEqual([h.links[0], h.links.at(-1)], [true, true]);
  assert.ok(h.links.slice(1, -1).every((up) => up === false), `every attempt in between was down: ${h.links}`);
  await waitUntil(() => bindFrames(server).length === 2, 1_500, 'the rebind after recovery');
  assert.equal(bindFrames(server)[1].cursor, 3, 'the rebind resumes from the last sequence seen');
});