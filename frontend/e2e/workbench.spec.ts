import { existsSync, mkdirSync, readFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'

import { seedSessionInRoot, shot, signIn } from './support'

/**
 * The workbench's four tabs (plan 017) and its working-directory operations,
 * proven on the real page against a real project directory: create a file
 * (typing its content), rename it, then delete its directory, checking the
 * disk after each step; and the changed-files tab's own card.
 */

const projectRoot = join(process.env.E2E_PROJECT_PARENT ?? '', 'e2e-bench')

function seedFixture() {
  rmSync(projectRoot, { recursive: true, force: true })
  mkdirSync(projectRoot, { recursive: true })
}

async function openTree(page: Page): Promise<void> {
  await page.waitForSelector('[data-testid="workbench-toggle"]')
  await page.click('[data-testid="workbench-toggle"]')
  await expect(page.locator('[data-testid="chat-workbench"]')).toBeVisible()
  await page.click('[data-testid="bench-tab-tree"]')
  await expect(page.locator('[data-testid="tree-new-root"]')).toBeVisible()
}

test.afterEach(() => {
  rmSync(projectRoot, { recursive: true, force: true })
})

test('the workbench opens on overview and switches to the four tabs', async ({ page }) => {
  await signIn(page)
  seedFixture()
  const sid = await seedSessionInRoot(page, projectRoot, 'e2e-bench')
  await page.goto(`/?session=${sid}`, { waitUntil: 'networkidle' })
  await page.click('[data-testid="workbench-toggle"]')
  await expect(page.locator('[data-testid="bench-tab-overview"]')).toHaveAttribute('aria-selected', 'true')
  await page.click('[data-testid="bench-tab-context"]')
  await expect(page.locator('[data-testid="bench-tab-context"]')).toHaveAttribute('aria-selected', 'true')
  await shot(page, 'p017-2-context')
  await page.click('[data-testid="bench-tab-tree"]')
  await expect(page.locator('[data-testid="tree-new-root"]')).toBeVisible()
  await page.click('[data-testid="bench-tab-changes"]')
  await expect(page.locator('[data-testid="bench-tab-changes"]')).toHaveAttribute('aria-selected', 'true')
  await shot(page, 'p017-1-tabs')
})

test('create, edit, rename and delete a file through the tree, checked on disk', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)
  seedFixture()
  const sid = await seedSessionInRoot(page, projectRoot, 'e2e-bench')
  await page.goto(`/?session=${sid}`, { waitUntil: 'networkidle' })
  await openTree(page)

  // New file at the root, with a sub-directory in its name.
  await page.click('[data-testid="tree-new-root"]')
  await page.fill('[data-testid="tree-new-name"]', 'notes/a.txt')
  await page.click('[data-testid="tree-new-create"]')

  // The edit dialog opens on the new file; type its content and save.
  await page.waitForSelector('[data-testid="file-editor"] .cm-content')
  await shot(page, 'p017-4-edit-dialog')
  await page.click('[data-testid="file-editor"] .cm-content')
  await page.keyboard.type('hello-bench-content')
  await page.click('[data-testid="tree-edit-save"]')

  await expect.poll(() => existsSync(join(projectRoot, 'notes', 'a.txt'))).toBe(true)
  expect(readFileSync(join(projectRoot, 'notes', 'a.txt'), 'utf8')).toContain('hello-bench-content')
  await shot(page, 'p017-2-tree-created')

  // Rename the file: open its editor and change the name field.
  await page.click('[data-path="notes"] .workspace-row')
  await expect(page.locator('[data-path="notes/a.txt"]')).toBeVisible()
  await page.click('[data-path="notes/a.txt"] [data-testid="tree-node-edit"]')
  await page.fill('[data-testid="tree-edit-name"]', 'b.txt')
  await page.click('[data-testid="tree-edit-save"]')

  await expect.poll(() => existsSync(join(projectRoot, 'notes', 'b.txt'))).toBe(true)
  expect(existsSync(join(projectRoot, 'notes', 'a.txt'))).toBe(false)
  expect(readFileSync(join(projectRoot, 'notes', 'b.txt'), 'utf8')).toContain('hello-bench-content')

  // Delete the whole directory.
  await page.click('[data-path="notes"] [data-testid="tree-node-delete"]')
  await expect(page.getByText('删除目录 notes？')).toBeVisible()
  await shot(page, 'p017-3-tree-delete-confirm')
  await page.click('[data-testid="confirm-ok"]')
  await expect.poll(() => existsSync(join(projectRoot, 'notes'))).toBe(false)
})

test('the changed-files tab draws its own card for a conversation with no changes', async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)
  seedFixture()
  const sid = await seedSessionInRoot(page, projectRoot, 'e2e-bench-empty')
  await page.goto(`/?session=${sid}`, { waitUntil: 'networkidle' })
  await page.click('[data-testid="workbench-toggle"]')
  await page.click('[data-testid="bench-tab-changes"]')
  await expect(page.locator('[data-testid="chat-workbench"]')).toContainText('变更文件')
  await expect(page.locator('[data-testid="chat-workbench"]')).toContainText('本会话还没有改过文件')
})
