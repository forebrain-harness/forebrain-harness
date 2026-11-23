# Plan 6: 根治 refusedAddr 端口竞态，让 gateway 探针测试在并行全量测试下稳定

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**:
> `git diff --stat 6667bb5 -- pkg/gateway/http_server_test.go`
> 注意：本计划写于一个**有未提交改动的工作区**（commit 6667bb5 + dirty tree）。
> 以"Current state" excerpts 对照磁盘现状为准，而不是 SHA diff。若 excerpts
> 与磁盘不符，视为 STOP 条件。

## Status

- **Priority**: P2（测试稳定性；不影响生产代码）
- **Effort**: S
- **Risk**: LOW（只动一个测试辅助函数；生产代码零改动）
- **Depends on**: none
- **Category**: tests / bug（flaky test 根因修复）
- **Planned at**: commit `6667bb5` + 工作区 dirty（2026-10-08，工作树 diff 审计发现 #1）

## Why this matters

`pkg/gateway/http_server_test.go` 的 `refusedAddr` 辅助函数先绑 `127.0.0.1:0`
让 OS 分配一个临时端口，然后立刻 `Close` 并把地址返回给测试用——**假定**该端
口此后仍然无人监听。这个假定在 `make test`（= `go test ./...`，各包测试二进制
并行运行、httptest 大量申请临时端口）下是概率性错误的：Close 之后、探针拨号
之前，任何并发测试进程都可能拿到同一端口并监听。此时 `GatewayHealthText` 的
探针不再收到 ECONNREFUSED，`reachOutcome` 走进别的分支，测试的逐字输出断言失
败。已实际复现：四包并行（`./pkg/turn ./pkg/gateway ./pkg/run ./pkg/state`）
一次红（gateway FAIL）、复跑绿；单包跑稳定绿。CI/本地全量测试的偶发红会持续
侵蚀对测试套的信任。

修复后：`refusedAddr` 返回的地址**经过拨号验证确属"连接被拒"**，被抢走的候
选端口在辅助函数内部就被发现并换下一个，flake 机制被消除。

## Current state

- `pkg/gateway/http_server_test.go` — gateway 状态/停止探针的测试。相关事实：
  - `refusedAddr(t)`（:236-249）是唯一改动点，当前实现：

    ```go
    // refusedAddr returns a loopback address that is guaranteed to have nothing
    // listening on it.
    func refusedAddr(t *testing.T) string {
    	t.Helper()
    	ln, err := net.Listen("tcp", "127.0.0.1:0")
    	if err != nil {
    		t.Fatalf("open probe port: %v", err)
    	}
    	addr := ln.Addr().String()
    	if err := ln.Close(); err != nil {
    		t.Fatalf("close probe port: %v", err)
    	}
    	return addr
    }
    ```

    注释承诺 "guaranteed"，实现只是"close 后祈祷"。这是根因：**测试对环境
    前置条件（无人监听）只做了假定，没有做验证**。

  - 消费方只有两个，均在同文件，行为断言为逐字输出匹配：
    - `TestGatewayHealthTextNotRunning`（:251-271）：断言输出含
      `"Reach      nothing is listening on <addr>"`（探针必须收到 ECONNREFUSED）。
    - `TestGatewayShutdownRequestNotRunning`（:321-331+）：同构断言。
  - 被测生产代码（**不改动，仅供理解**）：`pkg/gateway/http_server.go` 的
    `reachOutcome`（:630+）把 ECONNREFUSED 分类为 refused、把能连上分类为可达；
    `GatewayHealthText`/`GatewayShutdownRequest`（:659+/:705+）按分类输出文案。
    端口被抢时探针收到非 refused 结果 → 输出与断言不符。
  - 仓库内没有其它 `ECONNREFUSED` 测试辅助可复用（已 grep 确认）；此辅助是
    本文件私有，改名/改签名无外溢。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 编译+静态检查 | `go vet ./pkg/gateway` | 无输出，exit 0 |
