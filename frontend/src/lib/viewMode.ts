import type { ViewMode } from '@/composables/useViewMode'
import type { TimelineBlock } from '@/composables/useChatStream'

/**
 * blockVisibleIn is the one table behind both reading modes: what the view
 * draws for a block of this kind.
 *
 * Concise keeps the answers and the gates — a call's card, what the model
 * thought, the dispatch of a subagent and the checklist the run produced all
 * step out of the way (they are the run's own record, read in the workbench or
 * the terminal). An approval card is never hidden: it is the thing waiting for
 * the person, and hiding it would hide the gate itself. Everything else —
 * an error, a compaction, a goal line, a subagent's task frame — stays: none
 * of it is noise the reader asked to be spared.
 *
 * A new block kind is drawn in both modes until this table says otherwise, so
 * adding one means deciding here what concise does with it.
 */
export function blockVisibleIn(mode: ViewMode, kind: TimelineBlock['kind']): boolean {
  if (mode === 'default') return true
  switch (kind) {
    case 'thinking':
    case 'tool':
    case 'subagent':
    case 'plan':
      return false
    default:
      return true
  }
}
