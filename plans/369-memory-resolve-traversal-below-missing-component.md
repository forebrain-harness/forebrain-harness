# Plan 369: 记忆后端 `resolve` 在"缺失组件之后"放过 `..`——跨项目泄漏修复

> **Executor instructions**: Follow this plan step by step. Run every verification
> command and confirm the expected result before moving on. If anything in the
> "STOP conditions" section occurs, stop and report — do not improvise.
>
> **Drift check (run first)**:
> `git diff --stat 80bb73b4..HEAD -- pkg/memory/backend.go pkg/memory/backend_test.go pkg/tool/request_permissions_test.go`
> 三处 in-scope 文件在基线 `80bb73b4` 上只有 `pkg/tool/request_permissions_test.go` 被 367
> 改过（+217 行追加测试）。`pkg/memory/backend.go` 与 `pkg/memory/backend_test.go` 若与本计划
> "Current state" 摘录不符 → STOP。行号漂移不算漂移，以内容为准。

## Status

- **Priority**: P0（安全：跨项目记忆泄漏）
- **Effort**: S
- **Risk**: HIGH（改 `resolve` 的路径判定语义，全部 memory 工具与 `pkg/migrate` 共用）
- **Depends on**: 367（本计划在 367 的 worktree 上继续；`pkg/tool/request_permissions_test.go` 已有 367 的追加）
- **Category**: security
- **Planned at**: commit `80bb73b4`（工作区：worktree `.worktrees/memories-path-anchor`，367 改动未提交）
- **Approved**: owner 2026-11-23 指令「安全问题必须本期一并修复」
- **Executed**: `APPROVE`（2026-11-23，执行器 deepseek-v4.1-flash@xhigh，与 367 同一 worktree／
  分支，改动未提交）。见文末「验收记录」。
- **Landed**: `dc93b1ac`（dev，`git merge --ff-only` from `80bb73b4`，2026-11-23，与 367 同一提交）。
  落地后门禁亲跑：`go build -tags fts5 ./...` ok；`pkg/memory`／`pkg/tool`／`pkg/migrate`／
  `pkg/run`／`pkg/turn` 全 ok；`vet` 0；`gofmt` 净；`git diff` 对本计划三个文件为空（landed == tested）。

## 验收记录（2026-11-23，reviewer 亲跑，非采信执行器报告）

**根因证据链**（`resolve` :218-273 → 提前返回分支 :243-250 → `filepath.Join` 归一化 `..`）由
reviewer 在派发前用 `go test -overlay` 探针实测确认：基线代码下
`zzz/../../../projects/-Other-project/MEMORY.md` 读回 `OTHER PROJECT SECRET`，且把**基线**
`session_tools.go`（不含 367 的任何代码）换进来同样命中 ⇒ 先于 367、367 也未扩大。

**RED 由 reviewer 独立取得**：把**基线** `pkg/memory/backend.go` 用 `-overlay` 换进已修树，
两条新测试均按缺陷变红（`read … accepted, content = "OTHER SCOPE SECRET\n"` /
`memories_read … reached another project: … "OTHER PROJECT SECRET\n"`）；换回后两条 PASS。

**修复后 reviewer 亲跑**：
- 对抗探针（4 条自造越界路径 × read/list/search 共 12 组）全部拒绝、无泄漏；
- 泄漏探针两条原始路径改为 `path '…' must stay within the memories root`；
- `pkg/memory` 8.6s／`pkg/tool` 21.2s／`pkg/migrate` 4.0s／`pkg/run` 49.9s／`pkg/turn` 6.3s 全 `ok`；
- `go vet -tags fts5 ./pkg/memory/ ./pkg/tool/` exit 0；`gofmt -l pkg/memory pkg/tool` 空；
- `grep -c 'component == ".."' pkg/memory/backend.go` = **1**（前置判定；循环内那道已删）；
- `git status --porcelain` = 367 的三个文件 + 本计划的 `backend.go`／`backend_test.go`，无越界。
- 行为矩阵逐条核对为真：可达组件上的 `..` 仍拒、缺失组件后的 `..` 改为拒、
  `missing/../MEMORY.md` 改为拒（刻意的修订）、root 内绝对路径仍允许、越界绝对路径仍拒、
  空/blank 仍落到根、不含 `..` 的缺失路径仍走 not found + 367 的 anchor hint、dotfile 仍 not found。

