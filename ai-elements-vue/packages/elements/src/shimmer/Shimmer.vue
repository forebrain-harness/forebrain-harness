<script setup lang="ts">
import type { CSSProperties, HTMLAttributes } from 'vue'
import { cn } from '@repo/shadcn-vue/lib/utils'
import { motion } from 'motion-v'
import { computed } from 'vue'

export interface TextShimmerProps {
  as?: keyof HTMLElementTagNameMap
  class?: HTMLAttributes['class']
  duration?: number
  spread?: number
}

const props = withDefaults(defineProps<TextShimmerProps>(), {
  as: 'p',
  duration: 2,
  spread: 2,
})


// The animation is a breathing opacity on the text itself — no gradient
// sweep, so it survives a flat, solid-colour design.
const componentClasses = computed(() => cn('relative inline-block text-current', props.class))

const componentStyle = computed((): CSSProperties => ({}))

const MotionComponent = computed(() => {
  return motion[props.as as keyof typeof motion] || motion.p
})
</script>

<template>
  <component
    :is="MotionComponent"
    :class="componentClasses"
    :style="componentStyle"
    :initial="{ opacity: 1 }"
    :animate="{ opacity: [1, 0.45, 1] }"
    :transition="{
      repeat: Number.POSITIVE_INFINITY,
      duration,
      ease: 'easeInOut',
    }"
  >
    <slot />
  </component>
</template>
