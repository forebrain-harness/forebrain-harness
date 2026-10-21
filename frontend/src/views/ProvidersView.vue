<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto flex w-full max-w-5xl flex-col gap-4">
      <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <ListToolbar>
        <template #left>
          <input
            v-model="q"
            type="search"
            :placeholder="t('providers.searchPlaceholder')"
            class="forebrain-field h-[34px]"
            style="max-width: 240px"
            data-testid="providers-search"
          />
        </template>
        <template #actions>
          <button type="button" class="forebrain-btn forebrain-btn-primary gap-1.5 text-xs" :disabled="saving" data-testid="providers-add" @click="openAdd">
            <Plus class="size-3.5" aria-hidden="true" />{{ t('providers.add') }}
          </button>
          <button
            type="button"
            class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs"
            :disabled="saving"
            data-testid="providers-connect-opencode"
            @click="openConnect('opencode')"
          >
            <LogIn class="size-3.5" aria-hidden="true" />{{ t('providers.connectOpenCode') }}
          </button>
          <button
            type="button"
            class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs"
            :disabled="saving"
            data-testid="providers-connect-chatgpt"
            @click="openConnect('chatgpt')"
          >
            <LogIn class="size-3.5" aria-hidden="true" />{{ t('providers.connectChatGPT') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" :disabled="loading" @click="refresh">
            <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
          </button>
        </template>
      </ListToolbar>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('providers.empty') }}</div>
      <div v-else class="forebrain-table-wrap">
        <table class="forebrain-table forebrain-table--fixed">
          <colgroup>
            <col style="width: 70px" />
            <col style="width: 140px" />
            <col />
            <col style="width: 220px" />
            <col style="width: 150px" />
            <col style="width: 110px" />
          </colgroup>
          <thead>
            <tr>
              <th>{{ t('providers.colOrder') }}</th>
              <th>{{ t('providers.colProvider') }}</th>
              <th>{{ t('providers.colModels') }}</th>
              <th>{{ t('providers.colBaseUrl') }}</th>
              <th>{{ t('providers.colKey') }}</th>
              <th class="text-center">{{ t('providers.colActions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in rows" :key="entryKey(row)" :data-provider-row="row.provider || ''">
              <td>
                <BadgeComponent v-if="rowIndex(row) === 0">{{ t('providers.primary') }}</BadgeComponent>
                <BadgeComponent v-else variant="gray">{{ t('providers.fallback', { n: rowIndex(row) }) }}</BadgeComponent>
              </td>
              <td>
                <span class="forebrain-cell-1" v-tip:top.trunc="row.provider">{{ row.provider }}</span>
              </td>
              <td class="font-mono">
                <span class="forebrain-cell-1" v-tip:top.trunc="row.models.join('、')">{{ row.models.join('、') }}</span>
              </td>
              <td class="font-mono text-[var(--forebrain-text-2)]">
                <span class="forebrain-cell-1" v-tip:top.trunc="row.baseUrl || ''">{{ row.baseUrl }}</span>
              </td>
              <td>
                <BadgeComponent v-if="row.apiKeySet" variant="success" :data-testid="`provider-key-saved-${rowIndex(row)}`">
                  {{ t('providers.keySavedBadge', { hint: row.apiKeyHint ?? '' }) }}
                </BadgeComponent>
                <BadgeComponent v-else variant="warning" :data-testid="`provider-key-unset-${rowIndex(row)}`">{{ t('providers.unsetKey') }}</BadgeComponent>
              </td>
              <td class="text-right">
                <RowActions>
                  <RowActionSlot v-if="rowIndex(row) === 0" />
                  <RowAction
                    v-else
                    :icon="ArrowUp"
                    :label="t('providers.moveUp')"
                    :name="row.provider"
                    :disabled="saving"
                    :data-testid="`provider-up-${rowIndex(row)}`"
                    @click="moveUp(row)"
                  />
                  <RowAction
                    :icon="Pencil"
                    :label="t('providers.edit')"
                    :name="row.provider"
                    :disabled="saving"
                    :data-testid="`provider-edit-${rowIndex(row)}`"
                    @click="openEdit(row)"
                  />
                  <RowAction
                    :icon="Trash2"
                    :label="t('common.delete')"
                    :name="row.provider"
                    tone="danger"
                    :disabled="saving"
                    :data-testid="`provider-remove-${rowIndex(row)}`"
                    @click="askRemove(row)"
                  />
                </RowActions>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      <ListNote>{{ t('providers.description') }}</ListNote>
    </div>

    <ForebrainModal :open="modalOpen" :size="'md'" :title="editingKey !== null ? t('providers.editModalTitle') : t('providers.addModalTitle')" @close="modalOpen = false">
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

    <ForebrainModal :open="connectProvider !== null" size="md" :title="connectTitle" @close="closeConnect">
      <!-- The free tier first: it needs no account, so it is offered before
           anything starts a login. -->
      <template v-if="connectStep === 'start'">
        <p class="text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.connectStartHint') }}</p>
        <div class="flex flex-wrap gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-primary h-9 px-3 text-xs" data-testid="provider-login-start" @click="beginConnectLogin('opencode')">
            {{ t('providers.connectSignIn') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-ghost h-9 px-3 text-xs" data-testid="provider-login-free" @click="chooseFreeModels">
            {{ t('providers.connectFreeOnly') }}
          </button>
        </div>
      </template>

      <template v-else-if="connectStep === 'auth'">
        <p class="text-[12px] text-[var(--forebrain-text-2)]">{{ connectAuthHint }}</p>
        <div v-if="connectProvider === 'opencode'" class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] px-4 py-3">
          <span class="block text-[11px] text-[var(--forebrain-muted-text)]">{{ t('providers.connectCodeLabel') }}</span>
          <span class="font-mono text-2xl tracking-[0.2em] text-[var(--forebrain-text)]" data-testid="provider-login-code">
            {{ connectCode || t('providers.connectCodePending') }}
          </span>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <button
            type="button"
            class="forebrain-btn forebrain-btn-primary h-9 px-3 text-xs"
            :disabled="!connectUrl"
            data-testid="provider-login-open"
            @click="openAuthorizePage"
          >
            {{ t('providers.connectOpenPage') }}
          </button>
          <Loader :size="14" class="text-[var(--forebrain-muted-text)]" data-testid="provider-login-spinner" />
          <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('providers.connectWaiting') }}</span>
        </div>
        <p v-if="connectProvider === 'chatgpt'" class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('providers.connectChatGPTLocal') }}</p>
      </template>

      <!-- The catalog the finished login belongs to: the free tier after the
           no-login choice, the Go subscription after signing in, ChatGPT's
           account after its callback. -->
      <template v-else-if="connectStep === 'choose'">
        <p class="text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.connectChooseHint') }}</p>
        <Command class="rounded-lg border border-[var(--forebrain-divider)]" data-testid="provider-login-models">
          <CommandInput :placeholder="t('providers.connectSearchModels')" data-testid="provider-login-model-search" />
          <CommandList>
            <CommandEmpty>{{ t('providers.connectNoModels') }}</CommandEmpty>
            <CommandGroup>
              <CommandItem
                v-for="record in connectModels"
                :key="record.modelId"
                :value="record.modelName ?? record.apiModel"
                :data-testid="`provider-login-model-${modelIdOf(record)}`"
                @select="connectChoice = modelIdOf(record)"
              >
                <span class="flex-1 truncate text-left">{{ record.modelName }}</span>
                <Check v-if="connectChoice === modelIdOf(record)" class="size-3.5" aria-hidden="true" />
              </CommandItem>
            </CommandGroup>
          </CommandList>
        </Command>
      </template>

      <template v-else>
        <p class="text-[13px] text-[var(--forebrain-danger)]" data-testid="provider-login-error">
          {{ connectStep === 'lost' ? t('providers.connectLost') : connectError }}
        </p>
        <p class="text-[12px] text-[var(--forebrain-text-2)]" data-testid="provider-login-hint">
          {{ connectStep === 'lost' ? t('providers.connectLostHint') : t('providers.connectRetryHint') }}
        </p>
      </template>

      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" data-testid="provider-login-cancel" @click="closeConnect">
          {{ connectStep === 'auth' ? t('providers.cancelLogin') : t('common.close') }}
        </button>
        <button
          v-if="connectStep === 'failed' || connectStep === 'lost'"
          type="button"
          class="forebrain-btn forebrain-btn-primary text-xs"
          data-testid="provider-login-retry"
          @click="restartConnect"
        >
          {{ t('providers.connectRetry') }}
        </button>
        <button
          v-else-if="connectStep === 'choose'"
          type="button"
          class="forebrain-btn forebrain-btn-primary text-xs"
          :disabled="!connectChoice || saving"
          data-testid="provider-login-save"
          @click="saveConnectModel"
        >
          {{ saving ? t('common.loading') : t('providers.connectSave') }}
        </button>
      </template>
    </ForebrainModal>

    <ConfirmDialog
      :open="removeKey !== null"
      :title="t('providers.deleteTitle')"
      :message="t('providers.deleteMessage')"
      :confirm-label="t('common.delete')"
      danger
      @cancel="removeKey = null"
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
 *
 * The table is paged by the server (plan 073) but the order it pages through is
 * the whole fallback order, so each row carries its position in that order and
 * 上移 swaps with the row before it there — never with the row above it on the
 * page. A write therefore reads the whole list back, changes it, and saves it.
 *
 * Two of those services are signed in to rather than typed in. The flows run in
 * the gateway (a page cannot print a status line or open a window on the machine
 * the gateway sits on), so the modal is a poll view: it shows the device flow's
 * code or ChatGPT's authorization link, then the catalog the finished login
 * unlocks, and writes the chosen model back into the ordered list like any other
 * row. OpenCode's free tier skips all of it — no account, one catalog listing.
 */
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { ArrowUp, Check, LogIn, Pencil, Plus, RotateCw, Trash2 } from 'lucide-vue-next'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@repo/shadcn-vue/components/ui/command'
import { Loader } from '@repo/elements/loader'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import RowActionSlot from '@/components/common/RowActionSlot.vue'
import TablePager from '@/components/common/TablePager.vue'
import ModelChipsInput from '@/components/providers/ModelChipsInput.vue'
import { useServerList } from '@/composables/useServerList'
import {
  forebrainApi,
  GatewayHttpError,
  getErrorMessage,
  type ModelRecord,
  type ProviderLoginProvider,
  type ProviderRecord,
} from '@/lib/api'
import { useI18n } from '@/locales'

