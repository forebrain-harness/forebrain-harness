import test from 'node:test';
import assert from 'node:assert/strict';
import { missingBuildHtml, rewriteDevIndexHtml } from './devWebview';

test('the dev index rewrite points assets at the webview base and seals them with a CSP', () => {
  const html = '<html><head><title>Forebrain</title></head><body>' +
    '<script type="module" crossorigin src="/assets/entry-abc123.js"></script>' +
    '<link rel="stylesheet" href="/assets/index-def456.css"></body></html>';
  const out = rewriteDevIndexHtml(html, 'vscode-webview://webview', 'https://file+.vscode.resource/base');

  assert.ok(out.includes('src="https://file+.vscode.resource/base/assets/entry-abc123.js"'));
  assert.ok(out.includes('href="https://file+.vscode.resource/base/assets/index-def456.css"'));
  assert.ok(out.includes("script-src vscode-webview://webview"));
  assert.ok(out.includes("default-src 'none'"));
  // The CSP sits inside the head, before anything loads.
  assert.ok(out.indexOf('Content-Security-Policy') < out.indexOf('<script'));
});

test('a missing webview build is told, not thrown', () => {
  assert.match(missingBuildHtml(), /webview build is missing/);
});
