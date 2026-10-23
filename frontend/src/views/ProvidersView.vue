<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto flex w-full max-w-5xl flex-col gap-4">
      <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <ListToolbar>
        <template #left>
          <Input
            v-model="q"
            type="search"
            :placeholder="t('providers.searchPlaceholder')"
            class="forebrain-field h-[34px] w-auto"
            style="max-width: 240px"
            data-testid="providers-search"
          />
        </template>
        <template #actions>
          <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto gap-1.5 text-xs" :disabled="saving" data-testid="providers-add" @click="openAdd">
            <Plus class="size-3.5" aria-hidden="true" />{{ t('providers.add') }}
          </Button>
          <Button
            type="button"
            class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs"
            :disabled="saving"
            data-testid="providers-connect-opencode"
            @click="openConnect('opencode')"
          >
            <LogIn class="size-3.5" aria-hidden="true" />{{ t('providers.connectOpenCode') }}
          </Button>
          <Button
            type="button"
            class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs"
            :disabled="saving"
            data-testid="providers-connect-chatgpt"
            @click="openConnect('chatgpt')"
          >
            <LogIn class="size-3.5" aria-hidden="true" />{{ t('providers.connectChatGPT') }}
          </Button>
          <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs" :disabled="loading" @click="refresh">
            <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
          </Button>
        </template>
      </ListToolbar>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('providers.empty') }}</div>
      <DataTable
        v-else
        :columns="columns"
        :rows="rows"
        :row-key="entryKey"
        :row-attrs="providerRowAttrs"
      >
        <template #cell-provider="{ row }">
          <span class="forebrain-cell-1" v-tip:top.trunc="row.provider">{{ row.provider }}</span>
          <BadgeComponent v-if="isDefaultRow(row)" class="ml-1.5" :data-testid="`provider-default-${rowIndex(row)}`">{{ t('providers.defaultBadge') }}</BadgeComponent>
        </template>
        <template #cell-models="{ row }">
          <span class="forebrain-cell-1" v-tip:top.trunc="row.models.join('、')">{{ row.models.join('、') }}</span>
        </template>
        <template #cell-baseUrl="{ row }">
          <span class="forebrain-cell-1" v-tip:top.trunc="row.baseUrl || ''">{{ row.baseUrl }}</span>
        </template>
        <template #cell-key="{ row }">
          <BadgeComponent v-if="row.apiKeySet" variant="success" :data-testid="`provider-key-saved-${rowIndex(row)}`">
            {{ t('providers.keySavedBadge', { hint: row.apiKeyHint ?? '' }) }}
          </BadgeComponent>
          <BadgeComponent v-else variant="warning" :data-testid="`provider-key-unset-${rowIndex(row)}`">{{ t('providers.unsetKey') }}</BadgeComponent>
        </template>
        <template #cell-actions="{ row }">
          <RowActions>
            <RowAction
              v-if="!isDefaultRow(row)"
              :icon="Star"
              :label="t('providers.setDefault')"
              :name="row.provider"
              :disabled="saving"
              :data-testid="`provider-default-set-${rowIndex(row)}`"
              @click="setDefault(row)"
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
        </template>
      </DataTable>

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
    </div>

    <ForebrainModal :open="modalOpen" :size="'md'" :title="editingKey !== null ? t('providers.editModalTitle') : t('providers.addModalTitle')" @close="modalOpen = false">
      <div>
        <Label class="mb-1 block text-[12px] font-normal text-[var(--forebrain-text-2)]" for="provider-name">{{ t('providers.providerLabel') }}</Label>
        <Select :model-value="form.provider" @update:model-value="pickProvider">
          <SelectTrigger
            id="provider-name"
            class="h-9 w-full rounded-md border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-2.5 text-[13px] text-[var(--forebrain-text)]"
            data-testid="provider-name"
          >
            <SelectValue :placeholder="t('providers.providerPlaceholder')" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="name in providerOptions" :key="name" :value="name">{{ name }}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div>
        <Label class="mb-1 block text-[12px] font-normal text-[var(--forebrain-text-2)]" for="provider-base-url">{{ t('providers.baseUrlLabel') }}</Label>
        <Input
          id="provider-base-url"
          v-model="form.baseUrl"
          class="h-9 w-full rounded-md border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-2.5 font-mono text-[12px] text-[var(--forebrain-text)]"
          data-testid="provider-base-url"
        />
      </div>

      <div>
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.modelsLabel') }}</span>
        <ModelChipsInput v-model="form.models" :suggestions="suggestionsFor(form.provider)" />
      </div>

      <div>
        <Label class="mb-1 block text-[12px] font-normal text-[var(--forebrain-text-2)]" for="provider-key-input">{{ t('providers.apiKeyLabel') }}</Label>
        <div v-if="form.apiKeySet && !form.keyEdit" class="flex w-full items-center gap-2">
          <span
            class="flex h-9 min-w-0 flex-1 items-center rounded-md border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-2.5 font-mono text-[12px] text-[var(--forebrain-text-2)]"
            data-testid="provider-key-saved"
          >
            {{ t('providers.keySaved', { hint: form.apiKeyHint ?? '' }) }}
          </span>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            class="h-9 px-3 text-xs"
            data-testid="provider-key-change"
            @click="form.keyEdit = true"
          >
            {{ t('providers.changeKey') }}
          </Button>
        </div>
        <Input
          v-else
          id="provider-key-input"
          v-model="form.apiKeyPlain"
          type="password"
          autocomplete="new-password"
          class="h-9 w-full rounded-md border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-2.5 font-mono text-[12px] text-[var(--forebrain-text)]"
          :placeholder="t('providers.apiKeyPlaceholder')"
          data-testid="provider-key-input"
        />
        <p v-if="keyHint" class="mt-1 text-[11px] text-[var(--forebrain-muted-text)]" data-testid="provider-key-hint">{{ keyHint }}</p>
      </div>

      <div>
        <Label class="mb-1 block text-[12px] font-normal text-[var(--forebrain-text-2)]" for="provider-api-format">{{ t('providers.apiFormatLabel') }}</Label>
        <Select v-model="apiFormatChoice">
          <SelectTrigger
            id="provider-api-format"
            class="h-9 w-full rounded-md border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-2.5 text-[13px] text-[var(--forebrain-text)]"
            data-testid="provider-api-format"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-for="format in apiFormats" :key="format.value" :value="format.value">{{ format.label }}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div>
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('providers.paramsLabel') }}</span>
        <ParamsJsonField v-model="form.paramsText" :invalid="Boolean(paramsError)" :aria-label="t('providers.paramsLabel')" />
        <p v-if="paramsError" class="mt-1 text-[11px] text-[var(--forebrain-danger)]" data-testid="provider-params-error">{{ paramsError }}</p>
      </div>

      <!-- The server's own words, verbatim: a refusal names the row and the
           field it could not take. Inside the dialog, because that is the
           surface that raised it and the page banner sits under the mask. -->
      <div v-if="modalError" ref="saveErrorRef" class="forebrain-err-block" role="alert" data-testid="provider-save-error">
        <CircleAlert class="size-3.5" aria-hidden="true" />
        <div>
          <b>{{ t('mcp.saveFailed') }}</b>
          <span class="forebrain-err-s">{{ modalError }}</span>
        </div>
      </div>

      <template #footer>
        <Button type="button" variant="ghost" size="sm" class="text-xs" data-testid="provider-cancel" @click="modalOpen = false">
          {{ t('common.cancel') }}
        </Button>
        <Button
          type="button"
          size="sm"
          class="text-xs"
          :disabled="saving || Boolean(paramsError) || keyBlocked"
          data-testid="provider-save"
          @click="modalSave"
        >
          {{ saving ? t('common.loading') : t('common.save') }}
        </Button>
      </template>
    </ForebrainModal>

    <ForebrainModal :open="connectProvider !== null" size="md" :title="connectTitle" @close="closeConnect">
      <template v-if="connectStep === 'auth'">
        <p class="text-[12px] text-[var(--forebrain-text-2)]">{{ connectAuthHint }}</p>
        <div v-if="connectProvider === 'opencode'" class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] px-4 py-3">
          <span class="block text-[11px] text-[var(--forebrain-muted-text)]">{{ t('providers.connectCodeLabel') }}</span>
          <span class="font-mono text-2xl tracking-[0.2em] text-[var(--forebrain-text)]" data-testid="provider-login-code">
            {{ connectCode || t('providers.connectCodePending') }}
          </span>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            class="forebrain-btn forebrain-btn-primary h-9 px-3 text-xs"
            :disabled="!connectUrl"
            data-testid="provider-login-open"
            @click="openAuthorizePage"
          >
            {{ t('providers.connectOpenPage') }}
          </Button>
          <Loader :size="14" class="text-[var(--forebrain-muted-text)]" data-testid="provider-login-spinner" />
          <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('providers.connectWaiting') }}</span>
        </div>
        <p v-if="connectProvider === 'chatgpt'" class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('providers.connectChatGPTLocal') }}</p>
      </template>

      <!-- The catalog the finished login belongs to: the Go subscription after
           signing in, ChatGPT's account after its callback. -->
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
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" data-testid="provider-login-cancel" @click="closeConnect">
          {{ connectStep === 'auth' ? t('providers.cancelLogin') : t('common.close') }}
        </Button>
        <Button
          v-if="connectStep === 'failed' || connectStep === 'lost'"
          type="button"
          class="forebrain-btn forebrain-btn-primary h-auto text-xs"
          data-testid="provider-login-retry"
          @click="restartConnect"
        >
          {{ t('providers.connectRetry') }}
        </Button>
        <Button
          v-else-if="connectStep === 'choose'"
          type="button"
          class="forebrain-btn forebrain-btn-primary h-auto text-xs"
          :disabled="!connectChoice || saving"
          data-testid="provider-login-save"
          @click="saveConnectModel"
        >
          {{ saving ? t('common.loading') : t('providers.connectSave') }}
        </Button>
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
 * row.
 */
