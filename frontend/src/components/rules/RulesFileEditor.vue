<template>
  <div class="grid gap-4 lg:grid-cols-[260px_minmax(0,1fr)]">
    <aside class="space-y-2">
      <div
        v-for="file in files"
        :key="fileKey(file)"
        class="rules-file-row"
        :class="{ 'rules-file-row--active': fileKey(file) === activeKey, 'rules-file-row--missing': !file.exists }"
        role="button"
        tabindex="0"
        @click="emit('select', file)"
        @keydown.enter.prevent="emit('select', file)"
        @keydown.space.prevent="emit('select', file)"
      >
        <FileText class="size-4 shrink-0" aria-hidden="true" />
        <span class="min-w-0 flex-1 truncate">{{ label(file) }}</span>
        <span v-if="!file.exists" class="rules-file-tag">{{ t('rules.notCreated') }}</span>
      </div>
      <div v-if="creatable.length" class="pt-2">
        <div class="mb-1 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('rules.newFile') }}</div>
        <select class="forebrain-field w-full text-[13px]" data-testid="rules-create-select" @change="onCreate($event)">
          <option value="">{{ t('rules.createPlaceholder') }}</option>
          <option v-for="item in creatable" :key="itemKey(item)" :value="itemValue(item)">{{ itemLabel(item) }}</option>
        </select>
      </div>
    </aside>

    <section class="min-w-0 rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ activeLabel }}</div>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!canSave || saving" data-testid="rules-save" @click="emit('save')">
          {{ saving ? t('common.loading') : t('common.save') }}
        </button>
      </div>
      <p v-if="notice" class="mt-2 text-[12px] text-[var(--forebrain-text-2)]">{{ notice }}</p>
      <p v-if="warning" class="mt-2 text-[12px] text-[var(--forebrain-danger)]">{{ t('rules.budgetWarning') }}</p>
      <p v-if="error" class="mt-2 text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>
      <textarea
        :value="content"
        class="rules-editor"
        :placeholder="t('rules.editorPlaceholder')"
        :aria-label="activeLabel"
        @input="emit('update', ($event.target as HTMLTextAreaElement).value)"
      />
      <p class="mt-2 text-[11px] text-[var(--forebrain-muted-text)]">{{ t('rules.applyHint') }}</p>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { FileText } from 'lucide-vue-next'
import { useI18n } from '@/locales'

/**
 * The shared rules-file editor: a file list with a create dropdown on the
 * left, a plain-text markdown editor on the right. Host pages own the data
 * (agent bootstrap files or a project's FOREBRAIN.md chain) and pass
 * label/key callbacks so this component stays shape-agnostic.
 */
const props = defineProps<{
  files: Array<{ exists: boolean } & Record<string, unknown>>
  creatable: Array<Record<string, unknown>>
  activeKey: string
  activeLabel: string
  content: string
  dirty: boolean
  saving: boolean
  notice: string
  warning: string
  error: string
  fileKey: (file: Record<string, unknown>) => string
  label: (file: Record<string, unknown>) => string
  itemKey: (item: Record<string, unknown>) => string
  itemValue: (item: Record<string, unknown>) => string
  itemLabel: (item: Record<string, unknown>) => string
}>()

const emit = defineEmits<{
  (e: 'select', file: Record<string, unknown>): void
  (e: 'create', value: string): void
  (e: 'update', content: string): void
  (e: 'save'): void
}>()

const { t } = useI18n()
const canSave = computed(() => props.dirty)

function onCreate(event: Event) {
  const value = (event.target as HTMLSelectElement).value
  if (value) emit('create', value)
  ;(event.target as HTMLSelectElement).value = ''
}
</script>

<style scoped>
.rules-file-row {
  display: flex;
  align-items: center;
  gap: 8px;
  border-radius: 10px;
  border: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
  padding: 9px 11px;
  font-size: 13px;
  color: var(--forebrain-text);
  cursor: pointer;
}
.rules-file-row:hover {
  background: var(--forebrain-button-alt-bg);
}
.rules-file-row--active {
  border-color: var(--forebrain-brand-1);
  background: var(--forebrain-brand-soft);
}
.rules-file-row--missing {
  color: var(--forebrain-muted-text);
}
.rules-file-tag {
  flex: none;
  font-size: 10px;
  color: var(--forebrain-muted-text);
  border: 1px solid var(--forebrain-divider);
  border-radius: 9999px;
  padding: 1px 8px;
}
.rules-editor {
  margin-top: 12px;
  width: 100%;
  min-height: 420px;
  box-sizing: border-box;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 10px;
  padding: 12px;
  font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, Consolas, monospace;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--forebrain-text);
  background: var(--forebrain-surface);
  outline: none;
  resize: vertical;
}
.rules-editor:focus {
  border-color: var(--forebrain-focus-border);
}
</style>
