# 083 — 审批卡按「族」渲染：十二种问题各自一张卡，文案语义化

## Status

- **DONE**（2026-10-20 立项，基线 `cd11539`；2026-10-20 执行完毕，评审 APPROVE，改动留工作区未提交）
- 复审：2026-10-20 冷读复核（基线 `cd11539`）。行号、载荷键、决定档数已按工作区实况校准；
  修正了 6 处会直接导致实现错误的偏差：`shell` 的第二条提问路径（工具自己的 hook）、
  `apply_patch` 没有 `files[]`、沙箱外的 path-scoped 变体、既有 `approvalDecisionLabel` 与它的旧文案键、
  原型的按钮/事实文案（新建文件是「96 行」不是「+96 行」）、`web_search` 载荷里没有条数。
- 复审期三条待裁决事项已由 owner 于 2026-10-20 拍板，正文已按裁决写定：
  ① 兜底卡事实行**留空**（与原型的一处差别，见 A 节 `fallback`）；
  ② 旧文案键**逐字替换后删掉**（见 E 节第 2 条）；
  ③ `085` 的 `/plan-md` 归属校验**一并补**（记在 085，不在本计划）。
- 优先级 **P1**，工作量 **L**，风险 **MED**
- 依据：owner 2026-10-20 已审批原型 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 的
  「对话 · 审批卡（全部工具）」页（跳转按钮 `#ij-gates`，源码 `approvalCardsThread()` 与 `gate01..12` 一族函数）。
- 同一份原型说明写在页面「本轮迭代」里，两段：`要问的每一类都单独画了一张`、`这十二张卡的文案只有一个规则`。
  执行者若对某条文案有疑问，先看这两段，不要自创。

本计划**只做 web 与两处引擎载荷字段**（`write_file` 的旧行数、`web_search` 的条数，见 B/B2）。
终端 TUI 的审批面板本期不动（owner 2026-10-20：
「不涉及终端 tui，tui 保持现状」）。

---

## 为什么

今天 web 只有三种审批卡：退出计划模式、抓取（web_fetch）、其它全部挤在一张通用卡里。

`frontend/src/components/chat/ApprovalCard.vue:170-216`（通用分支）画的是：

```
approvalKindLabel(a.kind)      ← 工具内部名，例如 write_file / mcp__github__create_issue
approvalHint(a)                ← action id 或 model 自己写的一句话
[权限记忆] WebSearch · 查询 · default_ask
[批准一次] [拒绝并中断本轮]
```

这句话对做决定的人没有帮助：`write_file` 不说明要写哪个文件、写多少；`mcp__github__create_issue`
不说要建什么；`批准一次 / 拒绝并中断本轮` 是引擎词，不是人话。而引擎其实已经给足了事实
（每族有各自的可用决定，载荷里有文件、命令、主机、权限条目），只是界面没有按族说。

原型把「会停下来问人」的十二族逐一画在新的一页上（文件写/改/补丁、命令、沙箱外运行、外网主机、
申请权限、进计划模式、MCP、受保护读、兜底），owner 已确认；抓取与退出计划模式两张在前几轮已画并上线。
本计划把这一整套（12 张新卡 + 2 张已有卡接进同一个分派器 + 兜底）落到真机。
注意：`web_search` 与 `memories_add_ad_hoc_note` 虽然早前定过设计，但**代码里还没实现**
（`grep -rn "memories_add_ad_hoc_note" frontend/src` 无命中，`web_search` 也没有专门卡片），
它们在这 12 张里，属于本计划要新建的部分。

---

## 现状事实（执行前请自行复核，行号基于 `cd11539`）

### 1. 哪些工具会问（引擎事实，不要在界面里改这个集合）

`pkg/safety/engine.go:93-168` 的评估顺序决定一切：

```
deny 规则 → ask 规则 → allow 规则 → IsSafeReadOnlyTool（放行）→ isSafeInternalTool（放行）
        → shell（默认放行）→ default_ask（问）
```

- 放行集合：`pkg/safety/engine.go:406-445`（`read_file` / `list_directory` / `list_files` /
  `skill` / `retrieve_output` / `memories_list|read|search` / `Read` / `LS` / `LSP`），
  以及 `:447-458` 的 `safeInternalTools`（`intermediate_tool`、`session_todo`、八个 `subagent_*`）。
- shell 默认放行。**但 shell 有两条独立的提问路径，画卡时两条都要认**（只认第一条会漏掉绝大多数
  shell 卡）：
  1. **安全引擎**：`shellNeedsApprovalDespiteDefault`（`pkg/safety/engine.go:537-542`）为真 → 沙箱不可用，
     或 `AssessShellCommand(command).Dangerous != nil`。当前唯一的危险规则是 `forced_rm`
     （`pkg/safety/shell.go:532-540` 的 `rm` 分支：带 `--force`/`-f`；`sudo`/`env`/`sh -c`
     递归进去）。全 `pkg/` 只有这一个 `Rule:` 字面量，别处没有第二条危险规则。
  2. **shell 工具自己的 hook**（`pkg/tool/shell_tool.go`）：`shellAssessmentRequiresApproval`
     （`:977-1009`）为真时由工具直接发审批，载荷里带 `command_safety` 与 `approval_reason`。
     `approval_reason` 的取值就是这一族卡的判据（`shellAssessmentApprovalPayload` `:1027-1044`）：
     `dangerous_shell_syntax`（有危险匹配，或用了复杂解析）、`unknown_shell_command`（未知命令、
     策略为 unless-trusted 等），以及 `sandbox_denied`（`:762-786`）、`network_denied`
     （`:517-524`）、`require_escalated` 之类由模型自己申请的升级。
     所以「shell 卡 = 危险命令」不成立：**没有危险匹配时也会问**，这时事实行没有规则可套（见 A 节）。

### 2. 每族的可用决定（决定按钮档数，界面不得增删）

`pkg/safety/permission_types.go:999-1067`（`BuildApprovalDecisionOptions`）：

