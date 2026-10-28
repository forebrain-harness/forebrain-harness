import { mkdirSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'

import { seedSessionInRoot, shot, signIn } from './support'

/**
 * An office conversation's plan approval, on the real page (plan 153 A9).
 *
 * Coding work answers a plan with four choices that clear the context, keep
 * the plan or send it to a second model; office work cannot clear a
 * conversation it is meant to keep producing in, so its four words are 按计划
 * 执行 / 修改计划 / 只要计划，先不执行 / 请其他模型审阅. The last of those
 * only appears when a reviewer is configured.
 *
 * The turn is a real one: the conversation is put in plan mode through the
 * page's own mode switch, the plan file the model would have written is in
 * place (the words a model writes cannot be scripted, but the file it writes
 * lives at one path), and the model's `exit_plan_mode` call is scripted
 * through the fake provider's marker — so the approval is raised by the engine
 * and answered through the same endpoint every surface answers it with.
 */

/** The gateway's scratch home (web_e2e.sh exports it). */
function gatewayHome(): string {
  const home = process.env.E2E_HOME
  if (!home) throw new Error('E2E_HOME must point at the gateway FOREBRAIN_HOME')
  return home
}

/** The project key the gateway resolves plan paths by: the project root with
 *  its symlinks evaluated and separators turned into dashes, exactly as
 *  state.ProjectKeyForRoot does it. */
function projectKeyOf(root: string): string {
  return realpathSync(root).replace(/[/\\:]/g, '-')
}

const PLAN_TEXT = '# 计划：把季度材料整理成一份交付\n\n1. 收集三条渠道的原始数据。\n2. 汇总成一份报告，附一张分布图。\n'

/** The plan the model "wrote", under the state root, the project key and the
 *  session directory the gateway itself resolves — plans live per
 *  conversation (plans/<projectKey>/<session>/). */
function seedPlan(root: string, session: string): void {
  const dir = join(gatewayHome(), 'workspace', 'plans', projectKeyOf(root), session)
  mkdirSync(dir, { recursive: true })
  writeFileSync(join(dir, 'plan.md'), PLAN_TEXT)
}

/** The message whose scripted call leaves plan mode, which is the gate. */
const EXIT_PLAN_MESSAGE = `[[e2e:tool-call]]${JSON.stringify({ name: 'exit_plan_mode', arguments: {} })}\n\n计划写好了，退出计划模式。`

const scratch: string[] = []

test.afterAll(() => {
  for (const dir of scratch) rmSync(dir, { recursive: true, force: true })
})

/** An office conversation parked on its own exit-plan gate: plan mode on, the
 *  plan on disk, the model's call answered with the approval request. */
async function parkedOfficePlan(page: Page): Promise<string> {
  // An office project: a plain directory, so the gateway registers it as
  // 办公创作 and the conversation is born office.
  const root = mkdtempSync(join(tmpdir(), 'fb-e2e-153-office-plan-'))
  scratch.push(root)
  const session = await seedSessionInRoot(page, root, 'e2e-153-plan')
  const info = await page.request.get(`/api/chat/sessions/${encodeURIComponent(session)}`)
  expect(info.ok(), await info.text()).toBeTruthy()
  expect(((await info.json()) as { project_type?: string }).project_type).toBe('office')

  seedPlan(root, session)
  // The plan the gateway resolves is the one this spec wrote: the dock's body
  // and the deliverable both come from that file.
  const plan = await page.request.get(`/api/chat/sessions/${encodeURIComponent(session)}/plan-md`)
  expect(String(((await plan.json()) as { markdown?: string }).markdown ?? '')).toContain('汇总成一份报告')

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  // Plan mode is the person's own switch, and the office gate only exists
  // while it is on: exit_plan_mode in agent mode is a hallucination.
  await page.click('[data-testid="mode-chip"]')
  await page.click('[data-testid="mode-option-plan"]')
  await expect(page.locator('[data-testid="mode-chip"]')).toContainText('计划')

  await page.fill('textarea', EXIT_PLAN_MESSAGE)
  await page.press('textarea', 'Enter')
  // The approval takes the input's place — the dock, not a card in the
  // thread — and the plan is its body.
  const dock = page.locator('[data-testid="exit-plan-approval"]')
  await expect(dock).toBeVisible({ timeout: 60_000 })
  await expect(page.locator('[data-testid="exit-plan-text"]')).toContainText('汇总成一份报告')
  return session
}

test('an office plan is answered in the office\'s four words, never the coding ones', async ({ page }) => {
  test.info().setTimeout(180_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  await parkedOfficePlan(page)

  // The four office verdicts, in the prototype's order. 请其他模型审阅 needs a
  // configured reviewer, which this run has, so all four are on screen.
  await expect(page.locator('[data-testid="office-plan-execute"]')).toHaveText('按计划执行')
  await expect(page.locator('[data-testid="office-plan-revise"]')).toHaveText('修改计划')
  await expect(page.locator('[data-testid="office-plan-keep-only"]')).toHaveText('只要计划，先不执行')
  await expect(page.locator('[data-testid="exit-plan-ask-review"]')).toBeVisible()
  // The coding four — clearing the context above all — are not offered here:
  // an office deliverable keeps its conversation.
  await expect(page.locator('[data-testid="exit-plan-approve-clear"]')).toHaveCount(0)
  await expect(page.locator('[data-testid="exit-plan-approve"]')).toHaveCount(0)
  await expect(page.locator('[data-testid="exit-plan-keep-planning"]')).toHaveCount(0)
  // 修改计划 asks for the words the planner will read, and only then sends them.
  await expect(page.locator('[data-testid="exit-plan-feedback"]')).toHaveCount(0)
  await page.click('[data-testid="office-plan-revise"]')
  await expect(page.locator('[data-testid="exit-plan-feedback-input"]')).toBeVisible()
  await expect(page.locator('[data-testid="exit-plan-send-feedback"]')).toBeDisabled()
  await shot(page, 'p153-office-plan-dock')
})

test('只要计划 keeps the plan as the turn\'s deliverable and runs nothing', async ({ page }) => {
  test.info().setTimeout(180_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  await parkedOfficePlan(page)
  await page.click('[data-testid="office-plan-keep-only"]')
  await expect(page.locator('[data-testid="exit-plan-approval"]')).toHaveCount(0, { timeout: 60_000 })

  // The card head says what happened in the interface's words (已保留计划),
  // and the file itself is the turn's delivery group.
  await expect(page.locator('[data-testid="tool-kept-plan"]')).toBeVisible({ timeout: 15_000 })
  await expect(page.locator('.chat-shell main')).toContainText('已保留计划')
  const group = page.locator('[data-testid="artifact-list"]')
  await expect(group).toHaveCount(1, { timeout: 30_000 })
  await expect(group).toContainText('计划文件')
  const card = group.locator('[data-testid="artifact-card"]')
  await expect(card).toHaveCount(1)
  await expect(card).toContainText('plan.md')

  // The same turn read a second time from what the gateway stored: a replay
  // rebuilds the exit call's card from its row, which carries `plan_only` but
  // never the call's envelope. Nothing but the rows' own origin may name the
  // group here, or a kept-plan turn comes back as 本轮交付物.
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="tool-kept-plan"]')).toBeVisible({ timeout: 30_000 })
  await expect(page.locator('.chat-shell main')).toContainText('已保留计划')
  const reloaded = page.locator('[data-testid="artifact-list"]')
  await expect(reloaded).toHaveCount(1, { timeout: 30_000 })
  await expect(reloaded.locator('.artifacts-h b')).toHaveText('计划文件')
  // One row, and it is the plan file (the card titles itself by the plan's own
  // heading, so the row's name is the attribute the file is known by).
  await expect(reloaded.locator('[data-testid="artifact-card"]')).toHaveCount(1)
  await expect(reloaded.locator('[data-testid="artifact-card"] b')).toHaveAttribute('title', 'plan.md')

  // A plan is readable in the drawer: the markdown preview is the plan.
  await card.locator('[data-testid="artifact-preview"]').click()
  await expect(page.locator('[data-testid="artifact-markdown"]')).toContainText('汇总成一份报告')
  await shot(page, 'p153-office-plan-kept')
})
