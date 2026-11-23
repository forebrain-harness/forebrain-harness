# Plan 094: 被 provider 拒过的图片记进会话（粗档 provider+model、落库），且**当场**就告诉用户

> **Executor instructions**: 按步骤执行，每步跑完「Verify」并确认输出符合预期再进入下一步。
> 命中「STOP conditions」任一条就停下来报告，不要即兴改动。全部完成后更新
> `plans/README.md` 里本计划那一行的状态（除非派发你的评审方说他维护索引）。
>
> **Drift check（先跑）**: `git diff --stat 8d3322c..HEAD -- pkg/llm/image_heal.go pkg/run/image_input.go pkg/run/runner.go pkg/event/run_events.go pkg/state/schema.sql pkg/state/session_store.go pkg/tui/notify.go pkg/tui/commands.go pkg/gateway/server.go frontend/src/composables/useChatStream.ts frontend/src/views/ChatView.vue`
> 若有变动，先把「Current state」的摘录与现码逐行对照；对不上按 STOP 处理。
>
> **依赖提醒**: 本计划假设 plans/092 已执行（`withImageHeal` 同时装在主链与 `summaryChain` 上）。
> 若 092 未执行，Step 5 的"两条链都会上报"按现码实际情况接一次即可，不要为它先做 092。

## Status

- **Priority**: P1（这是一条用户看不见的成本与困惑）
- **Effort**: M–L
- **Risk**: MED
- **Depends on**: plans/092-image-heal-on-the-compaction-chain.md（软依赖，见上）
- **Category**: bug（成本 + 可见性）
- **Planned at**: commit `8d3322c`, 2026-10-21

## Why this matters

provider 因配置里没写的原因拒掉一张图时，`llm.NewImageHeal` 把图降级成一句文本重发，这一轮因此"成功"。两个后果：

1. **成本**：transcript 保留原图（这是既有设计），于是这个会话之后**每一轮**都要再白送一次被拒的请求——永久 2×。
2. **困惑**：界面上一个字都没有，用户以为模型懒得看图。

本项目对"图发不出去"本来有一条完整通路：`run.ImageTurnRefusal` 在**发请求之前**判定，命中则**只扣下请求、不丢消息**，
返回 `*ImageInputRefusedError`，TUI 显示本轮错误、网关发 `turn_refused`、web 画 `imageRefused` 提示并指向 `/model`。
本计划把 heal **学到的**拒绝回流给它，并补上第一轮的当场提示：

- **立刻**（本轮）：run 内发一条 `image_rejected` run event，两端各画一行"这轮的图被拒了、已按文字发出"；
- **之后**（下一次请求前）：粗档记录（`provider`+`model`）让既有闸门直接拦下，用户看到既有的"换模型后继续"。

**owner 2026-10-21 裁决**：粒度取**粗档**（一次被拒 ⇒ 该会话在该 `provider`+`model` 上判定为看不了图，换模型即恢复），
持久化取**落库**。两条都按此实现，不再讨论细档（"哪一张图"）。

## Current state

- `pkg/llm/image_heal.go:117-142` — 自愈，命中后只写日志并重发：

```go
func (h *imageHealLLM) Execute(ctx context.Context, messages []Message, tools []*Tool) (*Result, error) {
	result, err := h.inner.Execute(ctx, messages, tools)
	if err == nil || !IsImageRejection(err) { return result, err }
	degraded, replaced := DegradeImages(messages, err.Error())
	if replaced == 0 { return result, err }
	slog.Warn("provider rejected an image; retrying with the image degraded", "replaced", replaced, "err", err)
	return h.inner.Execute(ctx, degraded, tools)
}
```

  `IsImageRejection`（`pkg/llm/errors.go:277-313`）只判状态码与 7 个措辞短语，**不指向具体哪一张图**——这正是"粗档"成立的理由，
  也是"提示不许点名某张图"的理由。
- `pkg/run/image_input.go:71-84` — 既有的发送前闸门（本计划要扩的就是它）：

```go
func ImageTurnRefusal(ctx context.Context, r *Runner, sessionID string, attachesImage bool) error {
	...
	provider, model, refused := imageInputRefusedModel(ctx, r)
	if !refused { return nil }
	if !attachesImage && !sessionHistoryCarriesImage(ctx, r, sid) { return nil }
	return &ImageInputRefusedError{Provider: provider, Model: model}
}
```

  `imageInputRefusedModel`（`:111-125`）只看模型目录的 `SupportsAttachments`，对不认识的模型 **fail-open**；
  `sessionHistoryCarriesImage`（`:89-106`）按真实请求重建历史、搜 image part。
