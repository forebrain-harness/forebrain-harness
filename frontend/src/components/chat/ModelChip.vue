<template>
  <div ref="rootRef" class="relative">
    <button
      type="button"
      class="forebrain-chip-btn"
      :disabled="disabled || switching"
      v-tip:top="t('chat.modelChipTitle')"
      aria-haspopup="listbox"
      :aria-expanded="open"
      data-testid="model-chip"
      @click="toggle"
    >
      <Cpu v-if="!compact" class="size-3.5" aria-hidden="true" />
      <span class="max-w-44 truncate">{{ label }}</span>
      <ChevronDown class="forebrain-chip-caret" aria-hidden="true" />
    </button>

    <!-- The menu is the design's 420px panel: every option is the model's
         signature, so two rows configured against two gateways read as two
         rows instead of one name repeated. -->
    <div
      v-if="open"
      class="modelmenu absolute bottom-full right-0 mb-1 w-[420px] max-w-[calc(100vw-2rem)]"
      role="listbox"
      :aria-label="t('chat.modelChipTitle')"
    >
      <label v-if="showFilter" class="modelmenu__search">
        <Search class="size-3.5" aria-hidden="true" />
        <input
          v-model="filter"
          type="search"
          :placeholder="t('models.searchPlaceholder')"
          :aria-label="t('models.searchPlaceholder')"
          data-testid="model-filter"
        />
      </label>
      <div class="modelmenu__list">
        <button
          v-for="option in visibleOptions"
          :key="option.id ?? `${option.provider}/${option.model}`"
          type="button"
          class="modelmenu__opt"
          :class="{ 'modelmenu__opt--on': isCurrent(option) }"
          role="option"
          :aria-selected="isCurrent(option)"
          :data-testid="`model-option-${option.model}`"
          :disabled="switching"
          @click="choose(option)"
        >
          <span class="modelmenu__lead">
            <Check v-if="isCurrent(option)" class="size-3.5" aria-hidden="true" />
          </span>
          <ModelSignature
            :model="option.model"
            :provider-name="providerNameOf(option)"
            :base-url="option.baseUrl ?? ''"
            :api-path="option.apiPath ?? ''"
            bare
            class="modelmenu__sig"
          >
            <span v-if="option.supportsImages" class="forebrain-chip-menu-badge" data-testid="model-supports-images">{{ t('chat.modelAcceptsImages') }}</span>
          </ModelSignature>
        </button>
        <p v-if="!visibleOptions.length" class="modelmenu__empty">{{ t('models.empty') }}</p>
      </div>
      <div class="modelmenu__foot">
        <button type="button" data-testid="model-add-custom" @click="openAddCustom">
          <Pencil class="size-3.5" aria-hidden="true" />{{ t('models.addCustom') }}
        </button>
        <button type="button" data-testid="model-manage" @click="manage">
          <SlidersHorizontal class="size-3.5" aria-hidden="true" />{{ t('models.manage') }}
        </button>
      </div>
    </div>

    <p v-if="error && !open" role="alert" class="absolute bottom-full left-0 z-20 mb-2 w-72 rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-surface)] px-2.5 py-2 text-[12px] text-[var(--forebrain-danger)]">
      {{ error }}
    </p>

    <!-- 「添加自定义模型」 opens the same drawer the settings page uses, in its
         custom mode: one form for one row, wherever it is opened from. -->
    <ModelFormDrawer
      v-if="formOpen"
      :open="formOpen"
      mode="add"
      :providers="providers"
      :protocols="protocols"
      :custom-id="customId"
      preset-provider="custom"
      @close="formOpen = false"
      @saved="onModelAdded"
      @duplicate="onModelAdded"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * The conversation's model, as one chip: what it runs on now, and the models
 * this primary agent is configured with to switch to. A choice writes the
 * session's own model row — the same switch /model makes, session-scoped — and
 * rides the next turn; the agent's default for other conversations never moves.
 * Switching resets the effort to the model's default, exactly as a /model
 * switch does. On a new chat there is no row to write yet: the pick is handed to
 * the page, which writes it once its first message opens the session.
 *
 * The menu shows each model's whole address (plan 266): a provider and a model
 * name cannot tell two rows apart, and a bare name list would hide the very
 * difference the person is choosing between. Past eight rows a filter appears,
 * because that is the point a list stops being scannable.
 */
