import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { nextTick } from 'vue'

import tip from '@/directives/tip'
import { forebrainApi, type ProjectTaskRow } from '@/lib/api'
import { startBatchRun } from '@/lib/batchRun'
import { setLocale } from '@/locales'
import ProjectTasks from './ProjectTasks.vue'

// The socket is the page's only gateway-first act: the module it lives in is
// replaced so a test never opens one.
vi.mock('@/lib/batchRun', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/batchRun')>()
  return { ...actual, startBatchRun: vi.fn(async () => 'run-1'), stopBatchRun: vi.fn(async () => true) }
})

/**
 * A batch project's task table (plan 153 A10): one row per task with the state
 * of its newest execution, and only the acts that state allows — open it,
 * continue a stopped run, run a failed or stopped one again. Continuing sends
 * nothing at all: the engine supplies the input the execution was fixed with.
 */
const at = 1760000000

function taskRow(overrides: Partial<ProjectTaskRow> = {}): ProjectTaskRow {
  return {
    id: 'tsk-1',
    projectId: 'p1',
    title: '审核三季度底稿',
    externalRef: 'IB-0917',
    origin: 'api',
    originRef: 'ibs-audit',
    inputSet: true,
    createdAt: at,
    updatedAt: at,
    latest: { seq: 2, sessionId: 'web-2', status: 'stopped', startedAt: at, finishedAt: at + 60 },
    executions: 2,
    artifacts: 3,
    ...overrides,
  }
}

const startMock = vi.mocked(startBatchRun)

async function mountTasks(): Promise<VueWrapper> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/projects/:id/tasks', name: 'project-tasks', component: { template: '<div />' } },
      { path: '/projects/:id/tasks/:taskId', name: 'project-task', component: { template: '<div />' } },
      { path: '/projects/:id/tasks/:taskId/runs/:seq', name: 'project-task-run', component: { template: '<div />' } },
      { path: '/', component: { template: '<div />' } },
    ],
  })
  await router.push('/projects/p1/tasks')
  await router.isReady()
  const wrapper = mount(ProjectTasks, {
    props: { project: null, projectId: 'p1' },
    global: { plugins: [router], directives: { tip } },
  })
  await flushPromises()
  return wrapper
}

