<template>
  <div class="mx-auto max-w-4xl space-y-6">
    <!-- Trust gate: project rules only load for a trusted project. -->
    <div v-if="!trusted" class="rounded-lg border border-[var(--forebrain-brand-border)] bg-[var(--forebrain-brand-soft)] p-4">
      <div class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.trustTitle') }}</div>
      <p class="mt-1 text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.trustHint') }}</p>
      <button type="button" class="forebrain-btn forebrain-btn-primary mt-3 text-xs" :disabled="trusting" data-testid="rules-trust" @click="trust">
        {{ trusting ? t('common.loading') : t('projects.trustLabel') }}
      </button>
    </div>

    <template v-else>
      <section>
        <PageTopRow :title="t('rules.projectInstrTitle')" :description="t('rules.projectInstrDesc')">
          <template #actions>
            <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!instrDirty || savingInstr" data-testid="project-instructions-save" @click="saveInstructions">
              {{ savingInstr ? t('common.loading') : t('common.save') }}
            </button>
          </template>
        </PageTopRow>
        <p v-if="instrError" class="mb-2 text-[12px] text-[var(--forebrain-danger)]">{{ instrError }}</p>
        <p v-if="instrNotice" class="mb-2 text-[12px] text-[var(--forebrain-brand-1)]">{{ instrNotice }}</p>
        <textarea v-model="instructions" rows="3" class="forebrain-field w-full" data-testid="project-instructions" />
      </section>

      <section>
        <PageTopRow :title="t('rules.projectFilesTitle')" :description="t('rules.projectFilesDesc')">
          <template #actions>
            <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" data-testid="rules-create" @click="openCreate">
              {{ t('rules.newFile') }}
            </button>
          </template>
        </PageTopRow>

        <p v-if="error" class="mb-3 text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>
        <p v-if="notice" class="mb-3 text-[12px] text-[var(--forebrain-brand-1)]">{{ notice }}</p>

        <div v-if="loading && !files.length" class="py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
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
              <tr v-for="file in files" :key="file.dir || '__root__'" data-testid="rule-file-row" :data-rule-file="label(file.dir)">
                <td class="font-mono">
                  {{ label(file.dir) }}
                  <span v-if="!file.exists" class="ml-1 rounded-full border border-[var(--forebrain-divider)] px-1.5 py-0.5 text-[10px] text-[var(--forebrain-muted-text)]">{{ t('rules.notCreated') }}</span>
                </td>
                <td class="text-[var(--forebrain-text-2)]">{{ file.dir ? t('rules.purposeSubdir') : t('rules.purposeRoot') }}</td>
                <td class="font-mono text-[var(--forebrain-text-2)]">{{ formatTime(file.updatedAt) }}</td>
                <td class="text-right">
                  <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" :data-testid="`rule-edit-${label(file.dir)}`" @click="openEdit(file.dir)">
                    {{ t('rules.editFile') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <p class="mt-3 text-[11px] text-[var(--forebrain-muted-text)]">{{ t('rules.projectWhere') }}；{{ t('rules.applyShort') }}</p>
      </section>
    </template>

    <ForebrainModal :open="creating" size="sm" :title="t('rules.createModalTitle')" @close="creating = false">
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('rules.typeLabel') }}</span>
        <select class="forebrain-field w-full text-[13px]" data-testid="rules-create-type">
          <option>{{ t('rules.subdirType') }}</option>
        </select>
      </label>
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('rules.dirLabel') }}</span>
        <select v-model="createDir" class="forebrain-field w-full text-[13px]" data-testid="rules-create-select">
          <option v-for="dir in creatable" :key="dir" :value="dir">{{ dir }}</option>
        </select>
      </label>
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="creating = false">{{ t('common.cancel') }}</button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!createDir" data-testid="rules-create-ok" @click="confirmCreate">{{ t('rules.create') }}</button>
      </template>
    </ForebrainModal>

    <ForebrainModal :open="editing !== null" size="xl" :title="label(editDir)" @close="closeEdit">
      <div data-testid="rules-editor-modal">
        <p v-if="warning" class="mb-2 text-[12px] text-[var(--forebrain-danger)]">{{ t('rules.budgetWarning') }}</p>
        <div v-if="!loaded" class="min-h-[200px]">{{ t('common.loading') }}</div>
        <FileEditor v-else :key="editDir" height="420px" :path="label(editDir)" v-model="draft" @dirty="dirty = $event" />
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
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import PageTopRow from '@/components/common/PageTopRow.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import { getErrorMessage, forebrainApi, type ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const FileEditor = defineAsyncComponent(() => import('@/components/editor/FileEditor.vue'))

