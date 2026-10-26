import { expect, test, type Page } from '@playwright/test'
import { readFileSync } from 'node:fs'

import { revealTab, shot, signIn } from './support'

/**
 * The skill workshop is the chat page plus skill tabs: the same composer and
 * workbench, with four extra tabs (test cases, evaluation, trigger
 * description, versions). It is reached from the skills pages' own buttons,
 * not a rail entry (plan 020).
 */

const fixtureZip = process.env.E2E_SKILL_ZIP ?? ''
const realLLM = process.env.E2E_REAL_LLM === '1'


// The suite shares one gateway, so demo-e2e survives between tests: install it
// once and reuse the row.
async function ensureDemoSkill(page: Page) {
  if (!fixtureZip) return
  await page.goto('/skills', { waitUntil: 'networkidle' })
  if (await page.locator('[data-skill-row="demo-e2e"]').count()) return
  const upload = readFileSync(fixtureZip, null)
  await page.click('[data-testid="skills-offline-install"]')
  await page.setInputFiles('[data-testid="skills-offline-file"]', {
    name: 'demo-e2e.zip',
    mimeType: 'application/zip',
    buffer: Buffer.from(upload.buffer as ArrayBuffer, upload.byteOffset, upload.byteLength),
  })
  await page.click('[data-testid="skills-install-submit"]')
  await expect(page.locator('[data-skill-row="demo-e2e"]')).toBeVisible({ timeout: 15_000 })
}

