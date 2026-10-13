import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import EvalsTab from './EvalsTab.vue'
import { forebrainApi, type WorkshopEvalsResponse } from '@/lib/api'
import { setLocale } from '@/locales'

setLocale('zh')

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

const evals: WorkshopEvalsResponse = {
  name: 'gateway-ops',
  workspace: '/s/gateway-ops-workspace',
  iterations: [
    {
      name: 'iteration-1',
      order: 1,
      modified: 0,
      cases: [
        {
          id: 'eval-0',
          name: 'outage',
          prompt: 'gateway 502',
          expected: 'health 200',
          runId: 'eval-0-with_skill',
          feedback: '',
          configs: {
            with_skill: { present: true, passed: 1, total: 1, passRate: 1, output: 'ok', timeSeconds: 0 },
            without_skill: { present: true, passed: 0, total: 1, passRate: 0, output: 'no', timeSeconds: 0 },
          },
        },
      ],
    },
  ],
}

describe('EvalsTab', () => {
  beforeEach(() => {
    vi.spyOn(forebrainApi, 'skillWorkshopEvals').mockResolvedValue(evals)
  })
  afterEach(() => vi.restoreAllMocks())

  it('renders the round and its case results', async () => {
    const wrapper = mount(EvalsTab, { props: { skillName: 'gateway-ops' }, global: { stubs: { Teleport: true } } })
    await flush()
    expect(wrapper.text()).toContain('使用技能 1/1')
    expect(wrapper.text()).toContain('不用技能 0/1')
  })

  it('sends "run evaluation" directly and only fills for "rework using the feedback"', async () => {
    const wrapper = mount(EvalsTab, { props: { skillName: 'gateway-ops' }, global: { stubs: { Teleport: true } } })
    await flush()
    await wrapper.find('[data-testid="workshop-eval-run"]').trigger('click')
    expect(wrapper.emitted('send')?.[0]?.[0]).toBe('运行评测')
    await wrapper.find('[data-testid="workshop-eval-refix"]').trigger('click')
    expect(wrapper.emitted('fill')?.[0]?.[0]).toContain('按第 1 轮评测的反馈再改一轮')
    expect(wrapper.emitted('send')).toHaveLength(1)
  })
})