| 条件 | 决定序列 | 档数 |
|---|---|---|
| 载荷带 `network_approval_context`（`:1004-1015`） | accept / accept_for_session / apply_network_policy_amendment / cancel | 4 |
| `actionKind == "request_permissions"`（`:1018-1024`） | grant_for_turn / grant_for_turn_with_strict_auto_review / grant_for_session / decline | 4 |
| `actionKind == "apply_patch"`（`:1025-1026`） | accept / accept_for_session / cancel | 3 |
| `mcp__*` 且非一次性（`:1027-1031`） | accept / accept_for_session / accept_and_remember / cancel | 4 |
| `Bash`（`:1032-1049`） | **条件 3 档**：只有载荷能推出可记住的命令规则（`ProposedCommandApprovalRules` 命中）时才是 accept / remember / cancel——单 token 前缀时 remember 是 `accept_with_execpolicy_amendment`，否则是 `accept_and_remember`；推不出规则时**落到默认 2 档**（accept / cancel）。载荷带 `additional_permissions` 时直接不进这一支 | 3 或 2 |
| `WebFetch`（`:1050-1054`） | 条件是 `PrefixRuleContent` 非空（带 host 的 `domain:` 规则）：accept / accept_and_remember / cancel | 3（已实现） |
| 文件族 `ApprovalFileTool`（`:1055-1056`）：Read/Write/Edit/MultiEdit | accept / accept_for_session / cancel | 3 |
| 记忆族 `ApprovalMemoryTool`（`:1057-1064`） | accept / decline | 2（`memories_add_ad_hoc_note` 已实现） |
| 其余（`:1066`） | accept / cancel | 2 |

### 3. web 手上已有什么

- `frontend/src/composables/usePendingActions.ts:28-43` `ActionRow`：
  `id / kind / status / payloadJson / answerJson / createdAt / updatedAt / permissionSuggestion / agentId / subagentType`。
  `payloadJson` 就是工具入参（引擎 `pkg/state/action_service.go:39` 把 hook 载荷 `json.Marshal` 进这一列；
  `pkg/gateway/api_extra.go:3339-3354` 的 `actionListRow` 直接把它吐给前端）。
- `permissionSuggestion`（`frontend/src/lib/approvalSuggestions.ts:44-58`）带
  `permissionToolName / permissionInput / prefixRuleContent / availableDecisions /
  networkApprovalContext / networkPort / bypassSandbox / oneShotOnly` 等。
- `usePendingActions.approvalDecisionOptions(a)`（`:341-343`）已把 `available_decisions` 解出来；
  `approvalDecisionLabel(option, action)`（`:386-411`）是现有的按钮文案函数。

**这两个函数已经覆盖全部十个 decision id**（`accept`、`accept_for_session`、`accept_and_remember`、
`accept_with_execpolicy_amendment`、`apply_network_policy_amendment`、`grant_for_turn`、
`grant_for_turn_with_strict_auto_review`、`grant_for_session`、`decline`、`cancel`），
文案来自 `locales/index.ts:1388-1403`（zh）与 `:3045-3060`（en）那一批 `chat.approve*` / `chat.grant*` /
`chat.cancelRun` 键。也就是说：**按钮的数量与提交链路已经就绪，本计划要改的只有文案与卡片结构**，
见 C 节与 E 节。抓取卡（`ApprovalCard.vue:142-156`）就是现成的样板：

```html
<button
  v-for="(option, index) in approvalDecisionOptions(a)"
  :class="option.decision === 'accept' ? 'forebrain-btn forebrain-btn-primary text-xs' : 'forebrain-btn forebrain-btn-ghost text-xs'"
  :data-testid="`pending-decision-${option.decision}`"
  @click="submitApprovalDecision(a, option)"
>{{ approvalDecisionLabel(option, a) }}</button>
```

### 4. 各族载荷里的键（已逐个读过，按这个渲染）

| 族 | `kind` | `payload_json` 关键键 | 出处（已按工作区实况核对） |
|---|---|---|---|
| 写新文件 / 覆盖 | `write_file` | `file_path`、`resolved_file_path`、`content`；受保护路径时**另加** `justification`、`force_tool_approval: true`、`approval_reason` | `pkg/tool/file_tools.go:850-851`（`markProtectedApproval` 见 `pkg/tool/permissions.go:140-146`） |
| 改文件 | `edit_file` | `file_path`、`resolved_file_path`、`old_string`、`new_string`、`replace_all` | `pkg/tool/file_tools.go:1034-1041` |
| 补丁 | `apply_patch` | **只有三个键**：`apply_patch: true`、`patch`、`resolved_paths`（绝对路径数组）。**没有 `files[]`**——文件名只能从 `resolved_paths` 取 basename | `pkg/tool/file_tools.go:1583-1588` |
| 命令 | `shell` | `command`、`timeout_ms`、`sandbox_permissions`、`command_safety`、`approval_reason`；可选 `justification`、`prefix_rule`、`additional_permissions`、`force_tool_approval` | `pkg/tool/shell_tool.go:1027-1070`（两个构造器） |
| 沙箱外重跑 | `shell` | `sandbox_permissions: "require_escalated"`、`approval_reason: "sandbox_denied"`、`sandbox_denial_reason`、`force_tool_approval: true`；**path-scoped 变体**换成 `sandbox_permissions: "with_additional_permissions"` + `scoped_sandbox_retry: true` + `additional_permissions` | `pkg/tool/shell_tool.go:762-786`（另有 `:198-201` 的 `profile_elevation` 变体） |
| 外网主机 | `shell` | `network_approval_context: {host, protocol}`、`network_port`、`sandbox_denial_reason`、`approval_reason: "network_denied"`；内联路径另有 `network_inline` | `pkg/tool/shell_tool.go:517-524`、`:686-690` |
| 申请权限 | `request_permissions` | `request_permissions: true`、`permissions.file_system.entries[]`、`permissions.network`、`reason`、`cwd` | `pkg/tool/request_permissions.go:262-277` |
| 进计划模式 | `enter_plan_mode` | `reason`、`session_id`、`action: "enter"` | `pkg/tool/session_tools.go:425-429` |
| MCP 工具 | `mcp__<srv>__<tool>` | 该工具自己的入参 | `pkg/mcp/semantics.go:216-218` |
| 搜索 | `web_search` | 今天只有 `query`、`rule_content`——**没有 `limit`**，所以「最多 N 条」算不出来（见 B 的第二个字段） | `pkg/tool/web_tools.go:782-788`、调用点 `:990-992` |
| 记忆笔记 | `memories_add_ad_hoc_note` | `filename`、`note`、`scope`（枚举 `project` / `global`，缺省 project） | `pkg/tool/session_tools.go:559-563` |
| 受保护读 | `read_file` | `file_path`、`resolved_file_path` + `force_tool_approval: true`、`justification`、`approval_reason` | `pkg/tool/file_tools.go:275-276` |

