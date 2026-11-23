# Plan 334: 当前行派发的子代理续跑保持授权——派发时免开关的事实持久化进 record

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: baseline commit `9a3fba8c`（分支
> `execute-331-333` 尖，未合并）. Run:
> ```bash
> git diff --stat 9a3fba8c..HEAD -- pkg/run/subagent.go pkg/run/subagent_test.go pkg/agent/subagent_history.go
> ```
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> **content** mismatch, treat it as a STOP condition (moved line numbers
> alone are not drift).

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（授权语义改动；必须保持凭据检查与既有「开关撤销→续跑拒绝」语义）
- **Depends on**: none（但必须基于分支 `execute-331-333`，见 Git workflow）
- **Category**: bug（introduced by plans 331/333，2026-11-20 `improve branch` 审计复现）
- **Planned at**: commit `9a3fba8c`, 2026-11-20

## Why this matters

plan 331 让「会话当前模型」免 `SubagentAllowed` 开关入列/可派发（用户亲自为
本会话挑的行，与 hand-off 例外同族）。但这个例外只活在**派发 ctx** 的
`SessionModelSelection` 里；子代理续跑经 `subagentRecordPin → usableModelByID`
重读授权时，两条续跑路径都拿不到它：

1. **surface 续跑**（`startSubagentContinuation`）：base ctx 只有
   `context.Background()` + session id（为 hand-off 例外设计——hand-off 经
   state 持久，所以幸免），**必然拒绝**；
2. **tool 续跑**（`continueSubagentRecord`）：用户 `/model` 切换后该行不再
   current，拒绝。

后果：331+333 的组合旗舰场景——333 规定传 `reasoning_effort` 必须带 `model`，
所以「当前模型＋自选档位」派出的子代理**全部**是这种脆弱记录——续跑即失败，
拒绝文案还是给派发者的挑模型指引（`call list_usable_models`），用户看不懂。
331 之前开关关的行根本派发不了，此失败路径纯系该分支引入。

修法：**派发授权事实持久化进 record**。续跑重读的目的是拿活数据
（endpoint/key/caps/version），不是重新审判派发时已成立的授权；record 带上
「派发时作为当前行免开关授权」标记后，重读只对这一条记录免开关重判，
**凭据检查照旧**（没人能调用的行仍然失败），未带标记的记录语义一字不动。

## Current state

### 复现（advisor 已用 overlay 测试实证，仓库树零改动）

overlay 文件是 advisor 会话的 scratch（`/tmp/audit-331-333/`，可能已清理）；
**持久复现＝Step 1 的在树回归测试**（先红后绿，拒绝文案等价）。advisor
当时的命令与输出留档如下：

命令（在仓库根，overlay 文件内容见 Step 1 的测试，等价）：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
export FOREBRAIN_HOME="$(mktemp -d)"
CGO_ENABLED=1 go test -tags fts5 -overlay /tmp/audit-331-333/overlay.json ./pkg/run \
  -run TestAuditCurrentRowDispatchContinuationRepro -count=1
```

失败输出（逐字）：

```
REPRO path1: surface continuation of a current-row dispatch refused: model "zhipuai/glm-5.3" (provider/model) is not one you may pick for subagents; omit model to use the conversation's model.
REPRO path2: tool continuation after a /model switch refused: model "zhipuai/glm-5.3" (provider/model) is not one you may pick for subagents; call list_usable_models, or omit model to use the conversation's model.
--- FAIL: TestAuditCurrentRowDispatchContinuationRepro
```

对照组（selection 未变的 ctx）解析成功。因果链五环均已对照源码核实：

1. `usableModels`/`usableModelByID` 的 current 例外读 ctx selection
   （`pkg/run/subagent.go:4533` 段、`:4608-4613`）；
2. 派发成功 → `prepareSubagent` 写 record（`ModelLive=true`，`:2063-2070`）；
3. 续跑 `subagentRecordPin`（`:2503`）对 `ModelLive` 记录重读
   `usableModelByID`（`:2519`）；
4. path1 的 base ctx 无 selection（`:2974-2985`，注释明说为 hand-off 例外）；
   path2 的 selection 已指向别的行；
5. `!handedOff && !isCurrent && !row.SubagentAllowed` → 拒绝。

`usableModelByID` 全部消费者只有两个（已 grep 普查）：`subagentRecordPin`
（:2519）与 `subagentModelOverrideForRef`（:4737）。

### 关键代码摘录（HEAD `9a3fba8c` 活体）

`pkg/agent/subagent_history.go:48-52` —— 新字段加在 `ModelLive` 之后：

```go
	// ModelLive marks a run pinned to a row read live from the model table
	// ...（注释略）
	ModelLive bool   `json:"model_live,omitempty"`
