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
    <div v-if="open" class="forebrain-chip-menu right-0 w-[220px] max-w-[calc(100vw-2rem)]" role="listbox" :aria-label="t('chat.effortChipTitle')">
      <div class="forebrain-chip-menu-cap">{{ t('chat.effortChipTitle') }}</div>
      <button
        v-for="option in optionsList"
        :key="option.id"
        type="button"
        class="forebrain-chip-menu-opt forebrain-chip-menu-opt--line"
        :class="{ 'forebrain-chip-menu-opt--on': option.id === effort }"
        role="option"
        :aria-selected="option.id === effort"
        :data-testid="`effort-option-${option.id || 'default'}`"
        @click="choose(option.id)"
      >
        <i class="forebrain-chip-menu-lead" aria-hidden="true"><Check v-if="option.id === effort" class="size-3.5" /></i>
        <b>{{ option.label }}</b>
        <code v-if="option.value" class="forebrain-chip-menu-val">{{ option.value }}</code>
        <em v-if="option.fromSetting" class="forebrain-chip-menu-tag" data-testid="effort-setting-tag">{{ t('chat.effortModelSetting') }}</em>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * How hard this conversation's model thinks, as one chip: the level the
 * conversation's row stores, out of the levels the model in force offers. 「默认」
 * — the level that pins nothing, so the provider applies its own — is listed
 * only when it is the truth to show: the row stores no level, or the model is
 * configured with none. The level the model page's 「思考强度」 configured is
 * marked, because selecting the model is what pins it. A choice is written to
 * the session's own row and rides the next turn, exactly as /model's effort
 * pick does.
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { Brain, Check, ChevronDown } from 'lucide-vue-next'
import { EFFORT_LEVELS, effortLevelLabel } from '@/lib/effortLabel'
import { useI18n } from '@/locales'

const props = defineProps<{
  /** The pinned effort: '' is the explicit model default. */
  effort: string
  /** The levels the catalog offers for the model in force. */
  efforts: string[]
  /** The effort the model in force is configured with; '' or absent when it
   *  configures none. */
  defaultEffort?: string
  disabled?: boolean
}>()

const emit = defineEmits<{ (e: 'select', effort: string): void }>()

const { t } = useI18n()
const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)

const optionsList = computed(() => {
  // The order a picker offers levels in is the project's own (EFFORT_LEVELS,
  // `none` first); a level a source adds later keeps the server's place after them.
  const known: string[] = EFFORT_LEVELS.filter((level) => props.efforts.includes(level))
  const extra = props.efforts.filter((level) => !(EFFORT_LEVELS as readonly string[]).includes(level))
  const configured = props.defaultEffort ?? ''
  // 「默认」 is a row only when it is the truth to show: the conversation
  // stores no level, or the model is configured with none.
  const showDefault = props.effort === '' || configured === ''
  const ids: string[] = showDefault ? ['', ...known, ...extra] : [...known, ...extra]
  return ids.map((id) => {
    const label = effortLevelLabel(id)
    return {
      id,
      label,
      // The level's own value name beside a translated label; not beside one that already reads the same.
      value: id !== '' && label.toLowerCase() !== id.toLowerCase() ? id : '',
      fromSetting: id !== '' && id === configured,
    }
  })
})

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
