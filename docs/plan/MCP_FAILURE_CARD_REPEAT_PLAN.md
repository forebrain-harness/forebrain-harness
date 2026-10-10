# MCP_FAILURE_CARD_REPEAT_PLAN — TUI 中同一 MCP 启动失败被重复画成多张卡片（根因修复）

- 状态：**已实施并真机验收通过（2026-10-10）**——验收记录见 §12
- Planned at：工作区基线 `9656051` + 未提交改动（frontend/gateway/dist，与本计划无关）
- 实施落点：**当前项目目录、当前分支直接改源码**，不用 worktree/快照，改动留工作区不 commit
- 本文件由计划模式产出后复制到仓库惯例路径 `docs/plan/`，并在 §12 回填验收记录

---

## 1. 目标（Why this matters）

TUI 启动后，若某个 MCP server 启动失败（例如 `caveman-shrink` 30 秒超时），同一次失败会在会话里被画成
**多张完全相同的错误卡片**；用户切换模型时会再补画一张。用户报告："forebrain harness tui 启动后，伴随切换
模型的操作，会重复出现多次 mcp 消息卡片"。

语义要求（仓库既有裁决）：

- owner 裁决（`docs/plan/SQLITE_STATE_SCHEMA_OVERHAUL_PLAN.md:1129`）：**每个事实只在 `fb_session_events` 记一次**。
- replay 与 live 1:1（产品红线）。
- D16（同文件 `:1204`，实现见 `pkg/tui/notify.go:1166-1173` 注释与
  `pkg/tui/notify_test.go:219 TestMCPStartupFailureBeforeTheSessionExistsIsShownButNotStored`）：
  会话行尚不存在时观察到的启动失败**只展示、不落库**。

修复后：一次 MCP 启动失败 = **live 画 1 张卡 + 库里 1 条事件 + resume 回放 1 张卡**。

---

## 2. 现象与实测复现（证据）

### 2.1 用户真实环境（只读取证）

- `~/.forebrain/logs/info.log`（2026-10-10）：
  ```
  ... interactive.go:111 msg="opening chat session for streaming terminal"        (09:13:36 / 09:16:43)
  ... runner.go:1254 msg="agent load" ... mcp_generation=13f9d2d1006c mcp_pending=true
  ... runner.go:2423 msg="agent tools" generation=13f9d2d1006c mcp=1 servers=2 connected=1 error=1 elapsed=30.002s
  ```
  → `caveman-shrink` 每次启动都 30s 超时；`generation` 恒为 `13f9d2d1006c`（由配置指纹派生，稳定）。
- `~/.forebrain/state/forebrain.state.sqlite` → `fb_session_events` 中 `event_id like 'mcp:%'` 共 10 行，
  **每个会话只 1 行**，内容如：
  ```
  {"error":"startup timed out after 30s: context deadline exceeded","message":"startup timed out after 30s: context deadline exceeded",
   "title":"mcp caveman-shrink","source":"mcp","server":"caveman-shrink","generation":"13f9d2d1006c"}
  ```
  （今天两次 TUI 会话 `cli-d1968d71…`、`cli-388cb9d1…` 各 1 行。）

### 2.2 隔离环境复现（本机已实测，2026-10-10）

环境：`/tmp/fb-mcp`（隔离 `FOREBRAIN_HOME` + 临时项目目录 + `.claude/skills/run-forebrain/fake_provider.py`），
配置 2 个模型（可 `/model` 切换）+ 1 个永不响应的 stdio MCP server：

```yaml
approval_policy: on-request
agents:
  defaults:
    mcp_servers:
    - name: slowpoke
      transport: stdio
      command: sleep
      args: ["300"]          # 不回答 initialize → 30s 超时失败（StartupTimeoutFor 默认 30s）
  definitions:
    main:
      primary: true
      llm_providers:
      - {provider: deepseek, model: deepseek-chat,     api_key: ${FAKE_LLM_KEY}, base_url: http://127.0.0.1:8731}
      - {provider: deepseek, model: deepseek-reasoner, api_key: ${FAKE_LLM_KEY}, base_url: http://127.0.0.1:8731}
```

实测结果（`tmux capture-pane` 计数 `grep -c "mcp slowpoke"`）：

| 操作 | 屏幕上的同一张卡片份数 | `fb_sessions` 行数 | `fb_session_events` 事件数 |
|---|---|---|---|
| 启动 TUI，等到 30s 超时（未发任何消息、未切模型） | **2** | 0 | 0 |
| 之后做 1 次 `/model` 切换 | **3** | 1 | 1 |

一次失败 = 3 张一模一样的卡片。用户说的"重复出现多次"即此。

---

## 3. 根因（file:line 证据链）

### 3.1 代码链

1. **TUI 启动不建会话行**：`pkg/tui/run.go:96-115`
   ```go
   initialSessionID := strings.TrimSpace(opts.InitialSessionID)
   ...
   if initialSessionID == "" {
       initialSessionID = strings.TrimSpace(opts.Session.NewSessionID("cli"))
   }
   ```
   行只在首次用会话时才落库（`pkg/tui/chat_slash.go:666-670` 的 `/model` → `store.Ensure(ctx, sid, sid)`，
   `pkg/turn/executor.go:287` 的首条消息 → `Ensure`）。
