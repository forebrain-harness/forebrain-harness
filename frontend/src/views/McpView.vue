<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-4 pb-10 pt-5 sm:px-6">
    <div class="mx-auto flex w-full max-w-5xl flex-col gap-4">
      <!-- The menu bar's MCP page: this primary agent's own layer on top of the
           shared one, both read here and only the agent's written here. The
           table is the same one the settings tab and a project space draw, so
           the three pages cannot disagree about a row. -->
      <McpServersTable view="agent" :agent-id="activePrimaryId" />
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The MCP page in the menu bar (plan 074): the MCP servers the active primary
 * agent loads — its own layer first, the shared one under it, a same-named
 * shared entry hidden behind the agent's — with 添加 / 编辑 / 删除 for the
 * agent's own entries and a read-only view of the rest.
 *
 * The page itself is only the container the other menu-bar pages have; which
 * layers it lists, and what it may write, is `McpServersTable`'s `view`.
 */
import McpServersTable from '@/components/mcp/McpServersTable.vue'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'

// The tenant the shell is on: a primary agent is a tenant, and its MCP layer is
// keyed by its id, so a switch redraws the page under the new one (the shell
// rebuilds every page when the tenant changes).
const { activeId: activePrimaryId } = usePrimaryAgents()
</script>
