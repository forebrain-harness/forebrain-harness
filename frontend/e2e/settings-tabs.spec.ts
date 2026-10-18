import { expect, test, type Page } from '@playwright/test'

import { seedProject, signIn } from './support'

/**
 * The settings page as the design draws it (plan 076): a sub-navigation of
 * three labelled groups, ten tabs that carry no heading and no card of their
 * own, forms laid flat in their tab with standard radio groups and the save at
 * the bottom, lists as a toolbar + table + pager + note, and the runtime facts
 * as one flat key/value list.
 *
 * The last case is the one-time site-wide walk that closes the plan: every
 * menu-bar page, every project tab and every settings tab is opened and asked
 * the same four questions — no heading in the content, a pager under every
 * top-level table, nothing but icon buttons in an action cell, and a tip (never
 * a native `title`) on the icons a reader can only recognise by their glyph.
 */

/** The review's own screenshot directory, outside the run's shots dir. */
const shots = process.env.TMPDIR ?? '/tmp'

/** The settings tabs, in the order the sub-navigation lists them. */
const SETTINGS_TABS = [
  'appearance',
  'approval',
  'mcp',
  'lsp',
  'hooks',
  'cron',
  'memory',
  'shared-skills',
  'config',
  'runtime',
]

/** The menu bar's own pages. The chat page is not one of them: a new chat's
 *  greeting is an `h2` by design, so it is out of this walk. */
const MENU_PAGES = [
  '/projects',
  '/rules',
  '/skills',
  '/mcp',
  '/subagents',
  '/providers',
  '/tools',
  '/permissions',
  '/memories',
]

/** Every tab of a project space, in the tab row's order. */
const PROJECT_TABS = ['overview', 'rules', 'sessions', 'memory', 'perm', 'mcp', 'lsp', 'skills', 'cron', 'channels']

/**
 * The one hook these cases put in the store, so the hooks table has a row to
 * check its action column on. It binds to an event no browser run fires
 * (compaction), so seeding it changes nothing else in the suite.
 */
const SEED_HOOKS = {
  PreCompact: [{ matcher: '', hooks: [{ type: 'command', command: 'echo 076-settings-tabs' }] }],
}

test.beforeEach(async ({ page }) => {
  // The design's copy is Chinese; pinning it keeps the assertions reading the
  // words the prototype draws.
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
})

async function openTab(page: Page, tab: string): Promise<void> {
  await page.goto(`/settings?tab=${tab}`, { waitUntil: 'networkidle' })
}

/** Write (or, with an empty map, clear) the hook store through its own route. */
async function putHooks(page: Page, hooks: Record<string, unknown>): Promise<void> {
  const res = await page.request.put('/api/hooks', { data: { hooks } })
  if (!res.ok()) throw new Error(`write hooks failed: ${res.status()} ${await res.text()}`)
}

test('the sub-navigation is three labelled groups in the design order', async ({ page }) => {
  await signIn(page)
  await page.goto('/settings', { waitUntil: 'networkidle' })

  // One flat reading of the nav: each group's small heading, then the tabs it
  // holds. The order is the design's, and the six services sit between the two
  // things every visitor tunes and the two expert surfaces.
  const structure = await page.locator('[data-testid="settings-tabs"] > *').evaluateAll((nodes) =>
    nodes.map((node) => {
      const el = node as HTMLElement
      const kind = el.classList.contains('settings-group') ? 'group' : 'tab'
      return `${kind}:${(el.textContent ?? '').trim()}`
    }),
  )
  expect(structure).toEqual([
    'group:常规',
    'tab:外观',
    'tab:审批默认',
    'group:全局服务',
    'tab:MCP',
    'tab:语言服务器',
    'tab:钩子',
    'tab:定时任务',
    'tab:记忆开关',
    'tab:共享技能',
    'group:高级',
    'tab:配置文件',
    'tab:运行状态',
  ])
})

test('none of the ten settings tabs draws a heading or a card', async ({ page }) => {
  test.info().setTimeout(180_000)
  await signIn(page)

  for (const tab of SETTINGS_TABS) {
    await openTab(page, tab)
    // The tab's own name in the sub-navigation says what it is; the content
    // starts with the first field, row or table.
    await expect(page.locator('main h2, main h3'), `${tab} still draws a heading`).toHaveCount(0)
    await expect(page.locator('main .forebrain-card'), `${tab} still draws a card`).toHaveCount(0)
    await expect(page.locator('[data-testid="settings-tabs"] button[aria-selected="true"]')).toHaveCount(1)
    await page.mouse.move(2, 2)
    await page.screenshot({ path: `${shots}/076-settings-${tab}.png`, fullPage: true })
  }
})

