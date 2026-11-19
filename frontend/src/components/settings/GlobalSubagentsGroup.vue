<template>
  <!-- 设置 · 子代理 · 「全局共享子代理」(plan 314). One install-wide definition
       every primary agent may enable on its own subagents page, plus the four
       built-in types' global default rows beside it. Hand-written rather than
       schema-driven, like the two model groups above: a registry is a list with
       create/edit/delete, which SETTINGS_FORMS cannot express — and keeping it
       out of settingsSchema.ts is what keeps that file's test comparing the
       prototype's own FORMS.

       Every write answers with the whole registry, so this group renders what
       the server holds rather than predicting it. -->
  <section class="gs" data-group="shared-subagents">
    <header class="gs__head">
      <h3>{{ t('subagents.shared.title') }}</h3>
      <Button
        type="button"
        class="forebrain-btn forebrain-btn-primary h-auto gap-1.5 text-xs"
        data-testid="shared-subagents-new"
        @click="openNew"
      >
        <Plus class="size-3.5" aria-hidden="true" />{{ t('subagents.shared.new') }}
      </Button>
    </header>

    <p v-if="error" class="gs__err" role="alert" data-shared-error>{{ error }}</p>

    <!-- The per-type defaults: the install-wide row a built-in type runs on
         when an agent's own entry names none (plan 315). Editing writes
         agents.subagent_models.<type>; there is no provider to edit anywhere. -->
    <div class="gs__types" data-shared-types>
      <b class="gs__sub">{{ t('subagents.shared.typesTitle') }}</b>
      <div v-for="entry in types" :key="entry.type" class="gs__type" :data-shared-type="entry.type">
        <span class="gs__typename font-mono">{{ entry.type }}</span>
        <GoalCheckModelSelect
          :model-value="entry.model"
          :choices="choices"
          @update:model-value="(next: string) => onTypeModel(entry.type, next)"
        />
        <select
          class="gs__select"
          :disabled="!entry.model"
          :value="entry.reasoningEffort"
          :aria-label="t('subagents.shared.typeEffort')"
          :data-shared-type-effort="entry.type"
          @change="onTypeEffort(entry.type, $event)"
        >
          <option value="">{{ t('subagents.shared.typeEffortFollow') }}</option>
          <option v-for="level in effortsFor(entry)" :key="level" :value="level">
            {{ effortLevelLabel(level) }}
          </option>
        </select>
        <Button
          type="button"
          class="forebrain-btn forebrain-btn-ghost h-auto text-xs"
          :disabled="!isTypeDirty(entry.type)"
          :data-shared-type-save="entry.type"
          @click="saveType(entry.type)"
        >
          {{ t('subagents.shared.typeSave') }}
        </Button>
        <span v-if="typeSaved[entry.type]" class="gs__ok" :data-shared-type-saved="entry.type">
          ✓ {{ t('subagents.shared.typeSaved') }}
        </span>
      </div>
    </div>

    <p v-if="!rows.length" class="gs__empty" data-shared-empty>{{ t('subagents.shared.empty') }}</p>
    <DataTable
      v-else
      :columns="columns"
      :rows="rows"
      :row-key="(row) => row.name"
      :row-attrs="(row) => ({ 'data-shared-row': row.name })"
    >
      <template #cell-name="{ row }">
        <b class="font-mono" data-testid="shared-subagent-name">{{ row.name }}</b>
        <div class="gs__desc text-[var(--forebrain-muted-text)]" v-tip:top.trunc="row.description">{{ row.description }}</div>
      </template>
      <template #cell-model="{ row }">
        <span v-if="row.model" class="font-mono">{{ providerModel(row) }}</span>
        <span v-else class="text-[var(--forebrain-muted-text)]">{{ t('subagents.followPrimary') }}</span>
        <!-- The space rides in the expression: the template compiler condenses
             a leading space inside an element away, which butts the effort
             against the pair. -->
        <span v-if="row.reasoningEffort" class="text-[var(--forebrain-muted-text)]">{{ ' ' + effortLevelLabel(row.reasoningEffort) }}</span>
      </template>
      <template #cell-tools="{ row }">{{ toolsLabel(row) }}</template>
      <template #cell-actions="{ row }">
        <RowActions>
          <RowAction
            :icon="Pencil"
            :label="t('common.edit')"
            :name="row.name"
            :data-testid="`shared-subagent-edit-${row.name}`"
            @click="openEdit(row)"
          />
          <RowAction
            :icon="Trash2"
            :label="t('common.delete')"
            :name="row.name"
            tone="danger"
            :data-testid="`shared-subagent-delete-${row.name}`"
            @click="askDelete(row)"
          />
        </RowActions>
      </template>
    </DataTable>
  </section>

  <SubagentConfigDrawer
    v-if="drawer"
    :open="true"
    scope="global"
    :name="drawer.name"
    :mode="drawer.mode"
    @close="drawer = null"
    @saved="onSaved"
  />

  <ConfirmDialog
    :open="deleteTarget !== null"
    :title="t('subagents.shared.deleteTitle', { name: deleteTarget?.name ?? '' })"
    :message="t('subagents.shared.deleteBody')"
    :confirm-label="t('common.delete')"
    danger
    @cancel="deleteTarget = null"
    @confirm="confirmDelete"
  />
