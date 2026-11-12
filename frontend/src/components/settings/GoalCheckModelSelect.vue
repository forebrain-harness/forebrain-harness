<template>
  <!-- The clearable model picker of 设置 · 子代理's 「/goal 检查模型」 group
       (plan 299 §2). The component library has no arrow↔x swap
       (reka-ui's Select hard-codes its chevron and its ComboboxCancel clears
       an input, not a pair of controls), so this one is hand-built: a 32px
       combo that echoes the chosen row as name + provider, and a caret zone
       whose hover target becomes the clear x. -->
  <div ref="root" class="gcs">
    <button
      type="button"
      class="gcs__combo"
      :class="{ 'gcs__combo--open': open }"
      :data-goal-check-model="modelValue"
      aria-haspopup="listbox"
      :aria-expanded="open"
      @click="open = !open"
    >
      <ModelSignature
        v-if="selected"
        class="gcs__sig"
        :model="selected.model"
        :provider-name="selected.providerName"
      />
      <span v-else class="gcs__ph">{{ t('subagents.followPrimary') }}</span>
      <span class="gcs__caret-zone">
        <span class="gcs__caret"><ChevronDown class="size-3.5" aria-hidden="true" /></span>
        <span
          class="gcs__x"
          role="button"
          tabindex="0"
          :aria-label="t('subagents.goalCheck.clear')"
          :title="t('subagents.goalCheck.clear')"
          data-goal-check-clear
          @click.stop="clear"
          @keydown.enter.stop.prevent="clear"
          @keydown.space.stop.prevent="clear"
        >
          <X class="size-3.5" aria-hidden="true" />
        </span>
      </span>
    </button>

    <div v-if="open" class="gcs__menu" role="listbox">
      <label class="gcs__search">
        <Search class="size-3.5" aria-hidden="true" />
        <input
          v-model="query"
          type="text"
          :placeholder="t('subagents.goalCheck.search')"
          :aria-label="t('subagents.goalCheck.searchLabel')"
          data-goal-check-search
        >
      </label>
      <div class="gcs__list">
        <button
          v-for="choice in filtered"
          :key="choice.id"
          type="button"
          class="gcs__opt"
          :class="{ 'gcs__opt--on': choice.id === modelValue }"
          role="option"
          :aria-selected="choice.id === modelValue"
          :data-goal-check-option="choice.id"
          @click="pick(choice.id)"
        >
          <span class="gcs__lead">
            <Check v-if="choice.id === modelValue" class="size-3.5" aria-hidden="true" />
          </span>
          <ModelSignature
            class="gcs__opt-sig"
            :model="choice.model"
            :provider-name="choice.providerName"
            :base-url="choice.baseUrl"
            :api-path="choice.apiPath"
          />
        </button>
        <p v-if="filtered.length === 0" class="gcs__empty">{{ t('subagents.goalCheck.empty') }}</p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The /goal check's model picker (plan 299 §2). It answers a configured row's
 * id and nothing else: the same provider/model may be configured several times,
 * and only the id tells those rows apart, so the candidate list keeps the two
 * line signature (name + provider, then address + path) that distinguishes them
 * while the collapsed echo shows just the name and the provider.
 *
 * The menu opens on mount because that is the state the frozen prototype draws;
 * Escape or a click outside closes it, as there.
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Check, ChevronDown, Search, X } from 'lucide-vue-next'

import ModelSignature from '@/components/models/ModelSignature.vue'
import type { LlmModelChoice } from '@/lib/api'
import { useI18n } from '@/locales'

const props = defineProps<{
  /** The chosen row's id, or '' for "follow the primary agent". */
  modelValue: string
  choices: LlmModelChoice[]
}>()

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

const { t } = useI18n()

const root = ref<HTMLElement | null>(null)
const open = ref(true)
const query = ref('')

const selected = computed(() => props.choices.find((choice) => choice.id === props.modelValue) ?? null)

