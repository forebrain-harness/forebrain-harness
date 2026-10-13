<template>
  <div class="space-y-3" data-testid="workshop-trigger">
    <!-- 触发描述 -->
    <div class="rounded-lg border border-[var(--forebrain-divider)] p-3">
      <div class="flex items-center justify-between">
        <span class="text-[13px] font-semibold text-[var(--forebrain-text)]">{{ t('workshop.triggerDescTitle') }}</span>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="!editable" data-testid="workshop-trigger-desc-edit" @click="openDescEdit">{{ t('common.edit') }}</button>
      </div>
      <div class="mt-2 rounded-md bg-[var(--forebrain-input-bg)] p-2 text-[12px] text-[var(--forebrain-text)]">{{ description || '—' }}</div>
      <span class="mt-1 block text-[11px] text-[var(--forebrain-muted-text)]">{{ t('workshop.triggerDescHint') }}</span>
    </div>

    <!-- 触发测试 -->
    <div class="rounded-lg border border-[var(--forebrain-divider)] p-3">
      <div class="flex items-center justify-between gap-2">
        <span class="text-[13px] font-semibold text-[var(--forebrain-text)]">{{ t('workshop.triggerTestTitle') }}</span>
        <div class="flex gap-1">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="!editable" data-testid="workshop-trigger-add" @click="openQueryAdd">{{ t('workshop.addQuery') }}</button>
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="!skillName" data-testid="workshop-trigger-retest" @click="emit('send', t('workshop.retestMessage'))">{{ t('workshop.retest') }}</button>
        </div>
      </div>
      <template v-if="!results">
        <p class="mt-2 text-[12px] text-[var(--forebrain-muted-text)]">{{ t('workshop.notTested') }}</p>
        <button type="button" class="forebrain-btn forebrain-btn-primary mt-2 text-xs" :disabled="!skillName || !queries.length" data-testid="workshop-trigger-run" @click="emit('send', t('workshop.runTriggerTest'))">{{ t('workshop.runTriggerTest') }}</button>
      </template>
      <template v-else>
        <div class="mt-3 flex items-center gap-3">
          <div class="text-center">
            <div class="text-[22px] font-semibold text-[var(--forebrain-text)]">{{ percent(applied ? results.bestScore : results.currentScore) }}</div>
            <div class="text-[11px] text-[var(--forebrain-muted-text)]">{{ applied ? t('workshop.descInDraft') : t('workshop.descCurrent') }}</div>
          </div>
          <ArrowRight v-if="!applied" class="h-4 w-4 text-[var(--forebrain-muted-text)]" aria-hidden="true" />
          <div v-if="!applied" class="text-center">
            <div class="text-[22px] font-semibold text-[var(--forebrain-success)]">{{ percent(results.bestScore) }}</div>
            <div class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('workshop.descSuggested') }}</div>
          </div>
        </div>
        <div class="mt-3 space-y-1">
          <div v-for="(row, index) in visibleQueries" :key="index" class="flex items-center gap-2 text-[12px]">
            <BadgeComponent :variant="row.shouldTrigger ? 'default' : 'gray'">{{ row.shouldTrigger ? t('workshop.shouldTrigger') : t('workshop.shouldNotTrigger') }}</BadgeComponent>
            <span class="min-w-0 flex-1 truncate text-[var(--forebrain-text-2)]">{{ row.query }}</span>
            <Check v-if="row.correct" class="h-3.5 w-3.5 text-[var(--forebrain-success)]" aria-hidden="true" />
            <X v-else class="h-3.5 w-3.5 text-[var(--forebrain-danger)]" aria-hidden="true" />
          </div>
        </div>
        <button v-if="sortedQueries.length > 5 && !showAll" type="button" class="mt-2 text-[12px] text-[var(--forebrain-brand-1)] hover:underline" data-testid="workshop-trigger-showall" @click="showAll = true">
          {{ t('workshop.showAllQueries', { n: sortedQueries.length }) }}
        </button>
      </template>
    </div>

    <!-- 建议描述 -->
    <div v-if="results && results.bestDescription && !applied" class="rounded-lg border border-[var(--forebrain-divider)] p-3" data-testid="workshop-trigger-suggest">
      <div class="flex items-center justify-between">
        <span class="text-[13px] font-semibold text-[var(--forebrain-text)]">{{ t('workshop.suggestedTitle') }}</span>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="!skillName" @click="emit('send', t('workshop.regenerateMessage'))">{{ t('workshop.regenerate') }}</button>
      </div>
      <div class="mt-2 rounded-md bg-[var(--forebrain-input-bg)] p-2 text-[12px] text-[var(--forebrain-text)]">{{ results.bestDescription }}</div>
      <button type="button" class="forebrain-btn forebrain-btn-primary mt-2 text-xs" :disabled="!editable" data-testid="workshop-trigger-apply" @click="applyToDraft">{{ t('workshop.applyToDraft') }}</button>
    </div>

    <p v-if="error" class="text-[12px] text-[var(--forebrain-danger)]">{{ error }}</p>

    <ForebrainModal :open="descEditOpen" size="md" :title="t('workshop.editTriggerDesc')" @close="descEditOpen = false">
      <textarea v-model="descDraft" rows="4" class="forebrain-field w-full" data-testid="workshop-trigger-desc-input" />
      <span class="text-[11px] text-[var(--forebrain-muted-text)]">{{ t('workshop.triggerDescEditHint') }}</span>
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="descEditOpen = false">{{ t('common.cancel') }}</button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" data-testid="workshop-trigger-desc-save" @click="saveDescription">{{ t('common.save') }}</button>
      </template>
    </ForebrainModal>

    <ForebrainModal :open="queryAddOpen" size="sm" :title="t('workshop.addQuery')" @close="queryAddOpen = false">
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('workshop.queryLabel') }}</span>
        <input v-model="newQuery" class="forebrain-field w-full" data-testid="workshop-trigger-query-input" />
      </label>
      <label class="block">
        <span class="mb-1 block text-[12px] text-[var(--forebrain-text-2)]">{{ t('workshop.queryShould') }}</span>
        <select v-model="newShouldTrigger" class="forebrain-field w-full" data-testid="workshop-trigger-query-should">
          <option :value="true">{{ t('workshop.shouldTrigger') }}</option>
          <option :value="false">{{ t('workshop.shouldNotTrigger') }}</option>
        </select>
      </label>
      <template #footer>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" @click="queryAddOpen = false">{{ t('common.cancel') }}</button>
        <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="!newQuery.trim()" data-testid="workshop-trigger-query-save" @click="saveQuery">{{ t('workshop.addQuery') }}</button>
      </template>
    </ForebrainModal>
  </div>