```

`pkg/run/subagent.go:4390-4410` —— pin 结构（新标记加在 `Caps` 之后）：

```go
type SubagentModelOverride struct {
	ModelID  string
	Provider string
	Model    string
	Effort   string
	Entry *appcfg.AgentLLMProviderConfig
	Version int64
	Caps *llm.ModelOverride
}
```

`pkg/run/subagent.go:4726-4757` —— 派发解析（flag 的计算点）：

```go
func subagentModelOverrideForRef(ctx context.Context, fac Factory, id, effort string) (SubagentModelOverride, error) {
	id = strings.TrimSpace(id)
	effort = strings.TrimSpace(effort)
	if id == "" {
		if effort != "" {
			return SubagentModelOverride{}, fmt.Errorf("reasoning_effort needs a model argument naming the row it applies to; pass model, or omit reasoning_effort.")
		}
		return SubagentModelOverride{}, nil
	}
	usable, err := usableModelByID(ctx, fac, id)
	if err != nil {
		return SubagentModelOverride{}, err
	}
	entry, caps := usable.Entry, usable.Caps
	if effort == "" {
		effort = appcfg.ReasoningEffort(entry.Params)
	} else if err := dispatchEffortRefused(entry, usable, effort); err != nil {
		return SubagentModelOverride{}, err
	}
	return SubagentModelOverride{
		ModelID:  strings.TrimSpace(entry.ID),
		Provider: strings.TrimSpace(entry.Provider),
		Model:    strings.TrimSpace(entry.Model),
		Effort:   effort,
		Entry:    &entry,
		Version:  usable.Row.UpdatedAt,
		Caps:     caps,
	}, nil
}
```

`pkg/run/subagent.go:2017-2022` —— prepareSubagent 的 ModelLive 注释
（本计划要补一句当前行授权持久化的说明）：

```go
	// ModelLive is the model argument's mark, not any live row's: a
	// continuation rereads and re-authorizes that row (subagentRecordPin),
	// because the model argument is a choice the switch governs. A configured
	// type's row is read again at every dispatch by definition, so marking it
	// live would put a configured dispatch through that switch.
	modelLive := false
```

`pkg/run/subagent.go:2063-2070` —— record 写入块：

```go
	if pin.valid() {
		entry.ModelProvider = strings.TrimSpace(pin.Provider)
		entry.Model = strings.TrimSpace(pin.Model)
		entry.ModelID = strings.TrimSpace(pin.ModelID)
		entry.ReasoningEffort = strings.TrimSpace(pin.Effort)
		entry.ModelLive = modelLive
		cctx = WithSubagentModelOverride(cctx, pin)
	}
