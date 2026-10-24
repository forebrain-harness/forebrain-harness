<template>
  <div class="mx-auto max-w-4xl space-y-5">
    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

    <!-- Trust gate: until the project is trusted, nothing from its directory
         loads — the button is the gate, exactly as the terminal's. -->
    <div v-if="!trusted" class="rounded-lg border border-[var(--forebrain-brand-border)] bg-[var(--forebrain-brand-soft)] p-4">
      <div class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('projects.trustTitle') }}</div>
      <p class="mt-1 text-[12px] text-[var(--forebrain-text-2)]">{{ t('projects.trustHint') }}</p>
      <Button type="button" class="forebrain-btn forebrain-btn-primary mt-3 h-auto text-xs" :disabled="trusting" data-testid="project-trust" @click="trust">
        {{ trusting ? t('common.loading') : t('projects.trustLabel') }}
      </Button>
    </div>

    <!-- Trusted: the three layers this project loads, in the order the merge
         resolves them, with this project's own entries editable here and a
         pending entry asking for its decision in its own row. -->
    <McpServersTable v-else ref="mcpTable" view="project" :project-id="projectId" :agent-id="activePrimaryId">
      <template #toolbar-left>
        <!-- The project's "always allow" switch, the same one the LSP tab
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
      </template>
    </McpServersTable>
  </div>
</template>

<script setup lang="ts">
/**
 * A project's MCP tab (plan 074): what this project's sessions would load — its
 * own file first, then the primary agent's layer, then the shared one, a
 * same-named entry replaced whole by the higher layer — under the trust gate
 * that decides whether the project's file applies at all.
 *
 * Only the project's own entries are added, edited and deleted here; the agent's
 * and the shared ones are listed read-only, each saying where it is edited. An
 * entry the file configures but nobody has confirmed yet is a row like any
 * other, offering 允许 / 拒绝 rather than an edit.
 */
import { ref, watch } from 'vue'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import McpServersTable from '@/components/mcp/McpServersTable.vue'
import SwitchComponent from '@/components/common/SwitchComponent.vue'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'
import { forebrainApi, getErrorMessage, type ProjectRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const props = defineProps<{ project: ProjectRecord | null; projectId: string }>()

const { t } = useI18n()
const { activeId: activePrimaryId } = usePrimaryAgents()

const trusting = ref(false)
const error = ref('')

/** The table, whose reload the switch's own write needs: the switch decides
 * whether pending entries are rows at all. */
const mcpTable = ref<InstanceType<typeof McpServersTable> | null>(null)

/** The project's "always allow" switch, read beside the table it decides. */
const autoAllow = ref(false)
const switching = ref(false)

async function loadAutoAllow(): Promise<void> {
  try {
    const record = await forebrainApi.projectMcp(props.projectId)
    autoAllow.value = record.autoAllow === true
  } catch {
    // The switch keeps its last read; the table's own read reports list
    // errors, and a switch without a value yet stays off.
  }
}

async function setAutoAllow(on: boolean): Promise<void> {
  if (switching.value) return
  switching.value = true
  error.value = ''
  try {
    await forebrainApi.projectServersAutoAllow(props.projectId, on)
    autoAllow.value = on
    await mcpTable.value?.reload()
  } catch (cause) {
    error.value = getErrorMessage(cause)
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
watch(
  () => props.project?.trusted,
  (value) => {
    trusted.value = Boolean(value)
  },
  { immediate: true },
)

async function trust(): Promise<void> {
  if (trusting.value) return
  trusting.value = true
  error.value = ''
  try {
    await forebrainApi.projectUpdate(props.projectId, { trust: true })
    trusted.value = true
  } catch (cause) {
    error.value = getErrorMessage(cause)
  } finally {
    trusting.value = false
  }
}
</script>
