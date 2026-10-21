# Plan 010：plan 模式的计划文件写入按真实路径判断（同一目录换一种写法不再被拦）；plan 模式提醒说明计划目录由第一次 write_file 创建

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push；改动留在你所在 worktree 的工作区。
>
> **Drift check（先跑）**：
> `git diff --stat 7742bde -- pkg/tool/state.go pkg/tool/request_permissions_test.go pkg/run/config.go pkg/run/config_test.go`
> 预期**无输出**。有输出即说明有人动过这四个文件：对照下方 "Current state" 的行号与摘录，不符就 STOP 5。

## Status

- **Priority**: P2（真机复现于 2026-10-10：模型把计划目录换一种写法去 write_file，被 plan 模式拦下，绕路去 shell `mkdir`，触发沙箱越界审批；2026-10-21 在 HEAD 上重新取证，两处危害都仍在，见 "Why this matters"）
- **Effort**: S
- **Risk**: LOW（只放宽"同一个真实目录"的判断，越界判断与"目录本身"的判断都不变；提醒只加一句）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `9656051`，2026-10-10；2026-10-21 在 `7742bde` 上重写核证（行号、四处提醒字面量、四个测试的失败相位全部实测过；`7742bde` 之后的漂移用上面的 drift check 兜住）

## Why this matters

**同一目录的两种写法，被当成两个地方。** macOS 上 `/var` 是 `/private/var` 的符号链接（`/tmp` 是 `/private/tmp`），
所以任何在 `$TMPDIR` / `/tmp` 下、或经符号链接祖先到达的 home，都有两种写法指向同一处。
`buildPlanModeReminder` 把计划目录**原样**印给模型（`pkg/run/config.go:933` 的 `%s`），而写入守卫
`isSanctionedPlanWrite` 只做 `filepath.Clean` 后的**字面**比较（`pkg/tool/state.go:1060-1068`）。
模型只要换用另一种写法（跑过 `pwd -P` / `realpath` 就会拿到），同一件事就有两处真实成本：

1. **规划阶段被拒，然后误诊。** `GuardWrite` 直接报
   `plan mode: write blocked (only the plan directory is writable); call exit_plan_mode first`
   —— 2026-10-10 glm-5.3 真机验收就是这样：模型认为目录不可写，转去 `shell mkdir`，被沙箱挡下并弹出"是否在沙箱外运行"的审批，
   一轮规划被打断。而实际情况是目录**本来就由第一次 write_file 创建**，`write_file` 自己 `MkdirAll`
   （`pkg/tool/file_tools.go:1762`），模型完全没必要建它；提示词里也没有一句说明这一点
   （第二个诱因，owner 2026-10-10 裁决：在提醒里补一句，**不要**改成进入 plan 模式时预建目录）。

2. **批准之后写实施记录，弹出一句吓人的审批。** 这是原计划没写、2026-10-21 新取证的一条：`exit_plan_mode`
   批准后模型要把实施记录追加进**同一个计划文件**（`buildPlanImplementationReminder`，`pkg/run/config.go:751`），
   那时已经不是 plan 模式，`GuardWrite` 放行，改由 `ProtectedWriteReason` 决定要不要问用户。
   字面比较失败 → 走 `PathInsidePrimaryHome` → 弹出
   `Writing …/plans/proj/sid/p.md changes forebrain's own settings, state or skills under FOREBRAIN_HOME.`
   也就是：用户刚批准的计划，写回它自己的记录却被告知"你在改 forebrain 的设置"。两条路径都已在 HEAD 上实测复现
   （见下 "Current state" 末尾的实测记录）。

修完之后：写入是否落在计划目录内，按"**文件实际会被创建在哪个目录**"判断（只解析父目录，绝不解析文件本身，
否则会改变"计划目录里有个符号链接文件"的既有处理）；提醒告诉模型目录可能还不存在、由 write_file 创建、不要用 shell 建。

## Current state（2026-10-21 于 `7742bde` 逐行核过）

### 1. 写入检查 `pkg/tool/state.go:1060-1068`

