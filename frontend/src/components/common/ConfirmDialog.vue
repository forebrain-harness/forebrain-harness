<template>
  <ForebrainModal :open="open" size="sm" :title="title" @close="emit('cancel')">
    <p class="forebrain-confirm-message">{{ message }}</p>
    <label v-if="typedConfirm" class="forebrain-confirm-field">
      <input v-model="typed" class="forebrain-field" :placeholder="typedConfirm" autocomplete="off" />
    </label>
    <template #footer>
      <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="emit('cancel')">
        {{ t('common.cancel') }}
      </button>
      <button
        type="button"
        class="forebrain-btn text-xs"
        :class="danger ? 'forebrain-confirm-danger' : 'forebrain-btn-primary'"
        :disabled="!canConfirm"
        data-testid="confirm-ok"
        @click="emit('confirm')"
      >
        {{ confirmLabel }}
      </button>
    </template>
  </ForebrainModal>
</template>

<script setup lang="ts">
/**
 * The confirmation dialog: a small modal with one message and a cancel/confirm
 * pair. `danger` paints the confirm in the danger colour; `typedConfirm` gates
 * it behind retyping an exact string, for the one action that deletes data.
 */
import { computed, ref, watch } from 'vue'
import { useI18n } from '@/locales'
import ForebrainModal from './ForebrainModal.vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    message: string
    confirmLabel: string
    danger?: boolean
    typedConfirm?: string
  }>(),
  { danger: false, typedConfirm: '' },
)
const emit = defineEmits<{ confirm: []; cancel: [] }>()
const { t } = useI18n()

const typed = ref('')

// Each time the dialog opens, the typed text starts empty again.
watch(
  () => props.open,
  (open) => {
    if (open) typed.value = ''
  },
)

const canConfirm = computed(() => !props.typedConfirm || typed.value === props.typedConfirm)
</script>

<style scoped>
.forebrain-confirm-message {
  margin: 0;
  font-size: 13px;
  line-height: 1.5;
  color: var(--forebrain-text-2);
  white-space: pre-line;
}
.forebrain-confirm-field {
  display: block;
}
.forebrain-confirm-danger {
  border-color: var(--forebrain-danger);
  background: var(--forebrain-danger);
  color: var(--forebrain-on-brand);
}
.forebrain-confirm-danger:disabled {
  opacity: 0.5;
}
</style>
