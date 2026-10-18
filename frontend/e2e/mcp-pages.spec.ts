import { join } from 'node:path'

import { expect, test, type Page } from '@playwright/test'

import { seedProject, signIn } from './support'

/**
 * MCP on the web surface (plan 074): three pages, one table. 设置 · MCP lists
 * only the shared layer; the menu bar's MCP page lists the active primary
 * agent's layer on top of it; a project's MCP tab lists all three, project
 * first, each layer's rows editable only on the page that owns the layer.
 *
 * Every page is the server's: the search, the layer filter, the order and the
 * page are applied there, so each case seeds its layers through the gateway's
 * own write endpoints and then reads the table the person would see. The seed
 * is a stdio entry running /bin/echo — the cases read configuration, they never
 * start a server — plus one dead http target for the connection test.
 *
 * The seeds are removed in each case's `finally`: the gateway is shared with
 * every other spec in the run.
 */

const GLOBAL_PATH = '/api/v1/mcp/servers'

/** The review's own copy of each state, outside the run's shots directory. */
const shots = process.env.TMPDIR ?? '/tmp'

/** The shared layer's entries: 全局 a, 全局 b. */
const SHARED_NAMES = ['a', 'b']
/** The primary agent's own layer: 主代理 b (which hides 全局 b), 主代理 c. */
const AGENT_NAMES = ['b', 'c']

function agentPath(agentId: string): string {
  return `/api/agents/primary/${encodeURIComponent(agentId)}/mcp/servers`
}

function projectPath(projectId: string): string {
  return `/api/v1/projects/${encodeURIComponent(projectId)}/mcp/servers`
}

function consentPath(projectId: string): string {
  return `/api/v1/projects/${encodeURIComponent(projectId)}/mcp/consent`
}

/** One stdio entry, with a command that survives being configured and never
 * runs: every case here reads configuration, none of them starts a session. */
function stdioEntry(name: string, extra: Record<string, unknown> = {}): Record<string, unknown> {
  return { name, transport: 'stdio', target: `/bin/echo ${name}`, ...extra }
}

/** The primary agent the shell is on: the agent layer is keyed by its id. */
async function activeAgentId(page: Page): Promise<string> {
  const answer = await page.request.get('/api/agents/primary')
  if (!answer.ok()) throw new Error(`primary agents failed: ${answer.status()} ${await answer.text()}`)
  const body = (await answer.json()) as { active_id?: string }
  const id = String(body.active_id ?? '')
  if (id === '') throw new Error('no active primary agent to seed the agent layer under')
  return id
}

/** Write one entry into one layer. 409 means an earlier case of this run left
 * exactly the seed this case needs, which is the same seed. */
async function createEntry(page: Page, path: string, body: Record<string, unknown>): Promise<void> {
  const answer = await page.request.post(path, { data: body })
  if (answer.ok() || answer.status() === 409) return
  throw new Error(`seed ${String(body.name)} at ${path} failed: ${answer.status()} ${await answer.text()}`)
}

async function removeEntry(page: Page, path: string, name: string): Promise<void> {
  await page.request.delete(`${path}/${encodeURIComponent(name)}`)
}

/** The shared `a`/`b` and the agent's `b`/`c`, returning the agent id the agent
 * layer was written under. */
async function seedSharedLayers(page: Page): Promise<string> {
  const agentId = await activeAgentId(page)
  for (const name of SHARED_NAMES) {
    await createEntry(page, GLOBAL_PATH, stdioEntry(name, { startup_timeout: 2, request_timeout: 3 }))
  }
  for (const name of AGENT_NAMES) await createEntry(page, agentPath(agentId), stdioEntry(name, { startup_timeout: 2 }))
  return agentId
}

async function dropSharedLayers(page: Page, agentId: string): Promise<void> {
  for (const name of SHARED_NAMES) await removeEntry(page, GLOBAL_PATH, name)
  for (const name of AGENT_NAMES) await removeEntry(page, agentPath(agentId), name)
}

/**
 * A project's own entries: `d`, confirmed, then `e`, which nobody has decided
 * on yet — the confirmation is recorded with only `d` in the file, so `e` lands
 * as the one row that still asks for a decision.
 */
