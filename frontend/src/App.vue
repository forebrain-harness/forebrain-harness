<template>
<!-- Bare routes (the sign-in page) render without the app shell. -->
<router-view v-if="route.meta.bare" />
<div v-else class="forebrain-app-shell">
    <aside class="forebrain-rail" :class="{ 'forebrain-rail--collapsed': railCollapsed }">
      <div class="forebrain-rail-top">
        <RouterLink to="/" class="forebrain-rail-brand">
          <BrandMark :size="28" class="forebrain-rail-mark" />
          <span class="forebrain-rail-name">
            <span class="forebrain-rail-wordmark"><span class="forebrain-rail-wordmark-strong">Forebrain</span> Harness</span>
            <span class="forebrain-rail-version">{{ appVersion }}</span>
          </span>
        </RouterLink>
      </div>

      <!-- The tenant selector: the one switch point for primary agents. -->
      <div class="forebrain-tenant" ref="tenantMenuRef">
        <button
          v-if="!railCollapsed"
          type="button"
          class="forebrain-tenant-trigger"
          :aria-label="t('agents.selectorLabel')"
          :aria-expanded="tenantMenuOpen"
          aria-haspopup="listbox"
          :disabled="primaryLoading"
          @click="tenantMenuOpen = !tenantMenuOpen"
        >
          <span class="min-w-0 flex-1">
            <span class="forebrain-tenant-label">{{ t('agents.currentAgent', { count: primaryRecords.length }) }}</span>
            <span class="forebrain-tenant-name">{{ activePrimary?.id || t('common.none') }}</span>
          </span>
          <ChevronDown class="forebrain-tenant-caret" :class="{ 'forebrain-tenant-caret--open': tenantMenuOpen }" aria-hidden="true" />
        </button>
        <button
          v-else
          type="button"
          class="forebrain-tenant-tile"
          :aria-label="tenantTileLabel"
          aria-haspopup="listbox"
          :aria-expanded="tenantMenuOpen"
          v-tip:right="tenantTileLabel"
          @click="tenantMenuOpen = !tenantMenuOpen"
        >
          {{ tenantInitial }}
        </button>
        <Teleport to="body">
          <div v-if="tenantMenuOpen" class="forebrain-tenant-backdrop" @pointerdown="tenantMenuOpen = false" />
          <div
            v-if="tenantMenuOpen"
            class="forebrain-tenant-menu"
            role="listbox"
            :style="{ left: `${railWidth}px`, top: tenantMenuTop }"
          >
            <div v-for="agent in primaryRecords" :key="agent.id" class="forebrain-tenant-row">
              <button
                type="button"
                role="option"
                :aria-selected="agent.id === activePrimaryId"
                class="forebrain-tenant-option"
                :class="{ 'forebrain-tenant-option--active': agent.id === activePrimaryId }"
                @click="selectPrimaryAgent(agent.id)"
              >
                <span class="forebrain-tenant-option-name">
                  <span v-if="agent.id === activePrimaryId" class="forebrain-tenant-dot" aria-hidden="true" />
                  {{ agent.id }}
                </span>
                <span class="forebrain-tenant-option-path">{{ agent.workspaceRoot }}</span>
              </button>
              <button
                v-if="canDeleteAgent(agent.id)"
                type="button"
                class="forebrain-tenant-option-del"
                :aria-label="t('agents.deleteAria', { id: agent.id })"
                v-tip:right="t('agents.deleteButton')"
                @click.stop="askDeleteAgent(agent.id)"
              >
                <Trash2 class="forebrain-tenant-option-del-ic" aria-hidden="true" />
              </button>
            </div>
          </div>
        </Teleport>
        <!-- A switch or a load that failed says so, in the server's words. -->
        <p v-if="primaryError && !railCollapsed" class="forebrain-tenant-error" role="alert" data-testid="tenant-error">{{ primaryError }}</p>
      </div>

      <nav class="forebrain-rail-group" :aria-label="navAria">
        <button
          type="button"
          class="forebrain-rail-link"
          :class="{ 'forebrain-rail-link--active': isActive('/'), 'forebrain-rail-link--open': chatDrawerOpen }"
          :aria-label="L.chat"
          v-tip:right="railCollapsed ? L.chat : ''"
          @click="toggleChatDrawer"
        >
          <MessageSquare class="forebrain-rail-ic" aria-hidden="true" />
          <span>{{ L.chat }}</span>
          <ChevronRight v-if="!railCollapsed" class="forebrain-rail-caret" :class="{ 'forebrain-rail-caret--open': chatDrawerOpen }" aria-hidden="true" />
        </button>
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="forebrain-rail-link"
          :class="{ 'forebrain-rail-link--active': isActive(item.to) }"
          :aria-label="item.label"
          v-tip:right="railCollapsed ? item.label : ''"
          @click="chatDrawerOpen = false"
        >
          <component :is="item.icon" class="forebrain-rail-ic" aria-hidden="true" />
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>

      <div class="forebrain-rail-foot">
        <RouterLink
          to="/settings"
          class="forebrain-rail-link"
          :class="{ 'forebrain-rail-link--active': isActive('/settings') }"
          :aria-label="L.settings"
          v-tip:right="railCollapsed ? L.settings : ''"
          @click="chatDrawerOpen = false"
        >
          <Settings class="forebrain-rail-ic" aria-hidden="true" />
          <span>{{ L.settings }}</span>
        </RouterLink>
        <!-- Collapsing the rail sits at the foot's right edge — under 设置 once
             the rail is icons only (owner 2026-10-18). -->
        <button
          type="button"
          class="forebrain-rail-collapse"
          data-testid="rail-collapse"
          :aria-label="railCollapsed ? t('rail.expand') : t('rail.collapse')"
          :aria-expanded="!railCollapsed"
          v-tip:right="railCollapsed ? t('rail.expand') : t('rail.collapse')"
          @click="railCollapsed ? expandRail() : collapseRail()"
        >
          <PanelLeftOpen v-if="railCollapsed" class="forebrain-rail-ic" aria-hidden="true" />
          <PanelLeftClose v-else class="forebrain-rail-ic" aria-hidden="true" />
        </button>
      </div>
    </aside>

    <ChatDrawer
      :open="chatDrawerOpen"
      :rail-width="railWidth"
      :active-session-id="chatSessionId"
      @close="chatDrawerOpen = false"
      @select="(id) => { chatDrawerOpen = false; openSession(id) }"
      @new="newChat"
    />
    <!-- The app's one tip, painted where the collapsed rail's hint used to be. -->
    <AppTooltip />

    <div class="forebrain-work">
      <header class="forebrain-topbar">
        <!-- A view that filled the top bar owns its row: what it puts on the
             left replaces the page-title span and what it puts on the right
             sits before the shell's own controls. Every other page keeps the
             plain title. -->
        <template v-if="topbarContent?.leading">
          <component :is="renderTopbar" :render="topbarContent.leading" />
        </template>
        <span v-else class="forebrain-topbar-title" v-tip:bottom.trunc="pageTitle">{{ pageTitle }}</span>
        <span class="forebrain-topbar-spacer" />
        <component
          :is="renderTopbar"
          v-if="topbarContent?.trailing"
          :render="topbarContent.trailing"
        />
        <button
          type="button"
          class="forebrain-chip-btn"
          data-testid="new-agent"
          @click="openNewAgent"
        >
          <Plus aria-hidden="true" />
          <span>{{ t('agents.newButton') }}</span>
        </button>
        <div class="forebrain-language-menu" ref="languageMenuRef">
          <button
            type="button"
            class="forebrain-chip-btn forebrain-language-trigger"
            :aria-label="t('language.label')"
            :aria-expanded="languageMenuOpen"
            v-tip:bottom="t('language.label')"
            @click="languageMenuOpen = !languageMenuOpen"
          >
            <Languages class="forebrain-language-ic" aria-hidden="true" />
            <span class="forebrain-language-text">{{ currentLanguageLabel }}</span>
          </button>
          <div v-if="languageMenuOpen" class="forebrain-language-popover">
            <button
              v-for="item in languageOptions"
              :key="item.value"
              type="button"
              class="forebrain-language-option"
              :class="{ 'forebrain-language-option--active': selectedLocale === item.value }"
              @click="selectLanguage(item.value)"
            >
              <span>{{ item.label }}</span>
              <Check v-if="selectedLocale === item.value" class="forebrain-language-check" aria-hidden="true" />
            </button>
          </div>
        </div>
        <!-- The rail's light/dark switch, last on the right: the shell's one
             theme control lives here now, not in the rail's foot. -->
        <button
          type="button"
          class="forebrain-chip-btn forebrain-chip-btn--sq"
          data-testid="theme-toggle"
          :aria-label="themeToggleLabel"
          v-tip:bottom="themeToggleLabel"
          @click="toggleRailTheme"
        >
          <Sun v-if="isDarkTheme" aria-hidden="true" />
          <Moon v-else aria-hidden="true" />
        </button>
      </header>
      <main class="forebrain-main">
        <router-view v-slot="{ Component }">
          <!-- Keyed on path + tenant, not the full URL: a session change is a
               query change and must not rebuild the conversation view. No
               <transition> here: an out-in leave around async route
               components never completes on a cold load with the bare
               sign-in branch above, leaving the view mounted in memory but
               absent from the DOM. -->
          <component :is="Component" :key="`${route.path}::${activePrimaryId}`" />
        </router-view>
      </main>
    </div>

    <!-- New primary agent: a name is all it takes; creating it switches to it. -->
    <ForebrainModal :open="newAgentOpen" size="sm" :title="t('agents.newTitle')" @close="closeNewAgent">
      <label class="block">
        <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('agents.newNameLabel') }}</span>
        <input
          ref="newAgentInputRef"
          v-model="newAgentName"
          class="forebrain-field mt-1 w-full font-mono"
          :placeholder="t('agents.newNamePlaceholder')"
          autocomplete="off"
          data-testid="new-agent-name"
          @keyup.enter="submitNewAgent"
        />
      </label>
      <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('agents.newHint') }}</p>
      <p v-if="newAgentError" class="text-[12px] text-[var(--forebrain-danger)]" data-testid="new-agent-error">{{ newAgentError }}</p>
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="closeNewAgent">{{ t('common.cancel') }}</button>
        <button
          type="button"
          class="forebrain-btn forebrain-btn-primary text-xs"
          :disabled="newAgentBusy || !newAgentValid"
          data-testid="new-agent-create"
          @click="submitNewAgent"
        >
          {{ newAgentBusy ? t('common.loading') : t('common.create') }}
        </button>
      </template>
    </ForebrainModal>

    <!-- Deleting a primary agent: refused for main and the active one (they
         carry no delete control at all). -->
    <ConfirmDialog
      :open="deleteTarget !== ''"
      :title="t('agents.deleteTitle', { id: deleteTarget })"
      :message="t('agents.deleteBody')"
      :confirm-label="t('agents.deleteButton')"
      danger
      @confirm="confirmDeleteAgent"
      @cancel="deleteTarget = ''"
    />
  </div>
