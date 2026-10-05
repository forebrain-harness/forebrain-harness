# 任务 13：推荐（引擎、终端模态框、Web 卡片、`forebrain lsp recommendations reset`）

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范 §5.4（`recommendations.json`）、§6.3、§6.5、§10（全部）、§15 第 2、4、8 条。
>
> **前置**：任务 11、12、14 已合入。先确认：`grep -n "errNotAvailable" pkg/lsp/control.go` 只剩 `DecideRecommendation`、`ResetRecommendations` 两处；`grep -n "func (s \*Server) handleLSPSnapshot" pkg/gateway/api_extra.go` 有输出（任务 14 的 `GET /api/v1/lsp`）；`grep -n "lspSnapshot" frontend/src/lib/api.ts` 有输出。否则 STOP。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/tui/notify.go pkg/tui/run.go pkg/tui/chat_session.go pkg/process/open.go pkg/process/runner_pool.go pkg/gateway/api_extra.go frontend/src/composables/useChatStream.ts frontend/src/views/ChatView.vue`。任务 03、11、14 改过其中一些文件；对比「现状」摘录。

## 状态

- 优先级：P2 · 工作量：L · 风险：MED（界面在回合进行中弹出模态框；可能以用户身份执行安装命令）
- 依赖：11（触发点在 `DidWrite`）、12（`Install`）、14（Web 的快照接口与 API 客户端）
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

这是用户截图里的功能：agent 第一次编辑某种语言的文件、而该语言的语言服务器还没启用时，问用户要不要启用（或安装并启用）。目录服务器默认不启用（规范 §15 第 1 条），推荐是让大多数用户一键获得 LSP 能力的入口。

## 现状

- **发布 RunEvent**：`run.Runner.Events event.Sink`（`pkg/run/runner.go:124`）由界面在 Runner 建好后挂上：终端 `pkg/tui/notify.go:1212 runner.Events = event.SinkFunc(s.publishRunEvent)`，gateway `pkg/gateway/serve_run.go:102 runner.Events = gw.RunEvents()`；项目 Runner 在 `pkg/process/runner_pool.go:700` 复制 `Events: env.Runner.Events`。`event.NewRunEvent(id, runID, sessionID, type, payload, createdAt)`（`pkg/event/run_events.go`）。
- **上下文里的身份**：`tool.RunIDFromContext`（`pkg/tool/state.go:230`）、`tool.ConversationSessionIDFromContext`（`:249`，用户可见的会话）、`tool.IsForkChildFromContext`（`:443`）、`tool.SubagentTypeFromContext`（`:458`）。
- **为什么监听器不装在 `pkg/run`**：子代理 Runner（`pkg/run/factory.go` 的 `NewIsolatedRunner`）继承父 Runner 的 `CodeIntelControl`（任务 03），如果在 `loadLocked` 里安装监听器，子 Runner 的 Load 会用自己的（通常为空的）`Events` 覆盖父 Runner 的监听器。所以监听器在组合根 `pkg/process` 安装，只对主 Runner 与项目 Runner 各装一次，并在发布时读取 `runner.Events`（与 `publishSurfaceEvent`（`runner.go:1472`）一样在发布时读取）。
- **终端**：
  - `ChatSession.publishRunEvent`（`pkg/tui/notify.go:688`）先把事件持久化到会话日志（`AppendSessionEventOnce`，已存在则直接返回——所以重放和重连不会重复处理），再按 `evt.Type` 分发；auto-continue 的分支（:837）是 `s.notifyUI(msg)` 的范例。
  - `processUINotification`（`pkg/tui/run.go:241`）在主循环上处理通知；`MigrationPreviewMsg`（:448）在这里弹模态确认框。模态选择期间用 `state.deferQueueAutosendUntilSelectionApplied()` / `defer state.resumeQueueAutosend()` 阻止排队消息被发送（:411-415 的写法）。
  - `Selector.Review(label string, facts []turn.StatusFact, actions []string, defaultIdx int) (int, bool, error)`（`pkg/tui/channels.go:1416`）：`label` 首行是标题，其后的行是副标题（`pkg/tui/overlays.go:4222` 的注释）；返回所选动作下标，`ok == false` 表示取消。
  - 面板用的会话方法都在 `pkg/tui/chat_session.go`（`:2422 PanelStatusReport` 起），经 `s.runner()`（:479）拿 Runner。
  - 重放（`pkg/tui/commands.go` 的 `replaySubagentRunEventMessage` 等）对未知事件类型返回 `nil, false`，不会弹框。
- **Web**：
  - `applyObservedEvent(evt, historical)`（`frontend/src/composables/useChatStream.ts:1590`）与请求 socket 上的分发（:2921）都先处理 `AUTO_CONTINUE_EVENT_TYPES`（来自 `frontend/src/lib/autoContinue.ts`）；`applyAutoContinueEvent`（:1562）忽略 `historical` 与 `sequence <= observerHighWater` 的事件——推荐照此处理。
  - `ChatView.vue:505` 渲染 `<AutoContinueBanner :state="autoContinue" …/>`，`autoContinue` 由 `useChatStream` 返回（:1014 附近）。
  - 文案在 `frontend/src/locales/index.ts`（中文约 :95 起，英文约 :652 起，两套键必须一致）。
  - gateway 的会话级 Runner 解析：`s.runnerFor(r.Context(), sessionID)`（范例 `pkg/gateway/mcp_oauth_api.go:505`）。
- 结构约束：`tui`、`gateway`、`process` 已满 20 个生产文件，只能改已有文件；`pkg/lsp` 不新建文件。

## 范围

**修改：**

- `pkg/lsp/control.go`、`control_test.go`、`manager.go`、`manager_test.go`
- `pkg/process/open.go`、`open_test.go`、`runner_pool.go`、`runner_pool_test.go`
- `pkg/tui/notify.go`、`notify_test.go`、`run.go`、`run_test.go`、`chat_session.go`、`chat_session_test.go`
- `pkg/gateway/api_extra.go`、`api_extra_test.go`
- `cmd/forebrain/lsp.go`、`lsp_test.go`
- `frontend/src/composables/useChatStream.ts`、`useChatStream.test.ts`、`frontend/src/views/ChatView.vue`、`frontend/src/lib/api.ts`、`frontend/src/locales/index.ts`
- `pkg/architecture/testdata/graph.json`

**新建：** `frontend/src/lib/lspRecommendation.ts`、`lspRecommendation.test.ts`、`frontend/src/components/chat/LspRecommendationCard.vue`、`LspRecommendationCard.test.ts`。

**不要碰：** `pkg/tool`、`pkg/run`、系统提示、`pkg/gateway/dist`。推荐文本**不进入模型上下文**。

## 步骤

### 步骤 1：推荐状态文件（`control.go`）

```go
// recState is <StateDir>/recommendations.json (spec §5.4).
type recState struct {
	Disabled        bool     `json:"disabled"`
	DisabledReason  string   `json:"disabled_reason"`
	DismissedStreak int      `json:"dismissed_streak"`
	Never           []string `json:"never"`
}

