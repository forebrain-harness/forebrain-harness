import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

import tip from '@/directives/tip'
import { forebrainApi, type CronJobRow, type CronRunRow } from '@/lib/api'
import { setLocale } from '@/locales'

import CronRunsDialog from './CronRunsDialog.vue'

/**
 * One task's fire history (plan 075): a modal over the task table, its own
 * paged table inside — what became of each fire, when it started, what fired it
 * and where it went — with the output folded under the row it belongs to and
 * the door to the fire's own conversation.
 */
function job(): CronJobRow {
  return { id: 'j1', name: 'Nightly', schedule: 'daily at 11pm', prompt: 'run it', enabled: true }
}

function run(overrides: Partial<CronRunRow> = {}): CronRunRow {
  return { id: 1, jobId: 'j1', status: 'ok', startedAt: 1_700_000_000, trigger: 'schedule', ...overrides }
}

async function mountDialog(rows: CronRunRow[], total = rows.length): Promise<VueWrapper> {
  vi.mocked(forebrainApi.cronRunsPage).mockResolvedValue({ rows, total, page: 1, pageSize: 10 })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
    ],
  })
  const wrapper = mount(CronRunsDialog, {
    props: { projectId: 'p1', job: job() },
    global: { plugins: [router], stubs: { teleport: true }, directives: { tip } },
  })
  await flushPromises()
  return wrapper
}

describe('CronRunsDialog', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'cronRunsPage')
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('titles itself with the task and draws its fires as a table', async () => {
    const wrapper = await mountDialog([
      run({ id: 1, status: 'ok', deliveredTo: 'feishu' }),
      run({ id: 2, status: 'failed', startedAt: 1_700_000_600 }),
    ])

    expect(wrapper.find('.forebrain-modal-title').text()).toBe('执行记录 · Nightly')

    const headers = wrapper.findAll('thead th').map((th) => th.text())
    expect(headers).toEqual(['状态', '开始时间', '触发', '投递至', '操作'])

    const rows = wrapper.findAll('[data-cron-run]')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('成功')
    expect(rows[0].text()).toContain('feishu')
    expect(rows[1].text()).toContain('失败')
    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')
  })

  it('says so when the task never fired', async () => {
    const wrapper = await mountDialog([])

    expect(wrapper.findAll('[data-cron-run]')).toHaveLength(0)
    expect(wrapper.text()).toContain('暂无执行记录')
  })

  it('folds one fire’s output under its own row and unfolds it again', async () => {
    const wrapper = await mountDialog([
      run({ id: 1, output: 'first output' }),
      run({ id: 2, status: 'failed', output: 'second output', error: 'boom', errorCode: '' }),
    ])

    const toggle = wrapper.find('[data-testid="cron-run-output-2"]')
    expect(toggle.attributes('aria-label')).toContain('查看输出')
    expect(toggle.attributes('aria-label')).toContain('Nightly')

    await toggle.trigger('click')
    const open = wrapper.find('[data-cron-run-output="2"]')
    expect(open.exists()).toBe(true)
    expect(open.text()).toContain('second output')
    expect(open.find('[data-testid="cron-run-error"]').text()).toBe('boom')
    // Only the row that was asked opens: the other fire stays folded.
    expect(wrapper.find('[data-cron-run-output="1"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="cron-run-output-2"]').attributes('aria-label')).toContain('收起')

    await wrapper.find('[data-testid="cron-run-output-2"]').trigger('click')
    expect(wrapper.find('[data-cron-run-output="2"]').exists()).toBe(false)
  })

  it('opens the fire’s own conversation, and only when the record has one', async () => {
    const wrapper = await mountDialog([
      run({ id: 1, sessionId: 's1', hasConversation: true }),
      run({ id: 2, status: 'failed' }),
    ])

    const open = wrapper.findAll('[data-testid="cron-run-open"]')
    expect(open).toHaveLength(1)

    await open[0].trigger('click')
    await flushPromises()
    expect(wrapper.vm.$router.currentRoute.value.query.session).toBe('s1')
  })

  it('pages the history on the server', async () => {
    const wrapper = await mountDialog([run({ id: 1 })], 25)
    const page = vi.mocked(forebrainApi.cronRunsPage)

    expect(page.mock.calls[0][2]).toMatchObject({ page: 1, pageSize: 10 })

    await wrapper.find('[data-testid="pager-page-2"]').trigger('click')
    await flushPromises()

    expect(page).toHaveBeenCalledTimes(2)
    expect(page.mock.calls[1][2]).toMatchObject({ page: 2, pageSize: 10 })
  })
})
