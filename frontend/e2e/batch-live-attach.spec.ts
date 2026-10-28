import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { expect, test, type Browser, type Page } from '@playwright/test'

import { shot, signIn } from './support'

/**
 * Watching a run someone else started: a second browser opens a task
 * execution that is already going and draws it live — the answer grows as the
 * run writes it, and the page follows the run to its end — without ever
 * reloading itself and without asking the task how it is doing.
 *
 * The run is started by a different browser context, so nothing about this page
 * is a sender: it attaches to the session it was opened on and draws the
 * fragments the gateway pushes to it. Its status is the execution's own, which
 * the engine publishes as it moves (a `batch_execution` frame, durable with the
 * conversation), so the page is *told* the run started and ended. The task is
 * read once, when the page opens: every request it makes to /api/v1/tasks/ is
 * counted, and the count must not grow while the run plays out.
 *
 * The provider writes one word every BATCH_LIVE_STEP seconds under
 * `[[e2e:batch-live]]`, so the watching page has words that can only have
 * arrived as fragments: no snapshot can carry them before the run ends.
 */

const TASK_CONTENT = `[[e2e:batch]] [[e2e:batch-live]]\n\n把本月的对账单整理成一份表格。`

/** One browser watching an execution: its page, and every task read it made. */
interface Watcher {
  page: Page
  taskReads: () => string[]
  loads: () => number
}

/**
 * One browser set up to watch a run: its own context, signed in, counting every
 * task read and every document load before the page goes anywhere. The caller
 * opens the run — the counters have to be in place first, which is the point:
 * an execution's state is the engine's to report, so a page that polls the task
 * for it is the page guessing.
 */
async function watch(browser: Browser): Promise<Watcher> {
  const ctx = await browser.newContext()
  const page = await ctx.newPage()
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)
  const reads: string[] = []
  let loads = 0
  page.on('request', (request) => {
    if (request.url().includes('/api/v1/tasks/')) reads.push(request.url())
  })
  page.on('load', () => { loads += 1 })
  return { page, taskReads: () => [...reads], loads: () => loads }
}

const scratch: string[] = []

test.afterAll(() => {
  for (const dir of scratch) rmSync(dir, { recursive: true, force: true })
})

interface StartedTask {
  projectId: string
  taskId: string
}

/** A batch project and a task made from its list: its first execution runs. */
async function startTask(page: Page): Promise<StartedTask> {
  const root = mkdtempSync(join(tmpdir(), 'fb-e2e-184-live-'))
  mkdirSync(root, { recursive: true })
  scratch.push(root)
  const created = await page.request.post('/api/v1/projects', {
    data: { name: `e2e-184-live-${Date.now().toString(36)}`, root, project_type: 'batch', trust: true },
  })
  expect(created.ok(), await created.text()).toBeTruthy()
  const project = (await created.json()) as { id: string }

  await page.goto(`/projects/${encodeURIComponent(project.id)}/tasks`, { waitUntil: 'networkidle' })
  await page.click('[data-testid="tasks-new"]')
  await page.fill('[data-testid="task-new-content"]', TASK_CONTENT)
  await page.click('[data-testid="task-new-submit"]')
  await page.waitForURL(/\/projects\/[^/]+\/tasks\/[^/]+\/runs\/1(\?|$)/, { timeout: 60_000 })

  const segments = new URL(page.url()).pathname.split('/').filter(Boolean)
  const projectId = decodeURIComponent(segments[segments.indexOf('projects') + 1] ?? '')
  const taskId = decodeURIComponent(segments[segments.indexOf('tasks') + 1] ?? '')
  // The run's turn is admitted just after the execution opens; the watcher
  // attaches to a run that is actually going.
  await expect.poll(async () => {
    const res = await page.request.get(`/api/v1/tasks/${encodeURIComponent(taskId)}?page=1&limit=10`)
    const body = (await res.json()) as { executions?: { items?: Array<{ status?: string }> } }
    return String(body.executions?.items?.[0]?.status ?? '')
  }, { timeout: 60_000 }).toBe('running')
  return { projectId, taskId }
}

