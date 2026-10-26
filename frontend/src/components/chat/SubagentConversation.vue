<template>
  <section class="space-y-4" :aria-label="t('chat.subagentViewAria')">
    <div class="space-y-4">
      <template v-for="(run, runIdx) in runs" :key="runIdx">
        <!-- The turn's own action cards hang on one activity line, the same
             line the conversation draws (owner 2026-10-25); here the speaker
             is the robot, so the say node is the robot on the brand tint. -->
        <ActivityLine v-if="run.line">
          <template v-for="(block, idx) in run.items" :key="block.id || (block.kind === 'tool' ? block.step.stepId : `${record.agentId}-block-${idx}`)">
            <ActivityStep v-if="block.kind === 'thinking'" node="think">
              <ThinkingCard :block="block" />
            </ActivityStep>
            <ActivityStep v-else-if="block.kind === 'assistant'" node="sub-say" class="al-say">
              <MessageResponse :content="block.text" />
            </ActivityStep>
            <!-- The node names the call's state; the head carries the words. -->
            <ActivityStep
              v-else-if="block.kind === 'tool' && !block.step.subagentCall"
              :node="{ status: statusOf(block.step) }"
            >
              <ToolCallCard
                :step="block.step"
                :scope="scope"
                :agent-id="record.agentId"
                :open="foldOpen(blockFoldId(block, run.start + idx), false)"
                :pending-action="gateOf(block.step.stepId)"
                @update:open="updateFold(blockFoldId(block, run.start + idx), $event)"
                @open-doc="(tab) => emit('open-doc', tab)"
                @open-agent="(agentId) => emit('open-agent', agentId)"
              />
            </ActivityStep>
            <!-- A subagent_* call this agent made is the same card the
                 conversation draws — here is where a nested dispatch belongs,
                 never in the main conversation. -->
            <ActivityStep v-else-if="block.kind === 'tool'" node="subagent">
              <SubagentCallCard
                :step="block.step"
                :live="callTasks?.get(block.step.stepId)"
                :now="now"
                :active-agent-id="activeAgentId"
                @open="emit('open-agent', $event)"
              />
            </ActivityStep>
            <ActivityStep v-else-if="block.kind === 'subagent'" node="subagent">
              <SubagentCallCard
                :step="block.step"
                :live="callTasks?.get(block.stepId)"
                :now="now"
                :active-agent-id="activeAgentId"
                @open="emit('open-agent', $event)"
              />
            </ActivityStep>
            <ActivityStep v-else-if="block.kind === 'plan'" node="todo">
              <PlanUpdateCard :plan="block.plan" />
            </ActivityStep>
            <!-- A settled approval is one line on the line, with no node. -->
            <ActivityStep v-else-if="block.kind === 'approval'" :node="null">
              <ApprovalCard :block="block" />
            </ActivityStep>
          </template>
        </ActivityLine>

        <!-- Everything else keeps its own place: the task it was handed, the
             user's own words in this tab, the cards the lifecycle owns. -->
        <template v-else>
          <div
            v-if="run.item.kind === 'prompt'"
            class="subagent-task"
            :class="{ 'is-open': taskOpen(run.item, run.start) }"
            data-testid="subagent-task-card"
          >
            <div class="subagent-task-cap">{{ t('chat.subagentTaskCap') }}</div>
            <p class="subagent-task-body">{{ run.item.text }}</p>
            <button
              type="button"
              class="subagent-task-more"
              :aria-expanded="taskOpen(run.item, run.start)"
              :aria-label="taskOpen(run.item, run.start) ? t('chat.subagentTaskCollapse') : t('chat.subagentTaskExpand')"
              data-testid="subagent-task-toggle"
              @click="toggleTask(run.item, run.start)"
            >
              <ChevronUp v-if="taskOpen(run.item, run.start)" aria-hidden="true" />
              <ChevronDown v-else aria-hidden="true" />
            </button>
          </div>

          <!-- A message the user sent this subagent from its own view: the user's
               own message, not the dispatching agent's instruction. -->
          <Message v-else-if="run.item.kind === 'user'" from="user" data-testid="subagent-user-message">
            <MessageContent class="whitespace-pre-wrap">{{ run.item.text }}</MessageContent>
            <Avatar class="size-8 shrink-0 ring-1 ring-border">
              <AvatarFallback class="bg-muted">
                <User class="size-4 text-muted-foreground" />
              </AvatarFallback>
            </Avatar>
          </Message>

          <CompactionCard
            v-else-if="run.item.kind === 'compaction'"
            :data="run.item.compaction"
          />

          <GoalLine
            v-else-if="run.item.kind === 'goal'"
            :goal="run.item.goal"
          />

          <RunWorkedLine
            v-else-if="run.item.kind === 'worked'"
            :line="formatWorkedDurationLabel(run.item.durationMs, t, run.item.finishedAt, run.item.plan)"
          />

          <RunErrorBlock
            v-else-if="run.item.kind === 'error'"
            :text="run.item.text"
            :detail="run.item.detail"
          />
        </template>
      </template>

      <p
        v-if="!isRunning && !hasAnswer"
        class="px-1 text-[13px] text-[var(--forebrain-muted-text)]"
      >
        {{ t('chat.subagentNoAnswer') }}
      </p>

      <!-- This agent's own requests no card of its transcript carries (plan
           138): a gate with no card — the exit-plan gate above all — waits
           here, in the waiting card's own frame, at its flow's end. -->
      <ApprovalCard
        :agent-id="record.agentId"
        :anchored-action-ids="anchoredActionIds"
        can-open-agent
        @open-agent="(agentId) => emit('open-agent', agentId)"
      />
    </div>

    <Alert v-if="record.error" variant="destructive">
      <CircleAlert class="size-4" />
      <AlertTitle>{{ t('chat.subagentFailed') }}</AlertTitle>
      <AlertDescription class="whitespace-pre-wrap">{{ record.error }}</AlertDescription>
    </Alert>
  </section>
