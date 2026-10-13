import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

import type { Page, Response } from '@playwright/test'

/** Save a full-page screenshot into the run's shots directory. */
export async function shot(page: Page, name: string): Promise<void> {
  const dir = process.env.E2E_SHOTS
  if (!dir) throw new Error('E2E_SHOTS must point at the screenshots directory')
  await page.screenshot({ path: `${dir}/${name}.png`, fullPage: true })
}

let seedSeq = 0

function uniqueSuffix(): string {
  return `${Date.now().toString(36)}-${(++seedSeq).toString(36)}-${Math.random().toString(36).slice(2, 7)}`
}

/**
 * Find or create a project bound to root and open a conversation in it,
 * returning the session id. Used where a test needs the conversation's
 * directory to be a specific one — the gateway's launch directory, say.
 */
export async function seedSessionInRoot(page: Page, root: string, name = 'e2e-root'): Promise<string> {
  const res = await page.request.post('/api/v1/projects', { data: { name, root, trust: true } })
  let projectId = ''
  if (res.status() === 409) {
    const body = (await res.json()) as { project?: { id?: string } }
    projectId = String(body.project?.id ?? '')
  } else if (res.ok()) {
    projectId = String(((await res.json()) as { id: string }).id)
  } else {
    throw new Error(`seed project at root failed: ${res.status()} ${await res.text()}`)
  }
  const session = await page.request.post('/api/chat/sessions', { data: { project_id: projectId } })
  if (!session.ok()) throw new Error(`seed session failed: ${session.status()} ${await session.text()}`)
  return String(((await session.json()) as { id: string }).id)
}

/**
 * Create a project bound to a fresh scratch directory and open a conversation
 * in it, returning the session id. Since plan 029 every conversation belongs
 * to a project, so a test that just needs somewhere to send calls this and
 * navigates to `/?session=<id>`. The scratch tree under E2E_PROJECT_PARENT is
 * wiped when the acceptance run starts.
 */
export async function seedProjectSession(page: Page, name = 'e2e-seed'): Promise<string> {
  const parent = process.env.E2E_PROJECT_PARENT
  if (!parent) throw new Error('E2E_PROJECT_PARENT must point outside the forebrain home')
  const root = join(parent, 'e2e-seed', `${name}-${uniqueSuffix()}`)
  mkdirSync(root, { recursive: true })
  return seedSessionInRoot(page, root, name)
}

/** Sign the page in with E2E_TOKEN through the one-click sign-in link. */
export async function signIn(page: Page): Promise<void> {
  const token = process.env.E2E_TOKEN
  if (!token) throw new Error('E2E_TOKEN must hold the gateway token')
  // The link form is deterministic: no form fill to race a first-paint
  // re-render, and the hash is scrubbed by the page itself.
  await page.goto(`/login#token=${encodeURIComponent(token)}`)
  try {
    await page.waitForURL((url) => !url.pathname.endsWith('/login'), { timeout: 15_000 })
  } catch {
    // A cold gateway's first asset burst can break one modulepreload; the
    // session cookie is already set, so a plain reload lands in the app.
    await page.reload({ waitUntil: 'load' })
    await page.waitForURL((url) => !url.pathname.endsWith('/login'), { timeout: 15_000 })
  }
  await page.waitForLoadState('networkidle')
}

/**
 * Collect every 401 on /api/ the page sees. The session probe's own 401 is
 * the sign-in check working, not a leak, so it is filtered out.
 */
export function watch401(page: Page): () => Response[] {
  const seen: Response[] = []
  page.on('response', (response) => {
    if (response.status() === 401 && response.url().includes('/api/')) seen.push(response)
  })
  return () => seen.filter((response) => !response.url().includes('/api/auth/session'))
}

/**
 * Wait for the assistant's answer. With the fake provider the reply is an
 * exact known string; with a real model (E2E_REAL_LLM=1) the content is not
 * predictable, so any non-empty assistant message passes.
 */
export async function expectAssistantReply(page: Page): Promise<void> {
  if (process.env.E2E_REAL_LLM === '1') {
    // A real reply's words are unknown; what is known is how a turn ends.
    // The streaming placeholder ("...") is not a reply, and every run closes
    // with its worked line — so wait for words and for that line.
    await page.waitForFunction(
      () => {
        const replied = Array.from(document.querySelectorAll('.is-assistant'))
          .some((node) => (node.textContent ?? '').replace(/[.…\s]/g, '').length > 0)
        const closed = /已工作 \d|Worked for \d/.test(document.querySelector('.chat-shell main')?.textContent ?? '')
        return replied && closed
      },
      undefined,
      { timeout: 150_000 },
    )
    return
  }
  const reply = process.env.E2E_REPLY_OK ?? 'E2E_REPLY_OK'
  await page.waitForFunction(
    (text) => document.body.innerText.includes(text),
    reply,
    { timeout: 60_000 },
  )
}
