<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto w-full max-w-5xl">
      <PageTopRow :title="t('projects.title')" :description="t('projects.description')">
        <template #actions>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" data-testid="projects-new" @click="openCreate">
            {{ t('projects.newProject') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
            {{ t('common.refresh') }}
          </button>
        </template>
      </PageTopRow>

      <p v-if="error" class="mb-4 rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <div class="mb-4 flex flex-wrap items-center gap-2">
        <input
          v-model="search"
          :placeholder="t('projects.searchPlaceholder')"
          class="forebrain-field h-9 flex-1 min-w-40 text-[12px]"
          @keydown.enter.prevent="load"
        />
        <select v-model="sort" class="forebrain-field h-9 w-36 text-[12px]" @change="load">
          <option value="updated">{{ t('projects.sortUpdated') }}</option>
          <option value="name">{{ t('projects.sortName') }}</option>
        </select>
      </div>

      <div v-if="loading && !projects.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!projects.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('projects.empty') }}</div>
      <div v-else class="forebrain-list-boxed">
        <div v-for="project in projects" :key="project.id" class="flex flex-wrap items-start justify-between gap-3 py-3" :data-project-row="project.name">
          <div class="min-w-0 flex-1">
            <div class="flex flex-wrap items-center gap-2">
              <span v-if="project.icon" class="text-[15px]">{{ project.icon }}</span>
              <span class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ project.name }}</span>
              <span v-if="project.pinned" class="rounded-full border border-[var(--forebrain-brand-1)] px-2 py-0.5 text-[10px] text-[var(--forebrain-brand-1)]">{{ t('projects.pinned') }}</span>
              <span v-if="project.memoryScope === 'project_only'" class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[10px] text-[var(--forebrain-muted-text)]">{{ t('projects.memoryProjectOnly') }}</span>
            </div>
            <p v-if="project.description" class="mt-1 line-clamp-2 text-[12px] leading-relaxed text-[var(--forebrain-text-2)]">{{ project.description }}</p>
            <p class="mt-1 text-[11px] text-[var(--forebrain-muted-text)]"><span class="font-mono">{{ project.root }}</span> · {{ t('projects.updatedAt', { time: formatTime(project.updatedAt) }) }}</p>
          </div>
          <div class="flex shrink-0 flex-wrap items-center gap-1">
            <button type="button" class="forebrain-btn forebrain-btn-primary h-7 px-2 text-[11px]" @click="openDetail(project.id)">{{ t('projects.open') }}</button>
            <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" @click="togglePin(project)">
              {{ project.pinned ? t('projects.unpin') : t('projects.pin') }}
            </button>
            <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px] text-[var(--forebrain-danger)]" :data-testid="`project-delete-${project.name}`" @click="askDelete(project)">{{ t('common.delete') }}</button>
          </div>
        </div>
      </div>
    </div>

    <ForebrainModal :open="createOpen" size="sm" :title="t('projects.createTitle')" @close="closeCreate">
      <div>
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.dirLabel') }}</span>
        <div class="flex gap-2">
          <input v-model="draft.root" class="forebrain-field h-9 flex-1 font-mono text-[12px]" :placeholder="t('projects.rootPlaceholder')" data-testid="project-root" />
          <button type="button" class="forebrain-btn forebrain-btn-ghost h-9 px-3 text-xs" @click="folderOpen = true">{{ t('projects.browse') }}</button>
        </div>
      </div>
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.namePlaceholder') }}</span>
        <input v-model="draft.name" class="forebrain-field h-9 w-full" :placeholder="t('projects.namePlaceholder')" data-testid="project-name" />
      </label>
      <div v-if="taken" class="rounded-lg border border-[var(--forebrain-warning)] bg-[var(--forebrain-bg-alt)] px-3 py-2 text-[12px] text-[var(--forebrain-text-2)]">
        {{ t('projects.taken', { name: taken.name }) }}
        <button type="button" class="text-[var(--forebrain-brand-1)] underline" @click="openDetail(taken.id)">{{ t('projects.openIt') }}</button>
      </div>
      <p v-else class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('projects.rootHint') }}</p>
      <p v-if="createError" class="text-[12px] text-[var(--forebrain-danger)]">{{ createError }}</p>
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="closeCreate">{{ t('common.cancel') }}</button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!draft.name.trim() || !draft.root.trim() || submitting || !!taken" data-testid="project-create" @click="create">
          {{ submitting ? t('common.loading') : t('projects.create') }}
        </button>
      </template>
    </ForebrainModal>

    <ConfirmDialog
      v-if="deleteTarget && deletePreview && !deletePreview.running"
      :open="true"
      :title="t('projects.deleteModalTitle', { name: deleteTarget.name })"
      :message="deleteMessage"
      :confirm-label="t('projects.deleteConfirmButton')"
      danger
      :typed-confirm="deleteTarget.name"
      @cancel="deleteTarget = null"
      @confirm="confirmDelete"
    />
    <ForebrainModal v-else-if="deleteTarget" :open="true" size="sm" :title="t('projects.deleteModalTitle', { name: deleteTarget.name })" @close="deleteTarget = null">
      <p class="text-[13px] text-[var(--forebrain-text-2)]">{{ t('projects.deleteBusy') }}</p>
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="deleteTarget = null">{{ t('common.close') }}</button>
      </template>
    </ForebrainModal>

    <Teleport to="body">
      <FolderDialog v-if="folderOpen" :projects="projects" @select="pickFolder" @cancel="folderOpen = false" />
    </Teleport>
  </div>
