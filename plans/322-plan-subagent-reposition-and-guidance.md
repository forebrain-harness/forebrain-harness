# Plan 322: 把 built-in plan 子代理重定位为「独立设计/计划评审 + 只读设计提案」，并在计划工作流里引导主代理使用它

> **Executor instructions**: 按步骤顺序执行本计划。每一步的 Verify 命令必须实跑并确认期望结果后再进入下一步。出现「STOP conditions」所列情况时停下并报告，不要即兴发挥。完成后更新 `plans/README.md` 里本计划的状态行——除非派发你的评审者声明由它维护索引。
>
> **Drift check (run first)**:
> ```bash
> git diff --stat 42d4485d..HEAD -- pkg/agent/subagent_defs.go pkg/agent/advisor_prompts.go pkg/agent/advisor_prompts_test.go pkg/run/subagent_catalog.go pkg/run/config.go pkg/tool/session_tools.go pkg/run/subagent_catalog_test.go pkg/run/subagent_test.go pkg/run/config_test.go pkg/agent/subagent_defs_test.go pkg/tool/session_tools_test.go
> ```
> 任一 in-scope 文件自本计划写成后有改动时，先逐条对照下方「Current state」摘录与活体代码；**内容不匹配即 STOP**（仅行号漂移不算不匹配，用符号/grep 重定位）。

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none（与 318 共改 `pkg/run/config.go` 但区域不相交，见 Maintenance notes）
- **Category**: direction / model-guidance（模型可见文本契约）
- **Planned at**: commit `42d4485d`（dev）, 2026-11-18

## Why this matters

owner 报障「built-in plan 子代理从来没被主代理用过」，要求裁决删除还是优化。定向审计（状态台账按任务去重）证明前提不准确：plan 型有 **12 次唯一派发**（2026-09-04 ~ 2026-11-15，ok 6 / failed 3 / cancelled 3），其中 **10 次是模型自发的「独立计划评审 / 冷读 / 设计压力测试」**（如 Cold-read plan review、Review workbuddy plugin plan、复核待办分栏抽屉方案），只有 2 次是真正的实现设计且都失败于早期审批墙（`tool shell/web_fetch: requires action`）。对照组：general-purpose 538、explore 139、系统 plan-reviewer 130。

「实现设计」定位落空是结构性的：内建计划流水线没给 plan 型任何角色（enter_plan_mode 只引导 explore 做研究、主代理自己设计写计划；执行硬编码 general-purpose；评审归系统保留型 plan-reviewer），模型可见的引导只有一句 enum 描述，且 plan 型提示词缺主代理与 plan-reviewer 都有的交接质量标准（`PlanHandoffQualityPrompt`）。但「独立评审」需求真实存在——plan-reviewer 模型派发不到、只在 exit 门口评审本会话计划文件，跨仓库/设计中途/非会话计划的评审需求模型反复拿 plan 型顶替。

**owner 已裁决：保留并优化引导，不删除。** 本计划把 plan 型的公开定位对齐它的真实用途（独立设计/计划评审 + 只读设计提案），补齐提示词质量缺口，并在计划工作流的三个引导点明确告诉主代理何时派发它。不改工具面、不改配置契约、不删类型。

## Current state

以下摘录全部来自 advisor 亲读（基线 `42d4485d`），执行前逐条核对。

### 1. `pkg/agent/subagent_defs.go` — built-in 类型定义（静态，全项目类型共享）

`:20` 公共类型名单（**不改成员与顺序**）：

```go
var PublicSubagentNames = []string{"general-purpose", "explore", "plan", "verification"}
```

`:45-52` plan 定义现状（本计划 Step 1 的改动对象）：

```go
"plan": {
	Name:         "plan",
	Description:  "Planning-only subagent",
	Source:       "built-in",
	OneShot:      true,
	Continuable:  false,
	SystemPrompt: "You are a plan subagent for implementation design. This is read-only: modify no files, run no mutating commands, never claim implementation was done. Analyze the codebase, identify the relevant files and constraints, and produce a concrete phased plan with risks, sequencing and verification." + ScopeDisciplinePrompt + planScopeDisciplineAddendum + NonFunctionalRequirementsPrompt + bugDiagnosisPrompt + GitWorktreePrompt,
},
```