```

`pkg/run/subagent.go:2503-2541` —— 续跑 pin 重建（本计划主改点）：

```go
func subagentRecordPin(ctx context.Context, fac Factory, record agent.HistoryEntry) (SubagentModelOverride, error) {
	pin := SubagentModelOverride{}
	if strings.TrimSpace(record.Model) == "" {
		return pin, nil
	}
	modelID := strings.TrimSpace(record.ModelID)
	if modelID == "" {
		modelID = ModelIDForAgentModel(fac.appConfig(), fac.AgentName, record.ModelProvider, record.Model)
	}
	pin = SubagentModelOverride{
		ModelID:  modelID,
		Provider: record.ModelProvider, Model: record.Model, Effort: record.ReasoningEffort,
	}
	if !record.ModelLive || modelID == "" {
		return pin, nil
	}
	reread, err := usableModelByID(ctx, fac, modelID)
	if err != nil {
		return SubagentModelOverride{}, err
	}
	// ...（effort 保留段与返回结构略，见 live code）
}
```

`pkg/run/subagent.go:4608-4613` —— usableModelByID 的 current 判定
（**不改它**，只改喂给它的 ctx）：

```go
	isCurrent := false
	if sel, ok := SessionModelSelectionFromContext(ctx); ok {
		isCurrent = strings.TrimSpace(row.ID) != "" && strings.TrimSpace(sel.ModelID) == strings.TrimSpace(row.ID)
	}
	if !handedOff && !isCurrent && !row.SubagentAllowed {
```

### 仓库约定（照抄）

- 注释是完整句、讲「为什么」，涉及 owner 裁决带日期（例：`subagent.go:4533`
  段「(owner 2026-11-10)」）。新注释同风格。
- 测试范式：`pkg/run/subagent_test.go:9281`
  `TestSubagentPinKeepsTheRowsCapsThroughExecAndContinuation` ——
  `newModelArgumentFixture(t, subagentsEnabledConfig())` ＋
  `fix.insertModelRow(t, state.LLMModel{…})`（返回带 ID/UpdatedAt 的存储副本）＋
  `dispatchSubagent(WithSubagentModelOverride(ctx, pin), fix.fac, subagentDispatch{run: func(_ context.Context, _ Factory, prep preparedSubagent) (string, error){…}})`
  （run 闭包里可读 `prep.entry`）＋ 直接调 `subagentRecordPin` 做续跑腿。
  `fix.updateModelRow(t, stored, func(r *state.LLMModel){…})` 改行（自动等
  版本时钟前进）。`dispatchTestCtx()` = session-1 ctx（`:86`）。
- `HistoryEntry` 是 JSON 持久化（agent 子代理账本），加 `omitempty` bool
  字段是 additive：旧记录读出 false＝未授权，语义与今天一致。**无 SQL
  schema 改动、无迁移**。

## Commands you will need

每个新 shell 先：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
export FOREBRAIN_HOME="$(mktemp -d)"
```

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 聚焦测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestCurrentRowDispatchKeepsAuthorizationThroughContinuation -count=1 -v` | 先红（Step 2）后绿（Step 4） |
| 包级 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/agent -count=1` | 两包 ok |
| 包图 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | ok（无新 import，扇出不变） |
| Lint | `gofmt -l pkg/run pkg/agent` | 零输出 |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run ./pkg/agent` | exit 0 |

## Scope

**In scope**（只许改这些）:
- `pkg/run/subagent.go`
- `pkg/run/subagent_test.go`
- `pkg/agent/subagent_history.go`

**Out of scope**（看似相关，禁止触碰）:
- `usableModelByID` / `usableModels` 的判定逻辑本身——例外语义是 331 的
  owner 裁决，本计划只持久化事实，不改判定。
- `startSubagentContinuation` / `continueSubagentRecord` 的 ctx 构造——
  修在 `subagentRecordPin` 一处，两条路径同时受益；往 base ctx 塞 selection
  会泄漏进子代理自己的请求 ctx，影响其模型解析（已评估，否决）。
- `dispatchEffortRefused` / 档位校验——plan 335 的范围。
- gateway `subagentLevelRefused`、hand-off 审批链、`state.PlanHandOff`。
- 任何 frontend / docs / 其它 pkg。

## Git workflow

- **基线分支＝`execute-331-333`（尖 `9a3fba8c`），不是 dev**：被修的代码
  只存在于该未合并分支（owner 留它复审）。worktree 建在
  `.worktrees/334-current-row-continuation`，分支
  `334-current-row-continuation` 从 `9a3fba8c` 起步。这是对「worktree 基于
  dev」惯例的有据偏离，合并由 owner 决定（整条分支栈一起审）。
- 若开工时该分支已合并进 dev：STOP 报告，按 dev 新尖重定位锚点后再继续。
- 每步一个 commit，消息风格照分支现有（`git log --oneline -5` 例：
  `fix(run): a subagent continuation keeps its dispatched effort level`）。
- 不 push、不合并、不动主树 checkout。

## Steps

### Step 1 — 先写回归测试（红）

在 `pkg/run/subagent_test.go` 的
`TestSubagentRecordPinKeepsTheDispatchedEffort` 之后加：

```go
// A dispatch admitted as the conversation's current row (switch off) keeps
// that authorization for its whole life: the continuation rereads the row
// for live data, not to re-judge a choice the user already made (plan 334).
func TestCurrentRowDispatchKeepsAuthorizationThroughContinuation(t *testing.T) {
	fix := newModelArgumentFixture(t, subagentsEnabledConfig())
	row := fix.insertModelRow(t, state.LLMModel{
		Provider: "zhipuai", Model: "glm-5.3", BaseURL: "https://cur.test/v1",
		APIPath: "/chat/completions", APIKey: "key-cur", SubagentAllowed: false,
	})
	ctx := dispatchTestCtx()
	sel := WithSessionModelSelection(ctx, PrimaryModelState{ModelID: row.ID, Provider: "zhipuai", Model: "glm-5.3"})

	pin, err := subagentModelOverrideForRef(sel, fix.fac, row.ID, "")
	if err != nil {
		t.Fatalf("dispatch on the current row: %v", err)
	}
	if !pin.AuthorizedAsCurrent {
		t.Fatal("a switch-off row admitted as the conversation's current must carry the authorization flag")
	}

	// The dispatch writes the flag into the record.
	_, _, dispatchErr := dispatchSubagent(WithSubagentModelOverride(sel, pin), fix.fac, subagentDispatch{
		run: func(_ context.Context, _ Factory, prep preparedSubagent) (string, error) {
			if !prep.entry.ModelAuthorizedAsCurrent {
				t.Error("prepareSubagent dropped the current-row authorization from the record")
			}
			return "done", nil
		},
	})
	if dispatchErr != nil {
		t.Fatalf("dispatchSubagent: %v", dispatchErr)
	}

	record := agent.HistoryEntry{
		SessionID:                "session-1",
		ModelProvider:            pin.Provider,
		Model:                    pin.Model,
		ModelID:                  pin.ModelID,
		ModelLive:                true,
		ReasoningEffort:          pin.Effort,
		ModelAuthorizedAsCurrent: true,
	}
	// Path 1: startSubagentContinuation's base — Background + session id only.
	base := llm.WithAgentSessionID(context.Background(), "session-1")
	if _, err := subagentRecordPin(base, fix.fac, record); err != nil {
		t.Fatalf("surface continuation of a current-row dispatch: %v", err)
	}
	// Path 2: the conversation moved to another model; the authorization
	// persists for the subagent's life, not the selection's.
	other := fix.insertModelRow(t, state.LLMModel{
		Provider: "deepseek", Model: "deepseek-v4-flash", BaseURL: "https://other.test/v1",
		APIPath: "/chat/completions", APIKey: "key-other", SubagentAllowed: true,
	})
	switched := WithSessionModelSelection(ctx, PrimaryModelState{ModelID: other.ID, Provider: "deepseek", Model: "deepseek-v4-flash"})
	if _, err := subagentRecordPin(switched, fix.fac, record); err != nil {
		t.Fatalf("tool continuation after a model switch: %v", err)
	}

	// A record without the flag keeps today's re-authorization: a switch-off
	// row that is nobody's current choice still refuses.
	plain := record
	plain.ModelAuthorizedAsCurrent = false
	if _, err := subagentRecordPin(base, fix.fac, plain); err == nil || !strings.Contains(err.Error(), "not one you may pick") {
		t.Fatalf("unflagged record must keep the switch refusal: %v", err)
	}

	// The waiver never outlives the credentials: a keyless row still fails.
	fix.updateModelRow(t, row, func(r *state.LLMModel) { r.APIKey = "" })
	if _, err := subagentRecordPin(base, fix.fac, record); err == nil || !strings.Contains(err.Error(), "has no credentials") {
		t.Fatalf("credential revocation must still refuse: %v", err)
	}
}
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestCurrentRowDispatchKeepsAuthorizationThroughContinuation -count=1` →
编译失败（`pin.AuthorizedAsCurrent` / `prep.entry.ModelAuthorizedAsCurrent`
不存在）——这是预期红。若编译以**其它**原因失败，STOP。

### Step 2 — 加两个字段（编译转绿、测试仍红）

1. `pkg/agent/subagent_history.go`：`ModelLive` 字段（:52）之后加：

```go
	// ModelAuthorizedAsCurrent marks a live pin the dispatch admitted as the
	// conversation's own current row with the switch off (plan 331's
	// exception): the user's choice for this conversation, authorized once at
	// dispatch. A continuation honors it for the record's whole life instead
	// of re-judging the switch against today's selection (plan 334).
	ModelAuthorizedAsCurrent bool `json:"model_authorized_as_current,omitempty"`
```

2. `pkg/run/subagent.go` `SubagentModelOverride`（:4390）`Caps` 字段后加：

```go
	// AuthorizedAsCurrent says the resolution admitted a switch-off row as
	// the conversation's current model (plan 331): the one exception that
	// lives on the dispatch ctx alone, so the dispatch records it for the
	// continuation (plan 334).
	AuthorizedAsCurrent bool
```

**Verify**: 同 Step 1 命令 → 编译通过、测试 FAIL，失败信息含
`must carry the authorization flag`（flag 尚未被赋值）。

### Step 3 — 接通 flag：派发计算 → record 写入 → 续跑消费

1. `subagentModelOverrideForRef`（:4726）：`usable, err := usableModelByID…`
   成功后计算（能解析＋非 hand-off＋开关关 ⇒ 唯一入列途径就是 current 例外）：

```go
	authorizedAsCurrent := !usable.HandedOff && !usable.Row.SubagentAllowed
```

返回结构加 `AuthorizedAsCurrent: authorizedAsCurrent,`。

2. `prepareSubagent` record 写入块（:2063-2070）：`entry.ModelLive = modelLive`
   之后加 `entry.ModelAuthorizedAsCurrent = pin.AuthorizedAsCurrent`。
   同时把 :2017-2022 的 ModelLive 注释补一句（保持原意、追加）：

```go
	// …because the model argument is a choice the switch governs. The one
	// waiver persists: a row admitted as the conversation's current keeps
	// that authorization in the record (ModelAuthorizedAsCurrent), so a
	// continuation does not re-judge it against a later selection (plan 334).
```

3. `subagentRecordPin`（:2519）：把 `reread, err := usableModelByID(ctx, fac, modelID)`
   改为：

```go
	// The dispatch's current-row authorization persists for this record's
	// life: assert it for the re-read only, by naming the pinned row as the
	// selection this check sees. The continuation's request context is built
	// from ctx, never from rereadCtx, so nothing else reads this assertion.
	// Credentials are still checked — the waiver is the switch alone.
	rereadCtx := ctx
	if record.ModelAuthorizedAsCurrent {
		rereadCtx = WithSessionModelSelection(ctx, PrimaryModelState{ModelID: modelID})
	}
	reread, err := usableModelByID(rereadCtx, fac, modelID)
```

**Verify**: 同 Step 1 命令 → PASS（全部五腿：flag 计算、record 写入、
path1、path2、两控制腿）。

### Step 4 — 门禁

**Verify**（逐条）:
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/agent -count=1` → 两包 ok
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → ok
- `gofmt -l pkg/run pkg/agent` → 零输出
- `CGO_ENABLED=1 go vet -tags fts5 ./pkg/run ./pkg/agent` → exit 0
- `git status --short` → 只有三个 in-scope 文件

## Test plan

- 新测试即 Step 1 的 `TestCurrentRowDispatchKeepsAuthorizationThroughContinuation`
  （五腿：flag 计算、dispatch 写 record、surface 续跑、切换后续跑、
  无 flag 控制腿＋凭据撤销腿）。结构照
  `TestSubagentPinKeepsTheRowsCapsThroughExecAndContinuation`（:9281）。
- 既有测试不得改动断言来迁就实现；`TestUsableModelsIncludesTheCurrentRowWithoutTheSwitch`
  等 331 测试必须原样通过（判定逻辑未动）。
- 回归集：`./pkg/run ./pkg/agent ./pkg/architecture` 全包。

## Done criteria

ALL must hold:

- [ ] Step 1 测试先红（flag 缺失）后绿，五腿全过
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/agent ./pkg/architecture -count=1` 三包 ok
- [ ] `gofmt -l pkg/run pkg/agent` 零输出；`go vet` 两包 exit 0
- [ ] `grep -n 'ModelAuthorizedAsCurrent' pkg/agent/subagent_history.go pkg/run/subagent.go` ≥4 处（字段、写入、消费、注释）
- [ ] `git diff --name-only 9a3fba8c..HEAD`（worktree 分支）只含三个 in-scope 文件
- [ ] `plans/README.md` 状态行更新（除非评审方维护索引）

## STOP conditions

Stop and report back (do not improvise) if:

- Current state 摘录与活体代码内容不符（漂移）。
- 开工时 `execute-331-333` 已不在（被合并/删除）——基线变了，重定位后再开工。
- Step 3 后控制腿（无 flag 拒绝 / 凭据撤销拒绝）转不绿——说明 waiver 泄漏
  进了不该免的判定，**不得**放宽控制腿断言。
- 发现需要改 `usableModelByID` 判定本身或续跑 ctx 构造才能修好——超出本计划
  设计，报告评审方。
- 同一验证失败两次且无明确诊断。

## Maintenance notes

- 评审重点：waiver 只作用于 `subagentRecordPin` 内那次重读的 ctx
  （`rereadCtx`），绝不进 `hostCtx`/请求 ctx；凭据检查与 hand-off 语义原样。
- 旧 record（本修复前、分支测试期产生的当前行派发记录）无 flag，续跑仍会被拒——
  分支未发布，无迁移负担；如 owner 环境有此类残留记录，重派一次即可。
- 未来若给 `subagent_continue` 加档位参数（plan 336），其行解析会复用
  `subagentRecordPin`/`usableModelByID`，本 flag 自动生效。
- 若将来新增第三种免开关授权家族，必须同样持久化进 record——ctx-only 的
  授权在续跑路径天然丢失，这是本 bug 的根因模式。
- 考虑过并否决的替代：给 `usableModelByID` 加 waiver 参数（15 处测试调用点
  连锁改，噪音大）；续跑 base ctx 注入派发时 selection（只救 path1，且泄漏
  风险）；无条件免开关重判（改掉既有「开关撤销→拒绝」语义，超范围）。