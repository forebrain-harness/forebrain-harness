> **已推迟（owner 2026-09-28 决定）**：`forebrain exec` 原本唯一的消费方是 CI 自维护（原 009–011）。
> CI 自维护已被否决，改为 owner 自己机器上的 forebrain gateway 方案（见 README 的"推迟的批次"）；gateway 方案里
> 定时任务走 `RunAgentOnceExec`（`pkg/process/one_shot.go`），不需要新的 CLI 命令。本文件本期**不执行**，
> 保留作为下一批计划的输入；届时先重新评估 exec 是否还需要。文中的里程碑编号（M3）与依赖关系已过期。

# Plan 003：新增 `forebrain exec`——用 TUI 同一引擎做一次性无人值守执行

> **执行者须知**：逐步执行，每一步都要跑"验证"命令并确认结果与预期一致后才进入下一步。
> 出现"STOP 条件"中的任何一种情况，立即停止并汇报，不要自行发挥。完成后把
> `docs/plan/OSS_AUTOMATION/README.md` 里本计划的状态改成 DONE。
>
> **提交（里程碑 M3）**：本地步骤全部通过后，按 `README.md` 全局规则 1 提交**一次**：标题见 README 的
> "里程碑与提交标题"表，正文按 `.gitmessage` 模板（Why / What / Prompt cache / Verification，外加
> `Refs: docs/plan/OSS_AUTOMATION/<本文件名>`），用 `git commit -s -F <message-file>`；只 `git add` 本计划"范围"内的文件。
> 不得 `--amend`、`rebase`、`push --force`。开工前确认 `git status --short` 干净、`git log -1` 是上一个里程碑。
>
> **真实仓库实测**：按 README 的关卡推送后，在 `git@github.com:forebrain-harness/forebrain-harness.git` 上完成本文件"真实仓库实测"一节，
> 证据写入 `LIVE_TEST_LOG.md`。实测通过前本计划状态只能是 `AWAITING PUSH`。
>
> **绝不能碰真实数据**：开发与验证时一律使用临时 `FOREBRAIN_HOME`（`mktemp -d`），禁止让开发构建的二进制
> 打开 `~/.forebrain` 下的真实状态库。
>
> **漂移检查（先做）**：本计划编写时仓库还没有提交，M0 之后才有历史。开工前逐条核对"现状"中的摘录与实际文件一致，并用
> `git log --oneline -- <范围内路径>` 查看 M0 之后是否有别的提交改过这些文件；有且与摘录不符，就按 STOP 处理。

## 状态

| 项 | 值 |
| --- | --- |
| 优先级 | P1（计划 009–011 的自维护全部依赖它） |
| 工作量 | M |
| 风险 | MED：触及 TUI 引擎的通知队列与审批 sink |
| 依赖 | 无（与 001/002 无代码交集，可并行） |
| 类别 | direction / dx |
| 编写于 | 2026-09-28，基于未提交的工作区 |

## 为什么要做

owner 决定让项目由 Forebrain Harness 自己维护，方式是新增 `forebrain exec`：一个一次性、无人值守的命令。
它在临时 CI runner 里运行，所有改动只以 PR 形式出现，审批点放在人工审阅 PR 上。当 agent 需要人做决定时
（`user_interaction` 提问、`exit_plan_mode` 等计划审批，或者没开 `--yolo` 时的工具审批），`exec`
不能卡住等待，而要结束并把问题或计划打印出来，由工作流贴回 issue/PR。

项目有两条硬规则：
1. **TUI 是语义的黄金标准**，其他入口必须复用同一引擎，不得另写一套；
2. **输入 token 缓存命中率只升不降**。

因此 exec 必须走 TUI 的 `ChatSession.DispatchSurfaceTurn`，channel 也用 `"tui"`，保证 prompt 组装、
工具表、技能表、会话持久化与交互式 TUI 逐字节一致。只把"屏幕"换成"stdout/stderr"，"审批浮层"换成
"结束并报告"。

## 现状

### CLI 入口（`cmd/forebrain`，共 8 个生产文件，没有文件上限压力）

