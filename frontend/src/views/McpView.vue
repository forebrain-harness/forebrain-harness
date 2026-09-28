<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6">
    <div class="mx-auto w-full max-w-3xl">
      <header class="mb-5 flex items-end justify-between gap-4">
        <div>
          <h1 class="font-serif text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">{{ t('mcp.title') }}</h1>
          <p class="mt-1 text-[13px] leading-relaxed text-[var(--forebrain-text-2)]">{{ t('mcp.description') }}</p>
        </div>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
          {{ t('common.refresh') }}
        </button>
      </header>

      <p v-if="!runtimeAvailable && records.length" class="mb-4 text-[12px] text-[var(--forebrain-muted-text)]">
        {{ t('mcp.runtimeUnavailable') }}
      </p>

      <p v-if="error" class="mb-4 rounded-xl border border-[rgba(160,70,70,0.36)] bg-[rgba(160,70,70,0.08)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <div v-if="loading && !records.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <ul v-else-if="records.length" class="space-y-2">
        <li
          v-for="record in records"
          :key="record.name"
          class="rounded-xl border px-4 py-3"
          :class="record.scope === 'project' ? 'border-[rgba(120,140,190,0.4)] bg-[var(--forebrain-surface)]' : 'border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)]'"
        >
          <div class="flex flex-wrap items-center gap-2">
            <span class="font-mono text-[13px] text-[var(--forebrain-text)]">{{ record.name }}</span>
            <span v-if="record.transport" class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[11px] text-[var(--forebrain-muted-text)]">
              {{ record.transport }}
            </span>
            <span class="rounded-full border px-2 py-0.5 text-[10px]" :class="record.scope === 'project' ? 'border-[var(--forebrain-brand-1)] text-[var(--forebrain-brand-1)]' : 'border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]'">
              {{ record.scope === 'project' ? t('mcp.scopeProject') : t('mcp.scopeGlobal') }}
            </span>
            <span v-if="record.officialUrl" class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[11px] text-[var(--forebrain-brand-1)]">
              {{ t('mcp.official') }}
            </span>
            <span
              v-if="record.running === false"
              class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[11px] text-[var(--forebrain-muted-text)]"
              data-status="disabled"
            >
              {{ t('mcp.statusDisabled') }}
            </span>
            <span
              v-else-if="record.connStatus"
              class="rounded-full border px-2 py-0.5 text-[11px]"
              :class="statusClass(record.connStatus)"
              :data-status="record.connStatus"
            >
              {{ statusLabel(record.connStatus) }}
            </span>
            <span v-if="record.required" class="rounded-full border border-[var(--forebrain-brand-1)] px-2 py-0.5 text-[11px] text-[var(--forebrain-brand-1)]">
              {{ t('mcp.required') }}
            </span>
            <span
              v-if="authLabel(record.authStatus)"
              class="rounded-full border px-2 py-0.5 text-[11px]"
              :class="record.authStatus === 'needs-auth' || record.authStatus === 'failed' ? 'border-[rgba(200,150,60,0.5)] text-[rgba(200,150,60,1)]' : 'border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]'"
            >
              {{ authLabel(record.authStatus) }}
            </span>
            <span
              v-if="nextSessionLabel(record)"
              class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[11px] text-[var(--forebrain-muted-text)]"
            >
              {{ nextSessionLabel(record) }}
            </span>
          </div>

          <p
            v-if="record.error"
            class="mt-2 whitespace-pre-wrap rounded-lg border border-[rgba(160,70,70,0.36)] bg-[rgba(160,70,70,0.06)] px-3 py-2 font-mono text-[11px] leading-relaxed text-[var(--forebrain-danger)]"
            data-field="mcp-error"
          >
            <span class="not-italic">{{ t('mcp.errorLabel') }}: </span>{{ record.error }}
          </p>
          <details v-if="record.tools?.length" class="mt-2 rounded-lg border border-[var(--forebrain-divider)] px-3 py-2">
            <summary class="cursor-pointer text-[12px] text-[var(--forebrain-text-2)]">
              {{ t('mcp.toolsSummary', { count: record.tools.length }) }}
            </summary>
            <div class="mt-2 space-y-2">
              <details v-for="tool in record.tools" :key="tool.name" class="text-[12px]">
                <summary class="cursor-pointer font-mono text-[var(--forebrain-text)]">{{ tool.name }}</summary>
                <p v-if="tool.description" class="mt-1 whitespace-pre-wrap text-[var(--forebrain-text-2)]">{{ tool.description }}</p>
                <ul v-if="tool.params?.length" class="mt-1 space-y-0.5">
                  <li
                    v-for="row in flattenParams(tool.params)"
                    :key="row.key"
                    :style="{ paddingLeft: `${row.depth}rem` }"
                    class="text-[var(--forebrain-text-2)]"
                  >
                    <span class="font-mono text-[var(--forebrain-text)]">{{ row.param.name }}</span>
                    <span v-if="paramFacts(row.param)" class="ml-1">{{ paramFacts(row.param) }}</span>
                    <span v-if="row.param.description" class="ml-1 text-[var(--forebrain-muted-text)]">— {{ row.param.description }}</span>
                  </li>
                </ul>
                <p v-else class="mt-1 text-[var(--forebrain-muted-text)]">{{ t('mcp.noParams') }}</p>
              </details>
            </div>
          </details>
          <div class="mt-2">
            <span v-if="record.required" class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('mcp.requiredNoDisable') }}</span>
            <button
              v-else
              type="button"
              class="forebrain-btn forebrain-btn-ghost text-[11px]"
              :disabled="toggling === record.name"
              @click="toggleDisabled(record)"
            >
              {{ record.disabledNextSession ? t('mcp.enableNext') : t('mcp.disableNext') }}
            </button>
          </div>
          <dl class="mt-2 grid grid-cols-1 gap-1 text-[12px] text-[var(--forebrain-text-2)] sm:grid-cols-3">
            <div class="flex items-center gap-1.5">
              <span class="forebrain-mcp-dot" :class="record.urlSet ? 'is-on' : 'is-off'" />
              <span>{{ t('mcp.endpoint') }}: {{ record.urlSet ? t('common.yes') : t('common.no') }}</span>
            </div>
            <div class="flex items-center gap-1.5">
              <span class="forebrain-mcp-dot" :class="record.oauthConfigured ? 'is-on' : 'is-off'" />
              <span>{{ t('mcp.oauth') }}: {{ record.oauthConfigured ? t('common.yes') : t('common.no') }}</span>
            </div>
            <div class="flex items-center gap-1.5">
              <span class="forebrain-mcp-dot" :class="record.oauthOverlay ? 'is-on' : 'is-off'" />
              <span>{{ t('mcp.overlay') }}: {{ record.oauthOverlay ? t('common.yes') : t('common.no') }}</span>
            </div>
            <div v-if="typeof record.toolCount === 'number'" class="flex items-center gap-1.5">
              <span class="forebrain-mcp-dot is-on" />
              <span>{{ t('mcp.toolCount') }}: {{ record.toolCount }}</span>
            </div>
          </dl>
        </li>
      </ul>
      <p v-else class="rounded-xl border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
        {{ t('mcp.empty') }}
      </p>

      <section
        v-if="projectStatus && (projectStatus.overriddenGlobal?.length || projectStatus.notApplied?.length || projectStatus.pendingReload)"
        class="mt-5 rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4"
      >
        <h2 class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('mcp.projectSection') }}</h2>
        <p v-if="projectStatus.pendingReload" class="mt-2 text-[12px] text-[var(--forebrain-text-2)]">{{ t('mcp.pendingReload') }}</p>
        <p v-if="projectStatus.overriddenGlobal?.length" class="mt-2 text-[12px] text-[var(--forebrain-text-2)]">
          {{ t('projects.mcpOverridden', { names: projectStatus.overriddenGlobal.join(', ') }) }}
        </p>
        <div v-if="projectStatus.notApplied?.length" class="mt-2">
          <p class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('projects.mcpNotAppliedTitle') }}</p>
          <ul class="mt-1 space-y-0.5">
            <li v-for="row in projectStatus.notApplied" :key="row.name" class="text-[11px] text-[var(--forebrain-text-2)]">
              <span class="font-mono">{{ row.name || '(' + t('projects.unnamed') + ')' }}</span>
              <span class="ml-1 text-[var(--forebrain-muted-text)]">— {{ row.reason }}</span>
            </li>
          </ul>
        </div>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The MCP servers this session would run, with the two things that decide
 * whether one actually connects — an endpoint and credentials — and the
 * scope each entry came from. Project entries that exist but are not in the
 * running list say why, in one line.
 */