**tmux 真机验收：不适用（已说明理由）**。本缺陷只能由模型侧发出的 `memories_*` 调用触发，
没有 CLI／TUI 入口可以注入这样一条路径；四类真机场景（复现→修复、回归、中断自救、非 TTY）
都是面向界面的。等价证据是 `pkg/tool` 的 handler 级测试——它跑的就是模型调用的同一段生产代码，
再加上面 `-overlay` 探针的 RED/GREEN。若日后要有真机覆盖，得先给内存工具造一个可脚本化的
入口，那是另一个决定。

## Why this matters

`memories_read` / `memories_list` / `memories_search` 的路径归一化只保证"起点在 root 内"，
但 `resolve` 在顺着组件下降时，一旦某个组件不存在就**把剩余组件用 `filepath.Join` 一把拼上
并提前返回**。`filepath.Join` 会归一化 `..`，于是 `..` 检查被整体绕过：

```
memories_read {"path": "zzz/../../../projects/-Other-project/MEMORY.md"}
→ "OTHER PROJECT SECRET\n"      ← 别的项目的 MEMORY.md
```

命中条件只是"首个组件不存在 + 足够多 `..`"，模型无需知道任何密钥。这直接推翻
`pkg/memory/scope.go:26-32` 声称的"跨项目泄漏在结构上不可能，而不是提示纪律问题"。
既有守卫 `TestMemoryToolsCannotReachAnotherProjectsMemory`（`pkg/tool/request_permissions_test.go`）
的四条敌意路径都以 `..` 开头或为绝对路径，**恰好绕开了这个形态**，所以一直绿。

## Current state（advisor 亲读 + 实测，基线 `80bb73b4`）

`pkg/memory/backend.go:218-273`：

```go
func (b Backend) resolve(path *string) (string, error) {
	if strings.TrimSpace(b.Root) == "" || b.Root == "." {
		return "", fmt.Errorf("memory root is empty")
	}
	if path == nil || strings.TrimSpace(*path) == "" {
		return b.Root, nil
	}
	raw := *path
	rel := filepath.FromSlash(raw)
	if filepath.IsAbs(rel) {
		within, ok := rootRelative(b.Root, rel)     // :231-241 绝对路径越界在此拒绝
		if !ok {
			return "", fmt.Errorf("path '%s' must stay within the memories root", raw)
		}
		rel = within
	}
	components := strings.FieldsFunc(filepath.ToSlash(rel), func(r rune) bool { return r == '/' })
	current := b.Root
	for index, component := range components {
		if component == "." {
			continue
		}
		if component == ".." {                       // :237-239 只在"走到了"该组件时生效
			return "", fmt.Errorf("path '%s' must stay within the memories root", raw)
		}
		if strings.HasPrefix(component, ".") {
			return "", notFoundError(raw)
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			for _, remaining := range components[index+1:] {   // :243-250 缺陷点
				current = filepath.Join(current, remaining)     // Join 归一化 ".."，一路拼出去
			}
			return current, nil
		}
		if err != nil {
			return "", fmt.Errorf("I/O error while reading memories: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path '%s' must not be a symlink", displayPath(b.Root, current))
		}
		if index+1 < len(components) && !info.IsDir() {
			return "", fmt.Errorf("path '%s' traverses through a non-directory path component", raw)
		}
	}
	return current, nil
}
```

调用方：`list`（:317）、`read`（:394）、`search`（:478），全部先 `resolve` 再 `os.Lstat`；
提前返回的"不存在路径"随后由调用方转成 `notFoundError(request.Path)`。`route`（:81-98）先把
`global/` 前缀剥给 mount backend，再各自 `resolve` —— 同一缺陷在 global scope 上同样存在。

**实测证据（advisor 亲跑，`go test -overlay` 把探针注入 `pkg/tool`，仓库树零改动）**：

```
# 基线 session_tools.go（完全不含 367 的新兜底）+ 基线 backend.go
read "zzz/../../../projects/-Other-project/MEMORY.md"        err=<nil> content="OTHER PROJECT SECRET\n"
read "projects/-Test-project/../../../-Other-project/MEMORY.md" err=<nil> content="OTHER PROJECT SECRET\n"
```

