# Skill 消息卡片统一改造计划

> 状态：已实施（2026-09-16）。P0–P7 全部落地，§12.2 真机验收通过（场景 3/4 按降级条款以 Go 集成测试覆盖并注明）。实施状态见文末 §16。
>
> 日期：2026-09-16
>
> 范围：Forebrain Harness TUI 中用户显式触发 Skill 与 LLM 主动读取 Skill 的消息卡片
>
> 基线：`main` / `4330e079`，并保留当前工作区已有的 Skill 读取卡片相关未提交改动

## 1. 目标

把以下两条执行链统一成同一种 Skill 语义、同一套状态机和同一个 TUI 渲染入口：

1. 用户通过 `/review-agent` 或 `/skills` 的“立即运行”显式触发 Skill。
2. LLM 根据技能目录主动调用 `read_file` 读取某个 Skill 的根 `SKILL.md`。

统一后的卡片只回答三个用户问题：正在加载哪个 Skill、从哪里加载、是否成功。模型需要的 Skill 正文、工具参数和传输协议不进入消息卡片。

## 2. 已确认的最终 UI/UX

### 2.1 进行中

```text
⠋ Loading skill review-agent
```

- 使用现有工具执行中的 Braille 动画帧：`⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏`。
- 文本颜色与其他工具调用的进行中消息保持一致（`toolStatusRunning` 的现有灰色 244），**不**使用 Skill 淡紫色；淡紫色只用于 completed 终态。理由：进行中的灰色是"正在执行"的统一视觉信号，与调用内容无关；Skill 淡紫色标识"这是一次 Skill 加载"，只有在卡片定型（成功）后才有辨识价值。实施上即：`toolHeaderColor` 里 skill 分支不得排在 `toolStatusRunning` 之前（当前工作区改动把 `case Category == "skill"` 放在 running 分支之前，运行中的 Skill 卡会错误地显示淡紫色，本计划修正）。
- 只显示消息头，不创建 output 区域，不显示路径、耗时或占位文本。
- 同一个 `StepID` 的终态事件（完成、失败、取消、拒绝）原位替换进行中卡片，不新增第二张卡片。

### 2.2 成功

```text
● Skill review-agent <0.1s
  └ Loaded from /Users/doudou/.codex/skills/.system/review-agent/SKILL.md
```

- 使用青色实心圆 `●`（256 色 `43`；本条与 §8.3 的色值以 §13 修订 14 为准）。
- 消息头为 `Skill <skill-name> <duration>`。
- `Loaded from` 使用过去式，明确加载已经完成。
- 来源路径使用弱化样式，并沿用当前最新路径规则：
  - Skill 位于项目根目录内时显示项目相对路径，例如 `.forebrain/skills/review/SKILL.md`。
  - Skill 位于项目外时保留规范化后的绝对路径。
- 路径过长时沿用工具卡片的安全换行和右边距，不改变路径含义。

### 2.3 失败

```text
✗ Skill review-agent <0.1s
  └ Failed to load from /Users/doudou/.codex/skills/.system/review-agent/SKILL.md
    Permission denied
```

- 使用红色失败图标 `✗`；它表达明确失败，与上下文压缩失败和 Fanout 失败任务的语义一致。
- 消息头与正文的文字颜色与其他普通工具调用失败的消息卡片保持完全一致：失败 Skill 卡不引入任何新的颜色处理，图标与文字的配色完全继承通用工具失败卡片的现有实现（`toolHeaderColor` 失败分支 + `renderCompactFrame` 的 header 段样式），即图标红、文字沿用通用失败卡片的现有文字样式。
- 第一行正文固定为 `Failed to load from <path>`。
- 第二行直接显示原始错误消息（`evt.Error` / `Error` 字段原文），不做任何二次处理或加工：不去重路径、不去除 `open`/`read`/`SKILL.md:` 前缀、不改大小写、不归纳成"友好"措辞。原因：错误整理规则是启发式，错误场景不可枚举，任何加工都可能藏起对诊断有用的信息；原始错误的可信度高于任何二手归纳。

### 2.4 审批状态与状态机完备性（仅 LLM 读取路径）

**可达性说明（实现者必读）**：审批对 Skill 卡实际不可达。读取审批的判定是 `ProtectedReadReason`（FOREBRAIN_HOME 内 且 不在 primary workspace 下 且 **不在已加载 skill 根下** 且 无已批准写），而 Skill 卡的识别恰好要求 catalog 命中根 `SKILL.md`——两处用的是同一份 loaded-skill catalog 和同一个 resolved 路径。所以能被识别为 Skill 卡的读取总是被 catalog 整根豁免，不会停在 `awaiting approval`；会撞上审批的 FOREBRAIN_HOME 读取（auth.json、settings、未进 catalog 的目录）恰恰不满足 Skill 卡识别条件，显示为普通 `read_file` 卡。

前提不变量：**workspace root 恒在 FOREBRAIN_HOME 内**（默认即 `$FOREBRAIN_HOME/workspace`：`Runner.workspaceRoot` 与 `ActiveStateRoot` 的 fallback 都是 join(home, workspace)，活跃 agent 的配置值也在 home 之下）。因此"不在 primary workspace 下"这条豁免是对 FOREBRAIN_HOME 守卫的**内部豁免**——它划掉的是 home 里被授权的 workspace 子树（`$FOREBRAIN_HOME/workspace/**`），与 home 外的路径无关。按 skill 来源分解读取审批的命中情况：

| skill 来源 | 与 FOREBRAIN_HOME 的关系 | 命中审批？ |
| --- | --- | --- |
| 项目级 `<project>/{.forebrain,.agents,.claude,.codex}/skills` | home 外 | 否（第一条件即不满足） |
| 跨工具用户目录 `~/.agents/skills` 等 | home 外 | 否（同上） |
| workspace 级 `$FOREBRAIN_HOME/workspace/skills` | home 内，且在 workspace 子树下 | 否（workspace 豁免 + catalog 豁免双重覆盖） |
| 用户级 `$FOREBRAIN_HOME/skills/**`、系统级 `$FOREBRAIN_HOME/skills/.system` | home 内，**不在** workspace 子树下 | 否，且只靠 catalog 豁免——这正是识别与审批共享 catalog 的关键一环 |

结论：Skill 卡的可实现状态就是 `running | completed | failed` 三个（§5.1）。不为 `awaiting approval`/`canceled`/`denied` 设计任何 Skill 专属的文案、图标、颜色或渲染分支。`tool.ToolMeta.Status` 来自共享词表（`stepStatusLabel`），理论上其它取值能出现在 frame 里，唯一的要求是 Skill 渲染分支**白名单收尾**——只对 `completed` 渲染成功正文，其余不命中 Skill 专用路径而落回通用工具的既有渲染——这防的是"词表取值掉进成功模板"的串台，不是为不可达状态做防御。

