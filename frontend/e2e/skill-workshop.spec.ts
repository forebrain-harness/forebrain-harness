import { expect, test } from '@playwright/test'
import { readFileSync } from 'node:fs'

import { shot, signIn } from './support'

/**
 * The skill workshop: a workshop conversation on the left, the skill's files
 * on the right, and workshop tasks that never leak into the chat drawer.
 */

const fixtureZip = process.env.E2E_SKILL_ZIP ?? ''
const realLLM = process.env.E2E_REAL_LLM === '1'

test('the workshop page is reachable with an empty task list', async ({ page }) => {
  await signIn(page)
  await page.goto('/workshop', { waitUntil: 'networkidle' })
  await expect(page.getByRole('heading', { name: /技能工坊|Skill workshop/ })).toBeVisible()
  await expect(page.getByText(/还没有工坊任务|No workshop tasks yet/)).toBeVisible()
  await shot(page, 'workshop-empty')
})

test('an improve task on a built-in skill shows read-only files', async ({ page }) => {
  await signIn(page)
  await page.goto('/workshop', { waitUntil: 'networkidle' })
  await page.click('[data-testid="workshop-new-task"]')
  await page.selectOption('[data-testid="workshop-task-kind"]', 'improve')
  // The built-in skill-workshop itself is a legitimate improve target.
  const option = page.locator('[data-testid="workshop-improve-name"] option', { hasText: 'skill-workshop' })
  await expect(option).toHaveCount(1)
  await page.selectOption('[data-testid="workshop-improve-name"]', 'skill-workshop')
  await page.fill('[data-testid="workshop-purpose"]', 'e2e improve run')
  await page.click('[data-testid="workshop-create-task"]')

  // The task exists in the workshop list and the panel shows the skill's own
  // directory — read-only, because .system is replaced on upgrade.
  await expect(page.locator('[data-testid="workshop-tasks"] button')).toHaveCount(1, { timeout: 15_000 })
  await expect(page.locator('[data-workshop-file="SKILL.md"]')).toBeVisible({ timeout: 15_000 })
  await page.click('[data-workshop-file="SKILL.md"]')
  await expect(page.getByText(/内置技能只读|Built-in skills are read-only/).first()).toBeVisible()

  // The workshop task never appears in the chat drawer.
  await page.goto('/', { waitUntil: 'networkidle' })
  await page.click('button:has-text("对话"), button:has-text("Chat")')
  await expect(page.locator('.chat-drawer')).toBeVisible()
  await expect(page.locator('.chat-drawer').getByText(/skill-workshop/)).toHaveCount(0)
  await shot(page, 'workshop-conversation')
})

test('an owned skill is editable from the panel and downloadable', async ({ page }) => {
  test.skip(!fixtureZip, 'E2E_SKILL_ZIP fixture missing')
  // Install a writable skill first (idempotent across the shared gateway).
  const upload = readFileSync(fixtureZip, null)
  await signIn(page)
  await page.goto('/skills', { waitUntil: 'networkidle' })
  if (!(await page.locator('[data-skill-row="demo-e2e"]').count())) {
    await page.click('[data-testid="skills-offline-install"]')
    await page.setInputFiles('[data-testid="skills-offline-file"]', {
      name: 'demo-e2e.zip',
      mimeType: 'application/zip',
      buffer: Buffer.from(upload.buffer as ArrayBuffer, upload.byteOffset, upload.byteLength),
    })
    await page.click('[data-testid="skills-install-submit"]')
    await expect(page.locator('[data-skill-row="demo-e2e"]')).toBeVisible({ timeout: 15_000 })
  }

  await page.goto('/workshop', { waitUntil: 'networkidle' })
  await page.click('[data-testid="workshop-new-task"]')
  await page.selectOption('[data-testid="workshop-task-kind"]', 'improve')
  await page.selectOption('[data-testid="workshop-improve-name"]', 'demo-e2e')
  await page.fill('[data-testid="workshop-purpose"]', 'e2e editable panel')
  await page.click('[data-testid="workshop-create-task"]')
  await expect(page.locator('[data-workshop-file="SKILL.md"]')).toBeVisible({ timeout: 15_000 })

  // A new file round-trips through the panel's editor.
  await page.request.put('/api/skills/demo-e2e/file?path=notes/e2e.md', { data: { content: 'first' } })
  await page.click('[data-testid="workshop-refresh"]')
  await page.click('[data-workshop-file="notes/e2e.md"]')
  await page.fill('[data-testid="workshop-file-editor"]', 'panel edit')
  await page.click('[data-testid="workshop-file-save"]')
  await expect(page.locator('[data-testid="workshop-file-saved"]')).toBeVisible({ timeout: 10_000 })
  await page.click('[data-testid="workshop-refresh"]')
  await page.click('[data-workshop-file="notes/e2e.md"]')
  await expect(page.locator('[data-testid="workshop-file-editor"]')).toHaveValue('panel edit')
  await shot(page, 'workshop-files')

  const downloadPromise = page.waitForEvent('download')
  await page.click('[data-testid="workshop-download"]')
  const download = await downloadPromise
  expect(download.suggestedFilename()).toBe('demo-e2e.zip')
})

