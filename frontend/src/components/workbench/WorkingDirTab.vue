<template>
  <div class="bench-card wd-card">
    <div class="bench-card-head">
      <span class="bench-card-title"><FolderTree class="size-3.5" aria-hidden="true" />{{ t('chat.benchTabTree') }}</span>
      <div class="wd-head-acts">
        <button
          type="button"
          class="bench-icon-btn"
          :title="t('chat.benchRootNewFile')"
          :aria-label="t('chat.benchRootNewFile')"
          data-testid="tree-new-root"
          @click="openNewFile('')"
        >
          <FilePlus class="size-3.5" aria-hidden="true" />
        </button>
        <button
          type="button"
          class="bench-icon-btn"
          :aria-label="t('chat.benchRefresh')"
          data-testid="tree-refresh"
          @click="() => void refresh()"
        >
          <RotateCw class="size-3.5" aria-hidden="true" />
        </button>
      </div>
    </div>
    <div v-if="rootPath" class="wd-root font-mono" :title="rootPath">{{ rootPath }}</div>
    <WorkspaceTree
      :scope="scope"
      @insert-ref="emit('insert-ref', $event)"
      @edit="onEdit"
      @delete="onDelete"
      @new-file="onNewFile"
    />
    <div class="wd-tip">{{ t('chat.benchInsertRef') }}</div>
  </div>

  <!-- New file -->
  <ForebrainModal :open="newOpen" size="sm" :title="t('chat.treeNewTitle')" @close="closeNew">
    <span class="wd-hint font-mono">{{ newDir ? `${newDir}/` : t('chat.benchTabTree') + ' /' }}</span>
    <label class="wd-field">
      <span class="wd-label">{{ t('chat.treeNewName') }}</span>
      <input
        v-model="newName"
        class="forebrain-field font-mono"
        :placeholder="t('chat.treeNewPlaceholder')"
        autocomplete="off"
        data-testid="tree-new-name"
        @keydown.enter="createFile"
      />
    </label>
    <p v-if="newError" class="wd-error">{{ newError }}</p>
    <template #footer>
      <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="closeNew">{{ t('common.cancel') }}</button>
      <button
        type="button"
        class="forebrain-btn forebrain-btn-primary text-xs"
        :disabled="busy || !newName.trim()"
        data-testid="tree-new-create"
        @click="createFile"
      >
        {{ t('chat.treeNewCreate') }}
      </button>
    </template>
  </ForebrainModal>

  <!-- Rename directory -->
  <ForebrainModal :open="renameOpen" size="sm" :title="t('chat.treeRenameTitle')" @close="closeRename">
    <span class="wd-hint font-mono">{{ renameTarget?.path }}/</span>
    <label class="wd-field">
      <span class="wd-label">{{ t('chat.treeRenameName') }}</span>
      <input v-model="renameName" class="forebrain-field font-mono" autocomplete="off" data-testid="tree-rename-name" />
    </label>
    <p v-if="renameError" class="wd-error">{{ renameError }}</p>
    <template #footer>
      <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="closeRename">{{ t('common.cancel') }}</button>
      <button
        type="button"
        class="forebrain-btn forebrain-btn-primary text-xs"
        :disabled="busy || !renameName.trim()"
        data-testid="tree-rename-save"
        @click="saveRename"
      >
        {{ t('chat.treeRenameSave') }}
      </button>
    </template>
  </ForebrainModal>

  <!-- Edit file -->
  <FileEditDialog :open="editOpen" :scope="scope" :path="editPath" @close="editOpen = false" @saved="onSaved" />

  <!-- Delete file / directory -->
  <ConfirmDialog
    :open="deleteTarget !== null"
    :title="deleteTitle"
    :message="deleteMessage"
    :confirm-label="t('chat.treeDelete')"
    danger
    @confirm="confirmDelete"
    @cancel="closeDelete"
  />
</template>

<script setup lang="ts">
/**
 * The workbench's working-directory tab: the conversation's files as a tree,
 * with every row carrying its own actions. A file can be edited (rename and
 * content in one save) or deleted; a directory can be renamed, deleted whole,
 * or given a new file. The card head adds "new file at the root". Editing
 * here is deliberately distinct from the model's file tools: it is not
 * recorded as a conversation change and not offered to "rewind to here".
 */
import { computed, ref, toRef } from 'vue'
import { FilePlus, FolderTree, RotateCw } from 'lucide-vue-next'