- `cmd/forebrain/root.go`：
  - 全局 flag `--home`（设置 `FOREBRAIN_HOME`）、`--yolo`（设置 `FOREBRAIN_YOLO=1`），在
    `PersistentPreRunE = prepPersistentHome` 中生效。
  - `Execute()`（第 32–45 行）目前一律 `os.Exit(1)`：
    ```go
    func Execute() {
    	rootCmd.Version = home.Version
    	rootCmd.SetVersionTemplate("{{.Version}}\n")
    	if err := rootCmd.Execute(); err != nil {
    		if errors.Is(err, tui.ErrCancelled) {
    			return
    		}
    		rh := ""
    		if root, derr := home.Root(); derr == nil {
    			rh = root
    		}
    		telemetry.ErrorThenFprintln(rh, os.Stderr, err, "cmd/forebrain rootCmd.Execute")
    		os.Exit(1)
    	}
    }
    ```
- `cmd/forebrain/interactive.go` 的 `runStreamingTerminalWithInitialSessionID` 是交互式启动顺序的
  权威参照：信任检查 → 项目 MCP 确认 → `tui.RedirectProcessLoggingForTUI(root)` →
  `tui.NeedsFirstSetup()` → `tui.StartupConfigError()` → `process.Resolve()` +
  `tui.OpenChatSessionWithConfigForProject(ctx, rt.Config, cwd)` → `defer sess.Close()`。
- `cmd/forebrain/workspace_trust.go`：`workspaceTrustTarget(cwd)`、`safety.TrustLevel(home, safety.Project{Root: target})`、
  `markWorkspaceTrusted(home, target)`、`safety.LevelUnknown`。注释说明：已记录的"拒绝"不会中止会话，
  只会让审批策略变严。
- `cmd/forebrain/mcp_consent.go`：项目级 MCP 条目只在启动时逐条确认，未确认的一律不加载（fail closed）。

### TUI 引擎（`pkg/tui`，已有 20 个生产文件，**达到上限，不能新建文件**）

- `pkg/tui/chat_surface.go:23` `func (s *ChatSession) DispatchSurfaceTurn(ctx, submission turn.TurnSubmission) error`。
  审批循环（第 163–185 行）：
  ```go
  sink := s.toolApprovalSinkGet()
  for s.surfaceNeedsToolApproval(ctx, sessionID) {
  	req, err := s.buildSurfaceToolApprovalRequest(ctx, sessionID)
  	...
  	if sink == nil {
  		return fmt.Errorf("%w", turn.ErrAwaitingToolApproval)
  	}
  	...
  	decision, perr := sink.PromptToolApproval(ctx, *req)
  	if perr != nil {
  		return perr
  	}
  	...
  }
  ```
  sink 返回的错误会原样从 `DispatchSurfaceTurn` 返回。exec 就靠这一点结束回合。
- `pkg/tui/chat_surface.go:258` `func (s *ChatSession) SetToolApprovalSink(sink turn.ToolApprovalDecisionSink)`。
- `pkg/turn/model.go:492` `turn.ToolApprovalRequest`，与 exec 相关的字段有 `ActionKind`、`ToolName`、
  `Description`、`AskFormJSON`（`user_interaction` 的问题，JSON 格式为 `state.AskForm`）、`PlanFilePath`
  （`exit_plan_mode` 的计划文件）。`pkg/turn/model.go:559`：
  ```go
  type ToolApprovalDecisionSink interface {
  	PromptToolApproval(context.Context, ToolApprovalRequest) (ToolApprovalDecision, error)
  }
  ```
- `pkg/state/action_service.go:427` `state.AskForm{Title, Questions []AskQuestion{Prompt, Options []AskOption{Label, Description}}}`。
- 通知队列（`pkg/tui/chat_session.go:717–850`）：`notifyUI` 把消息放进 FIFO 队列，由后台 goroutine
  `runUINotificationDispatcher` 依次调用 `uiNotify`。`Close()` 调用 `stopUINotificationDispatcher()`，
  它会**丢弃**队列里尚未投递的消息，而且不等待。目前**没有**"等队列排空"的方法。
- `pkg/tui/chat_session.go:561` `PrependUINotify(fn func(any))` 用来挂一个通知回调。
- `pkg/tui/notify.go:42` `tui.Message`，有用的字段是 `Kind`（`MsgKindTool`、`MsgKindError` 等）、`ToolName`、
  `Summary`、`ToolPhase`（`event.RunEventToolStarted == "tool_call_started"`）、`Content`；
  通知类型是 `NewMessageMsg{Msg: Message}`。
