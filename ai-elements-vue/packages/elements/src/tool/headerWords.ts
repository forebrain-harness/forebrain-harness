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
  // The process tool (pkg/tool/header_parts.go): a background command's start,
  // readiness wait, log read and kill, each naming the command or the id.
  'Started background process': '已在后台启动',
  'Starting background process': '正在后台启动',
  'Failed to start background process': '后台启动失败',
  'Read log of': '已读取 … 的日志',
  'Reading log of': '正在读取 … 的日志',
  'Failed to read log of': '读取 … 的日志失败',
  'Waited for': '已等待',
  'Waiting for': '正在等待',
  'Failed to wait for': '等待失败',
  'Killed': '已终止',
  'Killing': '正在终止',
  'Failed to kill': '终止失败',
  'Listed processes': '已列出进程',
  'Listing processes': '正在列出进程',
  'Failed to list processes': '列出进程失败',
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
  // A tool this build has no words for — in practice a row another agent
  // recorded, brought in by /migrate. The frame is the reader's language; the
  // tool's own name stays as it was, because it is not one of ours.
  'Called': '已调用',
  'Calling': '正在调用',
  'Failed to call': '调用失败',
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
  // The built-in notes tool's three operations (pkg/tool/header_parts.go): the
  // verb carries the operation, so there is no bare noun to word.
  'Saved notes': '已保存笔记',
  'Saving notes': '正在保存笔记',
  'Failed to save notes': '保存笔记失败',
  'Read notes': '已读取笔记',
  'Reading notes': '正在读取笔记',
  'Failed to read notes': '读取笔记失败',
  'Cleared notes': '已清空笔记',
  'Clearing notes': '正在清空笔记',
  'Failed to clear notes': '清空笔记失败',
  'Presented files': '已交付文件',
  'Presenting files': '正在交付文件',
  'Failed to present files': '交付文件失败',
  'Asked user': '已询问用户',
  'Asking user': '正在询问用户',
  'Failed to ask user': '询问用户失败',
  'User declined to answer questions': '用户拒绝回答提问',
  'Skill': '技能',
  'Loading skill': '正在加载技能',
  'Sent a message to': '已向 … 发送消息',
  'Sending a message to': '正在向 … 发送消息',
  'Could not send a message to': '向 … 发送消息失败',
  'Sent a message': '已发送消息',
  'Sending a message': '正在发送消息',
  'Could not send a message': '未能发送消息',
  'Listed agents': '已列出代理',
  'Listing agents': '正在列出代理',
  'Failed to list agents': '列出代理失败',
  'Listed usable models': '已列出可用模型',
  'Listing usable models': '正在列出可用模型',
  'Failed to list usable models': '列出可用模型失败',
  'Checked context budget': '已检查上下文预算',
  'Checking context budget': '正在检查上下文预算',
  'Failed to check context budget': '检查上下文预算失败',
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
 * The slot a Chinese action leaves for its target, when the target reads
 * before the verb ("已向 … 发送消息") — the order Chinese puts a recipient in.
 * A full-width ellipsis, so the vocabulary test's "no ASCII letters" rule still
 * holds and no engine action can carry it by accident.
 */
export const HEADER_TARGET_SLOT = '…'

/**
 * The worded action split around its target: what reads before the target and
 * what reads after it. A word with no slot (every other action) and every
 * English head return null, so the caller keeps drawing verb then target.
 */
export function headerTargetedWord(
  action: string,
  lang: 'zh' | 'en',
): { before: string; after: string } | null {
  if (lang === 'en') return null
  const word = HEADER_ACTION_ZH[action] ?? ''
  const at = word.indexOf(HEADER_TARGET_SLOT)
  if (at < 0) return null
  return { before: word.slice(0, at).trim(), after: word.slice(at + HEADER_TARGET_SLOT.length).trim() }
}

/**
 * The suffix word in the reader's language: a read's line span, a search's
 * quoted match, the scope a memory search ran in, a readiness wait's goal.
 * Anything else — an MCP tool's own `参数: 值 · …` summary, a field name of an
 * outside tool — is external wording and stays as it came.
 */
export function headerSuffixWords(suffix: string, lang: 'zh' | 'en'): string {
  if (lang === 'en' || !suffix) return suffix
  // A process start that carries a readiness wait blocks the call, so its head
  // reads "Waiting for <cmd> to be ready" (pkg/tool/header_parts.go): 直到就绪
  // completes 正在等待 / 已等待 in both tenses.
  if (suffix === 'to be ready') return '直到就绪'
  let match = suffix.match(/^lines (.+)$/)
  if (match) return `第 ${match[1]} 行`
  match = suffix.match(/^matching (“.*”)$/)
  if (match) return `匹配 ${match[1]}`
  // The readiness wait a process card used to carry ("wait log"): still on rows
  // stored before that head change, so the rule stays reachable.
  match = suffix.match(/^wait (.+)$/)
  if (match) return `等待 ${match[1]}`
  match = suffix.match(/^in (.+)$/)
  if (match) return `在 ${match[1]} 中`
  return suffix
}

/**
 * The head as one line of text, in the reader's language — the same words
 * ToolHeader lays out, in the order it draws them (the woven Chinese order
 * included), with the duration last. A surface that names a call inside a
 * sentence of its own (the subagent card's latest-tool row) reads this instead
 * of assembling its own label, so the row and the card head never disagree
 * (owner 2026-11-27, the terminal's fanoutToolLabel rule).
 */
export function headerLine(
  parts: { action: string; target?: string; suffix?: string; duration?: string },
  lang: 'zh' | 'en',
): string {
  const action = headerActionWord(parts.action, lang)
  const target = String(parts.target ?? '').trim()
  const suffix = parts.suffix ? headerSuffixWords(parts.suffix, lang) : ''
  const woven = target ? headerTargetedWord(parts.action, lang) : null
  const head = woven
    ? `${woven.before} ${target} ${woven.after}`
    : [action, target].filter(Boolean).join(' ')
  return [head, suffix, String(parts.duration ?? '').trim()].filter(Boolean).join(' ')
}
