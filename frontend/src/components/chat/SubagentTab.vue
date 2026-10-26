<template>
  <div class="flex h-full min-h-0 flex-col" @keydown.esc="onEsc">
    <!-- The subagent's own transcript: its task card, then what it thought,
         called and answered, in the order it produced them. -->
    <Conversation class="min-h-0 flex-1 overflow-y-auto">
      <ConversationContent class="px-[18px] pt-4 pb-3">
        <!-- /context in this tab, drawn with the conversation's own panel so
             the two reports are read the same way. -->
        <ContextDebugPanel
          v-if="context"
          :data="context.data"
          :loading="context.loading"
          :error="null"
          @refresh="emit('refresh-context')"
        />
        <SubagentConversation
          :record="record"
          :fold-state="foldState"
          :call-tasks="callTasks"
          :now="now"
          :scope="scope"
          :active-agent-id="activeAgentId"
          :view-mode="viewMode"
          @fold-change="(value) => emit('fold-change', value)"
          @open-agent="(agentId) => emit('open-agent', agentId)"
          @open-doc="(tab) => emit('open-doc', tab)"
        />
      </ConversationContent>
      <!-- The drawer's own control sits above its composer, not over it. The
           composer lives outside the conversation's scroll box, so the
           prototype's 12px above the fold line is the button's own bottom. -->
      <ConversationScrollButton :label="t('chat.scrollToLatest')" />
    </Conversation>

    <!-- The terminal's working line sits above the composer, never in the
         record: the state, then the checklist it is working through. A network
         retry replaces it whole — the reason, then the countdown with the
         attempt — exactly as the conversation's own line does. -->
    <RunStatusLine
      v-if="active"
      class="px-[18px] pb-1.5"
      data-testid="subagent-working"
      :label="workingLabel"
      :reason="retryLine?.reason"
      :plan="retryLine ? undefined : (record.plan && record.plan.total > 0 ? record.plan : undefined)"
      :tail="retryLine ? '' : (record.plan?.active ?? '')"
    />

    <div class="subagent-composer">
      <PromptInputProvider :max-files="5" accept="image/*,application/pdf" @submit="(m) => emit('submit', m)">
        <Alert v-if="notice" variant="destructive" class="mb-2">
          <CircleAlert class="size-4" />
          <AlertTitle>{{ notice.title }}</AlertTitle>
          <AlertDescription class="whitespace-pre-line">{{ notice.detail }}</AlertDescription>
        </Alert>
        <AutoContinueBanner
          :state="autoContinue"
          :typing-cancels="active"
          @cancel="emit('cancel-auto-continue')"
        />
        <!-- Relative, so the model-and-context card opens against the composer
             rather than against the pill. -->
        <div v-if="active" class="relative">
          <PendingInputQueuePopover
            :preview="pendingInput"
            @edit-last-queued="emit('edit-last-queued')"
            @interrupt-run="emit('interrupt-run')"
          />
          <PromptInput
            class="subagent-composer-input w-full [&_[data-slot=input-group]]:border-[var(--forebrain-divider-strong)] [&_[data-slot=input-group]]:bg-[var(--forebrain-surface)] [&_[data-slot=input-group]]:ring-0 [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus]:border-[var(--forebrain-focus-border)] [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus-visible]:border-[var(--forebrain-focus-border)] [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus]:ring-0 [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus-visible]:ring-0"
          >
            <PromptInputBody>
              <ForebrainPromptTextarea
                ref="promptRef"
                :placeholder="t('chat.subagentComposerPlaceholder')"
                :bots="[]"
                :during-run="active"
                :queue-visible="queueVisible"
                :scope="scope"
                subagent-view
                attach-save
                class="min-h-[2.75rem] max-h-40 md:min-h-[2.75rem]"
                @files-picked="(files) => emit('files-picked', files)"
                @edit-last-queued="emit('edit-last-queued')"
                @typed="emit('typed')"
                @interrupt-run="emit('escape-key')"
              />
            </PromptInputBody>
            <PromptInputFooter>
              <ComposerPlusMenu subagent-view @insert="insertCommand" />
              <ViewModeChip v-model="viewMode" />
              <div class="flex items-center gap-1">
                <SubagentRunPill :record="record" />
                <button
                  v-if="active"
                  type="button"
                  class="subagent-stop"
                  v-tip:top="t('chat.subagentStop')"
                  :aria-label="t('chat.subagentStop')"
                  data-testid="subagent-stop"
                  @click="emit('stop')"
                >
                  <Square aria-hidden="true" />
                </button>
                <PromptInputSubmit status="ready" :label="t('chat.sendAria')" />
              </div>
            </PromptInputFooter>
          </PromptInput>
        </div>
        <!-- An idle subagent takes no messages: the agent that dispatched it
             decides what it does next, so its view draws no input box at all.
             What is left where the composer's bar was is what only reads the
             run it finished — the reading mode and the model/context pill, in
             the bar's own order (plan 143, the terminal's rule). -->
        <div v-else class="subagent-idle-bar" data-testid="subagent-idle-bar">
          <ViewModeChip v-model="viewMode" />
          <SubagentRunPill :record="record" />
        </div>
      </PromptInputProvider>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * One subagent, as a tab of the conversation's right-hand drawer: its
 * transcript, its own working line, and — while it runs — a composer of its
 * own that holds the same controls as the conversation's: attachments, skills,
 * @ references and the commands that view may run.
 *
 * A subagent takes a message only while it runs (plan 143): the agent that
 * dispatched it decides what it does next, so an idle tab draws no input box at
 * all, just the reading mode and the pill that read the run it finished — the
 * same rule the terminal's subagent view follows.
 *
 * Every tab keeps its own composer state, so opening another subagent never
 * moves a draft: ChatView mounts each open tab and shows one at a time.
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { CircleAlert, Square } from 'lucide-vue-next'
import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
} from '@repo/elements/conversation'
import {
  PromptInput,
  PromptInputBody,
  PromptInputFooter,
  PromptInputProvider,
  PromptInputSubmit,
} from '@repo/elements/prompt-input'
import { Alert, AlertDescription, AlertTitle } from '@repo/shadcn-vue/components/ui/alert'
import ForebrainPromptTextarea from '@/components/ForebrainPromptTextarea.vue'
import ComposerPlusMenu from '@/components/chat/ComposerPlusMenu.vue'
import ViewModeChip from '@/components/chat/ViewModeChip.vue'
import ContextDebugPanel from '@/components/ContextDebugPanel.vue'
import SubagentConversation from '@/components/chat/SubagentConversation.vue'
import SubagentRunPill from '@/components/chat/SubagentRunPill.vue'
import RunStatusLine from '@/components/chat/RunStatusLine.vue'
import AutoContinueBanner from '@/components/chat/AutoContinueBanner.vue'
import PendingInputQueuePopover from '@/components/PendingInputQueuePopover.vue'
import {
  formatFirstTokenDuration,
  formatRuntimeDuration,
  formatTransportRetryLine,
  type PendingInputPreview,
  type SubagentCallTaskLive,
  type SubagentTranscript,
} from '@/composables/useChatStream'
import type { AutoContinueState } from '@/lib/autoContinue'
import type { SessionContextDebug, WorkspaceScope } from '@/lib/api'
import type { PickedFile, ComposerSubmission, SubmittedAttachment } from '@/lib/composerSubmission'
import { subagentPhase } from '@/lib/subagentStatus'
import { useViewMode } from '@/composables/useViewMode'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  record: SubagentTranscript
  foldState: Record<string, boolean>
  /** The live bindings of the subagent_* cards this transcript draws. */
  callTasks?: ReadonlyMap<string, ReadonlyMap<number, SubagentCallTaskLive>>
  /** The stream's shared clock, so the cards' walking rows tick with it. */
  now?: number
  /** The conversation's file view: the @ picker and image steps resolve there. */
  scope?: WorkspaceScope
  /** The tab the drawer has in front, so a nested dispatch opens there marked. */
  activeAgentId?: string
  /** This subagent's own queue: what is waiting to be sent to it. */
  pendingInput: PendingInputPreview
  /** The continuation it waits on for a usage limit, if any. */
  autoContinue?: AutoContinueState | null
  /** A refusal from this tab's last submission, in the server's own words. */
  notice?: { title: string; detail: string } | null
  /** Its own /context report, or null while none is open. */
  context?: { data: SessionContextDebug | null; loading: boolean } | null
}>(), {
  callTasks: undefined,
  now: undefined,
  scope: undefined,
  activeAgentId: '',
  autoContinue: null,
  notice: null,
  context: null,
})

