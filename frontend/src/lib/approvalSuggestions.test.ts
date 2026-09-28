import { describe, expect, it } from 'vitest'
import { setLocale, t } from '@/locales'
import { approvalDecisions, approvalJustification, permissionSuggestionFromAction } from './approvalSuggestions'

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

  it('normalizes snake-case network decisions embedded in payload JSON', () => {
    const suggestion = permissionSuggestionFromAction({
      id: 'act-2',
      kind: 'shell',
      status: 'pending',
      payloadJson: JSON.stringify({
        permission_suggestion: {
          permission_tool_name: 'Bash',
          available_decisions: [
            'accept_for_session',
            {
              apply_network_policy_amendment: {
                host: 'Example.COM.',
                action: 'allow',
              },
            },
          ],
          network_approval_context: { host: 'Example.COM.', protocol: 'https' },
          network_port: 443,
          one_shot_only: false,
        },
      }),
    })

    expect(suggestion?.networkApprovalContext).toEqual({ host: 'example.com', protocol: 'https' })
    expect(suggestion?.networkPort).toBe(443)
    expect(approvalDecisions(suggestion)).toEqual([
      { decision: 'accept_for_session' },
      {
        decision: 'apply_network_policy_amendment',
        networkPolicyAmendment: { host: 'example.com', action: 'allow' },
      },
    ])
  })

  it('keeps request_permissions choices supplied by the server', () => {
    const decisions = approvalDecisions({
      permissionToolName: 'request_permissions',
      availableDecisions: [
        'grant_for_turn',
        'grant_for_turn_with_strict_auto_review',
        'grant_for_session',
        'decline',
      ],
    })

    expect(decisions.map((item) => item.decision)).toEqual([
      'grant_for_turn',
      'grant_for_turn_with_strict_auto_review',
      'grant_for_session',
      'decline',
    ])
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

describe('command approval scope copy', () => {
  it('names what the row grants in both languages', () => {
    for (const locale of ['zh', 'en'] as const) {
      setLocale(locale)
      expect(t('chat.approveCommandPrefix', { prefix: 'go test' })).toContain('go test')
      expect(t('chat.approveCommandVariants', { prefixes: 'mvn clean' })).toContain('mvn clean')
      for (const key of ['chat.approveCommandOnly', 'chat.approveCommandAnyVariant'] as const) {
        expect(t(key)).not.toBe(key)
        expect(t(key).length).toBeGreaterThan(0)
      }
    }
    setLocale('en')
  })
})

describe('approval justification', () => {
  it('renders the reason for any approval that carries one, not only a protected path', () => {
    const protectedPath = {
      approvalReason: 'protected_path',
      justification: 'Writing /home/u/.forebrain/skills/release/SKILL.md changes the "release" skill.',
    }
    const planMode = {
      approvalReason: 'plan_mode_unproven_command',
      justification: 'Plan mode is active: this command could not be proven read-only, so it will run only if you allow it.',
    }
    expect(approvalJustification(protectedPath)).toBe(protectedPath.justification)
    expect(approvalJustification(planMode)).toBe(planMode.justification)
  })

  it('shows nothing when the payload carries no reason', () => {
    expect(approvalJustification(null)).toBe('')
    expect(approvalJustification({ approvalReason: 'dangerous_shell_syntax' })).toBe('')
    expect(approvalJustification({ justification: '   ' })).toBe('')
  })
})
