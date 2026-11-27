<template>
  <div class="wv-thread" data-testid="webview-thread">
    <!-- The history the gateway still owes: a quiet pulse, and its refusal
         with the same way out the web gives — try again. -->
    <div v-if="historyLoading" class="wv-thread-pulse" data-testid="thread-history-loading" aria-label="…" />

    <div v-else-if="historyError" class="forebrain-err-block" role="alert" data-testid="thread-history-error">
      <CircleAlert class="size-3.5" aria-hidden="true" />
      <div>
        {{ t('chat.historyLoadFailed') }}
        <span class="forebrain-err-s">{{ historyError }}</span>
        <button type="button" class="wv-retry" data-testid="thread-history-retry" @click="emit('retry-history')">
          {{ t('chat.retry') }}
        </button>
      </div>
    </div>

    <div v-if="messages.length || isStreaming" class="wv-thread-list">
      <template v-for="(msg, idx) in messages" :key="msg.id ?? idx">
        <!-- A model that could not take this conversation's images: the notice
             names the model and the way out is the model chip. -->
        <div
          v-if="msg.role === 'notice' && msg.imageRefusal"
          class="forebrain-err-block"
          role="alert"
          data-testid="image-refused-notice"
        >
          <ImageOff class="size-3.5" aria-hidden="true" />
          <div>
            <b>{{ msg.imageRefusal.modelName || msg.imageRefusal.model || '' }}</b>
            {{ t(imageRefusalSentence(msg)) }}
          </div>
        </div>
        <!-- A slash command's reply, or any other UI-only line: a notice card,
             never part of the transcript. A picker card offers its choices. -->
        <div v-else-if="msg.role === 'notice'" class="wv-notice" data-testid="thread-notice">
          <template v-if="msg.content">{{ msg.content }}</template>
          <SlashPickerCard
            v-if="msg.picker"
            :class="msg.content ? 'mt-3' : ''"
            :picker="msg.picker"
            :picked="msg.picked"
            @choose="(value: string) => emit('choose', msg, value)"
          />
        </div>
        <!-- A letter from another agent, or the note about what became of the
             messages this conversation sent: peer rows come before everything
             else, so they never fall into a bubble that is not theirs. -->
        <PeerMessageCard
          v-else-if="msg.role === 'user' && msg.origin === 'peer'"
          :from-name="msg.peerFromName"
          :mark="msg.peerMark"
          :tone="msg.peerTone"
          :body="msg.peerBody"
          :content="msg.content"
          :created-at="msg.createdAt"
        />
        <PeerDeliveryCard v-else-if="msg.role === 'user' && msg.origin === 'peer_status'" :receipts="msg.peerReceipts ?? []" />
        <Message
          v-else-if="messageHasBubble(msg)"
          :from="messageBubbleRole(msg.role)"
          class="max-w-full overflow-visible"
        >
          <MessageContent :class="messageContentClass(msg.role)">
            <div v-if="hasVisibleBlocks(msg.blocks) && drawnBlocks(msg.blocks).length" class="msg-blocks">
              <template v-for="(run, runIdx) in activityRunsOf(msg)" :key="`${msg.id ?? idx}-run-${runIdx}`">
                <ActivityLine v-if="run.line">
                  <template v-for="(block, blockIdx) in run.items" :key="timelineBlockKey(block, blockIdx)">
                    <ActivityStep v-if="block.kind === 'thinking'" node="think">
                      <ThinkingCard :block="block" />
                    </ActivityStep>
                    <ActivityStep v-else-if="block.kind === 'assistant'" node="say" class="al-say">
                      <MessageResponse :content="block.text" />
                    </ActivityStep>
                    <ActivityStep
                      v-else-if="block.kind === 'tool' && !block.step.subagentCall"
                      :node="{ status: statusOf(block.step) }"
                    >
                      <ToolCallCard
                        :step="block.step"
                        :scope="scope"
                        :default-open="false"
                        :pending-action="gatePendingOf(msg, block.step.stepId)"
                      />
                    </ActivityStep>
                    <ActivityStep v-else-if="block.kind === 'tool'" node="subagent">
                      <SubagentCallCard :step="block.step" />
                    </ActivityStep>
                    <ActivityStep v-else-if="block.kind === 'subagent'" node="subagent">
                      <SubagentCallCard :step="block.step" />
                    </ActivityStep>
                    <ActivityStep v-else-if="block.kind === 'approval'" :node="null">
                      <ApprovalCard :block="block" />
                    </ActivityStep>
                  </template>
                </ActivityLine>
                <CompactionCard v-else-if="run.item.kind === 'compaction'" :data="run.item.compaction" />
                <GoalLine v-else-if="run.item.kind === 'goal'" :goal="run.item.goal" />
                <RunErrorBlock
                  v-else-if="run.item.kind === 'error'"
                  :text="run.item.text"
                  :detail="run.item.detail"
                  :session="sessionId ?? ''"
                  :started-at="msg.runStartedAt"
                  :at="failedAtOf(msg)"
                />
              </template>
            </div>
            <!-- What the user sent reads exactly as they wrote it, never as
                 Markdown. A message the runtime sent on their behalf says who. -->
            <div v-if="msg.role === 'user'" class="wv-user">
              <p v-if="originLabel(msg.origin)" data-testid="message-origin" class="wv-user-origin">
                {{ originLabel(msg.origin) }}
              </p>
              <div class="wv-user-bubble">
                <div v-if="String(msg.content ?? '').trim()" class="whitespace-pre-wrap break-words">{{ msg.content }}</div>
                <UserAttachments
                  v-if="msg.attachments?.length"
                  :attachments="msg.attachments"
                  :session-id="sessionId"
                  :row-id="msg.rowId"
                />
              </div>
              <div class="wv-user-acts">
                <button
                  type="button"
                  class="wv-icon-btn"
                  :title="isStreaming ? t('chat.rewindBusy') : t('chat.rewind')"
                  :aria-label="t('chat.rewindAria')"
                  :disabled="isStreaming"
                  data-testid="message-rewind"
                  @click="emit('rewind', msg)"
                >
                  <History class="size-3.5" aria-hidden="true" />
                </button>
                <button
                  type="button"
                  class="wv-icon-btn"
                  :aria-label="t('chat.copyMessage')"
                  data-testid="message-copy"
                  @click="emit('copy-message', msg)"
                >
                  <Copy class="size-3.5" aria-hidden="true" />
                </button>
              </div>
            </div>
            <MessageResponse
              v-else-if="String(msg.content ?? '').trim()"
              :key="`msg-content-${msg.id ?? idx}`"
              :content="msg.content"
            />
            <!-- The turn's own diff, when it made one. -->
            <div
              v-if="msg.role === 'assistant' && msg.turnDiffs?.length"
              class="wv-turn-diff"
            >
              <DiffView
                :files="msg.turnDiffs"
                :summary="msg.turnDiffs[0]?.summary"
                :total-lines="msg.turnDiffs.reduce((s, d) => s + (d.added ?? 0) + (d.deleted ?? 0), 0)"
              />
            </div>
            <!-- 本轮交付物: every file the turn handed back, in one group after
                 the turn's last words. The eye opens it in the editor/OS by
                 its kind — the host decides the route. -->
            <ArtifactList
              v-if="deliverablesOf(msg).length"
              :artifacts="deliverablesOf(msg)"
              :label="deliverableLabelOf(msg)"
              @preview="(artifact: ArtifactView) => emit('open-deliverable', artifact)"
            />
            <!-- The run's closing line, in the turn's column: how long it
                 worked and what the plan reached, the web's own wording. -->
            <RunWorkedLine class="ml-8" :line="workedLineOf(msg, t)" />
          </MessageContent>
        </Message>
      </template>
    </div>
    <!-- The thread's tail still holds what no card of this conversation
         carries — the agent's questions, and the requests with no card of
         their own (an old session's rows, a gate with no card). The gates
         that belong to calls hang on their cards above. -->
    <ApprovalCard
      :anchored-action-ids="anchoredActionIds"
      @focus-composer="emit('focus-composer')"
    />
    <!-- A send the gateway refused because the conversation runs somewhere
         else: the thread says why, the words wait in the composer. -->
    <div v-if="busyNotice" class="forebrain-err-block" role="alert" data-testid="busy-notice">
      <CircleAlert class="size-3.5" aria-hidden="true" />
      <div>{{ busyNoticeText }}</div>
    </div>
    <Alert v-if="error" variant="destructive" data-testid="thread-error">
      <CircleAlert class="size-4" />
      <AlertTitle>{{ t('chat.requestFailed') }}</AlertTitle>
      <AlertDescription class="whitespace-pre-line">{{ error }}</AlertDescription>
    </Alert>
  </div>
</template>

<script setup lang="ts">
// The conversation thread, assembled from the same pieces the web's chat
// view assembles — the timeline useChatStream projects, the activity-line
// grouping both surfaces share, and the same cards. What the panel does not
// draw here is what its steps own: the todo card, the deliverable group and
// the rewind affordances arrive with their steps.
import { computed } from 'vue'
import { CircleAlert, Copy, History, ImageOff } from 'lucide-vue-next'
import { Alert, AlertDescription, AlertTitle } from '@repo/shadcn-vue/components/ui/alert'
import { Message, MessageContent, MessageResponse } from '@repo/elements/message'
import ActivityLine from '@/components/chat/ActivityLine.vue'
import ActivityStep from '@/components/chat/ActivityStep.vue'
import ApprovalCard from '@/components/chat/ApprovalCard.vue'
import CompactionCard from '@/components/chat/CompactionCard.vue'
import DiffView from '@/components/DiffView.vue'
import GoalLine from '@/components/chat/GoalLine.vue'
import PeerDeliveryCard from '@/components/chat/PeerDeliveryCard.vue'
import PeerMessageCard from '@/components/chat/PeerMessageCard.vue'
import RunErrorBlock from '@/components/chat/RunErrorBlock.vue'
import ArtifactList from '@/components/chat/ArtifactList.vue'
import RunWorkedLine from '@/components/chat/RunWorkedLine.vue'
import type { ArtifactView } from '@/lib/artifacts'
import {
  artifactsOfPresentFiles,
  displayPathOf,
  planArtifactOf,
  turnDeliverables,
  turnDiffTotals,
} from '@/lib/artifacts'
import SlashPickerCard from '@/components/chat/SlashPickerCard.vue'
import SubagentCallCard from '@/components/chat/SubagentCallCard.vue'
import ThinkingCard from '@/components/chat/ThinkingCard.vue'
import ToolCallCard, { statusOf } from '@/components/chat/ToolCallCard.vue'
import UserAttachments from '@/components/chat/UserAttachments.vue'
import {
  hasVisibleBlocks,
  stepHoldsNoCard,
  stoppedCardSaysNothing,
  timelineBlockKey,
  workedLineOf,
  type ChatMessage,
  type TimelineBlock,
} from '@/composables/useChatStream'
import { blockOnActivityLine, groupActivityRuns } from '@/lib/activityRuns'
import { blockVisibleIn } from '@/lib/viewMode'
import { formatProviderError } from '@/lib/providerError'
import { injectPendingActions } from '@/composables/providePendingActions'
import type { WorkspaceScope } from '@/lib/api'
import type { ViewMode } from '@/composables/useViewMode'
import { currentLocale, t } from '@/locales'

const props = defineProps<{
  messages: ChatMessage[]
  sessionId: string | null
  isStreaming: boolean
  error: string | null
  historyLoading: boolean
  historyError: string | null
  scope: WorkspaceScope
  /** The refusal code when the conversation runs somewhere else; '' is none. */
  busyNotice?: string
  /** The reading mode of this view — the same per-session choice the web
   *  thread keeps (useViewMode), wired through by the shell. */
  viewMode: ViewMode
  /** This conversation's delivered files, as the gateway listed them. */
  sessionArtifacts: ArtifactView[]
}>()

const emit = defineEmits<{
  (e: 'choose', message: ChatMessage, value: string): void
  (e: 'copy-message', message: ChatMessage): void
  (e: 'rewind', message: ChatMessage): void
  (e: 'retry-history'): void
  (e: 'focus-composer'): void
  (e: 'open-deliverable', artifact: ArtifactView): void
}>()

const drawnBlocks = (blocks?: ChatMessage['blocks']): NonNullable<ChatMessage['blocks']> => {
  return (blocks ?? []).filter((block) => blockVisibleIn(props.viewMode, block.kind)
    && !(block.kind === 'tool' && (stepHoldsNoCard(block.step) || stoppedCardSaysNothing(block.step))))
}

const activityRunsOf = (msg: ChatMessage) => groupActivityRuns(drawnBlocks(msg.blocks), blockOnActivityLine)

// The conversation's one pending-actions store: gates decide through the same
// loaded requests the tail surface reads (plan 138). A thread mounted outside
// a provider — a bare test harness — simply carries no gates.
const pendingActions = injectPendingActions()

/** The request a tool card is parked on: the timeline's approval block names
 *  the call the gate holds, and the store's pending list says whether it is
 *  still asking. Null on every settled call. */
function gatePendingOf(msg: ChatMessage, stepId: string) {
  if (!pendingActions) return null
  const blocks = (msg.blocks ?? []).filter((block): block is Extract<TimelineBlock, { kind: 'approval' }> => block.kind === 'approval')
  return pendingActions.pendingForStep(stepId, blocks)
}

/** The ids whose gates hang on a tool card here, so the tail's own fallback
 *  list holds every request that is not one of them. */
const anchoredActionIds = computed(() => {
  const ids = new Set<string>()
  if (!pendingActions) return ids
  for (const message of props.messages) {
    for (const block of message.blocks ?? []) {
      if (block.kind !== 'tool') continue
      const found = gatePendingOf(message, block.step.stepId)
      if (found) ids.add(found.id)
    }
  }
  return ids
})

// ---- 本轮交付物 (plan 153 A7): the files the turn handed back ----
const artifactsByRun = computed(() => {
  const byRun = new Map<string, ArtifactView[]>()
  for (const artifact of props.sessionArtifacts) {
    const runId = String(artifact.runId ?? '').trim()
    if (!runId) continue
    const rows = byRun.get(runId) ?? []
    rows.push(artifact)
    byRun.set(runId, rows)
  }
  return byRun
})

function deliverablesOf(msg: ChatMessage): ArtifactView[] {
  if (msg.role !== 'assistant') return []
  if (msg.completedNormally === false) return []
  const sid = String(props.sessionId ?? '').trim()
  const presented: ArtifactView[] = []
  const plan: ArtifactView[] = []
  for (const block of msg.blocks ?? []) {
    if (block.kind !== 'tool') continue
    const name = String(block.step.toolName ?? '').trim()
    if (name === 'present_files') {
      presented.push(...artifactsOfPresentFiles(block.step.result, sid))
      continue
    }
    if (name === 'exit_plan_mode') {
      const kept = planArtifactOf(block.step.result, sid)
      if (kept) plan.push(kept)
    }
  }
  const runId = String(msg.runId ?? '').trim()
  const fallback = runId ? artifactsByRun.value.get(runId) ?? [] : []
  const totals = turnDiffTotals(msg.turnDiffs, undefined)
  return turnDeliverables({
    presented,
    plan,
    frame: msg.artifacts ?? [],
    fallback,
  }, undefined).map((row) => {
    const displayPath = displayPathOf(row, undefined)
    const counts = totals.get(displayPath)
    return {
      ...row,
      displayPath,
      ...(counts ? { added: counts.added, deleted: counts.deleted } : {}),
    }
  })
}

function deliverableLabelOf(msg: ChatMessage): string {
  const rows = deliverablesOf(msg)
  const onlyPlan = rows.length > 0 && rows.every((row) => row.origin === 'plan')
  return onlyPlan ? t('chat.artifactPlanGroup') : t('chat.artifactGroup')
}

function failedAtOf(msg: ChatMessage): number {
  const finished = msg.runFinishedAt ? Date.parse(msg.runFinishedAt) : Number.NaN
  if (!Number.isNaN(finished)) return finished
  return msg.createdAt ?? Date.now()
}

function messageBubbleRole(role: string): 'user' | 'assistant' {
  return role === 'user' ? 'user' : 'assistant'
}

/** A row with only a tool call carries no text: its card is what it means. */
function messageHasBubble(msg: ChatMessage): boolean {
  if (String(msg.content ?? '').trim()) return true
  if (msg.attachments?.length) return true
  if (msg.role !== 'assistant') return false
  if (hasVisibleBlocks(msg.blocks) && drawnBlocks(msg.blocks).length) return true
  return Boolean(msg.turnDiffs?.length)
}

function messageContentClass(role: string): string | undefined {
  if (role === 'user') {
    return 'gap-0 group-[.is-user]:rounded-none group-[.is-user]:bg-transparent group-[.is-user]:px-0 group-[.is-user]:py-0'
  }
  return 'w-full min-w-0 max-w-full overflow-visible break-words gap-[10px] text-[13px] leading-[1.55] [&_pre]:max-w-full [&_pre]:overflow-x-auto [&_table]:block [&_table]:max-w-full [&_table]:overflow-x-auto'
}

function originLabel(origin?: string): string {
  if (origin === 'heartbeat') return t('chat.originHeartbeat')
  if (origin === 'cron') return t('chat.originCron')
  return ''
}

function imageRefusalSentence(msg: { imageRefusal?: { kind?: 'unsupported' | 'rejected' | 'remembered' } }): 'chat.imageRefusedLine' | 'chat.imageRejectedNow' | 'chat.imageRejectedRemembered' {
  switch (msg.imageRefusal?.kind ?? 'unsupported') {
    case 'rejected':
      return 'chat.imageRejectedNow'
    case 'remembered':
      return 'chat.imageRejectedRemembered'
    default:
      return 'chat.imageRefusedLine'
  }
}

const busyNoticeText = computed(() =>
  props.busyNotice ? (formatProviderError({ code: props.busyNotice }, currentLocale.value) ?? '') : '')
</script>

<style scoped>
.wv-thread {
  display: flex;
  flex-direction: column;
  gap: 16px;
  padding: 12px 14px 8px;
  min-height: 0;
}
.wv-thread-list {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.wv-thread-state {
  color: var(--forebrain-muted-text);
  font-size: 12px;
  padding: 8px 2px;
}
.wv-thread-pulse {
  width: 22px;
  height: 6px;
  border-radius: 3px;
  background: var(--forebrain-divider);
  animation: wv-pulse 1.2s ease-in-out infinite;
}
@keyframes wv-pulse {
  50% { opacity: 0.35; }
}
@media (prefers-reduced-motion: reduce) {
  .wv-thread-pulse { animation: none; }
}
.wv-notice {
  margin: 0 auto;
  max-width: 46rem;
  border: 1px solid var(--forebrain-divider);
  border-radius: 0.5rem;
  background: var(--forebrain-surface);
  padding: 10px 14px;
  font-size: 13px;
  color: var(--forebrain-text);
  white-space: pre-wrap;
  word-break: break-word;
}
.wv-user {
  position: relative;
}
.wv-user-origin {
  margin: 0 0 2px;
  font-size: 11px;
  color: var(--forebrain-muted-text);
}
.wv-user-bubble {
  background: var(--forebrain-bg-alt);
  border: 1px solid var(--forebrain-divider);
  border-radius: 0.5rem;
  padding: 8px 12px;
  font-size: 13px;
  line-height: 1.55;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.wv-user-acts {
  position: absolute;
  right: 0;
  bottom: -20px;
  display: none;
}
.wv-user:hover .wv-user-acts,
.wv-user-acts:focus-within {
  display: inline-flex;
  gap: 4px;
}
.wv-icon-btn {
  display: inline-grid;
  place-items: center;
  width: 22px;
  height: 22px;
  border-radius: 5px;
  border: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
  color: var(--forebrain-text-2);
  cursor: pointer;
}
.wv-retry {
  border: 0;
  background: none;
  color: var(--forebrain-brand-1);
  cursor: pointer;
  font: inherit;
  padding: 0;
  text-decoration: underline;
}
.wv-turn-diff {
  margin-top: 10px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 0.5rem;
  background: var(--forebrain-surface);
  padding: 10px;
}
</style>