test('the appearance tab is standard radio groups that apply at once', async ({ page }) => {
  await signIn(page)
  await openTab(page, 'appearance')

  // Brand colour: one radio group, every option carrying its own colour chip.
  const brand = page.locator('[data-testid="appearance-brand"]')
  await expect(brand).toHaveAttribute('role', 'radiogroup')
  await expect(brand.locator('input[type="radio"]')).toHaveCount(3)
  await expect(brand.locator('.forebrain-radio-swatch')).toHaveCount(3)
  await expect(brand).toContainText('海军蓝（默认）')
  await expect(brand).toContainText('松石青')
  await expect(brand).toContainText('石墨黑')
  await expect(brand.locator('.forebrain-list-note')).toContainText('只保存在当前浏览器。')

  await brand.locator('input[value="teal"]').check()
  await expect(page.locator('html')).toHaveAttribute('data-brand', 'teal')

  // Menu bar: the same control for the rail's one dark/light choice.
  const rail = page.locator('[data-testid="appearance-rail"]')
  await expect(rail).toHaveAttribute('role', 'radiogroup')
  await rail.locator('input[value="light"]').check()
  await expect(page.locator('html')).toHaveAttribute('data-rail-theme', 'light')

  // Language: the one setting the top bar's menu also writes, so its button
  // follows the choice made here.
  await page.selectOption('[data-testid="appearance-language"]', 'en')
  await expect(page.locator('html')).toHaveAttribute('lang', 'en')
  await expect(page.locator('.forebrain-language-text')).toHaveText('English')
  await expect(page.locator('[data-testid="appearance-brand"]')).toContainText('Stored in this browser only.')
})

test('the approval default is one vertical radio group saved from the bottom', async ({ page }) => {
  await signIn(page)
  await openTab(page, 'approval')

  const group = page.locator('[data-testid="approval-default"]')
  await expect(group).toHaveAttribute('role', 'radiogroup')
  const options = group.locator('label.forebrain-radio')
  await expect(options).toHaveCount(3)

  // Vertical, one option per line, each naming both languages and explaining
  // itself on its own second line.
  const boxes = []
  for (const index of [0, 1, 2]) boxes.push(await options.nth(index).boundingBox())
  expect(boxes.map((box) => Math.round(box?.y ?? 0))).toEqual([...new Set(boxes.map((box) => Math.round(box?.y ?? 0)))])
  expect(boxes[1]!.y).toBeGreaterThan(boxes[0]!.y + 1)
  expect(boxes[2]!.y).toBeGreaterThan(boxes[1]!.y + 1)
  await expect(options.nth(0)).toContainText('只读 · Read Only')
  await expect(options.nth(1)).toContainText('默认 · Default')
  await expect(options.nth(2)).toContainText('完全访问 · Full Access')
  for (const index of [0, 1, 2]) {
    const description = options.nth(index).locator('.forebrain-radio-desc span')
    expect((await description.innerText()).trim().length, `option ${index} carries no description`).toBeGreaterThan(0)
  }

  // The save is the last thing in the tab and writes nothing until the choice
  // changes.
  const save = page.locator('[data-testid="approval-save"]')
  await expect(save).toBeDisabled()
  const saveBox = await save.boundingBox()
  expect(saveBox!.y).toBeGreaterThan(boxes[2]!.y + boxes[2]!.height - 1)

  await group.locator('input[value="full-access"]').check()
  await expect(save).toBeEnabled()
  await save.click()
  await expect(page.getByText(/已保存为全局默认。|Saved as the global default\./).first()).toBeVisible({ timeout: 10_000 })

  // The write is in forebrain.yaml: a reload opens on the new choice, with the
  // save disabled again.
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="approval-default"] input[value="full-access"]')).toBeChecked()
  await expect(page.locator('[data-testid="approval-save"]')).toBeDisabled()

  // Restore the safe default for the rest of the suite.
  await page.locator('[data-testid="approval-default"] input[value="auto"]').check()
  await page.locator('[data-testid="approval-save"]').click()
  await expect(page.getByText(/已保存为全局默认。|Saved as the global default\./).first()).toBeVisible({ timeout: 10_000 })
})

