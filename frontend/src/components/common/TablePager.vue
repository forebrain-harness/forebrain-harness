<template>
  <div class="forebrain-pager" role="navigation" :aria-label="t('list.pagerAria')" data-testid="table-pager">
    <span class="forebrain-list-note" data-testid="pager-total">{{ t('list.total', { total }) }}</span>
    <span class="flex-1" />
    <span class="forebrain-pager-size">
      <select
        :value="pageSize"
        :aria-label="t('list.pageSizeAria')"
        data-testid="pager-size"
        @change="onPageSizeChange"
      >
        <option v-for="size in sizeOptions" :key="size" :value="size">{{ t('list.pageSize', { n: size }) }}</option>
      </select>
      <ChevronDown aria-hidden="true" />
    </span>
    <button
      type="button"
      class="forebrain-btn forebrain-btn--outline forebrain-btn--sm"
      data-testid="pager-prev"
      :disabled="current === 1"
      @click="go(current - 1)"
    >
      <ChevronLeft class="size-3.5" aria-hidden="true" />{{ t('list.prev') }}
    </button>
    <template v-for="(number, index) in numbers" :key="`${number}-${index}`">
      <span v-if="number === 0" class="forebrain-pg-gap">…</span>
      <button
        v-else
        type="button"
        class="forebrain-pg-num"
        :class="{ 'is-on': number === current }"
        :aria-current="number === current ? 'page' : undefined"
        :aria-label="t('list.pageAria', { n: number })"
        :data-testid="`pager-page-${number}`"
        :disabled="number === current"
        @click="go(number)"
      >
        {{ number }}
      </button>
    </template>
    <button
      type="button"
      class="forebrain-btn forebrain-btn--outline forebrain-btn--sm"
      data-testid="pager-next"
      :disabled="current === pages"
      @click="go(current + 1)"
    >
      {{ t('list.next') }}<ChevronRight class="size-3.5" aria-hidden="true" />
    </button>
  </div>
</template>

<script setup lang="ts">
/**
 * The pager under every table (plan 071): 共 N 条 on the left, then the page
 * size and the page numbers on the right, all 26px tall. The prototype's page
 * list: every page when there are seven or fewer, otherwise the first, the
 * last, the current one and its neighbours, with … for the stretches left out.
 *
 * A single page still renders a pager — the row also says how many items there
 * are — and a page size outside 10/20/50 joins the options rather than leaving
 * a select with no matching entry.
 */
import { computed } from 'vue'
import { ChevronDown, ChevronLeft, ChevronRight } from 'lucide-vue-next'

import { useI18n } from '@/locales'

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

/** 0 in the page list stands for the … between two stretches of pages. */
const GAP = 0

const pages = computed(() => Math.max(1, Math.ceil(props.total / Math.max(1, props.pageSize))))
const current = computed(() => Math.min(Math.max(1, props.page), pages.value))

const numbers = computed<number[]>(() => {
  const total = pages.value
  const here = current.value
  if (total <= 7) return Array.from({ length: total }, (_, index) => index + 1)
  const list = [1]
  if (here > 3) list.push(GAP)
  for (let number = Math.max(2, here - 1); number <= Math.min(total - 1, here + 1); number++) list.push(number)
  if (here < total - 2) list.push(GAP)
  list.push(total)
  return list
})

const sizeOptions = computed(() =>
  PAGE_SIZES.includes(props.pageSize)
    ? PAGE_SIZES
    : [...PAGE_SIZES, props.pageSize].sort((a, b) => a - b),
)

function go(page: number): void {
  if (page < 1 || page > pages.value || page === current.value) return
  emit('update:page', page)
}

function onPageSizeChange(event: Event): void {
  const value = Number((event.target as HTMLSelectElement).value)
  if (!Number.isFinite(value) || value === props.pageSize) return
  emit('update:pageSize', value)
}
</script>
