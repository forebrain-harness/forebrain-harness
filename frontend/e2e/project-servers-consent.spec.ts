import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

import { expect, test, type Page } from '@playwright/test'

import { signIn } from './support'

/**
 * The project's servers confirmed once, on one panel (plan 113): a
 * conversation that would load the project's undecided MCP and
 * language-server entries asks about both kinds together, the answer is
 * recorded whole, the project page's "always allow" switch decides whether
 * anything is asked at all, and the two tabs show the same switch.
 *
 * The project's files are written straight to its directory the way an owner
 * would edit them: the cases read what the gateway resolves from them, they
 * never start a server (`/bin/echo` is configured, not run).
 */

/** The project's MCP file: two undecided entries to begin with. */
const MCP_TWO = [
  'mcp_servers:',
  '  - name: caveman',
  '    transport: stdio',
  '    command: /bin/echo caveman',
  '  - name: webtool',
  '    transport: stdio',
  '    command: /bin/echo webtool',
  '',
].join('\n')

const LSP_ONE = ['servers:', '  gopls: {command: gopls, args: [serve], extension_to_language: {".go": go}}', ''].join('\n')

/** A third MCP entry appended while the switch is on or off. */
const MCP_LATE = ['  - name: lateadd', '    transport: stdio', '    command: /bin/echo lateadd', ''].join('\n')

interface SeededProject {
  id: string
  root: string
}

/** A trusted, version-controlled project holding the two files, registered
 * through the gateway's own endpoint. */
async function seedConsentProject(page: Page, name: string): Promise<SeededProject> {
  const parent = process.env.E2E_PROJECT_PARENT
  if (!parent) throw new Error('E2E_PROJECT_PARENT must point outside the forebrain home')
  const root = join(parent, 'e2e-seed', `${name}-${Date.now().toString(36)}`)
  mkdirSync(join(root, '.git'), { recursive: true })
  mkdirSync(join(root, '.forebrain'), { recursive: true })
  writeFileSync(join(root, '.forebrain', 'mcp_servers.yaml'), MCP_TWO)
  writeFileSync(join(root, '.forebrain', 'lsp_servers.yaml'), LSP_ONE)
  const res = await page.request.post('/api/v1/projects', { data: { name, root, trust: true } })
  let id = ''
  if (res.status() === 409) {
    const body = (await res.json()) as { project?: { id?: string } }
    id = String(body.project?.id ?? '')
  } else if (res.ok()) {
    id = String(((await res.json()) as { id: string }).id)
  } else {
    throw new Error(`seed project failed: ${res.status()} ${await res.text()}`)
  }
  if (!id) throw new Error(`seed project at ${root} answered no id`)
  return { id, root }
}

/** A new conversation under the project — the act that would load its servers. */
async function newSession(page: Page, projectId: string): Promise<string> {
  const res = await page.request.post('/api/chat/sessions', { data: { project_id: projectId } })
  if (!res.ok()) throw new Error(`create session failed: ${res.status()} ${await res.text()}`)
  return String(((await res.json()) as { id: string }).id)
}

/** The consent dialog's root, when it is up. */
const dialog = (page: Page) => page.locator('[data-testid="project-servers-consent"]')

test('one panel asks about both kinds, and the answer lands on the project pages', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)
  const project = await seedConsentProject(page, 'srv-113')
  try {
    // A conversation under the project opens with the one panel: both groups,
    // everything checked, the switch off, the count in the subtitle.
    await page.goto(`/?session=${await newSession(page, project.id)}`, { waitUntil: 'networkidle' })
    await expect(dialog(page)).toBeVisible({ timeout: 15_000 })
    await expect(dialog(page)).toContainText('3 个服务要在本项目运行')
    await expect(dialog(page)).toContainText('MCP')
    await expect(dialog(page)).toContainText('语言服务器')
    await expect(page.locator('[data-testid="project-servers-check-caveman"]')).toHaveAttribute('data-state', 'checked')
    await expect(page.locator('[data-testid="project-servers-check-gopls"]')).toHaveAttribute('data-state', 'checked')
    await expect(page.locator('[data-testid="project-servers-always"]')).toHaveAttribute('data-state', 'unchecked')
    await page.mouse.move(2, 2)
    await page.screenshot({ path: join(process.env.E2E_SHOTS!, '113-consent-dialog.png'), fullPage: true })

    // webtool left unchecked is declined by the same answer; 确定 records the
    // whole panel and the dialog goes away.
    await page.click('[data-testid="project-servers-check-webtool"]')
    await page.click('[data-testid="project-servers-confirm"]')
    await expect(dialog(page)).toHaveCount(0, { timeout: 15_000 })

    // The project pages hold what was answered: one MCP entry allowed, one
    // declined with the way back, the language server allowed.
    await page.goto(`/projects/${project.id}/mcp`, { waitUntil: 'networkidle' })
    await expect(page.locator('[data-mcp-row="project:caveman"] .forebrain-badge', { hasText: '已允许' })).toBeVisible({
      timeout: 15_000,
    })
    await expect(page.locator('[data-mcp-row="project:webtool"] .forebrain-badge', { hasText: '已拒绝' })).toBeVisible()
    await page.screenshot({ path: join(process.env.E2E_SHOTS!, '113-mcp-after-answer.png'), fullPage: true })
    await page.goto(`/projects/${project.id}/lsp`, { waitUntil: 'networkidle' })
    await expect(page.locator('[data-lsp-row="gopls"]').getByText('已允许')).toBeVisible({ timeout: 15_000 })
    await page.screenshot({ path: join(process.env.E2E_SHOTS!, '113-lsp-after-answer.png'), fullPage: true })

    // Everything was decided, so the next conversation asks nothing.
    await page.goto(`/?session=${await newSession(page, project.id)}`, { waitUntil: 'networkidle' })
    await expect(dialog(page)).toHaveCount(0, { timeout: 15_000 })
  } finally {
    await page.request.delete(`/api/v1/projects/${project.id}`)
  }
})

