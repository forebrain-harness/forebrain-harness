import { readFileSync } from 'node:fs'

import { expect, test, type Page, type Response } from '@playwright/test'

import { pickOption, seedProjectSession, shot, signIn } from './support'

/**
 * The subagent configuration page (plan 079): the menu bar's 子代理 page is the
 * primary agent's configuration list, not the roster of what is running. The
 * table, the order, the paging and the search are the server's, so every case
 * seeds through the gateway's own write endpoints and then reads back what a
 * person would see — plus, where the case is about what was stored, the raw
 * answer of the detail endpoint and of `GET /api/config` (the file's own text).
 *
 * The subagents the harness ships are fixed: the four the model may dispatch,
 * then the six the system dispatches on its own occasions. The seeds are this
 * agent's custom subagents and are removed in each case's `finally` — the
 * gateway and its configuration file are shared with every other spec in the
 * run.
 */

/** The built-ins the model may dispatch, in the order the page lists them. */
const BUILTINS = ['general-purpose', 'explore', 'plan', 'verification']
/** The ones the system dispatches: nobody configures them, and they come last. */
const SYSTEM = ['plan-reviewer', 'goal-evaluator', 'guardian', 'cavecrew-investigator', 'cavecrew-builder', 'cavecrew-reviewer']

/** The run's own skill fixture, installed where a case needs a skill to open. */
const SKILL_ZIP = process.env.E2E_SKILL_ZIP ?? ''
const SKILL = 'demo-e2e'

/** A reasoning effort's label as the page writes it (lib/effortLabel.ts). */
const EFFORT_LABEL: Record<string, string> = { low: '低', medium: '中', high: '高', xhigh: '最高' }

/** One primary agent's subagents live here; the tail names one entry. */
function agentSubagents(agentId: string, tail = ''): string {
  const base = `/api/agents/primary/${encodeURIComponent(agentId)}/subagents`
  return tail === '' ? base : `${base}/${encodeURIComponent(tail)}`
}

/** The shared MCP list: one entry of each transport for the MCP page's columns. */
const MCP_SHARED = '/api/v1/mcp/servers'
const MCP_STDIO = 'e2e-079-stdio'
const MCP_HTTP = 'e2e-079-http'

/** The primary agent the shell is on: everything here is keyed by its id. */
async function activeAgentId(page: Page): Promise<string> {
  const answer = await page.request.get('/api/agents/primary')
  if (!answer.ok()) throw new Error(`primary agents failed: ${answer.status()} ${await answer.text()}`)
  const body = (await answer.json()) as { active_id?: string }
  const id = String(body.active_id ?? '')
  if (id === '') throw new Error('no active primary agent to configure subagents for')
  return id
}

/** One write through the gateway's own endpoint, read back as its raw answer. */
async function write(
  page: Page,
  method: 'POST' | 'PUT' | 'DELETE',
  path: string,
  data?: Record<string, unknown>,
): Promise<Record<string, unknown>> {
  const answer = await page.request.fetch(path, { method, data })
  if (!answer.ok()) throw new Error(`${method} ${path} failed: ${answer.status()} ${await answer.text()}`)
  const text = await answer.text()
  return text === '' ? {} : (JSON.parse(text) as Record<string, unknown>)
}

/** One subagent as the drawer opens it: the prompt in force, the harness's own
 * original behind it, and every field the write round trip has to keep. */
async function subagentDetail(page: Page, agentId: string, name: string): Promise<Record<string, unknown>> {
  const answer = await page.request.get(agentSubagents(agentId, name))
  if (!answer.ok()) throw new Error(`subagent ${name} failed: ${answer.status()} ${await answer.text()}`)
  return (await answer.json()) as Record<string, unknown>
}

/** The configuration file's own text, as `GET /api/config` serves it. */
async function configYAML(page: Page): Promise<string> {
  const answer = await page.request.get('/api/config')
  if (!answer.ok()) throw new Error(`config failed: ${answer.status()} ${await answer.text()}`)
  return String(((await answer.json()) as { yaml?: string }).yaml ?? '')
}

