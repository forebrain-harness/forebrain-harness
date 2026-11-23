# Plan 162：退出时删掉 shell diff 的私有 git 目录（含用户索引副本）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func forgetShellDiffRepo" pkg/tool/shell_diff.go` 为 `1`；
> `grep -c "func shellDiffSweep" pkg/tool/shell_diff.go` 为 `1`；
> `grep -c "func Execute()" cmd/forebrain/root.go` 为 `1`；
> `grep -c "forgetShellDiffRepo" pkg/tool/shell_diff_test.go` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/tool/shell_diff.go cmd/forebrain/root.go` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./cmd/forebrain -count=1`（超时给足 900 s）。

## Status

- **Priority**: P2（每个进程每个仓库留一份含索引副本的临时目录，最长 24 小时；单份可达数百 MB）
- **Effort**: S（一个 10 行的导出函数 + 调用一行 + 1 个测试）
- **Risk**: LOW（只在进程退出时删自己建的临时目录；测试辅助函数已证明这条删除路径是安全的）
- **Depends on**: 无
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: perf（磁盘）/ 资源泄漏

## Why this matters

shell 命令的"改动前后对比"用的是每个仓库一个**私有 git 仓库**：目录名
`os.TempDir()/forebrain-shell-diff/<pid>-<hash>-<rand>`（`shell_diff.go:345`），里面有一份**用户索引的副本**
（`seedIndex`，`setup` 时 `os.ReadFile` 后写出，`shell_diff.go:402-410`；索引上限 256 MB）以及提交过的树对象。

**没有任何正常的回收路径**：

- 唯一删除私有目录的地方是**失败恢复**（`shell_diff.go:484-485`）与测试辅助 `forgetShellDiffRepo`（`:1157`）；
- 崩溃兜底 `shellDiffSweep`（`:261-277`）每进程只跑一次，且**跳过自己的 pid 前缀**：
  `if !e.IsDir() || strings.HasPrefix(e.Name(), prefix) { continue }`，只删"不是本进程的、且超过 24 小时没动"的目录。

于是：**每次 CLI 运行、每个 gateway/TUI 进程**，只要用过一次 shell 命令（哪怕只是 `ls`），就在临时目录里留下
一个私有 git 仓库，一直留到 24 小时后被**另一个**进程的 sweep 扫掉。大索引仓库（monorepo、含大二进制的仓库）
单次可达数十至数百 MB；机器上短命进程越多，堆积越快。

要做成：**进程退出时释放自己名下的全部私有目录**（复用已经写好、已经被测试覆盖的
`forgetShellDiffRepo`：它摘掉注册表条目并 `os.RemoveAll(r.private)`），sweep 继续负责崩溃残留。

## Current state

注册表与私有目录（`pkg/tool/shell_diff.go`）：

```go
var shellDiffRepos = struct {                      // :154
	sync.Mutex
	m map[string]*shellDiffRepo
}{m: map[string]*shellDiffRepo{}}

type shellDiffRepo struct {
	mu           sync.Mutex
	top          string   // work tree root
	seedIndex    string
	…
	private      string   // private GIT_DIR; "" until set up
	…
}
```

建立私有目录（`shellDiffRepoFor` `:296` → `setup` `:344`）：

```go
	base := filepath.Join(os.TempDir(), "forebrain-shell-diff")     // :345
	…
	r.seedIndex = filepath.Join(gitDir, "index")                    // :328
	…
	if r.seedIndex != "" {
		if data, err := os.ReadFile(r.seedIndex); err == nil {      // :402-403
```

唯一可用、且已经正确的删除路径（`pkg/tool/shell_diff.go:1154-1166`）：

```go
// forgetShellDiffRepo is the test helper that detaches a repository's watcher
// (removing its private directory) so one test's registry state cannot leak
// into the next.
func forgetShellDiffRepo(top string) {
	shellDiffRepos.Lock()
	r := shellDiffRepos.m[top]
	delete(shellDiffRepos.m, top)
	shellDiffRepos.Unlock()
	if r != nil {
		os.RemoveAll(r.private)
	}
}
```

崩溃兜底（`:261-277`，行为不改）：

```go
func shellDiffSweep(base string) {
	…
	cutoff := time.Now().Add(-24 * time.Hour)
	prefix := fmt.Sprintf("%d-", os.Getpid())
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		…
	}
}
```

进程入口（`cmd/forebrain/root.go:32-48`，**唯一的常规退出路径**；`os.Exit(1)` 在错误分支里）：

