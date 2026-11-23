# Plan 110：项目服务确认的引擎层——一次决定全部 + 「总是允许本项目的服务」开关（MCP 与 LSP 同一语义）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。工作区里已有大量 owner 未提交的改动（`pkg/tui/*`、`pkg/run/*`、`pkg/gateway/*`、
> `frontend/*` 等），与本计划无关，**不要碰、不要回滚、不要格式化**。
>
> **Drift check（先跑）**：
> `git diff --stat f18ff14 -- pkg/process/mcp.go pkg/process/mcp_test.go pkg/mcp/project_consent.go pkg/lsp/project.go pkg/lsp/manager.go pkg/lsp/resolve.go pkg/process/open.go pkg/process/runner_pool.go`
> 预期**无输出**（这些文件在写计划时与 HEAD 一致）。有输出就逐段对照下面 "Current state" 的摘录，不符即 STOP 1。

## Status

- **Priority**: P1（`112`、`113` 都建在它上面）
- **Effort**: M
- **Risk**: MED（改的是「哪些项目服务会被启动」的判定；默认关闭的新开关不改变任何既有行为）
- **Depends on**: none
- **Category**: direction（owner 2026-10-23 新语义）
- **Planned at**: commit `f18ff14`（dev 工作区含 owner 未提交改动），2026-10-23

## Why this matters

项目目录里的 `.forebrain/mcp_servers.yaml`（MCP）和 `.forebrain/lsp_servers.yaml`（语言服务器）随代码仓库一起来，
所以每个条目要经操作者确认才会启动。现在的确认是**逐条**问的，owner 2026-10-23 判定「逐条确认的用户体验太差」，改成：

1. 一个面板**一次**列出全部待确认项（MCP 和语言服务器同一个面板、同一语义），勾着的允许、没勾的记为不允许；
2. 面板里多一个「**Always allow this project's servers**」——打开后，这个项目（对这个主代理）以后新增或改动的条目**无需再确认**直接允许；
   **显式拒绝过的条目仍然不启动**（显式拒绝优先，参照 Claude Code 的 `enableAllProjectMcpServers` + `disabledMcpjsonServers`）。

本计划只做引擎层（存储 + 判定 + 一次性记录面板答案的 API），不做任何界面：TUI 面板见 `plans/112`，网页见 `plans/113`。

## Current state

**两套逐条确认记录**（各自一个 JSON 文件，按主代理工作区 × 项目键 × 条目名 × 指纹存 allow/deny）：

- `pkg/mcp/project_consent.go` — MCP 的记录：`ProjectConsentPath`（`<ws>/state/mcp/project_consent.json`）、`LoadProjectConsents`、
  `SaveProjectConsents`（tmp+rename 原子写，目录 0700、文件 0600）、`Decision`、`Decide`、`ServerFingerprint`、
  `PendingProjectConsent`（`:167`）、`ConsentSummary`（`:176`）。**本计划新增文件的写法照抄这里的 Load/Save。**
- `pkg/lsp/project.go` — LSP 的记录（同形，`<ws>/state/lsp/project_consent.json`）、`PendingProjectServer`（`:293`）、
  `ResolveProjectServers`（`:315`）、`projectServerSummary`（`:351`）、`DecideProjectServersFor`（`:435`）。

**MCP 的判定**（`pkg/process/mcp.go:60-103` 节选，`ResolveSessionMCP`）：

```go
	gated := safety.TrustedRoot(launch) != ""
	projectKey := memory.ProjectKey(projectRoot)
	consents, err := mcp.LoadProjectConsents(agentWorkspace)
	...
	for _, srv := range entries {
		name := strings.TrimSpace(srv.Name)
		fp := mcp.ServerFingerprint(srv)
		switch {
		case !gated:
			notApplied = append(notApplied, mcp.MCPNotApplied{Name: name, Reason: "project is not trusted"})
		default:
			decision, decided := consents.Decision(projectKey, name, fp)
			switch {
			case decided && decision == mcp.ProjectConsentAllow:
				included = append(included, srv)
			case decided && decision == mcp.ProjectConsentDeny:
				notApplied = append(notApplied, mcp.MCPNotApplied{Name: name, Reason: "entry was declined for this project"})
			default:
				notApplied = append(notApplied, mcp.MCPNotApplied{Name: name, Reason: "awaiting confirmation"})
				pending = append(pending, mcp.PendingProjectConsent{
					Name:        name,
					Fingerprint: fp,
					Summary:     mcp.ConsentSummary(srv),
				})
			}
		}
	}
```

