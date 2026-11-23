# 计划：评审模型选择器的问题行顶替 verdict 问题行

> **执行者说明**：按步骤执行，每步跑完验证命令、对照预期再往下。遇到
> “STOP conditions” 中的任一条，停下报告，不要自行发挥。改动只留在工作区，
> **禁止任何 git commit**（owner 手动审阅提交）。

> **Drift check（先跑）**：`git log --oneline -1` 应为 `7c530851`，且
> `git status --porcelain pkg/tui` 为空；若非空或标题与此不符，先对照下文
> “Current state” 的代码摘录核实现场，内容不符即 STOP（行号漂移不算）。

## 状态

- **优先级**：P2
- **工作量**：S（2 处代码改动 + 1 个测试）
- **风险**：LOW（TUI 单面板文案/排版）
- **依赖**：无
- **类别**：dx（TUI 交互文案）
- **制定基线**：`7c530851`，工作区干净，分支 `dev`，2026-11-09
- **owner 原话**：“exit_plan_mode 审批面板里的 model picker 上方的 Which model should...
  文案移到 Would you like to proceed? 文案位置，当显示 model picker 时，不显示
  Would you like to proceed? 文案”；追加：“Which model should... 文案和下方的
  model picker 中间留出空行”。

## 目标（Why this matters）

exit_plan_mode 审批面板进入“选择评审模型”状态时，现在**同时**问两个问题：

```
Would you like to proceed?                       ← verdict 问题，保留着

Which model should review this plan? (Esc to go back)
> opencode-go/step-5-preview   https://…
```

两个问题叠在一起，用户读到的第一个问题（proceed?）此刻根本答不了——按键全被
picker 接管。owner 要的是同一时刻只问一个问题：picker 的问题占据 verdict 问题
那一行（也就是 `Would you like to proceed?` 原位置），picker 显示期间不再出现
`Would you like to proceed?`；问题行与模型行之间空一行。

目标形态（plan 正文为空时，即截图场景）：

```
Tool: exit_plan_mode
Which model should review this plan? (Esc to go back)

> opencode-go/step-5-preview   https://opencode.ai/inference…
  opencode-go/deepseek-v4.1-flash ●   https://opencode.ai/inference…
```

有 plan 正文时，问题行紧跟在计划分隔线之后，原来 `Would you like to proceed?`
就长在那里。

## Current state（现场证据，基线 7c530851）

全部相关代码在 `pkg/tui/overlays.go` 的 `(*approvalOverlay).buildApprovalContent`。

1. `pkg/tui/overlays.go:519-563` —— exit_plan_mode 的正文分支：

```go
} else if isApprovalExitPlanMode(o.req) {
    writeLine("Tool: %s", toolName)
    planContent := approvalPlanContent(o)
    ... 计划正文、每条 PlanReviews ...
    if planContent != "" || len(o.req.PlanReviews) > 0 {
        writeLine("Would you like to proceed?")          // ← 562：要顶替的那一行
    }
}
```

2. `pkg/tui/overlays.go:666` —— 正文与选项列表之间固定一个空行：

```go
    writeLine("")

    switch {
```

3. `pkg/tui/overlays.go:697-718` —— picker 分支（问题行在这里重复打印一次，
   且与模型行之间**没有**空行）：

```go
    case o.planReviewPicking:
        o.planReviewTopRow = lineCount
        writeLine("Which model should review this plan? (Esc to go back)")
        models := o.req.PlanReviewModels
        list := newPlanReviewList(models, width)
        for i, model := range models {
            ...
        }
```

4. 结构体字段 `pkg/tui/overlays.go:61-69`（`planReviewPicking`、
   `planReviewTopRow`、`focusStart/focusEnd` 的语义注释）与
   `pkg/tui/overlays.go:874-884`（首次绘制把 `planReviewTopRow` 锚到视口顶部）。
   `planReviewTopRow` 是“问题行”的行号，用于首帧滚动锚点；问题行搬家后这个
   赋值也要跟着搬，否则首帧锚点会落在模型行上。

5. 现成测试（`pkg/tui/chat_session_test.go`）：
   - `exitPlanReviewRequest`（:10769）造一个带两行 `PlanReviewModels` 的
     exit_plan_mode 请求；
   - `exitPlanReviewPickerLines`（:10965）进入 picker 并把渲染行去色返回；
   - `TestExitPlanOverlayRendersPickerAndCollectedReviews`（:10922）、
     `TestPlanReviewPickerKeepsTheSelectionOnScreen`（:4422）断言 picker 的问题
     文案在屏、且滚动到最后一个模型时滚出屏幕——这些断言在本改动后仍应成立
     （只是问题行上移两行）。

