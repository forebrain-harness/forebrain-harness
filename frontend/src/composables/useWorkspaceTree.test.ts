import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount } from '@vue/test-utils'

import { useWorkspaceTree } from './useWorkspaceTree'
import { forebrainApi } from '@/lib/api'

// The tree is module state; each case starts from a reset.
function fresh() {
  const tree = useWorkspaceTree()
  tree.reset()
  return tree
}

function node(name: string, path: string, isDir: boolean) {
  return { name, path, isDir }
}

describe('useWorkspaceTree', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    fresh()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    useWorkspaceTree().reset()
  })

  it('ensureRoot requests the root once', async () => {
    const tree = fresh()
    const mock = vi.fn().mockResolvedValue({ path: '', records: [node('docs', 'docs', true)] })
    vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)

    tree.ensureRoot()
    tree.ensureRoot()
    await Promise.resolve()
    await Promise.resolve()
    expect(mock).toHaveBeenCalledTimes(1)
    expect(mock).toHaveBeenCalledWith('')
    expect(tree.dirs.get('')?.children).toEqual([node('docs', 'docs', true)])
  })

  it('toggle loads a directory once, then only flips expansion', async () => {
    const tree = fresh()
    const mock = vi.fn(async (path = '') => ({ path, records: path === 'docs' ? [node('a.md', 'docs/a.md', false)] : [] }))
    vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)

    tree.toggle('docs')
    await Promise.resolve()
    await Promise.resolve()
    tree.toggle('docs')
    tree.toggle('docs')
    expect(mock).toHaveBeenCalledTimes(1)
    expect(mock).toHaveBeenCalledWith('docs')
    expect(tree.expanded.has('docs')).toBe(true)
  })

  it('refresh reloads the root and every expanded directory', async () => {
    const tree = fresh()
    const mock = vi.fn(async (path = '') => ({ path, records: [] }))
    vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)

    tree.ensureRoot()
    await Promise.resolve()
    await Promise.resolve()
    tree.toggle('docs')
    tree.toggle('src')
    await Promise.resolve()
    await Promise.resolve()
    mock.mockClear()

    await tree.refresh()
    const paths = mock.mock.calls.map((call) => call[0]).sort()
    expect(paths).toEqual(['', 'docs', 'src'])
  })

  it('refresh drops an expanded directory that now 404s', async () => {
    const tree = fresh()
    const mock = vi.fn(async (path = '') => ({ path, records: [] }))
    vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)

    tree.ensureRoot()
    await Promise.resolve()
    await Promise.resolve()
    tree.toggle('gone')
    await Promise.resolve()
    await Promise.resolve()
    expect(tree.expanded.has('gone')).toBe(true)

    mock.mockImplementation((path = '') => (path === 'gone'
      ? Promise.reject(new Error('no such file or directory'))
      : Promise.resolve({ path, records: [] })))
    await tree.refresh()
    expect(tree.expanded.has('gone')).toBe(false)
    expect(tree.dirs.get('gone')?.status).toBe('error')
  })

  it('reset clears everything', async () => {
    const tree = fresh()
    vi.spyOn(forebrainApi, 'workspaceTree').mockResolvedValue({ path: '', records: [node('docs', 'docs', true)] })
    tree.ensureRoot()
    await Promise.resolve()
    await Promise.resolve()
    tree.toggle('docs')
    tree.reset()
    expect(tree.dirs.size).toBe(0)
    expect(tree.expanded.size).toBe(0)
  })
})

enableAutoUnmount(() => {})
