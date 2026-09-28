import { describe, expect, it, vi } from 'vitest'

import { FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION } from './forebrainGatewayRuntime'
import { createForebrainGatewaySessionSocket, type GatewaySessionSocketLike } from './forebrainGatewaySessionSocket'

class FakeSocket implements GatewaySessionSocketLike {
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  sent: string[] = []
  closed = false

  send(data: string): void {
    this.sent.push(data)
  }

  close(): void {
    this.closed = true
    this.readyState = 3
    this.onclose?.()
  }

  open() {
    this.readyState = 1
    this.onopen?.()
  }

  message(payload: unknown) {
    this.onmessage?.({ data: JSON.stringify(payload) })
  }

  fail() {
    this.onerror?.()
  }

  shutdown() {
    this.readyState = 3
    this.onclose?.()
  }
}

describe('forebrain gateway session socket', () => {
  it('binds session on open and updates session from session_bound', () => {
    const socket = new FakeSocket()
    let sessionId = 'default'
    const controller = createForebrainGatewaySessionSocket({
      getSessionId: () => sessionId,
      setSessionId: (next) => {
        sessionId = next
      },
      createSocket: () => socket,
    })

    controller.start()
    socket.open()

    expect(JSON.parse(socket.sent[0] ?? '{}')).toMatchObject({
      op: 'bind_session',
      session_id: 'default',
	  protocol_version: FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION,
    })

    socket.message({
      op: 'session_bound',
      session_id: 'settings-session',
    })

    expect(sessionId).toBe('settings-session')
  })

  it('forwards non-session-bound messages to onMessage', () => {
    const socket = new FakeSocket()
    const onMessage = vi.fn()
    const controller = createForebrainGatewaySessionSocket({
      getSessionId: () => 'default',
      createSocket: () => socket,
      onMessage,
    })

    controller.start()
    socket.open()
    socket.message({
      op: 'task_notification',
      data: {
        event: 'progress',
        task: { id: 'task-1', title: 'Skill lifecycle', result: '{}' },
      },
    })

    expect(onMessage).toHaveBeenCalledTimes(1)
    expect(onMessage.mock.calls[0]?.[0]).toMatchObject({ op: 'task_notification' })
  })

  it('reconnects after close until stopped', () => {
    vi.useFakeTimers()
    try {
      const sockets: FakeSocket[] = []
      const controller = createForebrainGatewaySessionSocket({
        getSessionId: () => 'default',
        reconnectDelayMs: 250,
        createSocket: () => {
          const socket = new FakeSocket()
          sockets.push(socket)
          return socket
        },
      })

      controller.start()
      expect(sockets).toHaveLength(1)
      sockets[0]?.shutdown()
      vi.advanceTimersByTime(249)
      expect(sockets).toHaveLength(1)
      vi.advanceTimersByTime(1)
      expect(sockets).toHaveLength(2)

      controller.stop()
      sockets[1]?.shutdown()
      vi.advanceTimersByTime(500)
      expect(sockets).toHaveLength(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('sends payload only when socket is open', () => {
    const socket = new FakeSocket()
    const controller = createForebrainGatewaySessionSocket({
      getSessionId: () => 'default',
      createSocket: () => socket,
    })

    controller.start()
    expect(controller.send({ op: 'noop' })).toBe(false)
    socket.open()
    expect(controller.send({ op: 'noop' })).toBe(true)
	expect(JSON.parse(socket.sent[socket.sent.length - 1] ?? '{}')).toEqual({
	  op: 'noop',
	  protocol_version: FOREBRAIN_GATEWAY_WS_PROTOCOL_VERSION,
	})
  })

  it('stops instead of reconnecting on an incompatible declared protocol', () => {
	const socket = new FakeSocket()
	const onError = vi.fn()
	const controller = createForebrainGatewaySessionSocket({
	  getSessionId: () => 'default',
	  createSocket: () => socket,
	  onError,
	})

	controller.start()
	socket.open()
	socket.message({ op: 'connected', data: { protocol_version: '2099-01-01' } })

	expect(socket.closed).toBe(true)
	expect(onError).toHaveBeenCalledTimes(1)
	expect(controller.isConnected()).toBe(false)
  })
})
