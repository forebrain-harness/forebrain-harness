# Plan 102：不带 provider 的模型列表，没有登录就不发网络请求（npm 仓库、opencode Console 都不再被无关用户打到）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。
>
> **Drift check（先跑）**：
> ```
> git diff --stat 55019a2 -- pkg/process/models.go pkg/process/models_test.go     # 预期无输出
> git hash-object pkg/turn/models.go pkg/turn/models_test.go | cut -c1-10          # 预期依次 1dc3857f6e、6a5bf1b3d0
> ```
> `pkg/turn/models.go` / `models_test.go` 在工作区里有 owner 未提交的改动（`defaultModelIndex` 等），所以用哈希；
> 哈希不同不等于必须停——对照下面 "Current state" 的摘录，符号与行为还在就继续，对不上才是 STOP 1。
> （工作区里 `pkg/gateway/*`、`pkg/config/*`、`pkg/run/*`、`pkg/tui/*`、`frontend/*` 的既有改动与本计划无关，不要碰、不要回滚。）

## Status

- **Priority**: P3
- **Effort**: S（两处各一个守卫 + 2 个测试）
- **Risk**: LOW（只让「没登录」的情形少发请求；有登录或显式指定 provider 的查询行为不变）
- **Depends on**: none（和计划 101 互补：本计划减少「打开页面就触发令牌刷新」的次数，但不依赖它）
- **Category**: perf（顺带少一次对外请求）
- **Planned at**: commit `55019a2` + 未提交工作区，2026-10-23

## Why this matters

Web 的 Providers 页每次打开都会调一次不带 provider 的模型列表（`ProvidersView.vue:717` 的
`forebrainApi.getModels({ limit: 500 })`，只为拿「服务商 → 模型」的联想），网关于是对**每个**用户做实时发现：

1. **ChatGPT**：`chatGPTModelsSource.ChatGPTModels`（`pkg/process/models.go:34`）先 `resolveVersion`——一次对 npm 仓库的 GET
   （`openai.ResolveClientVersion`，`pkg/llm/openai/client_version.go:45`）——**然后**才在 `Transport` 里发现没有登录文件而失败。
   没登录 ChatGPT 的用户每次打开页面白付一次 npm 往返（离线/受限网络下拖到超时）。（已有行为，不是这次分支引入的。）
2. **OpenCode Console**：`listModelCatalogLive` 的 Console 分支（`pkg/turn/models.go`）在 `provider == ""` 时无条件取目录，
   用字面量 `public` 令牌打 `https://opencode.ai/inference/v1/models`。同文件的 Go 订阅分支早有 `OpenCodeSignedIn()` 守卫
   （注释："asking without a credential spends a request to learn what the login file already says"），Console 分支没有。
   这是 `0a066cb` 引入的：每个用户每次打开页面都向 opencode.ai 发一个请求，而该目录对第三方客户端多半只会回 403/401（分支里自己的注释这么说）。
3. 三段实时发现**共用一个 10 秒** `context`（`api_extra.go` 的 `handleModelsList`），前一段慢，后一段就拿到一个快过期的 ctx。
4. 已登录用户的这次列表还会**顺带刷新令牌**（发现走与回合相同的 `Transport`）——和计划 101 的竞态叠加。

修法：每个实时来源在「本机没有它的登录」时不发请求；显式指定 `provider=opencode` 的查询不变。

## Current state

**ChatGPT 来源**（`pkg/process/models.go:34-43`）：

```go
func (s chatGPTModelsSource) ChatGPTModels(ctx context.Context) ([]openai.ModelInfo, error) {
	// The declared client version is resolved live on every discovery: it
	// names the newest released codex client, whose availability gates the
	// backend's model list.
	version, err := s.resolveVersion(ctx)
	if err != nil {
		return nil, err
	}
	return openai.FetchModels(ctx, &openai.Transport{Path: s.path}, s.baseURL, version)
}
```

`openai.Load(path)` 在文件缺失或不完整时返回 error；`openai.Transport` 的未登录错误文案是 `"ChatGPT login required: %w"`。

**列表合成**（`pkg/turn/models.go` 的 `listModelCatalogLive`，节选）：

```go
	if sources.opencode != nil && (provider == "" || provider == opencodeProvider) {
		// The Console catalog needs a Console credential: ...
		records, err := DiscoverOpenCodeModels(ctx, sources.opencode, false)
		status := ModelCatalogStatus{Provider: opencodeProvider, Source: ModelSourceOpenCodeAccount}
		...
	}
	if sources.opencode != nil && (provider == "" || provider == opencodeGoProvider) {
		status := ModelCatalogStatus{Provider: opencodeGoProvider, Source: ModelSourceOpenCodeAccount}
		if !sources.opencode.OpenCodeSignedIn() {
			status.Source = ""
			status.Error = "not signed in"
		} else { ... }
	}
```

