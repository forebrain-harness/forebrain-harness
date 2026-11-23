# Plan 101：同一份登录文件的令牌刷新按文件加锁，被别人先轮换时采用磁盘上的新登录（不再误报「登录已过期」）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不用 worktree，
> 改动直接留在 dev 工作区。
>
> **Drift check（先跑）**：
> `git diff --stat 55019a2 -- pkg/llm/opencode/transport.go pkg/llm/opencode/transport_test.go pkg/llm/openai/auth.go pkg/llm/openai/auth_test.go`
> 预期**无输出**。有输出就对照下面 "Current state" 的摘录：不符即 STOP 1。
> （工作区里 `pkg/gateway/*`、`pkg/config/*`、`pkg/run/*`、`pkg/turn/*`、`pkg/tui/*`、`frontend/*` 的既有改动与本计划无关，不要碰、不要回滚。）

## Status

- **Priority**: P2（间歇性误报；到期边界处并行请求才触发，但一触发用户看到的是「去 /connect 重新登录」）
- **Effort**: S–M（一个 ~20 行的共享锁小文件；两个 Transport 各改一个方法；6 个测试）
- **Risk**: LOW（只改「谁先刷新、刷新失败后怎么办」；成功路径的行为和落盘格式不变）
- **Depends on**: none
- **Category**: bug（并发正确性）
- **Planned at**: commit `55019a2`，2026-10-23

## Why this matters

OpenCode 与 ChatGPT 的刷新令牌**每次刷新都轮换**（`pkg/llm/opencode/auth.go:437-439` 的注释："The server rotates the refresh token, so the caller must persist the result"），
旧令牌再用就是 `invalid_grant`。两个 `Transport` 的刷新互斥锁是**每个实例一把**（`opencode/transport.go:67`、`openai/auth.go:536`），
而进程里同时存在很多个指向同一个登录文件的实例：每次构建 LLM（`pkg/run/config.go:550`，含每个并行子代理）、
每次模型探测（`pkg/process/models.go:42,100`）都 `new` 一个；TUI 与网关又是两个进程。

用一次性测试复现（stub 的 console 实现了轮换：旧 refresh token 一律 `invalid_grant`）：
同一文件上两个独立 `Transport` 同时在到期边界发请求，**一个成功、另一个失败，报「OpenCode login expired; run /connect again」**——
而此时磁盘上其实已经有对方刚写入的有效新登录。同一个 `Transport` 实例上的并发则只刷新一次（锁有效）。
这条错误被分类成 401，上层会提示「请重新登录」，用户会被引去白白重走一遍浏览器流程。

修法两层：① 锁按**登录文件路径**共享（同进程里不再互相踩）；② 刷新被 `invalid_grant` 拒绝后**先重读磁盘**——
如果文件里的 refresh token 已经不是我们发出去的那个、且新登录还没到期，说明别的实例/进程抢先轮换了，直接采用它（覆盖跨进程，不需要文件锁）。

## Current state

- `pkg/llm/opencode/transport.go` — OpenCode 的 `Transport`；每次请求前 `token()` 读文件、需要时刷新并写回。
- `pkg/llm/openai/auth.go` — ChatGPT 的 `Transport`，同形（`current()`）。
- `pkg/llm/opencode/transport_test.go` — 现有夹具：`writeCredentials`、`refreshServer`、`stubConsole`、`headerRecorder`、`captureTransport`。
- `pkg/llm/openai/auth_test.go` — 现有夹具：`stubTokenEndpoint`，以及 `TestTransportRefreshTreatsInvalidGrantAsExpiredLogin`（`:347`）的写法。
- `pkg/llm` 是第 0 层包，opencode 与 openai 两个 provider 包都已经 import 它；共享锁放这里不会新增包依赖边。

**OpenCode（`transport.go:55-131`，节选）**：

