<template>
  <section class="space-y-4" :aria-label="t('chat.subagentViewAria')">
    <header class="flex items-start justify-between gap-3 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-2">
      <div class="min-w-0">
        <div class="flex min-w-0 items-center gap-2">
          <button
            type="button"
            class="forebrain-btn forebrain-btn-ghost h-7 shrink-0 gap-1 px-2 text-xs"
            @click="$emit('back')"
          >
            <ArrowLeft class="size-3.5" />
            {{ t('chat.agentViewBack') }}
          </button>
          <span class="truncate text-sm font-medium text-[var(--forebrain-text)]">
            {{ record.title || record.agentType || t('agents.subagent') }}
          </span>
          <span
            class="shrink-0 rounded-full border px-2 py-0.5 text-[11px]"
            :class="statusClass"
          >
            {{ statusLabel }}
          </span>
        </div>
        <div class="mt-1 truncate text-[11px] text-[var(--forebrain-muted-text)]">{{ record.agentId }}</div>
      </div>
      <div class="flex shrink-0 items-center gap-3 text-[11px] text-[var(--forebrain-muted-text)]">
        <span v-if="modelLabel">{{ modelLabel }}</span>
        <!-- This subagent's own context window, the terminal's footer gauge for
             its view: how much of the window it runs on is used. -->
        <span v-if="budgetLabel" data-testid="subagent-budget">{{ budgetLabel }}</span>
        <span v-if="record.inputTokens || record.outputTokens">
          {{ t('chat.subagentTokens', { input: record.inputTokens, output: record.outputTokens }) }}
        </span>
      </div>
    </header>

    <div class="space-y-4">
	  <template v-for="(block, idx) in record.blocks" :key="block.id || (block.kind === 'tool' ? block.step.stepId : `${record.agentId}-block-${idx}`)">
        <!-- The prompt the dispatching agent gave it, which opens the
             transcript exactly as it does in the terminal. -->
        <Message v-if="block.kind === 'prompt'" from="user">
          <MessageContent class="whitespace-pre-wrap">{{ block.text }}</MessageContent>
          <Avatar class="size-8 shrink-0 ring-1 ring-border">
            <AvatarFallback class="bg-muted">
              <Bot class="size-4 text-muted-foreground" />
            </AvatarFallback>
          </Avatar>
        </Message>

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

        <details
          v-else-if="block.kind === 'thinking'"
          class="rounded-lg border border-[var(--forebrain-divider)] px-3 py-2"
          :open="foldOpen(blockFoldId(block, idx), false)"
          @toggle="updateNativeFold(blockFoldId(block, idx), $event)"
        >
          <summary class="cursor-pointer text-[11px] text-[var(--forebrain-muted-text)]">
            {{ thinkingLabel(block) }}
          </summary>
          <p class="mt-2 whitespace-pre-wrap text-[12px] leading-relaxed text-[var(--forebrain-muted-text)]">{{ block.text }}</p>
        </details>

        <ToolCallCard
          v-else-if="block.kind === 'tool' && !block.step.subagentCall"
          :step="block.step"
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
          @open="emit('open-agent', $event)"
        />

        <SubagentCallCard
          v-else-if="block.kind === 'subagent'"
          :step="block.step"
          :live="callTasks?.get(block.stepId)"
          :now="now"
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

      <div
		v-if="isRunning"
        class="flex items-center gap-2 px-1 text-[12px] text-[var(--forebrain-muted-text)]"
      >
        <span class="forebrain-working-dot" />
        {{ t('chat.subagentWorking', { duration: workingDuration }) }}
        <PlanProgressSegments :plan="record.plan" />
      </div>
      <p
        v-else-if="!hasAnswer"
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
 * main thread so switching between them feels like changing channel rather than
 * opening a debug panel. The blocks arrive in the order the subagent produced
 * them — its thinking, its text, and the calls it made, interleaved — which is
 * the same record the terminal keeps on that subagent's own screen.
 */
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { ArrowLeft, Bot, CircleAlert, User } from 'lucide-vue-next'
import { Message, MessageContent, MessageResponse } from '@repo/elements/message'
import { Alert, AlertDescription, AlertTitle } from '@repo/shadcn-vue/components/ui/alert'
import { Avatar, AvatarFallback } from '@repo/shadcn-vue/components/ui/avatar'
import PlanProgressSegments from '@/components/chat/PlanProgressSegments.vue'
import RunWorkedLine from '@/components/chat/RunWorkedLine.vue'
import PlanUpdateCard from '@/components/chat/PlanUpdateCard.vue'
import CompactionCard from '@/components/chat/CompactionCard.vue'
import GoalLine from '@/components/chat/GoalLine.vue'
import ToolCallCard from '@/components/chat/ToolCallCard.vue'
import SubagentCallCard from '@/components/chat/SubagentCallCard.vue'
import ApprovalCard from '@/components/chat/ApprovalCard.vue'
import RunErrorBlock from '@/components/chat/RunErrorBlock.vue'
import { formatRuntimeDuration, formatWorkedDurationLabel, type SubagentCallTaskLive, type TimelineBlock, type SubagentTranscript } from '@/composables/useChatStream'
import { formatTokenCount } from '@/lib/forebrainGatewayRuntime'
import { t } from '@/locales'