</template>

<script setup lang="ts">
/**
 * Projects bind a working directory to the settings that travel with it. The
 * list is the surface for managing them; each entry opens the detail view where
 * its sessions and configuration live. Creating is a modal (a directory and a
 * name), and deleting is a modal that first says what it would take with it and
 * asks for the project's name.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import PageTopRow from '@/components/common/PageTopRow.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import FolderDialog from '@/components/chat/FolderDialog.vue'
import { getErrorMessage, forebrainApi, type ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()
const router = useRouter()
const projects = ref<ProjectRecord[]>([])
const loading = ref(false)
const submitting = ref(false)
const folderOpen = ref(false)
const error = ref('')
const search = ref('')
const sort = ref('updated')

const createOpen = ref(false)
const createError = ref('')
const taken = ref<{ id: string; name: string } | null>(null)
const draft = reactive({ name: '', root: '' })

const deleteTarget = ref<ProjectRecord | null>(null)
const deletePreview = ref<{ sessions: number; cronJobs: number; running: boolean } | null>(null)
const deleting = ref(false)

const deleteMessage = computed(() => {
  const p = deletePreview.value
  const sessions = p?.sessions ?? 0
  const jobs = p?.cronJobs ?? 0
  const head = jobs
    ? t('projects.deletePreviewWithJobs', { sessions, jobs })
    : t('projects.deletePreview', { sessions })
  return `${head}\n${t('projects.deleteModalDir')}：${deleteTarget.value?.root ?? ''}\n${t('projects.deleteModalHint')}`
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    projects.value = await forebrainApi.projectsList({
      search: search.value.trim() || undefined,
      sort: sort.value,
    })
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  createError.value = ''
  taken.value = null
  draft.name = ''
  draft.root = ''
  createOpen.value = true
}

function closeCreate() {
  createOpen.value = false
  taken.value = null
  createError.value = ''
}

/** The new-project form's folder picker: the picked directory is the root. */
function pickFolder(path: string) {
  draft.root = path
  if (!draft.name.trim()) draft.name = path.replace(/\/+$/, '').split('/').filter(Boolean).pop() ?? ''
  folderOpen.value = false
}

async function create() {
  submitting.value = true
  createError.value = ''
  taken.value = null
  try {
    const created = await forebrainApi.projectCreate({ name: draft.name.trim(), root: draft.root.trim() })
    closeCreate()
    await load()
    openDetail(created.id)
  } catch (e) {
    const response = (e as { response?: { status?: number; data?: { error?: string; project?: { id?: string; name?: string } } } }).response
    if (response?.status === 409 && response.data?.error === 'project_root_taken' && response.data.project) {
      taken.value = { id: String(response.data.project.id ?? ''), name: String(response.data.project.name ?? '') }
    } else {
      createError.value = getErrorMessage(e)
    }
  } finally {
    submitting.value = false
  }
}

function openDetail(id: string) {
  void router.push(`/projects/${encodeURIComponent(id)}`)
}

async function togglePin(project: ProjectRecord) {
  error.value = ''
  try {
    await forebrainApi.projectPin(project.id, !project.pinned)
    await load()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

async function askDelete(project: ProjectRecord) {
  deleteTarget.value = project
  deletePreview.value = null
  try {
    const preview = await forebrainApi.projectDeletePreview(project.id)
    deletePreview.value = { sessions: preview.sessions, cronJobs: preview.cron_jobs, running: preview.running }
  } catch (e) {
    error.value = getErrorMessage(e)
    deleteTarget.value = null
  }
}

async function confirmDelete() {
  const project = deleteTarget.value
  if (!project || deleting.value) return
  deleting.value = true
  error.value = ''
  try {
    await forebrainApi.projectDelete(project.id)
    deleteTarget.value = null
    deletePreview.value = null
    await load()
  } catch (e) {
    error.value = isProjectBusy(e) ? t('projects.deleteBusy') : getErrorMessage(e)
  } finally {
    deleting.value = false
  }
}

/** A 409 from the delete is the project-busy refusal: a run is in flight. */
function isProjectBusy(cause: unknown): boolean {
  const response = (cause as { response?: { status?: number; data?: { error?: string } } }).response
  return response?.status === 409 && response.data?.error === 'project_busy'
}

function formatTime(unixSeconds: number): string {
  if (!unixSeconds) return ''
  return new Date(unixSeconds * 1000).toLocaleString()
}

onMounted(load)
</script>