仓库惯例：TUI 面板文案/排版写在 `pkg/tui/overlays.go`；断言用被去色的渲染行
（`stripANSI`）而不是终端截图，见同文件既有用例。

## 行为变化矩阵

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 未进 picker，有 plan 或评审 | `Would you like to proceed?` + 空行 + verdict 行 | **不变** |
| 未进 picker，无 plan 无评审 | 无 verdict 问题行 | **不变** |
| 进 picker，有 plan/评审 | 计划 → `Would you like to proceed?` → 空行 → 问题 → 模型行 | 计划 → **问题** → 空行 → 模型行，`Would you like to proceed?` 不出现 |
| 进 picker，无 plan 无评审（截图场景） | `Would you like to proceed?` 不出现（旧条件）→ 空行 → 问题 → 模型行 | **问题** → 空行 → 模型行 |
| picker 内 Esc 返回 | verdict 问题行照旧 | **不变** |
| 滚动到最后一个模型 | 问题行滚出视口、按键提示进入 | 不变（行号上移 2 行，锚点随 `planReviewTopRow` 一起搬） |

## Scope

**In scope**（只改这两个文件）：
- `pkg/tui/overlays.go`
- `pkg/tui/chat_session_test.go`（新增一个回归用例）

**Out of scope**（明确不碰）：
- `frontend/src/locales/index.ts:4480`（`chat.exitPlanReviewChoose`）与 web
  `PlanApproval` 卡片：owner 只要求 TUI；web 由同一语义的另一 surface 承担，
  改它属于另一单。
- `pkg/gateway/api_extra.go` 的 `plan_review_models` 线格式、`pkg/turn/approval.go`
  的 `PlanReviewModelOptions`：本改动不涉及数据。
- `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/tui-prototype.html`：设计原型，
  处于 gitignore 的计划目录，不是产品代码；如 owner 要求再同步。
- `.worktrees/plan-review-*` 下同名文件：别的会话的工作树，**不要**改。

## Commands you will need

| 用途 | 命令 | 预期 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| 定点测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'ExitPlanReview|PlanReviewPicker|ExitPlanOverlay' -count=1` | ok |
| 包级回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok |
| vet | `go vet ./...` | exit 0 |
| 真机验收构建 | `go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |

（`FOREBRAIN_HOME` 必须是 mktemp 目录：测试/驱动会写 home。）

## Steps

### Step 1：问题行搬到 verdict 行位置

`pkg/tui/overlays.go`，把 561-563 的

```go
		if planContent != "" || len(o.req.PlanReviews) > 0 {
			writeLine("Would you like to proceed?")
		}
```

改成（picker 打开时用问题行顶替 verdict 问题，并在同一处记下锚点行）：

```go
		// 评审 picker 的问题顶替 verdict 那一行，而不是在它下面再问一遍：
		// 面板同一时刻只问一个问题，且 picker 打开时 proceed 根本答不了。
		// 行号在这里记下：首帧的滚动锚点就是这一行。
		switch {
		case o.planReviewPicking:
			o.planReviewTopRow = lineCount
			writeLine("Which model should review this plan? (Esc to go back)")
		case planContent != "" || len(o.req.PlanReviews) > 0:
			writeLine("Would you like to proceed?")
		}
```

注释用英文写（本文件既有注释全是英文，保持仓库风格；上面给出的是含义，
落盘请写成英文短注释）。

**Verify**：`grep -n "Which model should review" pkg/tui/overlays.go` → 只剩
一处（本步新增的这行）。

### Step 2：picker 分支删掉重复的问题行

`pkg/tui/overlays.go` 的 `case o.planReviewPicking:`（原 697-700）删掉两行：

```go
		o.planReviewTopRow = lineCount
		writeLine("Which model should review this plan? (Esc to go back)")
```

只保留 `models := o.req.PlanReviewModels` 起的循环体。问题行与模型行之间的
空行来自 Step 1 之前本就存在的 `writeLine("")`（原 666 行），**不要**再加一个
空行。

**Verify**：`sed -n '/case o.planReviewPicking:/,+6p' pkg/tui/overlays.go` →
首行是 `case …`，紧跟 `models := o.req.PlanReviewModels`，无 `writeLine("Which…")`；
`grep -c "Which model should review" pkg/tui/overlays.go` → `1`。

