# TUI：行内代码（inline code span）不再借用工具卡片 header 的琥珀色

状态：已实施并真机验收通过（2026-11-14）：一处生产改动 + 三个回归测试，验收记录见文末 §10
基线：仓库 `dev` @ `a1da14e4`（工作区另有未跟踪的临时验证测试，见 §2.2，本次一并删除）
计划文件落位：批准后复制到 `docs/plan/TUI_INLINE_CODE_COLOUR_PLAN.md`（该目录已 gitignore），收口时回填验收记录。执行偏离见 §10.2。

执行方式（owner 裁决，项目技能 `plan-gated-bugfix`）：**直接在当前工作区源码实施**，不用 worktree/快照，**不做任何 git commit**；改动留工作区等 owner 审阅。本仓库当前有并行会话 `forebrain-harness-b2` 的 WIP 留在主树（已收工但未提交：17 个已修改跟踪文件，含 `pkg/tui/run.go`、`pkg/tui/notify.go`、`pkg/gateway/*`、`pkg/turn/*`、`frontend/*`，另有未跟踪的 `pkg/tui/subagent_footer_display_test.go`）。我的改动只碰 `pkg/tui/render.go` 与新建测试文件，两者都不在其清单里；收口时用「相对 Step 0 基线的差集」判定改动范围（§5 Step 0.6、§6.1），绝不 revert/stash/格式化他们的文件。

---

## 1. 为什么修（Why this matters）

owner 报告：forebrain TUI 里 `memories_search` 完成卡片的 header「看起来」有两个问题——耗时 `0.7s` 不在最后一行末尾，记忆文件路径中间少了斜线。owner 随后给出真正的成因：

> 这个问题产生的原因是 body/命中路径的文字颜色跟 header 的文字颜色一样，都是黄色，所以用户误以为 body/命中路径是 header 的最后一行。

也就是说：卡片 header 折行后，紧随其下的 body 第一行（命中文件的路径）与 header 同色同缩进，读者把它读成了 header 的最后一行——于是 header 自己的耗时（真的在 header 最后一行末尾）被误判成「中间行末尾」，header 的 scope 路径（`in /Users/…/-harness`）与 body 的命中路径（`extensions/ad_hoc/notes/….md`）被误读成一条缺了斜线的路径。

修复对象不是措辞、不是路径词汇，而是**调色板语义**：`214` 在本仓库只有一个含义——"已经落定的工具卡片 chrome（header / marker）"；而行内代码也画成 `214`，于是正文（content）冒充了 chrome。这条规则仓库自己写在代码注释里，只是被这一处违反了（见 §2.1 (d)）。

## 2. 根因（file:line 证据链）

### 2.1 因果链（每一环都能在源码上指认）

(a) 行内代码被画成 `214` —— `pkg/tui/render.go:1752-1757`（verbatim）：

```go
	case *mdast.CodeSpan:
		text := collapseMarkdownWhitespace(renderMarkdownInlineChildren(n, source))
		if text == "" {
			return ""
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(text)
```

(b) `214` 同时就是落定工具卡片 header 的 accent —— `pkg/tui/render.go:2556-2562`（verbatim）：

```go
	skillCardHeaderColor = "43"
	// ordinaryToolHeaderColor tints every other settled tool card.
	ordinaryToolHeaderColor = "214"
	// reasoningHeaderColor tints the "Thought for …" card. It is named here,
	// beside the colors it must stay distinct from, so a future change to
	// either one is made with the other in view.
	reasoningHeaderColor = "147"
```

header 实际用色：`render.go:2088-2141` 取 `lineColor = toolHeaderColor(f)`（落定普通工具 = `ordinaryToolHeaderColor`，`render.go:2539-2540`），并把 action/target/suffix 三段都设成同一个 `headerPartStyle = Foreground(lineColor)`。

(c) memory search 的 body 第一行就是 code span —— `pkg/tool/format.go:1101-1130`（`formatMemorySearchResult`）用 `` markdownCode(path) `` 开始每一条命中（`format.go:1127`），`markdownCode` 输出反引号包裹 → goldmark 解析为 `mdast.CodeSpan` → 命中 (a) 的 `214`。memory 查询工具的 body 走的正是 Markdown 分支：`render.go:2385-2396`（`isMarkdownBodyTool(title) && content != ""` → `renderAssistantMarkdownWithWidth`），而 `isMarkdownBodyTool` 对 memory 查询工具返回 true（`render.go:3872-3875`，`IsMemoryQueryTool`）。

(d) 这一步踩了仓库自己写下的调色板规则 —— `render.go:1205-1208`：

```go
// markedSpanColor draws a marked span. It is deliberately neither the code
// colour nor the accent the tool header uses, so a mark cannot be mistaken for
// either the quoted text's own code spans or the card's chrome.
const markedSpanColor = "114"
```

「代码颜色」与「卡片 chrome 的 accent」被这段注释当成两个不同的东西；实际上 `render.go:1757` 让它们相等。

(e) 结构与颜色的叠加让错觉成立：`wrapCardHeader` 的续行 gutter 是 `"  │ "`（`render.go:3088-3090`），body 第一行前缀是 `"  └ "`（`toolOutputLinePrefix`，`render.go:3029-3037`）——两者都是 4 列，同色同列对齐，视觉上就是同一段文字。

### 2.2 复现（修复前必须亲自跑一遍；已在计划期跑过）

