<template>
  <ForebrainDrawer :open="open" :title="mode === 'edit' ? t('models.drawerEdit') : t('models.drawerAdd')" @close="emit('close')">
    <!-- A row a subscription wrote is not editable as a form: its endpoint,
         path and key come from the sign-in. The drawer says where they came
         from, how that sign-in stands, and the one thing that can be changed —
         the model's own capabilities. -->
    <template v-if="sourceRow">
      <div class="src" data-testid="models-source-card">
        <div class="src__head">
          <b>{{ t('models.sourceFrom') }}</b>
          <span>{{ sourceRow.sourceName || sourceRow.source }}<template v-if="sourceRow.sourcePlan"> · {{ sourceRow.sourcePlan }}</template></span>
          <BadgeComponent :variant="stateVariant" class="ml-auto" data-testid="models-source-state">{{ stateLabel }}</BadgeComponent>
        </div>
        <p>{{ sourceRow.loginBacked ? t('models.sourceLoginHint') : t('models.sourceKeyHint') }}</p>
        <div class="src__acts">
          <Button
            v-if="!sourceRow.loginBacked"
            type="button"
            variant="ghost"
            class="forebrain-btn forebrain-btn--ghost h-auto gap-1.5 text-xs"
            :disabled="refreshing"
            data-testid="models-refresh-key"
            @click="refreshKey"
          >
            <RotateCw class="size-3.5" aria-hidden="true" />{{ t('models.refreshKey') }}
          </Button>
          <Button
            type="button"
            :variant="sourceRow.loginBacked ? 'default' : 'ghost'"
            class="forebrain-btn h-auto gap-1.5 text-xs"
            :class="sourceRow.loginBacked ? 'forebrain-btn-primary' : 'forebrain-btn--ghost'"
            data-testid="models-relogin"
            @click="emit('openSubscribe', sourceRow.source || '')"
          >
            <LogIn class="size-3.5" aria-hidden="true" />{{ t('models.relogin') }}
          </Button>
        </div>
      </div>

      <div class="rows">
        <div class="row"><div class="row__label"><b>{{ t('models.provider') }}</b></div><div><span class="row__ro">{{ sourceRow.providerName || sourceRow.provider }}</span></div></div>
        <div class="row"><div class="row__label"><b>{{ t('models.modelName') }}</b></div><div><span class="row__ro row__ro--mono">{{ sourceRow.model }}</span></div></div>
        <div class="row"><div class="row__label"><b>{{ t('models.baseUrl') }}</b></div><div><span class="row__ro row__ro--mono">{{ sourceRow.baseUrl }}</span></div></div>
        <div class="row"><div class="row__label"><b>{{ t('models.colPath') }}</b></div><div><span v-if="sourceRow.urlVerbatim" class="forebrain-path forebrain-path--na">{{ t('models.customProtocol') }}</span><span v-else class="forebrain-path">{{ sourceRow.apiPath }}</span></div></div>
        <div class="row">
          <div class="row__label"><b>{{ t('models.apiKey') }}</b></div>
          <div>
            <span v-if="sourceRow.apiKeySet" class="forebrain-keymask" data-testid="models-source-key">{{ bullets(sourceRow.apiKey) }}<b>{{ sourceRow.apiKeyLast4 }}</b></span>
            <span v-else class="row__ro row__ro--quiet">{{ t('models.accountLogin') }}</span>
            <span v-if="sourceRow.apiKeySet" class="row__note">{{ t('models.planKey') }}</span>
          </div>
        </div>
      </div>
      <div class="src__switch">
        {{ t('models.sourceSwitchHint') }}
        <button type="button" data-testid="models-open-subscribe" @click="emit('openSubscribe', sourceRow.source || '')">
          {{ t('models.openSubscribe') }}
        </button>
      </div>
    </template>

    <template v-else>
      <div class="models-field">
        <span class="models-field__label">{{ t('models.provider') }}</span>
        <ProviderCombobox
          v-model="draft.provider"
          :providers="providers"
          :custom-id="customId"
          :open="providerOpen"
          @update:open="providerOpen = $event"
        />
      </div>

      <!-- A provider serving more than one endpoint (a plan and its compatible
           address) picks the address here; one endpoint needs no question. -->
      <div v-if="endpoints.length > 1" class="models-field">
        <span class="models-field__label">{{ t('models.endpoint') }}</span>
        <div class="models-chips">
          <button
            v-for="(endpoint, index) in endpoints"
            :key="endpoint.name"
            type="button"
            class="models-chip"
            :class="{ 'models-chip--on': draft.endpointIndex === index }"
            role="radio"
            :aria-checked="draft.endpointIndex === index"
            :data-endpoint="index"
            @click="pickEndpoint(index)"
          >
            {{ endpoint.name }}
          </button>
        </div>
      </div>

      <div class="models-field">
        <span class="models-field__label">{{ t('models.baseUrl') }}</span>
        <Input
          v-model="draft.baseUrl"
          class="forebrain-field h-[32px] font-mono text-[12px]"
          placeholder="https://api.example.com/v1"
          autocomplete="off"
          spellcheck="false"
          data-testid="models-base-url"
        />
      </div>

      <div class="models-field">
        <span class="models-field__label">{{ t('models.protocol') }}</span>
        <ProtocolPicker
          v-model="draft.apiPath"
          :verbatim="draft.verbatim"
          :allow-verbatim="isCustom"
          :stored-path="storedPath"
          :protocols="protocols"
          @update:verbatim="draft.verbatim = $event"
        />
        <RequestUrlLine :base-url="draft.baseUrl" :api-path="draft.apiPath" :verbatim="draft.verbatim" />
        <span v-if="draft.verbatim" class="models-field__hint">{{ t('models.customProtocolHint') }}</span>
      </div>

      <div class="models-field">
        <span class="models-field__label">{{ t('models.apiKey') }}</span>
        <div class="keyrow">
          <span class="keyrow__box">
            <input
              v-model="draft.apiKey"
              :type="reveal ? 'text' : 'password'"
              class="keyrow__input"
              :class="{ 'keyrow__input--plain': reveal }"
              autocomplete="off"
              spellcheck="false"
              :placeholder="t('models.apiKeyPlaceholder')"
              :aria-label="t('models.apiKey')"
              data-testid="models-api-key"
            />
            <button
              type="button"
              class="keyrow__eye"
              :aria-label="reveal ? t('models.apiKeyHide') : t('models.apiKeyShow')"
              :data-reveal="reveal ? 'on' : 'off'"
              @click="reveal = !reveal"
            >
              <Eye v-if="reveal" class="size-3.5" aria-hidden="true" />
              <EyeClosed v-else class="size-3.5" aria-hidden="true" />
            </button>
          </span>
          <Button
            type="button"
            variant="ghost"
            class="forebrain-btn forebrain-btn--ghost h-[32px] shrink-0 text-xs"
            :disabled="testing"
            data-testid="models-test"
            @click="test"
          >
            {{ t('models.test') }}
          </Button>
        </div>
        <span v-if="testState === 'ok'" class="models-field__ok" data-testid="models-test-ok">{{ t('models.testOk') }}</span>
        <span v-else-if="testState" class="models-field__bad" data-testid="models-test-failed">{{ t('models.testFailed') }}：{{ testState }}</span>
      </div>

      <div class="models-field">
        <span class="models-field__label">{{ t('models.modelName') }}</span>
        <Input
          v-model="draft.model"
          class="forebrain-field h-[32px] font-mono text-[12px]"
          :placeholder="t('models.modelNamePlaceholder')"
          autocomplete="off"
          spellcheck="false"
          data-testid="models-model-name"
        />
        <div v-if="suggestions.length" class="models-sugg">
          {{ t('models.commonModels') }}
          <button
            v-for="name in suggestions"
            :key="name"
            type="button"
            class="models-sugg__item"
            :data-suggestion="name"
            @click="draft.model = name"
          >
            {{ name }}
          </button>
        </div>
        <span v-if="isCustom" class="models-field__hint">{{ t('models.modelNameHint') }}</span>
      </div>

      <ModelCapsFields
        v-model:open="advancedOpen"
        :draft="draft.caps"
        :catalog="catalogCaps"
        :draft-efforts="draft.efforts"
        :source-efforts="sourceEfforts"
        :efforts-known="effortsKnown"
        :efforts-loading="effortsLoading"
        :params="draft.params"
        :changed-count="changedCount"
        @update:draft="setCaps"
        @update:draft-efforts="draft.efforts = $event"
        @update:default-effort="setDefaultEffort"
        @update:params="draft.params = $event"
      />
    </template>

    <!-- A refusal belongs to the surface that raised it: the page banner sits
         under the drawer's scrim. -->
    <div v-if="duplicateId" ref="refusalRef" class="forebrain-err-block dup" role="alert" data-testid="models-duplicate">
      <CircleAlert class="size-3.5" aria-hidden="true" />
      <div>
        <b class="dup__title">{{ t('models.duplicate') }}</b>
        <span class="forebrain-err-s">{{ t('models.duplicateHint') }}</span>
        <span class="dup__sig">
          <ModelSignature
            :model="draft.model"
            :provider-name="providerName"
            :base-url="draft.baseUrl"
            :api-path="draft.verbatim ? '' : draft.apiPath"
            :url-verbatim="draft.verbatim"
          />
        </span>
        <button type="button" class="dup__link" data-testid="models-goto-duplicate" @click="emit('duplicate', duplicateId)">
          {{ t('models.gotoDuplicate') }}
        </button>
      </div>
    </div>
    <div v-else-if="saveError" ref="refusalRef" class="forebrain-err-block" role="alert" data-testid="models-save-error">
      <CircleAlert class="size-3.5" aria-hidden="true" />
      <div>
        <b class="dup__title">{{ t('models.saveFailed') }}</b>
        <span class="forebrain-err-s">{{ saveError }}</span>
      </div>
    </div>

    <template #footer>
      <Button type="button" variant="ghost" class="forebrain-btn forebrain-btn--ghost h-auto text-xs" data-testid="models-cancel" @click="emit('close')">
        {{ t('common.cancel') }}
      </Button>
      <Button
        type="button"
        class="forebrain-btn forebrain-btn-primary h-auto text-xs"
        :disabled="saving || saveBlocked"
        data-testid="models-save"
        @click="save"
      >
        {{ saving ? t('common.loading') : t('common.save') }}
      </Button>
    </template>
  </ForebrainDrawer>
