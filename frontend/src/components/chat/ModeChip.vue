<template>
  <div class="relative" ref="rootRef">
    <button
      type="button"
      class="forebrain-chip-btn"
      :class="mode === 'plan' ? 'forebrain-chip-btn--plan' : ''"
      :disabled="disabled"
      :title="t('chat.modeTitle')"
      aria-haspopup="listbox"
      :aria-expanded="open"
      data-testid="mode-chip"
      @click="open = !open"
    >
      <InfinityIcon v-if="mode !== 'plan'" class="size-3.5" aria-hidden="true" />
      <Map v-else class="size-3.5" aria-hidden="true" />
      <span>{{ label }}</span>
      <ChevronDown class="forebrain-chip-caret" aria-hidden="true" />
    </button>
    <div v-if="open" class="forebrain-chip-menu left-0 w-60" role="listbox" :aria-label="t('chat.modeTitle')">
      <div class="forebrain-chip-menu-cap">{{ t('chat.modeMenuTitle') }}</div>
      <button
        v-for="option in options"
        :key="option.id"
        type="button"
        class="forebrain-chip-menu-opt"
        :class="{ 'forebrain-chip-menu-opt--on': option.id === mode }"
        role="option"
        :aria-selected="option.id === mode"
        :data-testid="`mode-option-${option.id}`"
        :disabled="disabled"
        @click="choose(option.id)"
      >
        <b><Check v-if="option.id === mode" class="size-3.5" aria-hidden="true" />{{ option.label }}</b>
        <span>{{ option.description }}</span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The composer's mode switch: Agent or Plan, for this conversation only. It
 * only calls the switch the page already owns — the same /plan command the
 * terminal's mode key sends — and is disabled while a run is going, because a
 * mode change can only ride the next turn.
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { Check, ChevronDown, Infinity as InfinityIcon, Map } from 'lucide-vue-next'
import { useI18n } from '@/locales'

const props = defineProps<{ mode: string; disabled?: boolean }>()

const emit = defineEmits<{ (e: 'select', mode: string): void }>()

const { t } = useI18n()
const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)

const options = computed(() => [
  { id: 'agent', label: t('chat.modeAgent'), description: t('chat.modeAgentDesc') },
  { id: 'plan', label: t('chat.modePlan'), description: t('chat.modePlanDesc') },
])

const label = computed(() => (props.mode === 'plan' ? t('chat.modePlan') : t('chat.modeAgent')))

function choose(next: string) {
  open.value = false
  emit('select', next)
}

function onDocPointerDown(event: PointerEvent) {
  if (open.value && rootRef.value && !rootRef.value.contains(event.target as Node)) open.value = false
}

onMounted(() => document.addEventListener('pointerdown', onDocPointerDown))
onUnmounted(() => document.removeEventListener('pointerdown', onDocPointerDown))
</script>
