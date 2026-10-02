import { expect, test } from '@playwright/test'
import { mkdirSync, utimesSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

import { shot, signIn } from './support'

/**
 * The memory file manager on both surfaces: the agent page (global scope) and
 * the project tab (project scope). Core files are editable but never
 * deletable; clearing goes through the one reset endpoint, scoped.
 */

const home = process.env.E2E_HOME ?? ''
const globalRoot = home ? join(home, 'workspace', 'memories', 'global') : ''

function seedGlobalFile(name: string, content: string, ageSeconds?: number): void {
  const path = join(globalRoot, name)
  mkdirSync(path.substring(0, path.lastIndexOf('/')), { recursive: true })
  writeFileSync(path, content)
  if (ageSeconds !== undefined) {
    // Sorting needs real time gaps: a burst of writes shares one mtime and
    // every ordering would collapse onto the path tie-break.
    const when = new Date(Date.now() - ageSeconds * 1000)
    utimesSync(path, when, when)
  }
}

test('memory page lists files with the core lock', async ({ page }) => {
  test.skip(!globalRoot, 'E2E_HOME missing')
  seedGlobalFile('MEMORY.md', '# e2e memory\n\n- remembers the layout\n')
  seedGlobalFile('notes/habit.md', 'prefers short answers\n')

  await signIn(page)
  await page.goto('/memories', { waitUntil: 'networkidle' })
  await expect(page.locator('[data-memory-file="MEMORY.md"]')).toBeVisible({ timeout: 10_000 })
  await expect(page.locator('[data-memory-file="MEMORY.md"] svg')).toBeVisible()
  await expect(page.locator('[data-testid="memories-delete-MEMORY.md"]')).toHaveCount(0)
  await expect(page.locator('[data-testid="memories-delete-notes/habit.md"]')).toBeVisible()
  await shot(page, 'memories-list')
})

test('sorting and paging across a seeded store', async ({ page }) => {
  test.skip(!globalRoot, 'E2E_HOME missing')
  for (let i = 0; i < 25; i++) {
    seedGlobalFile(`notes/bulk-${String(i).padStart(2, '0')}.md`, `bulk memory ${i}\n`, 600 - i)
  }
  // Written last with the current mtime: the newest file under updated-desc.
  seedGlobalFile('notes/z-latest.md', 'the freshest file\n')

  await signIn(page)
  await page.goto('/memories', { waitUntil: 'networkidle' })
  const firstPageLead = await page.locator('[data-memory-file]').first().getAttribute('data-memory-file')
  expect(firstPageLead).toBeTruthy()
  await expect(page.locator('[data-memory-file]')).toHaveCount(20, { timeout: 10_000 })

  // The second page is a different slice, not the first one again.
  await page.click('[data-testid="memories-next-page"]')
  await expect(page.locator('[data-testid="memories-page"]')).toHaveText(/^2 /, { timeout: 10_000 })
  await expect(page.locator('[data-memory-file]').first()).not.toHaveAttribute('data-memory-file', firstPageLead ?? '')

  // Default order is updated-desc (the freshest seed leads). Under
  // created-asc the bulk-00 file — explicitly stamped the oldest — leads.
  await page.click('[data-testid="memories-prev-page"]')
  await expect(page.locator('[data-memory-file]').first()).toHaveAttribute('data-memory-file', 'notes/z-latest.md', { timeout: 10_000 })
  await page.selectOption('[data-testid="memories-sort"]', 'created-asc')
  await expect(page.locator('[data-memory-file]').first()).toHaveAttribute('data-memory-file', 'notes/bulk-00.md', { timeout: 10_000 })
})

test('search matches content and file names', async ({ page }) => {
  test.skip(!globalRoot, 'E2E_HOME missing')
  seedGlobalFile('notes/needle-body.md', 'this file holds the haystack-word-plainly\n')
  seedGlobalFile('needle-name.md', 'nothing else here\n')

  await signIn(page)
  await page.goto('/memories', { waitUntil: 'networkidle' })
  await page.fill('[data-testid="memories-search"]', 'haystack-word-plainly')
  await expect(page.locator('[data-memory-file]')).toHaveCount(1, { timeout: 10_000 })
  await expect(page.locator('[data-memory-file="notes/needle-body.md"]')).toBeVisible()

  await page.fill('[data-testid="memories-search"]', 'needle-name')
  await expect(page.locator('[data-memory-file]')).toHaveCount(1, { timeout: 10_000 })
  await expect(page.locator('[data-memory-file="needle-name.md"]')).toBeVisible()
})

test('edit a core file and a regular file', async ({ page }) => {
  test.skip(!globalRoot, 'E2E_HOME missing')
  seedGlobalFile('MEMORY.md', '# before edit\n')
  seedGlobalFile('notes/editable.md', 'first version\n')

  await signIn(page)
  await page.goto('/memories', { waitUntil: 'networkidle' })
  await page.click('[data-testid="memories-edit-MEMORY.md"]')
  await expect(page.locator('[data-testid="memories-editor"]')).toBeVisible()
  await page.fill('[data-testid="memories-editor-text"]', '# after edit\n')
  await page.click('[data-testid="memories-editor-save"]')
  await expect(page.locator('[data-testid="memories-editor"]')).toHaveCount(0, { timeout: 10_000 })

  // Reopen: the saved bytes are what the editor loads.
  await page.click('[data-testid="memories-edit-MEMORY.md"]')
  await expect(page.locator('[data-testid="memories-editor-text"]')).toHaveValue('# after edit\n')
  await page.getByRole('button', { name: /取消|Cancel/ }).click()

  await page.click('[data-testid="memories-edit-notes/editable.md"]')
  await page.fill('[data-testid="memories-editor-text"]', 'second version\n')
  await page.click('[data-testid="memories-editor-save"]')
  await expect(page.locator('[data-testid="memories-editor"]')).toHaveCount(0, { timeout: 10_000 })
})

test('delete refuses core files and removes the rest', async ({ page }) => {
  test.skip(!globalRoot, 'E2E_HOME missing')
  seedGlobalFile('MEMORY.md', '# keep\n')
  seedGlobalFile('notes/one.md', 'one\n')
  seedGlobalFile('notes/two.md', 'two\n')

  await signIn(page)
  await page.goto('/memories', { waitUntil: 'networkidle' })
  // Batch: MEMORY.md is refused, the others go.
  await page.locator('[data-memory-file="MEMORY.md"] input[type=checkbox]').check()
  await page.locator('[data-memory-file="notes/one.md"] input[type=checkbox]').check()
  await page.locator('[data-memory-file="notes/two.md"] input[type=checkbox]').check()
  await page.click('[data-testid="memories-delete-selected"]')
  await expect(page.locator('[data-memory-file="MEMORY.md"]')).toBeVisible({ timeout: 10_000 })
  await expect(page.locator('[data-memory-file="notes/one.md"]')).toHaveCount(0, { timeout: 10_000 })
  await expect(page.locator('[data-memory-file="notes/two.md"]')).toHaveCount(0, { timeout: 10_000 })

  // Single delete of a regular file created through the API's own write path.
  await page.request.put('/api/memories/file?scope=global&path=notes/e2e-temp.md', { data: { content: 'temp' } })
  await page.reload({ waitUntil: 'networkidle' })
  await page.click('[data-testid="memories-delete-notes/e2e-temp.md"]')
  await expect(page.locator('[data-memory-file="notes/e2e-temp.md"]')).toHaveCount(0, { timeout: 10_000 })
})

test('clearing the project scope leaves global memory alone', async ({ page }) => {
  test.skip(!globalRoot, 'E2E_HOME missing')
  seedGlobalFile('MEMORY.md', '# global survivor\n')

  await signIn(page)
  const parent = process.env.E2E_PROJECT_PARENT ?? ''
  test.skip(!parent, 'E2E_PROJECT_PARENT missing')
  const root = `${parent}/memory-e2e-proj`
  mkdirSync(`${root}/.forebrain`, { recursive: true })
  writeFileSync(`${root}/README.md`, 'memory e2e project')

  await page.goto('/projects', { waitUntil: 'networkidle' })
  await page.click('button:has-text("新建项目"), button:has-text("New project")')
  await page.getByRole('textbox', { name: /项目名|Project name/ }).fill('memory-e2e')
  await page.getByRole('textbox', { name: /绝对路径|absolute path/i }).fill(root)
  await page.click('button:has-text("创建"), button:has-text("Create")')
  await page.waitForURL(/\/projects\/.+\/overview/, { timeout: 15_000 })

  // Give the project its own memory scope with one file, then clear it.
  // page.request answers raw gateway JSON (snake_case), not the frontend's
  // camelCased client.
  const created = await page.request.get('/api/v1/projects')
  const projectID = (await created.json()).projects?.find((row: { name: string }) => row.name === 'memory-e2e')?.id
  const detail = await page.request.get(`/api/v1/projects/${projectID}`)
  const key = (await detail.json()).project_key
  const projectMemoryRoot = join(home, 'workspace', 'memories', 'projects', String(key))
  mkdirSync(projectMemoryRoot, { recursive: true })
  writeFileSync(join(projectMemoryRoot, 'project-note.md'), 'project only\n')

  await page.click('.project-tab:has-text("记忆"), .project-tab:has-text("Memory")')
  await expect(page.locator('[data-memory-file="project-note.md"]')).toBeVisible({ timeout: 10_000 })
  await shot(page, 'memories-project')

  await page.click('[data-testid="memories-clear-all"]')
  await page.click('[data-testid="memories-clear-confirm-ok"]')
  await expect(page.locator('[data-memory-file="project-note.md"]')).toHaveCount(0, { timeout: 10_000 })

  // The global scope the agent page lists is untouched by that clear.
  await page.goto('/memories', { waitUntil: 'networkidle' })
  await expect(page.locator('[data-memory-file="MEMORY.md"]')).toBeVisible({ timeout: 10_000 })
})

test('clearing the global scope empties the agent page', async ({ page }) => {
  test.skip(!globalRoot, 'E2E_HOME missing')
  seedGlobalFile('MEMORY.md', '# about to be cleared\n')
  seedGlobalFile('notes/global-only.md', 'gone soon\n')

  await signIn(page)
  await page.goto('/memories', { waitUntil: 'networkidle' })
  await expect(page.locator('[data-memory-file="MEMORY.md"]')).toBeVisible({ timeout: 10_000 })
  await page.click('[data-testid="memories-clear-all"]')
  await page.click('[data-testid="memories-clear-confirm-ok"]')
  await expect(page.locator('[data-memory-file]')).toHaveCount(0, { timeout: 10_000 })
  await expect(page.getByText(/暂无记忆文件|No memory files yet/)).toBeVisible()
})
