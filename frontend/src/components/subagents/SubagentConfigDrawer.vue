<template>
  <ForebrainDrawer :open="open" :title="title" :readonly="readonly" @close="requestClose">
    <!-- The form draws only once the record and the agent's own model list are
         both in: a form built from half an answer would save what it never
         showed, and for a built-in that means writing a prompt nobody edited. -->
    <template v-if="loaded">
      <div class="sa-grid2">
        <label class="sa-field">
          <span class="sa-label">{{ t('subagents.fieldName') }}</span>
          <Input
            v-model="form.name"
            class="forebrain-field h-[34px] font-mono"
            :readonly="!creating"
            :placeholder="creating ? t('subagents.namePlaceholder') : ''"
            autocomplete="off"
            data-testid="subagent-name"
          />
          <span v-if="creating && form.name !== '' && !nameValid" class="sa-hint sa-hint-bad" data-testid="subagent-name-invalid">
            {{ t('subagents.nameInvalid') }}
          </span>
        </label>
        <!-- The label is its own element rather than a wrapper: the control is
             the library's Select, a button, and a button inside a `label` would
             answer one click with two. -->
        <div class="sa-field">
          <Label for="subagent-model" class="sa-label block text-[12px] leading-[normal]">{{ t('subagents.fieldModel') }}</Label>
          <!-- The first entry is this agent's own default — the model a
               conversation runs on until it pins another — and the rest are the
               entries the agent lists. Following the primary agent is the empty
               string on the wire, which a SelectItem may not carry: the sentinel
               stands in for it on both sides. -->
          <Select :model-value="toChoice(form.model)" @update:model-value="onModelChange">
            <SelectTrigger
              id="subagent-model"
              class="forebrain-field !h-[34px] w-full"
              :disabled="readonly"
              data-testid="subagent-model"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem :value="EMPTY_CHOICE">{{ followLabel }}</SelectItem>
              <SelectItem v-for="option in modelChoices" :key="modelKey(option)" :value="modelKey(option)">
                {{ modelKey(option) }}
              </SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      <div class="sa-grid2">
        <div class="sa-field">
          <Label for="subagent-effort" class="sa-label block text-[12px] leading-[normal]">{{ t('subagents.fieldEffort') }}</Label>
          <!-- An effort belongs to the model it was chosen for: while the model
               follows the primary agent, so does the effort. An empty tier is
               the empty string on the wire (the provider applies its own), hence
               the sentinel. -->
          <Select
            :model-value="toChoice(form.reasoningEffort)"
            @update:model-value="(value) => (form.reasoningEffort = fromChoice(value))"
          >
            <SelectTrigger
              id="subagent-effort"
              class="forebrain-field !h-[34px] w-full"
              :disabled="readonly || !canPickEffort"
              :aria-label="t('subagents.fieldEffort')"
              v-tip:top="effortTip"
              data-testid="subagent-effort"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem :value="EMPTY_CHOICE">{{
                canPickEffort ? t('subagents.effortFollowModel') : t('subagents.followPrimary')
              }}</SelectItem>
              <SelectItem v-for="effort in effortChoices" :key="effort" :value="effort">{{ effortLevelLabel(effort) }}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div class="sa-field">
          <Label for="subagent-mode" class="sa-label block text-[12px] leading-[normal]">{{ t('subagents.fieldMode') }}</Label>
          <!-- A SelectItem carries a string, and this field is a boolean on the
               wire: the two words are its two values, converted back on the way
               in, so the form field stays `true`/`false`. -->
          <Select
            v-if="custom && !readonly"
            :model-value="form.continuable ? 'on' : 'off'"
            @update:model-value="(value) => (form.continuable = String(value) !== 'off')"
          >
            <SelectTrigger id="subagent-mode" class="forebrain-field !h-[34px] w-full" data-testid="subagent-mode">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="on">{{ t('subagents.contRun') }}</SelectItem>
              <SelectItem value="off">{{ t('subagents.contOneShot') }}</SelectItem>
            </SelectContent>
          </Select>
          <!-- A built-in's run mode is its type, not this agent's choice. -->
          <Input
            v-else
            id="subagent-mode"
            class="forebrain-field h-[34px]"
            :model-value="form.continuable ? t('subagents.contRun') : t('subagents.contOneShot')"
            readonly
            data-testid="subagent-mode"
          />
        </div>
      </div>

      <label class="sa-field">
        <span class="sa-label">{{ t('subagents.fieldDescription') }}</span>
        <Input
          v-if="custom"
          v-model="form.description"
          class="forebrain-field h-[34px]"
          :readonly="readonly"
          :placeholder="t('subagents.descriptionPlaceholder')"
          autocomplete="off"
          data-testid="subagent-description"
        />
        <!-- A built-in's and a system subagent's description is the harness's,
             said in the page's own words. -->
        <Input v-else class="forebrain-field h-[34px]" :model-value="describedText" readonly data-testid="subagent-description" />
      </label>

      <label v-if="kind === 'system'" class="sa-field">
        <span class="sa-label">{{ t('subagents.fieldWhen') }}</span>
        <Input class="forebrain-field h-[34px]" :model-value="whenText" readonly data-testid="subagent-when" />
      </label>

      <template v-if="custom">
        <RadioField
          :model-value="form.tools"
          :label="t('subagents.fieldTools')"
          name="subagent-tools"
          :options="toolTiers"
          vertical
          @update:model-value="chooseTools"
        />
        <!-- The chosen set, once the tier asks for one: every tool the runtime
             offers except subagent_*, which no subagent may dispatch. -->
        <div v-if="form.tools === 'pick'" class="sa-pick" data-testid="subagent-picker">
          <Input
            v-model="toolQuery"
            class="forebrain-field h-[34px] sa-pick-search"
            :placeholder="t('subagents.pickSearch')"
            autocomplete="off"
            data-testid="subagent-pick-search"
          />
          <div class="sa-pick-grid">
            <div v-for="name in pickableTools" :key="name" class="sa-chk">
              <Checkbox
                :id="`subagent-tool-${name}`"
                :model-value="form.allowedTools.includes(name)"
                :disabled="readonly"
                @update:model-value="toggleTool(name)"
              />
              <Label :for="`subagent-tool-${name}`" class="sa-chk-name font-normal text-[12.5px] leading-[normal]">
                <span class="font-mono">{{ name }}</span>
              </Label>
            </div>
          </div>
          <span class="sa-hint" data-testid="subagent-pick-note">{{ t('subagents.pickNote', { n: form.allowedTools.length }) }}</span>
        </div>
      </template>
      <label v-else class="sa-field">
        <span class="sa-label">{{ t('subagents.fieldTools') }}</span>
        <Input class="forebrain-field h-[34px]" :model-value="fixedToolsText" readonly data-testid="subagent-tools-fixed" />
      </label>

      <div class="sa-field">
        <span class="sa-label sa-plab">
          <span>{{ t('subagents.fieldPrompt') }}</span>
          <BadgeComponent v-if="overridden" variant="warning" data-testid="subagent-overridden">
            {{ t('subagents.promptOverridden') }}
          </BadgeComponent>
          <button
            v-if="overridden && !readonly && detail?.defaultPrompt"
            type="button"
            class="sa-link"
            data-testid="subagent-restore"
            @click="restoreDefault"
          >
            {{ t('subagents.resetDefault') }}
          </button>
        </span>
        <!-- The text that will be injected, word for word: a built-in's own
             prompt, this agent's rewrite of it, or a custom subagent's own. -->
        <Textarea
          v-model="form.prompt"
          class="forebrain-field min-h-0 font-mono sa-prompt"
          :readonly="readonly"
          :placeholder="custom ? t('subagents.promptPlaceholder') : ''"
          data-testid="subagent-prompt"
        />
      </div>

      <span class="sa-note" data-testid="subagent-note">{{ footerNote }}</span>
    </template>

    <!-- The server's own words: a 409 names the entry that is already there, a
         400 names the rule a field broke. -->
    <div v-if="failure" class="forebrain-err-block" role="alert" data-testid="subagent-error">
      <CircleAlert class="size-3.5" aria-hidden="true" />
      <div>
        <b v-if="failureIsSave">{{ t('mcp.saveFailed') }}</b>
        <span class="forebrain-err-s">{{ failure }}</span>
      </div>
    </div>

    <template #footer>
      <Button type="button" class="forebrain-btn forebrain-btn-ghost h-auto text-xs" data-testid="subagent-cancel" @click="requestClose">
        {{ editable ? t('common.cancel') : t('common.close') }}
      </Button>
      <Button
        v-if="editable"
        type="button"
        class="forebrain-btn forebrain-btn-primary h-auto text-xs"
        :disabled="saving || !loaded || (creating && !nameValid)"
        data-testid="subagent-save"
        @click="save"
      >
        {{ saving ? t('mcp.saving') : creating ? t('common.create') : t('common.save') }}
      </Button>
    </template>
  </ForebrainDrawer>
