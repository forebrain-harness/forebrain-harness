# REMOVE_DIFF_HELP_SLASH_PLAN — 完全删除 /diff 与 /help 两个 slash 命令

Planned at: 工作区基线 `git rev-parse --short HEAD` = `88eb320`，且带 owner 未提交改动
（`pkg/tui/render.go`、`pkg/tui/render_test.go`、`pkg/tui/chat_session_test.go`、
`docs/plan/MASCOT_WHOLE_CELL_PIXELS_PLAN.md` 等）。实施只做手术式 edit_file 精确匹配，
**绝不整文件覆盖或 cp 快照恢复**——owner 会与执行者并行改同一工作区。
Drift 检查：以本计划 file:line 对照当前源码（行号可能因 owner 并行改动位移，锚点以代码内容为准）。

批准后第一步：本计划全文落盘到仓库 `docs/plan/REMOVE_DIFF_HELP_SLASH_PLAN.md`（仓库惯例），
然后按步骤实施；全部改动留在工作区，**不 commit**。

---

## 1. 目标（Why this matters）

owner 裁决：把 `/diff` 与 `/help` 两个 slash 命令**完全删除、清理干净**，TUI 端与
web 端都必须删净。范围 = 命令注册、两端执行链、专属 handler 接口与实现、专属引擎
函数、装配点、测试、README 行、指向 `/help` 的注释文案。不留 dead code、不留孤儿测试。

## 2. 现状与证据链（file:line，2026-10-07 实测）

命令链是四层结构：注册表（slash.go `commands`）→ 共享执行器（executor.go switch）→
`turn.Context` 上的窄接口（model.go）→ 每个 surface 的实现（TUI ChatSession / gateway
Server）+ `/diff` 专属引擎 `turn.ExecuteDiffSlash`（内部唯一依赖 `tool.ProjectDiff`）。

### /diff 链
| 层 | 位置 |
|---|---|
| 注册 | `pkg/turn/slash.go:93` `{Name: "diff", …}` |
| defaultCategory | `pkg/turn/slash.go:236` `case "compact", "diff", "memories":` |
| 执行器分发 | `pkg/turn/executor.go:90-91` `case "diff": return execDiff(ctx, toks)` |
| execDiff | `pkg/turn/executor.go:714-720` |
| 引擎 | `pkg/turn/slash.go:671-697` `ExecuteDiffSlash`（fenced 参数区分两 surface） |
| Context 接口 | `pkg/turn/model.go:237-239` `DiffSlashHandler`；`:453` `Diff DiffSlashHandler` 字段 |
| TUI 装配 | `pkg/tui/chat_session.go:1474` `Diff: s,` |
| TUI 实现 | `pkg/tui/chat_slash.go:1283-1290` `ChatSession.HandleDiffSlash`（fenced=false） |
| TUI 原生拦截 | `pkg/tui/run.go:1201-1203` `case "diff": cmds.handleDiff(…)` |
| TUI 渲染 | `pkg/tui/commands.go:1803-1823` `handleDiff`；`:1825-1833` `diffFrameTitle`（唯一调用方是 handleDiff） |
| TUI 会话接口 | `pkg/tui/notify.go:1488` `HandleDiffSlash` 方法声明 |
| gateway 装配 | `pkg/gateway/server.go:2215` `Diff: s,` |
| gateway 实现 | `pkg/gateway/slash_handlers.go:218-227` `Server.HandleDiffSlash`（fenced=true） |
| 引擎依赖 | `pkg/tool/workspace.go:19-61` `ProjectDiff`；`:15-17` `ErrNotGitRepository`；`:63-87` `runGit`（该文件内仅 ProjectDiff 使用；`pkg/memory`、`pkg/assembly` 各有自己私有的 runGit，无关） |

