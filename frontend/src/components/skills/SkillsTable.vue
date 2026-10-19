<template>
  <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
  <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
    {{ t('skills.empty') }}
  </div>
  <div v-else class="forebrain-table-wrap">
    <table class="forebrain-table forebrain-table--fixed">
      <colgroup>
        <col style="width: 30px" />
        <col />
        <col style="width: 110px" />
        <col style="width: 190px" />
        <col style="width: 110px" />
      </colgroup>
      <thead>
        <tr>
          <th>
            <input
              type="checkbox"
              :checked="allSelected"
              :aria-label="t('skills.selectAll')"
              data-testid="skills-select-all"
              @change="toggleAll(($event.target as HTMLInputElement).checked)"
            />
          </th>
          <th>{{ t('skills.colSkill') }}</th>
          <th>{{ t('skills.colSource') }}</th>
          <th class="text-right">{{ t('skills.colStatus') }}</th>
          <th class="text-center">{{ t('skills.colActions') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr
          v-for="row in rows"
          :key="row.rootPath || row.name"
          :data-skill-row="row.name"
          :data-skill-origin="row.origin"
        >
          <td>
            <input
              type="checkbox"
              :checked="selected.has(row.name)"
              :aria-label="t('skills.selectForDownload', { name: row.name })"
              @change="toggleSelect(row, ($event.target as HTMLInputElement).checked)"
            />
          </td>
          <td>
            <div class="flex min-w-0 items-center gap-1.5">
              <span class="min-w-0 truncate font-mono text-[13px] font-medium text-[var(--forebrain-text)]">{{ row.name }}</span>
              <BadgeComponent v-if="row.state === 'pending'" variant="warning" class="shrink-0">{{ t('skills.unpublished') }}</BadgeComponent>
              <BadgeComponent v-if="row.update" class="shrink-0">{{ t('skills.hasUpdate') }}</BadgeComponent>
            </div>
            <div class="mt-0.5 text-[12px] text-[var(--forebrain-text-2)]">
              <span class="forebrain-cell-1" v-tip:top.trunc="descriptionLine(row)">{{ descriptionLine(row) }}</span>
            </div>
          </td>
          <td>
            <BadgeComponent variant="gray">{{ originLabel(row.origin) }}</BadgeComponent>
          </td>
          <td class="text-right">
            <span class="inline-flex items-center gap-2">
              <BadgeComponent v-if="row.state === 'draft'" variant="warning">{{ t('skills.draft') }}</BadgeComponent>
              <BadgeComponent v-else-if="row.version" variant="gray" class="font-mono">{{ `v${row.version}` }}</BadgeComponent>
              <SwitchComponent
                v-if="isOwned(row) && !isDraft(row)"
                :model-value="row.enabled"
                :disabled="saving"
                :data-testid="`skill-toggle-${row.name}`"
                :aria-label="t('skills.toggleHint')"
                @update:model-value="emit('toggle', row, $event)"
              />
              <template v-else-if="!isOwned(row)">
                <BadgeComponent :variant="row.enabled ? 'success' : 'gray'">
                  {{ row.enabled ? t('skills.enabled') : t('skills.disabled') }}
                </BadgeComponent>
                <LockIcon class="size-3.5 text-[var(--forebrain-muted-text)]" v-tip:top="t('skills.inheritedReadOnly')" :aria-label="t('skills.inheritedReadOnly')" />
              </template>
            </span>
          </td>
          <td class="text-right">
            <RowActions>
              <RowAction
                v-if="showsView(row)"
                :icon="Eye"
                :label="t('skills.view')"
                :name="row.name"
                :data-testid="`skill-detail-${row.name}`"
                @click="emit('detail', row)"
              />
              <RowActionSlot v-else />
              <RowAction
                v-if="row.downloadUrl"
                :icon="Download"
                :label="t('skills.downloadZip')"
                :name="row.name"
                :data-testid="`skill-download-${row.name}`"
                @click="emit('download', row)"
              />
              <RowActionSlot v-else />
              <RowAction
                v-if="isOwned(row) && row.origin !== 'builtin'"
                :icon="Trash2"
                :label="t('common.delete')"
                :name="row.name"
                tone="danger"
                :data-testid="`skill-delete-${row.name}`"
                @click="emit('delete', row)"
              />
              <RowActionSlot v-else />
            </RowActions>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup lang="ts">
/**
 * The skill table every skill page draws (plan 073): the rows of one page, the
 * boxes ticked for a bulk download, and the row's actions — nothing else. The
 * search box, the layer filter, the buttons above and the pager below belong to
 * the page, because the three pages that use this table (the primary agent's,
 * a project space's, the shared library) offer different ones.
 *
 * A row is the layer's own only where the listing says so: inherited rows are
 * toggled and deleted in the layer that owns them (decision D9). Every action
 * is an icon button that names itself and the row it belongs to, and a row
 * missing one of them keeps its hole so the icons line up down the column.
 */
import { computed } from 'vue'
import { Download, Eye, Lock as LockIcon, Trash2 } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import RowActionSlot from '@/components/common/RowActionSlot.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import type { SkillRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const props = defineProps<{
  rows: SkillRecord[]
  loading: boolean
  saving: boolean
  /** The layer this page owns: rows of any other origin are read-only. */
  scope: 'agent' | 'shared' | 'project'
  /** The names ticked for a bulk download; the page owns the set. */
  selected: Set<string>
}>()

const emit = defineEmits<{
  toggle: [row: SkillRecord, enabled: boolean]
  delete: [row: SkillRecord]
  download: [row: SkillRecord]
  detail: [row: SkillRecord]
  'update:selected': [selected: Set<string>]
}>()

const { t } = useI18n()

const allSelected = computed(() => props.rows.length > 0 && props.rows.every((row) => props.selected.has(row.name)))

// Inherited rows carry no controls at all (decision D9): a skill is toggled
// and deleted only in the layer that owns it.
function isOwned(row: SkillRecord) {
  return row.origin === props.scope
}

// A draft-only row has no published version to enable, so it never grows a
// switch: the badge says 草稿 instead.
function isDraft(row: SkillRecord) {
  return row.state === 'draft' || row.draftOnly === true
}

// 查看 opens the skill in the workshop: the layer's own rows have something to
// improve, and a row with an upstream update has something to look at.
function showsView(row: SkillRecord) {
  return (isOwned(row) && props.scope !== 'shared') || Boolean(row.update)
}

/** The grey second line under a name: what it does, and where it lives. */
function descriptionLine(row: SkillRecord): string {
  const parts = [row.description || '—']
  if (row.rootPath) parts.push(row.rootPath)
  return parts.join(' · ')
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

function toggleSelect(row: SkillRecord, checked: boolean) {
  const next = new Set(props.selected)
  if (checked) next.add(row.name)
  else next.delete(row.name)
  emit('update:selected', next)
}

// 全选 works on the page on screen: a server-paged table has no other rows to
// tick, and the count above it says how many that is.
function toggleAll(checked: boolean) {
  const next = new Set(props.selected)
  for (const row of props.rows) {
    if (checked) next.add(row.name)
    else next.delete(row.name)
  }
  emit('update:selected', next)
}
</script>
