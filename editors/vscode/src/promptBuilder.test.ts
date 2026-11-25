import test from 'node:test';
import assert from 'node:assert/strict';
import {
  FILE_CONTEXT_LIMIT,
  buildFilePrompt,
  buildSelectionPrompt,
  diagnosticsOf,
  type DiagnosticLike,
} from './promptBuilder';

const SELECTION = {
  fsPath: '/ws/src/a.ts',
  relPath: 'src/a.ts',
  languageId: 'typescript',
  startLine: 12,
  endLine: 20,
  selection: 'const a = 1;\nconst b = 2;',
};

const FILE = { fsPath: '/ws/src/a.ts', relPath: 'src/a.ts', languageId: 'typescript', content: 'const a = 1;' };

const QUESTION = 'There is a bug here — fix it';

function diagnostic(fields: Partial<DiagnosticLike> & { range: DiagnosticLike['range']; message: string }): DiagnosticLike {
  return { severity: 0, source: 'ts', ...fields };
}

test('a selection becomes a header with its line range, a fenced block in its language, and the question last', () => {
  const prompt = buildSelectionPrompt(SELECTION, QUESTION, []);
  assert.deepEqual(prompt.split('\n\n'), [
    'In src/a.ts:12-20:',
    '```typescript\nconst a = 1;\nconst b = 2;\n```',
    QUESTION,
  ]);
});

test('a file becomes its relative path and the whole content, with the whole file in the fence', () => {
  const prompt = buildFilePrompt(FILE, QUESTION, []);
  assert.deepEqual(prompt.split('\n\n'), ['File src/a.ts:', '```typescript\nconst a = 1;\n```', QUESTION]);
});

test('a file over the context limit is cut at 64 KiB and says so after the fence', () => {
  const content = 'x'.repeat(FILE_CONTEXT_LIMIT + 10);
  const prompt = buildFilePrompt({ ...FILE, content }, QUESTION, []);
  const blocks = prompt.split('\n\n');
  assert.ok(content.length > FILE_CONTEXT_LIMIT);
  assert.equal(blocks[1], `\`\`\`typescript\n${'x'.repeat(FILE_CONTEXT_LIMIT)}\n\`\`\``, 'cut to exactly the limit');
  assert.equal(blocks[2], '(src/a.ts truncated at 64 KiB; use your file tools to read the rest)');
  assert.ok(!prompt.includes('x'.repeat(FILE_CONTEXT_LIMIT + 1)), 'not one character more');

  const exact = buildFilePrompt({ ...FILE, content: 'y'.repeat(FILE_CONTEXT_LIMIT) }, QUESTION, []);
  assert.ok(!exact.includes('truncated'), 'a file of exactly the limit is whole');
});

test('diagnostics are sorted by line with the most severe first, 1-based, with their severity spelled out', () => {
  const lines = diagnosticsOf([
    diagnostic({ range: { start: { line: 41 } }, severity: 3, message: 'unused variable', source: 'eslint' }),
    diagnostic({ range: { start: { line: 6 } }, severity: 1, message: 'possibly undefined', source: 'ts' }),
    diagnostic({ range: { start: { line: 6 } }, severity: 0, message: 'cannot find name', source: 'ts' }),
    diagnostic({ range: { start: { line: 6 } }, severity: 2, message: 'a hint', source: 'ts' }),
  ]);
  assert.deepEqual(lines, [
    { line: 7, severity: 'ERROR', source: 'ts', message: 'cannot find name' },
    { line: 7, severity: 'WARNING', source: 'ts', message: 'possibly undefined' },
    { line: 7, severity: 'INFO', source: 'ts', message: 'a hint' },
    { line: 42, severity: 'HINT', source: 'eslint', message: 'unused variable' },
  ]);
  const prompt = buildSelectionPrompt(SELECTION, QUESTION, lines);
  assert.ok(
    prompt.includes(
      [
        'Diagnostics reported in src/a.ts:',
        '- [ERROR] line 7: cannot find name (ts)',
        '- [WARNING] line 7: possibly undefined (ts)',
        '- [INFO] line 7: a hint (ts)',
        '- [HINT] line 42: unused variable (eslint)',
      ].join('\n'),
    ),
    prompt,
  );
  assert.ok(prompt.indexOf('Diagnostics reported') < prompt.indexOf(QUESTION), 'the diagnostics precede the question');
});

test('no diagnostics means no diagnostics block at all', () => {
  for (const prompt of [buildSelectionPrompt(SELECTION, QUESTION, []), buildFilePrompt(FILE, QUESTION, [])]) {
    assert.ok(!prompt.includes('Diagnostics reported'), prompt);
    assert.ok(!prompt.includes('\n\n\n'), 'no empty block leaves a double blank line');
  }
  assert.deepEqual(diagnosticsOf([]), []);
});

test('without a workspace folder the absolute path stands in for the relative one', () => {
  const prompt = buildSelectionPrompt({ ...SELECTION, relPath: '' }, QUESTION, []);
  assert.equal(prompt.split('\n')[0], 'In /ws/src/a.ts:12-20:');
  const filePrompt = buildFilePrompt({ ...FILE, relPath: '' }, QUESTION, []);
  assert.equal(filePrompt.split('\n')[0], 'File /ws/src/a.ts:');
});