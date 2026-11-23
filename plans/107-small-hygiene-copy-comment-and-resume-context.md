# Plan 107：三个小收口——两条会话模型文案改英文、去掉注释里的未来日期、审批续跑的查找不受请求取消影响

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。
>
> **执行顺序**：本计划的 Step 2 改 `frontend/src/views/ProvidersView.vue`、Step 3 改 `pkg/gateway/api_extra.go`，
> 计划 `100-provider-edit-replaces-in-place` 也改这两个文件——**先让 100 落地（或确认它不在执行中），再做本计划**。
> Step 1 与 100 无文件交集，可随时做。
>
> **Drift check（先跑）**：这几个文件在工作区里有未提交改动（含 100 的执行），所以不用哈希，**按符号定位**，下面每步开头给出要找的符号；
> 找不到或行为对不上即 STOP 1。`git diff --stat 55019a2 -- pkg/run/config.go` 之类的输出可以有，只要符号还在。
> （工作区里 `pkg/config/*`、`pkg/turn/*` 等的既有改动与本计划无关，不要碰、不要回滚。）

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW（一个文案、一条注释、一处把请求 ctx 换成不可取消的 ctx）
- **Depends on**: `100-provider-edit-replaces-in-place`（仅顺序：Step 2、3 与它同改文件）
- **Category**: tech-debt
- **Planned at**: commit `55019a2` + 未提交工作区，2026-10-23
- **owner 2026-10-23 的裁决**：Step 1 的两条文案改成英文，与周围一致（不保留中文）。

## Why this matters

三件各自很小、但都会在未来某个时刻绊人的事：

1. **两条用户可见文案是仓库里这两个包中仅有的中文**。`pkg/run/config.go:1936` 的回合拒绝错误与 `pkg/tui/chat_session.go:550` 的会话打开警告
   ——`pkg/run`、`pkg/tui`、`pkg/turn` 里其余用户可见字符串都是英文（TUI 与引擎错误）。这是「会话保存的模型已不在配置里」这条新语义（回合被拒绝而不是悄悄换模型）带进来的。
2. **一条代码注释写着 "owner 2026-10-26"，今天是 2026-10-23**（`frontend/src/views/ProvidersView.vue:602`）。注释里的日期是用来追溯裁决的，错的日期比没有更糟。
3. **审批续跑的查找用了请求 ctx**：`ca8ce5b4` 把 `go s.resumeGatewayRun(...)` 换成 `resumeGatewayRunIfParked(ctx, …)`，后者在**请求仍然开着**时用请求的 `ctx` 做 `FindRunByAction`。
   客户端恰好在「审批已写入」与「这次查找」之间断开时（`r.Context()` 被取消），`database/sql` 直接返回 `context canceled`，续跑被悄悄丢掉；run 一直停在已批准的 wait 上，
   只有重启时的 `recoverResolvedApprovalWaits` 或再次点击（幂等批准）才会救回来。窗口微秒级，但这个决定本来就不该依赖请求是否还连着——此时审批的结果已经落库。

## Current state

**Step 1 的两处**（原文）：

```go
// pkg/run/config.go（injectSessionModelSelection 末尾，约 :1936）
	return ctx, fmt.Errorf("本会话选择的模型 %s/%s 已不在配置中；请用 /model 重新选择",
		strings.TrimSpace(row.Provider), strings.TrimSpace(row.Model))

// pkg/tui/chat_session.go（ActivateSessionModel 末尾，约 :550）
	return fmt.Sprintf("本会话保存的模型 %s / %s 已不在配置中；重新发消息会报错，请先用 /model 选择模型", sel.Provider, sel.Model), nil
```

TUI 这条在旧版本里的英文原文是 "The model saved for this conversation (%s / %s) is no longer configured; it now runs on %s / %s."——新文案沿用它的口吻。

**现有测试**：
- `pkg/run/runner_test.go` 的 `TestRunContentRefusesStaleSessionModelRow`（约 `:2642`）断言错误含 `gpt-gone` 与 `/model`，**没有**断言语言。
- `pkg/tui/chat_session_test.go` 有两处 `require.Contains(t, warning, "已不在配置中")`（约 `:30669`、`:30728`），同处还断言 `"/model"`。

