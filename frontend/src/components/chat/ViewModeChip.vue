<template>
  <div class="relative" ref="rootRef">
    <button
      type="button"
      class="forebrain-chip-btn"
      v-tip:top="t('chat.viewModeTitle')"
      :aria-label="t('chat.viewModeTitle')"
      aria-haspopup="listbox"
      :aria-expanded="open"
      data-testid="view-mode-chip"
      @click="open = !open"
    >
      <EyeOff v-if="modelValue === 'concise'" class="size-3.5" aria-hidden="true" />
      <Eye v-else class="size-3.5" aria-hidden="true" />
      <span>{{ label }}</span>
      <ChevronDown class="forebrain-chip-caret" aria-hidden="true" />
    </button>
    <div v-if="open" class="forebrain-chip-menu left-0 w-72" role="listbox" :aria-label="t('chat.viewModeTitle')">
      <button
        v-for="option in options"
        :key="option.id"
        type="button"
        class="forebrain-chip-menu-opt"
        :class="{ 'forebrain-chip-menu-opt--on': option.id === modelValue }"
        role="option"
        :aria-selected="option.id === modelValue"
        :data-testid="`view-mode-option-${option.id}`"
        @click="choose(option.id)"
      >
        <b><Check v-if="option.id === modelValue" class="size-3.5" aria-hidden="true" />{{ option.label }}</b>
        <span>{{ option.hint }}</span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * How much of the run this view draws, beside the mode and the approval preset
 * in a composer. It is a reading mode, never a filter on the run: nothing is
 * deleted, and whatever waits for the person's answer stays in the panel above
 * the composer. The choice belongs to the view it sits in — the conversation or
 * one subagent's tab — and both composers carry the same chip.
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { Check, ChevronDown, Eye, EyeOff } from 'lucide-vue-next'
import { useI18n } from '@/locales'
import type { ViewMode } from '@/composables/useViewMode'

const props = defineProps<{ modelValue: ViewMode }>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: ViewMode): void
}>()

const { t } = useI18n()
const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)

const options = computed(() => [
  { id: 'default' as ViewMode, label: t('chat.viewModeDefault'), hint: t('chat.viewModeDefaultHint') },
  { id: 'concise' as ViewMode, label: t('chat.viewModeConcise'), hint: t('chat.viewModeConciseHint') },
])

const label = computed(() => (props.modelValue === 'concise' ? t('chat.viewModeConcise') : t('chat.viewModeDefault')))

function choose(next: ViewMode) {
  open.value = false
  emit('update:modelValue', next)
}

function onDocPointerDown(event: PointerEvent) {
  if (open.value && rootRef.value && !rootRef.value.contains(event.target as Node)) open.value = false
}

onMounted(() => document.addEventListener('pointerdown', onDocPointerDown))
onUnmounted(() => document.removeEventListener('pointerdown', onDocPointerDown))
</script>
