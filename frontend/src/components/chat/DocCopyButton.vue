<template>
  <!-- The prototype's .cp: always there in a document page (opacity 1), a
       plain grey button that turns to a tick once the copy landed. -->
  <Button
    type="button"
    variant="ghost"
    size="icon"
    class="doc-copy-btn"
    :aria-label="props.label"
    v-tip:top="props.label"
    data-testid="doc-copy"
    @click="copy"
  >
    <Check v-if="copied" class="size-4 text-[var(--forebrain-success)]" aria-hidden="true" />
    <Copy v-else class="size-4" aria-hidden="true" />
  </Button>
</template>

<script setup lang="ts">
/**
 * The document page's copy button. The ai-elements' CodeBlockCopyButton is
 * not used — it reads the code it copies out of a CodeBlock's own context, so
 * reusing it would mean rendering this text through CodeBlock's Shiki theme,
 * and the document pages are laid out in the prototype's plain greys, not as
 * highlighted code. The shadcn Button with the two-state icon copies the text
 * it is given, verbatim.
 */
import { ref } from 'vue'
import { Check, Copy } from 'lucide-vue-next'
import { Button } from '@repo/shadcn-vue/components/ui/button'

const props = defineProps<{
  /** What the button does, in the reader's language. */
  label: string
  /** The text copy gives, exactly as it came — never the laid-out form. */
  text: string
}>()

const copied = ref(false)
let restoreTimer: ReturnType<typeof setTimeout> | null = null

async function copy() {
  try {
    await navigator.clipboard.writeText(props.text)
    copied.value = true
    if (restoreTimer) clearTimeout(restoreTimer)
    restoreTimer = setTimeout(() => {
      copied.value = false
    }, 1500)
  } catch {
    // A clipboard the page may not write is not an error the reader can act
    // on: the text stays selectable in place.
  }
}
</script>

<style scoped>
.doc-copy-btn {
  width: 28px;
  height: 28px;
  border-radius: 7px;
  color: var(--forebrain-muted-text);
}

.doc-copy-btn:hover {
  background: rgba(16, 24, 40, 0.06);
  color: var(--forebrain-text);
}
</style>
