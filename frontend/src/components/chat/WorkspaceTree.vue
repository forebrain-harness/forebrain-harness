<template>
  <div class="workspace-tree" role="group" :aria-label="t('chat.workspaceFiles')">
    <div class="workspace-tree-head">
      <span class="workspace-tree-title">{{ t('chat.workspaceFiles') }}</span>
      <button
        type="button"
        class="workspace-tree-refresh"
        :aria-label="t('workspaceTree.refresh')"
        @click="() => void refresh()"
      >
        <RotateCw class="size-3.5" aria-hidden="true" />
      </button>
    </div>
    <ul role="tree" :aria-label="t('chat.workspaceFiles')" class="workspace-tree-list">
      <WorkspaceRow
        v-for="child in rootChildren"
        :key="child.path"
        :node="child"
        :depth="0"
        :tabbed="isTabbed(child.path)"
        @activate="onActivate"
        @move="onMove"
        @toggle="toggle"
        @insert="emit('insert-ref', $event)"
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
import { computed, onMounted, ref } from 'vue'
import { RotateCw } from 'lucide-vue-next'
import WorkspaceRow from '@/components/chat/WorkspaceRow.vue'
import { useWorkspaceTree } from '@/composables/useWorkspaceTree'
import type { WorkspaceTreeNode } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The workspace's files as a lazily loaded directory tree. The workbench is
 * collapsed by default and this component only mounts with it, so no
 * directory request happens until the tree is first shown.
 */
const emit = defineEmits<{ (e: 'insert-ref', path: string): void }>()

const { t } = useI18n()
const { dirs, expanded, ensureRoot, toggle, refresh, load } = useWorkspaceTree()

const tabbedPath = ref('')

const rootState = computed(() => dirs.get(''))
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
      if (child.isDir && expanded.has(child.path)) {
        walk(dirs.get(child.path)?.children ?? [])
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
.workspace-tree-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.workspace-tree-title {
  font-size: 12px;
  font-weight: 500;
  color: var(--forebrain-text-2);
}
.workspace-tree-refresh {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border-radius: 6px;
  color: var(--forebrain-muted-text);
}
.workspace-tree-refresh:hover {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
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