| 单包测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | `ok  github.com/forebrain-harness/forebrain-harness/pkg/gateway` |
| 并行复现拓扑 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway ./pkg/run ./pkg/state -count=1` | 四包全 `ok` |
| 格式 | `gofmt -l pkg/gateway` | 无输出 |

## Scope

**In scope（只允许改这些）**：
- `pkg/gateway/http_server_test.go` — 仅 `refusedAddr` 函数体与其注释；如需
  `errors`、`syscall`、`time` 导入则补导入。

**Out of scope（看起来相关也不许碰）**：
- `pkg/gateway/http_server.go` 的 `reachOutcome`/`GatewayHealthText`/
  `GatewayShutdownRequest` — 生产代码语义正确，竞态在测试侧。
- 两个 NotRunning 测试的断言文案 — 断言本身是对的。
- 其它包的任何文件。

## Git workflow

- 改动留在工作区，**禁止 git commit**（owner 手动逐个审阅提交，仓库纪律）。
- 不建分支、不 push、不开 PR。

## Steps

### Step 1: 重写 refusedAddr 为"验证后返回"

把 `refusedAddr` 替换为下方形态（目标代码形状；文字可微调，结构不得简化）：

```go
// refusedAddr returns a loopback address verified to refuse connections.
// Binding :0 and closing only *hopes* the port stays free: under the full
// parallel test run (make test), sibling package binaries churn ephemeral
// ports and can grab the candidate between the close and the probe, turning
// the "not running" fixture into a flake. So each candidate is verified by
// dialing it: only an address whose dial fails with ECONNREFUSED is returned.
func refusedAddr(t *testing.T) string {
	t.Helper()
	const attempts = 8
	for i := 0; i < attempts; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("open probe port: %v", err)
		}
		addr := ln.Addr().String()
		if err := ln.Close(); err != nil {
			t.Fatalf("close probe port: %v", err)
		}
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			// Another process grabbed the port between close and dial.
			_ = conn.Close()
			continue
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			return addr
		}
		// Unexpected dial failure (e.g. timeout): try a fresh candidate.
	}
	t.Fatalf("no loopback candidate refused a dial in %d attempts", attempts)
	return ""
}
```

注意：
- `t.Fatalf` 在返回 `""` 前（Go 惯例 unreachable，编译器需要返回值）。
- 若 `errors`/`syscall`/`time` 尚未在该测试文件导入，补导入并 `gofmt`。
- 不要为"dial 成功/超时"分支写日志或计数——那是测试脚手架噪声；`continue`
  静默换候选即可。

**Verify**: `gofmt -l pkg/gateway` → 无输出；`go vet ./pkg/gateway` → 无输出。

### Step 2: 单包与并行拓扑验证

**Verify**:
1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestGatewayHealthTextNotRunning|TestGatewayShutdownRequestNotRunning' -count=1 -v` → 两条 PASS。
2. `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → `ok`。
3. 并行复现拓扑（修复前的失败形态）连跑 3 次：
   `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/gateway ./pkg/run ./pkg/state -count=1`
   → 每次四包全 `ok`。
4. `git status --porcelain -- pkg/gateway` → 仅 `http_server_test.go` 一个 M，
   且 `git diff -- pkg/gateway/http_server_test.go` 只含 refusedAddr 及导入变更。

## Test plan

- 不新增测试文件/用例：被修的竞态在时序上不可确定性注入（无法强制内核把某
  端口发给并发进程），验证方式就是 Step 2 的并行拓扑重复运行——这正是原始
  复现路径。
- 既有两个 NotRunning 测试就是本修复的行为回归锚点，保持原断言不动。

## Done criteria

- [ ] `refusedAddr` 实现含"拨号验证 ECONNREFUSED 才返回 + 换候选重试"结构
      （`grep -n 'ECONNREFUSED' pkg/gateway/http_server_test.go` 命中辅助函数内）
- [ ] `go vet ./pkg/gateway` exit 0；`gofmt -l pkg/gateway` 无输出
- [ ] Step 2 的 1/2/3 全部通过（并行拓扑 ≥3 连绿）
- [ ] `git status` 显示 pkg/gateway 下仅 `http_server_test.go` 被修改
- [ ] `plans/README.md` 状态行更新

## STOP conditions

- `Current state` excerpts 与磁盘不符（工作区已漂移）。
- Step 2 任一验证在合理修复尝试后仍失败两次。
- 发现 `refusedAddr` 有本计划未列出的第三个消费方（说明影响面比计划大）。
- 并行拓扑仍偶发红且失败用例**不是**两个 NotRunning 之一（那是另一个 flake，
  不在本计划范围内，勿顺手修）。

## Maintenance notes

- 验证后到探针拨号之间仍存在一个亚毫秒级理论窗口（验证 dial 之后、
  `GatewayHealthText` dial 之前端口再被抢）。与修复前"无界窗口"相比已收缩数
  个量级且失败模式变为极小概率事件；若未来仍见本 flake，再议"低特权固定端
  口"方案（127.0.0.1:1 之类），那依赖"本机无 inetd"的环境假定，本期不引入。
- 评审重点：确认没有顺手改两个 NotRunning 测试的断言文案；确认生产代码
  `http_server.go` 零 diff。
