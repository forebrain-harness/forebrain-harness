# Plan 108：登录流程不被无关请求打断、取消后不再落盘凭据（ChatGPT 回调处理器 + OpenCode 设备流）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。
>
> **Drift check（先跑）**：
> `git diff --stat 55019a2 -- pkg/llm/openai/auth.go pkg/llm/openai/auth_test.go pkg/llm/opencode/auth.go pkg/llm/opencode/auth_test.go`
> 预期**无输出**。有输出就对照下面 "Current state" 的摘录：不符即 STOP 1。
> （工作区里 `pkg/gateway/*`、`pkg/config/*`、`pkg/run/*`、`pkg/turn/*`、`pkg/tui/*`、`frontend/*` 的既有改动与本计划无关，不要碰、不要回滚。）

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW（只改「什么请求算数」「什么时候落盘」；成功路径不变）
- **Depends on**: none。**计划 `104-chatgpt-login-by-pasted-callback` 依赖本计划**（它要改同一个回调处理器，请先落本计划）
- **Category**: bug（健壮性）
- **Planned at**: commit `55019a2`，2026-10-23

## Why this matters

三个互相独立的小缺陷，都在登录流程里，共同点是「流程的结局被不该决定它的东西决定了」：

1. **本机任何一次错误的回调请求就能终止 ChatGPT 登录**。回调处理器在 `state` 对不上时把一个错误发进结果通道，整个登录以「OAuth state mismatch」结束
   （`pkg/llm/openai/auth.go:294-298`）。登录期间 `localhost:1455/auth/callback` 是个开放端口，浏览器预取、别的页面、本机别的程序碰一下就行；
   真正的 state 是 256 位随机数，不对的请求本来就不该有任何效力。
2. **回调处理器可能在关服务时卡死**。结果通道容量为 1、处理器里都是阻塞发送（`auth.go:296,305,311,316`），主流程取走第一个结果就调
   `server.Shutdown(context.Background())`——一个**没有期限**的关闭。浏览器对回调地址连发几次（刷新、重试）时，第三个处理器会永远卡在发送上，
   `Shutdown` 又等它，登录 goroutine 就永远挂住（网关里它占着 1455 端口，直到进程退出）。
3. **页面点了「取消」之后，OpenCode 凭据仍可能落盘**。`LoginDeviceFlow` 在拿到令牌后调 `fetchAccount`——两个网络请求，**吞掉所有错误**（含 ctx 取消）——
   然后无条件 `Save`（`pkg/llm/opencode/auth.go:212-216`）。若取消恰好落在这两个请求期间，页面已经显示「已取消」，凭据却写进了磁盘。无害但状态说谎，
   而且「已取消的登录不留凭据」是用户能想到的最起码的语义。

## Current state

**ChatGPT 回调处理器**（`pkg/llm/openai/auth.go:283-332`，节选）：

```go
	type result struct {
		code string
		err  error
	}
	resultCh := make(chan result, 1)
	server := &http.Server{ReadHeaderTimeout: 10 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/callback" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("state") != state {
			http.Error(w, "State mismatch. Return to Forebrain Harness and try again.", http.StatusBadRequest)
			resultCh <- result{err: errors.New("OAuth state mismatch")}
			return
		}
		if oauthErr := r.URL.Query().Get("error"); oauthErr != "" {
			...
			http.Error(w, "Sign-in failed. Return to Forebrain Harness.", http.StatusBadRequest)
			resultCh <- result{err: fmt.Errorf("ChatGPT sign-in failed: %s", msg)}
			return
		}
		code := strings.TrimSpace(r.URL.Query().Get("code"))
		if code == "" {
			http.Error(w, "Missing authorization code.", http.StatusBadRequest)
			resultCh <- result{err: errors.New("ChatGPT callback did not include an authorization code")}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<!doctype html>...")
		resultCh <- result{code: code}
	})
	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			resultCh <- result{err: err}
		}
	}()

	var callback result
	select {
	case callback = <-resultCh:
	case <-ctx.Done():
		callback.err = ctx.Err()
	}
	_ = server.Shutdown(context.Background())
```

