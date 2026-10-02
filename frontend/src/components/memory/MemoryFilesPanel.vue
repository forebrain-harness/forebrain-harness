<template>
  <div class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)]">
    <div class="flex flex-wrap items-center gap-2 border-b border-[var(--forebrain-divider)] px-4 py-3">
      <input
        v-model="searchDraft"
        type="search"
        class="min-w-[180px] flex-1 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]"
        :placeholder="t('memories.searchPlaceholder')"
        data-testid="memories-search"
      />
      <select
        v-model="sortKey"
        class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[12px] text-[var(--forebrain-text)] outline-none"
        data-testid="memories-sort"
      >
        <option value="updated-desc">{{ t('memories.sortUpdatedDesc') }}</option>
        <option value="updated-asc">{{ t('memories.sortUpdatedAsc') }}</option>
        <option value="created-desc">{{ t('memories.sortCreatedDesc') }}</option>
        <option value="created-asc">{{ t('memories.sortCreatedAsc') }}</option>
      </select>
      <select
        v-model.number="pageSize"
        class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[12px] text-[var(--forebrain-text)] outline-none"
      >
        <option :value="20">20</option>
        <option :value="50">50</option>
      </select>
      <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
        {{ loading ? t('common.loading') : t('common.refresh') }}
      </button>
      <button
        type="button"
        class="rounded-xl border border-[var(--forebrain-danger)] px-3 py-2 text-[12px] font-medium text-[var(--forebrain-danger)] hover:bg-[var(--forebrain-input-hover-bg)]"
        data-testid="memories-clear-all"
        @click="confirmClear = true"
      >
        {{ t('memories.clearAll') }}
      </button>
    </div>

    <p v-if="error" class="px-4 pt-3 text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>
    <p v-else-if="notice" class="px-4 pt-3 text-[12px] text-[var(--forebrain-brand-1)]">{{ notice }}</p>

    <div v-if="loading" class="px-4 py-10 text-center text-[13px] text-[var(--forebrain-muted-text)]">
      {{ t('common.loading') }}
    </div>
    <div v-else-if="!files.length" class="px-4 py-10 text-center text-[13px] text-[var(--forebrain-muted-text)]">
      {{ t('memories.emptyFiles') }}
    </div>
    <template v-else>
      <div v-if="selected.size" class="flex items-center justify-between border-b border-[var(--forebrain-divider)] bg-[var(--forebrain-brand-soft)] px-4 py-2">
        <span class="text-[12px] text-[var(--forebrain-brand-1)]">{{ t('memories.selectedCount', { count: selected.size }) }}</span>
        <button
          type="button"
          class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-2.5 py-1 text-[11px] text-[var(--forebrain-danger)]"
          data-testid="memories-delete-selected"
          @click="deleteSelected"
        >
          {{ t('memories.deleteSelected', { count: selected.size }) }}
        </button>
      </div>
      <ul class="divide-y divide-[var(--forebrain-divider)]">
        <li
          v-for="file in files"
          :key="file.path"
          class="flex flex-wrap items-center gap-3 px-4 py-2.5"
          :data-memory-file="file.path"
        >
          <label class="flex shrink-0 cursor-pointer items-center">
            <input
              type="checkbox"
              class="h-4 w-4 rounded border-[var(--forebrain-divider-strong)] text-[var(--forebrain-brand-1)] focus:ring-[var(--forebrain-brand-1)]"
              :checked="selected.has(file.path)"
              @change="toggleSelect(file.path, ($event.target as HTMLInputElement).checked)"
            />
            <span class="sr-only">{{ t('memories.selectFile', { path: file.path }) }}</span>
          </label>
          <div class="min-w-0 flex-1">
            <div class="flex flex-wrap items-center gap-2">
              <span class="font-mono text-[12px] break-all text-[var(--forebrain-text)]">{{ file.path }}</span>
              <LockIcon
                v-if="file.core"
                class="h-3.5 w-3.5 text-[var(--forebrain-muted-text)]"
                :title="t('memories.coreFileHint')"
                :aria-label="t('memories.coreFileHint')"
              />
            </div>
            <div class="mt-0.5 text-[11px] text-[var(--forebrain-muted-text)]">
              {{ t('memories.fileMeta', { size: file.sizeBytes, created: formatTime(file.createdAt), updated: formatTime(file.updatedAt) }) }}
            </div>
          </div>
          <div class="flex shrink-0 gap-2">
            <button
              type="button"
              class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-2.5 py-1 text-[11px] text-[var(--forebrain-text)] hover:bg-[var(--forebrain-surface)]"
              :data-testid="`memories-edit-${file.path}`"
              @click="openEditor(file)"
            >
              {{ t('common.view') }} / {{ t('common.edit') }}
            </button>
            <button
              v-if="!file.core"
              type="button"
              class="rounded-lg border border-[var(--forebrain-divider)] px-2.5 py-1 text-[11px] text-[var(--forebrain-danger)] hover:bg-[var(--forebrain-input-hover-bg)]"
              :data-testid="`memories-delete-${file.path}`"
              @click="deleteOne(file)"
            >
              {{ t('common.delete') }}
            </button>
          </div>
        </li>
      </ul>
      <div class="flex items-center justify-between border-t border-[var(--forebrain-divider)] px-4 py-2 text-[12px] text-[var(--forebrain-muted-text)]">
        <span>{{ t('memories.totalCount', { count: total }) }}</span>
        <div class="flex items-center gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="page <= 1 || loading" data-testid="memories-prev-page" @click="page--; load()">
            {{ t('memories.prevPage') }}
          </button>
          <span data-testid="memories-page">{{ page }} / {{ totalPages }}</span>
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="page >= totalPages || loading" data-testid="memories-next-page" @click="page++; load()">
            {{ t('memories.nextPage') }}
          </button>
        </div>
      </div>
    </template>

    <!-- Editor -->
    <div
      v-if="editing"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4"
      data-testid="memories-editor"
      @click.self="closeEditor"
    >
      <div class="flex max-h-[85vh] w-full max-w-3xl flex-col rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-5 shadow-lg">
        <div class="flex items-center gap-2">
          <div class="font-mono text-[13px] font-medium break-all text-[var(--forebrain-text)]">{{ editing.path }}</div>
          <LockIcon v-if="editing.core" class="h-3.5 w-3.5 text-[var(--forebrain-muted-text)]" :title="t('memories.coreFileHint')" />
        </div>
        <p v-if="editing.core" class="mt-1 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('memories.coreFileHint') }}</p>
        <textarea
          v-model="draft"
          :disabled="loadingEditor"
          rows="18"
          class="mt-3 min-h-[240px] flex-1 resize-y rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-code-bg)] p-3 font-mono text-[12px] leading-relaxed text-[var(--forebrain-code-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]"
          data-testid="memories-editor-text"
        />
        <p v-if="editorError" class="mt-2 text-[12px] text-[var(--forebrain-danger)]">{{ editorError }}</p>
        <div class="mt-4 flex justify-end gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="closeEditor">{{ t('common.cancel') }}</button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving" data-testid="memories-editor-save" @click="saveEditor">
            {{ saving ? t('common.loading') : t('common.save') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Clear-all confirm -->
    <div
      v-if="confirmClear"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4"
      data-testid="memories-clear-confirm"
      @click.self="confirmClear = false"
    >
      <div class="w-full max-w-md rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-5 shadow-lg">
        <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('memories.clearAllTitle') }}</div>
        <p class="mt-2 text-[12px] leading-relaxed text-[var(--forebrain-text-2)]">
          {{ scope === 'global' ? t('memories.clearGlobalHint') : t('memories.clearProjectHint') }}
        </p>
        <div class="mt-4 flex justify-end gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="confirmClear = false">{{ t('common.cancel') }}</button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="clearing" data-testid="memories-clear-confirm-ok" @click="clearAll">
            {{ clearing ? t('common.loading') : t('memories.clearAllConfirm') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { LockIcon } from 'lucide-vue-next'
import forebrainApi, { getErrorMessage, type MemoryFileRecord } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The memory file panel both memory surfaces share: the primary agent's page
 * (scope=global) and the project space tab (scope=project). Core files are
 * editable but never deletable; clearing goes through the one reset endpoint
 * (decision D10) with the scope this panel was opened for.
 */
const props = defineProps<{
  scope: 'global' | 'project'
  projectId?: string
}>()

const { t, locale } = useI18n()

const files = ref<MemoryFileRecord[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const sortKey = ref('updated-desc')
const searchDraft = ref('')
const search = ref('')
const loading = ref(false)
const saving = ref(false)
const clearing = ref(false)
const error = ref('')
const notice = ref('')
const selected = ref(new Set<string>())
const editing = ref<MemoryFileRecord | null>(null)
const draft = ref('')
// The editor stays read-only until the file's current bytes arrive: editing
// into a stale draft would silently save the old content over the user's.
const loadingEditor = ref(false)
const editorError = ref('')
const confirmClear = ref(false)

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize.value)))

