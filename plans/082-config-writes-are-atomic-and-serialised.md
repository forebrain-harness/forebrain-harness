# Plan 082：`forebrain.yaml` 的保存改为临时文件 + rename（原子），子代理与 MCP 的写接口串行化读改写

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不建 worktree、不 commit、不 push、不 stash；改动留在当前工作区。
> 开工前先记快照，收尾时对比：`git status --short > "$TMPDIR/082-before.txt"`。
>
> **环境**（每个 shell 都要）：
> ```
> cd /Users/doudou/workspace/unionj-cloud/forebrain-harness
> export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
> export FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1
> ```
> 若 `go test` 报 `Package pdfium was not found in the pkg-config search path`，先跑 `make pdfium pdfium-fonts`。
>
> **与 079 的先后**：`pkg/gateway/subagents_api.go` 是工作区里**未提交**的 079 文件（未跟踪）。就在工作区里直接改，不要 `git checkout`/`git stash`；建议在 plan 080 之后做（080 不碰网关，但同一批人审）。
>
> **Drift check（先跑）**：以下 6 条必须有输出（第 1 条恰好 2 行，其余各 1 行），否则 STOP：
> ```
> grep -n 'return os.WriteFile(path, b, 0o600)' pkg/config/load.go        # 恰好 2 行（json 与 yaml 两路）
> grep -n 'func (s \*Server) saveAndReload(cfg config.Root) error' pkg/gateway/api_extra.go
> grep -n 'func (s \*Server) writeAgentSubagents' pkg/gateway/subagents_api.go
> grep -n 'func (s \*Server) handleSubagentsDefaultPut' pkg/gateway/subagents_api.go
> grep -n 'func (s \*Server) writeMCPServer' pkg/gateway/mcp_write_api.go
> grep -n 'ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0' pkg/process/config_reload.go
> ```

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED（动的是用户主配置的落盘方式；符号链接 / 容器绑定挂载的配置文件必须不被弄坏）
- **Depends on**: none
- **Category**: bug（数据完整性 / 并发）
- **Planned at**: commit `3096298` + 工作区（079 未提交），2026-10-19

## Why this matters

`forebrain.yaml` 存着用户的模型服务、密钥引用、MCP、子代理配置。保存它有两个缺陷：

1. **落盘不是原子的**：`config.Save` 用 `os.WriteFile`，先截断再写。写到一半进程崩溃 / 磁盘满，文件就是空的或半截的，下次启动配置读不出来；同时有别的读者（终端进程、热重载、另一端）可能读到半截内容。
2. **网关的读改写没有互斥**：网页的每个写接口都是 `persistedConfig()`（读文件）→ 改 → `saveAndReload`（再读一次、写）。两个请求并发（比如在子代理表格里连点两个开关、两个浏览器标签、MCP 与子代理同时保存），后写的覆盖先写的，**先写的改动静默丢失**。079 给子代理页加了“每个开关一次读改写”，把这个窗口放大了。网关里这样的读改写共 16 处。

本计划：`Save` 改成同目录临时文件 + `fsync` + `rename`（解析符号链接、保留权限、改不了时退回原地写），并给两条最频繁的写路径（子代理配置、MCP 配置）加进程内互斥。其余写路径留作后续（见 Maintenance notes）。

## Current state

- `pkg/config/load.go:251 Save(path, r Root) error`：校验后分两路——`:263-267` 扩展名 `.json`：`b, _ := json.MarshalIndent(r, "", "  "); return os.WriteFile(path, b, 0o600)`；`:269-273` 其余按 YAML：`b, err := yaml.Marshal(r); …; return os.WriteFile(path, b, 0o600)`。`Save` 开头会 `LoadPersisted(path)` 读旧文件以恢复密钥。`appcfg.Save` 的调用者共 7 处（`pkg/turn/models.go:388/409`、`pkg/migrate/{codex_assets,lsp,assets}.go`、`pkg/process/runtime.go:120`、`pkg/gateway/api_extra.go:4771`），**改 `Save` 内部即全部受益**。
- `pkg/gateway/api_extra.go:4750 persistedConfig()` 读、`:4766 saveAndReload(cfg)` = `config.Save(path, cfg)` + `s.Env.ReloadConfig()`；两者各自无锁。
- `pkg/gateway/subagents_api.go`
  - `:522 writeAgentSubagents(w, r, mutate)`：`subagentAgentID` → `persistedConfig()`（`:528`）→ `mutate(write)`（里面会 `decodeSubagentBody(r, …)`）→ `write.store()` → `config.ValidateAgentSubagents` → `saveAndReload(write.cfg)`（`:549`）→ `s.rebindAgentRunners()`（`:553`）→ `writeAgentsJSON(w, payload)`。
  - `:650 handleSubagentsDefaultPut`：`decodeSubagentSwitch` → `persistedConfig()`（`:658`）→ 改全局开关 → `saveAndReload`（`:664`）→ `rebindAgentRunners()`（`:671`）。
