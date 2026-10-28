import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'

import { signIn, shot } from './support'

/**
 * The folder's own project type, on the real page (plan 153): the new-project
 * dialog preselects what the gateway would give the folder, says why in the
 * gateway's own words, and never overrides a person's pick — while a
 * conversation started straight on a folder shows the type the gateway itself
 * decided for it, because the web never repeats the `.git` judgement.
 *
 * The four folders a person meets: a Git working tree's root, a subdirectory of
 * one, an empty repository (`git init`, no commit) and a plain directory. The
 * first three are `coding`, the last `office`; the spec asks the gateway for
 * the answer and holds the dialog to it, so the page can never drift from
 * `state.DefaultProjectTypeForRoot` without this failing.
 */

/** The harness's scratch project: a Git working tree's root (web_e2e.sh inits
 *  it), so everything below it is a subdirectory of that worktree. */
const parent = process.env.E2E_PROJECT_PARENT ?? ''

interface FolderCase {
  /** What it is, for the assertion messages. */
  what: string
  root: string
  /** The type the gateway must answer for this root. */
  projectType: 'coding' | 'office'
  /** The dialog's one-line reason for that type. */
  reason: string
}

const GIT_REASON = 'Git 工作树，默认创意编程'
const PLAIN_REASON = '非 Git 目录，默认办公创作'

/** The four folders, made once per run under the OS temp directory (outside
 *  the gateway's home, which no project root may live in). */
let made: FolderCase[] | null = null
const scratch: string[] = []

function folders(): FolderCase[] {
  if (made) return made
  if (!parent) throw new Error('E2E_PROJECT_PARENT must point at the scratch project')
  const base = mkdtempSync(join(tmpdir(), 'fb-e2e-153-types-'))
  scratch.push(base)
  const sub = join(parent, 'e2e-seed')
  mkdirSync(sub, { recursive: true })
  const emptyRepo = join(base, 'empty-repo')
  const plain = join(base, 'plain-folder')
  mkdirSync(emptyRepo)
  mkdirSync(plain)
  execFileSync('git', ['init', '-q', emptyRepo])
  made = [
    { what: 'Git working tree root', root: parent, projectType: 'coding', reason: GIT_REASON },
    { what: 'Git subdirectory', root: sub, projectType: 'coding', reason: GIT_REASON },
    { what: 'empty repository', root: emptyRepo, projectType: 'coding', reason: GIT_REASON },
    { what: 'plain directory', root: plain, projectType: 'office', reason: PLAIN_REASON },
  ]
  return made
}

function freshFolder(name: string): string {
  const base = mkdtempSync(join(tmpdir(), `fb-e2e-153-${name}-`))
  scratch.push(base)
  return base
}

test.afterAll(() => {
  for (const dir of scratch) rmSync(dir, { recursive: true, force: true })
})

/** What the gateway says a folder would get: the one answer the dialog may
 *  show, and the reason it must give for it. The wire is snake_case — the
 *  page's axios instance is what camel-cases it, and this call is not the
 *  page's. */
async function gatewayDefault(page: Page, root: string): Promise<{ projectType: string; source: string }> {
  const res = await page.request.get(`/api/v1/projects/default-type?root=${encodeURIComponent(root)}`)
  expect(res.ok(), await res.text()).toBeTruthy()
  const answer = (await res.json()) as { project_type?: string; source?: string }
  return { projectType: String(answer.project_type ?? ''), source: String(answer.source ?? '') }
}

/** Open the new-project dialog, with nothing carried over from a last time. */
async function openDialog(page: Page): Promise<void> {
  await page.goto('/projects', { waitUntil: 'networkidle' })
  await page.click('[data-testid="projects-new"]')
  await expect(page.locator('[data-testid="project-root"]')).toBeVisible()
}

test('the dialog preselects the folder\'s type, and says why in the gateway\'s words', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  for (const folder of folders()) {
    const answer = await gatewayDefault(page, folder.root)
    expect(answer.projectType, `${folder.what} answered by the gateway`).toBe(folder.projectType)

    await openDialog(page)
    await page.fill('[data-testid="project-root"]', folder.root)

    // The dialog's own read of the same question — never a `.git` check of its
    // own — lands as the selection and as the note under the cards.
    const hint = page.locator('[data-testid="project-type-hint"]')
    await expect(hint).toContainText(folder.reason)
    await expect(page.locator(`[data-testid="project-type-${folder.projectType}"]`)).toHaveAttribute('aria-checked', 'true')
    // The suggestion is marked, never imposed: the badge sits on the card the
    // gateway named while the person is still free to pick another.
    await expect(page.locator(`[data-testid="project-type-${folder.projectType}"]`)).toContainText('建议')
    // The reason is a suggestion, not a fact about what was chosen.
    await expect(hint).not.toContainText('已手动改为')
  }
  await shot(page, 'p153-project-type-preselect')
})

