/**
 * The app side of the prose file:line-range links (plan file-line-range-links).
 * The shared core — the regex, the basename guard and the renderer overrides —
 * lives beside MessageResponse in @repo/elements/message, the one component
 * that enables it for every consumer; this module is the app's entry to it.
 */
import type { FileLineRef } from '@repo/elements/message'

export {
  fileLineRefOf,
  fileLineRenderers,
  openFileLineKey,
  splitFileLineLinks,
  type FileLineRef,
  type FileLineSegment,
  type OpenFileLine,
} from '@repo/elements/message'

const HOST_KEY = 'forebrain.host'

/**
 * Reads `?host=vscode` once and writes it to sessionStorage, before any
 * navigation drops the query: the panel's iframe URL carries it on the first
 * load only, and every later page in the same webview still needs to know it
 * is embedded. Called at the top of the conversation pages' setup.
 */
export function captureHostParam(): void {
  const host = new URLSearchParams(window.location.search).get('host')
  if (host === 'vscode') sessionStorage.setItem(HOST_KEY, 'vscode')
}

/** Whether this page runs inside the VS Code panel, now or at first load. */
export function isVscodeHost(): boolean {
  return sessionStorage.getItem(HOST_KEY) === 'vscode'
}

/**
 * Hands a file range to the webview host. The target origin is '*' because
 * the parent frame is vscode-webview:// — an origin the page cannot name; the
 * host's listener validates origin, source and shape before it acts.
 */
export function postFileRangeToHost(ref: FileLineRef): void {
  window.parent.postMessage(
    { type: 'openFileRange', path: ref.path, startLine: ref.from, endLine: ref.to },
    '*',
  )
}

/**
 * The native webview's range transport, registered by the webview app at
 * startup: a native webview is no iframe (window.parent is the window itself
 * and no ?host=vscode ever arrives), so its links ride the embedder's RPC to
 * the host instead of this frame's parent. The browser and the old panel
 * iframe paths stay exactly as they were.
 */
let nativeFileRangeSender: ((ref: FileLineRef) => void) | null = null

/** Registers the native webview's range transport; `null` removes it again. */
export function setNativeFileRangeSender(send: ((ref: FileLineRef) => void) | null): void {
  nativeFileRangeSender = send
}

/** Whether this page's file ranges ride the native webview transport. */
export function isNativeWebviewHost(): boolean {
  return nativeFileRangeSender !== null
}

/** Hands a file range to the host over whichever transport this page has. */
export function sendFileRangeToHost(ref: FileLineRef): void {
  if (nativeFileRangeSender) nativeFileRangeSender(ref)
  else postFileRangeToHost(ref)
}