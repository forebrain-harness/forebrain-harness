# 计划：删除 shell 卡片 "(file diff unavailable for this command)" 噪音文案

> **状态：已实施、真机验收通过并合并回 dev（2026-11-18）**。dev@`5047b769`
> （rebase 后 commits 485f5142 + 5047b769，ff-only 合并，零文件重叠）。
> 合并后 dev 基线定点验证绿；worktree 与分支已按强制收尾规则删除。
> 详见文末 Implementation Status 与验收记录。

> **执行者须知**：逐步执行本计划，每步跑验证命令并确认预期结果后再继续。
> 遇到「STOP conditions」立即停下报告，不得即兴发挥。本计划经 owner 批准后
> 直接在当前源码实施（不用 worktree、不 commit，改动留工作区由 owner 审阅）。
> 本计划已经过独立冷读评审并把核实过的修正并入（评审发现的 raw-body 泄漏
> BLOCKER 已修入设计；真机配方已补 submit/wait、PATH 确定性、grep 修正）。
>
> **Drift check（第一步先跑）**：基线 commit `2c38938f`（2026-11-18 记录时
> in-scope 文件全部干净）。跑
> `git diff --stat 2c38938f..HEAD -- pkg/tool/diff_blocks.go pkg/tool/diff_blocks_test.go pkg/tool/format_test.go pkg/tool/shell_card_test.go pkg/tui/render.go pkg/tui/render_test.go frontend/src/locales/diffNotes.ts frontend/src/components/chat/FileChangeList.test.ts`
> 以及 `git status --porcelain` 对照同样路径。有改动时，把下面「Current state」
> 摘录与活代码逐字对照；**内容不匹配即 STOP**（行号漂移不算不匹配，用符号重新定位）。

## Status

- **Priority**: P1（owner 直接裁决的 UI 噪音）
- **Effort**: S-M（Go 两个文件 + 前端一个文件 + 测试 + 真机验收）
- **Risk**: LOW（纯显示层；数据字段与 hook 契约不动）
- **Depends on**: 无
- **Category**: bug（UI 缺陷：内部机制失败状态暴露给用户）
- **Planned at**: commit `2c38938f`, 2026-11-18
- **落盘惯例**：批准后把本文件复制为 `docs/plan/SHELL_DIFF_UNAVAILABLE_NOTE_REMOVAL_PLAN.md`
  （本仓 plan-gated-bugfix 惯例；docs/plan 已 gitignore，不入库）。会话计划文件是权威版本。

## 目标（Why this matters）

owner 在 TUI transcript 里看到 shell 工具卡 output 区域显示
`(file diff unavailable for this command)`，问其语义与价值，并裁决：
**「如果无价值，请删掉，不要显示，不要暴露给用户」**。

语义（已核实的完整因果链）：shell 卡片仿 Claude Code bashEditDiff，对非只读
命令在私有 GIT_DIR 里做命令前/后两次 git 树快照，把命令造成的文件改动渲染成
卡片上的 diff 块。当这套**辅助 diff 机制自身失败**时（快照 git 调用出错/超时，
或只有超大/不可读文件变化），卡片就显示这句「文件 diff 不可用」。它报告的是
内部机制的失败，不是命令结果——用户跑 `go test` 得到了完整输出，这句话既不说
原因（错误在代码里被吞掉）、也不提供任何可操作信息，纯噪音。owner 判定无价值，
要求所有 surface 不再显示。

本计划把「diff 不可用」这一内部状态从用户可见文案中彻底移除：新输出不再产生；
历史落盘卡片在解析层过滤（TUI live/replay、web live/stored 共用 ParseDiffBlocks
接缝），TUI 通用渲染器的 raw-body 回落单独补过滤（评审证实的泄漏点）。真实的
「N 个文件改动」计数信息保留，归一到既有 note 措辞。

## Current state（根因证据链，全部已逐字读过）

### 触发源（pkg/tool/shell_diff.go —— 本计划不改这个文件）

`Unavailable=true` 的四个来源：

1. before 快照失败（错误被吞）：
```go
// pkg/tool/shell_diff.go:1038-1045 (beginShellEditDiff)
	repo.mu.Lock()
	before, err := repo.snapshot(ctx)
	repo.mu.Unlock()
	if err != nil {
		run.unavailable = true
	} else {
		run.before = before
	}
```
2. finish 时 unavailable：
```go
// pkg/tool/shell_diff.go:1064-1066 (finish)
	if r.unavailable {
		return &ShellEditDiff{Files: []ShellEditDiffFile{}, Unavailable: true, Shared: r.shared}
	}
```
3. finish 时 diffSince 出错（shell_diff.go:1067-1071，同形状返回）。
4. 树未变但只有大文件/不可读文件变化（`largeChanged` = 超过
   `shellDiffMaxHashBytes`=20MB 或 `os.Open` EACCES 的文件，按 size+mtime
   签名跟踪，见 classifyChanged:702-739）：
```go
// pkg/tool/shell_diff.go:783-790
func (r *shellDiffRepo) unavailableDiff() *ShellEditDiff {
	changed := shellDiffAbsPaths(r.top, r.largeChanged)
	return &ShellEditDiff{
		MoreFiles:    len(r.largeChanged),
		ChangedFiles: changed,
		Unavailable:  true,
	}
}
```
   另注意 diffSince 成功路径也置 `Unavailable: len(r.largeChanged) > 0`，且
   `MoreFiles: len(records) - len(files) + len(r.largeChanged)`
   （shell_diff.go:892-897）——所以 Files=0、MoreFiles>0、Unavailable 时计数
   可含 binary/mode-only 记录，不只大文件。
快照超时预算：首次 10s、后续 750ms（shellDiffTimeoutFor:497-505）；连续失败
2 次后该 repo 被放弃（shellDiffGiveUpAfter，shellDiffRepoFor:333-335）。
owner 案例（worktree 里 8m24s 的 go test、磁盘 98% 满、并行会话同改主树）：
具体 git 错误被吞、不可从现场恢复；最可能是 750ms 快照超时或 ENOSPC。
**删除渲染覆盖全部触发路径，不需要区分是哪条。**

### 文案产生（写侧，唯一出口）

