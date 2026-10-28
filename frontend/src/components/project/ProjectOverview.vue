<template>
  <!-- The project's settings, flat in its tab (owner 2026-10-18): no card and
       no form title — the tab's own name says what this is. The root and the
       key are the project's identity on disk and in the state store, so they
       are shown, never edited. -->
  <form class="mx-auto flex max-w-4xl flex-col gap-3" @submit.prevent="save">
    <div class="grid gap-3 md:grid-cols-2">
      <label class="forebrain-form-field">
        <span>{{ t('projects.namePlaceholder') }}</span>
        <Input v-model="draft.name" class="forebrain-field h-[34px] w-full" data-testid="project-settings-name" />
      </label>
      <label class="forebrain-form-field">
        <span>{{ t('projects.fieldRoot') }}</span>
        <Input :model-value="project?.root ?? ''" class="forebrain-field h-[34px] w-full font-mono" readonly data-testid="project-settings-root" />
      </label>
    </div>

    <label class="forebrain-form-field">
      <span>{{ t('projects.fieldDescription') }}</span>
      <Input v-model="draft.description" class="forebrain-field h-[34px] w-full" :placeholder="t('projects.descriptionPlaceholder')" data-testid="project-settings-description" />
    </label>

    <!-- The type is a setting, not a label (plan 153): it is shown as the badge
         the project table draws, and changing it asks first, because it only
         reaches the sessions born after the change. -->
    <div class="forebrain-form-field">
      <span>{{ t('projects.typeCurrent') }}</span>
      <div class="flex flex-wrap items-center gap-2">
        <BadgeComponent :variant="currentTypeOption.badge" class="gap-1" data-testid="project-settings-type">
          <component :is="currentTypeOption.icon" class="size-3" aria-hidden="true" />
          {{ t(currentTypeOption.label) }}
        </BadgeComponent>
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs" data-testid="project-type-change" @click="openTypeChange">
          <Repeat class="size-3.5" aria-hidden="true" />{{ t('projects.typeChange') }}
        </Button>
        <span class="forebrain-list-note">{{ t('projects.typeAudienceHint') }}</span>
      </div>
    </div>

    <div class="grid gap-3 md:grid-cols-2">
      <label class="forebrain-form-field">
        <span>{{ t('projects.iconPlaceholder') }}</span>
        <Input v-model="draft.icon" class="forebrain-field h-[34px] w-full" data-testid="project-settings-icon" />
      </label>
      <label class="forebrain-form-field">
        <span>{{ t('projects.projectKey') }}</span>
        <Input :model-value="project?.projectKey ?? ''" class="forebrain-field h-[34px] w-full font-mono" readonly data-testid="project-settings-key" />
      </label>
    </div>

    <label class="forebrain-form-field">
      <span>{{ t('projects.instructionsLabel') }}</span>
      <Textarea v-model="draft.instructions" rows="3" class="forebrain-field min-h-0 w-full" data-testid="project-instructions" />
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
      <!-- Saving is the form's submit: pressing Enter in a field and clicking
           this button are the same act, and there is no click handler. -->
      <Button
        type="submit"
        class="forebrain-btn forebrain-btn-primary h-auto text-xs"
        :disabled="!changed || saving"
        data-testid="project-settings-save"
      >
        {{ saving ? t('common.loading') : t('common.save') }}
      </Button>
      <Button
        type="button"
        class="forebrain-btn forebrain-btn--danger h-auto bg-transparent text-xs hover:bg-transparent"
        data-testid="project-settings-delete"
        @click="deleteOpen = true"
      >
        {{ t('projects.deleteProject') }}
      </Button>
      <span v-if="formNotice" class="text-[12px] text-[var(--forebrain-text-2)]" data-testid="project-settings-saved">{{ formNotice }}</span>
    </div>
    <p v-if="formError" class="text-[12px] text-[var(--forebrain-danger)]">{{ formError }}</p>
  </form>

  <!-- 任务通知 belongs to a batch project only (plan 153 A12): its executions
       are the project's unattended work, and their ends are announced here. It
       writes itself, so it sits outside the form above. -->
  <div v-if="currentType === 'batch'" class="mx-auto mt-4 max-w-4xl">
    <ProjectTaskNotify :project="project" :project-id="projectId" @saved="emit('saved')" />
  </div>

  <!-- Changing a project's type asks first: a running conversation keeps the
       type it was born with, and a batch project needs its directory trusted. -->
  <ForebrainModal :open="typeChangeOpen" size="sm" :title="t('projects.typeChangeTitle')" @close="closeTypeChange">
    <div class="grid grid-cols-3 gap-2" role="radiogroup" :aria-label="t('projects.typeField')">
      <button
        v-for="option in PROJECT_TYPES"
        :key="option.value"
        type="button"
        class="grid justify-items-start gap-[3px] rounded-lg border px-2.5 py-[10px] text-left"
        :class="pendingType === option.value
          ? 'border-[var(--forebrain-brand-1)] bg-[var(--forebrain-brand-soft)]'
          : 'border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] hover:border-[var(--forebrain-brand-1)]'"
        role="radio"
        :aria-checked="pendingType === option.value"
        :data-testid="`project-type-card-${option.value}`"
        @click="pendingType = option.value"
      >
        <component
          :is="option.icon"
          class="size-4"
          :class="pendingType === option.value ? 'text-[var(--forebrain-brand-1)]' : 'text-[var(--forebrain-text-2)]'"
          aria-hidden="true"
        />
        <b class="text-[13px] font-semibold text-[var(--forebrain-text)]">{{ t(option.label) }}</b>
        <span class="text-[11.5px] leading-[1.45] text-[var(--forebrain-muted-text)]">{{ t(option.hint) }}</span>
      </button>
    </div>

    <p class="mt-3 text-[13px] text-[var(--forebrain-text-2)]" data-testid="project-type-ask">
      {{ t('projects.typeChangeAsk', { name: project?.name ?? '', from: t(currentTypeOption.label), to: t(pendingTypeOption.label) }) }}
    </p>
    <p class="mt-2 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] px-2.5 py-2 text-[12px] leading-[1.55] text-[var(--forebrain-text-2)]">
      {{ t('projects.typeChangeNote') }}
    </p>

    <div v-if="pendingType === 'batch'" class="mt-3">
      <SwitchRow v-model="typeChangeTrust" :title="t('projects.batchTrustLabel')" :hint="t('projects.batchTrustHint')" />
      <span class="mt-1 block text-[11px] text-[var(--forebrain-muted-text)]">{{ t('projects.batchTrustRequired') }}</span>
    </div>

    <p
      v-if="currentType === 'batch'"
      class="mt-2 rounded-lg border border-[var(--forebrain-warning)] bg-[var(--forebrain-bg-alt)] px-2.5 py-2 text-[12px] leading-[1.55] text-[var(--forebrain-text-2)]"
      data-testid="project-type-leave-batch"
    >{{ t('projects.typeLeaveBatchNote') }}</p>

    <p v-if="typeChangeError" class="mt-2 text-[12px] text-[var(--forebrain-danger)]">{{ typeChangeError }}</p>
    <template #footer>
      <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="closeTypeChange">{{ t('common.cancel') }}</Button>
      <Button
        type="button"
        class="forebrain-btn forebrain-btn-primary h-auto text-xs"
        :disabled="!canChangeType"
        data-testid="project-type-confirm"
        @click="confirmTypeChange"
      >
        {{ savingType ? t('common.loading') : t('projects.typeChangeConfirm') }}
      </Button>
    </template>
  </ForebrainModal>

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
import { Repeat } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import { Textarea } from '@repo/shadcn-vue/components/ui/textarea'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ProjectDeleteDialog from '@/components/project/ProjectDeleteDialog.vue'
import ProjectTaskNotify from '@/components/project/ProjectTaskNotify.vue'
import RadioField from '@/components/common/RadioField.vue'
import SwitchRow from '@/components/common/SwitchRow.vue'
import { getErrorMessage, forebrainApi, type ProjectRecord, type ProjectType } from '@/lib/api'
import { PROJECT_TYPES, projectTypeOption } from '@/lib/projectTypes'
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

