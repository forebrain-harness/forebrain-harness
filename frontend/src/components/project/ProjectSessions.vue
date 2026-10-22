<template>
  <div class="mx-auto max-w-4xl space-y-4">
    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

    <ListToolbar>
      <template #left>
        <Input
          v-model="q"
          type="search"
          :placeholder="t('projects.sessionSearchPlaceholder')"
          class="forebrain-field h-[34px] w-auto"
          style="max-width: 240px"
          data-testid="project-sessions-search"
        />
      </template>
      <template #actions>
        <Button type="button" class="forebrain-btn forebrain-btn-primary h-auto gap-1.5 text-xs" :disabled="creating" data-testid="project-session-create" @click="createSession">
          <Plus class="size-3.5" aria-hidden="true" />{{ creating ? t('common.loading') : t('projects.newProjectSession') }}
        </Button>
      </template>
    </ListToolbar>

    <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
      {{ t('projects.noSessions') }}
    </div>
    <DataTable
      v-else
      :columns="columns"
      :rows="rows"
      :row-key="(row) => row.id"
      :row-attrs="(row) => ({ 'data-session-row': row.id })"
      :sorting="{ key: sort, order }"
      @update:sorting="sortBy($event.key, $event.order)"
    >
      <template #cell-title="{ row }">
        <RouterLink
          :to="{ path: '/', query: { session: row.id } }"
          class="forebrain-cell-1 text-[var(--forebrain-brand-1)] hover:underline"
          v-tip:top.trunc="titleOf(row)"
        >
          {{ titleOf(row) }}
        </RouterLink>
      </template>
      <template #cell-model="{ row }">
        <span class="forebrain-cell-1" v-tip:top.trunc="row.model">{{ row.model || '—' }}</span>
      </template>
      <template #cell-messages="{ row }">{{ row.messageCount ?? 0 }}</template>
      <template #cell-heartbeat="{ row }">
        <BadgeComponent v-if="row.heartbeat && !heartbeatPaused(row)">
          {{ t('heartbeat.every', { minutes: heartbeatMinutes(row) }) }}
        </BadgeComponent>
        <BadgeComponent v-else-if="row.heartbeat" variant="gray">{{ t('heartbeat.paused') }}</BadgeComponent>
        <span v-else class="text-[var(--forebrain-text-2)]">—</span>
      </template>
      <template #cell-created="{ row }">{{ formatDateTime(row.createdAt) }}</template>
      <template #cell-updated="{ row }">{{ formatDateTime(row.updatedAt) }}</template>
      <template #cell-actions="{ row }">
        <RowActions>
          <RowAction
            :icon="MessageSquare"
            :label="t('projects.openSession')"
            :name="titleOf(row)"
            :data-testid="`project-session-open-${row.id}`"
            @click="openSession(row.id)"
          />
        </RowActions>
      </template>
    </DataTable>

    <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
    <ListNote>{{ t('projects.projectSessionsNote') }}</ListNote>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { MessageSquare, Plus } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import DataTable, { type DataColumn } from '@/components/common/DataTable.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import TablePager from '@/components/common/TablePager.vue'
import { useServerList } from '@/composables/useServerList'
import { getErrorMessage, forebrainApi, type ProjectSessionRow } from '@/lib/api'
import { formatDateTime } from '@/lib/time'
import { useI18n } from '@/locales'

/**
 * Only this project's sessions: the drawer keeps non-project conversations,
 * so a project's work lives behind its own door (owner decision D6).
 *
 * The table is the server's (plan 075): the search narrows it by title, the two
 * time columns order it (newest first, update time by default), and the model,
 * the message count and the heartbeat are the server's own derived facts — a
 * session that never pinned a model answers an empty one, which the table draws
 * as an em dash.
 */
const props = defineProps<{ project: unknown; projectId: string }>()

const { t } = useI18n()
const router = useRouter()

const { rows, total, page, pageSize, q, sort, order, loading, error, reload } = useServerList<ProjectSessionRow>(
  (query) => forebrainApi.projectSessionsPage(props.projectId, query),
  { pageSize: 10, sort: 'updated', order: 'desc' },
)

/**
 * The table's columns, at the design's widths: the title takes the room the
 * others leave. 创建时间 and 更新时间 sort — the sessions listing is one of the
 * three endpoints that read `sort`, newest first on the first click.
 */
const columns: DataColumn[] = [
  { id: 'title', header: t('projects.colSessionTitle') },
  { id: 'model', header: t('projects.colModel'), size: 150, cellClass: 'font-mono text-[var(--forebrain-text-2)]' },
  { id: 'messages', header: t('projects.colMessages'), size: 64, headerClass: 'text-right', cellClass: 'forebrain-num text-right' },
  { id: 'heartbeat', header: t('projects.colHeartbeat'), size: 100 },
  {
    id: 'created',
    header: t('projects.colCreated'),
    size: 150,
    enableSorting: true,
    sortDescFirst: true,
    cellClass: 'forebrain-num font-mono text-[var(--forebrain-text-2)]',
  },
  {
    id: 'updated',
    header: t('projects.colUpdated'),
    size: 150,
    enableSorting: true,
    sortDescFirst: true,
    cellClass: 'forebrain-num font-mono text-[var(--forebrain-text-2)]',
  },
  { id: 'actions', header: t('projects.colActions'), size: 56, headerClass: 'text-center', cellClass: 'text-right' },
]

const creating = ref(false)

function titleOf(session: ProjectSessionRow): string {
  return session.title || t('projects.untitledSession')
}

function heartbeatPaused(session: ProjectSessionRow): boolean {
  return Boolean(session.heartbeat?.paused)
}

function heartbeatMinutes(session: ProjectSessionRow): number {
  return Math.max(1, Math.round((session.heartbeat?.intervalSeconds ?? 0) / 60))
}

/** A header click orders the column the way the server does, from page one. */
function sortBy(key: string, next: 'asc' | 'desc'): void {
  sort.value = key
  order.value = next
}

async function createSession() {
  if (creating.value) return
  creating.value = true
  try {
    const created = await forebrainApi.projectSessionCreate(props.projectId, '')
    await router.push({ path: '/', query: { session: created.id } })
  } catch (cause) {
    error.value = getErrorMessage(cause)
    creating.value = false
  }
}

function openSession(id: string) {
  void router.push({ path: '/', query: { session: id } })
}

onMounted(() => {
  void reload()
})
</script>
