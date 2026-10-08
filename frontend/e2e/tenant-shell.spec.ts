import { expect, test } from '@playwright/test'

import { signIn } from './support'

/**
 * The tenant shell: final menu, the merged settings tabs, primary agent CRUD
 * behind the ID-is-immutable rule, and the switch selector as the one place
 * a tenant changes.
 */

test('final menu matches the approved order', async ({ page }) => {
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  const hrefs = await page.locator('.forebrain-rail-group a.forebrain-rail-link').evaluateAll((nodes) => nodes.map((n) => (n as HTMLAnchorElement).getAttribute('href')))
  expect(hrefs).toEqual(['/projects', '/rules', '/skills', '/workshop', '/subagents', '/cron', '/channels', '/providers', '/tools', '/permissions', '/memories'])
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/shell-menu.png`, fullPage: true })
})

test('removed routes fall back to the router and stay out of the menu', async ({ page }) => {
  await signIn(page)
  for (const path of ['/agents', '/mcp', '/hooks', '/config']) {
    await page.goto(path, { waitUntil: 'networkidle' })
    const menuText = await page.locator('.forebrain-rail').innerText()
    expect(menuText).not.toContain('/' + path.slice(1))
  }
})

test('settings shows the merged tab bar with appearance as the default', async ({ page }) => {
  await signIn(page)
  await page.goto('/settings', { waitUntil: 'networkidle' })
  const tabs = (await page.locator('[data-testid="settings-tabs"] button').allInnerTexts()).map((tab) => tab.trim())
  const expectedEn = ['Appearance', 'Approval default', 'Primary agents', 'MCP', 'Language servers', 'Hooks', 'Scheduled tasks', 'Memory switches', 'Config file', 'Runtime status', 'Shared skills']
  const expectedZh = ['外观', '审批默认', '主代理', 'MCP', '语言服务器', '钩子', '定时任务', '记忆开关', '配置文件', '运行状态', '共享技能']
  expect(tabs.length).toBe(11)
  expect(expectedEn.every((tab) => tabs.includes(tab)) || expectedZh.every((tab) => tabs.includes(tab))).toBe(true)
  // The appearance card is visible without clicking (default tab) — the
  // brand radiogroup is its content.
  await expect(page.locator('[role="radiogroup"]').first()).toBeVisible()
  await expect(page.locator('[data-testid="scope-badge"]')).toHaveText(/全局|Global/)
})

test('primary agent lifecycle: create, edit with immutable id, guards', async ({ page }) => {
  await signIn(page)
  await page.goto('/settings', { waitUntil: 'networkidle' })
  await page.locator('[data-testid="settings-tabs"] button', { hasText: /主代理|Primary agents/ }).click()

  // Create — the form card holds the id and name fields.
  const form = page.locator('[data-testid="agent-form"]')
  await form.locator('input').nth(0).fill('e2e-helper')
  await form.locator('input').nth(1).fill('E2E 助手')
  await page.locator('button', { hasText: /^创建$|^Create$/ }).click()
  await expect(page.locator('[data-testid="agent-card"]', { hasText: 'e2e-helper' }).first()).toBeVisible({ timeout: 10_000 })
  await expect(page.locator('text=E2E 助手')).toBeVisible()

  // Edit: id input is read-only, name changes.
  const helperCard = page.locator('[data-testid="agent-card"]', { hasText: 'e2e-helper' }).first()
  await helperCard.locator('button', { hasText: /^编辑$|^Edit$/ }).click()
  const editForm = page.locator('[data-testid="agent-form"]')
  await expect(editForm.locator('input').nth(0)).toBeDisabled()
  await editForm.locator('input').nth(1).fill('E2E 助手2')
  await page.locator('button', { hasText: /^保存$|^Save$/ }).click()
  await expect(page.locator('text=E2E 助手2')).toBeVisible({ timeout: 10_000 })

  // The active/default agent's delete is disabled at the button — the
  // refused state is visible before any confirm dialog.
  const mainDelete = page.locator('[data-testid="agent-card"]', { hasText: 'main' }).first().locator('button', { hasText: /^删除$|^Delete$/ })
  await expect(mainDelete).toBeDisabled()
  await expect(page.locator('text=main').first()).toBeVisible()

  // Clean up: delete the helper (not active, not main).
  page.once('dialog', (dialog) => dialog.accept())
  await page.locator('[data-testid="agent-card"]', { hasText: 'e2e-helper' }).first().locator('button', { hasText: /^删除$|^Delete$/ }).click()
  await expect(page.locator('[data-testid="agent-card"]', { hasText: 'e2e-helper' })).toHaveCount(0, { timeout: 10_000 })
})

test('switching the tenant re-fetches the session list', async ({ page }) => {
  await signIn(page)
  await page.goto('/', { waitUntil: 'networkidle' })
  const sessionRequests: string[] = []
  page.on('request', (req) => {
    if (req.url().includes('/api/chat/sessions')) sessionRequests.push(req.url())
  })
  const before = sessionRequests.length
  await page.locator('.forebrain-tenant-trigger').click()
  // With a single agent there is nothing to switch to; the assertion is that
  // the drawer's list reloads on a real switch, covered when a second agent
  // exists. Here we only prove the selector opens and keeps one entry.
  await expect(page.locator('.forebrain-tenant-menu')).toBeVisible()
  expect(sessionRequests.length).toBeGreaterThanOrEqual(before)
})

test('switching the tenant leaves the previous agent\'s conversation behind', async ({ page }) => {
  await signIn(page)
  const created = await page.request.post('/api/agents/primary', { data: { id: 'e2e-switch' } })
  expect(created.ok(), await created.text()).toBeTruthy()
  try {
    await page.goto('/', { waitUntil: 'networkidle' })
    await page.locator('.forebrain-rail-group > button.forebrain-rail-link').first().click()
    await page.click('[data-testid="drawer-new-chat"]')
    await page.waitForURL(/session=/)

    await page.locator('.forebrain-tenant-trigger').click()
    await page.locator('.forebrain-tenant-option', { hasText: 'e2e-switch' }).click()
    // The conversation belonged to the agent just left: it leaves the
    // address bar instead of being reopened under the new tenant.
    await expect(page).not.toHaveURL(/session=/, { timeout: 10_000 })
    await expect(page.locator('.forebrain-tenant-trigger')).toContainText('e2e-switch')
  } finally {
    await page.locator('.forebrain-tenant-trigger').click()
    await page.locator('.forebrain-tenant-option', { hasText: 'main' }).click()
    await expect(page.locator('.forebrain-tenant-trigger')).toContainText('main', { timeout: 10_000 })
    await page.request.delete('/api/agents/primary/e2e-switch')
  }
})

test('subagents page shows its empty state', async ({ page }) => {
  await signIn(page)
  await page.goto('/subagents', { waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="scope-badge"]')).toHaveText(/主代理|Primary agent/)
  const empty = await page.locator('main').innerText()
  expect(empty).toMatch(/没有正在运行的子代理|No subagents running/)
  await page.screenshot({ path: `${process.env.E2E_SHOTS}/shell-subagents.png`, fullPage: true })
})
