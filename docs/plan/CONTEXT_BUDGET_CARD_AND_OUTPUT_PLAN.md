# context_budget：卡片 header 友好化 + LLM 输出只留决策数据

状态：已实施并真机验收通过（2026-11-18，APPROVE；见文末 Implementation Status）。镜像自 session plan fix-context-budget-card-and-output.md（项目惯例，gitignored，禁 commit）。

基线：`dev@5047b7692514728d44088cfb64e5a96bc463d83b`（计划时 `git rev-parse HEAD`）。
drift 检查以内容锚为准（见 Steps 0），行号漂移不触发 STOP，内容不符才 STOP。

## 0. Owner 裁决（原文，约束本计划）

1. 「context_budget工具调用消息卡header 不要直接显示工具名称，对用户不够友好。」
2. 「这个工具返回给llm的信息必须精简，现在返回给llm太多冗余和无价值的信息，删掉对llm无意义的数据和信息，节省token」
3. 「对llm执行任务来说最优价值的信息是否只有 percent left？」
4. 「只需要发给llm可以帮助它判断和决策的数据和信息即可」

## 1. 目标（Why this matters）

- header 不再画 `Called context_budget` 裸工具名，归一到引擎既有友好动词语义（TUI live/replay、web、subagent 进度行三 surface 同源同步）。
- 工具返回 LLM 的 JSON 从 9 字段砍到 **2 个决策字段**（约省 80% token）；卡片 body 与 LLM 同源（引擎通用 JSON 行渲染），同步精简，不新造 formatter。

## 2. 现状证据链（file:line，计划时现场读取）

### 2.1 header 落 fallback 的根因

`pkg/tool/header_parts.go` `DisplayHeaderParts` 的 switch 无 `context_budget` 分支，落 default（计划时 413-433 行）：

```go
default:
    switch {
    case isFailed:
        action = "Failed to call"
    case isRunning || isPending:
        action = "Calling"
    default:
        action = "Called"
    }
    if name := strings.TrimSpace(title); name != "" {
        target = name
    } ...
```

该 default 是给 /migrate 外来工具行的兜底，**保留不动**。消费方（均零改动，自动跟随）：
- TUI：`pkg/tui/tool_header.go:35-37` `ToolDisplayHeader → tool.DisplayHeader`；
- web：`pkg/gateway/wsevents.go`（`DisplayHeader` 调用点）；
- subagent 进度：`pkg/run/subagent_progress.go`。
截图证据：header `Called context_budget`。

### 2.2 输出 9 字段中 7 个非决策数据

`pkg/tool/context_budget.go:29-39` 结构体、`68-84` `contextBudgetOutputOf`。字段决策价值判定（owner 裁决 4 为判据）：

| 字段 | 判定 | 理由 |
|---|---|---|
| percent_left_before_auto_compact | **留** | 核心决策值：距自动压缩线的余量比例，continue/wrap/compact/skip 全靠它；跨模型不变 |
| auto_compact_at_tokens | **留** | 唯一尺度锚：percent 的分母，模型估算「某大步操作放不放得下」时 headroom = 分母×percent/100；与 percent 互不可推导，非冗余 |
| model | 删 | 身份非决策：gauge 永远是调用者自己的（description 已声明），名字不改变任何决策 |
| context_window_tokens | 删 | 原始窗口非行动限额；压缩线才是行动限额，已由 auto_compact_at 编码 |
| effective_input_window_tokens | 删 | provider 输入上限（`pkg/state/usage.go:38-49`），通常==window（截图两者皆 1000000）；不等时其语义已编码进 auto_compact_at（=effective×percent，usage.go:289） |
| used_tokens | 删 | 可推导 ≈ 分母×(1−p/100)；决策不需要精确 used |
| used_percent_of_window | 删 | =round(used×100/window)，推导且分母非行动限额 |
| remaining_tokens_to_window | 删 | =max(window−used,0)，推导 |
| remaining_tokens_to_auto_compact | 删 | =max(分母−used,0) = 分母×p/100，推导 |

推导源：`pkg/state/usage.go:289-300`（autoThreshold=effective×percent/100；PercentLeft=round((threshold−usage)/threshold×100)，clamp≥0）。

### 2.3 body 三 surface 同源，精简自动跟随

- 引擎通用行渲染：`pkg/tool/format.go` `readableRows`（~4705）；无 context_budget 特判。
- web：`pkg/gateway/wsevents.go:979` `readableAnswerFor → tool.ReadableAnswer`；`api_extra.go:2441`。
- 故 JSON 砍字段 → TUI/web body 行数同步减少，**不需要也不允许**新写显示 formatter。

