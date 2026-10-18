<template>
  <ForebrainDrawer :open="true" :title="title" :readonly="readonly" @close="requestClose">
    <!-- Where this entry stands: the table only has room for one badge, so the
         drawer carries the rest — the layer it came from, the failure in its own
         words, and, for a viewer, the OAuth state, the official marker and the
         tools the runtime exposes. -->
    <div v-if="row" class="mcp-state" data-testid="mcp-state">
      <div class="mcp-state-row">
        <span class="mcp-state-key">{{ t('mcp.fieldSource') }}</span>
        <ScopeBadge :type="scope" :label="layerLabel" />
      </div>
      <div class="mcp-state-row">
        <span class="mcp-state-key">{{ t('mcp.fieldStatus') }}</span>
        <BadgeComponent :variant="statusVariant" data-testid="mcp-state-status">{{ statusLabel }}</BadgeComponent>
        <!-- An entry a higher layer's same-named one overrides: the badge is
             未运行 like any other silent entry, and the words beside it say why
             (the prototype's `row.over`). -->
        <span v-if="shadowedHint" class="mcp-key" data-testid="mcp-state-shadowed">{{ shadowedHint }}</span>
        <span v-else-if="row.disabledByConfig" class="mcp-key">enabled: false</span>
        <span v-else-if="row.disabledNextSession" class="mcp-key">{{ t('mcp.notLoadedNextSession') }}</span>
        <BadgeComponent v-if="readonly && row.officialUrl" data-testid="mcp-state-official">{{ t('mcp.official') }}</BadgeComponent>
      </div>
      <div v-if="failure" class="mcp-state-row">
        <span class="mcp-state-key">{{ t('mcp.fieldReason') }}</span>
        <span class="mcp-state-raw" data-testid="mcp-state-error">{{ failure }}</span>
      </div>
      <div v-if="readonly && oauthStatus" class="mcp-state-row">
        <span class="mcp-state-key">{{ t('mcp.fieldOAuthStatus') }}</span>
        <span data-testid="mcp-state-oauth">{{ oauthStatus }}</span>
      </div>
      <template v-if="readonly && toolNames.length">
        <div class="mcp-state-row">
          <span class="mcp-state-key">{{ t('mcp.fieldTools') }}</span>
          <button type="button" class="mcp-link" data-testid="mcp-state-tools-toggle" @click="stateToolsOpen = !stateToolsOpen">
            {{ stateToolsOpen ? t('mcp.toolsCollapse') : t('mcp.toolsToggle') }}
          </button>
        </div>
        <div v-if="stateToolsOpen" class="mcp-tools" data-testid="mcp-state-tools">
          <span v-for="name in toolNames" :key="name">{{ name }}</span>
        </div>
      </template>
    </div>

    <div class="mcp-grid2">
      <label class="mcp-field">
        <span class="mcp-label">{{ t('common.name') }}<span class="mcp-key">name</span></span>
        <input
          v-model="form.name"
          class="forebrain-field h-[34px] font-mono"
          :readonly="readonly"
          :placeholder="readonly ? t('mcp.unsetValue') : t('mcp.phName')"
          autocomplete="off"
          data-testid="mcp-name"
        />
      </label>
      <label class="mcp-field">
        <span class="mcp-label">{{ t('mcp.fieldTransport') }}<span class="mcp-key">transport</span></span>
        <select v-model="form.transport" class="forebrain-field h-[34px]" :disabled="readonly" data-testid="mcp-transport">
          <option value="stdio">{{ t('mcp.transportStdio') }}</option>
          <option value="streamable_http">{{ t('mcp.transportHttp') }}</option>
        </select>
      </label>
    </div>

    <label class="mcp-field">
      <span class="mcp-label">{{ t('mcp.fieldTarget') }}</span>
      <input
        v-model="form.target"
        class="forebrain-field h-[34px] font-mono"
        :readonly="readonly"
        :placeholder="readonly ? t('mcp.unsetValue') : targetPlaceholder"
        autocomplete="off"
        data-testid="mcp-target"
      />
    </label>

    <div class="mcp-grid2">
      <div class="mcp-switch-row">
        <SwitchComponent
          v-model="form.required"
          :disabled="readonly"
          :aria-label="t('mcp.required')"
          data-testid="mcp-required"
        />
        <span class="mcp-label">{{ t('mcp.required') }}</span>
      </div>
      <div class="mcp-switch-row">
        <SwitchComponent
          v-model="form.enabled"
          :disabled="readonly"
          :aria-label="t('mcp.fieldEnabled')"
          data-testid="mcp-enabled"
        />
        <span class="mcp-label">{{ t('mcp.fieldEnabled') }}</span>
      </div>
    </div>
    <span v-if="!readonly" class="mcp-hint">{{ t('mcp.hintSwitches') }}</span>

    <div class="mcp-grid2">
      <label class="mcp-field">
        <span class="mcp-label">{{ t('mcp.fieldStartupTimeout') }}<span class="mcp-key">startup_timeout</span></span>
        <input
          v-model="form.startupTimeout"
          class="forebrain-field h-[34px] font-mono"
          :readonly="readonly"
          :placeholder="readonly ? t('mcp.unsetValue') : t('mcp.phStartupTimeout')"
          inputmode="numeric"
          autocomplete="off"
          data-testid="mcp-startup-timeout"
        />
      </label>
      <label class="mcp-field">
        <span class="mcp-label">{{ t('mcp.fieldRequestTimeout') }}<span class="mcp-key">request_timeout</span></span>
        <input
          v-model="form.requestTimeout"
          class="forebrain-field h-[34px] font-mono"
          :readonly="readonly"
          :placeholder="readonly ? t('mcp.unsetValue') : t('mcp.phRequestTimeout')"
          inputmode="numeric"
          autocomplete="off"
          data-testid="mcp-request-timeout"
        />
      </label>
    </div>
    <span v-if="!readonly" class="mcp-hint">{{ t('mcp.hintTimeouts') }}</span>

    <!-- The advanced half follows the transport: a local command carries an
         environment and a process rule, a remote one carries headers and OAuth.
         A viewer opens it, because everything it hides is a fact about the
         record rather than a choice. -->
    <details class="mcp-adv" :open="advancedOpen" data-testid="mcp-advanced">
      <summary>
        <ChevronRight class="mcp-adv-caret" aria-hidden="true" />
        {{ isStdio ? t('mcp.advancedStdio') : t('mcp.advancedHttp') }}
      </summary>
      <div class="mcp-adv-body">
        <template v-if="isStdio">
          <span class="mcp-label">{{ t('mcp.fieldEnv') }}<span class="mcp-key">env</span></span>
          <McpKeyValueRows v-model="form.env" :add-label="t('mcp.kvAddEnv')" :readonly="readonly" />
          <div class="mcp-switch-row">
            <SwitchComponent
              v-model="form.inheritParentEnv"
              :disabled="readonly"
              :aria-label="t('mcp.fieldInheritParentEnv')"
              data-testid="mcp-inherit-parent-env"
            />
            <span class="mcp-label">
              {{ t('mcp.fieldInheritParentEnv') }}<span class="mcp-key">inherit_parent_env</span>
            </span>
          </div>
          <label class="mcp-field">
            <span class="mcp-label">
              {{ t('mcp.fieldCommandSha256') }}<span class="mcp-key">expected_command_sha256</span>
            </span>
            <input
              v-model="form.expectedCommandSha256"
              class="forebrain-field h-[34px] font-mono"
              :readonly="readonly"
              :placeholder="readonly ? t('mcp.unsetValue') : t('mcp.phSha256')"
              autocomplete="off"
              data-testid="mcp-command-sha256"
            />
          </label>
        </template>
        <template v-else>
          <span class="mcp-label">{{ t('mcp.fieldHeaders') }}<span class="mcp-key">headers</span></span>
          <McpKeyValueRows v-model="form.headers" :add-label="t('mcp.kvAddHeaders')" :readonly="readonly" />
          <div class="mcp-switch-row">
            <SwitchComponent
              v-model="form.disableStandaloneSse"
              :disabled="readonly"
              :aria-label="t('mcp.fieldDisableSse')"
              data-testid="mcp-disable-standalone-sse"
            />
            <span class="mcp-label">
              {{ t('mcp.fieldDisableSse') }}<span class="mcp-key">disable_standalone_sse</span>
            </span>
          </div>
          <span class="mcp-label">OAuth<span class="mcp-key">oauth</span></span>
          <label class="mcp-field">
            <span class="mcp-label">{{ t('mcp.fieldOAuthMode') }}<span class="mcp-key">mode</span></span>
            <select v-model="form.oauth.mode" class="forebrain-field h-[34px]" :disabled="readonly" data-testid="mcp-oauth-mode">
              <option value="">{{ t('mcp.oauthModeNone') }}</option>
              <option value="static">{{ t('mcp.oauthModeStatic') }}</option>
              <option value="client_credentials">{{ t('mcp.oauthModeClientCredentials') }}</option>
              <option value="refresh">{{ t('mcp.oauthModeRefresh') }}</option>
            </select>
          </label>
          <div class="mcp-grid2">
            <label class="mcp-field">
              <span class="mcp-label">
                {{ t('mcp.fieldAuthorizationUrl') }}<span class="mcp-key">authorization_url</span>
              </span>
              <input
                v-model="form.oauth.authorizationUrl"
                class="forebrain-field h-[34px] font-mono"
                :readonly="readonly"
                :placeholder="readonly ? t('mcp.unsetValue') : t('mcp.phAuthorizationUrl')"
                autocomplete="off"
                data-testid="mcp-oauth-authorization-url"
              />
            </label>
            <label class="mcp-field">
              <span class="mcp-label">{{ t('mcp.fieldTokenUrl') }}<span class="mcp-key">token_url</span></span>
              <input
                v-model="form.oauth.tokenUrl"
                class="forebrain-field h-[34px] font-mono"
                :readonly="readonly"
                :placeholder="readonly ? t('mcp.unsetValue') : t('mcp.phTokenUrl')"
                autocomplete="off"
                data-testid="mcp-oauth-token-url"
              />
            </label>
          </div>
          <div class="mcp-grid2">
            <label class="mcp-field">
              <span class="mcp-label">client_id</span>
              <input
                v-model="form.oauth.clientId"
                class="forebrain-field h-[34px] font-mono"
                :readonly="readonly"
                :placeholder="readonly ? t('mcp.unsetValue') : ''"
                autocomplete="off"
                data-testid="mcp-oauth-client-id"
              />
            </label>
            <label class="mcp-field">
              <span class="mcp-label">client_secret</span>
              <input
                v-model="form.oauth.clientSecret"
                class="forebrain-field h-[34px] font-mono"
                :readonly="readonly"
                :placeholder="readonly ? t('mcp.unsetValue') : ''"
                autocomplete="off"
                data-testid="mcp-oauth-client-secret"
              />
            </label>
          </div>
          <div class="mcp-grid2">
            <label class="mcp-field">
              <span class="mcp-label">scopes</span>
              <input
                v-model="form.oauth.scopes"
                class="forebrain-field h-[34px] font-mono"
                :readonly="readonly"
                :placeholder="readonly ? t('mcp.unsetValue') : t('mcp.phScopes')"
                autocomplete="off"
                data-testid="mcp-oauth-scopes"
              />
            </label>
            <label class="mcp-field">
              <span class="mcp-label">redirect_url</span>
              <input
                v-model="form.oauth.redirectUrl"
                class="forebrain-field h-[34px] font-mono"
                :readonly="readonly"
                :placeholder="readonly ? t('mcp.unsetValue') : t('mcp.phRedirectUrl')"
                autocomplete="off"
                data-testid="mcp-oauth-redirect-url"
              />
            </label>
          </div>
        </template>

        <label class="mcp-field">
          <span class="mcp-label">
            {{ t('mcp.fieldDefaultApproval') }}<span class="mcp-key">default_tools_approval_mode</span>
          </span>
          <select
            v-model="form.defaultToolsApprovalMode"
            class="forebrain-field h-[34px]"
            :disabled="readonly"
            data-testid="mcp-default-approval"
          >
            <option value="">{{ t('mcp.approvalInherit') }}</option>
            <option value="auto">{{ t('mcp.approvalAuto') }}</option>
            <option value="prompt">{{ t('mcp.approvalPrompt') }}</option>
            <option value="writes">{{ t('mcp.approvalWrites') }}</option>
            <option value="approve">{{ t('mcp.approvalApprove') }}</option>
          </select>
        </label>
        <template v-if="!readonly">
          <span class="mcp-hint">{{ t('mcp.hintToolsTable') }}</span>
          <span class="mcp-hint">{{ t('mcp.hintSecrets') }}</span>
        </template>
      </div>
    </details>

    <span class="mcp-location" data-testid="mcp-config-location">{{ locationLine }}</span>

    <!-- A row of another layer is edited elsewhere, and this is the way there;
         a row of this page's own layer can be tried out before it is saved. -->
    <div v-if="readonly" class="mcp-test-row">
      <RouterLink v-if="editPlace.to" class="mcp-link" :to="editPlace.to" data-testid="mcp-edit-elsewhere">
        {{ editPlace.label }}
      </RouterLink>
    </div>
    <div v-else class="mcp-test-row">
      <div class="mcp-test">
        <button
          type="button"
          class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs"
          :disabled="testState === 'running'"
          data-testid="mcp-test-run"
          @click="runTest"
        >
          <PlugZap class="size-3.5" aria-hidden="true" />{{ t('mcp.testConnection') }}
        </button>
        <span v-if="testState === 'running'" class="mcp-hint" data-testid="mcp-test-running">{{ t('mcp.testing') }}</span>
        <template v-else-if="testState === 'ok'">
          <BadgeComponent variant="success" data-testid="mcp-test-ok">✓ {{ t('mcp.statusConnected') }}</BadgeComponent>
          <span class="mcp-hint" data-testid="mcp-test-summary">{{ testSummary }}</span>
          <button
            v-if="testTools.length"
            type="button"
            class="mcp-link"
            data-testid="mcp-test-tools-toggle"
            @click="testToolsOpen = !testToolsOpen"
          >
            {{ testToolsOpen ? t('mcp.toolsCollapse') : t('mcp.toolsToggle') }}
          </button>
        </template>
        <template v-else-if="testState === 'failed'">
          <BadgeComponent variant="error" data-testid="mcp-test-failed">✗ {{ t('mcp.statusError') }}</BadgeComponent>
          <span class="mcp-test-raw" data-testid="mcp-test-error">{{ testError }}</span>
        </template>
      </div>
      <div v-if="testState === 'ok' && testToolsOpen" class="mcp-tools" data-testid="mcp-test-tools">
        <span v-for="name in testTools" :key="name">{{ name }}</span>
      </div>
    </div>

    <!-- The server's own words, verbatim: a 409 names the entry that already
         exists, a 400 names the field it could not take. -->
    <div v-if="saveError" class="forebrain-err-block" role="alert" data-testid="mcp-save-error">
      <CircleAlert class="size-3.5" aria-hidden="true" />
      <div>
        <b>{{ t('mcp.saveFailed') }}</b>
        <span class="forebrain-err-s">{{ saveError }}</span>
      </div>
    </div>

    <template #footer>
      <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" data-testid="mcp-cancel" @click="requestClose">
        {{ t('common.cancel') }}
      </button>
      <button
        v-if="!readonly"
        type="button"
        class="forebrain-btn forebrain-btn-primary text-xs"
        :disabled="saving"
        data-testid="mcp-save"
        @click="save"
      >
        {{ saving ? t('mcp.saving') : t('common.save') }}
      </button>
    </template>
  </ForebrainDrawer>
</template>

<script setup lang="ts">
/**
 * One MCP entry in the right-hand drawer (plan 074): the whole record, in the
 * order the prototype lists it, for 查看 (never editable), 编辑 and 添加.
 *
 * The contract with the table:
 *
 *   mode      'new' | 'edit' | 'view' — 'new' starts from an empty form,
 *             'edit' and 'view' from `row`;
 *   scope     which layer the entry belongs to, and therefore which write the
 *             save goes through;
 *   projectId the project's id for the project layer (the route needs it);
 *   agentId   the active primary agent's id for the agent layer;
 *   row       the entry being edited or viewed, `undefined` when adding;
 *   close     the operator cancelled, pressed Escape, clicked the mask or the
 *             X — nothing was written;
 *   saved     a save went through; the argument is the entry as the gateway
 *             answered, and the table reloads either way.
 *
 * The component is mounted per open (`v-if` in the caller) so a fresh open
 * re-reads `row`; the watch below keeps that true for a caller that reuses one
 * instance and changes `row` instead.
 *
 * Nothing here writes on its own: 测试连接 only asks the gateway to start the
 * entry as a new session would, and the form's payload is built by
 * `mcpWritePayload`, so an untouched field — including the per-tool approval
 * table that has no form field — is left exactly as stored.
 */
import { computed, reactive, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { ChevronRight, CircleAlert, PlugZap } from 'lucide-vue-next'

import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ForebrainDrawer from '@/components/common/ForebrainDrawer.vue'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import { forebrainApi, getErrorMessage, type McpLayer, type McpServerRow, type McpTestResult } from '@/lib/api'
import { useI18n } from '@/locales'

import McpKeyValueRows from './McpKeyValueRows.vue'
import { mcpEditPlace, mcpFormOf, mcpStatus, mcpWritePayload, type McpServerForm } from './mcpRows'

const props = defineProps<{
  /** 'new' adds an entry, 'edit' changes one, 'view' only reads one. */
  mode: 'new' | 'edit' | 'view'
  /** The layer the entry belongs to: which write the save goes through. */
  scope: McpLayer
  /** The project's id, for the project layer. */
  projectId?: string
  /** The active primary agent's id, for the agent layer. */
  agentId: string
  /** The entry being edited or viewed; absent when adding. */
  row?: McpServerRow
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved', row: McpServerRow | null): void
}>()

const { t } = useI18n()

const readonly = computed(() => props.mode === 'view')
const row = computed(() => props.row ?? null)
const form = reactive<McpServerForm>(mcpFormOf(props.row ?? null))

const saving = ref(false)
const saveError = ref('')
const advancedOpen = ref(props.mode === 'view')
const stateToolsOpen = ref(false)

/** What one connection test is doing, or what it found: nothing, running, a
 * server that answered, a server that did not. */
const testState = ref<'idle' | 'running' | 'ok' | 'failed'>('idle')
const testError = ref('')
const testResult = ref<McpTestResult | null>(null)
const testToolsOpen = ref(false)

// A caller that changes `row` on a mounted drawer gets a form that follows it.
watch(
  () => props.row,
  (next) => {
    Object.assign(form, mcpFormOf(next ?? null))
    saveError.value = ''
    testState.value = 'idle'
    testError.value = ''
    testResult.value = null
  },
)

const layerLabel = computed(() => {
  if (props.scope === 'project') return t('mcp.layerProject')
  if (props.scope === 'agent') return t('mcp.layerAgent')
  return t('mcp.layerGlobal')
})

const title = computed(() => {
  if (props.mode === 'new') return t('mcp.drawerAddTitle', { where: layerLabel.value })
  const name = row.value?.name ?? ''
  if (props.mode === 'edit') return t('mcp.drawerEditTitle', { name })
  return t('mcp.drawerViewTitle', { name })
})

const isStdio = computed(() => form.transport !== 'streamable_http')

const targetPlaceholder = computed(() => (isStdio.value ? t('mcp.phTargetStdio') : t('mcp.phTargetHttp')))

const status = computed(() => (row.value ? mcpStatus(row.value) : { tone: 'gray' as const, label: '', tip: '' }))

/** The grey words after the status badge for an entry a higher layer overrides;
 * empty when nothing overrides this one. */
const shadowedHint = computed(() => {
  const over = (row.value?.shadowedBy ?? '').trim()
  if (over === '') return ''
  return t('mcp.shadowedHint', { name: row.value?.shadowedByName || '—' })
})

/** The badge's words. A shadowed entry reads 未运行, not 未运行 · 被覆盖: the
 * drawer has a line beside the badge for the reason. */
const statusLabel = computed(() => (shadowedHint.value !== '' ? t('mcp.status.idle') : status.value.label))

const statusVariant = computed(() => {
  switch (status.value.tone) {
    case 'ok':
      return 'success' as const
    case 'warn':
      return 'warning' as const
    case 'err':
      return 'error' as const
    default:
      return 'gray' as const
  }
})

const failure = computed(() => (row.value?.error ?? '').trim())

/** The OAuth state as words, the way the runtime reports it. */
const oauthStatus = computed(() => {
  switch (row.value?.authStatus) {
    case 'needs-auth':
      return t('mcp.oauthNeedsAuth')
    case 'failed':
      return t('mcp.oauthAuthFailed')
    case 'authenticated':
      return t('mcp.oauthAuthorized')
    default:
      return ''
  }
})

const toolNames = computed(() => (row.value?.tools ?? []).map((tool) => tool.name))

const testTools = computed(() => (testResult.value?.tools ?? []).map((tool) => tool.name))

const testSummary = computed(() =>
  t('mcp.testSummary', {
    count: testResult.value?.toolCount ?? 0,
    seconds: ((testResult.value?.elapsedMs ?? 0) / 1000).toFixed(1),
  }),
)

const editPlace = computed(() => mcpEditPlace(props.scope, props.projectId ?? ''))

/** The file a save writes, said the same way the design does. */
const locationLine = computed(() => {
  const at =
    props.scope === 'project'
      ? t('mcp.configPathProject')
      : props.scope === 'agent'
        ? t('mcp.configPathAgent', { agent: props.agentId })
        : t('mcp.configPathGlobal')
  if (readonly.value) return t('mcp.configDefinedAt', { at })
  return props.scope === 'project' ? t('mcp.configWritesProject', { at }) : t('mcp.configWritesAt', { at })
})

/** The id the layer's routes carry: none for the shared layer. */
const ownerId = computed(() => {
  if (props.scope === 'agent') return props.agentId
  if (props.scope === 'project') return props.projectId ?? ''
  return ''
})

function requestClose() {
  // A save in flight owns the drawer: its answer decides what happens next.
  if (saving.value) return
  emit('close')
}

async function save() {
  if (readonly.value || saving.value) return
  saving.value = true
  saveError.value = ''
  try {
    const body = mcpWritePayload(row.value, form)
    const result =
      props.mode === 'new'
        ? await forebrainApi.mcpCreate(props.scope, ownerId.value, body)
        : await forebrainApi.mcpUpdate(props.scope, ownerId.value, row.value?.name ?? '', body)
    emit('saved', result?.server ?? null)
  } catch (error) {
    saveError.value = getErrorMessage(error)
  } finally {
    saving.value = false
  }
}

/**
 * Try the entry as a new session would start it. The whole form travels, so a
 * server that has not been saved yet (an addition, or an edit in progress) can
 * be tried; nothing is written and the drawer stays where it is.
 */
async function runTest() {
  if (testState.value === 'running') return
  testState.value = 'running'
  testError.value = ''
  testResult.value = null
  testToolsOpen.value = false
  try {
    const result = await forebrainApi.mcpTest(props.scope, ownerId.value, mcpWritePayload(null, form))
    if (result?.ok) {
      testResult.value = result
      testState.value = 'ok'
      return
    }
    testError.value = (result?.error ?? '').trim()
    testState.value = 'failed'
  } catch (error) {
    testError.value = getErrorMessage(error)
    testState.value = 'failed'
  }
}
</script>

<style scoped>
/* The record's standing, boxed: a muted panel of label/value rows. */
.mcp-state {
  display: grid;
  gap: 6px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  padding: 10px 12px;
  background: var(--forebrain-bg-alt);
}

.mcp-state-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  font-size: 12.5px;
  color: var(--forebrain-text);
}

/* The label column: wide enough for 来源 / 状态 / 原因, and it may grow for a
   longer one instead of clipping it. */
.mcp-state-key {
  min-width: 40px;
  flex: none;
  color: var(--forebrain-muted-text);
}

.mcp-state-raw {
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  color: var(--forebrain-text-2);
  word-break: break-all;
}

/* The field's config key, in the prototype's .key: small, grey, monospace. */
.mcp-key {
  margin-left: 6px;
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  font-weight: 400;
  color: var(--forebrain-muted-text);
}

.mcp-label {
  font-size: 12px;
  font-weight: 500;
  color: var(--forebrain-text-2);
}

.mcp-field {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.mcp-grid2 {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.mcp-switch-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 32px;
}

.mcp-hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}

.mcp-location {
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
  color: var(--forebrain-muted-text);
}

/* The advanced block: a rule above it, a caret that turns when it opens. */
.mcp-adv {
  border-top: 1px solid var(--forebrain-divider);
  padding-top: 10px;
}

.mcp-adv > summary {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  font-weight: 500;
  color: var(--forebrain-text-2);
  cursor: pointer;
  list-style: none;
}

.mcp-adv > summary::-webkit-details-marker {
  display: none;
}

.mcp-adv-caret {
  width: 13px;
  height: 13px;
  flex: none;
  color: var(--forebrain-muted-text);
  transition: transform 120ms ease;
}

.mcp-adv[open] > summary {
  color: var(--forebrain-text);
}

.mcp-adv[open] > summary .mcp-adv-caret {
  transform: rotate(90deg);
}

.mcp-adv-body {
  display: grid;
  gap: 10px;
  padding-top: 10px;
}

.mcp-test-row {
  border-top: 1px solid var(--forebrain-divider);
  padding-top: 12px;
  display: grid;
  gap: 8px;
  justify-items: start;
}

.mcp-test {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.mcp-test-raw {
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  color: var(--forebrain-text-2);
  word-break: break-all;
}

.mcp-tools {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.mcp-tools span {
  border-radius: 4px;
  padding: 2px 7px;
  background: var(--forebrain-button-alt-bg);
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  color: var(--forebrain-text-2);
}

/* A text action inside the drawer (工具列表, 在…修改): the prototype's .btn.link. */
.mcp-link {
  border: 0;
  padding: 0;
  background: transparent;
  color: var(--forebrain-brand-1);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
}

.mcp-link:hover {
  color: var(--forebrain-brand-hover);
  text-decoration: underline;
}
</style>