```go
type Transport struct {
	Path string
	Base http.RoundTripper
	key string
	// mu serializes refresh so that several goroutines noticing the same
	// expiring token refresh it once: ...
	mu sync.Mutex
}

func (t *Transport) token(ctx context.Context) (string, error) {
	if t.key != "" || strings.TrimSpace(t.Path) == "" {
		return t.key, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c, err := Load(t.Path)
	if err != nil {
		return "", fmt.Errorf("OpenCode login required; run /connect to log in: %w", err)
	}
	if time.Until(c.ExpiresAt) > refreshMargin {
		return c.AccessToken, nil
	}
	updated, err := refresh(ctx, c)
	if err != nil {
		return "", err
	}
	if err := Save(t.Path, updated); err != nil {
		return "", fmt.Errorf("save refreshed OpenCode credentials: %w", err)
	}
	return updated.AccessToken, nil
}
```

`refresh`（`opencode/auth.go:440`）遇 `invalid_grant` 返回
`&llm.APIError{StatusCode: 401, Message: loginExpiredMessage}`（`loginExpiredMessage = "OpenCode login expired; run /connect again"`）。
`refreshMargin = 60 * time.Second`。

**ChatGPT（`openai/auth.go:532-621`，节选）**：

```go
type Transport struct {
	Path string
	Base http.RoundTripper
	mu   sync.Mutex
}

func (t *Transport) current(ctx context.Context) (Credentials, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, err := Load(t.Path)
	if err != nil {
		return Credentials{}, fmt.Errorf("ChatGPT login required: %w", err)
	}
	if time.Until(c.ExpiresAt) > 2*time.Minute {
		return c, nil
	}
	tokens, err := exchange(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.RefreshToken},
		"client_id":     {ClientID},
	})
	if err != nil {
		// A dead refresh token is the login itself being gone ...
		var api *llm.APIError
		if errors.As(err, &api) && api.Code == "invalid_grant" {
			return Credentials{}, &llm.APIError{
				StatusCode: http.StatusUnauthorized,
				Message:    "ChatGPT login expired; sign in again",
			}
		}
		return Credentials{}, fmt.Errorf("refresh ChatGPT login: %w", err)
	}
	...
```

ChatGPT 的凭据文件由 `openai.Save`/`openai.Load` 读写，`Credentials` 有 `AccessToken`、`RefreshToken`、`ExpiresAt`。

**约定**：错误消息文案**不要改**——`TestTransportInvalidGrantRequiresLogin`（opencode）与
`TestTransportRefreshTreatsInvalidGrantAsExpiredLogin`（openai，断言消息含 "sign in again" 且**不含** "/connect"）钉住了它们。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 构建 | `go build ./...` | exit 0（`ld: warning` 可忽略） |
| 目标包测试 | `go test -tags fts5 ./pkg/llm/ ./pkg/llm/opencode/ ./pkg/llm/openai/ -count=1` | 三个 `ok` |
| 竞态 | `go test -tags fts5 -race ./pkg/llm/opencode/ ./pkg/llm/openai/ -run 'Transport|Credential' -count=1` | `ok`，无 `DATA RACE` |
| gofmt | `test -z "$(gofmt -l pkg/llm)"` | exit 0 |
| vet | `go vet ./pkg/llm/...` | exit 0 |
| 包图快照 | `scripts/package-graph.sh && git diff --stat -- pkg/architecture/testdata/graph.json` | 只有 `pkg/llm`、`pkg/llm/opencode`、`pkg/llm/openai` 的 `loc` 变化，没有新增边 |

## Scope

**In scope**：
- `pkg/llm/credential_lock.go`（新建）、`pkg/llm/credential_lock_test.go`（新建）
- `pkg/llm/opencode/transport.go`、`pkg/llm/opencode/transport_test.go`
- `pkg/llm/openai/auth.go`、`pkg/llm/openai/auth_test.go`
- `pkg/architecture/testdata/graph.json`（只允许 `scripts/package-graph.sh` 重新生成）

