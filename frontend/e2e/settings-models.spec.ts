import { expect, test, type Page } from '@playwright/test'

import { seedProjectSession, shot, signIn } from './support'

/**
 * Settings · Models (plan 266): the install's one table of models, added,
 * duplicated, refused and deleted through the page that owns it.
 *
 * The page replaces the old per-agent 「模型服务」 list, so the cases here are the
 * ones that list owned plus the new identity rule: four fields name a row, and a
 * second row with the same four is refused with a way to open the first. Rows a
 * case only needs as a backdrop are seeded through the API — the gateway is the
 * same one the page talks to, and a seed says what it means in one request.
 */

test.describe.configure({ mode: 'serial' })

const MODELS = '/settings?tab=models'

/** The product's own copy is Chinese-first; these assertions quote it. */
async function preferChinese(page: Page): Promise<void> {
  await page.addInitScript(() => {
    try {
      localStorage.setItem('forebrain-locale', 'zh')
    } catch {
      // A context with no storage still gets the page's own default.
    }
  })
}

/** The page, signed in, standing on the models tab. */
async function openModels(page: Page): Promise<void> {
  await preferChinese(page)
  await signIn(page)
  await page.goto(MODELS, { waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="models-add"]')).toBeVisible()
}

/** One model row, by the model name the table shows. */
function rowOf(page: Page, model: string) {
  return page.locator('[data-model-row]').filter({ hasText: model })
}

/** Seeds a row through the API and answers its id. */
async function seedModel(page: Page, body: Record<string, unknown>): Promise<string> {
  const res = await page.request.post('/api/llm-models', { data: body })
  expect(res.ok(), await res.text()).toBeTruthy()
  const created = (await res.json()) as { id?: string }
  const id = String(created.id ?? '')
  expect(id).not.toBe('')
  return id
}

test('adds a built-in model through the drawer, then refuses the same four fields again', async ({ page }) => {
  await openModels(page)

  await page.click('[data-testid="models-add"]')
  // The drawer, and its title says which action opened it.
  await expect(page.locator('.forebrain-drawer-head b')).toHaveText('添加模型')
  await page.click('[data-testid="models-provider-trigger"]')
  await page.click('[data-provider="volcengine"]')
  // The registry's first endpoint fills the address and the protocol.
  await expect(page.locator('[data-testid="models-base-url"]')).toHaveValue(
    'https://ark.cn-beijing.volces.com/api/coding',
  )
  await expect(page.locator('[data-testid="models-request-line"]')).toContainText('/v1/messages')
  await page.fill('[data-testid="models-api-key"]', 'ark-e2e-models-0001')
  await page.fill('[data-testid="models-model-name"]', 'doubao-e2e-pro')
  await shot(page, 'models-drawer-add-builtin')
  await page.click('[data-testid="models-save"]')

  const row = rowOf(page, 'doubao-e2e-pro')
  await expect(row).toBeVisible()
  // The provider's name follows the page's language, so the row is asserted on
  // the facts that never move: the address, the path and the key's tail.
  await expect(row).toContainText('https://ark.cn-beijing.volces.com/api/coding')
  await expect(row).toContainText('/v1/messages')
  // The key column is a mask: bullets and the last four, never the value.
  await expect(row.locator('[data-testid="models-cell-key"]')).toContainText('0001')
  await expect(row.locator('[data-testid="models-cell-key"]')).not.toContainText('ark-e2e-models')
  await shot(page, 'models-table-after-add')

  // The same four fields again: one refusal, naming the row that has them.
  await page.click('[data-testid="models-add"]')
  await page.click('[data-testid="models-provider-trigger"]')
  await page.click('[data-provider="volcengine"]')
  await page.fill('[data-testid="models-api-key"]', 'ark-e2e-models-0002')
  await page.fill('[data-testid="models-model-name"]', 'doubao-e2e-pro')
  await page.click('[data-testid="models-save"]')
  await expect(page.locator('[data-testid="models-duplicate"]')).toBeVisible()
  await shot(page, 'models-drawer-duplicate')

  // 「去编辑那一条」 closes the drawer, finds the row and opens it for editing.
  await page.click('[data-testid="models-goto-duplicate"]')
  await expect(page.locator('.forebrain-drawer-head b')).toHaveText('编辑模型')
  await expect(page.locator('[data-testid="models-model-name"]')).toHaveValue('doubao-e2e-pro')
  await shot(page, 'models-drawer-edit')
  await page.click('[data-testid="models-cancel"]')

  // And it is the same row: deleting it leaves the table without it, so the
  // duplicate never landed.
  await expect(page.locator('[data-testid="models-duplicate"]')).toHaveCount(0)
})

test('a custom model cannot be saved without a protocol', async ({ page }) => {
  await openModels(page)

  await page.click('[data-testid="models-add"]')
  await page.click('[data-testid="models-provider-trigger"]')
  await shot(page, 'models-drawer-provider-list')
  await page.click('[data-provider="custom"]')
  await page.fill('[data-testid="models-base-url"]', 'https://gateway.e2e.test/llm')
  await page.fill('[data-testid="models-api-key"]', 'sk-e2e-custom-7788')
  await page.fill('[data-testid="models-model-name"]', 'e2e/custom-model')

  // No protocol chosen: nothing can be sent, and the button says so.
  await expect(page.locator('[data-testid="models-save"]')).toBeDisabled()
  await shot(page, 'models-drawer-custom-no-protocol')

  // 「自定义协议」 is the custom provider's fourth choice: the address is the
  // whole request, and the line above says so instead of appending a path.
  await page.click('[data-testid="models-protocol-trigger"]')
  await page.click('[data-protocol="@verbatim"]')
  await expect(page.locator('[data-testid="models-request-line"]')).toContainText('实际请求（原样）')
  await expect(page.locator('[data-testid="models-request-line"]')).not.toContainText('/chat/completions')
  await shot(page, 'models-drawer-custom-verbatim')

  await expect(page.locator('[data-testid="models-save"]')).toBeEnabled()
  await page.click('[data-testid="models-save"]')

  const row = rowOf(page, 'e2e/custom-model')
  await expect(row).toBeVisible()
  await expect(row.locator('[data-testid="models-cell-path"]')).toHaveText('自定义协议')
  await expect(row).toContainText('https://gateway.e2e.test/llm')
})

test('the default model cannot be deleted, and the page names the agent that holds it', async ({ page }) => {
  // The seed writes through the gateway as this page: the session comes first.
  await preferChinese(page)
  await signIn(page)
  const id = await seedModel(page, {
    provider: 'custom',
    model: 'e2e-default-model',
    base_url: 'https://gateway.e2e.test/default',
    api_path: '/chat/completions',
    api_key: 'sk-e2e-default-4455',
  })
  const made = await page.request.put(`/api/llm-models/${encodeURIComponent(id)}/default`)
  expect(made.ok(), await made.text()).toBeTruthy()

  await page.goto(MODELS, { waitUntil: 'networkidle' })
  const row = rowOf(page, 'e2e-default-model')
  await expect(row).toBeVisible()
  await expect(row.locator('[data-testid="models-cell-default"]')).toBeVisible()

  await row.locator('[data-testid^="models-remove-"]').click()
  await shot(page, 'models-delete-confirm')
  await page.click('[data-testid="confirm-ok"]')

  // The refusal is the server's own list of agents, shown as badges.
  await expect(page.getByText('不能删除')).toBeVisible()
  await expect(page.locator('[data-testid="models-refused-agent"]').first()).toBeVisible()
  await shot(page, 'models-delete-refused')
  await page.click('[data-testid="models-refused-ok"]')
  await expect(row).toBeVisible()
})

test('a model stored on the page appears in the conversation’s model menu, twins and all', async ({
  page,
}) => {
  // Two rows for one model name, each against its own gateway: a provider and a
  // model name cannot tell them apart, which is why the menu writes each
  // option's address. The rows are stored through the same API the page uses, so
  // this also pins the write's adoption: the runtime snapshot the menu reads
  // has to carry them, or the menu would offer a model the table says exists.
  await preferChinese(page)
  await signIn(page)
  for (const [base, key] of [
    ['https://eu.e2e.test/v1', 'sk-e2e-twin-eu'],
    ['https://us.e2e.test/v1', 'sk-e2e-twin-us'],
  ]) {
    await seedModel(page, {
      provider: 'custom',
      model: 'e2e-twin-model',
      base_url: base,
      api_path: '/chat/completions',
      api_key: key,
    })
  }

  await page.goto(MODELS, { waitUntil: 'networkidle' })
  await expect(rowOf(page, 'e2e-twin-model')).toHaveCount(2)

  // The runtime's own view answers with both rows right away: the write adopted
  // the configuration instead of leaving the snapshot on what the process
  // started with.
  await expect
    .poll(
      async () => {
        const res = await page.request.get('/api/chat/model')
        const body = (await res.json()) as { options?: { model?: string }[] }
        return (body.options ?? []).filter((option) => option.model === 'e2e-twin-model').length
      },
      { timeout: 20_000 },
    )
    .toBe(2)

  const sessionId = await seedProjectSession(page, 'e2e-models')
  await page.goto(`/?session=${sessionId}`, { waitUntil: 'networkidle' })
  const chip = page.locator('[data-testid="model-chip"]')
  await expect(chip).toBeEnabled({ timeout: 15_000 })
  await chip.click()

  // Both rows are offered, and each one carries the address that tells it from
  // its twin — that is the whole point of the signature.
  const options = page.locator('[data-testid="model-option-e2e-twin-model"]')
  await expect(options).toHaveCount(2)
  await expect(options.nth(0)).toContainText('eu.e2e.test/v1')
  await expect(options.nth(0)).toContainText('/chat/completions')
  await expect(options.nth(1)).toContainText('us.e2e.test/v1')
  await expect(options.nth(1)).toContainText('/chat/completions')
  await shot(page, 'models-chat-menu-twins')
})

test('the settings page keeps the two other global surfaces: the memory models and the variables', async ({
  page,
}) => {
  await preferChinese(page)
  await signIn(page)

  // 设置 · 记忆: both model rows are the one picker — 「当前会话的模型」 plus the
  // install's configured rows, each written as its signature.
  await page.goto('/settings?tab=memory', { waitUntil: 'networkidle' })
  // The two model rows belong to the groups that follow the switch: with memory
  // off there is nothing to point at a model.
  await page.getByRole('switch', { name: '记忆功能' }).click()
  await expect(page.locator('[data-field="memories.extract_model"]')).toBeVisible()
  await page.locator('[data-field="memories.extract_model"] button').click()
  await expect(page.locator('[role="option"]').first()).toBeVisible()
  await shot(page, 'models-settings-memory')

  // 设置 · 变量: the table, its empty state, and the two-field dialog.
  await page.goto('/settings?tab=vars', { waitUntil: 'networkidle' })
  await shot(page, 'models-settings-vars')
  await page.click('[data-testid="vars-add"]')
  await page.fill('[data-testid="vars-name"]', 'E2E_TOKEN')
  await page.fill('[data-testid="vars-value"]', 'e2e-value-1234')
  await shot(page, 'models-settings-var-dialog')
  await page.click('[data-testid="vars-save"]')

  const row = page.locator('[data-var-row="E2E_TOKEN"]')
  await expect(row).toBeVisible()
  await expect(row).toContainText('1234')
  await expect(row).not.toContainText('e2e-value')
  await shot(page, 'models-settings-vars-filled')
})