**Step 2**（`frontend/src/views/ProvidersView.vue` 约 `:598-604`）：

```ts
/**
 * The table's columns, at the widths the design declares. The models column has
 * no width: it takes the room the others leave, which is what makes the list fit
 * the page at any width. No column sorts — the row order is storage order
 * (owner 2026-10-26), not priority, and the default model is marked by the
 * badge, not by position.
 */
```

同样的日期还出现在设计稿 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html:1266`（"owner 2026-10-26 语义修订"）。设计稿是 owner 自己在改的文件，**本计划不碰它**，只在报告里指给 owner。

**Step 3**（`pkg/gateway/api_extra.go`，符号 `resumeGatewayRunIfParked`）：

```go
func (s *Server) resumeGatewayRunIfParked(ctx context.Context, actionID string, clearedContext bool) {
	if s == nil || s.RunRT == nil {
		return
	}
	_, w, err := s.RunRT.FindRunByAction(ctx, actionID)
	if err != nil || w == nil {
		return
	}
	go s.resumeGatewayRun(actionID, clearedContext)
}
```

调用方（`resolveGatewayApproval`）传入的 `ctx` 来自 `r.Context()`（`api_extra.go` 里的三个审批 handler 与 `server.go` 的一处）。
夹具：`exitPlanServer(t, cfg)`（`api_extra_test.go`，约 `:4675`）返回 `(s, runs, actions, actionID, sessionID)`，并让 run 停在一个 wait 上；
`TestGatewayApproveCancelsAnInFlightPlanReview`（约 `:4878`）是用它的样板——**注意**那条测试特意 `ClearWait` 让 run 没有续跑，我们的新测试要保留 wait。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 构建 | `go build ./...` | exit 0（`ld: warning` 可忽略） |
| run 测试 | `go test -tags fts5 ./pkg/run/ -run 'TestRunContentRefusesStaleSessionModelRow' -count=1` | `ok` |
| tui 测试 | `go test -tags fts5 ./pkg/tui/ -run 'SwitchStreamSession\|ActivateSessionModel\|StaleSession' -count=1` | `ok`（用例名以 `grep -n 'no longer configured\|已不在配置中' pkg/tui/chat_session_test.go` 附近的 `func Test…` 为准） |
| 网关测试 | `go test -tags fts5 ./pkg/gateway/ -count=1` | `ok` |
| gofmt / vet | `test -z "$(gofmt -l pkg)" && go vet ./pkg/run/ ./pkg/tui/ ./pkg/gateway/` | exit 0（若 `gofmt -l` 只列出 `pkg/tui/notify.go`，那是计划 103 的既有问题，忽略） |
| 包图快照 | `scripts/package-graph.sh && git diff --stat -- pkg/architecture/testdata/graph.json` | 只有 `pkg/run`、`pkg/tui`、`pkg/gateway` 的 `loc` 变化 |
| 前端 | `cd frontend && npx vitest run src/views/ProvidersView.test.ts` | 全绿（Step 2 只改注释） |

## Scope

**In scope**：
- `pkg/run/config.go`、`pkg/run/runner_test.go`
- `pkg/tui/chat_session.go`、`pkg/tui/chat_session_test.go`
- `frontend/src/views/ProvidersView.vue`（仅 Step 2 的注释）
- `pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`
- `pkg/architecture/testdata/graph.json`（只允许 `scripts/package-graph.sh` 重新生成）

**Out of scope**：
- `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`（owner 的设计稿，只报告不改）。
- 「会话保存的模型已不在配置里就拒绝回合」这条语义本身——是 owner 的裁决，只改措辞。
- 日志（`slog`）里的英文字段不动。
- `resumeGatewayRun` 的内部逻辑、`recoverResolvedApprovalWaits`。

## Git workflow

- **不提交**。改动留在 dev 工作区。

## Steps

### Step 1：两条文案改英文

先改测试再改实现，确认先红后绿：

1. `pkg/run/runner_test.go` 的 `TestRunContentRefusesStaleSessionModelRow`：在已有的 `strings.Contains(err.Error(), "gpt-gone") || … "/model"` 判断上加上
   `|| !strings.Contains(err.Error(), "no longer configured")`（让三个子串都必须出现）。
2. `pkg/tui/chat_session_test.go` 两处 `require.Contains(t, warning, "已不在配置中")` 改成 `require.Contains(t, warning, "no longer configured")`。

**Verify（应当红）**：`go test -tags fts5 ./pkg/run/ -run TestRunContentRefusesStaleSessionModelRow -count=1 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'` → `--- FAIL`；
tui 那两条所在的测试同样 FAIL（先用 `grep -n 'no longer configured' pkg/tui/chat_session_test.go` 找到它们属于哪两个 `func Test…`，再 `-run` 这两个）。

