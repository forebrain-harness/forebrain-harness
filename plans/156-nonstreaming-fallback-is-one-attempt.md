# Plan 156：非流式回退只许一次——不许把 5 分钟盲等乘上重试预算

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**（行号以本计划的基线为准，以**符号**为准）：
> `grep -c "func withoutStreaming" pkg/llm/stream_failure.go` 为 `1`；
> `grep -c "nonStreaming := false" pkg/llm/stream_retry.go` 为 `1`；
> `grep -c "attemptCtx = WithNonStreamingRequest(ctx)" pkg/llm/stream_retry.go` 为 `1`；
> `grep -c "func runStreamRetry" pkg/llm/stream_retry_test.go` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/llm` 必须**无输出**。有输出就按下文"现状"里的
> **函数名**逐段核对；被引用的代码含义变了即 STOP 1。
>
> **基线（Step 1 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/run -count=1`（超时给足 900 s）。
> 基线里已经红的是存量问题，**不是你要修的**；Done criteria 只要求"不新增红"。

## Status

- **Priority**: P1（一个卡住的网关端点能让单个 turn 串行停 50 分钟）
- **Effort**: S（两处判断，各 3~8 行 + 两个测试）
- **Risk**: MED（改的是"重试要不要再发一次"的决策表；`pkg/llm` 是四个 provider 共用的层）
- **Depends on**: 无
- **Blocks**: 无（`plans/165` 的账本会读这里产生的 attempt 事实，但两者不冲突）
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: bug（挂起不自恢复 / 资源加倍）

## Why this matters

owner 的常设不变式：**任何挂起都必须自恢复**。流式请求有看门狗兜底（`stream_watchdog.go`，静默超过窗口就
掐掉当前尝试并当作掉线重发），非流式请求**没有**看门狗，只有一个 5 分钟的硬超时
（`nonstreaming.go:11` `var NonStreamingTimeout = 5 * time.Minute`）。而这个仓库的重试层在第一次
"降级为非流式"之后**再也不会回到流式**：

- `nonStreaming` 只是一个单调置真的局部变量（`stream_retry.go:56` 起，`:199` 置真）；
- 每次尝试都读它决定这次发流式还是非流式（`stream_retry.go:148-152`）；
- 决策表在"已经降级过"之后仍然会返回 `DecideRetryWithoutStreaming`
  （`stream_failure.go:145-155`：只有 `triedWithoutStreaming && retries >= maxRetries` 才 `fail`，
  其余照旧返回降级）；
- 默认重试上限 10（`pkg/config/agents.go:133` `DefaultNetworkRetryMax = 10`）。

于是最坏路径是 **1 次流式 + 10 次非流式**，每次非流式都能盲等 5 分钟，且期间没有任何进度可观察——
一个 turn 可以停 ~50 分钟，只有重试行在转。这与 `pkg/llm/retry.go:109-113` 自己写的原则直接冲突
（"服务器要求的等待也不能无界：超过上限就把错误交回调用方，而不是把 turn 停住"）。

要做成：**非流式回退是一次尝试，不是一种模式**。降级那次跑完（无论成败）就回到流式重试；
`DecideRetryWithoutStreaming` 每晚只会发生一次。最坏盲等从 N×5 分钟降为 5 分钟，且之后每次都重新受看门狗约束。

## Current state

### 1) 单调置真的降级开关（`pkg/llm/stream_retry.go`）

```go
	nonStreaming := false            // :56
	...
	for {
		...
		attemptCtx := ctx                       // :148
		if nonStreaming {                       // :149
			attemptCtx = WithNonStreamingRequest(ctx)   // :150
		}
		result, err := r.inner.Execute(attemptCtx, messages, tools)
		...
		case DecideRetryWithoutStreaming:       // :194-201
			withdraw(snap)
			...
			nonStreaming = true                 // :199  ← 从此永久非流式
			ResetStreamProgress(ctx)
			continue
```

### 2) 决策表里"降级"可以被反复选中（`pkg/llm/stream_failure.go:145-155`）

```go
func withoutStreaming(c streamRetryCounts, p streamRetryPolicy) streamDecision {
	if !p.allowNonStreaming {
		return failWith(c)
	}
	if c.triedWithoutStreaming && c.retries >= p.maxRetries {
		return failWith(c)
	}
	c.retries = min(c.retries+1, p.maxRetries)
	c.triedWithoutStreaming = true
	return streamDecision{DecideRetryWithoutStreaming, c}
}
```

`withoutStreaming` 是决策表里**几十个分支的兜底**（overloaded / stalled / truncated / suspended /
connection lost / server error / timed out / malformed / bad request / unknown，以及
`requestLevelDecision` 的 404）。上面的写法意味着：只要共享预算还没花光，每次失败都还能再要一次"非流式"。

### 3) 非流式没有看门狗（`pkg/llm/stream_watchdog.go:78-81`）

```go
func (t watchdogTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	window := streamWatchdogFrom(req.Context())
	if window <= 0 || !isStreamingRequest(req.Context()) {
		return t.next.RoundTrip(req)
	}
```

