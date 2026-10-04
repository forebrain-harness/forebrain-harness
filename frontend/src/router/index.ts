import { createRouter, createWebHistory } from 'vue-router'
import { onGatewayUnauthorized, probeGatewaySession } from '@/lib/gatewaySession'
import { t } from '@/locales'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    {
      path: '/login',
      name: 'login',
      component: () => import('@/views/LoginView.vue'),
      meta: { bare: true, title: () => t('routes.loginTitle') },
    },
    {
      path: '/workshop',
      name: 'workshop',
      component: () => import('@/views/WorkshopView.vue'),
      meta: { title: () => t('workshop.title') },
    },
    {
      path: '/skills',
      name: 'skills',
      component: () => import('@/views/SkillsView.vue'),
      meta: { title: () => t('skills.title') },
    },
    {
      path: '/rules',
      name: 'rules',
      component: () => import('@/views/RulesView.vue'),
      meta: { title: () => t('routes.rulesTitle') },
    },
    {
      path: '/subagents',
      name: 'subagents',
      component: () => import('@/views/SubagentsView.vue'),
      meta: { title: () => t('routes.subagentsTitle') },
    },
    {
      path: '/',
      name: 'chat',
      component: () => import('@/views/ChatView.vue'),
      meta: { title: () => t('routes.chatTitle') },
    },
    {
      path: '/permissions',
      name: 'permissions',
      component: () => import('@/views/PermissionsView.vue'),
      meta: { title: () => t('routes.permissionsTitle') },
    },
    {
      path: '/tools',
      name: 'tools',
      component: () => import('@/views/ToolsView.vue'),
      meta: { title: () => t('routes.toolsTitle') },
    },
    {
      path: '/projects',
      name: 'projects',
      component: () => import('@/views/ProjectsView.vue'),
      meta: { title: () => t('routes.projectsTitle') },
    },
    {
      path: '/projects/:id',
      component: () => import('@/views/ProjectDetailView.vue'),
      children: [
        { path: '', redirect: (to) => `/projects/${to.params.id}/overview` },
        { path: 'overview', name: 'project-overview', component: () => import('@/components/project/ProjectOverview.vue'), meta: { title: () => t('routes.projectsTitle') } },
        { path: 'rules', name: 'project-rules', component: () => import('@/components/project/ProjectRules.vue'), meta: { title: () => t('routes.projectsTitle') } },
        { path: 'sessions', name: 'project-sessions', component: () => import('@/components/project/ProjectSessions.vue'), meta: { title: () => t('routes.projectsTitle') } },
        { path: 'memory', name: 'project-memory', component: () => import('@/components/project/ProjectMemory.vue'), meta: { title: () => t('routes.projectsTitle') } },
        { path: 'perm', name: 'project-perm', component: () => import('@/components/project/ProjectPerm.vue'), meta: { title: () => t('routes.projectsTitle') } },
        { path: 'mcp', name: 'project-mcp', component: () => import('@/components/project/ProjectMcp.vue'), meta: { title: () => t('routes.projectsTitle') } },
        { path: 'lsp', name: 'project-lsp', component: () => import('@/components/project/ProjectLsp.vue'), meta: { title: () => t('routes.projectsTitle') } },
        { path: 'skills', name: 'project-skills', component: () => import('@/components/project/ProjectSkills.vue'), meta: { title: () => t('routes.projectsTitle') } },
        { path: 'cron', name: 'project-cron', component: () => import('@/components/project/ProjectCron.vue'), meta: { title: () => t('routes.projectsTitle') } },
      ],
    },
    {
      path: '/cron',
      name: 'cron',
      component: () => import('@/views/CronView.vue'),
      meta: { title: () => t('routes.cronTitle') },
    },
    {
      path: '/channels',
      name: 'channels',
      component: () => import('@/views/ChannelsView.vue'),
      meta: { title: () => t('routes.channelsTitle') },
    },
    {
      path: '/providers',
      name: 'providers',
      component: () => import('@/views/ProvidersView.vue'),
      meta: { title: () => t('routes.providersTitle') },
    },
    {
      path: '/memories',
      name: 'memories',
      component: () => import('@/views/MemoriesView.vue'),
      meta: { title: () => t('routes.memoriesTitle') },
    },
    {
      path: '/settings',
      name: 'settings',
      component: () => import('@/views/SettingsView.vue'),
      meta: { title: () => t('routes.settingsTitle') },
    },
  ],
})

// The gateway session decides what a navigation may reach: without it a page
// would mount its shell and then watch every request fail with 401.
router.beforeEach(async (to) => {
  if (to.name === 'login') return true
  if ((await probeGatewaySession()) === 'required') {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  return true
})

// api.ts reports 401s here (it cannot import the router without a cycle).
onGatewayUnauthorized(() => {
  const current = router.currentRoute.value
  if (current.name !== 'login') {
    void router.replace({ name: 'login', query: { redirect: current.fullPath } })
  }
})

router.afterEach((to) => {
  const title = to.meta?.title
  if (typeof title === 'function') {
    document.title = title()
  } else if (typeof title === 'string') {
    document.title = title
  }
})

export default router
