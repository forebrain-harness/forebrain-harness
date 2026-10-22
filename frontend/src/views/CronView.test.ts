import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

import { hideTip, useTooltipState } from '@/composables/useTooltip'
import tip from '@/directives/tip'
import { forebrainApi, type CronJobRow, type CronRunRow } from '@/lib/api'
import { formatMonthDayTime } from '@/lib/time'
import { setLocale } from '@/locales'
import { openSelect, selectedText } from '@/test-utils/formControls'
import { InlineTeleport } from '@/test-utils/portal'

import CronView from './CronView.vue'

/**
 * A project's scheduled tasks (plan 075): a fixed table whose switch is the
 * pause and the resume, whose last-run column is what became of the last fire,
 * and whose row actions run, edit (the builder the tab always opened), read the
 * history (a modal) and delete. No card list, no inline history.
 */
function job(overrides: Partial<CronJobRow> = {}): CronJobRow {
  return {
    id: 'j1',
    name: 'Nightly',
    schedule: 'daily at 11pm',
    prompt: 'run it',
    deliver: 'feishu',
    enabled: true,
    nextRunAt: 1_700_000_000,
    runCount: 6,
    lastStatus: 'ok',
    ...overrides,
  }
}

function runRow(): CronRunRow {
  return { id: 11, jobId: 'j1', status: 'ok', startedAt: 1_700_000_000, trigger: 'schedule', output: 'done' }
}

async function mountView(): Promise<VueWrapper> {
  vi.mocked(forebrainApi.cronJobsPage).mockResolvedValue({
    rows: [
      job(),
      job({
        id: 'j2',
        name: 'Cleanup',
        schedule: 'every monday 9am',
        deliver: '',
        enabled: false,
        nextRunAt: undefined,
        runCount: 0,
        lastStatus: 'failed',
        lastError: 'boom',
        lastErrorCode: '',
      }),
    ],
    total: 2,
    page: 1,
    pageSize: 10,
  })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
    ],
  })
  const wrapper = mount(CronView, {
    props: { projectId: 'p1' },
    global: { plugins: [router], stubs: { teleport: InlineTeleport }, directives: { tip } },
  })
  await flushPromises()
  return wrapper
}