### Step 3：补直接回归用例

`pkg/tui/chat_session_test.go`，紧挨
`TestExitPlanOverlayRendersPickerAndCollectedReviews`（:10922）之后新增：

```go
// The picker asks its own question in the verdict line's place instead of below
// it: the panel asks one thing at a time, and "Would you like to proceed?" is
// not answerable while the model list holds the keys. A blank line separates
// the question from the models it asks about.
func TestExitPlanReviewPickerQuestionTakesTheVerdictLine(t *testing.T) {
	req := exitPlanReviewRequest(t, "# Plan\n\nStep one.\n")
	lines := func(picking bool) []string {
		overlay := newApprovalOverlay(context.Background(), io.Discard, nil, req, nil)
		if picking {
			overlay.enterPlanReviewMode()
		}
		rendered, _ := overlay.buildApprovalContent(100)
		out := make([]string, len(rendered))
		for i, line := range rendered {
			out[i] = stripANSI(line)
		}
		return out
	}
	at := func(lines []string, want string) int {
		for i, line := range lines {
			if strings.Contains(line, want) {
				return i
			}
		}
		return -1
	}
	const (
		proceed  = "Would you like to proceed?"
		question = "Which model should review this plan?"
	)
	verdict, picking := lines(false), lines(true)
	proceedAt := at(verdict, proceed)
	if proceedAt < 0 {
		t.Fatalf("the verdict list must ask %q:\n%s", proceed, strings.Join(verdict, "\n"))
	}
	if got := at(picking, proceed); got >= 0 {
		t.Fatalf("the picker repeated the verdict question on line %d:\n%s", got, strings.Join(picking, "\n"))
	}
	questionAt := at(picking, question)
	if questionAt != proceedAt {
		t.Fatalf("the question is on line %d, want the verdict line %d:\n%s",
			questionAt, proceedAt, strings.Join(picking, "\n"))
	}
	if questionAt+1 >= len(picking) || strings.TrimSpace(picking[questionAt+1]) != "" {
		t.Fatalf("no blank line between the question and the models:\n%s", strings.Join(picking, "\n"))
	}
	if questionAt+2 >= len(picking) || !strings.Contains(picking[questionAt+2], "openai/gpt-5.1") {
		t.Fatalf("the model rows do not start under the blank line:\n%s", strings.Join(picking, "\n"))
	}
}
```

**Verify**：命令见上表“定点测试” → `ok`（新用例与既有 picker 用例全过）。

### Step 4：真机验收（必做）

按 `plan-gated-bugfix` 技能第 7 步与 `.claude/skills/run-forebrain/SKILL.md`：

```bash
D=.claude/skills/run-forebrain/driver.sh
$D build                                  # 改了 Go 必须先重建，否则跑旧二进制
$D start toolcall                         # 假模型依次调 enter_plan_mode、exit_plan_mode
$D submit 'plan this change'
$D wait 'Would you like to proceed?' 30   # 未进 picker：verdict 问题照旧
$D screen                                 # 取证 A：旧布局（问题行在 picker 问题之上）
$D key m                                  # 打开评审模型 picker
$D screen                                 # 取证 B：问题行顶替 verdict 行，下方一空行再列模型
$D key Escape                             # 返回 verdict 列表
$D screen                                 # 取证 C：Would you like to proceed? 回来
$D stop
```

断言（对照 `$D screen` 的纯文本）：
- 取证 A 含 `Would you like to proceed?` 且**不含** `Which model should review`；
- 取证 B **不含** `Would you like to proceed?`，含 `Which model should review this plan? (Esc to go back)`，
  且该行下一行是空行、再下一行是以 `>` 开头的模型行；
- 取证 C 与 A 相同（问题行回来）。

`driver.sh` 的配置里只有一行模型（`deepseek/deepseek-chat`），picker 里就是
一行——`len(PlanReviewModels) > 0` 已足够打开 picker（`m` 键）。

### Step 5：收口

- 把本计划复制到 `docs/plan/PLAN_REVIEW_PICKER_QUESTION_LINE_PLAN.md`
  （该目录 gitignore，不入库），并补“验收记录”章节（三条取证的原文片段）。
- `git status --porcelain` 只应出现 `pkg/tui/overlays.go`、`pkg/tui/chat_session_test.go`。
- 不 commit。

