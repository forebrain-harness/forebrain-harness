// The loopback relay: the one place where the gateway token meets a browser.
//
// The panel's iframe loads the gateway's Web UI from this relay instead of from
// the gateway itself, because the gateway's session cookie is SameSite=Strict
// and a page inside a VS Code webview is a cross-site frame: Chromium neither
// stores nor sends that cookie there. The relay fixes it without touching the
// harness: every upstream request carries `Authorization: Bearer <token>`, and
// the Web UI never needs a cookie.
//
// The relay's origin is the capability. Its hostname carries a 128-bit nonce
// (`fb-<nonce>.localhost`), and a request is admitted only when its Host header
// names exactly that origin, so another page or process that has not seen the
// iframe URL cannot use the relay to act as the token holder. A second secret
// (`boot`) rides in the iframe URL so that even a page that learned the origin
// cannot embed the Web UI. The extension generates both once and keeps them in
// its secret storage; the port is sticky while free; the relay lives only as
// long as the panel does.
import http, { request as httpRequest } from 'node:http';
import { request as httpsRequest } from 'node:https';
import net from 'node:net';
import tls from 'node:tls';
import type { Duplex } from 'node:stream';

export interface RelayTarget {
  /** The gateway's HTTP(S) origin, e.g. `http://127.0.0.1:6060` (no path, no query). */
  origin: string;
  /** The gateway token. It is only ever written into upstream request headers. */
  token: string;
}

export interface RelayOptions {
  /**
   * The capability half of the relay's hostname: 32 lower-case hex characters.
   * The caller generates it once and keeps it, so the hostname half of the
   * origin stays the same between sessions.
   */
  nonce: string;
  /**
   * A second secret, independent of the nonce and never written to any log: the
   * panel's iframe URL carries it as `?boot=<boot>`, and a navigation is
   * admitted only with it. The origin (nonce and port) is written to the output
   * channel for the person to read; a page that learns the origin from there
   * still cannot embed the Web UI and click-jack it.
   */
  boot: string;
  /**
   * The port to try first. The Web UI keeps its own preferences in
   * localStorage, which is keyed by origin, and the origin includes the port:
   * reusing the previous port keeps those preferences. `0` (the default) lets
   * the OS choose; a taken port falls back to that too.
   */
  preferredPort?: number;
  /** Observes every admitted request for diagnostics: the method and path only. */
  onRequest?: (method: string, path: string) => void;
}

export interface Relay {
  /** The origin the panel's iframe loads: `http://fb-<nonce>.localhost:<port>`. */
  readonly origin: string;
  /** The port that was bound: remember it to pass back as `preferredPort`. */
  readonly port: number;
  /** Stops listening and closes every relayed connection. Safe to call twice. */
  close(): Promise<void>;
}

// Hop-by-hop headers belong to one connection and are never forwarded
// (RFC 9110 §7.6.1). `connection` and `upgrade` are re-added by the upgrade
// path, which writes its own request line.
const HOP_BY_HOP = new Set([
  'connection', 'keep-alive', 'proxy-connection', 'te', 'trailer',
  'transfer-encoding', 'upgrade', 'proxy-authenticate', 'proxy-authorization',
]);

// Credentials and context that must not cross the relay: the browser's own
// cookies and the frame's Origin/Referer would either leak into the gateway's
// cookie logic or be replaced by the token. `host` is rewritten explicitly.
const NEVER_FORWARD = new Set([
  'cookie', 'set-cookie', 'origin', 'referer', 'authorization', 'x-api-key', 'host',
]);

type ForwardedHeaders = Record<string, string>;

function forwardHeaders(
  headers: http.IncomingHttpHeaders,
  upstreamHost: string,
  authorization: string,
): ForwardedHeaders {
  const out: ForwardedHeaders = {};
  for (const [name, value] of Object.entries(headers)) {
    const key = name.toLowerCase();
    if (value === undefined || HOP_BY_HOP.has(key) || NEVER_FORWARD.has(key)) continue;
    out[key] = Array.isArray(value) ? value.join(', ') : value;
  }
  out.host = upstreamHost;
  out.authorization = authorization;
  return out;
}

function responseHeaders(headers: http.IncomingHttpHeaders): ForwardedHeaders {
  const out: ForwardedHeaders = {};
  for (const [name, value] of Object.entries(headers)) {
    const key = name.toLowerCase();
    if (value === undefined || HOP_BY_HOP.has(key) || key === 'set-cookie') continue;
    out[key] = Array.isArray(value) ? value.join(', ') : value;
  }
  return out;
}

/**
 * Whether a request may use the relay. The Host check is the capability check;
 * the Fetch Metadata checks are defence in depth for what a browser does with
 * the origin once a page has it:
 *  - a navigation is admitted only as the panel's own iframe load carrying the
 *    boot secret;
 *  - everything else must be same-origin with the relay (the Web UI's own
 *    fetches, scripts and styles), never a cross-site embed from another page.
 * A client that sends no Fetch Metadata headers is not a browser (the unit
 * tests); the Host check has already gated it.
 */
function bootQuery(url: string): string | null {
  try {
    return new URL(url, 'http://relay.invalid').searchParams.get('boot');
  } catch {
    return null;
  }
}

