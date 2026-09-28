<template>
  <div class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] p-3">
    <div class="mb-3 flex items-center justify-between">
      <div class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('settings.installedSkills') }}</div>
      <div class="text-[12px] text-[var(--forebrain-muted-text)]">{{ skills.length }}</div>
    </div>
    <div v-if="loading" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] px-3 py-6 text-[13px] text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
    <div v-else-if="!skills.length" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface-soft)] px-3 py-6 text-[13px] text-[var(--forebrain-muted-text)]">{{ t('settings.noInstalledSkills') }}</div>
    <div v-else class="space-y-2">
      <label
        v-for="skill in skills"
        :key="skill.rootPath || skill.name"
        class="flex cursor-pointer items-start gap-3 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-3 transition hover:bg-[var(--forebrain-input-hover-bg)]"
      >
        <input
          :checked="enabledPaths.has(skill.rootPath || '')"
          :disabled="!skill.rootPath || saving"
          type="checkbox"
          class="mt-1 h-4 w-4 rounded border-[var(--forebrain-divider-strong)] text-[var(--forebrain-brand-1)] focus:ring-[var(--forebrain-brand-1)]"
          @change="emit('toggle', skill.rootPath || '', ($event.target as HTMLInputElement).checked)"
        />
        <div class="min-w-0 flex-1">
          <div class="flex flex-wrap items-center gap-2">
            <span class="font-medium text-[var(--forebrain-text)]">{{ skill.name }}</span>
            <span class="rounded-full bg-[var(--forebrain-button-alt-bg)] px-2 py-0.5 text-[11px] text-[var(--forebrain-text-2)]">{{ skill.source || 'unknown' }}</span>
            <span class="rounded-full bg-[rgba(25,183,210,0.14)] px-2 py-0.5 text-[11px] text-[var(--forebrain-brand-1)]">{{ skill.enabled ? 'enabled' : 'disabled' }}</span>
          </div>
          <p v-if="skill.description" class="mt-1 text-[12px] leading-relaxed text-[var(--forebrain-text-2)]">{{ skill.description }}</p>
          <div class="mt-2 space-y-1 text-[11px] text-[var(--forebrain-muted-text)]">
            <div v-if="skill.rootPath" class="font-mono break-all">{{ skill.rootPath }}</div>
            <div v-if="skill.allowedTools" class="font-mono">allowed_tools: {{ skill.allowedTools }}</div>
          </div>
        </div>
        <button
          type="button"
          class="shrink-0 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-2.5 py-1 text-[11px] text-[var(--forebrain-text)] hover:bg-[var(--forebrain-surface)]"
          @click.stop="emit('inspect', skill.name)"
        >
          {{ t('common.view') }}
        </button>
      </label>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { SkillRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()

defineProps<{
  skills: SkillRecord[]
  enabledPaths: Set<string>
  loading: boolean
  saving: boolean
}>()

const emit = defineEmits<{
  toggle: [path: string, enabled: boolean]
  inspect: [name: string]
}>()
</script>
