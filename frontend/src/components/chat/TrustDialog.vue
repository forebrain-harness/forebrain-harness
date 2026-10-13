<template>
  <div class="mask" @click.self="emit('cancel')">
    <div class="modal sm" role="dialog" :aria-label="t('chat.trustTitle')">
      <div class="modal-h">
        <b>{{ t('chat.trustTitle') }}</b>
      </div>
      <div class="modal-b">
        <p>{{ t('chat.trustBody') }}</p>
        <dl class="trust-path">
          <dt>{{ t('chat.trustPath') }}</dt>
          <dd>{{ target.root }}</dd>
          <dt>{{ t('chat.trustProject') }}</dt>
          <dd>{{ target.isNew ? t('chat.trustWillCreate', { name: target.name }) : target.name }}</dd>
        </dl>
        <span class="hint">{{ t('chat.trustOnce') }}</span>
      </div>
      <div class="modal-f">
        <span style="flex: 1" />
        <button type="button" class="btn" @click="emit('cancel')">{{ t('common.cancel') }}</button>
        <button type="button" class="btn pri" @click="emit('confirm')">{{ t('chat.trustConfirm') }}</button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from '@/locales'
import type { ProjectTarget } from '@/lib/projectTarget'

/** The one-time trust prompt before a send into a directory nobody has judged
 * yet — the same question, and the same record, as the terminal's. */
defineProps<{
  target: ProjectTarget
}>()

const emit = defineEmits<{
  (e: 'confirm'): void
  (e: 'cancel'): void
}>()

const { t } = useI18n()
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
}
.modal-h {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 14px 16px 10px;
}
.modal-h b {
  font-size: 14px;
  font-weight: 600;
  color: var(--forebrain-text);
}
.modal-b {
  padding: 0 16px 14px;
  display: grid;
  gap: 10px;
  min-height: 0;
  overflow: auto;
}
.modal-b p {
  margin: 0;
  font-size: 13px;
  color: var(--forebrain-text-2);
  line-height: 1.55;
}
.trust-path {
  display: grid;
  grid-template-columns: 44px 1fr;
  gap: 4px 10px;
  margin: 0;
  font-size: 12.5px;
  background: var(--forebrain-bg-alt);
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  padding: 10px 12px;
}
.trust-path dt {
  color: var(--forebrain-muted-text);
}
.trust-path dd {
  margin: 0;
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
  color: var(--forebrain-text);
  word-break: break-all;
}
.hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.modal-f {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--forebrain-divider);
}
.btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 32px;
  padding: 0 12px;
  border-radius: 6px;
  border: 1px solid var(--forebrain-divider-strong);
  background: var(--forebrain-surface);
  color: var(--forebrain-text);
  font-size: 12.5px;
  font-weight: 500;
  cursor: pointer;
  white-space: nowrap;
}
.btn.pri {
  background: var(--forebrain-brand-1);
  border-color: var(--forebrain-brand-1);
  color: var(--forebrain-on-brand);
}
.btn.pri:hover {
  background: var(--forebrain-brand-hover);
  border-color: var(--forebrain-brand-hover);
}
</style>
