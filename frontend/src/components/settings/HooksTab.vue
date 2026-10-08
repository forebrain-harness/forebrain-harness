<template>
  <div class="pb-6">
    <div class="mx-auto w-full max-w-3xl">
      <header class="mb-5 flex items-end justify-between gap-4">
        <div>
          <h1 class="text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">{{ t('hooks.title') }}</h1>
          <p class="mt-1 text-[13px] leading-relaxed text-[var(--forebrain-text-2)]">{{ t('hooks.description') }}</p>
        </div>
        <div class="flex gap-2">
          <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">{{ t('common.refresh') }}</button>
          <button type="button" class="forebrain-btn forebrain-btn-primary text-xs" :disabled="saving" @click="save">{{ t('common.save') }}</button>
        </div>
      </header>

      <p v-if="error" class="mb-4 rounded-xl border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>
      <p v-if="notice" class="mb-4 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3 text-sm text-[var(--forebrain-text)]">{{ notice }}</p>

      <CardComponent class="mb-4">
        <template #header>
          <h2 class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ t('hooks.addTitle') }}</h2>
        </template>
        <div class="grid gap-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
          <select v-model="draft.event" class="forebrain-field">
            <option v-for="name in events" :key="name" :value="name">{{ name }}</option>
          </select>
          <select v-model="draft.type" class="forebrain-field">
            <option v-for="kind in knownTypes" :key="kind" :value="kind">{{ kind }}</option>
          </select>
        </div>
        <input v-model="draft.matcher" :placeholder="t('hooks.matcherPlaceholder')" class="forebrain-field w-full font-mono" />
        <input v-model="draft.command" :placeholder="t('hooks.commandPlaceholder')" class="forebrain-field w-full font-mono" />
        <button type="button" class="forebrain-btn forebrain-btn-primary h-9 px-4 text-[12px]" :disabled="!draft.event || !draft.command.trim()" @click="add">
          {{ t('hooks.add') }}
        </button>
      </CardComponent>

      <div v-if="loading && !eventKeys.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <ul v-else-if="eventKeys.length" class="space-y-2">
        <li v-for="event in eventKeys" :key="event" class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3">
          <div class="text-[13px] font-medium text-[var(--forebrain-text)]">{{ event }}</div>
          <ul class="mt-2 space-y-1">
            <li v-for="(matcher, mIdx) in hooks[event]" :key="mIdx" class="rounded-lg bg-[var(--forebrain-surface)] px-2 py-1.5">
              <div class="text-[11px] text-[var(--forebrain-muted-text)]">{{ matcher.matcher || t('hooks.anyTool') }}</div>
              <div v-for="(cmd, cIdx) in matcher.hooks ?? []" :key="cIdx" class="mt-1 flex items-center justify-between gap-2">
                <span class="min-w-0 truncate font-mono text-[12px] text-[var(--forebrain-text-2)]">{{ cmd.type }}: {{ cmd.command || cmd.prompt || cmd.url }}</span>
                <button type="button" class="forebrain-btn forebrain-btn-ghost h-6 shrink-0 px-2 text-[11px] text-[var(--forebrain-danger)]" @click="removeCommand(event, mIdx, cIdx)">
                  {{ t('common.delete') }}
                </button>
              </div>
            </li>
          </ul>
        </li>
      </ul>
      <p v-else class="rounded-xl border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
        {{ t('hooks.empty') }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * Hooks run commands at points in a turn. The events and kinds offered here are
 * the ones the runtime accepts — the list comes from the server rather than
 * being written down twice — so a hook that is offered is a hook that will
 * load.
 */
import { computed, onMounted, reactive, ref } from 'vue'
import CardComponent from '@/components/common/CardComponent.vue'
import { getErrorMessage, forebrainApi, type HooksSettingsRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()
const hooks = ref<HooksSettingsRecord>({})
const events = ref<string[]>([])
const knownTypes = ref<string[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const notice = ref('')
const draft = reactive({ event: '', type: 'command', matcher: '', command: '' })

const eventKeys = computed(() => Object.keys(hooks.value).sort())

function add() {
  const event = draft.event
  const list = [...(hooks.value[event] ?? [])]
  list.push({
    matcher: draft.matcher.trim() || undefined,
    hooks: [{ type: draft.type, command: draft.command.trim() }],
  })
  hooks.value = { ...hooks.value, [event]: list }
  draft.matcher = ''
  draft.command = ''
}

function removeCommand(event: string, matcherIdx: number, commandIdx: number) {
  const list = [...(hooks.value[event] ?? [])]
  const matcher = { ...list[matcherIdx] }
  matcher.hooks = (matcher.hooks ?? []).filter((_, i) => i !== commandIdx)
  if (matcher.hooks.length === 0) {
    list.splice(matcherIdx, 1)
  } else {
    list[matcherIdx] = matcher
  }
  const next = { ...hooks.value }
  if (list.length === 0) {
    delete next[event]
  } else {
    next[event] = list
  }
  hooks.value = next
}

async function load() {
  loading.value = true
  error.value = ''
  notice.value = ''
  try {
    const res = await forebrainApi.hooks()
    hooks.value = res.hooks ?? {}
    events.value = res.events ?? []
    knownTypes.value = res.knownTypes ?? []
    if (!draft.event) draft.event = events.value[0] ?? ''
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  error.value = ''
  notice.value = ''
  try {
    await forebrainApi.saveHooks(hooks.value)
    notice.value = t('hooks.saved')
    await load()
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
