<template>
  <div class="mx-auto max-w-4xl space-y-4">
    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

    <!-- Trust gate: language-server entries load only in trusted,
         version-controlled projects — the button is the gate, exactly as
         the MCP tab's. -->
    <div v-if="!trusted" class="rounded-lg border border-[var(--forebrain-brand-border)] bg-[var(--forebrain-brand-soft)] p-4">
      <div class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.trustTitle') }}</div>
      <p class="mt-1 text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.trustHint') }}</p>
      <Button type="button" class="forebrain-btn forebrain-btn-primary mt-3 h-auto text-xs" :disabled="trusting" data-testid="project-trust" @click="trust">
        {{ trusting ? t('common.loading') : t('projects.trustLabel') }}
      </Button>
    </div>

    <template v-else>
      <ListToolbar>
        <template #left>
          <!-- The project's "always allow" switch, the same one the MCP tab
               shows: on, undecided entries load without asking; off, new and
               changed entries ask again in the next session. -->
          <span class="inline-flex items-center gap-2" data-testid="project-servers-auto-allow">
            <SwitchComponent
              :model-value="autoAllow"
              :disabled="switching"
              :aria-label="t('projects.serversAutoAllow')"
              data-testid="project-servers-auto-allow-switch"
              @update:model-value="setAutoAllow"
            />
            <span class="whitespace-nowrap text-xs text-[var(--forebrain-text-2)]">{{ t('projects.serversAutoAllow') }}</span>
          </span>
          <Input
            v-model="q"
            type="search"
            :placeholder="t('projects.lspSearchPlaceholder')"
            class="forebrain-field h-[34px] w-auto"
            style="max-width: 240px"
            data-testid="project-lsp-search"
          />
        </template>
        <template #actions>
          <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto gap-1.5 text-xs" :disabled="loading" data-testid="project-lsp-refresh" @click="refresh">
            <RotateCw class="size-3.5" aria-hidden="true" />{{ t('common.refresh') }}
          </Button>
        </template>
      </ListToolbar>

      <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
        {{ t('common.empty') }}
      </div>
      <DataTable
        v-else
        :columns="columns"
        :rows="rows"
        :row-key="(row) => row.id"
        :row-attrs="(row) => ({ 'data-lsp-row': row.id })"
      >
        <template #cell-id="{ row }">
          <span class="forebrain-cell-1 font-mono" v-tip:top.trunc="row.id">{{ row.id }}</span>
        </template>
        <template #cell-summary="{ row }">
          <span class="forebrain-cell-1" v-tip:top.trunc="row.summary">{{ row.summary || '—' }}</span>
        </template>
        <template #cell-status="{ row }">
          <BadgeComponent
            :variant="statusVariant(row.status)"
            v-tip:top="row.status === 'denied' ? t('projects.lspDeniedTip') : ''"
          >{{ statusLabel(row.status) }}</BadgeComponent>
        </template>
        <template #cell-actions="{ row }">
          <RowActions v-if="row.status === 'pending'">
            <!-- Allow applies the entry from now on; decline records it so
                 it stops asking. Each click decides this one entry. -->
            <RowAction
              :icon="Check"
              :label="t('projects.lspAllow')"
              :name="row.id"
              tone="ok"
              :disabled="consenting"
              :data-testid="`lsp-allow-${row.id}`"
              @click="consent(row, true)"
            />
            <RowAction
              :icon="X"
              :label="t('projects.lspDecline')"
              :name="row.id"
              tone="danger"
              :disabled="consenting"
              :data-testid="`lsp-decline-${row.id}`"
              @click="consent(row, false)"
            />
          </RowActions>
          <!-- A declined entry keeps its row and gets the way back: 允许
               records a new decision for it. -->
          <RowActions v-else-if="row.status === 'denied'">
            <RowAction
              :icon="Check"
              :label="t('projects.lspAllow')"
              :name="row.id"
              tone="ok"
              :disabled="consenting"
              :data-testid="`lsp-undecline-${row.id}`"
              @click="consent(row, true)"
            />
            <RowActionSlot />
          </RowActions>
          <RowActions v-else>
            <RowActionSlot />
            <RowActionSlot />
          </RowActions>
        </template>
      </DataTable>

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      <ListNote>{{ t('projects.lspDescription') }}</ListNote>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Check, RotateCw, X } from 'lucide-vue-next'
import type { AxiosError } from 'axios'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActionSlot from '@/components/common/RowActionSlot.vue'
import RowActions from '@/components/common/RowActions.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import TablePager from '@/components/common/TablePager.vue'
import { useServerList } from '@/composables/useServerList'
import { getErrorMessage, forebrainApi, type ProjectLspRow, type ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The project's language servers: what <root>/.forebrain/lsp_servers.yaml
 * declares for this project's sessions. Editing the file is the owner's move
 * on disk; this tab previews the effect, holds the trust gate, and records
 * the per-entry confirmations.
 *
 * One table, three kinds of row (plan 075): an entry awaiting its decision, an
 * allowed one and a declined one, in that order — the decision is the row's own
 * action, and only the pending entries carry a summary, since the consent store
 * knows the others by id alone. A declined row does not disappear: 允许 on it
 * records a new decision, which is the only way back.
 */
const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()

const { t } = useI18n()

const trusting = ref(false)
const consenting = ref(false)
const writeError = ref('')

const { rows, total, page, pageSize, q, loading, error: listError, reload } = useServerList<ProjectLspRow>(
  (query) => forebrainApi.projectLspPage(props.projectId, query),
  { pageSize: 10 },
)

/**
 * The table's columns, at the design's widths: the summary takes the room the
 * others leave. Nothing sorts — the list endpoint ignores `sort`.
 */
const columns: DataColumn[] = [
  { id: 'id', header: t('projects.lspColServer'), size: 260 },
  { id: 'summary', header: t('projects.lspColSummary'), cellClass: 'text-[var(--forebrain-text-2)]' },
  { id: 'status', header: t('projects.lspColStatus'), size: 100 },
  { id: 'actions', header: t('projects.colActions'), size: 88, headerClass: 'text-center', cellClass: 'text-right' },
]

const error = computed(() => listError.value || writeError.value)

/** The project's "always allow" switch, read beside the table it decides. */
const autoAllow = ref(false)
const switching = ref(false)

async function loadAutoAllow(): Promise<void> {
  try {
    const record = await forebrainApi.projectLsp(props.projectId)
    autoAllow.value = record.autoAllow === true
  } catch {
    // The switch keeps its last read; the table's own read reports list
    // errors, and a switch without a value yet stays off.
  }
}

async function setAutoAllow(on: boolean): Promise<void> {
  if (switching.value) return
  switching.value = true
  writeError.value = ''
  try {
    await forebrainApi.projectServersAutoAllow(props.projectId, on)
    autoAllow.value = on
    // The switch decides whether pending entries are rows at all, so the
    // table is re-read with it.
    await reload()
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
  } finally {
    switching.value = false
  }
}

watch(() => props.projectId, () => {
  void loadAutoAllow()
}, { immediate: true })

// The gate follows the project row the shell loaded (its trust decision is
// part of the detail read); the trust action here moves it forward.
const trusted = ref(false)
watch(() => props.project?.trusted, (value) => {
  trusted.value = Boolean(value)
}, { immediate: true })

function refresh(): void {
  void reload()
}

function statusLabel(status: ProjectLspRow['status']): string {
  switch (status) {
    case 'allowed':
      return t('projects.lspStatusAllowed')
    case 'denied':
      return t('projects.lspStatusDenied')
    default:
      return t('projects.lspStatusPending')
  }
}

function statusVariant(status: ProjectLspRow['status']): 'success' | 'warning' | 'gray' {
  switch (status) {
    case 'allowed':
      return 'success'
    case 'denied':
      return 'gray'
    default:
      return 'warning'
  }
}

async function trust() {
  if (trusting.value) return
  trusting.value = true
  writeError.value = ''
  try {
    await forebrainApi.projectUpdate(props.projectId, { trust: true })
    // The gate's own watcher reloads the table once trust moves.
    trusted.value = true
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
  } finally {
    trusting.value = false
  }
}

// One decision per click: the request names the entry it is about (`decide`)
// together with the fingerprint the row was rendered from, and the endpoint
// changes that entry's decision and nothing else — which is also how a
// declined entry is allowed again. A 409 means the file moved under the page:
// nothing was recorded, the server text stays up, and the rows reload so the
// entry reappears with its new summary and fingerprint.
async function consent(row: ProjectLspRow, allow: boolean) {
  if (consenting.value) return
  consenting.value = true
  writeError.value = ''
  try {
    await forebrainApi.projectLspConsent(props.projectId, allow ? [row.id] : [], [row.id], [
      { name: row.id, fingerprint: row.fingerprint },
    ])
    await reload()
  } catch (cause) {
    writeError.value = getErrorMessage(cause)
    if ((cause as AxiosError)?.response?.status === 409) await reload()
  } finally {
    consenting.value = false
  }
}

// The listing is only asked once the project is trusted: its entries come from
// the project's own file.
watch(trusted, (value) => {
  if (value) void reload()
}, { immediate: true })

</script>