test('the always-allow switch decides whether a new entry is asked about', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)
  const project = await seedConsentProject(page, 'srv-113b')
  try {
    // Answer the panel first (allow everything), so only the switch moves
    // from here on.
    await page.goto(`/?session=${await newSession(page, project.id)}`, { waitUntil: 'networkidle' })
    await expect(dialog(page)).toBeVisible({ timeout: 15_000 })
    await page.click('[data-testid="project-servers-confirm"]')
    await expect(dialog(page)).toHaveCount(0, { timeout: 15_000 })

    // The switch sits at the toolbar's left edge on both tabs, and it is one
    // switch: turning it on the MCP tab reads on the LSP tab too.
    await page.goto(`/projects/${project.id}/mcp`, { waitUntil: 'networkidle' })
    const label = page.locator('[data-testid="project-servers-auto-allow"]')
    await expect(label).toBeVisible({ timeout: 15_000 })
    await expect(label).toContainText('总是允许本项目的服务')
    await expect(page.locator('[data-testid="project-servers-auto-allow-switch"]')).toHaveAttribute(
      'data-state',
      'unchecked',
    )
    await page.click('[data-testid="project-servers-auto-allow-switch"]')
    await expect(page.locator('[data-testid="project-servers-auto-allow-switch"]')).toHaveAttribute(
      'data-state',
      'checked',
    )
    await page.goto(`/projects/${project.id}/lsp`, { waitUntil: 'networkidle' })
    await expect(page.locator('[data-testid="project-servers-auto-allow-switch"]')).toHaveAttribute(
      'data-state',
      'checked',
      { timeout: 15_000 },
    )
    await page.screenshot({ path: join(process.env.E2E_SHOTS!, '113-lsp-switch-on.png'), fullPage: true })

    // An entry appended to the file with the switch on loads without asking:
    // no panel, and the tab lists it as allowed.
    writeFileSync(join(project.root, '.forebrain', 'mcp_servers.yaml'), MCP_TWO + MCP_LATE)
    await page.goto(`/?session=${await newSession(page, project.id)}`, { waitUntil: 'networkidle' })
    await expect(dialog(page)).toHaveCount(0, { timeout: 15_000 })
    await page.goto(`/projects/${project.id}/mcp`, { waitUntil: 'networkidle' })
    await expect(page.locator('[data-mcp-row="project:lateadd"] .forebrain-badge', { hasText: '已允许' })).toBeVisible({
      timeout: 15_000,
    })
    await page.screenshot({ path: join(process.env.E2E_SHOTS!, '113-mcp-lateadd-allowed.png'), fullPage: true })

    // The switch off puts the appended entry back to waiting, and the next
    // conversation asks about it again — about it alone, everything else was
    // decided.
    await page.click('[data-testid="project-servers-auto-allow-switch"]')
    await expect(page.locator('[data-testid="project-servers-auto-allow-switch"]')).toHaveAttribute(
      'data-state',
      'unchecked',
    )
    await expect(page.locator('[data-mcp-row="project:lateadd"] .forebrain-badge', { hasText: '待确认' })).toBeVisible({
      timeout: 15_000,
    })
    await page.screenshot({ path: join(process.env.E2E_SHOTS!, '113-mcp-lateadd-pending.png'), fullPage: true })
    await page.goto(`/?session=${await newSession(page, project.id)}`, { waitUntil: 'networkidle' })
    await expect(dialog(page)).toBeVisible({ timeout: 15_000 })
    await expect(dialog(page)).toContainText('1 个服务要在本项目运行')
    await expect(page.locator('[data-testid="project-servers-check-lateadd"]')).toHaveCount(1)
    await page.mouse.move(2, 2)
    await page.screenshot({ path: join(process.env.E2E_SHOTS!, '113-lateadd-asks-again.png'), fullPage: true })
  } finally {
    await page.request.delete(`/api/v1/projects/${project.id}`)
  }
})