- `pkg/tui/chat_session.go:1104` `NewSessionID(prefix)`；交互式 TUI 用 `NewSessionID("cli")`（`pkg/tui/run.go:115`）。
- `pkg/tui/run.go:3011` 交互式 TUI 的提交方式（exec 要与之一致）：
  ```go
  err := session.DispatchSurfaceTurn(ctx, turn.TurnSubmission{
  	SessionID: sessionID, Channel: "tui", UserText: line, ...,
  	CallerRendersReturnedError: true,
  })
  ```
- `pkg/tui/reducer.go:1262` `workedForLabel(elapsed) string` 返回 `"Worked for 9m 09s"` 这类文案。项目规则：
  每个 run 必须以一行 "Worked for …" 结束，没有阈值，也没有例外。
- 取最终答复：`(s *ChatSession) SurfaceTranscriptMessages(ctx, sessionID) ([]state.Message, error)`，
  `state.Message` 有 `Role`、`Content`。

### 测试范式

- `pkg/tui/chat_session_test.go:5559` `newCharacterizationSession(t, client llm.LLM) *ChatSession`：用临时
  SQLite 和脚本化 LLM 搭一个真实会话。`TestCharacterizationEnterExitPlanMode`（`chat_session_test.go:6827`）
  示范了脚本化模型发起 `enter_plan_mode`/`exit_plan_mode` 并经 sink 应答。新测试照此编写。
- `pkg/tui/chat_session_test.go:2665` `TestNotifyUIKeepsQueuedEventsWhenSinkChanges` 是通知队列测试的范式。

### 手工验证工具

`.claude/skills/run-forebrain/` 有一个假的 OpenAI 兼容 provider：
`python3 .claude/skills/run-forebrain/fake_provider.py <mode> <port> <text> <delay>`。
- `reply`：返回一段文字后结束
- `toolcall`：先 `enter_plan_mode`，再 `exit_plan_mode`
- `ask`：发起一次有两个问题的 `user_interaction`
- `hang`：接受请求后永不返回

配套的最小配置写法见 `.claude/skills/run-forebrain/driver.sh` 第 50–62 行（`api_key` 必须写成
`${ENV}` 引用，明文会导致启动失败）。

## 设计（照此实现，不要另行设计）

### 命令行契约

```
forebrain exec [--trust] [prompt...]
```

- prompt：参数用空格拼接；没有参数，或唯一参数是 `-` 时，从 stdin 读完整内容。stdin 是终端且没有参数
  时报错 `exec needs a prompt as an argument or on stdin`。
- `--trust`：当前项目的信任状态为 unknown 时记为可信，然后继续。不带 `--trust` 且状态为 unknown 时，
  报错 `<path> is not a trusted directory yet; run forebrain there once or pass --trust`。
  已记录的信任或拒绝都照常继续，与 TUI 一致。
- 全局 flag `--home`、`--yolo` 照常生效。`--yolo` 只绕过工具审批，`exit_plan_mode` 和
  `user_interaction` 仍需人决定（owner 规定 YOLO 不能替用户拍板）。
- 项目级 MCP：exec 不做确认，未确认的条目不加载，与 TUI 上"没回答确认提示"时的结果相同。
- 未完成首次设置时（`tui.NeedsFirstSetup()` 为 true），报错
  `forebrain has no provider configured yet; run forebrain in a terminal once to set one up`。
- 输出：
  - stdout：成功时打印最后一条 assistant 消息的正文；需要人决定时打印问题或计划（格式见下）。
  - stderr：进度行（每次工具调用开始打印 `• <Summary 或 ToolName>`，运行时错误打印 `error: <Content>`），
    结束时打印 `Worked for …` 和 `session: <session id>`。
  - 进程日志按 TUI 的方式重定向到日志文件（`tui.RedirectProcessLoggingForTUI`），不写 stderr。
- 退出码：`0` 成功；`1` 错误；`3` 需要人做决定；`130` 被 SIGINT/SIGTERM 中断。

### "需要人决定"的 stdout 格式（纯文本 markdown，工作流会原样贴进评论）

- `user_interaction`（`AskFormJSON` 非空）：
  ```
  The agent needs answers before it can continue.

  1. <Prompt>
     - <Label> — <Description>
     - …
  2. …
  ```
- `exit_plan_mode`（`PlanFilePath` 非空）：
  ```
  The agent is waiting for approval of this plan.

  <计划文件全文>
  ```
- 其他工具审批：
  ```
  The agent is waiting for approval to run <ToolName>.

  <Description>
  ```

### 代码落点

