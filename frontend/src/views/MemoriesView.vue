<template>
  <div class="flex flex-1 flex-col bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6">
    <div class="mx-auto w-full max-w-3xl">
      <div class="mb-6 flex items-end justify-between gap-4">
        <div>
          <h1 class="font-serif text-[1.6rem] font-medium leading-tight tracking-tight text-[var(--forebrain-text)]">{{ t('memories.title') }}</h1>
          <p class="mt-1 max-w-xl text-[15px] leading-relaxed text-[var(--forebrain-text-2)]">{{ t('memories.description') }}</p>
        </div>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="loadSettings">
          {{ t('common.refresh') }}
        </button>
      </div>

      <p v-if="error" class="mb-4 rounded-xl border border-[rgba(160,70,70,0.36)] bg-[rgba(160,70,70,0.08)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>
      <p v-if="notice" class="mb-4 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-text)]">{{ notice }}</p>

      <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-5 forebrain-doc-shadow">
        <div v-if="loading && !loaded" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
        <div v-else class="space-y-5">
          <label class="flex items-start justify-between gap-5">
            <span>
              <span class="block text-sm font-semibold text-[var(--forebrain-text)]">{{ t('memories.feature') }}</span>
              <span class="mt-1 block text-xs leading-relaxed text-[var(--forebrain-muted-text)]">{{ t('memories.featureDescription') }}</span>
            </span>
            <input v-model="draft.enabled" type="checkbox" class="mt-1 h-5 w-5 accent-[var(--forebrain-brand-1)]" />
          </label>

          <div class="border-t border-[var(--forebrain-divider)]" />

          <label class="flex items-start justify-between gap-5" :class="!draft.enabled ? 'opacity-50' : ''">
            <span>
              <span class="block text-sm font-semibold text-[var(--forebrain-text)]">{{ t('memories.use') }}</span>
              <span class="mt-1 block text-xs leading-relaxed text-[var(--forebrain-muted-text)]">{{ t('memories.useDescription') }}</span>
            </span>
            <input v-model="draft.useMemories" :disabled="!draft.enabled" type="checkbox" class="mt-1 h-5 w-5 accent-[var(--forebrain-brand-1)]" />
          </label>

          <label class="flex items-start justify-between gap-5" :class="!draft.enabled ? 'opacity-50' : ''">
            <span>
              <span class="block text-sm font-semibold text-[var(--forebrain-text)]">{{ t('memories.generate') }}</span>
              <span class="mt-1 block text-xs leading-relaxed text-[var(--forebrain-muted-text)]">{{ t('memories.generateDescription') }}</span>
            </span>
            <input v-model="draft.generateMemories" :disabled="!draft.enabled" type="checkbox" class="mt-1 h-5 w-5 accent-[var(--forebrain-brand-1)]" />
          </label>

          <div class="flex justify-end">
            <button type="button" class="forebrain-btn forebrain-btn-primary" :disabled="saving || !changed" @click="saveSettings">
              {{ saving ? t('common.processing') : t('memories.save') }}
            </button>
          </div>
        </div>
      </section>

      <section class="mt-4 rounded-2xl border border-[rgba(160,70,70,0.28)] bg-[var(--forebrain-surface)] p-5">
        <h2 class="text-sm font-semibold text-[var(--forebrain-text)]">{{ t('memories.reset') }}</h2>
        <p class="mt-1 text-xs leading-relaxed text-[var(--forebrain-muted-text)]">{{ t('memories.resetDescription') }}</p>
        <div class="mt-4 flex justify-end">
          <button type="button" class="rounded-xl border border-[rgba(160,70,70,0.4)] px-4 py-2 text-sm font-medium text-[var(--forebrain-danger)] hover:bg-[rgba(160,70,70,0.08)]" :disabled="resetting" @click="resetMemories">
            {{ resetting ? t('common.processing') : t('memories.resetAction') }}
          </button>
        </div>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import forebrainApi, { getUser, type MemorySettings } from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()
const settings = ref<MemorySettings>({ enabled: false, useMemories: false, generateMemories: false })
const draft = reactive<MemorySettings>({ ...settings.value })
const loading = ref(false)
const loaded = ref(false)
const saving = ref(false)
const resetting = ref(false)
const error = ref('')
const notice = ref('')

const changed = computed(() =>
  draft.enabled !== settings.value.enabled ||
  draft.useMemories !== settings.value.useMemories ||
  draft.generateMemories !== settings.value.generateMemories,
)

function assignSettings(next: MemorySettings) {
  settings.value = { ...next }
  Object.assign(draft, next)
}

async function loadSettings() {
  loading.value = true
  error.value = ''
  try {
    assignSettings(await forebrainApi.memorySettings())
    loaded.value = true
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
    const enabledNow = !settings.value.enabled && draft.enabled
    assignSettings(await forebrainApi.updateMemorySettings({
      featureEnabled: draft.enabled,
      useMemories: draft.useMemories,
      generateMemories: draft.generateMemories,
      threadId: getUser()?.lastSessionId ?? undefined,
    }))
    notice.value = enabledNow ? t('memories.enabledNotice') : t('memories.saved')
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

onMounted(loadSettings)
</script>