</template>

<script setup lang="ts">
/**
 * Add or edit one model (plan 266). A model is the four identity fields — the
 * service, the model name, the endpoint and the API path — plus whatever its
 * sign-in or its key needs; the advanced section is everything the catalog
 * already answers.
 *
 * Three things this drawer refuses to guess at:
 *
 * - **The key.** The gateway hands a stored key back in plaintext (owner
 *   2026-11-07) so the eye can really show it, and a body that carries the same
 *   value sends nothing: an untouched key is the stored key, and a blank one is
 *   not the clear.
 * - **A duplicate.** Four identical fields are one row (the table's own rule), so
 *   the refusal names the row that has them and this drawer offers to open it.
 * - **What a subscription row may change.** Nothing but the capabilities: its
 *   endpoint, path and key belong to the sign-in, and the drawer shows them
 *   read-only rather than as fields that would be rejected.
 */
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { CircleAlert, Eye, EyeClosed, LogIn, RotateCw } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'

import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ForebrainDrawer from '@/components/common/ForebrainDrawer.vue'
import ModelCapsFields from '@/components/models/ModelCapsFields.vue'
import ModelSignature from '@/components/models/ModelSignature.vue'
import ProtocolPicker from '@/components/models/ProtocolPicker.vue'
import ProviderCombobox from '@/components/models/ProviderCombobox.vue'
import RequestUrlLine from '@/components/models/RequestUrlLine.vue'
import {
  forebrainApi,
  GatewayHttpError,
  getErrorMessage,
  type LlmModelCaps,
  type LlmModelRow,
  type LlmProviderEndpoint,
  type LlmProviderOption,
} from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(
  defineProps<{
    open: boolean
    mode: 'add' | 'edit'
    /** The row being edited; null when adding. */
    row?: LlmModelRow | null
    providers: LlmProviderOption[]
    protocols: { apiPath: string; name: string }[]
    customId?: string
    /** The models the catalog suggests for one provider (「常用」). */
    suggestions?: Record<string, string[]>
    /** The provider an add starts on (the composer's 「添加自定义模型」). */
    presetProvider?: string
  }>(),
  { row: null, customId: 'custom', suggestions: () => ({}), presetProvider: '' },
)

