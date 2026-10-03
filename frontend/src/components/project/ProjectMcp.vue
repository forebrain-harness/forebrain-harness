<template>
  <div class="mx-auto max-w-4xl space-y-5">
    <section class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex items-center gap-2">
          <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('projects.mcpTitle') }}</div>
          <span class="scope-badge">{{ t('scope.project') }}</span>
        </div>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
          {{ loading ? t('common.loading') : t('common.refresh') }}
        </button>
      </div>
      <p class="mt-1 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('projects.mcpDescription') }}</p>

      <p v-if="error" class="mt-3 text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

      <!-- Trust gate: until the project is trusted, nothing from its directory
           loads — the button is the gate, exactly as the terminal's. -->
      <div v-if="!trusted" class="mt-4 rounded-xl border border-[var(--forebrain-brand-border)] bg-[var(--forebrain-brand-soft)] p-4">
        <div class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.trustTitle') }}</div>
        <p class="mt-1 text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.trustHint') }}</p>
        <button type="button" class="forebrain-btn forebrain-btn-primary mt-3 text-xs" :disabled="trusting" data-testid="project-trust" @click="trust">
          {{ trusting ? t('common.loading') : t('projects.trustLabel') }}
        </button>
      </div>

      <template v-else>
        <div v-if="!records.servers.length && !records.notApplied.length" class="mt-6 rounded-xl border border-dashed border-[var(--forebrain-divider)] px-6 py-10 text-center">
          <p class="text-[13px] text-[var(--forebrain-muted-text)]">{{ t('projects.mcpEmpty') }}</p>
          <p class="mt-2 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('projects.mcpAddHint') }}</p>
        </div>

        <ul v-if="records.servers.length" class="mt-4 space-y-2">
          <li v-for="server in records.servers" :key="server.name" class="flex items-center justify-between gap-3 rounded-xl border border-[var(--forebrain-divider)] px-4 py-3">
            <div class="min-w-0">
              <div class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ server.name }}</div>
              <div class="mt-0.5 font-mono text-[11px] text-[var(--forebrain-muted-text)]">{{ server.transport }} · {{ server.scope }}</div>
            </div>
          </li>
        </ul>

        <div v-if="records.notApplied.length" class="mt-4">
          <div class="text-[12px] font-medium text-[var(--forebrain-text-2)]">{{ t('projects.mcpNotAppliedTitle') }}</div>
          <ul class="mt-2 space-y-1">
            <li v-for="item in records.notApplied" :key="item.name" class="text-[12px] text-[var(--forebrain-muted-text)]">
              {{ item.name }} — {{ item.reason }}
            </li>
          </ul>
        </div>

        <p class="mt-4 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('projects.mcpAddHint') }}</p>
      </template>
    </section>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { getErrorMessage, forebrainApi, type ProjectMcpRecord, type ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'

/**
 * The project's MCP services: what <root>/.forebrain/mcp_servers.yaml would
 * load for this project's sessions. Editing the file is the owner's move on
 * disk; this tab previews the effect and holds the trust gate.
 */
const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()

const { t } = useI18n()

const emptyRecords = (): ProjectMcpRecord => ({ servers: [], overriddenGlobal: [], notApplied: [], pendingConsent: [] })
const records = ref<ProjectMcpRecord>(emptyRecords())
const loading = ref(false)
const trusting = ref(false)
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
    records.value = await forebrainApi.projectMcp(props.projectId)
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

onMounted(() => {
  void load()
})
</script>

<style scoped>
.scope-badge {
  display: inline-flex;
  align-items: center;
  border-radius: 9999px;
  padding: 2px 10px;
  font-size: 11px;
  font-weight: 500;
  background: var(--forebrain-brand-1);
  color: var(--forebrain-on-brand);
}
</style>