async function seedProjectLayers(page: Page, projectId: string): Promise<void> {
  const path = projectPath(projectId)
  await createEntry(page, path, stdioEntry('d'))
  const consent = await page.request.post(consentPath(projectId), { data: { allow: ['d'] } })
  if (!consent.ok()) throw new Error(`seed project consent failed: ${consent.status()} ${await consent.text()}`)
  await createEntry(page, path, stdioEntry('e'))
}

/** The rows as drawn, in order: each row's `data-mcp-row` is its identity, the
 * layer it came from and its name. */
async function rowKeys(page: Page): Promise<string[]> {
  return page
    .locator('[data-mcp-row]')
    .evaluateAll((nodes) => nodes.map((node) => node.getAttribute('data-mcp-row') ?? ''))
}

/** One shared entry as the wire holds it, read raw through the browser's own
 * request: no camelCasing sits between the answer and the assertion, so this is
 * the file's own spelling of every key, the user's own `env` and `headers`
 * names included. */
async function storedRow(page: Page, name: string): Promise<Record<string, unknown>> {
  const answer = await page.request.get(`${GLOBAL_PATH}?scope=global`)
  if (!answer.ok()) throw new Error(`shared list failed: ${answer.status()} ${await answer.text()}`)
  const body = (await answer.json()) as { servers?: Record<string, unknown>[] }
  const row = (body.servers ?? []).find((entry) => entry.name === name)
  if (!row) throw new Error(`the shared list does not hold ${name}`)
  return row
}

async function dropProject(page: Page, projectId: string): Promise<void> {
  await page.request.delete(`/api/v1/projects/${encodeURIComponent(projectId)}`)
}

test.beforeEach(async ({ page }) => {
  // The pages' copy is the design's Chinese; pinning it keeps the assertions
  // reading the same words the prototype draws.
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
})

test('settings · MCP lists the shared layer only, and edits one entry in the drawer', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  const agentId = await seedSharedLayers(page)
  try {
    await page.goto('/settings?tab=mcp', { waitUntil: 'networkidle' })

    // One layer, so no 来源 column: the six columns this page owns.
    const headers = (await page.locator('main thead th').allInnerTexts()).map((text) => text.trim())
    expect(headers).toEqual(['MCP 服务', '命令或地址', '类型', '状态', '启用', '操作'])

    // Only the shared entries, in the file's own order, under a pager.
    await expect.poll(() => rowKeys(page), { timeout: 15_000 }).toEqual(['global:a', 'global:b'])
    await expect(page.locator('[data-testid="table-pager"]')).toHaveCount(1)
    // 全局 b is overridden by 主代理 b: the row stays and says what it is actually
    // doing, instead of claiming an entry that never runs is merely idle.
    await expect(page.locator('[data-mcp-row="global:b"] .forebrain-badge', { hasText: '未运行 · 被覆盖' })).toBeVisible({
      timeout: 15_000,
    })
    await page.mouse.move(2, 2)
    await page.screenshot({ path: join(shots, '074-settings.png'), fullPage: true })

    // 编辑 opens this page's drawer: the entry's name in the title and the two
    // buttons a write has.
    await page.click('[data-testid="mcp-edit-global:b"]')
    const drawer = page.locator('.forebrain-drawer')
    await expect(drawer).toBeVisible()
    await expect(drawer.locator('.forebrain-drawer-head b')).toHaveText('编辑 MCP 服务 · b')
    await expect(drawer.locator('[data-testid="mcp-cancel"]')).toBeVisible()
    await expect(drawer.locator('[data-testid="mcp-save"]')).toBeVisible()
    await page.screenshot({ path: join(shots, '074-drawer-edit.png') })

    // A changed request timeout saves, the drawer closes, and the list is read
    // again — so reopening the row reads the number back out of the table.
    const listReload = page.waitForResponse(
      (response) => response.url().includes('/api/v1/mcp/servers') && response.request().method() === 'GET',
      { timeout: 20_000 },
    )
    await drawer.locator('[data-testid="mcp-request-timeout"]').fill('42')
    await drawer.locator('[data-testid="mcp-save"]').click()
    await listReload
    await expect(page.locator('.forebrain-drawer')).toHaveCount(0, { timeout: 15_000 })
    await expect(async () => {
      // A table that had not reloaded would open the stale entry, so the loop
      // closes it and asks again rather than betting on one tick.
      if (await page.locator('.forebrain-drawer').count()) {
        await page.click('[data-testid="mcp-cancel"]')
        await expect(page.locator('.forebrain-drawer')).toHaveCount(0)
      }
      await page.click('[data-testid="mcp-edit-global:b"]')
      await expect(page.locator('[data-testid="mcp-request-timeout"]')).toHaveValue('42', { timeout: 2_000 })
    }).toPass({ timeout: 20_000 })
    await page.click('[data-testid="mcp-cancel"]')
    await expect(page.locator('.forebrain-drawer')).toHaveCount(0)
  } finally {
    await dropSharedLayers(page, agentId)
  }
})