/** The models this agent may pin a subagent to, each with the efforts it takes. */
async function modelOptions(page: Page, agentId: string): Promise<{ key: string; efforts: string[] }[]> {
  const answer = await page.request.get(`/api/agents/primary/${encodeURIComponent(agentId)}/model-options`)
  if (!answer.ok()) throw new Error(`model options failed: ${answer.status()} ${await answer.text()}`)
  const body = (await answer.json()) as { options?: { provider?: string; model?: string; efforts?: string[] }[] }
  return (body.options ?? []).map((option) => ({
    key: `${option.provider ?? ''}/${option.model ?? ''}`,
    efforts: Array.isArray(option.efforts) ? option.efforts : [],
  }))
}

/** The rows as drawn, in order: each row's `data-subagent-row` is its name. */
async function rowNames(page: Page): Promise<string[]> {
  return page
    .locator('[data-subagent-row]')
    .evaluateAll((nodes) => nodes.map((node) => node.getAttribute('data-subagent-row') ?? ''))
}

/** One column of the table as text, trimmed: 1 is the name, 2 the kind, 3 the
 * model, 4 the tools. */
async function columnTexts(page: Page, column: number): Promise<string[]> {
  return page
    .locator(`[data-subagent-row] td:nth-child(${column})`)
    .evaluateAll((nodes) => nodes.map((node) => (node as HTMLElement).innerText.trim()))
}

/** The list's own read: what a write reloads, and what every switch waits for. */
function isSubagentList(response: Response): boolean {
  return response.request().method() === 'GET' && new URL(response.url()).pathname.endsWith('/subagents')
}

/** The reload a write triggers, waited for before anything is read off the page. */
function nextListLoad(page: Page): Promise<Response> {
  return page.waitForResponse(isSubagentList, { timeout: 30_000 })
}

/** One switch pressed with its write waited for: the page holds the server's
 * answer, so the new state arrives with the reload the write triggers. */
async function flip(page: Page, target: ReturnType<Page['locator']>, next: boolean): Promise<void> {
  await expect(target).toBeEnabled({ timeout: 20_000 })
  const reload = nextListLoad(page)
  await target.click()
  await reload
  await expect(target).toHaveAttribute('aria-checked', String(next), { timeout: 20_000 })
}

function rowSwitch(page: Page, name: string) {
  return page.locator(`[data-testid="subagent-switch-${name}"]`)
}

/** The 操作 header's own alignment, read from the element the browser laid out. */
async function actionHeaderAlign(page: Page): Promise<string> {
  return page
    .locator('main thead th', { hasText: '操作' })
    .evaluate((node) => getComputedStyle(node as HTMLElement).textAlign)
}

/** The workbench is one column at one width; a case reads the width it got. */
async function workbenchWidth(page: Page): Promise<number> {
  const box = await page.locator('[data-testid="chat-workbench"]').boundingBox()
  return box?.width ?? 0
}

/** The tail of the tab bar: the gap between the last tab drawn and the right
 * arrow, which is what keeps the arrow at the far edge instead of inside the
 * strip. */
async function tabToArrowGap(page: Page): Promise<number> {
  return page.evaluate(() => {
    const tabs = Array.from(document.querySelectorAll('.bench-tab-strip [role="tab"]')) as HTMLElement[]
    const next = document.querySelector('[data-testid="bench-tabs-next"]') as HTMLElement | null
    if (tabs.length === 0) throw new Error('the tab strip draws no tab')
    if (!next) throw new Error('the tab strip has no right arrow')
    return next.getBoundingClientRect().left - tabs[tabs.length - 1].getBoundingClientRect().right
  })
}

/** One installed skill to open the workshop on: the run's own offline fixture,
 * installed once (the gateway is shared with every other spec). */
