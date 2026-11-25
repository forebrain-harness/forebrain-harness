import { randomBytes } from 'node:crypto';

export type PanelState =
  | { kind: 'connecting' }
  /** `session` names the conversation the panel must show; absent, the Web UI opens its own last one. */
  | { kind: 'ready'; relayOrigin: string; boot: string; session?: string }
  | { kind: 'unavailable'; reason: 'no-token' | 'gateway-unreachable' | 'error'; detail: string };

/** The commands the webview may ask the host to run. Anything else is ignored. */
export const PANEL_COMMANDS = new Set(['forebrain.enterGatewayToken', 'forebrain.reconnect', 'forebrain.openWebUiInBrowser']);

const RELAY_ORIGIN = /^http:\/\/fb-[0-9a-f]{32}\.localhost:\d{1,5}$/;
const SECRET = /^[0-9a-f]{32}$/;   // the relay's boot secret
const SAFE_ID = /^[0-9a-f]{64}$/;  // the nonce of this page's own script
const SESSION_ID = /^[A-Za-z0-9_-]{1,128}$/;

/**
 * The URL the panel's iframe loads: the relay origin, the boot secret the relay
 * insists on for a navigation, and (from plan 379 on) the session the Web UI
 * should open. Throws on anything that does not match its shape, so no
 * unvalidated text ever reaches an attribute.
 */
export function panelUrl(relayOrigin: string, boot: string, session?: string): string {
  if (!RELAY_ORIGIN.test(relayOrigin)) throw new Error('not a relay origin');
  if (!SECRET.test(boot)) throw new Error('boot secret must be 32 hex characters');
  let url = `${relayOrigin}/?boot=${boot}`;
  if (session !== undefined) {
    if (!SESSION_ID.test(session)) throw new Error('session id has an unexpected shape');
    url += `&session=${session}`;
  }
  return url;
}

/**
 * The panel's whole document. It takes no token: what varies with the
 * connection is the relay URL, and that URL is the capability — a page that
 * can load it is the panel, and nothing else.
 */
export function renderPanelHtml(state: PanelState, nonce: string = randomBytes(32).toString('hex')): string {
  if (!SAFE_ID.test(nonce)) throw new Error('panel nonce must be 64 hex characters');
  const frame = state.kind === 'ready' ? ` frame-src ${state.relayOrigin};` : '';
  const body =
    state.kind === 'ready'
      ? readyBody(state.relayOrigin, state.boot, state.session)
      : statusBody(state);
  return `<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-${nonce}';${frame}">
<title>Forebrain</title></head>
<body>${body}
<script nonce="${nonce}">${PANEL_SCRIPT}</script>
</body></html>`;
}

function escapeHtml(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;');
}

// VS Code's own theme variables, so the panel matches the editor it lives in.
const STYLE = `html,body{margin:0;height:100%;background:var(--vscode-panel-background);color:var(--vscode-foreground);font-family:var(--vscode-font-family);font-size:var(--vscode-font-size)}
iframe{width:100%;height:100%;border:0;display:block}
#status{box-sizing:border-box;padding:1.5em;height:100%;display:flex;flex-direction:column;gap:.75em;justify-content:center}
#status h1{font-size:1em;margin:0;font-weight:600}
#status p{margin:0;color:var(--vscode-descriptionForeground);line-height:1.5}
#status button{align-self:flex-start;border:0;border-radius:2px;padding:.45em 1em;cursor:pointer;background:var(--vscode-button-background);color:var(--vscode-button-foreground);font-family:inherit;font-size:inherit}
#status button:hover{background:var(--vscode-button-hoverBackground,var(--vscode-button-background))}`;

interface Button {
  readonly label: string;
  readonly command: string;
}

interface Copy {
  readonly headline: string;
  readonly buttons: readonly Button[];
}

const RECONNECT: Button = { label: 'Reconnect', command: 'forebrain.reconnect' };

function copy(state: PanelState): Copy {
  if (state.kind === 'connecting') return { headline: 'Connecting to Forebrain…', buttons: [] };
  if (state.kind === 'ready') return { headline: '', buttons: [] };
  switch (state.reason) {
    case 'no-token':
      return {
        headline: 'Forebrain needs the gateway token.',
        buttons: [{ label: 'Enter gateway token', command: 'forebrain.enterGatewayToken' }, RECONNECT],
      };
    case 'gateway-unreachable':
      return { headline: 'The Forebrain gateway is not answering.', buttons: [RECONNECT] };
    case 'error':
      return {
        headline: 'Forebrain could not connect.',
        buttons: [RECONNECT, { label: 'Open Web UI in browser', command: 'forebrain.openWebUiInBrowser' }],
      };
  }
}

function readyBody(relayOrigin: string, boot: string, session?: string): string {
  // A session id of the wrong shape is dropped, not thrown: one bad id must not
  // cost the whole panel its document.
  const url = panelUrl(relayOrigin, boot, session !== undefined && SESSION_ID.test(session) ? session : undefined);
  return `<style>${STYLE}</style>
<iframe id="panel" src="${url}" title="Forebrain" allow="clipboard-read; clipboard-write"></iframe>`;
}

function statusBody(state: PanelState): string {
  const view = copy(state);
  const detail = state.kind === 'unavailable' ? `<p>${escapeHtml(state.detail)}</p>` : '';
  const buttons = view.buttons
    .map((b) => `<button data-command="${b.command}">${b.label}</button>`)
    .join('');
  const reason = state.kind === 'unavailable' ? state.reason : state.kind;
  return `<style>${STYLE}</style>
<div id="status" class="${reason}">
<h1>${view.headline}</h1>${detail}${buttons}
</div>`;
}

// The only script the panel runs: forward a whitelisted button to the host and
// show a status line the host may push. It never sees the token.
const PANEL_SCRIPT = `const vscode = acquireVsCodeApi();
for (const b of document.querySelectorAll('button[data-command]')) {
  b.addEventListener('click', () => vscode.postMessage({ type: 'command', id: b.dataset.command }));
}
window.addEventListener('message', (e) => {
  const m = e.data;
  if (m && m.type === 'status' && typeof m.text === 'string') {
    const s = document.getElementById('status');
    if (s) { s.hidden = false; s.textContent = m.text; }
  }
});
vscode.postMessage({ type: 'ready' });`;