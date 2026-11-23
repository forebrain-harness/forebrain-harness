# 200 — 网页呈现：来信卡、投递状态卡、挂起放行、`/peers`、审批原因（先原型，owner 确认后实施）

- **基线**：`86d28e43`（`exec/199`，199 六笔提交之后；199 尚未合并入 dev）。
- **状态**：DONE（2026-11-03 执行，评审 APPROVE；worktree `.worktrees/200` 分支 `exec/200`，4 笔提交，未合并）
- **依赖**：199（TUI 语义是标准）
- **执行隔离**：worktree + `exec/200` 分支 + 增量提交（owner 2026-11-03 裁定；199 未合并，故本分支从 `exec/199` 起）；合并在 owner 批准后 ff-only
- **设计全文**：`docs/plan/CROSS_PROCESS_PEER_COORDINATION_DESIGN.md` §5、§6

> **执行者须知**：逐步执行，每步跑验证；碰到 §9 STOP 条件就停下报告。完成后更新 `plans/README.md` 本行状态。
>
> **Drift check（先跑）**：`git diff --stat 86d28e43 -- pkg/gateway/api_extra.go pkg/gateway/run_control.go frontend/src/composables/useChatStream.ts frontend/src/components/chat`，应无输出；有输出就 STOP。
>
> **2026-11-03 reconcile（相对 202ed4f2 的漂移，已在本文件原地改正）**：
> 1. 基线从 `202ed4f2` 改为 `86d28e43`（199 的六笔提交）；199 未合并入 dev，本计划的分支从 `exec/199` 起。
> 2. §2 的 `pkg/gateway/api_extra.go:2306` 已移到 `:2332`（194–199 插入）；`useChatStream.ts` 的 `:110`/`:1015`/`:3422` 未变。
> 3. §2 的「卡头中文词表 `frontend/src/locales/toolHeader.ts`」已随 199 迁到 `ai-elements-vue/packages/elements/src/tool/headerWords.ts`（`frontend/src/locales/toolHeader.ts` 只是转出面）。

## 0. 背景与 owner 裁决

- 全族裁决见设计 §0：跨进程互通、空闲自动唤醒、权限模式不一致的来信先挂起等用户放行、危险 git 在有同树代理时必须审批。
- 同日追加裁决（设计 §7）：与 Claude Code 互通是存于 `fb_config` 的全局开关，**默认关闭**——关闭时抽屉、来信卡里只会出现 forebrain 代理；forebrain 之间**自闭环**，不依赖 Claude Code 或其他任何进程。原型里的同目录对端因此改为另一个 forebrain 终端 `forebrain-harness-28`；`代理会话`（Claude Code）这一类只在开关打开（且本机装了 Claude Code）时出现，样式与终端一致、只换类型标签；那时开关没打开的 forebrain 不会出现（owner 再追加裁决：打开后只用 Claude Code 的目录）。开关本身的设置入口归 191（Web 设置，只在检测到 Claude Code 时显示），不在本计划。
- **TUI 语义是标准，网关向 TUI 看齐**（`tui-is-standard-gateway-sinks`）：本计划 1:1 复刻 199 的语义（来信卡、状态卡、挂起放行、`/peers`、审批原因）。
- 网页规则（owner 既有裁决）：**网页改动先在原型稿 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 上迭代，owner 确认后 1:1 实施**；优先用 ai-elements-vue，再用 shadcn-vue 原生组件，都没有才自己写；对话卡片要有设计感；文案简洁、能用标签不写句子；中文界面动作词必须中文；卡片里不能出现原始 JSON；所有列表服务端分页。

## 1. 共享架构（摘要）

- 数据全部来自引擎（196–198）：消息 API 已带 `origin`（`peer` / `peer_status`）；挂起靠运行事件 `peer_message_held` / `peer_message_settled`；放行/拒绝/列表调 `Environment.ReleasePeerMessage` / `DenyPeerMessage` / `HeldPeerMessages`；对端列表调 `PeerDirectory.List`。
- 信封解析在后端做：网关给消息行多带两个字段 `peer_from_name`、`peer_body`（用 `process.ParsePeerEnvelope`），前端**不**自己解析信封。
- `pkg/gateway` 已有 **31 个生产文件（上限）**：所有后端改动加在现有文件里。

## 2. 现状事实（已对基线核实）

