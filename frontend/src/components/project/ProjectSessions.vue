<template>
  <div class="mx-auto max-w-4xl space-y-4">
    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

    <ListToolbar>
      <template #left>
        <input
          v-model="q"
          type="search"
          :placeholder="t('projects.sessionSearchPlaceholder')"
          class="forebrain-field h-[34px]"
          style="max-width: 240px"
          data-testid="project-sessions-search"
        />
      </template>
      <template #actions>
        <button type="button" class="forebrain-btn forebrain-btn-primary gap-1.5 text-xs" :disabled="creating" data-testid="project-session-create" @click="createSession">
          <Plus class="size-3.5" aria-hidden="true" />{{ creating ? t('common.loading') : t('projects.newProjectSession') }}
        </button>
      </template>
    </ListToolbar>

    <div v-if="loading && !rows.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div v-else-if="!rows.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
      {{ t('projects.noSessions') }}
    </div>
    <div v-else class="forebrain-table-wrap">
      <table class="forebrain-table forebrain-table--fixed">
        <colgroup>
          <col />
          <col style="width: 150px" />
          <col style="width: 64px" />
          <col style="width: 100px" />
          <col style="width: 150px" />
          <col style="width: 150px" />
          <col style="width: 56px" />
        </colgroup>
        <thead>
          <tr>
            <th>{{ t('projects.colSessionTitle') }}</th>
            <th>{{ t('projects.colModel') }}</th>
            <th class="text-right">{{ t('projects.colMessages') }}</th>
            <th>{{ t('projects.colHeartbeat') }}</th>
            <SortHeader
              :label="t('projects.colCreated')"
              sort-key="created"
              :active="sort === 'created'"
              :order="order"
              first="desc"
              @sort="sortBy"
            />
            <SortHeader
              :label="t('projects.colUpdated')"
              sort-key="updated"
              :active="sort === 'updated'"
              :order="order"
              first="desc"
              @sort="sortBy"
            />
            <th class="text-right">{{ t('projects.colActions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="session in rows" :key="session.id" :data-session-row="session.id">
            <td>
              <RouterLink
                :to="{ path: '/', query: { session: session.id } }"
                class="forebrain-cell-1 text-[var(--forebrain-brand-1)] hover:underline"
                v-tip:top.trunc="titleOf(session)"
              >
                {{ titleOf(session) }}
              </RouterLink>
            </td>
            <td class="font-mono text-[var(--forebrain-text-2)]">
              <span class="forebrain-cell-1" v-tip:top.trunc="session.model">{{ session.model || '—' }}</span>
            </td>
            <td class="forebrain-num text-right">{{ session.messageCount ?? 0 }}</td>
            <td>
              <BadgeComponent v-if="session.heartbeat && !heartbeatPaused(session)">
                {{ t('heartbeat.every', { minutes: heartbeatMinutes(session) }) }}
              </BadgeComponent>
              <BadgeComponent v-else-if="session.heartbeat" variant="gray">{{ t('heartbeat.paused') }}</BadgeComponent>
              <span v-else class="text-[var(--forebrain-text-2)]">—</span>
            </td>
            <td class="forebrain-num font-mono text-[var(--forebrain-text-2)]">{{ formatDateTime(session.createdAt) }}</td>
            <td class="forebrain-num font-mono text-[var(--forebrain-text-2)]">{{ formatDateTime(session.updatedAt) }}</td>
            <td class="text-right">
              <RowActions>
                <RowAction
                  :icon="MessageSquare"
                  :label="t('projects.openSession')"
                  :name="titleOf(session)"
                  :data-testid="`project-session-open-${session.id}`"
                  @click="openSession(session.id)"
                />
              </RowActions>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <TablePager :total="total" :page="page" :page-size="pageSize" @update:page="page = $event" @update:page-size="pageSize = $event" />
    <ListNote>{{ t('projects.projectSessionsNote') }}</ListNote>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { MessageSquare, Plus } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ListNote from '@/components/common/ListNote.vue'
import ListToolbar from '@/components/common/ListToolbar.vue'
import RowAction from '@/components/common/RowAction.vue'
import RowActions from '@/components/common/RowActions.vue'
import SortHeader from '@/components/common/SortHeader.vue'
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
