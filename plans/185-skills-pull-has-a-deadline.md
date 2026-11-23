# Plan 185：面板里的 pull 也必须有上限（同族的第二处内联 git）

> **执行者须知**：本计划是 plan 184 的同族收尾，**必须先完成 184**（`gitOut` 的 `WaitDelay` 上限）。
> 按步骤执行，触发 "STOP conditions" 停下报告。**禁止提交代码**（owner 手动提交）。
> 工作区里与本计划无关的未提交改动不要碰（含 `.forebrain/skills/**` 的既有删除）。
>
> **Drift check（先跑）**：`git diff --stat 31370fb1..HEAD -- pkg/tui/skills_versions.go pkg/skill/service.go`
> 有输出就对照 "Current state" 的摘录；对不上按 STOP 1 处理。

## Status

- **Priority**: P3（不是 owner 报的那次冻结；同一根因族的第二处，修复成本 3 行）
- **Effort**: S
- **Risk**: LOW（只加一个上限；**不改**成异步——异步进度需要模态面板里的进度语义，属独立设计）
- **Depends on**: 184（没有 `WaitDelay`，加了 deadline 也照样不返回）
- **Category**: bug（交互冻结，同族）

## Why this matters

`/skills` → 某个技能 → “Pull the update” 会同步跑 `git clone --depth 1`，而且用的是
`context.Background()`（**没有 deadline**）。这段代码跑在 TUI 的主事件循环 goroutine 上（面板的一层层
`SelectRich` 都是同步的），所以上游慢一点，整机就和 plan 184 描述的完全一样：面板不动、按键无效、
只剩软件光标在闪。plan 184 修的是「面板打开时的探针」，这一处是**同一个 hazard 的另一个入口**，
不修就还能用「点一下 Pull」复现同一种冻结。

本计划只做一件事：给这次 clone 一个上限，让「永远不返回」变成「超时后一条可读的失败」。
真正的「后台跑 + 面板内进度」需要给模态面板加进度语义（照 `/mcp`、`/lsp` 的 `panelSession` 订阅模式），
属独立设计，不在本计划里做。

## Current state

`pkg/tui/skills_versions.go:149-155` —— TUI 侧入口，`context.Background()` 就是没有上限：

```go
// PullSkillUpdate fetches the entry's upstream content into its draft.
func (s *ChatSession) PullSkillUpdate(entry skill.Entry, replaceDraft bool) error {
	if s == nil || strings.TrimSpace(s.home()) == "" {
		return fmt.Errorf("skills: unavailable")
	}
	return s.skillLifecycleService().PullUpdate(context.Background(), strings.TrimSpace(entry.Name), replaceDraft)
}
```

`pkg/tui/skills_versions.go:899-904` —— 调用点（模态面板内，主循环 goroutine）：

```go
	if perr := c.session.PullSkillUpdate(entry, true); perr != nil {
		c.renderer.PrintError(perr)
		return skillsDone(), true
	}
	return skillsOutcome{}, false
```

`pkg/skill/service.go:967-975` —— 真正阻塞的一步（`ctx` 直接传给 `gitOut`）：

```go
	tmpDir, err := os.MkdirTemp("", "forebrain-skill-pull-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	repoRoot := filepath.Join(tmpDir, "repo")
	if _, err := gitOut(ctx, tmpDir, "clone", "--depth", "1", repo, "repo"); err != nil {
		return err
	}
```

本仓约定：超时值用包级变量并注明「只为测试能缩短」（`pkg/lsp/detect.go:22-30`）。

## Scope

**In scope**：

- `pkg/tui/skills_versions.go`
- `pkg/tui/skills_versions_test.go`
- `plans/README.md`（只改本计划的状态行）

**Out of scope**：

- 改成异步 / 加进度卡 / 加 spinner（需要模态面板的进度语义，独立计划）。
- `pkg/skill/service.go` 的 `PullUpdate` 实现与 gateway 的调用（HTTP 侧有请求 ctx，够用）。
- `handleSkills` / `pullUpdateFlow` 的交互结构（确认页、返回列表的路径都保持原样）。

## Steps

### Step 1：加一个包级上限并传进 ctx

`pkg/tui/skills_versions.go` 顶部常量区加：

```go
// The pull's own deadline. It runs on the event-loop goroutine, so without one
// an unreachable source froze the whole terminal for as long as git's own
// retries lasted. A package-level variable only so tests can shorten it.
var skillPullTimeout = 2 * time.Minute
```

`PullSkillUpdate` 改为：

```go
	ctx, cancel := context.WithTimeout(context.Background(), skillPullTimeout)
	defer cancel()
	return s.skillLifecycleService().PullUpdate(ctx, strings.TrimSpace(entry.Name), replaceDraft)
```

**Verify**：`grep -c "skillPullTimeout" pkg/tui/skills_versions.go` 为 `2`（定义 + 使用）。

### Step 2：确认超时是一条可读的失败

超时后 `gitOut` 返回 `git [...] : signal: killed` 一类的错误，`pullUpdateFlow` 已经把它交给
`c.renderer.PrintError`，不需要新文案。**不要**为了好看去改写 git 的错误字符串（`pkg/skill/git_install.go:119-123`
的注释解释了为什么原样透出）。

**Verify**：`grep -n "PrintError(perr)" pkg/tui/skills_versions.go` 仍在（未改交互路径）。

## Test plan

`pkg/tui/skills_versions_test.go` 追加一个测试：

- `TestPullSkillUpdateUsesItsDeadline` —— 把 `skillPullTimeout` 临时设成 `50 * time.Millisecond`
  （`t.Cleanup` 还原），造一个 `ChatSession`（`skillLifecycleService()` 需要非空 `home`：用 `t.TempDir()`
  并 `s.env`/`home` 按该文件现有构造方式填），PATH 里放一个 `sleep 30` 的假 `git`；
  调 `PullSkillUpdate(entry, true)`，断言它在 **2 秒内**返回且返回错误。
  未修时它会挂满 30 秒（假 git）——测试就会挂在 `-timeout` 上，所以断言写「2 秒内返回」。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'PullSkillUpdate' -count=1` 全绿。

## Done criteria

- [ ] `grep -c "skillPullTimeout" pkg/tui/skills_versions.go` 为 `2`
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` exit 0
- [ ] `go vet ./...` exit 0；`gofmt -l pkg/tui` 无输出
- [ ] `git status --porcelain` 只多出 in-scope 文件
- [ ] `plans/README.md` 状态行改为 DONE

## STOP conditions

- 184 未完成（`grep -c "cmd.WaitDelay = gitKillGrace" pkg/skill/git_install.go` 不为 `1`）：先做完 184，否则本计划的 deadline 不生效。
- `ChatSession` 在测试里无法在不碰真实 home 的前提下构造：停下报告你需要的依赖，不要改成测 `Service.PullUpdate` 了事（那不是本计划的对象）。
- 你发现 `PullUpdate` 的超时会留下半成品 draft 或坏状态：把现场贴回来（`VersionStore` 的写入顺序），不要自行加回滚逻辑。

## Maintenance notes

- 本计划只把「永远挂着」变成「最多 2 分钟 + 一条错误」。用户仍会在这 2 分钟里看到冻结的界面——
  真正的解法是把 pull 变成后台任务 + 面板内进度（对照 `/mcp`、`/lsp` 的 `panelSession` 订阅模式，
  以及 `pkg/tui/chat_slash.go:363-421` 的安装进度卡）。那是一份独立计划。
- 评审重点：`skillPullTimeout` 是不是被测试以外的代码写过（只允许测试改）。
