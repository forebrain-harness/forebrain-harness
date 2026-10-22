<template>
  <div class="space-y-2" data-testid="model-chips">
    <TagsInput
      class="gap-1.5 rounded-lg border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-2 py-1.5"
      :model-value="models"
      :add-on-blur="true"
      @update:model-value="onModelsChange"
    >
      <TagsInputItem
        v-for="model in models"
        :key="model"
        :value="model"
        :data-chip="model"
        class="h-auto gap-1 rounded-lg bg-[var(--forebrain-brand-soft)] px-2 py-0.5 font-mono text-[12px] text-[var(--forebrain-brand-1)]"
      >
        {{ model }}
        <TagsInputItemDelete
          class="text-[var(--forebrain-brand-1)]/70 hover:text-[var(--forebrain-brand-1)]"
          :aria-label="t('providers.removeModel', { model })"
        />
      </TagsInputItem>
      <TagsInputInput
        class="min-w-[140px] px-1 py-0.5 text-[13px] text-[var(--forebrain-text)]"
        data-testid="model-chips-input"
        :placeholder="models.length ? '' : t('providers.modelsPlaceholder')"
        :aria-label="t('providers.modelsLabel')"
        @input="onRawInput"
      />
    </TagsInput>
    <div v-if="suggestions.length" class="flex flex-wrap gap-1.5">
      <button
        v-for="model in suggestions"
        :key="model"
        type="button"
        class="rounded-lg border border-[var(--forebrain-divider)] px-2 py-0.5 font-mono text-[11px] text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-input-hover-bg)]"
        @click="addModel(model)"
      >{{ model }}</button>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The models field: one service row names the models to try, in order. Typing
 * a comma (or leaving the field) turns what was typed into a chip; the chips
 * are the value. Suggestions come from the model catalog for the current
 * provider and never block hand entry.
 *
 * The chips are the library's TagsInput (plan 096): Enter makes a chip
 * natively, and it guards against an IME composition so typing Chinese never
 * commits half a word. Two things this field still owns:
 *
 * - a comma commits everything before it *while typing*, leaving the tail in
 *   the field. In a browser the component's own `delimiter` does that; a
 *   programmatic `input` event (a test, a paste-like fill) carries no
 *   `event.data`, so the raw value is read here as well. Both paths end in the
 *   same emit, and the browser one clears the field first, so no value is
 *   added twice;
 * - the values are trimmed, deduplicated and never empty, which is what
 *   `mcpKeyValuePayload`-style payload rules expect of this list.
 */
import { computed } from 'vue'
import {
  TagsInput,
  TagsInputInput,
  TagsInputItem,
  TagsInputItemDelete,
} from '@repo/shadcn-vue/components/ui/tags-input'

import { useI18n } from '@/locales'

const DELIMITER = ','

const props = defineProps<{
  modelValue: string[]
  suggestions?: string[]
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string[]]
}>()

const { t } = useI18n()

const models = computed(() => props.modelValue)
const suggestions = computed(() => (props.suggestions ?? []).filter((model) => !models.value.includes(model)))

/** What the component hands back: trimmed, deduplicated, no blanks. */
function clean(list: string[]): string[] {
  const next: string[] = []
  for (const item of list) {
    const name = item.trim()
    if (name && !next.includes(name)) next.push(name)
  }
  return next
}

function same(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((item, at) => item === b[at])
}

function commit(next: string[]): void {
  const list = clean(next)
  if (same(list, models.value)) return
  emit('update:modelValue', list)
}

function onModelsChange(value: unknown): void {
  commit(Array.isArray(value) ? value.map((item) => String(item)) : [])
}

// A comma is the separator the owner asked for: everything before it becomes
// chips the moment it is typed; the rest stays in the input for the next one.
function onRawInput(event: Event): void {
  const input = event.target as HTMLInputElement | null
  if (!input || !input.value.includes(DELIMITER)) return
  const parts = input.value.split(DELIMITER)
  const rest = parts.pop() ?? ''
  commit([...models.value, ...parts])
  input.value = rest
}

function addModel(model: string): void {
  commit([...models.value, model])
}
</script>
