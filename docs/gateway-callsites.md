# Gateway 待删代码调用点

生成依据：`rg` 与 `go list ./...`，2026-08-30。

| 文件 | 符号 | 调用点 | 迁移目标 |
| --- | --- | --- | --- |
| `server.go` | `DeliverChannelOutbound` | `channelBus`、channel handlers | `channel.Service` |
| `server.go` | `PublishInbound` | `channel` inbound callback | `turn.Service.Submit` |
| `server.go` | `AttachREST` | `cmd/forebrain/serve.go` | 保留 transport |
| `server.go` | `HandleChatWS` | `AttachREST` 路由 | `turn.Service` + WS projector |
| `post_turn_runtime.go` | `ShouldRunBackgroundBookkeeping` | `finishSuccessfulTurn` | `turn.Service` |
| `post_turn_runtime.go` | `finishSuccessfulTurn` | `HandleChatWS` | `turn.Service.Submit` |
| `post_turn_runtime.go` | `appendTranscriptTurns` | `finishSuccessfulTurn` | `turn.Service` |
| `post_turn_runtime.go` | `persistCancelledGatewayTurn` | `HandleChatWS` cancel paths | `turn.Service` |
| `supervised_run.go` | `runAgentWithSupervisor` | `HandleChatWS` | `run.Controller` |
| `auto_compact.go` | `assembleContextSnapshotForUserInput` | `autoCompactBeforeUserAppend` | `turn.CompactionService` |
| `auto_compact.go` | `autoCompactBeforeUserAppend` | `HandleChatWS` | `turn.CompactionService` |
| `auto_compact.go` | `compactService` | compact preflight and slash | `turn.CompactionService` |
| `auto_compact.go` | `appendPreflightCompactStep` | `HandleChatWS` | `turn.CompactionService` |
| `approval_ws.go` | approval wire projection | `ws_run_events.go` | `turn.ApprovalService` projector |
| `network_approval.go` | approval decision flow | WS and HTTP action routes | `turn.ApprovalService.Decide` |
| `run_input.go` | active input map | `HandleChatWS`, queued-input routes | `run.Controller` |
| `run_cancel.go` | cancel map | cancel routes and `run.Run` | `run.Controller.Cancel` |
| `slash_handlers.go` | eight `Handle*Slash` methods | `turn.Execute` through `turn.Context` | `turn.CommandService` |
| `channels_bind.go` | `BindChannels` | `serve_run.go`, agent switch | `channel.Service` |

`AttachREST` 与 `HandleChatWS` 是最终保留的 HTTP/WS transport 边界；其余条目须在相应迁移阶段删除。
