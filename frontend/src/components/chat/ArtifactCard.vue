<template>
  <!-- One delivered file (plan 153 A5/A7): what it is, how big, and the two
       things a person can do with it — open it in the drawer's preview tab, or
       download it. The row states facts the engine gave; it never guesses. -->
  <div class="art" data-testid="artifact-card">
    <span class="art-file-icon" aria-hidden="true">
      <component :is="icon" />
    </span>
    <div class="m">
      <b :title="artifact.name">{{ artifact.title }}</b>
      <span>{{ kindLabel }} · {{ sizeLabel }}</span>
    </div>
    <span v-if="artifact.missing" class="badge err" data-testid="artifact-missing">{{ t('chat.artifactMissing') }}</span>
    <span v-else-if="artifact.changed" class="badge warn" data-testid="artifact-changed">{{ t('chat.artifactChanged') }}</span>
    <span class="a">
      <button
        v-if="canPreview"
        type="button"
        class="icon-btn row-act"
        :aria-label="t('chat.artifactPreview', { name: artifact.name })"
        v-tip:top="t('chat.artifactPreview', { name: artifact.name })"
        data-testid="artifact-preview"
        @click="emit('preview', artifact)"
      >
        <Eye aria-hidden="true" />
      </button>
      <a
        v-if="artifact.downloadUrl"
        class="icon-btn row-act"
        :href="artifact.downloadUrl"
        download
        :aria-label="t('chat.artifactDownload', { name: artifact.name })"
        v-tip:top="t('chat.artifactDownload', { name: artifact.name })"
        data-testid="artifact-download"
      >
        <Download aria-hidden="true" />
      </a>
    </span>
  </div>
</template>

<script setup lang="ts">
/**
 * A delivered file's row. It is drawn in the turn's delivery group and
 * nowhere else (plan 153 A7, owner 2026-10-27): the card that presented the
 * files names them in its head, and this list is the one place a person sees
 * the files themselves.
 */
import { computed, type Component } from 'vue'
import { Download, Eye } from 'lucide-vue-next'
import { useI18n } from '@/locales'
import { artifactKindIcon, artifactKindLabelKey, humanSize, type ArtifactView } from '@/lib/artifacts'

const props = defineProps<{
  artifact: ArtifactView
}>()

const emit = defineEmits<{
  /** The reader asked for the file's in-place preview (a drawer tab). */
  (e: 'preview', artifact: ArtifactView): void
}>()

const { t } = useI18n()

const icon = computed<Component>(() => artifactKindIcon(props.artifact.kind))
const kindLabel = computed(() => t(artifactKindLabelKey(props.artifact.kind)))
const sizeLabel = computed(() => humanSize(props.artifact.size))

/** A file is previewable only when it says how and can actually be read: a
 *  row whose bytes are gone has nothing to draw. */
const canPreview = computed(() => props.artifact.preview !== '' && props.artifact.contentUrl !== '' && !props.artifact.missing)
</script>

<style scoped>
.art {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 12px;
  padding: 10px 0;
  border-top: 1px solid var(--forebrain-divider);
}

.art:first-child {
  border-top: 0;
  padding-top: 2px;
}

.art-file-icon {
  display: grid;
  place-items: center;
  width: 28px;
  height: 32px;
  flex: none;
  color: var(--forebrain-text-2);
}

.art-file-icon :deep(svg) {
  width: 19px;
  height: 19px;
}

.m {
  flex: 1;
  min-width: 0;
  display: grid;
  gap: 2px;
}

.m b {
  font: 600 13px/1.4 var(--forebrain-ui-font);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.m span {
  font: 11.5px/1.4 var(--forebrain-ui-font);
  color: var(--forebrain-muted-text);
}

.a {
  display: flex;
  align-items: center;
  gap: 2px;
  flex: none;
}

.row-act {
  display: inline-grid;
  place-items: center;
  width: 30px;
  height: 30px;
  border-radius: 6px;
  color: var(--forebrain-text-2);
  text-decoration: none;
}

.row-act:hover {
  background: var(--forebrain-bg-alt);
  color: var(--forebrain-brand-1);
}

.row-act :deep(svg) {
  width: 15px;
  height: 15px;
}

.badge {
  display: inline-flex;
  align-items: center;
  height: 20px;
  padding: 0 7px;
  border-radius: 999px;
  font: 500 11px/1 var(--forebrain-ui-font);
  white-space: nowrap;
  flex: none;
}

.badge.warn {
  background: var(--forebrain-warning-soft);
  color: var(--forebrain-warning);
}

.badge.err {
  background: var(--forebrain-surface);
  color: var(--forebrain-danger);
  border: 1px solid var(--forebrain-danger);
}

@media (max-width: 680px) {
  .art {
    flex-wrap: wrap;
  }

  .a {
    width: 100%;
    justify-content: flex-end;
  }
}
</style>