```go
// pkg/tool/diff_blocks.go:253-273
func diffMoreFilesNote(filesShown bool, n int, unavailable bool) string {
	var note string
	if filesShown {
		note = fmt.Sprintf("… %d more %s changed", n, diffFilesWord(n))
	} else {
		note = diffFileCount(n) + " changed (binary, mode only or too large to show)"
	}
	if unavailable {
		note += " (part of the diff is unavailable)"
	}
	return note
}

func diffUnavailableNote(n int) string {
	if n > 0 {
		return "(file diff unavailable for this command; " + diffFileCount(n) + " changed)"
	}
	return "(file diff unavailable for this command)"
}
```
```go
// pkg/tool/diff_blocks.go:351-364 (shellEditDiffBlocks 头部)
	diff, ok := DecodeShellEditDiff(output["shell_edit_diff"])
	if !ok {
		return DiffBlocks{}, false
	}
	if diff.Skipped {
		return DiffBlocks{Notes: []string{diffNoteSkipped}}, true
	}
	if len(diff.Files) == 0 && diff.Unavailable {
		return DiffBlocks{Notes: []string{diffUnavailableNote(diff.MoreFiles)}}, true
	}
	if len(diff.Files) == 0 && diff.Shared {
		return DiffBlocks{Notes: []string{diffNoteShared}}, true
	}
// ... 文件块循环 ...
// pkg/tool/diff_blocks.go:383-391
	if diff.MoreFiles > 0 {
		blocks.Notes = append(blocks.Notes, diffMoreFilesNote(len(blocks.Files) > 0, diff.MoreFiles, diff.Unavailable))
	}
	if diff.TreeMoved {
		blocks.Notes = append(blocks.Notes, diffNoteTreeMoved)
	}
	if diff.Shared {
		blocks.Notes = append(blocks.Notes, diffNoteShared)
	}
```
落盘：`pkg/tool/format.go:834` `formatShellStep` 调 `shellDiffBlocks(evt.Output)`
→ `diffBlocksMarkdown` 写进卡片 body（持久化文本）。**stdout 为空时不写
stdout: 段**（formatShellStep 的 `if stdout != ""`）——静默命令（touch/chmod）
的 body 只有 input/output JSON 段 + note，这是下面 raw-body 泄漏的前提。

### 渲染出口（三个 surface + 一个回落）

- TUI live+replay：`pkg/tui/render.go:7101` `renderShellDiffCard` →
  `tool.ParseDiffBlocks(content)`；布局判定 `pkg/tui/reducer.go:3058-3062`
  `shellFrameHasDiffBlocks` → `tool.HasDiffBlocks(f.Content)`。
- web live+stored：gateway `shellCardFor`（wsevents.go:962、server.go:485、
  api_extra.go:2435）→ `tool.ParseShellCard`（pkg/tool/shell_card.go:101-109
  内部调 `ParseDiffBlocks(body)` 取 Files/Notes）。web 无 raw 泄漏：
  ParseShellCard 只认标签 fence 段，notes 全来自 ParseDiffBlocks（评审已核实）。
- `ParseDiffBlocks` 与 `HasDiffBlocks` 共用 `scanDiffBlocks`
  （pkg/tool/diff_blocks.go:161-222）。note 识别分支：
```go
// pkg/tool/diff_blocks.go:206-213
		if diffBlockNote(trimmed) {
			flush()
			blocks.Notes = append(blocks.Notes, trimmed)
			if stopEarly {
				return blocks, true
			}
			continue
		}
```
  `diffBlockNote`（:120-126）以 `"(file diff "` 等前缀识别 note。
- **TUI 通用渲染器 raw-body 回落（评审证实的泄漏点，必须一并修）**：
  历史 note-only body 过滤后 `HasDiffBlocks=false` → `renderShellDiffCard`
  ok=false → 回落链：
  render.go:2341-2347（diff 卡 try 失败）→ 2396-2404（summaryToolBody →
  formatToolContentWithInput）→ 2950-2953（shell → formatShellOutput）→
  3030-3041（**无 stdout/stderr sections 时** → `stripToolMetadataSections`）→
  4039-4098（:4093 `out = append(out, line)` 把 note 纯文本行原样保留）→
  屏幕仍显示该句。compact 预览同回落：3379 → visibleToolOutput 3929-3935。
  `stripToolMetadataSections` 的三个调用点：2964（非 shell 通用）、3040
  （shell raw 回落）、3934（compact raw 回落）——在它里面丢弃 unavailable
  行一处覆盖全部。**带 stdout 的历史 body（owner 的 go test 案例）不受影响**：
  sections 命中即只显 stdout；漏的是静默命令这一真实子类。
  安全性：shell 命令输出永远在标签 fence 内（sections 路径不经过此函数）；
  fence 内容在函数内整段提取、不逐行检查，不会误吃命令输出。
- 前端翻译：`frontend/src/locales/diffNotes.ts` —— `PART_UNAVAILABLE` 常量、
  `NOTE_RULES` 里两条 unavailable 规则（正则
  `^\(file diff unavailable for this command; (\d+) files? changed\)$` 与
  `^\(file diff unavailable for this command\)$`）、`translateDiffNote` 的后缀
  剥离/拼接逻辑、`diffNoteIcon` 的 `note.startsWith('(file diff unavailable')`
  分支。渲染组件 `FileChangeList.vue:35-37`，notes 唯一来源是 gateway
  shell_card（评审核实 ToolGateFacts/DiffView 不喂 notes）。Go 侧过滤后这些
  规则全部不可达。
- `ShellEditDiff.Unavailable` 字段消费者仅 `HookToolResponse`
  （pkg/tool/shell_diff.go:74-92，PostToolUse hook tool_response）与渲染；
  turn/events.go:252 只读 Files。**字段保留，只改渲染**，hook 数据契约不动。
- pkg/channel、docs、pkg/tui 与 pkg/gateway 的测试均无此文案（已 grep 核实）。

### 钉住文案的测试（需随契约更新）

- `pkg/tool/diff_blocks_test.go:130-160` `TestDiffBlocksNoteTexts`：
  `diffMoreFilesNote(true,1,false)`/`(true,2,false)`/`(false,3,true)` 断言、
  `diffUnavailableNote(1)`/`(0)` 断言、roundtrip notes 列表含
  `diffMoreFilesNote(false,3,true)` 与 `diffUnavailableNote(1)`。
  roundtrip 助手 `diffBlocksRoundTrip`（:10）= markdown 写出再 ParseDiffBlocks 读回。
- `pkg/tool/format_test.go:883-956` `TestFormatToolStepResult_shellEditDiffNotes`：
  cases `{"unavailable", ShellEditDiff{Unavailable: true, MoreFiles: 2}, []string{diffUnavailableNote(2)}}`
  与 `"full"`（`diffMoreFilesNote(true, 3, true)` + treeMoved + shared）；
  每个 case 跑 live（struct）与 replay（JSON round-trip）两半，`assertBody`
  要求 note 存在且 `ParseDiffBlocks(body).Notes` 深度相等。
- `pkg/tool/shell_card_test.go:17` `TestParseShellCardReadsBackWhatFormatShellStepWrote`
  —— 新增历史过滤用例的结构样板。
- `pkg/tui/render_test.go` 已有 formatShellOutput/renderShellDiffCard 相关测试
  —— 新增 raw-body 过滤回归的样板文件。
- `pkg/tool/shell_diff_test.go:420` `TestShellEditDiffLargeFilesAreUnavailable`
  断言的是 `diff.Unavailable` **字段**——字段保留，此测试不动。
- `frontend/src/components/chat/FileChangeList.test.ts:59-95`：中文翻译用例含
  `'3 files changed (binary, mode only or too large to show) (part of the diff is unavailable)'`
  → `'3 个文件改动（二进制、仅权限或太大，不显示）（部分差异不可用）'`、
  `'(file diff unavailable for this command; 2 files changed)'` →
  `'这条命令的文件差异不可用（2 个文件改动）'`；英文用例含
  `'… 7 more files changed (part of the diff is unavailable)'` 原样透传。

### 仓库约定（照抄现有风格）