async function ensureSkill(page: Page): Promise<void> {
  if (SKILL_ZIP === '') test.skip(true, 'E2E_SKILL_ZIP fixture missing')
  await page.goto('/skills', { waitUntil: 'networkidle' })
  if (await page.locator(`[data-skill-row="${SKILL}"]`).count()) return
  const upload = readFileSync(SKILL_ZIP, null)
  await page.click('[data-testid="skills-offline-install"]')
  await page.setInputFiles('[data-testid="skills-offline-file"]', {
    name: `${SKILL}.zip`,
    mimeType: 'application/zip',
    buffer: Buffer.from(upload.buffer as ArrayBuffer, upload.byteOffset, upload.byteLength),
  })
  await page.click('[data-testid="skills-install-submit"]')
  await expect(page.locator(`[data-skill-row="${SKILL}"]`)).toBeVisible({ timeout: 20_000 })
}

test.beforeEach(async ({ page }) => {
  // The page's copy is the design's Chinese; pinning it keeps the assertions
  // reading the same words the table draws.
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
})

test('the page lists the agent’s subagents in configuration order, ten to a page', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  const agentId = await activeAgentId(page)
  const custom = 'e2e-079-custom'
  await write(page, 'POST', agentSubagents(agentId), {
    name: custom,
    description: 'E2E 079 种下的自定义子代理',
    system_prompt: 'E2E-079 custom prompt',
  })
  try {
    await page.goto('/subagents', { waitUntil: 'networkidle' })

    // Five columns and no 状态: whether a subagent is configured is the row's
    // switch, not a status this page reports.
    const headers = (await page.locator('main thead th').allInnerTexts()).map((text) => text.trim())
    expect(headers).toEqual(['子代理', '类型', '模型', '工具', '操作'])
    expect(headers).not.toContain('状态')

    // This agent's own first, then the four built-ins, then the system ones —
    // eleven rows, so the first page carries the first ten.
    await expect.poll(() => rowNames(page), { timeout: 20_000 }).toEqual([custom, ...BUILTINS, ...SYSTEM.slice(0, 5)])
    await expect(page.locator('[data-testid="table-pager"]')).toHaveCount(1)
    await expect(page.locator('[data-testid="pager-total"]')).toHaveText(/共 11 条/)

    // The kind each row is tells the rest of the table where it came from.
    expect(await columnTexts(page, 2)).toEqual(['自定义', '内置', '内置', '内置', '内置', '系统', '系统', '系统', '系统', '系统'])
    await page.mouse.move(2, 2)
    await shot(page, 'p079-list-page-1')

    // The eleventh row is the last system one, on its own page.
    await page.click('[data-testid="pager-page-2"]')
    await expect(page.locator('[data-testid="pager-page-2"]')).toHaveAttribute('aria-current', 'page')
    await expect.poll(() => rowNames(page), { timeout: 20_000 }).toEqual([SYSTEM[5]])
    await expect(page.locator('[data-testid="pager-total"]')).toHaveText(/共 11 条/)
    await shot(page, 'p079-list-page-2')
  } finally {
    await page.request.delete(agentSubagents(agentId, custom))
  }
})