// recDismissLimit turns recommendations off after this many "not now" in a row (spec §10.2).
const recDismissLimit = 5

func loadRecState(agentWorkspace string) recState          // missing or unreadable file: zero value
func saveRecState(agentWorkspace string, st recState) error // temp file + rename; Never sorted, deduplicated

// ResetRecommendationState turns recommendations back on and forgets every
// "never" (spec §10.2); forebrain lsp recommendations reset calls it.
func ResetRecommendationState(agentWorkspace string) error
```

`Snapshot()` 读取它填 `RecommendationsDisabled`、`RecommendationsDisabledReason`（任务 08 保持 false 的两个字段）。

### 步骤 2：触发（`manager.go` + `control.go`）

`Manager` 增加字段（受 `m.mu` 保护）：`recommended map[string]bool`（键：会话 id）、`pendingRecs map[string]pendingRec`，其中

```go
type pendingRec struct {
	rec         event.LSPRecommendation
	sessionID   string
	triggerFile string // absolute path of the edited file that triggered it
}
```

在任务 08 `DidWrite` 步骤 1 的 `gated()` 判断**之后**、`eff.AfterEdit` 判断**之前**加一行 `m.considerRecommendation(ctx, changes)`（推荐与「编辑后诊断」开关无关）。`considerRecommendation` 必须**立即返回**，判断全部在内存里完成，任何需要 I/O 的部分放进 goroutine：

1. 条件（规范 §10.1，逐条）：`EffectiveLSP().Recommendations`；`!loadRecState(ws).Disabled`（这一步读文件，所以整个 `considerRecommendation` 的剩余部分放进 `go func()`，传入 `context.WithoutCancel(ctx)`）；`m.opts.Trusted`；`sid := tool.ConversationSessionIDFromContext(ctx)` 非空且 `!m.recommended[sid]`；`m.listener != nil`。编辑即触发：主代理、类型化子代理、fork 子会话相同（2026-10-05 修订，owner 裁决；子代理/fork 的 ctx 携带父会话 id，推荐按 conversation session 去重）。
2. 候选：对每个 `After != nil` 的 change（按传入顺序），`primary, diags := ServersForFile(servers, abs, root)`；`primary == nil && len(diags) == 0` 时，`cands := MatchFile(servers, abs)` 过滤出 `InCatalog && !Enabled && Invalid == "" && Role == "primary"` 且 id 不在 `Never` 中的条目；在候选中按 `priority` 大、id 字典序小选一个（与规范 §6.3 的后两条相同；前两条对未启用的目录条目不适用）。第一个有候选的文件即为触发文件。
3. 探测：取 `m.detected[id]`；没有或超过 10 分钟 → 启动一次后台探测（与 `Snapshot` 用的同一个刷新函数），**本次不推荐**，直接返回（规范 §10.1：探测未完成时本次不推荐）。
4. 模式：`Installed` → `"enable"`，`BinaryPath`、`Version` 取探测结果；否则 `UsableInstallRecipe(srv, os.Environ(), runtime.GOOS) != nil` → `"install"`，`InstallCommand` 为 argv 用空格连接；都不满足 → 返回。
5. 发布：`id := "lsprec-" + 8 个随机十六进制字符`；`rec := event.LSPRecommendation{ID: id, ServerID, DisplayName, Languages, TriggerExtension: 触发文件的小写扩展名（没有扩展名时用文件名）, Mode, …}`；在锁内再次检查并设置 `m.recommended[sid] = true`（并发的两次编辑只推荐一次）、记录 `m.pendingRecs[id]`；解锁后调用 `listener(ctx, rec)`。

### 步骤 3：决定（`control.go`）

`DecideRecommendation(recID, choice)`：`!choice.Valid()` → `fmt.Errorf("invalid choice %q", choice)`；`recID` 不在 `pendingRecs` → `tool.ErrUnknownLSPRecommendation`（任务 02 在 port 旁定义的哨兵错误，gateway 能导入 `tool` 而不能导入 `lsp`）；从 `pendingRecs` 删除；然后按规范 §10.2：

| choice | 动作 |
|---|---|
| `enable` | `SetEnabled(id, true)`；`DismissedStreak = 0`；后台预启动：`root, ok := ResolveRoot(triggerFile, ProjectRoot, srv)`，`ok` 时 `go` 调任务 08 的 `acquire`（`context.Background()` + `StartupTimeout`） |
| `install` | 立即返回 nil；后台 `go`：`m.Install(context.Background(), id, nil)`（任务 12，安装进度与结果进入快照）；成功后同 `enable` |
| `not_now` | `DismissedStreak++`；达到 `recDismissLimit` 时 `Disabled = true`、`DisabledReason = "dismissed 5 times in a row"` |
| `never` | `Never` 加入 id；`DismissedStreak = 0` |
| `disable_all` | `Disabled = true`、`DisabledReason = "turned off by the user"` |

每种都 `saveRecState` 并通知订阅者。`ResetRecommendations()` 调 `ResetRecommendationState(ws)` 并通知订阅者。二者替换骨架的 `errNotAvailable`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp -run 'Recommend' -count=1 -v` → PASS。

