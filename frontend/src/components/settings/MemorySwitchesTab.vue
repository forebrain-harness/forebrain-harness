<template>
  <!-- Memory (plan 191 §0.16, §4.3): one switch drawn in the group-title type
       with its own explanation, the three groups that follow it only while it is
       on — following the form's value, not the stored one — and 「重置记忆」,
       which is always there. -->
  <div class="grid gap-4">
    <SettingsTabForm :tab="tab" :form="form">
      <div class="flex flex-wrap items-center gap-2">
        <Button
          type="button"
          class="forebrain-btn forebrain-btn--danger h-auto bg-transparent text-xs hover:bg-transparent"
          :disabled="resetting"
          data-testid="memory-switches-reset"
          @click="resetMemories"
        >
          {{ resetting ? t('common.loading') : t('memories.reset') }}
        </Button>
      </div>
      <p v-if="notice" class="text-[12px] text-[var(--forebrain-brand-1)]">{{ notice }}</p>
      <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>
    </SettingsTabForm>
  </div>
</template>

<script setup lang="ts">
/**
 * The memory tab. Everything it draws — the switch, the models, the search
 * budgets, the session handling — comes from the schema's `memory` groups (the
 * switch being the one global runtime flag memory has, plan 205 §3.1); the tab
 * adds only the reset action, which wipes the stored memories rather than a
 * setting and so has no row of its own.
 */
import { ref } from 'vue'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import SettingsTabForm from '@/components/settings/form/SettingsTabForm.vue'
import type { useSettingsForm } from '@/components/settings/form/useSettingsForm'
import forebrainApi, { getErrorMessage } from '@/lib/api'
import { useI18n } from '@/locales'

defineProps<{
  tab: string
  form: ReturnType<typeof useSettingsForm>
}>()

const { t } = useI18n()
const resetting = ref(false)
const notice = ref('')
const error = ref('')

async function resetMemories(): Promise<void> {
  if (!window.confirm(t('memories.resetConfirm'))) return
  resetting.value = true
  notice.value = ''
  error.value = ''
  try {
    await forebrainApi.resetMemories()
    notice.value = t('memories.resetDone')
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    resetting.value = false
  }
}
</script>