```go
func Execute() {
	rootCmd.Version = home.Version
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	if err := rootCmd.Execute(); err != nil {
		if errors.Is(err, tui.ErrCancelled) {
			return
		}
		rh := ""
		if root, derr := home.Root(); derr == nil { rh = root }
		telemetry.ErrorThenFprintln(rh, os.Stderr, err, "cmd/forebrain rootCmd.Execute")
		os.Exit(1)
	}
}
```

`cmd/forebrain` 目前**没有** import `pkg/tool`（`grep -rn 'pkg/tool"' cmd/forebrain/*.go` 无输出），需要新增。

## Scope

**In scope（只改这些）**：
- `pkg/tool/shell_diff.go`（新增导出 `CloseShellDiffRepos`；顺带把 `forgetShellDiffRepo` 的注释从"test helper"改成两份职责）
- `pkg/tool/shell_diff_test.go`（新增 1 个测试）
- `cmd/forebrain/root.go`（`Execute` 里调用一次；新增 import）

**Out of scope（看着相关也不要动）**：
- `shellDiffSweep` 的 24 小时窗口与"跳过本 pid"规则（它防的是**活着的**另一个进程的目录被误删，不能动）
- 私有仓库的建立方式、索引副本、`large` 文件的签名逻辑
- `os.Exit(130)` 的 `hideLaunchCaret` 路径（启动期信号退出，那时不可能已经建过私有目录）
- `.gitignore` / 临时目录位置（不搬到 home 下，也不加配置项）
- 崩溃场景的兜底（sweep 已经覆盖）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run '^TestCloseShellDiffReposRemovesThePrivateDirectories$' -count=1 -v` | `--- PASS` |
| 本包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` | 与基线相比无新增 FAIL |
| CLI 回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./cmd/forebrain -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool ./cmd/forebrain` | exit 0 |
| gofmt | `gofmt -l pkg/tool cmd/forebrain` | 无输出 |

## Steps

### Step 1：先写会红的测试

`pkg/tool/shell_diff_test.go` 里新增（`newShellDiffRepo` 会建一个带提交的临时仓库，并在 `t.Cleanup` 里
调 `forgetShellDiffRepo`；`shellDiffRepoFor` + `repo.snapshot` 是文件里 `:368-376` 已有的用法）：

```go
// The private repository holds a copy of the user's index; a process that ran one
// shell command must not leave it behind for the 24-hour sweep.
func TestCloseShellDiffReposRemovesThePrivateDirectories(t *testing.T) {
	ctx := context.Background()
	root := newShellDiffRepo(t)
	repo := shellDiffRepoFor(ctx, root)
	if _, err := repo.snapshot(ctx); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if strings.TrimSpace(repo.private) == "" {
		t.Fatal("a snapshot must set up the private repository")
	}
	if _, err := os.Stat(repo.private); err != nil {
		t.Fatalf("the private directory must exist before the release: %v", err)
	}

	CloseShellDiffRepos()

	if _, err := os.Stat(repo.private); !os.IsNotExist(err) {
		t.Fatalf("the private directory must be gone after the release, stat err = %v", err)
	}
	if _, ok := shellDiffRepos.m[root]; ok {
		t.Fatal("the registry entry must be dropped as well")
	}
	CloseShellDiffRepos() // 可重复调用
}
```

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run '^TestCloseShellDiffReposRemovesThePrivateDirectories$' -count=1 -v`
→ FAIL（`undefined: CloseShellDiffRepos`）。若直接 PASS，STOP 3。

### Step 2：实现释放函数

在 `forgetShellDiffRepo`（`:1157`）**上方**新增，并把它的注释改准确（它不再只是测试辅助）：

```go
// CloseShellDiffRepos detaches every watcher and removes its private repository
// directory, then leaves the registry empty. It runs once as the process exits
// (cmd/forebrain's Execute): without it a run that captured one shell command
// leaves a private git dir — with a copy of the user's index — behind until some
// other process's 24-hour sweep finds it. Safe to call more than once.
func CloseShellDiffRepos() {
	shellDiffRepos.Lock()
	tops := make([]string, 0, len(shellDiffRepos.m))
	for top := range shellDiffRepos.m {
		tops = append(tops, top)
	}
	shellDiffRepos.Unlock()
	for _, top := range tops {
		forgetShellDiffRepo(top)
	}
}
```

