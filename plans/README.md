# Forebrain Harness 改进计划索引

本目录包含由 `improve` skill 生成的代码改进计划。每个计划都是自包含的实施指南，可由其他执行器模型独立完成。

## 最新审核信息

- **2026-10-13 阻塞：执行者额度用尽**。子代理派发返回 `429 您已达到每周/每月使用上限，限额将在 2026-10-15 15:19:57 重置`，并行执行通道暂时关闭；评审方改在主线程接手。**剩余计划（根 001/006/014–021/024/030/035/037/038–042 与 web 017/018/020）在额度恢复前无法派发。**
  - **036 落地情况**：一次派发在运行中被中断，但它已把 Step 1–6 基本写完（`pkg/skill/versions.go` 1026 行 + 9 个测试含发布原子性故障注入、`service.go` 的 bootstrap/安装即 v1、`git_install.go` 上游记录、两个内置 SKILL.md 改写、`LoadedSkill.Version/VersionsRoot`、工具侧草稿重定向、gateway 主代理层与项目层各 7 条版本路由）。评审方复核时发现两点并**根因修掉**：一是 `TestWorkshopSkillFilesListReadAndWrite` 的草稿路径多退了一级（代码按计划用 `<技能根>/.drafts/<名称>`，测试写成 `<工作区>/.drafts/…`）；二是同一测试的读回断言用子串匹配 JSON 转义后的响应体，改为解码后 `JSONEq`。修后**全量 Go 测试 29 包全 ok、前端 397 用例 + vue-tsc 干净、fake 全量 e2e 114 passed/2 skipped/0 failed**。
  - **待复核（评审缺执行者报告）**：STOP 条件 2 的提示缓存证明（`Version` 字段是否只进会话私有状态、不动冻结前缀）、STOP 3/4 的判定、上游更新检测与 `PullUpdate`、`pkg/turn/skills.go:52` 那处读取是否也应走版本目录（现用于斜杠命令发现，读正式副本）。
- **同批续（2026-10-12）**：**033** 执行完毕、评审 **APPROVE**。迁移 **v10**：新表 `fb_file_changes`/`fb_rewinds`，`fb_messages.visibility` 增加 `'rewound'`（走 rename-aside 重建，我逐字核对重建 DDL 与 `schema.sql` 一致、列序一致、两个索引重建、无触发器、`legacy_alter_table` 用法正确）。事件过滤收口为 `state.RewindEventsPredicate`（7 个展示读取方全部接入，插入去重那处按设计不接）；`sequence` 是 `AUTOINCREMENT` 故序号不会复用，过滤高水位与分页谓词一致、无空洞。web 与 TUI `/rewind` 共用 `pkg/turn/rewind.go`；`rewind-last`/`LatestRewindableToolCall`/`LatestSnapshot` 全部删除（我的独立 grep 为空）。fake 全量 e2e **114 passed / 2 skipped / 0 failed**。
  - **打开新构建时会一次性迁移真实库**（我在 4.3GB 真库上核对：`user_version=9`、`freelist_count=0`、磁盘余量 4.6GiB）：迁移本体约 45s，框架随后的 `VACUUM` 需再写一份约 3.6GB 的临时文件（峰值 original+temp ≈ 7.9GB，但 VACUUM 写的是新增占用，4.6GiB 余量下约剩 1.3GB 空间，能过但很紧）。迁移在 VACUUM **之前**已 COMMIT，且 VACUUM 只在版本变化时执行——所以万一磁盘满，只会让**那一次**启动失败，重试即跳过。**建议先腾出磁盘空间再启动**。