`OpenCodeModelsSource.OpenCodeSignedIn()`（接口，`pkg/turn/models.go`）= 本机有 OpenCode 登录文件或 `OPENCODE_API_KEY`。

**现有测试（必须保持绿）**：
- `pkg/turn/models_test.go` 的 `TestListModelCatalogLiveOpenCodeConsoleScope`：`Provider: "opencode"` 显式查询、`signedIn` 为零值 `false`，仍要取到 Console 记录。
- `TestListModelCatalogLiveOpenCodeBlocksFollowEachOther`：`signedIn: true` 的不带 provider 查询，要返回 `["openai","chatgpt","opencode","opencode-go"]` 与 3 条 status。
- `pkg/process/models_test.go` 的 `TestDiscoverChatGPTModelsWithoutCredentialsErrors`：无凭据时错误含 `"ChatGPT login required"`；
  `TestChatGPTModelsSourceSurfacesVersionResolutionFailure`：**有**登录文件时版本解析失败要如实返回。
- 夹具：`turn` 里 `stubChatGPTSource{calls}`、`stubOpenCodeSource{signedIn, console, consoleCalls}`、`useStaticCatalog(t)`；
  `process` 里 `newChatGPTModelsSource(path, baseURL)`（字段 `resolveVersion` 可替换）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 构建 | `go build ./...` | exit 0（`ld: warning` 可忽略） |
| 目标包测试 | `go test -tags fts5 ./pkg/turn/ ./pkg/process/ -count=1` | 两个 `ok` |
| gofmt | `test -z "$(gofmt -l pkg/turn pkg/process)"` | exit 0 |
| vet | `go vet ./pkg/turn/ ./pkg/process/` | exit 0 |
| 包图快照 | `scripts/package-graph.sh && git diff --stat -- pkg/architecture/testdata/graph.json` | 只有 `pkg/turn`、`pkg/process` 的 `loc` 变化 |

## Scope

**In scope**：
- `pkg/process/models.go`、`pkg/process/models_test.go`
- `pkg/turn/models.go`、`pkg/turn/models_test.go`
- `pkg/architecture/testdata/graph.json`（只允许 `scripts/package-graph.sh` 重新生成）

**Out of scope**：
- `frontend/src/views/ProvidersView.vue` 的 `loadCatalog`——保持不带 provider；守卫在服务端，前端不改。
- `pkg/gateway/api_extra.go` 的 `handleModelsList`（共用 10 秒 ctx 的结构）——给每个来源单独预算是另一件事，这里不做。
- 已登录时的发现仍会触发令牌刷新——靠计划 101 让它安全，不在这里去掉。
- `DiscoverOpenCodeModels`、`DiscoverChatGPTModels` 的投影逻辑。

## Git workflow

- **不提交**。改动留在 dev 工作区。

## Steps

### Step 1：先写会红的测试

1. `pkg/process/models_test.go`，仿 `TestDiscoverChatGPTModelsWithoutCredentialsErrors`（`:132`）新增
   `TestChatGPTModelsSourceWithoutALoginDoesNotResolveTheVersion`：
   `src := newChatGPTModelsSource(filepath.Join(t.TempDir(), "auth.json"), "https://example.invalid")`，
   `src.resolveVersion` 换成一个计数并返回 `("0.156.1", nil)` 的闭包；调 `src.ChatGPTModels(ctx)`；
   断言 `require.ErrorContains(t, err, "ChatGPT login required")` 且计数为 **0**。**预期现在红**（计数是 1）。
2. `pkg/turn/models_test.go`，仿 `TestListModelCatalogLiveSkipsGoCatalogWithoutLogin` 新增
   `TestListModelCatalogLiveUnscopedSkipsConsoleWithoutLogin`：
   `opencodeSrc := &stubOpenCodeSource{signedIn: false, console: []opencode.ModelInfo{{ID: "big-pickle"}}}`，
   `listModelCatalogLive(ctx, modelSources{opencode: opencodeSrc}, ModelCatalogQuery{})`（不带 provider）；
   断言 `opencodeSrc.consoleCalls == 0`，`listing.Status` 里**没有** `Provider == "opencode"` 的项（Go 订阅那项照旧是 `"not signed in"`），
   `listing.Records` 里没有 `opencode/` 开头的记录。**预期现在红**。
   再加一个同文件的对照 `TestListModelCatalogLiveScopedConsoleStillFetchesWithoutLogin`：`ModelCatalogQuery{Provider: "opencode"}` 且 `signedIn: false` 时 `consoleCalls == 1`。（已有行为，护栏。）

**Verify**：`go test -tags fts5 ./pkg/process/ ./pkg/turn/ -run 'TestChatGPTModelsSourceWithoutALoginDoesNotResolveTheVersion|TestListModelCatalogLiveUnscopedSkipsConsoleWithoutLogin' -count=1 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'` →
**恰好这 2 个 `--- FAIL`**。

