# 技能固化（`/skill-generator`）实现方案与开发计划

> 日期：2026-09-20
> 状态：待实施
> 基线：`main` / `d4779630`（工作区干净）
> 范围：`pkg/run`、`pkg/turn`、`pkg/skill`、`pkg/tool`、`pkg/config`、`pkg/tui`、`pkg/gateway`、`frontend`
>
> 本期同时修复读代码时发现的既有缺陷：项目技能目录来自进程工作目录（见 §4.7 / P3）

## 1. 要解决的问题

一轮任务里真正值钱的往往不是结果，而是"怎么跑起来的那套步骤"：用哪几个命令、哪个目录、卡在哪里、怎么绕过去、怎么验证成功。这套步骤现在只存在于当轮会话里，会话一结束就没了，下次要重新摸一遍。

forebrain 现有的三条路都不覆盖这件事：

- **记忆巩固**（`pkg/memory` → `pkg/skill/memory_source.go`）是离线、跨会话的提炼，产物落在记忆目录里等 `/memories` 提升，不在"刚做完这件事"的现场。
- **`/skills` → `skill-workshop`** 是完整的技能生命周期入口，`Creating a skill` 轨道要走一遍访谈流程，是"我想写一个技能"时的路，不是"刚跑通的这套步骤别丢了"时的路。
- **模型自己**不会主动提这件事，因为没有任何地方告诉过它这是它该做的判断。

本方案补上这条链路，包含两个部分：

1. **主动提议**：模型在一轮"确实不好摸出来"的工作收尾时，用一句话提议把这套步骤固化成项目技能。
2. **`/skill-generator`**：把当前会话里的这套步骤写成 `<project>/.forebrain/skills/<name>/`，下个会话起可被目录发现、可被 `/<name>` 调用。

## 2. 完整的用户语义

### 2.1 场景 A：一轮"难"的工作结束后主动提议

一轮任务里模型调了 12 次工具、用了 5 种工具、中间有两次失败后改对了。收尾消息的最后一句：

```text
这套跑起来的步骤（tmux 驱动 + 假 provider 卡住响应窗口 + 隔离 home + 环境变量密钥）不是一眼能想到的，
建议用 /skill-generator 固化成项目 skill，下次要在真实 TUI 里验交互就不用再摸一遍。

──────────────────────────────────────────────────────────
Worked for 9m 00s · 5 tools · 48.2k in / 3.1k out
```

约束：

- **一句话**，跟在正文后面，不另起卡片、不加装饰、不重复解释已经说过的工作内容。
- **一个会话只提一次**。用户不理它，这个会话里不再提第二次。
- **不值得就不提**。判断"不值得"时必须完全沉默，不能说"这次不值得固化"。

### 2.2 场景 B：用户接受提议

```text
› 用 /skill-generator 把这套步骤固化成 skill
```

或者直接 `/skill-generator`。发生的事：

1. 加载 `skill-generator` 系统技能（TUI/Web 都显示既有的 Skill 加载卡片）。
2. 模型从**当前会话已有的上下文**里抽出可复现步骤 —— 不重新探索、不重新跑命令。
3. 写 `<project>/.forebrain/skills/<name>/SKILL.md`（必要时带 `scripts/`）。该路径命中 `IsProtectedMetadataPath`，会照既有规则弹审批浮层，由用户批准。
4. 写完自检 frontmatter，然后一句话收尾：

```text
已写入 .forebrain/skills/tui-live-verify/SKILL.md，新会话生效。
```

### 2.3 场景 C：用户主动运行

`/skill-generator` 随时可用，不需要先有提议。可带一个可选参数作为命名或范围提示：

```text
/skill-generator tui-live-verify
/skill-generator 只固化假 provider 那一段
```

### 2.4 明确不该发生的事

| 反例 | 为什么禁止 |
| --- | --- |
| 每轮都提议 | 提示疲劳，会让真正值得的那次也被忽略 |
| 读了两个文件、改了一行也提议 | 没有可复现价值 |
| 提议写成多行说明 | 违反一句话文案要求 |
| 模型说"我不建议固化这次工作" | 不值得就沉默 |
| 生成的技能立刻在本会话生效 | 会重建工具表与技能目录，作废整段缓存前缀 |
| 写出没有 `description` 的 SKILL.md | 该文件在所有入口都不可见，等于白写 |
| 把会话里的密钥、绝对临时路径原样写进技能 | 技能会进 git |
| 在 plan 模式里提议固化 | 计划阶段的工作还没跑完 |

## 3. 机制与判断的分工

一条边界贯穿整个设计：

- **Go 侧只决定"允不允许提"**（闸门）：可数、可测、可回归。
- **模型只决定"值不值得提"**（判断）：语义问题，计数器答不了。

闸门关着的时候模型收不到任何提示，也就不可能提；闸门开了模型仍可以判断不值得而沉默。两边都不越界：Go 不去猜工作有没有复用价值，模型不去数自己调了几次工具。

## 4. 架构落点

### 4.1 常驻判据：冻结在会话前缀里

判据文本是"什么样的工作值得固化"的标准，每个会话都一样，属于前缀内容。

