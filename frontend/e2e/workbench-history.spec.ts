import { execFileSync } from 'node:child_process'
import { mkdirSync, realpathSync, rmSync, utimesSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from '@playwright/test'

import { seedSessionInRoot, shot, signIn } from './support'

/**
 * The workbench's two histories, proven on the real page (plan 085).
 *
 * A long conversation asks several rounds of questions and writes several
 * plans; the workbench keeps both as histories, one row per round or file,
 * newest first, one of them open. A plan's row carries the file's own checklist
 * progress, and 查看全文 opens the document in the same drawer the subagents
 * use — as a tab, rendered as a document rather than printed as Markdown.
 *
 * The rounds and the plan files are seeded straight into the gateway's state
 * (the actions table and the conversation's plan directory) because what a
 * model asks and writes cannot be scripted; the page is then read as a user
 * reads it.
 */

/** The gateway's state database, by name under E2E_HOME. */
function stateDb(): string {
  const home = process.env.E2E_HOME
  if (!home) throw new Error('E2E_HOME must point at the gateway FOREBRAIN_HOME')
  const found = execFileSync('find', [home, '-name', 'forebrain.state.sqlite'], { encoding: 'utf8' })
    .split('\n').map((line) => line.trim()).filter(Boolean)
  if (!found[0]) throw new Error('no state database found under E2E_HOME')
  return found[0]
}

function sqlite(sql: string): void {
  execFileSync('sqlite3', ['-cmd', '.timeout 5000', stateDb(), sql], { stdio: ['ignore', 'pipe', 'pipe'] })
}

/** SQL quoting for a raw JSON document stored as the column's text. */
function sqlJson(value: unknown): string {
  return JSON.stringify(value).replace(/'/g, "''")
}

const projectRoot = join(process.env.E2E_PROJECT_PARENT ?? '', 'e2e-bench-history')

/** The project key the gateway resolves its plan directory by: the session's
 *  project root — symlinks evaluated, exactly as memory.ProjectRoot resolves it
 *  — with its separators turned into dashes (memory.ProjectKey). */
function projectKey(): string {
  return realpathSync(projectRoot).replace(/[/\\:]/g, '-')
}

/** A scratch project directory of its own, wiped before and after: the plan
 *  files are written into the home, but the conversation still has to belong
 *  to a project the gateway will open. */
function seedFixture(): void {
  rmSync(projectRoot, { recursive: true, force: true })
  mkdirSync(projectRoot, { recursive: true })
}

test.afterEach(() => {
  rmSync(projectRoot, { recursive: true, force: true })
})

/** One plan file in the conversation's own plan directory, at a stated moment.
 *  A conversation that replans writes a new file each time, so two files are
 *  two versions of the same conversation's plan. */
function seedPlanFile(session: string, name: string, markdown: string, writtenAtMs: number): void {
  const home = process.env.E2E_HOME
  if (!home) throw new Error('E2E_HOME must point at the gateway FOREBRAIN_HOME')
  const dir = `${home}/workspace/plans/${projectKey()}/${session}`
  mkdirSync(dir, { recursive: true })
  const path = `${dir}/${name}`
  writeFileSync(path, markdown)
  const at = new Date(writtenAtMs)
  utimesSync(path, at, at)
}

/** One round of questions the conversation answered: the form it asked in
 *  payload_json and the answer it got in answer_json, the shape a real ask
 *  leaves behind. */
function seedAnsweredAsk(session: string, id: string, prompt: string, atSeconds: number): void {
  const payload = {
    kind: 'user_interaction',
    session_id: session,
    questions: [
      {
        id: '范围',
        prompt,
        options: [
          { id: 'web', label: '只改网页' },
          { id: 'engine', label: '引擎也改' },
        ],
      },
    ],
  }
  const answer = { answers: [{ question_id: '范围', option_ids: ['engine'] }] }
  sqlite(`
    INSERT INTO fb_actions(id, session_id, kind, status, payload_json, answer_json, created_at, updated_at)
      VALUES('${id}', '${session}', 'user_interaction', 'answered',
        '${sqlJson(payload)}', '${sqlJson(answer)}', ${atSeconds - 10}, ${atSeconds});
  `)
}

const NEWER_PLAN = [
  '# Plan: 网页上的计划历史',
  '',
  '- [x] 读出这个会话写过的每一份计划',
  '- [ ] 在抽屉里把计划渲染成文档',
  '- [ ] 补上验收',
  '',
  '## 为什么',
  '',
  '一个会话可以重写计划，每一版都是目录里的一个文件。',
].join('\n')

const OLDER_PLAN = [
  '# Plan: 旧的一版',
  '',
  '- [ ] 只留一份计划',
  '',
].join('\n')

test('the workbench keeps the questions it was asked and the plans it wrote', async ({ page }) => {
  test.info().setTimeout(120_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  seedFixture()
  const session = await seedSessionInRoot(page, projectRoot, 'e2e-bench-history')
  const nowMs = Date.now()
  seedPlanFile(session, 'older-plan.md', OLDER_PLAN, nowMs - 60 * 60 * 1000)
  seedPlanFile(session, 'newer-plan.md', NEWER_PLAN, nowMs - 60 * 1000)
  seedAnsweredAsk(session, 'act-e2e-ask-old', '这次只改网页吗？', Math.floor(nowMs / 1000) - 600)
  seedAnsweredAsk(session, 'act-e2e-ask-new', '这次改到哪一层？', Math.floor(nowMs / 1000) - 60)

  // The gateway hands the page both histories in the shape it expects: two
  // plans newest first, each with its text already there.
  const plans = await page.request.get(`/api/chat/sessions/${session}/plans`)
  expect(plans.ok()).toBe(true)
  const planRows = ((await plans.json()) as { plans: { name: string }[] }).plans
  expect(planRows.map((row) => row.name)).toEqual(['newer-plan.md', 'older-plan.md'])

  await page.goto(`/?session=${session}`, { waitUntil: 'networkidle' })
  await page.click('[data-testid="workbench-toggle"]')
  await expect(page.locator('[data-testid="chat-workbench"]')).toBeVisible()

  // 问答: every answered round is a row, newest first, one of them open.
  const answers = page.locator('[data-testid="bench-answers"]')
  await expect(answers).toBeVisible()
  await expect(answers).toContainText('问答')
  await expect(answers).toContainText('2 次')
  await expect(answers.getByText('1 个问题')).toHaveCount(2)
  // Two rows, newest first.
  const askRows = answers.locator('[data-testid^="bench-ask-act-e2e-ask-"]')
  await expect(askRows).toHaveCount(2)
  await expect(askRows.nth(0)).toHaveAttribute('data-testid', 'bench-ask-act-e2e-ask-new')
  await expect(askRows.nth(1)).toHaveAttribute('data-testid', 'bench-ask-act-e2e-ask-old')
  await expect(answers.locator('[data-testid="bench-ask-body"]')).toHaveCount(1)
  // The newest round is the one in front, and it shows what was answered.
  await expect(answers.locator('[data-testid="bench-ask-body"]')).toContainText('这次改到哪一层？')
  await expect(answers.locator('[data-testid="bench-ask-body"]')).toContainText('→ 引擎也改')
  await expect(answers.locator('[data-testid="bench-ask-act-e2e-ask-new"]')).toHaveAttribute('aria-expanded', 'true')

  // Opening the older round closes the newer one: a card shows one at a time.
  await answers.locator('[data-testid="bench-ask-act-e2e-ask-old"]').click()
  await expect(answers.locator('[data-testid="bench-ask-body"]')).toHaveCount(1)
  await expect(answers.locator('[data-testid="bench-ask-body"]')).toContainText('这次只改网页吗？')
  await expect(answers.locator('[data-testid="bench-ask-act-e2e-ask-new"]')).toHaveAttribute('aria-expanded', 'false')
  await shot(page, 'p085-1-workbench-answers')

  // 计划: both versions, newest first, each row carrying the file's own
  // checklist progress — read off the marks in the file, not a claim beside it.
  const plansCard = page.locator('[data-testid="bench-plans"]')
  await expect(plansCard).toBeVisible()
  await expect(plansCard).toContainText('2 份')
  const newestRow = plansCard.locator('[data-testid="bench-plan-newer-plan.md"]')
  await expect(newestRow).toContainText('网页上的计划历史')
  await expect(newestRow).toContainText('1/3')
  await expect(newestRow.locator('.lucide-square-check-icon')).toBeVisible()
  await expect(plansCard.locator('[data-testid="bench-plan-older-plan.md"]')).toContainText('0/1')
  // The bar says nothing like「最新」: progress and time are the row's facts.
  await expect(plansCard).not.toContainText('最新')

  // 查看全文 hands the document to the drawer: the workbench gives up the
  // column, and the plan arrives as a tab beside where the subagents live.
  await plansCard.locator('[data-testid="bench-plan-body"] .bench-link').click()
  await expect(page.locator('[data-testid="chat-workbench"]')).toHaveCount(0)
  const drawer = page.locator('[data-testid="subagent-drawer"]')
  await expect(drawer).toBeVisible()
  const planTab = drawer.locator('[data-testid="plan-tab-plan:newer-plan.md"]')
  await expect(planTab).toContainText('计划 · Plan: 网页上的计划历史')
  await expect(planTab.locator('[role="tab"]')).toHaveAttribute('aria-selected', 'true')

  // The file, rendered as a document: a heading, its checklist, its section.
  const doc = drawer.locator('[data-testid="plan-doc"]')
  await expect(doc).toBeVisible()
  await expect(doc.locator('.plan-doc-meta')).toContainText('plans/')
  await expect(doc.locator('.plan-doc-meta')).toContainText('newer-plan.md')
  await expect(doc.locator('.plan-doc-meta')).toContainText('1/3')
  await expect(doc.locator('h1').first()).toContainText('网页上的计划历史')
  await expect(doc.locator('li').first()).toContainText('读出这个会话写过的每一份计划')
  await expect(doc.locator('h2').first()).toContainText('为什么')
  // No composer under a document: there is nothing here to type into.
  await expect(drawer.locator('textarea')).toHaveCount(0)
  // The screenshot records the settled panel, not the renderer's fade-in or
  // the drawer's own entry slide.
  await page.waitForTimeout(600)
  // Where the drawer's collapse control settles: the workbench's has to land in
  // the same place, or switching between the two panels moves the control the
  // user is aiming at.
  const drawerCollapse = await drawer.locator('[data-testid="subagent-drawer-collapse"]').boundingBox()
  await shot(page, 'p085-2-plan-doc-tab')

  // Back to the workbench from the top bar: the drawer folds away and the
  // column returns, collapsing with the drawer's own icon.
  await page.click('[data-testid="workbench-toggle"]')
  await expect(page.locator('[data-testid="chat-workbench"]')).toBeVisible()
  await expect(page.locator('[data-testid="subagent-drawer"]')).toHaveCount(0)
  const collapse = page.locator('[data-testid="chat-workbench-close"]')
  await expect(collapse).toHaveAttribute('aria-label', '收起工作台')
  await expect(collapse.locator('.lucide-panel-right-close-icon')).toBeVisible()
  // Past the panel's own entry slide, so what is measured — and shot — is the
  // panel at rest rather than the frame it is passing through.
  await page.waitForTimeout(400)
  const workbenchCollapse = await collapse.boundingBox()
  expect(workbenchCollapse).not.toBeNull()
  expect(drawerCollapse).not.toBeNull()
  // Same pixel column, same row: one control, two panels.
  expect(Math.abs((workbenchCollapse?.x ?? 0) - (drawerCollapse?.x ?? 0))).toBeLessThanOrEqual(1)
  expect(Math.abs((workbenchCollapse?.y ?? 0) - (drawerCollapse?.y ?? 0))).toBeLessThanOrEqual(1)
  await shot(page, 'p085-3-workbench-collapse-icon')
})
