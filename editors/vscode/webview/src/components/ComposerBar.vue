<template>
  <div class="wv-composer" data-testid="webview-composer">
    <!-- The run's own state, where the terminal draws it: above the queue
         preview and the composer, gone the moment the run ends. -->
    <RunStatusLine v-if="runLine" data-testid="run-status-line" class="wv-runline" :label="runLine.label" :reason="runLine.reason" :plan="runLine.plan" :tail="runLine.tail" />
    <div v-if="composerNotice" data-testid="composer-notice">
      <Alert variant="destructive">
        <CircleAlert class="size-4" />
        <AlertTitle>{{ composerNotice.title }}</AlertTitle>
        <AlertDescription class="whitespace-pre-line">{{ composerNoticeDetail }}</AlertDescription>
      </Alert>
    </div>
    <!-- A peer message waiting for the person: the one thing above the
         composer that is a question rather than a notice. -->
    <PeerHeldBanner
      v-if="heldPeer"
      :held="heldPeer"
      @release="(row: PeerHeldRow) => emit('peer-release', row)"
      @deny="(row: PeerHeldRow) => emit('peer-deny', row)"
      @settled="(row: PeerHeldRow) => emit('peer-settled', row)"
    />
    <!-- A spent usage limit the runtime will clear on its own: the wait is
         said, and typing in the composer cancels it. -->
    <AutoContinueBanner :state="autoContinue" @cancel="emit('escape')" />
    <div class="wv-composer-relative">
      <PendingInputQueuePopover
        :preview="pendingInputPreview"
        @edit-last-queued="emit('edit-last-queued')"
        @interrupt-run="emit('interrupt-run')"
      />
      <PromptInputProvider :max-files="maxFiles" accept="image/*,application/pdf" @submit="onSubmit">
        <PromptInput
          multiple
          global-drop
          class="w-full [&_[data-slot=input-group]]:rounded-xl [&_[data-slot=input-group]]:border-[var(--forebrain-divider-strong)] [&_[data-slot=input-group]]:bg-[var(--forebrain-surface)] [&_[data-slot=input-group]]:shadow-none [&_[data-slot=input-group]]:ring-0 [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus]:border-[var(--forebrain-brand-1)] [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus-visible]:border-[var(--forebrain-brand-1)] [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus]:ring-2 [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus-visible]:ring-2 [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus]:ring-[var(--forebrain-brand-1)]/20 [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus-visible]:ring-[var(--forebrain-brand-1)]/20"
          :class="composerLocked ? 'wv-composer-locked' : ''"
        >
          <PromptInputHeader v-if="freshChat" class="wv-ctop">
            <ProjectPill
              ref="pillRef"
              :projects="projects"
              :selected="target"
              :invalid="pillInvalid"
              @select="(next: ProjectTarget) => emit('pick-target', next)"
              @browse="emit('browse-folder')"
            />
          </PromptInputHeader>
          <PromptInputBody>
            <ForebrainPromptTextarea
              ref="promptRef"
              :placeholder="placeholder"
              :bots="availableBots"
              :bots-fetch-done="botsFetchDone"
              :during-run="isStreaming"
              :queue-visible="queueVisible"
              :suggestion="suggestion"
              :disabled="composerLocked"
              :scope="scope"
              @queue-follow-up="emit('queue-follow-up')"
              @edit-last-queued="emit('edit-last-queued')"
              @interrupt-run="emit('interrupt-run')"
              @suggestion-accepted="emit('suggestion-accepted')"
              @typed="emit('typed')"
              @escape="emit('escape')"
              @files-picked="(files: PickedFile[]) => emit('files-picked', files)"
            />
          </PromptInputBody>
          <PromptInputFooter class="!px-2 !pb-2 flex-wrap">
            <div class="flex min-w-0 flex-1 flex-wrap items-center gap-2">
              <ComposerPlusMenu :disabled="composerLocked" @insert="(text: string) => emit('insert-plus', text)" />
              <ModeChip :mode="mode" :disabled="isStreaming || composerLocked" @select="(next: string) => emit('select-mode', next)" />
              <ApprovalPresetPicker ref="approvalPickerRef" :session-id="sessionId" />
              <ViewModeChip v-model="viewMode" />
              <!-- The session's todos, toggled in place: the panel's own
                   checklist, not a native tree. An unacknowledged update
                   hangs an unread badge on the chip. -->
              <button
                v-if="todoTotal > 0"
                type="button"
                class="wv-todo-chip"
                :class="{ 'wv-todo-chip--on': todosOpen }"
                data-testid="todos-toggle"
                :aria-expanded="todosOpen"
                aria-controls="forebrain-todos"
                :aria-label="todoUnread ? `待办，${todoUnread} 项未读` : '待办'"
                @click="emit('toggle-todos')"
              >
                待办<span class="wv-todo-chip-n">{{ todoCountText }}</span>
                <span v-if="todoUnread" class="wv-todo-chip-badge" data-testid="todos-badge">{{ todoUnread }}</span>
              </button>
            </div>
            <div class="flex items-center gap-2">
              <ContextRing v-if="contextUsedPercent != null" :percent="contextUsedPercent ?? 0" @open="emit('open-context')" />
              <ModelChip
                :session-id="sessionId ?? ''"
                :selection="sessionModelData"
                :options="sessionModelData?.options ?? []"
                :disabled="composerLocked || !sessionModelData"
                :compact="compact"
                @saved="emit('model-saved')"
                @pick="(option: SessionModelOption) => emit('pick-model', option)"
              />
              <EffortChip
                v-if="!sessionModelData || sessionEfforts.length"
                :effort="sessionModelData?.effort ?? ''"
                :efforts="sessionEfforts"
                :default-effort="sessionDefaultEffort"
                :disabled="composerLocked || !sessionModelData"
                @select="(next: string) => emit('select-effort', next)"
              />
              <PromptInputSubmit
                :status="submitStatus"
                :disabled="composerLocked"
                :label="submitLabel"
                @stop="emit('stop')"
              />
            </div>
          </PromptInputFooter>
        </PromptInput>
      </PromptInputProvider>
    </div>
    <!-- The quick starts, under the composer, as the new-chat page's
         prototype draws them. The panel has no workshop or cron space to
         jump to, so only the two starters a conversation can act on draw. -->
    <div v-if="freshChat" class="wv-starters" data-testid="webview-starters">
      <button v-for="starter in starters" :key="starter.action" type="button" class="wv-starter" data-testid="webview-starter" @click="runStarter(starter.action)">
        <component :is="starter.icon" class="size-3.5" aria-hidden="true" />
        {{ t(starter.label) }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
// The composer, assembled from the same chips and the same textarea the
// web's composer is: the bar keeps its shape from the first frame, every
// chip draws whether or not its data has landed, and the send button carries
// both of its jobs. What the panel omits is what its steps own — the
// approval gate, the plan dock, the todo chip.
import { computed, ref } from 'vue'
import { CircleAlert, FileText, Map } from 'lucide-vue-next'
import { Alert, AlertDescription, AlertTitle } from '@repo/shadcn-vue/components/ui/alert'
import {
  PromptInput,
  PromptInputBody,
  PromptInputFooter,
  PromptInputHeader,
  PromptInputProvider,
  PromptInputSubmit,
} from '@repo/elements/prompt-input'
import { formatProviderError } from '@/lib/providerError'
import {
  formatFirstTokenDuration,
  formatRuntimeStatusLabel,
  formatTransportRetryLine,
  type PendingInputPreview,
  type RuntimeStatus,
} from '@/composables/useChatStream'
import ApprovalPresetPicker from '@/components/chat/ApprovalPresetPicker.vue'
import AutoContinueBanner from '@/components/chat/AutoContinueBanner.vue'
import ComposerPlusMenu from '@/components/chat/ComposerPlusMenu.vue'
import PeerHeldBanner from '@/components/chat/PeerHeldBanner.vue'
import type { PeerHeldRow } from '@/lib/api'
import type { AutoContinueState } from '@/lib/autoContinue'
import ContextRing from '@/components/chat/ContextRing.vue'
import EffortChip from '@/components/chat/EffortChip.vue'
import ModeChip from '@/components/chat/ModeChip.vue'
import ModelChip from '@/components/chat/ModelChip.vue'
import ProjectPill from '@/components/chat/ProjectPill.vue'
import RunStatusLine from '@/components/chat/RunStatusLine.vue'
import ViewModeChip from '@/components/chat/ViewModeChip.vue'
import ForebrainPromptTextarea, { type BotOption } from '@/components/ForebrainPromptTextarea.vue'
import type { ViewMode } from '@/composables/useViewMode'
import PendingInputQueuePopover from '@/components/PendingInputQueuePopover.vue'
import type { ProjectRecord, SessionModelOption, SessionModelResponse, WorkspaceScope } from '@/lib/api'
import type { ProjectTarget } from '@/lib/projectTarget'
import type { ComposerSubmission, PickedFile } from '@/lib/composerSubmission'
import { useI18n, type I18nKey } from '@/locales'

const { t } = useI18n()

const props = withDefaults(defineProps<{
  sessionId: string | null
  mode: string
  isStreaming: boolean
  composerLocked: boolean
  placeholder: string
  submitLabel: string
  submitStatus: 'ready' | 'streaming'
  sessionModelData: SessionModelResponse | null
  sessionEfforts: string[]
  sessionDefaultEffort: string
  scope: WorkspaceScope
  suggestion: string | null
  queueVisible: boolean
  pendingInputPreview: PendingInputPreview
  availableBots: BotOption[]
  botsFetchDone: boolean
  contextUsedPercent: number | null
  composerNotice: { title: string; detail: string; detailCode?: string } | null
  freshChat: boolean
  projects: ProjectRecord[]
  target: ProjectTarget | null
  pillInvalid: boolean
  runtimeStatus: RuntimeStatus
  /** The usage-limit continuation the composer's typing cancels. */
  autoContinue?: AutoContinueState | null
  /** The oldest peer letter waiting to be released, if one is. */
  heldPeer?: PeerHeldRow | null
  /** The session's todo chip: the counts and the unacknowledged updates. */
  todoCountText?: string
  todoTotal?: number
  todoUnread?: number
  todosOpen?: boolean
  /** A drawer is open beside the thread: the chips read compact. */
  compact?: boolean
}>(), {
  autoContinue: null,
  heldPeer: null,
  todoCountText: '0/0',
  todoTotal: 0,
  todoUnread: 0,
  todosOpen: false,
  compact: false,
})

const emit = defineEmits<{
  (e: 'send', message: { text: string }): void
  (e: 'stop'): void
  (e: 'select-mode', mode: string): void
  (e: 'select-effort', effort: string): void
  (e: 'pick-model', option: SessionModelOption): void
  (e: 'model-saved'): void
  (e: 'files-picked', files: PickedFile[]): void
  (e: 'queue-follow-up'): void
  (e: 'edit-last-queued'): void
  (e: 'interrupt-run'): void
  (e: 'typed'): void
  (e: 'escape'): void
  (e: 'suggestion-accepted'): void
  (e: 'insert-plus', text: string): void
  (e: 'pick-target', target: ProjectTarget): void
  (e: 'browse-folder'): void
  (e: 'toggle-todos'): void
  (e: 'open-context'): void
  (e: 'peer-release', held: PeerHeldRow): void
  (e: 'peer-deny', held: PeerHeldRow): void
  (e: 'peer-settled', held: PeerHeldRow): void
}>()

const promptRef = ref<InstanceType<typeof ForebrainPromptTextarea> | null>(null)
const pillRef = ref<InstanceType<typeof ProjectPill> | null>(null)
const approvalPickerRef = ref<InstanceType<typeof ApprovalPresetPicker> | null>(null)

const maxFiles = 10
// The panel reads in the default mode; the chip is here so the bar reads as
// the prototype draws it and the reading mode carries over unchanged.
const viewMode = defineModel<ViewMode>('viewMode', { default: 'default' })

const composerNoticeDetail = computed(() => {
  const notice = props.composerNotice
  if (!notice) return ''
  const code = notice.detailCode?.trim()
  return (code ? formatProviderError({ code }) : null) ?? notice.detail
})

/** The run line above the composer: state, checklist, first-token wait — or
 *  the transport retry's countdown when the link is down. */
const runLine = computed(() => {
  const status = props.runtimeStatus
  if (!status || status.kind === 'idle') return null
  if (status.retry) {
    void status.elapsedMs
    return { ...formatTransportRetryLine(status.retry, t), plan: undefined, tail: '' }
  }
  const { label, plan } = formatRuntimeStatusLabel({ ...status, firstTokenMs: undefined }, t)
  if (!label) return null
  const wait = formatFirstTokenDuration(status.firstTokenMs)
  return { reason: undefined, label, plan, tail: wait ? t('chat.firstToken', { duration: wait }) : '' }
})

const starters: Array<{ icon: unknown; label: I18nKey; action: string }> = [
  { icon: FileText, label: 'chat.starterInit', action: '/init' },
  { icon: Map, label: 'chat.starterPlan', action: '@plan' },
]

function runStarter(action: string) {
  if (action === '@plan') {
    emit('select-mode', 'plan')
    return
  }
  emit('insert-plus', `${action} `)
}

function onSubmit(message: { text: string; files?: unknown[] }) {
  emit('send', { text: message.text ?? '' })
}

defineExpose({
  restoreSubmission: (submission: ComposerSubmission, unsentFiles?: File[]) =>
    promptRef.value?.restoreSubmission(submission, unsentFiles),
  takeAttached: () => promptRef.value?.takeAttached() ?? { attachments: [], mentionImages: [] },
  hasAttached: () => promptRef.value?.hasAttached() ?? false,
  addUploaded: (attachments: Parameters<NonNullable<typeof promptRef.value>['addUploaded']>[0]) =>
    promptRef.value?.addUploaded(attachments),
  focus: () => promptRef.value?.focus(),
  focusPill: () => pillRef.value?.focus(),
  /** A workspace path the workbench's tree handed over: the shared engine
   *  attaches an image and names anything else, exactly as the @ menu does. */
  insertWorkspacePath: (path: string) => promptRef.value?.insertWorkspacePath(path),
  /** What a new chat's approval preset was set to, so its first send can
   *  write it onto the conversation it opens. Taking it clears the draft. */
  takeDraftPreset: () => approvalPickerRef.value?.takeDraft() ?? null,
})
</script>

<style scoped>
.wv-composer {
  flex: none;
  padding: 6px 10px 10px;
  border-top: 1px solid var(--forebrain-divider);
  background: var(--forebrain-bg);
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.wv-composer-relative {
  position: relative;
}
.wv-runline {
  padding: 0 2px;
}
.wv-ctop {
  padding: 8px 10px 0;
}
.wv-starters {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding: 0 2px 2px;
}
.wv-starter {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 999px;
  background: var(--forebrain-surface);
  color: var(--forebrain-text-2);
  font-size: 12px;
  padding: 4px 10px;
  cursor: pointer;
}
.wv-starter:hover {
  border-color: var(--forebrain-brand-1);
  color: var(--forebrain-text);
}
.wv-composer-locked {
  opacity: 0.6;
  pointer-events: none;
}
.wv-todo-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 999px;
  background: var(--forebrain-surface);
  color: var(--forebrain-text-2);
  font-size: 11px;
  padding: 3px 8px;
  cursor: pointer;
  position: relative;
}
.wv-todo-chip--on {
  border-color: var(--forebrain-brand-1);
  color: var(--forebrain-brand-1);
}
.wv-todo-chip-n {
  font-family: var(--forebrain-mono-font);
  font-size: 10px;
}
.wv-todo-chip-badge {
  position: absolute;
  top: -6px;
  right: -6px;
  min-width: 14px;
  height: 14px;
  border-radius: 7px;
  background: var(--forebrain-brand-1);
  color: var(--forebrain-on-brand);
  font-size: 9px;
  line-height: 14px;
  text-align: center;
  padding: 0 3px;
  animation: wv-badge-pulse 1.6s ease-in-out infinite;
}
@keyframes wv-badge-pulse {
  50% { transform: scale(1.12); opacity: 0.85; }
}
@media (prefers-reduced-motion: reduce) {
  .wv-todo-chip-badge { animation: none; }
}
</style>
