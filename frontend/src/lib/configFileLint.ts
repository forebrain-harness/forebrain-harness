/**
 * The lint the configuration-file editor draws (plan 191 §0.19, §4.3): a key
 * forebrain.yaml does not carry — a retired setting, one the pages own, a
 * typo — gets a wavy line with「不存在」, and only a YAML syntax error refuses
 * a save. Both rules are the prototype's own (`lintYAML` and `syntaxYAML`
 * there), word for word; the whitelist itself is not copied here but comes
 * from the gateway (`GET /api/config/file-schema`), which reads the same
 * `FileSupportedKeys` the loader does.
 */

/** One wavy line: a zero-based line and the words its hover shows. */
export interface ConfigLintDiagnostic {
  line: number
  message: string
}

/** A scalar leaf in the whitelist tree. */
const LEAF = 1

/** The dotted whitelist as the tree the walk needs: `gateway.auth.token`
 * becomes `{gateway: {auth: {token: 1}}}`. */
function keysTree(keys: string[]): Record<string, unknown> {
  const root: Record<string, unknown> = {}
  for (const key of keys) {
    const parts = key.split('.').filter(Boolean)
    let node: Record<string, unknown> = root
    for (let depth = 0; depth < parts.length; depth += 1) {
      const part = parts[depth]
      if (depth === parts.length - 1) {
        node[part] = LEAF
      } else {
        if (typeof node[part] !== 'object' || node[part] === null) node[part] = {}
        node = node[part] as Record<string, unknown>
      }
    }
  }
  return root
}

/**
 * The lines whose outermost key the file does not support. Only the outermost
 * unsupported key is marked: what nests under it goes with it, not twice.
 */
export function unsupportedYAMLKeys(source: string, supportedKeys: string[]): number[] {
  const bad: number[] = []
  const root = keysTree(supportedKeys)
  const stack: { indent: number; node: unknown }[] = [{ indent: -1, node: root }]
  source.split('\n').forEach((line, index) => {
    if (!line.trim() || /^\s*#/.test(line) || /^\s*-/.test(line)) return
    const match = line.match(/^(\s*)([\w.-]+)\s*:/)
    if (!match) return
    const indent = match[1].length
    while (stack.length > 1 && stack[stack.length - 1].indent >= indent) stack.pop()
    const parent = stack[stack.length - 1].node
    let node: unknown
    if (parent === LEAF || parent === undefined || parent === null) {
      node = parent === LEAF ? LEAF : null
    } else if (typeof parent === 'object' && match[2] in parent) {
      node = (parent as Record<string, unknown>)[match[2]]
    } else {
      bad.push(index)
      node = null
    }
    stack.push({ indent, node })
  })
  return bad
}

/**
 * The first line a YAML parser would refuse — the one thing that stops a save.
 * The three reasons are the prototype's own words, which are also the
 * gateway's (`configFileSyntaxError`): the editor and the refusal say the same
 * thing for the same line.
 */
export function yamlSyntaxError(source: string): ConfigLintDiagnostic | null {
  const lines = source.split('\n')
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index]
    if (!line.trim() || /^\s*#/.test(line)) continue
    if (/^ *\t/.test(line)) return { line: index, message: '缩进不能用 Tab' }
    if (/^\s*-(\s|$)/.test(line)) continue
    if (!/^\s*[^:#\s][^:#]*:(\s|$)/.test(line)) return { line: index, message: '缺少冒号' }
    const value = line.slice(line.indexOf(':') + 1).trim()
    if (
      (value.startsWith('"') && !/^".*"$/.test(value)) ||
      (value.startsWith("'") && !/^'.*'$/.test(value))
    ) {
      return { line: index, message: '引号没有闭合' }
    }
  }
  return null
}

/** Everything the editor underlines: unsupported keys, and the syntax error. */
export function lintConfigYaml(source: string, supportedKeys: string[]): ConfigLintDiagnostic[] {
  const diagnostics: ConfigLintDiagnostic[] = unsupportedYAMLKeys(source, supportedKeys).map((line) => ({
    line,
    message: '不存在',
  }))
  const syntax = yamlSyntaxError(source)
  if (syntax) diagnostics.push(syntax)
  return diagnostics
}