**OpenCode 设备流收尾**（`pkg/llm/opencode/auth.go:196-219`，节选）：

```go
	tokens, err := pollDeviceToken(ctx, deviceCode)
	if err != nil {
		return Credentials{}, err
	}
	...
	account, orgs := fetchAccount(ctx, tokens.AccessToken)
	c.AccountID = account.ID
	c.Email = account.Email
	c.OrgID, c.OrgName = selectOrg(c.OrgID, orgs)
	if err := Save(path, c); err != nil {
		return Credentials{}, fmt.Errorf("save OpenCode credentials: %w", err)
	}
	return c, nil
```

`fetchUser` / `fetchOrgs`（`:386-411`）在任何错误（含 ctx 取消）时返回零值，这是**有意的**（"The profile calls are cosmetic. A valid token must be kept even when they fail"）——
本计划不改它们，只在它们之后检查 ctx。

**现有测试夹具**：
- `pkg/llm/openai/auth_test.go`：`stubTokenEndpoint(t, handler)`（`:296`，替换 `tokenEndpoint`）、`jwtWithExp(t, exp)`（`:41`）、
  `TestLoginWithBrowserHandsTheAuthorizationURLToTheCallback`（`:269`，演示如何用 `openBrowser` 回调拿到授权 URL；监听口不可用时 `t.Skip`）。
- `pkg/llm/opencode/auth_test.go`：`stubConsole(t, url)`（`:20`）、`stubPollFloor(t)`（`:31`）、
  `TestLoginDeviceFlowCompletesAgainstFakeConsole`（`:99`，整条设备流对着假 console 跑；`/auth/device/code`、`/auth/device/token`、`/api/user`、`/api/orgs` 的假回答在里面）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 构建 | `go build ./...` | exit 0（`ld: warning` 可忽略） |
| 目标包测试 | `go test -tags fts5 ./pkg/llm/openai/ ./pkg/llm/opencode/ -count=1` | 两个 `ok` |
| 竞态 | `go test -tags fts5 -race ./pkg/llm/openai/ ./pkg/llm/opencode/ -run 'Login' -count=1` | `ok` |
| gofmt / vet | `test -z "$(gofmt -l pkg/llm)" && go vet ./pkg/llm/...` | exit 0 |
| 包图快照 | `scripts/package-graph.sh && git diff --stat -- pkg/architecture/testdata/graph.json` | 只有 `pkg/llm/openai`、`pkg/llm/opencode` 的 `loc` 变化 |

## Scope

**In scope**：
- `pkg/llm/openai/auth.go`、`pkg/llm/openai/auth_test.go`
- `pkg/llm/opencode/auth.go`、`pkg/llm/opencode/auth_test.go`
- `pkg/architecture/testdata/graph.json`（只允许 `scripts/package-graph.sh` 重新生成）

**Out of scope**：
- `pkg/gateway/login_api.go` 及前端——网关侧的「取消」语义不变。
- `fetchUser` / `fetchOrgs` 吞错误的行为（有意的）。
- 计划 101 涉及的刷新锁与 `Transport`；如果 101 已落地，`auth.go` 的 `Transport` 部分不要碰。
- 登录成功路径的任何行为、落盘格式、文案。

## Git workflow

- **不提交**。改动留在 dev 工作区。

## Steps

### Step 1：先写会红的测试

1. `pkg/llm/opencode/auth_test.go`，仿 `TestLoginDeviceFlowCompletesAgainstFakeConsole` 新增
   `TestLoginDeviceFlowDoesNotSaveAfterTheLoginIsCanceled`：假 console 照常回 `/auth/device/code`、`/auth/device/token`（直接给出令牌，不用 pending 轮询），
   但在处理 `/api/user` 时调用测试持有的 `cancel()`（`ctx, cancel := context.WithCancel(...)` 传给 `LoginDeviceFlow`），再回一个正常的用户 JSON。
   断言：`LoginDeviceFlow` 返回的 `err` 满足 `errors.Is(err, context.Canceled)`，且 `os.Stat(path)` 报 `os.IsNotExist`（凭据文件**不存在**）。
   **预期现在红**（流程忽略取消、照样落盘）。
