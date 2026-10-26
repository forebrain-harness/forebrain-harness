<template>
  <!-- The prototype's .full-doc with .full-t: the whole title, one paragraph,
       nothing cut. Copy gives the text exactly as the engine worded it. -->
  <div class="full-doc" data-testid="tool-doc-title">
    <DocCopyButton :label="t('chat.copyDocText')" :text="text" class="doc-copy" />
    <p class="full-t">{{ text }}</p>
  </div>
</template>

<script setup lang="ts">
/**
 * The title as the document tab holds it (prototype .full-t): the whole of
 * what the card head had to shorten, verbatim.
 */
import { useI18n } from '@/locales'
import DocCopyButton from './DocCopyButton.vue'

defineProps<{ text: string }>()

const { t } = useI18n()
</script>

<style scoped>
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

.full-t {
  margin: 0;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  color: var(--forebrain-text);
}

.doc-copy {
  position: absolute;
  top: 8px;
  right: 8px;
}
</style>