</template>

<script setup lang="ts">
/**
 * One subagent as this primary agent configures it (plan 079), in the same
 * right-hand drawer the MCP page uses: the fields in the prototype's `subForm`
 * order. A custom subagent is written whole; a built-in takes only the model,
 * the effort and the prompt — its switch stays the table's, which is why the
 * form never carries one; a system subagent, which nobody configures, is
 * readable and nothing else.
 *
 * The contract with the page:
 *
 *   open      drawn while true; the record is read each time it turns true;
 *   agentId   the primary agent the write goes under;
 *   name      the subagent being edited or viewed, absent when creating one;
 *   mode      'create' | 'edit' | 'view' — 'view' is the shell's read-only
 *             drawer, which is what a system subagent opens in;
 *   close     the operator cancelled, pressed Escape or clicked the mask;
 *   saved     a write went through, naming what was written; the table reloads.
 *
 * Two rules the form holds rather than the server:
 *
 *   - a built-in's prompt travels only when the operator changed it. Sending
 *     the unchanged text would pin it as this agent's own rewrite, and the
 *     built-in would stop following the harness's text.
 *   - 恢复默认 sends `reset_prompt` and no prompt at all: it clears the rewrite
 *     and leaves the model this agent pinned exactly where it was.
 */
import { computed, reactive, ref, watch } from 'vue'
import { CircleAlert } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import { Checkbox } from '@repo/shadcn-vue/components/ui/checkbox'
import { Input } from '@repo/shadcn-vue/components/ui/input'
import { Label } from '@repo/shadcn-vue/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@repo/shadcn-vue/components/ui/select'
import { Textarea } from '@repo/shadcn-vue/components/ui/textarea'

