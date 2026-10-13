# Plan 010：plan 模式的计划文件写入检查按真实路径判断（同一目录换一种写法不再被拦）；plan 模式提醒说明计划目录由第一次 write_file 创建

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push；改动留在你所在 worktree 的工作区。
>
> **Drift check（先跑）**：
> `git diff --stat 9656051 -- pkg/tool/state.go pkg/tool/permissions.go pkg/tool/request_permissions_test.go pkg/run/config.go pkg/run/config_test.go`
> 预期无输出。然后对照下方 "Current state" 摘录逐段核对；不一致即 STOP。

## Status

- **Priority**: P2（真机复现：模型用同一目录的另一种写法写计划文件被拦，绕路去 mkdir，触发沙箱越界审批）
- **Effort**: S
- **Risk**: LOW（只放宽"同一个真实目录"的判断，越界判断不变；提醒只加一句）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `9656051`，2026-10-10

## Why this matters

2026-10-10 用 glm-5.3 真机验收（FOREBRAIN_HOME 在 `$TMPDIR` 下，macOS 上 `/var` 是 `/private/var` 的符号链接）时：
plan 模式提醒给出的计划目录是 `/var/folders/…/home/workspace/plans/<project>/<session>`。模型把它写成
`/private/var/folders/…/同一目录/x.md` 去 write_file，被拦下，报
`plan mode: write blocked (only the plan directory is writable); call exit_plan_mode first`。
两种写法指向同一个目录，但写入检查 `isSanctionedPlanWrite` 只做 `filepath.Clean` 后的**字面**比较。
同一个包里判断"是否在 FOREBRAIN_HOME 内"的 `PathInsidePrimaryHome` 早已按真实路径比较（`normalizeRootPath`），只有计划区还是字面比较。

第二个诱因：计划目录在第一次 write_file 之前**并不存在**（write_file 写入时自己 `MkdirAll`）。模型 `ls` 它得到
"No such file or directory"，误以为这是被拦的原因，转而用 shell `mkdir`，被沙箱挡下并弹出"是否在沙箱外运行"的审批。
plan 模式提醒没有说明目录会由第一次 write_file 创建。owner 裁决（2026-10-10）：在提醒里补一句说明，不在进入 plan 模式时预建目录。

修复后：写入是否落在计划目录内，按"文件实际会被创建在哪个目录"判断；提醒告诉模型目录可能还不存在、由 write_file 创建、不要用 shell 建。

## Current state（摘录，`9656051`）

### 1. 写入检查 `pkg/tool/state.go:910-924`

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

它的三个调用方（都不改）：`GuardWrite`（`state.go:868-878`，plan 模式下非计划区写入直接报上面那句错）、
`ProtectedWriteReason`（`state.go:891`，计划区写入不弹"改 FOREBRAIN_HOME"的审批）、
`planFileWriteTarget`（`pkg/tool/permissions.go:335-354`，write_file / apply_patch 先用它认出计划区写入）。

`isWithinDir`（`state.go:1003-1018`）：`dir` 的**直接子项**才返回 true（目录本身、子目录、`..` 越界都返回 false）。

同文件已有的真实路径工具 `normalizeRootPath`（`state.go:2097-2111`）：

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

（路径不存在时 `normalizeViaExistingParent` 解析最近的已存在祖先，再拼回剩余部分——计划目录尚未创建时两种写法也会得到同一个结果。）
`PathInsidePrimaryHome` → `pathWithinRoot`（`state.go:2057`、`:2136`）就是用它比较的，作为风格样板。

### 2. 实际写入方式（**不改**，决定了修法的边界）

`atomicWrite`（`pkg/tool/file_tools.go:542-570`）：在目标文件**所在目录**里写临时文件再 `os.Rename` 到目标名。
所以文件最终落在"目标路径的父目录"里；目标名本身若是符号链接，会被 rename **替换**而不是被跟随。
因此修法只需解析**目标的父目录**和**计划目录**的真实路径，**不要**解析目标路径的最后一段（否则会改变对"计划目录里有个符号链接文件"的既有处理）。

### 3. plan 模式提醒 `pkg/run/config.go:860-940`（`buildPlanModeReminder`）

四处与"还没有计划文件 / 目录可能不存在"相关的文字：

- `:868`（subagent 精简版）：
  `"Plan mode active (subagent). Write/edit plan files only inside %s - all other writes blocked. Use AskUserQuestion to clarify the approach."`
- `:878`（subagent 完整版，原始字符串内的一行）：
  `Files directly in this directory are the ONLY files you are allowed to edit. All write_file/edit_file calls to paths outside this directory are blocked.`