- `pkg/run/image_input.go:14-30` — 错误类型与 wire code：

```go
const ImageInputRefusedCode = "image_input_unsupported"
type ImageInputRefusedError struct { Provider string; Model string }
```

- **两个消费点都已在位**，本计划不重写它们：
  - 终端：`pkg/tui/chat_surface.go:182` 调 `run.ImageTurnRefusal`，错误交给 `dispatchUserTurnContent`
    （`pkg/tui/chat_session.go:2084-2109`：消息照常落库、**不发起 run**、返回该错误）；
  - 网关：`pkg/gateway/server.go:1734` 调同一闸门；`:1894-1896` 发 `turn_refused`，`Data` 带 `{code, provider, model}`；
  - web：`frontend/src/composables/useChatStream.ts:4872-4889` 把占位消息换成 `role:'notice'` + `imageRefusal`，
    `frontend/src/views/ChatView.vue:82-95` 画提示，文案 `frontend/src/locales/index.ts:1504-1506`(zh) / `:3251-3253`(en)。

- **run 事件在本仓是唯一的"live 可见 + 断线重连可回放 + 不进模型请求"通道**，且两端接线都已完成：
  - 发布：`pkg/run` 侧有 `Runner.Events event.Sink`（`pkg/run/runner.go:132`），经 `r.publishSurfaceEvent`
    （`runner.go:1596-1601`）与 `publishEvent(ctx, sink, runID, sessionID, typ, payload)`
    （`pkg/run/subagent.go:1783-1792`）发布；事件常量族在 `pkg/event/run_events.go:9-60`。
  - 网关：`Runner.Events` 接的是 `s.RunEvents()`（`pkg/gateway/run_control.go:702` 的 `Events: s.RunEvents()`），
    即 `runEventBus`；`Publish`（`pkg/gateway/wsevents.go:603-649`）先把事件**落库**
    （`b.store.AppendSessionEvent`）再 `deliver` 给连接；到前端的 op 由 `runEventToWSMessage`（`pkg/gateway/server.go:824-833`）
    统一成 `op:"run_event"` + `Data.RunEvent`。**因此本计划不需要改网关 `/api` 或 op 表。**
  - 终端：`Runner.Events` 接 `event.SinkFunc(s.publishRunEvent)`（`pkg/tui/notify.go:1430`）；`publishRunEvent`
    （`notify.go:753-800`）先 `AppendSessionEventOnce` 落库（`!inserted` ⇒ 直接 return，即**第二次观察同一事实不重画**），
    再 `markEventShownOnce`，最后 `switch evt.Type` 分派成 `NewMessageMsg`（例如 `RunEventAssistantDelta` 在 `:801-806`）。
  - 终端回放：`pkg/tui/commands.go:389-470` 逐条把事件变成消息/帧，`!ok` 就 `continue`；
    插入点形如 `msg, ok := replaySubagentRunEventMessage(evt)`（`:401-410`）。
  - web 侧：`applyObservedEvent`（`useChatStream.ts:2836`）→ `handleRunEvent`（`:4026`），后者用 `appliedEventIDs`
    与 `sessionEventCursor` 去重（`:4032-4037`）；`historical`（重载回放）会走同一函数，除非该 type 列在
    `transcriptBackedEventTypes` 里（`：2866` 的抑制只对"内容已在 transcript 里"的类型生效）。
- **落库幂等的既有范式**（本计划照抄，不新造）：`"turn-started:" + runID` 这种**固定 id**
  （`pkg/tui/chat_session.go:2143` 的注释："The fixed id keeps a continuation past an approval — the same run — from
  recording a second start"）；`AppendSessionEventOnce`（`pkg/state/session_events.go:75-118`）在
  `ON CONFLICT(session_id, event_id) DO NOTHING` 上判重，空 id 会变成随机 uuid（=每次都是新行）。
- 落库的会话级状态先例：`pkg/state/schema.sql:180-190` 的 `fb_session_model_state`
  （`session_id` 主键、`updated_at`、`WITHOUT ROWID`、`ON DELETE CASCADE`），读写见
  `pkg/state/session_store.go:2820-2880`（`s.db` + `s.requireOwned` + `s.AgentID()`）。该表注释给的理由本计划直接引用：
  "It lives with the session rather than in process memory … a resumed session gets the same bytes back after a restart,
  and the value is released when the session it belongs to is."
