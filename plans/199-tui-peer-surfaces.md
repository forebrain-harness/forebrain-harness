# 199 — TUI 呈现：来信卡、发信卡与投递状态、挂起放行、`/peers`、git 护栏审批原因

- **基线**：`db56121f`（dev；194–198 与 209–212 均已落地）。
- **状态**：DONE（2026-11-03 执行，评审 APPROVE；worktree `.worktrees/199` 分支 `exec/199`，6 笔提交，未合并）
- **依赖**：196、197（198 落地后补审批原因文案）
- **执行隔离**：worktree + `exec/199` 分支 + 增量提交（owner 2026-11-03 裁定）；合并在 owner 批准后 ff-only
- **设计全文**：`docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md` §5、§6

> **执行者须知**：逐步执行，每步跑验证；碰到 §9 STOP 条件就停下报告。完成后更新 `plans/README.md` 本行状态。交互改动必须用 `run-forebrain` 技能真机确认（见 §4 第 6 步），不能只看单元测试。
>
> **Drift check（先跑）**：`git diff --stat db56121f -- pkg/tui/commands.go pkg/tui/overlays.go pkg/tui/notify.go pkg/turn/slash.go pkg/tool/header_parts.go pkg/tool/testdata/header_actions.json ai-elements-vue/packages/elements/src/tool/headerWords.ts pkg/architecture/cache_test.go`，应无输出；有输出就 STOP。
>
> **2026-11-03 reconcile（相对 202ed4f2 的两处漂移，已在本文件原地改正）**：
> 1. `pkg/tui` 生产文件数已达 **31/31**（上限 `maxProductionFilesPerPackage = 31`，`pkg/architecture/cache_test.go:1514`；190 的 `config_panel.go`/`config_panel_schema.go` 与 `skills_versions.go` 占掉了余量）。**不得新建 `pkg/tui/peers.go`**——原第 3 步的新文件改为落在 `pkg/tui/overlays.go`（`questionOverlay`（`:2159`）+ `newQuestionOverlay`（`:2200`）就是「复用 `rawSelector`、不碰审批逻辑」的现成先例）与 `pkg/tui/commands.go`；测试进同名 `_test.go`（`TestTestFilesCorrespondToProductionFiles` 要求测试文件与生产文件同名）。
> 2. 中文动作词表已迁到 `ai-elements-vue/packages/elements/src/tool/headerWords.ts`（`HEADER_ACTION_ZH` 在 `:14`）；`frontend/src/locales/toolHeader.ts` 现在只是 `export … from '@repo/elements/tool/headerWords'` 的转出面。加词改在 `ai-elements-vue` 那份；`frontend/src/locales/toolHeader.test.ts` 读 `pkg/tool/testdata/header_actions.json` 金标，缺词即红，无需改测试。

## 0. 背景与 owner 裁决（全族共用，执行期不得再改）

owner 2026-10-30：多个代理跨进程互通、空闲自动唤醒、线协议与 Claude Code 互通、权限模式不一致的来信**先挂起、终端里问用户放行或拒绝**（Claude Code 原话：terminal 会话会就地询问）、危险 git 在有同树代理时必须审批。**TUI 语义是标准，网关向 TUI 看齐**（200 照本计划做）。UI 文案简洁，能用标签就不写句子；TUI 文案用英文（与现有 TUI 一致）。

本计划只做 TUI 呈现与交互；引擎行为已在 196–198 落地。

## 1. 共享架构（摘要）

- 来信＝user 行，`parts` 里 `is_meta` + `origin:peer`，内容是信封原文 `<cross-session-message … from-name="X" …>正文</cross-session-message>`；投递状态汇总＝user 行 `origin:peer_status`，内容是 `<system-reminder>Peer message delivery update: …</system-reminder>`。
- 挂起：运行事件 `peer_message_held{MsgID, From, FromName, Preview, Cause, HeldAtMs}`、`peer_message_settled{MsgID, Outcome}`；放行/拒绝调 `env.ReleasePeerMessage(msgID)` / `env.DenyPeerMessage(msgID)`；列出 `env.HeldPeerMessages(sessionID)`（196）。
- TUI **不得** import `pkg/peer`（扇出 19 已满）：信封解析用 `pkg/process` 导出的薄包装 `process.ParsePeerEnvelope(content) (fromName, body string, ok bool)`（本计划在 `pkg/process/peers.go` 加，内部调 `peer.Parse`）。

## 2. 现状事实（已对基线核实）

