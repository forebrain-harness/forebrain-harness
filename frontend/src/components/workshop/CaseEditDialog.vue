<template>
  <ForebrainModal :open="open" size="sm" :title="title" @close="emit('cancel')">
    <label class="block">
      <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('workshop.caseQuestion') }}</span>
      <Textarea v-model="question" rows="3" class="forebrain-field min-h-0 w-full" :placeholder="t('workshop.caseQuestionPlaceholder')" data-testid="workshop-case-question" />
    </label>
    <label class="block">
      <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('workshop.caseExpected') }}</span>
      <Textarea v-model="expected" rows="2" class="forebrain-field min-h-0 w-full" :placeholder="t('workshop.caseExpectedPlaceholder')" data-testid="workshop-case-expected" />
    </label>
    <template #footer>
      <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="emit('cancel')">{{ t('common.cancel') }}</Button>
      <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" :disabled="!question.trim()" data-testid="workshop-case-save" @click="save">
        {{ confirmLabel }}
      </Button>
    </template>
  </ForebrainModal>
</template>

<script setup lang="ts">
/** One test case's question and expected result, edited in a modal. */
import { ref, watch } from 'vue'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Textarea } from '@repo/shadcn-vue/components/ui/textarea'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import { useI18n } from '@/locales'

const props = defineProps<{
  open: boolean
  title: string
  confirmLabel: string
  question: string
  expected: string
}>()

const emit = defineEmits<{ save: [payload: { question: string; expected: string }]; cancel: [] }>()
const { t } = useI18n()

const question = ref('')
const expected = ref('')

watch(
  () => props.open,
  (open) => {
    if (open) {
      question.value = props.question
      expected.value = props.expected
    }
  },
  { immediate: true },
)

function save() {
  emit('save', { question: question.value.trim(), expected: expected.value.trim() })
}
</script>
