import { describe, expect, it } from 'vitest'

import { parseMention, parseSlash } from './composerTokens'
// The same table the Go engine resolves against, asserted by
// pkg/turn/composer_tokens_test.go (TestComposerTokenTable). Reading that one
// file here is what makes the two surfaces one contract instead of two
// implementations that happen to agree today.
import tokenCases from '../../../pkg/turn/testdata/composer_tokens.json'

type TokenCase = {
  draft: string
  cursor: number
  kind: '@' | '/'
  ok: boolean
  start: number
  query: string
  at_start: boolean
}

const cases = tokenCases as TokenCase[]

describe('composer token table', () => {
  it('is the shared table', () => {
    expect(cases.length).toBeGreaterThanOrEqual(16)
  })

  it('resolves every case the way the engine does', () => {
    for (const tc of cases) {
      if (tc.kind === '/') {
        const got = parseSlash(tc.draft, tc.cursor)
        expect(got, `slash ${JSON.stringify(tc.draft)}@${tc.cursor}`).toEqual(
          tc.ok ? { query: tc.query, start: tc.start, atStart: tc.at_start } : null,
        )
      } else {
        const got = parseMention(tc.draft, tc.cursor)
        expect(got, `mention ${JSON.stringify(tc.draft)}@${tc.cursor}`).toEqual(
          tc.ok ? { query: tc.query, start: tc.start } : null,
        )
      }
    }
  })

  it('does not treat a mid-word @ as a mention', () => {
    expect(parseMention('foo@bar', 7)).toBeNull()
  })
})
