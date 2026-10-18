import { join } from 'node:path'

import { expect, test, type Page } from '@playwright/test'

import { expectAssistantReply, seedProject, signIn } from './support'

/**
 * The project space as the design draws it (plan 075): the breadcrumb that
 * names the project lives in the shell's top bar, 项目设置 is the first tab and
 * one flat form (its instructions, its memory scope, its trust switch and the
 * delete beside 保存), every other tab is a search box, a table, a pager and
 * one note — and the session, cron and channel tables carry the columns the
 * server now derives.
 *
 * Everything seeded here goes through the endpoints the pages themselves use:
 * conversations with one real turn each (a conversation only lists once it
 * holds a message), a model pinned the way /model pins one, a heartbeat, a job
 * fired three times through its own run endpoint, and two channel kinds
 * written through the page's channel route.
 */

/** The review's own screenshot directory, outside the run's shots dir. */
const shots = process.env.TMPDIR ?? '/tmp'

test.beforeEach(async ({ page }) => {
  // The tab copy is the design's Chinese; pinning it keeps the assertions
  // reading the same words the prototype draws.
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
})

/** The review's own copy of a page, with nothing hovered. */
async function pageShot(page: Page, name: string): Promise<void> {
  await page.mouse.move(2, 2)
  await page.screenshot({ path: join(shots, name), fullPage: true })
}

interface SeededProject {
  id: string
  name: string
  root: string
}

/** A project of this spec's own, trusted, under the run's scratch tree. */
async function seed(page: Page, suffix: string): Promise<SeededProject> {
  const name = `075-${suffix}`
  const id = await seedProject(page, name)
  const row = await page.request.get(`/api/v1/projects/${encodeURIComponent(id)}`)
  if (!row.ok()) throw new Error(`read seeded project failed: ${row.status()} ${await row.text()}`)
  const body = (await row.json()) as { root?: string; trusted?: boolean }
  expect(body.trusted, 'the seeded project is trusted').toBe(true)
  return { id, name, root: String(body.root ?? '') }
}

/** One conversation in `project`, named by a real turn: the list only carries
 *  a conversation that holds a message. */
