<template>
  <!-- The scrim: the library draws an overlay only for a modal root, and a
       modal root would trap focus (see the component comment), so the shell
       paints the mask itself and keeps the panel non-modal. It is teleported
       ahead of the panel so the panel paints above it. -->
  <Teleport to="body">
    <div v-if="open" class="forebrain-modal-mask" />
  </Teleport>
  <Dialog :open="open" :modal="false" @update:open="onOpenChange">
    <DialogContent
      :class="['forebrain-modal', `forebrain-modal--${size}`, 'sm:max-w-none']"
      @escape-key-down="preventDefault"
      @interact-outside="onInteractOutside"
    >
      <div class="forebrain-modal-head">
        <DialogTitle class="forebrain-modal-title"><slot name="title">{{ title }}</slot></DialogTitle>
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
    </DialogContent>
  </Dialog>
</template>

<script setup lang="ts">
/**
 * The one modal shell every dialog is built from: the titled head with its
 * close button, the scrolling body and the footer row. Sizes follow the
 * approved preview — 440px (sm), 560px (md) and 860px (xl). The shell is the
 * library's Dialog (plan 096) and its look is the `.forebrain-modal` design
 * class in main.css, because a dialog's content is portalled to the body and a
 * scoped style would not reach it. Escape and an overlay press close only the
 * topmost open modal, and focus returns to whatever opened it once it is gone.
 *
 * The panel's root is non-modal: the page keeps the keyboard while a dialog is
 * up, and the conversation relies on that — a send that lands while an attach
 * upload is still running waits for it. A trap pulls the next keystroke back
 * onto the dialog's last pressed button, which turns that send into a second
 * press of it. The mask is therefore the shell's own, since the library paints
 * an overlay only for a modal root.
 */
import { nextTick, onBeforeUnmount, watch } from 'vue'
import { X } from 'lucide-vue-next'
import { Dialog, DialogContent, DialogTitle } from '@repo/shadcn-vue/components/ui/dialog'
import { useI18n } from '@/locales'
import { isTopModal, popModal, pushModal } from './modalStack'

const props = withDefaults(
  defineProps<{ open: boolean; title?: string; size?: 'sm' | 'md' | 'xl' }>(),
  { title: '', size: 'md' },
)
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()

const id = Symbol('forebrain-modal')
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

/**
 * The library dismisses a dialog on its own for Escape, an outside press and
 * when focus leaves it. All three are silenced here so that every close walks
 * `modalStack` and only the dialog on top of it goes away; the initial focus
 * placement is left to the library.
 */
function preventDefault(event: Event) {
  event.preventDefault()
}

/**
 * An outside press closes the panel only when it lands on this dialog's own
 * scrim. The same event also reports a focus move out of the panel (what
 * handing focus back to a dialog underneath looks like), and a press inside a
 * dialog raised over this one — the folder browser is teleported by the page,
 * so it is not part of this panel — neither of which may close anything.
 */
function onInteractOutside(event: Event) {
  event.preventDefault()
  const original = (event as CustomEvent<{ originalEvent?: Event }>).detail?.originalEvent
  const target = original?.target
  if (original?.type !== 'pointerdown' || !(target instanceof Element)) return
  if (!target.closest('.forebrain-modal-mask')) return
  requestClose()
}

function onOpenChange(open: boolean) {
  if (!open) requestClose()
}

function activate() {
  pushModal(id)
  if (typeof document === 'undefined') return
  restoreFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null
  document.addEventListener('keydown', onKeydown, true)
}

function deactivate() {
  if (typeof document !== 'undefined') document.removeEventListener('keydown', onKeydown, true)
  popModal(id)
  if (!restoreFocus) return
  const opener = restoreFocus
  restoreFocus = null
  // On the next tick: the panel leaves the document with this frame, and the
  // opener takes focus back once the panel is no longer the active surface.
  void nextTick(() => opener.focus())
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
