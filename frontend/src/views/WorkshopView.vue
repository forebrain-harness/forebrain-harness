<template>
  <div class="chat-shell flex h-full min-h-0 w-full flex-1 overflow-hidden">
    <div class="relative flex min-h-0 min-w-0 flex-1 flex-row overflow-hidden">
      <main class="mx-auto flex h-full min-h-0 w-full max-w-[880px] flex-1 flex-col overflow-hidden px-3 sm:px-5">
        <!-- Fresh task: the greeting and the composer, no thread yet. -->
        <div v-if="isFresh" class="flex flex-1 flex-col items-center justify-center gap-4">
          <h2 class="text-center text-[17px] font-medium text-[var(--forebrain-text)]" data-testid="workshop-greeting">
            {{ greeting }}
          </h2>
        </div>
        <Conversation
          v-else
          class="chat-scrollbar relative h-full overflow-y-scroll overscroll-contain"
          :aria-label="t('chat.threadAria')"
        >
          <ConversationContent>
            <div v-if="!stream.messages.value.length && !stream.isStreaming.value" class="py-10 text-center text-[13px] text-[var(--forebrain-muted-text)]">
              {{ t('workshop.emptyConversation') }}
            </div>
            <div v-for="message in stream.messages.value" :key="message.id" class="mb-3" :data-workshop-message="message.role">
              <div
                v-if="message.role === 'assistant' && hasVisibleBlocks(message.blocks)"
                class="max-w-[85%] rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-2.5 text-[13px] leading-relaxed text-[var(--forebrain-text)]"
              >
                <template v-for="(block, blockIdx) in message.blocks" :key="`${message.id}-block-${blockIdx}`">
                  <ThinkingCard v-if="block.kind === 'thinking'" :block="block" />
                  <template v-else-if="block.kind === 'assistant'">
                    <MessageResponse :content="candidateText(block.text)" />
                    <CandidateCards
                      v-if="candidateList(block.text).length"
                      :candidates="candidateList(block.text)"
                      :installed="installedNames"
                      :busy="installingSkill"
                      @install="installCandidate"
                    />
                  </template>
                  <ToolCallCard v-else-if="block.kind === 'tool'" class="my-2" :step="block.step" :scope="workbenchScope" :default-open="false" />
                  <ApprovalCard v-else-if="block.kind === 'approval'" class="my-2" :block="block" />
                  <RunErrorBlock v-else-if="block.kind === 'error'" class="my-2" :text="block.text" :detail="block.detail" />
                </template>
              </div>
              <div
                v-else-if="message.role !== 'assistant' || String(message.content ?? '').trim()"
                class="inline-block max-w-[85%] rounded-lg px-4 py-2.5 text-[13px] leading-relaxed whitespace-pre-wrap"
                :class="message.role === 'user'
                  ? 'bg-[var(--forebrain-brand-1)] text-[var(--forebrain-on-brand)]'
                  : 'border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] text-[var(--forebrain-text)]'"
              >{{ message.content }}</div>
              <RunWorkedLine class="max-w-[85%]" :message="message" />
            </div>
            <div v-for="name in installedNames" :key="name" class="sys-line text-[12px] text-[var(--forebrain-text-2)]" data-testid="workshop-installed-line">
              {{ t('workshop.installedLine', { name }) }}
            </div>
            <ApprovalCard
              :session-id="activeSessionId || null"
              :version="stream.pendingActionsVersion.value"
              :context-used-percent="contextUsedPercent"
            />
            <p v-if="stream.error.value" class="text-[12px] text-[var(--forebrain-danger)]" data-testid="workshop-error">{{ stream.error.value }}</p>
          </ConversationContent>
          <ConversationScrollButton :label="t('chat.scrollToLatest')" />
        </Conversation>

        <!-- The chat page's composer, reused. -->
        <div class="z-10 shrink-0 border-t border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-4 sm:px-5">
          <p v-if="notice" class="mb-2 rounded-lg bg-[var(--forebrain-input-bg)] px-3 py-2 text-[12px] text-[var(--forebrain-danger)]" data-testid="workshop-notice">{{ notice }}</p>
          <PromptInputProvider :max-files="4" accept="image/*,application/pdf" @submit="handleSubmit">
            <!-- Relative, so the composer's "+" sheet hangs off the input area
                 rather than off its own 28px button. -->
            <div class="relative w-full" data-testid="workshop-composer">
            <PromptInput multiple global-drop class="w-full">
              <PromptInputBody>
                <ForebrainPromptTextarea
                  ref="promptRef"
                  :placeholder="inputPlaceholder"
                  :during-run="stream.isStreaming.value"
                  :disabled="composerLocked"
                  :scope="workbenchScope"
                  @typed="() => (notice = '')"
                />
              </PromptInputBody>
              <PromptInputFooter>
                <div class="flex min-w-0 flex-1 items-center gap-1">
                  <ComposerPlusMenu :disabled="composerLocked" @insert="insertPlusCommand" />
                  <ModeChip :mode="stream.mode.value" :disabled="stream.isStreaming.value || composerLocked" @select="selectMode" />
                  <ApprovalPresetPicker :session-id="activeSessionId || null" />
                </div>
                <div class="flex items-center gap-1">
                  <ContextRing v-if="contextUsedPercent != null" :percent="contextUsedPercent" @open="workbench.openOn('context')" />
                  <ModelChip
                    v-if="sessionModelData"
                    :session-id="activeSessionId"
                    :selection="sessionModelData"
                    :options="sessionModelData.options"
                    :disabled="composerLocked"
                    @saved="loadSessionModel(activeSessionId)"
                  />
                  <EffortChip
                    v-if="sessionModelData && sessionEfforts.length"
                    :effort="sessionModelData.effort"
                    :efforts="sessionEfforts"
                    :disabled="composerLocked"
                    @select="selectEffort"
                  />
                  <PromptInputSubmit :status="submitStatus" :disabled="composerLocked" />
                </div>
              </PromptInputFooter>
            </PromptInput>
            </div>
            <div v-if="isFresh && starters.length" class="mt-2 flex flex-wrap gap-2" data-testid="workshop-starters">
              <button
                v-for="starter in starters"
                :key="starter.label"
                type="button"
                class="rounded-lg border border-[var(--forebrain-divider)] px-2.5 py-1 text-[12px] text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-input-hover-bg)]"
                :data-testid="`workshop-starter-${starter.label}`"
                @click="fillComposer(starter.text)"
              >
                {{ t(starter.label) }}
              </button>
            </div>
          </PromptInputProvider>
        </div>
      </main>

      <ChatWorkbench
        v-if="workbench.open.value"
        :records="runningRoster"
        :loading="rosterLoading"
        :error="rosterError"
        :overlay="workbenchOverlay"
        :scope="workbenchScope"
        :extra-tabs="extraTabs"
        :requested-tab="workbench.requested.value"
        @requested-tab-shown="workbench.consumed()"
        @close="workbench.close()"
      >
        <template #changes>
          <ChangesTab ref="changesRef" :skill-name="activeSkill" :project-id="projectId" :editable="editable" @compare="openCompare" />
        </template>
        <template #cases>
          <CasesTab ref="casesRef" :skill-name="activeSkill" :project-id="projectId" :editable="editable" />
        </template>
        <template #evals>
          <EvalsTab
            ref="evalsRef"
            :skill-name="activeSkill"
            :project-id="projectId"
            :running="evalRunning"
            :done-count="0"
            @send="sendDirect"
            @fill="fillComposer"
          />
        </template>
        <template #trigger>
          <TriggerTab ref="triggerRef" :skill-name="activeSkill" :project-id="projectId" :editable="editable" @send="sendDirect" @fill="fillComposer" />
        </template>
        <template #versions>
          <VersionsTab
            ref="versionsRef"
            :skill-name="activeSkill"
            :project-id="projectId"
            :editable="editable"
            @compare="openCompare"
            @changed="reloadAfterChange"
          />
        </template>
      </ChatWorkbench>
    </div>

    <PublishDialog
      :open="publishOpen"
      :skill-name="activeSkill"
      :project-id="projectId"
      :current-version="activeVersion"
      @published="onPublished"
      @cancel="publishOpen = false"
    />
    <CompareDialog
      :open="compareOpen"
      :skill-name="activeSkill"
      :project-id="projectId"
      :from="compareFrom"
      :to="compareTo"
      @cancel="compareOpen = false"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * The skill workshop — the chat page plus the skill tabs. It reuses the chat
 * page's message blocks, its composer (the whole PromptInput stack and its
 * chips) and its workbench; the workbench gains four skill tabs (test cases,
 * evaluation, trigger description, versions) and the changes tab shows the
 * draft against the current version. The conversation is a workshop session
 * (source=workshop); the skill it works on is named on the route.
 */
