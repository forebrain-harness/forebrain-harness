<script setup lang="ts">
import type { DynamicToolUIPart, ToolUIPart } from 'ai'
import type { HTMLAttributes } from 'vue'
import { CollapsibleTrigger } from '@repo/shadcn-vue/components/ui/collapsible'
import { cn } from '@repo/shadcn-vue/lib/utils'
import { ChevronDownIcon, WrenchIcon } from 'lucide-vue-next'
import { computed } from 'vue'
import StatusBadge from './ToolStatusBadge.vue'

type ToolHeaderProps = {
  title?: string
  /**
   * Keep the title on one line (ellipsis + native title tooltip with the full
   * text) instead of letting a long one wrap the header into a paragraph.
   * Default false: existing callers keep the wrapping behaviour.
   */
  singleLine?: boolean
  class?: HTMLAttributes['class']
} & (
  | { type: ToolUIPart['type'], state: ToolUIPart['state'], toolName?: never }
  | { type: DynamicToolUIPart['type'], state: DynamicToolUIPart['state'], toolName: string }
)

const props = defineProps<ToolHeaderProps>()

const derivedName = computed(() =>
  props.type === 'dynamic-tool'
    ? props.toolName
    : props.type.split('-').slice(1).join('-'),
)
</script>

<template>
  <CollapsibleTrigger
    :class="
      cn(
        'flex w-full items-center justify-between gap-4 p-3',
        props.class,
      )
    "
    v-bind="$attrs"
  >
    <div class="flex min-w-0 items-center gap-2">
      <WrenchIcon class="size-4 shrink-0 text-muted-foreground" />
      <!-- A trigger is a button, which centres its text; a title long enough
           to wrap reads from the left like every other line of the card.
           singleLine swaps the wrap for an ellipsis and carries the full text
           in the native title attribute (a tooltip), not in the title prop. -->
      <span
        :class="cn('min-w-0 text-left font-medium text-sm', props.singleLine ? 'truncate whitespace-nowrap' : 'break-words')"
        :title="props.singleLine ? props.title ?? derivedName : undefined"
      >{{ props.title ?? derivedName }}</span>
      <StatusBadge :state="props.state" />
    </div>
    <ChevronDownIcon
      class="size-4 text-muted-foreground transition-transform group-data-[state=open]:rotate-180"
    />
  </CollapsibleTrigger>
</template>