import WorkspaceTree, { type WorkspaceNodeHandle } from '@/components/chat/WorkspaceTree.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import FileEditDialog from '@/components/workbench/FileEditDialog.vue'
import { useWorkspaceTree } from '@/composables/useWorkspaceTree'
import { getErrorMessage, forebrainApi, type WorkspaceScope } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{ scope: WorkspaceScope; rootPath?: string }>(), { rootPath: '' })
const emit = defineEmits<{ (e: 'insert-ref', path: string): void }>()

const { t } = useI18n()
const { refresh } = useWorkspaceTree(toRef(props, 'scope'))

const busy = ref(false)

const newOpen = ref(false)
const newDir = ref('')
const newName = ref('')
const newError = ref('')

const renameOpen = ref(false)
const renameTarget = ref<WorkspaceNodeHandle | null>(null)
const renameName = ref('')
const renameError = ref('')

const editOpen = ref(false)
const editPath = ref('')

const deleteTarget = ref<WorkspaceNodeHandle | null>(null)

const deleteTitle = computed(() => {
  const node = deleteTarget.value
  if (!node) return ''
  return node.isDir
    ? t('chat.treeDelDirTitle', { name: node.name })
    : t('chat.treeDelFileTitle', { name: node.name })
})
const deleteMessage = computed(() => {
  const node = deleteTarget.value
  if (!node) return ''
  const body = node.isDir ? t('chat.treeDelDirBody') : t('chat.treeDelFileBody')
  const shown = node.isDir ? `${node.path}/` : node.path
  return `${shown}\n${body}`
})

function join(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name
}
function parentDir(path: string): string {
  const slash = path.lastIndexOf('/')
  return slash >= 0 ? path.slice(0, slash) : ''
}

function openNewFile(dir: string) {
  newDir.value = dir
  newName.value = ''
  newError.value = ''
  newOpen.value = true
}
function closeNew() {
  newOpen.value = false
}

async function createFile() {
  const name = newName.value.trim()
  if (!name || busy.value) return
  busy.value = true
  newError.value = ''
  const rel = join(newDir.value, name)
  try {
    await forebrainApi.workspaceWrite(props.scope, { path: rel, content: '', create: true })
    newOpen.value = false
    await refresh()
    editPath.value = rel
    editOpen.value = true
  } catch (cause) {
    newError.value = getErrorMessage(cause)
  } finally {
    busy.value = false
  }
}

function onNewFile(node: WorkspaceNodeHandle) {
  openNewFile(node.path)
}
function onEdit(node: WorkspaceNodeHandle) {
  if (node.isDir) {
    renameTarget.value = node
    renameName.value = node.name
    renameError.value = ''
    renameOpen.value = true
    return
  }
  editPath.value = node.path
  editOpen.value = true
}
function closeRename() {
  renameOpen.value = false
}

async function saveRename() {
  const node = renameTarget.value
  const name = renameName.value.trim()
  if (!node || !name || busy.value) return
  const to = join(parentDir(node.path), name)
  if (to === node.path) {
    renameOpen.value = false
    return
  }
  busy.value = true
  renameError.value = ''
  try {
    await forebrainApi.workspaceRename(props.scope, { from: node.path, to })
    renameOpen.value = false
    await refresh()
  } catch (cause) {
    renameError.value = getErrorMessage(cause)
  } finally {
    busy.value = false
  }
}

function onDelete(node: WorkspaceNodeHandle) {
  deleteTarget.value = node
}
function closeDelete() {
  deleteTarget.value = null
}

async function confirmDelete() {
  const node = deleteTarget.value
  if (!node || busy.value) return
  busy.value = true
  try {
    await forebrainApi.workspaceDelete(props.scope, node.path)
    deleteTarget.value = null
    await refresh()
  } catch (cause) {
    void getErrorMessage(cause)
    deleteTarget.value = null
  } finally {
    busy.value = false
  }
}

async function onSaved() {
  editOpen.value = false
  await refresh()
}
</script>

<style scoped>
.bench-card {
  display: grid;
  gap: 6px;
  padding: 12px 8px 10px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-surface);
}
.bench-card-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 0 4px;
}
.bench-card-title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  font-weight: 600;
  color: var(--forebrain-text);
}
.wd-head-acts {
  display: flex;
  align-items: center;
}
.bench-icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border-radius: 6px;
  color: var(--forebrain-muted-text);
}
.bench-icon-btn:hover {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
}
.wd-root {
  padding: 0 6px;
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wd-tip {
  padding: 0 6px;
  font-size: 11px;
  color: var(--forebrain-muted-text);
}
.wd-hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.wd-field {
  display: grid;
  gap: 4px;
}
.wd-label {
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
}
.wd-error {
  margin: 0;
  font-size: 12px;
  color: var(--forebrain-danger);
}
</style>
