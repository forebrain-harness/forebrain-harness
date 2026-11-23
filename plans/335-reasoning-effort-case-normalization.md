# Plan 335: reasoning_effort 派发参数校验前归一化大小写

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: baseline commit `9a3fba8c`（分支
> `execute-331-333` 尖；若 plan 334 已先落地，基线为其分支尖，漂移检查对
> 334 的改动视为预期内前序——本计划摘录的 `subagentModelOverrideForRef`
> 头部两行 TrimSpace 在 334 前后不变）. Run:
> ```bash
> git diff --stat 9a3fba8c..HEAD -- pkg/run/subagent.go pkg/run/subagent_test.go
> ```
> On a **content** mismatch against the excerpts below, STOP (moved line
> numbers alone are not drift).

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW（一行归一化＋测试腿；校验三语义不变）
- **Depends on**: plans/334-current-row-dispatch-continuation-authorization.md（同改 `pkg/run/subagent.go`，串行；334 先行）
- **Category**: bug（introduced by plan 333，2026-11-20 `improve branch` 审计复现）
- **Planned at**: commit `9a3fba8c`, 2026-11-20

## Why this matters

`dispatchEffortRefused` 的成员检查 `slices.Contains(levels, effort)` 里，
`levels` 经 `orderedEffortLevels` 已 lowercase，而 `effort` 只 TrimSpace。
LLM 传 `"High"`：协议检查通过（内部 lowercase），成员检查拒绝——
`does not offer thinking level "High"; it runs at: low, medium, high`。
而档位列表未知的行（成员检查跳过）却接受 `"High"`；下游
`appcfg.WithReasoningEffort`（`pkg/config/agent_llm.go:52`）写入 params 时
本就 `strings.ToLower`。即：**唯一大小写敏感的关卡是成员检查**，行为不对称，
且与下游归一化矛盾。

语义继承自 gateway `subagentLevelRefused`（`pkg/gateway/subagents_api.go:976`，
plan 333 要求同语义）——但 gateway 校验的是 UI picker 的恒小写值，派发校验
的是 LLM 自由文本。roster 虽列小写档位，LLM 仍可能.capitalize；fanout 一个
任务大小写错＝整单失败。owner 对工具参数的一贯裁决是「提高首次调用成功率」
（2026-11-20 user_interaction 裁决同族）。修法：校验前归一化，一行。

## Current state

### 复现（advisor 已用 overlay 测试实证）

overlay 文件是 advisor 会话的 scratch（`/tmp/audit-331-333/`，可能已清理）；
**持久复现＝Step 1 的在树红测试**，失败文案与下面逐字等价。advisor 留档：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
export FOREBRAIN_HOME="$(mktemp -d)"
CGO_ENABLED=1 go test -tags fts5 -overlay /tmp/audit-331-333/overlay.json ./pkg/run \
  -run TestAuditEffortCaseRepro -count=1
