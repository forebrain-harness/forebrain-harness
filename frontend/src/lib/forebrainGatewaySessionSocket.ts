import {
  buildBrowserForebrainGatewayChatWsUrl,
  parseForebrainGatewayServerHello,
  parseForebrainSessionBoundMessage,
  FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION,
  type ForebrainSessionBoundMessage,
} from './forebrainGatewayRuntime'

const WS_OPEN = 1
const DEFAULT_RECONNECT_DELAY_MS = 1000

export interface GatewaySessionSocketMessageEventLike {
  data: string
}

export interface GatewaySessionSocketLike {
  readyState: number
  onopen: (() => void) | null
  onmessage: ((event: GatewaySessionSocketMessageEventLike) => void) | null
  onclose: (() => void) | null
  onerror: (() => void) | null
  send(data: string): void
  close(): void
}

export interface GatewaySessionSocketOptions {
  getSessionId: () => string
  setSessionId?: (sessionId: string) => void
  createSocket?: (url: string) => GatewaySessionSocketLike
  reconnectDelayMs?: number
  onMessage?: (raw: unknown) => void
  onSessionBound?: (msg: ForebrainSessionBoundMessage) => void
  onOpen?: () => void
  onClose?: (willReconnect: boolean) => void
  onError?: () => void
}

export interface GatewaySessionSocketController {
  start: () => void
  stop: () => void
  send: (payload: unknown) => boolean
  isConnected: () => boolean
}

function defaultCreateSocket(url: string): GatewaySessionSocketLike {
  if (typeof WebSocket === 'undefined') {
    throw new Error('WebSocket unavailable')
  }
  return new WebSocket(url) as unknown as GatewaySessionSocketLike
}

export function createForebrainGatewaySessionSocket(
  options: GatewaySessionSocketOptions,
): GatewaySessionSocketController {
  const reconnectDelayMs = Math.max(100, options.reconnectDelayMs ?? DEFAULT_RECONNECT_DELAY_MS)
  const createSocket = options.createSocket ?? defaultCreateSocket
  let socket: GatewaySessionSocketLike | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let stopped = false

  function clearReconnectTimer() {
    if (reconnectTimer != null) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
  }

  function currentSessionId(): string {
    const sessionId = options.getSessionId()?.trim()
    return sessionId || 'default'
  }

  function bindSession(ws: GatewaySessionSocketLike) {
    ws.send(JSON.stringify({
      op: 'bind_session',
      protocol_version: FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION,
      request_id: `gateway-bind-${Date.now()}`,
      session_id: currentSessionId(),
    }))
  }

  function connect() {
    if (stopped) return
    if (socket && socket.readyState <= WS_OPEN) return
    clearReconnectTimer()
    const ws = createSocket(buildBrowserForebrainGatewayChatWsUrl())
    socket = ws
    ws.onopen = () => {
      options.onOpen?.()
      bindSession(ws)
    }
    ws.onmessage = (event) => {
      let raw: unknown
      try {
        raw = JSON.parse(String(event.data))
      } catch {
        return
      }
	  const hello = parseForebrainGatewayServerHello(raw)
	  if (hello?.protocolVersion && hello.protocolVersion !== FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION) {
		stopped = true
		options.onError?.()
		ws.close()
		return
	  }
      const sessionBound = parseForebrainSessionBoundMessage(raw)
      if (sessionBound?.sessionId) {
        options.setSessionId?.(sessionBound.sessionId)
        options.onSessionBound?.(sessionBound)
        return
      }
      options.onMessage?.(raw)
    }
    ws.onclose = () => {
      if (socket === ws) {
        socket = null
      }
      const willReconnect = !stopped
      options.onClose?.(willReconnect)
      if (willReconnect) {
        reconnectTimer = setTimeout(() => {
          reconnectTimer = null
          connect()
        }, reconnectDelayMs)
      }
    }
    ws.onerror = () => {
      options.onError?.()
    }
  }

  return {
    start() {
      stopped = false
      connect()
    },
    stop() {
      stopped = true
      clearReconnectTimer()
      const ws = socket
      socket = null
      if (ws) {
        ws.close()
      }
    },
    send(payload: unknown) {
      if (!socket || socket.readyState !== WS_OPEN) return false
	  const versioned = payload && typeof payload === 'object' && !Array.isArray(payload)
		? { ...(payload as Record<string, unknown>), protocol_version: FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION }
		: payload
      socket.send(JSON.stringify(versioned))
      return true
    },
    isConnected() {
      return Boolean(socket && socket.readyState === WS_OPEN)
    },
  }
}
