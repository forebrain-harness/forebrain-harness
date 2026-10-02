<template>
  <li role="treeitem"
    :data-path="node.path"
    :aria-level="depth + 1"
    :aria-expanded="node.isDir ? isExpanded : undefined"
  >
    <button
      ref="rowRef"
      type="button"
      class="workspace-row"
      :class="{ 'workspace-row--tabbed': tabbed }"
      :style="{ paddingInlineStart: `${8 + depth * 14}px` }"
      :title="node.path"
      :tabindex="tabbed ? 0 : -1"
      @click="activate"
      @keydown="keydown"
    >
      <ChevronRight v-if="node.isDir" class="workspace-row-caret" :class="{ 'workspace-row-caret--open': expanded }" aria-hidden="true" />
      <component :is="node.isDir ? (expanded ? FolderOpen : Folder) : FileIcon" class="workspace-row-ic" aria-hidden="true" />
      <span class="workspace-row-name">{{ node.name }}</span>
    </button>

    <ul v-if="node.isDir && expanded" role="group" class="workspace-row-group">
      <template v-if="state?.status === 'loaded'">
        <WorkspaceRow
          v-for="child in state.children"
          :key="child.path"
          :node="child"
          :depth="depth + 1"
          :tabbed="isTabbed(child.path)"
          @activate="$emit('activate', $event)"
          @move="forwardMove"
          @toggle="$emit('toggle', $event)"
          @insert="$emit('insert', $event)"
        />
        <li v-if="!state.children.length" class="workspace-row-hint">{{ t('workspaceTree.emptyDir') }}</li>
      </template>
      <li v-else-if="state?.status === 'loading'" class="workspace-row-hint">{{ t('workspaceTree.loading') }}</li>
      <li v-else-if="state?.status === 'error'" class="workspace-row-hint workspace-row-error">
        {{ state.error }}
        <button type="button" class="workspace-row-retry" @click="reload">{{ t('workspaceTree.retry') }}</button>
      </li>
    </ul>
  </li>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { ChevronRight, File as FileIcon, Folder, FolderOpen } from 'lucide-vue-next'
import { useWorkspaceTree } from '@/composables/useWorkspaceTree'
import type { WorkspaceTreeNode } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * One row of the workspace tree: a directory toggles its lazily loaded
 * children, a file offers itself as an @-reference.
 */
const props = defineProps<{
  node: WorkspaceTreeNode
  depth: number
  tabbed: boolean
}>()

const emit = defineEmits<{
  (e: 'activate', handle: { path: string; isDir: boolean; expanded: boolean }): void
  (e: 'move', from: string, delta: number): void
  (e: 'toggle', path: string): void
  (e: 'insert', path: string): void
}>()

const { t } = useI18n()
const { dirs, expanded, toggle, load } = useWorkspaceTree()
const rowRef = ref<HTMLButtonElement | null>(null)

const state = computed(() => dirs.get(props.node.path))
const isExpanded = computed(() => expanded.has(props.node.path))

function isTabbed(path: string): boolean {
  return props.tabbed && path === firstChildPath.value
}

const firstChildPath = computed(() => state.value?.children[0]?.path ?? '')

function activate() {
  if (props.node.isDir) {
    toggle(props.node.path)
    return
  }
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
      activate()
      break
    default:
      break
  }
}
</script>

<style scoped>
.workspace-row {
  display: flex;
  align-items: center;
  gap: 5px;
  width: 100%;
  min-height: 22px;
  border-radius: 5px;
  padding-inline-end: 6px;
  text-align: left;
  font-size: 12px;
  color: var(--forebrain-text-2);
}
.workspace-row:hover {
  background: var(--forebrain-bg-alt);
}
.workspace-row:focus-visible {
  outline: 1px solid var(--forebrain-focus-border);
  outline-offset: -1px;
}
.workspace-row-caret {
  width: 12px;
  height: 12px;
  flex: none;
  color: var(--forebrain-muted-text);
  transition: transform 120ms ease;
}
.workspace-row-caret--open {
  transform: rotate(90deg);
}
.workspace-row-ic {
  width: 13px;
  height: 13px;
  flex: none;
  color: var(--forebrain-muted-text);
}
.workspace-row-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.workspace-row-group {
  list-style: none;
  margin: 0;
  padding: 0;
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
