<template>
  <div class="wv-app">
    <header class="wv-h" data-testid="webview-header">
      <span class="wv-h-title">{{ headerTitle }}</span>
      <!-- This conversation's own heartbeat, where the web's conversation
           header keeps it: the recurring instruction and when it next fires. -->
      <SessionHeartbeat v-if="sessionId" :session-id="sessionId" />
      <span class="grow" />
      <button
        type="button"
        class="wv-icon-btn"
        :aria-label="t('chatDrawer.newChat')"
        :title="t('chatDrawer.newChat')"
        data-testid="header-new-chat"
        @click="newChat"
      >
        <Plus class="size-3.5" aria-hidden="true" />
      </button>
      <button
        type="button"
        class="wv-icon-btn"
        :aria-label="t('chat.subagentDrawer')"
        :title="t('chat.subagentDrawer')"
        data-testid="header-subagents"
        @click="openSubagentDrawer"
      >
        <Bot class="size-3.5" aria-hidden="true" />
      </button>
      <button
        type="button"
        class="wv-icon-btn"
        :aria-label="t('chat.peerDrawerTitle')"
        :title="t('chat.peerDrawerTitle')"
        data-testid="header-peers"
        @click="openPeerDrawer"
      >
        <Users class="size-3.5" aria-hidden="true" />
      </button>
      <button
        type="button"
        class="wv-icon-btn"
        :aria-label="t('chat.workbench')"
        :title="t('chat.workbench')"
        data-testid="header-workbench"
        @click="workbench.toggle()"
      >
        <PanelRight class="size-3.5" aria-hidden="true" />
      </button>
    </header>
    <main class="wv-main">
      <!-- The new chat's own page: the same hero the prototype draws, the
           starters under the composer, the project pill in the composer. -->
      <div v-if="isFreshChat" class="wv-empty" data-testid="webview-empty-hero">
        <div>
          <span class="wv-empty-mark" aria-hidden="true"><Sparkles class="size-4" /></span>
          <h3>{{ t('chat.panelEmptyTitle') }}</h3>
          <p>{{ t('chat.panelEmptyBody', { project: target?.name || projectNameOfLast || t('chatDrawer.untitled') }) }}</p>
        </div>
      </div>
      <!-- The MCP servers that failed to start, named where the thread begins. -->
      <McpNotice v-if="mcpFailures.length" :failures="mcpFailures" />
      <ThreadView
        v-show="!isFreshChat"
        class="wv-thread-scroll"
        :messages="messages"
        :session-id="sessionId"
        :is-streaming="isStreaming"
        :error="error"
        :history-loading="historyLoading"
        :history-error="historyError"
        :scope="workbenchScope"
        :busy-notice="busyNotice"
        :view-mode="viewMode"
        :session-artifacts="sessionArtifacts"
        :subagent-call-tasks="subagentCallTasks"
        :now="subagentNow"
        :active-agent-id="drawerVisible ? shownActive : ''"
        @choose="pickFromNotice"
        @copy-message="copyMessage"
        @rewind="openRewind"
        @retry-history="retryHistory"
        @focus-composer="focusComposer"
        @open-deliverable="openDeliverable"
        @open-doc="openDocTab"
        @open-agent="openSubagentTab"
      />
    </main>
    <!-- The session's todo checklist, above the composer: the chip in the
         bar toggles it. -->
    <TodoPanel
      v-if="todosOpen && latestTodo"
      :todo="latestTodo"
      :unread="todoUnread"
      :flash="todoFlash"
      @read="markTodoRead"
      @read-all="markAllTodosRead"
      @close="todosOpen = false"
    />
    <!-- The plan approval takes the input's place while the planner waits:
         no card in the thread, the dock here, the composer underneath. -->
    <PlanApprovalDock
      v-if="exitPlanPending"
      :action="exitPlanPending"
      :session-id="sessionId ?? ''"
      :session-type="sessionInfo?.project?.projectType ?? ''"
      :on-open-plan="openPlanDoc"
    />
    <ComposerBar
      v-show="!exitPlanPending"
      ref="composerRef"
      :session-id="sessionId"
      :mode="mode"
      :is-streaming="isStreaming"
      :composer-locked="composerLocked"
      :placeholder="composerPlaceholder"
      :submit-label="submitLabel"
      :submit-status="submitStatus"
      :session-model-data="sessionModelData"
      :session-efforts="sessionEfforts"
      :session-default-effort="sessionDefaultEffort"
      :scope="workbenchScope"
      :suggestion="suggestion"
      :queue-visible="queueVisible"
      :pending-input-preview="pendingInputPreview"
      :available-bots="availableBots"
      :bots-fetch-done="botsFetchDone"
      :context-used-percent="contextUsedPercent"
      :composer-notice="composerNotice"
      :fresh-chat="isFreshChat"
      :projects="projects"
      :target="target"
      :pill-invalid="pillInvalid"
      :runtime-status="runtimeStatus"
      :auto-continue="autoContinueForView('')"
      :held-peer="heldPeer"
      :todo-count-text="todoCountText"
      :todo-total="todoTotal"
      :todo-unread="todoUnread.length"
      :todos-open="todosOpen"
      :compact="drawerVisible"
      v-model:view-mode="viewMode"
      @send="handleSubmit"
      @stop="cancel()"
      @select-mode="selectMode"
      @select-effort="selectEffort"
      @pick-model="pickDraftModel"
      @model-saved="loadSessionModel(sessionId ?? '')"
      @files-picked="onFilesPicked"
      @queue-follow-up="nextSubmissionDisposition = 'queue'"
      @edit-last-queued="restoreLastQueuedMessage"
      @interrupt-run="interruptAndSendPendingSteers"
      @typed="onComposerTyped"
      @escape="cancelAutoContinue('')"
      @suggestion-accepted="acceptSuggestion"
      @insert-plus="insertPlusCommand"
      @pick-target="pickTarget"
      @browse-folder="browseFolder"
      @toggle-todos="todosOpen = !todosOpen"
      @open-context="workbench.openOn('context')"
      @peer-release="releaseHeldPeer"
      @peer-deny="denyHeldPeer"
      @peer-settled="settleHeldPeer"
    />
    <!-- The subagents this conversation dispatched, beside the thread: one
         tab each, its own composer, its own queue. -->
    <SubagentDrawer
      v-if="drawerVisible"
      :tabs="drawerRecords"
      :active="shownActive"
      :records="conversationSubagents"
      :seen="seenAgentActivity"
      :now="subagentNow"
      :overlay="true"
      :doc-tabs="docTabs"
      :active-doc="docActive"
      :order="drawerOrder"
      @select="selectSubagentTab"
      @close-tab="closeSubagentTab"
      @open="openSubagentTab"
      @collapse="drawerOpen = false"
      @select-doc="selectDocTab"
      @close-doc="closeDocTab"
      @escape-key="docActive ? closeDocTab(docActive) : (drawerOpen = false)"
    >
      <!-- The documents a card head opened — a whole command, a whole title,
           a whole output — in the same drawer, beside the subagents. -->
      <template v-for="tab in docTabs">
        <ToolDocCommand
          v-if="tab.kind === 'command'"
          v-show="tab.id === docActive"
          :key="tab.id"
          :command="tab.command ?? tab.text"
        />
        <ToolDocOutput
          v-else-if="tab.kind === 'output' && outputSourceOf(tab)"
          v-show="tab.id === docActive"
          :key="tab.id"
          :source="outputSourceOf(tab)!"
        />
        <ToolDocTitle
          v-else-if="tab.kind === 'title'"
          v-show="tab.id === docActive"
          :key="tab.id"
          :text="tab.text"
        />
      </template>
      <SubagentTab
        v-for="record in drawerRecords"
        v-show="!docActive && record.agentId === shownActive"
        :key="record.agentId"
        :ref="(el: unknown) => setSubagentTabRef(record.agentId, el)"
        :record="record"
        :fold-state="agentFolds[record.agentId] ?? {}"
        :call-tasks="subagentCallTasks"
        :now="subagentNow"
        :scope="workbenchScope"
        :active-agent-id="shownActive"
        :pending-input="subagentPendingInput[record.agentId] ?? emptyPendingInputPreview()"
        :auto-continue="autoContinueForView(record.agentId)"
        :notice="subagentNotices[record.agentId] ?? null"
        @submit="onSubagentSubmit(record.agentId, $event)"
        @fold-change="onSubagentFold(record.agentId, $event)"
        @open-agent="openSubagentTab"
        @open-doc="openDocTab"
        @stop="stopSubagent(record.agentId)"
        @pause="pauseSubagent(record.agentId)"
        @edit-last-queued="recallIntoTab(record.agentId)"
        @interrupt-run="interruptSubagentToSend(record.agentId)"
        @typed="cancelAutoContinue(record.agentId)"
        @files-picked="(files: PickedFile[]) => onFilesPicked(files, record.agentId)"
        @dismiss-notice="subagentNotices[record.agentId] = null"
      />
    </SubagentDrawer>
    <!-- The peers this conversation knows, and the held letters waiting. -->
    <PeerDrawer
      v-if="peerDrawerOpen"
      :held="heldPeers"
      :files="peerDirectory.files"
      :same-tree="peerDirectory.sameTree"
      :other="peerDirectory.other"
      :other-total="peerDirectory.otherTotal"
      :page="peerDirectoryPage"
      :page-size="PEER_DIRECTORY_PAGE_SIZE"
      :now="subagentNow"
      :overlay="true"
      @close="peerDrawerOpen = false"
      @release="releaseHeldPeer"
      @deny="denyHeldPeer"
      @page-change="changePeerPage"
    />
    <!-- The workbench: what is alive and what it spent, the context report,
         the working directory's tree and the files this conversation changed.
         The sidebar is narrow, so it overlays the thread rather than narrowing
         it — the same overlay the web uses below its lg breakpoint. -->
    <ChatWorkbench
      v-if="workbench.open.value && !isFreshChat && !drawerVisible"
      :records="runningRoster"
      :loading="rosterLoading"
      :error="rosterError"
      :overlay="true"
      :scope="workbenchScope"
      :root-path="sessionInfo?.project?.root ?? ''"
      :requested-tab="workbench.requested.value"
      :hide-changes="notACodeConversation"
      @requested-tab-shown="workbench.consumed()"
      @close="workbench.close()"
      @view="viewRoster"
      @cancel="cancelRoster"
      @cancel-all="cancelAll"
      @insert-ref="insertWorkspaceRef"
      @open-plan="openPlanDoc"
    />
    <!-- The folder the next conversation runs in: the same browser the web
         opens from the pill, over the host's own directory listing. -->
    <FolderDialog
      v-if="folderOpen"
      :projects="projects"
      :initial-path="target?.root"
      @select="onFolderPick"
      @cancel="folderOpen = false"
    />
    <!-- An attached file is saved into the project before it is sent: the
         same "save to" dialog the web asks, nothing uploads until confirmed. -->
    <AttachSaveDialog
      :open="attachOpen"
      :project="attachProject"
      :files="attachRows"
      :initial-dir="attachDir"
      @confirm="confirmAttach"
      @cancel="closeAttach"
    />
    <!-- The language server the turn recommended: the card owns its own
         install poll and its goodbye, exactly as the web's does. -->
    <LspRecommendationCard
      v-if="lspRecommendation"
      :recommendation="lspRecommendation"
      :session-id="sessionId ?? undefined"
      @close="lspRecommendation = null"
    />
    <!-- The project's servers that still want an answer, asked in the panel
         the way the web asks them, and recorded the same way. -->
    <ProjectServersConsentDialog
      :open="projectServersOpen"
      :mcp="projectServersMcp"
      :lsp="projectServersLsp"
      :busy="projectServersBusy"
      :error="projectServersError"
      @confirm="confirmProjectServers"
      @decline-all="declineAllProjectServers"
      @close="declineAllProjectServers"
    />
    <!-- Rewinding to a message: the preview says what it takes back. -->
    <RewindDialog
      v-if="rewindOpen"
      :message="rewindTarget?.content ?? ''"
      :preview="rewindPreview"
      :busy="rewindBusy"
      :error="rewindError"
      @restore="restoreRewind"
      @cancel="rewindOpen = false"
    />
    <!-- The rewind undo line: after a rewind it offers to put it all back. -->
    <div v-if="rewindLine" class="wv-undo-line" data-testid="rewind-undo-line">
      <span>{{ rewindLine }}</span>
      <button type="button" class="wv-retry" data-testid="rewind-undo" @click="undoRewind">{{ t('chat.rewindUndo') }}</button>
    </div>
  </div>
