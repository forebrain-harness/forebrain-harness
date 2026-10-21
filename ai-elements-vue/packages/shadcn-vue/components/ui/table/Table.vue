<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { cn } from '@repo/shadcn-vue/lib/utils'

const props = defineProps<{
  class?: HTMLAttributes['class']
  /** Inline style for the `<table>` itself; `style` on the component lands on the container. */
  tableStyle?: HTMLAttributes['style']
  /**
   * Classes for the scroll container around the table. A boxed list puts its
   * border, radius and overflow here: the container is the box, the table is
   * what scrolls inside it.
   */
  containerClass?: HTMLAttributes['class']
}>()
</script>

<template>
  <div data-slot="table-container" :class="cn('relative w-full overflow-auto', props.containerClass)">
    <table data-slot="table" :class="cn('w-full caption-bottom text-sm', props.class)" :style="props.tableStyle">
      <slot />
    </table>
  </div>
</template>