test('the menu bar names MCP, and its page puts the agent layer first', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  const agentId = await seedSharedLayers(page)
  try {
    await page.goto('/', { waitUntil: 'networkidle' })
    const hrefs = await page
      .locator('.forebrain-rail-group a.forebrain-rail-link')
      .evaluateAll((nodes) => nodes.map((node) => (node as HTMLAnchorElement).getAttribute('href')))
    expect(hrefs).toContain('/mcp')
    expect(hrefs.indexOf('/mcp')).toBe(hrefs.indexOf('/skills') + 1)

    await page.click('.forebrain-rail-group a[href="/mcp"]')
    await expect(page).toHaveURL(/\/mcp$/)
    // The top bar names the page; the content draws no title of its own.
    await expect(page.locator('.forebrain-topbar-title')).toHaveText('MCP')
    await expect(page.locator('main h1, main h2')).toHaveCount(0)

    // The agent's own rows first, then the shared ones it does not shadow: 全局 b
    // is hidden behind 主代理 b, and the layer column names each row's layer.
    await expect.poll(() => rowKeys(page), { timeout: 15_000 }).toEqual(['agent:b', 'agent:c', 'global:a'])
    await expect(page.locator('[data-testid="table-pager"]')).toHaveCount(1)
    await expect(page.locator('main thead th', { hasText: '来源' })).toHaveCount(1)
    await expect(page.locator('[data-mcp-row="global:a"] .scope-badge', { hasText: '全局' })).toBeVisible()
    await page.mouse.move(2, 2)
    await page.screenshot({ path: join(shots, '074-mcp-page.png'), fullPage: true })

    // A row of another layer: 查看 is its only action.
    await expect(page.locator('[data-testid="mcp-view-global:a"]')).toBeVisible()
    await expect(page.locator('[data-testid="mcp-edit-global:a"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="mcp-delete-global:a"]')).toHaveCount(0)
    // This page's own rows carry the two writes.
    await expect(page.locator('[data-testid="mcp-edit-agent:b"]')).toBeVisible()
    await expect(page.locator('[data-testid="mcp-delete-agent:b"]')).toBeVisible()

    // The read-only drawer: nothing in it can be typed into, its footer is 取消
    // alone, and it says where the entry is edited instead.
    await page.click('[data-testid="mcp-view-global:a"]')
    const drawer = page.locator('.forebrain-drawer')
    await expect(drawer).toBeVisible()
    await expect(drawer.locator('.forebrain-drawer-head b')).toHaveText('MCP 服务 · a')
    await expect(drawer.locator('input:not([readonly])')).toHaveCount(0)
    await expect(drawer.locator('select:not([disabled])')).toHaveCount(0)
    await expect(drawer.locator('button[role="switch"]:not([disabled])')).toHaveCount(0)
    await expect(drawer.locator('[data-testid="mcp-save"]')).toHaveCount(0)
    await expect(drawer.locator('.forebrain-drawer-foot button')).toHaveCount(1)
    await expect(drawer.locator('[data-testid="mcp-cancel"]')).toBeVisible()
    await expect(drawer.locator('[data-testid="mcp-edit-elsewhere"]')).toHaveText('在“设置 → MCP”修改')
    await page.screenshot({ path: join(shots, '074-drawer-view.png') })
    await page.click('[data-testid="mcp-cancel"]')
    await expect(page.locator('.forebrain-drawer')).toHaveCount(0)
  } finally {
    await dropSharedLayers(page, agentId)
  }
})

