# Plan 160：项目服务器确认必须先校验两类指纹、再落任何一条

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func DecideProjectServers" pkg/process/mcp.go` 为 `1`；
> `grep -c "func ProjectLSPFingerprints" pkg/process/mcp.go` 为 `1`；
> `grep -c "func splitProjectServerChoices" pkg/process/mcp.go` 为 `1`；
> `grep -c "refused whole with 409 and nothing recorded" pkg/gateway/mcp_oauth_api.go` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/process/mcp.go pkg/process/mcp_test.go pkg/gateway/mcp_oauth_api.go` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/tui ./pkg/gateway -count=1`（超时给足 900 s）。

## Status

- **Priority**: P2（用户被告知"什么都没记"，实际已写了一半；且新决策不生效）
- **Effort**: S（一个校验函数 + 调用点 + 2 个测试）
- **Risk**: LOW（只把"先写 MCP 再验 LSP"改成"先验两类再写"；两个 surface 的 stale 处理都已存在）
- **Depends on**: 无
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: bug（契约与实现相反 / 决策不生效）

## Why this matters

网页的确认接口在自己的注释里承诺了**整体原子性**：

```go
// handleProjectServersConsent records the session dialog's answer: every entry
// it lists is decided — checked means allow, unchecked means deny — and
// always_allow turns the project's switch on as well. Each choice carries the
// fingerprint the dialog showed, so a project file that moved under the
// operator is refused whole with 409 and nothing recorded; the surface reloads
// and asks again about what is still pending.
```

引擎侧的注释也这么写（"a stale LSP list leaves the MCP answers in place"——但把它写成"调用方会再问，
所以没问题"）：

```go
// DecideProjectServers records the answer. Each kind is bound to the
// fingerprints the operator was shown: an entry that changed since makes that
// kind's call fail with its *StaleConsentError before anything of that kind
// is recorded (MCP is recorded first; a stale LSP list leaves the MCP answers
// in place, and the caller asks again about what is still pending). The switch
// is turned on last, only when both kinds were recorded.
```

实现就是那个顺序：`if len(d.MCP) > 0 { DecideProjectMCPConsentsFor(...) }` **然后** `if len(d.LSP) > 0 {...}`。
于是"LSP 文件在弹窗与点击之间变了"时：

1. MCP 的允许/拒绝**已经落盘**；
2. `DecideProjectServers` 返回 `*lsp.StaleConsentError`；
3. 处理函数的错误分支直接 `writeStaleConsent(w, …)` 返回——**跳过了它下面的 `pool.RebindAgent`**。

结果：界面告诉用户"整条被拒、什么都没记"，而磁盘上多了一份没人回滚的决策，且池里的 runner 没有重建，
那份决策在某个无关操作触发 rebind 之前**既不回滚也不生效**。两个 surface 后续"只问仍 pending 的项"时，
用户看到的是与自己刚才的操作对不上的列表。

要做成：**两类指纹先用同一次读校验，任何一条过期就一条也不写**。这样处理函数那条早退分支重新变成正确的
（确实什么都没记），而 `RebindAgent` 被跳过也就不再是问题。

## Current state

`pkg/process/mcp.go:396-424`：

```go
// DecideProjectServers ... （注释见上）
func DecideProjectServers(agentWorkspace string, launch safety.ProjectContext, d ProjectServersDecision) error {
	root := safety.TrustedRoot(launch)
	if root == "" {
		return nil
	}
	if len(d.MCP) > 0 {
		names, allowed, fps := splitProjectServerChoices(d.MCP)
		if err := DecideProjectMCPConsentsFor(agentWorkspace, launch, names, allowed, fps); err != nil {
			return err
		}
	}
	if len(d.LSP) > 0 {
		names, allowed, fps := splitProjectServerChoices(d.LSP)
		if err := DecideProjectLSPConsentsFor(agentWorkspace, launch, names, allowed, fps); err != nil {
			return err
		}
	}
	if d.AlwaysAllow {
		return mcp.SetProjectAutoAllow(agentWorkspace, memory.ProjectKey(root), true, time.Now())
	}
	return nil
}
```