### 5. 退出计划模式卡：两处要动

- 计划正文**已经**是 Markdown 渲染（`ApprovalCard.vue:44-50` 的 `exit-plan-text` 盒，
  `<MessageResponse>` 在 `:49`），文案里没有「计划如下」这类前言，卡头只有 `chat.exitPlanMode`。
  **不要重画**。
- 但反馈输入框现在一直显示（`ApprovalCard.vue:108-113`），owner 要求**只有点了「继续规划」才出现**。

---

## 这次要落的东西

### A. 新增 `frontend/src/lib/approvalGates.ts`（纯函数，不碰 i18n、不碰 DOM）

一个「族」判定 + 事实抽取层，供两处复用（会话里的 `ApprovalCard`、工作台的 `PendingActionsPanel`）：

```ts
export type ApprovalFamily =
  | 'exit_plan_mode'        // 已有专用实现，这里只做转发（Step 5 只改反馈框）
  | 'web_fetch'             // 已实现，转发（本计划不改它的文案）
  | 'web_search'            // 未实现：今天走通用卡，本计划新建
  | 'memory_note'           // 未实现：今天走通用卡，本计划新建
  | 'file_write'            // write_file
  | 'file_edit'             // edit_file
  | 'apply_patch'
  | 'shell'
  | 'shell_outside_sandbox'
  | 'network_host'
  | 'request_permissions'
  | 'enter_plan_mode'
  | 'mcp_tool'
  | 'protected_read'
  | 'fallback'

export interface ApprovalFacts {
  family: ApprovalFamily
  /** 补丁/编辑的 diff 行（[符号, 文本]），空数组表示这一族不画 diff */
  diff?: ['' | '+' | '-', string][]
  /** 这次决定要看的一两条事实；纯文本，由调用方按序渲染 */
  facts: string[]
  /** 文件族的路径（会话卡里已显示过时可为空） */
  path?: string
  /** 新增/删除行数（引擎给得出的才填） */
  added?: number
  removed?: number
  /** 外网主机 */
  host?: string
  protocol?: string
  port?: number
  /** 申请权限的条目 */
  permissions?: { access: 'read' | 'write'; path: string }[]
  networkHosts?: string[]
}

export function approvalFamily(kind: string, payload: Record<string, unknown>, suggestion: PermissionSuggestionRecord | null): ApprovalFamily
export function approvalFacts(family: ApprovalFamily, payload: Record<string, unknown>, suggestion: PermissionSuggestionRecord | null): ApprovalFacts
```

**族的判定顺序（照引擎，不许调换）**：

1. `kind === 'exit_plan_mode'` → `exit_plan_mode`
2. `kind === 'user_interaction'` → 不是审批卡，调用方永远不该走进来（抛错或返回 `fallback` 都可以，但**不要**画卡）
3. `suggestion.networkApprovalContext` 存在 → `network_host`（这是 `shell` 载荷里的网络审批，
   `pkg/tool/shell_tool.go:517-524`、`:686-690`）
4. 沙箱外族，三种来源都算：`payload.approval_reason === 'sandbox_denied'`（覆盖 path-scoped 变体），
   或 `payload.sandbox_permissions === 'require_escalated'`，或 `payload.profile_elevation === true`
   → `shell_outside_sandbox`（`pkg/tool/shell_tool.go:762-786`、`:198-201`）
5. `kind === 'request_permissions'` → `request_permissions`
6. `kind === 'apply_patch'` → `apply_patch`
7. `kind === 'write_file'` → `file_write`
8. `kind === 'edit_file'` → `file_edit`
9. `kind === 'read_file'` **且** `payload.force_tool_approval === true` → `protected_read`
   （普通 `read_file` 是只读放行族，永远不该出现；出现即说明是受保护路径那一次提问）
10. `kind === 'web_search'` → `web_search`
11. `kind === 'memories_add_ad_hoc_note'` → `memory_note`
12. `kind` 以 `mcp__` 开头 → `mcp_tool`
13. `kind === 'shell'` → `shell`
14. `kind === 'enter_plan_mode'` → `enter_plan_mode`
15. `kind === 'web_fetch'` → `web_fetch`
16. 其余 → `fallback`

**事实抽取规则（这是本计划的灵魂，逐条照做）**：

- `file_write`：`added` = `content` 的行数（`split('\n').length`，末尾的一个换行不算）；
  `removed` = `payload.existing_lines`（**引擎新增的字段，见 B**；文件不存在时引擎给 `0`）。
  `facts`：新建（`removed === 0`）写 `["96 行"]`（**没有 `+`**，原型 `gateWriteNew()` 就是
  `96 行`）；覆盖写 `["+12 −3 行"]`（原型 `gateWriteOver()`）。
  `diff` = 全增行（`['+', line]`），与终端同一做法（`pkg/tui/overlays.go:1872-1879` 是 write 分支，
  用空串做 before）。
- `file_edit`：`old_string` / `new_string` 各自按行切开，`diff` 先全部 `-` 后全部 `+`
  （终端在没有统一 diff 时的兜底就是这个形状，`pkg/tui/overlays.go:1863-1871` 是 edit 分支）；
  `facts` = `["+3 −2 行"]`，**不写路径**（路径在调用行里）。
- `apply_patch`：`facts` = `["3 个文件", "+40 −12"]`；文件名取 `payload.resolved_paths` 每一项的
  basename（**载荷里没有 `files[]`**），多于 3 个写前三个 + `等 N 个`；`diff` 给 `patch` 里第一段
  `@@` 起的前 8 行（原文即可，不做语法高亮）。
