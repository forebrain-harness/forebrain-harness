import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import App from './App.vue'
import ComposerBar from './components/ComposerBar.vue'
import { installWebviewTransport, TRANSPORT_KEY, type WebviewTransport } from './transport'
import { setForebrainChatSocketTransport, setForebrainFetchTransport, setForebrainHttpAdapter } from '@/lib/api'

/**
 * The workbench against a scripted host: HTTP answers routed by path, chat
 * sockets opened and pumped by hand. The assertions that matter here are the
 * two the reviewer asked for — a session pick rebinds the observer socket
 * (switching never silently keeps the old conversation's stream), and a
 * refused turn says so instead of failing quietly.
 */

interface HttpFrame { type: 'rpc'; rpc: number; method: 'http'; params: { method: string; path: string } }
interface WsOpenFrame { type: 'rpc'; rpc: number; method: 'ws.open' }
interface WsSendFrame { type: 'rpc'; rpc: number; method: 'ws.send'; params: { socket: number; data: string } }
interface AnyFrame { type?: string; rpc?: number; method?: string; params?: Record<string, unknown> }

const routes: Array<{ prefix: string; status?: number; body?: unknown }> = []
let socketSeq = 0

const fakeHost = {
  posted: [] as AnyFrame[],
  postMessage(message: unknown) {
    const frame = message as AnyFrame
    this.posted.push(frame)
    if (frame.type === 'rpc' && frame.method === 'http') {
      const http = frame as HttpFrame
      const path = String(http.params?.path ?? '')
      const hit = routes.find((r) => path.startsWith(r.prefix))
      const status = hit?.status ?? 404
      const body = hit?.body ?? { error: 'no route' }
      queueMicrotask(() => {
        transport.onHostMessage({
          type: 'rpc', rpc: frame.rpc, ok: true,
          result: { status, statusText: '', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body) },
        })
      })
      return
    }
    if (frame.type === 'rpc' && frame.method === 'ws.open') {
      const open = frame as WsOpenFrame
      socketSeq += 1
      const id = socketSeq
      queueMicrotask(() => transport.onHostMessage({ type: 'rpc', rpc: open.rpc, ok: true, result: { socket: id } }))
      // The up-push rides a later macrotask on purpose: the webview registers
      // the socket id in a microtask after the answer, and a state frame that
      // lands before that registration would be dropped.
      setTimeout(() => transport.onHostMessage({ type: 'wsState', socket: id, up: true }), 0)
    }
  },
}

let transport: WebviewTransport

const tick = () => new Promise((resolve) => setTimeout(resolve, 0))
async function settle() {
  for (let i = 0; i < 6; i += 1) await tick()
}

function lastFrame<T extends AnyFrame>(predicate: (frame: AnyFrame) => boolean): T | undefined {
  return fakeHost.posted.filter(predicate).at(-1) as T | undefined
}

beforeEach(() => {
  routes.length = 0
  socketSeq = 0
  fakeHost.posted.length = 0
  routes.push(
    // The messages read answers an array, the list routes answer pages.
    { prefix: '/api/chat/sessions/s-9/messages', body: [] },
    { prefix: '/api/chat/sessions', body: { records: [], total: 0 } },
    { prefix: '/api/v1/projects', body: { records: [] } },
    { prefix: '/api/chat/model', body: { records: [], status: [] } },
  )
  ;(globalThis as { acquireVsCodeApi?: unknown }).acquireVsCodeApi = () => fakeHost
  transport = installWebviewTransport()
})

afterEach(() => {
  setForebrainHttpAdapter(null)
  setForebrainFetchTransport(null)
  setForebrainChatSocketTransport(null)
  delete (globalThis as { acquireVsCodeApi?: unknown }).acquireVsCodeApi
})

function mountApp() {
  return mount(App, {
    global: { provide: { [TRANSPORT_KEY as symbol]: transport } },
  })
}

describe('the webview chat workbench', () => {
  it('draws the fresh chat: the hero the prototype wrote, the starters, the pill', async () => {
    const wrapper = mountApp()
    await settle()
    expect(wrapper.get('[data-testid="webview-empty-hero"]').text()).toContain('要说点什么')
    expect(wrapper.findAll('[data-testid="webview-starter"]').length).toBe(2)
    expect(wrapper.findComponent(ComposerBar).findComponent({ name: 'ProjectPill' }).exists()).toBe(true)
  })

  it('rebinds the observer socket when the Sessions view hands it a conversation', async () => {
    const wrapper = mountApp()
    await settle()
    transport.onHostMessage({ type: 'openSession', sessionId: 's-9' })
    await settle()

    expect(lastFrame<WsOpenFrame>((frame) => frame.method === 'ws.open'), 'the observer socket dials through the host').toBeTruthy()
    const bind = lastFrame<WsSendFrame>((frame) => frame.method === 'ws.send')
    expect(bind, 'the rebind sends bind_session').toBeTruthy()
    const op = JSON.parse(bind!.params.data) as { op: string; session_id: string }
    expect(op.op).toBe('bind_session')
    expect(op.session_id).toBe('s-9')

    // A second pick rebinds again: the stream follows the conversation.
    transport.onHostMessage({ type: 'openSession', sessionId: 's-10' })
    await settle()
    const bindAgain = lastFrame<WsSendFrame>((frame) => frame.method === 'ws.send')
    expect(JSON.parse(bindAgain!.params.data).session_id).toBe('s-10')
    void wrapper
  })

  it('says why a turn was refused instead of failing quietly', async () => {
    const wrapper = mountApp()
    await settle()
    transport.onHostMessage({ type: 'openSession', sessionId: 's-9' })
    await settle()
    await wrapper.findComponent(ComposerBar).vm.$emit('send', { text: 'hello' })
    await settle()

    const startRun = lastFrame<WsSendFrame>((frame) => frame.method === 'ws.send')
    expect(startRun, 'the send dials its own socket and starts the run').toBeTruthy()
    const startRunOp = JSON.parse(startRun!.params.data) as { op: string; request_id: string }
    expect(startRunOp.op).toBe('start_run')
    const sendSocket = lastFrame<{ type?: string; socket?: number; params?: { socket: number } }>(
      (frame) => frame.method === 'ws.open',
    )
    void sendSocket

    // The gateway refuses the turn: the run errors, the thread says so.
    transport.onHostMessage({
      type: 'wsFrame',
      socket: 2,
      frames: [JSON.stringify({ op: 'run_error', request_id: startRunOp.request_id, runId: 'r-1', error: '网关拒绝了这个回合' })],
    })
    await settle()

    expect(wrapper.text()).toContain('网关拒绝了这个回合')
    // The send button is the send again, not a stop for a run that is over.
    expect(wrapper.findComponent(ComposerBar).props('submitLabel')).toBe('发送')
  })
})