</template>

<script setup lang="ts">
// The chat workbench: the state machine is useChatStream — the same one the
// web's conversation runs, over the injected transport — and this shell only
// wires it to the panels' surfaces: the header, the thread, the composer.
// The trust question rides the host's three-branch RPC; no TrustDialog is
// drawn here. What its steps own is not wired yet: the todo chip, the
// approval gate, the plan dock, the deliverable group.
import { computed, inject, onMounted, ref, watch } from 'vue'
import { Bot, PanelRight, Plus, Sparkles, Users } from 'lucide-vue-next'
import ComposerBar from './components/ComposerBar.vue'
import ThreadView from './components/ThreadView.vue'
import TodoPanel from './components/TodoPanel.vue'
import McpNotice from '@/components/chat/McpNotice.vue'
import LspRecommendationCard from '@/components/chat/LspRecommendationCard.vue'
import PeerDrawer from '@/components/chat/PeerDrawer.vue'
import PlanApprovalDock from '@/components/chat/PlanApprovalDock.vue'
import ProjectServersConsentDialog from '@/components/chat/ProjectServersConsentDialog.vue'
import RewindDialog from '@/components/chat/RewindDialog.vue'
import SessionHeartbeat from '@/components/chat/SessionHeartbeat.vue'
import FolderDialog from '@/components/chat/FolderDialog.vue'
import AttachSaveDialog from '@/components/chat/AttachSaveDialog.vue'
import ChatWorkbench from '@/components/chat/ChatWorkbench.vue'
import ToolDocCommand from '@/components/chat/ToolDocCommand.vue'
import ToolDocOutput from '@/components/chat/ToolDocOutput.vue'
import ToolDocTitle from '@/components/chat/ToolDocTitle.vue'
import SubagentDrawer from '@/components/chat/SubagentDrawer.vue'
import SubagentTab from '@/components/chat/SubagentTab.vue'
import { TRANSPORT_KEY } from './transport'
import { useChatStream, emptyPendingInputPreview, type ChatMessage, type PlanUpdateData } from '@/composables/useChatStream'
import { useViewMode } from '@/composables/useViewMode'
import { useChatSessions } from '@/composables/useChatSessions'
import { usePendingActions } from '@/composables/usePendingActions'
import { providePendingActions } from '@/composables/providePendingActions'
import { providePeerDirectory } from '@/composables/providePeerDirectory'
import { provideSurfaceNavigation } from '@/composables/provideSurfaceNavigation'
import { useWorkbench } from '@/composables/useWorkbench'
import { agentRosterConversationTarget, useAgentRoster } from '@/composables/useAgentRoster'
import { type DocTab } from '@/composables/useDocTabs'
import { getErrorMessage, forebrainApi, type AgentRosterRow, type ProjectRecord, type SessionModelOption, type SessionModelResponse, type WorkspaceScope } from '@/lib/api'
import type { ArtifactView } from '@/lib/artifacts'
import { targetFromFolder, targetFromProject, type ProjectTarget } from '@/lib/projectTarget'
import { lastSessionIdValue } from '@/composables/useLastSession'
import { isAllowedAttachmentType, type PickedFile, type SubmittedAttachment } from '@/lib/composerSubmission'
import type { BotOption } from '@/components/ForebrainPromptTextarea.vue'
import { t } from '@/locales'

