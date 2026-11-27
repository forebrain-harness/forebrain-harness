<template>
  <!-- A workspace file a prose file:line link opened (plan file-line-range-links):
       the whole text read-only, the named lines highlighted and centred. Every
       extension the link whitelist admits is source text here — md and html
       included, the way workbuddy's own editor shows them; there is no preview
       or browser route, because the whitelist admits no image or binary. -->
  <div class="file-doc" data-testid="file-doc">
    <p v-if="loading" class="file-state" data-testid="file-doc-loading">{{ t('chat.fileDocLoading') }}</p>
    <div v-else-if="errorBody" class="file-state file-none" data-testid="file-doc-error">
      <FileWarning aria-hidden="true" />
      <b>{{ t('chat.fileDocErrorTitle') }}</b>
      <span>{{ errorBody }}</span>
    </div>
    <template v-else>
      <p v-if="rangeNotice" class="file-range-note" data-testid="file-doc-range-note">{{ rangeNotice }}</p>
      <div class="file-face">
        <ReadonlyCode
          :path="file.path"
          :text="text"
          :highlight-from="clampedFrom"
          :highlight-to="clampedTo"
        />
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * The drawer tab a file:line link opens. One read of the file through the
 * conversation's own file view (the same bounded read the workbench uses),
 * then the read-only source view with the range as a landing mark. A range
 * past the file's end is clamped and says so; a refusal keeps the gateway's
 * status — a missing file, an unreadable path and an over-large file each
 * read as themselves.
 */
import { computed, onMounted, ref } from 'vue'
import { FileWarning } from 'lucide-vue-next'
import { useI18n } from '@/locales'
import { forebrainApi, GatewayHttpError, getErrorMessage } from '@/lib/api'
import type { FileDocTarget } from '@/composables/useDocTabs'
import ReadonlyCode from '@/components/editor/ReadonlyCode.vue'

const props = defineProps<{ file: FileDocTarget }>()

const { t } = useI18n()

const loading = ref(true)
const error = ref<unknown>(null)
const text = ref('')

// One read per tab: the tab's id is its scope and path, so a new range on the
// same tab never refetches — the view repaints its highlight instead.
onMounted(async () => {
  try {
    const answer = await forebrainApi.workspaceFile(props.file.scope, props.file.path)
    text.value = answer.content
  } catch (err) {
    error.value = err
  } finally {
    loading.value = false
  }
})

const errorBody = computed(() => {
  const err = error.value
  if (!err) return ''
  if (err instanceof GatewayHttpError) {
    if (err.status === 404) return t('chat.fileDocNotFound', { path: props.file.path })
    if (err.status === 413) return t('chat.fileDocTooLarge', { path: props.file.path })
    if (err.status === 400) return t('chat.fileDocBadPath', { path: props.file.path })
  }
  return t('chat.fileDocError', { message: getErrorMessage(err) })
})

/** The file's line count, the way the editor counts: a trailing newline opens
 *  one more (empty) line. */
const lineCount = computed(() => text.value.split('\n').length)
const clampedFrom = computed(() => Math.min(Math.max(1, props.file.lineFrom), lineCount.value))
const clampedTo = computed(() => Math.min(Math.max(clampedFrom.value, props.file.lineTo), lineCount.value))
const rangeNotice = computed(() =>
  props.file.lineFrom > lineCount.value || props.file.lineTo > lineCount.value
    ? t('chat.fileDocRangePast', { from: props.file.lineFrom, to: props.file.lineTo, lines: lineCount.value })
    : '',
)
</script>

<style scoped>
.file-doc {
  display: flex;
  flex-direction: column;
  flex: 1;
  min-height: 0;
  gap: 8px;
  padding: 10px 12px 12px;
}

.file-state {
  margin: 4px 2px;
  font: 12.5px/1.6 var(--forebrain-ui-font);
  color: var(--forebrain-muted-text);
}

.file-none {
  display: grid;
  justify-items: center;
  gap: 8px;
  padding: 34px 16px;
  text-align: center;
}

.file-none svg {
  width: 22px;
  height: 22px;
}

.file-none b {
  font: 600 13px/1.5 var(--forebrain-ui-font);
  color: var(--forebrain-text);
}

.file-range-note {
  margin: 0 2px;
  font: 11.5px/1.5 var(--forebrain-ui-font);
  color: var(--forebrain-muted-text);
}

.file-face {
  flex: 1;
  min-height: 0;
  padding: 10px 12px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  background: var(--forebrain-surface);
  overflow: auto;
}
</style>