- 新增 `pkg/skill/offer.go`：`RenderSkillOfferGuidance(SkillOfferGuidanceOptions) string`，纯函数，输入只有开关和命令名，输出固定字节。
- `pkg/run/skills.go` 的 `skillCatalogLLM.render()` 在 `skill.Catalog(...)` 之后拼接这一段，仍然走 `catalogForSession` 的**每会话冻结**（`sessionPromptStateSkillCatalog`），用 `memory.InjectDeveloperInstruction` 作为同一个 developer 块注入。
- 选择拼进同一个冻结块而不是新开一条 developer 消息：块数不变，前缀条目数不变，冻结逻辑不用复制第二份。
- `RenderCatalog` 在无技能时返回 `""`；判据段不受此影响，单独渲染后拼接，所以零技能的会话也有判据。

判据内容（要点，最终文案在实现时定稿）：

- 值得固化的信号：步骤不止一步且顺序重要；里面有一眼看不出来的选择（某个 flag、某个隔离手段、某个等待条件）；换个人从头做会踩同样的坑；这件事在这个仓库里还会再做。
- 不值得的信号：一次性排查；只是读代码回答问题；步骤全是仓库文档里已有的标准命令；已有技能覆盖了同一件事。
- 提议方式：收尾时一句话，给出命令 `/skill-generator` 和固化后能省掉什么；不值得就完全不提。
- 一个会话只提一次。

### 4.2 触发闸门：一次性锚定提醒

判据常驻，但"这一轮够不够格"是运行时的事。做法完全沿用已经验证过的计划提醒机制：

- 新增 `pkg/run/skill_offer_llm.go`，`skillOfferLLM` 包在 `wrapPlanModeLLM` 外面（`pkg/run/runner.go:856` 附近），与计划提醒共用同一个 adoption sink。
- 信号**只从 `msgs` 推导**，不新增运行时状态。这一点照搬 `analyzePlanReminders` 的思路，理由相同：compaction 之后不漂移，resume 之后仍然正确，gateway 与 TUI 天然一致。

推导的信号：

| 信号 | 来源 |
| --- | --- |
| `toolResults` | 当前 run 段内 tool 结果消息数 |
| `distinctTools` | 这些结果对应的不同工具名数 |
| `recoveredFailure` | 出现过失败的工具结果，且其后同名工具成功 |
| `alreadyOffered` | `msgs` 中已存在带 marker 的 IsMeta 消息 |

开闸条件：

```text
(toolResults >= 8 && distinctTools >= 3) || (recoveredFailure && toolResults >= 5)
```

硬性抑制（任一命中即不注入）：

1. `alreadyOffered` —— 一个会话一次。
2. 当前处于 plan 模式（`state.ModePlan`）。
3. 当前是 subagent 或 fork child（`tool.IsForkChildFromContext` / `tool.SubagentTypeFromContext`）—— 提议属于主线程，子任务的输出归它自己的视图。
4. 本 run 由 `/skill-generator` 自己发起。
5. 当前目录解析不出版本控制项目根（没有可写的项目技能目录，提议就是空头支票）。
6. `features.skill_offer` 关闭。
7. `skill-generator` 技能本身不可用或已被 `/skills` 关掉 —— 命令随之消失，提议就没有落点。

注入形态：

- 复用 `planReminderMessage` 的形状（`Role=user`、`IsMeta`、`<system-reminder>` 包裹），marker 常量 `skillOfferMarker = "Skill worth keeping"`。
- 复用 `stableReminderForRun` 的锚定：一个 run 内只算一次位置，之后每次请求都在同一位置，前序历史逐字节不变。
- 复用 adoption sink 让编排循环把它收进实时会话并持久化。**不能**用 `Ephemeral`：临时消息在下一轮消失会让公共前缀在插入点断掉，等于把后面整段历史重新计费。
- 提醒文案必须是回合作用域并自我终结，例如"……这条提示只出现一次，后面的回合不用再考虑"，避免它作为历史留在后续回合里持续唠叨。

需要的一处小重构：`planReminderAdoptionSink` 目前只记一条，第二条会覆盖第一条。改成 `reminderAdoptionSink`，按 `insertAt` 升序持有多条并逐条 adopt，plan 与提议共用。两者当前互斥（plan 模式下提议被抑制），但让 sink 只能容纳一条是个随时会咬人的隐式约束，改掉它比留着注释便宜。

### 4.3 `/skill-generator`：命令由技能派生，不新增内置命令

`skill-generator` 就是一个系统技能，`slash-command` 保持默认的 `true`，`pkg/turn/skills.go` 的 `RefreshSkills` 会自动为它派生 `/skill-generator`。**不在 `pkg/turn/slash.go` 的内置 `commands` 表里另加一条。**

理由是内置命令一条也多余：`skillCommand` 的 handler 返回的就是

```go
Result{Handled: true, ShouldContinueRun: true,
       ContinueInput: SkillCommandInput(name, request),
       SkillName: name, SkillPath: path}
```

技能文件照样由 runner 的 preload 阶段真实加载（`preloadExplicitSkill`），加载卡片、耗时、失败卡片全是现成的；TUI（`pkg/tui/chat_session.go:1286`）和 gateway（`pkg/gateway/server.go:2198`）也已经在转发这两个字段。一条手写内置命令能多出来的只有两样，而这两样都不该在 Go 里：