- 回放里带 origin 的 user 行：`pkg/tui/commands.go:288-292`——`origin != ""` 时渲染成 `Message{Kind: MsgKindSystem, Title: origin, Content: content}`。实时路径里开轮时的 user 行也走 origin（心跳先例），执行者用 `grep -n 'MessageOrigin' pkg/tui` 找全。
- 审批浮层：`pkg/tui/overlays.go:37` `approvalOverlay`、`:189` `newApprovalOverlay`、`:209` `buildOverlayChoices`、`:370` `Run()`；同文件 `:2159` `questionOverlay` + `:2200` `newQuestionOverlay` 是「小构造函数复用 `rawSelector`」的现成先例，挂起浮层照它写。
- 斜杠命令表：`pkg/turn/slash.go:77` `var commands = []Command{…}`，每项 `{Name, Description, AllowedSurfaces, SupportsInlineArgs, Visibility, SubagentView}`（例：`:96` `lsp`）。TUI 的分发在 `pkg/tui/chat_slash.go`。
- 卡头动作词单一来源：`pkg/tool/header_parts.go:425` `HeaderActions()`，golden `pkg/tool/testdata/header_actions.json`；中文词表在 `ai-elements-vue/packages/elements/src/tool/headerWords.ts:14` `HEADER_ACTION_ZH`（`frontend/src/locales/toolHeader.ts` 只是转出面），测试 `frontend/src/locales/toolHeader.test.ts` 按金标逐词覆盖。
- 文件数：`pkg/tui` **31 个生产文件，已达上限 31**（`pkg/architecture/cache_test.go:1514`）——本计划不新增生产文件。

## 3. 设计

### 3.1 来信卡（实时与回放同一渲染）

- `origin:peer` 的 user 行：`ParsePeerEnvelope` 成功 → `Message{Kind: MsgKindSystem, Title: "↘ " + fromName, Content: body}`；失败 → 标题 `↘ peer`、内容原文。
- `origin:peer_status`：标题 `Delivery`，内容去掉 `<system-reminder>`/`</system-reminder>` 两行与首行 `Peer message delivery update:` 后的列表。
- 两者都**不**显示成「你发送的」用户卡。

### 3.2 发信卡

- `HeaderActions()` 加 `"Messaged", "Messaging", "Failed to message", "Listed agents", "Listing agents", "Failed to list agents"`；`DisplayHeaderParts` 对 `peer_send`：动作 `Messaged`/`Messaging`/`Failed to message`，目标＝`to`；对 `peer_list`：动作 `Listed agents` 等、无目标。更新 golden。
- 前端 `HEADER_ACTION_ZH` 同步加词（纯词表，不动任何组件）：`Messaged→已发消息`、`Messaging→正在发消息`、`Failed to message→发消息失败`、`Listed agents→已列出代理`、`Listing agents→正在列出代理`、`Failed to list agents→列出代理失败`。
- 卡片正文：`peer_send` 显示消息正文前 3 行（超出折叠，沿用现有工具卡折叠）；结果行（`Sent to X (busy).`）作为卡片尾注。

### 3.3 挂起放行

- 收到 `peer_message_held`（当前会话）→ 在输入框上方弹一个与审批同风格的浮层（复用 `approvalOverlay` 的选择器部件，**新建一个小构造函数**，不改审批逻辑），内容：

```
Message from <FromName> is held
<Preview 前 3 行>
<cause 标签>
› Release   Deny
```

  `cause` 标签：`mode-mismatch → permission modes differ`、`no-mode-asserted → sender mode unknown`、`hold-all → holding all peer messages`。
- 选 Release → `env.ReleasePeerMessage(msgID)`；Deny → `env.DenyPeerMessage(msgID)`；Esc → 浮层收起、消息仍挂起（之后可在 `/peers` 里处理）。
- 浮层与工具审批同时出现时，工具审批优先；挂起浮层排在后面。
- `peer_message_settled` 到达且浮层还开着（例如超时被淘汰）→ 关浮层并打一行系统消息 `Held message from <name> <released|denied|expired>`。

### 3.4 `/peers`

- `pkg/turn/slash.go` 加 `{Name: "peers", Description: "agents on this machine you can message, and held messages", AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, SupportsInlineArgs: false, Visibility: VisibilityPublic, SubagentView: SubagentViewGlobal}`。
- TUI：打开一个列表面板（沿用现有 picker 面板），上半部分是与 `peer_list` 同样的三段内容（数据从 `PeerDirectory.List` 取，调用身份＝当前会话主代理），下半部分是挂起消息，选中后 Enter 弹 §3.3 的浮层。面板只读，不提供「发消息」入口（发消息是代理的事）。
- 少于 8 项不显示过滤框（TUI picker 规则）。

### 3.5 状态行与 git 护栏原因

- 状态行：当前会话工作树里有其它在线代理时，在现有状态行末尾加 `⇄ <N>`（N＝同树对端数）；为 0 时不显示。数据每 5s 从 `PeerDirectory.WorkTreePeers` 取一次，取不到就不显示。
- 审批卡：`approval_reason == "peer_work_tree"` 时，原因行显示 `Other agents in this tree: <peer_names 用逗号连接>`，规则名（`dangerous_rule`）沿用现有显示。

