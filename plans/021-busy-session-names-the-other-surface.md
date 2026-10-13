# Plan 021：会话正被另一端（另一个进程）运行或停在审批上时，拒绝并提示"正在另一端运行"；本端自己的忙碌提示不变

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：`git diff --stat 005c1a8 -- pkg/state/runrt.go frontend/src/lib/providerError.ts frontend/src/locales/index.ts frontend/e2e/session-busy.spec.ts`
> 预期无输出；有输出则对照 "Current state" 逐段核对，不一致即 STOP。

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW（只细分已有的忙碌拒绝；拒绝本身的判定与时机不变）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `005c1a8` + 主树工作区 008/011/012/013 未提交改动，2026-10-10

## Why this matters

owner 规则：本机 web 与 TUI 共享同一份会话数据、互相可续聊；**一端正在跑某会话的 turn 时，另一端发消息要拒绝并提示"正在另一端运行"**
（不做跨进程排队）。

拒绝本身已经有了：`RunStore.CreateRun` 在库里原子地检查"这个会话有没有活着的主 run"，有就返回 `ErrSessionRunning` 或
`ErrSessionAwaitingApproval`，web 把消息原样退回输入框并显示本地化句子，TUI 显示一个 "session busy" 框。但句子只说"这个对话正在运行一个回合"，
用户分不清是自己这一端还没结束，还是另一端（另一个 TUI 窗口 / web）在跑。库里已经有区分的依据：每个 run 记着驱动它的进程
`fb_runs.owner`，`RunStore.Owner` 是本进程。

## Current state

`pkg/state/runrt.go:30-60`：

```go
var ErrSessionBusy = errors.New("session busy")

type sessionBusyError struct{ message string }
func (e sessionBusyError) Error() string { return e.message }
func (e sessionBusyError) Is(target error) bool { return target == ErrSessionBusy }

var (
	// ErrSessionRunning: another live run is driving the conversation.
	ErrSessionRunning = sessionBusyError{"This conversation is already running a turn; send again when it finishes."}
	// ErrSessionAwaitingApproval: a run in it is parked on an approval.
	ErrSessionAwaitingApproval = sessionBusyError{"This conversation is waiting for an approval; answer it before sending another message."}
)

func SessionBusyCode(err error) string {
	switch {
	case errors.Is(err, ErrSessionAwaitingApproval):
		return "session_awaiting_approval"
	case errors.Is(err, ErrSessionRunning):
		return "session_running"
	default:
		return ""
	}
}
```

`pkg/state/runrt.go:206-264`：`livePrimaryRunCondition`（parked，或 running 且 owner 租约新鲜）；`CreateRun` 用一条
`INSERT ... SELECT ... WHERE NOT EXISTS (live primary run)` 原子插入，插入 0 行时再查一次是否 parked，返回上面两个错误之一：

```go
	if n, _ := res.RowsAffected(); n == 0 {
		// Nothing was inserted: name the blocker for the person who asked.
		var parked int
		if err := s.DB.QueryRowContext(ctx, `
SELECT EXISTS(SELECT 1 FROM fb_runs WHERE session_id=? AND parent_run_id IS NULL AND status=?)`,
			r.SessionID, string(RunStatusWaitingAction)).Scan(&parked); err != nil {
			return nil, err
		}
		if parked != 0 {
			return nil, ErrSessionAwaitingApproval
		}
		return nil, ErrSessionRunning
	}
```

`RunStore.Owner`（`runrt.go:193-199`）：本进程的 owner；为空时表示"没有 live 进程担保"（测试/旧代码）。

消费方：
- gateway：`pkg/gateway/server.go:1692` 把 `state.SessionBusyCode(err)` 作为 `code` 下发；`run_control.go:734`、`pkg/turn/scheduler.go:535`、
  `pkg/run/supervisor.go:201` 用 `errors.Is(err, state.ErrSessionBusy)` 判断（**不受影响**——新成员同样匹配家族哨兵）。
