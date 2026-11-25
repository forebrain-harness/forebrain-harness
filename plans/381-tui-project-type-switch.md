# TUI：查看并修改项目类型（`/project`）＋ 跑批项目免信任记录

> **状态：owner 已确认全部裁决点（2026-11-25）；六轮评审到达 approve；待批准实施。** 内容基线：dev `3a261297`（主树此刻在 `feature/vscode-extension` `7e6aee3b`，其父提交即它，`pkg/**`、`cmd/**`、`frontend/**` 逐字节相同）。
> 经过一轮独立冷读评审与 owner 指定模型的六轮评审，成立的意见已全部折入；引用的行号逐条实跑核对过，少数标注「探索代理核实」的由执行者在 Step 0 重核。
> 批准后落盘为 `plans/381-tui-project-type-switch.md`；原型落 `plans/381-project-type-tui-prototype.html` 与 `.test.cjs`。编号取 381（380 已被「Chat 参与者」预留，`plans/README.md:11`、`:2152`）。
> 起因：owner 报障「另一个 agent 执行完 Java 项目相关任务后没有发交付物」。根因已确认是项目类型为 office（§1）。owner 裁决：**在 TUI 端新增修改项目类型的 UI/UX 和功能语义**（A 部分）；随后裁决 **跑批项目免信任记录——加载器豁免、删掉创建/PATCH 里的写入、不保留任何尽力写入**（B 部分）。office 提示词不改。

## 0. 裁决

**已裁决（2026-11-25，owner）**

| # | 裁决 | 本计划怎么落地 |
|---|---|---|
| a | 后台跑批类项目不需要信任记录：不校验、不需要、不写（「保留一行尽力写入」的语义与逻辑整个删掉） | B 部分：一个解析函数让跑批项目按类型视同受信任；技能与权限规则两条「按根查库」的线各加一个由调用方传入的布尔位；创建与 PATCH 只在调用方显式给 `trust` 时才写记录，删掉跑批的自动写入与致命校验 |
| b | 回合运行中允许执行 `/project` | 与 `/permissions`、`/rename` 同：不进 `runDisallowedCommands`（`pkg/turn/slash.go:112-127`）。运行中的回合绑定的运行时不受项目行改动影响，只影响之后新建的会话 |
| c | 入口：新命令 `/project`（按推荐确认） | §5.1、§5.2：两页面板 + 一帧系统回复，选择器与 `/permissions` 同款。未取的备选：`/type`（含糊）、并入 `/status`（改变只读语义）、并入 `/config`（分区覆盖测试会拒绝，且要重冻结原型） |
| d | 后台跑批：可改成也可改出，并把 `project` 加进跑批只读查看器的白名单（按推荐确认） | §5.6；§2.2 的跑批文案与原型两个跑批场景保留；验收 4a/4b 保留 |
| e | 「成为跑批时播种沙箱开关」：网关 PATCH 与 TUI 共用一份实现（按推荐确认） | §5.4 `ProjectTypeInput`；Step 3 补齐播种分支的网关测试 |

**知悉项（不是选项）**

- 「未开始的会话」只指**还没建会话行的启动会话**：首条消息、`/new`、`/model`、`/rename` 都会立刻建行并冻结类型。这类会话改类型后**跟随新类型并重新绑定运行时**——由现有落库规则推出，不这样做会让库里的会话类型与实际运行时不一致（§3.2）。
- B 部分让跑批项目「按类型视同受信任」：项目级 MCP、技能、项目 FOREBRAIN.md、项目权限规则（`<root>/.forebrain/safety.json`）对一个没有信任记录的跑批项目照常加载。**与今天相比没有新增暴露**——今天这些项目在创建或改成跑批时被自动写入信任记录，同样全部加载；变化只是不再落盘。把项目改成跑批（Web 确认弹窗、TUI 确认页）是唯一的同意点。

## 1. 为什么做（已核实的因果链）

- 出错会话 `cli-07a23acf-d3c9-4629-953d-c62553369499`（cwd `~/workspace/hundsun/guangfa/wenquxing`）：`fb_projects.project_type` 与 `fb_sessions.project_type` 都是 `office`。第 2 轮 run `741f4e37…` 改了 6 个 Java 源文件，`fb_artifacts` 零行。
- 模型（deepseek-v4.1-flash-1017）该轮最后的 reasoning（`fb_messages` id 589436）原文：`present_files description says "documents, spreadsheets, slides, PDFs, images, audio or video, HTML or Markdown pages, archives". Code source edits aren't in that list … Skip.`——这串枚举逐字来自 office 版工具描述（`pkg/tool/artifacts.go:55`）。
- 引擎兜底只管 coding：`collectTurnDeliverables` 对非 coding 直接返回（`pkg/turn/deliverables.go:204-206`）。
- 该会话跑的二进制是 `npm/dist/…/bin/forebrain`，今天 11:16:03 构建自 `3a261297`，不是旧版本。
- 终端里无从知道、也无从修改项目类型：`pkg/tui` 非测试代码没有写 `ProjectType` 的地方，slash 表（`pkg/turn/slash.go:83-110`，26 条）没有项目类命令。类型本来就允许改，入口只在 Web（`state.ProjectStore.Update`，`pkg/state/project_store.go:753`、`:779-785`；`frontend/src/components/project/ProjectOverview.vue:33`、`:325-343`）。
- 现行的信任写入与校验是历史包袱：创建接口对「跑批项目写不进信任记录」是致命的（`pkg/gateway/api_extra.go:7268-7290`：删项目、返 500 `trust_not_recorded`），PATCH 则静默写（`:7377-7379`）；而跑批的加载器之所以依赖它，只是因为它们与普通项目共用同一道信任门（`pkg/skill/roots.go:53-57`、`pkg/process/mcp.go:67`、`pkg/process/runner_pool.go:1003-1032`、`pkg/assembly/rules_hook.go:184`、`:228-240`）。

## 2. 原型稿（待确认）

| 文件 | 说明 |
|---|---|
| `<plan_dir>/project-type-tui-prototype.html` | 11 个场景，可交互（方向键、Enter、Esc、点击），120 / 88 / 68 列三档 |
| `<plan_dir>/project-type-tui-prototype.test.cjs` | Playwright 回归，14 项全过、`browserErrors=[]` |

`<plan_dir>` = `/Users/doudou/.forebrain/workspace/plans/-Users-doudou-workspace-unionj-cloud-forebrain-harness/cli-06c580dd-d6b9-443e-83cf-3406558d7cea`

待确认稿的 sha256（落盘到 `plans/` 后以落盘内容的指纹回填）：

- html `3225bc48e38427d8d1b549224107f2d80a9c949ebc43e8daaed59ff903e230d8`
- test.cjs `8c6f280a8342a20726163d6cd22865d3d901918c38d744d5ee8f82eb50151411`

打开：`open "<plan_dir>/project-type-tui-prototype.html"`。运行回归（草稿在 `plans/` 之外，需指明仓库与静态服务器；本次实跑 `passed 14`）：

```bash
python3 -m http.server 8799 --bind 127.0.0.1 --directory "<plan_dir>" &
FOREBRAIN_REPO=/Users/doudou/workspace/unionj-cloud/forebrain-harness \
PROTOTYPE_ORIGIN=http://127.0.0.1:8799 \
node "<plan_dir>/project-type-tui-prototype.test.cjs"      # 期望 passed 14, browserErrors []
```

落盘后按 `plans/` 惯例：`python3 -m http.server 8765 --bind 127.0.0.1 --directory plans` + `node plans/381-project-type-tui-prototype.test.cjs`（test.cjs 以自身文件名定位页面）。

规范沿用 `374-account-tui-prototype.html` 的终端渲染；版式取真实选择器（`pkg/tui/overlays_test.go:1679-1700` 的 golden；三行不出过滤框，`pickerFilterMinRows = 8`，`overlays.go:5059`）。终端内全英文，零网络（CSP `connect-src 'none'`），全文无中点。

### 2.1 场景

| 组 | 场景 | 看什么 |
|---|---|---|
| A 完整流程 | 刚打开终端就改（本会话未开始） | 「this one has not started, so it takes the new type.」；回复不要求 `/new` |
| | 会话已开始时打开 | 光标停在当前类型，`✓` 在标签右侧；副标题点名「this one keeps Office work.」 |
| | 选了另一种类型：先确认 | 复述「从什么改成什么」+ 生效范围；光标默认在 No |
| | 确认后：一帧系统回复 | `● project` 标题行 + 缩进正文；命令本身不回显 |
| B 关键设计帧 | 未开始的会话确认后 | 「…This conversation takes the new type when it starts.」 |
| | `/new` 之后：新会话里再打开 | `/new` 立刻建行，所以要先改类型再 `/new` |
| | 取消：零写入 | 确认页 Esc / No 回选择页（光标回到刚选的那行）；选择页 Esc 直接关闭 |
| | 在当前类型上按 Enter | 等同关闭，不写入、不发回复 |
| | 改成后台跑批 | 确认页多一句后果；回复不建议 `/new` |
| | 从后台跑批改出（只读查看器里） | 任务页与通知不再显示、已建任务仍可查看；回复要求重启终端 |
| C 边界与失败 | 写入失败 | `● error` 帧，正文「project type was not changed: <存储原话>」，类型不变 |

### 2.2 终端文案（逐字，实现与测试以此为准）

