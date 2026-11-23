# TUI 待办进度入口与非覆盖式右侧抽屉实施计划

> **执行者说明**：本计划面向没有本次对话上下文的执行者。按步骤执行，每一步先完成验证再进入下一步。遇到“STOP 条件”立即停止并报告，不要自行扩大范围。当前只规划 `pkg/tui`，不涉及 Web、gateway、持久化结构或 `session_todo` 工具协议。
>
> **Drift check（先跑）**：
>
> ```bash
> git rev-parse --short HEAD          # 期望 0e6e3827（dev）
> git status --short                  # 期望无输出：基线是干净的已提交工作树
> git hash-object pkg/tui/input_events.go pkg/tui/input_events_test.go pkg/tui/chat_session_test.go
> ```
>
> 计划基线是 `dev @ 0e6e3827`（2026-10-29 逐条复核后重锚）。上面三条命令必须依次输出 `0e6e3827`、空、以及 `c0491e4e51fb6d970eb69f06e38ff171b1d64c92`、`f17605b86e5d3821594affcf8e0b117a824ff93c`、`f1cece5b405aea84f08e7cea1ed750bba2eda3dd`。任一不符，或 Scope 内出现未提交修改，STOP 并刷新计划；不得覆盖、回退或 stash 任何文件。
>
> 上一版把这三处并行修改记成**未提交**的脏树并要求"等 owner 交接"：那些修改现在都已提交、工作树干净，**交接条件已满足，不要再等**。上一版记录的提交号 `cd275645` 在本仓库并不存在（refs 和 reflog 都没有），不要拿它跑 `git diff`/`git show`，更不要为让它成立去 rebase、切分支或重建提交。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: HIGH（共享鼠标输入协议 + viewport  painter + 点击/选择命中测试）
- **Depends on**: 无 —— 三处并行修改已提交，drift check 记录的内容哈希即交接点
- **Category**: direction / feature
- **Planned at**: commit `0e6e3827`（dev，干净工作树），2026-10-29

## Why this matters

`Working` 行的 `☑N/M` 已展示完成数/总数，但目前只是文本，用户无法通过它查看完整待办。目标是用鼠标打开一个右侧待办抽屉，并让历史消息区域真正变窄重排，而不是被抽屉遮住。交互必须覆盖点击外部、关闭图标、Esc、再次点击进度入口，且点击、滚动、流式更新和终端缩放下都稳定，不误触历史卡、输入框或运行取消。

## UI / UX 合同

### 1. 入口与反馈

- 入口仅为实际绘制出来的完整 `☑N/M` token，不包含后面的 active task 文案。
- 静止时保持当前 faint 样式；**不要下划线、不要常驻新底色、不要其他文字装饰**。
- hover 时只在 `☑N/M` 的显示单元范围内使用现有 `48;5;236` 背景高亮；关闭 `×` 使用相同规则。
- 点击后打开抽屉；再次点击该 token 关闭。窄屏截断后若完整 token 不可见，就不注册热区。

### 2. 非覆盖式分栏

抽屉只占历史消息 body 的右侧；底部 composer、footer、输入 caret 和草稿保持原宽度与布局。

```text
关闭时（历史消息全宽）
┌──────────────────────────────────────────────────────────────┐
│ 历史消息                                                      │
│                                                              │
│ \ Working (...) · ☑3/7 · Step 3 ...                          │
│ › 输入框                                                      │
└──────────────────────────────────────────────────────────────┘

打开时（历史消息缩窄并 reflow；composer 不变）
┌───────────────────────────────────┬──────────────────────────┐
│ 历史消息按剩余宽度重新换行         │ ● Updated Plan         × │
│ 内容仍然完整可见，不被右侧覆盖     │                          │
│                                  │ ▣ Step 3 修改 ...        │
│                                  │ ✓ Step 0 归档计划 ...    │
│                                  │ □ Step 5 定点测试 ...    │
│                                  │ 2 above · 4 below        │
│                                  │                          │
│ \ Working (...) · ☑3/7 · ...     │                          │
│ › 输入框（仍横跨整个终端）         │                          │
└───────────────────────────────────┴──────────────────────────┘
```

- 分隔符是左侧一列 muted `│`；正文两列 padding，不再嵌套卡片或增加背景遮罩。
- 抽屉宽度：终端宽 `W` 时先取 `clamp(W/3, 32, 48)`，再 cap 到 `W/2`。120 列时抽屉 40 列，80 列时 32 列，窄终端最多占一半。
- 宽度计算必须保留 `FrameUser` 的 full-bleed 规则：普通历史内容宽为 `W - viewportRightPadding - D`，历史实际行宽再加回 `viewportRightPadding`，让用户卡正好画到 divider，而不是被裁掉一列。
- 打开、关闭、resize 都通过现有 block width cache 重新布局；不做逐列滑动动画。一次操作产生一帧稳定结果，避免连续 reflow 和闪烁。
- 正在 follow bottom 时保持跟随最新消息；用户手动向上阅读时，重排后保留原来位于视口顶部的 block 及块内相对行，不能只保留会随换行漂移的数字 offset（`viewportRender.spans` 的 `lineSpan{blockID,startRow,rowCount}` 就是为此存在的锚，具体算法见 Step 3）。
- `Jump to bottom` 在抽屉打开时只在左侧历史区域内居中，不得落入抽屉。

### 3. 内容与滚动