const props = withDefaults(defineProps<{
  record: SubagentTranscript
  foldState?: Record<string, boolean>
  /** The live bindings of the subagent_* cards this transcript draws. */
  callTasks?: ReadonlyMap<string, ReadonlyMap<number, SubagentCallTaskLive>>
  /** The stream's shared clock, so the cards' walking rows tick with it. */
  now?: number
}>(), {
  foldState: () => ({}),
  callTasks: undefined,
  now: undefined,
})
const emit = defineEmits<{
  (e: 'back'): void
  (e: 'fold-change', value: { id: string; open: boolean }): void
  (e: 'open-agent', agentId: string): void
}>()

const hasAnswer = computed(() => props.record.blocks.some((block) => block.kind === 'assistant'))

const isRunning = computed(() =>
  props.record.status === 'running' || props.record.status === 'waiting_approval' || props.record.status === 'waiting_input')

// The model this execution's spawn announced — never derived from the agent's
// type, and absent means the spawn named none, which names none here too.
const modelLabel = computed(() => {
  const model = [props.record.modelProvider, props.record.model]
    .map((part) => String(part ?? '').trim())
    .filter(Boolean)
    .join('/')
  const effort = String(props.record.reasoningEffort ?? '').trim()
  return [model, effort].filter(Boolean).join(' · ')
})

// The subagent's own context gauge, the terminal's footer reading for its view:
// the used percentage and the window it is measured against.
const budgetLabel = computed(() => {
  const budget = props.record.tokenBudget
  const percentLeft = budget?.percentLeft
  if (typeof percentLeft !== 'number' || !Number.isFinite(percentLeft)) return ''
  const window = formatTokenCount(budget?.contextWindow)
  return `${Math.round(100 - percentLeft)}%${window ? `/${window}` : ''}`
})

// The working line's clock is this execution's own: it counts from the spawn
// the engine recorded, a second at a time, and stops the moment the run ends.
const nowMs = ref(Date.now())
let workingTimer: ReturnType<typeof setInterval> | null = null
watch(isRunning, (running) => {
  if (running && workingTimer == null) {
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

const workingDuration = computed(() => {
  const start = props.record.startedAt ? Date.parse(props.record.startedAt) : NaN
  if (!Number.isFinite(start)) return ''
  return formatRuntimeDuration(Math.max(0, nowMs.value - start))
})

function thinkingLabel(block: Extract<TimelineBlock, { kind: 'thinking' }>): string {
  if (block.durationMs && block.durationMs >= 1000) {
    return t('chat.subagentThoughtFor', { seconds: Math.round(block.durationMs / 1000) })
  }
  return t('chat.subagentThinking')
}

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

function updateNativeFold(id: string, event: Event) {
  updateFold(id, Boolean((event.currentTarget as HTMLDetailsElement | null)?.open))
}

const statusLabel = computed(() => {
  switch (props.record.status) {
    case 'done':
      return t('chat.subagentDone')
    case 'failed':
      return t('chat.subagentFailed')
	case 'cancelled':
	case 'interrupted':
	case 'waiting_approval':
	case 'waiting_input':
	  return props.record.status.replace(/_/g, ' ')
    default:
      return t('chat.subagentRunning')
  }
})

const statusClass = computed(() => {
  switch (props.record.status) {
    case 'done':
      return 'border-[var(--forebrain-divider)] text-[var(--forebrain-brand-1)]'
    case 'failed':
	case 'cancelled':
	case 'interrupted':
      return 'border-[var(--forebrain-danger)] text-[var(--forebrain-danger)]'
    default:
      return 'border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]'
  }
})
</script>

<style scoped>
.forebrain-working-dot {
  height: 0.4rem;
  width: 0.4rem;
  border-radius: 9999px;
  background: var(--forebrain-brand-1);
  animation: forebrain-working-pulse 1.6s ease-in-out infinite;
}

@keyframes forebrain-working-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.35;
  }
}

@media (prefers-reduced-motion: reduce) {
  .forebrain-working-dot {
    animation: none;
  }
}
</style>
