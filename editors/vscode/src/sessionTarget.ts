// Which conversation a command sends into.
//
// The owner's ruling (2026-11-24): a selection goes into the conversation the
// panel is showing; the setting can change that to always a new one. The order
// below is the contract — observed, then explicit, then recent, then new.
import path from 'node:path';

export type SelectionTarget = 'current' | 'always-new';

export interface TargetInputs {
  readonly setting: SelectionTarget;
  /** The session this extension last sent into. */
  readonly explicit: string | undefined;
  /** The session the panel is showing, as the relay saw the Web UI read it. */
  readonly observed: string | undefined;
  /** The newest conversation of the workspace folder's project. */
  readonly recent: string | undefined;
}

/** The SPA loads a session's transcript at this path; its query is irrelevant. */
const MESSAGES = /^\/api\/chat\/sessions\/([A-Za-z0-9_-]{1,128})\/messages(?:\?|$)/;

/** Which existing session a command targets, or null = create a new one. */
export function resolveTargetSession(input: TargetInputs): string | null {
  if (input.setting === 'always-new') return null;
  return input.observed ?? input.explicit ?? input.recent ?? null;
}

/** The session the Web UI is reading. */
export function sessionFromRequestPath(method: string, requestPath: string): string | undefined {
  if (method !== 'GET') return undefined;
  return MESSAGES.exec(requestPath)?.[1];
}

/** The project row whose root is the folder (both through path.resolve; no symlink chase). */
export function projectForFolder(
  projects: readonly { readonly id: string; readonly root: string }[],
  folder: string,
): { id: string; root: string } | undefined {
  const wanted = path.resolve(folder);
  return projects.find((project) => path.resolve(project.root) === wanted);
}

/** The newest conversation of a project: the list endpoint answers newest first. */
export function newestSession(records: readonly { readonly id: string }[]): string | undefined {
  return records[0]?.id;
}

/** Title of the conversation a command creates. */
export function sessionTitle(fileBasename: string): string {
  return `VS Code: ${fileBasename}`;
}