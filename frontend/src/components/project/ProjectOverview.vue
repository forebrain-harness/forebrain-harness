<template>
  <div class="mx-auto max-w-4xl space-y-5">
    <!-- Overview card -->
    <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4">
      <div class="flex items-center gap-2">
        <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('projects.overviewTitle') }}</div>
        <span class="scope-badge">{{ t('scope.project') }}</span>
      </div>
      <dl class="mt-3 grid gap-x-6 gap-y-2 text-[13px] sm:grid-cols-2">
        <div class="flex gap-2"><dt class="text-[var(--forebrain-muted-text)]">{{ t('projects.fieldDescription') }}</dt><dd class="min-w-0 flex-1 break-words text-[var(--forebrain-text)]">{{ project?.description || '—' }}</dd></div>
        <div class="flex gap-2"><dt class="text-[var(--forebrain-muted-text)]">{{ t('projects.fieldRoot') }}</dt><dd class="min-w-0 flex-1 break-all font-mono text-[12px] text-[var(--forebrain-text)]">{{ project?.root }}</dd></div>
        <div class="flex gap-2"><dt class="text-[var(--forebrain-muted-text)]">ProjectKey</dt><dd class="min-w-0 flex-1 break-all font-mono text-[12px] text-[var(--forebrain-text)]">{{ project?.projectKey }}</dd></div>
        <div class="flex gap-2"><dt class="text-[var(--forebrain-muted-text)]">{{ t('projects.fieldUpdated') }}</dt><dd class="text-[var(--forebrain-text)]">{{ formatTime(project?.updatedAt) }}</dd></div>
        <div class="flex gap-2"><dt class="text-[var(--forebrain-muted-text)]">{{ t('projects.fieldCreated') }}</dt><dd class="text-[var(--forebrain-text)]">{{ formatTime(project?.createdAt) }}</dd></div>
        <div class="flex gap-2"><dt class="text-[var(--forebrain-muted-text)]">{{ t('projects.fieldArchived') }}</dt><dd class="text-[var(--forebrain-text)]">{{ project?.archivedAt ? t('projects.archived') : t('projects.live') }}</dd></div>
      </dl>
    </section>

    <!-- Project settings card -->
    <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4">
      <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('projects.settingsTitle') }}</div>
      <div class="mt-3 grid gap-3 md:grid-cols-2">
        <label class="block">
          <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('projects.namePlaceholder') }}</span>
          <input v-model="draft.name" class="forebrain-field mt-1 w-full" />
        </label>
        <label class="block">
          <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('projects.iconPlaceholder') }}</span>
          <input v-model="draft.icon" class="forebrain-field mt-1 w-full" />
        </label>
        <label class="block md:col-span-2">
          <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('projects.descriptionPlaceholder') }}</span>
          <textarea v-model="draft.description" rows="2" class="forebrain-field mt-1 w-full" />
        </label>
        <label class="block md:col-span-2">
          <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('projects.instructionsLabel') }}</span>
          <textarea v-model="draft.instructions" rows="5" class="forebrain-field mt-1 w-full" :placeholder="t('projects.instructionsPlaceholder')" />
          <span class="mt-1 block text-[11px] text-[var(--forebrain-muted-text)]">{{ t('projects.instructionsHint') }}</span>
        </label>
      </div>

      <div class="mt-4 space-y-3">
        <div>
          <div class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('projects.memoryScopeLabel') }}</div>
          <div class="mt-1 flex gap-2" role="radiogroup" :aria-label="t('projects.memoryScopeLabel')">
            <button
              type="button"
              role="radio"
              :aria-checked="draft.memoryScope === 'project_only'"
              class="rounded-lg border px-3 py-2 text-[13px]"
              :class="draft.memoryScope === 'project_only' ? 'border-[var(--forebrain-brand-1)] border-2 text-[var(--forebrain-text)]' : 'border-[var(--forebrain-divider)] text-[var(--forebrain-text-2)]'"
              data-testid="memory-scope-project"
              @click="draft.memoryScope = 'project_only'"
            >{{ t('projects.memoryProjectOnly') }}</button>
            <button
              type="button"
              role="radio"
              :aria-checked="draft.memoryScope !== 'project_only'"
              class="rounded-lg border px-3 py-2 text-[13px]"
              :class="draft.memoryScope !== 'project_only' ? 'border-[var(--forebrain-brand-1)] border-2 text-[var(--forebrain-text)]' : 'border-[var(--forebrain-divider)] text-[var(--forebrain-text-2)]'"
              @click="draft.memoryScope = 'shared'"
            >{{ t('projects.memoryShared') }}</button>
          </div>
        </div>

        <label class="flex items-start justify-between gap-4">
          <span>
            <span class="block text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.resourceAccessLabel') }}</span>
            <span class="mt-0.5 block text-[12px] text-[var(--forebrain-muted-text)]">{{ t('projects.resourceAccessHint') }}</span>
          </span>
          <input v-model="draft.resourceAccess" type="checkbox" class="mt-1 size-4 accent-[var(--forebrain-brand-1)]" />
        </label>

        <label class="flex items-start justify-between gap-4">
          <span class="block text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.pinned') }}</span>
          <input v-model="draft.pinned" type="checkbox" class="mt-1 size-4 accent-[var(--forebrain-brand-1)]" />
        </label>
      </div>

      <p v-if="formError" class="mt-2 text-[12px] text-[var(--forebrain-danger)]">{{ formError }}</p>
      <p v-if="formNotice" class="mt-2 text-[12px] text-[var(--forebrain-text-2)]">{{ formNotice }}</p>
      <div class="mt-3">
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!changed || saving" @click="save">
          {{ saving ? t('common.loading') : t('common.save') }}
        </button>
      </div>
    </section>

    <!-- Danger zone -->
    <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4">
      <div class="text-[14px] font-medium text-[var(--forebrain-danger)]">{{ t('projects.dangerTitle') }}</div>
      <div class="mt-3 flex flex-wrap gap-2">
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="toggleArchive">
          {{ project?.archivedAt ? t('projects.restore') : t('projects.archive') }}
        </button>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs text-[var(--forebrain-danger)]" @click="removeProject">
          {{ t('common.delete') }}
        </button>
      </div>
      <p class="mt-2 text-[11px] text-[var(--forebrain-muted-text)]">{{ t('projects.deleteHint') }}</p>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { getErrorMessage, forebrainApi, type ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * Overview facts plus the project's own settings. The root and key are the
 * project's identity on disk and in the state store; they are shown, never
 * edited.
 */
const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()

const { t } = useI18n()
const router = useRouter()

const draft = reactive({ name: '', icon: '', description: '', instructions: '', memoryScope: 'shared', resourceAccess: false, pinned: false })
const saving = ref(false)
const formError = ref('')
const formNotice = ref('')

watch(() => props.project, (project) => {
  if (!project) return
  Object.assign(draft, {
    name: project.name,
    icon: project.icon,
    description: project.description,
    instructions: project.instructions,
    memoryScope: project.memoryScope || 'shared',
    resourceAccess: Boolean(project.resourceAccess),
    pinned: Boolean(project.pinned),
  })
}, { immediate: true })

const changed = computed(() => {
  const p = props.project
  if (!p) return false
  return draft.name !== p.name
    || draft.icon !== p.icon
    || draft.description !== p.description
    || draft.instructions !== p.instructions
    || draft.memoryScope !== (p.memoryScope || 'shared')
    || draft.resourceAccess !== Boolean(p.resourceAccess)
    || draft.pinned !== Boolean(p.pinned)
})

function formatTime(unix?: number): string {
  if (!unix) return '—'
  return new Date(unix * 1000).toLocaleString()
}

async function save() {
  if (saving.value) return
  saving.value = true
  formError.value = ''
  formNotice.value = ''
  try {
    await forebrainApi.projectUpdate(props.projectId, {
      name: draft.name.trim(),
      icon: draft.icon.trim(),
      description: draft.description,
      instructions: draft.instructions,
      memoryScope: draft.memoryScope,
      resourceAccess: draft.resourceAccess,
    })
    if (draft.pinned !== Boolean(props.project?.pinned)) {
      await forebrainApi.projectPin(props.projectId, draft.pinned)
    }
    formNotice.value = t('projects.saved')
  } catch (cause) {
    formError.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

async function toggleArchive() {
  formError.value = ''
  try {
    await forebrainApi.projectArchive(props.projectId, !props.project?.archivedAt)
  } catch (cause) {
    formError.value = getErrorMessage(cause)
  }
}

async function removeProject() {
  if (!window.confirm(t('projects.deleteConfirm'))) return
  formError.value = ''
  try {
    await forebrainApi.projectDelete(props.projectId)
    await router.push('/projects')
  } catch (cause) {
    formError.value = getErrorMessage(cause)
  }
}
</script>

<style scoped>
.scope-badge {
  display: inline-flex;
  align-items: center;
  border-radius: 9999px;
  padding: 2px 10px;
  font-size: 11px;
  font-weight: 500;
  background: var(--forebrain-brand-1);
  color: var(--forebrain-on-brand);
}
</style>
