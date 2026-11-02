<template>
  <div class="min-h-[calc(100dvh-0px)] bg-[var(--forebrain-bg)] px-4 py-8">
    <div class="mx-auto max-w-6xl space-y-6">
      <div class="settings-layout">
        <nav class="settings-tabs" :aria-label="t('settings.narrowTabs')" data-testid="settings-tabs">
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
          <SettingsSessionTab v-else-if="activeTab === 'session'" tab="session" :form="form" />
          <SettingsApprovalTab v-else-if="activeTab === 'approval'" tab="approval" :form="form" />
          <SettingsSubagentsTab v-else-if="activeTab === 'subagents'" tab="subagents" :form="form" />
          <SettingsWebToolsTab v-else-if="activeTab === 'net'" tab="net" :form="form" />
          <McpTab v-else-if="activeTab === 'mcp'" />
          <LspTab v-else-if="activeTab === 'lsp'" />
          <HooksTab v-else-if="activeTab === 'hooks'" />
          <CronSettingsTab v-else-if="activeTab === 'cron'" tab="cron" :form="form" />
          <MemorySwitchesTab v-else-if="activeTab === 'memory'" tab="memory" :form="form" />
          <ConfigTab v-else-if="activeTab === 'config'" />
          <RuntimeStatusTab v-else-if="activeTab === 'runtime'" />
          <SettingsSharedSkillsTab v-else-if="activeTab === 'shared-skills'" />
        </section>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * Settings is the global scope: everything that is not bound to a primary agent
 * or a project lives behind these tabs. The tabs and their groups are the
 * prototype's (plan 191 §0, §4.2) — 常规〔外观 / 会话 / 审批与沙箱 / 子代理 /
 * 联网工具〕· 全局服务〔MCP / 语言服务器 / 钩子 / 定时任务 / 记忆 / 共享技能〕·
 * 高级〔配置文件 / 运行状态〕 — and the ones built from the schema share one
 * form, so a value read once serves every tab.
 *
 * `?tab=<key>` is the tab's key, not its position. Narrow screens turn the
 * sub-navigation into a horizontal strip of tabs instead of a column.
 */
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppearanceTab from '@/components/settings/AppearanceTab.vue'
import ConfigTab from '@/components/settings/ConfigTab.vue'
import CronSettingsTab from '@/components/settings/CronSettingsTab.vue'
import HooksTab from '@/components/settings/HooksTab.vue'
import { useSettingsForm } from '@/components/settings/form/useSettingsForm'
import McpTab from '@/components/settings/McpTab.vue'
import LspTab from '@/components/settings/LspTab.vue'
import MemorySwitchesTab from '@/components/settings/MemorySwitchesTab.vue'
import RuntimeStatusTab from '@/components/settings/RuntimeStatusTab.vue'
import SettingsApprovalTab from '@/components/settings/SettingsApprovalTab.vue'
import SettingsSessionTab from '@/components/settings/SettingsSessionTab.vue'
import SettingsSharedSkillsTab from '@/components/settings/SettingsSharedSkillsTab.vue'
import SettingsSubagentsTab from '@/components/settings/SettingsSubagentsTab.vue'
import SettingsWebToolsTab from '@/components/settings/SettingsWebToolsTab.vue'
import { useI18n } from '@/locales'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

/** The one form the schema-driven tabs share: read once, saved per group. */
const form = useSettingsForm()

const tabGroups = computed(() => [
  {
    key: 'general',
    label: t('settings.groupGeneral'),
    tabs: [
      { key: 'appearance', label: t('settings.tabAppearance') },
      { key: 'session', label: t('settings.tabSession') },
      // A machine with no sandbox has nothing to configure here: one posture,
      // no choice, so the whole tab is gone (plan 215 §3.4, 191 §0.12).
      ...(form.approvalPresetsVisible.value ? [{ key: 'approval', label: t('settings.tabApproval') }] : []),
      { key: 'subagents', label: t('settings.tabSubagents') },
      { key: 'net', label: t('settings.tabWebTools') },
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

// The appearance tab is the default: it is the one thing every visitor can act
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

// The sandbox answer arrives with the settings read, so a page opened straight
// on the approval tab is moved to the one that exists only once the tabs are
// known — the same fallback an unknown ?tab= gets, not a blank page.
onMounted(async () => {
  await form.load()
  if (!tabs.value.some((entry) => entry.key === activeTab.value)) activeTab.value = tabFromRoute()
})
</script>

<style scoped>
.settings-layout {
  display: grid;
  gap: 16px;
  grid-template-columns: 220px minmax(0, 1fr);
}
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
/* A narrow window turns the column into a horizontal strip of tabs (the
   prototype's 窄屏 scene): the group headings go, and the tabs scroll. */
@media (max-width: 720px) {
  .settings-layout {
    grid-template-columns: minmax(0, 1fr);
  }
  .settings-tabs {
    flex-direction: row;
    overflow-x: auto;
    gap: 6px;
    padding-bottom: 2px;
  }
  .settings-tabs .settings-group {
    display: none;
  }
  .settings-tab {
    white-space: nowrap;
    padding: 6px 12px;
    border: 1px solid var(--forebrain-divider);
  }
}
</style>
