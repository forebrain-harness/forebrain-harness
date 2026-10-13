import { mkdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'

import { seedSessionInRoot, signIn } from './support'

/**
 * The conversation's files as a lazily loaded directory tree: one level per
 * request, nothing at all until the workbench is first opened. The root is the
 * project the session belongs to — never the agent's own workspace.
 */

// The conversation's project root, outside the gateway's home (a project is
// never inside FOREBRAIN_HOME). The same scratch parent is wiped when the
// acceptance run starts.
const projectRoot = join(process.env.E2E_PROJECT_PARENT ?? '', 'e2e-tree')

function seedFixture() {
  rmSync(projectRoot, { recursive: true, force: true })
  mkdirSync(join(projectRoot, 'docs', 'guide'), { recursive: true })
  mkdirSync(join(projectRoot, 'src'), { recursive: true })
  writeFileSync(join(projectRoot, 'docs', 'guide', 'intro.md'), 'intro')
  writeFileSync(join(projectRoot, 'docs', 'readme.md'), 'readme')
  writeFileSync(join(projectRoot, 'src', 'main.go'), 'package main')
  writeFileSync(join(projectRoot, 'top.txt'), 'top')
  try {
    symlinkSync(tmpdir(), join(projectRoot, 'outside'))
  } catch {
    // Windows without symlink rights: the hiding case degrades to absence.
  }
}

/**
 * Sign in, seed the conversation's project directory, and open a session in
 * it. Returns the session id the file view names.
 */
async function openConversation(page: Page): Promise<string> {
  await signIn(page)
  seedFixture()
  const sid = await seedSessionInRoot(page, projectRoot, 'e2e-tree')
  await page.goto(`/?session=${sid}`, { waitUntil: 'networkidle' })
  return sid
}

test.afterEach(() => {
  rmSync(projectRoot, { recursive: true, force: true })
})

function treeRequests(page: Page): string[] {
  const paths: string[] = []
  page.on('request', (req) => {
    if (req.url().includes('/api/workspace/tree')) paths.push(req.url())
  })
  return paths
}

async function openWorkbench(page: Page) {
  await page.waitForSelector('[data-testid="workbench-toggle"]')
  await page.click('[data-testid="workbench-toggle"]')
  await expect(page.locator('[data-testid="chat-workbench"]')).toBeVisible()
  // The working directory is now its own tab (plan 017); the tree lives there.
  await page.click('[data-testid="bench-tab-tree"]')
}

function rowByName(page: Page, name: string) {
  return page.locator(`[role="treeitem"] button:has-text("${name}")`).first()
}

test('no tree request until the workbench opens', async ({ page }) => {
  const requests = treeRequests(page)
  await openConversation(page)
  await page.waitForTimeout(500)
  expect(requests).toEqual([])
})

test('first open lists one level of the conversation project', async ({ page }) => {
  const requests = treeRequests(page)
  const sid = await openConversation(page)
  await openWorkbench(page)
  await expect(rowByName(page, 'docs')).toBeVisible()
  await expect(rowByName(page, 'top.txt')).toBeVisible()
  const treeRequestsForRoot = requests.filter((url) => !url.includes('path='))
  expect(treeRequestsForRoot).toHaveLength(1)
  // The request names the conversation, so the server resolves its project.
  expect(treeRequestsForRoot[0]).toContain(`session_id=${sid}`)
  const body = await page.locator('[data-testid="chat-workbench"]').innerText()
  expect(body).not.toContain('.git')
  // The agent's own workspace (SOUL.md, memories, skills) is not this view.
  expect(body).not.toContain('SOUL.md')
  expect(body).not.toContain('memories')
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/workspace-tree.png`, fullPage: true })
})

test('expanding loads once and lazily', async ({ page }) => {
  const requests = treeRequests(page)
  await openConversation(page)
  await openWorkbench(page)
  await expect(rowByName(page, 'docs')).toBeVisible()
  await rowByName(page, 'docs').click()
  await expect(rowByName(page, 'readme.md')).toBeVisible()
  // Directories first, one level only.
  const bench = await page.locator('[data-testid="chat-workbench"]').innerText()
  expect(bench).toContain('src')
  expect(bench).toContain('top.txt')
  expect(bench).not.toContain('outside')
  expect(bench).not.toContain('intro.md')
  expect(requests.filter((url) => url.includes('path=docs'))).toHaveLength(1)

  // Collapse and expand again: no new request.
  await rowByName(page, 'docs').click()
  await rowByName(page, 'docs').click()
  await expect(rowByName(page, 'readme.md')).toBeVisible()
  expect(requests.filter((url) => url.includes('path=docs'))).toHaveLength(1)

  await rowByName(page, 'docs').click()
  await expect(rowByName(page, 'guide')).toBeVisible()
  await rowByName(page, 'guide').click()
  await expect(rowByName(page, 'intro.md')).toBeVisible()
})

test('file click inserts a reference', async ({ page }) => {
  await openConversation(page)
  await openWorkbench(page)
  await rowByName(page, 'top.txt').click()
  await expect(page.locator('textarea')).toHaveValue(/top\.txt/)
})

test('keyboard opens and closes directories', async ({ page }) => {
  await openConversation(page)
  await openWorkbench(page)
  const docs = rowByName(page, 'docs')
  await docs.waitFor({ state: 'visible' })
  await docs.focus()
  await docs.press('ArrowLeft')
  await expect(page.locator('[role="treeitem"][data-path="docs"]')).toHaveAttribute('aria-expanded', 'false')
  await docs.press('ArrowRight')
  await expect(page.locator('[role="treeitem"][data-path="docs"]')).toHaveAttribute('aria-expanded', 'true')
})

test('state survives closing the workbench', async ({ page }) => {
  const requests = treeRequests(page)
  await openConversation(page)
  await openWorkbench(page)
  await rowByName(page, 'docs').click()
  await expect(rowByName(page, 'readme.md')).toBeVisible()
  await page.locator('[data-testid="chat-workbench"] .chat-workbench-close').click()
  const during = requests.length
  await openWorkbench(page)
  await expect(rowByName(page, 'readme.md')).toBeVisible()
  expect(requests.length).toBe(during)
})

// Plan 022: the file view belongs to the conversation, not the process. Its
// tree, its @ references and a picked image all resolve against the session's
// project root, and a traversal out of it is still refused.
test('the file view is the session project, and refuses escapes', async ({ page }) => {
  const sid = await openConversation(page)
  await openWorkbench(page)

  // The tree lists the conversation's project root: a file's @ reference is
  // project-relative, so it names a path inside that directory.
  await expect(rowByName(page, 'top.txt')).toBeVisible()
  await rowByName(page, 'top.txt').click()
  await expect(page.locator('textarea')).toHaveValue(/top\.txt\s*$/)

  // A traversal out of the project root is refused, exactly as before.
  const escape = await page.request.get(`/api/workspace/tree?session_id=${sid}&path=../`)
  expect(escape.status()).toBe(400)
  const snippet = await page.request.get(`/api/workspace/snippet?session_id=${sid}&path=../README.md`)
  expect(snippet.status()).toBe(400)
})
