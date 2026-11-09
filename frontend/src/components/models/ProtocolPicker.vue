<template>
  <div ref="rootRef" class="proto">
    <button
      type="button"
      class="proto__head"
      :class="{ 'proto__head--open': open }"
      aria-haspopup="listbox"
      :aria-expanded="open"
      data-testid="models-protocol-trigger"
      @click="open = !open"
    >
      <!-- The trigger repeats the protocol's name only: the path belongs to the
           option, and a path here would read as part of the stored value. -->
      <span class="proto__name" :class="{ 'proto__name--empty': !current }">
        {{ current ? current.name : t('models.protocolPlaceholder') }}
      </span>
      <ChevronDown class="size-3.5" aria-hidden="true" />
    </button>

    <div v-if="open" class="proto__pop" role="listbox">
      <template v-for="option in options" :key="option.value">
        <div v-if="option.divide" class="proto__divider" />
        <div
          class="proto__opt"
          :class="{ 'proto__opt--on': option.verbatim === Boolean(verbatim) && option.value === (verbatim ? VERBATIM : modelValue) }"
          role="option"
          :aria-selected="option.verbatim === Boolean(verbatim) && option.value === (verbatim ? VERBATIM : modelValue)"
          :data-protocol="option.value"
          @click="pick(option.value)"
        >
          <span class="proto__lead">
            <Check
              v-if="option.verbatim === Boolean(verbatim) && option.value === (verbatim ? VERBATIM : modelValue)"
              class="size-3.5"
              aria-hidden="true"
            />
          </span>
          <span class="proto__opt-body">
            <span class="proto__name">{{ option.name }}</span>
            <span class="proto__path" :class="{ 'proto__path--ui': option.verbatim }">{{ option.detail }}</span>
          </span>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * 「接口协议」 (plan 266): a dropdown of the protocols a model can speak, each
 * option naming itself on one line and its path on the next — the path in the
 * UI font only for 「自定义协议」, which has no path to append.
 *
 * The custom protocol is the fourth option and appears only for a custom
 * provider: a built-in one serves its own paths, so offering "send the address
 * verbatim" there would invite a row that cannot work. A row already storing a
 * path none of the three names keeps it as an extra, already-chosen option, so
 * editing the other fields cannot silently drop it.
 */
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { Check, ChevronDown } from 'lucide-vue-next'

import { useI18n } from '@/locales'

const props = defineProps<{
  /** The chosen path; '*' stands for 「自定义协议」 (the address is the request). */
  modelValue: string
  /** The row stores 「自定义协议」: the address is sent as it is. */
  verbatim?: boolean
  /** Whether 「自定义协议」 is offered (a custom provider). */
  allowVerbatim?: boolean
  /** A path the row stores that the three protocols do not name. */
  storedPath?: string
  /** The three protocol entries the gateway lists, in its order. */
  protocols: { apiPath: string; name: string }[]
}>()

const emit = defineEmits<{ 'update:modelValue': [value: string]; 'update:verbatim': [value: boolean] }>()

const { t } = useI18n()
const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)

/** What 「自定义协议」 is named in the value: no stored path can collide. */
const VERBATIM = '@verbatim'

interface ProtoOption {
  value: string
  name: string
  detail: string
  verbatim?: boolean
  divide?: boolean
}

/**
 * The three known protocols in the order the form offers them — Anthropic
 * Messages, OpenAI Chat Completions, OpenAI Responses — then the custom one.
 * The gateway's listing names the same three; its order is the wire's, not the
 * form's.
 */
const ORDER = ['/v1/messages', '/chat/completions', '/responses']

const options = computed<ProtoOption[]>(() => {
  const known = [...props.protocols].sort(
    (a, b) => ORDER.indexOf(a.apiPath) - ORDER.indexOf(b.apiPath),
  )
  const list: ProtoOption[] = known.map((protocol) => ({
    value: protocol.apiPath,
    name: protocol.name,
    detail: protocol.apiPath,
  }))
  const stored = String(props.storedPath ?? '')
  if (stored && !known.some((protocol) => protocol.apiPath === stored)) {
    list.push({ value: stored, name: t('models.protocolCurrent'), detail: stored })
  }
  if (props.allowVerbatim) {
    list.push({
      value: VERBATIM,
      name: t('models.customProtocol'),
      detail: t('models.customProtocolDesc'),
      verbatim: true,
      divide: true,
    })
  }
  return list
})

const current = computed(() => {
  if (props.verbatim) return options.value.find((option) => option.verbatim) ?? null
  return options.value.find((option) => option.value === props.modelValue && !option.verbatim) ?? null
})

function pick(value: string): void {
  open.value = false
  const verbatim = value === VERBATIM
  emit('update:verbatim', verbatim)
  if (!verbatim) emit('update:modelValue', value)
}

function onDocPointerDown(event: PointerEvent): void {
  if (open.value && rootRef.value && !rootRef.value.contains(event.target as Node)) open.value = false
}

onMounted(() => document.addEventListener('pointerdown', onDocPointerDown))
onUnmounted(() => document.removeEventListener('pointerdown', onDocPointerDown))
</script>

<style scoped>
.proto {
  position: relative;
}
.proto__head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  height: 32px;
  padding: 0 10px;
  border: 1px solid var(--forebrain-divider-strong);
  border-radius: 6px;
  background: var(--forebrain-input-bg);
  font-size: 13px;
  text-align: left;
  color: var(--forebrain-text);
}
.proto__head--open,
.proto__head:focus-visible {
  border-color: var(--forebrain-brand-1);
  box-shadow: 0 0 0 3px var(--forebrain-brand-soft);
  outline: none;
}
.proto__head > :last-child {
  margin-left: auto;
  color: var(--forebrain-muted-text);
}
.proto__name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 500;
}
.proto__name--empty {
  font-weight: 400;
  color: var(--forebrain-muted-text);
}
.proto__pop {
  position: absolute;
  left: 0;
  right: 0;
  top: calc(100% + 6px);
  z-index: 20;
  padding: 4px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 10px;
  background: var(--forebrain-surface);
  box-shadow: 0 12px 32px rgba(16, 24, 40, 0.14);
}
.proto__opt {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 7px 9px;
  border-radius: 7px;
  cursor: pointer;
}
.proto__opt:hover {
  background: var(--forebrain-bg-alt);
}
.proto__lead {
  width: 14px;
  flex: none;
  padding-top: 3px;
  color: var(--forebrain-brand-1);
}
.proto__opt-body {
  display: grid;
  min-width: 0;
}
.proto__opt--on .proto__name {
  color: var(--forebrain-brand-1);
}
.proto__path {
  font-family: var(--forebrain-mono-font);
  font-size: 11.5px;
  line-height: 1.4;
  color: var(--forebrain-muted-text);
  text-align: left;
}
.proto__path--ui {
  font-family: var(--forebrain-ui-font);
  font-size: 12px;
}
.proto__divider {
  height: 1px;
  margin: 4px 8px;
  background: var(--forebrain-divider);
}
</style>