- `pkg/gateway/mcp_write_api.go`：`:680 writeMCPServer(w, r, resolve)` 是 POST / PUT / DELETE 三个写接口的唯一入口：`resolve(r)` 读配置得到 `layer`（含 `layer.entries` 快照）→ `createMCPServer/updateMCPServer/deleteMCPServer` 用快照算出 `next` → `saveMCPLayer`（`:604`）里 `mcpConfigForWrite()`（再读一次）→ 整层替换 → `saveAndReload` → `s.rebindAgentRunners()`（`:635`）。快照与整层替换之间没有锁，是典型的丢更新。
- `s.rebindAgentRunners()`（`mcp_write_api.go:642`）= `RunnerPool.RebindAgent` → `Close()` 拆池，每个 runner 的关闭有总期限 `runnerPoolCloseDeadline = 20 * time.Second`（`pkg/process/runner_pool.go:845`）。
- 热重载监视器 `pkg/process/config_reload.go:StartConfigHotReload`：**监视目录而不是文件**，事件按 `filepath.Base(ev.Name) == base` 过滤，`Write|Create|Rename` 都会触发 400ms 防抖后的重载。所以：原子替换产生的临时文件事件（名字不同）被忽略，最后一步 rename 到目标名产生一个 `Create/Rename` 事件被接住——**监视器与原子写兼容，不需要改**。
- 仓库里已有的原子写范例（逻辑相同，各包各一份，**本计划不去合并它们**）：`pkg/lsp/resolve.go:589 writeFileAtomic`（CreateTemp + Rename）、`pkg/tool/file_tools.go:953 atomicWrite`（保留已有文件权限）。
- 惯例：测试文件必须与被测生产文件同名（`pkg/architecture` 强制），所以新文件 `pkg/config/atomic_write.go` 配 `pkg/config/atomic_write_test.go`。网关夹具范例：`pkg/gateway/subagents_api_test.go:36 newSubagentFixture`（真 `forebrain.yaml` + 生产路由表）、`:381 TestSubagentBuiltinSwitchAndLinkage`（`f.do(t, http.MethodPut, "/api/agents/primary/main/subagents/plan/enabled", map[string]any{"enabled": false})`）。

## Commands you will need

| Purpose | Command | Expected |
|---|---|---|
| 配置包 | `go test -tags fts5 ./pkg/config -count=1` | `ok` |
| 网关 | `go test -tags fts5 ./pkg/gateway -count=1` | `ok` |
| 竞态 | `go test -tags fts5 -race ./pkg/config ./pkg/gateway -run 'Atomic\|WithConfigWrite\|DoNotLoseUpdates\|Save' -count=3` | `ok` |
| 相关包 | `go test -tags fts5 ./pkg/process ./pkg/turn ./pkg/migrate ./cmd/forebrain ./pkg/architecture -count=1` | `ok` ×5 |
| vet / gofmt | `go vet ./pkg/... ./cmd/...`；`gofmt -l cmd pkg third_party` | 无输出 |

## Scope

**In scope**：
- `pkg/config/atomic_write.go`（新建）、`pkg/config/atomic_write_test.go`（新建）、`pkg/config/load.go`、`pkg/config/load_test.go`
- `pkg/gateway/server.go`（只给 `Server` 加一个字段）、`pkg/gateway/api_extra.go`（只加 `withConfigWrite`）、`pkg/gateway/api_extra_test.go`（加互斥单测）
- `pkg/gateway/subagents_api.go`、`pkg/gateway/subagents_api_test.go`
- `pkg/gateway/mcp_write_api.go`
- `pkg/process/config_reload_test.go`（只加一个用例）