</template>

<script setup lang="ts">
/**
 * The workshop's trigger tab: the current draft description, the last trigger
 * run's accuracy and per-query verdicts, and the suggested description. The
 * run itself is the model's job — the tab fills the composer and sends.
 */
import { computed, onMounted, ref, watch } from 'vue'
import { ArrowRight, Check, X } from 'lucide-vue-next'
import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ForebrainModal from '@/components/common/ForebrainModal.vue'
import forebrainApi, { getErrorMessage, type WorkshopTriggerQuery, type WorkshopTriggerResults } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(defineProps<{ skillName: string; projectId?: string; editable: boolean }>(), { projectId: '' })
const emit = defineEmits<{ send: [text: string]; fill: [text: string] }>()
const { t } = useI18n()

const description = ref('')
const queries = ref<WorkshopTriggerQuery[]>([])
const results = ref<WorkshopTriggerResults | null>(null)
const error = ref('')
const applied = ref(false)
const showAll = ref(false)

const descEditOpen = ref(false)
const descDraft = ref('')
const queryAddOpen = ref(false)
const newQuery = ref('')
const newShouldTrigger = ref(false)

const sortedQueries = computed(() => {
  const rows = (results.value?.queries ?? []).map((row) => ({ ...row, correct: row.pass }))
  return rows.slice().sort((a, b) => Number(a.correct) - Number(b.correct))
})
const visibleQueries = computed(() => (showAll.value ? sortedQueries.value : sortedQueries.value.slice(0, 5)))

function percent(value: number): string {
  return `${Math.round((value ?? 0) * 100)}%`
}

async function load() {
  if (!props.skillName) return
  error.value = ''
  try {
    const [trigger, skillFile] = await Promise.all([
      forebrainApi.skillWorkshopTrigger(props.skillName, props.projectId),
      forebrainApi.skillFileRead(props.skillName, 'SKILL.md').catch(() => null),
    ])
    queries.value = trigger.queries ?? []
    results.value = trigger.results ?? null
    if (skillFile) description.value = parseDescription(skillFile.content)
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

function parseDescription(content: string): string {
  const match = /^---\n([\s\S]*?)\n---/.exec(content)
  if (!match) return ''
  for (const line of match[1].split('\n')) {
    const idx = line.indexOf(':')
    if (idx < 0) continue
    if (line.slice(0, idx).trim() === 'description') return line.slice(idx + 1).trim()
  }
  return ''
}

function withDescription(content: string, value: string): string {
  const match = /^---\n([\s\S]*?)\n---/.exec(content)
  if (!match) return content
  const block = match[1]
  const lines = block.split('\n')
  let replaced = false
  const next = lines.map((line) => {
    const idx = line.indexOf(':')
    if (idx >= 0 && line.slice(0, idx).trim() === 'description') {
      replaced = true
      return `description: ${value}`
    }
    return line
  })
  if (!replaced) next.push(`description: ${value}`)
  return `---\n${next.join('\n')}\n---${content.slice(match[0].length)}`
}

function openDescEdit() {
  descDraft.value = description.value
  descEditOpen.value = true
}

async function saveDescription() {
  descEditOpen.value = false
  await writeDescription(descDraft.value.trim())
}

async function applyToDraft() {
  if (!results.value?.bestDescription) return
  await writeDescription(results.value.bestDescription)
  applied.value = true
}

async function writeDescription(value: string) {
  try {
    const file = await forebrainApi.skillFileRead(props.skillName, 'SKILL.md')
    await forebrainApi.skillFileWrite(props.skillName, 'SKILL.md', withDescription(file.content, value))
    description.value = value
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

function openQueryAdd() {
  newQuery.value = ''
  newShouldTrigger.value = false
  queryAddOpen.value = true
}

async function saveQuery() {
  queryAddOpen.value = false
  const next = [...queries.value, { query: newQuery.value.trim(), shouldTrigger: newShouldTrigger.value }]
  try {
    await forebrainApi.skillFileWrite(props.skillName, 'evals/trigger_queries.json', `${JSON.stringify(
      next.map((row) => ({ query: row.query, should_trigger: row.shouldTrigger })),
      null,
      2,
    )}\n`)
    queries.value = next
  } catch (e: unknown) {
    error.value = getErrorMessage(e)
  }
}

defineExpose({ reload: load })
watch(() => [props.skillName, props.projectId], load)
onMounted(load)
</script>
