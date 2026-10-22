<template>
  <div class="flex flex-col gap-4 pb-6">
    <ListToolbar>
      <template #left>
        <span class="font-mono text-[12px] text-[var(--forebrain-muted-text)]" data-testid="config-path">{{ path }}</span>
      </template>
      <template #actions>
        <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto text-xs" :disabled="saving || !dirty" data-testid="config-apply" @click="save">
          {{ saving ? t('common.loading') : t('config.apply') }}
        </Button>
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs" :disabled="loading" data-testid="config-refresh" @click="load">
          <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
        </Button>
      </template>
    </ListToolbar>

    <p v-if="error" class="whitespace-pre-wrap rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>
    <p v-if="notice" class="text-[12px] text-[var(--forebrain-brand-1)]">{{ notice }}</p>

    <FileEditor :key="editorKey" :path="path || 'forebrain.yaml'" v-model="yaml" @dirty="dirty = $event" />

    <ListNote data-testid="config-note">{{ t('config.description') }}</ListNote>
  </div>
</template>

<script setup lang="ts">
/**
 * forebrain.yaml itself. The file is validated by the runtime before it is
 * written, so a mistake is reported here rather than at the next start; the
 * running process picks up the change immediately. The editor is the shared
 * FileEditor in code mode — YAML is not Markdown, so there is no WYSIWYG pane.
 *
 * The file's own path sits in the toolbar's left (plan 076), as the prototype
 * puts it, so the reader knows which file the button below it saves.
 */
import { defineAsyncComponent, onMounted, ref } from 'vue'
import { RotateCw } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import { getErrorMessage, forebrainApi } from '@/lib/api'
import { useI18n } from '@/locales'

const FileEditor = defineAsyncComponent(() => import('@/components/editor/FileEditor.vue'))

const { t } = useI18n()
const yaml = ref('')
const path = ref('')
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')
const dirty = ref(false)
// Remount the editor after every load so an edit-then-save leaves it clean even
// when the reloaded bytes are identical to what was sent.
const editorKey = ref(0)

async function load() {
  loading.value = true
  error.value = ''
  notice.value = ''
  try {
    const res = await forebrainApi.configFile()
    yaml.value = res.yaml ?? ''
    path.value = res.path ?? ''
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
    editorKey.value += 1
  }
}

async function save() {
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    await forebrainApi.saveConfigFile(yaml.value)
    // The reload clears what was said last, so the applied notice is what the
    // reader is left with once the file on screen is the one just saved.
    const applied = t('config.applied')
    await load()
    notice.value = applied
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
