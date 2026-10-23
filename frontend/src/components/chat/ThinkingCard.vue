<template>
  <!-- The prototype's .think, drawn with the library's own reasoning card: a
       caption that folds the prose away, closed until asked. -->
  <Reasoning :default-open="false" class="think" data-testid="thinking-card">
    <ReasoningTrigger class="think-l">
      <ChevronRight class="think-chev" aria-hidden="true" />
      <span>{{ thinkingBlockLabel(props.block, t) }}</span>
    </ReasoningTrigger>
    <ReasoningContent :content="props.block.text.trim()" class="think-body" />
  </Reasoning>
</template>

<script setup lang="ts">
/**
 * The thinking block, as the prototype's .think draws it: one caption line that
 * names the block and how long the model thought, and the reasoning itself only
 * once the reader asks for it.
 *
 * It is the library's Reasoning card (`@repo/elements/reasoning`) with this
 * surface's words and this design's metrics — the fold, the state and the
 * content are the component's, not a second implementation of them.
 */
import { ChevronRight } from 'lucide-vue-next'
import { Reasoning, ReasoningContent, ReasoningTrigger } from '@repo/elements/reasoning'
import { useI18n } from '@/locales'
import { thinkingBlockLabel, type TimelineBlock } from '@/composables/useChatStream'

const props = defineProps<{ block: Extract<TimelineBlock, { kind: 'thinking' }> }>()

const { t } = useI18n()
</script>

<style scoped>
/* The prototype's .think, in this app's tokens: --line is
   --forebrain-divider, --muted is --forebrain-muted-text, --text-2 is
   --forebrain-text-2. */
.think {
  margin-bottom: 0;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  padding: 6px 10px;
}

.think-l {
  width: 100%;
  gap: 6px;
  font-size: 12px;
  color: var(--forebrain-muted-text);
  text-align: left;
}

.think-l:hover {
  color: var(--forebrain-text-2);
}

/* The fold marker: a 14px chevron, turned down when open — the trigger carries
   the state, so the marker needs no JavaScript. (A "▸" glyph at the caption's
   11px was too small to read as a control.) */
.think-chev {
  width: 14px;
  height: 14px;
  flex: none;
  transition: transform 0.15s ease;
}

.think-l[data-state="open"] .think-chev {
  transform: rotate(90deg);
}

/* The body is the reasoning verbatim: 12px at 1.65, muted, its own lines
   kept. */
.think-body {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.65;
  color: var(--forebrain-muted-text);
}

.think-body :deep([data-stream-markdown="paragraph"]) {
  margin-bottom: 6px;
}

.think-body :deep([data-stream-markdown="paragraph"]:last-child) {
  margin-bottom: 0;
}
</style>
