<template>
  <!-- One step on the activity line: the node at x=10px says what this card is
       (the prototype's .al-node), and the card hangs 32px in from the line
       (20px node + 12px gap). The library's ChainOfThoughtStep draws the line
       itself; only the step's own metrics are set here. -->
  <ChainOfThoughtStep class="activity-step" :class="`activity-step--${stepKind}`">
    <template #icon>
      <!-- say: the brand mark — the assistant is speaking (.al-say > .al-me).
           sub-say: the robot — a subagent is speaking (.al-say > .al-sub).
           think: the brain, in the quiet grey (.al-run > .think > .al-think).
           todo: the checklist, in the brand tint (.al-run > .plan-card > .al-todo).
           subagent: the robot, on the brand tint (.al-run > .sa-card).
           A call's state is its node (statusOf the card); an approval's
           settled line carries no node at all (node: null). -->
      <BrandMark v-if="node === 'say'" class="activity-node activity-node--say" :size="20" aria-hidden="true" />
      <span v-else-if="node === 'sub-say' || node === 'subagent'" class="activity-node activity-node--sub" role="img" :aria-label="nodeLabel">
        <Bot aria-hidden="true" />
      </span>
      <span v-else-if="node === 'think'" class="activity-node activity-node--think" role="img" :aria-label="nodeLabel">
        <Brain aria-hidden="true" />
      </span>
      <span v-else-if="node === 'todo'" class="activity-node activity-node--todo" role="img" :aria-label="nodeLabel">
        <ListChecks aria-hidden="true" />
      </span>
      <ToolStatusBadge v-else-if="statusNode" variant="node" :status="statusNode.status" :label="nodeLabel" />
    </template>
    <slot />
  </ChainOfThoughtStep>
</template>

<script setup lang="ts">
/**
 * One step of the activity line: its node (what the card is) and the card
 * itself. It is the library's ChainOfThoughtStep with the prototype's node
 * set — the states and their colours are the library's own
 * ToolStatusBadge drawn as the node (variant "node"), the speaking marks are
 * this app's.
 */
import { computed } from 'vue'
import { Bot, Brain, ListChecks } from 'lucide-vue-next'
import { ChainOfThoughtStep } from '@repo/elements/chain-of-thought'
import { ToolStatusBadge } from '@repo/elements/tool'
import BrandMark from '@/components/BrandMark.vue'
import { useI18n } from '@/locales'

/** The state vocabulary a call's node carries (ToolCallCard's statusOf). */
export type ToolCardStatus = 'running' | 'awaiting' | 'done' | 'denied' | 'canceled' | 'error'

/** What the step's node is: a mark, a call's state, or nothing at all. */
export type ActivityNode = 'say' | 'sub-say' | 'think' | 'todo' | 'subagent' | { status: ToolCardStatus } | null

const props = defineProps<{
  node: ActivityNode
}>()

const { t } = useI18n()

const statusNode = computed(() => (props.node && typeof props.node === 'object' ? props.node : null))

const stepKind = computed(() => (typeof props.node === 'string' ? props.node : statusNode.value ? 'status' : 'none'))

/**
 * The node's name for a reader who cannot see it: the state's own word for a
 * call (the clock says it is waiting on the reader, not what it will do — the
 * head carries that), what the mark stands for otherwise.
 */
const nodeLabel = computed(() => {
  if (statusNode.value) {
    const words: Record<ToolCardStatus, string> = {
      running: t('chat.toolStatusRunning'),
      awaiting: t('chat.toolStatusAwaiting'),
      done: t('chat.toolStatusDone'),
      denied: t('chat.toolStatusDenied'),
      canceled: t('chat.toolStatusCanceled'),
      error: t('chat.toolStatusError'),
    }
    return words[statusNode.value.status]
  }
  if (props.node === 'think') return t('chat.thinkingBlock')
  if (props.node === 'todo') return t('chat.updatedPlan')
  if (props.node === 'say') return t('brand.name')
  return t('chat.subagentDrawer')
})
</script>

<style scoped>
/* The card hangs 32px in from the line: the 20px node column plus 12px. The
   step is a grid item with a flex column inside — both default to
   min-width:auto, which lets a long head widen the whole line instead of
   cutting; the column may shrink to the room it actually has. */
.activity-step {
  gap: 12px;
  animation: none;
  min-width: 0;
}

.activity-step :deep(.flex-1) {
  min-width: 0;
}

.activity-node {
  width: 20px;
  height: 20px;
  box-sizing: border-box;
  border-radius: 50%;
  display: grid;
  place-items: center;
}

.activity-node :deep(svg.lucide) {
  width: 12px;
  height: 12px;
  stroke-width: 2.2;
}

/* A subagent speaks under the robot on the brand tint (.al-say > .al-sub);
   the same robot marks a subagent call (.al-run > .sa-card). */
.activity-node--sub {
  background: var(--forebrain-brand-soft);
  box-shadow: inset 0 0 0 1.5px var(--forebrain-brand-border);
  color: var(--forebrain-brand-1);
}

/* A thought is a quiet row (.al-run > .think > .al-think: the prototype's
   --soft grey, a shade under the surface the cards sit on). */
.activity-node--think {
  background: #f2f4f7;
  box-shadow: inset 0 0 0 1.5px var(--forebrain-divider);
  color: var(--forebrain-text-2);
}

/* The todo list is the brand's own mark (.al-run > .plan-card > .al-todo). */
.activity-node--todo {
  background: var(--forebrain-brand-soft);
  box-shadow: inset 0 0 0 1.5px var(--forebrain-brand-border);
  color: var(--forebrain-brand-1);
}

/* The brand mark speaks for the assistant itself; its fill is the brand's. */
.activity-node--say :deep(.mk-fill) {
  fill: var(--brand-mark-fill);
}

.activity-node--say :deep(.mk-glyph) {
  fill: #ffffff;
}
</style>
