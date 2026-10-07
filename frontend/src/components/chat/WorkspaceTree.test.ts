import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'

import WorkspaceTree from './WorkspaceTree.vue'
import { useWorkspaceTree } from '@/composables/useWorkspaceTree'
import { forebrainApi, type WorkspaceTreeNode } from '@/lib/api'

function node(name: string, path: string, isDir: boolean): WorkspaceTreeNode {
  return { name, path, isDir }
}

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

describe('WorkspaceTree', () => {
  beforeEach(() => {
    useWorkspaceTree().reset()
  })

  it('renders the root level after loading', async () => {
    const children = [node('docs', 'docs', true), node('readme.md', 'readme.md', false)]
    vi.spyOn(forebrainApi, 'workspaceTree').mockResolvedValue({ path: '', records: children })
    const wrapper = mount(WorkspaceTree)
    await flush()
    expect(wrapper.text()).toContain('docs')
    expect(wrapper.text()).toContain('readme.md')
    expect(forebrainApi.workspaceTree).toHaveBeenCalledWith('')
  })

  it('expands a directory on click and shows its children', async () => {
    const mock = vi.fn(async (path = '') => (
      path === ''
        ? { path, records: [node('docs', 'docs', true)] }
        : { path, records: [node('a.md', 'docs/a.md', false)] }
    ))
    vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)
    const wrapper = mount(WorkspaceTree)
    await flush()

    await wrapper.find('[role="treeitem"] button').trigger('click')
    await flush()
    expect(mock).toHaveBeenCalledWith('docs')
    expect(wrapper.text()).toContain('a.md')
  })

  it('emits insert-ref with the full path when a file is clicked', async () => {
    vi.spyOn(forebrainApi, 'workspaceTree').mockResolvedValue({ path: '', records: [node('top.txt', 'top.txt', false)] })
    const wrapper = mount(WorkspaceTree)
    await flush()
    await wrapper.find('[role="treeitem"] button').trigger('click')
    expect(wrapper.emitted('insert-ref')).toEqual([['top.txt']])
  })

  it('shows the server error and a retry when a directory fails to load', async () => {
    const mock = vi.fn(async (path = '') => {
      if (path === '') return { path, records: [node('docs', 'docs', true)] }
      throw new Error('no such file or directory')
    })
    vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)
    const wrapper = mount(WorkspaceTree)
    await flush()
    await wrapper.find('[role="treeitem"] button').trigger('click')
    await flush()
    expect(wrapper.text()).toContain('no such file or directory')
    expect(wrapper.text()).toMatch(/重试|Retry/)
  })

  it('opens and closes with arrow keys', async () => {
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {})
    try {
      const mock = vi.fn(async (path = '') => (
        path === ''
          ? { path, records: [node('docs', 'docs', true)] }
          : { path, records: [node('a.md', 'docs/a.md', false)] }
      ))
      vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)
      const wrapper = mount(WorkspaceTree)
      await flush()
      const row = wrapper.find('[role="treeitem"] button')
      await row.trigger('keydown', { key: 'ArrowRight' })
      await flush()
      expect(wrapper.find('[role="treeitem"]').attributes('aria-expanded')).toBe('true')
      await row.trigger('keydown', { key: 'ArrowLeft' })
      await flush()
      expect(wrapper.find('[role="treeitem"]').attributes('aria-expanded')).toBe('false')
    } finally {
      errSpy.mockRestore()
      warnSpy.mockRestore()
    }
  })
})
