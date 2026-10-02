import { expect, test, type Page } from '@playwright/test'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'

import { shot, signIn } from './support'

/**
 * The skill lifecycle across its three pages: the primary agent's page, the
 * settings shared-skills tab and the project space skills tab. Inherited rows
 * are read-only everywhere (decision D9); offline install, zip download and
 * owning-layer delete are the new surface plan 010 adds.
 */

const fixtureZip = process.env.E2E_SKILL_ZIP ?? ''

/** One project skill directory seeded before the project is created. */
function seedProject(root: string): void {
  mkdirSync(`${root}/.forebrain/skills/proj-skill`, { recursive: true })
  writeFileSync(
    `${root}/.forebrain/skills/proj-skill/SKILL.md`,
    '---\nname: proj-skill\ndescription: a project skill seeded for e2e\n---\n\nbody\n',
  )
}


/**
 * The suite shares one gateway, so demo-e2e survives between tests: upload it
 * once and reuse the row, instead of re-uploading into a 409 the no-overwrite
 * install must answer with.
 */
async function ensureDemoSkill(page: Page): Promise<void> {
  await page.goto('/skills', { waitUntil: 'networkidle' })
  if (await page.locator('[data-skill-row="demo-e2e"]').count()) return
  const upload = readFileSync(fixtureZip, null)
  await page.click('[data-testid="skills-offline-install"]')
  await page.setInputFiles('[data-testid="skills-offline-file"]', {
    name: 'demo-e2e.zip',
    mimeType: 'application/zip',
    buffer: Buffer.from(upload.buffer as ArrayBuffer, upload.byteOffset, upload.byteLength),
  })
  await page.click('[data-testid="skills-install-submit"]')
  await expect(page.locator('[data-skill-row="demo-e2e"][data-skill-origin="agent"]')).toBeVisible({ timeout: 15_000 })
}

function expectNoToggle(page: Page, name: string) {
  return expect(page.locator(`[data-skill-row="${name}"] [data-testid^="skill-toggle-"]`)).toHaveCount(0)
}

test('skills page lists built-in skills read-only with downloads', async ({ page }) => {
  await signIn(page)
  await page.goto('/skills', { waitUntil: 'networkidle' })
  // The built-in layer the gateway seeded at startup: read-only, downloadable.
  const builtinRow = page.locator('[data-skill-origin="builtin"]').first()
  await expect(builtinRow).toBeVisible({ timeout: 10_000 })
  const builtinName = await builtinRow.getAttribute('data-skill-row')
  expect(builtinName).toBeTruthy()
  await expectNoToggle(page, builtinName ?? '')
  await expect(builtinRow.locator('[data-testid^="skill-download-"]')).toBeVisible()
  await expect(builtinRow.locator('[data-testid^="skill-delete-"]')).toHaveCount(0)
  await shot(page, 'skills-agent')
})

test('offline install uploads a zip, then the toggle persists', async ({ page }) => {
  test.skip(!fixtureZip, 'E2E_SKILL_ZIP fixture missing')
  await signIn(page)
  await ensureDemoSkill(page)

  // The visible switch is the label around the checkbox; clicking it is
  // what a user does, and the checkbox input carries the state.
  const state = page.locator('[data-testid="skill-toggle-demo-e2e"]')
  await expect(state).toBeChecked()
  await page.click('[data-testid="skill-switch-demo-e2e"]')
  await expect(state).not.toBeChecked()
  // Reload proves the state is on disk, not just in the page.
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="skill-toggle-demo-e2e"]')).not.toBeChecked()
  // Shared and built-in rows never grow a toggle on this page.
  await expect(page.locator('[data-skill-origin="shared"] [data-testid^="skill-toggle-"]')).toHaveCount(0)
})

test('single download answers a zip named after the skill', async ({ page }) => {
  test.skip(!fixtureZip, 'E2E_SKILL_ZIP fixture missing')
  await signIn(page)
  await ensureDemoSkill(page)

  const downloadPromise = page.waitForEvent('download')
  await page.click('[data-testid="skill-download-demo-e2e"]')
  const download = await downloadPromise
  expect(download.suggestedFilename()).toBe('demo-e2e.zip')
})

test('batch download zips every selected skill', async ({ page }) => {
  test.skip(!fixtureZip, 'E2E_SKILL_ZIP fixture missing')
  await signIn(page)
  await ensureDemoSkill(page)

  const builtinName = await page.locator('[data-skill-origin="builtin"]').first().getAttribute('data-skill-row')
  await page.locator(`[data-skill-row="demo-e2e"] input[type=checkbox]`).first().check()
  await page.locator(`[data-skill-row="${builtinName}"] input[type=checkbox]`).first().check()
  const downloadPromise = page.waitForEvent('download')
  await page.click('[data-testid="skills-batch-download"]')
  const download = await downloadPromise
  expect(download.suggestedFilename()).toBe('skills.zip')
})

