<template>
  <ForebrainModal :open="open" size="xl" :title="title" @close="emit('cancel')">
    <div v-if="item" class="space-y-3">
      <div class="rounded-lg border border-[var(--forebrain-divider)] p-2.5">
        <div class="text-[13px] text-[var(--forebrain-text)]">{{ item.prompt || '—' }}</div>
        <div class="mt-0.5 text-[11px] text-[var(--forebrain-muted-text)]">{{ t('workshop.caseExpectedLabel', { text: item.expected || '—' }) }}</div>
      </div>
      <div class="grid grid-cols-2 gap-3">
        <div v-for="config in ['with_skill', 'without_skill']" :key="config">
          <div class="mb-1 flex items-center gap-2 text-[12px] text-[var(--forebrain-text-2)]">
            {{ config === 'with_skill' ? t('workshop.evalWithSkill') : t('workshop.evalWithoutSkill') }}
            <BadgeComponent :variant="passed(config) ? 'success' : 'error'">
              {{ passed(config) ? t('workshop.evalPass') : t('workshop.evalFail') }}
            </BadgeComponent>
          </div>
          <pre class="max-h-[220px] overflow-auto rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] p-2 text-[11px] leading-relaxed whitespace-pre-wrap text-[var(--forebrain-text-2)]">{{ output(config) }}</pre>
        </div>
      </div>
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('workshop.yourFeedback') }}</span>
        <textarea v-model="feedback" rows="2" class="forebrain-field w-full" :placeholder="t('workshop.feedbackPlaceholder')" data-testid="workshop-eval-feedback" />
      </label>
    </div>
    <template #footer>
      <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="emit('cancel')">{{ t('common.cancel') }}</button>
      <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" data-testid="workshop-eval-feedback-save" @click="save">{{ t('common.save') }}</button>
    </template>
  </ForebrainModal>
</template>

<script setup lang="ts">
/** One evaluation case's two runs side by side, with a feedback box saved
 *  back into the round's feedback.json. */
import { ref, watch } from 'vue'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import type { WorkshopEvalCase } from '@/lib/api'
import { useI18n } from '@/locales'

const props = defineProps<{ open: boolean; title: string; item: WorkshopEvalCase | null }>()
const emit = defineEmits<{ save: [payload: { runId: string; feedback: string }]; cancel: [] }>()
const { t } = useI18n()

const feedback = ref('')

watch(
  () => props.open,
  (open) => {
    if (open) feedback.value = props.item?.feedback ?? ''
  },
  { immediate: true },
)

function passed(config: string): boolean {
  const c = props.item?.configs?.[config]
  return !!c && c.present && c.total > 0 && c.passed === c.total
}

function output(config: string): string {
  return props.item?.configs?.[config]?.output || '—'
}

function save() {
  if (!props.item) return
  emit('save', { runId: props.item.runId, feedback: feedback.value.trim() })
}
</script>