| 原本想放在 Go 里的 | 实际该放哪 | 为什么 |
| --- | --- | --- |
| 框架提示（目标目录、素材是本会话上下文、参数怎么解释） | SKILL.md 正文 | 它是指令。写在 Go 里意味着改一句话要重新编译，评审时也看不出它和技能正文有没有互相矛盾 |
| 前置检查（不在项目里就别跑） | SKILL.md 第一步 | 代价只是少见情况下多一个模型回合，换来机制只有一套 |

由此连带成立的几点：

- **参数约定沿用既有的**：`SkillCommandInput` 在无参时给出"照技能说明执行"的标准措辞，有参时原样传入。SKILL.md 里写明"参数是技能名或范围提示"，与其他技能命令一个规矩。
- **不需要 `IsBuiltinName` 保护**：这个能力本身就是技能，同名技能覆盖它正是技能根优先级（project > workspace > user > system）该有的行为，不是需要防的冲突。
- **命名的那点别扭没有了**：`/skill-generator` 不再是"唯一带连字符的内置命令"，它和其他 `/<skill-name>` 一样由技能名派生，本来就该带连字符。
- **描述要写成只在明确意图下触发**：技能在目录里，模型可以自行加载。描述要写清"用户要求把刚做完的流程留下来时用"，避免把普通的"保存一下"也吸进来。
- **技能被关掉时命令一起消失**，这不是缺陷而是正确行为，但它反过来约束了提议逻辑，见 §4.2 的抑制条件。

- **为什么在主线程跑而不是派子代理**：生成器的输入就是这段会话本身。主线程里它已经在缓存前缀里，这一轮几乎只付新增 token；派给子代理就得把整段转录重新喂一遍，等于自建一个全新的冷前缀。这条直接来自缓存优先的约束。

### 4.4 `skill-generator` 系统技能

新增 `pkg/skill/system_assets/skill-generator/SKILL.md`，`slash-command` 保持默认 `true` —— 这条技能派生的命令就是唯一入口（§4.3）。

**第 0 步**是确认项目根：不在版本控制项目里就一句话说明并停下，不要开始抽取。正文还要写明目标目录是 `<project>/.forebrain/skills/`、素材是本会话已有的上下文（不要重新探索、不要重跑命令）、参数是技能名或范围提示。这些原本打算在 Go 里拼的话都放在这里。

流程（窄，五步）：

1. **抽取**：从会话里抽可复现的东西 —— 命令与参数、路径、前置条件、等待/重试条件、失败现象与规避、验证成功的判据。一次性事实（这次的某个 PR 号、某个临时目录）不进技能。
2. **脱敏**：密钥、token、个人绝对路径一律换成占位符，并写明从哪里取。技能会进 git。
3. **命名**：小写字母数字连字符，最长 64；不得与内置命令重名；目录名与 frontmatter `name` 必须一致。
4. **写入**：`<project>/.forebrain/skills/<name>/SKILL.md`，必填 `name` 与 `description`，`description` 要写清触发场景（它同时是目录里那一行，也是 `/<name>` 的描述）。正文不超过 500 行，超了就拆 `references/`。确定性的重复步骤写成 `scripts/` 而不是让下一次重新敲。
5. **自检与收尾**：读回文件核对 frontmatter，然后一句话：写到哪、新会话生效。

**写作规范只保留一份**：`skill-workshop/SKILL.md` 里的 `Skill Writing Guide`（anatomy、progressive disclosure、description 写法、500 行上限）抽到 `skill-workshop/references/authoring.md`，`skill-workshop` 与 `skill-generator` 都指向它。`skill-workshop` 的 `Creating a skill` 轨道加一句：要固化的是刚刚这段会话时，走 `/skill-generator`。

### 4.5 写入期强制校验

没有 `description` 的 SKILL.md 在 forebrain 的所有入口都不可见，等于没写。靠提示词要求模型自觉不够 —— 得让这种文件根本落不了盘。

- 新增 `pkg/run/skill_write_guard.go`：一个 `llm.ToolMiddleware`，在 `pkg/run/runner.go:1042` 那一组里挂载（与 hook 中间件、权限中间件同一处）。
- 目标判定：写入路径是任一技能根下的 `*/SKILL.md`（用 `skill.CanonicalSkillPath` 归一，避免符号链接绕过）。
- 命中则用 `skill.ValidateSkillContent` 校验待写内容，外加目录名与 `name` 一致性检查。
- 不通过时**原样返回解析器的错误**给模型，不加自编解释，让它自己改对再写。
- 必须穷举所有能写文件的工具，不能只挡 `write_file` —— 同类的每个入口一起改。

这不是防御性补丁：它修的是"一个文件可以合法落盘却在所有入口不可见"这个不变量缺口，无论写它的是生成器、模型手写还是别的路径。

### 4.6 生效时机

生成的技能**本会话不生效**，这是既有规则，不是这个特性的妥协：技能目录在缓存前缀里，会话中途重建它会作废整段前缀。`Service.Refresh()` 仍然当场刷新磁盘元数据和动态命令表，所以 `/<name>` 在本会话内就能用（走 `SkillCommandInput` 把正文直接读进当轮）；目录条目和工具表下个会话生效。收尾文案照既有说法写。

