<script setup lang="ts">
import type { HTMLAttributes } from 'vue'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { cn } from '@repo/shadcn-vue/lib/utils'
import { ArrowDownIcon } from 'lucide-vue-next'
import { computed } from 'vue'
import { useStickToBottomContext } from 'vue-stick-to-bottom'

interface Props {
  class?: HTMLAttributes['class']
  /** What the button does, in the reader's language. */
  label?: string
}

const props = defineProps<Props>()
const { isAtBottom, scrollToBottom } = useStickToBottomContext()
const showScrollButton = computed(() => !isAtBottom.value)

function handleClick() {
  scrollToBottom()
}
</script>

<template>
  <Button
    v-if="showScrollButton"
    :class="cn(
      // The prototype's .sa-down (:1190): a 30px circle on the pop shadow.
      'absolute bottom-4 left-[50%] size-[30px] translate-x-[-50%] rounded-full shadow-[var(--forebrain-shadow-pop)] dark:bg-background dark:hover:bg-muted',
      props.class,
    )"
    :aria-label="props.label"
    size="icon"
    type="button"
    variant="outline"
    v-bind="$attrs"
    @click="handleClick"
  >
    <ArrowDownIcon class="size-4" />
  </Button>
</template>