test('a project’s MCP tab lists project, agent and shared, and decides a pending entry', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  const agentId = await seedSharedLayers(page)
  const projectId = await seedProject(page, 'mcp-074')
  try {
    await seedProjectLayers(page, projectId)
    await page.goto(`/projects/${projectId}/mcp`, { waitUntil: 'networkidle' })

    // Project first (its undecided entry above its confirmed one), then the
    // agent's, then the shared ones; 全局 b is hidden behind 主代理 b.
    await expect
      .poll(() => rowKeys(page), { timeout: 15_000 })
      .toEqual(['project:e', 'project:d', 'agent:b', 'agent:c', 'global:a'])
    await expect(page.locator('[data-testid="table-pager"]')).toHaveCount(1)
    await page.mouse.move(2, 2)
    await page.screenshot({ path: join(shots, '074-project-mcp.png'), fullPage: true })

    // The undecided row asks for the decision instead of offering a switch.
    const pending = page.locator('[data-mcp-row="project:e"]')
    await expect(pending.locator('[data-testid="mcp-allow-project:e"]')).toBeVisible()
    await expect(pending.locator('[data-testid="mcp-decline-project:e"]')).toBeVisible()
    await expect(page.locator('[data-testid="mcp-enable-project:e"]')).toHaveCount(0)
    await expect(pending.locator('.forebrain-badge', { hasText: '待确认' })).toBeVisible()

    // 允许 records the decision; the row comes back confirmed, with the writes
    // this page owns and no decision left to make.
    await pending.locator('[data-testid="mcp-allow-project:e"]').click()
    await expect(page.locator('[data-mcp-row="project:e"] .forebrain-badge', { hasText: '已允许' })).toBeVisible({
      timeout: 15_000,
    })
    await expect(page.locator('[data-testid="mcp-allow-project:e"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="mcp-edit-project:e"]')).toBeVisible()
  } finally {
    await dropProject(page, projectId)
    await dropSharedLayers(page, agentId)
  }
})

test('deciding one project entry leaves the others pending, and a declined entry can be allowed again', async ({
  page,
}) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  const agentId = await seedSharedLayers(page)
  const projectId = await seedProject(page, 'mcp-077')
  try {
    // d is confirmed and e is undecided; f is written after that, so it is
    // undecided too. Two entries are waiting on a decision, not one.
    await seedProjectLayers(page, projectId)
    await createEntry(page, projectPath(projectId), stdioEntry('f'))
    await page.goto(`/projects/${projectId}/mcp`, { waitUntil: 'networkidle' })

    // Wait for the server's list: d is confirmed while e and f both wait.
    await expect(page.locator('[data-mcp-row="project:f"]')).toHaveCount(1, { timeout: 15_000 })
    await expect(page.locator('[data-mcp-row="project:e"] .forebrain-badge', { hasText: '待确认' })).toBeVisible()
    await expect(page.locator('[data-mcp-row="project:f"] .forebrain-badge', { hasText: '待确认' })).toBeVisible()

    // 拒绝 decides e alone.
    await page.click('[data-testid="mcp-decline-project:e"]')
    await expect(page.locator('[data-mcp-row="project:e"] .forebrain-badge', { hasText: '已拒绝' })).toBeVisible({
      timeout: 15_000,
    })

    // The declined entry keeps its row with the way back and no switch, and the
    // project layer is now 待确认 → 已允许 → 已拒绝.
    await expect(page.locator('[data-testid="mcp-undecline-project:e"]')).toBeVisible()
    await expect(page.locator('[data-testid="mcp-enable-project:e"]')).toHaveCount(0)
    await expect.poll(() => rowKeys(page), { timeout: 15_000 }).toEqual([
      'project:f',
      'project:d',
      'project:e',
      'agent:b',
      'agent:c',
      'global:a',
    ])
    await page.mouse.move(2, 2)
    await page.screenshot({ path: join(shots, '077-project-mcp-declined.png'), fullPage: true })

    // f was not part of that decision: it is still waiting, with both buttons.
    await expect(page.locator('[data-mcp-row="project:f"] .forebrain-badge', { hasText: '待确认' })).toBeVisible()
    await expect(page.locator('[data-testid="mcp-allow-project:f"]')).toBeVisible()

    // 允许 on the declined row takes the decision back; f is still waiting.
    await page.click('[data-testid="mcp-undecline-project:e"]')
    await expect(page.locator('[data-mcp-row="project:e"] .forebrain-badge', { hasText: '已允许' })).toBeVisible({
      timeout: 15_000,
    })
    await expect(page.locator('[data-testid="mcp-edit-project:e"]')).toBeVisible()
    await expect(page.locator('[data-mcp-row="project:f"] .forebrain-badge', { hasText: '待确认' })).toBeVisible()
    await expect(page.locator('[data-testid="mcp-allow-project:f"]')).toBeVisible()
  } finally {
    await dropProject(page, projectId)
    await dropSharedLayers(page, agentId)
  }
})

