// Which delivered files a command may open in the editor.
//
// A delivery is named by the agent, so its path is not trusted: only an
// absolute path inside one of the window's workspace folders is openable, and
// everything else is listed but never opened.
import path from 'node:path';
import type { DeliveredFile } from './sessionState';

/** Paths a command may open in the editor: absolute, and inside one of the given workspace roots. */
export function openableDeliverables(files: readonly DeliveredFile[], workspaceRoots: readonly string[]): DeliveredFile[] {
  const roots = workspaceRoots.map((root) => path.resolve(root));
  return files.filter((file) => {
    if (!path.isAbsolute(file.path)) return false;
    const resolved = path.resolve(file.path);
    return roots.some((root) => resolved === root || resolved.startsWith(root + path.sep));
  });
}

/** How an openable delivery is opened. Code and text read in the editor; images
 *  and markdown use VS Code's own preview; the rest (PDF, Office, media,
 *  archives, HTML, SVG) VS Code cannot render, so they go to the OS default app
 *  through `vscode.env.openExternal`. Opening those as a text document — the
 *  original behaviour — showed binary garbage. */
export type DeliverableOpenKind = 'editor' | 'image' | 'markdown' | 'external';

const IMAGE_EXT = new Set(['png', 'jpg', 'jpeg', 'gif', 'bmp', 'webp', 'ico', 'avif']);
const MARKDOWN_EXT = new Set(['md', 'markdown', 'mdx']);
const EXTERNAL_EXT = new Set([
  'pdf', 'svg', 'html', 'htm',
  'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'odt', 'ods', 'odp',
  'zip', 'tar', 'gz', 'tgz', 'rar', '7z',
  'mp4', 'mov', 'webm', 'mkv', 'mp3', 'wav', 'ogg', 'flac',
]);

/** The open route for a delivered file, by extension; anything unlisted is code
 *  or text and reads in the editor. */
export function deliverableOpenKind(filePath: string): DeliverableOpenKind {
  const ext = path.extname(filePath).slice(1).toLowerCase();
  if (IMAGE_EXT.has(ext)) return 'image';
  if (MARKDOWN_EXT.has(ext)) return 'markdown';
  if (EXTERNAL_EXT.has(ext)) return 'external';
  return 'editor';
}

/** A file range the panel asked to open: a path and 1-based lines. */
export interface FileRangeRequest {
  readonly path: string;
  readonly startLine: number;
  readonly endLine: number;
}

/**
 * The shape a range request must have before anything resolves it. The panel
 * script checks its own copy first; the host never trusts that check — this is
 * the one the host runs on what actually arrived.
 */
export function fileRangeRequestOf(value: unknown): FileRangeRequest | null {
  if (typeof value !== 'object' || value === null) return null;
  const { path: requestPath, startLine, endLine } = value as Record<string, unknown>;
  if (typeof requestPath !== 'string' || requestPath.length === 0 || requestPath.includes('\0')) return null;
  if (typeof startLine !== 'number' || !Number.isInteger(startLine) || startLine < 1) return null;
  if (typeof endLine !== 'number' || !Number.isInteger(endLine) || endLine < startLine) return null;
  return { path: requestPath, startLine, endLine };
}

/**
 * Resolves a link's path against the workspace folders, in order, and returns
 * the first candidate that both stays inside a folder and exists. A relative
 * path is tried under every folder; an absolute one resolves to itself and
 * still must land inside a folder. `exists` is injected so the decision is
 * pure and tested; the caller hands it the filesystem.
 */
export function resolveWorkspaceFile(
  requestPath: string,
  workspaceRoots: readonly string[],
  exists: (candidate: string) => boolean,
): string | null {
  if (!requestPath || requestPath.includes('\0')) return null;
  const roots = workspaceRoots.map((root) => path.resolve(root));
  for (const root of roots) {
    const candidate = path.resolve(root, requestPath);
    const inside = roots.some((r) => candidate === r || candidate.startsWith(r + path.sep));
    if (inside && exists(candidate)) return candidate;
  }
  return null;
}

/**
 * Clamps a 1-based range to a document's line count: the start lands within
 * the file, the end never precedes it. A link past the file's end opens the
 * file at its last line instead of failing.
 */
export function clampLineRange(
  startLine: number,
  endLine: number,
  lineCount: number,
): { startLine: number; endLine: number } {
  const last = Math.max(1, lineCount);
  const start = Math.min(Math.max(1, startLine), last);
  const end = Math.min(Math.max(start, endLine), last);
  return { startLine: start, endLine: end };
}