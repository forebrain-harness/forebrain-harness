# Plan 100：provider 列表保存「就地编辑」——给行加模型不拆行、改端点不挪位置、不悄悄换默认模型

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。
>
> **Drift check（先跑）**：本计划建在工作区里**未提交**的 `default_model` 重构之上，所以不能用 `git diff HEAD` 判漂移。
> 改用内容哈希（计划写成时的值）：
>
> ```
> git hash-object pkg/gateway/api_extra.go pkg/gateway/api_extra_test.go \
>   frontend/src/views/ProvidersView.vue frontend/src/views/ProvidersView.test.ts frontend/src/lib/api.ts | cut -c1-10
> ```
>
> 输出的五行依次对应下表五个文件，写成时的值：
>
> | 文件 | 写成时的哈希前 10 位 |
> |---|---|
> | `pkg/gateway/api_extra.go` | `9780bdfde6` |
> | `pkg/gateway/api_extra_test.go` | `64edd8ec6a` |
> | `frontend/src/views/ProvidersView.vue` | `d5519e668c` |
> | `frontend/src/views/ProvidersView.test.ts` | `3604badf61` |
> | `frontend/src/lib/api.ts` | `a7f2744f9f` |
>
> 哈希不同**不等于必须停**：owner 可能还在改这几个文件。逐条对照下面 "Current state" 的摘录，
> 摘录里的符号与行为都还在就继续；摘录对不上才是 STOP 1。
> （工作区里 `pkg/config/*`、`pkg/run/*`、`pkg/turn/*`、`pkg/tui/*` 的既有改动与本计划无关，不要碰、不要回滚。）

## Status

- **Priority**: **P1**（保存路径上的用户可见回归：`connect` 流程每次给已有服务加模型都会触发）
- **Effort**: M（后端合并函数 ~90 行 + 新字段；前端 3 处；Go 测试 8 个 + 前端测试 3 个）
- **Risk**: MED（改 PUT 线协议——新增**可选**字段，旧客户端不发它时行为与今天完全一致；合并算法是本计划的核心）
- **Depends on**: none（但依赖工作区里未提交的 `default_model` 重构，那是它要修的对象，不要回滚）
- **Category**: bug（正确性：保存后列表结构与默认模型和用户的编辑不符）
- **Planned at**: commit `55019a2` + 未提交工作区，2026-10-23

## Why this matters

`PUT /api/providers` 现在按「条目身份」合并：身份 = provider + base_url + api_path + model
（`pkg/gateway/api_extra.go:5485`）。请求里没有对应旧条目的行一律当「新行」追加到末尾，旧条目当「被删」丢掉。
结果「编辑」被当成「删除再追加」，三个已用一次性测试复现的后果：

1. **给已有服务加一个模型，行被拆成两行**。种子 `[deepseek/deepseek-v4, openai/gpt-test]`，把 deepseek 行改成
   两个模型再保存，落盘顺序是 `[deepseek/deepseek-v4, openai/gpt-test, deepseek/deepseek-v4-flash]`，
   页面读回来是三行（`providerRowsFor` 只折叠**相邻**同签名条目）。`connect` 登录后选模型
   （`ProvidersView.vue:1017` 的 `saveConnectModel`）对已有行走的正是这条路。
2. **改隐含默认行的 base_url，默认模型悄悄变了**。种子 `[deepseek, openai]`、没写 `default_model`
   （第一行即默认）；把 deepseek 的 base_url 改掉再保存，落盘顺序变成 `[openai, deepseek]`，
   `GET` 回来的默认模型从 `deepseek/deepseek-v4` 变成 `openai/gpt-test`——新会话从此跑在另一个模型上，用户没动过默认。
3. **改显式默认行的模型名，被拒绝且文案是「删除」**。种子 `default_model: deepseek/deepseek-v4`；把该行模型
   改成 `deepseek-v4-pro`，返回 400「该模型服务当前是默认模型，请先将其他模型设为默认再删除」。

修法（owner 2026-10-23 已选）：编辑过的行在 PUT 里带上 `replaces`——它**编辑前**的样子；服务端按它在存量里
找到旧条目所占的槽位，就地换成新条目，多出来的模型紧挨着插在该行最后一个槽位后面，少了的删掉；
默认模型跟着被换掉的槽位走。没有 `replaces` 的行（没动过的行、新行）走今天的身份匹配，行为不变。

## Current state