- `pkg/tui/chat_session.go`：新增 `FlushUINotifications(ctx context.Context) error`。实现方式：往队列尾部放一个
  屏障消息 `uiNotificationBarrier{done chan struct{}}`；`dispatchUINotification` 遇到屏障时关闭 `done`，
  不把它转交给 `uiNotify`。队列未启动或已停止时直接返回 nil。等待时响应 `ctx.Done()`。
  这样能保证 exec 退出前，所有已产生的进度通知都已打印。这是修"Close 会丢弃排队通知"的根因，
  而不是在 exec 里 sleep。
  注意 `coalesceQueuedToolOutput` 只合并 `ToolOutputDelta`，不影响屏障。
- `pkg/tui/chat_surface.go`（不能新建文件）：新增
  ```go
  // HeadlessResult is what one unattended turn produced.
  type HeadlessResult struct {
  	SessionID string
  	FinalText string
  	Elapsed   time.Duration
  }

  // HeadlessDecisionError ends an unattended turn at the first decision only a
  // person can make. Details renders the question or plan for that person.
  type HeadlessDecisionError struct{ Request turn.ToolApprovalRequest }

  func (e *HeadlessDecisionError) Error() string // one sentence, e.g. "the agent needs a decision from a person"
  func (e *HeadlessDecisionError) Details() string // the stdout text above

  // RunHeadless runs prompt as one turn of a new session the way the terminal
  // UI does — same dispatch, same channel — with progress written to progress
  // instead of a screen and every pending decision ending the turn.
  func (s *ChatSession) RunHeadless(ctx context.Context, prompt string, progress io.Writer) (HeadlessResult, error)
  ```
  `RunHeadless` 的步骤：
  1. `sid := s.NewSessionID("cli")`；记录开始时间。
  2. `s.PrependUINotify(fn)`，由 fn 把 `NewMessageMsg` 里的工具开始事件和错误写成进度行。
  3. `s.SetToolApprovalSink(headlessDecisionSink{})`：`PromptToolApproval` 直接返回
     `ToolApprovalDecision{}` 和 `&HeadlessDecisionError{Request: req}`。
  4. `err := s.DispatchSurfaceTurn(ctx, turn.TurnSubmission{SessionID: sid, Channel: "tui", UserText: prompt, CallerRendersReturnedError: true})`。
  5. `s.FlushUINotifications(ctx)`。
  6. 在 progress 上写 `workedForLabel(elapsed)` 一行，再写 `session: <sid>` 一行。
     无论成功、失败还是中断，都要写这两行。
  7. `err != nil` 时返回 `HeadlessResult{SessionID: sid, Elapsed: …}` 和 `err`（原样返回，不改写）。
  8. 成功时从 `s.SurfaceTranscriptMessages(ctx, sid)` 里取最后一条 `Role == "assistant"`、`Content`
     非空的消息，作为 `FinalText`。
- `cmd/forebrain/exec.go`（新建）：cobra 子命令 `exec`，按"命令行契约"实现，启动顺序与
  `runStreamingTerminalWithInitialSessionID` 相同，只是去掉终端专属的部分（caret、trust 页面、MCP 提示、
  onboarding 页面）。用 `signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)` 包装 ctx。
  出错时的映射：
  - `*tui.HeadlessDecisionError`：把 `Details()` 打到 stdout，返回 `exitCodeError{code: 3, err}`；
  - ctx 被取消：返回 `exitCodeError{code: 130, err}`；
  - 其他错误：原样返回。
- `cmd/forebrain/root.go`：新增
  ```go
  // exitCodeError ends the process with code once its message is printed.
  type exitCodeError struct {
  	code int
  	err  error
  }
  func (e exitCodeError) Error() string { return e.err.Error() }
  func (e exitCodeError) Unwrap() error { return e.err }
  ```
  `Execute()` 打印错误后，若 `errors.As(err, &coded)` 成立就 `os.Exit(coded.code)`，否则 `os.Exit(1)`。

### prompt cache 影响

exec 不新增、不改变任何进入模型的内容：channel、会话 id 前缀、提交结构都与交互式 TUI 一致。
缓存前缀逐字节不变。完成标准里有一条专门验证这一点。

## 需要的命令