- schema 机制：`pkg/state/schema_migrations.go:21` `const stateSchemaVersion = 15`；`:41-57` 的 `stateSchemaMigrations`；
  `:703` `migrateStateV14ToV15` 是最近一次的写法（单条 `CREATE TABLE IF NOT EXISTS`、纯追加）。
  `migrateStateSchema`（`:1575`）在空库时直接执行 `schema.sql`——**新表必须同时出现在 `schema.sql` 和一次迁移里**。
- `pkg/event.IsPushOnlyStreamEvent` 是**白名单**（命中才"只推不存"），所以新 type 默认会落库、可回放。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| event 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/event -count=1` | `ok` |
| state 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` | `ok` |
| llm 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -count=1` | `ok` |
| run 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | `ok` |
| tui 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` | `ok` |
| gateway 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | `ok` |
| vet | `go vet ./pkg/event ./pkg/state ./pkg/llm ./pkg/run ./pkg/tui ./pkg/gateway` | 无输出 |
| 包图 | `scripts/package-graph.sh` | 无 diff |
| 前端单测 | `cd frontend && pnpm test -- src/composables/useChatStream.test.ts src/views/ChatView.test.ts` | 全 passed |
| 前端全量 | `cd frontend && pnpm test` | 全 passed |
| 类型检查 | `cd frontend && pnpm exec vue-tsc --noEmit` | exit 0 |
| e2e | `FOREBRAIN_E2E_SPEC="e2e/message-cards.spec.ts e2e/chat-composer.spec.ts" scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |

Go 测试必须带临时 home（`FOREBRAIN_HOME="$(mktemp -d)"`），见 `FOREBRAIN.md` 的 House rules。

## Scope

**In scope**：
- `pkg/event/run_events.go`（一个常量 + 一个 payload 结构）
- `pkg/state/schema.sql`、`pkg/state/schema_migrations.go`（`stateSchemaVersion` → 16、新迁移、注册）
- `pkg/state/session_store.go`（两个方法）+ `pkg/state/schema_migrations_test.go`、`session_store_test.go`
- `pkg/llm/image_heal.go` + `image_heal_test.go`（观察者钩子）
- `pkg/run/image_input.go`、`pkg/run/runner.go`（`RunContent` 装钩子）+ `pkg/run/image_input_test.go`
- `pkg/tui/notify.go`（一个 `case`）、`pkg/tui/commands.go`（一个 replay 助手）+ `pkg/tui/overlays_test.go` 或相邻用例文件
- `pkg/gateway/server.go`（`turn_refused` 的 `code` 分支）
- `frontend/src/composables/useChatStream.ts`（notice 的第三种形态）+ `useChatStream.test.ts`
- `frontend/src/views/ChatView.vue`（同一提示块按 kind 换句子）+ `ChatView.test.ts`
- `frontend/src/locales/index.ts`（两对键）

**Out of scope**：
- **不改 transcript、不删任何图**：`DegradeImages` 只影响这一次请求（`pkg/run/llm_middleware.go:262-266`、
  `pkg/tui/chat_session.go:2103-2108` 的注释都是这个决定）；"换模型后继续就能看到每张图"必须继续成立。
- **不做细档**（按"哪一张图"记忆）：owner 已裁决粗档。
- **不往 `fb_messages` 写 `role='system'` 行**：`pkg/state/session_store.go:2434-2445` 的注释写明，没有 boundary part 的
  system 行"the next request replays as an ordinary system message"——那会污染模型请求。提示只能走 run event。
- `pkg/gateway` 的 op 表、`runEventToWSMessage`、`fb_session_events` 的结构：**都不改**。
- `pkg/run/image_input.go` 的模型目录判定与 fail-open 策略：不动。
- `pkg/llm/image_scale.go`（092 的范围）、`pkg/tool/*`：不碰。

## Git workflow

- 直接改当前 `dev` 工作区；**不 commit、不 push**，除非派发方明确要求。
- 若要求提交：`fix(run): report a provider image rejection and remember it per session and model`，body 用
  `Why:` / `What:` / `Verification:`，`git commit -s`。

## Steps

### Step 1: 事件类型（第一轮提示的载体）

`pkg/event/run_events.go`：

```go
// RunEventImageRejected reports that the provider refused an image this run
// carried: the request was retried with the image degraded to a text note and
// the turn continued. It is a run event, not a transcript row, because the
// conversation's own record keeps its images — after /model the same pictures
// are sent to a model that can see them. The refusal names no image: the
// provider's own sentence never does.
RunEventImageRejected = "image_rejected"

// ImageRejectedPayload is one such refusal. Code is the stable wire code
// (see run.ImageRejectedCode); Reason is the provider's sentence, already
// flattened to one line and bounded by pkg/llm.
type ImageRejectedPayload struct {
	Code     string `json:"code,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Reason   string `json:"reason,omitempty"`
}
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/event -count=1` → `ok`。

### Step 2: schema v16 —— 一张会话级的拒绝记录表

1. `pkg/state/schema.sql`，在 `fb_session_model_state` 之后：

```sql
-- One session's observed image rejections, coarsely: a provider that refused an
-- image for this conversation is remembered per provider+model, so the next
-- request on that model is withheld instead of sent to be refused again. A
-- model switch is the recovery the refusal sentence already points at, and it
-- is free here because the lookup is keyed by the model in force. Only a turn
-- whose request would actually carry an image consults this row.
CREATE TABLE fb_session_image_refusals (
  session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  observed_at INTEGER NOT NULL,
  PRIMARY KEY (session_id, provider, model)
) STRICT, WITHOUT ROWID;
```

2. `pkg/state/schema_migrations.go`：`const stateSchemaVersion = 16`；新增 `migrateStateV15ToV16`
   （照 `migrateStateV14ToV15` 的形状：单条 `CREATE TABLE IF NOT EXISTS`，注释说明纯追加、空库与 v15 库同形）；
   在 `stateSchemaMigrations` 末尾注册 `{version: 16, apply: migrateStateV15ToV16}`。
3. 查一下 `stateObjectsAddedAfterV2`（`:3191` 附近）的用途：**只有当它的注释要求登记新增对象时**才加一行。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run TestMigrate -count=1` → `ok`。

### Step 3: `SessionStore` 读写 + 迁移/级联用例

`pkg/state/session_store.go` 追加（照 `SetSessionModelSelection` / `SessionModelSelection` 的形状：`s.requireOwned`、
`s.AgentID()`、`time.Now().Unix()`）：

```go
// RecordImageRefusal remembers that this session's model refused an image, so
// the next request that would carry one is withheld. Coarse on purpose: the
// provider's refusal never says which image it meant.
func (s *SessionStore) RecordImageRefusal(ctx context.Context, sessionID, provider, model, reason string) error

// ImageRefusal returns the recorded reason for this session on this exact
// provider+model, and false when there is none (or the session belongs to
// another primary agent).
func (s *SessionStore) ImageRefusal(ctx context.Context, sessionID, provider, model string) (string, bool, error)
```

要求：
- 空 `sessionID`/`provider`/`model` ⇒ 写方法返回 `nil`（没有身份就没有记录；它只是优化，不报错）；
- 写：`INSERT … ON CONFLICT(session_id, provider, model) DO UPDATE SET reason=excluded.reason, observed_at=excluded.observed_at`；
- `reason` 入库前压单行并截断（`strings.Join(strings.Fields(reason)," ")`，超 200 字符加 `…`，与 `pkg/llm` 的口径一致）；
- 读：精确匹配 `(session_id, provider, model)`，`sql.ErrNoRows` ⇒ `("", false, nil)`。

用例：
- `schema_migrations_test.go`（照现有 "fixture built from the current schema.sql with a lowered user_version" 写法）：
  `user_version=15` 的 fixture 迁移后为 16，新表存在且 5 列；
- `session_store_test.go`：写入后读回同一 reason；换 model 读不到；删会话后行随 CASCADE 消失；另一个 agent 的 store 读不到。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` → `ok`。

### Step 4: `pkg/llm` 把"我拒绝过一张图"报出来（不改变重试语义）

`pkg/llm/image_heal.go`：

```go
// WithImageRefusalObserver runs fn once per provider refusal the heal ladder
// meets, before the degraded retry. It is how a caller that does know the
// provider and model (pkg/run) learns a fact pkg/llm cannot name itself. The
// observer may not change the request or the result: it is a report.
func WithImageRefusalObserver(ctx context.Context, fn func(reason string)) context.Context

// ImageRefusalObserverFromContext returns the observer installed by
// WithImageRefusalObserver, or nil.
func ImageRefusalObserverFromContext(ctx context.Context) func(reason string)
```

在 `Execute` 里 `IsImageRejection(err)` 命中**且** `replaced > 0` 之后、降级重发之前调用一次。
要求：没装观察者时行为一字不变（现用例必须继续过）；观察者不得让本轮回失败；`pkg/llm` 不新增包依赖
（**不要** import `pkg/state` 或 `pkg/run`）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -count=1` → `ok`。

### Step 5: `pkg/run` 装钩子：记一笔 **+** 立刻发一条事件

在 `pkg/run/image_input.go` 加：

```go
// withImageRefusalRecorder installs the one observer the run layer needs: a
// refusal the heal ladder saw is (a) remembered for this conversation and the
// model that produced it, and (b) published once as this run's image_rejected
// event, so the surface can say what happened instead of leaving the person to
// wonder why the picture was ignored. Both are best-effort: a lost row costs one
// request, and a lost event costs one line — never a turn.
func withImageRefusalRecorder(ctx context.Context, r *Runner, sessionID string) context.Context
```

实现要点：
- `provider, model := r.effectiveModelFor(ctx)`（与闸门同一口径；`pkg/run/config.go:1684-1699`）；
- 写库：`r.SessionStore.RecordImageRefusal(context.WithoutCancel(ctx), sessionID, provider, model, reason)`，
  错误只 `slog.Debug`（不是 Warn：会在每轮重复，且不是用户可解决的问题）；
- 发事件：用**固定 id** `"image-rejected:" + runID`（照 `"turn-started:"+runID` 的范式），
  `r.publishSurfaceEvent(ctx, event.NewRunEvent(id, runID, sessionID, event.RunEventImageRejected, event.ImageRejectedPayload{...}, time.Now()))`；
  `runID := strings.TrimSpace(tool.RunIDFromContext(ctx))`，为空就**不发布**（只记账），不要发明一个 id；
  `Code: ImageRejectedCode`；`Reason` 用 `pkg/llm` 压过的那句（`pkg/llm` 已按 200 字符截断，这里不要再截第二遍）。

固定 id 是**幂等**的：网关 `AppendSessionEvent` 在 `(session_id, event_id)` 上 `DO NOTHING`（`pkg/state/session_events.go:110-115`），
终端漏斗在 `!inserted` 时直接 return（`pkg/tui/notify.go:770-781`），web 用 `appliedEventIDs` 去重
（`useChatStream.ts:4032-4034`）。**不需要** `sync.Once`：一轮里有多次 tool-loop 拒绝时，库与两端都会自然只说一次。

然后在 `pkg/run/runner.go` 的 `RunContent`（`:1393` 起）里，紧邻现有 `LatestUserMessageRowID` 那一段之后：

```go
if r.SessionStore != nil {
	if sid := strings.TrimSpace(tool.ConversationSessionIDFromContext(ctx)); sid != "" {
		ctx = withImageRefusalRecorder(ctx, r, sid)
	}
}
```

（不要动 `injectSessionModelSelection` 的语义。）

**Verify**: `go vet ./pkg/run` → 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → 见 Step 8 用例。

### Step 6: 闸门读记录（"下一次不再白送"）

`pkg/run/image_input.go` 的 `ImageTurnRefusal`，在既有目录判定**之后**再加一道，并保持"请求确实会带图"这个前置条件：

```go
provider, model = r.effectiveModelFor(ctx)   // 与上报时同一口径
if reason, recorded, err := recordedImageRefusal(ctx, r, sid, provider, model); err == nil && recorded {
	if attachesImage || sessionHistoryCarriesImage(ctx, r, sid) {
		return &ImageInputRefusedError{Provider: provider, Model: model, Reason: reason, Observed: true}
	}
}
```

`ImageInputRefusedError` 增加 `Reason string` / `Observed bool`，并新增第二个 wire code：

```go
// ImageRejectedCode is the wire code of a turn refused because this
// conversation's model already refused an image on it — the provider's own
// rejection, remembered, not a catalog lookup.
const ImageRejectedCode = "image_rejected_by_provider"
```

`Error()` 在 `Observed` 时换一句（仍是一句、可行动、点名模型、指向 `/model`），`Reason` 非空时句末附
`" (provider said: <reason>)"`。`pkg/gateway/server.go:1894-1896` 的 `code` 按 `detail.Observed` 二选一，
并把 `reason` 一并放进 `Data`。

**Verify**: `go vet ./pkg/run ./pkg/gateway` → 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/gateway -count=1` → 全 `ok`。

### Step 7: 终端 —— live 一行 + 回放一行

1. `pkg/tui/notify.go` 的 `switch evt.Type`（`:799` 起）加一个 case，照 `RunEventAssistantDelta` 的形状：

```go
case event.RunEventImageRejected:
	var p event.ImageRejectedPayload
	if json.Unmarshal(evt.Payload, &p) == nil {
		s.notifyUIForSession(evt.SessionID, NewMessageMsg{Msg: Message{
			Kind:      MsgKindSystem,
			Content:   imageRejectedLine(p),   // 新助手：一句人话，见下
			RunID:     evt.RunID,
			Timestamp: evt.CreatedAt,
		}})
	}
```

   `imageRejectedLine`（放 `pkg/tui/notify.go` 或邻近文件；**不要** import `frontend` 的任何东西）：
   终端是英文界面，照既有系统行风格写一句，例如
   `"an image in this turn was rejected by the provider; it was sent as a text note instead"`，
   带 `p.Reason` 时附 `" (provider said: …)"`。这句话是**用户可见文案**，改它要连带改 Step 8 的断言。
2. `pkg/tui/commands.go` 的回放链（`:401-418` 那一串 `replay*Message(evt)`）加一个助手：

```go
func replayImageRejectedMessage(evt event.RunEvent) (Message, bool)
```

   返回上面同一条 `MsgKindSystem` 消息（`Kind` 相同、`Content` 同源），并像其它助手一样被 `msg, ok = ...` 接上。
   回放与 live 必须**逐字相同**——这正是两端"同一句话"的硬要求。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` → `ok`（用例见 Step 8）。

### Step 8: Go 用例

1. `pkg/llm/image_heal_test.go`：
   - 记录型观察者 + 第一次拒绝、第二次成功的 fake ⇒ 观察者被调用**一次**、`reason` 非空，
     且两次请求的差异只有图片被换成文本（沿用现有断言方式）；
   - 不装观察者 ⇒ 行为不变；装 nil 观察者 ⇒ 不 panic。
2. `pkg/run/image_input_test.go`：
   - 一条 run 里 heal 命中 ⇒ SessionStore 里有该 `(provider, model)` 的记录，**且** `Runner.Events` 收到一条
     `image_rejected`，其 `ID == "image-rejected:"+runID`（用 `event.SinkFunc` 收集，照现有 fake sink 写法）；
   - 同一轮里观察者触发两次 ⇒ 落库一条（`ImageRefusal` 仍可读）、事件 id 相同（幂等由库/两端保证，断言"同一 id"即可）；
   - 会话有记录 + 历史带图 ⇒ `ImageTurnRefusal` 返回 `Observed == true` 且 `Reason` 为记录值；
   - 有记录但历史无图且本轮不附图 ⇒ `nil`（记过的账挡不住纯文本回合）；
   - 有记录但把会话模型换成另一个 model（`SessionModelSelection` 换掉）⇒ `nil`（换模型即恢复）；
   - 目录判定为"不支持图片" ⇒ 仍走原 code（`Observed == false`）。
3. `pkg/tui`：`publishRunEvent` 收到一条 `image_rejected` ⇒ UI 收到一条 `MsgKindSystem` 且 `Content` 含
   "rejected"；**再投一次同一事件（同 id）⇒ 不再产生第二条消息**（钉住幂等）。
   回放侧：把同一条事件交给回放链 ⇒ 得到的 `Message` 与 live 的 `Kind`/`Content` **完全相等**。
4. `pkg/gateway`：`turn_refused.Data.code` 在两种情形下分别是 `image_rejected_by_provider` 与 `image_input_unsupported`。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/event ./pkg/llm ./pkg/run ./pkg/tui ./pkg/gateway -count=1` → 全 `ok`。

### Step 9: web —— 第三种提示形态（一个块，三句话）

1. `frontend/src/composables/useChatStream.ts`：
   - `ChatMessage.imageRefusal` 扩成 `{ provider: string; model: string; kind?: 'unsupported' | 'rejected' | 'remembered' }`
     （缺 `kind` 视作 `'unsupported'`，历史/旧 code 因此照旧）；
   - `turn_refused` 分支（`:4872-4889`）：`data.code === 'image_rejected_by_provider'` ⇒ `kind: 'remembered'`，否则 `'unsupported'`；
   - `handleRunEvent`（`:4026`）加一个分支：`evt.type === 'image_rejected'` ⇒ 在**当前流式助手消息之前**插入一条
     `role:'notice'` + `imageRefusal:{provider, model, kind:'rejected'}`（插在助手消息前面，读起来的顺序是
     "这轮的图被拒了" → 然后才是回答）；
   - 确认 `'image_rejected'` **不在** `transcriptBackedEventTypes` 里（它不在 transcript 里，重载必须回放它）。
2. `frontend/src/views/ChatView.vue:82-95` 的同一个块按 `kind` 选句子：
   - `unsupported` ⇒ 既有 `chat.imageRefusedLine`（"不支持图片，消息已保留，未发送"）；
   - `rejected` ⇒ 新键 `chat.imageRejectedNow`（zh `'拒收了这轮的图片，已按文字发出，回答里看不到这张图'` /
     en `'rejected an image in this turn. It was sent as a text note, so the answer cannot see it.'`）；
   - `remembered` ⇒ 新键 `chat.imageRejectedRemembered`（zh `'已记下这个模型看不了图，这次没有发送'` /
     en `'already refused an image here, so this turn was not sent.'`）；
   - 恢复行（`imageRefusedRecover` + "换模型"按钮）三种情况都保留——它们都指向同一个出口。
3. 用例：`useChatStream.test.ts` 加一条"`run_event` `image_rejected` ⇒ `messages` 里在助手消息之前出现
   `role:'notice'` 且 `imageRefusal.kind==='rejected'`"；`ChatView.test.ts:699` 那条拆成三条（三种 kind），
   各断言自己的句子、且都不含另外两句的关键词。

**Verify**: `cd frontend && pnpm test -- src/composables/useChatStream.test.ts src/views/ChatView.test.ts` → 全 passed；
`pnpm exec vue-tsc --noEmit` → exit 0。

### Step 10: 全量回归 + 反向验证

**Verify**:
1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/event ./pkg/state ./pkg/llm ./pkg/run ./pkg/tui ./pkg/gateway -count=1` → 全 `ok`。
2. `go vet ./pkg/event ./pkg/state ./pkg/llm ./pkg/run ./pkg/tui ./pkg/gateway` → 无输出；`scripts/package-graph.sh` → 无 diff。
3. `cd frontend && pnpm test` → 全 passed；`pnpm exec vue-tsc --noEmit` → exit 0。
4. 反向验证（**四条都要做**）：
   - 注释掉 Step 5 的事件发布 ⇒ Step 8.2 的"收到一条 `image_rejected`"变红；
   - 注释掉 Step 6 的记录读取 ⇒ Step 8.2 的 `Observed == true` 变红；
   - 把 Step 5 的固定 id 改成空字符串 ⇒ Step 8.3 的"同 id 再投一次不产生第二条"变红；
   - 删掉 Step 9 的 `handleRunEvent` 分支 ⇒ Step 9.3 的第一条变红。
5. `FOREBRAIN_E2E_SPEC="e2e/message-cards.spec.ts e2e/chat-composer.spec.ts" scripts/acceptance/web_e2e.sh` → `web e2e: PASS`。
6. 迁移真机取样（**只**在临时 home）：起一次 `gateway start` 后 `PRAGMA user_version` 为 16、新表存在。
   绝不在真实 `~/.forebrain` 上验证。

## Test plan

- event：无独立用例（常量），由 run/tui 用例覆盖。
- state：迁移 15→16 + 读写 + CASCADE + agent 作用域（Step 3）。
- llm：观察者调用一次、nil 观察者不改变行为（Step 8.1）。
- run：事件 id 幂等、库记录、闸门三条（Step 8.2）。
- tui：live 一行 + 同 id 不重画 + 回放与 live 逐字相等（Step 8.3）。
- gateway：两个 code（Step 8.4）。
- web：插入位置 + 三种句子（Step 9.3）。
- 四条反向验证见 Step 10.4。

## Done criteria

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/event ./pkg/state ./pkg/llm ./pkg/run ./pkg/tui ./pkg/gateway -count=1` 全 `ok`
- [ ] `go vet`（六包）无输出；`scripts/package-graph.sh` 无 diff
- [ ] `cd frontend && pnpm test` exit 0；`pnpm exec vue-tsc --noEmit` exit 0
- [ ] `grep -n "stateSchemaVersion = 16" pkg/state/schema_migrations.go` → 命中
- [ ] `grep -n "fb_session_image_refusals" pkg/state/schema.sql pkg/state/schema_migrations.go pkg/state/session_store.go` → 三处命中
- [ ] `grep -n "RunEventImageRejected" pkg/event/run_events.go pkg/run/image_input.go pkg/tui/notify.go pkg/tui/commands.go` → 四处命中
- [ ] `grep -rn "image_rejected" pkg frontend/src | grep -v "_test\|image_rejected_by_provider"` → 常量、payload、两端判断各命中
- [ ] `grep -n "ImageRejectedCode" pkg/run/image_input.go pkg/gateway/server.go` → 两处命中
- [ ] `FOREBRAIN_E2E_SPEC="e2e/message-cards.spec.ts e2e/chat-composer.spec.ts" scripts/acceptance/web_e2e.sh` → PASS
- [ ] `git status --porcelain` 只含 Scope 列出的文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

- `tool.RunIDFromContext(ctx)` 在 `RunContent` 装配的这一层为空（run id 由更外层给）——**先报告**，不要发明 id，
  也不要因此把事件挪到别处发布；需要的是"这个 run 的真实 id"。
- `effectiveModelFor(ctx)` 在 `RunContent` 阶段与真正发请求时拿到的模型不同（子代理 / compaction 会换模型）——
  停下来报告：那意味着记账口径要改用 `agentModelFor`，不能直接按 `effectiveModelFor`。
- `r.Events` 在主 turn 上为 nil（例如某个 surface 没接 sink）——此时"事件发不出去"必须降级成"只记账 + 打日志"，
  并在报告里写明哪个 surface 拿不到。
- `pkg/state/session_events.go` 的 `ON CONFLICT` 不再按 `(session_id, event_id)` 去重（幂等假设失效）。
- `migrateStateSchema` 的 `stateObjectsAddedAfterV2`（或类似清单）要求登记新表，而语义你不确定——停下来问。
- 网关的 `turn_refused` 已被 e2e/前端按单一 code 断言，且改动会**放宽**断言。
- `pkg/llm` 的观察者需要用 `recover` 才安全（说明调用点可能 panic）——先查清 panic 来源，不要靠 recover 掩盖。
- 任一 Verify 连试两次仍失败。

## Maintenance notes

- **一句话只有一处来源**：live 与回放共用同一个 `imageRejectedLine`（终端）、同一个 locale 键（web）。
  改文案时三处（终端 live、终端回放、web 三种 kind）一起改，Step 8/9 的断言会同时提醒你。
- **固定 id 是幂等的全部机制**：`"image-rejected:" + runID`。改了 id 的构造方式就等于关掉幂等——一轮里多个
  tool-loop 反复被拒时会画出多行。不要用"每次生成 uuid"。
- **记录只是优化，不是真相**：发不发请求仍由"请求会不会带图"（`attachesImage || sessionHistoryCarriesImage`）决定，
  所以 rewind 掉带图的消息后，残留记录永远挡不住纯文本回合。评审要点之一。
- **粗档的代价**：同一模型若只因某一张图被拒（例如一张 40 MB 的截图），这个会话在这个模型上就再也不发图。
  句子会说明原因，换模型即恢复。日后若要细档，正确做法是"图的内容指纹 + 模型"，而不是把粗档改细档。
- **提示不进 transcript**：它走 run event（live + 回放），因为 `fb_messages` 里的 `role='system'` 行会被下一次请求
  当普通 system 消息重放（`pkg/state/session_store.go:2434-2445`）。**不要**为了让"重载后还能看到"而把它写进消息表。
- **事件是账单**：每条事件进 `fb_session_events`。本设计的每轮成本是"至多一次 INSERT 尝试"（同 id 时是
  `DO NOTHING` + 一次回读），不要给它加调试用的额外事件。
- 评审要点：一轮多发只画一行；回放与 live 逐字相同；`unsupported`/`rejected`/`remembered` 三句话互不串用；
  `allow_private_ip` 那类与图片无关的路径未被波及。
