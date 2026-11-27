import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readdirSync, readFileSync } from 'fs'
import { dirname, join } from 'path'
import { fileURLToPath } from 'url'

import { openForebrainChatSocket, setForebrainChatSocketTransport, setForebrainFetchTransport, setForebrainHttpAdapter } from '@/lib/api'
import { forebrainApi } from '@/lib/api'
import { onGatewayUnauthorized } from '@/lib/gatewaySession'
import { sendFileRangeToHost } from '@/lib/fileLineLinks'
import { bindHostChannel, HostSocket, installWebviewTransport, type WebviewTransport } from './transport'

/**
 * The transport's contract, proven against the shared seams it installs: an
 * axios round trip, a fetch write, a socket's frames, a file range, the theme
 * class — and the invariant that no bearer header or auth material ever
 * appears in webview source, because the host adds the only one.
 */

interface FakeHost {
  postMessage(message: unknown): void
  posted: unknown[]
}

const here = dirname(fileURLToPath(import.meta.url))

let fakeHost: FakeHost
let deliver: (raw: unknown) => void
let transport: WebviewTransport

beforeEach(() => {
  const posted: unknown[] = []
  const postMessage = vi.fn((message: unknown) => {
    posted.push(message)
  })
  fakeHost = { postMessage, posted }
  ;(globalThis as { acquireVsCodeApi?: unknown }).acquireVsCodeApi = () => fakeHost
  transport = installWebviewTransport()
  deliver = bindHostChannel(transport)
})

afterEach(() => {
  setForebrainHttpAdapter(null)
  setForebrainFetchTransport(null)
  setForebrainChatSocketTransport(null)
  delete (globalThis as { acquireVsCodeApi?: unknown }).acquireVsCodeApi
  document.documentElement.classList.remove('dark')
})

/** The single rpc frame posted so far. */
function lastRpc(): { rpc: number; method: string; params: Record<string, unknown> } {
  const frames = fakeHost.posted.filter((m) => (m as { type?: string }).type === 'rpc') as {
    rpc: number
    method: string
    params: Record<string, unknown>
  }[]
  expect(frames.length).toBeGreaterThan(0)
  return frames[frames.length - 1]
}

/** One macrotask: every microtask the axios interceptor chain still owes has
 *  run by the time this resolves, so a frame the call produced is on the wire. */
const tick = (): Promise<void> => new Promise((resolve) => setTimeout(resolve, 0))

describe('the axios adapter', () => {
  it('round-trips a read: path joined, answer parsed and camel-cased', async () => {
    const pending = forebrainApi.primaryAgents()
    await tick()
    const frame = lastRpc()
    expect(frame.method).toBe('http')
    expect(frame.params.method).toBe('GET')
    expect(frame.params.path).toBe('/api/agents/primary')
    deliver({ type: 'rpc', rpc: frame.rpc, ok: true, result: { status: 200, statusText: 'OK', headers: {}, body: JSON.stringify({ active_id: 'review' }) } })
    await expect(pending).resolves.toEqual({ activeId: 'review' })
  })

  it('serialises params the axios adapters do', async () => {
    const pending = forebrainApi.sessionEvents('s-1', { cursor: 5, limit: 10 })
    await tick()
    const frame = lastRpc()
    expect(String(frame.params.path)).toContain('/api/chat/sessions/s-1/events?')
    expect(String(frame.params.path)).toContain('cursor=5')
    expect(String(frame.params.path)).toContain('limit=10')
    deliver({ type: 'rpc', rpc: frame.rpc, ok: true, result: { status: 200, statusText: 'OK', headers: {}, body: '{}' } })
    await pending
  })

  it('rejects a 401 with the response attached, so the session watch fires', async () => {
    const unauthorized = vi.fn()
    onGatewayUnauthorized(unauthorized)
    const pending = forebrainApi.primaryAgents()
    await tick()
    const frame = lastRpc()
    deliver({ type: 'rpc', rpc: frame.rpc, ok: true, result: { status: 401, statusText: 'Unauthorized', headers: {}, body: '' } })
    await expect(pending).rejects.toMatchObject({ response: { status: 401 } })
    expect(unauthorized).toHaveBeenCalledTimes(1)
  })

  it('rejects a 500 through the settle the adapter owns', async () => {
    const pending = forebrainApi.primaryAgents()
    await tick()
    const frame = lastRpc()
    deliver({ type: 'rpc', rpc: frame.rpc, ok: true, result: { status: 500, statusText: 'Internal Server Error', headers: {}, body: 'boom' } })
    await expect(pending).rejects.toThrow(/Internal Server Error/)
  })

  it('surfaces a host refusal without an answer body', async () => {
    const pending = forebrainApi.primaryAgents()
    await tick()
    const frame = lastRpc()
    deliver({ type: 'rpc', rpc: frame.rpc, ok: false, error: 'the gateway did not answer' })
    await expect(pending).rejects.toThrow('the gateway did not answer')
  })
})