test('a manual pick outlives the folder and the answers that arrive after it', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const plain = folders().find((f) => f.projectType === 'office')!
  const git = folders().find((f) => f.what === 'Git working tree root')!

  await openDialog(page)
  await page.fill('[data-testid="project-root"]', plain.root)
  await expect(page.locator('[data-testid="project-type-hint"]')).toContainText(plain.reason)
  await expect(page.locator('[data-testid="project-type-office"]')).toHaveAttribute('aria-checked', 'true')

  // The person picks a kind the gateway did not suggest. Choosing the batch
  // kind is also the only one that asks for trust, because it runs unattended.
  await page.click('[data-testid="project-type-batch"]')
  const hint = page.locator('[data-testid="project-type-hint"]')
  await expect(hint).toContainText('已手动改为「后台跑批」')
  await expect(hint).toContainText('目录建议：办公创作（非 Git 目录）')
  await expect(page.getByText('信任此目录')).toBeVisible()

  // A new folder arrives with its own suggestion (this one is inside a Git
  // worktree) — and the pick stays where the person put it.
  await page.fill('[data-testid="project-root"]', git.root)
  await expect(page.locator('[data-testid="project-type-coding"]')).toContainText('建议')
  await expect(hint).toContainText('已手动改为「后台跑批」')
  await expect(hint).toContainText('目录建议：创意编程（Git 工作树）')
  await expect(page.locator('[data-testid="project-type-batch"]')).toHaveAttribute('aria-checked', 'true')
  await expect(page.locator('[data-testid="project-type-coding"]')).toHaveAttribute('aria-checked', 'false')
  await shot(page, 'p153-project-type-override')
})

test('a created project keeps the type that was picked, not the one suggested', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const root = freshFolder('create')
  const name = `e2e-153-picked-${Date.now().toString(36)}`
  const answer = await gatewayDefault(page, root)
  expect(answer.projectType).toBe('office')

  await openDialog(page)
  await page.fill('[data-testid="project-root"]', root)
  await expect(page.locator('[data-testid="project-type-hint"]')).toContainText(PLAIN_REASON)
  // Against the suggestion: coding, on a folder the gateway would call office.
  await page.click('[data-testid="project-type-coding"]')
  await page.fill('[data-testid="project-name"]', name)
  const createRequest = page.waitForRequest((req) => req.url().endsWith('/api/v1/projects') && req.method() === 'POST')
  await page.click('[data-testid="project-create"]')
  const body = (await (await createRequest).postDataJSON()) as { project_type?: string }
  expect(body.project_type).toBe('coding')

  // The project page it opens carries the type that was actually created.
  await page.waitForURL(/\/projects\/[^/?]+/, { timeout: 15_000 })
  const segments = new URL(page.url()).pathname.split('/').filter(Boolean)
  const createdId = decodeURIComponent(segments[segments.indexOf('projects') + 1] ?? '')

  await page.goto('/projects', { waitUntil: 'networkidle' })
  const row = page.locator('tr', { hasText: name })
  await expect(row).toContainText('创意编程')
  await expect(page.locator(`[data-testid="project-type-badge-${createdId}"]`)).toContainText('创意编程')
  // …and the gateway holds the same, which is what every later surface reads.
  const stored = await page.request.get(`/api/v1/projects/${encodeURIComponent(createdId)}`)
  expect(stored.ok(), await stored.text()).toBeTruthy()
  expect(((await stored.json()) as { project_type?: string }).project_type).toBe('coding')
  await shot(page, 'p153-project-type-created')
})

test('a conversation started on a folder shows the type the gateway decided', async ({ page }) => {
  test.info().setTimeout(180_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  // A project to start the picker from: its sidebar lists the agent's
  // projects, which is how a folder below it is reached.
  const anchorName = `e2e-153-anchor-${Date.now().toString(36)}`
  const anchorRoot = join(parent, 'e2e-seed', anchorName)
  mkdirSync(anchorRoot, { recursive: true })
  const anchor = await page.request.post('/api/v1/projects', { data: { name: anchorName, root: anchorRoot } })
  expect(anchor.ok(), await anchor.text()).toBeTruthy()

  await page.goto('/', { waitUntil: 'networkidle' })
  await page.click('[data-testid="project-pill"]')
  await page.click('.pj-opt:has-text("打开文件夹")')
  await expect(page.locator('.modal.fb')).toBeVisible()
  await page.click(`.fb-side button:has-text("${anchorName}")`)

  // A folder that is not a project yet: made here, inside the anchor project's
  // own directory, so the gateway's shared creation boundary decides its type.
  await page.click('.modal-f .btn.sm')
  const folderName = `chat-${Date.now().toString(36)}`
  await page.fill('.sheet input', folderName)
  await page.click('.sheet .btn.pri')
  await expect(page.locator('.sel-path')).toContainText(folderName)
  await page.click('.modal-f .btn.pri')
  await expect(page.locator('[data-testid="project-pill"]')).toContainText(folderName)

  // The send carries the folder alone — no type, no project — and the reply's
  // project is the gateway's answer, which is what the projects table shows.
  const createSession = page.waitForResponse((res) => res.url().includes('/api/chat/sessions') && res.request().method() === 'POST')
  await page.fill('textarea', '在新建的文件夹里开一段对话')
  await page.press('textarea', 'Enter')
  // A directory nobody has judged is trusted once before anything runs in it.
  await expect(page.locator('.trust-path')).toBeVisible()
  await page.click('.modal.sm button.btn.pri')
  const response = await createSession
  expect(response.ok(), await response.text()).toBeTruthy()
  const sent = response.request().postDataJSON() as Record<string, unknown>
  expect(sent.folder, 'the send names the folder, nothing else').toBeTruthy()
  expect(sent.project_type, 'the web never sends a type for a folder').toBeUndefined()

  const created = (await response.json()) as { project?: { id?: string; project_type?: string } }
  const finalType = String(created.project?.project_type ?? '')
  expect(finalType, 'the gateway decides the folder conversation\'s type').toBe('coding')

  await page.goto('/projects', { waitUntil: 'networkidle' })
  const badge = page.locator(`[data-testid="project-type-badge-${String(created.project?.id ?? '')}"]`)
  await expect(badge).toContainText('创意编程')
  await shot(page, 'p153-project-type-folder-chat')
})