2. **启动即订阅 MCP 状态**：`pkg/tui/run.go:580-585`
   ```go
   watchMCPStartup := func(sessionID string) {
       if watcher, ok := opts.Session.(mcpStartupWatcher); ok {
           watcher.WatchMCPStartup(sessionID)
       }
   }
   watchMCPStartup(initialSessionID)
   ```
   → `pkg/tui/notify.go:1080 WatchMCPStartup`，订阅回调（notify.go:1104-1115）：
   ```go
   cancel := runner.MCPStartup().Subscribe(func(snapshot run.MCPSnapshot) {
       s.recordMCPStartupFailures(sid, snapshot)
       ...
       s.notifyUI(MCPStatusMsg{Snapshot: snapshot, Progress: snapshot.Progress})
   })
   ```
   **每一次快照投递都会走 `recordMCPStartupFailures`**，而不是只在"变化"时。
3. **失败事件身份是派生的**（故意，为了可去重）：`pkg/tui/notify.go:1037-1051`
   ```go
   return event.RunEvent{ID: mcpFailureEventID(generation, rec.Name), ...}
   func mcpFailureEventID(generation, server string) string {
       return "mcp:" + strings.TrimSpace(generation) + ":" + strings.TrimSpace(server) + ":error"
   }
   ```
4. **发布路径**：`pkg/tui/notify.go:739-767`
   ```go
   func (s *ChatSession) publishRunEvent(ctx context.Context, evt event.RunEvent) error {
       ...
       if s.runSvc() != nil && strings.TrimSpace(evt.SessionID) != "" {
           persisted, inserted, err := s.runSvc().AppendSessionEventOnce(ctx, state.SessionEvent{...})
           switch {
           case errors.Is(err, state.ErrSessionNotStarted):
               // The conversation has not started: the event is shown and belongs
               // to no session's history. A resume re-observes whatever still
               // holds, against the session that exists by then.
           case err != nil:
               return err
           case !inserted:
               // ... The store is what makes that decidable ...
               return nil
           default:
               evt = turn.RunEventFromRecord(persisted)
           }
       }
       switch evt.Type { ... case event.RunEventTurnError: s.notifyTurnError(evt) ... }
   ```
5. **存储层的"无行"语义**：`pkg/state/session_events.go:110-118` 的插入带
   `SELECT ... WHERE EXISTS(SELECT 1 FROM fb_sessions WHERE id=?)` + `ON CONFLICT(session_id,event_id) DO NOTHING`；
   `:127-129` 无行时返回 `ErrSessionNotStarted`。
6. **每次 Load 都重放同一代并再次投递**：`pkg/run/runner.go:2136-2160 startOrReplayMCPSegmentLocked`
   ```go
   if seg := r.mcpSeg; seg != nil && seg.fingerprint == fp {
       r.replayMCPSegmentLocked(a, seg)
       r.mcpHub.attach(r.mcpReg, seg.generationServers(), seg.generation, false, time.Time{})
       return nil
   }
   ```
   `r.mcpHub.attach`（runner.go:2578-2608）会 `reg.Subscribe(h.onRegistrySnapshot)`，订阅本身**立即回放当前快照**；
   `onRegistrySnapshot`（:2614-2625）把快照交给每个订阅者；`markSettled`（:2661-2676）在代结算时再投递一次。
   模型切换（`/model`）会走 `SelectModel → SetPrimaryModel → loadLocked`（`pkg/tui/chat_slash.go:651-670`、
   `pkg/run/config.go:1401-1410`），于是又触发一次 attach/settle 投递。

### 3.2 语义缺口（根因一句话）

> `recordMCPStartupFailures` 在**每次** MCP 快照投递时都会重新发布同一个失败；
> 会话行存在时由 `AppendSessionEventOnce` 的 `UNIQUE(session_id,event_id)` 拒绝重复（`!inserted → return nil`，不画），
> 但**会话行不存在时（D16 的"只展示不落库"路径）没有任何仲裁者**：事件既没落库，也没有"已经画过"的记忆，
> 于是每投递一次就再画一张，直到会话行被创建（第一次 `/model` 或首条消息）为止；建行那一刻的投递又把
> 早已画过的事实**补画一张**并同时落库。

### 3.3 为什么不是别的解释（已排除）

- **不是 generation 漂移**：fingerprint 只由配置（name/ServerFingerprint/startup_timeout/required + 项目目录）
  决定（`pkg/run/runner.go:1754-1779`），实测跨天恒为 `13f9d2d1006c`。
- **不是流式/渲染重复**：`fb_messages` 中没有重复行；重复只发生在事件→卡片这条链上。
- **不是 MCP 真的重启多次**：`info.log` 里每次进程只 `mcp_pending=true` 一次；重复来自同一代的快照回放。
- **不是并发竞态**：`AppendSessionEventOnce` 是单条原子 INSERT…ON CONFLICT…RETURNING；重复来自"无行时无处去重"。
- **不是 web 侧**：`mcpStartupErrorEvent` 只有 TUI 一个生产者（`grep -rn mcpStartupErrorEvent pkg/` 只命中
  `pkg/tui/notify.go`），gateway 只订阅 MCP 状态做状态行（`pkg/gateway/server.go:773-785`）。

