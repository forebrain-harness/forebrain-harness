<template>
  <div class="relative" ref="rootRef">
    <button
      type="button"
      class="forebrain-chip-btn"
      :disabled="disabled || switching"
      :title="t('chat.modelChipTitle')"
      aria-haspopup="listbox"
      :aria-expanded="open"
      data-testid="model-chip"
      @click="open = !open"
    >
      <Cpu class="size-3.5" aria-hidden="true" />
      <span class="max-w-44 truncate">{{ label }}</span>
      <ChevronDown class="forebrain-chip-caret" aria-hidden="true" />
    </button>
    <div v-if="open" class="forebrain-chip-menu left-0 w-72" role="listbox" :aria-label="t('chat.modelChipTitle')">
      <div class="forebrain-chip-menu-cap">{{ t('chat.modelMenuTitle') }}</div>
      <button
        v-for="option in options"
        :key="`${option.provider}/${option.model}`"
        type="button"
        class="forebrain-chip-menu-opt"
        :class="{ 'forebrain-chip-menu-opt--on': isCurrent(option) }"
        role="option"
        :aria-selected="isCurrent(option)"
        :data-testid="`model-option-${option.model}`"
        :disabled="switching"
        @click="choose(option)"
      >
        <b><Check v-if="isCurrent(option)" class="size-3.5" aria-hidden="true" />{{ option.model }}<span v-if="option.supportsImages" class="forebrain-chip-menu-badge" data-testid="model-supports-images">{{ t('chat.modelAcceptsImages') }}</span></b>
        <span>{{ option.provider }}</span>
      </button>
    </div>
    <p v-if="error && !open" role="alert" class="absolute bottom-full left-0 z-20 mb-2 w-72 rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-surface)] px-2.5 py-2 text-[12px] text-[var(--forebrain-danger)]">
      {{ error }}
    </p>
  </div>
</template>

<script setup lang="ts">
/**
 * The conversation's model, as one chip: what it runs on now, and the models
 * this primary agent is configured with to switch to. A choice writes the
 * session's own model row — the same switch /model makes, session-scoped —
 * and rides the next turn; the agent's default for other conversations never
 * moves. Switching resets the effort to the model's default, exactly as a
 * /model switch does.
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { Check, ChevronDown, Cpu } from 'lucide-vue-next'
import { forebrainApi, getErrorMessage, type SessionModelOption, type SessionModelSelection } from '@/lib/api'
import { useI18n } from '@/locales'

const props = defineProps<{
  sessionId: string
  selection: SessionModelSelection | null
  options: SessionModelOption[]
  disabled?: boolean
}>()

const emit = defineEmits<{ (e: 'saved'): void }>()

const { t } = useI18n()
const open = ref(false)
const switching = ref(false)
const error = ref('')
const rootRef = ref<HTMLElement | null>(null)

const label = computed(() => props.selection?.model || t('chat.modelNone'))

function isCurrent(option: SessionModelOption): boolean {
  return props.selection != null
    && option.provider === props.selection.provider
    && option.model === props.selection.model
}

async function choose(option: SessionModelOption) {
  const sessionId = props.sessionId
  if (switching.value || !sessionId) return
  open.value = false
  if (isCurrent(option)) return
  switching.value = true
  error.value = ''
  try {
    await forebrainApi.saveSessionModel(sessionId, {
      provider: option.provider,
      model: option.model,
      // A model switch resets the effort to the model's own default — the
      // effort chip pins one for the model it belongs to.
      effort: '',
    })
    emit('saved')
  } catch (cause) {
    // Nothing changed on the server; the chip keeps the state it had, and
    // the server's own words say why.
    error.value = getErrorMessage(cause)
  } finally {
    switching.value = false
  }
}

function onDocPointerDown(event: PointerEvent) {
  if (open.value && rootRef.value && !rootRef.value.contains(event.target as Node)) open.value = false
}

onMounted(() => document.addEventListener('pointerdown', onDocPointerDown))
onUnmounted(() => document.removeEventListener('pointerdown', onDocPointerDown))

// A refusal notice under a message points the reader here: it opens the menu
// from its own click, which lands after the pointerdown that would close it.
function openMenu() {
  if (!props.disabled && !switching.value) open.value = true
}

defineExpose({ openMenu })
</script>

<style scoped>
/* The design's `.badge.ok` inside the model menu: the option's `<b>` is a flex
   row, so `margin-left: auto` pins the badge to the option's right edge. */
.forebrain-chip-menu-badge {
  display: inline-flex;
  align-items: center;
  margin-left: auto;
  height: 18px;
  padding: 0 7px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 500;
  white-space: nowrap;
  background: var(--forebrain-success-soft);
  color: var(--forebrain-success);
}
</style>
