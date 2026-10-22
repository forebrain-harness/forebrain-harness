/**
 * The open-modal stack, in open order. A modal registers itself while it is
 * shown; Escape and a mask click only ever close the one on top, so a dialog
 * raised over another (a confirm over a form) does not dismiss both at once.
 *
 * The shells are the library's Dialog/Sheet (plan 096), whose own dismiss paths
 * are silenced: whichever of the two is on the page, this stack still decides
 * which one closes.
 */
const stack: symbol[] = []

export function pushModal(id: symbol): void {
  stack.push(id)
}

export function popModal(id: symbol): void {
  const index = stack.lastIndexOf(id)
  if (index >= 0) stack.splice(index, 1)
}

export function isTopModal(id: symbol): boolean {
  return stack.length > 0 && stack[stack.length - 1] === id
}