- 标题复用当前视图最新 `FramePlan.Title`（默认 `Updated Plan`）和现有 plan accent `141`；标题栏固定，右侧放 `×`。
- 抽屉内部的列预算钉死，避免每个执行者自己分：`D` 列 = 0 号列 divider + 1 号列左 padding + `2..D-2` 号列正文（共 `D-3` 列）+ `D-1` 号列右 padding。`×` 占标题行正文的最右 3 列，热区就是这 3 列 = 半开区间 `[D-4, D-1)`（`D=40` 时是 36、37、38 号列）。
- 列表复用 `FramePlan.Content`，它已经由 `planText` 生成 `✓ / ▣ / □ / ✕` 和原始事项顺序；不复制 Tracker、不新增第二份 todo 数据。
- **`Content` 的第一行可能不是事项**：`planText` 先写 `Explanation` 再写事项（`render.go:6242-6267`），而 `emitPlanUpdateStep` 在恰好一条事项 active 时把 `Explanation` 设成该事项的标签（`pkg/tool/session_tools.go:288-295`）。抽屉**不显示**这行无状态符号的行（它与 `▣` 事项正文逐字重复），也不要把它当事项计数；唯一例外是整段内容为 `(empty plan)` 时按占位正文原样显示。
- 长事项按显示宽度软换行，continuation 与事项正文悬挂对齐；不得省略号截断事项正文。
- 标题固定，正文滚动。首次打开时将 `▣` 项带入视口；没有 active 项时从顶部开始。
- 用户滚轮滚动后，后续 plan update 只 clamp 当前 offset，不得把用户拉回 active 项。只有尚未手动滚动的跟随模式才持续保持 active 项可见。
- 溢出时用 muted 的 `N above` / `N below` 提示仍有内容；不写虚假的键盘滚动提示。
- **计量单位一律是显示行**（软换行之后的行），不是事项数：`N above` / `N below` 数的是行，滚轮一次 `wheelScrollLines`（=3，`input_events.go:797`）滚 3 个显示行，`buildPlanDrawerLayout` 的 `scroll` 也是显示行偏移。
- `session_todo clear` 后若抽屉仍开着，显示最新 `(empty plan)`，不能继续显示旧事项。

### 4. 关闭与事件优先级

支持四种关闭路径：

1. 点击抽屉外；
2. 点击标题栏 `×`；
3. 按 Esc；
4. 再次点击 `☑N/M`。

事件规则：

- 抽屉内的普通点击不关闭、不穿透，也不能选中/复制被遮住的底层坐标内容。
- 外部 plain click 只关闭并消费本次手势；不得同时折叠历史卡、打开子代理、移动 composer caret 或触发 `Jump to bottom`。下一次点击才作用于新屏幕。
- 从左侧历史开始的文本选择仍可选择真实可见文本；拖入抽屉边界时 clamp 到历史区域，不把抽屉单元映射成历史文本。
- 抽屉内滚轮优先滚动列表；不能滚动时再沿现有 nested-scroll 规则处理。
- Esc 先由更高优先级的斜杠/@菜单、roster、底部 panel、审批/问题 modal 消费；没有这些层时才关闭抽屉。关闭抽屉绝不能取消正在运行的 turn。
- 会话 reset、主/子代理视图切换时关闭抽屉；run 正常结束不强制关闭，让用户仍能阅读当前清单。

## Current state（执行前必须亲自复核）

### 待办数据已经汇入当前视图，无需新存储

`pkg/tui/reducer.go:1506-1527` 的 `PlanUpdatedMsg` 同时更新 Tracker 计数并生成当前 agent 的 `FramePlan`：

```go
frame := Frame{Kind: FramePlan, Title: planTitle(m.Payload), Content: planText(m.Payload), RunID: r.activeRunID, Final: true, AgentID: agentID}
return EventResult{Frames: r.withFlushedBuffers(r.agentBuf(agentID), frame)}
```

`pkg/tui/render.go:6242-6265` 的 `planText` 已生成完整文本和 `(empty plan)`；`statusGlyph` 在 `6268-6278` 固定四种状态符号。`activeVMLocked()`（`reducer.go:5547-5557`）返回主视图或当前子代理 VM，因此从 `activeVMLocked().blocks` 逆序找最新 `FramePlan` 即可与屏幕视图一致。

### 进度入口目前只是字符串

`pkg/tui/run.go:4468-4481`：

```go
if c.PlanTotal <= 0 { return "" }
progress := "☑" + strconv.Itoa(c.PlanDone) + "/" + strconv.Itoa(c.PlanTotal)
if active := strings.TrimSpace(c.PlanActive); active != "" {
    progress += " · " + active
}
```

`renderComposerStatusLine`（`render.go:4186-4195`）会 fold + truncate。热区必须从**最终绘制行**计算，不能直接从未截断的 `statusText` 推算。

### 这个 token 有两个绘制面，本计划只碰其中一个

`☑N/M` 由 `formatWorkingCounters`（`run.go:4468`）产出，被两处使用：

- **运行中的 Working 行（本计划的入口）**：`workingStatusFormatter`（`run.go:4377`）在 `4416` 把它拼进 `title`；该 `statusText` 由 `renderComposerStatusLine`（`render.go:4186`）绘制，三处 composer 构建都经过它 —— `buildReadOnlyComposerBlock`（`reducer.go:3624`，定义在 `3612`）、`buildNormalComposerBlock`（`3740`，定义在 `3718`）、`buildSlashComposerBlock`（`3816`，定义在 `3799`）。
- **run 结束后的历史状态帧（不是入口）**：`formatWorkedStatusTitle`（`reducer.go:1836`，在 `1846` 拼接）与 `subagentWorkedFrames`（`reducer.go:2167`）把同一个 token 写进 `Worked for … · ☑N/M` 的 `FrameStatus`。那是转录行，命中测试属于 fold / 文本选择体系。

