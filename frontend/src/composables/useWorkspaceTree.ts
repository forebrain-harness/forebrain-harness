import { computed, reactive, unref, watch, type MaybeRef } from 'vue'
import { getErrorMessage, forebrainApi, type WorkspaceScope, type WorkspaceTreeNode } from '@/lib/api'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'

export interface WorkspaceDirState {
  status: 'loading' | 'loaded' | 'error'
  children: WorkspaceTreeNode[]
  error: string
}

/** One file view's directories and expansions, keyed by its root's identity. */
interface WorkspaceTreeState {
  dirs: Map<string, WorkspaceDirState>
  expanded: Set<string>
}

/**
 * The workspace's directory tree, one level per request: directories load
 * when they are expanded and never reload until asked. The state is module
 * level so closing and reopening the workbench keeps both the loaded
 * directories and the expanded set. It is kept per file view — one entry per
 * conversation (`s:<session id>`) or, before a conversation exists, per the
 * project its composer chose (`p:<project id>`) — because every conversation
 * reads its own project's root. It belongs to the active primary agent: a
 * tenant switch resets it.
 */
const trees = reactive(new Map<string, WorkspaceTreeState>())
// Fallbacks for a view whose tree has not been created yet. They are never
// mutated: every mutator creates the view's tree first.
const emptyDirs: Map<string, WorkspaceDirState> = new Map()
const emptyExpanded: Set<string> = new Set()

/** The identity a file view's root is keyed by: its session, else its project. */
function scopeKey(scope: WorkspaceScope): string {
  const sessionId = String(scope.sessionId ?? '').trim()
  if (sessionId) return `s:${sessionId}`
  const projectId = String(scope.projectId ?? '').trim()
  if (projectId) return `p:${projectId}`
  return ''
}

function ensureTree(key: string): WorkspaceTreeState {
  if (!trees.has(key)) trees.set(key, { dirs: new Map(), expanded: new Set() })
  // Return the reactive proxy the Map hands out, not the raw object that was
  // stored: mutating the raw one would not notify the tree's computeds.
  return trees.get(key) as WorkspaceTreeState
}

function entry(key: string, path: string): WorkspaceDirState {
  const tree = ensureTree(key)
  const existing = tree.dirs.get(path)
  if (existing) return existing
  tree.dirs.set(path, { status: 'loading', children: [], error: '' })
  return tree.dirs.get(path) as WorkspaceDirState
}

async function load(key: string, scope: WorkspaceScope, path: string): Promise<void> {
  const state = entry(key, path)
  state.status = 'loading'
  state.error = ''
  try {
    const res = await forebrainApi.workspaceTree(path, scope)
    state.children = Array.isArray(res.records) ? res.records : []
    state.status = 'loaded'
  } catch (cause) {
    state.children = []
    state.error = getErrorMessage(cause)
    state.status = 'error'
  }
}

export function useWorkspaceTree(scope: MaybeRef<WorkspaceScope> = {}) {
  const key = computed(() => scopeKey(unref(scope)))
  const dirs = computed(() => trees.get(key.value)?.dirs ?? emptyDirs)
  const expanded = computed(() => trees.get(key.value)?.expanded ?? emptyExpanded)

  function ensureRoot(): void {
    if (!key.value) return
    const state = dirs.value.get('')
    if (!state || state.status === 'error') void load(key.value, unref(scope), '')
  }

  function toggle(path: string): void {
    if (!key.value) return
    ensureTree(key.value)
    const open = expanded.value
    if (open.has(path)) {
      open.delete(path)
      return
    }
    open.add(path)
    const state = dirs.value.get(path)
    if (!state || state.status === 'error') void load(key.value, unref(scope), path)
  }

  async function refresh(): Promise<void> {
    if (!key.value || !dirs.value.get('')) return
    const open = expanded.value
    const targets = ['', ...open]
    await Promise.all(targets.map((path) => load(key.value, unref(scope), path).then(() => {
      if (open.has(path) && dirs.value.get(path)?.status === 'error') {
        // A directory that no longer exists stops being expanded; the rest
        // of the tree keeps its shape.
        open.delete(path)
      }
    })))
  }

  function reset(): void {
    trees.clear()
  }

  function loadPath(path: string): void {
    if (!key.value) return
    void load(key.value, unref(scope), path)
  }

  return { dirs, expanded, ensureRoot, toggle, refresh, reset, load: loadPath }
}

/**
 * Carries one root's loaded tree over to another identity. A brand-new
 * conversation's file view is loaded under the project its pill named; once
 * the first send gives it a session id, the tree it already showed becomes
 * the conversation's own, so it does not flash empty.
 */
export function migrateWorkspaceTree(from: WorkspaceScope, to: WorkspaceScope): void {
  const fromKey = scopeKey(from)
  const toKey = scopeKey(to)
  if (!fromKey || !toKey || fromKey === toKey) return
  const tree = trees.get(fromKey)
  if (!tree) return
  trees.set(toKey, tree)
  trees.delete(fromKey)
}

// The tree belongs to the tenant: a primary agent switch throws it away.
const { refreshToken } = usePrimaryAgents()
watch(refreshToken, () => {
  trees.clear()
})