2. `pkg/llm/openai/auth_test.go`，仿 `TestLoginWithBrowserHandsTheAuthorizationURLToTheCallback` 新增
   `TestLoginWithBrowserIgnoresACallbackWithTheWrongState`：
   - `stubTokenEndpoint` 回一份有效令牌：`{"access_token": jwtWithExp(t, time.Now().Add(time.Hour)), "refresh_token": "refresh-1", "expires_in": 3600}`。
   - `openBrowser` 回调里解析授权 URL 的 `redirect_uri` 与 `state` 两个查询参数，启动一个 goroutine：
     先 `GET <redirect_uri>?code=stray&state=WRONG`，断言状态码 400；再 `GET <redirect_uri>?code=real-code&state=<真 state>`。
   - 断言 `LoginWithBrowser` 返回无错、凭据文件已写入且 `RefreshToken == "refresh-1"`。
   - 监听口不可用（`openBrowser` 从未被调用）时 `t.Skipf`，写法同 `:269` 那条。
   **预期现在红**（第一个请求就让登录以 "OAuth state mismatch" 结束）。
3. 同文件新增 `TestLoginWithBrowserSurvivesRepeatedCallbacks`（尽力而为的并发测试）：同上，但 `openBrowser` 里**并发**发 4 个带真 state 的回调请求，
   整个 `LoginWithBrowser` 调用放进 goroutine，用 `select` + `time.After(5 * time.Second)` 断言它在期限内返回。
   这条在修复前只在调度碰巧时才会挂住——写完后用 `go test ... -run TestLoginWithBrowserSurvivesRepeatedCallbacks -count=30` 看看修复前是否出现超时；
   没出现也不要紧，它是修复后的护栏。

**Verify**：`go test -tags fts5 ./pkg/llm/openai/ ./pkg/llm/opencode/ -run 'TestLoginDeviceFlowDoesNotSaveAfterTheLoginIsCanceled|TestLoginWithBrowserIgnoresACallbackWithTheWrongState' -count=1 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'` →
**恰好这 2 个 `--- FAIL`**（若 1455/1457 端口在本机被占而第 2 条被 skip，则只有第 1 个 FAIL，并在报告里写明）。

### Step 2：OpenCode——落盘前确认登录没被取消

`pkg/llm/opencode/auth.go` 的 `LoginDeviceFlow`，在 `selectOrg` 那行之后、`Save` 之前加：

```go
	// fetchAccount swallows every error, a cancel included, so the flow gets
	// here even when the caller already gave up. A login the caller canceled
	// must leave nothing behind: the page shows "canceled", and a credentials
	// file appearing after that would make the page a liar.
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
```

**Verify**：`go test -tags fts5 ./pkg/llm/opencode/ -count=1` → `ok`（Step 1 的第 1 条变绿，其余设备流测试不变）。

### Step 3：ChatGPT——陌生请求不再决定登录的结局，关闭有期限

`pkg/llm/openai/auth.go` 的 `LoginWithBrowser`：

1. 在 `resultCh` 定义之后加一个不阻塞的投递函数，并把处理器和 `Serve` 协程里**所有**的 `resultCh <- ...` 换成 `deliver(...)`：

```go
	resultCh := make(chan result, 1)
	// The first answer decides the login; any later one (a browser retrying or
	// refreshing the callback) is dropped instead of blocking a handler that
	// Shutdown would then wait for forever.
	deliver := func(r result) {
		select {
		case resultCh <- r:
		default:
		}
	}
```

2. `state` 不符的分支改成只回 400、**不**投递：

