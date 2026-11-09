<template>
  <div class="grid gap-3">
    <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

    <ListToolbar>
      <template #left>
        <Input
          v-model="q"
          type="search"
          :placeholder="t('settings.varsSearch')"
          class="forebrain-field h-[34px] w-auto"
          style="max-width: 240px"
          data-testid="vars-search"
        />
      </template>
      <template #actions>
        <Button
          type="button"
          class="forebrain-btn forebrain-btn-primary h-auto gap-1.5 text-xs"
          data-testid="vars-add"
          @click="openAdd"
        >
          <Plus class="size-3.5" aria-hidden="true" />{{ t('settings.varsAdd') }}
        </Button>
      </template>
    </ListToolbar>

    <div v-if="loading && !rows.length" class="py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
      {{ t('common.loading') }}
    </div>
    <DataTable v-else :columns="columns" :rows="rows" :row-key="(row) => row.name" :row-attrs="(row) => ({ 'data-var-row': row.name })">
      <template #cell-name="{ row }">
        <span class="font-mono text-[12px]" data-testid="vars-cell-name">{{ row.name }}</span>
      </template>
      <template #cell-value="{ row }">
        <span v-if="row.valueSet" class="forebrain-keymask" data-testid="vars-cell-value">{{ '•'.repeat(8) }}<b>{{ row.valueLast4 }}</b></span>
        <BadgeComponent v-else variant="warning" data-testid="vars-cell-unset">{{ t('models.keyUnset') }}</BadgeComponent>
      </template>
      <template #cell-updatedAt="{ row }">
        <span class="text-[var(--forebrain-muted-text)]">{{ formatTime(row.updatedAt) }}</span>
      </template>
      <template #cell-actions="{ row }">
        <RowActions>
          <RowAction
            :icon="Pencil"
            :label="t('settings.varsEdit')"
            :name="row.name"
            :data-testid="`vars-edit-${row.name}`"
            @click="openEdit(row)"
          />
          <RowAction
            :icon="Trash2"
            :label="t('common.delete')"
            :name="row.name"
            tone="danger"
            :data-testid="`vars-remove-${row.name}`"
            @click="askRemove(row)"
          />
        </RowActions>
      </template>
      <template #empty>
        <span data-testid="vars-empty">{{ t('settings.varsEmpty') }}</span>
      </template>
    </DataTable>

    <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />

    <ListNote>{{ t('settings.varsHint') }}</ListNote>

    <!-- A variable is written once and read by a config's ${NAME}: the dialog
         takes the two fields and nothing else. -->
    <ForebrainModal :open="dialogOpen" size="sm" :title="editing ? t('settings.varsEditTitle') : t('settings.varsAddTitle')" @close="dialogOpen = false">
      <div class="grid gap-3">
        <label class="grid gap-1.5">
          <span class="text-[12px] font-medium text-[var(--forebrain-text-2)]">{{ t('settings.varsName') }}</span>
          <Input
            v-model="form.name"
            class="forebrain-field h-[32px] font-mono text-[12px]"
            :placeholder="t('settings.varsNamePlaceholder')"
            :readonly="editing"
            autocomplete="off"
            spellcheck="false"
            data-testid="vars-name"
          />
          <span v-if="nameError" class="text-[12px] text-[var(--forebrain-danger)]" data-testid="vars-name-error">{{ nameError }}</span>
        </label>
        <label class="grid gap-1.5">
          <span class="text-[12px] font-medium text-[var(--forebrain-text-2)]">{{ t('settings.varsValue') }}</span>
          <Input
            v-model="form.value"
            type="password"
            class="forebrain-field h-[32px] font-mono text-[12px]"
            :placeholder="t('settings.varsValuePlaceholder')"
            autocomplete="new-password"
            spellcheck="false"
            data-testid="vars-value"
          />
        </label>
        <p v-if="saveError" class="text-[12px] text-[var(--forebrain-danger)]" data-testid="vars-save-error">{{ saveError }}</p>
      </div>
      <template #footer>
        <Button type="button" variant="ghost" class="forebrain-btn forebrain-btn--ghost h-auto text-xs" data-testid="vars-cancel" @click="dialogOpen = false">
          {{ t('common.cancel') }}
        </Button>
        <Button
          type="button"
          class="forebrain-btn forebrain-btn-primary h-auto text-xs"
          :disabled="saving || !form.name.trim() || !form.value.trim()"
          data-testid="vars-save"
          @click="save"
        >
          {{ saving ? t('common.loading') : t('common.save') }}
        </Button>
      </template>
    </ForebrainModal>

    <ConfirmDialog
      :open="removeName !== ''"
      :title="t('settings.varsDeleteTitle')"
      :message="t('settings.varsDeleteMessage')"
      :confirm-label="t('common.delete')"
      danger
      @cancel="removeName = ''"
      @confirm="confirmRemove"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * Settings · Variables (plan 266): the operator's own values, which MCP and LSP
 * entries read as `${NAME}` since the home `.env` went away (plans 259/268).
 *
 * The value never comes back — only the fact that one is set and its last four
 * characters — so an edit writes a new value rather than showing the old one;
 * a blank value is not a write at all, which is why the delete button exists.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import { Pencil, Plus, Trash2 } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'