### /help 链
| 层 | 位置 |
|---|---|
| 注册 | `pkg/turn/slash.go:99` `{Name: "help", …}` |
| 执行器分发 | `pkg/turn/executor.go:110-111` `case "help": return …CommandCatalog…` |
| TUI 原生拦截 | `pkg/tui/run.go:1156` `case "", "help":`（裸 `/` 与 `/help` 同支） |
| 目录函数 | `pkg/turn/executor.go:157-185` `CommandCatalog`（**保留**：裸 `/` 入口 `executor.go:31-33` 与 TUI `run.go:1157` 仍在用；仅注释提到 /help 需改） |
| 注释文案 | `pkg/turn/executor.go:157` 注释 "is /help on every surface"；`pkg/tui/setup.go:1691` 注释 "the rest are listed under /help." |

### 测试与文档
- `pkg/turn/slash_test.go:35`（side 过滤断言 `names["diff"]`）、`:119-121`（recordingSlashHandlers.HandleDiffSlash fake）、`:171`（`Diff: h,` 装配）、`:308`（hint 用例 `"diff": "/diff main.go"`）、`:333-357`（TestExecuteDiffSlashSaysWhyThereIsNoDiff 整个）、`:360-378`（TestHelpCatalogAndUnknownCommandSuggestions 的 `/help` 执行部分）、`:428`（`"help": SubagentViewGlobal`）、`:435`（`"diff": SubagentViewGlobal`）、`:484`（shown 列表含 help/diff）
- `pkg/turn/input_parts_test.go:504` 硬编码 removed 列表 `[]string{"copy","side","statusline","title","todo"}` → 追加 "diff","help"
- `pkg/tui/run_test.go:562`（`diffReply` 字段）、`:989-995`（fakeSession.HandleDiffSlash）
- `pkg/tui/commands_test.go:2324-2345`（TestHandleDiffDrawsADiffOrItsReason 整个）
- `pkg/tui/chat_session_test.go:2113-2130`（TestHandleDiffSlashShowsGitDiff 整个）
- `pkg/gateway/slash_handlers_test.go:172-192`（TestHandleDiffSlashShowsGitDiff 整个）
- `pkg/gateway/api_extra_test.go:89`（`names["diff"]` 断言行）
- `pkg/tool/workspace_test.go`：36/64/74 三个测试全部是 ProjectDiff 的 → 整文件删除
- `README.md:134`（`/diff` 行）、`:139`（`/help` 行）

### 明确不碰（看似相关但保留）
- `pkg/tui/render.go` 的 `RenderDiff`/`DiffTheme`/`WithDiffTheme`：turn diff、approval diff
  （`overlays.go renderApprovalDiff`）、markdown diff code block 高亮共用，非 /diff 专属。
- `pkg/event/turndiff.go`、`pkg/tool/semantic.go CompactDiff`、`pkg/tool/format.go`、
  `pkg/memory/workspace.go`、`pkg/assembly/git.go`：各自领域的 diff 基础设施。
- `pkg/tui/chat_session_test.go:21599-21600` SlashOverlay 状态机测试用 `Name: "help"` 作
  任意 fixture 名，不查命令表，与 /help 命令无关。
- `pkg/turn/events_test.go:50-70`：turn_diff payload 测试，`decoded["diff"]` 与 slash 无关。
- `cmd/forebrain/root_test.go:50-58`：Cobra CLI 的 help 命令/flag，不是 slash 命令。
- `docs/plan/**` 历史计划文档（含提到 /diff 的进度/设计记录）：历史档案不改。
- `frontend/`：命令面板数据来自 `/api/slash/commands`（`pkg/gateway/api_extra.go:452` 表驱动），
  注册删除后列表自动少两项；前端无 /diff /help 硬编码（已 grep 验证，仅一条无关测试注释）。
- channel 适配器：走 `ExecuteDynamicOnly`（只放行 skill 命令），本来就不执行 diff/help。
- `defaultCategory` 里 "context" 独立 case 等相邻代码：不做顺手合并/清理。

## 3. 修复设计

