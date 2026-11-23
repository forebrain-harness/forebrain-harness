# Plan 170：三处「`..` 前缀」判定把小写点文件误判为目录之外

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**（四处都得对得上）：
> `grep -rc 'HasPrefix(rel, "..")' pkg/tool/diff_blocks.go pkg/assembly/rules_hook.go pkg/tool/state.go`
> → 分别 `2` / `1` / `1`；
> `grep -c 'HasPrefix(rel, "..")\|HasPrefix(rel, ".."+string(filepath.Separator))' pkg/tool/shell_tool.go` 为 `1`（**正确写法**的样板）。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/tool/diff_blocks.go pkg/assembly/rules_hook.go pkg/tool/state.go` 必须**无输出**。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/assembly -count=1`（超时给足 600 s）。

## Status

- **Priority**: P3（false rejection / 卡片上露出绝对路径；**没有**越权方向的风险）
- **Effort**: S（三处各一行 + 三个测试）
- **Risk**: LOW（改动只会让原本被拒的合法文件通过、让本该相对的路径变相对）
- **Depends on**: 无
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: bug（路径判定）

## Why this matters

判断"这个路径在目录里吗"时，本仓有三处写成 `!strings.HasPrefix(rel, "..")`。
`filepath.Rel` 对**同目录下名字以 `..` 开头的文件**（`..env`、`..gitignore.ci`、`..idea.md`）
返回的就是它自己（`"..env"`），于是这三处都把它当成"跑到目录外面去了"：

| 位置 | 函数 | 误判后果 |
|---|---|---|
| `pkg/tool/diff_blocks.go:281,289` | `displayPathRelativeTo` | 卡片上显示**绝对路径**而不是 `..env`（本仓的契约是"项目内相对、项目外绝对"） |
| `pkg/assembly/rules_hook.go:226` | `displayRulePath` | 项目规则列表里那个文件显示绝对路径 ⇒ 用户家目录路径被写进注入给模型的上下文 |
| `pkg/tool/state.go:1189` | `isWithinDir`（服务于 `isSanctionedPlanWrite`） | plan 模式下写 `<计划目录>/..idea.md` 被判成"不在计划目录里"：`GuardWrite` 直接回 `plan mode: write blocked (only the plan directory is writable); call exit_plan_mode first`（`pkg/tool/state.go:1048-1052`），`permissions.go:399` 也不再给它免审批 |

方向值得说清：三处**都是"拒/退回绝对路径"**，没有一处是"放行"。
所以这不是安全漏洞，不会让任何越界写入发生；修它是为了让判定正确、让那张卡的路径契约成立、
让 plan 模式不要无缘无故拒绝一个合法文件名。

正确的写法就在同一个包里：`pkg/tool/shell_tool.go:676`（`rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))`），
它把"正好是 `..`"与"以 `../` 开头"分开判，因此 `..env` 通过。

## Current state

```go
// pkg/tool/diff_blocks.go:275-294
// displayPathRelativeTo is how a card names a file: relative to dir when the
// file is inside it, absolute otherwise.
func displayPathRelativeTo(dir, abs string) string {
	if dir == "" {
		return abs
	}
	if rel, err := filepath.Rel(dir, abs); err == nil && !strings.HasPrefix(rel, "..") {   // :281
		return rel
	}
	// The same directory can be spelled two ways when it sits behind a
	// symlink (/tmp vs /private/tmp on macOS, …). Resolve and retry once …
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
		if rel, err := filepath.Rel(resolved, abs); err == nil && !strings.HasPrefix(rel, "..") {   // :289
			return rel
```

```go
// pkg/tool/state.go:1178-1192
// isWithinDir reports whether absPath is a regular file located directly
// inside dir (one level deep, no subdirectories, no ".." traversal). …
func isWithinDir(absPath, dir string) bool {
	…
	if strings.HasPrefix(rel, "..") || strings.ContainsRune(rel, os.PathSeparator) {   // :1189
		return false // escapes the dir or reaches into a subdirectory
	}
	return true
}
```

```go
// pkg/assembly/rules_hook.go:220-228
// displayRulePath shortens a project rule path to be relative to the working
// directory when it lives underneath it ("docs/FOREBRAIN.md"), else keeps the
// absolute path.
func displayRulePath(path, wd string) string {
	if rel, err := filepath.Rel(resolvePath(wd), resolvePath(path)); err == nil && !strings.HasPrefix(rel, "..") {   // :226
		return filepath.ToSlash(rel)
	}
	return path
}
```

正确样板（`pkg/tool/shell_tool.go:670-678`）：

```go
		rel, relErr := filepath.Rel(root, target)
		if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
```

