import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import tip from '@/directives/tip'
import { config } from '@vue/test-utils'
import { setLocale } from '@/locales'
import SubagentConversation from './SubagentConversation.vue'
import type { SubagentTranscript } from '@/composables/useChatStream'

config.global.directives = { tip }

vi.mock('@/composables/providePendingActions', () => ({
  injectPendingActions: () => null,
}))

describe('scratch', () => {
  it('reviewer tool card title', () => {
    setLocale('zh')
    const record = {
      agentId: 'plan-review-e2e-1',
      agentType: 'plan-reviewer',
      task: 'review the plan against the README',
      status: 'running',
      blocks: [
        { kind: 'prompt', text: 'review the plan against the README' },
        {
          kind: 'tool',
          step: {
            stepId: 'call-e2e-review-read',
            toolName: 'read',
            summary: 'reading the README',
            status: 'running',
          },
        },
      ],
    } as unknown as SubagentTranscript
    const wrapper = mount(SubagentConversation, { props: { record } })
    console.log('HEAD:', wrapper.find('[data-slot="collapsible-trigger"]').text())
    console.log('TEXT:', wrapper.text().slice(0, 200))
    expect(wrapper.text()).toContain('reading the README')
  })
})
