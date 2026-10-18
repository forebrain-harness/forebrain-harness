<template>
  <div class="pb-6">
    <!-- The gateway's own facts, one list (the prototype's `kvList`): what it
         answers, which build it is, where it listens and what it keeps. A
         gateway that could not be asked, or that answered without a field,
         leaves the cell as — rather than inventing a value. -->
    <dl class="forebrain-kv" data-testid="runtime-kv">
      <dt>{{ t('settings.runtimeStatusLabel') }}</dt>
      <dd>
        <BadgeComponent v-if="health === 'ok'" variant="success">{{ t('settings.healthOk') }}</BadgeComponent>
        <BadgeComponent v-else-if="health === 'failed'" variant="error">{{ t('settings.runtimeAbnormal') }}</BadgeComponent>
        <span v-else class="text-[var(--forebrain-muted-text)]">{{ t('settings.healthChecking') }}</span>
        <span v-if="healthDetail" class="ml-1.5 break-all font-mono text-[var(--forebrain-muted-text)]">{{ healthDetail }}</span>
      </dd>

      <dt>{{ t('settings.runtimeVersion') }}</dt>
      <dd class="font-mono">{{ version || '—' }}</dd>

      <dt>{{ t('settings.runtimeAddress') }}</dt>
      <dd class="font-mono">{{ address || '—' }}</dd>

      <dt>{{ t('settings.runtimeAuth') }}</dt>
      <dd>{{ info?.auth || '—' }}</dd>

      <dt>{{ t('settings.runtimeAgents') }}</dt>
      <dd>{{ agentsText }}</dd>

      <dt>{{ t('settings.runtimeHome') }}</dt>
      <dd class="break-all font-mono">{{ info?.home || '—' }}</dd>

      <dt>{{ t('settings.runtimeLogs') }}</dt>
      <dd class="break-all font-mono">{{ info?.logsDir || '—' }}</dd>

      <dt>{{ t('settings.runtimeStateDir') }}</dt>
      <dd class="break-all font-mono">{{ info?.stateDir || '—' }}</dd>

      <dt>{{ t('settings.runtimeSkills') }}</dt>
      <dd>{{ skillsLoaded ? t('settings.runtimeSkillsCount', { discovered: skills.length, enabled: enabledSkillCount }) : '—' }}</dd>
    </dl>
  </div>
</template>

<script setup lang="ts">
/**
 * The gateway's runtime facts, read-only: its health, the build the browser is
 * running, the address it reached, the auth mode, the primary agents and the
 * directories the gateway keeps its data in, plus the skill census the
 * settings page has always shown.
 *
 * Everything but the version and the address comes from the gateway
 * (`/api/runtime`, plan 076); the version is this build's own constant and the
 * address is the one the browser used to reach it, which is the only address a
 * reader can act on.
 */
import { computed, onMounted, ref } from 'vue'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import forebrainApi, { type RuntimeInfo, type SkillRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()

const info = ref<RuntimeInfo | null>(null)
const health = ref<'checking' | 'ok' | 'failed'>('checking')
const healthDetail = ref('')
const skills = ref<SkillRecord[]>([])
const skillsLoaded = ref(false)

const version = __FOREBRAIN_VERSION__
const address = typeof window === 'undefined' ? '' : window.location.host
const agentsText = computed(() =>
  typeof info.value?.agents === 'number' ? t('settings.runtimeAgentsCount', { count: info.value.agents }) : '—',
)
const enabledSkillCount = computed(() => skills.value.filter((skill) => skill.enabled).length)

onMounted(async () => {
  try {
    const res = await fetch('/healthz', { method: 'GET' })
    health.value = res.ok ? 'ok' : 'failed'
    if (!res.ok) healthDetail.value = `HTTP ${res.status}`
  } catch (cause) {
    health.value = 'failed'
    healthDetail.value = cause instanceof Error ? cause.message : String(cause)
  }
  try {
    info.value = await forebrainApi.runtimeInfo()
  } catch {
    info.value = null
  }
  try {
    const overview = await forebrainApi.skillsOverview()
    skills.value = overview.installed
    // A census that could not be read is — below, not "0 of 0".
    skillsLoaded.value = true
  } catch {
    skills.value = []
  }
})
</script>