| 位置 | 文案 |
|---|---|
| 命令面板描述 | `show this project's type and change it` |
| 选择页标题 | `Project type` |
| 选项 | `Office work` / `Documents, spreadsheets, slide decks`；`Coding` / `Code and software projects`；`Batch runs` / `Runs tasks for a business system` |
| 当前类型标记 | 标签右侧一格画 `✓`（`SelectItem.Mark`，强调色 `overlays.go:6399-6400`，任何宽度都画；不用 `Status`——面板宽度 <100 列时不画且是暗色，`panel_columns.go:34`、`:189-193`） |
| 选择页副标题 | `<项目名>. A change reaches conversations started after it; <范围句>` |
| 确认页标题 | `Change project type` |
| 确认页副标题 | `Change <项目名> from <旧> to <新>? Only conversations started after the change get the new type; <范围句><追加句>` |
| 确认页选项 | `No, go back`、`Yes, change to <新类型>`（光标默认在 No） |
| 两页 footer | `↑/↓ to navigate enter to confirm esc to cancel`（`SelectRich` 默认，渲染层 `render.go:8304` 统一小写） |

label 为「标题 + 换行 + 副标题」（`pickerHead`，`overlays.go:5171-5182`）。

范围句（按屏幕上会话的状态三选一）：

| 屏幕上的会话 | 选择页 | 确认页 |
|---|---|---|
| 尚未开始（无会话行） | `this one has not started, so it takes the new type.` | 同左 |
| 已开始，出生类型 X | `this one keeps X.` | 同左 |
| 跑批执行（只读查看器） | `this screen keeps replaying a batch execution.` | `this screen keeps replaying a batch execution; restart Forebrain Harness to start a <新类型> conversation.` |

追加句（确认页，可叠加）：改成跑批时 ` Batch projects take tasks created on the web; the terminal only views them.`；改出跑批时 ` The task page and task notifications stop for this project; tasks already created stay readable.`

回复帧（`Frame{Kind: FrameSystem, Title: "project"}`；画法 `render.go:2149` `renderCompactFrame`、`reducer.go:3879-3880`）：

| 情形 | 文案 |
|---|---|
| 新类型是后台跑批 | `Project type is now Batch runs. Batch projects take tasks created on the web; the terminal only views them.`（屏幕会话已开始且不是跑批时追加 ` This conversation keeps <出生类型>.`） |
| 其它新类型，会话未开始 | `Project type is now <新>. This conversation takes the new type when it starts.` |
| 其它新类型，屏幕是跑批执行 | `Project type is now <新>. Tasks already created stay readable. Restart Forebrain Harness to start a <新> conversation.` |
| 其它新类型，会话已开始 | `Project type is now <新>. This conversation keeps <出生类型>; /new starts a <新> conversation.` |
| 会话不属于任何项目 | `This conversation has no project.` |
| 写入失败 | 不发回复帧；`PrintError` 画标题 `error` 的帧（`render.go:870-877`），正文 `project type was not changed: <存储原话>` |

## 3. 功能语义

### 3.1 行为矩阵

| 屏幕上的会话 | 改类型后发生什么 | 面板与回复怎么说 |
|---|---|---|
| **尚未开始**（启动会话，还没有会话行）→ 非跑批类型 | 项目行改成新类型；该会话**重新绑定**到新类型的运行时；首条消息落库时冻结为新类型。不需要 `/new` | 「has not started, so it takes the new type」 |
| **尚未开始** → 后台跑批 | 同上重绑，绑到的是跑批运行时；在终端里发首条消息会被拒绝：`This conversation is not a task execution.`（`pkg/process/run_executor.go:190-197`、`pkg/tui/chat_surface.go:366-367`）。此后该会话已有行、类型为跑批，按跑批会话对待（slash 门只放行白名单，含 `/project`）；重启后进查看器，无任务时提示 `No tasks yet…`（`run.go:4762-4771`） | 「has not started…」+ 跑批后果句；回复不建议 `/new` |
| **已开始**（有会话行，出生类型 X）→ 非跑批类型 | 项目行改成新类型；该会话的运行时、工具表、系统提示词**不变**；之后新建的会话取新类型 | 「this one keeps X」；回复给出 `/new` |
| **已开始** → 后台跑批 | 该会话不变；此后不能在终端里再开始会话 | 「this one keeps X」+ 跑批后果句 |
| **跑批执行**（只读查看器） | 项目行改成新类型；屏幕上仍是该执行的回放；重启终端后按项目新类型开始 | 「keeps replaying a batch execution; restart…」 |
| 改成后台跑批（任一方向进入） | 未显式给过时，项目行的 `batch_sandbox` 取安装默认值（与 Web PATCH 相同）。**不写信任记录、不校验**（裁决 a） | 确认页写明「任务在网页创建，终端只能查看」 |
| 改出后台跑批 | 沙箱开关、已有任务与执行原样留存，只是不能再建任务 | 确认页写明「任务页与通知不再显示，已建任务仍可查看」 |
| 在当前类型上按 Enter / 两页任一处 Esc | 不写入，不留回复帧 | — |
| 存储报错 | 一条 UPDATE 之内原子失败，项目行不变 | `PrintError` |

### 3.2 「尚未开始的会话跟随新类型」是推导出来的

- 会话行在这些时刻落库，类型在 INSERT 时取项目当前类型（`pkg/state/session_store.go:44-47`）：首条消息（`pkg/process/run_executor.go:114`）、`/new`（`pkg/turn/executor.go:295` `execNew` → `:309`）、`/model`（`pkg/tui/chat_slash.go:646`）、`/rename`（`SetTitle` → `session_store.go:1463`）。所以「未开始」只可能是启动会话。
- TUI 在启动时以及每次切换会话时（`switchStreamSession`，`run.go:2054`、`:2108`）都把屏幕上的会话**预绑定**到 runner 池条目（`run.go:4828` → `:4837-4847` → `chat_session.go:906-918` → `runner_pool.go:191-210` → `:797-826`）。无会话行时按项目**当时**的类型绑定（`withSessionType`，`:866-876`），绑定之后永远返回已绑定的条目（`:800-805`）。只改项目行会让首条消息落库冻结成新类型、运行时却仍是旧类型——必须重绑。
- 与 owner 2026-10-26 的裁定一致：出生冻结的是已出生的会话（`plans/145-project-types-foundation.md:31`、`:547-548`）。

### 3.3 跑批项目免信任记录（裁决 a 的语义）

| 读取点 | 今天 | 改后 |
|---|---|---|
| 项目运行时（池条目的启动上下文）→ 项目 MCP、LSP、技能根、有效配置 | 读信任记录；跑批项目靠创建/PATCH 写入的记录通过 | `process.ProjectLaunchContext`：跑批项目按类型视同受信任，不读记录 |
| 项目规则注入（项目 FOREBRAIN.md） | `safety.IsTrusted` 按根查库 | 回合上下文已知项目类型：跑批回合直接视为受信任 |
| 技能的开关 / 下载 / 删除 / 阴影标注 / 项目级安装（Web 技能页与 TUI 技能面板） | `TrustedProjectSkillRoots` 按根查库 | 调用方把已解析的信任决定传给 `skill.Service.ProjectTrusted` |
| 项目权限规则文件 `<root>/.forebrain/safety.json` | `trustedProjectRoot` 按根查库 | 调用方把决定传给 `safety.Paths.ProjectTrusted` |
| 网关项目级 MCP / LSP / 技能管理路由（9 处） | `ResolveRegisteredContext` 读记录 | 全部改走 `ProjectLaunchContext` |
| 创建 `POST /v1/projects` | 跑批自动写记录；写不进去删项目返 500 | 只在显式 `trust` 时写；无致命校验 |
| `PATCH /v1/projects/:id` | `body.Trust` 或结果为跑批 → 写记录 | 只在显式 `trust` 时写 |
| TUI `/project` 改成跑批 | （新功能） | 不写、不校验 |
| 启动时的信任确认（TUI 在项目文件夹里启动，`run.go:4795` `askWorkspaceTrust`） | 跑批项目文件夹**不问**（`workspace_trust.go:68-70`：`BatchFolderProject` 为真直接返回；既有测试 `workspace_trust_test.go:207`） | 不变：没有记录也不问，不会把记录写回来 |
| 存量信任记录（过去被自动写入的） | 被加载器读取 | 不清理、不迁移：对跑批项目无人再读；该文件夹日后改成其它类型时，这条旧记录沿用今天的行为 |
| 进程的基础运行时（TUI 在跑批项目文件夹里启动时的启动上下文） | 读自动写入的记录 → 受信任：`workspace-write` 沙箱、带项目 MCP 与技能根 | 开库之后按根查项目行，是跑批就用同一个解析函数重解析 → 行为不变。不这样做它会退成未定：只读沙箱、无项目资源（`safety/sandbox_config.go:528-531`、`process/mcp.go:67`） |
| Codex 资产导入的同意点；网页的信任等级读写 | 按根或按目录查库（`pkg/migrate/codex_assets.go:717`；`pkg/gateway/host_dirs.go:242-255`） | 不变：前者与跑批无关，后者是展示与记录而非门禁 |

不变：非跑批项目的一切信任行为；`/status` 的规则来源报告（`InstructionSources()` 无会话上下文，仍按记录报告，维护注记录这一处口径差异）；CLI `forebrain lsp`（`cmd/forebrain/lsp.go:126`，不服务跑批任务）。

## 4. 现状事实（执行前按 Step 0 重核）

**A 部分**

