# 修复：subagent 写操作不触发 gopls LSP 推荐

> 实施于 2026-10-05。验收记录见文末。

## 目标（Why this matters）

owner 用最新二进制（npm/dist，10-04 23:16 构建，含任务 13 推荐代码）在 forebrain-harness
项目跑真实 AI coding 任务，所有 `.go` 写操作由 subagent 完成，gopls 已安装
（`~/go/bin/gopls` v0.23.0，detect.json `installed:true`），但编辑多份 `.go` 文件后
**从未弹出 LSP 推荐模态**。owner 裁决（原话）："subagent 做的写操作也必须触发 lsp 推荐"。

LSP 推荐（spec §10）是任务 13 已实现的能力；本 bug 是它在实际工作流里永不触发。

## 根因（file:line 证据链 + 现场证据）

**根因：subagent/fork 编辑被 considerRecommendation 刻意排除（spec §10.1 条 4）。**

- `pkg/tool/file_tools.go:423`：`reportEditDiagnostics` 对每次编辑调
  `ci.DidWrite(ctx, llm.AgentSessionIDFromContext(ctx), changes)`（主代理与 subagent 都走）。
- `pkg/lsp/manager.go:356-362`：`DidWrite` 过 `gated()` 后调 `considerRecommendation(ctx, changes)`。
- `pkg/lsp/manager.go:476-481`（修复前证据原文）：
  ```go
  // Subagents and fork children share the parent conversation's surface;
  // the recommendation is the user's to answer, and the parent edit that
  // spawned them is the one that triggers it.
  if tool.IsForkChildFromContext(ctx) || tool.SubagentTypeFromContext(ctx) != "" {
      return
  }
  ```
  设计假设"主代理编辑是常态、父编辑才触发"。实际工作流（owner 裁决"可以并行开发的必须要
  调用并行 subagent"）里 `.go` 写操作**全部**由 subagent 完成 → 推荐永不触发。这是唯一根因：
  主代理路径无此问题（见下）。

**已排除的嫌疑（记录防误诊）：TUI 主对话 ctx 无显式 `WithConversationSessionID` 不是断点。**

`pkg/tool/state.go:249-256`：`ConversationSessionIDFromContext` 显式 key 缺失时**回退**返回
`llm.AgentSessionIDFromContext(ctx)`；TUI 主路径经 `pkg/process/session_context.go:36`
恒注入 AgentSessionID（空则 "default"），ctx values 一路保留到工具执行
（`pkg/tui/chat_turn.go:393-397` 在 agBase 上叠加派生）→ TUI 主代理编辑的 sid 恒非空，
**旧行为就是能触发推荐**。因此不在 TUI 补显式注入（owner 裁决"改动 2 删除"）。
注意对照差异：TUI resume 路径 `chat_turn.go:687-688` 的显式注入是**必要的**，因为
`resumeSessionID`（`chat_turn.go:672-679`）对 subagent resume 返回 worker 会话 id，
彼处 AgentSessionID 与 conversation id 真的分歧；主分发路径无此分歧，勿混为一谈。

**发布链与 UI 链完好（已核实，不动）**：

- subagent ctx 携带正确的 ConversationSessionID（=父会话 id）：`pkg/run/subagent.go:663/748/804/1490`；
  subagent 工具事件都记在父会话下（`subagent.go:702` TurnRequest.SessionID 同值）。
- listener 安装：`pkg/process/open.go:329`（主 Runner）、`pkg/process/runner_pool.go:745`（gateway 项目 Runner）。
- 发布：`pkg/process/open.go:454-468` → `RunEvent{Type: "lsp_recommendation", SessionID: ConversationSessionIDFromContext(ctx)}`。
- TUI 模态：`pkg/tui/notify.go:866-870` `RunEventLSPRecommendation → notifyUI(LSPRecommendationMsg)`，
  该 case 不过滤 SessionID，subagent 事件可达父会话界面。
- 并发去重：`recommendAsync` 锁内 re-check（`manager.go:588-604`）保证同会话并发编辑只发一条；
  `m.recommended[sid]` 按父会话去重，父子同会话只推荐一次。