- `shell` 危险命令：`facts` = 一句人话，说明这条命令会做什么。规则表放在 `approvalGates.ts` 顶部：
  `forced_rm` → `会强制删掉 <路径> 目录下的文件`（路径从 command 里取第一个非 `-` 的操作数；取不到就写
  `会强制删除文件`）。**除了这条，不许自己发明第二条危险规则**——引擎只有这一条。
  没有危险规则可套的 shell（`approval_reason` 是 `unknown_shell_command` 之类）→ `facts` = `[]`，
  只留问句与按钮；**不要**为了填满事实行去解释「这条命令为什么被问」。
- `shell_outside_sandbox`：`facts` = `["沙箱只允许改这个项目里的文件"]`
  （来源是 `payload.sandbox_denial_reason` 的人话版；如果该字段为空，写
  `沙箱拦下了这条命令`）。path-scoped 变体（`scoped_sandbox_retry === true`）说明的是范围而不是
  沙箱：`facts` = `["可写 <path>"]`，路径取 `payload.additional_permissions.file_system.entries[].path`。
  命令本身已经在调用行里，不要再抄一遍。
- `network_host`：`host` / `protocol` / `port` 取 `network_approval_context` 与 `network_port`；
  `facts` = `["api.github.com · HTTPS · 443"]`（无端口时省略端口）。
- `request_permissions`：`permissions` 逐条列出：读 → `可读 <path>`，写 → `可写 <path>`，
  `permissions.network.hosts` → `可连 <host>`。`facts` 每条一行。
- `enter_plan_mode`：`facts` = `[payload.reason]`，`reason` 为空则是 `[]`（只剩问句 + 两个按钮）。
  `reason` 是模型自己写的话；**空的时候不要编一句**（这是 owner 反复强调的一条：没说的文案不要加）。
- `mcp_tool`：`facts` = 载荷里前 3 个标量键，形如 `repo unionj-cloud/forebrain-harness`
  （值用等宽字体显示，键用小写原样，不改写模型给的参数名）。嵌套对象/数组跳过。
- `web_search`：`facts` = `["“<query>” · 最多 <n> 条"]`，`n` = `payload.results_limit`
  （**引擎新增的字段，见 B**）；字段缺失时退回 `5`。搜索词加引号：那是模型自己写的话。
  （原型 `webSearchGate()` 的计数就是 `NormalizeWebSearchLimit` 的口径：未给或 ≤0 → 5，>10 → 10。）
- `memory_note`：`payload` 的键就是 `filename` / `note` / `scope`（`pkg/tool/session_tools.go:559-563`，
  `scope` 是 `enum=project|global`，缺省即 project）。`facts` = `["<filename> · <作用域>"]`，
  作用域：`global` → 全局记忆，其余 → 项目记忆。`bodyMarkdown` = `payload.note` 原文，
  13px、限高 132px、可滚动、**不截断**（这一族有正文块，所以 `ApprovalFacts` 加一个可选
  `bodyMarkdown?: string`）。
- `protected_read`：`facts` = `[payload.resolved_file_path || payload.file_path]`。
- `fallback`：`facts` = `[]`（空）。**不要**再把 `approvalHint` 里的 action id 画出来。
  **这是与原型的一处有意差别，owner 2026-10-20 已裁决**：原型 `gateFallback()` 的事实行写的是
  `你给这个工具写了「询问」规则，默认它不问`，但那句只在「命中 ask 规则」时成立；走到
  `default_ask` 的工具并没有这条规则，印出来就是假话。裁决结果＝留空、
  按「没说的文案不要加」处理，**不接受**「无条件印原型那句」，也不要自己补一句替代文案。
  若将来要与原型逐字一致，必须按来源分叉（有 ask 规则才印），不能无条件印。

**行数算法要写进测试**：`content = "a\nb\n"` → 2 行（不是 3）；`""` → 0 行。

### B. 引擎补一个字段：`write_file` 覆盖前的旧文件行数

owner 2026-10-20 选定「引擎补旧行数」（原型里那句 `+12 −3` 引擎当时给不出来）。

改 `pkg/tool/file_tools.go` 的 write 分支（`ApprovalCard` 用的载荷在那里拼，`:850-851`）：
在 `payload := map[string]any{...}` 之后、`markProtectedApproval` 与调用 `hook` 之前，加

```go
// What the overwrite replaces. The approval card asks "may I write this file?",
// and the answer differs a lot between creating a file and replacing four
// hundred lines of one; the payload carried only the new content, so the card
// could not say which it was asking. Only the line count crosses the boundary —
// never the old content.
if before, err := os.ReadFile(abs); err == nil {
    payload["existing_lines"] = countTextLines(before)
} else {
    payload["existing_lines"] = 0
}
```

`countTextLines`：末尾的一个换行不算一行（`"a\nb\n"` → 2），空内容 → 0。放在
`pkg/tool/file_tools.go` 内部（小写、不导出），并在同文件加表驱动测试。

**不要**把旧内容放进载荷（那是把用户代码复制进 actions 表与事件的日志面）；只给行数。

### B2. 引擎补第二个字段：`web_search` 的条数

原型里搜索卡的事实行是「“query” · 最多 N 条」，而 `WebSearchPermissionPayload`
（`pkg/tool/web_tools.go:782-788`）今天只带 `query` 与 `rule_content`——**N 算不出来**。
改法很小，改函数签名并在唯一的调用点（`pkg/tool/web_tools.go:990-992`）传进去：

```go
// The card promises a size, and the tool already normalizes one: an absent limit
// is 5 and anything above 10 is capped. Putting the normalized number in the
// payload is what lets the approval card say "up to 5 results" instead of
// guessing or dropping the promise.
func WebSearchPermissionPayload(query string, limit int) map[string]any {
	q := strings.TrimSpace(query)
	return map[string]any{
		"query":         q,
		"rule_content":  "query:" + q,
		"results_limit": NormalizeWebSearchLimit(limit),
	}
}
```

调用点改成 `hook(ctx, "web_search", WebSearchPermissionPayload(q, in.Limit))`。
`NormalizeWebSearchLimit`（`:772-780`）就是原型用的那个口径，别另写一份夹取逻辑。

### C. `ApprovalCard.vue`：按族分派

