import { reactive, watch } from 'vue'
import { getErrorMessage, forebrainApi, type WorkspaceTreeNode } from '@/lib/api'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'

export interface WorkspaceDirState {
  status: 'loading' | 'loaded' | 'error'
  children: WorkspaceTreeNode[]
  error: string
}

/**
 * The workspace's directory tree, one level per request: directories load
 * when they are expanded and never reload until asked. The state is module
 * level so closing and reopening the workbench keeps both the loaded
 * directories and the expanded set, and belongs to the active primary agent —
 * a tenant switch resets it.
 */
const dirs = reactive(new Map<string, WorkspaceDirState>())
const expanded = reactive(new Set<string>())

function entry(path: string): WorkspaceDirState {
  const existing = dirs.get(path)
  if (existing) return existing
  dirs.set(path, { status: 'loading', children: [], error: '' })
  // Return the reactive proxy the Map hands out, not the raw object that was
  // stored: mutating the raw one would not notify the tree's computeds.
  return dirs.get(path) as WorkspaceDirState
}

async function load(path: string): Promise<void> {
  const state = entry(path)
  state.status = 'loading'
  state.error = ''
  try {
    const res = await forebrainApi.workspaceTree(path)
    state.children = Array.isArray(res.records) ? res.records : []
    state.status = 'loaded'
  } catch (cause) {
    state.children = []
    state.error = getErrorMessage(cause)
    state.status = 'error'
  }
}

export function useWorkspaceTree() {
  function ensureRoot(): void {
    const state = dirs.get('')
    if (!state || state.status === 'error') void load('')
  }

  function toggle(path: string): void {
    if (expanded.has(path)) {
      expanded.delete(path)
      return
    }
    expanded.add(path)
    const state = dirs.get(path)
    if (!state || state.status === 'error') void load(path)
  }

  async function refresh(): Promise<void> {
    if (!dirs.get('')) return
    const targets = ['', ...expanded]
    await Promise.all(targets.map((path) => load(path).then(() => {
      if (expanded.has(path) && dirs.get(path)?.status === 'error') {
        // A directory that no longer exists stops being expanded; the rest
        // of the tree keeps its shape.
        expanded.delete(path)
      }
    })))
  }

  function reset(): void {
    dirs.clear()
    expanded.clear()
  }

  return { dirs, expanded, ensureRoot, toggle, refresh, reset, load }
}

// The tree belongs to the tenant: a primary agent switch throws it away.
const { refreshToken } = usePrimaryAgents()
watch(refreshToken, () => {
  dirs.clear()
  expanded.clear()
})
