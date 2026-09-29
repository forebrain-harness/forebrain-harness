# 任务 03：建立 `pkg/lsp` 骨架并接入组合根

> **执行者须知**：逐步执行；每一步运行「验证」命令并确认结果后再进入下一步。出现「STOP 条件」中的任何情况立即停止并汇报。完成后把 `docs/plan/lsp/README.md` 中本任务状态改为 `DONE`。
>
> **规范**：`docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md`。开始前读规范「给实施者」、§3.3、§3.8、§3.9、§3.14、§4（全部）、§8.5。
>
> **前置**：任务 01（`appcfg.Root.LSP`、`EffectiveFeatures().LSP`）与任务 02（`tool.CodeIntelligence`、`tool.CodeIntelControl`、`event.LSPSnapshot` 等）已合入。先确认：`grep -n "CodeIntelligence interface" pkg/tool/search.go` 与 `grep -n "LSP  *\*bool" pkg/config/config.go` 都有输出，否则 STOP。
>
> **漂移检查（先运行）**：`git diff --stat 6305ea9..HEAD -- pkg/process/open.go pkg/process/runner_pool.go pkg/process/config_reload.go pkg/run/runner.go pkg/run/factory.go pkg/tool/registry.go pkg/architecture/cache_test.go`。有输出时（任务 01/02 不会改这些文件），对比「现状」摘录，对不上即 STOP。

## 状态

- 优先级：P0 · 工作量：M · 风险：MED（改组合根，但骨架不启动任何进程、不注册任何工具）
- 依赖：01、02
- 类别：direction
- 计划基于：`6305ea9`，2026-09-29

## 为什么

`pkg/architecture` 的 `TestEveryPackageHasAnImporter` 要求每个包从出现起就被别的包导入，所以 `pkg/lsp` 必须和它在 `pkg/process` 的接线在同一个 PR 里出现。本任务建一个**行为为空**的骨架：`Pool`、`Manager` 实现任务 02 的两个接口，但不启动任何服务器、不注册工具、推荐不可用；同时把它接进主 Runner、项目 Runner（gateway 的 RunnerPool）、子代理与 fork、配置重载和进程关闭。之后的任务只在这些已接好的位置后面填实现，不再动组合根。

## 现状

- `pkg/process/open.go:48-95` —— `Environment` 结构体（字段 `Root`、`LaunchProject`、`Deps run.Deps`、`Runner`、`pool *RunnerPool` …）。
- `pkg/process/open.go:96-119` —— `Environment.Close()`：先 `env.Runner.Close()`，再 `env.CloseRunnerPool()`，然后关遥测和 SQL。
- `pkg/process/open.go:255-313` —— `Open` 在这里解析会话 MCP 列表、构建 `env.Deps = run.Deps{...}`（:278）、创建 `runner := &run.Runner{Deps: &env.Deps, ...}`（:299）、设置 `runner.ProjectRoot` 与 `runner.ProjectKey = memory.ProjectKey(launchProject.Project.Root)`（`launchProject.Project.Root != ""` 时），之后 `runner.Load()`（:325）。
- `pkg/process/runner_pool.go:116-124` —— `poolEntry` 结构体；`:645-705 buildEntryLocked` 为项目构建 `deps := run.Deps{...}`（:671）与 `runner := &run.Runner{Deps: &deps, ...}`（:700），`runner.Load()` 失败时返回错误；`:606 closePoolEntries` 是所有驱逐/关闭路径共用的关闭函数；`:280-294` 的空闲释放循环调用 `entry.runner.MCPStartup().ReleaseIdleConnections(...)`。
- `pkg/process/config_reload.go:192-240 reloadConfig`：`env.Runner.LoadConfig(next)` 成功后继续；`:301 AdoptConfig(next)` 直接替换 `env.Deps.AppCfg`。
- `pkg/run/runner.go:48-135` —— `Deps` 结构体（字段带长注释，如 `MCPServers`、`MCPProject`、`LaunchProject`）。`Runner` 内嵌 `*Deps`，所以 `r.MCPServers` 这类写法是提升字段。
- `pkg/run/runner.go:1166-1176` —— `loadLocked` 里构建 `rt := &tool.AgentToolRuntime{...}` 后调用 `tool.RegisterDefaultTools(a, r.tools, rt)`。
- `pkg/run/factory.go:46` —— `NewIsolatedRunner` 用字面量逐个列出子 Runner 继承的 `Deps` 字段；`:163-178` 的 `ownerMCPServers` / `ownerMCPProject` 是「从 Owner 继承」的写法范例。
- `pkg/tool/registry.go:264-278` —— `AgentToolRuntime` 结构体。
- `pkg/architecture/cache_test.go`：`:169 fanOutBudgets`（其中 `"process": {current: 18, target: -1}`）、`:351 packageLayer`、`:526 sameLayerEdges`。
- 结构约束：`pkg/process`、`pkg/run`、`pkg/tool` 已各有 20 个生产文件，**本任务不得在这三个包新建生产文件**；`pkg/lsp` 至少 2 个生产文件、必须有 `doc.go`、不得有子包。