把 `:170-216`（通用分支 170-196、权限记忆盒 197-215、错误行 216）整体替换成一个 `v-else-if` 链：

```
isExitPlan(a)           → 现有退出计划模式模板（只改反馈框，见 D）
isWebFetchAction(a)     → 现有抓取卡片（文案不动）
family === 'file_write' | 'file_edit' | 'apply_patch' | 'shell' | 'shell_outside_sandbox'
        | 'network_host' | 'request_permissions' | 'enter_plan_mode' | 'mcp_tool'
        | 'protected_read' → 共用的一张 <ApprovalGateCard>
family === 'fallback'   → 共用卡（两档）
```

`ApprovalGateCard`（可以就写在 `ApprovalCard.vue` 里，也可以独立成
`frontend/src/components/chat/ApprovalGateCard.vue`；两处调用方要能共用，所以**独立文件**更好）：

- 头行：问句（`q`）+ 右侧按钮组，单选一个 primary（`accept` / `grant_for_turn` / `accept_for_session`
  三者里出现的第一个）——**只有一枚 primary**，其余 `forebrain-btn-ghost`。
- 按钮**不要另写一套**：直接用 `approvalDecisionOptions(a)` 遍历、`approvalDecisionLabel(option, a)`
  取文案、`submitApprovalDecision(a, option)` 提交，并保留
  `:data-testid="'pending-decision-' + option.decision"`（抓取卡 `ApprovalCard.vue:142-156` 就是
  这个形状，既有 e2e 也按这些 testid 断言）。本计划对按钮的唯一改动发生在
  `approvalDecisionLabel` 内部（见 E 节），组件这边保持「遍历 + 提交」不变。
- 事实行：`facts` 逐条一行，`text-[12px] text-[var(--forebrain-text-2)] pl-4`，
  `break-words`（长路径/URL 折行不截断）。
- diff：`facts` 之后，等宽 12px，`+` 绿底 / `−` 红底，最多 40 行、超出滚动（`max-h-64 overflow-auto`）。
- 子代理发起的请求：保留现有的 `requesterLabel` 行（通用分支里的形状在 `ApprovalCard.vue:174-181`，
  抓取卡里是 `:159-166`）。
- 错误：保留 `errors[a.id]` 那一行。
- **不要**给这十二种卡加「权限记忆」框：每一张的第二档已经把它记住的范围写在自己的按钮上。

### D. 退出计划模式：反馈框只在「继续规划」之后出现

`ApprovalCard.vue:108-113` 的输入框改成 `v-if="planFeedbackOpen[a.id]"`；点「继续规划」
（`keepPlanning(a)`）时置 `true` 并保持焦点在输入框上；点其它任何决定、或取消反馈，
置回 `false` 并清空草稿。状态放在 `usePendingActions` 里（和 `modelPicking` 同一个位置，
`usePendingActions.ts:79-80` 那两个 `ref` 旁边；组件里 `modelPicking[a.id]` 的读法在
`ApprovalCard.vue:114` 一带），别放进组件局部——工作台面板要用同一份。

### E. 文案表（zh / en，键名照现有 `chat.*` 惯例）

**先看清一件事**：按钮文案不是从零新增的。`approvalDecisionLabel`
（`usePendingActions.ts:386-411`）已经覆盖全部十个 decision id，只是用的是一批**引擎词**
（`chat.approveOnce`「批准一次」、`chat.approveSession`「本会话内批准」、
`chat.approveRemember`「批准并记住」、`chat.grantTurn`「为本轮授权」、`chat.cancelRun`
「拒绝并中断本轮」，`locales/index.ts:1388-1403` / `:3045-3060`）。本计划要做的是**改写这批文案并
让它按族说动词**，不是加一套平行词汇。所以：

1. `approvalDecisionLabel` 的签名改成 `approvalDecisionLabel(option, action, family)`（或读一个已经
   按族算好的 `ApprovalFacts`），每个 decision id 的键改成下表；
2. 被替换掉的旧键（`chat.approveOnce` / `approveSession` / `approveRemember` / `approveCommandRule` /
   `approveCommandPrefix` / `approveCommandOnly` / `approveCommandVariants` /
   `approveCommandAnyVariant` / `grantTurn` / `grantTurnStrict` / `grantSession` / `cancelRun`）在
   全仓不再有引用后**删掉**（zh 与 en 两份都删），删完 `grep -rn "chat.approve\|chat.grantTurn\|
   chat.cancelRun" frontend/src` 只应剩下这次新增/保留的键；
   **owner 2026-10-20 已裁决：逐字替换并删掉旧键**（按仓库规则「删掉的语义就当做从来没有存在过」），
   不接受「键名不动、只改值」，也不接受「新旧两套并存」；代价是本计划的 diff 会触及既有文案、
   不只是新增，PR 描述里要写明。
3. `chat.deny`（`locales/index.ts:1388`）仍被非审批的落败分支使用的话就保留，否则一并删——
   以 grep 结果为准，不要凭印象。

**decision id → 按钮文案（每族一列，缺省即该族没有这一档）**

| decision id | file 族 | shell | network | permissions | mcp | protected_read | enter_plan | fallback |
|---|---|---|---|---|---|---|---|---|
| `accept` | `approvalYesWrite` / `approvalYesOverwrite` / `approvalYesEdit` / `approvalYesApply` | `approvalYesRun`（沙箱外族用 `approvalYesRunOutside`） | `approvalYesAllow` | — | `approvalYesCall` | `approvalYesRead` | `approvalYesEnterPlan` | `approvalYesAllow` |
| `accept_for_session` | `approvalScopeThisSession` | `approvalScopeThisCommand` | `approvalScopeSessionAll` | — | `approvalScopeThisSession` | `approvalScopeThisSession` | — | — |
| `accept_and_remember` | — | `approvalScopeAlways` | — | — | `approvalScopeAlways` | — | — | — |
| `accept_with_execpolicy_amendment` | — | `approvalScopeThisCommand`（`commandScope` 里带前缀时就写前缀，沿用现有 `commandScopeLabel`） | — | — | — | — | — | — |
| `apply_network_policy_amendment` | — | — | `approvalScopeAlwaysAllow` | — | — | — | — | — |
| `grant_for_turn` | — | — | — | `approvalYesGrantTurn` | — | — | — | — |
| `grant_for_turn_with_strict_auto_review` | — | — | — | `approvalYesGrantTurnReviewed` | — | — | — | — |
| `grant_for_session` | — | — | — | `approvalYesGrantSession` | — | — | — | — |
| `decline` | — | — | — | `approvalNoGrant` | — | — | — | — |
| `cancel` | `approvalNoWrite` / `approvalNoEdit` / `approvalNoApply` | `approvalNoRun` | `approvalNoAccess` | — | `approvalNoCall` | `approvalNoRead` | `approvalNoEnter` | `approvalNo` |

