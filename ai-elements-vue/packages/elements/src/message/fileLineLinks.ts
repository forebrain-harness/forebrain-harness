/**
 * Prose file:line-range links (plan file-line-range-links): the one place a
 * `pkg/config/llm_models.go:338-343` in an assistant's sentence becomes a
 * clickable reference that opens the file at those lines.
 *
 * The path character class is workbuddy's (`[\w./-]+`); the linkify itself is
 * forebrain's own — workbuddy only jumps to search matches and fenced-code
 * ranges, never to a path written in prose. Two whitelists guard the basename
 * so a CLF timestamp (`26/Nov/2026:10`) or a docker reference
 * (`mcr.microsoft.com/dotnet/sdk:8.0`) never becomes a link: a basename must
 * carry a whitelisted extension or be a whitelisted name outright.
 *
 * This module lives beside MessageResponse because that is the component that
 * enables it for every consumer; the app reaches it through
 * frontend/src/lib/fileLineLinks.ts, which re-exports it and adds the host
 * wiring. Fenced code blocks are `code` nodes and are never touched.
 */
import {
  defineComponent,
  h,
  inject,
  markRaw,
  type Component,
  type InjectionKey,
  type PropType,
  type VNodeChild,
} from 'vue'
import { NODE_RENDERERS, type NodeRenderers } from 'vue-stream-markdown'

/** One file reference a prose link carries. Lines are 1-based; `to` >= `from`. */
export interface FileLineRef {
  path: string
  from: number
  to: number
}

/** What a conversation page does with a clicked reference. */
export type OpenFileLine = (ref: FileLineRef) => void

/**
 * The click handler the conversation page provides for its whole subtree.
 * Where nothing provides it, a reference stays plain text — a link that cannot
 * open anything is not drawn.
 */
export const openFileLineKey: InjectionKey<OpenFileLine> = Symbol('forebrain.openFileLine')

/**
 * `path:from` or `path:from-to` (hyphen, en dash or em dash). The lookbehind
 * refuses a path glued to a URL scheme, a Windows drive, a `~` home or another
 * path segment, so `https://x/a.go:1`, `C:\a.go:1` and `~/a.go:1` stay text.
 */
const FILE_LINE = /(?<![\w/:.\\~])([\w./-]+):(\d+)(?:[-–—](\d+))?/g

/** Extensions a link may name, compared lowercased (README.MD links too). */
const EXTENSIONS = new Set(
  ('go mod sum ts tsx js jsx mjs cjs vue svelte py pyi rs java c h cpp cc cxx hpp hh cs rb php swift kt kts scala sh bash zsh fish sql json jsonc yaml yml toml xml html htm css scss sass less md markdown mdx txt csv tsv proto graphql gql gradle properties ini cfg conf lock tf hcl ex exs erl hs ml clj lua pl pm r jl dart groovy vb fs fsx pas asm s v sv tcl awk vim el lisp scm ps1 psm1 bat cmd work').split(' '),
)

/** Whole basenames a link may name without an extension, case-sensitive. */
const NAMES = new Set([
  'Makefile', 'Dockerfile', 'README', 'LICENSE', 'CHANGELOG', 'CONTRIBUTING',
  'Gemfile', 'Rakefile', 'Procfile', 'Vagrantfile',
  '.gitignore', '.editorconfig', '.env', '.env.local', '.npmrc', '.nvmrc', 'go.work',
])

/** An IP or a ratio (`192.168.1.1:80`, `3:2`) is digits and dots, not a path. */
const NUMERIC_DOTTED = /^\d+(?:\.\d+)*$/

/** A range longer than this is a misread number, not a place to jump to. */
const MAX_SPAN = 2000

/**
 * The guard every candidate passes before it becomes a link: no `..` climb,
 * no numeric-dotted path, a whitelisted basename, and sane 1-based lines.
 * `to` defaults to `from`. Returns null for anything a link must not carry.
 */
