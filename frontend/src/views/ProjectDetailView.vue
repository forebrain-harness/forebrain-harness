<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6">
    <div class="mx-auto w-full max-w-3xl">
      <header class="mb-5 flex items-end justify-between gap-4">
        <div class="min-w-0">
          <p class="text-[11px] uppercase tracking-wide text-[var(--forebrain-muted-text)]">
            <span class="cursor-pointer hover:text-[var(--forebrain-text)]" @click="back">{{ t('projects.title') }}</span>
            <span> / </span>
            <span class="text-[var(--forebrain-text)]">{{ project?.name }}</span>
          </p>
          <h1 class="mt-1 flex items-center gap-2 font-serif text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">
            <span v-if="project?.icon">{{ project.icon }}</span>
            <span>{{ project?.name ?? '' }}</span>
          </h1>
          <p v-if="project" class="mt-1 truncate font-mono text-[12px] text-[var(--forebrain-muted-text)]">{{ project.root }}</p>
        </div>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
          {{ t('common.refresh') }}
        </button>
      </header>

      <p v-if="error" class="mb-4 rounded-xl border border-[rgba(160,70,70,0.36)] bg-[rgba(160,70,70,0.08)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <div v-if="loading && !project" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>

      <template v-if="project">
        <section class="mb-5 rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4">
          <h2 class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.settingsTitle') }}</h2>
          <div class="mt-3 grid gap-2 sm:grid-cols-2">
            <input v-model="edit.name" :placeholder="t('projects.namePlaceholder')" class="forebrain-field" />
            <input v-model="edit.icon" :placeholder="t('projects.iconPlaceholder')" class="forebrain-field w-20 text-center" maxlength="4" />
          </div>
          <input v-model="edit.description" :placeholder="t('projects.descriptionPlaceholder')" class="forebrain-field mt-2 w-full" />
          <textarea v-model="edit.instructions" rows="4" :placeholder="t('projects.instructionsPlaceholder')" class="forebrain-field mt-2 w-full" />
          <p class="mt-1 text-[11px] leading-relaxed text-[var(--forebrain-muted-text)]">{{ t('projects.instructionsFrozenHint') }}</p>
          <div class="mt-3 flex flex-wrap items-center gap-4">
            <label class="flex cursor-pointer items-center gap-2 text-[12px] text-[var(--forebrain-text-2)]">
              <span>{{ t('projects.memoryScopeLabel') }}</span>
              <select v-model="edit.memoryScope" class="forebrain-field h-8 w-40 text-[12px]">
                <option value="shared">{{ t('projects.memoryShared') }}</option>
                <option value="project_only">{{ t('projects.memoryProjectOnly') }}</option>
              </select>
            </label>
            <label class="flex cursor-pointer items-center gap-2 text-[12px] text-[var(--forebrain-text-2)]">
              <input v-model="edit.resourceAccess" type="checkbox" class="accent-[var(--forebrain-brand-1)]" />
              {{ t('projects.resourceAccessLabel') }}
            </label>
            <label class="flex cursor-pointer items-center gap-2 text-[12px] text-[var(--forebrain-text-2)]">
              <input v-model="edit.trust" type="checkbox" class="accent-[var(--forebrain-brand-1)]" />
              {{ t('projects.trustLabel') }}
            </label>
          </div>
          <div class="mt-3 flex items-center gap-2">
            <button type="button" class="forebrain-btn forebrain-btn-primary h-9 px-4 text-[12px]" :disabled="saving || !edit.name.trim()" @click="save">
              {{ t('common.save') }}
            </button>
          </div>
        </section>

        <section class="mb-5 rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4">
          <div class="flex items-center justify-between">
            <h2 class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.sessionsTitle') }}</h2>
            <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" @click="newSession">
              {{ t('projects.newSession') }}
            </button>
          </div>
          <p v-if="!sessions.length" class="mt-3 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('projects.noSessions') }}</p>
          <ul v-else class="mt-2 space-y-1">
            <li v-for="row in sessions" :key="row.id" class="rounded-lg px-2 py-1.5 text-[12px] odd:bg-[var(--forebrain-surface-soft)]">
              <span class="cursor-pointer text-[var(--forebrain-text)] underline decoration-[var(--forebrain-divider)] underline-offset-4 hover:decoration-[var(--forebrain-brand-1)]" @click="openSession(row.id)">{{ row.title || t('sidebar.untitledChat') }}</span>
              <span class="ml-2 text-[var(--forebrain-muted-text)]">{{ formatTime(row.updatedAt) }}</span>
            </li>
          </ul>
        </section>

        <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4">
          <h2 class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.mcpTitle') }}</h2>
          <p class="mt-1 text-[12px] leading-relaxed text-[var(--forebrain-text-2)]">{{ t('projects.mcpDescription') }}</p>

          <div v-if="mcpPending.length" class="mt-3 rounded-xl border border-[rgba(180,140,60,0.4)] bg-[rgba(180,140,60,0.07)] p-3">
            <p class="text-[12px] font-medium text-[var(--forebrain-text)]">{{ t('projects.mcpPendingTitle') }}</p>
            <ul class="mt-2 space-y-1">
              <li v-for="row in mcpPending" :key="row.name" class="flex flex-wrap items-center justify-between gap-2 rounded-lg px-2 py-1.5 text-[12px] odd:bg-[var(--forebrain-surface-soft)]">
                <div class="min-w-0">
                  <span class="font-mono text-[var(--forebrain-text)]">{{ row.name }}</span>
                  <span class="ml-2 text-[var(--forebrain-muted-text)]">{{ row.summary }}</span>
                </div>
                <div class="flex shrink-0 gap-1">
                  <button type="button" class="forebrain-btn forebrain-btn-primary h-7 px-2 text-[11px]" :disabled="consenting" @click="consent(row.name, true)">{{ t('projects.mcpAllow') }}</button>
                  <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" :disabled="consenting" @click="consent(row.name, false)">{{ t('projects.mcpDecline') }}</button>
                </div>
              </li>
            </ul>
          </div>

          <ul v-if="mcpServers.length" class="mt-3 space-y-1">
            <li v-for="row in mcpServers" :key="row.name" class="rounded-lg px-2 py-1.5 text-[12px] odd:bg-[var(--forebrain-surface-soft)]">
              <div class="flex flex-wrap items-center gap-2">
                <span class="font-mono text-[var(--forebrain-text)]">{{ row.name }}</span>
                <span v-if="row.transport" class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[11px] text-[var(--forebrain-muted-text)]">{{ row.transport }}</span>
                <span class="rounded-full border px-2 py-0.5 text-[10px]" :class="row.scope === 'project' ? 'border-[var(--forebrain-brand-1)] text-[var(--forebrain-brand-1)]' : 'border-[var(--forebrain-divider)] text-[var(--forebrain-muted-text)]'">
                  {{ row.scope === 'project' ? t('projects.scopeProject') : t('projects.scopeGlobal') }}
                </span>
              </div>
            </li>
          </ul>
          <p v-else class="mt-3 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('projects.mcpEmpty') }}</p>

          <div v-if="mcpNotApplied.length" class="mt-3 border-t border-[var(--forebrain-divider)] pt-2">
            <p class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('projects.mcpNotAppliedTitle') }}</p>
            <ul class="mt-1 space-y-0.5">
              <li v-for="row in mcpNotApplied" :key="row.name" class="text-[11px] text-[var(--forebrain-text-2)]">
                <span class="font-mono">{{ row.name || '(' + t('projects.unnamed') + ')' }}</span>
                <span class="ml-1 text-[var(--forebrain-muted-text)]">— {{ row.reason }}</span>
              </li>
            </ul>
          </div>
          <p v-if="mcpOverridden.length" class="mt-2 text-[11px] text-[var(--forebrain-muted-text)]">
            {{ t('projects.mcpOverridden', { names: mcpOverridden.join(', ') }) }}
          </p>
        </section>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * One project's detail: the settings that travel with its directory, the
 * conversations opened inside it, and the MCP servers its sessions would run
 * — including the ones waiting on an explicit confirmation.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getErrorMessage, forebrainApi, type ProjectMcpRecord, type ProjectRecord, type ProjectSessionRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const projectId = computed(() => String(route.params.id ?? ''))

