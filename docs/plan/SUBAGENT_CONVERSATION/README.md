# subagent 成为可以直接对话的会话：实施计划索引

由 improve skill 于 2026-10-04 生成，基线提交 `1a6d708`（工作区里另有大量不属于本计划的未提交改动，见"与并行工作的关系"）。
**状态：D1–D9 全部已由 owner 定夺（D1–D4 由 owner 直接选择，D5–D9 于 2026-10-04 按推荐批准，见决策表），全部纳入本期。开始实施前仍以 owner 的明确指令为准。**

## owner 的原始要求（2026-10-04，逐字）

> forebrain harness的subagent相关的语义和功能需要优化：
>
> 1. 现在subagent会话/视图因为网络原因失败和中断后，无法通过发送"continue"用户消息让subagent继续执行，subagent视图里发送的用户消息会加入primary agent的message queue，只能回到primary agent视图，按下esc键将本应该发送给subagent的消息发送给primary agent。subagent失败后，必须支持在该subagent的视图里发送用户消息给该subagent使会话继续
> 2. subagent会话/视图必须支持该subagent独享的完整的message queue语义和与用户直接对话的语义
> 3. subagent会话/视图必须支持compact压缩，包括auto compact自动压缩和用户执行slash命令/compact手动压缩
> 4. subagent视图footer区域右侧必须展示"75%/1M"这样的该subagent自己的上下文窗口统计数据，与primary agent主视图保持一致

> 还有第5点：primary agent主视图里点击subagent的消息卡片，进入该subagent的视图时，agent roster区域的agent选择器没有自动选中该subagent的行，已经进入subagent的alt-screen VM视图，但是agent roster里箭头仍然指向main，必须定位根因并修复，加入本期计划

> 必须使用 improve skill 先写计划，我批准后再实施，不要直接改代码

> （附 TUI 截图）如forebrain harness tui的截图所示，agent roster 里的subagent行的任务与subagent消息卡片的任务不一致，请定位根因并修复bug，加入本期计划。agent roster 里的subagent行的任务必须使用subagent消息卡片的任务

> 因为usage limit reach而中断的subagent必须可以自动恢复，对齐primary agent的语义，给subagent加上相同功能

## 一句话结论

七个要求背后是六个根因，前三个都在引擎里，不在界面上：

1. **subagent 的模型上下文从来没有落库。** typed subagent 用 `workerSessionID` 跑 `run.Run`，但没有任何代码往这个会话写消息（本机状态库核实：`fb_messages` 里 `session_id LIKE 'main:%'` 的行数为 0，`fb_sessions` 里也没有这类会话）。fork subagent 的上下文只在内存和一个旁路 JSONL 里。所以 subagent 一旦失败，它做过的工具调用和结果全部丢失。`subagent_continue` 只能拼一段"原任务 + 上次结果摘要"重新开始（`pkg/run/subagent.go:1283` `buildSubagentContinuePrompt`）。`/compact` 没有可压缩的东西，footer 的上下文占用也无从计算。这是第 1、3、4 点共同的根因（计划 002）。
2. **subagent 没有自己的输入通道。** subagent 的运行上下文被刻意剥掉了输入运行时（`pkg/run/subagent.go:803` `WithoutTurnInputRuntime`），子运行在控制器里也只登记了取消函数，没有输入队列（`:827` `own.Control.Track`）。TUI 的 composer 不看当前视图，提交永远走主会话：空闲时直接起主回合，运行中则 steer 或排进主 agent 的队列（`pkg/tui/run.go` 的主循环和 `handleActiveRunInput`）。这是第 1、2 点的根因（计划 005、007、008）。
3. **队列语义有两份实现。** TUI 主视图自己维护一套镜像队列（`streamState.pendingSteers/rejectedSteers/queuedTurns` + `nextAutomaticSubmission`），网页用引擎的 `run.InputQueue`。两者已经分叉，例如"召回最新排队消息"：TUI 按统一的入队时钟取最新一条，引擎先取 steer。TUI 的注释明确说后者是缺陷（`pkg/tui/run.go:2400-2407`）。要让 subagent "独享完整的队列语义、与主视图一致"，只能先把队列语义统一到引擎（以 TUI 为准），再给每个 subagent 一份（计划 004、005）。
4. **roster 光标有两个事实来源。** 箭头在 roster 获得键盘焦点后读 `streamState.agentRosterSelected`，这个下标只被 ↑/↓ 改写；点击卡片改变视图时它不动。已用一次性测试（`go test -overlay`，未落盘到仓库）复现：先按 ↓ 让 roster 获得焦点，再点卡片，视图已是 `task-b`，箭头仍指着 `main`。同一个下标在 subagent 行被移除时还会挪到别的 agent 上，按 `x` 会取消错的 subagent（计划 001）。
5. **roster 行显示的是"正在跑的工具"，任务名还有两套推导。** `pkg/tui/render.go` 的 `agentRosterRowDetail` 按 `Activity` → `Title` → `Task` 取值，subagent 一调工具，行就变成 `edit /…`、`read /…`（当初有意的设计，owner 现已否定）。卡片上的任务名（`parseFanoutTasksFromMeta`：title，否则 prompt 截 80 列）和 roster 的标题（引擎 `subagentDisplayTitle`：title，否则 prompt 第一行截 160 字节）又是两条规则，没给 title 时不一致（计划 010）。
6. **自动继续只挂在主会话的回合上。** 主 agent 的"额度恢复后自动继续"由 `turn.Service.Submit` 在回合结束时安排（`pkg/turn/submit.go` 的 `autoContinuer`），subagent 的执行不经过 `Submit`，而且 `planFor` 明确排除了子运行（`req.ParentRunID != ""`）。计划 011 让同一个调度器按 subagent 安排、取消和触发，继续时经由计划 005 的用户输入通道发给这个 subagent。