⇒ 与 367 无关（367 也没扩大它：剥前缀后的候选以 `..` 开头，仍被 :237-239 拒掉）。

**已排除的竞争解释**：① 绝对路径不是入口——`rootRelative`（:186-215）用 `filepath.Rel` +
`..` 前缀检查拒绝越界绝对路径，既有守卫测试 `otherAbsolute` 一直绿；② `..` 作首组件不是入口
——:237-239 拒；③ 写入面没有同类问题——`addAdHocNote` 先过 `validateNoteFilename`（:717+，
`backend_test.go:137` 钉住 `../` 被拒）；④ `control.go` 的清理路径另有 `validateClearRoot`。

**同一缺陷在本仓的其他落点**：`grep -n "filepath.Join(current" pkg/memory/*.go` 只有
`resolve` 这一处；`rootRelative`、`addAdHocNote`、`ensureDirectory` 均无此形态。故修复点是唯一一处。

## 修复设计（一处判定，删掉可绕过的分支）

在 `resolve` 里**把 `..` 判定提到下降之前**，对全部组件一次性判定；随后删除循环内那道
（届时不可达的）`..` 分支，保证"一条规则一处实现"：

```go
	components := strings.FieldsFunc(filepath.ToSlash(rel), func(r rune) bool { return r == '/' })
	// Every ".." is refused before the walk starts. past a missing component
	// the walk stops and joins the rest as-is, and filepath.Join collapses
	// ".." — so a check that only runs on components the walk reaches lets a
	// tail like "zzz/../../../projects/other/MEMORY.md" name another scope's
	// memory.  ← 注释按仓库风格写 WHY，中文上句只是解释，落盘英文即可
	for _, component := range components {
		if component == ".." {
			return "", fmt.Errorf("path '%s' must stay within the memories root", raw)
		}
	}
	current := b.Root
	for index, component := range components {
		if component == "." {
			continue
		}
		if strings.HasPrefix(component, ".") {
			return "", notFoundError(raw)
		}
		...（其余不动）
```

### 行为变化矩阵（**必须与实现逐条吻合**）

| 场景 | 旧行为 | 新行为 | 性质 |
|---|---|---|---|
| `..` 出现在走得到的组件上 | 拒绝（`must stay within the memories root`） | 拒绝，同文案 | 不变 |
| `..` 出现在"首个缺失组件"之后（本缺陷） | `filepath.Join` 归一化 → **越出 root 读到别的 scope** | 拒绝，同文案 | **修复** |
| `missing/../MEMORY.md`（缺失组件 + `..` 又回到 root 内） | 静默归一化到 `MEMORY.md`（意外成功） | 拒绝 | **刻意修订**：归一化等于猜，模型会以为自己读的是 `missing/` 下的文件 |
| 绝对路径落在 root 内 | 允许 | 允许 | 不变 |
| 绝对路径越出 root | 拒绝 | 拒绝 | 不变 |
| 空／blank path | 根 | 根 | 不变 |
| 缺失但不含 `..` 的路径（如 `no/such-file.md`） | 交给调用方报 not found | 同 | 不变（367 的 anchor hint 仍生效） |
| dotfile（`.git/config`） | `notFoundError` | 同（`..` 已先被拒，不影响 dotfile） | 不变 |

## Scope

**In scope**
- `pkg/memory/backend.go`（只改 `resolve`：新增前置 `..` 判定、删除循环内不可达分支）
- `pkg/memory/backend_test.go`（**追加**一个测试；`TestHiddenAndEscapingPathsAreRejected` 等既有测试一律不改）
- `pkg/tool/request_permissions_test.go`（**追加**一个测试，接在 367 的测试之后；既有测试一律不改）

**Out of scope**
- `pkg/memory/scope.go` —— 布局与隔离文档不动（其 :26-32 的表述在本修复后才真正成立，不改字）
- `pkg/memory/control.go`、`pkg/memory/pipeline.go`、`pkg/memory/workspace.go` —— 无此形态，不碰
- `pkg/tool/session_tools.go` —— 367 的改动已完成并通过评审，本计划不再动
- `pkg/safety/**` —— 按工具名免批与路径无关，不因本修复变化
- `pkg/migrate/**` —— 也用 `NewScoped`，但只跑代码改动后的回归，不改其源码

## Steps

### Step 1: RED（先证明新测试能抓住缺陷）