import { computed, h, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  Conversation,
  ConversationContent,
  ConversationScrollButton,
} from '@repo/elements/conversation'
import { MessageResponse } from '@repo/elements/message'
import {
  PromptInput,
  PromptInputBody,
  PromptInputFooter,
  PromptInputProvider,
  PromptInputSubmit,
} from '@repo/elements/prompt-input'
import ForebrainPromptTextarea from '@/components/ForebrainPromptTextarea.vue'
import ChatWorkbench from '@/components/chat/ChatWorkbench.vue'
import ApprovalCard from '@/components/chat/ApprovalCard.vue'
import ApprovalPresetPicker from '@/components/chat/ApprovalPresetPicker.vue'
import ComposerPlusMenu from '@/components/chat/ComposerPlusMenu.vue'
import ContextRing from '@/components/chat/ContextRing.vue'
import EffortChip from '@/components/chat/EffortChip.vue'
import ModeChip from '@/components/chat/ModeChip.vue'
import ModelChip from '@/components/chat/ModelChip.vue'
import RunErrorBlock from '@/components/chat/RunErrorBlock.vue'
import RunWorkedLine from '@/components/chat/RunWorkedLine.vue'
import ToolCallCard from '@/components/chat/ToolCallCard.vue'
import ThinkingCard from '@/components/chat/ThinkingCard.vue'
import WorkshopTopbar from '@/components/workshop/WorkshopTopbar.vue'
import CasesTab from '@/components/workshop/CasesTab.vue'
import ChangesTab from '@/components/workshop/ChangesTab.vue'
import EvalsTab from '@/components/workshop/EvalsTab.vue'
import TriggerTab from '@/components/workshop/TriggerTab.vue'
import VersionsTab from '@/components/workshop/VersionsTab.vue'
import CandidateCards from '@/components/workshop/CandidateCards.vue'
import PublishDialog from '@/components/workshop/PublishDialog.vue'
import CompareDialog from '@/components/workshop/CompareDialog.vue'
import { useAgentRoster } from '@/composables/useAgentRoster'
import { useChatStream, hasVisibleBlocks } from '@/composables/useChatStream'
import { useTopbar } from '@/composables/useTopbar'
import { useWorkbench } from '@/composables/useWorkbench'
import forebrainApi, {
  getErrorMessage,
  type ProjectRecord,
  type SessionModelResponse,
  type SkillCandidate,
  type SkillRecord,
  type WorkspaceScope,
} from '@/lib/api'
import { parseSkillCandidates } from '@/lib/skillCandidates'
import { parseWorkshopTarget } from '@/lib/workshopTarget'
import { useI18n, type I18nKey } from '@/locales'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const stream = useChatStream()
const workbench = useWorkbench()
const { set: setTopbar } = useTopbar()
const promptRef = ref<InstanceType<typeof ForebrainPromptTextarea> | null>(null)
const { records: rosterRecords, loading: rosterLoading, error: rosterError, loadRoster } = useAgentRoster()