- **同批后续（2026-10-12）**：**022** 执行完毕、评审 **APPROVE**（文件视图唯一的根解析 `pkg/gateway/session_files.go` 的 `filesRoot`/`resolveFilesPath`；旧 `mentionRoot`/`resolveWorkspacePath` 删除；越界检查仍走 `tool.ResolveWithinRoots`，新增用例覆盖项目根、工坊技能目录、无项目会话三种根与符号链接逃逸）。**web 016** 执行完毕、**APPROVE**：016 的 STOP 条件 3（审批入口）由 owner 裁决「审批进消息流」后化解——`ApprovalCard.vue` 改为可交互（承接 `PendingActionsPanel` 全部能力，testid 与行为零丢失、`exit-plan-approval.spec.ts` 未改且全绿），另加顶栏注入、composer 五个 chip、`McpNotice`、会话模型接口。**web 019** 执行完毕、**APPROVE**（Crepe + CodeMirror，monokai-light 单一调色板；`vaul-vue` 的 UNKNOWN 许可证是既有依赖）。**执行中发现并根因修掉的既有缺陷（无计划文件）**：记忆管理器把记忆仓库自身的 git 元数据当记忆文件——`collectMemoryFiles` 列出 `.git/config`、`.git/objects/*`（真实 home 里 41 个普通文件），`resolveMemoryFilePath` 也只查包含性，于是读/写/删接口都能改到仓库簿记。修法：gateway 侧与代理侧对齐，根下点号前缀条目一律隐藏（权威规则在 `pkg/memory/backend.go:511`），列表与路径解析共用一条规则。**e2e harness**：内存后台管线在首个回合写入记忆根（脚手架文件 + `.git`）并污染记忆 spec 的种子隔离；harness 两个配置分支改为 `features: memories: false`（只关 `generate_memories` 不够——启动管线由特性开关闸住）。
- **项目模型一批（2026-10-11/12，owner 指令「按 plans/README.md 顺序派发」）**：025 → 026 → 027 → 028 → 029 全部执行完毕、改动未提交留在工作区，评审 **全部 APPROVE**。执行中发现并**本期根因修掉**两处 027 引入的回归：① 计划 043（新建，原编号 038 与另一会话冲突）——027 去掉 gateway 启动项目后，`s.projectKey()`/`env.projectScope()` 恒为空，与项目 runner 的 `ProjectKey` 分叉，web 计划文件/审批/记忆/cron 会话全坏（我独立复现：`cron-fire` ×2 + `exit-plan-approval` ×2 失败）；修法是会话级 `SessionProjectKey`/`sessionProjectKey`，删掉 `Server.projectKey()` 与 `Environment.projectScope()`。② 池条目缺线（无计划文件，定点修复）——`buildEntryLocked` 手搓 runner 没装 `FileResolver`/`SubagentExecutor`，gateway 项目会话里 `subagent_run` 直接失败（`subagent requires an in-process executor`）；改为一处共用 `Environment.wireRunner`，`Open` 与 `buildEntryLocked` 都调它。评审侧独立复跑全量 web e2e：**95 passed / 2 skipped / 0 failed，`web e2e: PASS`**（基线 87/5/2）。028 与 029 **未并行**：两者共用 `pkg/gateway/api_extra.go`（028 删 `validateProjectRoot`、029 加端点），同一工作树并行会互相覆盖，改为串行。web 015 也已完成（同一批，见 web 目录 README）；web 019 尚未开始。
- **审核日期**：2026-10-10
- **基准 commit**：9656051（工作区含 owner 未提交改动：`pkg/tui/notify.go` 的 `markEventShownOnce` 等，与 007 无关）
- **审核类型**：exit_plan_mode 审批期间 TUI 出现假 "Exited plan mode" 卡、取消后多一张 "Canceled" 卡的根因定位与修复（`plan` 模式，单计划 007）
- **问题报告**：owner 报告（附截图）"running plan-review subagent 时并没有退出 plan 模式，但是显示了 Exited plan mode 消息卡片"；"按下 ctrl+c 取消审批后，已经显示了 You canceled forebrain's request to exit plan mode，不需要重复显示 Canceled 消息卡片"
- **结论（007）**：两个 bug 同一根因——主 agent 的工具步骤经 `runAuditStepHook → notifyToolStepHooks` 直接进 UI，不经 d501e09 加了过滤的 `publishRunEvent` 漏斗，gate 的等待帧被画成卡（标题落进 "Exited plan mode"），取消时被 `FinalizePendingTools` 改成 "Canceled"。去掉假卡会暴露确认行"插到最后一张工具卡之前"的落位错位（回放今天已错位，真机复现），所以 007 一并修 live 与回放的落位。web 同源错位未纳入，待 owner 决定是否另立计划
- **同日追加（008）**：owner 要求另写 web 同步计划。web 除了同源的落位错位，还会丢行（同一 gate 的多条决定只留最后一条，review 交付会抹掉 "You asked … review"）、重载时 review 卡落到整轮末尾。008 用不渲染的 gate 锚点块标记无卡调用的位置，exit gate 的决定行逐条插在锚点之前
- **008 执行（2026-10-10，owner 指定 worktree）**：已派发执行者在独立 worktree `../forebrain-harness-008`（detached 于 9656051）完成，评审 **APPROVE**。5 条 Done criteria 评审侧复跑全过（326 tests 全绿 / vue-tsc 无输出 / build 成功 / grep gateStepIds 与旧注释均无输出）；diff 逐段对照计划、范围干净（4 个 in-scope 前端文件 + `pkg/gateway/dist` 构建产物）；两处申报偏差均接受（`messageHasBubble` doc 注释同步一句；计划第 11 条"live 与重载各一遍"拆成两个用例，新增共 7 个）。**改动未提交、留在 worktree，由 owner 决定并回方式**（主树 ChatView.vue / WorkshopView.vue 有 owner 未提交改动，并回时注意）；007（TUI 侧）仍 TODO，两端应一起交付。
- **007 执行（2026-10-10，owner 指定 worktree）**：已派发执行者在独立 worktree `wt/improve-007-exit-gate`（`$TMPDIR/fb-007-wt`，基于 9656051）完成，评审 **APPROVE**。全部 Done criteria 评审侧独立复跑通过（8 个新/改写测试 PASS、`./pkg/... ./cmd/forebrain` 全绿、vet/gofmt 干净、graph.json 无 diff、grep 项全过）；diff 逐段对照计划（9 个 in-scope 文件 + 2 个勘误文档，无 import 增删）；driver 真机 A–F 全过（A/B/D 假卡与 Canceled 卡为 0、C 回放同序、E review 落位与批准卡正确、F 拒绝带反馈 live/回放一致）。三处申报偏差均接受（test 8 首匹配调整——回放合并 assistant 文本为 9656051 既有行为，非本改动引入；场景 C 的 "Worked for" 行落位为既有 run_id 缺失投影；channels.go 注释补两句）。**改动未提交、留在 worktree，由 owner 决定并回方式**（主树 `pkg/tui/notify.go` 有 owner 未提交的 `markEventShownOnce` 改动，并回时注意；`$TMPDIR` 重启会清空，尽快落地）。待 owner：用真实模型复核一次成功 review 路径（review 期间无 "Exited plan mode" 卡、交付后修订与新 gate 依次出现在 review 卡之后）。
- **007 branch 审核（2026-10-10，第二轮，`/improve branch` 针对 worktree `$TMPDIR/fb-007-wt`）**：逐段复核 007 实现，发现 1 处语义缺口 + 1 处文档落位问题，立计划 009 并派执行者在同一 worktree 叠加修复。① **回放未封存发出无卡 gate 调用的那段回答**（探针实证）：live 里 gate 的 running 步骤进 reducer 时封存文本，回放没有这条步骤的行（等待不画卡、取消/交付结果行被丢弃），owner 截图 1 的流程（review → 交付 → 修订 → 批准）回放成"残留 non-final 的 `here is the plan` 块 + 粘连块 `here is the planrevised plan`"，第一段回答出现两次；只有 transcript 时也会把两段回答粘成一块。缺口在 9656051 已存在，但 007 的行为矩阵与 test 8 明确要求修好——执行者把 test 8 改成"只取第一个匹配"，测试在 bug 存在时仍通过（上一轮评审接受的"test 8 首匹配调整"偏差即此，应驳回）。② `docs/plan/PLAN_REVIEW_DELIVERY_SILENT_PLAN.md` 的勘误段插进了第一个引用块中间，把"评审记录 / owner 裁决"并进了勘误引用块。其余逐项核对无问题：live 入口过滤取对缓冲区；漏斗删除后发布事件的 started 帧 `ToolMeta.Status` 恒为 `running`（`BuildToolMeta → stepStatusLabel`），仍被 reducer 拦截；所有带 `Confirmation` 的审批记录都写 `action_kind` 与 `tool_step_id`；gofmt/vet 干净。修法原型已在 scratchpad 副本验证：`./pkg/tui` 全绿，交付流程回放与 live 一致。
- **009 执行（2026-10-10，叠加在 007 的 worktree `$TMPDIR/fb-007-wt` 上）**：评审 **APPROVE**。执行者先让收紧后的 test 8 与新测试 `TestReplaySealsTheResponseThatIssuedACardlessGate` 在修复前失败（`here is the plan` 块 `Final:false`；`["first answersecond answer"]`），修复后通过；评审侧独立复跑：`./pkg/tui`、`./pkg/architecture` ok，007+009 相关 12 个测试全部 PASS，vet/gofmt/graph.json 干净，`git status` 仍是同样 11 个文件（009 只改 `commands.go`、`commands_test.go` 和勘误文档的位置）。真机：C'（取消后回放）由执行者跑通；E'（review → 批准 → 回放）被执行者误判跳过（driver 里 review 失败本来就在预期内），由评审补跑：live 与回放都是 `calling exit_plan_mode` → `You asked` → `Failed to start 1 plan-reviewer task` → `You approved` → 唯一一张 `◆ Exited plan mode`，`calling exit_plan_mode` 只出现 1 次，无 `◆ Canceled`。**007 + 009 的改动都未提交、留在同一个 worktree**，由 owner 决定并回方式（`$TMPDIR` 重启会清空，尽快落地；主树 `pkg/tui/notify.go` 有 owner 未提交改动，并回时注意）。上一轮接受的 007 偏差「test 8 首匹配调整」应视为驳回，已由 009 纠正。**已并回主树工作区**（2026-10-10，`git apply` 到 main 工作区、未提交、未动暂存区；与 owner 未提交的 `notify.go` 改动无冲突）。并回后 worktree `$TMPDIR/fb-007-wt` 与分支 `wt/improve-007-exit-gate` 已删除（删除前核对：分支无额外提交，worktree 改动在主树逐文件一致）。**review 交付成功路径已在主树用 glm-5.3 真机跑通**：review 期间 0 张假卡、交付静默、修订回答独立成块，live 与 `resume` 回放关键行逐行相同（记录见 009 末尾）。附带发现：home 在符号链接路径下时计划写入守卫只做字面比较（`pkg/tool/state.go:916`），既有问题，未立计划。
- **008 branch 审核（2026-10-10，`/improve branch` 针对 worktree `../forebrain-harness-008`，对照 plan 008）**：实现与计划逐行一致；TUI 发起的会话用本机真实数据（`cli-388cb9d1`）重载，块序正确。发现 3 处计划前提/范围缺口 + 1 组并回风险，均经探针（scratchpad 副本，未碰 worktree）或真实事件实证：① **gateway 发出的 `approval_requested` 不带 `tool_step_id`**（`pkg/gateway/wsevents.go:92-103`；只有 TUI 写），web 发起的会话重载后锚点学不到 actionId，review 卡落到整轮末尾、live≠重载——008 的问题 3 在 web 会话里原样存在（008 写"已核实"只核了 TUI）；TUI 回放 gateway 会话同样落不了位。② **拒绝带反馈时 web live 不画 settled 卡**：运行时注入拒绝结果不发步骤事件（`pkg/run/orchestration_llm.go:111-145`），TUI 的 "Kept planning" 是本地合成（`pkg/tui/chat_turn.go:1026` 起）；008 推演表声称 live 有这张卡，实际只有重载有（9656051 既有）。③ **ChatView 的块分支仍按 `msg.blocks?.length` 选择**（`ChatView.vue:184`），只剩锚点的回答（不说话直接调 exit_plan_mode 后在 web 停止）画空气泡、藏掉 "Request cancelled"；`hasVisibleBlocks` 还把无确认行的审批块算作可见。④ 并回：WorkshopView 那一 hunk 与主树 `005c1a8` 的 `rounded-lg` 改动相邻，`git apply --check` 失败；worktree 的 `pkg/gateway/dist` 由旧源码构建，不能拷进主树（并回后在主树重新 `pnpm build`）；上一条 008 记录里"007 仍 TODO"已过时（007+009 已随 `005c1a8` 提交）；008 Done criteria 的"TUI 回放 vs web 重载对照"尚未做。**Owner 裁决（2026-10-10）**：① gateway 补 `tool_step_id`（改 Go，不做前端启发式）；② 由**引擎**在注入拒绝结果处发 settled 步骤事件（不由 web 补画）；写完即派执行者在同一 worktree 叠加。立计划 011/012/013。写 012 时发现关键坑：拒绝完成事件与"等待审批"完成事件的规范事件 ID 相同（`tool:<run>:<step>:tool_call_completed`，真实数据核实），会被 `ON CONFLICT DO NOTHING` 吞掉——012 Step 1 加 `:denied` 边界。
- **011 执行（2026-10-10，worktree `../forebrain-harness-008`，叠加在 008 上）**：评审 **APPROVE**。评审侧独立复跑：`./pkg/gateway`、`./pkg/turn`、`./pkg/architecture` ok，vet/gofmt 无输出，前端 40 文件 327 用例全绿，vue-tsc 无输出；diff 7 个 in-scope 文件逐条对照计划（`turn.ApprovalToolStepID`、`withApprovalToolStepID` 定义 1 + 调用 2、wsevents 转换 1 行、3 个新测试）。执行者做了反证：去掉 `tool_step_id` 后新前端用例失败，review 卡掉到重载末尾，与探针一致。一处申报偏差接受：完成标准里 `grep withApprovalToolStepID` 实得 4 行而非 3 行——计划自己给的 doc 注释含该标识符，属计划笔误。未提交、HEAD 仍为 `9656051`。
- **012 执行（2026-10-10，同一 worktree，叠加在 008+011 上）**：评审 **APPROVE**，无申报偏差。源码 diff 逐条对照计划：`StepEvent.Denied/DenyFeedback`、`DeniedToolStep`、状态/正文/summary 三处、`:denied` 事件边界、拒绝注入循环；delivered 分支只改成提前 `continue`，Body 和 meta 的构造逐字节不变；TUI 主 agent 分支只落库不画。6 个新测试逐条读过，断言都打在关键行为上（只发一条、落库与步骤同源、delivered 不发、ID 不撞、TUI 有哨兵证明 UI 通道是通的、gateway 两条完成事件都在库里）。评审侧复跑：`pkg/tool`、`run`、`gateway`、`turn`、`event`、`architecture` 均 ok；`pkg/tui` 只有 `TestCharacterizationNetworkApproval` 失败，原因是 curl 访问 example.com 超时（走批准后真实执行的路径，与拒绝无关），单跑三次都通过；vet/gofmt 无输出，前端 328 个全绿。**评审侧独立 driver 真机复跑**（隔离目录 `$TMPDIR/fb-012r`，端口 8813）：live 和回放都是 `calling exit_plan_mode` → `✗ You did not approve` → 唯一一张 `◆ Kept planning └ 先拆分里程碑`；库里同一调用 `call_fake_1` 同时有 `…:tool_call_completed` 与 `…:tool_call_completed:denied` 两条，证实 `:denied` 边界防住了被吞。临时目录已删。未提交，HEAD 仍为 `9656051`。
- **013 执行（2026-10-10，同一 worktree，叠加在 008+011+012 上）**：评审 **APPROVE**。diff：`hasVisibleBlocks` 判断 + doc、`ChatView.vue:184` 一处条件、008 用例补 3 条断言、`ChatView.test.ts` mock 暴露 `messages` 并新增用例。评审侧复跑：前端 40 文件 329 用例全绿，vue-tsc 无输出，`msg.blocks?.length` 只剩打字点那一处；**评审侧在 scratchpad 副本里独立做了反证**：把 `:184` 改回原样，新用例就失败在 `toContain('Request cancelled')`。dist 在最后一次源码改动之后重建（16:51:31）。两处申报偏差接受：① 用 `vi.waitFor` 代替计划样例的 `nextTick`——Markdown 组件逐步打字输出，仓库已有同样写法的先例（`ToolOutput.test.ts`）；② `mountView` 与共用的 `beforeAll` 提到文件顶层（计划允许"提升或照抄"二选一）。未提交，HEAD 仍为 `9656051`。**011–013 全部完成**，连同 008 留在 worktree `../forebrain-harness-008` 待 owner 并回。
- **008 系列并回主树（2026-10-10，owner 指令"008 系列并回主树"）**：从 worktree `../forebrain-harness-008` 导出 `git diff 9656051 -- . ':!pkg/gateway/dist'`（20 个源码/测试文件），其中 18 个 `git apply` 到主树工作区时干净应用；`WorkshopView.vue` 与 `ChatView.test.ts` 按 README 并回须知手工合并：前者保留主树的 `rounded-lg`、取 `hasVisibleBlocks`，后者取 worktree 版本并把主树的 `SessionHeartbeat: true` 补进顶层 `mountView` 的 stubs。合并后逐一核对：两个手工合并的文件都保留了双方的改动。dist 按仓库约定用 `make ui` 重建（先清空再构建，196 个资源换了哈希，`index.html` 引用的文件都在，`.gitkeep` 保留），嵌入新 dist 的二进制编译通过。主树验证：`go build ./...` 通过；`pkg/tool`、`run`、`gateway`、`turn`、`event`、`state`、`architecture`、`tui` 全部 ok（含 007/009 的 TUI 改动一起跑）；vet/gofmt 无输出；前端 40 个文件 339 个用例全绿，vue-tsc 无输出。**未提交、未动暂存区**，HEAD 仍为 `005c1a8`。跨端对照已做，结果写在 008 末尾"验收记录"：F 拒绝带反馈、E review 失败后批准两段真实 TUI 会话，TUI 回放与 web 重载逐行一致。**影响 plan 010**：010 的 drift check 要求 `pkg/tool/state.go` 相对 9656051 无改动；012 在 `StepEvent` 末尾加了两个字段（与 010 要改的区域无关），010 的执行者会在 drift check 停下，需评审方先刷新 010 的 drift check。worktree `../forebrain-harness-008` 仍保留（改动已全部进入主树工作区），是否删除由 owner 决定。**worktree 已于同日按 owner 指令删除**（删除前逐文件核对：18 个文件与主树一致；另 3 个文件的差异全部来自主树 `005c1a8` 的 owner 改动，其中 `ChatView.test.ts` 仅缩进不同；worktree 为 detached HEAD、无额外提交）。
- **"本机 web 与 TUI 同源"一轮（2026-10-10，owner 连续裁决，立计划 014–024）**：起因是 owner 追问"TUI 会话不落主 agent 文字增量是否是 bug"。现状核实：TUI 主 agent 只落工具步骤/审批/plan review/压缩/`plan_updated`/`turn_error`/`turn_cancelled`；没有 `turn_started`/`turn_completed`、文字增量、diff、预算、队列、模式事件；gateway 的事件总线只转发本进程发布的事件，`bind_session` 只在绑定时补快照，不读其他进程写入的数据；subagent 的逐 token 增量已有约 450 万行（每行约 4 个字符），与 transcript 重复存储；web 工作台文件树、`@` 补全、`@` 图片以 primary agent workspace 为根，不是会话实际目录（owner 截图）；非项目会话的工具一律在进程启动目录执行。**Owner 裁决**：① 两端所有会话数据落库、同源；② 流式文字只存 transcript（文字块结束时写入，不按空行切；增量不再落库，主 agent 与 subagent 本期一并改；旧增量迁移清理，迁移前备份）；③ 首 token 延迟一轮一值、只存 `fb_runs.first_token_ms`（不写 `usage_json`），web 与 TUI 都在 Worked 行显示，live 首 token 到达即显示；④ 两端会话互相可见、可续聊；⑤ **不做**跨端实时推送（刷新/重新进入看到最新即可；owner 先要求实时观看，后撤回）；⑥ 一端在跑时另一端拒绝并提示"正在另一端运行"；⑦ 续聊时工具在会话出生目录执行，目录不存在则拒绝；web 工作台跟随会话目录；⑧ 文件型会话数据（`state/modes`、hook transcript、intermediate、输入历史）入库**暂不做**。期间被否决的方案见"已考虑并拒绝的发现"。数据核实（本机只读）：238 次 subagent 执行引用 222 个 worker 会话，库里只剩 70 个；约 150 次执行的文字只存在于增量中——迁移不能直接删除（019 对这部分改为合并）；cron 调度读取 `turn_completed.Text` 作为定时任务的输出（018 先改读者，再去掉 Text）。
- **同日追加裁决（2026-10-10）**：① 工具输出增量（`tool_output_delta`）本期也只推不存——018 纳入（含在途工具输出由 gateway 内存补发），019 迁移一并清理（有完成事件的删除、没有的按步骤合并；本机 19,394 行、15,425 个步骤，只有 6 个步骤没有完成事件）；判定函数改名 `event.IsPushOnlyStreamEvent`。② 迁移后**不**自动 VACUUM，也不加压缩入口。③ 023 的方案先出、owner 拍板后再实施：评审方已直接完成调研并写出 `docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md`。关键发现：工具执行目录完全由进程当前目录决定（全仓库无 `os.Chdir`），gateway 一个进程跑多个会话，必须引入每轮的"会话工作目录"上下文并按目录建 runner；TUI 推荐"在出生目录重新打开"。附带发现一个**已有缺陷**：gateway 已注册项目会话的 shell 在 gateway 启动目录执行、沙箱/审批取 gateway 启动目录的信任。4 个待决问题（信任、TUI 方案、渠道会话范围、项目会话是否同修）等 owner 回答。
- **项目模型裁决（2026-10-10～11，owner 多轮）**：设计文档已改写为定稿（`docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md` 第 0 节 R1–R10）。要点：每个会话都属于项目，出生目录 = 项目根 = 工作目录；TUI 启动目录即项目根（不找 git 根），自动登记进 `fb_projects`；web 对话前必须选项目或文件夹（文件夹自己没有项目就自动建以它为根的项目，与 TUI 同口径），项目根按用户所选原样；FOREBRAIN_HOME 内不能做项目根/启动 TUI；gateway 启动目录无任何业务语义；信任按项目根自己的；项目级配置去掉"受版本控制"门槛；TUI `/resume` 只列当前项目；渠道/定时任务必选项目；附件上传选目录保存在项目里；老会话迁移归入项目，确定不了的删除。**已执行**：本机 63 个 cwd 被旧版本错写成 agent workspace 的会话按 owner 指示直接在本机真实库删除（2026-10-11，未备份，owner 明确要求）。影响：022、024 需改写；新实现计划 025–031 待写。补充裁决（R11）：去掉项目"归档"，删除项目连同它的对话一起删（目录文件不动）。补充裁决（Q1–Q4，已定）：迁移时目录不存在也照样建项目，只删 cwd 为空的；FOREBRAIN_HOME 内出生的会话只本机有，迁移不写分支；工作坊会话也必须选项目；TUI 粘贴的截图存 `<项目根>/attachments/`。
- **全部计划落笔（2026-10-11，owner 指令"完成所有计划文件的编写，我确认后实施"）**：按项目模型与设计稿第 18 版写完 025–030、033、035–037，改写 022、024（原 031 并入 024 的 D、E 段）；web 改版计划写在 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/015-020`（外壳与页面结构、对话页、工作台、列表加弹窗、文件编辑器、技能与工坊）。同轮 owner 裁决：
  - ① 撤回"迁移后不自动 VACUUM"，照框架行为回收（019 已改）；
  - ② 项目键按项目根本身生成，不再向上找 git 根（025、027）；本机 2 个子目录会话没有计划文件，无需搬；
  - ③ 技能正式目录的改动自动进草稿（036、017）；
  - ④ 旧的主代理级通道配置迁移时删掉（035）；
  - ⑤ 设置去掉"主代理"标签，顶栏"新建主代理"只填名称、建好自动切换，删除在菜单栏下拉里（web 015）；
  - ⑥ 所有页面去掉标题层、减少嵌套（web 015）；
  - ⑦ 文件编辑用 Milkdown Crepe + CodeMirror 6（MIT，web 019）；web 代码高亮一律 monokai-light（web 015/019）。

  写计划时核实的新问题：
  - 迁移在外键关闭下运行，删会话必须逐表显式删除（025）；
  - v0→v1 旧迁移执行当前 schema 并写 `archived_at`，删列时要同步改（025）；
  - 主 agent 的 PermissionRequest 钩子 `cwd` 落到主代理工作区（026 一起修）；
  - TUI 每轮注入的 `project_root` 是 git 根（028）；
  - gateway 无会话模型 / 推理强度接口（web 016）。
- **自动压缩误触发与本地 token 估算一轮（2026-10-11，立计划 038–042）**：owner 报告 deepseek-v4-flash（1M 窗口）续聊从 Claude Code 迁移来的会话，第一句就压缩（卡片 "398.2k → 3.3k"）。
  根因（本机状态库与日志只读核实）：轮前判定以最后一条带 `usage_json` 的 assistant 行为锚点，那是迁移导入的 Claude Code 行（opus，900089 = CC 的系统提示/工具 + 257 个保留的加密 thinking 块 + 图片），≥ 900000 阈值；forebrain 自己的估算只有 398170。
  本机实测 15 次原生换模型，原生锚点 13 次比纯本地估算准，所以判据是"用量是否由 forebrain 自己的请求产生（有 run_id）"而非"模型是否相同"（038）。
  本地估算对所有模型都用 cl100k，增量偏差中位数 deepseek 1.63、glm 1.30。**Owner 裁决**：
  - ① 删除自研 `pkg/llm/hftokenizer.go`，改用 daulet/tokenizers（Rust HF tokenizers 的 cgo 绑定），**所有平台强制链接**：Windows 在 CI 用 Rust 现编，`go install` 不再开箱即用（040）；
  - ② 主流模型（deepseek/glm/gpt/claude/kimi/qwen/minimax/grok/gemini/xiaomi）的分词器文件提交进仓库、随 npm 包分发、首次运行放进缓存目录；先读缓存，缺失才按 HuggingFace → ModelScope 联网下载；内网可拷缓存目录（041）；
  - ③ Claude、Grok、Gemini 无当代公开分词器，全部用近似文件：Claude 2 旧分词器、Grok-2 的 HF 转换版、Gemma 4（041）；
  - ④ **禁止嵌入二进制**：models.json 也改成词典那种随包分发（039）。
  
  评审方补充的处理：
  - MiniMax-M3 是非商用许可，改用词表 id 完全相同、许可为 MIT 的 MiniMax-M2 文件；
  - Grok 转换版已逐个核对 130944 个 token id 与官方一致；
  - 11 个文件的 sha256 在 HF 与 ModelScope 两处一致，已写进 041；
  - daulet v1.27.0 四个平台预编译包的 sha256 与版本符号已核实，写进 040；
  - `${SRCDIR}` 相对路径链接方案已在本机独立模块里验证可用。

  035 的通道凭据存储 D-035-1：owner 已选方案 A（状态库新表 + `.env` 引用）。
- **上上轮（004）**：多 TUI 进程 plan 模式计划文件串台
- **Owner 裁决（004）**：隔离方案选「每会话子目录」`plans/<projectKey>/<sessionID>/`（评估过保持平铺的后缀命名 / 绑定文件名 / SQLite 归属表后仍维持子目录）；旧会话不迁移（扁平目录里的旧计划只作历史）；`/migrate` 导入的计划进入所属对话的目录，一个 Claude slug 对应多个会话时复制到每个会话；删会话不碰计划目录
- **上一轮（003）**：TUI message queue 在途 steer 无法撤回；owner 裁决：压缩窗口选「压缩不吞未送达 steer」；「清空队列对在途 steer 不撤回」和 web/gateway 同类问题并入本期一起修，web 送达的 steer 实时拆成独立气泡

## 计划列表

| # | 标题 | 类别 | 状态 | 优先级 | 工作量 | 风险 |
|---|------|------|------|--------|--------|------|
| [001-fix-message-recall-after-detach](001-fix-message-recall-after-detach.md) | 修复 runtime detach 后消息无法撤回 | Bug Fix | DONE | **HIGH** | S | LOW |
| [001-frontend-component-design-spec-alignment](001-frontend-component-design-spec-alignment.md) | 前端通用组件样式规范对齐 | Design System | TODO | HIGH | M (4-6h) | MEDIUM |
| [002-subagent-view-input-freeze-ledger-read](002-subagent-view-input-freeze-ledger-read.md) | subagent 视图的 composer 重绘不再整读 subagent 账本（修复快速滚轮后输入卡死） | Bug Fix / Perf | DONE | **P1** | S | LOW |
| [003-recall-in-flight-steer](003-recall-in-flight-steer.md) | 队列里显示的 steer 在模型开始回答前随时能撤回、清空时一并撤走，web/gateway 与 TUI 一致 | Bug Fix | DONE | **P1** | L | MED |
| [004-session-scoped-plan-files](004-session-scoped-plan-files.md) | 计划文件按会话隔离：多个 TUI / 会话同时在 plan 模式时 exit_plan_mode 审批不再显示别人的计划 | Bug Fix | DONE | **P1** | M | MED |
| [005-serialize-run-queue-hook-and-drop-retract-last-steer](005-serialize-run-queue-hook-and-drop-retract-last-steer.md) | gateway 的 run 队列 hook 一次只发一个变化；删掉已无人使用的 `RetractLastSteer` | Bug Fix / Tech Debt | DONE | P3 | S | LOW |
| [006-shared-compaction-guard-and-unseparable-provisional-tail](006-shared-compaction-guard-and-unseparable-provisional-tail.md) | 排查 Runner 共享的压缩 guard 会否被同 run ID 的另一段历史覆盖；不可达则让分不清暂定尾巴的调用不压缩。**BLOCKED（2026-10-13）：Q1 可达——fork 续跑（`subagent_continue`）不带自己的 run ID，沿用编排 run ID，经主/fork 共享的 `recoverableLLM` 把主链 checkpoint 拼进续跑请求（探针 C 实证）；Step 5 小修与共享 guard 的修法独立，交 owner 拍板** | Investigation | BLOCKED | P3 | S | LOW |
| [007-exit-gate-live-card-and-cardless-anchoring](007-exit-gate-live-card-and-cardless-anchoring.md) | exit_plan_mode 审批期间不再画假 "Exited plan mode" 卡、取消后不再多 "Canceled" 卡；无卡 gate 的确认行与 review 卡按真实顺序落位（live 与回放一致） | Bug Fix | DONE | **P1** | M | MED |
| [008-web-cardless-exit-gate-anchoring](008-web-cardless-exit-gate-anchoring.md) | web 端 exit gate 的决定行逐条显示、按真实顺序落在 gate 调用处，review 卡跟在请求它的行之后；live 与重载一致（对齐 007 后的 TUI） | Bug Fix | DONE | P2 | M | MED |
| [009-replay-seals-response-before-cardless-gate](009-replay-seals-response-before-cardless-gate.md) | 回放在发出无卡 exit gate 调用的行之后补喂 running 步骤、封存那段回答（修 review 交付流程回放文本重复 / 粘连）；收紧 007 test 8；勘误段放回原位 | Bug Fix | DONE | **P1** | S | LOW |
| [011-gateway-approval-request-names-gated-call](011-gateway-approval-request-names-gated-call.md) | gateway 的 `approval_requested` 带上被审批调用的 `tool_step_id`（与 TUI 同一推导 `turn.ApprovalToolStepID`），web 发起的会话重载后 review 卡不再落到整轮末尾 | Bug Fix | DONE | P2 | S | LOW |
| [012-engine-emits-settled-step-for-refused-calls](012-engine-emits-settled-step-for-refused-calls.md) | 用户拒绝的调用由运行时发出 settled "denied" 步骤（`:denied` 事件边界防吞），落库显示与之同源；web live 补上 "Kept planning"/denied 卡，TUI 只落库不重复画 | Bug Fix | DONE | P2 | M | MED |
| [013-chatview-blocks-that-draw-nothing-yield-to-content](013-chatview-blocks-that-draw-nothing-yield-to-content.md) | ChatView 只在有可见块时走块分支；`hasVisibleBlocks` 不再把锚点与无确认行的审批块算作可见（修空气泡） | Bug Fix | DONE | P3 | S | LOW |
| [014-tui-persists-main-agent-turn-lifecycle](014-tui-persists-main-agent-turn-lifecycle.md) | TUI 主 agent 的 `turn_started`/`turn_completed` 落库（不带回答文字），取消/失败事件带计划进度；计划进度计算下沉到 `pkg/turn` | Bug Fix | DONE（2026-10-14 执行完毕） | **P1** | S | LOW |
| [015-first-token-latency-per-turn](015-first-token-latency-per-turn.md) | 首 token 延迟：引擎在 `Submit` 测一次，存 `fb_runs.first_token_ms`（加列+迁移），两端运行中状态行与 Worked 行显示 | Feature | DONE（2026-10-14 引擎、迁移 v13、gateway 与 web 已提交 main；TUI 侧显示因 020 占用 pkg/tui 未做，待串行补齐；2026-10-15 已补齐落地：Submit 经 FirstTokenStore 上报，TUI 收 FirstTokenMsg，运行中状态行与 live Worked 行显示该值，回放从 fb_runs 读，实测 live/replay 同为 0.2s）| P2 | M | MED |
| [016-transcript-written-at-text-block-end](016-transcript-written-at-text-block-end.md) | 回答/思考在文字块结束时写入 transcript（引擎层，两端与 subagent 共用，幂等对账）；每个思考块单独一行并记录耗时 | Bug Fix | DONE（2026-10-13 执行完毕）| **P1** | M | MED |
| [017-subagent-views-from-worker-transcript](017-subagent-views-from-worker-transcript.md) | subagent 视图（web 与 TUI）重载/回放时从 worker 会话 transcript 重建文字；worker 会话缺失时退回事件重放 | Bug Fix | DONE（2026-10-13 执行完毕） | **P1** | L | HIGH |
| [018-stream-deltas-pushed-not-persisted](018-stream-deltas-pushed-not-persisted.md) | 回答/思考/工具输出增量只推送不落库；持久化的生命周期事件不带回答文字（cron 改读 transcript）；断线重连与刷新时 gateway 从内存补发在途文字与在途工具输出 | Bug Fix | DONE（2026-10-13 执行完毕） | **P1** | L | HIGH |
| [019-migrate-away-stored-stream-deltas](019-migrate-away-stored-stream-deltas.md) | 状态库迁移：先备份，再处理旧增量（含工具输出）——已有完整记录的删除，没有的合并成段；迁移后照框架自动 VACUUM（owner 2026-10-11 撤回"不回收"） | Migration | DONE（2026-10-14 执行完毕） | P2 | M | HIGH |
| [020-tui-persists-main-agent-state-events](020-tui-persists-main-agent-state-events.md) | TUI 主 agent 的 turn diff、预算、队列、steer 送达、模式事件落库；gateway 审批续跑补上 turn diff 伴随事件 | Bug Fix | DONE（2026-10-14 执行完毕，未提交） | P2 | M | LOW-MED |
| [021-busy-session-names-the-other-surface](021-busy-session-names-the-other-surface.md) | 会话被另一端（另一个进程）运行或停在审批上时，按 `fb_runs.owner` 判断并提示"正在另一端运行" | Bug Fix | DONE（2026-10-13 执行完毕） | P2 | S | LOW |
| [022-workbench-files-follow-session-directory](022-workbench-files-follow-session-directory.md) | web 文件视图（树、片段、`@` 补全、`@` 图片）以会话所属项目的根为准，主代理级工坊为主代理技能目录；新对话按所选项目（2026-10-11 改写） | Bug Fix | DONE（2026-10-12 执行完毕，未提交；评审 APPROVE） | **P1** | M | MED |
| [023-continue-in-session-birth-directory-design](023-continue-in-session-birth-directory-design.md) | 调研与方案：续聊时在会话出生目录执行（gateway runner 池 + TUI 单 runner），产出设计文档，信任处理交 owner 拍板 | Design | DONE（定稿设计已写，所有待决项已定；下一步写实现计划 025–031） | **P1** | M | — |
| [024-cross-surface-see-and-continue-acceptance](024-cross-surface-see-and-continue-acceptance.md) | 验收：两端双向看到并续聊、另一端在跑时拒绝、工具在项目根执行、信任按项目根、子目录项目、工作台根（2026-10-11 改写，并入原 031） | Tests | TODO | **P1** | M | — |
| [025-projects-own-every-session-migration](025-projects-own-every-session-migration.md) | 状态库：老会话归入项目（cwd 为空的删除）、项目根唯一、去掉归档、删除项目连同对话、项目键按项目根本身 | Migration | DONE（2026-10-11 执行完毕，未提交）| **P1** | M | HIGH |
| [026-working-dir-travels-with-every-turn](026-working-dir-travels-with-every-turn.md) | 每一轮带上项目根，工具、权限、规则、git 上下文都在项目根执行，不再读进程目录；修 PermissionRequest 钩子 cwd | Bug Fix | DONE（2026-10-11 执行完毕，未提交）| **P1** | M–L | MED |
| [027-every-session-runs-on-its-project-runner](027-every-session-runs-on-its-project-runner.md) | 每个会话跑在自己项目的 runner 上、按项目根自己的信任推导沙箱与审批、gateway 启动目录去语义、去掉"受版本控制"门槛、工坊 runner | Bug Fix / Security | DONE（2026-10-11 执行完毕，未提交）| **P1** | L | HIGH |
| [028-tui-launch-dir-is-the-project](028-tui-launch-dir-is-the-project.md) | TUI 启动目录登记为项目、数据目录内拒绝启动、`/resume` 只列本项目、跨项目 resume 拒绝、粘贴截图存进项目 | Feature | DONE（2026-10-11 执行完毕，未提交）| **P1** | M | MED |
| [029-web-chat-picks-a-project-first](029-web-chat-picks-a-project-first.md) | web 对话前必须选项目或文件夹：新对话页、项目胶囊、系统式文件夹对话框、信任提示；会话只能带项目创建 | Feature | DONE（2026-10-11 执行完毕，未提交）| **P1** | L | MED |
| [030-attachments-save-into-the-project](030-attachments-save-into-the-project.md) | web 附件"保存到"项目里选的目录，记住上次目录 | Feature | TODO | P2 | M | LOW–MED |
| [033-rewind-to-before-a-message](033-rewind-to-before-a-message.md) | 回到这里：文件工具改动按会话记录、三种恢复方式、可撤销；web 与 TUI `/rewind` 共用；"变更文件"数据源；删 rewind-last | Feature | DONE（2026-10-12 执行完毕，未提交；评审 APPROVE） | **P1** | L | MED |
| [035-channels-and-cron-belong-to-a-project](035-channels-and-cron-belong-to-a-project.md) | 通道与定时任务属于项目；进来的对话出生在项目根；旧主代理级通道配置迁移时删掉 | Feature | TODO | P2 | L | MED |
| [036-skill-drafts-publish-and-versions](036-skill-drafts-publish-and-versions.md) | 技能草稿 / 发布 / 版本：存储、发布原子、会话按版本加载、正式目录改动自动进草稿、gateway 接口 | Feature | DONE（2026-10-12/13；改动已落地并复跑全绿，评审待补：执行者运行被限额中断，无交付报告） | **P1** | L | MED |
| [037-tui-skills-panel-publish-and-versions](037-tui-skills-panel-publish-and-versions.md) | TUI `/skills` 面板：状态段、随状态变化的子菜单、发布、查看改动、版本记录（分页）、恢复、拉取更新 | Feature | DONE（2026-10-13 执行完毕，未提交）| **P1** | M | LOW–MED |
| [032-remove-working-set-tools](032-remove-working-set-tools.md) | 删除 working_set_* 工具与 WorkingSetSource（模型从未调用），连带 TUI 显示、安全白名单、上下文 API 字段与 web 面板区块 | Tech Debt | DONE（2026-10-13 执行完毕，未提交）| P2 | S | LOW |
| [034-composer-pickers-anywhere](034-composer-pickers-anywhere.md) | TUI 与 web 输入框任意位置输入 / 和 @ 都能呼出选择器；中间的 / 只列技能，发送时加载（一轮可多个）；两端共用一份记号用例表 | Bug Fix / Feature | DONE（2026-10-14 执行完毕）| **P1** | M | MED |
| [038-pre-turn-anchor-only-trusts-own-usage](038-pre-turn-anchor-only-trusts-own-usage.md) | 上下文占用只用 forebrain 自己请求（有 run_id）的用量当锚点，迁移会话续聊第一句不再被误压缩；压缩卡片显示判定用的数 | Bug Fix | DONE（2026-10-13 执行完毕，未提交；评审 APPROVE）| **P1** | S | LOW |
| [039-models-json-ships-beside-binary](039-models-json-ships-beside-binary.md) | models.json 不再嵌入二进制：随 npm 包 / 归档 / 镜像装在二进制旁（`install-assets.sh`），首次运行种入 home；go install 发行版缺文件时下载 | Packaging | DONE（2026-10-13 执行完毕，未提交）| **P1** | M | MED |
| [040-link-hf-tokenizers-library](040-link-hf-tokenizers-library.md) | 所有平台强制链接 daulet/tokenizers v1.27.0（`fetch-libtokenizers.sh`，Windows 用 Rust 现编），建 `pkg/llm/hftok` 包装 | Dependency / Build | DONE（2026-10-13 执行完毕，未提交）| **P1** | L | HIGH |
| [041-tokenizer-assets-and-per-model-estimator](041-tokenizer-assets-and-per-model-estimator.md) | 10 个家族的分词器文件随包分发、缓存优先、缺失按 HF→ModelScope 下载；按模型选分词器；删除 `hftokenizer.go` 与 tiktoken-go | Feature / Tech Debt | DONE（2026-10-13 执行完毕，未提交）| **P1** | L | MED |
| [042-compaction-estimates-with-session-model-tokenizer](042-compaction-estimates-with-session-model-tokenizer.md) | 轮前 / 轮中压缩判定与用量观测按本次调用所用模型的分词器估算（同口径）；glm-5.3 真机验收 | Bug Fix | DONE（2026-10-13 执行完毕）| P2 | M | MED |

## web 实现与设计稿的偏差（owner 2026-10-11：必须修，实施须 1:1 还原设计稿）

基准设计稿：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`（2026-10-11 已按项目模型迭代，原型 https://claude.ai/artifact/KKfPW8dXWScuRdTzonsk2F ，待 owner 确认）。web 端任何改动先在该稿上迭代、owner 确认后再写计划。已知偏差（owner 截图指出），待全量对照后单独立项：