## Scope

**In scope（只改这些）**：
- `pkg/tool/diff_blocks.go`（两处判定）
- `pkg/tool/state.go`（`isWithinDir` 一处判定）
- `pkg/assembly/rules_hook.go`（一处判定）
- `pkg/tool/diff_blocks_test.go`、`pkg/tool/state_test.go`、`pkg/assembly/rules_hook_test.go`（各加 1 个测试）

**Out of scope（看着相关也不要动）**：
- **不要**新建一个跨包的共享 helper：三个包各一行，为一个单行判定建立 `pkg/paths` 之类的公共包，
  换来的是一条新的依赖边（`pkg/assembly` → `pkg/tool` 已经在别处存在，但这次没必要动它）。同理见
  `pkg/tool/shell_tool.go:676`：它保持自己那行。
- `filepath.EvalSymlinks` 的重试逻辑、`resolvePath`
- `IsProtectedMetadataPath` / `PathInsidePrimaryHome` / `IsGitExecutionSurfacePath`
- plan 模式的整体判定（`isSanctionedPlanWrite` 的语义、`AllowedPlanPathFromContext`）
- macOS `/var` → `/private/var` 的处理（本计划只改 `..` 的判定）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run '^(TestDisplayPathRelativeToKeepsDotDotNamesRelative\|TestIsWithinDirAcceptsDotDotNames)$' -count=1 -v` | 两个 PASS |
| assembly 定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/assembly -run '^TestDisplayRulePathKeepsDotDotNamesRelative$' -count=1 -v` | PASS |
| 两包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/assembly -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool ./pkg/assembly` | exit 0 |
| gofmt | `gofmt -l pkg/tool pkg/assembly` | 无输出 |

## Steps

### Step 1：先写三个会红的测试

**1a. `pkg/tool/diff_blocks_test.go`**

```go
// A sibling whose name starts with ".." is still inside the directory: the
// card names it relative to the project, like any other file there.
func TestDisplayPathRelativeToKeepsDotDotNamesRelative(t *testing.T) {
	dir := t.TempDir()
	if got := displayPathRelativeTo(dir, filepath.Join(dir, "..env")); got != "..env" {
		t.Fatalf("displayPathRelativeTo(..env) = %q, want %q", got, "..env")
	}
	// 真正的目录外仍然给绝对路径。
	outside := filepath.Join(t.TempDir(), "other.txt")
	if got := displayPathRelativeTo(dir, outside); got != outside {
		t.Fatalf("displayPathRelativeTo(outside) = %q, want the absolute path %q", got, outside)
	}
}
```

**1b. `pkg/tool/state_test.go`**

```go
func TestIsWithinDirAcceptsDotDotNames(t *testing.T) {
	dir := t.TempDir()
	if !isWithinDir(filepath.Join(dir, "..idea.md"), dir) {
		t.Fatal("a file named ..idea.md directly inside the plan directory is a sanctioned plan write")
	}
	if isWithinDir(filepath.Join(dir, "sub", "..idea.md"), dir) {
		t.Fatal("a subdirectory must stay out")
	}
	if isWithinDir(dir, dir) {
		t.Fatal("the directory itself is not a file inside it")
	}
}
```

**1c. `pkg/assembly/rules_hook_test.go`**

```go
// A rule file whose name starts with ".." is inside the working directory, so
// the rules list names it relatively — the absolute path never reaches the
// prompt for a file that lives in the project.
func TestDisplayRulePathKeepsDotDotNamesRelative(t *testing.T) {
	wd := t.TempDir()
	path := filepath.Join(wd, "..env")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := displayRulePath(path, wd); got != "..env" {
		t.Fatalf("displayRulePath(..env) = %q, want %q", got, "..env")
	}
}
```

（`displayRulePath` 内部会 `EvalSymlinks`，所以文件必须**真的存在**——上面已写入。
`t.TempDir()` 在 macOS 上位于 `/var/folders/...`，正是这条路径需要符号链接解析的原因。）

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run '^(TestDisplayPathRelativeToKeepsDotDotNamesRelative|TestIsWithinDirAcceptsDotDotNames)$' -count=1 -v`
→ 两个 FAIL；
`… -tags fts5 ./pkg/assembly -run '^TestDisplayRulePathKeepsDotDotNamesRelative$' -count=1 -v` → FAIL。
若有一个直接 PASS，STOP 3。

### Step 2：三处判定换成"先判等、再判前缀"

**2a. `pkg/tool/diff_blocks.go`**：两处 `&& !strings.HasPrefix(rel, "..")` 都改成
`&& rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))`；
`filepath` 已经 import（同文件在用 `filepath.Rel`），无需新增 import。