`forgetShellDiffRepo` 的注释改成："detaches one repository's watcher and removes its private directory.
The test fixtures call it between cases; CloseShellDiffRepos calls it for every repository at exit."

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` → exit 0。

### Step 3：在进程退出时调用

`cmd/forebrain/root.go`：

1. import 里加 `"github.com/forebrain-harness/forebrain-harness/pkg/tool"`（放在现有本仓 import 组里，
   与 `pkg/home`、`pkg/telemetry`、`pkg/tui` 按该组的排序规则插入）。
2. `Execute` 改成"先取 err、再释放、再决定退出码"：

```go
func Execute() {
	rootCmd.Version = home.Version
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	err := rootCmd.Execute()
	// The shell diff capture keeps a private git repository per work tree for
	// the process's lifetime; releasing it here is what keeps a CLI run from
	// leaving one behind (the sweep only covers crashed processes).
	tool.CloseShellDiffRepos()
	if err == nil {
		return
	}
	if errors.Is(err, tui.ErrCancelled) {
		return
	}
	rh := ""
	if root, derr := home.Root(); derr == nil {
		rh = root
	}
	telemetry.ErrorThenFprintln(rh, os.Stderr, err, "cmd/forebrain rootCmd.Execute")
	os.Exit(1)
}
```

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run '^TestCloseShellDiffReposRemovesThePrivateDirectories$' -count=1 -v`
→ PASS；`CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0。

### Step 4：回归

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./cmd/forebrain -count=1`
→ 与基线相比无新增 FAIL。特别注意 `pkg/tool` 里所有 `shell_diff` 用例与 `cmd/forebrain` 的
`TestExecute*`/root 相关用例。

### Step 5（可选但推荐）：真机确认目录确实消失

在**自己的临时目录**里做，不要动 owner 的仓库：

```bash
tmp=$(mktemp -d) && cd "$tmp" && git init -q . && git -c user.email=t@t -c user.name=t commit -q --allow-empty -m init
before=$(ls "$TMPDIR/forebrain-shell-diff" 2>/dev/null | wc -l)
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go run ./cmd/forebrain --help >/dev/null 2>&1
after=$(ls "$TMPDIR/forebrain-shell-diff" 2>/dev/null | wc -l)
echo "before=$before after=$after"
```

（`--help` 不会建私有目录——这一步只验证"调用不破坏正常启动"。真正建目录的是 shell 工具调用，
要在隔离 home 下跑一次 `forebrain` 交互并执行一条命令，成本高；**本步骤属可选**，不作为 Done criteria。）

## Test plan

- 新增 `TestCloseShellDiffReposRemovesThePrivateDirectories`：跑一次 `snapshot` 让私有目录存在 →
  调用 `CloseShellDiffRepos()` → 目录不存在、注册表条目消失、可重复调用。
- 结构照抄 `pkg/tool/shell_diff_test.go` 里既有用例（`newShellDiffRepo` + `shellDiffRepoFor` +
  `repo.snapshot`，`:368-376`）。

## Done criteria

机器可检查，全部满足：

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool ./cmd/forebrain` exit 0
- [ ] `gofmt -l pkg/tool cmd/forebrain` 无输出
- [ ] `… -run '^TestCloseShellDiffReposRemovesThePrivateDirectories$'` PASS
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./cmd/forebrain -count=1` 与基线相比无新增 FAIL
- [ ] `grep -c "func CloseShellDiffRepos" pkg/tool/shell_diff.go` 为 `1`
- [ ] `grep -c "tool.CloseShellDiffRepos()" cmd/forebrain/root.go` 为 `1`
- [ ] `git status --porcelain` 只显示 Scope 里的三个文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1，或 drift check 有输出且被引用函数的语义变了。
2. 基线命令本身跑不起来，拿不到失败清单。
3. 新测试在 Step 1 直接 PASS（`CloseShellDiffRepos` 已存在）。
4. `repo.snapshot(ctx)` 在测试里失败（可能是 git 版本/环境问题）：把错误原文贴出来，**不要**改成"手工 mkdir 一个假目录"
   来凑断言——那样这个测试就不再证明私有目录能被删掉。
5. 你发现除了 `Execute` 还有第二条常规退出路径（例如某个子命令内部 `os.Exit`）：报告它，不要顺手改；
   本计划只覆盖 `Execute`。
6. 你为了让测试通过而想动 `shellDiffSweep` 的窗口或 pid 前缀判断——那会删掉**活着的**另一个进程的目录，
   绝对不要改。

## Maintenance notes

- 谁将来新增"每个进程一个临时工作副本"的机制（另一个私有 git dir、另一个缓存目录），都要接进
  `CloseShellDiffRepos` 或在 `Execute` 里加自己的释放调用；评审时把"有没有释放路径"当准入条件。
- `CloseShellDiffRepos` 在进程退出时调用，此时不应有在飞的 snapshot；若将来要支持"运行中释放"，
  必须先 `e.mu.Lock()` 之类的互斥，否则会删掉正在被 `git` 读的目录。
- 评审重点：`cmd/forebrain/root.go` 的改动只应是"取 err → 释放 → 分支"，不要顺手改 `os.Exit(1)` 的位置或
  telemetry 的调用参数。