## 范围

**新建：** `pkg/lsp/doc.go`、`pkg/lsp/pool.go`、`pkg/lsp/manager.go`、`pkg/lsp/control.go`、`pkg/lsp/resolve.go`，以及 `pkg/lsp/pool_test.go`、`pkg/lsp/manager_test.go`、`pkg/lsp/control_test.go`、`pkg/lsp/resolve_test.go`。

**修改：** `pkg/process/open.go`、`pkg/process/runner_pool.go`、`pkg/process/config_reload.go`、`pkg/process/open_test.go`、`pkg/run/runner.go`、`pkg/run/factory.go`、`pkg/run/factory_test.go`、`pkg/tool/registry.go`、`pkg/architecture/cache_test.go`、`pkg/architecture/testdata/graph.json`。

**不要碰：** 工具注册逻辑（`RegisterDefaultTools` 的内容）、`pkg/tui`、`pkg/gateway`、任何 LLM wrapper。本任务之后模型可见的内容必须完全不变。

## 步骤

### 步骤 1：新建 `pkg/lsp` 骨架

`pkg/lsp/doc.go`：

```go
// Package lsp runs language servers for code intelligence: it owns their
// processes, speaks the Language Server Protocol to them, and implements the
// tool.CodeIntelligence and tool.CodeIntelControl ports that pkg/process
// injects into each runner. See docs/plan/LSP_CODE_INTELLIGENCE_PLAN.md.
//
// Files: pool.go (process-wide instance pool), manager.go (one runner's view,
// the CodeIntelligence port), control.go (the CodeIntelControl port),
// resolve.go (which servers a project gets and whether the lsp tool is
// registered).
package lsp
```

`pkg/lsp/pool.go`：

```go
package lsp

import (
	"sync"
	"sync/atomic"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

// Pool is the process-wide owner of language-server instances. One per
// process.Environment; every runner gets a Manager from it.
type Pool struct {
	cfg      atomic.Pointer[appcfg.Root]
	mu       sync.Mutex
	managers map[*Manager]struct{}
	closed   bool
}

// NewPool builds the pool over the configuration in force.
func NewPool(cfg *appcfg.Root) *Pool {
	p := &Pool{managers: map[*Manager]struct{}{}}
	p.cfg.Store(cfg)
	return p
}

// config returns the configuration in force.
func (p *Pool) config() *appcfg.Root {
	if p == nil {
		return nil
	}
	return p.cfg.Load()
}

// Reconcile adopts a reloaded configuration. Instances whose configuration
// changed are stopped and start again on next use (task 08).
func (p *Pool) Reconcile(cfg *appcfg.Root) {
	if p == nil || cfg == nil {
		return
	}
	p.cfg.Store(cfg)
}

// NewManager returns the view one runner uses. After Close it returns a
// manager that is already closed.
func (p *Pool) NewManager(opts ManagerOptions) *Manager {
	m := &Manager{pool: p, opts: opts}
	if p == nil {
		m.closed = true
		return m
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		m.closed = true
		return m
	}
	p.managers[m] = struct{}{}
	return m
}

// Close stops every instance and closes every manager. Idempotent.
func (p *Pool) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	managers := make([]*Manager, 0, len(p.managers))
	for m := range p.managers {
		managers = append(managers, m)
	}
	p.managers = map[*Manager]struct{}{}
	p.mu.Unlock()
	for _, m := range managers {
		m.Close()
	}
	return nil
}

func (p *Pool) forget(m *Manager) {
	if p == nil {
		return
	}
	p.mu.Lock()
	delete(p.managers, m)
	p.mu.Unlock()
}
```

`pkg/lsp/manager.go`（实现 `tool.CodeIntelligence`；骨架行为：什么都不启动）：

