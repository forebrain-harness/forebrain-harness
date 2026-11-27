import axios, { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios'
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  forebrainApi,
  GatewayHttpError,
  openForebrainChatSocket,
  setForebrainChatSocketTransport,
  setForebrainFetchTransport,
  setForebrainHttpAdapter,
} from './api'
import { onGatewayUnauthorized } from './gatewaySession'

/**
 * The transport seam: a browser tab keeps the implementations it always had,
 * an embedded surface (the VS Code webview) installs its own at startup. These
 * tests install a stand-in, prove the whole shared pipeline still runs on top
 * of it — read merge, camel-casing, 401 reporting, refusal bodies — and prove
 * `null` puts the browser's own back.
 */

/** An adapter answer shaped the way the real one hands it over: a body string axios parses. */
function adapterAnswer(config: InternalAxiosRequestConfig, status: number, body: unknown): AxiosResponse {
  return { data: JSON.stringify(body), status, statusText: '', headers: {}, config }
}

afterEach(() => {
  setForebrainHttpAdapter(null)
  setForebrainFetchTransport(null)
  setForebrainChatSocketTransport(null)
  vi.restoreAllMocks()
})

describe('the axios adapter seam', () => {
  it('runs a request through the installed adapter and camel-cases the answer', async () => {
    const adapter = vi.fn((config: InternalAxiosRequestConfig) =>
      Promise.resolve(adapterAnswer(config, 200, { active_id: 'review', workspace_root: '/tmp/r' })),
    )
    setForebrainHttpAdapter(adapter)

    const res = await forebrainApi.primaryAgents()

    expect(adapter).toHaveBeenCalledTimes(1)
    expect(adapter.mock.calls[0][0].url).toBe('/agents/primary')
    expect(res).toEqual({ activeId: 'review', workspaceRoot: '/tmp/r' })
  })

  it('still merges two identical reads into one backend call', async () => {
    const adapter = vi.fn((config: InternalAxiosRequestConfig) =>
      Promise.resolve(adapterAnswer(config, 200, { records: [] })),
    )
    setForebrainHttpAdapter(adapter)

    const [a, b] = await Promise.all([forebrainApi.primaryAgents(), forebrainApi.primaryAgents()])
    expect(a).toEqual(b)
    expect(adapter).toHaveBeenCalledTimes(1)
  })

  it('reports a 401 through the interceptor that sits above the adapter', async () => {
    const unauthorized = vi.fn()
    onGatewayUnauthorized(unauthorized)
    // A custom adapter owns the settle step the built-ins do: a non-2xx answer
    // rejects with the response attached, which is what the 401 watch reads.
    setForebrainHttpAdapter((config) => {
      const response = adapterAnswer(config, 401, { error: 'sign in' })
      return Promise.reject(
        new AxiosError('Request failed with status code 401', AxiosError.ERR_BAD_RESPONSE, config, {}, response),
      )
    })

    await expect(forebrainApi.primaryAgents()).rejects.toThrow()
    expect(unauthorized).toHaveBeenCalledTimes(1)
  })

  it('restores the browser adapter on null', () => {
    const adapter = vi.fn((config: InternalAxiosRequestConfig) =>
      Promise.resolve(adapterAnswer(config, 200, {})),
    )
    setForebrainHttpAdapter(adapter)
    setForebrainHttpAdapter(null)
    // The browser default is axios's own adapter again — one that is not ours.
    expect(axios.defaults.adapter).not.toBe(adapter)
  })
})

describe('the fetch transport seam', () => {
  it('carries a write with its snake-case body and camel-cases the answer', async () => {
    const fetchImpl = vi.fn((_input: RequestInfo | URL, _init?: RequestInit) =>
      Promise.resolve(new Response(JSON.stringify({ id: 'm1', provider_id: 'p1' }), { status: 200 })),
    )
    setForebrainFetchTransport(fetchImpl as unknown as typeof fetch)

    const res = await forebrainApi.createLlmModel({ name: 'gpt', providerId: 'p1' } as never)

    expect(fetchImpl).toHaveBeenCalledTimes(1)
    const [input, init] = fetchImpl.mock.calls[0]
    expect(input).toBe('/api/llm-models')
    expect(init?.method).toBe('POST')
    expect(init?.body).toBe(JSON.stringify({ name: 'gpt', provider_id: 'p1' }))
    expect(res).toEqual({ id: 'm1', providerId: 'p1' })
  })

  it('reads a refusal body as data through GatewayHttpError', async () => {
    setForebrainFetchTransport(
      (() =>
        Promise.resolve(
          new Response(JSON.stringify({ error: { message: 'duplicate row' } }), { status: 400 }),
        )) as unknown as typeof fetch,
    )

    await expect(forebrainApi.createLlmModel({ name: 'gpt', providerId: 'p1' } as never)).rejects.toBeInstanceOf(
      GatewayHttpError,
    )
  })

  it('reports a 401 the way the browser fetch did', async () => {
    const unauthorized = vi.fn()
    onGatewayUnauthorized(unauthorized)
    setForebrainFetchTransport((() => Promise.resolve(new Response('', { status: 401 }))) as unknown as typeof fetch)

    await expect(forebrainApi.createLlmModel({ name: 'gpt', providerId: 'p1' } as never)).rejects.toThrow()
    expect(unauthorized).toHaveBeenCalledTimes(1)
  })
})

describe('the chat socket seam', () => {
  it('opens sockets through the installed transport', () => {
    const marker = { readyState: 0 } as unknown as WebSocket
    const opener = vi.fn(() => marker)
    setForebrainChatSocketTransport(opener)

    expect(openForebrainChatSocket()).toBe(marker)
    expect(opener).toHaveBeenCalledTimes(1)
  })

  it('restores the browser dial on null', () => {
    setForebrainChatSocketTransport(() => ({}) as WebSocket)
    setForebrainChatSocketTransport(null)

    const ws = openForebrainChatSocket()
    expect(ws.url).toBe('ws://localhost:3000/ws/chat')
    try {
      ws.close()
    } catch {
      // jsdom may refuse a close before its dial settles; the socket dies with the test anyway.
    }
  })
})