- `pkg/gateway/api_extra.go` — `handleProviders`；PUT 分支把请求行展开成 `entries`，再和磁盘上的 `stored` 合并。
- `pkg/gateway/api_extra_test.go` — 现有 providers 测试；夹具 `providersDTOServer(t)`、`providersPut(t, s, body)`、
  `seedProvidersConfig(t, home, extraTop, pairs...)`、`providersGetDefaultModel(t, s)`、`providerOrderInYAML(t, raw)` 都在这里。
- `frontend/src/views/ProvidersView.vue` — 表格页；`toBody`（`:656`）把一行写成 PUT 体，`modalSave`（`:757`）是编辑弹窗的保存，
  `saveConnectModel`（`:1017`）是登录后选模型的保存。
- `frontend/src/lib/api.ts` — `ProviderRecord`（`:1085`）与 `saveProviders`（约 `:3540`）。

**请求行的线格式**（`pkg/gateway/api_extra.go:5448-5463`，节选）：

```go
type providerPutRow struct {
	Provider string                  `json:"provider"`
	BaseURL  string                  `json:"base_url,omitempty"`
	APIPath  string                  `json:"api_path,omitempty"`
	Params   config.LLMRequestParams `json:"params,omitempty"`
	Model    string                  `json:"model,omitempty"`
	Models   []string                `json:"models,omitempty"`
	APIKey      *string `json:"api_key,omitempty"`
	APIKeyPlain string  `json:"api_key_plain,omitempty"`
}
```

**条目身份**（`:5485`）——provider/model 不分大小写，base_url/api_path 精确比较：

```go
func providerEntryIdentity(item config.AgentLLMProviderConfig) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(item.Provider)),
		strings.TrimSpace(item.BaseURL),
		strings.TrimSpace(item.APIPath),
		strings.ToLower(strings.TrimSpace(item.Model)),
	}, "\x00")
}
```

**PUT 里把请求行展开成 `entries`**（循环 `for _, row := range req.Providers` 在 `:5636` 起，节选）：每个请求行按 `models` 展开成每个模型一条
`config.AgentLLMProviderConfig`；`models` 为空时用 `[""]`。展开之后**丢失了「这几条来自同一行」的信息**——
本计划要把它留下来。

**当前合并块**（`:5691-5714`，这就是要被替换的代码）：

```go
		stored := config.ResolvedLLMConfigs(def)
		prevDefault := effectiveNonPlaceholderDefault(def)
		merged := make([]config.AgentLLMProviderConfig, 0, len(entries))
		used := make([]bool, len(entries))
		for _, item := range stored {
			match := -1
			for i, row := range entries {
				if !used[i] && providerEntryIdentity(row) == providerEntryIdentity(item) {
					match = i
					break
				}
			}
			if match < 0 {
				continue // the request dropped this entry
			}
			used[match] = true
			merged = append(merged, entries[match])
		}
		for i, row := range entries {
			if !used[i] {
				merged = append(merged, row)
			}
		}
		def.LLMProviders = merged
```

**紧随其后的默认模型处理**（`:5715-5733`）：

```go
		if req.DefaultModel != nil {
			if strings.TrimSpace(*req.DefaultModel) == "" {
				http.Error(w, "default_model 不能清空：请将其他模型服务设为默认", http.StatusBadRequest)
				return
			}
			def.DefaultModel = strings.TrimSpace(*req.DefaultModel)
			if config.EffectiveDefaultLLM(def) == nil {
				http.Error(w, "default_model "+def.DefaultModel+" 不在模型服务列表中，请先对该列表中的模型设为默认", http.StatusBadRequest)
				return
			}
		} else if prevDefault != nil &&
			!providerListHasEntry(def.LLMProviders, prevDefault.Provider, prevDefault.Model) {
			http.Error(w, "该模型服务当前是默认模型，请先将其他模型设为默认再删除", http.StatusBadRequest)
			return
		}
```

**读回时的折叠规则**（`providerRowsFor`，`:5508`）：只有**相邻**且 `providerSignature` 相同（provider、base_url、api_path、params、api_key）
的条目折成一行。所以「同一行的模型必须在存量里相邻」是本计划的不变量。

**默认模型解析**（`pkg/config/agent_llm.go` 的 `EffectiveDefaultLLM`，工作区未提交）：`default_model` 非空时按
`provider/model`（首个斜杠切分、不分大小写）在**展开后**的列表里找第一个匹配；为空时取展开后的第一条。

**前端现状**（`frontend/src/views/ProvidersView.vue`）：

