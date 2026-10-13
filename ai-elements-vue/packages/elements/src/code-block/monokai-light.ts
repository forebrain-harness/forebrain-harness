import type { ThemeRegistration } from 'shiki'
import { monokaiLightPalette as M } from './palette'

/**
 * Monokai Light — the light counterpart of the Monokai theme every forebrain
 * surface paints code with. Shiki bundles Monokai but no light sibling, so this
 * is Shiki's own Monokai with the palette of the terminal renderer's light
 * theme (chroma's `monokailight`, which Monokai names as its counterpart)
 * substituted colour for colour, leaving the scope table untouched: same
 * scopes, same emphasis, light canvas. Keeping the scopes identical to the dark
 * theme is the point — a file reads the same way in either mode, and in the
 * terminal and the browser alike.
 *
 * The colours come from `monokai-light-palette` (the one place they are named);
 * the substitutions, from Shiki's Monokai to chroma's monokailight, are:
 *
 *   #f8f8f2, #cfcfc2 → M.name       names, punctuation, plain text
 *   #272822          → M.background canvas
 *   #66d9ef          → M.keyword    storage and keyword cyan
 *   #a6e22e          → M.green      functions, classes, attributes
 *   #e6db74, #fd971f → M.string     strings, parameters
 *   #f44747          → M.error      invalid
 *   #6796e6          → #2f6fd0      editor annotation tokens (no palette entry)
 *   #cd9731          → #a06a00
 *   #b267e6          → #7b3fbf
 *   #88846f          → M.comment    comments (unchanged in the counterpart)
 *   #ae81ff, #f92672 unchanged      literals, operators and tags
 *
 * Redo that table rather than hand-editing entries if the bundled Monokai
 * changes.
 */