/**
 * This project's rules tab: the project instructions every session in it is
 * told up front, over the FOREBRAIN.md chain — one file per directory layer,
 * root first. Both are lists-and-buttons; editing happens in modals. Only the
 * project's own files appear — the chain is assembled from its root, never
 * from anything inherited.
 */
const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()

const { t } = useI18n()

type FileRow = { dir: string; exists: boolean; sizeBytes?: number; updatedAt?: number }

const trusted = ref(false)
const trusting = ref(false)

const files = ref<FileRow[]>([])
const creatable = ref<string[]>([])
const loading = ref(false)
const error = ref('')
const notice = ref('')
const creating = ref(false)
const createDir = ref('')
const editing = ref<string | null>(null)
const editDir = computed(() => editing.value ?? '')
const loaded = ref(false)
const draft = ref('')
const dirty = ref(false)
const saving = ref(false)
const warning = ref(false)

const instructions = ref('')
const savedInstructions = ref('')
const savingInstr = ref(false)
const instrError = ref('')
const instrNotice = ref('')
const instrDirty = computed(() => instructions.value !== savedInstructions.value)

function label(dir: string): string {
  return dir ? `${dir}/FOREBRAIN.md` : 'FOREBRAIN.md'
}

function formatTime(unix?: number): string {
  if (!unix) return '—'
  return new Date(unix * 1000).toLocaleString()
}

watch(() => props.project?.trusted, (value) => {
  trusted.value = Boolean(value)
}, { immediate: true })

watch(() => props.project?.instructions, (value) => {
  instructions.value = value ?? ''
  savedInstructions.value = value ?? ''
}, { immediate: true })

async function loadList() {
  loading.value = true
  error.value = ''
  try {
    const res = await forebrainApi.projectRuleFiles(props.projectId)
    files.value = res.files ?? []
    creatable.value = res.create ?? []
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    loading.value = false
  }
}

async function trust() {
  if (trusting.value) return
  trusting.value = true
  error.value = ''
  try {
    await forebrainApi.projectUpdate(props.projectId, { trust: true })
    trusted.value = true
    await loadList()
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    trusting.value = false
  }
}

async function saveInstructions() {
  if (savingInstr.value || !instrDirty.value) return
  savingInstr.value = true
  instrError.value = ''
  instrNotice.value = ''
  try {
    await forebrainApi.projectUpdate(props.projectId, { instructions: instructions.value })
    savedInstructions.value = instructions.value
    instrNotice.value = t('projects.saved')
  } catch (cause) {
    instrError.value = getErrorMessage(cause)
  } finally {
    savingInstr.value = false
  }
}

function openCreate() {
  createDir.value = creatable.value[0] ?? ''
  creating.value = true
}

function confirmCreate() {
  if (!createDir.value) return
  const dir = createDir.value
  creating.value = false
  void openEdit(dir)
}

async function openEdit(dir: string) {
  editing.value = dir
  loaded.value = false
  dirty.value = false
  warning.value = false
  try {
    const file = await forebrainApi.projectRuleFile(props.projectId, dir)
    // A layer without a file yet reads as empty; saving it creates it.
    draft.value = file.content
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    loaded.value = true
  }
}

function closeEdit() {
  editing.value = null
  loaded.value = false
  dirty.value = false
}

async function save() {
  if (saving.value || !dirty.value || editing.value === null) return
  saving.value = true
  error.value = ''
  notice.value = ''
  warning.value = false
  try {
    const res = await forebrainApi.saveProjectRuleFile(props.projectId, editing.value, draft.value)
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

watch(trusted, (value) => {
  if (value) void loadList()
}, { immediate: true })
</script>
