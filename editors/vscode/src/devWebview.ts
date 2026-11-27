// The development loading entry for the native webview (plan 391, Step 3):
// `forebrain.devOpenWebview` opens the built webview app in a panel so the
// Steps 3–9 real-machine checks have a surface before the three views of the
// alignment contract exist. Step 10 deletes this file, the command and its
// registrations when the views take over.
//
// Pure module: the vscode types appear only as structural slices, so the unit
// tests load it without the `vscode` stub — the same split approvalText.ts
// draws from approvals.ts.
import * as fs from 'node:fs';
import * as path from 'node:path';

/** The slice of a VS Code webview the loader touches. */
export interface DevWebviewLike {
  readonly cspSource: string;
  asWebviewUri(uri: { readonly fsPath: string }): { toString(): string };
}

/** The slice of a VS Code Uri the loader touches. */
export interface DevUriLike {
  readonly fsPath: string;
}

/** The panel document: one built entry's index with its asset URLs rewritten
 *  to webview URIs and a CSP that allows nothing but the webview's own build.
 *  `entry` names the built html — `index` (the chat workbench) or `sessions`. */
export function devWebviewHtml(webview: DevWebviewLike, distRoot: DevUriLike, entry: string = 'index'): string {
  let index: string;
  try {
    index = fs.readFileSync(path.join(distRoot.fsPath, `${entry}.html`), 'utf-8');
  } catch {
    return missingBuildHtml();
  }
  return rewriteDevIndexHtml(index, webview.cspSource, webview.asWebviewUri(distRoot).toString());
}

/**
 * Pure so the tests hold it to its word: every `/assets/…` reference gains the
 * webview base, and the CSP names the webview scheme as the only source.
 */
export function rewriteDevIndexHtml(html: string, cspSource: string, baseUrl: string): string {
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
