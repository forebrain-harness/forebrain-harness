# Plan 336: subagent_continue 支持 model＋reasoning_effort——续跑可重钉模型与档位

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: baseline commit `7f6ead06`（分支
> `335-effort-case` 尖＝334→335 叠层；334/335 已落地未合并）. Run:
> ```bash
> git diff --stat 7f6ead06..HEAD -- pkg/run/subagent.go pkg/run/subagent_test.go
> ```
> On a **content** mismatch against the excerpts below, STOP (moved line
> numbers alone are not drift).

## Status

- **Priority**: P3
- **Effort**: M
- **Risk**: LOW-MED（续跑可重钉 record 的模型身份；授权/凭据/校验语义复用派发，不新造）
- **Depends on**: plans/334（当前行 record 续跑 waiver，DONE @`1e780d6f`）、plans/335（大小写归一化，DONE @`7f6ead06`）；owner 裁决已齐（见下）
- **Category**: direction（2026-11-20 `improve branch` 审计 D1，owner 选定；设计稿已定稿转实施）
- **Planned at**: commit `7f6ead06`, 2026-11-20（设计定稿同日）

## Why this matters

plan 333 给三个**派发**工具（subagent_run/send/fanout）加了 `model`＋
`reasoning_effort`，但**续跑**工具 `subagent_continue` 没有——续跑只能继承
派发时冻结的模型与档位（`subagentRecordPin` 读 `record.ReasoningEffort`）。
表面不对称：主代理无法在续跑时把子代理调到另一档思考强度或另一个模型
（例如执行阶段用 high、收尾复核用 low）。本计划给 `subagent_continue` 补
`model`＋`reasoning_effort` 两参数，**复用派发的解析/校验/授权机制**，不发明
新通道。owner 2026-11-20 裁决：续跑可同时换 model（Q2），校验以首次调用
成功率优先、只在行已知时判（Q1）。

## 设计裁决（owner 2026-11-20，user_interaction 已回）

- **Q1（非 live record 传档位）**：owner 原话「采用首次调用成功率最高的方案，
  减少在 harness 层的无意义的影响工具调用成功率的校验」。落地＝**不一刀切
  拒绝**：行可解析时按派发语义校验（协议恒查、成员仅已知非空列表时查）；
  行不可解析时**不判、verbatim 生效**（333「未知档位行不判」第三语义延伸到
  「未知行」）。唯一结构性拒绝＝record 无任何 model 且未传 model 时传 effort
  （同派发的 effort-无-model 拒绝，是 identity 约束不是任意上限）。
- **Q2（是否同时换 model）**：**同时加 model**。续跑命名 model＝按派发同款
  解析重钉 record（授权/凭据/协议/成员/归一/current-row flag 全复用）。
- **Q3（空档位）**：空＝**保持 record 现有档位**（不是重置行默认）。model 与
  effort 皆空＝record 一字不动。
- **Q4（effort 描述文本）**：采用草案
  `Optional thinking level for this continuation, one of the levels the subagent's model supports. Omit to keep the level the subagent already runs at.`
- **advisor 据裁决细化（评审时可见）**：
  - model 命名＋effort 空＝**新行默认档位**（fresh pin 的派发语义；旧档位
    可能不属于新行）。model 空＋effort 空＝不变（Q3）。
  - model 描述（固定文本，advisor 草案，与 SubagentInput.Model 同族）：
    `Optional id of a configured model to run this continuation on. Omit to keep the model the subagent already runs on.`
  - 仅 effort 时只改 `record.ReasoningEffort`，不动 record 的模型身份；
    model 命名时重钉全部模型字段并置 `ModelLive=true`。

## Current state（已核实事实，基线 `7f6ead06`）

### 1. 续跑输入结构无模型/档位字段（`pkg/run/subagent.go:127-131`）

```go
type SubagentContinueInput struct {
	TaskID  string `json:"task_id,omitempty" jsonschema:"description=Async subagent task id returned by subagent_send or subagent_run."`
	RunID   string `json:"run_id,omitempty" jsonschema:"description=Child run id when available."`
	Message string `json:"message" jsonschema:"description=Self-contained follow-up instructions for the selected subagent."`
}
```

