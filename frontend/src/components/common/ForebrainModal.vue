<template>
  <Teleport to="body">
    <div v-if="open" class="forebrain-modal-mask" @click.self="requestClose">
      <div
        ref="panelRef"
        class="forebrain-modal"
        :class="`forebrain-modal--${size}`"
        role="dialog"
        aria-modal="true"
        tabindex="-1"
        @keydown.esc.stop="requestClose"
      >
        <div class="forebrain-modal-head">
          <div class="forebrain-modal-title"><slot name="title">{{ title }}</slot></div>
          <button
            type="button"
            class="forebrain-modal-close"
            :aria-label="t('common.close')"
            @click="requestClose"
          >
            <X class="forebrain-modal-close-ic" aria-hidden="true" />
          </button>
        </div>
        <div class="forebrain-modal-body"><slot /></div>
        <div v-if="$slots.footer" class="forebrain-modal-foot"><slot name="footer" /></div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
/**
 * The one modal shell every dialog is built from: the mask, the titled head
 * with its close button, the scrolling body and the footer row. Sizes follow
 * the approved preview — 440px (sm), 560px (md) and 860px (xl). Escape and a
 * mask click close only the topmost open modal, and focus returns to whatever
 * opened it once it is gone.
 */
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { X } from 'lucide-vue-next'
import { useI18n } from '@/locales'
import { isTopModal, popModal, pushModal } from './modalStack'

const props = withDefaults(
  defineProps<{ open: boolean; title?: string; size?: 'sm' | 'md' | 'xl' }>(),
  { title: '', size: 'md' },
)
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()

const id = Symbol('forebrain-modal')
const panelRef = ref<HTMLElement | null>(null)
let restoreFocus: HTMLElement | null = null

function onKeydown(event: KeyboardEvent) {
  if (event.key !== 'Escape') return
  if (!isTopModal(id)) return
  event.stopPropagation()
  requestClose()
}

function requestClose() {
  if (!isTopModal(id)) return
  emit('close')
}

function activate() {
  pushModal(id)
  if (typeof document === 'undefined') return
  restoreFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  document.addEventListener('keydown', onKeydown, true)
  void nextTick(() => panelRef.value?.focus())
}

function deactivate() {
  if (typeof document !== 'undefined') document.removeEventListener('keydown', onKeydown, true)
  popModal(id)
  if (restoreFocus) {
    restoreFocus.focus()
    restoreFocus = null
  }
}

watch(
  () => props.open,
  (open) => {
    if (open) activate()
    else deactivate()
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  if (props.open) deactivate()
})
</script>

<style scoped>
.forebrain-modal-mask {
  position: fixed;
  inset: 0;
  z-index: 50;
  display: grid;
  place-items: center;
  padding: 16px;
  background: color-mix(in srgb, var(--forebrain-text) 32%, transparent);
}
.forebrain-modal {
  width: 560px;
  max-width: 100%;
  max-height: 660px;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  border: 1px solid var(--forebrain-divider);
  border-radius: 12px;
  background: var(--forebrain-surface);
  box-shadow: var(--forebrain-shadow-pop);
  outline: none;
}
.forebrain-modal--sm {
  width: 440px;
}
.forebrain-modal--xl {
  width: 860px;
  max-height: 780px;
}
.forebrain-modal-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 14px 16px 10px;
}
.forebrain-modal-title {
  font-family: var(--forebrain-ui-font);
  font-size: 14px;
  font-weight: 600;
  color: var(--forebrain-text);
}
.forebrain-modal-close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  flex: none;
  border-radius: 6px;
  color: var(--forebrain-text-2);
}
.forebrain-modal-close:hover {
  background: var(--forebrain-button-alt-hover-bg);
  color: var(--forebrain-text);
}
.forebrain-modal-close-ic {
  width: 16px;
  height: 16px;
}
.forebrain-modal-body {
  display: grid;
  gap: 10px;
  min-height: 0;
  overflow: auto;
  padding: 0 16px 14px;
}
.forebrain-modal-foot {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--forebrain-divider);
}
</style>