- web：`frontend/src/lib/providerError.ts:115-120` 按 code 取文案 `runError.sessionAwaitingApproval` / `runError.sessionRunning`
  （`frontend/src/locales/index.ts:694-695` 中文、`:1676-1677` 英文）。
- TUI：`pkg/tui/run.go:3608-3614` 渲染 `Frame{Kind: FrameSystem, Title: "session busy", Content: err.Error()}`；`chat_session.go:1967` 撤回用户行。
- e2e：`frontend/e2e/session-busy.spec.ts` 造一个 owner 为 `e2e-busy-owner` 的 running run（与 gateway 进程的 owner 不同），断言中文句子
  `这个对话正在运行一个回合，等它结束后再发送。`（`:27` 附近）和英文句子（`:36` 附近）。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/gateway/... ./pkg/turn/... ./pkg/run/... ./pkg/tui/... -count=1` | 全部 `ok`（`TestCharacterizationNetworkApproval` 偶发网络超时，单跑通过即可） |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/...`；`gofmt -l pkg/state` | 无输出 |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| e2e（可选） | `FOREBRAIN_E2E_SPEC=e2e/session-busy.spec.ts scripts/acceptance/web_e2e.sh` | 通过；跑不起来（缺浏览器等）时在报告里写明 |

## Scope

**In scope**：`pkg/state/runrt.go`、`pkg/state/runrt_test.go`（或该文件的既有测试文件，`grep -ln 'ErrSessionRunning' pkg/state/*_test.go`）、
`frontend/src/lib/providerError.ts`、`frontend/src/locales/index.ts`、`frontend/src/lib/providerError.test.ts`（若存在；否则加到最近的前端测试）、
`frontend/e2e/session-busy.spec.ts`。

**Out of scope**：拒绝的判定条件与时机；跨进程排队（owner 明确不做）；TUI 的 "session busy" 框样式（只是句子变了）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：新的错误成员与 code

`pkg/state/runrt.go` 的 var 块加：

```go
	// ErrSessionRunningElsewhere: the live run driving the conversation belongs
	// to another process — the other surface, or another window of this one.
	ErrSessionRunningElsewhere = sessionBusyError{"This conversation is running on another surface (the terminal or the web); send again when it finishes there."}
	// ErrSessionAwaitingApprovalElsewhere: a run parked on an approval that
	// another process is holding.
	ErrSessionAwaitingApprovalElsewhere = sessionBusyError{"This conversation is waiting for an approval on another surface (the terminal or the web); answer it there before sending another message."}
```

`SessionBusyCode` 在已有两个 case **之前**加 `session_awaiting_approval_elsewhere` 与 `session_running_elsewhere`（`errors.Is` 对值相等判断，
新成员不会误匹配旧成员；顺序只为可读）。

### Step 2：CreateRun 说出是谁占着

把"插入 0 行"分支改为一次查询拿到占用者的状态与 owner：

```go
	if n, _ := res.RowsAffected(); n == 0 {
		// Nothing was inserted: name the blocker for the person who asked —
		// parked or running, and whether this process is the one holding it.
		var status, owner string
		err := s.DB.QueryRowContext(ctx, `
SELECT r.status, r.owner FROM fb_runs r
WHERE r.session_id = ? AND r.parent_run_id IS NULL AND `+fmt.Sprintf(livePrimaryRunCondition, "r")+`
ORDER BY r.updated_at DESC LIMIT 1`, append([]any{r.SessionID}, liveRunArgs(now)...)...).Scan(&status, &owner)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		elsewhere := strings.TrimSpace(s.Owner) != "" && strings.TrimSpace(owner) != strings.TrimSpace(s.Owner)
		switch {
		case status == string(RunStatusWaitingAction) && elsewhere:
			return nil, ErrSessionAwaitingApprovalElsewhere
		case status == string(RunStatusWaitingAction):
			return nil, ErrSessionAwaitingApproval
		case elsewhere:
			return nil, ErrSessionRunningElsewhere
		default:
			return nil, ErrSessionRunning
		}
	}
```

