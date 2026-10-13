import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, ref } from 'vue'
import { enableAutoUnmount } from '@vue/test-utils'

import { migrateWorkspaceTree, useWorkspaceTree } from './useWorkspaceTree'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'
import { forebrainApi, type WorkspaceScope, type WorkspaceTreeResponse } from '@/lib/api'

// The tree is module state; each case starts from a reset.
function fresh() {
  const tree = useWorkspaceTree(ref({ sessionId: 's1' }))
  tree.reset()
  return tree
}

function node(name: string, path: string, isDir: boolean) {
  return { name, path, isDir }
}

const flush = async () => {
  await Promise.resolve()
  await Promise.resolve()
}

describe('useWorkspaceTree', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    useWorkspaceTree().reset()
  })

  afterEach(() => {
    vi.restoreAllMocks()
    useWorkspaceTree().reset()
  })

  it('reads each conversation against its own root, and keeps them apart', async () => {
    const mock = vi.fn(async (path = '', scope?: WorkspaceScope): Promise<WorkspaceTreeResponse> => ({
      path,
      records: scope?.sessionId === 'a' ? [node('a.txt', 'a.txt', false)] : [node('b.txt', 'b.txt', false)],
    }))
    vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)

    const a = useWorkspaceTree(ref({ sessionId: 'a' }))
    const b = useWorkspaceTree(ref({ sessionId: 'b' }))
    a.ensureRoot()
    b.ensureRoot()
    await flush()

    expect(mock).toHaveBeenCalledWith('', { sessionId: 'a' })
    expect(mock).toHaveBeenCalledWith('', { sessionId: 'b' })
    // Each conversation's loaded tree is its own: one did not overwrite the other.
    expect(a.dirs.value.get('')?.children).toEqual([node('a.txt', 'a.txt', false)])
    expect(b.dirs.value.get('')?.children).toEqual([node('b.txt', 'b.txt', false)])
  })

  it('reads a not-yet-started conversation against its project', async () => {
    const mock = vi.fn(async (path = '', scope?: WorkspaceScope): Promise<WorkspaceTreeResponse> => {
      expect(scope).toEqual({ projectId: 'p1' })
      return { path, records: [] }
    })
    vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)

    const tree = useWorkspaceTree(ref({ projectId: 'p1' }))
    tree.ensureRoot()
    await flush()

    expect(mock).toHaveBeenCalledWith('', { projectId: 'p1' })
  })

  it('reads nothing when no session or project is named', () => {
    const mock = vi.fn()
    vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)

    const tree = useWorkspaceTree(ref({}))
    tree.ensureRoot()

    expect(mock).not.toHaveBeenCalled()
  })

  it('carries a project tree over to the session it becomes', async () => {
    vi.spyOn(forebrainApi, 'workspaceTree').mockResolvedValue({ path: '', records: [node('docs', 'docs', true)] })

    const fresh = useWorkspaceTree(ref({ projectId: 'p1' }))
    fresh.ensureRoot()
    await flush()
    expect(fresh.dirs.value.get('')?.children).toEqual([node('docs', 'docs', true)])

    migrateWorkspaceTree({ projectId: 'p1' }, { sessionId: 's1' })

    const created = useWorkspaceTree(ref({ sessionId: 's1' }))
    expect(created.dirs.value.get('')?.children).toEqual([node('docs', 'docs', true)])
  })

  it('drops every tree when the primary agent switches', async () => {
    vi.spyOn(forebrainApi, 'workspaceTree').mockResolvedValue({ path: '', records: [node('docs', 'docs', true)] })

    const tree = useWorkspaceTree(ref({ sessionId: 's1' }))
    tree.ensureRoot()
    await flush()
    expect(tree.dirs.value.get('')?.children).toHaveLength(1)

    const { refreshToken } = usePrimaryAgents()
    refreshToken.value += 1
    await nextTick()

    expect(tree.dirs.value.size).toBe(0)
    expect(tree.expanded.value.size).toBe(0)
  })

  it('refresh reloads the root and every expanded directory', async () => {
    const mock = vi.fn(async (path = ''): Promise<WorkspaceTreeResponse> => ({ path, records: [] }))
    vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)

    const tree = useWorkspaceTree(ref({ sessionId: 's1' }))
    tree.ensureRoot()
    await flush()
    tree.toggle('docs')
    tree.toggle('src')
    await flush()
    mock.mockClear()

    await tree.refresh()
    const paths = mock.mock.calls.map((call) => call[0]).sort()
    expect(paths).toEqual(['', 'docs', 'src'])
  })

  it('refresh drops an expanded directory that now 404s', async () => {
    const mock = vi.fn(async (path = ''): Promise<WorkspaceTreeResponse> => ({ path, records: [] }))
    vi.spyOn(forebrainApi, 'workspaceTree').mockImplementation(mock)

    const tree = useWorkspaceTree(ref({ sessionId: 's1' }))
    tree.ensureRoot()
    await flush()
    tree.toggle('gone')
    await flush()
    expect(tree.expanded.value.has('gone')).toBe(true)

    mock.mockImplementation((path = '') => (path === 'gone'
      ? Promise.reject(new Error('no such file or directory'))
      : Promise.resolve({ path, records: [] })))
    await tree.refresh()
    expect(tree.expanded.value.has('gone')).toBe(false)
    expect(tree.dirs.value.get('gone')?.status).toBe('error')
  })

  it('reset clears everything', async () => {
    vi.spyOn(forebrainApi, 'workspaceTree').mockResolvedValue({ path: '', records: [node('docs', 'docs', true)] })
    const tree = fresh()
    tree.ensureRoot()
    await flush()
    tree.toggle('docs')
    tree.reset()
    expect(tree.dirs.value.size).toBe(0)
    expect(tree.expanded.value.size).toBe(0)
  })
})

enableAutoUnmount(() => {})
