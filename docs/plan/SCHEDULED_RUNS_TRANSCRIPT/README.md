# 心跳与定时任务成为真正的对话：实施计划索引

由 improve skill 于 2026-10-03 生成，基线提交 `1a6d708`（工作区里另有一组不属于本计划的改动，见"与并行工作的关系"）。**状态：D1–D9 已由 owner 定夺（见决策表），整套计划待 owner 审批，未批准前不得改代码。**

## owner 的原始要求（2026-10-03，逐字）

- "what's your suggestion about scheduled (cron) runs"——我给出的建议是：心跳是缺陷（答复被丢弃、什么都不落库），定时任务每次触发应成为一段完整的、标明来源的对话。
- "/improve plan 书面方案, 两项都做, 心跳优先"
- "D8必须写进本期计划。D1-D7按你的推荐继续设计和计划"
- "D9：要设置保留上限，默认30天，必须支持在forebrain.yaml里可配置"；"web端也必须支持配置"

## 一句话结论

心跳和定时任务今天都走 `RunAgentOnceExec` 这条"一次性"旁路：它在 agent 级基础 runner 上直接调 `run.Run`，**不写对话记录、不发事件、不进审批闸门、不受运行控制器追踪，也不跑输入/输出护栏**。所以心跳的答复被丢掉了，定时任务只剩一行文本输出；而且每次触发都留下一个空会话，挤在对话抽屉和 `/resume` 里。
本期把两者都改走 gateway 已有的"无人值守回合"路径，也就是自动继续（auto-continue）用的那条 detached run。这样它们和网页上的一个回合完全一样：落库、流式推送、可停止、可审批、有 "Worked for" 收尾行，重放也一致。
同时补上一个更底层的缺口（D8）："一个运行是否还活着""一个会话里是否已经有活着的回合"今天只存在进程内存里。进程崩溃后运行永远停在执行中，TUI 和 gateway 也可以同时往同一个会话里跑回合。计划 004 把这两件事落到共享状态库上：每个进程一个续租的属主，过期即回收；建主运行时一条原子语句保证会话独占。

## 执行顺序与状态

| 计划 | 标题 | 优先级 | 工作量 | 依赖 | 状态 |
| --- | --- | --- | --- | --- | --- |
| 001 | [心跳成为它所在对话里的真实回合](001-heartbeat-turns.md) | P1 | M | — | DONE |
| 002 | [会话的目录身份在出生时由创建者给定，不再改写全局默认值](002-session-birth-identity.md) | P1 | S | — | DONE |
| 003 | [会话列表按用途在 SQL 里过滤，标题直接按 id 读取](003-conversation-lists-by-purpose.md) | P1 | M | — | DONE |
| 004 | [运行的存活由进程租约证明，一个会话同一时刻只有一个活着的主运行](004-run-liveness-and-session-exclusivity.md)（D8） | P1 | L | 001 | DONE |
| 005 | [定时任务的每次触发都是一段完整的对话](005-cron-fire-conversations.md) | P1 | L | 001、002、003、004 | DONE |
| 006 | [定时任务对话的保留期——默认 30 天，forebrain.yaml 与网页都能配置](006-cron-conversation-retention.md)（D9） | P1 | L | 004、005 | DONE |

状态取值：TODO | IN PROGRESS | DONE | BLOCKED（附一句原因）| REJECTED（附一句理由）

002 和 003 是写方案时顺带发现的既有缺陷。按项目规矩，它们作为正式任务纳入本期，不记成"已知问题"：

- **002**：创建项目会话的接口改写了会话存储的全局默认 cwd。此后这个 gateway 进程新建的每个会话（普通对话、渠道会话、定时任务会话）都被记在那个项目名下，记忆抽取因此会串到别的项目。这里还有一个数据竞争。
- **003**：对话抽屉和工作坊列表先取最近 200 个会话，再在前端过滤；项目会话在 Go 里事后跳过。会话一多，普通对话就被挤出列表。另有三处"按 id 找标题"的代码靠扫最近 N 条列表来找。定时任务每次触发都会多出一个会话，这几个问题会立刻变严重。

## 与并行工作的关系

写本计划时（2026-10-03），工作区里有一组**不属于本计划**的未提交改动，来自语言服务器计划 `docs/plan/lsp/`：`pkg/lsp/`、`pkg/config/lsp.go`、`pkg/event/lsp.go`、`pkg/process/open.go`、`pkg/process/runner_pool.go`、`pkg/process/config_reload.go`、`pkg/run/runner.go`、`pkg/run/factory.go`、`pkg/tool/search.go`、`pkg/architecture/*` 等。本计划的基线仍是 `1a6d708`。执行前：

- 先确认那组改动已经由 owner 提交，或者已经放弃。不要在别人的半成品上叠改，也不要动、不要还原那些文件里不属于本计划的部分。
- 漂移检查列出 `pkg/process/open.go` 等文件时，用 `git log` 确认改动来自 LSP 计划，再按内容（而不是行号）核对本计划摘录的那几段。
- `pkg/event` 因 LSP 计划多了一个生产文件（18 个），本计划不在 `pkg/event` 新建文件，不受影响。

