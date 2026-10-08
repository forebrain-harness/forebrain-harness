<template>
  <div class="pb-6">
    <p v-if="error" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>
    <p v-if="notice" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-text)]">{{ notice }}</p>

    <CardComponent class="mt-3">
      <div class="space-y-4">
        <label v-for="row in switches" :key="row.key" class="flex items-start justify-between gap-4">
          <span class="min-w-0">
            <span class="block text-[13px] font-medium text-[var(--forebrain-text)]">{{ row.title }}</span>
            <span class="mt-0.5 block text-[12px] text-[var(--forebrain-muted-text)]">{{ row.description }}</span>
          </span>
          <SwitchComponent
            :model-value="draft[row.key]"
            :disabled="loading || saving"
            class="mt-0.5"
            @update:model-value="draft[row.key] = $event"
          />
        </label>
      </div>
      <div class="flex gap-2">
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!changed || saving" @click="saveSettings">
          {{ saving ? t('common.loading') : t('common.save') }}
        </button>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs text-[var(--forebrain-danger)]" :disabled="resetting" @click="resetMemories">
          {{ resetting ? t('common.loading') : t('memories.reset') }}
        </button>
      </div>
    </CardComponent>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import CardComponent from '@/components/common/CardComponent.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import forebrainApi, { type MemorySettings } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The three memory switches are global runtime flags, so they live in the
 * settings page's memory tab. /memories keeps its page for plan 012 to
 * rebuild around the memory file list.
 */
const { t } = useI18n()
const settings = ref<MemorySettings>({ enabled: false, useMemories: false, generateMemories: false })
const draft = reactive<MemorySettings>({ ...settings.value })
const loading = ref(false)
const saving = ref(false)
const resetting = ref(false)
const error = ref('')
const notice = ref('')

const switches = computed(() => [
  { key: 'enabled' as const, title: t('memories.feature'), description: t('memories.featureDescription') },
  { key: 'useMemories' as const, title: t('memories.use'), description: t('memories.useDescription') },
  { key: 'generateMemories' as const, title: t('memories.generate'), description: t('memories.generateDescription') },
])

const changed = computed(() =>
  draft.enabled !== settings.value.enabled ||
  draft.useMemories !== settings.value.useMemories ||
  draft.generateMemories !== settings.value.generateMemories,
)

async function loadSettings() {
  loading.value = true
  error.value = ''
  try {
    const next = await forebrainApi.memorySettings()
    settings.value = { ...next }
    Object.assign(draft, next)
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause)
  } finally {
    loading.value = false
  }
}

async function saveSettings() {
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    const next = await forebrainApi.updateMemorySettings({
      featureEnabled: draft.enabled,
      useMemories: draft.useMemories,
      generateMemories: draft.generateMemories,
    })
    settings.value = { ...next }
    Object.assign(draft, next)
    notice.value = t('memories.saved')
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause)
  } finally {
    saving.value = false
  }
}

async function resetMemories() {
  if (!window.confirm(t('memories.resetConfirm'))) return
  resetting.value = true
  error.value = ''
  notice.value = ''
  try {
    await forebrainApi.resetMemories()
    notice.value = t('memories.resetDone')
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause)
  } finally {
    resetting.value = false
  }
}

onMounted(() => {
  void loadSettings()
})
</script>
