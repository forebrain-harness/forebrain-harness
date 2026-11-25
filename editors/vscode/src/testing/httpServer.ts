import http from 'node:http';

export interface SeenRequest {
  method: string;
  path: string;
  headers: http.IncomingHttpHeaders;
  /** The request body as text; empty for a request that sent none. */
  body: string;
}

/** A loopback HTTP server that answers by path and records what it received. */
export interface TestServer {
  readonly origin: string;
  readonly port: number;
  readonly seen: SeenRequest[];
  close(): Promise<void>;
}

export interface TestServerOptions {
  /** Status code per request path (query included); a path missing here answers 404. */
  routes?: Record<string, number>;
  /** Paths that never answer, so the caller's own timeout is what ends them. */
  hang?: string[];
  /** Response body for every answered request. */
  body?: string;
}

/**
 * The stand-in gateway: the same shape the tests need from the real one — an
 * HTTP origin that answers by path and shows what reached it.
 */
export async function startTestServer(options: TestServerOptions = {}): Promise<TestServer> {
  const seen: SeenRequest[] = [];
  const server = http.createServer(async (req, res) => {
    const record: SeenRequest = { method: req.method ?? '', path: req.url ?? '', headers: req.headers, body: '' };
    seen.push(record);
    for await (const chunk of req) record.body += chunk;
    if (options.hang?.includes(req.url ?? '')) return;
    res.writeHead(options.routes?.[req.url ?? ''] ?? 404, { 'content-type': 'application/json' });
    res.end(options.body ?? '');
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const address = server.address();
  if (address === null || typeof address === 'string') throw new Error('test server did not bind a TCP port');
  return {
    origin: `http://127.0.0.1:${address.port}`,
    port: address.port,
    seen,
    close: () => new Promise<void>((resolve) => {
      server.close(() => resolve());
      server.closeAllConnections();
    }),
  };
}

/** A loopback port with nothing listening on it (bind it, read the number, release it). */
export async function closedPort(): Promise<number> {
  const server = http.createServer();
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const address = server.address();
  if (address === null || typeof address === 'string') throw new Error('no port to release');
  await new Promise<void>((resolve) => server.close(() => resolve()));
  return address.port;
}