<template>
  <div class="space-y-4 pb-6">
    <p v-if="error" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

    <!-- The list: every primary agent of this install. Switching happens in
         the rail's selector only — this page manages definitions. -->
    <CardComponent
      v-for="agent in records"
      :key="agent.id"
      data-testid="agent-card"
      :style="agent.active ? { '--forebrain-card-border': 'var(--forebrain-brand-border)' } : undefined"
    >
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="min-w-0">
          <div class="flex items-center gap-2">
            <span class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ agent.name || agent.id }}</span>
            <span
              v-if="agent.active"
              class="rounded-full border border-[var(--forebrain-brand-border)] bg-[var(--forebrain-brand-soft)] px-2 py-0.5 text-[11px] text-[var(--forebrain-brand-1)]"
            >{{ t('agents.currentTag') }}</span>
          </div>
          <div class="mt-1 font-mono text-[11px] text-[var(--forebrain-muted-text)]">{{ agent.id }}</div>
          <div class="mt-0.5 truncate font-mono text-[11px] text-[var(--forebrain-muted-text)]" :title="agent.workspaceRoot">{{ agent.workspaceRoot }}</div>
          <p v-if="agent.description" class="mt-1 text-[12px] text-[var(--forebrain-text-2)]">{{ agent.description }}</p>
        </div>
        <div class="flex gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="startEdit(agent)">{{ t('common.edit') }}</button>
          <button
            type="button"
            class="forebrain-btn forebrain-btn-ghost text-xs text-[var(--forebrain-danger)]"
            :disabled="agent.active || agent.id === 'main'"
            :title="deleteDisabledTitle(agent)"
            @click="confirmDelete(agent)"
          >{{ t('common.delete') }}</button>
        </div>
      </div>
    </CardComponent>

    <!-- Create / edit form -->
    <CardComponent data-testid="agent-form">
      <template #header>
        <div class="text-[14px] font-medium text-[var(--forebrain-text)]">
          {{ editing ? t('agents.editTitle', { id: editing }) : t('agents.createTitle') }}
        </div>
      </template>
      <div class="grid gap-3 md:grid-cols-2">
        <label class="block">
          <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('agents.idLabel') }}</span>
          <input
            v-model="form.id"
            class="forebrain-field mt-1 w-full font-mono"
            :disabled="editing !== null"
            :placeholder="t('agents.idPlaceholder')"
          />
          <span v-if="!editing" class="mt-1 block text-[11px] text-[var(--forebrain-muted-text)]">{{ t('agents.idHint') }}</span>
        </label>
        <label class="block">
          <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('agents.nameLabel') }}</span>
          <input v-model="form.name" class="forebrain-field mt-1 w-full" :placeholder="t('agents.namePlaceholder')" />
        </label>
        <label class="block md:col-span-2">
          <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('agents.descriptionLabel') }}</span>
          <textarea v-model="form.description" rows="2" class="forebrain-field mt-1 w-full" :placeholder="t('agents.descriptionPlaceholder')" />
        </label>
      </div>
      <p v-if="formError" class="text-[12px] text-[var(--forebrain-danger)]">{{ formError }}</p>
      <div class="flex gap-2">
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="busy || !formValid" @click="submit">
          {{ editing ? t('common.save') : t('common.create') }}
        </button>
        <button v-if="editing" type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="cancelEdit">{{ t('common.cancel') }}</button>
      </div>
    </CardComponent>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import CardComponent from '@/components/common/CardComponent.vue'
import { getErrorMessage, forebrainApi, type PrimaryAgentRecord } from '@/lib/api'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'
import { useI18n } from '@/locales'

/**
 * Primary agents are tenants. The ID keys the state database and the
 * workspace layout, so it is immutable after creation: the edit form shows
 * it read-only, and deletion only removes the definition — workspace
 * directories and history stay on disk for the operator to clean up.
 */
const { t } = useI18n()
const { records, loadPrimaryAgents } = usePrimaryAgents()

const editing = ref<string | null>(null)
const form = ref({ id: '', name: '', description: '' })
const busy = ref(false)
const formError = ref('')
const error = ref('')

const formValid = computed(() => (editing.value ? true : /^[a-z][a-z0-9_-]{0,62}$/.test(form.value.id.trim())))

onMounted(() => {
  void loadPrimaryAgents().catch((cause) => {
    error.value = getErrorMessage(cause)
  })
})

function startEdit(agent: PrimaryAgentRecord) {
  editing.value = agent.id
  form.value = { id: agent.id, name: agent.name ?? '', description: agent.description ?? '' }
  formError.value = ''
}

function cancelEdit() {
  editing.value = null
  form.value = { id: '', name: '', description: '' }
  formError.value = ''
}

function deleteDisabledTitle(agent: PrimaryAgentRecord): string {
  if (agent.active) return t('agents.deleteActiveRefused')
  if (agent.id === 'main') return t('agents.deleteDefaultRefused')
  return t('agents.deleteHint')
}

function confirmDelete(agent: PrimaryAgentRecord) {
  if (agent.active || agent.id === 'main') return
  if (!window.confirm(t('agents.deleteConfirm', { id: agent.id }))) return
  busy.value = true
  error.value = ''
  forebrainApi.deletePrimaryAgent(agent.id)
    .then(() => loadPrimaryAgents())
    .catch((cause) => {
      error.value = getErrorMessage(cause)
    })
    .finally(() => {
      busy.value = false
    })
}

async function submit() {
  if (busy.value) return
  busy.value = true
  formError.value = ''
  try {
    if (editing.value) {
      await forebrainApi.updatePrimaryAgent(editing.value, {
        name: form.value.name.trim(),
        description: form.value.description.trim(),
      })
    } else {
      await forebrainApi.createPrimaryAgent({
        id: form.value.id.trim(),
        name: form.value.name.trim(),
        description: form.value.description.trim(),
      })
    }
    cancelEdit()
    await loadPrimaryAgents()
  } catch (cause) {
    formError.value = getErrorMessage(cause)
  } finally {
    busy.value = false
  }
}
</script>
