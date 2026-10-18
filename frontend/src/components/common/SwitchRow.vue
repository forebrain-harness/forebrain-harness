<template>
  <div
    class="flex items-center gap-[12px] border-t border-[var(--forebrain-divider)] py-[11px] first:border-t-0 first:pt-0"
  >
    <div class="min-w-0 flex-1">
      <b class="block text-[13px] font-medium text-[var(--forebrain-text)]">{{ title }}</b>
      <span class="mt-0.5 block text-[12px] text-[var(--forebrain-muted-text)]">{{ hint }}</span>
    </div>
    <SwitchComponent
      :model-value="modelValue"
      :disabled="disabled"
      :aria-label="title"
      @update:model-value="$emit('update:modelValue', $event)"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * One switch with its name and what it means (owner 2026-10-18): the title and
 * its grey hint on the left, the switch on the right, rows separated by a rule
 * and no frame around the group. The project's settings and the settings
 * page's memory tab both show their switches this way.
 */
import SwitchComponent from '@/components/common/SwitchComponent.vue'

withDefaults(
  defineProps<{
    /** The switch's name — also what a screen reader announces it as. */
    title: string
    /** The grey second line: what turning it on or off does. */
    hint?: string
    modelValue: boolean
    disabled?: boolean
  }>(),
  { hint: '', disabled: false },
)

defineEmits<{ 'update:modelValue': [value: boolean] }>()
</script>
