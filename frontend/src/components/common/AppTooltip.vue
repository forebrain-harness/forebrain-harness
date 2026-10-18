<template>
  <Teleport to="body">
    <div
      v-if="state.anchor && state.text"
      ref="tipRef"
      class="forebrain-tip"
      :class="`forebrain-tip--${side}`"
      role="tooltip"
      :style="{ left: `${left}px`, top: `${top}px`, '--tip-x': `${arrowX}px` }"
    >
      {{ state.text }}
    </div>
  </Teleport>
</template>

<script setup lang="ts">
/**
 * The one tip the app paints (plan 071), mounted once in `App.vue` where the
 * collapsed rail's hint used to live. It reads the module-level tooltip state
 * and positions itself against its anchor's rectangle in window coordinates —
 * fixed, so neither a rail's overflow nor a table's scroll container can clip
 * it, and above the modal and drawer layer (z-index 70).
 *
 * Placement follows the prototype: right of the anchor, above it, or below it
 * when there is no room above (the anchor's top is nearer than the tip's own
 * height plus a 10px margin). Centred placements stay 4px inside the window
 * and the arrow's `--tip-x` keeps pointing at the anchor's centre.
 */
import { ref, watch } from 'vue'

import { useTooltipState } from '@/composables/useTooltip'

const state = useTooltipState()
const tipRef = ref<HTMLElement | null>(null)
const left = ref(0)
const top = ref(0)
const arrowX = ref(0)
const side = ref<'above' | 'below' | 'right'>('above')

function place(): void {
  const anchor = state.anchor
  const tip = tipRef.value
  if (!anchor || !tip) return
  const rect = anchor.getBoundingClientRect()
  const width = tip.offsetWidth
  const height = tip.offsetHeight
  if (state.placement === 'right') {
    side.value = 'right'
    left.value = rect.right + 8
    top.value = rect.top + rect.height / 2 - height / 2
    return
  }
  const above = state.placement === 'top' && rect.top >= height + 10
  side.value = above ? 'above' : 'below'
  const centre = rect.left + rect.width / 2
  left.value = Math.max(4, Math.min(centre - width / 2, window.innerWidth - width - 4))
  top.value = above ? rect.top - height - 6 : rect.bottom + 6
  arrowX.value = centre - left.value
}

watch(
  () => [state.anchor, state.text, state.placement] as const,
  () => place(),
  { immediate: true, flush: 'post' },
)
</script>
