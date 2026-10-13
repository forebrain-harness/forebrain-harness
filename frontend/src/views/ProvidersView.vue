<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto w-full max-w-5xl">
      <PageTopRow :title="t('providers.title')" :description="t('providers.description')">
        <template #actions>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving" data-testid="providers-add" @click="openAdd">
            {{ t('providers.add') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
            {{ t('common.refresh') }}
          </button>
        </template>
      </PageTopRow>

      <p v-if="error" class="mb-4 rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('providers.empty') }}</div>
      <div v-else class="forebrain-table-wrap">
        <table class="forebrain-table">
          <thead>
            <tr>
              <th>{{ t('providers.colOrder') }}</th>
              <th>{{ t('providers.colProvider') }}</th>
              <th>{{ t('providers.colModels') }}</th>
              <th>{{ t('providers.colBaseUrl') }}</th>
              <th>{{ t('providers.colKey') }}</th>
              <th class="text-right">{{ t('providers.colActions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, index) in rows" :key="index" :data-provider-row="row.provider || ''">
              <td>
                <span
                  class="rounded-full px-2 py-0.5 text-[11px]"
                  :class="index === 0 ? 'bg-[var(--forebrain-brand-soft)] text-[var(--forebrain-brand-1)]' : 'border border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]'"
                >{{ index === 0 ? t('providers.primary') : t('providers.fallback', { n: index }) }}</span>
              </td>
              <td>{{ row.provider }}</td>
              <td class="font-mono">{{ row.models.join('、') }}</td>
              <td class="font-mono text-[var(--forebrain-text-2)]">{{ row.baseUrl }}</td>
              <td>
                <span v-if="row.apiKeySet" class="rounded-full border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-2 py-0.5 font-mono text-[11px] text-[var(--forebrain-text-2)]" :data-testid="`provider-key-saved-${index}`">
                  {{ t('providers.keySaved', { hint: row.apiKeyHint ?? '' }) }}
                </span>
                <span v-else class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[11px] text-[var(--forebrain-muted-text)]" :data-testid="`provider-key-unset-${index}`">
                  {{ t('providers.unsetKey') }}
                </span>
              </td>
              <td class="text-right">
                <button v-if="index > 0" type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" :disabled="saving" :data-testid="`provider-up-${index}`" @click="move(index, -1)">
                  {{ t('providers.moveUp') }}
                </button>
                <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" :disabled="saving" :data-testid="`provider-edit-${index}`" @click="openEdit(index)">
                  {{ t('providers.edit') }}
                </button>
                <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px] text-[var(--forebrain-danger)]" :disabled="saving" :data-testid="`provider-remove-${index}`" @click="askRemove(index)">
                  {{ t('common.delete') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <ForebrainModal :open="modalOpen" :size="'md'" :title="editingIndex >= 0 ? t('providers.editModalTitle') : t('providers.addModalTitle')" @close="modalOpen = false">
      <div class="grid gap-3 sm:grid-cols-2">
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.providerLabel') }}</span>
          <input v-model="form.provider" list="provider-options" class="forebrain-field h-9" data-testid="provider-name" />
          <datalist id="provider-options">
            <option v-for="name in catalogProviders" :key="name" :value="name" />
          </datalist>
        </label>
        <label class="block">
          <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.baseUrlLabel') }}</span>
          <input v-model="form.baseUrl" class="forebrain-field h-9 font-mono" data-testid="provider-base-url" />
        </label>
      </div>

      <div>
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.modelsLabel') }}</span>
        <ModelChipsInput v-model="form.models" :suggestions="suggestionsFor(form.provider)" />
      </div>

      <div>
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.apiKeyLabel') }}</span>
        <div v-if="form.apiKeySet && !form.keyEdit" class="flex flex-wrap items-center gap-2">
          <span class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 font-mono text-[12px] text-[var(--forebrain-text-2)]" data-testid="provider-key-saved">
            {{ t('providers.keySaved', { hint: form.apiKeyHint ?? '' }) }}
          </span>
          <button type="button" class="forebrain-btn forebrain-btn-ghost h-9 px-3 text-xs" data-testid="provider-key-change" @click="form.keyEdit = true">
            {{ t('providers.changeKey') }}
          </button>
        </div>
        <input
          v-else
          v-model="form.apiKeyPlain"
          type="password"
          autocomplete="new-password"
          class="forebrain-field h-9 w-full font-mono"
          :placeholder="t('providers.apiKeyPlaceholder')"
          data-testid="provider-key-input"
        />
        <p class="mt-1 text-[11px] text-[var(--forebrain-muted-text)]">{{ t('providers.envHint') }}</p>
      </div>

      <details>
        <summary class="cursor-pointer text-[12px] text-[var(--forebrain-muted-text)]">{{ t('providers.advanced') }}</summary>
        <div class="mt-2 grid gap-2 sm:grid-cols-2">
          <label class="block">
            <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.apiPathLabel') }}</span>
            <input v-model="form.apiPath" class="forebrain-field h-9 font-mono" data-testid="provider-api-path" />
          </label>
          <label class="block">
            <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.paramsLabel') }}</span>
            <input v-model="form.paramsText" class="forebrain-field h-9 font-mono" placeholder='{"temperature":0.2}' data-testid="provider-params" />
          </label>
        </div>
      </details>

      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="modalOpen = false">{{ t('common.cancel') }}</button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving" data-testid="provider-save" @click="modalSave">
          {{ saving ? t('common.loading') : t('common.save') }}
        </button>
      </template>
    </ForebrainModal>

    <ConfirmDialog
      :open="removeIndex >= 0"
      :title="t('providers.deleteTitle')"
      :message="t('providers.deleteMessage')"
      :confirm-label="t('common.delete')"
      danger
      @cancel="removeIndex = -1"
      @confirm="confirmRemove"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * The model services this primary agent uses, in order: the first row is the
 * primary and the rest are fallbacks. The page is the list; adding and editing
 * happen in a modal, and every change — a modal save, a move, a delete — writes
 * the whole ordered list back in one round trip (the interface is whole-table).
 * The API key is typed once, masked as typed, stored as an ${ENV} reference by
 * the server and never echoed back — only its masked tail says one exists.
 */
import { onMounted, reactive, ref } from 'vue'
import PageTopRow from '@/components/common/PageTopRow.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
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

const modalOpen = ref(false)
const editingIndex = ref(-1)
const removeIndex = ref(-1)
const form = reactive<ProviderRow>(blankRow())

function blankRow(): ProviderRow {
  return { provider: '', baseUrl: '', apiPath: '', paramsText: '', models: [], apiKeySet: false, keyEdit: true, apiKeyPlain: '' }
}

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

function openAdd() {
  editingIndex.value = -1
  Object.assign(form, blankRow())
  modalOpen.value = true
}

function openEdit(index: number) {
  editingIndex.value = index
  Object.assign(form, { ...rows.value[index], apiKeyPlain: '', keyEdit: false })
  modalOpen.value = true
}

async function modalSave() {
  const row: ProviderRow = { ...form, models: [...form.models] }
  if (editingIndex.value >= 0) rows.value[editingIndex.value] = row
  else rows.value.push(row)
  modalOpen.value = false
  await persist()
}

function move(index: number, delta: number) {
  const target = index + delta
  if (target < 0 || target >= rows.value.length) return
  const next = [...rows.value]
  ;[next[index], next[target]] = [next[target], next[index]]
  rows.value = next
  void persist()
}

function askRemove(index: number) {
  removeIndex.value = index
}

function confirmRemove() {
  if (removeIndex.value >= 0) rows.value.splice(removeIndex.value, 1)
  removeIndex.value = -1
  void persist()
}

async function persist() {
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
