import { expect, test, type Page } from '@playwright/test'

import { expectAssistantReply, signIn } from './support'

/**
 * Every fire of a scheduled job is a conversation of its own: the record it
 * leaves opens that conversation, the prompt arrives labelled as the task's,
 * the answer and the worked line follow the way a sent turn's do — and the
 * conversation stays out of the drawer, which lists only the person's own. A
 * fire whose answer could not be delivered says so in the viewer's language
 * and quotes the channel's own words below it.
 */

/** The fake provider answers in seconds; a real model needs its own budget. */
const settleTimeout = process.env.E2E_REAL_LLM === '1' ? 150_000 : 30_000

/** Create a once-in-30m job through the builder, the way a person would. */
async function createOnceJob(page: Page, name: string, prompt: string) {
  await page.goto('/cron', { waitUntil: 'networkidle' })
  await page.click('[data-testid="cron-new"]')
  await expect(page.locator('[data-testid="cron-editor"]')).toBeVisible()
  await page.fill('[data-testid="cron-name"]', name)
  await page.fill('[data-testid="cron-prompt"]', prompt)
  await page.selectOption('[data-testid="schedule-mode"]', 'once-in')
  await page.fill('[data-testid="schedule-in-count"]', '30')
  await page.selectOption('[data-testid="schedule-in-unit"]', 'm')
  await page.click('[data-testid="cron-save"]')
  await expect(page.locator(`[data-cron-job="${name}"]`)).toBeVisible({ timeout: 15_000 })
}

/**
 * Wait for the job's latest run to reach a status, reading the store the
 * panel is filled from — the list is fetched only when the history opens, so
 * the wait lives here and the panel is redrawn afterwards. Returns the record
 * it found.
 *
 * A real model answering a vague prompt may decide to do something gated —
 * write a file, search the web — and the fire then parks on that approval
 * with its record still running, which is exactly the designed behavior. The
 * person the task belongs to answers it in the conversation; the fake
 * provider never raises one, so this is real-LLM mode only.
 */
async function waitForRunRecord(
  page: Page,
  jobId: string,
  status: RegExp,
): Promise<{ id: number; session_id?: string; status?: string }> {
  let found: { id: number; session_id?: string; status?: string } | undefined
  const answered = new Set<string>()
  await expect
    .poll(
      async () => {
        const res = await page.request.get(`/api/cron/${jobId}/runs`)
        const data = (await res.json()) as { records?: Array<{ id: number; session_id?: string; status?: string }> }
        found = data.records?.find((row) => status.test(row.status ?? ''))
        if (found || process.env.E2E_REAL_LLM !== '1') return Boolean(found)
        const open = data.records?.find((row) => row.status === 'running' && row.session_id)
        if (!open?.session_id) return Boolean(found)
        const actions = (await (await page.request.get('/api/actions')).json()) as Array<{
          id: string
          session_id: string
          status: string
        }>
        for (const action of actions) {
          if (action.session_id === open.session_id && action.status === 'pending' && !answered.has(action.id)) {
            answered.add(action.id)
            await page.request.post(`/api/actions/${action.id}/approve`, { data: {} })
          }
        }
        return Boolean(found)
      },
      { timeout: settleTimeout, message: `run of job ${jobId} to become ${status}` },
    )
    .toBeTruthy()
  return found!
}

test('a fire is a conversation its record opens, and not one the drawer lists', async ({ page }) => {
  test.info().setTimeout(240_000)
  await signIn(page)
  await createOnceJob(page, 'fire brief', 'cron e2e fire prompt')

  const jobs = (await (await page.request.get('/api/cron')).json()) as { records: Array<{ id: string; name?: string }> }
  const job = jobs.records.find((row) => row.name === 'fire brief')
  expect(job, 'the created job is in the agent list').toBeTruthy()

  await page.locator('[data-cron-job="fire brief"]').getByRole('button', { name: /立即执行|Run now/ }).click()
  const record = await waitForRunRecord(page, job!.id, /^ok$/)
  expect(record.session_id, 'a fire that finished has its conversation').toBeTruthy()

  // The history panel "Run now" opened was filled before the run settled;
  // toggling it re-fetches, so the row shows what landed. The button is the
  // anchor: the plan's 30s wait is on it, then its own row carries the status.
  const jobRow = page.locator('[data-cron-job="fire brief"]')
  const history = jobRow.getByRole('button', { name: /执行记录|History/ })
  await history.click()
  await history.click()
  const openBtn = jobRow.getByTestId('cron-run-open')
  await expect(openBtn).toBeVisible({ timeout: 30_000 })
  const runRow = openBtn.locator('xpath=ancestor::li[1]')
  await expect(runRow).toContainText(/成功|ok/)
  await openBtn.click()
  await page.waitForURL((url) => url.pathname === '/' && url.searchParams.get('session') === record.session_id)

  // The prompt arrives marked as the task's, and the turn closes the way a
  // sent one does: answer, then the worked line.
  const origin = page
    .locator('[role=log] .is-user', { hasText: 'cron e2e fire prompt' })
    .locator('[data-testid="message-origin"]')
  await expect(origin).toBeVisible({ timeout: 30_000 })
  await expect(origin).toHaveText(/由定时任务发送|Sent by a scheduled task/)
  await expectAssistantReply(page)
  await expect(page.locator('[data-testid="run-worked-line"]')).toHaveCount(1, { timeout: 30_000 })

  // The drawer lists the person's own conversations; the fire's is reached
  // from its record only.
  const drawerListed = page.waitForResponse(
    (res) => res.url().includes('/api/chat/sessions') && res.request().method() === 'GET',
  )
  await page.locator('.forebrain-rail-group > button.forebrain-rail-link').first().click()
  await expect(page.locator('[data-testid="chat-drawer"]')).toBeVisible()
  await drawerListed
  await expect(page.locator('.chat-drawer-row', { hasText: 'fire brief' })).toHaveCount(0)
})

test('a delivery that no channel takes is said in the viewer\'s language, quoting the channel', async ({ page }) => {
  test.info().setTimeout(240_000)
  await signIn(page)

  // The builder's delivery dropdown lists only channels that exist, so a job
  // addressed to one that does not is written straight through the API.
  const created = await page.request.post('/api/cron', {
    data: { name: 'fire undelivered', schedule: 'in 30m', prompt: 'cron e2e deliver prompt', deliver: 'no-such-channel' },
  })
  expect(created.ok()).toBeTruthy()
  const job = (await created.json()) as { id: string }
  const fired = await page.request.post(`/api/cron/${job.id}/run`, { data: {} })
  expect(fired.ok()).toBeTruthy()

  const record = await waitForRunRecord(page, job.id, /^delivery_failed$/)
  expect(record.session_id).toBeTruthy()

  await page.goto('/cron', { waitUntil: 'networkidle' })
  await expect(page.locator('[data-cron-job="fire undelivered"]')).toBeVisible({ timeout: 15_000 })
  await page.locator('[data-cron-job="fire undelivered"]').getByRole('button', { name: /执行记录|History/ }).click()
  // The sentence is the viewer's; the channel's own words follow on the line
  // below it, as stored.
  const error = page.locator('[data-cron-job="fire undelivered"] [data-testid="cron-run-error"]')
  await expect(error).toBeVisible({ timeout: 10_000 })
  await expect(error).toContainText(/答复没能投递到渠道。|The answer could not be delivered to the channel\./)
  await expect(error).toContainText('channel: no bound handler for channel id')
})