### 2.4 友好动词的既有扩展点（三件套+双门测试）

- case 白名单：`header_parts.go` switch（参照无参工具 `peer_list` ~385-395、`list_usable_models` ~397-405：无 target，动词单独成句）。
- 失败动词：`pkg/tool/display_header.go:221-260` `FailedActionPhrase`。
- 词表：`header_parts.go:512` `HeaderActions()`（~556 行 peers/models 组）。
- golden：`pkg/tool/testdata/header_actions.json`；`TestHeaderActionsGolden`（`UPDATE_HEADER_ACTIONS=1` 再生）。
- 中文表：`ai-elements-vue/packages/elements/src/tool/headerWords.ts` `HEADER_ACTION_ZH`（111-114 行例：`'Listed agents': '已列出代理'`）；`frontend/src/locales/toolHeader.ts` 仅 re-export；`frontend/src/locales/toolHeader.test.ts:13` 断言每个 golden action 都有 zh 词条。
- 门测试：`pkg/tool/header_parts_test.go` `TestHeaderActionsCoverEveryKnownTool`（~207 行起，已知工具×6 状态 action∈HeaderActions）与 `TestHeaderActionsGolden`。

### 2.5 测试锚

- `pkg/tool/context_budget_test.go`：`TestContextBudgetReportsAbsoluteTokens`（39-70，断言全 9 字段）、`TestContextBudgetFreshContextShowsWholeWindow`（74-98，断言推导字段）——随契约更新。
- header 测试 exemplar：`header_parts_test.go:167` `TestDisplayHeaderPartsPeerTools`。
- description 常量 `contextBudgetDescription`/`contextBudgetMeta` **无**测试锚（grep 证实）；footer 的 PercentLeft 走 event payload（`pkg/tui/render.go:4391`），不读工具输出，不受影响。

### 2.6 并行 WIP

工作区 16 项 dirty 全在 `scripts/acceptance/*`、`.claude/skills/run-forebrain/*`（另一 agent forebrain-harness-93 的 WIP），与本计划 scope 零交集；不碰、不还原、不提交。

## 3. 修复设计

### 3.1 输出契约（新）

```go
type contextBudgetOutput struct {
    PercentLeftBeforeAutoCompact int `json:"percent_left_before_auto_compact"`
    AutoCompactAtTokens          int `json:"auto_compact_at_tokens"`
}
```

`contextBudgetOutputOf` 直接取 `p.PercentLeft`、`p.AutoCompactThreshold`；删 `budgetRemaining`、usedPercent 计算、`math` import；结构体注释重写为「决策两数：余量比例及其分母」。键名沿用旧名（body 行标签自解释：`Percent left before auto compact: 57`）。

`contextBudgetDescription` 同步重写（模型可见契约，一次会话付一次）：保留触发句与 「Never estimate…Read-only, no arguments」，中段改为报告 headroom percent 与其分母、并教一次推导 `absolute headroom = budget × percent / 100`。`contextBudgetMeta` 不动。

### 3.2 header 行为矩阵（刻意修订）

| 状态 | 旧 | 新 en | 新 zh |
|---|---|---|---|
| running / awaiting approval | Calling context_budget | Checking context budget | 正在检查上下文预算 |
| completed | Called context_budget | Checked context budget | 已检查上下文预算 |
| failed | Failed to call context_budget | Failed to check context budget | 检查上下文预算失败 |
| canceled | Canceled | Canceled（不变，通用尾） | 不变 |
| denied | Denied | Denied（不变；read-only 预批准实际不可达，通用尾兜底） | 不变 |

实现：`DisplayHeaderParts` 在 `list_usable_models` case 后、`// --- MCP ---` 前加 `case lower == "context_budget":`（无 target，三态动词，failed 走 `FailedActionPhrase`）；`FailedActionPhrase` 加 `case "context_budget": return "check context budget"`；`HeaderActions()` 加三串；golden 再生；zh 表加三词条（放在 models 组后）。

### 3.3 body 行为矩阵

旧 9 行 → 新 2 行（`Percent left before auto compact` / `Auto compact at tokens`），TUI live/replay、web 同步（2.3 同源）。

## 4. Scope

