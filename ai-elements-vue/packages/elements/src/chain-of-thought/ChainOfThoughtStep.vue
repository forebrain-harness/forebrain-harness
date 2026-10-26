<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { cn } from '@repo/shadcn-vue/lib/utils'

const props = withDefaults(
  defineProps<{
    /**
     * The step's caption beside the icon column. Optional (plan 136): a step
     * on the activity line carries its card, not a caption of its own.
     */
    label?: string
    description?: string
    status?: 'complete' | 'active' | 'pending'
    class?: HTMLAttributes['class']
  }>(),
  {
    label: undefined,
    status: 'complete',
    description: undefined,
  },
)

const statusStyles = {
  complete: 'text-muted-foreground',
  active: 'text-foreground',
  pending: 'text-muted-foreground/50',
}
</script>

<template>
  <div
    :class="
      cn(
        'flex gap-2 text-sm',
        statusStyles[props.status],
        'fade-in-0 slide-in-from-top-2 animate-in',
        props.class,
      )
    "
    v-bind="$attrs"
  >
    <div class="relative mt-0.5">
      <slot name="icon" />
      <!-- The line this step hangs on, drawn from under the icon to the step's
           own bottom. A parent that chains steps into one line (the activity
           line) finds it by its data-slot and extends it across the gap. -->
      <div
        data-slot="chain-of-thought-line"
        class="-mx-px absolute top-7 bottom-0 left-1/2 w-px bg-border"
      />
    </div>
    <div class="flex-1 space-y-2">
      <div v-if="props.label">{{ props.label }}</div>
      <div
        v-if="props.description"
        class="text-muted-foreground text-xs"
      >
        {{ props.description }}
      </div>
      <slot />
    </div>
  </div>
</template>