工作区现有临时复现测试 `pkg/tui/zz_scratch_memsearch_test.go`（**未跟踪**，本次收口要删）：它按 owner 截图的那次调用构造 frame（4 条 query、`path` = 项目 memory root 的绝对路径、命中 `extensions/ad_hoc/notes/2026-11-07T01-14-32-llm-models-in-db-no-dotenv.md · line 14`、`Duration: 700ms`），在 110/120/132 列宽各渲染一次。

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestScratchMemSearchCard -count=1 -v
```

132 列宽的输出与截图**非空行逐行一致**（body 与首个 `>` 之间还有一行只有 4 空格缩进的空白续行，此处省略未打印）：

```
● Searched memories "connect 登录 火山 coding plan", "api_key required non-interactive", "volcengine ark 火山方舟",
  │ "connect command TUI OAuth" in
  │ /Users/doudou/.forebrain/workspace/memories/projects/-Users-doudou-workspace-unionj-cloud-forebrain-harness · 0.7s
  └ extensions/ad_hoc/notes/2026-11-07T01-14-32-llm-models-in-db-no-dotenv.md · line 14
    > line thirteen
    > 另：同 provider+model 可配多条
    > line fifteen
```

这条复现同时证明了两件事实，修复不应改变它们：

- 耗时**确实**在 header 的最后一行末尾（第 3 行），`DisplayParts` 把 `suffix + " · " + duration` 拼在 header 文本尾部（`pkg/tool/header_parts.go:467-478`），折行后仍落在 header 末行。**不需要**改耗时拼接逻辑。
- header 的 scope 是调用方传入参数的忠实回显（`MemorySearchScopeLabel`，`pkg/tool/format.go:1295-1313`），body 命中的路径是 store 相对地址（`pkg/memory/backend.go:922-928` `displayPath`）。两段都是各自正确的；缺斜线是读者把它们读成一条路径的结果。

### 2.3 同类缺口穷举（owner：同类问题必须一并修复，补齐所有缺口）

`pkg/tui` 里 `214` 的每一处使用，按语义分类：

| 位置 | 用途 | 归类 |
|---|---|---|
| `render.go:1757` | Markdown 行内代码 | **content —— 本次唯一缺陷** |
| `render.go:2558` | `ordinaryToolHeaderColor`（落定卡片 header/marker） | chrome，正确 |
| `render.go:3530` | 折叠 header 的 `●` + 色 | chrome，正确 |
| `render.go:6041/6047/6049` | goal 卡片的 `■/◎/↻` | chrome，正确 |
| `render.go:7479` | `panelWarnStyle`（面板告警文字） | chrome，正确 |
| `reducer.go:3402` | `renderCompactFrame(f, "tool", "214")` | chrome，正确 |
| `reducer.go:6093` | `overlayNoteStyle`（overlay 注释文字） | chrome，正确 |

结论：content 侧只有 `render.go:1757` 一处，改这一个常量即覆盖全部同类缺口——因为所有 Markdown 正文（memory 查询卡、`memories_add_ad_hoc_note`、`intermediate_tool`、`web_fetch`/`web_search`/`retrieve_output`、`peer_send`、`exit_plan_mode` body、以及**助手正文**）都吃同一个 `mdast.CodeSpan` 分支。

**一条必须写死的设计事实**（否则新测试会写错断言）：body 首行行首的 `└` 是本卡片的 rule 色画的（`paintCardCorner`，`render.go:3107-3130`），落定卡片就是 `214`。所以修好之后 body 首行仍然**含且仅含一个** `38;5;214`——那是 `└`，不是路径。测试只能断言「`└ ` 之后不再出现 214」或「该行 214 恰出现 1 次」，不能断言 body 整行无 214。

### 2.4 竞争者解释与被排除的理由

| 候选解释 | 证据与结论 |
|---|---|
| 「耗时拼接位置错了（应拼到最后一行）」 | 实测 header 末行末尾就是 `· 0.7s`；`DisplayParts` 把 duration 放在文本尾部，折行由 `wrapToolDisplayRows` 做，duration 必然是末行最后一个 token。排除。 |
| 「header 显示的 scope 路径错了/删了斜线」 | `MemorySearchScopeLabel` 只做 `"in " + input path`（`format.go:1312`），没有拼接；body 的路径来自 `displayPath(root, file)`（`backend.go:922-928`），两段各属不同对象。没有代码在两者之间拼字符串。排除。 |
| 「代码逻辑正确，读者看错」 | 一半对：布局正确，但**颜色**让错误的读法成为唯一合理的读法（同色同列对齐）。owner 已确认为缺陷。 |
| 「应该改 header 的 accent 颜色」 | 会重绘所有落定卡片的 chrome（blast radius 大，且 `214` 在 palette 注册表里就是 header 的含义）。排除。 |

## 3. 修复设计

一条原则：**行内代码属于正文，不能穿卡片 chrome 的颜色**。给行内代码一个专属常量，值取 owner 已选定的 `"32"`——xterm-256 的 32 是 **#0087d7（azure 青蓝；cube 0/2/4 = 0,135,215）**，白底对比 ~3.9:1、深底 ~4.4:1。它与已占用的色相都不相同：43 技能卡 / 147 推理卡 / 114 命中标记绿 / 39 计划模式的默认蓝（terminal blue，与 azure 可区分，且计划模式 header 罕见）/ 74 提示符蓝 / 45 助手点青 / 141 计划抽屉紫 / 170·127 子代理粉 / 244 暗 / 252 正文 / 214 卡片琥珀。
（注意别抄成 `#0087af`：那是 31 的值。色值以 `"32"` 这个索引为准，测试按 `38;5;32` 断言。）

改动只有两处代码 + 测试：

1. `pkg/tui/render.go` 的 palette 注册块（`render.go:2543-2563` 那个 `const (...)`）里新增：

```go
	// inlineCodeColor paints a Markdown code span. Inline code is prose that
	// quotes something literally, not card chrome, so it must stay clear of
	// every card accent above: a body whose first line is a code span — a
	// memory search's matched path — was otherwise the same amber as the
	// header it sits under, and read as the header's last row.
	inlineCodeColor = "32"
```

2. `pkg/tui/render.go:1757` 的 `lipgloss.Color("214")` → `lipgloss.Color(inlineCodeColor)`。