（`now` 用 `CreateRun` 里已有的时间变量；`ErrNoRows`——占用者在两条语句之间刚结束——按原逻辑落到 `ErrSessionRunning`，让用户重发。
若 `livePrimaryRunCondition` 的占位方式与上面写法不符，按 `CreateRun` 主 INSERT 里的用法改写，语义不变。）

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/state/...` → 无输出。

### Step 3：Go 测试

在 `pkg/state` 既有的 `CreateRun` 忙碌测试旁（`grep -n 'ErrSessionRunning' pkg/state/*_test.go` 找样板）加 `TestCreateRunNamesTheSurfaceHoldingTheSession`（表驱动）：
同一个 DB，两个 `RunStore{DB: db, Owner: "proc-a"}` / `{Owner: "proc-b"}`，`fb_run_owners` 给两者都登记新鲜心跳（照样板的写法）。
- A 建 running run → A 再建：`ErrSessionRunning`、code `session_running`；B 建：`ErrSessionRunningElsewhere`、code `session_running_elsewhere`；
  两者 `errors.Is(err, ErrSessionBusy)` 都为 true。
- A 的 run 改为 `waiting_action` → A 建：`ErrSessionAwaitingApproval`；B 建：`ErrSessionAwaitingApprovalElsewhere`、code `session_awaiting_approval_elsewhere`。
- `Owner` 为空的 store 建：落到不带 Elsewhere 的旧错误（兼容）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ -run TestCreateRunNamesTheSurfaceHoldingTheSession -count=1 -v` → PASS；`./pkg/state/...` 全部 `ok`。

### Step 4：web 文案

- `frontend/src/locales/index.ts` 中文加：
  `'runError.sessionRunningElsewhere': '这个对话正在另一端运行（终端或网页），等它结束后再发送。'`、
  `'runError.sessionAwaitingApprovalElsewhere': '这个对话正在另一端等待审批（终端或网页），请在那一端处理后再发送。'`；
  英文加与 Step 1 相同的两句。
- `frontend/src/lib/providerError.ts` 在 `session_awaiting_approval` 判断之前加两个 code 的映射。
- 前端测试：在 `providerError` 的既有测试里（`grep -rln "session_running" frontend/src --include=*.test.ts`）加两条：两种新 code 分别得到中英文句子。

**Verify**：`cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` → 全绿 / 无输出。

### Step 5：e2e 期望

`frontend/e2e/session-busy.spec.ts` 造的占用 run 属于 `e2e-busy-owner`（不是 gateway 进程），改完后 gateway 会返回 "elsewhere"：
把断言的中文句子改为 `这个对话正在另一端运行（终端或网页），等它结束后再发送。`、英文改为 Step 1 的英文句子；用例上方注释补一句"占用者是另一个进程，所以提示说另一端"。
能跑就跑 Commands 表里的 e2e 命令；跑不了在报告里写明原因。

## Test plan

- Go：`TestCreateRunNamesTheSurfaceHoldingTheSession`（本端/另一端 × 运行中/停审批，外加空 owner 兼容）。
- 前端：两个新 code 的文案映射。
- e2e：`session-busy.spec.ts` 期望句更新（可运行时验证）。

## Done criteria

- [ ] Commands 表的 Go 与前端命令全部通过
- [ ] `grep -n 'session_running_elsewhere\|session_awaiting_approval_elsewhere' pkg/state/runrt.go frontend/src/lib/providerError.ts` 各有命中
- [ ] e2e 期望已更新（运行结果或无法运行的原因写进报告）
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. 有调用方按"错误值相等"（`err == ErrSessionRunning`）而不是 `errors.Is` 判断（`grep -rn '== state.ErrSessionRunning\|== ErrSessionRunning' pkg`）——改了会让它漏判，停下报告。
3. 既有测试断言了忙碌拒绝的英文原句且占用者属于另一进程——停下报告是哪条，不要改断言迁就。

## Maintenance notes

- "另一端"按进程判断：两个 TUI 窗口互相也算另一端，同一个 gateway 下的两个网页标签算同一端（共享一个进程 owner）。
- 新增一种忙碌原因时，同时给出本端/另一端两个成员和 code，并补 web 文案。