test('a second browser draws a running execution live, and follows it to its end', async ({ browser }) => {
  test.info().setTimeout(300_000)

  // A: the browser that starts the task. It is never the page under test.
  const ctxA = await browser.newContext()
  const pageA = await ctxA.newPage()
  await pageA.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(pageA)
  const { projectId, taskId } = await startTask(pageA)
  const runUrl = `/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}/runs/1`

  // B: a separate browser that only ever watches this execution.
  const watcher = await watch(browser)
  const { page: pageB } = watcher
  const loads = watcher.loads

  await pageB.goto(runUrl, { waitUntil: 'networkidle' })

  const status = pageB.locator('[data-testid="batch-run-status"]')
  const answer = pageB.locator('[role=log] .is-assistant').last()
  const answerText = async () => (await answer.innerText().catch(() => '')).replace(/\s+/g, ' ').trim()
  const stillRunning = async () => (await status.innerText().catch(() => '')).trim() === '运行中'

  await expect(status).toHaveText('运行中', { timeout: 30_000 })
  // What the run had already said arrives whole when the page binds: the
  // in-flight text the gateway holds is pushed on attach.
  await expect.poll(answerText, { timeout: 30_000 }).toContain('working')
  // The first screen: reading the task is how the page finds the run at all —
  // once, and never again. The shell resolves the tenant after the page has
  // mounted, and that resolution must not rebuild the page underneath it:
  // everything the execution does from here is sent.
  const readsBeforeRunEnds = watcher.taskReads().length
  expect(readsBeforeRunEnds).toBe(1)

  // Every word after that travels only as a fragment: the run is still going,
  // so no reload of the transcript can carry it. Seeing them is the attach.
  await expect.poll(async () => (await stillRunning()) && (await answerText()).includes('slowly'),
    { timeout: 30_000 }).toBe(true)
  const firstSeen = await answerText()
  await expect.poll(async () => (await stillRunning()) && (await answerText()).includes('surely'),
    { timeout: 30_000 }).toBe(true)
  expect((await answerText()).length).toBeGreaterThan(firstSeen.length)

  // Not one of those words cost a reload.
  expect(loads()).toBe(1)

  // The run ends on its own; the watching page follows it there by itself,
  // still without a reload — and without asking the task even once.
  await expect(status).toHaveText('已完成', { timeout: 120_000 })
  expect(loads()).toBe(1)
  expect(watcher.taskReads()).toHaveLength(readsBeforeRunEnds)
  console.log(`[batch-live-attach] /api/v1/tasks/ reads: ${readsBeforeRunEnds} on the first screen, ${watcher.taskReads().length} by the end`)
  await expect.poll(answerText, { timeout: 30_000 }).toContain('steadily')
  await shot(pageB, 'batch-live-attach')

  await ctxA.close()
  await pageB.context().close()
})

/**
 * A page opened on an execution that is already going is told the state it is
 * in — the engine's own running frame, which the snapshot the page binds with
 * replays — and it stays that way until the run ends, without the page ever
 * asking the task how it is doing.
 */
test('a page opened during the run shows it running from what the engine said', async ({ browser }) => {
  test.info().setTimeout(300_000)

  const ctxA = await browser.newContext()
  const pageA = await ctxA.newPage()
  await pageA.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(pageA)
  const { projectId, taskId } = await startTask(pageA)
  const runUrl = `/projects/${encodeURIComponent(projectId)}/tasks/${encodeURIComponent(taskId)}/runs/1`

  const watcher = await watch(browser)
  const status = watcher.page.locator('[data-testid="batch-run-status"]')
  await watcher.page.goto(runUrl, { waitUntil: 'networkidle' })

  // The page opened mid-run: 运行中 is the state it is told, and the only read
  // it made is the one that found the run it was opened on.
  await expect(status).toHaveText('运行中', { timeout: 30_000 })
  await watcher.page.waitForTimeout(2000)
  const firstScreenReads = watcher.taskReads().length
  expect(firstScreenReads).toBe(1)

  // Still running a moment later, still without another read: nothing here is
  // asking the task whether anything changed.
  await expect(status).toHaveText('运行中')
  expect(watcher.taskReads()).toHaveLength(firstScreenReads)

  await expect(status).toHaveText('已完成', { timeout: 120_000 })
  expect(watcher.taskReads()).toHaveLength(firstScreenReads)
  expect(watcher.loads()).toBe(1)
  console.log(`[batch-live-attach] opened mid-run: /api/v1/tasks/ reads: ${firstScreenReads} on the first screen, ${watcher.taskReads().length} by the end`)

  await ctxA.close()
  await watcher.page.context().close()
})