test('editing a built-in stores its model, effort and prompt, and the table says so', async ({ page }) => {
  test.info().setTimeout(150_000)
  await signIn(page)
  const agentId = await activeAgentId(page)

  // A model that can reason: the effort select belongs to the model beside it.
  const options = await modelOptions(page, agentId)
  const pinnable = options.find((option) => option.efforts.length > 0)
  if (!pinnable) throw new Error(`no model with reasoning levels: ${JSON.stringify(options)}`)
  const effort = pinnable.efforts.includes('high') ? 'high' : pinnable.efforts[0]
  const label = EFFORT_LABEL[effort] ?? effort
  const prompt = 'E2E-079 explore prompt'

  try {
    const before = await subagentDetail(page, agentId, 'explore')
    const defaultPrompt = String(before.default_prompt ?? '')
    // The drawer's box is the harness's own text, word for word: a reader
    // comparing a rewrite against "see the source" cannot judge anything.
    expect(defaultPrompt.trim().length).toBeGreaterThan(0)

    await page.goto('/subagents', { waitUntil: 'networkidle' })
    await expect(page.locator('[data-testid="subagent-edit-explore"]')).toBeVisible({ timeout: 20_000 })
    await page.click('[data-testid="subagent-edit-explore"]')
    const drawer = page.locator('.forebrain-drawer')
    await expect(drawer).toBeVisible()
    await expect(drawer.locator('.forebrain-drawer-head b')).toHaveText('编辑内置子代理 · explore')
    await expect(drawer.locator('[data-testid="subagent-prompt"]')).toHaveValue(defaultPrompt)
    await expect(drawer.locator('[data-testid="subagent-overridden"]')).toHaveCount(0)

    const reload = nextListLoad(page)
    await drawer.locator('[data-testid="subagent-prompt"]').fill(prompt)
    await pickOption(page, 'subagent-model', pinnable.key)
    await pickOption(page, 'subagent-effort', label)
    await drawer.locator('[data-testid="subagent-save"]').click()
    await reload
    await expect(page.locator('.forebrain-drawer')).toHaveCount(0, { timeout: 20_000 })

    // The row now says what it will run on, and that its prompt is this
    // agent's own.
    const row = page.locator('[data-subagent-row="explore"]')
    await expect(row.locator('[data-testid="subagent-overridden-explore"]')).toBeVisible({ timeout: 20_000 })
    await expect(row.locator('td:nth-child(3)')).toContainText(pinnable.key)
    await expect(row.locator('td:nth-child(3)')).toContainText(`· ${label}`)
    await page.mouse.move(2, 2)
    await shot(page, 'p079-builtin-rewritten')

    // The file carries the same three settings: the answer of the write and the
    // configuration text are the two halves of "it saved".
    const detail = await subagentDetail(page, agentId, 'explore')
    expect(detail.prompt_overridden).toBe(true)
    expect(String(detail.system_prompt).trim()).toBe(prompt)
    expect(`${String(detail.model_provider)}/${String(detail.model)}`).toBe(pinnable.key)
    expect(detail.reasoning_effort).toBe(effort)

    const yaml = await configYAML(page)
    expect(yaml).toMatch(/system_prompt:\s*["']?E2E-079 explore prompt/)
    expect(yaml).toMatch(new RegExp(`model:\\s*["']?${pinnable.key.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`))
    expect(yaml).toMatch(new RegExp(`reasoning_effort:\\s*["']?${effort}`))
  } finally {
    // The override entry says nothing once both are cleared, so the built-in
    // goes back to following the harness.
    await page.request.put(agentSubagents(agentId, 'explore'), {
      data: { model: '', reasoning_effort: '', reset_prompt: true },
    })
  }
})

test('恢复默认 clears the rewrite and leaves the model alone', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  const agentId = await activeAgentId(page)
  const prompt = 'E2E-079 rewritten prompt to be restored'
  await write(page, 'PUT', agentSubagents(agentId, 'explore'), { system_prompt: prompt })
  try {
    await page.goto('/subagents', { waitUntil: 'networkidle' })
    const row = page.locator('[data-subagent-row="explore"]')
    await expect(row.locator('[data-testid="subagent-overridden-explore"]')).toBeVisible({ timeout: 20_000 })

    await page.click('[data-testid="subagent-edit-explore"]')
    const drawer = page.locator('.forebrain-drawer')
    await expect(drawer.locator('[data-testid="subagent-overridden"]')).toBeVisible()
    await expect(drawer.locator('[data-testid="subagent-prompt"]')).toHaveValue(prompt)

    // 恢复默认 puts the harness's own text back in the box and marks the save
    // as "clear the rewrite"; the badge goes with it.
    await drawer.locator('[data-testid="subagent-restore"]').click()
    await expect(drawer.locator('[data-testid="subagent-overridden"]')).toHaveCount(0)
    const defaultPrompt = String((await subagentDetail(page, agentId, 'explore')).default_prompt ?? '')
    await expect(drawer.locator('[data-testid="subagent-prompt"]')).toHaveValue(defaultPrompt)

    const reload = nextListLoad(page)
    await drawer.locator('[data-testid="subagent-save"]').click()
    await reload
    await expect(page.locator('.forebrain-drawer')).toHaveCount(0, { timeout: 20_000 })

    // The badge is gone, the prompt in force is the original again, and the
    // file no longer holds a rewrite at all.
    await expect(page.locator('[data-testid="subagent-overridden-explore"]')).toHaveCount(0, { timeout: 20_000 })
    const detail = await subagentDetail(page, agentId, 'explore')
    expect(detail.prompt_overridden).toBe(false)
    expect(String(detail.system_prompt)).toBe(defaultPrompt)
    expect(await configYAML(page)).not.toContain('system_prompt')
    await page.mouse.move(2, 2)
    await shot(page, 'p079-restored-default')
  } finally {
    await page.request.put(agentSubagents(agentId, 'explore'), {
      data: { model: '', reasoning_effort: '', reset_prompt: true },
    })
  }
})

test('a new custom subagent lands first, wearing the tools it was given', async ({ page }) => {
  test.info().setTimeout(150_000)
  await signIn(page)
  const agentId = await activeAgentId(page)
  const name = 'e2e-079-tools'
  try {
    await page.goto('/subagents', { waitUntil: 'networkidle' })
    await expect(page.locator('[data-testid="subagents-new"]')).toBeVisible({ timeout: 20_000 })
    await page.click('[data-testid="subagents-new"]')

    const drawer = page.locator('.forebrain-drawer')
    await expect(drawer.locator('.forebrain-drawer-head b')).toHaveText('新建子代理')
    await drawer.locator('[data-testid="subagent-name"]').fill(name)
    await drawer.locator('[data-testid="subagent-description"]').fill('E2E 079 的指定工具子代理')
    await drawer.locator('[data-testid="subagent-prompt"]').fill('E2E-079 custom prompt')

    // 指定工具 asks for a set: the picker lists what the runtime offers, minus
    // the subagent_* family a subagent may neither call nor dispatch. A box is
    // the library's Checkbox — a `role="checkbox"` button with the tool's name
    // in the label beside it — so the names are read off the labels and the
    // boxes are clicked in the same order.
    await drawer.locator('[role="radio"][value="pick"]').click()
    const picker = drawer.locator('[data-testid="subagent-picker"]')
    await expect(picker).toBeVisible()
    const boxes = picker.getByRole('checkbox')
    await expect(boxes.first()).toBeVisible({ timeout: 30_000 })
    const offered = await picker.locator('label').evaluateAll((nodes) => nodes.map((node) => (node.textContent ?? '').trim()))
    expect(offered.length).toBeGreaterThan(2)
    expect(offered.some((tool) => tool.startsWith('subagent_'))).toBe(false)
    const picks = offered.slice(0, 2)
    for (const tool of picks) await boxes.nth(offered.indexOf(tool)).click()
    await expect(drawer.locator('[data-testid="subagent-pick-note"]')).toContainText('已选 2 个')

    const reload = nextListLoad(page)
    await drawer.locator('[data-testid="subagent-save"]').click()
    await reload
    await expect(page.locator('.forebrain-drawer')).toHaveCount(0, { timeout: 20_000 })

    // First row, custom, and its tools named as a count.
    await expect.poll(async () => (await rowNames(page))[0] ?? '', { timeout: 20_000 }).toBe(name)
    const row = page.locator(`[data-subagent-row="${name}"]`)
    await expect(row.locator('td:nth-child(2)')).toHaveText('自定义')
    await expect(row.locator('td:nth-child(4)')).toHaveText('指定 2 个')
    await page.mouse.move(2, 2)
    await shot(page, 'p079-created-custom')

    // The file holds the tier and the two tools it names.
    const detail = await subagentDetail(page, agentId, name)
    expect(detail.tools).toBe('pick')
    expect([...((detail.allowed_tools as string[]) ?? [])].sort()).toEqual([...picks].sort())
    const yaml = await configYAML(page)
    expect(yaml).toContain(`name: ${name}`)
    expect(yaml).toMatch(/tools:\s*["']?pick/)
  } finally {
    await page.request.delete(agentSubagents(agentId, name))
  }
})

test('the page switch and the rows switch each other', async ({ page }) => {
  test.info().setTimeout(150_000)
  await signIn(page)
  const agentId = await activeAgentId(page)
  try {
    await page.goto('/subagents', { waitUntil: 'networkidle' })
    const pageSwitch = page.locator('[data-testid="subagents-switch"]')
    // The run's configuration has the feature on and this agent sets no switch
    // of its own, so the toolbar follows the global default.
    await expect(pageSwitch).toHaveAttribute('aria-checked', 'true', { timeout: 20_000 })
    for (const name of BUILTINS) await expect(rowSwitch(page, name)).toHaveAttribute('aria-checked', 'true')

    // Off: every row under it follows, since none of them can run without it.
    await flip(page, pageSwitch, false)
    for (const name of BUILTINS) await expect(rowSwitch(page, name)).toHaveAttribute('aria-checked', 'false')
    await page.mouse.move(2, 2)
    await shot(page, 'p079-switch-off')

    // One row back on: the feature has to be on for it to run, so the toolbar
    // follows the row.
    await flip(page, rowSwitch(page, 'explore'), true)
    await expect(pageSwitch).toHaveAttribute('aria-checked', 'true', { timeout: 20_000 })
    for (const name of BUILTINS.filter((builtin) => builtin !== 'explore')) {
      await expect(rowSwitch(page, name)).toHaveAttribute('aria-checked', 'false')
    }

    // The last row off is a list that dispatches nothing: the feature follows
    // it off.
    await flip(page, rowSwitch(page, 'explore'), false)
    await expect(pageSwitch).toHaveAttribute('aria-checked', 'false', { timeout: 20_000 })
    await page.mouse.move(2, 2)
    await shot(page, 'p079-switch-linkage')
  } finally {
    // Leave the run as it was found: the feature on, and every row with it.
    await page.request.put(`/api/agents/primary/${encodeURIComponent(agentId)}/subagent-switch`, { data: { enabled: true } })
  }
})

test('a system subagent opens read-only, with its prompt and only 关闭', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  await page.goto('/subagents', { waitUntil: 'networkidle' })

  // The search brings the row to the first page whatever else is configured.
  await page.fill('[data-testid="subagents-search"]', 'plan-reviewer')
  const view = page.locator('[data-testid="subagent-view-plan-reviewer"]')
  await expect(view).toBeVisible({ timeout: 20_000 })
  // Nothing about a system subagent is this agent's to switch.
  await expect(page.locator('[data-testid="subagent-switch-plan-reviewer"]')).toHaveCount(0)
  await expect(page.locator('[data-testid="subagent-edit-plan-reviewer"]')).toHaveCount(0)

  await view.click()
  const drawer = page.locator('.forebrain-drawer')
  await expect(drawer).toBeVisible()
  await expect(drawer.locator('.forebrain-drawer-head b')).toHaveText('查看系统子代理 · plan-reviewer')
  await expect(drawer.locator('.forebrain-drawer-head .forebrain-badge')).toContainText('只读')
  await expect(drawer.locator('[data-testid="subagent-prompt"]')).toHaveAttribute('readonly', '')
  const prompt = await drawer.locator('[data-testid="subagent-prompt"]').inputValue()
  expect(prompt.trim().length).toBeGreaterThan(0)
  // Read-only is the whole footer: one button, the one that closes it.
  await expect(drawer.locator('.forebrain-drawer-foot button')).toHaveCount(1)
  await expect(drawer.locator('[data-testid="subagent-cancel"]')).toHaveText('关闭')
  await expect(drawer.locator('[data-testid="subagent-save"]')).toHaveCount(0)
  await shot(page, 'p079-system-view')

  await drawer.locator('[data-testid="subagent-cancel"]').click()
  await expect(page.locator('.forebrain-drawer')).toHaveCount(0)
})

test('设置 · 子代理 writes the global switch', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  const readDefaults = async (): Promise<boolean> => {
    const answer = await page.request.get('/api/agents/defaults/subagents')
    if (!answer.ok()) throw new Error(`defaults failed: ${answer.status()} ${await answer.text()}`)
    return ((await answer.json()) as { enabled?: boolean }).enabled === true
  }
  const before = await readDefaults()
  try {
    await page.goto('/settings?tab=subagents', { waitUntil: 'networkidle' })
    const switchRow = page.locator('[data-testid="subagents-feature-switch"]')
    const toggle = switchRow.locator('button[role="switch"]')
    await expect(toggle).toHaveAttribute('aria-checked', String(before), { timeout: 20_000 })
    // Nothing to save until the form differs from the file.
    const save = page.locator('[data-testid="subagents-feature-save"]')
    await expect(save).toBeDisabled()

    await toggle.click()
    await expect(toggle).toHaveAttribute('aria-checked', String(!before))
    await expect(save).toBeEnabled()
    await save.click()

    // The answer of the write, and the state of the file behind it.
    await expect.poll(readDefaults, { timeout: 20_000 }).toBe(!before)
    await expect(save).toBeDisabled()
    await page.mouse.move(2, 2)
    await shot(page, 'p079-settings-subagents')
  } finally {
    await page.request.put('/api/agents/defaults/subagents', { data: { enabled: before } })
  }
})

test('the MCP tables keep the switch first in 操作 and name only the transport', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  for (const entry of [
    { name: MCP_STDIO, transport: 'stdio', target: `/bin/echo ${MCP_STDIO}` },
    { name: MCP_HTTP, transport: 'streamable_http', target: 'http://127.0.0.1:1/mcp' },
  ]) {
    await write(page, 'POST', MCP_SHARED, entry)
  }
  try {
    await page.goto('/settings?tab=mcp', { waitUntil: 'networkidle' })

    // No 启用 column: the switch opens the action column instead.
    const headers = (await page.locator('main thead th').allInnerTexts()).map((text) => text.trim())
    expect(headers).toEqual(['MCP 服务', '命令或地址', '类型', '状态', '操作'])
    await expect(page.locator(`[data-mcp-row="global:${MCP_STDIO}"]`)).toBeVisible({ timeout: 20_000 })
    for (const name of [MCP_STDIO, MCP_HTTP]) {
      const first = page.locator(`[data-mcp-row="global:${name}"] td:last-child .forebrain-row-actions > *:first-child`)
      await expect(first).toHaveAttribute('data-testid', `mcp-enable-global:${name}`)
      await expect(first).toHaveAttribute('role', 'switch')
    }

    // The 类型 column says which transport an entry uses and nothing else.
    await expect(page.locator(`[data-mcp-row="global:${MCP_STDIO}"] td:nth-child(3)`)).toHaveText('stdio')
    await expect(page.locator(`[data-mcp-row="global:${MCP_HTTP}"] td:nth-child(3)`)).toHaveText('http')
    await page.mouse.move(2, 2)
    await shot(page, 'p079-mcp-action-column')

    // The drawer's options speak the same two words. 类型 is a shadcn select, so
    // the options are only in the DOM while it is open — and picking the
    // transport the form already holds closes it again rather than leaving the
    // list over the button the case clicks next.
    await page.click('[data-testid="mcp-add"]')
    const drawer = page.locator('.forebrain-drawer')
    await expect(drawer).toBeVisible()
    await page.click('[data-testid="mcp-transport"]')
    const transports = await page.getByRole('option').allInnerTexts()
    expect(transports.map((text) => text.trim())).toEqual(['stdio', 'http'])
    await page.getByRole('option', { name: 'stdio', exact: true }).click()
    await shot(page, 'p079-mcp-drawer-transport')
    await drawer.locator('[data-testid="mcp-cancel"]').click()
    await expect(page.locator('.forebrain-drawer')).toHaveCount(0)
  } finally {
    for (const name of [MCP_STDIO, MCP_HTTP]) await page.request.delete(`${MCP_SHARED}/${name}`)
  }
})

