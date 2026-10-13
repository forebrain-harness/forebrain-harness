import { mkdirSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'

import { seedProjectSession, signIn } from './support'

let fixtureParent = ''
let ruleSeedSeq = 0

async function seedProject(_page: Page, name: string): Promise<string> {
  if (!fixtureParent) {
    fixtureParent = process.env.E2E_PROJECT_PARENT ?? ''
    if (!fixtureParent) throw new Error('E2E_PROJECT_PARENT must point at a directory outside the forebrain home')
  }
  // A unique root per test: one project per (agent, root) is enforced.
  const root = join(fixtureParent, 'e2e-rules', `${name}-${++ruleSeedSeq}`)
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
  const rows = await page.locator('[data-testid="rule-file-row"]').allInnerTexts()
  expect(rows.length).toBe(3)
  // Create USER.md through the modal (only uncreated files are offered).
  await page.click('[data-testid="rules-create"]')
  const select = page.locator('[data-testid="rules-create-select"]')
  const options = await select.locator('option').allInnerTexts()
  if (options.some((option) => option.includes('USER.md'))) {
    await select.selectOption({ index: options.findIndex((option) => option.includes('USER.md')) })
    await page.click('[data-testid="rules-create-ok"]')
    // The editor modal opens; write into the WYSIWYG editor and save.
    const prose = page.locator('[data-testid="rules-editor-modal"] .ProseMirror')
    await expect(prose).toBeVisible({ timeout: 15_000 })
    await prose.click()
    await page.keyboard.type('Be kind.')
    await page.click('[data-testid="rules-save"]')
    await expect(page.getByText(/已保存|Saved/).first()).toBeVisible({ timeout: 10_000 })
    // Reopening loads the saved bytes from disk.
    await page.reload({ waitUntil: 'networkidle' })
    await page.click('[data-testid="rule-edit-USER.md"]')
    await expect(page.locator('[data-testid="rules-editor-modal"] .ProseMirror')).toContainText('Be kind.', { timeout: 15_000 })
    await page.getByRole('button', { name: /取消|Cancel/ }).click()
  }
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/rules-agent.png`, fullPage: true })
})

test('project rules tab manages the FOREBRAIN.md chain', async ({ page }) => {
  await signIn(page)
  const projectId = await createProject(page, 'e2e-rules')
  await page.goto(`/projects/${projectId}/rules`, { waitUntil: 'networkidle' })
  // A freshly created project may still need the trust decision before its
  // rules load.
  const trust = page.locator('[data-testid="rules-trust"]')
  if (await trust.count()) await trust.click()
  // Write the root file by editing the root row.
  await page.click('[data-testid="rule-edit-FOREBRAIN.md"]')
  const rootProse = page.locator('[data-testid="rules-editor-modal"] .ProseMirror')
  await expect(rootProse).toBeVisible({ timeout: 15_000 })
  await rootProse.click()
  await page.keyboard.type('root rules')
  await page.click('[data-testid="rules-save"]')
  await expect(page.getByText(/已保存|Saved/).first()).toBeVisible({ timeout: 10_000 })
  // Create one in a first-level directory through the modal.
  await page.click('[data-testid="rules-create"]')
  const select = page.locator('[data-testid="rules-create-select"]')
  const options = await select.locator('option').allInnerTexts()
  const docsIndex = options.findIndex((option) => option.includes('docs'))
  if (docsIndex >= 0) {
    await select.selectOption({ index: docsIndex })
    await page.click('[data-testid="rules-create-ok"]')
    const docsProse = page.locator('[data-testid="rules-editor-modal"] .ProseMirror')
    await expect(docsProse).toBeVisible({ timeout: 15_000 })
    await docsProse.click()
    await page.keyboard.type('docs rules')
    await page.click('[data-testid="rules-save"]')
    await expect(page.getByText(/已保存|Saved/).first()).toBeVisible({ timeout: 10_000 })
    await page.reload({ waitUntil: 'networkidle' })
    const rows = await page.locator('[data-testid="rule-file-row"]').allInnerTexts()
    expect(rows.join('\n')).toContain('FOREBRAIN.md')
    expect(rows.join('\n')).toContain('docs/FOREBRAIN.md')
  }
})

test('permissions page adds a rule through a modal and removes it', async ({ page }) => {
  await signIn(page)
  await page.goto('/permissions', { waitUntil: 'networkidle' })
  await page.click('[data-testid="add-rule"]')
  // The tool dropdown is populated from the runtime registry.
  await page.waitForFunction(() => {
    const select = document.querySelector('[data-testid="rule-tool"]') as HTMLSelectElement | null
    return !!select && select.options.length > 0
  }, undefined, { timeout: 15_000 })
  await page.fill('[data-testid="rule-content"]', '/tmp/e2e-rules-*')
  await page.click('[data-testid="rule-save"]')
  const row = page.locator('[data-testid="agent-rule"]', { hasText: '/tmp/e2e-rules-*' })
  await expect(row).toHaveCount(1, { timeout: 10_000 })
  // The rule is the agent's own and on disk: a reload still lists it.
  await page.reload({ waitUntil: 'networkidle' })
  await expect(row).toHaveCount(1, { timeout: 10_000 })
  // And it can be taken back from the same row through the confirm dialog.
  await row.locator('[data-testid="remove-rule"]').click()
  await page.click('[data-testid="confirm-ok"]')
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
  // Open a conversation (plan 029: every conversation belongs to a project).
  const readOnlySession = await seedProjectSession(page, 'rules-a')
  await page.goto(`/?session=${readOnlySession}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('[data-testid="approval-picker"]:not([disabled])', { timeout: 15_000 })
  await page.click('[data-testid="approval-picker"]')
  await page.click('[data-testid="approval-option-read-only"]')
  // The choice is accepted (no error surface) and the picker shows it.
  await expect(page.locator('[data-testid="approval-picker"]')).toContainText(/只读|Read Only/)
  const readOnlyURL = page.url()
  // The preset is this conversation's alone: another one does not inherit it.
  const otherSession = await seedProjectSession(page, 'rules-b')
  const otherRead = page.waitForResponse((res) => res.request().method() === 'GET'
    && res.url().includes('/permissions/session-preset?')
    && !res.url().includes(encodeURIComponent(readOnlySession)))
  await page.goto(`/?session=${otherSession}`, { waitUntil: 'networkidle' })
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
  await expect(page.locator('[data-testid="add-rule"]')).toBeVisible({ timeout: 10_000 })
  // The rule lands in this project's own settings, and the trust decision
  // survives a reload (the gate reads the project row, not the create answer).
  await page.click('[data-testid="add-rule"]')
  await page.waitForFunction(() => {
    const select = document.querySelector('[data-testid="rule-tool"]') as HTMLSelectElement | null
    return !!select && select.options.length > 0
  }, undefined, { timeout: 15_000 })
  await page.fill('[data-testid="rule-content"]', 'rm -rf:*')
  await page.click('[data-testid="rule-save"]')
  await expect(page.getByText('rm -rf:*').first()).toBeVisible({ timeout: 10_000 })
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="perm-trust"]')).toHaveCount(0)
  await expect(page.getByText('rm -rf:*').first()).toBeVisible({ timeout: 10_000 })
})