export const monokaiLight: ThemeRegistration = {
  name: 'monokailight',
  displayName: 'Monokai Light',
  type: 'light',
  colors: {
    'editor.background': M.background,
    'editor.foreground': M.name,
  },
  tokenColors: [
    {
      settings: {
        foreground: M.name,
      },
    },
    {
      scope: [
        'meta.embedded',
        'source.groovy.embedded',
        'string meta.image.inline.markdown',
        'variable.legacy.builtin.python',
      ],
      settings: {
        foreground: M.name,
      },
    },
    {
      scope: 'comment',
      settings: {
        foreground: M.comment,
      },
    },
    {
      scope: 'string',
      settings: {
        foreground: M.string,
      },
    },
    {
      scope: [
        'punctuation.definition.template-expression',
        'punctuation.section.embedded',
      ],
      settings: {
        foreground: M.operator,
      },
    },
    {
      scope: [
        'meta.template.expression',
      ],
      settings: {
        foreground: M.name,
      },
    },
    {
      scope: 'constant.numeric',
      settings: {
        foreground: M.literal,
      },
    },
    {
      scope: 'constant.language',
      settings: {
        foreground: M.literal,
      },
    },
    {
      scope: 'constant.character, constant.other',
      settings: {
        foreground: M.literal,
      },
    },
    {
      scope: 'variable',
      settings: {
        foreground: M.name,
      },
    },
    {
      scope: 'keyword',
      settings: {
        foreground: M.operator,
      },
    },
    {
      scope: 'storage',
      settings: {
        foreground: M.operator,
      },
    },
    {
      scope: 'storage.type',
      settings: {
        foreground: M.keyword,
        fontStyle: 'italic',
      },
    },
    {
      scope: 'entity.name.type, entity.name.class, entity.name.namespace, entity.name.scope-resolution',
      settings: {
        foreground: M.green,
        fontStyle: 'underline',
      },
    },
    {
      scope: [
        'entity.other.inherited-class',
        'punctuation.separator.namespace.ruby',
      ],
      settings: {
        foreground: M.green,
        fontStyle: 'italic underline',
      },
    },
    {
      scope: 'entity.name.function',
      settings: {
        foreground: M.green,
      },
    },
    {
      scope: 'variable.parameter',
      settings: {
        foreground: M.string,
        fontStyle: 'italic',
      },
    },
    {
      scope: 'entity.name.tag',
      settings: {
        foreground: M.tag,
      },
    },
    {
      scope: 'entity.other.attribute-name',
      settings: {
        foreground: M.green,
      },
    },
    {
      scope: 'support.function',
      settings: {
        foreground: M.keyword,
      },
    },
    {
      scope: 'support.constant',
      settings: {
        foreground: M.keyword,
      },
    },
    {
      scope: 'support.type, support.class',
      settings: {
        foreground: M.keyword,
        fontStyle: 'italic',
      },
    },
    {
      scope: 'support.other.variable',
      settings: {},
    },
    {
      scope: 'invalid',
      settings: {
        foreground: M.error,
      },
    },
    {
      scope: 'invalid.deprecated',
      settings: {
        foreground: M.error,
      },
    },
    {
      scope: 'meta.structure.dictionary.json string.quoted.double.json',
      settings: {
        foreground: M.name,
      },
    },
    {
      scope: 'meta.diff, meta.diff.header',
      settings: {
        foreground: M.comment,
      },
    },
    {
      scope: 'markup.deleted',
      settings: {
        foreground: M.tag,
      },
    },
    {
      scope: 'markup.inserted',
      settings: {
        foreground: M.green,
      },
    },
    {
      scope: 'markup.changed',
      settings: {
        foreground: M.string,
      },
    },
    {
      scope: 'constant.numeric.line-number.find-in-files - match',
      settings: {
        foreground: '#ae81ffa0',
      },
    },
    {
      scope: 'entity.name.filename.find-in-files',
      settings: {
        foreground: M.string,
      },
    },
    {
      scope: 'markup.quote',
      settings: {
        foreground: M.tag,
      },
    },
    {
      scope: 'markup.list',
      settings: {
        foreground: M.string,
      },
    },
    {
      scope: 'markup.bold, markup.italic',
      settings: {
        foreground: M.keyword,
      },
    },
    {
      scope: 'markup.inline.raw',
      settings: {
        foreground: M.string,
      },
    },
    {
      scope: 'markup.heading',
      settings: {
        foreground: M.green,
      },
    },
    {
      scope: 'markup.heading.setext',
      settings: {
        foreground: M.green,
        fontStyle: 'bold',
      },
    },
    {
      scope: 'markup.heading.markdown',
      settings: {
        fontStyle: 'bold',
      },
    },
    {
      scope: 'markup.quote.markdown',
      settings: {
        foreground: M.comment,
        fontStyle: 'italic',
      },
    },
    {
      scope: 'markup.bold.markdown',
      settings: {
        fontStyle: 'bold',
      },
    },
    {
      scope: 'string.other.link.title.markdown,string.other.link.description.markdown',
      settings: {
        foreground: M.literal,
      },
    },
    {
      scope: 'markup.underline.link.markdown,markup.underline.link.image.markdown',
      settings: {
        foreground: M.string,
      },
    },
    {
      scope: 'markup.italic.markdown',
      settings: {
        fontStyle: 'italic',
      },
    },
    {
      scope: 'markup.strikethrough',
      settings: {
        fontStyle: 'strikethrough',
      },
    },
    {
      scope: 'markup.list.unnumbered.markdown, markup.list.numbered.markdown',
      settings: {
        foreground: M.name,
      },
    },
    {
      scope: [
        'punctuation.definition.list.begin.markdown',
      ],
      settings: {
        foreground: M.green,
      },
    },
    {
      scope: 'token.info-token',
      settings: {
        foreground: '#2f6fd0',
      },
    },
    {
      scope: 'token.warn-token',
      settings: {
        foreground: '#a06a00',
      },
    },
    {
      scope: 'token.error-token',
      settings: {
        foreground: M.error,
      },
    },
    {
      scope: 'token.debug-token',
      settings: {
        foreground: '#7b3fbf',
      },
    },
    {
      scope: 'variable.language',
      settings: {
        foreground: M.string,
      },
    },
  ],
}