1. 在 `pkg/memory/backend_test.go` 追加（放在 `TestHiddenAndEscapingPathsAreRejected` 之后）：

```go
// A ".." past a component that does not exist used to be collapsed by
// filepath.Join instead of refused, so `zzz/../../../<other scope>/MEMORY.md`
// named another project's memory. The root below is shaped like a real
// per-project scope so the traversal lands where it would in production.
func TestTraversalBelowAMissingComponentStaysInsideTheRoot(t *testing.T) {
	base := t.TempDir()
	mine := filepath.Join(base, "memories", "projects", "-Scope-A")
	theirs := filepath.Join(base, "memories", "projects", "-Scope-B")
	for dir, body := range map[string]string{mine: "my memory\n", theirs: "OTHER SCOPE SECRET\n"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	backend := New(mine).WithSearchIndex(testSearchIndexDB(t))
	for _, path := range []string{
		"zzz/../../../projects/-Scope-B/MEMORY.md",
		"missing/../MEMORY.md",
		"a/b/c/../../../../projects/-Scope-B/MEMORY.md",
	} {
		read, err := backend.Read(ReadRequest{Path: path, LineOffset: 1})
		if err == nil {
			t.Fatalf("read %q accepted, content = %q", path, read.Content)
		}
		if strings.Contains(read.Content, "OTHER SCOPE SECRET") {
			t.Fatalf("read %q returned another scope's memory: %q", path, read.Content)
		}
		for _, call := range []struct {
			name string
			run  func() (string, error)
		}{
			{"list", func() (string, error) {
				response, err := backend.List(ListRequest{Path: &path, MaxResults: 10})
				return fmt.Sprint(response.Entries), err
			}},
			{"search", func() (string, error) {
				response, err := backend.Search(SearchRequest{Queries: []string{"SECRET"}, Path: &path, TopK: 5})
				return fmt.Sprint(response.Matches), err
			}},
		} {
			got, err := call.run()
			if err == nil {
				t.Fatalf("%s %q accepted: %s", call.name, path, got)
			}
			if strings.Contains(got, "OTHER SCOPE SECRET") {
				t.Fatalf("%s %q leaked: %s", call.name, path, got)
			}
		}
	}
}
```

2. 在 `pkg/tool/request_permissions_test.go` 追加（接 367 的测试之后）：

```go
// The tool layer's own guard for the same shape: a path whose first component
// does not exist and whose tail walks back out with ".." must not reach another
// project's memory, whichever memory tool asks.
func TestMemoryToolsMissingComponentTraversalCannotReachAnotherProject(t *testing.T) {
	home := t.TempDir()
	roots, err := memory.ResolveRootsForAgent(home)
	if err != nil {
		t.Fatal(err)
	}
	const otherKey = "-Other-project"
	mine := roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: testMemoryProjectKey})
	theirs := roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: otherKey})
	for root, body := range map[string]string{mine.MemoryRoot: "my memory\n", theirs.MemoryRoot: "OTHER PROJECT SECRET\n"} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handlers := memoryToolHandlers(t, home)
	for _, path := range []string{
		"zzz/../../../projects/" + otherKey + "/MEMORY.md",
		"zzz/../../../../projects/" + otherKey + "/MEMORY.md",
	} {
		for _, call := range []struct {
			name string
			args string
		}{
			{"memories_read", `{"path":"` + path + `"}`},
			{"memories_list", `{"path":"` + path + `"}`},
			{"memories_search", `{"queries":["SECRET"],"path":"` + path + `"}`},
		} {
			result, err := handlers[call.name](context.Background(), call.args)
			if err == nil {
				t.Fatalf("%s %q reached another project: %#v", call.name, path, result)
			}
			if strings.Contains(fmt.Sprintf("%#v", result), "OTHER PROJECT SECRET") {
				t.Fatalf("%s %q leaked in the response: %#v", call.name, path, result)
			}
		}
	}
}
```

3. 跑一遍，**必须先红**（生产代码未改）：
   `CGO_ENABLED=1 go test -tags fts5 ./pkg/memory/ ./pkg/tool/ -run 'TestTraversalBelowAMissingComponentStaysInsideTheRoot|TestMemoryToolsMissingComponentTraversalCannotReachAnotherProject' -count=1 -v`
   预期：两条都 FAIL，报 `accepted`／`reached another project` 且内容含 `OTHER ... SECRET`。
   **不红就 STOP**（说明缺陷形态与本计划不符）。把这段红输出记进报告。

