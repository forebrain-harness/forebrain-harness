<template>
  <div class="ctx-meter" role="img" :aria-label="ariaLabel">
    <i
      v-for="(seg, idx) in normalized"
      :key="idx"
      :style="{ width: `${seg.pct}%`, background: seg.color }"
    />
  </div>
  <div class="ctx-legend">
    <span v-for="(seg, idx) in normalized" :key="idx">
      <i :style="{ background: seg.color }" />{{ seg.label }}
    </span>
  </div>
</template>

<script setup lang="ts">
/**
 * The three-segment context meter and its legend, shared by the workbench's
 * context tab and the subagent's context panel so both read a context window
 * the same way. Each segment names its own colour; the bar is normalised so
 * the percentages always add to the full width.
 */
import { computed } from 'vue'

export interface ContextMeterSegment {
  label: string
  value: number
  color: string
}

const props = defineProps<{ segments: ContextMeterSegment[] }>()

const normalized = computed(() => {
  const total = props.segments.reduce((sum, seg) => sum + Math.max(0, seg.value), 0)
  if (!total) {
    return props.segments.map((seg) => ({ ...seg, pct: 0 }))
  }
  return props.segments.map((seg) => ({ ...seg, pct: (Math.max(0, seg.value) / total) * 100 }))
})

const ariaLabel = computed(() =>
  normalized.value.map((seg) => `${seg.label} ${Math.round(seg.pct)}%`).join(', '),
)
</script>

<style scoped>
.ctx-meter {
  display: flex;
  height: 8px;
  border-radius: 999px;
  background: var(--forebrain-button-alt-bg);
  overflow: hidden;
}
.ctx-meter i {
  display: block;
  height: 100%;
}
.ctx-legend {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  font-size: 11.5px;
  color: var(--forebrain-text-2);
}
.ctx-legend i {
  display: inline-block;
  width: 8px;
  height: 8px;
  margin-right: 4px;
  border-radius: 2px;
}
</style>
