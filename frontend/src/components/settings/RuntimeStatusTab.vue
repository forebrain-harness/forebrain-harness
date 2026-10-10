<template>
  <div class="space-y-4 pb-6">
    <div class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-4 py-3 text-sm text-[var(--forebrain-text)]">
      <div class="font-medium text-[var(--forebrain-text)]">{{ t('settings.healthCheck') }}</div>
      <p class="mt-2 text-[var(--forebrain-text-2)]">{{ t('settings.status', { status: healthStatus }) }}</p>
      <p v-if="healthError" class="mt-2 text-sm text-[var(--forebrain-danger)]">{{ healthError }}</p>
    </div>

    <div class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-4 py-3 text-sm text-[var(--forebrain-text)]">
      <div class="font-medium text-[var(--forebrain-text)]">{{ t('settings.skillsSummary') }}</div>
      <p class="mt-2 text-[var(--forebrain-text-2)]">{{ t('settings.discovered', { count: skills.length }) }}</p>
      <p class="text-[var(--forebrain-text-2)]">{{ t('settings.enabled', { count: enabledSkillCount }) }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import forebrainApi, { type SkillRecord } from '@/lib/api'
import { useI18n } from '@/locales'

/** The gateway's own health and skill census — read-only runtime facts. */
const { t } = useI18n()
const healthStatus = ref(t('settings.healthChecking'))
const healthError = ref<string | null>(null)
const skills = ref<SkillRecord[]>([])

const enabledSkillCount = computed(() => skills.value.filter((skill) => skill.enabled).length)

onMounted(async () => {
  try {
    const res = await fetch('/healthz', { method: 'GET' })
    healthStatus.value = res.ok ? t('settings.healthOk') : `HTTP ${res.status}`
    if (!res.ok) healthError.value = await res.text()
  } catch (cause) {
    healthStatus.value = t('settings.healthFailed')
    healthError.value = cause instanceof Error ? cause.message : String(cause)
  }
  try {
    const overview = await forebrainApi.skillsOverview()
    skills.value = overview.installed
  } catch {
    skills.value = []
  }
})
</script>