非流式上下文不带 streaming mark，所以整段旁路看门狗，只剩 `NonStreamingTimeout` 的 5 分钟硬顶。

## 要达成的行为矩阵

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 流式失败一次 → 降级非流式 → 非流式成功 | 成功 | 不变 |
| 流式失败 → 降级 → 非流式失败 | 继续非流式，最多再 9 次盲等 | 回到**流式**重试（看门狗重新生效） |
| 降级过之后决策表又想要降级 | 再次降级 | `failWith`：把错误交回调用方 |
| 单次 turn 的最坏盲等 | 10 × 5 min | 1 × 5 min |
| 404（流式端点不存在）→ 降级 | 一次降级（本来就只有一次） | 不变 |

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run '^(TestNonStreamingFallback\|TestRetryGivesUpAfterOneNonStreamingAttempt)' -count=1 -v` | 看到 `--- PASS` |
| 本包全测 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -count=1` | 与基线相比无新增 FAIL |
| 下游不受影响 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | 与基线相比无新增 FAIL |
| 编译 / vet | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm` | exit 0 |
| gofmt | `gofmt -l pkg/llm` | 无输出 |

## Scope

**In scope（只改这些）**：
- `pkg/llm/stream_failure.go`（`withoutStreaming`：降级只许一次）
- `pkg/llm/stream_retry.go`（降级按次消费，不再粘着）
- `pkg/llm/stream_retry_test.go`（新增 2 个测试）

**Out of scope（看着相关也不要动）**：
- `NonStreamingTimeout` 的值（5 分钟是另一个决定；本计划只保证它不被乘上重试次数）
- `nonstreaming.go` 的 `NonStreamingError`/`WithNonStreamingDeadline` 语义
- 看门狗跳过非流式的那一行（`stream_watchdog.go:78-81`）——非流式**就是**没有空闲信号，加看门狗会误杀
  正常的慢响应；本计划改用"只许一次"来约束它
- `pkg/config` 的 `network_retry_max` / `stream_idle_timeout_seconds`（数值不动、语义不动）
- 退避与 jitter（`retry.go` 的 `waitFor`）
- 任何 provider 适配器（`pkg/llm/{openai,google,anthropic,...}`）
- `pkg/tui`、`pkg/gateway`、`frontend/`（重试行怎么画不变）

## Steps

### Step 1：先写两个会红的测试

两个测试都放在 `pkg/llm/stream_retry_test.go`，用文件里现成的 `runStreamRetry` 驱动
（它返回 `inner.nonStreaming []bool`，每一次尝试都记录该次上下文是否带非流式标记——**这正是断言的对象**）。

```go
// 降级那次失败之后必须回到流式：非流式没有看门狗，粘在它上面就是把 5 分钟盲等乘上重试预算。
func TestNonStreamingFallbackReturnsToStreamingAfterOneAttempt(t *testing.T) {
	_, err, inner, _ := runStreamRetry(t, 10,
		func(ctx context.Context) (*Result, error) {
			return nil, &APIError{StatusCode: http.StatusNotFound}      // 选择降级
		},
		func(ctx context.Context) (*Result, error) {
			return nil, fmt.Errorf("%w: %v", ErrNonStreamingTimeout, context.DeadlineExceeded)
		},
		func(ctx context.Context) (*Result, error) { return okResult("streamed again") },
	)
	if err != nil {
		t.Fatalf("Execute: %v (a streaming attempt after the fallback should succeed)", err)
	}
	want := []bool{false, true, false}
	if !reflect.DeepEqual(inner.nonStreaming, want) {
		t.Fatalf("non-streaming per attempt = %v, want %v", inner.nonStreaming, want)
	}
}

