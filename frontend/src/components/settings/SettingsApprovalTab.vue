<template>
  <div class="pb-6">
    <CardComponent>
      <template #header>
        <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('approval.title') }}</div>
      </template>
      <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('approval.description') }}</p>

      <div class="space-y-2">
        <button
          v-for="preset in presets"
          :key="preset.id"
          type="button"
          class="approval-card"
          :class="{ 'approval-card--active': current === preset.id }"
          role="radio"
          :aria-checked="current === preset.id"
          :data-testid="`approval-${preset.id}`"
          @click="select(preset.id)"
        >
          <span class="flex items-center gap-2">
            <span class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ preset.label }}</span>
            <span v-if="current === preset.id" class="rounded-full border border-[var(--forebrain-brand-1)] px-2 py-0.5 text-[10px] text-[var(--forebrain-brand-1)]">{{ t('approval.current') }}</span>
          </span>
          <span class="mt-1 block text-left text-[12px] leading-relaxed text-[var(--forebrain-text-2)]">{{ preset.description }}</span>
        </button>
      </div>

      <p v-if="current === null && loaded" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('approval.custom') }}</p>
      <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>
      <p v-if="notice" class="text-[12px] text-[var(--forebrain-text-2)]">{{ notice }}</p>
    </CardComponent>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import CardComponent from '@/components/common/CardComponent.vue'
import { getErrorMessage, forebrainApi } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The global approval default: the same three presets the terminal's
 * /permissions offers. Selection persists to forebrain.yaml; sessions may
 * override their own view from the composer.
 */
const { t } = useI18n()

type Preset = { id: string; label: string; description: string }

const presets: Preset[] = [
  { id: 'read-only', label: t('approval.readOnly'), description: t('approval.readOnlyDescription') },
  { id: 'auto', label: t('approval.default'), description: t('approval.defaultDescription') },
  { id: 'full-access', label: t('approval.fullAccess'), description: t('approval.fullAccessDescription') },
]

const current = ref<string | null>(null)
const loaded = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')

async function load() {
  error.value = ''
  try {
    const res = await forebrainApi.approvalDefault()
    current.value = res.current
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    loaded.value = true
  }
}

async function select(id: string) {
  if (saving.value || current.value === id) return
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    const res = await forebrainApi.saveApprovalDefault(id)
    current.value = res.current
    notice.value = t('approval.saved')
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  void load()
})
</script>

<style scoped>
.approval-card {
  display: block;
  width: 100%;
  text-align: left;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  padding: 12px 14px;
  background: var(--forebrain-surface);
}
.approval-card:hover {
  background: var(--forebrain-button-alt-bg);
}
.approval-card--active {
  border: 2px solid var(--forebrain-brand-1);
  background: var(--forebrain-brand-soft);
}
</style>
