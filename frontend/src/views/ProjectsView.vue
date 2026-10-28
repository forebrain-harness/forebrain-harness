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
        <template #cell-type="{ row }">
          <BadgeComponent :variant="projectTypeOption(row.projectType).badge" class="gap-1" :data-testid="`project-type-badge-${row.id}`">
            <component :is="projectTypeOption(row.projectType).icon" class="size-3" aria-hidden="true" />
            {{ t(projectTypeOption(row.projectType).label) }}
          </BadgeComponent>
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

      <!-- The three kinds (plan 153): an icon, its name and one line saying
           what it is for. The gateway's suggestion is marked, never imposed. -->
      <div class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.typeField') }}</span>
        <div class="grid grid-cols-3 gap-2" role="radiogroup" :aria-label="t('projects.typeField')">
          <button
            v-for="option in PROJECT_TYPES"
            :key="option.value"
            type="button"
            class="relative grid justify-items-start gap-[3px] rounded-lg border px-2.5 py-[10px] text-left"
            :class="draft.projectType === option.value
              ? 'border-[var(--forebrain-brand-1)] bg-[var(--forebrain-brand-soft)]'
              : 'border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] hover:border-[var(--forebrain-brand-1)]'"
            role="radio"
            :aria-checked="draft.projectType === option.value"
            :data-testid="`project-type-${option.value}`"
            @click="pickType(option.value)"
          >
            <span
              v-if="defaultType?.projectType === option.value"
              class="absolute right-2 top-2 rounded border border-[var(--forebrain-brand-1)] px-1 text-[10px] font-semibold leading-[1.5] text-[var(--forebrain-brand-1)]"
            >{{ t('projects.typeSuggest') }}</span>
            <component
              :is="option.icon"
              class="size-4"
              :class="draft.projectType === option.value ? 'text-[var(--forebrain-brand-1)]' : 'text-[var(--forebrain-text-2)]'"
              aria-hidden="true"
            />
            <b class="text-[13px] font-semibold text-[var(--forebrain-text)]">{{ t(option.label) }}</b>
            <span class="text-[11.5px] leading-[1.45] text-[var(--forebrain-muted-text)]">{{ t(option.hint) }}</span>
          </button>
        </div>
        <!-- Why the suggestion is what it is, and — once the user picked a
             card — that the suggestion no longer applies. -->
        <div
          v-if="typeHint"
          class="mt-2 flex items-start gap-2 rounded-lg border px-2.5 py-2 text-[12px] leading-[1.55]"
          :class="typeHint.overridden
            ? 'border-[var(--forebrain-brand-1)] bg-[var(--forebrain-brand-soft)] text-[var(--forebrain-text)]'
            : 'border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] text-[var(--forebrain-text-2)]'"
          data-testid="project-type-hint"
        >
          <component :is="typeHint.icon" class="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          <span>
            {{ typeHint.title }}
            <span v-if="typeHint.sub" class="mt-0.5 block text-[11px] text-[var(--forebrain-muted-text)]">{{ typeHint.sub }}</span>
          </span>
        </div>
      </div>

      <!-- A batch project runs unattended on this directory, so its trust is
           part of creating it: without it the gateway refuses the create. -->
      <div v-if="draft.projectType === 'batch'">
        <SwitchRow v-model="draft.batchTrust" :title="t('projects.batchTrustLabel')" :hint="t('projects.batchTrustHint')" />
        <span class="mt-1 block text-[11px] text-[var(--forebrain-muted-text)]">{{ t('projects.batchTrustRequired') }}</span>
      </div>

      <p v-if="createError" class="text-[12px] text-[var(--forebrain-danger)]">{{ createError }}</p>
      <template #footer>
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="closeCreate">{{ t('common.cancel') }}</Button>
        <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" :disabled="!canCreate" data-testid="project-create" @click="create">
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
 * its sessions and configuration live. Creating is a modal (a directory, a
 * name and a type), and deleting is a modal that first says what it would take
 * with it and asks for the project's name.
 *
 * The list is a server-paged table (plan 073): the search, the order and the
 * page are the server's — the pinned projects stay on top there, before the
 * sort key — and the pager under the table says how many there are.
 *
 * The type column and the dialog's three cards are plan 153. A folder's
 * suggested type is the gateway's read of it (145) and never a browser-side
 * `.git` check; the dialog marks the suggestion, explains it, and gives way the
 * moment a person picks a card.
 */
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Check, CircleAlert, Folder, FolderGit2, FolderOpen, Pin, PinOff, Plus, RotateCw, Trash2 } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import SwitchRow from '@/components/common/SwitchRow.vue'
import TablePager from '@/components/common/TablePager.vue'
import FolderDialog from '@/components/chat/FolderDialog.vue'
import ProjectDeleteDialog from '@/components/project/ProjectDeleteDialog.vue'
import { useServerList } from '@/composables/useServerList'
import { forebrainApi, getErrorMessage, type ProjectRecord, type ProjectType, type ProjectTypeDefaultSource } from '@/lib/api'
import { PROJECT_TYPES, projectTypeDefaultReason, projectTypeOption } from '@/lib/projectTypes'
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
  { id: 'type', header: t('projects.colType'), size: 118 },
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
const draft = reactive({ name: '', root: '', projectType: 'coding' as ProjectType, batchTrust: false })