提示词组件的角色分工记录在 `pkg/agent/advisor_prompts.go`：
- `PlanHandoffQualityPrompt`（`:34-49`，交接质量标准）今天只挂**主代理**系统提示（`pkg/run/runner.go:1282` `desc += agent.PlanHandoffQualityPrompt`）与 **plan-reviewer**（`subagent_defs.go:153` 和 `:164`）。plan 型没有。
- `codingReviewPrompt`（`advisor_prompts.go:99-101`，评审纪律：从症状独立重推因果、跑计划自带的复现、未验证根因是最严重发现）今天只给主代理（经 `BugRootCausePrompt`，`runner.go:1275`）与 coding plan-reviewer（`subagent_defs.go:164`）。它要求的是**运行**既有复现命令与独立重推，不要求写文件——与 plan 型的只读工具面兼容（写复现测试是 plan-reviewer 专属的 `planReviewerReproductionPrompt` + `tool.ReviewWriteScope`，本计划不引入）。
- `advisor_prompts.go:19-22` 头注释与 `:97-98` `codingReviewPrompt` 注释枚举了各角色携带的提示词部分，Step 2 同步更新。

### 2. `pkg/run/subagent_catalog.go` — 模型可见的 enum 描述（唯一的类型选择引导）

`:22-27`：

```go
var publicSubagentEnumSentences = map[string]string{
	"general-purpose": "bounded independent implementation or general delegated work",
	"explore":         "read-only investigation and locating facts",
	"plan":            "read-only implementation design, sequencing, risks, and verification planning",
	"verification":    "falsify or verify an implementation with concrete checks",
}
```

`EnumDescription()`（`:251-278`）把每个启用的 built-in 拼成 ` <name>: <sentence>.` 放进 `subagent_type` 的 schema description；office/batch 用各自的句子表（`agent.OfficeSubagentEnumSentences`/`BatchSubagentEnumSentences`，plan 型在这两类会话不可派发，`office_prompts.go:98` `OfficeSubagentNames = []string{"general-purpose", "explore"}`——**不改**）。

### 3. `pkg/tool/session_tools.go` — enter_plan_mode 工具描述

`enterPlanModeDescription` 常量（约 `:340-352`）最后一段现状：

```
In plan mode, explore the code and its existing patterns (explore subagents for parallel research), design the approach, ask the user only what they must decide, write the plan to the session plan file - the only file you may edit - and call exit_plan_mode when ready to implement.
```

全仓没有任何测试逐字钉住这段描述（已 grep 核实：该短语只出现在 `session_tools.go`）。另：`session_tools.go:635` 的 exit 后执行派发硬编码 `subagent_type "general-purpose"`——**不改**（执行者角色与本计划无关）。

### 4. `pkg/run/config.go` — coding plan 模式完整提醒

`buildPlanModeReminder(planDir, planFile string, planExists bool, kind string, isReentry bool, isSubagent bool)`（`:1097`）。`isSubagent` 分支（`:1102-1136`）与 `kind == "sparse"` 分支（`:1137-1148`）是独立模板，**不改**。主代理 full 模板（`fmt.Sprintf` 约 `:1168-1213`）里本计划要动的两行现状：

```
Phase 3 - Review: pressure-test the plan. Identify failure modes, missing tests, and unresolved decisions.
```

```
- subagent_run subtype=explore, subagent_fanout subtype=explore: free use for research
```

该提醒每 5 轮重发一次（`planModeFullReminderEveryNTurns = 5`，`config.go:639`）；coding full 提醒**没有**逐字 golden（office 变体才有 `pkg/run/testdata/office_plan_prompts.golden.json`），既有测试只做 contains 断言（见下）。

### 5. 工具面事实（本计划保持不变，列出防止执行者「顺手」改动）

