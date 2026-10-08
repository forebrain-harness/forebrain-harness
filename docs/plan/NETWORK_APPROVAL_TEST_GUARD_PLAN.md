# Plan: Fix TestCharacterizationNetworkApproval failure (skip-guard gap)

> Copied verbatim from the approved plan
> (`~/.forebrain/workspace/plans/.../fix-network-approval-test-guard.md`), with the
> acceptance record filled in after implementation. 批准时点：main @ 639b624。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW (test-only change; no product code touched)
- **Depends on**: none
- **Category**: tests

## Why this matters

`TestCharacterizationNetworkApproval` is the only non-hermetic test in
`pkg/tui/chat_session_test.go`. Its own doc comment promises: "Skip cleanly
rather than fail somewhere deep in the sandbox/proxy stack on a host that
can't support either [a real OS sandbox **or genuine internet access**]".
The guard implements the first precondition (darwin + `sandbox-exec`) but not
the second. On any host whose test process cannot reach the internet — a
network-restricted agent sandbox, an offline laptop, a firewalled CI runner —
the test fails with a 5-second curl timeout and a confusing assertion message
instead of skipping. Observed for real: full `pkg/tui` suite run inside
forebrain's default shell sandbox failed exactly this way, blocking
verification of unrelated work.

## Root cause (evidence chain)

1. Failure observed (default sandbox run):
   `chat_session_test.go:7997: Messages[2].Content = "{\"exit_code\":28,\"stderr\":\"curl: (28) Connection timed out after 5002 milliseconds...\"}"`.
   Everything before the final curl **passed**: exactly 1 approved shell action,
   `approval_reason=network_denied`, host `example.com` recorded — the approval
   machinery works; only the approved outbound fetch timed out.
2. The approved fetch is performed by the managed proxy **inside the test
   process** (`pkg/safety/managed_network.go` — `http.Transport` with
   `Proxy: http.ProxyFromEnvironment`, dropped only when `AllowUpstreamProxy`
   is false; default is **true**, `pkg/config/network_proxy.go`). Curl inside
   the seatbelt sandbox only talks to the loopback proxy; the outbound dial is
   the test process's.
3. Environment proof at plan time (2026-10-08):
   - default sandbox: `curl -sS --max-time 5 https://example.com` → `curl: (28) Operation timed out`
   - unsandboxed host: same command → `network-ok`
   → host has internet; the failure is the execution environment's blocked egress.
4. Guard gap (the defect): the guard checks `runtime.GOOS == "darwin"` and
   `exec.LookPath("sandbox-exec")` only. The documented second precondition —
   test-process internet reachability — is never checked, so a host that
   cannot support it fails instead of skipping, contradicting the test's own
   contract.

Not the root cause: product code (`pkg/safety`, approval flow) — evidence
point 1 shows it behaved correctly; evidence point 3 shows connectivity was
the only missing input.

## Fix design

Add the missing precondition guard right after the `sandbox-exec` check:
probe `https://example.com` with a short deadline using the **same egress
policy the managed proxy uses** (`http.Client` default transport ⇒
`Proxy: http.ProxyFromEnvironment`, `AllowUpstreamProxy` default true). On
transport error → `t.Skipf` with the reason. Any completed HTTP response
(any status code) proves reachability → proceed.

Why an HTTP probe and not a raw `net.DialTimeout("tcp","example.com:443")`:
the proxy transport honors upstream env proxies by default. A raw dial would
falsely skip on hosts whose only egress is an HTTP(S)_PROXY. The HTTP probe
through `ProxyFromEnvironment` predicts exactly what the proxy's approved
fetch will do. Timeout 4s < the curl's `--max-time 5`, so probe failure
implies the in-test curl would time out anyway.

```go
// The managed proxy performs the approved fetch from this test process
// (http.Transport with ProxyFromEnvironment, AllowUpstreamProxy default
// true), so probe example.com with the same egress policy: if this
// process cannot reach it, the in-test curl cannot either, and the
// contract above says skip, not fail. 4s < curl's --max-time 5 so a
// probe failure predicts the timeout. Any completed response (any
// status code) proves reachability.
probe := &http.Client{Timeout: 4 * time.Second}
probeResp, err := probe.Get("https://example.com")
if err != nil {
    t.Skipf("skipping: no internet access from the test process (example.com: %v); the approved-fetch assertion needs genuine connectivity", err)
}
_ = probeResp.Body.Close()
```

Imports: add `"net/http"` to `chat_session_test.go`.

### Behavior matrix

| Scenario | Old | New |
|---|---|---|
| darwin + sandbox-exec + internet (owner host, CI with net) | full run, passes | unchanged (probe succeeds, full run) |
| darwin + sandbox-exec, **no test-process egress** (agent sandbox, offline, firewalled CI) | FAIL: 5s curl timeout, confusing message | SKIP with explicit reason |
| non-darwin / no sandbox-exec | skip | unchanged |