import { onMounted, ref } from 'vue'
import {
  getErrorMessage,
  mcpSetServerDisabled,
  forebrainApi,
  type McpConnStatus,
  type McpProjectStatus,
  type McpServerRecord,
  type McpToolParam,
} from '@/lib/api'
import { lastSessionIdValue } from '@/composables/useAuth'
import { useI18n } from '@/locales'

const { t } = useI18n()
const records = ref<McpServerRecord[]>([])
const projectStatus = ref<McpProjectStatus | null>(null)
const loading = ref(false)
const error = ref('')
const toggling = ref('')

/** toggleDisabled records the next-session choice, then re-reads the list so
 *  the row shows what the gateway recorded. This session keeps running exactly
 *  as it is — the frozen list and the prompt prefix never move. */
async function toggleDisabled(record: McpServerRecord) {
  toggling.value = record.name
  try {
    await mcpSetServerDisabled(record.name, !record.disabledNextSession, lastSessionIdValue() ?? undefined)
    await load()
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    toggling.value = ''
  }
}

/** authLabel names the credential state; a server without OAuth shows none. */
function authLabel(status?: string): string {
  switch (status) {
    case 'needs-auth': return t('mcp.authNeedsAuth')
    case 'authenticated': return t('mcp.authAuthenticated')
    case 'configured': return t('mcp.authConfigured')
    case 'failed': return t('mcp.authFailed')
    default: return ''
  }
}