---

## 4. 修复设计

### 4.1 方案（最小充分改动）

给"只展示不落库"的事件补上**发布面自己的、按（会话, 派生事件 ID）去重的"已画过"记忆**，作为存储层
`UNIQUE(session_id,event_id)` 在"会话尚不存在"这一窗口内的对等物：

- 新增 `ChatSession` 字段（放在 `mcpWatch*` 旁）：`shownEventsMu sync.Mutex` + `shownEvents map[string]struct{}`，
  key = `sessionID + "\x00" + eventID`。
- 新增方法 `func (s *ChatSession) markEventShownOnce(sessionID, eventID string) bool`：
  首次调用记录并返回 `true`；重复返回 `false`；`sessionID`/`eventID` 任一为空时返回 `true`（无身份可去重）。
- `publishRunEvent` 在**存储分支之后、绘制 switch 之前**插入一次判定：
  ```go
  if !s.markEventShownOnce(evt.SessionID, evt.ID) {
      // 这个事实在本会话里已经画过一次：这次投递把它落库（或仍然无处落库），
      // 但不再补画第二张卡 —— 记录与绘制是两件事。
      return nil
  }
  ```

要点：

- **放在存储分支之后**：落库行为不变（会话存在时事件照样落一条）；只抑制"重复绘制"。
- **只对派生 ID 生效**：TUI 里到达 `publishRunEvent` 的路径有两条——
  （a）生产环境 `runner.Events = event.SinkFunc(s.publishRunEvent)`（`pkg/tui/notify.go:1350`），运行时发布的
  `plan-review:<reviewID>:started|reviewed`（`pkg/turn/approval.go:1489-1504`，reviewID 是每次评审新生成的
  uuid 前缀）与 LSP 推荐（`pkg/process/open.go:492-500`，ID = `rec.ID`）都带派生 ID；
  （b）TUI 自己调用的四处 `approval-*`（`chat_session.go:677`、`chat_surface.go:529/960/1149`）同样带派生 ID。
  这些身份**都是一次性的**（每次评审/推荐/审批都是新 id），且都发生在会话行已存在之后 → 判定对它们零影响。
  其余发布（`publishTUIRunEvent`、`persistRunEvent`、`persistRunTurnError` 等）传空 ID
  （`AppendSessionEventOnce` 内部生成 `evt-<uuid>`，调用方 evt.ID 保持空）→ `markEventShownOnce` 返回 true，行为零变化。
  **全仓唯一"稳定 ID + 会被反复投递"的事件就是 MCP 启动失败。**
- **不做持久化**：进程内记忆即可——进程重启后，会话若已存在，存储层负责去重（`!inserted → 不画`）；
  若会话仍不存在，重新观察到的失败按 D16 再展示一次，这是正确语义（Step 5 场景四会正面断言这一点）。
- **按会话分键**：不同会话的历史互不吞并。
- **规模有界**：map 每个条目 = 一条"本进程真实观察到过的派生身份"，即 MCP 失败按
  (会话, generation, server) 一条，加上一次性的 `approval-*`/`plan-review-*`/LSP 推荐身份各一条；
  单进程生命周期内与真实事件数同阶（一条几十字节），无需上限——**故意不加上限**，因为任意淘汰策略都会
  重新引入"漏画/重画"，与存储层语义不一致；若将来真的需要上限，应与存储层身份语义一起设计。
- **不替代存储层**：规范 §10.1 的既有约定——"the session log's identity is what makes a resubscribing
  surface quiet, **not the surface's own memory**"（`pkg/tui/notify_test.go:781-783` 的注释，
  对应 `TestPublishRunEventLSPRecommendationNotifies`）。本判定**只在存储层无法仲裁的窗口**
  （会话行不存在）起作用：会话行存在时，`AppendSessionEventOnce` 的答案先落地
  （`!inserted → return nil` 提前返回；首次插入走 `default` 且本会话从未画过 → 照旧绘制）。
  因此"存储层是唯一仲裁者"的既有结论仍成立，本判定只是它的补集。
- **触发面极窄**：全仓只有 MCP 失败事件在"行不存在"时被反复投递；`approval-*` / `plan-review-*` 等派生 ID
  事件虽然也走同一判定，但它们都发生在会话/run 存在之后，行为与今天完全一致。
- **已知且接受的边角**：若某次投递发生在"会话切换窗口"内（订阅回调针对刚离开的会话），claim 会被消耗
  而绘制被 `notifyUIForSession` 的会话门挡掉。这种情况下该事实仍会被落库（若行已存在），切换回去时由回放
  画出；若行不存在则不画也不存，与 D16 的"不属于任何会话的历史"一致。

