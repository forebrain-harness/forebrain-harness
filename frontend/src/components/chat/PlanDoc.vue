<template>
  <div class="plan-doc" data-testid="plan-doc">
    <!-- The strip names the file, when that version was written and how far its
         list got: the three facts the workbench row carried, so opening the
         document never loses the context of which version this is. -->
    <div class="plan-doc-meta">
      <span class="mono plan-doc-path">{{ path }}</span>
      <span>{{ at }}</span>
      <span class="plan-doc-progress">
        <SquareCheck aria-hidden="true" />{{ done }}/{{ total }}
      </span>
    </div>
    <div class="plan-doc-body">
      <MessageResponse :content="markdown" />
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * One plan document as its drawer tab draws it: the plan file is Markdown —
 * headings, checklists, fenced examples, tables — so it is rendered as a
 * document, never printed as one run of preformatted text. The renderer is the
 * repository's own (`@repo/elements/message`), the same one the conversation's
 * cards use.
 */
import { MessageResponse } from '@repo/elements/message'
import { SquareCheck } from 'lucide-vue-next'

defineProps<{
  /** The plan file, relative to the state root. */
  path: string
  /** When this version was written. */
  at: string
  done: number
  total: number
  markdown: string
}>()
</script>

<style scoped>
.plan-doc {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  overflow-y: auto;
}
.plan-doc-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: none;
  padding: 10px 16px;
  border-bottom: 1px solid var(--forebrain-divider);
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
}
.plan-doc-path {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--forebrain-text-2);
}
.plan-doc-progress {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  margin-left: auto;
  flex: none;
  font-variant-numeric: tabular-nums;
}
.plan-doc-progress svg {
  width: 13px;
  height: 13px;
}
.plan-doc-body {
  padding: 12px 16px 24px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--forebrain-text);
}
</style>
