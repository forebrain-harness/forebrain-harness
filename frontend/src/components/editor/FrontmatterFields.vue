<template>
  <div v-if="fields.length" class="mde-props">
    <template v-for="(line, index) in fields" :key="index">
      <span v-if="line.key" class="k">{{ line.key }}</span>
      <input
        v-if="line.key"
        :class="['field', { mono: line.key === 'name' }]"
        :value="line.value"
        :aria-label="line.key"
        :disabled="disabled"
        @input="onInput(index, ($event.target as HTMLInputElement).value)"
      />
      <span v-else class="k raw">{{ line.raw }}</span>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

/**
 * The YAML front matter of a Markdown file (a SKILL.md's `name`/`description`,
 * say) shown as a row of fields above the WYSIWYG editor, the way the design
 * preview does it — the editor body only ever sees the prose, and the block is
 * stitched back on save.
 *
 * Round-trip safety is the point: a line is re-serialised only when the user
 * actually edits it, so opening a file and touching nothing writes back the
 * original bytes, comments and spacing intact.
 */
const props = defineProps<{ modelValue: string; disabled?: boolean }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: string): void }>()

interface Field {
  raw: string
  key: string | null
  value: string
}

const fields = computed<Field[]>(() => props.modelValue.split('\n').map(parseLine))

function parseLine(line: string): Field {
  // A `key: value` line, allowing any non-colon key; anything else (a comment,
  // a blank line, a nested block) is shown read-only and kept verbatim.
  const match = /^([^\s:][^:]*?):[ \t]*(.*)$/.exec(line)
  if (!match) return { raw: line, key: null, value: line }
  return { raw: line, key: match[1].trim(), value: match[2] }
}

function onInput(index: number, value: string) {
  const line = fields.value[index]
  if (!line.key) return
  const next = props.modelValue.split('\n')
  next[index] = `${line.key}: ${value}`
  emit('update:modelValue', next.join('\n'))
}
</script>

<style scoped>
.mde-props {
  display: grid;
  grid-template-columns: 84px minmax(0, 1fr);
  gap: 6px 10px;
  align-items: center;
  padding: 10px 16px;
  border-bottom: 1px solid var(--forebrain-divider);
  background: var(--forebrain-bg-alt);
}
.mde-props .k {
  color: var(--forebrain-muted-text);
  font-family: var(--forebrain-mono-font);
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mde-props .field {
  height: 28px;
  width: 100%;
  box-sizing: border-box;
  border: 1px solid var(--forebrain-divider);
  border-radius: 6px;
  background: var(--forebrain-input-bg);
  padding: 0 8px;
  font-size: 12.5px;
  color: var(--forebrain-text);
}
.mde-props .field.mono {
  font-family: var(--forebrain-mono-font);
}
</style>
