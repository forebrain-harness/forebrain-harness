# TUI slash 面板 footer 键词全小写清扫

> **Status: REJECTED（2026-11-25 execute 前置核查证伪；owner 裁决 A＝不做，源码零改动）**
> 计划的前提「存量 footer 仍是大写键词、与原型不一致」不成立：需求早已由**渲染层单一机制**实现，
> 见文末 §Verdict。以下正文保留为起草时的记录，不再执行。
>
> ~~2026-11-25 起草；owner 裁决的范围 A＝严格只改 footer hint 行；前置 377+374 已落 dev `3a261297`。
> 派发执行须 owner 审批，且与 forebrain-harness-4c 的 pkg/tui 网络报错修复错峰——见协调节。~~

## Context（为什么做）

owner 2026-11-24 裁决：**TUI 端所有 slash 命令面板 footer 统一为全小写**。plan 377（Allowed tools picker）
已落地首个全小写 footer（`↑/↓ to navigate space to toggle enter to save esc to cancel`），但存量 footer 仍是
大写键词（`Enter to confirm Esc to go back` 等），与原型（`plans/config-tui-prototype.html` 的 `footerHTML` 早已全小写）
不一致。本计划把存量 footer hint 行的键词清扫为全小写，使 TUI 与原型逐字一致。

**范围裁决（owner 2026-11-25，选项 A）**：**严格只改面板底部 footer hint 行**。问题/确认/反馈提示行里嵌入的键词
（如 `(Esc to go back)`、`Y to confirm N or Enter to decline`、`Enter to send, Esc to refuse`）**不在本计划范围**，保持现状。
注释里的键词散文也不改。

## Baseline 与漂移检查

- 起草基线：`dev` @ `3a261297`（＝377 `9c964e8a` + 248 `aad6fb9d` + 374 `3a261297`）。执行 worktree 从 dev HEAD 出发；
  若 dev 再前进（4c 的修复等），从新 HEAD 出发并重跑漂移检查。
- 漂移锚点（内容不符 = STOP；行号仅参考）：
  - `pkg/tui/overlays.go`：`func (p *richPickerState) footerHint() string` 内非 multi 分支
    `hint = "↑/↓ to navigate Enter to " + p.keys.Enter + " Esc to " + p.keys.Esc`；`selectFooterHint`（multi 返回
    `"↑/↓ to navigate Space to select Enter to confirm "`）；multilineEditorPanel 的 `"Ctrl+Enter to save"` 提示。
  - `pkg/tui/render.go`、`run.go`、`setup.go`、`tabbed_picker.go`、`project_servers.go`、`workspace_trust.go` 的 footer hint 字串（见清单）。
  - 原型 `footerHTML` 全小写串（目标对照）：`↑/↓ to navigate enter to confirm esc to cancel`、
    `↑/↓ to navigate enter to open esc to close`、`↑/↓ to navigate enter to open esc to go back`、
    `↑/↓ to navigate enter to save esc to cancel`、`↑/↓ to navigate space to toggle enter to save esc to cancel`、
    `enter to save esc to cancel`、`esc to go back`、`↑/↓ to navigate enter to open esc to cancel s to save`。

## 变换规则（机械、唯一）

在** footer hint 字串**里，把键名 token 改全小写，动作词与箭头不变：
`Enter→enter`、`Esc→esc`、`Space→space`、`Tab→tab`、`Ctrl+Enter→ctrl+enter`、`Ctrl+U→ctrl+u`、
`PgUp→pgup`、`PgDn→pgdn`。`↑/↓` 箭头保留；动作词（confirm/open/save/cancel/go back/close/run/complete/scroll/
select/toggle/switch/decline all/details/submit/when signed in/for a new line/for details/or）本已小写，不动。
动态模板里硬编码的键名（如 `"↑/↓ to navigate Enter to " + p.keys.Enter`）改小写键名，`p.keys.Enter/Esc` 的**值**
（save/cancel/open 等）本已小写，不动。有原型对照的 footer 以原型逐字为准。

**不改**：键词出现在非 footer 的提示/确认/反馈行、注释、标题、描述、错误消息、model-visible 文本。
377 已落地的 multi 全小写分支不动（清扫后非 multi 分支与之同款）。

## 范围内清单（footer hint 行；执行时按内容重 grep 定位，行号仅参考）

- `pkg/tui/overlays.go`：
  - `selectFooterHint`：`Esc to cancel`（esc 默认）、`↑/↓ to navigate Space to select Enter to confirm `（multi）、
    `↑/↓ to navigate Enter to confirm `（single）。
  - memorySettings footer：`↑/↓ to navigate Enter to confirm Esc to go back`、`↑/↓ to navigate Space to toggle Enter to save Esc to cancel`。
  - 滚动/编辑 footer：`↑/↓ PgUp/PgDn to scroll Enter or Esc to close`、`Enter to `（编辑提示）、
    `Ctrl+Enter to save`、`Ctrl+Enter to ` + ` Enter for a new line Esc to `（multiline，含 Keys 分支）、
    `Enter to confirm `、`↑/↓ to navigate Enter to confirm `（picker）。
  - `footerHint` 非 multi 分支：`↑/↓ to navigate Enter to ` + keys.Enter + ` Esc to ` + keys.Esc。