/**
 * The folder's suggested type, as the gateway reads it — the web never repeats
 * the `.git` check (plan 153). `projectTypeTouched` is the user's own pick: once
 * it is set, neither a new folder nor a late answer may move the choice again.
 */
const defaultType = ref<{ projectType: ProjectType; source: ProjectTypeDefaultSource } | null>(null)
const defaultTypeFailed = ref(false)
const projectTypeTouched = ref(false)
/** The newest suggestion request; an answer that is not the newest is dropped. */
let defaultTypeSeq = 0
let defaultTypeTimer: ReturnType<typeof setTimeout> | null = null

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
  openDialogOn('coding')
}

function closeCreate() {
  createOpen.value = false
  taken.value = null
  createError.value = ''
  clearTypeTimer()
}

/** A fresh dialog starts from 创意编程 and from the folder's suggestion again:
 *  a pick made in the previous dialog is not the next one's. */
function openDialogOn(type: ProjectType) {
  draft.projectType = type
  draft.batchTrust = false
  projectTypeTouched.value = false
  defaultType.value = null
  defaultTypeFailed.value = false
  // Anything already in flight belongs to the dialog that just closed.
  defaultTypeSeq += 1
  clearTypeTimer()
  createOpen.value = true
}

function clearTypeTimer() {
  if (defaultTypeTimer === null) return
  clearTimeout(defaultTypeTimer)
  defaultTypeTimer = null
}

/**
 * Every root the dialog is given asks the gateway what type it would get, so a
 * folder picked after a typed one is suggested on its own merits. The answer
 * only lands while it is still the newest one and nobody has picked a card: a
 * folder change mid-flight or a manual pick both make it history. A failed read
 * keeps the current choice and says so rather than guessing in the browser.
 */
watch(() => draft.root, (root) => {
  clearTypeTimer()
  defaultTypeFailed.value = false
  defaultType.value = null
  const trimmed = root.trim()
  if (!trimmed || !createOpen.value) return
  defaultTypeTimer = setTimeout(() => {
    defaultTypeTimer = null
    void loadDefaultType(trimmed)
  }, 300)
})

async function loadDefaultType(root: string) {
  // Issuing bumps the counter, so an answer that is no longer the newest — a
  // folder changed mid-flight, or a dialog reopened — is dropped on arrival.
  const seq = ++defaultTypeSeq
  try {
    const suggestion = await forebrainApi.getProjectDefaultType(root)
    if (seq !== defaultTypeSeq || draft.root.trim() !== root) return
    defaultType.value = suggestion
    if (!projectTypeTouched.value) draft.projectType = suggestion.projectType
  } catch {
    if (seq !== defaultTypeSeq || draft.root.trim() !== root) return
    defaultType.value = null
    defaultTypeFailed.value = true
  }
}

function pickType(type: ProjectType) {
  draft.projectType = type
  projectTypeTouched.value = true
  if (type !== 'batch') draft.batchTrust = false
}

/** The note under the cards: the suggestion and its reason, the override once
 *  the user chose against it, or a failed read. */
const typeHint = computed(() => {
  if (defaultTypeFailed.value) {
    return { icon: CircleAlert, overridden: false, title: t('projects.typeDetectFailed'), sub: '' }
  }
  const suggestion = defaultType.value
  if (!suggestion) return null
  const suggested = projectTypeOption(suggestion.projectType)
  const source = t(suggestion.source === 'git_worktree' ? 'projects.typeSourceGit' : 'projects.typeSourceNonGit')
  if (projectTypeTouched.value && draft.projectType !== suggestion.projectType) {
    return {
      icon: Check,
      overridden: true,
      title: t('projects.typeOverrideNote', { name: t(projectTypeOption(draft.projectType).label) }),
      sub: t('projects.typeOverrideSub', { name: t(suggested.label), source }),
    }
  }
  return {
    icon: suggestion.source === 'git_worktree' ? FolderGit2 : Folder,
    overridden: false,
    title: t(projectTypeDefaultReason(suggestion.source)),
    sub: t('projects.typeDefaultNote'),
  }
})

/** A batch project cannot be created without its trust decision, so the
 *  dialog's own create button waits for the tick as the gateway would. */
const canCreate = computed(() =>
  Boolean(draft.name.trim()) && Boolean(draft.root.trim()) && !submitting.value && !taken.value
  && (draft.projectType !== 'batch' || draft.batchTrust))

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
    const created = await forebrainApi.projectCreate({
      name: draft.name.trim(),
      root: draft.root.trim(),
      // The dialog always says which type it means: the suggestion it shows is
      // only a suggestion, and the folder's own default is the gateway's to
      // apply when nothing is asked for.
      projectType: draft.projectType,
      ...(draft.projectType === 'batch' ? { trust: draft.batchTrust } : {}),
    })
    closeCreate()
    await load()
    openDetail(created.id)
  } catch (e) {
    const response = (e as { response?: { status?: number; data?: { error?: string; project?: { id?: string; name?: string } } } }).response
    const code = response?.data?.error
    if (response?.status === 409 && code === 'project_root_taken' && response.data?.project) {
      taken.value = { id: String(response.data.project.id ?? ''), name: String(response.data.project.name ?? '') }
    } else if (code === 'batch_requires_trust') {
      createError.value = t('projects.errBatchRequiresTrust')
    } else if (code === 'project_type_invalid') {
      createError.value = t('projects.errTypeInvalid')
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

onBeforeUnmount(() => {
  clearTypeTimer()
})
</script>