const injectedTransport = inject(TRANSPORT_KEY)
// main.ts is the only mount point and it always provides; a missing transport
// is a wiring bug, not a state to render around.
const transport: NonNullable<typeof injectedTransport> = injectedTransport
  ?? (() => { throw new Error('the chat workbench needs the webview transport provided') })()

const stream = useChatStream()
const {
  sessionId, mode, isStreaming, error, messages, historyLoading, historyError,
  mcpStatus,
  suggestion, acceptSuggestion, dismissSuggestion,
  cancelAutoContinue, autoContinueForView,
  pendingInputPreview, editLastQueuedMessage, sendPendingSteersAfterInterrupt, lspRecommendation,
  runtimeStatus, send, cancel, switchMode, switchToSession, resetForNewChat,
  subagents, subagentCallTasks, subagentNow, subagentPendingInput,
  sendToSubagent, recallSubagentInput, pauseSubagent, interruptSubagentToSend,
  loadSubagentBudget, heldPeers, answerPeerHeld, peerDirectory,
  peerDirectoryPage, refreshPeerDirectory,
  sessionArtifacts,
} = stream
const PEER_DIRECTORY_PAGE_SIZE = 20

// The cards' "open the page that owns this" has no router here: another
// conversation is this panel's own switch, and a settings page is the Web UI
// in the person's browser, signed in by the host.
provideSurfaceNavigation({
  openConversation: (sid) => {
    if (sid && sid !== sessionId.value) void switchToSession(sid)
  },
  openSettingsPage: (tab, query) => {
    const params = new URLSearchParams({ tab, ...(query ?? {}) })
    transport.openWebUi(`/settings?${params.toString()}`)
  },
})

// The workbench column and the machine's running roster — the same two
// composables the web's chat page runs.
const workbench = useWorkbench()
const {
  records: rosterRecords,
  loading: rosterLoading,
  error: rosterError,
  loadRoster,
  cancelRoster,
  cancelAll,
} = useAgentRoster()
const runningRoster = computed(() =>
  rosterRecords.value.filter((row) => String(row.status ?? '').trim().toLowerCase() === 'running'))

const queueVisible = computed(() => Boolean(
  pendingInputPreview.value.pendingSteers.length
  || pendingInputPreview.value.queuedMessages.length,
))

const { sessions, fetchSessions } = useChatSessions()

// ---- session steering (the Sessions view's pick arrives here) ----
transport.onOpenSession((sid) => {
  if (sid !== sessionId.value) switchToSession(sid)
})
watch(sessionId, (sid) => {
  if (sid) transport.reportActiveSession(sid)
})

// The header names the conversation: the shared session store carries the
// titles, so the panel reads the same list the Sessions view does.
const headerTitle = computed(() => {
  if (!sessionId.value) return t('sidebar.untitledChat')
  const row = sessions.value.find((candidate) => candidate.id === sessionId.value)
  return row?.title ?? t('chatDrawer.untitled')
})

function newChat() {
  resetForNewChat()
}

// ---- the fresh chat: project pill, starters, the first send ----
const projects = ref<ProjectRecord[]>([])
const target = ref<ProjectTarget | null>(null)
const pillInvalid = ref(false)
const isFreshChat = computed(() => !sessionId.value && !messages.value.length && !isStreaming.value)

const projectNameOfLast = computed(() => sessions.value[0]?.projectName ?? '')

const workbenchScope = computed<WorkspaceScope>(() => {
  const sid = String(sessionId.value ?? '').trim()
  if (sid) return { sessionId: sid }
  const pid = String(target.value?.projectId ?? '').trim()
  if (pid) return { projectId: pid }
  return {}
})

const composerLocked = computed(() => false)

const nextSubmissionDisposition = ref<'steer' | 'queue'>('steer')
const busyNotice = ref('')
const composerNotice = ref<{ title: string; detail: string; detailCode?: string } | null>(null)

const composerPlaceholder = computed(() => t('chat.placeholder'))
const submitStatus = computed(() => (isStreaming.value ? 'streaming' as const : 'ready' as const))
const submitLabel = computed(() => (isStreaming.value ? t('chat.stopAria') : t('chat.sendAria')))