单类内部的"先校验后写"已经是正确的（`DecideProjectMCPConsentsFor` `:288-315`、
`lsp.DecideProjectServersFor` `pkg/lsp/project.go:489-514`：都先用**同一次读**的 fingerprint 表比对，
`stale` 非空就 `return &StaleConsentError{...}`，一个字节都不写）。缺的只是**跨类的顺序**。

可复用的现成材料（不要重写它们的比对规则）：

- MCP 当前指纹：`mcp.LoadProjectMCPServers(root)` + `mcp.ServerFingerprint(srv)`，名字小写化
  （与 `DecideProjectMCPConsentsFor` 完全一致）。
- LSP 当前指纹：`ProjectLSPFingerprints(agentWorkspace, launch)`（`pkg/process/mcp.go:485`，
  返回 `id → fingerprint`，未受信任的项目返回 nil）。
- 决策项：`ProjectServerChoice{Name, Fingerprint string, Allow bool}`（`:382-386`）。
- 错误类型：`*StaleConsentError`（`:247-254`，`StaleNames() []string`）。网页处理函数用的是
  `var stale interface{ StaleNames() []string }` + `errors.As`，两个包的 stale 类型都满足它，
  所以返回本包的 `*StaleConsentError` **不需要改网关**。

处理函数（`pkg/gateway/mcp_oauth_api.go:772` 起）的错误分支在 `:812-818`（`writeStaleConsent` 后 return），
`pool.RebindAgent` 在 `:820-826`。

终端侧（`pkg/tui/project_servers.go:219-233`）已经按 stale 重问，最多 3 轮：

```go
		err = c.DecideProjectServers(items, answer)
		var stale interface{ StaleNames() []string }
		if errors.As(err, &stale) {
			continue
		}
```

## Scope

**In scope（只改这些）**：
- `pkg/process/mcp.go`（新增 `staleProjectServerChoices` / `staleChoiceNames`；`DecideProjectServers` 先校验；
  改写它那段已经过期的注释）
- `pkg/process/mcp_test.go`（新增 2 个测试）

**Out of scope（看着相关也不要动）**：
- `pkg/gateway/mcp_oauth_api.go`（修好后它那条早退分支是正确的；**不要**为了"部分已记录"去加新错误码）
- `pkg/lsp/project.go` 的单类校验与 `lsp.StaleConsentError`
- `DecideProjectMCPConsentsFor` / `DecideProjectLSPConsentsFor` 的内部逻辑
- `mcp.SetProjectAutoAllow` 的时机（它已经是最末尾，保持不变）
- `pkg/tui`（它的 stale→重问循环不用改）
- 网页前端

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run '^TestDecideProjectServers' -count=1 -v` | 新用例 PASS |
| 本包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -count=1` | 与基线相比无新增 FAIL |
| 两个 surface | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/gateway -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/process` | exit 0 |
| gofmt | `gofmt -l pkg/process` | 无输出 |

## Steps

### Step 1：先写会红的测试

放在 `pkg/process/mcp_test.go`，用文件里现成的 helper：`trustedVCSProject(t, home, dir)`（`:526` 用法）、
`writeProjectMCP(t, dir, body)`（`:34`）、`pendingMCPFingerprint(t, ws, launch, "one")`、
`writeProjectLSPFile(t, dir, body)`（`:789`）、`pendingLSPFingerprint(t, ws, launch, "one")`。

```go
// The confirmation is one answer about one dialog: when a language-server entry
// moved under the operator, the MCP half of the same answer must not be
// recorded either — the surface told the user nothing was.
func TestDecideProjectServersRecordsNothingWhenOneKindWentStale(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	launch := trustedVCSProject(t, home, dir)
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: one\n    transport: stdio\n    command: /bin/one\n")
	writeProjectLSPFile(t, dir, "servers:\n  one: {command: /bin/one}\n")
	mcpFP := pendingMCPFingerprint(t, ws, launch, "one")
	lspFP := pendingLSPFingerprint(t, ws, launch, "one")

	// 只有 LSP 那份文件变了：MCP 的指纹仍然有效。
	writeProjectLSPFile(t, dir, "servers:\n  one: {command: /bin/changed}\n")

	err := DecideProjectServers(ws, launch, ProjectServersDecision{
		MCP: []ProjectServerChoice{{Name: "one", Fingerprint: mcpFP, Allow: true}},
		LSP: []ProjectServerChoice{{Name: "one", Fingerprint: lspFP, Allow: true}},
	})
	var stale *StaleConsentError
	if !errors.As(err, &stale) {
		t.Fatalf("err = %v, want *StaleConsentError", err)
	}
	if !slices.Equal(stale.StaleNames(), []string{"one"}) {
		t.Fatalf("stale names = %v, want [one]", stale.StaleNames())
	}
	if _, statErr := os.Stat(mcp.ProjectConsentPath(ws)); !os.IsNotExist(statErr) {
		t.Fatalf("a stale language-server list must leave the MCP consents unwritten: %v", statErr)
	}
}

