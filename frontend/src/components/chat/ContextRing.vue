<template>
  <!-- The context window as a ring: how much of it this conversation has used,
       from the same number the worked line closes on. Past 80% the ring turns
       warning-coloured and names its percentage; clicking opens the workbench,
       where the detail lives. -->
  <button
    type="button"
    class="forebrain-ctx-ring-btn"
    v-tip:top="t('chat.ctxRingTitle', { percent })"
    :aria-label="t('chat.ctxRingTitle', { percent })"
    data-testid="context-ring"
    @click="emit('open')"
  >
    <svg class="forebrain-ctx-ring" viewBox="0 0 18 18" aria-hidden="true">
      <circle cx="9" cy="9" r="7" fill="none" stroke="var(--forebrain-divider-strong)" stroke-width="2.2" />
      <circle
        cx="9" cy="9" r="7" fill="none"
        :stroke="warn ? 'var(--forebrain-warning)' : 'var(--forebrain-brand-1)'"
        stroke-width="2.2"
        stroke-linecap="round"
        :stroke-dasharray="`${(circumference * percent / 100).toFixed(1)} ${circumference.toFixed(1)}`"
        transform="rotate(-90 9 9)"
      />
    </svg>
    <span v-if="warn" class="forebrain-ctx-ring-pc">{{ percent }}%</span>
  </button>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from '@/locales'

const props = defineProps<{ percent: number }>()

const emit = defineEmits<{ (e: 'open'): void }>()

const { t } = useI18n()

const circumference = 2 * Math.PI * 7
const warn = computed(() => props.percent >= 80)
</script>

<style scoped>
.forebrain-ctx-ring-btn {
  width: auto;
  min-width: 28px;
  height: 28px;
  padding: 0 4px;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border: 0;
  background: transparent;
  border-radius: 6px;
  color: var(--forebrain-muted-text);
  cursor: pointer;
}

.forebrain-ctx-ring-btn:hover {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
}

.forebrain-ctx-ring {
  width: 18px;
  height: 18px;
  display: block;
}

.forebrain-ctx-ring-pc {
  font-size: 11.5px;
  color: var(--forebrain-warning);
  font-variant-numeric: tabular-nums;
}
</style>