import BadgeComponent from '@/components/common/BadgeComponent.vue'
import ForebrainDrawer from '@/components/common/ForebrainDrawer.vue'
import RadioField from '@/components/common/RadioField.vue'
import { effortLevelLabel } from '@/lib/effortLabel'
import { EMPTY_CHOICE, fromChoice, toChoice } from '@/lib/selectValue'
import {
  forebrainApi,
  getErrorMessage,
  type SessionModelOption,
  type SubagentConfigBody,
  type SubagentConfigDetail,
  type SubagentTools,
} from '@/lib/api'
import { useI18n, type I18nKey } from '@/locales'

const props = defineProps<{
  /** Drawn while true; the record is read whenever it turns true. */
  open: boolean
  /** The primary agent the form writes under. */
  agentId: string
  /** The subagent being edited or viewed; absent when creating one. */
  name?: string
  /** 'create' adds one, 'edit' changes one, 'view' only reads one. */
  mode: 'create' | 'edit' | 'view'
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved', name: string): void
}>()

const { t } = useI18n()

/** A subagent's name, exactly as the server's own validator reads it. */
const NAME_PATTERN = /^[a-z][a-z0-9-]{1,62}$/

interface SubagentForm {
  name: string
  description: string
  /** `provider/model` as stored, empty while it follows the primary agent. */
  model: string
  reasoningEffort: string
  tools: SubagentTools
  allowedTools: string[]
  continuable: boolean
  prompt: string
}

const readonly = computed(() => props.mode === 'view')
const editable = computed(() => !readonly.value)
const creating = computed(() => props.mode === 'create')
/** The name the route carries: the record's identity, and what the title names. */
const subject = computed(() => props.name ?? '')

