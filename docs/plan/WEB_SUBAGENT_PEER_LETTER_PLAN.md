# Plan: web 子代理视图收到的 peer 来信画成与主视图同一张 PeerMessageCard（原型已确认）

> **Executor instructions**: 按步骤顺序执行，每步跑验证命令确认预期后再继续。命中 STOP
> conditions 停下报告，不即兴。只改 Scope in-scope 文件。在 worktree 自己分支上 commit
> （conventional commits + `Signed-off-by:`，author wubin1989 <328454505@qq.com>），不 push/
> merge/碰其它分支。计划文件/索引由 advisor 维护，执行者跳过。fresh worktree 无构建产物与
> gitignored 原生依赖：先取依赖（见 Commands）。回报用末尾报告格式，每条断言附工具证据。
>
> **Drift check（先跑）**: 基线 dev `329790da`（2026-11-20）。以「Current state」摘录对照活
> 代码内容为准（仅行号漂移不算 drift）。advisor 已核实下列锚点匹配。

## Status
- Priority P1 · Effort M · Risk MED（引擎新增 port + 共享事件契约 + web 渲染）· Category bug
- Planned at dev `329790da`, 2026-11-20
- 设计契约：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/subagent-peer-letter-preview.html`
  （owner 2026-11-20 已确认）。视觉与主视图 `PeerMessageCard.vue` 逐字一致，不新造样式。

## Why this matters
TUI 子代理视图 peer 来信卡已修（dev ac09eb14/329790da）。web 端同缺陷仍在：
`useChatStream.ts` 的 `subagent_input_delivered` 一律建 `{kind:'user'}` 块，
`SubagentConversation.vue` 把 user 块画成右对齐用户气泡——peer 来信被当成读者自己的话，
气泡里铺原样 `<agent-message>`/`<cross-session-message>` 信封。主视图则经
`PeerMessageCard.vue` 画「不套框、发送方身份块+名字+类别+同目录+时间、发送方色竖线引出正文」。
本计划让 web 子代理视图复用同一张卡，实时与回放一致。

## Current state（锚点，advisor 亲自读取）

### 引擎侧
- `pkg/run/subagent.go` `publishInputDelivered(fac Factory, ex, delivered)`（约 1016-1038）：
  对每个 steer entry 发 `event.SubagentInputDeliveredPayload{AgentID, ExecutionID, Text, Origin}`
  （Origin 已于 TUI 修复携带；Text 逐字）。**未带 peer 显示字段**。三个调用点：beginAttempt
  change hook / awaitUserMessage / interrupt-to-send resume（同函数，改一处全覆盖）。
- `pkg/event/run_events.go` `SubagentInputDeliveredPayload`（约 553-563）：AgentID/ExecutionID/
  Text/Origin。**对照** `InputDeliveredPayload`（约 573-595）已带 `FromName/PeerMark/PeerTone/
  PeerBody`（json: from_name/peer_mark/peer_tone/peer_body）。
- **主视图推导范式**（必须镜像）`pkg/gateway/run_control.go:427-434` `watchRunQueue`：
  ```go
  if state.IsPeerOrigin(in.Origin) {
      payload.Origin = in.Origin
      if fromName, body, ok := process.PeerEnvelopeDisplay(in.Text); ok {
          payload.FromName = fromName
          payload.PeerMark, payload.PeerTone = process.PeerMark(fromName, "")
          payload.PeerBody = body
      }
  }
  ```
- 推导函数：`process.PeerEnvelopeDisplay(content)(fromName,body,ok)`（pkg/process/peer_display.go:10）、
  `process.PeerMark(name,parent)(mark,tone)`（pkg/process/peers.go:123，fnv hash，唯一推导）。
- **层约束**：`publishInputDelivered` 在 pkg/run（L3），**不能 import pkg/process（L4）**。
  pkg/peer（L1）无 mark/tone 推导。故须经 **process 注入的 port**（镜像现有 `PeerDirectory`）：
  - `pkg/run/runner.go:146` `Deps.Peers PeerDirectory`；`pkg/run/factory.go:208-213`
    `Factory.peerDirectory()` 读 `Owner.Peers`；接口 `pkg/run/peer_tools.go:45`。
  - process 注入点：`pkg/process/open.go:679` 与 `pkg/process/peers.go:291`
    `env.deps().Peers = env.peerDirectory()`。

### web 侧
- `frontend/src/composables/useChatStream.ts:4618-4632` `subagent_input_delivered`：现建
  `{kind:'user', id:evt.id, text}`。**镜像主视图** `input_delivered` 处理（约 5724-5743）：读
  `payload.origin/fromName(peer_mark)/peerTone/peerBody`，peer origin 时带这些字段。
- `TimelineBlock` 联合（约 1721-1789）：有 `user/prompt/assistant/thinking/tool/...`，**无 letter**。
- `frontend/src/components/chat/SubagentConversation.vue:89` user 块渲染：
  `<Message v-else-if="run.item.kind==='user'" from="user">…`（缺陷视觉）。活动线在上方
  （`ActivityLine`/`ActivityStep`，子代理说话节点 `node="sub-say"` 机器人）。
- **主视图路由范式**（必须镜像）`frontend/src/views/ChatView.vue:206-217`：
  ```html
  <PeerMessageCard v-if="msg.role==='user' && msg.origin==='peer'"
    :from-name="msg.peerFromName" :mark="msg.peerMark" :tone="msg.peerTone"
    :body="msg.peerBody" :content="msg.content"
    :kind="peerKindOf(msg.peerFromName)" :same-tree="peerSameTreeOf(msg.peerFromName)"
    :created-at="msg.createdAt" />
  ```
- `PeerMessageCard.vue`（props: fromName/mark/tone/body/content/kind/sameTree/createdAt；
  body 空时回退 content；fromName 空时 t('chat.peerUnknownAgent')）。
- `peerKindOf/peerSameTreeOf`（ChatView.vue:2828-2840）：按名字查 `peerDirectory.value`
  （sameTree/other roster）得 kind/sameTree——**目录查找，非解信封**。现是 ChatView 局部函数，
  需提取到共享模块供两视图复用（一处一义）。

## 设计
1. **引擎 port**：run 定义 `PeerDisplay` 接口（一个方法，如
   `Describe(content string)(fromName, body, mark string, tone int, ok bool)`），`Deps.PeerDisplay`
   字段 + `Factory.peerDisplay()` 访问器（镜像 Peers/peerDirectory）。process 实现 wraps
   `PeerEnvelopeDisplay`+`PeerMark`，在 open.go/peers.go 注入点 wiring。port nil 时跳过推导
   （payload 仅 origin+text；web 仍按 origin 画 letter，name 空回退 unknown、body 空回退 text）。
2. **payload**：`SubagentInputDeliveredPayload` 加 `FromName/PeerMark/PeerTone/PeerBody`
   （json 与 InputDeliveredPayload 同名）。`publishInputDelivered` 当 `state.IsPeerOrigin(entry.Origin)`
   时经 port 推导填充（镜像 watchRunQueue）。
3. **web block**：`TimelineBlock` 加 `{kind:'letter'; id?; fromName?; mark?; tone?; body?; content; createdAt?}`。
   `subagent_input_delivered` handler：origin==='peer' → letter 块（带 fromName/mark/tone/body/
   content=text/createdAt=evt.createdAt）；否则维持 user 块（含旧事件无 origin → user，向后兼容）。
4. **web 渲染**：`SubagentConversation.vue` letter 块画成活动线上一步：发送方 `PeerMark` 作节点
   （方形身份块，镜像主视图回合中来信 `.step>.pm`+letter）+ `PeerMessageCard`（kind/sameTree 经
   共享 peerKindOf/peerSameTreeOf）。提取 peerKindOf/peerSameTreeOf 到共享 composable/lib，
   ChatView 与 SubagentConversation 共用。
5. **测试**：引擎 publishInputDelivered peer 字段推导（port 注入）+ 用户消息空；web
   useChatStream subagent_input_delivered peer→letter / user→user / 旧事件→user；
   SubagentConversation letter 渲染（若有测试夹具）。

## 行为变化矩阵（旧→新）
| 场景 | 旧 | 新 |
|---|---|---|
| web 子代理视图收到 peer 来信（live） | 右对齐用户气泡 + 原样信封 | 活动线来信卡（PeerMessageCard），与主视图一致 |
| 同上 replay（持久化事件） | 用户气泡 | 来信卡（payload 带 peer 字段，持久化于事件） |
| 用户亲手插话 | 用户气泡 | 不变 |
| 旧事件（无 origin/peer 字段） | 用户气泡 | 不变（向后兼容） |
| 主视图 | 不变 | 不变（不碰 ChatView 路由逻辑，仅提取共享函数） |

## Commands you will need
Go 前置：`export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`，
`FOREBRAIN_HOME="$(mktemp -d)"`，CGO_ENABLED=1，`-tags fts5`。fresh worktree 先取原生依赖：
`scripts/fetch-libtokenizers.sh && scripts/fetch-pdfium.sh && scripts/fetch-pdfium-fonts.sh`
（直连 stall 时用本机代理 http_proxy/https_proxy=http://127.0.0.1:7897）；或从主树符号链接
third_party/{pdfium,libtokenizers}（绝对路径）+ 复制主树 pkg/gateway/dist（gitignored）供
architecture 的 web 资产测试。前端：`cd frontend && pnpm install`（若 node_modules 缺）。

| Purpose | Command | Expected |
|---|---|---|
| Go 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| Go 定点 | `FOREBRAIN_HOME=… CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'SubagentDeliveredPayload' -count=1` | PASS |
| Go 包级 | `… go test -tags fts5 ./pkg/run ./pkg/process ./pkg/event ./pkg/architecture -count=1 -timeout 30m` | 全 ok |
| Go vet/gofmt | `CGO_ENABLED=1 go vet -tags fts5 ./...`；`gofmt -l cmd pkg third_party plugins` | exit 0 / 无输出 |
| 前端 typecheck | `cd frontend && pnpm typecheck` | exit 0 |
| 前端测试 | `cd frontend && pnpm test` | 全 PASS |
| 前端构建 | `cd frontend && pnpm build` | exit 0 |

## Scope
**In scope**：pkg/event/run_events.go；pkg/run/{runner.go,factory.go,peer_tools.go(或新 port 文件),subagent.go}；
pkg/process/{open.go,peers.go,peer_display.go(或新 impl 文件)}；frontend/src/composables/useChatStream.ts；
frontend/src/components/chat/SubagentConversation.vue；共享 peerKindOf 提取（新 lib/composable + ChatView.vue 改引用）；
对应测试（pkg/run/*_test.go、pkg/process/*_test.go、frontend useChatStream.test.ts、SubagentConversation 测试）。
**Out of scope**：PeerMessageCard.vue 本体（复用不改）；ChatView 的 peer 路由逻辑（仅提取共享函数）；
TUI（已修）；pkg/gateway watchRunQueue（主视图范式，不改）；pkg/peer/pkg/tui；plans/README.md（并发编辑，不碰）。

## Steps（每步带验证）
1. 引擎 port：run 定义 PeerDisplay 接口 + Deps 字段 + Factory 访问器；process impl + wiring。
   Verify: `go build -tags fts5 ./pkg/run ./pkg/process` exit 0。
2. payload 加 4 字段；publishInputDelivered peer-origin 经 port 推导（镜像 watchRunQueue）。
   Verify: build exit 0；`git diff pkg/run/subagent.go` 仅 publishInputDelivered。
3. 引擎测试：peer 推导字段 + 用户消息空 + port nil 回退。Verify: 定点 PASS。
4. web TimelineBlock letter + handler origin 路由。Verify: `pnpm typecheck` exit 0。
5. 提取 peerKindOf/peerSameTreeOf 共享；SubagentConversation letter 渲染（活动线 sender 节点 + PeerMessageCard）。
   Verify: typecheck exit 0。
6. web 测试（peer→letter/user→user/旧→user；渲染）。Verify: `pnpm test` 相关 PASS。
7. 全门禁：Go 包级(含 architecture)+vet+gofmt；前端 typecheck+test+build。Verify: 全绿。
8. commit（conventional+sign-off）；`git status` 干净；`git diff --stat 329790da..HEAD` ⊆ Scope。

## Done criteria
- Go build/vet/gofmt 绿；pkg/run/process/event/architecture 全 ok；新引擎测试 PASS。
- frontend typecheck/test/build 绿；新 web 测试 PASS。
- grep：SubagentInputDeliveredPayload 含 peer_mark；useChatStream subagent_input_delivered 含 origin 路由；
  SubagentConversation 含 PeerMessageCard。
- scope ⊆ in-scope；worktree git status 干净；未 push/merge。

## STOP conditions
- 摘录与活代码内容不符（drift）。
- 需改 out-of-scope（尤其 PeerMessageCard 本体、ChatView 路由逻辑、TUI、gateway watchRunQueue）。
- 层约束被违（run 直接 import process）→ 停，必须经 port。
- 验证合理修复后仍两次失败。
- 前端构建因主树并发改动报错 → 停，peer_send 问对应 agent，不自修他人文件。

## Maintenance
- 引擎 peer 字段推导唯一来源 process.PeerMark/PeerEnvelopeDisplay，经 port 注入 run；未来加字段镜像
  InputDeliveredPayload。web 表面永不解信封（owner 红线）。
- 旧事件无 peer 字段 → user 气泡（向后兼容），不在渲染端嗅探信封补 origin。
- reviewer 重点：port 层约束、payload 与主视图契约对称、letter 块 live/replay 一致、共享 peerKindOf 一处一义。

## 报告格式
STATUS: COMPLETE | STOPPED
WORKTREE: path and branch
STEPS: per step done/skipped + verification result
STOPPED BECAUSE: (if STOPPED)
FILES CHANGED: list
NOTES: deviations/surprises/judgment calls

## Implementation Status

- **VERDICT: APPROVE**（advisor tech lead 独立复核，2026-11-20）。**已合并回主树 dev**（owner 指示）。
- **合并落地**: owner「合并回主树dev分支」→ 配方：dev=329790da=分支基线（直接后代，无需 rebase）→
  无活跃代理争 dev → 主树 `git merge --ff-only`（线性，dev `329790da`→`39569e96`）→ 主树全门禁重跑绿
  （含 pkg/architecture + 前端 typecheck/test/build）→ `git worktree remove --force` + `git branch -d`
  （was 39569e96）。永不 push。344 worktree 未动。
- **Worktree（已清理）**: 原 `.worktrees/web-subagent-peer-letter`，branch 同名，executor commit
  `39569e96`（conventional + Signed-off-by wubin1989）。现已 remove/delete；落地 commit = dev HEAD `39569e96`。
- **Executor**: general-purpose 子代理（默认模型）。

### advisor 本人复跑（worktree 内，PKG_CONFIG_PATH+scratch FOREBRAIN_HOME+fts5）
| 判据 | 结果 |
|---|---|
| `gofmt -l cmd pkg third_party plugins` | 无输出 ✓ |
| `go build -tags fts5 ./...` | 无错误（仅 ld macOS 版本 warning）✓ |
| `go vet -tags fts5 ./...` | 无错误 ✓ |
| `go test ./pkg/run ./pkg/process ./pkg/event ./pkg/architecture -count=1 -timeout 30m` | run 44.2s / process 227.4s / event 0.3s / architecture 2.3s 全 ok ✓ |
| `pnpm typecheck` | exit 0 ✓ |
| `pnpm test` | 194 文件 1562/1562 PASS ✓（本人复跑无 flake） |
| `pnpm build` | exit 0（9.5s）✓ |
| scope `git diff --stat 329790da..HEAD` | 16 文件全 ⊆ in-scope ✓ |
| 层约束 `grep pkg/process pkg/run/*.go` | 无 import（仅注释）✓ |
| grep peer_mark/origin 路由/PeerMessageCard | 命中 ✓ |
| ActivityLine 连线 CSS 认 `.activity-step` | ActivityLine.vue:47 证实，letter 步与 ActivityStep 同机制 ✓ |

### 合并后主树复跑（dev=39569e96，ff-only 内容与 worktree 受测 commit 逐字一致）
- `gofmt -l cmd pkg third_party plugins` 无输出 ✓；`go vet -tags fts5 ./...` 无错误 ✓。
- `go test ./pkg/run ./pkg/process ./pkg/event ./pkg/architecture -count=1 -timeout 30m`：
  run 44.3s / process 233.5s / event 0.45s / architecture 2.4s 全 ok ✓。
- `pnpm typecheck` exit 0 ✓；`pnpm test` 194 文件 1562/1562 PASS ✓；`pnpm build` exit 0（9.6s）✓。
- `git status --porcelain` 干净；`git worktree list` 仅余主树 dev + 344（我方已清理）。

### Deviations（均 documented、in-scope、按 merit 接受）
1. letter 步用库 `ChainOfThoughtStep.activity-step`（ActivityStep.vue 节点词汇闭合且 out-of-scope）；
   与 ActivityStep 同一机制，ActivityLine.vue:47 连线 CSS 认 `.activity-step`，scoped CSS 镜像其度量+原型 step 内 letter 覆盖。
2. letter 上线用 SubagentConversation 本地谓词扩展 groupActivityRuns（activityRuns.ts out-of-scope；letter 仅子代理转录产生）。
3. 目录共享走 provide/inject（providePeerDirectory.ts，镜像 providePendingActions），避免改 out-of-scope 的
   SubagentTab/SubagentDrawer prop drilling；无 provider 时卡无标签（优雅降级）。
4. PeerMark kind 用既有名字形状规则 `name.includes('.')→sub`（PeerAgentRow/ChatView crowd 同款）；
   主代理插话画实心方形身份块（原型品牌圆「主」无法从 payload 推导，实现随真实 PeerMark 组件）。
5. process display port 不加 peer.Enabled() 门（纯推导无服务依赖，协议不可用平台来信仍带发送方身份，有测试钉住）。
6. 渲染测试断言 PeerMessageCard props + 同步头部文案 + 节点 + 无用户气泡（异步 markdown body 由卡自家未改动测试钉住）。
7. 执行期磁盘满（剩 571Mi）→ 按 macos-disk-reclaim skill 仅清 go-build 缓存 15G（未动 Docker），恢复 13Gi+ 后重跑全绿。
8. 前端首跑 1 例未名 flake（高负载），随后两次全量 + advisor 复跑均 1562/1562 绿。

### 复核结论
diff 最小、复用主视图唯一卡定义 PeerMessageCard/PeerMark（一处一义）；引擎 port 守层约束且推导唯一来源
process.PeerMark/PeerEnvelopeDisplay；payload 与 InputDeliveredPayload 契约对称；letter 块 live/replay 一致、
旧事件向后兼容；测试覆盖全分支且断言有力。scope 干净，质量成立。**APPROVE**。合并由 owner 决定。