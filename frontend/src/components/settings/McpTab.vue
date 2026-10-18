<template>
  <div class="pb-6">
    <div class="mx-auto w-full max-w-3xl">
      <!-- Settings · MCP owns the shared layer only: the entries every primary
           agent inherits, added, edited and deleted here, and the agent's and
           the projects' own layers edited on their pages. -->
      <McpServersTable view="global" :agent-id="activePrimaryId" />
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * Settings · MCP (plan 074): the shared MCP list — `agents.defaults.mcp_servers`
 * — as the same table the menu bar's MCP page and a project space draw, with
 * the create, edit, delete and connection-test actions the list needs. A
 * same-named entry in a higher layer replaces one of these whole, which is what
 * the note under the table says; the entry is still listed here, because this is
 * where it is edited.
 *
 * The agent id is passed because the table takes it for the write routes a
 * different view needs; the shared layer's writes carry none.
 */
import McpServersTable from '@/components/mcp/McpServersTable.vue'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'

const { activeId: activePrimaryId } = usePrimaryAgents()
</script>