import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import TablePager from '@/components/common/TablePager.vue'
import { useServerList } from '@/composables/useServerList'
import { forebrainApi, getErrorMessage, type EnvVarRow } from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()

const { rows, total, page, pageSize, q, loading, error, load, reload } = useServerList<EnvVarRow>(
  (query) => forebrainApi.envVarsPage(query),
  { pageSize: 10 },
)

const columns = computed<DataColumn[]>(() => [
  { id: 'name', header: t('settings.varsColName') },
  { id: 'value', header: t('settings.varsColValue') },
  { id: 'updatedAt', header: t('settings.varsColUpdated'), size: 160 },
  { id: 'actions', header: t('models.colActions'), size: 100, headerClass: 'text-center' },
])

const dialogOpen = ref(false)
const editing = ref(false)
const saving = ref(false)
const saveError = ref('')
const removeName = ref('')
const form = reactive({ name: '', value: '' })

/** The name rules the gateway enforces, said where it is typed rather than after
 *  the write: letters, digits and underscores, and no leading digit. */
const nameError = computed(() => {
  const name = form.name.trim()
  if (!name || editing.value) return ''
  return /^[A-Za-z_][A-Za-z0-9_]*$/.test(name) ? '' : t('settings.varsNameInvalid')
})

function formatTime(ms: number): string {
  if (!ms) return ''
  const when = new Date(ms)
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${pad(when.getMonth() + 1)}-${pad(when.getDate())} ${pad(when.getHours())}:${pad(when.getMinutes())}`
}

function openAdd(): void {
  editing.value = false
  form.name = ''
  form.value = ''
  saveError.value = ''
  dialogOpen.value = true
}

function openEdit(row: EnvVarRow): void {
  editing.value = true
  form.name = row.name
  form.value = ''
  saveError.value = ''
  dialogOpen.value = true
}

async function save(): Promise<void> {
  if (saving.value) return
  saving.value = true
  saveError.value = ''
  try {
    await forebrainApi.setEnvVar(form.name.trim(), form.value)
    dialogOpen.value = false
    await reload()
  } catch (cause) {
    saveError.value = getErrorMessage(cause)
  } finally {
    saving.value = false
  }
}

function askRemove(row: EnvVarRow): void {
  removeName.value = row.name
}

async function confirmRemove(): Promise<void> {
  const name = removeName.value
  removeName.value = ''
  if (!name) return
  try {
    await forebrainApi.deleteEnvVar(name)
    await reload()
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
}

onMounted(() => {
  void load()
})
</script>
