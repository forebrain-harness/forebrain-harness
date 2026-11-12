import { expect, test, type Page } from '@playwright/test'

import { shot, signIn } from './support'

/**
 * Settings · 子代理 · 「/goal 检查模型」(plan 299 §2, §5.2): the one system subagent
 * the settings page configures. The block is the third group of the tab —
 * after the subagent defaults (and after the peer switch where this machine has
 * Claude Code) — and its two rows save through `PATCH /api/settings`.
 *
 * What the block stores is the contract the backend reads, so the cases here
 * assert the configuration the save actually produced, not only what the page
 * shows: the model is a configured row's id, and the thinking level is "" while
 * it equals that row's own default (a live reading of the model page) and the
 * level itself only when it overrides one.
 */

const SUBAGENTS = '/settings?tab=subagents'

async function preferChinese(page: Page): Promise<void> {
  await page.addInitScript(() => {
    try {
      localStorage.setItem('forebrain-locale', 'zh')
    } catch {
      // A context with no storage still gets the page's own default.
    }
  })
}

/** Seeds one model row with a configured default level and answers its id. */
async function seedModel(page: Page, body: Record<string, unknown>): Promise<string> {
  const res = await page.request.post('/api/llm-models', { data: body })
  expect(res.ok(), await res.text()).toBeTruthy()
  const created = (await res.json()) as { id?: string }
  const id = String(created.id ?? '')
  expect(id).not.toBe('')
  return id
}

/** The stored settings the save produced. */
async function storedSettings(page: Page): Promise<Record<string, unknown>> {
  const res = await page.request.get('/api/settings')
  expect(res.ok(), await res.text()).toBeTruthy()
  const body = (await res.json()) as { values?: Record<string, unknown> }
  return body.values ?? {}
}

/** The block, signed in and standing on the subagents tab. */
async function openBlock(page: Page): Promise<void> {
  await preferChinese(page)
  await signIn(page)
  await page.goto(SUBAGENTS, { waitUntil: 'networkidle' })
  await expect(page.locator('[data-group="goal-check-model"]')).toBeVisible()
}

/**
 * Closes the candidate menu. The frozen prototype draws it open on arrival, and
 * every page load (a reload included) starts that way; a reader's first click or
 * Escape closes it, and the level row under it is only reachable afterwards.
 */
async function dismissMenu(page: Page): Promise<void> {
  const block = page.locator('[data-group="goal-check-model"]')
  if (await block.locator('.gcs__menu').isVisible()) await page.keyboard.press('Escape')
  await expect(block.locator('.gcs__menu')).toHaveCount(0)
}

/** Chooses one candidate. The menu opens on mount (the prototype's state), so
 *  only a closed one has to be opened first. */
async function pickRow(page: Page, id: string): Promise<void> {
  const block = page.locator('[data-group="goal-check-model"]')
  if (!(await block.locator('.gcs__menu').isVisible())) {
    await block.locator('[data-goal-check-model]').click()
  }
  await block.locator(`[data-goal-check-option="${id}"]`).click()
}

test.describe.configure({ mode: 'serial' })

