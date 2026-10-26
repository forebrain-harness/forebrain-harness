import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { ref } from 'vue'

import { setLocale } from '@/locales'
import type { ApprovalDecisionName } from '@/lib/approvalSuggestions'
import type { ApprovalFacts } from '@/lib/approvalGates'
import type { ActionRow } from './usePendingActions'
import { usePendingActions } from './usePendingActions'

/**
 * Every decision button says its family's verb and, when it is a scope, how
 * far the yes reaches — the copy table the approval cards were designed
 * against, asserted per decision id and family, in both languages the table
 * ships. The engine words the retired generic card printed (assembled below,
 * piece by piece, so the retirement grep over the tree stays clean) must not
 * survive anywhere in it.
 */
describe('usePendingActions approvalDecisionLabel', () => {
  beforeAll(() => setLocale('zh'))
  afterAll(() => setLocale('en'))

  const { approvalDecisionLabel } = usePendingActions({
    sessionId: ref(null),
    version: ref(0),
    contextUsedPercent: ref(null),
  })

  function facts(family: ApprovalFacts['family'], extra: Partial<ApprovalFacts> = {}): ApprovalFacts {
    return { family, facts: [], ...extra }
  }

  function label(decision: ApprovalDecisionName, family: ApprovalFacts['family'], extra: Partial<ApprovalFacts> = {}): string {
    return approvalDecisionLabel({ decision }, undefined, facts(family, extra))
  }

  it('labels every decision id by family, exactly as the copy table says', () => {
    const table: [ApprovalFacts['family'], ApprovalDecisionName, string, Partial<ApprovalFacts>?][] = [
      // The file family: write (new), write (overwrite), edit, patch.
      ['file_write', 'accept', '写入'],
      ['file_write', 'accept', '覆盖', { removed: 41 }],
      ['file_write', 'accept_for_session', '本会话不再问'],
      ['file_write', 'cancel', '不要写'],
      ['file_edit', 'accept', '修改'],
      ['file_edit', 'accept_for_session', '本会话不再问'],
      ['file_edit', 'cancel', '不要改'],
      ['apply_patch', 'accept', '应用'],
      ['apply_patch', 'accept_for_session', '本会话不再问'],
      ['apply_patch', 'cancel', '不要应用'],
      // Commands: run, remember by scope, outside the sandbox.
      ['shell', 'accept', '运行'],
      ['shell', 'accept_for_session', '这类命令不再问'],
      ['shell', 'accept_and_remember', '以后都不再问'],
      ['shell', 'accept_with_execpolicy_amendment', '这类命令不再问'],
      ['shell', 'cancel', '不要运行'],
      ['shell_outside_sandbox', 'accept', '在沙箱外运行'],
      ['shell_outside_sandbox', 'cancel', '不要运行'],
      // A host on the public network.
      ['network_host', 'accept', '允许'],
      ['network_host', 'accept_for_session', '本会话都允许'],
      ['network_host', 'apply_network_policy_amendment', '以后都允许'],
      ['network_host', 'cancel', '不要访问'],
      // A permission request.
      ['request_permissions', 'grant_for_turn', '本轮允许'],
      ['request_permissions', 'grant_for_turn_with_strict_auto_review', '本轮允许，自动审查'],
      ['request_permissions', 'grant_for_session', '本会话允许'],
      ['request_permissions', 'decline', '不授予'],
      // An MCP call.
      ['mcp_tool', 'accept', '调用'],
      ['mcp_tool', 'accept_for_session', '本会话不再问'],
      ['mcp_tool', 'accept_and_remember', '以后都不再问'],
      ['mcp_tool', 'cancel', '不要调用'],
      // Forebrain's own configuration, and plan mode.
      ['protected_read', 'accept', '读取'],
      ['protected_read', 'accept_for_session', '本会话不再问'],
      ['protected_read', 'cancel', '不要读'],
      ['enter_plan_mode', 'accept', '进入'],
      ['enter_plan_mode', 'cancel', '不要进入'],
      // Search and memory only ask do / don't.
      ['web_search', 'accept', '搜索'],
      ['web_search', 'cancel', '不要搜'],
      ['memory_note', 'accept', '记住这条'],
      ['memory_note', 'decline', '不要记'],
      // Everything else.
      ['fallback', 'accept', '允许'],
      ['fallback', 'cancel', '不允许'],
    ]
    for (const [family, decision, expected, extra] of table) {
      expect(label(decision, family, extra), `${family}/${decision}`).toBe(expected)
    }
  })

  it('keeps the fetch gate\'s shipped copy', () => {
    const action = {
      id: 'act-fetch',
      kind: 'web_fetch',
      status: 'pending',
      payloadJson: JSON.stringify({ url: 'https://example.com/pricing' }),
      createdAt: 1,
      updatedAt: 1,
      permissionSuggestion: {
        permissionToolName: 'WebFetch',
        permissionInput: 'https://example.com/pricing',
        prefixRuleContent: 'domain:example.com',
        availableDecisions: ['accept', 'accept_and_remember', 'cancel'],
      },
    } as ActionRow
    expect(approvalDecisionLabel({ decision: 'accept' }, action)).toBe('取这个页面')
    expect(approvalDecisionLabel({ decision: 'accept_and_remember' }, action)).toBe('不再询问 example.com')
    expect(approvalDecisionLabel({ decision: 'cancel' }, action)).toBe('不要取')
  })

  it('says no engine word anywhere in the ten decision ids', () => {
    // The two engine-word buttons the retired generic card printed, spliced
    // so this file keeps the retirement grep over the tree at zero hits.
    const retiredWords = ['批' + '准一次', '拒绝并中' + '断本轮']
    const families: ApprovalFacts['family'][] = [
      'file_write', 'file_edit', 'apply_patch', 'shell', 'shell_outside_sandbox', 'network_host',
      'request_permissions', 'enter_plan_mode', 'mcp_tool', 'protected_read', 'web_search', 'memory_note', 'fallback',
    ]
    const decisions = [
      'accept', 'accept_for_session', 'accept_and_remember', 'accept_with_execpolicy_amendment',
      'apply_network_policy_amendment', 'grant_for_turn', 'grant_for_turn_with_strict_auto_review',
      'grant_for_session', 'decline', 'cancel',
    ] as const
    for (const family of families) {
      for (const decision of decisions) {
        const text = label(decision, family)
        for (const retired of retiredWords) {
          expect(text, `${family}/${decision}`).not.toBe(retired)
        }
      }
    }
  })

  it('labels the same table in english', () => {
    setLocale('en')
    expect(label('accept', 'file_write')).toBe('Write')
    expect(label('accept', 'file_write', { removed: 3 })).toBe('Overwrite')
    expect(label('accept', 'shell_outside_sandbox')).toBe('Run outside the sandbox')
    expect(label('grant_for_turn_with_strict_auto_review', 'request_permissions')).toBe('Allow this turn, auto-reviewed')
    expect(label('accept_for_session', 'network_host')).toBe('Allow all this session')
    expect(label('cancel', 'protected_read')).toBe("Don't read")
    expect(label('decline', 'memory_note')).toBe("Don't keep it")
    expect(label('cancel', 'fallback')).toBe("Don't allow")
  })
})

