import type { RouteLocationRaw } from 'vue-router'

/** The layer a workshop was opened from: the agent's skills, or a project's. */
export type WorkshopScope = 'agent' | 'project'

/** What the workshop opens to do: improve a named skill, create a new one, or
 *  discover one to install. */
export type WorkshopMode = 'improve' | 'create' | 'discover'

export interface WorkshopTarget {
  scope: WorkshopScope
  mode: WorkshopMode
  skill: string
  projectId: string
}

/** The workshop route a skills-page button opens, carried on the query. */
export function workshopRoute(target: Partial<WorkshopTarget>): RouteLocationRaw {
  const query: Record<string, string> = {}
  if (target.mode) query.mode = target.mode
  if (target.scope) query.scope = target.scope
  if (target.skill) query.skill = target.skill
  if (target.projectId) query.project = target.projectId
  return { name: 'workshop', query }
}

/** Read the workshop entry back out of a route query. */
export function parseWorkshopTarget(query: Record<string, unknown>): WorkshopTarget {
  const scope = query.scope === 'project' ? 'project' : 'agent'
  const rawMode = String(query.mode ?? '')
  const mode: WorkshopMode = rawMode === 'create' || rawMode === 'discover' ? rawMode : 'improve'
  return {
    scope,
    mode,
    skill: String(query.skill ?? '').trim(),
    projectId: String(query.project ?? '').trim(),
  }
}