**Out of scope**：
- `pkg/process/models.go`、`pkg/run/config.go`——调用方不动：锁按路径共享后，它们各自 `new` 的实例自然共用一把锁。
- 跨进程文件锁（`flock`）——仓库支持 Windows（`openBrowserDefault` 有 windows 分支），`flock` 要按平台拆文件；「invalid_grant 后重读磁盘」已覆盖跨进程，不引入。
- `opencode.Save` / `openai.Save` 的落盘格式与权限（0600 原子替换）。
- 登录流程（`LoginDeviceFlow`、`LoginWithBrowser`）、错误文案。

## Git workflow

- **不提交**。改动留在 dev 工作区。

## Steps

### Step 1：共享的按路径互斥锁

新建 `pkg/llm/credential_lock.go`：

```go
package llm

import (
	"path/filepath"
	"sync"
)

// credentialLocks holds one mutex per credentials file, so every client of a
// login file in this process refreshes it one at a time. A refresh token that
// rotates on use cannot be refreshed twice from the same old value: the second
// caller must wait, then read what the first one wrote.
var credentialLocks sync.Map // absolute path -> *sync.Mutex

// CredentialMutex returns the mutex that serializes token refresh for the
// credentials file at path. Two spellings of one file ("a/../f" and "f") share
// it; the lock lives for the life of the process.
func CredentialMutex(path string) *sync.Mutex {
	key := filepath.Clean(path)
	if abs, err := filepath.Abs(key); err == nil {
		key = abs
	}
	mu, _ := credentialLocks.LoadOrStore(key, &sync.Mutex{})
	return mu.(*sync.Mutex)
}
```

新建 `pkg/llm/credential_lock_test.go`，包名 `llm`，用 `testing` 标准库风格（看同目录任一 `_test.go` 的 import 习惯）：

- `TestCredentialMutexIsSharedPerFile`：`CredentialMutex(p) == CredentialMutex(p)`；`CredentialMutex(filepath.Join(dir, "x", "..", "f.json")) == CredentialMutex(filepath.Join(dir, "f.json"))`；
  `CredentialMutex(a) != CredentialMutex(b)`（两个不同路径）。

**Verify**：`go test -tags fts5 ./pkg/llm/ -run TestCredentialMutexIsSharedPerFile -count=1` → `ok`。

### Step 2：先写会红的 OpenCode 测试

在 `pkg/llm/opencode/transport_test.go` 里，仿 `TestTransportRefreshesOnceForConcurrentRequests`（`:152`）与 `TestTransportInvalidGrantRequiresLogin`（`:211`）的写法
（`httptest.NewServer` + `stubConsole(t, server.URL)` + `writeCredentials` + `headerRecorder`/`captureTransport` 做 `Base`），新增：

1. `TestTransportsSharingAFileRefreshOnce`：stub console 实现**轮换**——持有 `current := "refresh-1"`，请求体的 `refresh_token` 等于 `current` 就把 `current` 改成 `"refresh-2"`
   并返回 `{"access_token":"access-2","refresh_token":"refresh-2","expires_in":3600}`，否则返回 400 `{"error":"invalid_grant"}`；每次进来计数并 `time.Sleep(20 * time.Millisecond)`。
   文件初始 `RefreshToken: "refresh-1"`、`ExpiresAt: time.Now().Add(-time.Minute)`。**两个**独立 `&Transport{Path: path, Base: recorder}` 各开若干 goroutine 并发 `RoundTrip`。
   断言：所有 `RoundTrip` 都无错；console 收到的刷新请求数恰为 **1**；每个请求带的 `Authorization` 都是 `Bearer access-2`。**预期现在红**（两个实例各刷新一次，后到者 `invalid_grant`）。
2. `TestTransportAdoptsALoginAnotherProcessRotated`：stub console 在收到第一次刷新时，**先**用 `writeCredentials` 往同一个文件写入
   `{AccessToken: "access-other", RefreshToken: "refresh-other", ExpiresAt: time.Now().Add(time.Hour)}`（模拟另一个进程抢先轮换并落盘），再返回 400 `invalid_grant`。
   单个 `Transport` 发一次请求；断言无错、`Authorization` 为 `Bearer access-other`、console 只被调用 1 次。**预期现在红**。
