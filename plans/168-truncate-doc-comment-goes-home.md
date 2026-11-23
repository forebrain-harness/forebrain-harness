# Plan 168：把走错位置的显示注释归还给 `TruncateForDisplay`

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func DisplayLine" pkg/tool/display_header.go` 为 `1`；
> `grep -c "func TruncateForDisplay" pkg/tool/display_header.go` 为 `1`；
> `grep -c "truncateForDisplay shortens" pkg/tool/display_header.go` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/tool/display_header.go` 必须**无输出**。

## Status

- **Priority**: P3（godoc 与真实契约相反；本项目最容易踩的正是列宽/CJK 规则）
- **Effort**: S（搬一段注释 + 补一段函数文档，零逻辑改动）
- **Risk**: LOW（纯注释）
- **Depends on**: 无
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: docs

## Why this matters

`pkg/tool/display_header.go` 里有一段写得相当好的注释，讲"按终端列数而不是字节截断"的理由
（多字节字符被切会画出替换字形；CJK 三个字节两列，按字节算会让标题只拿到三分之一宽度）。
问题是它**挂错了函数**：

- 这段注释紧跟着定义的是 `DisplayLine`——而 `DisplayLine` 的契约正好相反：**不截断**，只做"单行化 + UTF-8 修复"；
- 被描述的函数 `TruncateForDisplay`（下一个声明）**没有文档**。

godoc 与 IDE 悬浮于是显示一份自相矛盾的契约：悬在 `DisplayLine` 上的是"会截断、按列算"，
实际这个函数从不截断。后果不是编译错误，而是**下一次改动的方向性错误**：维护者按注释去"修"
`DisplayLine` 的截断行为，或以为 `TruncateForDisplay` 已经不用按列算而去改成 `len(s)`/`s[:maxLen]`
——后者会把 CJK 切碎。

要做成：注释归位。`TruncateForDisplay` 拿到它该有的那段（列数、CJK、无效 UTF-8 的理由），
`DisplayLine` 保留"单行、不设上限"的那半句（它现在本来就写着，只是被前一段抢了位置）。

## Current state

`pkg/tool/display_header.go:381-413`：

```go
// truncateForDisplay shortens s to at most maxLen terminal columns, ending it
// with an ellipsis when it had to cut.
//
// Every caller spends maxLen as a column budget (a task title beside a fixed
// label, a query inside a card), so columns — not bytes — is the unit that
// makes those budgets come out right. Counting bytes went wrong twice: it cut
// multi-byte characters mid-rune, which the terminal draws as a replacement
// glyph, and it under-filled the budget for wide text, since CJK costs three
// bytes per two columns — a title got barely a third of the width it was given.
//
// Input that is already invalid UTF-8 is repaired rather than passed along:
// these strings come from tool output and model text that something upstream
// may have byte-sliced, and runewidth measures a stray byte as zero columns,
// which would silently skew the budget it is asked to enforce.
// DisplayLine normalizes a value for a tool header: one logical line, valid
// UTF-8, and no length cap.
//
// A header segment that carries the call's own arguments must use this rather
// than truncateForDisplay. wrapToolDisplayLine already folds a long header onto
// continuation rows, so there is nothing to gain from cutting the tail off —
// and a reader who cannot see the last of the arguments cannot tell what the
// call actually asked for.
func DisplayLine(s string) string {
	s = SingleDisplayLine(strings.TrimSpace(s))
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	return s
}

func TruncateForDisplay(s string, maxLen int) string {
	return runewidth.Truncate(DisplayLine(s), maxLen, "…")
}
```

注意：全文**没有**叫 `truncateForDisplay` 的函数（小写那版在 `pkg/tui/tool_header.go:71`，
只是一个转发到 `tool.TruncateForDisplay` 的一行包装）——所以这段注释既挂错人，也写错名。

调用点（改注释时不要动它们）：`pkg/tool/header_parts.go:210,212`（查询 72 列、行号 32 列）、
`pkg/tui/tool_header.go:71`。

## Scope

**In scope（只改这些）**：
- `pkg/tool/display_header.go`（**仅注释**：把截断那段搬到 `TruncateForDisplay` 上、把"不设上限"那段留给
  `DisplayLine`、把 `truncateForDisplay` 的写法改成真实名字）

**Out of scope（看着相关也不要动）**：
- `runewidth.Truncate` 的实现选择、`SingleDisplayLine`、`strings.ToValidUTF8` 的行为
- `pkg/tui/tool_header.go` 那行包装
- `pkg/tool/header_parts.go` 的列宽预算（72/32 是既有取值）
- 任何字符串处理逻辑（本计划 diff 里**不允许**出现非注释的代码行）

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 本包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` | 与基线相比无新增 FAIL |
| TUI 回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | 与基线相比无新增 FAIL |
| 文档渲染 | `go doc ./pkg/tool TruncateForDisplay`；`go doc ./pkg/tool DisplayLine` | 前者首句是"shortens s to at most maxLen terminal columns"；后者首句是"normalizes a value … no length cap" |
| vet / gofmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool`；`gofmt -l pkg/tool` | exit 0；无输出 |

## Steps

### Step 1：把截断那段移到 `TruncateForDisplay`

改完后的形状（注释文字除名字外保持原文，不要重写理由——它们是这个仓库的既有知识）：

```go
// DisplayLine normalizes a value for a tool header: one logical line, valid
// UTF-8, and no length cap.
//
// A header segment that carries the call's own arguments must use this rather
// than TruncateForDisplay. wrapToolDisplayLine already folds a long header onto
// continuation rows, so there is nothing to gain from cutting the tail off —
// and a reader who cannot see the last of the arguments cannot tell what the
// call actually asked for.
func DisplayLine(s string) string { … }