</template>

<script setup lang="ts">
/**
 * The install-wide shared subagent registry (plan 314): one definition every
 * primary agent may consume, edited here and nowhere else, with the four
 * built-in types' global default model rows beside it (plan 315).
 *
 * Enablement is deliberately absent from this group: a shared subagent is
 * disabled for every agent until that agent's own page turns it on, so a switch
 * here would be a promise this surface cannot keep.
 */
import { onMounted, ref } from 'vue'
import { Pencil, Plus, Trash2 } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import GoalCheckModelSelect from '@/components/settings/GoalCheckModelSelect.vue'
import SubagentConfigDrawer from '@/components/subagents/SubagentConfigDrawer.vue'
import { useModelChoices } from '@/composables/useModelChoices'
import {
  forebrainApi,
  getErrorMessage,
  type SharedSubagentListing,
  type SubagentConfigRow,
  type SubagentTypeDefault,
} from '@/lib/api'
import { effortLevelLabel } from '@/lib/effortLabel'
import { useI18n } from '@/locales'

const { t } = useI18n()
const { choices, ensureChoices } = useModelChoices()

/** The registry and the per-type rows, exactly as the last answer carried them. */
const rows = ref<SubagentConfigRow[]>([])
const types = ref<SubagentTypeDefault[]>([])
/** What each type's row was when it was read, so a staged edit knows it moved. */
const seed = ref<Record<string, string>>({})
/** A write's refusal, in the server's own words. */
const error = ref('')
const typeSaved = ref<Record<string, boolean>>({})

const columns: DataColumn[] = [
  { id: 'name', header: t('subagents.colName') },
  { id: 'model', header: t('subagents.colModel'), size: 220 },
  { id: 'tools', header: t('subagents.colTools'), size: 92 },
  { id: 'actions', header: t('subagents.colActions'), size: 96, headerClass: 'text-center', cellClass: 'text-right' },
]

