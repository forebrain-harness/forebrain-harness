<template>
  <Sheet :open="open" @update:open="onOpenChange">
    <SheetContent
      side="right"
      class="forebrain-drawer sm:max-w-none"
      :style="widthStyle"
      :aria-label="title"
      @escape-key-down="preventDefault"
      @interact-outside="onInteractOutside"
    >
      <div class="forebrain-drawer-head">
        <!-- A record that draws its own header — a call's status and moment, a
             log line's level and title — replaces the shell's title here; the
             `title` prop stays the panel's name for assistive technology. -->
        <slot name="head" />
        <b v-if="!$slots.head">{{ title }}</b>
        <BadgeComponent v-if="readonly" variant="gray">{{ t('drawer.readonly') }}</BadgeComponent>
        <button
          v-tip:bottom="t('common.close')"
          type="button"
          class="forebrain-icon-btn"
          :aria-label="t('common.close')"
          @click="requestClose"
        >
          <X class="size-3.5" aria-hidden="true" />
        </button>
      </div>
      <div class="forebrain-drawer-body">
        <slot />
      </div>
      <div v-if="$slots.footer" class="forebrain-drawer-foot">
        <span class="flex-1" />
        <slot name="footer" />
      </div>
    </SheetContent>
  </Sheet>
</template>

<script setup lang="ts">
/**
 * The right-hand drawer (plan 071): one record's form beside the table it came
 * from, so the list stays in sight. It joins the same stack as a modal — Escape
 * and an overlay press close only the topmost one, and focus returns to
 * whatever opened it — and its `readonly` badge says the record cannot be
 * changed here. It is the library's Sheet (plan 096); its look is the global
 * `.forebrain-drawer` design class, since a sheet's content is portalled.
 *
 * Two things a caller can change: the width (the log page's call drawer is the
 * prototype's 720 rather than the design's 560) and the header (`#head` draws a
 * record's own badges and moment where the plain title would be).
 */
import { computed, nextTick, onBeforeUnmount, watch } from 'vue'
import { X } from 'lucide-vue-next'

import { Sheet, SheetContent } from '@repo/shadcn-vue/components/ui/sheet'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import { useI18n } from '@/locales'
import { isTopModal, popModal, pushModal } from './modalStack'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    readonly?: boolean
    /** The panel's width in px; absent keeps the design's 560. */
    width?: number
  }>(),
  { readonly: false },
)
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()

const widthStyle = computed(() => (props.width ? { width: `${props.width}px` } : undefined))

const id = Symbol('forebrain-drawer')
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
 * The library dismisses a sheet on its own for Escape, an outside press and
 * when focus leaves it. All three are silenced here so that every close walks
 * `modalStack` and only the panel on top of it goes away; the initial focus
 * placement is left to the library.
 */
function preventDefault(event: Event) {
  event.preventDefault()
}

/**
 * An outside press closes the panel only when it lands on this sheet's own
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
  if (!target.closest('[data-slot="sheet-overlay"]')) return
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
  // Once the panel is gone: while it is still in the document the library's
  // focus trap pulls focus back into it, and the opener would lose.
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
