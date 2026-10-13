<template>
  <div>
    <div class="flex flex-wrap items-center gap-2">
      <input
        v-model="query"
        type="search"
        class="forebrain-field h-8 max-w-[240px]"
        :placeholder="t('skills.searchPlaceholder')"
        data-testid="skills-search"
      />
      <select v-model="sourceFilter" class="forebrain-field h-8 w-[120px]" data-testid="skills-source-filter">
        <option value="all">{{ t('skills.sourceAll') }}</option>
        <option value="project">{{ t('skills.originProject') }}</option>
        <option value="agent">{{ t('skills.originAgent') }}</option>
        <option value="shared">{{ t('skills.originShared') }}</option>
        <option value="builtin">{{ t('skills.originBuiltin') }}</option>
      </select>
      <span class="flex-1" />
      <span v-if="selectedNames.length" class="text-[12px] text-[var(--forebrain-muted-text)]">
        {{ t('skills.selectedCount', { count: selectedNames.length }) }}
      </span>
      <button
        type="button"
        class="forebrain-btn forebrain-btn-ghost text-xs"
        :disabled="!selectedNames.length || downloading"
        data-testid="skills-batch-download"
        @click="emit('batchDownload', selectedNames)"
      >
        {{ downloading ? t('common.loading') : t('skills.batchDownload', { count: selectedNames.length }) }}
      </button>
    </div>

    <div v-if="loading" class="px-4 py-8 text-center text-[13px] text-[var(--forebrain-muted-text)]">
      {{ t('common.loading') }}
    </div>
    <div v-else-if="!visibleRows.length" class="px-4 py-10 text-center text-[13px] text-[var(--forebrain-muted-text)]">
      {{ t('skills.empty') }}
    </div>
    <table v-else class="forebrain-table mt-3 w-full">
      <thead>
        <tr>
          <th class="w-8">
            <input
              type="checkbox"
              :checked="allSelected"
              :aria-label="t('skills.selectAll')"
              @change="toggleAll(($event.target as HTMLInputElement).checked)"
            />
          </th>
          <th>{{ t('skills.colSkill') }}</th>
          <th class="w-[110px]">{{ t('skills.colSource') }}</th>
          <th class="w-[190px] text-right">{{ t('skills.colStatus') }}</th>
          <th class="w-[170px] text-right">{{ t('skills.colActions') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="row in visibleRows"
          :key="row.rootPath || row.name"
          :data-skill-row="row.name"
          :data-skill-origin="row.origin"
        >
          <td>
            <input
              type="checkbox"
              :checked="selected.has(selectKey(row))"
              :aria-label="t('skills.selectForDownload', { name: row.name })"
              @change="toggleSelect(row, ($event.target as HTMLInputElement).checked)"
            />
          </td>
          <td>
            <div class="flex flex-wrap items-center gap-2">
              <span class="font-mono text-[13px] font-medium text-[var(--forebrain-text)]">{{ row.name }}</span>
              <BadgeComponent v-if="row.state === 'pending'" variant="warning">{{ t('skills.unpublished') }}</BadgeComponent>
              <BadgeComponent v-if="row.update" variant="default">{{ t('skills.hasUpdate') }}</BadgeComponent>
            </div>
            <div class="mt-0.5 text-[12px] leading-relaxed text-[var(--forebrain-text-2)]">
              {{ row.description || '—' }}
              <span v-if="row.rootPath" class="font-mono text-[11px] text-[var(--forebrain-muted-text)]"> · {{ row.rootPath }}</span>
            </div>
          </td>
          <td>
            <BadgeComponent variant="gray">{{ originLabel(row.origin) }}</BadgeComponent>
          </td>
          <td class="text-right">
            <div class="inline-flex items-center gap-2">
              <BadgeComponent v-if="row.state === 'draft'" variant="warning">{{ t('skills.draft') }}</BadgeComponent>
              <BadgeComponent v-else-if="row.version" variant="gray">{{ `v${row.version}` }}</BadgeComponent>
              <SwitchComponent
                v-if="isOwned(row) && !isDraft(row)"
                :model-value="row.enabled"
                :disabled="saving"
                :data-testid="`skill-toggle-${row.name}`"
                :title="t('skills.toggleHint')"
                :aria-label="t('skills.toggleHint')"
                @update:model-value="emit('toggle', row, $event)"
              />
              <template v-else-if="!isOwned(row)">
                <BadgeComponent :variant="row.enabled ? 'success' : 'gray'">
                  {{ row.enabled ? t('skills.enabled') : t('skills.disabled') }}
                </BadgeComponent>
                <LockIcon class="h-3.5 w-3.5 text-[var(--forebrain-muted-text)]" :title="t('skills.inheritedReadOnly')" :aria-label="t('skills.inheritedReadOnly')" />
              </template>
            </div>
          </td>
          <td class="text-right">
            <div class="inline-flex items-center gap-1">
              <button
                v-if="(isOwned(row) && ownerOrigin !== 'shared') || row.update"
                type="button"
                class="forebrain-btn forebrain-btn-ghost text-xs"
                :data-testid="`skill-detail-${row.name}`"
                @click="emit('detail', row)"
              >
                {{ t('skills.detail') }}
              </button>
              <button
                v-if="row.downloadUrl"
                type="button"
                class="inline-flex h-7 w-7 items-center justify-center rounded-md text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-input-hover-bg)]"
                :title="t('skills.download')"
                :aria-label="t('skills.download')"
                :data-testid="`skill-download-${row.name}`"
                @click="emit('download', row)"
              >
                <Download class="h-4 w-4" aria-hidden="true" />
              </button>
              <button
                v-if="isOwned(row) && row.origin !== 'builtin'"
                type="button"
                class="skill-row-danger inline-flex h-7 w-7 items-center justify-center rounded-md hover:bg-[var(--forebrain-input-hover-bg)]"
                :title="t('common.delete')"
                :aria-label="t('common.delete')"
                :data-testid="`skill-delete-${row.name}`"
                @click="emit('delete', row)"
              >
                <Trash2 class="h-4 w-4" aria-hidden="true" />
              </button>
            </div>
          </td>
        </tr>
      </tbody>
    </table>
    <span v-if="hint" class="mt-2 block text-[11px] text-[var(--forebrain-muted-text)]">{{ hint }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Download, Lock as LockIcon, Trash2 } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import type { SkillRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  rows: SkillRecord[]
  loading: boolean
  saving: boolean
  downloading: boolean
  /** The layer this page owns: rows of any other origin are read-only. */
  ownerOrigin: 'agent' | 'shared' | 'project'
  /** The trailing path line under the table. */
  hint?: string
}>(), { hint: '' })

const emit = defineEmits<{
  toggle: [row: SkillRecord, enabled: boolean]
  delete: [row: SkillRecord]
  download: [row: SkillRecord]
  batchDownload: [names: string[]]
  detail: [row: SkillRecord]
}>()

const { t } = useI18n()

const query = ref('')
const sourceFilter = ref('all')

const visibleRows = computed(() => {
  const q = query.value.trim().toLowerCase()
  return props.rows.filter((row) => {
    if (sourceFilter.value !== 'all' && row.origin !== sourceFilter.value) return false
    if (!q) return true
    return row.name.toLowerCase().includes(q) || (row.description ?? '').toLowerCase().includes(q)
  })
})

// Inherited rows carry no controls at all (decision D9): a skill is toggled
// and deleted only in the layer that owns it.
function isOwned(row: SkillRecord) {
  return row.origin === props.ownerOrigin
}

// A draft-only row has no published version to enable, so it never grows a
// switch: the badge says 草稿 instead.
function isDraft(row: SkillRecord) {
  return row.state === 'draft' || row.draftOnly === true
}

function originLabel(origin: SkillRecord['origin']) {
  switch (origin) {
    case 'project': return t('skills.originProject')
    case 'agent': return t('skills.originAgent')
    case 'shared': return t('skills.originShared')
    case 'builtin': return t('skills.originBuiltin')
    case 'cross-tool': return t('skills.originCrossTool')
    default: return origin ?? ''
  }
}

function selectKey(row: SkillRecord) {
  return row.rootPath || row.name
}

const selected = ref(new Set<string>())

const selectedNames = computed(() =>
  props.rows.filter((row) => selected.value.has(selectKey(row))).map((row) => row.name),
)

const allSelected = computed(() =>
  visibleRows.value.length > 0 && visibleRows.value.every((row) => selected.value.has(selectKey(row))),
)

function toggleSelect(row: SkillRecord, checked: boolean) {
  const next = new Set(selected.value)
  if (checked) next.add(selectKey(row))
  else next.delete(selectKey(row))
  selected.value = next
}

function toggleAll(checked: boolean) {
  const next = new Set(selected.value)
  for (const row of visibleRows.value) {
    if (checked) next.add(selectKey(row))
    else next.delete(selectKey(row))
  }
  selected.value = next
}

// A refreshed list may rename paths; stale selections must not survive it.
watch(
  () => props.rows,
  () => {
    const live = new Set(props.rows.map(selectKey))
    const next = new Set<string>()
    for (const key of selected.value) {
      if (live.has(key)) next.add(key)
    }
    selected.value = next
  },
)
</script>

<style scoped>
.skill-row-danger {
  color: var(--forebrain-danger);
}
</style>