### Step 2: 实施修复

按 §修复设计 改 `pkg/memory/backend.go` 的 `resolve`：新增前置 `..` 判定循环，删除原循环内的
`if component == ".."` 分支（不可达 ⇒ 死代码，必须删，不得留下）。注释解释 WHY（提前返回 +
`filepath.Join` 归一化），与仓库既有注释风格一致。

**Verify**：
- `CGO_ENABLED=1 go build -tags fts5 ./pkg/memory/ ./pkg/tool/` → exit 0
- Step 1 的两条测试 → 全 PASS
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/memory/ -count=1` → `ok`（`TestHiddenAndEscapingPathsAreRejected`、`TestScopedBackendReadsListsAndSearchesTheGlobalMount` 等既有测试原样绿）

### Step 3: 回归与闸门

**Verify**：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/memory/ ./pkg/tool/ ./pkg/migrate/ -count=1` → 三包 `ok`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ ./pkg/turn/ -count=1` → 两包 `ok`（`resolve` 的间接消费者）
- `CGO_ENABLED=1 go vet -tags fts5 ./pkg/memory/ ./pkg/tool/` → exit 0
- `gofmt -l pkg/memory pkg/tool` → 空
- `grep -n "component == \"..\"" pkg/memory/backend.go` → **恰好 1 处**（前置判定），循环内已删
- `git status --porcelain -- pkg/memory pkg/tool` → 只有 `pkg/memory/backend.go`、`pkg/memory/backend_test.go`、`pkg/tool/request_permissions_test.go` 三个 in-scope 文件

## Done criteria

- [ ] Step 1 两条新测试存在，且在 Step 2 之前红、之后绿
- [ ] `pkg/memory`／`pkg/tool`／`pkg/migrate`／`pkg/run`／`pkg/turn` 全 `ok`；`vet` 0；`gofmt` 净
- [ ] `grep -n 'component == ".."' pkg/memory/backend.go` 恰好 1 处
- [ ] 既有守卫原样绿且未被修改：`TestMemoryToolsCannotReachAnotherProjectsMemory`、`TestHiddenAndEscapingPathsAreRejected`、`TestScopedBackendReadsListsAndSearchesTheGlobalMount`、367 的 6 个新测试
- [ ] `git status` 只含三个 in-scope 文件（+ 367 已改的三个）

## STOP conditions

- `pkg/memory/backend.go` 的 `resolve` 与本计划摘录不符（drift）→ 停，报告差异。
- Step 1 新测试**未变红** → 停，报告实际行为（说明形态判断错了，不要改测试去迁就实现）。
- 修复后 `pkg/migrate`／`pkg/run`／`pkg/turn` 出现失败 → 停，报告是哪个用例、什么路径；不要为使测试变绿而放宽 `..` 判定。
- 发现还有第二条泄漏路径（除 `resolve` 之外）→ 停，报告，不要顺手扩大改动。
- 需要改 out-of-scope 文件才能完成 → 停，报告。

## Maintenance notes

- 本修复把"memory 路径永不包含 `..`"变成**前置不变式**：以后给 `resolve` 加任何提前返回，
  都不必再各自防 `..`；反过来，若有人把前置判定改回"按组件边走边判"，缺陷会原样回来。
- `filepath.Join` 在 `resolve` 里仍用于逐段拼接（安全：此时已无 `..`）；不要在提前返回分支
  里改用"不归一化的字符串拼接"来替代判定——那只是把泄漏换成路径不干净。
- Windows：`filepath.ToSlash` 已把 `\` 规整为 `/`，故 `projects\<key>\..\..` 同样在组件列表里
  命中 `..`；不需要额外的反斜杠处理。
- 遥测/日志：本修复不新增事件。若要观测，看 memory 工具失败里出现
  `must stay within the memories root` 的比例——这是被拒绝的越权尝试，不是正常用户路径。
- 367 与 369 的关系：367 的 scope 前缀兜底只剥**本会话自己的 key**，剥出的候选若含 `..`
  由本修复拒绝；两者叠加后 `projects/<own-key>/../../<other-key>/…` 全链路拒绝。