const target = computed(() => parseWorkshopTarget(route.query as Record<string, unknown>))
const scope = computed(() => target.value.scope)
const mode = computed(() => target.value.mode)
const projectId = computed(() => target.value.projectId)

// ---- Skill facts ----
const skills = ref<SkillRecord[]>([])
const activeSkill = ref('')
const activeVersion = ref(0)
const activeState = ref('')
const activeHasUpdate = ref(false)
const activeEditable = ref(false)

const currentRecord = computed(() => {
  if (!activeSkill.value) return null
  const rows = skills.value.filter((row) => row.name === activeSkill.value)
  if (scope.value === 'project') return rows.find((row) => row.origin === 'project') ?? rows[0] ?? null
  return rows.find((row) => row.origin === 'agent') ?? rows[0] ?? null
})

const editable = computed(() => currentRecord.value?.editable ?? (scope.value === 'agent' && currentRecord.value?.origin === 'agent'))
const hasSkill = computed(() => !!activeSkill.value)
const publishVersion = computed(() => String(activeVersion.value + (activeState.value === 'published' ? 0 : 1)))
const canPublish = computed(() => hasSkill.value && activeState.value !== 'published')

async function loadSkills() {
  try {
    const data = scope.value === 'project' && projectId.value
      ? await forebrainApi.projectSkillsOverview(projectId.value)
      : await forebrainApi.skillsOverview()
    skills.value = data.installed
  } catch {
    skills.value = []
  }
}