```ts
type ProviderEntry = ProviderRecord & { apiKeyPlain?: string; apiKey?: string }       // :412

function entryKey(entry: ProviderRecord): string {                                      // :637
  return [String(entry.provider ?? '').trim(), String(entry.baseUrl ?? '').trim(), [...(entry.models ?? [])].join('|')].join('\u0000')
}

function toBody(entry: ProviderEntry): Record<string, unknown> {                        // :656
  const body: Record<string, unknown> = {
    provider: String(entry.provider ?? '').trim(),
    models: [...(entry.models ?? [])],
    base_url: String(entry.baseUrl ?? '').trim(),
    api_path: String(entry.apiPath ?? '').trim(),
  }
  ...
}

// modalSave 里（:757 起）：
  await writeWholeOrder(
    (all) => {
      const at = key === null ? -1 : all.findIndex((candidate) => entryKey(candidate) === key)
      if (at >= 0) all[at] = { ...all[at], ...entry }
      else all.push(entry)
    },
    ...

// saveConnectModel 里（:1017 起）：
    const at = all.findIndex((candidate) => String(candidate.provider ?? '').trim() === provider)
    if (at >= 0) {
      const models = [...(all[at].models ?? [])]
      if (!models.includes(model)) models.push(model)
      all[at] = { ...all[at], models, baseUrl }
      return
    }
```

`writeWholeOrder` 每次先 `forebrainApi.providers()` 重读整张表，所以 `all[at]` 是**服务端当前**的那一行
（也就是 `replaces` 需要的「编辑前的样子」）。

**约定**：网关的错误文案在这一族接口里是中文（如上）；测试用 `testify/require`，夹具用 `t.Helper()`；
前端测试风格见 `ProvidersView.test.ts` 里 `saves the picked format as the row api_path`（读 `saveProviders.mock.calls[0][0]`）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 构建 | `go build ./...` | exit 0（链接器的 `ld: warning` 可忽略） |
| Go 针对性测试 | `go test -tags fts5 ./pkg/gateway/ -run 'TestProvidersDTO\|TestMergeProviderEntries' -count=1` | `ok` |
| Go 整包 | `go test -tags fts5 ./pkg/gateway/ -count=1` | `ok` |
| gofmt | `test -z "$(gofmt -l cmd pkg third_party)"` | exit 0（注意 HEAD 上 `pkg/tui/notify.go` 已有一处无关的格式问题，见计划 103；只要不是你改的文件就忽略） |
| vet | `go vet ./pkg/gateway/` | exit 0 |
| 包图快照 | `scripts/package-graph.sh && git diff --stat -- pkg/architecture/testdata/graph.json` | 只会有 `pkg/gateway` 的 `loc` 行变化 |
| 前端单测 | `cd frontend && npx vitest run src/views/ProvidersView.test.ts` | 全绿 |
| 前端类型 | `cd frontend && npx vue-tsc --noEmit` | 本计划不新增错误（工作区里 `ProvidersView.test.ts` 的 4 个既有 `defaultModel` 错误由计划 103 处理，不在这里修） |

## Scope

**In scope**（只允许改这些）：
- `pkg/gateway/api_extra.go`
- `pkg/gateway/api_extra_test.go`
- `pkg/architecture/testdata/graph.json`（只允许 `scripts/package-graph.sh` 重新生成它，CI 有 `git diff --exit-code` 门禁）
- `frontend/src/views/ProvidersView.vue`
- `frontend/src/views/ProvidersView.test.ts`
- `frontend/src/lib/api.ts`（只改 `ProviderRecord.index` 的过期注释 `:1094-1097`，它还写着「fallback order / 0 is the primary」）

**Out of scope**（看起来相关，但不要碰）：
- `pkg/config/*`（`EffectiveDefaultLLM`、`Save`、`NormalizeLLMProviderEntries`）——默认模型语义已定，本计划只在网关侧消费它。
- `providerRowsFor` / `providerSignature` 的折叠规则——「相邻同签名折成一行」是正确的，改合并而不是改折叠。
- `pkg/gateway/dist/*`、`frontend/e2e/*`——不重建 dist、不跑 e2e（评审方另行真机验收）。
- 其它 handler、`pkg/run`、`pkg/turn`、`pkg/tui`。
- 不要给 PUT 加任何别的新字段；不要改 `default_model` 的现有三条拒绝规则（清空、悬空、删除默认行）。

## Git workflow

- **不提交**。改动留在 dev 工作区。仓库提交风格（仅供评审方参考）：`fix(gateway): ...`，正文写 Why / What / Verification。