async function seedConversation(page: Page, projectId: string, prompt: string): Promise<string> {
  const created = await page.request.post('/api/chat/sessions', { data: { project_id: projectId } })
  if (!created.ok()) throw new Error(`seed session failed: ${created.status()} ${await created.text()}`)
  const sessionId = String(((await created.json()) as { id: string }).id)
  await page.goto(`/?session=${sessionId}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await page.fill('textarea', prompt)
  await page.press('textarea', 'Enter')
  await expectAssistantReply(page)
  return sessionId
}

/** Pin a conversation's own model, the way /model does, and answer its name. */
async function pinSessionModel(page: Page, sessionId: string): Promise<string> {
  const offered = await page.request.get(`/api/chat/sessions/${encodeURIComponent(sessionId)}/model`)
  const body = (await offered.json()) as { options?: Array<{ provider?: string; model?: string }> }
  const option = body.options?.[0]
  if (!option?.provider || !option.model) throw new Error('the active agent offers no model to pin')
  const pinned = await page.request.put(`/api/chat/sessions/${encodeURIComponent(sessionId)}/model`, {
    data: { provider: option.provider, model: option.model, effort: '' },
  })
  if (!pinned.ok()) throw new Error(`pin model failed: ${pinned.status()} ${await pinned.text()}`)
  return option.model
}

/** Give a conversation its heartbeat, the way the heartbeat panel does. */
async function seedHeartbeat(page: Page, sessionId: string, minutes: number): Promise<void> {
  const res = await page.request.put('/api/heartbeat', {
    data: { session_id: sessionId, interval_seconds: minutes * 60, prompt: '075 heartbeat prompt', paused: false },
  })
  if (!res.ok()) throw new Error(`seed heartbeat failed: ${res.status()} ${await res.text()}`)
}

function runState(page: Page, projectId: string, jobId: string) {
  return page.request
    .get(`/api/v1/projects/${encodeURIComponent(projectId)}/cron/jobs/${encodeURIComponent(jobId)}/runs?page=1&page_size=10`)
    .then(async (res) => {
      if (!res.ok()) throw new Error(`read runs failed: ${res.status()} ${await res.text()}`)
      const body = (await res.json()) as { runs?: Array<{ status?: string }>; total?: number }
      return {
        total: Number(body.total ?? 0),
        running: (body.runs ?? []).some((run) => run.status === 'running'),
      }
    })
}

/** Wait for the job to hold exactly `total` settled fires. A fire is a turn in
 *  a conversation of its own, and the engine refuses a second one while the
 *  first is still in flight — so they are fired one at a time. */
async function waitForRuns(page: Page, projectId: string, jobId: string, total: number): Promise<void> {
  await expect
    .poll(async () => {
      const state = await runState(page, projectId, jobId)
      return state.running ? -1 : state.total
    }, { timeout: 90_000, message: `job ${jobId} to settle ${total} fires` })
    .toBe(total)
}

/** One job of `project` with `runs` finished fires in its history. */
async function seedCronJob(page: Page, projectId: string, name: string, runs: number): Promise<string> {
  const created = await page.request.post(`/api/v1/projects/${encodeURIComponent(projectId)}/cron/jobs`, {
    data: { name, schedule: 'in 30m', prompt: '075 cron prompt' },
  })
  if (!created.ok()) throw new Error(`seed job failed: ${created.status()} ${await created.text()}`)
  const jobId = String(((await created.json()) as { id: string }).id)
  for (let fired = 0; fired < runs; fired++) {
    await waitForRuns(page, projectId, jobId, fired)
    const run = await page.request.post(
      `/api/v1/projects/${encodeURIComponent(projectId)}/cron/jobs/${encodeURIComponent(jobId)}/run`,
      { data: {} },
    )
    if (!run.ok()) throw new Error(`fire job failed: ${run.status()} ${await run.text()}`)
  }
  await waitForRuns(page, projectId, jobId, runs)
  return jobId
}

/** 飞书 and Telegram configured, nothing else: the two facts the channel table
 *  is read over (one row with a masked secret, one kind left unconfigured). */
async function seedChannels(page: Page, projectId: string): Promise<void> {
  const res = await page.request.put(`/api/v1/projects/${encodeURIComponent(projectId)}/channels`, {
    data: {
      feishu: { enabled: false, app_id: 'cli-075-feishu', app_secret: 'feishu-secret-9f3c' },
      telegram: { enabled: false, bot_token: '075:telegram-token-7ab1' },
    },
  })
  if (!res.ok()) throw new Error(`seed channels failed: ${res.status()} ${await res.text()}`)
}

/** Every control of 项目设置, in document order, named the way it reads. */
function settingsControls(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const nodes = Array.from(document.querySelectorAll(
      'main input:not([type="radio"]), main textarea, main [role="radiogroup"], main [role="switch"], main button',
    ))
    return nodes.map((node) => {
      const el = node as HTMLElement
      const testid = el.dataset.testid ?? ''
      const role = el.getAttribute('role') ?? ''
      if (role === 'radiogroup') return 'radiogroup'
      if (role === 'switch') return `switch:${el.getAttribute('aria-label') ?? ''}`
      if (el.tagName === 'TEXTAREA') return `textarea:${testid}`
      if (el.tagName === 'INPUT') return `input:${testid}${(el as HTMLInputElement).readOnly ? ':readonly' : ''}`
      return `button:${testid || (el.textContent ?? '').trim()}`
    })
  })
}

test('the top bar carries the breadcrumb and 项目设置 is the first tab of one flat form', async ({ page }) => {
  test.info().setTimeout(180_000)
  await signIn(page)
  const project = await seed(page, 'settings')
  await page.goto(`/projects/${project.id}/overview`, { waitUntil: 'networkidle' })

  // 1. The shell's top bar: the layer it returns to, the project's name, its
  //    directory and whether that directory is trusted.
  await expect(page.locator('.forebrain-topbar-crumb')).toBeVisible()
  await expect(page.locator('[data-testid="project-crumb-back"]')).toHaveText('项目')
  await expect(page.locator('[data-testid="project-crumb-name"]')).toHaveText(project.name)
  await expect(page.locator('[data-testid="project-crumb-root"]')).toHaveText(project.root)
  await expect(page.locator('[data-testid="project-crumb-trust"]')).toHaveText('已信任')
  // The page keeps no breadcrumb of its own, and the tab row opens on 项目设置.
  await expect(page.locator('.crumb')).toHaveCount(0)
  await expect(page.locator('.proj-tab').first()).toHaveText('项目设置')

  // 2. The settings form: no heading, no card, and the fields in the design's
  //    order — the two-column pairs, 说明, 图标 beside the read-only key, the
  //    instructions, the memory scope, the two switches, then the buttons.
  await expect(page.locator('main h2, main h3')).toHaveCount(0)
  await expect(page.locator('main .forebrain-card')).toHaveCount(0)
  expect(await settingsControls(page)).toEqual([
    'input:project-settings-name',
    'input:project-settings-root:readonly',
    'input:project-settings-description',
    'input:project-settings-icon',
    'input:project-settings-key:readonly',
    'textarea:project-instructions',
    'radiogroup',
    'switch:信任此目录',
    'switch:允许读取项目目录之外的资料',
    'button:project-settings-save',
    'button:project-settings-delete',
  ])
  await expect(page.locator('main')).toContainText('注入本项目每个会话的开头，新会话生效。')

  // The memory scope is a radio group whose help follows the choice.
  const scope = page.locator('[role="radiogroup"]')
  await expect(scope).toHaveCount(1)
  const help = scope.locator('.forebrain-list-note')
  await expect(help).toContainText('本项目的会话也能召回其他记忆；修改只在新会话生效。')
  await scope.locator('label', { hasText: '仅本项目' }).locator('input').check()
  await expect(help).toContainText('只召回本项目自己的记忆；修改只在新会话生效。')
  await pageShot(page, '075-project-settings.png')

  // An instruction saved with the form survives a reload: it is the project's
  // own field, not a per-tab draft.
  const instruction = '075 指令：回答之前先读 README'
  await page.fill('[data-testid="project-instructions"]', instruction)
  await page.click('[data-testid="project-settings-save"]')
  await expect(page.locator('[data-testid="project-settings-saved"]')).toBeVisible({ timeout: 15_000 })
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="project-instructions"]')).toHaveValue(instruction)

  // Withdrawing the trust moves the top bar's badge: the shell reloads the
  // project on the form's `saved`, so the badge follows the new settings.
  await page.locator('[role="switch"][aria-label="信任此目录"]').click()
  await page.click('[data-testid="project-settings-save"]')
  await expect(page.locator('[data-testid="project-crumb-trust"]')).toHaveText('未信任', { timeout: 15_000 })

  // 删除项目 sits to the right of 保存 and asks for the project's name.
  const save = await page.locator('[data-testid="project-settings-save"]').boundingBox()
  const remove = await page.locator('[data-testid="project-settings-delete"]').boundingBox()
  expect(remove!.x, '删除项目 is not to the right of 保存').toBeGreaterThan(save!.x + save!.width - 1)
  await page.click('[data-testid="project-settings-delete"]')
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('删除项目')
  const typed = dialog.locator('input')
  await expect(typed).toHaveAttribute('placeholder', project.name)
  await expect(dialog.locator('[data-testid="confirm-ok"]')).toBeDisabled()
  await typed.fill(project.name)
  await expect(dialog.locator('[data-testid="confirm-ok"]')).toBeEnabled()
  await dialog.getByRole('button', { name: '取消' }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

test('项目指令 lives in 项目设置 and the memory scope with it', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  const project = await seed(page, 'split')
  await page.goto(`/projects/${project.id}/overview`, { waitUntil: 'networkidle' })
  // The settings tab is the one that holds both.
  await expect(page.locator('[data-testid="project-instructions"]')).toHaveCount(1)
  await expect(page.locator('[role="radiogroup"]')).toHaveCount(1)

  // 规则指令 keeps the FOREBRAIN.md chain only.
  await page.goto(`/projects/${project.id}/rules`, { waitUntil: 'networkidle' })
  await expect(page.locator('main .forebrain-list-toolbar')).toHaveCount(1, { timeout: 15_000 })
  await expect(page.locator('[data-testid="project-instructions"]')).toHaveCount(0)
  await expect(page.locator('main')).not.toContainText('项目指令')

  // 记忆 keeps the file table only.
  await page.goto(`/projects/${project.id}/memory`, { waitUntil: 'networkidle' })
  await expect(page.locator('main .forebrain-list-toolbar')).toHaveCount(1, { timeout: 15_000 })
  await expect(page.locator('[role="radiogroup"]')).toHaveCount(0)
  await expect(page.locator('main')).not.toContainText('记忆作用域')
})

test('the session table carries model, message count and heartbeat, and sorts by 创建时间', async ({ page }) => {
  test.info().setTimeout(240_000)
  await signIn(page)
  const project = await seed(page, 'sessions')

  // One conversation pinned to a model, one carrying a heartbeat.
  const pinnedId = await seedConversation(page, project.id, '075 会话一')
  const model = await pinSessionModel(page, pinnedId)
  const heartbeatId = await seedConversation(page, project.id, '075 会话二')
  await seedHeartbeat(page, heartbeatId, 5)

  await page.goto(`/projects/${project.id}/sessions`, { waitUntil: 'networkidle' })
  const rows = page.locator('[data-session-row]')
  await expect(rows).toHaveCount(2, { timeout: 20_000 })
  await pageShot(page, '075-sessions.png')

  const pinnedRow = page.locator(`[data-session-row="${pinnedId}"]`)
  await expect(pinnedRow.locator('td').nth(1)).toHaveText(model)
  expect(Number(await pinnedRow.locator('td').nth(2).innerText()), 'the pinned conversation counts its messages').toBeGreaterThan(0)
  await expect(pinnedRow.locator('td').nth(3)).toHaveText('—')

  const beatRow = page.locator(`[data-session-row="${heartbeatId}"]`)
  expect(Number(await beatRow.locator('td').nth(2).innerText()), 'the heartbeat conversation counts its messages').toBeGreaterThan(0)
  await expect(beatRow.locator('td').nth(3)).toHaveText(/每 5 分钟/)

  // The header orders the whole list on the server, newest first.
  const created = page.locator('th.forebrain-sortable', { hasText: '创建时间' })
  await created.locator('button').click()
  await expect(created).toHaveAttribute('aria-sort', 'descending')
  await expect(page.locator('[data-testid="pager-total"]')).toHaveText(/共 2 条/)
})

test('the cron table pauses in place and its history is a modal', async ({ page }) => {
  test.info().setTimeout(240_000)
  await signIn(page)
  const project = await seed(page, 'cron')
  const jobId = await seedCronJob(page, project.id, '075 brief', 3)

  await page.goto(`/projects/${project.id}/cron`, { waitUntil: 'networkidle' })
  const row = page.locator('[data-cron-job="075 brief"]')
  await expect(row).toHaveCount(1, { timeout: 20_000 })
  await expect(page.locator('[data-cron-job]')).toHaveCount(1)
  await expect(page.locator('[data-testid="pager-total"]')).toHaveText(/共 1 条/)
  await pageShot(page, '075-cron.png')

  // The switch in 启用 is the pause: the next-run column says so at once.
  await page.locator(`[data-testid="cron-toggle-${jobId}"]`).click()
  await expect(row.locator('td').nth(3)).toHaveText(/已暂停/, { timeout: 15_000 })

  // A task's fires are a history of their own, in a modal over the table.
  await page.locator(`[data-testid="cron-history-${jobId}"]`).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('执行记录')
  await expect(dialog.locator('[data-cron-run]')).toHaveCount(3, { timeout: 20_000 })
  await expect(dialog.locator('[data-testid="table-pager"]')).toBeVisible()
  await expect(dialog.locator('[data-testid="pager-total"]')).toHaveText(/共 3 条/)
  await pageShot(page, '075-cron-runs.png')
})

test('the channel table masks secrets, disables what is unconfigured, and edits one kind alone', async ({ page }) => {
  test.info().setTimeout(180_000)
  await signIn(page)
  const project = await seed(page, 'channels')
  await seedChannels(page, project.id)

  await page.goto(`/projects/${project.id}/channels`, { waitUntil: 'networkidle' })
  // Every kind on one page, so the configured and the unconfigured read alike.
  await expect(page.locator('[data-testid="pager-size"]')).toBeVisible({ timeout: 20_000 })
  await page.selectOption('[data-testid="pager-size"]', '50')

  const feishu = page.locator('[data-channel-row="feishu"]')
  await expect(feishu).toBeVisible({ timeout: 20_000 })
  await expect(feishu.locator('td').nth(1)).toHaveText('feishu')
  await expect(feishu.locator('td').nth(2)).toContainText('app_id')
  // A secret is the server's hint: the mask and the last four of the value.
  await expect(feishu.locator('td').nth(2)).toContainText('••••9f3c')
  await expect(page.locator('[data-testid="channel-toggle-feishu"]')).toBeEnabled()

  const slack = page.locator('[data-channel-row="slack"]')
  await expect(slack.locator('td').nth(1)).toHaveText('—')
  await expect(slack.locator('td').nth(2)).toHaveText('未配置')
  await expect(page.locator('[data-testid="channel-toggle-slack"]')).toBeDisabled()

  // The switch and the editor each write on their own: no page-level 保存.
  await expect(page.locator('main button', { hasText: /^\s*保存\s*$/ })).toHaveCount(0)
  await pageShot(page, '075-channels.png')

  // Editing 飞书 writes that one kind; Telegram's configuration survives.
  await page.locator('[data-testid="channel-edit-feishu"]').click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toContainText('编辑通道 · 飞书')
  await expect(dialog.locator('[data-testid="channel-field-app_secret"]')).toHaveValue('')
  await expect(dialog.locator('[data-testid="channel-secret-hint"]')).toBeVisible()
  await pageShot(page, '075-channel-edit.png')
  await dialog.locator('[data-testid="channel-field-app_id"]').fill('cli-075-feishu-edited')
  await dialog.locator('[data-testid="channel-save"]').click()
  await expect(page.getByRole('dialog')).toHaveCount(0, { timeout: 20_000 })
  await expect(feishu.locator('td').nth(2)).toContainText('cli-075-feishu-edited', { timeout: 20_000 })
  await expect(feishu.locator('td').nth(2)).toContainText('••••9f3c')

  const telegram = page.locator('[data-channel-row="telegram"]')
  await expect(telegram.locator('td').nth(1)).toHaveText('telegram')
  await expect(telegram.locator('td').nth(2)).toContainText('bot_token')
  await expect(telegram.locator('td').nth(2)).toContainText('••••7ab1')
})

test('every project tab is a search box over a table, and none draws a heading', async ({ page }) => {
  test.info().setTimeout(240_000)
  await signIn(page)
  const project = await seed(page, 'tabs')
  // The permission tab's table only appears for a project the engine takes
  // rules from; a conversation gives the session tab its row.
  await seedConversation(page, project.id, '075 各标签')

  const tabs = ['rules', 'sessions', 'memory', 'perm', 'mcp', 'lsp', 'skills', 'cron', 'channels']
  for (const tab of tabs) {
    await page.goto(`/projects/${project.id}/${tab}`, { waitUntil: 'networkidle' })
    await expect(page.locator('main h1, main h2, main h3'), `${tab} still draws a heading`).toHaveCount(0)

    const toolbar = page.locator('main .forebrain-list-toolbar')
    await expect(toolbar, `${tab} has no toolbar`).toHaveCount(1, { timeout: 20_000 })
    const firstChild = await toolbar.evaluate((node) => {
      const first = node.firstElementChild as HTMLElement | null
      return first ? `${first.tagName.toLowerCase()}|${(first as HTMLInputElement).type ?? ''}` : ''
    })
    expect(firstChild, `${tab}'s toolbar does not open with the search box`).toBe('input|search')

    // The search box is the leftmost and topmost control of the tab body:
    // nothing else in the body is on the row above it, or to the left of it.
    // The tab row above the body is the shell's, not the tab's.
    const geometry = await page.evaluate(() => {
      const search = document.querySelector('main .forebrain-list-toolbar input[type="search"]')
      if (!search) return null
      const own = search.getBoundingClientRect()
      const boxes: Array<{ left: number; top: number; what: string }> = []
      for (const node of Array.from(document.querySelectorAll(
        'main input, main select, main textarea, main button, main table, main [role="switch"], main a, main p',
      ))) {
        if (node.closest('.proj-head')) continue
        const rect = node.getBoundingClientRect()
        if (rect.width === 0 || rect.height === 0) continue
        const el = node as HTMLElement
        const name = `${el.tagName.toLowerCase()}${el.dataset.testid ? `[${el.dataset.testid}]` : ''}`
        boxes.push({ left: rect.left, top: rect.top, what: `${name} ${(el.textContent ?? '').trim().slice(0, 24)}` })
      }
      const leftmost = boxes.reduce((best, box) => (box.left < best.left ? box : best), boxes[0])
      const topmost = boxes.reduce((best, box) => (box.top < best.top ? box : best), boxes[0])
      return { search: { left: own.left, top: own.top }, leftmost, topmost }
    })
    expect(geometry, `${tab} draws no search box`).not.toBeNull()
    expect(geometry!.search.left - geometry!.leftmost.left, `${tab} draws ${geometry!.leftmost.what} left of its search box`).toBeLessThanOrEqual(1)
    expect(geometry!.search.top - geometry!.topmost.top, `${tab} draws ${geometry!.topmost.what} above its search box`).toBeLessThanOrEqual(1)
  }
})

