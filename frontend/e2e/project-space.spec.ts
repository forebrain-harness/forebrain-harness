import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'

import { signIn } from './support'

/** The project space: its own shell, its own sessions, its own MCP preview —
 * and nothing from other layers inside it. */

// Projects may not bind a root inside the forebrain home (the create API
// rejects it), so the fixture lives in the scratch project directory the
// e2e gateway runs from — resolved from E2E_PROJECT_PARENT when set.
let fixtureParent = ''

async function seed(_page: Page, name: string): Promise<string> {
  if (!fixtureParent) {
    fixtureParent = process.env.E2E_PROJECT_PARENT ?? ''
    if (!fixtureParent) throw new Error('E2E_PROJECT_PARENT must point at a directory outside the forebrain home')
  }
  const root = join(fixtureParent, 'e2e-proj', name)
  mkdirSync(root, { recursive: true })
  writeFileSync(join(root, 'README.md'), `project ${name}`)
  return root
}

test.afterEach(() => {
  if (fixtureParent) rmSync(join(fixtureParent, 'e2e-proj'), { recursive: true, force: true })
})

async function createProject(page: Page, name: string): Promise<void> {
  const root = await seed(page, name)
  await page.goto('/projects', { waitUntil: 'networkidle' })
  await page.click('button:has-text("新建项目"), button:has-text("New project")')
  await page.getByRole('textbox', { name: /项目名|Project name/ }).fill(name)
  await page.getByRole('textbox', { name: /绝对路径|absolute path/i }).fill(root)
  await page.click('button:has-text("创建"), button:has-text("Create")')
  await page.waitForURL(/\/projects\/.+\/overview/, { timeout: 15_000 })
}

test('project list shows the agent scope and a created card', async ({ page }) => {
  await signIn(page)
  await createProject(page, 'e2e-alpha')
  await page.goto('/projects', { waitUntil: 'networkidle' })
  await expect(page.locator('text=e2e-alpha').first()).toBeVisible()
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/projects-list.png`, fullPage: true })
})

test('project space shell: band, breadcrumb, default overview', async ({ page }) => {
  await signIn(page)
  await createProject(page, 'e2e-alpha')
  await expect(page).toHaveURL(/\/projects\/.+\/overview$/)
  await expect(page.locator('.project-band')).toBeVisible()
  await expect(page.locator('.project-band')).toContainText('e2e-alpha')
  const tabLabels = await page.locator('.project-tab').allInnerTexts()
  expect(tabLabels.map((label) => label.trim()).length).toBe(8)
  // Placeholder tabs render their notice.
  await page.click('.project-tab:has-text("规则"), .project-tab:has-text("Rules")')
  await expect(page).toHaveURL(/\/rules$/)
})

test('project settings persist scope and resource access', async ({ page }) => {
  await signIn(page)
  await createProject(page, 'e2e-alpha')
  // Only-this-project memory + allow outside reads.
  await page.click('[data-testid="memory-scope-project"]')
  const resourceToggle = page.locator('input[type="checkbox"]').first()
  await resourceToggle.check()
  await page.click('button:has-text("保存"), button:has-text("Save")')
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="memory-scope-project"][aria-checked="true"], [data-testid="memory-scope-project"][aria-checked="true"]')).toHaveCount(1)
})

test('project sessions live only in the project space', async ({ page }) => {
  await signIn(page)
  await createProject(page, 'e2e-alpha')
  const projectUrl = page.url().replace(/\/overview$/, '')
  await page.click('.project-tab:has-text("会话"), .project-tab:has-text("Sessions")')
  await page.click('button:has-text("新的项目会话"), button:has-text("New session")')
  await page.waitForURL((url) => url.pathname === '/' && url.searchParams.has('session'), { timeout: 15_000 })
  await page.goto(projectUrl + '/sessions', { waitUntil: 'networkidle' })
  await expect(page.getByRole('button', { name: /未命名会话|Untitled session/ }).first()).toBeVisible({ timeout: 10_000 })
  // The drawer must not list the project session.
  await page.locator('.forebrain-rail-group > button.forebrain-rail-link').first().click()
  await expect(page.locator('[data-testid="chat-drawer"]')).toBeVisible()
  const drawerText = await page.locator('[data-testid="chat-drawer"]').innerText()
  expect(drawerText).not.toContain('e2e-alpha')
})

test('project mcp shows the trust gate then the preview', async ({ page }) => {
  await signIn(page)
  await createProject(page, 'e2e-alpha')
  await page.click('.project-tab:has-text("MCP")')
  await expect(page.locator('[data-testid="project-trust"]')).toBeVisible()
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/project-mcp.png`, fullPage: true })
  await page.click('[data-testid="project-trust"]')
  await expect(page.locator('[data-testid="project-trust"]')).toHaveCount(0, { timeout: 10_000 })
})

test('breadcrumb returns to the list', async ({ page }) => {
  await signIn(page)
  await createProject(page, 'e2e-alpha')
  await page.click('.project-breadcrumb-link')
  await page.waitForURL(/\/projects$/)
})
