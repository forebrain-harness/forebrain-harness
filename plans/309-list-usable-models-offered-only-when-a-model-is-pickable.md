# list_usable_models：无可选模型时不暴露给 LLM

状态：**已完成（2026-11-13）**，改动留主树工作区未 commit。执行摘要见文末「验收记录」。开工基线 `d0d74a072123ecd0497a153dc4ddbe52a94a97b3`（计划撰写基线 `09b44fe323421be5455b0839dc4ecdfb3bc52a89`；漂移检查通过：所有 load-bearing 引用与 `d0d74a07` 源码逐字一致）。

## 1. 目标（Why this matters）

owner 报障（附 TUI transcript 证据）：模型表里没有任何「允许主代理自主选择和使用」的大模型时，
`list_usable_models` 工具仍被注册并加进**发给 LLM 的每次请求体的 tools 字段**，暴露给 LLM。
模型于是会调用它，得到的只是 `{"models":[],"note":"No model is allowed for subagents you pick. …"}`。

要求：发请求前先判断当前是否存在主代理可自主选择/使用的模型（既有语义 = `usableModels` 非空），
有才在 tools 里带上 `list_usable_models`，没有则不带、不暴露。

这同时是仓库既有的工具面哲学（`pkg/run/subagent.go:378-385` 原话）：
"the model is never offered a tool whose only outcome would be a refusal, and the tool table
it is prompted with matches what the session can actually do"——空 roster 的
`list_usable_models` 唯一结局就是一条「没有可选模型」的答复，正是该哲学要排除的实例。

## 2. 根因（file:line 证据链，全部已对照基线源码核实）

1. **注册无条件**：`pkg/run/subagent.go:374` `RegisterSubagentTool` 在
   「subagents 开关开 + catalog 有可派发类型」时（`:386-389`）**无条件**注册
   `list_usable_models`（`:511-533`）。其设计注释（`:505-510`）写明它只跟 dispatch
   开关走（"offered exactly when dispatching is"），从不看 roster 是否非空。
   注册发生在 Runner 的 agent 构建（`pkg/run/runner.go:1345`，`loadLocked` 内）。
2. **请求体 tools 的最终裁决点不做 roster 判定**：每次 LLM 请求都经过
   `toolOrchestrationLLM.Execute`（`pkg/run/orchestration_llm.go:205-210`）：
   ```go
   tools = effectiveToolsForContext(ctx, tools)
   ```
   而 `effectiveToolsForContext`（`pkg/run/controller.go:636-647`）只做上下文嵌套过滤
   （子代理列表闸 `dropSubagentManagementTools` + subtype 过滤），没有任何模型表判定。
   这是 TUI 与 gateway 共用的唯一请求 tools 组装点（两 surface 都走 `run` 层
   `wrapExecutionLLM`，`pkg/run/runner.go:1468-1494`，`loadLocked` 里包 main/fork LLM）。
3. **既有语义已存在，出错路径没用它**：`usableModels`（`pkg/run/subagent.go:4409-4442`）
   是唯一查询对——「`SubagentAllowed` 开关开 + 有凭证」的行，加上本会话 plan hand-off 行
   （hand-off 免开关但仍需凭证，`:4425-4434`）。注释 `:4406-4408` 原话：
   "This and usableModelByID are the one query pair behind both list_usable_models and the
   dispatch's own check, so what the model sees listed is exactly what it may dispatch on."
   ——即「是否有可选模型」这个问题已有唯一答案源，缺的只是把**工具可见性**也挂上去。

**症状→根因链**：owner 环境所有模型行 `SubagentAllowed=false`（TUI 显示
`Models: (none)`）→ 注册条件只看 subagents 开关与 catalog → 工具照常注册 →
请求组装点无 roster 过滤 → 请求体 tools 含 `list_usable_models` → 模型可见并可调用，
得到空 roster 答复。

**被否决的备选**：在 `RegisterSubagentTool` 注册时判定（一次性查表，空则不注册）。
否决理由：① 注册点无会话 ctx，`handOffModelID`（`subagent.go:4496-4509`，从 ctx 取
session id 再读 state）永远评估不出 hand-off 行，会错杀「仅 hand-off 可派发」的会话；
② 注册是静态的，用户在 web 设置里翻 `subagent_allowed` 开关后，已加载的 runner 直到
重载才生效，与 roster 本身 "read live from the model table on every call"
（`subagent.go:537-541` 注释）的既有 live 设计相悖。请求时过滤两点都成立。

