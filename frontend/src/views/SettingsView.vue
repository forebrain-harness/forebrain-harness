<template>
  <div class="min-h-[calc(100dvh-0px)] bg-[var(--forebrain-bg)] px-4 py-8">
    <div class="mx-auto max-w-6xl space-y-6">
      <div class="grid gap-4 lg:grid-cols-[220px_minmax(0,1fr)]">
        <nav class="settings-tabs" :aria-label="t('settings.title')" data-testid="settings-tabs">
          <template v-for="group in tabGroups" :key="group.key">
            <div class="settings-group" :data-group="group.key">{{ group.label }}</div>
            <button
              v-for="tab in group.tabs"
              :key="tab.key"
              type="button"
              class="settings-tab"
              :class="{ 'settings-tab--active': tab.key === activeTab }"
              :aria-selected="tab.key === activeTab"
              role="tab"
              @click="activeTab = tab.key"
            >
              {{ tab.label }}
            </button>
          </template>
        </nav>

        <section class="min-w-0">
          <AppearanceTab v-if="activeTab === 'appearance'" />
          <SettingsApprovalTab v-else-if="activeTab === 'approval'" />
          <McpTab v-else-if="activeTab === 'mcp'" />
          <LspTab v-else-if="activeTab === 'lsp'" />
          <HooksTab v-else-if="activeTab === 'hooks'" />
          <CronSettingsTab v-else-if="activeTab === 'cron'" />
          <MemorySwitchesTab v-else-if="activeTab === 'memory'" />
          <ConfigTab v-else-if="activeTab === 'config'" />
          <RuntimeStatusTab v-else-if="activeTab === 'runtime'" />
          <SettingsSharedSkillsTab v-else-if="activeTab === 'shared-skills'" />
        </section>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppearanceTab from '@/components/settings/AppearanceTab.vue'
import SettingsApprovalTab from '@/components/settings/SettingsApprovalTab.vue'
import ConfigTab from '@/components/settings/ConfigTab.vue'
import CronSettingsTab from '@/components/settings/CronSettingsTab.vue'
import HooksTab from '@/components/settings/HooksTab.vue'
import McpTab from '@/components/settings/McpTab.vue'
import LspTab from '@/components/settings/LspTab.vue'
import MemorySwitchesTab from '@/components/settings/MemorySwitchesTab.vue'
import RuntimeStatusTab from '@/components/settings/RuntimeStatusTab.vue'
import SettingsSharedSkillsTab from '@/components/settings/SettingsSharedSkillsTab.vue'
import { useI18n } from '@/locales'

/**
 * Settings is the global scope: everything that is not bound to a primary
 * agent or a project lives behind these tabs. The tabs are grouped (owner
 * 2026-10-18, after the design's sub-navigation): what every visitor tunes,
 * the services that run for every primary agent, and the expert surfaces.
 * Later plans append a tab by adding it to its group; `?tab=<key>` is the
 * key of the tab, not its position.
 */
const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const tabGroups = computed(() => [
  {
    key: 'general',
    label: t('settings.groupGeneral'),
    tabs: [
      { key: 'appearance', label: t('settings.tabAppearance') },
      { key: 'approval', label: t('settings.tabApproval') },
    ],
  },
  {
    key: 'services',
    label: t('settings.groupServices'),
    tabs: [
      { key: 'mcp', label: t('settings.tabMcp') },
      { key: 'lsp', label: t('settings.tabLsp') },
      { key: 'hooks', label: t('settings.tabHooks') },
      { key: 'cron', label: t('settings.tabCron') },
      { key: 'memory', label: t('settings.tabMemory') },
      { key: 'shared-skills', label: t('settings.tabSharedSkills') },
    ],
  },
  {
    key: 'advanced',
    label: t('settings.groupAdvanced'),
    tabs: [
      { key: 'config', label: t('settings.tabConfig') },
      { key: 'runtime', label: t('settings.tabRuntime') },
    ],
  },
])

const tabs = computed(() => tabGroups.value.flatMap((group) => group.tabs))

// The tab lives in ?tab=<key> so other pages can link straight to one. The
// appearance tab is the default: it is the one thing every visitor can act
// on, and the visual spec's entry point.
function tabFromRoute(): string {
  const tab = route.query.tab
  const key = Array.isArray(tab) ? tab[0] : tab
  return typeof key === 'string' && tabs.value.some((entry) => entry.key === key) ? key : 'appearance'
}

const activeTab = ref(tabFromRoute())

watch(
  () => route.query.tab,
  () => {
    const tab = tabFromRoute()
    if (tab !== activeTab.value) activeTab.value = tab
  },
)

// replace, not push: switching tabs is not a page in the history.
watch(activeTab, (tab) => {
  if (route.query.tab !== tab) void router.replace({ query: { ...route.query, tab } })
})
</script>

<style scoped>
.settings-tabs {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.settings-tab {
  text-align: left;
  border-radius: 8px;
  padding: 9px 12px;
  font-size: 13px;
  color: var(--forebrain-text-2);
}
.settings-tab:hover {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text);
}
.settings-tab--active {
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
  font-weight: 500;
}
/* The group's small heading, above the tabs it holds (the design's
   `.subnav .grp`): quieter and tighter than a tab, and not clickable. */
.settings-group {
  padding: 10px 10px 4px;
  font-size: 11px;
  letter-spacing: 0.04em;
  color: var(--forebrain-muted-text);
}
</style>
