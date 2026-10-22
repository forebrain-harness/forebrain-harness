<template>
  <div class="space-y-3" data-testid="workshop-versions">
    <div class="flex flex-wrap items-center justify-between gap-y-1.5">
      <span class="whitespace-nowrap text-[13px] font-semibold text-[var(--forebrain-text)]">{{ t('workshop.tabVersions') }}</span>
      <!-- The icon and the label are one run of text, as the native button had
           them: the library's Button would space them with `gap-2`. -->
      <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto gap-0 text-xs" :disabled="!skillName" data-testid="workshop-version-download" @click="downloadCurrent">
        <Download class="size-3.5" aria-hidden="true" /> {{ t('workshop.downloadZip') }}
      </Button>
    </div>

    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]" data-testid="workshop-versions-error">{{ error }}</p>
    <p v-if="!skillName" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('workshop.tabNeedsSkill') }}</p>
    <template v-else>
      <div v-if="state?.update" class="flex items-center gap-2 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] p-2 text-[12px] text-[var(--forebrain-text-2)]" data-testid="workshop-version-update">
        <span class="min-w-0 flex-1">{{ t('workshop.upstreamUpdate', { rev: state.update.rev, date: state.update.date ?? '' }) }}</span>
        <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" :disabled="!editable || busy" data-testid="workshop-version-pull" @click="pullUpdate">{{ t('workshop.pullToDraft') }}</Button>
      </div>

      <div v-if="state?.hasDraft" class="ver-draft rounded-lg border border-[var(--forebrain-border-strong,var(--forebrain-divider))] p-2.5" data-testid="workshop-version-draft">
        <div class="flex items-center justify-between">
          <span class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('workshop.draftRow') }}</span>
          <span class="flex gap-1">
            <Button v-if="state.draftBase" type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" data-testid="workshop-draft-compare" @click="emit('compare', { from: `v${state.draftBase}`, to: 'draft' })">{{ t('workshop.compare') }}</Button>
            <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs text-[var(--forebrain-danger)]" :disabled="!editable || busy" data-testid="workshop-draft-discard" @click="confirmDiscard = true">{{ t('workshop.discard') }}</Button>
          </span>
        </div>
        <div class="mt-0.5 text-[11px] text-[var(--forebrain-muted-text)]">{{ state.draftBase ? t('workshop.draftBasedOn', { v: state.draftBase }) : t('workshop.draftUnpublished') }}</div>
      </div>

      <div v-if="!state || !state.versions.length" class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('workshop.noVersions') }}</div>
      <div v-else class="space-y-2">
        <div v-for="(v, index) in visibleVersions" :key="v.v" class="rounded-lg border border-[var(--forebrain-divider)] p-2.5" :data-testid="`workshop-version-row-${v.v}`">
          <div class="flex items-center justify-between gap-2">
            <span class="font-mono text-[13px] text-[var(--forebrain-text)]">v{{ v.v }}</span>
            <span class="flex items-center gap-1">
              <BadgeComponent v-if="v.v === state.current" variant="success">{{ t('workshop.current') }}</BadgeComponent>
              <Button v-if="v.v > 1" type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" @click="emit('compare', { from: `v${prev(v.v)}`, to: `v${v.v}` })">{{ t('workshop.compare') }}</Button>
              <Button v-if="v.v !== state.current" type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" :disabled="!editable || busy" @click="askRestore(v.v)">{{ t('workshop.restoreAsDraft') }}</Button>
              <button type="button" class="inline-flex h-6 w-6 items-center justify-center rounded-md text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-input-hover-bg)]" v-tip:top="t('workshop.downloadVersion', { v: v.v })" @click="downloadVersion(v.v)">
                <Download class="h-3.5 w-3.5" aria-hidden="true" />
              </button>
            </span>
          </div>
          <div class="mt-0.5 text-[11px] text-[var(--forebrain-text-2)]">{{ kindLabel(v) }}<span v-if="v.note"> · {{ v.note }}</span></div>
          <div class="text-[11px] text-[var(--forebrain-muted-text)]">{{ formatTime(v.at) }}<span v-if="v.stat"> · {{ t('workshop.statLine', { added: v.stat.added, removed: v.stat.removed }) }}</span></div>
          <div v-if="index === 0 && visibleVersions.length === 1" class="sr-only">{{ t('workshop.newest') }}</div>
        </div>
      </div>
      <button v-if="state && state.versions.length > visibleCount" type="button" class="text-[12px] text-[var(--forebrain-brand-1)] hover:underline" data-testid="workshop-version-more" @click="visibleCount += 20">
        {{ t('workshop.showEarlier') }}
      </button>
    </template>

    <ConfirmDialog
      :open="confirmDiscard"
      :title="t('workshop.discardTitle', { name: skillName })"
      :message="t('workshop.discardMessage', { v: state?.current ?? 0 })"
      :confirm-label="t('workshop.discard')"
      danger
      @confirm="discardDraft"
      @cancel="confirmDiscard = false"
    />
    <ConfirmDialog
      :open="restoreVersion !== null"
      :title="t('workshop.restoreTitle', { v: restoreVersion ?? 0 })"
      :message="t('workshop.restoreMessage', { v: restoreVersion ?? 0, replaced: state?.hasDraft ? t('workshop.restoreReplaces') : '' })"
      :confirm-label="t('workshop.restoreAsDraft')"
      @confirm="doRestore"
      @cancel="restoreVersion = null"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * The workshop's version tab: the draft against the current version, the
 * published history (twenty at a time), and the per-version compare, restore
 * and download actions. Every action goes through the skill version routes.
 */