handler 直传 `*SubagentContinueInput`（`addSubagentLifecycleTool(a, fac, cat,
"subagent_continue", …, func(ctx, in *SubagentContinueInput)…)` →
`continueSubagentExecution(ctx, fac, in)`），加字段经反射自动入 schema。

### 2. 插入点：continueSubagentExecution 尾部（`pkg/run/subagent.go:3215-3226`）

```go
	// A deleted worktree is refused here, by name: the continuation belongs in
	// the tree the record names, and running it in the conversation's directory
	// would leave its edits where nobody looks for them.
	if err := subagentWorktreeMissing(record); err != nil {
		return agent.HistoryEntry{}, err
	}
	// The tool path passes no parts (its message is text alone) and no
	// approval hook: continueSubagentRecord takes the hook from the same
	// context it always did, derived from the record.
	final, _, err := continueSubagentRecord(ctx, fac, record, msg, nil, nil)
	return final, err
}
```

重钉块插在 `subagentWorktreeMissing` 检查之后、`continueSubagentRecord` 之前。

### 3. 派发解析镜像（`pkg/run/subagent.go:427`，subagent_run handler）

```go
			if in != nil {
				pin, err := subagentModelOverrideForRef(ctx, fac, in.Model, in.ReasoningEffort)
				if err != nil {
					return "", err
				}
				ctx = WithSubagentModelOverride(ctx, pin)
			}
```

`subagentModelOverrideForRef`（:4746）已含：TrimSpace＋ToLower（335）、
`usableModelByID` 授权（switch/current/hand-off＋凭据）、`dispatchEffortRefused`
协议＋成员校验、空 effort 落行默认、`AuthorizedAsCurrent` flag（334）。
**model 命名时续跑直接复用它，一字不改。**

### 4. 续跑 pin 的 effort 保留段（`pkg/run/subagent.go:2540-2543`）

```go
	effort := strings.TrimSpace(record.ReasoningEffort)
	if effort == "" {
		effort = appcfg.ReasoningEffort(entry.Params)
	}
```

即：**改 record.ReasoningEffort 就能改续跑档位**——机制现成。非 live 记录
（`!record.ModelLive || modelID == ""`，:2516）`subagentRecordPin` 直接返回
`Effort: record.ReasoningEffort`，不重读行。

### 5. 持久化路径（已核实，无需额外步骤）

`continueSubagentRecord`（:3247）内 `lifecycle := record`、结束时 `final = record`
（均值拷贝）→ `finishSubagentExecution(fac, handle, final)`（:2107-2114）：

```go
func finishSubagentExecution(fac Factory, handle *agent.Handle, entry agent.HistoryEntry) {
	entry = normalizeSubagentFinal(entry)
	if handle != nil {
		handle.Finish(entry)
	}
	if root := fac.subagentScopeRoot(); root != "" {
		_ = agent.AppendHistory(root, entry)
	}
}
```

**调用方在 `continueSubagentRecord` 前改 record 副本，新模型/档位即随 final
落库**（`agent.AppendHistory`）。effort 消费经 `subagentRecordContext` →
`WithSubagentModelOverride(hostCtx, pin)`（:2573）。

### 6. 334 waiver 模式（`subagentRecordPin`，:2523-2531，仅重读 ctx）

```go
	rereadCtx := ctx
	if record.ModelAuthorizedAsCurrent {
		rereadCtx = WithSessionModelSelection(ctx, PrimaryModelState{ModelID: modelID})
	}
	reread, err := usableModelByID(rereadCtx, fac, modelID)
```

### 7. 描述钉死测试范式（`pkg/run/subagent_test.go:9123-9134`）

```go
func TestSubagentReasoningEffortArgumentDescriptionIsFixedText(t *testing.T) {
	const want = "Optional thinking level for this subagent, one of the levels the chosen model supports. Omit to run at the model's default thinking level."
	for _, typ := range []reflect.Type{reflect.TypeOf(SubagentInput{}), reflect.TypeOf(SubagentSendInput{}), reflect.TypeOf(SubagentTask{})} {
		field, ok := typ.FieldByName("ReasoningEffort")
		...
		if tag, _ := field.Tag.Lookup("jsonschema_description"); tag != want { ... }
	}
}
```

