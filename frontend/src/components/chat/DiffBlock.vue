<template>
  <!-- The prototype’s .fc-b (app-preview.html :371): the one diff panel every
       card shares — a pale surface, one faint line-number column, a 3px colour
       bar on added and removed rows, one fold mark between hunks. A new file
       (plain) reads as ordinary code, not a wall of green. -->
  <div class="fc-b" data-testid="diff-block">
    <ScrollArea class="diff-scroll" :style="maxHeight == null ? undefined : { maxHeight: `${maxHeight}px` }">
      <div
        v-for="(row, i) in drawnRows"
        :key="i"
        class="dr"
        :class="row.mark === '+' ? 'add' : row.mark === '-' ? 'del' : row.mark === '…' ? 'gap' : undefined"
      >
        <span class="g">{{ row.line ?? '' }}</span>
        <span class="t">
          <span v-if="row.mark === '+'" class="sr-only">{{ t('chat.diffRowAdded') }}</span>
          <span v-else-if="row.mark === '-'" class="sr-only">{{ t('chat.diffRowDeleted') }}</span>
          <template v-if="row.mark === '…'">⋯</template>
          <template v-else>{{ row.text }}</template>
        </span>
      </div>
    </ScrollArea>
  </div>
</template>

<script setup lang="ts">
/**
 * The one diff row component (owner 2026-10-25): every surface that shows a
 * diff draws these rows, so there is exactly one set of diff colours in the
 * app. Neither @repo/elements nor @repo/shadcn-vue ships a diff rows
 * component, so this is written here — the rows are the prototype’s own
 * layout (.dr, :372-379), copied value for value.
 */
import { computed } from 'vue'
import { ScrollArea } from '@repo/shadcn-vue/components/ui/scroll-area'
import { t } from '@/locales'
import type { DiffRow } from '@/lib/unifiedDiff'

const props = withDefaults(defineProps<{
  rows: DiffRow[]
  /** A new file: every row reads as context — no green wash, no bars. */
  plain?: boolean
  /** The panel’s scroll bound; the prototype’s card body scrolls at 360px.
   * Null leaves the panel unbounded, for a surface that scrolls it itself. */
  maxHeight?: number | null
}>(), {
  plain: false,
  maxHeight: 360,
})

// plain re-marks every row as context, the way the prototype’s fileDiff does
// for a created file; the rows keep their line numbers.
const drawnRows = computed(() => (props.plain
  ? props.rows.map((row) => (row.mark === ' ' ? row : { ...row, mark: ' ' as const }))
  : props.rows))
</script>

<style scoped>
/* The prototype’s :is(.shc, .fdiff) .fc-b (:371) and .dr (:372-379). */
.fc-b {
  background: #fbfbfc;
  border-top: 1px solid var(--forebrain-divider);
  padding: 6px 0 10px;
  font: 11.5px/1.65 var(--forebrain-mono-font);
  color: var(--forebrain-text);
  tab-size: 4;
}

.diff-scroll {
  /* The prototype’s .scroll (:345): a long diff scrolls inside its panel. */
  overflow: auto;
  overscroll-behavior: contain;
  scrollbar-width: thin;
}

.dr {
  display: grid;
  grid-template-columns: 52px minmax(0, 1fr);
}

.dr .g {
  padding-right: 14px;
  text-align: right;
  color: #b0b7c3;
  user-select: none;
  font-variant-numeric: tabular-nums;
}

.dr .t {
  min-width: 0;
  padding-right: 14px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.dr.add {
  background: #edfbf2;
  box-shadow: inset 3px 0 0 #32d583;
}

.dr.del {
  background: #fff1f0;
  box-shadow: inset 3px 0 0 #f97066;
}

.dr.del .t {
  color: #7a271a;
}

.dr.gap .t {
  color: #b0b7c3;
}
</style>