另有两处顺带发现的既有缺陷，按规矩纳入本期：

- **有自己模型的 typed subagent，压缩阈值按主 agent 的模型窗口算。** `compactDeps.ActiveModel` 和压缩服务的 `PrimaryModel` 都走 `r.effectiveModelFor(ctx)`（`pkg/run/config.go:1676`），而 typed subagent 的请求被 `typedSubagentProviderLLM`（`pkg/run/typedsubagent_llm.go`）路由到 `agents.definitions[<type>].llm_providers`。窗口比主模型小的 subagent 会在压缩前先撞上上限（计划 003）。
- **footer 的上下文预算在 TUI 和 gateway 各算一遍。** `pkg/tui/chat_turn.go:1214` `tokenBudgetMessageFromUsage` 和 `pkg/gateway/wsevents.go:392` `tokenBudgetPayloadFromSession` 是同一段逻辑的两份拷贝（计划 006 下沉到引擎，并按 agent 区分模型）。

## 执行顺序与状态

| 计划 | 标题 | 对应要求 | 优先级 | 工作量 | 依赖 | 状态 |
| --- | --- | --- | --- | --- | --- | --- |
| 001 | [roster 光标以视图为准，按 agent 键记录](001-roster-cursor-follows-view.md) | 5 | P1 | S | — | TODO |
| 002 | [每个 subagent 的模型上下文落到它自己的 worker 会话里](002-persist-subagent-transcripts.md) | 1、3、4 的根基 | P1 | L | SRT-002、SRT-003 | TODO |
| 003 | [subagent 的压缩：按它自己的模型算阈值，支持回合前自动压缩和 /compact](003-subagent-compaction.md) | 3 | P1 | M | 002 | TODO |
| 004 | [队列语义只在引擎里实现一份，以 TUI 为准，TUI 主视图改用它](004-unify-queue-semantics.md) | 2 的前提（既有缺陷） | P1 | L | — | TODO |
| 005 | [引擎：每个 subagent 一条独享的用户输入通道](005-subagent-input-channel.md) | 1、2 | P1 | L | 002、004 | TODO |
| 006 | [上下文预算下沉到引擎，按 agent 计算并推送](006-per-agent-context-budget.md) | 4 | P1 | M | 002、003 | TODO |
| 007 | [TUI：subagent 视图里的对话、队列、Esc、斜杠命令和 footer](007-tui-subagent-conversation.md) | 1–4 | P1 | L | 001、003、005、006 | TODO |
| 008 | [网页：与 TUI 对齐的 subagent 对话](008-web-subagent-conversation.md) | 1–4 | P1 | L | 003、005、006、007 | TODO |
| 009 | [真机验收：断网后在 subagent 视图里继续](009-live-acceptance.md) | 1–7 | P1 | M | 001–008、010、011 | TODO |
| 010 | [roster 的 subagent 行显示的任务就是卡片上的任务](010-roster-row-shows-the-card-task.md) | 6 | P1 | S | — | TODO |
| 011 | [因用量上限中断的 subagent 在额度恢复后自动继续](011-subagent-auto-continue.md) | 7 | P1 | M | 005、007、008 | TODO |

010、011 是 owner 在写计划过程中追加的第 6、7 点，编号沿用追加顺序，不重排。**推荐的执行顺序**：001 → 010 → 002 → 003 → 004 → 005 → 006 → 007 → 008 → 011 → 009。

