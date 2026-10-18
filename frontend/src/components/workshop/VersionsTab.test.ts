import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import VersionsTab from './VersionsTab.vue'
import { forebrainApi, type SkillVersionState } from '@/lib/api'
import { setLocale } from '@/locales'

// The app registers `tip` once (main.ts); mounting here does the same, so an
// element that names itself with it resolves instead of warning.
config.global.directives = { tip }

setLocale('zh')

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

function state(count: number): SkillVersionState {
  return {
    name: 'gateway-ops',
    current: count,
    hasDraft: false,
    versions: Array.from({ length: count }, (_, i) => ({ v: count - i, at: '2026-10-01T00:00:00Z', kind: 'published' })),
  }
}

describe('VersionsTab', () => {
  beforeEach(() => {
    vi.spyOn(forebrainApi, 'skillVersions').mockResolvedValue({ name: 'gateway-ops', rootPath: '/s/gateway-ops', state: state(25) })
  })
  afterEach(() => vi.restoreAllMocks())

  it('pages versions twenty at a time', async () => {
    const wrapper = mount(VersionsTab, { props: { skillName: 'gateway-ops', editable: true }, global: { stubs: { Teleport: true } } })
    await flush()
    expect(wrapper.findAll('[data-testid^="workshop-version-row-"]').length).toBe(20)
    await wrapper.find('[data-testid="workshop-version-more"]').trigger('click')
    expect(wrapper.findAll('[data-testid^="workshop-version-row-"]').length).toBe(25)
    expect(wrapper.find('[data-testid="workshop-version-more"]').exists()).toBe(false)
  })
})
