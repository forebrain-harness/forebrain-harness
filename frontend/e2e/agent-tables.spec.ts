import { mkdirSync, rmSync, utimesSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

import { expect, test, type Page } from '@playwright/test'

import { expectAssistantReply, signIn } from './support'

/**
 * The menu bar's primary-agent pages as the design draws them (plan 073): no
 * title inside the content (the top bar names the page), one toolbar above the
 * table whose left end is the search box, the table with its own fixed
 * columns, a pager under it saying how many rows there are, and the page's
 * description as one note under the pager.
 *
 * Every list is the server's: the search, the filters, the column order and
 * the page are applied there, and the pinned projects stay on top of the
 * projects table whatever the column order is. Each row's actions are icons
 * that name themselves with the app's one tip; a row keeps a hole where it has
 * no action so the icons line up down the column.
 *
 * The eight pages, the data they need seeded, and the one thing this run
 * cannot seed — a subagent genuinely in flight — are noted where they are used.
 */

const home = process.env.E2E_HOME ?? ''
const parent = process.env.E2E_PROJECT_PARENT ?? ''
/** The review's own screenshot directory, outside the run's shots dir. */
const shots = process.env.TMPDIR ?? '/tmp'

const PAGES = [
  { path: '/projects', name: 'projects' },
  { path: '/rules', name: 'rules' },
  { path: '/skills', name: 'skills' },
  { path: '/subagents', name: 'subagents' },
  { path: '/providers', name: 'providers' },
  { path: '/tools', name: 'tools' },
  { path: '/permissions', name: 'permissions' },
  { path: '/memories', name: 'memories' },
]

test.beforeEach(async ({ page }) => {
  // The pages' copy is the design's Chinese; pinning it keeps the assertions
  // reading the same words the prototype draws.
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
})

async function pageShot(page: Page, name: string): Promise<void> {
  // Nothing hovered: a tip left over from the page before would be in the shot.
  await page.mouse.move(2, 2)
  await page.screenshot({ path: join(shots, `${name}.png`), fullPage: true })
}

/** The names the seeded projects carry; one search term finds exactly them. */
const SEED_PREFIX = 'e2e-tbl'

interface SeededProject {
  name: string
  id: string
}

/**
 * Register `count` projects under one scratch directory and return them by
 * name, `e2e-tbl-00` first. Their names share a prefix so the table can be
 * scoped to them by its search box, whatever else the run has registered.
 */
async function seedProjects(page: Page, count: number): Promise<SeededProject[]> {
  if (!parent) throw new Error('E2E_PROJECT_PARENT must point outside the forebrain home')
  const seeded: SeededProject[] = []
  for (let i = 0; i < count; i++) {
    const name = `${SEED_PREFIX}-${String(i).padStart(2, '0')}`
    const root = join(parent, 'e2e-tables', name)
    mkdirSync(root, { recursive: true })
    const res = await page.request.post('/api/v1/projects', { data: { name, root, trust: true } })
    if (!res.ok()) throw new Error(`seed project ${name} failed: ${res.status()} ${await res.text()}`)
    seeded.push({ name, id: String(((await res.json()) as { id: string }).id) })
  }
  return seeded
}

/** Undo seedProjects: the rows and their directories both go. */
async function dropProjects(page: Page, seeded: SeededProject[]): Promise<void> {
  for (const project of seeded) {
    await page.request.delete(`/api/v1/projects/${encodeURIComponent(project.id)}`)
  }
  if (parent) rmSync(join(parent, 'e2e-tables'), { recursive: true, force: true })
}

/**
 * One full turn in a new conversation of `project`: the projects table's 对话
 * column counts the conversations that hold a message, so a seeded session
 * with nothing said in it would count zero.
 */
async function seedConversation(page: Page, project: SeededProject): Promise<void> {
  const created = await page.request.post('/api/chat/sessions', { data: { project_id: project.id } })
  if (!created.ok()) throw new Error(`seed session failed: ${created.status()} ${await created.text()}`)
  const session = String(((await created.json()) as { id: string }).id)
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')
  await page.fill('textarea', '记一下：这个项目里有一句话')
  await page.press('textarea', 'Enter')
  await expectAssistantReply(page)
}

/** The memory files the memory table is asserted over, stamped seconds apart. */
function seedMemoryFiles(count: number): void {
  for (let i = 0; i < count; i++) {
    const path = join(home, 'workspace', 'memories', 'global', 'notes', `073seed-${String(i).padStart(2, '0')}.md`)
    mkdirSync(join(home, 'workspace', 'memories', 'global', 'notes'), { recursive: true })
    writeFileSync(path, `073seed memory ${i}\n`)
    // Sorting needs real time gaps: a burst of writes shares one mtime and
    // every order would collapse onto the path tie-break.
    const when = new Date(Date.now() - (count - i) * 1000)
    utimesSync(path, when, when)
  }
}

/** One rule on the permissions page, written through the endpoint the page uses. */
async function seedPermissionRule(page: Page): Promise<void> {
  const res = await page.request.post('/api/permissions/updates', {
    data: {
      type: 'addRules',
      destination: 'localSettings',
      behavior: 'allow',
      rules: [{ tool_name: 'shell', rule_content: '/tmp/e2e-073-nothing-*' }],
    },
  })
  if (!res.ok()) throw new Error(`seed permission rule failed: ${res.status()} ${await res.text()}`)
}

/** One skill in the primary agent's own layer, so its row owns its actions. */
function seedAgentSkill(name: string): void {
  const root = join(home, 'workspace', 'skills', name)
  mkdirSync(root, { recursive: true })
  writeFileSync(join(root, 'SKILL.md'), `---\nname: ${name}\ndescription: a skill seeded for the table e2e\n---\n\nbody\n`)
}

test('every page is a toolbar, a table and a pager — and no title row', async ({ page }) => {
  test.info().setTimeout(180_000)
  await signIn(page)

  for (const { path, name } of PAGES) {
    await page.goto(path, { waitUntil: 'networkidle' })

    // The top bar names the page; the content has no heading of its own.
    await expect(page.locator('main h1, main h2'), `${path} still draws a heading`).toHaveCount(0)
    await expect(page.locator('.forebrain-page-top-title'), `${path} still draws the title row`).toHaveCount(0)

    // The first row is the toolbar, and its left end is the search box.
    const toolbar = page.locator('main .forebrain-list-toolbar')
    await expect(toolbar, `${path} has no toolbar`).toHaveCount(1)
    const firstChild = await toolbar.evaluate((node) => {
      const first = node.firstElementChild as HTMLElement | null
      return first ? `${first.tagName.toLowerCase()}|${(first as HTMLInputElement).type ?? ''}` : ''
    })
    expect(firstChild, `${path}'s toolbar does not open with the search box`).toBe('input|search')

    // The table (or its empty state), then the pager, then the note.
    const pager = page.locator('[data-testid="table-pager"]')
    await expect(pager, `${path} has no pager`).toHaveCount(1)
    await expect(page.locator('p.forebrain-list-note')).not.toHaveText('')

    const toolbarBox = await toolbar.boundingBox()
    const pagerBox = await pager.boundingBox()
    expect(toolbarBox?.y ?? 0, `${path}'s toolbar is not above its pager`).toBeLessThan(pagerBox?.y ?? 0)
    const table = page.locator('main table.forebrain-table').first()
    if (await table.count()) {
      const tableBox = await table.boundingBox()
      expect(toolbarBox?.y ?? 0, `${path}'s table is not under its toolbar`).toBeLessThan(tableBox?.y ?? 0)
      expect(tableBox?.y ?? 0, `${path}'s pager is not under its table`).toBeLessThan(pagerBox?.y ?? 0)
    }
  }
})

test('the project table pages, sorts from its header and pins to the top', async ({ page }) => {
  test.info().setTimeout(180_000)
  await signIn(page)

  const seeded = await seedProjects(page, 23)
  try {
    // Built before the table is opened: the third project holds one
    // conversation, and the twelfth is the pinned one — a name in the middle
    // of the alphabet, so leading the table can only be the pin's doing.
    await seedConversation(page, seeded[3])
    const pinned = seeded[11]
    const pin = await page.request.post(`/api/v1/projects/${encodeURIComponent(pinned.id)}/pin`, { data: { pinned: true } })
    expect(pin.ok(), await pin.text()).toBeTruthy()

    await page.goto('/projects', { waitUntil: 'networkidle' })
    await page.fill('[data-testid="projects-search"]', SEED_PREFIX)

    // Twenty-three seeded rows: a total, and three pages of ten.
    await expect(page.locator('[data-testid="pager-total"]')).toHaveText(/共 23 条/, { timeout: 15_000 })
    await expect(page.locator('[data-testid="pager-page-1"]')).toHaveAttribute('aria-current', 'page')
    await expect(page.locator('[data-testid="pager-page-2"]')).toBeVisible()
    await expect(page.locator('[data-testid="pager-page-3"]')).toBeVisible()
    await expect(page.locator('[data-testid="pager-page-4"]')).toHaveCount(0)

    const rows = page.locator('[data-project-row]')
    await expect(rows).toHaveCount(10)
    // The default order is 更新时间 新→旧, and the pin leads it.
    await expect(rows.first()).toHaveAttribute('data-project-row', pinned.name)

    // 对话 counts the conversations that hold a message.
    const nameHeader = page.locator('th.forebrain-sortable', { hasText: '项目' })
    await nameHeader.locator('button').click()
    await expect(nameHeader).toHaveAttribute('aria-sort', 'ascending')
    // A→Z, and the pinned project is still the first row.
    await expect(rows.first()).toHaveAttribute('data-project-row', pinned.name)
    await expect(page.locator(`[data-project-row="${seeded[3].name}"] td`).nth(3)).toHaveText('1')
    await expect(page.locator(`[data-project-row="${seeded[0].name}"] td`).nth(3)).toHaveText('0')

    // Z→A turns the column around; the pin still leads.
    await nameHeader.locator('button').click()
    await expect(nameHeader).toHaveAttribute('aria-sort', 'descending')
    await expect(rows.first()).toHaveAttribute('data-project-row', pinned.name)

    // Pinning a row from the table takes it to the top of the same view.
    await nameHeader.locator('button').click()
    await expect(nameHeader).toHaveAttribute('aria-sort', 'ascending')
    const first = page.locator(`[data-project-row="${seeded[0].name}"]`)
    await first.locator('button[aria-label^="置顶"]').click()
    await expect(rows.first()).toHaveAttribute('data-project-row', seeded[0].name)
    await expect(first.locator('.forebrain-badge', { hasText: '置顶' })).toBeVisible()
    // The total is the search's, not the page's: pinning changes no row count.
    await expect(page.locator('[data-testid="pager-total"]')).toHaveText(/共 23 条/)
  } finally {
    await dropProjects(page, seeded)
  }
})

test('the tool table filters on the server and opens its inputs in place', async ({ page }) => {
  test.info().setTimeout(120_000)
  await signIn(page)

  // The category dropdown is the runtime's own list, whatever the filters keep.
  const answer = await page.request.get('/api/tools?page=1&page_size=10')
  expect(answer.ok(), await answer.text()).toBeTruthy()
  const body = (await answer.json()) as { categories?: string[] }
  const categories = Array.isArray(body.categories) ? body.categories : []
  expect(categories.length).toBeGreaterThan(0)

  await page.goto('/tools', { waitUntil: 'networkidle' })
  const options = await page.locator('[data-testid="tools-category-filter"] option').evaluateAll((nodes) =>
    nodes.map((node) => (node as HTMLOptionElement).value),
  )
  // The first option is 全部分类; the rest are the server's categories, in order.
  expect(options.slice(1)).toEqual(categories)

  // 只读 is a server filter: every row left is read-only.
  await page.selectOption('[data-testid="tools-type-filter"]', 'readonly')
  const rows = page.locator('[data-tool-row]')
  await expect(rows.first()).toBeVisible({ timeout: 15_000 })
  await expect(async () => {
    const types = await rows.evaluateAll((nodes) =>
      nodes.map((node) => (node.querySelectorAll('td')[3]?.textContent ?? '').trim()),
    )
    expect(types.length).toBeGreaterThan(0)
    expect(types.every((type) => type.startsWith('只读'))).toBe(true)
  }).toPass({ timeout: 10_000 })

  // One icon opens the tool's inputs: a nested table under the row, read from
  // the JSON Schema the runtime publishes, and the icon says it will close.
  const tool = await rows.first().getAttribute('data-tool-row')
  const toggle = page.locator(`[data-testid="tool-inputs-${tool}"]`)
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  await expect(toggle).toHaveAttribute('aria-label', /^查看入参/)
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-expanded', 'true')
  await expect(toggle).toHaveAttribute('aria-label', /^收起/)

  const params = page.locator(`[data-testid="tool-params-${tool}"]`)
  await expect(params).toBeVisible()
  const headers = params.locator('thead th')
  if (await headers.count()) {
    const labels = (await headers.allInnerTexts()).map((label) => label.trim())
    expect(labels).toEqual(['参数', '类型', '必填', '说明'])
    expect(await params.locator('[data-tool-param]').count()).toBeGreaterThan(0)
  } else {
    await expect(params).toContainText('无入参')
  }

  await toggle.click()
  await expect(params).toHaveCount(0)
  await expect(toggle).toHaveAttribute('aria-expanded', 'false')
})

test('the memory table sorts from its header and pages', async ({ page }) => {
  test.info().setTimeout(120_000)
  test.skip(!home, 'E2E_HOME missing')
  seedMemoryFiles(12)
  await signIn(page)

  await page.goto('/memories', { waitUntil: 'networkidle' })
  await page.fill('[data-testid="memories-search"]', '073seed')
  await expect(page.locator('[data-testid="pager-total"]')).toHaveText(/共 12 条/, { timeout: 15_000 })

  const files = page.locator('[data-memory-file]')
  await expect(files).toHaveCount(10)
  const leading = await files.first().getAttribute('data-memory-file')

  // The pager's own page numbers are the way across the list.
  await page.click('[data-testid="pager-page-2"]')
  await expect(page.locator('[data-testid="pager-page-2"]')).toHaveAttribute('aria-current', 'page')
  await expect(files).toHaveCount(2)
  await expect(files.first()).not.toHaveAttribute('data-memory-file', leading ?? '')

  // A header click orders the whole list on the server and lands on page one.
  const created = page.locator('th.forebrain-sortable', { hasText: '创建时间' })
  await created.locator('button').click()
  await expect(created).toHaveAttribute('aria-sort', 'descending')
  await expect(page.locator('[data-testid="pager-page-1"]')).toHaveAttribute('aria-current', 'page')
  await expect(page.locator('[data-testid="pager-page-2"]')).not.toHaveAttribute('aria-current', 'page')
  await expect(files).toHaveCount(10)
})

test('every table action is an icon button that names itself', async ({ page }) => {
  test.info().setTimeout(180_000)
  await signIn(page)

  // A row on every page that can be seeded. The subagents page needs no seed:
  // it lists the agent's configuration — the harness's own subagents plus the
  // agent's — so it has rows on a fresh run.
  const seeded = await seedProjects(page, 3)
  await seedPermissionRule(page)
  seedMemoryFiles(1)
  seedAgentSkill('e2e-073-skill')

  try {
    for (const { path, name } of PAGES) {
      await page.goto(path, { waitUntil: 'networkidle' })
      await expect(page.locator('main tbody tr').first()).toBeVisible({ timeout: 15_000 })

      // The action column holds icon buttons only: no words, so no text button.
      // A cell may open with a switch — a subagent's own on/off sits in front of
      // its row actions — and the switches come first, so nothing else may
      // follow them but row actions.
      const report = await page.evaluate(() => {
        const rows = Array.from(document.querySelectorAll('main tbody tr'))
        const bad: string[] = []
        let actions = 0
        for (const row of rows) {
          const cells = row.querySelectorAll('td')
          // The tools page's inputs row is one full-width cell of its own.
          if (cells.length < 2) continue
          const last = cells[cells.length - 1]
          let afterAction = false
          for (const button of Array.from(last.querySelectorAll('button'))) {
            const name = button.getAttribute('aria-label') ?? button.textContent ?? ''
            if (button.classList.contains('forebrain-switch')) {
              if (afterAction) bad.push(`${name}: a switch after a row action`)
            } else if (button.classList.contains('forebrain-row-action')) {
              afterAction = true
              actions += 1
            } else {
              bad.push(`${name}: not a row action or a switch`)
            }
            if ((button.textContent ?? '').trim() !== '') {
              bad.push(`${button.textContent}: carries text`)
            }
          }
        }
        return { rows: rows.length, actions, bad }
      })
      expect(report.rows, `${path} has no rows to check`).toBeGreaterThan(0)
      expect(report.actions, `${path} has no row action`).toBeGreaterThan(0)
      expect(report.bad, `${path} still draws something other than a row action in its action column`).toEqual([])

      // The review's own copy of the page, taken before a tip is open.
      await pageShot(page, `073-${name}`)

      // An icon names itself with the app's one tip.
      const action = page.locator('main tbody tr td:last-child button.forebrain-row-action').first()
      await action.hover()
      const tip = page.getByRole('tooltip')
      await expect(tip, `${path}'s row action names nothing`).toBeVisible()
      expect((await tip.innerText()).trim().length).toBeGreaterThan(0)
    }
  } finally {
    await dropProjects(page, seeded)
  }
})

test('the skill table offers only the layers this page can hold', async ({ page }) => {
  test.info().setTimeout(120_000)
  test.skip(!home, 'E2E_HOME missing')
  seedAgentSkill('e2e-073-skill')
  await signIn(page)

  await page.goto('/skills', { waitUntil: 'networkidle' })
  const sources = await page.locator('[data-testid="skills-source-filter"] option').evaluateAll((nodes) =>
    nodes.map((node) => (node as HTMLOptionElement).value),
  )
  // The primary agent's page holds its own layer, the shared one and the
  // built-ins — never a project's.
  expect(sources).toEqual(['', 'agent', 'shared', 'builtin'])

  // 查看 is the eye icon, not a 详情 text button anywhere on the page.
  await expect(page.locator('main')).not.toContainText('详情')
  // The listing runs over every layer, so the search box is what brings this
  // page to the seeded row.
  await page.fill('[data-testid="skills-search"]', 'e2e-073-skill')
  await expect(page.locator('[data-testid="pager-total"]')).toHaveText(/共 1 条/, { timeout: 15_000 })
  const own = page.locator('[data-skill-row="e2e-073-skill"]')
  await expect(own).toBeVisible({ timeout: 15_000 })
  const view = own.locator('[data-testid="skill-detail-e2e-073-skill"]')
  await expect(view).toBeVisible()
  expect((await view.innerText()).trim()).toBe('')
  await expect(view).toHaveAttribute('aria-label', /^查看/)
})
