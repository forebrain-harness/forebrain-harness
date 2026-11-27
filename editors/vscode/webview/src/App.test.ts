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
      const hit = routes.slice().sort((a, b) => b.prefix.length - a.prefix.length).find((r) => path.startsWith(r.prefix))
      const status = hit ? (hit.status ?? 200) : 404
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

  it('parks the plan approval where the input is, and gives the input back after', async () => {
    routes.push({
      prefix: '/api/actions',
      body: [{ id: 'act-exit', kind: 'exit_plan_mode', status: 'pending', session_id: 's-9', payload_json: '{}' }],
    })
    const wrapper = mountApp()
    await settle()
    transport.onHostMessage({ type: 'openSession', sessionId: 's-9' })
    await settle()

    const dock = wrapper.findComponent({ name: 'PlanApprovalDock' })
    expect(dock.exists(), 'the planner waits in the dock').toBe(true)
    expect(wrapper.findComponent(ComposerBar).attributes('style') ?? '', 'the input steps aside').toContain('display: none')

    // Moving to a conversation with nothing parked gives the input back.
    routes.push({ prefix: '/api/chat/sessions/s-10/messages', body: [] })
    routes.length = 0
    routes.push(
      { prefix: '/api/chat/sessions/s-10/messages', body: [] },
      { prefix: '/api/chat/sessions', body: { records: [], total: 0 } },
      { prefix: '/api/actions', body: [] },
    )
    transport.onHostMessage({ type: 'openSession', sessionId: 's-10' })
    await settle()
    expect(wrapper.findComponent({ name: 'PlanApprovalDock' }).exists()).toBe(false)
    expect(wrapper.findComponent(ComposerBar).attributes('style') ?? '').not.toContain('display: none')
  })

  it('opens the project consent from the pending rows and answers row by row', async () => {
    routes.push(
      {
        prefix: '/api/v1/projects/p-1/servers/pending',
        body: { mcp: [{ name: 'codegraph', fingerprint: 'fp-1' }], lsp: [], autoAllow: false },
      },
      { prefix: '/api/v1/projects/p-1/servers/consent', body: { ok: true } },
    )
    const wrapper = mountApp()
    await settle()
    // The pill picks the project the conversation runs in; the panel then asks
    // about the servers that project would start.
    await wrapper.findComponent(ComposerBar).vm.$emit('pick-target', { projectId: 'p-1', name: 'repo', root: '/ws/repo' })
    await settle()

    const dialog = wrapper.findComponent({ name: 'ProjectServersConsentDialog' })
    expect(dialog.exists(), 'the consent is a panel surface').toBe(true)
    expect(JSON.stringify(dialog.props())).toContain('codegraph')

    await dialog.vm.$emit('confirm', { allowed: { codegraph: true }, alwaysAllow: true })
    await settle()
    const sent = lastFrame<HttpFrame>((frame) => frame.method === 'http' && String(frame.params?.path ?? '').includes('/servers/consent'))
    expect(sent, 'the answer goes to the gateway').toBeTruthy()
    expect(sent!.params.method.toUpperCase()).toBe('POST')
  })

  it('toggles the todo checklist from the chip and keeps an update unread until read', async () => {
    const wrapper = mountApp()
    await settle()
    transport.onHostMessage({ type: 'openSession', sessionId: 's-9' })
    await settle()
    await wrapper.findComponent(ComposerBar).vm.$emit('send', { text: 'go' })
    await settle()
    const start = lastFrame<WsSendFrame>((frame) => frame.method === 'ws.send')
    const requestId = (JSON.parse(start!.params.data) as { request_id: string }).request_id

    const items = [
      { content: '读 manifest', status: 'completed' },
      { content: '跑门禁', status: 'in_progress' },
      { content: '装机', status: 'pending' },
    ]
    transport.onHostMessage({
      type: 'wsFrame',
      socket: 2,
      frames: [JSON.stringify({ op: 'run_event', request_id: requestId, data: { type: 'plan_updated', payload: { items } } })],
    })
    await settle()

    const bar = wrapper.findComponent(ComposerBar)
    expect(bar.props('todoCountText')).toBe('1/3')
    expect(wrapper.find('[data-testid="todo-list"]').exists(), 'the checklist starts closed').toBe(false)
    await bar.vm.$emit('toggle-todos')
    await settle()
    expect(wrapper.get('[data-testid="todo-list"]').text()).toContain('跑门禁')

    // A changed row lands unread and stays unread until the reader says so.
    // The first payload is all new, so read it once before the update lands.
    await wrapper.findComponent({ name: 'TodoPanel' }).vm.$emit('read-all')
    await settle()
    expect(wrapper.findComponent({ name: 'TodoPanel' }).props('unread')).toEqual([])
    const next = items.map((item, index) => (index === 2 ? { ...item, status: 'completed' } : item))
    transport.onHostMessage({
      type: 'wsFrame',
      socket: 2,
      frames: [JSON.stringify({ op: 'run_event', request_id: requestId, data: { type: 'plan_updated', payload: { items: next } } })],
    })
    await settle()
    expect(wrapper.findComponent({ name: 'TodoPanel' }).props('unread')).toEqual([2])
    await wrapper.findComponent({ name: 'TodoPanel' }).vm.$emit('read-all')
    await settle()
    expect(wrapper.findComponent({ name: 'TodoPanel' }).props('unread')).toEqual([])
  })
})