### 4.2 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 是否刻意 |
|---|---|---|---|
| 会话行不存在，MCP 启动失败（一次失败，多次快照投递：注册表变化 + markSettled） | 同一失败画 **2 张**卡，库里 0 条 | 画 **1 张**卡，库里 0 条 | 刻意修订（本 bug） |
| 承上，随后第一次 `/model`（建行 + Load 重放） | 补画**第 3 张**卡 + 落 1 条事件 | **不再补画**；落 1 条事件 | 刻意修订（本 bug）。live 1 张 / 库 1 条 / resume 回放 1 张，1:1 |
| 承上，resume 该会话（新进程） | 回放 1 张（与 live 的 3 张不一致） | 回放 1 张（与 live 的 1 张一致） | 顺带修正 |
| 会话行已存在（先发消息，MCP 之后失败） | 1 张卡 + 1 条事件（存储层去重） | **不变** | 不变 |
| 同一会话，MCP 配置变化后新 generation 再次失败 | 再画 1 张（重试可见） | **不变**（ID 含 generation） | 不变 |
| 同一会话，失败重试成功（generation 不同、无 error） | 无卡 | **不变** | 不变 |
| 被 Esc 跳过的 optional server（`ConnStatusCancelled`） | 无卡（非 error） | **不变** | 不变 |
| 会话切换 A→B→A（A 无行） | **不可达**（见下）| 不可达 | 不涉及 |
| 从无行的 A 切走到 B | A 的卡片随 A 的 timeline 一起消失（从未落库），无法回到 A | **不变** | 不变 |
| 其它事件（随机 ID：turn_started/completed、approval_resolved 的随机部分、tool 事件…） | 各自一次性 | **不变**（空 ID 直接放行） | 不变 |
| web（gateway）会话 | 无该生产者，不涉及 | **不变** | 不变 |

**"无行的会话能否再被切回"= 不可达**（评审意见 1 的核实结论，已读源码确认）：

- 会话切换的每一个入口都只认已有行的会话，且目标在切换时就建行：
  - `/resume` 选择器：列表来自 `fb_sessions`（`pkg/tui/chat_surface.go:238-261` → `ListSessionsRecent(Paged)`，
    `pkg/tui/run.go:4764-4780`），**无行的会话根本不在列表里**；
  - `/resume <id>`（inline）：`pkg/turn/executor.go:646` 先 `Ensure(ctx, id, id)` 再 `SessionSwitched`；
  - `/new`：`pkg/turn/executor.go:287` 先 `Ensure`；
  - fork（`/fork`）：`pkg/turn/executor.go:367-390` 走 `ForkInto`（建目标行）；
  - 迁移等其它 `SessionSwitched`：`pkg/tui/run.go:4477` 的 `outcome.SessionID` 同样出自上面这些命令。
- 因此"无行的 A"一旦被切走就再也回不去；它屏幕上那张未落库的卡片随之消失（今天也是这样，本计划不改变）。
  另一面：切换回任何会话都会用库回放重建 timeline（`pkg/tui/commands.go:85-126`；`len(turns)==0 && len(events)==0`
  直接 return），所以**修复不会造成"卡片消失"**：有行的会话由库回放给出那张卡，无行的会话本来就回不去。
- 结论：判定里的"已画过"记忆只是**同一屏内的幂等**，不承担跨切换的可见性职责。

### 4.3 备选方案与否决理由

1. **把 claim 放在存储分支之前（不落库 + 不重画）**：live 1 张、库里 0 条、resume 回放 0 张 →
   破坏"replay 与 live 1:1"红线。**否决**。
2. **TUI 启动即建会话行（`Ensure`），让存储层成为唯仲裁者**：改动最小、确实能消除重复，但会让每次启动都
   在会话列表里留下一个从未使用过的空会话（产品级可见变化，title 是会话 id），且不覆盖"未来任何 pre-session
   订阅型生产者"这一类缺陷。**否决**（并记录为产品令：会话行仍在首次使用时创建）。
3. **只让 `recordMCPStartupFailures` 在"结算"时记录一次**：结算仍有多次投递（注册表变化 + markSettled），
   且每次 Load 的重放同样投递结算后的快照，挡不住。**否决**。
4. **对 `ErrSessionNotStarted` 分支加 `HasSession` 轮询/延迟重试**：引入时序依赖与后台重试，复杂度高于收益。**否决**。

---

## 5. Scope

**In scope（本计划必须改/加）**

- `pkg/tui/notify.go`：`publishRunEvent` 增加"已画一次"判定；新增 `markEventShownOnce`（含注释说明与存储层的关系）。
- `pkg/tui/chat_session.go`：`ChatSession` 增加 `shownEventsMu` / `shownEvents` 字段（放在 `mcpWatch*` 附近并写清用途）。
- `pkg/tui/notify_test.go`：新增直接回归测试（见 §7 Step 3）。
- `docs/plan/MCP_FAILURE_CARD_REPEAT_PLAN.md`：本计划的仓库落点 + 验收记录回填。

**Out of scope（明确不碰）**

- `pkg/run/*`（MCP 生命周期、hub、segment 缓存、fingerprint/generation 语义）——它们的行为是正确的。
- `pkg/state/*`（`AppendSessionEventOnce`、`ErrSessionNotStarted`、schema）——存储层已是唯一仲裁者。
- `pkg/gateway/*`、`frontend/*`（web 无该生产者；web 的 MCP 状态订阅只做状态行）。
- 会话行创建时机（D16 语义）——保持不变。
- MCP 启动本身为什么超时（`caveman-shrink` 环境问题）——不是本 bug；但要能在报告里说明"卡片内容真实"。
- 其它历史重复工具卡（`fb_messages` 里 12-13 条的旧会话行）——迁移遗留，另行处理，本计划不动。

