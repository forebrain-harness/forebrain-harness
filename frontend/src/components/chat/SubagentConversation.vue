<template>
  <section class="space-y-4" :aria-label="t('chat.subagentViewAria')">
    <div class="space-y-4">
	  <template v-for="(block, idx) in drawnBlocks" :key="block.id || (block.kind === 'tool' ? block.step.stepId : `${record.agentId}-block-${idx}`)">
        <!-- The prompt the dispatching agent gave it, collapsed to a card the
             reader can open — it is the frame around everything below, not a
             message someone typed here. -->
        <div
          v-if="block.kind === 'prompt'"
          class="subagent-task"
          :class="{ 'is-open': taskOpen(block, idx) }"
          data-testid="subagent-task-card"
        >
          <div class="subagent-task-cap">{{ t('chat.subagentTaskCap') }}</div>
          <p class="subagent-task-body">{{ block.text }}</p>
          <button
            type="button"
            class="subagent-task-more"
            :aria-expanded="taskOpen(block, idx)"
            :aria-label="taskOpen(block, idx) ? t('chat.subagentTaskCollapse') : t('chat.subagentTaskExpand')"
            data-testid="subagent-task-toggle"
            @click="toggleTask(block, idx)"
          >
            <ChevronUp v-if="taskOpen(block, idx)" aria-hidden="true" />
            <ChevronDown v-else aria-hidden="true" />
          </button>
        </div>

        <!-- A message the user sent this subagent from its own view: the user's
             own message, not the dispatching agent's instruction. -->
        <Message v-else-if="block.kind === 'user'" from="user" data-testid="subagent-user-message">
          <MessageContent class="whitespace-pre-wrap">{{ block.text }}</MessageContent>
          <Avatar class="size-8 shrink-0 ring-1 ring-border">
            <AvatarFallback class="bg-muted">
              <User class="size-4 text-muted-foreground" />
            </AvatarFallback>
          </Avatar>
        </Message>

        <!-- The same thinking card the conversation and the workshop draw: one
             caption that folds the prose away, closed by default. -->
        <ThinkingCard v-else-if="block.kind === 'thinking'" :block="block" />

        <ToolCallCard
          v-else-if="block.kind === 'tool' && !block.step.subagentCall"
          :step="block.step"
          :scope="scope"
          :open="foldOpen(blockFoldId(block, idx), false)"
          @update:open="updateFold(blockFoldId(block, idx), $event)"
        />

        <!-- A subagent_* call this agent made is the same card the
             conversation draws — here is where a nested dispatch belongs,
             never in the main conversation. -->
        <SubagentCallCard
          v-else-if="block.kind === 'tool'"
          :step="block.step"
          :live="callTasks?.get(block.step.stepId)"
          :now="now"
          :active-agent-id="activeAgentId"
          @open="emit('open-agent', $event)"
        />

        <SubagentCallCard
          v-else-if="block.kind === 'subagent'"
          :step="block.step"
          :live="callTasks?.get(block.stepId)"
          :now="now"
          :active-agent-id="activeAgentId"
          @open="emit('open-agent', $event)"
        />

        <PlanUpdateCard
          v-else-if="block.kind === 'plan'"
          :plan="block.plan"
        />

		<ApprovalCard
		  v-else-if="block.kind === 'approval'"
		  :block="block"
		/>

		<CompactionCard
		  v-else-if="block.kind === 'compaction'"
		  :data="block.compaction"
		/>

		<GoalLine
		  v-else-if="block.kind === 'goal'"
		  :goal="block.goal"
		/>

		<RunWorkedLine
		  v-else-if="block.kind === 'worked'"
		  :line="formatWorkedDurationLabel(block.durationMs, t, block.finishedAt, block.plan)"
		/>

		<RunErrorBlock
		  v-else-if="block.kind === 'error'"
		  :text="block.text"
		  :detail="block.detail"
		/>

        <Message v-else-if="block.kind === 'assistant'" from="assistant">
          <Avatar class="size-8 shrink-0 ring-1 ring-border">
            <AvatarFallback class="bg-muted">
              <Bot class="size-4 text-muted-foreground" />
            </AvatarFallback>
          </Avatar>
          <MessageContent>
            <MessageResponse :content="block.text" />
          </MessageContent>
        </Message>
      </template>

      <p
        v-if="!isRunning && !hasAnswer"
        class="px-1 text-[13px] text-[var(--forebrain-muted-text)]"
      >
        {{ t('chat.subagentNoAnswer') }}
      </p>
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
import { Bot, ChevronDown, ChevronUp, CircleAlert, User } from 'lucide-vue-next'
import { Message, MessageContent, MessageResponse } from '@repo/elements/message'
import { Alert, AlertDescription, AlertTitle } from '@repo/shadcn-vue/components/ui/alert'
import { Avatar, AvatarFallback } from '@repo/shadcn-vue/components/ui/avatar'
import RunWorkedLine from '@/components/chat/RunWorkedLine.vue'
import ThinkingCard from '@/components/chat/ThinkingCard.vue'
import PlanUpdateCard from '@/components/chat/PlanUpdateCard.vue'
import CompactionCard from '@/components/chat/CompactionCard.vue'
import GoalLine from '@/components/chat/GoalLine.vue'
import ToolCallCard from '@/components/chat/ToolCallCard.vue'
import SubagentCallCard from '@/components/chat/SubagentCallCard.vue'
import ApprovalCard from '@/components/chat/ApprovalCard.vue'
import RunErrorBlock from '@/components/chat/RunErrorBlock.vue'
import { formatWorkedDurationLabel, type SubagentCallTaskLive, type TimelineBlock, type SubagentTranscript } from '@/composables/useChatStream'
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
}>()

const hasAnswer = computed(() => props.record.blocks.some((block) => block.kind === 'assistant'))

/**
 * drawnBlocks is what this tab draws under its reading mode: the transcript
 * minus the cards concise withholds. The record itself is untouched — the
 * drawer, the workbench and the terminal still hold the whole run.
 */
const drawnBlocks = computed(() => props.record.blocks.filter((block) => blockVisibleIn(props.viewMode, block.kind)))

const isRunning = computed(() => subagentPhase(props.record.status) === 'active')

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
