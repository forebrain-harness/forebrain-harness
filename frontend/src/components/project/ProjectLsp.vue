<template>
  <div class="mx-auto max-w-4xl space-y-5">
    <CardComponent>
      <template #header>
        <div class="flex w-full flex-wrap items-center justify-between gap-3">
        <div class="flex items-center gap-2">
          <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('projects.lspTitle') }}</div>
          <ScopeBadge type="project" :label="t('scope.project')" />
        </div>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
          {{ loading ? t('common.loading') : t('common.refresh') }}
        </button>
        </div>
      </template>
      <p class="text-[12px] text-[var(--forebrain-muted-text)]">{{ t('projects.lspDescription') }}</p>

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
        <!-- Entries awaiting the per-entry confirmation: allow applies the
             entry from now on, decline records it so it asks again only
             when the file changes. -->
        <div v-if="records.pending.length" class="rounded-lg border border-[rgba(180,140,60,0.4)] bg-[rgba(180,140,60,0.07)] p-3" data-testid="project-lsp-pending">
          <p class="text-[12px] font-medium text-[var(--forebrain-text)]">{{ t('projects.lspPendingTitle') }}</p>
          <ul class="mt-2 space-y-1">
            <li v-for="row in records.pending" :key="row.id" class="flex flex-wrap items-center justify-between gap-2 rounded-lg px-2 py-1.5 text-[12px] odd:bg-[var(--forebrain-surface-soft)]">
              <div class="min-w-0">
                <span class="font-mono text-[var(--forebrain-text)]">{{ row.id }}</span>
                <span class="ml-2 text-[var(--forebrain-muted-text)]">{{ row.summary }}</span>
              </div>
              <div class="flex shrink-0 gap-1">
                <button type="button" class="forebrain-btn forebrain-btn-primary h-7 px-2 text-[11px]" :disabled="consenting" :data-testid="`lsp-allow-${row.id}`" @click="consent(row.id, true)">{{ t('projects.lspAllow') }}</button>
                <button type="button" class="forebrain-btn forebrain-btn-ghost h-7 px-2 text-[11px]" :disabled="consenting" :data-testid="`lsp-decline-${row.id}`" @click="consent(row.id, false)">{{ t('projects.lspDecline') }}</button>
              </div>
            </li>
          </ul>
        </div>

        <div v-if="records.allowed.length">
          <div class="text-[12px] font-medium text-[var(--forebrain-text-2)]">{{ t('projects.lspAllowedTitle') }}</div>
          <ul class="mt-2 space-y-1">
            <li v-for="id in records.allowed" :key="id" class="rounded-lg px-2 py-1.5 font-mono text-[12px] odd:bg-[var(--forebrain-surface-soft)]">{{ id }}</li>
          </ul>
        </div>

        <div v-if="records.denied.length">
          <div class="text-[12px] font-medium text-[var(--forebrain-text-2)]">{{ t('projects.lspDeniedTitle') }}</div>
          <ul class="mt-2 space-y-1">
            <li v-for="id in records.denied" :key="id" class="rounded-lg px-2 py-1.5 font-mono text-[12px] odd:bg-[var(--forebrain-surface-soft)]">{{ id }}</li>
          </ul>
        </div>

        <div v-if="!records.pending.length && !records.allowed.length && !records.denied.length && !records.notes.length" class="rounded-lg border border-dashed border-[var(--forebrain-divider)] px-6 py-10 text-center">
          <p class="text-[13px] text-[var(--forebrain-muted-text)]">{{ t('projects.lspEmpty') }}</p>
          <p class="mt-2 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('projects.lspAddHint') }}</p>
        </div>

        <div v-if="records.notes.length" class="border-t border-[var(--forebrain-divider)] pt-2">
          <div class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('projects.lspNotesTitle') }}</div>
          <ul class="mt-1 space-y-0.5">
            <li v-for="(note, i) in records.notes" :key="i" class="text-[11px] text-[var(--forebrain-text-2)]">{{ note }}</li>
          </ul>
        </div>
      </template>
    </CardComponent>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import CardComponent from '@/components/common/CardComponent.vue'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import { getErrorMessage, forebrainApi, type ProjectLspRecord, type ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The project's language servers: what <root>/.forebrain/lsp_servers.yaml
 * declares for this project's sessions. Editing the file is the owner's move
 * on disk; this tab previews the effect, holds the trust gate, and records
 * the per-entry confirmations.
 */
const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()

const { t } = useI18n()

const emptyRecords = (): ProjectLspRecord => ({ trusted: false, pending: [], allowed: [], denied: [], notes: [] })
const records = ref<ProjectLspRecord>(emptyRecords())
const loading = ref(false)
const trusting = ref(false)
const consenting = ref(false)
const error = ref('')

// The gate follows the project row the shell loaded (its trust decision is
// part of the detail read); the trust action here moves it forward.
const trusted = ref(false)
watch(() => props.project?.trusted, (value) => {
  trusted.value = Boolean(value)
}, { immediate: true })

async function load() {
  loading.value = true
  error.value = ''
  try {
    records.value = await forebrainApi.projectLsp(props.projectId)
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    loading.value = false
  }
}

async function trust() {
  if (trusting.value) return
  trusting.value = true
  error.value = ''
  try {
    await forebrainApi.projectUpdate(props.projectId, { trust: true })
    trusted.value = true
    await load()
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    trusting.value = false
  }
}

// One decision per click: allowing (or declining) one entry records a
// decision for every other pending entry as declined — the same semantics
// the terminal's startup prompt applies — so the list always reloads.
async function consent(id: string, allow: boolean) {
  if (consenting.value) return
  consenting.value = true
  error.value = ''
  try {
    await forebrainApi.projectLspConsent(props.projectId, allow ? [id] : [])
    await load()
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    consenting.value = false
  }
}

onMounted(() => {
  void load()
})
</script>
