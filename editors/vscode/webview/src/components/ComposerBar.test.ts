import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ComposerBar from './ComposerBar.vue'
import ModeChip from '@/components/chat/ModeChip.vue'
import ModelChip from '@/components/chat/ModelChip.vue'
import ProjectPill from '@/components/chat/ProjectPill.vue'
import { emptyPendingInputPreview, type RuntimeStatus } from '@/composables/useChatStream'
import type { ProjectRecord, SessionModelResponse } from '@/lib/api'
import type { ProjectTarget } from '@/lib/projectTarget'

const project = {
  id: 'p1', name: 'forebrain-harness', root: '/ws/repo', active: true,
} as unknown as ProjectRecord
const target: ProjectTarget = { projectId: 'p1', name: 'forebrain-harness', root: '/ws/repo', isNew: false, trustLevel: 'trusted' }

const baseProps = () => ({
  sessionId: null as string | null,
  mode: 'agent',
  isStreaming: false,
  composerLocked: false,
  placeholder: '输入消息，/ 调出命令，@ 引用文件',
  submitLabel: '发送',
  submitStatus: 'ready' as 'ready' | 'streaming',
  sessionModelData: null as SessionModelResponse | null,
  sessionEfforts: [] as string[],
  sessionDefaultEffort: '',
  scope: {},
  suggestion: null as string | null,
  queueVisible: false,
  pendingInputPreview: emptyPendingInputPreview(),
  availableBots: [],
  botsFetchDone: true,
  contextUsedPercent: null as number | null,
  composerNotice: null as { title: string; detail: string; detailCode?: string } | null,
  freshChat: true,
  projects: [project],
  target: null as ProjectTarget | null,
  pillInvalid: false,
  runtimeStatus: { kind: 'idle', elapsedMs: 0 } as RuntimeStatus,
})

function bar(overrides: Partial<ReturnType<typeof baseProps>> = {}) {
  return mount(ComposerBar, { props: { ...baseProps(), ...overrides } })
}

describe('the webview composer', () => {
  afterEach(() => vi.restoreAllMocks())

  it('draws the bar from the first frame: pill, plus, mode, preset, view mode, model, send', () => {
    const wrapper = bar()
    expect(wrapper.findComponent(ProjectPill).exists()).toBe(true)
    expect(wrapper.find('[data-testid="webview-starters"]').exists()).toBe(true)
    // The send button is icon-only: its job is spoken by the aria-label.
    expect(wrapper.find('button[aria-label="发送"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('Agent')
  })

  it('carries the placeholder the prototype writes', () => {
    const wrapper = bar()
    expect(String(wrapper.find('textarea').attributes('placeholder'))).toContain('输入消息')
  })

  it('starts a turn on submit and stops a running one on the same button', async () => {
    const wrapper = bar({ submitLabel: '停止', submitStatus: 'streaming', isStreaming: true })
    const submit = wrapper.findComponent({ name: 'PromptInputSubmit' })
    expect(submit.exists()).toBe(true)
    await submit.vm.$emit('stop')
    expect(wrapper.emitted('stop')).toHaveLength(1)
  })

  it('hands a mode pick to the parent', async () => {
    const wrapper = bar()
    await wrapper.findComponent(ModeChip).vm.$emit('select', 'plan')
    expect(wrapper.emitted('select-mode')?.[0]).toEqual(['plan'])
  })

  it('shows the model chip once the session data lands', () => {
    const wrapper = bar({
      sessionId: 's-1',
      sessionModelData: {
        modelId: 'm1', provider: 'openai', model: 'gpt-x', effort: 'high', options: [
          { id: 'm1', provider: 'openai', model: 'gpt-x', defaultEffort: 'high', efforts: ['low', 'high'] },
        ],
      } as unknown as SessionModelResponse,
      sessionEfforts: ['low', 'high'],
      freshChat: false,
    })
    const chip = wrapper.findComponent(ModelChip)
    expect(chip.exists()).toBe(true)
    expect(chip.props('disabled')).toBe(false)
    expect(wrapper.findComponent({ name: 'EffortChip' }).exists()).toBe(true)
  })

  it("says what a refused send or upload has to say, in the web's words", () => {
    const wrapper = bar({ composerNotice: { title: '消息没有发送', detail: '网关没有应答' } })
    expect(wrapper.get('[data-testid="composer-notice"]').text()).toContain('消息没有发送')
    expect(wrapper.get('[data-testid="composer-notice"]').text()).toContain('网关没有应答')
  })

  it('offers the two starters a conversation can act on and inserts the command', async () => {
    const wrapper = bar()
    const starters = wrapper.findAll('[data-testid="webview-starter"]')
    expect(starters).toHaveLength(2)
    await starters[0].trigger('click')
    expect(wrapper.emitted('insert-plus')?.[0]).toEqual(['/init '])
    await starters[1].trigger('click')
    expect(wrapper.emitted('select-mode')?.[0]).toEqual(['plan'])
  })

  it('picks a project through the pill and marks it when a send was refused for want of one', async () => {
    const wrapper = bar({ pillInvalid: true, target: null })
    const pill = wrapper.findComponent(ProjectPill)
    expect(pill.props('invalid')).toBe(true)
    await pill.vm.$emit('select', target)
    expect(wrapper.emitted('pick-target')?.[0]).toEqual([target])
  })

  it('shows the queued messages the run holds, above the input', () => {
    const wrapper = bar({
      isStreaming: true,
      queueVisible: true,
      pendingInputPreview: emptyPendingInputPreview(),
    })
    expect(wrapper.findComponent({ name: 'PendingInputQueuePopover' }).exists()).toBe(true)
  })
})
