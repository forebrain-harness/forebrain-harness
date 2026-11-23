# Plan 081：项目 MCP / LSP 的逐行“允许 / 拒绝”绑定到用户看到的那一条（指纹随请求回传，不一致则 409、什么都不记）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不建 worktree、不 commit、不 push、不 stash；改动留在当前工作区。
> 开工前先记快照，收尾时对比：`git status --short > "$TMPDIR/081-before.txt"`。
>
> **环境**（每个 shell 都要）：
> ```
> cd /Users/doudou/workspace/unionj-cloud/forebrain-harness
> export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
> export FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1
> ```
> 若 `go test` 报 `Package pdfium was not found in the pkg-config search path`，先跑 `make pdfium pdfium-fonts`。前端命令在 `frontend/` 下跑（`pnpm` 即 `corepack pnpm`）。
>
> **与 079 的先后**：本计划与 079 都改 `frontend/src/lib/api.ts`。079 当前是工作区里**未提交**的改动；你在同一个工作区里做，不要 `git checkout`/`git stash` 任何文件，只在现有内容上加。
>
> **Drift check（先跑）**：以下 6 条都必须各有 1 行输出，否则 STOP：
> ```
> grep -n 'func DecideProjectMCPConsentsFor(agentWorkspace string, launch safety.ProjectContext, decide, allowed \[\]string) error' pkg/process/mcp.go
> grep -n 'func DecideProjectServersFor(agentWorkspace, projectRoot, projectKey string, decide, allowed \[\]string) error' pkg/lsp/project.go
> grep -n 'func DecideProjectLSPConsentsFor(agentWorkspace string, launch safety.ProjectContext, decide, allowed \[\]string) error' pkg/process/mcp.go
> grep -n 'func mcpConsentPreviewRows' pkg/gateway/mcp_oauth_api.go
> grep -n 'func (s \*Server) handleProjectLSPConsent' pkg/gateway/api_extra.go
> grep -n 'projectMcpConsent(id: string, allow: string\[\], decide?: string\[\])' frontend/src/lib/api.ts
> ```

## Status

- **Priority**: P2（安全加固；需要“页面开着的时候项目文件被改”才触发，但被绑定的是命令执行的同意）
- **Effort**: M（Go 两处决定函数 + 网关两个处理器与行 + 前端两个组件 + 测试）
- **Risk**: LOW（只对网页逐行路径生效；启动提示 / 批量路径与旧调用者的语义不变）
- **Depends on**: none（建议在 079 提交之后再合并，因为同改 `api.ts`；可在工作区里直接叠加）
- **Category**: security
- **Planned at**: commit `3096298` + 工作区（079 未提交），2026-10-19

## Why this matters

项目里的 `.forebrain/mcp_servers.yaml` / `lsp_servers.yaml` 随检出而来，每个条目要用户确认一次才能启动；确认**按指纹**记录（命令、参数、环境、URL、头……任一改变就重新询问）。
网页项目空间的逐行“允许 / 拒绝”（077 新增的 `decide`）只把**条目名字**发给服务端；服务端在**收到请求的那一刻**才读文件、算指纹、记决定。
所以：用户在页面上看到的是条目 A 的摘要（`/bin/project-e`），页面开着期间文件被改了（agent 写了项目文件、`git pull`），点“允许”记下的是**改后的新命令**的指纹——用户同意的不是他看到的东西。终端启动提示的窗口只有几秒，网页页面可以开几个小时。
修法：行里带上指纹，请求把指纹送回，服务端在记录前逐条核对，**有任何一条与当前文件不一致就整单拒绝（409），一条都不记**，前端提示并刷新行。

## Current state

- `pkg/process/mcp.go`
  - `:235 DecideProjectMCPConsentsFor(agentWorkspace, launch, decide, allowed []string) error`：读 `entries, _ := mcp.LoadProjectMCPServers(root)`；对 `decide` 里点名的条目，用**当前**文件算 `mcp.ServerFingerprint(srv)` 并 `consents.Decide(projectKey, srv.Name, fp, decision, now)`，最后 `mcp.SaveProjectConsents`。点名但文件里已不存在的名字被**静默跳过**。
  - `:170 PendingProjectMCPConsents` / `:274 DeclinedProjectMCPConsents` 返回的 `mcp.PendingProjectConsent{Name, Fingerprint, Summary}` 已带指纹（`pkg/mcp/project_consent.go`）。
  - `:323-332 DecideProjectLSPConsentsFor` 转调 `lsp.DecideProjectServersFor`。
