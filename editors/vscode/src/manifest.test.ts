import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';

// The declared containers and views, typed so the assertions below stay checked.
type ViewContainer = { id: string; title: string; icon: string; when?: string };
type View = { id: string; type?: string; when?: string };

const root = join(__dirname, '..');
const manifest = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
const containers: Record<string, ViewContainer[]> = manifest.contributes.viewsContainers ?? {};
const views: Record<string, View[]> = manifest.contributes.views ?? {};
const VALID = ['activitybar', 'panel', 'secondarySidebar'];

test('view containers use only valid VS Code contribution points', () => {
  for (const key of Object.keys(containers)) assert.ok(VALID.includes(key), `invalid viewsContainers key: ${key}`);
});

test('the chat container is declared for both the secondary sidebar and the activity-bar fallback', () => {
  const secondary = (containers.secondarySidebar ?? []).find((c) => c.id === 'forebrain-sidebar-secondary');
  assert.ok(secondary, 'the chat container lives in secondarySidebar');
  assert.equal(secondary.when, '!forebrain.doesNotSupportSecondarySidebar');
  const fallback = (containers.activitybar ?? []).find((c) => c.id === 'forebrain-sidebar');
  assert.ok(fallback, 'the activity-bar fallback exists for VS Code below 1.106');
  assert.equal(fallback.when, 'forebrain.doesNotSupportSecondarySidebar');
  for (const entry of [secondary, fallback]) {
    assert.equal(entry.icon, 'assets/forebrain.svg');
    assert.ok(existsSync(join(root, entry.icon)), 'declared icon must exist');
  }
});

test('every views key references a declared container', () => {
  const ids = Object.values(containers).flat().map((c) => c.id);
  for (const key of Object.keys(views)) assert.ok(ids.includes(key), `views key ${key} has no container`);
});

test('the alignment contract is three webview views, one app shown by capability plus Sessions', () => {
  const all = Object.values(views).flat();
  const webviews = all.filter((v) => v.type === 'webview');
  assert.equal(webviews.length, 3, 'exactly three webview views');
  const ids = webviews.map((v) => v.id).sort();
  assert.deepEqual(ids, ['forebrain.chatSecondary', 'forebrain.chatSidebar', 'forebrain.sessionsList']);
  const byId = Object.fromEntries(webviews.map((v) => [v.id, v]));
  assert.equal(byId['forebrain.chatSidebar'].when, 'forebrain.doesNotSupportSecondarySidebar');
  assert.equal(byId['forebrain.chatSecondary'].when, '!forebrain.doesNotSupportSecondarySidebar');
  assert.equal(byId['forebrain.sessionsList'].when, 'forebrain.sessionsListEnabled');
});

test('the extension activates on startup and declares the untrusted-workspace capability', () => {
  assert.ok(manifest.activationEvents.includes('onStartupFinished'), 'onStartupFinished activates the host');
  const cap = manifest.capabilities?.untrustedWorkspaces;
  assert.ok(cap, 'untrustedWorkspaces capability is declared');
  assert.equal(cap.supported, false, 'the extension stays off until the folder is trusted');
});

test('the retired checklist tree and dev loading command are gone', () => {
  const all = Object.values(views).flat().map((v) => v.id);
  assert.ok(!all.includes('forebrain.checklist'), 'the independent checklist tree is removed');
  assert.ok(!manifest.contributes.viewsWelcome, 'no checklist welcome view remains');
  const commands = (manifest.contributes.commands ?? []).map((c: { command: string }) => c.command);
  assert.ok(!commands.includes('forebrain.devOpenWebview'), 'the dev loading command is removed');
  assert.ok(!manifest.contributes.configuration.properties['forebrain.relayLogRequests'], 'the relay log setting is removed');
});