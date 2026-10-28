/**
 * Card heads speak the reader's language (owner 2026-10-25). The engine's
 * action is a fixed vocabulary (pkg/tool HeaderActions, golden copy in
 * pkg/tool/testdata/header_actions.json), so the Chinese words come from this
 * table; the target — a path, a command, a query, a URL — is never translated.
 *
 * A new engine action word changes three places together: the engine's
 * HeaderActions, its golden header_actions.json, and this table (the golden
 * test in the app, frontend/src/locales/toolHeader.test.ts, reads the copy and
 * turns red until the word is here).
 */

/** Every action word the engine puts in a card head, in the reader's language. */
export const HEADER_ACTION_ZH: Record<string, string> = {
  'Ran': '已运行',
  'Running': '正在运行',
  'Failed to run': '运行失败',
  'Read': '已读取',
  'Reading': '正在读取',
  'Failed to read': '读取失败',
  'Edited': '已编辑',
  'Editing': '正在编辑',
  'Failed to edit': '编辑失败',
  'Created': '已创建',
  'Wrote': '已写入',
  'Writing': '正在写入',
  'Failed to write': '写入失败',
  'Fetched': '已抓取',
  'Fetching': '正在抓取',
  'Failed to fetch': '抓取失败',
  'Searched': '已搜索',
  'Searching': '正在搜索',
  'Failed to search': '搜索失败',
  'Looked up': '已查询',
  'Looking up': '正在查询',
  'Failed to look up': '查询失败',
  'Requested permissions': '已申请权限',
  'Requesting permissions': '正在申请权限',
  'Failed to request permissions': '申请权限失败',
  'Permission request denied': '权限申请被拒绝',
  'Permission request canceled': '权限申请已取消',
  'Retrieved saved output': '已取回保存的输出',
  'Retrieving saved output': '正在取回保存的输出',
  'Failed to retrieve saved output': '取回保存的输出失败',
  'Output retrieval denied': '取回输出被拒绝',
  'Output retrieval canceled': '取回输出已取消',
  'Searched saved output': '已搜索保存的输出',
  'Searching saved output': '正在搜索保存的输出',
  'Todo': '待办',
  'Updating todo': '正在更新待办',
  'Failed to update todo': '更新待办失败',
  'Entered plan mode': '已进入计划模式',
  'Entering plan mode': '正在进入计划模式',
  'Failed to enter plan mode': '进入计划模式失败',
  'Exited plan mode': '已退出计划模式',
  'Failed to exit plan mode': '退出计划模式失败',
  'Kept planning': '继续规划',
  'Kept the plan': '已保留计划',
  'Saved memory note': '已保存记忆笔记',
  'Saving memory note': '正在保存记忆笔记',
  'Failed to save memory note': '保存记忆笔记失败',
  'Memory note save denied': '保存记忆笔记被拒绝',
  'Memory note save canceled': '保存记忆笔记已取消',
  'Listed memories': '已列出记忆',
  'Listing memories': '正在列出记忆',
  'Failed to list memories': '列出记忆失败',
  'Memory listing denied': '列出记忆被拒绝',
  'Memory listing canceled': '列出记忆已取消',
  'Read memory': '已读取记忆',
  'Reading memory': '正在读取记忆',
  'Failed to read memory': '读取记忆失败',
  'Memory read denied': '读取记忆被拒绝',
  'Memory read canceled': '读取记忆已取消',
  'Searched memories': '已搜索记忆',
  'Searching memories': '正在搜索记忆',
  'Failed to search memories': '搜索记忆失败',
  'Memory search denied': '搜索记忆被拒绝',
  'Memory search canceled': '搜索记忆已取消',
  'Notes': '笔记',
  'Saving notes': '正在保存笔记',
  'Failed to save notes': '保存笔记失败',
  'Presented files': '已交付文件',
  'Presenting files': '正在交付文件',
  'Failed to present files': '交付文件失败',
  'Asked user': '已询问用户',
  'Asking user': '正在询问用户',
  'Failed to ask user': '询问用户失败',
  'Skill': '技能',
  'Loading skill': '正在加载技能',
  'Denied': '已拒绝',
  'Canceled': '已取消',
  'MCP': 'MCP',
}

/**
 * The action word in the reader's language. An action the table has not
 * worded yet is shown as the engine wrote it — never dropped, never guessed.
 */
export function headerActionWord(action: string, lang: 'zh' | 'en'): string {
  if (lang === 'en') return action
  return HEADER_ACTION_ZH[action] ?? action
}

/**
 * The suffix word in the reader's language: a read's line span, a search's
 * quoted match, the scope a memory search ran in. Anything else — an MCP
 * tool's own `参数: 值 · …` summary, a field name of an outside tool — is
 * external wording and stays as it came.
 */
export function headerSuffixWords(suffix: string, lang: 'zh' | 'en'): string {
  if (lang === 'en' || !suffix) return suffix
  let match = suffix.match(/^lines (.+)$/)
  if (match) return `第 ${match[1]} 行`
  match = suffix.match(/^matching (“.*”)$/)
  if (match) return `匹配 ${match[1]}`
  match = suffix.match(/^in (.+)$/)
  if (match) return `在 ${match[1]} 中`
  return suffix
}
