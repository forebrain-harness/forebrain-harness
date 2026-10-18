import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { RouterView, createMemoryHistory, createRouter } from 'vue-router'

import tip from '@/directives/tip'
import { useTopbarContent } from '@/composables/useTopbar'
import { forebrainApi, type ProjectRecord } from '@/lib/api'
import { setLocale } from '@/locales'
import ProjectDetailView from './ProjectDetailView.vue'

/**
 * The project space shell (plan 075): the breadcrumb — layer, project name,
 * directory, trust — is the shell top bar's own row now, and the page keeps
 * only the tab row under it. Saving the settings tab reloads the project, so
 * the badge in that row follows the new trust.
 */
const at = 1760000000

function project(overrides: Partial<ProjectRecord> = {}): ProjectRecord {
  return {
    id: 'p-alpha',
    name: 'alpha',
    icon: '',
    description: '主仓库',
    instructions: '',
    root: '/Users/me/alpha',
    projectKey: 'alpha',
    memoryScope: 'shared',
    resourceAccess: true,
    pinned: false,
    createdAt: at,
    updatedAt: at,
    trusted: true,
    ...overrides,
  }
}

const tabRoutes = [
  'project-overview', 'project-rules', 'project-sessions', 'project-memory', 'project-perm',
  'project-mcp', 'project-lsp', 'project-skills', 'project-cron', 'project-channels',
]

/** The settings tab, standing in for the routed child: it only emits. */
const SettingsStub = { template: '<button type="button" data-testid="child-saved" @click="$emit(\'saved\')" />' }

async function mountShell(overrides: Partial<ProjectRecord> = {}) {
  vi.spyOn(forebrainApi, 'projectGet').mockResolvedValue(project(overrides))
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: { template: '<div />' } },
      { path: '/projects', component: { template: '<div />' } },
      {
        path: '/projects/:id',
        component: ProjectDetailView,
        children: tabRoutes.map((name) => ({
          path: name.replace('project-', ''),
          name,
          component: name === 'project-overview' ? SettingsStub : { template: '<div />' },
        })),
      },
    ],
  })
  await router.push('/projects/p-alpha/overview')
  await router.isReady()
  // The shell draws the routed tab through its own RouterView, so the router
  // has to render the shell: mounting it directly would nest a second copy.
  const wrapper = mount({ components: { RouterView }, template: '<RouterView />' }, {
    global: { plugins: [router], directives: { tip } },
  })
  await flushPromises()
  return wrapper
}

function leadingNodes() {
  return useTopbarContent().value?.leading?.() ?? []
}

describe('ProjectDetailView', () => {
  beforeAll(() => setLocale('zh'))

  beforeEach(() => {
    vi.spyOn(forebrainApi, 'projectUpdate').mockResolvedValue(project())
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('moves the breadcrumb into the top bar: layer, name, directory and trust', async () => {
    const wrapper = await mountShell()

    const nodes = leadingNodes()
    const crumb = nodes[0]
    expect(crumb.props?.class).toBe('forebrain-topbar-crumb')
    const [back, chevron, name] = crumb.children as Array<{ type: unknown; props?: Record<string, unknown>; children: unknown }>
    // The layer name leads back to the projects list.
    expect(back.props?.to).toBe('/projects')
    expect(chevron).toBeTruthy()
    expect(name.props?.class).toBe('forebrain-topbar-name')
    expect(name.children).toBe('alpha')

    expect(nodes[1].props?.['data-testid']).toBe('project-crumb-root')
    expect(nodes[1].props?.class).toContain('forebrain-topbar-meta')
    expect(nodes[1].children).toBe('/Users/me/alpha')

    expect(nodes[2].props?.['data-testid']).toBe('project-crumb-trust')
    expect(nodes[2].props?.variant).toBe('success')
    expect((nodes[2].children as { default: () => string }).default()).toBe('已信任')

    // The page itself keeps the tab row and nothing above it.
    expect(wrapper.find('.crumb').exists()).toBe(false)
    const head = wrapper.find('.proj-head')
    expect(head.exists()).toBe(true)
    expect(head.findAll(':scope > *').map((child) => child.classes())).toEqual([['proj-tabs']])
    expect(wrapper.findAll('.proj-tab').map((tab) => tab.text())[0]).toBe('项目设置')
    expect(wrapper.findAll('.proj-tab')).toHaveLength(10)

    wrapper.unmount()
  })

  it('reloads the project when the settings tab says it saved', async () => {
    const wrapper = await mountShell()
    const get = vi.mocked(forebrainApi.projectGet)
    expect(get).toHaveBeenCalledTimes(1)

    await wrapper.find('[data-testid="child-saved"]').trigger('click')
    await flushPromises()

    // The reload is what moves the top bar's trust badge and every tab's gate.
    expect(get).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('badges an untrusted directory grey', async () => {
    const wrapper = await mountShell({ trusted: false })

    const nodes = leadingNodes()
    expect(nodes[2].props?.variant).toBe('gray')
    expect((nodes[2].children as { default: () => string }).default()).toBe('未信任')

    wrapper.unmount()
  })
})