不改：`ordinaryToolHeaderColor` 及一切 chrome 用色、`markedSpanColor`、`DisplayParts`/`DisplayHeaderParts` 的任何措辞与拼接、`pkg/tool` 的全部文案、memory 路径词汇（见 §4）。

### 3.1 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 性质 |
|---|---|---|---|
| memory search 卡 body 首行（owner 报的这条） | code span = 214，与 header 同色 → 被读成 header 末行 | code span = 32，header 仍是 214 → body 一眼是 body | 刻意修订（本 bug） |
| `memories_read`/`memories_list` body 的 `` `path` · … `` | 同上 | code span = 32 | 刻意修订（同类） |
| `memories_add_ad_hoc_note` / `intermediate_tool` 的 note Markdown body | 同上 | code span = 32 | 刻意修订（同类） |
| `web_fetch` / `web_search` / `retrieve_output` body | 同上 | code span = 32 | 刻意修订（同类） |
| `peer_send` 卡片 Markdown body | 同上 | code span = 32 | 刻意修订（同类） |
| 助手正文里的行内代码（`` `pkg/tui/render.go` ``） | 214，在满是 214 header 的转写里读作 chrome | 32 | 刻意修订（同类） |
| 卡片 header / marker / 折叠 header / goal 卡片 / 面板告警 / overlay 注释 | 214 | 214 不变 | 不变 |
| fenced code block（syntax highlighting，chroma） | 主题色 | 不变 | 不变 |
| 无颜色模式（`noColorActive` / Ascii profile） | 无色 | 无色 | 不变 |

风险：只有可见色相变了，不涉及布局、换行、宽度计算；`32` 在 256 色与 truecolor 下都可量化（256 索引本身，测试按 `38;5;32` 断言）。

## 4. Scope

**In scope（只允许动这两个路径）**

- `pkg/tui/render.go`：新增 `inlineCodeColor` 常量；`mdast.CodeSpan` 分支改用它。
- `pkg/tui/inline_code_color_test.go`（新建）：§5 Step 2 的三个测试。

**Out of scope（明确不碰，理由都写在这里）**

- `pkg/tui/run.go`、`pkg/tui/notify.go`、`pkg/tui/subagent_footer_display_test.go`、`pkg/gateway/*`、`frontend/src/*`：**另一个会话 forebrain-harness-b2 已收工但 WIP 留树**（17 个已修改跟踪文件 + 1 个未跟踪文件，见前言）。我的改动不碰这些文件；若确实需要动，STOP 并把情况报告 owner（b2 已不在线，peer_send 无处投递）。
- `pkg/tool/*`：header 措辞、`MemorySearchScopeLabel`、`memoryStepSummary`、格式化器全部不动。
- memory 路径词汇（绝对入参 → store 相对地址的归一化、global 绝对路径路由）：一度被考虑（`Backend.Search` 的 `path` 回显是原始入参、`Backend.Read` 同理、instruction 承诺 global base 内绝对路径可解析但实际被拒），但 owner 已确认本次成因是颜色，且 header 的 scope 是「调用方传了什么」的忠实回显、body 已是 store 自身地址词汇——**不在本计划内**，避免把一次配色修复扩成 tool 结果契约变更。
- web 端行内代码配色：`frontend/src/assets/main.css:1987-1997` 已是「正文色 + alt 底小框」（`--forebrain-code-text: #111111`），本就与卡片 chrome 分离，无需改。
- memory 工具审批 overlay 里的原始路径回显（`pkg/tui/overlays.go:1690-1740`）：审批发生在执行前，此时只有入参，显示即将读取的参数是诚实的。不在本计划内。

## 5. 步骤（每步一个小改动 + 一个可验证结果；保持仓库随时可构建）

**Step 0 — 基线与现场取证（动代码之前）**

1. `git rev-parse --short HEAD` → 期望 `a1da14e4`；`git branch --show-current` → `dev`。
2. drift 检查（内容不符才 STOP，行号漂移不算）：

```bash
git show a1da14e4:pkg/tui/render.go | sed -n '1750,1760p;2535,2565p'
```