| 用途 | 命令 | 成功时 |
| --- | --- | --- |
| tui 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok |
| cmd 测试 | `CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain -count=1` | ok |
| 架构测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | ok（pkg/tui 仍是 20 个文件） |
| 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 全部 ok |
| vet | `go vet ./...`，外加 `GOOS=windows go vet ./cmd/forebrain` | exit 0 |
| 包图 | `scripts/package-graph.sh` | 只有 loc 变化 |
| 死代码 | `go run golang.org/x/tools/cmd/deadcode@latest -tags fts5 -filter 'forebrain-harness/(pkg|cmd)/' ./...` | 条目数不多于开工前的基线 |
| 构建 | `CGO_ENABLED=1 go build -tags fts5 -o $TMPDIR/fbx ./cmd/forebrain && scripts/install-dictionary.sh $TMPDIR` | exit 0 |

## 范围

**允许修改**：`cmd/forebrain/exec.go`（新建）、`cmd/forebrain/exec_test.go`（新建）、`cmd/forebrain/root.go`、
`cmd/forebrain/root_test.go`、`pkg/tui/chat_surface.go`、`pkg/tui/chat_surface_test.go`、
`pkg/tui/chat_session.go`、`pkg/tui/chat_session_test.go`、`pkg/tui/doc.go`（记一句 headless 入口）、
`pkg/architecture/testdata/graph.json`（脚本生成）。

**不许碰**：
- `DispatchSurfaceTurn` 的主体逻辑：exec 必须复用它，不能复制一份。
- 审批决定的应用逻辑（`completeSurfaceToolApprovalDecision` 等）：exec 从不做决定，只报告。
- `pkg/gateway`：Web 端与本计划无关。
- 新建 `pkg/tui` 文件：会突破 20 文件上限。

## 步骤

### 步骤 0：记录基线

- `go run golang.org/x/tools/cmd/deadcode@latest -tags fts5 -filter 'forebrain-harness/(pkg|cmd)/' ./... | wc -l`，
  记下数字 N。
- `cp pkg/architecture/testdata/graph.json $TMPDIR/graph.before`。

### 步骤 1：通知队列的排空屏障

在 `pkg/tui/chat_session.go` 实现 `FlushUINotifications`（见"设计"）。在 `chat_session_test.go` 新增：

- `TestFlushUINotificationsDeliversEverythingQueuedBeforeIt`：挂一个慢回调（每条 sleep 几毫秒），
  enqueue 50 条消息后调用 Flush；返回时回调已收到 50 条，且顺序与入队一致。
- `TestFlushUINotificationsReturnsWhenNothingWasEverQueued`：没有启动过 dispatcher 时立即返回 nil。
- `TestFlushUINotificationsHonorsContext`：回调阻塞时，ctx 取消后 Flush 返回 `context.Canceled`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestFlushUINotifications -count=1 -v` → 3 个 PASS

### 步骤 2：`RunHeadless`、`HeadlessDecisionError`、sink

在 `pkg/tui/chat_surface.go` 实现（见"设计"）。在 `chat_surface_test.go` 新增，都用
`newCharacterizationSession` + 脚本化 LLM：

- `TestRunHeadlessReturnsTheFinalAssistantText`：模型回复 "done"；`FinalText == "done"`，
  progress 以 `Worked for` 行和 `session: cli-…` 行结尾。
- `TestRunHeadlessStopsAtAQuestion`：模型发起一次 `user_interaction`；返回的错误满足
  `errors.As(err, &*HeadlessDecisionError)`，`Details()` 含问题的 Prompt 和选项 Label；progress 仍以
  `Worked for` 结尾。
- `TestRunHeadlessStopsAtPlanApprovalEvenInYOLO`：设置 `FOREBRAIN_YOLO=1`（`t.Setenv`），模型走
  `enter_plan_mode` → `exit_plan_mode`；返回 `HeadlessDecisionError`，`Details()` 含计划正文。
- `TestRunHeadlessPersistsTheTurnLikeTheTerminal`：回合写入的会话与交互式 TUI 相同。断言会话 id 以 `cli-`
  开头（与 `pkg/tui/run.go:115` 相同的前缀），且 transcript 的第一条 user 消息内容逐字等于 prompt。查询方式参照
  `TestDispatchSurfaceTurnPassesResolvedMentionsThroughVerbatim`（`chat_surface_test.go:345`，
  `s.sessStore().ListTranscriptMessages`）。fb_runs 没有 channel 列，channel 一致性由步骤 6 的请求体比对证明。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestRunHeadless -count=1 -v` → 4 个 PASS

### 步骤 3：退出码机制