### 步骤 4：监听器（`pkg/process`）

在 `open.go` 创建主 Runner 的 Manager 之后、`runner.Load()` 之前，以及 `runner_pool.go` 的 `buildEntryLocked` 创建 `entry.lsp` 之后，调用同一个未导出函数（放在 `open.go`）：

```go
// publishLSPRecommendations hands recommendations from mgr to whichever
// surface is attached to runner when one is made. The sink is read at publish
// time: surfaces attach it after the runner is built.
func publishLSPRecommendations(mgr *lsp.Manager, runner *run.Runner) {
	if mgr == nil || runner == nil {
		return
	}
	mgr.SetRecommendationListener(func(ctx context.Context, rec event.LSPRecommendation) {
		sink := runner.Events
		if sink == nil {
			return
		}
		_ = sink.Publish(context.WithoutCancel(ctx), event.NewRunEvent(
			rec.ID, tool.RunIDFromContext(ctx), tool.ConversationSessionIDFromContext(ctx),
			event.RunEventLSPRecommendation, rec, time.Now()))
	})
}
```

监听器只对主 Runner 与项目 Runner 各装一次；子代理与 fork 共用父 Runner 的 Manager，它们的编辑触发的推荐同样发往父 Runner 的界面（2026-10-05 修订：subagent/fork 的写操作也触发推荐，与主代理编辑等价）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1` → `ok`。

### 步骤 5：终端

1. `pkg/tui/notify.go`：
   - 在 `MigrationPreviewMsg` 附近加：

     ```go
     // LSPRecommendationMsg asks the user whether to enable (or install and
     // enable) a language server. It is shown as a modal; the answer goes back
     // through ChatSession.DecideLSPRecommendation.
     type LSPRecommendationMsg struct {
     	Rec event.LSPRecommendation
     }

     // LSPInstallProgressMsg is one output line of an install the user started
     // from a recommendation. It renders as the transient status line.
     type LSPInstallProgressMsg struct {
     	ServerID string
     	Line     string
     }

     // LSPInstallDoneMsg ends that install.
     type LSPInstallDoneMsg struct {
     	ServerID string
     	Text     string // the transcript line on success
     	Err      string // the failure text, including the output tail
     }
     ```

   - `publishRunEvent` 的 `switch evt.Type` 加 `case event.RunEventLSPRecommendation:` 解码 `event.LSPRecommendation`，`ID` 非空时 `s.notifyUI(LSPRecommendationMsg{Rec: p})`。（持久化在前面的通用代码里已完成；重放不经过这里。）
2. `pkg/tui/chat_session.go`（与 `Panel*` 方法放在一起）：

   ```go
   // DecideLSPRecommendation applies the user's answer and returns the line
   // the transcript shows for it ("" when there is nothing to say).
   func (s *ChatSession) DecideLSPRecommendation(rec event.LSPRecommendation, choice event.LSPRecommendationChoice) (string, error)
   ```

   - `ctl := s.runner().CodeIntelControl`，nil → `errPanelUnavailable`。
   - `choice == install`：**先** `ctl.Subscribe` 观察该服务器：`Installing` 且 `InstallLog` 末行变化 → `s.notifyUI(LSPInstallProgressMsg{…})`；从 `Installing == true` 变为 false → `InstallError != ""` 时 `s.notifyUI(LSPInstallDoneMsg{ServerID, Err: InstallError})`，否则 `LSPInstallDoneMsg{ServerID, Text: "<DisplayName> installed and enabled for <languages>. Diagnostics start with the next edit."}`，然后取消订阅。再调 `ctl.DecideRecommendation`。
   - 其余 choice：调 `ctl.DecideRecommendation`，然后按下表返回文本（`<languages>` 为 `Languages` 用 `, ` 连接）：

     | choice | 返回的 transcript 行 |
     |---|---|
     | `enable` | `<DisplayName> enabled for <languages>. Diagnostics start with the next edit.` |
     | `install` | `Installing <DisplayName>: <InstallCommand>` |
     | `not_now` | 决定后 `ctl.Snapshot().RecommendationsDisabled` 为 true → `Language server recommendations are now off (dismissed 5 times in a row). Turn them back on in /lsp.`；否则 `Not now. No language server will be suggested again in this session.` |
     | `never` | `<ServerID> will not be suggested again. /lsp can still enable it.` |
     | `disable_all` | `Language server recommendations are off. Turn them back on in /lsp.` |

3. `pkg/tui/run.go` 的 `processUINotification`，在 migration 的 `switch` 之后加：

   ```go
   // A language-server recommendation is a modal like the migration preview:
   // it can open mid-turn, the agent keeps working, and queued input waits
   // until the answer is applied.
   switch lspMsg := m.(type) {
   case LSPRecommendationMsg:
   	…
   case LSPInstallProgressMsg:
   	renderer.RenderTransientStatus(transientSourceLSP, "lsp: installing "+lspMsg.ServerID+" · "+lspMsg.Line)
   case LSPInstallDoneMsg:
   	renderer.FinishTransientStatus(transientSourceLSP)
   	…FrameSystem（Text）或 FrameError（Title: "<ServerID> install failed", Content: Err）…
   }
   ```

   `transientSourceLSP` 与已有的 `transientSourceMigrate` 定义在同一处，值为 `"lsp"`。`LSPRecommendationMsg` 的处理：
   - `sess, ok := state.session.(interface{ DecideLSPRecommendation(event.LSPRecommendation, event.LSPRecommendationChoice) (string, error) })`；不满足则忽略。
   - `state.deferQueueAutosendUntilSelectionApplied()`；`defer state.resumeQueueAutosend()`。
   - label：`"LSP recommendation\nA language server gives the agent diagnostics after its edits and lets it find definitions and references by symbol. Enable this language server?"`。
   - facts（`turn.StatusFact{Label, Value}`，逐字）：`Server` = `<DisplayName> (<languages>)`；`Found` = enable 模式 `<BinaryPath 中家目录前缀换成 ~>`，有版本时加 ` (<Version>)`；install 模式 `Not installed. Install with: <InstallCommand>`；`Triggered by` = `<TriggerExtension> files`；`Runs` = `in this trusted project, outside the sandbox`。
   - actions（逐字）：enable 模式 `["Yes, enable", "No, not now", "Never for <ServerID>", "Disable all LSP recommendations"]`，默认下标 0；install 模式第一项为 `"Yes, install and enable"`，**默认下标 1**（执行安装命令不能只差一次误按回车）。
   - 下标映射：0 → `enable` 或 `install`；1 → `not_now`；2 → `never`；3 → `disable_all`；`ok == false`（Esc）→ `not_now`；`err != nil` → `renderer.PrintError(err)` 并按 `not_now` 处理。
   - 结果：`text, err := sess.DecideLSPRecommendation(...)`；`err` → `renderer.PrintError(err)`；`text != ""` → `renderer.RenderFrame(Frame{Kind: FrameSystem, Title: "lsp", Content: text, Final: true})`。**不**写入会话、不进入模型上下文。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`。