**Out of scope**：
- 其余 12 处读改写点（`agents_api.go:555/662/699`、`api_extra.go:5296/5331/5465/5509/5533/5557/6816`、`handleConfigFile` 的 PUT `:4815`）：留作后续，见 Maintenance notes。
- `.env` 文件的读改写；`config_reload.go` 的监视逻辑（已兼容）。
- 把仓库里那几份 `writeFileAtomic` 合并成一份。
- 不把 `rebindAgentRunners` 挪出 MCP 的锁（见 Step 3 的“已知代价”）。

## Steps

### Step 0：基线

记快照；跑 Drift check；`go test -tags fts5 ./pkg/config ./pkg/gateway -count=1`。
**Verify**: grep 命中；两包 `ok`。否则 STOP。

### Step 1：原子写

新建 `pkg/config/atomic_write.go`：

```go
package config

import (
	"log/slog"
	"os"
	"path/filepath"
)

// renameFile is os.Rename; tests replace it to make the replace step fail.
var renameFile = os.Rename

// writeFileAtomic replaces path with data so no reader ever sees a partial
// file: the bytes go to a sibling temp file, are synced, and the temp file is
// renamed over the target.
//
// Three behaviours keep it a drop-in for os.WriteFile on a config file:
//   - a symlinked path is written through to its target, so a dotfiles-managed
//     link stays a link;
//   - an existing file keeps its permission bits (perm only applies to a file
//     that does not exist yet);
//   - where a sibling cannot be created or the rename is refused (a read-only
//     directory, a bind-mounted single file in a container), it falls back to
//     the in-place write the file always had, and says so once in the log.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	if info, err := os.Stat(target); err == nil {
		perm = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return writeInPlace(target, data, perm, err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := renameFile(name, target); err != nil {
		_ = os.Remove(name)
		return writeInPlace(target, data, perm, err)
	}
	return nil
}

func writeInPlace(target string, data []byte, perm os.FileMode, cause error) error {
	slog.Warn("config: atomic replace unavailable, writing in place", "path", target, "err", cause)
	return os.WriteFile(target, data, perm)
}
```

`pkg/config/load.go:Save`：把 json 与 yaml 两处 `os.WriteFile(path, b, 0o600)` 都换成 `writeFileAtomic(path, b, 0o600)`（json 那处原来忽略 `MarshalIndent` 的错误，**保持原样不动**）。

**Verify**: `go build -tags fts5 ./pkg/config` 通过；`go test -tags fts5 ./pkg/config -count=1` → `ok`（既有用例不应变化）。

### Step 2：原子写的测试

新建 `pkg/config/atomic_write_test.go`（包 `config`，可直接引用未导出的 `writeFileAtomic` / `renameFile`）：

1. `TestWriteFileAtomicReplacesAndKeepsMode`：新文件按传入的 `0o600` 创建；已存在的 `0o640` 文件写后仍是 `0o640`，内容为新内容；目录里**没有**以 `.<name>.tmp-` 开头的残留。（Windows 的权限位语义不同：用 `runtime.GOOS == "windows"` 跳过模式断言，保留内容与残留断言。）
2. `TestWriteFileAtomicWritesThroughASymlink`：`link -> real`，对 `link` 写；断言 `link` 仍是符号链接（`os.Lstat` 的 mode 有 `ModeSymlink`）、`real` 内容已更新。符号链接创建失败（Windows 无权限）时 `t.Skip`。
3. `TestWriteFileAtomicFallsBackInPlace`：把 `renameFile` 临时替换成总返回错误的函数（`t.Cleanup` 还原），写一个已存在的文件；断言返回 `nil`、内容已更新、没有残留临时文件。
4. `TestWriteFileAtomicNeverExposesAPartialFile`：两份 256 KiB 的不同内容 A、B；一个 goroutine 循环读目标文件，另一个交替写 A、B 共 200 次；每次读到的内容必须**恰好等于** A 或 B（允许读到文件尚不存在之前的错误，但不允许读到空 / 半截）。用 `sync.WaitGroup` + 一个停止通道收尾。该用例在 `os.WriteFile` 原地写下可能失败，在原子写下必须稳定通过。
5. 在 `pkg/config/load_test.go` 加 `TestSaveLeavesNoTempFileAndKeepsMode`：`Save` 一个最小 `Root` 到临时目录里的 `forebrain.yaml` 两次，目录里只剩这一个文件，权限为 `0o600`；再对 `.json` 路径做一次。

