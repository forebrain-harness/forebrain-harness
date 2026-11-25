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
  assert.equal(panelUrl(ORIGIN, BOOT), `${ORIGIN}/?boot=${BOOT}`);
  assert.equal(panelUrl(ORIGIN, BOOT, 's1'), `${ORIGIN}/?boot=${BOOT}&session=s1`);
  assert.throws(() => panelUrl('http://127.0.0.1:6060', BOOT), /not a relay origin/);
  assert.throws(() => panelUrl(ORIGIN, 'not-a-secret'), /boot secret/);
  assert.throws(() => panelUrl(ORIGIN, BOOT, 'a/b'), /session id/);
});

test('the ready panel frames the relay origin under a CSP that allows exactly it', () => {
  const html = renderPanelHtml({ kind: 'ready', relayOrigin: ORIGIN, boot: BOOT }, NONCE);
  assert.ok(html.includes(`<iframe id="panel" src="${ORIGIN}/?boot=${BOOT}"`), html);
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
  assert.ok(html.includes(`<iframe id="panel" src="${ORIGIN}/?boot=${BOOT}&session=s1"`), html);
  assert.ok(html.includes(`frame-src ${ORIGIN};`));
});

test('a session id of the wrong shape is dropped, not thrown: the panel still renders', () => {
  for (const session of ['../x', 'a/b', '', 'a'.repeat(129)]) {
    const html = renderPanelHtml({ kind: 'ready', relayOrigin: ORIGIN, boot: BOOT, session }, NONCE);
    assert.ok(html.includes(`<iframe id="panel" src="${ORIGIN}/?boot=${BOOT}"`), `${JSON.stringify(session)}: ${html}`);
    assert.ok(!html.includes('session='), `${JSON.stringify(session)} carries no session`);
  }
});