test('a from-scratch task gets a model answer (real LLM)', async ({ page }) => {
  test.skip(!realLLM, 'needs E2E_REAL_LLM=1 with real provider credentials')
  // A real workshop turn reads files and may call tools before it answers.
  test.setTimeout(480_000)
  await signIn(page)
  await page.goto('/workshop', { waitUntil: 'networkidle' })
  await page.click('[data-testid="workshop-new-task"]')
  await page.fill('[data-testid="workshop-new-name"]', 'e2e-demo-skill')
  await page.fill('[data-testid="workshop-purpose"]', 'greet the user warmly when they say hi')
  await page.click('[data-testid="workshop-create-task"]')
  // The turn ran to its end with the workshop skill active: the model
  // answered in words (not a placeholder), and no error surfaced — a skill
  // that failed to load would end the turn with one.
  await expect(page.locator('[data-workshop-message="assistant"]').last()).toContainText(/[^.…\s]{2,}/, { timeout: 150_000 })
  // The turn may park on gates — a tool approval or a question. Each is
  // decided right here, with the chat page's own controls, and deciding one
  // has to set the run going again: it reaches its end, or the next gate.
  const decided = new Set<string>()
  const deadline = Date.now() + 420_000
  for (;;) {
    expect(Date.now(), 'the workshop turn neither finished nor resumed after a decision').toBeLessThan(deadline)
    const gates = page.locator('[data-testid="pending-approval"], [data-testid="pending-question"]')
    const ids = await gates.evaluateAll((rows) => rows.map((row) => row.getAttribute('data-action-id') ?? ''))
    if (decided.size && ids.some((id) => !decided.has(id))) break
    const open = ids.find((id) => !decided.has(id))
    if (open) {
      const gate = page.locator(`[data-action-id="${open}"]`)
      if (await gate.getAttribute('data-testid') === 'pending-question') {
        for (const group of await gate.locator('.space-y-1').all()) {
          await group.locator('input[type="radio"], input[type="checkbox"]').first().check({ timeout: 5_000 }).catch(() => undefined)
        }
        await gate.getByRole('button', { name: /提交|Submit/ }).click({ timeout: 5_000 })
      } else {
        await gate.locator('[data-testid="pending-approve"], [data-testid="pending-decision-accept"]').first().click({ timeout: 5_000 })
      }
      decided.add(open)
      continue
    }
    const sending = /加载中|Loading/.test(await page.locator('[data-testid="workshop-send"]').textContent() ?? '')
    if (!sending && !ids.length) break
    await page.waitForTimeout(1000)
  }
  // Neither a page-level failure nor a run that ended in one.
  await expect(page.locator('[data-testid="workshop-error"]')).toHaveCount(0)
  await expect(page.locator('[data-testid="run-error"]')).toHaveCount(0)
  await shot(page, 'workshop-real-llm')
})

test('the skills page links into the workshop', async ({ page }) => {
  await signIn(page)
  await page.goto('/skills', { waitUntil: 'networkidle' })
  await page.click('[data-testid="skills-workshop-hint"]')
  await expect(page).toHaveURL(/\/workshop$/)
})
