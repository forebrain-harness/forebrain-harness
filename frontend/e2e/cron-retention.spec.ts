import { expect, test } from '@playwright/test'

import { signIn } from './support'

/**
 * Retention is install-level configuration with a web form (plans 006, 191):
 * the settings tab opens from ?tab=cron on the value the state database holds,
 * a group save writes it there, and the value survives a reload — the file on
 * disk has nothing to do with it any more.
 */
test('retention is set from the settings tab and survives a reload', async ({ page }) => {
  // The design's copy is Chinese; the assertions read the prototype's words.
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  // /settings?tab=cron opens the scheduled-tasks tab on the stored value; the
  // group is the tab's only one, so the row carries the field's own name.
  await page.goto('/settings?tab=cron', { waitUntil: 'networkidle' })
  const days = page.locator('[data-num="cron.retention_days"]')
  await expect(days).toBeVisible()
  await expect(page.locator('[data-field="cron.retention_days"] b')).toHaveText('执行记录保留')
  await expect(page.locator('.settings-num__range')).toHaveText('1–3650')
  const initial = await days.inputValue()

  // Saving 7 writes the key and keeps it across a reload.
  await days.fill('7')
  await page.locator('[data-save="cn"]').click()
  await expect(page.getByText('已保存，下一轮生效').first()).toBeVisible({ timeout: 10_000 })
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-num="cron.retention_days"]')).toHaveValue('7')

  // A number past the range turns red in place — the prototype's own rule —
  // and the gateway refuses it naming the row.
  await page.locator('[data-num="cron.retention_days"]').fill('4000')
  await expect(page.locator('.settings-num__box')).toHaveClass(/is-bad/)

  // Back to what the install had.
  await page.locator('[data-num="cron.retention_days"]').fill(initial)
  await page.locator('[data-save="cn"]').click()
  await expect(page.getByText('已保存，下一轮生效').first()).toBeVisible({ timeout: 10_000 })
})
