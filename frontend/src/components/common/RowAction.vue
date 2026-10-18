<template>
  <button
    v-tip:top="label"
    type="button"
    class="forebrain-icon-btn forebrain-row-action"
    :class="toneClass"
    :aria-label="ariaLabel"
    :disabled="disabled"
  >
    <component :is="icon" class="size-3.5" aria-hidden="true" />
  </button>
</template>

<script setup lang="ts">
/**
 * One action in a table row (plan 071): a 28×28 icon button that names itself
 * with the compact tip above it and with its aria-label, which also carries the
 * row's name for a screen reader ("删除 会话 A"). The tone tints the icon —
 * `danger` for a destructive action, `ok` for a permissive one — and the
 * listeners and `data-testid` a caller passes land on the button itself.
 */
import { computed, type Component } from 'vue'

const props = withDefaults(
  defineProps<{
    icon: Component
    label: string
    name?: string
    tone?: 'danger' | 'ok'
    disabled?: boolean
  }>(),
  { name: '', tone: undefined, disabled: false },
)

const toneClass = computed(() =>
  props.tone === 'danger' ? 'forebrain-row-action--danger' : props.tone === 'ok' ? 'forebrain-row-action--ok' : '',
)

const ariaLabel = computed(() => (props.name ? `${props.label} ${props.name}` : props.label))
</script>