// Both kinds fresh is the ordinary case and must still record both.
func TestDecideProjectServersRecordsBothKindsWhenNothingMoved(t *testing.T) {
	home := t.TempDir()
	ws := t.TempDir()
	dir := t.TempDir()
	launch := trustedVCSProject(t, home, dir)
	writeProjectMCP(t, dir, "mcp_servers:\n  - name: one\n    transport: stdio\n    command: /bin/one\n")
	writeProjectLSPFile(t, dir, "servers:\n  one: {command: /bin/one}\n")

	err := DecideProjectServers(ws, launch, ProjectServersDecision{
		MCP: []ProjectServerChoice{{Name: "one", Fingerprint: pendingMCPFingerprint(t, ws, launch, "one"), Allow: true}},
		LSP: []ProjectServerChoice{{Name: "one", Fingerprint: pendingLSPFingerprint(t, ws, launch, "one"), Allow: true}},
	})
	if err != nil {
		t.Fatalf("DecideProjectServers: %v", err)
	}
	if _, statErr := os.Stat(mcp.ProjectConsentPath(ws)); statErr != nil {
		t.Fatalf("the MCP decision must be recorded: %v", statErr)
	}
	if _, statErr := os.Stat(lsp.ProjectConsentPath(ws)); statErr != nil {
		t.Fatalf("the language-server decision must be recorded: %v", statErr)
	}
}
```

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run '^(TestDecideProjectServersRecordsNothingWhenOneKindWentStale|TestDecideProjectServersRecordsBothKindsWhenNothingMoved)$' -count=1 -v`
→ 第一个 FAIL（MCP 的 consent store 已被写），第二个 PASS。若第一个也 PASS，STOP 3。

### Step 2：两类先校验、再落盘

在 `pkg/process/mcp.go` 的 `DecideProjectServers` **之前**新增两个函数：

```go
// staleProjectServerChoices lists every entry this answer names whose current
// fingerprint is not the one the surface showed — both kinds, from one read of
// each file. Nothing is written before this returns empty, so a dialog whose
// project files moved under the operator records no decision at all: that is
// what both surfaces tell the user a 409 means.
func staleProjectServerChoices(agentWorkspace string, launch safety.ProjectContext, d ProjectServersDecision) []string {
	root := safety.TrustedRoot(launch)
	var stale []string
	if len(d.MCP) > 0 {
		entries, _ := mcp.LoadProjectMCPServers(root)
		current := make(map[string]string, len(entries))
		for _, srv := range entries {
			current[strings.ToLower(strings.TrimSpace(srv.Name))] = mcp.ServerFingerprint(srv)
		}
		stale = append(stale, staleChoiceNames(d.MCP, current)...)
	}
	if len(d.LSP) > 0 {
		stale = append(stale, staleChoiceNames(d.LSP, ProjectLSPFingerprints(agentWorkspace, launch))...)
	}
	return stale
}

// staleChoiceNames compares one kind's choices with the fingerprints the files
// hold now. A name the file no longer has is stale in the same way a moved
// fingerprint is: the operator confirmed an entry as it was shown.
func staleChoiceNames(choices []ProjectServerChoice, current map[string]string) []string {
	var stale []string
	seen := make(map[string]struct{}, len(choices))
	for _, choice := range choices {
		key := strings.ToLower(strings.TrimSpace(choice.Name))
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if fp, ok := current[key]; !ok || fp != choice.Fingerprint {
			stale = append(stale, key)
		}
	}
	return stale
}
```

然后把 `DecideProjectServers` 改成：

