<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6">
    <div class="mx-auto w-full max-w-3xl">
      <header class="mb-5 flex items-end justify-between gap-4">
        <div>
          <div class="flex items-center gap-2">
            <h1 class="text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">{{ t('projects.title') }}</h1>
            <ScopeBadge type="agent" :label="t('projects.scopeAgent', { name: agentName })" />
          </div>
          <p class="mt-1 text-[13px] leading-relaxed text-[var(--forebrain-text-2)]">{{ t('projects.description') }}</p>
        </div>
        <div class="flex items-center gap-2">
          <select v-model="filter" class="forebrain-field h-9 w-28 text-[12px]" @change="load">
            <option value="all">{{ t('projects.filterAll') }}</option>
            <option value="pinned">{{ t('projects.filterPinned') }}</option>
            <option value="archived">{{ t('projects.filterArchived') }}</option>
          </select>
          <button type="button" class="forebrain-btn forebrain-btn-primary h-9 px-4 text-[12px]" @click="openCreate">
            {{ t('projects.newProject') }}
          </button>
        </div>
      </header>

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

      <CardComponent v-if="creating" class="mb-5">
        <template #header>
          <h2 class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.createTitle') }}</h2>
        </template>
        <div class="grid gap-2 sm:grid-cols-2">
          <input v-model="draft.name" :placeholder="t('projects.namePlaceholder')" class="forebrain-field" />
          <input v-model="draft.icon" :placeholder="t('projects.iconPlaceholder')" class="forebrain-field w-20 text-center" maxlength="4" />
        </div>
        <input v-model="draft.root" :placeholder="t('projects.rootPlaceholder')" class="forebrain-field w-full font-mono text-[12px]" />
        <p class="text-[11px] leading-relaxed text-[var(--forebrain-muted-text)]">{{ t('projects.rootHint') }}</p>
        <input v-model="draft.description" :placeholder="t('projects.descriptionPlaceholder')" class="forebrain-field w-full" />
        <textarea v-model="draft.instructions" rows="3" :placeholder="t('projects.instructionsPlaceholder')" class="forebrain-field w-full" />
        <p class="text-[11px] leading-relaxed text-[var(--forebrain-muted-text)]">{{ t('projects.instructionsHint') }}</p>
        <div class="flex flex-wrap items-center gap-4">
          <label class="flex cursor-pointer items-center gap-2 text-[12px] text-[var(--forebrain-text-2)]">
            <input v-model="draft.trust" type="checkbox" class="accent-[var(--forebrain-brand-1)]" />
            {{ t('projects.trustLabel') }}
          </label>
          <label class="flex cursor-pointer items-center gap-2 text-[12px] text-[var(--forebrain-text-2)]">
            <span>{{ t('projects.memoryScopeLabel') }}</span>
            <select v-model="draft.memoryScope" class="forebrain-field h-8 w-40 text-[12px]">
              <option value="shared">{{ t('projects.memoryShared') }}</option>
              <option value="project_only">{{ t('projects.memoryProjectOnly') }}</option>
            </select>
          </label>
          <label class="flex cursor-pointer items-center gap-2 text-[12px] text-[var(--forebrain-text-2)]">
            <input v-model="draft.resourceAccess" type="checkbox" class="accent-[var(--forebrain-brand-1)]" />
            {{ t('projects.resourceAccessLabel') }}
          </label>
        </div>
        <p class="text-[11px] leading-relaxed text-[var(--forebrain-muted-text)]">{{ t('projects.resourceAccessHint') }}</p>
        <div class="flex items-center gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-primary h-9 px-4 text-[12px]" :disabled="!draft.name.trim() || !draft.root.trim() || submitting" @click="create">
            {{ t('projects.create') }}
          </button>
          <button type="button" class="forebrain-btn forebrain-btn-ghost h-9 px-4 text-[12px]" @click="creating = false">
            {{ t('common.cancel') }}
          </button>
        </div>
      </CardComponent>

      <div v-if="loading && !projects.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <ul v-else-if="projects.length" class="grid gap-3 sm:grid-cols-2">
        <li
          v-for="project in projects"
          :key="project.id"
          class="rounded-lg border px-4 py-3"
          :class="project.archivedAt ? 'border-dashed border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] opacity-70' : 'border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)]'"
        >
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-center gap-2">
                <span v-if="project.icon" class="text-[15px]">{{ project.icon }}</span>
                <span class="cursor-pointer text-[13px] font-medium text-[var(--forebrain-text)] underline decoration-[var(--forebrain-divider)] underline-offset-4 hover:decoration-[var(--forebrain-brand-1)]" @click="openDetail(project.id)">{{ project.name }}</span>
                <span v-if="project.pinned" class="rounded-full border border-[var(--forebrain-brand-1)] px-2 py-0.5 text-[10px] text-[var(--forebrain-brand-1)]">{{ t('projects.pinned') }}</span>
                <span v-if="project.archivedAt" class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[10px] text-[var(--forebrain-muted-text)]">{{ t('projects.archived') }}</span>
                <span v-if="project.memoryScope === 'project_only'" class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[10px] text-[var(--forebrain-muted-text)]">{{ t('projects.memoryProjectOnly') }}</span>
              </div>
              <p class="mt-0.5 truncate font-mono text-[11px] text-[var(--forebrain-muted-text)]">{{ project.root }}</p>
              <p v-if="project.description" class="mt-1 line-clamp-2 text-[12px] leading-relaxed text-[var(--forebrain-text-2)]">{{ project.description }}</p>
              <p class="mt-1 text-[11px] text-[var(--forebrain-muted-text)]">{{ t('projects.updatedAt', { time: formatTime(project.updatedAt) }) }}</p>
            </div>
            <div class="flex shrink-0 flex-col items-end gap-1">
              <button type="button" class="forebrain-btn forebrain-btn-primary h-7 px-2 text-[11px]" @click="openDetail(project.id)">{{ t('projects.open') }}</button>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" @click="togglePin(project)">
                {{ project.pinned ? t('projects.unpin') : t('projects.pin') }}
              </button>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" @click="toggleArchive(project)">
                {{ project.archivedAt ? t('projects.restore') : t('projects.archive') }}
              </button>
              <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px] text-[var(--forebrain-danger)]" @click="remove(project)">{{ t('common.delete') }}</button>
            </div>
          </div>
        </li>
      </ul>
      <p v-else class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
        {{ t('projects.empty') }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * Projects bind a working directory to the settings that travel with it.
 * The list is the surface for managing them; each entry opens the detail
 * view where its sessions and MCP servers live.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import CardComponent from '@/components/common/CardComponent.vue'
import { getErrorMessage, forebrainApi, type ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'

const { t } = useI18n()
const router = useRouter()
const { active: activeAgent } = usePrimaryAgents()
const agentName = computed(() => activeAgent.value?.name || activeAgent.value?.id || '')
const projects = ref<ProjectRecord[]>([])
const loading = ref(false)
const creating = ref(false)
const submitting = ref(false)
const error = ref('')
const search = ref('')
const sort = ref('updated')
const filter = ref<'all' | 'pinned' | 'archived'>('all')
const draft = reactive({ name: '', icon: '', root: '', description: '', instructions: '', trust: false, memoryScope: 'shared', resourceAccess: true })

async function load() {
  loading.value = true
  error.value = ''
  try {
    projects.value = await forebrainApi.projectsList({
      search: search.value.trim() || undefined,
      sort: sort.value,
      archived: filter.value === 'archived' || undefined,
    })
    if (filter.value === 'pinned') {
      projects.value = projects.value.filter((project) => project.pinned)
    }
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  creating.value = true
}

async function create() {
  submitting.value = true
  error.value = ''
  try {
    const created = await forebrainApi.projectCreate({
      name: draft.name.trim(),
      root: draft.root.trim(),
      icon: draft.icon.trim(),
      description: draft.description.trim(),
      instructions: draft.instructions.trim(),
      trust: draft.trust,
      memoryScope: draft.memoryScope,
      resourceAccess: draft.resourceAccess,
    })
    creating.value = false
    draft.name = ''
    draft.icon = ''
    draft.root = ''
    draft.description = ''
    draft.instructions = ''
    draft.trust = false
    draft.memoryScope = 'shared'
    draft.resourceAccess = true
    await load()
    openDetail(created.id)
  } catch (e) {
    error.value = getErrorMessage(e)
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

async function toggleArchive(project: ProjectRecord) {
  error.value = ''
  try {
    await forebrainApi.projectArchive(project.id, !project.archivedAt)
    await load()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

async function remove(project: ProjectRecord) {
  if (!window.confirm(t('projects.deleteConfirm', { name: project.name }))) return
  error.value = ''
  try {
    await forebrainApi.projectDelete(project.id)
    await load()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

function formatTime(unixSeconds: number): string {
  if (!unixSeconds) return ''
  return new Date(unixSeconds * 1000).toLocaleString()
}

onMounted(load)
</script>