## 依赖说明

- 004 依赖 001：004 把 001 在 `startDetachedTurn` 里的进程内预检（`Find` + `ParkedOnApproval`）换成数据库里的原子规则，并删掉 001 为单进程正确性加的"`Submit` 拒绝"分支。001 先单独做对（owner 指定心跳第一），004 再把同一条规则推广到跨进程。
- 005 依赖 001：共用 `startDetachedTurn` 和"消息来源"标记（`state.PartTypeOrigin`）。
- 005 依赖 002：绑定项目的定时任务，其会话要用 `SessionStore.EnsureAt` 以项目根目录出生；会话用途 `cron` 也在出生时写入。
- 005 依赖 003：定时任务会话的用途是 `cron`，003 让所有对话列表在 SQL 里排除它，并用直接查询取标题。
- 005 依赖 004："这次触发结束了吗""上一次触发还活着吗"都读 004 的存活规则；崩溃留下的触发由 004 的回收器收尾。迁移版本也按顺序排：004 是 v5，005 是 v6。
- 006 依赖 004、005：只删除 005 产生的 `source='cron'` 会话，并用 004 的"活着的主运行"规则保证不删正在进行的对话。
- 执行顺序：001 → 002 → 003 → 004 → 005 → 006。002、003 与 001 互不依赖，但仍按编号执行，避免同一批文件的改动交错。

## 需要 owner 拍板的决策

| 编号 | 问题 | 选项 | 推荐 | 结论 |
| --- | --- | --- | --- | --- |
| D1 | 心跳触发的那条提示在对话里怎么显示 | A 标明"由心跳发送"（网页在气泡上加一行标签，终端重放画成标题为 `heartbeat` 的系统卡片）/ B 和用户亲手输入的消息一模一样 | A | **A**（owner 2026-10-03） |
| D2 | 心跳间隔从哪一刻算起 | A 从本次心跳**开始**算（启动即返回，运行中到点则跳过，不叠加）/ B 从本次心跳**结束**算（今天的行为，要求调度器阻塞等回合结束） | A | **A**（owner 2026-10-03） |
| D3 | 定时任务每次触发的对话在哪里能找到 | A 只从定时任务页（含项目空间的定时任务标签）的"执行记录"里点"打开对话"进入；对话抽屉、项目会话列表、网页和终端的 `/resume` 选择器都不列出 / B 也列在抽屉和 `/resume` 里 | A | **A**（owner 2026-10-03） |
| D4 | 定时任务触发后停在审批上怎么办 | A 运行保持暂停，等用户在对话里批准；执行记录保持"执行中"直到运行真正结束；同一任务在此期间不再触发；若任务配置了投递渠道，先投递一句"正在等待审批"的提示（与渠道消息停在审批时的提示同一句话）/ B 直接记失败 | A | **A**（owner 2026-10-03） |
| D5 | 删除定时任务时，它历次触发的执行记录和对话怎么办 | A 保留（这正是 `fb_cron_runs` 表注释写明的设计"执行历史刻意比任务活得久"）；任务删掉后这些对话只能凭会话 id 打开（如 `forebrain resume <id>`）/ B 一并删除 | A | **A**（owner 2026-10-03） |
| D6 | 定时任务对话是否参与记忆抽取 | A 不参与（每次触发都会让后台多跑一次一阶段抽取的模型调用，产出的又多是重复的报告内容）/ B 和普通对话一样参与 | A | **A**（owner 2026-10-03） |
| D7 | 定时任务触发撞上用量上限怎么办 | A 本次记失败（附上已解释过的原因），不自动继续，下一次按计划照常触发（和渠道对话一样：没人看着，不在背后续跑）/ B 像网页对话一样等额度恢复后自动继续 | A | **A**（owner 2026-10-03） |
| D8 | 跨进程/跨副本的会话互斥与运行存活检测（例如 gateway 崩溃后，`fb_runs` 和 `fb_cron_runs` 里的 `running` 记录永远停在执行中；TUI 和 gateway 共用同一个 FOREBRAIN_HOME 时，两边可能同时往一个会话里跑回合） | A 另起一份计划 / B 本期一起做 | A | **B：本期做**（owner 2026-10-03："D8必须写进本期计划"），见计划 004 |
| D9 | 定时任务对话的保留上限 | A 本期不做 / B 本期做 | A | **B：本期做，默认 30 天，`forebrain.yaml` 的 `cron.retention_days` 与网页设置页都能配置**（owner 2026-10-03/04），见计划 006 |


## 全局规则（每份计划都适用，执行者必须遵守）