### 4.7 既有缺陷修复：项目根必须来自会话，而不是进程工作目录

**现象**：在 gateway 里为项目 B 的会话创建或更新项目技能，文件会落到 gateway 进程启动目录下的 `.forebrain/skills/`，而不是项目 B 里；同一个 gateway 中，项目 B 会话看到的技能目录（进 prompt 前缀的那一份）列的也是进程启动目录的项目技能。TUI 里看不出来，因为 TUI 的进程工作目录恰好就是项目根。

**根因**：`skill.Service` 和技能根解析描述的是一对 (agent workspace, project) —— `Service.ProjectKey` 就是为此存在的 —— 但"项目根目录"从来不是这对参数的一部分，于是每个调用点各自用 `os.Getwd()` 重新推导一次。运行时其实早就有权威来源：`pkg/process/runner_pool.go:244` 为每个项目单独做 `safety.ResolveProjectContext(env.Root, project.Root)`，`Runner.LaunchProject` 与 `Deps.ProjectRoot` 都是按项目冻结的。缺的不是信息，是把信息传进去。

**同类的全部落点**（一起改，不只改最初发现的那一个）：

| 位置 | 推导方式 | 后果 |
| --- | --- | --- |
| `pkg/skill/service.go:409` `projectSkillPath` | `filepath.Join(".", ".forebrain", "skills")` | `Service.Create`/`Update` 写进错误的项目；gateway 侧（`pkg/gateway/api_extra.go:2294`、`:2329`）构造的 service 根本没有项目 |
| `pkg/skill/roots.go:108` `TrustedProjectSkillRoots` | `os.Getwd()` | 经 `AgentSkillRootsForWorkspace` 影响 `pkg/run/runner.go:847`（**进 prompt 前缀的技能目录**）、`pkg/turn/skills.go:16`（动态命令表）、`pkg/skill/state.go:149`（开关存储） |
| `pkg/skill/roots.go:128` `ProjectSkillRootsForDir("")` 的空参回退 | `os.Getwd()` | `pkg/skill/hub.go:26`（管理视图列表）、`pkg/skill/skilltrust.go:49`（来源分类） |

**顺带暴露的不一致**：`pkg/run/runner.go:1101` 已经在按 `LaunchProject` 取发现用的 roots，而同一个 runner 的 `runner.go:847` 仍走 cwd 变体。gateway 里这两者可以给出不同答案，于是技能目录与真正发现到的技能集合不一致 —— 这正是 `pkg/turn/skills.go` 的注释承诺不会发生的事（"a skill cannot be missing from the catalog yet still answer to its slash command"）。

**修法**（按不写兜底、并且删掉让人走错的 API 的规矩）：

1. `skill.Service` 增加 `ProjectRoot`，与 `Home`/`WorkspaceRoot`/`ProjectKey` 并列；`projectSkillPath` 改成方法，拼 `<ProjectRoot>/.forebrain/skills/<name>`；`ProjectRoot` 为空时一句话报错，**不回退到 cwd**。
2. `TrustedProjectSkillRoots` 改为要求显式项目根；删掉 `AgentSkillRootsForWorkspace` 的 cwd 推导，调用方一律传项目上下文，`AgentSkillRootsForLaunch` 成为唯一入口（名字随之简化）。
3. 删掉 `ProjectSkillRootsForDir` 的空参回退，`dir` 变必填；`Hub` 与 `skilltrust` 从各自的构造参数拿项目根。
4. `pkg/run/runner.go` 的 roots 只算一次，`847` 与 `1101` 共用同一个表达式。
5. TUI 传 `ChatSession.LaunchProject`；gateway 的技能生命周期改为项目作用域（沿用已有的 `/projects/:id/...` 路由形态，`/projects/:id/mcp` 是现成的先例），`skillLifecycleService()` 接项目参数并从 `state.Project.Root` 取根，前端相应改调用。
6. 本期新增的写入校验（P6）与生成器命令（P4）都从这同一个项目根取值，不新增第四种推导方式。

**与缓存的关系**：技能目录每会话冻结，所以这不是一条会话内的失效路径；修复的收益主要是正确性。但 cwd 变体让"这个会话看到哪些技能"取决于进程状态而不是会话状态，冻结下来的那份字节因此不由会话决定 —— 修好之后这份冻结内容才真正是会话自己的。命中率只可能不降。

## 5. 缓存影响分析

这是本项目的最高优先级约束，所以逐项列出，而不是一句"不影响"。