## 4. 步骤

1. **薄包装与卡头词**：`pkg/process/peers.go` 加 `ParsePeerEnvelope`；`pkg/tool/header_parts.go` 加词与 `peer_send`/`peer_list` 的分支，更新 `testdata/header_actions.json`；`ai-elements-vue/packages/elements/src/tool/headerWords.ts` 的 `HEADER_ACTION_ZH` 加 6 个词（**不要**改 `frontend/src/locales/toolHeader.ts`，它只是转出面）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/process -count=1` → ok；`cd frontend && pnpm vitest run src/locales/toolHeader.test.ts` → pass。
2. **来信卡与状态卡**：改 `pkg/tui/commands.go`（回放）与实时渲染处。用例（参照心跳 origin 的现有渲染用例）：peer 行渲染标题 `↘ X`、正文是信封正文；坏信封回退；peer_status 行渲染 `Delivery`。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Peer|Origin|Replay' -count=1` → ok。
3. **挂起浮层**：**不新建文件**（`pkg/tui` 已 31/31）。浮层构造与 `/peers` 面板写进 `pkg/tui/overlays.go`（照 `questionOverlay`/`newQuestionOverlay` 的形状：自带小构造函数、复用 `rawSelector`、不改 `approvalOverlay` 任何逻辑），事件分发接在 `pkg/tui/chat_turn.go` 或现有 `peer_message_*` 落点，状态行计数写进状态行所在文件。在事件分发处接 `peer_message_held`/`peer_message_settled`。用例写进对应的 `overlays_test.go`/`commands_test.go`：held 事件 → 浮层出现；Release/Deny 调到假 env 的对应方法；工具审批与挂起同时出现时审批先显示。
   验证：同上包 ok。
4. **`/peers` 命令**：`pkg/turn/slash.go` 加表项；`pkg/tui/chat_slash.go` 分发到面板。若网关侧对未实现的命令有「surface 不支持」的测试，按现有约定处理（200 会实现网关侧）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/tui ./pkg/gateway -count=1` → ok。
5. **状态行与审批原因**：改状态行组装处与审批卡原因渲染处 + 用例。
   验证：`./pkg/tui` ok。
6. **真机确认（必须）**：按 `run-forebrain` 技能的方法构建并在 tmux 里起两个 TUI（不同 scratch home、相同的四个覆盖变量 `FOREBRAIN_PEERS_DIR`/`FOREBRAIN_PEER_SOCKET_DIR`/`FOREBRAIN_PEERS_CC_DIR`/`FOREBRAIN_PEER_CC_SOCKET_DIR`、同一临时项目目录；**与 Claude Code 互通的开关保持默认关闭**——forebrain 之间的界面必须在关闭状态下全部成立；两个 TUI 的开关必须一致，开关不同的进程互相看不见），fake provider 驱动：
   - A 的模型调用 `peer_send` 给 B → A 出现 `Messaged <B>` 卡；B 空闲时自动开一轮，B 屏上出现 `↘ <A>` 卡。
   - 把 B 切到 Full Access（`/permissions`），A（Default）再发一条 → B 弹挂起浮层；Release 后 B 开轮；A 屏上出现 `Delivery` 卡含 released 一行。
   - B 里 `/peers` 能看到 A；状态行出现 `⇄ 1`。
   - 截屏（技能里的 capture 方法）贴进执行记录。
7. **收口**：`go vet ./...`；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`；`cd frontend && pnpm vitest run src/locales`。

## 5. 边界

- **只动**：`pkg/tui/commands.go`、`pkg/tui/chat_slash.go`、`pkg/tui/overlays.go`（只加构造函数）、TUI 事件分发与状态行所在文件、`pkg/turn/slash.go`、`pkg/tool/header_parts.go`、`pkg/tool/testdata/header_actions.json`、`pkg/process/peers.go`（薄包装）、`ai-elements-vue/packages/elements/src/tool/headerWords.ts`（只加词）及以上测试。
- **不动**：引擎投递/挂起逻辑（196）、工具行为（197）、任何前端组件（200）、`frontend/src/locales/toolHeader.ts`（转出面，无需改）。
- TUI 不 import `pkg/peer`。
- **不新增 `pkg/tui` 生产文件**（31/31 已满）。

## 6. 完成判据（机器可查）

1. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全绿；`go vet ./...` 无输出；`cd frontend && pnpm vitest run src/locales` 通过。
2. `grep -rn 'forebrain-harness/pkg/peer"' pkg/tui --include='*.go'` 无输出。
3. `ls pkg/tui/*.go | grep -v _test | wc -l` = 31（不得新增生产文件）。
4. 第 6 步真机截屏与说明贴进执行记录。
5. `plans/README.md` 本行状态更新。

