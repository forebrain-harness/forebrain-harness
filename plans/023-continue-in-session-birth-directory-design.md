# Plan 023（调研 + 方案，带决策闸门）：从另一端续聊时，工具在会话出生目录执行——先摸清 gateway runner 池与 TUI 单 runner 的改造方案，产出设计文档，交 owner 拍板后再实现

> **执行者须知**：这是一份**调研计划**：只读代码、跑只读命令、写一份设计文档；**不改任何源码**。按步骤执行，每步把发现写进设计文档。
> 触发 "STOP conditions" 时停下报告。**不要更新 `plans/README.md`**（评审方维护索引）。**禁止提交**。
>
> **Drift check（先跑）**：`grep -n 'func (s \*SessionStore) SessionWorkingDir' pkg/state/session_store.go` 有 1 行（plan 022 已执行——本计划复用它）。没有 → STOP。

## 执行记录

- 2026-10-10：owner 要求"现在就出方案，先拍板再实施"。评审方直接完成调研（未派执行者，未改源码），设计文档已写：`docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md`。Drift check 里"022 已执行"一项未满足——设计只引用 022 计划中的 `SessionWorkingDir` 定义，不依赖其代码。等 owner 回答文档末尾 D1–D4 后，评审方写实现计划 025–028。
- 2026-10-11：owner 多轮裁决后设计文档改写为定稿（项目模型 R1–R10，见文档第 0 节）。实现计划改为 025–031，待文档末尾 Q1–Q4 定下后写。

## Status

- **Priority**: P1
- **Effort**: M（调研）
- **Risk**: 调研本身无风险；后续实现为 HIGH（改变工具执行目录、沙箱根、信任判断）
- **Depends on**: `plans/022-workbench-files-follow-session-directory.md`
- **Category**: direction / design
- **Planned at**: commit `005c1a8` + 主树工作区未提交改动，2026-10-10

## Why this matters

owner 规则：web 发起的会话要能在 TUI 续聊、TUI 发起的会话要能在 web 续聊，**续聊时工具在会话出生时的目录执行**（`fb_sessions.cwd`，项目会话用项目根），
与续聊端进程在哪启动无关；目录不存在就拒绝并提示。

现状（已核实）：不属于项目的会话一律跑在**进程的基础 runner** 上，根目录是进程启动目录——
`process.Environment.RunnerForSession`（`pkg/process/runner_pool.go:128-146`）注释："The TUI never triggers pool creation: its sessions have no project row, so every call returns the base Runner"；
gateway 的 `runnerFor`（`pkg/gateway/server.go:77-91`）对非项目会话同样返回基础 runner。于是：web 续聊一个在仓库 A 里出生的 TUI 会话，工具在 gateway 的启动目录执行；
TUI 在目录 B 里 `resume` 一个 web 会话，工具在 B 执行。

已有的积木：runner 池能为任意根建 runner——`buildEntryLocked(ctx, project state.Project)`（`runner_pool.go:658-720`）：
`safety.ResolveRegisteredContext(env.Root, project.Root)`、按根解析 MCP（`ResolveSessionMCP`）、LSP（`Trusted: safety.TrustedRoot(launch) != ""`）、
`run.Deps{ProjectKey, ProjectRoot, LaunchProject: launch, ...}`；`poolKey`（`:543-560`）按项目内容指纹区分 runner 代。信任读持久化记录（`safety.IsTrusted`，TUI 启动时的信任提示写的就是它）。
TUI 的 `ChatSession` 通篇用 `s.runner()`（一个 runner），MCP 状态、工具、模型、`@` 补全根都挂在它上面。

## Steps（调研，逐步写进设计文档）

设计文档路径：`docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md`（仓库约定：方案写在 `docs/plan/`）。每一节写"现状（带 `file:line`）→ 可选方案 → 推荐与理由 → 风险"。

1. **gateway 侧**：非项目会话若出生目录 ≠ gateway 启动目录，能否用"合成项目"`state.Project{Root: cwd, ProjectKey: <按 cwd 算>}` 走 `buildEntryLocked` 得到 runner？
   - `ResolveRegisteredContext` 对未注册目录的行为；`ProjectKey` 的算法（`grep -rn 'func ProjectKey\|projectKey(' pkg`）；池的 LRU/驱逐对这类条目是否合适。
   - 会话与 runner 的绑定（`p.sessions[sessionID]`）如何改成按出生目录。
   - 出生目录**不存在**时在哪里拒绝、错误怎么传到 web（复用 plan 021 的忙碌提示模式还是新错误码）。
2. **信任**：出生目录在续聊端未被信任时怎么办？列出今天 gateway 项目会话对"未信任根"的处理（沙箱、MCP、LSP、技能），给出推荐（例如与项目会话一致地降级运行 / 拒绝并提示去信任），
   **这是需要 owner 拍板的点**——设计文档里单列"待 owner 决定"小节，给 2–3 个选项与推荐。
3. **TUI 侧**：`resume` 一个出生目录 ≠ 当前进程目录的会话，要让工具在出生目录执行，有几条路：
   (a) TUI 为该会话改用池里按出生目录建的 runner（列出 `ChatSession` 里直接依赖 `s.runner()` 的地方：MCP 状态、技能、`@` 补全、权限、计划文件目录……，估算改动面）；
   (b) TUI 提示"该会话出生在 X，是否在 X 中继续"，确认后**以 X 为启动目录重新拉起自身**（exec 新进程 / 切换进程工作目录与环境），比较副作用；
   (c) 其他可行方案。给出推荐与理由。
4. **`@` 补全与工作台**：022 已让 web 工作台按会话目录；TUI 的 `@` 补全根（进程 cwd）在 (a)/(b) 下如何与出生目录一致。
5. **同目录的快速路径**：出生目录 = 进程启动目录（绝大多数情况）时必须走今天的基础 runner，不能因此多建 runner、多连 MCP。写明判定方式（路径规范化、符号链接——
   注意 README 里记录过的"home 在符号链接路径下时字面比较出错"问题，`pkg/tool/state.go:916` 一带）。
6. **测试与验收设计**：列出实现阶段要写的测试（gateway 合成项目 runner、目录不存在、未信任、TUI 方案对应的测试）与真机步骤（driver + gateway 同一 home 的双向续聊，工具 `pwd` 输出等于出生目录）。
7. **工作量与拆分**：给出实现计划的拆分建议（如 023a gateway、023b TUI），每份的 S/M/L 估算。

## Done criteria

- [ ] `docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md` 存在，覆盖 Step 1–7，每个结论带 `file:line` 证据
- [ ] "待 owner 决定"小节列出信任处理与 TUI 方案的选项与推荐
- [ ] `git status --short` 只新增该设计文档（不改任何源码）

## STOP conditions

1. Drift check：plan 022 未执行。
2. 发现某个安全约束明确禁止在未注册/未信任目录里执行工具（代码或文档里的明确规则）——写进设计文档并停下，等 owner 决定。

## Maintenance notes

- 评审方据设计文档与 owner 的决定写实现计划（编号续排），本计划只产出方案。
