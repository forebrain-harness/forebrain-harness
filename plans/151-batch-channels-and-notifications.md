# Plan 151：后台跑批的通道接入与任务结束通知——通道里每条消息开一条新任务并回执；任务结束（成功 / 失败 / 停止）自动经通道通知

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不建 worktree，改动直接留在 dev 工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`149`、`150` 为 DONE；`grep -n "func WithBatchTasks" pkg/turn/*.go` 有输出。
>
> **Drift check（先跑）**：`git diff --stat d9d0b4d -- pkg/gateway/server.go pkg/gateway/channels.go pkg/gateway/serve_run.go pkg/gateway/api_extra.go pkg/process/cron_service.go pkg/state/project_store.go pkg/state/schema.sql pkg/config/config.go`
>
> **基线**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./pkg/process ./pkg/state ./pkg/config -count=1`（900 s）。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（改通道入站分派；向外部 IM 发消息）
- **Depends on**: `149`、`150`
- **Category**: direction
- **Planned at**: dev `d9d0b4d` + 2026-10-27 复核工作区

## owner 裁定（2026-10-26）

1. 跑批项目允许"通道接入"，并且"**必须支持任务完成（包括成功、失败和停止）后自动通过接入的通道发出通知**"。
2. 通知发给谁、发什么（选项题，选推荐）："**通道发来的任务回到发消息的群/人；WS、定时、手工新建的任务发到项目设置里配的『任务通知』（通道 + 接收者 ID，可配多个）**。
   内容：成功/失败/已停止、任务标题、耗时、结论摘要、生成物清单和 web 查看链接。**通道来的每条消息都开一条新任务**。"
3. 跑批"每次任务都是一个新会话"（149 的任务 / 执行模型）。

## 设计（评审方定）

- **入站**：跑批项目的通道收到一条消息 → 内置斜杠命令一律拒绝（回一句"后台跑批项目只接收任务内容"）；否则新建任务（`origin='channel'`，`origin_ref` 记来源群/人）+ 第 1 次执行（新会话、新工作区）→
  立刻回执一句 `已接收任务：<标题>（<任务ID>），完成后通知。` → 跑回合（无人值守）。原"把回答直接回复到群里"的路径对跑批**不走**（结果由结束通知送达）。
- **结束通知**：引擎在每次执行结束时调用 149 的 `onEnd` 回调；网关在回调里按来源选目标并投递：
  `origin='channel'` → 回到 `origin_ref` 那个群/人；其它来源 → 项目的 `task_notify` 目标列表（可为空，空则不发）。
  失败的投递只记日志与执行行的 `notify_error`，**不影响任务状态**。
- **通知文案**（固定中文模板，按 owner "文案简洁"规则；字段缺失的行整行省略）：
  ```
  【任务完成】<标题>            ← 失败为【任务失败】，停止为【任务已停止】
  任务 <任务ID> · 第 <序号> 次执行 · 用时 <1m23s>
  结论：<最后一条助手回复的前 300 字，换行压成空格>
  原因：<error_code>：<error_text 前 200 字>   ← 仅失败
  生成物：<名称>（<大小>）、<名称>（<大小>）…（最多 10 个，超出写"等 N 个"）
  查看：<gateway.public_url>/projects/<项目ID>/tasks/<任务ID>   ← 仅当配置了 public_url
  ```
- **项目通知目标**存在项目行上：`fb_projects.task_notify_json`（JSON 数组 `[{"channel":"feishu","target":"oc_xxx"}]`），项目接口读写；
  另给网页一个"可选目标"接口：列出该项目通道里出现过的群 / 人（来自 `origin='channel'` 任务的 `origin_ref` 与该项目的通道会话），省得手填 ID。
- **定时任务**：跑批项目的定时触发不再用定时任务自己的"投递通道"（否则会和任务通知重复）——跑批项目里 `CronService.deliver` 直接返回 nil；网页在 `153` 隐藏该字段。
- **对外地址**：新增可选配置 `gateway.public_url`（如 `https://forebrain.example.com`），只用于拼通知里的查看链接；未配置则不出"查看"行。

## Current state

