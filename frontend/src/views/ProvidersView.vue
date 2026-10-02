<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6">
    <div class="mx-auto w-full max-w-3xl">
      <header class="mb-5 flex items-end justify-between gap-4">
        <div>
          <div class="flex items-center gap-2">
            <h1 class="text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">{{ t('providers.title') }}</h1>
            <span class="scope-badge">{{ t('scope.agent') }}</span>
          </div>
          <p class="mt-1 text-[13px] leading-relaxed text-[var(--forebrain-text-2)]">{{ t('providers.description') }}</p>
        </div>
        <div class="flex gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
            {{ t('common.refresh') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving" data-testid="providers-save" @click="save">
            {{ saving ? t('common.loading') : t('common.save') }}
          </button>
        </div>
      </header>

      <p v-if="error" class="mb-4 rounded-xl border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>

      <ul class="space-y-3">
        <li
          v-for="(row, index) in rows"
          :key="index"
          class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4"
          :data-provider-row="row.provider || index"
        >
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-[13px] font-medium text-[var(--forebrain-text)]">#{{ index + 1 }}</span>
            <span
              class="rounded-full px-2 py-0.5 text-[11px]"
              :class="index === 0 ? 'bg-[var(--forebrain-brand-soft)] text-[var(--forebrain-brand-1)]' : 'border border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]'"
            >{{ index === 0 ? t('providers.primary') : t('providers.fallback') }}</span>
            <div class="ml-auto flex gap-1">
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" :disabled="index === 0" :aria-label="t('providers.moveUp')" :data-testid="`provider-up-${index}`" @click="move(index, -1)">↑</button>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" :disabled="index === rows.length - 1" :aria-label="t('providers.moveDown')" :data-testid="`provider-down-${index}`" @click="move(index, 1)">↓</button>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px] text-[var(--forebrain-danger)]" :aria-label="t('providers.remove')" :data-testid="`provider-remove-${index}`" @click="removeRow(index)">×</button>
            </div>
          </div>

          <div class="mt-3 grid gap-2 sm:grid-cols-2">
            <label class="block">
              <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.providerLabel') }}</span>
              <input
                v-model="row.provider"
                list="provider-options"
                class="forebrain-field h-9"
                :data-testid="`provider-name-${index}`"
              />
              <datalist id="provider-options">
                <option v-for="name in catalogProviders" :key="name" :value="name" />
              </datalist>
            </label>
            <label class="block">
              <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.baseUrlLabel') }}</span>
              <input v-model="row.baseUrl" class="forebrain-field h-9 font-mono" :data-testid="`provider-base-url-${index}`" />
            </label>
          </div>

          <div class="mt-2">
            <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.modelsLabel') }}</span>
            <ModelChipsInput v-model="row.models" :suggestions="suggestionsFor(row.provider)" />
          </div>

          <div class="mt-3">
            <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.apiKeyLabel') }}</span>
            <div v-if="row.apiKeySet && !row.keyEdit" class="flex flex-wrap items-center gap-2">
              <span class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 font-mono text-[12px] text-[var(--forebrain-text-2)]" :data-testid="`provider-key-saved-${index}`">
                {{ t('providers.keySaved', { hint: row.apiKeyHint ?? '' }) }}
              </span>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-9 px-3 text-xs" :data-testid="`provider-key-change-${index}`" @click="row.keyEdit = true">
                {{ t('providers.changeKey') }}
              </button>
            </div>
            <input
              v-else
              v-model="row.apiKeyPlain"
              type="password"
              autocomplete="new-password"
              class="forebrain-field h-9 font-mono"
              :placeholder="t('providers.apiKeyPlaceholder')"
              :data-testid="`provider-key-input-${index}`"
            />
          </div>

          <details class="mt-3">
            <summary class="cursor-pointer text-[12px] text-[var(--forebrain-muted-text)]">{{ t('providers.advanced') }}</summary>
            <div class="mt-2 grid gap-2 sm:grid-cols-2">
              <label class="block">
                <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.apiPathLabel') }}</span>
                <input v-model="row.apiPath" class="forebrain-field h-9 font-mono" />
              </label>
              <label class="block">
                <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.paramsLabel') }}</span>
                <input v-model="row.paramsText" class="forebrain-field h-9 font-mono" placeholder='{"temperature":0.2}' />
              </label>
            </div>
          </details>
        </li>
      </ul>

      <button type="button" class="mt-3 w-full rounded-xl border border-dashed border-[var(--forebrain-divider)] px-4 py-3 text-sm text-[var(--forebrain-muted-text)] hover:bg-[var(--forebrain-input-hover-bg)]" data-testid="providers-add" @click="addRow">
        + {{ t('providers.add') }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The model services this primary agent uses, in order: the first row is the
 * primary and the rest are fallbacks. The API key is typed once, masked as
 * typed, stored as an ${ENV} reference by the server and never echoed back —
 * only its masked tail says one exists.
 */
import { onMounted, ref } from 'vue'
import ModelChipsInput from '@/components/providers/ModelChipsInput.vue'
import { getErrorMessage, forebrainApi, type ProviderRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()

type ProviderRow = {
  provider: string
  baseUrl: string
  apiPath: string
  paramsText: string
  models: string[]
  apiKeySet: boolean
  apiKeyHint?: string
  keyEdit: boolean
  apiKeyPlain: string
}

const rows = ref<ProviderRow[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const catalogProviders = ref<string[]>([])
const catalogModels = ref<Record<string, string[]>>({})

function toRow(record: ProviderRecord): ProviderRow {
  return {
    provider: record.provider ?? '',
    baseUrl: record.baseUrl ?? '',
    apiPath: record.apiPath ?? '',
    paramsText: typeof record.params === 'string' ? record.params : record.params ? JSON.stringify(record.params) : '',
    models: [...(record.models ?? [])],
    apiKeySet: Boolean(record.apiKeySet),
    apiKeyHint: record.apiKeyHint,
    keyEdit: false,
    apiKeyPlain: '',
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    rows.value = (await forebrainApi.providers()).map(toRow)
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

async function loadCatalog() {
  try {
    const listing = await forebrainApi.getModels({ limit: 500 })
    const providers = new Set<string>()
    const byProvider: Record<string, string[]> = {}
    for (const record of listing.records) {
      const provider = String(record.provider ?? record.modelId?.split('/')[0] ?? '').trim()
      if (!provider) continue
      providers.add(provider)
      byProvider[provider] = byProvider[provider] ?? []
      const model = String(record.modelId ?? '')
      if (model && byProvider[provider].length < 20) byProvider[provider].push(model)
    }
    catalogProviders.value = Array.from(providers).sort()
    catalogModels.value = byProvider
  } catch {
    // The catalog is a convenience: without it the forms still work by hand.
    catalogProviders.value = []
    catalogModels.value = {}
  }
}

function suggestionsFor(provider: string): string[] {
  return catalogModels.value[provider.trim()] ?? []
}

function addRow() {
  rows.value.push({
    provider: '',
    baseUrl: '',
    apiPath: '',
    paramsText: '',
    models: [],
    apiKeySet: false,
    keyEdit: true,
    apiKeyPlain: '',
  })
}

function removeRow(index: number) {
  rows.value.splice(index, 1)
}

function move(index: number, delta: number) {
  const target = index + delta
  if (target < 0 || target >= rows.value.length) return
  const next = [...rows.value]
  ;[next[index], next[target]] = [next[target], next[index]]
  rows.value = next
}

async function save() {
  saving.value = true
  error.value = ''
  try {
    await forebrainApi.saveProviders(
      rows.value.map((row) => {
        const body: Record<string, unknown> = {
          provider: row.provider.trim(),
          models: row.models,
          base_url: row.baseUrl.trim(),
          api_path: row.apiPath.trim(),
        }
        if (row.paramsText.trim()) body.params = JSON.parse(row.paramsText)
        // The key travels only when the user typed one; an untouched key is
        // the server's to keep.
        if (row.apiKeyPlain.trim()) body.api_key_plain = row.apiKeyPlain.trim()
        return body
      }),
    )
    await load()
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  void load()
  void loadCatalog()
})
</script>