test('delete removes the installed skill; built-ins carry no delete', async ({ page }) => {
  test.skip(!fixtureZip, 'E2E_SKILL_ZIP fixture missing')
  await signIn(page)
  await ensureDemoSkill(page)

  await page.click('[data-testid="skill-delete-demo-e2e"]')
  await expect(page.locator('[data-testid="skills-delete-confirm"]')).toBeVisible()
  await page.click('[data-testid="skills-delete-confirm-ok"]')
  await expect(page.locator('[data-skill-row="demo-e2e"]')).toHaveCount(0, { timeout: 10_000 })
  // Built-ins show no delete anywhere: the UI refuses before the server would.
  await expect(page.locator('[data-skill-origin="builtin"] [data-testid^="skill-delete-"]')).toHaveCount(0)
})

test('settings shared-skills tab shows only shared and built-in rows', async ({ page }) => {
  await signIn(page)
  await page.goto('/settings', { waitUntil: 'networkidle' })
  await page.click('button[role=tab]:has-text("共享技能"), button[role=tab]:has-text("Shared skills")')
  await expect(page.locator('[data-skill-origin="builtin"]').first()).toBeVisible({ timeout: 10_000 })
  // Only the shared and built-in layers live on this tab — no other row.
  await expect(
    page.locator('[data-skill-row]:not([data-skill-origin="shared"]):not([data-skill-origin="builtin"])'),
  ).toHaveCount(0)
  // Built-ins stay read-only here too; shared rows carry the controls.
  await expect(page.locator('[data-skill-origin="builtin"] [data-testid^="skill-toggle-"]')).toHaveCount(0)
})

test('project skills tab: project rows editable, inherited rows read-only', async ({ page }) => {
  await signIn(page)
  const parent = process.env.E2E_PROJECT_PARENT ?? ''
  test.skip(!parent, 'E2E_PROJECT_PARENT missing')
  const root = `${parent}/skills-e2e-proj`
  mkdirSync(root, { recursive: true })
  writeFileSync(`${root}/README.md`, 'skills e2e project')
  seedProject(root)

  await page.goto('/projects', { waitUntil: 'networkidle' })
  await page.click('button:has-text("新建项目"), button:has-text("New project")')
  await page.getByRole('textbox', { name: /项目名|Project name/ }).fill('skills-e2e')
  await page.getByRole('textbox', { name: /绝对路径|absolute path/i }).fill(root)
  await page.click('button:has-text("创建"), button:has-text("Create")')
  await page.waitForURL(/\/projects\/.+\/overview/, { timeout: 15_000 })
  await page.click('.project-tab:has-text("技能"), .project-tab:has-text("Skills")')

  // Trust gate first — the same one the MCP tab shows.
  await page.click('[data-testid="project-trust"]')
  await expect(page.locator('[data-skill-row="proj-skill"][data-skill-origin="project"]')).toBeVisible({ timeout: 10_000 })
  await expect(page.locator('[data-testid="skill-toggle-proj-skill"]')).toBeVisible()
  // Inherited layers load with the project but stay read-only here.
  await expect(page.locator('[data-skill-origin="agent"] [data-testid^="skill-toggle-"]')).toHaveCount(0)
  await expect(page.locator('[data-skill-origin="shared"] [data-testid^="skill-toggle-"]')).toHaveCount(0)
  await expect(page.locator('[data-skill-origin="builtin"] [data-testid^="skill-toggle-"]')).toHaveCount(0)
  await shot(page, 'skills-project')

  rmSync(root, { recursive: true, force: true })
})

test('install dialogs: public-internet hint and no rar in the picker', async ({ page }) => {
  await signIn(page)
  await page.goto('/skills', { waitUntil: 'networkidle' })
  await page.click('[data-testid="skills-online-install"]')
  await expect(page.locator('[data-testid="skills-install-dialog"]')).toBeVisible()
  await expect(page.getByText(/在线安装需要访问公网|requires access to the public internet/)).toBeVisible()
  await page.getByRole('button', { name: /取消|Cancel/ }).click()
  await expect(page.locator('[data-testid="skills-install-dialog"]')).toHaveCount(0)
  await page.click('[data-testid="skills-offline-install"]')
  await expect(page.locator('[data-testid="skills-install-dialog"]')).toBeVisible()
  const accept = await page.locator('[data-testid="skills-offline-file"]').getAttribute('accept')
  expect(accept).not.toContain('.rar')
  await expect(page.getByText(/rar 请先转成 zip|convert rar to zip/i)).toBeVisible()
})