test('adding a name the project already has keeps the drawer open and says why', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  const projectId = await seedProject(page, 'mcp-074-dup')
  try {
    await seedProjectLayers(page, projectId)
    await page.goto(`/projects/${projectId}/mcp`, { waitUntil: 'networkidle' })

    await page.click('[data-testid="mcp-add"]')
    const drawer = page.locator('.forebrain-drawer')
    await expect(drawer.locator('.forebrain-drawer-head b')).toHaveText('添加 MCP 服务 · 项目')
    await drawer.locator('[data-testid="mcp-name"]').fill('d')
    await drawer.locator('[data-testid="mcp-save"]').click()

    // The gateway's own sentence, verbatim, and the drawer stays where it is.
    const failure = drawer.locator('[data-testid="mcp-save-error"]')
    await expect(failure).toBeVisible({ timeout: 15_000 })
    await expect(failure).toContainText('没有保存：')
    await expect(failure).toContainText('d')
    await expect(drawer).toBeVisible()
    await page.screenshot({ path: join(shots, '074-duplicate.png') })
    await page.click('[data-testid="mcp-cancel"]')
    await expect(page.locator('.forebrain-drawer')).toHaveCount(0)
  } finally {
    await dropProject(page, projectId)
  }
})

test('a connection test against a dead address says the failure in its own words', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  const projectId = await seedProject(page, 'mcp-074-test')
  try {
    await page.goto(`/projects/${projectId}/mcp`, { waitUntil: 'networkidle' })

    await page.click('[data-testid="mcp-add"]')
    const drawer = page.locator('.forebrain-drawer')
    await expect(drawer.locator('.forebrain-drawer-head b')).toHaveText('添加 MCP 服务 · 项目')
    await drawer.locator('[data-testid="mcp-name"]').fill('e2e-dead')
    await drawer.locator('[data-testid="mcp-transport"]').selectOption('streamable_http')
    // Port 1 answers nothing: the attempt is refused instead of hanging.
    await drawer.locator('[data-testid="mcp-target"]').fill('http://127.0.0.1:1/mcp')
    await drawer.locator('[data-testid="mcp-test-run"]').click()

    await expect(drawer.locator('[data-testid="mcp-test-failed"]')).toBeVisible({ timeout: 45_000 })
    await expect(drawer.locator('[data-testid="mcp-test-failed"]')).toContainText('连接失败')
    const reason = drawer.locator('[data-testid="mcp-test-error"]')
    await expect(reason).toBeVisible()
    await expect.poll(async () => (await reason.innerText()).trim().length, { timeout: 15_000 }).toBeGreaterThan(0)
    // The test writes nothing and the drawer stays open.
    await expect(drawer).toBeVisible()
    await page.screenshot({ path: join(shots, '074-test-failed.png') })
    await page.click('[data-testid="mcp-cancel"]')
    await expect(page.locator('.forebrain-drawer')).toHaveCount(0)
  } finally {
    await dropProject(page, projectId)
  }
})

/** The entry whose `env` map is keyed the operator's own way, with a `${ENV}`
 * reference for a value: the one shape a key rewrite between the wire and the
 * drawer would corrupt, because a renamed key is a variable nobody wrote. */
const ENV_NAME = '074-env'
const ENV_KEY = 'CODEGRAPH_HOME'
const ENV_VALUE = '${CODEGRAPH_HOME}'

