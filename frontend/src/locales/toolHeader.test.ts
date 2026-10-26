import { describe, expect, it } from 'vitest'

// The engine's action-word vocabulary, straight from its golden copy. A static
// import rather than node:fs: this package has no @types/node, and the golden
// file is a compile-time constant anyway — either way the test reads exactly
// what the engine ships and turns red when a word is not worded here.
import goldenActions from '../../../pkg/tool/testdata/header_actions.json'

import { HEADER_ACTION_ZH, headerActionWord, headerSuffixWords } from './toolHeader'

describe('the card head vocabulary', () => {
  it('words every action the engine can put in a head, in Chinese and nothing else', () => {
    for (const action of goldenActions) {
      const zh = HEADER_ACTION_ZH[action]
      expect(zh, `a Chinese word for ${action}`).toBeTruthy()
      // The one exception is MCP, a proper name. Everything else is Chinese:
      // an ASCII letter means the engine's English slipped through.
      if (action !== 'MCP') {
        expect(zh, `${action} worded without ASCII letters`).not.toMatch(/[A-Za-z]/)
      }
    }
  })

  it('leaves an unworded action as the engine wrote it, both languages', () => {
    expect(headerActionWord('Read', 'zh')).toBe('已读取')
    expect(headerActionWord('Read', 'en')).toBe('Read')
    expect(headerActionWord('Something new', 'zh')).toBe('Something new')
  })

  it('words the three suffix shapes and leaves everything else alone', () => {
    expect(headerSuffixWords('lines 15-20', 'zh')).toBe('第 15-20 行')
    // The engine words a search's suffix as `matching “query”` (header_parts.go).
    expect(headerSuffixWords('matching “go test”', 'zh')).toBe('匹配 “go test”')
    expect(headerSuffixWords('in this project', 'zh')).toBe('在 this project 中')
    // An MCP tool's own field summary is external wording — never translated.
    expect(headerSuffixWords('repo: forebrain-harness · limit: 10', 'zh')).toBe('repo: forebrain-harness · limit: 10')
    expect(headerSuffixWords('lines 15-20', 'en')).toBe('lines 15-20')
  })
})
