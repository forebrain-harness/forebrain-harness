<template>
  <div class="mcp-kv">
    <span v-if="readonly && rows.length === 0" class="mcp-kv-empty" data-kv-empty>
      {{ t('mcp.unsetValue') }}
    </span>
    <div v-else class="mcp-kv-rows">
      <div
        v-for="(row, index) in rows"
        :key="index"
        class="mcp-kv-row"
        :class="{ 'mcp-kv-row--ro': readonly }"
        :data-kv-row="index"
      >
        <Input
          class="forebrain-field h-[34px] font-mono"
          :model-value="row.key"
          :aria-label="t('mcp.kvKey')"
          :readonly="readonly"
          autocomplete="off"
          data-kv-key
          @update:model-value="(value) => setKey(index, String(value))"
        />
        <Input
          class="forebrain-field h-[34px] font-mono"
          :model-value="row.value"
          :aria-label="t('mcp.kvValue')"
          :readonly="readonly"
          autocomplete="off"
          data-kv-value
          @update:model-value="(value) => setValue(index, String(value))"
        />
        <button
          v-if="!readonly"
          v-tip:top="t('common.delete')"
          type="button"
          class="forebrain-icon-btn mcp-kv-remove"
          :aria-label="t('mcp.kvRemove')"
          data-kv-remove
          @click="remove(index)"
        >
          <Trash2 class="size-3.5" aria-hidden="true" />
        </button>
      </div>
    </div>
    <Button
      v-if="!readonly"
      type="button"
      class="forebrain-btn forebrain-btn-ghost mcp-kv-add h-auto gap-1.5 text-xs"
      data-kv-add
      @click="add"
    >
      <Plus class="size-3.5" aria-hidden="true" />{{ addLabel }}
    </Button>
  </div>
</template>

<script setup lang="ts">
/**
 * One MCP entry's key/value map as a table of one pair per row (plan 074): the
 * environment of a stdio server, the headers of an http one. The keys are the
 * operator's own spelling and travel unchanged, so both fields are monospace
 * and neither is ever rewritten here.
 *
 * `modelValue` is the pairs exactly as the form holds them — a stored secret
 * arrives as `[REDACTED]` and stays that way until someone types over it, which
 * is how the save knows to keep the stored value. The component itself decides
 * nothing about the payload; `mcpKeyValuePayload` does that.
 *
 * Read-only is a viewer, not a disabled editor: no add button, no delete
 * button, every field `readonly`, and an empty map says `未设置` instead of
 * showing nothing at all.
 */
import { computed } from 'vue'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import { Plus, Trash2 } from 'lucide-vue-next'

import { useI18n } from '@/locales'

import type { McpKeyValueRow } from './mcpRows'

const props = withDefaults(
  defineProps<{
    /** The pairs as the form holds them. */
    modelValue: McpKeyValueRow[]
    /** The add button's words: `添加环境变量` or `添加请求头`. */
    addLabel: string
    /** A viewer: no add, no delete, every field read-only. */
    readonly?: boolean
  }>(),
  { readonly: false },
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: McpKeyValueRow[]): void
}>()

const { t } = useI18n()

const rows = computed(() => props.modelValue ?? [])

function emitRows(next: McpKeyValueRow[]) {
  emit('update:modelValue', next)
}

function setKey(index: number, key: string) {
  emitRows(rows.value.map((row, at) => (at === index ? { ...row, key } : row)))
}

function setValue(index: number, value: string) {
  emitRows(rows.value.map((row, at) => (at === index ? { ...row, value } : row)))
}

function remove(index: number) {
  emitRows(rows.value.filter((_, at) => at !== index))
}

/** A blank pair is a row somebody started and left; it is not sent unless it
 * gets a key, which is `mcpKeyValuePayload`'s rule, not this component's. */
function add() {
  emitRows([...rows.value, { key: '', value: '' }])
}
</script>

<style scoped>
.mcp-kv {
  display: grid;
  gap: 6px;
}

.mcp-kv-rows {
  display: grid;
  gap: 6px;
}

/* Key column narrower than the value's, plus the delete button's own column. */
.mcp-kv-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1.3fr) 30px;
  gap: 6px;
  align-items: center;
}

.mcp-kv-row--ro {
  grid-template-columns: minmax(0, 1fr) minmax(0, 1.3fr);
}

.mcp-kv-remove {
  color: var(--forebrain-danger);
}

/* The add button sits under the key it appends to, not across the drawer. */
.mcp-kv-add {
  justify-self: start;
}

.mcp-kv-empty {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}
</style>