3. `TestTransportDoesNotAdoptARotationThatIsAlreadyExpiring`：同 2，但另一个进程写入的新登录 `ExpiresAt` 是 `time.Now().Add(10 * time.Second)`（小于 `refreshMargin`）；
   断言仍然返回 401 的 `*llm.APIError`（消息含 `/connect`）——采用一个马上又要过期的登录没有意义。（现有行为，旧代码下已绿，护栏。）

既有的 `TestTransportInvalidGrantRequiresLogin`（文件没被别人改过 → 仍然 401）**不许改**，必须仍绿。

`headerRecorder` 的通道要开够容量（现有并发测试用 `make(chan string, 16)`），两个 `Transport` 可以共用同一个 recorder 指针。
同文件里 `TestTransportRefreshesOnceForConcurrentRequests`（`:152`）的测试**体内**（`:164-165`，`shared := &Transport{...}` 那行的上方）有注释
「One transport shared by every goroutine: the mutex that makes the refresh single is per transport, so this is the shape production has」——
锁改成按文件之后这句话不再成立，**只改这句注释**（说明锁按文件共享、这里用一个实例只是最简形态），测试代码一行不动。
（该测试上方的函数 doc 注释 `:147-151` 说的是「并发的几个 goroutine 只能刷一次」，改锁后依然成立，不要动它。）

**Verify**：`go test -tags fts5 ./pkg/llm/opencode/ -run 'TestTransportsSharingAFileRefreshOnce|TestTransportAdoptsALoginAnotherProcessRotated|TestTransportDoesNotAdoptARotationThatIsAlreadyExpiring' -count=1 2>&1 | grep -E '^(--- FAIL|FAIL|ok)'` →
**恰好 1、2 两个 `--- FAIL`**（3 已绿）。

### Step 3：OpenCode 的 `token()` 改用共享锁并采用被轮换的登录

`pkg/llm/opencode/transport.go`：

1. 删掉 `Transport` 里的 `mu sync.Mutex` 字段及其注释（锁不再属于实例）；`sync` import 若不再被本文件使用就一并删（本文件另有 `sync/atomic`，注意区分）。
2. `token()` 改成：

```go
func (t *Transport) token(ctx context.Context) (string, error) {
	if t.key != "" || strings.TrimSpace(t.Path) == "" {
		return t.key, nil
	}
	// One lock per login file, not per Transport: every client of this file in
	// the process (a turn's LLM, a subagent's, a model-listing probe) shares it,
	// so the refresh token — which rotates on use — is exchanged once.
	mu := llm.CredentialMutex(t.Path)
	mu.Lock()
	defer mu.Unlock()
	c, err := Load(t.Path)
	if err != nil {
		return "", fmt.Errorf("OpenCode login required; run /connect to log in: %w", err)
	}
	if time.Until(c.ExpiresAt) > refreshMargin {
		return c.AccessToken, nil
	}
	updated, err := refresh(ctx, c)
	if err != nil {
		if adopted, ok := adoptRotatedLogin(t.Path, c, err); ok {
			return adopted.AccessToken, nil
		}
		return "", err
	}
	if err := Save(t.Path, updated); err != nil {
		return "", fmt.Errorf("save refreshed OpenCode credentials: %w", err)
	}
	return updated.AccessToken, nil
}

// adoptRotatedLogin handles a refresh the server refused as invalid_grant. The
// refresh token we sent may simply be stale: another process (the TUI beside
// the gateway) exchanged it first and wrote the rotated pair. When the file now
// holds a different refresh token and a login that is not about to expire, that
// login is the live one — use it instead of reporting a dead login.
func adoptRotatedLogin(path string, sent Credentials, refreshErr error) (Credentials, bool) {
	var api *llm.APIError
	if !errors.As(refreshErr, &api) || api.StatusCode != http.StatusUnauthorized {
		return Credentials{}, false
	}
	latest, err := Load(path)
	if err != nil || latest.RefreshToken == sent.RefreshToken {
		return Credentials{}, false
	}
	if time.Until(latest.ExpiresAt) <= refreshMargin {
		return Credentials{}, false
	}
	return latest, true
}
```