test('the goal-check block is the subagents tab’s last group and opens unconfigured', async ({ page }) => {
  await openBlock(page)

  const block = page.locator('[data-group="goal-check-model"]')
  await expect(block.locator('h3')).toHaveText('/goal 检查模型')
  // §2: the title carries no explanation under it.
  await expect(block.locator('.gc__head p')).toHaveCount(0)

  // The order the prototype draws: the subagent defaults, then the peer switch
  // where it exists, then this block — never between or before them.
  const order = await page.evaluate(() => {
    const sa = document.querySelector('[data-group="sa"]')
    const peers = document.querySelector('[data-group="peers"]')
    const goal = document.querySelector('[data-group="goal-check-model"]')
    if (!sa || !goal) return { saBeforeGoal: false, peersBetween: false }
    const saBefore = (sa.compareDocumentPosition(goal) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0
    const peersBetween =
      !peers ||
      ((sa.compareDocumentPosition(peers) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0 &&
        (peers.compareDocumentPosition(goal) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0)
    return { saBeforeGoal: saBefore, peersBetween }
  })
  expect(order.saBeforeGoal).toBe(true)
  expect(order.peersBetween).toBe(true)

  // Nothing configured: both controls say 跟随主代理, and the level is disabled.
  await expect(block.locator('[data-goal-check-model]')).toContainText('跟随主代理')
  await expect(block.locator('[data-goal-check-effort]')).toBeDisabled()
  await expect(block.locator('[data-goal-check-effort]')).toHaveValue('')
  await expect(block.locator('[data-goal-check-hint]')).toHaveCount(0)
  await shot(page, 'settings-goal-check-unconfigured')
})

test('picks a row, stores its id and the live level, then overrides and clears', async ({ page }) => {
  await openBlock(page)
  // A row whose own default level is 高 high, so 跟随 and an override differ.
  const id = await seedModel(page, {
    provider: 'deepseek',
    model: 'deepseek-e2e-goal',
    base_url: 'https://api.deepseek.com',
    api_key: 'sk-e2e-goal-check-0001',
    caps: { efforts: ['low', 'high'], default_effort: 'high' },
  })
  await page.reload({ waitUntil: 'networkidle' })
  await dismissMenu(page)

  const block = page.locator('[data-group="goal-check-model"]')
  await pickRow(page, id)

  // The echo is the model and its provider, and the level follows the row.
  await expect(block.locator('[data-goal-check-model]')).toContainText('deepseek-e2e-goal')
  await expect(block.locator('[data-goal-check-model]')).not.toContainText('https://api.deepseek.com')
  await expect(block.locator('[data-goal-check-effort]')).toHaveValue('high')
  await expect(block.locator('[data-goal-check-effort]')).toBeEnabled()
  await expect(block.locator('[data-goal-check-hint]')).toHaveText('模型页配置的默认强度：高 high')
  await expect(block.locator('[data-goal-check-reset]')).toHaveCount(0)

  // Saving the row while the level equals its default stores "" — the live
  // reading — and the page says so.
  await block.locator('[data-goal-check-save]').click()
  await expect(block.locator('[data-goal-check-saved]')).toHaveText('✓ 已保存，下一轮对话生效')
  let values = await storedSettings(page)
  expect(values['agents.goal_evaluator.model']).toBe(id)
  expect(values['agents.goal_evaluator.reasoning_effort']).toBe('')

  // Reopened, the block comes back on exactly that.
  await page.reload({ waitUntil: 'networkidle' })
  await dismissMenu(page)
  await expect(block.locator('[data-goal-check-model]')).toContainText('deepseek-e2e-goal')
  await expect(block.locator('[data-goal-check-effort]')).toHaveValue('high')
  await expect(block.locator('[data-goal-check-hint]')).toHaveText('模型页配置的默认强度：高 high')

  // An override is stored as itself, and the hint and the reset link say so.
  await block.locator('[data-goal-check-effort]').selectOption('low')
  await expect(block.locator('[data-goal-check-hint]')).toHaveText('已改为 低 low，仅影响目标检查子代理')
  await expect(block.locator('[data-goal-check-reset]')).toHaveText('恢复为模型默认')
  await block.locator('[data-goal-check-save]').click()
  await expect(block.locator('[data-goal-check-saved]')).toHaveText('✓ 已保存，下一轮对话生效')
  values = await storedSettings(page)
  expect(values['agents.goal_evaluator.model']).toBe(id)
  expect(values['agents.goal_evaluator.reasoning_effort']).toBe('low')

  // The same row reopened keeps the override, and 恢复为模型默认 takes it back
  // to the live level — stored as "" again, not as a snapshot of it.
  await page.reload({ waitUntil: 'networkidle' })
  await dismissMenu(page)
  await expect(block.locator('[data-goal-check-effort]')).toHaveValue('low')
  await expect(block.locator('[data-goal-check-hint]')).toHaveText('已改为 低 low，仅影响目标检查子代理')
  await block.locator('[data-goal-check-reset]').click()
  await expect(block.locator('[data-goal-check-effort]')).toHaveValue('high')
  await expect(block.locator('[data-goal-check-hint]')).toHaveText('模型页配置的默认强度：高 high')
  await block.locator('[data-goal-check-save]').click()
  await expect(block.locator('[data-goal-check-saved]')).toHaveText('✓ 已保存，下一轮对话生效')
  values = await storedSettings(page)
  expect(values['agents.goal_evaluator.reasoning_effort']).toBe('')

  // The caret turns into a clear x on hover, and the x empties both controls.
  await block.locator('.gcs__caret-zone').hover()
  await expect(block.locator('[data-goal-check-clear]')).toBeVisible()
  await block.locator('[data-goal-check-clear]').click()
  await expect(block.locator('[data-goal-check-model]')).toContainText('跟随主代理')
  await expect(block.locator('[data-goal-check-effort]')).toBeDisabled()
  await expect(block.locator('[data-goal-check-effort]')).toHaveValue('')
  await expect(block.locator('[data-goal-check-hint]')).toHaveCount(0)

  await block.locator('[data-goal-check-save]').click()
  await expect(block.locator('[data-goal-check-saved]')).toHaveText('✓ 已保存，下一轮对话生效')
  values = await storedSettings(page)
  expect(values['agents.goal_evaluator.model']).toBe('')
  expect(values['agents.goal_evaluator.reasoning_effort']).toBe('')
  await shot(page, 'settings-goal-check-cleared')

  // And a reload agrees: the block is unconfigured again.
  await page.reload({ waitUntil: 'networkidle' })
  await dismissMenu(page)
  await expect(block.locator('[data-goal-check-model]')).toContainText('跟随主代理')
  await expect(block.locator('[data-goal-check-effort]')).toBeDisabled()
})
