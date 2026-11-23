# Plan 293: 交接兜底路径也要校验凭据，守住「列得出就派得出」

> **Executor instructions**：按步骤执行，每一步都跑完验证命令并确认预期输出再进入下一步。
> 遇到「STOP 条件」里任意一条就停下报告，不要自行发挥。完成后更新 `plans/README.md`
> 里本计划的状态行（除非派发你的评审者说他维护索引）。
>
> **Drift check（开工第一步）**：
> `cd /Users/doudou/workspace/unionj-cloud/forebrain-harness && git status --porcelain -- pkg/run/subagent.go pkg/run/subagent_test.go && git diff d715dafd -- pkg/run/subagent.go pkg/run/subagent_test.go`
> 若两个文件在 `d715dafd` 之后已有改动（尤其 `290` 若已执行），把「当前状态」的摘录与活代码比对；行号可以变，代码形状必须对得上。对不上即 STOP。
> `plans/` 被 `.gitignore` 忽略，不能用 `git status` 干净推断计划文件没动过。

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none（与 `290` 同文件不同位置；若 `290` 同期执行，先做 `290` 再做本计划并重新 drift check）
- **Category**: security / bug
- **Planned at**: commit `d715dafd`, 2026-11-11

## Why this matters

`usableModelByID` 是 `model` 行参数的派发校验口，与 `list_usable_models` 的 `usableModels` 共用同一套准入规则。计划 283 的非功能要求把它写成一条不变量：「`list_usable_models` 与派发判定共用一个查询函数……列得出就派得出」，语义 9 也明确交接例外放行的那一行「**只要它有凭据**，不看开关」。

`usableModels` 对 DB 行（含交接例外行）与 `usableModelByID` 对 DB 行都做了 `credentialed` 判断；唯独 `usableModelByID` 里「库里查不到的旧式配置条目」这条交接兜底分支直接 `return usableModel{Entry: entry, HandedOff: true}, nil`，**没有**凭据校验。结果：一个无凭据、且不在 `fb_llm_models` 表里的旧式配置条目，只要被本会话的交接记录指名，就能通过派发校验；而 `list_usable_models` 只遍历 DB 行，永远不会列出它。派发失败被推迟到 provider 请求阶段，模型拿不到定稿的 "has no credentials; call list_usable_models" 文案，而该文案又指向一个列不出这个 id 的工具。

## Current state

仓库：`/Users/doudou/workspace/unionj-cloud/forebrain-harness`（Go 1.26，CGO，`-tags fts5`）。

- `pkg/run/subagent.go:4351-4386`（缺陷与对照，逐字）：
  ```go
  func usableModelByID(ctx context.Context, fac Factory, id string) (usableModel, error) {
      id = strings.TrimSpace(id)
      if id == "" {
          return usableModel{}, fmt.Errorf("no configured model has id %q; call list_usable_models", id)
      }
      if fac.Owner == nil || fac.Owner.Deps == nil || fac.Owner.Deps.SessionStore == nil {
          return usableModel{}, fmt.Errorf("model store unavailable")
      }
      row, err := (&state.LLMModelStore{DB: fac.Owner.Deps.SessionStore.DB()}).Get(ctx, id)
      if err != nil {
          if errors.Is(err, state.ErrLLMModelNotFound) {
              // A hand-off id recorded against a pre-table configuration entry
              // never had a row: resolve it against the snapshot so the
              // hand-off the user made themselves still runs (语义 9).
              if handOffID := handOffModelID(ctx, fac); handOffID == id {
                  if entry, ok := appcfg.ResolveModelRef(fac.appConfig(), fac.AgentName, id); ok && strings.TrimSpace(entry.Model) != "" {
                      return usableModel{Entry: entry, HandedOff: true}, nil
                  }
              }
              return usableModel{}, fmt.Errorf("no configured model has id %q; call list_usable_models", id)
          }
          return usableModel{}, err
      }
      entry, err := row.ConfigRow()
      if err != nil { return usableModel{}, err }
      credentialed := strings.TrimSpace(row.APIKey) != "" || appcfg.CredentialsBackedProvider(row.Provider)
      handedOff := strings.TrimSpace(row.ID) == handOffModelID(ctx, fac) && handOffModelID(ctx, fac) != ""
      if !handedOff && !row.SubagentAllowed {
          return usableModel{}, fmt.Errorf("model %q (provider/model) is not one you may pick for subagents; call list_usable_models, or omit model to use the conversation's model.", llm.FormatProviderModel(row.Provider, row.Model))
      }
      if !credentialed {
          return usableModel{}, fmt.Errorf("model %q (provider/model) has no credentials; call list_usable_models, or omit model to use the conversation's model.", llm.FormatProviderModel(row.Provider, row.Model))
      }
      return usableModel{Row: row, Entry: appcfg.ProviderFromModelRow(entry), HandedOff: handedOff}, nil
  }
  ```