```go
package lsp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// ManagerOptions describes the runner a Manager serves. Everything here is
// frozen for the runner's lifetime.
type ManagerOptions struct {
	Home              string // FOREBRAIN_HOME
	AgentWorkspace    string // the primary agent's workspace root; state lives in <AgentWorkspace>/state/lsp
	ProjectRoot       string // the launch project root; "" when the runner has no project
	ProjectKey        string
	Trusted           bool // safety.TrustedRoot(launch) != ""
	VersionControlled bool
	// ToolRegistered is the frozen answer of ToolEnabled for this runner; the
	// caller computes it and passes it in so the snapshot can report it.
	ToolRegistered bool
}

// Manager is one runner's view of the pool.
type Manager struct {
	pool     *Pool
	opts     ManagerOptions
	mu       sync.Mutex
	closed   bool
	listener func(ctx context.Context, rec event.LSPRecommendation)
	subs     map[int]func(event.LSPSnapshot)
	nextSub  int
}

var (
	_ tool.CodeIntelligence = (*Manager)(nil)
	_ tool.CodeIntelControl = (*Manager)(nil)
)

// errNotAvailable answers every control action until task 08 implements them.
var errNotAvailable = errors.New("language servers are not available in this build yet")

// Close releases the manager. Idempotent.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.subs = nil
	m.listener = nil
	m.mu.Unlock()
	m.pool.forget(m)
}

// ReleaseIdle stops this runner's idle instances (task 08).
func (m *Manager) ReleaseIdle() {}

func (m *Manager) Handles(absPath string) bool { return false }

func (m *Manager) Query(ctx context.Context, q tool.CodeIntelQuery) (tool.CodeIntelResult, error) {
	if m == nil || !m.opts.Trusted {
		return tool.CodeIntelResult{}, errors.New("language servers only run in trusted projects")
	}
	ext := strings.ToLower(filepath.Ext(q.AbsPath))
	if ext == "" {
		ext = filepath.Base(q.AbsPath)
	}
	return tool.CodeIntelResult{}, fmt.Errorf("no language server handles %s files in this project; the user can enable one with /lsp", ext)
}

func (m *Manager) DidWrite(ctx context.Context, agentSessionID string, changes []tool.FileChange) tool.DiagnosticsDelta {
	return tool.DiagnosticsDelta{}
}

func (m *Manager) DidRead(ctx context.Context, absPath string, content []byte) {}

func (m *Manager) DidRunShell(ctx context.Context) {}

func (m *Manager) PeekLate(agentSessionID string) (string, uint64) { return "", 0 }

func (m *Manager) AckLate(agentSessionID string, token uint64) {}
```

`pkg/lsp/control.go`（实现 `tool.CodeIntelControl`）：

```go
package lsp

import (
	"context"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
)

// Snapshot reports what /lsp shows. The skeleton has no servers yet.
func (m *Manager) Snapshot() event.LSPSnapshot {
	snap := event.LSPSnapshot{Servers: []event.LSPServerStatus{}}
	if m == nil {
		return snap
	}
	snap.ProjectRoot = m.opts.ProjectRoot
	snap.Trusted = m.opts.Trusted
	snap.ToolRegistered = m.opts.ToolRegistered
	snap.FeatureEnabled = m.pool.config().EffectiveFeatures().LSP
	return snap
}

func (m *Manager) Subscribe(fn func(event.LSPSnapshot)) (cancel func()) {
	if m == nil || fn == nil {
		return func() {}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return func() {}
	}
	if m.subs == nil {
		m.subs = map[int]func(event.LSPSnapshot){}
	}
	id := m.nextSub
	m.nextSub++
	m.subs[id] = fn
	return func() {
		m.mu.Lock()
		delete(m.subs, id)
		m.mu.Unlock()
	}
}

func (m *Manager) SetEnabled(serverID string, enabled bool) error { return errNotAvailable }

func (m *Manager) Restart(serverID string) error { return errNotAvailable }

func (m *Manager) Install(ctx context.Context, serverID string, progress func(line string)) error {
	return errNotAvailable
}

func (m *Manager) SetRecommendationListener(fn func(ctx context.Context, rec event.LSPRecommendation)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.listener = fn
	m.mu.Unlock()
}

func (m *Manager) DecideRecommendation(recommendationID string, choice event.LSPRecommendationChoice) error {
	return errNotAvailable
}

func (m *Manager) ResetRecommendations() error { return errNotAvailable }
```