const availableBots = ref<BotOption[]>([])
// The @ picker's bot section: the panel lists none until its step owns the
// roster, and the fetch is done so the picker does not wait on one.
const botsFetchDone = ref(true)
// The reading mode is the conversation's own, kept per session exactly as the
// web's chat page keeps it (useViewMode): the chip writes it, the thread and
// the composer read it.
const { viewMode } = useViewMode(() => String(sessionId.value ?? ''))

async function loadProjects() {
  try {
    projects.value = await forebrainApi.projectsList({ sort: 'updated' })
  } catch {
    projects.value = []
  }
}

/** The pill preselects the last conversation's project, where the web's
 *  chat page starts a new chat too. */
onMounted(() => {
  void loadProjects()
  void fetchSessions()
  const lid = String(lastSessionIdValue() ?? '').trim()
  if (!lid) return
  void forebrainApi.chatSession(lid).then((info) => {
    if (!info.project) return
    const match = projects.value.find((p) => p.id === info.project?.id)
    target.value = match
      ? targetFromProject(match)
      : { projectId: info.project.id, name: info.project.name, root: '', isNew: false, trustLevel: 'trusted', projectType: info.project.projectType } as ProjectTarget
  }).catch(() => undefined)
})

function pickTarget(next: ProjectTarget) {
  target.value = next
  pillInvalid.value = false
}

// ---- the folder the next conversation runs in ----
const folderOpen = ref(false)

function browseFolder() {
  folderOpen.value = true
}

function onFolderPick(path: string) {
  folderOpen.value = false
  const existing = projects.value.find((p) => p.root === path)
  target.value = targetFromFolder(path, existing)
  pillInvalid.value = false
}

const composerRef = ref<InstanceType<typeof ComposerBar> | null>(null)

function focusComposer() {
  composerRef.value?.focus()
}
void focusComposer

function insertPlusCommand(text: string) {
  composerRef.value?.restoreSubmission({ text, attachments: [], mentionImages: [] })
}

function onComposerTyped() {
  cancelAutoContinue('')
  dismissSuggestion()
}

// ---- the trust question, through the host's three branches ----
/** Ask once whether a directory nobody has judged may be trusted — in the
 *  panel the question is the host's native modal with this app's words, and
 *  no dialog is drawn here. */
async function askTrustViaHost(picked: ProjectTarget): Promise<boolean> {
  return transport.askTrust(picked.root, {
    title: t('chat.trustTitle'),
    body: t('chat.trustBody'),
    confirm: t('chat.trustConfirm'),
    once: t('chat.trustOnce'),
    cancel: t('common.cancel'),
  })
}

async function ensureSessionForSend(): Promise<string | null> {
  if (sessionId.value) return sessionId.value
  const picked = target.value
  if (!picked) {
    pillInvalid.value = true
    composerRef.value?.focusPill()
    return null
  }
  if (picked.trustLevel === 'unknown') {
    const trusted = await askTrustViaHost(picked)
    if (!trusted) return null
  }
  try {
    const created = await forebrainApi.chatSessionCreate({
      ...(picked.projectId ? { projectId: picked.projectId } : { folder: picked.folder }),
    })
    await applyDraftChoices(created.id)
    sessionId.value = created.id
    return created.id
  } catch (cause) {
    const message = getErrorMessage(cause)
    if (message.includes('session_directory_missing')) {
      composerNotice.value = {
        title: t('chat.messageNotSent'),
        detail: t('chat.sessionDirMissing', { dir: picked.root }),
      }
    } else {
      composerNotice.value = { title: t('chat.messageNotSent'), detail: message }
    }
    return null
  }
}

/** What the chips were set to before there was a conversation becomes this
 *  conversation's own choice, ahead of its first turn. */
async function applyDraftChoices(id: string) {
  const model = draftModel.value
  const preset = composerRef.value?.takeDraftPreset() ?? null
  draftModel.value = null
  const writes: Promise<unknown>[] = []
  if (model) writes.push(forebrainApi.saveSessionModel(id, model).catch(() => undefined))
  if (preset) writes.push(forebrainApi.sessionPreset(id, preset).catch(() => undefined))
  await Promise.all(writes)
}

// ---- send / stop ----
async function handleSubmit(message: { text: string }) {
  const text = (message.text ?? '').trim()
  const attachedByComposer = composerRef.value?.hasAttached() ?? false
  if (!text && !attachedByComposer) return
  busyNotice.value = ''
  await waitForPendingAttachUpload()
  const attached = composerRef.value?.takeAttached() ?? { attachments: [], mentionImages: [] }
  composerNotice.value = null
  if (!sessionId.value) {
    const opened = await ensureSessionForSend()
    if (!opened) {
      composerRef.value?.restoreSubmission({ text: message.text ?? '', ...attached })
      return
    }
  }
  const activeInputDisposition = isStreaming.value ? nextSubmissionDisposition.value : undefined
  nextSubmissionDisposition.value = 'steer'
  await send(text, {
    sessionId: sessionId.value ?? undefined,
    attached: { attachments: attached.attachments, mentionImages: attached.mentionImages },
    activeInputDisposition,
  })
}

/** The conversation's own queue: taking the newest queued message back into
 *  the composer. */
async function restoreLastQueuedMessage() {
  const submission = await editLastQueuedMessage()
  if (!submission) return
  composerRef.value?.restoreSubmission(submission)
}

async function interruptAndSendPendingSteers() {
  await sendPendingSteersAfterInterrupt()
}

// ---- attachments: saved into the project, then uploaded over the RPC ----
/** The files a paste, drop or pick handed up, waiting to be saved. */
const attachPending = ref<PickedFile[]>([])
const attachProject = ref<{ id: string; name: string } | null>(null)
const attachDir = ref('')
const attachOpen = ref(false)
/** Which composer the files being saved belong to: a subagent's tab, or the
 *  conversation's own when empty. */
const attachTarget = ref('')
let pendingAttachUpload: Promise<unknown> | null = null

const attachRows = computed(() =>
  attachPending.value.map((f) => ({
    filename: f.filename,
    size: f.file.size,
    mediaType: f.mediaType,
    fromClipboard: f.fromClipboard ?? false,
  })))

/**
 * A file the user attached is saved into the project before the message is
 * sent — the same flow the web runs: the conversation is opened first, then
 * the "save to" dialog asks where. Nothing uploads until it is confirmed; a
 * cancel simply drops the files.
 */
async function onFilesPicked(files: PickedFile[], agentId = '') {
  const allowed = files.filter((f) => isAllowedAttachmentType(f.mediaType, f.filename))
  if (!allowed.length) return
  const opened = await ensureSessionForSend()
  if (!opened) return
  const project = await attachProjectFor(opened)
  if (!project) return
  attachTarget.value = String(agentId ?? '').trim()
  attachPending.value = allowed
  attachProject.value = { id: project.id, name: project.name }
  attachDir.value = project.lastAttachDir ?? ''
  attachOpen.value = true
}