const emit = defineEmits<{
  (e: 'submit', message: { text: string; files?: unknown[] }): void
  (e: 'fold-change', value: { id: string; open: boolean }): void
  (e: 'open-agent', agentId: string): void
  (e: 'open-doc', tab: import('@/composables/useDocTabs').DocTab): void
  (e: 'stop'): void
  (e: 'edit-last-queued'): void
  (e: 'interrupt-run'): void
  (e: 'typed'): void
  (e: 'escape-key'): void
  (e: 'files-picked', files: PickedFile[]): void
  (e: 'cancel-auto-continue'): void
  (e: 'refresh-context'): void
  (e: 'dismiss-notice'): void
}>()

const { t } = useI18n()
const promptRef = ref<InstanceType<typeof ForebrainPromptTextarea> | null>(null)

// Still in progress — running, or waiting on the operator. Only then does this
// tab take a message; when it goes idle its composer is gone (plan 143).
const active = computed(() => subagentPhase(props.record.status) === 'active')

/**
 * How much of this subagent's run its tab draws — its own reading mode, kept
 * under this conversation and this agent, so one tab's choice never changes
 * another tab's, nor the conversation's.
 */
const { viewMode } = useViewMode(
  () => String(props.scope?.sessionId ?? ''),
  () => props.record.agentId,
)