describe('the fetch shim', () => {
  it('carries a JSON write and rebuilds a Response the caller reads as text', async () => {
    const pending = forebrainApi.createLlmModel({ name: 'gpt', providerId: 'p1' } as never)
    await tick()
    const frame = lastRpc()
    expect(frame.params.method).toBe('POST')
    expect(frame.params.path).toBe('/api/llm-models')
    expect(frame.params.body).toBe(JSON.stringify({ name: 'gpt', provider_id: 'p1' }))
    const body = JSON.stringify({ id: 'm1', provider_id: 'p1' })
    deliver({
      type: 'rpc',
      rpc: frame.rpc,
      ok: true,
      result: { status: 200, statusText: 'OK', headers: { 'content-type': 'application/json' }, body: new TextEncoder().encode(body).buffer },
    })
    await expect(pending).resolves.toEqual({ id: 'm1', providerId: 'p1' })
  })

  it('rebuilds a Response that keeps refusal status readable as data', async () => {
    const pending = forebrainApi.createLlmModel({ name: 'gpt', providerId: 'p1' } as never)
    const frame = lastRpc()
    const body = JSON.stringify({ error: { message: 'duplicate' } })
    deliver({
      type: 'rpc',
      rpc: frame.rpc,
      ok: true,
      result: { status: 400, statusText: 'Bad Request', headers: {}, body: new TextEncoder().encode(body).buffer },
    })
    await expect(pending).rejects.toMatchObject({ status: 400 })
  })

  it('turns an abort into the AbortError a fetch caller expects and cancels the host work', async () => {
    const controller = new AbortController()
    const pending = transport.fetchShim('/api/chat/sessions/s/events', { signal: controller.signal })
    controller.abort()
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
    const cancels = fakeHost.posted.filter((m) => (m as { type?: string }).type === 'rpcCancel')
    expect(cancels.length).toBeGreaterThan(0)
  })
})

describe('the attachment upload', () => {
  it('encodes FormData as the multipart body the gateway parses today', async () => {
    const pending = forebrainApi.filesUpload(new File(['hello world'], 'a.txt', { type: 'text/plain' }), 's-1')
    await tick()
    const frame = lastRpc()
    const contentType = (frame.params.headers as Record<string, string>)['content-type']
    expect(contentType).toMatch(/^multipart\/form-data; boundary=/)
    const bytes = new TextDecoder().decode(frame.params.body as ArrayBuffer)
    expect(bytes).toContain('Content-Disposition: form-data; name="file"; filename="a.txt"')
    expect(bytes).toContain('Content-Type: text/plain')
    expect(bytes).toContain('hello world')
    expect(bytes).toContain('name="session_id"')
    expect(bytes).toContain('s-1')
    deliver({ type: 'rpc', rpc: frame.rpc, ok: true, result: { status: 200, statusText: 'OK', headers: {}, body: new TextEncoder().encode('{"file_id":"f1"}').buffer } })
    await expect(pending).resolves.toEqual({ fileId: 'f1' })
  })
})

describe('the chat socket', () => {
  it('carries frames in order and keys sends by the host-assigned id', async () => {
    const ws = openForebrainChatSocket()
    expect(ws).toBeInstanceOf(HostSocket)
    const events: string[] = []
    ws.onmessage = (event) => events.push(event.data)
    const opened = vi.fn()
    ws.onopen = opened

    const openCall = fakeHost.posted.find((m) => (m as { method?: string }).method === 'ws.open') as { rpc: number }
    deliver({ type: 'rpc', rpc: openCall.rpc, ok: true, result: { socket: 7 } })
    // The socket registers under its host id in a microtask after the answer.
    await tick()
    deliver({ type: 'wsState', socket: 7, up: true })
    expect(opened).toHaveBeenCalledTimes(1)

    deliver({ type: 'wsFrame', socket: 7, frames: ['{"op":"a"}', '{"op":"b"}'] })
    expect(events).toEqual(['{"op":"a"}', '{"op":"b"}'])

    ws.send('{"op":"start_run"}')
    const sent = fakeHost.posted.find((m) => (m as { method?: string }).method === 'ws.send') as { params: { socket: number; data: string } }
    expect(sent.params).toEqual({ socket: 7, data: '{"op":"start_run"}' })

    deliver({ type: 'wsState', socket: 7, up: false })
    expect(ws.readyState).toBe(HostSocket.CLOSED)
    expect(() => ws.send('x')).toThrow(/not open/)
  })
})

describe('the file range and the theme', () => {
  it('sends prose file:line links to the host over the rpc', () => {
    sendFileRangeToHost({ path: 'pkg/a.go', from: 3, to: 7 })
    const frame = lastRpc()
    expect(frame.method).toBe('openFileRange')
    expect(frame.params).toEqual({ path: 'pkg/a.go', startLine: 3, endLine: 7 })
  })

  it('toggles the dark class the main.css family answers to', () => {
    deliver({ type: 'theme', dark: true })
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    deliver({ type: 'theme', dark: false })
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })
})

describe('the token invariant', () => {
  it('no webview source sets auth material — the host adds the only bearer header', () => {
    const sources = readdirSync(here, { withFileTypes: true })
      .filter((entry: { isFile(): boolean; name: string }) => (
        entry.isFile() && /\.(ts|vue)$/.test(entry.name) && !entry.name.includes('.test.')
      ))
      .map((entry: { name: string }) => readFileSync(join(here, entry.name), 'utf-8'))
    for (const source of sources) {
      // Code shapes only: a quoted header name or a literal `Bearer ` prefix.
      expect(source).not.toMatch(/['"]authorization['"]|['"]cookie['"]|Bearer\s/)
    }
  })
})
