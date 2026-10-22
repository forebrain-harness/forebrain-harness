<template>
  <div class="forebrain-pager" data-testid="table-pager">
    <span class="forebrain-list-note" data-testid="pager-total">{{ t('list.total', { total }) }}</span>
    <span class="flex-1" />
    <span class="forebrain-pager-size">
      <Select :model-value="String(pageSize)" @update:model-value="onPageSizeChange">
        <SelectTrigger
          class="forebrain-field !h-[26px]"
          :aria-label="t('list.pageSizeAria')"
          data-testid="pager-size"
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem v-for="size in sizeOptions" :key="size" :value="String(size)">
            {{ t('list.pageSize', { n: size }) }}
          </SelectItem>
        </SelectContent>
      </Select>
    </span>
    <Pagination
      :page="current"
      :items-per-page="pageSize"
      :total="total"
      :sibling-count="1"
      show-edges
      :aria-label="t('list.pagerAria')"
      class="forebrain-pagination"
      @update:page="go"
    >
      <PaginationContent v-slot="{ items }" class="forebrain-pagination-content">
        <PaginationPrevious class="forebrain-pg-step" data-testid="pager-prev" :aria-label="t('list.prev')">
          <ChevronLeft class="size-3.5" aria-hidden="true" />{{ t('list.prev') }}
        </PaginationPrevious>
        <template v-for="(item, index) in items as PageItem[]" :key="`${item.type}-${index}`">
          <PaginationEllipsis v-if="item.type === 'ellipsis'" class="forebrain-pg-gap">…</PaginationEllipsis>
          <PaginationItem
            v-else
            :value="item.value"
            :is-active="item.value === current"
            class="forebrain-pg-num"
            :class="{ 'is-on': item.value === current }"
            :data-testid="`pager-page-${item.value}`"
            :aria-label="t('list.pageAria', { n: item.value })"
          >
            {{ item.value }}
          </PaginationItem>
        </template>
        <PaginationNext class="forebrain-pg-step" data-testid="pager-next" :aria-label="t('list.next')">
          {{ t('list.next') }}<ChevronRight class="size-3.5" aria-hidden="true" />
        </PaginationNext>
      </PaginationContent>
    </Pagination>
  </div>
</template>

<script setup lang="ts">
/**
 * The pager under every table (plan 071): 共 N 条 on the left, then the page
 * size and the page numbers on the right, all 26px tall.
 *
 * The pagination itself is shadcn-vue's (reka-ui underneath): the page range —
 * every page when there are seven or fewer, otherwise the first, the last, the
 * current one and one neighbour each side with … for the stretches left out —
 * and the two step buttons' disabled states are the component's, not this
 * file's. What is ours is the wording, the 26px look from the design and the
 * page-size select beside it, which is the library's Select like every other
 * dropdown in the app (plan 096).
 *
 * A single page still renders a pager — the row also says how many items there
 * are — and a page size outside 10/20/50 joins the options rather than leaving
 * a select with no matching entry. The sizes are numbers and the component's
 * values are strings, so `String` on the way in and `Number` on the way out —
 * no sentinel needed, a size is never empty.
 */
import { computed } from 'vue'
import { ChevronLeft, ChevronRight } from 'lucide-vue-next'
import {
  Pagination,
  PaginationContent,
  PaginationEllipsis,
  PaginationItem,
  PaginationNext,
  PaginationPrevious,
} from '@repo/shadcn-vue/components/ui/pagination'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'

import { useI18n } from '@/locales'

/** One entry of the page range: a page number, or the gap between two runs. */
type PageItem = { type: 'page'; value: number } | { type: 'ellipsis' }

const props = defineProps<{
  total: number
  page: number
  pageSize: number
}>()

const emit = defineEmits<{
  'update:page': [page: number]
  'update:pageSize': [pageSize: number]
}>()

const { t } = useI18n()

const PAGE_SIZES = [10, 20, 50]

const current = computed(() => {
  const pages = Math.max(1, Math.ceil(props.total / Math.max(1, props.pageSize)))
  return Math.min(Math.max(1, props.page), pages)
})

const sizeOptions = computed(() =>
  PAGE_SIZES.includes(props.pageSize)
    ? PAGE_SIZES
    : [...PAGE_SIZES, props.pageSize].sort((a, b) => a - b),
)

function go(page: number): void {
  if (page < 1 || page === current.value) return
  emit('update:page', page)
}

function onPageSizeChange(value: unknown): void {
  const size = Number(value)
  if (!Number.isFinite(size) || size === props.pageSize) return
  emit('update:pageSize', size)
}
</script>