// The queue preview is shown exactly as long as one of its lines has content.
const queueVisible = computed(() => {
  const preview = props.pendingInput
  return preview.pendingSteers.length + preview.rejectedSteers.length + preview.queuedMessages.length > 0
})

// The working line's clock is this execution's own: it counts from the spawn
// the engine recorded, a second at a time, and stops the moment the run ends.
const nowMs = ref(Date.now())
let workingTimer: ReturnType<typeof setInterval> | null = null
watch(active, (running) => {
  if (running && workingTimer == null) {
    nowMs.value = Date.now()
    workingTimer = setInterval(() => {
      nowMs.value = Date.now()
    }, 1_000)
  } else if (!running && workingTimer != null) {
    clearInterval(workingTimer)
    workingTimer = null
  }
}, { immediate: true })
onBeforeUnmount(() => {
  if (workingTimer != null) {
    clearInterval(workingTimer)
    workingTimer = null
  }
})

const duration = computed(() => {
  const start = props.record.startedAt ? Date.parse(props.record.startedAt) : NaN
  if (!Number.isFinite(start)) return ''
  return formatRuntimeDuration(Math.max(0, nowMs.value - start))
})

// The retry line, when this subagent's own stream is waiting on the network:
// it counts down on the same once-a-second clock the duration walks.
const retryLine = computed(() =>
  props.record.retry ? formatTransportRetryLine(props.record.retry, t, nowMs.value) : undefined)

// What the tab's status line says: the retry owns it while one is pending,
// and the ordinary working label — with its checklist and its task — returns
// once the retry is over. The label carries this execution's first-token wait
// once the engine has measured it, the same suffix the conversation's own
// working line shows for the turn.
const firstTokenSuffix = computed(() => {
  const wait = formatFirstTokenDuration(props.record.firstTokenMs)
  return wait ? ` · ${t('chat.firstToken', { duration: wait })}` : ''
})
const workingLabel = computed(() =>
  retryLine.value ? retryLine.value.label : t('chat.subagentWorking', { duration: duration.value }) + firstTokenSuffix.value)

/** A command picked in the "+" menu lands in this tab's composer, not sent. */
function insertCommand(text: string) {
  promptRef.value?.restoreSubmission({ text, attachments: [], mentionImages: [] })
}

// Escape means one thing to this tab, and the drawer must not hear it twice:
// marking the key as used here is what tells its own handler to stand down.
function onEsc(event: KeyboardEvent) {
  if (event.defaultPrevented) return
  event.preventDefault()
  emit('escape-key')
}

defineExpose({
  takeAttached: () => promptRef.value?.takeAttached(),
  hasAttached: () => promptRef.value?.hasAttached() ?? false,
  restoreSubmission: (submission: ComposerSubmission, unsent: File[] = []) =>
    promptRef.value?.restoreSubmission(submission, unsent),
  addUploaded: (attachments: SubmittedAttachment[]) => promptRef.value?.addUploaded(attachments),
  insertWorkspacePath: (path: string, isDir = false) => promptRef.value?.insertWorkspacePath(path, isDir),
})
</script>

<style scoped>
.subagent-composer {
  flex: none;
  margin: 0 12px 12px;
}

/* The composer's own border is the stronger one, as in the prototype: it sits
   inside the drawer rather than on the conversation's surface. */
.subagent-composer-input :deep([data-slot='input-group']) {
  border-radius: 10px;
  min-height: 0;
}

/* The bar an idle tab keeps where its composer's footer was: the reading mode
   on the left, the model-and-context pill on the right, in the footer's own
   padding — no border, no fill, nothing to type into. */
.subagent-idle-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.25rem;
  padding: 0 0.75rem 0.75rem;
}

.subagent-stop {
  width: 28px;
  height: 28px;
  flex: none;
  display: grid;
  place-items: center;
  border: 1px solid var(--forebrain-divider);
  border-radius: 7px;
  background: var(--forebrain-surface);
  color: var(--forebrain-danger);
  cursor: pointer;
}

.subagent-stop:hover {
  background: var(--forebrain-button-alt-bg);
}

.subagent-stop svg {
  width: 13px;
  height: 13px;
}
</style>