1. 心跳、工作台按钮：实现放在顶栏下方单独一行；设计稿在顶栏、语言按钮左侧。
2. 输入框上方的“会话上下文、待办与运行诊断”面板：设计稿没有；本轮重新设计并移进工作台（概览 / 上下文 / 工具调用 / 文件四个标签）。
3. MCP 启动失败：实现作为一条红色回答出现在历史记录最下方；MCP 只在新开对话时启动一次，设计稿改为对话开头的提示，原始错误收进“查看详情”，工作台概览同步状态。
4. 消息窗口：用户消息、思考、所有工具调用卡片必须实时流式显示，与终端一致；工作台不重复展示工具调用（owner 2026-10-11）。待核对实现是否有缺口。
5. 现有"撤销最近一次文件改动"（`/api/chat/sessions/:id/rewind-last`）只恢复一个文件、取的是不分会话的最新快照、不可撤回、终端没有对应功能；设计稿改为用户消息上的"回到这里"（owner 已定：只记文件工具改动、对话回退点保留数据可撤销、TUI 用 `/rewind`），见设计文档 2.10，实现计划 033 待写。
6. 菜单栏底部：实现是两行文字（"设置"、"菜单栏切换为深色"）；设计稿是同一行，左边"设置"，右边一个方形图标按钮（深色菜单栏显示太阳，浅色显示月亮），见设计稿 `rail()` 的 `r-foot`。
7. 表单聚焦：实现是深色粗边框加外圈；设计稿不给输入框单独写聚焦样式，用浏览器默认的聚焦外框（owner 2026-10-11 试过"品牌色细边框 + 光晕"后要求改回原稿效果），实现按此还原。
8. 记忆"查看/编辑"、新建项目：实现与原稿都是在页面里展开；设计稿已改为弹窗。
9. 代码高亮：`ai-elements-vue/packages/elements/src/code-block/CodeBlockContent.vue:77,87` 的 `dark:` 变体（Tailwind 默认跟随系统暗色）会把代码块切成深底的 monokai；owner 2026-10-11 要求 web 端不论主题色、菜单栏深浅，代码高亮一律 monokai-light（与 TUI 的 chroma `monokailight` 同色）。
10. 文件编辑：规则文件、记忆文件、技能文件、配置文件现在都是纯文本框；设计稿改为同一个编辑器：.md 用 Milkdown Crepe（MIT，所见即所得，代码块可编辑、选语言、带高亮，可切源码），源码和代码文件用 CodeMirror 6（MIT）。选型与许可证核对见 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/vendor/README.md`。
11. 页面标题上方的"主代理 · main"标识：删掉（owner 2026-10-11：菜单栏顶部的选择器已经显示当前主代理）；设置页的"全局"、项目空间的"项目"保留。
12. 规则指令、模型服务、权限、设置的主代理 / MCP / 钩子：改为"列表 + 列表上方按钮 + 弹窗"，项目空间的规则指令、权限、MCP 同一套。
13. 技能工坊：改为会话页的超集（同一套消息流、输入框、工作台），工作台加文件、测试用例、评测、触发描述、版本。
14. 全量对照：已拆进 web 计划 015–020，每份的完成标准都要求逐页与设计稿并排截图对照，差异写进报告。

上述偏差对应的计划：1、2、3、4 → web 016；5 → 033；6、7、9、11、12 的页面结构 → web 015；8、12 → web 018；10 → web 019；13 → web 020；工作台 → web 017。

## 执行顺序

推荐按以下顺序执行：

```
003-recall-in-flight-steer (独立，无依赖，owner 报告的严重 bug，优先)
004-session-scoped-plan-files (逻辑上独立；与 003 改到同几个文件 api_extra.go / orchestration_llm_test.go，等 003 提交后再执行，或在基于 HEAD 的独立 worktree 里执行)
001-frontend-component-design-spec-alignment (独立，需用户确认命名策略)
005-serialize-run-queue-hook-and-drop-retract-last-steer (依赖 003 的代码在工作区或已提交；与 004 不重叠)
006-shared-compaction-guard-and-unseparable-provisional-tail (依赖 003；排查计划，Step 4 是决策闸门，可达时停下交 owner 拍板；与 005 不重叠，可并行)
007-exit-gate-live-card-and-cardless-anchoring (独立，owner 报告的可见 bug，优先；只改 pkg/tui，与 006 不重叠；notify.go 里有 owner 未提交改动，只删两处过滤条件，不碰其余)
008-web-cardless-exit-gate-anchoring (语义依赖 007、代码不重叠，可与 007 并行，应一起交付；只改 frontend；ChatView.vue / WorkshopView.vue 有 owner 未提交改动，只改点名的行)
009-replay-seals-response-before-cardless-gate (依赖 007 的未提交实现，在同一 worktree $TMPDIR/fb-007-wt 里叠加；与 007 一起并回)
011-gateway-approval-request-names-gated-call (叠加在 worktree ../forebrain-harness-008 的 008 未提交改动上)
012-engine-emits-settled-step-for-refused-calls (011 之后，同一 worktree；两者都改 pkg/gateway/approval_test.go 与 useChatStream.test.ts，必须顺序执行)
013-chatview-blocks-that-draw-nothing-yield-to-content (012 之后，同一 worktree；最后一步 pnpm build 重建 dist)
--- 本机 web/TUI 同源一轮（主树工作区，均基于 005c1a8 + 008 系列未提交改动）---
022-workbench-files-follow-session-directory (独立；owner 截图的可见问题，可最先做；023 复用它的 SessionWorkingDir)
014-tui-persists-main-agent-turn-lifecycle (独立)
021-busy-session-names-the-other-surface (独立)
015-first-token-latency-per-turn (独立；加迁移 v9——与 019 的迁移版本号顺延，谁先执行谁取下一个号)
016-transcript-written-at-text-block-end (独立；017/018 的前提)
017-subagent-views-from-worker-transcript (依赖 016；018 的前提——先换重载数据源，才能停增量)
018-stream-deltas-pushed-not-persisted (依赖 016、017)
019-migrate-away-stored-stream-deltas (依赖 016、017、018；迁移前备份)
020-tui-persists-main-agent-state-events (依赖 014 的写法；与 016–019 无冲突)
023-continue-in-session-birth-directory-design (依赖 022；调研计划，产出设计文档后交 owner 拍板，再写实现计划)
--- 项目模型与设计稿第 18 版（2026-10-11 写）---
025-projects-own-every-session-migration (先做；迁移版本号与 015/019 顺延)
022-workbench-files-follow-session-directory (改写版；依赖 025)
026-working-dir-travels-with-every-turn (依赖 025 无强依赖，建议在 025 后)
027-every-session-runs-on-its-project-runner (依赖 025、026)
028-tui-launch-dir-is-the-project (依赖 025、026、027)
029-web-chat-picks-a-project-first (依赖 025、027；可与 028 并行)
030-attachments-save-into-the-project (依赖 025、029)
033-rewind-to-before-a-message (依赖 025、026)
035-channels-and-cron-belong-to-a-project (依赖 025、027、028)
036-skill-drafts-publish-and-versions (依赖 026)
037-tui-skills-panel-publish-and-versions (依赖 036)
web 015 → 019 → 016 → 017（另需 022、033、036）→ 018（另需 025、029）→ 020（另需 029、036）
024-cross-surface-see-and-continue-acceptance (最后；依赖 014–022、025–029)
--- 自动压缩锚点与本地 token 估算（2026-10-11 写）---
038-pre-turn-anchor-only-trusts-own-usage (独立，owner 报告的可见 bug，可最先做)
039-models-json-ships-beside-binary (独立；建立 assets/ 与 install-assets.sh)
040-link-hf-tokenizers-library (独立；与 039 都改 Makefile/Dockerfile/npm 脚本/acceptance 脚本/README，串行执行，039 先)
041-tokenizer-assets-and-per-model-estimator (依赖 039、040)
042-compaction-estimates-with-session-model-tokenizer (依赖 038、041；含 glm-5.3 真机验收)
```

**同一批次的执行约束（2026-10-11 追加）**：025、030、033、035、036 中涉及状态库结构的（025、030、033、035）都改 `pkg/state/schema.sql` 与迁移，**按 025 → 030 → 033 → 035 串行**，迁移版本号依次顺延；026、027、028 都改 `pkg/process`、`pkg/tool`、`pkg/tui` 的同一批文件，**按序串行**；web 015–020 都改 `ChatView.vue` / `App.vue` 等，**按上面的顺序串行**。

**同一批次的执行约束**：014/015/016/018/020/022 都改 `pkg/tui` 或 `pkg/gateway` 的同一批文件（`chat_turn.go`、`chat_session.go`、`notify.go`、`api_extra.go`、`server.go`），**必须在同一工作区按上面的顺序串行执行**，不要并行派发。

**008 系列并回主树须知**（owner 决定方式与时机）：主树 `005c1a8` 已含 007+009（TUI），008/011/012/013 只在 worktree。
- `frontend/src/views/WorkshopView.vue` 的 hunk 与主树 `rounded-2xl→rounded-lg` 改动相邻，需手工合并（保留主树的 `rounded-lg`，取 worktree 的 `hasVisibleBlocks`）；`ChatView.vue`、`ChatView.test.ts` 主树也有 owner 改动，`git apply --check` 先试。
- `pkg/gateway/dist` **不要从 worktree 拷**：它由 worktree 的旧源码构建；并回源码后在主树 `cd frontend && pnpm build` 重新生成。
- `pkg/tui/chat_session.go` 主树已被 007 改动，011/012 均未触碰它；012 改的 `pkg/tui/chat_turn.go`、`pkg/tui/run_test.go` 主树与 9656051 相同。
- `frontend/src/views/ChatView.test.ts`：013 把 `mountView` 提到了文件顶层，主树 `005c1a8` 在原来那份 `mountView` 的 stubs 里加了 `SessionHeartbeat: true`——并回时把这一行补进顶层 `mountView` 的 stubs（否则主树的 `SessionHeartbeat` 组件会在测试里真实挂载）。
- 011/012/013 的 Go 改动所在文件（`pkg/gateway/{approval,server,wsevents}.go`、`pkg/turn/approval.go`、`pkg/tool/{state,format,notification}.go`、`pkg/run/orchestration_llm.go` 及其测试）在主树与 9656051 相同，可直接 `git apply`。
- 并回后补做 008 Done criteria 里的跨表面对照：同一会话 TUI 回放 vs web 重载（含 review 交付、拒绝带反馈两条流程）。

## 状态说明

- **TODO**：待执行
- **IN_PROGRESS**：执行中
- **BLOCKED**：被阻塞（等待决策或依赖）
- **DONE**：已完成
- **REJECTED**：已拒绝（说明原因）

---

## 最新审核发现总结（2026-10-09）

### branch 审核：003 在工作区里的实现（未提交，基于 `71c0449`）

**结论**：没有阻塞问题。实现与 003 计划一致；核心不变式经逐段核对成立（steer 恒在 `pending` 或某个未结算交付里、同一把 `rt.mu`；
「先 Commit 再查 `Retracted()`」两处顺序正确；`Recall`（`q.mu`→`rt.mu`）与 `Commit`（释放 `rt.mu` 后才进 `q.mu`）无锁序反转；
提醒类包装都在 `recoverableLLM` 里面，暂定尾巴的长度校验在今天的链路上成立；压缩的摘要请求会屏蔽 `OnResponseStarted`；
三个流式 provider 都触发 `NotifyResponseStarted`；`TakeAll`/`Next` 只在不可能有在途交付的位置调用）。
验证：`go vet`、`gofmt` 干净；`go test -race ./pkg/run`、`./pkg/gateway`、`./pkg/turn`、`./pkg/event`、`./pkg/architecture` 通过；
`./pkg/tui` 仅 `TestCharacterizationNetworkApproval` 因真实访问 example.com 偶发失败、单跑通过；两个前端测试文件 108 个用例通过。
未做：全量 `go test ./...`、TUI 真机 Step 12、web 浏览器复核清单（003 Maintenance notes ①②③，需 owner 手动）。

| # | 发现 | 归属 | 影响 | 计划 |
|---|------|------|------|------|
| 1 | gateway `watchRunQueue` 的 hook 在多个 goroutine 上并发运行且无互斥：别的请求的 hook 可能抢走刚送达的 steer，`input_delivered` 晚于回答首个 delta 落库，web 实时把回答开头画进上一个气泡；旧预览也可能最后落库 | 本分支引入 | 低（窗口极窄，仅 web 实时，刷新恢复） | 005 |
| 2 | `TurnInputRuntime.RetractLastSteer` 已无生产调用方，且看不到在途交付——003 自己的维护说明把它列为回归陷阱 | 本分支引入（变成死代码） | 低 | 005 |
| 3 | 撤回路径裁剪 adoption 依赖「暂定尾巴被遵守」；`applyRunScopedCompact` 的防御分支会整段压缩并折入 steer。探针证实：共享的单槽 `compactRunGuard` 在同一 run ID 下会把另一段历史的 checkpoint 拼进父请求；尾巴不被遵守时撤回会把 checkpoint 裁掉。生产可达性未定（线索：`own.RunRT == nil` 时 fork 子任务沿用父 run ID，且 fork 链与主链共用同一个 `recoverableLLM`） | 防御分支本分支引入；共享 guard 为既有 | 今天的链路上未见触发；若可达则是更大的既有压缩串台 | 006（排查） |

### 类别：Correctness — 多进程 / 多会话 plan 模式的计划文件串台

**严重程度**：**HIGH**

**根因**：「当前计划」= `<stateRoot>/plans/<projectKey>/` 下 mtime 最新的 `.md`（`pkg/state/plan.go` 的
`PlanPathForProject` → `newestMarkdown`），解析不带会话 ID。同一项目里的所有 TUI 进程、gateway 会话、/fork 出来的会话
共用这个扁平目录，谁最后写计划，谁的文件就成了所有会话的当前计划。同一个函数还决定了：批准后告诉模型去实现哪个文件、
计划评审读哪个文件、每轮 plan-mode 提醒里的 current、实现阶段的提醒、`/plan`、`/status`、web 的 plan-md 与审批卡、
shell 的 `FOREBRAIN_PLAN_FILE`。

**真机证据**（driver `toolcall` 模式，HEAD `71c0449`）：会话自己从没写过计划，在项目计划目录里放一份「别的进程的计划」后，
exit_plan_mode 审批框显示了这份外来计划；批准后工具结果是 `Implement the approved plan in …/other-process-plan.md`。

**修复**：见 `004-session-scoped-plan-files.md`——计划改放 `plans/<projectKey>/<sessionID>/`，会话 ID 一律取对话 ID
（`tool.PlanSessionIDFromContext`，subagent 写进父对话的目录）；所有读写入口切到会话版本，删掉按项目解析的旧 API；
/fork 复制源会话的计划；plan 模式下写不进别的会话目录（`isSanctionedPlanWrite` 只放行可写目录正下方的文件，无需修改）；
`/migrate` 把导入的计划写进 `cli-<id>` 会话目录（slug 对应多个会话时每个一份）；`RemoveSessionStateFiles` 有意不删计划目录。

### 类别：Correctness — 在途 steer 显示在队列里却无法撤回

**严重程度**：**HIGH**

**根因**：steer 在工具边界（或无工具调用的最终回答处）被 `TurnInputRuntime.BeginSteerDelivery` 从 runtime 的
`pending` 取走、拼进下一次模型请求，但要等该请求的**首个输出事件**才 `Commit`、才离开 `InputQueue.steers`
（队列预览的数据源）。这段窗口里消息仍显示在队列中，`InputQueue.Recall` 却用只查 `pending` 的
`RetractLastSteer` 去撤回，必然失败并跳过整条 steer 车道 → Shift+← 无响应，首 token 到达后消息自动显示为已发送。
窗口 = 下一次请求的首 token 延迟，若该请求触发中途自动压缩还要加上整段压缩时间（压缩在 `recoverableLLM` 里，
处于同一次调用之内）。现有子测试 `TestInputQueueRecallPicksNewestByLane/unretractable steer is skipped` 把这个行为钉死了。

**同类缺口（owner 要求并入本期）**：切换会话时 `InputQueue.Discard` 清空旧会话队列，但同样只撤得回 `pending`——
在途 steer 只从预览消失，旧 run 照样把它交给模型，Commit 时队列里已找不到它，模型回答了一条从未进 transcript 的消息；
runtime 已 detach 时 `Discard` 还不计 steer，「queued input discarded」提示少报。

**web / gateway 的同类问题（owner 要求并入本期）**：
- gateway 的主会话与 subagent `edit_last` 都走 `InputQueue.Recall`，同一个在途窗口；引擎修复覆盖，补 gateway 测试锁住。
- web subagent 视图的队列预览只在 HTTP 操作后推送：steer 送达、执行结束后预览不更新，已送达的消息一边显示成用户消息、一边挂在队列里，按撤回必失败（持久化事件回放后依旧）。修法：引擎 `SubagentSurface.OnQueueChanged` + 通道的队列 change hook，gateway 据此推预览。
- web 主会话里 steer 送达后从队列消失、却不进实时对话（TUI 画成 you 消息），要刷新才可见；gateway 会话队列的 delivered 列表只增不减。修法：新增持久事件 `input_delivered`，gateway `watchRunQueue` 取走 delivered 并先于预览发出；web 实时收尾当前回答、插入用户气泡、开新回答气泡（与刷新后的回放一致）。
- web 的 Shift+← 只在本页自己的 run 流式时生效：主会话空闲的 subagent 视图、刷新后进行中的 run，队列可见却按键无效。修法：输入框按「队列可见」放行，`editLastQueuedMessage` 捕获 409。

**顺带发现的潜伏 bug**：中途压缩的输入是整个 session（含在途 steer），checkpoint 落库后若这次请求失败，
`Rollback` 把 steer 放回队列由 turn 边界重发——checkpoint 里一份、重发一份，重复。

**修复**：见 `003-recall-in-flight-steer.md`——runtime 登记未结算的在途交付，新增 `RetractSteer(seq)` 可从在途交付里
拿回 steer 并中止承载它的调用；`Recall` 与 `Discard` 都改用它；被撤回过的交付拒绝 Commit，编排循环丢弃该调用结果、去掉撤回的 steer 后自动重发；
中途/被动压缩只 checkpoint 暂定消息之前的历史，并在不受撤回影响的 ctx 上运行。三个撤回入口（TUI、subagent 视图、
gateway `edit_last`）都走 `InputQueue.Recall`，引擎层一处修复全部生效。

### 类别：Performance / Correctness — subagent 视图输入卡死

**严重程度**：**HIGH**

**根因**：subagent 视图里每处理一次输入事件，TUI 主 goroutine 都会重绘 composer，而 composer 的队列预览
`run.SubagentInputPreview` 经 `agent.GetMerged → ListHistory` **整文件读取并逐行 JSON 解码**
workspace 级账本 `state/subagent-history.jsonl`（owner 环境 3.4 MB / 448 条，只增不减，约 30 ms/次）。
快速滚轮产生上千事件，每轮主循环付 30 ms，事件在输入管线积压 → 输入冻结数秒，之后积压的操作被快速执行。
主视图的预览读内存队列，不受影响。

**证据**（tmux + owner 真实 `~/.forebrain`，同一进程对照）：N=1000 个滚轮事件后探针回显延迟，
subagent 视图 3.97 s，主视图 0.11 s；`sample` + `go tool addr2line` 显示主 goroutine 栈 124/124 落在
`renderComposerWithState → … → agent.ListHistory → json.Unmarshal`。

**修复**：见 `002-subagent-view-input-freeze-ledger-read.md`——给进程内 subagent channel 表加
(会话 id, task id) 二级索引，预览 / 撤回 / Esc / 丢弃 / running 判断只查内存，不再碰账本。

---

## 历史审核记录

### 2026-10-08 - TUI message queue bug（Shift+← 无法撤回）

**根因**：`pkg/run/turn_input.go` 的 `InputQueue.Recall()` 把 `q.rt == nil`（runtime 已 detach）误当作
"不可撤回"，跳过整条 steer lane。runtime detach 后消息仍在 `InputQueue.steers`（未交付），可直接撤回。

**计划**：`001-fix-message-recall-after-detach.md`（已 DONE；单测 `TestRecallSteerAfterRuntimeDetach`，
TUI 真机：提交 → 等待 → Shift+← 撤回）。

### 2026-10-08 - 设计稿与实现一致性审核（通用组件样式）

**审核范围**：`frontend/src/assets/main.css` vs `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`

**发现**：设计稿定义的通用组件规范（`.btn`, `.field`, `.badge`, `.sw`, `.card`）与 `main.css` 实现存在三类问题：

1. **命名空间不一致**：设计稿用简短类名（`.btn`），实现用前缀类名（`.forebrain-btn`）
2. **数值偏差**：按钮、输入框的 `border-radius` 和 `padding` 不匹配
3. **组件缺失**：`.badge`、`.sw`、`.card` 及部分变体完全缺失

**计划**：见 `001-frontend-component-design-spec-alignment.md`（需用户确认命名策略）

---

## 已审核但未计划的发现

- ~~web 端无卡 exit gate 的审批记录落位错位~~ → 已立计划 008（2026-10-10，owner 要求另写 web 同步计划）。

- **会话内容落在磁盘文件里，而非只在 SQLite**（owner 2026-10-09 定方向："所有会话消息无论是 primary agent
  的还是 subagent 的，都不应该存在磁盘文件里，都应该只存在 sqlite 数据库里"；同日裁决"本期先优先解决
  subagent 视图卡死，其他放在以后处理"）。已盘点到的落盘位置（均在 `<workspaceRoot>/state/` 下，
  owner 环境 2026-10-09 体量）：
  - `subagent-history.jsonl`（3.4 MB）——`pkg/agent/subagent_history.go`，subagent 任务全文与输出；
    读写方遍布 `pkg/run`、`pkg/tui`、`pkg/gateway`、`pkg/turn`。002 只把它移出每事件热路径，迁库待下期。
  - `fork-sidechain/<sid>/*.jsonl`（9.3 MB）——`pkg/hook/dispatch.go:633`
  - `hook-transcripts/<sid>.jsonl`（62 MB）——`pkg/hook/dispatch.go:640`（hook 协议的 transcript_path，迁库涉及兼容性取舍，需 owner 拍板）
  - `intermediate/<sid>.md`——`pkg/state/intermediate.go:14`
  - `cli-input-history.txt`——composer 输入历史
  - 迁库需按仓库约定为用户旧数据写迁移（state 库当前 `user_version = 8`，账本按 agent workspace 分布、
    state 库在 home 级，导入旧 jsonl 只能在运行期做，不能放进纯 SQL 的 schema 迁移）。

## 已考虑并拒绝的发现

- **web 实时观看 TUI 会话 / TUI 实时显示 web 的 turn（跨进程实时推送）**（2026-10-10）：owner 先要求"必须实现"，随后撤回，改为"只要数据互通，刷新或重新进入能看到最新即可"。
  若将来重启：gateway 需要跟随数据库里其他进程写入的事件（总线目前只转发本进程发布的事件）。
- **增量逐 token 落库 / 按固定间隔合并落库 / 按文字块合并成事件落库**（2026-10-10）：三者先后被讨论，最终 owner 选择"文字只存 transcript、增量不落库"（016–019），避免与 transcript 重复存储。
- **首 token 延迟写进每条 assistant 行的 `usage_json`**（2026-10-10）：owner 明确否决——一轮只存一次，存 `fb_runs.first_token_ms`（015）。
- **跨进程排队（另一端在跑时把消息排进对方的队列）**（2026-10-10）：owner 选择"拒绝并提示正在另一端运行"（021）。
- **文件型会话数据入库（`state/modes/<sid>.json`、hook transcript、intermediate 笔记、输入历史）**（2026-10-10）：owner 说"先不考虑，以后再说"——本期不做。
- **工作台显示当前根目录路径**（022 起草时加入、随即删除）：owner 没有要求，属于范围外的界面改动。

- **web 端在 `approval_requested` 无 toolStepId 时，把 actionId 绑到本轮最早一个未绑定的锚点**（008 branch 审核）：纯前端，但属启发式——
  exit 调用在请求审批前就失败时会绑错，TUI 回放 gateway 会话也仍错位。owner 2026-10-10 选 gateway 补字段（011）。
- **web 收到 exit gate 的 denied 决定时自己补画 settled 卡**（008 branch 审核）：只动前端、风险小，但只修 web 的 exit gate；owner 2026-10-10 选
  由运行时发步骤事件（012），一并修好 shell 等被拒卡在 web live 停在 "awaiting approval" 的问题。
- **012 让 TUI 也从运行时事件画拒绝卡、删掉本地合成**（008 branch 审核）：更"引擎为源"，但 `toolStepSummary` 在 summary 为空时会退回
  `"ok"`/`"done"` 字样，嵌套 request_permissions 的 `canceled` 措辞也会被 `denied` 覆盖——可见变化需另行裁决；012 让 TUI 主视图只落库不画。
- **gateway 在 live 多建的 `gate:tool:exit_plan_mode` 锚点**（`server.go:1937` 合成等待步骤的 step_id）：看不见、不影响可见块序，记入 011 的
  Maintenance notes，不立计划。

- **007 入口过滤提前返回、跳过了 `Tracker.ObserveToolStep`，取消/交付掉的 exit gate 不再计入工具计数**（007 branch 审核第二轮）：
  属实，但 `Counters.Tools` 只被追踪、从不绘制（`formatWorkingCounters` 只画计划清单进度；回放用的是不带 tracker 的新 `Reducer`），
  没有用户可见后果，不值得为此挪动 007 已评审的过滤位置。
- **回放的封存直接在 `flush` 里调 `reducer.flushBufferedText`**（009）：能修，但在回放侧复制了 live 规则（缓冲区选择、reasoning 时长）；
  009 改为补喂 live 同款的 running 步骤，让回放走 reducer 同一入口。

- **只在 `notifyToolStepHooks`（主 agent 生产者处）补一道 `ToolStepHoldsNoCard` 过滤**（007）：能修这次的两个 bug，
  但和 d501e09 一样是"在某条生产路径上打补丁"，下一条新路径照样漏。007 把过滤放在所有工具消息都必经的
  `Reducer.reduceMessage`，并删掉漏斗处的重复检查。
- **给 exit gate 保留一个不可见的占位块，让确认行继续"插到它之前"**（007）：不用改落位规则，但不可见块要在视口行数、
  点击展开、`FinalizePendingTools` 等处处特判，复杂度和风险都更高。
- **web 用"隐藏的 exit_plan_mode 工具块"代替锚点块**（008）：能复用"插在被审批卡之上"的旧逻辑，但要撤回 d501e09 在
  `handleStepEvent` 的跳过、让运行状态行与各处工具块消费者都学会忽略它，牵连更广；锚点块是一个只有落位语义的新类型，渲染层天然忽略。
- **web 保持"一个 action 一个审批块"，把多条决定行合进同一块**（008）：改动小，但 review 卡无法夹在 "You asked" 与
  "You approved" 之间，读起来与 TUI 不同；008 对 exit gate 改为一条决定一个块。
- **把所有审批确认行的落位改成"按 StepID 找被审批的块，找不到就追加"**（007）：规则更精确，但改变所有工具审批的语义
  （并行调用时位置会变），超出两个 bug 的范围；007 只对无卡 gate 改规则。

- **在 `InputQueue` 引擎层统一串行化所有 change hook**（branch 审核，2026-10-09）：能顺带覆盖 TUI 与 subagent 通道，但 subagent 的 hook
  会在持有通道锁时被调用，要重新论证锁序；问题只在 gateway 有可见后果，005 只在 `watchRunQueue` 里加锁。
- **subagent 队列 hook 在每次执行 Attach/Detach 都落一条 `pending_input_updated`，且 HTTP 处理函数还会再发一次**（branch 审核）：
  事件日志多几行空预览，主会话早就是同样模式，不值得单独改。
- **`Discard` 与「已 Commit、队列尚未收到通知」的瞬间竞争**（branch 审核）：被丢的 steer 已在 session 里、会进 transcript；`Discard`
  只在切换会话时调用，离开的视图本就不再渲染它，回来时从 transcript 回放。既有行为，不改。
- **web 一次送达两条 steer 时两条用户气泡之间留一个空回答气泡**（branch 审核）：核实后不会显示——被收尾的空气泡 content 置空、无 blocks，
  `messageHasBubble` 返回 false。

- **计划文件「会话绑定」方案（扁平目录不变，在 `state/modes/<sid>.json` 里记会话写过的计划文件名）**：改动更集中，
  但两个会话起同名文件时仍会串台，还要额外处理 subagent 并发写绑定。owner 2026-10-09 选了「每会话子目录」。
- **旧会话迁移 / 回退（从 transcript 回填绑定、或无绑定时回退到扁平目录里最新的文件）**：owner 2026-10-09 裁决不迁移；
  回退会让旧会话继续串台。
- **保持平铺目录的串台修法——文件名带会话后缀 `<name>--<指纹>.md`、在 SQLite 里记计划文件归属**：都能修好，但前者让文件名带上
  不可读的后缀、模型写错名要被拒一次，后者要升 schema 并把数据库句柄传进 planModeLLM / shell / 工具等多处。owner 2026-10-09 评估后维持每会话子目录。
- **删会话时清理它的计划目录**：owner 2026-10-09 裁决删会话不碰计划目录，计划作为历史保留（004 只在 `RemoveSessionStateFiles` 注释里写明）。

- **在途 steer 一被取走就移出队列预览（显示为「发送中」）**：能让「显示即可撤回」字面成立，但 owner 要的是能撤回；
  且 `SteerDelivery` 的设计本意就是失败时把 steer 留在队列里由边界重发，提前移出会重新引入「显示已发送、实际没人回答」的问题。
- **「checkpoint 落库即视为送达」处理压缩窗口**：改动小，但 PostCompact hook 执行期间撤回存在竞态，已撤回的话可能残留在
  checkpoint 里，且压缩中撤回会白做一次压缩、重跑 PreCompact hook。owner 2026-10-09 选择了「压缩不吞未送达 steer」。

- **"只在裸终端复现、终端排水阻塞 paint 写入"**（a7151f5 asyncPaintWriter，已于 2026-10-09 回滚）：不是本卡死的根因——
  tmux 里稳定复现，且同一进程主视图正常。此前 tmux 复现失败是因为 driver 隔离 home 的账本几乎为空。
- **滚轮 release/motion 报告被重复计档**（b7eda22，已回滚）：只影响滚动行程，与输入冻结无关。
- **transcript 越长 `renderViewport` 越慢**：被对照推翻——主视图 557 条消息 1000 事件 0.11 s，subagent 视图消息更少却 3.97 s。
- **给 `agent.ListHistory` 加 mtime 缓存/增量解析**：账本在每次 subagent 执行开始和结束都追加，缓存频繁失效；
  且 owner 已定迁库方向。002 让热路径根本不读账本，更彻底。

## 下一步

1. **owner 确认**：本轮写完的计划（022、024 改写版，025–030、033、035–037；web 015–020）。035 的通道凭据存储已定（方案 A）。
2. 确认后按"执行顺序"里的分组派发：先 025，再 026 → 027，然后 028 / 029 并行；web 先 015、019。
3. 仍为 TODO 的早期计划（001 设计规范、006 排查、007/008 系列之外的 014–021、032、034）照旧可独立执行，与上面不冲突的可穿插。

---

**索引更新时间**：2026-10-11
**Improve Skill 版本**：按 improve skill 模板生成
