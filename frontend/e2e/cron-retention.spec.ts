import { readFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'

import { signIn } from './support'

/**
 * Retention is install-level configuration with a web form (plan 006): the
 * settings tab opens from ?tab=cron, a save lands the key in forebrain.yaml
 * and survives a reload, the cron page states the value in force and links
 * back to its editor, and restoring the default removes the key again.
 */
const home = process.env.E2E_HOME ?? ''

test.skip(!home, 'E2E_HOME missing')

test('retention is set from the settings tab, shown on the cron page, and resettable', async ({ page }) => {
  await signIn(page)

  // /settings?tab=cron opens the scheduled-tasks tab with the default in force.
  await page.goto('/settings?tab=cron', { waitUntil: 'networkidle' })
  const days = page.getByTestId('cron-retention-days')
  await expect(days).toHaveValue('30')
  await expect(page.getByText(/30 days when not set\.|未设置时为 30 天。/)).toBeVisible()

  // Saving 7 writes the key and keeps it across a reload.
  await days.fill('7')
  await page.getByTestId('cron-retention-save').click()
  await expect(page.getByText(/Saved; it applies from the next cleanup\.|已保存，下一次清理时生效。/)).toBeVisible()
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.getByTestId('cron-retention-days')).toHaveValue('7')
  expect(readFileSync(`${home}/forebrain.yaml`, 'utf8')).toContain('retention_days: 7')

  // The cron page states the retention in force and its link returns to the tab.
  await page.goto('/cron', { waitUntil: 'networkidle' })
  await expect(page.getByTestId('cron-retention-note')).toContainText(/保留 7 天|kept for 7 days/)
  await page.getByTestId('cron-retention-edit').click()
  await page.waitForURL((url) => url.pathname === '/settings' && url.searchParams.get('tab') === 'cron')
  await expect(page.getByTestId('cron-retention-days')).toHaveValue('7')

  // Restoring the default removes the key from the file.
  await page.getByTestId('cron-retention-reset').click()
  await expect(page.getByTestId('cron-retention-days')).toHaveValue('30')
  expect(readFileSync(`${home}/forebrain.yaml`, 'utf8')).not.toContain('retention_days')
})