const detail = ref<SubagentConfigDetail | null>(null)
/** The agent's own models: the follow label and each model's efforts come here. */
const options = ref<SessionModelOption[]>([])
/** Every tool a subagent may be given; read once per open, when the tier asks. */
const toolNames = ref<string[]>([])
const toolQuery = ref('')
/** True once the record and the model list are in; the form waits for it. */
const loaded = ref(false)
const saving = ref(false)
/** The last failure in the server's own words. */
const failure = ref('')
/** Whether that failure was a refused save (shown as 没有保存：) or a read. */
const failureIsSave = ref(false)
/** The prompt the drawer opened with, so an untouched one is never written. */
const openedPrompt = ref('')
/** 恢复默认 was pressed: the save clears the rewrite instead of writing a text. */
const resetPrompt = ref(false)

const form = reactive<SubagentForm>(emptyForm())

let toolsRequested = false

function emptyForm(): SubagentForm {
  return {
    name: subject.value,
    description: '',
    model: '',
    reasoningEffort: '',
    tools: 'read_only',
    allowedTools: [],
    continuable: true,
    prompt: '',
  }
}

/** What kind of subagent the form is for: a create only makes a custom one. */
const kind = computed(() => (creating.value ? 'custom' : (detail.value?.kind ?? 'custom')))
const custom = computed(() => kind.value === 'custom')

const title = computed(() => {
  if (creating.value) return t('subagents.drawerNew')
  if (readonly.value) return t('subagents.drawerViewSystem', { name: subject.value })
  if (kind.value === 'builtin') return t('subagents.drawerEditBuiltin', { name: subject.value })
  return t('subagents.drawerEdit', { name: subject.value })
})

const nameValid = computed(() => NAME_PATTERN.test(form.name.trim()))

function modelKey(option: SessionModelOption): string {
  return `${option.provider}/${option.model}`
}

/** The models the select offers. A row may pin a model this agent no longer
 * lists (the legacy per-type entry is one of them); it stays offered, so
 * opening the record and saving it leaves that pin where it was. */
const modelChoices = computed<SessionModelOption[]>(() => {
  const pinned = detail.value
  const key = pinned && pinned.model ? `${pinned.modelProvider}/${pinned.model}` : ''
  if (key === '' || options.value.some((option) => modelKey(option) === key)) return options.value
  return [...options.value, { provider: pinned?.modelProvider ?? '', model: pinned?.model ?? '', label: key, efforts: [] }]
})

const canPickEffort = computed(() => form.model !== '')

const selectedModel = computed(() => modelChoices.value.find((option) => modelKey(option) === form.model) ?? null)

/** The efforts the chosen model takes; a stored effort the catalog no longer
 * names stays on the list, so it cannot silently turn into the default. */
const effortChoices = computed(() => {
  const listed = selectedModel.value?.efforts ?? []
  const current = form.reasoningEffort
  return current !== '' && !listed.includes(current) ? [current, ...listed] : listed
})

const effortTip = computed(() => (canPickEffort.value || readonly.value ? '' : t('subagents.effortNeedsModel')))

/** 跟随主代理, naming the model the conversation runs on when the agent lists one. */
const followLabel = computed(() => {
  const first = options.value[0]
  return first ? t('subagents.modelFollowPrimary', { model: modelKey(first) }) : t('subagents.followPrimary')
})

const toolTiers = computed(() => [
  { value: 'read_only', label: t('subagents.toolsReadOnly'), description: t('subagents.toolsReadOnlyHint') },
  { value: 'edit', label: t('subagents.toolsEdit'), description: t('subagents.toolsEditHint') },
  { value: 'pick', label: t('subagents.toolsPickLabel'), description: t('subagents.toolsPickHint') },
])

/** The tools the search box leaves, in the catalog's own order. */
const pickableTools = computed(() => {
  const needle = toolQuery.value.trim().toLowerCase()
  if (needle === '') return toolNames.value
  return toolNames.value.filter((name) => name.toLowerCase().includes(needle))
})

/** A built-in's or a system subagent's tool tier as words: fixed by its type. */
const fixedToolsText = computed(() => t('subagents.builtinToolsFixed', { tools: tierLabel(form.tools) }))

/** The page's own words for a built-in or a system subagent, falling back to
 * the definition's English where the page has none. */