- `pkg/tool/permissions.go:789-795` `typedSubagentDisallowedTools`：`"plan": {"write_file": {}, "edit_file": {}}`（与 explore 完全相同）。类型级禁写先于 GuardWrite 的计划文件豁免——plan 子代理连会话计划文件都写不了，这是**有意的**：主代理是计划作者，plan 子代理只回文本。
- `pkg/tool/shell_tool.go:1742` `shellSandboxProfile`：`case "explore", "plan", "plan-reviewer", ...` 走只读沙箱。
- `planModeBlockedTools` 为空（`permissions.go:741`）：plan 模式下主代理可以派发任何子代理，无模式阻碍。

### 6. 仓库约定与测试 exemplar

- **模型可见文本必须有测试钉住**（FOREBRAIN.md「Model-visible texts ... have golden tests」）。既有 exemplar：
  - `pkg/run/config_test.go:1993` `TestCodingPlanReminderUnchanged`：对 `buildPlanModeReminder(..., "full", false, false)` 做 contains 断言，并断言 sparse/subagent 变体不重复主代理专属文本。
  - `pkg/run/subagent_catalog_test.go:157` `TestSubagentCatalogEnumDescriptionNamesCustomSubagents`：contains 断言 + `:178-184` 未配置 agent 的**整串逐字**断言。
  - `pkg/run/subagent_test.go:1479-1487`：工具 schema enum description 的 contains 断言，其中 `:1482` 为 `"plan: read-only implementation design"`。
  - `pkg/agent/subagent_defs_test.go:45-46`：`SystemPromptForSubtype("plan")` 非空检查（包内测试，可直接引用未导出常量）。