## 3. 修复设计

**方案：请求时过滤（挂在 tools 字段的最终裁决点），复用 `usableModels` 原查询。**

### 3.1 改动点 1 — `pkg/run/orchestration_llm.go`

`toolOrchestrationLLM` 结构体（`:24-38`）加一个字段：

```go
// listUsableModelsOffered, when set, answers whether the roster the dispatch
// tools' model argument accepts is non-empty right now; list_usable_models
// rides a request only while it can list something. nil keeps the tool
// offered unconditionally (a bare wrapper with no model store behind it).
listUsableModelsOffered func(context.Context) bool
```

`Execute`（`:210` `tools = effectiveToolsForContext(ctx, tools)` 之后）追加一步按名剔除：
谓词为假时从 `tools` 里去掉名为 `list_usable_models` 的项。实现要点：

- 过滤逻辑放 orchestration_llm.go 内（依赖 wrapper 自己的字段；不要改
  `effectiveToolsForContext` 的纯函数签名——它现在无 IO，保持可裸测）。
- 剔除用按名循环即可；若文件内已有等价的 by-name drop 辅助（执行时 grep
  `filterToolsForSubagentSubtype` 附近），复用之，不新造近重复。
- 注释写明语义来源（见 3.3 行为矩阵的「同一问题」原则）。

### 3.2 改动点 2 — `pkg/run/runner.go` `wrapExecutionLLM`（`:1484-1492`）

在已有 `if o, ok := orchestrated.(*toolOrchestrationLLM); ok { o.withdrawGate = … }`
处一并接线（同一构造后置模式）：

```go
fac := r.subagentFactory() // runner.go:1710-1721，含 Owner: r
o.listUsableModelsOffered = func(ctx context.Context) bool {
    usable, err := usableModels(ctx, fac)
    // 查询出错保留工具：坏掉的 store 不得悄悄改写工具表（fail-open=旧行为）。
    return err != nil || len(usable) > 0
}
```

注册路径（`RegisterSubagentTool`）、guard、`RegisterToolMeta`、卡片渲染
（`pkg/tool/format.go:4183` / `header_parts.go:395`）**全部不动**：工具仍注册，
历史会话回放里已发生的调用仍能渲染卡片，模型无视可见性硬调用时仍由
`guardSubagentTool` 拒绝——可见性收紧只发生在请求体这一层。

### 3.3 行为变化矩阵（★=刻意修订）

| 场景 | 旧行为 | 新行为 |
| --- | --- | --- |
| 无任何可选模型（开关全关/无凭证，无 hand-off）——**owner 报障场景** | 请求 tools 含 `list_usable_models`；调用答空 roster note | ★ 请求 tools 不含；模型看不到 |
| 有 ≥1 个 `SubagentAllowed`+凭证 行 | 含 | 含（不变） |
| 仅 hand-off 行可派发（plan 审批接过模型） | 含（列 1 行，标记 handed_off） | 含（不变；hand-off 属于 `usableModels` 语义，请求时 ctx 可评估） |
| 会话中途在 web 设置翻 `subagent_allowed` | 工具一直在，答复内容变 | ★ 工具在下一请求即出现/消失（live，与 roster 答复同步） |
| 模型表查询出错 | 含 | 含（fail-open，不变） |
| 子代理上下文 + roster 空 | 工具可见（非 `subagent_*` 名，不在子代理列表闸内），调用被 guard 拒 | ★ 工具不可见（收紧到与「不提供只会被拒的工具」哲学一致） |
| subagents 开关关 / catalog 空 | 家族整体不注册（`:386-389`） | 不变 |

**刻意不修（留痕，见 §8）**：dispatch 工具 `model` 参数的 schema 描述
（`subagent.go:102/114/148` "Optional id of a model from list_usable_models…"）与
`usableModelByID` 错误文案（`:4451/4469/4474/4485/4488` "call list_usable_models"）
仍静态引用该工具名。理由：schema 字节稳定是为 prompt cache 故意固定的
（`subagent.go:537-541` 注释）；错误文案只在模型无视工具缺席硬传 id 时可见，
且同句已正确指引 "omit model to use the conversation's model"。

### 3.4 非功能检查

- **延迟**：每次 LLM 请求 +1 次 `fb_llm_models` 全表查询（几行小表；SQLite 本地
  μs 级，远端 PG/MySQL ~ms 级）+ 1 次 `state.Get`（hand-off 读取）。相对秒级 LLM
  请求 <1%，且与工具被真实调用时的查询同源同价。