let searchDebounce: ReturnType<typeof setTimeout> | null = null
watch(searchDraft, (value) => {
  if (searchDebounce) clearTimeout(searchDebounce)
  searchDebounce = setTimeout(() => {
    search.value = value.trim()
    page.value = 1
    void load()
  }, 300)
})
onBeforeUnmount(() => {
  if (searchDebounce) clearTimeout(searchDebounce)
})

watch([sortKey, pageSize], () => {
  page.value = 1
  void load()
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [sort, order] = sortKey.value.split('-') as ['created' | 'updated', 'asc' | 'desc']
    const data = await forebrainApi.memoryFilesList({
      scope: props.scope,
      projectId: props.projectId,
      page: page.value,
      pageSize: pageSize.value,
      sort,
      order,
      q: search.value || undefined,
    })
    files.value = data.files
    total.value = data.total
    // A page that no longer exists (after deletes) falls back to the last
    // live one instead of showing an empty listing.
    if (!data.files.length && data.page > 1 && data.total > 0) {
      page.value = Math.ceil(data.total / data.pageSize)
      return load()
    }
    const live = new Set(data.files.map((file) => file.path))
    const next = new Set<string>()
    for (const path of selected.value) {
      if (live.has(path)) next.add(path)
    }
    selected.value = next
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

function formatTime(unix: number): string {
  if (!unix) return '—'
  return new Date(unix * 1000).toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}

function toggleSelect(path: string, checked: boolean) {
  const next = new Set(selected.value)
  if (checked) {
    next.add(path)
  } else {
    next.delete(path)
  }
  selected.value = next
}

async function openEditor(file: MemoryFileRecord) {
  editing.value = file
  draft.value = ''
  editorError.value = ''
  loadingEditor.value = true
  try {
    const data = await forebrainApi.memoryFileRead(props.scope, file.path, props.projectId)
    draft.value = data.content
  } catch (e: unknown) {
    editorError.value = getErrorMessage(e)
  } finally {
    loadingEditor.value = false
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
    notice.value = t('memories.savedFile', { path: file.path })
    closeEditor()
    await load()
  } catch (e: unknown) {
    editorError.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

async function deleteOne(file: MemoryFileRecord) {
  error.value = ''
  try {
    const res = await forebrainApi.memoryFilesDelete(props.scope, [file.path], props.projectId)
    const refused = res.results.filter((row) => !row.ok)
    if (refused.length) {
      notice.value = refused.map((row) => `${row.path}: ${row.error ?? ''}`).join('; ')
    } else {
      notice.value = t('memories.deletedFile', { path: file.path })
    }
    await load()
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

async function deleteSelected() {
  const paths = Array.from(selected.value)
  if (!paths.length) return
  error.value = ''
  try {
    const res = await forebrainApi.memoryFilesDelete(props.scope, paths, props.projectId)
    const refused = res.results.filter((row) => !row.ok)
    notice.value = refused.length
      ? t('memories.deletedWithRefusal', { deleted: res.deleted, refused: refused.length })
      : t('memories.deletedSelected', { count: res.deleted })
    await load()
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

async function clearAll() {
  clearing.value = true
  error.value = ''
  try {
    await forebrainApi.resetMemories(
      props.scope === 'global' ? { scope: 'global' } : { scope: 'project', projectId: props.projectId },
    )
    notice.value = t('memories.cleared')
    confirmClear.value = false
    page.value = 1
    await load()
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    clearing.value = false
  }
}

onMounted(load)
</script>
