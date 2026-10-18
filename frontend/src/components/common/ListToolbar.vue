<template>
  <div class="forebrain-list-toolbar">
    <slot name="left" />
    <span class="flex-1" />
    <slot name="actions" />
  </div>
</template>

<script setup lang="ts">
/**
 * The row above a table (plan 071, the prototype's `tabTools`): what narrows the
 * list on the left, the page's buttons on the right.
 *
 * The page has no title row any more — its description and its former hints
 * belong in `ListNote` under the table. The left slot always carries the search
 * box (`forebrain-field`, `max-width: 240px`), then any filter select; the right
 * slot carries the buttons the page used to keep in its title row.
 */
</script>