`PendingProjectMCPConsents`（`pkg/process/mcp.go:173`）用同样的规则列出「没有决定的条目」；`DeclinedProjectMCPConsents`（`:318`）列出拒绝的。

**LSP 的判定**（`pkg/lsp/project.go:315-345`，`ResolveProjectServers(agentWorkspace, projectRoot, projectKey string, trusted bool)`）：
同样的 `switch`：`decided && Allow` → `state.Allowed[id]`；`decided && Deny` → `state.Denied`；否则 → `state.Pending`（带 `Summary: projectServerSummary(id, entries[id])`）。
调用方：`pkg/lsp/manager.go:246`（`resolveCached`，带缓存）、`pkg/lsp/resolve.go:653`（`ToolEnabled`，决定 `lsp` 工具注册与否）、
`pkg/process/mcp.go:352`（`PendingProjectLSPConsents`）、`pkg/process/mcp.go:406`（`InspectProjectLSP`），以及 13 处测试。

**LSP manager 的缓存**（`pkg/lsp/manager.go:231-254`）：按 `enabled.json`、项目文件、LSP 确认文件三者的 mtime 判缓存是否有效
（字段 `cacheEnabled`、`cacheProjMod`、`cacheConsMod`，`manager.go:66-68`）。

**架构护栏（关键约束）**：`pkg/architecture/cache_test.go` 的 `fanOutBudgets` 只许变小不许变大——`"lsp": {current: 4}`
（只能 import config、home、event、tool），`"process": {current: 19}`。**所以不能新建包、`pkg/lsp` 也不能 import `pkg/mcp`。**
本计划的做法：开关的存储放在 `pkg/mcp`（`process` 早已 import 它）；`pkg/lsp` 不读这个文件，由 `process` 把「开没开」**注入**进去
（`ResolveProjectServers` 加一个 `autoAllow bool` 参数；`lsp.ManagerOptions` 加一个回调）。

**其他约定**：项目键一律 `memory.ProjectKey(root)`（= `state.ProjectKeyForRoot(root)`）；名字/ID 在记录里一律
`strings.ToLower(strings.TrimSpace(...))`；注释是完整英文句子、解释「为什么」，与周边一致。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| 本计划的包 | `go test -tags fts5 ./pkg/mcp/ ./pkg/lsp/ ./pkg/process/` | `ok` ×3 |
| 架构护栏 | `go test ./pkg/architecture/` | `ok` |
| 依赖方编译 | `go vet -tags fts5 ./pkg/gateway/ ./pkg/tui/ ./cmd/forebrain/` | exit 0 |
| 格式 | `gofmt -l pkg/mcp pkg/lsp pkg/process` | 无输出 |
| 包图 | `scripts/package-graph.sh && git diff --stat -- pkg/architecture/testdata/graph.json` | 只有 `loc` 数字变化，`edges` 不变 |

`-tags fts5` 必须带（state 库是带 FTS5 的 SQLite）。

## Scope

**In scope**：
- `pkg/mcp/project_auto_allow.go`（新建）、`pkg/mcp/project_auto_allow_test.go`（新建）
- `pkg/mcp/project_consent.go`（`PendingProjectConsent` 加 `Detail`，新增 `ConsentDetail`）
- `pkg/lsp/project.go`、`pkg/lsp/manager.go`、`pkg/lsp/resolve.go` 及 `pkg/lsp/*_test.go` 里 `ResolveProjectServers(` 的调用处
- `pkg/process/mcp.go`、`pkg/process/mcp_test.go`、`pkg/process/open.go`（只改 `lspOpts`）、`pkg/process/runner_pool.go`（只改 `lspOpts`）
- `pkg/architecture/testdata/graph.json`（只由 `scripts/package-graph.sh` 重新生成）

