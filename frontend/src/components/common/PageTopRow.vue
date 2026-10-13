<template>
  <div class="forebrain-page-top">
    <h2 class="forebrain-page-top-title"><slot name="title">{{ title }}</slot></h2>
    <span v-if="description || $slots.description" class="forebrain-page-top-desc">
      <slot name="description">{{ description }}</slot>
    </span>
    <div v-if="$slots.actions" class="forebrain-page-top-actions"><slot name="actions" /></div>
  </div>
</template>

<script setup lang="ts">
/**
 * A page's one top row: the page's name, a one-line description and its
 * buttons. The top bar already names the page, so a page never opens with a
 * second heading layer — this row is the content's first line, and the
 * buttons sit at its right edge.
 */
withDefaults(defineProps<{ title?: string; description?: string }>(), { title: '', description: '' })
</script>
