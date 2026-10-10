<template>
  <div class="space-y-2" data-testid="model-chips">
    <div class="flex flex-wrap items-center gap-1.5 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-2 py-1.5">
      <span
        v-for="model in models"
        :key="model"
        class="inline-flex items-center gap-1 rounded-lg bg-[var(--forebrain-brand-soft)] px-2 py-0.5 font-mono text-[12px] text-[var(--forebrain-brand-1)]"
        :data-chip="model"
      >
        {{ model }}
        <button
          type="button"
          class="text-[var(--forebrain-brand-1)]/70 hover:text-[var(--forebrain-brand-1)]"
          :aria-label="t('providers.removeModel', { model })"
          @click="removeModel(model)"
        >×</button>
      </span>
      <input
        v-model="draft"
        type="text"
        class="min-w-[140px] flex-1 bg-transparent px-1 py-0.5 text-[13px] text-[var(--forebrain-text)] outline-none"
        :placeholder="models.length ? '' : t('providers.modelsPlaceholder')"
        :aria-label="t('providers.modelsLabel')"
        data-testid="model-chips-input"
        @input="onInput"
        @keydown.enter.prevent="commitDraft"
        @blur="commitDraft"
      />
    </div>
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
import { computed, ref } from 'vue'
import { useI18n } from '@/locales'

/**
 * The models field: one service row names the models to try, in order. Typing
 * a comma (or leaving the field) turns what was typed into a chip; the chips
 * are the value. Suggestions come from the model catalog for the current
 * provider and never block hand entry.
 */
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

const draft = ref('')

function addModel(model: string) {
  const name = model.trim()
  if (!name || models.value.includes(name)) return
  emit('update:modelValue', [...models.value, name])
}

function removeModel(model: string) {
  emit('update:modelValue', models.value.filter((item) => item !== model))
}

// A comma is the separator the owner asked for: everything before it becomes
// chips the moment it is typed; the rest stays in the input for the next one.
function onInput() {
  if (!draft.value.includes(',')) return
  const parts = draft.value.split(',')
  const rest = parts.pop() ?? ''
  const next = [...models.value]
  for (const part of parts) {
    const name = part.trim()
    if (name && !next.includes(name)) next.push(name)
  }
  emit('update:modelValue', next)
  draft.value = rest
}

// Leaving the field (or pressing Enter) commits what is left as one chip.
function commitDraft() {
  const name = draft.value.trim()
  if (!name) return
  if (!models.value.includes(name)) {
    emit('update:modelValue', [...models.value, name])
  }
  draft.value = ''
}
</script>
