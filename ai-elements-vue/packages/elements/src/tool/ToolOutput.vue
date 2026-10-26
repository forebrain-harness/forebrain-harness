<script setup lang="ts">
import type { DynamicToolUIPart, ToolUIPart } from 'ai'
import type { HTMLAttributes } from 'vue'
import { cn } from '@repo/shadcn-vue/lib/utils'
import { computed, isVNode } from 'vue'
import { CodeBlock } from '../code-block'
import { MessageResponse } from '../message'

export type ToolPart = ToolUIPart | DynamicToolUIPart

interface Props extends /* @vue-ignore */ HTMLAttributes {
  output: ToolPart['output']
  errorText: ToolPart['errorText']
  class?: HTMLAttributes['class']
}

const props = defineProps<Props>()

const showOutput = computed(() => props.output || props.errorText)

const isObjectOutput = computed(
  () => typeof props.output === 'object' && !isVNode(props.output),
)

/**
 * A string output is the runtime's own display body: Markdown, formatted by the
 * same code that draws the terminal, and the only reason the two surfaces agree
 * about what a tool said. It is rendered as Markdown rather than dropped into a
 * JSON code block, which showed the reader the source — literal `**bold**`
 * around a search hit, bare `>` in front of quoted evidence, unrendered fences
 * around shell output.
 *
 * Structured output has no display body to render and stays JSON.
 */
const isStringOutput = computed(() => typeof props.output === 'string')

const formattedOutput = computed(() => {
  if (isObjectOutput.value) {
    return JSON.stringify(props.output, null, 2)
  }
  return props.output as string
})
</script>

<template>
  <!-- The prototype's .tool-b: the call's own result, one column, the card's
       own padding, never a second frame around the content. A failed call is
       told apart by its header's state word and by the text's colour — a
       "Result"/"Error" heading would only repeat what the header says. -->
  <div
    v-if="showOutput"
    :class="cn('tool-body', props.class)"
    v-bind="$attrs"
  >
    <!-- Error text -->
    <div v-if="errorText" class="tool-body-error">
      {{ props.errorText }}
    </div>

    <!-- Output rendering based on type -->
    <template v-else>
      <CodeBlock
        v-if="isObjectOutput"
        :code="formattedOutput"
        language="json"
      />
      <MessageResponse
        v-else-if="isStringOutput"
        :content="formattedOutput"
      />
      <div v-else>
        {{ props.output }}
      </div>
    </template>
  </div>
</template>

<style scoped>
.tool-body {
  padding: 9px 11px;
  display: grid;
  gap: 8px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--forebrain-text);
  /* A long result is read in place: bounded, and scrolled rather than cut.
     The bound is the prototype's one panel height (app-preview.html :345),
     the same height a shell card's output panel scrolls at (plan 137). */
  max-height: 360px;
  overflow: auto;
}

.tool-body :deep(> :first-child) {
  margin-top: 0;
}

.tool-body :deep(> :last-child) {
  margin-bottom: 0;
}

/* The body is the one vertical scroller (208px, above): a second bound on the
   code block gave a box inside a box, each with its own bar. The block scrolls
   sideways only, by itself. */
.tool-body :deep(pre) {
  border-radius: 6px;
}

.tool-body-error {
  color: var(--forebrain-danger);
}
</style>
