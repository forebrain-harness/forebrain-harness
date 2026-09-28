<template>
  <section class="slash-picker" :aria-label="picker.title">
    <header class="slash-picker-head">
      <span class="slash-picker-title">{{ picker.title }}</span>
      <span v-if="picker.hint" class="slash-picker-hint">{{ picker.hint }}</span>
    </header>
    <ul class="slash-picker-items">
      <li v-for="item in picker.items" :key="item.value">
        <button
          type="button"
          class="slash-picker-item"
          :class="{ 'is-picked': picked === undefined ? item.current : picked === item.value }"
          :disabled="picked !== undefined"
          :aria-pressed="picked === item.value"
          @click="emit('choose', item.value)"
        >
          <span class="slash-picker-label">{{ item.label }}</span>
          <span v-if="item.description" class="slash-picker-description">{{ item.description }}</span>
        </button>
      </li>
    </ul>
  </section>
</template>

<script setup lang="ts">
/**
 * The choice a slash command asks for, as the web shows it: the engine's
 * picker — the same items the terminal lists under its composer, opening on
 * the one in force. A pick is answered once; the card then keeps what was
 * picked.
 */
import type { SlashPicker } from '@/composables/useChatStream'

defineProps<{ picker: SlashPicker; picked?: string }>()
const emit = defineEmits<{ (e: 'choose', value: string): void }>()
</script>

<style scoped>
.slash-picker {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.slash-picker-head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  column-gap: 8px;
  row-gap: 2px;
}

.slash-picker-title {
  font-weight: 650;
  color: var(--forebrain-text);
}

.slash-picker-hint {
  color: var(--forebrain-text-2);
}

.slash-picker-items {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.slash-picker-item {
  display: flex;
  width: 100%;
  flex-wrap: wrap;
  align-items: baseline;
  column-gap: 8px;
  row-gap: 2px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  padding: 6px 10px;
  text-align: left;
  color: var(--forebrain-text);
  background: transparent;
  transition: background 0.15s ease, border-color 0.15s ease;
}

.slash-picker-item:not(:disabled):hover,
.slash-picker-item:not(:disabled):focus-visible {
  border-color: var(--forebrain-brand-border-strong);
  background: color-mix(in srgb, var(--forebrain-brand-1) 8%, transparent);
  outline: none;
}

.slash-picker-item:disabled {
  cursor: default;
}

.slash-picker-item.is-picked {
  border-color: var(--forebrain-brand-border-strong);
  background: color-mix(in srgb, var(--forebrain-brand-1) 10%, transparent);
}

/* A label or a path is shown whole; a long one wraps. */
.slash-picker-label {
  min-width: 0;
  overflow-wrap: anywhere;
  font-weight: 550;
}

.slash-picker-description {
  flex-basis: 100%;
  min-width: 0;
  overflow-wrap: anywhere;
  font-size: 12px;
  color: var(--forebrain-text-2);
}
</style>
