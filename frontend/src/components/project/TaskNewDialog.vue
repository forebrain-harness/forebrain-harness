<template>
  <ForebrainModal :open="true" :title="t('tasks.newTitle')" size="md" @close="emit('close')">
    <div class="grid gap-3">
      <label class="forebrain-form-field">
        <span>{{ t('tasks.fieldContent') }}</span>
        <Textarea
          v-model="content"
          rows="4"
          class="forebrain-field min-h-0 w-full"
          :placeholder="t('tasks.contentPlaceholder')"
          data-testid="task-new-content"
        />
      </label>

      <!-- Attachments first (plan 153): the files are uploaded, then the task
           is opened, then the run that carries them starts — the engine copies
           them into the execution's own inputs/ folder. -->
      <div class="forebrain-form-field">
        <span>{{ t('tasks.fieldAttachments') }}</span>
        <div class="flex flex-wrap items-center gap-1.5" data-testid="task-new-attachments">
          <span
            v-for="(file, index) in files"
            :key="`${file.name}-${index}`"
            class="inline-flex items-center gap-1.5 rounded-md border border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] px-2 py-1 text-[12px] text-[var(--forebrain-text-2)]"
          >
            <FileText class="size-3.5" aria-hidden="true" />
            {{ file.name }}
            <button
              type="button"
              class="text-[var(--forebrain-muted-text)] hover:text-[var(--forebrain-danger)]"
              :aria-label="`${t('projects.taskNotifyDelete')} ${file.name}`"
              :data-testid="`task-new-attachment-remove-${index}`"
              @click="files.splice(index, 1)"
            >
              <X class="size-3" aria-hidden="true" />
            </button>
          </span>
          <Button
            type="button"
            class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs"
            data-testid="task-new-attach"
            @click="pickerEl?.click()"
          >
            <Paperclip class="size-3.5" aria-hidden="true" />{{ t('tasks.addAttachment') }}
          </Button>
          <input ref="pickerEl" type="file" multiple class="hidden" data-testid="task-new-file-input" @change="onPicked" />
        </div>
      </div>

      <div class="grid gap-3 md:grid-cols-2">
        <label class="forebrain-form-field">
          <span>{{ t('tasks.fieldTitle') }}</span>
          <Input v-model="title" class="forebrain-field h-[34px] w-full" :placeholder="t('tasks.titlePlaceholder')" data-testid="task-new-title" />
        </label>
        <label class="forebrain-form-field">
          <span>{{ t('tasks.fieldExternalRef') }}</span>
          <Input v-model="externalRef" class="forebrain-field h-[34px] w-full font-mono" :placeholder="t('tasks.externalRefPlaceholder')" data-testid="task-new-external-ref" />
        </label>
      </div>

      <span class="forebrain-list-note">{{ t('tasks.newHint') }}</span>
      <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]" role="alert" data-testid="task-new-error">{{ error }}</p>
    </div>
    <template #footer>
      <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" :disabled="saving" @click="emit('close')">
        {{ t('common.cancel') }}
      </Button>
      <Button
        type="button"
        class="forebrain-btn forebrain-btn-primary h-auto text-xs"
        :disabled="saving || !content.trim()"
        data-testid="task-new-submit"
        @click="submit"
      >
        {{ saving ? t('common.loading') : t('tasks.createAndRun') }}
      </Button>
    </template>
  </ForebrainModal>
</template>

<script setup lang="ts">
/**
 * 新建任务 (plan 153 A10): the task's own words, the files it starts with, an
 * optional title and the caller's own reference. Creating it opens the task,
 * uploads what was attached into that execution, and starts the first run
 * immediately — the form says so, and 创建并执行 is what it does.
 *
 * A blank title takes the task's first line, which is what the field promises:
 * the gateway records exactly what it is given, so the fallback belongs here.
 */
import { ref } from 'vue'
import { FileText, Paperclip, X } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import { Textarea } from '@repo/shadcn-vue/components/ui/textarea'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import { forebrainApi, getErrorMessage } from '@/lib/api'
import { BatchStartRefusal, startBatchRun } from '@/lib/batchRun'
import { batchRefusalMessage } from '@/lib/tasks'
import { useI18n } from '@/locales'

const props = defineProps<{ projectId: string }>()
const emit = defineEmits<{
  (e: 'close'): void
  /** The execution the new task started: its task, its number and its session. */
  (e: 'created', payload: { taskId: string; seq: number; sessionId: string }): void
}>()

const { t } = useI18n()

const content = ref('')
const title = ref('')
const externalRef = ref('')
const files = ref<File[]>([])
const saving = ref(false)
const error = ref('')
const pickerEl = ref<HTMLInputElement | null>(null)

function onPicked(event: Event) {
  const input = event.target as HTMLInputElement
  files.value = [...files.value, ...Array.from(input.files ?? [])]
  input.value = ''
}

/** The task's name when none was typed: its first non-empty line, cut to a
 *  name a table column can hold. */
function derivedTitle(): string {
  const line = content.value.split('\n').map((row) => row.trim()).find(Boolean) ?? ''
  return line.length > 60 ? `${line.slice(0, 60)}…` : line
}

async function submit() {
  if (saving.value || !content.value.trim()) return
  saving.value = true
  error.value = ''
  try {
    const attachments: string[] = []
    for (const file of files.value) {
      const uploaded = await forebrainApi.filesUpload(file)
      if (uploaded?.fileId) attachments.push(uploaded.fileId)
    }
    const created = await forebrainApi.createTaskSession(props.projectId, {
      title: title.value.trim() || derivedTitle(),
      externalRef: externalRef.value.trim(),
    })
    await startBatchRun(created.id, { content: content.value, attachments })
    emit('created', { taskId: created.task.id, seq: created.execution.seq, sessionId: created.id })
  } catch (cause) {
    error.value = cause instanceof BatchStartRefusal
      ? batchRefusalMessage(cause.code, cause.message)
      : getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}
</script>
