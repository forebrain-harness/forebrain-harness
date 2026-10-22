<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto flex w-full max-w-5xl flex-col gap-4">
      <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <ListToolbar>
        <template #left>
          <Input
            v-model="q"
            type="search"
            :placeholder="t('projects.searchPlaceholder')"
            class="forebrain-field h-[34px] w-auto"
            style="max-width: 240px"
            data-testid="projects-search"
          />
        </template>
        <template #actions>
          <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto gap-1.5 text-xs" data-testid="projects-new" @click="openCreate">
            <Plus class="size-3.5" aria-hidden="true" />{{ t('projects.newProject') }}
          </Button>
          <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs" :disabled="loading" @click="refresh">
            <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
          </Button>
        </template>
      </ListToolbar>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('projects.empty') }}</div>
      <DataTable
        v-else
        :columns="columns"
        :rows="rows"
        :row-key="(row) => row.id"
        :row-attrs="(row) => ({ 'data-project-row': row.name })"
        :sorting="{ key: sort, order }"
        @update:sorting="sortBy($event.key, $event.order)"
      >
        <template #cell-name="{ row }">
          <div class="flex min-w-0 items-center gap-1.5">
            <span v-if="row.icon" class="shrink-0 text-[15px]">{{ row.icon }}</span>
            <button
              type="button"
              class="min-w-0 truncate text-[var(--forebrain-brand-1)] hover:underline"
              :data-testid="`project-open-${row.name}`"
              @click="openDetail(row.id)"
            >
              {{ row.name }}
            </button>
            <BadgeComponent v-if="row.pinned" class="shrink-0">{{ t('projects.pinned') }}</BadgeComponent>
            <BadgeComponent v-if="row.trustLevel !== 'trusted'" variant="gray" class="shrink-0">{{ t('projects.untrusted') }}</BadgeComponent>
            <BadgeComponent v-if="row.missing" variant="error" class="shrink-0">{{ t('projects.dirMissing') }}</BadgeComponent>
            <BadgeComponent v-if="row.memoryScope === 'project_only'" variant="gray" class="shrink-0">{{ t('projects.memoryProjectOnlyBadge') }}</BadgeComponent>
          </div>
        </template>
        <template #cell-root="{ row }">
          <span class="forebrain-cell-1" v-tip:top.trunc="row.root">{{ row.root }}</span>
        </template>
        <template #cell-description="{ row }">
          <span class="forebrain-cell-1" v-tip:top.trunc="row.description">{{ row.description }}</span>
        </template>
        <template #cell-sessions="{ row }">{{ row.sessionCount ?? 0 }}</template>
        <template #cell-updated="{ row }">{{ formatDateTime(row.updatedAt) }}</template>
        <template #cell-actions="{ row }">
          <RowActions>
            <RowAction :icon="FolderOpen" :label="t('projects.openSpace')" :name="row.name" @click="openDetail(row.id)" />
            <RowAction
              :icon="row.pinned ? PinOff : Pin"
              :label="row.pinned ? t('projects.unpin') : t('projects.pin')"
              :name="row.name"
              @click="togglePin(row)"
            />
            <RowAction
              :icon="Trash2"
              :label="t('common.delete')"
              :name="row.name"
              tone="danger"
              :data-testid="`project-delete-${row.name}`"
              @click="askDelete(row)"
            />
          </RowActions>
        </template>
      </DataTable>

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      <ListNote>{{ t('projects.description') }}</ListNote>
    </div>

    <ForebrainModal :open="createOpen" size="sm" :title="t('projects.createTitle')" @close="closeCreate">
      <div>
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.dirLabel') }}</span>
        <div class="flex gap-2">
          <Input v-model="draft.root" class="forebrain-field h-9 flex-1 font-mono text-[12px]" :placeholder="t('projects.rootPlaceholder')" data-testid="project-root" />
          <Button type="button" class="forebrain-btn forebrain-btn-ghost h-9 px-3 text-xs" @click="browse">{{ t('projects.browse') }}</Button>
        </div>
      </div>
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.namePlaceholder') }}</span>
        <Input v-model="draft.name" class="forebrain-field h-9 w-full" :placeholder="t('projects.namePlaceholder')" data-testid="project-name" />
      </label>
      <div v-if="taken" class="rounded-lg border border-[var(--forebrain-warning)] bg-[var(--forebrain-bg-alt)] px-3 py-2 text-[12px] text-[var(--forebrain-text-2)]">
        {{ t('projects.taken', { name: taken.name }) }}
        <button type="button" class="text-[var(--forebrain-brand-1)] underline" @click="openDetail(taken.id)">{{ t('projects.openIt') }}</button>
      </div>
      <p v-else class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('projects.rootHint') }}</p>
      <p v-if="createError" class="text-[12px] text-[var(--forebrain-danger)]">{{ createError }}</p>
      <template #footer>
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="closeCreate">{{ t('common.cancel') }}</Button>
        <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" :disabled="!draft.name.trim() || !draft.root.trim() || submitting || !!taken" data-testid="project-create" @click="create">
          {{ submitting ? t('common.loading') : t('projects.create') }}
        </Button>
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
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
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

/**
 * The table's columns, at the design's widths: the root and the description
 * share the room the others leave. Name and 更新时间 sort — the projects listing
 * is one of the three endpoints that read `sort`.
 */
const columns: DataColumn[] = [
  { id: 'name', header: t('projects.colName'), size: 210, enableSorting: true },
  { id: 'root', header: t('projects.colRoot'), cellClass: 'font-mono text-[var(--forebrain-text-2)]' },
  { id: 'description', header: t('projects.colDesc'), size: 200 },
  { id: 'sessions', header: t('projects.colSessions'), size: 64, headerClass: 'text-right', cellClass: 'forebrain-num text-right' },
  {
    id: 'updated',
    header: t('projects.colUpdated'),
    size: 150,
    enableSorting: true,
    sortDescFirst: true,
    cellClass: 'text-[var(--forebrain-text-2)]',
  },
  { id: 'actions', header: t('projects.colActions'), size: 112, headerClass: 'text-center', cellClass: 'text-right' },
]

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