- 提示词用英文、与既有组件拼接（`+ ScopeDisciplinePrompt + ...`）；注释解释**角色分工与为什么**，照 `advisor_prompts.go` 头注释的风格。
- 文案禁 `·` 连接符（owner 常设规则，plan 316 已全仓清扫）——新增文本用空格/破折号。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 环境（直接调 go 编译 pkg/pdf 依赖链必需，Makefile 只在 make 内设置） | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"` | 无输出；`third_party/pdfium/<os>_<arch>` 已由 `make build` 拉取存在 |
| 焦点测试（快） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/agent -count=1` | ok |
| 焦点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestCodingPlanReminder\|TestSubagentCatalog\|TestSubagentToolSchemasExposeSubtypeGuideToLLMAndToolMeta' -count=1` | ok |
| 焦点测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestEnterPlanMode' -count=1` | ok |
| 三包全量 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/agent ./pkg/run ./pkg/tool -count=1` | 全部 ok（pkg/run 较慢，分钟级） |
| 格式 | `gofmt -l pkg/agent pkg/run pkg/tool` | 无输出 |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/agent ./pkg/run ./pkg/tool` | exit 0 |
| 合并前全量门禁（scratch home；**本机磁盘长期 ~98%，必须 `-p 2` 限并发**，见 Maintenance notes） | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 -p 2 ./... -count=1` | 全部 ok |

## Scope

**In scope**（唯一允许修改的文件）：
- `pkg/agent/subagent_defs.go`（plan 定义 Description + SystemPrompt）
- `pkg/agent/advisor_prompts.go`（仅注释：角色分工枚举）
- `pkg/run/subagent_catalog.go`（仅 `publicSubagentEnumSentences["plan"]` 一句）
- `pkg/tool/session_tools.go`（仅 `enterPlanModeDescription` 最后一段追加一句）
- `pkg/run/config.go`（仅主代理 full plan 提醒模板的 Phase 3 行与 tool guidance 一行）
- 测试：`pkg/run/subagent_catalog_test.go`、`pkg/run/subagent_test.go`、`pkg/run/config_test.go`、`pkg/agent/subagent_defs_test.go`、`pkg/tool/session_tools_test.go`、`pkg/agent/advisor_prompts_test.go`（角色矩阵，扩 scope 批准见下节）

## Scope extension record（2026-11-18，执行期 STOP → 派发者活体核验 → 批准）

执行者在 Step 1 Verify 命中 STOP 条件 #3：`pkg/agent/advisor_prompts_test.go:219` `TestCodingRulesReachEveryCodingRole` 用角色矩阵钉住「哪个 built-in 携带哪个规则组件」，其中 `codingReviewPrompt: {}` 断言没有任何 built-in 携带它——与本计划给 plan 型加 `codingReviewPrompt` 直接矛盾。advisor 写计划时漏扫该矩阵（原「既有测试只查非空与名字」的说法为假，已更正）。派发者亲读该测试全文核验：
- 冲突**仅此一处**：`PlanHandoffQualityPrompt` 不在矩阵里（只在 plan-reviewer 专属断言段出现），给 plan 型加它不触发第二处失败；`runner_test.go:2799-2809` 钉主代理组装，不受影响。
- 批准的最小扩 scope＝该测试文件两处：① 矩阵行 `codingReviewPrompt: {},` → `codingReviewPrompt: {"plan": true},`；② 测试头注释 `plan diagnoses,` → `plan diagnoses and reviews,`（与 Step 2 同口径）。
- 矩阵语义不变：仍逐 built-in 断言携带/不携带，explorer/verification 仍不携带任何 coding 规则。

**Out of scope**（看似相关，禁止触碰）：
- `pkg/tool/permissions.go`、`pkg/tool/shell_tool.go`、`pkg/tool/registry.go`——plan 型只读工具面是契约，不开任何写口（含会话计划文件）。
- `plan-reviewer` / `codingPlanReviewerPrompt` / `planReviewerReproductionPrompt`（`subagent_defs.go:123-200`）——门口评审员角色不变，不与 plan 型合并。
- `agent.PublicSubagentNames` 成员与顺序、`pkg/config/load.go:24,39` 白名单、配置 schema——类型名 `plan` 不改名不删除（用户配置兼容）。
- office/batch 提示词与句子表（`office_prompts.go`）、`OfficeSubagentNames`。
- `session_tools.go:635` 执行派发（general-purpose 硬编码）、`runner.go` 主代理系统提示组装。
- 前端——web 子代理页的 built-in 描述来自 API 的 `Definition.Description`，无硬编码（已 grep 核实 `frontend/src` 无 "Planning-only"）；locales 无需改。
- `pkg/run/testdata/office_plan_prompts.golden.json`——office 变体不动。

## Git workflow

owner 常设规则：**默认禁止任何 git commit**（owner 手动审阅提交）。隔离方式跟随 owner 当次指令：in-tree 零 commit，或 worktree（`.worktrees/<name>`）+ exec 分支 + rebase/ff-merge 收尾。永不 push。本计划不预设 commits；若 owner 指示 worktree 执行，分支名建议 `exec-322-plan-subagent-guidance`。

## Steps

### Step 1: 重定位 plan 定义（`pkg/agent/subagent_defs.go`）

把 Current state §1 摘录的 plan 条目整体替换为（Description 与 SystemPrompt 开头重写；拼接链插入 `PlanHandoffQualityPrompt` 与 `codingReviewPrompt`，其余组件与顺序保持）：

```go
"plan": {
	Name:         "plan",
	Description:  "Read-only design proposals and independent plan critique",
	Source:       "built-in",
	OneShot:      true,
	Continuable:  false,
	SystemPrompt: "You are a plan subagent: an independent read-only reviewer of designs and plans, and a designer of implementation approaches. This is read-only: modify no files, run no mutating commands, never claim implementation was done. When given a design or plan, pressure-test it against the real sources - verify its claims, find gaps, wrong assumptions, missing verification and risks, and say what you would change. When asked for a design, analyze the codebase, identify the relevant files and constraints, and produce a concrete phased plan with risks, sequencing and verification." + ScopeDisciplinePrompt + planScopeDisciplineAddendum + PlanHandoffQualityPrompt + NonFunctionalRequirementsPrompt + bugDiagnosisPrompt + codingReviewPrompt + GitWorktreePrompt,
},
```

同步更新角色矩阵测试 `pkg/agent/advisor_prompts_test.go`（批准的扩 scope，见「Scope extension record」）：
- 矩阵行 `codingReviewPrompt: {},` → `codingReviewPrompt: {"plan": true},`
- 测试头注释 `plan diagnoses, general-purpose diagnoses and executes,` → `plan diagnoses and reviews, general-purpose diagnoses and executes,`

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/agent -count=1` → ok（含更新后的角色矩阵 `TestCodingRulesReachEveryCodingRole`；注意该矩阵钉住组件携带关系，是 Step 1 的真正守卫）。