| 事实 | 位置 |
|---|---|
| slash 表 26 条；`status`、`config` | `pkg/turn/slash.go:83-110`（`:99`、`:104`） |
| 分类与动作缺省 | `:245-266` `defaultCategory`（`config` `:257-258`、`status` `:261-262`）；`:268-279` `defaultActionKind`（`:270` 的 `open-panel` 名单） |
| 运行中禁用的命令（不含 `/rename`、`/permissions`） | `:112-127` `runDisallowedCommands` |
| 命令表唯一穷举测试 | `pkg/turn/slash_test.go:448-456` 起 `want` 表 |
| TUI 分发与兜底 | `pkg/tui/run.go:1374` 起；`:1417`；漏 case 的两种下场：真实会话 `executeSlash` 的 default 答 unknown 帧（`pkg/turn/executor.go:127-129`），测试替身的 `ExecuteSurfaceSlash` 返回 false 时当普通回合发给模型（`run.go:1463-1472`） |
| `/permissions` 范本；可选能力写法 | `pkg/tui/commands.go:2660-2692`；`handleConfig` `config_panel.go:262-271`、`handleMemories` `commands.go:2524-2536` |
| 选择器、`Mark`、label、footer、帧画法 | `channels.go:664`、`:616-652`；`overlays.go:6399-6400`、`:5171-5182`、`:5184-5192`；`render.go:8304`、`:2149`、`:870-877` |
| 项目与类型句柄 | `chat_session.go:80`、`:298-303`、`:305-317`、`:329`、`:820-826`、`:828-838`；`pkg/state/project_store.go:1097` `SessionProjectTypeKnown`、`:753` `Update`（先 `Get`，`:756-759`）、`:730`、`:779-785`、`:186`、`:162` |
| 预绑定的三个调用点 | `run.go:4828`、`:2108`、定义 `:4837-4847` |
| 池条目与回收 | `runner_pool.go:797-826`、`:839-854`、`:866-876`、`:1283-1297`、`:108-123`、`:67`（LRU 8）、`:892-905` |
| MCP 启动监视幂等 | `notify.go:1412-1434` |
| 跑批只读查看器 | `chat_surface.go:281-287`、`:316`、`:55`；`run.go:1321-1335`、`:1992-1998`、`:4757-4779`；测试 `run_test.go:8445` |
| 测试夹具 | `run_test.go:769-829` `recordingSelector`（`SelectRich` `:804`）；`commands_test.go:26` `stubSelector`；`runner_pool_test.go:23` `newPoolTestEnv`、`:1168-1171` |
| 包文件数上限 | `pkg/architecture/cache_test.go` `maxProductionFilesPerPackage = 34`：`pkg/tui` 33、`pkg/process` 34（不新增生产文件）；`pkg/skill` 19；测试配对 `:1502`（预存红 `deliverables_outcome_test.go`，不修） |

**B 部分**

| 事实 | 位置 |
|---|---|
| 启动上下文与信任原语 | `pkg/safety/sandbox_config.go:425` `ProjectContext{Project, TrustLevel}`、`IsTrusted()`；`:479` `ResolveRegisteredContext`；`engine.go:948-953` `Level`；`:1002` `IsTrusted`、`:1054` `MarkTrusted`（记录在 `StatePath(home)` 的文件里）、`:1156` `load` |
| 运行时加载读的是冻结的上下文 | `safety/runtime.go:276` `TrustedRoot`（纯函数，注释：不在每个消费点重读信任存储）；`pkg/process/mcp.go:67` 等 15 处；`pkg/skill/roots.go:53-57`；`pkg/lsp/manager.go:34` ← `runner_pool.go:1032`、`open.go:630` |
| 池条目解析点 | `pkg/process/runner_pool.go:982` `buildEntryLocked`、`:1003` |
| 进程启动时的启动上下文 | `pkg/process/open.go:445`（解析早于 `:468` 开库）、`:597`（组装 `env`）、`:630`（`Deps.Trusted` 与 LSP 选项）；`pkg/process/config_apply.go:132`（`AdoptLaunchTrust` 重解析）；未定信任的后果：`pkg/safety/sandbox_config.go:528-531`（`decided` 为假 → 只读沙箱） |
| 技能开关的写路径 | `pkg/skill/service.go:537-548` `SetEnabledPaths` → `state.go:97-103` `SetEnabledPathsForWorkspace`（自己按记录发现 entries）→ `:105-127` `setEnabledPathsForEntries` |
| 规则注入 | `pkg/assembly/rules_hook.go:94-95`（按 ctx 的项目类型决定是否带人设文件）、`:127-128` `loadRules`、`:139-143` `InstructionSources`、`:172-190`（`safety.IsTrusted` 查库）、`:228-240`（未受信任则不注入项目 FOREBRAIN.md） |
| 网关 9 个解析点（手里都有整行 `p`） | `mcp_write_api.go:568-577` `mcpProjectWriteLayer`；`api_extra.go:5701-5715` `projectSkillLaunch`、`:8917-8932`、`:9020-9052`；`mcp_oauth_api.go:340-355`、`:583-614`、`:732-747`、`:783-809`、`:844-866`（探索代理核实） |
| 技能线的唯一查库点及其上游 | `pkg/skill/roots.go:94-108` `TrustedProjectSkillRoots` ← `state.go:153` `DiscoverForWorkspace`、`offline.go:134-146` `offlineDestRoot`；`Service.SetEnabledPaths` `service.go:537-545`；调用方：TUI `chat_slash.go:279`、`:343-347`、构造 `:1090-1092`、`:1098-1100`；网关 `api_extra.go:5644-5667` `skillLifecycleService`（`:5658-5659`）、`skills_download.go:83-94`、`:159-171`、`:514-551`（`:613` 用的是进程启动项目，不动）；`turn/executor.go:1058-1062` 的 `ctx.ProjectRoot` 恒空，不动（探索代理核实） |
| 项目权限规则文件 | `safety/runtime.go:56-60` `Paths{Home, WorkspaceRoot, ProjectRoot}`、`:248` → `<root>/.forebrain/safety.json`、`:292-306` `trustedProjectRoot`；构造点 `pkg/run/permissions.go:791`、`gateway/api_extra.go:1145`、`:8622` |
| 创建与 PATCH 的写入 | `api_extra.go:7268-7296`（自动写入 + 致命校验 + `trust_recorded`）、`:7377-7379` |
| 启动时的信任确认对跑批文件夹不问 | `pkg/tui/run.go:4795`（`startupQuestions` 第一句）→ `pkg/tui/workspace_trust.go:68-70`（`BatchFolderProject` 为真即返回）；测试 `pkg/tui/workspace_trust_test.go:207` |
| 钉住旧行为的测试 | `api_extra_test.go:930` `TestProjectsCreateTrustIsExplicitOperatorChoice`（保留）、`:1131` `TestProjectsBatchRecordsTrustWithoutADecision`（改写）、`:1258` `TestProjectPatchToBatchRecordsTrustWithoutADecision`（改写）；`skill/offline_test.go:349`、`skill/roots_test.go:158`、`assembly/rules_test.go:230`、`rules_hook_test.go:220`、`gateway/skills_download_test.go:283`、`process/runner_pool_test.go:1067`（照抄夹具） |
| 文档与 e2e | `docs/integration/batch-tasks.md:17`、`:79`、`:88`、`:94`、`:494`（后者还是早已不存在的 `409 batch_requires_trust`）；`frontend/e2e/batch-tasks.spec.ts:46-57`、`batch-live-attach.spec.ts:74`；Web 对跑批项目同样显示 MCP 与技能页签（`frontend/src/views/ProjectDetailView.vue:63`、`:65`） |

## 5. 设计

### 5.1 命令与分发（A）

`pkg/turn/slash.go`，表内 `status`（`:99`）之后加一行：

```go
{Name: "project", Description: "show this project's type and change it", AllowedSurfaces: []Surface{SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal},
```

- `defaultCategory`：加 `case "project": return "ui"`。`defaultActionKind`：`:270` 的 `open-panel` 名单加 `"project"`。
- `pkg/tui/run.go` 的 switch（`:1417` 之后），与其它 case 同形、**始终算已处理**：

```go
case "project":
	_ = cmds.handleProject(ctx, state.sessionID)
	return true, false, ComposerSubmission{}, false
```

### 5.2 面板（A；`pkg/tui/commands.go`，放在 `handlePermissions` 之后）

```go
// projectTypeEditor is the session's project-type half: what /project shows, and
// the one write it makes. An optional capability, the way the config panel and
// the memory settings assert theirs.
type projectTypeEditor interface {
	ProjectTypeState(ctx context.Context, sessionID string) (ProjectTypeSnapshot, error)
	SetProjectType(ctx context.Context, sessionID, projectType string) (reply string, err error)
}
```

`handleProject(ctx, sessionID) bool`（恒返回 true）：

1. 断言不到 `projectTypeEditor` → `PrintError(fmt.Errorf("/project is unavailable: this session has no project registry mounted"))`，返回。
2. `st, err := editor.ProjectTypeState(ctx, sessionID)`；出错 → `PrintError`。`st.ProjectID == ""` → 系统帧（Title `project`）`This conversation has no project.`，返回。
3. 循环，`cursor := 当前类型下标`：
   - 选择页：三行，当前类型行 `Mark: "✓"`；`SelectRich(projectTypePickerLabel(st), items, cursor)`。Esc → 返回；下标等于当前类型 → 返回；否则记下 `picked` 与 `next`。
   - 确认页：`SelectRich(projectTypeConfirmLabel(st, next), [No, go back / Yes, change to <新>], 0)`。Esc 或 No → `cursor = picked`，回选择页。
   - Yes → `reply, err := editor.SetProjectType(ctx, sessionID, next.ID)`。**错误规则**：`err != nil && reply == ""` → 没改成，只 `PrintError`；`err != nil && reply != ""` → 已改、重绑失败，`PrintError` 再渲染回复；`reply != ""` → `RenderFrame(Frame{Kind: FrameSystem, Title: "project", Content: reply, Final: true})`。

文案由四个纯函数拼出（`commands.go`）：`projectTypeScope(st, confirm bool, next string)`、`projectTypePickerLabel(st)`、`projectTypeConfirmLabel(st, next)`、`projectTypeReply(sessionType string, born bool, to string)`（先看新类型是不是跑批，再看屏幕会话的状态；§2.2 末表）。`projectTypes` 常量表取 Web 的英文词（`frontend/src/locales/index.ts:3782-3787`）。

