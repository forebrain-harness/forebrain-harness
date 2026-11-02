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

    <p v-if="notice" class="text-[12px] text-[var(--forebrain-brand-1)]" data-testid="config-notice">{{ notice }}</p>

    <!-- Only a YAML syntax error refuses a save (§0.19): the file is left as
         it was and the reader is told which line and why, above the editor
         where the wavy line already points. -->
    <div v-if="refusal" class="forebrain-err-block" role="alert" data-testid="config-unsaved">
      <CircleAlert class="size-3.5" aria-hidden="true" />
      <div>
        <b>{{ t('settings.configUnsaved') }}</b>
        <span class="forebrain-err-s">{{ t('config.lineRefusal', { line: refusal.line, message: refusal.message }) }}</span>
      </div>
    </div>
    <p v-else-if="error" class="whitespace-pre-wrap rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

    <FileEditor :key="editorKey" :path="path || 'forebrain.yaml'" :lint="lint" v-model="yaml" @dirty="dirty = $event" />

    <ListNote data-testid="config-note">{{ t('config.description') }}</ListNote>
  </div>
</template>

<script setup lang="ts">
/**
 * forebrain.yaml itself (plan 191 §0.19, §4.3). The page edits the file on
 * disk and nothing else: every setting the pages draw lives in the state
 * database, so a save writes the file and takes effect after a restart. A key
 * the file does not support is not an error — it is underlined with「不存在」,
 * saved as written and silently ignored when the file is read; only text that
 * is not YAML at all is refused, with the line and the reason above the
 * editor, and then nothing is written.
 *
 * The file's own path sits in the toolbar's left (plan 076), as the prototype
 * puts it, so the reader knows which file the button below it saves. The
 * whitelist behind「不存在」is the gateway's own (`/api/config/file-schema`),
 * not a copy — the same `FileSupportedKeys` the loader reads.
 */
import { defineAsyncComponent, onMounted, ref } from 'vue'
import { CircleAlert, RotateCw } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import { lintConfigYaml } from '@/lib/configFileLint'
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
const refusal = ref<{ line: number; message: string } | null>(null)
const dirty = ref(false)
/** The keys forebrain.yaml supports, as the gateway names them. */
const supportedKeys = ref<string[]>([])
// Remount the editor after every load so an edit-then-save leaves it clean even
// when the reloaded bytes are identical to what was sent.
const editorKey = ref(0)

/** The wavy lines: unsupported keys and the syntax error, on the current
 * text. Before the whitelist arrives there is nothing to compare against, so
 * the syntax check runs alone — the same rule a save falls back to. */
function lint(text: string): { line: number; message: string }[] {
  return lintConfigYaml(text, supportedKeys.value)
}

async function load() {
  loading.value = true
  error.value = ''
  notice.value = ''
  refusal.value = null
  try {
    const [file, schema] = await Promise.all([forebrainApi.configFile(), forebrainApi.configFileSchema()])
    yaml.value = file.yaml ?? ''
    path.value = file.path ?? ''
    supportedKeys.value = schema.keys ?? []
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
    editorKey.value += 1
  }
}

async function save() {
  if (saving.value || !dirty.value) return
  saving.value = true
  error.value = ''
  notice.value = ''
  refusal.value = null
  try {
    await forebrainApi.saveConfigFile(yaml.value)
    // The reload clears what was said last, so the saved notice is what the
    // reader is left with once the file on screen is the one just saved.
    const saved = t('config.applied')
    await load()
    notice.value = saved
  } catch (e) {
    // A syntax refusal answers {line, message}; anything else is the
    // transport's own words on the same line the editor already underlines.
    const data = (e as { response?: { data?: unknown } })?.response?.data as { line?: unknown; message?: unknown } | undefined
    if (data && typeof data.line === 'number' && typeof data.message === 'string') {
      refusal.value = { line: data.line, message: data.message }
    } else {
      error.value = getErrorMessage(e)
    }
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