test('the workshop opens as the chat page: greeting, composer, workbench', async ({ page }) => {
  await signIn(page)
  await page.goto('/workshop?mode=create&scope=agent', { waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="workshop-greeting"]')).toHaveText(/想做一个什么技能|What skill do you want to build/)
  await expect(page.locator('[data-testid="workshop-composer"] textarea')).toBeVisible()

  // The composer's "+" opens the add sheet as a menu off the button itself
  // (the 2026-10-23 redesign): its own width, never past the composer that
  // holds it, its left edge on the button's.
  const plus = page.locator('[data-testid="workshop-composer"] [data-testid="composer-plus"]')
  await plus.click()
  const sheet = page.locator('[data-testid="workshop-composer"] .forebrain-plus-pop')
  await expect(sheet).toBeVisible()
  const sheetBox = await sheet.boundingBox()
  const composerBox = await page.locator('[data-testid="workshop-composer"] [data-slot="input-group"]').boundingBox()
  const plusBox = await plus.boundingBox()
  expect((sheetBox?.width ?? 0)).toBeGreaterThanOrEqual(260)
  expect((sheetBox?.width ?? 0)).toBeLessThanOrEqual(440)
  expect((sheetBox?.width ?? 0)).toBeLessThanOrEqual(composerBox?.width ?? 0)
  expect(Math.abs((sheetBox?.x ?? 0) - (plusBox?.x ?? 0))).toBeLessThanOrEqual(2)
  expect((sheetBox?.x ?? 0)).toBeGreaterThanOrEqual((composerBox?.x ?? 0) - 2)
  expect((sheetBox?.x ?? 0) + (sheetBox?.width ?? 0))
    .toBeLessThanOrEqual((composerBox?.x ?? 0) + (composerBox?.width ?? 0) + 2)
  await plus.click()
  await expect(sheet).toHaveCount(0)

  // The workbench carries the four skill tabs, disabled until a skill is chosen.
  await page.click('[data-testid="workshop-workbench-toggle"]')
  await expect(page.locator('[data-testid="chat-workbench"]')).toBeVisible()
  await expect(page.locator('[data-testid="bench-tab-overview"]')).toBeVisible()
  await revealTab(page, 'cases')
  await expect(page.locator('[data-testid="bench-tab-cases"]')).toBeDisabled()
  await revealTab(page, 'evals')
  await expect(page.locator('[data-testid="bench-tab-evals"]')).toBeDisabled()
  await revealTab(page, 'trigger')
  await expect(page.locator('[data-testid="bench-tab-trigger"]')).toBeDisabled()
  await revealTab(page, 'versions')
  await expect(page.locator('[data-testid="bench-tab-versions"]')).toBeDisabled()
  await shot(page, 'workshop-empty')
})

test('the skills page opens the workshop for create and for discover', async ({ page }) => {
  await signIn(page)
  await page.goto('/skills', { waitUntil: 'networkidle' })
  await page.click('[data-testid="skills-create"]')
  await expect(page).toHaveURL(/\/workshop/)
  await expect(page.locator('[data-testid="workshop-crumb-leaf"]')).toHaveText(/新建技能|New skill/)

  await page.goto('/skills', { waitUntil: 'networkidle' })
  await page.click('[data-testid="skills-discover"]')
  await expect(page).toHaveURL(/\/workshop/)
  await expect(page.locator('[data-testid="workshop-crumb-leaf"]')).toHaveText(/发现并安装|Discover and install/)
})

test('an installed skill shows its version and the workshop publish button', async ({ page }) => {
  test.skip(!fixtureZip, 'E2E_SKILL_ZIP fixture missing')
  await signIn(page)
  await ensureDemoSkill(page)

  // The row carries its installed version v1.
  const row = page.locator('[data-skill-row="demo-e2e"]')
  await expect(row).toContainText('v1')
  await expect(page.locator('[data-testid="skill-toggle-demo-e2e"]')).toBeVisible()

  // "Detail" opens the workshop named after the skill.
  await page.click('[data-testid="skill-detail-demo-e2e"]')
  await expect(page).toHaveURL(/\/workshop\?.*skill=demo-e2e/)
  await expect(page.locator('[data-testid="workshop-crumb-leaf"]')).toHaveText('demo-e2e')

  // No draft yet: publish is disabled, the skill tabs are live.
  await expect(page.locator('[data-testid="workshop-publish"]')).toBeDisabled()
  await page.click('[data-testid="workshop-workbench-toggle"]')
  await revealTab(page, 'cases')
  await expect(page.locator('[data-testid="bench-tab-cases"]')).toBeEnabled()
  await revealTab(page, 'versions')
  await page.click('[data-testid="bench-tab-versions"]')
  await expect(page.locator('[data-testid="chat-workbench"]')).toContainText('v1')
  await shot(page, 'workshop-skill')
})

test('a draft shows unpublished changes and the versions tab compares', async ({ page }) => {
  test.skip(!fixtureZip, 'E2E_SKILL_ZIP fixture missing')
  await signIn(page)
  await ensureDemoSkill(page)

  // A change lands in the draft (plan 036): writing the formal path redirects
  // there, so the skill now has unpublished changes on top of v1.
  const res = await page.request.put('/api/skills/demo-e2e/file?path=SKILL.md', {
    data: { content: '---\nname: demo-e2e\ndescription: e2e offline install fixture, draft\n---\n\nbody\n\nmore\n' },
  })
  expect(res.ok()).toBeTruthy()

  try {
    await page.goto('/skills', { waitUntil: 'networkidle' })
    await expect(page.locator('[data-skill-row="demo-e2e"]')).toContainText(/有未发布的修改|Unpublished changes/)

    await page.click('[data-testid="skill-detail-demo-e2e"]')
    await expect(page).toHaveURL(/\/workshop\?.*skill=demo-e2e/)
    // Publish is enabled and names the next version.
    const publish = page.locator('[data-testid="workshop-publish"]')
    await expect(publish).toBeEnabled()
    await expect(publish).toContainText('v2')

    // The versions tab lists the draft against v1 and offers a compare.
    await page.click('[data-testid="workshop-workbench-toggle"]')
    await revealTab(page, 'versions')
    await page.click('[data-testid="bench-tab-versions"]')
    await expect(page.locator('[data-testid="workshop-version-draft"]')).toBeVisible()
    await page.click('[data-testid="workshop-draft-compare"]')
    await expect(page.locator('[role="dialog"]')).toContainText('SKILL.md')
    // The compare draws the selected file's lines in the one diff style
    // (plan 144): removed and added rows, not a line count.
    const pane = page.locator('[data-testid="workshop-compare-pane"]')
    await expect(pane.locator('[data-testid="diff-block"]')).toBeVisible()
    await expect(pane.locator('.dr.del')).toHaveCount(1)
    await expect(pane.locator('.dr.del')).toContainText('e2e offline install fixture')
    await expect(pane.locator('.dr.add')).toHaveCount(3)
    await expect(pane.locator('.dr.add').last()).toContainText('more')
    await shot(page, 'workshop-draft')
  } finally {
    // Restore demo-e2e for the tests that follow.
    await page.request.post('/api/skills/demo-e2e/draft/discard')
  }
})

test('a from-scratch task gets a model answer (real LLM)', async ({ page }) => {
  test.skip(!realLLM, 'needs E2E_REAL_LLM=1 with real provider credentials')
  // A real workshop turn reads files and may call tools before it answers.
  test.setTimeout(480_000)
  await signIn(page)
  await page.goto('/workshop?mode=create&scope=agent', { waitUntil: 'networkidle' })
  const composer = page.locator('[data-testid="workshop-composer"] textarea')
  await composer.fill('把一个排障步骤做成技能：先确认没有发布，再看 pod 与健康检查。')
  await composer.press('Enter')
  // The turn ran to its end with the workshop skill active: the model answered
  // in words (not a placeholder), and no error surfaced.
  await expect(page.locator('[data-workshop-message="assistant"]').last()).toContainText(/[^.…\s]{2,}/, { timeout: 150_000 })
  await expect(page.locator('[data-testid="workshop-error"]')).toHaveCount(0)
  await shot(page, 'workshop-real-llm')
})

test('a workshop turn hangs on the activity line, not in a box', async ({ page }) => {
  test.skip(realLLM, 'the fake provider answers the turn deterministically')
  await signIn(page)
  await page.goto('/workshop?mode=create&scope=agent', { waitUntil: 'networkidle' })
  const composer = page.locator('[data-testid="workshop-composer"] textarea')
  await composer.fill('做一个检查发布的技能')
  await composer.press('Enter')

  // The answer hangs on the activity line — the same line the conversation
  // draws — on the brand mark's node, with no box around the turn (owner
  // 2026-10-25: the workshop is the conversation page's superset).
  const thread = page.locator('[data-workshop-message="assistant"]').last()
  await expect(thread.locator('.activity-line')).toHaveCount(1, { timeout: 30_000 })
  await expect(thread.locator('.activity-node--say')).toHaveCount(1)
  await shot(page, 'workshop-activity-line')
})