### 步骤 6：gateway（`api_extra.go`）

在任务 14 加的 `lsp := api.Group("/v1/lsp")` 下加：

```go
lsp.Post("/recommendations/:id/decision", s.handleLSPRecommendationDecision)
```

请求体 `{"choice": "enable", "session_id": "…"}`：

- 非 POST → 405；解码失败或 `choice` 无效 → 400 `invalid choice`。
- Runner 解析同 `handleMCPServerToggle`（`session_id` → `s.runnerFor`，否则 `s.Runner`）；`runner.CodeIntelControl == nil` → 503 `language servers are not available`。
- `errors.Is(err, tool.ErrUnknownLSPRecommendation)` → 404（gateway 已导入 `pkg/tool`）；其他错误 → 409 + 错误文本。
- 成功 → `{"ok": true}`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run LSP -count=1` → `ok`。

### 步骤 7：Web

1. `frontend/src/lib/lspRecommendation.ts`（照 `autoContinue.ts` 的结构）：

   ```ts
   export interface LspRecommendation {
     id: string
     serverId: string
     displayName: string
     languages: string[]
     triggerExtension: string
     mode: 'enable' | 'install'
     binaryPath?: string
     version?: string
     installCommand?: string
   }
   export type LspRecommendationChoice = 'enable' | 'install' | 'not_now' | 'never' | 'disable_all'
   export const LSP_RECOMMENDATION_EVENT_TYPE = 'lsp_recommendation'
   /** parseLspRecommendation accepts snake_case or camelCase keys; null when id/serverId/mode are missing or mode is unknown. */
   export function parseLspRecommendation(raw: unknown): LspRecommendation | null
   ```

2. `frontend/src/lib/api.ts`：`decideLspRecommendation(id: string, choice: LspRecommendationChoice, sessionId?: string)` → `POST /v1/lsp/recommendations/${encodeURIComponent(id)}/decision`，体 `{ choice, session_id }`（照 `mcpSetServerDisabled`）。
3. `useChatStream.ts`：新增 `const lspRecommendation = ref<LspRecommendation | null>(null)` 并从 composable 返回；在两个分发点（`applyObservedEvent` 开头的 auto-continue 分支旁，以及请求 socket 上 :2921 的分支旁）加：`type === LSP_RECOMMENDATION_EVENT_TYPE` → `if (rememberObservedEvent(evt) && !historical && !(sequence > 0 && sequence <= observerHighWater)) lspRecommendation.value = parseLspRecommendation(evt.payload)`，然后 `return`。切换会话时清空。
4. `LspRecommendationCard.vue`：props `recommendation: LspRecommendation`、`sessionId`；显示标题、说明、四条事实（与终端相同含义，文案走 i18n）和按钮：主按钮（`enable` 或 `install` 模式对应文案）、「暂不」、「不再推荐 {id}」、「关闭全部推荐」、右上角关闭（= `not_now`）。点击后调用 `decideLspRecommendation`；成功后卡片变为一行结果文案（与终端表格同义的本地化文本）；`install` 模式成功提交后显示「正在安装…」，每 2 秒请求一次任务 14 的 `lspSnapshot(sessionId)` 直到该服务器 `installing` 为 false，再显示成功或 `install_error`（最多轮询 10 分钟）。
5. `ChatView.vue`：在 `AutoContinueBanner` 附近渲染 `<LspRecommendationCard v-if="lspRecommendation" …/>`，决定后把 `lspRecommendation` 置空（结果行由卡片自己在关闭前显示 5 秒）。
6. `locales/index.ts`：中英文各加一组 `lsp.recommendation.*` 键（标题、说明、四个事实标签、五个按钮、五种结果、安装中、安装失败）。英文按钮文案与终端完全相同。

**验证**：`cd frontend && pnpm install --frozen-lockfile && pnpm test && pnpm build` → 退出码 0；然后 `git checkout -- pkg/gateway/dist`。

### 步骤 8：`forebrain lsp recommendations reset`

`cmd/forebrain/lsp.go` 加子命令组 `recommendations`（`Args: cobra.NoArgs`，无子命令时报错 `forebrain lsp recommendations needs a subcommand: reset`）与 `reset`：调用 `lsp.ResetRecommendationState(ws)`，输出 `Language server recommendations are back on; servers marked "never" can be suggested again.`

### 步骤 9：全量检查

**验证**：`gofmt`、`go vet ./...` 干净；满额包文件数不变；`scripts/package-graph.sh` 后提交 `graph.json`；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → 全部 `ok`；`git status --short pkg/gateway/dist` 无输出。

## 测试计划

- `pkg/lsp/control_test.go`（推荐只针对目录条目，所以用任务 06 的测试替换点 `catalogOverride` 放入一个 `fake` 目录条目：`Command` 为假服务器路径、`ExtensionToLanguage: {".fk": "fake"}`、`Install` 为任务 12 测试里的假安装器）
  - `TestRecommendOncePerSession`：两个文件、两次 `DidWrite`（同一会话 id）→ 监听器只收到一次；换一个会话 id → 再收到一次。
  - `TestRecommendModes`：探测到已安装 → `mode == "enable"` 且有 `BinaryPath`；未安装但有可用配方 → `"install"`；都没有 → 不推荐。
  - `TestRecommendWaitsForDetection`：第一次 `DidWrite` 时没有探测结果 → 不推荐；探测完成后下一次 `DidWrite` → 推荐。
  - `TestRecommendSkips`：未受信任、fork 子会话（`tool.WithForkChild(ctx, true)`，`pkg/tool/state.go:436`）、类型化子代理（`tool.WithSubagentType(ctx, "explore")`，`:451`）、`recommendations: false`、`disabled: true`、`never` 包含该 id、已启用 → 都不推荐。
  - `TestRecommendDoesNotBlockDidWrite`：监听器阻塞 1 秒 → `DidWrite` 在 100ms 内返回。
  - `TestDecideRecommendation`：五种 choice 的持久化结果逐一断言（`enabled.json`、`recommendations.json`）；第五次 `not_now` 后 `Disabled == true` 且原因为 `dismissed 5 times in a row`；`enable` 后 `DismissedStreak == 0`。
  - `TestDecideUnknownRecommendation`：错误 `errors.Is(err, tool.ErrUnknownLSPRecommendation)`；同一 id 第二次决定也是它。
  - `TestDecideInstallRunsInBackground`：用任务 12 的假安装器，`DecideRecommendation(install)` 立即返回；稍后 `Snapshot` 显示已启用。
  - `TestResetRecommendations`：清空四个字段；`Snapshot().RecommendationsDisabled == false`。
- `pkg/process/open_test.go`：`TestLSPRecommendationPublishedToSurface`：用一个测试 `Manager` 替身不可行时，直接调用 `publishLSPRecommendations(mgr, runner)` 并让 `runner.Events` 为记录型 sink，触发监听器 → 收到一个 `Type == "lsp_recommendation"` 的事件，`SessionID`、`RunID` 取自 ctx；`runner.Events == nil` 时不 panic。
- `pkg/tui/notify_test.go`：`TestPublishRunEventLSPRecommendationNotifies`（发布一次 → 收到一个 `LSPRecommendationMsg`；同一事件 id 再发布 → 不再收到）。
- `pkg/tui/run_test.go`：用测试 selector 返回下标 0/1/2/3 与取消，断言传给 `DecideLSPRecommendation` 的 choice；install 模式默认下标为 1。
- `pkg/tui/chat_session_test.go`：`TestDecideLSPRecommendationTexts`（五种 choice 的返回文本逐字）；`TestDecideLSPRecommendationInstallReportsDone`（控制面替身在 `Subscribe` 回调里依次给出 installing → 完成的快照 → 收到 `LSPInstallProgressMsg` 与 `LSPInstallDoneMsg`）。
- `pkg/gateway/api_extra_test.go`：`TestLSPRecommendationDecisionEndpoint`（200；无效 choice 400；未知 id 404；无控制面 503）。
- `cmd/forebrain/lsp_test.go`：`TestLSPRecommendationsReset`（写入 disabled 状态后执行命令 → 文件被重置，输出逐字）。
- 前端：`lspRecommendation.test.ts`（解析两种键风格；缺字段返回 null）；`LspRecommendationCard.test.ts`（五个按钮各发出正确 choice；关闭按钮等于 `not_now`；install 轮询到 `installing: false` 后显示结果）；`useChatStream.test.ts`（历史事件不设置 `lspRecommendation`；实时事件设置；重复 id 不重复设置）。

## 完成判据

- [ ] 上述测试全部通过；`pnpm test`、`pnpm build` 通过且 `pkg/gateway/dist` 无改动
- [ ] `grep -n "errNotAvailable" pkg/lsp/control.go` 无输出（骨架占位全部被替换，同时删除该变量）
- [ ] `grep -rn "SetRecommendationListener" pkg/run` 无输出（监听器只在 `pkg/process` 安装）
- [ ] 手动验证（在 PR 描述里写结果）：受信任的 Go 项目、`gopls` 已装未启用时，让 agent 编辑一个 `.go` 文件 → 终端弹出推荐框；选「Yes, enable」→ transcript 出现 `gopls enabled for Go. …`，下一次编辑的结果带诊断
- [ ] `graph.json` 已提交；全量测试通过
- [ ] README 中任务 13 状态为 `DONE`

## STOP 条件

- 推荐需要在模型可见的内容里出现（工具结果、提醒、系统提示）。
- 需要在 `pkg/run` 里安装监听器，或需要给 `run.Runner` 加方法。
- 终端模态框在回合进行中出现时导致渲染错乱且无法用现有 overlay 机制解决（先在 PR 里描述现象再决定）。
- gateway 或 tui 需要新增包导入（fan-out 已满）。
- 测试无法构造「未启用的目录服务器」候选（任务 06 没有目录替换点）。

## 维护说明

- 推荐只针对目录中的 primary 服务器；自定义条目是用户自己写的，不需要推荐。
- `recDismissLimit` 与规范 §10.2 绑定；修改前先改规范。
- 评审重点：`considerRecommendation` 不阻塞 `DidWrite`；install 模式默认选中「No, not now」；`publishRunEvent` 的持久化去重让重连不会重复弹框。
