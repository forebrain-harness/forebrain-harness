<template>
  <div class="relative flex items-center" ref="menuRef">
    <button
      type="button"
      class="inline-flex h-8 items-center gap-1.5 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-control)] px-2.5 text-xs font-medium text-[var(--forebrain-text-2)] transition hover:bg-[var(--forebrain-button-alt-bg)]"
      :disabled="disabled"
      :title="t('approval.pickerTitle')"
      data-testid="approval-picker"
      @click="open = !open"
    >
      <ShieldCheck class="size-3.5" aria-hidden="true" />
      <span>{{ currentLabel }}</span>
      <span class="text-[var(--forebrain-muted-text)]">▾</span>
    </button>
    <div v-if="open" class="absolute bottom-full left-0 z-20 mb-2 w-72 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-1.5 shadow-[var(--forebrain-shadow-pop)]">
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
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ShieldCheck } from 'lucide-vue-next'
import { forebrainApi } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The session-scoped approval preset switch beside the composer. A choice
 * here applies to this conversation only and never writes forebrain.yaml —
 * the same rule the terminal's /permissions follows.
 */
const props = defineProps<{ sessionId: string | null }>()

const { t } = useI18n()

type Preset = { id: string; label: string; description: string }

const presets: Preset[] = [
  { id: 'read-only', label: t('approval.readOnly'), description: t('approval.readOnlyDescription') },
  { id: 'auto', label: t('approval.default'), description: t('approval.defaultDescription') },
  { id: 'full-access', label: t('approval.fullAccess'), description: t('approval.fullAccessDescription') },
]

const open = ref(false)
const current = ref<string | null>(null)
const switching = ref(false)
const menuRef = ref<HTMLElement | null>(null)

const disabled = computed(() => switching.value || !props.sessionId)
const currentLabel = computed(() => {
  if (!props.sessionId) return t('approval.needSession')
  const preset = presets.find((item) => item.id === current.value)
  return preset ? preset.label : t('approval.followDefault')
})

async function load() {
  try {
    const res = await forebrainApi.approvalDefault()
    current.value = res.current
  } catch {
    current.value = null
  }
}

async function choose(id: string) {
  if (switching.value || !props.sessionId) return
  switching.value = true
  open.value = false
  try {
    await forebrainApi.sessionPreset(props.sessionId, id)
    current.value = id
  } catch {
    // The picker keeps its previous label; the composer shows load errors.
  } finally {
    switching.value = false
  }
}

function onDocPointerDown(event: PointerEvent) {
  if (!menuRef.value?.contains(event.target as Node)) open.value = false
}

onMounted(() => {
  document.addEventListener('pointerdown', onDocPointerDown)
  void load()
})

onUnmounted(() => {
  document.removeEventListener('pointerdown', onDocPointerDown)
})
</script>