import { computed, defineAsyncComponent, onMounted, onUnmounted, ref } from 'vue'
import { Check, ChevronDown, Cpu, Pencil, Search, SlidersHorizontal } from 'lucide-vue-next'
import { useRouter } from 'vue-router'

import ModelSignature from '@/components/models/ModelSignature.vue'
import { forebrainApi, getErrorMessage, type SessionModelOption, type SessionModelSelection } from '@/lib/api'
import { useI18n } from '@/locales'

// The drawer carries CodeMirror through its params editor: it loads when the
// person actually adds a model, not with the composer.
const ModelFormDrawer = defineAsyncComponent(() => import('@/components/models/ModelFormDrawer.vue'))

const props = defineProps<{
  sessionId: string
  selection: SessionModelSelection | null
  options: SessionModelOption[]
  disabled?: boolean
  /** The composer is narrow (the subagent drawer is open): the name alone. */
  compact?: boolean
}>()

const emit = defineEmits<{
  (e: 'saved'): void
  /** A new chat has no session to write: the page holds the pick for the one it opens. */
  (e: 'pick', option: SessionModelOption): void
}>()

const { t } = useI18n()
const router = useRouter()
const open = ref(false)
const switching = ref(false)
const error = ref('')
const filter = ref('')
const rootRef = ref<HTMLElement | null>(null)
const formOpen = ref(false)
const providers = ref<import('@/lib/api').LlmProviderOption[]>([])
const protocols = ref<{ apiPath: string; name: string }[]>([])
const customId = ref('custom')

const label = computed(() => props.selection?.model || t('chat.modelNone'))

/** Eight rows is where a list stops being scannable by eye. */
const showFilter = computed(() => props.options.length >= 8)

const visibleOptions = computed(() => {
  const needle = filter.value.trim().toLowerCase()
  if (!needle) return props.options
  return props.options.filter((option) =>
    [option.model, option.provider, option.baseUrl ?? '', option.apiPath ?? ''].some((value) =>
      String(value).toLowerCase().includes(needle),
    ),
  )
})

/**
 * The provider's display name, when the gateway's registry knows one. The
 * session's own model list carries the id only, and a menu that says
 * 「deepseek」 where every other surface says 「深度求索」 would look like another
 * service. One request serves every chip on the page; without it the id stands
 * in, which is the provider's own short name.
 */
const providerNames = ref<Record<string, string>>({})
let providerNamesRequest: Promise<Record<string, string>> | null = null
function ensureProviderNames(): void {
  providerNamesRequest ??= forebrainApi
    .llmProviders()
    .then((listing) => ({
      ...Object.fromEntries(listing.providers.map((provider) => [provider.id, provider.name])),
      // 「自定义」 is a provider id the registry lists by itself: a row against a
      // gateway of one's own must not read as the raw word `custom`.
      [listing.customId]: t('models.providerCustom'),
    }))
    .catch(() => ({}))
  void providerNamesRequest.then((names) => {
    providerNames.value = names
  })
}

function providerNameOf(option: SessionModelOption): string {
  return providerNames.value[option.provider] ?? option.provider
}

function toggle(): void {
  open.value = !open.value
  if (open.value) ensureProviderNames()
}

function isCurrent(option: SessionModelOption): boolean {
  if (props.selection == null) return false
  // The configured row is the identity: the same provider/model may be
  // configured more than once, and only the row the selection names is current.
  if (option.id && props.selection.modelId) return option.id === props.selection.modelId
  return option.provider === props.selection.provider && option.model === props.selection.model
}

