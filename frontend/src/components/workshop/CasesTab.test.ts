import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'

import CaseEditDialog from './CaseEditDialog.vue'
import CasesTab from './CasesTab.vue'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'

setLocale('zh')

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

function mountTab(editable = true): VueWrapper {
  return mount(CasesTab, {
    props: { skillName: 'gateway-ops', editable },
    global: { stubs: { Teleport: true } },
  })
}

describe('CasesTab', () => {
  beforeEach(() => {
    vi.spyOn(forebrainApi, 'skillFileRead').mockRejectedValue(new Error('missing'))
    vi.spyOn(forebrainApi, 'skillFileWrite').mockResolvedValue({ ok: true, path: 'evals/evals.json' })
  })
  afterEach(() => vi.restoreAllMocks())

  it('adds a case by writing evals.json', async () => {
    const wrapper = mountTab()
    await flush()
    await wrapper.findComponent(CaseEditDialog).vm.$emit('save', { question: 'gateway 502', expected: 'health 200' })
    await flush()
    const write = vi.mocked(forebrainApi.skillFileWrite)
    expect(write).toHaveBeenCalledWith('gateway-ops', 'evals/evals.json', expect.stringContaining('gateway 502'))
    expect(write.mock.calls[0][2]).toContain('health 200')
  })

  it('disables editing when the skill is read-only', async () => {
    const wrapper = mountTab(false)
    await flush()
    expect(wrapper.find('[data-testid="workshop-case-add"]').attributes('disabled')).toBeDefined()
  })
})