const describedText = computed(() => localeOr(`subagents.desc.${subject.value}`, detail.value?.description ?? ''))
const whenText = computed(() => localeOr(`subagents.when.${subject.value}`, ''))

const overridden = computed(() => Boolean(detail.value?.promptOverridden) && !resetPrompt.value)

const footerNote = computed(() => {
  if (readonly.value) return t('subagents.footerSystem')
  if (kind.value === 'builtin') return t('subagents.footerBuiltin', { id: props.agentId })
  return t('subagents.footerCustom', { id: props.agentId })
})

/** A locale sentence, or the fallback when the page has no entry for the name
 * (a subagent the harness added before this page was told about it). */
function localeOr(key: string, fallback: string): string {
  const text = t(key as I18nKey)
  return text === key ? fallback : text
}

function tierLabel(tier: string): string {
  switch (tier) {
    case 'edit':
      return t('subagents.toolsEdit')
    case 'pick':
      return t('subagents.toolsPick', { n: form.allowedTools.length })
    case 'none':
      return t('subagents.toolsNone')
    default:
      return t('subagents.toolsReadOnly')
  }
}

function chooseTools(value: string) {
  form.tools = value as SubagentTools
}

function toggleTool(name: string) {
  form.allowedTools = form.allowedTools.includes(name)
    ? form.allowedTools.filter((tool) => tool !== name)
    : [...form.allowedTools, name]
}

/** An effort is a property of the model chosen beside it: switching to another
 * model drops one it does not take, and following the primary agent drops it
 * too. */
function onModelChange(choice: unknown) {
  form.model = fromChoice(choice)
  const efforts = modelChoices.value.find((option) => modelKey(option) === form.model)?.efforts ?? []
  if (form.reasoningEffort !== '' && !efforts.includes(form.reasoningEffort)) form.reasoningEffort = ''
}

/** 恢复默认: the box shows the harness's own text, and the save says "clear the
 * rewrite". Typing into it again makes it a rewrite once more. */
function restoreDefault() {
  form.prompt = detail.value?.defaultPrompt ?? ''
  resetPrompt.value = true
}

function applyDetail(record: SubagentConfigDetail) {
  form.name = record.name
  form.description = record.description
  form.model = record.model ? `${record.modelProvider}/${record.model}` : ''
  form.reasoningEffort = record.reasoningEffort
  form.tools = record.tools
  form.allowedTools = [...record.allowedTools]
  form.continuable = record.continuable
  form.prompt = record.systemPrompt
  openedPrompt.value = record.systemPrompt
}

/** Read the record and the agent's models. Both, or nothing: the form is not
 * shown until they are in, because a save writes every visible field. */
async function load() {
  loaded.value = false
  detail.value = null
  failure.value = ''
  failureIsSave.value = false
  resetPrompt.value = false
  openedPrompt.value = ''
  toolQuery.value = ''
  toolNames.value = []
  toolsRequested = false
  Object.assign(form, emptyForm())
  try {
    const record: Promise<SubagentConfigDetail | null> = creating.value
      ? Promise.resolve(null)
      : forebrainApi.subagentConfig(props.agentId, subject.value)
    const [loadedRecord, models] = await Promise.all([record, forebrainApi.subagentModelOptions(props.agentId)])
    options.value = Array.isArray(models) ? models : []
    if (loadedRecord) {
      detail.value = loadedRecord
      applyDetail(loadedRecord)
    }
    loaded.value = true
  } catch (error) {
    failure.value = getErrorMessage(error)
    failureIsSave.value = false
  }
}

/** Every tool the runtime offers, without the subagent_* family a subagent can
 * neither call nor dispatch. The endpoint pages, so every page is read. */
async function ensureTools() {
  if (toolsRequested) return
  toolsRequested = true
  const names: string[] = []
  let read = 0
  try {
    for (let page = 1; ; page += 1) {
      const answer = await forebrainApi.toolsPage({ page, pageSize: 100 })
      read += answer.rows.length
      for (const row of answer.rows) {
        if (!row.name.startsWith('subagent_')) names.push(row.name)
      }
      if (answer.rows.length === 0 || read >= answer.total) break
    }
    toolNames.value = names
  } catch (error) {
    // Switching the tier away and back is the retry, so the request may run again.
    toolsRequested = false
    failure.value = getErrorMessage(error)
    failureIsSave.value = false
  }
}