## Steps

### Step 0（先做）：先写 Go HTTP 测试，确认其中 4 个是红的

在 `pkg/gateway/api_extra_test.go` 里、`TestProvidersDTORefusesDeletingTheDefaultRow` 之后，仿 `TestProvidersDTOPutPreservesDiskOrder` 的写法
（`providersDTOServer` → `seedProvidersConfig` → `providersPut` → `providerOrderInYAML` / `providersGetDefaultModel`）新增下面 7 个测试。
PUT 体里的 `replaces` 字段此刻旧代码还不认（`json.Decoder` 忽略未知字段），所以测试能编译、能跑。
**每个 PUT 行都像现有测试那样带 `"api_key_plain":"sk-x-1111"`**（key-mode 的行没有 key 会被「needs api_key」拒掉；参照 `TestProvidersDTOPutPreservesDiskOrder` 的 PUT 体）：

1. `TestProvidersDTOPutEditsARowInPlace`：种子三行 `deepseek/deepseek-v4`、`openai/gpt-test`、`moonshotai/kimi`（`seedProvidersConfig` 给每行都写 `base_url: https://api.deepseek.com`）；
   PUT 三行，其中 openai 行 `base_url` 改成 `https://mirror.example.com` 并带
   `"replaces":{"provider":"openai","base_url":"https://api.deepseek.com","models":["gpt-test"]}`，另两行不带 `replaces`；
   断言 `providerOrderInYAML` 为 `[deepseek openai moonshotai]`，且 openai 的 base_url 已更新。**预期现在红**（openai 被挪到末尾）。
2. `TestProvidersDTOPutAddsAModelNextToItsRow`：种子 `[deepseek/deepseek-v4, openai/gpt-test]`；deepseek 行改成 `"models":["deepseek-v4","deepseek-v4-flash"]` 并带
   `replaces`（models 只有 `deepseek-v4`）；断言落盘 provider/model 顺序是
   `deepseek/deepseek-v4, deepseek/deepseek-v4-flash, openai/gpt-test`（本测试自己写一个读 `Provider+"/"+Model` 的小辅助，别改 `providerOrderInYAML`），
   并且 `GET` 读回恰好 **2 行**、第一行有 2 个模型（用 `providersGet(t, s)`）。**预期现在红**（读回 3 行）。
3. `TestProvidersDTOPutRemovesAModelFromARow`：种子里让 deepseek 有 `deepseek-v4`、`deepseek-v4-flash` 两条相邻条目（`seedProvidersConfig` 的 pairs 重复 provider 即可）+ openai；
   PUT deepseek 行只留 `deepseek-v4`，带 `replaces`（两个模型）；断言 `deepseek-v4-flash` 没了、顺序是 `deepseek/deepseek-v4, openai/gpt-test`。（旧代码下已经绿，是回归护栏。）
4. `TestProvidersDTOEditingTheImplicitDefaultKeepsItDefault`：种子 `[deepseek, openai]`，不写 `default_model`；改 deepseek 的 base_url（带 `replaces`）；
   断言 `providersGetDefaultModel` 仍是 `deepseek/deepseek-v4`，顺序仍是 `[deepseek openai]`。**预期现在红**（默认变成 `openai/gpt-test`）。
5. `TestProvidersDTOEditingTheExplicitDefaultFollowsTheEdit`：种子 `default_model: deepseek/deepseek-v4`（`extraTop` 传 `"      default_model: deepseek/deepseek-v4\n"`）；
   把 deepseek 行的模型改成 `deepseek-v4-pro`（带 `replaces`）；断言 200，YAML 含 `default_model: deepseek/deepseek-v4-pro`，`providersGetDefaultModel` 返回它。**预期现在红**（400「…再删除」）。
6. `TestProvidersDTOStaleReplacesFallsBackToIdentity`：`replaces` 指向存量里没有的条目（比如 provider `ghost`）；断言 200、没有 panic、未改动的行位置不变、该行作为新行追加在末尾。（旧代码下已经绿，护栏。）
7. `TestProvidersDTOEditThatRemovesTheDefaultModelIsRefused`：显式默认是 `deepseek/deepseek-v4-flash`，该行原有两个模型；PUT 带 `replaces`（两个模型）但只保留 `deepseek-v4`；断言 400 且正文含「默认模型」。（旧代码下已经绿，护栏。）