### Step 2: 同步角色分工注释（`pkg/agent/advisor_prompts.go`，仅注释）

- 头注释 `:19-22` 中 `plan diagnoses, general-purpose diagnoses and executes, the plan reviewer diagnoses and reviews` → `plan diagnoses and reviews, general-purpose diagnoses and executes, the plan reviewer diagnoses and reviews`。
- `codingReviewPrompt` 上方注释（`:97-98`）`for whoever reviews a coding plan: the primary and the coding plan reviewer.` → `for whoever reviews a coding plan: the primary, the plan subagent and the coding plan reviewer.`

**Verify**: `gofmt -l pkg/agent` → 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/agent -count=1` → ok。

### Step 3: 改 enum 句子（`pkg/run/subagent_catalog.go`）

`publicSubagentEnumSentences["plan"]` 一句替换为：

```go
"plan": "independent read-only critique of a design or plan, and read-only implementation design proposals",
```

同 Step 更新两处 golden（同一逻辑单元，保持树不红）：
- `pkg/run/subagent_catalog_test.go:164`：`"plan: read-only implementation design, sequencing, risks, and verification planning."` → `"plan: independent read-only critique of a design or plan, and read-only implementation design proposals."`
- `pkg/run/subagent_catalog_test.go:180` 未配置整串中的同一句做相同替换（整串其余部分逐字不动）。
- `pkg/run/subagent_test.go:1482`：`"plan: read-only implementation design"` → `"plan: independent read-only critique"`。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestSubagentCatalog' -count=1` → ok；`grep -rn "read-only implementation design, sequencing" pkg` → 0 命中；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestSubagentToolSchemasExposeSubtypeGuideToLLMAndToolMeta' -count=1` → ok（该测试在 `pkg/run/subagent_test.go:1375`，`:1482` 的 contains 断言已在上面同改）。

### Step 4: enter_plan_mode 描述追加引导（`pkg/tool/session_tools.go`）

`enterPlanModeDescription` 最后一段末尾追加一句（其余逐字不动）：

```
For a non-trivial design, dispatch a plan subagent (subagent_run subtype=plan) to pressure-test your draft independently before you exit.
```

新增钉住测试（`pkg/tool/session_tools_test.go`，包内；照既有 `TestEnterPlanMode*` 的包名与风格）：

```go
func TestEnterPlanModeDescriptionGuidesThePlanSubagent(t *testing.T) {
	if !strings.Contains(enterPlanModeDescription, "subagent_run subtype=plan") {
		t.Fatal("enter_plan_mode description must guide the plan subagent for non-trivial designs")
	}
}
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestEnterPlanMode' -count=1` → ok（含新测试）。

### Step 5: plan 模式完整提醒加引导（`pkg/run/config.go`）

主代理 full 模板（`buildPlanModeReminder` 内 `fmt.Sprintf`，Current state §4）改两处：

1. Phase 3 行替换为：

```
Phase 3 - Review: pressure-test the plan. Identify failure modes, missing tests, and unresolved decisions. For a non-trivial design, dispatch subagent_run subtype=plan for an independent cold-read critique of your draft, and fold its findings into the plan.
```

2. tool guidance 列表在 explore 行之后新增一行：

```
- subagent_run subtype=plan: independent read-only critique of your draft design or plan; it returns findings and cannot write files
```

扩展 `pkg/run/config_test.go` 的 `TestCodingPlanReminderUnchanged`（`:1993`，照其 contains 风格追加断言）：
- full 主代理提醒 contains `"dispatch subagent_run subtype=plan for an independent cold-read critique"`；
- full 主代理提醒 contains `"- subagent_run subtype=plan: independent read-only critique"`；
- 既有 `for _, kind := range []string{"full", "sparse"}` 的 subagent 循环里追加：`sub` 不得 contains `"cold-read critique"`（子代理派发不了子代理，引导只给主代理）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestCodingPlanReminder' -count=1` → ok。