- 消息行 origin：`pkg/gateway/api_extra.go:2332` `m.Origin = state.MessageOrigin(t.PartsJSON)`；前端 `frontend/src/composables/useChatStream.ts:110` `origin?: string`、`:1015` 透传、`:3422` 心跳 `origin: 'heartbeat'`。
- 前端对话组件目录 `frontend/src/components/chat/`（如 `ApprovalCard.vue`、`AutoContinueBanner.vue` 及其 `.test.ts`）——挂起横幅照 `AutoContinueBanner` 的位置与形态做（在输入框上方）。
- 卡头中文词表在 `ai-elements-vue/packages/elements/src/tool/headerWords.ts`（199 已加 `peer_send`/`peer_list` 的 6 个词；`frontend/src/locales/toolHeader.ts` 只是转出面）。
- **原型稿**：owner 规则里的整站稿 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 已不在工作树（`0e6e3827` 把 `docs/plan` 移出历史后目录里只剩 `vendor/`）；全盘只找到一份 2026-10-13 的旧副本，位于 Go 模块缓存 `~/go/pkg/mod/github.com/forebrain-harness/forebrain-harness@v0.0.0-installcheck/docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`（只读，早于 10-25 的活动线定稿，不能当基准）。owner 2026-10-30 指示「找不到就根据本期计划重新生成一份原型文件」，并要求用 frontend-design 技能、有设计感：**本计划的原型＝`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/peer-coordination-preview.html`**（视觉取自当前实现；锚点 `#wake` `#midturn` `#hold` `#crowd` `#git`，加 `:m` 看手机宽度，`#hold:release` / `#hold:deny` 直接看放行/拒绝后的状态）。

## 3. 设计（以原型 `peer-coordination-preview.html` 为准，1:1 还原；下面是实施要点）

- **身份块**（全套界面的唯一强调元素）：方形 20px（抽屉 28px）圆角 6/8px，代表「别的代理」；圆形节点只留给本会话自己的步骤。两个字符＝名字最后一段（`-` 分隔）的前两个字符（`forebrain-harness-a3`→`a3`、`forebrain-harness-28`→`28`）；子代理＝自己名字（`.` 之后）各段首字母，空心描边、用父代理的色调。色调＝对进程代理名做稳定哈希取模 4：`#0E6E7E`、`#6B4C9A`、`#45607D`、`#4F6B3A`（同一个代理在顶栏、对话、抽屉、审批卡里永远同色）。「我」是品牌色圆形。
- **来信**（`origin === 'peer'`）：左对齐、不套框；一行头（身份块、名字半粗、来源类型、「同目录」中性灰标签、时间）+ 正文左侧 2px 对方色调竖线；不是「我发出的」气泡。回合进行中收到时，它是活动线上的节点（节点＝身份块）。
- **发信卡**：引擎卡头 `已发消息 <收件人>`；面板正文是消息；底栏是对方身份块 + 对方此刻状态（「对方正忙，会在下一步读到」/「对方空闲，已开始新一轮」）。
- **投递状态**（`origin === 'peer_status'`）：活动线节点（纸飞机图标），面板里一行一个收件人：身份块、名字、状态签（等对方放行＝琥珀、已送达＝绿、已拒绝＝红、过期/拒收/没送出＝灰）、原因白话。
- **等你放行**：输入框上方，与审批同一族（品牌色边框与底栏）；标题「<名字> 的消息在等你放行」，右侧原因白话（`mode-mismatch`→「你在完全访问模式，对方不是」等），正文 3 行带对方色调竖线；按钮「放行」「拒绝」。**放行动效**：正文从横幅飞入对话流落成来信（420ms，`cubic-bezier(.2,.7,.2,1)`），横幅高度收起，随后唤醒的回合淡入；拒绝只收起横幅并留一行「已拒绝 <名字> 的消息」；`prefers-reduced-motion` 时无动效。
- **顶栏**：同目录代理的身份块叠放（第一块在上）+ 数量，点开右侧抽屉（沿用子代理/MCP 抽屉，440px；手机宽度全屏覆盖）。
- **抽屉**分四区，顺序固定：「等你放行」（有才显示）→「这个目录正在改的文件」（按目录分组，每个文件挂正在改它的代理身份块与时间；我和别人都在改的文件整行琥珀底、时间位写「都在改」）→「这个目录」（代理行：身份块、名字、来源类型、状态点+文字、任务）→「其他目录」（同上 + 目录路径，服务端分页带总数）。抽屉里不放「发消息」入口。
- **审批原因**：`approval_reason === 'peer_work_tree'` 的审批卡，事实区第一句「这个目录还有 N 个代理没提交改动，stash 会把它们一起收走」，下面每个代理一行（身份块、名字、「在改 <文件> 等 N 个文件」）；底栏沿用现有审批。

## 4. 步骤