| 改动 | 对前缀的影响 | 结论 |
| --- | --- | --- |
| 判据段拼进冻结的 developer 块 | 每会话渲染一次并冻结，字节恒定；只对**新会话**改变字节，已冻结的会话原样复用 | 命中率不降。新增约 250–350 token 只在首轮计入 creation，之后每轮都是 read，长会话里命中率单调上升 |
| 闸门提醒 | 一次性锚定插入，插入点之前逐字节不变；后续请求把它当历史前缀继续命中 | 命中率不降。等同已验证的计划提醒路径 |
| 提醒被持久化（非 Ephemeral） | 下一轮仍在同一位置 | 必须如此；若做成临时消息，插入点之后整段历史每轮重算 |
| 新增内置命令 | 只进命令表，不进工具数组、不进 system 块 | 零影响 |
| `skill-generator` 作为系统技能 | 目录里多一行（名字+描述+路径） | 前缀多约 40 token，恒定字节；不改工具数组 |
| 生成的新技能 | 下个会话才进目录 | 会话中途零影响 |
| 写入校验中间件 | 只在工具执行期生效 | 不进前缀 |
| `/skill-generator` 在主线程运行 | 复用已缓存的会话前缀 | 相对派子代理是净收益 |

**必须测的，不是论证的：**

- 字节稳定性 golden：对冻结 developer 块（目录 + 判据）渲染出的完整字符串做 golden，任何非故意的字节变化在单测里就红。
- 前缀不变量测试：同一 run 内连续两次请求，断言前一次的完整消息序列是后一次的严格前缀（含提醒注入的那一次）。
- 真实命中率：用 `cmd/cachebaseline` 录改动前后各一组，比对 `pkg/architecture/testdata/cache_baseline.json`，`scripts/hitrate.sh` 出数。**只在 DeepSeek 与 OpenAI 上验证**，其余 provider 不在本次验证范围内。命中率按 `CacheRead / (CacheRead + CacheCreation + Input)` 计，要求改动后 ≥ 改动前。

## 6. 产物规范

```text
<project>/.forebrain/skills/<name>/
├── SKILL.md          必需
│   ├── name          必需，小写字母数字连字符，≤64，与目录名一致，不与内置命令重名
│   ├── description   必需，写清"什么时候用"，缺失即拒绝写入
│   └── 正文          ≤500 行，超出拆 references/
├── scripts/          可选，确定性重复步骤
└── references/       可选，正文放不下的细节
```

正文该有的：前置条件、按顺序的步骤、每步为什么这么做（尤其是不直观的那一步）、失败现象与规避、怎么验证成功。

正文不该有的：这次会话的一次性事实、密钥与 token、个人机器的绝对路径、把仓库文档已有内容再抄一遍。

## 7. 配置与开关

- `pkg/config` 的 `FeaturesSection` 新增 `SkillOffer *bool`，默认 `true`，进 `EffectiveFeatures()`。
- 关闭后：判据段不渲染、闸门不注入提醒；`/skill-generator` 仍然可用 —— 关的是主动提议，不是能力。
- `/status verbose` 增加一行显示当前状态，保证"可配置"的同时"可验证"。

## 8. 两个 surface 的一致

不新增任何 UI 组件。

- **TUI**：命令进 `/` 补全（`turn.ListSlashCommands` 自动）；技能加载走既有 Skill 卡片；写入走既有审批浮层（`ProtectedWriteReason` 已有对应文案）；提议是助手正文的一部分，逐字流式呈现，不做特殊处理。
- **Web**：命令列表与执行共用 `pkg/turn`；`SkillName`/`SkillPath` 已在 `pkg/gateway/server.go` 转发；技能卡片与审批走既有组件。
- 语义以 TUI 为准，两侧共用 `pkg/turn` 与 `pkg/run` 的同一份实现，gateway 不另写分支。

## 9. 任务分解

### P0 判据渲染与冻结块

- 新增 `pkg/skill/offer.go`：`RenderSkillOfferGuidance`，纯函数。
- `pkg/run/skills.go`：`render()` 拼接判据段；开关关闭时不拼。
- 验收：`RenderSkillOfferGuidance` golden 通过；同一会话两次调用 `catalogForSession` 返回同一字符串；零技能会话仍有判据段。

### P1 adoption sink 泛化

- `pkg/run/config.go`：`planReminderAdoptionSink` → `reminderAdoptionSink`，持有多条、按 `insertAt` 升序 adopt；`pkg/run/orchestration_llm.go:203` 相应改为逐条插入。
- 验收：现有计划提醒测试全绿；新增"同一次请求记录两条提醒、两条都被 adopt 且顺序正确"的测试。

### P2 闸门与一次性提醒

- 新增 `pkg/run/skill_offer_llm.go`：信号推导、开闸条件、六条抑制、marker、锚定注入。
- `pkg/run/runner.go`：接入中间件链。
- 验收：六条抑制各一个测试；开闸条件边界值测试；同一 run 连续两次请求的前缀不变量测试；compaction 之后不重复注入；resume 之后不重复注入。

### P3 项目根来源修复（既有缺陷，详见 §4.7）

排在 P4/P6 之前，因为这两个任务都要拿同一个项目根。