const emit = defineEmits<{
  close: []
  saved: []
  /** The refusal named an existing row: the page opens it. */
  duplicate: [id: string]
  /** Sign in again to the subscription that wrote this row. */
  openSubscribe: [subscriptionId: string]
}>()

const { t } = useI18n()

interface Draft {
  provider: string
  endpointIndex: number
  baseUrl: string
  apiPath: string
  verbatim: boolean
  model: string
  apiKey: string
  params: string
  caps: LlmModelCaps
  efforts: string[]
}

const draft = reactive<Draft>(blankDraft())
const reveal = ref(false)
const providerOpen = ref(false)
const advancedOpen = ref(false)
const saving = ref(false)
const refreshing = ref(false)
const testing = ref(false)
const testState = ref('')
const saveError = ref('')
const duplicateId = ref('')
/** The catalog's own values: what an untouched control follows, and what the
 *  numbers' placeholders show. */
const catalogCaps = ref<LlmModelCaps>({})
const sourceEfforts = ref<string[]>([])
const effortsKnown = ref(false)
const effortsLoading = ref(false)
/** The refusal block, scrolled into view when it lands: the drawer's body
 *  scrolls, and a refusal the reader has to hunt for is the same bug in another
 *  form. */
const refusalRef = ref<HTMLElement | null>(null)
watch(
  () => [duplicateId.value, saveError.value] as const,
  async ([duplicate, failure]) => {
    if (!duplicate && !failure) return
    await nextTick()
    refusalRef.value?.scrollIntoView({ block: 'nearest' })
  },
)

