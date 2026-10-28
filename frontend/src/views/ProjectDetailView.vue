<template>
  <!-- The project space shell: the breadcrumb that names the project and its
       directory sits in the shell's top bar, like the workshop's, and the tab
       row below it addresses one project's own configuration surfaces. -->
  <div class="flex min-h-0 flex-1 flex-col overflow-hidden">
    <div class="proj-head">
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
      <RouterView v-else :project="project" :project-id="projectId" @saved="load()" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, h, onMounted, ref, watch, withDirectives } from 'vue'
import { useRoute } from 'vue-router'
import { RouterLink, RouterView } from 'vue-router'
import { ChevronRight } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import { useTopbar } from '@/composables/useTopbar'
import tip from '@/directives/tip'
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
//
// The one tab that follows the project's kind is 会话 / 任务 (plan 153): a batch
// project's work is its tasks, each with its own execution history, and it has
// no conversation to list; every other type keeps 会话 and has no task page.
const batchProject = computed(() => project.value?.projectType === 'batch')
const tabs = computed(() => {
  const list = [
    { key: 'overview', routeName: 'project-overview', label: t('projects.tabOverview') },
    { key: 'rules', routeName: 'project-rules', label: t('projects.tabRules') },
    batchProject.value
      ? { key: 'tasks', routeName: 'project-tasks', label: t('projects.tabTasks') }
      : { key: 'sessions', routeName: 'project-sessions', label: t('projects.tabSessions') },
    { key: 'memory', routeName: 'project-memory', label: t('projects.tabMemory') },
    { key: 'perm', routeName: 'project-perm', label: t('projects.tabPerm') },
    { key: 'mcp', routeName: 'project-mcp', label: t('projects.tabMcp') },
    { key: 'lsp', routeName: 'project-lsp', label: t('projects.tabLsp') },
    { key: 'skills', routeName: 'project-skills', label: t('projects.tabSkills') },
    { key: 'cron', routeName: 'project-cron', label: t('projects.tabCron') },
    { key: 'channels', routeName: 'project-channels', label: t('projects.tabChannels') },
  ]
  return list
})

/**
 * The project's own row in the shell's top bar (owner 2026-10-18): the layer
 * it returns to, the project's name — capped and cut like a title — the
 * directory it is bound to, and whether that directory is trusted. The
 * factory runs inside the shell's render, so saving the project's settings
 * (which reloads it) moves the badge without the page re-registering.
 */
const { set: setTopbar } = useTopbar()

setTopbar({
  leading: () => {
    const current = project.value
    const name = current?.name || current?.id || ''
    return [
      h('div', { class: 'forebrain-topbar-crumb' }, [
        h(RouterLink, {
          to: '/projects',
          class: 'font-medium text-[var(--forebrain-brand-1)] hover:underline',
          'data-testid': 'project-crumb-back',
        }, { default: () => t('projects.title') }),
        h(ChevronRight, { class: 'size-3.5 text-[var(--forebrain-muted-text)]', 'aria-hidden': 'true' }),
        withDirectives(
          h('span', { class: 'forebrain-topbar-name', 'data-testid': 'project-crumb-name' }, name),
          [[tip, name, 'bottom', { trunc: true }]],
        ),
      ]),
      ...(current?.root
        ? [h('span', { class: 'forebrain-topbar-meta font-mono', 'data-testid': 'project-crumb-root' }, current.root)]
        : []),
      ...(current
        ? [h(BadgeComponent, {
          variant: current.trusted ? 'success' : 'gray',
          'data-testid': 'project-crumb-trust',
        }, { default: () => (current.trusted ? t('projects.trusted') : t('projects.untrusted')) })]
        : []),
    ]
  },
})

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
/* The tab row is all the page keeps above its content (prototype .proj-head
   after the breadcrumb moved into the top bar): the tabs and the rule that
   closes them. */
.proj-head {
  border-bottom: 1px solid var(--forebrain-divider);
  padding: 4px 28px 0;
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