</template>

<script setup lang="ts">
import {
  ScrollText,
  Users,
  Boxes,
  Brain,
  Check,
  ChevronDown,
  ChevronRight,
  FolderGit2,
  Languages,
  MessageSquare,
  Moon,
  PanelLeftClose,
  PanelLeftOpen,
  Plug,
  Plus,
  Settings,
  ShieldCheck,
  Sun,
  Trash2,
  Wrench,
  Sparkles,
} from 'lucide-vue-next'
import { computed, defineComponent, nextTick, onMounted, onUnmounted, ref, watch, type VNode } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import ChatDrawer from '@/components/ChatDrawer.vue'
import AppTooltip from '@/components/common/AppTooltip.vue'
import BrandMark from '@/components/BrandMark.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import { useAppearance } from '@/composables/useAppearance'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'
import { useRailCollapse } from '@/composables/useRailCollapse'
import { resetTenantScope } from '@/composables/useTenantScope'
import { useTopbarContent } from '@/composables/useTopbar'
import { forebrainApi, getErrorMessage } from '@/lib/api'
import { navLabels, type Locale, useI18n } from '@/locales'

/** Draws one contributed top-bar half by calling its factory inside this
 *  render, so the row tracks the filling view's own reactive state. */
const renderTopbar = defineComponent({
  props: { render: { type: Function, required: true } },
  setup: (props) => () => (props.render as () => VNode[])(),
})