```

失败输出（逐字）：

```
REPRO: capitalized High refused on a known-level row: model "openai/gpt-5" (provider/model) does not offer thinking level "High"; it runs at: low, medium, high. Omit reasoning_effort to run at the model's default thinking level.
REPRO asymmetry: unknown-level row accepts High, known-level row refuses it
--- FAIL: TestAuditEffortCaseRepro
```

### 关键代码摘录（HEAD `9a3fba8c` 活体）

`pkg/run/subagent.go:4726-4731` —— 归一化加在这里（入口一次，pin.Effort
随之规范）：

```go
func subagentModelOverrideForRef(ctx context.Context, fac Factory, id, effort string) (SubagentModelOverride, error) {
	id = strings.TrimSpace(id)
	effort = strings.TrimSpace(effort)
	if id == "" {
```

`pkg/run/subagent.go:4763-4776` —— 成员检查（**不改它**）：

```go
func dispatchEffortRefused(entry appcfg.AgentLLMProviderConfig, usable usableModel, effort string) error {
	display := llm.FormatProviderModel(entry.Provider, entry.Model)
	if len(sendableEffortLevels(entry.Provider, entry.Model, entry.APIPath, []string{effort})) == 0 {
		return fmt.Errorf("model %q (provider/model) cannot send thinking level %q on its protocol; omit reasoning_effort to run at the model's default thinking level.", display, effort)
	}
	if raw := usableModelRawLevels(usable); len(raw) > 0 {
		levels := sendableEffortLevels(entry.Provider, entry.Model, entry.APIPath, raw)
		if !slices.Contains(levels, effort) {
```

`pkg/run/subagent_test.go` `TestSubagentReasoningEffortValidation` 现有腿
（新腿照此形状）：

```go
	if pin, err := subagentModelOverrideForRef(ctx, fix.fac, known.ID, " high "); err != nil || pin.Effort != "high" {
		t.Fatalf("trimmed member level = (%q, %v), want high", pin.Effort, err)
	}
```

fixture：`known` 行 openai `/chat/completions`、`CapEfforts: []string{"low","medium","high"}`、
params 默认 low；`unknown` 行无 CapEfforts（目录不识 `gpt-effort-unknown`）。

### 仓库约定

- 校验三语义（plan 333 钉死，不得违反）：**空＝跟随行默认，永不校验**；
  **协议检查恒做**；**成员检查只在列表已知且非空时做**。归一化不触碰三条。
- 下游归一化先例：`appcfg.WithReasoningEffort`
  （`pkg/config/agent_llm.go:52`）`effort = strings.ToLower(strings.TrimSpace(effort))`。
- 测试风格：表驱动/逐腿 `t.Fatalf` 带 got/want，照
  `TestSubagentReasoningEffortValidation`。

## Commands you will need

每个新 shell 先：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
export FOREBRAIN_HOME="$(mktemp -d)"
```

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 聚焦 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestSubagentReasoningEffortValidation -count=1 -v` | 先红后绿 |
| 包级 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | ok |
| Lint | `gofmt -l pkg/run` | 零输出 |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run` | exit 0 |

## Scope

**In scope**:
- `pkg/run/subagent.go`（仅 `subagentModelOverrideForRef` 头部一行）
- `pkg/run/subagent_test.go`（仅 `TestSubagentReasoningEffortValidation` 加腿）

**Out of scope**:
- gateway `subagentLevelRefused`——UI picker 值恒小写，无此问题；改它是
  另一个 surface 的语义变更，不在本计划。
- `dispatchEffortRefused` / `orderedEffortLevels` / `sendableEffortLevels`
  本身——镜像与平价测试（`TestSendableEffortLevelsMatchThePickerSetup`）不动。
- hand-off 提示词链（picker 值恒小写，`HandOffPlanExecutionPrompt` 原样传递）。
- plan 334 的 flag 逻辑（若已落地，保持原样）。

## Git workflow

-  worktree `.worktrees/335-effort-case`，分支 `335-effort-case`，基于
  plan 334 分支尖（334 未落地则基于 `execute-331-333` 尖 `9a3fba8c`，并在
  334 落地后 rebase）。理由同 334：被修代码只在未合并分支栈上。
- commit 消息例：`fix(run): normalize the reasoning_effort argument before validation`。
- 不 push、不合并。

## Steps

### Step 1 — 测试先红

`TestSubagentReasoningEffortValidation` 的 `" high "` 腿之后加两腿：

```go
	if pin, err := subagentModelOverrideForRef(ctx, fix.fac, known.ID, "High"); err != nil || pin.Effort != "high" {
		t.Fatalf("capitalized member level = (%q, %v), want the normalized high", pin.Effort, err)
	}
	if pin, err := subagentModelOverrideForRef(ctx, fix.fac, known.ID, " HIGH "); err != nil || pin.Effort != "high" {
		t.Fatalf("mixed-case member level = (%q, %v), want the normalized high", pin.Effort, err)
	}
```

**Verify**: 聚焦命令 → FAIL，信息含
`capitalized member level = ("", …does not offer thinking level "High"…)`。

### Step 2 — 一行归一化

`subagentModelOverrideForRef` 头部：

```go
	effort = strings.TrimSpace(effort)
```

改为：

```go
	// The argument is free text from the model, not a picker value: judge it
	// in the canonical case the level lists and the wire params already use
	// (WithReasoningEffort lowercases), so "High" is the level "high".
	effort = strings.ToLower(strings.TrimSpace(effort))
```

**Verify**: 聚焦命令 → PASS（含新两腿与全部既有腿）。

### Step 3 — 门禁

**Verify**:
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → ok
- `gofmt -l pkg/run` → 零输出
- `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run` → exit 0
- `git status --short` → 只有两个 in-scope 文件

## Test plan

- 新腿即 Step 1 两条（"High"、" HIGH " → pin.Effort=="high"），在既有
  `TestSubagentReasoningEffortValidation` 内，fixture 复用。
- 既有腿（空/trim/非成员/协议/未知行/effort-无-model）必须原样通过——
  归一化不改变任何一条的判定结果。
- 三个派发工具的 plumbing 已由 `TestSubagentDispatchToolsCarryReasoningEffort`
  钉住（共用同一解析器），无需新增工具级测试。

## Done criteria

- [ ] 新两腿先红后绿
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` ok
- [ ] `gofmt -l pkg/run` 零输出；vet exit 0
- [ ] `git diff` 生产代码改动仅一行（+注释）
- [ ] `plans/README.md` 状态行更新（除非评审方维护索引）

## STOP conditions

- 摘录与活体代码内容不符。
- 既有校验腿有任何一条转红——归一化不该改变判定，转红说明改错了位置。
- 发现需要改 `dispatchEffortRefused` 或 gateway 才能修好——报告，不扩范围。

## Maintenance notes

- 评审重点：归一化在入口一次完成，`pin.Effort` 与拒绝文案里的 effort 都是
  规范小写；`dispatchEffortRefused` 保持纯函数。
- gateway 侧同形大小写敏感（`subagentLevelRefused`）是 pre-existing，UI 值
  恒小写故无实害；若将来 gateway 也接受自由文本档位，应做同样归一化。
- 旧 record 可能带混合大小写 ReasoningEffort（未知档位行曾接受）——续跑
  原样使用、下游 WithReasoningEffort 归一化，无需清洗。