- Go 注释风格：完整句子解释「为什么」，见 diff_blocks.go 现有函数注释。
- 测试风格：表驱动 + `t.Fatalf` 带 got/want，见 TestDiffBlocksNoteTexts。
- 本仓裁决：不为不可达状态写防御代码；同一语义归一到既有实现，不新造措辞。
  计数 note 复用既有 `diffMoreFilesNote` 的 no-files-shown 措辞，不新造句子
  （该措辞涵盖 binary/mode-only/too-large 主因；unreadable 文件是既有近似，
  不为此扩句）。前缀常量从 pkg/tool 导出共享，TUI 不另写一份字符串。
- 全仓文案禁 `·` 连接符（本计划不涉及）。

## 修复设计 + 行为变化矩阵

**写侧**（diff_blocks.go）：删除 unavailable 分支与 `diffUnavailableNote`；
`diffMoreFilesNote` 去掉 unavailable 参数与后缀。`Files==0 && MoreFiles>0` 自然
落到既有计数 note；`Files==0 && MoreFiles==0` 无文件无 note → 返回 false，卡片
只剩命令输出（与 `dropExternalWrites` 注释所述「go test 的真相就是命令输出」
一致，shell_diff.go:1095-1099）。shared 早退条件收紧为
`Files==0 && MoreFiles==0 && Shared`，保住计数。

**读侧 A**（scanDiffBlocks，三 surface 共用接缝）：丢弃
`DiffUnavailableNotePrefix`（新导出常量 `"(file diff unavailable"`）前缀的
note 行；其余 note 剥离尾部 `diffPartUnavailableSuffix`。被丢弃的 note 不触发
stopEarly（保证 HasDiffBlocks 与 ParseDiffBlocks 一致）。

**读侧 B**（pkg/tui/render.go stripToolMetadataSections，raw-body 回落）：
丢弃同一前缀的行（用同一导出常量）。此处不需要剥后缀：带 more-files note 的
body 仍被 diffBlockNote 识别 → 走 diff 卡路径 → 读侧 A 已处理；回落到这里的
只有 note 全被丢弃的 body。

**前端**：删除 diffNotes.ts 中不可达的 unavailable 规则/后缀逻辑/图标分支。

| # | 场景（ShellEditDiff 字段） | 旧行为 | 新行为 | 性质 |
|---|---|---|---|---|
| 1 | Files=0, MoreFiles=0, Unavailable, !Shared（owner 案例：快照失败） | `(file diff unavailable for this command)` | 无 diff 段，卡片只有命令输出 | 刻意修订（owner 裁决） |
| 2 | Files=0, MoreFiles=0, Unavailable, Shared | 同上裸 note | shared note（归一既有 empty-shared 行为） | 刻意修订 |
| 3 | Files=0, MoreFiles=N>0, Unavailable（大文件/不可读，可含 binary/mode-only 计数） | `(file diff unavailable for this command; N files changed)` | `N files changed (binary, mode only or too large to show)` | 刻意修订：留计数、去机制措辞 |
| 4 | 场景 3 + Shared | unavailable note | 计数 note + shared note | 刻意修订 |
| 5 | Files>0, MoreFiles>0, Unavailable | `… N more files changed (part of the diff is unavailable)` | `… N more files changed` | 刻意修订 |
| 6 | Files=0, MoreFiles>0, !Unavailable, Shared（全 binary/mode-only） | 仅 shared note（计数被早退吞掉） | 计数 note + shared note | 刻意修订（早退条件收紧的连带） |
| 7 | Files=0, MoreFiles>0, !Unavailable, !Shared | 计数 note | 不变 | — |
| 8 | Skipped / 单独 Shared / 单独 TreeMoved | 各自 note | 不变（owner 未要求，语义不同） | — |
| 9 | Files=0, MoreFiles>0, Shared/TreeMoved 组合（含 Unavailable 与否） | 早退吞掉计数与 treeMoved | 计数 note + treeMoved note + shared note（按标志） | 刻意修订（早退收紧连带，良性） |
| 10 | 历史 body 含 unavailable note/后缀（TUI live/replay、web stored） | 显示（web 中文翻译） | 读侧 A 过滤；TUI 静默命令 raw 回落由读侧 B 兜底 | 刻意修订 |
| 11 | hook tool_response 的 shell_edit_diff.unavailable 字段 | 存在 | 不变（数据契约） | — |
| 12 | 前端 zh 翻译 unavailable 规则 | 两条规则+后缀 | 删除（引擎不再产生、gateway 已过滤历史） | 刻意修订 |

## Scope

**In scope（只许改这些）**：
- `pkg/tool/diff_blocks.go`（写侧 + 读侧 A + 导出前缀常量）
- `pkg/tool/diff_blocks_test.go`（更新 + 新增历史过滤用例）
- `pkg/tool/format_test.go`（更新 shellEditDiffNotes 用例）
- `pkg/tool/shell_card_test.go`（新增历史 body 过滤用例）
- `pkg/tui/render.go`（**仅 stripToolMetadataSections 加一行前缀丢弃**）
- `pkg/tui/render_test.go`（新增 raw-body 过滤回归）
- `frontend/src/locales/diffNotes.ts`
- `frontend/src/components/chat/FileChangeList.test.ts`
- `docs/plan/SHELL_DIFF_UNAVAILABLE_NOTE_REMOVAL_PLAN.md`（创建，本计划副本，gitignore 不入库）

**Out of scope（看起来相关但明确不碰）**：
- `pkg/tool/shell_diff.go` —— 触发源与 `Unavailable` 字段保留（hook 契约）；
  错误被吞、750ms 超时预算等是既有设计，不在本次裁决范围。
- `pkg/tool/format.go`、`pkg/tool/shell_card.go` —— 调用链不动。
- `pkg/tui/render.go` 的其余部分、`pkg/tui/reducer.go`、`pkg/gateway/*` ——
  读侧接缝已覆盖。**并行 agent forebrain-harness-86 正在 pkg/gateway 工作，
  绝不触碰；pkg/tui 若发现并行改动先对照 drift check。**
- diffNoteSkipped / diffNoteShared / diffNoteTreeMoved 三条 note 及其翻译 ——
  语义不同，owner 未要求。
- `ShellEditDiff` 结构体字段、`DecodeShellEditDiff`、`HookToolResponse`。
- 任何 git commit / push / worktree。

## Commands you will need

所有 go 命令在仓库根目录跑，先 `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`
（直接调 go 编译 pkg/pdf 的 cgo 步骤必需；make 之外不会自动设置）。

| 用途 | 命令 | 预期 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| 定点测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestDiffBlocks\|TestFormatToolStepResult_shellEditDiffNotes\|TestParseShellCard' -count=1` | 全过 |
| TUI 定点 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestShellRawBodyDropsHistoricalUnavailableNote\|TestShellDiffCard\|TestShellCardWithoutDiffBlocks' -count=1` | 全过 |
| 包级回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui ./pkg/turn -count=1` | 全过 |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool ./pkg/tui ./pkg/turn` | exit 0 |
| gofmt | `gofmt -l pkg/tool pkg/tui` | 无输出 |
| 前端测试 | `cd frontend && pnpm test -- FileChangeList` | 全过 |
| 前端类型 | `cd frontend && pnpm typecheck` | exit 0 |

