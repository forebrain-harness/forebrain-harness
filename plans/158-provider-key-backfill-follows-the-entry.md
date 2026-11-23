# Plan 158：providers 的密钥回填按「entry 身份」匹配，不按 provider 名

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func providerEntryIdentity" pkg/gateway/api_extra.go` 为 `1`；
> `grep -c "oldKeyByProvider" pkg/gateway/api_extra.go` 为 `4`；
> `grep -c "stored := config.ResolvedLLMConfigs(def)" pkg/gateway/api_extra.go` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/gateway/api_extra.go pkg/gateway/api_extra_test.go` 必须**无输出**。
> 有输出就按下文"现状"里的**符号名**核对；`handleProviders` 的写分支结构变了即 STOP 1。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1`（超时给足 900 s）。

## Status

- **Priority**: P2（凭据被送到非用户所选 endpoint；触发条件窄但一旦触发就是安全事件）
- **Effort**: S（一个查表函数 + 一处调用 + 把 `stored` 的声明提前）
- **Risk**: LOW-MED（改的是"用户没动 key"的回填；错的方向是"不再自动带上已存的 key"）
- **Depends on**: 无
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: security（凭据流向）/ bug

## Why this matters

`PUT /api/providers` 允许请求里**不带** `api_key` 字段，含义是"这一行的 key 用户没动，沿用已存的那个"。
回填时用的是**按 provider 名**建的表：

```go
oldKeyByProvider := map[string]string{}          // :5928
for _, item := range def.LLMProviders {
	if key := strings.TrimSpace(item.APIKey); key != "" {
		if _, seen := oldKeyByProvider[strings.TrimSpace(item.Provider)]; !seen {
			oldKeyByProvider[strings.TrimSpace(item.Provider)] = key   // :5931-5932 同名先到先得
		}
	}
}
...
} else {
	apiKey = oldKeyByProvider[strings.TrimSpace(row.Provider)]         // :5972
}
```

但同一份代码里，**合并身份**已经明确不是"provider 名"，而是 provider+endpoint+model：

```go
// providerEntryIdentity is the merge identity of one expanded llm_providers
// entry: provider+model at one endpoint. provider and model compare
// case-insensitively (the engine resolves them that way); base_url and
// api_path compare exactly — the same provider+model at a different endpoint
// is a legal separate entry, so provider+model alone must not be the identity.
func providerEntryIdentity(item config.AgentLLMProviderConfig) string {   // :5653
	return strings.Join([]string{ ... Provider, BaseURL, APIPath, Model ... }, "\x00")
}
```

两者不一致的后果很具体：一个 provider 配了两个 endpoint（这正是上面那位"合法独立条目"），用户编辑**第二条**
而没有重填 key 时，第二条会**继承第一条的 key 引用**——于是第一条的密钥被发往第二条的 `base_url`。
今天 `providerKeyHint`（`:5851-5866`）会把 `${ENV}` 解出来给页面显示末四位，所以这种错配在界面上是
"看起来有 key、认证莫名失败"，只有对过末四位才发现。

修复的判据很简单：**密钥跟着 entry 身份走**；身份对不上就不要猜——留空让既有的"
`needs api_key`"校验把写入拒掉，比把一条密钥送去别的 endpoint 好。

## Current state

写分支的结构（`pkg/gateway/api_extra.go` 的 `handleProviders`，`:5867` 起）：