含逗号必须 `jsonschema_description` tag（`TestJSONSchemaDescriptionTagsSurviveReflection`，
`pkg/llm/schema_test.go:27` 全模块扫）。**续跑描述与派发不同**（keep vs
default），故另立钉死测试，不改既有测试的 want。

### 8. 续跑集成测试范式（`pkg/run/subagent_test.go:1551`
`TestContinueSubagentExecutionAppendsUpdatedHistory`）

seed ledger（`agent.AppendHistory(fac.subagentScopeRoot(), agent.HistoryEntry{…
Continuable:true…})`）＋`fixedSubagentExecutor{output:…}`＋
`continueSubagentExecution(ctx, fac, &SubagentContinueInput{TaskID,Message})`＋
`agent.GetMerged(...)` 读回断言落库。`newModelArgumentFixture`（:8382）提供
模型表＋session-1＋`fac.Owner.SubagentExecutor`（可换成 fixedSubagentExecutor）。

### 仓库约定（照抄）

- 注释完整句、讲「为什么」，owner 裁决带日期。
- 校验三语义（333 钉死）：空＝跟随行默认永不校验；协议恒查；成员仅已知
  非空列表时查。本计划延伸到「行未知不判」，不触碰三条本身。
- `dispatchEffortRefused`/`usableModelByID`/`subagentModelOverrideForRef`/
  `subagentRecordPin` 均**只消费、不改**。

## Commands you will need

每个新 shell 先：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
export FOREBRAIN_HOME="$(mktemp -d)"
```

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 聚焦 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestSubagentContinue' -count=1 -v` | 先红后绿 |
| 包级 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | ok |
| 包图 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | ok（无新 import） |
| Lint | `gofmt -l pkg/run` | 零输出 |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run` | exit 0 |

## Scope

**In scope**（只许改这些）:
- `pkg/run/subagent.go`
- `pkg/run/subagent_test.go`

**Out of scope**（禁止触碰）:
- 派发三工具（subagent_run/send/fanout）的 handler 与 `SubagentInput`/
  `SubagentSendInput`/`SubagentTask`。
- `subagentModelOverrideForRef`/`dispatchEffortRefused`/`usableModelByID`/
  `subagentRecordPin`/`usableModels` 本身——只消费。
- gateway、hand-off 链、frontend、surface 续跑路径
  （`startSubagentContinuation` 取 Input 不取 SubagentContinueInput，不受影响）、
  `ContinuePlanReviewSubagent`（共享 continueSubagentRecord 但传自己的 record）。
- 既有描述钉死测试的 want 文本。

## Git workflow

- worktree `.worktrees/336-continue-effort`，分支 `336-continue-effort`，
  **基于 335 尖 `7f6ead06`**（334→335→336 叠层，被修代码只在未合并分支栈上）。
  新 worktree 起步先 `git reset --hard 7f6ead06` 并核对 HEAD。
- 每步一个 commit，消息照分支风格（例：
  `feat(run): subagent_continue re-pins the model and thinking level`）。
- 不 push、不合并、不动主树 checkout。

## Steps

### Step 1 — 先写回归测试（红）

在 `pkg/run/subagent_test.go` 加新测试（fixture 照
`TestSubagentRecordPinKeepsTheDispatchedEffort` :9346）。核心红测试：

```go
// A continuation may re-pin the model and level it runs on (plan 336): a
// level alone is judged against the record's own row, and the choice persists
// for the record's life. Empty model and level keep what it already runs on.
func TestSubagentContinueRePinsEffort(t *testing.T) {
	fix := newModelArgumentFixture(t, subagentsEnabledConfig())
	row := fix.insertModelRow(t, state.LLMModel{
		Provider: "openai", Model: "gpt-cont", BaseURL: "https://cont.test/v1",
		APIPath: "/chat/completions", APIKey: "key-cont", SubagentAllowed: true,
		ParamsJSON: `{"reasoning":{"effort":"low"}}`,
		CapEfforts: []string{"low", "medium", "high"},
	})
	ctx := dispatchTestCtx()
	record := agent.HistoryEntry{
		ModelProvider: "openai", Model: "gpt-cont",
		ModelID: row.ID, ModelLive: true, ReasoningEffort: "low",
	}

	// A level alone re-pins the record; the continuation pin reads the new one.
	got, err := applySubagentContinueModel(ctx, fix.fac, record, &SubagentContinueInput{ReasoningEffort: "High"})
	if err != nil {
		t.Fatalf("continue effort: %v", err)
	}
	if got.ReasoningEffort != "high" {
		t.Fatalf("continue effort = %q, want the normalized high", got.ReasoningEffort)
	}
	if pin, err := subagentRecordPin(ctx, fix.fac, got); err != nil || pin.Effort != "high" {
		t.Fatalf("continuation pin = (%q, %v), want high", pin.Effort, err)
	}

	// Empty keeps the record's level (owner Q3).
	if got, err := applySubagentContinueModel(ctx, fix.fac, record, &SubagentContinueInput{}); err != nil || got.ReasoningEffort != "low" {
		t.Fatalf("empty continue = (%q, %v), want the kept low", got.ReasoningEffort, err)
	}

	// A non-member level is refused with the offered list.
	if _, err := applySubagentContinueModel(ctx, fix.fac, record, &SubagentContinueInput{ReasoningEffort: "xhigh"}); err == nil ||
		!strings.Contains(err.Error(), "does not offer") || !strings.Contains(err.Error(), "low, medium, high") {
		t.Fatalf("non-member continue level must be refused: %v", err)
	}
}
```

**Verify**: 聚焦命令 → 编译失败（`applySubagentContinueModel` 不存在）＝预期红。
若以其它原因失败，STOP。

### Step 2 — 加两个输入字段

`SubagentContinueInput`（:127）`Message` 之后加：

```go
	// Model is fixed text for the same reason as SubagentInput.Model: it names
	// no model and no tool, so the definition never moves with the table.
	Model string `json:"model,omitempty" jsonschema_description:"Optional id of a configured model to run this continuation on. Omit to keep the model the subagent already runs on."`
	// ReasoningEffort is fixed text; see SubagentInput.ReasoningEffort. The
	// continuation keeps its current level when this is empty (owner 2026-11-20).
	ReasoningEffort string `json:"reasoning_effort,omitempty" jsonschema_description:"Optional thinking level for this continuation, one of the levels the subagent's model supports. Omit to keep the level the subagent already runs at."`
