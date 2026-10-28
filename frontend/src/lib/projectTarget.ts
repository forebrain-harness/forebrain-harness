import type { ProjectRecord, ProjectType, TrustLevel } from '@/lib/api'

/**
 * The project or folder a new conversation will run in — what the project pill
 * shows and what the chat page hands the gateway when it opens the session.
 * An existing project is named by id; a folder that is not yet a project is
 * named by path and the gateway creates the project from it.
 */
export interface ProjectTarget {
  /** Set when the target is an existing project. */
  projectId?: string
  /** Set when the target is a folder with no project yet. */
  folder?: string
  name: string
  root: string
  /** True for a folder that is not yet a project. */
  isNew: boolean
  trustLevel: TrustLevel
  /** The existing project's kind. A batch project takes no conversation, so
   *  the new-chat page offers its task page instead of a composer (plan 153). */
  projectType?: ProjectType
}

/** The target a project row becomes. */
export function targetFromProject(p: ProjectRecord): ProjectTarget {
  return {
    projectId: p.id,
    name: p.name,
    root: p.root,
    isNew: false,
    trustLevel: p.trustLevel ?? (p.trusted ? 'trusted' : 'unknown'),
    projectType: p.projectType,
  }
}

/**
 * The target a picked folder becomes: the existing project bound to it, or a
 * new project named for the folder's last segment.
 */
export function targetFromFolder(folder: string, existing?: ProjectRecord | null): ProjectTarget {
  if (existing) return targetFromProject(existing)
  const name = folder.split('/').filter(Boolean).pop() ?? folder
  return { folder, name, root: folder, isNew: true, trustLevel: 'unknown' }
}

/** The name of a path's last segment, for a folder's prospective project name. */
export function baseName(path: string): string {
  return path.replace(/\/+$/, '').split('/').filter(Boolean).pop() ?? path
}