describe('ProjectTasks', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'listProjectTasks').mockResolvedValue({ rows: [taskRow()], total: 1, page: 1, pageSize: 10 })
    vi.spyOn(forebrainApi, 'createTaskSession').mockResolvedValue({
      id: 'web-3',
      title: '审核三季度底稿 #3',
      project: {} as never,
      task: { id: 'tsk-1', title: '审核三季度底稿', externalRef: 'IB-0917', origin: 'web' },
      execution: { seq: 3, status: 'pending', workspace: '/w/tasks/tsk-1/3', workspaceRel: 'tasks/tsk-1/3' },
    })
    startMock.mockClear()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    document.body.innerHTML = ''
  })

  it('draws a task with its id, source, newest status and counts', async () => {
    const wrapper = await mountTasks()

    const row = wrapper.get('[data-task-row="tsk-1"]')
    expect(row.text()).toContain('审核三季度底稿')
    expect(row.text()).toContain('tsk-1')
    expect(row.text()).toContain('业务系统 · ibs-audit')
    expect(wrapper.get('[data-testid="task-status-tsk-1"]').text()).toBe('已停止')
    // Runs and files are the server's counts, and the updated column is the
    // row's own moment.
    expect(row.text()).toContain('2025-10-09')
    wrapper.unmount()
  })

  it('offers continue only for a stopped run, and run again for failed or stopped', async () => {
    vi.spyOn(forebrainApi, 'listProjectTasks').mockResolvedValue({
      rows: [
      taskRow({ id: 'tsk-running', latest: { seq: 1, sessionId: 's-run', status: 'running' } }),
      taskRow({ id: 'tsk-failed', latest: { seq: 2, sessionId: 's-fail', status: 'failed' } }),
      taskRow({ id: 'tsk-stopped', latest: { seq: 3, sessionId: 's-stop', status: 'stopped' } }),
      taskRow({ id: 'tsk-done', latest: { seq: 4, sessionId: 's-done', status: 'completed' } }),
      ],
      total: 4,
      page: 1,
      pageSize: 10,
    })
    const wrapper = await mountTasks()

    // A run in flight may only be looked at: continuing it would start a
    // second turn of the same execution.
    expect(wrapper.find('[data-testid="task-continue-tsk-running"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="task-rerun-tsk-running"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="task-continue-tsk-failed"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="task-rerun-tsk-failed"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="task-continue-tsk-stopped"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="task-rerun-tsk-stopped"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="task-continue-tsk-done"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="task-rerun-tsk-done"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('continues a stopped execution by sending nothing, then opens that run', async () => {
    const wrapper = await mountTasks()
    const router = wrapper.vm.$router as unknown as { currentRoute: { value: { name?: string; params: Record<string, string> } } }

    await wrapper.get('[data-testid="task-continue-tsk-1"]').trigger('click')
    await flushPromises()
    await nextTick()

    expect(startMock).toHaveBeenCalledWith('web-2')
    expect(router.currentRoute.value.name).toBe('project-task-run')
    expect(router.currentRoute.value.params).toMatchObject({ id: 'p1', taskId: 'tsk-1', seq: '2' })
    wrapper.unmount()
  })

  it('runs a failed task again as one more execution of the same task', async () => {
    vi.spyOn(forebrainApi, 'listProjectTasks').mockResolvedValue({
      rows: [taskRow({ id: 'tsk-1', latest: { seq: 2, sessionId: 'web-2', status: 'failed' } })],
      total: 1,
      page: 1,
      pageSize: 10,
    })
    const wrapper = await mountTasks()
    const create = vi.spyOn(forebrainApi, 'createTaskSession')
    const router = wrapper.vm.$router as unknown as { currentRoute: { value: { name?: string; params: Record<string, string> } } }

    await wrapper.get('[data-testid="task-rerun-tsk-1"]').trigger('click')
    await flushPromises()
    await nextTick()

    // The task is named, never re-created: a re-run is another execution.
    expect(create).toHaveBeenCalledWith('p1', { taskId: 'tsk-1' })
    expect(startMock).toHaveBeenCalledWith('web-3')
    expect(router.currentRoute.value.params).toMatchObject({ taskId: 'tsk-1', seq: '3' })
    wrapper.unmount()
  })

  it('opens the new-task dialog when the page was reached with ?new=1', async () => {
    const wrapper = await mountTasks()
    // The new-chat page's 新建任务 lands on the list with the flag.
    const vm = wrapper.vm as unknown as { $router: { push: (to: unknown) => Promise<void> } }
    await vm.$router.push('/projects/p1/tasks?new=1')
    await nextTick()
    await flushPromises()

    // The dialog is opened by the flag on arrival; the list itself stays.
    expect(wrapper.find('[data-testid="tasks-search"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('says a refused start in the page\'s own language', async () => {
    const { BatchStartRefusal } = await import('@/lib/batchRun')
    startMock.mockRejectedValueOnce(new BatchStartRefusal('batch_execution_running', 'another turn is running'))
    const wrapper = await mountTasks()

    await wrapper.get('[data-testid="task-continue-tsk-1"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="tasks-error"]').text()).toBe('这次执行还在运行，等它结束后再操作。')
    wrapper.unmount()
  })

  it('says there is nothing to show when the filter matches no task', async () => {
    vi.spyOn(forebrainApi, 'listProjectTasks').mockResolvedValue({ rows: [], total: 0, page: 1, pageSize: 10 })
    const wrapper = await mountTasks()

    const empty = wrapper.get('[data-testid="tasks-empty"]')
    expect(empty.text()).toContain('没有匹配的任务')
    expect(empty.text()).toContain('换一个标题、任务 ID 或状态。')
    wrapper.unmount()
  })
})