### Step 2：ChatGPT 来源先看登录再解析版本

`pkg/process/models.go` 的 `ChatGPTModels` 开头加：

```go
	// No login on this machine means nothing to discover. Resolving the client
	// version is a request to the npm registry, and the fetch would fail on the
	// missing credentials right after it — so ask for the login first. The
	// wording is the Transport's own, which callers already match on.
	if _, err := openai.Load(s.path); err != nil {
		return nil, fmt.Errorf("ChatGPT login required: %w", err)
	}
```

并在该文件的 import 里补 `"fmt"`。其余不动（版本解析、`FetchModels` 都在这之后）。

**Verify**：`go test -tags fts5 ./pkg/process/ -count=1` → `ok`（含 `TestChatGPTModelsSourceSurfacesVersionResolutionFailure`——它先写了登录文件，不受影响）。

### Step 3：Console 分支在不带 provider 时也要求有登录

`pkg/turn/models.go` 的 `listModelCatalogLive`，把 Console 分支的条件

```go
	if sources.opencode != nil && (provider == "" || provider == opencodeProvider) {
```

改成：

```go
	// A listing that names no provider asks every source the machine could
	// have an account on. The Console catalog is read with a public token, so
	// without this guard every unscoped listing — the web providers page opens
	// one each time — would contact opencode.ai for users who never signed in
	// to it. A query that names "opencode" is an explicit ask and still fetches.
	if sources.opencode != nil && (provider == opencodeProvider ||
		(provider == "" && sources.opencode.OpenCodeSignedIn())) {
```

分支体不动。

**Verify**：`go test -tags fts5 ./pkg/turn/ -count=1` → `ok`（Step 1 的两条变绿；`TestListModelCatalogLiveOpenCodeConsoleScope` 与 `TestListModelCatalogLiveOpenCodeBlocksFollowEachOther` 不变）。

### Step 4：收尾门禁

1. `scripts/package-graph.sh`，`git diff --stat -- pkg/architecture/testdata/graph.json`：只应有 `pkg/turn`、`pkg/process` 的 `loc` 变化。
2. `test -z "$(gofmt -l pkg/turn pkg/process)"` → exit 0。
3. `go build ./... && go vet ./pkg/turn/ ./pkg/process/` → exit 0。
4. `go test -tags fts5 ./pkg/turn/ ./pkg/process/ ./pkg/gateway/ -count=1` → 全 `ok`（网关的 `/api/models` 测试走这条路径）。

## Test plan

见 Step 1：3 个新测试（2 个先红、1 个护栏）。结构样板：`pkg/turn/models_test.go` 的 `TestListModelCatalogLiveSkipsGoCatalogWithoutLogin`；
`pkg/process/models_test.go` 的 `TestDiscoverChatGPTModelsWithoutCredentialsErrors`。

## Done criteria

- [ ] `go test -tags fts5 ./pkg/turn/ ./pkg/process/ ./pkg/gateway/ -count=1` 全 `ok`
- [ ] Step 1 的 2 个先红的测试现在 `PASS`，其余既有测试无一被修改
- [ ] `grep -n 'sources.opencode.OpenCodeSignedIn()' pkg/turn/models.go` 恰有 2 处命中（Console 分支的条件与 Go 订阅分支）
- [ ] `git diff --name-only -- pkg/architecture/testdata/graph.json` 只有这一个文件，且只含 loc 变化
- [ ] `git status --short` 里新增的改动只落在 In scope 列表的文件
- [ ] 没有任何 `git commit` / `git push` / 新分支

## STOP conditions

1. 摘录对不上现状（`ChatGPTModels`、Console 分支条件任一已被改过）。
2. 既有的 `TestListModelCatalogLiveOpenCodeConsoleScope` 或 `TestListModelCatalogLiveOpenCodeBlocksFollowEachOther` 变红——说明守卫误伤了显式 provider 或已登录的查询，回头检查条件，不要改这些测试。
3. 发现还有别的调用方依赖「不带 provider 的列表里一定带 Console 记录」（`grep -rn 'ListModelCatalogLive\|getModels(' pkg frontend/src` 逐个看）——停下报告，由 owner 决定。
4. 一个步骤的验证在合理修复后仍失败两次。

## Maintenance notes

- 行为变化要让 owner 知道：**未登录 OpenCode 的用户，在不带 provider 的列表里不再看到 `opencode/*`（Console 免费族）记录**；
  显式 `provider=opencode` 仍然取。Console 目录对第三方客户端本就多半不可用（见 `listModelCatalogLive` 里 Console 分支的注释），所以实际损失应当为零。
- 若日后别的 provider 也加「实时发现」，同样要先过「本机有它的登录」这道闸，再发请求。
- 共用 10 秒预算的问题（前一段慢、后一段饿死）仍在；真有人抱怨再给每个来源单独的 `context.WithTimeout`。