状态取值：TODO | IN PROGRESS | DONE | BLOCKED（附一句原因）| REJECTED（附一句理由）

"SRT-002""SRT-003"指 `docs/plan/SCHEDULED_RUNS_TRANSCRIPT/002-session-birth-identity.md` 和 `003-conversation-lists-by-purpose.md`。

## 依赖说明

- **002 依赖 SRT-002、SRT-003。** worker 会话要在出生时就带上用途（`source='subagent'`）、父会话和"不参与记忆"的身份，并且不能出现在任何对话列表里。SRT-002 引入 `SessionBirth` / `EnsureAt`，SRT-003 把列表改成在 SQL 里按用途过滤。`SessionBirth.Source` 字段由 SRT-005 引入；如果 002 开工时 SRT-005 还没做，002 第 1 步照 SRT-005 的原文加上这个字段（见 002）。
- **003 依赖 002。** 压缩要有落库的历史可压；回合前自动压缩读的是 worker 会话的行。
- **004 不依赖其它计划**，但它改的是 TUI 主视图的队列，风险最高，建议在 001 之后、005 之前单独做完并真机验证。
- **005 依赖 002、004。** 用户给 subagent 的消息要写进它的 worker 会话（002），队列用 004 统一后的 `run.InputQueue`。
- **006 依赖 002、003。** subagent 的占用读它 worker 会话的最后一次 API 用量；"这个 agent 跑在哪个模型上"的解析函数 `run.AgentModel` 由 002 提供；压缩阈值的口径由 003 定下，footer 的百分比必须和它一致。
- **007 依赖 001、003、005、006**；**008 依赖 003、005、006，并以 007 定下的交互为标准**（TUI 是语义标准）。
- **011 依赖 005、007、008。** 到点时经 005 的 `SendToSubagent` 继续；提示和 Esc 取消显示在 007/008 建好的 subagent 视图里。
- **009 最后做**，覆盖 001–008、010、011 的全部验收。

## owner 已定的决策（全部纳入本期）

| 编号 | 问题 | 选项 | 推荐 | 结论 |
| --- | --- | --- | --- | --- |
| D1 | subagent 视图里 Esc 的语义 | A 有在途输入才作用于该 subagent：撤回窗口内撤回刚发给它的消息；有发给它的待投递 steer 时中断它并立即发送；其余情况返回主视图；Esc 永不直接取消 subagent / B 完全照搬主视图（无待投递时 Esc 取消 subagent）/ C Esc 只负责返回 | A | **A**（owner 2026-10-04） |
| D2 | 哪些 subagent 能在视图里直接对话 | A 所有公开类型（含一次性类型），内部保留类型只读 / B 沿用 OneShot 限制 | A | **所有类型，包括一次性类型和内部保留类型（plan-reviewer、goal-evaluator）**（owner 2026-10-04："因为网络原因等非人为的原因造成的中断都必须支持用户直接跟该subagent对话，让该subagent继续执行"）。`OneShot`/`Continuable` 只约束模型调用 `subagent_continue`。 |
| D3 | 用户在 subagent 视图里驱动它继续后，主 agent 是否被告知 | A 不注入，主 agent 用 `subagent_status`/`subagent_wait`/`subagent_list` 自行查询 / B 在主会话尾部追加通知 | A | **A**（owner 2026-10-04） |
| D4 | subagent 视图里斜杠命令和 `!shell` 的作用范围 | A 三分类（作用于该 subagent / 照旧全局 / 隐藏并一句话提示）/ B 只开放 /compact / C 只开放 /compact，其余全拒 | A | **A**（owner 2026-10-04），逐条分类表见 007 |
| D5 | worker 会话是否参与记忆抽取 | A 不参与（subagent 做的事已由主会话里它的结论和卡片代表；参与会让后台为每个 subagent 多跑一次抽取模型调用）/ B 参与 | A | **A：不参与**（owner 2026-10-04 按推荐批准；与 SRT 的 D6"定时任务对话不参与"同理） |
| D6 | fork subagent 继续时用什么前缀 | A 原样重放它上次请求的 system 和消息（冻结 system、消息落库），前缀与它上次请求逐字节相同 / B 改走 typed 路径重建前缀 | A | **A**（owner 2026-10-04 按推荐批准；B 会让 fork 继续时的整段前缀失效，违反缓存铁律） |
| D7 | 统一队列语义（计划 004）是否纳入本期 | 按"顺带发现的既有缺陷必须纳入本期"的规矩，只能问"怎么修"：以 TUI 为准把语义收进 `run.InputQueue`，TUI 主视图和网页都只用它 | — | **纳入本期**（owner 2026-10-04 按推荐批准；工作量 L、风险高，单列为计划 004。网页上带附件的消息在运行中改为作为 steer 投递，与 TUI 一致，此变化一并批准） |
| D8 | subagent 视图里有待触发的自动继续时 Esc 做什么 | 由 owner"对齐 primary agent 的语义"和 D1 的原则推出：主视图里 Esc 取消待继续；subagent 视图里按"撤回 → 中断并发送 → 取消自动继续 → 返回主视图"的优先级 | — | **按推导的优先级执行**（owner 2026-10-04 按推荐批准，见 007、011） |
| D9 | 斜杠命令三分类里由推导得出的 9 个命令 | `diff`、`subagents`、`skills`、`connect`、`memories`、`migrate` 在 subagent 视图里照旧全局执行；`rename`、`agent`、`fast` 在 subagent 视图里隐藏并一句话提示（依据：是否改变主会话） | 按左列 | **按左列执行**（owner 2026-10-04 按推荐批准，见 007） |