import { computed, onMounted, ref, watch } from 'vue'
import { Download } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import forebrainApi, { getErrorMessage, saveSkillDownload, type SkillVersionRecord, type SkillVersionState } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{ skillName: string; projectId?: string; editable: boolean }>(), { projectId: '' })
const emit = defineEmits<{ compare: [payload: { from: string; to: string }]; changed: [] }>()
const { t, locale } = useI18n()

const state = ref<SkillVersionState | null>(null)
const error = ref('')
const busy = ref(false)
const confirmDiscard = ref(false)
const restoreVersion = ref<number | null>(null)
const visibleCount = ref(20)

const visibleVersions = computed(() => (state.value?.versions ?? []).slice(0, visibleCount.value))

async function load() {
  if (!props.skillName) {
    state.value = null
    return
  }
  error.value = ''
  try {
    const res = await forebrainApi.skillVersions(props.skillName, props.projectId)
    state.value = res.state
    visibleCount.value = 20
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

function prev(v: number): number {
  return v - 1
}

const KIND_LABELS: Record<string, Parameters<typeof t>[0]> = {
  created: 'workshop.versionKind.created',
  installed: 'workshop.versionKind.installed',
  published: 'workshop.versionKind.published',
  updated: 'workshop.versionKind.updated',
}

function kindLabel(v: SkillVersionRecord): string {
  const base = t(KIND_LABELS[v.kind] ?? 'workshop.versionKind.published')
  return v.source ? `${base} · ${v.source}` : base
}

function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime())
    ? '—'
    : date.toLocaleString(locale.value === 'zh' ? 'zh-CN' : 'en-US', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })
}

async function downloadCurrent() {
  try {
    saveSkillDownload(await forebrainApi.skillDownload(skillDownloadUrl()))
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

function skillDownloadUrl(): string {
  const base = props.projectId ? `/api/v1/projects/${encodeURIComponent(props.projectId)}/skills` : '/api/skills'
  return `${base}/${encodeURIComponent(props.skillName)}/download`
}

async function downloadVersion(version: number) {
  try {
    saveSkillDownload(await forebrainApi.skillDownload(forebrainApi.skillVersionDownloadUrl(props.skillName, version, props.projectId)))
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

async function discardDraft() {
  confirmDiscard.value = false
  await run(() => forebrainApi.skillDiscardDraft(props.skillName, props.projectId))
}

function askRestore(version: number) {
  restoreVersion.value = version
}

async function doRestore() {
  const version = restoreVersion.value
  restoreVersion.value = null
  if (version === null) return
  await run(() => forebrainApi.skillRestoreVersion(props.skillName, version, state.value?.hasDraft ?? false, props.projectId))
}

async function pullUpdate() {
  await run(() => forebrainApi.skillPull(props.skillName, state.value?.hasDraft ?? false, props.projectId))
}

async function run(action: () => Promise<unknown>) {
  busy.value = true
  error.value = ''
  try {
    await action()
    emit('changed')
    await load()
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  } finally {
    busy.value = false
  }
}

defineExpose({ reload: load })
watch(() => [props.skillName, props.projectId], load)
onMounted(load)
</script>