test('an eighty-character project name is cut in the top bar and the controls stay in view', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)
  const project = await seed(page, 'longname')
  const longName = `075-${'w'.repeat(76)}`
  expect(longName.length).toBe(80)

  await page.goto(`/projects/${project.id}/overview`, { waitUntil: 'networkidle' })
  await page.fill('[data-testid="project-settings-name"]', longName)
  await page.click('[data-testid="project-settings-save"]')
  await expect(page.locator('[data-testid="project-settings-saved"]')).toBeVisible({ timeout: 15_000 })

  const name = page.locator('[data-testid="project-crumb-name"]')
  await expect(name).toHaveText(longName)
  const box = await name.boundingBox()
  expect(box!.width, 'the top bar name is not capped at 360px').toBeLessThanOrEqual(360)
  const metrics = await name.evaluate((node) => ({ scroll: node.scrollWidth, client: node.clientWidth }))
  expect(metrics.scroll, 'the long name is not cut').toBeGreaterThan(metrics.client)

  // The shell's own controls, the layer link and the directory all stay on
  // screen: the name is what gives way.
  const viewport = page.viewportSize()
  expect(viewport).not.toBeNull()
  for (const id of ['project-crumb-back', 'new-agent', 'theme-toggle', 'project-crumb-root']) {
    const rect = await page.locator(`[data-testid="${id}"]`).boundingBox()
    expect(rect, `${id} is not on screen`).not.toBeNull()
    expect(rect!.x, `${id} was pushed off the left edge`).toBeGreaterThanOrEqual(0)
    expect(rect!.x + rect!.width, `${id} was pushed off the right edge`).toBeLessThanOrEqual(viewport!.width)
  }
  await pageShot(page, '075-long-name.png')
})
