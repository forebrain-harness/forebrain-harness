import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'

import tip from '@/directives/tip'
import { forebrainApi, type TaskExecutionRow, type TaskRecord } from '@/lib/api'
import { startBatchRun, stopBatchRun } from '@/lib/batchRun'
import { noteSessionBound, recordBatchExecution } from '@/lib/batchExecution'
import { setLiveState } from '@/lib/liveConnection'
import { batchRunActions } from '@/lib/tasks'
import { setLocale } from '@/locales'
import TaskRunView from './TaskRunView.vue'

vi.mock('@/lib/batchRun', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/batchRun')>()
  return { ...actual, startBatchRun: vi.fn(async () => 'run-1'), stopBatchRun: vi.fn(async () => true) }
})

/**
 * One execution's page (plan 153 A11): it finds the run in the task's history,
 * names it in the address bar the way every conversation is named, and mounts
 * the read-only conversation with that run's facts. The conversation itself is
 * ChatView — stubbed here, because what this page owns is the finding, the
 * three acts and the state the engine reports.
 *
 * An execution's state is not read on a clock: the engine's own
 * `batch_execution` frame is what moves it, so these tests move it the same
 * way, by filing a frame. Each test runs on its own execution (its own run
 * number and therefore its own conversation) because the frames are kept per
 * conversation for the tab's lifetime.
 */
const at = 1760000000

const task: TaskRecord = {
  id: 'tsk-1',
  projectId: 'p1',
  title: '审核三季度底稿',
  externalRef: '',
  origin: 'web',
  originRef: '',
  inputSet: true,
  createdAt: at,
  updatedAt: at,
}

function run(seq: number, status = 'completed'): TaskExecutionRow {
  return {
    seq,
    sessionId: `web-${seq}`,
    status,
    workspaceRel: `tasks/tsk-1/${seq}`,
    startedAt: at,
    finishedAt: at + 60,
    errorCode: '',
    errorText: '',
    artifacts: 1,
  }
}

const startMock = vi.mocked(startBatchRun)
const stopMock = vi.mocked(stopBatchRun)

/** A page of the history holding one run, plus what the list's total says. */
function history(rows: TaskExecutionRow[], total: number) {
  return { task, rows, total, page: 1, pageSize: 10 }
}

/** One execution frame, the way the conversation's stream files it. */
function frame(status: string, seq: number, sequence = 1) {
  recordBatchExecution({
    sessionId: `web-${seq}`,
    sequence,
    payload: {
      taskId: 'tsk-1',
      sessionId: `web-${seq}`,
      seq,
      status,
      startedAt: at,
      finishedAt: status === 'running' ? 0 : at + 60,
    },
  })
}

async function mountRun(seq = 2): Promise<{ wrapper: VueWrapper; router: ReturnType<typeof createRouter> }> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/projects/:id/tasks', name: 'project-tasks', component: { template: '<div />' } },
      { path: '/projects/:id/tasks/:taskId', name: 'project-task', component: { template: '<div />' } },
      { path: '/projects/:id/tasks/:taskId/runs/:seq', name: 'project-task-run', component: { template: '<div />' } },
      { path: '/', component: { template: '<div />' } },
    ],
  })
  await router.push(`/projects/p1/tasks/tsk-1/runs/${seq}`)
  await router.isReady()
  const wrapper = mount(TaskRunView, {
    global: {
      plugins: [router],
      directives: { tip },
      stubs: {
        ChatView: {
          name: 'ChatView',
          props: ['taskRun'],
          emits: ['batch-stop', 'batch-continue', 'batch-rerun'],
          template: '<div data-testid="stub-chat" />',
        },
      },
    },
  })
  await flushPromises()
  return { wrapper, router }
}

/** A run number no other test uses: frames are kept per conversation. */
let lastSeq = 1
function nextSeq(): number {
  lastSeq += 1
  return lastSeq
}

/** Mounts one execution that stands in `status` when the page reads it. */
async function mountStatus(status: string): Promise<{ wrapper: VueWrapper; seq: number }> {
  const seq = nextSeq()
  vi.spyOn(forebrainApi, 'getTask').mockResolvedValue(history([run(seq, status)], seq))
  const { wrapper } = await mountRun(seq)
  return { wrapper, seq }
}