真机验收用 run-forebrain 夹具：`D=.claude/skills/run-forebrain/driver.sh`
（先读 `.claude/skills/run-forebrain/SKILL.md` 的 mode 表与陷阱表）。
driver 的 `build` 子命令同样需要上面 export 的 PKG_CONFIG_PATH。
**改了 Go 代码必须 `$D build` 重建**——start 只在二进制缺失时才构建。

## Steps

### Step 0：修复前复现（先于任何源码改动）

0a. 单元基线（钉住现状的测试必须绿）：
```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool \
  -run 'TestDiffBlocksNoteTexts|TestFormatToolStepResult_shellEditDiffNotes' -count=1 -v 2>&1 | tail -15
```
预期：PASS，`unavailable` 子测试存在且通过——证明当前契约确实渲染该 note。

0b. 真机复现（裸 note，owner 看到的那句）：
```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
D=.claude/skills/run-forebrain/driver.sh
export FOREBRAIN_RUN_WORK="${TMPDIR:-/tmp}/fb-unavail-a"
$D build && $D serve reply ok          # 建隔离 home/proj（不启 TUI）
git -C "$FOREBRAIN_RUN_WORK/proj" init -q
git -C "$FOREBRAIN_RUN_WORK/proj" add README.md
git -C "$FOREBRAIN_RUN_WORK/proj" -c user.email=t@t -c user.name=t commit -qm init
# wrapper git：私有快照 GIT_DIR 必败，其余调用透传真 git
mkdir -p "$FOREBRAIN_RUN_WORK/bin"
printf '#!/bin/sh\ncase "${GIT_DIR:-}" in *forebrain-shell-diff*) exit 1;; esac\nexec /usr/bin/git "$@"\n' > "$FOREBRAIN_RUN_WORK/bin/git"
chmod +x "$FOREBRAIN_RUN_WORK/bin/git"
# 二进制 wrapper：PATH 注入发生在 forebrain 进程内部，不依赖 tmux 环境传递
# （tmux new-session 命令串只设 FAKE_LLM_KEY/FOREBRAIN_HOME，PATH 传递不可靠）
mv "$FOREBRAIN_RUN_WORK/forebrain" "$FOREBRAIN_RUN_WORK/forebrain.real"
printf '#!/bin/sh\nexport PATH="%s/bin:$PATH"\nexec "%s/forebrain.real" "$@"\n' \
  "$FOREBRAIN_RUN_WORK" "$FOREBRAIN_RUN_WORK" > "$FOREBRAIN_RUN_WORK/forebrain"
chmod +x "$FOREBRAIN_RUN_WORK/forebrain"
$D start shell 'touch marker.txt'
$D submit run                          # start 只到 composer，必须显式提交
$D wait 'Ran touch' 25                 # 等 shell 卡出现
tmux capture-pane -t forebrain-run -p > /tmp/unavail-before-a.txt
grep -F "file diff unavailable" /tmp/unavail-before-a.txt
```
预期：**grep 命中** `(file diff unavailable for this command)`。
原理：discovery 的 env 不含 GIT_DIR（shellDiffScrubEnv 剥掉所有 GIT_*），
私有快照 env 带 `GIT_DIR=<tmp>/forebrain-shell-diff/...` → wrapper exit 1 →
beginShellEditDiff 快照失败 → 裸 note。
注意：shell 模式用全局请求计数（fake_provider.py serve_tool_script），
background 请求理论上可抢走脚本——start 后立即 submit 使主 turn 成为首个
请求。若 wait 超时：`$D screen` 与 `$D provider-log` 取证；屏幕若是审批
overlay 则 `$D key Enter` 批准后重新 wait；若脚本被 background 抢走则
`$D stop` 后重跑本场景（fresh start）。仍失败则 STOP 报告。

0c. 真机复现（带计数变体）：
```bash
$D stop
export FOREBRAIN_RUN_WORK="${TMPDIR:-/tmp}/fb-unavail-b"
$D build && $D serve reply ok          # 全新 WORK：无 wrapper，真实 git
git -C "$FOREBRAIN_RUN_WORK/proj" init -q
git -C "$FOREBRAIN_RUN_WORK/proj" add README.md
git -C "$FOREBRAIN_RUN_WORK/proj" -c user.email=t@t -c user.name=t commit -qm init
$D start shell 'touch secret && chmod 000 secret'
$D submit run
$D wait 'Ran touch' 25
tmux capture-pane -t forebrain-run -p > /tmp/unavail-before-b.txt
grep -F "file diff unavailable" /tmp/unavail-before-b.txt
```
预期：命中 `(file diff unavailable for this command; 1 file changed)`
（chmod 000 → os.Open EACCES → largeChanged，树不变 → unavailableDiff）。
此场景 PATH 不含 wrapper（独立 WORK + 直接构建的二进制），不会退化成裸 note。

### Step 1：写侧——删除 unavailable 文案（pkg/tool/diff_blocks.go）

1. `diffMoreFilesNote` 去掉第三参与后缀分支，目标形状：
```go
// diffMoreFilesNote is the note for changed files the card did not show:
// when some files are shown it counts the rest; when none are shown it says
// why (binary, mode-only or too large).
func diffMoreFilesNote(filesShown bool, n int) string {
	if filesShown {
		return fmt.Sprintf("… %d more %s changed", n, diffFilesWord(n))
	}
	return diffFileCount(n) + " changed (binary, mode only or too large to show)"
}
```
2. 删除 `diffUnavailableNote` 整个函数。
3. `shellEditDiffBlocks`：删除 `len(diff.Files) == 0 && diff.Unavailable` 分支；
   shared 早退改为 `len(diff.Files) == 0 && diff.MoreFiles == 0 && diff.Shared`；
   MoreFiles 调用改 `diffMoreFilesNote(len(blocks.Files) > 0, diff.MoreFiles)`。
   在删除分支处留注释说明裁决，例如：
```go
	// The diff machinery's own failures are not user-facing (owner 2026-11-18):
	// an unavailable diff leaves the card with the command output alone, and a
	// count of changed files too large to show keeps the existing note wording.
```
   函数头注释「with Claude Code's note set」改为如实描述（unavailable 一句已按
   owner 裁决移除，其余沿用）。
4. `ShellEditDiff.Unavailable` 字段、shell_diff.go 一律不动。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` → exit 0
（此时测试会红——Step 4 修；编译必须先绿）。

### Step 2：读侧 A——scanDiffBlocks 过滤历史 note（同文件）

1. 新增导出常量与前缀（放在 diffNote* 常量附近；TUI 读侧 B 共用）：
```go
// DiffUnavailableNotePrefix opens the note historical cards carry when the
// diff machinery could not compute a diff. The engine no longer writes it
// (owner 2026-11-18); readers drop it so old bodies read like new ones.
const DiffUnavailableNotePrefix = "(file diff unavailable"

