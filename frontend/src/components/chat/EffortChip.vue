<template>
  <div class="relative" ref="rootRef">
    <button
      type="button"
      class="forebrain-chip-btn"
      :disabled="disabled"
      v-tip:top="t('chat.effortChipTitle')"
      aria-haspopup="listbox"
      :aria-expanded="open"
      data-testid="effort-chip"
      @click="open = !open"
    >
      <Brain class="size-3.5" aria-hidden="true" />
      <span>{{ currentLabel }}</span>
      <ChevronDown class="forebrain-chip-caret" aria-hidden="true" />
    </button>
    <div v-if="open" class="forebrain-chip-menu right-0 w-56" role="listbox" :aria-label="t('chat.effortChipTitle')">
      <div class="forebrain-chip-menu-cap">{{ t('chat.effortMenuTitle') }}</div>
      <button
        v-for="option in optionsList"
        :key="option.id"
        type="button"
        class="forebrain-chip-menu-opt"
        :class="{ 'forebrain-chip-menu-opt--on': option.id === effort }"
        role="option"
        :aria-selected="option.id === effort"
        :data-testid="`effort-option-${option.id || 'default'}`"
        @click="choose(option.id)"
      >
        <b><Check v-if="option.id === effort" class="size-3.5" aria-hidden="true" />{{ option.label }}</b>
        <span>{{ option.description }}</span>
      </button>
      <div class="forebrain-chip-menu-cap">{{ t('chat.effortMenuFoot') }}</div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * How hard this conversation's model thinks, as one chip: the default level —
 * which pins nothing, so the provider applies its own — plus the levels the
 * shared model catalog reports for the model in force. The levels move with
 * the model; a model that offers none has no chip at all. A choice is written
 * to the session's own row and rides the next turn, exactly as /model's
 * effort pick does.
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { Brain, Check, ChevronDown } from 'lucide-vue-next'
import { effortLevelLabel } from '@/lib/effortLabel'
import { useI18n } from '@/locales'

const props = defineProps<{
  /** The pinned effort: '' is the explicit model default. */
  effort: string
  /** The levels the catalog offers for the model in force. */
  efforts: string[]
  disabled?: boolean
}>()

const emit = defineEmits<{ (e: 'select', effort: string): void }>()

const { t } = useI18n()
const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)

function levelDescription(id: string): string {
  if (id === 'low') return t('chat.effortLowHint')
  if (id === 'medium') return t('chat.effortMediumHint')
  if (id === 'high') return t('chat.effortHighHint')
  if (id === 'xhigh') return t('chat.effortXhighHint')
  return ''
}

const optionsList = computed(() => [
  { id: '', label: t('chat.effortDefault'), description: t('chat.effortDefaultHint') },
  ...props.efforts.map((id) => ({ id, label: effortLevelLabel(id), description: levelDescription(id) })),
])

const currentLabel = computed(() => (props.effort ? effortLevelLabel(props.effort) : t('chat.effortDefault')))

async function choose(next: string) {
  open.value = false
  if (next === props.effort) return
  emit('select', next)
}

function onDocPointerDown(event: PointerEvent) {
  if (open.value && rootRef.value && !rootRef.value.contains(event.target as Node)) open.value = false
}

onMounted(() => document.addEventListener('pointerdown', onDocPointerDown))
onUnmounted(() => document.removeEventListener('pointerdown', onDocPointerDown))
</script>