- `pkg/run/subagent.go:4318-4345`（`usableModels` 的交接例外，**已经做对**）：
  ```go
  credentialed := strings.TrimSpace(row.APIKey) != "" || appcfg.CredentialsBackedProvider(row.Provider)
  if strings.TrimSpace(row.ID) == handOff && handOff != "" {
      // The hand-off exception (语义 8): the user picked this row for
      // this conversation's plan, so the agent may dispatch on it for
      // that purpose without the switch. It still needs credentials —
      // a row nobody can call is not a model to run on.
      if !credentialed { continue }
      out = append(out, usableModel{Row: row, Entry: appcfg.ProviderFromModelRow(entry), HandedOff: true})
      continue
  }
  ```
  注释里 "It still needs credentials" 就是本计划要贯彻到兜底分支的规则。
- `pkg/run/subagent.go:4394-4404`：`handOffModelID(ctx, fac)` 从 `llm.AgentSessionIDFromContext(ctx)` 拿会话 id，读 `state.Get(...).PlanHandOff.ModelID`；无 session id / 无交接记录返回 `""`。
- `pkg/config/agents.go:92-110`：`AgentLLMProviderConfig` 有 `APIKey string`（`:95`）、`Provider`、`Model`、`BaseURL`；`ID` 是 runtime-only。
- `pkg/config/agent_llm.go:439-445`：`CredentialsBackedProvider(provider)` 只对 `chatgpt` / `opencode` / `opencode-go` 为真（订阅型凭据不在 api_key 上）。
- `pkg/config/llm_models.go:115-127`：`ResolveModelRef(root, agentName, ref)` 支持行 id 与旧式 `provider/model` 标签；后者按 provider+model 匹配 legacy 列表。
- `appcfg.ConfiguredLLMs`（`pkg/config/llm_models.go:90-113`）先投影 `root.LLMModels`，再投影各 agent 定义的 `LLMProviders` legacy 列表。

测试样板（**不要新建 `_test.go` 文件**）：
- `pkg/run/subagent_test.go:8347-8395`：`newModelArgumentFixture(t, cfg)`（真实 SQLite + `state.LLMModelStore`）、`dispatchTestCtx()` = `llm.WithAgentSessionID(context.Background(), "session-1")`。
- `pkg/run/subagent_test.go:8523-8580` `TestUsableModelsListAndDispatchAgree`：用 `state.HandOffPlan(fix.fac.Owner.StateRoot(), "session-1", state.ModeAgent, state.PlanHandOff{...})` 写交接状态，再查 `usableModels` / `usableModelByID`。**照它写**。
- `pkg/run/subagent_test.go:405-411`：构造 legacy 配置的样板：
  ```go
  cfg := &appcfg.Root{Agents: appcfg.AgentsSection{
      Definitions: map[string]appcfg.AgentDefinition{
          "main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
              {Provider: "openai", Model: "gpt-4o", APIKey: "key", BaseURL: "https://example.invalid"},
          }},
      },
  }}
  ```