function syncActiveSkill() {
  const record = currentRecord.value
  activeVersion.value = record?.version ?? 0
  activeState.value = record?.state ?? ''
  activeHasUpdate.value = !!record?.update
  activeEditable.value = record?.editable ?? false
}

// ---- Session ----
const activeSessionId = ref('')
const notice = ref('')
const projects = ref<ProjectRecord[]>([])

const projectName = computed(() => projects.value.find((p) => p.id === projectId.value)?.name ?? '')
const isFresh = computed(() => !activeSessionId.value && !stream.messages.value.length && !stream.isStreaming.value)
const composerLocked = computed(() => stream.isStreaming.value)

const workbenchScope = computed<WorkspaceScope>(() =>
  activeSessionId.value ? { sessionId: activeSessionId.value } : projectId.value ? { projectId: projectId.value } : {},
)
const runningRoster = computed(() => rosterRecords.value.filter((row) => String(row.status ?? '').trim().toLowerCase() === 'running'))
const workbenchOverlay = ref(false)
let workbenchMq: MediaQueryList | null = null
function syncWorkbenchOverlay() {
  workbenchOverlay.value = workbenchMq?.matches ?? false
}

const evalRunning = ref(false)
const submitStatus = computed(() => (stream.isStreaming.value ? 'streaming' : 'ready'))

const contextUsedPercent = computed(() => {
  for (let i = stream.messages.value.length - 1; i >= 0; i--) {
    const left = stream.messages.value[i]?.tokenBudget?.percentLeft
    if (typeof left === 'number' && Number.isFinite(left)) return Math.max(0, Math.min(100, 100 - left))
  }
  return null
})

const sessionModelData = ref<SessionModelResponse | null>(null)
let modelGeneration = 0
async function loadSessionModel(sid: string | null) {
  const generation = ++modelGeneration
  if (!sid) {
    sessionModelData.value = null
    return
  }
  try {
    const res = await forebrainApi.sessionModel(sid)
    if (generation === modelGeneration) sessionModelData.value = res
  } catch {
    if (generation === modelGeneration) sessionModelData.value = null
  }
}
const sessionEfforts = computed(() => {
  const data = sessionModelData.value
  if (!data) return []
  return data.options.find((option) => option.provider === data.provider && option.model === data.model)?.efforts ?? []
})

async function selectEffort(effort: string) {
  const sid = activeSessionId.value
  const data = sessionModelData.value
  if (!sid || !data) return
  try {
    const saved = await forebrainApi.saveSessionModel(sid, { provider: data.provider, model: data.model, effort })
    sessionModelData.value = { ...data, provider: saved.provider, model: saved.model, effort: saved.effort }
  } catch {
    /* the chip keeps its state; the next load reconciles */
  }
}

// ---- Workbench tabs ----
const extraTabs = computed<{ key: string; label: I18nKey; disabled?: boolean }[]>(() => [
  { key: 'cases', label: 'workshop.tabCases', disabled: !hasSkill.value },
  { key: 'evals', label: 'workshop.tabEvals', disabled: !hasSkill.value },
  { key: 'trigger', label: 'workshop.tabTrigger', disabled: !hasSkill.value },
  { key: 'versions', label: 'workshop.tabVersions', disabled: !hasSkill.value },
])

const changesRef = ref<InstanceType<typeof ChangesTab> | null>(null)
const casesRef = ref<InstanceType<typeof CasesTab> | null>(null)
const evalsRef = ref<InstanceType<typeof EvalsTab> | null>(null)
const triggerRef = ref<InstanceType<typeof TriggerTab> | null>(null)
const versionsRef = ref<InstanceType<typeof VersionsTab> | null>(null)

function reloadAfterChange() {
  void loadSkills()
  void changesRef.value?.reload?.()
  void evalsRef.value?.reload?.()
  void triggerRef.value?.reload?.()
}

// ---- Compare / publish ----
const compareOpen = ref(false)
const compareFrom = ref('')
const compareTo = ref('')
function openCompare(payload: { from: string; to: string }) {
  compareFrom.value = payload.from
  compareTo.value = payload.to
  compareOpen.value = true
}

const publishOpen = ref(false)
async function onPublished() {
  publishOpen.value = false
  await loadSkills()
  await versionsRef.value?.reload?.()
  await changesRef.value?.reload?.()
}

