<template>
  <div class="workspace-tree" role="group" :aria-label="t('chat.workspaceFiles')">
    <ul role="tree" :aria-label="t('chat.workspaceFiles')" class="workspace-tree-list">
      <WorkspaceRow
        v-for="child in rootChildren"
        :key="child.path"
        :node="child"
        :depth="0"
        :tabbed="isTabbed(child.path)"
        :scope="scope"
        @activate="onActivate"
        @move="onMove"
        @toggle="toggle"
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
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, toRef } from 'vue'
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
const { dirs, expanded, ensureRoot, toggle, load } = useWorkspaceTree(toRef(props, 'scope'))

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

onMounted(() => {
  ensureRoot()
  tabbedPath.value = rootChildren.value[0]?.path ?? ''
})

function onActivate(handle: { path: string; isDir: boolean; expanded: boolean }) {
  if (handle.isDir) toggle(handle.path)
}

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
.workspace-tree {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.workspace-tree-list {
  list-style: none;
  margin: 0;
  padding: 0;
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
