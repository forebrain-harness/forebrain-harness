<script setup lang="ts">
import type { ToolUIPart } from 'ai'
import type { HTMLAttributes } from 'vue'
import {
  CollapsibleTrigger,
} from '@repo/shadcn-vue/components/ui/collapsible'
import { cn } from '@repo/shadcn-vue/lib/utils'
import { ChevronDownIcon, Code } from 'lucide-vue-next'
import { computed } from 'vue'
import { ToolStatusBadge } from '../tool'

type CollapsibleTriggerProps = InstanceType<typeof CollapsibleTrigger>['$props']

interface Props extends /* @vue-ignore */ CollapsibleTriggerProps {
  title?: string
  state: ToolUIPart['state']
  /** The state's word in the reader's language, drawn beside the title. */
  statusLabel: string
  class?: HTMLAttributes['class']
}

const props = defineProps<Props>()

/** The card's vocabulary for the state it was handed: a call that produced
 *  output is done, one still going is running, and the rest reads as failed. */
const status = computed(() => {
  switch (props.state) {
    case 'input-streaming':
    case 'input-available':
      return 'running'
    case 'approval-requested':
    case 'approval-responded':
      return 'awaiting'
    case 'output-error':
      return 'error'
    case 'output-denied':
      return 'denied'
    default:
      return 'done'
  }
})
</script>

<template>
  <CollapsibleTrigger
    :class="cn(
      'flex w-full items-center justify-between gap-4 p-3',
      props.class,
    )"
    v-bind="$attrs"
  >
    <div class="flex items-center gap-2">
      <Code class="size-4 text-muted-foreground" />
      <span class="font-medium text-sm">{{ title }}</span>
      <ToolStatusBadge :status="status" :label="props.statusLabel" />
    </div>
    <ChevronDownIcon class="size-4 text-muted-foreground transition-transform group-data-[state=open]:rotate-180" />
  </CollapsibleTrigger>
</template>