function requestClose() {
  // A save in flight owns the drawer: its answer decides what happens next.
  if (saving.value) return
  emit('close')
}

/** A custom subagent is written whole. */
function customBody(): SubagentConfigBody {
  return {
    description: form.description.trim(),
    model: form.model,
    reasoningEffort: form.reasoningEffort,
    systemPrompt: form.prompt.trim(),
    tools: form.tools,
    allowedTools: form.tools === 'pick' ? [...form.allowedTools] : [],
    continuable: form.continuable,
  }
}

/** A built-in takes the model, the effort and its prompt — and its prompt only
 * when it was rewritten here. */
function builtinBody(): SubagentConfigBody {
  const body: SubagentConfigBody = { model: form.model, reasoningEffort: form.reasoningEffort }
  if (resetPrompt.value) body.resetPrompt = true
  else if (form.prompt.trim() !== openedPrompt.value.trim()) body.systemPrompt = form.prompt.trim()
  return body
}

async function save() {
  if (!editable.value || saving.value) return
  if (creating.value && !nameValid.value) return
  saving.value = true
  failure.value = ''
  failureIsSave.value = false
  try {
    if (creating.value) {
      const name = form.name.trim()
      await forebrainApi.createSubagentConfig(props.agentId, { name, ...customBody() })
      emit('saved', name)
    } else {
      await forebrainApi.updateSubagentConfig(props.agentId, subject.value, custom.value ? customBody() : builtinBody())
      emit('saved', subject.value)
    }
  } catch (error) {
    failure.value = getErrorMessage(error)
    failureIsSave.value = true
  } finally {
    saving.value = false
  }
}

watch(
  () => props.open,
  (open) => {
    if (open) void load()
  },
  { immediate: true },
)

// The picker's tool list is read the first time a tier asks for it.
watch(
  () => form.tools,
  (tier) => {
    if (tier === 'pick') void ensureTools()
  },
)

// Typing after 恢复默认 is a rewrite again, so the save writes it rather than
// clearing the one it replaced.
watch(
  () => form.prompt,
  (text) => {
    if (!resetPrompt.value) return
    if (text.trim() !== (detail.value?.defaultPrompt ?? '').trim()) resetPrompt.value = false
  },
)
</script>

<style scoped>
.sa-label {
  font-size: 12px;
  font-weight: 500;
  color: var(--forebrain-text-2);
}

.sa-field {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.sa-grid2 {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.sa-hint {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}

.sa-hint-bad {
  color: var(--forebrain-danger);
}

.sa-note {
  font-size: 12px;
  color: var(--forebrain-muted-text);
}

/* The prompt label's own row: the rewrite badge and the way back from it. */
.sa-plab {
  display: flex;
  align-items: center;
  gap: 8px;
}

.sa-plab button {
  margin-left: auto;
}

/* A text action inside the drawer, the prototype's .btn.link. */
.sa-link {
  border: 0;
  padding: 0;
  background: transparent;
  color: var(--forebrain-brand-1);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
}

.sa-link:hover {
  color: var(--forebrain-brand-hover);
  text-decoration: underline;
}

/* The prompt itself: tall enough to read a whole built-in one, and the reader
   decides whether it grows. */
.sa-prompt {
  min-height: 320px;
  line-height: 1.6;
  font-size: 12.5px;
  white-space: pre-wrap;
}

/* The tool picker: a boxed search and three columns of names. */
.sa-pick {
  display: grid;
  gap: 8px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  padding: 10px 12px;
}

.sa-pick-search {
  max-width: 220px;
}

.sa-pick-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 6px 12px;
}

.sa-chk {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  font-size: 12.5px;
  color: var(--forebrain-text);
}

/* The name is a label of the box beside it, so the room the ellipsis needs is
   the label's own: the box keeps its library size (16px, plan 096's recorded
   departure from the prototype's 15px native one). */
.sa-chk-name {
  min-width: 0;
  overflow: hidden;
}

.sa-chk span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