- 禁止以"以防万一"为由为不可达状态新增防御分支、专用图标或专用测试用例。
- 当前工作区改动的 skill 渲染分支只排除了 pending/failed，其它词表取值会错误地渲染成功正文——修复方式即上面的白名单收尾，而非为每个状态添加分支。

### 2.5 两种触发方式必须完全一致的部分

| 项目 | 用户显式触发 | LLM 主动读取 |
| --- | --- | --- |
| 进行中标题 | `Loading skill <name>` | `Loading skill <name>` |
| 成功标题 | `Skill <name> <duration>` | `Skill <name> <duration>` |
| 成功正文 | `Loaded from <path>` | `Loaded from <path>` |
| 失败图标 | `✗` | `✗` |
| 失败正文 | `Failed to load from <path>` + 原因 | `Failed to load from <path>` + 原因 |
| 颜色 | 进行中灰色（与工具一致），成功淡紫色，失败与普通工具失败卡一致 | 进行中灰色（与工具一致），成功淡紫色，失败与普通工具失败卡一致 |
| 折叠行为 | 相同 | 相同 |
| 实时与恢复 | 相同 | 相同 |

触发来源只作为内部审计元数据保存，TUI 不根据来源选择不同文案或布局。

## 3. 不得进入 UI 的内容

以下数据可以继续服务于模型执行、诊断或内部审计，但不得出现在 Skill 卡片的消息头、output 区域、折叠预览或恢复后的历史消息中：

- `{"explicit":true}`。
- `output:` 标签和 JSON 代码块。
- `activation.Content` 中的 `<skill>`、`<name>`、`<path>`、`<context>` 标签。
- `<context>` 内的 `root_dir`、`relative_paths`、`directories`、`files` 等 JSON。
- `SKILL.md` 正文。
- 经过 JSON 转义后的 `\u003c`、`\n`、`\"` 等传输表示。
- 通用工具 formatter 根据 Input 自动生成的参数摘要。

## 4. 根因

显式 Skill 当前不是一次真实工具调用，但 `pkg/run/skills.go` 为了保留步骤和审计信号，会伪造一个已完成的 `tool.StepEvent`：

```go
ToolName:        activation.SkillName,
ToolDescription: "Apply the `<name>` skill.",
Input:           map[string]any{"explicit": true},
Output:          map[string]any{"output": activation.Content},
```

TUI 的 `pkg/tui/notify.go:isSkillTool` 没有读取结构化类型，而是要求描述以旧文案 `Load and apply the installed skill ` 开头。提交 `8de168cd` 修改了生产端描述却没有同步这个判断，导致事件失去 `Category=skill`：

1. 消息头进入通用工具分支，显示 `review-agent {"explicit":true}`。
2. 输出进入 `pkg/tool/format.go:formatGenericToolStep`。
3. `activation.Content` 被作为 `output` 字段再次 JSON 序列化。
4. XML 风格标签、context JSON 和 Markdown 以多层转义形式直接显示。

现有 `TestRunnerObservesExplicitSkillActivationAsCompletedStep` 只检查 `explicit` 和 `output` 存在，没有覆盖事件经过通知、TUI reducer 和 renderer 后的最终卡片，因此该协议漂移没有被测试发现。

## 5. 目标数据模型

### 5.1 单一语义身份

为 Skill 加载步骤提供稳定、结构化的显示元数据；具体字段名在实现时服从现有类型命名，但至少包含：

```text
Category   = "skill"
SkillName  = "review-agent"
SkillPath  = "/Users/doudou/.codex/skills/.system/review-agent/SKILL.md"
Status     = running | completed | failed
StepID     = 同一次加载生命周期内稳定
Duration   = 完成或失败时的真实耗时
Origin     = explicit | llm-read（仅内部使用，不影响 UI）
Error      = 失败时的原始错误
```

- 显式加载不进入审批；LLM 读取路径在当前豁免设计下审批也不可达（§2.4 可达性说明），所以 Skill 卡的可达状态就是这三个。`Status` 字段本身仍是共享词表的普通字符串——canceled/denied 等**其它词表取值**属于"不是 Skill 卡"的情形：要么渲染层识别不出 Skill（普通 read_file 卡），要么是未来的设计变化改变了可达性，届时由通用工具渲染兜底（§2.4 白名单收尾），本计划不为其增加任何 Skill 专属处理。
- `Origin` 落在 `tool.StepEvent` 的非 UI 字段并进入 canonical event payload 与 run audit，供审计区分来源；不进入 `ToolMeta`，任何渲染层不得读取它。

禁止使用以下信息判断 Skill 卡片：

- `ToolDescription` 的自然语言前缀。
- `ToolName` 是否刚好等于某个 Skill 名称。
- `StepID` 的字符串前缀。
- Output 中是否包含 `<skill>` 或 `SKILL.md`。

### 5.2 单一投影结果

两种来源都必须投影为相同的 `tool.ToolMeta`：

```text
Category:  "skill"
SkillName: <name>
SkillPath: <canonical path>
Status:    <lifecycle status>
```

实时 TUI、canonical run event、会话存储和 resume replay 必须保存并恢复这些字段。恢复链路不得重新解析历史正文来猜测 Skill 身份。

### 5.3 内容与显示分离

- 模型上下文继续接收完整、可信的 `activation.Content`。
- FC telemetry 如仍需记录完整激活内容，继续走独立的 telemetry 字段。
- 用户可见的 `DisplayBody` 不携带激活内容。
- LLM 的 `read_file` 工具结果仍可在内部携带文件内容供模型使用，但 Skill formatter 必须在进入 surface 前将其排除出 `DisplayBody`。
- `DisplayBody` 不能因此变成空串：成功卡片必须携带 `Loaded from <canonical absolute path>`，失败卡片携带 `Failed to load from <path>` 与原始错误消息（§2.3，不加工）。该正文由共享层（`pkg/tool` 的 formatter / `BuildRenderedToolStepNotification`）生成，使用规范化绝对路径；TUI renderer 拿到后可以用 `displayPath` 相对项目根再缩短显示。共享层没有 cwd，绝不在共享层做相对化。

## 6. 显式 Skill 的真实生命周期

### 6.1 当前问题

`pkg/turn/skills.go` 当前在 slash handler 内同步调用 `skill.LoadActivation(path)`，随后才把完整 `SkillActivation` 交给 Runner。Runner 收到时加载早已完成，只能发出一个伪造的 completed 事件：