```go
	case http.MethodPut:
		var req struct {
			Providers    []providerPutRow `json:"providers"`
			DefaultModel *string          `json:"default_model"`
		}
		...
		oldKeyByProvider := map[string]string{}      // :5928  ← 要删
		...
		mergeRows := make([]providerMergeRow, 0, len(req.Providers))   // :5937
		for _, row := range req.Providers {                            // :5938
			...
			models := row.Models
			if len(models) == 0 && strings.TrimSpace(row.Model) != "" { models = []string{row.Model} }
			if len(models) == 0 { models = []string{""} }
			apiKey := ""
			cleared := false
			if plain := strings.TrimSpace(row.APIKeyPlain); plain != "" {
				apiKey, err = process.ProviderAPIKeyConfigReference(strings.TrimSpace(s.Home), strings.TrimSpace(row.Provider), plain)
				...
			} else if row.APIKey != nil {
				apiKey = strings.TrimSpace(*row.APIKey)     // 线上出现 = 用户明确表态（"" 表示清空）
				cleared = apiKey == ""
			} else {
				apiKey = oldKeyByProvider[strings.TrimSpace(row.Provider)]   // :5972  ← 要换
			}
			start := len(entries)
			for _, model := range models {
				entries = append(entries, config.AgentLLMProviderConfig{
					Provider: ..., Model: ..., APIKey: apiKey, BaseURL: row.BaseURL, APIPath: row.APIPath, ...
				})
			}
			mergeRows = append(mergeRows, providerMergeRow{Entries: entries[start:], Replaces: row.Replaces})
		}
		...
		stored := config.ResolvedLLMConfigs(def)         // :5999  ← 要提前到循环之前
		...
		result := mergeProviderEntries(stored, mergeRows)
```

⚠ `models` 在 key 决策**之前**就已经算好了（上面那段），所以按"这一行会展开成哪些 model"查身份不需要改结构。

写盘之后的既有校验（不要动它，它是本计划的兜底）：

```go
		for _, entry := range merged {
			if config.CredentialsBackedProvider(entry.Provider) || strings.TrimSpace(entry.APIKey) != "" {
				continue
			}
			http.Error(w, strings.TrimSpace(entry.Provider)+" needs api_key: this model service authenticates with a key, not a login", http.StatusBadRequest)
			return
		}
```

## Scope

**In scope（只改这些）**：
- `pkg/gateway/api_extra.go`（新增 `storedKeyForRow`；`handleProviders` 写分支换掉回填并提前 `stored`）
- `pkg/gateway/api_extra_test.go`（新增 2 个测试）

**Out of scope（看着相关也不要动）**：
- `mergeProviderEntries` / `providerEntryIdentity` / `providerMergeRow.Replaces` 的合并语义
- `process.ProviderAPIKeyConfigReference` 与 `.env` 的命名规则（`ProviderKeyEnvMap`）
- `providerKeyHint` 的展示规则
- 客户端（`frontend/src`）：网页发的 DTO 不用改，语义（"不带 api_key = 没动"）不变
- 默认模型（`default_model`）相关的一切

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run '^(TestProviderKeyBackfill\|TestProviderKeyBackfillNeverCrosses)' -count=1 -v` | 看到 `--- PASS` |
| 本包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/gateway` | exit 0 |
| gofmt | `gofmt -l pkg/gateway` | 无输出 |

## Steps

### Step 1：先写会红的测试

放在 `pkg/gateway/api_extra_test.go`，用文件里现成的三个 helper（`:3166` `providersDTOServer`、`:3173`
`providersGet`、`:3185` `providersPut`）以及 `.env` 的直写（同文件 `:3207`、`:3482` 已有先例：
`os.ReadFile(filepath.Join(home, ".env"))`）。

**测试 1：同一 provider 的两个 endpoint，各自保住自己的 key**

```go
func TestProviderKeyBackfillFollowsTheEntryNotTheProvider(t *testing.T) {
	s, home := providersDTOServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".env"),
		[]byte("ZHIPUAI_A_KEY=sk-aaaa-1111\nZHIPUAI_B_KEY=sk-bbbb-2222\n"), 0o600))
	created := providersPut(t, s, `{"providers":[
		{"provider":"zhipuai","models":["glm-4"],"base_url":"https://a.example","api_key":"${ZHIPUAI_A_KEY}"},
		{"provider":"zhipuai","models":["glm-4"],"base_url":"https://b.example","api_key":"${ZHIPUAI_B_KEY}"}
	]}`)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())

	// 同两行再存一次，api_key 都不在线上 —— "用户没动 key"。
	untouched := providersPut(t, s, `{"providers":[
		{"provider":"zhipuai","models":["glm-4"],"base_url":"https://a.example"},
		{"provider":"zhipuai","models":["glm-4"],"base_url":"https://b.example"}
	]}`)
	require.Equal(t, http.StatusOK, untouched.Code, untouched.Body.String())

	rows := providersGet(t, s)
	require.Len(t, rows, 2)
	require.Equal(t, "https://a.example", rows[0]["base_url"])
	require.Equal(t, "https://b.example", rows[1]["base_url"])
	require.Equal(t, "1111", rows[0]["api_key_hint"], "a.example 必须还是 A 的 key")
	require.Equal(t, "2222", rows[1]["api_key_hint"], "b.example 不许继承 A 的 key")
}
```