## 7. 测试计划

见 §4。渲染用例参照 `pkg/tui/commands_test.go` 的回放用例；浮层用例参照 `pkg/tui/overlays_test.go` 的审批浮层用例。

## 8. 维护注意

- 200（网页）必须 1:1 对齐这里的语义：来信卡、状态卡、挂起放行、`/peers`、审批原因。
- 挂起浮层复用审批选择器的部件，审批浮层的改动要顺带检查这里。

## 9. STOP 条件

- 回放与实时渲染对 origin 的处理不在同一函数族、要改 4 个以上文件才能让两边一致。
- `approvalOverlay` 的选择器部件无法在不改审批逻辑的前提下复用。
- `pkg/tui` 生产文件数会超过 31（即：挂起浮层/`/peers` 面板无法在 `overlays.go`/`commands.go` 现有文件里落位）。

## 10. 执行记录（2026-11-03，评审 APPROVE）

- **提交**：`1cfd0b0e`（步骤 1 薄包装 + 卡头词）、`dcc76e6e`（步骤 2 来信卡/状态卡）、`5d665e32`（步骤 3 挂起放行）、`eda58d1a`（步骤 4 `/peers`）、`f07b0e14`（步骤 5 状态行 + 审批原因）、`86d28e43`（评审 REVISE：`peer_send` 卡正文）。
- **真机**：两个 TUI（不同 scratch home、同一临时项目、四个 peer 覆盖变量一致、CC 开关默认关）跑通 A→B 互发、B 空闲自动开轮、`↘` 卡、`/peers` 面板、状态行 `⇄ 1`、挂起浮层 Release/Deny。
- **评审 REVISE 的一项**：§3.2 第三条（`peer_send` 卡正文＝消息前 3 行 + `Sent to X (busy).` 脚注）原执行未实现也未报告。执行器补做时发现 **`summaryToolBody` 不是 viewport 全幅渲染的活路径**（`renderBlockFull` → `renderFrameLinesWithAgents` 把 `fullBodyMode` 置真，`renderCompactFrame` 因此跳过它），已把 `peer_send` 一并接进全幅分支，并用 `renderFrameLines` 的测试证明读者实际看到的是塑形后的卡。
- **留给 196/197 的三个引擎侧观察**（不在本计划范围，200/201 需接手）：
  1. **回执竞态**：`peerOutbound.Send` 先发送（`pkg/process/peer_outbound.go:367`）后登记（`:374`），回执若先到会被 `receipt` 丢弃（`:561`），本地往返 <0.1s 时 `Delivery` 卡因此不出现。
  2. **挂起浮层 Preview 是原始信封**：`PeerMessageHeldPayload.Preview` 装的是 `peerPreview(envelope)`（`pkg/process/peer_inbound.go:594`），浮层显示 `<cross-session-message …>` 而非正文。（200 已补 `Body`/`PeerBody` 修掉。）
  3. **登记记录的模式不随权限刷新**：`/permissions → Full Access` 后 `modeFor` 按实时模式判定挂起，但发布出去的 agent 块仍是 `prompting`，于是按 socket 寻址不挂起、按名字寻址才挂起。

### 10.1 201 真机揪出的两个缺陷（已修，提交在 `exec/201`）

1. **空闲 TUI 收到对等消息后不自动开轮**（`4789773a`）：本计划步骤 3 新建的 `idleWake()` 只返回 `autoContinueWake() || heldPeersHeld()`，漏了 `peerDue`。`peerDue` 由 196 的 `handlePeerWakeNotification` 设置、由空闲循环的 `takePeerDue()` 消费，两边没接上，空闲循环永远不醒。设计 §3.1/§3.9 要求「收件方空闲时自动唤醒开一轮」。修法：`idleWake()` 加 `|| s.peerDue != nil` + 回归测试。
2. **对等输入投进主代理回合会 panic**（`5cb91311`）：`queuedSubmissions`（`pkg/tui/run.go`）对队列里每个 `run.Input` 做 `in.Payload.(ComposerSubmission)`，未检查 `Payload` 是否为 nil；而 `process.peerInput` 与 `deliverNote` 构造的 `run.Input` 都不带 `Payload`。一条对等消息被 `Steer`/`FollowUp` 投进正在运行/结束的回合，该回合在工具边界 drain delivered 列表时 panic 退出。修法：`queuedSubmissions` 跳过非 `ComposerSubmission` 的输入；`takeDeliveredSteersForDisplay` 一次排空分回两半；新增 `renderDeliveredInputCards` 用 `peerOriginCard` 画对等投递。

这两个缺陷都是本计划（199）引入或未覆盖的：①出在本计划新建的 `idleWake`，②出在本计划未覆盖的「对等消息投递进运行中回合」路径（196 的 `peerDue`/`peerInput` × 本计划的 delivered-drain 渲染）。