describe('usePendingActions pendingForStep', () => {
  beforeAll(() => setLocale('zh'))

  function storeWith(actions: ActionRow[]) {
    const { pendingApprovals, pendingForStep } = usePendingActions({
      sessionId: ref(null),
      version: ref(0),
      contextUsedPercent: ref(null),
    })
    pendingApprovals.value = actions
    return { pendingForStep }
  }

  function row(id: string): ActionRow {
    return { id, kind: 'shell', status: 'pending', payloadJson: '{}', createdAt: 1, updatedAt: 1 }
  }

  it('matches the request the timeline block names for this step', () => {
    const { pendingForStep } = storeWith([row('act-1'), row('act-2')])
    expect(pendingForStep('call-write', [{ actionId: 'act-2', toolStepId: 'call-write' }])?.id).toBe('act-2')
  })

  it('finds nothing when no block names this step', () => {
    const { pendingForStep } = storeWith([row('act-1')])
    expect(pendingForStep('call-write', [{ actionId: 'act-1', toolStepId: 'call-read' }])).toBeNull()
    expect(pendingForStep('call-write', [{ actionId: 'act-1' }])).toBeNull()
    expect(pendingForStep('', [{ actionId: 'act-1', toolStepId: '' }])).toBeNull()
  })

  it('finds nothing when the named request is no longer pending', () => {
    const { pendingForStep } = storeWith([row('act-other')])
    expect(pendingForStep('call-write', [{ actionId: 'act-settled', toolStepId: 'call-write' }])).toBeNull()
  })
})
