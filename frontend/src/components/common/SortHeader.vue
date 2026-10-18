<template>
  <th class="forebrain-sortable" :class="{ 'is-on': active }" :aria-sort="ariaSort">
    <button type="button" class="forebrain-th-sort" v-tip:top="tip" @click="toggle">
      {{ label }}
      <svg class="forebrain-sort-ic" viewBox="0 0 8 12" aria-hidden="true">
        <path :class="{ 'is-act': active && order === 'asc' }" d="M4 1 7.5 5h-7z" />
        <path :class="{ 'is-act': active && order === 'desc' }" d="M4 11 .5 7h7z" />
      </svg>
    </button>
  </th>
</template>

<script setup lang="ts">
/**
 * One sortable column header (plan 071): the label plus the stacked carets, the
 * column being sorted now named by `is-on` and its direction by the brand caret.
 * The first click sorts by this column's own first direction (times newest
 * first, names A→Z); clicking again turns the order around. The hint says what
 * the next click does.
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

const ariaSort = computed(() => (props.active ? (props.order === 'desc' ? 'descending' : 'ascending') : 'none'))

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