const project = ref<ProjectRecord | null>(null)
const sessions = ref<ProjectSessionRecord[]>([])
const mcp = ref<ProjectMcpRecord | null>(null)
const loading = ref(false)
const saving = ref(false)
const consenting = ref(false)
const error = ref('')
const edit = reactive({ name: '', icon: '', description: '', instructions: '', memoryScope: 'shared', resourceAccess: true, trust: false })

const mcpServers = computed(() => mcp.value?.servers ?? [])
const mcpPending = computed(() => mcp.value?.pendingConsent ?? [])
const mcpNotApplied = computed(() => mcp.value?.notApplied ?? [])
const mcpOverridden = computed(() => mcp.value?.overriddenGlobal ?? [])

async function load() {
  loading.value = true
  error.value = ''
  try {
    const id = projectId.value
    if (!id) return
    project.value = await forebrainApi.projectGet(id)
    edit.name = project.value.name
    edit.icon = project.value.icon
    edit.description = project.value.description
    edit.instructions = project.value.instructions
    edit.memoryScope = project.value.memoryScope || 'shared'
    edit.resourceAccess = project.value.resourceAccess
    edit.trust = false
    sessions.value = await forebrainApi.projectSessions(id)
    mcp.value = await forebrainApi.projectMcp(id)
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  error.value = ''
  try {
    project.value = await forebrainApi.projectUpdate(projectId.value, {
      name: edit.name.trim(),
      icon: edit.icon.trim(),
      description: edit.description.trim(),
      instructions: edit.instructions.trim(),
      memoryScope: edit.memoryScope,
      resourceAccess: edit.resourceAccess,
      trust: edit.trust || undefined,
    })
    edit.trust = false
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

async function newSession() {
  error.value = ''
  try {
    const created = await forebrainApi.projectSessionCreate(projectId.value, project.value?.name ?? '')
    openSession(created.id)
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

function openSession(sessionId: string) {
  void router.push({ path: '/', query: { session: sessionId } })
}

async function consent(name: string, allow: boolean) {
  consenting.value = true
  error.value = ''
  try {
    const current = mcpPending.value.map((row) => row.name)
    const allowList = allow ? (current.includes(name) ? current : [...current, name]) : current.filter((item) => item !== name)
    await forebrainApi.projectMcpConsent(projectId.value, allowList)
    mcp.value = await forebrainApi.projectMcp(projectId.value)
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    consenting.value = false
  }
}

function back() {
  void router.push('/projects')
}

function formatTime(unixSeconds: number): string {
  if (!unixSeconds) return ''
  return new Date(unixSeconds * 1000).toLocaleString()
}

onMounted(load)
</script>