- `pkg/tui/render.go`：`Esc to go back`/`Esc to quit`/`Esc to cancel`（view esc footer，多处）、
  `Esc to close Tab to switch PgUp/PgDn to scroll`、`Esc to close`、`↑/↓ to navigate Enter to open Esc to close`、
  `↑/↓ to navigate Enter to confirm Esc to go back`、`↑/↓ to navigate Enter for details Esc to go back`、
  `PgUp/PgDn to scroll Esc to go back`、`↑/↓ to navigate Enter to run Esc to go back`。
- `pkg/tui/run.go`：`↑/↓ to navigate Tab to complete Enter to run Esc to close`。
- `pkg/tui/setup.go`：`Enter to submit Esc to cancel`、`Esc to cancel`、`Enter when signed in Esc to cancel`（sign-in 页 footer，多处）。
- `pkg/tui/tabbed_picker.go`：`↑/↓ to navigate Enter to open Esc to close`、`Esc to cancel`、`Tab to switch`、`Space to select`、`Enter to confirm`。
- `pkg/tui/project_servers.go`：`↑/↓ to navigate Space to select Enter to confirm Esc to decline all`。
- `pkg/tui/workspace_trust.go`：`Esc to quit`。

## 范围外（明确不改，owner 裁决 A）

- 问题/选择提示行：overlays.go `Which reasoning effort … (Esc to go back)`、`Which model … (Esc to go back)`（×3）。
- 反馈/拒绝提示行：overlays.go `Feedback … (Enter to send, Esc to refuse …)`、`No (Enter to refuse …)`。
- 确认提示行：overlays.go `Y to confirm N or Enter to decline`、`Y or Enter to confirm N to decline`。
- 注释：config_panel.go editAllowedTools 注释、input_events.go macOS Option/Alt+Enter 注释、overlays.go/render.go 若干散文注释。
- web 端、gateway、model-visible 文本、错误消息。

## 测试同步

- grep 全部 `pkg/tui/*_test.go` 里被改字串的断言（已知至少 `chat_session_test.go`、`overlays_test.go`、`setup_test.go`；
  另查 `tabbed_picker_test.go`、`config_panel_test.go`、`project_servers_test.go`、`workspace_trust_test.go`、render 相关测试），
  把断言里的旧大写串改为新小写串。**只改断言期望值，不改测试逻辑**。
- 377 的 `TestConfigPanelAllowedToolsPagePaintsCheckBoxes` 已断言全小写 footer，不动。

## Steps（有序，每步带检查）

0. worktree 原生依赖接线（照 377：符号链接主树 `third_party/{libtokenizers,pdfium}`、`frontend/node_modules`）。
   检查：`PKG_CONFIG_PATH=… CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestNothing -count=1` 过（会 link 的命令）。
1. 按清单与变换规则改 7 个非测试文件的 footer 字串。检查：`go build -tags fts5 ./pkg/tui` 过。
2. 同步测试断言。检查：`go test -tags fts5 ./pkg/tui -run 'TestSelect|TestRich|TestTabbed|TestConfigPanel|TestSetup|TestRender|TestMemory|TestProject|TestWorkspace' -count=1` 全绿。
3. 门禁：`gofmt -l pkg/tui` 空；`go vet -tags fts5 ./pkg/tui` 净；`go test -tags fts5 ./pkg/tui -count=1` 全包绿。
4. 原型回归：`node plans/config-tui-prototype.test.cjs`（需 8765 静态服务器供 plans/）全过、browserErrors=[]
   （原型本已全小写，应零改动仍绿——若需改原型说明 TUI 与原型仍有出入，停下核对）。
5. 真机抽查（run-forebrain 配方）：/model、/config 某 section 页、memory settings、sign-in 页 footer 全小写；`capture-pane -e` 留证据。
6. 落地：照本仓 execute 标准 git 工作流（worktree 分支提交 → 门禁绿 → ff-only 落 dev；dev 前进先 rebase 重跑门禁；不动他人 WIP）。
   **执行前 peer_send 通告 forebrain-harness-4c / ad / 76**（pkg/tui 共享）。
7. 收尾（评审方/主树 docs）：README 378 行状态翻 DONE＋落地 SHA；本计划补 Implementation Status。

## Done criteria