**2b. `pkg/tool/state.go`** 的 `isWithinDir`：

```go
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) ||
		strings.ContainsRune(rel, os.PathSeparator) {
		return false // escapes the dir or reaches into a subdirectory
	}
```

同时把该函数注释里 `no ".." traversal` 补一句准确说明：
"a sibling whose *name* starts with `..` is not traversal"。

**2c. `pkg/assembly/rules_hook.go`** 的 `displayRulePath`：

```go
	if rel, err := filepath.Rel(resolvePath(wd), resolvePath(path)); err == nil &&
		rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
```

**Verify**：Step 1 的三条定向命令全部 PASS。

### Step 3：确认没有漏网的同类

**Verify**：
`grep -rn 'HasPrefix(rel, "..")' --include=*.go pkg/` → **无输出**（本仓只剩 `rel != ".." && HasPrefix(rel, ".."+sep)` 的正确形态；
`grep -rn 'HasPrefix(rel, ".."+string' --include=*.go pkg/` 应能数到 `shell_tool.go` 一处 + 本计划改的三处共 5 行
（`diff_blocks.go` 2 + `state.go` 1 + `rules_hook.go` 1 + `shell_tool.go` 1））。

### Step 4：两包回归

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/assembly -count=1`
→ 与基线相比无新增 FAIL。额外确认 plan 模式相关用例仍绿：
`-run 'Plan'` 在 `./pkg/tool ./pkg/assembly` 上单独跑一次。

## Test plan

- `TestDisplayPathRelativeToKeepsDotDotNamesRelative`（`pkg/tool/diff_blocks_test.go`）：`..env` 相对、
  真外部绝对。
- `TestIsWithinDirAcceptsDotDotNames`（`pkg/tool/state_test.go`）：`..idea.md` 通过、子目录里的同名文件拒绝、
  目录本身拒绝（三条边界一起钉住，防止把"宽松"改成"全放行"）。
- `TestDisplayRulePathKeepsDotDotNamesRelative`（`pkg/assembly/rules_hook_test.go`）：`..env` 显示相对。

## Done criteria

机器可检查，全部满足：

- [ ] 三个新测试 PASS
- [ ] `grep -rn 'HasPrefix(rel, "..")' --include=*.go pkg/` **无输出**
- [ ] `grep -c 'HasPrefix(rel, ".."+string' pkg/tool/diff_blocks.go pkg/tool/state.go pkg/assembly/rules_hook.go pkg/tool/shell_tool.go`
      逐文件为 `2` / `1` / `1` / `1`（`grep -c` 每行打印 `路径:计数`）
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/assembly -count=1` 与基线相比无新增 FAIL
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；`gofmt -l pkg/tool pkg/assembly` 无输出
- [ ] `git status --porcelain` 只显示 Scope 里的六个文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数与标注不符，或 drift check 有输出且"Current state"摘录与现场代码对不上。
2. 基线命令本身跑不起来（编译失败/环境错误），拿不到失败清单。
3. Step 1 的三个测试里有一个**直接 PASS**（基线已变——别处已经修过其中一处）。
4. `grep -rn 'HasPrefix(rel, "..")'` 在**别的包**里还有命中（说明基数与本计划基线不同）：先把命中的文件贴出来，
   就地判断"改还是不改"后再问我——本计划只覆盖上面三处。
5. `TestIsWithinDirAcceptsDotDotNames` 在 Step 2 之后**仍然红**：说明 `isWithinDir` 的另一个条件
   （`strings.ContainsRune(rel, os.PathSeparator)`）也在拦它——那说明 `..idea.md` 这条路径另有决定，
   报告后停手，不要放松那个条件。
6. 你想把这些判定抽成一个公共函数：**不要**（见 Out of scope）。
7. 你发现 `displayRulePath` 之外还有把绝对路径写进模型可见文本的地方（例如别的 prompt 组装点）：
   只记录、报告，不在本计划里改。

## Maintenance notes

- `..` 判定在本仓的正确形态只有一种：`rel != ".." && !strings.HasPrefix(rel, ".."+string(sep))`。
  评审时看到 `HasPrefix(rel, "..")` 就直接打回——它是"看起来对、把 `..env` 判错"的经典写法。
- 三处之所以没有共享 helper：它们是三个包各一行，且 `pkg/assembly` 与 `pkg/tool` 已有既定的依赖方向；
  为一个单行判定新增公共包，换来的依赖边比省下的重复更贵。
- 这三处误判的方向都是"拒/退回绝对路径"，所以历史上没有出过事故，也不需要回补数据。
