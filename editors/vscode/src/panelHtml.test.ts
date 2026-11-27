import test from 'node:test';
import assert from 'node:assert/strict';
import { panelUrl, renderPanelHtml, type PanelState } from './panelHtml';

const NONCE_HEX = '0123456789abcdef0123456789abcdef';
const ORIGIN = `http://fb-${NONCE_HEX}.localhost:51151`;
const BOOT = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';
const NONCE = 'f'.repeat(64);

const UNAVAILABLE = [
  { kind: 'unavailable', reason: 'no-token', detail: 'no token yet' },
  { kind: 'unavailable', reason: 'gateway-unreachable', detail: 'nothing answered' },
  { kind: 'unavailable', reason: 'error', detail: 'something broke' },
] as const satisfies readonly PanelState[];

/** Every state the panel can be in, with detail text that is safe to inline. */
const STATES: readonly PanelState[] = [
  { kind: 'connecting' },
  { kind: 'ready', relayOrigin: ORIGIN, boot: BOOT },
  ...UNAVAILABLE,
];

function buttons(html: string): string[] {
  return [...html.matchAll(/data-command="([^"]+)"/g)].map((m) => m[1]);
}

test('panelUrl carries the boot secret, an optional session, and refuses anything else', () => {
  assert.equal(panelUrl(ORIGIN, BOOT), `${ORIGIN}/?boot=${BOOT}&host=vscode`);
  assert.equal(panelUrl(ORIGIN, BOOT, 's1'), `${ORIGIN}/?boot=${BOOT}&host=vscode&session=s1`);
  assert.throws(() => panelUrl('http://127.0.0.1:6060', BOOT), /not a relay origin/);
  assert.throws(() => panelUrl(ORIGIN, 'not-a-secret'), /boot secret/);
  assert.throws(() => panelUrl(ORIGIN, BOOT, 'a/b'), /session id/);
});

test('the ready panel frames the relay origin under a CSP that allows exactly it', () => {
  const html = renderPanelHtml({ kind: 'ready', relayOrigin: ORIGIN, boot: BOOT }, NONCE);
  assert.ok(html.includes(`<iframe id="panel" src="${ORIGIN}/?boot=${BOOT}&host=vscode"`), html);
  assert.ok(html.includes(`frame-src ${ORIGIN};`), 'the CSP names the relay origin');
  assert.ok(!html.includes('#token='), 'no sign-in link in the panel');
});

test('the ready panel refuses an origin or boot secret of the wrong shape', () => {
  assert.throws(() => renderPanelHtml({ kind: 'ready', relayOrigin: 'http://127.0.0.1:6060"', boot: BOOT }, NONCE));
  assert.throws(() => renderPanelHtml({ kind: 'ready', relayOrigin: 'javascript:alert(1)', boot: BOOT }, NONCE));
  assert.throws(() => renderPanelHtml({ kind: 'ready', relayOrigin: ORIGIN, boot: '<script>' }, NONCE));
});

test('an unavailable panel frames nothing and offers exactly the commands of its reason', () => {
  const expected: Record<string, string[]> = {
    'no-token': ['forebrain.enterGatewayToken', 'forebrain.reconnect'],
    'gateway-unreachable': ['forebrain.reconnect'],
    error: ['forebrain.reconnect', 'forebrain.openWebUiInBrowser'],
  };
  for (const state of UNAVAILABLE) {
    const html = renderPanelHtml(state, NONCE);
    assert.ok(!html.includes('<iframe'), `${state.reason} renders no frame`);
    assert.ok(!html.includes('frame-src'), `${state.reason} allows no frame source`);
    assert.deepEqual(buttons(html), expected[state.reason], `${state.reason} offers its own commands`);
  }
});

test('the detail text is escaped, so it can never become markup', () => {
  const html = renderPanelHtml({ kind: 'unavailable', reason: 'error', detail: '<img onerror=x>' }, NONCE);
  assert.ok(html.includes('&lt;img'), html);
  assert.ok(!html.includes('<img'), 'the detail is never markup');
});