注意：`(*appcfg.Root)(nil).EffectiveFeatures()` 可以安全调用（任务 01 的测试覆盖了 nil Root）。

`pkg/lsp/resolve.go`：

```go
package lsp

import appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"

// ToolEnabled is the frozen per-runner decision whether to register the lsp
// tool (spec §8.5): features.lsp, a trusted project, and at least one enabled
// primary server. The skeleton knows no servers yet, so it is always false;
// task 06 implements the server resolution this reads.
func ToolEnabled(cfg *appcfg.Root, opts ManagerOptions) bool {
	return false
}
```

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/lsp` → 退出码 0；`go vet ./pkg/lsp` → 退出码 0。

### 步骤 2：`pkg/tool/registry.go` —— `AgentToolRuntime` 加字段

在 `AgentToolRuntime`（:264）末尾加：

```go
	// CodeIntel is the language-server runtime for this runner; nil when the
	// runner has none. CodeIntelTool is the frozen decision whether the lsp
	// tool is registered (spec §8.5). Neither is read yet; tasks 10 and 11 do.
	CodeIntel     CodeIntelligence
	CodeIntelTool bool
```

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` → 退出码 0。

### 步骤 3：`pkg/run` —— `Deps` 字段、传给工具运行时、子 Runner 继承

1. `pkg/run/runner.go` 的 `Deps` 结构体，在 `ProjectInstructionsFor` 字段之后加（注释照下文，风格同其他字段）：

   ```go
   	// CodeIntel and CodeIntelControl are this runtime's language-server
   	// runtime (the tools' and the surfaces' ports onto one pkg/lsp Manager),
   	// installed by the composition root; nil when the runtime has none.
   	// CodeIntelTool is the frozen decision whether the lsp tool is registered:
   	// it sits in the prompt prefix, so it is computed once per runtime and
   	// never recomputed on a config reload. Subagents inherit all three.
   	CodeIntel        tool.CodeIntelligence
   	CodeIntelControl tool.CodeIntelControl
   	CodeIntelTool    bool
   ```

