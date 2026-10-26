<template>
  <!-- The prototype's .full-doc with .steps: the whole command, each source
       line and each chain step on its own row, the connector in its own
       column on a brand hairline, a heredoc as an indented block. Greys carry
       it; one warm tone marks strings, one cool tone variables. -->
  <div class="full-doc" data-testid="tool-doc-command">
    <DocCopyButton :label="t('chat.copyDocCommand')" :text="command" class="doc-copy" />
    <div class="steps" :class="{ multi: rows.multi }">
      <template v-for="(row, i) in rows.rows" :key="i">
        <div v-if="row.kind === 'step'" class="step">
          <span class="j">{{ row.op }}</span>
          <span class="x"><template v-for="(token, j) in row.tokens" :key="j"><span v-if="token.kind !== 'text' || token.text.trim()" :class="`k-${token.kind}`">{{ token.text }}</span><template v-else>{{ token.text }}</template></template></span>
        </div>
        <div v-else-if="row.kind === 'heredoc'" class="step hd">
          <span class="j"></span>
          <div class="x hd-body">{{ row.body }}</div>
        </div>
        <div v-else class="step">
          <span class="j"></span>
          <span class="x"><span class="k-end">{{ row.text }}</span></span>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The command as the document tab lays it out (prototype shSteps): the whole
 * of what ran, one step per row — never re-wrapped into prose, never
 * re-worded. The layout data comes from shellSteps; this component only
 * draws it, and copy gives the command exactly as it ran.
 */
import { computed } from 'vue'
import { shellSteps } from '@/lib/shellSteps'
import { useI18n } from '@/locales'
import DocCopyButton from './DocCopyButton.vue'

const props = defineProps<{ command: string }>()

const { t } = useI18n()

const rows = computed(() => shellSteps(props.command))
</script>

<style scoped>
/* The prototype's .full-doc (:459) and .steps (:322-335). */
.full-doc {
  position: relative;
  margin: 10px 12px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  padding: 12px 44px 12px 14px;
  font: 12.5px/1.7 var(--forebrain-mono-font);
  color: var(--forebrain-text-2);
  tab-size: 4;
}

.steps {
  display: grid;
}

.step {
  display: grid;
  grid-template-columns: minmax(0, 1fr);
}

.step .j {
  display: none;
}

/* The connector column, drawn only when there is more than one row. */
.steps.multi .step {
  grid-template-columns: 22px minmax(0, 1fr);
}

.steps.multi .step .j {
  display: block;
  padding-right: 7px;
  text-align: right;
  user-select: none;
  color: var(--forebrain-brand-1);
  font: 600 10.5px/2.02 var(--forebrain-mono-font);
  box-shadow: inset -1px 0 0 var(--forebrain-brand-border);
}

.step .x {
  min-width: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  padding-left: 1.5ch;
  text-indent: -1.5ch;
}

.steps.multi .step .x {
  padding-left: calc(10px + 1.5ch);
}

.step.hd .x {
  white-space: normal;
  text-indent: 0;
}

/* Greys carry the command; the program carries the weight; one warm tone for
   strings, one cool one for variables. */
.k-cmd {
  color: var(--forebrain-text);
  font-weight: 600;
}

.k-flag {
  color: #7a8494;
}

.k-str {
  color: #93600c;
}

.k-var {
  color: #0e6e7e;
}

.k-op {
  color: var(--forebrain-brand-1);
}

.k-end {
  color: #98a2b3;
}

.hd-body {
  margin: 2px 0 4px;
  padding: 6px 0 6px 12px;
  border-left: 2px solid var(--forebrain-divider-strong);
  font: 12px/1.65 var(--forebrain-mono-font);
  color: var(--forebrain-text-2);
  white-space: pre;
  overflow-x: auto;
}

.doc-copy {
  position: absolute;
  top: 8px;
  right: 8px;
}
</style>