- 通道入站：`pkg/gateway/server.go` `channelBus.PublishInbound`（约 `:626` 起）：`sid := state.WrapProjectChannelSession(ch, m.ProjectID, m.SessionID)` → `parseSlashCommand(b.s, sid, ch, m.Text)` →
  `sc.ShouldContinueRun` 时 `b.s.submitChannelTurn(ctx, ch, projectID, sid, input, m.Text, sc.SkillName, sc.SkillPath)` → 拿 `res.TextContent()` 经 `safety.SanitizeOutbound` 后回复到 `sid`。
  `channel.Inbound`（`pkg/channel/channel.go:16-25`）只有 `ChannelID, ProjectID, Text, SessionID, Raw`——**没有附件**，所以通道任务只有文字输入。
- 出站：`channelBus.deliverOutboundMaybeSanitized(ctx, channelID, sessionID, text, skip)`（`server.go` 约 `:600`）把带项目前缀的会话 id 拆回 `projectID, rawSession`（`state.UnwrapProjectChannelSession`）再 `b.s.Channels.Deliver(ctx, channel.Outbound{ChannelID, ProjectID, SessionID: rawSession, Text})`；
  各通道的 `DeliverOutbound` 从 `SessionID` 解析收件方（例：飞书 `parseFeishuSession`：`chat:<id>` 或裸 id）。`(*Server).DeliverChannelOutbound(ctx, channelID, sessionID, text)`（约 `:571`）是公开入口。
- `state.WrapProjectChannelSession(kind, projectID, raw)` = `<prefix>-<hex(projectID)>-<raw>`（`pkg/state/id.go:108`）。
- 定时投递：`pkg/process/cron_service.go` `(*CronService).deliver(ctx, channelID, sessionID, text)`（约 `:225-243`）由调度器在一次触发结束后调用（`DeliverText: c.deliver`，约 `:92`）。
- 149 提供：`turn.WithBatchTasks(store, artifacts, onEnd func(context.Context, turn.BatchExecutionEnd))`（网关构造点 `pkg/gateway/serve_run.go` 约 `:66`，149 先传了 nil）；
  `BatchExecutionEnd{SessionID, RunID, Task, Execution, Status, ErrorCode, ErrorText, Artifacts}`；`ProjectStore.CreateTask` / `AddTaskExecution` / `StartTaskExecution`（由 149 首次 Submit 原子固定输入）；`state.TaskOriginChannel`。
- 配置：`type Gateway struct { HTTPAddr string; Auth GatewayAuth }`（`pkg/config/config.go:5-8`）。
- 约束：`pkg/gateway` 31 个生产文件——不新建；`pkg/state` 新列走迁移（下一个版本号）。

## Steps

### Step 1：项目的通知目标（`pkg/state` + 迁移）

1. `schema.sql` 的 `fb_projects` 加 `task_notify_json TEXT NOT NULL DEFAULT '[]'`（注释：跑批项目任务结束通知的目标；`[{"channel","target"}]`）；
   `fb_task_executions` 加 `notify_error TEXT NOT NULL DEFAULT ''`。迁移：下一个版本号（149 基线预期之后为 v23），两个"查列再 ADD COLUMN"；以 149 落地后的当前值 + 1 为准，若其它迁移先落地则顺延。
2. `Project` 加 `TaskNotify []TaskNotifyTarget`（`type TaskNotifyTarget struct { Channel string \`json:"channel"\`; Target string \`json:"target"\` }`），`projectColumns`/`scanProject`/`Create`/`FindOrCreateByRoot`/`Update` 照 145 加 `project_type` 的方式补列（JSON 编解码；坏 JSON 读作空）。
   `UpdateProjectInput` 加 `TaskNotify *[]TaskNotifyTarget`；校验：`channel` 必须在 `channel.ChannelKinds` 里——`pkg/state` 不能 import `pkg/channel`，**校验放网关**，state 只做 trim 与去重（同 channel+target 只留一个）、最多 20 条。
3. `ProjectStore.SetExecutionNotifyError(ctx, sessionID, text string) error`。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` → ok。

### Step 2：配置 `gateway.public_url`

`pkg/config/config.go` `Gateway` 加 `PublicURL string \`yaml:"public_url,omitempty" json:"public_url,omitempty"\``（注释：外部访问 web 的根地址，只用于拼通知链接；不影响监听）。
仓库根 `forebrain.yaml` 里 `gateway:` 段（若有）加一行注释示例 `# public_url: https://forebrain.example.com`。