需要的 import（`transport.go` 现有 import 里补齐缺的）：`errors`。`net/http`、`fmt`、`context`、`strings`、`time`、`llm` 已在。

**Verify**：
- `go build ./pkg/llm/... && go vet ./pkg/llm/opencode/` → exit 0。
- `go test -tags fts5 ./pkg/llm/opencode/ -count=1` → `ok`（Step 2 的 1、2 变绿，既有测试全绿）。
- `go test -tags fts5 -race ./pkg/llm/opencode/ -run 'Transport' -count=1` → `ok`，无 `DATA RACE`。

### Step 4：ChatGPT 同样处理

先在 `pkg/llm/openai/auth_test.go` 里仿 `TestTransportRefreshTreatsInvalidGrantAsExpiredLogin`（`:347`，用 `stubTokenEndpoint` 和 `os.WriteFile` 写文件）新增：

1. `TestTransportsSharingAFileRefreshOnce`：stub 令牌端点做同样的**轮换**（旧 refresh token → 400 `{"error":"invalid_grant"}`，新的 → 返回新 `access_token` 和 `refresh_token`）。
   **注意**：ChatGPT 凭据的到期时间不是文件里的字段，而是 `Load` 从 access token 这个 JWT 的 `exp` 声明里读出来的（`auth.go:157` 的 `accessTokenExpiry`）。
   所以文件里的初始 access token 用 `jwtWithExp(t, time.Now().Add(-time.Minute))`（已过期），stub 返回的新 `access_token` 用 `jwtWithExp(t, time.Now().Add(time.Hour))`（`jwtWithExp` 在 `auth_test.go:41`）；非 JWT 的 access token 会被读成「已过期」。
   两个独立 `&Transport{Path: path}` 并发调 `current`；断言都无错、令牌端点只收到 1 次刷新。**预期现在红**。
2. `TestTransportAdoptsALoginAnotherProcessRotated`：令牌端点在第一次刷新时先用 `Save(path, Credentials{AccessToken: jwtWithExp(t, time.Now().Add(time.Hour)), RefreshToken: "refresh-other"})` 往同一文件写入一份 refresh token 不同、一小时后才到期的凭据，再返回 `invalid_grant`；
   断言 `current` 返回无错并给出那份新凭据（`AccessToken` 与写入的一致）。**预期现在红**。
3. 既有 `TestTransportRefreshTreatsInvalidGrantAsExpiredLogin` 不许改，必须仍绿。

然后 `pkg/llm/openai/auth.go`：删 `Transport.mu` 字段；`current()` 开头改用 `llm.CredentialMutex(t.Path)`；
把 `invalid_grant` 分支改成**先尝试采用磁盘上被轮换的登录**，再退回原来的 401：

```go
		var api *llm.APIError
		if errors.As(err, &api) && api.Code == "invalid_grant" {
			if latest, loadErr := Load(t.Path); loadErr == nil &&
				latest.RefreshToken != c.RefreshToken &&
				time.Until(latest.ExpiresAt) > 2*time.Minute {
				return latest, nil
			}
			return Credentials{}, &llm.APIError{
				StatusCode: http.StatusUnauthorized,
				Message:    "ChatGPT login expired; sign in again",
			}
		}
```

（`2*time.Minute` 就是该方法顶部已经在用的那个阈值；保持一致，别引入新常量。）`pkg/llm` 已在 `openai/auth.go` 的 import 里（用到 `llm.APIError`）。

**Verify**：
- `go build ./pkg/llm/... && go vet ./pkg/llm/openai/` → exit 0。
- `go test -tags fts5 ./pkg/llm/openai/ -count=1` → `ok`。
- `go test -tags fts5 -race ./pkg/llm/openai/ -run 'Transport' -count=1` → `ok`。

