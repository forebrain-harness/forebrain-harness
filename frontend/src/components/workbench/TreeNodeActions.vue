<template>
  <span class="tn-acts">
    <button
      v-if="isDir"
      type="button"
      class="tn-act"
      v-tip:top="t('chat.treeActionNewFile')"
      :aria-label="`${t('chat.treeActionNewFile')} · ${name}`"
      data-testid="tree-node-new"
      @click.stop="emit('new-file')"
    >
      <Plus class="tn-act-ic" aria-hidden="true" />
    </button>
    <button
      type="button"
      class="tn-act"
      v-tip:top="isDir ? t('chat.treeActionRename') : t('chat.treeActionEdit')"
      :aria-label="`${isDir ? t('chat.treeActionRename') : t('chat.treeActionEdit')} · ${name}`"
      data-testid="tree-node-edit"
      @click.stop="emit('edit')"
    >
      <Pencil class="tn-act-ic" aria-hidden="true" />
    </button>
    <button
      type="button"
      class="tn-act tn-act--del"
      v-tip:top="t('chat.treeActionDelete')"
      :aria-label="`${t('chat.treeActionDelete')} · ${name}`"
      data-testid="tree-node-delete"
      @click.stop="emit('delete')"
    >
      <Trash2 class="tn-act-ic" aria-hidden="true" />
    </button>
  </span>
</template>

<script setup lang="ts">
/**
 * The per-node actions of the workbench's directory tree: a file offers edit
 * and delete, a directory also offers "new file". The icons are always shown
 * in the muted colour and darken on hover; delete turns to the danger colour.
 */
import { Pencil, Plus, Trash2 } from 'lucide-vue-next'

import { useI18n } from '@/locales'

defineProps<{ isDir: boolean; name: string }>()
const emit = defineEmits<{ (e: 'new-file'): void; (e: 'edit'): void; (e: 'delete'): void }>()
const { t } = useI18n()
</script>

<style scoped>
.tn-acts {
  display: flex;
  flex: none;
  padding-right: 2px;
}
.tn-act {
  display: inline-grid;
  place-items: center;
  width: 22px;
  height: 22px;
  border: 0;
  background: transparent;
  border-radius: 6px;
  color: var(--forebrain-muted-text);
}
.tn-act:hover {
  color: var(--forebrain-text);
  background: var(--forebrain-button-alt-bg);
}
.tn-act--del:hover {
  color: var(--forebrain-danger);
}
.tn-act-ic {
  width: 13px;
  height: 13px;
}
</style>
