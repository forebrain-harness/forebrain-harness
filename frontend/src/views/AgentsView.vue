<template>
  <div class="min-h-full bg-[var(--forebrain-content-gradient)] px-4 py-6">
    <div class="mx-auto grid max-w-6xl gap-4 lg:grid-cols-[minmax(0,1.15fr)_minmax(320px,0.85fr)]">
      <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-card-gradient)] p-5 shadow-[var(--forebrain-doc-shadow)]">
        <div class="mb-4 flex items-start justify-between gap-3">
          <div>
            <h1 class="font-serif text-2xl font-semibold text-[var(--forebrain-text)]">{{ t('agents.pageTitle') }}</h1>
            <p class="mt-1 max-w-2xl text-sm leading-relaxed text-[var(--forebrain-text-2)]">
              {{ t('agents.pageDescription') }}
            </p>
          </div>
          <button class="forebrain-btn forebrain-btn-ghost shrink-0 text-xs" type="button" @click="refreshAll">
            {{ t('common.refresh') }}
          </button>
        </div>
        <div v-if="primaryLoading" class="rounded-xl border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
          {{ t('common.loading') }}
        </div>
        <div v-else-if="primaryError" class="rounded-xl border border-[rgba(160,70,70,0.36)] bg-[rgba(160,70,70,0.08)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">
          {{ primaryError }}
        </div>
        <div v-else-if="!primaryRecords.length" class="rounded-xl border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
          {{ t('agents.noPrimaryAgents') }}
        </div>
        <div v-else class="space-y-3">
          <article
            v-for="agent in primaryRecords"
            :key="agent.id"
            class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] p-4"
          >
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <div class="flex min-w-0 flex-wrap items-center gap-2">
                  <span class="truncate text-base font-semibold text-[var(--forebrain-text)]">{{ agent.id }}</span>
                  <span
                    v-if="agent.id === activeId"
                    class="rounded-full bg-[var(--forebrain-brand-soft)] px-2 py-0.5 text-[10px] uppercase tracking-wide text-[var(--forebrain-brand-1)]"
                  >
                    {{ t('agents.active') }}
                  </span>
                </div>
                <div class="mt-2 space-y-1 break-all font-mono text-[11px] text-[var(--forebrain-muted-text)]">
                  <div>{{ t('agents.workspace') }}: {{ agent.workspaceRoot || '-' }}</div>
                  <div>{{ t('agents.skills') }}: {{ agent.privateSkillsRoot || '-' }}</div>
                </div>
              </div>
              <button
                v-if="agent.id !== activeId"
                class="forebrain-btn forebrain-btn-primary shrink-0 text-xs"
                type="button"
                :disabled="primaryLoading"
                @click="switchAgent(agent.id)"
              >
                {{ t('agents.switch') }}
              </button>
            </div>
          </article>
        </div>
      </section>
      <div class="space-y-4">
        <AgentRosterPanel
          :title="t('agents.liveTitle')"
          :records="rosterRecords"
          :loading="rosterLoading"
          :error="rosterError"
          @view="viewRoster"
          @cancel="cancelRoster"
          @cancel-all="cancelAll"
        />
        <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-glass)] p-5">
          <h2 class="text-sm font-semibold text-[var(--forebrain-text)]">{{ t('agents.sharedRoots') }}</h2>
          <div class="mt-3 space-y-1 break-all font-mono text-[11px] text-[var(--forebrain-muted-text)]">
            <div v-if="!active?.sharedSkillsRoots?.length">{{ t('agents.skills') }}: -</div>
            <div v-for="root in active?.sharedSkillsRoots || []" :key="root">{{ t('agents.skills') }}: {{ root }}</div>
          </div>
        </section>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import AgentRosterPanel from '@/components/AgentRosterPanel.vue'
import { agentRosterViewTarget, useAgentRoster } from '@/composables/useAgentRoster'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'
import type { AgentRosterRow } from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()
const router = useRouter()
const {
  records: primaryRecords,
  active,
  activeId,
  loading: primaryLoading,
  error: primaryError,
  refreshToken,
  loadPrimaryAgents,
  switchPrimaryAgent,
} = usePrimaryAgents()
const {
  records: rosterRecords,
  loading: rosterLoading,
  error: rosterError,
  loadRoster,
  cancelRoster,
  cancelAll,
} = useAgentRoster()

async function refreshAll() {
  await Promise.all([loadPrimaryAgents(), loadRoster()])
}

async function switchAgent(id: string) {
  await switchPrimaryAgent(id)
  await loadRoster()
}

function viewRoster(row: AgentRosterRow) {
  const target = agentRosterViewTarget(row)
  if (target) void router.push(target)
}

onMounted(refreshAll)

watch(refreshToken, () => {
  void loadRoster()
})
</script>
