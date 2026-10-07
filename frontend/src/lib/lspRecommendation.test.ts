import { describe, expect, it } from 'vitest'

import { parseLspRecommendation } from './lspRecommendation'

describe('parseLspRecommendation', () => {
  it('reads a snake_case event payload', () => {
    const rec = parseLspRecommendation({
      id: 'lsprec-9f1c2a7b',
      server_id: 'gopls',
      display_name: 'gopls',
      languages: ['Go'],
      trigger_extension: '.go',
      mode: 'enable',
      binary_path: '/usr/local/bin/gopls',
      version: 'v0.23.0',
    })
    expect(rec).toEqual({
      id: 'lsprec-9f1c2a7b',
      serverId: 'gopls',
      displayName: 'gopls',
      languages: ['Go'],
      triggerExtension: '.go',
      mode: 'enable',
      binaryPath: '/usr/local/bin/gopls',
      version: 'v0.23.0',
      installCommand: undefined,
    })
  })

  it('reads camelCase keys and falls back to the id for the display name', () => {
    const rec = parseLspRecommendation({
      id: 'lsprec-1',
      serverId: 'pyright',
      mode: 'install',
      installCommand: 'npm install -g pyright',
      triggerExtension: '.py',
    })
    expect(rec).toMatchObject({
      id: 'lsprec-1',
      serverId: 'pyright',
      displayName: 'pyright',
      mode: 'install',
      installCommand: 'npm install -g pyright',
      languages: [],
    })
  })

  it('rejects anything without the identity or a known mode', () => {
    expect(parseLspRecommendation(null)).toBeNull()
    expect(parseLspRecommendation('lsprec')).toBeNull()
    expect(parseLspRecommendation({ serverId: 'gopls', mode: 'enable' })).toBeNull()
    expect(parseLspRecommendation({ id: 'lsprec-1', mode: 'enable' })).toBeNull()
    expect(parseLspRecommendation({ id: 'lsprec-1', serverId: 'gopls', mode: 'maybe' })).toBeNull()
    expect(parseLspRecommendation({ id: ' ', serverId: 'gopls', mode: 'enable' })).toBeNull()
  })
})