### 行为变化矩阵
| 场景 | 旧行为 | 新行为 | 备注 |
|---|---|---|---|
| TUI 输入 `/diff [path]` | 原生 handleDiff 渲染 diff 帧 | `UnknownCommandReply`："There is no /diff; …"（did-you-mean / type / 提示） | 刻意修订 |
| TUI 输入 `/help` | 原生显示命令目录帧 | 同上未知命令提示 | 刻意修订 |
| TUI 输入裸 `/` | 显示命令目录（CommandCatalog） | **不变** | `run.go:1156` 改为 `case "":` |
| Web 输入 `/diff` `/help` | executor 执行（diff 带 fence / help 目录） | 消息回复未知命令提示 | 刻意修订 |
| Web/TUI 命令面板（输入 `/` 弹出） | 含 diff、help 两项 | 两项消失（表驱动） | 自动 |
| skill/动态命令想注册名为 diff 或 help | 覆盖检查仅对 builtin 生效，diff/help 删表后可被 skill 占用 | `ReplaceDynamicSource` 报错 "uses a removed slash command name" | 加入 `removedSlashCommandNames`（仓库既有机制，先例 copy/side/statusline/title/todo） |
| git diff 能力本身 | 也有 shell 工具可用 | 不变（本任务只删 slash 命令） | |

### 关键设计裁决
1. **`CommandCatalog` 保留**：裸 `/` 是目录的唯一入口（executor.go:31-33 兜底 + TUI run.go:1157
   原生），/help 只是它的一个别名入口。仅更新 executor.go:157 注释，不再说 "is /help"。
2. **`removedSlashCommandNames` 追加 `"diff"`、`"help"`**：防止 skill 静默占用已删命令名造成
   语义歧义；`IsBuiltinName`（slash.go:130）随之把这些名视为保留名。同步把两个名字加进
   `input_parts_test.go:504` 的硬编码列表。
3. **`tool.ProjectDiff`/`ErrNotGitRepository`/`runGit` 一并删除**：唯一生产调用方是
   `ExecuteDiffSlash`；留下即 dead code，违背"清理干净"。`pkg/tool/workspace.go` 只保留
   `attachTurnDiff`（turn diff 用），清理 imports；`workspace_test.go` 整文件删除。
4. **TUI `setup.go:1691` 注释**改为指向 `/`（banner 里 `/` 就是 commands 入口）。

## 4. Scope

**In scope**：`pkg/turn/{slash.go,executor.go,model.go,slash_test.go,input_parts_test.go}`、
`pkg/tui/{run.go,run_test.go,chat_session.go,chat_slash.go,commands.go,commands_test.go,chat_session_test.go,notify.go,setup.go}`、
`pkg/gateway/{server.go,slash_handlers.go,slash_handlers_test.go,api_extra_test.go}`、
`pkg/tool/{workspace.go,workspace_test.go}`、`README.md`。

**Out of scope**：第 2 节"明确不碰"清单全部；frontend/、docs/plan/、通用 diff 渲染设施、
Cobra CLI help、channel 适配器、`pkg/architecture` 包图（无包增删，不改 import 边界
——`pkg/turn` 对 `pkg/tool` 的 import 因其他代码仍存在而保留，实施后用
`scripts/package-graph.sh` 验证 graph.json 不变）。

## 5. Steps

每步完成后跑该步验证；步骤 7-9 是全量收口。

1. **仓库计划落盘**：本计划全文写至 `docs/plan/REMOVE_DIFF_HELP_SLASH_PLAN.md`。
   验证：文件存在且含本标题。
2. **pkg/turn 注册与引擎**：
   - slash.go:93 删 `/diff` 注册行；:99 删 `/help` 注册行；
   - slash.go:236 `case "compact", "diff", "memories":` → `case "compact", "memories":`；
   - slash.go:384-390 `removedSlashCommandNames` 追加 `"diff": {}` 与 `"help": {}`；
   - slash.go:671-697 删 `ExecuteDiffSlash` 及其注释。
   验证：`grep -n '"diff"\|"help"\|ExecuteDiffSlash' pkg/turn/slash.go` 仅剩 removed 名单两行。
