import { describe, expect, it, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'

import ChangedFilesTab from './ChangedFilesTab.vue'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'

setLocale('zh')

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

describe('ChangedFilesTab', () => {
  afterEach(() => vi.restoreAllMocks())

  it('offers compare/edit/revert on a changed file and no compare on a new one', async () => {
    vi.spyOn(forebrainApi, 'sessionFileChanges').mockResolvedValue([
      { path: 'pkg/a.go', added: 4, removed: 2, created: false },
      { path: 'pkg/b.go', added: 9, removed: 0, created: true },
    ])
    const wrapper = mount(ChangedFilesTab, {
      props: { scope: { sessionId: 's1' } },
      global: { stubs: { teleport: true } },
    })
    await flush()

    const changed = wrapper.find('[data-path="pkg/a.go"]')
    expect(changed.find('[data-testid="chg-diff-pkg/a.go"]').exists()).toBe(true)
    expect(changed.find('[data-testid="chg-edit-pkg/a.go"]').exists()).toBe(true)
    expect(changed.find('[data-testid="chg-revert-pkg/a.go"]').exists()).toBe(true)
    expect(changed.text()).toContain('+4')

    const created = wrapper.find('[data-path="pkg/b.go"]')
    expect(created.find('[data-testid="chg-diff-pkg/b.go"]').exists()).toBe(false)
    expect(created.find('[data-testid="chg-edit-pkg/b.go"]').exists()).toBe(true)
    expect(created.find('[data-testid="chg-revert-pkg/b.go"]').exists()).toBe(true)
    expect(created.text()).toContain('新增')
    expect(wrapper.text()).toContain('本会话用文件工具改过的文件。')
  })
})