**Out of scope**：
- 任何界面（`pkg/tui/*`、`pkg/gateway/*`、`frontend/*`、`cmd/forebrain/*`）——分别在 `112`、`113`、`114`。
- 不改既有的 `DecideProjectMCPConsents` / `DecideProjectLSPConsents`（批量、无指纹）和 `...For` 函数的行为——网关和旧调用方还在用。
- 不改 `ConsentSummary` / `projectServerSummary` 的输出文字（网页和测试按原文断言）。
- 不新增 `pkg/` 包、不给 `pkg/lsp` / `pkg/process` 加新的 `pkg/` import（护栏会红）。

## Steps

### Step 1：在 `pkg/mcp` 新建「总是允许本项目的服务」开关的存储

新建 `pkg/mcp/project_auto_allow.go`，照 `project_consent.go` 的 Load/Save 写法：

```go
// "Always allow this project's servers": one switch per primary agent and
// project that lets every entry the project's server files declare — MCP
// servers and language servers alike — run without being confirmed one by
// one. An entry the operator declined explicitly stays out regardless.
//
// It lives here because the composition root reads it for both kinds of
// entry and pkg/lsp may not import pkg/mcp; pkg/lsp is handed the answer.
package mcp

// ProjectAutoAllowRecord is one project's switch; its presence means on.
type ProjectAutoAllowRecord struct {
	Since int64 `json:"since"`
}

// ProjectAutoAllow maps project key -> record.
type ProjectAutoAllow map[string]ProjectAutoAllowRecord

// ProjectAutoAllowPath is <agentWorkspace>/state/project_auto_allow.json, or ""
// when there is no workspace (a path relative to the process directory would
// be a different store for every launch directory).
func ProjectAutoAllowPath(agentWorkspace string) string

// LoadProjectAutoAllow: no workspace or missing file -> empty, nil; a read
// error -> nil, err; unparsable JSON -> empty, nil (the switch fails closed).
func LoadProjectAutoAllow(agentWorkspace string) (ProjectAutoAllow, error)

// SaveProjectAutoAllow writes atomically (tmp + rename, dir 0700, file 0600).
// No workspace is an error.
func SaveProjectAutoAllow(agentWorkspace string, a ProjectAutoAllow) error

// On reports whether projectKey's switch is on ("" is never on).
func (a ProjectAutoAllow) On(projectKey string) bool

// SetProjectAutoAllow turns one project's switch on or off and saves. An empty
// key is a no-op. An unreadable store is returned as an error and not
// overwritten.
func SetProjectAutoAllow(agentWorkspace, projectKey string, on bool, at time.Time) error

// ProjectAutoAllowed is the read every resolution uses: any error reads as off.
func ProjectAutoAllowed(agentWorkspace, projectKey string) bool
```

键一律 `strings.TrimSpace(projectKey)`。

**Verify**：`go build ./pkg/mcp/` → exit 0。

### Step 2：两种条目都带上「不含名字的说明」`Detail`

面板要把名字和「它会跑什么」分两列显示（`112`），现有 `Summary` 是「`name: runs \`cmd\``」一句话，不适合。

1. `pkg/mcp/project_consent.go`：`PendingProjectConsent` 加字段 `Detail string \`json:"-"\``（`json:"-"`：不改任何 API 的 JSON）；新增

   ```go
   // ConsentDetail is what an entry would run or contact, without its name:
   // the command line, or the address.
   func ConsentDetail(srv appcfg.MCPServerConfig) string
   ```

   返回：有 command → `cmd`（有 args 时 `cmd + " " + strings.Join(args, " ")`）；否则有 URL → URL；否则 `"no command or URL"`。
   `ConsentSummary` **不动**。
2. `pkg/lsp/project.go`：`PendingProjectServer` 加 `Detail string \`json:"-"\``；新增未导出的 `projectServerDetail(id string, srv appcfg.LSPServerConfig) string`：
   有 command → 命令行（同上拼法）；无 command 且 `inCatalog(id)` → `"changes built-in " + id + " settings"`；否则 `"no command"`；
   `srv.Enabled != nil && *srv.Enabled` 时再追加 `" · turns it on"`。`projectServerSummary` **不动**。
