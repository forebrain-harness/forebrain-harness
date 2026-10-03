import { mkdirSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'

import { signIn } from './support'

let fixtureParent = ''

async function seedProject(_page: Page, name: string): Promise<string> {
  if (!fixtureParent) {
    fixtureParent = process.env.E2E_PROJECT_PARENT ?? ''
    if (!fixtureParent) throw new Error('E2E_PROJECT_PARENT must point at a directory outside the forebrain home')
  }
  const root = join(fixtureParent, 'e2e-rules', name)
  mkdirSync(join(root, 'docs'), { recursive: true })
  // A repository: the engine takes project permission rules only from a
  // trusted, version-controlled project.
  mkdirSync(join(root, '.git'), { recursive: true })
  return root
}

test.afterEach(() => {
  if (fixtureParent) rmSync(join(fixtureParent, 'e2e-rules'), { recursive: true, force: true })
})

async function createProject(page: Page, name: string): Promise<string> {
  const root = await seedProject(page, name)
  await page.goto('/projects', { waitUntil: 'networkidle' })
  await page.click('button:has-text("新建项目"), button:has-text("New project")')
  await page.getByRole('textbox', { name: /项目名|Project name/ }).fill(name)
  await page.getByRole('textbox', { name: /绝对路径|absolute path/i }).fill(root)
  await page.click('button:has-text("创建"), button:has-text("Create")')
  await page.waitForURL(/\/projects\/.+\/overview$/, { timeout: 15_000 })
  return page.url().match(/\/projects\/([^/]+)\/overview/)![1]
}

test('agent rules page creates and edits a bootstrap file', async ({ page }) => {
  await signIn(page)
  await page.goto('/rules', { waitUntil: 'networkidle' })
  // The three bootstrap files are listed.
  const rows = await page.locator('.rules-file-row').allInnerTexts()
  expect(rows.length).toBe(3)
  // Create USER.md via the dropdown (only uncreated files are offered).
  const select = page.locator('[data-testid="rules-create-select"]')
  const options = await select.locator('option').allInnerTexts()
  if (options.some((option) => option.includes('USER.md'))) {
    await select.selectOption({ index: options.findIndex((option) => option.includes('USER.md')) })
    await page.locator('.rules-editor').fill('# user rules\nBe kind.')
    await page.click('[data-testid="rules-save"]')
    await expect(page.getByText(/已保存|Saved/).first()).toBeVisible({ timeout: 10_000 })
    await page.reload({ waitUntil: 'networkidle' })
    await expect(page.locator('.rules-editor')).toHaveValue(/Be kind\./, { timeout: 10_000 })
  }
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/rules-agent.png`, fullPage: true })
})

test('project rules tab manages the FOREBRAIN.md chain', async ({ page }) => {
  await signIn(page)
  const projectId = await createProject(page, 'e2e-rules')
  await page.goto(`/projects/${projectId}/rules`, { waitUntil: 'networkidle' })
  // Create the root file.
  await page.locator('.rules-editor').fill('# root rules')
  await page.click('[data-testid="rules-save"]')
  await expect(page.getByText(/已保存|Saved/).first()).toBeVisible({ timeout: 10_000 })
  // Create one in a first-level directory via the dropdown.
  const select = page.locator('[data-testid="rules-create-select"]')
  const options = await select.locator('option').allInnerTexts()
  const docsIndex = options.findIndex((option) => option.includes('docs'))
  if (docsIndex >= 0) {
    await select.selectOption({ index: docsIndex })
    await page.locator('.rules-editor').fill('# docs rules')
    await page.click('[data-testid="rules-save"]')
    await expect(page.getByText(/已保存|Saved/).first()).toBeVisible({ timeout: 10_000 })
    await page.reload({ waitUntil: 'networkidle' })
    const rows = await page.locator('.rules-file-row').allInnerTexts()
    expect(rows.join('\n')).toContain('FOREBRAIN.md')
    expect(rows.join('\n')).toContain('docs/FOREBRAIN.md')
  }
})

test('permissions page adds a rule through dropdowns and verifies it', async ({ page }) => {
  await signIn(page)
  await page.goto('/permissions', { waitUntil: 'networkidle' })
  // Tool dropdown is populated from the runtime registry.
  await page.waitForFunction(() => {
    const select = document.querySelectorAll('select')
    return select.length > 0 && select[select.length - 1].options.length > 1
  }, undefined, { timeout: 15_000 })
  await page.getByRole('textbox', { name: /路径或命令|path or command/i }).fill('/tmp/e2e-rules-*')
  await page.waitForFunction(() => (document.querySelector('[data-testid="add-rule"]') as HTMLButtonElement | null)?.disabled === false, undefined, { timeout: 15_000 })
  await page.click('[data-testid="add-rule"]')
  const row = page.locator('[data-testid="agent-rule"]', { hasText: '/tmp/e2e-rules-*' })
  await expect(row).toHaveCount(1, { timeout: 10_000 })
  // The rule is the agent's own and on disk: a reload still lists it.
  await page.reload({ waitUntil: 'networkidle' })
  await expect(row).toHaveCount(1, { timeout: 10_000 })
  // And it can be taken back from the same row.
  await row.locator('[data-testid="remove-rule"]').click()
  await expect(row).toHaveCount(0, { timeout: 10_000 })
  await page.reload({ waitUntil: 'networkidle' })
  await expect(row).toHaveCount(0, { timeout: 10_000 })
})

test('settings approval tab persists the preset', async ({ page }) => {
  await signIn(page)
  await page.goto('/settings', { waitUntil: 'networkidle' })
  await page.locator('[data-testid="settings-tabs"] button', { hasText: /审批默认|Approval default/ }).click()
  await page.click('[data-testid="approval-full-access"]')
  await expect(page.getByText(/已保存为全局默认|Saved as the global default/).first()).toBeVisible({ timeout: 10_000 })
  // Reopening keeps the selection.
  await page.reload({ waitUntil: 'networkidle' })
  await page.locator('[data-testid="settings-tabs"] button', { hasText: /审批默认|Approval default/ }).click()
  await expect(page.locator('[data-testid="approval-full-access"]').first()).toHaveAttribute('aria-checked', 'true', { timeout: 10_000 })
  // Restore the safe default.
  await page.click('[data-testid="approval-auto"]')
  await expect(page.getByText(/已保存为全局默认|Saved as the global default/).first()).toBeVisible({ timeout: 10_000 })
})

test('session picker switches the approval preset', async ({ page }) => {
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  // Without a session the picker is inert.
  await expect(page.locator('[data-testid="approval-picker"]')).toBeDisabled()
  // Start a conversation: the drawer creates a session.
  await page.locator('.forebrain-rail-group > button.forebrain-rail-link').first().click()
  await page.waitForSelector('[data-testid="chat-drawer"]')
  await page.click('[data-testid="drawer-new-chat"]')
  await page.waitForURL(/session=/)
  await page.waitForSelector('[data-testid="approval-picker"]:not([disabled])', { timeout: 15_000 })
  await page.click('[data-testid="approval-picker"]')
  await page.click('[data-testid="approval-option-read-only"]')
  // The choice is accepted (no error surface) and the picker shows it.
  await expect(page.locator('[data-testid="approval-picker"]')).toContainText(/只读|Read Only/)
  const readOnlyURL = page.url()
  const readOnlySession = new URL(readOnlyURL).searchParams.get('session') ?? ''
  // The preset is this conversation's alone: a new one does not inherit it.
  const otherRead = page.waitForResponse((res) => res.request().method() === 'GET'
    && res.url().includes('/permissions/session-preset?')
    && !res.url().includes(encodeURIComponent(readOnlySession)))
  await page.locator('.forebrain-rail-group > button.forebrain-rail-link').first().click()
  await page.waitForSelector('[data-testid="chat-drawer"]')
  await page.click('[data-testid="drawer-new-chat"]')
  await page.waitForURL((url) => url.toString() !== readOnlyURL && /session=/.test(url.toString()))
  expect((await otherRead).ok()).toBe(true)
  await page.waitForSelector('[data-testid="approval-picker"]:not([disabled])', { timeout: 15_000 })
  await expect(page.locator('[data-testid="approval-picker"]')).not.toContainText(/只读|Read Only/)
  // Coming back to the first conversation finds its own choice still there.
  await page.goto(readOnlyURL, { waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="approval-picker"]')).toContainText(/只读|Read Only/, { timeout: 10_000 })
})

test('project perm tab gates on trust', async ({ page }) => {
  await signIn(page)
  const projectId = await createProject(page, 'e2e-rules')
  await page.goto(`/projects/${projectId}/perm`, { waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="perm-trust"]')).toBeVisible()
  await page.click('[data-testid="perm-trust"]')
  await expect(page.locator('[data-testid="perm-add"]')).toBeVisible({ timeout: 10_000 })
  // The rule lands in this project's own settings, and the trust decision
  // survives a reload (the gate reads the project row, not the create answer).
  await page.getByPlaceholder(/路径或命令|path or command/i).first().fill('rm -rf:*')
  await page.click('[data-testid="perm-add"]')
  await expect(page.getByText('rm -rf:*').first()).toBeVisible({ timeout: 10_000 })
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="perm-trust"]')).toHaveCount(0)
  await expect(page.getByText('rm -rf:*').first()).toBeVisible({ timeout: 10_000 })
})