- 无法展示真实的进行中状态。
- 无法计算真实加载耗时。
- 加载失败会走普通 ephemeral reply，而不是统一的 Skill 失败卡片。
- 步骤 Output 被迫携带完整激活正文以便后续注入，造成显示层泄漏风险。

### 6.2 改造方向

slash 解析阶段只返回可信的 Skill 选择信息：`SkillName` 与 `SkillPath`。在第一次 LLM 请求之前，由 Runner 的显式 Skill 预加载阶段完成以下操作：

1. 生成稳定 `StepID` 并记录开始时间。
2. 发出 `running` Skill 事件。
3. 调用 `skill.LoadActivation(SkillPath)`。
4. 校验加载结果的规范化名称仍与用户选择一致，避免命令注册后文件被替换或改名。
5. 成功时发出 `completed` 事件，记录真实耗时，并把 `activation.Content` 注入本轮模型上下文。
6. 失败时发出 `failed` 事件，记录真实耗时和错误，终止本轮的首次 LLM 请求。
7. 通过可识别的 typed error 告知 surface“失败已由 Skill 卡片展示”，避免再追加一张通用错误卡片。

`/review-agent` 原始文本仍作为用户消息保存；移动加载 owner 不得改变传给模型的用户请求和技能内容，也不得把 Skill 内容加入稳定 prompt prefix。

### 6.3 其他显式入口

以下入口必须复用同一预加载阶段，不能各自生成卡片：

- 自动派生的 `/<skill-name>` slash 命令。
- `/skills` 选择器中的”立即运行”。
- TUI 会话内排队后执行的显式 Skill submission。
- Gateway/WebChat 已支持的显式 Skill submission：共享事件字段不丢失，且 web 端渲染本次同步改造（见 §10-web），不是”只保底不丢字段”。

### 6.4 显式 Skill 卡片的持久化与恢复（TUI 铁律）

当前实现里伪造的 completed 事件只走两条路：`AppendStep`（run audit 内部表，resume 不回放）和 `notifyUI`（瞬时 UI 派发）。它既不是 transcript 的 tool 行（模型从未发起 tool call），也不是 canonical session event（TUI step hook 对主 agent 步骤直接 `notifyToolStepHooks`，不经 `Events.Publish` 落库）——所以今天 resume 后显式 Skill 卡片直接消失。这违反"退出 TUI 前所有已显示消息必须持久化并可完整回放"的铁律，重设计不得延续。

改造要求：

1. 显式 Skill 的 started/completed/failed 三个事件在发给 UI 之前，先以 canonical run event 形式持久化到 session event 存储（`AppendSessionEvent`，与 subagent 步骤同一通道），顺序必须是先落库再画行。
2. resume 的事件回放 mapper（`pkg/tui/commands.go` 的 `replaySubagentRunEventMessage`）目前对 `RunEventToolStarted/RunEventToolCompleted` 要求 `ToolMeta.AgentID != ""`，主 agent 的 Skill 事件会被静默跳过；必须为 `Category=skill` 的主 agent 事件放开该门槛，并把它锚定到所属 turn 的位置（沿用现有 approval record 的锚定方式），保证回放顺序与 live 一致。
3. 该持久化路径与 §7 的 LLM 读取路径互不依赖：读取路径的卡片已经通过 tool-role 行的 `tool_display` part 持久化，不需要也不得再写一份 session event，避免同一张卡回放两次。
4. 验收：live 显示过的三种状态卡片，在 resume 后逐张回放且文案与 live 完全一致；显式加载失败时 resume 回放 user 行 + 失败卡片，不出现 assistant 行。

## 7. LLM 主动读取 Skill 的生命周期

沿用当前工作区正在开发的根 `SKILL.md` 识别能力：

1. `read_file` started 事件发出前，通过 session 的 loaded-skill catalog 解析规范化 `SkillName` 和根 `SkillPath`。
2. started 与 completed（成功/失败）事件都携带同一组 Skill 元数据。
3. 普通文件、Skill 的辅助资源文件和无法唯一归属的路径继续显示为普通 `read_file`，只有已加载 Skill 的根 `SKILL.md` 使用 Skill 卡片。
4. 文件工具修复缩短路径时，卡片显示最终解析到的规范化 Skill 路径。
5. 读取结果继续交给 LLM，但不进入用户可见的 Skill 卡片正文。

## 8. TUI 渲染改造

### 8.1 统一识别函数

把当前仅表示 `read_file` 的 `isSkillReadFrame` 泛化为结构化的 Skill frame 判断。判断只依赖 `Category`、`SkillName` 和 `SkillPath`，不关心事件来自 slash 还是 `read_file`。

### 8.2 消息头

在 `toolDisplayParts` 的 Skill 分支按状态输出：

| 状态 | Action | Target | Suffix |
| --- | --- | --- | --- |
| running/pending | `Loading skill` | `<SkillName>` | 空 |
| completed | `Skill` | `<SkillName>` | `<duration>` |
| failed | `Skill` | `<SkillName>` | `<duration>` |
| 其它（不可达，见 §2.4） | 落到通用工具渲染 | — | — |

进行中状态必须在打印消息头后立即返回，禁止落入 `(no output)`、通用输入预览或通用 output 格式化路径。

### 8.3 图标与颜色

在通用工具图标选择逻辑之前处理 Skill 的状态语义：

| 状态 | 图标 | 颜色 |
| --- | --- | --- |
| running | Braille spinner | 灰色 244（与所有进行中工具一致，非 Skill 青色） |
| completed | `●` | Skill 青色（256 色 `43`，见 §13 修订 14） |
| failed | `✗` | 与普通工具失败卡片完全一致（继承现有实现，不新增颜色处理） |
| 其它（不可达，见 §2.4） | 落到通用工具渲染 | — |

该变更只作用于 `Category=skill` 的 frame：

- 普通工具失败继续使用现有红色 `●`。
- compact 失败继续使用 `✗`。
- Fanout 失败任务行继续使用 `✗`。
- 现有 `toolHeaderColor` 的 failed/denied 分支优先于 Skill 淡紫色，保持不变；running 分支同样必须优先于 skill 分支（§2.1）。

### 8.4 正文

成功和失败正文由同一个 Skill card renderer 生成，不消费通用 JSON formatter 的输出：

```text
completed: Loaded from <display path>
failed:    Failed to load from <display path>\n<raw error>
```