`file` 族里 `write_file` 还要按 `existing_lines > 0` 分成「覆盖」（`approvalYesOverwrite` +
`approvalNoWrite`）与「新建」；搜索与记忆两族只有两档：`accept` / `cancel`（见 A 节判定）——
所以它们的按钮用 `approvalSearchYes|No`、`approvalMemoryYes|No`，不走上表的动词列。

中文（左）／英文（右）。括号里是这条文案服务的族。

| 键 | zh | en |
|---|---|---|
| `approvalWriteNewQuestion` | 要新建这个文件吗？ | Create this file? |
| `approvalWriteOverQuestion` | 要覆盖这个文件吗？ | Overwrite this file? |
| `approvalEditQuestion` | 要改这个文件吗？ | Change this file? |
| `approvalPatchQuestion` | 要应用这个补丁吗？ | Apply this patch? |
| `approvalShellQuestion` | 要运行这条命令吗？ | Run this command? |
| `approvalShellOutsideQuestion` | 沙箱拦下了这条命令，要在沙箱外运行吗？ | The sandbox blocked this command. Run it outside the sandbox? |
| `approvalNetworkQuestion` | 要让它访问外网吗？ | Let it reach the public network? |
| `approvalPermissionsQuestion` | 要给它这些权限吗？ | Grant these permissions? |
| `approvalEnterPlanQuestion` | 要进入计划模式吗？ | Enter plan mode? |
| `approvalMCPQuestion` | 要调用 {server} 的 {tool} 吗？ | Call {tool} on {server}? |
| `approvalSearchQuestion` | 要我搜一下公开网络吗？ | Search the public web? |
| `approvalSearchYes` | 搜索 | Search |
| `approvalSearchNo` | 不要搜 | Don't search |
| `approvalSearchFacts` | “{query}” · 最多 {n} 条 | “{query}” · up to {n} results |
| `approvalMemoryQuestion` | 要我把这条记进长期记忆吗？ | Keep this in long-term memory? |
| `approvalMemoryYes` | 记住这条 | Keep it |
| `approvalMemoryNo` | 不要记 | Don't keep it |
| `approvalMemoryFacts` | {file} · {scope} | {file} · {scope} |
| `approvalMemoryScopeProject` | 项目记忆 | Project memory |
| `approvalMemoryScopeGlobal` | 全局记忆 | Global memory |
| `approvalProtectedReadQuestion` | 这是 Forebrain 自己的配置文件，要读吗？ | This is one of Forebrain's own configuration files. Read it? |
| `approvalFallbackQuestion` | 要允许这次调用吗？ | Allow this call? |
| `approvalYesWrite` | 写入 | Write |
| `approvalYesOverwrite` | 覆盖 | Overwrite |
| `approvalYesEdit` | 修改 | Change |
| `approvalYesApply` | 应用 | Apply |
| `approvalYesRun` | 运行 | Run |
| `approvalYesRunOutside` | 在沙箱外运行 | Run outside the sandbox |
| `approvalYesAllow` | 允许 | Allow |
| `approvalYesGrantTurn` | 本轮允许 | Allow this turn |
| `approvalYesGrantTurnReviewed` | 本轮允许，自动审查 | Allow this turn, auto-reviewed |
| `approvalYesGrantSession` | 本会话允许 | Allow this session |
| `approvalYesEnterPlan` | 进入 | Enter |
| `approvalYesCall` | 调用 | Call |
| `approvalYesRead` | 读取 | Read |
| `approvalScopeThisSession` | 本会话不再问 | Don't ask again this session |
| `approvalScopeThisCommand` | 这类命令不再问 | Don't ask again for commands like this |
| `approvalScopeAlways` | 以后都不再问 | Don't ask again |
| `approvalScopeSessionAll` | 本会话都允许 | Allow all this session |
| `approvalScopeAlwaysAllow` | 以后都允许 | Always allow |
| `approvalNoWrite` | 不要写 | Don't write |
| `approvalNoEdit` | 不要改 | Don't change |
| `approvalNoApply` | 不要应用 | Don't apply |
| `approvalNoRun` | 不要运行 | Don't run |
| `approvalNoAccess` | 不要访问 | Don't access |
| `approvalNoCall` | 不要调用 | Don't call |
| `approvalNoRead` | 不要读 | Don't read |
| `approvalNoEnter` | 不要进入 | Don't enter |
| `approvalNo` | 不要 | No |
| `approvalNoGrant` | 不授予 | No |
| `approvalFactsLines` | +{added} −{removed} 行 | +{added} −{removed} lines |
| `approvalFactsNewLines` | {added} 行 | {added} lines |
| `approvalFactsFiles` | {count} 个文件 | {count} files |
| `approvalFactsSandboxOnlyProject` | 沙箱只允许改这个项目里的文件 | The sandbox only allows changes inside this project |
| `approvalFactsSandboxBlocked` | 沙箱拦下了这条命令 | The sandbox blocked this command |
| `approvalFactsForcedRm` | 会强制删掉 {path} 目录下的文件 | Force-deletes files under {path} |
| `approvalFactsForcedRmNoPath` | 会强制删除文件 | Force-deletes files |
| `approvalFactsRead` | 可读 {path} | Read {path} |
| `approvalFactsWrite` | 可写 {path} | Write {path} |
| `approvalFactsReach` | 可连 {host} | Reach {host} |

**文案硬规则**（owner 2026-10-20，违者视为未完成）：

