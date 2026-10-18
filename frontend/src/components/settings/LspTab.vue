<template>
  <div class="flex flex-col gap-4 pb-6">
    <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]" data-testid="lsp-error">{{ error }}</p>

    <ListToolbar>
      <template #left>
        <input
          v-model="q"
          type="search"
          :placeholder="t('lsp.searchPlaceholder')"
          class="forebrain-field h-[34px]"
          style="max-width: 240px"
          data-testid="lsp-search"
        />
      </template>
      <template #actions>
        <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" :disabled="loading" data-testid="lsp-refresh" @click="reload">
          <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
        </button>
      </template>
    </ListToolbar>

    <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div
      v-else-if="!rows.length"
      class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]"
      data-testid="lsp-empty"
    >
      {{ t('lsp.empty') }}
    </div>
    <div v-else class="forebrain-table-wrap">
      <table class="forebrain-table forebrain-table--fixed">
        <colgroup>
          <col />
          <col style="width: 84px" />
          <col style="width: 120px" />
          <col />
          <col style="width: 112px" />
        </colgroup>
        <thead>
          <tr>
            <th>{{ t('lsp.colServer') }}</th>
            <th>{{ t('lsp.colScope') }}</th>
            <th>{{ t('lsp.colState') }}</th>
            <th>{{ t('lsp.colProgram') }}</th>
            <th class="text-right">{{ t('lsp.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="server in rows" :key="server.id" :data-server="server.id">
            <td>
              <div class="flex min-w-0 flex-col">
                <span class="font-mono text-[12.5px] text-[var(--forebrain-text)]">{{ server.id }}</span>
                <span v-if="server.languages?.length" class="text-[11px] text-[var(--forebrain-muted-text)]">{{ server.languages.join(', ') }}</span>
              </div>
            </td>
            <td>
              <ScopeBadge :type="scopeType(server.scope)" :label="scopeLabel(server.scope)" />
            </td>
            <td>
              <!-- One badge per server: what it is doing now, and one hover
                   away the problems and the last error it hit. -->
              <BadgeComponent
                :variant="stateVariant(server)"
                :data-state="server.installing ? 'installing' : server.state"
                v-tip:top="statusTip(server)"
              >
                {{ stateLabel(server) }}
              </BadgeComponent>
            </td>
            <td>
              <span
                v-if="server.binaryPath"
                class="forebrain-cell-1 font-mono text-[var(--forebrain-text-2)]"
                v-tip:top.trunc="programText(server)"
              >
                {{ programText(server) }}
              </span>
              <span v-else class="text-[var(--forebrain-muted-text)]">—</span>
            </td>
            <td class="text-right">
              <RowActions>
                <RowAction
                  v-if="!server.enabled && server.state !== 'blocked'"
                  :icon="Power"
                  :label="t('lsp.enable')"
                  :name="server.id"
                  :disabled="busy === server.id"
                  :data-enable="server.id"
                  @click="enable(server)"
                />
                <RowAction
                  v-else-if="server.enabled"
                  :icon="PowerOff"
                  :label="t('lsp.disable')"
                  :name="server.id"
                  :disabled="busy === server.id"
                  :data-disable="server.id"
                  @click="disable(server)"
                />
                <RowActionSlot v-else />
                <RowAction
                  v-if="server.enabled && restartable.includes(server.state)"
                  :icon="RotateCw"
                  :label="t('lsp.restart')"
                  :name="server.id"
                  :disabled="busy === server.id"
                  :data-restart="server.id"
                  @click="restart(server)"
                />
                <RowActionSlot v-else />
                <RowAction
                  v-if="server.state === 'not_installed' && server.installCommand && !server.installing"
                  :icon="Download"
                  :label="t('lsp.install')"
                  :name="server.id"
                  :data-install="server.id"
                  @click="confirming = server"
                />
                <RowActionSlot v-else />
                <RowAction
                  :icon="Eye"
                  :label="t('lsp.details')"
                  :name="server.id"
                  :data-detail="server.id"
                  @click="detailId = server.id"
                />
              </RowActions>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />

    <!-- Everything the table has no column for: the page's own description,
         where the servers run, the project they serve, and the notes the
         project files raised. -->
    <ListNote data-testid="lsp-note">
      {{ t('lsp.description') }}
      <span class="mt-1 block">{{ t('lsp.hostNote') }}</span>
      <span v-if="extras" class="mt-1 block" :class="projectLineClass" data-testid="lsp-project-line">{{ projectLine }}</span>
      <span v-if="extras && !extras.featureEnabled" class="mt-1 block" data-testid="lsp-feature-off">{{ t('lsp.featureOff') }}</span>
      <span v-for="note in extras?.projectNotes ?? []" :key="note" class="mt-1 block">{{ t('lsp.projectFile') }}: {{ note }}</span>
      <span v-if="extras?.recommendationsDisabled" class="mt-1 block" data-testid="lsp-recommendations">
        {{ t('lsp.recommendationsOff', { reason: extras.recommendationsDisabledReason || '' }) }}
        <button
          type="button"
          class="text-[var(--forebrain-brand-1)] underline"
          :disabled="busy === 'recommendations'"
          @click="resetRecommendations"
        >
          {{ t('lsp.recommendationsEnable') }}
        </button>
      </span>
    </ListNote>

    <!-- One server, read-only: the facts the table cannot hold, and the copy
         button for the path a reader would otherwise retype. -->
    <ForebrainDrawer v-if="detail" :open="true" :title="detail.id" readonly @close="detailId = ''">
      <dl class="forebrain-kv">
        <dt>{{ t('lsp.binary') }}</dt>
        <dd class="break-all font-mono">{{ detail.binaryPath || '—' }}</dd>
        <dt>{{ t('lsp.versionLabel') }}</dt>
        <dd class="font-mono">{{ detail.version || '—' }}</dd>
        <dt>{{ t('lsp.problems') }}</dt>
        <dd>{{ problemLine(detail) || '—' }}</dd>
        <dt>{{ t('lsp.log') }}</dt>
        <dd class="break-all font-mono">
          {{ detail.logPath || '—' }}
          <button
            v-if="detail.logPath"
            type="button"
            class="ml-1 text-[var(--forebrain-brand-1)]"
            :data-copy="detail.id"
            @click="copyLog(detail)"
          >
            {{ copiedLog === detail.id ? t('lsp.copied') : t('lsp.copy') }}
          </button>
        </dd>
        <dt>{{ t('lsp.projectWrites') }}</dt>
        <dd class="break-all font-mono">{{ detail.projectWrites?.join(', ') || '—' }}</dd>
        <dt>{{ t('lsp.lastError') }}</dt>
        <dd class="whitespace-pre-wrap font-mono text-[var(--forebrain-danger)]" data-field="lsp-error">{{ detail.lastError || '—' }}</dd>
      </dl>
      <template v-if="detail.installing || detail.installError || detail.installLog?.length">
        <p class="mt-3 text-[12px] font-medium text-[var(--forebrain-text)]">{{ t('lsp.installOutput') }}</p>
        <pre
          class="mt-1 max-h-40 overflow-auto whitespace-pre-wrap rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] px-3 py-2 font-mono text-[11px] leading-relaxed text-[var(--forebrain-text-2)]"
          data-testid="lsp-install-log"
        >{{ installOutput(detail) }}</pre>
      </template>
    </ForebrainDrawer>

    <!-- The install never starts from the table: the whole command is shown
         first, and only its own button runs it. -->
    <ForebrainModal :open="!!confirming" size="sm" :title="t('lsp.install')" @close="confirming = null">
      <div data-testid="lsp-install-confirm">
        <p class="text-[12px] text-[var(--forebrain-text-2)]">{{ t('lsp.installConfirm') }}</p>
        <p class="mt-1 break-all rounded bg-[var(--forebrain-surface)] px-2 py-1 font-mono text-[12px] text-[var(--forebrain-text)]">{{ confirming?.installCommand }}</p>
        <p class="mt-1 text-[11px] text-[var(--forebrain-muted-text)]">{{ t('lsp.installHostNote') }}</p>
      </div>
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :data-install-cancel="confirming?.id" @click="confirming = null">
          {{ t('common.cancel') }}
        </button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :data-install-run="confirming?.id" @click="install()">
          {{ t('lsp.installRun') }}
        </button>
      </template>
    </ForebrainModal>
  </div>
</template>

<script setup lang="ts">
/**
 * The language servers this session's runner has: what runs where, the
 * per-server switches, and the install that only starts after the user has
 * seen the whole command. Every action goes through the same control plane
 * the terminal's /lsp panel uses, so the two surfaces cannot disagree.
 *
 * The table is the server's (plan 076): the search narrows it there over the
 * id and the languages, the pager says how many servers there are, and each
 * row offers its actions as icons — the words they stand for are the tip on
 * hover. What a row cannot hold (the binary, the problems, the log path with
 * its copy button, the last error) opens in a read-only drawer beside it.
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { Download, Eye, Power, PowerOff, RotateCw } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ForebrainDrawer from '@/components/common/ForebrainDrawer.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActionSlot from '@/components/common/RowActionSlot.vue'
import RowActions from '@/components/common/RowActions.vue'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import TablePager from '@/components/common/TablePager.vue'
import { useServerList } from '@/composables/useServerList'
import { lastSessionIdValue } from '@/composables/useLastSession'
import {
  getErrorMessage,
  lspInstall,
  lspPage,
  lspResetRecommendations,
  lspRestart,
  lspSetEnabled,
  type ListQuery,
  type LspServerStatus,
  type LspSnapshot,
} from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()

/** The states after which a running server can be restarted. */
const restartable = ['ready', 'indexing', 'starting', 'failed']

/**
 * The session the browser is on decides which Runner answers — the same rule
 * the MCP tab follows, so a project session never reports another
 * generation's servers.
 */
function sessionId(): string | undefined {
  const sid = lastSessionIdValue()
  return sid ? sid : undefined
}

/** The snapshot's own fields; the page's rows arrive beside them. */
const extras = ref<Omit<LspSnapshot, 'servers'> | null>(null)

async function fetchServers(query: ListQuery) {
  const answer = await lspPage({ ...query, sessionId: sessionId() })
  extras.value = answer
  // While an install runs the tab refreshes itself every 2s and stops the
  // moment none does, so a finished install settles without a timer running.
  schedulePoll(answer.rows)
  return answer
}

const {
  rows,
  total,
  page,
  pageSize,
  q,
  loading,
  error: listError,
  reload,
} = useServerList<LspServerStatus>(fetchServers, { pageSize: 10 })

const actionError = ref('')
const error = computed(() => listError.value || actionError.value)
const busy = ref('')
const confirming = ref<LspServerStatus | null>(null)
const detailId = ref('')
const copiedLog = ref('')

/** The server the drawer shows, kept current by every page load. */
const detail = computed<LspServerStatus | null>(() => rows.value.find((server) => server.id === detailId.value) ?? null)

const projectLine = computed(() => {
  const snap = extras.value
  if (!snap) return ''
  if (!snap.projectRoot) return t('lsp.noProject')
  return snap.trusted
    ? t('lsp.projectTrusted', { root: snap.projectRoot })
    : t('lsp.projectNotTrusted', { root: snap.projectRoot })
})

const projectLineClass = computed(() => {
  const snap = extras.value
  if (!snap?.projectRoot) return ''
  return snap.trusted ? '' : 'text-[var(--forebrain-warning)]'
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

/** stateVariant colours the badge the way the MCP table colours its statuses. */
function stateVariant(server: LspServerStatus): 'success' | 'error' | 'warning' | 'gray' {
  if (server.installing) return 'gray'
  switch (server.state) {
    case 'ready': return 'success'
    case 'failed': return 'error'
    case 'blocked': return 'warning'
    default: return 'gray'
  }
}

/** The scope chip's kind: a server belongs to the project, the agent, or the catalog. */
function scopeType(scope: string): 'agent' | 'project' | 'global' {
  if (scope === 'project') return 'project'
  if (scope === 'global') return 'global'
  return 'agent'
}

function scopeLabel(scope: string): string {
  if (scope === 'project') return t('lsp.scopeProject')
  if (scope === 'global') return t('lsp.scopeGlobal')
  return t('lsp.scopeCatalog')
}

/** programText is the program column: the binary and the version it reported. */
function programText(server: LspServerStatus): string {
  if (!server.binaryPath) return ''
  return server.version ? `${server.binaryPath} · ${server.version}` : server.binaryPath
}

/** problemLine names the errors and warnings of an enabled server, each side only when it counts. */
function problemLine(server: LspServerStatus): string {
  if (!server.enabled) return ''
  const parts: string[] = []
  if (server.errors) parts.push(t('lsp.errorCount', { count: server.errors }))
  if (server.warnings) parts.push(t('lsp.warningCount', { count: server.warnings }))
  return parts.join(' · ')
}

/** statusTip is what the state badge says on hover: the counts, then the last error. */
function statusTip(server: LspServerStatus): string {
  return [problemLine(server), server.lastError].filter(Boolean).join(' · ')
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

async function run(key: string, action: () => Promise<unknown>) {
  busy.value = key
  actionError.value = ''
  try {
    await action()
    await reload()
  } catch (e) {
    actionError.value = getErrorMessage(e)
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
async function install() {
  const server = confirming.value
  confirming.value = null
  if (!server) return
  await run(server.id, () => lspInstall(server.id, sessionId()))
}

async function resetRecommendations() {
  await run('recommendations', () => lspResetRecommendations(sessionId()))
}

// While an install runs the page refreshes itself every 2s and stops the
// moment none does, so a finished install settles without a timer running.
let pollTimer: ReturnType<typeof setTimeout> | null = null

function stopPoll() {
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
}

function schedulePoll(servers: LspServerStatus[]) {
  stopPoll()
  if (!servers.some((server) => server.installing)) return
  pollTimer = setTimeout(() => {
    pollTimer = null
    void reload()
  }, 2000)
}

// The list loads itself from here on — a search or a page change asks the
// server; a mount is the one read the tab makes for it.
onMounted(() => {
  void reload()
})

onBeforeUnmount(stopPoll)
</script>
