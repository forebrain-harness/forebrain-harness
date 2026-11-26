import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';

// The declared containers, typed so the assertions below stay checked.
type ViewContainer = { id: string; title: string; icon: string };

const root = join(__dirname, '..');
const manifest = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
const containers: Record<string, ViewContainer[]> = manifest.contributes.viewsContainers ?? {};
const VALID = ['activitybar', 'panel', 'secondarySidebar'];

test('view containers use only valid VS Code contribution points', () => {
  for (const key of Object.keys(containers)) assert.ok(VALID.includes(key), `invalid viewsContainers key: ${key}`);
});

test('the Forebrain container is declared for the secondary sidebar', () => {
  const entry = (containers.secondarySidebar ?? []).find((c) => c.id === 'forebrain');
  assert.ok(entry, 'forebrain container must live in secondarySidebar');
  assert.equal(entry.icon, 'assets/forebrain.svg');
  assert.ok(existsSync(join(root, entry.icon)), 'declared icon must exist');
});

test('every views key references a declared container', () => {
  const ids = Object.values(containers).flat().map((c) => c.id);
  for (const key of Object.keys(manifest.contributes.views ?? {})) assert.ok(ids.includes(key), `views key ${key} has no container`);
});