**测试 2：身份对不上就不猜——留空让既有校验拒绝**

```go
func TestProviderKeyBackfillNeverCrossesEndpoints(t *testing.T) {
	s, home := providersDTOServer(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".env"),
		[]byte("ZHIPUAI_A_KEY=sk-aaaa-1111\n"), 0o600))
	created := providersPut(t, s, `{"providers":[
		{"provider":"zhipuai","models":["glm-4"],"base_url":"https://a.example","api_key":"${ZHIPUAI_A_KEY}"}
	]}`)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())

	// 同一批请求里再加一个**新 endpoint**，仍然不带 key：它没有任何已存身份可继承。
	added := providersPut(t, s, `{"providers":[
		{"provider":"zhipuai","models":["glm-4"],"base_url":"https://a.example"},
		{"provider":"zhipuai","models":["glm-4"],"base_url":"https://c.example"}
	]}`)
	require.Equal(t, http.StatusBadRequest, added.Code,
		"a new endpoint must not inherit another endpoint's key; the write is refused instead: %s", added.Body.String())
	require.Contains(t, added.Body.String(), "needs api_key")
}
```

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run '^(TestProviderKeyBackfillFollowsTheEntryNotTheProvider|TestProviderKeyBackfillNeverCrossesEndpoints)$' -count=1 -v`
→ 测试 1 FAIL（`b.example` 的 hint 变成 `1111`），测试 2 FAIL（返回 200，`c.example` 静默拿到 A 的 key）。
若两个都直接 PASS，STOP 3。

### Step 2：按 entry 身份查已存的 key

在 `providerEntryIdentity`（`:5653`）之后新增：

```go
// storedKeyForRow is the key the stored list already carries for this request
// row's models, matched by providerEntryIdentity — provider+model at one
// endpoint. A second entry of the same provider at another endpoint therefore
// never inherits the first one's key: the key the user typed for b.example is
// the only one that may be sent there. Empty means "no stored entry for these
// models at this endpoint", and the caller's own api_key validation refuses the
// write rather than guessing; two stored keys that disagree for one row are the
// same "do not guess".
func storedKeyForRow(stored []config.AgentLLMProviderConfig, row providerPutRow, models []string) string {
	byIdentity := make(map[string]string, len(stored))
	for _, item := range stored {
		if key := strings.TrimSpace(item.APIKey); key != "" {
			byIdentity[providerEntryIdentity(item)] = key
		}
	}
	key := ""
	for _, model := range models {
		want := providerEntryIdentity(config.AgentLLMProviderConfig{
			Provider: strings.TrimSpace(row.Provider),
			Model:    strings.TrimSpace(model),
			BaseURL:  strings.TrimSpace(row.BaseURL),
			APIPath:  strings.TrimSpace(row.APIPath),
		})
		got := byIdentity[want]
		if got == "" {
			continue
		}
		if key != "" && key != got {
			return ""
		}
		key = got
	}
	return key
}
```

（`providerPutRow` 就是本文件 `handleProviders` 写分支用的请求行类型，字段名见"现状"。）

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/gateway` → exit 0。

### Step 3：换掉回填，把 `stored` 提前

1. 删除 `:5928-5935` 的 `oldKeyByProvider` 建表循环（连同它上面那段注释；注释里的语义要跟着改到新函数上，
   不要留一份说"按 provider 带过去"的旧注释）。