**Verify**: `go test -tags fts5 ./pkg/config -count=1` → `ok`；`go test -tags fts5 -race ./pkg/config -run 'WriteFileAtomic|SaveLeavesNoTempFile' -count=3` → `ok`。

### Step 3：网关互斥

1. `pkg/gateway/server.go` 的 `Server` 结构体加字段（紧挨 `Env` 之类的字段，写一句注释）：
   ```go
   // configWriteMu serialises the read-modify-write of forebrain.yaml across
   // request goroutines: persistedConfig → change → saveAndReload would
   // otherwise let the later write overwrite the earlier one's change.
   configWriteMu sync.Mutex
   ```
   `go vet ./pkg/gateway` 若报 `copylocks`（某处按值复制了 `Server`），STOP 报告是哪一处，**不要**为了过 vet 改别处的复制；退路是把互斥改成包级变量 `var configWriteMu sync.Mutex`（评审方接受，一个进程只有一份配置文件）。
2. `pkg/gateway/api_extra.go`，紧挨 `saveAndReload` 加：
   ```go
   // withConfigWrite runs one read-modify-write of forebrain.yaml with every
   // other such write in this process held off. fn must read the file (via
   // persistedConfig) inside the call, not before it, and must not call
   // withConfigWrite itself.
   func (s *Server) withConfigWrite(fn func() error) error {
   	s.configWriteMu.Lock()
   	defer s.configWriteMu.Unlock()
   	return fn()
   }
   ```
3. `writeAgentSubagents`（`subagents_api.go:522`）重构为：先 `subagentAgentID`（不涉及配置读写，留在锁外）；然后

   ```go
   var payload any
   err = s.withConfigWrite(func() error {
       cfg, err := s.persistedConfig()
       if err != nil { return err }
       // …原有：补 Definitions map、write := &subagentWrite{…}、payload, err = mutate(write)、write.store()、
       //     ValidateAgentSubagents（错误包成 subagentBadRequest）、saveAndReload(write.cfg)
       return nil
   })
   if err != nil { failSubagentWrite(w, err); return }
   s.rebindAgentRunners()      // 锁外：拆池可能慢
   writeAgentsJSON(w, payload)
   ```
   各处错误原来怎么映射状态码，现在就怎么映射（`mutate` 返回的 `*subagentAPIError` 原样透传）；不改任何文案。
4. `handleSubagentsDefaultPut`：`decodeSubagentSwitch` 留在锁外；`persistedConfig` → 改 `EnableSubagent` → `saveAndReload` 放进 `withConfigWrite`；`rebindAgentRunners()` 与应答在锁外。
5. `writeMCPServer`（`mcp_write_api.go:680`）：函数最开头加
   ```go
   s.configWriteMu.Lock()
   defer s.configWriteMu.Unlock()
   ```
   整个函数体（`resolve(r)` → 三个处理器 → `saveMCPLayer`）都在锁内，这样 `layer.entries` 快照与整层替换之间不会插进别的写入。**已知代价**：`saveMCPLayer` 末尾的 `rebindAgentRunners()` 也在锁内，最坏情况（某个 MCP 子进程不响应取消）持锁最长 `runnerPoolCloseDeadline`（20 秒），期间其它配置写入排队；平时是毫秒级。接受这个代价，不要为此重构 `saveMCPLayer`。项目层（写项目目录里的文件）也会取这把锁，无害。
   `saveMCPLayer`、`mcpConfigForWrite`、`persistedConfig`、`saveAndReload` **自己不加锁**（否则重入死锁）。

**Verify**: `go build -tags fts5 ./pkg/gateway` 通过；`go vet ./pkg/gateway` 无输出；`go test -tags fts5 ./pkg/gateway -count=1` → `ok`（既有的子代理与 MCP 写接口用例全部不变）。

