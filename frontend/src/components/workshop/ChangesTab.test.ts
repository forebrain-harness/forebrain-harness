import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import ChangesTab from './ChangesTab.vue'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'

config.global.directives = { tip }
setLocale('zh')

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

describe('ChangesTab', () => {
  beforeEach(() => {
    vi.spyOn(forebrainApi, 'skillVersions').mockResolvedValue({
      state: { draftBase: 1, hasDraft: true, draftDir: '/d' },
    } as Awaited<ReturnType<typeof forebrainApi.skillVersions>>)
    vi.spyOn(forebrainApi, 'skillDiff').mockResolvedValue({
      name: 'gateway-ops',
      from: 'v1',
      to: 'draft',
      files: [
        { path: 'SKILL.md', status: 'modified', added: 3, removed: 2 },
        { path: 'new.txt', status: 'added', added: 1, removed: 0 },
      ],
    })
  })
  afterEach(() => vi.restoreAllMocks())

  it('offers compare for changed files only — a new file has no base to diff against', async () => {
    const wrapper = mount(ChangesTab, { props: { skillName: 'gateway-ops', editable: true } })
    await flush()
    const buttons = wrapper.findAll('[data-testid="workshop-change-compare"]')
    expect(buttons).toHaveLength(1)
    // The one button sits in SKILL.md's row; new.txt (added) gets none.
    expect(buttons[0]!.element.closest('div')?.textContent).toContain('SKILL.md')
  })

  it('names the file it wants compared', async () => {
    const wrapper = mount(ChangesTab, { props: { skillName: 'gateway-ops', editable: true } })
    await flush()
    await wrapper.find('[data-testid="workshop-change-compare"]').trigger('click')
    expect(wrapper.emitted('compare')![0]![0]).toEqual({ from: 'v1', to: 'draft', path: 'SKILL.md' })
  })
})