只给第一处注册热区。给第二处加会改掉历史行的 fold 命中语义，本期明确不做；执行者若发现"用户也想点 Worked 行"，回到评审，不要顺手扩。

### 入口在 run 结束后不存在（本期接受的限制）

`workingStatusFormatter` 只在 run 进行中返回非空状态行（子代理视图在 `snap.Ended` 时直接返回 `""`），所以 turn 一结束，屏幕上的 `☑N/M` 就消失：已打开的 drawer 仍可读，但关掉后要等下一次 run 才能重新打开，即使 `FramePlan` 仍在历史里。这是有意接受的取舍（同一次 run 里入口就在手边，历史里的 `☑N/M` 是静态行）。

**不要**为此新增第二个入口（给历史 plan 卡或 `Worked for` 行加热区）。真觉得不可接受，回到计划评审。

### 历史宽度与 full-bleed 规则

`renderViewport`（`reducer.go:2448-2475`）对普通 block 使用传入 `width`，但对 `FrameUser` 使用 `width + viewportRightPadding`。`paintViewportLocked`（`reducer.go:4437-4455`）当前用 `contentWidth := width - viewportRightPadding`，并在 `4644` 对整个历史调用一次 `renderViewport(activeVM, contentWidth, ...)`。

### 分栏拼接的精确接缝

`paintViewportLocked` 先把每一行的字节组好，再按差异写屏（`reducer.go:4682-4719`）：

```go
rows := make([]string, bodyHeight+composerHeight)        // 4682
for row := 0; row < bodyHeight; row++ {
    line := canvasRow(vr.lines[row], width)              // 4687：拼接点
    …
    } else if r.vpHoverRow == row && vr.isClickableRow(row, activeVM) {
        rows[row] = applyHoverHighlight(line, width, hoverBgSeq)      // 4713
    } else {
        rows[row] = line
    }
}
```

抽屉打开时改的是这两处的宽度参数，别处不要动：

- `renderViewport` 用 `leftWidth - viewportRightPadding`（`viewportRightPadding = 2`，`render.go:70`，`reducer.go:5792` 另有一份同名常量）；
- `canvasRow` 用 `leftWidth`（它经 `fitRow` 把行补齐到给定宽度，`reducer.go:4300`），divider 与抽屉单元接在这行之后，整行必须**正好**等于终端宽 `W`；
- `applyHoverHighlight` 的 `width` 同样换成 `leftWidth`，否则历史 hover 底色会刷进抽屉区域；
- `jumpToBottomButtonLayout(width, ...)`（`reducer.go:6648`）及其追加到行末的 CUP 字节都按 `leftWidth` 计算（UX 合同已要求）。

`r.vpPainted` 按整行字节比较，所以开关抽屉会让**每一行**都变化——这是预期的一次性全量重绘，不要为此新增整屏 `\x1b[2J`（会闪）。

### 鼠标按 press 时的屏幕快照命中

`shownScreen`（`reducer.go:6380-6390`）保存 body/composer/overlay/jump 信息；`screenSpan` 是半开区间。`viewportRelease`（`6988-7032`）在 plain click 时使用 `press.screen / press.col / press.row`，随后才处理 composer caret、overlay 和 transcript。新控制逻辑必须在底层动作之前执行，并同样进入 `ViewportClickToggle` 路径，避免测试/备用路径与真实 press-release 路径不一致。

`viewportPressLocked`（`6743-6804`）默认会启动选择；抽屉和入口控件必须像 jump button 一样拦截 press，避免拖拽误选。`onJumpButton` 的 release 分支早于普通 click，因此抽屉打开时也必须服从“外部点击只关闭”的优先级。

### hover 目前被输入协议明确禁用

`pkg/tui/input_events.go:694-697` 的鼠标常量块当前启用 `?1002h`、禁用 `?1003h`（`enableMouseSeq = "\x1b[?1003l\x1b[?1002h\x1b[?1006h"`，`695`）；`mouseEventFromSGR`（`793`）在 `788-812` 的注释和分支里明确丢弃 `Cb=35`（无按键移动）。`672-681` 同一段里的常量是括号粘贴与焦点上报，不是鼠标协议，不要改。`chat_session_test.go:24302-24334` 还用 `TestMouseTrackingAvoidsAllMotion` 和 `TestMouseEventFromSGRDropsNoButtonMotion` 固定该旧产品决策。

用户本次明确要求 hover 高亮，因此必须有意识地改为：启用 `?1003h`（1003 已覆盖 1002 的事件集合），让 `Cb=35` 产生 `inputEventMouseMove`，并更新旧测试。现有 `orphanSGRMousePattern` / draft sanitize 必须保留，seed 修复的 torn-SGR 测试必须扩展到 hover 报告，证明恢复 hover 不会把 `;18M` 一类尾字节写进 composer。

`ViewportHover`（`reducer.go:6019-6037`）当前只记 row、忽略 col，而且 `HandleMouse` 没把事件时间传进去。需要增加内部 `viewportHoverAt(col,row,at)`，按事件时屏幕计算 hover target，并且只有 target 改变才 repaint，避免 all-motion 输入引发持续重绘。

### Esc 的现有优先级

