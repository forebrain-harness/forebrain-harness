import { mkdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { readFileSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'

import { signIn } from './support'

/**
 * The workspace files as a lazily loaded directory tree: one level per
 * request, nothing at all until the workbench is first opened.
 */

let workspaceRoot = ''

// Fetched per test from the signed-in page's own request context; the raw
// HTTP response is snake_case (the app's axios layer camelCases it).
async function resolveWorkspaceRoot(page: Page): Promise<string> {
  if (workspaceRoot) return workspaceRoot
  const res = await page.request.get('/api/agents/primary')
  const body = (await res.json()) as {
    active_id?: string
    records?: { id: string; workspace_root: string }[]
  }
  const active = body.records?.find((row) => row.id === body.active_id) ?? body.records?.[0]
  workspaceRoot = active?.workspace_root ?? ''
  return workspaceRoot
}

async function seedFixture(page: Page) {
  await resolveWorkspaceRoot(page)
  const base = join(workspaceRoot, 'e2e-tree')
  rmSync(base, { recursive: true, force: true })
  mkdirSync(join(base, 'docs', 'guide'), { recursive: true })
  mkdirSync(join(base, 'src'), { recursive: true })
  writeFileSync(join(base, 'docs', 'guide', 'intro.md'), 'intro')
  writeFileSync(join(base, 'docs', 'readme.md'), 'readme')
  writeFileSync(join(base, 'src', 'main.go'), 'package main')
  writeFileSync(join(base, 'top.txt'), 'top')
  try {
    symlinkSync(tmpdir(), join(base, 'outside'))
  } catch {
    // Windows without symlink rights: the hiding case degrades to absence.
  }
}

test.afterEach(() => {
  rmSync(join(workspaceRoot, 'e2e-tree'), { recursive: true, force: true })
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
}

function rowByName(page: Page, name: string) {
  return page.locator(`[role="treeitem"] button:has-text("${name}")`).first()
}

test('no tree request until the workbench opens', async ({ page }) => {
  const requests = treeRequests(page)
  await signIn(page)
  await seedFixture(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  await page.waitForTimeout(500)
  expect(requests).toEqual([])
})

test('first open lists one level', async ({ page }) => {
  const requests = treeRequests(page)
  await signIn(page)
  await seedFixture(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  await openWorkbench(page)
  await expect(rowByName(page, 'e2e-tree')).toBeVisible()
  const treeRequestsForRoot = requests.filter((url) => !url.includes('path='))
  expect(treeRequestsForRoot).toHaveLength(1)
  const body = await page.locator('[data-testid="chat-workbench"]').innerText()
  expect(body).not.toContain('.git')
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/workspace-tree.png`, fullPage: true })
})

test('expanding loads once and lazily', async ({ page }) => {
  const requests = treeRequests(page)
  await signIn(page)
  await seedFixture(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  await openWorkbench(page)
  await rowByName(page, 'e2e-tree').click()
  await expect(rowByName(page, 'docs')).toBeVisible()
  // Directories first, one level only.
  const bench = await page.locator('[data-testid="chat-workbench"]').innerText()
  expect(bench).toContain('src')
  expect(bench).toContain('top.txt')
  expect(bench).not.toContain('outside')
  expect(bench).not.toContain('intro.md')
  expect(requests.filter((url) => url.includes('path=e2e-tree'))).toHaveLength(1)

  // Collapse and expand again: no new request.
  await rowByName(page, 'e2e-tree').click()
  await rowByName(page, 'e2e-tree').click()
  await expect(rowByName(page, 'docs')).toBeVisible()
  expect(requests.filter((url) => url.includes('path=e2e-tree'))).toHaveLength(1)

  await rowByName(page, 'docs').click()
  await expect(rowByName(page, 'readme.md')).toBeVisible()
  await rowByName(page, 'guide').click()
  await expect(rowByName(page, 'intro.md')).toBeVisible()
})

test('file click inserts a reference', async ({ page }) => {
  await signIn(page)
  await seedFixture(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  await openWorkbench(page)
  await rowByName(page, 'e2e-tree').click()
  await rowByName(page, 'top.txt').click()
  await expect(page.locator('textarea')).toHaveValue(/e2e-tree\/top\.txt/)
})

test('keyboard opens and closes directories', async ({ page }) => {
  await signIn(page)
  await seedFixture(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  await openWorkbench(page)
  await rowByName(page, 'e2e-tree').click()
  const docs = rowByName(page, 'docs')
  await docs.waitFor({ state: 'visible' })
  await docs.focus()
  await docs.press('ArrowLeft')
  await expect(page.locator('[role="treeitem"][data-path="e2e-tree/docs"]')).toHaveAttribute('aria-expanded', 'false')
  await docs.press('ArrowRight')
  await expect(page.locator('[role="treeitem"][data-path="e2e-tree/docs"]')).toHaveAttribute('aria-expanded', 'true')
})

test('state survives closing the workbench', async ({ page }) => {
  const requests = treeRequests(page)
  await signIn(page)
  await seedFixture(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  await openWorkbench(page)
  await rowByName(page, 'e2e-tree').click()
  await rowByName(page, 'docs').click()
  await expect(rowByName(page, 'readme.md')).toBeVisible()
  await page.locator('[data-testid="chat-workbench"] .chat-workbench-close').click()
  const during = requests.length
  await openWorkbench(page)
  await expect(rowByName(page, 'readme.md')).toBeVisible()
  expect(requests.length).toBe(during)
})