**Verify**：`go test -tags fts5 ./pkg/gateway/ -run 'TestProvidersDTOPutEditsARowInPlace|TestProvidersDTOPutAddsAModelNextToItsRow|TestProvidersDTOEditingTheImplicitDefaultKeepsItDefault|TestProvidersDTOEditingTheExplicitDefaultFollowsTheEdit' -count=1 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'` →
**恰好这 4 个 `--- FAIL`**。不是红的就是测试没写到点子上（比如忘了带 `replaces`，或种子顺序不对），先修测试，不要往下走。

### Step 1：后端加线字段和纯合并函数

在 `pkg/gateway/api_extra.go`、`providerPutRow` 定义**之后**（`providerSignature` 之前）加：

```go
// providerReplaces is the row an edit started from, exactly as the page loaded
// it. It lets the merge replace that row's stored entries in place; without it
// an edit looks like "these entries vanished, these new ones appeared" and the
// merge would delete the old ones and append the new ones at the end.
type providerReplaces struct {
	Provider string   `json:"provider"`
	BaseURL  string   `json:"base_url,omitempty"`
	APIPath  string   `json:"api_path,omitempty"`
	Models   []string `json:"models,omitempty"`
}
```

并在 `providerPutRow` 里（`APIKeyPlain` 之后）加一个字段：

```go
	// Replaces is set only on a row the page edited. See providerReplaces.
	Replaces *providerReplaces `json:"replaces,omitempty"`
```

然后加纯函数（放在 `providerListHasEntry` 之后）。**照这个形状写**，它是本计划的核心：

```go
// providerMergeRow is one request row after expansion: its entries (one per
// model, in the row's order) and, for an edited row, where it started.
type providerMergeRow struct {
	Entries  []config.AgentLLMProviderConfig
	Replaces *providerReplaces
}

// providerMergeResult is the merged list plus what every replaced stored slot
// became. A nil value means the edit removed that slot's model.
type providerMergeResult struct {
	Merged   []config.AgentLLMProviderConfig
	Replaced map[int]*config.AgentLLMProviderConfig
}

// mergeProviderEntries merges the request into the stored (expanded) list
// while keeping the stored order, which is user data:
//
//  1. An edited row (Replaces set) claims the stored slots its old entries
//     occupy. Its i-th new entry takes its i-th slot; extra entries go right
//     after its last slot (so the row stays adjacent and folds into one row on
//     read); slots it no longer fills are deleted.
//  2. Every other stored entry is kept when an unused request entry has the
//     same identity (updated in place), and dropped otherwise.
//  3. Request entries nobody used are new rows, appended in request order.
//
// A Replaces that finds none of its entries in the stored list (the page was
// stale) claims nothing and the row falls back to identity matching.
func mergeProviderEntries(stored []config.AgentLLMProviderConfig, rows []providerMergeRow) providerMergeResult {
	owner := make([]int, len(stored))
	for i := range owner {
		owner[i] = -1
	}
	slots := make([][]int, len(rows))
	for r, row := range rows {
		if row.Replaces == nil {
			continue
		}
		models := row.Replaces.Models
		if len(models) == 0 {
			models = []string{""}
		}
		for _, model := range models {
			want := providerEntryIdentity(config.AgentLLMProviderConfig{
				Provider: row.Replaces.Provider,
				BaseURL:  row.Replaces.BaseURL,
				APIPath:  row.Replaces.APIPath,
				Model:    model,
			})
			for p := range stored {
				if owner[p] == -1 && providerEntryIdentity(stored[p]) == want {
					owner[p] = r
					slots[r] = append(slots[r], p)
					break
				}
			}
		}
		sort.Ints(slots[r])
	}

	used := make([][]bool, len(rows))
	for r := range rows {
		used[r] = make([]bool, len(rows[r].Entries))
	}
	replaced := map[int]*config.AgentLLMProviderConfig{}
	after := map[int][]config.AgentLLMProviderConfig{}
	for r, row := range rows {
		n := len(slots[r])
		for i, p := range slots[r] {
			if i < len(row.Entries) {
				entry := row.Entries[i]
				replaced[p] = &entry
				used[r][i] = true
			} else {
				replaced[p] = nil
			}
		}
		if n > 0 && len(row.Entries) > n {
			last := slots[r][n-1]
			for i := n; i < len(row.Entries); i++ {
				after[last] = append(after[last], row.Entries[i])
				used[r][i] = true
			}
		}
	}

	merged := make([]config.AgentLLMProviderConfig, 0, len(stored))
	for p, item := range stored {
		if owner[p] >= 0 {
			if entry := replaced[p]; entry != nil {
				merged = append(merged, *entry)
			}
			merged = append(merged, after[p]...)
			continue
		}
		found := false
		for r := range rows {
			for i, entry := range rows[r].Entries {
				if !used[r][i] && providerEntryIdentity(entry) == providerEntryIdentity(item) {
					used[r][i] = true
					merged = append(merged, entry)
					found = true
					break
				}
			}
			if found {
				break
			}
		}
	}
	for r := range rows {
		for i, entry := range rows[r].Entries {
			if !used[r][i] {
				merged = append(merged, entry)
			}
		}
	}
	return providerMergeResult{Merged: merged, Replaced: replaced}
}
```

