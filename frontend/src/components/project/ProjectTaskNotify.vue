<template>
  <!-- 任务通知 (plan 153 A12): only a batch project announces its task ends,
       and it announces them where the settings already are. Every write is the
       whole list, so adding and removing are the same call. -->
  <section class="forebrain-form-field" data-testid="project-task-notify" aria-labelledby="task-notify-title">
    <span id="task-notify-title">{{ t('projects.taskNotifyTitle') }}</span>
    <div class="grid gap-1.5" data-testid="task-notify-list">
      <div
        v-for="(target, index) in targets"
        :key="`${target.channel}:${target.target}`"
        class="flex items-center gap-2 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-2.5 py-2"
        :data-testid="`task-notify-row-${index}`"
      >
        <div class="min-w-0 flex-1">
          <b class="forebrain-cell-1 block text-[13px] font-medium text-[var(--forebrain-text)]">
            {{ channelName(target.channel) }} · {{ target.target }}
          </b>
          <span class="block text-[11.5px] text-[var(--forebrain-muted-text)]">{{ t('projects.taskNotifyRowHint') }}</span>
        </div>
        <button
          type="button"
          class="forebrain-icon-btn"
          v-tip:top="t('projects.taskNotifyDelete')"
          :aria-label="`${t('projects.taskNotifyDelete')} ${channelName(target.channel)} ${target.target}`"
          :disabled="saving"
          :data-testid="`task-notify-delete-${index}`"
          @click="remove(index)"
        >
          <Trash2 class="size-3.5" aria-hidden="true" />
        </button>
      </div>
      <div v-if="!targets.length" class="text-[12px] text-[var(--forebrain-muted-text)]" data-testid="task-notify-empty">
        {{ t('projects.taskNotifyEmpty') }}
      </div>
      <div>
        <button
          type="button"
          class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs"
          data-testid="task-notify-add"
          @click="openAdd"
        >
          <Plus class="size-3.5" aria-hidden="true" />{{ t('projects.taskNotifyAdd') }}
        </button>
      </div>
    </div>
    <span class="forebrain-list-note">{{ t('projects.taskNotifyHint') }}</span>
    <span v-if="notice" class="text-[12px] text-[var(--forebrain-text-2)]" data-testid="task-notify-notice">{{ notice }}</span>
    <span v-if="error" class="text-[12px] text-[var(--forebrain-danger)]" role="alert" data-testid="task-notify-error">{{ error }}</span>
  </section>

  <ForebrainModal :open="addOpen" size="sm" :title="t('projects.taskNotifyAddTitle')" @close="addOpen = false">
    <div class="grid gap-3">
      <div class="grid gap-1">
        <span class="text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.taskNotifyChannel') }}</span>
        <Select :model-value="channelChoice" @update:model-value="(value) => (channelChoice = String(value ?? ''))">
          <SelectTrigger class="forebrain-field h-9" :aria-label="t('projects.taskNotifyChannel')" data-testid="task-notify-channel">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem v-if="!enabledChannels.length" value="__none__" :disabled="true">
              {{ t('projects.taskNotifyNoChannels') }}
            </SelectItem>
            <SelectItem v-for="kind in enabledChannels" :key="kind" :value="kind">
              {{ channelName(kind) }}
            </SelectItem>
          </SelectContent>
        </Select>
        <span class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('projects.taskNotifyChannelHint') }}</span>
      </div>

      <label class="grid gap-1">
        <span class="text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.taskNotifyRecipient') }}</span>
        <Input
          v-model="recipient"
          class="forebrain-field h-[34px] w-full"
          :placeholder="t('projects.taskNotifyRecipientPlaceholder')"
          autocomplete="off"
          data-testid="task-notify-recipient"
        />
      </label>

      <!-- Who this project's channels have already spoken to: picking one is
           easier than typing an id nobody remembers. -->
      <div v-if="known.length" class="grid gap-1.5" data-testid="task-notify-known">
        <div
          v-for="row in known"
          :key="`${row.channel}:${row.target}`"
          class="flex items-center gap-2 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-2.5 py-1.5"
        >
          <div class="min-w-0 flex-1">
            <b class="forebrain-cell-1 block text-[12.5px] font-medium text-[var(--forebrain-text)]">{{ channelName(row.channel) }} · {{ row.target }}</b>
            <span v-if="row.label" class="forebrain-cell-1 block text-[11px] text-[var(--forebrain-muted-text)]">{{ row.label }}</span>
          </div>
          <Button
            type="button"
            class="forebrain-btn forebrain-btn-ghost h-auto text-xs"
            :data-testid="`task-notify-pick-${row.channel}`"
            @click="pick(row)"
          >{{ t('projects.taskNotifyPick') }}</Button>
        </div>
      </div>

      <p v-if="addError" class="text-[12px] text-[var(--forebrain-danger)]" role="alert">{{ addError }}</p>
    </div>
    <template #footer>
      <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" :disabled="saving" @click="addOpen = false">
        {{ t('common.cancel') }}
      </Button>
      <Button
        type="button"
        class="forebrain-btn forebrain-btn-primary h-auto text-xs"
        :disabled="saving || !channelChoice || !recipient.trim() || !enabledChannels.length"
        data-testid="task-notify-submit"
        @click="add"
      >{{ t('projects.taskNotifyAdd') }}</Button>
    </template>
  </ForebrainModal>