In scope（7 源码/资产文件 + 1 计划镜像）：
1. `pkg/tool/context_budget.go`
2. `pkg/tool/header_parts.go`
3. `pkg/tool/display_header.go`
4. `pkg/tool/testdata/header_actions.json`（再生）
5. `ai-elements-vue/packages/elements/src/tool/headerWords.ts`
6. `pkg/tool/context_budget_test.go`
7. `pkg/tool/header_parts_test.go`
8. `docs/plan/CONTEXT_BUDGET_CARD_AND_OUTPUT_PLAN.md`（本计划镜像，不 commit）

Out of scope（明确不碰）：`pkg/tui/*`、`pkg/gateway/*`、`pkg/run/*`（消费引擎语义，零改动）；`frontend/src/locales/toolHeader.ts/.test.ts`（re-export + golden 驱动，自动过）；`pkg/event/compact_events.go`、`pkg/state/usage.go`（footer/压缩仍用全量字段）；`header_parts.go` default 兜底分支；并行 WIP 的 scripts/skill 文件。

## 5. Steps（每步带验证）

0. 镜像计划到 docs/plan/；drift 锚检查（内容不符→STOP）：
   `grep -n 'case lower == "list_usable_models":' pkg/tool/header_parts.go`（1 处）
   `grep -c 'EffectiveInputWindowTokens' pkg/tool/context_budget.go`（≥1）
   `grep -n "'Listed usable models'" ai-elements-vue/packages/elements/src/tool/headerWords.ts`（1 处）
   `grep -n 'func TestDisplayHeaderPartsPeerTools' pkg/tool/header_parts_test.go`（1 处）
