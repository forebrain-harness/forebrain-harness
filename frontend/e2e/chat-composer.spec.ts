import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

import { expect, test } from '@playwright/test'

import { seedProjectSession, shot, signIn } from './support'

/**
 * The composer's session-scoped controls, proven on the real page: the
 * reasoning-effort choice is the conversation's own (it survives a reload
 * through the session's model row), and an MCP server that cannot start is
 * said once, at the head of the thread, with the server's own words behind
 * "查看详情".
 */

test('a reasoning effort picked on the chip survives a reload', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const session = await seedProjectSession(page, 'chat-effort')
  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.waitForSelector('textarea')

  // The model chip names the effective model. Effort levels belong to the
  // model: deepseek-chat offers none (its chip stays hidden), so switch to
  // the reasoning-capable entry the agent is configured with first.
  await expect(page.locator('[data-testid="model-chip"]')).toContainText(/glm|deepseek/i)
  await page.click('[data-testid="model-chip"]')
  await page.click('[data-testid="model-option-glm-5.3-flash"]')
  await expect(page.locator('[data-testid="model-chip"]')).toContainText('glm-5.3-flash')

  // The effort chip offers the model's levels behind one menu.
  await page.click('[data-testid="effort-chip"]')
  await expect(page.locator('[data-testid="effort-option-default"]')).toBeVisible()
  await page.click('[data-testid="effort-option-high"]')

  // The choice is the conversation's own: written to its session row, read
  // back after a full reload.
  await expect(page.locator('[data-testid="effort-chip"]')).toContainText('高')
  await shot(page, 'chat-effort-chip')
  await page.reload({ waitUntil: 'networkidle' })
  await page.waitForSelector('[data-testid="effort-chip"]')
  await expect(page.locator('[data-testid="effort-chip"]')).toContainText('高', { timeout: 15_000 })
})

test('an MCP server that cannot start is said once, at the head of the thread', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  // A project whose one MCP server is a command that does not exist, allowed
  // through the same consent the project space records — so the session's
  // startup genuinely tries it and fails.
  const parent = process.env.E2E_PROJECT_PARENT
  if (!parent) throw new Error('E2E_PROJECT_PARENT must point outside the forebrain home')
  const root = join(parent, 'e2e-mcp-fail', `broken-${Date.now().toString(36)}`)
  mkdirSync(join(root, '.forebrain'), { recursive: true })
  writeFileSync(join(root, '.forebrain', 'mcp_servers.yaml'), [
    'mcp_servers:',
    '  - name: broken-e2e',
    '    transport: stdio',
    '    command: /nonexistent/forebrain-e2e-mcp-binary',
    '    args: []',
    '    startup_timeout: 2',
    '',
  ].join('\n'))

  const created = await page.request.post('/api/v1/projects', { data: { name: 'e2e-mcp-fail', root, trust: true } })
  if (!created.ok()) throw new Error(`seed project failed: ${created.status()} ${await created.text()}`)
  const projectId = String(((await created.json()) as { id: string }).id)
  const consent = await page.request.post(`/api/v1/projects/${projectId}/mcp/consent`, { data: { allow: ['broken-e2e'] } })
  if (!consent.ok()) throw new Error(`consent failed: ${consent.status()} ${await consent.text()}`)

  const session = await page.request.post('/api/chat/sessions', { data: { project_id: projectId } })
  if (!session.ok()) throw new Error(`seed session failed: ${session.status()} ${await session.text()}`)
  const sessionId = String(((await session.json()) as { id: string }).id)

  await page.goto(`/?session=${sessionId}`, { waitUntil: 'networkidle' })
  const notice = page.locator('[data-testid="mcp-notice"]')
  // The startup waits out its (2s) timeout before it reports the failure.
  await expect(notice).toBeVisible({ timeout: 30_000 })
  await expect(notice).toContainText('MCP 服务 broken-e2e')
  await expect(notice).toContainText('本对话不能用它的工具')
  await expect(notice).toContainText('修好后新开对话即可。')

  // The server's own words stay behind the toggle: one code block per failed
  // server, none of it on the line itself.
  await expect(page.locator('[data-testid="mcp-notice-detail"]')).toBeHidden()
  await page.click('[data-testid="mcp-notice-toggle"]')
  await expect(page.locator('[data-testid="mcp-notice-detail"]')).toBeVisible()
  await expect(page.locator('[data-testid="mcp-notice-detail"]')).toContainText('broken-e2e:')
  await shot(page, 'chat-mcp-notice')
})
