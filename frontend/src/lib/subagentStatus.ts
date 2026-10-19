import type { SubagentTranscript } from '@/composables/useChatStream'
import { t } from '@/locales'

/** What a subagent's execution is doing, as the surfaces that show it care. */
export type SubagentPhase = 'active' | 'done' | 'failed'

/**
 * The three phases every surface groups a subagent by: still in progress (it
 * runs, or it waits on the operator), finished with an answer, or ended
 * without one. A waiting execution is active because it has not answered yet
 * and the operator can still unblock it.
 */
export function subagentPhase(status: SubagentTranscript['status']): SubagentPhase {
  switch (status) {
    case 'done':
      return 'done'
    case 'failed':
    case 'cancelled':
    case 'interrupted':
      return 'failed'
    default:
      return 'active'
  }
}

/**
 * The status in words, for the drawer's "+" menu and the tab's own header —
 * the same vocabulary the card and the terminal use, never the raw enum with
 * its underscores swapped for spaces.
 */
export function subagentStatusLabel(status: SubagentTranscript['status']): string {
  switch (status) {
    case 'done':
      return t('chat.subagentDone')
    case 'failed':
      return t('chat.subagentFailed')
    case 'cancelled':
      return t('chat.subagentCancelled')
    case 'interrupted':
      return t('chat.subagentInterrupted')
    case 'waiting_approval':
      return t('chat.subagentWaitingApproval')
    case 'waiting_input':
      return t('chat.subagentWaitingInput')
    default:
      return t('chat.subagentRunning')
  }
}

/**
 * A tab is named the way the terminal's roster row is named: what the agent
 * is, then the task it was dispatched to do — the one name the engine derived,
 * never the prompt. Without a title the type alone stands (the engine names
 * every spawn, so this is the fallback for records older than that rule).
 */
export function subagentTabLabel(record: Pick<SubagentTranscript, 'agentType' | 'title'>): string {
  const type = record.agentType || t('agents.subagent')
  const title = (record.title ?? '').trim()
  return title ? `${type} · ${title}` : type
}