---

## 6. 影响的既有测试（预期无需改断言）

- `pkg/tui/notify_test.go:88 TestMCPStartupFailureIsStoredOnceAndShownOnce`：
  第二次发布走 `!inserted → return nil`（在 claim 之前），断言不变。
- `pkg/tui/notify_test.go:219 TestMCPStartupFailureBeforeTheSessionExistsIsShownButNotStored`：
  先 `recordMCPStartupFailures("ghost", gen-1)` 一次（展示 1 次），再用 **gen-2** 对已建行的 `s1`（ID 不同）→ 不受影响。
- `pkg/tui/notify_test.go:170 TestStoredMCPFailureReplaysWithItsHeadingAndText`：回放路径不经 `publishRunEvent`，不受影响。
- `pkg/tui/notify_test.go:784 TestPublishRunEventLSPRecommendationNotifies`（派生 ID 重复发布只展示一次）：
  第二次发布仍走 `!inserted → return nil`，断言不变。
- `pkg/tui/chat_session_test.go:29051 TestConversationApprovalEventsDoNotNotifyTheSurfaceTwice`、
  `pkg/tui/chat_session_test.go:29092 TestForeignSessionEventsStayOffTheScreen`
  （含 `publish("ended-void","")` 的**空会话 id** 必须照旧绘制）：`markEventShownOnce` 对空 id 返回 true，断言不变。
- Step 4 的全量回归必须包含上面的文件级验证：`go test -tags fts5 ./pkg/tui -run 'TestPublishRunEvent|TestConversationApproval|TestForeignSessionEvents|TestMCPStartup|TestStoredMCPFailure' -count=1`。

---

## 7. Steps（每步含验证命令与预期）

> 全部在项目根目录执行；`CGO_ENABLED=1` 与 `-tags fts5` 是本仓库硬性要求。

**Step 0 — 漂移检查（先做）**
```bash
git rev-parse --short HEAD            # 预期 9656051（或含 owner 新提交，如不同先核对本计划引用的 file:line）
sed -n '739,767p' pkg/tui/notify.go   # 预期仍是 §3.1-4 的发布路径
```
预期：file:line 与本计划一致；不一致则按函数名/测试名定位后再改（STOP 见 §9）。

**Step 1 — 字段与方法**
- `pkg/tui/chat_session.go` 在 `mcpWatch*` 字段块后新增：
  ```go
  // shownEvents* remember which events with a derived identity this surface has
  // already painted, keyed per conversation. [...]
  shownEventsMu sync.Mutex
  shownEvents   map[string]struct{}
  ```
- `pkg/tui/notify.go` 新增 `markEventShownOnce`，并在 `publishRunEvent` 的存储分支之后加判定（§4.1）。

验证：
```bash
CGO_ENABLED=1 go build -tags fts5 ./pkg/tui/    # 预期：无输出、exit 0
go vet ./pkg/tui/                                # 预期：无输出
```

**Step 2 — 让"每一次快照投递"都走同一条路（不需要改代码，只做确认）**
确认 `recordMCPStartupFailures` 的 goroutine 内仍逐个调用 `publishRunEvent`（notify.go:1163-1176），
即每次投递都会经过新的判定。

**Step 3 — 新增直接回归测试（`pkg/tui/notify_test.go`）**

新测试 `TestMCPStartupFailureBeforeTheSessionExistsIsShownOnceAndItsRecordPaintsNoSecondCopy`：
1. 建临时 home + `state.OpenStateForTest` + `state.NewRunStore` + `NewSessionStore`（照抄
   `notify_test.go:219-257` 的 `sessionEnv{Home, SQL, SessStore, RunSvc}.session()` 构造）；
2. `session.PrependUINotify` 收集 `NewMessageMsg{Kind: MsgKindError}` 到带缓冲 channel；
3. `session.recordMCPStartupFailures("ghost", run.MCPSnapshot{Generation:"gen-1", Servers: []mcp.ServerRecord{mcpFailureRecord("docs","connection refused")}})`；
   等第一条（`requireUIMessage(t, shown)`，`pkg/tui/chat_surface_test.go:84` 的 helper）；
4. **再投递同一快照两次**（模拟注册表变化 + markSettled + 每次 Load 的重放）：
   `require.Never(t, func() bool { select { case <-shown: return true; default: return false } }, 300*time.Millisecond, 10*time.Millisecond)`；
   同时断言 `runs.ListSessionEvents(ctx,"ghost",0,0,50)` 仍为空（D16：不落库）；
5. `sessions.Ensure(ctx,"ghost","ghost")` 之后再 `recordMCPStartupFailures("ghost", 同一快照)`：
   断言库里出现 **1 条** `mcp:gen-1:docs:error`，且**没有**第二条 UI 卡片（同一 `require.Never`）。

验证：
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestMCPStartup|TestStoredMCPFailure' -count=1
# 预期：全部 PASS（含新增用例）
```
（反向验证可选：临时把 `markEventShownOnce` 的返回改成恒 true，新用例必须 FAIL——证明测试真的锁住了缺陷。）

**Step 4 — 包级回归**
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/state ./pkg/run ./pkg/turn -count=1
go vet ./...
```
预期：全绿（既有 MCP 三例测试不改断言）。

