import { execFileSync } from 'node:child_process'
import { expect, test } from '@playwright/test'

import { expectAssistantReply, signIn } from './support'

/**
 * The state database the gateway runs on, located the way an outsider has to:
 * by name under E2E_HOME (web_e2e.sh passes it in). Today the library is
 * state/forebrain.state.sqlite; the *.db arm keeps the search honest if the
 * name moves again.
 */
function stateDb(): string {
  const home = process.env.E2E_HOME
  if (!home) throw new Error('E2E_HOME must point at the gateway FOREBRAIN_HOME')
  const found = execFileSync('find', [home, '-name', '*.db', '-o', '-name', 'forebrain.state.sqlite'], { encoding: 'utf8' })
    .split('\n').map((line) => line.trim()).filter(Boolean)
  const db = found.find((path) => path.endsWith('forebrain.state.sqlite')) ?? found[0]
  if (!db) throw new Error(`no state database found under ${home}`)
  return db
}

/** One sqlite3 statement batch against the gateway's own state database. */
function sqlite(sql: string): void {
  // The gateway writes to the same file (its owner lease renews every few
  // seconds), so a write may meet the lock; wait for it rather than racing.
  execFileSync('sqlite3', ['-cmd', '.timeout 5000', stateDb(), sql], { stdio: ['ignore', 'pipe', 'pipe'] })
}

/**
 * A session that already has one live run refuses a new message: the runtime
 * says why with a stable code, the page writes the sentence in the viewer's
 * language — following a switch made while the notice is on screen — and the
 * message waits in the composer whole, without adding a user row. Once the
 * run's owner stops renewing its lease, the same message goes through.
 */
test('a busy session refuses a new message until the run owning it is gone', async ({ page }) => {
  test.info().setTimeout(120_000)
  // Pin the language so the first assertion reads the Chinese sentence and
  // the switch below tests the notice following the viewer, not whatever
  // Chrome's default locale was.
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const created = await page.request.post('/api/chat/sessions', { data: {} })
  expect(created.ok()).toBeTruthy()
  const session = String((await created.json()).id)

  // A run from a process the gateway cannot see drives the conversation; its
  // owner's lease is fresh, so the session counts as busy to everyone.
  sqlite(`
    INSERT INTO fb_runs (id, session_id, input_text, status, owner, created_at, updated_at)
      VALUES ('e2e-busy-run', '${session}', 'occupied by another process', 'running', 'e2e-busy-owner',
              CAST(strftime('%s','now') AS INTEGER), CAST(strftime('%s','now') AS INTEGER));
    INSERT INTO fb_run_owners (owner, heartbeat_at_ms)
      VALUES ('e2e-busy-owner', CAST(strftime('%s','now') AS INTEGER) * 1000);
  `)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await page.fill('textarea', 'is anybody out there?')
  await page.press('textarea', 'Enter')

  // The refusal is said in the viewer's language, the message is the user's
  // draft again, and the conversation gained no user row: the turn the
  // message would have started never existed.
  const noticeZh = page.getByText('这个对话正在运行一个回合，等它结束后再发送。')
  await expect(noticeZh).toBeVisible()
  await expect(page.locator('textarea')).toHaveValue('is anybody out there?')
  await expect(page.locator('[role=log] .is-user')).toHaveCount(0)

  // The sentence is drawn from the code, so it follows a language switch made
  // while the notice is on screen — the way a run's error block does.
  await page.click('.forebrain-language-trigger')
  await page.locator('.forebrain-language-option', { hasText: 'English' }).click()
  await expect(page.getByText('This conversation is already running a turn; send again when it finishes.')).toBeVisible()
  await expect(noticeZh).toHaveCount(0)

  // The owner stops renewing: the run it left behind no longer counts as
  // alive, and the same message goes through as a turn of its own.
  sqlite(`UPDATE fb_run_owners SET heartbeat_at_ms = 1 WHERE owner = 'e2e-busy-owner';`)
  await page.press('textarea', 'Enter')
  await expectAssistantReply(page)
  await expect(page.locator('[role=log] .is-user', { hasText: 'is anybody out there?' })).toHaveCount(1)
})