### Step 5：收尾门禁

1. `scripts/package-graph.sh`，然后 `git diff --stat -- pkg/architecture/testdata/graph.json`：只应有 `pkg/llm`、`pkg/llm/opencode`、`pkg/llm/openai` 的 `loc` 数字变化；出现新增边 → STOP 3。
2. `test -z "$(gofmt -l pkg/llm)"` → exit 0。
3. `go build ./... && go vet ./pkg/llm/...` → exit 0。
4. `go test -tags fts5 ./pkg/llm/... ./pkg/process/ ./pkg/run/ -count=1` → 全 `ok`（`pkg/process`、`pkg/run` 里有构造 `opencode.Transport` / `openai.Transport` 的调用方，确认没被牵连）。

## Test plan

见 Step 1–4：共享锁 1 个、OpenCode 3 个、ChatGPT 2 个（外加 2 个既有测试作回归钉子）。
**可证伪性**：OpenCode 的 1、2 与 ChatGPT 的 1、2 在旧代码下必须红（Step 2 的 Verify 已要求先看到 FAIL；Step 4 同理，先写测试再改实现时自行确认）。

## Done criteria

- [ ] `go test -tags fts5 ./pkg/llm/... -count=1` 全 `ok`
- [ ] `go test -tags fts5 -race ./pkg/llm/opencode/ ./pkg/llm/openai/ -run 'Transport|Credential' -count=1` → `ok`
- [ ] `grep -n 'mu *sync.Mutex' pkg/llm/opencode/transport.go pkg/llm/openai/auth.go` 无命中
- [ ] `grep -rn 'CredentialMutex' pkg --include='*.go' | grep -v _test` 恰有 3 处（定义 + opencode + openai）
- [ ] 既有的 `TestTransportInvalidGrantRequiresLogin`（opencode）与 `TestTransportRefreshTreatsInvalidGrantAsExpiredLogin`（openai）**未被修改**且 `PASS`
- [ ] `git diff --name-only -- pkg/architecture/testdata/graph.json` 只有这一个文件，且只含 loc 变化
- [ ] `git status --short` 里新增的改动只落在 In scope 列表的文件
- [ ] 没有任何 `git commit` / `git push` / 新分支

## STOP conditions

1. 摘录对不上现状（`token()`、`current()`、`Transport` 字段任一已被改过）。
2. 既有的 `invalid_grant` 两条测试在你的改动后变红——说明采用逻辑误吞了「文件没被别人改过」的真死登录，回头检查 `latest.RefreshToken == sent.RefreshToken` 的判断，不要改测试。
3. `scripts/package-graph.sh` 报告新增依赖边（说明共享锁放错了层），或 `jq` 不可用。
4. 发现有第三个地方在 `new` 登录文件的 Transport 且**不**经过这两个类型（比如另一个 provider 包的同形实现）——停下报告，不要顺手改。
5. 一个步骤的验证在合理修复后仍失败两次。

## Maintenance notes

- 采用逻辑的前提是「别人轮换后会落盘」。若服务器对旧 refresh token 有宽限期（重用时返回另一对新令牌而不是 `invalid_grant`），
  两个进程各存一对时后写者会覆盖先写者，先写者的 refresh token 随后失效——这是服务器语义，本计划不处理；若日后在真机观察到，再考虑跨进程文件锁（需按平台拆文件）。
- 再加第三个 OAuth 登录供应商时，直接用 `llm.CredentialMutex` 与同样的「invalid_grant 后重读」模式；到那时可以把两套 `Transport` 的刷新骨架抽成一份（本计划有意不做，避免跨 provider 重构）。
- 评审重点：`adoptRotatedLogin` 只在 401 型 `invalid_grant` 后触发，且要求磁盘上的 refresh token **不同**——这两个条件缺一都会把真死登录误当活的。