- idle 主循环先在 `run.go:790-826` 处理 panel、mention、slash、roster；之后才进入 `handleIdleHotkey`。
- active-run 在 `run.go:4638-4653` 先处理 roster 和子代理视图，再进入取消逻辑。
- blocking slash 在 `run.go:1417-1454` 把 Esc 当取消；关闭抽屉应先于该取消。
- 真正 modal 由 rawSelector 自己读 key，不在本计划抢它的 Esc。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 基线/定点测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run '<regex>' -count=1` | exit 0，列出的测试全绿 |
| TUI 包级回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | exit 0；不得继承真实 `FOREBRAIN_HOME` |
| Vet | `go vet ./...` | exit 0 |
| 真机构建 | 使用下文的 run-forebrain driver，独立 `FOREBRAIN_RUN_WORK` | 二进制和 assets 写到临时目录 |
| 行边界检查 | `tmux capture-pane -t <session> -p -e -N \| .forebrain/skills/tui-row-edge-audit/scripts/row-bytes.py 120` | 每行宽度正确，无 full-width trailing EL，边界无丢字/残色 |

## Suggested executor toolkit

- 必须加载并遵循 `run-forebrain`：fake provider、独立 home、tmux 点击/滚轮、避免 stale binary。
- 必须加载并遵循 `tui-row-edge-audit`：显式 cell painting、禁止依赖 EL 填背景、捕获必须带 `-e -N`。
- 测试复用 `reducer_test.go:2928-2949` 的 `clickAt` / `newComposerRenderer`，以及 `overlays_test.go:415-457` 的独立滚轮区域断言模式。这些测试修改全局 terminal size，**不要 `t.Parallel()`**。

## Scope

**只允许修改以下文件：**

- `pkg/tui/plan_drawer.go`（新建）：drawer 状态、最新 FramePlan 查询、宽度/布局/软换行/滚动、open-close helper、几何类型。
- `pkg/tui/plan_drawer_test.go`（新建）：本功能主要测试。
- `pkg/tui/render.go`：Renderer 的 drawer/last-painted hover 几何字段；不改 `planText` 的语义。
- `pkg/tui/reducer.go`：composer 热区元数据、分栏 paint、shownScreen、mouse/hover/wheel、历史滚动锚点、reset/view 生命周期。
- `pkg/tui/run.go`：现有 Esc 链中的最小挂点。
- `pkg/tui/input_events.go`：受控启用 all-motion + Cb=35 解码；保留并行 seed 修改。
- `pkg/tui/input_events_test.go`：hover 解码和 torn-input 回归；保留并行新增测试。
- `pkg/tui/chat_session_test.go`：只更新旧的禁 hover 协议测试，其他测试不动。

**明确 out of scope：**

- `pkg/tool`、`pkg/event`、`pkg/state`、`pkg/turn`、`pkg/gateway`、`frontend/`：不改协议、事件、存储或 Web。
- `docs/plan/TUI_COMPOSER_ESCAPE_TAIL_LEAK_PLAN.md`、`docs/plan/PLAN_REVIEW_REQUEST_AND_INPUT_RAIL_PLAN.md`：属于其他工作。
- 当前并行修改的 `pkg/run/llm_middleware.go`、`pkg/run/llm_middleware_test.go`、`pkg/turn/approval.go`、`pkg/turn/approval_test.go`、`pkg/tui/overlays.go`、`pkg/tui/overlays_test.go`：一律保留，不纳入本功能。
- `.claude/skills/run-forebrain/`：不为本功能修改驱动；验收只改临时 home 的生成配置。
- 不新增 slash 命令、快捷键提示设置、配置项、持久化字段、公开 API、动画框架或通用 drawer 框架。
- 不修改 composer 文本/caret 语义，不重写既有鼠标选择系统，不顺手重构 `renderViewport`。

## Git workflow

- owner 手动审阅并提交：执行者不得 `git commit` / `git push` / `git merge` / `git rebase` / `git stash`，不得开 PR 或擅自建 worktree。
- 改动留在当前工作树；保留所有既有未提交修改。不得用 checkout/restore 丢弃任何文件。
- 不更新 `plans/README.md`，不发布外部 issue。

## Steps

### Step 0：确认绿色基线

先确认 drift check 的三条命令与上文完全一致（HEAD、空 `git status`、三个内容哈希），并把完整的 `git status --short` 与 Scope 外文件内容哈希存成执行基线。再跑：

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui \
  -run 'Test(FormatWorkingCountersIncludesPlanProgress|WorkingStatusFormatterAppendsLatestCounters|PlanText|ViewportClickInComposerAreaDoesNotToggle|ReaderKeepsAPendingEscapeAcrossASeed|MouseTrackingAvoidsAllMotion|MouseEventFromSGRDropsNoButtonMotion)$' -count=1
```

最后两个测试**改之前必须是绿的**：Step 1 会改写它们固定的旧协议，所以"改前绿、改后绿"才是这次协议变更的对照。

**Verify**：exit 0。若基线红，STOP 并归因；不得把旧失败当成本功能通过。

### Step 1：受控恢复 hover 输入，先封住输入回归

在 `input_events.go`：

- 把 mouse enable 改成"先关 1002、再开 1003 + SGR 1006"，现有值是 `"\x1b[?1003l\x1b[?1002h\x1b[?1006h"`（`input_events.go:695`），改成 `"\x1b[?1002l\x1b[?1003h\x1b[?1006h"`；disable 仍完整关闭 1006/1003/1002。
- `mouseEventFromSGR` 把 `Cb=35 && press` 映射为 `inputEventMouseMove`，保留 32=left drag、其他 button 丢弃、wheel 规则不变。
- `inputEventMouseMove` **已经存在**（`input_events.go:36`）并已在三处路由（`run.go:1426`、`4526`、`5623`），只补解码，不要新增 kind 或新的 dispatch 分支。
- 更新注释，说明 all-motion 仅用于局部 hover，渲染层必须按 target 去重。