`sort` 已在该文件的 import 里（`providerBaseURLDefaults` 在用）；不要重复 import。

**Verify**：`go build ./pkg/gateway/ && go vet ./pkg/gateway/` → exit 0（此时函数还没被调用，Go 允许未使用的包级函数）。

### Step 2：PUT 分支改用合并函数，并让默认模型跟着被换掉的槽位走

在 PUT 分支里展开请求行的那个循环（`for _, row := range req.Providers { ... entries = append(entries, ...) }`，`:5636` 起）旁边，
**同时**构造 `mergeRows []providerMergeRow`：每个请求行一项，`Entries` 是这一行展开出的那几条（同一次循环里
`entries` 追加之前记下起点、之后切片即可），`Replaces: row.Replaces`。`entries` 本身继续保留（其它地方在用它）。

然后把 `:5691-5714` 的整块合并代码（从 `stored := config.ResolvedLLMConfigs(def)` 到 `def.LLMProviders = merged`）替换为：

```go
		stored := config.ResolvedLLMConfigs(def)
		prevDefault := effectiveNonPlaceholderDefault(def)
		prevSlot := -1
		if prevDefault != nil {
			prevSlot = indexOfProviderModel(stored, prevDefault.Provider, prevDefault.Model)
		}
		result := mergeProviderEntries(stored, mergeRows)
		merged := result.Merged
		def.LLMProviders = merged
```

并加辅助函数（`providerListHasEntry` 旁）：

```go
// indexOfProviderModel is providerListHasEntry's position: the first entry for
// the provider+model pair (the one EffectiveDefaultLLM resolves), or -1.
func indexOfProviderModel(entries []config.AgentLLMProviderConfig, provider, model string) int {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	for i, item := range entries {
		if strings.ToLower(strings.TrimSpace(item.Provider)) == provider &&
			strings.ToLower(strings.TrimSpace(item.Model)) == model {
			return i
		}
	}
	return -1
}
```

把默认模型处理的 `else if` 分支（`:5727-5733`）换成下面这个；`req.DefaultModel != nil` 的分支**一个字符都不改**：

```go
		} else if prevDefault != nil {
			if moved, replacedHere := result.Replaced[prevSlot]; prevSlot >= 0 && replacedHere {
				if moved == nil {
					// The edit removed the very model that is the default.
					http.Error(w, "该模型服务当前是默认模型，请先将其他模型设为默认再删除", http.StatusBadRequest)
					return
				}
				// The default row was edited in place: the default follows the
				// edit. An implicit default (first row) needs nothing — the slot
				// kept its position — an explicit one is retargeted.
				if strings.TrimSpace(def.DefaultModel) != "" {
					def.DefaultModel = strings.TrimSpace(moved.Provider) + "/" + strings.TrimSpace(moved.Model)
				}
			} else if !providerListHasEntry(def.LLMProviders, prevDefault.Provider, prevDefault.Model) {
				http.Error(w, "该模型服务当前是默认模型，请先将其他模型设为默认再删除", http.StatusBadRequest)
				return
			}
		}
```

**Verify**：
- `go build ./pkg/gateway/ && go vet ./pkg/gateway/` → exit 0。
- `go test -tags fts5 ./pkg/gateway/ -run 'TestProvidersDTO' -count=1` → `ok`（**现有**的 `TestProvidersDTOPutPreservesDiskOrder`、`TestProvidersDTODefaultModel`、`TestProvidersDTORefusesDeletingTheDefaultRow` 一条都不许改、必须仍绿——它们都不带 `replaces`，走的是没变的身份匹配路径）。

### Step 3：补纯函数单测

在 `pkg/gateway/api_extra_test.go` 里新增 `TestMergeProviderEntries`（表驱动，直接测 `mergeProviderEntries`，不起 HTTP）：
覆盖「无 `replaces` 等价于旧的身份匹配行为」「就地替换」「增加模型紧挨最后一个槽位插入」「删除模型（`Replaced[p]` 为 nil）」
「两行争同一槽位，后到者回退为身份匹配」「`Replaces.Models` 为空」「`Replaces` 找不到任何存量条目」。