function admitted(req: http.IncomingMessage, expectedHost: string, boot: string): boolean {
  if (req.headers.host !== expectedHost) return false;
  // A proxy-style absolute URL would send the relay somewhere else entirely.
  if (!req.url || !req.url.startsWith('/')) return false;
  const site = req.headers['sec-fetch-site'];
  if (site === undefined) return true;
  if (req.headers['sec-fetch-mode'] === 'navigate') {
    // The panel's own iframe load: the boot secret is what tells it apart from
    // another page that learned the origin and tries to embed the same URL.
    return req.headers['sec-fetch-dest'] === 'iframe' && bootQuery(req.url) === boot;
  }
  return site === 'same-origin';
}

/**
 * Starts the relay on 127.0.0.1. `target.origin` must be an absolute http(s)
 * origin; the promise rejects for anything else.
 */
export async function startRelay(target: RelayTarget, options: RelayOptions): Promise<Relay> {
  const upstream = new URL(target.origin);
  if (upstream.origin !== target.origin || (upstream.protocol !== 'http:' && upstream.protocol !== 'https:')) {
    await Promise.reject(new Error(`gateway origin must be an http(s) origin without a path: ${target.origin}`));
  }
  const token = target.token.trim();
  if (!token) {
    await Promise.reject(new Error('gateway token is empty'));
  }
  if (!/^[0-9a-f]{32}$/.test(options.nonce)) {
    await Promise.reject(new Error('relay nonce must be 32 lower-case hex characters'));
  }
  if (!/^[0-9a-f]{32}$/.test(options.boot)) {
    await Promise.reject(new Error('relay boot secret must be 32 lower-case hex characters'));
  }
  const preferredPort = options.preferredPort ?? 0;
  const authorization = `Bearer ${token}`;
  const isTls = upstream.protocol === 'https:';
  const request = isTls ? httpsRequest : httpRequest;
  const nonce = options.nonce;
  const boot = options.boot;
  const onRequest = options.onRequest;
  const sockets = new Set<Duplex>();

  const server = http.createServer((req, res) => {
    sockets.add(req.socket);
    req.socket.once('close', () => sockets.delete(req.socket));
    if (!admitted(req, expectedHost, boot)) {
      res.writeHead(404, { 'content-type': 'text/plain' });
      res.end('not found');
      return;
    }
    onRequest?.(req.method ?? '', req.url ?? '');
    const preq = request(
      {
        protocol: upstream.protocol,
        hostname: upstream.hostname,
        port: upstream.port || (isTls ? 443 : 80),
        method: req.method,
        path: req.url,
        headers: forwardHeaders(req.headers, upstream.host, authorization),
      },
      (pres) => {
        res.writeHead(pres.statusCode ?? 502, responseHeaders(pres.headers));
        pres.pipe(res);
      },
    );
    preq.on('error', () => {
      if (!res.headersSent) res.writeHead(502, { 'content-type': 'text/plain' });
      res.end('gateway unreachable');
    });
    req.pipe(preq);
  });

  // A WebSocket handshake is relayed as raw bytes: the browser's handshake
  // headers are forwarded with the token added and the Origin dropped (the
  // gateway's default origin check passes a handshake without one), and the
  // gateway's `101` answer reaches the browser unchanged.
  server.on('upgrade', (req, client, head) => {
    if (!admitted(req, expectedHost, boot) || req.headers.origin !== origin) {
      client.destroy();
      return;
    }
    const headers = forwardHeaders(req.headers, upstream.host, authorization);
    headers.connection = 'Upgrade';
    headers.upgrade = 'websocket';
    const connect = isTls
      ? () => tls.connect({ host: upstream.hostname, port: Number(upstream.port || 443), servername: upstream.hostname })
      : () => net.connect({ host: upstream.hostname, port: Number(upstream.port || 80) });
    const upstreamSocket = connect();
    sockets.add(client);
    sockets.add(upstreamSocket);
    const drop = () => {
      sockets.delete(client);
      sockets.delete(upstreamSocket);
      client.destroy();
      upstreamSocket.destroy();
    };
    for (const s of [client, upstreamSocket]) {
      s.on('error', drop);
      s.on('close', drop);
    }
    upstreamSocket.once(isTls ? 'secureConnect' : 'connect', () => {
      let raw = `${req.method} ${req.url} HTTP/1.1\r\n`;
      for (const [name, value] of Object.entries(headers)) raw += `${name}: ${value}\r\n`;
      raw += '\r\n';
      upstreamSocket.write(raw);
      if (head.length > 0) upstreamSocket.write(head);
      client.pipe(upstreamSocket);
      upstreamSocket.pipe(client);
    });
  });

  await new Promise<void>((resolve, reject) => {
    const onError = (err: NodeJS.ErrnoException) => {
      if (err.code === 'EADDRINUSE' && preferredPort !== 0) {
        // The preferred port is taken: any port still serves, the origin just
        // changes and the Web UI starts from its default preferences.
        server.listen(0, '127.0.0.1', () => resolve());
        return;
      }
      reject(err);
    };
    server.on('error', onError);
    server.once('listening', () => {
      server.removeListener('error', onError);
      server.on('error', () => {}); // a later error is logged by the caller's close path
      resolve();
    });
    server.listen(preferredPort, '127.0.0.1');
  });
  const address = server.address();
  if (address === null || typeof address === 'string') {
    throw new Error('relay did not bind a TCP port');
  }
  const expectedHost = `fb-${nonce}.localhost:${address.port}`;
  const origin = `http://${expectedHost}`;

  let closed: Promise<void> | undefined;
  return {
    origin,
    port: address.port,
    close() {
      closed ??= (async () => {
        for (const s of sockets) s.destroy();
        await new Promise<void>((resolve) => server.close(() => resolve()));
      })();
      return closed;
    },
  };
}