// ---- Greeting / starters ----
const greeting = computed(() => {
  if (mode.value === 'discover') return t('workshop.greetDiscover')
  if (activeSkill.value) return t('workshop.greetImprove', { name: activeSkill.value })
  return t('workshop.greetCreate')
})

const starters = computed<{ label: I18nKey; text: string }[]>(() => {
  if (mode.value === 'discover') return []
  if (activeSkill.value) {
    return [
      { label: 'workshop.starterEval', text: t('workshop.starterEvalText') },
      { label: 'workshop.starterTrigger', text: t('workshop.starterTriggerText') },
      { label: 'workshop.starterCases', text: t('workshop.starterCasesText') },
    ]
  }
  return [
    { label: 'workshop.starterSteps', text: t('workshop.starterStepsText') },
    { label: 'workshop.starterDoc', text: t('workshop.starterDocText') },
    { label: 'workshop.starterCopy', text: t('workshop.starterCopyText') },
  ]
})

const inputPlaceholder = computed(() => {
  if (mode.value === 'discover') return t('workshop.composerDiscover')
  if (activeSkill.value) return t('workshop.composerImprove', { name: activeSkill.value })
  return t('workshop.composerPlaceholder')
})

// ---- Composer actions ----
function insertPlusCommand(text: string) {
  promptRef.value?.restoreSubmission({ text, attachments: [], mentionImages: [] })
}

function fillComposer(text: string) {
  promptRef.value?.restoreSubmission({ text, attachments: [], mentionImages: [] })
}

function selectMode(next: string) {
  if (next === stream.mode.value) return
  void sendDirect(next === 'plan' ? '/plan' : '/agent')
}

async function sendDirect(text: string) {
  const value = String(text ?? '').trim()
  if (!value) return
  const sid = await ensureSession()
  if (!sid) return
  await stream.send(value, { sessionId: sid })
}

async function sendFirst(text: string) {
  const overview = await forebrainApi.skillsOverview().catch(() => null)
  const workshop = overview?.installed.find((item) => item.name === 'skill-workshop' && item.enabled)
  if (!workshop?.rootPath) {
    notice.value = t('workshop.skillUnavailable')
    promptRef.value?.restoreSubmission({ text, attachments: [], mentionImages: [] })
    return false
  }
  await stream.send(text, { sessionId: activeSessionId.value, skillName: workshop.name, skillPath: workshop.rootPath })
  return true
}

async function handleSubmit(message: { text: string; files?: { url?: string; filename?: string; mediaType?: string }[] }) {
  const text = (message.text ?? '').trim()
  if (!text && !(message.files ?? []).length) return
  if (!text) return
  const wasFresh = !activeSessionId.value
  const sid = await ensureSession()
  if (!sid) return
  if (wasFresh) {
    const ok = await sendFirst(text)
    if (!ok) return
  } else {
    await stream.send(text, { sessionId: sid })
  }
}

async function ensureSession(): Promise<string> {
  if (activeSessionId.value) return activeSessionId.value
  try {
    const created = await forebrainApi.chatSessionCreate({
      title: sessionTitle(),
      source: 'workshop',
      ...(scope.value === 'project' && projectId.value ? { projectId: projectId.value } : {}),
    })
    activeSessionId.value = created.id
    await stream.switchToSession(created.id)
    void loadRoster()
    return created.id
  } catch (e: unknown) {
    notice.value = getErrorMessage(e)
    return ''
  }
}

function sessionTitle(): string {
  if (activeSkill.value) return t('workshop.taskTitleImprove', { name: activeSkill.value })
  return t('workshop.newSkillTitle')
}

// ---- Discovery: install a candidate from the model's reply ----
const installedNames = ref<string[]>([])
const installingSkill = ref('')

function candidateList(text: string): SkillCandidate[] {
  return parseSkillCandidates(text).candidates
}
function candidateText(text: string): string {
  return parseSkillCandidates(text).text
}

