// The transport the chat webview rides on: every gateway call it makes is one
// postMessage RPC to the extension host, which adds the bearer header no page
// ever sees. Installing this (once, at startup) redirects the shared seams the
// web app already routes through — the axios adapter, the fetch the gateway
// helpers use, the chat socket opener and the file:line range sender — so the
// composables and components behave identically to a browser tab, byte for
// byte, with only the carrier swapped.
//
// Frame vocabulary mirrors editors/vscode/src/webviewRpc.ts.
import axios, { AxiosError, CanceledError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios'
import {
  setForebrainChatSocketTransport,
  setForebrainFetchTransport,
  setForebrainHttpAdapter,
} from '@/lib/api'
import { setNativeFileRangeSender, type FileLineRef } from '@/lib/fileLineLinks'

interface VsCodeApi {
  postMessage(message: unknown): void
}

declare function acquireVsCodeApi(): VsCodeApi

/** The slice of a cancellation signal the adapter needs; axios hands a
 *  `GenericAbortSignal`, whose optional listeners this covers. */
interface AbortSignalLike {
  readonly aborted: boolean
  addEventListener?(type: 'abort', listener: () => void, options?: { once?: boolean }): void
  removeEventListener?(type: 'abort', listener: () => void): void
}

/** The fields an axios config carries that the adapter has to honour. */
interface Pending {
  resolve: (value: unknown) => void
  reject: (reason: Error) => void
}

export class WebviewTransport {
  private readonly pending = new Map<number, Pending>()
  private readonly sockets = new Map<number, HostSocket>()
  private nextId = 0

  constructor(private readonly host: VsCodeApi) {}

  /** The one inbound gate: RPC answers, socket frames, theme pushes. */
  onHostMessage(raw: unknown): void {
    if (typeof raw !== 'object' || raw === null) return
    const frame = raw as { type?: unknown };
    if (frame.type === 'theme') {
      this.applyTheme(raw as { dark?: unknown });
      return
    }
    if (frame.type === 'wsFrame') {
      const { socket, frames } = raw as { socket?: unknown; frames?: unknown };
      const target = this.sockets.get(typeof socket === 'number' ? socket : -1);
      if (target && Array.isArray(frames)) for (const data of frames) target.pump(String(data));
      return
    }
    if (frame.type === 'wsState') {
      const { socket, up, error } = raw as { socket?: unknown; up?: unknown; error?: unknown };
      const target = this.sockets.get(typeof socket === 'number' ? socket : -1);
      if (!target) return
      if (up === true) target.markOpen()
      else target.markClosed(typeof error === 'string' ? error : undefined)
      return
    }
    if (frame.type !== 'rpc') return
    const { rpc, ok, result, error } = raw as { rpc?: unknown; ok?: unknown; result?: unknown; error?: unknown };
    const pending = this.pending.get(typeof rpc === 'number' ? rpc : -1);
    if (!pending) return
    this.pending.delete(rpc as number)
    if (ok === true) pending.resolve(result)
    else pending.reject(new Error(typeof error === 'string' ? error : 'the host refused the request'))
  }

  /** One correlated call. */
  private request<T = unknown>(method: string, params?: unknown, signal?: AbortSignalLike): Promise<T> {
    const id = ++this.nextId
    const call = new Promise<T>((resolve, reject) => {
      this.pending.set(id, { resolve: resolve as (value: unknown) => void, reject })
      this.host.postMessage({ type: 'rpc', rpc: id, method, params })
    })
    if (!signal) return call
    return new Promise<T>((resolve, reject) => {
      const onAbort = () => {
        // The host unwinds its fetch; the answer still arrives and is ignored.
        this.host.postMessage({ type: 'rpcCancel', rpc: id })
        this.pending.delete(id)
        // The cancellation axios itself throws when a request is aborted.
        reject(new CanceledError())
      }
      if (signal.aborted) return onAbort()
      signal.addEventListener?.('abort', onAbort, { once: true })
      call.then(
        (value) => {
          signal.removeEventListener?.('abort', onAbort)
          resolve(value)
        },
        (reason: Error) => {
          signal.removeEventListener?.('abort', onAbort)
          reject(reason)
        },
      )
    })
  }

  /** The axios adapter: one request in, an axios answer out — settle included,
   *  which the built-in adapters own and a custom one must reproduce. */
  async httpRequest(config: InternalAxiosRequestConfig): Promise<AxiosResponse> {
    const headers = headerMap(config.headers)
    const body = await encodeBody(config.data, headers)
    const binary = config.responseType === 'arraybuffer' || config.responseType === 'blob'
    let reply: Reply
    try {
      reply = await this.request<Reply>(
        'http',
        {
          method: String(config.method ?? 'get').toUpperCase(),
          path: joinPath(config.baseURL, config.url, config.params),
          headers,
          body,
          binary,
        },
        config.signal as AbortSignalLike | undefined,
      )
    } catch (err) {
      if (axios.isCancel(err)) throw err
      // A refusal without an answer body: surface it the way the built-in
      // adapters surface a network failure, so getErrorMessage reads it.
      throw new AxiosError(err instanceof Error ? err.message : String(err), AxiosError.ERR_BAD_OPTION, config)
    }
    const response: AxiosResponse = {
      data: reply.body,
      status: reply.status,
      statusText: reply.statusText,
      headers: reply.headers,
      config,
    }
    if (!config.validateStatus || config.validateStatus(response.status)) return response
    // Same shape the built-in adapters reject with: the 401 watch upstream
    // reads `error.response.status`.
    throw new AxiosError(
      response.statusText || `Request failed with status code ${response.status}`,
      response.status === 401 ? AxiosError.ERR_BAD_RESPONSE : AxiosError.ERR_BAD_REQUEST,
      config,
      {},
      response,
    )
  }

  /** The fetch the gateway helpers ride on. The answer is always rebuilt from
   *  bytes, so a caller reading `blob()` or `arrayBuffer()` sees the file and
   *  a caller reading `text()` sees the same words a browser fetch returned. */
  async fetchShim(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    const path = toPath(input)
    const headers: Record<string, string> = headerMap(init?.headers)
    let body: string | ArrayBuffer | undefined
    if (init?.body instanceof FormData) {
      const multipart = await encodeMultipart(init.body)
      body = multipart.body
      headers['content-type'] = multipart.contentType
    } else if (init && isArrayBuffer(init.body)) {
      body = init.body as ArrayBuffer
    } else if (init && ArrayBuffer.isView(init.body)) {
      body = (init.body as Uint8Array).slice().buffer as ArrayBuffer
    } else if (typeof init?.body === 'string') {
      body = init.body
    }
    let reply: Reply
    try {
      reply = await this.request<Reply>('http', {
        method: init?.method ?? 'GET',
        path,
        headers,
        body,
        binary: true,
      }, init?.signal ?? undefined)
    } catch (err) {
      if (axios.isCancel(err)) throw new DOMException('The operation was aborted.', 'AbortError')
      throw err
    }
    const bytes = isArrayBuffer(reply.body)
      ? (reply.body as ArrayBuffer)
      : new TextEncoder().encode(String(reply.body)).buffer as ArrayBuffer
    return new Response(bytes.byteLength > 0 ? bytes : undefined, {
      status: reply.status,
      statusText: reply.statusText,
      headers: reply.headers,
    })
  }

  /** One chat WebSocket backed by the host's pump. The host answers `ws.open`
   *  with the id its frames and send/close calls carry. */
  openSocket(): HostSocket {
    const socket = new HostSocket((method, params) => {
      void this.request(method, params)
    })
    void this.request<{ socket: number }>('ws.open').then(
      (answer) => {
        socket.hostId = answer.socket
        this.sockets.set(answer.socket, socket)
      },
      (err: Error) => socket.fail(err),
    )
    return socket
  }

  /** Prose file:line links ride the same RPC to the host's opener. */
  openFileRange(ref: FileLineRef): void {
    void this.request('openFileRange', { path: ref.path, startLine: ref.from, endLine: ref.to })
  }

  /** The host pushes the editor theme; dark is the one class the web app's
   *  stylesheet answers to (main.css `html.dark`, the frozen prototype's family). */
  private applyTheme(frame: { dark?: unknown }): void {
    document.documentElement.classList.toggle('dark', frame.dark === true)
  }
}

interface Reply {
  status: number
  statusText: string
  headers: Record<string, string>
  body: string | ArrayBuffer
}

/** A WebSocket stand-in with the surface the composables touch. */
export class HostSocket {
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3

  readyState = HostSocket.CONNECTING
  /** The socket id the host assigned; sends and closes are keyed by it. */
  hostId: number | null = null
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onerror: ((event: unknown) => void) | null = null
  onclose: (() => void) | null = null

  constructor(private readonly send_: (method: string, params: unknown) => void) {}

  /** One gateway frame, in arrival order. */
  pump(data: string): void {
    this.onmessage?.({ data })
  }

  markOpen(): void {
    this.readyState = HostSocket.OPEN
    this.onopen?.()
  }

  markClosed(_error?: string): void {
    this.readyState = HostSocket.CLOSED
    this.onclose?.()
  }

  fail(err: Error): void {
    this.readyState = HostSocket.CLOSED
    this.onerror?.(err)
    this.onclose?.()
  }

  send(data: string): void {
    if (this.readyState !== HostSocket.OPEN || this.hostId === null) throw new Error('the socket is not open')
    this.send_('ws.send', { socket: this.hostId, data })
  }

  close(): void {
    if (this.readyState === HostSocket.CLOSED) return
    this.readyState = HostSocket.CLOSED
    if (this.hostId !== null) this.send_('ws.close', { socket: this.hostId })
    this.onclose?.()
  }
}

/** Installs the transport over the webview's own message channel. Call once,
 *  before the first request — everything the app does afterwards goes through it. */
export function installWebviewTransport(): WebviewTransport {
  const host = acquireVsCodeApi()
  const transport = new WebviewTransport(host)
  setForebrainHttpAdapter((config) => transport.httpRequest(config))
  setForebrainFetchTransport((input, init) => transport.fetchShim(input, init))
  // The composables type the opener as WebSocket; HostSocket carries the
  // surface they touch (send/close/readyState/handlers), which is the contract.
  setForebrainChatSocketTransport(() => transport.openSocket() as unknown as WebSocket)
  setNativeFileRangeSender((ref) => transport.openFileRange(ref))
  return transport
}

/** Reads back what the transport installed — the tests' handle, and how a
 *  caller pumps host frames into the app without touching the channel itself. */
export function bindHostChannel(transport: WebviewTransport): (raw: unknown) => void {
  return (raw: unknown) => transport.onHostMessage(raw)
}

function headerMap(headers: unknown): Record<string, string> {
  const result: Record<string, string> = {}
  if (typeof headers === 'object' && headers !== null) {
    for (const [name, value] of Object.entries(headers as Record<string, unknown>)) {
      if (typeof value === 'string') result[name] = value
      else if (typeof value === 'number') result[name] = String(value)
    }
  }
  return result
}

function joinPath(baseURL: unknown, url: unknown, params: unknown): string {
  let path = `${typeof baseURL === 'string' ? baseURL : ''}${typeof url === 'string' ? url : ''}`
  // buildURL: same order axios's own adapters append with.
  if (params && typeof params === 'object' && !path.includes('?')) {
    const query = new URLSearchParams()
    for (const [key, value] of Object.entries(params as Record<string, unknown>)) {
      if (value !== undefined && value !== null) query.append(key, String(value))
    }
    const encoded = query.toString()
    if (encoded) path += `?${encoded}`
  }
  return path
}

function toPath(input: RequestInfo | URL): string {
  const text = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
  if (text.startsWith('/')) return text
  try {
    const url = new URL(text)
    return `${url.pathname}${url.search}`
  } catch {
    return text
  }
}

async function encodeBody(data: unknown, headers: Record<string, string>): Promise<string | ArrayBuffer | undefined> {
  if (data === undefined || data === null) return undefined
  if (typeof data === 'string') return data
  if (isArrayBuffer(data)) return data as ArrayBuffer
  if (ArrayBuffer.isView(data)) return (data as Uint8Array).slice().buffer as ArrayBuffer
  if (data instanceof FormData) {
    const multipart = await encodeMultipart(data)
    headers['content-type'] = multipart.contentType
    return multipart.body
  }
  // transformRequest has run by now, so anything else here is unexpected;
  // keep the browser's contract and let the gateway refuse a bad body.
  return String(data)
}

/** Realm-safe ArrayBuffer check: the webview's tests (and some embedders) hand
 *  buffers over from another JS realm, where `instanceof` lies. */
function isArrayBuffer(value: unknown): boolean {
  return Object.prototype.toString.call(value) === '[object ArrayBuffer]'
}

/** The multipart body a browser builds for `FormData`, byte for byte — the
 *  composer's attachment upload posts the very shape `filesUpload` posts today. */
export async function encodeMultipart(form: FormData): Promise<{ body: ArrayBuffer; contentType: string }> {
  const boundary = `----forebrain-webview-${crypto.randomUUID()}`
  const chunks: Uint8Array[] = []
  const encoder = new TextEncoder()
  for (const [name, value] of form.entries()) {
    if (typeof value === 'string') {
      chunks.push(encoder.encode(
        `--${boundary}\r\nContent-Disposition: form-data; name="${escapeName(name)}"\r\n\r\n${value}\r\n`,
      ))
      continue
    }
    chunks.push(encoder.encode(
      `--${boundary}\r\nContent-Disposition: form-data; name="${escapeName(name)}"; filename="${escapeName(value.name)}"\r\n` +
        `Content-Type: ${value.type || 'application/octet-stream'}\r\n\r\n`,
    ))
    chunks.push(new Uint8Array(await value.arrayBuffer()))
    chunks.push(encoder.encode('\r\n'))
  }
  chunks.push(encoder.encode(`--${boundary}--\r\n`))
  const total = chunks.reduce((sum, chunk) => sum + chunk.byteLength, 0)
  const body = new Uint8Array(total)
  let offset = 0
  for (const chunk of chunks) {
    body.set(chunk, offset)
    offset += chunk.byteLength
  }
  return { body: body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength) as ArrayBuffer, contentType: `multipart/form-data; boundary=${boundary}` }
}

function escapeName(name: string): string {
  return name.split('"').join('%22')
}