async function attachProjectFor(session: string): Promise<{ id: string; name: string; lastAttachDir: string } | null> {
  try {
    const info = await forebrainApi.chatSession(session)
    if (!info.project) return null
    return { id: info.project.id, name: info.project.name, lastAttachDir: info.project.lastAttachDir ?? '' }
  } catch {
    return null
  }
}

function closeAttach() {
  attachOpen.value = false
  attachPending.value = []
  attachTarget.value = ''
}

/** Save the waiting files into dir and hand them to the composer they came
 *  from. A send that lands before the upload finishes waits for it. */
async function confirmAttach(dir: string) {
  const session = sessionId.value
  const pending = attachPending.value
  if (!session || !pending.length) {
    closeAttach()
    return
  }
  const run = (async () => {
    const uploaded: SubmittedAttachment[] = []
    for (const item of pending) {
      try {
        const res = await forebrainApi.filesUpload(item.file, session, dir)
        uploaded.push({ fileId: res.fileId, filename: item.filename, mediaType: item.mediaType })
      } catch (cause) {
        // The dialog stays open so the user can retry; nothing attached is lost.
        composerNotice.value = {
          title: t('chat.messageNotSent'),
          detail: `${t('chat.uploadFailed', { name: item.filename })}\n${getErrorMessage(cause)}`,
        }
        return
      }
    }
    const targetAgent = attachTarget.value
    if (targetAgent) subagentComposer(targetAgent)?.addUploaded(uploaded)
    else composerRef.value?.addUploaded(uploaded)
  })()
  pendingAttachUpload = run
  try {
    await run
  } finally {
    if (pendingAttachUpload === run) pendingAttachUpload = null
  }
  closeAttach()
}

async function waitForPendingAttachUpload() {
  while (pendingAttachUpload) {
    await pendingAttachUpload
  }
}

// ---- refusals the runtime hands back ----
watch(stream.returnedDraft, () => {
  const draft = stream.takeReturnedDraft()
  if (draft) composerRef.value?.restoreSubmission(draft)
  const notice = stream.takeReturnedNotice()
  const noticeCode = stream.takeReturnedNoticeCode()
  if (noticeCode === 'session_running' || noticeCode === 'session_running_elsewhere') {
    busyNotice.value = noticeCode
    return
  }
  if (notice || noticeCode) {
    composerNotice.value = { title: t('chat.messageNotSent'), detail: notice ?? '', ...(noticeCode ? { detailCode: noticeCode } : {}) }
  }
})

// ---- model / effort chips ----
const draftModel = ref<{ modelId?: string; provider: string; model: string; effort?: string } | null>(null)
const sessionModelData = ref<SessionModelResponse | null>(null)
let sessionModelGeneration = 0

async function loadSessionModel(sid: string | null) {
  const generation = ++sessionModelGeneration
  if (!sid) draftModel.value = null
  try {
    const res = sid ? await forebrainApi.sessionModel(sid) : await forebrainApi.chatModelDefault()
    if (generation === sessionModelGeneration) sessionModelData.value = res
  } catch {
    if (generation === sessionModelGeneration) sessionModelData.value = null
  }
}

watch(sessionId, (sid) => {
  void loadSessionModel(sid)
}, { immediate: true })

function pickDraftModel(option: SessionModelOption) {
  const data = sessionModelData.value
  if (!data) return
  draftModel.value = { modelId: option.id, provider: option.provider, model: option.model }
  sessionModelData.value = { ...data, modelId: option.id, provider: option.provider, model: option.model, effort: option.defaultEffort ?? '' }
}

// The catalog facts name the option in force; without a shared catalog row
// the selection itself is the answer (the web reads the same two). A session
// whose model payload carries no options list is the same "no catalog row"
// case, not a crash.
const sessionModelOption = computed(() =>
  (sessionModelData.value?.options ?? []).find((o) => o.id === sessionModelData.value?.modelId) ?? null)
const sessionEfforts = computed(() => sessionModelOption.value?.efforts ?? [])
const sessionDefaultEffort = computed(() => sessionModelOption.value?.defaultEffort ?? '')

async function selectEffort(effort: string) {
  const sid = sessionId.value
  const data = sessionModelData.value
  if (!data) return
  if (!sid) {
    draftModel.value = { modelId: data.modelId, provider: data.provider, model: data.model, effort }
    sessionModelData.value = { ...data, effort }
    return
  }
  try {
    const saved = await forebrainApi.saveSessionModel(sid, { modelId: data.modelId, provider: data.provider, model: data.model, effort })
    sessionModelData.value = { ...data, modelId: saved.modelId, provider: saved.provider, model: saved.model, effort: saved.effort }
  } catch {
    // The chip keeps the state it had; the next load reconciles with the row.
  }
}

async function selectMode(next: string) {
  const targetMode = next === 'plan' ? 'plan' : next === 'agent' ? 'agent' : null
  if (!targetMode || mode.value === targetMode || isStreaming.value) return
  await switchMode(targetMode, { sessionId: sessionId.value ?? undefined })
}

// ---- helpers ----
function pickFromNotice(message: ChatMessage, value: string) {
  // A primary agent picked from /agent is a tenant switch on the web; the
  // panel has no tenant chrome, so a pick resolves through the same choose
  // call and nothing else.
  if (!message.id || !message.picker || message.picked !== undefined) return
  void stream.choose(message.id, { command: message.picker.command, value })
}

const contextUsedPercent = computed(() => {
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const left = messages.value[i]?.tokenBudget?.percentLeft
    if (typeof left === 'number' && Number.isFinite(left)) {
      return Math.max(0, Math.min(100, 100 - left))
    }
  }
  return null
})

function copyMessage(message: ChatMessage) {
  void navigator.clipboard?.writeText(message.content ?? '').catch(() => undefined)
}

function retryHistory() {
  const sid = sessionId.value
  if (!sid) return
  void stream.loadMessages(sid)
}

// A finished turn refreshes the shared list, exactly as the web's page does.
watch(isStreaming, (streaming, wasStreaming) => {
  if (wasStreaming && !streaming) void fetchSessions()
})

// ============================================================================
// Steps 7-9. The gates decide through the same pending store the tail
// surface reads; the runtime cards, the drawers and the consent dialog are
// the web's own components, driven by the same composable state.
// ============================================================================

// ---- pending actions: the gates decide through approvals.decide ----
const pendingActions = usePendingActions({
  sessionId: computed(() => sessionId.value),
  version: computed(() => stream.pendingActionsVersion.value),
  contextUsedPercent,
})
providePendingActions(pendingActions)
// The peer directory the letters' sender labels read — the conversation's own
// cards and a subagent's ask the same two questions of the same directory.
providePeerDirectory(peerDirectory)

