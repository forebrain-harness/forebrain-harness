// A stand-in gateway chat endpoint for the host socket's tests: the same shape
// the real one has at `/ws/chat`, with the two roles the gateway actually plays.
//
// An `observer` connection (one that only ever binds a session) receives the
// `connected` frame, gets `session_bound` and the history replay, and is then
// fed `run_event` frames only — the real gateway never writes `requires_action`
// or `run_completed` to it, because those go to the connection that started the
// run. An `initiator` connection is the one that sent `start_run`: it gets
// `run_started`, and whatever the script says after it.
import http from 'node:http';
import { WebSocketServer, type WebSocket } from 'ws';

type Json = Record<string, unknown>;

export type MockRole = 'observer' | 'initiator';

export interface MockChatServerOptions {
  /** The bearer token the handshake must carry; anything else is destroyed mid-handshake. */
  readonly token?: string;
  /** Default `observer`. */
  readonly role?: MockRole;
  /** The run id an accepted `start_run` answers with. */
  readonly runId?: string;
  /** Pushed right after `session_bound`, the way the gateway replays history. */
  readonly replay?: readonly Json[];
  /** Pushed instead of `run_started`; gets the request id the client is waiting on. */
  readonly startRunReply?: (requestId: string, frame: Json) => readonly Json[];
}

export interface MockChatServer {
  readonly origin: string;
  readonly token: string;
  /** The headers of every upgrade request, accepted or refused. */
  readonly handshakes: readonly http.IncomingHttpHeaders[];
  /** Every frame the client sent, in arrival order. */
  readonly frames: readonly Json[];
  /** Broadcast to every open connection (a scripted live event). */
  send(message: Json): void;
  /** Destroy every open connection, as a server restart or a read timeout would. */
  drop(): void;
  /** Refuse the next `count` upgrades during the handshake. */
  refuseNext(count: number): void;
  close(): Promise<void>;
}

export async function startMockChatServer(options: MockChatServerOptions = {}): Promise<MockChatServer> {
  const token = options.token ?? 'test-token';
  const role = options.role ?? 'observer';
  const handshakes: http.IncomingHttpHeaders[] = [];
  const frames: Json[] = [];
  const live = new Set<WebSocket>();
  let refuse = 0;
  let requestCounter = 0;

  const server = http.createServer((_req, res) => {
    res.writeHead(404);
    res.end();
  });
  const wss = new WebSocketServer({ noServer: true });

  const write = (ws: WebSocket, message: Json): void => {
    if (ws.readyState === ws.OPEN) ws.send(JSON.stringify(message));
  };

  server.on('upgrade', (req, socket, head) => {
    handshakes.push(req.headers);
    const authorization = req.headers.authorization ?? '';
    if (authorization !== `Bearer ${token}` || refuse > 0) {
      if (refuse > 0) refuse -= 1;
      socket.destroy();
      return;
    }
    wss.handleUpgrade(req, socket, head, (ws) => {
      live.add(ws);
      ws.on('close', () => live.delete(ws));
      ws.on('message', (raw: Buffer) => {
        const frame = JSON.parse(raw.toString()) as Json;
        frames.push(frame);
        if (frame.op === 'bind_session') {
          const requestId = String(frame.request_id ?? '');
          write(ws, {
            op: 'session_bound',
            request_id: requestId,
            session_id: frame.session_id,
            data: { cursor: Number(frame.cursor ?? 0), high_water: 1000, schema_version: 1 },
          });
          for (const event of options.replay ?? []) write(ws, { op: 'run_event', data: event });
        } else if (frame.op === 'start_run' && role === 'initiator') {
          const requestId = String(frame.request_id ?? '');
          const scripted = options.startRunReply?.(requestId, frame);
          if (scripted) {
            for (const message of scripted) write(ws, message);
          } else {
            write(ws, {
              op: 'run_started',
              request_id: requestId,
              run_id: options.runId ?? `run-${(requestCounter += 1)}`,
              session_id: frame.session_id,
            });
          }
        }
      });
      // The gateway announces every accepted connection, unprompted.
      write(ws, { op: 'connected' });
    });
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const address = server.address();
  if (address === null || typeof address === 'string') throw new Error('mock chat server did not bind a TCP port');

  return {
    origin: `http://127.0.0.1:${address.port}`,
    token,
    handshakes,
    frames,
    send(message: Json): void {
      for (const ws of live) write(ws, message);
    },
    drop(): void {
      for (const ws of live) ws.terminate();
      live.clear();
    },
    refuseNext(count: number): void {
      refuse = count;
    },
    close: () =>
      new Promise<void>((resolve) => {
        for (const ws of live) ws.terminate();
        live.clear();
        wss.close(() => resolve());
        server.close(() => resolve());
        server.closeAllConnections();
      }),
  };
}