// 全部失败时也只能有一次非流式盲等：决策表不再反复要降级。
func TestRetryGivesUpAfterOneNonStreamingAttempt(t *testing.T) {
	fail := func(ctx context.Context) (*Result, error) {
		return nil, fmt.Errorf("%w: %v", ErrNonStreamingTimeout, context.DeadlineExceeded)
	}
	_, err, inner, _ := runStreamRetry(t, 10,
		func(ctx context.Context) (*Result, error) {
			return nil, &APIError{StatusCode: http.StatusNotFound}
		}, fail, fail, fail, fail, fail, fail, fail, fail, fail, fail, fail)
	if err == nil {
		t.Fatal("every attempt failed: an error is expected")
	}
	n := 0
	for _, nonStreaming := range inner.nonStreaming {
		if nonStreaming {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("non-streaming attempts = %d, want exactly 1 (each one is a 5-minute blind wait)", n)
	}
}
```

需要的 import：`reflect` 与 `fmt`（`stream_retry_test.go` 目前两者都没有；`okResult` 是 `func(string) (*Result, error)`，
**不能**裸传进 `runStreamRetry`——上面第三次尝试包了一层闭包，与文件里既有用例的写法一致）。
`APIError`、`ErrNonStreamingTimeout`、`okResult` 都在同包。

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run '^(TestNonStreamingFallbackReturnsToStreamingAfterOneAttempt|TestRetryGivesUpAfterOneNonStreamingAttempt)$' -count=1 -v`
→ 两个都 `--- FAIL`。若有一个直接 PASS，STOP 3（说明基线已经变了，本计划的前提不成立）。

### Step 2：决策表——降级只许一次

`pkg/llm/stream_failure.go` 的 `withoutStreaming`，把第二个 `if` 改成：

```go
	if c.triedWithoutStreaming {
		// The fallback is one attempt, not a mode. A non-streaming request has
		// no idle watchdog (stream_watchdog skips it by design), so repeating
		// the fallback multiplies a wait nothing bounds: every attempt gets its
		// own NonStreamingTimeout. After the blind attempt failed, the error
		// goes back to the caller instead.
		return failWith(c)
	}
```

（即删掉原来的 `&& c.retries >= p.maxRetries`，并把注释换成上面这段。）

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run '^TestDecideStreamFailure' -count=1 -v`
→ 全 PASS。决策表里所有"降级"用例都传 `triedWithoutStreaming:false`，因此不受影响；
传 `true` 的用例有两条（"connection lost after everything was tried fails" 与
"exhausted small budget fails after the non-streaming attempt"，`stream_failure_test.go:144-145`），
**两条本来就期望 `fail`**，新代码下结果不变。（`TestRequestLevelDecision` 里那条 "404 after the
non-streaming attempt passes" 到不了 `withoutStreaming`——`requestLevelDecision` 自己有
`status == 404 && !c.triedWithoutStreaming` 守卫。）

### Step 3：重试循环——降级按次消费

`pkg/llm/stream_retry.go` 的循环头，把 `:148-152` 改成：

```go
		attemptCtx := ctx
		if nonStreaming {
			attemptCtx = WithNonStreamingRequest(ctx)
			// One attempt, not a mode: consumed here so the attempt after this
			// one is streamed again — and back under the watchdog.
			nonStreaming = false
		}
```

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run '^(TestNonStreamingFallbackReturnsToStreamingAfterOneAttempt|TestRetryGivesUpAfterOneNonStreamingAttempt)$' -count=1 -v`
→ 两个都 `--- PASS`。

### Step 4：全包回归

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/run -count=1`
→ 与基线相比无新增 FAIL（`pkg/llm` 的其余重试/取消/部分结果用例必须全绿）。

## Test plan

- 新增 `TestNonStreamingFallbackReturnsToStreamingAfterOneAttempt`（`pkg/llm/stream_retry_test.go`）：
  断言每一次尝试是否非流式 = `[false, true, false]`，并断言最终成功。
- 新增 `TestRetryGivesUpAfterOneNonStreamingAttempt`（同文件）：全部失败时非流式尝试恰好 1 次、最终返回错误。
- 结构照抄同文件已有用例（`TestStreamRetryKeepsPartialAfterThinkingAndText` 用 `runStreamRetry` + 脚本化尝试）。
- 不新增 mock 框架：`scriptedStreamLLM` 已经记录 `nonStreaming`。

## Done criteria

机器可检查，全部满足：

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm` exit 0
- [ ] `gofmt -l pkg/llm` 无输出
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run 'TestNonStreamingFallbackReturnsToStreamingAfterOneAttempt|TestRetryGivesUpAfterOneNonStreamingAttempt' -count=1` 全 PASS
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/run -count=1` 与基线相比无新增 FAIL
- [ ] `grep -c "if c.triedWithoutStreaming {" pkg/llm/stream_failure.go` 为 `1`
- [ ] `grep -c "nonStreaming = false" pkg/llm/stream_retry.go` 为 `1`
- [ ] `git status --porcelain` 只显示 Scope 里列出的三个文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1，或 drift check 有输出且"现状"里被引用函数的语义变了
   （ excerpts 与现场代码对不上）。
2. 基线命令本身跑不起来（编译失败/环境错误），拿不到失败清单。
3. 两个新测试里有一个在 Step 1 **直接 PASS**（基线变了——说明别处已经修过，重复实施会改变既有语义）。
4. Step 2 之后决策表测试出现**新的 FAIL**（说明降级语义被别处依赖；把失败用例原样贴出来）。
5. 你发现"非流式"还会被别的入口置真（例如某个 adapter 自己在上下文里塞 `WithNonStreamingRequest`）——
   那不是本计划的范围，报告后停手。
6. 你为了让测试通过而想改 `NonStreamingTimeout`、看门狗、或 `network_retry_max` 的默认值——先停下报告。

## Maintenance notes

- 谁将来要加"更聪明的降级"（比如按 provider 决定降级次数），必须同时回答"这段时间谁在兜底"：
  非流式没有空闲信号，任何重复降级都等于把 `NonStreamingTimeout` 乘上次数。
- 评审重点：`stream_failure.go` 的 `withoutStreaming` 是**几十个分支的公共兜底**，改它等于改整张表；
  逐条核对 `stream_failure_test.go` 的 table 用例是否仍表达原意（尤其 "suspended on partial output
  spends the shared budget" 这条只花预算、没降级的用例）。
- 未来若需要"整回合总预算"（wall-clock 上限），那是另一个计划：它会与 `pkg/run` 的 turn 级取消语义交互，
  不能塞进 `streamRetryLLM` 里草草实现。
