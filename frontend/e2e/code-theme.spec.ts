import { execFileSync } from 'node:child_process'
import { expect, test } from '@playwright/test'

import { seedProjectSession, signIn, shot } from './support'

/**
 * Web code highlighting is Monokai Light whatever the client's colour scheme
 * is. A dark system preference must not hand the dark palette to the
 * highlighter: the token styles a stored reply renders carry the light
 * colours only.
 *
 * The reply is seeded straight into the gateway's state database — a stored
 * assistant row renders through the same reload path a real turn takes, and
 * the code fence reaches the Markdown highlighter exactly as a model's answer
 * would.
 */
function stateDb(): string {
  const home = process.env.E2E_HOME
  if (!home) throw new Error('E2E_HOME must point at the gateway FOREBRAIN_HOME')
  const found = execFileSync('find', [home, '-name', 'forebrain.state.sqlite'], { encoding: 'utf8' })
    .split('\n').map((line) => line.trim()).filter(Boolean)
  if (!found[0]) throw new Error('no state database found under E2E_HOME')
  return found[0]
}

function sqlite(sql: string): void {
  execFileSync('sqlite3', ['-cmd', '.timeout 5000', stateDb(), sql], { stdio: ['ignore', 'pipe', 'pipe'] })
}

function sqlText(value: string): string {
  return `'${value.replace(/'/g, "''")}'`
}

test('a code block keeps the light palette under a dark system scheme', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  // The whole run is under a dark system preference: the page must not react.
  await page.emulateMedia({ colorScheme: 'dark' })
  await signIn(page)

  const session = await seedProjectSession(page, 'code-theme')
  const now = Math.floor(Date.now() / 1000)
  sqlite(`INSERT INTO fb_messages(session_id, run_id, role, content, parts, created_at)
    VALUES(${sqlText(session)}, 'run-code-theme', 'assistant', ${sqlText('```go\nfunc main() {}\n```')}, '[]', ${now});`)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })

  const block = page.locator('.shiki').first()
  await expect(block).toBeVisible({ timeout: 30_000 })

  // The dark palette never reaches the tokeniser: no token carries the
  // custom property the dark theme swap would read.
  const inner = await block.evaluate((el) => el.innerHTML)
  expect(inner).not.toContain('--shiki-dark')
  // And the block's own ink is the light theme's foreground.
  const ink = await block.evaluate((el) => getComputedStyle(el).color)
  expect(ink).toBe('rgb(17, 17, 17)')

  await page.screenshot({ path: `${process.env.E2E_SHOTS}/code-theme-dark-scheme.png`, fullPage: true })
  await shot(page, 'code-theme-dark-scheme-viewport')
})
