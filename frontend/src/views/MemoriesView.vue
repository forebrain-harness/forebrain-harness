<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto flex w-full max-w-5xl flex-col gap-4">
      <p v-if="pageError || error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">
        {{ pageError || error }}
      </p>
      <p v-if="notice" class="text-[12px] text-[var(--forebrain-brand-1)]">{{ notice }}</p>

      <ListToolbar>
        <template #left>
          <Input
            v-model="q"
            type="search"
            :placeholder="t('memories.searchPlaceholder')"
            class="forebrain-field h-[34px] w-auto"
            style="max-width: 240px"
            data-testid="memories-search"
          />
        </template>
        <template #actions>
          <span v-if="selected.size" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('memories.selectedCount', { count: selected.size }) }}</span>
          <Button
            type="button"
            class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs text-[var(--forebrain-danger)]"
            :disabled="!selected.size || deleting"
            data-testid="memories-delete-selected"
            @click="deleteSelected"
          >
            <Trash2 class="size-3.5" aria-hidden="true" />{{ t('memories.deleteSelected') }}
          </Button>
          <Button
            type="button"
            class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs text-[var(--forebrain-danger)]"
            data-testid="memories-reset-all"
            @click="confirmReset = true"
          >
            {{ t('memories.resetAll') }}
          </Button>
        </template>
      </ListToolbar>

      <MemoryFilesPanel
        v-model:selected="selected"
        scope="global"
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
      <ListNote>{{ t('memories.note') }}</ListNote>
    </div>

    <ConfirmDialog
      :open="confirmReset"
      :title="t('memories.resetAllTitle')"
      :message="t('memories.resetGlobalHint')"
      :confirm-label="t('memories.resetAllConfirm')"
      danger
      @cancel="confirmReset = false"
      @confirm="resetAll"
    />
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { Trash2 } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import TablePager from '@/components/common/TablePager.vue'
import MemoryFilesPanel from '@/components/memory/MemoryFilesPanel.vue'
import { useServerList } from '@/composables/useServerList'
import { forebrainApi, getErrorMessage, type ListQuery, type MemoryFileRecord } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The primary agent's memory files: the cross-project scope every session of
 * this tenant may recall. The memory on/off switches live in settings; this
 * page is the files themselves.
 *
 * The table is the server's (plan 073): the search and the time column's order
 * are applied there and the pager says how many files there are. The page owns
 * the toolbar and the selection, so a bulk delete and the reset are the page's
 * own writes; the table reports what a row's edit did and asks to be reloaded.
 */
const { t } = useI18n()

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
  filters: { scope: 'global' },
})

/** A header click orders the column the way the server does, from page one. */
function sortBy(key: string, next: 'asc' | 'desc'): void {
  sort.value = key
  order.value = next
}

async function deleteSelected() {
  const paths = Array.from(selected.value)
  if (!paths.length || deleting.value) return
  deleting.value = true
  pageError.value = ''
  try {
    const res = await forebrainApi.memoryFilesDelete('global', paths)
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
    await forebrainApi.resetMemories({ scope: 'global' })
    notice.value = t('memories.resetDone')
    selected.value = new Set()
    await reload()
  } catch (e: unknown) {
    pageError.value = getErrorMessage(e)
  }
}

onMounted(() => {
  void reload()
})
</script>