1. **不要提交代码。** 所有改动留在工作区，由 owner 手动审阅提交。不要运行 `git commit`、`git merge`、`git rebase`，也不要做任何改写历史的操作。
2. **缓存命中率只能升不能降。** 任何改动都不得改变会话在对话之前的那段前缀（工具表、system、注入在对话前的开发者指令），也不得在会话中途改变它。新内容只能追加在消息尾部。每份计划都有"缓存影响"一节，执行者必须按它的测量步骤留下前后对比数字。
3. **修根因，不打补丁。** 不加只为掩盖症状的 nil 判断、兜底、重试或 recover。真正来自外部的输入（用户输入、模型响应、文件系统、网络）除外。
4. **每个包最多 20 个生产文件。** `pkg/turn`、`pkg/state`、`pkg/gateway`、`pkg/process`、`pkg/tui`、`pkg/run`，以及加上 LSP 计划的 `lsp.go` 之后的 `pkg/config`，现在**都正好是 20 个**，所以**不得新建任何生产 `.go` 文件**，新代码只能写进已有文件。测试文件必须以它覆盖的生产文件命名（`pkg/architecture` 的 `TestTestFilesCorrespondToProductionFiles` 会检查）。
5. **包的扇出只能减少。** 不得给 `pkg/gateway`、`pkg/tui`、`pkg/run` 新增任何仓库内部包的 import（`TestFanOutOnlyShrinks`）。`run.Runner` 的导出方法数不得增加（`TestRunnerOnlyShrinks`）。
6. **死代码要删干净。** 用 `$(go env GOPATH)/bin/deadcode -tags fts5 ./...` 查。基线输出只有既有的 `NewRuntimeWithStore` 一行，每份计划完成后的输出必须和基线一致。
7. **界面文案。** 所有面向用户的文案都只写一句话，中英文同时加在 `frontend/src/locales/index.ts`（中文在前一半，英文在后一半，键名一致）。任何界面都不得显示原始 JSON。运行时（Go）要告诉网页的话，一律带一个稳定的代码（`event.TurnErrorDetail.Code`、`turn_withdrawn` 的 `data.code`、执行记录的 `error_code`），由网页在绘制时用查看者的语言写出句子（`frontend/src/lib/providerError.ts` 的 `formatProviderError`）；Go 拼的英文句子只作不认识代码时的兜底。终端和渠道消息照旧用英文句子。
8. **新配置项必须同时有网页表单。** 加进 `forebrain.yaml` 的任何键，都要在设置页有经过校验的表单，能显示生效值（含默认值）；只靠 YAML 编辑器不算完成。
9. **不碰 `pkg/gateway/dist`。** 真机验证用的前端由 `scripts/acceptance/web_e2e.sh` 构建到临时目录。
10. **密钥。** 真机验证用的智谱密钥只存在 `~/.forebrain/e2e-zhipu.env`（变量名 `FOREBRAIN_E2E_ZHIPU_KEY`，权限 600）。任何仓库文件、计划、日志、截图说明里都不得出现密钥的值；配置里只能写 `${FOREBRAIN_E2E_ZHIPU_KEY}` 这种引用（参照 `scripts/acceptance/web_e2e.sh:78-98`）。
11. **真机证明。** 涉及模型行为或屏幕呈现的改动，必须在运行中的 TUI（tmux）和网页上，对着真实模型（智谱 `glm-5.3-flash`）证明，单测和假模型不够。缓存命中率的真机测量只要求 DeepSeek 和 OpenAI。本机若没有这两家的凭据，就记为"凭据缺失，已跳过"，不要因此停下等人；智谱的数字照记，作为参考。

## 常用命令（已在基线核实）

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| 格式 | `gofmt -l pkg cmd` | 无输出 |
| 静态检查 | `go vet -tags fts5 ./...` | 退出码 0 |
| Go 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 全部 `ok` |
| 架构约束 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` | `ok` |
| 死代码 | `$(go env GOPATH)/bin/deadcode -tags fts5 ./...` | 与基线一致（只有 `NewRuntimeWithStore`） |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 前端类型检查 | `cd frontend && corepack pnpm exec vue-tsc --noEmit -p tsconfig.json` | 退出码 0 |
| 网页真机（假模型） | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |
| 网页真机（智谱） | `FOREBRAIN_E2E_REAL_LLM=1 scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |

## 考虑过但不做的

- **给自动继续（auto-continue）的那条提示也加"来源"标签。** 不做。那条提示是运行时自己写的固定文本（"The previous response was interrupted by a usage limit…"），字面上已经说明了来历；`pkg/turn/submit.go:170-178` 也明确写了它要作为一个真实回合出现在对话里。心跳不同，它的提示是用户早先写下的任意文本，没有标签就会像用户刚刚又打了一遍。
- **心跳和定时任务记录运行 id 到新列，用 run id 而不是 session id 去匹配完成事件。** 不做。定时任务会话是专属会话，执行记录在运行开始前就已写入 session id，按 session 匹配没有竞态。按 run id 匹配则要求运行创建后、启动前再回写一次，否则一个瞬间失败的运行会找不到自己的记录。
- **修复历史数据里被 002 的缺陷记错 cwd 的会话。** 做不到。历史上无法区分"gateway 本来就在这个项目目录启动"和"被别的项目会话改写了默认值"，没有可靠的判别依据，所以不改历史行，只保证今后不再发生。