- [ ] 清单内 footer 字串全小写；范围外（提示/确认/反馈/注释）零改动（`git diff` 逐行核对）。
- [ ] `go test -tags fts5 ./pkg/tui -count=1` 全包绿；gofmt 空；vet 净。
- [ ] 原型回归全过、browserErrors=[]（原型零改动）。
- [ ] 真机抽查 footer 全小写有 capture 证据。
- [ ] 范围合规：`git diff --stat` 只含 7 个非测试文件 + 相关测试文件。
- [ ] 无死件（纯字串替换，不新增标识符）。

## STOP 条件

- 漂移锚点内容不符。
- 某 footer 字串无法判定是否 footer（边界模糊）→ 停下问 owner，不即兴扩范围。
- 同一验证两次修不绿 → 停下报告。
- 需改范围外文件（提示/确认行、web、gateway）→ 停下问 owner。
- 与 4c 的 pkg/tui 在途改动冲突 → 协调错峰，不 stash/discard。

## 协调 / 维护注

- **forebrain-harness-4c** 正在 pkg/tui 查「网络报错重复显示」bug（可能动 render.go/run.go/chat 文件）——378 执行须待其落地或与其错峰；
  派发前 peer_send 确认。本计划起草（docs）不与其冲突。
- 374 刚改 setup.go（sign-in footer 在范围内）——基线已含 374，执行从新 dev HEAD 出发即可。
- 键词大小写契约：原型 `footerHTML` 是全小写权威；本清扫后 TUI footer 与原型逐字一致。提示/确认行键词大小写仍按现状（owner 裁决 A 不动）。
- 纯机械字串替换，无行为/逻辑变化；评审重点＝范围合规（只改 footer 行）与测试断言同步。

## Verdict（2026-11-25，execute 前置核查 → REJECTED；owner 裁决 A）

**未派发执行器，源码零改动。** 计划的前提在 dev `3a261297`（＝起草基线，`git status --porcelain pkg/tui` 为空）上被证伪：
footer 的全小写**早已由渲染层的单一机制实现**，与源码字面量的大小写无关。

- `pkg/tui/render.go:8304`：`func (b *panelBuilder) hint(text string) { b.hintText = strings.ToLower(text) }`，
  由提交 `6cd9140c`「style(ui): lowercase footer key hints」（2026-11-22，dev 祖先）引入，
  提交正文即声明「One casing rule for every key hint」。
- 面板 hint 的唯一出口是 `b.hintText`（`pkg/tui/render.go:8206`：`slashPanel{… hint: b.hintText}`），
  所以清单内 45 处字面量虽然写的是 `Enter`/`Esc`/`Space`/`Tab`/`PgUp`/`PgDn`/`Ctrl+Enter`，绘出时一律小写：
  `overlays.go:5158/5915/6067/6429/4224`（含 `page.Hint`）、`tabbed_picker.go:327`、
  `project_servers.go:136`、`workspace_trust.go:94 → reviewWith`、`render.go:8456-8935`、`run.go:3883`、
  `setup.go:1620/1624/1628/2123`。
- 不走 `b.hint` 的暗色 footer（`writeLine("\x1b[2m…")`）在同一次 `6cd9140c` 里已改成小写字面量：
  `overlays.go:967-979`、`3281-3511`、`3766`。
- 不是推断，是实跑：九条字节级 golden 测试在 dev `3a261297` 全绿，断言的都是全小写 footer——
  `TestTabbedPickerPanelGolden`（整块 `reflect.DeepEqual`，期望 `tab to switch ↑/↓ to navigate enter to confirm esc to cancel`）、
  `TestRichPickerLaysRowsInTwoColumns`、`TestPickersShowEveryRowWhole`、`TestSlashMenuPanelGroupsCommandsAndSkills`、
  `TestRawSelectorSelectRendersPanel`、`TestProjectServersPanelGolden`、`TestWorkspaceTrustPanelPaintsTheChosenPicture`、
  `TestConfigPanelAllowedToolsPagePaintsCheckBoxes`、`TestComposerVariantsStayWithinOnePhysicalRow`
  （`go test -tags fts5 ./pkg/tui -run '<上述九条>' -count=1` → `ok … 0.748s`，逐条 `--- PASS`）。

按原计划执行只剩源码字面量 churn：零可见变化，Step 5「真机抽查 footer 全小写」在改动前后同样成立、无法区分前后，
Done criteria 也就无法证明任何用户价值。

owner 2026-11-25 裁决 **A＝不做**（另两个备选一并否决：B＝仍清扫字面量、`ToLower` 留兜底；C＝清扫并删 `ToLower`，
让字面量成为唯一来源——将来任何传入大写内容的调用点会立刻在屏幕上显示大写）。

维护注：将来若有人想删 `render.go:8304` 的 `strings.ToLower`，必须先把 45 处字面量全部改成小写（即本计划的正文），
并全量核对调用者；否则 footer 会重新变大写。提示/确认/反馈行（`Which model … (Esc to go back)`、
`Y to confirm N or Enter to decline` 等）按 owner 裁决 A 仍保持现状，且它们经 `b.hint` 绘出时同样是小写。