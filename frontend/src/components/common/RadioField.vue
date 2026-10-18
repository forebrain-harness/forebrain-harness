<template>
  <div class="forebrain-form-field" role="radiogroup" :aria-labelledby="labelId">
    <span :id="labelId">{{ label }}</span>
    <div class="forebrain-radios" :class="{ 'is-vertical': vertical }">
      <label
        v-for="option in options"
        :key="option.value"
        class="forebrain-radio"
        :class="{ 'has-desc': Boolean(option.description) }"
      >
        <input
          type="radio"
          :name="name"
          :value="option.value"
          :checked="option.value === modelValue"
          @change="choose(option.value)"
        />
        <i v-if="option.swatch" class="forebrain-radio-swatch" :style="{ background: option.swatch }" aria-hidden="true" />
        <span v-if="option.description" class="forebrain-radio-desc">
          <b>{{ option.label }}</b>
          <span>{{ option.description }}</span>
        </span>
        <template v-else>{{ option.label }}</template>
      </label>
    </div>
    <span v-if="hint" class="forebrain-list-note">{{ hint }}</span>
  </div>
</template>

<script setup lang="ts">
/**
 * One radio group (plan 071): the label above it like every other field, the
 * options on one line — or one per line when each carries a description — and
 * the hint that says what the choice means. Arrow keys, the space bar and the
 * group's name all are the browser's own; nothing here intercepts them.
 */
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

function choose(value: string): void {
  if (value === props.modelValue) return
  emit('update:modelValue', value)
}
</script>