const topbarContent = useTopbarContent()

const route = useRoute()
const router = useRouter()
const {
  locale: selectedLocale,
  languageOptions,
  currentLanguageLabel,
  setLocale,
  t,
} = useI18n()
const {
  records: primaryRecords,
  activeId: activePrimaryId,
  active: activePrimary,
  loading: primaryLoading,
  error: primaryError,
  loadPrimaryAgents,
  switchPrimaryAgent,
} = usePrimaryAgents()
const { railTheme, toggleRailTheme } = useAppearance()
const { collapsed: railCollapsed, collapse: collapseRail, expand: expandRail } = useRailCollapse()

const appVersion = __FOREBRAIN_VERSION__
const languageMenuOpen = ref(false)
const languageMenuRef = ref<HTMLElement | null>(null)
const tenantMenuOpen = ref(false)
const tenantMenuRef = ref<HTMLElement | null>(null)
const chatDrawerOpen = ref(false)

// The top bar's "new primary agent" dialog, and the tenant dropdown's delete
// confirmation. The two live in the shell because both act on the tenant list
// the shell already owns.
const newAgentOpen = ref(false)
const newAgentName = ref('')
const newAgentError = ref('')
const newAgentBusy = ref(false)
const newAgentInputRef = ref<HTMLInputElement | null>(null)
const deleteTarget = ref('')