3. 改实现：

```go
// pkg/run/config.go
	return ctx, fmt.Errorf("the model chosen for this conversation (%s/%s) is no longer configured; choose another with /model",
		strings.TrimSpace(row.Provider), strings.TrimSpace(row.Model))

// pkg/tui/chat_session.go
	return fmt.Sprintf("The model saved for this conversation (%s / %s) is no longer configured; your next message will be refused until you choose one with /model.", sel.Provider, sel.Model), nil
```

**Verify**：上面三条测试变绿；`grep -n '[一-龥]' pkg/run/config.go pkg/tui/chat_session.go | grep -v '^\S*:\s*[0-9]*:\s*//'` 不再命中**字符串字面量**里的中文（注释里的中文可以有）。

### Step 2：去掉注释里的错误日期

`frontend/src/views/ProvidersView.vue` 里把 `(owner 2026-10-26)` 改成 `(owner decision)`——只改这几个字，整条注释其余原样。

**Verify**：`grep -n 'owner 2026-10-26' frontend/src/views/ProvidersView.vue` 无命中；`cd frontend && npx vitest run src/views/ProvidersView.test.ts` → 全绿。

### Step 3：续跑的查找不依赖请求 ctx

1. 先写测试（`pkg/gateway/api_extra_test.go`，放在 `TestGatewayApproveCancelsAnInFlightPlanReview` 附近）。需要一个测试缝：在 `resumeGatewayRunIfParked` 旁加包级变量

```go
// dispatchGatewayResume starts the continuation of an answered action. It is a
// variable only so a test can observe the dispatch without running the run.
var dispatchGatewayResume = func(s *Server, actionID string, clearedContext bool) {
	go s.resumeGatewayRun(actionID, clearedContext)
}
```

   （此刻先只加变量、让 `resumeGatewayRunIfParked` 末尾那行 `go s.resumeGatewayRun(...)` 改成 `dispatchGatewayResume(s, actionID, clearedContext)`，行为不变。）
   新增两个测试：
   - `TestResumeIfParkedStillDispatchesWhenTheRequestIsGone`：`s, _, _, actionID, _ := exitPlanServer(t, cfg)`（cfg 参照 `TestGatewayApproveCancelsAnInFlightPlanReview` 里那份）；
     `dispatchGatewayResume` 换成往 channel 记录 `actionID` 的函数（`t.Cleanup` 还原）；
     建一个**已取消**的 ctx（`ctx, cancel := context.WithCancel(context.Background()); cancel()`）调 `s.resumeGatewayRunIfParked(ctx, actionID, false)`；
     断言 channel 在 2 秒内收到 `actionID`。**预期现在红**（查找带着已取消的 ctx 失败，没有派发）。
   - `TestResumeIfParkedDispatchesNothingWithoutAParkedRun`：同夹具，但先 `runs.ClearWait(ctx, runID)`（`runID` 取 `runs.FindRunByAction(ctx, actionID)` 的第一个返回值，写法同 `:4924-4927`）；
     调用后断言 channel **没有**收到（用 `select` + 200ms 超时）。（已有行为，护栏。）