仓库约定：
- 三种派发错误文案是 owner 逐字定稿的（plan 283 Done criteria 点名），**复用原句，不要另造措辞**。
- `model` 只认行 id；旧式 `provider/model` 标签仅在本兜底分支里作为「交接记录曾写下的 id」被解析，不要扩大兼容面。
- 交接例外仍**不看** `subagent_allowed` —— 本计划只加凭据判断，不加开关判断。
- `gofmt` 干净；注释用英文。

## Commands you will need

| Purpose | Command | Expected on success |
|---------|---------|---------------------|
| 定向测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestUsableModelsLegacyHandOffNeedsCredentials$' -count=1 -v` | `--- PASS` |
| 相邻回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestUsableModelsListAndDispatchAgree$' -count=1 -v` | `--- PASS` |
| 回归包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tool ./pkg/turn -count=1` | 全部 `ok` |
| 架构规则 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |
| 构建 / Vet / 格式 | `CGO_ENABLED=1 go build -tags fts5 ./...` / `CGO_ENABLED=1 go vet -tags fts5 ./...` / `gofmt -l pkg cmd` | exit 0 / 无输出 / 无输出 |

## Scope

**In scope**（只允许改这两个文件）：
- `pkg/run/subagent.go` —— 只改 `usableModelByID` 的 `ErrLLMModelNotFound` 兜底分支。
- `pkg/run/subagent_test.go` —— 新增一条测试。

**Out of scope**（不许动）：
- `usableModels`、`handOffModelID`、`subagentModelOverrideForRef`、`subagentRecordPin`（后者由 `290` / 后续计划处理）。
- `pkg/config/*`、`pkg/state/*`。
- 三种派发错误文案、工具定义与 `model` 参数说明文字。
- `subagentModelOverrideForRef` 对零值 `Row` 调 `ConfigRow()` 的现状（见维护说明，本计划不修）。

## Git workflow

- 按 `plans/README.md` 既有约定：**in-tree、零 commit**，改动留在工作区由 owner 审阅。不要自行 `git merge` / `rebase` / `push` / 删 worktree。
- 若 owner 另行要求提交：Conventional Commits 标题，如 `fix(run): require credentials for the legacy hand-off fallback`；subject 小写开头、≤100 字符，`git commit -s`（DCO）。

## Steps

### Step 1: 先写会红的回归测试

在 `pkg/run/subagent_test.go` 末尾新增 `func TestUsableModelsLegacyHandOffNeedsCredentials(t *testing.T)`。写一个本地小工厂，按 `:405-411` 的形状构造 legacy 配置（把 `APIKey` 做成参数），并带上 `subagentsEnabledConfig()` 里的 `EnableSubagent` 默认值：

```go
legacyCfg := func(key string) *appcfg.Root {
    on := true
    return &appcfg.Root{Agents: appcfg.AgentsSection{
        Defaults: appcfg.AgentDefaults{EnableSubagent: &on},
        Definitions: map[string]appcfg.AgentDefinition{
            "main": {LLMProviders: []appcfg.AgentLLMProviderConfig{
                {Provider: "openai", Model: "legacy-m", BaseURL: "https://legacy.test/v1", APIKey: key},
            }},
        },
    }}
}
```

用例步骤：
1. `ref := "openai/legacy-m"`（旧式标签，库里不存在这一行）。**先断言前提**：`entry, ok := appcfg.ResolveModelRef(legacyCfg(""), "main", ref)` → `ok == true`，`strings.TrimSpace(entry.APIKey) == ""`，`appcfg.CredentialsBackedProvider(entry.Provider) == false`。前提不成立就 `t.Fatalf` 并停下（说明 legacy 配置形状不对）。
2. `fix := newModelArgumentFixture(t, legacyCfg(""))`；`ctx := dispatchTestCtx()`。
3. 写交接状态：`if _, err := state.HandOffPlan(fix.fac.Owner.StateRoot(), "session-1", state.ModeAgent, state.PlanHandOff{ModelID: ref, Provider: "openai", Model: "legacy-m"}); err != nil { t.Fatalf("hand off: %v", err) }`（**必须检查返回的 error**，否则写失败会让后面的红断言失败在错误的原因上）；随后断言 `handOffModelID(ctx, fix.fac) == ref`。
4. `listed, err := usableModels(ctx, fix.fac)` → 断言没有一行以 `ref` 为身份（列表只遍历 DB 行，本 fixture 未 seed 任何行，可直接断言 `len(listed) == 0`）。
5. **红的断言**：`_, err = usableModelByID(ctx, fix.fac, ref)` → 期望 `err != nil` 且 `strings.Contains(err.Error(), "has no credentials")`。
6. **正例**：另建 `fix2 := newModelArgumentFixture(t, legacyCfg("key-x"))`，同样写交接状态（注意每个 fixture 有自己的 `StateRoot()`），`usable, err := usableModelByID(ctx, fix2.fac, ref)` → `err == nil && usable.HandedOff == true && strings.TrimSpace(usable.Entry.Model) == "legacy-m"`。证明有凭据的旧式条目仍可通过交接例外。
7. **负例边界**：交接记录不存在时 `usableModelByID(ctx, fix2.fac, ref)` 必须返回 `no configured model has id` —— 守住「只有本会话交接记录指名的那一行」这条语义。**注意 `fix2` 在第 6 步已经写过交接记录**，所以这一步必须另建**第三个没写过交接**的 fixture：`fix3 := newModelArgumentFixture(t, legacyCfg("key-x"))`，再断言 `usableModelByID(ctx, fix3.fac, ref)` 的错误文本含 `no configured model has id`。

**Verify（预期红）**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestUsableModelsLegacyHandOffNeedsCredentials$' -count=1 -v` → **FAIL** 于步骤 5（修复前返回 `nil` error）。把红输出记进执行报告。

### Step 2: 给兜底分支加凭据判断

在 `usableModelByID` 的 `ErrLLMModelNotFound` 分支里，把

```go
if entry, ok := appcfg.ResolveModelRef(fac.appConfig(), fac.AgentName, id); ok && strings.TrimSpace(entry.Model) != "" {
    return usableModel{Entry: entry, HandedOff: true}, nil
}
```

改成：

```go
if entry, ok := appcfg.ResolveModelRef(fac.appConfig(), fac.AgentName, id); ok && strings.TrimSpace(entry.Model) != "" {
    // The hand-off exception waives the switch, not the credential rule:
    // list_usable_models only lists rows that can actually be called, and a
    // dispatch must agree with it (语义 9).
    credentialed := strings.TrimSpace(entry.APIKey) != "" || appcfg.CredentialsBackedProvider(entry.Provider)
    if !credentialed {
        return usableModel{}, fmt.Errorf("model %q (provider/model) has no credentials; call list_usable_models, or omit model to use the conversation's model.", llm.FormatProviderModel(entry.Provider, entry.Model))
    }
    return usableModel{Entry: entry, HandedOff: true}, nil
}
```

错误文案**逐字复用** `:4383-4384` 的 DB 行分支（只把 `row.Provider, row.Model` 换成 `entry.Provider, entry.Model`）。不要新增 import；`appcfg`、`llm`、`fmt`、`strings` 本文件均已 import。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestUsableModelsLegacyHandOffNeedsCredentials$' -count=1 -v` → `--- PASS`；`grep -c 'has no credentials' pkg/run/subagent.go` = `2`（DB 行分支 + 兜底分支）。

### Step 3: 收口回归

**Verify**（全部要跑）：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestUsableModelsListAndDispatchAgree$' -count=1 -v` → `--- PASS`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tool ./pkg/turn -count=1` → 全部 `ok`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0
- `CGO_ENABLED=1 go vet -tags fts5 ./...` → 无输出
- `gofmt -l pkg cmd` → 无输出

## Test plan

- 新测试：`pkg/run/subagent_test.go` 的 `TestUsableModelsLegacyHandOffNeedsCredentials`，覆盖：
  - 前提：legacy 条目能被 `ResolveModelRef` 按旧式标签解析且确实无凭据；
  - 回归（本计划修的 bug）：无凭据的交接兜底条目被 `usableModelByID` 以定稿文案拒绝；
  - happy path：有凭据的同一条目仍以 `HandedOff=true` 放行；
  - 一致性：`usableModels` 不列该条目（列表只遍历 DB 行），派发也不接受它；
  - 边界：没有交接记录时按「未知 id」拒绝，兜底不扩大到任意调用。
- 结构样板：`pkg/run/subagent_test.go:8523-8580` 与 `:405-411`。
- 验证：Step 1 红、Step 2 绿；禁止用 `-run` 零匹配当证据。

## Done criteria

- [ ] `grep -c 'has no credentials' pkg/run/subagent.go` = `2`
- [ ] `grep -n 'credentialed := strings.TrimSpace(entry.APIKey)' pkg/run/subagent.go` 有 1 处命中
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestUsableModelsLegacyHandOffNeedsCredentials$' -count=1 -v` → `--- PASS`（修复前 FAIL 的证据在报告里）
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '^TestUsableModelsListAndDispatchAgree$' -count=1 -v` → `--- PASS`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/tool ./pkg/turn -count=1` 全 `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...`、`CGO_ENABLED=1 go vet -tags fts5 ./...`、`gofmt -l pkg cmd` 全绿
- [ ] `git status --porcelain -- pkg/run` 只列出 `subagent.go` 与 `subagent_test.go`（若 `290` 同期执行，两文件本就在改动列表里，不要据此误判越界）
- [ ] `plans/README.md` 中本计划状态行已更新

## STOP conditions

- Drift check 显示两个文件在 `d715dafd` 之后被改过且摘录对不上（尤其 `290` 已改过 `subagentRecordPin`；行号漂移可以接受，兜底分支形状必须一致）。
- `AgentLLMProviderConfig` 上没有 `APIKey` 字段，或 `CredentialsBackedProvider` 不在 `pkg/config` —— 说明凭据规则已迁移，先报告。
- 新用例的前提断言（步骤 1 第 1 点）不成立：不要靠改 `ResolveModelRef` 或扩大兼容面来「修好」，先报告 legacy 配置的实际形状。
- 修复需要改 `usableModels`、三种派发错误文案或工具定义文字。
- 相邻既有用例（尤其 `TestUsableModelsListAndDispatchAgree`、`TestSubagentModelArgumentPinsTheNamedRow`）必须放宽断言才能通过。
- 同一验证连续两次失败。

## Maintenance notes

- **已知但未修的相邻问题**（范围外，留给后续）：兜底分支返回 `usableModel{Entry: entry, HandedOff: true}` 时 `Row` 是零值；`subagentModelOverrideForRef`（`:4423`）随后调 `usable.Row.ConfigRow()`，因 `ParseLLMRequestParamsJSON("")` 返回 `nil, nil` 而不报错，但得到的 `Caps` 全 nil，派发时该行能力投影为空。也就是说「有凭据的旧式条目」能派出去却拿不到该条目的能力设置。这是交接兜底本身的局限，不是本计划引入的；若 owner 要求彻底修，应单独立项把 `Row`/`Caps` 一起补上。
- 以后再给 `usableModels` / `usableModelByID` 加准入条件时，必须同时检查**三条**路径：列表路径、DB 行派发路径、旧式配置兜底路径。本计划的 bug 就是第三条漏了。
- 评审 PR 时重点看：错误文案是否与 DB 行分支逐字一致（模型可见文本是定稿的），以及正例是否仍放行（别把交接例外一起关掉）。
