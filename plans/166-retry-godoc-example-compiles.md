# Plan 166：`NewRetry` 的文档示例改成能编译的真代码

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "middleware.NewRetry" pkg/llm/retry.go` 为 `1`；
> `grep -c "func WithInitialBackoff" pkg/llm/retry_test.go` 为 `1`；
> `grep -c "defaultInitialBackoff = 500 \* time.Millisecond" pkg/llm/retry.go` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/llm/retry.go pkg/llm/retry_test.go` 必须**无输出**。

## Status

- **Priority**: P3（文档给出不存在的 API，照抄必扑空）
- **Effort**: S（改 5 行 godoc + 加一个 Example）
- **Risk**: LOW（纯文档 + 测试文件）
- **Depends on**: 无
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: docs

## Why this matters

`pkg/llm/retry.go` 的 `NewRetry` 文档给了一个"复制即可用"的示例，但它调用的是两个**不存在**的东西：

```go
//	mw, err := middleware.NewRetry(3, middleware.WithInitialBackoff(200*time.Millisecond))
//	if err != nil { ... }
//	client := Use(base, mw)
```

- 没有 `middleware` 这个包——函数就在本包里，叫 `NewRetry`；
- 没有 `WithInitialBackoff`——它只存在于 `pkg/llm/retry_test.go`（文件里有一段注释解释为什么：测试专用，
  生产文件不放没人调用的代码；测试里对它的调用有 15 处）。

执行者照着这段写代码会拿到三个编译错误，然后最可能的"修法"是把 `WithInitialBackoff` 搬进生产
（凭空多一个没有配置入口的公开选项），或者删掉测试里的 11 处调用。两者都比"把文档写对"更贵。

要做成：**文档里的示例就是能在本包编译的真代码**——把示例改成真实 API，并同时提供一个
`ExampleNewRetry`（Go 的 Example 会被 `go test` 编译；没有 `Output:` 注释则只编译不运行，正好当护栏）。

## Current state

`pkg/llm/retry.go:126-134`：

```go
// NewRetry returns an LLMMiddleware that automatically retries failed
// Execute calls up to maxAttempts times using exponential back-off with jitter.
//
// Context cancellation is honoured between retries: if ctx is cancelled while
// sleeping before a retry, the middleware returns immediately.
//
//	mw, err := middleware.NewRetry(3, middleware.WithInitialBackoff(200*time.Millisecond))
//	if err != nil { ... }
//	client := Use(base, mw)
func NewRetry(maxAttempts int, opts ...RetryOption) (LLMMiddleware, error) {
```

本包真实存在的选项（`pkg/llm/retry.go:51-90`）：`WithShouldRetry`、`WithMaxBackoff`、`WithUpwardJitter`、
`WithRetryAfter`、`WithRetryNotify`、`WithResendGuard`。首次退避固定 `defaultInitialBackoff = 500 * time.Millisecond`
（`:14`），**不可配置**。

测试专用 helper（`pkg/llm/retry_test.go:167-177`）：

```go
// The helpers below were production functions that only the tests in this
// package ever called. They live here so the production files carry no
// unused code while the tests keep exercising the live code underneath.

// WithInitialBackoff sets the initial back-off duration before the first retry.
// It doubles on each subsequent attempt (capped by WithMaxBackoff). Default: 500ms.
func WithInitialBackoff(d time.Duration) RetryOption {
	return func(c *retryConfig) { c.initialBackoff = d }
}
```

`Use(inner, mw)` 是本包真实函数（测试里大量使用，如 `pkg/llm/stream_retry_test.go` 的 `runStreamRetry`）。

## Scope

**In scope（只改这些）**：
- `pkg/llm/retry.go`（只改 `NewRetry` 的文档注释块）
- `pkg/llm/retry_test.go`（新增一个 `ExampleNewRetry`）

