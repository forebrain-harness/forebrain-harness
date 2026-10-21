<template>
  <!-- The prototype's .ask-done (:3337): what was asked, then each answer under
       it. It is content, not a control — the conversation says what happened
       here, and nothing about it is a way into another panel. Each line is the
       library's task row, which is what a row of a settled card is. -->
  <div class="ask-done" data-testid="ask-done">
    <div v-for="(entry, index) in entries" :key="index" class="qa">
      <TaskItem v-if="entry.question" class="q">{{ entry.question }}</TaskItem>
      <TaskItem v-for="(answer, at) in entry.answers" :key="at" class="ans">
        <!-- The arrow and its space are one text run, so the line reads — and
             copies — exactly as the terminal prints it. -->
        <span class="arrow" aria-hidden="true">→ </span>
        <span class="text">{{ answer }}</span>
      </TaskItem>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * A question call the user has answered, as the thread keeps it: the question
 * in the agent's own words (its short tab header says nothing to whoever reads
 * the transcript later) and the answer that was given.
 *
 * No component in ai-elements-vue draws a settled question record — the nearest
 * are `confirmation` (an accept/reject gate, which this is not) and `task`
 * (whose row this uses) — so the card is composed from the library's task row.
 * The entries come from the runtime's own settled body, so the web and the
 * terminal print the same question and the same answer; see answeredAskEntries.
 */
import { computed } from 'vue'
import { TaskItem } from '@repo/elements/task'
import { answeredAskEntries } from '@/composables/useChatStream'

const props = defineProps<{ text: string }>()

const entries = computed(() => answeredAskEntries(props.text))
</script>

<style scoped>
.ask-done {
  display: grid;
  gap: 6px;
}

.qa {
  display: grid;
  gap: 1px;
}

.q {
  font-size: 12px;
  color: var(--forebrain-text-2);
}

.ans {
  display: flex;
  font-size: 13px;
  color: var(--forebrain-text);
}

.arrow {
  flex: none;
  white-space: pre;
  color: var(--forebrain-muted-text);
}

.text {
  min-width: 0;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}
</style>