```go
// isSanctionedPlanWrite reports whether absPath targets the plan region the
// context marks writable. The allowed plan region is a dedicated plan
// directory: permit any regular file directly inside it (the LLM may author new
// plan files with semantic names there) and reject the directory itself,
// sub-directories, and traversal outside it. Keep an exact-match fallback for
// any caller that still pins a single plan file path.
func isSanctionedPlanWrite(ctx context.Context, absPath string) bool {
	allowed := strings.TrimSpace(AllowedPlanPathFromContext(ctx))
	if allowed == "" || strings.TrimSpace(absPath) == "" {
		return false
	}
	cleanAbs := filepath.Clean(absPath)
	cleanAllowed := filepath.Clean(allowed)
	return isWithinDir(cleanAbs, cleanAllowed) || cleanAbs == cleanAllowed
}
```

它的三个调用方（**都不改**）：

- `GuardWrite`（`state.go:1012`，plan 模式下非计划区写入在此报上面那句错；`:1019-1021`）。
- `ProtectedWriteReason`（`state.go:1034`）：计划区写入返回 `""`（不弹审批），否则落到
  `PathInsidePrimaryHome`（`:2218`）等三个分支。
- `planFileWriteTarget`（`pkg/tool/permissions.go:383`）：write_file / edit_file / apply_patch 先用它认出计划区写入。

`isWithinDir`（`state.go:1147`）：`dir` 的**直接子项**才返回 true（目录本身、子目录、`..` 越界都返回 false）。

同文件已有的真实路径工具 `normalizeRootPath`（`state.go:2272`）：

```go
func normalizeRootPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if abs, err := filepath.Abs(raw); err == nil && abs != "" {
		raw = abs
	}
	raw = filepath.Clean(raw)
	if resolved, err := filepath.EvalSymlinks(raw); err == nil && strings.TrimSpace(resolved) != "" {
		raw = filepath.Clean(resolved)
	} else if resolved, ok := normalizeViaExistingParent(raw); ok {
		raw = resolved
	}
	return raw
}
```

（路径不存在时 `normalizeViaExistingParent`（`state.go:2289`）解析最近的已存在祖先，再拼回剩余部分——计划目录尚未创建时两种写法也会得到同一个结果。）
`PathInsidePrimaryHome` → `pathWithinRoot`（`state.go:2218`、`:2311`）就是用它比较的，作为风格样板。

### 2. 实际写入方式（**不改**，决定了修法的边界）

`atomicWrite`（`pkg/tool/file_tools.go:964`）：在目标文件**所在目录**里写临时文件再 `os.Rename` 到目标名
（调用点 `:1762` 先 `os.MkdirAll(filepath.Dir(writePath))`，所以第一次写计划文件时目录由它创建）。
文件最终落在"目标路径的父目录"里；目标名本身若是符号链接，会被 rename **替换**而不是被跟随。
因此修法只需解析**目标的父目录**和**计划目录**的真实路径，**不要**解析目标路径的最后一段。

### 3. plan 模式提醒 `pkg/run/config.go:860-976`（`buildPlanModeReminder`）

四处与"还没有计划文件 / 目录可能不存在"相关的文字（行号与字面量 2026-10-21 逐个 `grep -F` 核过）：

- `:868`（subagent 精简版）：
  `"Plan mode active (subagent). Write/edit plan files only inside %s - all other writes blocked. Use AskUserQuestion to clarify the approach."`
- `:878`（subagent 完整版，原始字符串内的一行）：
  `Files directly in this directory are the ONLY files you are allowed to edit. All write_file/edit_file calls to paths outside this directory are blocked.`
- `:906`（主 agent 精简版、尚无计划文件）：
  `"Plan mode active. Write/edit plan files only inside %s - all other writes blocked. No plan file yet: create a NEW <descriptive-name>.md file (do NOT use plan.md). Call exit_plan_mode when plan ready."`
- `:910`（主 agent 完整版、尚无计划文件时插入的 `exists` 文字）：
  `exists := "(no plan file exists yet - create a NEW <descriptive-name>.md file, e.g. 'add-auth-validation.md'. Do NOT name it plan.md)"`