test('the table tabs open with a search box, a pager and icon-only actions', async ({ page }) => {
  test.info().setTimeout(180_000)
  await signIn(page)
  await putHooks(page, SEED_HOOKS)

  try {
    const tabs = [
      { key: 'hooks', search: '[data-testid="hooks-search"]', row: '[data-testid="hook-row"]' },
      { key: 'lsp', search: '[data-testid="lsp-search"]', row: 'tbody [data-server]' },
      { key: 'shared-skills', search: '[data-testid="shared-skills-search"]', row: '[data-skill-row]' },
    ]

    for (const { key, search, row } of tabs) {
      await openTab(page, key)

      // The toolbar's left end is the search box — the one thing every table
      // tab opens with.
      const toolbar = page.locator('main .forebrain-list-toolbar')
      await expect(toolbar).toHaveCount(1)
      const firstChild = await toolbar.evaluate((node) => {
        const first = node.firstElementChild as HTMLElement | null
        return first ? `${first.tagName.toLowerCase()}|${(first as HTMLInputElement).type ?? ''}` : ''
      })
      expect(firstChild, `${key} does not open with the search box`).toBe('input|search')
      await expect(page.locator(search)).toBeVisible()

      // A pager under the table, whatever the row count.
      await expect(page.locator('[data-testid="table-pager"]'), `${key} has no pager`).toBeVisible()

      // Rows, and an action column of icon buttons only.
      await expect(page.locator(row).first(), `${key} has no row to check`).toBeVisible({ timeout: 20_000 })
      expect(
        await page.locator('main tbody tr td:last-child button.forebrain-row-action').count(),
        `${key} offers no row action`,
      ).toBeGreaterThan(0)
      const bad = await page.evaluate(() => {
        const offenders: string[] = []
        for (const tr of Array.from(document.querySelectorAll('main tbody tr'))) {
          const cells = tr.querySelectorAll('td')
          if (cells.length < 2) continue
          const last = cells[cells.length - 1]
          for (const button of Array.from(last.querySelectorAll('button'))) {
            const text = (button.textContent ?? '').trim()
            const label = button.getAttribute('aria-label') ?? text ?? ''
            if (!button.classList.contains('forebrain-row-action')) offenders.push(`${label}: not a row action`)
            else if (text !== '') offenders.push(`${label}: carries text`)
          }
        }
        return offenders
      })
      expect(bad, `${key} still draws a text button in its action column`).toEqual([])

      // The pager sits under the table, and the note under both.
      const tableBox = await page.locator('main table.forebrain-table').first().boundingBox()
      const pagerBox = await page.locator('[data-testid="table-pager"]').boundingBox()
      expect(tableBox!.y + tableBox!.height - 1).toBeLessThan(pagerBox!.y + 1)
    }
  } finally {
    await putHooks(page, {})
  }
})

test('the runtime tab is one flat list that answers from the gateway', async ({ page }) => {
  await signIn(page)
  await openTab(page, 'runtime')

  const list = page.locator('dl.forebrain-kv')
  await expect(list).toBeVisible()
  // The health probe answers before the rest is read; the first row is it.
  await expect(list.locator('dd').first()).toContainText(/正常|OK/, { timeout: 20_000 })

  // The list is never a card: nine plain term/value pairs, in the design's
  // order, with the value the gateway (or this browser) actually holds.
  const pairs = await list.evaluate((node) => {
    const out: Record<string, string> = {}
    let term = ''
    for (const child of Array.from(node.children)) {
      if (child.tagName === 'DT') term = (child.textContent ?? '').trim()
      else if (child.tagName === 'DD') out[term] = (child.textContent ?? '').trim()
    }
    return out
  })
  expect(Object.keys(pairs)).toEqual(['状态', '版本', '监听地址', '鉴权', '主代理', '主目录', '日志', '状态库', '技能'])

  for (const label of ['状态', '版本', '监听地址', '鉴权', '主代理', '主目录', '日志', '状态库', '技能']) {
    expect(pairs[label], `${label} is empty`).toBeTruthy()
    expect(pairs[label], `${label} answered nothing`).not.toBe('—')
  }
  expect(pairs['监听地址']).toBe(new URL(String(process.env.E2E_BASE_URL)).host)
  expect(pairs['主代理']).toMatch(/^\d+ 个$/)
  expect(pairs['技能']).toMatch(/^发现 \d+ 个，启用 \d+ 个$/)
})

