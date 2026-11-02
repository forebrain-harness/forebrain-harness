import { expect, test, type Page } from '@playwright/test'

import { signIn } from './support'

/**
 * Plan 191 §6's 1:1 check: the prototype is the acceptance standard, so the
 * page and the prototype are photographed side by side — the same scene, the
 * same name, one shot each into the run's shots directory — and, where a fresh
 * install draws the same rows the prototype's reset state draws, the words are
 * compared directly: every group title and every row name the prototype draws,
 * the page draws the same, in the same order.
 *
 * The prototype answers `#s=<scene>` (its own scenario buttons use the same
 * keys), so each shot is one navigation.
 */

/** The review's own screenshot directory, outside the run's shots dir. */
const shots = process.env.E2E_SHOTS ?? '/tmp'

const PROTOTYPE = new URL('../../docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html', import.meta.url)

/** The scene pairs: the prototype's scenario key and the page's own address.
 * `wait` is what each side draws once it is ready — a group for the form
 * scenes, the editor for the configuration-file ones. */
const PAIRS: { scene: string; url: string; shot: string; wait: string }[] = [
  { scene: 'approval', url: '/settings?tab=approval', shot: '191-parity-approval', wait: '[data-group="sww"]' },
  { scene: 'save', url: '/settings?tab=memory', shot: '191-parity-memory', wait: '[data-group="ms"]' },
  { scene: 'session', url: '/settings?tab=session', shot: '191-parity-session', wait: '[data-group="te"]' },
  { scene: 'net', url: '/settings?tab=net', shot: '191-parity-net', wait: '[data-group="wf"]' },
  { scene: 'lsp2', url: '/settings?tab=lsp', shot: '191-parity-lsp', wait: 'main .forebrain-list-toolbar' },
  { scene: 'config', url: '/settings?tab=config', shot: '191-parity-config', wait: '[data-testid="file-editor"]' },
  { scene: 'configerr', url: '/settings?tab=config', shot: '191-parity-config-unsupported', wait: '[data-testid="file-editor"]' },
  { scene: 'configsyntax', url: '/settings?tab=config', shot: '191-parity-config-syntax', wait: '[data-testid="file-editor"]' },
]

/** The prototype's own words for one scene: its group titles and row names. */
async function prototypeWords(page: Page, scene: string): Promise<string[]> {
  await page.goto(`${PROTOTYPE.href}#s=${scene}`)
  await page.waitForSelector(scene.startsWith('config') ? '.ed' : '[data-grp]')
  if (scene.startsWith('config')) return []
  return page.evaluate(() => {
    const words: string[] = []
    for (const group of Array.from(document.querySelectorAll('[data-grp]'))) {
      const title = group.querySelector(':scope > .grp-h > h3')
      if (title) words.push(`group:${(title.textContent ?? '').trim()}`)
      for (const row of Array.from(group.querySelectorAll('.row[data-row]'))) {
        const name = row.querySelector(':scope > .lab > b')
        if (name) words.push(`row:${(name.textContent ?? '').trim()}`)
      }
    }
    return words
  })
}

/** The page's own words for one tab: the same walk over the page's DOM. The
 * `wait` names what the tab draws once its values have landed — the groups a
 * fold hides or shows depend on them. */
async function pageWords(page: Page, url: string, wait: string): Promise<string[]> {
  await page.goto(url, { waitUntil: 'networkidle' })
  await page.waitForSelector(wait)
  return page.evaluate(() => {
    const words: string[] = []
    for (const group of Array.from(document.querySelectorAll('.settings-group'))) {
      const title = group.querySelector(':scope > header > h3')
      if (title) words.push(`group:${(title.textContent ?? '').trim()}`)
      for (const row of Array.from(group.querySelectorAll('.settings-row[data-field]'))) {
        const name = row.querySelector(':scope > .settings-row__label > b')
        if (name) words.push(`row:${(name.textContent ?? '').trim()}`)
      }
    }
    return words
  })
}

test.beforeEach(async ({ page }) => {
  // The design's copy is Chinese; the prototype's words are the assertion.
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
})

test('every scene is photographed beside its page', async ({ page }) => {
  test.info().setTimeout(180_000)
  await signIn(page)

  for (const { scene, url, shot, wait } of PAIRS) {
    await page.goto(`${PROTOTYPE.href}#s=${scene}`)
    await page.waitForSelector(scene.startsWith('config') ? '.ed' : '[data-grp]')
    await page.screenshot({ path: `${shots}/${shot}-prototype.png`, fullPage: true })

    await page.goto(url, { waitUntil: 'networkidle' })
    await page.waitForSelector(wait)
    await page.screenshot({ path: `${shots}/${shot}-impl.png`, fullPage: true })
  }

  // The narrow strip: 390px turns the sub-navigation into a horizontal tab
  // row (the prototype's 窄屏 scene).
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto(`${PROTOTYPE.href}#s=narrow`)
  await page.waitForSelector('[data-grp]')
  await page.screenshot({ path: `${shots}/191-parity-narrow-prototype.png`, fullPage: true })
  await page.goto('/settings?tab=memory', { waitUntil: 'networkidle' })
  await page.waitForSelector('[data-group="ms"]')
  await page.screenshot({ path: `${shots}/191-parity-narrow-impl.png`, fullPage: true })
})

/** The scenes whose reset state a fresh install draws the same way, compared
 * word for word: every group title and every row name, in order. The memory
 * scene is not among them: this run's install has memories off (the memory
 * specs need it), so the page rightly folds the three groups the prototype
 * keeps in the DOM behind its animation. */
const WORD_PAIRS = [
  { scene: 'approval', url: '/settings?tab=approval', wait: '[data-group="sww"]' },
  { scene: 'session', url: '/settings?tab=session', wait: '[data-group="te"]' },
]

for (const { scene, url, wait } of WORD_PAIRS) {
  test(`the page says what the prototype says: ${scene}`, async ({ page }) => {
    await signIn(page)
    const prototype = await prototypeWords(page, scene)
    const live = await pageWords(page, url, wait)
    expect(live, `the page and the prototype differ on ${scene}`).toEqual(prototype)
  })
}