**Out of scope（看着相关也不要动）**：
- **不要**把 `WithInitialBackoff` 搬进生产文件（没有配置入口，只会多一个假想调用者）
- **不要**删掉测试里对 `WithInitialBackoff` 的 15 处调用
- **不要**给 `agents.defaults` 加一个 `initial_backoff` 配置项（那是产品决策，不在本计划）
- 其它选项函数的文档、退避算法本身、jitter

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 编译 + 跑 Example | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run '^ExampleNewRetry$' -count=1 -v` | `ok`（Example 无输出注释时不会打印 `--- PASS`，exit 0 即可） |
| 本包回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -count=1` | 与基线相比无新增 FAIL |
| 文档渲染 | `go doc ./pkg/llm NewRetry` | 示例里出现 `NewRetry(` 与 `WithMaxBackoff`，**没有** `middleware.` |
| vet / gofmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm`；`gofmt -l pkg/llm` | exit 0；无输出 |

## Steps

### Step 1：把 godoc 示例改成真 API

`pkg/llm/retry.go` 的 `NewRetry` 注释块，把最后的示例段替换成：

```go
//	base := someLLM()                       // the LLM being wrapped
//	mw, err := NewRetry(3, WithMaxBackoff(8*time.Second))
//	if err != nil {
//		return err
//	}
//	client := Use(base, mw)                 // every Execute now retries
//
// The first wait is fixed at 500ms and doubles per attempt (capped by
// WithMaxBackoff); it is deliberately not configurable — an option nothing
// sets is an option nobody tests. WithRetryNotify reports each wait to the
// caller, which is how the surfaces draw the retry line.
```

**Verify**：`grep -c "middleware\." pkg/llm/retry.go` 为 `0`；
`go doc ./pkg/llm NewRetry | grep -c "WithMaxBackoff"` ≥ 1。

### Step 2：加一个会编译的 Example 当护栏

`pkg/llm/retry_test.go` 末尾新增（**无** `Output:` 注释 ⇒ `go test` 只编译它，不执行它——
这正是护栏想要的：文档里的调用一旦写错，`go test ./pkg/llm` 立刻编译失败）：

```go
// ExampleNewRetry is the live copy of NewRetry's doc example: it is compiled by
// `go test`, so the API the comment names must exist.
func ExampleNewRetry() {
	inner := &stubLLM{fn: func(context.Context, []Message, []*Tool) (*Result, error) {
		return okResult("hi")
	}}
	mw, err := NewRetry(3, WithMaxBackoff(8*time.Second))
	if err != nil {
		return
	}
	_, _ = Use(inner, mw).Execute(context.Background(), []Message{UserMessage(Text("hi"))}, nil)
}
```

（`stubLLM`、`okResult` 都是本测试文件里已有的 helper；`okResult` 定义在 `stream_retry_test.go`，
同包可见。若 `stubLLM` 的字段名不同，按它实际的形状写——**不要**为了这个 Example 新增 helper。）

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run '^ExampleNewRetry$' -count=1 -v`
→ exit 0。**故意验证一次护栏**：临时把 Example 里的 `WithMaxBackoff` 改成 `WithInitialBackoff`，
`go test -run '^ExampleNewRetry$'` 必须**编译失败**；改回来再跑一次通过。

### Step 3：本包回归

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -count=1`
→ 与基线相比无新增 FAIL（尤其 `TestNewRetry_*` 与 `TestStreamRetry*`）。

## Test plan

- 新增 `ExampleNewRetry`（`pkg/llm/retry_test.go`）：只有编译价值，无断言。
- 不用新增单测：`NewRetry` 的行为已有 `TestNewRetry_*` 与 `TestStreamRetry*` 覆盖。

## Done criteria

机器可检查，全部满足：

- [ ] `grep -c "middleware\." pkg/llm/retry.go` 为 `0`
- [ ] `go doc ./pkg/llm NewRetry` 的输出里含 `NewRetry(` 与 `WithMaxBackoff`，不含 `WithInitialBackoff`
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -count=1` 与基线相比无新增 FAIL
- [ ] `grep -c "func ExampleNewRetry" pkg/llm/retry_test.go` 为 `1`
- [ ] `gofmt -l pkg/llm` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm` exit 0
- [ ] `git status --porcelain` 只显示 Scope 里的两个文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数与标注不符，或 drift check 有输出且"Current state"摘录与现场代码对不上。
2. `grep -c "func WithInitialBackoff" pkg/llm/retry.go` 不为 `0`（有人已经把它搬进生产）——那本计划的前提消失，
   文档照它改即可，先报告再动。
3. `stubLLM` 的形状无法在不新增 helper 的前提下构造出一个可编译的 Example——报告后停手，
   文档部分（Step 1）照做，Example 留待评审决定。
4. 你想顺手把 `WithInitialBackoff` 从测试文件删掉：**不要**，15 处调用依赖它。

## Maintenance notes

- 本仓的"文档示例"没有自动编译检查，除了这里是靠 `Example` 兜住的。将来别的包写 godoc 示例时，
  同一个手法可用（Example 无 `Output:` 只编译），值得在评审里提一句。
- 首次退避固定 500ms 是刻意的：`pkg/config/agents.go` 只暴露 `network_retry_max` 与
  `stream_idle_timeout_seconds`，没有退避参数。谁将来要加，必须同时加配置、默认值、clamp 与文档。