/** The tenant key pattern the loader enforces: a legal id is a legal name. */
const newAgentValid = computed(() => /^[a-z][a-z0-9_-]{0,62}$/.test(newAgentName.value.trim()))

/** The rail's own width: the overlays anchor to its right edge. */
const railWidth = computed(() => (railCollapsed.value ? 64 : 264))

/** The conversation the chat page has open, for the drawer to highlight: the
 *  session the address bar names, when the chat page is where we are. */
const chatSessionId = computed(() => {
  const sid = String(route.query.session ?? '').trim()
  return route.path === '/' && sid ? sid : null
})

/** The collapsed rail names its entries on hover and on focus: every rail
 *  control binds `v-tip:right` to its own label, so the tip appears beside it
 *  only while the rail is icons only (an empty label shows nothing). */

function toggleChatDrawer() {
  chatDrawerOpen.value = !chatDrawerOpen.value
}

async function openSession(id: string) {
  await router.push({ path: '/', query: { ...route.query, session: id } })
}

/**
 * A new conversation: open the new-chat page with nothing selected. The page
 * itself preselects the last project and creates the conversation on the first
 * message (plan 029), so no session is created here. `new=1` tells the chat
 * page not to jump back to the last-used conversation.
 */
function newChat() {
  chatDrawerOpen.value = false
  void router.push({ path: '/', query: { new: '1' } })
}

/** Open the new-primary-agent dialog with an empty name and no stale error. */
function openNewAgent() {
  newAgentName.value = ''
  newAgentError.value = ''
  newAgentOpen.value = true
  void nextTick(() => newAgentInputRef.value?.focus())
}