test('every table’s 操作 header is centred', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  await write(page, 'POST', MCP_SHARED, { name: MCP_STDIO, transport: 'stdio', target: `/bin/echo ${MCP_STDIO}` })
  try {
    // A table with rows on it: the header is drawn with the table.
    for (const path of ['/subagents', '/mcp', '/settings?tab=mcp']) {
      await page.goto(path, { waitUntil: 'networkidle' })
      await expect(page.locator('main tbody tr').first()).toBeVisible({ timeout: 20_000 })
      expect(await actionHeaderAlign(page), `${path}'s 操作 header is not centred`).toBe('center')
      await page.mouse.move(2, 2)
      await shot(page, `p079-actions-header${path.replace(/[/?=]/g, '-')}`)
    }
  } finally {
    await page.request.delete(`${MCP_SHARED}/${MCP_STDIO}`)
  }
})

test('one 340px workbench, five tabs at a time in the workshop', async ({ page }) => {
  test.info().setTimeout(240_000)
  await signIn(page)
  await ensureSkill(page)

  // The workshop brings eight tabs: four of the conversation page's and four
  // of its own.
  await page.goto(`/workshop?skill=${SKILL}`, { waitUntil: 'networkidle' })
  await page.click('[data-testid="workshop-workbench-toggle"]')
  await expect(page.locator('[data-testid="chat-workbench"]')).toBeVisible()
  expect(Math.abs((await workbenchWidth(page)) - 340)).toBeLessThanOrEqual(1)

  const bench = page.locator('[data-testid="chat-workbench"]')
  const tabs = bench.locator('.bench-tab-strip [role="tab"]')
  await expect(tabs).toHaveCount(5)
  // Every drawn tab is one line: a wrapped one would be twice as tall.
  const heights = await tabs.evaluateAll((nodes) => nodes.map((node) => Math.round((node as HTMLElement).getBoundingClientRect().height)))
  for (const height of heights) expect(height).toBeLessThan(44)
  expect(new Set(heights).size).toBe(1)
  // The window is sliced, not scrolled sideways.
  const strip = await bench.locator('.bench-tabs').evaluate((node) => ({ scroll: node.scrollWidth, client: node.clientWidth }))
  expect(strip.scroll).toBeLessThanOrEqual(strip.client + 1)

  // The right arrow stays at the far edge whatever the window, with the last
  // tab drawn close to it.
  let gap = await tabToArrowGap(page)
  expect(gap).toBeLessThanOrEqual(30)
  await page.mouse.move(2, 2)
  await shot(page, 'p079-bench-tabs-first')

  await expect(page.locator('[data-testid="bench-tabs-prev"]')).toHaveCount(0)
  for (let step = 1; step <= 3; step += 1) {
    await page.click('[data-testid="bench-tabs-next"]')
    gap = await tabToArrowGap(page)
    expect(gap, `the gap after ${step} step(s) is ${gap}px`).toBeLessThanOrEqual(30)
    expect(await bench.locator('.bench-tabs').evaluate((node) => node.scrollWidth)).toBeLessThanOrEqual(strip.client + 1)
  }
  // Three steps reach the end of the list, where 版本 lives and the right arrow
  // has nowhere left to go.
  await expect(page.locator('[data-testid="bench-tab-versions"]')).toBeVisible()
  await expect(page.locator('[data-testid="bench-tabs-next"]')).toBeDisabled()
  await expect(page.locator('[data-testid="bench-tabs-prev"]')).toBeVisible()
  await expect(tabs).toHaveCount(5)
  await page.mouse.move(2, 2)
  await shot(page, 'p079-bench-tabs-last')

  // The conversation page's workbench is the same column, and with four tabs
  // there is no window to move.
  const sessionId = await seedProjectSession(page, 'subagent-config-bench')
  await page.goto(`/?session=${sessionId}`, { waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="workbench-toggle"]')).toBeVisible({ timeout: 20_000 })
  await page.click('[data-testid="workbench-toggle"]')
  await expect(page.locator('[data-testid="chat-workbench"]')).toBeVisible()
  expect(Math.abs((await workbenchWidth(page)) - 340)).toBeLessThanOrEqual(1)
  await expect(page.locator('.bench-tab-strip [role="tab"]')).toHaveCount(4)
  await expect(page.locator('[data-testid="bench-tabs-next"]')).toHaveCount(0)
  await expect(page.locator('[data-testid="bench-tabs-prev"]')).toHaveCount(0)
  await page.mouse.move(2, 2)
  await shot(page, 'p079-chat-workbench')
})
