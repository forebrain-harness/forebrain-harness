<template>
  <!-- The prototype's .al-run (:399): one vertical line a turn's action cards
       hang on, each card's node saying what it is. The library's
       ChainOfThought supplies the per-step line; this wrapper makes those
       lines one — every step's line runs on through the gap below it, so the
       line runs from the first node to the last card's bottom. -->
  <ChainOfThought class="activity-line">
    <slot />
  </ChainOfThought>
</template>

<script setup lang="ts">
/**
 * The activity line (owner 2026-10-25): a turn's action cards — the agent's
 * words, its thinking, its calls — hang on one vertical line, 32px in from
 * the left, each on a node. It is the library's ChainOfThought laid out to
 * the prototype's .al-run: a grid with a 10px gap, no max width, the line in
 * the app's hairline colour at 1.5px through the nodes' centres (x=10px).
 */
import { ChainOfThought } from '@repo/elements/chain-of-thought'
</script>

<style scoped>
/* Overrides ChainOfThought's own `max-w-prose space-y-4`: the line is a grid
   (the prototype's .al-run), so the gap belongs to the grid, not to margins. */
.activity-line {
  position: relative;
  display: grid;
  gap: 10px;
  min-width: 0;
  max-width: none;
}

/* The line itself: the app's hairline at the prototype's 1.5px, centred under
   the 20px node (the icon column is full-height, so top-7 of the library's
   default — measured for a 28px icon — is reset to the node's centre). */
.activity-line :deep([data-slot='chain-of-thought-line']) {
  top: 10px;
  width: 1.5px;
  margin-left: -0.75px;
  background: var(--forebrain-divider);
}

/* Every step but the last reaches its line across the 10px gap below it, so
   the whole run is one line. The last step's line stops at its own bottom —
   nothing follows it. */
.activity-line :deep(.activity-step:not(:last-child) [data-slot='chain-of-thought-line']) {
  bottom: -10px;
}
</style>
