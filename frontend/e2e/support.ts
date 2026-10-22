import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

import { expect, type Page, type Response } from '@playwright/test'

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

/**
 * Seed n conversations the drawer's list carries: one project, n sessions in
 * it, each titled "listed <i>" and holding one message. A message is what
 * lists a conversation — the create endpoint alone leaves a row the lists
 * hide — and the composer turn is the one public way to add one, so this
 * walks n quick turns against the fake provider (n real turns under
 * E2E_REAL_LLM=1; keep n small there).
 */
export async function seedListedConversations(page: Page, n: number, name = 'e2e-seed-list'): Promise<void> {
  const parent = process.env.E2E_PROJECT_PARENT
  if (!parent) throw new Error('E2E_PROJECT_PARENT must point outside the forebrain home')
  const root = join(parent, 'e2e-seed', `${name}-${uniqueSuffix()}`)
  mkdirSync(root, { recursive: true })
  const res = await page.request.post('/api/v1/projects', { data: { name, root, trust: true } })
  if (!res.ok()) throw new Error(`seed project at ${root} failed: ${res.status()} ${await res.text()}`)
  const projectId = String(((await res.json()) as { id: string }).id)
  for (let i = 0; i < n; i++) {
    const session = await page.request.post('/api/chat/sessions', {
      data: { project_id: projectId, title: `listed ${i}` },
    })
    if (!session.ok()) throw new Error(`seed session ${i} failed: ${session.status()} ${await session.text()}`)
    const id = String(((await session.json()) as { id: string }).id)
    await page.goto(`/?session=${id}`, { waitUntil: 'networkidle' })
    await page.waitForSelector('textarea')
    await page.fill('textarea', `listed-e2e ${i}`)
    await page.press('textarea', 'Enter')
    await expectAssistantReply(page)
  }
}

/**
 * Register (or reuse) a project bound to a fresh scratch directory and return
 * its id. Scheduled tasks and channels belong to a project, so a spec that
 * drives their project tab calls this before navigating to it.
 */
export async function seedProject(page: Page, name = 'e2e-proj'): Promise<string> {
  const parent = process.env.E2E_PROJECT_PARENT
  if (!parent) throw new Error('E2E_PROJECT_PARENT must point outside the forebrain home')
  const root = join(parent, 'e2e-seed', `${name}-${uniqueSuffix()}`)
  mkdirSync(root, { recursive: true })
  const res = await page.request.post('/api/v1/projects', { data: { name, root, trust: true } })
  if (res.status() === 409) {
    const body = (await res.json()) as { project?: { id?: string } }
    return String(body.project?.id ?? '')
  }
  if (!res.ok()) throw new Error(`seed project at ${root} failed: ${res.status()} ${await res.text()}`)
  return String(((await res.json()) as { id: string }).id)
}

/**
 * A shadcn-vue select (reka-ui): click the trigger, click the item. There is no
 * native `<select>` for Playwright's selectOption to drive, and the item opens
 * on the trigger's own click, so a person's two clicks are the whole protocol.
 */
export async function pickOption(page: Page, testid: string, label: string): Promise<void> {
  await page.click(`[data-testid="${testid}"]`)
  await page.getByRole('option', { name: label, exact: true }).click()
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
 * Bring a workbench tab into the window before asserting or clicking it: the bar
 * shows as many tabs as the column has room for — five of the workshop's eight
 * in Chinese, three of them in English — and the ones past the window are behind
 * the arrows. The window only moves forward here, so a case walks the tabs in
 * the order they appear.
 */
export async function revealTab(page: Page, key: string): Promise<void> {
  const tab = page.locator(`[data-testid="bench-tab-${key}"]`)
  const next = page.locator('[data-testid="bench-tabs-next"]')
  // The toggle has only been clicked, so wait for the bar it draws, which is
  // where the window and its arrows live.
  await expect(page.locator('.bench-tab-strip [role="tab"]').first()).toBeVisible()
  for (let step = 0; step < 8 && !(await tab.isVisible()); step += 1) {
    if (!(await next.isEnabled())) break
    await next.click()
  }
  await expect(tab).toBeVisible()
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
