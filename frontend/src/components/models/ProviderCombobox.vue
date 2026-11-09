<template>
  <Popover :open="open" @update:open="(next) => emit('update:open', next)">
    <PopoverTrigger as-child>
      <button
        type="button"
        class="prov__head"
        :class="{ 'prov__head--open': open }"
        aria-haspopup="listbox"
        :aria-expanded="open"
        data-testid="models-provider-trigger"
      >
        <span class="prov__name" :class="{ 'prov__name--empty': !selected }">
          {{ selected ? selected.name : t('models.providerPlaceholder') }}
        </span>
        <ChevronDown class="size-3.5" aria-hidden="true" />
      </button>
    </PopoverTrigger>
    <PopoverContent class="prov__pop" align="start" :side-offset="6">
      <Command>
        <CommandInput :placeholder="t('models.providerSearch')" data-testid="models-provider-search" />
        <CommandList>
          <CommandEmpty>{{ t('models.providerEmpty') }}</CommandEmpty>
          <CommandGroup :heading="t('models.providerGroupBuiltin')">
            <CommandItem
              v-for="provider in builtin"
              :key="provider.id"
              :value="`${provider.name} ${provider.id}`"
              :data-provider="provider.id"
              @select="pick(provider.id)"
            >
              {{ provider.name }}
              <Check v-if="provider.id === modelValue" class="ml-auto size-3.5" aria-hidden="true" />
            </CommandItem>
          </CommandGroup>
          <CommandGroup :heading="t('models.providerGroupOther')">
            <CommandItem :value="t('models.providerCustom')" :data-provider="customId" @select="pick(customId)">
              {{ t('models.providerCustom') }}
              <small class="prov__hint">{{ t('models.providerCustomHint') }}</small>
              <Check v-if="modelValue === customId" class="ml-auto size-3.5" aria-hidden="true" />
            </CommandItem>
          </CommandGroup>
        </CommandList>
      </Command>
    </PopoverContent>
  </Popover>
</template>

<script setup lang="ts">
/**
 * The form's provider picker (plan 266): a searchable list of the built-in
 * services in the registry's own order, then 「自定义」 — a service not on the
 * list, which is the one entry that may carry no provider name at all.
 *
 * A sign-in-backed provider is not offered here: its rows are written by the
 * subscription's own flow, with an endpoint and a key the person cannot type,
 * so listing it would offer a form that cannot be saved.
 */
import { computed } from 'vue'
import { Check, ChevronDown } from 'lucide-vue-next'
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from '@repo/shadcn-vue/components/ui/command'
import { Popover, PopoverContent, PopoverTrigger } from '@repo/shadcn-vue/components/ui/popover'

import type { LlmProviderOption } from '@/lib/api'
import { useI18n } from '@/locales'

const props = withDefaults(
  defineProps<{
    modelValue: string
    providers: LlmProviderOption[]
    customId?: string
    open: boolean
  }>(),
  { customId: 'custom' },
)

const emit = defineEmits<{ 'update:modelValue': [id: string]; 'update:open': [open: boolean] }>()

const { t } = useI18n()

const builtin = computed(() => props.providers.filter((provider) => !provider.loginBacked && provider.id !== props.customId))
const selected = computed(
  () =>
    props.providers.find((provider) => provider.id === props.modelValue) ??
    (props.modelValue === props.customId ? { id: props.customId, name: t('models.providerCustom') } : null),
)

function pick(id: string): void {
  emit('update:open', false)
  if (id === props.modelValue) return
  emit('update:modelValue', id)
}
</script>

<style scoped>
.prov__head {
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
.prov__head--open,
.prov__head:focus-visible {
  border-color: var(--forebrain-brand-1);
  box-shadow: 0 0 0 3px var(--forebrain-brand-soft);
  outline: none;
}
.prov__head > :last-child {
  margin-left: auto;
  color: var(--forebrain-muted-text);
}
.prov__name {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.prov__name--empty {
  color: var(--forebrain-muted-text);
}
.prov__hint {
  margin-left: auto;
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
}
</style>

<style>
/* The popover is portalled, so its width has to be set where it lands. */
.prov__pop {
  width: min(360px, calc(100vw - 2rem));
  padding: 0;
}
</style>
