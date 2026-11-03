<template>
  <!-- The identity block (plan 200 §3): the one emphasis element that means
       "another agent". Square, in that agent's own tone; a subagent is an
       outlined block in its parent's tone, and the reader's own mark is the
       brand's circle. The two characters and the tone both come from the
       engine — a surface never derives one. -->
  <span
    class="peer-mark"
    :class="[`peer-mark--${kind}`, `peer-mark--t${tone}`]"
    :style="{ '--peer-tone': toneColor }"
    :title="name || undefined"
    :aria-label="name || undefined"
    data-testid="peer-mark"
  >{{ mark }}</span>
</template>

<script setup lang="ts">
/**
 * One agent's identity block (plan 200 §3). It is deliberately the only
 * coloured square in the interface: a circular node belongs to this session's
 * own steps, so a square is how the reader tells somebody else from the work in
 * hand at a glance.
 *
 * The tone is an index into the four identity colours, not a colour itself, so
 * the palette can change without a wire change; the mark is the two characters
 * the engine derived from the agent's name.
 */
import { computed } from 'vue'

const props = withDefaults(defineProps<{
  /** The two characters the engine derived from the agent's name. */
  mark: string
  /** Index into the four identity tones (0..3). */
  tone?: number
  /** The agent's full name, for a reader who cannot see the block. */
  name?: string
  /** lg is the drawer's size; the default is the one a card header carries. */
  size?: 'md' | 'lg'
  /** sub is a subagent: outlined in its parent's tone rather than filled. */
  kind?: 'agent' | 'sub' | 'me'
}>(), { tone: 0, name: '', size: 'md', kind: 'agent' })

/**
 * The four identity tones, in the order the engine numbers them. They are the
 * same low-saturation band as the brand's navy, so a block never shouts over
 * the card it sits on.
 */
const TONES = ['#0E6E7E', '#6B4C9A', '#45607D', '#4F6B3A'] as const

const toneColor = computed(() => TONES[Math.abs(props.tone) % TONES.length] ?? TONES[2])
</script>

<style scoped>
.peer-mark {
  --peer-tone: #45607d;
  flex: none;
  display: inline-grid;
  place-items: center;
  width: 20px;
  height: 20px;
  box-sizing: border-box;
  border-radius: 6px;
  background: var(--peer-tone);
  color: #ffffff;
  font-size: 9.5px;
  font-weight: 700;
  letter-spacing: 0.02em;
  line-height: 1;
  text-transform: lowercase;
}

.peer-mark--lg {
  width: 28px;
  height: 28px;
  border-radius: 8px;
  font-size: 11.5px;
}

/* A subagent is the same agent's colour, drawn as an outline: it belongs to its
   parent, and a second fill would read as a second process. */
.peer-mark--sub {
  background: var(--forebrain-surface);
  color: var(--peer-tone);
  box-shadow: inset 0 0 0 1.5px var(--peer-tone);
}

/* The reader's own mark is the brand's, and the only circle: a circle is this
   session's own steps on the activity line. */
.peer-mark--me {
  background: var(--forebrain-brand-1);
  border-radius: 50%;
  font-size: 9px;
}
</style>
