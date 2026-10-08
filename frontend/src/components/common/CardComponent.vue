<template>
  <div class="forebrain-card">
    <div v-if="$slots.header" class="forebrain-card-header">
      <slot name="header" />
    </div>
    <div class="forebrain-card-body">
      <slot />
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The bordered container a settings screen is built from: an optional header
 * row and a body that stacks its children with the card's own rhythm.
 */
</script>

<style scoped>
.forebrain-card {
  /* The hairline reads a variable whose default is the divider, so the page
     that owns one card can re-tone it (the active primary agent marks itself
     with the brand border) without reaching into this component. */
  border: 1px solid var(--forebrain-card-border, var(--forebrain-divider));
  border-radius: 8px;
  padding: 16px;
  display: grid;
  gap: 12px;
  align-content: start;
  background: var(--forebrain-bg);
}

.forebrain-card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.forebrain-card-header h3 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 8px;
}

.forebrain-card-body {
  display: grid;
  gap: 12px;
}
</style>
