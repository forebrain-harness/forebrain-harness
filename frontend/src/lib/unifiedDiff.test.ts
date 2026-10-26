import { describe, expect, it } from 'vitest'

import { diffRowsFromLines, parseUnifiedDiff } from './unifiedDiff'

describe('parseUnifiedDiff', () => {
  it('numbers context and added rows from the new file, removed rows from the old one, and folds once between hunks', () => {
    const diff = [
      'diff --git a/a.go b/a.go',
      'index 3f7a1c2..9d2e8b1 100644',
      '--- a/a.go',
      '+++ b/a.go',
      '@@ -10,4 +10,5 @@ func main() {',
      ' \tctx := context.Background()',
      '-\told := 1',
      '+\tnewA := 2',
      '+\tnewB := 3',
      ' \trun(ctx)',
      '@@ -40,2 +41,2 @@',
      ' \t// tail',
      '-\tremoved',
      '+\tadded',
    ].join('\n')
    expect(parseUnifiedDiff(diff)).toEqual([
      { mark: ' ', line: 10, text: '\tctx := context.Background()' },
      { mark: '-', line: 11, text: '\told := 1' },
      { mark: '+', line: 11, text: '\tnewA := 2' },
      { mark: '+', line: 12, text: '\tnewB := 3' },
      { mark: ' ', line: 13, text: '\trun(ctx)' },
      { mark: '…', line: null, text: '' },
      { mark: ' ', line: 41, text: '\t// tail' },
      { mark: '-', line: 41, text: '\tremoved' },
      { mark: '+', line: 42, text: '\tadded' },
    ])
  })

  it('drops the no-newline marker and every header line before the first hunk, and adds no fold before the first hunk', () => {
    const diff = [
      '--- a/notes.txt:before',
      '+++ b/notes.txt:after',
      '@@ -1 +1 @@',
      '-end without newline',
      '\\ No newline at end of file',
      '+end with one',
    ].join('\n')
    expect(parseUnifiedDiff(diff)).toEqual([
      { mark: '-', line: 1, text: 'end without newline' },
      { mark: '+', line: 1, text: 'end with one' },
    ])
  })

  it('reads an addition-only diff (a new file) with the new file’s numbers', () => {
    const rows = parseUnifiedDiff('@@ -0,0 +1,3 @@\n+one\n+\n+three')
    expect(rows).toEqual([
      { mark: '+', line: 1, text: 'one' },
      { mark: '+', line: 2, text: '' },
      { mark: '+', line: 3, text: 'three' },
    ])
  })

  it('draws no phantom row for the trailing newline a diff ends with', () => {
    expect(parseUnifiedDiff('@@ -1 +1 @@\n+only\n')).toEqual([
      { mark: '+', line: 1, text: 'only' },
    ])
  })
})

describe('diffRowsFromLines', () => {
  it('maps the engine’s structured lines without re-parsing text', () => {
    const rows = diffRowsFromLines([
      { lines: [
        { kind: 'ctx' as const, old_no: 5, new_no: 6, text: 'ctx' },
        { kind: 'del' as const, old_no: 6, text: 'gone' },
        { kind: 'add' as const, new_no: 7, text: 'here' },
      ] },
      { lines: [{ kind: 'add' as const, new_no: 20, text: 'far' }] },
    ])
    expect(rows).toEqual([
      { mark: ' ', line: 6, text: 'ctx' },
      { mark: '-', line: 6, text: 'gone' },
      { mark: '+', line: 7, text: 'here' },
      { mark: '…', line: null, text: '' },
      { mark: '+', line: 20, text: 'far' },
    ])
  })
})