- `pkg/lsp/project.go:417 DecideProjectServersFor(agentWorkspace, projectRoot, projectKey string, decide, allowed []string) error`：同样在调用时读文件、`ProjectServerFingerprint(entries[id])`。`PendingProjectServer.Fingerprint` 是 `json:"-"`（`:295`），已允许 / 已拒绝的条目在 `ResolveProjectServers` 里只以 id 列出。
- `pkg/gateway/mcp_oauth_api.go`
  - `:398-423` 分页视图拼行：待确认行与已拒绝行来自 `mcpConsentPreviewRows(items, launch, flag)`（`:464` 起），已允许的项目行来自 `mcpPreviewRow(srv)` 再加 `allowed:true`。**这些行都没有指纹**。
  - `:487 handleProjectMCPConsent`：body `{allow, decide?}`；`decide != nil` 走 `process.DecideProjectMCPConsentsFor`，否则走批量 `DecideProjectMCPConsents`（启动提示语义）。
- `pkg/gateway/api_extra.go`
  - `:7330-7400`（`handleProjectLSPPreview` 一带）：分页时 `lspRow{ID, Summary, Status}` 由 `process.InspectProjectLSP` 的 pending / allowed / denied 拼出，**没有指纹**。
  - `:7415 handleProjectLSPConsent`：与 MCP 同形，`decide != nil` 走 `process.DecideProjectLSPConsentsFor`。
  - 路由注册：`pkg/gateway/api_extra_test.go:1390-1392` 的测试夹具里能看到 `projects.Post("/:id/mcp/consent", s.handleProjectMCPConsent)`。
- 前端
  - `frontend/src/lib/api.ts:3215 projectMcpConsent(id, allow, decide?)` 与 `:3246 projectLspConsent`：用 `postJson`，**会对请求体做 snake_case 键转换**（`:1859`）——所以**不能用以条目名为键的对象**传指纹（名字 `my-server` 会被改成 `my_server`）；用 `[{name, fingerprint}]` 数组，值不会被改。
  - `frontend/src/components/mcp/McpServersTable.vue:447 decide(row, allow)` → `projectMcpConsent(projectId, allow ? [row.name] : [], [row.name])`；`frontend/src/components/project/ProjectLsp.vue:214 consent(id, allow)`，模板 `:80 :89 :102` 调 `consent(row.id, …)`。
  - 行类型：`api.ts:533 McpServerRow`（已有 `pending/allowed/declined/summary` 之类的标志）、`api.ts:883 ProjectLspRow {id, summary, status}`。错误展示统一用 `getErrorMessage(cause)`（`api.ts:8`），它读响应体的 `error` / `message` 或纯文本。