/** The key the row stores, so an untouched field sends nothing. */
const originalKey = ref('')
/** The path the row stores when adding from an existing identity. */
const storedPath = ref('')

function blankDraft(): Draft {
  return { provider: '', endpointIndex: 0, baseUrl: '', apiPath: '', verbatim: false, model: '', apiKey: '', params: '', caps: {}, efforts: [] }
}

const sourceRow = computed(() => (props.row && props.row.source ? props.row : null))

const isCustom = computed(() => draft.provider === props.customId)

const providerName = computed(
  () => props.providers.find((provider) => provider.id === draft.provider)?.name ?? draft.provider,
)

const endpoints = computed<LlmProviderEndpoint[]>(
  () => props.providers.find((provider) => provider.id === draft.provider)?.endpoints ?? [],
)

const suggestions = computed(() => (isCustom.value ? [] : (props.suggestions[draft.provider] ?? [])))

/** Every field this form would store, so the section can say how many changed. */
const capsWrite = computed<LlmModelCaps>(() => {
  const out: LlmModelCaps = {}
  const catalog = catalogCaps.value
  const caps = draft.caps
  for (const key of ['images', 'pdf', 'video', 'audio', 'reasoning', 'reasoningOff'] as const) {
    if (caps[key] !== undefined && caps[key] !== catalog[key]) out[key] = caps[key]
  }
  if (draft.efforts.length) out.efforts = [...draft.efforts]
  if (caps.defaultEffort) out.defaultEffort = caps.defaultEffort
  if (caps.contextWindow !== undefined && caps.contextWindow !== catalog.contextWindow) out.contextWindow = caps.contextWindow
  if (caps.maxOutput !== undefined && caps.maxOutput !== catalog.maxOutput) out.maxOutput = caps.maxOutput
  return out
})

const changedCount = computed(() => Object.keys(capsWrite.value).length + (draft.params.trim() ? 1 : 0))

const saveBlocked = computed(() => {
  if (sourceRow.value) return false
  if (!draft.provider) return true
  // A custom provider must say which protocol it speaks: an address alone is
  // not a client, and 「自定义协议」 is the fourth choice that says so.
  if (isCustom.value && !draft.verbatim && !draft.apiPath) return true
  return !draft.model.trim()
})