### 5.3 会话方法（A；`pkg/tui/chat_session.go`，放在 `BatchProject` 之后）

```go
// ProjectTypeSnapshot is what /project shows: the launch project as it stands
// now and the type the conversation on screen was born with. Born is false
// while that conversation has no session row: it has frozen nothing and follows
// the project's current type.
type ProjectTypeSnapshot struct {
	ProjectID, Name, ProjectType string
	SessionType                  string
	Born                         bool
}
```

- `ProjectTypeState(ctx, sessionID)`：`store.Get(ctx, s.ProjectID)` + `store.SessionProjectTypeKnown(ctx, sessionID)`。`projectStoreFor()` 为 nil 只出现在无项目注册表的测试夹具，返回错误即可。
- `SetProjectType(ctx, sessionID, projectType) (reply string, err error)`：
  1. `current := store.Get(...)`；`in, err := process.ProjectTypeInput(current.ProjectType, projectType, sandboxDefault)`（`sandboxDefault` 取 `s.cfg()`，nil 时退到 `config.BatchSection{}.EffectiveSandboxDefault()`）。
  2. **写入之前**读一次屏幕会话的状态：`sessionType, born, _ := store.SessionProjectTypeKnown(ctx, sessionID)`——供重绑判断，也供回复。必须在 UPDATE 之前：若读写之间恰好有事务建了行（如排队的后续消息），那一行冻结的是**新**类型，此时仍然重绑才与行一致；放到之后读，会把这种会话当成「已出生、保留旧类型」而不重绑，留下旧类型的条目。
  3. `updated, err := store.Update(ctx, s.ProjectID, in)`；出错 → `return "", fmt.Errorf("project type was not changed: %w", err)`。
  4. `if !born`：`r, bErr := s.Env.RebindConversation(ctx, sessionID, s.ProjectID)`；成功 → `r.ReconcileSessionMCP()`、`s.WatchMCPStartup(sessionID)`；失败 → `err = fmt.Errorf("project type changed, but the conversation's runtime was not re-bound: %w", bErr)`（不回滚：旧绑定已解除，首回合 `Ensure` 后 `RunnerForTurn` 会按行里的新类型解析）。
  5. `return projectTypeReply(sessionType, born, updated.ProjectType), err`。
  - 不写信任记录、不校验（裁决 a）。

### 5.4 共用的沙箱播种（A；`pkg/process/config_apply.go`，补 `pkg/state` import）

```go
// ProjectTypeInput is the store input that changes an existing project's type —
// the one way every surface does it (the web's PATCH /v1/projects/:id and the
// terminal's /project). A project that becomes a batch project acquires the
// sandbox switch now, from the install's default: the global value is a
// starting point, and changing it later never reaches a project that already
// has one (plans/216). It writes no trust record: a batch project needs none
// (owner 2026-11-25).
func ProjectTypeInput(current, next string, batchSandboxDefault bool) (state.UpdateProjectInput, error)
```

- `state.ParseProjectType(next)` 出错原样返回；播种条件沿用网关今天的原式：`current` 不是 batch 且 `strings.TrimSpace(next) == state.ProjectTypeBatch`（区分大小写；`"Batch"` 今天不播种，保持不变）时设 `in.BatchSandbox = appcfg.BoolPtr(batchSandboxDefault)`。

网关 `handleProjectOne` 的 PATCH 分支逐字改成（`pkg/gateway/api_extra.go`；只有类型块与末尾信任行有变化）：

```go
	case http.MethodPatch:
		var body projectPayload
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		in := state.UpdateProjectInput{}
		if body.Name != "" {
			in.Name = &body.Name
		}
		in.Icon = &body.Icon
		in.Description = &body.Description
		in.Instructions = &body.Instructions
		if body.MemoryScope != "" {
			in.MemoryScope = &body.MemoryScope
		}
		if body.ProjectType != "" {
			typeIn, err := process.ProjectTypeInput(p.ProjectType, body.ProjectType, s.batchSandboxDefault())
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "project_type_invalid"})
				return
			}
			in.ProjectType = typeIn.ProjectType
			if body.BatchSandbox == nil {
				in.BatchSandbox = typeIn.BatchSandbox
			}
		}
		if body.ResourceAccess != nil {
			in.ResourceAccess = body.ResourceAccess
		}
		if body.BatchSandbox != nil {
			in.BatchSandbox = body.BatchSandbox
		}
		// …task_notify 校验与赋值、store.Update、错误处理：原样不动…
		// An explicit trust decision is the client's to record; a project that
		// merely became a batch project records nothing (owner 2026-11-25).
		if body.Trust {
			_ = safety.MarkTrusted(s.Home, safety.Project{Root: updated.Root})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.projectRow(updated))
```

逐项等价（类型块）：空 / 相同 / 不同 / 非法四种取值不变；仍是单次 `store.Update`；「未显式带 `batch_sandbox` 且成为跑批才播种、显式值优先」不变。

### 5.5 未开始会话的重绑（A；`pkg/process/runner_pool.go`，紧跟 `BindConversation`）

```go
// RebindConversation binds sessionID to projectID again, dropping the binding it
// already holds. It is for a conversation that has no session row yet: an
// earlier binding followed the project's type at that moment, and a type edit
// since makes that pre-resolution stale — the row its first turn writes freezes
// the project's current type, so the runner has to be that type's as well.
// Call it only for such a conversation: a conversation that has a row is meant to
// keep the entry it is bound to.
func (env *Environment) RebindConversation(ctx context.Context, sessionID, projectID string) (*run.Runner, error) {
	if env == nil { return nil, nil }
	if env.SQL == nil { return env.Runner, nil }
	env.EnsureRunnerPool()
	_ = env.pool.BindSession(ctx, sessionID, "") // drops the stale pre-resolution
	return env.BindConversation(ctx, sessionID, projectID)
}
```

### 5.6 跑批只读查看器（A；裁决点 2）

`pkg/tui/chat_surface.go:281-287` 的 `batchViewerCommands` 加 `"project": true`。不取此项时：删掉这一行，原型的两个跑批场景、对应文案与验收 4 一并删除。

### 5.7 跑批项目免信任记录（B；裁决 a）

**B1 一个解析函数**（放在 `pkg/process/runner_pool.go`、与 `BindConversation` 同族：`pkg/process` 的生产文件数已到顶，且它解析的正是池条目用的那种启动上下文）：

```go
// ProjectLaunchContext resolves a registered project's launch context the way
// its type needs (owner 2026-11-25): a batch project carries no trust decision
// and needs none — its tasks run unattended on the project's skills, MCP
// servers and rules — so it reads as trusted without a record. Any other
// project reads its recorded decision. Callers freeze the result, as every
// launch context is. A context that did not resolve is never trusted.
func ProjectLaunchContext(home string, project state.Project) (safety.ProjectContext, error) {
	launch, err := safety.ResolveRegisteredContext(home, project.Root)
	if err != nil {
		return launch, err
	}
	if state.NormalizeProjectType(project.ProjectType) == state.ProjectTypeBatch {
		launch.TrustLevel = safety.LevelTrusted
	}
	return launch, nil
}
```

使用点（手里都有整行 `p` / `project`，或能在开库之后按根查到行）：

- `pkg/process/runner_pool.go:1003`：`resolved, err := ProjectLaunchContext(env.Root, project)`。
- `pkg/process/open.go`：启动上下文在 `:445` 解析（早于 `:468` 开库；TUI 经 `ExactLaunchRoot` 按登记根解析，其它入口默认向上找 `.git` 边界），`activeAgent` 在 `:586` 算出，`env` 在 `:597` 组装，`Deps.Trusted` 与 LSP 选项在 `:630` 读取。**插在 `:586` 之后、`:597` 之前**，并且**只在查到的项目是跑批时**才重解析——登记根从不向上找，无条件替换会把「已登记、没有自己的 `.git`、位于某个 checkout 内」的非跑批项目的边界与信任一起改掉：

```go
	if !opts.NoLaunchProject {
		// A launch folder that is a batch project needs no trust record (owner
		// 2026-11-25). Any other folder keeps the context resolved above: its
		// boundary may come from walking up to a .git marker, and a registered
		// root never walks up, so replacing it would move the project.
		projects := state.NewProjectStore(sqlDB, activeAgent.ID)
		p, ok, perr := projects.GetByRoot(ctx, launchProject.Project.Root)
		if perr == nil && !ok {
			// The launch directory may be spelled through a symlink, and the rows
			// hold canonical roots: look it up the way registration wrote it.
			// GetByRoot only trims; CanonicalPath also resolves symlinks.
			if registered, rerr := safety.ResolveRegisteredProject(launchDir); rerr == nil {
				p, ok, perr = projects.GetByRoot(ctx, registered.Root)
			}
		}
		if perr == nil && ok && state.NormalizeProjectType(p.ProjectType) == state.ProjectTypeBatch {
			launchProject, _ = ProjectLaunchContext(root, p)
		}
	}
```

- `pkg/process/config_apply.go:132`（`AdoptLaunchTrust`）：同样的护栏——按 `launch.Project.Root` 查行，**只在是跑批时**改用 `ProjectLaunchContext` 的结果；非跑批保持 `ResolveRegisteredContext` 的结果不变（`env.SQL` 已在手）。
- 网关 9 处：把 `safety.ResolveRegisteredContext(s.Home, p.Root)` 换成 `process.ProjectLaunchContext(s.Home, p)`（`mcp_write_api.go:577`；`api_extra.go:5715`、`:8932`、`:9052`；`mcp_oauth_api.go:355`、`:614`、`:747`、`:809`、`:866`）。各处的错误处理原样不动。

**B2 规则注入**（`pkg/assembly/rules_hook.go`）：