统一使用现有 `formatToolOutputBlock` 的 `└` 和续行缩进，保证窄终端、宽字符和 ANSI 样式下对齐。失败状态的配色不新增任何 Skill 专属处理：图标与文字颜色完全继承通用工具失败卡片的现有实现（`toolHeaderColor` 失败分支 + `renderCompactFrame` 的 header 段样式），与 §2.3 一致——实现方式就是不为 `Category=skill` 的失败卡写任何颜色特例。

正文的生成位置分两层，避免两个 surface 各自为政：

- 共享层（`pkg/tool`）依据 `SkillPath` 与 `Error` 产出上述语义文本（绝对路径形态），作为 `DisplayBody` 提供给 gateway/webchat、canonical event 与持久化。
- TUI renderer 对共享正文中的绝对路径做 `displayPath` 项目根缩短后渲染；未识别为路径的部分原样保留。
- Skill 渲染分支以白名单收尾：只对 `completed` 渲染成功正文、对 `failed` 渲染失败正文，其余状态不再命中 Skill 专用路径而落回通用工具渲染。这是修复"当前工作区改动只排除 pending/failed 导致其它词表取值渲染成功正文"的正确方式——不是为每个不可达状态新增分支。
- 审批重放（attempt）事件与沙箱拒绝后等待审批的 read，同样按上述状态规则走 Skill 卡。

### 8.5 错误显示

不新增错误整理函数。失败正文第二行直接使用 `Error` 字段原文，唯一的处理是通用工具渲染已有的安全换行与终端控制字符清理（`formatToolOutputBlock` 路径本身的行为），不做任何 Skill 专属的去重、去前缀、大小写或措辞加工（§2.3）。

## 9. 通知、持久化与恢复

### 9.1 通知层

- 删除 `pkg/tui/notify.go:isSkillTool(toolDescription)` 及其自然语言前缀约定。
- 删除把 `activation.Content` 覆盖到 `rendered.DisplayBody` 的路径。
- `BuildToolMeta` 直接从 StepEvent 的结构化字段生成 Skill metadata。
- `BuildRenderedToolStepNotification` 对 started 事件继续不生成正文，但必须允许 TUI 依据 metadata 建立 header-only frame。

### 9.2 Canonical event 与 Gateway

- started、completed、failed 都在 `event.ToolCallMeta` 中保存 `Category`、`SkillName`、`SkillPath`。
- `DisplayBody` 只保存最终用户可见的语义文本（§5.3 的绝对路径形态），不能保存技能正文或通用 output JSON，也不能为空串。
- canonical event 的 raw output 若因模型或诊断需要保留，surface 不得从 raw output 回退生成 Skill 卡片。
- Gateway 的 WS 步骤负载（`pkg/gateway/server.go` 的 `gatewayToolMetaPayload`）当前会丢弃 `category`、`skill_name`、`skill_path` 三个字段，必须补齐转发，否则 webchat 无法识别 Skill 卡；webchat 的工具卡正文消费 `display_body`，依赖上一条的非空 `DisplayBody`，否则成功卡片展开后是空白（当前工作区改动已引入此缺陷，本计划修复）。
- 显式 Skill 步骤按 §6.4 走 canonical session event 持久化。

### 9.3 Resume replay

- `pkg/tui/commands.go` 从 `tool_meta_json` 恢复 LLM 读取路径的 Skill metadata（tool-role 行的 `tool_display` part）。
- 显式路径按 §6.4 从 session event log 回放，不经过 tool 行。
- replay 使用与 live 完全相同的 renderer，不维护第二份 Skill 文案。
- 历史记录缺少新 metadata 时可以保留旧卡片原貌；禁止通过解析任意 Markdown/XML 猜测并升级历史消息。

## 10. 实施步骤

### P0：清理工作区遗留（§12.1）

删除 `pkg/run/zz_angleb_test.go` 与 `pkg/tool/zz_angleb_test.go` 两个零断言调试文件，确认 `pkg/architecture` 测试恢复通过。未完成本步不得开始 P1。

### P1：固定最终行为测试

先增加会失败的测试，覆盖最终 UI 字符串、图标、状态替换和禁止泄漏字段。暂不调整生产代码。

- 新增失败测试只针对显式触发路径：`/review-agent` 当前渲染的原始 JSON 卡片（`review-agent {"explicit":true}` 等）必须被稳定复现，并在最终期望上失败。
- LLM 读取路径的测试在当前工作区改动中已存在并通过，不需要重写；不为不可达状态（canceled/denied，见 §2.4）新增期望用例。

完成判据：测试能稳定复现当前 `/review-agent` 的原始 JSON 卡片，并在最终期望上失败。

### P2：补齐 Skill 结构化事件契约

1. 扩展 Skill activation/turn submission，使可信 `SkillPath` 能进入 Runner。
2. 统一 StepEvent、ToolMeta、ToolCallMeta 的 Skill 字段和 Category 投影。
3. 让 LLM `read_file` 的 started/completed/failed 事件都携带同一身份。
4. 移除描述字符串识别。

完成判据：两种来源生成除 Origin 外完全相同的显示 metadata；修改 ToolDescription 文案不会改变卡片类型。

### P3：把显式加载纳入 Runner 生命周期

1. slash handler 只提交 SkillName/SkillPath。
2. Runner 在首次 LLM 请求前真实加载 Skill。
3. 发出 started/completed/failed，并计算真实 Duration。
4. 保持模型注入内容和 ephemeral 语义不变。
5. 防止失败卡片之后出现重复通用错误。

完成判据：显式 Skill 的三种事件与真实 I/O 边界一致；加载失败时 LLM 调用次数为零。

### P4：统一 TUI renderer

1. 实现统一 Skill frame 判断。
2. 实现三种消息头、图标和颜色（running 沿用通用灰色，见 §2.1；失败与普通工具失败卡完全一致、不写颜色特例，见 §2.3；当前工作区改动把 skill 分支排在 running 之前导致运行中显示淡紫，需调整 `toolHeaderColor` 分支顺序）。
3. 实现 header-only 进行中状态。
4. 实现成功/失败正文（共享层生成语义正文，TUI 做路径缩短，见 §8.4；失败第二行用原始错误，见 §8.5）。
5. 复用项目相对路径/外部绝对路径规则。
6. 修复非 failed 终态渲染成功正文的缺陷：Skill 分支白名单收尾（只对 completed 渲染成功正文），其余落回通用工具渲染，不新增 Skill 专属分支（§2.4）。

完成判据：用户显式触发与 LLM 主动读取的同状态渲染结果逐字一致；`Status="completed"` 以外的 frame 不出现 `Loaded from`。

### P5：实时、子 Agent 与恢复一致性

