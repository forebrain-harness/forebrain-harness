import { describe, expect, it } from 'vitest'
import { setLocale, t } from '@/locales'
import { approvalDecisions, permissionSuggestionFromAction } from './approvalSuggestions'

describe('approval suggestions', () => {
  it('reads server-owned decisions and command amendments from action rows', () => {
    const suggestion = permissionSuggestionFromAction({
      id: 'act-1',
      kind: 'shell',
      status: 'pending',
      payloadJson: '{"command":"npm run build"}',
      createdAt: 1,
      updatedAt: 1,
      permissionSuggestion: {
        permissionToolName: 'Bash',
        permissionInput: 'npm run build',
        availableDecisions: [
          'accept',
          { acceptWithExecpolicyAmendment: { execpolicyAmendment: ['npm', 'run', 'build'] } },
          'cancel',
        ],
        proposedExecpolicyAmendment: ['npm', 'run', 'build'],
      },
    })

    expect(suggestion?.permissionToolName).toBe('Bash')
    expect(suggestion?.proposedExecpolicyAmendment).toEqual(['npm', 'run', 'build'])
    expect(approvalDecisions(suggestion)).toEqual([
      { decision: 'accept' },
      { decision: 'accept_with_execpolicy_amendment', execpolicyAmendment: ['npm', 'run', 'build'] },
      { decision: 'cancel' },
    ])
  })

  it('preserves repeated, empty, and whitespace command-prefix tokens exactly', () => {
    const tokens = ['printf', '%s%s%s', 'same', 'same', '', ' spaced ']
    const suggestion = permissionSuggestionFromAction({
      id: 'act-token-shape',
      kind: 'shell',
      status: 'pending',
      payloadJson: '{}',
      permissionSuggestion: {
        permissionToolName: 'Bash',
        availableDecisions: [
          { accept_with_execpolicy_amendment: { execpolicy_amendment: tokens } },
        ],
        proposedExecpolicyAmendment: tokens,
      },
    })

    expect(suggestion?.proposedExecpolicyAmendment).toEqual(tokens)
    expect(approvalDecisions(suggestion)).toEqual([
      { decision: 'accept_with_execpolicy_amendment', execpolicyAmendment: tokens },
    ])
  })

  it('drops the retired network-policy decision', () => {
    // The managed network proxy and its per-host approvals are gone (plan 206):
    // a stale payload naming one must contribute no button at all.
    const suggestion = approvalDecisions({
      permissionToolName: 'Bash',
      permissionInput: 'curl https://example.com',
      availableDecisions: [
        'accept',
        { apply_network_policy_amendment: { network_policy_amendment: { host: 'example.com', action: 'allow' } } },
        'cancel',
      ],
    })

    expect(suggestion.map((item) => item.decision)).toEqual(['accept', 'cancel'])
  })

  it('keeps request_permissions choices supplied by the server, without a reviewer', () => {
    const decisions = approvalDecisions({
      permissionToolName: 'request_permissions',
      availableDecisions: ['grant_for_turn', 'grant_for_session', 'decline'],
    })

    expect(decisions.map((item) => item.decision)).toEqual([
      'grant_for_turn',
      'grant_for_session',
      'decline',
    ])
    // Approving a permission request is a question of how long the grant lasts,
    // not of who reviews it: the strict auto-review answer is gone from every
    // surface (plan 203), and the frontend never adds it on its own.
    expect(decisions.map((item) => item.decision)).not.toContain('grant_for_turn_with_strict_auto_review')
  })
})

describe('command approval scope', () => {
  it('carries the server-derived scope on both persistent command decisions', () => {
    const suggestion = permissionSuggestionFromAction({
      id: 'act-scope',
      kind: 'shell',
      status: 'pending',
      payloadJson: '{"command":"export JAVA_HOME=/opt/jdk17 && mvn clean install 2>&1 | tail -12"}',
      createdAt: 1,
      updatedAt: 1,
      permissionSuggestion: {
        permissionToolName: 'Bash',
        permissionInput: 'export JAVA_HOME=/opt/jdk17 && mvn clean install 2>&1 | tail -12',
        availableDecisions: [
          'accept',
          { accept_and_remember: { command_scope: { kind: 'command_with_variants', prefixes: ['mvn clean'] } } },
          'cancel',
        ],
      },
    })

    expect(approvalDecisions(suggestion)).toEqual([
      { decision: 'accept' },
      {
        decision: 'accept_and_remember',
        commandScope: { kind: 'command_with_variants', prefixes: ['mvn clean'] },
      },
      { decision: 'cancel' },
    ])
  })

  it('keeps the remember decision usable when the server sends no scope', () => {
    const suggestion = permissionSuggestionFromAction({
      id: 'act-bare',
      kind: 'shell',
      status: 'pending',
      payloadJson: '{"command":"mvn clean install"}',
      createdAt: 1,
      updatedAt: 1,
      permissionSuggestion: {
        permissionToolName: 'Bash',
        permissionInput: 'mvn clean install',
        availableDecisions: ['accept', 'accept_and_remember', 'cancel'],
      },
    })

    expect(approvalDecisions(suggestion)).toEqual([
      { decision: 'accept' },
      { decision: 'accept_and_remember' },
      { decision: 'cancel' },
    ])
  })

  it('reads the scope that accompanies a prefix amendment', () => {
    const suggestion = permissionSuggestionFromAction({
      id: 'act-prefix',
      kind: 'shell',
      status: 'pending',
      payloadJson: '{"command":"go test ./..."}',
      createdAt: 1,
      updatedAt: 1,
      permissionSuggestion: {
        permissionToolName: 'Bash',
        permissionInput: 'go test ./...',
        availableDecisions: [
          'accept',
          {
            accept_with_execpolicy_amendment: {
              execpolicy_amendment: ['go', 'test'],
              command_scope: { kind: 'prefix', prefixes: ['go test'] },
            },
          },
          'cancel',
        ],
      },
    })

    expect(approvalDecisions(suggestion)).toEqual([
      { decision: 'accept' },
      {
        decision: 'accept_with_execpolicy_amendment',
        execpolicyAmendment: ['go', 'test'],
        commandScope: { kind: 'prefix', prefixes: ['go test'] },
      },
      { decision: 'cancel' },
    ])
  })
})

describe('fetch approval facts', () => {
  it('reads the rule a remembered fetch writes, which is what names its host', () => {
    const suggestion = permissionSuggestionFromAction({
      id: 'act-fetch',
      kind: 'web_fetch',
      status: 'pending',
      payloadJson: JSON.stringify({
        url: 'https://example.com/pricing',
        rule_content: 'domain:example.com',
        permission_suggestion: {
          permission_tool_name: 'WebFetch',
          permission_input: 'https://example.com/pricing',
          prefix_rule_content: 'domain:example.com',
          available_decisions: ['accept', 'accept_and_remember', 'cancel'],
        },
      }),
    })

    expect(suggestion?.permissionInput).toBe('https://example.com/pricing')
    expect(suggestion?.prefixRuleContent).toBe('domain:example.com')
    expect(approvalDecisions(suggestion).map((item) => item.decision)).toEqual([
      'accept',
      'accept_and_remember',
      'cancel',
    ])
  })
})

describe('command approval scope copy', () => {
  it('names what the row grants in both languages', () => {
    for (const locale of ['zh', 'en'] as const) {
      setLocale(locale)
      expect(t('chat.approvalScopeThisCommand')).not.toBe('chat.approvalScopeThisCommand')
      expect(t('chat.approvalScopeThisCommand').length).toBeGreaterThan(0)
    }
    setLocale('en')
  })
})
