# Plan: gateway 显示 host 映射去重 + probe 超时文案同源（GATEWAY_DISPLAY_HOST_DEDUP）

## Status

- **Priority**: P3（审计 #2 + #5）
- **Effort**: S
- **Risk**: LOW（同包内函数合并；输出文案逐字不变）
- **Depends on**: GATEWAY_STATUS_FRIENDLY_OUTPUT 已实施（工作区未提交状态即该
  基线；`pkg/gateway/http_server_test.go` 已有其测试）
- **Category**: tech debt（重复实现漂移风险）
- **标签**: pre-existing（GATEWAY_STATUS_FRIENDLY_OUTPUT 引入）

## 问题（file:line 证据）

同一「unspecified host → 127.0.0.1」映射在 `pkg/gateway`（同包）两处独立实现：

1. `pkg/gateway/http_server.go` `displayBaseURL`（约 :636-645，新增）：
   ```go
   host, port, err := net.SplitHostPort(addr)
   ...
   if host == "" || host == "0.0.0.0" || host == "::" {
       host = "127.0.0.1"
   }
   return "http://" + net.JoinHostPort(host, port)
   ```
2. `pkg/gateway/serve_run.go` `displayHost`（约 :230-237，该批次刚把签名从
   `net.Addr` 改为 `string`）：
   ```go
   func displayHost(addr string) string {
       host, port, err := net.SplitHostPort(addr)
       ...同款三分支映射...
   }
   ```

启动 banner 用 (2)，status/stop 输出用 (1)。两处漂移后同一 gateway 在两个
出口显示不同地址。

附带（审计 #5）：`http_server.go` `reachOutcome` 的超时文案
`"no response within 3s"`（约 :629）与两个 probe client 的
`&http.Client{Timeout: 3 * time.Second}`（:664、:710）是三处独立硬编码——
超时值调整后文案撒谎。

## Fix design

1. **单一映射源**：保留 `displayHost(addr string) string` 为唯一实现（放
   `serve_run.go` 原位，banner 与 status 共用；同包无需搬文件）。
   `displayBaseURL` 收敛为：
   ```go
   func displayBaseURL(addr string) string {
       return "http://" + displayHost(addr)
   }
   ```
   删除 `displayBaseURL` 内的 SplitHostPort/映射逻辑。

2. **超时常量同源**：`http_server.go` 顶部（或 reachOutcome 旁）：
   ```go
   // gatewayProbeTimeout bounds the status/stop local probes; the outcome
   // text derives from it so the sentence never disagrees with the clock.
   const gatewayProbeTimeout = 3 * time.Second
   ```
   :664 与 :710 两处 client 改用常量；`reachOutcome` 超时分支改为：
   ```go
   return false, fmt.Sprintf("no response within %ds", int(gatewayProbeTimeout.Seconds()))
   ```

## Steps

1. 测试先行：`pkg/gateway/http_server_test.go`（GATEWAY_STATUS 友好化批次已带
   reachOutcome/displayBaseURL 的表测——在其中）：
   - displayBaseURL 表测补齐与 displayHost 的一致性样例（`0.0.0.0:6060`、
     `:6060`、`[::]:6060`、`127.0.0.1:6060`、无端口非法串）——若已有则确认
     覆盖，缺则补行。
   - reachOutcome 超时分支断言改为派生文案（用同一常量构造期望串），锁定
     「文案跟随时钟」。
2. 实现合并 + 常量替换。
3. 回归：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` ok；
   gofmt/vet 干净。

## Verification

- 既有 GATEWAY_STATUS 表测全绿（输出逐字不变——合并是纯去重，golden 兜底）。
- `grep -n "0.0.0.0" pkg/gateway/*.go`（排除测试）仅 displayHost 一处命中。

## Out of scope

- banner（writeStartupBanner）输出格式本身——该批次已验收，不动。

## STOP conditions

- 无（纯去重，golden 全覆盖）。