const stateVariant = computed(() => {
  const state = sourceRow.value?.sourceState ?? ''
  return state === 'usable' ? 'success' : state === 'dead' || state === 'unreachable' ? 'warning' : 'gray'
})

const stateLabel = computed(() => {
  switch (sourceRow.value?.sourceState ?? '') {
    case 'usable':
      return t('models.stateUsable')
    case 'dead':
      return t('models.stateDead')
    case 'unreachable':
      return t('models.stateUnreachable')
    default:
      return t('models.stateMissing')
  }
})

/** The bullets a stored key is shown with: one per character it hides, held
 *  between four and twelve so a long key never widens the column. */
function bullets(key: string | undefined): string {
  const length = String(key ?? '').length || 32
  return '•'.repeat(Math.min(12, Math.max(4, length - 4)))
}

function setCaps(next: LlmModelCaps): void {
  draft.caps = next
}

function setDefaultEffort(level: string): void {
  draft.caps = { ...draft.caps, defaultEffort: level }
}

/** Picking an endpoint writes the address and the protocol it serves. */
function pickEndpoint(index: number): void {
  const endpoint = endpoints.value[index]
  if (!endpoint) return
  draft.endpointIndex = index
  draft.baseUrl = endpoint.baseUrl
  draft.apiPath = endpoint.apiPath
  draft.verbatim = false
}

/**
 * Picking a provider starts the row from that provider's first endpoint. A
 * service with no endpoints is 「自定义」, whose address is whatever is typed.
 */
function resetForProvider(): void {
  advancedOpen.value = false
  storedPath.value = ''
  const first = endpoints.value[0]
  draft.endpointIndex = 0
  if (first) {
    draft.baseUrl = first.baseUrl
    draft.apiPath = first.apiPath
  } else {
    draft.baseUrl = ''
    draft.apiPath = ''
  }
  draft.verbatim = false
  draft.caps = { ...catalogCaps.value }
  draft.efforts = []
}

watch(
  () => draft.provider,
  () => {
    resetForProvider()
    void loadCaps()
  },
)

// The endpoint chips write the address; a chip clicked back to where it was
// must not undo an edit the person made after it.
watch(
  () => [draft.provider, draft.model, draft.baseUrl] as const,
  () => {
    void loadCaps()
  },
)

/** The drawer is opened on another row: start from that row's values. */
watch(
  () => [props.open, props.row?.id, props.mode] as const,
  () => {
    if (!props.open) return
    testState.value = ''
    saveError.value = ''
    duplicateId.value = ''
    reveal.value = false
    providerOpen.value = false
    const row = props.row
    if (props.mode === 'edit' && row) {
      originalKey.value = row.apiKey ?? ''
      storedPath.value = row.apiPath
      Object.assign(draft, {
        provider: row.provider,
        endpointIndex: Math.max(
          0,
          endpoints.value.findIndex((endpoint) => endpoint.baseUrl === row.baseUrl && endpoint.apiPath === row.apiPath),
        ),
        baseUrl: row.baseUrl,
        apiPath: row.apiPath,
        verbatim: row.urlVerbatim,
        model: row.model,
        apiKey: row.apiKey ?? '',
        params: row.params ?? '',
        caps: { ...row.catalogCaps, ...row.caps },
        efforts: [...(row.caps.efforts ?? [])],
      } satisfies Partial<Draft>)
      catalogCaps.value = { ...row.catalogCaps }
      sourceEfforts.value = [...(row.catalogCaps.efforts ?? [])]
      effortsKnown.value = row.effortsKnown
      advancedOpen.value = false
      void loadCaps()
      return
    }
    originalKey.value = ''
    Object.assign(draft, blankDraft())
    // An add opened from the composer starts on the provider it named: the
    // drawer is one form, and 「添加自定义模型」 is that form already on 自定义.
    if (props.presetProvider) {
      draft.provider = props.presetProvider
      resetForProvider()
    }
    catalogCaps.value = {}
    sourceEfforts.value = []
    effortsKnown.value = false
    advancedOpen.value = false
  },
  { immediate: true },
)

