<template>
  <div class="ctx-acc">
    <button
      type="button"
      class="ctx-acc-head"
      :aria-expanded="open"
      :data-testid="testid"
      @click="emit('toggle')"
    >
      {{ title }}
      <span class="ctx-acc-count">{{ count }}</span>
      <ChevronRight class="ctx-acc-chev" :class="{ 'ctx-acc-chev--open': open }" aria-hidden="true" />
    </button>
    <div v-if="open" class="ctx-acc-body">
      <slot />
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * One collapsible section of the context tab: a bold header with a count and
 * a chevron, then its body. Shared with the subagent's context panel so both
 * surfaces disclose the same way.
 */
import { ChevronRight } from 'lucide-vue-next'

withDefaults(defineProps<{ title: string; count: number | string; open: boolean; testid?: string }>(), {
  testid: undefined,
})
const emit = defineEmits<{ toggle: [] }>()
</script>

<style scoped>
.ctx-acc {
  border-top: 1px solid var(--forebrain-divider);
}
.ctx-acc-head {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 8px 2px;
  border: 0;
  background: transparent;
  text-align: left;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--forebrain-text);
}
.ctx-acc-count {
  font-size: 12px;
  font-weight: 400;
  color: var(--forebrain-muted-text);
}
.ctx-acc-chev {
  width: 14px;
  height: 14px;
  margin-left: auto;
  color: var(--forebrain-muted-text);
  transition: transform 120ms ease;
}
.ctx-acc-chev--open {
  transform: rotate(90deg);
}
.ctx-acc-body {
  display: grid;
  gap: 5px;
  padding: 0 2px 10px;
}
@media (prefers-reduced-motion: reduce) {
  .ctx-acc-chev {
    transition: none;
  }
}
</style>
