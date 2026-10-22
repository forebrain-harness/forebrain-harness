<template>
  <!-- The global subagent switch, flat in its tab (plan 079): the one switch,
       what it covers, and the save that writes agents.defaults.enable_subagent.
       A primary agent's own switch lives on the menu bar's 子代理 page. -->
  <div class="grid gap-3">
    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

    <div class="grid">
      <SwitchRow
        v-model="draft"
        :title="t('settings.subagentFeature')"
        :hint="t('settings.subagentFeatureHint')"
        :disabled="loading || saving"
        data-testid="subagents-feature-switch"
      />
    </div>

    <ListNote>{{ t('settings.subagentFeatureNote') }}</ListNote>

    <div class="flex flex-wrap items-center gap-2">
      <Button
        type="button"
        class="forebrain-btn forebrain-btn-primary h-auto text-xs"
        :disabled="!changed || saving"
        data-testid="subagents-feature-save"
        @click="save"
      >
        {{ saving ? t('common.loading') : t('common.save') }}
      </Button>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The global subagent switch: whether the model may dispatch subagents at all,
 * for every primary agent. It is the default each primary agent follows on the
 * menu bar's 子代理 page, where that one agent may override it.
 */
import { computed, onMounted, ref } from 'vue'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import ListNote from '@/components/common/ListNote.vue'
import SwitchRow from '@/components/common/SwitchRow.vue'
import forebrainApi from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()

/** The switch as the file has it, and the switch as the form shows it. */
const enabled = ref(false)
const draft = ref(false)
const loading = ref(false)
const saving = ref(false)
const error = ref('')

const changed = computed(() => draft.value !== enabled.value)

async function loadSettings() {
  loading.value = true
  error.value = ''
  try {
    const next = await forebrainApi.subagentDefaults()
    enabled.value = next.enabled
    draft.value = next.enabled
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause)
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  error.value = ''
  try {
    const next = await forebrainApi.setSubagentDefaults(draft.value)
    // The answer is the switch as written; the form holds the same value, so
    // the save button goes quiet again only once the file agreed.
    enabled.value = next.enabled
    draft.value = next.enabled
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : String(cause)
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  void loadSettings()
})
</script>
