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
  // The library's own smooth scroll back; a reader who asked the system for
  // reduced motion gets there at once instead.
  const reduce = typeof window !== 'undefined'
    && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
  scrollToBottom(reduce ? { animation: 'instant' } : undefined)
}
</script>

<template>
  <!-- The prototype's .sa-down (:1424): a 30px circle on the pop shadow,
       centred, 12px above whatever closes the thread — the composer, or the
       working line above it. It fades in slid up 6px and fades out the same
       way, unless the reader asked for reduced motion. -->
  <Transition name="scroll-button">
    <Button
      v-if="showScrollButton"
      :class="cn(
        'conversation-scroll-button absolute bottom-3 left-1/2 -ml-[15px] size-[30px] rounded-full border-[var(--forebrain-divider)] bg-[var(--forebrain-page-bg)] text-[var(--forebrain-text-2)] shadow-[var(--forebrain-shadow-pop)] hover:text-[var(--forebrain-brand-1)] hover:bg-[var(--forebrain-page-bg)] dark:bg-background dark:hover:bg-muted',
        props.class,
      )"
      :aria-label="props.label"
      size="icon"
      type="button"
      variant="outline"
      v-bind="$attrs"
      @click="handleClick"
    >
      <ArrowDownIcon class="size-[15px]" />
    </Button>
  </Transition>
</template>

<style scoped>
.scroll-button-enter-active,
.scroll-button-leave-active {
  transition: opacity 0.16s ease, transform 0.16s ease;
}

.scroll-button-enter-from,
.scroll-button-leave-to {
  opacity: 0;
  transform: translateY(6px);
}

@media (prefers-reduced-motion: reduce) {
  .scroll-button-enter-active,
  .scroll-button-leave-active {
    transition: none;
  }

  .scroll-button-enter-from,
  .scroll-button-leave-to {
    transform: none;
  }
}
</style>
