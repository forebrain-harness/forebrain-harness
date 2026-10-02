<template>
  <div class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)]">
    <div class="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--forebrain-divider)] px-4 py-3">
      <div class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('skills.listTitle') }}</div>
      <div class="flex items-center gap-3">
        <span class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('skills.rowCount', { count: rows.length }) }}</span>
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
    </div>

    <div v-if="loading" class="px-4 py-8 text-center text-[13px] text-[var(--forebrain-muted-text)]">
      {{ t('common.loading') }}
    </div>
    <div v-else-if="!rows.length" class="px-4 py-10 text-center text-[13px] text-[var(--forebrain-muted-text)]">
      {{ t('skills.empty') }}
    </div>
    <ul v-else class="divide-y divide-[var(--forebrain-divider)]">
      <li
        v-for="row in rows"
        :key="row.rootPath || row.name"
        class="flex flex-wrap items-start gap-3 px-4 py-3"
        :data-skill-row="row.name"
        :data-skill-origin="row.origin"
      >
        <label class="mt-1 flex shrink-0 cursor-pointer items-center" :class="{ invisible: !row.downloadUrl }">
          <input
            type="checkbox"
            class="h-4 w-4 rounded border-[var(--forebrain-divider-strong)] text-[var(--forebrain-brand-1)] focus:ring-[var(--forebrain-brand-1)]"
            :checked="selected.has(selectKey(row))"
            @change="toggleSelect(row, ($event.target as HTMLInputElement).checked)"
          />
          <span class="sr-only">{{ t('skills.selectForDownload', { name: row.name }) }}</span>
        </label>

        <div class="min-w-0 flex-1">
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ row.name }}</span>
            <span class="scope-badge" :data-skill-origin-badge="row.origin">{{ originLabel(row.origin) }}</span>
            <span
              v-if="isOwned(row)"
              class="rounded-full bg-[var(--forebrain-brand-soft)] px-2 py-0.5 text-[11px] text-[var(--forebrain-brand-1)]"
            >{{ row.enabled ? t('skills.enabled') : t('skills.disabled') }}</span>
            <LockIcon
              v-else
              class="h-3.5 w-3.5 text-[var(--forebrain-muted-text)]"
              :title="t('skills.inheritedReadOnly')"
              :aria-label="t('skills.inheritedReadOnly')"
            />
            <span
              v-if="row.shadows && row.shadows.length"
              class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[11px] text-[var(--forebrain-muted-text)]"
              :title="row.shadows.join('\n')"
            >{{ t('skills.shadows', { count: row.shadows.length }) }}</span>
          </div>
          <p v-if="row.description" class="mt-1 text-[12px] leading-relaxed text-[var(--forebrain-text-2)]">{{ row.description }}</p>
          <div v-if="row.rootPath" class="mt-1 font-mono text-[11px] break-all text-[var(--forebrain-muted-text)]">{{ row.rootPath }}</div>
        </div>

        <div class="flex shrink-0 flex-wrap items-center gap-2">
          <label
            v-if="isOwned(row)"
            class="relative inline-flex cursor-pointer items-center"
            :title="t('skills.toggleHint')"
            :data-testid="`skill-switch-${row.name}`"
          >
            <input
              type="checkbox"
              class="peer sr-only"
              :checked="row.enabled"
              :disabled="saving"
              :data-testid="`skill-toggle-${row.name}`"
              @change="emit('toggle', row, ($event.target as HTMLInputElement).checked)"
            />
            <span class="pointer-events-none h-5 w-9 rounded-full bg-[var(--forebrain-divider-strong)] transition peer-checked:bg-[var(--forebrain-brand-1)] peer-focus-visible:ring-2 peer-focus-visible:ring-[var(--forebrain-ring-soft)]"></span>
            <span class="pointer-events-none absolute left-0.5 top-0.5 h-4 w-4 rounded-full bg-white transition peer-checked:translate-x-4"></span>
            <span class="sr-only">{{ t('skills.toggleHint') }}</span>
          </label>
          <button
            v-if="row.downloadUrl"
            type="button"
            class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-2.5 py-1 text-[11px] text-[var(--forebrain-text)] hover:bg-[var(--forebrain-surface)]"
            :data-testid="`skill-download-${row.name}`"
            @click="emit('download', row)"
          >
            {{ t('skills.download') }}
          </button>
          <button
            v-if="isOwned(row)"
            type="button"
            class="rounded-lg border border-[var(--forebrain-divider)] px-2.5 py-1 text-[11px] text-[var(--forebrain-danger)] hover:bg-[var(--forebrain-input-hover-bg)]"
            :data-testid="`skill-delete-${row.name}`"
            @click="emit('delete', row)"
          >
            {{ t('common.delete') }}
          </button>
        </div>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { LockIcon } from 'lucide-vue-next'
import type { SkillRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const props = defineProps<{
  rows: SkillRecord[]
  loading: boolean
  saving: boolean
  downloading: boolean
  /** The layer this page owns: rows of any other origin are read-only. */
  ownerOrigin: 'agent' | 'shared' | 'project'
}>()

const emit = defineEmits<{
  toggle: [row: SkillRecord, enabled: boolean]
  delete: [row: SkillRecord]
  download: [row: SkillRecord]
  batchDownload: [names: string[]]
}>()

const { t } = useI18n()

// Inherited rows carry no controls at all (decision D9): a skill is toggled
// and deleted only in the layer that owns it.
function isOwned(row: SkillRecord) {
  return row.origin === props.ownerOrigin
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

function toggleSelect(row: SkillRecord, checked: boolean) {
  const next = new Set(selected.value)
  if (checked) {
    next.add(selectKey(row))
  } else {
    next.delete(selectKey(row))
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