**现场证据（2026-10-05 00:00-00:15 取证）**：

- `fb_session_events` 近 2 天 **零** `lsp_recommendation` 事件；正在跑的任务会话
  `cli-8dac8616` 00:08:05 / 00:10:17 有 `edit_file pkg/tui/commands_test.go`（.go，subagent
  所为）却无推荐。
- `~/.forebrain/forebrain.yaml` 无 `lsp:` 段（默认 Recommendations=true）、
  `~/.forebrain/workspace/state/lsp/` 无 enabled.json / recommendations.json（无 never/disabled
  阻塞）、detect.json gopls `installed:true`。
- 项目在 workspace_trust.json；二进制含 `lsp_recommendation` 字符串（新代码）。
- detect.json mtime 10-04 23:41（用户开 /lsp 面板触发 snapshot 探测），与推荐链无关。

## 修复设计

### 改动 1：subagent/fork 编辑同样触发推荐（契约修订）

`pkg/lsp/manager.go` `considerRecommendation`（原 476-481）：删除 subagent/fork 排除门及其注释，
注释改为说明"任何会话内编辑（主代理、类型化子代理、fork 子会话）都触发；推荐挂在
conversation session 上由用户回答，按 conversation session 去重"。
理由：subagent ctx 已带父会话 id，模态出现在用户面前由用户回答——subagent 编辑触发与
主代理编辑触发语义等价；owner 裁决明确要求。

### 改动 2：spec 与任务计划文档同步（规范↔实现一致）

- `docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md` §10.1 条 4/5：改为"编辑即触发（主代理、类型化
  子代理、fork 子会话相同），推荐按 conversation session 去重"。
- `docs/plan/lsp/13-recommendation.md` 步骤 2 条件列表与步骤 4 监听器段同步修订。

### 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 备注 |
|---|---|---|---|
| TUI subagent 编辑 `.go`（gopls 未启用） | 无推荐（gate 排除） | 触发；模态弹在父会话；发布事件 RunID=子 run id、SessionID=父会话 id | **本 bug 主体** |
| TUI 主代理编辑 `.go`（gopls 未启用） | 能触发（sid 经 AgentSessionID 回退恒非空） | 不变 | 无需改动；验收中作为回归确认 |
| gateway 主代理/项目 runner 编辑 | 正常推荐 | 不变 | 回归保护 |
| 同会话第二次及以后编辑（父或其它 subagent） | 已推荐过不重复 | 不变（按 conversation sid 去重） | |
| 首次编辑（探测未完成） | 本次不推，下次再判 | 不变（spec §10.1 探测语义） | |
| untrusted / recommendations off / disabled / never / already enabled | 不推荐 | 不变 | TestRecommendSkips 其余 5 case |
| 推荐后 enable → 后续编辑诊断块 / late reminder | 既有实现 | 不变，subagent 编辑自然受益 | |

刻意修订项：仅第一行（owner 裁决）；其余行为保持。

## Scope

**In scope**：
- `pkg/lsp/manager.go`（considerRecommendation 删 gate + 注释）
- `pkg/lsp/control_test.go`（TestRecommendSkips 契约更新 + subagent/fork 触发、去重、
  AgentSessionID 回退契约的用例）
- `docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`、`docs/plan/lsp/13-recommendation.md`
- `pkg/architecture/testdata/graph.json` 重生成（pkg/ 生产代码行数变化必脏）

**Out of scope（明确不碰）**：
- `pkg/tui/chat_session.go`（owner 裁决删除原改动 2：显式注入与回退值相同，是 no-op；
  TUI 主路径经回退已能触发）
- `pkg/lsp` 其它文件（DidWrite 诊断分支、detect TTL 双窗口语义、install/prestart）
- gateway 三处注入、subagent.go 四处注入（已正确）
- resume 路径注入（必要，语义不同，勿"归一"）
- `pkg/process`（发布链已正确）
- "5 次 not_now 自动 disable"、never/disabled 持久化、recommendations reset（既有语义不动）
- enabled/prewarm/late-diagnostics 语义