### Step 6: 钉住 plan 提示词的新组件（`pkg/agent/subagent_defs_test.go`）

新增包内测试（照 `:45-46` 的 `SystemPromptForSubtype` 用法）：

```go
func TestPlanSubagentPromptCarriesTheHandoffBarAndReviewRule(t *testing.T) {
	got, ok := SystemPromptForSubtype("plan")
	if !ok {
		t.Fatal("plan system prompt missing")
	}
	if !strings.Contains(got, PlanHandoffQualityPrompt) {
		t.Fatal("plan subagent must carry the handoff quality bar")
	}
	if !strings.Contains(got, codingReviewPrompt) {
		t.Fatal("plan subagent must carry the plan-review discipline")
	}
	if !strings.Contains(got, "pressure-test it against the real sources") {
		t.Fatal("plan subagent must state its independent-critique role")
	}
}
```

（该文件为 `package agent`，`strings` 已 import，直接追加函数即可。）

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/agent -count=1` → ok（含新测试）。

### Step 7: 全包门禁

**Verify**:
1. `gofmt -l pkg/agent pkg/run pkg/tool` → 无输出
2. `CGO_ENABLED=1 go vet -tags fts5 ./pkg/agent ./pkg/run ./pkg/tool` → exit 0
3. `CGO_ENABLED=1 go test -tags fts5 ./pkg/agent ./pkg/run ./pkg/tool -count=1` → 全部 ok
4. 残留扫描：`grep -rn "Planning-only subagent\|read-only implementation design, sequencing" pkg` → 0 命中
5. 合并前（owner 收尾时）：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 -p 2 ./... -count=1` → 全部 ok（`-p 2` 必需，见 Maintenance notes 磁盘条）

真机验收为**可选**（本计划是提示词文本变更，golden/contains 测试即相称证明）：owner 如需实跑，按 `run-forebrain` skill 起会话进 plan 模式，确认提醒含新 Phase 3 句；派发一次 `subagent_type=plan`，在 `~/.forebrain/logs/debug.log` 的子代理请求体里确认 system prompt 含 "Handoff plan quality bar"。

## Test plan

- 更新既有 golden：`subagent_catalog_test.go`（contains + 未配置整串）、`subagent_test.go`（schema description contains）——Step 3。
- 扩展 `TestCodingPlanReminderUnchanged`：主代理 full 提醒含两处新引导；sparse/subagent 变体不含——Step 5。
- 新增 `TestPlanSubagentPromptCarriesTheHandoffBarAndReviewRule`（`pkg/agent/subagent_defs_test.go`）——Step 6。
- 新增 `TestEnterPlanModeDescriptionGuidesThePlanSubagent`（`pkg/tool/session_tools_test.go`）——Step 4。
- 失败路径覆盖：subagent 变体不得携带派发引导（既有循环追加断言）；未配置 agent 的 enum 整串逐字一致（既有断言刷新后继续生效）。
- 结构 exemplar：`TestCodingPlanReminderUnchanged` 与 `TestSubagentCatalogEnumDescriptionNamesCustomSubagents`。

## Done criteria

全部成立才算完成：

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/agent ./pkg/run ./pkg/tool -count=1` 全绿（含 2 个新测试与刷新后的 golden）
- [ ] `gofmt -l pkg/agent pkg/run pkg/tool` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/agent ./pkg/run ./pkg/tool` exit 0
- [ ] `grep -rn "Planning-only subagent\|read-only implementation design, sequencing" pkg` 0 命中
- [ ] `grep -n "PlanHandoffQualityPrompt + NonFunctionalRequirementsPrompt + bugDiagnosisPrompt + codingReviewPrompt" pkg/agent/subagent_defs.go` ≥1 命中（plan 条目拼接链）
- [ ] `git status` 改动文件全部在 In scope 列表内
- [ ] `plans/README.md` 本计划状态行已更新（除非评审者维护索引）

