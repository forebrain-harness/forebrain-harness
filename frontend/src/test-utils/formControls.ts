import { flushPromises } from '@vue/test-utils'

/**
 * Driving a shadcn-vue select in jsdom (plan 096). The element is reka-ui's, so
 * the browser's own `selectOption`/`setValue` is gone with the native select:
 * the trigger opens on Enter and the item commits on pointerup, which is why a
 * plain `.click()` reaches neither.
 *
 * The element is passed in rather than a test id: a component test owns its own
 * wrapper (a modal may render into the document, a page may not), so only the
 * caller knows where to look. `ProvidersView.test.ts` is the reference caller.
 */
export async function openSelect(el: HTMLElement): Promise<void> {
  el.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
  await flushPromises()
}

export function optionTexts(): string[] {
  return [...document.querySelectorAll<HTMLElement>('[role="option"]')].map((o) => (o.textContent ?? '').trim())
}

/** Pick by visible label; the item commits on pointerup, not on click. */
export async function pickOption(el: HTMLElement, label: string): Promise<void> {
  await openSelect(el)
  const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(
    (o) => (o.textContent ?? '').trim() === label,
  )
  if (!option) throw new Error(`no option "${label}"; saw ${JSON.stringify(optionTexts())}`)
  option.dispatchEvent(new Event('pointerup'))
  await flushPromises()
}

/** What a select shows: the trigger's text (SelectValue renders the chosen item). */
export function selectedText(el: HTMLElement): string {
  return (el.textContent ?? '').trim()
}