// ---- The project's type (plan 153) ----

/** The kind the project is stored as; an unknown field reads as 创意编程. */
const currentType = computed<ProjectType>(() => props.project?.projectType ?? 'coding')
const currentTypeOption = computed(() => projectTypeOption(currentType.value))
const pendingType = ref<ProjectType>('coding')
const pendingTypeOption = computed(() => projectTypeOption(pendingType.value))
const typeChangeOpen = ref(false)
/** The trust the change records when it turns the project into a batch one. */
const typeChangeTrust = ref(false)
const savingType = ref(false)
const typeChangeError = ref('')

const canChangeType = computed(() =>
  !savingType.value && pendingType.value !== currentType.value
  && (pendingType.value !== 'batch' || typeChangeTrust.value))

/** The dialog opens on the next kind rather than the current one, so a click on
 *  确定修改 is the whole change; the cards above it pick any other. */
function openTypeChange() {
  const next = PROJECT_TYPES[(PROJECT_TYPES.findIndex((option) => option.value === currentType.value) + 1) % PROJECT_TYPES.length]
  pendingType.value = next.value
  typeChangeTrust.value = false
  typeChangeError.value = ''
  typeChangeOpen.value = true
}

function closeTypeChange() {
  typeChangeOpen.value = false
  typeChangeError.value = ''
}

/** The change is its own write: it carries the new type (and, for a batch
 *  project, the trust decision with it) and leaves the rest of the form alone. */
async function confirmTypeChange() {
  if (!props.project || !canChangeType.value) return
  savingType.value = true
  typeChangeError.value = ''
  try {
    await forebrainApi.projectUpdate(props.projectId, {
      projectType: pendingType.value,
      ...(pendingType.value === 'batch' ? { trust: typeChangeTrust.value } : {}),
    })
    typeChangeOpen.value = false
    formError.value = ''
    formNotice.value = t('projects.saved')
    emit('saved')
  } catch (cause) {
    const code = (cause as { response?: { data?: { error?: string } } }).response?.data?.error
    typeChangeError.value = code === 'batch_requires_trust'
      ? t('projects.errBatchRequiresTrust')
      : code === 'project_type_invalid'
        ? t('projects.errTypeInvalid')
        : getErrorMessage(cause)
  } finally {
    savingType.value = false
  }
}

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
