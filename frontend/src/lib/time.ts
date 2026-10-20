/**
 * How a table cell draws a moment (plan 073): the local date and time to the
 * minute, and an em dash when the row has none. The lists that show it are
 * paged, searched and ordered by the server, so the value is the server's unix
 * seconds (the shape every list endpoint answers in), never a string the
 * browser parsed back into a date.
 */
export function formatDateTime(unixSeconds?: number): string {
  if (!unixSeconds) return '—'
  const date = new Date(unixSeconds * 1000)
  const pad = (value: number) => String(value).padStart(2, '0')
  return (
    `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}` +
    ` ${pad(date.getHours())}:${pad(date.getMinutes())}`
  )
}

/**
 * How a table cell draws a moment coming up (plan 075): the local month and day
 * and the time to the minute, and an em dash when the row has none. A schedule's
 * next fire is read for which day and when, never for the year, so the year is
 * left out to keep the column narrow.
 */
/**
 * How a history row in the workbench draws a moment: the local time alone, to
 * the minute, because the row already sits in one conversation's day and what
 * the reader is ordering is which of today's asks or plans came when.
 */
export function formatClock(unixSeconds?: number): string {
  if (!unixSeconds) return '—'
  const date = new Date(unixSeconds * 1000)
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${pad(date.getHours())}:${pad(date.getMinutes())}`
}

export function formatMonthDayTime(unixSeconds?: number): string {
  if (!unixSeconds) return '—'
  const date = new Date(unixSeconds * 1000)
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`
}