export function fileLineRefOf(path: string, from: number, to: number = from): FileLineRef | null {
  if (!path || path.includes('..') || NUMERIC_DOTTED.test(path)) return null
  const base = path.slice(path.lastIndexOf('/') + 1)
  const dot = base.lastIndexOf('.')
  const ext = dot > 0 ? base.slice(dot + 1).toLowerCase() : ''
  const named = NAMES.has(base) || base.startsWith('Dockerfile.')
  if (!named && !EXTENSIONS.has(ext)) return null
  if (!Number.isInteger(from) || from < 1) return null
  if (!Number.isInteger(to) || to < from || to - from + 1 > MAX_SPAN) return null
  return { path, from, to }
}

/** One stretch of a prose node: plain text, or text that carries a reference. */
export interface FileLineSegment {
  text: string
  ref?: FileLineRef
}

/**
 * Splits a text or inline-code node's value into plain stretches and linkable
 * references. The segments always cover the whole value in order; a value with
 * no reference comes back as one plain segment.
 */
export function splitFileLineLinks(value: string): FileLineSegment[] {
  const segments: FileLineSegment[] = []
  let last = 0
  for (const match of value.matchAll(FILE_LINE)) {
    const from = Number(match[2])
    const ref = fileLineRefOf(match[1], from, match[3] === undefined ? from : Number(match[3]))
    if (!ref) continue
    const start = match.index ?? 0
    if (start > last) segments.push({ text: value.slice(last, start) })
    segments.push({ text: match[0], ref })
    last = start + match[0].length
  }
  if (last < value.length) segments.push({ text: value.slice(last) })
  return segments.length ? segments : [{ text: value }]
}

/**
 * The reference itself: a button-shaped anchor, keyboard reachable, that hands
 * the reference to the page's handler. It carries no href — the page opens the
 * file, the browser never navigates.
 */
function fileLineAnchor(segment: FileLineSegment & { ref: FileLineRef }, open: OpenFileLine) {
  return h('a', {
    'data-stream-markdown': 'file-line',
    role: 'button',
    tabindex: 0,
    title: segment.text,
    onClick: (event: MouseEvent) => {
      event.preventDefault()
      open(segment.ref)
    },
    onKeydown: (event: KeyboardEvent) => {
      if (event.key === 'Enter' || event.key === ' ') {
        event.preventDefault()
        open(segment.ref)
      }
    },
  }, segment.text)
}

/**
 * A text or inline-code renderer that keeps the built-in shell — the same tag
 * and `data-stream-markdown` mark — and linkifies the references inside it.
 * With no handler provided the value renders exactly as the built-in does.
 */
function proseRenderer(tag: 'span' | 'code', marker: 'text' | 'inline-code', name: string): Component {
  return markRaw(defineComponent({
    name,
    inheritAttrs: false,
    props: {
      node: { type: Object as PropType<{ value?: unknown }>, required: true },
    },
    setup(props) {
      const open = inject(openFileLineKey, undefined)
      return () => {
        const value = String(props.node.value ?? '')
        const children: VNodeChild[] = []
        if (open) {
          for (const segment of splitFileLineLinks(value)) {
            children.push(segment.ref ? fileLineAnchor(segment as FileLineSegment & { ref: FileLineRef }, open) : segment.text)
          }
        } else {
          children.push(value)
        }
        return h(tag, { 'data-stream-markdown': marker }, children)
      }
    },
  }))
}

/**
 * The built-in link renderer with its children de-linkified: a reference
 * written inside a markdown link's label (`[a.go:1](url)`) must not nest a
 * second link inside the first. The children see the original plain text and
 * inline-code renderers; the link itself keeps its href and behaviour.
 */
const FileLineLink = markRaw(defineComponent({
  name: 'FileLineLink',
  inheritAttrs: false,
  setup(_props, { attrs }) {
    return () => h(NODE_RENDERERS.link as Component, {
      ...attrs,
      nodeRenderers: {
        ...(attrs.nodeRenderers as NodeRenderers | undefined),
        text: NODE_RENDERERS.text,
        inlineCode: NODE_RENDERERS.inlineCode,
      },
    })
  },
}))

/**
 * The renderer overrides MessageResponse enables for every consumer. Call it
 * once per component instance: the returned components are stable, so merging
 * them into a computed does not remount anything.
 */
export function fileLineRenderers(): NodeRenderers {
  return {
    text: proseRenderer('span', 'text', 'FileLineText'),
    inlineCode: proseRenderer('code', 'inline-code', 'FileLineInlineCode'),
    link: FileLineLink,
  }
}