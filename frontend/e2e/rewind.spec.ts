import { expect, test } from '@playwright/test'

import { expectAssistantReply, seedProjectSession, shot, signIn } from './support'

/**
 * Rewind to before a message (plan 033), proven on the real page: a user
 * message offers "回到这里" on hover, the confirm dialog opens with the three
 * modes, restoring leaves the undo line and puts the message back in the
 * composer, and undoing puts the conversation back.
 */
test('a user message offers rewind, and restoring leaves an undo line', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedProjectSession(page, 'chat-rewind')
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await page.fill('textarea', '请把这次对话回到这里之前')
  await page.press('textarea', 'Enter')
  await expectAssistantReply(page)

  // The hover actions appear on the user message.
  const wrap = page.locator('.u-wrap').first()
  await wrap.hover()
  const rewind = page.locator('[data-testid="message-rewind"]').first()
  await expect(rewind).toBeVisible()
  await shot(page, 'p033-1-message-actions')

  // The dialog opens with the message and the three modes.
  await rewind.click()
  await expect(page.locator('.modal.sm')).toBeVisible()
  await expect(page.locator('.rw-modes .rc')).toHaveCount(3)
  await shot(page, 'p033-2-rewind-dialog')

  // Conversation-only: no files are listed, and the mode is the one in force.
  await page.click('.rw-modes .rc:has-text("只恢复对话")')
  await expect(page.locator('.rw-modes .rc[aria-checked="true"]')).toContainText('只恢复对话')

  // Restore: the undo line appears and the anchor goes back to the composer.
  await page.click('[data-testid="rewind-restore"]')
  await expect(page.locator('[data-testid="rewind-undo-line"]')).toBeVisible()
  await expect(page.locator('textarea')).toHaveValue(/请把这次对话回到这里之前/)
  await shot(page, 'p033-3-after-restore')

  // Undo puts the conversation back and retires the undo line.
  await page.click('[data-testid="rewind-undo"]')
  await expect(page.locator('[data-testid="rewind-undo-line"]')).toHaveCount(0)
  await expect(page.getByText('请把这次对话回到这里之前').first()).toBeVisible()
})