- `:906`（主 agent 精简版、尚无计划文件）：
  `"Plan mode active. Write/edit plan files only inside %s - all other writes blocked. No plan file yet: create a NEW <descriptive-name>.md file (do NOT use plan.md). Call exit_plan_mode when plan ready."`
- `:910`（主 agent 完整版、尚无计划文件时插入的 `exists` 文字）：
  `exists := "(no plan file exists yet - create a NEW <descriptive-name>.md file, e.g. 'add-auth-validation.md'. Do NOT name it plan.md)"`

（`planExists == true` 的分支不改：已有计划文件说明目录已存在。）
现有测试只按 `"PLAN MODE ACTIVE"` 等子串断言提醒（`pkg/run/orchestration_llm_test.go`），没有断言上述整句。

### 4. 测试样板

- `pkg/tool/request_permissions_test.go:2533` `TestRelocatedForebrainHomeStillAllowsPlanWrites`：`NewState` + `WithAllowedPlanPath` + `GuardWrite` / `ProtectedWriteReason`。
- 同文件 `:2196` `TestPlanModeWriteFileAllowsPlanFileUnderStateRoot`：`WithMode(ctx, "plan")` + `NewFileWriteTool(st, &AgentToolRuntime{Home: stateRoot})` + `tool.Handle(ctx, json)` 真写一次。
- `pkg/run/config_test.go`：package `run`，已 import `strings`、`testing`。

### 仓库约定

- 注释英文、完整句子、写"为什么"；不写"修复了 bug X"式历史叙述。
- `pkg/architecture` 守护 import 图与每包生产文件数（≤20）：本计划不新增生产文件、不增删 import。

## Commands you will need（worktree 根目录）

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| tool 单测过滤 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run '<Name>' -count=1` | ok |
| run 单测过滤 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run '<Name>' -count=1` | ok |
| 回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/architecture -count=1` | ok |
| vet | `go vet ./pkg/tool ./pkg/run` | 无输出 |
| fmt | `gofmt -l pkg cmd` | 无输出 |

## Scope

**In scope**：
- `pkg/tool/state.go` — 只改 `isSanctionedPlanWrite` 及其 doc 注释。
- `pkg/tool/request_permissions_test.go` — 新增 2 个测试。
- `pkg/run/config.go` — 只改 `buildPlanModeReminder` 里上面列出的 4 处文字。
- `pkg/run/config_test.go` — 新增 1 个测试。

**Out of scope**（不要碰）：
- `GuardWrite`、`ProtectedWriteReason`、`planFileWriteTarget`、`atomicWrite`、`normalizeRootPath`、`isWithinDir` 的实现。
- 进入 plan 模式时预建计划目录（owner 已裁决不做）。
- 沙箱策略（shell 不能写 FOREBRAIN_HOME 是既定设计）。
- `planExists == true` 分支与 re-entry 段落的提醒文字。
- 其他任何文件。

## Git workflow

不 commit、不 push，改动留在工作区。

## Steps

### Step 1：先写写入检查的失败测试

在 `pkg/tool/request_permissions_test.go` 的 `TestRelocatedForebrainHomeStillAllowsPlanWrites` 之后新增：

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
```

（`os`、`filepath`、`context` 已在该测试文件 import。）

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestPlanWriteGuardJudgesTheDirectoryAFileLandsIn' -count=1`
→ **FAIL**，信息含 `plan write rejected: plan mode: write blocked`。若通过或被 Skip → STOP 1。

### Step 2：再写一个经 write_file 真写的失败测试

紧接 Step 1 的测试之后新增（结构照 `TestPlanModeWriteFileAllowsPlanFileUnderStateRoot`）：

