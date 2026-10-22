<template>
  <!-- The memory switches, flat in their tab (owner 2026-10-18): one row per
       switch, the help that says what they cover, and the save that writes
       them. -->
  <div class="grid gap-3">
    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>
    <p v-if="notice" class="text-[12px] text-[var(--forebrain-text-2)]">{{ notice }}</p>

    <div class="grid">
      <SwitchRow
        v-for="row in switches"
        :key="row.key"
        v-model="draft[row.key]"
        :title="row.title"
        :hint="row.description"
        :disabled="loading || saving"
        :data-testid="`memory-switch-${row.key}`"
      />
    </div>

    <p class="forebrain-list-note">{{ t('memories.switchesDescription') }}</p>

    <div class="flex flex-wrap items-center gap-2">
      <Button
        type="button"
        class="forebrain-btn forebrain-btn-primary h-auto text-xs"
        :disabled="!changed || saving"
        data-testid="memory-switches-save"
        @click="saveSettings"
      >
        {{ saving ? t('common.loading') : t('common.save') }}
      </Button>
      <Button
        type="button"
        class="forebrain-btn forebrain-btn--danger h-auto bg-transparent text-xs hover:bg-transparent"
        :disabled="resetting"
        data-testid="memory-switches-reset"
        @click="resetMemories"
      >
        {{ resetting ? t('common.loading') : t('memories.reset') }}
      </Button>
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
import { Button } from '@repo/shadcn-vue/components/ui/button'
import SwitchRow from '@/components/common/SwitchRow.vue'
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
