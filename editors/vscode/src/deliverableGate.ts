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