### Step 4：互斥与丢更新的测试

1. `pkg/gateway/api_extra_test.go`：`TestWithConfigWriteSerialisesWriters`（确定性）——`s := &Server{}`；8 个 goroutine 各调一次 `s.withConfigWrite`，回调里用 `atomic` 记录当前并发数与历史最大并发数，中间 `time.Sleep(5 * time.Millisecond)`；全部结束后断言最大并发数 == 1，且 8 个回调都跑过。
2. `pkg/gateway/subagents_api_test.go`：`TestSubagentRowSwitchesDoNotLoseUpdates`——`f := newSubagentFixture(t, nil)`；同时（`sync.WaitGroup`，一个起跑通道让它们尽量同时开始）对 `general-purpose`、`explore`、`plan`、`verification` 四行各发一次 `PUT /api/agents/primary/main/subagents/<name>/enabled {"enabled": false}`，状态码收集进切片（**不要在 goroutine 里调用 `require`/`t.Fatal`**，回到主 goroutine 再断言）；断言 4 个都是 200，磁盘上 `f.subagentEntries(t)` 里这四个名字的条目都存在且 `Enabled` 为 `false`，`f.mainDefinition(t).EnableSubagent` 为 `false`（全部关闭 → 总开关随之关闭，见 `TestSubagentRowSwitchLinkageWhenEveryRowIsOff`）。
   这个用例在没有互斥时**可能**偶发失败（窗口很短），以 `-count=20` 观察红是加分项而不是硬要求；确定性的保证来自上一条。
3. `pkg/process/config_reload_test.go`：`TestHotReloadSeesAnAtomicSave`——`env := &Environment{ConfigPath: filepath.Join(t.TempDir(), "forebrain.yaml")}`，`env.reload = NewConfigManager(nil, func() error { calls.Add(1); return nil })`（`calls` 是 `atomic.Int32`），`StartConfigHotReload(ctx, env)`，`t.Cleanup(env.stopConfigHotReload)`；用 `appcfg.Save(env.ConfigPath, appcfg.Root{})` 保存一次；最多等 3 秒（轮询）直到 `calls.Load() >= 1`，否则 `t.Fatal`。这个用例在 Step 1 之前后都应通过——它钉住的是“监视器与原子写兼容”。

**Verify**:
- `go test -tags fts5 ./pkg/gateway -run 'WithConfigWrite|DoNotLoseUpdates' -count=1 -v` → 新用例 PASS。
- `go test -tags fts5 -race ./pkg/gateway -run 'WithConfigWrite|DoNotLoseUpdates' -count=3` → `ok`。
- `go test -tags fts5 ./pkg/process -run 'TestHotReloadSeesAnAtomicSave' -count=1 -v` → PASS。

### Step 5：收口

1. `go vet ./pkg/... ./cmd/...`、`gofmt -l cmd pkg third_party` 无输出。
2. `go test -tags fts5 ./pkg/config ./pkg/gateway ./pkg/process ./pkg/turn ./pkg/migrate ./cmd/forebrain ./pkg/architecture -count=1` → 全 `ok`（`pkg/turn`、`pkg/migrate`、`cmd/forebrain` 里有直接或间接走 `Save` 的用例，必须不变）。
3. 手工确认热重载没坏（可选但建议）：`FOREBRAIN_HOME="$(mktemp -d)"` 下启动 `go run ./cmd/forebrain gateway` 之外的路径太重，**用 Step 4.3 的用例代替**即可，不必起服务。
4. 快照对比：`git status --short > "$TMPDIR/082-after.txt"; diff "$TMPDIR/082-before.txt" "$TMPDIR/082-after.txt"` → 只多出 `pkg/config/atomic_write.go` 与 `pkg/config/atomic_write_test.go` 两个新文件；`git diff --stat` 里其余文件都在 Scope 内。

## Test plan