- `buildRules(wd string, agentFiles bool)` → `buildRules(wd string, agentFiles, trustByRule bool)`；`loadRules(wd, hc, agentFiles)` 同步加参；钩子体（`:94-95`）里 `batch := tool.ProjectTypeFromContext(ctx) == state.ProjectTypeBatch`，`loadRules(wd, hc, !batch, batch)`。
- `buildRules` 里算完 `trusted` 之后：`if trustByRule { trusted = true }`（缓存键已含 `trusted`，自然分开）。`InstructionSources()`（无会话上下文）传 `false`，行为不变。

**B3 技能线**（`pkg/skill` 不 import `state`，由调用方传决定；现有函数签名全部保留）：

- `pkg/skill/service.go`：`Service` 加字段 `ProjectTrusted bool`（注释写明：调用方已解析的「项目按规则受信任」决定，为真时不读记录；每个 `Service` 实例由调用方按次构造——网关 `skillLifecycleService`、TUI `chat_slash.go:1090` 都是每次新建——所以设这个字段是安全的，共享实例则不是）；加方法 `func (s *Service) Discover() ([]Entry, error)`（`ProjectTrusted` 为真走 `DiscoverForTrustedProject`，否则走 `DiscoverForWorkspace`）。
- **开关的写路径必须自己取 entries**：`SetEnabledPaths`（`service.go:537-548`）现在是把工作交给 `SetEnabledPathsForWorkspace`，而后者（`state.go:97-103`）**自己再按记录**调用 `DiscoverForWorkspace`——只把 `SetEnabledPaths` 改成调 `s.Discover()` 改不到写路径，跑批项目的项目技能不在 entries 里，开关会静默无效。改成：草稿校验之后 `entries, err := s.Discover()`，再直接调 `setEnabledPathsForEntries(stateRootForWorkspace(s.Home, s.workspaceRoot()), entries, paths)`，最后照旧 `s.Refresh()`。`SetEnabledPathsForWorkspace` 保持不变（其它调用方照旧）。
- `pkg/skill/state.go`：加 `DiscoverForTrustedProject(home, workspaceRoot, projectRoot string) ([]Entry, error)`；`DiscoverForWorkspace` 改为一行委托（项目技能根按记录；受信任变体直接取 `projectSkillRootsForRoot(root)`，规范化照抄 `roots.go:99-102`）。
- `pkg/skill/offline.go:141`：`s.ProjectTrusted` 为真时跳过「必须受信任」的长度判断。
- 网关：`api_extra.go:5715` 已走 B1；`skillLifecycleService`（`:5658-5659` 之后）加 `svc.ProjectTrusted = launch.IsTrusted()`；`skills_download.go:94`、`:171` 改调 `svc.Discover()`；`:613` 不动。
- TUI：`chat_slash.go:1092`、`:1100` 之后加 `svc.ProjectTrusted = launch.IsTrusted() || s.BatchFolderProject(launch.Project.Root)`；`:279`、`:347` 改调 `svc.Discover()`。

**B4 项目权限规则文件**（`pkg/safety`）：

- `safety.Paths` 加字段 `ProjectTrusted bool`；`trustedProjectRoot(paths)`（`runtime.go:292-306`）开头：`if paths.ProjectTrusted { return filepath.Clean(dir) }`，并加一行注释：这里跳过了 `ResolveRegisteredProject` 的规范化，因为调用方给的是已解析过的项目根。
- 三个构造点填值：`pkg/run/permissions.go:791`（`r.ProjectType() == state.ProjectTypeBatch`）、`gateway/api_extra.go:1145`、`:8622`（手里是项目根或行：能拿到行就按类型，拿不到就保持 false 并在维护注写明）。

**B5 删掉写入**（`pkg/gateway/api_extra.go`）：

- 创建（`:7274-7291`）：

```go
	trustRecorded := false
	if body.Trust {
		if err := safety.MarkTrusted(s.Home, safety.Project{Root: root}); err == nil {
			trustRecorded = true
		}
	}
	row := s.projectRow(p)
	row["trust_recorded"] = trustRecorded
	if !trustRecorded && body.Trust {
		row["trust_error"] = "the trust decision could not be recorded"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(row)
```

  （删掉 `|| projectType == state.ProjectTypeBatch`、删掉「batch 且未写成 → 删项目 → 500」整段；`trust_recorded` 从此只表示「调用方显式给的信任决定写成了」。）
- PATCH：见 §5.4 末尾（只留 `body.Trust`）。

**B6 文档与 e2e 源**：`docs/integration/batch-tasks.md` 的 `:17`（项目说明）、`:79`（示例去掉 `"trust":true`）、`:88`（响应示例去掉 `trust_recorded` 或改 `false`）、`:94` 与 `:494`（删掉早已不存在的 `batch_requires_trust`）；`frontend/e2e/batch-tasks.spec.ts:46-57`（注释改写、去掉 `trust: true` 与 `trust_level === 'trusted'` 断言）、`batch-live-attach.spec.ts:74`（去掉 `trust: true`）。

## 6. Scope

**In scope**

| 文件 | 改动 |
|---|---|
| `plans/381-tui-project-type-switch.md`；`plans/381-project-type-tui-prototype.html`、`.test.cjs` | 计划与原型落盘、回填 |
| `plans/README.md` | 审核信息一条 + 索引行；**写前 ping d1 等它回「好」；只追加 / 插入新行，不得整段重写或回退**（文件含他人未提交改动） |
| `pkg/process/config_apply.go`（+test）、`pkg/process/open.go`（+test） | `ProjectTypeInput`；`AdoptLaunchTrust` 与进程启动时对跑批启动目录按类型解析 |
| `pkg/process/runner_pool.go`（+test） | `ProjectLaunchContext`、`RebindConversation`；`:1003` 改调 |
| `pkg/gateway/api_extra.go`（+test）、`mcp_write_api.go`、`mcp_oauth_api.go`、`skills_download.go`（+`skills_download_test.go`） | PATCH 播种改调共用函数、信任行只留显式；创建删自动写入与致命校验；9 个解析点与技能线改调 |
| `pkg/assembly/rules_hook.go`（+`rules_hook_test.go`、`rules_test.go`） | `trustByRule` 参数 |
| `pkg/skill/service.go`、`state.go`、`offline.go`（+ 对应 test） | `ProjectTrusted`、`Discover()`、`DiscoverForTrustedProject` |
| `pkg/safety/runtime.go`（+test） | `Paths.ProjectTrusted` |
| `pkg/run/permissions.go`（+test） | 构造 `Paths` 时填 `ProjectTrusted` |
| `pkg/turn/slash.go`、`slash_test.go` | 命令表一行；分类、动作；`want` 表加 `"project"` |
| `pkg/tui/chat_session.go`、`commands.go`、`run.go`、`chat_surface.go`、`chat_slash.go`（+ `chat_session_test.go`、`commands_test.go`、`run_test.go`） | A 部分的会话方法、处理器、分发、白名单；B3 的两处 `ProjectTrusted` 与两处 `svc.Discover()` |
| `docs/integration/batch-tasks.md`、`frontend/e2e/batch-tasks.spec.ts`、`batch-live-attach.spec.ts` | B6 |

**Out of scope（明确不碰）**

- 三种类型的提示词与工具描述（`pkg/agent/office_prompts.go`、`pkg/tool/artifacts.go`、`pkg/agent/coding_delivery.go`）；`pkg/turn/deliverables.go` 的兜底范围。
- `pkg/state/*` 存储层（会话类型出生冻结的 SQL、项目表）。
- 创建接口以外的信任写入语义：`body.Trust` 的显式决定照旧记录；`row["trusted"]`（`api_extra.go:7039` `projectIsTrusted`，仅展示）不动。
- CLI `forebrain lsp`（`cmd/forebrain/lsp.go:126`）：不服务跑批任务。
- `pkg/turn/executor.go`：不给 `executeSlash` 加 TUI 原生 case；`execSkills` 不动。`pkg/skill/state.go:97-103` `SetEnabledPathsForWorkspace` 不动（写路径的改动在 `Service.SetEnabledPaths`）。
- `frontend/src/**`：Web 组件不改（只改两份 e2e 源）；`/config` 及其冻结原型不动；`/status` 不加项目类型行。其中 `frontend/src/components/project/ProjectOverview.vue:106-107` 的注释「a batch project needs its directory trusted」说的正是本期删掉的规则——功能不看它（`canChangeType`，`:305`，与 trust 无关），留待下次动到该文件时一并改。
- 不新增 `pkg/tui`、`pkg/process` 生产文件（文件数上限）。

## 7. Steps（有序；每步结束项目可构建、相关包测试绿）

