<template>
  <div class="relative" ref="rootRef">
    <button
      type="button"
      class="forebrain-chip-btn !h-7 !w-7 !justify-center !px-0"
      :class="{ 'forebrain-chip-btn--pressed': open }"
      :disabled="disabled"
      v-tip:top="t('chat.plusAdd')"
      :aria-label="t('chat.plusAdd')"
      aria-haspopup="menu"
      :aria-expanded="open"
      data-testid="composer-plus"
      @click="open = !open"
    >
      <Plus class="size-4" aria-hidden="true" />
    </button>
    <div v-if="open" class="forebrain-plus-pop" role="menu" :aria-label="t('chat.plusAdd')">
      <div class="forebrain-plus-cap">{{ t('chat.plusAdd') }}</div>
      <button type="button" class="forebrain-plus-opt" role="menuitem" data-testid="plus-attach" @click="attach">
        <Paperclip class="size-3.5 shrink-0" aria-hidden="true" />
        <b>{{ t('chat.plusAttach') }}</b>
      </button>
      <button type="button" class="forebrain-plus-opt" role="menuitem" data-testid="plus-goal" @click="insert('/goal ')">
        <Target class="size-3.5 shrink-0" aria-hidden="true" />
        <b>{{ t('chat.plusGoal') }}</b>
      </button>
      <div class="forebrain-plus-cap">{{ t('chat.plusSkills') }}</div>
      <div class="forebrain-plus-skills">
        <button
          v-for="skill in skills"
          :key="skill.name"
          type="button"
          class="forebrain-plus-opt"
          role="menuitem"
          :data-testid="`plus-skill-${skill.name}`"
          @click="insert(`/${skill.name} `)"
        >
          <Sparkles class="size-3.5 shrink-0" aria-hidden="true" />
          <b class="font-mono">{{ skill.name }}</b>
          <span class="truncate">{{ skill.description }}</span>
        </button>
      </div>
      <div class="forebrain-plus-foot">
        <span><kbd>@</kbd>{{ t('chat.plusFootAt') }}</span>
        <span><kbd>/</kbd>{{ t('chat.plusFootSlash') }}</span>
        <span class="items-center gap-1.5"><Info class="size-3.5" aria-hidden="true" />{{ t('chat.plusFootType') }}</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * The composer's "+" menu: attachments, the /goal command, and this session's
 * skills — the things that add to the next message. Picking a skill inserts
 * its command; attachments go through the composer's own file picker so they
 * upload exactly as a dragged-in file would.
 */
import { onMounted, onUnmounted, ref } from 'vue'
import { Info, Paperclip, Plus, Sparkles, Target } from 'lucide-vue-next'
import { usePromptInput } from '@repo/elements/prompt-input'
import { forebrainApi } from '@/lib/api'
import { useI18n } from '@/locales'

defineProps<{ disabled?: boolean }>()

const emit = defineEmits<{ (e: 'insert', text: string): void }>()

const { t } = useI18n()
const { openFileDialog } = usePromptInput()

const open = ref(false)
const rootRef = ref<HTMLElement | null>(null)
const skills = ref<{ name: string; description: string }[]>([])

async function loadSkills() {
  try {
    const res = await forebrainApi.slashCommands('webchat', '', {})
    skills.value = (Array.isArray(res.records) ? res.records : [])
      .filter((row) => row.category === 'skill' && row.name)
      .map((row) => ({ name: row.name, description: row.description ?? '' }))
  } catch {
    skills.value = []
  }
}

function attach() {
  open.value = false
  openFileDialog()
}

function insert(text: string) {
  open.value = false
  emit('insert', text)
}

function onDocPointerDown(event: PointerEvent) {
  if (open.value && rootRef.value && !rootRef.value.contains(event.target as Node)) open.value = false
}

onMounted(() => {
  document.addEventListener('pointerdown', onDocPointerDown)
  void loadSkills()
})
onUnmounted(() => document.removeEventListener('pointerdown', onDocPointerDown))
</script>

<style scoped>
/* The prototype's .plus-pop: a sheet as wide as the composer, opening above
   it, with the add entries, the session's skills, and the @// hints. */
.forebrain-plus-pop {
  position: absolute;
  left: 0;
  right: 0;
  bottom: calc(100% + 6px);
  background: var(--forebrain-surface);
  border: 1px solid var(--forebrain-divider);
  border-radius: 12px;
  box-shadow: var(--forebrain-shadow-pop);
  z-index: 20;
  padding: 6px;
  display: grid;
  gap: 1px;
  text-align: left;
}

.forebrain-plus-cap {
  font-size: 11.5px;
  color: var(--forebrain-muted-text);
  padding: 6px 10px 2px;
}

.forebrain-plus-opt {
  display: flex;
  align-items: center;
  gap: 10px;
  border: 0;
  background: transparent;
  border-radius: 8px;
  padding: 7px 10px;
  cursor: pointer;
  text-align: left;
  width: 100%;
  color: var(--forebrain-text);
  font-size: 13px;
}

.forebrain-plus-opt:hover,
.forebrain-plus-opt:focus-visible {
  background: var(--forebrain-button-alt-bg);
}

.forebrain-plus-opt b {
  font-weight: 500;
  flex: none;
}

.forebrain-plus-opt span {
  font-size: 12px;
  color: var(--forebrain-muted-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  min-width: 0;
}

.forebrain-plus-opt svg.lucide {
  color: var(--forebrain-text-2);
}

.forebrain-plus-skills {
  max-height: 176px;
  overflow: auto;
  display: grid;
  gap: 1px;
}

.forebrain-plus-foot {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 16px;
  border-top: 1px solid var(--forebrain-divider);
  margin-top: 4px;
  padding: 8px 10px 4px;
  font-size: 12px;
  color: var(--forebrain-text-2);
}

.forebrain-plus-foot span {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.forebrain-plus-foot kbd {
  display: inline-grid;
  place-items: center;
  min-width: 18px;
  height: 18px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 4px;
  font-family: var(--forebrain-mono-font);
  font-size: 11px;
  background: var(--forebrain-button-alt-bg);
}

.forebrain-plus-foot svg.lucide {
  width: 13px;
  height: 13px;
  color: var(--forebrain-muted-text);
}
</style>