/** The exit-plan approval parked where the input is (Step 8's dock). */
const exitPlanPending = computed(() =>
  pendingActions.pendingApprovals.value.find((row) => pendingActions.isExitPlan(row)) ?? null)

// ---- the session's own facts: project, kind ----
const sessionInfo = ref<import('@/lib/api').ChatSessionInfo | null>(null)
watch(sessionId, (sid) => {
  sessionInfo.value = null
  if (!sid) return
  void forebrainApi.chatSession(sid).then((info) => {
    sessionInfo.value = info
  }).catch(() => undefined)
}, { immediate: true })

// ---- the project's servers that still want an answer ----
const projectServersScopeId = computed(() => {
  const sid = String(sessionId.value ?? '').trim()
  if (sid) return String(sessionInfo.value?.project?.id ?? '').trim()
  return String(target.value?.projectId ?? '').trim()
})
const projectServersMcp = ref<import('@/lib/api').ProjectServerPendingRow[]>([])
const projectServersLsp = ref<import('@/lib/api').ProjectServerPendingRow[]>([])
const projectServersOpen = ref(false)
const projectServersBusy = ref(false)
const projectServersError = ref('')
let projectServersGeneration = 0

async function loadProjectServers(projectId: string): Promise<void> {
  const generation = ++projectServersGeneration
  try {
    const answer = await forebrainApi.projectServersPending(projectId)
    if (generation !== projectServersGeneration) return
    projectServersMcp.value = answer.mcp
    projectServersLsp.value = answer.lsp
    projectServersOpen.value = answer.mcp.length > 0 || answer.lsp.length > 0
  } catch (cause) {
    if (generation !== projectServersGeneration) return
    projectServersError.value = getErrorMessage(cause)
  }
}

async function answerProjectServers(payload: { allowed: Record<string, boolean>; alwaysAllow?: boolean }): Promise<void> {
  const projectId = projectServersScopeId.value
  if (!projectId || projectServersBusy.value) return
  projectServersBusy.value = true
  projectServersError.value = ''
  const choice = (rows: import('@/lib/api').ProjectServerPendingRow[]) =>
    rows.map((row) => ({ name: row.name, fingerprint: row.fingerprint, allow: payload.allowed[row.name] === true }))
  try {
    await forebrainApi.projectServersConsent(projectId, {
      mcp: choice(projectServersMcp.value),
      lsp: choice(projectServersLsp.value),
      alwaysAllow: payload.alwaysAllow === true,
    })
    await loadProjectServers(projectId)
    // The answer is what this session's own MCP reconcile will find, so the
    // conversation is bound again.
    stream.rebindSessionObserver()
  } catch (cause) {
    projectServersError.value = getErrorMessage(cause)
    await loadProjectServers(projectId)
  } finally {
    projectServersBusy.value = false
  }
}

function confirmProjectServers(payload: { allowed: Record<string, boolean>; alwaysAllow: boolean }): void {
  void answerProjectServers({ ...payload, alwaysAllow: payload.alwaysAllow })
}
function declineAllProjectServers(): void {
  void answerProjectServers({ allowed: {} })
}

// A conversation that opens (or a project the pill picks) re-reads what it
// would ask about; nothing pending keeps the dialog closed.
watch(projectServersScopeId, (projectId) => {
  if (projectId) void loadProjectServers(projectId)
}, { immediate: true })

// ---- MCP servers that failed to start ----
const mcpFailures = computed(() =>
  (mcpStatus.value?.servers ?? [])
    .filter((row) => row.connStatus === 'error' && (row.error ?? '').trim())
    .map((row) => ({ name: row.name, error: (row.error ?? '').trim() })),
)

// ---- the peer letters and the directory ----
const peerDrawerOpen = ref(false)
const heldPeer = computed(() => heldPeers.value[0] ?? null)

async function releaseHeldPeer(held: import('@/lib/api').PeerHeldRow) {
  if (await answerPeerHeld(held.msgId, true)) settleHeldPeer(held)
}
async function denyHeldPeer(held: import('@/lib/api').PeerHeldRow) {
  if (await answerPeerHeld(held.msgId, false)) settleHeldPeer(held)
}
function settleHeldPeer(held: import('@/lib/api').PeerHeldRow) {
  // answerPeerHeld already dropped it from heldPeers; the banner/drawer read
  // the same list, so nothing else to clear here.
  void held
}
function changePeerPage(page: number) {
  void refreshPeerDirectory(page)
}

// ---- rewind ----
const rewindOpen = ref(false)
const rewindTarget = ref<ChatMessage | null>(null)
const rewindPreview = ref<import('@/lib/api').RewindPreview | null>(null)
const rewindBusy = ref(false)
const rewindError = ref('')
const rewindLine = ref<string | null>(null)

async function openRewind(msg: ChatMessage) {
  if (isStreaming.value) return
  const sid = String(sessionId.value ?? '').trim()
  if (!sid) return
  const rowId = Number(msg.rowId ?? 0)
  if (!rowId) {
    // A message sent live has no row id yet: read the transcript back and
    // find it by its ordinal among the user messages.
    const ordinal = messages.value.filter((m) => m.role === 'user').findIndex((m) => m.id === msg.id)
    await stream.loadMessages(sid)
    const users = messages.value.filter((m) => m.role === 'user')
    const match = ordinal >= 0 ? users[ordinal] : undefined
    if (!match?.rowId) return
    rewindTarget.value = match
  } else {
    rewindTarget.value = msg
  }
  rewindOpen.value = true
  rewindError.value = ''
  rewindBusy.value = true
  try {
    rewindPreview.value = await forebrainApi.rewindPreview(sid, Number(rewindTarget.value?.rowId ?? 0))
  } catch (cause) {
    rewindError.value = getErrorMessage(cause)
  } finally {
    rewindBusy.value = false
  }
}

async function restoreRewind(payload: { mode: 'both' | 'chat' | 'code'; keepPaths: string[] }) {
  const sid = String(sessionId.value ?? '').trim()
  const messageId = Number(rewindTarget.value?.rowId ?? 0)
  if (!sid || !messageId) return
  rewindBusy.value = true
  try {
    const res = await forebrainApi.rewind(sid, { messageId, mode: payload.mode, keepPaths: payload.keepPaths })
    rewindOpen.value = false
    rewindLine.value = payload.mode === 'code'
      ? t('chat.rewindDoneCode', { n: res.restored.length, text: (rewindTarget.value?.content ?? '').split('\n')[0] ?? '' })
      : t('chat.rewindDoneBoth', { n: res.restored.length })
    composerRef.value?.restoreSubmission({ text: res.anchorText, attachments: [], mentionImages: [] })
    await stream.loadMessages(sid)
  } catch (cause) {
    rewindError.value = getErrorMessage(cause)
  } finally {
    rewindBusy.value = false
  }
}

