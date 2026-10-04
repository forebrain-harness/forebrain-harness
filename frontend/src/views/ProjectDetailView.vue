<template>
  <!-- The project space shell: a dark band names the project layer and carries
       the breadcrumb back to the list; the tab bar addresses one project's
       own configuration surfaces. -->
  <div class="flex min-h-0 flex-1 flex-col overflow-hidden">
    <header class="project-band">
      <div class="flex min-w-0 max-w-[50%] shrink-0 items-center gap-3">
        <span class="project-band-badge">{{ t('scope.project') }}</span>
        <nav class="flex min-w-0 items-center gap-2 text-[13px]" :aria-label="t('projects.breadcrumb')">
          <RouterLink to="/projects" class="project-breadcrumb-link">{{ t('projects.title') }}</RouterLink>
          <span class="text-white/40" aria-hidden="true">/</span>
          <span class="truncate font-medium text-white">{{ project?.name || project?.id }}</span>
        </nav>
      </div>
      <!-- The root is shown whole: a long path wraps inside its half of the
           band instead of pushing over the breadcrumb. -->
      <span class="hidden min-w-0 break-all text-right font-mono text-[11px] text-white/60 md:block" :title="project?.root">{{ project?.root }}</span>
    </header>

    <nav class="project-tabs" :aria-label="t('projects.title')">
      <RouterLink
        v-for="tab in tabs"
        :key="tab.key"
        :to="{ name: tab.routeName, params: { id: projectId } }"
        class="project-tab"
        :class="{ 'project-tab--active': route.name === tab.routeName }"
      >
        {{ tab.label }}
      </RouterLink>
    </nav>

    <div class="min-h-0 flex-1 overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
      <div v-if="error" class="mx-auto max-w-4xl rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</div>
      <RouterView v-else :project="project" :project-id="projectId" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { RouterLink, RouterView } from 'vue-router'
import { getErrorMessage, forebrainApi, type ProjectRecord } from '@/lib/api'
import { setActiveProject } from '@/composables/useTenantScope'
import { useI18n } from '@/locales'

const route = useRoute()
const { t } = useI18n()

const projectId = computed(() => String(route.params.id ?? ''))
const project = ref<ProjectRecord | null>(null)
const error = ref('')

// The tabs are fixed by the approved preview; each feature's plan adds its
// own tab as it lands (rules 009, memory 012, perm 009, skills 010, cron
// 013, lsp 015). The shell itself never changes.
const tabs = computed(() => [
  { key: 'overview', routeName: 'project-overview', label: t('projects.tabOverview') },
  { key: 'rules', routeName: 'project-rules', label: t('projects.tabRules') },
  { key: 'sessions', routeName: 'project-sessions', label: t('projects.tabSessions') },
  { key: 'memory', routeName: 'project-memory', label: t('projects.tabMemory') },
  { key: 'perm', routeName: 'project-perm', label: t('projects.tabPerm') },
  { key: 'mcp', routeName: 'project-mcp', label: t('projects.tabMcp') },
  { key: 'lsp', routeName: 'project-lsp', label: t('projects.tabLsp') },
  { key: 'skills', routeName: 'project-skills', label: t('projects.tabSkills') },
  { key: 'cron', routeName: 'project-cron', label: t('projects.tabCron') },
])

async function load() {
  error.value = ''
  try {
    project.value = await forebrainApi.projectGet(projectId.value)
    setActiveProject(projectId.value)
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
}

onMounted(() => {
  void load()
})

// Leaving the project space entirely closes the project boundary; the tenant
// is the same one, so nothing tenant-wide is dropped.
watch(() => route.path, (path) => {
  if (!path.startsWith('/projects/')) setActiveProject('')
})
watch(projectId, () => {
  if (projectId.value) void load()
})
</script>

<style scoped>
.project-band {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 12px 20px;
  background: #0f1d38;
  color: #ffffff;
}
.project-band-badge {
  display: inline-flex;
  align-items: center;
  border-radius: 9999px;
  padding: 3px 12px;
  font-size: 11px;
  font-weight: 500;
  background: var(--forebrain-brand-1, #1f4a9e);
  color: #ffffff;
}
.project-breadcrumb-link {
  color: rgba(255, 255, 255, 0.72);
  text-decoration: none;
  white-space: nowrap;
}
.project-breadcrumb-link:hover {
  color: #ffffff;
}
.project-tabs {
  display: flex;
  gap: 2px;
  border-bottom: 1px solid var(--forebrain-divider);
  padding: 0 16px;
  background: var(--forebrain-surface);
  overflow-x: auto;
}
.project-tab {
  padding: 10px 14px;
  font-size: 13px;
  color: var(--forebrain-text-2);
  text-decoration: none;
  border-bottom: 2px solid transparent;
  white-space: nowrap;
}
.project-tab:hover {
  color: var(--forebrain-text);
}
.project-tab--active {
  color: var(--forebrain-brand-1);
  border-bottom-color: var(--forebrain-brand-1);
  font-weight: 500;
}
</style>