- 原子写 5 个用例（Step 2）+ 互斥 1 个确定性用例 + 丢更新 1 个并发用例 + 监视器兼容 1 个用例。
- 范例：`pkg/lsp/resolve.go` 里的原子写（实现形状）；`pkg/gateway/subagents_api_test.go:381`（写接口请求形状）；`pkg/process/config_reload_test.go:25`（`Environment` 的最小构造）。
- 红绿证据写进报告：`TestWriteFileAtomicNeverExposesAPartialFile` 在把 `writeFileAtomic` 临时改回 `os.WriteFile` 时应失败或至少在 `-count=20` 下偶发失败（这一步是验证“测试能抓到问题”，验证完**立刻还原**，不要把改回的版本留在工作区）。

## Done criteria

- [ ] `go test -tags fts5 ./pkg/config ./pkg/gateway ./pkg/process ./pkg/turn ./pkg/migrate ./cmd/forebrain ./pkg/architecture -count=1` 全 `ok`
- [ ] `go test -tags fts5 -race ./pkg/config ./pkg/gateway -run 'WriteFileAtomic|SaveLeavesNoTempFile|WithConfigWrite|DoNotLoseUpdates' -count=3` 全 `ok`
- [ ] `grep -n "os.WriteFile" pkg/config/load.go` 在 `Save` 里没有命中（只剩 `atomic_write.go` 里的回退写）
- [ ] 8 个新增用例存在并通过（5 + 1 + 1 + 1）
- [ ] `go vet ./pkg/... ./cmd/...`、`gofmt -l cmd pkg third_party` 无输出
- [ ] 与开工前快照比较：只多两个新文件，无 Scope 外变化；没有 commit / 分支 / worktree
- [ ] 报告里写明：`Server` 的互斥是字段还是退到了包级变量

## STOP conditions

- Drift check 不符，或 `Save` 里 `os.WriteFile` 的位置与 Current state 不一致。
- `go vet` 报 `copylocks` 且退路（包级变量）也不可行。
- 加锁后任何既有网关用例**死锁或超时**（说明某条被锁住的路径又回调了被锁住的函数）——不要加 `TryLock`/超时去绕，停下报告调用链。
- 原子写的符号链接用例或回退用例在你的平台上无法成立，且不是“Windows 无权限创建符号链接”这一类可 `Skip` 的原因。
- 需要改 Scope 外的文件，或想顺手去锁另外 12 处读改写点。
- 任一步验证连续两次修复后仍失败。

## Maintenance notes

- **后续**（本计划故意没做）：把其余 12 处读改写点逐个收进 `withConfigWrite`——`pkg/gateway/agents_api.go:555/662/699`、`api_extra.go:5296/5331/5465/5509/5533/5557/6816`、`handleConfigFile` 的 PUT（`:4815`）。配方：把 `persistedConfig()` 到 `saveAndReload()` 的整段放进回调，`rebindAgentRunners()` 和应答留在锁外。在它们都迁完之前，这把锁只保护已接入的写路径之间不丢更新；原子写对所有 `Save` 调用者（含终端、迁移、`/model` 持久化）已经生效。
- `writeAgentSubagents` 的 `mutate` 回调里会读请求体（`decodeSubagentBody`，上限 1 MiB），所以一个读得极慢的客户端会在持锁期间拖住其它配置写入；本期接受（接口需要登录、只有本机用户），若要消除，把解码挪到锁外需要拆开 `mutate` 的两个阶段。
- 进程之间（终端进程与网关进程同时改同一份配置）仍无互斥；原子写保证的是“谁都不会读到半截文件”，不是“两个进程的改动不互相覆盖”。需要时再考虑文件锁，本期不做。
- 回退到原地写的场景（只读目录、容器里绑定挂载的单个文件）会在日志里打一条 `config: atomic replace unavailable`；若在 k8s 配置映射挂载（`deploy/k8s`）里频繁出现，说明这类部署实际一直在原地写，行为与改动前相同。
- 监视器与本改动兼容的依据是它监视目录并按文件名过滤；若将来有人把它改成监视文件本身，原子替换会让监视丢失（inode 变了），必须同时改监视方式。
- Review 重点：`writeAgentSubagents` 重构后每条错误路径的状态码是否与改动前一致（既有用例覆盖）；`writeMCPServer` 的锁是否覆盖了 `resolve(r)`；`rebindAgentRunners` 在子代理两条路径里是否已挪到锁外。
