<template>
  <div class="min-h-[calc(100dvh-0px)] bg-[var(--forebrain-bg)] px-4 py-8">
    <div class="mx-auto max-w-6xl space-y-6">
      <div class="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 class="font-serif text-2xl font-medium text-[var(--forebrain-text)]">{{ t('settings.title') }}</h1>
          <p class="mt-1 text-sm text-[var(--forebrain-text-2)]">{{ t('settings.description') }}</p>
        </div>
        <RouterLink
          to="/"
          class="inline-flex rounded-lg border border-[var(--forebrain-button-alt-bg)] bg-[var(--forebrain-button-alt-bg)] px-4 py-2 text-sm font-medium text-[var(--forebrain-text)] hover:bg-[var(--forebrain-button-alt-hover-bg)]"
        >
          {{ t('settings.backToChat') }}
        </RouterLink>
      </div>

      <p v-if="error" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>
      <p v-if="notice" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-text)]">{{ notice }}</p>

      <div class="grid gap-4 xl:grid-cols-[320px_minmax(0,1fr)]">
        <section class="space-y-4">
          <div class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-4 py-3 text-sm text-[var(--forebrain-text)] shadow-sm">
            <div class="font-medium text-[var(--forebrain-text)]">{{ t('settings.healthCheck') }}</div>
            <p class="mt-2 text-[var(--forebrain-text-2)]">{{ t('settings.status', { status: healthStatus }) }}</p>
            <p v-if="healthError" class="mt-2 text-sm text-[var(--forebrain-danger)]">{{ healthError }}</p>
          </div>

          <div class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-4 py-3 text-sm text-[var(--forebrain-text)] shadow-sm">
            <div class="font-medium text-[var(--forebrain-text)]">{{ t('settings.skillsSummary') }}</div>
            <p class="mt-2 text-[var(--forebrain-text-2)]">{{ t('settings.discovered', { count: skills.length }) }}</p>
            <p class="text-[var(--forebrain-text-2)]">{{ t('settings.enabled', { count: enabledSkillCount }) }}</p>
          </div>
        </section>

        <section class="space-y-4">
          <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4 shadow-sm">
            <div class="mb-4 flex flex-wrap items-center justify-between gap-3 border-b border-[var(--forebrain-divider)] pb-3">
              <div>
                <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('settings.skillsManagement') }}</div>
                <div class="mt-1 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('settings.skillsManagementDescription') }}</div>
              </div>
              <div class="flex flex-wrap gap-2">
                <select
                  v-if="skillProjects.length"
                  v-model="skillProjectId"
                  class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[12px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]"
                >
                  <option value="">{{ t('settings.skillScopeGateway') }}</option>
                  <option v-for="project in skillProjects" :key="project.id" :value="project.id">
                    {{ project.name || project.root }}
                  </option>
                </select>
                <button type="button" @click="loadSkillsOverview" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-2 text-[12px] font-medium text-[var(--forebrain-text)] hover:bg-[var(--forebrain-input-hover-bg)]">{{ t('common.refresh') }}</button>
                <button type="button" :disabled="skillsSaving" @click="saveSkillToggleState" class="rounded-xl bg-[var(--forebrain-brand-1)] px-4 py-2 text-[12px] font-medium text-[var(--forebrain-on-brand)] shadow-[var(--forebrain-brand-outline-shadow)] disabled:cursor-not-allowed disabled:opacity-60 hover:opacity-95">{{ t('settings.saveEnabled') }}</button>
              </div>
            </div>

            <div class="grid gap-4 lg:grid-cols-[minmax(0,1.25fr)_minmax(0,0.9fr)]">
              <div class="space-y-4">
                <SkillsInstalledList
                  :skills="skills"
                  :enabled-paths="enabledSkillPaths"
                  :loading="skillsLoading"
                  :saving="skillsSaving"
                  @toggle="setSkillPathEnabled"
                  @inspect="inspectSkill"
                />

                <div class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] p-3">
                  <div class="mb-3 text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('settings.createOrUpdateSkill') }}</div>
                  <div class="grid gap-3 md:grid-cols-[180px_1fr]">
                    <input v-model="editorForm.name" :placeholder="t('settings.skillNamePlaceholder')" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]" />
                    <div class="flex flex-wrap gap-2">
                      <button type="button" :disabled="editing" @click="createSkill" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-2 text-[13px] font-medium text-[var(--forebrain-text)] disabled:cursor-not-allowed disabled:opacity-60 hover:bg-[var(--forebrain-input-hover-bg)]">{{ t('common.create') }}</button>
                      <button type="button" :disabled="editing || !editorForm.name.trim()" @click="updateSkill" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-2 text-[13px] font-medium text-[var(--forebrain-text)] disabled:cursor-not-allowed disabled:opacity-60 hover:bg-[var(--forebrain-input-hover-bg)]">{{ t('common.update') }}</button>
                      <button type="button" :disabled="editing || !editorForm.name.trim()" @click="loadSkillIntoEditor" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-2 text-[13px] font-medium text-[var(--forebrain-text)] disabled:cursor-not-allowed disabled:opacity-60 hover:bg-[var(--forebrain-input-hover-bg)]">{{ t('common.load') }}</button>
                    </div>
                  </div>
                  <textarea v-model="editorForm.content" rows="14" :placeholder="t('settings.skillContentPlaceholder')" class="mt-3 w-full rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-code-bg)] px-3 py-3 font-mono text-[12px] leading-relaxed text-[var(--forebrain-code-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]" />
                </div>

                <div class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] p-3">
                  <div class="mb-3 text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('settings.installSkillPackage') }}</div>
                  <div class="grid gap-3 md:grid-cols-2">
                    <input v-model="installForm.sourceRef" :placeholder="t('settings.sourceRefPlaceholder')" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)] md:col-span-2" />
                    <select v-model="installForm.dest" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]">
                      <option value="project">project</option>
                      <option value="workspace">workspace</option>
                      <option value="global">global</option>
                    </select>
                    <input v-model="installForm.skill" :placeholder="t('settings.skillInstallPlaceholder')" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]" />
                    <input v-model="installForm.name" :placeholder="t('settings.installNamePlaceholder')" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]" />
                    <input v-model="installForm.ref" :placeholder="t('settings.gitRefPlaceholder')" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]" />
                  </div>
                  <div class="mt-3 flex flex-wrap items-center gap-3">
                    <button type="button" :disabled="installing" @click="installSkillPackage" class="rounded-xl bg-[var(--forebrain-brand-1)] px-4 py-2 text-[13px] font-medium text-[var(--forebrain-on-brand)] shadow-[var(--forebrain-brand-outline-shadow)] disabled:cursor-not-allowed disabled:opacity-60 hover:opacity-95">{{ t('common.install') }}</button>
                    <span v-if="installing" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('settings.submittingInstall') }}</span>
                    <span v-else-if="lastSkillInstallTaskId" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('settings.latestTask', { id: lastSkillInstallTaskId }) }}</span>
                  </div>
                  <SkillInstallProgressPanel :events="skillInstallEvents" />
                </div>
              </div>

              <div class="space-y-4">
                <div class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] p-3">
                  <div class="mb-3 text-[13px] font-medium text-[var(--forebrain-text)]">{{ inspectorTitle }}</div>
                  <div v-if="inspectorLoading" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] px-3 py-6 text-[13px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
                  <div v-else-if="!inspectorText" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] px-3 py-6 text-[13px] text-[var(--forebrain-muted-text)]">{{ t('settings.selectSkillForDetails') }}</div>
                  <pre v-else class="max-h-[560px] overflow-auto rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-code-bg)] p-3 text-[12px] leading-relaxed text-[var(--forebrain-code-text)]">{{ inspectorText }}</pre>
                </div>
              </div>
            </div>
          </section>

          <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4 shadow-sm">
            <div class="mb-4 flex flex-wrap items-center justify-between gap-3 border-b border-[var(--forebrain-divider)] pb-3">
              <div>
                <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('settings.permissionMemory') }}</div>
                <div class="mt-1 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('settings.currentMode', { mode: permissionsMode }) }}</div>
              </div>
              <button type="button" @click="loadPermissions" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-2 text-[12px] font-medium text-[var(--forebrain-text)] hover:bg-[var(--forebrain-input-hover-bg)]">{{ t('common.refresh') }}</button>
            </div>

            <div class="grid gap-3 md:grid-cols-3">
              <label class="space-y-1 text-[12px] text-[var(--forebrain-text-2)]">
                <span>{{ t('settings.mode') }}</span>
                <select v-model="modeForm.mode" class="w-full rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]">
                  <option value="on-request">on-request</option>
                  <option value="unless-trusted">unless-trusted</option>
                  <option value="never">never</option>
                  <option value="granular">granular</option>
                </select>
              </label>
              <label class="space-y-1 text-[12px] text-[var(--forebrain-text-2)]">
                <span>{{ t('settings.destination') }}</span>
                <select v-model="modeForm.destination" class="w-full rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]">
                  <option value="session">session</option>
                  <option value="localSettings">localSettings</option>
                  <option value="projectSettings">projectSettings</option>
                </select>
              </label>
              <div class="flex items-end">
                <button type="button" @click="setPermissionMode" class="w-full rounded-xl bg-[var(--forebrain-brand-1)] px-4 py-2 text-[13px] font-medium text-[var(--forebrain-on-brand)] shadow-[var(--forebrain-brand-outline-shadow)] hover:opacity-95">{{ t('settings.setMode') }}</button>
              </div>
            </div>

            <div class="mt-5 rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] p-3">
              <div class="mb-3 text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('settings.addRule') }}</div>
              <div class="grid gap-3 md:grid-cols-[120px_120px_1fr_1fr_auto]">
                <select v-model="ruleForm.destination" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]">
                  <option value="session">session</option>
                  <option value="localSettings">localSettings</option>
                  <option value="projectSettings">projectSettings</option>
                </select>
                <select v-model="ruleForm.behavior" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]">
                  <option value="allow">allow</option>
                  <option value="deny">deny</option>
                  <option value="ask">ask</option>
                </select>
                <input v-model="ruleForm.toolName" :placeholder="t('settings.toolPlaceholder')" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]" />
                <input v-model="ruleForm.ruleContent" :placeholder="t('settings.rulePlaceholder')" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]" />
                <button type="button" @click="addPermissionRule" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-2 text-[13px] font-medium text-[var(--forebrain-text)] hover:bg-[var(--forebrain-input-hover-bg)]">{{ t('common.add') }}</button>
              </div>
            </div>

            <div class="mt-5 rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] p-3">
              <div class="mb-3 text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('settings.ruleEvaluation') }}</div>
              <div class="grid gap-3 md:grid-cols-[160px_1fr_auto]">
                <input v-model="evalForm.toolName" :placeholder="t('settings.toolPlaceholder')" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]" />
                <input v-model="evalForm.input" :placeholder="t('settings.inputPlaceholder')" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]" />
                <button type="button" @click="evaluatePermission" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-2 text-[13px] font-medium text-[var(--forebrain-text)] hover:bg-[var(--forebrain-input-hover-bg)]">{{ t('settings.evaluate') }}</button>
              </div>
              <pre v-if="evalResult" class="mt-3 max-h-44 overflow-auto rounded-xl bg-[var(--forebrain-code-bg)] p-3 text-[12px] leading-relaxed text-[var(--forebrain-code-text)]">{{ evalResult }}</pre>
            </div>

            <div class="mt-5">
              <div class="mb-2 flex items-center justify-between text-[13px] font-medium text-[var(--forebrain-text)]">
                <span>{{ t('settings.ruleList') }}</span>
                <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ permissionRules.length }}</span>
              </div>
              <div v-if="permissionsLoading" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] px-3 py-4 text-[13px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
              <div v-else-if="!permissionRules.length" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] px-3 py-8 text-center text-[13px] text-[var(--forebrain-muted-text)]">{{ t('settings.noRules') }}</div>
              <div v-else class="max-h-[420px] overflow-auto rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)]">
                <table class="w-full text-left text-[12px]">
                  <thead class="sticky top-0 bg-[var(--forebrain-surface)] text-[var(--forebrain-text-2)]">
                    <tr>
                      <th class="px-3 py-2 font-medium">source</th>
                      <th class="px-3 py-2 font-medium">behavior</th>
                      <th class="px-3 py-2 font-medium">tool</th>
                      <th class="px-3 py-2 font-medium">rule</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="(r, idx) in permissionRules" :key="`${r.source}-${r.behavior}-${r.toolName}-${r.ruleContent}-${idx}`" class="border-t border-[var(--forebrain-divider)] text-[var(--forebrain-text)]">
                      <td class="px-3 py-2 font-mono text-[11px]">{{ r.source }}</td>
                      <td class="px-3 py-2 font-mono text-[11px]">{{ r.behavior }}</td>
                      <td class="px-3 py-2 font-mono text-[11px]">{{ r.toolName }}</td>
                      <td class="px-3 py-2 font-mono text-[11px]">{{ r.ruleContent || '*' }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
          </section>
        </section>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import SkillInstallProgressPanel, {
  type SkillInstallEventViewModel,
} from '@/components/settings/SkillInstallProgressPanel.vue'
import SkillsInstalledList from '@/components/settings/SkillsInstalledList.vue'
import forebrainApi, {
  type PermissionRuleRecord,
  type ProjectRecord,
  type SkillRecord,
} from '@/lib/api'
import { createForebrainGatewaySessionSocket } from '@/lib/forebrainGatewaySessionSocket'
import {
  parseForebrainSkillLifecycleNotification,
} from '@/lib/forebrainGatewayRuntime'
import { useI18n } from '@/locales'

const { t } = useI18n()

const healthStatus = ref(t('settings.healthChecking'))
const healthError = ref<string | null>(null)
const error = ref('')
const notice = ref('')

const permissionsLoading = ref(false)
const permissionsMode = ref('on-request')
const permissionRules = ref<PermissionRuleRecord[]>([])
const evalResult = ref('')

const skillsLoading = ref(false)
const skillsSaving = ref(false)
const installing = ref(false)
const skills = ref<SkillRecord[]>([])
const enabledSkillPaths = ref<Set<string>>(new Set())
const inspectorTitle = ref(t('settings.detailsViewer'))
const inspectorText = ref('')
const inspectorLoading = ref(false)
const lastSkillInstallTaskId = ref('')
const editing = ref(false)
const skillLifecycleSessionId = ref('default')
const skillInstallEvents = ref<SkillInstallEventViewModel[]>([])
// skillProjectId selects which project the skill lifecycle operates on. Empty
// means the gateway's own launch project — one gateway per checkout, which is
// the common case; picking a project routes the same operations through the
// project-scoped endpoints instead.
const skillProjectId = ref('')
const skillProjects = ref<ProjectRecord[]>([])

const modeForm = ref({ mode: 'on-request', destination: 'session' })
const ruleForm = ref({ destination: 'session', behavior: 'allow', toolName: 'Bash', ruleContent: '' })
const evalForm = ref({ toolName: 'Bash', input: '' })
const installForm = ref({
  sourceRef: '',
  dest: 'global' as 'project' | 'workspace' | 'global',
  skill: '',
  name: '',
  ref: '',
})
const editorForm = ref({
  name: '',
  content: '',
})
const enabledSkillCount = computed(() => Array.from(enabledSkillPaths.value).length)

function syncEnabledSkillPaths(items: SkillRecord[]) {
  enabledSkillPaths.value = new Set(
    items
      .map((item) => item.rootPath || '')
      .filter((path, idx, arr) => Boolean(path) && arr.indexOf(path) === idx)
      .filter((path) => {
        const matched = items.find((item) => (item.rootPath || '') === path)
        return Boolean(matched?.enabled)
      }),
  )
}

function setSkillPathEnabled(path: string, enabled: boolean) {
  const normalized = path.trim()
  if (!normalized) return
  const next = new Set(enabledSkillPaths.value)
  if (enabled) {
    next.add(normalized)
  } else {
    next.delete(normalized)
  }
  enabledSkillPaths.value = next
}

async function loadHealth() {
  try {
    const r = await fetch('/health', { method: 'GET' })
    healthStatus.value = r.ok ? t('settings.healthOk') : `HTTP ${r.status}`
    if (!r.ok) healthError.value = await r.text()
  } catch (e) {
    healthStatus.value = t('settings.healthFailed')
    healthError.value = e instanceof Error ? e.message : String(e)
  }
}

async function loadPermissions() {
  permissionsLoading.value = true
  error.value = ''
  try {
    const data = await forebrainApi.permissionsRules()
    permissionsMode.value = data.mode
    modeForm.value.mode = data.mode || 'on-request'
    permissionRules.value = data.rules
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    permissionsLoading.value = false
  }
}

async function loadSkillProjects() {
  try {
    skillProjects.value = await forebrainApi.projectsList({ archived: false, limit: 200 })
  } catch {
    // The project list is a scope convenience: without it the page keeps
    // operating on the gateway's own launch project, which is what it always
    // did.
    skillProjects.value = []
  }
}

async function loadSkillsOverview() {
  skillsLoading.value = true
  error.value = ''
  try {
    const projectId = skillProjectId.value.trim()
    const data = projectId
      ? await forebrainApi.projectSkillsOverview(projectId)
      : await forebrainApi.skillsOverview()
    skills.value = data.installed
    syncEnabledSkillPaths(data.installed)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    skillsLoading.value = false
  }
}

async function saveSkillToggleState() {
  skillsSaving.value = true
  error.value = ''
  notice.value = ''
  try {
    const enabledPaths = Array.from(enabledSkillPaths.value)
    const projectId = skillProjectId.value.trim()
    const res = projectId
      ? await forebrainApi.projectSkillsToggle(projectId, enabledPaths)
      : await forebrainApi.skillsToggle(enabledPaths)
    skills.value = res.skills
    syncEnabledSkillPaths(res.skills)
    notice.value = t('settings.skillToggleUpdated', { count: enabledPaths.length })
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    skillsSaving.value = false
  }
}

async function inspectSkill(name: string) {
  inspectorLoading.value = true
  inspectorTitle.value = `Skill: ${name}`
  inspectorText.value = ''
  error.value = ''
  try {
    const projectId = skillProjectId.value.trim()
    const res = projectId
      ? await forebrainApi.projectSkillInspect(projectId, name)
      : await forebrainApi.skillInspect(name)
    const meta = [
      `name: ${res.skill.name}`,
      `source: ${res.skill.source}`,
      `trust: ${res.skill.trust}`,
      `path: ${res.skill.path}`,
      res.skill.allowedTools ? `allowed_tools: ${res.skill.allowedTools}` : '',
      res.skill.description ? `description: ${res.skill.description}` : '',
      '',
    ].filter(Boolean).join('\n')
    inspectorText.value = `${meta}\n${res.content || ''}`.trim()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    inspectorLoading.value = false
  }
}

async function loadSkillIntoEditor() {
  const name = editorForm.value.name.trim()
  if (!name) return
  editing.value = true
  error.value = ''
  try {
    const projectId = skillProjectId.value.trim()
    const res = projectId
      ? await forebrainApi.projectSkillInspect(projectId, name)
      : await forebrainApi.skillInspect(name)
    editorForm.value.content = res.content || ''
    notice.value = t('settings.skillLoaded', { name })
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    editing.value = false
  }
}

async function createSkill() {
  const name = editorForm.value.name.trim()
  const content = editorForm.value.content
  if (!name || !content.trim()) {
    error.value = t('settings.skillNameContentRequired')
    return
  }
  editing.value = true
  error.value = ''
  notice.value = ''
  try {
    const projectId = skillProjectId.value.trim()
    if (projectId) {
      await forebrainApi.projectSkillCreate(projectId, { name, content })
    } else {
      await forebrainApi.skillCreate({ name, content })
    }
    notice.value = t('settings.skillCreated', { name })
    await loadSkillsOverview()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    editing.value = false
  }
}

async function updateSkill() {
  const name = editorForm.value.name.trim()
  const content = editorForm.value.content
  if (!name || !content.trim()) {
    error.value = t('settings.skillNameContentRequired')
    return
  }
  editing.value = true
  error.value = ''
  notice.value = ''
  try {
    const projectId = skillProjectId.value.trim()
    if (projectId) {
      await forebrainApi.projectSkillUpdate(projectId, name, { content })
    } else {
      await forebrainApi.skillUpdate(name, { content })
    }
    notice.value = t('settings.skillUpdated', { name })
    await loadSkillsOverview()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    editing.value = false
  }
}

function resetSkillInstallEvents(taskId: string) {
  lastSkillInstallTaskId.value = taskId
  skillInstallEvents.value = []
}

function upsertSkillInstallEvent(payload: {
  taskId: string
  event?: string
  message?: string
  progress?: number
  phase?: string
  phaseLabel?: string
  installedNames?: string[]
  createdAt?: string
  updatedAt?: string
}) {
  const taskId = String(payload.taskId ?? '').trim()
  if (!taskId) return
  if (lastSkillInstallTaskId.value && taskId !== lastSkillInstallTaskId.value) return
  const phase = String(payload.phase ?? '').trim()
  const phaseLabel = String(payload.phaseLabel ?? '').trim()
  const progressRaw = Number(payload.progress ?? 0)
  const progress = Number.isFinite(progressRaw) ? Math.max(0, Math.min(progressRaw, 1)) : 0
  const installedNames = Array.isArray(payload.installedNames)
    ? payload.installedNames.map((item) => String(item)).filter(Boolean)
    : []
  const message = String(payload.message ?? '').trim()
  const createdAtMs = (() => {
    const raw = String(payload.updatedAt ?? payload.createdAt ?? '').trim()
    if (!raw) return Date.now()
    const ts = Date.parse(raw)
    return Number.isFinite(ts) ? ts : Date.now()
  })()
  const record = {
    taskId,
    phase,
    phaseLabel,
    message,
    progress,
    progressText: `${Math.round(progress * 100)}%`,
    progressBarWidth: `${Math.round(progress * 100)}%`,
    installedNames,
    createdAt: createdAtMs,
  }
  const next = skillInstallEvents.value.filter((item) => !(item.taskId === taskId && item.phase === phase))
  next.push(record)
  next.sort((a, b) => a.createdAt - b.createdAt)
  skillInstallEvents.value = next
}

function connectSkillLifecycleSocket() {
  skillLifecycleSocket.start()
}

const skillLifecycleSocket = createForebrainGatewaySessionSocket({
  getSessionId: () => skillLifecycleSessionId.value,
  setSessionId: (sessionId) => {
    skillLifecycleSessionId.value = sessionId
  },
  onMessage: (rawMsg) => {
    const detail = parseForebrainSkillLifecycleNotification(rawMsg)
    if (!detail) return
    upsertSkillInstallEvent({
      taskId: detail.taskId,
      event: detail.event,
      message: detail.message,
      progress: detail.progress,
      phase: detail.phase,
      phaseLabel: detail.phaseLabel,
      installedNames: detail.installedNames,
      createdAt: detail.createdAt,
      updatedAt: detail.updatedAt,
    })
    if (detail.event === 'done' || detail.event === 'failed') {
      void loadSkillsOverview()
    }
  },
})

async function installSkillPackage() {
  const sourceRef = installForm.value.sourceRef.trim()
  if (!sourceRef) {
    error.value = t('settings.sourceRefRequired')
    return
  }
  installing.value = true
  error.value = ''
  notice.value = ''
  try {
    connectSkillLifecycleSocket()
    const body = {
      sourceRef,
      dest: installForm.value.dest,
      skill: installForm.value.skill.trim() || undefined,
      name: installForm.value.name.trim() || undefined,
      ref: installForm.value.ref.trim() || undefined,
      sessionId: skillLifecycleSessionId.value,
    }
    const projectId = skillProjectId.value.trim()
    const res = projectId
      ? await forebrainApi.projectSkillInstall(projectId, body)
      : await forebrainApi.skillInstall(body)
    resetSkillInstallEvents(res.taskId)
    const installedCount = res.installed.count || res.installed.installed?.length || 1
    notice.value = t('settings.installSubmitted', { taskId: res.taskId, count: installedCount })
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    installing.value = false
  }
}

async function setPermissionMode() {
  error.value = ''
  notice.value = ''
  try {
    await forebrainApi.permissionsUpdate({
      type: 'setMode',
      destination: modeForm.value.destination,
      mode: modeForm.value.mode,
    })
    notice.value = t('settings.permissionModeSet', { mode: modeForm.value.mode, destination: modeForm.value.destination })
    await loadPermissions()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function addPermissionRule() {
  const toolName = ruleForm.value.toolName.trim()
  if (!toolName) return
  error.value = ''
  notice.value = ''
  try {
    await forebrainApi.permissionsUpdate({
      type: 'addRules',
      destination: ruleForm.value.destination,
      behavior: ruleForm.value.behavior,
      rules: [{ toolName, ruleContent: ruleForm.value.ruleContent.trim() }],
    })
    notice.value = t('settings.ruleAdded', { behavior: ruleForm.value.behavior, destination: ruleForm.value.destination })
    ruleForm.value.ruleContent = ''
    await loadPermissions()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

async function evaluatePermission() {
  const toolName = evalForm.value.toolName.trim()
  if (!toolName) return
  error.value = ''
  evalResult.value = ''
  try {
    const res = await forebrainApi.permissionsEvaluate({ toolName, input: evalForm.value.input })
    evalResult.value = JSON.stringify(res, null, 2)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

onMounted(() => {
  loadHealth()
  loadPermissions()
  loadSkillProjects()
  loadSkillsOverview()
  connectSkillLifecycleSocket()
})

onUnmounted(() => {
  skillLifecycleSocket.stop()
})
</script>
