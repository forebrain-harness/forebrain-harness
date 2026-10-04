<template>
  <div class="pb-6">
    <div class="mx-auto w-full max-w-3xl">
      <header class="mb-5 flex items-end justify-between gap-4">
        <div>
          <h1 class="text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">{{ t('lsp.title') }}</h1>
          <p class="mt-1 text-[13px] leading-relaxed text-[var(--forebrain-text-2)]">{{ t('lsp.description') }}</p>
        </div>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
          {{ t('common.refresh') }}
        </button>
      </header>

      <p v-if="error" class="mb-4 rounded-xl border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <p
        v-if="snapshot"
        class="mb-4 rounded-xl border px-4 py-3 text-[12px] leading-relaxed"
        :class="projectLineClass"
        data-testid="lsp-project-line"
      >
        {{ projectLine }}
      </p>
      <p v-if="snapshot && !snapshot.featureEnabled" class="mb-4 text-[12px] text-[var(--forebrain-muted-text)]" data-testid="lsp-feature-off">
        {{ t('lsp.featureOff') }}
      </p>
      <p class="mb-4 text-[12px] leading-relaxed text-[var(--forebrain-muted-text)]">{{ t('lsp.hostNote') }}</p>
      <p v-for="note in snapshot?.projectNotes ?? []" :key="note" class="mb-1 text-[11px] text-[var(--forebrain-muted-text)]">
        {{ t('lsp.projectFile') }}: {{ note }}
      </p>

      <div v-if="loading && !snapshot" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <ul v-else-if="servers.length" class="space-y-2">
        <li
          v-for="server in servers"
          :key="server.id"
          class="rounded-xl border px-4 py-3"
          :class="server.scope === 'project' ? 'border-[var(--forebrain-brand-border-strong)] bg-[var(--forebrain-surface)]' : 'border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)]'"
          :data-server="server.id"
        >
          <div class="flex flex-wrap items-center gap-2">
            <span class="font-mono text-[13px] text-[var(--forebrain-text)]">{{ server.id }}</span>
            <span v-if="server.languages?.length" class="text-[11px] text-[var(--forebrain-text-2)]">{{ server.languages.join(', ') }}</span>
            <span class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[10px] text-[var(--forebrain-muted-text)]">
              {{ scopeLabel(server.scope) }}
            </span>
            <span
              class="rounded-full border px-2 py-0.5 text-[11px]"
              :class="stateClass(server.state, server.installing)"
              :data-state="server.installing ? 'installing' : server.state"
            >
              {{ stateLabel(server) }}
            </span>
          </div>

          <dl class="mt-2 grid grid-cols-1 gap-1 text-[12px] text-[var(--forebrain-text-2)] sm:grid-cols-2">
            <div v-if="server.binaryPath">
              <dt class="inline text-[var(--forebrain-muted-text)]">{{ t('lsp.binary') }}: </dt>
              <dd class="ml-1 inline font-mono">{{ server.binaryPath }}<span v-if="server.version"> · {{ server.version }}</span></dd>
            </div>
            <div v-if="problemLine(server)">
              <dt class="inline text-[var(--forebrain-muted-text)]">{{ t('lsp.problems') }}: </dt>
              <dd class="ml-1 inline">{{ problemLine(server) }}</dd>
            </div>
            <div v-if="server.logPath" class="min-w-0">
              <dt class="inline text-[var(--forebrain-muted-text)]">{{ t('lsp.log') }}: </dt>
              <dd class="ml-1 inline font-mono break-all">{{ server.logPath }}</dd>
              <button type="button" class="ml-1 text-[11px] text-[var(--forebrain-brand-1)]" :data-copy="server.id" @click="copyLog(server)">
                {{ copiedLog === server.id ? t('lsp.copied') : t('lsp.copy') }}
              </button>
            </div>
            <div v-if="server.projectWrites?.length">
              <dt class="inline text-[var(--forebrain-muted-text)]">{{ t('lsp.projectWrites') }}: </dt>
              <dd class="ml-1 inline font-mono">{{ server.projectWrites.join(', ') }}</dd>
            </div>
          </dl>

          <p
            v-if="server.lastError"
            class="mt-2 whitespace-pre-wrap rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-3 py-2 font-mono text-[11px] leading-relaxed text-[var(--forebrain-danger)]"
            data-field="lsp-error"
          >
            <span class="not-italic">{{ t('lsp.lastError') }}: </span>{{ server.lastError }}
          </p>

          <div class="mt-2 flex flex-wrap items-center gap-2">
            <button
              v-if="!server.enabled && server.state !== 'blocked'"
              type="button"
              class="forebrain-btn forebrain-btn-ghost text-[11px]"
              :disabled="busy === server.id"
              :data-enable="server.id"
              @click="enable(server)"
            >
              {{ t('lsp.enable') }}
            </button>
            <button
              v-if="server.enabled"
              type="button"
              class="forebrain-btn forebrain-btn-ghost text-[11px]"
              :disabled="busy === server.id"
              :data-disable="server.id"
              @click="disable(server)"
            >
              {{ t('lsp.disable') }}
            </button>
            <button
              v-if="server.enabled && ['ready', 'indexing', 'starting', 'failed'].includes(server.state)"
              type="button"
              class="forebrain-btn forebrain-btn-ghost text-[11px]"
              :disabled="busy === server.id"
              :data-restart="server.id"
              @click="restart(server)"
            >
              {{ t('lsp.restart') }}
            </button>
            <button
              v-if="server.state === 'not_installed' && server.installCommand && !server.installing"
              type="button"
              class="forebrain-btn forebrain-btn-ghost text-[11px]"
              :data-install="server.id"
              @click="confirming = server.id"
            >
              {{ t('lsp.install') }}
            </button>
          </div>

          <div
            v-if="confirming === server.id"
            class="mt-2 rounded-lg border border-[var(--forebrain-warning)] bg-[var(--forebrain-bg-alt)] px-3 py-2"
            data-testid="lsp-install-confirm"
          >
            <p class="text-[12px] text-[var(--forebrain-text-2)]">{{ t('lsp.installConfirm') }}</p>
            <p class="mt-1 break-all rounded bg-[var(--forebrain-surface)] px-2 py-1 font-mono text-[12px] text-[var(--forebrain-text)]">{{ server.installCommand }}</p>
            <p class="mt-1 text-[11px] text-[var(--forebrain-muted-text)]">{{ t('lsp.installHostNote') }}</p>
            <div class="mt-2 flex gap-2">
              <button
                type="button"
                class="forebrain-btn forebrain-btn-ghost text-[11px]"
                :data-install-run="server.id"
                @click="install(server)"
              >
                {{ t('lsp.installRun') }}
              </button>
              <button
                type="button"
                class="forebrain-btn forebrain-btn-ghost text-[11px]"
                :data-install-cancel="server.id"
                @click="confirming = ''"
              >
                {{ t('common.cancel') }}
              </button>
            </div>
          </div>

          <div v-if="server.installing || server.installError || server.installLog?.length" class="mt-2">
            <p class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('lsp.installOutput') }}</p>
            <pre
              class="mt-1 max-h-40 overflow-auto whitespace-pre-wrap rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] px-3 py-2 font-mono text-[11px] leading-relaxed text-[var(--forebrain-text-2)]"
              data-testid="lsp-install-log"
            >{{ installOutput(server) }}</pre>
          </div>
        </li>
      </ul>
      <p v-else class="rounded-xl border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
        {{ t('lsp.empty') }}
      </p>

      <section
        v-if="snapshot?.recommendationsDisabled"
        class="mt-5 rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4"
        data-testid="lsp-recommendations"
      >
        <p class="text-[12px] text-[var(--forebrain-text-2)]">{{ t('lsp.recommendationsOff', { reason: snapshot.recommendationsDisabledReason || '' }) }}</p>
        <button type="button" class="forebrain-btn forebrain-btn-ghost mt-2 text-[11px]" :disabled="busy === 'recommendations'" @click="resetRecommendations">
          {{ t('lsp.recommendationsEnable') }}
        </button>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The language servers this session's runner has: what runs where, the
 * per-server switches, and the install that only starts after the user has
 * seen the whole command. Every action goes through the same control plane
 * the terminal's /lsp panel uses, so the two surfaces cannot disagree.
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  getErrorMessage,
  lspInstall,
  lspResetRecommendations,
  lspRestart,
  lspSetEnabled,
  lspSnapshot,
  type LspServerStatus,
  type LspSnapshot,
} from '@/lib/api'
import { lastSessionIdValue } from '@/composables/useLastSession'
import { useI18n } from '@/locales'

