<template>
  <div class="mx-auto max-w-4xl space-y-4">
    <p v-if="error" class="rounded-lg border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>
    <p v-if="notice" class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-text)]">{{ notice }}</p>

    <ListToolbar>
      <template #left>
        <input
          v-model="q"
          type="search"
          :placeholder="t('channels.searchPlaceholder')"
          class="forebrain-field h-[34px]"
          style="max-width: 240px"
          data-testid="channels-search"
        />
      </template>
      <template #actions>
        <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" :disabled="loading" data-testid="channels-refresh" @click="reload()">
          <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
        </button>
      </template>
    </ListToolbar>

    <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
      {{ t('common.empty') }}
    </div>
    <div v-else class="forebrain-table-wrap">
      <table class="forebrain-table forebrain-table--fixed">
        <colgroup>
          <col style="width: 130px" />
          <col style="width: 230px" />
          <col />
          <col style="width: 56px" />
          <col style="width: 56px" />
        </colgroup>
        <thead>
          <tr>
            <th>{{ t('channels.colChannel') }}</th>
            <th>{{ t('channels.colIdentity') }}</th>
            <th>{{ t('channels.colConfig') }}</th>
            <th>{{ t('channels.enabled') }}</th>
            <th class="text-right">{{ t('projects.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in rows" :key="row.kind" :data-channel-row="row.kind">
            <td>
              <span class="forebrain-cell-1 font-medium" v-tip:top.trunc="channelLabel(row.kind)">{{ channelLabel(row.kind) }}</span>
            </td>
            <!-- The identity a scheduled task delivers to is the channel kind
                 itself; a channel nobody filled in has none yet. -->
            <td>
              <span
                v-if="row.configured"
                class="forebrain-cell-1 font-mono text-[var(--forebrain-text-2)]"
                v-tip:top.trunc="row.kind"
              >{{ row.kind }}</span>
              <span v-else class="text-[var(--forebrain-muted-text)]">—</span>
            </td>
            <td>
              <span v-if="!row.configured" class="text-[var(--forebrain-muted-text)]">{{ t('channels.unconfigured') }}</span>
              <span v-else class="forebrain-cell-1">
                <template v-for="(part, index) in summaryOf(row)" :key="part.key">
                  <span v-if="index" class="text-[var(--forebrain-muted-text)]"> · </span>{{ part.key }}
                  <span class="font-mono text-[var(--forebrain-text-2)]">{{ part.value }}</span>
                </template>
              </span>
            </td>
            <td>
              <!-- A channel that holds no configuration cannot be switched on:
                 there is nothing behind the switch yet. -->
              <span :data-testid="`channel-switch-${row.kind}`" v-tip:top="row.configured ? '' : t('channels.configureFirst')" class="inline-flex">
                <SwitchComponent
                  :model-value="row.enabled"
                  :disabled="!row.configured || busy"
                  :aria-label="t('channels.switchLabel', { name: channelLabel(row.kind) })"
                  :data-testid="`channel-toggle-${row.kind}`"
                  @update:model-value="setEnabled(row, $event)"
                />
              </span>
            </td>
            <td class="text-right">
              <RowActions>
                <RowAction
                  :icon="Pencil"
                  :label="t('common.edit')"
                  :name="channelLabel(row.kind)"
                  :data-testid="`channel-edit-${row.kind}`"
                  @click="editing = row"
                />
              </RowActions>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
    <ListNote>{{ t('channels.projectNote') }}</ListNote>

    <ChannelEditDialog
      v-if="editing"
      :project-id="projectId"
      :row="editing"
      @cancel="editing = null"
      @saved="onSaved"
    />
  </div>
</template>

<script setup lang="ts">
/**
 * The delivery channels of one project. A channel binds an external account to
 * this project's conversations, so what is edited here belongs to the project
 * named in the breadcrumb and to no other: the same bot cannot be configured
 * for two projects.
 *
 * The table is the server's (plan 075): the search and the page are applied
 * there, the 配置 column says in the channel's own keys what was filled in, and
 * a secret shows the hint the server hands back. There is no page-level save —
 * the switch and the editor each write on their own, and both write the whole
 * channel map, because a PUT that drops a kind clears it.
 */
import { computed, onMounted, ref } from 'vue'
import { Pencil, RotateCw } from 'lucide-vue-next'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import TablePager from '@/components/common/TablePager.vue'
import ChannelEditDialog from '@/components/project/ChannelEditDialog.vue'
import { useServerList } from '@/composables/useServerList'
import { forebrainApi, getErrorMessage, type ProjectChannelRow } from '@/lib/api'
import { channelLabel } from '@/lib/channelLabels'
import { useI18n } from '@/locales'

const props = defineProps<{ project: unknown; projectId: string }>()

const { t } = useI18n()

const { rows, total, page, pageSize, q, loading, error: listError, reload } = useServerList<ProjectChannelRow>(
  (query) => forebrainApi.projectChannelsPage(props.projectId, query),
  { pageSize: 10 },
)

const writeError = ref('')
const error = computed(() => listError.value || writeError.value)
const notice = ref('')
const busy = ref(false)
/** The row whose editor is open; a kind, never a position. */
const editing = ref<ProjectChannelRow | null>(null)

/** One non-empty field of a configured channel: its own key and what it holds.
 * A secret shows the hint the server sent instead of a value nobody has. */
function summaryOf(row: ProjectChannelRow): Array<{ key: string; value: string }> {
  return Object.keys(row.fields)
    .filter((key) => key !== 'enabled')
    .sort()
    .filter((key) => String(row.fields[key] ?? '').trim() !== '')
    .map((key) => ({
      key,
      value: row.secretKeys.includes(key) ? (row.secretHints[key] || String(row.fields[key] ?? '')) : String(row.fields[key] ?? ''),
    }))
}

// The switch writes at once: the whole map goes back — the PUT clears the kinds
// a page drops — with only this kind's `enabled` moved, so every other channel
// and every stored secret stays exactly as the server handed them over.
async function setEnabled(row: ProjectChannelRow, value: boolean) {
  if (!row.configured || busy.value) return
  busy.value = true
  writeError.value = ''
  notice.value = ''
  try {
    const raw = await forebrainApi.projectChannelsRaw(props.projectId)
    const channels: Record<string, Record<string, unknown>> = { ...raw.channels }
    channels[row.kind] = { ...(channels[row.kind] ?? {}), enabled: value }
    await forebrainApi.saveProjectChannels(props.projectId, channels)
    notice.value = t('channels.saved')
    await reload()
  } catch (e: unknown) {
    writeError.value = getErrorMessage(e)
  } finally {
    busy.value = false
  }
}

/** The editor wrote the kind it owns; the table behind it is now stale. */
async function onSaved() {
  editing.value = null
  notice.value = t('channels.saved')
  await reload()
}

onMounted(() => {
  void reload()
})
</script>
