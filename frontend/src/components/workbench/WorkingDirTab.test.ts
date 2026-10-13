import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'

import WorkingDirTab from './WorkingDirTab.vue'
import { forebrainApi } from '@/lib/api'
import { setLocale } from '@/locales'

setLocale('zh')
import { useWorkspaceTree } from '@/composables/useWorkspaceTree'

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

function stubTree() {
  vi.spyOn(forebrainApi, 'workspaceTree').mockResolvedValue({
    path: '',
    records: [
      { name: 'notes', path: 'notes', isDir: true },
      { name: 'a.md', path: 'a.md', isDir: false },
    ],
  })
  vi.spyOn(forebrainApi, 'workspaceWrite').mockResolvedValue({ ok: true, path: '' })
  vi.spyOn(forebrainApi, 'workspaceRename').mockResolvedValue({ ok: true, path: '' })
  vi.spyOn(forebrainApi, 'workspaceDelete').mockResolvedValue({ ok: true })
  vi.spyOn(forebrainApi, 'workspaceFile').mockResolvedValue({ path: 'x', content: '' })
}

async function mountTab() {
  const wrapper = mount(WorkingDirTab, {
    props: { scope: { sessionId: 's1' }, rootPath: '/tmp/proj' },
    global: { stubs: { teleport: true } },
  })
  await flush()
  return wrapper
}

describe('WorkingDirTab', () => {
  beforeEach(() => {
    useWorkspaceTree().reset()
    stubTree()
  })
  afterEach(() => vi.restoreAllMocks())

  it('gives a file two actions and a directory three', async () => {
    const wrapper = await mountTab()
    expect(wrapper.find('[data-path="notes"] [data-testid="tree-node-new"]').exists()).toBe(true)
    expect(wrapper.find('[data-path="notes"] [data-testid="tree-node-edit"]').exists()).toBe(true)
    expect(wrapper.find('[data-path="notes"] [data-testid="tree-node-delete"]').exists()).toBe(true)
    expect(wrapper.find('[data-path="a.md"] [data-testid="tree-node-new"]').exists()).toBe(false)
    expect(wrapper.find('[data-path="a.md"] [data-testid="tree-node-edit"]').exists()).toBe(true)
    expect(wrapper.find('[data-path="a.md"] [data-testid="tree-node-delete"]').exists()).toBe(true)
  })

  it('creates a file from the head button then opens the editor', async () => {
    const wrapper = await mountTab()
    await wrapper.find('[data-testid="tree-new-root"]').trigger('click')
    await flush()
    expect(wrapper.text()).toContain('新建文件')
    await wrapper.find('[data-testid="tree-new-name"]').setValue('docs/note.md')
    await wrapper.find('[data-testid="tree-new-create"]').trigger('click')
    await flush()
    expect(forebrainApi.workspaceWrite).toHaveBeenCalledWith(
      { sessionId: 's1' },
      { path: 'docs/note.md', content: '', create: true },
    )
    // The edit dialog opens on the file just created.
    expect(wrapper.text()).toContain('编辑文件')
  })

  it('asks before deleting a directory and names what goes with it', async () => {
    const wrapper = await mountTab()
    await wrapper.find('[data-path="notes"] [data-testid="tree-node-delete"]').trigger('click')
    await flush()
    expect(wrapper.text()).toContain('删除目录 notes？')
    expect(wrapper.text()).toContain('notes/')
    expect(wrapper.text()).toContain('目录里的全部文件和子目录会一起删除，不能恢复。')
  })
})
