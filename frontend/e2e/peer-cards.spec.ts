import { execFileSync, spawn } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

import { expect, test } from '@playwright/test'

import { seedSessionInRoot, shot, signIn } from './support'

/**
 * The two peer facts a page can be shown (plan 200 §3), proven on the real
 * gateway at the real width:
 *
 * - a letter from another agent: a user row whose origin is `peer`, drawn
 *   left-aligned and unframed with the sender's identity block and the body
 *   the envelope carried — never as a bubble of the reader's own;
 * - a message held for the person: the banner above the composer, and what
 *   releasing it does — the banner collapses, the letter lands in the thread,
 *   and the turn the release started runs.
 *
 * The held case needs a real second process. A held message lives in the
 * receiving process's memory, so no database row can stand in for it; the spec
 * starts `peer-fake.mjs`, which registers as a peer and sends one frame, and
 * the gateway holds it the way it would hold a real agent's.
 */

// The drawer and the banner both read best at the harness's own width.
test.use({ viewport: { width: 1440, height: 900 } })

/** The gateway's state database, by name under E2E_HOME. */
function stateDb(): string {
  const home = process.env.E2E_HOME
  if (!home) throw new Error('E2E_HOME must point at the gateway FOREBRAIN_HOME')
  const found = execFileSync('find', [home, '-name', 'forebrain.state.sqlite'], { encoding: 'utf8' })
    .split('\n').map((line) => line.trim()).filter(Boolean)
  if (!found[0]) throw new Error('no state database found under E2E_HOME')
  return found[0]
}

/** One sqlite3 statement batch against the gateway's own state database. */
function sqlite(sql: string): void {
  execFileSync('sqlite3', ['-cmd', '.timeout 5000', stateDb(), sql], { stdio: ['ignore', 'pipe', 'pipe'] })
}

/** SQL quoting for a string stored in a text column. */
function sqlText(value: string): string {
  return value.replace(/'/g, "''")
}

/** The envelope a peer message arrives in, with the attributes in wire order
 *  and the body on its own line — the exact shape `peer.Build` renders. */
const FROM_NAME = 'forebrain-harness-28'
const BODY = 'are you still editing open.go? which parts should I leave alone?'
const ENVELOPE =
  `<cross-session-message from="uds:/tmp/forebrain-e2e.sock" from-name="${FROM_NAME}" from-mode="prompting">\n${BODY}\n</cross-session-message>`

/** A user row another session wrote: meta, origin `peer`, and the envelope as
 *  its content — the shape `turn.PersistUserTurn` writes for a peer prompt. */
function seedPeerMessage(session: string): void {
  const now = Math.floor(Date.now() / 1000)
  const parts = JSON.stringify([{ type: 'is_meta' }, { type: 'origin', kind: 'peer' }])
  sqlite(`INSERT INTO fb_messages(session_id, run_id, role, content, parts, created_at)
    VALUES('${session}', '', 'user', '${sqlText(ENVELOPE)}', '${sqlText(parts)}', ${now});`)
}

/** The stand-in peer the held case sends from: its name, and the two characters
 *  the identity block derives from the last dash-separated segment of it. */
const HELD_NAME = 'forebrain-harness-5e'
const HELD_MARK = '5e'

/** A project of its own and a conversation in it, rooted there. */
async function seedPeerSession(page: import('@playwright/test').Page, name: string): Promise<{ session: string; root: string }> {
  const parent = process.env.E2E_PROJECT_PARENT
  if (!parent) throw new Error('E2E_PROJECT_PARENT must point outside the forebrain home')
  const root = join(parent, 'e2e-peer', `${name}-${Date.now().toString(36)}`)
  mkdirSync(root, { recursive: true })
  const session = await seedSessionInRoot(page, root, name)
  return { session, root }
}

/** Start the stand-in peer and resolve once the gateway has held its message.
 *  The registry and socket directories are the acceptance run's own, so the
 *  record it writes and the socket it listens on are scratch, not the user's. */
function sendHeldPeerMessage(root: string): Promise<void> {
  const registry = process.env.FOREBRAIN_PEERS_DIR
  const sockets = process.env.FOREBRAIN_PEER_SOCKET_DIR
  if (!registry || !sockets) {
    return Promise.reject(new Error('FOREBRAIN_PEERS_DIR and FOREBRAIN_PEER_SOCKET_DIR must point at this run\'s scratch tree'))
  }
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [join(import.meta.dirname, 'peer-fake.mjs')], {
      env: {
        ...process.env,
        FOREBRAIN_PEERS_DIR: registry,
        FOREBRAIN_PEER_SOCKET_DIR: sockets,
        E2E_PEER_ROOT: root,
        E2E_PEER_NAME: HELD_NAME,
        E2E_PEER_MESSAGE: BODY,
        // The receiving session runs prompting; a sender that claims bypass is
        // the mismatch that makes the gateway hold the message for the person.
        E2E_PEER_FROM_MODE: 'bypass',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    let out = ''
    let err = ''
    const timer = setTimeout(() => {
      child.kill()
      reject(new Error(`fake peer timed out\nstdout: ${out}\nstderr: ${err}`))
    }, 30_000)
    child.stdout.on('data', (chunk) => {
      out += String(chunk)
      if (out.includes('HELD')) {
        clearTimeout(timer)
        resolve()
      }
    })
    child.stderr.on('data', (chunk) => { err += String(chunk) })
    child.on('error', (error) => {
      clearTimeout(timer)
      reject(error)
    })
  })
}

test('a letter from another agent is drawn as a letter, not the reader\'s own bubble', async ({ page }) => {
  await signIn(page)
  const { session } = await seedPeerSession(page, 'e2e-peer-letter')
  seedPeerMessage(session)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  const letter = page.locator('[data-testid="peer-message-28"]')
  await expect(letter).toBeVisible()
  await expect(letter).toContainText(FROM_NAME)
  await expect(letter).toContainText(BODY)
  // The envelope itself never reaches the reader: the body is what the gateway
  // took out of it.
  await expect(letter).not.toContainText('<cross-session-message')
  await shot(page, 'peer-letter')
})

test('a held peer message waits above the composer, and releasing it lands the letter and starts a turn', async ({ page }) => {
  await signIn(page)
  const { session, root } = await seedPeerSession(page, 'e2e-peer-hold')

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="peer-held-banner"]')).toHaveCount(0)

  await sendHeldPeerMessage(root)

  const banner = page.locator('[data-testid="peer-held-banner"]')
  await expect(banner).toBeVisible()
  await expect(banner).toContainText(HELD_NAME)
  await expect(banner).toContainText(BODY)
  // The mismatch is the reason it is held, in the reader's own words.
  await expect(banner).toContainText('full-access mode')
  await shot(page, 'peer-held')

  await page.getByTestId('peer-held-release').click()
  await expect(banner).toHaveCount(0)

  // The released message lands in the thread as the letter it always was.
  const letter = page.locator(`[data-testid="peer-message-${HELD_MARK}"]`)
  await expect(letter).toBeVisible()
  await expect(letter).toContainText(BODY)
  // And the release started the turn the message was waiting for: the fake
  // provider's reply is in the thread and the run's own worked line closed it.
  // (The run line above the composer is gone by now — the turn already ended.)
  await expect(page.locator('[data-testid="run-worked-line"]')).toBeVisible()
  await expect(page.locator('[data-testid="run-worked-line"]')).toContainText(/Worked for|已工作/)
  await shot(page, 'peer-released')
})
