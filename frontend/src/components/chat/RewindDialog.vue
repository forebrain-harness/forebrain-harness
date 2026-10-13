<template>
  <div class="mask" @click.self="emit('cancel')">
    <div class="modal sm" role="dialog" :aria-label="t('chat.rewindTitle')">
      <div class="modal-h">
        <b>{{ t('chat.rewindTitle') }}</b>
        <button type="button" class="icon-btn" :aria-label="t('chat.rewindCancel')" @click="emit('cancel')">
          <X class="size-4" aria-hidden="true" />
        </button>
      </div>
      <div class="modal-b">
        <div class="rw-msg">{{ message }}</div>
        <div class="rw-modes" role="radiogroup">
          <button
            v-for="opt in modes"
            :key="opt.value"
            type="button"
            class="rc"
            role="radio"
            :aria-checked="mode === opt.value"
            :data-rwmode="opt.value"
            @click="mode = opt.value"
          >{{ t(opt.label) }}</button>
        </div>
        <template v-if="mode !== 'chat'">
          <div v-if="files.length" class="rw-files">
            <div class="cap">{{ t('chat.rewindFiles', { n: files.length }) }}</div>
            <label v-for="f in files" :key="f.path" class="rw-file">
              <input type="checkbox" :checked="!keep.includes(f.path)" @change="toggle(f.path)">
              <span class="mono">{{ f.path }}</span>
              <span v-if="f.changedAfter" class="badge warn" style="height: 18px">{{ t('chat.rewindChangedAfter') }}</span>
              <span v-else-if="f.created" class="badge" style="height: 18px">{{ t('chat.rewindNewFile') }}</span>
              <span class="dt">+{{ f.added }} −{{ f.removed }}</span>
            </label>
          </div>
          <span class="hint">{{ t('chat.rewindTerminalNote') }}</span>
        </template>
        <span v-if="mode !== 'code'" class="hint">{{ t('chat.rewindChatNote') }}</span>
        <span v-if="error" class="err">{{ error }}</span>
      </div>
      <div class="modal-f">
        <span style="flex: 1" />
        <button type="button" class="btn" :disabled="busy" @click="emit('cancel')">{{ t('chat.rewindCancel') }}</button>
        <button type="button" class="btn pri" :disabled="busy" data-testid="rewind-restore" @click="emit('restore', { mode, keepPaths: keep })">
          {{ t('chat.rewindRestore') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { X } from 'lucide-vue-next'
import { useI18n, type I18nKey } from '@/locales'
import type { RewindPreview } from '@/lib/api'

/**
 * The confirm dialog a "rewind to here" opens (plan 033), matching the
 * prototype's rewindModal(): the message's text, the three modes, the files a
 * code restore would touch with a checkbox each, and the two notes. Restoring
 * emits the chosen mode and the paths the user left unchecked.
 */
const props = defineProps<{
  /** The anchor message's text, shown so the user knows what they picked. */
  message: string
  /** The preview the server returned, or null while it loads. */
  preview: RewindPreview | null
  busy?: boolean
  error?: string
}>()

const emit = defineEmits<{
  (e: 'restore', payload: { mode: 'both' | 'chat' | 'code'; keepPaths: string[] }): void
  (e: 'cancel'): void
}>()

const { t } = useI18n()

const modes: Array<{ value: 'both' | 'chat' | 'code'; label: I18nKey }> = [
  { value: 'both', label: 'chat.rewindModeBoth' },
  { value: 'chat', label: 'chat.rewindModeChat' },
  { value: 'code', label: 'chat.rewindModeCode' },
]

const mode = ref<'both' | 'chat' | 'code'>('both')
const files = computed(() => props.preview?.files ?? [])
// Paths the user unchecked: the code half must leave them alone.
const keep = ref<string[]>([])

function toggle(path: string) {
  const i = keep.value.indexOf(path)
  if (i >= 0) keep.value = keep.value.filter((p) => p !== path)
  else keep.value = [...keep.value, path]
}
</script>

<style scoped>
.mask {
  position: fixed;
  inset: 0;
  background: color-mix(in srgb, var(--forebrain-text) 32%, transparent);
  z-index: 40;
  display: grid;
  place-items: center;
}
.modal {
  background: var(--forebrain-surface);
  border: 1px solid var(--forebrain-divider);
  border-radius: 12px;
  box-shadow: var(--forebrain-shadow-pop);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.modal.sm {
  width: 440px;
  max-width: calc(100vw - 32px);
}
.modal-h {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--forebrain-divider);
}
.modal-h b {
  font-size: 13.5px;
  font-weight: 600;
  color: var(--forebrain-text);
}
.modal-b {
  display: grid;
  gap: 10px;
  padding: 14px;
  overflow-y: auto;
}
.modal-f {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 14px;
  border-top: 1px solid var(--forebrain-divider);
}
.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: 6px;
  color: var(--forebrain-text-2);
}
.icon-btn:hover:not(:disabled) {
  background: var(--forebrain-button-alt-hover-bg);
}
.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 32px;
  padding: 0 12px;
  border-radius: 6px;
  border: 1px solid var(--forebrain-divider);
  background: var(--forebrain-surface);
  color: var(--forebrain-text);
  font-size: 12.5px;
  font-weight: 500;
  cursor: pointer;
}
.btn:hover:not(:disabled) {
  background: var(--forebrain-button-alt-hover-bg);
}
.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn.pri {
  background: var(--forebrain-brand-1);
  border-color: var(--forebrain-brand-1);
  color: var(--forebrain-on-brand);
}
.btn.pri:hover:not(:disabled) {
  background: var(--forebrain-brand-hover);
  border-color: var(--forebrain-brand-hover);
}
/* The prototype's .rw-* rules (plan 033). */
.rw-msg {
  border-left: 3px solid var(--forebrain-brand-border, var(--forebrain-brand-1));
  padding: 4px 10px;
  color: var(--forebrain-text-2);
  font-size: 12.5px;
  background: var(--forebrain-subtle, var(--forebrain-bg-alt));
  border-radius: 4px;
}
.rw-modes {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
}
.rc {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  height: 32px;
  padding: 0 10px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  background: var(--forebrain-surface);
  color: var(--forebrain-text-2);
  font-size: 12.5px;
  font-weight: 500;
  cursor: pointer;
}
.rc[aria-checked='true'] {
  color: var(--forebrain-brand-1);
  border-color: var(--forebrain-brand-1);
}
.rw-files {
  display: grid;
  gap: 5px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  padding: 8px 10px;
}
.rw-files .cap {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.rw-file {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  min-width: 0;
}
.rw-file .mono {
  font-family: var(--forebrain-mono-font);
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rw-file .dt {
  margin-left: auto;
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
}
.rw-file input {
  accent-color: var(--forebrain-brand-1);
}
.badge {
  display: inline-flex;
  align-items: center;
  padding: 0 7px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 500;
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
  white-space: nowrap;
}
.badge.warn {
  background: var(--forebrain-warn-soft, var(--forebrain-brand-soft));
  color: var(--forebrain-warn, var(--forebrain-brand-1));
}
.hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.err {
  font-size: 12px;
  color: var(--forebrain-danger, var(--forebrain-brand-1));
}
</style>
