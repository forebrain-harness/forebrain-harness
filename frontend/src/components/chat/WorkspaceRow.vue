<template>
  <!-- A directory is the library's file-tree folder: it draws the caret, the
       folder mark, the name and its own nested group, and it owns the fold.
       This row only adds what the workspace tree needs on top — children
       loaded one level at a time, the roving tab stop, the arrow keys and the
       per-node actions. -->
  <FileTreeFolder
    v-if="node.isDir"
    :path="node.path"
    :name="node.name"
    :data-path="node.path"
    :aria-expanded="isExpanded ? 'true' : 'false'"
    :tabindex="tabbed ? 0 : -1"
    class="workspace-row-item"
    button-class="workspace-row"
    @keydown="keydown"
  >
    <template #actions>
      <TreeNodeActions
        :is-dir="node.isDir"
        :name="node.name"
        @new-file="emit('new-file', handle)"
        @edit="emit('edit', handle)"
        @delete="emit('delete', handle)"
      />
    </template>
    <template v-if="isExpanded">
      <template v-if="state?.status === 'loaded'">
        <WorkspaceRow
          v-for="child in state.children"
          :key="child.path"
          :node="child"
          :tabbed="isTabbed(child.path)"
          :scope="scope"
          @move="forwardMove"
          @insert="$emit('insert', $event)"
          @edit="$emit('edit', $event)"
          @delete="$emit('delete', $event)"
          @new-file="$emit('new-file', $event)"
        />
        <p v-if="!state.children.length" class="workspace-row-hint">{{ t('workspaceTree.emptyDir') }}</p>
      </template>
      <p v-else-if="state?.status === 'loading'" class="workspace-row-hint">{{ t('workspaceTree.loading') }}</p>
      <p v-else-if="state?.status === 'error'" class="workspace-row-hint workspace-row-error">
        {{ state.error }}
        <button type="button" class="workspace-row-retry" @click="reload">{{ t('workspaceTree.retry') }}</button>
      </p>
    </template>
  </FileTreeFolder>

  <FileTreeFile
    v-else
    :path="node.path"
    :name="node.name"
    :data-path="node.path"
    :tabindex="tabbed ? 0 : -1"
    class="workspace-row workspace-row-item"
    @keydown="keydown"
    @click="activate"
  >
    <template #actions>
      <TreeNodeActions
        :is-dir="node.isDir"
        :name="node.name"
        @new-file="emit('new-file', handle)"
        @edit="emit('edit', handle)"
        @delete="emit('delete', handle)"
      />
    </template>
  </FileTreeFile>
</template>

<script setup lang="ts">
import { computed, toRef } from 'vue'
import { FileTreeFile, FileTreeFolder } from '@repo/elements/file-tree'
import TreeNodeActions from '@/components/workbench/TreeNodeActions.vue'
import { useWorkspaceTree } from '@/composables/useWorkspaceTree'
import type { WorkspaceScope, WorkspaceTreeNode } from '@/lib/api'
import { useI18n } from '@/locales'

/** One tree row's identity for the per-node actions the row reports. */
export interface WorkspaceNodeHandle {
  path: string
  name: string
  isDir: boolean
}

/**
 * One row of the workspace tree: a directory toggles its lazily loaded
 * children, a file offers itself as an @-reference, and a file row carries its
 * edit and delete buttons.
 *
 * The row's own chrome — the caret, the folder/file mark, the name, the fold
 * and the nested group — is the library's file tree; this component adds only
 * what the workspace needs: children loaded one level at a time, a single
 * roving tab stop, the arrow keys, and the file actions.
 */
const props = withDefaults(defineProps<{
  node: WorkspaceTreeNode
  tabbed: boolean
  scope?: WorkspaceScope
}>(), { scope: () => ({}) })

const emit = defineEmits<{
  (e: 'move', from: string, delta: number): void
  (e: 'toggle', path: string): void
  (e: 'insert', path: string): void
  (e: 'edit', node: WorkspaceNodeHandle): void
  (e: 'delete', node: WorkspaceNodeHandle): void
  (e: 'new-file', node: WorkspaceNodeHandle): void
}>()

const { t } = useI18n()
const { dirs, expanded, toggle, load } = useWorkspaceTree(toRef(props, 'scope'))

const handle = computed<WorkspaceNodeHandle>(() => ({ path: props.node.path, name: props.node.name, isDir: props.node.isDir }))
const state = computed(() => dirs.value.get(props.node.path))
const isExpanded = computed(() => expanded.value.has(props.node.path))
const firstChildPath = computed(() => state.value?.children[0]?.path ?? '')

function isTabbed(path: string): boolean {
  return props.tabbed && path === firstChildPath.value
}

function activate() {
  if (props.node.isDir) return
  emit('insert', props.node.path)
}

function forwardMove(from: string, delta: number) {
  emit('move', from, delta)
}

function reload() {
  void load(props.node.path)
}

function keydown(event: KeyboardEvent) {
  switch (event.key) {
    case 'ArrowDown':
      event.preventDefault()
      emit('move', props.node.path, 1)
      break
    case 'ArrowUp':
      event.preventDefault()
      emit('move', props.node.path, -1)
      break
    case 'ArrowRight':
      event.preventDefault()
      if (props.node.isDir && !isExpanded.value) toggle(props.node.path)
      else if (props.node.isDir && firstChildPath.value) emit('move', props.node.path, 1)
      break
    case 'ArrowLeft':
      event.preventDefault()
      if (props.node.isDir && isExpanded.value) toggle(props.node.path)
      else emit('move', props.node.path, -1)
      break
    case 'Enter':
    case ' ':
      event.preventDefault()
      if (props.node.isDir) toggle(props.node.path)
      else activate()
      break
    default:
      break
  }
}
</script>

<style scoped>
/* The library's row is a padded 14px line; this tree is the workbench's dense
   one: 12px on a 22px line, with the marks this app draws at 13px. */
.workspace-row {
  gap: 5px;
  min-height: 22px;
  border-radius: 5px;
  padding: 0 6px 0 8px;
  font-size: 12px;
  color: var(--forebrain-text-2);
}

.workspace-row:hover {
  background: var(--forebrain-bg-alt);
}

.workspace-row :deep(svg) {
  width: 13px;
  height: 13px;
  color: var(--forebrain-muted-text);
}

.workspace-row-line {
  display: flex;
  align-items: center;
  border-radius: 5px;
}

.workspace-row-line:hover {
  background: var(--forebrain-bg-alt);
}

.workspace-row-hint {
  font-size: 11px;
  color: var(--forebrain-muted-text);
  padding: 2px 0 2px 26px;
}

.workspace-row-error {
  color: var(--forebrain-danger);
}

.workspace-row-retry {
  margin-left: 6px;
  text-decoration: underline;
  font-size: 11px;
}
</style>