3. 填 `Detail`：`ResolveProjectServers` 里构造 `PendingProjectServer` 处；`pkg/process/mcp.go` 里三处构造 `mcp.PendingProjectConsent`
   （`ResolveSessionMCP` 的 pending、`PendingProjectMCPConsents`、`DeclinedProjectMCPConsents`）都加 `Detail: mcp.ConsentDetail(srv)`。

**Verify**：`go build ./... && go test -tags fts5 ./pkg/mcp/ ./pkg/lsp/ ./pkg/process/` → 全 `ok`（此步只是加字段，既有测试不该变红）。

### Step 3：MCP 的判定认这个开关

`pkg/process/mcp.go`：

1. `ResolveSessionMCP`：在 `projectKey := ...` 之后算一次
   `autoAllow := gated && mcp.ProjectAutoAllowed(agentWorkspace, projectKey)`；内层 `switch` 在 `ProjectConsentDeny` 分支**之后**、`default` **之前**加：

   ```go
			case autoAllow:
				// The project's servers are always allowed: an entry nobody
				// decided about runs; an explicit decline above still wins.
				included = append(included, srv)
   ```
2. `PendingProjectMCPConsents`：信任检查之后，若 `mcp.ProjectAutoAllowed(agentWorkspace, projectKey)` 为真就 `return nil`
   （开关开着时没有「待确认」的条目）。`projectKey` 在函数里已有同名变量，复用它。
3. 新增两个给界面用的函数（放在 `DeclinedProjectMCPConsents` 后面）：

   ```go
// ProjectServersAutoAllowed reports the launch project's "always allow" switch.
// An untrusted launch has no project servers at all and reads as off.
func ProjectServersAutoAllowed(agentWorkspace string, launch safety.ProjectContext) bool

// SetProjectServersAutoAllow turns the switch on or off. An untrusted launch is
// a no-op: its project files are never read.
func SetProjectServersAutoAllow(agentWorkspace string, launch safety.ProjectContext, on bool) error
   ```

   都按 `root := safety.TrustedRoot(launch)`、`memory.ProjectKey(root)` 取键。

**Verify**：`go test -tags fts5 ./pkg/process/ -run 'ResolveSessionMCP|ProjectMCP'` → `ok`（既有用例全绿）。

### Step 4：LSP 的判定认这个开关（注入，不 import）

1. `pkg/lsp/project.go`：`ResolveProjectServers` 签名改为
   `ResolveProjectServers(agentWorkspace, projectRoot, projectKey string, trusted, autoAllow bool) ProjectServerState`；
   `switch` 在 `ProjectConsentDeny` 分支之后加 `case autoAllow: state.Allowed[id] = entries[id]`（注释同 Step 3）。函数注释补一句开关的语义。
2. `pkg/lsp/manager.go`：`ManagerOptions` 加字段

   ```go
	// AutoAllowProject reports the project's "always allow" switch. The
	// composition root supplies it; pkg/lsp does not read that store itself.
	// Nil reads as off.
	AutoAllowProject func() bool
   ```

   并加未导出方法 `func (o ManagerOptions) autoAllowProject() bool`（nil → false）。`Manager` 加缓存字段 `cacheAuto bool`；
   `resolveCached` 开头取 `auto := m.opts.autoAllowProject()`，缓存有效条件再加 `auto == m.cacheAuto`，调用改为
   `ResolveProjectServers(..., m.opts.Trusted, auto)`，最后 `m.cacheAuto = auto`。
3. `pkg/lsp/resolve.go:653`（`ToolEnabled`）：调用加 `opts.autoAllowProject()`。
4. `pkg/lsp/*_test.go` 里 13 处 `ResolveProjectServers(` 调用补最后一个参数 `false`（行为不变）。
5. `pkg/process/mcp.go:352`、`:406`：传 `mcp.ProjectAutoAllowed(agentWorkspace, memory.ProjectKey(root))`。
6. `pkg/process/open.go` 与 `pkg/process/runner_pool.go` 的 `lspOpts := lsp.ManagerOptions{...}` 之后（`ProjectKey` 已赋值之后）加：

   ```go
	lspOpts.AutoAllowProject = func() bool {
		return mcp.ProjectAutoAllowed(agentWorkspace, lspOpts.ProjectKey)
	}
   ```

   `open.go` 里工作区变量是 `activeAgent.WorkspaceRoot`；`runner_pool.go` 里是 `agentWorkspace`。闭包捕获的必须是**值**而不是之后会变的变量
   （先 `ws, key := ..., lspOpts.ProjectKey` 再闭包）。