/**
 * What this model can do by default, and which levels a source lists for it.
 * One request per settled identity: the answer decides whether the form shows
 * the level dropdown or the checkboxes, and it fills the numbers' placeholders.
 */
let capsTimer: ReturnType<typeof setTimeout> | null = null
async function loadCaps(): Promise<void> {
  if (capsTimer) clearTimeout(capsTimer)
  const provider = draft.provider
  const model = draft.model.trim()
  if (!provider || !model) {
    effortsLoading.value = false
    return
  }
  effortsLoading.value = true
  capsTimer = setTimeout(() => {
    capsTimer = null
    void (async () => {
      try {
        const answer = await forebrainApi.llmModelCatalogCaps({
          provider,
          model,
          baseUrl: draft.baseUrl,
          id: props.mode === 'edit' ? props.row?.id : undefined,
        })
        if (draft.provider !== provider || draft.model.trim() !== model) return
        const { effortsKnown: known, effortsSource: _source, ...caps } = answer
        catalogCaps.value = caps
        sourceEfforts.value = [...(caps.efforts ?? [])]
        effortsKnown.value = known
      } catch {
        // The lookup is a convenience: without it the checkboxes are offered,
        // which is the same answer a model no source lists gets.
        effortsKnown.value = false
      } finally {
        effortsLoading.value = false
      }
    })()
  }, 400)
}

/** The capability body one save sends: only what differs from the catalog. */
function capsBody(): LlmModelCaps {
  return capsWrite.value
}

