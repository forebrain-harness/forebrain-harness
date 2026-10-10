<template>
  <div class="relative flex items-center" ref="menuRef">
    <button
      type="button"
      class="inline-flex h-8 items-center gap-1.5 rounded-md border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-control)] px-2.5 text-xs font-medium text-[var(--forebrain-text-2)] transition hover:bg-[var(--forebrain-button-alt-bg)]"
      :disabled="disabled"
      :title="t('approval.pickerTitle')"
      data-testid="approval-picker"
      @click="open = !open"
    >
      <ShieldCheck class="size-3.5" aria-hidden="true" />
      <span>{{ currentLabel }}</span>
      <span class="text-[var(--forebrain-muted-text)]">▾</span>
    </button>
    <p
      v-if="error && !open"
      role="alert"
      class="absolute bottom-full left-0 z-20 mb-2 w-72 rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-surface)] px-2.5 py-2 text-[12px] text-[var(--forebrain-danger)]"
      data-testid="approval-picker-error"
    >
      {{ error }}
    </p>
    <div v-if="open" class="absolute bottom-full left-0 z-20 mb-2 w-72 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-1.5 shadow-[var(--forebrain-shadow-pop)]">
      <button
        v-for="preset in presets"
        :key="preset.id"
        type="button"
        class="block w-full rounded-lg px-2.5 py-2 text-left transition hover:bg-[var(--forebrain-button-alt-bg)]"
        :class="{ 'bg-[var(--forebrain-brand-soft)]': preset.id === current }"
        :data-testid="`approval-option-${preset.id}`"
        @click="choose(preset.id)"
      >
        <span class="flex items-center justify-between">
          <span class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ preset.label }}</span>
          <span v-if="preset.id === current" class="text-[var(--forebrain-brand-1)]">✓</span>
        </span>
        <span class="mt-0.5 block text-[11px] leading-snug text-[var(--forebrain-muted-text)]">{{ preset.description }}</span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ShieldCheck } from 'lucide-vue-next'
import { forebrainApi, getErrorMessage } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The session-scoped approval preset switch beside the composer. A choice
 * here applies to this conversation only and never writes forebrain.yaml —
 * the same rule the terminal's /permissions follows. What it shows is the
 * conversation's own state, read back from the server for each session the
 * page opens, so a choice made in one conversation never appears to carry
 * over to another.
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

const disabled = computed(() => switching.value || !props.sessionId)
const currentLabel = computed(() => {
  if (!props.sessionId) return t('approval.needSession')
  const preset = presets.value.find((item) => item.id === current.value)
  return preset ? preset.label : t('approval.followDefault')
})

// The read is per conversation: an answer that arrives after the page has
// moved to another session is that other session's no longer.
async function load(sessionId: string | null) {
  current.value = null
  error.value = ''
  if (!sessionId) return
  try {
    const res = await forebrainApi.sessionPresetCurrent(sessionId)
    if (props.sessionId === sessionId) current.value = res.current
  } catch (cause) {
    if (props.sessionId === sessionId) error.value = getErrorMessage(cause)
  }
}

async function choose(id: string) {
  const sessionId = props.sessionId
  if (switching.value || !sessionId) return
  switching.value = true
  open.value = false
  error.value = ''
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

onMounted(() => {
  document.addEventListener('pointerdown', onDocPointerDown)
})

onUnmounted(() => {
  document.removeEventListener('pointerdown', onDocPointerDown)
})
</script>