</template>

<script setup lang="ts">
/**
 * One subagent's conversation, drawn with the same message primitives as the
 * main thread so its tab reads like a channel rather than a debug panel. The
 * blocks arrive in the order the subagent produced them — its thinking, its
 * text, and the calls it made, interleaved — which is the same record the
 * terminal keeps on that subagent's own screen.
 *
 * It draws the transcript and nothing about the execution's state: the task it
 * was handed opens it as a collapsed card, and the model, the context budget,
 * the clock and the checklist all live in the composer bar of the tab that
 * holds it (SubagentTab).
 */
import { computed } from 'vue'
import { ChevronDown, ChevronUp, CircleAlert, User } from 'lucide-vue-next'
import { Message, MessageContent, MessageResponse } from '@repo/elements/message'
import { Alert, AlertDescription, AlertTitle } from '@repo/shadcn-vue/components/ui/alert'
import { Avatar, AvatarFallback } from '@repo/shadcn-vue/components/ui/avatar'
import ActivityLine from '@/components/chat/ActivityLine.vue'
import ActivityStep from '@/components/chat/ActivityStep.vue'
import RunWorkedLine from '@/components/chat/RunWorkedLine.vue'
import ThinkingCard from '@/components/chat/ThinkingCard.vue'
import PlanUpdateCard from '@/components/chat/PlanUpdateCard.vue'
import CompactionCard from '@/components/chat/CompactionCard.vue'
import GoalLine from '@/components/chat/GoalLine.vue'
import ToolCallCard, { statusOf } from '@/components/chat/ToolCallCard.vue'
import SubagentCallCard from '@/components/chat/SubagentCallCard.vue'
import ApprovalCard from '@/components/chat/ApprovalCard.vue'
import RunErrorBlock from '@/components/chat/RunErrorBlock.vue'
import { formatWorkedDurationLabel, type SubagentCallTaskLive, type TimelineBlock, type SubagentTranscript } from '@/composables/useChatStream'
import { injectPendingActions } from '@/composables/providePendingActions'
import type { ActionRow } from '@/composables/usePendingActions'
import type { DocTab } from '@/composables/useDocTabs'
import { groupActivityRuns, blockOnActivityLine } from '@/lib/activityRuns'
import { subagentPhase } from '@/lib/subagentStatus'
import { blockVisibleIn } from '@/lib/viewMode'
import type { ViewMode } from '@/composables/useViewMode'
import type { WorkspaceScope } from '@/lib/api'
import { t } from '@/locales'

const props = withDefaults(defineProps<{
  record: SubagentTranscript
  foldState?: Record<string, boolean>
  /** The live bindings of the subagent_* cards this transcript draws. */
  callTasks?: ReadonlyMap<string, ReadonlyMap<number, SubagentCallTaskLive>>
  /** The stream's shared clock, so the cards' walking rows tick with it. */
  now?: number
  /** The file view an image step's thumbnail is fetched from, if any. */
  scope?: WorkspaceScope
  /** The tab the drawer has in front, so a nested dispatch opens there marked. */
  activeAgentId?: string
  /** How much of this run the tab draws — its own reading mode. */
  viewMode?: ViewMode
}>(), {
  foldState: () => ({}),
  callTasks: undefined,
  now: undefined,
  scope: undefined,
  activeAgentId: '',
  viewMode: 'default',
})
const emit = defineEmits<{
  (e: 'fold-change', value: { id: string; open: boolean }): void
  (e: 'open-agent', agentId: string): void
  (e: 'open-doc', tab: DocTab): void
}>()