test('no surface draws a heading, a table without a pager, or an untitled icon', async ({ page }) => {
  test.info().setTimeout(600_000)
  await signIn(page)
  const project = await seedProject(page, '076-sweep')
  await putHooks(page, SEED_HOOKS)

  /** The icon buttons checked so far, one per page at most. */
  const checked: string[] = []
  /** What the walk actually saw, so an empty page set cannot pass silently. */
  const seen = { tables: 0, actions: 0 }

  try {
    const paths = [
      ...MENU_PAGES,
      ...SETTINGS_TABS.map((tab) => `/settings?tab=${tab}`),
      ...PROJECT_TABS.map((tab) => `/projects/${project}/${tab}`),
    ]

    let stride = 0
    for (const path of paths) {
      await page.goto(path, { waitUntil: 'networkidle' })

      // 1. The content has no heading: the top bar names the page.
      await expect(page.locator('main h1, main h2'), `${path} still draws a heading`).toHaveCount(0)

      // 2. Every top-level table has its pager under it, and 3. every action
      //    cell holds icon buttons only. Tables inside a modal, a drawer or
      //    another table are not in this walk: each carries its own affordance.
      const audit = await page.evaluate(() => {
        const hidden = (el: Element) =>
          Boolean(
            el.closest(
              '.forebrain-modal, .forebrain-modal-mask, .forebrain-drawer, [role="dialog"], .forebrain-tenant-menu, .forebrain-chip-menu, .forebrain-language-popover',
            ),
          )
        const tables = Array.from(document.querySelectorAll('table.forebrain-table'))
          .filter((table) => !table.parentElement?.closest('table'))
          .filter((table) => !hidden(table))
        const pagers = Array.from(document.querySelectorAll('[data-testid="table-pager"]')).filter((pager) => !hidden(pager))
        const withoutPager: string[] = []
        for (const table of tables) {
          const bottom = table.getBoundingClientRect().bottom
          if (!pagers.some((pager) => pager.getBoundingClientRect().top >= bottom - 1)) {
            withoutPager.push((table.querySelector('thead')?.textContent ?? '').replace(/\s+/g, ' ').trim().slice(0, 60))
          }
        }
        const badActions: string[] = []
        let actions = 0
        for (const tr of Array.from(document.querySelectorAll('main tbody tr'))) {
          if (hidden(tr)) continue
          const cells = tr.querySelectorAll('td')
          if (cells.length < 2) continue
          const last = cells[cells.length - 1]
          for (const button of Array.from(last.querySelectorAll('button'))) {
            actions += 1
            const text = (button.textContent ?? '').trim()
            const label = button.getAttribute('aria-label') ?? text
            if (!button.classList.contains('forebrain-row-action')) badActions.push(`「${label}」is not a row action`)
            else if (text !== '') badActions.push(`「${label}」carries text`)
          }
        }
        return { withoutPager, badActions, tables: tables.length, actions }
      })
      seen.tables += audit.tables
      seen.actions += audit.actions
      expect(audit.withoutPager, `${path} draws a top-level table with no pager under it`).toEqual([])
      expect(audit.badActions, `${path} draws a text button in an action cell`).toEqual([])

      // 4. Five icon-only buttons across the run: each names itself with the
      //    app's compact tip and carries no native `title` behind it.
      if (checked.length < 5) {
        const label = await checkOneIconButton(page, stride++)
        if (label) checked.push(`${path} · ${label}`)
      }
      // Nothing hovered stays hovered: a tip would land in the next reading.
      await page.mouse.move(2, 2)
    }

    expect(checked.length, `only ${checked.length} icon-only buttons were reachable: ${checked.join('; ')}`).toBe(5)
    // The walk found what it was looking for: a run against an empty app would
    // otherwise satisfy every check above by having nothing to check.
    expect(seen.tables, 'the walk found no table at all').toBeGreaterThan(10)
    expect(seen.actions, 'the walk found no row action at all').toBeGreaterThan(20)
  } finally {
    await putHooks(page, {})
  }
})

/**
 * Hover one icon-only button on the current page and check how it names
 * itself, then return what it was called. The choice walks the candidates by
 * `stride` so the five checked across a run are spread over the pages rather
 * than all taken from the first one.
 */
async function checkOneIconButton(page: Page, stride: number): Promise<string> {
  const candidates = page.locator(
    'main button:not([role="switch"]), .forebrain-topbar button:not([role="switch"]), .forebrain-rail button:not([role="switch"])',
  )
  const usable: number[] = []
  for (let index = 0; index < (await candidates.count()); index++) {
    const button = candidates.nth(index)
    if (!(await button.isVisible())) continue
    if ((await button.innerText()).trim() !== '') continue
    if (await button.isDisabled()) continue
    if (await button.evaluate((node) => Boolean(node.closest('[role="dialog"], .forebrain-modal, .forebrain-drawer')))) continue
    usable.push(index)
  }
  if (!usable.length) return ''

  const button = candidates.nth(usable[stride % usable.length])
  const label =
    (await button.getAttribute('aria-label')) ?? (await button.getAttribute('data-testid')) ?? 'an icon button'
  // The tip replaces the browser's tooltip: nothing is set natively.
  expect(await button.getAttribute('title'), `「${label}」still carries a native title`).toBeNull()
  await button.hover()
  const tip = page.getByRole('tooltip')
  await expect(tip, `「${label}」names nothing on hover`).toBeVisible()
  expect((await tip.innerText()).trim().length, `「${label}」shows an empty tip`).toBeGreaterThan(0)
  return `${label} → ${(await tip.innerText()).trim()}`
}