在 `chat_session_test.go` 把 `TestMouseTrackingAvoidsAllMotion`（`24302`）和 `TestMouseEventFromSGRDropsNoButtonMotion`（`24327`）改成固定新协议；在 `input_events_test.go` 增加：

- Cb=35 解码为 move，0-based 坐标正确；32 仍为 drag。
- enable/disable 序列完整，退出会关闭 1003。
- 一个 hover SGR 被 seed 从中间切断时，后续 tail 不进入 draft；测试 harness 沿用 `TestReaderKeepsAPendingEscapeAcrossASeed`。
- move 不产生 draft、paste、caret request 或 selection。

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui \
  -run 'Test(MouseTracking|MouseEventFromSGR|ReaderKeepsAPending|SanitizeTerminalDraft|StreamStateAppendPasteSanitizesTerminalControls)' -count=1
```

**Verify**：exit 0；尤其 seed/torn 和 sanitize 测试全绿。`-run` 是按前缀子串匹配，`SanitizeTerminalDraft` 实际命中的是 `TestSanitizeTerminalDraftAdjustsCursor`（`chat_session_test.go:24291`）；仓里没有叫 `TestSanitizeTerminalDraft` 的测试，别去找。

### Step 2：建立单一数据来源和纯 drawer 布局

在 `plan_drawer.go` 新建小型内部实现，不做通用框架：

- 文件属于现成的 `package tui`，不新增包、不需要 `doc.go`；测试放同包的 `plan_drawer_test.go`。
- `planDrawerState` 只保存 open/view/scroll/followActive；`screenRect` 使用半开单元区间。
- `latestPlanFrameLocked()` 从当前 VM 逆序取最新 `FramePlan`（`activeVMLocked()`，`reducer.go:5547`）。
- `planDrawerWidth(totalWidth)` 按整数运算实现，不要引入别的 clamp：`d := totalWidth/3; if d < 32 {d = 32}; if d > 48 {d = 48}; if d > totalWidth/2 {d = totalWidth/2}`，`leftWidth := totalWidth - d`（120→d=40/left=80，80→d=32/left=48，60→d=30/left=30）。
- `buildPlanDrawerLayout(frame, width, height, scroll, followActive)` 返回固定标题、完整软换行正文、above/below 信息、close span、active focus 范围和 clamp 后 scroll。
- open 时绑定当前 view 并聚焦 active；close 重置临时状态；没有 FramePlan 时入口点击不打开空抽屉。
- 复用 `Frame.Title` / `Frame.Content` 和现有 plan 色，不重新解析 session store，不复制 []PlanUpdateItem。

在 `plan_drawer_test.go` 覆盖：宽度 120/80/窄屏、最新 plan、主/子代理隔离、CJK 长事项软换行、empty plan、active 初始可见、手动滚动后更新不抢位置、`(empty plan)` 占位、无 active 时从首行开始、首行 Explanation 不当事项显示。

测试函数名就按功能取 `TestPlanDrawerLayout`、`TestPlanDrawerWidth`、`TestPlanDrawerLatestFrame`、`TestPlanDrawerContent`、`TestPlanDrawerScroll`，其余用例挂在这五个前缀下（Step 3-5 的 `TestPlanDrawer.*` 也依赖它们存在）。

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestPlanDrawer(Layout|Width|Latest|Content|Scroll)' -count=1 -v | tee /tmp/drawer-step2.log
grep -c '^--- PASS' /tmp/drawer-step2.log
```

**Verify**：exit 0，且第二条命令打印的 PASS 数 **≥ 5**。`go test -run` 匹配不到任何测试时同样 exit 0（只打印 `no tests to run`），所以这里必须数 PASS 行——它是唯一能证明布局层真的被测过的证据。所有事项正文都存在于布局的显示行中，无省略号截断。

### Step 3：实现非覆盖式历史分栏和稳定 reflow

在 `render.go` / `reducer.go`：

- 给 Renderer 增加 drawer 状态和 **last-painted** progress/drawer/close/history-width 几何；事件不得用新尺寸重算旧屏。新增的几何字段要在 `liveScreenLocked()`（`reducer.go:6538`）里复制进 `shownScreen`，因为记录屏幕快照的就是它。
- `paintViewportLocked` 在 drawer open 时按 Step 2 的 `leftWidth` 走"分栏拼接的精确接缝"：`renderViewport` 传 `leftWidth - viewportRightPadding`（`4644`），`canvasRow` 传 `leftWidth`（`4687`），抽屉单元接在同一行末尾；bodyHeight 和 composer block 不变。
- 拼接前分别完成左侧 selection/hover（`applySelectionHighlight` / `applyHoverHighlight`，`4713` 的宽度参数换成 `leftWidth`），drawer 区域不能再被历史 hover 填充。用户卡 full-bleed、右侧 padding、SGR reset、CJK 宽度必须按 UX 合同处理。
- 每行用显式空格拥有自己的 cell；不要在 full-width row 末尾新增 `\x1b[K`，不要在行内塞 CUP 来“跳”到右栏，也不要整屏 `\x1b[2J`。
- 开关 drawer 时保存/恢复历史滚动锚点；follow bottom 不脱离，手动滚动保留顶部 block。锚点是 `(blockID, 块内相对行)`，不是数字 offset——现成材料在 `viewportRender`（`reducer.go:2427-2435`：`spans []lineSpan` 与 `firstRow`）和 `lineSpan{blockID,startRow,rowCount}`（`2419-2424`）：重排前用 `r.vpLastRender.blockAtRow(0)` 取顶部 block（它收的是**屏幕行**，内部自己加 `firstRow`，`reducer.go:2529-2540`），再从 `spans` 找到该 block 拿 `span.startRow`，`rel := r.vpLastRender.firstRow - span.startRow`；重排后在新的 `viewportRender.spans` 里找同一 `blockID`，`newOffset = span.startRow + min(rel, span.rowCount-1)`。找不到该 block（reset / 视图切换）就回 follow bottom。
- jump-to-bottom 的布局和 hit span 都使用 last-painted 左侧宽度。
- session reset/view switch 清 drawer；resize 重新 clamp drawer scroll 和几何。

