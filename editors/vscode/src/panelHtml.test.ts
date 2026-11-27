import test from 'node:test';
import assert from 'node:assert/strict';
import { missingBuildHtml, readyFrameFor, renderPanelHtml, rewriteIndexHtml, type PanelState } from './panelHtml';

const NONCE = 'f'.repeat(64);

const UNAVAILABLE = [
  { kind: 'unavailable', reason: 'no-token', detail: 'no token yet' },
  { kind: 'unavailable', reason: 'gateway-unreachable', detail: 'nothing answered' },
  { kind: 'unavailable', reason: 'error', detail: 'something broke' },
] as const satisfies readonly PanelState[];

/** Every state the connection document can be in. */
const STATES: readonly PanelState[] = [
  { kind: 'connecting' },
  ...UNAVAILABLE,
];

function buttons(html: string): string[] {
  return [...html.matchAll(/data-command="([^"]+)"/g)].map((m) => m[1]);
}

test('the native loader points assets at the webview base and seals them with a CSP', () => {
  const html = '<html><head><title>Forebrain</title></head><body>' +
    '<script type="module" crossorigin src="/assets/entry-abc123.js"></script>' +
    '<link rel="stylesheet" href="/assets/index-def456.css"></body></html>';
  const out = rewriteIndexHtml(html, 'vscode-webview://webview', 'https://file+.vscode.resource/base');

  assert.ok(out.includes('src="https://file+.vscode.resource/base/assets/entry-abc123.js"'));
  assert.ok(out.includes('href="https://file+.vscode.resource/base/assets/index-def456.css"'));
  assert.ok(out.includes('script-src vscode-webview://webview'));
  assert.ok(out.includes("default-src 'none'"));
  // The CSP sits inside the head, before anything loads.
  assert.ok(out.indexOf('Content-Security-Policy') < out.indexOf('<script'));
  assert.ok(!out.includes('token='), 'the loader carries no token');
});

test('a missing webview build is told, not thrown', () => {
  assert.match(missingBuildHtml(), /webview build is missing/);
});

test('an unavailable panel loads no app and offers exactly the commands of its reason', () => {
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

test('the connection document runs no relay listener and announces itself once', () => {
  for (const state of STATES) {
    const html = renderPanelHtml(state, NONCE);
    assert.ok(!html.includes("window.addEventListener('message'"), `${state.kind} listens for nothing`);
    assert.ok(!html.includes('openFileRange'), 'the native app rides the RPC, not a relayed message');
    assert.ok(html.includes("vscode.postMessage({ type: 'ready' })"), 'the ready handshake stays');
  }
});

test('a rebuilt chat view lands on the conversation it had, and a pending pick wins', () => {
  // Nothing asked, nothing bound: a fresh chat, nothing to steer.
  assert.equal(readyFrameFor('index', {}, undefined), null);
  // A rebuild with no pending pick replays the conversation the host last bound.
  assert.deepEqual(readyFrameFor('index', {}, 's-7'), { type: 'openSession', sessionId: 's-7' });
  // A pick made while the view loaded goes out ahead of the observed one.
  assert.deepEqual(readyFrameFor('index', { chat: 's-9' }, 's-7'), { type: 'openSession', sessionId: 's-9' });
  // The Sessions view is told which row to mark, not opened as a conversation.
  assert.deepEqual(readyFrameFor('sessions', {}, 's-7'), { type: 'activeSession', sessionId: 's-7' });
  assert.deepEqual(readyFrameFor('sessions', { sessions: 's-9' }, 's-7'), { type: 'activeSession', sessionId: 's-9' });
  assert.equal(readyFrameFor('sessions', {}, undefined), null);
});