```

另立钉死测试（不改既有派发测试）：

```go
func TestSubagentContinueModelArgumentDescriptionsAreFixedText(t *testing.T) {
	typ := reflect.TypeOf(SubagentContinueInput{})
	for field, want := range map[string]string{
		"Model":           "Optional id of a configured model to run this continuation on. Omit to keep the model the subagent already runs on.",
		"ReasoningEffort": "Optional thinking level for this continuation, one of the levels the subagent's model supports. Omit to keep the level the subagent already runs at.",
	} {
		f, ok := typ.FieldByName(field)
		if !ok {
			t.Fatalf("SubagentContinueInput has no %s field", field)
		}
		if tag, _ := f.Tag.Lookup("jsonschema_description"); tag != want {
			t.Fatalf("SubagentContinueInput.%s description =\n%q\nwant the fixed text", field, tag)
		}
	}
}
```

**Verify**: 聚焦命令 → 编译通过、`TestSubagentContinueRePinsEffort` 仍 FAIL
（helper 未实现）；描述测试 PASS。

### Step 3 — 实现重钉 helper＋接通调用点

1. 在 `continueSubagentExecution` 附近加 helper（只消费现成机制）：

```go
// applySubagentContinueModel re-pins a continuation's record to the model and
// thinking level the caller named (plan 336). A named model is resolved exactly
// as a dispatch resolves it (subagentModelOverrideForRef): authorization,
// protocol and membership checks, the normalized level, the current-row
// authorization flag. A level alone is judged against the record's own row when
// that row resolves, and applied unjudged when it does not — the same "a model
// this process knows no levels for is not judged" rule a dispatch follows
// (owner 2026-11-20: first-call success over harness gatekeeping). Empty model
// and level leave the record untouched: a continuation keeps what it runs on.
func applySubagentContinueModel(ctx context.Context, fac Factory, record agent.HistoryEntry, in *SubagentContinueInput) (agent.HistoryEntry, error) {
	if in == nil {
		return record, nil
	}
	model := strings.TrimSpace(in.Model)
	effort := strings.ToLower(strings.TrimSpace(in.ReasoningEffort))
	if model == "" && effort == "" {
		return record, nil
	}
	if model != "" {
		pin, err := subagentModelOverrideForRef(ctx, fac, model, effort)
		if err != nil {
			return agent.HistoryEntry{}, err
		}
		record.ModelProvider = pin.Provider
		record.Model = pin.Model
		record.ModelID = pin.ModelID
		record.ReasoningEffort = pin.Effort
		record.ModelLive = pin.Entry != nil
		record.ModelAuthorizedAsCurrent = pin.AuthorizedAsCurrent
		return record, nil
	}
	// A level alone: judge it against the record's own row when that row
	// resolves. A record that names no model at all has nothing to attach the
	// level to — the same structural refusal a dispatch gives an effort
	// without a model.
	modelID := strings.TrimSpace(record.ModelID)
	if modelID == "" {
		modelID = ModelIDForAgentModel(fac.appConfig(), fac.AgentName, record.ModelProvider, record.Model)
	}
	if modelID == "" && strings.TrimSpace(record.Model) == "" {
		return agent.HistoryEntry{}, fmt.Errorf("reasoning_effort needs a model argument naming the row it applies to; pass model, or omit reasoning_effort.")
	}
	if modelID != "" {
		// The dispatch's current-row authorization persists for this record's
		// life (plan 334): assert it for this read only, exactly as
		// subagentRecordPin does, so nothing else sees the assertion.
		readCtx := ctx
		if record.ModelAuthorizedAsCurrent {
			readCtx = WithSessionModelSelection(ctx, PrimaryModelState{ModelID: modelID})
		}
		if usable, err := usableModelByID(readCtx, fac, modelID); err == nil {
			if err := dispatchEffortRefused(usable.Entry, usable, effort); err != nil {
				return agent.HistoryEntry{}, err
			}
		}
	}
	record.ReasoningEffort = effort
	return record, nil
}
```

2. `continueSubagentExecution` 尾部（:3221 worktree 检查后）插入：

```go
	// A continuation may re-pin the model and level it runs on (plan 336): the
	// dispatcher's choice for this round, judged the way a fresh dispatch
	// judges it. The record copy carries the choice into continueSubagentRecord,
	// which pins from it and persists it on finish (finishSubagentExecution).
	record, err = applySubagentContinueModel(ctx, fac, record, in)
	if err != nil {
		return agent.HistoryEntry{}, err
	}
