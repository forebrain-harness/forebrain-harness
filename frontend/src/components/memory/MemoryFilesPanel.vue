<template>
  <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
  <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
    {{ t('memories.emptyFiles') }}
  </div>
  <template v-else>
    <div class="forebrain-table-wrap">
      <table class="forebrain-table forebrain-table--fixed">
        <colgroup>
          <col style="width: 40px" />
          <col style="width: 300px" />
          <col />
          <col style="width: 150px" />
          <col style="width: 150px" />
          <col style="width: 88px" />
        </colgroup>
        <thead>
          <tr>
            <th>
              <input
                type="checkbox"
                :checked="allSelected"
                :aria-label="t('memories.selectAll')"
                data-testid="memories-select-all"
                @change="toggleAll(($event.target as HTMLInputElement).checked)"
              />
            </th>
            <th>{{ t('memories.colFile') }}</th>
            <th>{{ t('memories.colContent') }}</th>
            <SortHeader
              :label="t('memories.colCreated')"
              sort-key="created"
              :active="sort === 'created'"
              :order="order"
              first="desc"
              @sort="sortBy"
            />
            <SortHeader
              :label="t('memories.colUpdated')"
              sort-key="updated"
              :active="sort === 'updated'"
              :order="order"
              first="desc"
              @sort="sortBy"
            />
            <th class="text-right">{{ t('memories.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="file in rows" :key="file.path" :data-memory-file="file.path">
            <td>
              <input
                type="checkbox"
                :checked="selected.has(file.path)"
                :aria-label="t('memories.selectFile', { path: file.path })"
                @change="toggleSelect(file.path, ($event.target as HTMLInputElement).checked)"
              />
            </td>
            <td>
              <div class="flex min-w-0 items-center gap-1.5">
                <span class="forebrain-cell-1 font-mono" v-tip:top.trunc="file.path">{{ file.path }}</span>
                <BadgeComponent v-if="file.core" variant="gray" class="shrink-0" v-tip:top="t('memories.coreFileHint')">
                  {{ t('memories.coreBadge') }}
                </BadgeComponent>
              </div>
            </td>
            <td class="text-[var(--forebrain-text-2)]">
              <span class="forebrain-cell-1" v-tip:top.trunc="previewOf(file)">{{ previewOf(file) }}</span>
            </td>
            <td class="forebrain-num font-mono text-[var(--forebrain-text-2)]">{{ formatDateTime(file.createdAt) }}</td>
            <td class="forebrain-num font-mono text-[var(--forebrain-text-2)]">{{ formatDateTime(file.updatedAt) }}</td>
            <td class="text-right">
              <RowActions>
                <RowAction
                  :icon="Pencil"
                  :label="t('common.edit')"
                  :name="file.path"
                  :data-testid="`memories-edit-${file.path}`"
                  @click="openEditor(file)"
                />
                <RowAction
                  v-if="!file.core"
                  :icon="Trash2"
                  :label="t('common.delete')"
                  :name="file.path"
                  tone="danger"
                  :data-testid="`memories-delete-${file.path}`"
                  @click="deleteOne(file)"
                />
                <RowActionSlot v-else />
              </RowActions>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Editor -->
    <ForebrainModal :open="editing !== null" size="xl" @close="closeEditor">
      <template #title>
        <span class="font-mono">{{ editing?.path }}</span>
      </template>
      <div data-testid="memories-editor">
        <p class="mb-2 text-[11px] text-[var(--forebrain-muted-text)]">{{ t('memories.updatedAtLabel', { time: formatDateTime(editing?.updatedAt ?? 0) }) }}</p>
        <p v-if="editing?.core" class="mb-2 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('memories.coreFileHint') }}</p>
        <FileEditor
          v-if="!loadingEditor"
          :key="editorKey"
          :path="editing?.path ?? ''"
          height="440px"
          v-model="draft"
          @dirty="dirty = $event"
        />
        <div v-else class="min-h-[240px] rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-code-bg)] p-3 text-[12px] text-[var(--forebrain-muted-text)]">
          {{ t('common.loading') }}
        </div>
        <p v-if="editorError" class="mt-2 text-[12px] text-[var(--forebrain-danger)]">{{ editorError }}</p>
        <div class="mt-3 flex items-center gap-2 border-t border-[var(--forebrain-divider)] pt-3">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs text-[var(--forebrain-danger)]" :disabled="saving" data-testid="memories-editor-delete" @click="deleteFromEditor">
            {{ editing?.core ? t('memories.emptyContent') : t('memories.deleteFile') }}
          </button>
          <span class="flex-1" />
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="closeEditor">{{ t('common.cancel') }}</button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving || !dirty" data-testid="memories-editor-save" @click="saveEditor">
            {{ saving ? t('common.loading') : t('common.save') }}
          </button>
        </div>
      </div>
    </ForebrainModal>
  </template>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, ref } from 'vue'
import { Pencil, Trash2 } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import RowActionSlot from '@/components/common/RowActionSlot.vue'
import SortHeader from '@/components/common/SortHeader.vue'
import forebrainApi, { getErrorMessage, type MemoryFileRecord } from '@/lib/api'
import { formatDateTime } from '@/lib/time'
import { useI18n } from '@/locales'

const FileEditor = defineAsyncComponent(() => import('@/components/editor/FileEditor.vue'))

/**
 * The memory file table both memory surfaces share: the primary agent's page
 * (scope=global) and the project space tab (scope=project). It draws one page
 * of rows and nothing else — the search box, the bulk buttons and the pager
 * belong to the page, because the two surfaces word and place them their own
 * way.
 *
 * Core files are editable but never deletable (the row keeps its hole so the
 * icons line up), and the row's 删除 acts at once: clearing a core file's
 * content and deleting a file both leave a version behind, so the page is told
 * to reload rather than the row being dropped from a list this table does not
 * own.
 */
const props = defineProps<{
  scope: 'global' | 'project'
  projectId?: string
  /** The rows of the page this table draws. */
  rows: MemoryFileRecord[]
  loading: boolean
  /** The column the server ordered by, and that column's direction. */
  sort: string
  order: 'asc' | 'desc'
  /** The paths ticked for a bulk delete; the page owns the set. */
  selected: Set<string>
}>()

const emit = defineEmits<{
  sort: [key: string, order: 'asc' | 'desc']
  'update:selected': [selected: Set<string>]
  /** After an edit or a delete the list the page holds is one row out of date. */
  reload: []
  /** What the write just did, for the page's one notice line. */
  notice: [text: string]
  error: [text: string]
}>()

const { t } = useI18n()

const saving = ref(false)
const editing = ref<MemoryFileRecord | null>(null)
const draft = ref('')
// The editor stays read-only until the file's current bytes arrive: editing
// into a stale draft would silently save the old content over the user's.
const loadingEditor = ref(false)
const editorError = ref('')
// True once the open file differs from the bytes it was loaded with; saving is
// refused otherwise, so an untouched file is never rewritten.
const dirty = ref(false)
// Remounting the editor re-baselines it for each newly opened file.
const editorKey = ref(0)

const allSelected = computed(() => props.rows.length > 0 && props.rows.every((file) => props.selected.has(file.path)))

/**
 * The grey 内容 line: the listing answers each file's first non-empty line
 * beside its name and times (plan 073), so the table never asks for a body of
 * its own.
 */
function previewOf(file: MemoryFileRecord): string {
  return file.preview?.trim() || '—'
}

function sortBy(key: string, order: 'asc' | 'desc'): void {
  emit('sort', key, order)
}

function toggleSelect(path: string, checked: boolean): void {
  const next = new Set(props.selected)
  if (checked) {
    next.add(path)
  } else {
    next.delete(path)
  }
  emit('update:selected', next)
}

function toggleAll(checked: boolean): void {
  emit('update:selected', checked ? new Set(props.rows.map((file) => file.path)) : new Set<string>())
}

async function openEditor(file: MemoryFileRecord) {
  editing.value = file
  draft.value = ''
  editorError.value = ''
  dirty.value = false
  loadingEditor.value = true
  try {
    const data = await forebrainApi.memoryFileRead(props.scope, file.path, props.projectId)
    draft.value = data.content
  } catch (e: unknown) {
    editorError.value = getErrorMessage(e)
  } finally {
    loadingEditor.value = false
    editorKey.value += 1
  }
}

function closeEditor() {
  editing.value = null
}

async function saveEditor() {
  const file = editing.value
  if (!file) return
  saving.value = true
  editorError.value = ''
  try {
    await forebrainApi.memoryFileWrite(props.scope, file.path, draft.value, props.projectId)
    emit('notice', t('memories.savedFile', { path: file.path }))
    closeEditor()
    emit('reload')
  } catch (e: unknown) {
    editorError.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

async function deleteFromEditor() {
  const file = editing.value
  if (!file) return
  saving.value = true
  editorError.value = ''
  try {
    if (file.core) {
      // A core file can be cleared but never removed.
      await forebrainApi.memoryFileWrite(props.scope, file.path, '', props.projectId)
      emit('notice', t('memories.emptiedFile', { path: file.path }))
    } else {
      const res = await forebrainApi.memoryFilesDelete(props.scope, [file.path], props.projectId)
      const refused = res.results.filter((row) => !row.ok)
      emit(
        'notice',
        refused.length
          ? refused.map((row) => `${row.path}: ${row.error ?? ''}`).join('; ')
          : t('memories.deletedFile', { path: file.path }),
      )
    }
    closeEditor()
    emit('reload')
  } catch (e: unknown) {
    editorError.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

async function deleteOne(file: MemoryFileRecord) {
  try {
    const res = await forebrainApi.memoryFilesDelete(props.scope, [file.path], props.projectId)
    const refused = res.results.filter((row) => !row.ok)
    emit(
      'notice',
      refused.length
        ? refused.map((row) => `${row.path}: ${row.error ?? ''}`).join('; ')
        : t('memories.deletedFile', { path: file.path }),
    )
    emit('reload')
  } catch (e: unknown) {
    emit('error', getErrorMessage(e))
  }
}
</script>