实施位置：**当前源码**（owner 裁决，不用 worktree）。动手前：`git status --porcelain` 存一份快照；`peer_send forebrain-harness-d1` 告知将改的文件，并约定 `plans/README.md` 的写法（写前 ping、等它回「好」、只追加 / 插入新行——d1 2026-11-25 已确认此协议）。**全程不 commit**。主树在 d1 的 `feature/vscode-extension` 上实施与在 dev 上等价；**不要自己切分支**，需要 dev 时请 d1 切。

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
export FOREBRAIN_HOME="$(mktemp -d)"      # 测试一律跑在临时 home
```

**Step 0 — 基线与漂移检查（任一不符即 STOP）**

```bash
git branch --show-current                       # 记录（计划评审时为 feature/vscode-extension）
git rev-parse --short=8 HEAD                    # 记录（计划评审时 7e6aee3b，父提交 dev 3a261297）
git diff --quiet 3a261297 HEAD -- pkg cmd frontend npm && echo SAME-AS-BASELINE   # 必须输出
git status --porcelain -- pkg cmd frontend docs frontend/e2e     # 期望：空
grep -n '{Name: "status"' pkg/turn/slash.go                                   # :99
grep -n 'case "status":' pkg/tui/run.go                                        # :1417
grep -n 'func (c \*commandController) handlePermissions(' pkg/tui/commands.go   # :2660
grep -n 'func (s \*ChatSession) BatchProject' pkg/tui/chat_session.go           # :329
grep -n 'safety.ResolveRegisteredContext(env.Root, project.Root)' pkg/process/runner_pool.go   # :1003
grep -n 'launchProject, trustErr = safety.ResolveRegisteredContext(root, launchDir)' pkg/process/open.go   # :445
grep -n 'sqlDB, err := state.OpenStateFromHome' pkg/process/open.go                                          # :468
grep -n 'activeAgent := activePrimaryAgent(root, cfgRoot)' pkg/process/open.go                               # :586
grep -n 'Trusted:        safety.TrustedRoot(launchProject)' pkg/process/open.go                              # :630
grep -n 'launch, err := safety.ResolveRegisteredContext(env.Root, env.LaunchProject.Project.Root)' pkg/process/config_apply.go   # :132
grep -n -F 'if body.Trust || projectType == state.ProjectTypeBatch {' pkg/gateway/api_extra.go  # :7275
grep -n -F 'if body.Trust || state.NormalizeProjectType(updated.ProjectType) == state.ProjectTypeBatch {' pkg/gateway/api_extra.go   # :7377
grep -rn 'safety.ResolveRegisteredContext(s.Home, p.Root)' pkg/gateway | wc -l  # 9
grep -n 'trusted, projectErr = safety.IsTrusted(p.Home, project)' pkg/assembly/rules_hook.go   # :184
grep -n 'func TrustedProjectSkillRoots' pkg/skill/roots.go                      # :94
grep -n 'func trustedProjectRoot' pkg/safety/runtime.go                        # :292
grep -n 'var batchViewerCommands' pkg/tui/chat_surface.go                      # :281
ls pkg/tui/*.go pkg/process/*.go pkg/skill/*.go | grep -v _test.go | wc -l     # 86（33 + 34 + 19）
```

基线门禁（计划模式内无法实跑会写文件的命令，由执行者跑）：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/process ./pkg/gateway ./pkg/state ./pkg/assembly ./pkg/skill ./pkg/safety ./pkg/run -count=1   # 期望全 ok
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1                                                                            # 期望全 ok
CGO_ENABLED=1 go test ./pkg/architecture -count=1                                                                               # 期望只有 deliverables_outcome_test.go 那一条红
```

**Step 1 — 落盘计划与原型**：复制为 `plans/381-…`；`plans/README.md` 追加审核信息与索引行（TODO）；跑 `node plans/381-project-type-tui-prototype.test.cjs` → `passed 14`；sha256 回填 §2。

**Step 2 — `pkg/process`：`ProjectTypeInput`**（§5.4）。测试 `config_apply_test.go`：`TestProjectTypeInputSeedsSandboxOnlyWhenBecomingBatch`（默认 true / false 各一；batch→batch、coding→office、`"Batch"` 不种）、`TestProjectTypeInputRejectsAnUnknownType`。检查：`go test -tags fts5 ./pkg/process -run 'TestProjectTypeInput' -count=1`。

**Step 3 — `pkg/gateway`：PATCH 的播种改调 + 补测试**（§5.4；信任行的删减留到 Step 8 一起）。新增表驱动 `TestProjectPatchToBatchSeedsTheSandboxSwitch`：未显式带 → `batch_sandbox` = 安装默认（把全局 `batch.sandbox_default` 设为 false 使播种可观测）；显式 `batch_sandbox: false` 保留；`"Batch"` 解析为 batch 但不播种。检查：`go test -tags fts5 ./pkg/gateway -run 'TestProjectPatch' -count=1`。

**Step 4 — `pkg/process`：`RebindConversation`**（§5.5）。测试 `TestRebindConversationMovesAnUnbornSessionToTheNewType`（照 `:1168-1171` 的夹具）。检查：`-run 'TestRebindConversation|TestEnvironmentBindConversation'`。

**Step 5 — B1：`ProjectLaunchContext` + 池条目 + 基础运行时**（§5.7）。测试：
- `runner_pool_test.go`：`TestBatchProjectRunnerIsTrustedWithoutARecord`（跑批项目、不 `MarkTrusted`：条目 `Deps.Trusted` 为真、`ResolveSessionMCP` 含项目服务器、`skill.AgentSkillRoots` 含项目技能根）；`TestUntrustedProjectRunnerKeepsItsRecordedDecision`（非跑批未受信任项目不变——`:1067` 的用例已覆盖，确认仍绿）。
- `open_test.go`（配对 `open.go`）：`TestBatchLaunchFolderKeepsItsBaseRuntimeTrusted`（启动目录是已登记的跑批项目、无记录 → `Deps.Trusted` 为真、有效沙箱是 `workspace-write` 而不是只读；非跑批未受信任目录仍是只读；**启动目录经符号链接拼写**的子用例——`t.TempDir()` 下建一个链接指向跑批项目根、以链接路径启动——同样命中）；**护栏对照**：已登记、没有自己的 `.git`、位于某个 checkout 内的非跑批项目，启动上下文的根与信任和改动前一致（先记录改动前的值再断言）；`config_apply_test.go`：`AdoptLaunchTrust` 对跑批启动目录按类型解析，对上述非跑批项目结果不变。
检查：`go test -tags fts5 ./pkg/process -run 'TestBatchProjectRunner|TestProjectRunnerDerivesSandbox|TestBatchLaunchFolder|TestAdoptLaunchTrust' -count=1`。

**Step 6 — B1：网关 9 个解析点；B2：规则注入；B3：技能线；B4：权限规则文件**（§5.7）。测试：
- 网关：跑批项目（无记录）的 MCP 预览路由成功；**技能开关经 Web 路由**（`api_extra.go:5474` `handleSkillsToggleWith`）对一个无记录的跑批项目关掉项目技能 → 回包 `enabled:false`，随后的列表也反映（照 `skills_download_test.go:283` 与 `api_extra_test.go:2129` 的夹具）；既有路由测试全绿。
- `pkg/assembly`：`TestBatchTurnLoadsProjectRulesWithoutATrustRecord`；`rules_test.go:230`、`rules_hook_test.go:220` 的「未受信任不注入」对非跑批仍绿。
- `pkg/skill`：`offline_test.go` 加「`ProjectTrusted` 为真且无记录 → 项目级安装成功；为假仍报 trust」；`state_test.go` / `service_test.go` 加「`DiscoverForTrustedProject` 列出项目技能；`Service.SetEnabledPaths` 在 `ProjectTrusted` 为真、无记录时能把项目技能写进 disabled（写路径），为假时写不进去」。
- `pkg/safety`：`TestProjectSettingsPathTrustsABatchProjectByRule`；`pkg/run`：构造 `Paths` 的用例断言 `ProjectTrusted` 随类型。
检查：`go test -tags fts5 ./pkg/gateway ./pkg/assembly ./pkg/skill ./pkg/safety ./pkg/run -count=1`。

**Step 7 — TUI 会话方法（A）**（§5.3）。`chat_session_test.go`：`TestProjectTypeStateTellsABornConversationFromOneThatHasNotStarted`、`TestSetProjectTypeKeepsABornConversationOnItsBirthType`、`TestSetProjectTypeRebindsAConversationThatHasNotStarted`、`TestSetProjectTypeToBatchSeedsTheSwitchAndWritesNoTrustRecord`、`TestSetProjectTypeFailureChangesNothing`（`s.ProjectID` 指向不存在的行）。检查：`-run 'ProjectType|SetProjectType'`。

**Step 8 — B5 + B6：删掉写入，改测试、文档、e2e 源**（§5.7、§5.4 末尾）。`api_extra_test.go`：`:1131` 改写为 `TestProjectsBatchNeedsNoTrustRecord`（201、`trust_recorded == false`、`IsTrusted` 为假、详情可读）；`:1258` 改写为 `TestProjectPatchToBatchRecordsNoTrust`；`:930` 保留并加一段「`project_type: batch` + `trust: true` → 显式决定照记」。文档与 e2e 源按 B6。跑两份 e2e：`FOREBRAIN_E2E_SPEC='batch-tasks.spec.ts batch-live-attach.spec.ts' scripts/acceptance/web_e2e.sh`（需先 `make ui`；跑不了在验收记录写明）。检查：`go test -tags fts5 ./pkg/gateway -count=1`。

**Step 9 — A：处理器、文案、命令表、分发、白名单；B3 的 TUI 两处**（§5.1、§5.2、§5.6、§5.7 B3）。`commands_test.go`：纯函数逐字断言 + `handleProject` 流程（替身会话内嵌现有 `fakeSession` 并实现两个方法；选择器用 `recordingSelector`，`SelectRich` 在 `:804`，没记 `defaultIdx` 就在该类型上补记）；`slash_test.go:448` 的 `want` 加 `"project": SubagentViewGlobal`；`run_test.go` 在 `:8445` 旁加「跑批会话里 `/project` 不被拦」。命令表这一行与处理器在同一步落地（避免命令先出现在菜单里却答 unknown 帧）。检查：`go test -tags fts5 ./pkg/turn ./pkg/tui -count=1`。

**Step 10 — 门禁**

```bash
gofmt -l cmd pkg third_party plugins                                  # 无输出
CGO_ENABLED=1 go vet -tags fts5 ./...                                  # exit 0
CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1   # 除预存红外全 ok
CGO_ENABLED=1 go test ./pkg/architecture -count=1                      # 仅 TestTestFilesCorrespondToProductionFiles 预存红
grep -rn 'safety.ResolveRegisteredContext(s.Home, p.Root)' pkg/gateway | wc -l   # 0
grep -n 'MarkTrusted' pkg/gateway/api_extra.go                           # 只剩创建与 PATCH 各一处，且都只在 body.Trust 下
git status --porcelain | grep -v '^.. plans/'                           # 只含 §6 In scope 的文件
```

**Step 11 — tmux 真机验收**（§10）。

**Step 12 — 收口**：回填验收记录；索引行改 DONE；清理。**重建 `npm/dist` 的 vendored 二进制需你另行点头**（`npm/scripts/build-platform-packages.sh`，或 `make release` 后 `cd npm && npm link`）。

## 8. 测试矩阵

| 层 | 用例 | 钉住什么 |
|---|---|---|
| `pkg/process` | `TestProjectTypeInput*`；`TestRebindConversation…`；`TestBatchProjectRunnerIsTrustedWithoutARecord` | 播种只有一份实现且条件不变；未开始会话重绑；跑批项目无记录也加载项目资源 |
| `pkg/gateway` | 既有 `:1195`、`:930`（不改 / 只加段）；新增播种表驱动；`:1131`、`:1258` 改写；跑批无记录的路由用例 | PATCH 播种等价；创建与 PATCH 不再为跑批写记录；管理路由对跑批不卡 |
| `pkg/assembly` | `TestBatchTurnLoadsProjectRulesWithoutATrustRecord` + 既有「未受信任不注入」 | 规则注入对跑批豁免，对其它类型不变 |
| `pkg/skill` | `ProjectTrusted` 的安装与发现用例 + 既有两条「需要信任」 | 技能线由调用方决定，按根查库只剩一处 |
| `pkg/safety`、`pkg/run` | `Paths.ProjectTrusted` | 项目权限规则文件对跑批可读 |
| `pkg/turn` | `TestEveryBuiltinCommandHasASubagentViewScope`（`want` 加一行）+ 既有表驱动 | 新命令登记完整 |
| `pkg/tui` | 文案纯函数逐字；`handleProject` 流程；`SetProjectType` × 4 + 状态读取；查看器放行 | §2.2、§3.1 |
| `pkg/architecture` | 全量（预存红除外） | 层级、文件数、配对 |
| 原型 | `node plans/381-project-type-tui-prototype.test.cjs` | 14 项 |
| e2e | `batch-tasks.spec.ts`、`batch-live-attach.spec.ts` | 跑批项目不带 `trust` 也能建、能跑 |

失败路径：存储失败不变更（不发回复帧）；重绑失败不回滚（回复照发 + 另报错，首回合由 `RunnerForTurn` 兜底）；`ProjectLaunchContext` 解析出错时各调用点的错误处理与今天一致（跑批也照样报错，不吞）。

## 9. 非功能要求

| 维度 | 风险 | 对策 | 证明 |
|---|---|---|---|
| 安全 | 跑批项目按类型视同受信任 | 与今天零新增暴露（今天靠自动写入的记录同样全部加载）；同意点是「把项目设为跑批」这一个动作；非跑批项目的信任门一字不动 | `TestUntrustedProjectRunnerKeepsItsRecordedDecision`、既有「需要信任」用例全绿 |
| 安全 | 少了信任记录这个「用户显式同意」的痕迹 | 这是裁决本身；同意点的界面文案（Web 确认弹窗、TUI 确认页）写明跑批的后果 | 原型场景 9、10 |
| 延迟 | 打开面板读库两次 | 与 `/status` 同量级 | 验收 |
| 延迟 | 未开始会话改类型后重建一代 runner | 只对未开始会话；走启动时同一条路径；面板此时已关闭 | 验收场景 1 |
| 延迟 | 进程启动多一次按根点查项目行（§5.7 B1） | 单行点查，只在有启动目录时发生一次 | `TestBatchLaunchFolderKeepsItsBaseRuntimeTrusted` 与既有 open 用例照常通过 |
| 资源 | 每次在未开始会话上改类型多一代池条目 | 超过 LRU（8）立即关、或 24 小时后回收，空闲 30 分钟先放 MCP 连接（`runner_pool.go:108-123`、`:892-905`） | 既有 `TestSweep…`、`TestEvictStale…` |
| 鲁棒 | 写到一半失败 | 单条 UPDATE 原子；B 部分不再有任何写入，无半态 | `TestSetProjectTypeFailureChangesNothing` |
| 鲁棒 | 改类型时恰有回合在跑（允许执行，裁决 b） | 有回合必有会话行 → 不重绑，只改项目行；`Born` 在写入**之前**读取（§5.3），读写之间建行时仍朝新类型重绑 | `TestSetProjectTypeKeepsABornConversationOnItsBirthType` |
| 鲁棒 | 重绑失败 | 不回滚；旧绑定已解除 | Step 7 |
| 鲁棒 | 重复触发 MCP 启动监视 | `WatchMCPStartup` 按「会话 + runner」幂等 | 验收场景 1 |
| 可维护 | 三条线各自的「按规则受信任」布尔位会不会漂移 | 都来自同一个 `ProjectLaunchContext` 的 `IsTrusted()`（TUI 技能面板另加 `BatchFolderProject`，因其启动上下文不是按项目行解析的）；Step 10 的 grep 钉住网关不再直接解析 | 门禁 grep |
| 可维护 | 文案散落 | 四个纯函数 + 逐字测试 + 原型 | `commands_test.go` |
| 体验 | 改完不知道当前会话变没变 | 面板始终点名屏幕上的会话；回复给出下一步 | 原型 11 场景 |

## 10. tmux 真机验收（必做）

一律临时 `FOREBRAIN_HOME` 与临时目录，不碰真实 `~/.forebrain*`。二进制带 `-tags fts5`。用 `run-forebrain` 技能的 `driver.sh`：`$D build` → `$D start reply "<应答文本>"`（**不要用默认的 `hang`**）→ `$D key` / `$D screen` / `$D messages` / `$D db` → `$D stop`。驱动的项目目录不是 Git 仓库 → 默认 office 项目，正合本次故障场景。状态库在 `$WORK/home/state/forebrain.state.sqlite`；信任存储是 `safety.StatePath(home)`（`pkg/safety/engine.go:995`）指向的文件。

| # | 场景 | 操作 | 断言 |
|---|---|---|---|
| 1 | 复现→修复（本次故障路径） | 启动后不发消息，先 `/project` → `Coding` → `Yes` → 发一条消息 | `✓` 在 Office work、副标题含 `has not started`；回复帧 `● project` + `takes the new type when it starts`；库：项目与该会话都是 `coding`；回合正常结束 |
| 2 | 已开始的会话保留 | 重启前先经 `/project` 改回 Office work；先发一条消息，再 `/project` → Coding → Yes；`/new` → 发消息 | 回复含 `This conversation keeps Office work; /new starts a Coding conversation`；库里旧会话 `office`、新会话 `coding` |
| 3 | 取消零写入 | 确认页 Esc → 选择页 Esc；在当前类型上 Enter | 无回复帧；项目行 `updated_at` 不变 |
| 4a | 改成跑批再改回（无任务） | 未开始时 `/project` → `Batch runs` → `Yes`；发一条消息；`/project` → `Coding` → `Yes`；重启 | 回复为跑批句、不含 `/new`；库 `project_type = batch`、`batch_sandbox = 1`；**信任存储里没有该项目的记录**；消息被拒 `This conversation is not a task execution.`；此后 `/project` 仍可用，回复含 `Restart Forebrain Harness…`；重启时**不弹信任确认**（既有行为），直接是查看器或 `No tasks yet…` 提示；改回后重启是普通会话 |
| 4b | 跑批任务在没有信任记录时照常加载项目资源 | 仅当隔离环境能起网关 + 假模型并经 REST 建任务与执行时做：项目根放 `.forebrain/skills/demo/SKILL.md` 与一条项目 MCP 配置，跑一次执行 | 日志 / 状态行显示项目技能与 MCP 已加载。做不了就写明「未覆盖，分支由单测钉住」 |
| 5 | 中断自救 | 确认页 Ctrl+C | `EXIT_CODE=0`；再启动可恢复 |
| 6 | 非 TTY 不变 | `FOREBRAIN_HOME=$WORK/home forebrain gateway start </dev/null` | 行为与改动前一致 |

## 11. Done criteria（机器可查）

- [ ] 原型回归 `passed 14`、`browserErrors []`；sha256 已回填。
- [ ] `gofmt -l cmd pkg third_party plugins` 无输出；`go vet -tags fts5 ./...` exit 0。
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1`：除预存红外全 ok。
- [ ] `grep -rn 'safety.ResolveRegisteredContext(s.Home, p.Root)' pkg/gateway | wc -l` = 0；`grep -c 'Name: "project"' pkg/turn/slash.go` = 1；`grep -c 'case "project":' pkg/tui/run.go` = 1。
- [ ] `pkg/gateway` 里 `MarkTrusted` 只剩创建与 PATCH 各一处，且都只在 `body.Trust` 下；`trust_not_recorded` 在 `pkg/` 中零命中。
- [ ] `ls pkg/tui/*.go | grep -v _test.go | wc -l` = 33、`pkg/process` = 34（未新增生产文件）。
- [ ] `git status --porcelain | grep -v '^.. plans/'` 只列出 §6 In scope 的文件；未 commit。
- [ ] §10 场景 1–5 的取证（4b 或其「未覆盖」说明）已贴入计划；`plans/README.md` 索引行为 DONE。

## 12. STOP 条件

1. **漂移**：Step 0 的内容判据不过，或任一 grep 锚点的内容对不上 → 停，重核 §4。
2. **PATCH 行为变化**：`TestProjectPatch*` 任一变红，或语义有差异 → 停，不改断言，回报。
3. **重绑不可行或有副作用** → 停，回报证据；不改用「一律 `/new`」掩盖。
4. **会话行时机与 §3.2 不符** → 停，「未开始」分支按实际重判。
5. **非跑批项目的信任行为发生变化**（任何「需要信任」的既有测试变红）→ 停，这是 B 部分的红线。
6. **预存红扩大** → 停，先分清归属。
7. **需要改 Out of scope 的文件**才能继续（例如某个网关解析点手里拿不到整行）→ 停，回报。
8. **同文件并发改动**：d1 没有对写 `plans/README.md` 回「好」，或他人改了 §6 的文件 → 停，`peer_send` 协调；主树在 `feature/vscode-extension` 期间不 commit、不自己切分支。

## 13. 维护注与后续

- **评审重点**：① `ProjectLaunchContext` 是唯一的「按类型视同受信任」实现，三个布尔位（`trustByRule`、`skill.Service.ProjectTrusted`、`safety.Paths.ProjectTrusted`）都源于调用方的解析结果；② `handleProjectOne` 逐项等价；③ `RebindConversation` 只被「未开始」分支调用；④ 文案与原型逐字一致。
- **被本计划取代的旧裁定**：plan 145「创建为 / 改成 batch 的项目必须同时记录信任」、plan 216 §0.4「记录仍然写入，因为加载器读它」——以 owner 2026-11-25 裁决为准。
- **存量信任记录不清理**：过去为跑批项目自动写入的记录留在 `safety.StatePath(home)`；对跑批项目已无人读。清理需要拿项目表反查并改写用户的信任存储文件，收益为零、有误删风险，本期不做。
- **基础运行时豁免没覆盖的组合**：外层 checkout 自己也被登记为（非跑批）项目时，启动解析落在那一行，备用查询不会执行，内层那个已登记的跑批项目不会被认出来，基础运行时按外层项目解析。这与今天相比不是回退（今天外层未受信任时也是这样），只是这条豁免不覆盖「并列登记」的组合。
- **报告口径差异**：`/status` 的规则来源（`InstructionSources()`）没有会话上下文，对没有记录的跑批项目仍会报「project not trusted」，而实际注入已加载；只影响这一行报告。
- **既有边角，不修**：`DispatchSurfaceTurn` 的 `sessionID == "" && s.BatchProject(ctx)`（`chat_surface.go:55`）在 TUI 永远不命中；`ApplyPermissionPreset` 的跑批拒绝看的是基础 runner 的类型（`chat_session.go:2682`）。
- **类型标签两个来源**：TUI 常量与 Web 的 `frontend/src/locales/index.ts`；改其一要同改另一。
- **后续候选**：office 提示词「封闭式枚举」的措辞仍会让弱模型把源码改动排除在交付物之外；本计划让用户能自己纠正类型，不改那段措辞。

## Implementation Status

**VERDICT: APPROVE**（2026-11-25，评审方独立复核，不采信执行器自报）。

- **执行**：owner 指定模型 alibaba/deepseek-v4.1-flash-1017，worktree `.worktrees/tui-project-type-switch`，分支 `tui-project-type-switch`，基于 `7e6aee3b`（父提交 dev `3a261297`）。6 个提交，未 push / 未合并——**合并由 owner 裁决**：
  `cc706717` feat(project): share the sandbox seeding between every type change
  `ac9b3c69` feat(process): rebind an unstarted conversation after its project's type changes
  `2ff21da5` feat(process): a batch project needs no trust record
  `6f8fcfe3` feat(gateway,skill,assembly,safety): a batch project needs no trust record
  `5b226e44` feat(tui): add /project to show and change the project type
  `72507120` fix(tui): draw /project's current-type mark beside the label
- **范围**：`git diff --stat 7e6aee3b..HEAD` 共 36 个文件，与 §6 In scope 逐一对应，无范围外改动；worktree `git status` 干净（依赖软链未提交）。主树未 commit、未切分支。

**各步状态**：Step 0 done（漂移与锚点全过）；Step 1 由评审方落盘（本节 + `plans/381-*` 三件 + README 两处增量）；Step 2–10 done；Step 11 done（执行器一轮 + 评审方独立复跑一轮）；Step 12 由评审方收口。4b（网关+假模型+REST 建任务验项目资源加载）未做端到端，分支由 `TestBatchProjectRunnerIsTrustedWithoutARecord`、`TestBatchProjectRoutesNeedNoTrustRecord` 钉住——计划本就允许写明未覆盖。

**评审方亲跑的判据**（全部在 worktree 内）：
- 原型回归 `node plans/381-project-type-tui-prototype.test.cjs` → `passed 14`、`browserErrors []`；落盘指纹 html `3225bc48…`、test `8c6f280a…`（与待确认稿一致）。
- `gofmt -l cmd pkg third_party plugins` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./...` exit 0。
- `FOREBRAIN_HOME=$(mktemp -d) CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1`：唯一 FAIL 为预存红 `TestTestFilesCorrespondToProductionFiles`（`deliverables_outcome_test.go`，与基线一致）；其余全 ok（tui 200s、process 230s、gateway、assembly、skill、safety、run、turn、cmd 等）。
- grep 判据：网关直接解析 0 处；`Name: "project"` 1 处；`case "project":` 1 处；`api_extra.go` 的 `MarkTrusted` 仅创建/PATCH 各一处且都在 `body.Trust` 下；`trust_not_recorded` 在 `pkg/` 零命中；生产文件数 tui/process/skill = 33/34/19。
- 真机独立复跑（verification 子代理，隔离 home/端口 8743，`start reply`）：S1/S2/S3/S5/S6 全 PASS（含 S5 捕到 `EXIT=0`、`tui.err` 0 字节）；E2E `batch-tasks` + `batch-live-attach` 4/4 PASS。S4a 按意图 PASS：唯一未过的字面断言「trust 文件不含项目根」经 mtime 取证归因于 driver 首次 office 启动自动答信任页所写的旧记录（早于 batch 切换），切换本身零写入；「全新 home 改 batch 不写记录」由单测钉住。存量记录不清理是 owner 裁决（§13），非缺口。
- 全量 diff 逐段读过；新增测试逐条审计断言（播种四支、重绑前后对照、护栏先记改前值、技能写路径经 Web 路由落地、文案逐字、失败两态）。

**已声明偏差（均核实成立、按 merit 接受）**：
1. `open.go` 插入点重派生 `cfgRoot = effectiveFor(loaded, launchProject)`，解析出错保留原上下文只 `slog.Warn`——必要：`cfgRoot` 在 :573 已派生并喂给后续一切信任敏感消费；计划里的 `_ =` 会丢启动项目。:573–:586 之间唯一消费 `activePrimaryAgent` 与信任无关。
2. `✓` 拼在 label 上而非 `SelectItem.Mark`——计划前提有误：`panel_columns.go columnRows` 只在不带 description 的行画 mark（此前无任何生产 Mark 使用点）。真机可见；与原型相比颜色随焦点（原型恒强调色），属可接受的外观差异。
3. 确认页 Ctrl+C = 取消回选择页（选择器通用行为，非本次引入）；S5 以正常退出路径取证 `EXIT=0`。
4. 跑批拒绝文案实为 `this batch conversation has no task execution`（`RunnerForTurn` 路径的原始 message；`chat_surface.go` 的映射句属另一条准入路径）——既有行为，未改。
5. 护栏测试用非跑批内层项目（跑批内层被认出正是本计划意图）。
6. `Born=false` 时 `SessionType` 留空。
7. UI 构建补了 `node_modules`、`ai-elements-vue/packages/*` 软链（环境性，未提交）。

**评审方另观察到的微适配（计划意图内、不改行为，记录备查）**：
- `handleProject(sessionID)` 内部用 `context.Background()`（计划草图带 ctx 参）；与 `handlePermissions` 同形，`projectTypeEditor` 接口仍带 ctx。
- 测试用既有 `scriptedSelector.richSteps` 而非 `recordingSelector`；`SelectRich` 的 `defaultIdx` 未在单测断言（代码传字面量 0；真机断言光标在 No）。
- `skill/state.go discoverSkillEntries` 在 trusted 变体下仍先调一次 `TrustedProjectSkillRoots` 再覆盖——多一次冗余存储读，注释「minus the store read」仅描述覆盖块自身。**后续清理项**：把该调用移入 else 分支。正确性无影响。
- safety 新测试放在 `sandbox_config_test.go`（配对既有生产文件，合法）。

**未做 / 遗留**：4b 端到端（见上）；`npm/dist` vendored 二进制未重建（需 owner 点头，Step 12）；§13 各维护注记事项；`/status` 规则来源报告对无记录跑批项目的口径差异（代码已留注释）。

**落地（2026-11-25，owner 指示「合并回主树当前分支」）**：`git merge --ff-only tui-project-type-switch` 于主树 `feature/vscode-extension`，`7e6aee3b..72507120`，36 文件与评审时逐字一致；主树 `plans/` 的 30 项未提交改动原样保留，d1 的 worktree `process-card-head-names-command` 不受影响（已 peer 知会）。**主树门禁复跑绿**：gofmt 无输出、vet exit 0、`go build -tags fts5` OK、全量 `go test -tags fts5 ./pkg/... ./cmd/forebrain` 唯一 FAIL 仍为预存红 `TestTestFilesCorrespondToProductionFiles`。worktree 与分支按交接约定保留未删；`npm/dist` vendored 二进制仍未重建（需 owner 点头）。

**再落地（2026-11-25，owner 指示「合并回 dev 并删分支」）**：主树 `git checkout dev` → `git merge --ff-only feature/vscode-extension`（dev `3a261297..72507120`，含 d1 的 376/379 提交 `7e6aee3b` 与本计划六提交，共 83 文件）→ `git branch -d feature/vscode-extension`（was `72507120`）。主树 30 项 plans/ 未提交改动原样保留；合并后树与已通过门禁的 `72507120` 逐字一致（`git diff` 空），门禁结论沿用。d1 的 worktree `process-card-head-names-command` 与本计划 worktree `tui-project-type-switch` 均不受影响（已 peer 知会）。未 push。