</template>

<script setup lang="ts">
/**
 * A batch project's task notifications (plan 153 A12): where every execution's
 * end — success or failure — is announced. The list is the project's own
 * `task_notify`, written whole on every change, so what is stored is exactly
 * what was on screen.
 *
 * The channel dropdown only offers the channels this project has enabled: a
 * notification sent through a channel nobody configured would go nowhere. The
 * recipients are the chats and people this project's channels have already
 * spoken to, with typing one in always allowed.
 */
import { onMounted, ref, watch } from 'vue'
import { Plus, Trash2 } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import { forebrainApi, getErrorMessage, type ChannelTargetRow, type ProjectRecord, type TaskNotifyTarget } from '@/lib/api'
import { channelLabel } from '@/lib/channelLabels'
import { useI18n } from '@/locales'

const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()
const emit = defineEmits<{ saved: [] }>()
const { t } = useI18n()

const targets = ref<TaskNotifyTarget[]>([])
const enabledChannels = ref<string[]>([])
const known = ref<ChannelTargetRow[]>([])
const saving = ref(false)
const notice = ref('')
const error = ref('')

const addOpen = ref(false)
const channelChoice = ref('')
const recipient = ref('')
const addError = ref('')

/** The channel's own name, in the reader's language when it has one. */
function channelName(kind: string): string {
  return channelLabel(kind)
}

watch(() => props.project?.taskNotify, (value) => {
  targets.value = [...(value ?? [])]
}, { immediate: true })

/** The whole list goes back on every write: the PATCH replaces it. */
async function write(next: TaskNotifyTarget[]) {
  if (saving.value) return
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    await forebrainApi.projectUpdate(props.projectId, { taskNotify: next })
    targets.value = next
    notice.value = t('projects.taskNotifySaved')
    emit('saved')
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

function remove(index: number) {
  const next = targets.value.filter((_, at) => at !== index)
  void write(next)
}

function openAdd() {
  addError.value = ''
  recipient.value = ''
  channelChoice.value = enabledChannels.value[0] ?? ''
  addOpen.value = true
}

function pick(row: ChannelTargetRow) {
  channelChoice.value = channelChoice.value && enabledChannels.value.includes(channelChoice.value)
    ? channelChoice.value
    : row.channel
  recipient.value = row.target
}

async function add() {
  const channel = String(channelChoice.value ?? '').trim()
  const target = recipient.value.trim()
  if (!channel || !target) {
    addError.value = t('projects.taskNotifyPickFirst')
    return
  }
  const next = [...targets.value, { channel, target }]
  const before = error.value
  await write(next)
  if (error.value && error.value !== before) return
  addOpen.value = false
}

onMounted(async () => {
  try {
    const page = await forebrainApi.projectChannelsPage(props.projectId, { page: 1, pageSize: 50 })
    enabledChannels.value = page.rows.filter((row) => row.enabled).map((row) => row.kind)
  } catch {
    // Without the channel list the dialog offers none; the list already on the
    // project still shows, and a target is added once a channel is read again.
    enabledChannels.value = []
  }
  try {
    known.value = await forebrainApi.getChannelTargets(props.projectId)
  } catch {
    known.value = []
  }
})
</script>
