import type { TaskStatus } from '@/lib/api'
import { t, type I18nKey } from '@/locales'

/**
 * The vocabulary of a batch project's tasks (plan 153): the words a status is
 * drawn with, the source a task came in by, how long one execution took and
 * what a refused turn says. Everything here is shared by the task table, the
 * task page and the read-only run view, so the three never disagree about what
 * a status is called.
 */
export const TASK_STATUSES: TaskStatus[] = ['pending', 'running', 'stopped', 'completed', 'failed']

/**
 * One execution, as the read-only conversation page names it (plan 153 A11):
 * the task it belongs to, which run of it this is, and how that run stands.
 * The page draws its top bar and its read-only bar from this alone.
 */
export interface BatchRunFacts {
  projectId: string
  taskId: string
  /** The task's title — the run's page names its task, not the session. */
  title: string
  seq: number
  status: TaskStatus | (string & {})
  /** The model the execution runs on, when the page knows it. */
  model?: string
}

/** What a read-only batch conversation may ask for: the three acts a task's
 *  viewer is allowed, and nothing that would join the run. */
export type BatchRunAction = 'stop' | 'continue' | 'rerun'

/** The operations a run in this state offers, in the prototype's order. */
export function batchRunActions(status: string): BatchRunAction[] {
  if (status === 'running') return ['stop']
  if (status === 'stopped') return ['continue', 'rerun']
  if (status === 'failed') return ['rerun']
  return []
}

/** The status filter's values, in the prototype's order (owner 2026-10-26):
 *  全部状态, then the four states a person filters by. A task whose first turn
 *  has not started yet is still found under 全部状态. */
export function taskStatusFilterOptions(): Array<{ value: string; label: string }> {
  return [
    { value: '', label: t('tasks.statusAll') },
    { value: 'running', label: t('tasks.statusRunning') },
    { value: 'completed', label: t('tasks.statusCompleted') },
    { value: 'failed', label: t('tasks.statusFailed') },
    { value: 'stopped', label: t('tasks.statusStopped') },
  ]
}

/** A task's own status word, from its newest execution. */
export function taskStatusLabel(status: string): string {
  const key: I18nKey = status === 'running'
    ? 'tasks.statusRunning'
    : status === 'completed'
      ? 'tasks.statusCompleted'
      : status === 'failed'
        ? 'tasks.statusFailed'
        : status === 'stopped'
          ? 'tasks.statusStopped'
          : 'tasks.statusPending'
  return t(key)
}

/** The badge a status wears: the palette's own five, never a colour picked here. */
export function taskStatusVariant(status: string): 'default' | 'gray' | 'success' | 'error' {
  switch (status) {
    case 'running':
      return 'default'
    case 'completed':
      return 'success'
    case 'failed':
      return 'error'
    default:
      return 'gray'
  }
}

/** One execution's badge: the same five states, named for a run ("完成", not
 *  "已完成") because the column already says it is an execution. */
export function runStatusLabel(status: string): string {
  const key: I18nKey = status === 'running'
    ? 'tasks.runRunning'
    : status === 'completed'
      ? 'tasks.runDone'
      : status === 'failed'
        ? 'tasks.runFailed'
        : status === 'stopped'
          ? 'tasks.runStopped'
          : 'tasks.runPending'
  return t(key)
}

/** The door a task came in by, in the reader's language. */
export function taskOriginLabel(origin: string): string {
  switch (origin) {
    case 'api':
      return t('tasks.originApi')
    case 'cron':
      return t('tasks.originCron')
    case 'channel':
      return t('tasks.originChannel')
    default:
      return t('tasks.originWeb')
  }
}

/** The 来源 cell: the door, and behind it whatever the door recorded — the
 *  business system's name, the schedule, the channel's recipient. */
export function taskSourceLabel(origin: string, originRef?: string): string {
  const label = taskOriginLabel(origin)
  const ref = String(originRef ?? '').trim()
  return ref ? `${label} · ${ref}` : label
}

/** The words a finished execution's row ends on, when it has no error text of
 *  its own: what a reader wants to know from the result column. */
export function runOutcomeLabel(status: string): string {
  switch (status) {
    case 'completed':
      return t('tasks.resultDone')
    case 'running':
      return t('tasks.resultRunning')
    case 'stopped':
      return t('tasks.resultStopped')
    default:
      return t('tasks.resultPending')
  }
}

/** What one execution took: compact and readable at a glance (26m 53s), or an
 *  em dash while it has no end yet. */
export function formatRunDuration(startedAt?: number, finishedAt?: number): string {
  if (!startedAt || !finishedAt || finishedAt < startedAt) return '—'
  const seconds = Math.max(0, Math.round(finishedAt - startedAt))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  const rest = seconds % 60
  if (minutes < 60) return rest ? `${minutes}m ${String(rest).padStart(2, '0')}s` : `${minutes}m`
  const hours = Math.floor(minutes / 60)
  return `${hours}h ${String(minutes % 60).padStart(2, '0')}m`
}

/**
 * What a batch turn's refusal says (plan 149's stable codes). A caller switches
 * on the code, never on the engine's English sentence, so the rule it names can
 * be read in the viewer's own language. An unknown code falls back to the
 * gateway's sentence — never to silence.
 */
export function batchRefusalMessage(code: string, fallback = ''): string {
  const key: I18nKey | null = code === 'batch_execution_running'
    ? 'tasks.refusalRunning'
    : code === 'batch_execution_finished'
      ? 'tasks.refusalFinished'
      : code === 'batch_takes_no_input'
        ? 'tasks.refusalNoInput'
        : code === 'batch_task_input_fixed'
          ? 'tasks.refusalInputFixed'
          : code === 'batch_task_input_missing'
            ? 'tasks.refusalInputMissing'
            : code === 'batch_admission_required'
              ? 'tasks.refusalAdmission'
              : code === 'batch_mode_fixed'
                ? 'tasks.refusalModeFixed'
                : code === 'batch_command_not_allowed'
                  ? 'tasks.refusalCommand'
                  : code === 'batch_session_has_no_task'
                    ? 'tasks.refusalNoTask'
                    : null
  if (key) return t(key)
  return String(fallback ?? '').trim()
}

/** The refusal code the gateway put in a websocket message's data, if any. */
export function refusalCodeOf(data: unknown): string {
  if (!data || typeof data !== 'object') return ''
  return String((data as Record<string, unknown>).code ?? '').trim()
}