async function save(): Promise<void> {
  if (saving.value || saveBlocked.value) return
  saving.value = true
  saveError.value = ''
  duplicateId.value = ''
  try {
    if (sourceRow.value) {
      // A subscription row keeps its identity: only the capabilities travel.
      await forebrainApi.updateLlmModel(sourceRow.value.id, { caps: capsBody(), params: draft.params.trim() })
      emit('saved')
      return
    }
    const body = {
      provider: draft.provider,
      model: draft.model.trim(),
      baseUrl: draft.baseUrl.trim(),
      apiPath: draft.verbatim ? '/chat/completions' : draft.apiPath.trim(),
      urlVerbatim: draft.verbatim,
      params: draft.params.trim(),
      caps: capsBody(),
      // An untouched key is the stored key: sending it back would be a write of
      // a value the person did not choose, and a blank one is not the clear.
      ...(draft.apiKey && draft.apiKey !== originalKey.value ? { apiKey: draft.apiKey } : {}),
    }
    if (props.mode === 'edit' && props.row) await forebrainApi.updateLlmModel(props.row.id, body)
    else await forebrainApi.createLlmModel(body)
    emit('saved')
  } catch (cause) {
    if (cause instanceof GatewayHttpError && cause.status === 409) {
      const existing = String(cause.payload?.existingId ?? '')
      if (existing) {
        duplicateId.value = existing
        return
      }
    }
    saveError.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

async function test(): Promise<void> {
  if (testing.value) return
  testing.value = true
  testState.value = ''
  try {
    const answer = await forebrainApi.testLlmModel({
      ...(props.mode === 'edit' && props.row ? { id: props.row.id } : {}),
      provider: draft.provider,
      model: draft.model.trim(),
      baseUrl: draft.baseUrl.trim(),
      apiPath: draft.verbatim ? '/chat/completions' : draft.apiPath.trim(),
      urlVerbatim: draft.verbatim,
      ...(draft.apiKey && draft.apiKey !== originalKey.value ? { apiKey: draft.apiKey } : {}),
    })
    testState.value = answer.ok ? 'ok' : String(answer.error ?? t('models.testFailed'))
  } catch (cause) {
    testState.value = getErrorMessage(cause)
  } finally {
    testing.value = false
  }
}

async function refreshKey(): Promise<void> {
  const row = sourceRow.value
  if (!row || refreshing.value) return
  refreshing.value = true
  saveError.value = ''
  try {
    await forebrainApi.refreshLlmModelKey(row.id)
    emit('saved')
  } catch (cause) {
    saveError.value = getErrorMessage(cause)
  } finally {
    refreshing.value = false
  }
}
</script>

<style scoped>
.models-field {
  display: grid;
  gap: 6px;
  margin-top: 14px;
}
.models-field:first-child {
  margin-top: 4px;
}
.models-field__label {
  font-size: 12px;
  font-weight: 500;
  color: var(--forebrain-text-2);
}
.models-field__hint {
  font-size: 12px;
  line-height: 1.5;
  color: var(--forebrain-muted-text);
}
.models-field__ok {
  font-size: 12px;
  color: var(--forebrain-success);
}
.models-field__bad {
  font-size: 12px;
  color: var(--forebrain-danger);
  word-break: break-word;
}
.models-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.models-chip {
  height: 26px;
  padding: 0 10px;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 999px;
  background: var(--forebrain-surface);
  font-size: 12px;
  color: var(--forebrain-text-2);
}
.models-chip--on {
  border-color: var(--forebrain-brand-border);
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}
.models-sugg {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.models-sugg__item {
  height: 22px;
  padding: 0 8px;
  border: 0;
  border-radius: 5px;
  background: var(--forebrain-button-alt-bg);
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  color: var(--forebrain-text);
}
.models-sugg__item:hover {
  background: var(--forebrain-button-alt-hover-bg);
}
/* The key field and its button share one line: the box flexes, the button does
   not, so the row is as wide as the drawer at any width. */
.keyrow {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.keyrow__box {
  position: relative;
  display: block;
  flex: 1;
  min-width: 0;
}
.keyrow__input {
  width: 100%;
  height: 32px;
  padding: 0 34px 0 10px;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 6px;
  background: var(--forebrain-input-bg);
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
  color: var(--forebrain-text);
  outline: none;
}
.keyrow__input--plain {
  font-size: 11.5px;
}
.keyrow__input:focus {
  border-color: var(--forebrain-brand-1);
  box-shadow: 0 0 0 3px var(--forebrain-brand-soft);
}
.keyrow__eye {
  position: absolute;
  top: 1px;
  right: 1px;
  width: 30px;
  height: 30px;
  display: grid;
  place-items: center;
  border: 0;
  border-radius: 5px;
  background: transparent;
  color: var(--forebrain-muted-text);
}
.keyrow__eye:hover {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
}
/* The read-only facts of a subscription row, and the card above them. */
.rows {
  margin-top: 10px;
}
.row {
  display: grid;
  grid-template-columns: 96px minmax(0, 1fr);
  align-items: center;
  gap: 10px;
  padding: 8px 0;
  border-top: 1px solid var(--forebrain-divider);
}
.row__label b {
  font-size: 12.5px;
  font-weight: 400;
  color: var(--forebrain-text-2);
}
.row__ro {
  font-size: 13px;
  color: var(--forebrain-text);
  word-break: break-all;
}
.row__ro--mono {
  font-family: var(--forebrain-mono-font);
  font-size: 12.5px;
}
.row__ro--quiet {
  color: var(--forebrain-muted-text);
}
.row__note {
  margin-left: 8px;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.src {
  display: grid;
  gap: 10px;
  margin-top: 4px;
  padding: 12px 14px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-bg-alt);
}
.src__head {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}
.src__head b {
  font-weight: 600;
}
.src p {
  margin: 0;
  font-size: 12px;
  line-height: 1.5;
  color: var(--forebrain-muted-text);
}
.src__acts {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.src__switch {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 10px;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.src__switch button {
  border: 0;
  background: transparent;
  padding: 0;
  font-size: 12px;
  color: var(--forebrain-brand-1);
}
/* The refusal block is `.forebrain-err-block`, the one the page's other forms
   raise theirs with. A duplicate adds the row it named, and the way to it. */
.dup {
  margin-top: 12px;
}
.dup__title {
  display: block;
  font-weight: 600;
  color: var(--forebrain-danger);
}
.dup__sig {
  display: block;
  margin-top: 6px;
  padding: 7px 9px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: var(--forebrain-surface);
}
.dup__link {
  margin-top: 6px;
  border: 0;
  background: transparent;
  padding: 0;
  font-size: 12.5px;
  font-weight: 500;
  color: var(--forebrain-brand-1);
}
</style>