**Verify**：`go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/config -count=1` → ok。

### Step 3：通道入站 → 新任务（`pkg/gateway/server.go` `PublishInbound`）

在算出 `sid`、`projectID` 之后、`parseSlashCommand` 之前：若 `projectID != ""` 且该项目 `ProjectType == batch`，走新私有方法 `b.s.startChannelTask(ctx, ch, projectID, sid, m.Text)` 并 `return`（不再执行后面的普通对话逻辑）。`startChannelTask`（写在 `pkg/gateway/channels.go`）：
1. 文本以 `/` 开头且是内置命令（`turn.IsBuiltinName` 判命令名）→ `deliverOutbound(ch, sid, "后台跑批项目只接收任务内容，不支持命令。")`，返回。空文本 → 直接返回。
2. `task := CreateTask(Task{ProjectID, Title: state.SessionTitleFromContent(text), Origin: channel, OriginRef: <JSON {"channel": ch, "session": sid}>})`；`title := task.Title`。**不提前 RecordTaskInput**，首次 Submit 按 149 的原子启动规则写入原始文本，附件准备/预检失败不会留下已锁输入的 pending 执行。
3. 新会话 id `state.NewID("batch")`；`EnsureAt(newSID, title, SessionBirth{Cwd: p.Root, GitBranch: memory.GitBranch(p.Root), ProjectID: p.ID, Source: state.SessionSourceConversation})` → `bindSessionToProject` → `AddTaskExecution(task.ID, newSID, p.Root)` → 建 `inputs/ outputs/ .work/`。
4. 回执：`deliverOutbound(ch, sid, fmt.Sprintf("已接收任务：%s（%s），完成后通知。", title, task.ID))`。
5. 跑回合：`b.s.submitChannelTurn(ctx, ch, projectID, newSID, text, text, "", "")`（它会再 `EnsureAt`/绑定，幂等），**忽略**它的返回文本（结果走结束通知）。
任何一步失败：`slog.Error` + 回一句 `任务创建失败：<错误>` 到原群 / 人，并清理已建的执行目录。

**Verify**：`go build ./...` → exit 0。

### Step 4：结束通知（`onEnd`）

1. `pkg/gateway/serve_run.go` 的 `turn.New(` 处把 149 传的 `nil` 换成 `s.notifyBatchExecutionEnd`（若 `s` 在该处还未构造，改为传一个闭包，在 server 构造后赋值——照该文件里其它"先建 core 后绑 server"的写法）。
2. `pkg/gateway/channels.go` 新增 `func (s *Server) notifyBatchExecutionEnd(ctx context.Context, end turn.BatchExecutionEnd)`：
   - 目标：`end.Task.Origin == channel` → 解析 `OriginRef` JSON 得 `{channel, session}`，投递用 `s.DeliverChannelOutbound(ctx, channel, session, text)`；
     否则读项目 `TaskNotify`，对每个 `{channel, target}`：`s.Channels.Deliver(ctx, channel.Outbound{ChannelID: channel, ProjectID: end.Task.ProjectID, SessionID: target, Text: safety.SanitizeOutbound(cfg, text)})`。
   - 文案：照"设计"里的模板。"结论"取该会话最后一条助手消息文字（用 `s.Sessions` 现有的"最近消息"读取函数，取 role=assistant 的最后一条）；"用时"= `finished_at - started_at`（用 `tool.FormatDuration` 或该包现有的时长格式函数，>=60s 用 `1m23s`）；
     大小格式与 146 的 `formatPresentFilesStep` 用同一个函数（若是私有函数，把它导出为 `tool.HumanSize`，并在 146 的调用处换用——这一改动在 `pkg/tool` 内部，允许）。
   - 失败的投递：收集错误文本，`SetExecutionNotifyError(sid, text)`，`slog.Warn`；**不 panic、不阻塞**：整个通知放 `go func(){...}()` 并用 `context.WithTimeout(context.Background(), 30*time.Second)`。