```go
func DecideProjectServers(agentWorkspace string, launch safety.ProjectContext, d ProjectServersDecision) error {
	root := safety.TrustedRoot(launch)
	if root == "" {
		return nil
	}
	// Both kinds are checked before either is recorded: the dialog is one
	// answer about one file state, and a 409 promises the user that nothing of
	// it was kept.
	if stale := staleProjectServerChoices(agentWorkspace, launch, d); len(stale) > 0 {
		return &StaleConsentError{Names: stale}
	}
	if len(d.MCP) > 0 {
		... 原样
	}
	...
}
```

并把该函数上方那段注释里 "(MCP is recorded first; a stale LSP list leaves the MCP answers in place, and the
caller asks again about what is still pending)" 换成新的契约：**两类先校验，任一条过期则一条也不落**。

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run '^(TestDecideProjectServersRecordsNothingWhenOneKindWentStale|TestDecideProjectServersRecordsBothKindsWhenNothingMoved)$' -count=1 -v`
→ 两个 PASS。

### Step 3：本包与两个 surface 的回归

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/tui ./pkg/gateway -count=1`
→ 与基线相比无新增 FAIL。既有的
`TestDecideProjectMCPConsentsForRefusesAChangedEntry`（`:526`）、`…StaleRefusalRecordsNothing`（`:562`）、
`…MissingEntryIsStale`（`:591`）、`TestDecideProjectLSPConsentsForRefusesAChangedEntry`（`:615`）
必须仍然 PASS——它们证明单类语义没被改坏。

## Test plan

- 新增 `TestDecideProjectServersRecordsNothingWhenOneKindWentStale`：MCP 指纹有效、LSP 指纹过期 →
  返回 `*StaleConsentError`（名字是过期那条），且 **MCP 的 consent store 不存在**。
- 新增 `TestDecideProjectServersRecordsBothKindsWhenNothingMoved`：两类都新鲜 → 两个 store 都写出来了
  （防止"永远拒绝"式的假修复）。
- 两个测试都用 `t.TempDir()` 里的临时 home / workspace / project，不碰真实 `~/.forebrain`。

## Done criteria

机器可检查，全部满足：

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/process` exit 0
- [ ] `gofmt -l pkg/process` 无输出
- [ ] 两个新测试 PASS
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/tui ./pkg/gateway -count=1` 与基线相比无新增 FAIL
- [ ] `grep -c "func staleProjectServerChoices" pkg/process/mcp.go` 为 `1`
- [ ] `grep -c "a stale LSP list leaves the MCP answers" pkg/process/mcp.go` 为 `0`（旧注释必须改掉）
- [ ] `git status --porcelain` 只显示 Scope 里的两个文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1，或 drift check 有输出且被引用函数的语义变了。
2. 基线命令本身跑不起来，拿不到失败清单。
3. 第一个新测试在 Step 1 直接 PASS（说明已经有人做了两阶段）。
4. `pendingLSPFingerprint` / `writeProjectLSPFile` 在 `pkg/process` 的测试里不可用（它们可能在别的文件里
   或需要额外前置）：先 `grep -rn "func pendingLSPFingerprint\|func writeProjectLSPFile" pkg/process/` 确认；
   找不到就 STOP，不要自己造一套文件格式。
5. 你发现 `ProjectLSPFingerprints` 对未受信任 launch 返回 nil，而 `d.LSP` 非空——不要"顺手兼容"，
   按"nil 视为全部过期"写测试并报告（这是真实边界，需要评审定语义）。
6. 你想顺手改网关的 `writeStaleConsent` 或给它加新错误码——那是超出本计划的契约变更，先停下报告。

## Maintenance notes

- 谁将来给确认面板加第三类服务器，必须同时加进 `staleProjectServerChoices` 与 `DecideProjectServers`
  写入段；两类校验、两类落盘、一个 `AlwaysAllow` 尾开关，顺序不能换。
- 评审重点：`staleChoiceNames` 会对名字做小写化，这与 `DecideProjectMCPConsentsFor` / `lsp` 内部的键
  一致；LSP 的 id 大小写敏感程度与 MCP 不同，但两边内部都小写化后再比，所以这里统一小写是安全的。
- 已知限制（不在本计划内）：`AlwaysAllow` 只在两类都记录后才置真——这条不变；但如果将来允许"只问一类"，
  那就要重新定义"两类的指纹引用的是同一次读"是什么意思。
