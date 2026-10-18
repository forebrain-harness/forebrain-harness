import { mkdirSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'

import { expectAssistantReply, signIn } from './support'

/** The project space: its own shell, its own sessions, its own MCP preview —
 * and nothing from other layers inside it. */

// Projects may not bind a root inside the forebrain home (the create API
// rejects it), so the fixture lives in the scratch project directory the
// e2e gateway runs from — resolved from E2E_PROJECT_PARENT when set.
let fixtureParent = ''
// One project per (agent, root) is enforced now (plan 025), so each test needs
// its own root even though the project name is reused.
let seedSeq = 0

async function seed(_page: Page, name: string): Promise<string> {
  if (!fixtureParent) {
    fixtureParent = process.env.E2E_PROJECT_PARENT ?? ''
    if (!fixtureParent) throw new Error('E2E_PROJECT_PARENT must point at a directory outside the forebrain home')
  }
  const root = join(fixtureParent, 'e2e-proj', `${name}-${++seedSeq}`)
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

test('project space shell: breadcrumb, tabs, default overview', async ({ page }) => {
  await signIn(page)
  await createProject(page, 'e2e-alpha')
  await expect(page).toHaveURL(/\/projects\/.+\/overview$/)
  // The breadcrumb that names the project lives in the shell's top bar now, so
  // the page keeps only the tab row (plan 075).
  await expect(page.locator('.proj-head')).toBeVisible()
  await expect(page.locator('.forebrain-topbar-crumb')).toContainText('e2e-alpha')
  const tabLabels = await page.locator('.proj-tab').allInnerTexts()
  expect(tabLabels.map((label) => label.trim()).length).toBe(10)
  // Placeholder tabs render their notice.
  await page.click('.proj-tab:has-text("规则"), .proj-tab:has-text("Rules")')
  await expect(page).toHaveURL(/\/rules$/)
})

test('project settings persist scope and resource access', async ({ page }) => {
  await signIn(page)
  await createProject(page, 'e2e-alpha')
  // Only-this-project memory: the scope is a radio group in the settings form
  // now, and reading outside the project is one of its switches (plan 075).
  const scope = page.locator('[role="radiogroup"]')
  const projectOnly = scope.locator('label', { hasText: /仅本项目|This project only/ }).locator('input')
  await projectOnly.check()
  const outside = page.getByRole('switch', { name: /项目目录之外|outside the project directory/i })
  await expect(outside).toHaveAttribute('aria-checked', 'true')
  await outside.click()
  await page.click('[data-testid="project-settings-save"]')
  await page.reload({ waitUntil: 'networkidle' })
  await expect(projectOnly).toBeChecked()
  await expect(outside).toHaveAttribute('aria-checked', 'false')
})

test('project sessions live only in the project space', async ({ page }) => {
  // One full model turn names the session, which is what lists it.
  test.info().setTimeout(150_000)
  await signIn(page)
  await createProject(page, 'e2e-alpha')
  const projectUrl = page.url().replace(/\/overview$/, '')
  await page.click('.proj-tab:has-text("会话"), .proj-tab:has-text("Sessions")')
  await page.click('button:has-text("新建项目会话"), button:has-text("New project session")')
  await page.waitForURL((url) => url.pathname === '/' && url.searchParams.has('session'), { timeout: 15_000 })
  const sessionUrl = page.url()
  // A conversation with no message is not a conversation: the project's list
  // shows its empty state until the first message lands.
  await page.goto(projectUrl + '/sessions', { waitUntil: 'networkidle' })
  await expect(page.getByRole('button', { name: /未命名会话|Untitled session/ })).toHaveCount(0)
  await expect(page.getByText(/该项目还没有会话|No sessions in this project yet/)).toBeVisible()
  // Send the first message; the conversation becomes listable under its title.
  await page.goto(sessionUrl, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await page.fill('textarea', 'project-space-e2e')
  await page.press('textarea', 'Enter')
  await expectAssistantReply(page)
  await page.goto(projectUrl + '/sessions', { waitUntil: 'networkidle' })
  // The list is a table now: a row's title is the link that opens it.
  await expect(page.getByRole('link', { name: /project-space-e2e/ }).first()).toBeVisible({ timeout: 15_000 })
  // The drawer must not list the project session.
  await page.locator('.forebrain-rail-group > button.forebrain-rail-link').first().click()
  await expect(page.locator('[data-testid="chat-drawer"]')).toBeVisible()
  const drawerText = await page.locator('[data-testid="chat-drawer"]').innerText()
  expect(drawerText).not.toContain('e2e-alpha')
})

test('the chat page names a project session by its own title', async ({ page }) => {
  // One full model turn plus the title's read-back exceeds the default minute.
  test.info().setTimeout(150_000)
  await signIn(page)
  await createProject(page, 'e2e-alpha')
  await page.click('.proj-tab:has-text("会话"), .proj-tab:has-text("Sessions")')
  await page.click('button:has-text("新建项目会话"), button:has-text("New project session")')
  await page.waitForURL((url) => url.pathname === '/' && url.searchParams.has('session'), { timeout: 15_000 })
  await page.waitForSelector('textarea')
  // Unnamed so far: the header carries the page's placeholder, not a name.
  await expect(page.locator('[data-testid="chat-session-title"]')).toHaveText(/^(对话|Chat)$/)
  await page.fill('textarea', 'project-title-e2e')
  await page.press('textarea', 'Enter')
  await expectAssistantReply(page)
  // The first message named the session; the header now reads that title
  // from the session itself — the drawer's list never carried it.
  await expect(page.locator('[data-testid="chat-session-title"]')).toContainText('project-title-e2e', { timeout: 15_000 })
})

test('project mcp shows the trust gate then the preview', async ({ page }) => {
  await signIn(page)
  await createProject(page, 'e2e-alpha')
  await page.click('.proj-tab:has-text("MCP")')
  await expect(page.locator('[data-testid="project-trust"]')).toBeVisible()
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/project-mcp.png`, fullPage: true })
  await page.click('[data-testid="project-trust"]')
  await expect(page.locator('[data-testid="project-trust"]')).toHaveCount(0, { timeout: 10_000 })
})

test('breadcrumb returns to the list', async ({ page }) => {
  await signIn(page)
  await createProject(page, 'e2e-alpha')
  await page.click('[data-testid="project-crumb-back"]')
  await page.waitForURL(/\/projects$/)
})
