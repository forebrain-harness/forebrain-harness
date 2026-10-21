<template>
  <!-- The tree itself is the library's file tree: it owns the tree role, the
       expanded set and the selection, and its folders draw their own nested
       groups. This component supplies the workspace's data — one directory
       level per request — plus the roving tab stop and the arrow keys. -->
  <FileTree
    :class="['workspace-tree', 'workspace-tree-list']"
    :aria-label="t('chat.workspaceFiles')"
    :expanded="expanded"
    :selected-path="tabbedPath"
    @expanded-change="onExpandedChange"
    @update:selected-path="onSelectPath"
  >
    <WorkspaceRow
      v-for="child in rootChildren"
      :key="child.path"
      :node="child"
      :tabbed="isTabbed(child.path)"
      :scope="scope"
      @move="onMove"
      @insert="emit('insert-ref', $event)"
      @edit="emit('edit', $event)"
      @delete="emit('delete', $event)"
      @new-file="emit('new-file', $event)"
    />
    <p v-if="rootStatus === 'loading'" class="workspace-tree-hint">{{ t('workspaceTree.loading') }}</p>
    <p v-else-if="rootStatus === 'error'" class="workspace-tree-hint workspace-tree-error">
      {{ rootError }}
      <button type="button" class="workspace-tree-retry" @click="reload('')">{{ t('workspaceTree.retry') }}</button>
    </p>
  </FileTree>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, toRef } from 'vue'
import { FileTree } from '@repo/elements/file-tree'
import WorkspaceRow from '@/components/chat/WorkspaceRow.vue'
import { useWorkspaceTree } from '@/composables/useWorkspaceTree'
import type { WorkspaceScope, WorkspaceTreeNode } from '@/lib/api'
import { useI18n } from '@/locales'

/** One tree row's identity for the per-node actions a parent renders. */
export interface WorkspaceNodeHandle {
  path: string
  name: string
  isDir: boolean
}

/**
 * The conversation's files as a lazily loaded directory tree, rooted at the
 * project the session belongs to (or the project its composer's pill chose
 * before the conversation exists). The workbench is collapsed by default and
 * this component only mounts with it, so no directory request happens until
 * the tree is first shown. The card head, the dialogs and the refresh live in
 * the tab that owns this tree; the tree itself only lists rows and reports
 * what the user asked to do with one.
 */
const props = withDefaults(defineProps<{ scope?: WorkspaceScope }>(), { scope: () => ({}) })

const emit = defineEmits<{
  (e: 'insert-ref', path: string): void
  (e: 'edit', node: WorkspaceNodeHandle): void
  (e: 'delete', node: WorkspaceNodeHandle): void
  (e: 'new-file', node: WorkspaceNodeHandle): void
}>()

const { t } = useI18n()
const { dirs, expanded, ensureRoot, load } = useWorkspaceTree(toRef(props, 'scope'))

const tabbedPath = ref('')

const rootState = computed(() => dirs.value.get(''))
const rootChildren = computed(() => rootState.value?.children ?? [])
const rootStatus = computed(() => rootState.value?.status ?? 'loading')
const rootError = computed(() => rootState.value?.error ?? '')

function isTabbed(path: string): boolean {
  return tabbedPath.value === '' ? path === rootChildren.value[0]?.path : path === tabbedPath.value
}

function reload(path: string) {
  void load(path)
}

/**
 * The tree's own expanded set is this tree's: what it opens is what loads, and
 * a directory closes without a request. Only the directories that just opened
 * are asked for, so a collapse costs nothing.
 */
function onExpandedChange(next: Set<string>) {
  const current = expanded.value
  for (const path of next) {
    if (current.has(path)) continue
    current.add(path)
    const state = dirs.value.get(path)
    if (!state || state.status === 'error') void load(path)
  }
  for (const path of [...current]) {
    if (!next.has(path)) current.delete(path)
  }
}

function onSelectPath(path: string) {
  tabbedPath.value = path
}

onMounted(() => {
  ensureRoot()
  tabbedPath.value = rootChildren.value[0]?.path ?? ''
})

function onMove(from: string, delta: number) {
  const visible = visiblePaths()
  const index = visible.indexOf(from)
  const next = visible[Math.min(Math.max(index + delta, 0), visible.length - 1)]
  if (next) tabbedPath.value = next
}

function visiblePaths(): string[] {
  const out: string[] = []
  const walk = (children: WorkspaceTreeNode[]) => {
    for (const child of children) {
      out.push(child.path)
      if (child.isDir && expanded.value.has(child.path)) {
        walk(dirs.value.get(child.path)?.children ?? [])
      }
    }
  }
  walk(rootChildren.value)
  return out
}
</script>

<style scoped>
/* The library's tree brings a card frame and a padded inner box; the
   workbench's file tab has its own frame, so the tree sits flat inside it. */
.workspace-tree {
  border: 0;
  border-radius: 0;
  background: transparent;
  font-family: inherit;
  font-size: inherit;
}

.workspace-tree :deep(> div) {
  padding: 0;
}

.workspace-tree-list {
  display: flex;
  flex-direction: column;
}

.workspace-tree-hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
  padding: 2px 8px;
}

.workspace-tree-error {
  color: var(--forebrain-danger);
}

.workspace-tree-retry {
  margin-left: 6px;
  text-decoration: underline;
  font-size: 12px;
}
</style>