const hasAnswer = computed(() => props.record.blocks.some((block) => block.kind === 'assistant'))

/**
 * drawnBlocks is what this tab draws under its reading mode: the transcript
 * minus the cards concise withholds. The record itself is untouched — the
 * drawer, the workbench and the terminal still hold the whole run.
 */
const drawnBlocks = computed(() => props.record.blocks.filter((block) => blockVisibleIn(props.viewMode, block.kind)))

/** The turn's cards folded into activity-line runs, each run carrying where
 *  it starts in the drawn list so a block's fold id stays the one its
 *  persisted state was written under. */
const runs = computed(() => {
  let offset = 0
  return groupActivityRuns(drawnBlocks.value, blockOnActivityLine).map((run) => {
    const start = offset
    offset += run.line ? run.items.length : 1
    return { ...run, start }
  })
})

const isRunning = computed(() => subagentPhase(props.record.status) === 'active')

/** The conversation's own pending-actions store: a gate this subagent raised
 *  hangs on its own call's card here, the same way the conversation's do. */
const pending = injectPendingActions()

/** This transcript's approval blocks: the gate records that name a call. */
const approvalBlocks = computed(() => props.record.blocks.filter(
  (block): block is Extract<TimelineBlock, { kind: 'approval' }> => block.kind === 'approval',
))

/** The pending request one of this subagent's calls is parked on. */
function gateOf(stepId: string): ActionRow | null {
  return pending?.pendingForStep(stepId, approvalBlocks.value) ?? null
}

/** The ids whose gates hang on a card of this transcript; the tail below
 *  answers this agent's requests that are not one of them. */
const anchoredActionIds = computed(() => {
  if (!pending) return []
  const ids: string[] = []
  for (const block of props.record.blocks) {
    if (block.kind !== 'tool') continue
    const found = gateOf(block.step.stepId)
    if (found) ids.push(found.id)
  }
  return ids
})

function blockFoldId(block: TimelineBlock, index: number): string {
  if (block.id) return `${block.kind}:${block.id}`
  if (block.kind === 'tool' && block.step.stepId) return `tool:${block.step.stepId}`
  return `${block.kind}:${index}`
}

function foldOpen(id: string, fallback: boolean): boolean {
  return Object.prototype.hasOwnProperty.call(props.foldState, id)
    ? Boolean(props.foldState[id])
    : fallback
}

function updateFold(id: string, open: boolean) {
  emit('fold-change', { id, open })
}

/** The task card starts collapsed — its frame is what matters, not the whole brief. */
function taskOpen(block: TimelineBlock, index: number): boolean {
  return foldOpen(blockFoldId(block, index), false)
}

function toggleTask(block: TimelineBlock, index: number) {
  updateFold(blockFoldId(block, index), !taskOpen(block, index))
}
</script>

<style scoped>
/* The prototype's .al-say: the subagent's own words, a size up from the cards
   (14px / 1.75), plain text on the line. */
.al-say {
  font-size: 14px;
  line-height: 1.75;
  color: var(--forebrain-text);
}
</style>

<style scoped>
/* The prototype's .sa-task: the frame around the transcript, cut to a few
   lines with a fade until the reader opens it. */
.subagent-task {
  position: relative;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-bg-alt);
  padding: 10px 12px;
  font-size: 12.5px;
  color: var(--forebrain-text-2);
}

.subagent-task-cap {
  font-size: 11px;
  color: var(--forebrain-muted-text);
  margin-bottom: 4px;
}

.subagent-task-body {
  margin: 0;
  white-space: pre-wrap;
}

.subagent-task:not(.is-open) .subagent-task-body {
  max-height: 76px;
  overflow: hidden;
  -webkit-mask-image: linear-gradient(#000 55%, transparent);
  mask-image: linear-gradient(#000 55%, transparent);
}

.subagent-task-more {
  position: absolute;
  left: 50%;
  bottom: -12px;
  margin-left: -12px;
  width: 24px;
  height: 24px;
  border-radius: 50%;
  border: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
  color: var(--forebrain-muted-text);
  display: grid;
  place-items: center;
  cursor: pointer;
}

.subagent-task-more:hover {
  color: var(--forebrain-text);
}

.subagent-task-more svg {
  width: 14px;
  height: 14px;
}
</style>
