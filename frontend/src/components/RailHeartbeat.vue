<template>
  <Teleport to="body">
    <div v-if="open" class="rail-tooltip-backdrop" @pointerdown="emit('close')" />
    <div
      v-if="open"
      class="rail-popover"
      role="dialog"
      :aria-label="t('heartbeat.title')"
      :style="{ left: `${railWidth}px`, bottom: '16px' }"
      @keydown.escape="emit('close')"
    >
      <SessionHeartbeat :session-id="sessionId" bare />
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import SessionHeartbeat from '@/components/chat/SessionHeartbeat.vue'
import { useI18n } from '@/locales'

/**
 * The rail's heartbeat entry: a low-frequency control, so it lives in the
 * rail's foot rather than the workbench, in a popover that opens upward.
 */
defineProps<{
  open: boolean
  railWidth: number
  sessionId: string | null
}>()

const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
</script>

<style scoped>
.rail-tooltip-backdrop {
  position: fixed;
  inset: 0;
  z-index: 45;
  background: transparent;
}
.rail-popover {
  position: fixed;
  z-index: 46;
  width: 300px;
  padding: 12px;
  background: var(--forebrain-surface);
  border: 1px solid var(--forebrain-divider);
  border-radius: 12px;
  box-shadow: var(--forebrain-shadow-pop);
}
</style>
