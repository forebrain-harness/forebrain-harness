<template>
  <div class="pb-6">
    <div class="mx-auto w-full max-w-4xl">
      <header class="mb-4 flex flex-wrap items-end justify-between gap-4">
        <div class="min-w-0">
          <h1 class="text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">{{ t('config.title') }}</h1>
          <p class="mt-1 break-all font-mono text-[12px] text-[var(--forebrain-muted-text)]">{{ path }}</p>
        </div>
        <div class="flex gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">{{ t('common.refresh') }}</button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving" @click="save">{{ t('config.apply') }}</button>
        </div>
      </header>

      <p class="mb-3 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('config.notice') }}</p>
      <p v-if="error" class="mb-4 whitespace-pre-wrap rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>
      <p v-if="notice" class="mb-4 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-text)]">{{ notice }}</p>

      <textarea
        v-model="yaml"
        spellcheck="false"
        rows="26"
        class="w-full rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-code-bg)] px-3 py-3 font-mono text-[12px] leading-relaxed text-[var(--forebrain-code-text)] outline-none focus:border-[var(--forebrain-focus-border)]"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * forebrain.yaml itself. The file is validated by the runtime before it is
 * written, so a mistake is reported here rather than at the next start; the
 * running process picks up the change immediately.
 */
import { onMounted, ref } from 'vue'
import { getErrorMessage, forebrainApi } from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()
const yaml = ref('')
const path = ref('')
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')

async function load() {
  loading.value = true
  error.value = ''
  notice.value = ''
  try {
    const res = await forebrainApi.configFile()
    yaml.value = res.yaml ?? ''
    path.value = res.path ?? ''
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    await forebrainApi.saveConfigFile(yaml.value)
    notice.value = t('config.applied')
    await load()
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