// The inference root OpenCode's free "no account" entry writes: the Console's,
// which serves the free family. It mirrors the backend's
// opencode.ProviderBaseURL("opencode") — Complete() requires a base URL, so a
// row with none would be refused. The signed-in entry needs no copy here: the
// Go subscription's root comes from the login's own completion answer, which is
// the one place that decides it.
const OPENCODE_BASE_URL = 'https://opencode.ai/inference'

// The poll period on the gateway's own status endpoint. It is unrelated to the
// device flow's own token interval (5s inside the gateway): this one only asks a
// local process what it has learned so far.
const CONNECT_POLL_MS = 1000

/** What one sign-in unlocks, and what the row it writes carries. */
type ConnectTarget = {
  /** The provider id the row gets. */
  provider: string
  /** The catalog listing asked for the models. */
  catalog: string
  /** The inference root that catalog is served from. */
  baseUrl: string
}

const { t } = useI18n()

/** What the add/edit modal edits. */
type ProviderDraft = {
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

/** One entry of the whole ordered list as this page writes it back. */
type ProviderEntry = ProviderRecord & { apiKeyPlain?: string }

const { rows, total, page, pageSize, q, loading, error, reload } = useServerList<ProviderRecord>(forebrainApi.providersPage, {
  pageSize: 10,
})

const saving = ref(false)
const catalogProviders = ref<string[]>([])
const catalogModels = ref<Record<string, string[]>>({})

const modalOpen = ref(false)
// The entry a dialog acts on, named by what it is rather than by where it sat
// when the dialog opened.
const editingKey = ref<string | null>(null)
const removeKey = ref<string | null>(null)
const form = reactive<ProviderDraft>(blankDraft())

function blankDraft(): ProviderDraft {
  return { provider: '', baseUrl: '', apiPath: '', paramsText: '', models: [], apiKeySet: false, keyEdit: true, apiKeyPlain: '' }
}

/** A row's position in the whole order, as the list draws it (主用 / 回退 N). */
function rowIndex(row: ProviderRecord): number {
  return row.index ?? 0
}

/**
 * What a provider entry *is*: its own fields, not its place in the order. A
 * write that acted on a position read before another write landed would move
 * (or delete) a neighbour instead — two quick clicks on 上移 would cancel out,
 * and a 删除 right after one could take the wrong row with it.
 */
function entryKey(entry: ProviderRecord): string {
  return [String(entry.provider ?? '').trim(), String(entry.baseUrl ?? '').trim(), [...(entry.models ?? [])].join('|')].join('\u0000')
}

function toDraft(record: ProviderRecord): ProviderDraft {
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

/** One entry of the list as the PUT takes it. */
function toBody(entry: ProviderEntry): Record<string, unknown> {
  const body: Record<string, unknown> = {
    provider: String(entry.provider ?? '').trim(),
    models: [...(entry.models ?? [])],
    base_url: String(entry.baseUrl ?? '').trim(),
    api_path: String(entry.apiPath ?? '').trim(),
  }
  const params = typeof entry.params === 'string' ? entry.params.trim() : entry.params ? JSON.stringify(entry.params) : ''
  if (params) body.params = JSON.parse(params)
  // The key travels only when the user typed one; an untouched key is the
  // server's to keep.
  if (entry.apiKeyPlain?.trim()) body.api_key_plain = entry.apiKeyPlain.trim()
  return body
}

/**
 * The list is whole-table on the wire, so a change reads the whole order back,
 * applies itself to it, and saves it: writing the page alone would delete every
 * row the page does not show. Writes run one after another — each reads the
 * order the previous one wrote — so two quick clicks move a row two places
 * instead of both working from the same stale snapshot and undoing each other.
 */
let pendingWrites = 0
let lastWrite: Promise<void> = Promise.resolve()

function writeWholeOrder(change: (all: ProviderEntry[]) => void): Promise<void> {
  pendingWrites += 1
  saving.value = true
  const write = lastWrite.then(async () => {
    error.value = ''
    try {
      const all: ProviderEntry[] = await forebrainApi.providers()
      change(all)
      await forebrainApi.saveProviders(all.map(toBody))
      await reload()
    } catch (cause) {
      error.value = getErrorMessage(cause)
    } finally {
      pendingWrites -= 1
      if (pendingWrites === 0) saving.value = false
    }
  })
  lastWrite = write
  return write
}

function refresh(): void {
  void reload()
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
  editingKey.value = null
  Object.assign(form, blankDraft())
  modalOpen.value = true
}

function openEdit(row: ProviderRecord) {
  editingKey.value = entryKey(row)
  Object.assign(form, toDraft(row))
  modalOpen.value = true
}

async function modalSave() {
  const key = editingKey.value
  const entry: ProviderEntry = {
    provider: form.provider.trim(),
    baseUrl: form.baseUrl.trim(),
    apiPath: form.apiPath.trim(),
    params: form.paramsText.trim(),
    models: [...form.models],
    apiKeyPlain: form.apiKeyPlain,
  }
  modalOpen.value = false
  await writeWholeOrder((all) => {
    const at = key === null ? -1 : all.findIndex((candidate) => entryKey(candidate) === key)
    if (at >= 0) all[at] = { ...all[at], ...entry }
    else all.push(entry)
  })
}

function moveUp(row: ProviderRecord) {
  const key = entryKey(row)
  void writeWholeOrder((all) => {
    const at = all.findIndex((candidate) => entryKey(candidate) === key)
    if (at <= 0) return
    ;[all[at - 1], all[at]] = [all[at], all[at - 1]]
  })
}

function askRemove(row: ProviderRecord) {
  removeKey.value = entryKey(row)
}

function confirmRemove() {
  const key = removeKey.value
  removeKey.value = null
  if (key === null) return
  void writeWholeOrder((all) => {
    const at = all.findIndex((candidate) => entryKey(candidate) === key)
    if (at < 0) return
    all.splice(at, 1)
  })
}

/* ---- signing in to a service -------------------------------------------- */

/** Which sign-in the modal is running; null is the modal being closed. */
const connectProvider = ref<ProviderLoginProvider | null>(null)
/** The modal's step: the OpenCode choice, a running flow, the catalog it
 * unlocked, or how the flow ended. */
const connectStep = ref<'start' | 'auth' | 'choose' | 'failed' | 'lost'>('start')
const connectLoginId = ref('')
const connectUrl = ref('')
const connectCode = ref('')
const connectError = ref('')
const connectModels = ref<ModelRecord[]>([])
const connectChoice = ref('')
const connectTarget = ref<ConnectTarget | null>(null)

const connectTitle = computed(() =>
  t('providers.connectTitle', { name: connectProvider.value === 'chatgpt' ? 'ChatGPT' : 'OpenCode' }),
)

const connectAuthHint = computed(() =>
  connectProvider.value === 'chatgpt' ? t('providers.connectChatGPTHint') : t('providers.connectOpenCodeHint'),
)

let connectTimer: ReturnType<typeof setTimeout> | null = null

function modelIdOf(record: ModelRecord): string {
  return String(record.apiModel ?? record.modelId ?? '').trim()
}

function stopConnectPoll() {
  if (connectTimer) {
    clearTimeout(connectTimer)
    connectTimer = null
  }
}

/** Clears everything a previous sign-in left on screen, so reopening the modal
 * never shows the last one's code or error. */
function resetConnect() {
  stopConnectPoll()
  connectLoginId.value = ''
  connectUrl.value = ''
  connectCode.value = ''
  connectError.value = ''
  connectModels.value = []
  connectChoice.value = ''
  connectTarget.value = null
}

function openConnect(provider: ProviderLoginProvider) {
  resetConnect()
  connectProvider.value = provider
  if (provider === 'opencode') {
    // Two ways in, and the free one needs no account: the choice comes before
    // anything starts a flow.
    connectStep.value = 'start'
    return
  }
  connectStep.value = 'auth'
  void beginConnectLogin(provider)
}

async function beginConnectLogin(provider: ProviderLoginProvider) {
  stopConnectPoll()
  connectStep.value = 'auth'
  connectLoginId.value = ''
  connectUrl.value = ''
  connectCode.value = ''
  connectError.value = ''
  try {
    const started = await forebrainApi.startProviderLogin(provider)
    if (connectProvider.value !== provider) return
    connectLoginId.value = started.loginId
    pollConnect(provider, started.loginId)
  } catch (cause) {
    connectError.value =
      cause instanceof GatewayHttpError && cause.status === 409 ? t('providers.connectBusy') : t('providers.connectFailed')
    connectStep.value = 'failed'
  }
}

function pollConnect(provider: ProviderLoginProvider, loginId: string) {
  stopConnectPoll()
  connectTimer = setTimeout(() => {
    connectTimer = null
    void pollConnectOnce(provider, loginId)
  }, CONNECT_POLL_MS)
}

async function pollConnectOnce(provider: ProviderLoginProvider, loginId: string) {
  if (connectLoginId.value !== loginId) return
  let status: Awaited<ReturnType<typeof forebrainApi.providerLoginStatus>>
  try {
    status = await forebrainApi.providerLoginStatus(provider, loginId)
  } catch (cause) {
    if (connectLoginId.value !== loginId) return
    connectError.value = getErrorMessage(cause)
    connectStep.value = 'failed'
    return
  }
  // A closed modal, or a login started over the top of this one, owns the
  // screen now — this answer is about a flow nobody is watching.
  if (connectLoginId.value !== loginId) return
  if (status === null) {
    // The gateway forgot the id: it restarted, or the ended login was pruned.
    // Polling it again can only answer the same, so the page offers a restart.
    connectStep.value = 'lost'
    return
  }
  if (status.url) connectUrl.value = status.url
  if (status.userCode) connectCode.value = status.userCode
  if (status.status === 'failed') {
    connectError.value = connectFailureText(status.error)
    connectStep.value = 'failed'
    return
  }
  if (status.status === 'complete') {
    await chooseCatalog(connectTargetFor(provider, status.baseUrl))
    return
  }
  pollConnect(provider, loginId)
}

/** The sentence a failure reason becomes. The reasons are the OAuth vocabulary
 * the flows report ("denied", "expired", "canceled"); anything else says only
 * that it did not finish, because the raw message names internals. */
function connectFailureText(reason: string): string {
  switch (String(reason).trim().toLowerCase()) {
    case 'canceled':
      return t('providers.connectCanceled')
    case 'denied':
      return t('providers.connectDenied')
    case 'expired':
      return t('providers.connectExpired')
    default:
      return t('providers.connectFailed')
  }
}

/** The row a finished sign-in writes. The OpenCode login runs against the
 * Console but unlocks the Go subscription's catalog, which is served under /go
 * and nowhere else, so the row is "opencode-go". Its base URL is the one the
 * login's completion answer named: the gateway decides that value (the Console
 * root would name a surface those models do not answer on), and a copy here
 * could only drift from it. ChatGPT's row is its own service either way. */
function connectTargetFor(provider: ProviderLoginProvider, baseUrl: string): ConnectTarget {
  if (provider === 'chatgpt') return { provider: 'chatgpt', catalog: 'chatgpt', baseUrl }
  return { provider: 'opencode-go', catalog: 'opencode-go', baseUrl }
}

async function chooseCatalog(target: ConnectTarget) {
  connectTarget.value = target
  connectStep.value = 'choose'
  try {
    const listing = await forebrainApi.getModels({ provider: target.catalog })
    const records = listing.records.filter((record) => String(record.provider ?? '').trim() === target.catalog)
    connectModels.value = records
    const preset = records.find((record) => record.isDefault) ?? records[0]
    connectChoice.value = preset ? modelIdOf(preset) : ''
  } catch (cause) {
    connectError.value = getErrorMessage(cause)
    connectStep.value = 'failed'
  }
}

/** OpenCode's free tier: no account, no flow, the Console catalog directly. */
function chooseFreeModels() {
  void chooseCatalog({ provider: 'opencode', catalog: 'opencode', baseUrl: OPENCODE_BASE_URL })
}

function openAuthorizePage() {
  if (!connectUrl.value) return
  window.open(connectUrl.value, '_blank', 'noopener')
}

function restartConnect() {
  const provider = connectProvider.value
  if (!provider) return
  if (provider === 'opencode') {
    // The choice is offered again: a retry may well be the free tier, and a
    // login the user did not want should not be started on their behalf.
    resetConnect()
    connectStep.value = 'start'
    return
  }
  void beginConnectLogin(provider)
}

function closeConnect() {
  const provider = connectProvider.value
  const loginId = connectLoginId.value
  const running = connectStep.value === 'auth'
  resetConnect()
  connectProvider.value = null
  // The gateway keeps the flow alive until it times out on its own; a page that
  // walked away says so, so the flow stops holding a device code or a loopback
  // port for the next sign-in.
  if (provider && loginId && running) void forebrainApi.cancelProviderLogin(provider, loginId).catch(() => undefined)
}

async function saveConnectModel() {
  const target = connectTarget.value
  const model = connectChoice.value
  if (!target || !model) return
  const { provider, baseUrl } = target
  closeConnect()
  await writeWholeOrder((all) => {
    const at = all.findIndex((candidate) => String(candidate.provider ?? '').trim() === provider)
    if (at >= 0) {
      // Re-signing in to a service keeps the models already on its row; the one
      // just chosen becomes the model it leads with.
      const models = [...(all[at].models ?? [])].filter((known) => known !== model)
      all[at] = { ...all[at], models: [model, ...models], baseUrl }
      return
    }
    all.push({ provider, models: [model], baseUrl, apiPath: '', params: '', apiKeyPlain: '' })
  })
}

onMounted(() => {
  void reload()
  void loadCatalog()
})

onBeforeUnmount(() => {
  const provider = connectProvider.value
  const loginId = connectLoginId.value
  const running = connectStep.value === 'auth'
  resetConnect()
  if (provider && loginId && running) void forebrainApi.cancelProviderLogin(provider, loginId).catch(() => undefined)
})
</script>