3. **pkg/turn 执行器与模型**：
   - executor.go:90-91 删 `case "diff"` 两行；:110-111 删 `case "help"` 两行；
   - executor.go:714-720 删 `execDiff`；
   - executor.go:157-158 注释改为不提 /help（例：`// CommandCatalog is the bare "/" answer on every surface: …`）；
   - model.go:237-239 删 `DiffSlashHandler` 接口；:453 删 `Diff DiffSlashHandler` 字段。
   验证：`grep -n 'execDiff\|DiffSlashHandler\|case "diff"\|case "help"' pkg/turn/*.go` 无输出。
4. **pkg/tui**：
   - run.go:1156 `case "", "help":` → `case "":`；:1201-1203 删 `case "diff"` 三行；
   - chat_session.go:1474 删 `Diff: s,`；
   - chat_slash.go:1283-1290 删 `ChatSession.HandleDiffSlash`；
   - commands.go:1803-1833 删 `handleDiff` 与 `diffFrameTitle`（含注释）；
   - notify.go:1488 删接口方法声明行；
   - setup.go:1691 注释 "listed under /help" → 指向 `/`。
   验证：`grep -rn 'HandleDiffSlash\|handleDiff\|diffFrameTitle\|case "diff"' pkg/tui/*.go` 无输出。
5. **pkg/gateway**：
   - slash_handlers.go:218-227 删 `Server.HandleDiffSlash`（含注释）；
   - server.go:2215 删 `Diff: s,`。
   验证：`grep -rn 'HandleDiffSlash\|Diff:' pkg/gateway/*.go` 无 diff 相关输出。
6. **pkg/tool**：
   - workspace.go 删 `ErrNotGitRepository`、`ProjectDiff`、`runGit`，保留 `attachTurnDiff`，
     清理 imports（bytes/os/exec/fmt/runtime/errors 中仅删除部分使用的）；
   - 删除 `pkg/tool/workspace_test.go` 整文件。
   验证：`grep -rn 'ProjectDiff\|ErrNotGitRepository' pkg/ cmd/` 无输出；
   `CGO_ENABLED=1 go build ./pkg/tool`。
7. **测试修订**（第 2 节测试清单逐项）：
   - slash_test.go：:35 删 diff 断言行；:119-121 删 fake 方法；:171 删 `Diff: h,`；
     :308 删 diff 用例；:333-357 删 TestExecuteDiffSlashSaysWhyThereIsNoDiff；
     TestHelpCatalogAndUnknownCommandSuggestions（:360-378）改名为
     `TestUnknownCommandSuggestionsAndBareSlashCatalog`：`/help` 执行断言改为
     `Execute(ctx, "/")` 断言同样的 Commands/Skills 目录输出（CommandCatalog 覆盖不丢），
     并新增断言 `Execute(ctx, "/help")` 回复以 "There is no /help" 开头；
     :428/:435 删两行；:484 shown 列表删 "help"、"diff" 两词；
   - input_parts_test.go:504 列表追加 "diff","help"；
   - run_test.go：:562 删 `diffReply` 字段，:989-995 删 fake 方法；
   - commands_test.go:2324-2345 删 TestHandleDiffDrawsADiffOrItsReason；
   - chat_session_test.go:2113-2130 删 TestHandleDiffSlashShowsGitDiff（注意该文件带
     owner 未提交改动，只删本测试函数块）；
   - slash_handlers_test.go:172-192 删 TestHandleDiffSlashShowsGitDiff；api_extra_test.go:89
     删 `require.True(t, names["diff"])` 一行。
8. **README.md**：删 :134 `/diff` 行与 :139 `/help` 行。
   验证：`grep -n '/diff\|/help' README.md` 无输出。