在测试中对比开关前后的 composer 行与 draft/caret，验证其不变；验证历史长文在较窄宽度完整 reflow、用户卡到 divider、无右侧残字、jump 不进入 drawer。另加一例：slash/overlay 面板打开（composer 被替换、bodyHeight 缩短）时抽屉仍开、行数跟着缩、没有假热区。

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui \
  -run 'TestPlanDrawer.*|TestViewportComposerKeepsSingleSpacingAroundWorkingAndPendingQueue|TestComposerStatusLineIsOneControlFreeRow' -count=1
```

**Verify**：exit 0；每个合成物理行**恰好**等于终端宽（`canvasRow` 补到 `leftWidth`、抽屉补到 `D`，两者相加必须正好是 `W`；短一列就是右侧空缺，长一列就是换行/残字），full-width 行无 trailing EL。

### Step 4：注册精确热区并接通所有鼠标手势

`composerBlock` 定义在 `reducer.go:3415`，新字段只加这一处。在它里面记录 block-local progress span：三个 composer 构建分支（`3624` / `3740` / `3816`）都只在 `renderComposerStatusLine` 产生的最终行里寻找完整 `☑N/M`——那一行已 fold + truncate，列号必须按显示宽度算；垂直滚动 composer 时同步转换/隐藏该 span。paint 之后转成 screen span，并经 `liveScreenLocked()`（`reducer.go:6538`）复制进 `shownScreen`。

slash / overlay 打开时，composer 被 `paintViewportLocked` 的 overlay 分支（`4483` / `4564` / `4611`）整体替换，那一帧没有 status 行，因此**没有** progress 热区：抽屉保持打开、行数随 bodyHeight 缩短，不要为 overlay 造一个假热区。

实现一个共享的 `planDrawerClickLocked(s,col,row)`，同时供 `ViewportClickToggle` 和真实 release 使用，优先级为：

1. progress token → toggle；
2. 已打开 drawer 的 close span → close；
3. 已打开 drawer 的内部 → consume；
4. 已打开 drawer 的外部（包括 jump button/composer/transcript）→ close + consume；
5. 否则交给原逻辑。

其他鼠标规则：

- press 落在 progress/drawer 控件时只 arm 控件，不启动选择；drag/release 不得穿透。照抄 jump button 的做法：`viewportPress{armed: true, onJumpButton: true, …}`（`reducer.go:6756`）是同一类拦截。
- drawer 内 wheel 先路由到 drawer scroll；再进入原有 overlay/composer/transcript 路由。divider 那一列 (`col == leftWidth`) 归抽屉。
- `viewportHoverAt` 按事件时间的 shownScreen 计算 progress/close/transcript target；只有 target 改变才 repaint。高亮只作用于 token/close 的局部单元，静止样式不变，禁止下划线。
- 这个去重必须被测试固定：同一 target 上连续 20 次 move，写出的字节数不增长。注意 `newComposerRenderer`（`reducer_test.go:2939`）把 `bytes.Buffer` 藏在函数里、只回 `*Renderer`，拿不到长度——这类断言要照 `overlays_test.go:415-457` 自己搭：`var out bytes.Buffer; r := NewRenderer(&out, &out); r.EnableViewportMode(); forcedTermWidth, forcedTermHeight = w, h; t.Cleanup(...)`，再对 `out.Len()` 断言。`?1003h` 之后指针每过一格都会来一个事件，per-motion 重绘等于把终端打满。
- 历史选择跨越 drawer 边界时 clamp col，确保复制内容来自真实左侧文本。

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui \
  -run 'TestPlanDrawer.*|TestViewportClickInComposerAreaDoesNotToggle|TestComposerClickPlacesCaretAtClickedRune|TestViewportScrollAtOverTallOverlayScrollsOverlay' -count=1
```

**Verify**：exit 0；外部关闭点击后无 caret request、无 fold/view switch/jump，drawer 内滚轮不移动历史。

### Step 5：接入 Esc 优先级和完整生命周期

在 `run.go` 最小修改，三个插入点都用行号锚定：

- idle：`handleIdleHotkey`（`run.go:5044`）里 `case hotkeyEscapeInterrupt:`（`5054`）的第一句。它之前的高层菜单（panel / mention / slash / roster / auto-continue）已在 `run.go:790-826` 和调用处处理完。这个分支**不会**从 active-run 路径 fall through 进来（active-run 的 Esc 分支每条路径都 `return`，到不了 `4828` 的 `_ = handleIdleHotkey(...)`；`5050-5051` 那句注释讲的是 `hotkeyRedraw`），所以这三处是**彼此独立、都必须改**的插入点，任何一处漏掉都是一条真实路径上 Esc 直接取消 turn。
- active-run：`run.go:4638`（`if ev.hotkey == hotkeyEscapeInterrupt {`）里，roster 分支（`4641-4645`）之后、子代理视图分支（`4648`）之前。
- blocking slash：`handleBlockingSlashInput`（`run.go:1417`）里的 `case hotkeyInterrupt, hotkeyEscapeInterrupt:`（`1453`）之前——它现在直接 `return true` 取消。