async function undoRewind() {
  const sid = String(sessionId.value ?? '').trim()
  if (!sid) return
  try {
    await forebrainApi.rewindUndo(sid)
    rewindLine.value = null
    await stream.loadMessages(sid)
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
}

// ---- the subagent drawer ----
const drawerOpen = ref(false)
const drawerTabs = ref<string[]>([])
const drawerActive = ref('')
const seenAgentActivity = ref<Record<string, number>>({})
const agentFolds = ref<Record<string, Record<string, boolean>>>({})
const subagentTabRefs: Record<string, {
  takeAttached: () => Pick<import('@/lib/composerSubmission').ComposerSubmission, 'attachments' | 'mentionImages'>
  hasAttached: () => boolean
  restoreSubmission: (submission: import('@/lib/composerSubmission').ComposerSubmission, unsent?: File[]) => void
  addUploaded: (attachments: SubmittedAttachment[]) => void
}> = {}

function setSubagentTabRef(agentId: string, el: unknown) {
  const id = String(agentId ?? '').trim()
  if (el) subagentTabRefs[id] = el as (typeof subagentTabRefs)[string]
  else delete subagentTabRefs[id]
}
function subagentComposer(agentId: string) {
  return subagentTabRefs[String(agentId ?? '').trim()] ?? null
}

const conversationSubagents = computed(() => subagents.value)
const drawerRecords = computed(() => drawerTabs.value
  .map((id) => subagents.value.find((record) => record.agentId === id))
  .filter((record): record is NonNullable<typeof record> => Boolean(record)))
const drawerVisible = computed(() => drawerOpen.value
  && (drawerRecords.value.length > 0 || docTabs.value.length > 0)
  && !isFreshChat.value)
const shownActive = computed(() => (
  drawerRecords.value.some((record) => record.agentId === drawerActive.value)
    ? drawerActive.value
    : (drawerRecords.value[0]?.agentId ?? '')
))

function markAgentSeen(agentId: string) {
  const entry = subagents.value.find((row) => row.agentId === agentId)
  if (!entry) return
  seenAgentActivity.value = { ...seenAgentActivity.value, [agentId]: entry.updatedSeq }
}

function openSubagentTab(agentId: string) {
  const id = String(agentId ?? '').trim()
  if (!id) return
  if (!drawerTabs.value.includes(id)) drawerTabs.value = [...drawerTabs.value, id]
  drawerActive.value = id
  drawerOpen.value = true
  markAgentSeen(id)
  void loadSubagentBudget(id)
}

function selectSubagentTab(agentId: string) {
  const id = String(agentId ?? '').trim()
  if (!id) return
  drawerOpen.value = true
  drawerActive.value = id
  markAgentSeen(id)
  void loadSubagentBudget(id)
}

function closeSubagentTab(agentId: string) {
  const rest = drawerTabs.value.filter((tab) => tab !== agentId)
  const index = drawerTabs.value.indexOf(agentId)
  drawerTabs.value = rest
  if (drawerActive.value === agentId) drawerActive.value = rest[index] ?? rest[index - 1] ?? ''
}

function updateAgentFold(agentId: string, value: { id: string; open: boolean }) {
  const foldID = String(value?.id ?? '').trim()
  if (!agentId || !foldID) return
  const prior = agentFolds.value[agentId] ?? {}
  agentFolds.value = { ...agentFolds.value, [agentId]: { ...prior, [foldID]: Boolean(value.open) } }
}

const subagentNotices = ref<Record<string, { title: string; detail: string } | null>>({})

function onSubagentSubmit(agentId: string, message: { text: string }) {
  void handleSubagentSubmit(agentId, message)
}

function onSubagentFold(agentId: string, value: { id: string; open: boolean }) {
  updateAgentFold(agentId, value)
}

async function handleSubagentSubmit(agentId: string, message: { text: string }) {  const id = String(agentId ?? '').trim()
  const text = (message.text ?? '').trim()
  const composer = subagentComposer(id)
  const attachedByComposer = composer?.hasAttached() ?? false
  if (!text && !attachedByComposer) return
  const refuse = (detail: string) => {
    subagentNotices.value = { ...subagentNotices.value, [id]: { title: t('chat.messageNotSent'), detail } }
  }
  // A shell line is the conversation's, never the subagent's.
  if (text.startsWith('!') || text.startsWith('/')) {
    refuse(t('chat.subagentViewCommand'))
    return
  }
  const attached = composer?.takeAttached() ?? { attachments: [], mentionImages: [] }
  subagentNotices.value = { ...subagentNotices.value, [id]: null }
  await sendToSubagent(id, { text, attachments: attached.attachments, mentionImages: attached.mentionImages })
}

function stopSubagent(agentId: string) {
  void forebrainApi.subagentCancel(agentId).catch((cause) => {
    subagentNotices.value = {
      ...subagentNotices.value,
      [agentId]: { title: t('chat.requestFailed'), detail: getErrorMessage(cause) },
    }
  })
}

async function recallIntoTab(agentId: string) {
  const submission = await recallSubagentInput(agentId)
  if (!submission) return
  subagentComposer(agentId)?.restoreSubmission(submission)
}

// ---- the session's todos: chip counts, unread, flash ----
const todosOpen = ref(false)
const todoUnread = ref<number[]>([])
const todoFlash = ref<number[]>([])
let todoFlashTimer: ReturnType<typeof setTimeout> | null = null
const TODO_UNREAD_KEY = 'forebrain.todos.unread'

const latestTodo = computed(() => {
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const updates = messages.value[i]?.planUpdates
    if (updates?.length) return updates[updates.length - 1]
  }
  return null
})
const todoTotal = computed(() => latestTodo.value?.items.length ?? 0)
const todoCountText = computed(() => {
  const items = latestTodo.value?.items ?? []
  const done = items.filter((item) => ['completed', 'done'].includes(String(item.status ?? '').trim().toLowerCase())).length
  return `${done}/${items.length}`
})

function persistTodos() {
  const sid = String(sessionId.value ?? '').trim()
  if (!sid) return
  try { localStorage.setItem(`${TODO_UNREAD_KEY}.${sid}`, JSON.stringify(todoUnread.value)) } catch { /* storage refused: unread stays in memory */ }
}

