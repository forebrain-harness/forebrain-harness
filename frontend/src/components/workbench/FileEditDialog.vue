<template>
  <ForebrainModal :open="open" size="xl" :title="t('chat.treeEditTitle')" @close="emit('close')">
    <div class="fed-head">
      <label class="fed-field">
        <span class="fed-label">{{ t('chat.treeEditName') }}</span>
        <Input v-model="name" class="forebrain-field h-auto font-mono" autocomplete="off" data-testid="tree-edit-name" />
      </label>
      <span v-if="dir" class="fed-dir font-mono">{{ dir }}/</span>
    </div>

    <p v-if="loading" class="fed-hint">{{ t('common.loading') }}</p>
    <p v-else-if="loadError" class="fed-hint fed-hint--error">{{ loadError }}</p>
    <FileEditor
      v-else
      :key="editorKey"
      :path="currentPath"
      v-model="content"
      height="52vh"
      @dirty="dirty = $event"
    />

    <template #footer>
      <p v-if="saveError" class="fed-footer-error">{{ saveError }}</p>
      <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="emit('close')">
        {{ t('common.cancel') }}
      </Button>
      <Button
        type="button"
        class="forebrain-btn forebrain-btn-primary h-auto text-xs"
        :disabled="saving || loading || !name.trim()"
        data-testid="tree-edit-save"
        @click="save"
      >
        {{ saving ? t('common.loading') : t('chat.treeEditSave') }}
      </Button>
    </template>
  </ForebrainModal>
</template>

<script setup lang="ts">
/**
 * The workbench's file editor dialog (plan 017): the shared FileEditor with a
 * "file name" field above it. It opens the file's own text, and one save
 * writes the content and then applies a rename when the name changed, so the
 * two edits land together. Renaming the file is not counted as a
 * conversation change — only the model's file tools are.
 */
import { computed, defineAsyncComponent, ref, watch } from 'vue'

import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import { getErrorMessage, forebrainApi, type WorkspaceScope } from '@/lib/api'
import { useI18n } from '@/locales'

const FileEditor = defineAsyncComponent(() => import('@/components/editor/FileEditor.vue'))

const props = defineProps<{ open: boolean; scope: WorkspaceScope; path: string }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved', path: string): void }>()

const { t } = useI18n()
const name = ref('')
const content = ref('')
const loading = ref(false)
const saving = ref(false)
const dirty = ref(false)
const loadError = ref('')
const saveError = ref('')
let generation = 0

const currentPath = computed(() => props.path)
const dir = computed(() => {
  const slash = props.path.lastIndexOf('/')
  return slash >= 0 ? props.path.slice(0, slash) : ''
})
const editorKey = computed(() => `${props.path}:${generation}`)

function basename(path: string): string {
  const slash = path.lastIndexOf('/')
  return slash >= 0 ? path.slice(slash + 1) : path
}

async function load() {
  const gen = ++generation
  name.value = basename(props.path)
  content.value = ''
  dirty.value = false
  loadError.value = ''
  saveError.value = ''
  if (!props.path) return
  loading.value = true
  try {
    const res = await forebrainApi.workspaceFile(props.scope, props.path)
    if (gen === generation) content.value = res.content
  } catch (cause) {
    if (gen === generation) loadError.value = getErrorMessage(cause)
  } finally {
    if (gen === generation) loading.value = false
  }
}

async function save() {
  if (saving.value) return
  const target = basename(name.value.trim())
  if (!target) return
  const newPath = dir.value ? `${dir.value}/${target}` : target
  saving.value = true
  saveError.value = ''
  try {
    await forebrainApi.workspaceWrite(props.scope, { path: props.path, content: content.value })
    if (newPath !== props.path) {
      await forebrainApi.workspaceRename(props.scope, { from: props.path, to: newPath })
    }
    emit('saved', newPath)
    emit('close')
  } catch (cause) {
    saveError.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

watch(() => [props.open, props.path], () => {
  if (props.open) void load()
}, { immediate: true })
</script>

<style scoped>
.fed-head {
  display: flex;
  align-items: flex-end;
  gap: 8px;
}
.fed-field {
  display: grid;
  gap: 4px;
  flex: 1;
  min-width: 0;
}
.fed-label {
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
}
.fed-dir {
  font-size: 12px;
  color: var(--forebrain-muted-text);
  padding-bottom: 8px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 45%;
}
.fed-hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
  margin: 0;
}
.fed-hint--error,
.fed-footer-error {
  color: var(--forebrain-danger);
}
.fed-footer-error {
  flex: 1;
  margin: 0;
  font-size: 12px;
  text-align: left;
}
</style>
