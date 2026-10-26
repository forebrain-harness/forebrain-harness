<template>
  <!-- What the decision turns on, as the waiting call's panel (prototype
       .tool-b .facts / .memo, :596-700): the facts as lines, one per line for
       a set; a memory note as the document it is, its own scroll and a fading
       bottom edge; a patch's files one row each, then one representative hunk
       in the one diff panel every card shares. -->
  <button
    v-if="requester && canOpenAgent"
    type="button"
    class="gate-requester block max-w-full truncate text-left text-xs text-[var(--forebrain-text-2)] hover:text-[var(--forebrain-brand-1)] disabled:pointer-events-none"
    :disabled="!action.agentId"
    @click="action.agentId && emit('open-agent', action.agentId)"
  >{{ requester }}</button>
  <div v-else-if="requester" class="gate-requester truncate text-xs text-[var(--forebrain-text-2)]">{{ requester }}</div>
  <div v-if="gate.facts.facts.length" class="facts" :class="{ rows: gate.facts.facts.length > 1 }">
    <div v-for="(fact, index) in gate.facts.facts" :key="`${action.id}-fact-${index}`" data-testid="approval-gate-fact">{{ fact }}</div>
  </div>
  <!-- A note bound for memory is a document: prose size, its own scroll. -->
  <div v-if="gate.facts.bodyMarkdown" class="memo" data-testid="approval-gate-body">
    <MessageResponse :content="gate.facts.bodyMarkdown" />
  </div>
  <FileChangeList v-if="gate.facts.patchFiles?.length" :files="gate.facts.patchFiles" />
  <DiffBlock v-if="diffRows.length" :rows="diffRows" :max-height="240" data-testid="approval-gate-diff" />
</template>

<script setup lang="ts">
/**
 * The facts panel of a waiting call's card (plan 138): what the decision
 * turns on, moved out of the retired end-of-thread gate card onto the call it
 * belongs to. The facts and their shapes come from approvalGates; the diff is
 * the one row component every card shares (DiffBlock), so an approval's
 * preview reads exactly like the settled call's diff would.
 */
import { computed } from 'vue'
import { MessageResponse } from '@repo/elements/message'
import type { ApprovalFacts } from '@/lib/approvalGates'
import type { ActionRow } from '@/composables/usePendingActions'
import DiffBlock from './DiffBlock.vue'
import FileChangeList from './FileChangeList.vue'

const props = withDefaults(defineProps<{
  /** The pending action this panel describes. */
  action: ActionRow
  /** The action's family, facts, and question (`approvalGate`). */
  gate: { facts: ApprovalFacts; question: string }
  /** Who is blocked on this approval, when a subagent raised it. */
  requester?: string
  /** Whether the requester line can open that subagent's own view. */
  canOpenAgent?: boolean
}>(), { requester: '', canOpenAgent: false })

const emit = defineEmits<{ (e: 'open-agent', agentId: string): void }>()

// Forty rows is the most one approval asks anyone to read; past that the diff
// scrolls inside its panel instead of growing the card.
const diffRows = computed(() => (props.gate.facts.diff ?? []).slice(0, 40))
</script>

<style scoped>
/* The prototype's .tool-b .facts (:596-597, :695-697): 12.5px, the second
   text colour, anywhere-wrapping so a long identifier breaks instead of
   being cut; a set is one line per row, 2px apart. */
.facts {
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--forebrain-text-2);
  overflow-wrap: anywhere;
}

.facts.rows {
  display: grid;
  gap: 2px;
}

/* A note bound for memory: prose size, its own scroll, a fading bottom edge
   (prototype .tool-b .memo, :699-700). */
.memo {
  border-left: 2px solid var(--forebrain-divider-strong);
  padding: 1px 0 1px 12px;
  max-height: 160px;
  overflow: auto;
  font-size: 13px;
  line-height: 1.6;
  -webkit-mask-image: linear-gradient(#000 calc(100% - 20px), transparent);
  mask-image: linear-gradient(#000 calc(100% - 20px), transparent);
}

.gate-requester {
  margin: 0;
}
</style>
