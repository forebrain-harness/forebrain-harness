import { describe, expect, it, vi, beforeEach } from 'vitest'
import { config, mount } from '@vue/test-utils'
import { h } from 'vue'
import { FileTree, FileTreeFolder } from '@repo/elements/file-tree'
import tip from '@/directives/tip'

import WorkspaceTree from './WorkspaceTree.vue'
import { useWorkspaceTree } from '@/composables/useWorkspaceTree'
import { forebrainApi, type WorkspaceTreeNode } from '@/lib/api'

// The app registers `tip` once (main.ts); mounting here does the same, so an
// element that names itself with it resolves instead of warning.
config.global.directives = { tip }

const scope = { sessionId: 's1' }

function node(name: string, path: string, isDir: boolean): WorkspaceTreeNode {
  return { name, path, isDir }
}

async function flush() {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
}

function mountTree() {
  return mount(WorkspaceTree, { props: { scope } })
}

describe('WorkspaceTree', () => {
  beforeEach(() => {
    useWorkspaceTree().reset()
  })

  // The other half of the component's contract, and the one this tree does not
  // use: with no `expanded` set at all, the tree itself keeps what is open from
  // `defaultExpanded` and its own toggles — the app is not in the loop.
  it('opens and closes on its own when no expanded set is given', async () => {
    const wrapper = mount(FileTree, {
      props: { defaultExpanded: new Set(['docs']) },
      slots: { default: () => h(FileTreeFolder, { path: 'docs', name: 'docs' }, () => h('span', { class: 'leaf' }, 'a.md')) },
    })
    const leaf = () => wrapper.find('.leaf')
    expect(leaf().exists()).toBe(true)

    await wrapper.find('[role="treeitem"] button').trigger('click')
    await flush()
    expect(leaf().exists()).toBe(false)

    await wrapper.find('[role="treeitem"] button').trigger('click')
    await flush()
    expect(leaf().exists()).toBe(true)
  })

  it('renders the root level after loading', async () => {
    const children = [node('docs', 'docs', true), node('readme.md', 'readme.md', false)]
    vi.spyOn(forebrainApi, 'workspaceTree').mockResolvedValue({ path: '', records: children })
    const wrapper = mountTree()
    await flush()
    expect(wrapper.text()).toContain('docs')
    expect(wrapper.text()).toContain('readme.md')
    // The tree reads the conversation's file view, not the agent workspace.
    expect(forebrainApi.workspaceTree).toHaveBeenCalledWith('', scope)
  })

  it('expands a directory on click and shows its children', async () => {
    const mock = vi.fn(async (path = '') => (
      path === ''
        ? { path, records: [node('docs', 'docs', true)] }
        : { path, records: [node('a.md', 'docs/a.md', false)] }
    ))
    vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)
    const wrapper = mountTree()
    await flush()

    await wrapper.find('[role="treeitem"] button').trigger('click')
    await flush()
    expect(mock).toHaveBeenCalledWith('docs', scope)
    expect(wrapper.text()).toContain('a.md')
  })

  it('emits insert-ref with the full path when a file is clicked', async () => {
    vi.spyOn(forebrainApi, 'workspaceTree').mockResolvedValue({ path: '', records: [node('top.txt', 'top.txt', false)] })
    const wrapper = mountTree()
    await flush()
    // A file row is the library's file tree item: the whole row is the target,
    // not a button inside it.
    await wrapper.find('[role="treeitem"]').trigger('click')
    expect(wrapper.emitted('insert-ref')).toEqual([['top.txt']])
  })

  it('shows the server error and a retry when a directory fails to load', async () => {
    const mock = vi.fn(async (path = '') => {
      if (path === '') return { path, records: [node('docs', 'docs', true)] }
      throw new Error('no such file or directory')
    })
    vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)
    const wrapper = mountTree()
    await flush()
    await wrapper.find('[role="treeitem"] button').trigger('click')
    await flush()
    expect(wrapper.text()).toContain('no such file or directory')
    expect(wrapper.text()).toMatch(/重试|Retry/)
  })

  // One owner of what is open. The tree keeps its expand state in the app (the
  // same set the keyboard toggles and the same set that decides which children
  // exist), so a fold the mouse has used cannot drift away from the rows: with a
  // copy on the library side, a directory the mouse had opened and closed could
  // no longer be opened with →, because the copy still said "closed" while the
  // rows were already built.
  it('keeps mouse and keyboard on one expand state', async () => {
    const mock = vi.fn(async (path = '') => (
      path === ''
        ? { path, records: [node('docs', 'docs', true)] }
        : { path, records: [node('a.md', 'docs/a.md', false)] }
    ))
    vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)
    const wrapper = mountTree()
    await flush()
    const row = () => wrapper.find('[role="treeitem"] button')
    const item = () => wrapper.find('[role="treeitem"]')

    // Opened and closed with the mouse once.
    await row().trigger('click')
    await flush()
    expect(wrapper.text()).toContain('a.md')
    await row().trigger('click')
    await flush()
    expect(wrapper.text()).not.toContain('a.md')

    // The keyboard opens the same fold, and the child is really on screen — not
    // merely announced by aria-expanded.
    await row().trigger('keydown', { key: 'ArrowRight' })
    await flush()
    expect(item().attributes('aria-expanded')).toBe('true')
    expect(wrapper.text()).toContain('a.md')

    await row().trigger('keydown', { key: 'ArrowLeft' })
    await flush()
    expect(item().attributes('aria-expanded')).toBe('false')
    expect(wrapper.text()).not.toContain('a.md')
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
      const wrapper = mountTree()
      await flush()
      // The keyboard alone, with no mouse click before it.
      const row = wrapper.find('[role="treeitem"] button')
      await row.trigger('keydown', { key: 'ArrowRight' })
      await flush()
      expect(wrapper.find('[role="treeitem"]').attributes('aria-expanded')).toBe('true')
      expect(wrapper.text()).toContain('a.md')
      await row.trigger('keydown', { key: 'ArrowLeft' })
      await flush()
      expect(wrapper.find('[role="treeitem"]').attributes('aria-expanded')).toBe('false')
      expect(wrapper.text()).not.toContain('a.md')
    } finally {
      errSpy.mockRestore()
      warnSpy.mockRestore()
    }
  })
})