## Scope

**In scope**: `pkg/tui/chat_session_test.go` — one guard block + one import.

**Out of scope**: `pkg/safety/managed_network.go`, `pkg/config/network_proxy.go`
(behavior correct); the seatbelt guard and all test assertions; the startup-card
palette diff (separate commit); making the test hermetic (would defeat its
documented purpose).

## Maintenance notes

- Keep the probe target in sync with the curl target (`example.com`); if the
  test ever moves to another host, move both.
- The probe intentionally mirrors the proxy's egress policy
  (`ProxyFromEnvironment`); if `AllowUpstreamProxy`'s default ever flips to
  false, revisit then.

---

## 验收记录（实施后填写，2026-10-08）

### A. 场景 → 证据

| 场景 | 结果 | 证据 |
|---|---|---|
| Step 1 未沙箱单测（基线分类） | PASS（重试后） | 首跑 FAIL（`curl: (28) Connection timed out after 5002 milliseconds`，5.17s）→ 判别实验：DNS/TCP/TLS 拆解全通（3ms/197ms/416ms）、`http.Client` 连发 5 次 4 ok 1 timeout → 本机到 example.com 链路间歇丢包（~20%），首跑撞抖动窗口；同参重试 `--- PASS (0.87s)`。产品代码无缺陷，STOP 条件不触发 |
| 无出口环境单测（原故障场景） | **SKIP（新行为）** | 本会话 yolo 模式下 forebrain 默认沙箱不再断网（shell 直连 curl 即通），原环境不可直接复现；改用显式 deny-egress seatbelt profile（`(deny network*)` + loopback 白名单，语法与 `pkg/safety/backend_darwin.go` 同源）忠实复现「测试进程无出口」：`--- SKIP: TestCharacterizationNetworkApproval (0.00s)`，理由 `skipping: no internet access from the test process (example.com: Get "https://example.com": dial tcp: lookup example.com: no such host); the approved-fetch assertion needs genuine connectivity`，package `ok`。旧行为对照：同环境无守卫时 FAIL 5.17s + 迷惑断言消息 |
| 未沙箱单测（守卫存在时） | PASS | `--- PASS: TestCharacterizationNetworkApproval (2.92s)`（探测通过 → 完整运行，无 SKIP） |
| 默认沙箱整包回归 | ok | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui/ -count=1` → `ok 43.839s`（此前整包正是被该测试卡死） |
| build / vet | clean | `CGO_ENABLED=1 go build ./...` → BUILD-OK；`go vet ./...` → VET-OK |
| gofmt | clean | `gofmt -l pkg/tui/chat_session_test.go` → 无输出 |
| diff 边界 | 满足 | `git diff` 中本修复仅 import hunk（+`"net/http"`）+ 守卫 hunk（7924–7937 行 +13），同一文件的 startup-card 调色板测试 hunk 独立拆分提交 |

### B. 实施偏差

1. **Step 4 复现方式变更**：计划原设「默认沙箱跑出 SKIP」，但本会话为 yolo
   模式（每次 shell 调用 `approval_bypassed_by_yolo:true`），默认沙箱已不再
   阻断出口，无法直接复现计划时环境。改用显式 seatbelt deny-egress profile
   复现同一前置条件（测试进程无出口）——机制类相同（macOS seatbelt）、阻断
   条件相同，且 profile 语法与仓库 `pkg/safety/backend_darwin.go` 生成的
   过滤器同源。
2. **链路抖动发现（新事实，非本修复缺陷）**：本机到 example.com 的链路
   间歇丢包（单次 `http.Client` 4s 超时率 ~20%）。该抖动使「单次探测失败
   ⇒ curl 必超时」的推断在抖动机器上退化为概率性：探测失败仍会干净 SKIP，
   探测成功但 curl 撞抖动窗口时测试仍可能 FAIL——这是该非 hermetic 测试的
   既有固有属性（`docs/plan/STARTUP_CARD_COLOR_PLAN.md` 验收记录已把
   「4 连跑 1 败」列为本机已知基线），守卫将其中「稳定无出口」一类转化为
   SKIP，不在本期扩大为重试/hermetic 改造（计划 Out of scope 明示）。

## Implementation Status

**已完成并提交。** 守卫（`net/http` import + 13 行守卫块）落在
`pkg/tui/chat_session_test.go` 的 `TestCharacterizationNetworkApproval` 中
（`sandbox-exec` 检查之后、会话创建之前），与批准设计逐字一致。

- **验证**：见验收记录 A——无出口 seatbelt 复现 SKIP 0.00s（旧：FAIL
  5.17s）、未沙箱 PASS 2.92s、默认沙箱整包 `ok 43.839s`、build/vet/gofmt
  全 clean。
- **偏差**：见验收记录 B（Step 4 复现方式变更 + 链路抖动事实记录）。
- **未做**：计划 Out of scope 各项一行未碰；测试未改 hermetic（计划明示
  不做）。