- 既有测试要同步改：`pkg/process/mcp_test.go:122/142/181`（`DecideProjectMCPConsentsFor` 调用）、`:460-480`（`DecideProjectLSPConsentsFor`）；`pkg/lsp/project_test.go:262/288`（`DecideProjectServersFor`）；`pkg/gateway/api_extra_test.go:6318 TestProjectMCPConsentDecideOnlyTouchesNamedEntries`、`:3960` 一带的 LSP decide 用例；前端 `McpServersTable.test.ts`、`ProjectLsp.test.ts`、`api.test.ts`。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| Go 目标包 | `go test -tags fts5 ./pkg/process ./pkg/lsp ./pkg/mcp ./pkg/gateway ./cmd/forebrain -count=1` | `ok` ×5 |
| 前端单测 | `cd frontend && pnpm test` | 全部通过 |
| 类型检查 | `cd frontend && npx vue-tsc --noEmit -p tsconfig.json` | exit 0 |
| e2e（只跑相关） | `FOREBRAIN_E2E_SPEC="e2e/mcp-pages.spec.ts e2e/project-space-tables.spec.ts" scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |
| vet / gofmt | `go vet ./pkg/... ./cmd/...`；`gofmt -l cmd pkg third_party` | 无输出 |
| 包图 | `scripts/package-graph.sh && git diff --exit-code -- pkg/architecture/testdata/graph.json` | 无差异 |

## Scope

**In scope**：
- `pkg/process/mcp.go`、`pkg/process/mcp_test.go`
- `pkg/lsp/project.go`、`pkg/lsp/project_test.go`
- `pkg/gateway/mcp_oauth_api.go`、`pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`
- `frontend/src/lib/api.ts`、`frontend/src/lib/api.test.ts`
- `frontend/src/components/mcp/McpServersTable.vue`、`McpServersTable.test.ts`
- `frontend/src/components/project/ProjectLsp.vue`、`ProjectLsp.test.ts`
- `frontend/src/locales/index.ts`（只加下面点名的 1 个键）

**Out of scope**：
- 终端启动提示（`cmd/forebrain/mcp_consent.go`）与批量 `DecideProjectMCPConsents` / `DecideProjectLSPConsents`：语义不变。
- 指纹算法 `ServerFingerprint` / `ProjectServerFingerprint`：不改。
- `pkg/gateway/dist`（构建产物，发布流程重建）；`frontend/e2e/*` 的种子请求用的是批量路径（`mcp-pages.spec.ts:97`），不需要改。
- 不给 `InspectProjectLSP` 加返回值（它有 6 个调用点，见 Step 1c，改用新函数）。

## Steps

### Step 0：基线

记快照；跑 Drift check 的 6 条 grep；跑 `go test -tags fts5 ./pkg/process ./pkg/lsp ./pkg/mcp ./pkg/gateway ./cmd/forebrain -count=1` 与 `cd frontend && pnpm test`。
**Verify**: grep 各 1 行；测试全绿。否则 STOP。

### Step 1：Go 核心——决定函数核对指纹

**1a** `pkg/process/mcp.go`：
- 新增（放在 `DecideProjectMCPConsentsFor` 上方）：
  ```go
  // StaleConsentError refuses a per-entry decision whose entry is no longer what
  // the operator was shown: the project file changed (or lost the entry) between
  // the page listing it and the click. Nothing is recorded when it is returned.
  type StaleConsentError struct{ Names []string }

  func (e *StaleConsentError) Error() string {
      return "project entries changed since they were listed: " + strings.Join(e.Names, ", ")
  }

  // StaleNames lists the entries whose fingerprint no longer matches.
  func (e *StaleConsentError) StaleNames() []string { return e.Names }
  ```
- 签名改为 `DecideProjectMCPConsentsFor(agentWorkspace string, launch safety.ProjectContext, decide, allowed []string, fingerprints map[string]string) error`。规则：
  - `fingerprints == nil` → **不核对**（内部 / 旧调用者，保持原行为）。
  - 非 nil → 先对 `decide` 里的**每个**名字（`strings.ToLower(strings.TrimSpace(..))`）核对：文件里（`entries`）找不到该名字，或 `mcp.ServerFingerprint(srv)` 与 `fingerprints[name]` 不相等（含 map 里没有该名字）→ 收集到 stale 列表。**全部核对完**，有任何 stale → `return &StaleConsentError{Names: stale}`，**此时一行决定都不写、`SaveProjectConsents` 都不调**。无 stale 才进入原有的逐条 `consents.Decide` 循环。
  - 核对与记录必须用**同一份** `entries`（不要核对时读一次、记录时再读一次）。
- `pkg/process/mcp.go` 里 `DecideProjectLSPConsentsFor` 同样加 `fingerprints map[string]string` 参数并原样传给 `lsp.DecideProjectServersFor`。
- 新增 `ProjectLSPFingerprints(agentWorkspace string, launch safety.ProjectContext) map[string]string`：`root := safety.TrustedRoot(launch)`，为空返回 nil；否则 `entries, _ := lsp.LoadProjectServers(root)`，返回 `id → lsp.ProjectServerFingerprint(entries[id])`（键用 id 原样，与 `InspectProjectLSP` 给出的 id 一致）。

**1b** `pkg/lsp/project.go`：在 `DecideProjectServersFor` 旁新增同形的 `StaleConsentError`（同样的 `Error()` / `StaleNames()`，文案里 `entries` 换成 `language-server entries`），签名加 `fingerprints map[string]string`，规则同 1a（`nil` 不核对；核对用该函数自己读到的 `entries`，比较 `ProjectServerFingerprint(entries[id])`；id 的归一化沿用函数里 `decideSet` 的小写 + trim）。两个包各自一份错误类型，**不要**为此新建共享包。

**1c** 更新既有调用点——只在原参数列表后补 `, nil`（保持这些测试的原语义）：`pkg/process/mcp_test.go` 里 `DecideProjectMCPConsentsFor`（`:128 :156 :181`）与 `DecideProjectLSPConsentsFor`（`:480`），`pkg/lsp/project_test.go` 里 `DecideProjectServersFor`（`:271 :304` 及同文件其余）。
用 `grep -rn "DecideProjectMCPConsentsFor(\|DecideProjectLSPConsentsFor(\|DecideProjectServersFor(" pkg cmd` 列全，确认除网关处理器（Step 2 改）外都已补上。

**Verify**: `go build -tags fts5 ./pkg/... ./cmd/...` 此时只应剩网关两个处理器的编译错误；`go test -tags fts5 ./pkg/process ./pkg/lsp -count=1` → `ok`。

### Step 2：网关——行带指纹、请求核对、409

**2a MCP 行**（`pkg/gateway/mcp_oauth_api.go`）：
- `mcpConsentPreviewRows`：每一行加 `row["fingerprint"] = item.Fingerprint`（待确认与已拒绝都经过这里）。
- 分页视图里已允许的项目行（`:402-409` 的循环，`mcpPreviewRow(srv)` + `allowed`）加 `row["fingerprint"] = mcp.ServerFingerprint(srv)`。
- 其它层（agent / global）的行**不加**（它们没有同意）。

**2b MCP 处理器**（`handleProjectMCPConsent`）：body 结构加
```go
Fingerprints []struct {
    Name        string `json:"name"`
    Fingerprint string `json:"fingerprint"`
} `json:"fingerprints"`
```
`body.Decide != nil` 分支：
1. 把 `Fingerprints` 整理成 `map[string]string`（键 = 小写 trim 的 name）；`*body.Decide` 里**每个**名字必须在 map 里有非空指纹，否则 `400`，文案 `fingerprint required for "<name>"`（`decide` 为空列表时不要求）。
2. 调 `process.DecideProjectMCPConsentsFor(workspace, launch, *body.Decide, body.Allow, fps)`，`fps` 非 nil（即使为空也传空 map 而不是 nil，确保走核对）。
3. 错误处理：`var stale interface{ StaleNames() []string }; errors.As(err, &stale)` → `409`，响应体 JSON `{"error": "项目文件已变化，请核对后重新确认 / The project file changed; review and confirm again", "code": "consent_stale", "stale": [...]}`；其余错误保持原来的 500。
批量路径（`decide == nil`）一字不动。

**2c LSP 行与处理器**（`pkg/gateway/api_extra.go`）：
- 分页行 `lspRow` 加 `Fingerprint string \`json:"fingerprint"\``；用 `fps := process.ProjectLSPFingerprints(workspace, launch)` 给 pending / allowed / denied 三种行按 id 填（pending 也用同一张表，保证三种行口径一致）。
- `handleProjectLSPConsent` 与 2b 同样处理（同一份 400 / 409 约定，同一份文案）。可以把“整理 fingerprints + 校验 + 映射 409”抽成包内一个小函数供两个处理器共用（放在 `mcp_oauth_api.go`，不要新建文件）。

**Verify**: `go build -tags fts5 ./pkg/... ./cmd/...` 通过；`go vet ./pkg/gateway` 无输出。

### Step 3：前端

1. `frontend/src/lib/api.ts`
   - `McpServerRow` 加 `fingerprint?: string`；`ProjectLspRow` 加 `fingerprint: string`。
   - `projectMcpConsent(id, allow, decide?, fingerprints?: Array<{ name: string; fingerprint: string }>)`：`decide` 存在时 body 为 `{ allow, decide, fingerprints }`；不存在时仍是 `{ allow }`。`projectLspConsent` 同形。更新两处注释，写明为什么是数组（键转换会改名字）。
2. `frontend/src/components/mcp/McpServersTable.vue` 的 `decide(row, allow)`：传 `[{ name: row.name, fingerprint: row.fingerprint ?? '' }]`。`catch` 里：先 `writeError.value = getErrorMessage(cause)`；若 `(cause as AxiosError)?.response?.status === 409`，**再** `await load()`（让行带着新摘要与新指纹重新出现）。成功路径不变。
3. `frontend/src/components/project/ProjectLsp.vue`：`consent(id, allow)` 改为 `consent(row: ProjectLspRow, allow: boolean)`，传 `[{ name: row.id, fingerprint: row.fingerprint }]`；模板 `:80 :89 :102` 的 `consent(row.id, …)` 改为 `consent(row, …)`；`catch` 同 2（409 时 `await reload()`）。
4. 文案：不新增前端 i18n 键——错误文本就是服务端 409 响应里的 `error`（双语一句，沿用 `pkg/gateway/server.go:169` 的“中文 / English”先例）。（`locales/index.ts` 因此**不改**；如果你发现必须新增键，STOP 报告。）

**Verify**: `cd frontend && npx vue-tsc --noEmit -p tsconfig.json` → exit 0。

### Step 4：测试

**Go**（范例：`pkg/process/mcp_test.go:122/142` 的项目夹具、`pkg/gateway/api_extra_test.go:6318` 的 `newMCPPreviewFixture` / `doJSON` / `mcpServerRow`）：

1. `pkg/process/mcp_test.go`
   - `TestDecideProjectMCPConsentsForRefusesAChangedEntry`：建项目夹具并取得条目 `one` 的当前指纹 fp1；改写项目文件里 `one` 的 `command`；用 fp1 调 `DecideProjectMCPConsentsFor(…, []string{"one"}, []string{"one"}, map[string]string{"one": fp1})` → 返回 `*StaleConsentError`，`StaleNames()==["one"]`，且 `mcp.LoadProjectConsents` 读出的决定里**没有** `one`；用新指纹再调 → 成功并记下 allow。
   - 再加两个小用例：多名字里只有一个过期时**全部不记**（原子性）；点名的名字在文件里已不存在 → 也算 stale。
   - `TestDecideProjectLSPConsentsForRefusesAChangedEntry`：LSP 同上（范例 `:460`）。
   - `TestProjectLSPFingerprints`：不受信任的 launch 返回 nil；受信任时每个 id 有非空指纹，且与 `lsp.ProjectServerFingerprint` 一致。
2. `pkg/lsp/project_test.go`：`TestDecideProjectServersForRefusesAChangedEntry`（范例 `:262`）。
3. `pkg/gateway/api_extra_test.go`
   - 更新 `TestProjectMCPConsentDecideOnlyTouchesNamedEntries` 与 LSP 的 decide 用例：请求里的 `decide` 现在必须带 `fingerprints`，指纹从**列表行**里读（`row["fingerprint"]`），这同时证明行带了指纹。
   - 新增 `TestProjectMCPConsentDecideRefusesAStaleFingerprint`：取行指纹 → 改写项目文件里该条目 → POST decide → `409`，响应 `code == "consent_stale"`、`stale` 含该名；再 GET 列表，该条目仍是 pending（什么都没记）；用刷新后的指纹再 POST → `200`。
   - 新增 `TestProjectMCPConsentDecideRequiresFingerprints`：`decide` 非空而 `fingerprints` 缺失 → `400`。
   - LSP 各一份（`409` 与 `400`）。
   - 保留（不改）`TestProjectMCPConsentWithoutDecideKeepsDecidingAll`：批量路径不带指纹照常工作。

**前端**（范例：同文件里现有 `decide` / `consent` 用例）：
- `api.test.ts`：`projectMcpConsent` / `projectLspConsent` 带 `decide` 时请求体含 `fingerprints: [{name, fingerprint}]`，且**名字里的连字符 / 大写不被改写**（用 `my-Server` 之类的名字断言原样）；不带 `decide` 时没有 `fingerprints` 键。
- `McpServersTable.test.ts`：点“允许”发出的请求带该行指纹；服务端回 409 时显示服务端文案并**再请求一次列表**。
- `ProjectLsp.test.ts`：同上。

**Verify**: `go test -tags fts5 ./pkg/process ./pkg/lsp ./pkg/mcp ./pkg/gateway ./cmd/forebrain -count=1` → `ok` ×5；`cd frontend && pnpm test` → 全绿，新用例在列。

### Step 5：收口

1. `go vet ./pkg/... ./cmd/...`、`gofmt -l cmd pkg third_party` 无输出；`scripts/package-graph.sh && git diff --exit-code -- pkg/architecture/testdata/graph.json` 无差异（本计划不加包，不应有变化）。
2. e2e（相关两份）：`FOREBRAIN_E2E_SPEC="e2e/mcp-pages.spec.ts e2e/project-space-tables.spec.ts" scripts/acceptance/web_e2e.sh` → `web e2e: PASS`。它们走真 gateway，覆盖“页面点允许 / 拒绝 / 撤回”这条链路在带指纹之后仍然通。
3. 快照对比：`git status --short > "$TMPDIR/081-after.txt"; diff "$TMPDIR/081-before.txt" "$TMPDIR/081-after.txt"` → **无输出**（没有新增 / 删除文件）；`git diff --stat` 里的文件都在 Scope 内。

## Test plan

见 Step 4。红绿证据：`TestProjectMCPConsentDecideRefusesAStaleFingerprint` 与进程层的 `…RefusesAChangedEntry` 在 Step 1/2 之前（`fingerprints` 参数还不存在）无法编译；为取得“行为上的红”，可先只加 `fingerprints` 参数而不加核对逻辑，这两个用例必须因“决定被记下了”而失败，加上核对后通过。报告里附这一对输出。

## Done criteria

- [ ] `go test -tags fts5 ./pkg/process ./pkg/lsp ./pkg/mcp ./pkg/gateway ./cmd/forebrain -count=1` 全 `ok`
- [ ] `cd frontend && pnpm test` 全绿；`npx vue-tsc --noEmit -p tsconfig.json` exit 0
- [ ] e2e 两份 spec `web e2e: PASS`
- [ ] 新增用例存在且通过：进程层 MCP / LSP 各 1 + 原子性 + 条目消失；`ProjectLSPFingerprints`；网关 MCP / LSP 各 409 与 400；前端 3 处
- [ ] 批量路径（`TestProjectMCPConsentWithoutDecideKeepsDecidingAll`、`cmd/forebrain` 里的启动提示用例）未改且通过
- [ ] `grep -rn "DecideProjectMCPConsentsFor(\|DecideProjectLSPConsentsFor(\|DecideProjectServersFor(" pkg cmd` 的每个调用都带第 5 / 第 6 个参数
- [ ] `go vet`、`gofmt`、包图 `graph.json` 无差异；与开工前快照比较没有 Scope 外变化；没有 commit / 分支 / worktree

## STOP conditions

- Drift check 任何一条无输出，或 Current state 的摘录与工作区不符。
- 需要改指纹算法、启动提示、或 Scope 外文件。
- 网关夹具里取不到行的 `fingerprint`（说明行没有走 2a / 2c 修改的路径）——先查路径，不要在测试里自己算指纹来绕过。
- e2e 失败且原因是“点允许后列表没变化”：多半是前端漏传指纹导致 400 / 409，先查请求体，不要放宽服务端校验。
- 任一步验证连续两次修复后仍失败。

## Maintenance notes

- 今后任何“按名字改同意”的新接口都应带指纹；`nil` 指纹 map 只留给进程内的启动提示 / 批量路径，不要从 HTTP 层传 nil。
- 指纹随行下发，意味着 API 响应里多了一个不含秘密的哈希（`ServerFingerprint` 是对规范化条目的 sha256；条目里的 env / headers 值已在行里脱敏，哈希本身不可逆）。Review 时确认 `fingerprint` 没有被放进未脱敏的字段。
- 终端侧仍是“看到摘要 → 几秒内回答”，窗口很小；若将来终端也出现长时间挂起的确认界面，同样需要把指纹带进决定。
- Review 重点：409 分支是否真的“一条都没记”（读 `SaveProjectConsents` 是否在 stale 返回之前被调用）；前端是否在 409 后刷新了行。