**Step 5 — tmux 真机验收（必须，测试绿 ≠ 真机可用）**

按下述脚本复现（隔离 home，严禁污染真实 `~/.forebrain*`）：
```bash
# 1) 构建（带 fts5）
CGO_ENABLED=1 go build -tags fts5 -o /tmp/fb-mcp/forebrain ./cmd/forebrain
# 2) 隔离 home：§2.2 的两模型 + sleep-300 MCP server 配置；临时项目目录
# 3) fake provider + tmux（见 .claude/skills/run-forebrain/driver.sh 的做法）
tmux new-session -d -s fbmcp -x 120 -y 42 -c /tmp/fb-mcp/proj \
  "FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME=/tmp/fb-mcp/home /tmp/fb-mcp/forebrain 2>/tmp/fb-mcp/tui.err"
tmux send-keys -t fbmcp Down Enter          # 信任目录
sleep 40
tmux capture-pane -t fbmcp -p | grep -c "mcp slowpoke"    # 预期：1（修复前为 2）
# 4) 做两次 /model 切换（含 effort 选择）
tmux send-keys -t fbmcp -l "/model"; tmux send-keys -t fbmcp Enter; ...
tmux capture-pane -t fbmcp -p | grep -c "mcp slowpoke"    # 预期：仍为 1（修复前为 3）
# 5) 库侧：每个会话 1 条事件
sqlite3 /tmp/fb-mcp/home/state/forebrain.state.sqlite \
  "select count(*) from fb_sessions; select sequence,event_id from fb_session_events;"
# 预期：fb_sessions=1；fb_session_events 恰好 1 行 mcp:…:slowpoke:error
# 6) 场景二（回归）：先发一条消息让行存在，再触发一次新 generation 的 MCP 失败
#    → 仍恰好 1 张卡 + 库里恰好 1 条（存储层仲裁路径不变）
# 7) 场景三（resume 1:1）：resume 该会话 → 回放出的 "mcp slowpoke" 卡片数 == live 的 1
# 8) 场景四（D16 语义保持 + 记忆不跨进程）：在任何会话行诞生前 kill 掉 TUI 进程，
#    用同一个 home 重启（新 session id、无行），等到 30s 超时：
tmux kill-session -t fbmcp; sleep 2
tmux new-session -d -s fbmcp -x 120 -y 42 -c /tmp/fb-mcp/proj \
  "FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME=/tmp/fb-mcp/home /tmp/fb-mcp/forebrain 2>/tmp/fb-mcp/tui.err"
sleep 40
tmux capture-pane -t fbmcp -p | grep -c "mcp slowpoke"    # 预期：1（新进程重新展示一次，既不是 0 也不是 2）
sqlite3 /tmp/fb-mcp/home/state/forebrain.state.sqlite \
  "select (select count(*) from fb_sessions), (select count(*) from fb_session_events);"
# 预期：0|0 —— 会话行仍不存在，按 D16 不落库
# 9) 清理：tmux kill-session -t fbmcp; 删除 /tmp/fb-mcp（临时目录，不影响真实 home）
```

**Step 6 — 收口**
- 把本文件复制到 `docs/plan/MCP_FAILURE_CARD_REPEAT_PLAN.md`，状态改为"已实施并真机验收通过（日期）"，
  追加"验收记录"（场景 → 证据，含踩过的工具链坑）。
- `git status` 核对改动范围与 §5 Scope 一致；**不 commit**。

---

## 8. Done criteria（机器可查）