/** nextSessionLabel says what a new session will do differently, if anything. */
function nextSessionLabel(record: McpServerRecord): string {
  if (record.running !== false && record.disabledNextSession) return t('mcp.disabledNextSession')
  if (record.running === false && !record.disabledNextSession) return t('mcp.enabledNextSession')
  return ''
}

/** flattenParams lays a parameter table out as rows, nested properties
 *  indented under their parent. */
function flattenParams(params: McpToolParam[], depth = 0, prefix = ''): Array<{ key: string; depth: number; param: McpToolParam }> {
  const rows: Array<{ key: string; depth: number; param: McpToolParam }> = []
  for (const param of params) {
    const key = `${prefix}${param.name}`
    rows.push({ key, depth, param })
    if (param.children?.length) rows.push(...flattenParams(param.children, depth + 1, `${key}.`))
  }
  return rows
}

/** paramFacts is the type, required flag, enum values and default of one row. */
function paramFacts(param: McpToolParam): string {
  const facts: string[] = []
  if (param.type) facts.push(param.type)
  if (param.required) facts.push(t('mcp.paramRequired'))
  if (param.enum?.length) facts.push(t('mcp.paramOneOf', { values: param.enum.join(' | ') }))
  if (param.default) facts.push(t('mcp.paramDefault', { value: param.default }))
  return facts.join(' · ')
}
// Whether the gateway had a Runner to ask. Without one the list is
// configuration only, and saying so is what keeps a missing status from
// reading as "connected".
const runtimeAvailable = ref(false)

/**
 * statusLabel names the startup state a server is in.
 *
 * An unknown state is not rendered as one of the four: a client that meets a
 * state it does not know renders the raw value, so a server-side addition shows
 * up as itself instead of being silently mapped onto whichever state happens to
 * be the fallback.
 */
function statusLabel(status: McpConnStatus | string): string {
  switch (status) {
    case 'connecting': return t('mcp.statusConnecting')
    case 'connected': return t('mcp.statusConnected')
    case 'error': return t('mcp.statusError')
    case 'cancelled': return t('mcp.statusCancelled')
    case 'disconnected': return t('mcp.statusDisconnected')
    case 'unknown': return t('mcp.statusUnknown')
    default: return String(status)
  }
}

function statusClass(status: McpConnStatus | string): string {
  switch (status) {
    case 'connected':
      return 'border-[var(--forebrain-brand-1)] text-[var(--forebrain-brand-1)]'
    case 'connecting':
      return 'border-[var(--forebrain-divider)] text-[var(--forebrain-text-2)]'
    case 'error':
      return 'border-[rgba(160,70,70,0.5)] text-[var(--forebrain-danger)]'
    case 'cancelled':
    case 'disconnected':
      // An idle release looks like a skip did: quiet, present, nothing wrong.
      return 'border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]'
    default:
      return 'border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]'
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    // The session the browser is on decides which Runner answers: a project
    // session runs on its own, and reporting the primary agent's servers there
    // would describe a generation this conversation never started.
    const sessionId = lastSessionIdValue()
    const { servers, project, runtimeAvailable: hasRuntime } = await forebrainApi.mcpServersWithStatus(
      sessionId ? { sessionId } : undefined,
    )
    records.value = servers
    projectStatus.value = project
    runtimeAvailable.value = hasRuntime
  } catch (e) {
    error.value = getErrorMessage(e)
    records.value = []
    runtimeAvailable.value = false
    projectStatus.value = null
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.forebrain-mcp-dot {
  height: 0.375rem;
  width: 0.375rem;
  flex: none;
  border-radius: 999px;
}

.forebrain-mcp-dot.is-on {
  background: var(--forebrain-brand-1);
}

.forebrain-mcp-dot.is-off {
  background: var(--forebrain-muted-text);
  opacity: 0.5;
}
</style>
