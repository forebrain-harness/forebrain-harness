<template>
  <Teleport to="body">
    <div v-if="open" class="forebrain-drawer-mask" @click.self="requestClose">
      <aside
        ref="panelRef"
        class="forebrain-drawer"
        role="dialog"
        aria-modal="true"
        :aria-label="title"
        tabindex="-1"
        @keydown.esc.stop="requestClose"
      >
        <div class="forebrain-drawer-head">
          <b>{{ title }}</b>
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
      </aside>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
/**
 * The right-hand drawer (plan 071): one record's form beside the table it came
 * from, so the list stays in sight. It joins the same stack as a modal — Escape
 * and a mask click close only the topmost one, and focus returns to whatever
 * opened it — and its `readonly` badge says the record cannot be changed here.
 */
import { nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { X } from 'lucide-vue-next'

import BadgeComponent from '@/components/common/BadgeComponent.vue'
import { useI18n } from '@/locales'
import { isTopModal, popModal, pushModal } from './modalStack'

const props = withDefaults(defineProps<{ open: boolean; title: string; readonly?: boolean }>(), {
  readonly: false,
})
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()

const id = Symbol('forebrain-drawer')
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