期望看到 §2.1 的 (a)(b) 两段原文，且 `git show a1da14e4:pkg/tui/render.go | grep -c 'Color("214")).Render(text)'` → `1`。
3. 复现（§2.2）：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestScratchMemSearchCard -count=1 -v`，期望 132 列那段的**非空行**与截图逐行一致。
4. 把颜色证据抓成 SGR：在同一次运行里把渲染出的各行以 `%q` 打印（临时改 `zz_scratch_memsearch_test.go` 的打印语句即可），期望 **header 第 3 行与 body 第 4 行（`└` 那一行）都含 `\x1b[38;5;214m`**——即缺陷现场（body 行上 214 出现 2 次：`└` 与路径）。若 header 与 body 路径不同色，STOP（说明现场已变）。

5. 基线测试，作为「预存失败」分类依据（owner 规则：预存缺陷必须本期修掉，0 失败）：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/architecture -count=1
```

记录通过/失败的用例名。若出现与我改动文件无关的失败，先判断是否由 b2 留树的 WIP 所致（b2 已下线，报告 owner），不自行修别人的文件。
6. 记录**基线改动快照**（收口按差集判定，本计划绝不要求"工作区只有我的改动"——主树本来就带着 b2 的 17 个已修改文件）：

```bash
git status --porcelain > /tmp/fb-baseline-status.txt
git diff --stat > /tmp/fb-baseline-diffstat.txt
grep -c ' M pkg/tui/render.go' /tmp/fb-baseline-status.txt   # 期望 0：基线里 render.go 未被任何人改
```

期望：`git status --porcelain` 约 20 条（17 个 ` M` + 3 个 `??`，其中两个是我本次的探针、一个是 b2 的 `subagent_footer_display_test.go`）；`render.go` **不在**其中。若 `pkg/tui/render.go` 在基线里已被修改 → STOP 并报告 owner（说明别人先动了同一个文件；b2 已下线，不在线的会话无法协调）。

**Step 1 — 生产改动（2 行级）**

在 `pkg/tui/render.go` 的 palette 注册块加入 §3 的 `inlineCodeColor = "32"` 及其注释；把 `render.go:1757` 的 `lipgloss.Color("214")` 改为 `lipgloss.Color(inlineCodeColor)`。

检查：

```bash
gofmt -l pkg/tui/render.go && CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui
git diff --stat -- pkg/tui/render.go
```

期望：`gofmt -l pkg/tui/render.go` 无输出、`vet` 无诊断、`git diff --stat` 只有 render.go 一处文件（新增 8±2 行、改 1 行）。

注意（基线实测）：基线工作区里 `gofmt -l cmd pkg third_party` **本来就非空**——`pkg/tui/subagent_footer_display_test.go`（forebrain-harness-b2 会话遗留的未跟踪文件，§4 明确禁碰）与本次的探针 `pkg/tui/zz_scratch_memsearch_test.go` 都未格式化。所以 gofmt 门禁只查本计划拥有的文件；仓库级 gofmt 输出仅作信息量，**不构成失败**，更不许去格式化别人的文件。

**Step 2 — 回归测试（新建 `pkg/tui/inline_code_color_test.go`；文件名用美式拼写，与常量 `inlineCodeColor` 一致）**

三个测试，全部沿用本包已有测试手法（`useANSI256ColorProfile(t)` / `renderFrameLines(frame, width, DiffThemeDark, 0)` / `stripANSI`，见 `pkg/tui/reducer_test.go:4712-4731`、`pkg/tui/chat_session_test.go:19966-20013`）：

1. `TestInlineCodeSpanKeepsItsOwnColour`
   `renderAssistantMarkdownWithWidth("see `pkg/tui/render.go` now", 80, DiffThemeDark)`：断言 `` pkg/tui/render.go `` 那个 token 带 `\x1b[38;5;32m`，且整段不含 `38;5;214`；断言未加反引号的相邻词不带色。
2. `TestMemorySearchCardBodyPathDoesNotWearTheHeaderAccent`（owner 截图场景，把 `zz_scratch_memsearch_test.go` 的构造搬进来）
   用 `tool.FormatToolStepResult` 生成 body（4 条 query + 1 条命中 `extensions/ad_hoc/notes/2026-11-07T01-14-32-llm-models-in-db-no-dotenv.md · line 14`），`Frame{Duration: 700*time.Millisecond, ToolMeta{Status:"completed"}}`，132 列宽渲染。断言：
   - header 的**最后一行**（body 首行之前的那一行）以 `· 0.7s` 结尾；
   - body 首行以 `└ ` 前缀开头、含命中路径、其路径 token 带 `38;5;32`；
   - **body 首行 214 恰出现 1 次**（那是 `└`，由 `paintCardCorner` 用卡片 rule 色画）；等价写法：去掉 `  └ ` 这 4 个可见列之后不再出现 `38;5;214`。**不要**断言 body 整行无 214（见 §2.3 的设计事实）。
3. `TestPaletteKeepsContentColoursOffCardChrome`
   `inlineCodeColor` 与 `ordinaryToolHeaderColor` / `skillCardHeaderColor` / `reasoningHeaderColor` / `markedSpanColor` 两两不等——把 `render.go:1205` 与 `2543-2563` 注释里「必须彼此区分」的规则变成机器门禁。


检查：

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestInlineCodeSpanKeepsItsOwnColour|TestMemorySearchCardBodyPathDoesNotWearTheHeaderAccent|TestPaletteKeepsContentColoursOffCardChrome' -count=1 -v
```

期望：3 个 PASS。

**Step 3 — 反证（守卫必须能失败）**

临时把 `render.go:1757` 改回 `lipgloss.Color("214")`，重跑 Step 2 的三条命令：期望 **1 和 2 失败**（3 仍会通过，因为它只比常量，不比使用点——这正是它存在的意义之外的一条护栏，说明两句注释里的规则被再次违反）。确认后改回 `inlineCodeColor`，再跑一次全 PASS。

**Step 4 — 包级回归 + 清理**

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/architecture ./pkg/tool -count=1
rm pkg/tui/zz_scratch_memsearch_test.go pkg/memory/zz_scratch_address_test.go
git worktree remove --force .worktrees/plan-review-1121217519   # 评审会话留下的 worktree（含其验证测试与已施加修复），--force 因为里面有 modified/untracked；不删会让后续 git worktree 命令失败
gofmt -l pkg/tui/render.go pkg/tui/inline_code_color_test.go   # 本计划拥有的两个文件，必须无输出
gofmt -l cmd pkg third_party   # 仅信息量：基线上已知非空（他人的未跟踪测试文件），不是失败项
```

期望：`pkg/tui`/`pkg/architecture`/`pkg/tool` 全绿；若某条失败在 Step 0 基线里也存在，按 §5 Step 0.5 的分类处理（其他人的 WIP 所致 → 报告 owner，不自行修别人的文件；是预存缺陷 → 本期根因修掉并补记本计划）。两个 `zz_scratch_*` 删除后，`git status` 相对 Step 0.6 基线快照的**差集**只剩新增 ` M pkg/tui/render.go` 与 `?? pkg/tui/inline_code_color_test.go`（b2 的 17 个 ` M` + 1 个 `??` 原样保留），判定以 §6.1 为准。

两个临时文件的归属说明：

- `pkg/tui/zz_scratch_memsearch_test.go` —— 本会话在计划阶段为复现 owner 截图那一次调用而建的探针（构造 frame 并在 110/120/132 列宽打印渲染结果），它的构造搬进 Step 2 的测试 2 后即删。
- `pkg/memory/zz_scratch_address_test.go` —— 本会话在计划阶段排查「memory 路径词汇」那条被否决线索时建的探针（`TestScratchAbsolutePathVocabulary`，验证绝对入参回显/global 绝对路径被拒）。它的结论已写进 §4「Out of scope」，**该线索不实施**，探针随本次删除。两者都是本会话产物，不在 forebrain-harness-b2 的改动清单里。


**Step 5 — SGR 级 A/B 与真机验收**

按 `.forebrain/skills/tui-render-ab-verify/SKILL.md` 走（脚本在 `.forebrain/skills/tui-render-ab-verify/scripts/`）：

```bash
S=.forebrain/skills/tui-render-ab-verify/scripts
$S/build-binary.sh old "$TMPDIR/forebrain-ab/old"   # 干净 HEAD
$S/build-binary.sh new "$TMPDIR/forebrain-ab/new"   # 工作区
FIX="$TMPDIR/forebrain-ab/render_sample.go"         # fixture（工具调用读到的文件）
ARGS='{"name":"memories_add_ad_hoc_note","arguments":{"filename":"2026-11-07T00-00-00-inline-code.md","note":"token `pkg/tui/render.go` in a note body"}}'
$S/drive-card.sh "$TMPDIR/forebrain-ab/old" ab-old 8741 "$FIX" "$ARGS" 'note it' /tmp/ab-old 256
$S/drive-card.sh "$TMPDIR/forebrain-ab/new" ab-new 8731 "$FIX" "$ARGS" 'note it' /tmp/ab-new 256
python3 - <<'EOF'
import re,sys
for p in ("/tmp/ab-old.card.ansi","/tmp/ab-new.card.ansi"):
    s=open(p,encoding="utf-8").read()
    print(p, "214:", s.count("\x1b[38;5;214m"), "32:", s.count("\x1b[38;5;32m"))
EOF
diff <(sed 's/\x1b\[[0-9;]*m//g' /tmp/ab-old.card.ansi) <(sed 's/\x1b\[[0-9;]*m//g' /tmp/ab-new.card.ansi)
```

期望：old 的正文 code span 计数落在 214 上（body 与 header 同色），new 的 214 只剩 header、正文 code span 落在 32；剥离 SGR 后的文本 diff 为空（只有颜色变化，没有布局变化）。

若 `memories_add_ad_hoc_note` 路线在该 fixture 下拿不到 code span，改用 `memories_read`（`{"name":"memories_read","arguments":{"path":"MEMORY.md","line_offset":1,"max_lines":3}}`，需要 scratch home 已有 memory 骨架；`drive-card.sh` 的 `start` 会为 `<work>/proj` 建 store），或在临时 home 里先写一个含反引号的 note 再读它。两条路线都拿不到 code span 时 STOP 并报告（说明验收工具链没覆盖到缺陷面）。

再做一次真机（plan-gated-bugfix 步骤 7）复看：用上述 new 构建在 tmux 里重现 owner 那次调用形态的 memory 查询卡（fake 模型，不得用真模型），`tmux capture-pane -t <session> -e -p` 取证，确认 body 的命中路径与 header 不再同色、header 末行末尾仍是耗时。

```bash
$S/cleanup.sh "$TMPDIR/forebrain-ab"   # 必须执行：遗留 HEAD worktree 会让后续 git worktree 命令出错
```

**Step 6 — 收口**

- 把本计划复制到 `docs/plan/TUI_INLINE_CODE_COLOUR_PLAN.md`，状态行改「已实施并真机验收通过（2026-…）」，补「验收记录」章节（场景→证据：三次测试输出、A/B 计数、tmux 取证摘要、踩到的工具链坑）。
- **不 commit**（owner 手动审阅提交）。收口时用 `diff /tmp/fb-baseline-status.txt <(git status --porcelain)`：差集应只有 ` M pkg/tui/render.go` 与 `?? pkg/tui/inline_code_color_test.go`，并少了两个探针；b2 的 17 个已修改文件必须与基线逐条一致（详见 §6.1）。
- 报告：根因链（§2.1）、两行改动、验证证据、遗留限制（§7）。

## 6. Done criteria（机器可查）

1. 改动范围按**相对 Step 0 基线的差集**判定（主树本来就带 b2 的 17 个已修改文件，不能要求"工作区只有我的改动"）：

```bash
git status --porcelain > /tmp/fb-final-status.txt
diff /tmp/fb-baseline-status.txt /tmp/fb-final-status.txt
git diff --stat -- pkg/tui/render.go
```

期望：差集只有两种条目——新增 ` M pkg/tui/render.go` 与 `?? pkg/tui/inline_code_color_test.go`，消失两条 `?? pkg/{tui,memory}/zz_scratch_*`；其余条目（b2 的 17 个 ` M` + 1 个 `??`）与基线逐条一致。出现基线之外的新条目、或 b2 的文件在差集里被改动 → 失败并 STOP。
2. `grep -c 'lipgloss.Color("214")).Render(text)' pkg/tui/render.go` → `0`；`grep -c 'inlineCodeColor' pkg/tui/render.go` → `3`（常量声明 1 + 使用点 1 + 新注释 1）。
3. `gofmt -l pkg/tui/render.go pkg/tui/inline_code_color_test.go` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tui` 无诊断。仓库级 `gofmt -l cmd pkg third_party` 因基线上他人未跟踪文件而**已知非空**，不算失败（见 §5 Step 1 的注意）。
4. Step 2 三个测试 PASS（测试 2 的 214 断言按 §5 Step 2 的形式：body 首行恰 1 次 214）；Step 3 反证时测试 1 与 2 必失败。
5. `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/architecture ./pkg/tool -count=1` 全绿（Step 0 记录为预存失败的用例除外，且那些必须按 owner 规则本期根因修掉或证明与本次改动无关）。
6. A/B 捕获：new 的正文 code span 是 `38;5;32`、header 仍 `38;5;214`；剥离 SGR 的文本 diff 为空。
7. tmux 真机 capture 显示 owner 报的那类卡片 body 首行与 header 不同色，header 末行末尾带耗时。

## 7. STOP conditions

- `pkg/tui/render.go` 的 `mdast.CodeSpan` 分支已不再是「写死 214」的样子（别人先改了）→ STOP 并报告现状，不猜。
- Step 0.4 的 SGR 取证显示 header 与 body 首行**不是**同一颜色 → STOP（现场与本计划的因果链不符）。
- 需要改 `pkg/tui/run.go`、`pkg/tui/notify.go`、`pkg/gateway/*`、`frontend/src/*` 才能完成 → STOP 并报告 owner（那些文件是 forebrain-harness-b2 已收工但未提交的 WIP，b2 已不在线，不得改动或 revert）。
- Step 3 反证无法让测试失败 → 说明测试没有真正锁住使用点，回到 Step 2 加锁（这是计划缺陷，不是可选项）。
- A/B 的剥离文本 diff 出现任何布局/措辞差异 → STOP，说明改动越界。
- 连续两轮修正仍无法让 §6 全绿 → 停下汇报，不扩大到 tool 层或用例层重写。

## 8. 维护注记

- 调色板规则现在有两处注释登记：`render.go:1205-1208`（`markedSpanColor`）与 `render.go:2543-2563`（卡片 accents + 本次的 `inlineCodeColor`）。任何新增"内容色"都应加进后者的注册块，并把「与 chrome 色相冲突」写进注释——这是本 bug 的唯一防线（`TestPaletteKeepsContentColoursOffCardChrome` 是它的机器门禁）。
- 本次色值 `32` 是 theme-blind 的（与其它 content 色一致：TUI 正文不按 light/dark 换色）。若将来引入主题感知的正文配色，`inlineCodeColor` 应随该方案一起变成两值；**不要**为了让它在某个主题里更好看而改回黄色系。
- web 端同一规则由 `frontend/src/assets/main.css:1987-1997` 实现（正文色 + alt 底小框）：TUI 用前景色、web 用底色块，语义都是「行内代码是正文，不是卡片 chrome」。改任一端时另一端不该被迫跟随。
- 遗留（本计划明确不修）：memory 工具 header 的 scope 回显的是调用方原始入参（模型传绝对路径时 header 会显示绝对路径，body 显示 store 相对地址）。这是「忠实回显入参」的既有契约，本次只解决颜色造成的误读；若将来要统一路径词汇，那是 `pkg/memory` + `ToolMeta` 层面的独立契约变更，需另立计划。
- 审阅者请重点看两点：(1) 是否真的只有 `mdast.CodeSpan` 一处的内容色被改（`grep 214` 分类表 §2.3）；(2) 新测试是否锁住了「使用点」而不是只锁常量（Step 3 反证记录是唯一证据）。
- 与本次改动无关但同树可见：基线工作区里有若干其它会话遗留的、处于 prunable 状态的 `plan-review-*` worktree，以及 b2 遗留的未跟踪文件 `pkg/tui/subagent_footer_display_test.go`（未格式化）。本计划不清理它们（他人的产物），必要时由 owner 统一 `git worktree prune` 并处理。交接后新会话不得把它们当作本次改动的遗留。

## 9. 评审记录（zhipuai/glm-5.3，独立 worktree 复现+实施验证）

评审结论：「approve with changes」——根因链经 SGR 级独立复现成立（HEAD 处 header 末行与 body 首行同为 `38;5;214`），并在独立 worktree 施加本计划的改动后 `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 全绿（无一测试锁住 214 code span），§2.3 的 214 穷举分类经全量 grep 核对准确，依赖（A/B 脚本参数签名、`docs/plan/` 已 ignore、基线 SHA、render.go 不在 b2 清单）全部真实。四条意见及处理：

| # | 评审意见 | 处理 |
|---|---|---|
| 1（中） | Step 2 测试 2「body 各行不含 214」按字面会失败：body 首行的 `└` 由 `paintCardCorner` 用卡片 rule 色（214）画 | 采纳。断言改为「body 首行 214 恰 1 次／`└ ` 之后无 214」，并把这条设计事实写进 §2.3，防止执行者顺手"修掉" `└`。 |
| 2（低） | 色值写错：32 是 `#0087d7`（cube 0/2/4），不是 `#0087af` | 采纳。§3 改为 `#0087d7`（`#0087af` 是 31），并注明以索引 `"32"` 为准、测试按 `38;5;32` 断言。 |
| 3（低） | §2.2 的复现输出漏掉两行 4 空格空白续行 | 采纳。比较口径改为「非空行逐行一致」，并说明省略了什么。 |
| 4（低） | Step 4 要删的 `pkg/memory/zz_scratch_address_test.go` 全文未交代来历 | 采纳。Step 4 点名两个临时探针的来历与去向。 |
| 建议 | 测试文件名用美式拼写 | 采纳：`pkg/tui/inline_code_color_test.go`。 |

**第二轮复核**（同一评审模型，复用第一轮 worktree，主树仍零改动）结论同为 approve with changes：上轮四条已落实（并核实修复后 body 首行 214 恰 1 次，闭环成立），但修订文本自身引入三处字面错误，已全部改掉：

| # | 第二轮意见 | 处理 |
|---|---|---|
| 5 | 新文件名只有 §4/Step 2 改成美式，Step 6 与 §6 Done criteria 1 仍写英式 `inline_code_colour_test.go`（§6.1 是机器可查项，按此执行会误判 STOP） | 采纳。全文统一为 `pkg/tui/inline_code_color_test.go`。 |
| 6 | `git worktree remove` 缺 `--force`，因该 worktree 含已修改的 render.go 与未跟踪测试文件，实测必败 | 采纳。命令改为 `git worktree remove --force .worktrees/plan-review-1121217519`，并注明原因。 |
| 7 | 空白续行数是**一行**不是两行（`line 4 = "    "`，`line 5 = "    > line thirteen"`） | 采纳。§2.2 括注改为「一行」。 |

**第三轮复核**（同一评审模型，只读核对，主树仍零改动、`render.go` 零改动）结论 approve with changes，一条新意见：

| # | 第三轮意见 | 处理 |
|---|---|---|
| 8 | 计划自带的 gofmt 门禁在基线上**不可能通过**：`gofmt -l cmd pkg third_party` 实测列出两个文件——本次的探针 `pkg/tui/zz_scratch_memsearch_test.go`（Step 4 才删，Step 1 检查时必在场）与 b2 遗留的未跟踪 `pkg/tui/subagent_footer_display_test.go`（§4 禁碰）。按字面执行要么误判 STOP，要么诱使执行者去格式化别人的文件 | 采纳（已实跑复核：确认这两处就是基线仅有的 gofmt 违规）。门禁收窄到本计划拥有的文件：Step 1 `gofmt -l pkg/tui/render.go`，Step 4 与 §6.3 `gofmt -l pkg/tui/render.go pkg/tui/inline_code_color_test.go`；仓库级 gofmt 降为「仅信息量、不是失败项」，并在 Step 1 写明原因。 |

评审提出的次要建议（仓库里有若干其它会话遗留的 prunable `plan-review-*` worktree，必要时由 owner 统一 `git worktree prune`）已写进 §9 末尾，不进本计划动作。

**第四轮复核**（同一评审模型，只读核对）结论 approve with changes，一条新意见（指出的是 v1 起就存在的收口口径问题，v4 只豁免了一半）：

| # | 第四轮意见 | 处理 |
|---|---|---|
| 9 | §6.1 与 Step 6 的 `git status` / `git diff --stat` 期望在基线上不可能为真：主树有 **17 个** b2 已修改的跟踪文件（` M` 条目），只豁免它的 1 个未跟踪文件不够 | 采纳（已实跑复核：`git status --porcelain` 现为 20 条 = 17 ` M` + 3 `??`）。收口口径改为**相对 Step 0 基线的差集**：Step 0.6 新增基线快照（`/tmp/fb-baseline-status.txt`、`/tmp/fb-baseline-diffstat.txt`，并断言基线里 `render.go` 未被改），§6.1 与 Step 6 改为「差集只有 ` M pkg/tui/render.go` + `?? pkg/tui/inline_code_color_test.go`，两个探针消失，其余与基线逐条一致」。这比"工作区只有我的改动"更符合并行主树的现实。 |

**第五轮复核**（同一评审模型，只读核对）结论 approve with changes，两条收尾文字：

| # | 第五轮意见 | 处理 |
|---|---|---|
| 10 | #9 的旧口径在 Step 4 期望段还残留一句「只剩 render.go 与新建测试文件」，与新的差集口径矛盾 | 采纳。Step 4 期望段改为差集表述，并指明以 §6.1 为判定依据。 |
| 11 | b2 状态描述新旧不一：§4 与 §7 仍写「正在改/peer_send 协调」，而前言已改为「已收工、WIP 留树」；b2 已不在线，peer_send 无处投递 | 采纳。§4 与 §7 同步为「已收工未提交、WIP 留树；需要动其文件时 STOP 并报告 owner」。 |

**第六轮复核**结论 **approve**（六轮累计：根因链、修复实测、全量回归、214 穷举、全部验证命令与基线核算逐条核实；本轮基线未漂移，HEAD 仍 `a1da14e4`、status 仍 20 条）。评审留下的唯一非阻塞残留是 Step 0.5/0.6 仍写「peer_send b2」，已在提交前顺手统一为「报告 owner」（:105、:148 两处 `peer_send` 是工具名——讲 peer_send 卡片的 body——不是指令，保持原样）。


评审自查补充（与我独立结论一致）：Markdown 行内其余元素（Link/AutoLink/Emphasis）无前景色，内容侧唯一颜色就是 code span 与 `markedSpanColor(114)`，改 `render.go:1757` 即穷尽；fenced code block 用 chroma 主题色，属另一语义。
评审创建的 `.worktrees/plan-review-1121217519`（含其验证测试与已施加修复的 render.go）在 Step 4 删除（未动主树）。

## 10. 验收记录（2026-11-14，执行会话 cli-961a6bcd）

### 10.1 逐项证据

| 项 | 命令/动作 | 结果 |
|---|---|---|
| 基线 | `git rev-parse --short HEAD`、`git branch --show-current` | `a1da14e4` / `dev`；`git show a1da14e4:pkg/tui/render.go` 的 §2.1(a)(b) 原文一致，`grep -c 'Color("214")).Render(text)'` → 1；基线 `git status` 20 条、`render.go` 不在其中 |
| 复现（修复前） | `go test -tags fts5 ./pkg/tui -run TestScratchMemSearchCard`（132 列） | 与 §2.2 非空行逐行一致；SGR 取证：header 末行 `3 "  \x1b[0m\x1b[38;5;214m│\x1b[0m\x1b[38;5;214m /Users/… · 0.7s\x1b[0m"`，body 首行 `4 "  \x1b[38;5;214m└\x1b[0m \x1b[38;5;214mextensions/ad_hoc/notes/…md\x1b[0m · line 14"`（214 出现 2 次：`└` + 路径）→ 因果链闭环 |
| 基线测试分类 | `go test -tags fts5 ./pkg/tui ./pkg/architecture` | `pkg/tui` ok（82.7s）；`pkg/architecture` 3 失败，全部预存：`TestFileNamesHaveAtMostTwoUnderscores`、`TestTestFilesCorrespondToProductionFiles`（由两个 `zz_scratch_*` 探针 + b2 遗留的 `pkg/tui/subagent_footer_display_test.go` 触发）、`TestSQLiteStatementsUnchanged`（`pkg/state/schema_migrations.go` 437 快照 vs 440 实测，文件无人改过，HEAD 即失败） |
| 生产改动 | `pkg/tui/render.go` | +7/−1：palette 注册块新增 `inlineCodeColor = "32"` 及注释；`mdast.CodeSpan` 分支改用 `lipgloss.Color(inlineCodeColor)` |
| 回归测试 | `go test -tags fts5 ./pkg/tui -run 'TestInlineCodeSpanKeepsItsOwnColour\|TestMemorySearchCardBodyPathDoesNotWearTheHeaderAccent\|TestPaletteKeepsContentColoursOffCardChrome' -v` | 3 PASS |
| 反证（守卫必须能失败） | 临时把使用点改回 `lipgloss.Color("214")` | 测试 1、2 FAIL，且失败输出即缺陷现场（`see \x1b[38;5;214mpkg/tui/render.go\x1b[0m now`、body 行 `└`+路径同为 214）；测试 3 仍 PASS（它只比常量）。改回后 3 PASS |
| 包级回归 | `go test -tags fts5 ./pkg/tui ./pkg/architecture ./pkg/tool` | `pkg/tui` ok（83.6s）、`pkg/tool` ok（11.2s）；`pkg/architecture` 仍为上述 3 个预存失败，其中命名两条现在**只剩 b2 的未跟踪文件**（我的探针已删） |
| gofmt/vet | `gofmt -l pkg/tui/render.go pkg/tui/render_test.go`、`go vet -tags fts5 ./pkg/tui` | 无输出 / 无诊断；仓库级 `gofmt -l cmd pkg third_party` 只剩 b2 的未跟踪文件（已知非空，不是失败项） |
| SGR 级 A/B（卡片正文 code span） | `build-binary.sh old/new` + `drive-card.sh`（256 色，`intermediate_tool` 的 Markdown note） | old：`214: 5, 32: 0`，body 行 `  \x1b[38;5;214m└\x1b[39m Found that \x1b[38;5;214mpkg/tui/render.go\x1b[39m …`；new：`214: 4, 32: 1`，同行 code span 变 `\x1b[38;5;32m`。剥离 SGR 的文本 diff 只有 scratch 工作目录路径（`…/old/proj` vs `…/new/proj`），无布局/措辞差异 |
| 真机（owner 那张卡） | tmux 里用 old/new 各跑一次真实 `memories_search`（fake 模型，store 内预置一条含 `provider+model` 的 note），`capture-pane -e` | old body 首行 `  \x1b[38;5;214m└\x1b[39m \x1b[38;5;214mextensions/ad_hoc/notes/2026-11-14T00-00-00-provider-note.md\x1b[39m · line 4`（整卡 214=9、32=0，body 与 header 同色）；new 同行 code span `\x1b[38;5;32m`（214=8、32=1，`└` 仍是卡片 rule 色）。剥离 SGR 的 diff 只有 scratch 路径与 `⇄ 1/⇄ 2` 请求计数 |
| 范围判定（差集） | `diff /tmp/fb-baseline-status.txt <(git status --porcelain)` | 新增 ` M pkg/tui/render.go`、` M pkg/tui/render_test.go`；消失 `?? pkg/memory/zz_scratch_address_test.go`、`?? pkg/tui/zz_scratch_memsearch_test.go`；b2 的 17 个 ` M` + 1 个 `??` 逐条不变 |

### 10.2 执行偏离（两处，均记录证据）

1. **测试落位改为 `pkg/tui/render_test.go`（不是新建 `inline_code_color_test.go`）。** 计划给的文件名会被仓库自己的架构门禁判为违规：`pkg/architecture/cache_test.go:1445-1520` 的 `TestFileNamesHaveAtMostTwoUnderscores` 要求文件名（含 `_test`）最多两个下划线，`TestTestFilesCorrespondToProductionFiles` 要求 `X_test.go` 旁边必须有 `X.go`。`inline_code_color_test` 是 3 个下划线且没有 `inline_code_color.go`，会新增两条失败——正是 §6.3/§6.5 要求「除已分类预存失败外全绿」不允许的。三个测试的断言、名称、意图全部照搬，落进覆盖 `render.go` 的那个测试文件（`pkg/tui/render_test.go`，不在 b2 的改动清单里）。没有为通过门禁去改门禁、或去格式化/删除别人的文件。
2. **A/B 的「正文 code span」路线用 `intermediate_tool` 而不是 `memories_add_ad_hoc_note`。** `intermediate_tool` 的 body 走同一个 Markdown 渲染（`noteToolMarkdownBody`），且不需要审批，能稳定拿到 code span；owner 那张 memory search 卡另用真实 `memories_search` 在 tmux 里单独 A/B（见 §10.1 最后两行），两条路线都拿到 code span，故未触发 STOP。

### 10.3 遗留（不改，且非本次引入）

- `pkg/tui/subagent_footer_display_test.go`（并行会话 `forebrain-harness-b2` 的未跟踪文件）使两条架构命名门禁保持红；按 §4/§7 未触碰。
- `TestSQLiteStatementsUnchanged` 在 HEAD 即失败（437 vs 440，缺的是 `pkg/state/schema_migrations.go` 里 `UPDATE fb_llm_models SET cap_images = NULL …` 三条语句的快照），与本改动无关；重新生成快照是有意变更，留给 owner 决定。
- 构建 old 侧时 `.worktrees/ab-head/third_party` 缺 `libtokenizers`/`pdfium`，需先 `ln -s ../../../third_party/{libtokenizers,pdfium}` 才能链接（技能脚本没做这一步）；`cleanup.sh` 结尾的 `git worktree prune` 顺手清掉了若干其它会话遗留的 prunable 注册（目录本已不存在，未删任何文件）。


