<template>
  <div class="flex min-h-0 flex-1 flex-col overflow-hidden bg-[var(--forebrain-bg)]">
    <div class="flex items-center gap-2 border-b border-[var(--forebrain-divider)] px-4 py-3">
      <h1 class="text-xl font-medium text-[var(--forebrain-text)]">{{ t('workshop.title') }}</h1>
      <span class="scope-badge">{{ t('scope.agent') }}</span>
      <div class="ml-auto flex gap-2">
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="newTaskOpen = true" data-testid="workshop-new-task">
          {{ t('workshop.newTask') }}
        </button>
      </div>
    </div>
    <p class="border-b border-[var(--forebrain-divider)] px-4 py-2 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('workshop.description') }}</p>

    <div class="grid min-h-0 flex-1 grid-cols-[200px_minmax(0,1fr)_320px]">
      <!-- Task list -->
      <aside class="flex min-h-0 flex-col overflow-y-auto border-r border-[var(--forebrain-divider)] p-2" data-testid="workshop-tasks">
        <button
          v-for="task in tasks"
          :key="task.id"
          type="button"
          class="mb-1 rounded-xl px-3 py-2 text-left text-[12px]"
          :class="task.id === activeTaskId ? 'bg-[var(--forebrain-brand-soft)] text-[var(--forebrain-brand-1)]' : 'text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-input-hover-bg)]'"
          @click="openTask(task.id)"
        >
          <div class="truncate font-medium">{{ task.title || task.id }}</div>
          <div class="mt-0.5 text-[11px] text-[var(--forebrain-muted-text)]">{{ formatTaskTime(task.updateTime) }}</div>
        </button>
        <p v-if="listError" class="px-2 py-2 text-[12px] text-[var(--forebrain-danger)]">{{ listError }}</p>
        <p v-else-if="!tasks.length" class="px-2 py-6 text-center text-[12px] text-[var(--forebrain-muted-text)]">{{ t('workshop.noTasks') }}</p>
      </aside>

      <!-- Conversation -->
      <section class="flex min-h-0 min-w-0 flex-col">
        <div class="min-h-0 flex-1 overflow-y-auto px-4 py-3">
          <p v-if="!stream.messages.value.length" class="py-10 text-center text-[13px] text-[var(--forebrain-muted-text)]">{{ t('workshop.emptyConversation') }}</p>
          <div v-for="message in stream.messages.value" :key="message.id" class="mb-3" :data-workshop-message="message.role">
            <!-- A turn reads as the chat page reads it, live: what was said,
                 what was thought, the calls made and the gates passed, in
                 the order they happened — not just the final words. -->
            <div
              v-if="message.role === 'assistant' && message.blocks?.length"
              class="max-w-[85%] rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-2.5 text-[13px] leading-relaxed text-[var(--forebrain-text)]"
            >
              <template v-for="(block, blockIdx) in message.blocks" :key="`${message.id}-block-${blockIdx}`">
                <div
                  v-if="block.kind === 'thinking'"
                  class="mb-2 whitespace-pre-wrap break-words text-[12px] text-[var(--forebrain-muted-text)]"
                >{{ block.text }}</div>
                <MessageResponse v-else-if="block.kind === 'assistant'" :content="block.text" />
                <ToolCallCard v-else-if="block.kind === 'tool'" class="my-2" :step="block.step" :default-open="false" />
                <ApprovalCard v-else-if="block.kind === 'approval'" class="my-2" :block="block" />
                <RunErrorBlock v-else-if="block.kind === 'error'" class="my-2" :text="block.text" :detail="block.detail" />
              </template>
            </div>
            <div
              v-else-if="message.role !== 'assistant' || String(message.content ?? '').trim()"
              class="inline-block max-w-[85%] rounded-2xl px-4 py-2.5 text-[13px] leading-relaxed whitespace-pre-wrap"
              :class="message.role === 'user'
                ? 'bg-[var(--forebrain-brand-1)] text-[var(--forebrain-on-brand)]'
                : 'border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] text-[var(--forebrain-text)]'"
            >{{ message.content }}</div>
            <!-- Every run closes with its line, a run that said nothing too. -->
            <RunWorkedLine class="max-w-[85%]" :message="message" />
          </div>
          <!-- A gate the task's turn is parked on is decided here, with the
               same controls the chat page has. -->
          <PendingActionsPanel :session-id="activeTaskId || null" :version="stream.pendingActionsVersion.value" />
          <p v-if="stream.error.value" class="text-[12px] text-[var(--forebrain-danger)]" data-testid="workshop-error">{{ stream.error.value }}</p>
        </div>
        <form class="flex items-end gap-2 border-t border-[var(--forebrain-divider)] px-4 py-3" @submit.prevent="submit">
          <textarea
            v-model="draft"
            rows="2"
            class="min-w-0 flex-1 resize-none rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:ring-2 focus:ring-[var(--forebrain-ring-soft)]"
            :placeholder="t('workshop.composerPlaceholder')"
            data-testid="workshop-composer"
            @keydown.enter.exact.prevent="submit"
          />
          <button type="submit" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="stream.isStreaming.value || !draft.trim() || !activeTaskId" data-testid="workshop-send">
            {{ stream.isStreaming.value ? t('common.loading') : t('workshop.send') }}
          </button>
        </form>
      </section>

      <!-- Skill panel -->
      <aside class="min-h-0 overflow-hidden border-l border-[var(--forebrain-divider)] p-3">
        <WorkshopSkillPanel :skill-name="panelSkill" />
      </aside>
    </div>

    <!-- New-task dialog -->
    <div
      v-if="newTaskOpen"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/30 p-4"
      data-testid="workshop-new-dialog"
      @click.self="newTaskOpen = false"
    >
      <div class="w-full max-w-md rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-5 shadow-lg">
        <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('workshop.newTask') }}</div>
        <div class="mt-3 space-y-3">
          <label class="block">
            <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('workshop.taskKind') }}</span>
            <select v-model="taskKind" class="forebrain-field h-9" data-testid="workshop-task-kind">
              <option value="new">{{ t('workshop.taskNew') }}</option>
              <option value="improve">{{ t('workshop.taskImprove') }}</option>
            </select>
          </label>
          <label v-if="taskKind === 'new'" class="block">
            <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('workshop.skillNameLabel') }}</span>
            <input v-model="newName" type="text" class="forebrain-field h-9" data-testid="workshop-new-name" :placeholder="t('workshop.skillNamePlaceholder')" />
          </label>
          <label v-else class="block">
            <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('workshop.pickSkill') }}</span>
            <select v-model="improveName" class="forebrain-field h-9" data-testid="workshop-improve-name">
              <option value="">{{ t('workshop.pickSkillPlaceholder') }}</option>
              <option v-for="skill in editableSkills" :key="skill.name" :value="skill.name">{{ skill.name }}</option>
            </select>
          </label>
          <label class="block">
            <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('workshop.purposeLabel') }}</span>
            <textarea v-model="purpose" rows="2" class="forebrain-field w-full" data-testid="workshop-purpose" :placeholder="t('workshop.purposePlaceholder')" />
          </label>
          <label v-if="taskKind === 'new'" class="block">
            <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('workshop.targetLabel') }}</span>
            <select v-model="target" class="forebrain-field h-9" data-testid="workshop-target">
              <option value="agent">{{ t('skills.destAgent') }}</option>
              <option value="shared">{{ t('skills.destShared') }}</option>
            </select>
          </label>
        </div>
        <p v-if="dialogError" class="mt-3 text-[12px] text-[var(--forebrain-danger)]">{{ dialogError }}</p>
        <div class="mt-4 flex justify-end gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="newTaskOpen = false">{{ t('common.cancel') }}</button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" data-testid="workshop-create-task" @click="createTask">
            {{ creating ? t('common.loading') : t('workshop.start') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The skill workshop: a conversation with the skill-workshop skill on the
 * left, the skill's own files on the right. Tasks are sessions marked
 * source=workshop, which the chat drawer's list never includes — a workshop
 * task is tooling, not a conversation.
 */
import { computed, onMounted, ref } from 'vue'
import { MessageResponse } from '@repo/elements/message'
import ApprovalCard from '@/components/chat/ApprovalCard.vue'
import PendingActionsPanel from '@/components/chat/PendingActionsPanel.vue'
import RunWorkedLine from '@/components/chat/RunWorkedLine.vue'
import RunErrorBlock from '@/components/chat/RunErrorBlock.vue'
import ToolCallCard from '@/components/chat/ToolCallCard.vue'
import WorkshopSkillPanel from '@/components/workshop/WorkshopSkillPanel.vue'
import forebrainApi, { getErrorMessage, type SkillRecord } from '@/lib/api'
import { useChatStream } from '@/composables/useChatStream'
import { useI18n } from '@/locales'

const { t, locale } = useI18n()
const stream = useChatStream()

type TaskRow = { id: string; title: string | null; updateTime: string }

const tasks = ref<TaskRow[]>([])
const activeTaskId = ref('')
const draft = ref('')
const newTaskOpen = ref(false)
const taskKind = ref<'new' | 'improve'>('new')
const newName = ref('')
const improveName = ref('')
const purpose = ref('')
const target = ref<'agent' | 'shared'>('agent')
const creating = ref(false)
const dialogError = ref('')
const listError = ref('')
const editableSkills = ref<SkillRecord[]>([])

// The skill the panel shows: the one the active task was started for. A
// workshop task's title is "Workshop: <skill>" by construction, so the panel
// resolves the skill from it; the name remembered from this page's own
// create/open actions wins when present.
const panelByName = ref('')
const panelSkill = computed(() => {
  if (panelByName.value) return panelByName.value
  const task = tasks.value.find((item) => item.id === activeTaskId.value)
  const fromTitle = (task?.title ?? '').trim()
  if (!fromTitle) return ''
  return fromTitle.replace(/^(工坊改进：|工坊：|Workshop improve: |Workshop: )/, '').trim()
})

async function loadTasks() {
  try {
    const data = await forebrainApi.chatSessions('workshop')
    tasks.value = data.records
    listError.value = ''
  } catch (e: unknown) {
    tasks.value = []
    listError.value = getErrorMessage(e)
  }
}

// What a task may improve: the agent's own skills, which are edited in
// place, and the built-ins, which the workshop copies to a layer the user
// owns first. Inherited rows belong to the layer that manages them.
async function loadEditableSkills() {
  try {
    const data = await forebrainApi.skillsOverview()
    editableSkills.value = data.installed.filter((row) => row.origin === 'agent' || row.origin === 'builtin')
  } catch (e: unknown) {
    editableSkills.value = []
    listError.value = getErrorMessage(e)
  }
}

function formatTaskTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime())
    ? ''
    : date.toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}

