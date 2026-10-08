<template>
  <div class="flex flex-1 flex-col overflow-y-auto bg-[var(--forebrain-bg)] px-3 pb-12 pt-6 sm:px-6">
    <div class="mx-auto w-full max-w-3xl">
      <header class="mb-5 flex items-end justify-between gap-4">
        <div>
          <div class="flex items-center gap-2">
            <h1 class="text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">{{ t('tools.title') }}</h1>
            <ScopeBadge type="agent" :label="t('scope.agent')" />
          </div>
          <p class="mt-1 text-[13px] leading-relaxed text-[var(--forebrain-text-2)]">{{ t('tools.description') }}</p>
        </div>
        <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs" :disabled="loading" @click="load">
          {{ t('common.refresh') }}
        </button>
      </header>

      <p v-if="error" class="mb-4 rounded-xl border border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] px-4 py-3 text-sm text-[var(--forebrain-danger)]">{{ error }}</p>

      <input
        v-model="query"
        type="search"
        :placeholder="t('tools.filter')"
        class="mb-3 w-full rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-input-bg)] px-3 py-2 text-[13px] text-[var(--forebrain-text)] outline-none focus:border-[var(--forebrain-focus-border)]"
      />

      <p class="mb-3 text-[12px] text-[var(--forebrain-muted-text)]">
        {{ t('tools.count', { shown: filtered.length, total: records.length }) }}
      </p>

      <div v-if="loading && !records.length" class="py-10 text-center text-sm text-[var(--forebrain-muted-text)]">{{ t('common.loading') }}</div>
      <ul v-else class="space-y-2">
        <li
          v-for="record in filtered"
          :key="record.name"
          class="rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-4 py-3"
        >
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <span class="font-mono text-[13px] text-[var(--forebrain-text)]">{{ record.name }}</span>
                <span v-if="record.category" class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[11px] text-[var(--forebrain-muted-text)]">
                  {{ record.category }}
                </span>
                <span v-if="record.readOnly" class="rounded-full border border-[var(--forebrain-divider)] px-2 py-0.5 text-[11px] text-[var(--forebrain-brand-1)]">
                  {{ t('tools.readOnly') }}
                </span>
                <span v-if="record.destructive" class="rounded-full border border-[var(--forebrain-danger)] px-2 py-0.5 text-[11px] text-[var(--forebrain-danger)]">
                  {{ t('tools.destructive') }}
                </span>
              </div>
              <p v-if="record.description" class="mt-1 text-[12px] leading-relaxed text-[var(--forebrain-text-2)]">{{ record.description }}</p>
            </div>
            <button
              v-if="record.inputSchema"
              type="button"
              class="forebrain-btn forebrain-btn-ghost h-7 shrink-0 px-2 text-[11px]"
              @click="toggleSchema(record.name)"
            >
              {{ openSchema === record.name ? t('tools.hideSchema') : t('tools.showSchema') }}
            </button>
          </div>
          <pre
            v-if="openSchema === record.name"
            class="mt-2 overflow-x-auto rounded-lg bg-[var(--forebrain-code-bg)] px-3 py-2 font-mono text-[11px] leading-relaxed text-[var(--forebrain-code-text)]"
          >{{ formatSchema(record.inputSchema) }}</pre>
        </li>
      </ul>
      <p v-if="!loading && !filtered.length" class="rounded-xl border border-dashed border-[var(--forebrain-divider)] px-4 py-8 text-center text-sm text-[var(--forebrain-muted-text)]">
        {{ t('common.empty') }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The tool table the active primary agent's runtime exposes. It is a
 * verification surface: what the model can call right now, under this tenant,
 * with the safety flags the approval policy reads.
 */
import { computed, onMounted, ref } from 'vue'
import ScopeBadge from '@/components/common/ScopeBadge.vue'
import { getErrorMessage, forebrainApi, type ToolMetaRecord } from '@/lib/api'
import { useI18n } from '@/locales'

const { t } = useI18n()
const records = ref<ToolMetaRecord[]>([])
const loading = ref(false)
const error = ref('')
const query = ref('')
const openSchema = ref('')

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return records.value
  return records.value.filter((record) =>
    record.name.toLowerCase().includes(q) || (record.description ?? '').toLowerCase().includes(q))
})

function toggleSchema(name: string) {
  openSchema.value = openSchema.value === name ? '' : name
}

function formatSchema(schema: unknown): string {
  try {
    return JSON.stringify(schema, null, 2)
  } catch {
    return String(schema)
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    records.value = await forebrainApi.toolsList()
  } catch (e) {
    error.value = getErrorMessage(e)
    records.value = []
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>
