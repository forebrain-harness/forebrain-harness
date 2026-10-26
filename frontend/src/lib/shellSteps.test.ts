import { describe, expect, it } from 'vitest'

import { shellSteps } from './shellSteps'

/** The tokens that carry the layout's words, without the whitespace between. */
function words(row: unknown): { kind: string; text: string }[] {
  const tokens = (row as { tokens: { kind: string; text: string }[] }).tokens
  return tokens.filter((token) => token.text.trim() !== '')
}

/**
 * The document tab's command layout, ported from the prototype's
 * shSplit/shHi/shSteps: the same rules, the same rows — expressed as data.
 */
describe('shellSteps', () => {
  it('splits a long chain one step per row, each led by its connector', () => {
    const { rows, multi } = shellSteps(
      'go vet -tags fts5 $(go list ./pkg/... | grep -v third_party) && go test -tags fts5 -count=1 -race ./pkg/tool',
    )
    expect(multi).toBe(true)
    expect(rows.map((row) => row.kind)).toEqual(['step', 'step', 'step'])
    // The connectors that lead rows two and three: the pipe inside $( ),
    // then the top-level &&.
    expect((rows[1] as { op: string }).op).toBe('|')
    expect((rows[2] as { op: string }).op).toBe('&&')
  })

  it('never splits inside quotes, however long the line', () => {
    const line = `echo "a | b && c ; d e f g h i j k l m n o p q r s" | wc -l`
    const { rows } = shellSteps(line)
    const echo = rows.find((row) => row.kind === 'step' && words(row).some((token) => token.text.startsWith('"a'))) as { tokens: { kind: string; text: string }[] }
    // The quoted string rode whole into the first step, pipe and all.
    expect(echo).toBeTruthy()
    expect(echo.tokens.find((token) => token.text.includes('|'))?.kind).toBe('str')
    // Only the pipe outside the quotes made a second row.
    const piped = rows.filter((row) => row.kind === 'step' && row.op === '|')
    expect(piped).toHaveLength(1)
    expect(words(piped[0])[0]).toEqual({ kind: 'cmd', text: 'wc' })
  })

  it('keeps a short line whole, one row, not multi', () => {
    const { rows, multi } = shellSteps('ls -la')
    expect(multi).toBe(false)
    expect(rows).toHaveLength(1)
    expect(words(rows[0])).toEqual([
      { kind: 'cmd', text: 'ls' },
      { kind: 'flag', text: '-la' },
    ])
  })

  it('bolds the program, keeps sudo and VAR=x prefixes out of the name', () => {
    const sudo = shellSteps('sudo rm -rf /tmp/x')
    expect(words(sudo.rows[0])).toEqual([
      { kind: 'text', text: 'sudo' },
      { kind: 'cmd', text: 'rm' },
      { kind: 'flag', text: '-rf' },
      { kind: 'text', text: '/tmp/x' },
    ])
    const env = shellSteps('FOO=bar go test ./...')
    expect(words(env.rows[0]).find((token) => token.kind === 'cmd')?.text).toBe('go')
  })

  it('marks strings warm and variables cool', () => {
    const { rows } = shellSteps(`grep "exact words" $HOME/notes`)
    const tokens = words(rows[0])
    expect(tokens.find((token) => token.text === '"exact words"')?.kind).toBe('str')
    expect(tokens.find((token) => token.text === '$HOME')?.kind).toBe('var')
  })

  it('reads a heredoc as one indented block under its line, closed by its marker', () => {
    const { rows, multi } = shellSteps('cat <<\'PATCH\' > /tmp/x\ntext body\n  more body\nPATCH\necho done')
    expect(multi).toBe(true)
    expect(rows.find((row) => row.kind === 'heredoc')).toMatchObject({ kind: 'heredoc', body: 'text body\n  more body' })
    expect(rows.find((row) => row.kind === 'end')).toMatchObject({ kind: 'end', text: 'PATCH' })
    // The line after the marker reads as its own step.
    expect(words(rows[rows.length - 1])[0]).toEqual({ kind: 'cmd', text: 'echo' })
  })
})