- **缓存**：Anthropic 前缀缓存只在 roster 空↔非空翻转时受一次影响（罕见，且答复内容
  本来就变）；平时工具表字节稳定不变。
- **健壮性**：查询 err → fail-open；hand-off 读取 err → `handOffModelID` 返回 `""`
  （既有语义，`subagent.go:4505`）；无死锁风险（`usableModels` 不持 Runner 锁）。
- **可维护性**：零新语义——「有没有可选模型」仍只有 `usableModels` 一个答案源；
  过滤点就在 tools 字段的唯一裁决点。

## 4. Scope

**In scope（源码改动仅这 2 个生产文件 + 测试）：**
- `pkg/run/orchestration_llm.go`（结构体字段 + Execute 过滤一步）
- `pkg/run/runner.go`（wrapExecutionLLM 接线一处）
- `pkg/run/orchestration_llm_test.go`（新增测试；若 fixture 更贴近
  `subagent_test.go` 的 `modelArgumentFixture`（`:8393`，播种样例 `:8401`
  `APIKey: key, SubagentAllowed: allowed`），可复用之，不新造重复 fixture）

**Out of scope（明确不碰）：**
- `pkg/run/subagent.go`（注册、guard、schema、错误文案、`usableModels` 本体）
- `pkg/run/controller.go`（`effectiveToolsForContext` 签名与逻辑）
- `pkg/tool/format.go`、`pkg/tool/header_parts.go`（卡片渲染，工具名 case 保留）
- `pkg/gateway`、`pkg/tui`（两 surface 经 run 层自动获得该行为，零 surface 改动）
- 子代理 `subagent_*` 家族的列表闸语义、`PrimaryOnlyToolName`

## 5. Steps（每步带验证）

**第 0 步（并行协调 + 计划落盘）**：`peer_list` / `peer_send` 向
forebrain-harness-78（307，turn record sinks into the engine）确认其 WIP 是否触碰
`pkg/run/orchestration_llm.go` / `runner.go`；有交集则约定先后。把本计划抄送到
`plans/309-list-usable-models-offered-only-when-a-model-is-pickable.md` 并在
`plans/README.md` 顶部块加一行登记（plans/ 已 gitignore，非源码改动）。

**第 1 步（红测复现，修任何源码之前）**：在 `pkg/run/orchestration_llm_test.go` 新增：

```go
func TestToolOrchestrationOmitsListUsableModelsWhenTheRosterIsEmpty(t *testing.T)
```

构造：临时 home + state store + `state.LLMModelStore` 播种 1 行
`{APIKey: "k", SubagentAllowed: false}`（无 hand-off）→ 注册
`RegisterSubagentTool`（参照 `runner_test.go:2860` 或 `subagent_test.go:53` 的
Factory 构造模式）→ 走 `wrapExecutionLLM`（真接线，不手设谓词）→ 驱动一轮
`Execute`，recording inner LLM 捕获收到的 tools → 断言**不含** `list_usable_models`。

验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestToolOrchestrationOmitsListUsableModelsWhenTheRosterIsEmpty -count=1`
→ **FAIL（工具在场）**，失败输出即复现。不红即停（§7 STOP-1）。

**第 2 步（实施 §3.1/§3.2 两个改动点）**。
验证：红测转绿；`gofmt -l` 两个改动文件输出为空。

**第 3 步（补充钉行为的绿测）**，同一测试文件：
- `SubagentAllowed=true` + key → tools 含（防过度收紧）；
- 仅 hand-off（会话 state `PlanHandOff.ModelID` 指向无开关有 key 的行）→ 含；
- store 查询出错（关闭 DB 后查询）→ 含（fail-open）；
- 子代理 ctx（`IsSubagentRunFromContext` 的安装器）+ 空 roster → 不含（钉住矩阵第 6 行）；
- wiring 断言：`wrapExecutionLLM` 返回值断言为 `*toolOrchestrationLLM` 且
  `listUsableModelsOffered != nil`（防接线漏装静默退化为恒可见）。

**第 4 步（回归）**：
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1          # 包级，预期全绿
CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1         # 卡片/渲染包不受影响
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
CGO_ENABLED=1 go vet -tags fts5 ./...                          # CI 同款
```
既有测试若因新行为红（断言了「工具总在场」），按 §3.3 矩阵更新断言并在计划回填理由；
`pkg/run` 之外的失败先查是否 peers WIP 所致（不修，问对应 agent）。