// diffPartUnavailableSuffix is the marker historical cards carry on a
// more-files note when part of the diff was unavailable.
const diffPartUnavailableSuffix = " (part of the diff is unavailable)"
```
2. note 分支目标形状：
```go
		if diffBlockNote(trimmed) {
			flush()
			// Historical bodies may carry the unavailable note the engine no
			// longer writes: drop it, and strip the marker from the notes that
			// survive it. A dropped note is not a block.
			if strings.HasPrefix(trimmed, DiffUnavailableNotePrefix) {
				continue
			}
			blocks.Notes = append(blocks.Notes, strings.TrimSuffix(trimmed, diffPartUnavailableSuffix))
			if stopEarly {
				return blocks, true
			}
			continue
		}
```
注意：`diffBlockNote` 本身不改（仍需识别才能干净丢弃）；被丢弃行不触发
stopEarly，保证 `HasDiffBlocks` 与 `ParseDiffBlocks` 结论一致。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` → exit 0。

### Step 3：读侧 B——TUI raw-body 回落过滤（pkg/tui/render.go）

`stripToolMetadataSections`（:4039）的 switch 里加一个 case（与既有 label
丢弃并列，放在 fence/bold 判断之前）：
```go
		case strings.HasPrefix(trimmed, tool.DiffUnavailableNotePrefix):
			continue
```
注释说明：历史 shell body 的 unavailable note 不是命令输出（命令输出永远在
标签 fence 内，sections 路径不经过此函数），按 owner 裁决丢弃。
**只改这一个函数**；render.go 其余部分不动。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/tui` → exit 0。

### Step 4：更新与新增 Go 测试

1. `diff_blocks_test.go` `TestDiffBlocksNoteTexts`：
   - `diffMoreFilesNote(true,1)` → `"… 1 more file changed"`；`(true,2)` →
     `"… 2 more files changed"`；`(false,3)` →
     `"3 files changed (binary, mode only or too large to show)"`。
   - 删除两条 `diffUnavailableNote` 断言；roundtrip notes 列表删除
     `diffUnavailableNote(1)` 与 `diffMoreFilesNote(false,3,true)`，换成
     `diffMoreFilesNote(false,3)`。
2. 新增 `TestDiffBlocksHistoricalUnavailableNotesFiltered`（样板：
   TestDiffBlocksIgnoreFencedPseudoHeaders 的直接断言风格），覆盖：
   - 文件头 + ```diff fence + `(file diff unavailable for this command; 2 files changed)`
     + `… 1 more file changed (part of the diff is unavailable)` 的 body →
     ParseDiffBlocks：文件 intact，Notes 恰为 `["… 1 more file changed"]`；
     HasDiffBlocks=true。
   - 仅 `(file diff unavailable for this command)` 一行的 body →
     ParseDiffBlocks ok=false 且 HasDiffBlocks=false。
   - 仅 `3 files changed (binary, mode only or too large to show) (part of the diff is unavailable)`
     的 body → Notes 恰为去后缀那句，ok=true。
   - stdout fence 内印有 unavailable 句子的 body → 不识别为 note（沿用 fence 规则）。
3. `format_test.go` `TestFormatToolStepResult_shellEditDiffNotes`：
   - `"unavailable"` case 期望 notes 改
     `[]string{"2 files changed (binary, mode only or too large to show)"}`。
   - `"full"` case 的 `diffMoreFilesNote(true, 3, true)` 改
     `diffMoreFilesNote(true, 3)`。
   - 新增 case `{"unavailable-empty", ShellEditDiff{Unavailable: true}, ...}`：
     期望 body **无任何 diff 段**——assertBody 加一个「期望无 blocks」分支
     （断言 `ParseDiffBlocks(body)` ok=false 且 body 不含 `"(file diff"`），
     live 与 replay 两半都跑。
   - 新增 case `{"unavailable-shared", ShellEditDiff{Unavailable: true, Shared: true},
     []string{diffNoteShared}}` 与 `{"shared-count", ShellEditDiff{Shared: true, MoreFiles: 2},
     []string{"2 files changed (binary, mode only or too large to show)", diffNoteShared}}`
     （矩阵行 2、6）。
4. `shell_card_test.go` 新增历史过滤用例（样板：
   TestParseShellCardReadsBackWhatFormatShellStepWrote）：手工构造含
   `**shell**` 头、stdout fence、unavailable note 与带后缀计数 note 的 body →
   ParseShellCard 的 Notes 已过滤/去后缀，Stdout 不受影响。
