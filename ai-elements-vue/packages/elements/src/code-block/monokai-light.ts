import type { ThemeRegistration } from 'shiki'

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
 * The substitutions, from Shiki's Monokai to chroma's monokailight:
 *
 *   #f8f8f2, #cfcfc2 → #111111   names, punctuation, plain text
 *   #272822          → #fafafa   canvas
 *   #66d9ef          → #00a8c8   storage and keyword cyan
 *   #a6e22e          → #75af00   functions, classes, attributes
 *   #e6db74, #fd971f → #d88200   strings, parameters
 *   #f44747          → #960050   invalid
 *   #6796e6          → #2f6fd0   editor annotation tokens
 *   #cd9731          → #a06a00
 *   #b267e6          → #7b3fbf
 *   #88846f          → #75715e   comments (unchanged in the counterpart)
 *   #ae81ff, #f92672 unchanged   literals, operators and tags
 *
 * Redo that table rather than hand-editing entries if the bundled Monokai
 * changes.
 */
export const monokaiLight: ThemeRegistration = {
  name: 'monokailight',
  displayName: 'Monokai Light',
  type: 'light',
  colors: {
    'editor.background': '#fafafa',
    'editor.foreground': '#111111',
  },
  tokenColors: [
    {
      settings: {
        foreground: '#111111',
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
        foreground: '#111111',
      },
    },
    {
      scope: 'comment',
      settings: {
        foreground: '#75715e',
      },
    },
    {
      scope: 'string',
      settings: {
        foreground: '#d88200',
      },
    },
    {
      scope: [
        'punctuation.definition.template-expression',
        'punctuation.section.embedded',
      ],
      settings: {
        foreground: '#f92672',
      },
    },
    {
      scope: [
        'meta.template.expression',
      ],
      settings: {
        foreground: '#111111',
      },
    },
    {
      scope: 'constant.numeric',
      settings: {
        foreground: '#ae81ff',
      },
    },
    {
      scope: 'constant.language',
      settings: {
        foreground: '#ae81ff',
      },
    },
    {
      scope: 'constant.character, constant.other',
      settings: {
        foreground: '#ae81ff',
      },
    },
    {
      scope: 'variable',
      settings: {
        foreground: '#111111',
      },
    },
    {
      scope: 'keyword',
      settings: {
        foreground: '#f92672',
      },
    },
    {
      scope: 'storage',
      settings: {
        foreground: '#f92672',
      },
    },
    {
      scope: 'storage.type',
      settings: {
        foreground: '#00a8c8',
        fontStyle: 'italic',
      },
    },
    {
      scope: 'entity.name.type, entity.name.class, entity.name.namespace, entity.name.scope-resolution',
      settings: {
        foreground: '#75af00',
        fontStyle: 'underline',
      },
    },
    {
      scope: [
        'entity.other.inherited-class',
        'punctuation.separator.namespace.ruby',
      ],
      settings: {
        foreground: '#75af00',
        fontStyle: 'italic underline',
      },
    },
    {
      scope: 'entity.name.function',
      settings: {
        foreground: '#75af00',
      },
    },
    {
      scope: 'variable.parameter',
      settings: {
        foreground: '#d88200',
        fontStyle: 'italic',
      },
    },
    {
      scope: 'entity.name.tag',
      settings: {
        foreground: '#f92672',
      },
    },
    {
      scope: 'entity.other.attribute-name',
      settings: {
        foreground: '#75af00',
      },
    },
    {
      scope: 'support.function',
      settings: {
        foreground: '#00a8c8',
      },
    },
    {
      scope: 'support.constant',
      settings: {
        foreground: '#00a8c8',
      },
    },
    {
      scope: 'support.type, support.class',
      settings: {
        foreground: '#00a8c8',
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
        foreground: '#960050',
      },
    },
    {
      scope: 'invalid.deprecated',
      settings: {
        foreground: '#960050',
      },
    },
    {
      scope: 'meta.structure.dictionary.json string.quoted.double.json',
      settings: {
        foreground: '#111111',
      },
    },
    {
      scope: 'meta.diff, meta.diff.header',
      settings: {
        foreground: '#75715e',
      },
    },
    {
      scope: 'markup.deleted',
      settings: {
        foreground: '#f92672',
      },
    },
    {
      scope: 'markup.inserted',
      settings: {
        foreground: '#75af00',
      },
    },
    {
      scope: 'markup.changed',
      settings: {
        foreground: '#d88200',
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
        foreground: '#d88200',
      },
    },
    {
      scope: 'markup.quote',
      settings: {
        foreground: '#f92672',
      },
    },
    {
      scope: 'markup.list',
      settings: {
        foreground: '#d88200',
      },
    },
    {
      scope: 'markup.bold, markup.italic',
      settings: {
        foreground: '#00a8c8',
      },
    },
    {
      scope: 'markup.inline.raw',
      settings: {
        foreground: '#d88200',
      },
    },
    {
      scope: 'markup.heading',
      settings: {
        foreground: '#75af00',
      },
    },
    {
      scope: 'markup.heading.setext',
      settings: {
        foreground: '#75af00',
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
        foreground: '#75715e',
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
        foreground: '#ae81ff',
      },
    },
    {
      scope: 'markup.underline.link.markdown,markup.underline.link.image.markdown',
      settings: {
        foreground: '#d88200',
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
        foreground: '#111111',
      },
    },
    {
      scope: [
        'punctuation.definition.list.begin.markdown',
      ],
      settings: {
        foreground: '#75af00',
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
        foreground: '#960050',
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
        foreground: '#d88200',
      },
    },
  ],
}