## STOP conditions

停下并报告，不要即兴发挥：

- 「Current state」任一条摘录与活体代码内容不匹配（漂移检查命中且内容变了）。
- `pkg/run/config.go` 的 plan 提醒模板区已被并行计划（318 等）重写，Phase 3/tool guidance 锚点不存在——报告冲突，不要自行合并语义。
- 发现第三处 golden/测试钉住旧文案且不在 In scope 测试列表内——报告，经批准后再扩 scope。
- 任一 Verify 合理修复后仍失败两次。
- 认为需要给 plan 型开写权限（含计划文件）、改 `typedSubagentDisallowedTools`、改 plan-reviewer、改类型名才能完成——这些是契约禁区，报告而不是做。
- 关键假设为假：`codingReviewPrompt` 与只读工具面冲突（它只要求运行既有复现与独立重推，不要求写文件；若执行者发现实际语义要求写，停下报告）。

## Maintenance notes

- **一份契约三个表达**：enum 句子（`subagent_catalog.go`）、Definition（`subagent_defs.go`）、计划工作流引导（`session_tools.go` + `config.go` 提醒）描述同一个角色。未来改 plan 型角色时三处与 golden 必须同改；评审时重点盯这三处是否一致。
- **plan 型 ≠ plan-reviewer**：plan-reviewer 是 exit 门口的制度性评审（系统派发、coding 变体带复现写权限）；plan 型是主代理设计中途的自选独立批判。不要把两者合并，也不要把 plan 型加进系统保留名单。
- **只读不变式**：plan 型禁写是有意的（主代理是计划作者，子代理回文本由主代理转录）。若未来出现「让 plan 子代理直接写计划文件」的需求，那是新的 owner 裁决，不在本计划语义内。
- **并行工作（2026-11-18 更新）**：318 与 context-budget 均已落地 dev（本分支已 rebase 到 `5cfe6312` 无冲突）；319/320 由 forebrain-harness-93 排队执行（新 worktree exec-319-effort-pickers-2 / exec-320-approval-preselect-3，门禁已写死 `-p 2` + df<2G 停手），与本计划零交集（已 peer 确认）。
- **磁盘与全量门禁（2026-11-18 实证）**：本机磁盘长期 ~98% 满。冷缓存下 `go test ./...` 默认并发会把各包链接峰值叠加，实测两次把磁盘打穿（`ld: write() failed, errno=28` / lsp fake server `mapping output file failed: no space left on device`），表现为大面积无关包 build failed——**先查 `df` 再定性，别当代码回归**。全量门禁一律 `-p 2`（限并发后 39 ok/0 FAIL 一次绿）；`go clean -cache` 只治标（缓存会重建到 ~6.7G），清缓存/复跑须与并行会话协调窗口（93 已约定同用 `-p 2`）。
- **既有 flaky（未修，建议另立计划）**：`pkg/gateway` `TestPaidTierRESTOffersTakesAndDismisses`（run_control_test.go:3013）断言与异步重发 turn 的新 offer 上架赛跑；执行期 R2 红/R1+R3 绿/单测×20 绿，-p 2 干净门禁轮亦绿。与提示词改动无因果路径。
- **token 成本**：full 提醒每 5 轮重发（`config.go:639`），新增约两行是有意代价；不要为省 token 把引导挪进 sparse 变体（sparse 是短提醒，契约会破坏既有测试断言）。
- **证据基线**：台账统计截至 2026-11-18（`~/.forebrain/workspace/state/subagent-history.jsonl`，按 `(session_id, task_id, task_index)` 去重取最新 `updated_at`）。未来复审「plan 型是否被用起来」用同法重算；若引导落地后设计用途仍为零而评审用途持续，说明重定位正确，不要回头再提删除。
- **已考虑并否决**：A 完全删除（owner 裁决否决：10 次评审需求将失去唯一合身公共类型——explore 提示词禁止产出计划、general-purpose 是可写执行者、fork 继承上下文破坏冷读）；给 plan 型开计划文件写口（破坏只读不变式与主代理作者地位）；改名（破坏配置兼容）。