/** What the search box filters on: the three facts a row is told apart by. */
const haystack = (choice: LlmModelChoice): string =>
  [choice.model, choice.providerName, choice.baseUrl, choice.apiPath].join(' ').toLowerCase()

const filtered = computed(() => {
  const needle = query.value.trim().toLowerCase()
  if (!needle) return props.choices
  return props.choices.filter((choice) => haystack(choice).includes(needle))
})

function pick(id: string): void {
  emit('update:modelValue', id)
  open.value = false
  query.value = ''
}

/** The x: the same event as choosing nothing, so the group clears the level too. */
function clear(): void {
  emit('update:modelValue', '')
  open.value = false
  query.value = ''
}

function onPointerDown(event: PointerEvent): void {
  if (!open.value) return
  const target = event.target as Node | null
  if (target && root.value?.contains(target)) return
  open.value = false
}

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape' && open.value) open.value = false
}

onMounted(() => {
  document.addEventListener('pointerdown', onPointerDown)
  document.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onPointerDown)
  document.removeEventListener('keydown', onKeydown)
})

// A closed menu keeps no stale filter for the next opening.
watch(open, (isOpen) => {
  if (!isOpen) query.value = ''
})
</script>

<style scoped>
.gcs {
  position: relative;
  width: 100%;
}
.gcs__combo {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  height: 32px;
  padding: 0 10px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: var(--forebrain-input-bg);
  font-size: 13px;
  text-align: left;
}
.gcs__combo--open {
  border-color: var(--forebrain-brand-1);
}
.gcs__ph {
  color: var(--forebrain-muted-text);
}
.gcs__sig {
  flex: 1;
}
/* The caret zone: 18×20, the down arrow at rest, the clear x on hover. */
.gcs__caret-zone {
  position: relative;
  flex: none;
  margin-left: auto;
  width: 18px;
  height: 20px;
  display: grid;
  place-items: center;
  color: var(--forebrain-muted-text);
}
.gcs__caret {
  display: grid;
  place-items: center;
}
.gcs__x {
  position: absolute;
  inset: 0;
  display: none;
  place-items: center;
  cursor: pointer;
  color: var(--forebrain-muted-text);
}
.gcs__caret-zone:hover .gcs__caret {
  visibility: hidden;
}
.gcs__caret-zone:hover .gcs__x {
  display: grid;
}
.gcs__x:hover,
.gcs__x:focus-visible {
  color: var(--forebrain-danger);
}
/* Right-aligned so a 420px menu never overflows the settings column. */
.gcs__menu {
  position: absolute;
  right: 0;
  top: 38px;
  z-index: 15;
  width: 420px;
  max-width: min(420px, 92vw);
  padding: 4px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-bg);
  box-shadow: 0 12px 32px rgb(16 24 40 / 14%);
}
.gcs__search {
  display: flex;
  align-items: center;
  gap: 7px;
  height: 30px;
  margin: 2px 2px 4px;
  padding: 0 9px;
  border-radius: 6px;
  background: var(--forebrain-bg-alt);
  color: var(--forebrain-muted-text);
  font-size: 12.5px;
}
.gcs__search input {
  flex: 1;
  min-width: 0;
  border: 0;
  outline: none;
  background: transparent;
  font-size: 12.5px;
  color: var(--forebrain-text);
}
.gcs__list {
  max-height: 320px;
  overflow: auto;
}
.gcs__opt {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  width: 100%;
  padding: 6px 8px;
  border-radius: 7px;
  text-align: left;
}
.gcs__opt:hover {
  background: var(--forebrain-bg-alt);
}
.gcs__opt--on :deep(.modelsig__model) {
  color: var(--forebrain-brand-1);
}
.gcs__lead {
  flex: none;
  width: 14px;
  padding-top: 3px;
  color: var(--forebrain-brand-1);
}
.gcs__opt-sig {
  flex: 1;
}
.gcs__empty {
  padding: 8px 10px;
  font-size: 12.5px;
  color: var(--forebrain-muted-text);
}
</style>