```go
// The write itself, not just the guard: write_file in plan mode, pointed at the
// plan directory through the other spelling, creates the plan file where the
// directory really is.
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

	st := NewState(t.TempDir())
	writer, err := NewFileWriteTool(st, &AgentToolRuntime{Home: stateRootAlias})
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

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestPlanModeWriteFileReachesThePlanDirectoryThroughAnotherSpelling' -count=1`
→ **FAIL**，信息含 `plan write through the real spelling should succeed`。若通过或被 Skip → STOP 1。
（若失败原因不是 plan 模式拦截，而是别的错误——例如路径不在允许根内——把原文写进报告，STOP 2。）

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
	realAbs := filepath.Join(normalizeRootPath(filepath.Dir(cleanAbs)), filepath.Base(cleanAbs))
	realAllowedDir := normalizeRootPath(cleanAllowed)
	realAllowedFile := filepath.Join(normalizeRootPath(filepath.Dir(cleanAllowed)), filepath.Base(cleanAllowed))
	return isWithinDir(realAbs, realAllowedDir) || realAbs == realAllowedFile
}
```

（先保留原来的字面判断作为快速路径——字面上就在计划目录里的写入不做任何文件系统调用，行为与今天完全相同；
只有字面不匹配时才按真实路径再判一次。）

**Verify**：
- `CGO_ENABLED=1 go build ./...` → exit 0
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestPlanWriteGuardJudgesTheDirectoryAFileLandsIn|TestPlanModeWriteFileReachesThePlanDirectoryThroughAnotherSpelling|TestRelocatedForebrainHomeStillAllowsPlanWrites|TestPlanModeWriteFile' -count=1 -v` → 全部 `--- PASS`（不得有 SKIP），ok。

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
   → **FAIL**（前 4 个子测试失败）。若全部通过 → STOP 1。

2. 改 `pkg/run/config.go` 的 4 处文字（只改这 4 处，标点与其余文字原样保留）：

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

### Step 5：回归

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/architecture -count=1
go vet ./pkg/tool ./pkg/run
gofmt -l pkg cmd
git status --short
```

**Verify**：三个包 ok；vet、gofmt 无输出；`git status --short` 恰好 4 行：
` M pkg/run/config.go`、` M pkg/run/config_test.go`、` M pkg/tool/request_permissions_test.go`、` M pkg/tool/state.go`。
（`./pkg/run` 若出现与本改动无关的偶发失败，单独 `-run <Name> -count=3` 复跑并在报告里写明。）

## Test plan

- 新增 `TestPlanWriteGuardJudgesTheDirectoryAFileLandsIn`（pkg/tool）：两种写法互换、目录存在与不存在两种状态都放行；子目录、别的会话目录、目录本身、FOREBRAIN_HOME 内其他状态文件仍被拦。
- 新增 `TestPlanModeWriteFileReachesThePlanDirectoryThroughAnotherSpelling`（pkg/tool）：经 write_file 真写，文件出现在计划目录。
- 新增 `TestPlanModeReminderSaysWriteFileCreatesThePlanDirectory`（pkg/run）：尚无计划文件的 4 种提醒都含说明，已有计划文件的 2 种不含。
- 三个测试在改代码前都必须失败（报告附失败信息）。既有 `TestRelocatedForebrainHomeStillAllowsPlanWrites`、`TestPlanModeWriteFile*` 不改、仍通过。

## Done criteria

- [ ] Step 1、2、4.1 的测试在对应改动前 FAIL（报告附失败信息），改动后 PASS 且无 SKIP
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/architecture -count=1` ok
- [ ] `go vet ./pkg/tool ./pkg/run`、`gofmt -l pkg cmd` 无输出
- [ ] `grep -c 'write_file creates the directory' pkg/run/config.go` = 4
- [ ] `git status --short` 恰好是 Step 5 列出的 4 个文件

## STOP conditions

1. 任一"应当失败"的测试在改代码前就通过，或被 Skip（说明环境不支持符号链接或推演有误）——报告原文。
2. Step 2 的失败原因不是 plan 模式拦截（例如写入路径解析、允许根检查报错）——报告原文，不要去改 `resolveWriteFilePath` 或 permissions.go。
3. Step 3 之后既有 `pkg/tool` 测试出现新的失败——报告用例与输出，不要改那些测试。
4. 需要改 Out of scope 中的任何代码，或需要新增生产文件 / 增删 import。
5. Drift check 不为空，或 Current state 摘录对不上。

## Maintenance notes

- 计划区的判定现在是"文件实际落入的目录是不是计划目录"。字面匹配仍是快速路径；只有字面不匹配时才做文件系统解析，所以日常写入没有额外开销。
- 只解析目标的**父目录**、不解析目标本身，是和 `atomicWrite` 的 rename 语义配套的：若将来写文件改成跟随符号链接写入（例如直接 `os.WriteFile`），这里必须改成连目标本身一起解析，否则计划目录里的一个符号链接文件就能把写入带出计划目录。
- 提醒文字被持久化进 transcript（IsMeta 行）并参与提示缓存；本次改动只影响之后新注入的提醒，旧会话里已经写下的提醒不变。
- 评审时重点看：Step 1 测试里"仍被拦"的四条路径在新实现下确实走到了 `return false`；提醒 4 处改动没有误改 `planExists == true` 分支。
