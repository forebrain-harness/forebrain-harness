<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6">
    <div class="mx-auto w-full max-w-3xl">
      <header class="mb-5 flex items-end justify-between gap-4">
        <div>
          <h1 class="font-serif text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">{{ t('providers.title') }}</h1>
          <p class="mt-1 text-[13px] leading-relaxed text-[var(--forebrain-text-2)]">{{ t('providers.description') }}</p>
        </div>
        <div class="flex gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">{{ t('common.refresh') }}</button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving" @click="save">{{ t('common.save') }}</button>
        </div>
      </header>

      <p v-if="error" class="mb-4 rounded-xl border border-[rgba(160,70,70,0.36)] bg-[rgba(160,70,70,0.08)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>
      <p v-if="notice" class="mb-4 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-text)]">{{ notice }}</p>
      <p class="mb-3 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('providers.orderNotice') }}</p>

      <ul class="space-y-2">
        <li v-for="(provider, idx) in providers" :key="idx" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3">
          <div class="mb-2 flex items-center justify-between gap-2">
            <span class="text-[12px] text-[var(--forebrain-muted-text)]">
              {{ idx === 0 ? t('providers.primary') : t('providers.fallback', { n: idx }) }}
            </span>
            <div class="flex gap-1">
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" :disabled="idx === 0" @click="move(idx, -1)">{{ t('providers.moveUp') }}</button>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px] text-[var(--forebrain-danger)]" @click="remove(idx)">{{ t('common.delete') }}</button>
            </div>
          </div>
          <div class="grid gap-2 sm:grid-cols-2">
            <input v-model="provider.provider" :placeholder="t('providers.providerPlaceholder')" class="forebrain-field" />
            <input v-model="provider.model" :placeholder="t('providers.modelPlaceholder')" class="forebrain-field" />
            <input v-model="provider.baseUrl" :placeholder="t('providers.baseUrlPlaceholder')" class="forebrain-field font-mono" />
            <input v-model="provider.apiKey" :placeholder="t('providers.apiKeyPlaceholder')" class="forebrain-field font-mono" />
          </div>
        </li>
      </ul>
      <button type="button" class="forebrain-btn forebrain-btn-ghost mt-3 text-xs" @click="add">{{ t('providers.add') }}</button>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The model providers of the current primary agent, in the order they are
 * tried. The first entry is the one the agent talks to; the rest are fallbacks,
 * which is why the order is editable rather than incidental.
 */
import { onMounted, ref } from 'vue'
import { getErrorMessage, forebrainApi, type ProviderRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()
const providers = ref<ProviderRecord[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')

function add() {
  providers.value = [...providers.value, { provider: '', model: '' }]
}

function remove(idx: number) {
  providers.value = providers.value.filter((_, i) => i !== idx)
}

function move(idx: number, delta: number) {
  const next = [...providers.value]
  const target = idx + delta
  if (target < 0 || target >= next.length) return
  ;[next[idx], next[target]] = [next[target], next[idx]]
  providers.value = next
}

async function load() {
  loading.value = true
  error.value = ''
  notice.value = ''
  try {
    providers.value = await forebrainApi.providers()
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
    await forebrainApi.saveProviders(providers.value)
    notice.value = t('providers.saved')
    await load()
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