const { t } = useI18n()
const snapshot = ref<LspSnapshot | null>(null)
const loading = ref(false)
const error = ref('')
const busy = ref('')
const confirming = ref('')
const copiedLog = ref('')
let pollTimer: ReturnType<typeof setTimeout> | null = null

const servers = computed(() => snapshot.value?.servers ?? [])

/**
 * The session the browser is on decides which Runner answers — the same rule
 * the MCP tab follows, so a project session never reports another
 * generation's servers.
 */
function sessionId(): string | undefined {
  const sid = lastSessionIdValue()
  return sid ? sid : undefined
}

const projectLine = computed(() => {
  const snap = snapshot.value
  if (!snap) return ''
  if (!snap.projectRoot) return t('lsp.noProject')
  return snap.trusted
    ? t('lsp.projectTrusted', { root: snap.projectRoot })
    : t('lsp.projectNotTrusted', { root: snap.projectRoot })
})

const projectLineClass = computed(() => {
  const snap = snapshot.value
  if (!snap?.projectRoot) return 'border-[var(--forebrain-divider)] text-[var(--forebrain-text-2)]'
  return snap.trusted
    ? 'border-[var(--forebrain-divider)] text-[var(--forebrain-text-2)]'
    : 'border-[var(--forebrain-warning)] text-[var(--forebrain-warning)]'
})