async function choose(option: SessionModelOption) {
  const sessionId = props.sessionId
  if (switching.value) return
  open.value = false
  if (isCurrent(option)) return
  if (!sessionId) {
    emit('pick', option)
    return
  }
  switching.value = true
  error.value = ''
  try {
    await forebrainApi.saveSessionModel(sessionId, {
      modelId: option.id,
      provider: option.provider,
      model: option.model,
      // A model switch resets the effort to the model's own default — the
      // effort chip pins one for the model it belongs to.
      effort: '',
    })
    emit('saved')
  } catch (cause) {
    // Nothing changed on the server; the chip keeps the state it had, and
    // the server's own words say why.
    error.value = getErrorMessage(cause)
  } finally {
    switching.value = false
  }
}

async function openAddCustom(): Promise<void> {
  open.value = false
  try {
    const listing = await forebrainApi.llmProviders()
    providers.value = listing.providers
    protocols.value = listing.protocols
    customId.value = listing.customId
  } catch {
    providers.value = []
  }
  formOpen.value = true
}

/** The row landed: the page rereads its options, and this menu is stale. */
function onModelAdded(): void {
  formOpen.value = false
  emit('saved')
}

function manage(): void {
  open.value = false
  void router.push({ path: '/settings', query: { tab: 'models' } })
}

function onDocPointerDown(event: PointerEvent) {
  if (open.value && rootRef.value && !rootRef.value.contains(event.target as Node)) open.value = false
}

onMounted(() => {
  ensureProviderNames()
  document.addEventListener('pointerdown', onDocPointerDown)
})
onUnmounted(() => document.removeEventListener('pointerdown', onDocPointerDown))

// A refusal notice under a message points the reader here: it opens the menu
// from its own click, which lands after the pointerdown that would close it.
function openMenu() {
  if (props.disabled || switching.value) return
  open.value = true
  ensureProviderNames()
}

defineExpose({ openMenu })
</script>

<style scoped>
/* The design's `.badge.ok` inside the model menu: the option's name row is a
   flex row, so `margin-left: auto` pins the badge to the option's right edge. */
.forebrain-chip-menu-badge {
  display: inline-flex;
  align-items: center;
  margin-left: auto;
  height: 18px;
  padding: 0 7px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 500;
  white-space: nowrap;
  background: var(--forebrain-success-soft);
  color: var(--forebrain-success);
}
.modelmenu {
  position: absolute;
  z-index: 20;
  padding: 4px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-surface);
  box-shadow: var(--forebrain-shadow-pop);
}
.modelmenu__search {
  display: flex;
  align-items: center;
  gap: 7px;
  height: 30px;
  margin: 2px 2px 4px;
  padding: 0 9px;
  border-radius: 6px;
  background: var(--forebrain-bg-alt);
  color: var(--forebrain-muted-text);
  font-size: 12.5px;
}
.modelmenu__search input {
  flex: 1;
  min-width: 0;
  border: 0;
  outline: none;
  background: transparent;
  font-size: 12.5px;
  color: var(--forebrain-text);
}
.modelmenu__list {
  max-height: 440px;
  overflow: auto;
}
.modelmenu__opt {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  width: 100%;
  padding: 6px 8px;
  border: 0;
  border-radius: 7px;
  background: transparent;
  text-align: left;
}
.modelmenu__opt:hover {
  background: var(--forebrain-bg-alt);
}
.modelmenu__lead {
  width: 14px;
  flex: none;
  padding-top: 3px;
  color: var(--forebrain-brand-1);
}
.modelmenu__sig {
  flex: 1;
}
.modelmenu__opt--on :deep(.modelsig__model) {
  color: var(--forebrain-brand-1);
}
.modelmenu__empty {
  margin: 0;
  padding: 10px;
  font-size: 12px;
  color: var(--forebrain-muted-text);
  text-align: center;
}
.modelmenu__foot {
  margin-top: 4px;
  padding-top: 4px;
  border-top: 1px solid var(--forebrain-divider);
}
.modelmenu__foot button {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 7px 8px;
  border: 0;
  border-radius: 7px;
  background: transparent;
  font-size: 12.5px;
  color: var(--forebrain-text-2);
  text-align: left;
}
.modelmenu__foot button:hover {
  background: var(--forebrain-bg-alt);
  color: var(--forebrain-text);
}
</style>
