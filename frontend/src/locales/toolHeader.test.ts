import { describe, expect, it } from 'vitest'

// The engine's action-word vocabulary, straight from its golden copy. A static
// import rather than node:fs: this package has no @types/node, and the golden
// file is a compile-time constant anyway — either way the test reads exactly
// what the engine ships and turns red when a word is not worded here.
import goldenActions from '../../../pkg/tool/testdata/header_actions.json'

import { HEADER_ACTION_ZH, headerActionWord, headerSuffixWords, headerLine } from './toolHeader'

describe('the card head vocabulary', () => {
  it('words every action the engine can put in a head, in Chinese and nothing else', () => {
    for (const action of goldenActions) {
      const zh = HEADER_ACTION_ZH[action]
      expect(zh, `a Chinese word for ${action}`).toBeTruthy()
      // The one exception is MCP, a proper name. Everything else is Chinese:
      // an ASCII letter means the engine's English slipped through.
      if (action !== 'MCP') {
        expect(zh, `${action} worded without ASCII letters`).not.toMatch(/[A-Za-z]/)
      }
    }
  })

  it('leaves an unworded action as the engine wrote it, both languages', () => {
    expect(headerActionWord('Read', 'zh')).toBe('已读取')
    expect(headerActionWord('Read', 'en')).toBe('Read')
    expect(headerActionWord('Something new', 'zh')).toBe('Something new')
  })

  // The notes tool's three operations (pkg/tool/header_parts.go): every state
  // says which one it is, and the bare noun is gone from the table — a head
  // that reads 笔记 leaves the reader unable to tell a save from a read.
  it('words each notes operation by what it did', () => {
    expect(HEADER_ACTION_ZH['Notes']).toBeUndefined()
    expect(headerActionWord('Saved notes', 'zh')).toBe('已保存笔记')
    expect(headerActionWord('Saving notes', 'zh')).toBe('正在保存笔记')
    expect(headerActionWord('Failed to save notes', 'zh')).toBe('保存笔记失败')
    expect(headerActionWord('Read notes', 'zh')).toBe('已读取笔记')
    expect(headerActionWord('Reading notes', 'zh')).toBe('正在读取笔记')
    expect(headerActionWord('Failed to read notes', 'zh')).toBe('读取笔记失败')
    expect(headerActionWord('Cleared notes', 'zh')).toBe('已清空笔记')
    expect(headerActionWord('Clearing notes', 'zh')).toBe('正在清空笔记')
    expect(headerActionWord('Failed to clear notes', 'zh')).toBe('清空笔记失败')
  })

  it('words each process action and its wait suffix', () => {
    expect(headerActionWord('Started background process', 'zh')).toBe('已在后台启动')
    expect(headerActionWord('Starting background process', 'zh')).toBe('正在后台启动')
    expect(headerActionWord('Failed to start background process', 'zh')).toBe('后台启动失败')
    expect(headerActionWord('Read log of', 'zh')).toBe('已读取 … 的日志')
    expect(headerActionWord('Reading log of', 'zh')).toBe('正在读取 … 的日志')
    expect(headerActionWord('Failed to read log of', 'zh')).toBe('读取 … 的日志失败')
    expect(headerActionWord('Waited for', 'zh')).toBe('已等待')
    expect(headerActionWord('Waiting for', 'zh')).toBe('正在等待')
    expect(headerActionWord('Failed to wait for', 'zh')).toBe('等待失败')
    expect(headerActionWord('Killed', 'zh')).toBe('已终止')
    expect(headerActionWord('Killing', 'zh')).toBe('正在终止')
    expect(headerActionWord('Failed to kill', 'zh')).toBe('终止失败')
    expect(headerActionWord('Listed processes', 'zh')).toBe('已列出进程')
    expect(headerActionWord('Listing processes', 'zh')).toBe('正在列出进程')
    expect(headerActionWord('Failed to list processes', 'zh')).toBe('列出进程失败')
    expect(headerSuffixWords('wait log', 'zh')).toBe('等待 log')
    expect(headerSuffixWords('wait tcp', 'zh')).toBe('等待 tcp')
    // A start that carries a readiness wait says what it waits for (plan 380).
    expect(headerSuffixWords('to be ready', 'zh')).toBe('直到就绪')
  })

  it('words the three suffix shapes and leaves everything else alone', () => {
    expect(headerSuffixWords('lines 15-20', 'zh')).toBe('第 15-20 行')
    // The engine words a search's suffix as `matching “query”` (header_parts.go).
    expect(headerSuffixWords('matching “go test”', 'zh')).toBe('匹配 “go test”')
    expect(headerSuffixWords('in this project', 'zh')).toBe('在 this project 中')
    // An MCP tool's own field summary is external wording — never translated.
    expect(headerSuffixWords('repo: forebrain-harness limit: 10', 'zh')).toBe('repo: forebrain-harness limit: 10')
    expect(headerSuffixWords('lines 15-20', 'en')).toBe('lines 15-20')
    expect(headerSuffixWords('to be ready', 'en')).toBe('to be ready')
  })

  // headerLine is the head as one line of text — the same words ToolHeader
  // draws, in its order — for a surface that names a call inside a sentence of
  // its own (the subagent card's latest-tool row).
  it('joins the head as one line in the order ToolHeader draws it', () => {
    const parts = { action: 'Started background process', target: 'sleep 6; echo ok', duration: '6.0s' }
    expect(headerLine(parts, 'en')).toBe('Started background process sleep 6; echo ok 6.0s')
    expect(headerLine(parts, 'zh')).toBe('已在后台启动 sleep 6; echo ok 6.0s')
    // A suffix rides between the target and the duration, worded.
    expect(headerLine({ action: 'Read', target: 'types.go', suffix: 'lines 2-101' }, 'zh')).toBe('已读取 types.go 第 2-101 行')
    expect(headerLine({ action: 'Waiting for', target: 'sleep 6', suffix: 'to be ready' }, 'zh')).toBe('正在等待 sleep 6 直到就绪')
    // A woven Chinese action puts its target inside the sentence.
    expect(headerLine({ action: 'Sent a message to', target: 'agent-1' }, 'zh')).toBe('已向 agent-1 发送消息')
    expect(headerLine({ action: 'Sent a message to', target: 'agent-1' }, 'en')).toBe('Sent a message to agent-1')
    // A head with no object is its action alone; no part invents a space.
    expect(headerLine({ action: 'Listed processes' }, 'zh')).toBe('已列出进程')
    expect(headerLine({ action: 'Listed processes' }, 'en')).toBe('Listed processes')
  })
})