// TruncateForDisplay shortens s to at most maxLen terminal columns, ending it
// with an ellipsis when it had to cut.
//
// Every caller spends maxLen as a column budget (a task title beside a fixed
// label, a query inside a card), so columns — not bytes — is the unit that
// makes those budgets come out right. Counting bytes went wrong twice: it cut
// multi-byte characters mid-rune, which the terminal draws as a replacement
// glyph, and it under-filled the budget for wide text, since CJK costs three
// bytes per two columns — a title got barely a third of the width it was given.
//
// Input that is already invalid UTF-8 is repaired rather than passed along:
// these strings come from tool output and model text that something upstream
// may have byte-sliced, and runewidth measures a stray byte as zero columns,
// which would silently skew the budget it is asked to enforce.
func TruncateForDisplay(s string, maxLen int) string { … }
```

**Verify**：`go doc ./pkg/tool DisplayLine | head -4` 与 `go doc ./pkg/tool TruncateForDisplay | head -4`
各显示对应首句（见命令表）。

### Step 2：确认没有第三份同名注释

**Verify**：`grep -rn "truncateForDisplay shortens" pkg/` → 只有 `pkg/tool/display_header.go` 一处（且已在新位置）；
`grep -rn "truncateForDisplay" pkg/tui/tool_header.go` 仍是那一行包装（未被牵连重命名）。

### Step 3：回归（行为零改动的证明）

**Verify**：`git diff -- pkg/tool/display_header.go` 里**只有**注释行（`+` / `-` 都不得出现非 `//` 或空行）。
用 `git diff -U0 -- pkg/tool/display_header.go | grep -E "^[+-]" | grep -vE "^(\+\+\+|---)" | grep -vE "^[+-]\s*//" | grep -vE "^[+-]$"`
→ **无输出**。
再跑 `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui -count=1`
→ 与基线相比无新增 FAIL。

## Test plan

纯注释，无新增测试。回归靠上面的"diff 只含注释"闸门 + 两个包的既有用例。

## Done criteria

机器可检查，全部满足：

- [ ] `go doc ./pkg/tool TruncateForDisplay` 的首句包含 `at most maxLen terminal columns`
- [ ] `go doc ./pkg/tool DisplayLine` 的首句包含 `no length cap`
- [ ] `grep -c "truncateForDisplay shortens" pkg/tool/display_header.go` 为 `0`
- [ ] `grep -c "TruncateForDisplay shortens" pkg/tool/display_header.go` 为 `1`
- [ ] 上面的"diff 只含注释"命令**无输出**
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui -count=1` 与基线相比无新增 FAIL
- [ ] `gofmt -l pkg/tool` 无输出
- [ ] `git status --porcelain` 只显示 `pkg/tool/display_header.go`
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数与标注不符，或 drift check 有输出且"Current state"摘录与现场代码对不上。
2. "diff 只含注释"的闸门出现输出（说明你顺手动了代码）——回退那一行，只保留注释改动。
3. `go doc` 只显示一段（Go 只把紧邻声明的那一段作为 doc）——说明你把两段之间漏了空行或多写了注释块，
   按 Step 1 的形状对齐。
4. 你发现 `pkg/tui/tool_header.go:71` 的包装也需要文档：那是另一个文件，报告后停手。

## Maintenance notes

- 这条规则的通用形态：**注释要贴着它描述的那个声明**。本仓多处是"多段说明 + 紧邻的声明"，
  改动时最容易产生的缺陷就是把下一段的说明抢过来（本次即如此）。
- 评审重点：本计划的 diff 应当只有 `//` 行；任何 `func` 行的变化都是越界。
- 若将来给 `DisplayLine` 加真正的上限（不推荐——`wrapToolDisplayLine` 已经负责折行），
  必须同时改本节两段注释与 `header_parts.go` 的列宽预算。