- `pkg/skill/service.go`：`Service` 增加 `ProjectRoot`；`projectSkillPath` 改成方法并拼绝对路径；空值一句话报错，不回退 cwd。
- `pkg/skill/roots.go`：`TrustedProjectSkillRoots` 要求显式项目根；删除 `AgentSkillRootsForWorkspace` 的 cwd 推导与 `ProjectSkillRootsForDir` 的空参回退，`AgentSkillRootsForLaunch` 成为唯一入口。
- `pkg/skill/hub.go`、`pkg/skill/skilltrust.go`、`pkg/skill/state.go`：从构造参数取项目根。
- `pkg/run/runner.go`：roots 只算一次，`847` 与 `1101` 共用。
- `pkg/turn/skills.go`：`RefreshSkills` 接项目根。
- `pkg/tui/chat_slash.go`：传 `ChatSession.LaunchProject`。
- `pkg/gateway/api_extra.go`：技能生命周期改项目作用域路由，`skillLifecycleService()` 接项目参数；`frontend` 跟着改调用。
- 验收：`pkg` 下 `os.Getwd()` 不再出现在任何技能根解析路径上（用 `grep` 断言 + 单测）；给定两个不同项目根的 service，`Create` 各写各的；同一 runner 的目录 roots 与发现 roots 逐项相等；`ProjectRoot` 为空时 `Create`/`Update` 报错而不是写到 cwd；gateway 集成测试：为项目 B 创建技能后文件出现在 B 的 `.forebrain/skills` 下、A 里没有。
- 删除口径：cwd 变体不保留、不加弃用注释 —— 留着就还会有人用。

### P4 `skill-generator` 系统技能与规范单一化

命令由技能派生，本任务不碰 `pkg/turn/slash.go` 的内置 `commands` 表。

- 新增 `pkg/skill/system_assets/skill-generator/SKILL.md`，`slash-command` 保持默认 `true`。
- SKILL.md 承担原本想写在 Go 里的两件事：第一步确认项目根（不在版本控制项目里就一句话说明并停下），正文写明目标目录 `<project>/.forebrain/skills/`、素材是本会话已有上下文不要重新探索、参数是技能名或范围提示。
- 描述写成只在明确意图下触发，避免把普通的"保存一下"吸进来。
- `skill-workshop`：`Skill Writing Guide` 抽到 `references/authoring.md`，两个技能都指过去；`Creating a skill` 轨道加一句指向 `/skill-generator`；顺带清掉资产正文里残留的外部产品称呼。
- 验收：`skill.Install` 指纹更新后能发现新技能；`ValidateSkillDir` 通过；`/skill-generator` 出现在两个 surface 的命令列表里且 `SkillPath` 指向真实文件；`/skills` 里关掉该技能后命令消失；授权规范在仓库里只有一份。

### P5 写入期强制校验

- 新增 `pkg/run/skill_write_guard.go`，在 `pkg/run/runner.go:1042` 那一组里挂载。
- 穷举所有能写文件的工具，逐个覆盖。
- 拒绝理由：缺 `description`、目录名与 frontmatter `name` 不一致、技能名与内置命令重名（`IsBuiltinName`，否则该技能会劫持那条内置命令）。
- 验收：三条拒绝理由各一个测试且错误原文回传；符号链接绕行被拒；写普通文件不受影响；每个写工具各有一个测试。

### P6 配置开关

- `pkg/config`：`Features.SkillOffer`，默认 `true`，进 `EffectiveFeatures()`。
- `/status verbose` 显示。
- 两个开关各管一件事，不重叠：`features.skill_offer` 关掉只是不再主动提，命令仍在；`/skills` 里关掉 `skill-generator` 则命令和提议一起消失。
- 验收：关闭后判据段与提醒都不出现，命令仍可用。

### P7 测试与缓存回归

见第 10 节。

### P8 真机验收

见第 11 节。

实施顺序：P1 → P0 → P2 → P3 → P4 → P5 → P6 → P7 → P8。P1 先做是因为 P2 依赖它；P3 必须在 P4、P5 之前。

## 10. 测试计划

**单元测试**

- `pkg/skill`：`RenderSkillOfferGuidance` golden；`skill-generator` 资产的 frontmatter 校验；项目根注入后的路径解析（含空值报错）。
- `pkg/run`：信号推导的每个分支；六条抑制；锚定位置稳定；sink 多条 adopt；写入校验的每个拒绝理由与每个写工具；目录 roots 与发现 roots 相等。
- `pkg/turn`：`RefreshSkills` 按项目根取集合，并为 `skill-generator` 派生出命令；技能被关掉后命令消失。
- `pkg/config`：开关默认值与生效。

**集成测试**

- 假 provider 驱动一轮跨过闸门的 run，断言：提醒恰好注入一次、位置锚定、被持久化、第二轮不重复注入。
- `/skill-generator` 的完整链路：命令 → 技能加载卡片 → 写入审批 → 落盘 → `Refresh` → 动态命令可用；新会话里目录出现该技能。
- gateway 双项目：A、B 两个项目各建一个技能，各自落在各自的 `.forebrain/skills` 下；B 会话的技能目录里没有 A 的技能。

**缓存回归**

- 冻结块字节 golden。
- 同一 run 内相邻请求的严格前缀断言。
- `cmd/cachebaseline` 录改动前后各一组，`scripts/hitrate.sh` 比对，DeepSeek 与 OpenAI 各一组，要求 ≥ 基线。

## 11. 真机验收

这个特性的主体是模型行为，单测和假 provider 证明不了它。必须用 `run-forebrain` 在真实 TUI 里对真实模型跑完下面四条，并留截图：

