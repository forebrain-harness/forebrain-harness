<template>
  <!-- The project's settings, flat in its tab (owner 2026-10-18): no card and
       no form title — the tab's own name says what this is. The root and the
       key are the project's identity on disk and in the state store, so they
       are shown, never edited. -->
  <form class="mx-auto flex max-w-4xl flex-col gap-3" @submit.prevent="save">
    <div class="grid gap-3 md:grid-cols-2">
      <label class="forebrain-form-field">
        <span>{{ t('projects.namePlaceholder') }}</span>
        <input v-model="draft.name" class="forebrain-field h-[34px] w-full" data-testid="project-settings-name" />
      </label>
      <label class="forebrain-form-field">
        <span>{{ t('projects.fieldRoot') }}</span>
        <input :value="project?.root ?? ''" class="forebrain-field h-[34px] w-full font-mono" readonly data-testid="project-settings-root" />
      </label>
    </div>

    <label class="forebrain-form-field">
      <span>{{ t('projects.fieldDescription') }}</span>
      <input v-model="draft.description" class="forebrain-field h-[34px] w-full" :placeholder="t('projects.descriptionPlaceholder')" data-testid="project-settings-description" />
    </label>

    <div class="grid gap-3 md:grid-cols-2">
      <label class="forebrain-form-field">
        <span>{{ t('projects.iconPlaceholder') }}</span>
        <input v-model="draft.icon" class="forebrain-field h-[34px] w-full" data-testid="project-settings-icon" />
      </label>
      <label class="forebrain-form-field">
        <span>{{ t('projects.projectKey') }}</span>
        <input :value="project?.projectKey ?? ''" class="forebrain-field h-[34px] w-full font-mono" readonly data-testid="project-settings-key" />
      </label>
    </div>

    <label class="forebrain-form-field">
      <span>{{ t('projects.instructionsLabel') }}</span>
      <textarea v-model="draft.instructions" rows="3" class="forebrain-field w-full" data-testid="project-instructions" />
      <span class="forebrain-list-note">{{ t('projects.instructionsHint') }}</span>
    </label>

    <RadioField
      v-model="draft.memoryScope"
      :label="t('projects.memoryScopeLabel')"
      name="project-memory-scope"
      :options="memoryOptions"
      :hint="memoryHint"
    />

    <div class="grid">
      <SwitchRow
        v-for="row in switchRows"
        :key="row.key"
        v-model="draft[row.key]"
        :title="row.title"
        :hint="row.hint"
      />
    </div>

    <div class="flex flex-wrap items-center gap-2">
      <button
        type="submit"
        class="forebrain-btn forebrain-btn-primary text-xs"
        :disabled="!changed || saving"
        data-testid="project-settings-save"
      >
        {{ saving ? t('common.loading') : t('common.save') }}
      </button>
      <button
        type="button"
        class="forebrain-btn forebrain-btn--danger text-xs"
        data-testid="project-settings-delete"
        @click="deleteOpen = true"
      >
        {{ t('projects.deleteProject') }}
      </button>
      <span v-if="formNotice" class="text-[12px] text-[var(--forebrain-text-2)]" data-testid="project-settings-saved">{{ formNotice }}</span>
    </div>
    <p v-if="formError" class="text-[12px] text-[var(--forebrain-danger)]">{{ formError }}</p>
  </form>

  <ProjectDeleteDialog
    v-if="deleteOpen && project"
    :project="project"
    @cancel="closeDelete"
    @deleted="removeProject"
  />
</template>

<script setup lang="ts">
/**
 * The project's settings — the only entry a project-level setting has
 * (owner 2026-10-18): name, description, icon, project instructions, the
 * memory scope, trust in the directory and reading outside it. Pinning lives
 * in the projects table's pin icon; deleting sits beside saving, behind the
 * same typed confirmation the table uses.
 */
