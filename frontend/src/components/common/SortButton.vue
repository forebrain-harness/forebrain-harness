<template>
  <button type="button" class="forebrain-th-sort" v-tip:top="tip" @click="toggle">
    {{ label }}
    <svg class="forebrain-sort-ic" viewBox="0 0 8 12" aria-hidden="true">
      <path :class="{ 'is-act': active && order === 'asc' }" d="M4 1 7.5 5h-7z" />
      <path :class="{ 'is-act': active && order === 'desc' }" d="M4 11 .5 7h7z" />
    </svg>
  </button>
</template>

<script setup lang="ts">
/**
 * The sort control a sortable column header draws (plan 071): the label plus the
 * stacked carets, and a hint naming what the next click does. The first click
 * sorts by this column's own first direction (times newest first, names A→Z);
 * clicking again turns the order around.
 *
 * The cell around it belongs to `DataTable`, which draws its own header for each
 * column and puts this control in the ones the caller opened for sorting.
 */
import { computed } from 'vue'

import { useI18n } from '@/locales'

const props = defineProps<{
  label: string
  sortKey: string
  active: boolean
  order: 'asc' | 'desc'
  first: 'asc' | 'desc'
}>()

const emit = defineEmits<{ sort: [key: string, order: 'asc' | 'desc'] }>()

const { t } = useI18n()

/** How this column speaks about a direction: A→Z/Z→A, or 新→旧/旧→新. */
function direction(order: 'asc' | 'desc'): string {
  if (props.first === 'asc') return order === 'asc' ? t('list.dirAZ') : t('list.dirZA')
  return order === 'desc' ? t('list.dirNewOld') : t('list.dirOldNew')
}

const tip = computed(() => {
  if (!props.active) return t('list.sortFirst', { label: props.label, dir: direction(props.first) })
  return props.order === 'desc'
    ? t('list.sortAsc', { dir: direction('asc') })
    : t('list.sortDesc', { dir: direction('desc') })
})

function toggle(): void {
  const next: 'asc' | 'desc' = props.active ? (props.order === 'desc' ? 'asc' : 'desc') : props.first
  emit('sort', props.sortKey, next)
}
</script>
