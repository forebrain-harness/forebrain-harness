<script setup lang="ts">
import type { Component, HTMLAttributes } from 'vue'
import { CollapsibleTrigger } from '@repo/shadcn-vue/components/ui/collapsible'
import { cn } from '@repo/shadcn-vue/lib/utils'
import { ChevronDownIcon, WrenchIcon } from 'lucide-vue-next'
import StatusBadge from './ToolStatusBadge.vue'

type ToolHeaderProps = {
  title?: string
  /**
   * Keep the title on one line (ellipsis + native title tooltip with the full
   * text) instead of letting a long one wrap the header into a paragraph.
   * Default false: existing callers keep the wrapping behaviour.
   */
  singleLine?: boolean
  /** The call's own mark, matching the tool it invoked (prototype TOOL_ICON). */
  icon?: Component
  /** The call's state, in this surface's own vocabulary. */
  status: 'running' | 'awaiting' | 'done' | 'denied' | 'error'
  /** What that state is called in the reader's language. */
  statusLabel: string
  class?: HTMLAttributes['class']
}

const props = defineProps<ToolHeaderProps>()
</script>

<template>
  <!-- The prototype's .tool-h: one line, always — the tool's mark, the engine's
       own summary of the call, the state and the fold. The padding, the alt
       surface and the hairline under it belong to the header, so the card's
       body reads as one column beneath it. -->
  <CollapsibleTrigger :class="cn('tool-head', props.class)" v-bind="$attrs">
    <component :is="props.icon ?? WrenchIcon" class="tool-head-icon" aria-hidden="true" />
    <!-- A trigger is a button, which centres its text; the summary reads from
         the left like every other line of the card. singleLine swaps the wrap
         for an ellipsis and carries the full text in the native title
         attribute (a tooltip), not in the title prop. -->
    <span
      class="tool-head-cmd"
      :class="props.singleLine ? 'truncate' : 'break-words'"
      :title="props.singleLine ? props.title : undefined"
    >{{ props.title }}</span>
    <StatusBadge :status="props.status" :label="props.statusLabel" />
    <ChevronDownIcon class="tool-head-chev transition-transform group-data-[state=open]:rotate-180" aria-hidden="true" />
  </CollapsibleTrigger>
</template>

<style scoped>
.tool-head {
  display: flex;
  width: 100%;
  align-items: center;
  gap: 8px;
  padding: 8px 11px;
  background: var(--forebrain-bg-alt);
  border-bottom: 1px solid var(--forebrain-divider);
  font-size: 12px;
  text-align: left;
}

.tool-head-icon {
  width: 14px;
  height: 14px;
  flex: none;
  color: var(--forebrain-muted-text);
}

.tool-head-cmd {
  flex: 1;
  min-width: 0;
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
  color: var(--forebrain-text-2);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.tool-head-chev {
  width: 14px;
  height: 14px;
  flex: none;
  color: var(--forebrain-muted-text);
}
</style>
