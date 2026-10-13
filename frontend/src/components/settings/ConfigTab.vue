<template>
  <div class="pb-6">
    <PageTopRow :title="'forebrain.yaml'" :description="t('config.description')">
      <template #actions>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">{{ t('common.refresh') }}</button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving || !dirty" data-testid="config-apply" @click="save">{{ t('config.apply') }}</button>
      </template>
    </PageTopRow>

    <p class="mb-3 font-mono text-[11px] text-[var(--forebrain-muted-text)]">{{ path }}</p>
    <p v-if="error" class="mb-4 whitespace-pre-wrap rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>
    <p v-if="notice" class="mb-4 text-[12px] text-[var(--forebrain-brand-1)]">{{ notice }}</p>

    <FileEditor :key="editorKey" :path="path || 'forebrain.yaml'" v-model="yaml" @dirty="dirty = $event" />
  </div>
</template>

<script setup lang="ts">
/**
 * forebrain.yaml itself. The file is validated by the runtime before it is
 * written, so a mistake is reported here rather than at the next start; the
 * running process picks up the change immediately. The editor is the shared
 * FileEditor in code mode — YAML is not Markdown, so there is no WYSIWYG pane.
 */
import { defineAsyncComponent, onMounted, ref } from 'vue'
import PageTopRow from '@/components/common/PageTopRow.vue'
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
    notice.value = t('config.applied')
    await load()
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