**Verify**：`go build ./... && go test -tags fts5 ./pkg/lsp/ ./pkg/process/` → `ok` ×2；`go test ./pkg/architecture/` → `ok`。

### Step 5：一次记录面板的全部答案

`pkg/process/mcp.go` 新增：

```go
// ProjectServerChoice is one project entry as the operator was shown it — an
// MCP server name or a language-server id, and the fingerprint it had — and
// whether they allowed it.
type ProjectServerChoice struct {
	Name        string
	Fingerprint string
	Allow       bool
}

// ProjectServersDecision is one answer to the project-servers confirmation:
// every entry it lists is decided (allowed or declined), and AlwaysAllow turns
// the project's "always allow" switch on as well.
type ProjectServersDecision struct {
	MCP         []ProjectServerChoice
	LSP         []ProjectServerChoice
	AlwaysAllow bool
}

// DecideProjectServers records the answer. Each kind is bound to the
// fingerprints the operator was shown: an entry that changed since makes that
// kind's call fail with its *StaleConsentError before anything of that kind
// is recorded (MCP is recorded first; a stale LSP list leaves the MCP answers
// in place, and the caller asks again about what is still pending). The switch
// is turned on last, only when both kinds were recorded.
func DecideProjectServers(agentWorkspace string, launch safety.ProjectContext, d ProjectServersDecision) error
```

实现：`root := safety.TrustedRoot(launch)`，空就 `return nil`；`MCP` 非空 → 拆出 `names`、`allowed`、`fps`（键 `strings.ToLower(strings.TrimSpace(name))`）
调 `DecideProjectMCPConsentsFor(agentWorkspace, launch, names, allowed, fps)`；`LSP` 非空 → 同样调 `DecideProjectLSPConsentsFor`；
任一出错原样返回；都成功且 `AlwaysAllow` → `mcp.SetProjectAutoAllow(agentWorkspace, memory.ProjectKey(root), true, time.Now())`。
拆分逻辑写成一个未导出小函数 `splitProjectServerChoices`。**不要**为 `fps` 传 nil（那会跳过指纹校验）。

**Verify**：`go test -tags fts5 ./pkg/process/` → `ok`。

### Step 6：注释与包图

1. `pkg/process/mcp.go` 文件头的三道闸门说明里，第 2 条补一句：项目的「always allow」开关打开时，没有决定的条目直接通过，显式拒绝仍排除。
2. `pkg/lsp/project.go` 文件头补同一句（说明开关由 composition root 注入）。
3. 跑 `scripts/package-graph.sh`。

**Verify**：`gofmt -l pkg/mcp pkg/lsp pkg/process` → 无输出；
`git diff -- pkg/architecture/testdata/graph.json | grep '^[+-]' | grep -v '"loc"' | grep -v '^[+-][+-]'` → 无输出（只有 loc 变化，没有新边）。

## Test plan

模式参照：`pkg/process/mcp_test.go` 的 `trustedVCSProject`、`writeProjectMCP`、`resolveMCPGlobal` 辅助函数；LSP 项目文件参照
`pkg/lsp/manager_test.go:1080-1150` 一带（写 `.forebrain/lsp_servers.yaml`、`DecideProjectServers`）。

新增（全部要有）：

- `pkg/mcp/project_auto_allow_test.go`
  1. 没有文件 → `ProjectAutoAllowed` 为 false；`SetProjectAutoAllow(on)` 后文件在 `ProjectAutoAllowPath` 且为 true；再 `off` → false 且键被删。
  2. 工作区为空 → `ProjectAutoAllowPath` 为 `""`、`ProjectAutoAllowed` 为 false、`SetProjectAutoAllow` 返回 nil 不写任何文件（空键同理）。
  3. 文件是坏 JSON → `ProjectAutoAllowed` 为 false（fail closed）。
  4. 两个项目键互不影响。