（`planExists == true` 的分支（`:901`、`:912`）不改：已有计划文件说明目录已存在。）
该函数里其余几句（`:884`、`:937`、`:964`、`:971`）只讲"只有这个目录可写"，不声称目录已存在，不必改、也不会与新增那句冲突。
现有测试只按 `"PLAN MODE ACTIVE"` 等子串断言提醒（`pkg/run/orchestration_llm_test.go`），没有断言上述整句。

### 4. 测试样板（都已核过，可照抄结构）

- `pkg/tool/request_permissions_test.go:2533` `TestRelocatedForebrainHomeStillAllowsPlanWrites`：`NewState` + `WithAllowedPlanPath` + `GuardWrite` / `ProtectedWriteReason`。
- 同文件 `:2196` `TestPlanModeWriteFileAllowsPlanFileUnderStateRoot`：`WithMode(ctx, "plan")` + `NewFileWriteTool` + `tool.Handle(ctx, json)` 真写一次。
  **注意它为什么今天能过**：那条测试的计划目录与允许路径是**同一个字符串**，`planFileWriteTarget` 字面命中、提前返回，
  从来没走到"允许根"这一步。Step 2 的测试跨了两种写法，会走到，所以必须给 State 生产环境那套允许根（见 Step 2）。
- `pkg/run/config_test.go`：package `run`，已 import `strings`、`testing`。

### 5. 2026-10-21 实测记录（重写本计划时的取证，执行者可据此判断"还是不是同一件事"）

三个新测试用 `go test -overlay` 在 **HEAD `7742bde` 上未改动的源码**里跑过（测试文件不进仓库）：

| 测试 | HEAD（未修） | 用 Step 3 的正确实现 |
|---|---|---|
| Step 1 `TestPlanWriteGuardJudgesTheDirectoryAFileLandsIn` | FAIL：`plan write rejected: plan mode: write blocked (only the plan directory is writable); call exit_plan_mode first` | PASS |
| Step 1 的第二个函数 `TestPlanWriteGuardDoesNotAskAboutTheImplementationRecord` | FAIL：`… changes forebrain's own settings, state or skills under FOREBRAIN_HOME.` | PASS |
| Step 2 `TestPlanModeWriteFileReachesThePlanDirectoryThroughAnotherSpelling` | FAIL：同一句 `plan mode: write blocked`（**前提是** State 的允许根取生产那套，见 Step 2） | PASS |
| Step 4 `TestPlanModeReminderSaysWriteFileCreatesThePlanDirectory` | FAIL（4/6 子测试） | PASS（6/6） |

修好后 `pkg/tool` 全套 `ok`、`pkg/run` 全套 `ok`（含既有的 `TestPlanModeWriteFile*` 五条与 4 处提醒的既有断言）。

### 仓库约定

- 注释英文、完整句子、写"为什么"；不写"修复了 bug X"式历史叙述。
- `pkg/architecture` 守护 import 图与每包生产文件数（≤20）：本计划不新增生产文件、不增删 import
  （Step 3 只用 `filepath` / `strings`，`state.go` 已 import）。