function closeNewAgent() {
  newAgentOpen.value = false
  newAgentName.value = ''
  newAgentError.value = ''
}

// The server's words for the two refusals a create can hit: a taken name and
// an illegal one. Anything else is shown as it came.
function createAgentErrorText(cause: unknown): string {
  const status = (cause as { response?: { status?: number } })?.response?.status
  if (status === 409) return t('agents.newExists')
  if (status === 400) return t('agents.newInvalid')
  return getErrorMessage(cause)
}

/**
 * Create the named primary agent and switch to it: the new agent becomes the
 * tenant the shell is on, exactly as picking one in the selector would.
 */
async function submitNewAgent() {
  if (newAgentBusy.value || !newAgentValid.value) return
  const id = newAgentName.value.trim()
  newAgentBusy.value = true
  newAgentError.value = ''
  try {
    await forebrainApi.createPrimaryAgent({ id })
    // The open conversation belonged to the agent being left.
    if (route.query.session) {
      await router.replace({ query: { ...route.query, session: undefined } })
    }
    const before = activePrimaryId.value
    try {
      await switchPrimaryAgent(id)
    } finally {
      if (activePrimaryId.value !== before) resetTenantScope()
    }
    closeNewAgent()
  } catch (cause) {
    newAgentError.value = createAgentErrorText(cause)
  } finally {
    newAgentBusy.value = false
  }
}

/** main and the active primary agent cannot be deleted, so they carry no
 *  delete control; the server refuses them too. */
function canDeleteAgent(id: string): boolean {
  return id !== 'main' && id !== activePrimaryId.value
}

function askDeleteAgent(id: string) {
  tenantMenuOpen.value = false
  deleteTarget.value = id
}

async function confirmDeleteAgent() {
  const id = deleteTarget.value
  deleteTarget.value = ''
  if (!id) return
  try {
    await forebrainApi.deletePrimaryAgent(id)
    await loadPrimaryAgents()
  } catch (cause) {
    primaryError.value = getErrorMessage(cause)
  }
}

const tenantInitial = computed(() => (activePrimary.value?.id ?? '?').trim().charAt(0).toUpperCase() || '?')

const tenantTileLabel = computed(() => t('rail.currentAgentName', { name: activePrimary.value?.id ?? '' }))

const tenantMenuTop = computed(() => {
  const rect = tenantMenuRef.value?.getBoundingClientRect()
  return rect ? `${rect.top}px` : '76px'
})

// One flat list: destinations are peers, and the Chat entry above them opens
// the drawer instead of navigating. Group labels are gone with the groups.
// The final flat menu, order fixed by the approved preview. Rules (009),
// skills (010) and the workshop (011) add their own entries later; every
// item carries the scope its page operates in.
const navItems = computed(() => {
  const L = navLabels()
  return [
    { to: '/projects', label: L.projects, icon: FolderGit2, scope: 'project' as const },
    { to: '/rules', label: L.rules, icon: ScrollText, scope: 'agent' as const },
    { to: '/skills', label: L.skills, icon: Sparkles, scope: 'agent' as const },
    { to: '/mcp', label: L.mcp, icon: Plug, scope: 'agent' as const },
    { to: '/subagents', label: L.subagents, icon: Users, scope: 'agent' as const },
    { to: '/providers', label: L.providers, icon: Boxes, scope: 'agent' as const },
    { to: '/tools', label: L.tools, icon: Wrench, scope: 'agent' as const },
    { to: '/permissions', label: L.permissions, icon: ShieldCheck, scope: 'agent' as const },
    { to: '/memories', label: L.memories, icon: Brain, scope: 'agent' as const },
  ]
})

const L = computed(() => navLabels())

const navAria = computed(() => navLabels().aria)

