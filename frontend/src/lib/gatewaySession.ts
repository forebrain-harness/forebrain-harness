/**
 * The browser's gateway session: a cookie the gateway sets after the user
 * presents the gateway token once. Nothing here ever holds the token — the
 * cookie rides along with same-origin requests automatically, which is the
 * whole point of the design.
 */

export type GatewaySessionState = 'ok' | 'required'

let probe: Promise<GatewaySessionState> | null = null

const unauthorizedHandlers: Array<() => void> = []

/**
 * Ask the gateway whether this browser already holds a session. The result is
 * cached: the router guard runs on every navigation and must not re-probe
 * after the answer is known. Only 'required' is certain — anything else
 * (including the server being unreachable) leaves page-level loading states
 * to report their own errors.
 */
export function probeGatewaySession(): Promise<GatewaySessionState> {
  probe ??= fetch('/api/auth/session').then(
    (res) => (res.status === 401 ? 'required' : 'ok'),
    () => 'ok',
  )
  return probe
}

/** Record that a session now exists, without re-probing the server. */
export function markGatewaySessionOk(): void {
  probe = Promise.resolve('ok')
}

/** Forget the cached answer, e.g. because the server just answered 401. */
function resetGatewaySessionProbe(): void {
  probe = null
}

/** Register the handler the router points at the sign-in page. */
export function onGatewayUnauthorized(handler: () => void): void {
  unauthorizedHandlers.push(handler)
}

/** Announce that the session is no longer valid; api.ts calls this on 401. */
export function reportGatewayUnauthorized(): void {
  resetGatewaySessionProbe()
  for (const handler of [...unauthorizedHandlers]) {
    try {
      handler()
    } catch {
      // A broken handler must not block the others.
    }
  }
}

/** Exchange the gateway token for the session cookie. */
export async function signInToGateway(token: string): Promise<void> {
  const res = await fetch('/api/auth/session', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token }),
  })
  if (res.ok) return
  let message = `HTTP ${res.status}`
  try {
    const data: unknown = await res.json()
    if (data !== null && typeof data === 'object' && 'error' in data) {
      const error = (data as { error: unknown }).error
      if (typeof error === 'string' && error) message = error
    }
  } catch {
    // Non-JSON body: keep the status text.
  }
  throw new Error(message)
}

/** Read the token a sign-in link carries in `#token=…`, if any. */
export function tokenFromLocationHash(hash: string): string {
  const match = /(?:^|&)token=([^&]*)/.exec(hash.replace(/^#/, ''))
  return match ? decodeURIComponent(match[1]) : ''
}
