import { createRouter, createWebHistory } from 'vue-router'
import { t } from '@/locales'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    {
      path: '/',
      name: 'chat',
      component: () => import('@/views/ChatView.vue'),
      meta: { title: () => t('routes.chatTitle') },
    },
    {
      path: '/agents',
      name: 'agents',
      component: () => import('@/views/AgentsView.vue'),
      meta: { title: () => t('routes.agentsTitle') },
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
      path: '/mcp',
      name: 'mcp',
      component: () => import('@/views/McpView.vue'),
      meta: { title: () => t('routes.mcpTitle') },
    },
    {
      path: '/projects',
      name: 'projects',
      component: () => import('@/views/ProjectsView.vue'),
      meta: { title: () => t('routes.projectsTitle') },
    },
    {
      path: '/projects/:id',
      name: 'project-detail',
      component: () => import('@/views/ProjectDetailView.vue'),
      meta: { title: () => t('routes.projectsTitle') },
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
      path: '/hooks',
      name: 'hooks',
      component: () => import('@/views/HooksView.vue'),
      meta: { title: () => t('routes.hooksTitle') },
    },
    {
      path: '/config',
      name: 'config',
      component: () => import('@/views/ConfigView.vue'),
      meta: { title: () => t('routes.configTitle') },
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

router.afterEach((to) => {
  const title = to.meta?.title
  if (typeof title === 'function') {
    document.title = title()
  } else if (typeof title === 'string') {
    document.title = title
  }
})

export default router