describe('TaskRunView', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'getTask').mockResolvedValue(history([run(2, 'stopped'), run(1)], 2))
    startMock.mockClear()
    stopMock.mockClear()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.unstubAllGlobals()
  })

  it('mounts the read-only conversation on the run the address asked for', async () => {
    const { wrapper, router } = await mountRun(2)

    const chat = wrapper.getComponent({ name: 'ChatView' })
    expect(chat.props('taskRun')).toMatchObject({
      projectId: 'p1',
      taskId: 'tsk-1',
      title: '审核三季度底稿',
      seq: 2,
      status: 'stopped',
    })
    // The conversation is named in the address bar, as every conversation is.
    expect(router.currentRoute.value.query.session).toBe('web-2')
    wrapper.unmount()
  })

  it('finds a run that is not on the history\'s first page', async () => {
    // 25 runs, newest first: run 3 is on the page after the first one.
    const page1 = Array.from({ length: 10 }, (_, i) => run(25 - i))
    const page3 = [run(5), run(4), run(3), run(2), run(1)]
    vi.spyOn(forebrainApi, 'getTask').mockImplementation(async (_id, query) => (
      (query.page ?? 1) === 1
        ? { task, rows: page1, total: 25, page: 1, pageSize: 10 }
        : { task, rows: page3, total: 25, page: 3, pageSize: 10 }
    ))

    const { wrapper } = await mountRun(3)

    expect(wrapper.getComponent({ name: 'ChatView' }).props('taskRun')).toMatchObject({ seq: 3 })
    wrapper.unmount()
  })

  it('says the run does not exist rather than drawing an empty conversation', async () => {
    vi.spyOn(forebrainApi, 'getTask').mockResolvedValue(history([run(2)], 2))
    const { wrapper } = await mountRun(9)

    expect(wrapper.find('[data-testid="stub-chat"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="task-run-not-found"]').text()).toContain('找不到这次执行')
    wrapper.unmount()
  })

  it('drives the three acts the bar asks for', async () => {
    const { wrapper } = await mountRun(2)
    const chat = () => wrapper.getComponent({ name: 'ChatView' })

    // Continuing resumes this session and sends nothing: the engine supplies
    // the continue prompt.
    chat().vm.$emit('batch-continue')
    await flushPromises()
    expect(startMock).toHaveBeenCalledWith('web-2')

    chat().vm.$emit('batch-stop')
    await flushPromises()
    expect(stopMock).toHaveBeenCalledWith('web-2')

    // Running it again is one more execution of the task, and the page moves
    // to that execution's own address.
    vi.spyOn(forebrainApi, 'createTaskSession').mockResolvedValue({
      id: 'web-3',
      title: '审核三季度底稿 #3',
      project: {} as never,
      task: { id: 'tsk-1', title: '审核三季度底稿', externalRef: '', origin: 'web' },
      execution: { seq: 3, status: 'pending', workspace: '/w/tasks/tsk-1/3', workspaceRel: 'tasks/tsk-1/3' },
    })
    chat().vm.$emit('batch-rerun')
    await flushPromises()
    expect(startMock).toHaveBeenLastCalledWith('web-3')
    wrapper.unmount()
  })

  it('never puts the execution on a clock', async () => {
    const getTask = vi.spyOn(forebrainApi, 'getTask')
    const interval = vi.fn(() => 1)
    vi.stubGlobal('setInterval', interval)
    const { wrapper, seq } = await mountStatus('running')
    const chat = () => wrapper.getComponent({ name: 'ChatView' })

    expect(chat().props('taskRun')).toMatchObject({ status: 'running' })
    // The run moves on, and the page is told rather than asking.
    frame('failed', seq)
    await flushPromises()
    expect(chat().props('taskRun')).toMatchObject({ status: 'failed' })
    expect(batchRunActions(chat().props('taskRun').status)).toEqual(['rerun'])

    await flushPromises()
    expect(interval).not.toHaveBeenCalled()
    // One read, the page's own: nothing else was asked of the task.
    expect(getTask).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('follows a continued execution to the state the engine reports', async () => {
    const interval = vi.fn(() => 1)
    vi.stubGlobal('setInterval', interval)
    const { wrapper, seq } = await mountStatus('stopped')
    const chat = () => wrapper.getComponent({ name: 'ChatView' })

    expect(batchRunActions(chat().props('taskRun').status)).toEqual(['continue', 'rerun'])
    chat().vm.$emit('batch-continue')
    await flushPromises()
    expect(startMock).toHaveBeenCalledWith(`web-${seq}`)

    // The engine admitted the turn: the execution is running again, and the
    // bar offers the act that state allows.
    frame('running', seq, 2)
    await flushPromises()
    expect(chat().props('taskRun')).toMatchObject({ status: 'running' })
    expect(batchRunActions(chat().props('taskRun').status)).toEqual(['stop'])

    frame('completed', seq, 3)
    await flushPromises()
    expect(chat().props('taskRun')).toMatchObject({ status: 'completed' })
    expect(interval).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('follows a stop until the execution is really stopped', async () => {
    const { wrapper, seq } = await mountStatus('running')
    const chat = () => wrapper.getComponent({ name: 'ChatView' })

    chat().vm.$emit('batch-stop')
    await flushPromises()
    expect(stopMock).toHaveBeenCalledWith(`web-${seq}`)
    // Still 运行中: the cancel has been asked for, not yet applied.
    expect(chat().props('taskRun')).toMatchObject({ status: 'running' })

    frame('stopped', seq, 2)
    await flushPromises()
    expect(chat().props('taskRun')).toMatchObject({ status: 'stopped' })
    expect(batchRunActions(chat().props('taskRun').status)).toEqual(['continue', 'rerun'])
    wrapper.unmount()
  })

  it('draws the state the engine reported over the one its own read found', async () => {
    // The page opens while the execution is still pending, and the turn is
    // admitted before its socket finished binding: the frame is the newer
    // fact, so it is what the page draws — a page opened during a run shows
    // the run, not the state the task held a moment earlier.
    const seq = nextSeq()
    vi.spyOn(forebrainApi, 'getTask').mockResolvedValue(history([run(seq, 'pending')], seq))
    frame('running', seq)
    const { wrapper } = await mountRun(seq)

    const chat = wrapper.getComponent({ name: 'ChatView' })
    expect(chat.props('taskRun')).toMatchObject({ seq, status: 'running' })
    expect(batchRunActions(chat.props('taskRun').status)).toEqual(['stop'])
    wrapper.unmount()
  })

  it('catches up once when its socket reconnects with no frame to drive it', async () => {
    const getTask = vi.spyOn(forebrainApi, 'getTask')
    const { wrapper, seq } = await mountStatus('running')
    const chat = () => wrapper.getComponent({ name: 'ChatView' })

    // The attach that follows the page's own read: no reading owed.
    noteSessionBound(`web-${seq}`)
    await flushPromises()
    expect(getTask).toHaveBeenCalledTimes(1)

    // A reconnect with no execution frame ever seen: the one catch-up read.
    noteSessionBound(`web-${seq}`)
    await flushPromises()
    expect(getTask).toHaveBeenCalledTimes(2)
    expect(chat().props('taskRun')).toMatchObject({ status: 'running' })

    // Once frames arrive they are what moves the page, and a reconnect reads
    // nothing more.
    frame('stopped', seq, 2)
    await flushPromises()
    noteSessionBound(`web-${seq}`)
    await flushPromises()
    expect(getTask).toHaveBeenCalledTimes(2)
    expect(chat().props('taskRun')).toMatchObject({ status: 'stopped' })
    wrapper.unmount()
  })

  // A run's state is carried by the conversation's own frames, so a line that
  // dropped leaves the page drawing the last frame it received — which looks
  // exactly like a run that stopped moving. The page says which it is, and
  // while the line is fully out it offers one read, at the person's asking.
  it('says the live line is down, and offers one read once it is out', async () => {
    const getTask = vi.spyOn(forebrainApi, 'getTask')
    const { wrapper, seq } = await mountStatus('running')
    const live = () => wrapper.find('[data-testid="task-run-live"]')

    // Frames are arriving: nothing to say.
    expect(live().exists()).toBe(false)

    setLiveState(`web-${seq}`, 'reconnecting')
    await flushPromises()
    expect(live().text()).toContain('实时更新已暂停')
    expect(wrapper.find('[data-testid="task-run-live-refresh"]').exists()).toBe(false)

    setLiveState(`web-${seq}`, 'offline')
    await flushPromises()
    expect(live().text()).toContain('实时更新已断开')

    // One press, one read: the button is not a clock.
    const before = getTask.mock.calls.length
    await wrapper.get('[data-testid="task-run-live-refresh"]').trigger('click')
    await flushPromises()
    expect(getTask.mock.calls.length).toBe(before + 1)
    wrapper.unmount()
  })
})
