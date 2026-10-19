<template>
  <div class="mx-auto max-w-4xl space-y-4">
    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

    <!-- Trust gate: language-server entries load only in trusted,
         version-controlled projects — the button is the gate, exactly as
         the MCP tab's. -->
    <div v-if="!trusted" class="rounded-lg border border-[var(--forebrain-brand-border)] bg-[var(--forebrain-brand-soft)] p-4">
      <div class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.trustTitle') }}</div>
      <p class="mt-1 text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.trustHint') }}</p>
      <button type="button" class="forebrain-btn forebrain-btn-primary mt-3 text-xs" :disabled="trusting" data-testid="project-trust" @click="trust">
        {{ trusting ? t('common.loading') : t('projects.trustLabel') }}
      </button>
    </div>

    <template v-else>
      <ListToolbar>
        <template #left>
          <input
            v-model="q"
            type="search"
            :placeholder="t('projects.lspSearchPlaceholder')"
            class="forebrain-field h-[34px]"
            style="max-width: 240px"
            data-testid="project-lsp-search"
          />
        </template>
        <template #actions>
          <button type="button" class="forebrain-btn forebrain-btn-ghost gap-1.5 text-xs" :disabled="loading" data-testid="project-lsp-refresh" @click="refresh">
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
            <col style="width: 260px" />
            <col />
            <col style="width: 100px" />
            <col style="width: 88px" />
          </colgroup>
          <thead>
            <tr>
              <th>{{ t('projects.lspColServer') }}</th>
              <th>{{ t('projects.lspColSummary') }}</th>
              <th>{{ t('projects.lspColStatus') }}</th>
              <th class="text-center">{{ t('projects.colActions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in rows" :key="row.id" :data-lsp-row="row.id">
              <td>
                <span class="forebrain-cell-1 font-mono" v-tip:top.trunc="row.id">{{ row.id }}</span>
              </td>
              <td class="text-[var(--forebrain-text-2)]">
                <span class="forebrain-cell-1" v-tip:top.trunc="row.summary">{{ row.summary || '—' }}</span>
              </td>
              <td>
                <BadgeComponent
                  :variant="statusVariant(row.status)"
                  v-tip:top="row.status === 'denied' ? t('projects.lspDeniedTip') : ''"
                >{{ statusLabel(row.status) }}</BadgeComponent>
              </td>
              <td class="text-right">
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
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
      <ListNote>{{ t('projects.lspDescription') }}</ListNote>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Check, RotateCw, X } from 'lucide-vue-next'
import type { AxiosError } from 'axios'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActionSlot from '@/components/common/RowActionSlot.vue'
import RowActions from '@/components/common/RowActions.vue'
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

const error = computed(() => listError.value || writeError.value)

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
