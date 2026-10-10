import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

import { expect, test, type Page } from '@playwright/test'

import { signIn } from './support'

/**
 * The radius scale is law (CARD_RADIUS_UNIFY_8PX): containers 8px, form
 * controls 6px, chips 5px, the composer input alone 12px, pills and circles
 * round. This walk forbids the retired values — 16/10/9/7px — on every page,
 * every settings tab, every project tab and every known dialog, and pins the
 * surviving 12px to the composer textarea whitelist, so a future card cannot
 * quietly drift back to the old scale.
 */

type Offender = { tag: string; cls: string; radius: string; text: string; testid: string | null }

const FORBIDDEN = ['16px', '10px', '9px', '7px']

/** One radius audit of the live document: every in-layout element that draws
 * a border or a background and is wide enough to show a corner must stay on
 * the scale. 12px survives only on the composer textarea. */
async function auditRadii(page: Page): Promise<Offender[]> {
  return page.evaluate((forbidden) => {
    const offenders: Offender[] = []
    for (const el of Array.from(document.querySelectorAll('*'))) {
      if (!(el instanceof HTMLElement)) continue
      const cs = getComputedStyle(el)
      if (cs.display === 'none' || cs.visibility === 'hidden') continue
      const rect = el.getBoundingClientRect()
      if (rect.width < 10 || rect.height < 1) continue
      const hasBorder = ['top', 'right', 'bottom', 'left'].some(
        (side) => parseFloat(cs.getPropertyValue(`border-${side}-width`)) > 0,
      )
      const hasBackground = cs.backgroundColor !== 'rgba(0, 0, 0, 0)'
      if (!hasBorder && !hasBackground) continue
      const corners = [
        cs.borderTopLeftRadius,
        cs.borderTopRightRadius,
        cs.borderBottomRightRadius,
        cs.borderBottomLeftRadius,
      ]
      const isComposer = el.tagName === 'TEXTAREA' && (el.getAttribute('data-testid') ?? '').includes('composer')
      for (const corner of corners) {
        const radius = corner.split(' ')[0] ?? ''
        if (forbidden.includes(radius) || (radius === '12px' && !isComposer)) {
          offenders.push({
            tag: el.tagName.toLowerCase(),
            cls: el.className.toString().slice(0, 120),
            radius,
            text: (el.textContent ?? '').trim().slice(0, 60),
            testid: el.getAttribute('data-testid'),
          })
          break
        }
      }
    }
    return offenders
  }, FORBIDDEN)
}

const ROUTES = [
  '/',
  '/workshop',
  '/skills',
  '/rules',
  '/subagents',
  '/permissions',
  '/tools',
  '/projects',
  '/cron',
  '/channels',
  '/providers',
  '/memories',
]

const SETTINGS_TABS = [
  'appearance',
  'approval',
  'agents',
  'mcp',
  'lsp',
  'hooks',
  'cron',
  'memory',
  'config',
  'runtime',
  'shared-skills',
]

const PROJECT_TABS = ['overview', 'rules', 'sessions', 'memory', 'perm', 'mcp', 'lsp', 'skills', 'cron']

test('no retired border radius anywhere on the app surface', async ({ page }) => {
  test.info().setTimeout(240_000)
  await signIn(page)

  const offenders: Array<{ where: string; hit: Offender }> = []
  const sweep = async (where: string) => {
    for (const hit of await auditRadii(page)) offenders.push({ where, hit })
  }

  for (const route of ROUTES) {
    await page.goto(route, { waitUntil: 'networkidle' })
    await sweep(route)
  }

  for (const tab of SETTINGS_TABS) {
    await page.goto(`/settings?tab=${tab}`, { waitUntil: 'networkidle' })
    await sweep(`settings:${tab}`)
  }

  // The first project's own tabs: create a scratch project the same way the
  // project-space spec does (a root outside the forebrain home).
  const parent = process.env.E2E_PROJECT_PARENT
  if (!parent) throw new Error('E2E_PROJECT_PARENT must point at a directory outside the forebrain home')
  const root = join(parent, 'e2e-proj', 'e2e-radius')
  mkdirSync(root, { recursive: true })
  writeFileSync(join(root, 'README.md'), 'radius walk project')
  await page.goto('/projects', { waitUntil: 'networkidle' })
  await page.click('button:has-text("新建项目"), button:has-text("New project")')
  await page.getByRole('textbox', { name: /项目名|Project name/ }).fill('e2e-radius')
  await page.getByRole('textbox', { name: /绝对路径|absolute path/i }).fill(root)
  await page.click('button:has-text("创建"), button:has-text("Create")')
  await page.waitForURL(/\/projects\/.+\/overview/, { timeout: 15_000 })
  const projectId = page.url().match(/\/projects\/([^/]+)\//)?.[1] ?? ''
  expect(projectId, 'project created for the walk').not.toBe('')
  for (const tab of PROJECT_TABS) {
    await page.goto(`/projects/${projectId}/${tab}`, { waitUntil: 'networkidle' })
    await sweep(`project:${tab}`)
  }

  // Known dialogs. Each opens, is swept, and is dismissed by its own cancel.
  const openDialog = async (where: string, open: () => Promise<void>, close: () => Promise<void>) => {
    await open()
    await page.waitForTimeout(400) // let the overlay finish mounting
    await sweep(where)
    await close()
    await page.waitForTimeout(200)
  }

  await page.goto('/cron', { waitUntil: 'networkidle' })
  await openDialog(
    'dialog:cron-editor',
    () => page.click('[data-testid="cron-new"]'),
    () => page.click('.fixed button:has-text("取消"), .fixed button:has-text("Cancel")'),
  )

  await page.goto('/skills', { waitUntil: 'networkidle' })
  await openDialog(
    'dialog:skills-install',
    () => page.click('[data-testid="skills-online-install"]'),
    () => page.click('.fixed button:has-text("取消"), .fixed button:has-text("Cancel")'),
  )

  await page.goto('/workshop', { waitUntil: 'networkidle' })
  await openDialog(
    'dialog:workshop-new',
    () => page.click('[data-testid="workshop-new-task"]'),
    () => page.click('.fixed button:has-text("取消"), .fixed button:has-text("Cancel")'),
  )

  await page.goto('/providers', { waitUntil: 'networkidle' })
  // providers-add appends an inline row (no overlay); sweep it, then remove it.
  await page.click('[data-testid="providers-add"]')
  await page.waitForTimeout(200)
  await sweep('dialog:providers-add')
  const removeBtn = page.locator('[data-provider-row] button[data-testid^="provider-remove-"]').last()
  if ((await removeBtn.count()) > 0) await removeBtn.click()

  // The memory file editor only exists when a memory file row exists.
  await page.goto('/memories', { waitUntil: 'networkidle' })
  const editRow = page.locator('[data-testid^="memories-edit-"]').first()
  if ((await editRow.count()) > 0) {
    await openDialog(
      'dialog:memory-files',
      () => editRow.click(),
      () => page.click('[data-testid="memories-editor"] button:has-text("取消"), [data-testid="memories-editor"] button:has-text("Cancel")'),
    )
  }

  expect(offenders, JSON.stringify(offenders, null, 2)).toEqual([])
})
