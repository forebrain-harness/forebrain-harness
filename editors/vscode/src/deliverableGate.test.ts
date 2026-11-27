import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { clampLineRange, deliverableOpenKind, fileRangeRequestOf, openableDeliverables, resolveWorkspaceFile } from './deliverableGate';

const ROOT = path.resolve('/ws/repo');
const OTHER = path.resolve('/ws/other');

function delivered(...paths: string[]): { path: string; title: string }[] {
  return paths.map((p) => ({ path: p, title: path.basename(p) }));
}

test('a file inside a workspace folder may be opened', () => {
  const files = delivered(`${ROOT}/out/report.md`, `${ROOT}/a.png`);
  assert.deepEqual(openableDeliverables(files, [ROOT]), files);
  assert.deepEqual(openableDeliverables([{ path: ROOT, title: 'root' }], [ROOT]), [{ path: ROOT, title: 'root' }]);
  assert.deepEqual(openableDeliverables(files, [OTHER, ROOT]), files, 'any of the folders counts');
});

test('a path that climbs out of the folder with .. may not be opened', () => {
  const files = delivered(`${ROOT}/../etc/passwd`, `${ROOT}/out/../../secret.txt`);
  assert.deepEqual(openableDeliverables(files, [ROOT]), []);
});

test('a relative path may not be opened', () => {
  assert.deepEqual(openableDeliverables(delivered('out/report.md', './out/a.png'), [ROOT]), []);
});

test('with no workspace folder nothing may be opened', () => {
  assert.deepEqual(openableDeliverables(delivered(`${ROOT}/out/report.md`), []), []);
  assert.deepEqual(openableDeliverables([], [ROOT]), []);
});

test('code and text read in the editor', () => {
  for (const p of ['a.ts', 'src/extension.ts', 'package.json', 'notes.txt', 'Makefile', 'LICENSE', 'data.csv', 'script.go']) {
    assert.equal(deliverableOpenKind(`${ROOT}/${p}`), 'editor', p);
  }
});

test('images use VS Code’s own image preview', () => {
  for (const p of ['shot.png', 'photo.JPG', 'anim.gif', 'pic.webp', 'icon.ico', 'logo.bmp', 'photo.jpeg', 'img.avif']) {
    assert.equal(deliverableOpenKind(`${ROOT}/${p}`), 'image', p);
  }
});

test('markdown uses the built-in preview', () => {
  for (const p of ['README.md', 'plan.markdown', 'doc.mdx']) {
    assert.equal(deliverableOpenKind(`${ROOT}/${p}`), 'markdown', p);
  }
});

test('PDF, Office, media, archives, HTML and SVG go to the OS default app', () => {
  for (const p of ['report.pdf', 'sheet.xlsx', 'deck.pptx', 'letter.docx', 'page.html', 'drawing.svg', 'clip.mp4', 'song.mp3', 'bundle.zip', 'archive.tar.gz']) {
    assert.equal(deliverableOpenKind(`${ROOT}/${p}`), 'external', p);
  }
});

test('the extension match ignores case and a missing extension reads as text', () => {
  assert.equal(deliverableOpenKind(`${ROOT}/REPORT.PDF`), 'external');
  assert.equal(deliverableOpenKind(`${ROOT}/PHOTO.PNG`), 'image');
  assert.equal(deliverableOpenKind(`${ROOT}/README`), 'editor');
});

test('a relative link path resolves under the first folder that holds it', () => {
  const present = new Set([`${OTHER}/pkg/a.go`, `${ROOT}/pkg/b.go`]);
  const exists = (p: string) => present.has(p);
  assert.equal(resolveWorkspaceFile('pkg/a.go', [ROOT, OTHER], exists), `${OTHER}/pkg/a.go`);
  assert.equal(resolveWorkspaceFile('pkg/b.go', [ROOT, OTHER], exists), `${ROOT}/pkg/b.go`);
  // Folder order decides: the first hit wins, not the last.
  present.add(`${ROOT}/pkg/a.go`);
  assert.equal(resolveWorkspaceFile('pkg/a.go', [ROOT, OTHER], exists), `${ROOT}/pkg/a.go`);
  assert.equal(resolveWorkspaceFile('pkg/missing.go', [ROOT, OTHER], exists), null);
});

test('an absolute link path still must land inside a workspace folder', () => {
  const exists = () => true;
  assert.equal(resolveWorkspaceFile(`${ROOT}/pkg/a.go`, [ROOT], exists), `${ROOT}/pkg/a.go`);
  assert.equal(resolveWorkspaceFile('/etc/passwd', [ROOT], exists), null);
  assert.equal(resolveWorkspaceFile(`${ROOT}/../secret.go`, [ROOT], exists), null);
  assert.equal(resolveWorkspaceFile('pkg/../../secret.go', [ROOT], exists), null);
  assert.equal(resolveWorkspaceFile('', [ROOT], exists), null);
  assert.equal(resolveWorkspaceFile('pkg/a.go', [], exists), null);
});

test('a range request is admitted only in shape', () => {
  assert.deepEqual(fileRangeRequestOf({ type: 'openFileRange', path: 'a.go', startLine: 3, endLine: 7 }), {
    path: 'a.go',
    startLine: 3,
    endLine: 7,
  });
  for (const bad of [
    null,
    'a.go:3',
    {},
    { path: '', startLine: 1, endLine: 1 },
    { path: 'a\0.go', startLine: 1, endLine: 1 },
    { path: 3, startLine: 1, endLine: 1 },
    { path: 'a.go', startLine: 0, endLine: 1 },
    { path: 'a.go', startLine: 1.5, endLine: 2 },
    { path: 'a.go', startLine: 5, endLine: 3 },
    { path: 'a.go', startLine: 1, endLine: '2' },
    { path: 'a.go', startLine: 1 },
  ]) {
    assert.equal(fileRangeRequestOf(bad), null, JSON.stringify(bad));
  }
});

test('a range clamps to the document, start first, end never before it', () => {
  assert.deepEqual(clampLineRange(3, 7, 100), { startLine: 3, endLine: 7 });
  assert.deepEqual(clampLineRange(10, 20, 5), { startLine: 5, endLine: 5 });
  assert.deepEqual(clampLineRange(2, 99, 5), { startLine: 2, endLine: 5 });
  assert.deepEqual(clampLineRange(0, 0, 5), { startLine: 1, endLine: 1 });
  assert.deepEqual(clampLineRange(1, 1, 0), { startLine: 1, endLine: 1 });
});