import { computed, defineAsyncComponent, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { Check, CircleAlert, LogIn, Pencil, Plus, RotateCw, Star, Trash2 } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@repo/shadcn-vue/components/ui/command'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import { Label } from '@repo/shadcn-vue/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'
import { Loader } from '@repo/elements/loader'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import TablePager from '@/components/common/TablePager.vue'
import ModelChipsInput from '@/components/providers/ModelChipsInput.vue'
// The params editor carries CodeMirror, which only the add/edit modal needs: the
// chunk loads when the modal first opens instead of with the list.
const ParamsJsonField = defineAsyncComponent(() => import('@/components/providers/ParamsJsonField.vue'))
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

/**
 * One entry of the whole ordered list as this page writes it back. `apiKey` is
 * the wire's own statement about the stored key: set to `''` when the user
 * emptied the field (that is the clear), and left unset for a row they did not
 * touch — the page never receives the stored value, so it must not send a guess
 * back.
 */
type ProviderEntry = ProviderRecord & {
  apiKeyPlain?: string
  apiKey?: string
  /** The row as it stood before this edit; the server replaces its entries in place. */
  replaces?: { provider: string; baseUrl: string; apiPath: string; models: string[] }
}

/** The agent's effective default ("provider/model"), as the server resolves
 * it — default_model when set, otherwise the first row. The default badge and
 * the set-default action both read this one value, never a client-side
 * re-derivation. */
const defaultModel = ref('')
const { rows, total, page, pageSize, q, loading, error, reload } = useServerList<ProviderRecord>(
  (query) => forebrainApi.providersPage(query).then((res) => {
    defaultModel.value = res.defaultModel
    return res
  }),
  {
    pageSize: 10,
  },
)

const saving = ref(false)
const catalogProviders = ref<string[]>([])
const catalogModels = ref<Record<string, string[]>>({})
/** The endpoint each provider is served at, as the model catalog reports it. */
const providerBaseUrls = ref<Record<string, string>>({})

const modalOpen = ref(false)
// The entry a dialog acts on, named by what it is rather than by where it sat
// when the dialog opened.
const editingKey = ref<string | null>(null)
const removeKey = ref<string | null>(null)
const form = reactive<ProviderDraft>(blankDraft())
/**
 * A refused save, in the dialog's own words. The page banner sits under the
 * mask while a dialog is open, so the surface that raised the refusal is the
 * one that has to show it; the block is also the element the dialog scrolls
 * back into view.
 */
const modalError = ref('')
const saveErrorRef = ref<HTMLElement | null>(null)

/**
 * The model services that authenticate with a login on this machine
 * (opencode.json, auth.json) instead of an api_key in the table. Mirrors the
 * engine's config.CredentialsBackedProvider: these rows are legitimately
 * keyless, and every other row cannot be saved without a key.
 */
const LOGIN_PROVIDERS = new Set(['chatgpt', 'opencode', 'opencode-go'])
function needsAPIKey(provider: string): boolean {
  const name = provider.trim().toLowerCase()
  return name !== '' && !LOGIN_PROVIDERS.has(name)
}

/** The stored key this dialog is keeping: shown as a chip, never typed again. */
const keyKept = computed(() => form.apiKeySet && !form.keyEdit)
/**
 * The row still owes a key: a key-mode service with no stored key and nothing
 * typed. No save can satisfy the server in that state, so the button is off and
 * the field says why.
 */
const keyMissing = computed(() => needsAPIKey(form.provider) && !keyKept.value && !form.apiKeyPlain.trim())
/**
 * The user opened a stored key and emptied the field. That is the clear, and
 * the key-mode services refuse it — the request goes out so the dialog can show
 * the server's own sentence instead of a silent no-op.
 */
const keyClearAttempt = computed(() => form.apiKeySet && form.keyEdit && !form.apiKeyPlain.trim())

/** The one state that makes Save impossible: no key, and none being cleared. */
const keyBlocked = computed(() => keyMissing.value && !keyClearAttempt.value)

/** What the API key field says when the row cannot be saved without one. */
const keyHint = computed(() => {
  if (!needsAPIKey(form.provider)) return ''
  if (keyClearAttempt.value) return t('providers.apiKeyCannotClear')
  if (keyMissing.value) return t('providers.apiKeyRequired')
  return ''
})

/**
 * The providers the form offers. Picking a provider must be a choice, not
 * typing, so the list is everything the model catalog serves — the providers
 * the gateway names a default endpoint for included — plus whatever the row
 * already stores: a service the catalog no longer lists must not silently
 * change just because its row was edited.
 */
const providerOptions = computed(() => {
  const names = new Set<string>([...catalogProviders.value, ...Object.keys(providerBaseUrls.value)])
  const current = form.provider.trim()
  if (current) names.add(current)
  return [...names].sort()
})

/** Picking a provider fills in what that provider's entry starts from. */
function onProviderPicked() {
  const baseUrl = providerBaseUrls.value[form.provider]
  if (baseUrl) form.baseUrl = baseUrl
  form.apiPath = defaultAPIPath(form.provider)
}

/**
 * The shadcn select's v-model, as one function: reka-ui hands back its own
 * value union, and the draft stores the provider id as text.
 */
function pickProvider(value: unknown): void {
  form.provider = String(value ?? '').trim()
  onProviderPicked()
}

/**
 * The API-format dropdown. The engine reads the stored path as the protocol to
 * speak, not as a URL: empty is Chat Completions, `/responses` builds the
 * Responses client, and `/v1/messages` builds the Anthropic Messages client
 * (the client appends its own endpoint to the base URL). A row that already
 * stores some other path — a compatible endpoint serving its chat API under a
 * path of its own — keeps it as a fourth, already-selected entry: editing the
 * row's other fields must not silently drop it.
 */
const DEFAULT_CHAT_PATH = '/chat/completions'
const RESPONSES_PATH = '/responses'
const MESSAGES_PATH = '/v1/messages'
/**
 * The select item that stands for the empty path. A select cannot carry an
 * empty item value (reka-ui refuses it), and the empty string is a real choice
 * here — Chat Completions — so the item names itself and the draft converts.
 */
const CHAT_FORMAT_CHOICE = '@chat-completions'
const apiFormatChoice = computed({
  get: () => form.apiPath.trim() || CHAT_FORMAT_CHOICE,
  set: (choice: string) => {
    form.apiPath = choice === CHAT_FORMAT_CHOICE ? '' : choice
  },
})
const apiFormats = computed(() => {
  const options = [
    { value: MESSAGES_PATH, label: t('providers.apiFormatAnthropic') },
    { value: CHAT_FORMAT_CHOICE, label: t('providers.apiFormatChat') },
    { value: RESPONSES_PATH, label: t('providers.apiFormatResponses') },
  ]
  const current = form.apiPath.trim()
  if (current && !options.some((option) => option.value === current)) {
    options.push({ value: current, label: t('providers.apiFormatCustom', { path: current }) })
  }
  return options
})

/**
 * The two providers that speak Messages without being told: the engine builds
 * their client from the provider name whatever api_path says, so the form says
 * so too instead of claiming Chat Completions. Every other provider starts on
 * Chat Completions, its own default endpoint.
 */
const ANTHROPIC_PROVIDERS = new Set(['anthropic', 'minimax'])
function defaultAPIPath(provider: string): string {
  return ANTHROPIC_PROVIDERS.has(provider.trim().toLowerCase()) ? MESSAGES_PATH : ''
}

/** A stored "/chat/completions" is what the empty path already means. */
function normalizeAPIPath(path: string | undefined, provider: string | undefined): string {
  const trimmed = String(path ?? '').trim()
  if (trimmed === DEFAULT_CHAT_PATH) return ''
  // A Messages provider stores no path at all, and the engine gives it the
  // Messages client regardless — showing that format is the honest reading.
  if (!trimmed) return defaultAPIPath(String(provider ?? ''))
  return trimmed
}

/**
 * The params field is JSON the engine hands to the provider verbatim, so it has
 * to parse as an object before it can be saved: the server refuses anything else
 * and the modal would already be closed when it said so.
 */
const paramsError = computed(() => {
  const text = form.paramsText.trim()
  if (!text) return ''
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    return t('providers.paramsInvalid')
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return t('providers.paramsNotObject')
  return ''
})

function blankDraft(): ProviderDraft {
  return { provider: '', baseUrl: '', apiPath: '', paramsText: '', models: [], apiKeySet: false, keyEdit: true, apiKeyPlain: '' }
}

/**
 * The table's columns, at the widths the design declares. The models column has
 * no width: it takes the room the others leave, which is what makes the list fit
 * the page at any width. No column sorts — the row order is storage order
 * (owner 2026-10-26), not priority, and the default model is marked by the
 * badge, not by position.
 */
const columns = computed<DataColumn[]>(() => [
  { id: 'provider', header: t('providers.colProvider'), size: 140 },
  { id: 'models', header: t('providers.colModels'), cellClass: 'font-mono' },
  { id: 'baseUrl', header: t('providers.colBaseUrl'), size: 220, cellClass: 'font-mono text-[var(--forebrain-text-2)]' },
  { id: 'key', header: t('providers.colKey'), size: 150 },
  { id: 'actions', header: t('providers.colActions'), size: 110, headerClass: 'text-center', cellClass: 'text-right' },
])

/** A row's hook for the e2e suite and the tips: the service it is. */
function providerRowAttrs(row: ProviderRecord): Record<string, string> {
  return { 'data-provider-row': row.provider || '' }
}

/** A row's position in the whole stored list — a stable row id for tests, not
 * a priority: no badge and no action derives meaning from it. */
function rowIndex(row: ProviderRecord): number {
  return row.index ?? 0
}

/** Whether the agent's effective default names any model on this row. */
function isDefaultRow(row: ProviderRecord): boolean {
  const provider = String(row.provider ?? '').trim()
  if (!provider || !defaultModel.value) return false
  return (row.models ?? []).some((model) => defaultModel.value === `${provider}/${String(model ?? '').trim()}`)
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

/** The row as it stands now, for the server to find the stored entries an edit replaces. */
function rowAsLoaded(row: ProviderRecord): NonNullable<ProviderEntry['replaces']> {
  return {
    provider: String(row.provider ?? '').trim(),
    baseUrl: String(row.baseUrl ?? '').trim(),
    apiPath: String(row.apiPath ?? '').trim(),
    models: [...(row.models ?? [])],
  }
}

function toDraft(record: ProviderRecord): ProviderDraft {
  return {
    provider: record.provider ?? '',
    baseUrl: record.baseUrl ?? '',
    apiPath: normalizeAPIPath(record.apiPath, record.provider),
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
  // A row the user emptied states that on the wire: the empty string is the
  // clear. Absent (never set here) means the same as an untouched key.
  if (entry.apiKey !== undefined) body.api_key = entry.apiKey
  if (entry.replaces) {
    body.replaces = {
      provider: entry.replaces.provider,
      base_url: entry.replaces.baseUrl,
      api_path: entry.replaces.apiPath,
      models: [...entry.replaces.models],
    }
  }
  return body
}

let pendingWrites = 0
let lastWrite: Promise<void> = Promise.resolve()

/**
 * The list is whole-table on the wire, so a change reads the whole order back,
 * applies itself to it, and saves it: writing the page alone would delete every
 * row the page does not show. Writes run one after another — each reads the
 * order the previous one wrote — so two quick clicks move a row two places
 * instead of both working from the same stale snapshot and undoing each other.
 *
 * A caller that owns the surface the failure belongs to names it with `report`:
 * a refused save then says so in the dialog that raised it, and the page banner
 * — which that dialog covers — stays empty.
 */
function writeWholeOrder(change: (all: ProviderEntry[]) => void, report?: (message: string) => void, defaultModelOverride?: string): Promise<void> {
  pendingWrites += 1
  saving.value = true
  const write = lastWrite.then(async () => {
    error.value = ''
    try {
      const all: ProviderEntry[] = (await forebrainApi.providers()).providers
      change(all)
      await forebrainApi.saveProviders(all.map(toBody), defaultModelOverride)
      await reload()
    } catch (cause) {
      const message = getErrorMessage(cause)
      if (report) report(message)
      else error.value = message
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
    providerBaseUrls.value = listing.providerBaseUrls ?? {}
  } catch {
    // The catalog is a convenience: without it the forms still work by hand.
    catalogProviders.value = []
    catalogModels.value = {}
    providerBaseUrls.value = {}
  }
}

function suggestionsFor(provider: string): string[] {
  return catalogModels.value[provider.trim()] ?? []
}

function openAdd() {
  editingKey.value = null
  Object.assign(form, blankDraft())
  modalError.value = ''
  modalOpen.value = true
}

function openEdit(row: ProviderRecord) {
  editingKey.value = entryKey(row)
  Object.assign(form, toDraft(row))
  modalError.value = ''
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
  // Emptying the field of a row that had a key *is* the clear: the page says so
  // with an empty api_key. A typed key travels as api_key_plain instead, and a
  // row whose key the user never opened sends neither.
  if (form.apiKeySet && form.keyEdit && !form.apiKeyPlain.trim()) entry.apiKey = ''
  modalError.value = ''
  // The write can be refused (a model service that signs in with a key cannot
  // lose it): keep the dialog open with the reason inside itself rather than
  // discard the user's edits — and rather than paint it under the mask.
  await writeWholeOrder(
    (all) => {
      const at = key === null ? -1 : all.findIndex((candidate) => entryKey(candidate) === key)
      if (at >= 0) all[at] = { ...all[at], ...entry, replaces: rowAsLoaded(all[at]) }
      else all.push(entry)
    },
    (message) => {
      modalError.value = message
    },
  )
  if (modalError.value !== '') {
    // The body scrolls: a refusal the user has to hunt for is the same bug in
    // another form. The block is the last element, so the dialog scrolls to it.
    await nextTick()
    saveErrorRef.value?.scrollIntoView({ block: 'nearest' })
    return
  }
  modalOpen.value = false
}

/** Retargets agents.definitions.<agent>.default_model at this row. The list's
 * order never changes — the default is a named value, not a position. A row
 * serving several models sets its first one (the tooltip says so). */
function setDefault(row: ProviderRecord) {
  const provider = String(row.provider ?? '').trim()
  const model = String(row.models?.[0] ?? '').trim()
  if (!provider || !model) return
  void writeWholeOrder(() => {}, undefined, `${provider}/${model}`)
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
/** The modal's step: a running flow, the catalog it unlocked, or how the flow
 * ended. */
const connectStep = ref<'auth' | 'choose' | 'failed' | 'lost'>('auth')
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
    // A reusable login on this machine (or one that stands while only the
    // listing is unreachable) is already the finished sign-in: go straight to
    // the catalog, whose own fetch reports a listing that still fails.
    if (started.status === 'complete' && started.baseUrl) {
      connectLoginId.value = ''
      await chooseCatalog(connectTargetFor(provider, started.baseUrl))
      return
    }
    // Type narrowing, not a defense branch: the contract is complete-with-
    // base_url or pending-with-loginId; the guard below satisfies the compiler.
    if (!started.loginId) {
      connectError.value = getErrorMessage(new Error('login started without an id'))
      connectStep.value = 'failed'
      return
    }
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

function openAuthorizePage() {
  if (!connectUrl.value) return
  window.open(connectUrl.value, '_blank', 'noopener')
}

function restartConnect() {
  const provider = connectProvider.value
  if (!provider) return
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
      // Re-signing in to a service keeps the row's models exactly as they are;
      // the one just chosen is appended when it is new, never re-ordered. No
      // default_model is written here — this page holds no conversation to
      // enable the model for.
      const models = [...(all[at].models ?? [])]
      if (!models.includes(model)) models.push(model)
      all[at] = { ...all[at], models, baseUrl, replaces: rowAsLoaded(all[at]) }
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