```

**Verify**: 聚焦命令 → `TestSubagentContinueRePinsEffort` PASS。

### Step 4 — 补齐测试腿＋门禁

在 `pkg/run/subagent_test.go` 补腿（fixture 同上；逐腿 `t.Fatalf` 带 got/want）：

- **协议拒绝**：anthropic 行（APIPath `/v1/messages`）effort `none` →
  `cannot send thinking level`。
- **未知行不判**：record 指向无 CapEfforts 的行（或 ModelID 空但 Model 非空）
  effort `xhigh` → 生效、不拒（Q1）。
- **无 model 结构性拒绝**：record Model/ModelID 皆空＋effort 非空＋未传 model →
  `reasoning_effort needs a model argument`。
- **model 重钉**：传 model＝另一行 id → record.Model/ModelID 更新、
  ModelLive=true、effort 空时＝新行默认；传 model＋effort → 两者皆新。
- **model 授权拒绝**：传一个 switch-off 且非 current/hand-off 的行 →
  `not one you may pick`。
- **当前行 record（334 flag）effort-only**：record.ModelAuthorizedAsCurrent=true、
  base ctx 无 selection → 经 waiver 可读、换档成功。
- **凭据撤销**：live record 行 APIKey 清空后 effort-only → helper verbatim 生效，
  但 `subagentRecordPin` 重读拒（`has no credentials`）——真实行问题在续跑暴露。
- **集成落库**（照 `TestContinueSubagentExecutionAppendsUpdatedHistory` :1551）：
  `newModelArgumentFixture`＋把 `fix.fac.Owner.SubagentExecutor` 换成
  `&fixedSubagentExecutor{output:"continued"}`＋`agent.AppendHistory(fix.fac.subagentScopeRoot(),
  record{…ModelID:row.ID,ModelLive:true,ReasoningEffort:"low",Continuable:true,
  TaskID,RunID,ParentRunID,SessionID:"session-1",WorkerSessionID:"session-1",AgentType:"fork",RuntimeKind:"fork_subagent"})`
  ＋`continueSubagentExecution(ctx,fix.fac,&SubagentContinueInput{TaskID,Message:"carry on",ReasoningEffort:"high"})`
  ＋`agent.GetMerged(...)` 断言 `got.ReasoningEffort=="high"`。
  若 fixture 的 scope root/executor 接线使集成腿不可行，STOP 报告（勿改 fixture
  本身），退化为断言返回 record＋subagentRecordPin 并在 NOTES 留痕。

**Verify**（逐条）:
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → ok
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → ok
- `gofmt -l pkg/run` → 零输出
- `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run` → exit 0
- `git status --short` → 只有两个 in-scope 文件

## Test plan

- 新测试：`TestSubagentContinueRePinsEffort`（核心三腿）＋
  `TestSubagentContinueModelArgumentDescriptionsAreFixedText`＋Step 4 各腿。
- 既有测试不得改断言迁就实现；派发三工具测试、334/335 测试、
  `TestSubagentRecordPinKeepsTheDispatchedEffort` 原样通过。
- 回归集：`./pkg/run ./pkg/architecture` 全包。

## Done criteria

ALL must hold:

- [ ] Step 1 测试先红（helper 缺失）后绿
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/architecture -count=1` 两包 ok
- [ ] `gofmt -l pkg/run` 零输出；`go vet ./pkg/run` exit 0
- [ ] `grep -n 'applySubagentContinueModel' pkg/run/subagent.go` ≥2 处（定义＋调用）
- [ ] `grep -n 'ReasoningEffort\|Model string' pkg/run/subagent.go` 含 SubagentContinueInput 两新字段
- [ ] `git diff --name-only 7f6ead06..HEAD` 只含两个 in-scope 文件
- [ ] `plans/README.md` 状态行更新（除非评审方维护索引）