1. **原型（闸门）**：原型已于 2026-10-30 按 owner 指示重建为 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/peer-coordination-preview.html`。把它交 owner 确认（原型「请你确认」一栏列了三个待拍板的点）；owner 要求修改就在这份原型上迭代（继续用 frontend-design 技能），直到确认。**确认前不做第 2 步**。
2. **后端**（只改现有文件）：
   - `api_extra.go`：消息行在 `origin == "peer"` 时带 `peer_from_name`、`peer_body`、`peer_mark`（两个字符）、`peer_tone`（0–3）——身份块的字符与色调**由后端按 §3 规则算**，前端不自己哈希；新增 `GET /api/peers?session_id=&page=&page_size=`（返回 `held[]`、`files[]`、`same_tree[]`、`other[]`、`other_total`；`files[]` 每项 `{dir, name, agents:[{name, mark, tone, self}], last_ms, clash}`，数据来自 198 的身份台账，含本会话自己的写入），`GET /api/sessions/{id}/peers/held`、`POST /api/peers/held/{msg_id}/release`、`POST /api/peers/held/{msg_id}/deny`（路由注册照该文件现有写法）。身份块规则实现在 `pkg/process`（导出 `PeerMark(name, parent string) (mark string, tone int)`），TUI（199）不用它。
   - `peer_message_held` / `peer_message_settled` 经现有运行事件总线推到页面（196 已接 `publishGatewayRunEvent`），会话快照绑定时补发当前挂起列表（照自动续跑 `AutoContinuePlan.Payload()` 在绑定时补发的做法）。
   - `/peers` 命令在网关侧的分发：返回打开抽屉的指令（与 `/mcp` 打开抽屉的现有约定一致）。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → ok；`ls pkg/gateway/*.go | grep -v _test | wc -l` → 31。
3. **前端**：按确认的原型 1:1 实现；组件先找 ai-elements-vue、再 shadcn-vue；新组件放 `frontend/src/components/chat/`（`PeerMessageCard.vue`、`PeerHeldBanner.vue`、`PeerDrawer.vue` 等），每个配 vitest。`useChatStream.ts` 处理两个新事件与新字段。
   验证：`cd frontend && pnpm vitest run src/components/chat src/composables` → pass；`pnpm type-check`（或仓库现有等价命令）→ 0 错误。
4. **e2e**：照 `scripts/acceptance/web_e2e.sh` 的方式新增 `e2e/peer-*.spec.ts`（fake provider；两条用例：来信卡出现、挂起横幅放行后开轮），只跑本计划的 spec：`FOREBRAIN_E2E_SPEC="e2e/peer-*.spec.ts" scripts/acceptance/web_e2e.sh`。
5. **收口**：`go vet ./...`；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway ./pkg/process -count=1`；`make ui` 后 `go build ./cmd/forebrain`。

## 5. 边界

- **只动**：原型稿；`pkg/gateway/api_extra.go`、`pkg/gateway/run_control.go`（如需）、gateway 现有测试文件；`frontend/src/components/chat/*`（新组件）、`frontend/src/composables/useChatStream.ts`、`frontend/src/locales/*`、`e2e/peer-*.spec.ts`（新）。
- **不动**：引擎（194–198）、TUI（199）。不新建 `pkg/gateway` 文件。

## 6. 完成判据（机器可查）

1. 原型经 owner 确认（执行记录里写明确认日期与原话）。
2. `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → ok；`go vet ./...` 无输出。
3. `cd frontend && pnpm vitest run` 通过；新 e2e spec 通过。
4. `ls pkg/gateway/*.go | grep -v _test | wc -l` 等于 31。
5. `plans/README.md` 本行状态更新。

## 7. 测试计划

- 前端：新组件各一个 vitest（渲染、按钮回调、暗色类名）；`useChatStream` 对两个事件的状态迁移用例（参照 `AutoContinueBanner.test.ts` 与 `useChatStream` 现有自动续跑用例）。
- 后端：新接口的 handler 用例（参照 `api_extra.go` 现有接口测试），含分页总数。
- e2e：§4 第 4 步两条。

## 8. 维护注意

- 语义以 199 为准；TUI 语义改了，这里跟着改。
- 卡头词只能从引擎词表来（`header_actions.json` + `HEADER_ACTION_ZH`），前端不得自造动作词。

## 9. STOP 条件

- owner 还没确认 `peer-coordination-preview.html`（执行记录里没有确认日期与原话）→ 只做第 1 步，不进入第 2 步。
- 实施时发现原型里的某个交互无法用 ai-elements-vue / shadcn-vue 组件 1:1 还原——停下报告差异，不要自行改设计。
- 后端改动需要新建 `pkg/gateway` 文件。
- 需要前端解析信封才能拿到发信人（说明第 2 步的字段没带上）。

**确认（owner 2026-10-31）**：原型 `peer-coordination-preview.html` 已确认，按原型 1:1 实施；原型闸门解除。

## 10. 执行记录（2026-11-03，评审 APPROVE）

- **提交**：`2c004c8f`（步骤 2 后端）、`99717f68`（步骤 3 前端）、`47eecb9f`（步骤 4 e2e + held body）、`7a62e89f`（步骤 5 dist 重建 + ChatView 测试 mock）。
- **验证**：`go test -tags fts5 ./... -count=1` 全绿（35 包）；`go vet ./...` 无输出；`pnpm vitest run` 175 文件/1346 测试全过；`FOREBRAIN_E2E_SPEC="e2e/peer-*.spec.ts"` 2/2 通过；`pkg/gateway` 生产文件 31。
- **计划前提失真与执行器的处置**（全部如实报告，评审逐条判为可接受）：
  1. 「196 已接 `publishGatewayRunEvent`」不实——网关漏斗拒绝无 run id 的事件（`approval.go:140`），两个 peer 事件根本到不了页面。改法：`approval.go` 只白名单这两个事件类型（`runLessPeerEvent`）。
  2. peer 消息是 `is_meta`，`handleChatMessages` 会跳过所有 meta 行。改法：过滤放行 peer origin 行，其余 meta 仍隐藏。
  3. `input_delivered` 不带 origin，运行中送达的 peer 来信与读者自己的插话分不开。改法：`event.InputDeliveredPayload` 加 `Origin`/`FromName`/`PeerMark`/`PeerTone`。
  4. `origin === 'peer_status'` 是纯文本，卡片要一行一个收件人。改法：`process.ParsePeerReceiptSummary` 把回执行解析成 `PeerReceiptRow`（后端解析，前端不解析）。
  5. held 的 `Preview` 是信封开头，横幅引的是 `<cross-session-message …>`。改法：`HeldPeerMessage.Body` + `PeerMessageHeldPayload.PeerBody`（同时修掉 199 §10 的第 2 条）。
  6. `peer_work_tree` 的 payload 只有名字没有身份块，且 `pkg/tool` 不能 import `pkg/process`。改法：网关 `actionListRow` 用 `process.PeerMark` enrich 出 `peer_agents`。
  7. `files[]` 需要本会话自己的写入，而 `TouchesBetween` 按设计排除自己。改法：`Environment.RecentPeerFileTouches`。
  8. `/peers` 在网关侧没有 executor，`/mcp` 也不是开抽屉而是渲染 markdown。改法：在 composer 里回答（`ChatView.handleSubmit`，照 `/memories`），打开抽屉；子代理视图仍回「Unknown command /peers」（199 的既有缺口）。
  9. `pkg/gateway/dist` 入库且 `go:embed` 提供（此前 36 笔提交动过），故 `make ui` 的产物一并提交（333 文件）。
  10. `scripts/acceptance/web_e2e.sh` 补 `FOREBRAIN_PEERS_DIR`/`FOREBRAIN_PEER_SOCKET_DIR` 到本次运行的 scratch 树——真实网关会注册为 peer，设计 §9 禁止测试读写用户自己的会合目录。
  11. worktree 缺 `ai-elements-vue/packages/{elements,shadcn-vue}/node_modules` 软链（与 199 的 `frontend/node_modules` 不同），`pnpm build` 无法解析依赖；执行器补了软链（均 gitignore，未 `pnpm install`）。
- **刻意未闭的三处原型保真缺口**（执行器报告，评审接受）：
  1. 回合进行中的来信画成独立左对齐消息，不是活动线上的节点——转录没有「正在进行一轮」的信号能让实时与重载渲染一致，用 runId 启发式会让同一会话两种渲染不同。
  2. 四个原型色是手挑的，没有任何稳定哈希能复现（试过 FNV-1/1a 32/64、CRC32/Castagnoli、MD5、SHA1/256、djb2、sdbm、Java hash，对全名与末段都试过）；§3 的规则按原文实现（进程代理名 FNV-1a 32 取模 4），同一代理在各 surface 同色，但四个示例名字与 mock 对不上。
  3. 拒收/丢弃回执的原因是引擎自己的英文句子（终端也是如此），四种常见状态有中文句。
- **其他**：`pnpm typecheck` 在该基线本就不干净（1124 个 TS7006 既有错误，全在未触碰的文件里），执行器新增文件 0 错误；Playwright 自带浏览器未装，但配置驱动本机 Chrome，e2e 是真跑。