9. **全量验证**：
   ```
   CGO_ENABLED=1 go build ./...
   CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/tui ./pkg/gateway ./pkg/tool -count=1
   go vet ./...
   scripts/package-graph.sh && git diff --stat pkg/architecture/testdata/graph.json   # 必须无变化
   ```
10. **tmux 真机验收**（临时 `FOREBRAIN_HOME` + 临时项目目录，构建带 `-tags fts5`）：
    - 场景 1（删除生效）：TUI 输入 `/diff` → 系统帧回复 "There is no /diff; …"；
      `/help` → "There is no /help; …"；capture-pane 取证。
    - 场景 2（目录回归）：裸 `/` → 命令目录帧正常，列表中无 diff、help 两项。
    - 场景 3（web parity）：`curl '.../api/slash/commands?surface=webchat'` 返回无
      diff/help；gateway 会话内提交 `/diff` 得到同样的未知命令回复。
    - 场景 4（skill 占名被拒）：注册名为 help 的动态命令报 "uses a removed slash
      command name"（由单元测试覆盖即可，真机可省）。
    - 结束清理：kill-session、删临时目录。

## 6. Done criteria（机器可查）

- `grep -rn '"diff"\|"help"' pkg/turn/slash.go` 除 removedSlashCommandNames 两行外无输出；
- `grep -rn 'HandleDiffSlash\|ExecuteDiffSlash\|execDiff\|handleDiff\|DiffSlashHandler\|diffFrameTitle\|ProjectDiff\|ErrNotGitRepository' pkg/ cmd/` 无输出；
- `grep -n '| `/diff`\|| `/help`' README.md` 无输出；
- 第 5 步 9 的四条命令全绿，graph.json 无 diff；
- tmux 场景 1-3 逐屏取证通过。

## 7. STOP conditions

- 删除 `Context.Diff` 字段导致编译错误指向本清单之外的调用方（意味着有未调查到的
  第四个 surface 实现）→ 停下报告，先补查证再动。
- 发现 owner 在实施期间对同一文件做了并行改动导致 edit_file 锚点失配 → 停下比对
  归属，绝不整文件覆盖。
- 任一测试失败且根因不在本计划删除范围（既有失败）→ 按 owner 既有裁决：定位根因
  修掉并在此计划补记；若属方案取舍则停下问。

---

## Implementation Status

**全部落地**（2026-10-07 实施完成，改动全部留在工作区未 commit）。步骤 1-10 全部
按计划完成。

### 实际改动清单

- **pkg/turn**：`slash.go`（删 /diff、/help 注册行；defaultCategory 去掉 "diff"；
  `removedSlashCommandNames` 追加 "diff"/"help"；删 `ExecuteDiffSlash` 与 tool import）；
  `executor.go`（删 `case "diff"`、`case "help"`、`execDiff`；CommandCatalog 注释改为
  "the bare \"/\" answer"）；`model.go`（删 `DiffSlashHandler` 接口与 `Context.Diff` 字段）。
- **pkg/tui**：`run.go`（`case "", "help":` → `case "":`；删 `case "diff"` 拦截）；
  `chat_session.go` 删 `Diff: s,`；`chat_slash.go` 删 `HandleDiffSlash`；
  `commands.go` 删 `handleDiff` + `diffFrameTitle`；`notify.go` 删接口方法；
  `setup.go` 注释指向 `/`。
- **pkg/gateway**：`slash_handlers.go` 删 `Server.HandleDiffSlash`；`server.go` 删 `Diff: s,`。
- **pkg/tool**：`workspace.go` 重写为只含 `attachTurnDiff`（删 `ProjectDiff`/
  `ErrNotGitRepository`/`runGit`，imports 只剩 event）。