## STOP conditions

Stop and report back (do not improvise) if:

- Current state 摘录与活体代码内容不符（漂移）。
- 334/335 不在基线栈上（`7f6ead06` 不可达）——依赖缺失，重定位后再开工。
- 发现需要改 `subagentModelOverrideForRef`/`dispatchEffortRefused`/
  `usableModelByID`/`subagentRecordPin` 本身才能修好——超出本计划设计，报告。
- 集成落库腿因 fixture 接线不可行——报告，勿改 fixture；按 Step 4 退化并留痕。
- 同一验证失败两次且无明确诊断。

## Maintenance notes

- 评审重点：model 命名走派发同款解析（授权/凭据不绕过）；effort-only 的
  waiver 只作用那次 `usableModelByID` 的 readCtx，绝不进请求 ctx；未知行
  verbatim 生效是 owner Q1 裁决（不是漏校验）；空＝保持（Q3）。
- 持久化由 `continueSubagentRecord` 值拷贝＋`finishSubagentExecution`→
  `AppendHistory` 保证，无需额外写库步骤；集成腿钉死它。
- 与 334 交互：当前行 record 续跑换档依赖 334 flag；model 重钉时 flag 由
  `subagentModelOverrideForRef` 重新计算（新行的授权事实）。
- surface 续跑（web/TUI 按钮）与 plan-review 续跑不取 SubagentContinueInput，
  不受影响；若将来它们也要换档，另立计划。
- `listUsableModelsDescription` 是否提一句续跑也可传 model/effort，属模型可见
  文本变更，需 owner 点头（缓存前缀不变式），本期不动。
- 考虑过并否决：一刀切拒绝非 live record（违 owner Q1 首次成功率裁决）；
  续跑换 model 另立计划（owner Q2 选定本期一起做）。