## Test plan

- 新增：`TestExitPlanReviewPickerQuestionTakesTheVerdictLine`（上方），覆盖
  “同一行号顶替 + proceed 不出现 + 空行 + 模型行随即开始”。
- 既有：`TestExitPlanOverlayRendersPickerAndCollectedReviews`、
  `TestPlanReviewPickerKeepsTheSelectionOnScreen`、
  `TestExitPlanReviewPickerRendersTwoColumns` 等全部保持绿色——它们断言的是
  问题文案仍在屏、滚动行为不变，本改动不应改动其期望；若其中某条因为行号
  上移而失败，先确认是断言写死了行号（STOP 报告），不要为了让测试过而改回文案。

## Done criteria（机器可查）

1. `grep -c "Which model should review" pkg/tui/overlays.go` → `1`。
2. `grep -c "Would you like to proceed" pkg/tui/overlays.go` → `1`。
3. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → ok。
4. `CGO_ENABLED=1 go build ./... && go vet ./...` → exit 0。
5. 真机三条取证（A/B/C）与上方断言一致。
6. `git status --porcelain` 仅两个 in-scope 文件。

## STOP conditions

- 基线不是 `7c530851`，或 `pkg/tui/overlays.go` 的 561-563 / 697-700 一带内容
  与上文摘录不符（别的会话已改过这段排版）——先报告现场，不要叠加改动。
- 进入 picker 后问题行位置仍与 verdict 行不同（说明还有第二处打印问题的地方）。
- 验收时 `m` 打不开 picker（`PlanReviewModels` 为空）——是驱动配置问题，
  不要改产品代码去造模型列表。
- 任何需要改 `frontend/`、`pkg/turn/`、`pkg/gateway/` 才能"看起来对"的冲动：
  超出 scope，停下问 owner。

## Maintenance notes

- 语义在此收口：**exit_plan_mode 面板同一时刻只显示一个问题行**。以后加新的
  picker/子状态时，沿用“顶替 verdict 行 + 下方一个空行”的形态，不要再往
  verdict 行下面追加第二个问题。
- `planReviewTopRow` 的含义不变（问题行行号），只是打印点前移到正文分支；
  首帧滚动锚点（`renderViewport`）依赖它，改动位置时两处要一起动。
- web surface 的 plan 审批卡仍并列两个问题；若 owner 后续要求对齐，按
  `frontend/src/locales/index.ts` 的 `chat.exitPlanReview*` 另开一单。

## 验收记录（2026-11-10，基线 7c530851 + 本改动）

驱动：`.claude/skills/run-forebrain/driver.sh`（fake provider `toolcall`，
隔离 `FOREBRAIN_RUN_WORK`/`FOREBRAIN_RUN_SESSION`/`FOREBRAIN_RUN_PORT`）。
计划里的驱动工作目录 `proj` 不是 git 仓库，基线会把这类目录判定为 office
项目（`DefaultProjectTypeForRoot`），而 office 项目不注册 `enter_plan_mode`/
`exit_plan_mode`；因此预先把 `proj` 做 `git init` 再启动，项目类型为 coding，
产品代码未动。

取证 A（未进 picker）：verdict 问题在，picker 问题不在。

```
Here is the plan:
────────────────────────────────────────────────
No plan found.
────────────────────────────────────────────────
Would you like to proceed?

> 1. Yes, clear context and proceed
  2. Yes, proceed
  3. No, keep planning
  4. Ask another model to review this plan
```

取证 B（`m` 打开 picker）：verdict 问题消失，picker 问题占同一行，下面空一
行再列模型。

```
Here is the plan:
────────────────────────────────────────────────
No plan found.
────────────────────────────────────────────────
Which model should review this plan? (Esc to go back)

❯ deepseek/deepseek-chat ●http://127.0.0.1:8769/chat/completions
```

取证 C（Esc 返回）：verdict 问题回来，选择停在评审行。

```
Would you like to proceed?

  1. Yes, clear context and proceed
  2. Yes, proceed
  3. No, keep planning
> 4. Ask another model to review this plan
```

三条断言均满足：A/C 含 `Would you like to proceed?` 且不含 picker 问题；B 不
含 `Would you like to proceed?`，picker 问题下一行为空行、再下一行为模型行。
测试与构建：`go test -tags fts5 ./pkg/tui -count=1` ok；`go build ./...`、
`go vet ./...` exit 0。