## 全局规则（每份计划都适用，执行者必须遵守）

1. **不要提交代码。** 所有改动留在工作区，由 owner 手动审阅提交。不要运行 `git commit`、`git merge`、`git rebase`，也不要做任何改写历史的操作。
2. **缓存命中率只能升不能降，这是最高优先级。** 任何改动都不得改变会话在对话之前的那段前缀（工具表、system、对话前注入的开发者指令），也不得在会话中途改变它；新内容只能追加在消息尾部。每份计划都有"缓存影响"一节，执行者必须按它留下前后对比的证据（逐字节金样测试，或 `usage_json` 算出的命中率 `CacheRead / (CacheRead + CacheCreation + Input)`）。
3. **修根因，不打补丁。** 不加只为掩盖症状的 nil 判断、兜底、重试或 recover。真正来自外部的输入（用户输入、模型响应、文件系统、网络）除外。
4. **每个包最多 20 个生产文件。** `pkg/run`、`pkg/turn`、`pkg/tui`、`pkg/process`、`pkg/gateway`、`pkg/state`、`pkg/tool`、`pkg/llm` 现在**都正好是 20 个**（2026-10-04 核实），所以**不得在这些包里新建任何生产 `.go` 文件**，新代码只能写进已有文件。`pkg/event` 有 18 个、`pkg/agent` 有 12 个、`pkg/assembly` 有 18 个，但本计划也不在里面新建文件。测试文件必须以它覆盖的生产文件命名（`pkg/architecture` 的 `TestTestFilesCorrespondToProductionFiles`）。
5. **包的扇出只能减少，`run.Runner` 只能变小。** 不得给任何包新增仓库内部包的 import（`TestFanOutOnlyShrinks`，`pkg/architecture/cache_test.go:169-218`）；`pkg/run`、`pkg/turn` 互不 import（`TestLayer3PackagesDoNotImportEachOther`）。`run.Runner` 现有 **39 个导出方法、25 个字段，都正好是上限**（`runnerMethods = 39`、`runnerFields = 25`，`cache_test.go:849-850`）：**不得给 `Runner` 新增导出方法或字段**。引擎对外的新入口一律写成包级函数，第一个参数是 `*Runner`（先例：`run.PrimaryModel(r)`、`run.SubagentOwnModel(r, …)`、`run.CompactionService(r, …)`）。
6. **TUI 是语义标准，共享的一律下沉到引擎。** 两个 surface 都需要的语义只实现一份，放在 `pkg/run` / `pkg/turn` / `pkg/process`，TUI 和 gateway 都调用它；冲突时以 TUI 为准。gateway 只保留传输层的事。
7. **死代码要删干净。** 开工前记录基线：`$(go env GOPATH)/bin/deadcode -tags fts5 ./... > /tmp/deadcode-before.txt`；每份计划完成后输出必须与基线一致（不得新增条目）。
8. **界面文案只写一句话。** 中英文同时加在 `frontend/src/locales/index.ts`（键名一致）。任何界面都不得显示原始 JSON。Go 要告诉网页的话带稳定代码，由网页用查看者的语言写出句子；终端照旧用英文句子。TUI 内容文本不得用 dim/faint 低亮度配色，不得用粉色/品红。
9. **subagent 的一切都属于它自己的视图。** 它说的、被告知的、关于它的确认，都只出现在它的视图里；主视图只有它的生命周期卡片。新加的任何"属于某个 agent"的帧或事件都必须带 roster key（`agent.RosterKey(taskID, agentType)`），路由按"是否带 agent"判断，不按帧类型白名单判断。
10. **不碰 `pkg/gateway/dist`。** 不要运行 `make ui` 或在 `frontend` 里运行 `pnpm build`（`frontend/vite.config.ts:50` 的 `outDir` 指向 `pkg/gateway/dist`）。网页真机验证用 `scripts/acceptance/web_e2e.sh`，它构建到临时目录。
11. **真机证明。** 涉及模型行为或屏幕呈现的改动，必须在运行中的 TUI（tmux，`.claude/skills/run-forebrain/driver.sh`）和网页上证明；单测和假模型只证明"做出来了"，不证明"真的这样工作"。真实模型用智谱：密钥只在 `~/.forebrain/e2e-zhipu.env`（变量 `FOREBRAIN_E2E_ZHIPU_KEY`，权限 600），配置里只能写 `${FOREBRAIN_E2E_ZHIPU_KEY}` 引用，任何文件、日志、截图说明里都不得出现密钥的值。缓存命中率的真机测量只要求 DeepSeek 和 OpenAI；本机没有凭据就记"凭据缺失，已跳过"，不要停下等人。
12. **状态库结构不变。** 本计划不新增表、不改列，不需要迁移。如果执行中发现必须改结构，按 STOP 处理，回来问；改了结构就必须迁移用户已有的旧库。