2. `loadLocked` 里构建 `rt := &tool.AgentToolRuntime{...}`（约 :1166）时加上 `CodeIntel: r.CodeIntel, CodeIntelTool: r.CodeIntelTool,`。
3. `pkg/run/factory.go`：
   - 在 `ownerMCPProject` 之后加三个方法，写法照 `ownerMCPProject`：

     ```go
     // ownerCodeIntel hands a subagent its parent's language-server runtime:
     // the child works in the same project, and a child whose tool table
     // differed from its parent's would break a fork's shared prompt prefix.
     func (f Factory) ownerCodeIntel() (tool.CodeIntelligence, tool.CodeIntelControl, bool) {
     	if f.Owner != nil && f.Owner.Deps != nil {
     		return f.Owner.CodeIntel, f.Owner.CodeIntelControl, f.Owner.CodeIntelTool
     	}
     	return nil, nil, false
     }
     ```

   - `NewIsolatedRunner`（:46）：在 `return` 之前 `codeIntel, codeIntelControl, codeIntelTool := f.ownerCodeIntel()`，并在 `&Deps{...}` 字面量里加 `CodeIntel: codeIntel, CodeIntelControl: codeIntelControl, CodeIntelTool: codeIntelTool`。

   （若 `pkg/run/factory.go` 尚未导入 `pkg/tool`：它已通过 `Factory.Tools *tool.State` 导入，无需新增 import。）

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/run` → 退出码 0。

### 步骤 4：`pkg/process` —— 创建 Pool 与 Manager 并注入

1. `open.go`：`import` 加 `"github.com/forebrain-harness/forebrain-harness/pkg/lsp"`（第二组，按字母序）。`Environment` 结构体加字段：

   ```go
   	// LSP is the process-wide language-server pool; lspManager is the
   	// primary runner's view of it. Project runners get their own managers
   	// from the same pool (see RunnerPool).
   	LSP        *lsp.Pool
   	lspManager *lsp.Manager
   ```

2. `Open` 中，在 `env.Deps = run.Deps{...}`（:278）**之前**加：

   ```go
   	env.LSP = lsp.NewPool(cfgRoot)
   	lspOpts := lsp.ManagerOptions{
   		Home:              root,
   		AgentWorkspace:    activeAgent.WorkspaceRoot,
   		ProjectRoot:       launchProject.Project.Root,
   		Trusted:           safety.TrustedRoot(launchProject) != "",
   		VersionControlled: launchProject.Project.VersionControlled,
   	}
   	if lspOpts.ProjectRoot != "" {
   		lspOpts.ProjectKey = memory.ProjectKey(lspOpts.ProjectRoot)
   	}
   	lspOpts.ToolRegistered = lsp.ToolEnabled(cfgRoot, lspOpts)
   	env.lspManager = env.LSP.NewManager(lspOpts)
   ```

   并在 `run.Deps{...}` 字面量里加 `CodeIntel: env.lspManager, CodeIntelControl: env.lspManager, CodeIntelTool: lspOpts.ToolRegistered,`。
3. `Open` 中 `runner.Load()` 失败返回错误的分支里，返回前加 `_ = env.LSP.Close()`。
4. `Environment.Close()`：在 `env.CloseRunnerPool()` 之后、遥测关闭之前加：

   ```go
   	// Language servers are children of this process too; they go after
   	// every runner that could still ask them for something.
   	if env.LSP != nil {
   		_ = env.LSP.Close()
   	}
   ```

5. `runner_pool.go`：
   - `poolEntry` 加字段 `lsp *lsp.Manager`（注释：this project's view of the environment's language-server pool）。
   - `buildEntryLocked`：在 `deps := run.Deps{...}` 之前构建 `lspOpts`（字段同上，但 `Home: env.Root`、`AgentWorkspace: agentWorkspace`、`ProjectRoot: project.Root`、`ProjectKey: project.ProjectKey`、`Trusted: safety.TrustedRoot(launch) != ""`、`VersionControlled: launch.Project.VersionControlled`、`ToolRegistered: lsp.ToolEnabled(baseDeps.AppCfg, lspOpts)`）；`if env.LSP != nil { entry.lsp = env.LSP.NewManager(lspOpts) }`；`deps` 字面量加 `CodeIntelTool: lspOpts.ToolRegistered`；`if entry.lsp != nil { deps.CodeIntel = entry.lsp; deps.CodeIntelControl = entry.lsp }`（**注意**：不要把 nil 的 `*lsp.Manager` 直接赋给接口字段，否则接口非 nil）。`runner.Load()` 失败的分支里 `entry.lsp.Close()`（`Close` 对 nil 安全）。
   - `closePoolEntries`：在收集 `closing` 的循环里同时收集 `entry.lsp`，在关闭 runner 的 goroutine 之后逐个 `Close()`（`Manager.Close` 不阻塞，可以在等待 runner 之后直接调用）。
   - 空闲释放循环（:280-294）：对每个空闲 entry，在释放 MCP 之后调用 `entry.lsp.ReleaseIdle()`（nil 安全）。
6. `config_reload.go`：`reloadConfig` 中 `env.Runner.LoadConfig(next)` 成功之后加 `if env.LSP != nil { env.LSP.Reconcile(next) }`；`AdoptConfig` 在 `env.Deps.AppCfg = next` 之后加同样一行。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./...` → 退出码 0。

### 步骤 5：更新分层表

`pkg/architecture/cache_test.go`：

- `packageLayer`（:351）的 Layer 2 行加 `"lsp": 2`。
- `sameLayerEdges`（:526）Layer 2 部分加，并更新上方注释里的拓扑序为 `safety < event < tool < {hook, mcp, skill, lsp} < memory < assembly`：

  ```go
  	// lsp implements the code intelligence ports tool defines and fills in
  	// the event DTOs they carry; it imports nothing else in this layer.
  	"lsp": {"event", "tool"},
  ```

- `fanOutBudgets`（:169）：加 `"lsp": {current: 4, target: 4},`（注释：config, home, event, tool — home arrives with task 05）；把 `"process": {current: 18, target: -1}` 改为 `current: 19`，并在其上方注释里加一句：`The composition root also imports pkg/lsp: it owns the process-wide language-server pool and hands each runner its view, the same reason it imports pkg/mcp.`

然后重新生成包图。

**验证**：

- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- `scripts/package-graph.sh && git status --short pkg/architecture/testdata/graph.json` → ` M pkg/architecture/testdata/graph.json`（需要提交）

### 步骤 6：写测试

见「测试计划」。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/lsp ./pkg/run ./pkg/process -count=1` → 全部 `ok`。

### 步骤 7：确认模型可见内容没有变化

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/tui ./pkg/gateway ./pkg/turn -count=1` → 全部 `ok`（任何工具表/提示词 golden 测试失败都按 STOP 处理：骨架不应改变它们）。

