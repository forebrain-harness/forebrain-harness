import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

import tip from '@/directives/tip'
import { forebrainApi, type TaskExecutionRow, type TaskRecord } from '@/lib/api'
import { stopBatchRun } from '@/lib/batchRun'
import { setLocale } from '@/locales'
import { useTopbarContent } from '@/composables/useTopbar'
import TaskDetailView from './TaskDetailView.vue'

vi.mock('@/lib/batchRun', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/batchRun')>()
  return { ...actual, startBatchRun: vi.fn(async () => 'run-1'), stopBatchRun: vi.fn(async () => true) }
})

/**
 * One task and its execution history (plan 153 A11): the task's own row in the
 * top bar, the four facts, and the history as an index — each row opens the run
 * as the conversation it was, and only the acts the newest state allows are
 * offered.
 */
const at = 1760000000

const task: TaskRecord = {
  id: 'tsk-1',
  projectId: 'p1',
  title: '审核三季度底稿',
  externalRef: 'IB-0917',
  origin: 'api',
  originRef: 'ibs-audit',
  inputSet: true,
  createdAt: at,
  updatedAt: at,
}

function run(overrides: Partial<TaskExecutionRow> = {}): TaskExecutionRow {
  return {
    seq: 3,
    sessionId: 'web-3',
    status: 'running',
    workspaceRel: 'tasks/tsk-1/3',
    startedAt: at,
    finishedAt: 0,
    errorCode: '',
    errorText: '',
    artifacts: 2,
    ...overrides,
  }
}

const stopMock = vi.mocked(stopBatchRun)

function rowsFixture(state: string): TaskExecutionRow[] {
  return [
    run({ seq: 3, status: state, finishedAt: state === 'running' ? 0 : at + 221 }),
    run({ seq: 2, status: 'failed', sessionId: 'web-2', errorText: '缺 OCR 依赖', finishedAt: at + 60 }),
    run({ seq: 1, status: 'completed', sessionId: 'web-1', finishedAt: at + 120, artifacts: 3 }),
  ]
}

async function mountTask(): Promise<{ wrapper: VueWrapper; router: ReturnType<typeof createRouter> }> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/projects/:id/tasks', name: 'project-tasks', component: { template: '<div />' } },
      { path: '/projects/:id/tasks/:taskId', name: 'project-task', component: { template: '<div />' } },
      { path: '/projects/:id/tasks/:taskId/runs/:seq', name: 'project-task-run', component: { template: '<div />' } },
      { path: '/', component: { template: '<div />' } },
    ],
  })
  await router.push('/projects/p1/tasks/tsk-1')
  await router.isReady()
  const wrapper = mount(TaskDetailView, {
    global: { plugins: [router], directives: { tip } },
  })
  await flushPromises()
  return { wrapper, router }
}

describe('TaskDetailView', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'getTask').mockImplementation(async () => ({
      task,
      rows: rowsFixture('running'),
      total: 3,
      page: 1,
      pageSize: 10,
    }))
    stopMock.mockClear()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    useTopbarContent().value = null
  })

  it('names the task in the top bar and draws the four facts', async () => {
    const { wrapper, router } = await mountTask()

    const bar = useTopbarContent().value
    expect(bar).not.toBeNull()
    const rendered = mount({ props: {}, render: () => bar!.leading!() }, { global: { plugins: [router], directives: { tip } } })
    expect(rendered.text()).toContain('任务')
    expect(rendered.text()).toContain('审核三季度底稿')
    expect(rendered.text()).toContain('tsk-1')
    expect(rendered.text()).toContain('运行中')

    const facts = wrapper.get('[data-testid="task-facts"]').text()
    expect(facts).toContain('执行次数')
    expect(facts).toContain('已完成')
    expect(facts).toContain('最新状态')
    expect(facts).toContain('tasks/tsk-1/3')
    expect(wrapper.get('[data-testid="task-meta"]').text()).toContain('业务系统 · ibs-audit')
    expect(wrapper.get('[data-testid="task-meta"]').text()).toContain('IB-0917')
    wrapper.unmount()
  })

  it('lists every execution as its own row, with what it ended on', async () => {
    const { wrapper } = await mountTask()

    expect(wrapper.get('[data-task-run="3"]').text()).toContain('第 3 次')
    // A failed run says why, and the completed one says it finished.
    expect(wrapper.get('[data-task-run="2"]').text()).toContain('缺 OCR 依赖')
    expect(wrapper.get('[data-task-run="1"]').text()).toContain('全部处理完成')
    // The history is not a conversation: no thread lives on this page.
    expect(wrapper.find('[data-testid="batch-readonly-bar"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('opens one execution as its own page', async () => {
    const { wrapper, router } = await mountTask()

    await wrapper.get('[data-testid="task-run-view-2"]').trigger('click')
    await flushPromises()

    expect(router.currentRoute.value.name).toBe('project-task-run')
    expect(router.currentRoute.value.params).toMatchObject({ id: 'p1', taskId: 'tsk-1', seq: '2' })
    expect(router.currentRoute.value.query.session).toBe('web-2')
    wrapper.unmount()
  })

  it('stops a running execution through the run the roster names', async () => {
    const { wrapper } = await mountTask()

    await wrapper.get('[data-testid="task-stop"]').trigger('click')
    await flushPromises()

    expect(stopMock).toHaveBeenCalledWith('web-3')
    wrapper.unmount()
  })

  it('offers run-again only when the newest execution failed or stopped', async () => {
    vi.spyOn(forebrainApi, 'getTask').mockImplementation(async () => ({
      task, rows: rowsFixture('completed'), total: 3, page: 1, pageSize: 10,
    }))
    const done = await mountTask()
    expect(done.wrapper.find('[data-testid="task-stop"]').exists()).toBe(false)
    expect(done.wrapper.find('[data-testid="task-rerun"]').exists()).toBe(false)
    done.wrapper.unmount()

    vi.spyOn(forebrainApi, 'getTask').mockImplementation(async () => ({
      task, rows: rowsFixture('stopped'), total: 3, page: 1, pageSize: 10,
    }))
    const stopped = await mountTask()
    expect(stopped.wrapper.get('[data-testid="task-rerun"]').text()).toContain('重新执行')
    stopped.wrapper.unmount()
  })

  it('says the run is no longer active when there is nothing to stop', async () => {
    stopMock.mockResolvedValueOnce(false)
    const { wrapper } = await mountTask()

    await wrapper.get('[data-testid="task-stop"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="task-error"]').text()).toBe('这次执行已经不在运行了，可能已在另一端结束。')
    wrapper.unmount()
  })
})
