import { Briefcase, Code, ListChecks, type LucideIcon } from 'lucide-vue-next'

import type { I18nKey } from '@/locales'

import type { ProjectType, ProjectTypeDefaultSource } from './api'

/**
 * The three project kinds as the pages draw them (plan 153): the new-project
 * dialog's three cards, the projects table's type column and the settings
 * tab's current type all read this one list, so the icon and the wording of a
 * kind cannot drift between them.
 */
export interface ProjectTypeOption {
  value: ProjectType
  icon: LucideIcon
  /** The kind's name: 办公创作 / 创意编程 / 后台跑批. */
  label: I18nKey
  /** The one line under the name saying what the kind is for. */
  hint: I18nKey
  /** The badge tone: a category, not a state — coding stays grey, office takes
   *  the brand tint, batch carries the warning tone because it changes what a
   *  person may do at all. */
  badge: 'default' | 'gray' | 'warning'
}

export const PROJECT_TYPES: ProjectTypeOption[] = [
  { value: 'office', icon: Briefcase, label: 'projects.typeOffice', hint: 'projects.typeOfficeHint', badge: 'default' },
  { value: 'coding', icon: Code, label: 'projects.typeCoding', hint: 'projects.typeCodingHint', badge: 'gray' },
  { value: 'batch', icon: ListChecks, label: 'projects.typeBatch', hint: 'projects.typeBatchHint', badge: 'warning' },
]

/** One kind's entry; an unknown value reads as 创意编程, the kind every project
 *  was before types existed. */
export function projectTypeOption(type: ProjectType | string | undefined): ProjectTypeOption {
  return PROJECT_TYPES.find((option) => option.value === type) ?? PROJECT_TYPES[1]
}

/** Why the gateway suggested the default it suggested, as the dialog's one-line
 *  reason. The web never repeats the `.git` check that produced it. */
export function projectTypeDefaultReason(source: ProjectTypeDefaultSource): I18nKey {
  return source === 'git_worktree' ? 'projects.typeDefaultGit' : 'projects.typeDefaultNonGit'
}
