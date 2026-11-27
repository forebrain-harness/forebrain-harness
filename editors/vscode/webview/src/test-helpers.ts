/** The chain from adapter answer to drawn DOM crosses several microtasks and
 *  Vue's own render tick; give it real time. */
export async function flush() {
  for (let i = 0; i < 6; i += 1) await new Promise((resolve) => setTimeout(resolve, 25))
}