async function openTask(id: string) {
  if (activeTaskId.value === id) return
  activeTaskId.value = id
  await stream.switchToSession(id)
  panelByName.value = ''
}

function firstMessage(kind: 'new' | 'improve', name: string, why: string, dest: string): string {
  if (kind === 'improve') {
    return t('workshop.firstMessageImprove', { name, why })
  }
  return t('workshop.firstMessageNew', { name, why, dest })
}

async function createTask() {
  dialogError.value = ''
  const name = taskKind.value === 'new' ? newName.value.trim() : improveName.value.trim()
  if (!name) {
    dialogError.value = t('workshop.nameRequired')
    return
  }
  creating.value = true
  try {
    // The task is a conversation with the skill-workshop skill; without it
    // in this agent's set there is nothing to start, and a task that silently
    // ran without it would be an ordinary chat in a workshop's clothes.
    const overview = await forebrainApi.skillsOverview()
    const workshop = overview.installed.find((item) => item.name === 'skill-workshop' && item.enabled)
    if (!workshop?.rootPath) {
      dialogError.value = t('workshop.skillUnavailable')
      return
    }
    const created = await forebrainApi.chatSessionCreate(
      taskKind.value === 'new' ? t('workshop.taskTitle', { name }) : t('workshop.taskTitleImprove', { name }),
      'workshop',
    )
    await loadTasks()
    newTaskOpen.value = false
    await openTask(created.id)
    panelByName.value = name
    // The workshop skill is activated for the first message — the same
    // handoff the terminal performs.
    await stream.send(firstMessage(taskKind.value, name, purpose.value.trim(), target.value), {
      sessionId: created.id,
      skillName: workshop.name,
      skillPath: workshop.rootPath,
    })
    newName.value = ''
    improveName.value = ''
    purpose.value = ''
  } catch (e: unknown) {
    dialogError.value = getErrorMessage(e)
  } finally {
    creating.value = false
  }
}

async function submit() {
  const text = draft.value.trim()
  if (!text || !activeTaskId.value || stream.isStreaming.value) return
  draft.value = ''
  await stream.send(text, { sessionId: activeTaskId.value })
}

onMounted(() => {
  void loadTasks()
  void loadEditableSkills()
})
</script>