async function installCandidate(candidate: SkillCandidate) {
  installingSkill.value = candidate.name
  notice.value = ''
  try {
    const dest = scope.value === 'project' ? 'project' : 'workspace'
    if (scope.value === 'project' && projectId.value) {
      await forebrainApi.projectSkillInstall(projectId.value, { sourceRef: candidate.source, dest, skill: candidate.name, name: candidate.name })
    } else {
      await forebrainApi.skillInstall({ sourceRef: candidate.source, dest, skill: candidate.name, name: candidate.name })
    }
    installedNames.value = [...installedNames.value, candidate.name]
    // The workshop switches onto the installed skill and becomes an improve
    // task; its four skill tabs come alive.
    await loadSkills()
    activeSkill.value = candidate.name
    syncActiveSkill()
  } catch (e: unknown) {
    notice.value = getErrorMessage(e)
  } finally {
    installingSkill.value = ''
  }
}

async function startNewSession() {
  activeSessionId.value = ''
  stream.resetForNewChat()
  sessionModelData.value = null
  notice.value = ''
}

// ---- Top bar ----
const runningCount = computed(() => runningRoster.value.length)
const crumbLabel = computed(() => {
  if (mode.value === 'discover') return t('workshop.modeDiscover')
  if (mode.value === 'create' && !hasSkill.value) return t('workshop.modeCreate')
  return activeSkill.value || t('workshop.modeCreate')
})

function backToSource() {
  if (scope.value === 'project' && projectId.value) {
    void router.push({ path: `/projects/${encodeURIComponent(projectId.value)}/skills` })
    return
  }
  void router.push({ name: 'skills' })
}

const topbarLeading = () => [
  h(WorkshopTopbar, {
    part: 'leading',
    label: crumbLabel.value,
    hasSkill: hasSkill.value,
    version: activeVersion.value,
    state: activeState.value,
    scope: scope.value,
    projectName: projectName.value,
    workbenchOpen: workbench.open.value,
    publishVersion: publishVersion.value,
    canPublish: canPublish.value,
    onBack: backToSource,
    onOpenProject: () => {
      if (projectId.value) void router.push({ path: `/projects/${encodeURIComponent(projectId.value)}/overview` })
    },
  }),
]
const topbarTrailing = () => [
  h(WorkshopTopbar, {
    part: 'trailing',
    label: crumbLabel.value,
    hasSkill: hasSkill.value,
    version: activeVersion.value,
    state: activeState.value,
    scope: scope.value,
    sessionId: activeSessionId.value,
    workbenchOpen: workbench.open.value,
    runningCount: runningCount.value,
    publishVersion: publishVersion.value,
    canPublish: canPublish.value,
    onNewSession: startNewSession,
    onToggleWorkbench: () => workbench.toggle(),
    onPublish: () => (publishOpen.value = true),
  }),
]

// ---- Load ----
async function findRecentSession(name: string): Promise<string> {
  try {
    const data = await forebrainApi.chatSessions('workshop')
    const match = data.records.find((rec) => titleNamesSkill(rec.title ?? '', name))
    return match?.id ?? ''
  } catch {
    return ''
  }
}

function titleNamesSkill(title: string, name: string): boolean {
  const trimmed = title.trim()
  return trimmed === t('workshop.taskTitle', { name })
    || trimmed === t('workshop.taskTitleImprove', { name })
    || trimmed === `Workshop: ${name}`
    || trimmed === `Workshop improve: ${name}`
    || trimmed.endsWith(`：${name}`)
    || trimmed.endsWith(`: ${name}`)
}

async function loadProjects() {
  try {
    projects.value = await forebrainApi.projectsList({ sort: 'updated' })
  } catch {
    projects.value = []
  }
}

watch(activeSessionId, (sid) => void loadSessionModel(sid || null), { immediate: true })

async function bootstrap() {
  const parsed = parseWorkshopTarget(route.query as Record<string, unknown>)
  await loadSkills()
  activeSkill.value = parsed.skill
  syncActiveSkill()
  if (scope.value === 'project' && projectId.value) void loadProjects()
  if (parsed.skill && parsed.mode === 'improve') {
    const recent = await findRecentSession(parsed.skill)
    if (recent) {
      activeSessionId.value = recent
      await stream.switchToSession(recent)
    }
  }
}

watch(() => route.fullPath, () => void bootstrap())

onMounted(() => {
  workbenchMq = window.matchMedia('(max-width: 1023px)')
  syncWorkbenchOverlay()
  workbenchMq.addEventListener('change', syncWorkbenchOverlay)
  setTopbar({ leading: topbarLeading, trailing: topbarTrailing })
  void loadRoster()
  void bootstrap()
})

onUnmounted(() => {
  workbenchMq?.removeEventListener('change', syncWorkbenchOverlay)
})
</script>