```go
		if r.URL.Query().Get("state") != state {
			// Not our request: the state is a secret only the browser we sent
			// to the authorization page can know. Answering it with an error is
			// enough — ending the login for a stranger's request would let any
			// page on this machine cancel it.
			http.Error(w, "State mismatch. Return to Forebrain Harness and try again.", http.StatusBadRequest)
			return
		}
```

3. 关闭改成有期限的：把 `_ = server.Shutdown(context.Background())` 换成

```go
	shutdownCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
	}
	stop()
```

`error` 参数（state 正确时）与缺 `code`（state 正确时）两个分支**仍然**投递错误并结束登录——那是授权服务器或浏览器对**我们这次授权**的真实回答。

**Verify**：
- `go build ./pkg/llm/... && go vet ./pkg/llm/openai/` → exit 0。
- `go test -tags fts5 ./pkg/llm/openai/ -count=1` → `ok`（Step 1 的第 2、3 条通过，既有登录测试不变）。
- `go test -tags fts5 -race ./pkg/llm/openai/ ./pkg/llm/opencode/ -run 'Login' -count=1` → `ok`，无 `DATA RACE`。
- `go test -tags fts5 ./pkg/llm/openai/ -run TestLoginWithBrowserSurvivesRepeatedCallbacks -count=30` → `ok`（修复后 30 次都不超时）。

### Step 4：收尾门禁

1. `scripts/package-graph.sh`，`git diff --stat -- pkg/architecture/testdata/graph.json`：只应有 `pkg/llm/openai`、`pkg/llm/opencode` 的 `loc` 变化。
2. `test -z "$(gofmt -l pkg/llm)"` → exit 0；`go build ./... && go vet ./pkg/llm/...` → exit 0。
3. `go test -tags fts5 ./pkg/llm/... ./pkg/gateway/ -count=1` → 全 `ok`（网关的登录接口测试 stub 了这两个流程，确认没被牵连）。

## Test plan

见 Step 1：3 个新测试（2 个先红、1 个并发护栏）。结构样板：`TestLoginWithBrowserHandsTheAuthorizationURLToTheCallback`、`TestLoginDeviceFlowCompletesAgainstFakeConsole`。

## Done criteria

- [ ] `go test -tags fts5 ./pkg/llm/... -count=1` 全 `ok`
- [ ] Step 1 的 2 个先红的测试现在 `PASS`；既有测试无一被修改
- [ ] `grep -n 'resultCh <-' pkg/llm/openai/auth.go` 只剩 `deliver` 内的那一处
- [ ] `grep -n 'Shutdown(context.Background())' pkg/llm/openai/auth.go` 无命中
- [ ] `git diff --name-only -- pkg/architecture/testdata/graph.json` 只有这一个文件，且只含 loc 变化
- [ ] `git status --short` 里新增的改动只落在 In scope 列表的文件
- [ ] 没有任何 `git commit` / `git push` / 新分支

## STOP conditions

1. 摘录对不上现状（回调处理器、`LoginDeviceFlow` 收尾任一已被改过——尤其是计划 104 若已先落地，处理器会长得不一样）。
2. 既有的 `TestLoginWithBrowser...` / `TestLoginDeviceFlow...` 任一变红。
3. 为了让 Step 1 的第 2 条测试跑起来需要改 `listenLoopback` 的行为——说明测试写法不对（它应当用真实监听口），停下报告。
4. `scripts/package-graph.sh` 报告新增依赖边，或 `jq` 不可用。
5. 一个步骤的验证在合理修复后仍失败两次。

## Maintenance notes

- 计划 104 会在这个处理器旁边再加一条「手动投递回调」的入口，它的状态校验必须沿用本计划的原则：state 不符的输入只被拒绝、不终止登录。
- 本计划**没有**给 ChatGPT 流程加「落盘前检查 ctx」：`exchange` 自己带 ctx，取消落在换票期间会直接报错，取消落在换票完成与 `Save` 之间的窗口是微秒级、不可观察。
- 评审重点：`deliver` 是否把「第一个结果」之外的都丢了（登录只认第一个）；`error` / 缺 `code` 两个分支是否仍然终止登录。