2. **Verify（应当红）**：`go test -tags fts5 ./pkg/gateway/ -run 'TestResumeIfParkedStillDispatchesWhenTheRequestIsGone' -count=1 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'` → `--- FAIL`。
3. 改实现：`FindRunByAction(ctx, actionID)` 改成 `FindRunByAction(context.WithoutCancel(ctx), actionID)`，并在其上加注释：

```go
	// The answer is already stored by the time this runs; whether to continue the
	// run must not depend on the client still being connected. WithoutCancel keeps
	// the request's values and drops its cancellation — the lookup is one short read.
```

**Verify**：`go test -tags fts5 ./pkg/gateway/ -count=1` → `ok`（新增两个测试通过；`TestGatewayApproveCancelsAnInFlightPlanReview` 与它的幂等兄弟 `TestGatewayIdempotentApproveStillCancelsAnInFlightPlanReview` 用 `-count=20` 各跑一遍不出现新的不稳定：
`go test -tags fts5 ./pkg/gateway/ -run 'TestGatewayApproveCancelsAnInFlightPlanReview|TestGatewayIdempotentApproveStillCancelsAnInFlightPlanReview' -count=20` → `ok`）。

### Step 4：收尾门禁

1. `scripts/package-graph.sh`，`git diff --stat -- pkg/architecture/testdata/graph.json`：只应有 `pkg/run`、`pkg/tui`、`pkg/gateway` 的 `loc` 变化。
2. `test -z "$(gofmt -l pkg/run pkg/tui pkg/gateway)"`（`pkg/tui/notify.go` 的既有问题除外）；`go build ./... && go vet ./pkg/run/ ./pkg/tui/ ./pkg/gateway/` → exit 0。
3. `go test -tags fts5 ./pkg/run/ ./pkg/tui/ ./pkg/gateway/ -count=1` → 全 `ok`（`pkg/tui` 约 70 秒）。

## Test plan

见各步。Step 1 改了 3 处既有断言的措辞（先红后绿）；Step 3 新增 2 个测试（1 个先红、1 个护栏）。

## Done criteria

- [ ] `go test -tags fts5 ./pkg/run/ ./pkg/tui/ ./pkg/gateway/ -count=1` 全 `ok`
- [ ] `grep -rn '已不在配置中' pkg` 无命中
- [ ] `grep -n 'owner 2026-10-26' frontend/src/views/ProvidersView.vue` 无命中
- [ ] `grep -n 'FindRunByAction(context.WithoutCancel' pkg/gateway/api_extra.go` 恰有 1 处
- [ ] 设计稿 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` **未被本计划修改**（`git diff` 里它的改动与开始前一致）；报告里提醒 owner 其中 `:1266` 有同样的 "2026-10-26"
- [ ] `git diff --name-only -- pkg/architecture/testdata/graph.json` 只有这一个文件，且只含 loc 变化
- [ ] 没有任何 `git commit` / `git push` / 新分支

## STOP conditions

1. 找不到上面写的符号（`injectSessionModelSelection` 末尾的错误、`ActivateSessionModel` 末尾的返回、`resumeGatewayRunIfParked`），或它们的行为已不同。
2. `pkg/gateway` 当前**编译不过**（计划 100 可能还在执行中）——等它落地后再做 Step 3，不要去修别人的半成品。
3. 新增的 `TestResumeIfParkedStillDispatchesWhenTheRequestIsGone` 在改实现之前就是绿的——说明夹具没保住 wait 行，或 `FindRunByAction` 不看 ctx；停下报告，不要硬凑。
4. `-count=20` 的审批测试出现新的不稳定。
5. 一个步骤的验证在合理修复后仍失败两次。

## Maintenance notes

- 以后新增会写进用户界面的字符串，英文是 TUI 与引擎的默认；网关 HTTP 错误里的中文是给中文版网页看的（该族接口既有的约定），别把二者混用。
- `dispatchGatewayResume` 这个测试缝是仓库里常见的写法（包级变量供测试替换，如 `chatGPTLoginRun`）；不要把它扩成接口。
- 评审重点：`context.WithoutCancel` 只用在那一次短读上，**不要**顺手把 `resumeGatewayRun` 本身改成继承请求 ctx 或不可取消的 ctx。
