import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import { forebrainApi, type ActionRecord } from '@/lib/api'
import type { ApprovalDecisionName, ApprovalDecisionOption } from '@/lib/approvalSuggestions'
import { setLocale } from '@/locales'
import ApprovalGateCard from './ApprovalGateCard.vue'

/**
 * The one card every approval family shares. What is pinned here is its
 * structure: exactly one primary answer, the fact lines in order, the diff
 * signs, long strings folding instead of truncating, and the requester row
 * appearing only for a subagent's request.
 */
function gateRecord(payload: Record<string, unknown>, extra: Partial<ActionRecord> = {}): ActionRecord {
  return { id: 'act-1', kind: 'write_file', status: 'pending', payloadJson: JSON.stringify(payload), createdAt: 1, updatedAt: 1, ...extra }
}

function mountCard(record: ActionRecord, options: ApprovalDecisionName[] = ['accept', 'accept_for_session', 'cancel']) {
  const submit = vi.fn()
  const wrapper = mount(ApprovalGateCard, {
    props: {
      action: record,
      options: options.map((decision) => ({ decision }) as ApprovalDecisionOption),
      decisionLabel: (option: { decision: string }) => option.decision,
      submit,
      requester: '',
    },
  })
  return { wrapper, submit }
}

describe('ApprovalGateCard', () => {
  beforeAll(() => setLocale('zh'))
  afterAll(async () => {
    await vi.dynamicImportSettled()
    setLocale('en')
  })
  afterEach(() => vi.restoreAllMocks())

  it('marks exactly one primary answer', () => {
    const { wrapper } = mountCard(gateRecord({ content: 'a\n', existing_lines: 2 }), ['accept', 'accept_for_session', 'cancel'])
    const primaries = wrapper.findAll('button').filter((b) => b.classes().includes('forebrain-btn-primary'))
    expect(primaries).toHaveLength(1)
    expect(primaries[0]?.attributes('data-testid')).toBe('pending-decision-accept')
    // The primary falls to the family's own first tier when accept is absent.
    const granted = mountCard(gateRecord({ permissions: {} }), ['grant_for_turn', 'grant_for_session', 'decline'])
    const grantedPrimaries = granted.wrapper.findAll('button').filter((b) => b.classes().includes('forebrain-btn-primary'))
    expect(grantedPrimaries).toHaveLength(1)
    expect(grantedPrimaries[0]?.attributes('data-testid')).toBe('pending-decision-grant_for_turn')
  })

  it('shows the fact lines in order, folding long strings instead of cutting them', () => {
    const record = gateRecord({
      content: 'a\nb\nc',
      existing_lines: 2,
      resolved_file_path: '/very/long/path/that/would/otherwise/be/cut/off/entirely/somefile.go',
    })
    const { wrapper } = mountCard(record, ['accept', 'cancel'])
    const factRows = wrapper.findAll('[data-testid="approval-gate-fact"]')
    expect(factRows.map((row) => row.text())).toEqual(['+3 −2 行'])
    expect(factRows[0]?.classes()).toContain('break-words')
  })

  it('draws the diff signs: additions green, removals red, in order', () => {
    const edit = gateRecord({ old_string: 'old', new_string: 'new' }, { kind: 'edit_file' })
    const { wrapper } = mountCard(edit, ['accept', 'cancel'])
    const diff = wrapper.find('[data-testid="approval-gate-diff"]')
    expect(diff.exists()).toBe(true)
    const rows = diff.findAll('div')
    expect(rows).toHaveLength(2)
    expect(rows[0]?.classes()).toContain('gate-diff-del')
    expect(rows[1]?.classes()).toContain('gate-diff-add')
    expect(rows[0]?.text()).toBe('-old')
    expect(rows[1]?.text()).toBe('+new')
  })

  it('shows a memory note\'s body rendered, scrollable and uncut', async () => {
    const note = gateRecord({ filename: 'n.md', note: '# 标题\n\n正文很长', scope: 'project' }, { kind: 'memories_add_ad_hoc_note' })
    const { wrapper } = mountCard(note, ['accept', 'decline'])
    const body = wrapper.find('[data-testid="approval-gate-body"]')
    expect(body.exists()).toBe(true)
    // A note is Markdown, so its heading is drawn rather than printed, and the
    // renderer types it out over a tick.
    await vi.waitFor(() => expect(body.text()).toContain('标题'), { timeout: 2000 })
    expect(body.classes()).toContain('overflow-y-auto')
  })

  it('names a subagent requester only when there is one to name', async () => {
    vi.spyOn(forebrainApi, 'actionsList').mockResolvedValue([])
    const record = gateRecord({ content: 'a' }, { agentId: 'child-1', subagentType: 'explorer' })
    const plain = gateCardWithRequester(record, '由子智能体 explorer 发起')
    expect(plain.find('[data-testid="approval-gate-file_write"]').text()).toContain('explorer')

    const none = gateCardWithRequester(gateRecord({ content: 'a' }), '')
    expect(none.find('[data-testid="approval-gate-file_write"]').text()).not.toContain('子智能体')
  })

  it('submits the option whose button was pressed', async () => {
    const { wrapper, submit } = mountCard(gateRecord({ content: 'a' }), ['accept', 'cancel'])
    await wrapper.find('[data-testid="pending-decision-cancel"]').trigger('click')
    expect(submit).toHaveBeenCalledWith({ decision: 'cancel' })
  })
})

function gateCardWithRequester(record: ActionRecord, requester: string) {
  return mount(ApprovalGateCard, {
    props: {
      action: record,
      options: [{ decision: 'accept' } as ApprovalDecisionOption, { decision: 'cancel' } as ApprovalDecisionOption],
      decisionLabel: (option: { decision: string }) => option.decision,
      submit: () => {},
      requester,
    },
  })
}