import { computed, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import ProjectDeleteDialog from '@/components/project/ProjectDeleteDialog.vue'
import RadioField from '@/components/common/RadioField.vue'
import SwitchRow from '@/components/common/SwitchRow.vue'
import { getErrorMessage, forebrainApi, type ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()
/** Saving is the parent's to hear: it reloads the project, so the top bar's
 *  trust badge and every tab's trust gate follow the new settings. */
const emit = defineEmits<{ saved: [] }>()

const { t } = useI18n()
const router = useRouter()

const draft = reactive({
  name: '',
  icon: '',
  description: '',
  instructions: '',
  memoryScope: 'shared',
  resourceAccess: false,
  trust: false,
})
const saving = ref(false)
const formError = ref('')
const formNotice = ref('')
const deleteOpen = ref(false)

watch(() => props.project, (project) => {
  if (!project) return
  Object.assign(draft, {
    name: project.name,
    icon: project.icon,
    description: project.description,
    instructions: project.instructions,
    memoryScope: project.memoryScope || 'shared',
    resourceAccess: Boolean(project.resourceAccess),
    trust: Boolean(project.trusted),
  })
}, { immediate: true })

const changed = computed(() => {
  const p = props.project
  if (!p) return false
  return draft.name !== p.name
    || draft.icon !== p.icon
    || draft.description !== p.description
    || draft.instructions !== (p.instructions ?? '')
    || draft.memoryScope !== (p.memoryScope || 'shared')
    || draft.resourceAccess !== Boolean(p.resourceAccess)
    || draft.trust !== Boolean(p.trusted)
})

const trustChanged = computed(() => Boolean(props.project) && draft.trust !== Boolean(props.project?.trusted))

const memoryOptions = computed(() => [
  { value: 'shared', label: t('projects.memoryShared') },
  { value: 'project_only', label: t('projects.memoryProjectOnly') },
])

/** What the chosen scope means; it changes with the choice (owner 2026-10-18). */
const memoryHint = computed(() => (draft.memoryScope === 'project_only'
  ? t('projects.memoryScopeProjectHint')
  : t('projects.memoryScopeSharedHint')))

const switchRows = computed(() => [
  { key: 'trust' as const, title: t('projects.trustLabel'), hint: t('projects.trustHint') },
  { key: 'resourceAccess' as const, title: t('projects.resourceAccessLabel'), hint: t('projects.resourceAccessHint') },
])

async function save() {
  // Pressing Enter in a field submits the form even while 保存 is disabled;
  // an unchanged form has nothing to send.
  if (saving.value || !props.project || !changed.value) return
  saving.value = true
  formError.value = ''
  formNotice.value = ''
  try {
    const payload: Partial<{
      name: string
      icon: string
      description: string
      instructions: string
      memoryScope: string
      resourceAccess: boolean
      trust: boolean
    }> = {
      name: draft.name.trim(),
      icon: draft.icon.trim(),
      description: draft.description,
      instructions: draft.instructions,
      memoryScope: draft.memoryScope,
      resourceAccess: draft.resourceAccess,
    }
    if (trustChanged.value) payload.trust = draft.trust
    await forebrainApi.projectUpdate(props.projectId, payload)
    // The project's PATCH can record a trust, never withdraw one; the
    // directory's own trust record is what the gates read, so taking the
    // trust back goes through that record's setter.
    if (trustChanged.value && !draft.trust) {
      await forebrainApi.hostTrustSet(props.project.root, false)
    }
    emit('saved')
    formNotice.value = t('projects.saved')
  } catch (cause) {
    formError.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

function closeDelete(error?: string) {
  if (error) formError.value = error
  deleteOpen.value = false
}

async function removeProject() {
  if (!props.project) return
  deleteOpen.value = false
  formError.value = ''
  try {
    await forebrainApi.projectDelete(props.projectId)
    await router.push('/projects')
  } catch (cause) {
    formError.value = isProjectBusy(cause) ? t('projects.deleteBusy') : getErrorMessage(cause)
  }
}

/** A 409 from the delete is the project-busy refusal: a run is in flight. */
function isProjectBusy(cause: unknown): boolean {
  const response = (cause as { response?: { status?: number; data?: { error?: string } } }).response
  return response?.status === 409 && response.data?.error === 'project_busy'
}
</script>