## Done criteria（机器可查）— 全部达成

1. `grep -n 'SubagentTypeFromContext' pkg/lsp/manager.go` 无输出（gate 已删）。✅
2. `grep -n 'WithConversationSessionID' pkg/tui/chat_session.go` 无输出（确认未做 no-op 注入）。✅
3. `CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -run 'Recommend' -count=1` 通过，
   含 subagent/fork 触发、去重、AgentSessionID 回退三类用例。✅
4. `CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp ./pkg/tui ./pkg/run ./pkg/tool -count=1` 全绿。✅
5. `go vet ./...` 退出码 0；`gofmt -l pkg cmd` 无输出；graph.json 已重生成。✅
6. tmux 真机验收通过（下节）。✅

## tmux 真机验收 — 已完成（2026-10-05）

方法：`go build -tags fts5` 含修复的二进制；隔离 `FOREBRAIN_HOME=$(mktemp -d)`；
临时 Go 项目（go.mod + git init——**trust 要求 VersionControlled，无 .git 会整链被
`TrustedRoot` 拦下，/lsp 面板显示 "not trusted"**，这是验收环境陷阱不是产品缺陷）；
PATH 含 `~/go/bin`（gopls v0.23.0）；fake provider tool 模式编排工具调用
（subagent_run[general-purpose] → write_file b1.go → b2.go → b3/b4.go 故意写错）。

1. **subagent 编辑触发（主体）** ✅：模态在 subagent 仍在跑（"write b2.go"）时弹出：
   `LSP recommendation / Server: gopls (Go) / Found: ~/go/bin/gopls (golang.org/x/tools/gopls v0.23.0)
   / Triggered by: .go files`，默认项 `Yes, enable`。
2. **主代理编辑触发（回归确认）** ✅：主对话直接编辑 `.go` → 同一模态照弹（删 gate 后主路径不回归）。
3. **enable → 诊断** ✅：Enter → transcript `● lsp / gopls enabled for Go. Diagnostics start
   with the next edit.`；下一回合故意写错 → `Found 1 new diagnostic issue in 1 file /
   error 4:10 undefined: stillUndefined [compiler UndeclaredName]`。
4. **去重** ✅：同会话后续编辑（父与 subagent 混合）不再弹；两个场景各自恰好 1 条
   `lsp_recommendation` 事件。
5. **中断自救** ✅：模态出现时 Esc → transcript `Not now. No language server will be suggested
   again in this session.`，agent 不受影响继续（第 3 次写完成，回合正常收尾 "done"）。
6. **不信任目录** ✅（结构性）：TUI 信任页只有 Quit / Trust and continue，Esc 即退出（EXIT=0），
   不存在"运行中的 untrusted 会话"；untrusted 门由 `TestRecommendSkips/untrusted` 钉住（绿）。

**事件身份（DB 证据）**：`fb_session_events` 的 `lsp_recommendation` 行
`session_id=cli-b17e7b59…（父会话）、run_id=8c132ec2…`；`fb_runs` 证实 8c132ec2 的
`parent_run_id=5bfb118b…`（主 run）——RunID=子 run id、SessionID=父会话 id 契约与计划一致。

## Maintenance notes

- 推荐去重键 = conversation session id（父会话）；`Manager.recommended` 是内存态，进程
  重启后同会话会重新推荐一次（既有语义，spec §10.1 条 5，本次不改）。
- `ConversationSessionIDFromContext` 的 AgentSessionID 回退是 TUI 主路径的契约
  （`pkg/tool/state.go:249-256`），已有钉子测试（`TestRecommendFallsBackToAgentSessionID`）
  防回归；resume 路径的显式注入另有所指（worker 会话 id 分歧），两者并存是刻意设计。
- 验收环境备忘：临时项目必须 `git init`（trust 的 VersionControlled 前置）；fake provider
  tool 模式下 write_file 覆盖已有文件会撞"must read_file before overwriting"，编排时写**新**文件。
- 改动全部留工作区，不 commit（owner 手动审阅提交）。
