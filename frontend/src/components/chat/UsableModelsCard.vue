<template>
  <!-- A list_usable_models answer (pkg/tool ReadableModel): the model roster,
       one group per provider in the answer's own order. The label a person
       reads, its thinking levels as chips with the default highlighted, and
       the two marks naming this conversation's current row and an approved
       hand-off's row. The wire id is never here (owner 2026-11-20, plan 337). -->
  <div class="usable-models" data-testid="usable-models">
    <p v-if="!groups.length" class="empty" data-testid="usable-models-empty">
      {{ t('chat.usableModelsEmpty') }}
      <span class="hint">{{ t('chat.usableModelsEmptyHint') }}</span>
    </p>
    <div v-for="group in groups" :key="group.provider" class="grp" data-testid="usable-models-group">
      <div class="prov">{{ group.provider }}</div>
      <div v-for="row in group.rows" :key="row.model" class="row" data-testid="usable-models-row">
        <div class="l1">
          <span class="nm">{{ row.model }}</span>
          <span v-if="row.current" class="flag cur">{{ t('chat.usableModelsCurrent') }}</span>
          <span v-if="row.handedOff" class="flag ho">{{ t('chat.usableModelsHandedOff') }}</span>
        </div>
        <div v-if="row.efforts?.length" class="l2 chips">
          <span
            v-for="effort in row.efforts"
            :key="effort"
            class="chip"
            :class="{ def: effort === row.defaultEffort }"
          >{{ effort }}<span v-if="effort === row.defaultEffort" class="dtag">{{ t('chat.usableModelsDefault') }}</span></span>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The roster a list_usable_models call answers with, drawn in the web card's
 * own language (plan 337, prototype frame A2): provider headings, one line per
 * model, thinking levels as chips with the row's default highlighted. The
 * engine owns the facts; this only lays them out and words the two marks.
 */
import { computed } from 'vue'
import type { ReadableAnswer, ReadableModel } from '@/composables/useChatStream'
import { useI18n } from '@/locales'

const props = defineProps<{ answer: ReadableAnswer }>()

const { t } = useI18n()

/** One provider's heading and its models, in the answer's first-seen order. */
interface Group {
  provider: string
  rows: ReadableModel[]
}

/** groups keeps the answer's order: a provider heads the rows that follow it,
 * and repeats nothing when its rows are not contiguous. */
const groups = computed<Group[]>(() => {
  const models = props.answer.models ?? []
  const byProvider = new Map<string, ReadableModel[]>()
  for (const model of models) {
    const provider = String(model.provider ?? '').trim() || String(model.model ?? '').trim()
    const rows = byProvider.get(provider)
    if (rows) rows.push(model)
    else byProvider.set(provider, [model])
  }
  return [...byProvider.entries()].map(([provider, rows]) => ({ provider, rows }))
})
</script>

<style scoped>
/* The card's own body (ToolOutput's .tool-body): the same padding, size and
   bound as the peer roster, so a model roster reads as one more tool result. */
.usable-models {
  padding: 9px 11px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--forebrain-text);
  max-height: 360px;
  overflow: auto;
}

.empty {
  margin: 0;
  color: var(--forebrain-text-2);
}

.empty .hint {
  display: block;
  margin-top: 3px;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}

.grp + .grp {
  margin-top: 12px;
}

.prov {
  font-size: 12px;
  font-weight: 600;
  color: var(--forebrain-text-2);
  margin-bottom: 3px;
}

.row {
  padding: 4px 0;
}

.l1 {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.nm {
  font-size: 13px;
  font-weight: 550;
  color: var(--forebrain-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* The two marks name the conversation's own row and an approved hand-off's:
   the current row wears the brand's soft fill, a hand-off an outline. */
.flag {
  flex: none;
  font-size: 11px;
  line-height: 1.5;
  border-radius: 999px;
  padding: 0 7px;
  border: 1px solid transparent;
}

.flag.cur {
  color: var(--forebrain-brand-1);
  background: var(--forebrain-brand-soft);
  border-color: var(--forebrain-brand-border);
}

.flag.ho {
  color: var(--forebrain-text-2);
  background: var(--forebrain-bg);
  border-color: var(--forebrain-divider-strong);
}

.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 5px;
  align-items: center;
  margin-top: 2px;
}

.chip {
  font-family: var(--forebrain-mono-font);
  font-size: 11px;
  line-height: 1.6;
  color: var(--forebrain-text-2);
  background: var(--forebrain-bg-alt);
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  padding: 0 7px;
}

.chip.def {
  color: var(--forebrain-brand-1);
  background: var(--forebrain-brand-soft);
  border-color: var(--forebrain-brand-border);
}

.dtag {
  font-family: var(--forebrain-ui-font);
  font-size: 10.5px;
  opacity: 0.8;
  margin-left: 4px;
}
</style>
