<template>
  <ForebrainModal :open="open" size="md" :title="t('chat.projectMcpConsentTitle')" @close="emit('close')">
    <div data-testid="project-mcp-consent">
      <p class="text-[12px] text-[var(--forebrain-text-2)]">{{ t('chat.projectMcpConsentBody') }}</p>
      <!-- The list is the library's confirmation shell — the same one every
           approval gate in the thread is drawn with — so the question, the
           entry it is about and its two answers read the way the rest of the
           conversation reads. The library has no dialog of its own: the modal
           around it is the repository's shared shell, which is drawn on the
           library's Dialog. -->
      <Confirmation
        :state="'approval-requested'"
        :approval="{ id: 'project-mcp-consent' }"
        class="!gap-0 !px-3 !py-1"
        data-testid="project-mcp-consent-list"
      >
        <ConfirmationRequest>
          <div
            v-for="row in pending"
            :key="row.name"
            class="border-b border-[var(--forebrain-divider)] last:border-b-0"
            :data-testid="`project-mcp-row-${row.name}`"
          >
            <ConfirmationActions class="!mt-0 !w-full !justify-between !self-stretch !gap-3 !py-2">
              <!-- What the entry would run, in the server's own one line: the
                   command or the address, and nothing else. -->
              <ConfirmationTitle class="min-w-0 !text-sm !font-normal !text-[var(--forebrain-text)]">
                <code class="break-words font-mono text-[12px] text-[var(--forebrain-text-2)]">{{ row.summary || row.name }}</code>
              </ConfirmationTitle>
              <div class="flex shrink-0 items-center gap-2">
                <ConfirmationAction
                  class="forebrain-btn forebrain-btn-primary text-xs"
                  :disabled="busy"
                  :data-testid="`project-mcp-allow-${row.name}`"
                  @click="emit('decide', { name: row.name, allow: true })"
                >
                  {{ t('chat.projectMcpConsentAllow') }}
                </ConfirmationAction>
                <ConfirmationAction
                  class="forebrain-btn forebrain-btn-ghost text-xs"
                  :disabled="busy"
                  :data-testid="`project-mcp-decline-${row.name}`"
                  @click="emit('decide', { name: row.name, allow: false })"
                >
                  {{ t('chat.projectMcpConsentDecline') }}
                </ConfirmationAction>
              </div>
            </ConfirmationActions>
          </div>
        </ConfirmationRequest>
      </Confirmation>
      <p class="mt-2 text-[11px] text-[var(--forebrain-muted-text)]">{{ t('chat.projectMcpConsentHint') }}</p>
      <p v-if="error" class="mt-2 text-[12px] text-[var(--forebrain-danger)]" role="alert">{{ error }}</p>
    </div>
  </ForebrainModal>
</template>

<script setup lang="ts">
/**
 * The project-level MCP entries nobody has decided yet, asked one entry at a
 * time: a new session re-resolves the three MCP layers, and an entry the
 * project declares but nobody has confirmed is waiting for an answer before it
 * can run.
 *
 * One click decides one entry, the way the project page's own rows do: the
 * answer travels as the entry's name, and the caller records it against the
 * entry as it was shown. The other entries keep whatever they had, so an
 * answer for one never decides another.
 *
 * The shell is the repository's shared modal (built on the library's Dialog)
 * and the list is the library's confirmation — the library has no dialog of
 * its own and no list of pending entries; the one confirmation shell it has
 * is the one a conversation's approval gate is drawn with, so the entries are
 * asked in the same shape the rest of the thread asks.
 */
import {
  Confirmation, ConfirmationAction, ConfirmationActions, ConfirmationRequest, ConfirmationTitle,
} from '@repo/elements/confirmation'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import { useI18n } from '@/locales'

withDefaults(defineProps<{
  /** Whether the dialog is up. */
  open: boolean
  /** The entries the project declares and nobody has confirmed yet. */
  pending: Array<{ name: string; summary: string; fingerprint?: string }>
  /** A decision is in flight: nothing may be answered twice. */
  busy?: boolean
  /** The last failed decision's own text, when one landed. */
  error?: string
}>(), { busy: false, error: '' })

const emit = defineEmits<{
  /** One entry's answer: the entry it is about and whether it may run. */
  (e: 'decide', payload: { name: string; allow: boolean }): void
  (e: 'close'): void
}>()

const { t } = useI18n()
</script>