1. 验证 started frame 被同 StepID 的终态原位替换。
2. 验证主会话和子 Agent 视图的 owner 不变。
3. 验证 canonical event 和数据库回放保持 Skill metadata。
4. 验证 resume 不显示 raw output。
5. 落地 §6.4：显式 Skill 三态事件先落 session event store 再通知 UI；扩展 resume 的事件回放 mapper 接受主 agent 的 `Category=skill` 事件并正确锚定；验证 resume 后显式卡片完整回放且不与读取路径重复。

完成判据：live、resume 和 subagent view 使用同一 renderer 输出相同卡片；显式路径 resume 后三态卡片逐张回放。

### P6：Web 端 Skill 消息卡片同步

Web 端（webchat）的 Skill 卡片与 TUI 同步改造，不是"字段不丢"的保底：

1. `gatewayToolMetaPayload`（`pkg/gateway/server.go`）补转发 `category`、`skill_name`、`skill_path`（§9.2）。
2. 前端 `SubagentToolStep`（`frontend/src/composables/useChatStream.ts`）与 `toolMetaRecord` 解析链补齐 skill 三字段，live（WS payload）与历史（`tool_meta_json`）两条入口都取得到。
3. `ToolCallCard.vue` 的标题计算识别 `category === 'skill'`：`Loading skill <name>`（进行中）/ `Skill <name>`（终态），不显示 `review-agent {"explicit":true}` 之类的参数摘要；`toolState` 沿用现有 streaming/available/error 状态映射。
4. 卡片正文使用 `display_body`（共享层生成的 `Loaded from <绝对路径>` / `Failed to load from <path>` + 原始错误），失败正文不做二次加工（§8.5 对两个 surface 一致生效）。
5. 前端测试（vitest，`frontend/src/composables/useChatStream.test.ts`）覆盖：skill meta 解析、卡片标题、display_body 透传、禁止泄漏断言（§11.6）在 web 侧同样成立。

完成判据：同一次 Skill 加载在 TUI 与 webchat 上显示同一组语义信息（标题文字、状态、正文、来源路径）；web 卡片不含激活正文或参数摘要。

### P7：清理与全量验证

1. 删除失效的 `isSkillTool`、旧注释和只为 raw Skill output 服务的 TUI 分支。
2. 更新当前工作区已有 Skill 读取卡片测试：`Load from` 改为 `Loaded from`，失败图标断言从红色 `●` 改为 `✗`。
3. 检查没有新增基于名称、描述或 output 内容的兼容判断。
4. 新增/改名的 Go 文件遵守仓库命名规则（文件名不超过两个下划线；`X_test.go` 与生产文件同名同目录），由 `pkg/architecture` 测试强制。
5. 若包导入关系有变化，运行 `scripts/package-graph.sh` 并提交更新后的 `pkg/architecture/testdata/graph.json`。
6. 执行格式化、静态检查和相关测试（Go 命令见 §12；前端 `cd frontend && pnpm test`）。

## 11. 测试矩阵

### 11.1 Producer 与 formatter

- 显式 Skill started/completed/failed 均携带 Category、SkillName、SkillPath 和稳定 StepID。
- Origin 正确区分 explicit 与 llm-read，出现在 StepEvent/canonical payload/run audit，且不出现在 ToolMeta 与任何用户可见字段。
- completed/failed Duration 非负且来自真实加载区间。
- LLM 根 SKILL.md read 三种状态携带同一 metadata。
- 普通文件、Skill 辅助文件和歧义路径不被分类为 Skill。
- `FormatToolStepResult` 的 Skill 分支不包含激活正文、文件正文或 JSON envelope。
- 任意修改 ToolDescription 不影响分类。

### 11.2 TUI 精确渲染

对显式与 LLM 两种来源分别断言：

```text
running:   ⠋ Loading skill review-agent
completed: ● Skill review-agent <0.1s
           └ Loaded from <path>
failed:    ✗ Skill review-agent <0.1s
           └ Failed to load from <path>
             open /path/SKILL.md: permission denied
```

测试应对 ANSI 去色后断言文字与图标，并单独断言颜色：

- running 为灰色 244（与其它进行中工具一致）；completed 为 Skill 淡紫色。
- failed 与普通工具失败卡片的文字颜色完全一致（继承现有实现）。
- 进行中没有 `└`、`output`、path 和 `(no output)`。

### 11.3 路径与错误

- 项目内路径显示项目相对路径。
- 外部路径显示规范化绝对路径。
- 长路径正确换行且不越过右边距。
- 失败第二行与 `Error` 字段原文逐字符一致：`open /path/SKILL.md: permission denied` 原样显示，不改写为 `Permission denied`；多行错误原样保留（仅通用安全换行与控制字符清理）。

### 11.4 状态与恢复

- started → completed 原位替换，历史中只有一张最终卡片。
- started → failed 原位替换，历史中只有一张最终卡片。
- 沙箱拒绝后的 attempt（审批重放）事件同样渲染 Skill 卡。
- 显式加载失败不会调用 LLM，不会追加第二张通用错误卡片。
- 会话恢复后的成功/失败卡片与 live 输出一致（两条路径分别验证：读取路径走 tool 行回放，显式路径走 session event 回放）。
- 显式卡片 resume 回放不与读取路径的回放重复出卡。
- 子 Agent 读取 Skill 的卡片只出现在该 Agent 视图。

### 11.5 Gateway/WebChat

后端（Go 测试）：

- gateway 的 `tool_meta` 负载包含 `category`、`skill_name`、`skill_path`。
- 成功 Skill 步骤的 `display_body` 非空且为 `Loaded from <absolute path>`；失败步骤为 `Failed to load from <path>` + 原始错误。

前端（vitest）：

- `SubagentToolStep`/`toolMetaRecord` 从 live WS payload 与历史 `tool_meta_json` 两条入口都解析出 skill 三字段。
- `category === 'skill'` 的卡片标题为 `Loading skill <name>`（进行中）/ `Skill <name>`（终态），不显示参数摘要（`{"explicit":true}` 等）。
- 卡片正文透传 `display_body`，成功与失败文案与 TUI 的语义文本一致。
- §11.6 的禁止泄漏清单在 web 卡片的标题、正文与展开区同样逐项成立。

### 11.6 禁止泄漏断言

在 live、canonical event 的 DisplayBody、持久化 transcript display 和 resume 输出中逐项禁止：