## Commands you will need（worktree 根目录）

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| tool 单测过滤 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run '<Name>' -count=1` | ok |
| run 单测过滤 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '<Name>' -count=1` | ok |
| 回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/architecture -count=1` | ok |
| vet | `go vet ./pkg/tool ./pkg/run` | 无输出 |
| fmt | `gofmt -l pkg cmd` | 无输出 |

（`pkg/tool` / `pkg/run` 会打开状态库的用例都要给一次性 home；在 agent 会话里跑全量时按仓库规则用
`FOREBRAIN_HOME="$(mktemp -d)"` 前缀。）

## Scope

**In scope**（只改这四个文件）：
- `pkg/tool/state.go` — 只改 `isSanctionedPlanWrite` 及其 doc 注释。
- `pkg/tool/request_permissions_test.go` — 新增 2 个测试。
- `pkg/run/config.go` — 只改 `buildPlanModeReminder` 里第 3 节列出的 4 处文字。
- `pkg/run/config_test.go` — 新增 1 个测试。

**Out of scope**（不要碰，即使看起来相关）：
- `GuardWrite`、`ProtectedWriteReason`、`planFileWriteTarget`（`pkg/tool/permissions.go`）、`atomicWrite`、
  `normalizeRootPath`、`isWithinDir` 的实现。`permissions.go` 只在 drift check 之外**只读**，不修改。
- 进入 plan 模式时预建计划目录（owner 2026-10-10 已裁决不做）。
- 沙箱策略（shell 不能写 FOREBRAIN_HOME 是既定设计）。
- `planExists == true` 分支与 re-entry 段落的提醒文字。
- 其他任何文件（尤其不要顺手改 `pkg/home` 的 `canonicalCandidates` / `ResolveWithinRoots`：它们已经按真实路径比较，Step 2 证明了这一点）。

## Git workflow

不 commit、不 push，改动留在工作区（owner 审阅后自行提交）。

## Steps

### Step 1：先写两个失败的守卫测试

在 `pkg/tool/request_permissions_test.go` 的 `TestRelocatedForebrainHomeStillAllowsPlanWrites` 之后新增这两个函数
（`context`、`os`、`path/filepath`、`testing` 该文件都已 import）：

```go
// The plan directory is a place, not a spelling: a home reached through a
// symlinked ancestor (macOS's /var -> /private/var, a relocated home) names the
// same directory two ways, and a plan write through either one lands in it.
// The region itself does not grow: a subdirectory, another session's
// directory and the directory itself stay outside it whichever way they are
// spelled.
func TestPlanWriteGuardJudgesTheDirectoryAFileLandsIn(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(base, "alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	rel := filepath.Join("workspace", "plans", "proj", "sid")
	planDirReal := filepath.Join(realRoot, rel)
	planDirAlias := filepath.Join(aliasRoot, rel)

	for _, created := range []bool{false, true} {
		if created {
			if err := os.MkdirAll(planDirReal, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct{ name, allowed, target string }{
			{"allowed through the alias, written through the real path", planDirAlias, filepath.Join(planDirReal, "p.md")},
			{"allowed through the real path, written through the alias", planDirReal, filepath.Join(planDirAlias, "p.md")},
		} {
			ctx := WithMode(WithAllowedPlanPath(context.Background(), tc.allowed), "plan")
			st := NewState(t.TempDir())
			if err := st.GuardWrite(ctx, tc.target); err != nil {
				t.Fatalf("created=%v %s: plan write rejected: %v", created, tc.name, err)
			}
			if reason := st.ProtectedWriteReason(ctx, tc.target); reason != "" {
				t.Fatalf("created=%v %s: plan write would ask for approval: %q", created, tc.name, reason)
			}
		}

		ctx := WithMode(WithAllowedPlanPath(context.Background(), planDirAlias), "plan")
		st := NewState(t.TempDir())
		for _, outside := range []string{
			filepath.Join(planDirReal, "nested", "p.md"),
			filepath.Join(realRoot, "workspace", "plans", "proj", "other-sid", "p.md"),
			planDirReal,
			filepath.Join(realRoot, "workspace", "state", "permissions.json"),
		} {
			if err := st.GuardWrite(ctx, outside); err == nil {
				t.Fatalf("created=%v: a path outside the plan directory was writable: %s", created, outside)
			}
		}
	}
}

// The other half of the plan loop: after exit_plan_mode the implementation
// record goes into the plan file in a working mode, so GuardWrite passes and
// ProtectedWriteReason decides whether the user is asked. A write spelled the
// other way must not tell the user their own approved plan is a change to
// forebrain's settings.
func TestPlanWriteGuardDoesNotAskAboutTheImplementationRecord(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(base, "alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	homeAlias := filepath.Join(aliasRoot, "home")
	homeReal := filepath.Join(realRoot, "home")
	workspaceAlias := filepath.Join(homeAlias, "workspace")
	planDirAlias := filepath.Join(workspaceAlias, "plans", "proj", "sid")
	planDirReal := filepath.Join(homeReal, "workspace", "plans", "proj", "sid")
	if err := os.MkdirAll(planDirReal, 0o755); err != nil {
		t.Fatal(err)
	}
	planFileReal := filepath.Join(planDirReal, "p.md")
	if err := os.WriteFile(planFileReal, []byte("# Plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Working mode: no WithMode("plan"), so GuardWrite passes and only the
	// approval question is left.
	ctx := WithAllowedPlanPath(context.Background(), planDirAlias)
	st := NewState(workspaceAlias)
	st.SetPrimaryWorkspaceBoundary(homeAlias, workspaceAlias, nil)
	if reason := st.ProtectedWriteReason(ctx, planFileReal); reason != "" {
		t.Fatalf("implementation record write through the other spelling asks the user: %q", reason)
	}
}
```

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestPlanWriteGuardJudgesTheDirectoryAFileLandsIn|TestPlanWriteGuardDoesNotAskAboutTheImplementationRecord' -count=1`
→ **FAIL**，第一根测试的信息含 `plan write rejected: plan mode: write blocked`，第二根含 `changes forebrain's own settings`。
若通过或被 Skip → STOP 1。

### Step 2：再写一个经 write_file 真写的失败测试

紧接 Step 1 的两个测试之后新增（结构照 `TestPlanModeWriteFileAllowsPlanFileUnderStateRoot`，
但允许根必须取生产环境那套，否则 `ResolveWithinRoots` 会先拒，测试根本到不了 plan 模式守卫）：

```go
// The write itself, not just the guard: write_file in plan mode, pointed at the
// plan directory through the other spelling, creates the plan file where the
// directory really is. The State carries the roots a real session gives it (the
// workspace root), because a write outside them is refused before the plan
// policy is ever consulted.
func TestPlanModeWriteFileReachesThePlanDirectoryThroughAnotherSpelling(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(base, "alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	sid := "sid-plan-spelling"
	stateRootAlias := filepath.Join(aliasRoot, ".forebrain", "workspace")
	planDirAlias := state.PlanDirForSession(stateRootAlias, "proj", sid)
	planDirReal := state.PlanDirForSession(filepath.Join(realRoot, ".forebrain", "workspace"), "proj", sid)

	ctx := llm.WithAgentSessionID(context.Background(), sid)
	ctx = WithMode(ctx, "plan")
	ctx = WithAllowedPlanPath(ctx, planDirAlias)

	st := NewState(DefaultAllowedRootsForWorkspace("", stateRootAlias)...)
	writer, err := NewFileWriteTool(st, &AgentToolRuntime{Home: stateRootAlias, WorkspaceRoot: stateRootAlias})
	if err != nil {
		t.Fatalf("NewFileWriteTool: %v", err)
	}
	args, _ := json.Marshal(FileWriteInput{FilePath: filepath.Join(planDirReal, "spelled-real.md"), Content: "# Plan\n\nstep one\n"})
	if _, err := writer.Handle(ctx, string(args)); err != nil {
		t.Fatalf("plan write through the real spelling should succeed, got: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(planDirAlias, "spelled-real.md"))
	if err != nil {
		t.Fatalf("plan file not in the plan directory: %v", err)
	}
	if !strings.Contains(string(got), "step one") {
		t.Fatalf("plan file content not written, got %q", got)
	}
}
```

（`encoding/json`、`strings`、`llm`、`state` 该文件都已 import——`pkg/tool/request_permissions_test.go` 的 import 块里都有。）

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestPlanModeWriteFileReachesThePlanDirectoryThroughAnotherSpelling' -count=1`
→ **FAIL**，信息含 `plan write through the real spelling should succeed` 且**原因是** `plan mode: write blocked`
（2026-10-21 在 `7742bde` 上实测就是这样）。若通过或被 Skip → STOP 1。
（若失败原因不是 plan 模式拦截，而是 `path not allowed` 之类——说明允许根没按本步写——把原文写进报告，STOP 2。）

### Step 3：写入检查按文件实际落入的目录判断

把 `pkg/tool/state.go` 的 `isSanctionedPlanWrite`（含 doc 注释）整体替换为：

```go
// isSanctionedPlanWrite reports whether absPath targets the plan region the
// context marks writable. The allowed plan region is a dedicated plan
// directory: permit any regular file directly inside it (the LLM may author new
// plan files with semantic names there) and reject the directory itself,
// sub-directories, and traversal outside it. Keep an exact-match fallback for
// any caller that still pins a single plan file path.
//
// The region is a place, not a spelling. A home reached through a symlinked
// ancestor names the plan directory two ways (macOS's /var and /private/var),
// and a model may write through either, so both sides are compared by the
// directory they really are. Only the file's parent is resolved, never the file
// itself: a write lands as an entry of that directory (atomicWrite renames into
// it), so the parent is what decides where the plan file ends up.
func isSanctionedPlanWrite(ctx context.Context, absPath string) bool {
	allowed := strings.TrimSpace(AllowedPlanPathFromContext(ctx))
	if allowed == "" || strings.TrimSpace(absPath) == "" {
		return false
	}
	cleanAbs := filepath.Clean(absPath)
	cleanAllowed := filepath.Clean(allowed)
	if isWithinDir(cleanAbs, cleanAllowed) || cleanAbs == cleanAllowed {
		return true
	}
	realTarget := filepath.Join(normalizeRootPath(filepath.Dir(cleanAbs)), filepath.Base(cleanAbs))
	return isWithinDir(realTarget, normalizeRootPath(cleanAllowed))
}
```

三点必须照做的理由，**不要改写这三处**：

1. **第一步保留原来的字面判断作为快速路径**：字面上就在计划目录里的写入不做任何文件系统调用，行为与今天完全相同；
   只有字面不匹配时才按真实路径再判一次。`ProtectedWriteReason` 在**每次**写入时都会调这个函数，快路径是它保住的部分。
2. **真实路径分支只判"直接子项"（`isWithinDir`），不要再加"等于允许路径"的等值兜底。**
   曾经试过在真实路径分支里保留 `realTarget == filepath.Join(normalizeRootPath(filepath.Dir(cleanAllowed)), filepath.Base(cleanAllowed))`
   这一条，用来兼容"允许路径被钉成单个计划文件"的调用方——实测它会**把计划目录本身放行**：`cleanAllowed` 就是目录时，
   右边的值恰好等于 `normalizeRootPath(cleanAllowed)`，于是"写这个目录"被判成合法，
   Step 1 的第三组断言（目录本身必须被拦）会失败。全仓库现在没有任何调用方把允许路径钉成文件
   （`WithAllowedPlanPath` 的调用点都传目录），所以这条兜底在本分支没有存在价值，只留字面等值那一条。
3. **只解析父目录**：目标名本身若是符号链接，`atomicWrite` 会 rename 替换它而不是跟随它；解析最后一段会改变这条既有语义。

**Verify**：
- `CGO_ENABLED=1 go build ./...` → exit 0
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestPlanWriteGuard|TestPlanModeWriteFile|TestRelocatedForebrainHomeStillAllowsPlanWrites' -count=1 -v`
  → 三个新测试与既有五条（`TestPlanModeWriteFileAllowsPlanFile`、`…UnderStateRoot`、`…StaysInOwnSessionPlanDir`、
  `…BlocksNonPlanPath`、`TestRelocatedForebrainHomeStillAllowsPlanWrites`）全部 `--- PASS`，ok；**不得有 SKIP**
  （SKIP 说明符号链接建不起来，这台机器上无法判断真伪 → STOP 1）。

### Step 4：提醒补一句（先写失败测试）

1. 在 `pkg/run/config_test.go` 末尾新增：

```go
// The plan directory does not exist until the first plan file is written, so a
// model that lists it first finds nothing there. Every reminder given before a
// plan file exists says so, so that absence reads as expected rather than as
// the reason a write failed — and so the model does not reach for a shell
// mkdir, which the sandbox refuses inside FOREBRAIN_HOME.
func TestPlanModeReminderSaysWriteFileCreatesThePlanDirectory(t *testing.T) {
	const note = "write_file creates the directory"
	for _, tc := range []struct {
		name       string
		kind       string
		planExists bool
		subagent   bool
		want       bool
	}{
		{name: "full, no plan yet", kind: "full", want: true},
		{name: "sparse, no plan yet", kind: "sparse", want: true},
		{name: "subagent full", kind: "full", subagent: true, want: true},
		{name: "subagent sparse", kind: "sparse", subagent: true, want: true},
		{name: "full, plan exists", kind: "full", planExists: true, want: false},
		{name: "sparse, plan exists", kind: "sparse", planExists: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := buildPlanModeReminder("/plans/proj/sid", "/plans/proj/sid/p.md", tc.planExists, tc.kind, false, tc.subagent)
			if strings.Contains(got, note) != tc.want {
				t.Fatalf("reminder mentions %q = %v, want %v:\n%s", note, !tc.want, tc.want, got)
			}
		})
	}
}
```

   **Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestPlanModeReminderSaysWriteFileCreatesThePlanDirectory' -count=1`
   → **FAIL**，且失败的是前 4 个子测试（`full,_no_plan_yet` / `sparse,_no_plan_yet` / `subagent_full` / `subagent_sparse`）。
   若全部通过 → STOP 1。

2. 改 `pkg/run/config.go` 的 4 处文字（只改这 4 处，标点与其余文字原样保留；行号见第 3 节）：

   - `:868` 改为：
     `"Plan mode active (subagent). Write/edit plan files only inside %s (write_file creates the directory if it does not exist yet - do not create it with shell) - all other writes blocked. Use AskUserQuestion to clarify the approach.",`
   - `:878` 那一行改为：
     `Files directly in this directory are the ONLY files you are allowed to edit. The directory may not exist yet: write_file creates the directory on the first plan file, so do not create it with shell. All write_file/edit_file calls to paths outside this directory are blocked.`
   - `:906` 改为：
     `"Plan mode active. Write/edit plan files only inside %s - all other writes blocked. No plan file yet: create a NEW <descriptive-name>.md file (do NOT use plan.md; write_file creates the directory if it does not exist yet - do not create it with shell). Call exit_plan_mode when plan ready.",`
   - `:910` 改为：
     `exists := "(no plan file exists yet - create a NEW <descriptive-name>.md file, e.g. 'add-auth-validation.md'. Do NOT name it plan.md. The directory may not exist yet: write_file creates the directory on the first plan file, so do not create it with shell)"`

   **Verify**：
   - `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestPlanModeReminderSaysWriteFileCreatesThePlanDirectory' -count=1 -v` → 6 个子测试全部 PASS。
   - `grep -c 'write_file creates the directory' pkg/run/config.go` → `4`
   - `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → `ok`（既有提醒断言只按子串匹配，2026-10-21 已实测改后全套 `ok`）。

### Step 5：回归

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/architecture -count=1
go vet ./pkg/tool ./pkg/run
gofmt -l pkg cmd
git status --short
```

**Verify**：三个包 `ok`；vet、gofmt 无输出；`git status --short` 恰好 4 行：
` M pkg/run/config.go`、` M pkg/run/config_test.go`、` M pkg/tool/request_permissions_test.go`、` M pkg/tool/state.go`。
（`./pkg/run` 若出现与本改动无关的偶发失败，单独 `-run <Name> -count=3` 复跑并在报告里写明。）

## Test plan

- 新增 `TestPlanWriteGuardJudgesTheDirectoryAFileLandsIn`（pkg/tool）：两种写法互换、目录存在与不存在两种状态都放行；
  子目录、别的会话目录、**计划目录本身**、FOREBRAIN_HOME 内其他状态文件仍被拦。
- 新增 `TestPlanWriteGuardDoesNotAskAboutTheImplementationRecord`（pkg/tool）：非 plan 模式下写计划文件（实施记录），
  另一种写法也不弹"改 forebrain 设置"的审批。
- 新增 `TestPlanModeWriteFileReachesThePlanDirectoryThroughAnotherSpelling`（pkg/tool）：经 write_file 真写，
  文件出现在计划目录（允许根取生产那套，否则测不到守卫）。
- 新增 `TestPlanModeReminderSaysWriteFileCreatesThePlanDirectory`（pkg/run）：尚无计划文件的 4 种提醒都含说明，
  已有计划文件的 2 种不含。
- 四个测试在改代码前都必须失败（报告附失败信息）。既有 `TestRelocatedForebrainHomeStillAllowsPlanWrites`、
  `TestPlanModeWriteFile*` 四条不改、仍通过。
- 变异验证（评审侧会复核，执行者可选做）：把 Step 3 的真实路径分支删掉 → 前三个新测试必须重新变红；
  把 `isWithinDir` 换成"等于或在其下" → "目录本身"那条断言必须变红。

## Done criteria

- [ ] Step 1（2 个）、Step 2（1 个）、Step 4.1（1 个）的测试在对应改动前 FAIL（报告附失败信息），改动后 PASS 且无 SKIP
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/architecture -count=1` ok
- [ ] `go vet ./pkg/tool ./pkg/run`、`gofmt -l pkg cmd` 无输出
- [ ] `grep -c 'write_file creates the directory' pkg/run/config.go` → `4`
- [ ] `git status --short` 恰好 4 行，且只有 Scope 里那四个文件
- [ ] 报告里贴出四个测试**修复前**的失败原文与**修复后**的 PASS 行（评审方会独立复跑）

## STOP conditions

- **STOP 1**：Step 1 / Step 2 / Step 4.1 的某个测试在改代码前就通过，或被 Skip。
- **STOP 2**：Step 2 的失败原因不是 `plan mode: write blocked`，而是 `path not allowed` 等——说明允许根没取生产那套，把原文写进报告，别改生产代码去迁就测试。
- **STOP 3**：Step 3 之后 Step 1 的"目录本身被拦"那条仍然失败——说明有人又加回了等值兜底（见 Step 3 第 2 点）。
- **STOP 4**：实现这个修法需要碰 `pkg/home`、`pkg/tool/permissions.go`，或需要让 `GuardWrite` 去查允许根。
- **STOP 5**：Drift check 有输出，且 "Current state" 的行号/摘录与磁盘对不上。
- **STOP 6**：`pkg/architecture` 报包图或文件数变化。

## Maintenance notes

- `isSanctionedPlanWrite` 在**每一次**写入工具调用上都会跑（经 `ProtectedWriteReason`）。字面快速路径是它的性能边界：
  将来若要再加真实路径判断，先想清楚是否要为此缓存 `normalizeRootPath` 的结果（`EvalSymlinks` 是 syscall）。
- 这个函数现在按"真实目录"认区域，所以**经符号链接写进计划目录是允许的**：`/tmp/x -> 计划目录` 时写 `/tmp/x/p.md`
  会落进计划目录。这是刻意行为（同一个地方的另一种写法），不是漏洞；`atomicWrite` 只 rename，不会跟随目标名上的符号链接。
  若将来有人想收紧，注意别把"同一目录的两种写法"一起禁掉。
- "允许路径被钉成单个计划**文件**"这一支现在只剩字面等值这一条路。若将来真有调用方这么用（今天没有），
  它必须自己保证字面一致，或在 `planFileWriteTarget` 那一层补，而不是把等值兜底加回真实路径分支。
- 审阅 PR 时重点看两处：① 真实路径分支只有 `isWithinDir`，没有等值兜底；② 四处提醒文字改的是 `planExists == false`
  的分支，`planExists == true` 与 re-entry 段落一字未动。
- 与本次相邻但**不在范围内**的两件事（owner 若要另立计划再看）：`pkg/home` 的 `canonicalCandidates` 已经把
  "真实路径 + 最近已存在祖先"这一套做过一遍（`pkg/home/pathguard.go:175`），将来这三处各自的路径规范化可以考虑收口成一处；
  以及 `SetPlanForSession`（`pkg/state/plan.go:217`）目前在仓库里没有生产调用点，若日后启用它会自己建计划目录，
  届时第 4 步那句提醒里"目录可能不存在"需要重新核对是否还成立。