### 步骤 8：全量检查

**验证**：`gofmt -l cmd pkg third_party` → 无输出；`go vet ./...` → 退出码 0；`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` → 全部 `ok`。

## 测试计划

- `pkg/lsp/pool_test.go`：`TestPoolCloseClosesManagers`（`NewManager` 两个，`Close` 后两个 manager 的 `Subscribe` 返回的 cancel 可调用且 manager 已 closed；再次 `Close` 不 panic）；`TestNewManagerAfterCloseIsClosed`；`TestReconcileAdoptsConfig`（`Reconcile` 后 `config()` 返回新指针；传 nil 不替换）。
- `pkg/lsp/manager_test.go`：`TestSkeletonManagerDoesNothing`（`Handles` false；`DidWrite` 返回 `Empty()`；`PeekLate` 为 `""`）；`TestSkeletonQueryErrors`（`Trusted: false` 时错误文本为 `language servers only run in trusted projects`；`Trusted: true`、路径 `/p/a.go` 时为 `no language server handles .go files in this project; the user can enable one with /lsp`）。
- `pkg/lsp/control_test.go`：`TestSkeletonSnapshot`（`ProjectRoot`、`Trusted`、`ToolRegistered` 原样反映 options；`FeatureEnabled` 随 `features.lsp` 变化；`Servers` 非 nil 且为空）；`TestSkeletonControlActionsReportNotAvailable`（`SetEnabled` 等返回非 nil 错误）。
- `pkg/lsp/resolve_test.go`：`TestToolEnabledSkeletonIsFalse`。
- `pkg/run/factory_test.go`：`TestIsolatedRunnerInheritsCodeIntel`（用一个实现两个接口的测试桩作为 Owner 的 `Deps.CodeIntel` 等；`NewIsolatedRunner` 得到的 Runner 三个字段与 Owner 相同；Owner 为 nil 时三个字段为零值）。照该文件已有测试的构造方式。
- `pkg/process/open_test.go`：`TestOpenWiresCodeIntelligence`，照 `:148 TestOpenFilesSessionsUnderTheLaunchProjectScope` 的准备方式调用 `Open`，断言 `env.LSP != nil`、`env.Deps.CodeIntel != nil`、`env.Deps.CodeIntelControl != nil`、`env.Deps.CodeIntelTool == false`、`env.Deps.CodeIntelControl.Snapshot().ProjectRoot` 等于项目根；`env.Close()` 之后 `env.LSP.NewManager(...)` 返回的 manager 已 closed（`Subscribe` 返回的 cancel 可调用）。

## 完成判据

- [ ] `ls pkg/lsp/*.go | grep -v _test.go` 恰好是 `control.go doc.go manager.go pool.go resolve.go`
- [ ] `for d in tool run process; do ls pkg/$d/*.go | grep -v _test.go | wc -l; done` 三个都仍是 20
- [ ] `grep -n '"lsp": {"event", "tool"}' pkg/architecture/cache_test.go` 有一行；`grep -n '"process": {current: 19' pkg/architecture/cache_test.go` 有一行
- [ ] `graph.json` 已重新生成并随本任务提交
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -timeout 20m` 全部 `ok`；`go vet ./...`、`gofmt` 干净
- [ ] README 中任务 03 状态为 `DONE`

## STOP 条件

- 「现状」里的行号附近找不到对应代码（如 `buildEntryLocked` 的 `deps := run.Deps{` 或 `closePoolEntries`）。
- 需要在 `pkg/tool`、`pkg/run`、`pkg/process` 新建生产文件，或给 `run.Runner` 加方法/字段（`TestRunnerOnlyShrinks` 会失败）。
- 任何工具表、系统提示或 golden 输出发生变化。
- `pkg/architecture` 的其他测试（不是本任务修改的三张表）失败。

## 维护说明

- 本任务之后，组合根的接线不再改动：任务 08 在 `Pool`/`Manager` 内部实现真实行为；任务 06 实现 `ToolEnabled`；任务 15 在 `ToolEnabled` 与 Manager 内部读取项目级条目。
- `Deps.CodeIntel` 等字段是界面访问 LSP 的唯一入口（`r.CodeIntelControl` 是提升字段）；不要给 `Runner` 加访问方法。
- 评审重点：nil 接口陷阱（不要把 nil 的 `*lsp.Manager` 赋给接口字段）；`Environment.Close` 的顺序（Runner → RunnerPool → LSP → 遥测 → SQL）。
