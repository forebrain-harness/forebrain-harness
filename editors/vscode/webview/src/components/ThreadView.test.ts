import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ThreadView from './ThreadView.vue'
import ApprovalCard from '@/components/chat/ApprovalCard.vue'
import DiffView from '@/components/DiffView.vue'
import PeerDeliveryCard from '@/components/chat/PeerDeliveryCard.vue'
import PeerMessageCard from '@/components/chat/PeerMessageCard.vue'
import SlashPickerCard from '@/components/chat/SlashPickerCard.vue'
import SubagentCallCard from '@/components/chat/SubagentCallCard.vue'
import ThinkingCard from '@/components/chat/ThinkingCard.vue'
import ToolCallCard from '@/components/chat/ToolCallCard.vue'
import RunErrorBlock from '@/components/chat/RunErrorBlock.vue'
import type { ChatMessage, TimelineBlock } from '@/composables/useChatStream'

const base = {
  sessionId: 's-1' as string | null,
  isStreaming: false,
  error: null as string | null,
  historyLoading: false,
  historyError: null as string | null,
  scope: {},
}

function threadOf(...messages: ChatMessage[]) {
  return mount(ThreadView, { props: { ...base, messages } })
}

function messageWith(blocks: TimelineBlock[], extra: Partial<ChatMessage> = {}): ChatMessage {
  return { id: 'a1', role: 'assistant', content: '最终回答', ...extra, blocks }
}

/** The prose renderer types its content out; assertions wait for the words. */
async function expectSoon(wrapper: { text: () => string }, contains: string) {
  await vi.waitFor(() => expect(wrapper.text()).toContain(contains), { timeout: 2000 })
}

describe('the webview thread', () => {
  it('reads the user message verbatim — never as Markdown', () => {
    const wrapper = threadOf({ id: 'u1', role: 'user', content: '**不是粗体** hello' })
    const bubble = wrapper.get('[data-testid="webview-thread"] .wv-user-bubble')
    expect(bubble.text()).toContain('**不是粗体** hello')
    expect(bubble.find('strong').exists()).toBe(false)
  })

  it('draws the turn as the activity line: thinking, the call, the answer', async () => {
    const wrapper = threadOf(messageWith([
      { kind: 'thinking', id: 't1', text: '想一想', open: false, startedAt: 1 },
      { kind: 'tool', id: 'to1', step: { stepId: 's1', toolName: 'shell', summary: 'ls', header: 'Ran ls', status: 'success', output: 'ok' } },
      { kind: 'assistant', id: 'as1', text: '中间回答', open: false },
    ]))
    expect(wrapper.findComponent(ThinkingCard).exists()).toBe(true)
    const card = wrapper.findComponent(ToolCallCard)
    expect(card.exists()).toBe(true)
    expect(card.props('step').status).toBe('success')
    await expectSoon(wrapper, '中间回答')
    await expectSoon(wrapper, '最终回答')
  })

  it('draws a failed call through the same card, whose danger colouring is the card\'s own', () => {
    const wrapper = threadOf(messageWith([
      { kind: 'tool', id: 'to2', step: { stepId: 's2', toolName: 'shell', summary: 'ls', header: 'Ran ls', status: 'failed', error: 'boom' } },
    ]))
    const card = wrapper.findComponent(ToolCallCard)
    expect(card.exists()).toBe(true)
    expect(card.props('step').status).toBe('failed')
  })

  it('renders a subagent dispatch as its own card', () => {
    const wrapper = threadOf(messageWith([
      { kind: 'subagent', id: 'sb1', stepId: 'sb-step', step: { stepId: 'sb-step', toolName: 'subagent', summary: 'plan-reviewer', header: 'plan-reviewer', status: 'running' } },
    ]))
    const card = wrapper.findComponent(SubagentCallCard)
    expect(card.exists()).toBe(true)
    expect(card.props('step').stepId).toBe('sb-step')
  })

  it('renders a settled approval as one line off the call', () => {
    const wrapper = threadOf(messageWith([
      { kind: 'approval', id: 'ap1', actionId: 'act-1', actionKind: 'approve_action', status: 'approved', confirmation: '已允许' },
    ]))
    expect(wrapper.findComponent(ApprovalCard).exists()).toBe(true)
  })

  it('renders peer letters and delivery notes as the cards the web thread uses', async () => {
    const wrapper = threadOf(
      { id: 'p1', role: 'user', content: '', origin: 'peer', peerFromName: 'reviewer', peerMark: 'rv', peerTone: 1, peerBody: '评审意见' },
      { id: 'p2', role: 'user', content: '', origin: 'peer_status', peerReceipts: [{ name: 'reviewer', status: 'read', mark: 'rv', tone: 1 }] },
    )
    expect(wrapper.findComponent(PeerMessageCard).exists()).toBe(true)
    expect(wrapper.findComponent(PeerDeliveryCard).exists()).toBe(true)
    await expectSoon(wrapper, '评审意见')
  })

  it('hands the turn diff to the shared diff view', () => {
    const wrapper = threadOf(messageWith([], {
      turnDiffs: [{ path: 'a.go', summary: 'a.go', added: 2, deleted: 1, hunks: [] }],
    }))
    expect(wrapper.findComponent(DiffView).exists()).toBe(true)
  })

  it('renders a slash notice with its picker and chooses through the parent', async () => {
    const wrapper = threadOf({
      id: 'n1', role: 'notice', content: '选择一个命令',
      picker: { command: 'agent', title: '选择代理', items: [{ value: 'a', label: 'A' }] },
    })
    expect(wrapper.findComponent(SlashPickerCard).exists()).toBe(true)
    await wrapper.findComponent(SlashPickerCard).vm.$emit('choose', 'a')
    const chosen = wrapper.emitted('choose')
    expect(chosen?.[0]?.[1]).toBe('a')
  })

  it('shows the run error the web thread shows, in its own card', () => {
    const wrapper = threadOf(messageWith([
      { kind: 'error', id: 'e1', text: '网关断了' },
    ]))
    expect(wrapper.findComponent(RunErrorBlock).exists()).toBe(true)
    expect(wrapper.text()).toContain('网关断了')
  })

  it('says the request failed when the surface itself errors', () => {
    const wrapper = mount(ThreadView, { props: { ...base, messages: [], error: 'HTTP 500' } })
    expect(wrapper.text()).toContain('请求失败')
    expect(wrapper.text()).toContain('HTTP 500')
  })

  it('offers the retry the history refusal owes, and asks again on click', async () => {
    const wrapper = mount(ThreadView, { props: { ...base, messages: [], historyError: 'gateway down' } })
    expect(wrapper.text()).toContain('历史记录加载失败')
    await wrapper.get('[data-testid="thread-history-retry"]').trigger('click')
    expect(wrapper.emitted('retry-history')).toHaveLength(1)
  })

  it('says why nothing went when the conversation runs somewhere else', () => {
    const wrapper = mount(ThreadView, { props: { ...base, messages: [], busyNotice: 'session_running_elsewhere' } })
    expect(wrapper.get('[data-testid="busy-notice"]').text()).toContain('另一端运行')
  })
})
