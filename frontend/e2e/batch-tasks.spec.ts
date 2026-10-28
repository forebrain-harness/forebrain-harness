import { mkdirSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { expect, test, type Page } from '@playwright/test'

import { shot, signIn } from './support'

/**
 * A batch project's tasks, on the real pages (plan 153 B6/A11): a task created
 * from the task list runs its first execution at once, and that execution is
 * read in a conversation with no input in it — a batch run is looked at, never
 * joined. The acts a viewer may ask for follow the run's state and nothing
 * else: 停止 while it runs, 继续 on a stopped one, 重新执行 on a failed one —
 * and running it again opens execution #2 of the same task, not a new task.
 *
 * The run is made stoppable by the fake provider's batch marker: a turn
 * carrying it holds its answer for half a minute, so the page has something to
 * stop. The continue turn of a stopped execution is refused with a 400 — the
 * provider's own rejection — which is what makes that execution end failed and
 * 重新执行 the only thing left.
 */

const TASK_CONTENT = `[[e2e:batch]]\n\n把本月的对账单整理成一份表格。`

/** The scratch trees this spec made, removed when it is done. */
const scratch: string[] = []

test.afterAll(() => {
  for (const dir of scratch) rmSync(dir, { recursive: true, force: true })
})

/** The read-only bar's own act: the top bar draws the same component, so the
 *  acts a state allows are named the same way in both places. */
function barAction(page: Page, action: string) {
  return page.locator(`.task-readonly-bar [data-testid="batch-run-${action}"]`)
}

interface StartedTask {
  projectId: string
  taskId: string
}

/** A batch project, a task made from its task list, and the page the create
 *  flow lands on: that task's first execution, running. */
async function startTask(page: Page): Promise<StartedTask> {
  // A batch project: a plain directory, trusted up front, because a batch
  // project may not exist without that decision.
  const root = mkdtempSync(join(tmpdir(), 'fb-e2e-153-batch-'))
  mkdirSync(root, { recursive: true })
  scratch.push(root)
  const created = await page.request.post('/api/v1/projects', {
    data: { name: `e2e-153-batch-${Date.now().toString(36)}`, root, project_type: 'batch', trust: true },
  })
  expect(created.ok(), await created.text()).toBeTruthy()
  const project = (await created.json()) as { id: string; project_type?: string; trust_level?: string }
  expect(project.project_type).toBe('batch')
  expect(project.trust_level).toBe('trusted')

  await page.goto(`/projects/${encodeURIComponent(project.id)}/tasks`, { waitUntil: 'networkidle' })
  // A batch project has no conversations to make: its space is its tasks.
  await expect(page.locator('[data-testid="tasks-empty"]')).toBeVisible()

  // 新建任务: the task's own words, then the run starts by itself.
  await page.click('[data-testid="tasks-new"]')
  await page.fill('[data-testid="task-new-content"]', TASK_CONTENT)
  await page.click('[data-testid="task-new-submit"]')
  await page.waitForURL(/\/projects\/[^/]+\/tasks\/[^/]+\/runs\/1(\?|$)/, { timeout: 60_000 })

  const segments = new URL(page.url()).pathname.split('/').filter(Boolean)
  const projectId = decodeURIComponent(segments[segments.indexOf('projects') + 1] ?? '')
  const taskId = decodeURIComponent(segments[segments.indexOf('tasks') + 1] ?? '')

  // The run's own turn is admitted just after the run opens, so the execution
  // is the reader's fact to wait for, not the page's to guess; read it afresh
  // once it is running.
  await expect.poll(async () => {
    const res = await page.request.get(`/api/v1/tasks/${encodeURIComponent(taskId)}?page=1&limit=10`)
    const body = (await res.json()) as { executions?: { items?: Array<{ status?: string }> } }
    return String(body.executions?.items?.[0]?.status ?? '')
  }, { timeout: 60_000 }).toBe('running')
  await page.reload({ waitUntil: 'networkidle' })
  await expect(page.locator('[data-testid="batch-run-status"]')).toHaveText('运行中', { timeout: 30_000 })
  return { projectId, taskId }
}

test('a task made from the list runs, is read without an input, and can be stopped', async ({ page }) => {
  test.info().setTimeout(180_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const { taskId } = await startTask(page)

  // The create flow lands on execution #1, read as a conversation: the task it
  // belongs to, which run of it, and no input anywhere.
  await expect(page.locator('[data-testid="batch-run-crumb-name"]')).toHaveText('第 1 次执行')
  await expect(page.locator('[data-testid="batch-readonly-bar"]')).toBeVisible()
  await expect(page.locator('textarea')).toHaveCount(0)
  await expect(barAction(page, 'stop')).toBeVisible()
  await expect(barAction(page, 'continue')).toHaveCount(0)
  await shot(page, 'p153-batch-run-running')

  // The task's own page lists that execution, carries no input either, and
  // counts it as the task's only one. The run page's crumb names its task, and
  // that link is the way up to it.
  await page.click('[data-testid="batch-run-crumb-task"]')
  await page.waitForURL(new RegExp(`/tasks/${taskId}$`), { timeout: 30_000 })
  await expect(page.locator('[data-testid="task-run-open-1"]')).toBeVisible()
  await expect(page.locator('[data-testid="task-facts"]')).toContainText('执行次数')
  await expect(page.locator('[data-testid="task-facts"] b').first()).toHaveText('1')
  await expect(page.locator('textarea')).toHaveCount(0)
  await shot(page, 'p153-batch-task-history')

  // 停止: the run in flight is stopped from the execution's own page.
  await page.click('[data-testid="task-run-open-1"]')
  await expect(page.locator('[data-testid="batch-run-status"]')).toHaveText('运行中', { timeout: 30_000 })
  await barAction(page, 'stop').click()
  await expect(page.locator('[data-testid="batch-run-status"]')).toHaveText('已停止', { timeout: 60_000 })
  // A stopped execution may be continued, and may be run again.
  await expect(barAction(page, 'continue')).toBeVisible()
  await expect(barAction(page, 'continue')).toBeEnabled()
  await expect(barAction(page, 'stop')).toHaveCount(0)
  await shot(page, 'p153-batch-run-stopped')
})

test('继续 resumes the stopped execution, and its failure offers 重新执行 as execution #2', async ({ page }) => {
  test.info().setTimeout(240_000)
  await page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))
  await signIn(page)

  const { taskId } = await startTask(page)

  // Stop the first execution, then continue it: the engine resumes it on its
  // own continue prompt, and the provider refuses that turn — so it fails.
  await barAction(page, 'stop').click()
  await expect(page.locator('[data-testid="batch-run-status"]')).toHaveText('已停止', { timeout: 60_000 })
  await barAction(page, 'continue').click()
  await expect(page.locator('[data-testid="batch-run-status"]')).toHaveText('失败', { timeout: 20_000 })
  await expect(barAction(page, 'continue')).toHaveCount(0)
  await expect(barAction(page, 'rerun')).toBeVisible()
  await shot(page, 'p153-batch-run-failed')

  // 重新执行: one more execution of the same task — execution #2, not a task.
  await barAction(page, 'rerun').click()
  await page.waitForURL(/\/projects\/[^/]+\/tasks\/[^/]+\/runs\/2(\?|$)/, { timeout: 60_000 })
  await expect(page.locator('[data-testid="batch-run-crumb-name"]')).toHaveText('第 2 次执行')
  await expect(page.locator('textarea')).toHaveCount(0)

  await page.click('[data-testid="batch-run-crumb-task"]')
  await page.waitForURL(new RegExp(`/tasks/${taskId}$`), { timeout: 30_000 })
  await expect(page.locator('[data-testid="task-run-open-1"]')).toBeVisible()
  await expect(page.locator('[data-testid="task-run-open-2"]')).toBeVisible()
  await expect(page.locator('[data-testid="task-history-empty"]')).toHaveCount(0)
  await shot(page, 'p153-batch-two-runs')
})
