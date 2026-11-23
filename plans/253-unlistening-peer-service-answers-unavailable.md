# Plan 253: 还没监听的 peer 服务对外一律答「不可用」，让 237 的目录兜底重新生效

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 236fc32a..HEAD -- pkg/process/peer_outbound.go pkg/process/peers.go pkg/process/peer_worktree.go pkg/process/peer_worktree_test.go pkg/process/peer_outbound_test.go pkg/process/peers_test.go`
> → 必须为空。不为空就把下面「Current state」里的摘录和实际代码逐段对照，对不上即 STOP。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW（只收紧一个判据：「在跑」改成「在跑且有监听地址」；已登记进程的行为一个字节都不变）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `236fc32a`, 2026-11-06
- **Finding**: 2026-11-06 分支审计（238–245 实现细节），发现 1（`introduced` by plan 240）

## Why this matters

本进程作为「本机 peer」登记时，第一次登记可能失败，比如 socket 目录被占、磁盘满、权限不对。
plan 237 为这种情况做了两件事：

1. tick 循环每拍重试登记；
2. 「这个目录树里还有谁」的视图（`WorkTreePeers`）不依赖本进程自己的登记。服务不可用时，直接读会合目录（rendezvous directory）作答，即 `workTreePeersFromDirectory`。

plan 240 把服务接线（`env.setPeerService(svc)`）提前到第一次登记之前，好让重试成功后收信、
放行、`peer_list`/`peer_send` 立刻可用。但这样一来，**登记失败、正在重试**的这段时间里，
`env.peers` 已经非空，`svc.running == true`，而 `svc.layout`/`svc.addr` 还是零值。`peerOutbound.view`
只看 `running`，于是返回了一个**目录为空串**的视图。后果是：

- `WorkTreePeers` 拿到 `err == nil` 的视图，不再走 237 的目录兜底；`candidates()` 读
  `peer.ReadRecords("")` 得到 nil，所以**同目录树里明明有人，也回答 0 个**。用到它的地方有两处：
  - 每轮请求前的「本目录树还有其他代理」提醒（`pkg/run/config.go:1523`）；
  - TUI 的 `/peers` 计数（`pkg/tui/chat_slash.go:1196`）。
- `peer_list` 答「没有任何对端」，`peer_send` 答「找不到这个名字」，而正确答案是
  「Peer messaging is not available right now.」。
- 同一个登记失败连报两条 WARN：`StartPeers` 一条，第一拍 `enterPendingModeLocked` 又一条。
  237 §4 的约定是只报一次。

**已实测**（scratchpad 拷贝 HEAD、临时测试，不进仓库）：在同树里放一个活着的邻居记录，并把 socket
目录堵住。`StartPeers` 之前 `WorkTreePeers` 答 1 个邻居（走兜底），`StartPeers` 失败之后答 0 个。

## Current state

- `pkg/process/peer_outbound.go:148-180`：`view` 是 `List`、`Send`、`WorkTreePeers`、
  `peerTasksByAddress`、`guardIdentity` 共用的唯一入口：

```go
// view snapshots the running service. A process that is not registered as a
// peer right now answers errPeerMessagingUnavailable. The session is the
// conversation that is speaking: its approval mode is what the recipient reads
// in from-mode, and a session this process does not run reads as prompting.
func (o *peerOutbound) view(sessionID string) (*peerOutboundView, error) {
	svc := o.service()
	if svc == nil {
		return nil, errPeerMessagingUnavailable
	}
	svc.mu.Lock()
	if !svc.running {
		svc.mu.Unlock()
		return nil, errPeerMessagingUnavailable
	}
	view := &peerOutboundView{
		dir:    svc.layout.Dir,
		cc:     svc.ccMode,
		addr:   svc.addr,
		socket: svc.socketPath,
		hopKey: svc.hopKey,
		name:   svc.name,
	}
```

  注释说的是「没登记就答 unavailable」，代码判的却是 `running`。

- `pkg/process/peers.go:355-378`：`start` 先置 `running = true`、起 tick 循环，再尝试登记：

```go
	svc.mu.Lock()
	defer svc.mu.Unlock()
	svc.running = true
	go svc.tickLoop()
	if err := svc.enterModeLocked(target, false); err != nil {
		return err
	}
	svc.modeEntered = true
	return nil
```

- `pkg/process/peers.go:481-536`：`enterModeLocked` 只有在 socket 监听成功之后才写
  `svc.layout`/`svc.addr`；`teardownSocketLocked` 把 `svc.addr` 清成 `""`。所以
  **`svc.addr != ""` ⟺ 此刻在某个 socket 上监听**。登记失败、模式切换失败两种情况下 `addr` 都是空串。

- `pkg/process/peers.go:313-324`：240 的接线顺序（**保留不动**）：

```go
	env.setPeerService(svc)
	if err := svc.start(ctx); err != nil {
		slog.Warn("peer registration did not start; retrying every tick", "err", err)
	}
	return stop
```

- `pkg/process/peers.go:437-448`：重试路径的「只报一次」：

```go
func (svc *peerService) enterPendingModeLocked(target bool) {
	if err := svc.enterModeLocked(target, false); err != nil {
		// One warning names the reason; the retries after it are debug lines, so
		// a directory that stays unusable does not fill the log.
		if !svc.enterWarned {
			svc.enterWarned = true
			slog.Warn("peer registration could not be entered; retrying every tick", "err", err, "target_cc", target)
		} else {
			slog.Debug("peer registration retry failed", "err", err, "target_cc", target)
		}
		return
	}
	svc.modeEntered = true
}
```

- `pkg/process/peer_worktree.go:446-470`：`WorkTreePeers` 在 `view` 报错时才走兜底：

```go
	view, err := o.view(caller.SessionID)
	if err != nil {
		// This process's registration is not what makes the tree shared: the
		// rendezvous directory is readable without it, so a service that never
		// started still answers (plan 237 §2).
		o.warnUnavailableOnce(err)
		return o.workTreePeersFromDirectory(resolvedRoot)
	}
```

- 现有测试：
  - `pkg/process/peer_worktree_test.go:177` 的 `TestPeerWorkTreeAnswersWhenTheServiceNeverStarted` 只覆盖 `env.peers == nil`，即手装端口、根本没调 `StartPeers` 的情况；
  - `pkg/process/peers_test.go:577` 的 `TestPeerServiceRetriesAFailedStart` 覆盖「堵住 socket 目录 → 失败 → 移除阻塞物 → 重试成功」，但不问 `WorkTreePeers`。

  测试工具：
  - `peerTestEnvFor(t, kind, session)`（`peers_test.go:71`）设好四个目录覆盖；
  - `terminalPeer(socket, cwd, name, agents...)`（`peer_outbound_test.go:151`）造一条记录；
  - `ownRecord(t, dir)`（`peers_test.go:119`）、`waitFor(t, what, cond)`（`peers_test.go:106`）；
  - `peerWarnCapture`（`peer_worktree_test.go:337-370`）收集 WARN 日志。

- 仓库规则：`X_test.go` 必须与 `X.go` 同名，由 `pkg/architecture` 的
  `TestTestFilesCorrespondToProductionFiles` 检查。所以新测试放进已有的
  `peer_worktree_test.go`、`peer_outbound_test.go`、`peers_test.go`，**不要**新建测试文件。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'Peer' -count=1` | `ok` |
| Neighbours | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/run ./pkg/tui -count=1` | `ok` |
| House rule | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |

## Scope

**In scope**:
- `pkg/process/peer_outbound.go`：只改 `view` 的判据和它的注释
- `pkg/process/peers.go`：`start` 失败分支置 `enterWarned`（step 2）
- `pkg/process/peer_worktree_test.go`、`pkg/process/peer_outbound_test.go`、`pkg/process/peers_test.go`：加测试

**Out of scope**:
- 240 的接线顺序（`env.setPeerService(svc)` 在 `svc.start` 之前）。它是对的：收信、放行要在重试成功的那一拍就可用。**不要**挪回去。
- `start` 里「先起 tick 循环再尝试登记」的顺序（237 §3）。
- `workTreePeersFromDirectory` 本身的逻辑，以及 `warnUnavailableOnce`。
- `peerInbox`（收信、放行）对未登记服务的行为。240 的测试钉住了「放行走到 inbox」，保持原样。

## Steps

### Step 1: 没有监听地址就等于没有服务

把 `peer_outbound.go` 里 `view` 的判据和注释改成：

```go
// view snapshots the registration this process is answering on. A process
// that is not listening on a socket right now — no service, a service whose
// first registration is still being retried (plan 237 §3, wired early by plan
// 240), or one whose mode switch failed — answers errPeerMessagingUnavailable:
// it has no directory to list, no address to send from and no name to sign
// with, and the error is what sends WorkTreePeers to the rendezvous directory.
// The session is the conversation that is speaking: its approval mode is what
// the recipient reads in from-mode, and a session this process does not run
// reads as prompting.
func (o *peerOutbound) view(sessionID string) (*peerOutboundView, error) {
	svc := o.service()
	if svc == nil {
		return nil, errPeerMessagingUnavailable
	}
	svc.mu.Lock()
	if !svc.running || svc.addr == "" {
		svc.mu.Unlock()
		return nil, errPeerMessagingUnavailable
	}
```

后面的代码不动。

**Verify**: `go build ./...` → exit 0；`grep -n 'if !svc.running || svc.addr == ""' pkg/process/peer_outbound.go` → 恰好 1 行

### Step 2: 同一个登记失败只报一次 WARN

在 `peers.go` 的 `start` 里，第一次 `enterModeLocked` 失败时把 `enterWarned` 置上。
`StartPeers` 那条 WARN 已经说出了原因，后面的重试就应该只是 debug 行：

```go
	svc.running = true
	go svc.tickLoop()
	if err := svc.enterModeLocked(target, false); err != nil {
		// StartPeers reports this failure; the retries the loop makes after it
		// are debug lines (plan 237 §4: one warning per cause).
		svc.enterWarned = true
		return err
	}
```

`svc.mu` 此时正被 `start` 持有（`defer svc.mu.Unlock()`），直接赋值即可。

**Verify**: `go build ./...` → exit 0

### Step 3: 钉住「失败启动后兜底仍生效」

在 `pkg/process/peer_worktree_test.go` 里，紧跟 `TestPeerWorkTreeAnswersWhenTheServiceNeverStarted` 之后
新增 `TestPeerWorkTreeAnswersWhileTheFirstRegistrationIsRetried`：

```go
// The service is wired before its first registration (plan 240), so while a
// failed first attempt is being retried env.peers is set but nothing listens.
// That service must answer like no service: the tree's neighbours still come
// from the rendezvous directory (plan 237 §2), never from an empty listing.
func TestPeerWorkTreeAnswersWhileTheFirstRegistrationIsRetried(t *testing.T) {
	env := peerTestEnvFor(t, "tui", func() string { return "cli-guard-retry" })
	blocker := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocker, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOREBRAIN_PEER_SOCKET_DIR", filepath.Join(blocker, "sock"))

	dir := peer.NewLayout(false).Dir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := terminalPeer(filepath.Join(env.dirs.fbSock, "900272.sock"), env.LaunchDir, "editing",
		peer.ExtAgent{ID: "cli-editing", Name: "editing", Kind: "primary", Root: env.LaunchDir,
			Status: peer.StatusBusy, Task: "Rewriting the importer"})
	rec.PID = os.Getppid()
	rec.ProcStart, _ = peer.ProcStart(rec.PID)
	if err := peer.WriteRecord(dir, rec); err != nil {
		t.Fatal(err)
	}

	stop := env.StartPeers(context.Background(), PeerSurface{Kind: "tui", CurrentSession: func() string { return "cli-guard-retry" }})
	defer stop()
	if env.peers == nil {
		t.Fatal("this case needs the service wired while its registration is retried")
	}
	if rec := ownRecord(t, env.dirs.fbDir); rec != nil {
		t.Fatalf("record = %+v, want the first attempt to have failed", rec)
	}
	peers := env.Deps.Peers.WorkTreePeers(context.Background(), env.LaunchDir)
	if len(peers) != 1 || peers[0].Name != "editing" {
		t.Fatalf("peers = %#v, want the neighbour from the rendezvous directory", peers)
	}
}
```

如果该文件缺少 `os`、`path/filepath`、`peer` 的 import 就补上，以编译通过为准。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestPeerWorkTreeAnswersWhileTheFirstRegistrationIsRetried' -count=1 -v` → `PASS`

**负对照（必须做，做完复原）**：临时把 step 1 的 `|| svc.addr == ""` 删掉再跑同一条命令，
应失败于 `want the neighbour from the rendezvous directory`，`peers = []`。观察到后复原 step 1。

### Step 4: 钉住 `peer_list` / `peer_send` 的答案

在 `pkg/process/peer_outbound_test.go` 末尾新增 `TestPeerToolsAnswerUnavailableWhileTheFirstRegistrationIsRetried`。
用 step 3 同样的 socket 目录阻塞手法和 `StartPeers` 调用，然后：

```go
	port := env.outbound()
	if port == nil {
		t.Fatal("this process has no port onto the other processes")
	}
	caller := run.PeerCaller{SessionID: "cli-tools-retry"}
	if _, err := port.List(context.Background(), caller); !errors.Is(err, errPeerMessagingUnavailable) {
		t.Fatalf("List while unregistered = %v, want %v", err, errPeerMessagingUnavailable)
	}
	if _, err := port.Send(context.Background(), caller, "editing", "hello"); !errors.Is(err, errPeerMessagingUnavailable) {
		t.Fatalf("Send while unregistered = %v, want %v", err, errPeerMessagingUnavailable)
	}
```

`run.PeerCaller` 的字段名以 `pkg/run` 里的定义为准。如果 `Send` 在调 `view` 之前就因别的原因返回
（比如对空名字做校验），换一个非空的目标名再试；`view` 必须是第一个失败点。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestPeerToolsAnswerUnavailableWhileTheFirstRegistrationIsRetried' -count=1 -v` → `PASS`

### Step 5: 钉住「只报一次」

在 `pkg/process/peers_test.go` 的 `TestPeerServiceRetriesAFailedStart` **之后**新增
`TestPeerServiceWarnsOnceForAFailedFirstRegistration`：

- 用 `peerWarnCapture`（同包、定义在 `peer_worktree_test.go`）替换默认 logger，
  `t.Cleanup` 里恢复；
- 堵住 socket 目录，调 `StartPeers`，`time.Sleep(3 * peerTickInterval)` 让循环至少重试两次；
- 断言收集到的 WARN 里，消息以 `peer registration` 开头的**恰好 1 条**。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process -run 'TestPeerServiceWarnsOnceForAFailedFirstRegistration' -count=1 -v` → `PASS`

**负对照**：临时删掉 step 2 的 `svc.enterWarned = true`，应失败（2 条）；观察后复原。

### Step 6: 全量回归

**Verify**：
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/run ./pkg/tui -count=1` → 三个 `ok`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`

## Test plan

- 新增 3 个测试：worktree 兜底、peer 工具答 unavailable、WARN 只报一次。前两个和第三个各有一个负对照。
- 保持绿：
  - `TestPeerServiceRetriesAFailedStart`：240 的断言「放行走到 inbox，不报 no peer service is running」不受影响，因为 `ReleasePeerMessage` 不经过 `view`；
  - `TestPeerWorkTree*`；
  - `TestPeerInbound*`；
  - 模式切换用例。

## Done criteria

ALL must hold:

- [ ] `go build ./...`、`go vet ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/process ./pkg/run ./pkg/tui -count=1` 全部 `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- [ ] `grep -n 'if !svc.running || svc.addr == ""' pkg/process/peer_outbound.go` → 1 行
- [ ] `grep -n 'env.setPeerService(svc)' pkg/process/peers.go` 仍在 `svc.start` 之前（240 不被回退）
- [ ] step 3 与 step 5 的负对照都已观察到红，且已复原
- [ ] `git status --porcelain` 只列出 In scope 的文件，外加 `plans/README.md`
- [ ] `plans/README.md` 的 253 行已更新

## STOP conditions

- 某个现有测试依赖「未监听也能拿到视图」，例如断言登记失败时 `peer_list` 返回空列表：报告该测试与它的理由，不要改它的断言。
- `svc.addr` 在某条**已登记**路径上也可能是空串，比如某处先清 `addr` 再写 record：报告具体位置。这说明「`addr != ""` ⟺ 在监听」的前提不成立，判据需要换成别的字段。
- step 4 里 `Send` 的第一个失败点不是 `view`（参数校验之外的别的东西先报错）：报告调用链，不要改 `Send`。

## Maintenance notes

- 要守住的不变量：**`view` 只在本进程正监听某个 socket 时给出视图**。新的、要「以本进程身份」对外说话的入口一律经过 `view`；只想知道「树里有谁」的入口，在 `view` 报错时走目录兜底。
- 240 的「`env.peers` 在 tick 循环运行期间一直非空」和本计划的「未监听即不可用」是两件事，评审时别把它们合成一个判据：前者服务于收信与放行，后者服务于对外发言与列名单。
- 模式切换失败（`switchModeLocked` 拆掉旧 socket 后新的没起来）现在也答 unavailable，下一拍重试成功后自动恢复。这是有意的：没有地址就没法签名发信。