在 `cmd/forebrain/root.go` 加 `exitCodeError`，并修改 `Execute()`。`Execute` 里的 `os.Exit` 不好直接测，
把"错误 → 退出码"抽成 `exitCodeFor(err error) int`，由 `Execute` 调用。
在 `root_test.go` 新增 `TestExitCodeFor`：普通错误 → 1；`exitCodeError{3,…}` → 3；
被 `fmt.Errorf("%w")` 包一层的 `exitCodeError{130,…}` → 130。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain -run TestExitCodeFor -count=1` → ok

### 步骤 4：`exec` 子命令

新建 `cmd/forebrain/exec.go`，在 `init()` 里 `rootCmd.AddCommand(execCmd)`。启动顺序依次为：`home.Root()` →
信任检查（`--trust`）→ `tui.RedirectProcessLoggingForTUI(root)` → `tui.NeedsFirstSetup()` →
`tui.StartupConfigError()` → `process.Resolve()` → `tui.OpenChatSessionWithConfigForProject` →
`sess.RunHeadless(ctx, prompt, cmd.ErrOrStderr())` → 输出。可测试的部分参照 `interactive.go`，做成包级变量
（例如 `execOpenChatSession`），方便测试替换。

在 `cmd/forebrain/exec_test.go` 新增，风格参照 `interactive_test.go` 与 `workspace_trust_test.go`：
- `TestExecReadsThePromptFromStdinWhenGivenNoArguments`
- `TestExecRefusesAnUnknownDirectoryWithoutTrust`：错误文本是契约里的那一句。
- `TestExecTrustRecordsTheDirectory`：`--trust` 之后 `safety.TrustLevel` 不再是 unknown。
- `TestExecPrintsDecisionDetailsAndExitsThree`：替换 open 函数，返回一个 `RunHeadless` 会给出
  `HeadlessDecisionError` 的会话；如果难以构造，就把"错误 → stdout + exitCode"抽成函数单独测。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain -count=1` → ok

### 步骤 5：实机验证（假 provider，临时 HOME）

```bash
BIN=$TMPDIR/fbx; CGO_ENABLED=1 go build -tags fts5 -o $BIN ./cmd/forebrain && scripts/install-dictionary.sh $TMPDIR
H=$(mktemp -d); P=$(mktemp -d); echo scratch > $P/README.md; PORT=8741
cat > $H/forebrain.yaml <<YAML
approval_policy: on-request
agents:
  definitions:
    main:
      primary: true
      enable_subagent: false
      llm_providers:
      - provider: deepseek
        model: deepseek-chat
        api_key: \${FAKE_LLM_KEY}
        base_url: http://127.0.0.1:$PORT
YAML
python3 .claude/skills/run-forebrain/fake_provider.py reply $PORT 'hello from exec' 0 > $TMPDIR/p.log 2>&1 & echo $! > $TMPDIR/p.pid
(cd $P && FAKE_LLM_KEY=sk-fake FOREBRAIN_HOME=$H $BIN exec --trust 'say hi'; echo "exit=$?")
kill "$(cat $TMPDIR/p.pid)"   # 只杀自己记录的 PID，禁止 pkill
```

依次用 `reply`、`ask`、`toolcall`、`hang` 四种模式重复。`hang` 模式在另一个 shell 里对 exec 的 PID
发 SIGINT。

**验证**：
- `reply`：stdout 是 `hello from exec`，stderr 以 `Worked for …`、`session: cli-…` 结尾，`exit=0`
- `ask`：stdout 以 `The agent needs answers before it can continue.` 开头并列出两个问题，`exit=3`
- `toolcall`：stdout 是计划审批或 enter_plan_mode 审批的说明（取决于先卡在哪一步；两者都算对，但要在
  汇报里写明实际是哪一个），`exit=3`；加 `--yolo` 再跑一次，`exit_plan_mode` 仍然 `exit=3`
- `hang`：SIGINT 后 `exit=130`，stderr 仍有 `Worked for` 行
- 不带 `--trust` 在一个新目录执行：一句话报错，`exit=1`
- stderr 里没有 slog 日志行（日志在 `$H/logs/` 下）

### 步骤 6：缓存前缀一致性

用 `FAKE_DUMP_DIR`（fake provider 会把每个请求体存成 `request-NNNN.json`，见
`.claude/skills/run-forebrain/SKILL.md`）分别抓两次请求：一次交互式 TUI（用 driver 的 `start reply`，
提交同样的 prompt），一次 `exec`。比较两个请求体中 messages 之前的部分，也就是 `tools` 和 system。

