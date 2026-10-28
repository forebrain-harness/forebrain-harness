<template>
  <!-- The turn's delivery group (plan 153 A7, owner 2026-10-27): the one place
       a turn's files are drawn. Past three rows the rest folds away behind one
       quiet line; three or fewer are shown whole, because folding that little
       would be noise in itself. -->
  <section class="artifacts" :class="{ 'artifacts--grouped': Boolean(label) }" data-testid="artifact-list">
    <div v-if="label" class="artifacts-h">
      <b>{{ label }}</b>
      <span>{{ t('chat.artifactCount', { count: artifacts.length }) }}</span>
    </div>
    <ArtifactCard
      v-for="artifact in shown"
      :key="artifact.id"
      :artifact="artifact"
      @preview="(file) => emit('preview', file)"
    />
    <button
      v-if="foldable"
      type="button"
      class="artifacts-more"
      :aria-expanded="open"
      data-testid="artifact-list-more"
      @click="open = !open"
    >
      <component :is="open ? ChevronUp : ChevronDown" aria-hidden="true" />
      {{ open ? t('chat.artifactFold') : t('chat.artifactUnfold', { count: hidden }) }}
    </button>
  </section>
</template>

<script setup lang="ts">
/**
 * The turn's delivery group. The fold keeps the turn's last rows from pushing
 * the run's closing line off the screen; the state is the list's own, so
 * opening one turn's group never opens another's.
 */
import { computed, ref } from 'vue'
import { ChevronDown, ChevronUp } from 'lucide-vue-next'
import { useI18n } from '@/locales'
import type { ArtifactView } from '@/lib/artifacts'
import ArtifactCard from './ArtifactCard.vue'

/** How many rows a folded list shows (owner 2026-10-27). */
const VISIBLE = 3

const props = defineProps<{
  artifacts: ArtifactView[]
  /** The group's heading; omitted inside a card, where the card is the frame. */
  label?: string
}>()

const emit = defineEmits<{
  (e: 'preview', artifact: ArtifactView): void
}>()

const { t } = useI18n()

const open = ref(false)

const foldable = computed(() => props.artifacts.length > VISIBLE)
const shown = computed(() => (foldable.value && !open.value ? props.artifacts.slice(0, VISIBLE) : props.artifacts))
const hidden = computed(() => props.artifacts.length - shown.value.length)
</script>

<style scoped>
.artifacts {
  display: grid;
}

/* The delivery group closes the turn but keeps the body's left edge: the
   activity line's own 32px indent, and a hairline the body does not have. */
.artifacts--grouped {
  margin: 20px 0 0 32px;
  padding-top: 13px;
  border-top: 1px solid var(--forebrain-divider);
}

.artifacts-h {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 6px;
}

.artifacts-h b {
  font: 650 13.5px/1.4 var(--forebrain-ui-font);
  color: var(--forebrain-text);
}

.artifacts-h span {
  color: var(--forebrain-muted-text);
  font: 11.5px/1.4 var(--forebrain-ui-font);
}

.artifacts-more {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  width: 100%;
  margin-top: 2px;
  padding: 9px 0;
  border: 0;
  border-top: 1px solid var(--forebrain-divider);
  background: transparent;
  color: var(--forebrain-text-2);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}

.artifacts-more:hover {
  color: var(--forebrain-brand-1);
}

.artifacts-more :deep(svg) {
  width: 14px;
  height: 14px;
}
</style>