/** stateLabel names the state a server is in, percent included while indexing. */
function stateLabel(server: LspServerStatus): string {
  if (server.installing) return t('lsp.stateInstalling')
  switch (server.state) {
    case 'ready': return t('lsp.stateReady')
    case 'indexing': return server.indexingPercent ? t('lsp.stateIndexingPercent', { percent: server.indexingPercent }) : t('lsp.stateIndexing')
    case 'starting': return t('lsp.stateStarting')
    case 'failed': return t('lsp.stateFailed')
    case 'stopped': return t('lsp.stateStopped')
    case 'available': return t('lsp.stateAvailable')
    case 'not_installed': return t('lsp.stateNotInstalled')
    case 'blocked': return t('lsp.stateBlocked')
    default: return String(server.state)
  }
}

/** stateClass colours the badge the way the MCP tab colours its statuses. */
function stateClass(state: string, installing?: boolean): string {
  if (installing) return 'border-[var(--forebrain-divider)] text-[var(--forebrain-text-2)]'
  switch (state) {
    case 'ready':
      return 'border-[var(--forebrain-brand-1)] text-[var(--forebrain-brand-1)]'
    case 'starting':
    case 'indexing':
      return 'border-[var(--forebrain-divider)] text-[var(--forebrain-text-2)]'
    case 'failed':
      return 'border-[var(--forebrain-danger)] text-[var(--forebrain-danger)]'
    case 'blocked':
      return 'border-[var(--forebrain-warning)] text-[var(--forebrain-warning)]'
    default:
      return 'border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]'
  }
}

function scopeLabel(scope: string): string {
  if (scope === 'project') return t('lsp.scopeProject')
  if (scope === 'global') return t('lsp.scopeGlobal')
  return t('lsp.scopeCatalog')
}

/** problemLine names the errors and warnings of an enabled server, each side only when it counts. */
function problemLine(server: LspServerStatus): string {
  if (!server.enabled) return ''
  const parts: string[] = []
  if (server.errors) parts.push(t('lsp.errorCount', { count: server.errors }))
  if (server.warnings) parts.push(t('lsp.warningCount', { count: server.warnings }))
  return parts.join(' · ')
}

/** installOutput is the install's tail with its error's first line in front. */
function installOutput(server: LspServerStatus): string {
  const lines: string[] = []
  if (server.installError) lines.push(server.installError.split('\n')[0])
  lines.push(...(server.installLog ?? []))
  return lines.join('\n')
}

async function copyLog(server: LspServerStatus) {
  try {
    await navigator.clipboard.writeText(server.logPath ?? '')
    copiedLog.value = server.id
    setTimeout(() => {
      if (copiedLog.value === server.id) copiedLog.value = ''
    }, 2000)
  } catch {
    // Copying is best effort: the path stays on screen either way.
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    snapshot.value = await lspSnapshot(sessionId())
  } catch (e) {
    error.value = getErrorMessage(e)
    snapshot.value = null
  } finally {
    loading.value = false
  }
  schedulePoll()
}

/**
 * schedulePoll refreshes every 2s while any server is installing and stops
 * the moment none is, so a finished install settles without a timer running.
 */
function schedulePoll() {
  stopPoll()
  if (!snapshot.value?.servers.some((server) => server.installing)) return
  pollTimer = setTimeout(async () => {
    try {
      snapshot.value = await lspSnapshot(sessionId())
    } catch {
      // The next tick retries; a transient failure must not cancel polling.
    }
    schedulePoll()
  }, 2000)
}

function stopPoll() {
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
}

async function run(key: string, action: () => Promise<unknown>) {
  busy.value = key
  try {
    await action()
    await load()
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    busy.value = ''
  }
}

async function enable(server: LspServerStatus) {
  await run(server.id, () => lspSetEnabled(server.id, true, sessionId()))
}

async function disable(server: LspServerStatus) {
  await run(server.id, () => lspSetEnabled(server.id, false, sessionId()))
}

async function restart(server: LspServerStatus) {
  await run(server.id, () => lspRestart(server.id, sessionId()))
}

/** install runs only from the confirm dialog; nothing calls it directly. */
async function install(server: LspServerStatus) {
  confirming.value = ''
  await run(server.id, () => lspInstall(server.id, sessionId()))
}

async function resetRecommendations() {
  await run('recommendations', () => lspResetRecommendations(sessionId()))
}

onMounted(load)
onBeforeUnmount(stopPoll)
</script>
