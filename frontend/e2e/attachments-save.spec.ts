import { existsSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'

import { expect, test } from '@playwright/test'

import { seedSessionInRoot, shot, signIn } from './support'

/** A one-pixel PNG, valid enough for the gateway to store and serve. */
const PNG =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=='

/** A fresh scratch directory the gateway can bind a project to. */
function scratchRoot(name: string): string {
  const parent = process.env.E2E_PROJECT_PARENT
  if (!parent) throw new Error('E2E_PROJECT_PARENT must point outside the forebrain home')
  const root = join(parent, 'attach-save', `${name}-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 7)}`)
  mkdirSync(root, { recursive: true })
  return root
}

/**
 * The "save to" flow (plan 030): attaching a file opens the dialog at once, a
 * new folder is made in the project, the upload lands inside it on disk, and
 * the next attachment opens the dialog on that same directory.
 */
test('an attachment is saved into a new project folder, then remembered', async ({ page }) => {
  await signIn(page)
  const root = scratchRoot('inbox')
  const session = await seedSessionInRoot(page, root, 'attach-save')
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')

  // Attaching the file opens the dialog before any message is sent.
  await page.setInputFiles('input[type=file]', {
    name: 'shot.png',
    mimeType: 'image/png',
    buffer: Buffer.from(PNG, 'base64'),
  })
  await expect(page.locator('[data-testid="attach-confirm"]')).toBeVisible()

  // A new folder under the project root, then save into it.
  await page.click('[data-testid="attach-new-folder"]')
  await page.fill('.sheet input', 'inbox')
  await page.click('.sheet .btn.pri')
  await expect(page.locator('[data-testid="attach-dir-inbox"]')).toHaveClass(/\bon\b/)
  await page.click('[data-testid="attach-confirm"]')

  // The file is on disk inside <root>/inbox.
  await expect.poll(() => existsSync(join(root, 'inbox', 'shot.png')), { timeout: 15_000 }).toBe(true)
  await shot(page, '30-attach-saved-into-inbox')

  // The next attachment opens the dialog on inbox, the remembered directory.
  await page.setInputFiles('input[type=file]', {
    name: 'second.png',
    mimeType: 'image/png',
    buffer: Buffer.from(PNG, 'base64'),
  })
  await expect(page.locator('[data-testid="attach-dir-inbox"]')).toHaveClass(/\bon\b/)
  await page.click('[data-testid="attach-confirm"]')
  await expect.poll(() => existsSync(join(root, 'inbox', 'second.png')), { timeout: 15_000 }).toBe(true)
  await shot(page, '30-attach-remembered-inbox')
})
