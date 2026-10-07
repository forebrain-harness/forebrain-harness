import { expect, test } from '@playwright/test'

import { expectAssistantReply, signIn } from './support'

/**
 * A heartbeat is a turn of the conversation it belongs to: the prompt it
 * sends is drawn marked as the heartbeat's, the answer and the worked line
 * follow the way any sent turn's do, and a reload rebuilds the same
 * conversation from the stored rows instead of losing them.
 */
test('a heartbeat turn appears in its conversation and survives a reload', async ({ page }) => {
  // The scheduler ticks every 30s on a one-minute floor, so the first beat
  // lands within about 90s of the heartbeat being saved; two turns plus that
  // wait exceed the default minute.
  test.info().setTimeout(240_000)
  await signIn(page)

  const created = await page.request.post('/api/chat/sessions', { data: {} })
  expect(created.ok()).toBeTruthy()
  const session = String((await created.json()).id)

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await page.fill('textarea', 'first-e2e')
  await page.press('textarea', 'Enter')
  await expectAssistantReply(page)

  // The scheduler fires the beat on its own: the page that happens to be
  // watching sees the turn start with the prompt the heartbeat wrote.
  const saved = await page.request.put('/api/heartbeat', {
    data: { session_id: session, interval_seconds: 60, prompt: 'e2e heartbeat ping' },
  })
  expect(saved.ok()).toBeTruthy()

  const heartbeatMessage = page.locator('[role=log] .is-user', { hasText: 'e2e heartbeat ping' })
  await expect(heartbeatMessage.locator('[data-testid="message-origin"]')).toBeVisible({ timeout: 150_000 })
  // The beat's turn answers and closes the way a sent one does.
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[role=log] .is-assistant').length >= 2
      && document.querySelectorAll('[data-testid="run-worked-line"]').length >= 2,
    undefined,
    { timeout: 150_000, polling: 500 },
  )

  // What the runtime wrote is storage, not this page's memory: a reload
  // rebuilds the conversation from the rows, label and answer included.
  await page.reload({ waitUntil: 'networkidle' })
  await expect(
    page.locator('[role=log] .is-user', { hasText: 'e2e heartbeat ping' }).locator('[data-testid="message-origin"]'),
  ).toBeVisible({ timeout: 15_000 })
  await expect(page.locator('[data-testid="run-worked-line"]')).toHaveCount(2, { timeout: 15_000 })

  await page.request.delete(`/api/heartbeat?session_id=${session}`)
})