2. 把 `:5999` 的 `stored := config.ResolvedLLMConfigs(def)` **整行上移**到
   `mergeRows := make(...)`（`:5937`）之前，并删掉原位置那一行（同一作用域只能声明一次）。
3. 把 `:5972` 的 `apiKey = oldKeyByProvider[strings.TrimSpace(row.Provider)]` 换成：

```go
				// No key on the wire means the user did not touch it: carry the
				// stored key of this entry — the same provider+model at this
				// endpoint — never another endpoint's.
				apiKey = storedKeyForRow(stored, row, models)
```

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run '^(TestProviderKeyBackfillFollowsTheEntryNotTheProvider|TestProviderKeyBackfillNeverCrossesEndpoints)$' -count=1 -v`
→ 两个 PASS。

### Step 4：本包回归

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1`
→ 与基线相比无新增 FAIL。特别注意既有的
`TestProvidersDTORoundTripStoresKeyAsEnvReference`（`:3193`）、写 `.env` 与 "空 key 清空" 的那几条
（`:3252`、`:3469`）都必须仍然 PASS——它们证明"不带 key = 沿用"和"显式空 = 清空"都没被改坏。

## Test plan

- 新增 `TestProviderKeyBackfillFollowsTheEntryNotTheProvider`：两个同 provider 不同 endpoint 的行，
  第二次保存不带 `api_key`，断言两行各自的 `api_key_hint` 仍是自己的末四位。
- 新增 `TestProviderKeyBackfillNeverCrossesEndpoints`：新增一个没有已存身份的 endpoint 且不带 key，
  断言写入被既有校验拒绝（400 + `needs api_key`），而不是静默继承。
- 断言用 DTO 的 `api_key_hint`（`providerKeyHint` 会把 `${ENV}` 解到 `.env` 的真实值），**不要**在测试里
  回显密钥原文之外的任何东西；`.env` 由测试自己写、自己销毁（`t.TempDir()`）。

## Done criteria

机器可检查，全部满足：

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/gateway` exit 0
- [ ] `gofmt -l pkg/gateway` 无输出
- [ ] 两个新测试 PASS
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` 与基线相比无新增 FAIL
- [ ] `grep -c "oldKeyByProvider" pkg/gateway/api_extra.go` 为 `0`
- [ ] `grep -c "func storedKeyForRow" pkg/gateway/api_extra.go` 为 `1`
- [ ] `git status --porcelain` 只显示 Scope 里的两个文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 预期，或 `handleProviders` 写分支的结构变了。
2. 基线命令本身跑不起来，拿不到失败清单。
3. 两个新测试在 Step 1 都直接 PASS（说明基线已改；重复实施会掩盖真实语义）。
4. `VerifyProvider("zhipuai")` 在该测试环境被拒（provider 目录变了）——换成本仓
   `pkg/process.VerifyProvider` 认可的任一 provider，并在报告里说明换了哪个。
5. 你发现"不带 api_key"并不等于"用户没动"（例如某个客户端总是发空串）——那是另一个语义问题，
   报告后停手，不要顺手改 `row.APIKey != nil` 的判定。
6. 移动 `stored` 之后 `providerListHasEntry` / `prevDefault` 那段出现行为变化（编译过但测试红）——
   先把那段读清楚再决定，读不清就停。

## Maintenance notes

- 谁将来给 providers 增加新的"行身份"维度（例如把 `params` 纳入身份），`storedKeyForRow` 与
  `providerEntryIdentity` 必须同时改——它们现在共享同一个身份串，这是故意的。
- 评审重点：这条路径上**永远不要**把 key 写进日志或错误串；`providerKeyHint` 只回末四位，
  新增的 `storedKeyForRow` 不返回错误、不带上下文，也不会打印。
- 已知限制（不在本计划内）：同一 provider 的两个 endpoint 用 `api_key_plain` 各写一把不同的 key 时，
  `.env` 的变量名由 provider 决定，第二个会覆盖第一个——这是 `ProviderKeyEnvMap` 的既有命名规则。
  真要支持"一 provider 多端点各自独立 key"，那是 env 命名方案的另一个计划。