5. `render_test.go` 新增 `TestShellRawBodyDropsHistoricalUnavailableNote`
   （样板：TestShellCardWithoutDiffBlocksIsUnchanged，render_test.go:1646 ——
   直接调 renderShellDiffCard/formatShellOutput 断言；历史 body 需手工构造，
   因为引擎不再产生该 note），覆盖：
   - 历史 note-only shell body（`**shell**` + input fence + unavailable note，
     **无 stdout 段**——静默命令形状），形如：
     `"**shell**\n\ninput:\n\n```json\n{\"command\":\"touch marker.txt\"}\n```\n\n(file diff unavailable for this command)\n\n"`
     → `renderShellDiffCard` ok=false；`formatShellOutput` 与
     `visibleToolOutput` 均返回 ""（渲染器将显示统一空态文案）。
   - 带 stdout fence + unavailable note 的 body → formatShellOutput 只返回
     stdout 内容（sections 路径不受影响）。
   - stdout fence 内印有 unavailable 句子 → 原样保留（命令输出不可吃）。

**Verify**：
```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool \
  -run 'TestDiffBlocks|TestFormatToolStepResult_shellEditDiffNotes|TestParseShellCard' -count=1
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui \
  -run 'TestShellRawBodyDropsHistoricalUnavailableNote|TestShellDiffCard|TestShellCardWithoutDiffBlocks' -count=1
```
→ 全绿（含新增用例）。

### Step 5：前端——删除不可达的翻译规则

1. `frontend/src/locales/diffNotes.ts`：
   - 删 `PART_UNAVAILABLE` 常量；`translateDiffNote` 简化为纯查表
     （locale!=='zh' 原样返回；zh 逐条 match NOTE_RULES；未知 note 原样返回），
     注释同步更新（不再提 unavailable marker）。
   - 删 NOTE_RULES 里两条 unavailable 规则。
   - `diffNoteIcon` 删 `note.startsWith('(file diff unavailable') ||`，保留
     `/binary, mode only/` → FileX。
2. `frontend/src/components/chat/FileChangeList.test.ts`：
   - 中文用例：输入删 `'(file diff unavailable for this command; 2 files changed)'`；
     `'3 files changed ... (part of the diff is unavailable)'` 改无后缀版，期望
     `'3 个文件改动（二进制、仅权限或太大，不显示）'`；期望数组同步删对应项。
   - 英文用例：`'… 7 more files changed (part of the diff is unavailable)'` 改
     `'… 7 more files changed'`，期望原样。

**Verify**：`cd frontend && pnpm test -- FileChangeList && pnpm typecheck` → 全绿。
（若 frontend 依赖未装：先 `pnpm install`。）

### Step 6：全量验证

```bash
gofmt -l pkg/tool pkg/tui                             # 无输出
CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool ./pkg/tui ./pkg/turn   # exit 0
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/tui ./pkg/turn -count=1  # 全绿
grep -rn "file diff unavailable\|part of the diff is unavailable\|diffUnavailableNote" pkg frontend/src --include="*.go" --include="*.ts" --include="*.vue"
```
最后一条 grep 预期：只剩 scanDiffBlocks/stripToolMetadataSections 过滤用的
前缀/后缀常量与逻辑、测试里的历史 body 构造（都是「过滤历史」语义），
**没有任何产生该文案的代码**。
若 pkg/tui 或 pkg/turn 出现与本改动无关的失败：先确认是否并行 agent 的在改
文件（forebrain-harness-86 在 pkg/gateway），不要修别人的文件，报告。

### Step 7：修复后真机验收（tmux，四场景）

重建：`$D build`（Step 0 的 export 仍在）。**注意场景 A 的 WORK 里二进制已被
wrapper 替换——先恢复：`mv $FOREBRAIN_RUN_WORK/forebrain.real $FOREBRAIN_RUN_WORK/forebrain`
再 `$D build`，或直接 `$D build` 覆盖 wrapper（build 输出同名文件）。**

- **A 复现→修复（裸 note）**：
  ```bash
  export FOREBRAIN_RUN_WORK="${TMPDIR:-/tmp}/fb-unavail-a"
  $D stop; $D build                    # 覆盖 wrapper，修复后二进制
  # git wrapper 仍在 $WORK/bin，但修复后二进制不再注入 PATH → 真实 git。
  # 为继续强制快照失败，重新包一层二进制 wrapper（同 Step 0b 的 mv+printf）。
  $D start shell 'touch marker.txt'
  $D submit run; $D wait 'Ran touch' 25
  tmux capture-pane -t forebrain-run -p > /tmp/unavail-after-a.txt
  grep -F "file diff unavailable" /tmp/unavail-after-a.txt   # 预期无命中
  ```
  卡片只有 `● Ran touch marker.txt` 与统一空态文案（touch 无输出）。
  **本场景同时实证读侧 B**：修复前该 body 只有 note，过滤后回落 raw 渲染器，
  若 stripToolMetadataSections 没修，这里会泄漏。
- **B 计数变体**：
  ```bash
  $D stop; export FOREBRAIN_RUN_WORK="${TMPDIR:-/tmp}/fb-unavail-b"
  $D build
  $D start shell 'touch secret && chmod 000 secret'
  $D submit run; $D wait 'Ran touch' 25
  tmux capture-pane -t forebrain-run -p > /tmp/unavail-after-b.txt
  grep -F "file diff unavailable" /tmp/unavail-after-b.txt   # 预期无命中
  grep -F "1 file changed (binary, mode only or too large to show)" /tmp/unavail-after-b.txt  # 预期命中
  ```
  注意：fb-unavail-b 的 proj 里已有 0c 留下的 secret（mode 000）——重跑命令
  会更新其 mtime，largeChanged 仍触发；若 want 干净状态可先删 proj 重建
  （serve 会重写 README，git init/commit 重做）。
- **C 回归（正常 diff 不受影响）**：
  ```bash
  $D stop; export FOREBRAIN_RUN_WORK="${TMPDIR:-/tmp}/fb-unavail-c"
  $D build && $D serve reply ok
  git -C "$FOREBRAIN_RUN_WORK/proj" init -q
  git -C "$FOREBRAIN_RUN_WORK/proj" add README.md
  git -C "$FOREBRAIN_RUN_WORK/proj" -c user.email=t@t -c user.name=t commit -qm init
  $D start shell 'echo hi >> notes.txt'
  $D submit run; $D wait 'Ran echo' 25
  tmux capture-pane -t forebrain-run -p | grep -F 'Created notes.txt'
  ```
  预期命中（TUI diff 卡 header 是 `Created notes.txt (+1 -0)`，**无反引号**，
  反引号只在 markdown body 里）。
- **D 历史 replay**：用场景 A 修复前留在 `$TMPDIR/fb-unavail-a/home` 的会话
  （Step 0b 产生；若已清理则先用修复前二进制重跑一次 0b 再 rebuild）：
  ```bash
  export FOREBRAIN_RUN_WORK="${TMPDIR:-/tmp}/fb-unavail-a"
  $D stop; $D build                    # 确保是修复后的真实二进制（无 wrapper）
  $D title                             # SELECT id, title FROM fb_sessions —— 取 0b 的 session id
  RESUME_ID=<id> $D start reply ok
  sleep 3                              # replay 渲染
  tmux capture-pane -t forebrain-run -p > /tmp/unavail-after-d.txt
  grep -F "file diff unavailable" /tmp/unavail-after-d.txt   # 预期无命中
  ```
  web 侧同一接缝由 ParseShellCard 新用例覆盖（gateway 正是调它），不另起 web e2e。
- 收尾：`$D stop`；删除三个 fb-unavail-* 临时目录；不得污染真实 ~/.forebrain。

### Step 8：收口

- 本计划复制为 `docs/plan/SHELL_DIFF_UNAVAILABLE_NOTE_REMOVAL_PLAN.md`，
  状态行改「已实施并真机验收通过（日期）」，新增「验收记录」章节
  （场景→证据文件路径，含踩过的工具链坑）。
- `git status --porcelain` 核对改动范围与 Scope 一致；**不 commit**。
- 报告：根因链、最小改动清单、验证证据、遗留限制。

## Test plan

- 新增：TestDiffBlocksHistoricalUnavailableNotesFiltered（4 例）、
  format 表新增 3 case（unavailable-empty / unavailable-shared / shared-count）、
  ParseShellCard 历史过滤 1 例、TestShellRawBodyDropsHistoricalUnavailableNote
  （3 例）、前端翻译用例更新。
- 更新：TestDiffBlocksNoteTexts、TestFormatToolStepResult_shellEditDiffNotes
  既有断言随契约修订（理由：owner 裁决删除该文案）。
- 不动：TestShellEditDiffLargeFilesAreUnavailable（字段级契约保留）。
- 结构样板：TestDiffBlocksIgnoreFencedPseudoHeaders、
  TestParseShellCardReadsBackWhatFormatShellStepWrote、render_test.go 既有 shell 测试。

## Done criteria（全部机器可查）

- [ ] Step 0 复现证据存档（before-a/b 含 unavailable 字样）。
- [ ] `grep -rn "diffUnavailableNote" pkg` 无命中。
- [ ] `grep -rn "file diff unavailable" pkg frontend/src --include="*.go" --include="*.ts" --include="*.vue"`
      命中仅限过滤常量/逻辑与测试的历史 body 构造。
- [ ] `grep -n "DiffUnavailableNotePrefix" pkg/tui/render.go` 命中（读侧 B 存在）。
- [ ] 定点测试、pkg/tool+pkg/tui+pkg/turn 回归、go vet、gofmt、前端 test+typecheck 全绿。
- [ ] 真机 A/B/C/D 四场景证据存档（after-a/b/d 无 unavailable；B 有计数 note；
      C 有 `Created notes.txt` diff 块）。
- [ ] `git status --porcelain` 改动 ⊆ Scope 清单；无 commit。
- [ ] docs/plan 副本已回填验收记录。

## STOP conditions

- Drift check 内容不匹配（in-scope 文件被并行会话改过且摘录对不上）。
- Step 0 真机复现 grep 不命中（wrapper 没生效或触发链与计划不符）——报告屏幕
  与 tui.err/provider-log，不要改用别的触发方式硬凑。
- 任何验证连续两次失败且原因不在本计划改动内。
- 需要触碰 out-of-scope 文件（尤其 pkg/gateway、shell_diff.go 的字段/触发逻辑、
  render.go 的其余函数）。
- 发现「三个 surface 共用 scanDiffBlocks + raw 回落只有 stripToolMetadataSections
  一条」这一承重假设为假（例如某 surface 绕过两者直接渲染 note）——停下报告。
- 前端出现 Go 侧过滤之外的 note 来源（shellCardFromPayload 之外还有路径喂
  unavailable note 给 FileChangeList）。

## Maintenance notes

- 过滤有两处，职责不同：scanDiffBlocks（结构化解析，三 surface 共用）与
  stripToolMetadataSections（TUI raw-body 回落，静默命令历史 body）。未来加新
  note 时写侧 shellEditDiffBlocks 与读侧 diffBlockNote 必须同步；「机制失败不
  面向用户」是 owner 裁决（2026-11-18），不要再引入「diff 计算失败」类文案。
  Skipped/Shared/TreeMoved 三条保留 note 的措辞仍源自 Claude Code bashEditDiff
  （plan 130 的 verbatim 规则对这三条继续有效）。
- `ShellEditDiff.Unavailable` 字段仍是 hook tool_response 的一部分；若未来改
  hook 契约，记得它还有「大文件变化计数」语义（MoreFiles>0 时渲染计数 note）。
- 读侧过滤是历史兼容层：若将来确认所有存量会话都已过期/清理，可考虑移除
  两处过滤与前端残留——但在那之前它们是三 surface 一致性的保证。
- 评审重点：shared 早退条件收紧（矩阵行 6/9）是连带行为修订；stopEarly 一致性
  （HasDiffBlocks vs ParseDiffBlocks）；fence 内伪 note 与命令输出不被误过滤。
- 明确遗留（不在本次裁决范围，仅记录）：快照错误被吞（无法诊断为何 unavailable）、
  750ms 后续快照超时预算在大树上偏紧——两者只影响内部 diff 质量，不再产生
  用户可见噪音。

## Implementation Status

- **Verdict**: APPROVE（reviewer 独立重跑全部判据，2026-11-18）
- **Worktree**: `.worktrees/remove-shell-diff-unavailable-note`，分支
  `remove-shell-diff-unavailable-note`（基线 `2c38938f`）。合并是 owner 的决定，
  worktree/分支保留待裁决。
- **Executor commits**: `b06168a4` fix(tool): drop user-facing shell diff
  unavailable note（Go）；`a812b0b2` fix(web): drop the unreachable unavailable
  diff note rules（前端）。执行模型 opencode-go/deepseek-v4.1-flash。
- **Reviewer 亲自重跑的验证（全部在 worktree 内）**:
  - `gofmt -l pkg/tool pkg/tui` → 无输出 ✓
  - `grep -rn diffUnavailableNote pkg` → 无命中 ✓
  - 文案 grep（非测试代码）→ 仅 diff_blocks.go:47/51 两个过滤常量 ✓
  - `grep -n DiffUnavailableNotePrefix pkg/tui/render.go` → :4092 ✓
  - `go vet -tags fts5 ./pkg/tool ./pkg/tui ./pkg/turn` → exit 0 ✓
  - `go test -tags fts5 ./pkg/tool ./pkg/tui ./pkg/turn -count=1` →
    ok 21.3s / 199.5s / 4.6s ✓
  - `frontend pnpm test` → 192 files / 1513 tests passed ✓；`pnpm typecheck` ✓
  - 六份真机证据逐份核对：before-a/b 各含 1 处 unavailable 且命令对应正确
    场景；after-a/b/d 零命中；after-b 含既有措辞计数 note；after-c 含
    `Created notes.txt (+1 -0)` 与 `1 + hi` hunk ✓
  - 范围：`git diff --stat 2c38938f..HEAD` 恰好 8 个 in-scope 文件，
    `git status --porcelain` 干净 ✓
  - 完整 diff 与测试断言逐行读过：写侧/读侧 A/读侧 B/前端与计划一致；
    新测试断言实质（含 stopEarly 一致性、fence 伪 note、命令输出不可吃、
    raw-body 回落空态）✓
- **执行者记录的偏差（均经 reviewer 判定为合理、最小、服务于计划意图）**:
  1. fetch-pdfium.sh 遭 GitHub API 限流 → 从主树只读复制已构建的
     third_party/pdfium/darwin_arm64 并改写 pdfium.pc 前缀（gitignore 目录，
     不影响 diff）。
  2. run-forebrain driver 生成的旧式 yaml 不再满足 plan 315 后的启动门槛
     （NeedsFirstSetup → EffectiveDefaultLLMFor 模型视图）→ 仅在 scratch home
     播种 fb_llm_models 行 + default_model 绕过，未改任何源码。**夹具本身待修，
     已上报 owner 裁决。**
  3. driver serve 不检测端口占用 → 显式杀旧 provider；场景 A 证据曾受串台
     影响，已重新取证（reviewer 核对 before-a 命令为 touch marker.txt，真实）。
  4. 场景 B 需全新 proj：untracked cache 按目录 mtime 失效，重触已存在的
     mode-000 文件不被 status 重新列出（与 snapshot:581-583 语义一致）。
  5. `go build -o` 拒绝覆盖 shell wrapper → 先删再建（计划 Step 7 配方瑕疵，
     执行者适配正确）；期间一次 forebrain.real 误覆盖在运行前被发现并重建，
     after-* 证据呈现的新行为本身证明验收用的是修复后二进制。
  6. render.go 新 case 位于 bold case 之后（计划建议之前）——前缀互斥，
     无行为差异。
- **未做项**：docs/plan 副本与本状态回填由 reviewer 完成（执行者按指令跳过）；
  web e2e 未另起（ParseShellCard 用例覆盖 gateway 所用接缝，计划已声明）。
- **相邻缺陷裁决**: owner 2026-11-18「相邻缺陷必须本期一并修复」——验收夹具
  家族已全量修复并验证，见下节。

## 相邻缺陷修复：验收夹具家族（2026-11-18，owner 裁决本期一并修复）

### 缺陷类与根因（全部真机复现/源码核实）

1. **模型配置契约漂移**（主缺陷）：plan 262/315 后启动门槛判定的模型视图唯一
   来源是状态库 `fb_llm_models` 表（`ConfiguredLLMs` 只读 root.LLMModels，
   pkg/config/llm_models.go:80-96；`EffectiveDefaultLLMFor` 无 legacy 列表
   回退，:119-140；composed 路径从 LLMModelStore.List 投影，
   pkg/process/config_db.go:184-193）。夹具写的 yaml `llm_providers` 列表
   不再配置任何东西 → fresh home 落 onboarding wizard（真机复现：Step 1/6，
   driver 报 "TUI never reached the composer"）。
2. **LISTENING 先于 bind**：fake_provider.py（run-forebrain 与 acceptance
   两份）与 flaky_proxy.py 在 `Server()` 构造（bind）之前打印 LISTENING →
   端口被占时 driver 的 grep 仍命中 → 旧 provider 静默应答新场景（执行者
   实证串台；修复后三次实证 Errno 48 + 响亮 die）。
3. **信任页文案漂移 + 早退**：产品文案是 "Trust this folder?"
   （pkg/tui/workspace_trust.go:23），6 个脚本仍匹配过时的 "Trust this
   directory?"；且 "/ commands" 横幅先于信任问题绘制，无守卫的 break 会
   提前返回，后续按键落在信任页（opens on Quit）→ TUI 退出。
   scripts/acceptance/driver.sh 完全缺失信任处理。
4. **memories_search 单发 toolcall 形状**：FAKE_TOOL_NAME 单发以 JSON 体
   应答流式期望 → 重试阶梯耗时约 30s 超出脚本等待窗（multi-replica 注释
   早已记录此坑）→ 改流式多发 FAKE_TOOL_SCRIPT。

### 修复设计

- **新增 `.claude/skills/run-forebrain/model_seed.sh`**（单一实现，10 处调用）：
  key 走 stdin、SQL 走 sqlite3 stdin（真实凭证不上命令行，native_advisor
  凭证纪律）；home 无库时跑一次无头 `gateway start` 建库+导入，**等待导入
  标记 `meta.legacy_yaml_imported` 落库**才杀进程（验证中发现并修复的竞态：
  只等表存在会抢在导入前杀进程，fb_config 为空 → composed 路径不激活 →
  门槛仍判未配置）；按固定 id upsert（re-serve 幂等、换端口不留旧行）；
  可选 id/created_at_ms/params_json 参数支持 web_e2e 多行有序与
  native_advisor reasoning effort。
- **multi-replica-test.sh**：共享外置 DB（MySQL/PG）不适用 sqlite 助手 →
  两副本 healthy 后经 replica a 的 `POST /api/llm-models`（Bearer token）
  seeding，产品自己的写入路径，引擎无关。
- 三个 python server：LISTENING 移到 bind 成功之后。
- 信任循环：6 处文案归一 "Trust this folder?" + quiet 计数守卫（信任已答
  或 15 帧静默才接受 "/ commands"）；acceptance driver 移植 run-forebrain
  完整循环（含 EXIT= 守卫与信任未答拒发按键）。
- memories_search：FAKE_TOOL_SCRIPT 流式多发。
- SKILL.md 陷阱条目改写（模型在状态库不在 yaml；旧 ${ENV} 规则失效）。

### 改动文件（16，全部夹具，零产品代码，留工作区未 commit）

run-forebrain: model_seed.sh（新）、driver.sh、mcp_acc.sh、p8.sh、
fake_provider.py、SKILL.md；
scripts/acceptance: driver.sh、fake_provider.py、flaky_proxy.py、
memories_search.sh、read_image.sh、read_pdf.sh、live_subagent.sh、
native_advisor_workflows.sh、web_e2e.sh；scripts/multi-replica-test.sh。

### 验证矩阵（reviewer 真机重跑）

| 场景 | 结果 |
|---|---|
| run-forebrain driver fresh home（stdin 助手+marker 等待）→ composer + reply turn | ✓ |
| gemini 模式（provider 切换 upsert + turn） | ✓ |
| re-serve 幂等（模型行数=1） | ✓ |
| 端口占用 → 响亮 die（Errno 48，无串台） | ✓ 三次实证 |
| acceptance driver（trust 移植）→ composer + turn | ✓ |
| memories_search 全流程（fresh home 词典播种 + 中文查询 tool turn） | ✓ PASS |
| mcp_acc（trust 修复）→ composer + turn | ✓ |
| p8（trust 文案修复，dummy key 只验启动不发请求） | ✓ composer、无信任页、进程存活 |
| web_e2e 配置路径手动复现：gateway + 三行有序 seeding → REST 列表顺序正确、default=e2e-primary | ✓ |
| POST /api/llm-models 创建行（multi-replica 机制） | ✓ |
| params_json 回读 {"reasoning":{"effort":"low"}}（native_advisor 形状） | ✓ |
| 全脚本 bash -n / py_compile | ✓ |

### 凭证/容器门控（修复已落地，完整运行需 owner 资源，明示非静默遗留）

- read_image / read_pdf 全流程：需 FOREBRAIN_E2E_DEEPSEEK_KEY（真实
  DeepSeek，网络费用）。seeding+trust 修复已落地，机制由上表覆盖。
- live_subagent / native_advisor --real / web_e2e real-LLM 模式：需
  FOREBRAIN_E2E_ZHIPU_KEY。native_advisor --fake 复用 acceptance driver
  已验证；params 形状已验证。
- web_e2e 完整浏览器套件：需 playwright/UI 构建；配置路径已手动复现验证。
- multi-replica 完整运行：需 scripts/test-db.sh 容器；REST seeding 机制已验证。

## 验收记录（2026-11-18）

| 场景 | 证据 | 结果 |
|---|---|---|
| 0a 单元基线（修复前） | TestFormatToolStepResult_shellEditDiffNotes/unavailable 绿 | 现状契约确实渲染 note |
| 0b 裸 note 真机复现 | /tmp/unavail-before-a.txt：`Ran touch marker.txt` + `└ (file diff unavailable for this command)` | 复现成立（wrapper git 强制私有快照失败） |
| 0c 计数变体复现 | /tmp/unavail-before-b.txt：`└ (file diff unavailable for this command; 1 file changed)` | 复现成立（chmod 000 → largeChanged） |
| A 修复后裸 note | /tmp/unavail-after-a.txt：`└ (no output)`，零 unavailable | 通过（同时实证读侧 B raw 回落过滤） |
| B 修复后计数变体 | /tmp/unavail-after-b.txt：`└ 1 file changed (binary, mode only or too large to show)` | 通过（计数保留、机制措辞消失） |
| C 正常 diff 回归 | /tmp/unavail-after-c.txt：`Created notes.txt (+1 -0)` + `1 + hi` | 通过 |
| D 历史 replay | /tmp/unavail-after-d.txt：resume 修复前会话（落盘 body 仍含 note），`└ (no output)` | 通过（读侧 A 过滤历史） |
| Go 回归 | reviewer 重跑 pkg/tool 21.3s / pkg/tui 199.5s / pkg/turn 4.6s 全 ok | 通过 |
| 前端 | reviewer 重跑 192 files / 1513 tests passed + typecheck | 通过 |
| vet/gofmt/grep 判据 | reviewer 重跑全过 | 通过 |

工具链坑（踩过的）：
- fetch-pdfium.sh GitHub API 限流（60/h/IP）→ 从主树只读复制 third_party 并
  改写 pdfium.pc 前缀。
- run-forebrain driver 的旧式 yaml 不满足 plan 315 启动门槛 → scratch home
  播种 fb_llm_models + default_model（夹具待修，已上报）。
- driver serve 不检测端口占用，旧 provider 会串台应答 → 换 WORK 时显式杀旧
  provider。
- untracked cache 按目录 mtime 失效：重触已存在文件不产生 largeChanged，
  计数场景需全新 proj。
- `go build -o` 不覆盖 shell wrapper：先删二进制再 build。