**验证**：两份请求的 `tools` 字段与 system 消息逐字节相同（`jq -S '.tools' a.json | sha256sum` 与 b.json 的相同；
system 同理）。**不相同就 STOP**。

### 步骤 7：收尾

跑全量测试、vet（含 `GOOS=windows go vet ./cmd/forebrain`）、`gofmt -l cmd pkg`、包图、死代码。

**验证**：全部符合"需要的命令"表；deadcode 条目数 ≤ N。

## 真实仓库实测（关卡 G1）

1. 从真实仓库全新克隆并构建：
   ```bash
   git clone git@github.com:forebrain-harness/forebrain-harness.git "$TMPDIR/fb-clone" && cd "$TMPDIR/fb-clone"
   CGO_ENABLED=1 go build -tags fts5 -o "$TMPDIR/fbc/forebrain" ./cmd/forebrain && scripts/install-dictionary.sh "$TMPDIR/fbc"
   ```
2. 在克隆目录里重跑步骤 5 的五个场景（fake provider），结果必须与本地一致。
3. **真实模型**（项目规则：修复要在真实模型下证明）：
   - 新建临时 `FOREBRAIN_HOME`；
   - 只读地从 `~/.forebrain/forebrain.yaml` 复制当前主 agent 的 `llm_providers` 段。这个文件里应当只有 `${ENV}` 引用；
     **若出现明文密钥，STOP**，不要复制，也不要复述；
   - 确认对应环境变量在当前 shell 中存在，不存在就 STOP，请 owner 提供。然后在克隆目录里依次执行：
   - `forebrain exec --trust "In one sentence, what does scripts/check-go-install.sh prove?"` → exit 0，
     答复提到 go install，stderr 以 `Worked for …` 和 `session: cli-…` 结尾；
   - `forebrain --yolo exec --trust "Create a file named LIVE_TEST.txt whose only content is the word ok"` → exit 0，
     克隆目录中出现该文件（这是临时克隆，测完删除整个目录）；
   - `forebrain exec --trust "Before doing anything, use your question tool to ask me whether to proceed, with options yes and no"`
     → exit 3，stdout 以 `The agent needs answers before it can continue.` 开头。
4. 记录三条真实模型调用的 stdout、stderr 末尾与退出码。**不要**记录任何密钥或环境变量的值。

## 完成标准

- [ ] M3 已按规范提交
- [ ] 真实仓库实测第 1–3 步通过并记录（含真实模型）
- [ ] 步骤 1、2、3、4 的新测试全部存在且通过；`go test -tags fts5 ./...` 全绿
- [ ] `ls pkg/tui/*.go | grep -v _test.go | wc -l` 仍为 20
- [ ] 步骤 5 的五个实机场景结果全部与预期一致（在汇报里贴出每个场景的 stdout、stderr 末尾和 exit 码）
- [ ] 步骤 6：tools 与 system 逐字节一致
- [ ] deadcode 条目数 ≤ 基线 N；gofmt 无输出
- [ ] 改动全部在允许清单内

## STOP 条件

- 子代理（subagent）的审批不经过 `toolApprovalSinkGet()` 返回的 sink，导致 exec 在子代理审批处卡死，
  或者出现硬拒绝。项目规则是审批必须交给用户，不能硬拒绝，这需要 owner 决定。
- 实现 `RunHeadless` 需要改动 `DispatchSurfaceTurn` 的主体逻辑，而不只是调用它。
- 步骤 6 发现 exec 与 TUI 的 tools 或 system 不一致。
- 回合结束后 `SurfaceTranscriptMessages` 取不到 assistant 消息，而模型确实回复了。这说明持久化与假设
  不符，要汇报。
- 需要在 `pkg/tui` 新建文件。

## 维护说明

- exec 是"没有屏幕的 TUI"：以后给交互式 TUI 的提交路径（`run.go` 里的 `DispatchSurfaceTurn` 调用）加字段时，
  要同步检查 `RunHeadless` 要不要带上同样的字段。
- `FlushUINotifications` 是通用能力，以后任何需要"关闭前把通知送完"的 surface 都应该用它。
- 退出码 3 是计划 009–011 工作流的契约：工作流看到 3 就把 stdout 贴成评论，而不是开 PR。改退出码要同步改工作流。
