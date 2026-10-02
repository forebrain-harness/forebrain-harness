<template>
  <Teleport to="body">
    <div
      v-if="visible"
      class="rail-tooltip"
      role="tooltip"
      :style="{ left: `${left + width + 10}px`, top: `${top + height / 2}px` }"
    >
      <span class="rail-tooltip-arrow" aria-hidden="true" />
      {{ text }}
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed } from 'vue'

/**
 * The collapsed rail's name hints: browser-native title tooltips take a
 * beat to appear and render differently everywhere, so the rail paints its
 * own — immediately, beside the icon, outside the rail's overflow.
 */
const props = defineProps<{
  anchor: HTMLElement | null
  text: string
}>()

const visible = computed(() => Boolean(props.anchor && props.text))
const metrics = computed(() => {
  const rect = props.anchor?.getBoundingClientRect()
  return rect ?? { left: 0, top: 0, width: 0, height: 0 }
})
const left = computed(() => metrics.value.left)
const top = computed(() => metrics.value.top)
const width = computed(() => metrics.value.width)
const height = computed(() => metrics.value.height)
</script>

<style scoped>
.rail-tooltip {
  position: fixed;
  z-index: 60;
  transform: translateY(-50%);
  background: #101828;
  color: #FFFFFF;
  font-size: 12px;
  line-height: 1;
  padding: 6px 9px;
  border-radius: 6px;
  pointer-events: none;
  white-space: nowrap;
}
.rail-tooltip-arrow {
  position: absolute;
  left: -4px;
  top: 50%;
  width: 8px;
  height: 8px;
  background: #101828;
  transform: translateY(-50%) rotate(45deg);
}
</style>
