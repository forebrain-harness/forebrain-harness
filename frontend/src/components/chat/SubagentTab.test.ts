import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { config, mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import tip from '@/directives/tip'
import { forebrainApi, type SlashCommandRecord } from '@/lib/api'
import { setLocale, t } from '@/locales'
import type { SubagentTranscript } from '@/composables/useChatStream'
import ForebrainPromptTextarea from '@/components/ForebrainPromptTextarea.vue'
import SubagentRunPill from './SubagentRunPill.vue'
import SubagentTab from './SubagentTab.vue'

config.global.directives = { tip }

/**
 * One subagent as a drawer tab: its transcript, its own working line, and a
 * composer of its own — the same controls as the conversation's, pointed at
 * this subagent.
 */
describe('SubagentTab', () => {
  beforeAll(() => setLocale('en'))
  afterAll(async () => {
    await vi.dynamicImportSettled()
    setLocale('en')
  })
  afterEach(() => {
    vi.restoreAllMocks()
    setLocale('en')
  })

  function record(overrides: Partial<SubagentTranscript> = {}): SubagentTranscript {
    return {
      agentId: 'task-1',
      agentType: 'plan-reviewer',
      title: 'review plan 078',
      task: 'review the plan',
      status: 'running',
      blocks: [{ kind: 'prompt', text: 'review the plan' }],
      inputTokens: 0,
      outputTokens: 0,
      startedAt: new Date().toISOString(),
      updatedSeq: 1,
      ...overrides,
    }
  }

  function mountTab(props: Record<string, unknown> = {}) {
    const list = vi.spyOn(forebrainApi, 'slashCommands').mockResolvedValue({
      records: [{
        canonicalName: 'plan-writing', name: 'plan-writing', description: 'writes plans', category: 'skill',
        argumentHint: '', actionKind: 'local', allowedModes: [], supportsInlineArgs: false,
        availableDuringRun: true, availableInSideConversation: true, visibility: 'public',
      } as SlashCommandRecord],
    } as Awaited<ReturnType<typeof forebrainApi.slashCommands>>)
    const wrapper = mount(SubagentTab, {
      props: {
        record: record(),
        foldState: {},
        pendingInput: { pendingSteers: [], rejectedSteers: [], queuedMessages: [] },
        ...props,
      },
      global: {
        stubs: {
          Conversation: { template: '<div><slot /></div>' },
          ConversationContent: { template: '<div><slot /></div>' },
          ConversationScrollButton: true,
          ContextDebugPanel: true,
        },
      },
    })
    return { wrapper, list }
  }

  it('gives the subagent a composer of its own, with the conversation’s controls', () => {
    const scope = { sessionId: 's1' }
    const { wrapper } = mountTab({ scope })
    const textarea = wrapper.findComponent(ForebrainPromptTextarea)
    expect(textarea.props('placeholder')).toBe('Message this subagent — / for commands, @ to reference files')
    // The "/" menu and the "save to" dialog both belong to this tab's view.
    expect(textarea.props('subagentView')).toBe(true)
    expect(textarea.props('attachSave')).toBe(true)
    expect(textarea.props('scope')).toEqual(scope)
    // The bar carries the "+", the model and context pill, and send.
    expect(wrapper.find('[data-testid="composer-plus"]').exists()).toBe(true)
    expect(wrapper.findComponent(SubagentRunPill).exists()).toBe(true)
    // The send control says what it does in the reader's language, not "Submit".
    expect(wrapper.find(`button[aria-label="${t('chat.sendAria')}"]`).exists()).toBe(true)
  })

  it('leaves the goal out of the tab’s "+" menu and inserts a picked skill', async () => {
    const { wrapper } = mountTab()
    await wrapper.get('[data-testid="composer-plus"]').trigger('click')
    expect(wrapper.find('[data-testid="plus-goal"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="plus-attach"]').exists()).toBe(true)

    await wrapper.get('[data-testid="plus-skill-plan-writing"]').trigger('click')
    await nextTick()
    // The same path the conversation's "+" takes: the command lands in this
    // tab's composer, ready to send (the shared merge trims the trailing space).
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('/plan-writing')
  })

  it('offers to stop the subagent only while it runs', async () => {
    const { wrapper } = mountTab()
    await wrapper.get('[data-testid="subagent-stop"]').trigger('click')
    expect(wrapper.emitted('stop')).toHaveLength(1)

    await wrapper.setProps({ record: record({ status: 'done' }) })
    expect(wrapper.find('[data-testid="subagent-stop"]').exists()).toBe(false)
  })

  it('draws the working line between the transcript and the composer', async () => {
    setLocale('zh')
    const { wrapper } = mountTab({ record: record({ plan: { done: 1, total: 3, active: 'fix the middleware' } }) })
    const working = wrapper.get('[data-testid="subagent-working"]')
    expect(working.text()).toContain('工作中')
    expect(working.text()).toContain('1/3')
    expect(working.text()).toContain('fix the middleware')
    expect(working.find('.plan-check').exists()).toBe(true)

    // Below the transcript and above the composer, the terminal's order.
    expect(working.element.previousElementSibling?.textContent).toContain('交给它的任务')
    expect(working.element.nextElementSibling?.querySelector('textarea')).not.toBeNull()

    await wrapper.setProps({ record: record({ status: 'done' }) })
    expect(wrapper.find('[data-testid="subagent-working"]').exists()).toBe(false)
  })

  it('carries the execution’s first token wait on the working line once measured', async () => {
    const { wrapper } = mountTab({ record: record({ firstTokenMs: 1234 }) })
    const working = wrapper.get('[data-testid="subagent-working"]')
    expect(working.text()).toContain('first token 1.2s')

    // An execution that has not produced output yet shows no wait, not "0s".
    await wrapper.setProps({ record: record({ firstTokenMs: undefined }) })
    expect(wrapper.get('[data-testid="subagent-working"]').text()).not.toContain('first token')
  })

  it('draws a composer only while the subagent runs, and a bar that reads it when idle', async () => {
    const { wrapper } = mountTab()
    // Running: the tab's own input box, its queue, its stop and its send.
    expect(wrapper.find('textarea').exists()).toBe(true)
    expect(wrapper.find('[data-testid="subagent-idle-bar"]').exists()).toBe(false)

    // Idle: an idle subagent takes no message (the gateway refuses one with
    // 409), so there is nothing to type into — what is left where the bar was
    // is the reading mode and the model-and-context pill, and nothing else.
    await wrapper.setProps({ record: record({ status: 'done' }) })
    expect(wrapper.find('textarea').exists()).toBe(false)
    expect(wrapper.find('[data-testid="subagent-stop"]').exists()).toBe(false)
    expect(wrapper.find(`button[aria-label="${t('chat.sendAria')}"]`).exists()).toBe(false)
    const bar = wrapper.get('[data-testid="subagent-idle-bar"]')
    expect(bar.find('[data-testid="view-mode-chip"]').exists()).toBe(true)
    expect(bar.findComponent(SubagentRunPill).exists()).toBe(true)
  })

  it('hands Escape to the tab’s own rule, once', async () => {
    // Idle there is no composer to spend the key, so the tab itself reports it
    // upward exactly once and the page's order (interrupt, cancel the
    // continuation, leave) is what acts on it.
    const { wrapper } = mountTab({ record: record({ status: 'done' }) })
    await wrapper.trigger('keydown', { key: 'Escape' })
    expect(wrapper.emitted('escape-key')).toHaveLength(1)
    expect(wrapper.emitted('cancel-auto-continue')).toBeUndefined()
  })

  it('reports a running composer’s Escape upward once, not twice', async () => {
    const { wrapper } = mountTab()
    await wrapper.get('textarea').trigger('keydown', { key: 'Escape' })
    // While it runs the composer spends the key on "interrupt and send", which
    // is the same three-step rule: the root hears it already prevented.
    expect(wrapper.emitted('escape-key')).toHaveLength(1)
    expect(wrapper.emitted('cancel-auto-continue')).toBeUndefined()
  })

  it('draws its own /context report when one is open', () => {
    const closed = mountTab().wrapper
    expect(closed.find('context-debug-panel-stub').exists()).toBe(false)

    const open = mountTab({ context: { data: null, loading: true } }).wrapper
    expect(open.find('context-debug-panel-stub').exists()).toBe(true)
  })

  it('forwards the transcript’s folds and nested dispatches to its host', async () => {
    const { wrapper } = mountTab()
    const conversation = wrapper.findComponent({ name: 'SubagentConversation' })
    conversation.vm.$emit('fold-change', { id: 'prompt:0', open: true })
    conversation.vm.$emit('open-agent', 'subagent-9a1f')
    expect(wrapper.emitted('fold-change')).toEqual([[{ id: 'prompt:0', open: true }]])
    expect(wrapper.emitted('open-agent')).toEqual([['subagent-9a1f']])
  })
})