- `pkg/process/mcp_test.go`
  5. `TestResolveSessionMCPAutoAllowLoadsUndecided`：开关开 + 未决定条目 → 在 `res.Servers` 里；`PendingProjectMCPConsents` 为空。
  6. `TestResolveSessionMCPAutoAllowKeepsDeclined`：先 `DecideProjectMCPConsents(ws, launch, nil)`（拒绝），再开开关 → 仍不在 `res.Servers`，原因含 "declined"。
  7. `TestResolveSessionMCPAutoAllowNeedsTrust`：未信任项目 + 开关开 → 项目条目不加载、`ProjectServersAutoAllowed` 为 false。
  8. `TestDecideProjectServersRecordsShownEntries`：MCP 两条（一允一拒）+ LSP 两条（一允一拒），指纹取自 `PendingProjectMCPConsents` / `PendingProjectLSPConsents` →
     决定后 `ResolveSessionMCP` 与 `InspectProjectLSP` 结果对应；开关仍关。
  9. `TestDecideProjectServersAlwaysAllow`：`AlwaysAllow: true` → 开关开；之后往 `mcp_servers.yaml` **追加**一条新条目 → 无需决定直接在 `res.Servers`。
  10. `TestDecideProjectServersStale`：拿到指纹后改写条目的 command → 返回错误且 `errors.As(err, &stale)` 成立（`interface{ StaleNames() []string }`）；该条目仍待确认。
  11. `Pending` 系列的 `Detail`：stdio 条目是命令行、URL 条目是地址、LSP 内置覆盖是 `changes built-in gopls settings`。
- `pkg/lsp`（加到 `manager_test.go` 或 `project_test.go`）
  12. `ResolveProjectServers(..., trusted=true, autoAllow=true)`：未决定 → `Allowed`；已拒绝 → 仍 `Denied`。
  13. Manager 缓存：`AutoAllowProject` 回调先返回 false（条目在 Pending）→ 改为返回 true 后再次解析 → 条目进入生效集合（证明缓存认这个开关）。

**Verify**：`go test -tags fts5 ./pkg/mcp/ ./pkg/lsp/ ./pkg/process/` → `ok` ×3，以上 13 个用例都存在并通过。

## Done criteria

- [ ] `go build ./...` exit 0
- [ ] `go test -tags fts5 ./pkg/mcp/ ./pkg/lsp/ ./pkg/process/` 全 `ok`，含上面 13 个新用例
- [ ] `go test ./pkg/architecture/` `ok`（扇出护栏未破）
- [ ] `go vet -tags fts5 ./pkg/gateway/ ./pkg/tui/ ./cmd/forebrain/` exit 0
- [ ] `gofmt -l pkg/mcp pkg/lsp pkg/process` 无输出
- [ ] `grep -rn "ResolveProjectServers(" pkg | grep -v "_test.go"` 的每一处都传了 5 个参数
- [ ] `git status --short` 里本计划新增/修改的文件都在 Scope 内（owner 原有的未提交改动不算）

## STOP conditions

1. Drift check 有输出且摘录对不上。
2. `go test ./pkg/architecture/` 因扇出或层级变红——说明某处新加了 `pkg/` import；不要去改 `fanOutBudgets`，停下报告。
3. 发现 `open.go` 的 `lspOpts.ProjectKey` 与 `memory.ProjectKey(root)` 对同一目录给出不同的键（写一个一次性测试比较即可）——两边会读写不同的键，停下报告。
4. 某个既有测试因为本计划变红、且原因不是「`ResolveProjectServers` 少一个参数」。

## Maintenance notes

- 开关只影响「没有决定的条目」。任何新增的项目条目判定路径（例如以后的项目级 hooks）都要同时认 `ProjectAutoAllowed`，否则语义会裂开。
- `DecideProjectServers` 不是原子的（MCP 先记、LSP 后记）。面板在 stale 时重新读待确认列表再问一次就能收敛；不要为此加锁或事务。
- 网页撤销开关走 `SetProjectServersAutoAllow`（`113`）；关掉后，开着期间「直接允许、但从没被显式决定」的条目会重新变成待确认——这是有意的。