describe('CronView', () => {
  beforeAll(() => {
    setLocale('zh')
    // The delivery select measures its viewport with ResizeObserver, which jsdom
    // does not have; scrolling an item into view is not implemented either.
    ;(globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    Element.prototype.scrollIntoView = () => undefined
  })

  beforeEach(() => {
    hideTip()
    vi.spyOn(forebrainApi, 'cronJobsPage')
    vi.spyOn(forebrainApi, 'cronRunsPage').mockResolvedValue({ rows: [runRow()], total: 1, page: 1, pageSize: 10 })
    vi.spyOn(forebrainApi, 'projectChannelsRaw').mockResolvedValue({
      kinds: ['feishu', 'telegram'],
      channels: { feishu: { app_id: 'cli_a1b2', enabled: true }, telegram: { bot_token: '[REDACTED]', enabled: false } },
    })
    vi.spyOn(forebrainApi, 'cronPreview').mockResolvedValue({ raw: 'daily at 11pm', valid: true, next: [] })
    vi.spyOn(forebrainApi, 'cronUpdate').mockResolvedValue(job())
    vi.spyOn(forebrainApi, 'cronRunNow').mockResolvedValue({ started: 'j1' })
    vi.spyOn(forebrainApi, 'cronDelete').mockResolvedValue({ deleted: 'j1' })
  })

  afterEach(() => {
    hideTip()
    vi.restoreAllMocks()
  })

  it('draws the tasks as a table with the toolbar, the pager and the note', async () => {
    const wrapper = await mountView()

    const search = wrapper.find('.forebrain-list-toolbar input[type="search"]')
    expect(search.attributes('placeholder')).toBe('搜索任务')
    expect(search.classes()).toContain('h-[34px]')
    expect(wrapper.find('.forebrain-table-wrap table.forebrain-table--fixed').exists()).toBe(true)

    // A row is a table row, and there is no card list left behind.
    const rows = wrapper.findAll('[data-cron-job]')
    expect(rows.map((row) => row.element.tagName)).toEqual(['TR', 'TR'])
    expect(wrapper.findAll('ul')).toHaveLength(0)

    expect(wrapper.find('[data-testid="table-pager"]').text()).toContain('共 2 条')
    expect(wrapper.find('p.forebrain-list-note').text()).toBe('定时任务属于本项目，在项目根目录执行。')
    expect(wrapper.find('[data-testid="cron-new"]').exists()).toBe(true)
  })

  it('names the columns the table carries', async () => {
    const wrapper = await mountView()

    expect(wrapper.findAll('thead th').map((th) => th.text())).toEqual([
      '名称',
      '计划',
      '投递至',
      '下次执行',
      '最近一次',
      '已执行',
      '启用',
      '操作',
    ])
  })

  it('draws each task’s schedule, delivery, next run, last run and count', async () => {
    const wrapper = await mountView()

    const nightly = wrapper.find('[data-cron-job="Nightly"]')
    expect(nightly.text()).toContain('daily at 11pm')
    expect(nightly.text()).toContain('feishu')
    expect(nightly.text()).toContain(formatMonthDayTime(1_700_000_000))
    expect(nightly.text()).toContain('成功')
    expect(nightly.text()).toContain('6 次')

    // A paused task says so where its next run would be, and a task that
    // records instead of delivering says that in grey.
    const cleanup = wrapper.find('[data-cron-job="Cleanup"]')
    expect(cleanup.text()).toContain('只记录')
    expect(cleanup.text()).toContain('已暂停')
    expect(cleanup.text()).toContain('失败')
    expect(cleanup.text()).toContain('0 次')

    // The failure's own sentence is the tip on its badge.
    const failed = cleanup.findAll('.forebrain-badge').find((badge) => badge.text() === '失败')
    await failed!.trigger('mouseenter')
    expect(useTooltipState().text).toBe('boom')
  })

  it('pauses from the switch in the 启用 column', async () => {
    const wrapper = await mountView()
    const page = vi.mocked(forebrainApi.cronJobsPage)

    const toggle = wrapper.find('[data-testid="cron-toggle-j1"]')
    expect(toggle.attributes('role')).toBe('switch')
    expect(toggle.attributes('aria-checked')).toBe('true')
    expect(toggle.attributes('aria-label')).toBe('启用 Nightly')

    await toggle.trigger('click')
    await flushPromises()

    expect(forebrainApi.cronUpdate).toHaveBeenCalledWith('p1', 'j1', { enabled: false })
    expect(page).toHaveBeenCalledTimes(2)
  })

  it('opens the stored schedule in the builder from 编辑', async () => {
    const wrapper = await mountView()

    expect(wrapper.find('[data-testid="cron-editor"]').exists()).toBe(false)
    await wrapper.find('[data-testid="cron-edit-j1"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="cron-editor"]').exists()).toBe(true)
    expect((wrapper.find('[data-testid="cron-name"]').element as HTMLInputElement).value).toBe('Nightly')
    // The stored expression is shown as it is, never rebuilt from a mode's
    // defaults; the delivery list is the project's enabled channels, by kind.
    expect(selectedText(wrapper.find('[data-testid="schedule-mode"]').element as HTMLElement)).toBe('Cron 表达式')
    expect((wrapper.find('[data-testid="schedule-cron"]').element as HTMLInputElement).value).toBe('daily at 11pm')
    // 只记录，不投递 is the empty string on the wire, which a select item may not
    // carry; the sentinel stands in for it, and it is still the first choice.
    const deliver = wrapper.find('[data-testid="cron-deliver"]')
    await openSelect(deliver.element as HTMLElement)
    const choices = wrapper.find('[role="listbox"][data-state="open"]')
    expect(choices.findAll('[role="option"]').map((option) => option.text())).toEqual(['只记录，不投递', 'feishu'])

    // The builder's own preview is debounced; let it run while its answer is mocked.
    await new Promise((resolve) => setTimeout(resolve, 450))
    await flushPromises()
  })

  it('reads the history in a modal over the table', async () => {
    const wrapper = await mountView()

    await wrapper.find('[data-testid="cron-history-j1"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.cronRunsPage).toHaveBeenCalledWith('p1', 'j1', expect.objectContaining({ page: 1 }))
    expect(wrapper.find('.forebrain-modal-title').text()).toBe('执行记录 · Nightly')
    expect(wrapper.findAll('[data-cron-run]')).toHaveLength(1)
    // The table underneath keeps its rows: the history is over it, not in it.
    expect(wrapper.findAll('[data-cron-job]')).toHaveLength(2)
    expect(wrapper.find('.forebrain-table-wrap').text()).not.toContain('done')
  })

  it('runs a task now and reads the table again', async () => {
    const wrapper = await mountView()
    const page = vi.mocked(forebrainApi.cronJobsPage)

    await wrapper.find('[data-testid="cron-run-j1"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.cronRunNow).toHaveBeenCalledWith('p1', 'j1')
    expect(page).toHaveBeenCalledTimes(2)
  })

  it('deletes a task from its row', async () => {
    const wrapper = await mountView()

    await wrapper.find('[data-testid="cron-delete-j1"]').trigger('click')
    await flushPromises()

    expect(forebrainApi.cronDelete).toHaveBeenCalledWith('p1', 'j1')
    expect(forebrainApi.cronJobsPage).toHaveBeenCalledTimes(2)
  })
})
