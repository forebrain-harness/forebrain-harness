export const REQUEST_TIMEOUT_MS = 5_000;

/** The gateway answered, but not the way the caller hoped. */
export class GatewayHttpError extends Error {
  constructor(readonly status: number, readonly path: string, body: string) {
    super(`${path} answered ${status}: ${body.slice(0, 200)}`);
  }
}

/** Nothing answered, or nothing answered in time: the gateway is down, not unhappy. */
export class GatewayUnreachableError extends Error {
  constructor(readonly path: string) {
    super(`${path} did not answer`);
  }
}

export type FetchLike = (input: string, init: RequestInit) => Promise<Response>;

/** What `GET /api/auth/session` said about a token. */
export type ProbeResult = 'ok' | 'rejected' | 'unreachable';

export class GatewayClient {
  constructor(readonly origin: string, private readonly fetchImpl: FetchLike = fetch) {}

  /** One GET. Nothing answered in time is `'unreachable'`; any answer is returned as is. */
  private async get(path: string, token?: string): Promise<Response | 'unreachable'> {
    const headers: Record<string, string> = {};
    if (token !== undefined) headers.authorization = `Bearer ${token}`;
    try {
      return await this.fetchImpl(`${this.origin}${path}`, {
        method: 'GET',
        headers,
        signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
      });
    } catch {
      return 'unreachable';
    }
  }

  /** `GET /health` is unauthenticated: it says whether a gateway listens here. */
  async health(): Promise<boolean> {
    const res = await this.get('/health');
    return res !== 'unreachable' && res.status === 200;
  }

  /** 200 = accepted, 401 = rejected, no answer = unreachable; any other status is a GatewayHttpError. */
  async probe(token: string): Promise<ProbeResult> {
    const res = await this.get('/api/auth/session', token);
    if (res === 'unreachable') return 'unreachable';
    if (res.status === 200) return 'ok';
    if (res.status === 401) return 'rejected';
    throw new GatewayHttpError(res.status, '/api/auth/session', await res.text());
  }

  /** GET with the token; no answer is a GatewayUnreachableError, any non-2xx answer a GatewayHttpError. */
  async getJson<T>(path: string, token: string): Promise<T> {
    const res = await this.get(path, token);
    return this.answer<T>(path, res);
  }

  /** POST a JSON body with the token; same errors as getJson. */
  async postJson<T>(path: string, token: string, body: unknown): Promise<T> {
    let res: Response | 'unreachable';
    try {
      res = await this.fetchImpl(`${this.origin}${path}`, {
        method: 'POST',
        headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' },
        body: JSON.stringify(body),
        signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
      });
    } catch {
      res = 'unreachable';
    }
    return this.answer<T>(path, res);
  }

  private async answer<T>(path: string, res: Response | 'unreachable'): Promise<T> {
    if (res === 'unreachable') throw new GatewayUnreachableError(path);
    if (!res.ok) throw new GatewayHttpError(res.status, path, await res.text());
    return (await res.json()) as T;
  }
}