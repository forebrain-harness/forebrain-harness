<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto w-full max-w-5xl">
      <PageTopRow :title="t('rules.title')" :description="t('rules.agentDescription')">
        <template #actions>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" data-testid="rules-create" @click="openCreate">
            {{ t('rules.newFile') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="loadList">
            {{ t('common.refresh') }}
          </button>
        </template>
      </PageTopRow>

      <p v-if="error" class="mb-4 rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>
      <p v-if="notice" class="mb-4 text-[12px] text-[var(--forebrain-brand-1)]">{{ notice }}</p>

      <div v-if="loading && !files.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!files.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('rules.empty') }}</div>
      <div v-else class="forebrain-table-wrap">
        <table class="forebrain-table">
          <thead>
            <tr>
              <th>{{ t('rules.colFile') }}</th>
              <th>{{ t('rules.colPurpose') }}</th>
              <th>{{ t('rules.colUpdated') }}</th>
              <th class="text-right">{{ t('rules.colActions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="file in files" :key="file.name" data-testid="rule-file-row" :data-rule-file="file.name">
              <td class="font-mono">
                {{ file.name }}
                <span v-if="!file.exists" class="ml-1 rounded-full border border-[var(--forebrain-divider)] px-1.5 py-0.5 text-[10px] text-[var(--forebrain-muted-text)]">{{ t('rules.notCreated') }}</span>
              </td>
              <td class="text-[var(--forebrain-text-2)]">{{ purposeFor(file.name) }}</td>
              <td class="font-mono text-[var(--forebrain-text-2)]">{{ formatTime(file.updatedAt) }}</td>
              <td class="text-right">
                <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" :data-testid="`rule-edit-${file.name}`" @click="openEdit(file.name)">
                  {{ t('rules.editFile') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="mt-3 text-[11px] text-[var(--forebrain-muted-text)]">{{ t('rules.agentWhere') }}；{{ t('rules.applyShort') }}</p>
    </div>

    <ForebrainModal :open="creating" size="sm" :title="t('rules.createModalTitle')" @close="creating = false">
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('rules.fileLabel') }}</span>
        <select v-model="createName" class="forebrain-field w-full text-[13px]" data-testid="rules-create-select">
          <option v-for="name in creatable" :key="name" :value="name">{{ name }}</option>
        </select>
      </label>
      <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('rules.createOnlyHint') }}</p>
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="creating = false">{{ t('common.cancel') }}</button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!createName" data-testid="rules-create-ok" @click="confirmCreate">{{ t('rules.create') }}</button>
      </template>
    </ForebrainModal>

    <ForebrainModal :open="editing !== ''" size="xl" :title="editing" @close="closeEdit">
      <div data-testid="rules-editor-modal">
        <p v-if="warning" class="mb-2 text-[12px] text-[var(--forebrain-danger)]">{{ t('rules.budgetWarning') }}</p>
        <div v-if="!loaded" class="min-h-[200px]">{{ t('common.loading') }}</div>
        <FileEditor v-else :key="editing" height="420px" :path="editing" v-model="draft" @dirty="dirty = $event" />
      </div>
      <p class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('rules.applyShort') }}</p>
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="closeEdit">{{ t('common.cancel') }}</button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving || !dirty" data-testid="rules-save" @click="save">
          {{ saving ? t('common.loading') : t('common.save') }}
        </button>
      </template>
    </ForebrainModal>
  </div>
</template>

<script setup lang="ts">
import { computed, defineAsyncComponent, onMounted, ref } from 'vue'
import PageTopRow from '@/components/common/PageTopRow.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import { getErrorMessage, forebrainApi } from '@/lib/api'
import { useI18n } from '@/locales'

const FileEditor = defineAsyncComponent(() => import('@/components/editor/FileEditor.vue'))

/**
 * The primary agent's instruction files (AGENTS.md / SOUL.md / USER.md) as a
 * list: the files themselves, and the buttons above it. Creating and editing
 * happen in modals, so the page never grows an inline form. Editing them is
 * editing what every session of this tenant is told before its first word;
 * changes start with the next session, never mid-run.
 */
const { t } = useI18n()

type FileRow = { name: string; exists: boolean; sizeBytes?: number; updatedAt?: number }

const files = ref<FileRow[]>([])
const loading = ref(false)
const error = ref('')
const notice = ref('')
const creating = ref(false)
const createName = ref('')
const editing = ref('')
const loaded = ref(false)
const draft = ref('')
const dirty = ref(false)
const saving = ref(false)
const warning = ref(false)

const creatable = computed(() => files.value.filter((file) => !file.exists).map((file) => file.name))

const purposes: Record<string, () => string> = {
  'AGENTS.md': () => t('rules.purposeAgents'),
  'SOUL.md': () => t('rules.purposeSoul'),
  'USER.md': () => t('rules.purposeUser'),
}

function purposeFor(name: string): string {
  return purposes[name] ? purposes[name]() : ''
}

function formatTime(unix?: number): string {
  if (!unix) return '—'
  return new Date(unix * 1000).toLocaleString()
}

async function loadList() {
  loading.value = true
  error.value = ''
  try {
    const res = await forebrainApi.agentRuleFiles()
    files.value = res.files ?? []
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  createName.value = creatable.value[0] ?? ''
  creating.value = true
}

function confirmCreate() {
  if (!createName.value) return
  const name = createName.value
  creating.value = false
  void openEdit(name)
}

async function openEdit(name: string) {
  editing.value = name
  loaded.value = false
  dirty.value = false
  warning.value = false
  notice.value = ''
  try {
    const file = await forebrainApi.agentRuleFile(name)
    // A file not created yet reads as empty; saving it creates it.
    draft.value = file.content
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    loaded.value = true
  }
}

function closeEdit() {
  editing.value = ''
  loaded.value = false
  dirty.value = false
}

async function save() {
  if (saving.value || !dirty.value) return
  saving.value = true
  error.value = ''
  notice.value = ''
  warning.value = false
  try {
    const res = await forebrainApi.saveAgentRuleFile(editing.value, draft.value)
    warning.value = res.warning === 'exceeds_assembly_budget'
    notice.value = t('rules.savedNotice')
    dirty.value = false
    closeEdit()
    await loadList()
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

onMounted(loadList)
</script>