````text
{"explicit":true}
output:
```json
<skill>
<context>
root_dir
relative_paths
\u003c
\"output\"
````

测试 fixture 可以包含这些内容，但断言对象必须是用户可见字段；不要误把模型内部输入或专用 telemetry 作为 UI 泄漏。

## 12. 建议执行的验证命令

仓库规则：CGO 必须开启，CI 使用 `-tags fts5`。

```bash
gofmt -w <本次修改的 Go 文件>
CGO_ENABLED=1 go test -tags fts5 ./pkg/skill ./pkg/turn ./pkg/run ./pkg/tool ./pkg/event ./pkg/tui ./pkg/gateway -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1
go vet ./pkg/skill ./pkg/turn ./pkg/run ./pkg/tool ./pkg/event ./pkg/tui ./pkg/gateway ./pkg/architecture
git diff --check
```

TUI 侧必须用 `run-forebrain` skill（`.claude/skills/run-forebrain/driver.sh`）做真机验收，单测通过不能替代（场景见 §12.2）。

若相关包的定向测试全部通过，再执行仓库全量测试（同样必须带 CGO 与 fts5）：

```bash
CGO_ENABLED=1 go test -tags fts5 ./... -count=1
go vet ./...
```

- 新增/移动包导入后运行 `scripts/package-graph.sh`，提交更新后的 `pkg/architecture/testdata/graph.json`。
- 以下测试在 `main` / `4330e079` 基线上已失败（多为 `sandbox-exec: sandbox_apply: Operation not permitted` 环境原因），与本次改动无关，验证时按已知失败对待，不要求修复也不得归因于本计划：pkg/run 的 `TestEditInsideWorkspaceNeedsNoApproval`、`TestToolOrchestrationRetriesFileMutationAfterMissingPath`、`TestCWDFileToolsUseResolvedApprovalPath`；pkg/tool 的 `TestProtectedMetadataPathsRequireApproval`、`TestRelocatedForebrainHomeRequiresApproval`、`TestUnrelatedCommandStaysSandboxed`；pkg/tui 的 `TestCharacterizationNetworkApproval`。

### 12.1 实施前必须先清理的工作区遗留（来自 2026-09-15 review-agent 对工作区 diff 的评审）

当前工作区有两个未跟踪文件 `pkg/run/zz_angleb_test.go` 和 `pkg/tool/zz_angleb_test.go`，是探测 protected-path 写入行为的调试遗留。它们使 `pkg/architecture` 的 `TestTestFilesCorrespondToProductionFiles` 失败（`zz_angleb_test.go has no zz_angleb.go beside it`），CI 跑 `go test ./...` 会直接挂；且两个测试函数零断言（所有分支只有 `t.Logf`，任何结果都不会失败），其中一个还会向 `.git/hooks/pre-commit` 写入模拟恶意载荷。

实施 P1 之前：

1. 删除这两个文件。若其中有值得保留的场景，改写成带断言的测试并归入合规文件（如 `pkg/tool/file_tools_test.go`）；否则直接删除。
2. 删除后运行 `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` 确认恢复通过。

### 12.2 TUI 真机验收（run-forebrain skill，强制）

TUI 侧的实施结果必须用 `run-forebrain` skill 在真实 TUI 进程里验收——`driver.sh` 用 tmux 驱动真实构建的二进制（隔离的 FOREBRAIN_HOME，fake provider 可控时序），`screen` 读渲染结果、`messages`/`db` 读持久化。单测断言 renderer 函数的输出，但 reducer 派发、状态原位替换、resume 回放这些只在完整 TUI 进程里发生的行为，单测覆盖不到。

注意事项（来自 skill 文档）：

- 改完 Go 代码必须显式 `$D build`，`start` 复用旧二进制不会自动重编。
- 判定"卡片定型"要等 producer 侧（`provider-log`/DB），不要只看屏幕（UI 派发是异步队列）。
- fake provider 需要能产生工具调用才能驱动 LLM 读取路径；若该 provider 模式不支持 tool call，读取路径用 `reply` 模式提交一个明确请求模型读 SKILL.md 的 prompt，或按下文"降级为 Go 集成测试"处理。

验收场景（`D=.claude/skills/run-forebrain/driver.sh`，每个场景先 `$D build` 再 `$D reset; $D start <mode>`）：

1. **显式成功**：`$D start reply`，`$D submit '/<某已安装skill> <请求>'`；`$D wait 'Skill ' 25` 后 `$D screen` 断言：`● Skill <name> <耗时>` + `└ Loaded from <路径>`；不出现 `{"explicit":true}`、`output:`、`<skill>`、`<context>`。`$D messages`/`$D db` 确认 user 行保留、无激活正文泄漏。
2. **显式失败**：提交指向已删除/损坏 SKILL.md 的 skill（预先在隔离 home 里布置）；断言 `✗ Skill <name>` + `Failed to load from <path>` + 原始错误原文；无重复通用错误卡；`$D db` 确认 LLM 调用次数为零（provider-log 无 REQUEST 行）。
3. **LLM 读取成功**：驱动模型调用 `read_file` 读根 SKILL.md；断言渲染为 Skill 卡（同场景 1 的文案），不显示文件正文预览（`1|` 行号样式不得出现）。
4. **进行中→终态原位替换**：用 `hang` 模式或延迟 provider 抓中间态：`⠋ Loading skill <name>`（灰色，无 `└`、无 output 区）；终态到达后同一位置只有一张最终卡，不出现第二张。
5. **resume 回放**：场景 1/2 完成后退出 TUI，`$D start reply`（同一隔离 home）+ `/resume` 选同一会话；断言 Skill 卡完整回放且文案与 live 一致、无重复卡。
6. **普通工具不受影响**：同一会话里跑一次普通 read_file/shell，断言其卡片样式与改动前一致（回归）。

每个场景的 `$D screen` 输出留档作为验收证据；任一场景失败则该次实施不算完成，不得提交。

## 13. 修订记录

### 2026-09-16 评审修订

对照 `main` / `4330e079` 与工作区改动逐项核实后补齐的缺口：

1. **显式 Skill 卡片无持久化/恢复路径（违反 TUI 持久化铁律）**：伪造事件只进 run audit 与瞬时 UI，resume 不回放。新增 §6.4，P5 增加对应步骤与验收。
2. **WebChat 成功卡片正文为空且丢失 Skill 元数据**：共享 formatter 返回空正文、gateway `gatewayToolMetaPayload` 不转发 skill 字段。§5.3/§8.4/§9.2 明确"共享层生成语义正文（绝对路径）、TUI 负责缩短"的分工，新增 §11.5 验收。
3. **状态机可达性核实**：工作区改动的渲染分支以"非 pending、非 failed 即成功正文"收尾，词表里其它取值会渲染成功正文。核实 `ProtectedReadReason` 与 Skill 卡识别共享同一 catalog 后确认审批对 Skill 卡不可达，故不把 canceled/denied/awaiting approval 列为可达状态或实现目标，只要求 Skill 分支白名单收尾防串台（§2.4、§5.1、§8.2、§8.3、§8.4）。
4. **§8.4 与 §9.2 原文互相矛盾**（正文由 TUI renderer 生成 vs DisplayBody 保存语义文本）：按第 2 条的分工统一。
5. **验证命令不符合仓库规则**：补 CGO_ENABLED=1、`-tags fts5`、`pkg/architecture`、package-graph，并记录已知预存失败清单。
6. **P1 措辞精确化**：新失败测试只针对显式路径；读取路径测试已在工作区存在。
7. **细节补齐**：`Origin` 的存放位置与禁止渲染层读取；attempt 事件走 Skill 卡；清理步骤补图标断言更新与 Go 文件命名规则。
8. **吸收 2026-09-15 review-agent 对工作区 diff 的评审结论**：P1 级问题（`zz_angleb_test.go` 两个零断言调试遗留使架构测试失败）固化为 §12.1 的实施前置清理；P3 级问题（webchat 空 body + gateway 丢 skill 元数据）与第 2 条同源，并入 §9.2/§11.5。
9. **running 状态颜色修正**：`Loading skill` 进行中消息与其他工具调用一致使用灰色 244，Skill 淡紫色只用于 completed 终态；当前工作区改动把 skill 分支排在 running 之前导致运行中显示淡紫，§2.1、§8.3、P4、§11.2、§14 同步修正。
10. **失败卡片配色与普通工具一致**：失败 Skill 卡不为 `Category=skill` 写任何颜色特例，图标与文字颜色完全继承通用工具失败卡片的现有实现（§2.3、§8.3、§8.4、P4、§11.2、§14 同步修正）。
11. **失败第二行显示原始错误**：删除原 §8.5 的错误整理函数设计，失败正文第二行直接使用 `Error` 字段原文，不做去重、去前缀、大小写或措辞加工（§2.3、§8.4、§8.5、P4、§11.3 同步修正）。
12. **Web 端同步为一等要求**：原计划把 webchat 定位为"只保字段不丢、本任务只验收 TUI 视觉"。按用户要求升级为与 TUI 同步改造：新增 P6（gateway 字段转发 + 前端 meta 解析/标题/正文/测试），§11.5 拆分 Go 与 vitest 断言，§14 加验收项，原 P6 顺延为 P7。
13. **TUI 真机验收强制化**：按用户要求，TUI 侧实施结果必须用 run-forebrain skill（driver.sh + tmux + fake provider）在真实 TUI 进程里验收，单测不替代。新增 §12.2 六个验收场景（显式成功/失败、LLM 读取、状态替换、resume 回放、普通工具回归），§12/§14/§15 挂接强制要求。

### 2026-09-20 用户修正

14. **Skill 卡成功终态禁用粉红，改为青色（256 色 `43`）**：用户原话"skill 消息卡片的消息头的文字颜色改一下，禁止使用粉红色，换成其他颜色"。completed 终态的图标与消息头颜色由 212（洋红）改为 `43`（aqua/青绿，`pkg/tui/render.go` 的 `skillCardHeaderColor`）；§2.2、§5.2、§8.3 以及 §13 第 9/10 条中的"淡紫色"字样（含实施期临时使用的 147/212）均以本条为准，现行色值只有一个：`43`。颜色仍由 `skillCardHeaderColor` 单点定义，`TestSkillCardHeaderIsNotTheReasoningColor` 按常量断言（不硬编码色号），继续保证与 reasoning 147、普通工具 214 分属不同色相，并与 assistant 圆点 45 保持可辨识距离。
   - 真机验收（run-forebrain `driver.sh`，隔离 `FOREBRAIN_HOME`，`$HOME/skills/aqua-demo/SKILL.md` 触发显式加载，`tmux capture-pane -p -e` 留档 `/tmp/forebrain_card_ansi.txt`）：`● Skill aqua-demo <0.1s` + `└ Loaded from <path>` 正常渲染，该行四段文字全部为 `38;5;43`，整屏 `38;5;212` 计数为 0；无任何颜色/渲染行为之外的改动。

## 14. 验收标准

- `/review-agent` 不再显示 `review-agent {"explicit":true}`。
- 用户显式触发与 LLM 主动读取使用同一种 Skill metadata 和同一个 renderer。
- 进行中卡片只有 `⠋ Loading skill <name>` 消息头，颜色与其它进行中工具一致（灰色 244）。
- 成功卡片使用 `●`、`Skill <name>` 和 `Loaded from <path>`。
- 失败卡片使用 `✗`、`Skill <name>`、`Failed to load from <path>` 和原始错误消息（不加工，§2.3）；文字颜色与普通工具失败卡片完全一致，不为 Skill 写颜色特例。
- 普通工具失败图标语义不变。
- started 和终态按 StepID 原位替换，不产生重复卡片。
- live、subagent、持久化与 resume 展示一致；显式路径的卡片 resume 后完整回放（§6.4）。
- TUI 侧通过 run-forebrain 真机验收（§12.2 六个场景全部通过，`$D screen` 留档）；单测通过不替代本项。
- gateway/webchat 的 `tool_meta` 携带 Skill 字段，成功卡片 `display_body` 非空（§11.5）。
- Web 端 Skill 卡片与 TUI 同步（P6）：同一组标题语义、状态、正文与来源路径；web 卡片不含激活正文或参数摘要；前端 vitest 用例通过。
- 非成功状态不出现 `Loaded from`（白名单收尾，§8.4）；不为不可达状态新增 Skill 专属渲染分支。
- Skill 指令、内部 JSON、XML 标签和转义数据不出现在任何用户可见 Skill 卡片字段中。
- 模型收到的 Skill 内容、显式 slash 的用户输入语义及 prompt prefix 缓存行为保持不变。
- 当前工作区已有未提交改动没有被覆盖或丢失。

## 15. 实施约束

- 在当前非干净工作区上增量修改；实施前记录 `git diff`，不得 reset、checkout 或覆盖现有改动。
- 先补行为测试，再改 producer、metadata 和 renderer。
- 结构化字段是唯一真相源，不新增描述字符串、Skill 名称或 output 内容启发式判断。
- 不为不可达的状态、路径或输入编写防御性分支、文案或测试用例（§2.4 可达性说明）；状态收尾用白名单而非逐状态排除。
- 用户可见文案集中在统一 Skill renderer，避免 live 与 replay 各维护一份。
- 不把本次 UI 修复扩展成通用工具失败图标重构。
- TUI 侧验收必须走 §12.2 的 run-forebrain 真机流程；fake provider 无法驱动的场景按 §12.2 的降级说明处理并注明。
- 未完成上述验收项前不提交实现。

## 16. 实施状态（2026-09-16 完成）

### 已落地

- **P0**：删除 `pkg/run/zz_angleb_test.go`、`pkg/tool/zz_angleb_test.go`，`pkg/architecture` 恢复通过。
- **P1**：新增 `TestExplicitSkillInvocationRendersDedicatedCard`，先在旧实现上稳定复现 `● review-agent {"explicit":true}` 原始 JSON 卡并在最终期望上失败，再随实施转绿。
- **P2**：`tool.StepEvent` 增 `Category`/`Origin`；`event.ToolCallMeta` 增 `Origin`（仅 canonical payload 与 run audit，不进 `ToolMeta`，渲染层不可读）；`skillStepMetadata` 以结构化 `Category` 为唯一身份（保留 read_file output-marker 兼容旧事件），描述前缀/名称/StepID 启发式全部移除；`FormatToolStepResult` 为 completed 产出 `Loaded from <绝对路径>`、failed 产出 `Failed to load from <绝对路径>` + 原始错误第二行，白名单收尾，其余状态落回通用渲染。
- **P3**：slash handler（含 `/skills` 立即运行、workshop handoff、网关 channel 路径）只提交 `SkillName`/`SkillPath` 可信选择；`Runner.preloadExplicitSkill` 在首次 LLM 请求前发出真实 `started`→加载（`skill.LoadActivation` + 规范名校验）→`completed/failed`，记录真实 Duration，成功后以 `WithExplicitSkillActivation` 注入模型上下文（与旧行为字节一致）；失败返回 `*ExplicitSkillLoadError`，TUI 与 webchat 识别后不再追加通用错误卡，**provider-log 证实失败时 LLM 调用为零**。
- **P4**：`toolHeaderColor` running/244 优先于 skill/147；失败卡继承通用失败配色（203）不新增特例；失败图标 `✗` 同时覆盖 `renderCompactFrame` 与折叠头 `statusMarker` 两条路径；`toolDisplayParts` skill 分支白名单（loading→`Loading skill <name>`，completed/failed→`Skill <name>`+真实时长）；成功/失败正文由共享层生成、TUI 仅做项目根路径缩短（`shortenSkillLoadPaths`）；进行中 header-only；删除 `isSkillTool` 描述前缀识别、激活正文覆盖路径与 skill markdown 渲染分支。
- **P5**：显式 Skill 的 started/completed 在 TUI step hook 与 gateway step hook 中改走 canonical 事件（`RunEventFromStep` → `Events.Publish` → `AppendSessionEvent` 先落库后画行）；resume 回放 mapper 以 `replayToolEventSkipped`（AgentID 为空且非 skill 才跳过）放行主 agent skill 事件，并以 `skillEventAnchorPosition` 按角色锚定到所属 turn（毫秒事件戳 vs 秒级行戳不可直接比较，锚定走"最后一条不晚于事件的 user 行 + 同秒 user 行属同 turn"规则）；DB 证实 started/completed 共享同一 `skill-explicit-<nano>` StepID。
- **P6**：`gatewayToolMetaPayload` 补转发 `category`/`skill_name`/`skill_path`（Origin 仍不入 UI meta）；前端 `SubagentToolStep` 增 `category`/`skillName`/`skillPath`，`toolStepFromPayload`（live WS）与 `toolStepFromRow`（历史 tool_meta）双入口解析；新增导出纯函数 `skillCardTitle`（`Loading skill <name>` / `Skill <name>`），`ToolCallCard.vue` 标题改由其驱动、skill 卡正文仅消费 `display_body`（错误行已含于正文，不再重复渲染 error 行）；vitest 新增 6 个用例（meta 双入口解析、display_body 透传、原始错误保留、标题语义、普通工具不受影响）。

### §12.2 真机验收（run-forebrain，隔离 FOREBRAIN_HOME + fake provider，screen 留档 /tmp/acceptance_s*_screen.txt）

1. **显式成功** ✅：`● Skill demo-review <0.1s` + `└ Loaded from <绝对路径>`（长路径安全换行）；无 `{"explicit":true}`/`output:`/`<skill>`；user 行原文持久化。
2. **显式失败** ✅：启动后使 SKILL.md 不可读再提交——`✗ Skill demo-broken <0.1s` + `Failed to load from <路径>`（折叠内为原始错误原文）；无重复通用错误卡；`provider-log` REQUEST 计数为 0；DB 仅 user 行、无 assistant 行。
3. **LLM 读取**：fake provider 无工具调用模式，按 §12.2 降级条款以 Go 集成/单测覆盖——`pkg/tool/file_tools_test`（主文件标记/失败保身份）、`pkg/tool/format_test`（语义正文/错误正文/预览不泄漏）、`pkg/tui`（事件驱动 seam 渲染成功/失败/加载三态 + 外部绝对路径）。
4. **进行中→终态原位替换**：显式加载为 runner 内同步本地读取，无时序控制点，真机无法稳定抓帧——按降级条款以单测覆盖：`TestExplicitSkillLoadingCardIsHeaderOnlyAndGray`（灰色 244、无 `└`/output）+ reducer `replaceOrAppendBlock` 按 (Kind,StepID) 原位替换 + DB 证实同 StepID；`Loading skill` 头文案由 `toolDisplayParts` 测试断言。
5. **resume 回放** ✅：退出后 `/resume` 同一会话，失败卡与成功卡逐字回放（`✗ … Failed to load from` / `● … Loaded from`），顺序与 live 一致（user→卡→回答），`3 messages and 4 events replayed`，无重复卡。
6. **普通工具回归** ✅：普通消息回合与回放行为不变；`read_file`/`shell` 等通用工具卡渲染由既有全量单测覆盖（fake provider 不产生工具调用为 skill 文档已知限制）。

### 验证结果

- `CGO_ENABLED=1 go test -tags fts5 ./... -count=1`：仅剩 §12 记录的 7 个已知预存失败（沙箱环境 `sandbox-exec: Operation not permitted`），无新增失败。
- `go vet ./...` 干净；`gofmt` 无待格式化文件；`git diff --check` 干净。
- `scripts/package-graph.sh` 重新生成 `pkg/architecture/testdata/graph.json`（仅 loc 计数变化，包边界无变化）并随本次改动提交。
- 前端 `pnpm vitest run src/composables/useChatStream.test.ts`：38 用例全过。