## 常用命令（2026-10-04 在本机核实过的形式）

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| 格式 | `gofmt -l pkg cmd` | 无输出 |
| 静态检查 | `go vet -tags fts5 ./...` | 退出码 0 |
| 单包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '<Name>' -count=1` | `ok` |
| Go 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 全部 `ok` |
| 竞态 | `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/run ./pkg/tui -run '<Name>' -count=1` | `ok`，无 `DATA RACE` |
| 架构约束 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` | `ok`（2026-10-04 基线通过） |
| 包依赖图 | `scripts/package-graph.sh && git diff --exit-code pkg/architecture/testdata/graph.json` | 退出码 0 |
| 死代码 | `$(go env GOPATH)/bin/deadcode -tags fts5 ./...` | 与开工前的基线一致 |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 前端类型检查 | `cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` | 退出码 0 |
| TUI 真机（假模型） | `.claude/skills/run-forebrain/driver.sh`（用法见 `.claude/skills/run-forebrain/SKILL.md`） | 见各计划 |
| 网页真机（假模型） | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |
| 网页真机（智谱） | `FOREBRAIN_E2E_REAL_LLM=1 scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |

本机磁盘余量很紧（2026-10-04 只剩约 4–5 GiB）。不要复制仓库或 Go 构建缓存；临时验证用 `go test -overlay` 或 scratch 目录里的小文件。

## 与并行工作的关系

写本计划时，工作区里有一大批**不属于本计划**的未提交改动（`git status` 显示 `pkg/run/runner.go`、`pkg/run/config.go`、`pkg/run/factory.go`、`pkg/process/open.go`、`pkg/process/runner_pool.go`、`pkg/tui/chat_session.go`、`pkg/tui/run.go`、`pkg/tui/render.go`、`pkg/turn/*`、`pkg/tool/*`、`frontend/src/*` 等，以及新目录 `pkg/lsp/`、`docs/plan/lsp/`、`docs/plan/SCHEDULED_RUNS_TRANSCRIPT/`）。它们来自 LSP 计划和定时任务计划。执行前：

- 先确认那些改动已经由 owner 提交，或者已经放弃。不要在别人的半成品上叠改，也不要动、不要还原不属于本计划的部分。
- 本计划摘录的代码是 2026-10-04 工作区里的样子（含上述未提交改动）。漂移检查列出文件时，按内容（函数名、注释原文）核对摘录，不按行号核对；对不上就按 STOP 处理。

## 考虑过但不做的

- **把 subagent 账本（`<workspace>/state/subagent-history.jsonl`）和进程内注册表搬进状态库，以支持 gateway 多副本。** 不在本期。gateway 集群化的前提是状态库本身可以跨副本共享（今天是本地 SQLite），这是全局性的前置工作，不属于 subagent 对话这个特性。本期让 subagent 的对话历史和主会话一样落在状态库里，已经是"与主会话同等持久"的最好结果；账本的位置不变，不会让集群化更难。
- **把 fork 旁路 JSONL（`SidechainFilePath`）删掉。** 不做。`SubagentStop` 钩子通过 `hook.SidechainTranscriptPath` 读它，它是钩子的输入契约，不是对话历史的来源；002 之后对话历史的唯一来源是 worker 会话。
- **subagent 视图里的 `ctrl+c`。** 不改。它今天在任何视图都作用于主运行和 composer；owner 这次没有提出，D1 也只定了 Esc。
