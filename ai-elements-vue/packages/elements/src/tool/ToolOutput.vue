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
  <div
    v-if="showOutput"
    :class="cn('space-y-2 p-4', props.class)"
    v-bind="$attrs"
  >
    <h4
      class="font-medium text-muted-foreground text-xs uppercase tracking-wide"
    >
      {{ props.errorText ? "Error" : "Result" }}
    </h4>
    <div
      :class="
        cn(
          'overflow-x-auto rounded-md text-xs [&_table]:w-full',
          props.errorText
            ? 'bg-destructive/10 text-destructive'
            : 'bg-muted/50 text-foreground',
        )
      "
    >
      <!-- Error text -->
      <div v-if="errorText">
        {{ props.errorText }}
      </div>

      <!-- Output rendering based on type -->
      <CodeBlock
        v-if="isObjectOutput"
        :code="formattedOutput"
        language="json"
      />
      <MessageResponse
        v-else-if="isStringOutput"
        :content="formattedOutput"
        class="px-3 py-2"
      />
      <div v-else>
        {{ props.output }}
      </div>
    </div>
  </div>
</template>
