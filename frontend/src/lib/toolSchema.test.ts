import { describe, expect, it } from 'vitest'

import { toolParams } from './toolSchema'

describe('toolParams', () => {
  it('keeps the declaration order and the required flags', () => {
    const params = toolParams({
      type: 'object',
      properties: {
        path: { type: 'string', description: 'the file to read' },
        limit: { type: 'number', description: 'how many lines' },
        offset: { type: 'number' },
      },
      required: ['path', 'offset'],
    })
    expect(params).toEqual([
      { name: 'path', type: 'string', required: true, description: 'the file to read' },
      { name: 'limit', type: 'number', required: false, description: 'how many lines' },
      { name: 'offset', type: 'number', required: true, description: '' },
    ])
  })

  it('has no rows when the schema declares no properties', () => {
    expect(toolParams({ type: 'object' })).toEqual([])
    expect(toolParams({})).toEqual([])
    expect(toolParams(undefined)).toEqual([])
    expect(toolParams('not a schema')).toEqual([])
  })

  it('treats a missing required list as nothing being required', () => {
    const params = toolParams({ properties: { path: { type: 'string' } } })
    expect(params).toEqual([{ name: 'path', type: 'string', required: false, description: '' }])
  })

  it('names an array by its element type', () => {
    const params = toolParams({
      properties: {
        paths: { type: 'array', items: { type: 'string' }, description: 'the files' },
        bare: { type: 'array' },
      },
    })
    expect(params.map((param) => param.type)).toEqual(['string[]', 'array'])
  })

  it('calls a property with no type an object', () => {
    const params = toolParams({
      properties: {
        config: { description: 'nested settings', properties: { mode: { type: 'string' } } },
      },
    })
    expect(params).toEqual([{ name: 'config', type: 'object', required: false, description: 'nested settings' }])
  })
})