1. 改 `context_budget.go`（3.1）。验证：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` 通过。
2. 更新 `context_budget_test.go`：ReportsAbsoluteTokens 改名/改断言为契约测试——unmarshal 成 `map[string]any` 断言键集恰为 {percent_left_before_auto_compact, auto_compact_at_tokens} 且值 63/900000（fixture 不变）；FreshContext 断言 100/180000；错误路径两测试不动。验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestContextBudget -count=1` 绿。
3. `header_parts.go` case + `HeaderActions()` 三串；`display_header.go` `FailedActionPhrase` 词条。验证：build 通过。
4. `header_parts_test.go`：coverage map 加 `"context_budget": {}`；新增 `TestDisplayHeaderPartsContextBudget`（仿 PeerTools：completed/running/awaiting approval/failed/canceled 五断言）。验证：`go test -tags fts5 ./pkg/tool -run 'TestDisplayHeaderParts|TestHeaderActionsCover' -count=1` 绿；此时 `TestHeaderActionsGolden` 应**红**（golden 未再生）——预期，进步骤 5。
5. `UPDATE_HEADER_ACTIONS=1 CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestHeaderActionsGolden -count=1` 再生 golden；去掉 env 重跑绿。
6. `headerWords.ts` 加三 zh 词条。验证：`cd frontend && pnpm install（如需）&& pnpm typecheck && pnpm test`（toolHeader 完整性绿）；`cd ai-elements-vue/packages/elements && pnpm test`。
7. 回归：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui ./pkg/gateway ./pkg/run -count=1`；`CGO_ENABLED=1 go vet -tags fts5 ./...`；`gofmt -l cmd pkg third_party plugins` 无输出。
8. tmux 真机（加载 run-forebrain skill 按其 driver.sh 配方；构建必带 `-tags fts5`；隔离 FOREBRAIN_HOME+临时项目目录，禁污染真实 home）：
   - fake provider：`fake_provider.py tool <port> '{"name":"context_budget","arguments":{}}'`（argv=mode port text），`FAKE_TOOL_ONLY=1`、`FAKE_DUMP_DIR=<tmp>/dumps`。
   - 断言 A（live）：`tmux capture-pane` header 为 `Checked context budget`，无 `Called context_budget`；body 恰 2 行。
   - 断言 B（LLM 侧真跑）：dumps 里第二个请求的 tool_result content 为恰 2 键 JSON。
   - 断言 C（replay）：resume 同会话，header/body 与 live 一致。
   - 回归 D：shell 模式卡 header `Ran …` 不变。
   - 清理：kill-session、删临时目录。
9. 回填两计划文件「验收记录」（场景→证据）；报告。改动留工作区，不 commit。

## 6. Done criteria（机器可查）

- `context_budget` 输出键集 == 2（步骤 2 测试断言）；`grep -c 'budgetRemaining' pkg/tool/context_budget.go` == 0。
- `grep -n 'case lower == "context_budget":' pkg/tool/header_parts.go` == 1；golden 与 zh 表完整性测试绿。
- 步骤 7 全绿；gofmt/vet 干净。
- 真机断言 A-D 取证入计划。
- `git status` 增量 == In scope（并行 WIP 原样）；无 commit。

## 7. STOP conditions

- drift 锚内容不符（并行会话改了同文件）→ 停，报告。
- 真机出现 failed 卡（gauge unavailable）→ 先诊断 `CallingAgentContextBudget`/`ContextOccupancy`（pkg/run/config.go:2738），不得改断言凑绿。
- frontend/elements 测试基建因并行 WIP 损坏 → 报告，不修他人文件。
- 步骤 4 预期红不出现或步骤 5 后仍红 → 停，查词表链路。

## 8. Maintenance notes

- 今后新工具友好化必须走三件套（case + HeaderActions + golden + zh），双门测试与 frontend 完整性测试是守门员。
- 决策契约判据（owner 2026-11-18）：工具输出只发帮助 LLM 判断/决策的数据；推导值/身份值不发。footer/压缩如需全量数，取 event payload，不得复活工具输出字段。
- reviewer 重点：两字段是否覆盖决策（percent=proximity，分母=scale，headroom 可推导）；zh 措辞与既有风格一致；default 兜底未被误改。

## Implementation Status

- 裁决：APPROVE（tech lead 复核，0 轮修订）。
- 执行者：opencode-go/deepseek-v4.1-flash；worktree `.worktrees/fix-context-budget-card-and-output`，分支同名；commit `f997eb92`（base `b0cc564b`＝计划基线 `5047b769` 之上一个 scripts-only 提交）。合并回 dev 是 owner 的决定；合并后按常设规则删 worktree/分支。
- 复核自跑（worktree 内）：
  - 定点 `go test -tags fts5 ./pkg/tool -run 'TestContextBudget|TestDisplayHeaderParts|TestHeaderActions'` ok（含 golden 无 env 绿）。
  - 回归 `./pkg/tool ./pkg/tui ./pkg/gateway ./pkg/run -count=1` 全 ok；`go vet -tags fts5 ./...` OK；`gofmt -l cmd pkg third_party plugins` 无输出。
  - frontend `npx vitest run src/locales/toolHeader.test.ts` 4/4；elements `pnpm test` 16/16。
  - grep：`budgetRemaining`=0；`case lower == "context_budget":`=1；`diff --stat b0cc564b..HEAD` 恰 7 个 in-scope 文件（+72/−59）；worktree clean。
  - 真机自跑（隔离 fb-ctxreview、port 8766、FAKE_TOOL_ONLY=1）：A live `● Checked context budget`、body 恰 2 行（Percent left before auto compact: 100 / Auto compact at tokens: 900000）、`Called context_budget` 0 处；B dumps/request-0002.json tool_result 恰 2 键 `{"percent_left_before_auto_compact":100,"auto_compact_at_tokens":900000}`；C resume 同会话 header/body 一致、裸名 0；D shell 卡 `Ran echo hello-from-shell` 不变；tmux/临时目录已清理。
- 偏差（最小、已说明、接受）：测试改名 `TestContextBudgetReportsDecisionPair`/`…FreshContextShowsWholeBudget`（计划允许「改名」）；新增注释；执行环境适配（third_party 符号链接自主树、elements node_modules symlink farm、被 install 改写的 ai-elements-vue/pnpm-lock.yaml 恢复 HEAD）。
- 遗留/旁注：frontend 全量 suite 1 个预存 flaky 失败（ModelFormDrawer.test.ts；单跑 19/19 绿、stash 对照全量同样 1 失败但不同用例；import 链不触 headerWords）——预存、scope 外、未修。执行者因磁盘 100% 跑 `pnpm store prune`（全局 pnpm store 清空、可再生、已装 node_modules 不受影响）——报备 owner。
- 任务状态：Steps 0–8 全 done（step 0 镜像与 step 9 回填由 reviewer 完成）；Done criteria 全过。
- 收尾（owner 指令「合并回主树dev分支」）：worktree `rebase dev` 无冲突（dev 新增 /config 提交与本分支零交集）→ 主树 `merge --ff-only` dev@`8e64744a`（f997eb92 rebase 后）→ worktree 与分支已删（2026-11-17 强制收尾）。合并后主树复跑 `pkg/tool` 定点 + `pkg/tui` 全量绿；主树工作区 clean（并行 agent 的 WIP 由其自行提交）。