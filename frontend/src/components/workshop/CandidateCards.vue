<template>
  <div class="my-2 space-y-1.5" data-testid="workshop-candidates">
    <div
      v-for="candidate in candidates"
      :key="candidate.name"
      class="flex items-center gap-2 rounded-lg border border-[var(--forebrain-divider)] px-3 py-2"
    >
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-2">
          <span class="font-mono text-[12px] font-medium text-[var(--forebrain-text)]">{{ candidate.name }}</span>
        </div>
        <div class="text-[11px] text-[var(--forebrain-text-2)]">{{ candidate.description }}</div>
        <div class="font-mono text-[11px] text-[var(--forebrain-muted-text)]">{{ candidate.source }}</div>
      </div>
      <BadgeComponent v-if="isInstalled(candidate)" variant="success">{{ t('skills.enabled') }}</BadgeComponent>
      <button
        v-else
        type="button"
        class="forebrain-btn forebrain-btn-primary text-xs"
        :disabled="busy === candidate.name"
        :data-testid="`workshop-install-${candidate.name}`"
        @click="emit('install', candidate)"
      >
        {{ busy === candidate.name ? t('common.loading') : t('common.install') }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
/** The discovery reply's candidate skills as installable cards. */
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import type { SkillCandidate } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{
  candidates: SkillCandidate[]
  /** Names already installed this session, on top of the reply's own flags. */
  installed?: string[]
  busy?: string
}>(), { installed: () => [], busy: '' })

const emit = defineEmits<{ install: [candidate: SkillCandidate] }>()
const { t } = useI18n()

function isInstalled(candidate: SkillCandidate): boolean {
  return candidate.installed || props.installed.includes(candidate.name)
}
</script>
