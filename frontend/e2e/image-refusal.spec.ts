import { execFileSync } from 'node:child_process'

import { expect, test } from '@playwright/test'

import { expectAssistantReply, seedProjectSession, shot, signIn } from './support'

/** A one-pixel PNG, valid enough for the gateway to store and serve. */
const PNG =
  'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=='

/**
 * The state database the gateway runs on, located the way an outsider has to:
 * by name under E2E_HOME (web_e2e.sh passes it in). Today the library is
 * state/forebrain.state.sqlite; the *.db arm keeps the search honest if the
 * name moves again.
 */
function stateDb(): string {
  const home = process.env.E2E_HOME
  if (!home) throw new Error('E2E_HOME must point at the gateway FOREBRAIN_HOME')
  const found = execFileSync('find', [home, '-name', '*.db', '-o', '-name', 'forebrain.state.sqlite'], { encoding: 'utf8' })
    .split('\n').map((line) => line.trim()).filter(Boolean)
  const db = found.find((path) => path.endsWith('forebrain.state.sqlite')) ?? found[0]
  if (!db) throw new Error(`no state database found under ${home}`)
  return db
}

/** One sqlite3 statement batch against the gateway's own state database. */
function sqlite(sql: string): string {
  // The gateway writes to the same file (its owner lease renews every few
  // seconds), so a read may meet the lock; wait for it rather than racing.
  return execFileSync('sqlite3', ['-cmd', '.timeout 5000', '-separator', '\t', stateDb(), sql], { encoding: 'utf8' })
}

/**
 * A message the session's model cannot take stays in the conversation (plan
 * 070): the row is stored, only the request is withheld, and the notice under
 * the message names the model in bold, says the message was kept, and offers
 * the composer's own model menu as the way out — the design's four states,
 * "图片 · 模型不支持 / 没换模型再发 / 换模型 / 换模型后继续". The notice is live
 * only: a reload draws the messages alone, the way the terminal does.
 */
test('an image the model cannot take keeps its message and refuses only the turn', async ({ page }) => {
  test.skip(process.env.E2E_REAL_LLM === '1', 'needs the fake chain with a text-only model')
  test.info().setTimeout(180_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedProjectSession(page, 'image-refusal')
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')

  // The session model menu marks what the catalog knows takes images: the
  // text-only entry carries no badge, deepseek-v4-flash does. Switching to the
  // text-only one is what makes the next turn refusable.
  await page.click('[data-testid="model-chip"]')
  const chatOption = page.locator('[data-testid="model-option-deepseek-v4-flash"]')
  const proOption = page.locator('[data-testid="model-option-deepseek-v4-pro"]')
  await expect(chatOption).toContainText('支持图片')
  await expect(chatOption.locator('[data-testid="model-supports-images"]')).toBeVisible()
  await expect(proOption.locator('[data-testid="model-supports-images"]')).toHaveCount(0)
  await proOption.click()
  await expect(page.locator('[data-testid="model-chip"]')).toContainText('deepseek-v4-pro')

  // A picture, saved into the project, then the question about it.
  await page.setInputFiles('input[type=file]', {
    name: 'shot.png',
    mimeType: 'image/png',
    buffer: Buffer.from(PNG, 'base64'),
  })
  await expect(page.locator('[data-testid="attach-confirm"]')).toBeVisible()
  await page.click('[data-testid="attach-confirm"]')
  await page.fill('textarea', '这个 401 是哪一步报的？')
  await page.press('textarea', 'Enter')

  // The message stayed, its picture stayed, and the box is empty again: only
  // the request was withheld.
  const firstNotice = page.locator('[data-testid="image-refused-notice"]')
  await expect(firstNotice).toHaveCount(1)
  await expect(firstNotice).toContainText('deepseek-v4-pro 不支持图片，消息已保留，未发送。')
  await expect(firstNotice).toContainText('对话里有图片，换支持图片的模型后继续。')
  await expect(page.locator('.u-wrap', { hasText: '这个 401 是哪一步报的？' })).toHaveCount(1)
  await expect(page.locator('[role=log] li', { hasText: 'shot.png' })).toHaveCount(1)
  await expect(page.locator('textarea')).toHaveValue('')
  await shot(page, '070-image-refused')

  // Sending again without switching model keeps the second message too, and
  // each refused message carries its own notice.
  await page.fill('textarea', '继续')
  await page.press('textarea', 'Enter')
  await expect(page.locator('[data-testid="image-refused-notice"]')).toHaveCount(2)
  await expect(page.locator('textarea')).toHaveValue('')
  await shot(page, '070-image-refused-again')

  // The notice's own link opens the model menu; a model that takes images
  // lets the same conversation continue, picture and all.
  await page.locator('[data-testid="image-refused-notice"]').last()
    .locator('[data-testid="image-refused-switch"]').click()
  await expect(page.locator('[data-testid="model-option-deepseek-v4-flash"]')).toBeVisible()
  await expect(page.locator('[data-testid="model-option-deepseek-v4-flash"] [data-testid="model-supports-images"]')).toBeVisible()
  await shot(page, '070-image-model-menu')
  await page.click('[data-testid="model-option-deepseek-v4-flash"]')
  await expect(page.locator('[data-testid="model-chip"]')).toContainText('deepseek-v4-flash')
  await page.fill('textarea', '继续')
  await page.press('textarea', 'Enter')
  await expectAssistantReply(page)
  await shot(page, '070-image-after-switch')

  // The refusal was never stored: a reload shows the messages alone.
  await page.reload({ waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await expect(page.locator('[data-testid="image-refused-notice"]')).toHaveCount(0)
  await expect(page.locator('.u-wrap', { hasText: '这个 401 是哪一步报的？' })).toHaveCount(1)
  await expect(page.locator('.u-wrap', { hasText: '继续' })).toHaveCount(2)

  // And the rows are in the state database: three user messages, the first
  // one still carrying the picture as the reference a later turn replays.
  const rows = sqlite(
    `SELECT content, parts FROM fb_messages
      WHERE session_id='${session}' AND role='user' AND visibility='visible' ORDER BY id;`,
  ).split('\n').filter(Boolean).map((line) => line.split('\t'))
  const asked = rows.filter(([content]) =>
    (content ?? '').startsWith('这个 401 是哪一步报的？') || (content ?? '').startsWith('继续'))
  expect(asked.map(([content]) => (content ?? '').split('\n')[0])).toEqual([
    '这个 401 是哪一步报的？',
    '继续',
    '继续',
  ])
  expect(asked[0]?.[1]).toContain('"type":"file_reference"')
  expect(asked[0]?.[1]).toContain('"mime_type":"image/png"')
})
