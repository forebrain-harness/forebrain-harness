import { existsSync, mkdirSync, readFileSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'

import { revealTab, seedSessionInRoot, shot, signIn } from './support'

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

const BENCH = '[data-testid="chat-workbench"]'
const BENCH_HANDLE = '[data-testid="chat-workbench-resizer"]'

/** The panel's width as drawn, in whole pixels. */
async function benchWidth(page: Page): Promise<number> {
  const box = await page.locator(BENCH).boundingBox()
  return Math.round(box?.width ?? 0)
}

/** Drag the panel's left edge by `dx` px — a negative `dx` widens the panel. */
async function dragEdge(page: Page, selector: string, dx: number): Promise<void> {
  const box = await page.locator(selector).boundingBox()
  if (!box) throw new Error(`${selector} was not laid out`)
  const x = box.x + box.width / 2
  const y = box.y + box.height / 2
  await page.mouse.move(x, y)
  await page.mouse.down()
  await page.mouse.move(x + dx, y, { steps: 8 })
  await page.mouse.up()
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
  await revealTab(page, 'changes')
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

test('the column is resized by its left edge, for this session only', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'en'))
  await signIn(page)
  seedFixture()
  const sid = await seedSessionInRoot(page, projectRoot, 'e2e-bench-drag')
  await page.goto(`/?session=${sid}`, { waitUntil: 'networkidle' })
  await page.click('[data-testid="workbench-toggle"]')
  const bench = page.locator(BENCH)
  // Measure it where it lands, not where the slide-in happens to be.
  await expect(bench).toHaveCSS('transform', 'none')
  const design = await benchWidth(page)
  expect(design).toBe(340)

  // The handle is the panel's left edge: 80px of travel left is 80px of column,
  // taken from the conversation rather than from the window.
  await expect(page.locator(BENCH_HANDLE)).toBeVisible()
  await dragEdge(page, BENCH_HANDLE, -80)
  await expect.poll(async () => Math.abs((await benchWidth(page)) - (design + 80))).toBeLessThanOrEqual(2)
  const widened = await bench.boundingBox()
  const viewport = page.viewportSize()
  expect(Math.abs((widened?.x ?? 0) + (widened?.width ?? 0) - (viewport?.width ?? 0))).toBeLessThanOrEqual(1)
  await shot(page, 'workbench-resized')

  // A double-click on the handle is the way back to the design width.
  await page.locator(BENCH_HANDLE).dblclick()
  await expect.poll(() => benchWidth(page)).toBe(design)

  // Dragged wide and reloaded: the width was this tab's, so a load is the
  // design's 340 again — nothing about it was stored.
  await dragEdge(page, BENCH_HANDLE, -80)
  await expect.poll(async () => Math.abs((await benchWidth(page)) - (design + 80))).toBeLessThanOrEqual(2)
  await page.reload({ waitUntil: 'networkidle' })
  await page.click('[data-testid="workbench-toggle"]')
  await expect(bench).toHaveCSS('transform', 'none')
  expect(await benchWidth(page)).toBe(design)
})
