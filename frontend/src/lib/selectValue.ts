/**
 * A dropdown's "empty string" choice. reka-ui's SelectItem refuses an empty
 * `value` (the empty string is what a select shows when nothing is chosen), and
 * several of this project's real options are legitimately empty — the Chat
 * Completions api_path, an empty effort tier, an empty delivery target. A marker
 * no real value can equal stands in for it, translated once on each side.
 */
export const EMPTY_CHOICE = '@empty'

export function toChoice(value: string | null | undefined): string {
  const text = String(value ?? '')
  return text === '' ? EMPTY_CHOICE : text
}

export function fromChoice(choice: unknown): string {
  const text = String(choice ?? '')
  return text === EMPTY_CHOICE ? '' : text
}
