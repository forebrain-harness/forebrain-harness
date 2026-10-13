import { mkdirSync, rmSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from '@playwright/test'

import { expectAssistantReply, shot, signIn } from './support'

/**
 * The new-chat page (plan 029): a conversation is opened into a project the
 * user picks. Choosing a folder with no project makes one; a directory nobody
 * has judged is trusted once before it is used, and the created project is
 * announced at the top of the thread.
 */
const parent = process.env.E2E_PROJECT_PARENT ?? ''
const projectRoot = join(parent, 'e2e-newchat')
const folderName = 'fresh-piece'

test.afterEach(() => {
  rmSync(projectRoot, { recursive: true, force: true })
})

test('a new chat picks a folder, creates the project, and trusts it once', async ({ page }) => {
  test.info().setTimeout(180_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'en'))
  await signIn(page)

  // A project so the folder picker has a place to start from — its sidebar
  // lists the agent's projects.
  mkdirSync(projectRoot, { recursive: true })
  const created = await page.request.post('/api/v1/projects', { data: { name: 'e2e-newchat', root: projectRoot } })
  expect(created.ok(), await created.text()).toBeTruthy()

  await page.goto('/', { waitUntil: 'networkidle' })
  // The new-conversation page: greeting, project pill, quick starts.
  await expect(page.locator('[data-testid="chat-greeting"]')).toBeVisible()
  await shot(page, 'p029-1-fresh')

  // 1. The project chooser (prototype: 新对话 · 选项目弹层).
  await page.click('[data-testid="project-pill"]')
  await expect(page.locator('.pj-pop')).toBeVisible()
  await shot(page, 'p029-2-project-popover')

  // 2. The folder picker (prototype: 浏览文件夹).
  await page.click('.pj-opt:has-text("Open folder")')
  await expect(page.locator('.modal.fb')).toBeVisible()
  await page.click('.fb-side button:has-text("e2e-newchat")')
  await shot(page, 'p029-3-folder-dialog')

  // 3. A new folder made and chosen (prototype: 新对话 · 选了新文件夹).
  await page.click('.modal-f .btn.sm')
  await page.fill('.sheet input', folderName)
  await page.click('.sheet .btn.pri')
  await expect(page.locator('.sel-path')).toContainText(folderName)
  await page.click('.modal-f .btn.pri')

  // The pill names the folder as a new project.
  await expect(page.locator('[data-testid="project-pill"]')).toContainText(folderName)
  await expect(page.locator('[data-testid="project-pill"]')).toContainText('New project')
  await shot(page, 'p029-4-new-folder-picked')

  // 4. The trust prompt (prototype: 信任提示): the directory is unjudged, so
  // trust is asked once before anything runs.
  await page.fill('textarea', 'first message from the new chat')
  await page.press('textarea', 'Enter')
  await expect(page.locator('.trust-path')).toBeVisible()
  await shot(page, 'p029-5-trust-dialog')
  await page.click('.modal-f .btn.pri') // Trust and continue

  await expectAssistantReply(page)
  // The project the send created is announced at the top of the thread.
  await expect(page.locator('[data-testid="created-project-line"]')).toBeVisible()
  await expect(page.locator('[data-testid="created-project-line"]')).toContainText('fresh-piece')
})
