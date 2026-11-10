<template>
  <DataTable
    :columns="columns"
    :rows="rows"
    :row-key="(row) => row.id"
    :row-attrs="rowAttrs"
    :row-class="rowClass"
  >
    <template #cell-model="{ row }">
      <span class="mname" data-testid="models-cell-model">{{ row.model }}</span>
      <BadgeComponent v-if="row.isDefault" class="ml-1.5" data-testid="models-cell-default">{{ t('models.defaultBadge') }}</BadgeComponent>
      <span class="mprov">{{ providerLine(row) }}</span>
    </template>

    <template #cell-baseUrl="{ row }">
      <span class="addr" data-testid="models-cell-endpoint">{{ row.baseUrl }}</span>
    </template>

    <template #cell-apiPath="{ row }">
      <span v-if="row.urlVerbatim" class="forebrain-path forebrain-path--na" data-testid="models-cell-path">
        {{ t('models.customProtocol') }}
      </span>
      <!-- A sign-in row whose endpoint speaks its own wire format: there is no
           path to show, and saying so is honest where an empty cell is not. -->
      <span v-else-if="!row.apiPath && row.loginBacked" class="forebrain-path forebrain-path--na" data-testid="models-cell-path">
        {{ t('models.nativeProtocol') }}
      </span>
      <span v-else class="forebrain-path" data-testid="models-cell-path">{{ shownPath(row) }}</span>
    </template>

    <template #cell-key="{ row }">
      <span v-if="row.loginBacked" class="key-none" data-testid="models-cell-account">{{ t('models.accountLogin') }}</span>
      <span v-else-if="row.apiKeySet" class="forebrain-keymask" data-testid="models-cell-key">
        {{ bullets(row) }}<b>{{ row.apiKeyLast4 }}</b>
      </span>
      <BadgeComponent v-else variant="warning" data-testid="models-cell-key-unset">{{ t('models.keyUnset') }}</BadgeComponent>
    </template>

    <template #cell-actions="{ row }">
      <RowActions>
        <!-- The hole keeps the icons aligned: a row that cannot be made the
             default is the row that already is one. -->
        <RowActionSlot v-if="row.isDefault || !row.id" />
        <RowAction
          v-else
          :icon="Star"
          :label="t('models.setDefault')"
          :name="row.model"
          :data-testid="`models-default-${row.id}`"
          @click="emit('setDefault', row)"
        />
        <RowAction
          :icon="Pencil"
          :label="t('models.edit')"
          :name="row.model"
          :data-testid="`models-edit-${row.id}`"
          @click="emit('edit', row)"
        />
        <RowAction
          :icon="Trash2"
          :label="t('models.delete')"
          :name="row.model"
          tone="danger"
          :data-testid="`models-remove-${row.id}`"
          @click="emit('remove', row)"
        />
      </RowActions>
    </template>

    <template #empty>
      <span data-testid="models-empty">{{ t('models.empty') }}</span>
    </template>
  </DataTable>
</template>

<script setup lang="ts">
/**
 * The install's models table (plan 266): one row per configured model, the four
 * fields that identify it, and the mask that says a key exists.
 *
 * Every row shows the endpoint and the API path even when its sign-in decided
 * them: 「由登录决定」 would hide the one fact that tells two otherwise identical
 * rows apart. The key is bullets for every character but the last four — never a
 * “saved” pill, which says nothing about which key it is — and a row that
 * signs in has 「账号登录」 there instead.
 */
import { computed } from 'vue'
import { Pencil, Star, Trash2 } from 'lucide-vue-next'

import BadgeComponent from '@/components/common/BadgeComponent.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActionSlot from '@/components/common/RowActionSlot.vue'
import RowActions from '@/components/common/RowActions.vue'
import type { LlmModelRow } from '@/lib/api'
import { useI18n } from '@/locales'

const props = defineProps<{
  rows: LlmModelRow[]
  /** The row the page just jumped to: highlighted for a moment. */
  flashId?: string
}>()

const emit = defineEmits<{
  edit: [row: LlmModelRow]
  remove: [row: LlmModelRow]
  setDefault: [row: LlmModelRow]
}>()

const { t } = useI18n()

/** The design's column widths: the model at a quarter, the endpoint taking the
 *  room the four fixed columns leave, the path and the mask fixed. */
const columns = computed<DataColumn[]>(() => [
  { id: 'model', header: t('models.colModel'), size: 240 },
  { id: 'baseUrl', header: t('models.colEndpoint'), cellClass: 'models-addr-cell !whitespace-normal' },
  { id: 'apiPath', header: t('models.colPath'), size: 160 },
  { id: 'key', header: t('models.colKey'), size: 140 },
  { id: 'actions', header: t('models.colActions'), size: 112, headerClass: 'text-center' },
])

function rowAttrs(row: LlmModelRow): Record<string, string> {
  return { 'data-model-row': row.id }
}

function rowClass(row: LlmModelRow): Record<string, boolean> {
  return { 'models-row--flash': Boolean(props.flashId) && row.id === props.flashId }
}

/** The provider line: the service, plus where a subscription row came from, and
 *  — for a row the agent may pick for subagents — the word for that (plan 283). */
function providerLine(row: LlmModelRow): string {
  const name = row.providerName || row.provider
  const line = (() => {
    if (!row.source) return name
    const plan = row.sourcePlan ? t('models.sourceLineWithPlan', { plan: row.sourcePlan }) : t('models.sourceLine')
    return `${name}${plan}`
  })()
  if (!row.subagentAllowed) return line
  return `${line} · ${t('models.subagentAllowedMark')}`
}

/** The path a row's requests really take. A row that stores none is the engine's
 *  own Chat Completions default (the same reading the old page had), which is
 *  worth showing rather than an empty cell. */
function shownPath(row: LlmModelRow): string {
  return row.apiPath || '/chat/completions'
}

/** A key's mask: one bullet per hidden character, held between four and twelve
 *  so a long key cannot widen the column. */
function bullets(row: LlmModelRow): string {
  const length = String(row.apiKey ?? '').length || 32
  return '•'.repeat(Math.min(12, Math.max(4, length - 4)))
}
</script>

<style scoped>
.mname {
  font-size: 13px;
  font-weight: 500;
  color: var(--forebrain-text);
}
.mprov {
  display: block;
  font-size: 11.5px;
  line-height: 1.45;
  color: var(--forebrain-muted-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.addr {
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
  line-height: 1.5;
  color: var(--forebrain-text-2);
  word-break: break-all;
  white-space: normal;
}
.key-none {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
:deep(.models-row--flash > td) {
  animation: models-flash 1.6s ease-out 1;
}
@keyframes models-flash {
  0%,
  25% {
    background: var(--forebrain-brand-soft);
  }
  100% {
    background: transparent;
  }
}
</style>
