<template>
  <RadioGroup
    :model-value="modelValue"
    :aria-labelledby="labelId"
    class="forebrain-form-field"
    @update:model-value="choose"
  >
    <span :id="labelId">{{ label }}</span>
    <div class="forebrain-radios" :class="{ 'is-vertical': vertical }">
      <div
        v-for="option in options"
        :key="option.value"
        class="forebrain-radio"
        :class="{ 'has-desc': Boolean(option.description) }"
      >
        <RadioGroupItem :id="`${labelId}-${option.value}`" :value="option.value" />
        <i v-if="option.swatch" class="forebrain-radio-swatch" :style="{ background: option.swatch }" aria-hidden="true" />
        <Label
          :for="`${labelId}-${option.value}`"
          class="font-normal leading-[normal] text-[13px] text-[var(--forebrain-text)]"
        >
          <span v-if="option.description" class="forebrain-radio-desc">
            <b>{{ option.label }}</b>
            <span>{{ option.description }}</span>
          </span>
          <template v-else>{{ option.label }}</template>
        </Label>
      </div>
    </div>
    <span v-if="hint" class="forebrain-list-note">{{ hint }}</span>
  </RadioGroup>
</template>

<script setup lang="ts">
/**
 * One radio group (plan 071): the label above it like every other field, the
 * options on one line — or one per line when each carries a description — and
 * the hint that says what the choice means.
 *
 * The group is the library's RadioGroup (plan 096), so an option is a
 * `role="radio"` button with the indicator the library draws rather than a
 * native radio input; the focus ring, the disabled state and the
 * `checked` state all come from it. The option's words stay in a `<Label for>`,
 * which is what makes clicking them pick the option — an interaction element
 * must not be wrapped in a `<label>`, or one click would be two. The
 * `.forebrain-radio*` class names and the props below are the callers' contract
 * (7 call sites) and do not change.
 *
 * Arrow keys between options come from the library too: its roving focus binds
 * the key handler from the item's own focus event, so a test (or any code) has
 * to let focus settle one tick before sending a key.
 */
import { Label } from '@repo/shadcn-vue/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@repo/shadcn-vue/components/ui/radio-group'

interface RadioOption {
  value: string
  label: string
  /** A colour chip drawn before the label (a brand swatch). */
  swatch?: string
  /** A second line explaining the option; makes the group read as a vertical list. */
  description?: string
}

const props = withDefaults(
  defineProps<{
    label: string
    name: string
    options: RadioOption[]
    modelValue: string
    hint?: string
    vertical?: boolean
  }>(),
  { hint: '', vertical: false },
)

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

const labelId = `radio-group-${props.name}`

function choose(value: unknown): void {
  const next = String(value ?? '')
  if (next === props.modelValue) return
  emit('update:modelValue', next)
}
</script>