test('editing an entry leaves the operator’s own env key spelled as it was seeded', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  await createEntry(page, GLOBAL_PATH, { ...stdioEntry(ENV_NAME), env: { [ENV_KEY]: ENV_VALUE } })
  try {
    await page.goto('/settings?tab=mcp', { waitUntil: 'networkidle' })
    await expect.poll(() => rowKeys(page), { timeout: 15_000 }).toContain(`global:${ENV_NAME}`)

    // The entry as the wire holds it before anything is edited: the one
    // variable, and whatever the gateway reports as its value (a `${NAME}`
    // reference is resolved when the file is loaded, so the read carries the
    // resolved value rather than the reference — the key spelling is what this
    // case is about). `env` has to come back byte-identical after the save.
    const before = await storedRow(page, ENV_NAME)
    const envBefore = (before.env ?? {}) as Record<string, string>
    expect(Object.keys(envBefore)).toEqual([ENV_KEY])
    expect(before.request_timeout).not.toBe(37)

    await page.click(`[data-testid="mcp-edit-global:${ENV_NAME}"]`)
    const drawer = page.locator('.forebrain-drawer')
    await expect(drawer).toBeVisible()
    // The advanced half is closed while editing; the env table is inside it.
    await drawer.locator('[data-testid="mcp-advanced"] summary').click()

    // The drawer shows the variable the operator wrote. A read that camelCased
    // the whole answer would draw `codegraphHome` here — a second, bogus
    // variable — and `mcpWritePayload` would submit that spelling back, which
    // the gateway merges in beside the real key instead of replacing it.
    await expect(drawer.locator('[data-kv-key]')).toHaveValue(ENV_KEY)
    await expect(drawer.locator('[data-kv-value]')).toHaveValue(envBefore[ENV_KEY] ?? '')
    await page.screenshot({ path: join(shots, '074-env-keys.png') })

    // Something innocuous changes — the request timeout — so the save rewrites
    // the entry and the env map has to survive the round trip untouched.
    const listReload = page.waitForResponse(
      (response) => response.url().includes('/api/v1/mcp/servers') && response.request().method() === 'GET',
      { timeout: 20_000 },
    )
    await drawer.locator('[data-testid="mcp-request-timeout"]').fill('37')
    await drawer.locator('[data-testid="mcp-save"]').click()
    await listReload
    await expect(page.locator('.forebrain-drawer')).toHaveCount(0, { timeout: 15_000 })

    // What the entry holds now, read raw so nothing between the wire and the
    // assertion can rewrite a key: exactly the seeded variable with the value
    // it had, and no renamed or snake-cased copy beside it. The save did land —
    // the field the case changed is the new one.
    const after = await storedRow(page, ENV_NAME)
    expect(after.env).toEqual(envBefore)
    expect(after.request_timeout).toBe(37)
  } finally {
    await removeEntry(page, GLOBAL_PATH, ENV_NAME)
  }
})

test('the shared list pages on the server: twelve entries, one page of ten, two pages', async ({ page }) => {
  test.info().setTimeout(180_000)
  await signIn(page)
  const names = Array.from({ length: 12 }, (_, index) => `074-p${String(index + 1).padStart(2, '0')}`)
  try {
    for (const name of names) await createEntry(page, GLOBAL_PATH, stdioEntry(name))

    await page.goto('/settings?tab=mcp', { waitUntil: 'networkidle' })
    // The pager's total is the server's count of the whole list, not the page's.
    await expect(page.locator('[data-testid="pager-total"]')).toHaveText(/共 12 条/, { timeout: 15_000 })
    await expect(page.locator('[data-testid="pager-page-1"]')).toHaveAttribute('aria-current', 'page')
    await expect(page.locator('[data-testid="pager-page-2"]')).toBeVisible()
    await expect(page.locator('[data-testid="pager-page-3"]')).toHaveCount(0)
    await expect(page.locator('[data-mcp-row]')).toHaveCount(10)
    await page.mouse.move(2, 2)
    await page.screenshot({ path: join(shots, '074-paged.png'), fullPage: true })

    // The page numbers are the way across the list, and the second page is the
    // two entries the first one left.
    await page.click('[data-testid="pager-page-2"]')
    await expect(page.locator('[data-testid="pager-page-2"]')).toHaveAttribute('aria-current', 'page')
    await expect(page.locator('[data-mcp-row]')).toHaveCount(2)
    await expect.poll(() => rowKeys(page)).toEqual(['global:074-p11', 'global:074-p12'])
  } finally {
    for (const name of names) await removeEntry(page, GLOBAL_PATH, name)
  }
})
