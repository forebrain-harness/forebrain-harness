<template>
  <span :class="['forebrain-badge', variantClass]">
    <slot />
  </span>
</template>

<script setup lang="ts">
/**
 * The one status pill: a connection is live, a job failed, a value is
 * inherited. Every colour it can take comes from the palette, so a badge
 * never carries a colour of its own.
 */
import { computed } from 'vue'

type BadgeVariant = 'default' | 'gray' | 'success' | 'warning' | 'error'

interface Props {
  variant?: BadgeVariant
}

const props = withDefaults(defineProps<Props>(), {
  variant: 'default',
})

const variantClass = computed(() => `forebrain-badge--${props.variant}`)
</script>

<style scoped>
.forebrain-badge {
  display: inline-flex;
  align-items: center;
  height: 20px;
  padding: 0 7px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 500;
  white-space: nowrap;
}

/* Default: brand soft */
.forebrain-badge--default {
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}

/* Gray */
.forebrain-badge--gray {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text-2);
}

/* Success */
.forebrain-badge--success {
  background: var(--forebrain-success-soft);
  color: var(--forebrain-success);
}

/* Warning */
.forebrain-badge--warning {
  background: var(--forebrain-warning-soft);
  color: var(--forebrain-warning);
}

/* Error: white background with red border */
.forebrain-badge--error {
  background: var(--forebrain-bg);
  color: var(--forebrain-danger);
  border: 1px solid var(--forebrain-danger);
}
</style>