// The pages the rail lists, then the two the top bar reaches on its own: every
// destination names itself here, so no page ever falls back to the chat title
// (the prototype's titles table, app-preview.html :2829).
const extraTitles: { to: string; title: () => string }[] = [
  { to: '/workshop', title: () => L.value.workshop },
  { to: '/settings', title: () => L.value.settings },
]

const pageTitle = computed(() => {
  if (isActive('/')) return L.value.chat
  for (const item of navItems.value) {
    if (isActive(item.to)) return item.label
  }
  for (const item of extraTitles) {
    if (isActive(item.to)) return item.title()
  }
  return L.value.chat
})

const isDarkTheme = computed(() => railTheme.value === 'dark')
const themeToggleLabel = computed(() => (isDarkTheme.value ? t('theme.toLight') : t('theme.toDark')))

function selectLanguage(nextLocale: Locale) {
  setLocale(nextLocale)
  languageMenuOpen.value = false
}

async function selectPrimaryAgent(id: string) {
  tenantMenuOpen.value = false
  if (!id || id === activePrimaryId.value) return
  try {
    // The open conversation belongs to the agent being left. The address bar
    // is what the chat page opens, and the page is rebuilt for the new
    // tenant the moment the switch lands — so the session leaves the
    // address first, or the new tenant's page would reopen it.
    if (route.query.session) {
      await router.replace({ query: { ...route.query, session: undefined } })
    }
    const before = activePrimaryId.value
    try {
      await switchPrimaryAgent(id)
    } finally {
      // The tenant the gateway is on now — the one picked, or the one a
      // failed switch recorded before its rebuild failed — owns everything
      // the frontend shows; what was loaded under the old one is dropped.
      if (activePrimaryId.value !== before) resetTenantScope()
    }
  } catch {
    // switchPrimaryAgent records the failure on the shared store and reloads
    // the list, so the rail shows the agent the gateway is actually bound to.
  }
}

// The tenant menu is teleported out of the rail, so "outside the rail's
// selector" says nothing about it — every press on one of its options would
// count as outside and close it before the click lands. Its own backdrop is
// what catches presses outside it.
function handleDocumentPointerDown(event: PointerEvent) {
  const target = event.target
  if (!(target instanceof Node)) return
  if (!languageMenuRef.value?.contains(target)) languageMenuOpen.value = false
}

function isActive(path: string) {
  if (path === '/') {
    return route.path === '/' || route.path === ''
  }
  // The workshop is not its own rail entry (plan 020): it highlights the page
  // it was opened from — the agent's skills, or the project it was opened in.
  if (route.path === '/workshop') {
    const scope = String(route.query.scope ?? '')
    if (scope === 'project') return path === '/projects'
    return path === '/skills'
  }
  return route.path === path || route.path.startsWith(path + '/')
}

// A narrow window has no room for the rail's names, so the rail is collapsed —
// and it is *collapsed*, one state with one truth: the flag the toggle and
// `aria-expanded` read, not a stylesheet that hides the words behind its back.
// Widening the window gives the rail back.
let railMq: MediaQueryList | null = null

function syncRailToWidth() {
  if (!railMq) return
  if (railMq.matches) collapseRail()
  else expandRail()
}

onMounted(() => {
  document.addEventListener('pointerdown', handleDocumentPointerDown)
  railMq = window.matchMedia('(max-width: 900px)')
  syncRailToWidth()
  railMq.addEventListener('change', syncRailToWidth)
})

// The shell loads its agents when it is actually shown: the bare sign-in page
// must not fire business requests before a session exists.
watch(() => route.meta.bare, (bare) => {
  if (!bare) void loadPrimaryAgents()
}, { immediate: true })

onUnmounted(() => {
  document.removeEventListener('pointerdown', handleDocumentPointerDown)
  railMq?.removeEventListener('change', syncRailToWidth)
})

watch(selectedLocale, () => {
  const title = route.meta?.title
  if (typeof title === 'function') document.title = title()
})
</script>