**第 5 步（tmux 真机验收，fake 模型，不烧真配额）**：
1. `CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain`。
2. 临时 `FOREBRAIN_HOME` + 临时项目目录；fake provider
   （`.claude/skills/run-forebrain/fake_provider.py`，plain 应答模式）配为主模型；
   模型行经 sqlite 直插 `fb_llm_models`（表名/列名执行时从 `state.LLMModelStore`
   建表迁移核实），`subagent_allowed=0`、带 key。
3. 场景 1（复现→修复）：tmux 起 TUI，发一条消息 → 从事件日志（plan 286 起，每次模型
   调用落一份请求记录，读法执行时查 `pkg/telemetry`）读该请求报文 tools →
   **断言无 `list_usable_models`**。
4. 场景 2（回归）：sqlite 翻 `subagent_allowed=1` → 同会话发新消息 → 事件日志 →
   **断言含 `list_usable_models`**（live 出现，无需重启）。
5. 场景 3/4（中断自救/非 TTY）裁剪说明：本修复只改请求体 tools 组装，与 Esc/Ctrl+C
   恢复、非 TTY 路径无交互；跑一次常规退出 EXIT_CODE 干净即可，不做专项。

**第 6 步（收口）**：计划文件回填状态行 + 验收记录（场景→证据）；清理 tmux/临时目录；
改动留工作区**不 commit**；报告根因链/最小改动/证据/遗留。

## 6. Done criteria（机器可查）

- [ ] 第 1 步红测在基线上 FAIL（复现留痕），第 2 步后 PASS。
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tool -count=1` 全绿。
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./...` 零报告（含 PKG_CONFIG_PATH）。
- [ ] `gofmt -l` 于两个生产文件 + 测试文件输出为空。
- [ ] 真机两态事件日志证据各一份（无→不含 / 翻开关→含）。
- [ ] `git status` 源码改动仅 §4 In-scope 文件；无 commit。
- [ ] `plans/309-…` 落盘且 `plans/README.md` 有登记行。

## 7. STOP conditions

1. 第 1 步红测在基线**不红**（工具本就不在场）→ 复现失败，停，重查根因，不实施。
2. 发现请求 tools 字段存在绕过 `toolOrchestrationLLM` 的第二组装路径（某 surface
   直连 agent LLM）→ 停，报告路径，方案需重审。
3. `pkg/run/runner.go` / `orchestration_llm.go` 与 peer 307 的 WIP 内容冲突且协调
   不成 → 停，等 owner 裁决先后。
4. 回归红出现在 `pkg/run` 之外且与本次改动无因果（peers WIP）→ 不修，转对应 agent。
5. 真机验收发现工具表出现/消失引起 TUI 异常（卡片/回放报错）→ 停，带屏证报告。

## 8. Maintenance notes

- **静态引用残留**（§3.3 刻意不修）：dispatch 工具 schema 描述与 `usableModelByID`
  错误文案仍引用 `list_usable_models`；若 owner 要求绝对一致，需另立小计划
  （错误文案可条件化，schema 描述动之前必须先算 prompt cache 账）。
- **性能挂钩**：`listUsableModelsOffered` 每请求一查是设计选择（与 roster live 语义
  一致）；若未来模型表变大或远端库延迟敏感，可加 turn 级缓存，但会让「中途翻开关
  下一请求生效」退化为「下一回合生效」，动之前先权衡。
- **评审者须盯**：fail-open 分支（`err != nil ||`）别被"顺手"改成 fail-closed；
  `effectiveToolsForContext` 的纯函数性质不得被破坏。
- **并行会话**：本树另有 307（pkg/turn 方向）与 308（pkg/skill 提示词）在跑；
  本计划文件集与其无设计交集，仅 `pkg/run/runner.go` 有同文件可能，见第 0 步。

## 验收记录（2026-11-13 执行完毕）

改动：`pkg/run/orchestration_llm.go`（+12：字段 + Execute 过滤一步，剔除复用既有 `withoutTool`）、`pkg/run/runner.go`（+7：`wrapExecutionLLM` 接线）、`pkg/run/orchestration_llm_test.go`（+175：6 个测试）。未 commit，留工作区。

