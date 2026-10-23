<template>
  <div class="relative flex items-center" ref="menuRef">
    <button
      type="button"
      class="forebrain-chip-btn"
      :disabled="disabled"
      v-tip:top="t('approval.pickerTitle')"
      :aria-label="t('approval.pickerTitle')"
      aria-haspopup="listbox"
      :aria-expanded="open"
      data-testid="approval-picker"
      @click="open = !open"
    >
      <ShieldCheck class="size-3.5" aria-hidden="true" />
      <span>{{ currentLabel }}</span>
      <ChevronDown class="forebrain-chip-caret" aria-hidden="true" />
    </button>
    <p
      v-if="error && !open"
      role="alert"
      class="absolute bottom-full left-0 z-20 mb-2 w-72 rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-surface)] px-2.5 py-2 text-[12px] text-[var(--forebrain-danger)]"
      data-testid="approval-picker-error"
    >
      {{ error }}
    </p>
    <div v-if="open" class="forebrain-chip-menu left-0 w-[360px]" role="listbox" :aria-label="t('chat.approvalMenuTitle')">
      <div class="forebrain-chip-menu-cap">{{ t('chat.approvalMenuTitle') }}</div>
      <button
        v-for="preset in presets"
        :key="preset.id"
        type="button"
        class="forebrain-chip-menu-opt"
        :class="{ 'forebrain-chip-menu-opt--on': preset.id === current }"
        role="option"
        :aria-selected="preset.id === current"
        :data-testid="`approval-option-${preset.id}`"
        @click="choose(preset.id)"
      >
        <b><Check v-if="preset.id === current" class="size-3.5" aria-hidden="true" />{{ preset.label }}</b>
        <span>{{ preset.description }}</span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { Check, ChevronDown, ShieldCheck } from 'lucide-vue-next'
import { forebrainApi, getErrorMessage } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The session-scoped approval preset switch beside the composer. A choice
 * here applies to this conversation only and never writes forebrain.yaml —
 * the same rule the terminal's /permissions follows. What it shows is the
 * conversation's own state, read back from the server for each session the
 * page opens, so a choice made in one conversation never appears to carry
 * over to another.
 *
 * A new chat has no conversation yet, and the switch still works: it shows
 * the agent's default, and a pick is held as a draft that the page hands to
 * the conversation its first message opens (`takeDraft`) — the same
 * session-scoped write, made the moment there is a session to make it on.
 */
const props = defineProps<{ sessionId: string | null }>()

const { t } = useI18n()

type Preset = { id: string; label: string; description: string }

const presets = computed<Preset[]>(() => [
  { id: 'read-only', label: t('approval.readOnly'), description: t('approval.readOnlyDescription') },
  { id: 'auto', label: t('approval.default'), description: t('approval.defaultDescription') },
  { id: 'full-access', label: t('approval.fullAccess'), description: t('approval.fullAccessDescription') },
])

const open = ref(false)
const current = ref<string | null>(null)
const switching = ref(false)
const error = ref('')
const menuRef = ref<HTMLElement | null>(null)

/** The preset picked before any conversation existed, until it is handed over. */
const draft = ref<string | null>(null)

const disabled = computed(() => switching.value)
const currentLabel = computed(() => {
  const preset = presets.value.find((item) => item.id === current.value)
  return preset ? preset.label : t('approval.followDefault')
})

// The read is per conversation: an answer that arrives after the page has
// moved to another session is that other session's no longer.
async function load(sessionId: string | null) {
  current.value = null
  draft.value = null
  error.value = ''
  try {
    // No conversation yet: what a new one would start under is the agent's own
    // default.
    const res = sessionId
      ? await forebrainApi.sessionPresetCurrent(sessionId)
      : await forebrainApi.approvalDefault()
    if (props.sessionId === sessionId) current.value = res.current
  } catch (cause) {
    if (props.sessionId === sessionId) error.value = getErrorMessage(cause)
  }
}

async function choose(id: string) {
  const sessionId = props.sessionId
  if (switching.value) return
  open.value = false
  error.value = ''
  if (!sessionId) {
    // Held for the conversation the first message opens.
    current.value = id
    draft.value = id
    return
  }
  switching.value = true
  try {
    await forebrainApi.sessionPreset(sessionId, id)
    if (props.sessionId === sessionId) current.value = id
  } catch (cause) {
    // Nothing changed on the server; the label keeps the state it had, and
    // the server's own words say why.
    if (props.sessionId === sessionId) error.value = getErrorMessage(cause)
  } finally {
    switching.value = false
  }
}

function onDocPointerDown(event: PointerEvent) {
  if (!menuRef.value?.contains(event.target as Node)) open.value = false
}

watch(() => props.sessionId, (sessionId) => {
  open.value = false
  void load(sessionId)
}, { immediate: true })

watch(open, (value) => {
  if (value) error.value = ''
})

/**
 * The preset picked on a new chat, once: the page applies it to the
 * conversation it just opened. Null when nothing was picked there.
 */
function takeDraft(): string | null {
  const picked = draft.value
  draft.value = null
  return picked
}

defineExpose({ takeDraft })

onMounted(() => {
  document.addEventListener('pointerdown', onDocPointerDown)
})

onUnmounted(() => {
  document.removeEventListener('pointerdown', onDocPointerDown)
})
</script>
