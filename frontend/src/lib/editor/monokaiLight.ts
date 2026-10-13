import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { tags as t } from '@lezer/highlight'

import { monokaiLightPalette as M } from '@repo/elements/code-block'

/**
 * Monokai Light for every editor in the web client — a Crepe code block, the
 * Markdown source pane, a whole code file — whatever the brand colour or rail
 * theme. The colours are the shared palette (chroma's `monokailight`, the same
 * ones the assistant's replies are painted with); only the tags differ here,
 * mapped from chroma token families onto lezer highlight tags the way the
 * terminal renderer maps them. Keep that mapping beside the palette so a code
 * token never reads two ways.
 */
export const monokaiLightHighlightStyle = HighlightStyle.define([
  { tag: [t.keyword, t.controlKeyword, t.definitionKeyword, t.operatorKeyword, t.modifier, t.typeName, t.self], color: M.keyword }, // Keyword, KeywordType
  { tag: [t.moduleKeyword], color: M.namespace }, // KeywordNamespace
  { tag: [t.bool, t.null, t.constant(t.name), t.standard(t.name)], color: M.keyword }, // KeywordConstant, NameConstant
  { tag: [t.name, t.variableName, t.punctuation, t.bracket, t.separator], color: M.punctuation }, // Name, Punctuation
  { tag: [t.function(t.variableName), t.function(t.propertyName), t.function(t.definition(t.variableName)), t.className, t.attributeName, t.macroName, t.annotation, t.labelName], color: M.green }, // NameFunction/Class/Attribute/Decorator/Other
  { tag: [t.tagName, t.propertyName, t.definition(t.propertyName)], color: M.tag }, // NameTag (JSON/YAML keys)
  { tag: [t.number, t.integer, t.float, t.atom, t.unit], color: M.literal }, // LiteralNumber, Literal
  { tag: [t.string, t.special(t.string), t.regexp, t.character, t.url], color: M.string }, // LiteralString
  { tag: [t.escape], color: M.escape }, // LiteralStringEscape
  { tag: [t.operator, t.compareOperator, t.arithmeticOperator, t.logicOperator, t.bitwiseOperator, t.updateOperator, t.definitionOperator, t.typeOperator, t.derefOperator], color: M.operator }, // Operator
  { tag: [t.comment, t.lineComment, t.blockComment, t.docComment, t.meta, t.processingInstruction], color: M.comment }, // Comment
  { tag: [t.heading, t.strong], fontWeight: 'bold' }, // GenericStrong
  { tag: [t.emphasis], fontStyle: 'italic' }, // GenericEmph
  { tag: [t.strikethrough], textDecoration: 'line-through' },
  { tag: [t.link], textDecoration: 'underline' },
  { tag: [t.invalid], color: M.error }, // Error
])

/** The extension every editor installs; `syntaxHighlighting` of the style above. */
export const monokaiLightHighlight = syntaxHighlighting(monokaiLightHighlightStyle)
