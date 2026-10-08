<template>
  <button
    type="button"
    role="switch"
    :aria-checked="modelValue"
    :class="['forebrain-switch', { 'forebrain-switch--on': modelValue }]"
    :disabled="disabled"
    @click="toggle"
  >
    <span class="forebrain-switch-thumb" aria-hidden="true" />
  </button>
</template>

<script setup lang="ts">
/**
 * A two-state switch, driven by v-model. It is a real button with
 * role="switch" rather than a checkbox, so its state is readable as a
 * switch and it can carry a label from the row it sits in.
 */
interface Props {
  modelValue: boolean
  disabled?: boolean
}

interface Emits {
  (e: 'update:modelValue', value: boolean): void
}

const props = withDefaults(defineProps<Props>(), {
  disabled: false,
})

const emit = defineEmits<Emits>()

function toggle() {
  if (!props.disabled) {
    emit('update:modelValue', !props.modelValue)
  }
}
</script>

<style scoped>
.forebrain-switch {
  width: 32px;
  height: 18px;
  border-radius: 999px;
  background: var(--forebrain-divider-strong);
  position: relative;
  flex: none;
  border: 0;
  cursor: pointer;
  padding: 0;
  transition: background 120ms ease;
}

.forebrain-switch:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.forebrain-switch-thumb {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: #ffffff;
  transition: left 120ms ease;
}

.forebrain-switch--on {
  background: var(--forebrain-brand-1);
}

.forebrain-switch--on .forebrain-switch-thumb {
  left: 16px;
}
</style>
