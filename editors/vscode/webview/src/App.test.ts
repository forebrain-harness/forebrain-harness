import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import App from './App.vue'
import ComposerBar from './components/ComposerBar.vue'
import { installWebviewTransport, TRANSPORT_KEY, type WebviewTransport } from './transport'
import { setForebrainChatSocketTransport, setForebrainFetchTransport, setForebrainHttpAdapter } from '@/lib/api'
import { useWorkbench } from '@/composables/useWorkbench'

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

// The workbench's open state is the module's own, so a wrapper left mounted
// from an earlier test would react to the next test's open and consume its
// requested tab first. Every wrapper goes down with its test.
enableAutoUnmount(afterEach)

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
  // The workbench remembers the tab a conversation last read in localStorage,
  // and its open/requested state is the module's own (one tab session); one
  // test's memory is not the next test's starting state.
  localStorage.clear()
  const bench = useWorkbench()
  bench.close()
  bench.consumed()
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

/**
 * The faces the panel owed the web's conversation page (owner 2026-11-27:
 * everything that is not a background batch project gets filled in). Each one
 * is the web's own component, driven here over the same transport.
 */
describe('the webview chat workbench — the faces it owed', () => {
  const sessionRoute = { prefix: '/api/chat/sessions/s-9', body: { id: 's-9', title: '面板', project: { id: 'p-1', name: 'repo', root: '/ws/repo', lastAttachDir: 'shots' } } }

  it('opens the peer drawer and the workbench from the header, and the context ring lands on the context tab', async () => {
    routes.push({ prefix: '/api/agents/roster', body: { records: [] } })
    const wrapper = mountApp()
    await settle()
    transport.onHostMessage({ type: 'openSession', sessionId: 's-9' })
    await settle()

    expect(wrapper.findComponent({ name: 'PeerDrawer' }).exists()).toBe(false)
    await wrapper.get('[data-testid="header-peers"]').trigger('click')
    await settle()
    expect(wrapper.findComponent({ name: 'PeerDrawer' }).exists(), 'the header opens the peers').toBe(true)
    await wrapper.findComponent({ name: 'PeerDrawer' }).vm.$emit('close')
    await settle()

    expect(wrapper.findComponent({ name: 'ChatWorkbench' }).exists()).toBe(false)
    await wrapper.get('[data-testid="header-workbench"]').trigger('click')
    await settle()
    const bench = wrapper.findComponent({ name: 'ChatWorkbench' })
    expect(bench.exists(), 'the header opens the workbench').toBe(true)
    expect(bench.props('overlay'), 'the narrow sidebar overlays rather than narrows').toBe(true)

    void wrapper
  })

  it('the context ring opens the workbench on the context tab, whatever was read last', async () => {
    routes.push({ prefix: '/api/agents/roster', body: { records: [] } })
    const wrapper = mountApp()
    await settle()
    transport.onHostMessage({ type: 'openSession', sessionId: 's-9' })
    await settle()
    expect(wrapper.findComponent({ name: 'ChatWorkbench' }).exists()).toBe(false)

    await wrapper.findComponent(ComposerBar).vm.$emit('open-context')
    await settle()
    const bench = wrapper.findComponent({ name: 'ChatWorkbench' })
    expect(bench.exists(), 'the ring opens the workbench').toBe(true)
    expect(wrapper.get('[data-testid="bench-tab-context"]').attributes('aria-selected'), 'it lands on the context').toBe('true')
  })

  it('carries this conversation’s heartbeat in the header, read from the gateway', async () => {
    routes.push({
      prefix: '/api/heartbeats',
      body: { records: [{ sessionId: 's-9', intervalSeconds: 900, prompt: '看一眼进度', paused: false }] },
    })
    const wrapper = mountApp()
    await settle()
    expect(wrapper.findComponent({ name: 'SessionHeartbeat' }).exists(), 'a fresh chat has no heartbeat of its own').toBe(false)
    transport.onHostMessage({ type: 'openSession', sessionId: 's-9' })
    await settle()

    const beat = wrapper.findComponent({ name: 'SessionHeartbeat' })
    expect(beat.exists(), 'the conversation’s own heartbeat sits in its header').toBe(true)
    expect(beat.props('sessionId')).toBe('s-9')
    expect(lastFrame<HttpFrame>((frame) => String(frame.params?.path ?? '').startsWith('/api/heartbeats')), 'the rows come over the transport').toBeTruthy()
  })

  it('browses the host’s folders from the pill and takes the pick as the target', async () => {
    routes.push({
      prefix: '/api/host/dirs',
      body: { path: '/ws', parent: '/', ancestors: ['/ws', '/'], places: [], entries: [{ name: 'repo', mtime: 1, blocked: false, project: { id: 'p-1', name: 'repo' } }] },
    })
    const wrapper = mountApp()
    await settle()
    expect(wrapper.findComponent({ name: 'FolderDialog' }).exists()).toBe(false)

    await wrapper.findComponent(ComposerBar).vm.$emit('browse-folder')
    await settle()
    const dialog = wrapper.findComponent({ name: 'FolderDialog' })
    expect(dialog.exists(), 'the pill’s browse opens the same folder browser the web uses').toBe(true)

    await dialog.vm.$emit('select', '/ws/repo')
    await settle()
    expect(wrapper.findComponent(ComposerBar).props('target')).toMatchObject({ root: '/ws/repo' })
    expect(wrapper.findComponent({ name: 'FolderDialog' }).exists()).toBe(false)
  })

  it('saves an attached file into the project before it uploads, and only on confirm', async () => {
    routes.push(sessionRoute, { prefix: '/api/v1/projects/p-1/dirs', body: { path: '', entries: [] } }, { prefix: '/api/files', body: { fileId: 'f-1' } })
    const wrapper = mountApp()
    await settle()
    transport.onHostMessage({ type: 'openSession', sessionId: 's-9' })
    await settle()

    const file = new File(['bytes'], 'paste.png', { type: 'image/png' })
    await wrapper.findComponent(ComposerBar).vm.$emit('files-picked', [{ file, filename: 'paste.png', mediaType: 'image/png', fromClipboard: true }])
    await settle()

    const dialog = wrapper.findComponent({ name: 'AttachSaveDialog' })
    expect(dialog.props('open'), 'the save-to dialog asks before anything uploads').toBe(true)
    expect(dialog.props('initialDir'), 'it opens on the project’s last attach directory').toBe('shots')
    expect(lastFrame<HttpFrame>((frame) => String(frame.params?.path ?? '') === '/api/files'), 'nothing uploaded yet').toBeFalsy()

    await dialog.vm.$emit('confirm', 'shots')
    await settle()
    const upload = lastFrame<HttpFrame>((frame) => String(frame.params?.path ?? '') === '/api/files')
    expect(upload, 'the confirm uploads over the transport').toBeTruthy()
    expect(upload!.params.method.toUpperCase()).toBe('POST')
    const body = (upload!.params as { body?: unknown }).body
    const text = body instanceof ArrayBuffer ? new TextDecoder().decode(body) : String(body ?? '')
    expect(text, 'the chosen directory rides the upload').toContain('shots')
    expect(text).toContain('paste.png')
    expect(wrapper.findComponent({ name: 'AttachSaveDialog' }).props('open')).toBe(false)
  })

  it('opens a whole command as a document tab of the drawer, and closes it again', async () => {
    const wrapper = mountApp()
    await settle()
    transport.onHostMessage({ type: 'openSession', sessionId: 's-9' })
    await settle()

    const tab = { id: 'doc:command:abc', kind: 'command' as const, label: '命令 ls', text: 'ls -la', command: 'ls -la' }
    await wrapper.findComponent({ name: 'ThreadView' }).vm.$emit('open-doc', tab)
    await settle()

    const drawer = wrapper.findComponent({ name: 'SubagentDrawer' })
    expect(drawer.exists(), 'a document opens the drawer even with no subagent tab').toBe(true)
    expect(drawer.props('docTabs')).toEqual([tab])
    expect(drawer.props('activeDoc')).toBe('doc:command:abc')
    expect(wrapper.findComponent({ name: 'ToolDocCommand' }).exists(), 'the whole command is the tab’s body').toBe(true)

    await drawer.vm.$emit('close-doc', 'doc:command:abc')
    await settle()
    expect(wrapper.findComponent({ name: 'SubagentDrawer' }).exists()).toBe(false)
  })

  it('opens the subagent a call card names, from the thread', async () => {
    const wrapper = mountApp()
    await settle()
    transport.onHostMessage({ type: 'openSession', sessionId: 's-9' })
    await settle()
    routes.push({ prefix: '/api/chat/sessions/s-9/subagents/agent-1/budget', body: { percentLeft: 70 } })
    await wrapper.findComponent({ name: 'ThreadView' }).vm.$emit('open-agent', 'agent-1')
    await settle()
    const drawer = wrapper.findComponent({ name: 'SubagentDrawer' })
    expect(drawer.exists(), 'a call card opens that subagent’s tab').toBe(true)
    expect(drawer.props('active')).toBe('agent-1')
    expect(wrapper.findComponent({ name: 'ThreadView' }).props('activeAgentId'), 'the thread marks the row as here').toBe('agent-1')
  })

  it('sends a settings page to the host’s browser, not to a router that is not here', async () => {
    routes.push(sessionRoute)
    const wrapper = mountApp()
    await settle()
    transport.onHostMessage({ type: 'openSession', sessionId: 's-9' })
    await settle()

    // A failed turn draws the same error card the web draws; its log button is
    // the settings page the panel has no native surface for.
    await wrapper.findComponent(ComposerBar).vm.$emit('send', { text: 'go' })
    await settle()
    const start = lastFrame<WsSendFrame>((frame) => frame.method === 'ws.send')
    const requestId = (JSON.parse(start!.params.data) as { request_id: string }).request_id
    transport.onHostMessage({
      type: 'wsFrame',
      socket: 2,
      frames: [JSON.stringify({ op: 'run_error', request_id: requestId, runId: 'r-1', error: '网关断了' })],
    })
    await settle()
    await wrapper.get('[data-testid="run-error-logs"]').trigger('click')
    await settle()

    const frame = lastFrame<{ type?: string; method?: string; params?: { path?: string } }>(
      (f) => f.method === 'openWebUi',
    )
    expect(frame, 'the page goes to the host, not to a router').toBeTruthy()
    expect(frame!.params!.path).toMatch(/^\/settings\?tab=logs&session=s-9&from=/)
  })
})

