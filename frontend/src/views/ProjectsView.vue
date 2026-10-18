<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto flex w-full max-w-5xl flex-col gap-4">
      <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <ListToolbar>
        <template #left>
          <input
            v-model="q"
            type="search"
            :placeholder="t('projects.searchPlaceholder')"
            class="forebrain-field h-[34px]"
            style="max-width: 240px"
            data-testid="projects-search"
          />
        </template>
        <template #actions>
          <button type="button" class="forebrain-btn forebrain-btn-primary gap-1.5 text-xs" data-testid="projects-new" @click="openCreate">
            <Plus class="size-3.5" aria-hidden="true" />{{ t('projects.newProject') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" :disabled="loading" @click="refresh">
            <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
          </button>
        </template>
      </ListToolbar>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('projects.empty') }}</div>
      <div v-else class="forebrain-table-wrap">
        <table class="forebrain-table forebrain-table--fixed">
          <colgroup>
            <col style="width: 210px" />
            <col />
            <col style="width: 200px" />
            <col style="width: 64px" />
            <col style="width: 150px" />
            <col style="width: 112px" />
          </colgroup>
          <thead>
            <tr>
              <SortHeader
                :label="t('projects.colName')"
                sort-key="name"
                :active="sort === 'name'"
                :order="order"
                first="asc"
                @sort="sortBy"
              />
              <th>{{ t('projects.colRoot') }}</th>
              <th>{{ t('projects.colDesc') }}</th>
              <th class="text-right">{{ t('projects.colSessions') }}</th>
              <SortHeader
                :label="t('projects.colUpdated')"
                sort-key="updated"
                :active="sort === 'updated'"
                :order="order"
                first="desc"
                @sort="sortBy"
              />
              <th class="text-right">{{ t('projects.colActions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="project in rows" :key="project.id" :data-project-row="project.name">
              <td>
                <div class="flex min-w-0 items-center gap-1.5">
                  <span v-if="project.icon" class="shrink-0 text-[15px]">{{ project.icon }}</span>
                  <button
                    type="button"
                    class="min-w-0 truncate text-[var(--forebrain-brand-1)] hover:underline"
                    :data-testid="`project-open-${project.name}`"
                    @click="openDetail(project.id)"
                  >
                    {{ project.name }}
                  </button>
                  <BadgeComponent v-if="project.pinned" class="shrink-0">{{ t('projects.pinned') }}</BadgeComponent>
                  <BadgeComponent v-if="project.trustLevel !== 'trusted'" variant="gray" class="shrink-0">{{ t('projects.untrusted') }}</BadgeComponent>
                  <BadgeComponent v-if="project.missing" variant="error" class="shrink-0">{{ t('projects.dirMissing') }}</BadgeComponent>
                  <BadgeComponent v-if="project.memoryScope === 'project_only'" variant="gray" class="shrink-0">{{ t('projects.memoryProjectOnlyBadge') }}</BadgeComponent>
                </div>
              </td>
              <td class="font-mono text-[var(--forebrain-text-2)]">
                <span class="forebrain-cell-1" v-tip:top.trunc="project.root">{{ project.root }}</span>
              </td>
              <td>
                <span class="forebrain-cell-1" v-tip:top.trunc="project.description">{{ project.description }}</span>
              </td>
              <td class="forebrain-num text-right">{{ project.sessionCount ?? 0 }}</td>
              <td class="text-[var(--forebrain-text-2)]">{{ formatDateTime(project.updatedAt) }}</td>
              <td class="text-right">
                <RowActions>
                  <RowAction :icon="FolderOpen" :label="t('projects.openSpace')" :name="project.name" @click="openDetail(project.id)" />
                  <RowAction
                    :icon="project.pinned ? PinOff : Pin"
                    :label="project.pinned ? t('projects.unpin') : t('projects.pin')"
                    :name="project.name"
                    @click="togglePin(project)"
                  />
                  <RowAction
                    :icon="Trash2"
                    :label="t('common.delete')"
                    :name="project.name"
                    tone="danger"
                    :data-testid="`project-delete-${project.name}`"
                    @click="askDelete(project)"
                  />
                </RowActions>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      <ListNote>{{ t('projects.description') }}</ListNote>
    </div>

    <ForebrainModal :open="createOpen" size="sm" :title="t('projects.createTitle')" @close="closeCreate">
      <div>
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.dirLabel') }}</span>
        <div class="flex gap-2">
          <input v-model="draft.root" class="forebrain-field h-9 flex-1 font-mono text-[12px]" :placeholder="t('projects.rootPlaceholder')" data-testid="project-root" />
          <button type="button" class="forebrain-btn forebrain-btn-ghost h-9 px-3 text-xs" @click="browse">{{ t('projects.browse') }}</button>
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

    <ProjectDeleteDialog
      v-if="deleteTarget"
      :project="deleteTarget"
      @cancel="closeDelete"
      @deleted="confirmDelete"
    />

    <Teleport to="body">
      <FolderDialog v-if="folderOpen" :projects="folderProjects" @select="pickFolder" @cancel="folderOpen = false" />
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
 *
 * The list is a server-paged table (plan 073): the search, the order and the
 * page are the server's — the pinned projects stay on top there, before the
 * sort key — and the pager under the table says how many there are.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { FolderOpen, Pin, PinOff, Plus, RotateCw, Trash2 } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import SortHeader from '@/components/common/SortHeader.vue'
import TablePager from '@/components/common/TablePager.vue'
import FolderDialog from '@/components/chat/FolderDialog.vue'
import ProjectDeleteDialog from '@/components/project/ProjectDeleteDialog.vue'
import { useServerList } from '@/composables/useServerList'
import { forebrainApi, getErrorMessage, type ProjectRecord } from '@/lib/api'
import { formatDateTime } from '@/lib/time'
import { useI18n } from '@/locales'

const { t } = useI18n()
const router = useRouter()

const { rows, total, page, pageSize, q, sort, order, loading, error: listError, reload } = useServerList<ProjectRecord>(
  forebrainApi.projectsPage,
  { pageSize: 10, sort: 'updated', order: 'desc' },
)

const writeError = ref('')
const error = computed(() => listError.value || writeError.value)

const submitting = ref(false)
const folderOpen = ref(false)
/** The dialog's sidebar lists every project, not the page the table shows. */
const folderProjects = ref<ProjectRecord[]>([])

const createOpen = ref(false)
const createError = ref('')
const taken = ref<{ id: string; name: string } | null>(null)
const draft = reactive({ name: '', root: '' })

const deleteTarget = ref<ProjectRecord | null>(null)
const deleting = ref(false)

/** A header click: the composable turns the new order into page 1 and reloads. */
function sortBy(key: string, direction: 'asc' | 'desc'): void {
  sort.value = key
  order.value = direction
}

/** Load the page, and forget the last write's failure: the list is the truth. */
async function load(): Promise<void> {
  writeError.value = ''
  await reload()
}

function refresh(): void {
  void load()
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
async function browse() {
  folderOpen.value = true
  try {
    folderProjects.value = await forebrainApi.projectsList({})
  } catch {
    // The picker still works without the markers of what is already a project.
    folderProjects.value = rows.value
  }
}

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
  writeError.value = ''
  try {
    await forebrainApi.projectPin(project.id, !project.pinned)
    await load()
  } catch (e) {
    writeError.value = getErrorMessage(e)
  }
}

/** The row's delete action opens the shared dialog, which asks the preview itself. */
function askDelete(project: ProjectRecord) {
  deleteTarget.value = project
}

/** The dialog closed without deleting; a preview that could not be read says why. */
function closeDelete(error?: string) {
  if (error) writeError.value = error
  deleteTarget.value = null
}

async function confirmDelete() {
  const project = deleteTarget.value
  if (!project || deleting.value) return
  deleting.value = true
  writeError.value = ''
  try {
    await forebrainApi.projectDelete(project.id)
    deleteTarget.value = null
    await load()
  } catch (e) {
    writeError.value = isProjectBusy(e) ? t('projects.deleteBusy') : getErrorMessage(e)
  } finally {
    deleting.value = false
  }
}

/** A 409 from the delete is the project-busy refusal: a run is in flight. */
function isProjectBusy(cause: unknown): boolean {
  const response = (cause as { response?: { status?: number; data?: { error?: string } } }).response
  return response?.status === 409 && response.data?.error === 'project_busy'
}

onMounted(() => {
  void load()
})
</script>
