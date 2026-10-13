<template>
  <!-- The project space shell: a breadcrumb names the project layer and its
       directory, and the tab bar below addresses one project's own
       configuration surfaces. -->
  <div class="flex min-h-0 flex-1 flex-col overflow-hidden">
    <div class="proj-head">
      <div class="crumb">
        <RouterLink to="/projects" class="crumb-link">{{ t('projects.title') }}</RouterLink>
        <ChevronRight class="crumb-ic" aria-hidden="true" />
        <b>{{ project?.name || project?.id }}</b>
        <span v-if="project?.root" class="crumb-path">{{ project.root }}</span>
        <span v-if="project?.trusted" class="crumb-badge">{{ t('projects.trusted') }}</span>
      </div>
      <nav class="proj-tabs" :aria-label="t('projects.title')">
        <RouterLink
          v-for="tab in tabs"
          :key="tab.key"
          :to="{ name: tab.routeName, params: { id: projectId } }"
          class="proj-tab"
          :class="{ 'proj-tab--active': route.name === tab.routeName }"
        >
          {{ tab.label }}
        </RouterLink>
      </nav>
    </div>

    <div class="min-h-0 flex-1 overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
      <div v-if="error" class="mx-auto max-w-4xl rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</div>
      <RouterView v-else :project="project" :project-id="projectId" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { RouterLink, RouterView } from 'vue-router'
import { ChevronRight } from 'lucide-vue-next'
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
.proj-head {
  display: grid;
  gap: 6px;
  border-bottom: 1px solid var(--forebrain-divider);
  padding: 14px 28px 0;
}
.crumb {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  font-size: 13px;
  color: var(--forebrain-muted-text);
}
.crumb-link {
  color: var(--forebrain-brand-1);
  text-decoration: none;
}
.crumb-link:hover {
  text-decoration: underline;
}
.crumb-ic {
  width: 14px;
  height: 14px;
  color: var(--forebrain-muted-text);
}
.crumb b {
  color: var(--forebrain-text);
  font-weight: 650;
  font-size: 15px;
}
.crumb-path {
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
}
.crumb-badge {
  display: inline-flex;
  align-items: center;
  border-radius: 999px;
  padding: 2px 9px;
  font-size: 11px;
  background: var(--forebrain-success-soft);
  color: var(--forebrain-success);
}
.proj-tabs {
  display: flex;
  gap: 2px;
  overflow-x: auto;
}
.proj-tab {
  padding: 10px 14px;
  font-size: 13px;
  color: var(--forebrain-text-2);
  text-decoration: none;
  border-bottom: 2px solid transparent;
  white-space: nowrap;
}
.proj-tab:hover {
  color: var(--forebrain-text);
}
.proj-tab--active {
  color: var(--forebrain-brand-1);
  border-bottom-color: var(--forebrain-brand-1);
  font-weight: 500;
}
</style>