watch(latestTodo, (todo) => {
  const sid = String(sessionId.value ?? '').trim()
  if (!todo || !sid) return
  let previous: PlanUpdateData['items'] = []
  try { previous = JSON.parse(localStorage.getItem(`${TODO_UNREAD_KEY}.snapshot.${sid}`) ?? '[]') as PlanUpdateData['items'] } catch { previous = [] }
  const changed = todo.items
    .map((item, index) => (!previous[index] || JSON.stringify(previous[index]) !== JSON.stringify(item) ? index : -1))
    .filter((index) => index >= 0)
  if (changed.length === 0) return
  const merged = [...new Set([...todoUnread.value, ...changed])].sort((a, b) => a - b)
  todoUnread.value = merged
  todoFlash.value = changed
  if (todoFlashTimer) clearTimeout(todoFlashTimer)
  todoFlashTimer = setTimeout(() => { todoFlash.value = [] }, 1500)
  persistTodos()
  try { localStorage.setItem(`${TODO_UNREAD_KEY}.snapshot.${sid}`, JSON.stringify(todo.items)) } catch { /* as above */ }
}, { deep: true })

watch(sessionId, (sid) => {
  const key = `${TODO_UNREAD_KEY}.${String(sid ?? '').trim()}`
  try { todoUnread.value = JSON.parse(localStorage.getItem(key) ?? '[]') as number[] } catch { todoUnread.value = [] }
})

function markTodoRead(index: number) {
  todoUnread.value = todoUnread.value.filter((unread) => unread !== index)
  persistTodos()
}
function markAllTodosRead() {
  todoUnread.value = []
  persistTodos()
}

// ---- the documents a card head opened (whole command / title / output) ----
const docTabs = ref<DocTab[]>([])
const docActive = ref('')
/** The order tabs were opened in, across subagents and documents alike. */
const drawerOrder = ref<string[]>([])

function outputSourceOf(tab: DocTab): { sessionId: string; agentId?: string; retrieveId: number } | undefined {
  const source = tab.source
  if (!source?.sessionId || !source.retrieveId) return undefined
  return source as { sessionId: string; agentId?: string; retrieveId: number }
}

/** The same content is the same tab: opening it again only brings it front. */
function openDocTab(tab: DocTab) {
  if (!docTabs.value.some((open) => open.id === tab.id)) docTabs.value = [...docTabs.value, tab]
  if (!drawerOrder.value.includes(tab.id)) drawerOrder.value = [...drawerOrder.value, tab.id]
  docActive.value = tab.id
  drawerOpen.value = true
}

function selectDocTab(id: string) {
  if (!docTabs.value.some((tab) => tab.id === id)) return
  drawerOpen.value = true
  docActive.value = id
}

function closeDocTab(id: string) {
  const rest = docTabs.value.filter((tab) => tab.id !== id)
  const index = docTabs.value.findIndex((tab) => tab.id === id)
  docTabs.value = rest
  drawerOrder.value = drawerOrder.value.filter((tab) => tab !== id)
  if (docActive.value === id) docActive.value = rest[index]?.id ?? rest[index - 1]?.id ?? ''
}

// ---- the drawers' own openers (the header's buttons) ----
function openSubagentDrawer() {
  const first = subagents.value[0]?.agentId ?? ''
  if (first) openSubagentTab(first)
  else drawerOpen.value = true
}

function openPeerDrawer() {
  peerDrawerOpen.value = true
  void refreshPeerDirectory(peerDirectoryPage.value)
}

// ---- the workbench column ----
/** Office creative work does not change the project's own files: its
 *  workbench offers no 变更文件 tab (the same rule the web applies). */
const notACodeConversation = computed(() => {
  const type = String(sessionInfo.value?.projectType ?? '').trim().toLowerCase()
  return type === 'office' || type === 'batch'
})

/** A roster row's "view": a subagent of this conversation opens in the drawer,
 *  one from another conversation opens that conversation and then its tab. */
const pendingRosterAgent = ref('')
function viewRoster(row: AgentRosterRow) {
  if (row.kind === 'subagent' && subagents.value.some((record) => record.agentId === row.id)) {
    openSubagentTab(row.id)
    return
  }
  const target = agentRosterConversationTarget(row)
  const query = ((target ?? {}) as { query?: Record<string, string> }).query ?? {}
  const sid = String(query.session ?? '').trim()
  if (!sid) return
  const agent = String(query.agent ?? '').trim()
  if (sid === sessionId.value) {
    if (agent) openSubagentTab(agent)
    return
  }
  pendingRosterAgent.value = agent
  void switchToSession(sid)
}

// The conversation a jump landed on loads its own subagents; the tab the jump
// named opens as soon as that list carries it.
watch(subagents, (records) => {
  const agent = pendingRosterAgent.value
  if (!agent) return
  if (!records.some((record) => record.agentId === agent)) return
  pendingRosterAgent.value = ''
  openSubagentTab(agent)
})

function insertWorkspaceRef(path: string) {
  void composerRef.value?.insertWorkspacePath?.(path)
}

// The roster is read when the workbench opens, and again after a turn ends —
// the same two moments the web reads it.
watch(() => workbench.open.value, (open) => {
  if (open) void loadRoster()
})
watch(isStreaming, (streaming, wasStreaming) => {
  if (wasStreaming && !streaming && workbench.open.value) void loadRoster()
})

// ---- the plan dock's eye, and the deliverable eye ----
function openPlanDoc(plan: { path: string }) {
  transport.openPlanDoc(plan.path)
}
function openDeliverable(artifact: ArtifactView) {
  // displayPath is the path the file is located by (rel_path under the
  // project, or the row's own absolute path).
  transport.openDeliverable(artifact.displayPath ?? artifact.path ?? artifact.relPath ?? '')
}
</script>

<style scoped>
.wv-app {
  display: flex;
  flex-direction: column;
  height: 100vh;
  background: var(--forebrain-bg);
  color: var(--forebrain-text);
  font-family: var(--forebrain-ui-font);
  font-size: 13px;
}
.wv-h {
  flex: none;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--forebrain-divider);
  background: var(--forebrain-bg);
}
.wv-h-title {
  font-size: 13px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.wv-icon-btn {
  display: inline-grid;
  place-items: center;
  width: 24px;
  height: 24px;
  border-radius: 6px;
  border: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
  color: var(--forebrain-text-2);
  cursor: pointer;
}
.wv-main {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.wv-thread-scroll {
  flex: 1;
  min-height: 0;
}
.wv-empty {
  flex: 1;
  display: grid;
  place-items: center;
  padding: 16px;
}
.wv-empty > div {
  max-width: 420px;
  text-align: center;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
}
.wv-empty-mark {
  display: inline-grid;
  place-items: center;
  width: 32px;
  height: 32px;
  border-radius: 10px;
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
  margin-bottom: 4px;
}
.wv-empty h3 {
  margin: 0;
  font-size: 15px;
  color: var(--forebrain-text);
}
.wv-empty p {
  margin: 0;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--forebrain-muted-text);
}
</style>
