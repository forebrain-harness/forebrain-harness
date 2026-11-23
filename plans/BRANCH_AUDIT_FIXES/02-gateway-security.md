# 02 gateway 安全与健壮性：/resume 归属复查、权限写入归属校验、请求体上限

来源发现：P-1、P-2（pre-existing，但都落在本分支触碰的文件里，且与"多租户同进程是文档化生产形态"直接相关）、I-6（introduced）。
涉及包：`pkg/gateway`、`pkg/turn`（B1 的第二道防线一处）。

## 前置检查（漂移核对）

1. `pkg/gateway/server.go`：WS 消息循环先对**切换前**的 sid 做 `s.Sessions.Ensure(r.Context(), sid, sid)`，拒绝则 `turn_withdrawn` + `continue`（约 1406-1415 行）；斜杠命令返回 `SessionSwitched` 后 `sid = strings.TrimSpace(sc.SessionID)` 直接 `bindRunEvents(sid)`，无任何归属复查（约 1434-1449 行）。
2. `pkg/turn/executor.go` 的 `chooseSession`（约 640-657 行）对任意 id 原样返回 `SessionSwitched/SessionID`，无 `Ensure`；`ctx.Sessions` 在该方法可用（642 行判空）。
3. `pkg/gateway/wsevents.go` 的 `runEventSubscription.deliver` 只按绑定 sid 过滤（约 742-750 行）——绑定即泄露面。
4. `pkg/gateway/api_extra.go`：`handleSessionPreset` 接受任意 `session_id`，对 `runnerFor` 的 runner 直接 `preset.SessionUpdates(sid)`，无 `sessionOwned`（约 4660-4692 行）；`handlePermissionUpdate` 的 `DestinationSession` 分支同样缺（约 708-719 行）。既有范式：`s.sessionOwned(r.Context(), sid)` 返回 `(owned, err)`，全文件 12+ 处在用（含 `handleHeartbeat:3783`）。
5. `pkg/gateway/api_extra.go:3570`：`/cron-settings` PUT 用 `json.NewDecoder(r.Body)` 无上限；同类写端点用 `http.MaxBytesReader`（如 4669 行，4096）。

## B1 [SECURITY] /resume 切会话前复查归属，拒绝时不绑定事件流（P-1）

**影响回顾**：已认证租户 A 经 WS 发 `/resume` 携带租户 B 的会话 id，即可订阅 B 会话的实时事件（提示、回答、工具调用）直至断连；后续消息虽被 Ensure 拒，但拒绝路径不解绑、不重绑，泄露窗口持续。

**改动**（两处，防线在边界、语义在引擎）：

1. `pkg/gateway/server.go` 的 `SessionSwitched` 分支（1434-1436）：切换前先复查，失败不切换、不绑定、按既有拒绝语义回 `turn_withdrawn`（理由句与 1409-1411 行一致 `"Session belongs to another primary agent"`）：
```go
if sc.SessionSwitched && strings.TrimSpace(sc.SessionID) != "" {
    newSid := strings.TrimSpace(sc.SessionID)
    if s.Sessions != nil {
        if _, err := s.Sessions.Ensure(r.Context(), newSid, newSid); err != nil {
            reason := err.Error()
            if errors.Is(err, state.ErrSessionNotOwned) {
                reason = "Session belongs to another primary agent"
            }
            writeMsg(wsServerMsg{Op: "turn_withdrawn", RequestID: m.RequestID, SessionID: sid, Error: reason})
            continue
        }
    }
    sid = newSid
}
```
2. `pkg/turn/executor.go` 的 `chooseSession`：第二道防线（TUI 单租户下行为不变——自己的会话 Ensure 必过）——`ctx.Sessions != nil` 时先 `Ensure`（ctx 需要一个 `context.Context`，用该方法已有的 ctx 来源；核对 `turn.Context` 的嵌入字段），`errors.Is(err, state.ErrSessionNotOwned)` 时返回 `Result{Handled: true, Reply: "That conversation belongs to another primary agent."}`，不切换。

**缓存影响**：无（不触碰提示组装）。

**测试**：
- `pkg/gateway`（`session_context_test.go` / `server_test.go` 现成的双租户夹具）：A 的 WS `/resume` 到 B 的会话 → 收到 `turn_withdrawn`；随后 B 会话产生运行事件，A 的 socket 收不到任何一条；A 切回自己的会话后事件恢复。
- `pkg/turn`（`slash_test.go`）：`chooseSession` 对不属于本 agent 的 id 返回拒绝 Reply 且不置 `SessionSwitched`；对自有 id 行为不变。

## B2 [SECURITY] 会话作用域权限写入先校验会话归属（P-2）

**影响回顾**：租户 A 可对 B 的会话 id 下发 YOLO/全访问 preset 或权限规则；B 会话下一次工具调用按同一进程内权限库判定时跳过审批。本分支已删掉旧的全局 `ApplyToConfig` 改写（收窄了爆炸半径），残留缺口就是这一次校验。

**改动**（`pkg/gateway/api_extra.go`，两处，完全照 `handleHeartbeat:3783` 的既有写法）：

1. `handleSessionPreset`：`sid` 非空校验之后、`runnerFor` 之前：
```go
owned, err := s.sessionOwned(r.Context(), sid)
if err != nil || !owned {
    http.Error(w, "session not found", http.StatusNotFound)
    return
}
```
（错误文案与状态码逐字对齐 3783 处范式，以现场代码为准。）
2. `handlePermissionUpdate` 的 `case safety.DestinationSession:` 分支：取 runner 前同样补这两行。`DestinationLocalSettings` 保持现状（agent 级本就全租户共享，是既定语义）。

**缓存影响**：无。

**测试**（`pkg/gateway/api_extra_test.go`）：preset 与权限规则各两条——他人会话 → 404 且权限库无写入；自有会话 → 现行为不变（沿用既有用例，补断言状态码）。

## B3 [SECURITY/PERF] /cron-settings PUT 限制请求体（I-6）

**改动**（`pkg/gateway/api_extra.go:3570`，一行）：
```go
if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
```
载荷只是一个 `*int`，4096 与 `handleSessionPreset` 的既有约定一致。

**缓存影响**：无。

**测试**（`api_extra_test.go`）：超长 body → 4xx（MaxBytesReader 语义），正常小 body → 现行为不变。

## 验证（本计划全部完成后）

```bash
gofmt -l pkg cmd
go vet -tags fts5 ./...
CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ ./pkg/turn/ -count=1
```

安全项自查：B1/B2 的拒绝路径都不落库、不写对方租户的任何行（`turn_withdrawn` 是"op 不记入会话日志"的既有语义，照旧）。