1. **该提的时候提**：在一个真实项目里跑一轮多步骤、中间出过错的任务，收尾消息末尾出现一句话提议，`Worked for` 行照常收口。
2. **不该提的时候不提**：同一会话之后再问一个简单问题，不再出现任何提议。新开会话跑一轮"读两个文件回答问题"，不出现提议。
3. **命令能跑通**：接受提议 → 出现 Skill 加载卡片 → 写入弹审批 → 批准后落盘 → 收尾一句话说明路径与新会话生效。
4. **下个会话生效**：重启 TUI，`/` 补全里出现新技能的命令，技能目录里有它那一行，`/<name>` 能把正文带进当轮。

Web 侧重复第 3、4 条，确认与 TUI 显示同样的事实与同样的卡片语义；另外在 Web 上用两个不同项目各建一次技能，确认落盘位置正确（P3 的验收面）。

## 12. 风险与边界

| 风险 | 处理 |
| --- | --- |
| 阈值定得太松，变成噪音 | 阈值集中在一处常量，真机验收后按实际观感调整；判据文本明确要求"不值得就沉默" |
| 阈值定得太紧，等于没有 | 同上；`recoveredFailure` 分支专门覆盖"步骤不多但不好摸"的情况 |
| 模型在长回合里忘掉提醒 | 提醒锚定后每次请求都在历史里，不会消失 |
| 提醒作为历史在后续回合唠叨 | 文案回合作用域并自我终结；`alreadyOffered` 抑制二次注入 |
| 生成的技能质量差 | 授权规范单一来源；写入期强制校验挡住结构性问题；用户批准是最后一道 |
| 生成的技能名与内置命令重名，会反过来劫持那条内置命令（`All()` 里动态命令优先于内置） | 生成器命名步骤显式查 `IsBuiltinName`；P5 的写入校验把它作为结构性拒绝理由之一，不依赖模型自觉 |
| P3 删掉 cwd 变体会波及 gateway 路由与前端 | 改动面已在 §4.7 逐点列出；`/projects/:id/mcp` 是现成的路由先例，前端改动限于技能生命周期这几个调用 |
| `skill-workshop` 资产正文里残留了外部产品的称呼 | 随 P4 一并清理，不扩大到其他资产 |

## 13. 实施状态

| 任务 | 状态 | 备注 |
| --- | --- | --- |
| P0 判据渲染与冻结块 | 完成 | `pkg/skill/offer.go` + `skillCatalogLLM.render()`；真机会话的冻结块已在 `fb_session_prompt_state` 中逐字核对 |
| P1 adoption sink 泛化 | 完成 | `reminderAdoptionSink` 持有多条，编排循环逐条 adopt |
| P2 闸门与一次性提醒 | 完成 | `skillOfferLLM` 包在 `planModeLLM` 外；真机验证过开闸、持久化、一次一条 |
| P3 项目根来源修复 | 完成 | 既有缺陷，本期一并修；另发现主运行时 `Deps` 从未冻结 `LaunchProject`，一并补上 |
| P4 `skill-generator` 系统技能 | 完成 | 命令由技能派生，未动内置命令表；写作规范抽到 `skill-workshop/references/authoring.md` |
| P5 写入期强制校验 | 完成 | `skillWriteGuard` 中间件，覆盖 `write_file`/`edit_file`/shell 的 `apply_patch` |
| P6 配置开关 | 完成 | `features.skill_offer` 默认 true；`/status verbose` 显示；参数解析收敛到 `turn.ParseStatusArgs` |
| P7 测试与缓存回归 | 完成 | 全量测试 + vet + 架构测试；真实命中率未验（见 §14） |
| P8 真机验收 | 完成 | 真实 DeepSeek（deepseek-flash）+ 真机 TUI，四条全部通过（见 §15） |

## 14. 实施记录

### 14.1 实际落点与计划的差异

| 计划 | 实际 | 原因 |
| --- | --- | --- |
| 新增 `pkg/skill/offer.go` | 同计划 | —— |
| 新增 `pkg/run/skill_offer_llm.go`、`pkg/run/skill_write_guard.go` | 合并进 `pkg/run/skills.go` | 架构测试硬性上限"每包最多 20 个生产文件"，`pkg/run` 加到 22 会红。`skills.go` 本来就是 "Skills in a run" 的归属，两个新关注点跟着它 |
| adoption sink "按 insertAt 升序 adopt" | 按**记录序** adopt | 各 wrapper 记录的 `insertAt` 是各自收到切片的坐标；外层先插会移动内层坐标，按记录序回放才是坐标精确的（升序在等 index 时会翻序）。测试断言两条都 adopt 且顺序正确 |
| P5 只挂一个 `llm.ToolMiddleware` | 同上，另在 `pkg/tool` 增加 `PreviewApplyPatchWrites` | `edit_file` 的终稿在 handler 内算出，middleware 无法忠实重算；`apply_patch` 的解析/合并是 `pkg/tool` 内部实现，只能由它给出窄预览，避免第二份解析器 |
| —— | `turn.RefreshSkills` 增加 `launch safety.ProjectContext` 参数；`turn.Context` 增加 `ProjectRoot` | 删掉 cwd 变体后，调用方必须交出项目上下文，否则编译不过 |
| —— | 统一 `/status` 参数解析到 `turn.ParseStatusArgs` | 两个 surface 各写一份 `arg == "verbose"` 判断，文案会漂 |
| —— | `pkg/process/open.go` 补 `Deps.LaunchProject` | 计划未列，但**不补就无法工作**：主运行时从来没冻结过 launch project（只有 runner pool 冻了），TUI 侧项目技能根会解析为空 |

