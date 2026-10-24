<template>
  <ForebrainModal :open="open" size="md" :title="t('chat.projectServersTitle')" @close="emit('close')">
    <div data-testid="project-servers-consent">
      <!-- Only the count moves between openings; everything else the dialog
           has to say is the list itself. -->
      <p class="text-[12px] text-[var(--forebrain-text-2)]">{{ t('chat.projectServersCount', { n: rows.length }) }}</p>

      <!-- A group heading only for a kind that has entries, MCP first — the
           order the TUI panel reads in. -->
      <div v-if="mcp.length" class="mt-3">
        <div class="text-[12px] font-medium text-[var(--forebrain-text)]">{{ t('chat.projectServersMcp') }}</div>
        <div
          v-for="row in mcp"
          :key="`mcp-${row.name}`"
          class="border-b border-[var(--forebrain-divider)] py-2 last:border-b-0"
          :data-testid="`project-servers-row-${row.name}`"
        >
          <div class="grid grid-cols-[minmax(96px,auto)_1fr] items-center gap-3">
            <Checkbox
              :model-value="checked[row.name] !== false"
              :disabled="busy"
              :aria-label="row.name"
              :data-testid="`project-servers-check-${row.name}`"
              @update:model-value="(value) => toggleEntry(row.name, value === true)"
            />
            <!-- The checkbox is a button, so it stands outside the text: the
                 name and what the entry would run read as two columns, the
                 command or address in mono, muted. -->
            <div class="flex min-w-0 items-baseline gap-3">
              <span class="min-w-0 truncate text-[13px] text-[var(--forebrain-text)]">{{ row.name }}</span>
              <code class="min-w-0 flex-1 truncate font-mono text-[12px] text-[var(--forebrain-text-2)]">{{ row.detail }}</code>
            </div>
          </div>
        </div>
      </div>

      <div v-if="lsp.length" class="mt-3">
        <div class="text-[12px] font-medium text-[var(--forebrain-text)]">{{ t('chat.projectServersLsp') }}</div>
        <div
          v-for="row in lsp"
          :key="`lsp-${row.name}`"
          class="border-b border-[var(--forebrain-divider)] py-2 last:border-b-0"
          :data-testid="`project-servers-row-${row.name}`"
        >
          <div class="grid grid-cols-[minmax(96px,auto)_1fr] items-center gap-3">
            <Checkbox
              :model-value="checked[row.name] !== false"
              :disabled="busy"
              :aria-label="row.name"
              :data-testid="`project-servers-check-${row.name}`"
              @update:model-value="(value) => toggleEntry(row.name, value === true)"
            />
            <div class="flex min-w-0 items-baseline gap-3">
              <span class="min-w-0 truncate text-[13px] text-[var(--forebrain-text)]">{{ row.name }}</span>
              <code class="min-w-0 flex-1 truncate font-mono text-[12px] text-[var(--forebrain-text-2)]">{{ row.detail }}</code>
            </div>
          </div>
        </div>
      </div>

      <!-- The switch row, apart from the list: checked it means every entry
           above is checked too. -->
      <div class="mt-3 flex items-center gap-3 border-t border-[var(--forebrain-divider)] pt-3">
        <Checkbox
          :model-value="alwaysAllow"
          :disabled="busy"
          :aria-label="t('chat.projectServersAlways')"
          data-testid="project-servers-always"
          @update:model-value="(value) => toggleAlways(value === true)"
        />
        <span class="text-[13px] text-[var(--forebrain-text)]">{{ t('chat.projectServersAlways') }}</span>
      </div>

      <p v-if="error" class="mt-2 text-[12px] text-[var(--forebrain-danger)]" role="alert">{{ error }}</p>
    </div>

    <template #footer>
      <div class="flex justify-end gap-2">
        <Button
          type="button"
          class="forebrain-btn forebrain-btn-ghost h-auto text-xs"
          :disabled="busy"
          data-testid="project-servers-decline-all"
          @click="emit('declineAll')"
        >
          {{ t('chat.projectServersDeclineAll') }}
        </Button>
        <Button
          type="button"
          class="forebrain-btn forebrain-btn-primary h-auto text-xs"
          :disabled="busy"
          data-testid="project-servers-confirm"
          @click="confirm"
        >
          {{ t('chat.projectServersConfirm') }}
        </Button>
      </div>
    </template>
  </ForebrainModal>
</template>

<script setup lang="ts">
/**
 * The project's undecided MCP and language-server entries, confirmed once on
 * one panel: checked means allowed, unchecked means declined, and both are
 * remembered for this project until the entry's configuration changes. The
 * linkage is the TUI panel's own — every entry opens checked and the switch
 * off, unchecking an entry turns the switch off with it, and switching on
 * checks every entry — so the two surfaces record the same answer.
 *
 * The buttons answer the whole list at once: 确定 takes it as shown, 全部不允
 * 许 declines every entry, and closing the dialog (×, Escape, the mask) is the
 * same decline. The caller records the answer against the fingerprints of the
 * rows it showed.
 */
import { computed, ref, watch } from 'vue'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Checkbox } from '@repo/shadcn-vue/components/ui/checkbox'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import type { ProjectServerPendingRow } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  /** Whether the dialog is up. */
  open: boolean
  /** The project's undecided MCP entries. */
  mcp: ProjectServerPendingRow[]
  /** The project's undecided language-server entries, named by id. */
  lsp: ProjectServerPendingRow[]
  /** An answer is in flight: nothing may be answered twice. */
  busy?: boolean
  /** The last failed answer's own text, when one landed. */
  error?: string
}>(), { busy: false, error: '' })

const emit = defineEmits<{
  /** The answer as shown: which entries are checked, and the switch. */
  (e: 'confirm', payload: { allowed: Record<string, boolean>; alwaysAllow: boolean }): void
  /** Decline every listed entry and record it, so it is not asked again. */
  (e: 'declineAll'): void
  (e: 'close'): void
}>()

const { t } = useI18n()

const rows = computed(() => [...props.mcp, ...props.lsp])

// The checks open on every entry checked and the switch off, and every read
// of the list — the reopen after a stale answer included — starts over.
const checked = ref<Record<string, boolean>>({})
const alwaysAllow = ref(false)

watch(() => [props.open, props.mcp, props.lsp], () => {
  const next: Record<string, boolean> = {}
  for (const row of rows.value) next[row.name] = true
  checked.value = next
  alwaysAllow.value = false
}, { immediate: true })

/** An entry flips itself, and one left unchecked turns the switch off with
 * it — the switch being on means every entry is checked. */
function toggleEntry(name: string, value: boolean): void {
  checked.value = { ...checked.value, [name]: value }
  if (!value) alwaysAllow.value = false
}

/** The switch flips itself, and switched on it checks every entry. */
function toggleAlways(value: boolean): void {
  alwaysAllow.value = value
  if (value) {
    const next = { ...checked.value }
    for (const row of rows.value) next[row.name] = true
    checked.value = next
  }
}

function confirm(): void {
  const allowed: Record<string, boolean> = {}
  for (const row of rows.value) allowed[row.name] = checked.value[row.name] !== false
  emit('confirm', { allowed, alwaysAllow: alwaysAllow.value })
}
</script>
