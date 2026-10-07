import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  markGatewaySessionOk,
  probeGatewaySession,
  reportGatewayUnauthorized,
  signInToGateway,
  tokenFromLocationHash,
} from './gatewaySession'

const fetchMock = vi.fn()

describe('tokenFromLocationHash', () => {
  it('decodes the token from a sign-in link hash', () => {
    expect(tokenFromLocationHash('#token=abc%2F1')).toBe('abc/1')
    expect(tokenFromLocationHash('#token=abc&other=1')).toBe('abc')
  })

  it('takes a malformed escape as typed instead of throwing', () => {
    expect(tokenFromLocationHash('#token=100%')).toBe('100%')
  })

  it('returns empty when the hash carries no token', () => {
    expect(tokenFromLocationHash('')).toBe('')
    expect(tokenFromLocationHash('#other=1')).toBe('')
  })
})

describe('probeGatewaySession', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.clearAllMocks()
    // The cached probe is module state; reset it between cases the same way
    // the app does after a 401 (no handlers registered in tests).
    reportGatewayUnauthorized()
  })

  it('reports required on 401 and caches the answer', async () => {
    fetchMock.mockResolvedValue({ status: 401 })
    vi.stubGlobal('fetch', fetchMock)
    await expect(probeGatewaySession()).resolves.toBe('required')
    await expect(probeGatewaySession()).resolves.toBe('required')
    expect(fetchMock).toHaveBeenCalledOnce()
  })

  it('treats any other answer as ok', async () => {
    fetchMock.mockResolvedValue({ status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await expect(probeGatewaySession()).resolves.toBe('ok')
  })

  it('re-probes after the session is reported unauthorized', async () => {
    fetchMock.mockResolvedValueOnce({ status: 401 }).mockResolvedValueOnce({ status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await expect(probeGatewaySession()).resolves.toBe('required')
    reportGatewayUnauthorized()
    await expect(probeGatewaySession()).resolves.toBe('ok')
  })
})

describe('signInToGateway', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.clearAllMocks()
  })

  it('posts the token and resolves on success', async () => {
    fetchMock.mockResolvedValue({ ok: true })
    vi.stubGlobal('fetch', fetchMock)
    await expect(signInToGateway('secret')).resolves.toBeUndefined()
    const [input, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(input).toBe('/api/auth/session')
    expect(init.method).toBe('POST')
    expect(JSON.parse(String(init.body))).toEqual({ token: 'secret' })
  })

  it('throws the server error body verbatim on rejection', async () => {
    fetchMock.mockResolvedValue({ ok: false, status: 401, json: async () => ({ error: 'invalid gateway token' }) })
    vi.stubGlobal('fetch', fetchMock)
    await expect(signInToGateway('wrong')).rejects.toThrow('invalid gateway token')
  })
})

describe('markGatewaySessionOk', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.clearAllMocks()
    reportGatewayUnauthorized()
  })

  it('answers ok without a network probe', async () => {
    markGatewaySessionOk()
    vi.stubGlobal('fetch', fetchMock)
    await expect(probeGatewaySession()).resolves.toBe('ok')
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