**Verify**：`go test -tags fts5 ./pkg/gateway/ -run 'TestProvidersDTO|TestMergeProviderEntries' -count=1 -v 2>&1 | grep -E '^(--- (PASS|FAIL)|ok|FAIL)'` →
Step 0 的 7 个 + 本步 1 个全部 `PASS`，现有 providers 测试仍 `PASS`，无 `FAIL`。

### Step 4：前端把「编辑前的样子」发出去

`frontend/src/views/ProvidersView.vue`：

1. 扩 `ProviderEntry`（`:412`）：
   ```ts
   type ProviderEntry = ProviderRecord & {
     apiKeyPlain?: string
     apiKey?: string
     /** The row as it stood before this edit; the server replaces its entries in place. */
     replaces?: { provider: string; baseUrl: string; apiPath: string; models: string[] }
   }
   ```
2. `toBody`：在组装完 `body`、`return body` 之前加
   ```ts
   if (entry.replaces) {
     body.replaces = {
       provider: entry.replaces.provider,
       base_url: entry.replaces.baseUrl,
       api_path: entry.replaces.apiPath,
       models: [...entry.replaces.models],
     }
   }
   ```
3. 加一个小函数（`entryKey` 旁）：
   ```ts
   /** The row as it stands now, for the server to find the stored entries an edit replaces. */
   function rowAsLoaded(row: ProviderRecord): NonNullable<ProviderEntry['replaces']> {
     return {
       provider: String(row.provider ?? '').trim(),
       baseUrl: String(row.baseUrl ?? '').trim(),
       apiPath: String(row.apiPath ?? '').trim(),
       models: [...(row.models ?? [])],
     }
   }
   ```
4. `modalSave`：把 `if (at >= 0) all[at] = { ...all[at], ...entry }` 改为
   `if (at >= 0) all[at] = { ...all[at], ...entry, replaces: rowAsLoaded(all[at]) }`。
5. `saveConnectModel`：把已有行的分支 `all[at] = { ...all[at], models, baseUrl }` 改为
   `all[at] = { ...all[at], models, baseUrl, replaces: rowAsLoaded(all[at]) }`（注意 `rowAsLoaded(all[at])` 必须读**赋值之前**的 `all[at]`——对象字面量里先求值，没问题，但别把它挪到后面）。

`setDefault`、`askRemove` 等不动：它们不编辑行内容，没有 `replaces` 就走身份匹配。

`frontend/src/lib/api.ts`：把 `ProviderRecord.index` 的注释（`:1094-1097`，现在写着 "fallback order (0 is the primary)"）改为
「The row's position in the whole stored list — a stable row id across pages, not a priority.」。

**Verify**：`cd frontend && npx vue-tsc --noEmit 2>&1 | grep -v 'ProvidersView.test.ts'` → 无输出（只有计划 103 要处理的那 4 个测试文件错误允许存在）。

### Step 5：前端测试

在 `ProvidersView.test.ts` 的 `describe` 里（仿 `saves the picked format as the row api_path`，读 `vi.mocked(forebrainApi.saveProviders).mock.calls[0][0]`）新增：

1. `sends the row as it was loaded when an edit changes it`：用 `mountModalPage()` + `click('provider-edit-0')`，
   改 `provider-base-url`，保存；断言 `saved[0].replaces` 深等于 `{ provider: 'deepseek', base_url: 'https://api.deepseek.com', api_path: '', models: ['deepseek-v4'] }`
   （`forebrainApi.providers` 的 mock 返回里要有这一行，参照 `keeps a path the list does not know...` 那条的 mock 写法）。
2. `does not send replaces for rows it did not edit`：同上场景有两行，只编辑第一行；断言 `saved[1].replaces` 为 `undefined`。
3. `the connect flow replaces an existing row instead of appending a second one`：参照现有 `opencode-login` 相关测试（同文件里搜 `saveConnectModel` 或 `connect`），
   让已有 `opencode-go` 行再选一个新模型；断言 `saved` 里该行 `models` 含新旧两个、且该行带 `replaces`（models 为旧的那个）。

**Verify**：`cd frontend && npx vitest run src/views/ProvidersView.test.ts` → 全绿，新增 3 个。

### Step 6：收尾门禁

