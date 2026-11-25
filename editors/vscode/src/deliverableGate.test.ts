import test from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import { openableDeliverables } from './deliverableGate';

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