### 14.2 顺带修掉的既有缺陷

- 主运行时（`pkg/process/open.go`）从不冻结 `LaunchProject`：`Runner.LaunchProject` 在 TUI 路径上恒为零值，所以 `AgentSkillRootsForLaunch` 分支永远走不到、项目技能目录永远为空。这是 §4.7 那个缺陷的更深一层。
- `skill.Service.Install` 的 `ScopeProject` 也写 `<cwd>/.forebrain/skills`（计划表格未列），一并改为 `ProjectRoot`。

### 14.3 验证

| 项 | 证据 |
| --- | --- |
| 单测 | `CGO_ENABLED=1 go test -tags fts5 ./...` 与 HEAD 干净 worktree 的失败集合逐项相同（仅 5 个既有沙箱失败） |
| vet | `go vet -tags fts5 ./...` 干净 |
| 架构 | `pkg/architecture` 全绿（包图已重新生成，仅 LOC 变化）；`scripts/package-graph.sh` |
| 冻结块字节 | `pkg/skill/offer_test.go` golden + `pkg/run/skills_test.go` 冻结/零技能/关闭三例 |
| 前缀不变量 | `TestSkillOfferSuppressedOnceAlreadyOffered` 断言第二轮是同轮的严格前缀 |
| 项目根 | `pkg/skill` 两项目各写各的、空 root 报错；`pkg/run` roots 与发现集合逐项相等；`pkg/gateway` 双项目路由落盘 |
| 前端 | `vue-tsc --noEmit` 干净；`vitest run` 95 通过 |

### 14.4 未验证项（风险登记）

1. **真实命中率（§5 要求）未验证**：`cmd/cachebaseline` + `scripts/hitrate.sh` 需要真实 DeepSeek/OpenAI 录制各一组，本次未做。机械证据（golden 字节 + 严格前缀不变量 + 冻结块落库）已齐，但"改动后 ≥ 改动前"的实测数字仍缺。
2. **阈值只在一个真实会话里观察过**：开闸（16 工具、含一次失败恢复）与不开闸（2 种工具）各一例，样本小；判据文案与阈值集中在 `pkg/run/skills.go` 的常量与 `pkg/skill/offer.go`，真机观感不对时改一处即可。
3. **一次性提醒在压缩后的存活**：`alreadyOffered` 从 `msgs` 推导，长会话里若中途压缩把那条 IsMeta 提醒摘要掉，同一会话可能出现第二次提议。与计划模式提醒同一性质（计划模式也容忍压缩后重注入），未额外加固。

### 14.5 真机验收记录

```bash
DEEPSEEK_API_KEY=... .claude/skills/run-forebrain/p8.sh start   # 真实 provider 的 TUI 驱动
```

1. **该提的时候提**：在 forebrain 仓库的浅克隆里跑"把 pkg/skill 的测试跑通并报告确切命令"（16 工具、gocache 只读的坑）。收尾末句：
   `Where a later session would have to rediscover the GOCACHE override to run any Go test here, /skill-generator could capture it in a few words.`
   紧随 `Worked for 2m 09s · 16 tools · 298k in / 6.5k out`。
2. **不该提的时候不提**：同一会话追问"pkg/skill/doc.go 说了什么"（1 工具）→ 无提议；另一次 2 工具会话也不提；而该会话只有一条提醒落库（`fb_messages` id 19，`source=transcript`，正文 `<system-reminder>\nSkill worth keeping - one-time note.`）。
3. **命令能跑通**：输入"用 /skill-generator 把这套步骤固化成 skill"→ `● Skill skill-generator <0.1s └ Loaded from …/.system/skill-generator/SKILL.md` → 写入 `.forebrain/skills/run-go-tests/scripts/go-test.sh` 与 `SKILL.md` 时弹真实审批浮层（"● Writing <path>" + diff + Yes/No）→ 批准后落盘到**本会话自己的项目**（P3 修复的现场证据）→ 收尾一句话给出路径与新会话生效。
4. **下个会话生效**：重启 TUI 新会话 → `/run-go` 补全出现 `/run-go-tests`（描述即技能自己的描述）→ `fb_session_prompt_state` 的新会话冻结块含 `- run-go-tests: … (…/proj2/.forebrain/skills/run-go-tests/SKILL.md)`，而**旧会话的冻结块不含**（冻结正确）→ `/run-go-tests` 把正文带进当轮（Skill 卡片 + 模型按其 scripts/ 步骤执行）。

生成的技能质量抽查：frontmatter `name` 与目录一致、`description` 写明触发场景、脱敏（正文无 `/Users/doudou/...`，改用 `$(go env GOCACHE)`/`$TMPDIR`）、确定性步骤落 `scripts/go-test.sh`、正文含根因/验证计数/gotchas（107 行）。