- 红测复现：`TestToolOrchestrationOmitsListUsableModelsWhenTheRosterIsEmpty` 修前 FAIL（`list_usable_models rode the request with an empty roster: [subagent_run list_usable_models …]`），修后 PASS。
- 绿测 5 个：可派发行保留 / hand-off 行保留（`state.HandOffPlan`）/ 关库 fail-open 保留 / 子代理 ctx（`tool.WithSubagentRun`）空 roster 不含 / 接线断言 `listUsableModelsOffered != nil`。
- 回归：`pkg/run` ok 42.9s、`pkg/tool` ok 12.3s、`go vet -tags fts5 ./...` exit 0、`gofmt -l` 三文件空。
- 真机（fake provider，独立实例 port 8791，`FAKE_DUMP_DIR` 请求体落盘）：
  - 无模型行 → `n_tools=27 subagent_run=True list_usable_models=False`；
  - 两行均 `subagent_allowed=0`（owner 场景）→ 同上，仍 False；
  - 不重启 sqlite 翻 `subagent_allowed=1` → 下一请求 `n_tools=28 list_usable_models=True`（live 出现）；
  - 常规退出 `EXIT=0`。
- 协调：peer 78 确认 307 全程在 `.worktrees/exec-307`，不碰 `pkg/run`。
- 偏差：第 5 步证据源用 `FAKE_DUMP_DIR` 请求体落盘替代事件日志读取（更直接的同一事实，断言不变）。

## 9. 增补（owner 裁决 2026-11-13：「刻意不修的静态引用残留 必须一并修复」）

§8 第一条残留升格为本期必做。**改动面（`pkg/run/subagent.go`，原 out-of-scope 经 owner 裁决入 scope）**：

1. **schema 描述 ×3**（`:102/:114/:148`）：`Optional id of a model from list_usable_models to run this subagent on. …`
   → `Optional id of a configured model you may run a subagent on. Omit to use the model this subagent type is configured with, else the conversation's model.`
   （沿用 `listUsableModelsDescription` 既成语汇 "the configured models you may run a subagent on"；发现指针留在列表工具自己的描述里——它在场当且仅当有东西可列，两态皆不悬空。）
2. **错误文案 ×5**（`:4451/:4469/:4474/:4485/:4488`）：尾巴统一经新助手 `modelPickGuidance(ctx, fac)` 现场裁决——
   roster 非空或查询出错 → `call list_usable_models, or omit model to use the conversation's model.`；
   roster 空 → `omit model to use the conversation's model.`。
   字节影响：`:4469/:4485/:4488` 非空态字节不变；`:4451/:4474` 非空态补上 omit 尾巴（统一语义；既有 Contains 断言 `subagent_test.go:8503` 兼容）。
3. **prompt cache 账**（§8 要求先算）：schema 三处文本变化 → 含 dispatch 工具的既有会话缓存前缀升级后各重计费一次，之后工具表字节恢复稳定；错误文案是工具结果载荷非前缀，零缓存影响。

测试：`TestSubagentModelArgumentDescriptionIsFixedText` 更新为新固定文本（红→绿）；新增空/非空两态 refusal 尾巴矩阵测试；真机重跑 FAKE_DUMP_DIR 断言请求体 schema 新文本。

**增补执行记录（2026-11-13 完结）**：
- 红测双 FAIL 留痕（`denied row with an empty roster = …call list_usable_models…` + 旧固定文本不匹配）→ 修后全绿；相邻既有 `TestSubagentModelArgumentPinsTheNamedRow`（:8503 Contains 断言）与 `TestSubagentFanoutModelArgumentResolvesAllOrNothing` 未动即过（非空态字节如设计保持）。
- 改动：`pkg/run/subagent.go`（schema tag ×3 + 注释、`modelPickGuidance` 助手、错误文案 ×5）、`pkg/run/subagent_test.go`（固定文本测试更新 + `TestModelArgumentRefusalNamesOnlyAnOfferedTool`）。
- 回归：`pkg/run` ok 44.0s、`pkg/tool` ok 23.6s、`go vet -tags fts5 ./...` exit 0、gofmt 空。
- 真机（fake，port 8792，FAKE_DUMP_DIR）：request-0001 `n_tools=27`、`list_usable_models` 在**整个请求体**零出现（工具表与全部 schema），三处 model 描述（subagent_run.model / subagent_send.model / subagent_fanout.tasks[].model）字节等于新固定文本；常规退出 EXIT=0。
- 旧文本 `from list_usable_models` 全仓（go/md/ts/vue）零残留。工作区共 5 个 in-scope 文件（原 3 + 本增补 2），未 commit。
