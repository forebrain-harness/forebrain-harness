<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { cn } from '@repo/shadcn-vue/lib/utils'
import { computed, useAttrs, useSlots } from 'vue'
import { Markdown, type NodeRenderers } from 'vue-stream-markdown'
import { markdownShikiOptions } from '../code-block/theme'
import { fileLineRenderers } from './fileLineLinks'
import 'vue-stream-markdown/index.css'

interface Props {
  content?: string
  class?: HTMLAttributes['class']
}

const props = defineProps<Props>()

const attrs = useAttrs()
const slots = useSlots()
const slotContent = computed<string | undefined>(() => {
  const nodes = slots.default?.()
  if (!Array.isArray(nodes)) {
    return undefined
  }
  let text = ''
  for (const node of nodes) {
    if (typeof node.children === 'string')
      text += node.children
  }
  return text || undefined
})

const md = computed(() => (slotContent.value ?? props.content ?? '') as string)

// Prose file:line-range links, enabled here once for every consumer: a
// reference becomes a link wherever the page provides openFileLineKey, and
// stays plain text where nothing does. A consumer's own renderers win per key.
const fileLine = fileLineRenderers()
const nodeRenderers = computed<NodeRenderers>(() => ({
  ...fileLine,
  ...(attrs.nodeRenderers as NodeRenderers | undefined),
}))
</script>

<template>
  <Markdown
    :content="md"
    :shiki-options="markdownShikiOptions"
    :class="
      cn(
        'size-full [&>*:first-child]:mt-0! [&>*:last-child]:mb-0!',
        props.class,
      )
    "
    v-bind="$attrs"
    :node-renderers="nodeRenderers"
  />
</template>

<style scoped>
/* The prose file reference (plan file-line-range-links): the path ink every
   scheme already states, a hover that says it opens, and a focus ring for the
   keyboard. It sits inline in the sentence, never a block chip. */
:deep([data-stream-markdown='file-line']) {
  color: var(--path-ink, #1f4a9e);
  cursor: pointer;
  border-radius: 3px;
  text-decoration: none;
}

:deep([data-stream-markdown='file-line']:hover) {
  background: var(--forebrain-brand-soft, #ebf0f9);
  text-decoration: underline;
}

:deep([data-stream-markdown='file-line']:focus-visible) {
  outline: 2px solid var(--forebrain-brand-1, #1f4a9e);
  outline-offset: 1px;
}
</style>