1. 问句一句话说「这一次要发生什么」，不出现工具内部名、action id、JSON、`permission`。
2. 按钮动词开头；范围档写清楚「记住多久」，拒绝档写 `不要 + 动词`。
3. 事实行只放这次决定要看的一两件事；**问句里说过的对象，事实行不再重复**。
4. 不新增任何解释性文案（没有「这会修改你的文件」这类提示）。

### F. 工作台面板同步

`frontend/src/components/chat/PendingActionsPanel.vue`：审批区 `:16-165`（通用分支 117-143、
权限记忆盒 144-162），ask 区 `:166-203`。审批区改成渲染同一个 `ApprovalGateCard`，文案随同一张表。
**不要**在两处各写一份卡片。

同时有一处两表面已经不一致、本计划顺手统一（属于同一张卡的分派条件，不是新功能）：审批记忆盒的
显示条件在 `ApprovalCard.vue:197` 是 `!isExitPlan && !isWebFetchAction && suggestion`，
在 `PendingActionsPanel.vue:144` 少了 `!isWebFetchAction` 一项，于是抓取审批在工作台里会多画一个
记忆盒。改成两处都走 `approvalFamily()`，条件只剩一处。

注意 owner 2026-10-20 的另一条裁决：询问用户与更新待办这两张卡**没有**「在工作台查看」的入口，
工作台面板只为审批服务；这条本计划不改变，别顺手加回去。

---

## Steps（每步都要跑它自己的验证命令）

### Step 1 — 引擎：两个字段

1. `pkg/tool/file_tools.go`：在 write 分支的 `payload` 构造处加 `existing_lines`（见 B）。
2. 新增 `countTextLines`，同文件。
3. `pkg/tool/web_tools.go`：`WebSearchPermissionPayload` 加 `results_limit`（见 B2），
   同步改唯一的调用点（`:990-992`）与既有测试 `pkg/tool/web_tools_test.go:883`
   （`TestWebSearchPermissionPayloadIncludesQueryRule`，签名多一个参数）。
4. 测试：`pkg/tool/file_tools_test.go` 新增
   `TestWriteApprovalPayloadCarriesTheLineCountItReplaces`（表驱动：不存在 → 0；`"a\nb\n"` → 2；
   `""` → 0；`"a"` → 1），断言 `payload["existing_lines"]` 与 `content` 仍在载荷里；
   `pkg/tool/web_tools_test.go` 新增一条断言 `results_limit` 是规范化后的值
   （0 → 5、7 → 7、50 → 10）。

验证：

```bash
gofmt -l pkg/tool
go vet -tags fts5 ./pkg/tool
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'WriteApproval|FileWrite' -count=1
```

期望：`gofmt` 无输出、`vet` 无输出、`ok`。

### Step 2 — `approvalGates.ts` + 单测

1. 新建 `frontend/src/lib/approvalGates.ts`（见 A）。
2. 新建 `frontend/src/lib/approvalGates.test.ts`：每族一条正例 + 判定顺序的反例
   （尤其是：网络审批必须先于 `shell_outside_sandbox`；`read_file` 只有带
   `force_tool_approval` 才是 `protected_read`；`approval_reason: 'sandbox_denied'` 的
   path-scoped 载荷也判成 `shell_outside_sandbox` 且事实是「可写 <path>」；
   `apply_patch` 的文件名来自 `resolved_paths`）。
3. 行数/事实字符串的断言逐条写死，不写「包含」这种松断言。
4. decision id → 文案的映射新建一份测试（放 `frontend/src/lib/approvalGates.test.ts` 或
   新建 `frontend/src/composables/usePendingActions.test.ts`，二选一，别两处都写）：十个 decision id
   在每一族的返回值对本文 E 表逐字断言；同时断言没有任何一个返回值还等于
   `批准一次` / `拒绝并中断本轮`。

验证：

```bash
cd frontend && corepack pnpm exec vue-tsc --noEmit
cd frontend && corepack pnpm test src/lib/approvalGates.test.ts
```

期望：`vue-tsc` 退出 0；vitest 全绿。

### Step 3 — `ApprovalGateCard.vue` + 文案表

1. 新建 `frontend/src/components/chat/ApprovalGateCard.vue`（见 C）。
2. `frontend/src/locales/index.ts`：把 E 表的键**同时**加进 zh 与 en 两个对象
   （zh 从 `:7` 起、en 从 `:1672` 起，两个对象是分开的，别只加一处）；同时按 E 节第 2 条
   删掉被替换的旧键（两份都删）。
3. 组件测试 `frontend/src/components/chat/ApprovalGateCard.test.ts`：
   一个 primary、事实行顺序、diff 符号、长 URL 折行（`break-words` 类存在）、
   子代理请求者行只在 `subagentType` 非空时出现。

验证：

```bash
cd frontend && corepack pnpm exec vue-tsc --noEmit
cd frontend && corepack pnpm test src/components/chat/ApprovalGateCard.test.ts
```

### Step 4 — `ApprovalCard.vue` 接线

1. 把 `:170-216` 的通用分支换成族分派（见 C）。
2. 保住既有测试：`frontend/src/components/chat/ApprovalCard.test.ts` 里抓取那几条必须继续绿
   （它们的按钮顺序与文案不变）。
3. 新增用例：十四族（12 张新卡 + 抓取 + 退出计划模式）与兜底各渲染一次，断言问句文案、
   按钮文案、事实行；并断言
   **卡片里不出现 `write_file` / `mcp__` / `action id`**（`expect(wrapper.text()).not.toContain('write_file')` 这类）。

验证：

```bash
cd frontend && corepack pnpm test src/components/chat/ApprovalCard.test.ts
```

### Step 5 — 退出计划模式：反馈框门控

1. `usePendingActions.ts`：加 `planFeedbackOpen: Record<string, boolean>` 与开关函数。
2. `ApprovalCard.vue`：输入框按 `planFeedbackOpen[a.id]` 显示；`keepPlanning` 打开它；
   其它决定与取消关闭它。
3. 测试：`ApprovalCard.test.ts` 加三条——初始不显示；点「继续规划」显示；点「批准」后又消失。

验证：

```bash
cd frontend && corepack pnpm test src/components/chat/ApprovalCard.test.ts
```

### Step 6 — 工作台面板复用同一张卡