1. `scripts/package-graph.sh`（需要 `jq`，本机在 `/usr/bin/jq`），然后 `git diff --stat -- pkg/architecture/testdata/graph.json`：
   只应出现 `pkg/gateway` 的 `loc` 数字变化。出现别的包或边的变化 → STOP 3。
2. `test -z "$(gofmt -l pkg/gateway)"` → exit 0。
3. `go build ./... && go vet ./pkg/gateway/` → exit 0。
4. `go test -tags fts5 ./pkg/gateway/ -count=1` → `ok`。

## Test plan

见 Step 0、Step 3 与 Step 5。结构样板：Go 侧 `TestProvidersDTOPutPreservesDiskOrder`（`pkg/gateway/api_extra_test.go:7677`）；
前端侧 `ProvidersView.test.ts` 的 `saves the picked format as the row api_path`。
**可证伪性**：Step 0 的 1/2/4/5 号测试在旧合并代码下必须红（行被挪到末尾、被拆成三行、默认变了、被拒绝）——Step 0 的 Verify 已要求先看到这 4 个 FAIL；
评审方还会把 Step 2 的替换临时还原来再确认一次。

## Done criteria

全部成立才算完成：

- [ ] `go test -tags fts5 ./pkg/gateway/ -count=1` → `ok`
- [ ] `go test -tags fts5 ./pkg/gateway/ -run 'TestProvidersDTO|TestMergeProviderEntries' -count=1 -v` 中新增的 8 个测试全部 `PASS`，现有 providers 测试无一改动且全部 `PASS`
- [ ] `cd frontend && npx vitest run src/views/ProvidersView.test.ts` → 全绿，比改动前多 3 个用例
- [ ] `grep -n 'fallback order\|is the primary' frontend/src/lib/api.ts` 不再命中 `ProviderRecord.index` 的注释
- [ ] `scripts/package-graph.sh` 之后 `git diff --name-only -- pkg/architecture/testdata/graph.json` 只有这一个文件，且只含 `pkg/gateway` 的 loc 变化
- [ ] `git status --short` 里本计划新增的改动只落在 In scope 列表的文件（工作区里原有的其它脏文件保持原样）
- [ ] 没有任何 `git commit` / `git push` / 新分支

## STOP conditions

遇到下列情况停下报告，不要即兴发挥：

1. 摘录对不上现状：`providerEntryIdentity`、合并块、`providerPutRow`、`toBody`/`modalSave`/`saveConnectModel` 任一与上面 "Current state" 的行为不符（owner 可能已改过）。
2. 现有的 `TestProvidersDTOPutPreservesDiskOrder` / `TestProvidersDTODefaultModel` / `TestProvidersDTORefusesDeletingTheDefaultRow` 在 Step 2 之后变红——说明无 `replaces` 路径的行为被改了，不要改这些测试去迁就。
3. `scripts/package-graph.sh` 产生了 `pkg/gateway` 以外的变化，或 `jq` 不可用。
4. 实现需要改 `pkg/config/*` 才能让默认模型跟随——说明假设「`EffectiveDefaultLLM` 按展开后顺序取第一个匹配」不成立，停下报告。
5. 一个步骤的验证在合理修复后仍失败两次。

## Maintenance notes

- 「同一行的模型在存量里必须相邻」是读回折叠的前提。任何会把同一服务的条目打散的写路径（`/connect`
  的 `applyAgentLLMFields` 把新条目追加到末尾，对**已有服务的新模型**同样会拆行）都有同样的问题——
  `pkg/process/agentyaml.go` 的追加逻辑本计划**没改**，若 owner 要让 TUI 的 `/connect` 也把新模型紧挨着已有行插入，另立计划。
- `replaces` 是可选字段：旧页面（缓存的前端）不发它，行为退回身份匹配，只会重现今天的拆行，不会更坏。
- 评审重点：`mergeProviderEntries` 的槽位认领是否会让两个请求行争同一槽位（测试 8 覆盖）；默认模型跟随只在「显式默认」时改写
  `def.DefaultModel`，隐含默认靠槽位保持第一位。
- 评审方真机验收（`run-forebrain` 技能，隔离 home）：Providers 页给一个已有服务加一个模型 → 仍是一行；
  改默认行的 base_url → 默认徽标不动；`connect` 登录后给已有的 opencode-go 行再选一个模型 → 仍是一行。
- 未做（有意）：多模型行里「设为默认」只能选该行第一个模型、默认落在第二个模型时按钮会消失，这是 UI 取舍，不在本计划。
