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