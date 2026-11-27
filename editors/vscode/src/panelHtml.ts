import { randomBytes } from 'node:crypto';
import * as fs from 'node:fs';
import * as path from 'node:path';

export type PanelState =
  | { kind: 'connecting' }
  /** The host holds a gateway token; `session` names the conversation to open, if one was asked for. */
  | { kind: 'ready'; session?: string }
  | { kind: 'unavailable'; reason: 'no-token' | 'gateway-unreachable' | 'error'; detail: string };

/** The commands the webview may ask the host to run. Anything else is ignored. */
export const PANEL_COMMANDS = new Set(['forebrain.enterGatewayToken', 'forebrain.reconnect', 'forebrain.openWebUiInBrowser']);

/**
 * What the host pushes when a view announces itself ready: the conversation a
 * pick asked for while it loaded, or else the one the host last saw bound — so
 * a chat view that comes back after a rebuild lands on the conversation it had
 * and re-binds its stream. The Sessions view is told which row to mark. Null
 * when there is nothing to steer (a fresh chat). Pure, so the rebuild replay is
 * held to its word without a VS Code instance.
 */
export function readyFrameFor(
  entry: 'index' | 'sessions',
  pending: { chat?: string; sessions?: string },
  observed: string | undefined,
): { type: 'openSession' | 'activeSession'; sessionId: string } | null {
  const sessionId = entry === 'index' ? (pending.chat ?? observed) : (pending.sessions ?? observed);
  if (!sessionId) return null;
  return { type: entry === 'index' ? 'openSession' : 'activeSession', sessionId };
}

const SAFE_ID = /^[0-9a-f]{64}$/;  // the nonce of this page's own script

/** The slice of a VS Code webview the loader touches. */
export interface WebviewLike {
  readonly cspSource: string;
  asWebviewUri(uri: { readonly fsPath: string }): { toString(): string };
}

/** The slice of a VS Code Uri the loader touches. */
export interface UriLike {
  readonly fsPath: string;
}

/**
 * The native app's document: one built entry's index with its asset URLs
 * rewritten to webview URIs and a CSP that allows nothing but the webview's own
 * build. `entry` names the built html — `index` (the chat workbench) or
 * `sessions`. The token never enters this document: the app reaches the gateway
 * only through the host's RPC proxy.
 */
export function renderWebviewHtml(webview: WebviewLike, distRoot: UriLike, entry: string = 'index'): string {
  let index: string;
  try {
    index = fs.readFileSync(path.join(distRoot.fsPath, `${entry}.html`), 'utf-8');
  } catch {
    return missingBuildHtml();
  }
  return rewriteIndexHtml(index, webview.cspSource, webview.asWebviewUri(distRoot).toString());
}

/**
 * Pure so the tests hold it to its word: every `/assets/…` reference gains the
 * webview base, and the CSP names the webview scheme as the only source.
 */
export function rewriteIndexHtml(html: string, cspSource: string, baseUrl: string): string {
  const withUris = html
    .split('src="/assets/').join(`src="${baseUrl}/assets/`)
    .split('href="/assets/').join(`href="${baseUrl}/assets/`);
  const csp =
    `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; ` +
    `script-src ${cspSource}; style-src ${cspSource} 'unsafe-inline'; img-src ${cspSource} data:; font-src ${cspSource}">`;
  return withUris.includes('<head>') ? withUris.replace('<head>', `<head>${csp}`) : `${csp}${withUris}`;
}

/** What a panel shows when nobody ran the webview build yet. */
export function missingBuildHtml(): string {
  return '<html><body><p>The webview build is missing. Run it once:</p>' +
    '<pre>cd frontend &amp;&amp; corepack pnpm --filter @forebrain/vscode-webview build</pre></body></html>';
}

/**
 * The connection-state document: what the panel shows while the host has no
 * gateway to proxy yet. It takes no token and loads no app — the buttons ask the
 * host to run a whitelisted command, and the host re-renders once it connects.
 */
export function renderPanelHtml(state: PanelState, nonce: string = randomBytes(32).toString('hex')): string {
  if (!SAFE_ID.test(nonce)) throw new Error('panel nonce must be 64 hex characters');
  return `<!DOCTYPE html>
<html lang="en"><head><meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-${nonce}';">
<title>Forebrain</title></head>
<body>${statusBody(state)}
<script nonce="${nonce}">${panelScript()}</script>
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

// The only script the state document runs: forward a whitelisted button to the
// host, and announce the page so a pick made while it loaded goes out now. It
// never sees the token.
function panelScript(): string {
  return `const vscode = acquireVsCodeApi();
for (const b of document.querySelectorAll('button[data-command]')) {
  b.addEventListener('click', () => vscode.postMessage({ type: 'command', id: b.dataset.command }));
}
vscode.postMessage({ type: 'ready' });`;
}