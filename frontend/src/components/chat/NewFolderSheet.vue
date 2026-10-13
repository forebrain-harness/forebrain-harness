<template>
  <div class="sheet-mask" @click.self="emit('cancel')">
    <div class="sheet" role="dialog" :aria-label="t('chat.newFolderTitle')">
      <b>{{ t('chat.newFolderTitle') }}</b>
      <label class="lb">
        {{ t('chat.newFolderName') }}
        <input
          ref="input"
          v-model="name"
          class="field"
          autocomplete="off"
          :aria-label="t('chat.newFolderName')"
          @keydown.enter.prevent="submit"
          @keydown.escape.prevent="emit('cancel')"
        />
      </label>
      <span v-if="error" class="err">{{ t('chat.newFolderExists') }}</span>
      <span v-else class="hint mono">{{ parent }}</span>
      <div class="acts">
        <button type="button" class="btn" @click="emit('cancel')">{{ t('common.cancel') }}</button>
        <button type="button" class="btn pri" @click="submit">{{ t('chat.newFolderCreate') }}</button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { nextTick, onMounted, ref } from 'vue'
import { useI18n } from '@/locales'

/** The small sheet the folder picker opens to make a new folder in place. */
defineProps<{
  parent: string
  error?: boolean
}>()

const emit = defineEmits<{
  (e: 'create', name: string): void
  (e: 'cancel'): void
}>()

const { t } = useI18n()
const name = ref('')
const input = ref<HTMLInputElement | null>(null)

function submit() {
  const trimmed = name.value.trim()
  if (!trimmed) return
  emit('create', trimmed)
}

onMounted(() => {
  void nextTick(() => {
    input.value?.focus()
    input.value?.select()
  })
})
</script>

<style scoped>
.sheet-mask {
  position: absolute;
  inset: 0;
  background: color-mix(in srgb, var(--forebrain-text) 18%, transparent);
  display: flex;
  justify-content: center;
  align-items: flex-start;
  padding-top: 72px;
  z-index: 3;
}
.sheet {
  width: 340px;
  background: var(--forebrain-surface);
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  box-shadow: var(--forebrain-shadow-pop);
  padding: 14px 16px;
  display: grid;
  gap: 10px;
  text-align: left;
}
.sheet b {
  font-size: 13.5px;
  font-weight: 600;
  color: var(--forebrain-text);
}
.lb {
  display: grid;
  gap: 4px;
  font-size: 12px;
  color: var(--forebrain-text-2);
  font-weight: 500;
}
.field {
  height: 34px;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 6px;
  padding: 0 10px;
  font: inherit;
  font-size: 13px;
  color: var(--forebrain-text);
  background: var(--forebrain-surface);
  width: 100%;
  box-sizing: border-box;
}
.hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
.hint.mono {
  font-family: var(--forebrain-mono-font);
}
.err {
  color: var(--forebrain-danger);
  font-size: 12px;
}
.acts {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
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
