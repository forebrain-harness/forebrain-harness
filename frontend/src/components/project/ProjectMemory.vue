<template>
  <div class="mx-auto max-w-4xl space-y-4">
    <!-- A project with no project key shares no memory directory of its own;
         there is nothing to manage here. -->
    <div v-if="!project" class="text-[13px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div v-else-if="!hasScope" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-6 py-10 text-center">
      <p class="text-[13px] text-[var(--forebrain-muted-text)]">{{ t('memories.noProjectScope') }}</p>
    </div>
    <template v-else>
      <p v-if="pageError || error" class="text-[12px] text-[var(--forebrain-danger)]">{{ pageError || error }}</p>
      <p v-if="notice" class="text-[12px] text-[var(--forebrain-brand-1)]">{{ notice }}</p>

      <ListToolbar>
        <template #left>
          <Input
            v-model="q"
            type="search"
            :placeholder="t('memories.searchPlaceholder')"
            class="forebrain-field h-[34px] w-auto"
            style="max-width: 240px"
            data-testid="project-memories-search"
          />
        </template>
        <template #actions>
          <span v-if="selected.size" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('memories.selectedCount', { count: selected.size }) }}</span>
          <Button
            type="button"
            class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs text-[var(--forebrain-danger)]"
            :disabled="!selected.size || deleting"
            data-testid="project-memories-delete-selected"
            @click="deleteSelected"
          >
            <Trash2 class="size-3.5" aria-hidden="true" />{{ t('memories.deleteSelected') }}
          </Button>
          <Button
            type="button"
            class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs text-[var(--forebrain-danger)]"
            data-testid="project-memories-reset-all"
            @click="confirmReset = true"
          >
            {{ t('memories.resetAll') }}
          </Button>
        </template>
      </ListToolbar>

      <MemoryFilesPanel
        v-model:selected="selected"
        scope="project"
        :project-id="projectId"
        :rows="rows"
        :loading="loading"
        :sort="sort"
        :order="order"
        @sort="sortBy"
        @reload="reload"
        @notice="notice = $event"
        @error="pageError = $event"
      />

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      <ListNote>{{ t('memories.projectNote') }}</ListNote>
    </template>

    <ConfirmDialog
      :open="confirmReset"
      :title="t('memories.resetAllTitle')"
      :message="t('memories.resetProjectHint')"
      :confirm-label="t('memories.resetAllConfirm')"
      danger
      @cancel="confirmReset = false"
      @confirm="resetAll"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Trash2 } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import TablePager from '@/components/common/TablePager.vue'
import MemoryFilesPanel from '@/components/memory/MemoryFilesPanel.vue'
import { useServerList } from '@/composables/useServerList'
import { forebrainApi, getErrorMessage, type ListQuery, type MemoryFileRecord, type ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The project's own memory scope: exactly the files this project's sessions
 * recall, never another project's. Projects without their own key have no
 * separate scope and say so instead of listing a neighbour's. The key comes
 * from the project row the shell already loaded (and whose load errors the
 * shell reports).
 *
 * The table and its pager are the shared memory panel's; this tab keeps the
 * list's own state (search, order, selection, the bulk writes) so the panel can
 * stay one page of rows. The trust gate, the memory scope and the project's own
 * settings live elsewhere (plan 075): this tab is the file table and its note.
 */
const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()

const { t } = useI18n()

const hasScope = computed(() => Boolean(props.project?.projectKey))

const selected = ref(new Set<string>())
const notice = ref('')
const pageError = ref('')
const deleting = ref(false)
const confirmReset = ref(false)

/** The rows on the page, minus a selection that had to catch up with them. */
async function fetchFiles(query: ListQuery) {
  const answer = await forebrainApi.memoryFilesPage(query)
  const live = new Set(answer.rows.map((row) => row.path))
  const kept = Array.from(selected.value).filter((path) => live.has(path))
  if (kept.length !== selected.value.size) selected.value = new Set(kept)
  return answer
}

const { rows, total, page, pageSize, q, sort, order, loading, error, reload } = useServerList<MemoryFileRecord>(fetchFiles, {
  pageSize: 10,
  sort: 'updated',
  order: 'desc',
  filters: { scope: 'project', projectId: props.projectId },
})

/** A header click orders the column the way the server does, from page one. */
function sortBy(key: string, next: 'asc' | 'desc'): void {
  sort.value = key
  order.value = next
}

// The listing is only asked once the project is known to have a scope.
watch(
  hasScope,
  (ready) => {
    if (ready) void reload()
  },
  { immediate: true },
)

async function deleteSelected() {
  const paths = Array.from(selected.value)
  if (!paths.length || deleting.value) return
  deleting.value = true
  pageError.value = ''
  try {
    const res = await forebrainApi.memoryFilesDelete('project', paths, props.projectId)
    const refused = res.results.filter((row) => !row.ok)
    notice.value = refused.length
      ? t('memories.deletedWithRefusal', { deleted: res.deleted, refused: refused.length })
      : t('memories.deletedSelected', { count: res.deleted })
    selected.value = new Set()
    await reload()
  } catch (e: unknown) {
    pageError.value = getErrorMessage(e)
  } finally {
    deleting.value = false
  }
}

async function resetAll() {
  confirmReset.value = false
  pageError.value = ''
  try {
    await forebrainApi.resetMemories({ scope: 'project', projectId: props.projectId })
    notice.value = t('memories.resetDone')
    selected.value = new Set()
    await reload()
  } catch (e: unknown) {
    pageError.value = getErrorMessage(e)
  }
}
</script>
