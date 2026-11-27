<template>
  <div class="wv-app">
    <header class="wv-h" data-testid="webview-header">
      <span class="wv-h-title">{{ headerTitle }}</span>
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
        @choose="pickFromNotice"
        @copy-message="copyMessage"
        @retry-history="retryHistory"
      />
    </main>
    <ComposerBar
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
    />
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
import { Plus, Sparkles } from 'lucide-vue-next'
import ComposerBar from './components/ComposerBar.vue'
import ThreadView from './components/ThreadView.vue'
import { TRANSPORT_KEY } from './transport'
import { useChatStream, type ChatMessage } from '@/composables/useChatStream'
import { useChatSessions } from '@/composables/useChatSessions'
import { getErrorMessage, forebrainApi, type ProjectRecord, type SessionModelOption, type SessionModelResponse, type WorkspaceScope } from '@/lib/api'
import { targetFromProject, type ProjectTarget } from '@/lib/projectTarget'
import { lastSessionIdValue } from '@/composables/useLastSession'
import type { PickedFile, SubmittedAttachment } from '@/lib/composerSubmission'
import type { BotOption } from '@/components/ForebrainPromptTextarea.vue'
import { t } from '@/locales'

const injectedTransport = inject(TRANSPORT_KEY)
// main.ts is the only mount point and it always provides; a missing transport
// is a wiring bug, not a state to render around.
const transport: NonNullable<typeof injectedTransport> = injectedTransport
  ?? (() => { throw new Error('the chat workbench needs the webview transport provided') })()

const stream = useChatStream()
const { sessionId, mode, isStreaming, error, messages, historyLoading, historyError } = stream
const {
  suggestion, acceptSuggestion, dismissSuggestion,
  cancelAutoContinue,
  pendingInputPreview, editLastQueuedMessage, sendPendingSteersAfterInterrupt,
  runtimeStatus, send, cancel, switchMode, switchToSession, resetForNewChat,
} = stream

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
const viewMode = ref<'default' | 'concise'>('default')

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

function browseFolder() {
  // The panel's folder picker is the host's (Step 9); the pill's own project
  // list still covers the projects that exist.
  void composerRef.value?.focusPill()
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

// ---- attachments: upload over the RPC, straight into the composer ----
let pendingAttachUpload: Promise<unknown> | null = null
async function onFilesPicked(files: PickedFile[]) {
  if (!files.length) return
  const opened = await ensureSessionForSend()
  if (!opened) return
  const upload = Promise.all(files.map(async (file) => {
    if (!file.file) return null
    try {
      const result = await forebrainApi.filesUpload(file.file, opened)
      const row: SubmittedAttachment = { fileId: result.fileId, filename: file.filename, mediaType: file.mediaType }
      return row
    } catch {
      composerNotice.value = {
        title: t('chat.fileNotAdded'),
        detail: t('chat.uploadFailed', { name: file.filename }),
      }
      return null
    }
  }))
  pendingAttachUpload = upload
  const uploaded = (await upload).filter((row): row is NonNullable<typeof row> => row !== null)
  pendingAttachUpload = null
  if (uploaded.length) composerRef.value?.addUploaded(uploaded)
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
// the selection itself is the answer (the web reads the same two).
const sessionEfforts = computed(() => sessionModelData.value?.options.find((o) => o.id === sessionModelData.value?.modelId)?.efforts ?? [])
const sessionDefaultEffort = computed(() => sessionModelData.value?.options.find((o) => o.id === sessionModelData.value?.modelId)?.defaultEffort ?? '')

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
  overflow-y: auto;
}
.wv-thread-scroll {
  flex: 1;
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
