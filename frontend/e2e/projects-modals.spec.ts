import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

import { expect, test } from '@playwright/test'

import { signIn } from './support'

/**
 * The project list's modals: creating is a modal with a directory and a name
 * (with the shared folder browser), and deleting is a modal that says what it
 * would take with it and asks for the project's name before it acts.
 */

test('the new-project modal offers the folder browser', async ({ page }) => {
  await signIn(page)
  await page.goto('/projects', { waitUntil: 'networkidle' })
  await page.click('[data-testid="projects-new"]')
  await expect(page.locator('[data-testid="project-root"]')).toBeVisible()
  await expect(page.getByRole('textbox', { name: /项目名|Project name/ })).toBeVisible()

  // The picker is the shared folder dialog from the composer.
  await page.locator('.forebrain-modal button:has-text("浏览"), .forebrain-modal button:has-text("Browse")').click()
  await expect(page.locator('.modal.fb')).toBeVisible()
  await page.locator('.modal.fb button:has-text("取消"), .modal.fb button:has-text("Cancel")').click()
  await expect(page.locator('.modal.fb')).toHaveCount(0)
  // The new-project modal itself is still open; close it.
  await page.locator('.forebrain-modal button:has-text("取消"), .forebrain-modal button:has-text("Cancel")').click()
  await expect(page.locator('[data-testid="project-root"]')).toHaveCount(0)
})

test('deleting a project asks for its name first', async ({ page }) => {
  await signIn(page)
  const parent = process.env.E2E_PROJECT_PARENT ?? ''
  test.skip(!parent, 'E2E_PROJECT_PARENT missing')
  const root = join(parent, 'e2e-del', `del-${Date.now().toString(36)}`)
  mkdirSync(root, { recursive: true })

  await page.goto('/projects', { waitUntil: 'networkidle' })
  await page.click('[data-testid="projects-new"]')
  await page.getByRole('textbox', { name: /项目名|Project name/ }).fill('e2e-del')
  await page.getByRole('textbox', { name: /绝对路径|absolute path/i }).fill(root)
  await page.click('[data-testid="project-create"]')
  await page.waitForURL(/\/projects\/.+\/overview$/, { timeout: 15_000 })

  await page.goto('/projects', { waitUntil: 'networkidle' })
  await page.click('[data-testid="project-delete-e2e-del"]')

  // The confirm stays disabled until the project's name is retyped.
  const ok = page.locator('[data-testid="confirm-ok"]')
  await expect(ok).toBeDisabled()
  await expect(page.locator('.forebrain-modal')).toContainText(/无法恢复|cannot be undone/)
  await page.locator('.forebrain-modal input').fill('e2e-del')
  await expect(ok).toBeEnabled()
  await ok.click()

  await expect(page.locator('[data-project-row="e2e-del"]')).toHaveCount(0, { timeout: 10_000 })
})
