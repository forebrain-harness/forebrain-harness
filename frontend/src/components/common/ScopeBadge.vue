<template>
  <span :class="['scope-badge', `scope-badge--${type}`]">
    <component :is="iconComponent" class="scope-badge-icon" aria-hidden="true" />
    {{ label }}
  </span>
</template>

<script setup lang="ts">
/**
 * The scope chip that every page wears next to its title: whose data the page
 * below it belongs to — the current primary agent, one project, or the global
 * settings that outlive both.
 */
import { computed } from 'vue'
import { FolderGit2, Globe, UserRound } from 'lucide-vue-next'

type ScopeType = 'agent' | 'project' | 'global'

interface Props {
  type: ScopeType
  label: string
}

const props = defineProps<Props>()

const iconComponent = computed(() => {
  switch (props.type) {
    case 'agent':
      return UserRound
    case 'project':
      return FolderGit2
    case 'global':
      return Globe
  }
})
</script>

<style scoped>
.scope-badge {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 22px;
  padding: 0 8px;
  border-radius: 5px;
  font-size: 11.5px;
  font-weight: 600;
  letter-spacing: 0.02em;
}

.scope-badge-icon {
  width: 12px;
  height: 12px;
}

/* Agent scope: brand soft background + brand color text */
.scope-badge--agent {
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}

/* Global scope: soft gray background + text-2 color */
.scope-badge--global {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text-2);
}

/* Project scope: dark background + white text */
.scope-badge--project {
  background: var(--forebrain-text);
  color: #ffffff;
}
</style>
