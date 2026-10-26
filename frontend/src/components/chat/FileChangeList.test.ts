import { afterEach, beforeAll, describe, expect, it } from 'vitest'
import { config, mount } from '@vue/test-utils'
import tip from '@/directives/tip'

import { setLocale } from '@/locales'
import FileChangeList from './FileChangeList.vue'

config.global.directives = { tip }

function filesOf(overrides: Record<string, unknown> = {}) {
  return [{
    verb: 'Updated', path: 'pkg/gateway/api_extra.go', added: 3, deleted: 2,
    unifiedDiff: '@@ -1,2 +1,2 @@\n-a\n+b',
    ...overrides,
  }]
}

describe('FileChangeList', () => {
  beforeAll(() => setLocale('zh'))
  afterEach(() => setLocale('zh'))

  it('tells created, updated and deleted files apart by their colour block icon', () => {
    const created = mount(FileChangeList, { props: { files: [{ verb: 'Created', path: 'new.go', added: 4, deleted: 0 }] } })
    expect(created.get('.fc-ic').classes()).toContain('add')
    expect(created.get('.fc-ic').attributes('aria-label')).toBe('新建')

    const deleted = mount(FileChangeList, { props: { files: [{ verb: 'Deleted', path: 'gone.go', added: 0, deleted: 9 }] } })
    expect(deleted.get('.fc-ic').classes()).toContain('del')
    expect(deleted.get('.fc-ic').attributes('aria-label')).toBe('删除')

    const updated = mount(FileChangeList, { props: { files: filesOf() } })
    expect(updated.get('.fc-ic').classes()).toContain('upd')
    expect(updated.get('.fc-ic').attributes('aria-label')).toBe('修改')
  })

  it('splits a path into its grey directory half and its dark file name, and counts +N −M', () => {
    const wrapper = mount(FileChangeList, { props: { files: filesOf() } })
    expect(wrapper.get('.fc-dir').text()).toBe('pkg/gateway/')
    expect(wrapper.get('.fc-fn').text()).toBe('api_extra.go')
    expect(wrapper.get('.fc-n').text()).toBe('+3−2')
    // A root-level file has no directory half at all.
    const root = mount(FileChangeList, { props: { files: [{ verb: 'Updated', path: 'Makefile', added: 1, deleted: 0 }] } })
    expect(root.find('.fc-dir').exists()).toBe(false)
    expect(root.get('.fc-fn').text()).toBe('Makefile')
  })

  it('draws the diff under its row, plain for a created file', () => {
    const updated = mount(FileChangeList, { props: { files: filesOf() } })
    expect(updated.find('[data-testid="diff-block"]').exists()).toBe(true)
    expect(updated.findAll('.dr.add').length + updated.findAll('.dr.del').length).toBe(2)

    const created = mount(FileChangeList, {
      props: { files: [{ verb: 'Created', path: 'new.go', added: 1, deleted: 0, unifiedDiff: '@@ -0,0 +1 @@\n+body' }] },
    })
    expect(created.findAll('.dr.add')).toHaveLength(0)
    expect(created.text()).toContain('body')
  })

  it('translates the engine’s notes to Chinese, and leaves an unknown note as it came', () => {
    const wrapper = mount(FileChangeList, {
      props: {
        files: [],
        notes: [
          '… 7 more files changed',
          '(file diff skipped for this git command)',
          '(another command ran in this repository at the same time; a change made by either may show under either result)',
          '(a git step in this command can move the working tree, so these may be its changes, not edits)',
          '3 files changed (binary, mode only or too large to show) (part of the diff is unavailable)',
          '(file diff unavailable for this command; 2 files changed)',
          '(some future note the engine may grow)',
        ],
      },
    })
    const notes = wrapper.findAll('[data-testid^="diff-note-"]').map((note) => note.text())
    expect(notes).toEqual([
      '还有 7 个文件改动',
      'git 切换分支类命令，不比较文件',
      '同一仓库同时有别的命令在跑，改动可能记在任一条下',
      '命令里有 git 步骤，可能是它移动了工作区',
      '3 个文件改动（二进制、仅权限或太大，不显示）（部分差异不可用）',
      '这条命令的文件差异不可用（2 个文件改动）',
      '(some future note the engine may grow)',
    ])
  })

  it('keeps the engine’s own wording in English', () => {
    setLocale('en')
    const wrapper = mount(FileChangeList, {
      props: { files: [], notes: ['… 7 more files changed (part of the diff is unavailable)'] },
    })
    expect(wrapper.get('[data-testid="diff-note-0"]').text()).toBe('… 7 more files changed (part of the diff is unavailable)')
    setLocale('zh')
  })
})