/** The one read: the registry, the per-type rows, and the models a picker needs. */
async function load(): Promise<void> {
  error.value = ''
  try {
    const listing = await forebrainApi.fetchSharedSubagents()
    apply(listing)
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
}

function apply(listing: SharedSubagentListing): void {
  rows.value = listing.rows
  types.value = listing.types
  const next: Record<string, string> = {}
  for (const entry of listing.types) next[entry.type] = typeKey(entry)
  seed.value = next
}

onMounted(() => {
  ensureChoices()
  void load()
})

/** One type row's staged identity: the model and the level it names. */
function typeKey(entry: SubagentTypeDefault): string {
  return `${entry.model}\u0000${entry.reasoningEffort}`
}

function isTypeDirty(type: string): boolean {
  const entry = types.value.find((candidate) => candidate.type === type)
  return entry !== undefined && typeKey(entry) !== (seed.value[type] ?? '')
}

/** The levels the chosen row offers, plus a stored one it no longer lists so a
 *  configured level cannot silently turn into the default. */
function effortsFor(entry: SubagentTypeDefault): string[] {
  const selected = choices.value.find((choice) => choice.id === entry.model)
  const listed = selected?.efforts ?? []
  return entry.reasoningEffort !== '' && !listed.includes(entry.reasoningEffort)
    ? [entry.reasoningEffort, ...listed]
    : listed
}

/** Choosing another row drops a level it does not offer; the row's own default
 *  is what an empty level means, so nothing is invented here. */
function onTypeModel(type: string, next: string): void {
  const entry = types.value.find((candidate) => candidate.type === type)
  if (!entry) return
  entry.model = next
  const efforts = choices.value.find((choice) => choice.id === next)?.efforts ?? []
  if (entry.reasoningEffort !== '' && !efforts.includes(entry.reasoningEffort)) entry.reasoningEffort = ''
  typeSaved.value[type] = false
}

function onTypeEffort(type: string, event: Event): void {
  const entry = types.value.find((candidate) => candidate.type === type)
  if (!entry) return
  entry.reasoningEffort = (event.target as HTMLSelectElement).value
  typeSaved.value[type] = false
}

async function saveType(type: string): Promise<void> {
  const entry = types.value.find((candidate) => candidate.type === type)
  if (!entry) return
  error.value = ''
  try {
    const listing = await forebrainApi.updateSubagentTypeDefault(type, {
      model: entry.model,
      reasoningEffort: entry.reasoningEffort,
    })
    apply(listing)
    typeSaved.value[type] = true
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
}

function providerModel(row: SubagentConfigRow): string {
  return [row.modelProvider, row.model].filter(Boolean).join('/')
}

function toolsLabel(row: SubagentConfigRow): string {
  switch (row.tools) {
    case 'edit':
      return t('subagents.toolsEdit')
    case 'pick':
      return t('subagents.toolsPick', { n: row.allowedTools.length })
    default:
      return t('subagents.toolsReadOnly')
  }
}

/** The drawer, when one is open: a shared definition is created or replaced. */
const drawer = ref<{ mode: 'create' | 'edit'; name?: string } | null>(null)

function openNew(): void {
  drawer.value = { mode: 'create' }
}

function openEdit(row: SubagentConfigRow): void {
  drawer.value = { mode: 'edit', name: row.name }
}

async function onSaved(): Promise<void> {
  drawer.value = null
  await load()
}

const deleteTarget = ref<SubagentConfigRow | null>(null)

function askDelete(row: SubagentConfigRow): void {
  deleteTarget.value = row
}

async function confirmDelete(): Promise<void> {
  const target = deleteTarget.value
  if (!target) return
  error.value = ''
  try {
    await forebrainApi.deleteSharedSubagent(target.name)
    deleteTarget.value = null
    await load()
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
}

</script>

<style scoped>
.gs {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 14px 16px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-bg-alt);
}
.gs__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.gs__head h3 {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
}
.gs__types {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.gs__sub {
  font-size: 12px;
  font-weight: 500;
  color: var(--forebrain-text-2);
}
.gs__type {
  display: grid;
  grid-template-columns: 140px minmax(0, 1fr) 140px auto auto;
  align-items: center;
  gap: 8px;
}
.gs__typename {
  font-size: 12px;
}
.gs__select {
  height: 32px;
  padding: 0 8px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: var(--forebrain-bg);
  color: var(--forebrain-text);
  font-size: 12px;
}
.gs__desc {
  margin-top: 1px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 12px;
}
.gs__err {
  margin: 0;
  font-size: 12px;
  color: var(--forebrain-danger);
}
.gs__ok {
  font-size: 12px;
  color: var(--forebrain-success, var(--forebrain-text-2));
}
.gs__empty {
  margin: 0;
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
</style>
