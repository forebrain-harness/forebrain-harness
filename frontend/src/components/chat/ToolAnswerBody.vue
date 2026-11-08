<template>
  <!-- A tool's answer for a reader (pkg/tool ReadableAnswer): its fields as a
       quiet two-column list — keys in the second text colour, every value
       starting on the one column the longest key sets, nested keys indented
       under the field that holds them, a list's items each opening on a small
       dot — then its text, as the Markdown it usually is. Never the JSON the
       answer arrived in. -->
  <div class="tool-answer" data-testid="tool-answer">
    <dl v-if="answer.rows.length" class="rows">
      <div
        v-for="(row, index) in answer.rows"
        :key="index"
        class="row"
        :class="{ bullet: row.bullet, head: row.label && !row.value }"
        :style="{ '--depth': row.depth }"
        data-testid="tool-answer-row"
      >
        <dt v-if="row.label" class="k">{{ row.label }}</dt>
        <dd v-if="row.value" class="v">{{ row.value }}</dd>
      </div>
    </dl>
    <MessageResponse v-if="answer.text" :content="answer.text" />
  </div>
</template>

<script setup lang="ts">
/**
 * The body of a call whose result is its answer itself — a tool with no card
 * of its own, or a row a migrated session recorded. The rows are the engine's
 * (the terminal writes the same rows as "Field: value" lines), so the page
 * only lays them out.
 */
import { MessageResponse } from '@repo/elements/message'
import type { ReadableAnswer } from '@/composables/useChatStream'

defineProps<{ answer: ReadableAnswer }>()
</script>

<style scoped>
/* The card's own body (ToolOutput's .tool-body): the same padding, size and
   bound, so an answer reads as one more tool result, not a new kind of box. */
.tool-answer {
  padding: 9px 11px;
  display: grid;
  gap: 8px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--forebrain-text);
  max-height: 360px;
  overflow: auto;
}

/* One key column for the whole answer, as wide as its widest key with that
   key's indent, so every value starts on the same line whatever its depth. */
.rows {
  margin: 0;
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  column-gap: 16px;
  row-gap: 2px;
}

.row {
  display: contents;
}

.k {
  grid-column: 1;
  position: relative;
  padding-left: calc(var(--depth, 0) * 16px);
  color: var(--forebrain-text-2);
}

/* A key that heads the fields nested under it has no value of its own. */
.row.head .k {
  font-weight: 500;
}

/* A list item's first key sits one level in, its small dot hanging in the
   indent, so the item's fields line up under one another. */
.row.bullet .k {
  padding-left: calc((var(--depth, 0) + 1) * 16px);
}

.row.bullet .k::before {
  content: '';
  position: absolute;
  left: calc(var(--depth, 0) * 16px + 4px);
  top: calc(0.8em - 2px);
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background: var(--forebrain-divider-strong);
}

.v {
  grid-column: 2;
  margin: 0;
  min-width: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