不要改 rawSelector/modal 的 Esc；它们关闭后，下一次 Esc 才处理 drawer。补充测试证明：Esc 第一次只关 drawer，active run 仍在；第二次才走原逻辑；高层菜单优先级不变。

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui \
  -run 'TestPlanDrawer.*|TestWorkingStatusFormatterIncludesSpinnerElapsedAndEscapeInterruptHint' -count=1
```

**Verify**：exit 0；close drawer 不调用 `CancelActiveRun`，不改变 draft、session 或 active view。

### Step 6：包级回归和静态检查

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1
go vet ./...
```

**Verify**：两个命令均 exit 0。若全包失败而相同测试单独通过，先隔离归因；不要反复重跑当证据，也不要修改无关测试。

### Step 7：fake provider 真机验收 + SGR/行边界证据

使用独立临时实例，禁止继承真实 home。下面的 `serve` 只生成临时 provider/config；在启动 TUI 前把**临时配置**模型名改为 owner 统一的 `deepseek-v4-flash`，不改仓库 driver。

```bash
export FOREBRAIN_RUN_WORK="${TMPDIR:-/tmp}/forebrain-run-plan-drawer"
export FOREBRAIN_RUN_SESSION=fb-plan-drawer
export FOREBRAIN_RUN_PORT=8764
D=.claude/skills/run-forebrain/driver.sh
$D reset
$D build
TODO_JSON='{"name":"session_todo","arguments":{"action":"set","items":['
TODO_JSON+='{"id":"0","content":"归档计划并保留完整中文长路径，验证历史与抽屉都自动软换行而不截断","status":"completed"},'
TODO_JSON+='{"id":"1","content":"构建修复前二进制","status":"completed"},'
TODO_JSON+='{"id":"2","content":"新增回归测试","status":"completed"},'
TODO_JSON+='{"id":"3","content":"修改 pkg/tui/input_events.go 的鼠标交互并验证 hover","status":"in_progress"},'
TODO_JSON+='{"id":"4","content":"验证中文、路径和很长的事项正文在右侧抽屉中完整换行显示，不出现省略号","status":"pending"},'
TODO_JSON+='{"id":"5","content":"tmux 真机 A/B 验收","status":"pending"},'
TODO_JSON+='{"id":"6","content":"收口并清理临时环境","status":"pending"}]}}'
FAKE_REPLY_DELAY=120 $D serve tool "$TODO_JSON"
python3 - <<'PY'
from pathlib import Path
import os
p = Path(os.environ['FOREBRAIN_RUN_WORK']) / 'home' / 'forebrain.yaml'
s = p.read_text()
assert 'model: deepseek-chat' in s
p.write_text(s.replace('model: deepseek-chat', 'model: deepseek-v4-flash'))
PY
tmux new-session -d -s "$FOREBRAIN_RUN_SESSION" -x 120 -y 40 \
  -c "$FOREBRAIN_RUN_WORK/proj" \
  "FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME=$FOREBRAIN_RUN_WORK/home $FOREBRAIN_RUN_WORK/forebrain 2>$FOREBRAIN_RUN_WORK/tui.err; exec bash"
# 若出现 Trust this folder?，只回答一次：$D key Down Enter
$D wait '/ commands' 25
$D submit '建立七项待办并保留足够长的历史消息用于分栏重排验收'
$D wait '☑3/7' 25
```

按顺序完成并留 `$D screen` / SGR 证据：

1. 从 `tmux capture-pane -p -e` 找到 progress 的 1-based row，再用 `$D col '☑3/7' <row>` 读数——它打印 `row=… col=…`，两个都是 1-based，`col` 是 token 首字符的显示列。用 `$D click <col> <row>` 点击；确认 `Updated Plan` 与 `×` 同处右侧标题行，左侧历史仍在且换行，composer 行位置/宽度不变。
2. 向 progress 发一次无键移动，确认只有 token 出现 `48;5;236` 背景：

   ```bash
   $D send $'\x1b[<35;COL;ROWM'    # COL/ROW 换成上一步读到的 1-based 值
   ```

   再把指针移到另一行（例如 `$D send $'\x1b[<35;1;2M'`），确认 token 恢复 faint 且无下划线。
3. 点击 drawer 内非 `×` 区域，保持打开；`$D wheel <col> <row> down` 时只滚动列表，长事项完整可读。
4. 点击 drawer 外，关闭且同次不折叠卡、不切 view、不移动 caret。
5. 重开，点击 `×` 关闭；重开，Esc 关闭且 `Working` 仍在；重开，再次点击 progress 关闭。
6. drawer 打开时 `tmux resize-window -t "$FOREBRAIN_RUN_SESSION" -x 80 -y 40`，抓一次 80 列证据（此时抽屉 32 列、`W/2` 上限才真正生效），再恢复到 120 列，验证双栏、历史滚动位置和 composer 正常。
7. 打开状态下，在两种宽度各跑一次行边界检查：

   ```bash
   tmux capture-pane -t "$FOREBRAIN_RUN_SESSION" -p -e -N | \
     .forebrain/skills/tui-row-edge-audit/scripts/row-bytes.py 120
   # 缩到 80 列后再抓一次：
   tmux capture-pane -t "$FOREBRAIN_RUN_SESSION" -p -e -N | \
     .forebrain/skills/tui-row-edge-audit/scripts/row-bytes.py 80
   ```

   检查 divider/右边界无丢字、无残色、无 full-width trailing EL。
8. `$D stop` 清理。

**Verify**：以上每一步都有实际 screen 或 SGR 输出，不允许只写“目测正常”。真机任何一步失败都先回到对应单元测试复现，再修复。

## Test plan（最低矩阵）