`PendingActionsPanel.vue` 的审批区块换成 `ApprovalGateCard`。它的既有测试
（`PendingActionsPanel.test.ts`）会因结构变化而断——**这部分的断言要按新卡片改，不是文案替换**：

- `:35-37` 现在断言「卡片文本包含 `write the skill file`（那是 `justification`）」+ 点
  `[data-testid="pending-approve"]` 触发 `actionsApprove('a1', {})`。新卡片把决定按钮换成
  `pending-decision-accept`（走 `submitApprovalDecision`），且事实行不再印 `justification`
  ——改成断言问句（`要新建这个文件吗？`）+ 点 `pending-decision-accept`。
- `:75` 的 `run the tests` 同理（shell 族的事实行只放规则句）。
- `:112-117` 的 `explorer` 请求者行保持不变（那是 `requesterLabel`，本计划不动）。

**这是有意的行为变更**，在 PR 描述里写明。

验证：

```bash
cd frontend && corepack pnpm test src/components/chat/PendingActionsPanel.test.ts
```

### Step 7 — e2e：真 gateway 上的十二族

新增 `frontend/e2e/approval-families.spec.ts`，做法照现有
`frontend/e2e/exit-plan-approval.spec.ts`（种一条 pending action，让真 gateway 的
`/api/actions` 吐出来，再断言 DOM）：

1. `write_file`（带 `existing_lines: 41`, `content` 96 行）→ 问句「要覆盖这个文件吗？」、
   事实「+96 −41 行」、按钮「覆盖 / 本会话不再问 / 不要写」；同一份 spec 里再种一条**不带**
   `existing_lines` 的 `write_file` → 问句「要新建这个文件吗？」、事实「96 行」（没有 `+`）。
2. `shell` + `command_safety` + `approval_reason: dangerous_shell_syntax` → 「要运行这条命令吗？」
   + `forced_rm` 规则句；再种一条 `approval_reason: unknown_shell_command` → 同样问句、
   **没有**事实行。
3. `shell` + `network_approval_context` → 「要让它访问外网吗？」+ 主机行 + **四**个按钮。
4. `shell` + `approval_reason: sandbox_denied` + `sandbox_denial_reason` → 「沙箱拦下了这条命令…」；
   另加一条 path-scoped（`scoped_sandbox_retry: true` + `additional_permissions`）→ 事实「可写 <path>」。
5. `request_permissions` → 三条权限行 + 四个按钮。
6. `mcp__github__create_issue` → 「要调用 github 的 create_issue 吗？」+ 四个按钮。
7. 断言整页没有出现 `write_file`、`mcp__github__create_issue`、`批准一次`、`拒绝并中断本轮`。

验证（定向，不跑全量）：

```bash
FOREBRAIN_E2E_SPEC=e2e/approval-families.spec.ts scripts/acceptance/web_e2e.sh
```

期望最后一行 `web e2e: PASS`。（全量 e2e 属发布门禁，不在本计划的完成条件里。）

---

## Done criteria（机器可判）

```bash
gofmt -l pkg/tool pkg/gateway                        # 无输出
go vet -tags fts5 ./pkg/tool ./pkg/gateway            # 无输出
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/gateway -count=1   # ok
cd frontend && corepack pnpm exec vue-tsc --noEmit    # 退出 0
cd frontend && corepack pnpm test                     # 全绿（新增用例在内）
FOREBRAIN_E2E_SPEC=e2e/approval-families.spec.ts scripts/acceptance/web_e2e.sh   # web e2e: PASS
```

外加两条人工可核对项：

- `grep -rn "批准一次\|拒绝并中断本轮\|Deny and interrupt turn" frontend/src` **零命中**
  （旧键按 E 节删干净；若还有残留，说明漏了一处调用点，逐条报告，不要顺手改文案）。
- 各族问句与按钮与本文 E 表逐字一致（`grep` 对照 locales）；抓取卡的四个键
  （`chat.webFetchQuestion / webFetchAccept / webFetchRemember / webFetchDecline`）未被改动。

---

## 不在本计划范围

- 终端 TUI 的审批面板（owner：本期保持现状）。
- 抓取卡（`web_fetch`）的文案与结构：已经上线并验收过（`ApprovalCard.vue:136-169`、
  `locales/index.ts:1404-1407`），本计划只把它接进同一个分派器，**字号与措辞一个字不改**。
- 注意：`web_search` 与 `memories_add_ad_hoc_note` 在 web 上**还没有**专门卡片（今天走通用卡），
  它们属于本计划要新建的两族，文案照 E 表（出自已审批原型）。
- 退出计划模式的计划正文渲染（已经是 Markdown，`ApprovalCard.vue:44-50`）。
- plan-reviewer 的子代理卡片（已经在 `useChatStream.ts:4105-4130` 里独立成块，不重画）。
- 「拒绝后模型看到什么」这类引擎语义（`permission_types.go` 的决定集合一个字都不改）。

---

## 维护提示

- 新的审批族必须同时改三处，否则界面会掉回通用卡：`BuildApprovalDecisionOptions`
  （`pkg/safety/permission_types.go`）、`approvalGates.ts` 的判定顺序、E 表的文案。
- 引擎新增一个会问人的工具时，**先**问清楚它的决定集合，再画卡；没有决定集合的工具不该有卡。
- `existing_lines` 只活在审批载荷与 actions 表里，不落 transcript；任何把它写进事件/日志的改动都要重新评估
  （行数不敏感，但仍属用户代码的元数据）。

## 逃生口（遇到这些就停下来报告，不要即兴发挥）

1. `write_file` 的 hook 调用点找不到能拿到 `abs` 的位置（例如载荷在别处拼）→ 停。
2. `payloadJson` 在真机上拿不到 `content`（说明网关做了裁剪）→ 停，报告实际字段。
3. `permissionSuggestion.availableDecisions` 与 `permission_types.go` 的序列不一致（说明网关映射有洞）
   → 停，报告是哪一族。
4. 某族在真机上根本不会出现（例如 `enter_plan_mode` 被 YOLO/审批策略短路）→ 保留卡片实现，
   但在交付报告里写明「该族在当前默认配置下不可达」，不要为了让 e2e 变绿去伪造路径。
