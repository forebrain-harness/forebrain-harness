<template>
  <Switch
    :model-value="modelValue"
    :disabled="disabled"
    class="forebrain-switch"
    :class="{ 'forebrain-switch--on': modelValue }"
    thumb-class="forebrain-switch-thumb size-3.5 !translate-x-0 data-[state=checked]:!translate-x-[14px]"
    @update:model-value="(value) => emit('update:modelValue', value === true)"
  />
</template>

<script setup lang="ts">
/**
 * A two-state switch, driven by v-model, built on the library's Switch
 * (plan 096): a real button with role="switch" rather than a checkbox, so its
 * state is readable as a switch and it can carry a label from the row it sits
 * in.
 *
 * The design's geometry (32×18 pill, 14px thumb inset 2px, the brand colour
 * when on) is not the library's: it ships `h-[1.15rem] w-8` with a 16px thumb
 * and a `translate-x-[calc(100%-2px)]` it derives from that size. So the pill
 * is `.forebrain-switch` here, and the thumb is overridden through the library's
 * `thumbClass` — `!translate-x-[14px]` is required because the shipped
 * translate carries a `data-[state=checked]:` modifier that `tailwind-merge`
 * will not collapse against ours. The scoped rules below reach the thumb with
 * `:deep()`: the library renders it, so it never carries this component's
 * scope id.
 */
import { Switch } from '@repo/shadcn-vue/components/ui/switch'

interface Props {
  modelValue: boolean
  disabled?: boolean
}

interface Emits {
  (e: 'update:modelValue', value: boolean): void
}

withDefaults(defineProps<Props>(), {
  disabled: false,
})

const emit = defineEmits<Emits>()
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

/* The pill's two states are the design's drop-in for the library's
   `data-[state=…]:bg-*` utilities. The checked colour is the brand either way,
   but the unchecked one is not — the library paints `--input`, the design
   `--forebrain-divider-strong` — and that utility ties on specificity, so the
   state is named here as well instead of trusting the sheet's chunk order. */
.forebrain-switch[data-state='unchecked'] {
  background: var(--forebrain-divider-strong);
}

.forebrain-switch:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

:deep(.forebrain-switch-thumb) {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: #ffffff;
  transition: transform 120ms ease;
}

.forebrain-switch--on {
  background: var(--forebrain-brand-1);
}
</style>
