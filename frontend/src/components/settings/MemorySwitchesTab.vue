<template>
  <div class="pb-6">
    <PageTopRow :title="t('memories.featureTitle')" :description="t('memories.switchesDescription')">
      <template #actions>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!changed || saving" data-testid="memory-switches-save" @click="saveSettings">
          {{ saving ? t('common.loading') : t('common.save') }}
        </button>
      </template>
    </PageTopRow>

    <p v-if="error" class="mb-3 text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>
    <p v-if="notice" class="mb-3 text-[12px] text-[var(--forebrain-brand-1)]">{{ notice }}</p>

    <div class="forebrain-list-boxed">
      <label v-for="row in switches" :key="row.key" class="flex items-start justify-between gap-4 py-3">
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

    <div class="mt-3">
      <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs text-[var(--forebrain-danger)]" :disabled="resetting" @click="resetMemories">
        {{ resetting ? t('common.loading') : t('memories.reset') }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The three memory switches are global runtime flags, so they live in the
 * settings page's memory tab as one flat group. /memories keeps its page for
 * the memory file list.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import PageTopRow from '@/components/common/PageTopRow.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import forebrainApi, { type MemorySettings } from '@/lib/api'
import { useI18n } from '@/locales'

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