- **测试**：`slash_test.go`（删 diff 断言/fake/装配/hint 用例/`TestExecuteDiffSlashSaysWhyThereIsNoDiff`；
  `TestHelpCatalogAndUnknownCommandSuggestions` 改名
  `TestUnknownCommandSuggestionsAndBareSlashCatalog`，裸 `/` 断言目录 + `/help` 断言
  "There is no /help" 前缀；subagent-view 表删两行；shown 列表删两词；删 os/os/exec/
  path/filepath imports）；`input_parts_test.go` removed 列表追加 diff/help；
  `run_test.go` 删 `diffReply` 字段与 fake 方法；`commands_test.go` 删
  `TestHandleDiffDrawsADiffOrItsReason`；`chat_session_test.go` 删
  `TestHandleDiffSlashShowsGitDiff`（保留 `runInDir`，另有 5 处使用）；
  `slash_handlers_test.go` 删 `TestHandleDiffSlashShowsGitDiff` 与孤儿
  `runInDirGateway`（唯一使用者），删 `os/exec` import；`api_extra_test.go` 删
  `names["diff"]` 断言行。
- **README.md**：删 `/diff`、`/help` 两行。

### 与计划的偏差

1. **`pkg/tool/workspace_test.go` 整文件删除是计划误判**：该文件除三个 ProjectDiff
   测试外还承载包级共享测试辅助 `WriteFullFileForRoots`（被 `file_tools_test.go`
   使用；注释明说 "production functions that only the tests in this package ever
   called"）。删除后 vet 报 undefined。修正：三个 ProjectDiff 测试与其专属 helper
   （gitIn/writeFile，仅内部使用）随删；`WriteFullFileForRoots` 移入新文件
   `pkg/tool/helpers_test.go`（内容取自 git HEAD，owner 未提交改动清单不含此文件）。
2. **graph.json 出现 5 行 LOC diff 而非"无变化"**：fan_in/fan_out（import 边界）完全
   无变化——计划真正要保证的包结构不变成立。LOC 变化中 gateway −12/tool −78/turn −47
   是本次删除；llm +75/tui +65 来自 owner 并行未提交改动（graph.json 基线 stale）。
   已 `git checkout` 恢复 graph.json，避免把混合归因的重新生成结果留在工作区。
3. 真机发现既有交互（非本次引入）：TUI 输入 `/help` 时 slash overlay 子串补全把
   `/caveman-help`（skill 名含 "help"）列为高亮建议，直接 Enter 会执行该 skill；
   Esc 关闭 overlay 后提交原文才走 unknown-reply 路径。`/diff` 无此歧义。这是 overlay
   对所有命令的既有补全行为，未改动。

### 验证

- `CGO_ENABLED=1 go build ./...` 全绿；`go vet ./...` 全绿。
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/tui ./pkg/gateway ./pkg/tool -count=1`
  四包全 ok。
- `scripts/package-graph.sh`：fan_in/fan_out 无任何变化（见偏差 2）。
- Done criteria 机器核对：slash.go 仅剩 removed 名单两行；全仓 grep 八个符号零输出；
  README 零输出。
- **tmux 真机（driver.sh，fts5 构建，fake provider）**：
  - `/diff` → 系统帧 "There is no /diff; type / to see every command."；
  - `/help`（Esc 关 overlay 后提交）→ "There is no /help; did you mean /caveman-help?"；
  - 裸 `/` → 命令目录帧正常渲染，`/` overlay 面板 Commands 区逐屏翻看无 diff/help；
  - **gateway 真机**（临时 FOREBRAIN_HOME + token，手写 WS 客户端走 `/ws/chat` 的
    `start_run`）：`/api/slash/commands?surface=webchat` 43 项无 diff/help；会话提交
    `/diff` → slash_reply "There is no /diff; type / to see every command."；
    `/help` → "There is no /help; did you mean /caveman-help?"——与 TUI 逐字 parity。
  - 场景 4（skill 占名被拒）由 `TestReplaceDynamicSourceRejectsRemovedSlashCommandNames`
    （已扩展 diff/help）覆盖，按计划省真机。
- 验收后已清理：gateway 进程已停、tmux session 已 kill、临时 home 已 reset、
  /tmp/wsprobe.py 已删。
