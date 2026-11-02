import { describe, expect, it } from 'vitest'

import { lintConfigYaml, unsupportedYAMLKeys, yamlSyntaxError } from '@/lib/configFileLint'

/** The gateway's own whitelist (config.FileSupportedKeys): gateway.http_addr,
 * gateway.auth.token, credentials.chatgpt, database.url — nothing else. */
const KEYS = ['gateway.http_addr', 'gateway.auth.token', 'credentials.chatgpt', 'database.url']

describe('the configuration file lint', () => {
  it('flags the three syntax errors a YAML parser refuses, each on its own line', () => {
    // A missing colon (the prototype's own example: the token line).
    const colon = yamlSyntaxError('gateway:\n  auth:\n    token fbg_1234\n')
    expect(colon).toEqual({ line: 2, message: '缺少冒号' })

    // A Tab where an indent belongs.
    const tab = yamlSyntaxError('gateway:\n\thttp_addr: 127.0.0.1:8787\n')
    expect(tab).toEqual({ line: 1, message: '缩进不能用 Tab' })

    // A quote that never closes.
    const quote = yamlSyntaxError('gateway:\n  http_addr: "127.0.0.1:8787\n')
    expect(quote).toEqual({ line: 1, message: '引号没有闭合' })

    // Comments, blank lines and list items are not syntax errors.
    expect(yamlSyntaxError('# a note\ngateway:\n\n  http_addr: 127.0.0.1:8787\n')).toBeNull()
    expect(yamlSyntaxError('items:\n  - one\n  - two\n')).toBeNull()
  })

  it('underlines a key the file does not support, and only the outermost one', () => {
    const yaml = [
      'gateway:', // 0 supported
      '  http_addr: 127.0.0.1:8787', // 1 supported
      'approval_policy: never', // 2 retired — flagged
      'memories:', // 3 owned by the pages — flagged
      '  search_top_k: 8', // 4 nests under it — not flagged again
      'gateway_url: https://x', // 5 a typo — flagged
    ].join('\n')
    expect(unsupportedYAMLKeys(yaml, KEYS)).toEqual([2, 3, 5])
  })

  it('accepts every key of the whitelist itself, nested as the file writes it', () => {
    const yaml = ['gateway:', '  http_addr: 127.0.0.1:8787', '  auth:', '    token: fbg_1', 'credentials:', '  chatgpt: /h/auth.json'].join('\n')
    expect(unsupportedYAMLKeys(yaml, KEYS)).toEqual([])
  })

  it('carries both kinds of line to the editor, 不存在 first', () => {
    const yaml = 'memories: {}\ngateway:\n  http_addr "x"\n'
    expect(lintConfigYaml(yaml, KEYS)).toEqual([
      { line: 0, message: '不存在' },
      { line: 2, message: '缺少冒号' },
    ])
  })
})
