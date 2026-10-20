/**
 * Reading a plan document: how far its checklist got, and what it calls
 * itself. A plan is Markdown prose with a list in it, so both numbers come
 * from the file's own marks rather than from any progress the writer kept
 * beside it, and the title is the first heading the file carries.
 */
const CHECKBOX = /^\s*[-*+]\s+\[([ xX])\]\s/
const HEADING = /^#{1,6}\s+\S/

export function planProgress(markdown: string): { done: number; total: number } {
  let done = 0
  let total = 0
  for (const line of String(markdown ?? '').split('\n')) {
    const match = CHECKBOX.exec(line)
    if (!match) continue
    total += 1
    if (match[1].toLowerCase() === 'x') done += 1
  }
  return { done, total }
}

export function planTitle(markdown: string): string {
  const heading = String(markdown ?? '')
    .split('\n')
    .map((line) => line.trim())
    .find((line) => HEADING.test(line))
  return heading ? heading.replace(/^#{1,6}\s+/, '') : ''
}