| 类别 | 必须覆盖 |
|---|---|
| 数据来源 | 主视图/子代理各取自己的最新 FramePlan；plan update/clear 立即更新 drawer；不增加第二份 todo 状态 |
| 入口 | 完整 token 可点击；active task 文案不属热区；截断 token 无热区；CJK 前列显示列计算正确 |
| 打开/关闭 | 外部点击、×、Esc、progress toggle；内部点击不关闭；run 结束不强制关闭 |
| 防穿透 | 外部点击不 fold、不开子代理、不 jump、不移动 caret；控件 press/drag/release 不选择底层 |
| hover | Cb35 输入、局部 bg236、移出恢复、无下划线；target 不变不 repaint；torn hover report 不泄漏到 draft |
| 分栏 | 120/80/窄屏；普通历史与用户卡 full-bleed；composer 不变；开关/resize 完整 reflow；slash/overlay 打开时抽屉随 bodyHeight 缩 |
| 阅读连续性 | follow bottom 保持；手动历史顶部 block 保持（锚点是 blockID+块内相对行，不是 offset）；drawer active 初始可见；**无 active 时从首行开始**；手动 drawer scroll 不被 update 抢走 |
| 滚动 | 长列表 wheel（每次 3 个显示行）、上下边界、above/below 按显示行计数；空/短列表不伪造滚动；divider 列（`col == leftWidth`）归抽屉；抽屉已到边界时 wheel **回退**到既有 overlay/composer/transcript 路由 |
| 选择/点击共存 | 左侧文本选择正常；跨界 clamp；overlay/composer 原有点击与滚轮测试不回归 |
| 生命周期 | session reset、view switch、resize 清旧几何；stale shownScreen 不触发新屏幕上的动作 |
| 字节安全 | 无 full-width trailing EL；每行恰好预算宽度；SGR 不泄漏；中文/emoji 不裁半 |
| Esc | 高层菜单优先；第一次只关 drawer；active run 不取消；第二次恢复原行为 |
| 转录行不动 | `formatWorkedStatusTitle` / `subagentWorkedFrames` / `planText` 无改动：`Worked for … · ☑N/M` 行仍不可点 |

## Done criteria

全部满足才算完成：

- [ ] 上述四种打开/关闭路径及防穿透测试均存在并通过。
- [ ] drawer 打开时历史区域实际变窄并 reflow，无覆盖；composer/draft/caret 不变。
- [ ] hover 真机输入可用，仅局部背景高亮，无下划线；target 不变不重绘（连续 20 次同 target move 不写任何字节）；torn SGR 回归全绿。
- [ ] 最新 plan、空 plan、长事项、active 初始定位、手动滚动稳定性和 resize 全有测试。
- [ ] `formatWorkedStatusTitle` / `subagentWorkedFrames` / `planText` 未改动（`git diff` 里这三个函数不出现）。
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` exit 0。
- [ ] `go vet ./...` exit 0。
- [ ] fake-provider 真机八步验收完成并保存 screen/SGR 证据（120 与 80 列各一份行边界报告），模型为临时配置的 `deepseek-v4-flash`。
- [ ] 对照执行前保存的 `git status --short` 与内容哈希，只新增本计划 Scope 内的预期修改；Scope 外文件保持基线内容。
- [ ] 未运行任何 commit/push/merge/rebase/stash。

## STOP conditions

出现以下任一情况立即停止并报告：

- drift check 三条命令与上文不符（HEAD 不是 `0e6e3827`、Scope 内有未提交修改、或三个内容哈希不同）；不得自行合并未知改动。
- Current state 摘录与 live code 语义不符，或需要修改 Scope 外文件才能完成。
- `?1003h` 恢复后任何 hover/torn SGR 能进入 composer draft，或 hover 引发持续无差别 repaint。
- 为实现分栏而必须复制 todo 数据、改变 Frame/事件/存储协议，或必须让 composer 一起缩窄。
- 外部关闭点击仍会触发底层动作；无法保证 press 时屏幕快照语义。
- 为了让入口"随时可用"，发现非要在 `formatWorkedStatusTitle` 的 `Worked for … · ☑N/M` 转录行或历史 plan 卡上再加一个入口——那是合同外的第二个入口，停下来报告。
- 包级测试出现与改动相关的重复失败；同一验证合理修复后仍失败两次。
- 真机验收需要读取真实 `~/.forebrain`、真实凭据或修改仓库验收驱动才能继续。

## Maintenance notes

- 入口只在 run 进行中存在：`workingStatusFormatter` 结束即消失，历史里的 `☑N/M` 不是入口。若产品后来要求 run 之外的常驻入口，那是一次单独设计（要让历史行也能打开抽屉），不要在本实现上补。
- 未来新增 composer status producer 时，如果也能显示 `☑N/M`，必须复用最终绘制行的热区注册，不要另写坐标计算。
- 未来改 `renderViewport` 的宽度/full-bleed 规则、`canvasRow` 的补齐宽度、jump button、选择坐标或 `shownScreen` 时，必须同步跑 drawer 分栏与 stale-screen 测试。
- all-motion 是本次明确产品需求对旧“禁 hover”决策的替换；不得再次无条件丢弃 Cb35。若未来要限制流量，应做 target 级去重/事件合并，而不是破坏 hover 语义。
- drawer 是有意的轻量只读分栏，不是通用 modal/panel 框架。任务勾选编辑、键盘列表导航、动画和 drawer 内文本选择均不在本期；如未来需要，应单独设计，不能在本实现里顺手加入。
- 本计划未审计 Web/gateway，也未设计 Web 待办抽屉；它们不属于本次交付。