test('no state puts token material in the document, and one nonce signs both places', () => {
  for (const state of STATES) {
    const html = renderPanelHtml(state, NONCE);
    assert.ok(!html.includes('‹'), 'no redaction marker is invented here');
    assert.ok(!html.includes('Bearer'), 'the panel never carries the authorization header');
    assert.ok(!html.includes('token='), 'the panel never carries a token');
    assert.ok(html.includes(`script-src 'nonce-${NONCE}'`), 'the CSP carries the nonce');
    assert.ok(html.includes(`<script nonce="${NONCE}">`), 'the script carries the same nonce');
    assert.equal(renderPanelHtml(state, NONCE), html, 'the same nonce renders the same document');
  }
});

test('a nonce that is not 64 hex characters is refused', () => {
  assert.throws(() => renderPanelHtml({ kind: 'connecting' }, 'short'), /panel nonce/);
  assert.throws(() => renderPanelHtml({ kind: 'connecting' }, `${'z'.repeat(64)}`), /panel nonce/);
});
test('a ready panel with a session frames that conversation through the relay', () => {
  const html = renderPanelHtml({ kind: 'ready', relayOrigin: ORIGIN, boot: BOOT, session: 's1' }, NONCE);
  assert.ok(html.includes(`<iframe id="panel" src="${ORIGIN}/?boot=${BOOT}&host=vscode&session=s1"`), html);
  assert.ok(html.includes(`frame-src ${ORIGIN};`));
});

test('a session id of the wrong shape is dropped, not thrown: the panel still renders', () => {
  for (const session of ['../x', 'a/b', '', 'a'.repeat(129)]) {
    const html = renderPanelHtml({ kind: 'ready', relayOrigin: ORIGIN, boot: BOOT, session }, NONCE);
    assert.ok(html.includes(`<iframe id="panel" src="${ORIGIN}/?boot=${BOOT}&host=vscode"`), `${JSON.stringify(session)}: ${html}`);
    assert.ok(!html.includes('session='), `${JSON.stringify(session)} carries no session`);
  }
});

test('the ready script relays only a shaped openFileRange from the relay frame itself', () => {
  const html = renderPanelHtml({ kind: 'ready', relayOrigin: ORIGIN, boot: BOOT }, NONCE);
  // The origin is interpolated as a quoted literal, never as raw text.
  assert.ok(html.includes(`e.origin !== ${JSON.stringify(ORIGIN)}`), html);
  assert.ok(html.includes("e.source !== frame.contentWindow"), 'only the panel iframe may post');
  assert.ok(html.includes("m.type !== 'openFileRange'"), 'one whitelisted type');
  assert.ok(html.includes("typeof m.path !== 'string'"), html);
  assert.ok(html.includes('!Number.isInteger(m.startLine) || m.startLine < 1'), html);
  assert.ok(html.includes('!Number.isInteger(m.endLine) || m.endLine < m.startLine'), html);
  assert.ok(html.includes("vscode.postMessage({ type: 'openFileRange', path: m.path, startLine: m.startLine, endLine: m.endLine })"), html);
});

test('the dead status listener is gone and no other type is relayed', () => {
  for (const state of STATES) {
    const html = renderPanelHtml(state, NONCE);
    assert.ok(!html.includes("m.type === 'status'"), 'no listener for a message nothing sends');
    assert.ok(!html.includes('s.textContent = m.text'), 'the status line is never written from a message');
  }
});

test('a panel without a relay frame runs no relay listener at all', () => {
  for (const state of STATES.filter((s) => s.kind !== 'ready')) {
    const html = renderPanelHtml(state, NONCE);
    assert.ok(!html.includes("window.addEventListener('message'"), `${state.kind} listens for nothing`);
    assert.ok(html.includes("vscode.postMessage({ type: 'ready' })"), 'the ready handshake stays');
  }
});