3. `pkg/process/cron_service.go` `deliver`：先查该会话所属项目（函数里已在查 `ProjectForSession`），项目是 batch → `return nil`（注释：跑批的结果由任务结束通知送达，避免重复）。

**Verify**：`go build ./...` → exit 0。

### Step 5：项目接口与"可选目标"接口

1. `projectPayload` 加 `TaskNotify *[]state.TaskNotifyTarget \`json:"task_notify"\``；PATCH 时校验每条 `channel` 在 `channel.ChannelKinds` 内、`target` 非空，否则 400 `{"error":"task_notify_invalid"}`；`projectRow` 输出 `"task_notify"`（空数组而非 null）。
2. `GET /api/v1/projects/:id/channel-targets` → `{"items":[{"channel","target","label","last_seen"}]}`：来源① 本项目 `origin='channel'` 的任务的 `origin_ref`（`target` = 用 `state.UnwrapProjectChannelSession(channel, session)` 拆出的原始收件方 id，`label` = 该任务标题，`last_seen` = 任务 `created_at`）；
   ② 本项目的通道会话（`fb_sessions` 中 id 以该项目各通道前缀开头的，用同一拆分）。按 `(channel, target)` 去重、`last_seen` 倒序、最多 100 条。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → 无新增失败。

### Step 6：对接文档补一节

`docs/integration/batch-tasks.md` 加"通道与通知"一节：通道入站即任务、回执与通知模板、`task_notify` 配置示例（PATCH 项目）、`gateway.public_url`。

## Test plan

1. `pkg/state`：`TestProjectTaskNotifyRoundTrip`（写入去重 / 上限 / 坏 JSON 读空）；迁移测试 `TestStateV22UpgradesToV23AddsTaskNotify`（版本号按实际）。
2. `pkg/gateway/channels_test.go`（照该文件现有的假通道测试写法：注册一个记录出站消息的假 handler）：
   - `TestBatchChannelMessageStartsATask`：跑批项目收到文本 → 新任务（origin channel）+ 执行 + 回执消息发回原会话；普通（非跑批）项目行为不变。
   - `TestBatchChannelRefusesBuiltinCommands`：`/plan` → 回复拒绝文案、没有建任务。
   - `TestBatchEndNotifiesTheOriginChat`：直接调用 `notifyBatchExecutionEnd`（completed、有两个生成物）→ 原群收到的文案含【任务完成】、任务 ID、两个生成物名；配置了 `public_url` 时含链接，否则不含"查看"行。
   - `TestBatchEndNotifiesProjectTargets`：origin api 的任务 → 发到项目两个目标；一个目标投递失败 → 另一个仍收到、执行行 `notify_error` 有记录。
   - `TestBatchStoppedAndFailedWording`：stopped → 【任务已停止】；failed → 【任务失败】+ 原因行。
3. `pkg/process/cron_service_test.go`：`TestCronDeliverSkipsBatchProjects`。
4. `pkg/gateway/api_extra_test.go`：`TestProjectTaskNotifyValidation`；`TestProjectChannelTargetsListsSeenChats`。

## Done criteria

- [ ] `go build ./...` exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./pkg/process ./pkg/state ./pkg/config ./pkg/turn ./pkg/architecture -count=1` 无新增失败；新测试存在且通过
- [ ] `ls pkg/gateway/*.go | grep -v _test.go | wc -l` 仍是 31
- [ ] 非跑批项目的通道对话行为不变（`channels_test.go` 既有用例全绿）
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

1. 前置不满足或"现状"对不上。
2. `serve_run.go` 里 `turn.New(` 的位置拿不到 `*Server`、也无法用闭包后绑（会形成初始化环）——停下报告。
3. 某通道的 `DeliverOutbound` 需要的收件方格式无法从入站会话 id 拆出（`channel-targets` 给不出可用 target）——列出该通道停下，不要为它写特例。

## Maintenance notes

- 通知走的是各通道已有的纯文字出站能力；附文件到 IM 不在本期（owner 选的是"清单 + 链接"）。
- `origin_ref` 的 JSON 形状 `{"channel","session"}` 是内部格式，`channel-targets` 接口负责把它翻成网页可用的 `(channel, target)`。
- 回执与通知文案是固定中文模板；若以后要多语言，按项目或通道加语言设置，不要在引擎里猜。