1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` 全绿，且新增回归用例存在并通过。
2. `go vet ./...` 无输出。
3. tmux 真机：修复前 2→修复后 1（未切换模型）；修复前 3→修复后 1（含 2 次模型切换）；
   `fb_session_events` 中该会话恰好 1 条 `mcp:<gen>:<server>:error`；Step 5 场景四（重启后仍无行）
   恰好 1 张且库中 `0|0`。
4. `git status --porcelain -- pkg/` 只包含 `pkg/tui/notify.go`、`pkg/tui/chat_session.go`、`pkg/tui/notify_test.go`。
5. resume 该会话回放出的 MCP 卡片数 = live 时的卡片数（1:1）。

---

## 9. STOP conditions（实施中必须停下来报告）

- `git rev-parse --short HEAD` 与计划基线差异大，或 `pkg/tui/notify.go` 的 `publishRunEvent` /
  `recordMCPStartupFailures` 已被改成另一套去重机制（说明有人在修同一个 bug）→ 停止，报告后再说。
- 新判定导致**任何**既有测试失败（除本计划允许的语义修订外）→ 停止，报告失败断言与原因，不擅自改断言。
- 真机验证出现"库里 0 条 + 屏幕上 1 张且 resume 后 0 张"→ 说明落库被一起挡掉了 → 停止并报告（这违反 §4.3-1 的红线）。
- 发现重复卡片还有**第二条生产者**（例如 gateway 也在发布 MCP 失败）→ 停止，重新界定 Scope 后再实施。

---

## 10. Maintenance notes

- **这个"已画过"记忆只覆盖"存储层无法仲裁"的窗口**。如果将来新增"每次投递都会重放"的订阅型生产者
  （同类：任何 `Subscribe(...)` 回调里发布事件的地方），必须复用 `markEventShownOnce`，否则同类缺陷会以
  新事件类型复现。判据：**事件 ID 是否是派生（稳定）身份**。
- 反向风险：**不要**把该判定搬到存储分支之前——那会让 pre-session 事实永远不落库，破坏 live/replay 1:1。
- 不要为解决本 bug 而改成"启动即建会话行"：会话行在首次使用时创建是既定产品语义（D16 的前提）。
- 评审要点：新判定必须放在 `switch evt.Type` 之前、存储分支之后；`markEventShownOnce` 对空 ID 必须放行。
- 与 `mcpWatch*` 的关系：会话切换会 `WatchMCPStartup(next)` 重新订阅并回放；记忆按会话分键。注意"无行的会话
  不可再被切回"（§4.2 注），所以这份记忆只负责**同一屏内的幂等**，不承担跨切换的可见性职责；跨切换的可见性
  始终由库回放负责。
- **owner 裁决（2026-10-10）**：MCP 的连接与启动**只在会话开启时执行一次**；会话中途切换模型**不得**再执行
  连接/启动操作。当前实现即为此语义：`/model` 走 `SetPrimaryModel → loadLocked → startOrReplayMCPSegmentLocked`，
  fingerprint 不含模型，命中复用分支——启动进行中则续接原 generation（`attachAgent`，不重启），已结算则纯缓存
  回放（`replayMCPSegmentLocked` "without contacting any server"）。只有 MCP 配置变化（fingerprint 变）才开新代。
  任何未来改动不得让模型切换触碰 MCP 生命周期（验收证据见 §12.6）。

---

## 11. 评审修订记录（2026-10-10）

本计划经外部模型（zhipuai / glm-5.3）评审一轮，逐条核实后修订如下。

1. **采纳**："A→B→A（A 无行）→ 不补画"这一矩阵行未经证实，且可能把"屏幕彻底看不到卡片"说成刻意修订。
   → 已读源码确认该场景**不可达**（会话切换的每个入口都只认 `fb_sessions` 里的会话，且目标在切换时 `Ensure`/`ForkInto`；
   `/resume` 列表来自 `ListSessionsRecent(Paged)`）。矩阵行已改为"不可达/不改变"，并在 §4.2 加了带证据的说明，
   明确"修复不会造成卡片消失：无行的会话本就回不去"。
   → Step 5 增加**场景四**（重启后会话仍无行 → 恰好 1 张且库 `0|0`），正面断言 §4.1 声明的 D16 语义。
2. **部分采纳**：评审指出 §4.1 里"`plan-review-*` 用派生 ID"这一句需要订正——它说 plan-review 走
   `publishTUIRunEvent` 的空 ID。核实结果：**评审此点不成立**。生产环境 `runner.Events = event.SinkFunc(s.publishRunEvent)`
   （`pkg/tui/notify.go:1350`），运行时发布的 `plan-review:<reviewID>:started|reviewed`
   （`pkg/turn/approval.go:1489-1504`）**带派生 ID 且确实经过 `publishRunEvent`**；评审引用的
   `publishTUIRunEvent`（`chat_turn.go`）是另一条发布路径。不过评审的**结论**（行为不受影响）成立：
   这些身份每次都是新 uuid / 新 rec.ID / 新 action id，且都发生在会话行存在之后。已把该段按核实事实重写。
3. **采纳**：`shownEvents` 增长量级需说明 → §4.1 增加"规模有界"条目（与真实事件数同阶，一条几十字节；
   故意不设上限，理由写清）。
4. **未验证项**：评审未复核用户日志与隔离复现的具体计数（2→3 张）。这两项是本次会话**实测**所得
   （§2.1/§2.2 的原始命令与输出），保留为根因证据。

---

## 12. 实施与验收记录（2026-10-10）

### 12.1 实施

- 基线核对：`git rev-parse --short HEAD` = `9656051`，与 Planned-at 一致；in-scope 的 `pkg/tui`、`pkg/run`、
  `pkg/turn` 无未提交改动（工作区另有他人会话遗留的 `frontend/*`、`pkg/gateway/*`、`pkg/state/cron*` 改动，
  均与本计划无关，未触碰）。
- diff（`git diff --stat -- pkg/tui/`）：`chat_session.go +10`、`notify.go +47`、`notify_test.go +69`，
  共 **126 insertions, 0 deletions**，纯增量，与 §5 Scope 一一对应；未 commit（改动留工作区）。
- 反向验证（§7 Step 3 括号项）：临时把 `markEventShownOnce` 改为恒 `return true` 后，新用例
  在第一个 `require.Never` 处 FAIL（`Condition satisfied`，即第二张卡真的到来）；还原后 PASS——测试确实锁住缺陷。

### 12.2 单测与静态检查

| 命令 | 结果 |
|---|---|
| `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestMCPStartup|TestStoredMCPFailure' -count=1` | ok（含新增用例） |
| `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | ok（43.7s） |
| `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui ./pkg/state ./pkg/run ./pkg/turn -count=1` | 全部 ok |
| `go vet ./...` | 无输出，exit 0 |
| `gofmt -l pkg/tui/notify.go pkg/tui/chat_session.go pkg/tui/notify_test.go` | 无输出（清洁） |

### 12.3 tmux 真机验收（隔离 `/tmp/fb-mcp`，fake provider + sleep-300 MCP，验收后已清理）

| 场景 | 屏幕卡片数（grep -c "mcp slowpoke"） | 库（fb_sessions / fb_session_events） | 判定 |
|---|---|---|---|
| 1. 启动等到 30s 超时（未发消息、未切模型） | **1**（修复前 2） | 0 / 0 | ✅ |
| 2. 承上做两次 `/model` 切换（chat→reasoner→chat，含 effort 选择） | **1**（修复前 3） | 1 / 1（`mcp:61e5c6b338a9:slowpoke:error`） | ✅ |
| 3. resume 该会话（kill 后 `forebrain resume cli-ee210fb2-…`，等 MCP 再超时） | **1**（= live 的 1，1:1） | 1 / 1（live 重失败被存储层去重，无新增） | ✅ |
| 4a. 无行时 kill 重启（同 home、新 session id） | **1**（既不是 0 也不是 2） | 0 / 0 | ✅（D16 + 记忆不跨进程） |
| 5. 先发消息建行，再等 MCP 失败（存储层仲裁路径） | **1** | 1 / 1 | ✅（行为不变） |

场景 4 的第一阶段（首次启动后未做任何操作即查库）与 12.3 第一行同环境同结果（1 张、0/0），此处合并记录。

### 12.4 工具链坑（复跑时注意）

- **YAML flow 写法里 `api_key: ${FAKE_LLM_KEY}` 会解析失败**：flow mapping `{...}` 内的 `${` 被当作嵌套
  flow 开始，报 `yaml: line 12: did not find expected ',' or '}'` 且 TUI 直接 exit 1。复跑 §2.2 配置时
  两个 provider 要用 block 写法（本文件 §2.2 的片段是计划原文，实跑时已改为等价 block 形式）。
- **遗留 fake provider 占端口**：上一会话的 `fake_provider.py` 未随目录清理而死在后台，新实例
  `OSError: [Errno 48] Address already in use`。复跑前先 `pkill -f fake_provider.py` + 确认 `lsof -ti :8731` 为空。
- **构建产物要带词典**：memory search 从二进制旁读词典，构建后需 `scripts/install-dictionary.sh <dir>`。

### 12.5 Done criteria 对照

1. ✅ `./pkg/tui` 全绿且新增用例存在并通过（含反向验证）。
2. ✅ `go vet ./...` 无输出。
3. ✅ 真机 2→1、3→1、库恰 1 条、场景四 1 张 + `0|0`（§12.3）。
4. ✅ 本次改动在 `pkg/` 下只新增 `pkg/tui/notify.go`、`pkg/tui/chat_session.go`、`pkg/tui/notify_test.go`
   三个文件的修改（工作区另有计划前即存在的无关改动，见 §12.1）。
5. ✅ resume 回放卡片数 = live 卡片数 = 1（§12.3 场景 3）。

### 12.6 owner 裁决复核：切换模型不执行 MCP 连接/启动（2026-10-10）

owner 裁决："应该是在开启新会话时只尝试连接和启动一次 mcp，会话中途切换模型不再执行连接和启动 mcp 操作"。
代码与真机双重核实，**当前实现（含本修复）已满足该语义**，无需再改 runner：

- 代码路径：`/model` → `SetPrimaryModel`（`pkg/run/config.go:1401`）→ `loadLocked` →
  `startOrReplayMCPSegmentLocked`（`pkg/run/runner.go:2136`）。`mcpSegmentFingerprint`（`:1754`）只含
  server 配置与项目目录、不含模型 → 模型切换必然命中两个复用分支之一：
  启动进行中（`:2138-2147`）续接同一 generation（`attachAgent`，不重启任何 server）；
  已结算（`:2148-2152`）走 `replayMCPSegmentLocked`（`:2530`，注释明言 "without contacting any server"）。
  只有 fingerprint 变化（MCP 配置被编辑）才会 `newMCPLoadLocked` 开新代。
- 真机证据（隔离 `/tmp/fb-mcp`，同 §2.2 配置，验收后已清理）：
  - 在 connecting 窗口内做第一次 `/model` 切换：`sleep 300` 子进程 PID **90943 → 90943**（未重启）；
  - 全程日志只有**一条** `agent tools ... generation=61e5c6b338a9 ... error=1 elapsed=30s`（整个进程只
    spawn 过一次 slowpoke、只 settle 过一次）；
  - 切换触发的重载日志：结算前 `agent load ... mcp_pending=true`（复用 in-flight 代）、结算后
    `mcp_pending=false`（纯回放），generation 恒为 `61e5c6b338a9`；
  - 两次切换 + 一次 30s 失败：屏幕恰 1 张卡、库 1 会话 + 1 事件（`mcp:61e5c6b338a9:slowpoke:error`）。
- 结论：用户原先看到的"切换模型重复出现 MCP 卡片"不是重连/重启，而是**同一失败快照的重复投递被重复绘制**；
  本修复（发布面幂等）已根除。模型切换需要重建 agent 与工具表（模型变了），但 MCP 段按 fingerprint 复用，
  连接与子进程不受影响——这正是裁决要求的语义，作为维护红线记入 §10。
