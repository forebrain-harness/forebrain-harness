# Sandbox 与工具审批工作交接

更新时间：2026-08-11（Asia/Shanghai）

当前 Forebrain Harness 提交：`161ba440`

语义基准源码：`/Users/doudou/workspace/unionj-cloud/codex/codex-rs`，提交 `2e3a1702c2`

当前状态：实现与自动化验收已完成，工作树未提交且包含大量修改。禁止在未审阅差异前执行提交、重置或覆盖操作。

> 2026-09-07 补充：subagent live/resume 修复进一步把 approval action 与 run continuation 解耦并持久化。`pending/approved/denied/cancelled/answered/expired/error` 是 action 状态；run wait 通过 durable owner lease、claim phase 和 execution fence 恢复，不能再用删除 wait 表示“由内存接管”。session grant 绑定用户可见 conversation session，worker session 只隔离子 agent 历史。Gateway 在事件订阅接线后负责 TTL sweep，并将 `expired` 写成 `approval_resolved` 后终止无法继续的 parked run；历史审批只读，只有服务端确认仍 pending 的 action 可提交决定。协议、实现证据和剩余人工门禁见 `SUBAGENT_LIVE_RESUME_REPAIR_PLAN.md` 第 11 节。

完成记录（2026-08-11）：原文第 4、5 节记录的是中断时的待办快照，现已全部完成实现，包括 HTTP/Web/TUI 服务端审批决策、Windows 后端、原生 token-prefix 策略、typed payload、平台 filesystem profile、managed proxy 及旧语义清理。全仓 Go test、race、vet、前端 test/build、Windows/macOS 交叉编译和 Linux bwrap live release gate 均通过。受当前 Linux 主机限制，macOS Seatbelt 与 Windows restricted-token 后端只完成了单元测试和交叉编译，仍应在对应原生主机上执行 live release gate。

## 1. 目标与不可变约束

本次工作的目标是让 Forebrain Harness 的 sandbox、工具审批、相关配置项及默认值具备与基准源码相同的行为。

必须持续遵守以下约束：

- Forebrain Harness 只暴露已有的 `--yolo`。不要增加 `--dangerously-bypass-approvals-and-sandbox`。
- `--yolo` 的效果是审批策略 `never` 加 `danger-full-access`。
- 删除过时、冲突或无法复用的旧行为、旧代码和旧配置；不做兼容、不做回退，也不校验已经删除的旧配置键。
- 生产代码、注释和测试名称中不要加入描述迁移来源的 `port`、`mirror`、`align codex` 等字样。
- 所有配置默认值都属于验收范围。
- 不得覆盖用户已有改动，尤其是脏子模块 `forebrain-harness.github.io`。
- 未收到明确要求时不要执行 `git add`、`git commit` 或发布操作。

## 2. 原始问题与根因

原始现象：`sandbox.enabled = true` 时，工具执行被 sandbox 阻止后，Forebrain Harness TUI 从不发起审批；截图中的基准实现会展示“单次批准”和“批准并记住命令”等选项。

已定位的根因是 sandbox 拒绝识别器过窄，而不是简单的 UI 缺失：

- 旧实现只识别少量固定文本或单一输出通道。
- 某些平台拒绝信息可能出现在 stdout、stderr、聚合输出或进程信号中。
- 拒绝没有被识别为 sandbox denial 后，执行链不会进入对应的审批分支。

需要保留的审批策略细节：

- `unless-trusted`：普通 shell 在 sandbox denial 后可请求无 sandbox 重试。
- `never`：不得请求审批。
- `on-request`：普通 shell 不因运行后的 sandbox denial 自动请求重试；模型显式请求 `require_escalated` 时才可请求。网络审批是独立例外。
- `granular`：由 `sandbox_approval`、`rules`、`request_permissions`、`mcp_elicitations` 等开关分别控制。
- `apply_patch` 有独立的审批路径，允许在 `on-request` 下按解析出的文件集合请求批准。

## 3. 已完成的主要工作

### 3.1 配置和默认值

- 默认审批策略为 `on-request`。
- 默认审批 reviewer 为 `user`。
- 默认 sandbox mode 为 `read-only`。
- `sandbox_workspace_write` 默认：
  - `writable_roots = []`
  - `network_access = false`
  - `exclude_tmpdir_env_var = false`
  - `exclude_slash_tmp = false`
- `features.exec_permission_approvals = false`。
- `features.request_permissions_tool = false`。
- `features.network_proxy = false`。
- Windows sandbox level 未配置时为 disabled 语义；私有桌面默认启用。
- `approval_policy = on-failure` 仍作为基准源码当前存在的别名处理为 `on-request`，不是 Forebrain Harness 旧配置回退。
- 已删除旧的嵌套 sandbox 配置结构及相应校验路径。

### 3.2 `--yolo`

- CLI 仅使用 `--yolo`。
- 生效后强制审批策略为 `never`、sandbox 为 `danger-full-access`。
- TUI、Gateway 和 doctor/runtime warning 已补充相应状态或警告。
- 硬性 deny 规则仍优先于 YOLO；测试中的 deny 必须来自全局策略来源或带正确 session id 的会话来源。

### 3.3 Sandbox denial 识别与 shell 重试

- 拒绝识别已覆盖 stdout、stderr、聚合输出和 `SIGSYS`。
- 文本 marker 集合与顺序已按基准实现核对。
- 普通 shell、网络拒绝和显式 `require_escalated` 的审批分支已拆分。
- 默认 shell timeout 已改为 10 秒。
- 已阻止 deny-read 限制被无 sandbox 重试绕过。

### 3.4 `apply_patch`

- 删除旧兼容实现，新增严格 patch parser。
- 在执行前解析并规范化全部受影响路径。
- 审批按完整文件集合进行，不允许只批准其中一部分后扩大到其他路径。
- “本会话批准”按 session 隔离，并携带 sandbox bypass 能力。
- shell 中的 `apply_patch` heredoc 被路由到专用审批逻辑，避免先批准不透明 shell 文本。

### 3.5 `request_permissions`

- 已实现结构化 filesystem/network permission profile。
- 支持 path、glob pattern 和 special path。
- 支持 turn/session scope、strict auto review、请求与响应的交集约束。
- 批准响应不能超过工具原始请求范围。
- session grant 和 turn grant 已接入 shell、文件工具与 sandbox runtime。

### 3.6 会话隔离

此前同一个 Runner 服务多个会话时，`SourceSession` 规则和 grant 会跨会话泄漏。现已完成：

- `PermissionRuleValue`、filesystem grant、network grant 均携带 session id。
- 新增 `SnapshotForSession`、`ModeForSession`、`EvaluateForSession`、`ExplainForSession`。
- middleware、Gateway、TUI、slash command、文件工具和 shell 均改为使用当前 agent session id。
- 全局 snapshot 不再包含 session 规则或 grant，避免刷新进程级 runtime config 时泄漏。
- 已加入“本会话允许、其他会话和全局不匹配”的测试。

### 3.7 审批入口加固

新增 `internal/codetools/approval_update.go`：

- 所有从 UI/Gateway 提交的记忆型审批必须被限制在 pending action 表示的能力范围内。
- 拒绝扩大命令前缀、增加文件路径、遗漏 patch 文件、错误 destination 或错误 bypass 标志。
- `apply_patch` 只能记忆到当前 session。
- MCP/file 的 session 规则只能精确匹配当前 action。
- 命令持久批准只能保存服务端提出的 amendment：能推导出前缀时保存 `command_prefix`，
  否则保存该条精确命令（`rule_content`，字符串相等匹配），两者都带 `bypass_sandbox`。
  精确规则必须是解析器判定的 exact 形态，含通配符的命令一律不提供该选项。
- 只有当已匹配的规则是 deny/ask 时才撤下持久选项；匹配到 allow 规则说明这是沙箱
  升级提示，撤下选项会让同一提示在每次调用时重复出现且无从记忆。
- HTTP 和 WebSocket 审批入口都调用服务端验证，不信任客户端构造的任意 `PermissionUpdate`。

### 3.8 网络策略

- 已实现托管 HTTP、HTTPS CONNECT、SOCKS5 TCP/UDP 路径。
- 支持 session allow/deny 和持久 network policy amendment。
- 主机名解析后再次阻止 private/local 地址，防止 DNS rebind 绕过。
- 已接入 Linux network bridge、证书和代理环境处理。
- `features.network_proxy` 只限制已经获得网络能力的 sandbox，不会自行开启 sandbox 网络。

### 3.9 平台 sandbox

- macOS Seatbelt policy 改为 closed-by-default，并补充集成 release gate 测试。
- Linux bwrap 使用 tmpfs 根文件系统，workspace-write 工作目录可写，deny-read pattern 有平台处理。
- Windows 默认 disabled 行为已接入配置，但真正的 Windows 受限执行后端尚未完成，见未完成项。

### 3.10 已删除的旧目录授权语义

最近已删除以下旧路径：

- `PermissionUpdate` 的 `addDirectories`、`removeDirectories`。
- permission store/snapshot 的 `Directories`。
- permissions 持久化文件中的 directories。
- HTTP permission rules 返回中的 directories。
- slash/status 中的 directory 计数。
- 将通用 `Read`/`Edit` 规则隐式转换成进程级 sandbox allow/deny roots 的逻辑。

文件系统能力现在只能来自显式配置 profile 或结构化 runtime grant，避免旧规则扩大所有会话的 sandbox。

### 3.11 命令 amendment 收窄

旧实现会把 `npm run build` 收缩成 `npm run:*`，把 `go test ./...` 收缩成 `go test:*`，授权范围过宽。现已改为：

- 自动建议完整、经过 shell quoting 的 token 前缀。
- token-prefix 匹配支持任意 token 数，不再依赖两段式命令白名单。
- 模型显式提供的 prefix 仍可使用，但会拒绝基准实现禁止的宽前缀。
- 禁止的宽前缀会回落到当前完整命令，不会回落到更宽规则。
- sandbox denial 重试 action 会保留模型提供的 `prefix_rule`。
- 持久命令批准携带 sandbox bypass；不同命令不能因共享子命令而被误放行。

## 4. 中断时正在进行的工作

### 4.1 HTTP 审批 decision API

`internal/gateway/api_extra.go` 已增加以下请求字段并可编译：

- `decision`
- `execpolicy_amendment`
- `network_policy_amendment`
- `request_permissions_response`

HTTP 路径开始复用 WebSocket 的服务端 decision 处理，包括：

- `accept`
- `accept_for_session`
- `accept_and_remember`
- `accept_with_execpolicy_amendment`
- `apply_network_policy_amendment`
- request_permissions 的 turn/strict/session grant

当前仅确认 `internal/gateway` 编译通过，尚未补齐 HTTP decision 的覆盖测试。还应检查并发批准时“先应用记忆、后更新 action 状态”的副作用边界。

### 4.2 Web 前端尚未改完

这是当前最直接的未完成点。

前端仍使用旧的“exact/prefix + destination selector”模型：

- `frontend/src/lib/approvalSuggestions.ts`
- `frontend/src/views/ChatView.vue`
- `frontend/src/lib/api.ts`
- `frontend/src/lib/approvalSuggestions.test.ts`

后端已经拒绝部分旧组合，因此当前 Web UI 的某些按钮会返回 400。应改为消费服务端 `available_decisions` 和 proposed amendment：

- 普通命令：`accept`、`accept_with_execpolicy_amendment`（可推导出前缀时）或
  `accept_and_remember`（推导不出前缀时，记忆该条精确命令）、`cancel`。两者互斥。
- `apply_patch`：`accept`、`accept_for_session`、`cancel`。
- 文件工具（`Read`/`Write`/`Edit`/`MultiEdit`）：`accept`、`accept_for_session`、`cancel`。
- `WebFetch`：`accept`、`accept_and_remember`（记忆 `domain:<host>`）、`cancel`。
- MCP：`accept`、可选 session/persistent remember、`cancel`。
- network：`accept`、`accept_for_session`、network policy amendment、`cancel`。
- `request_permissions`：turn、strict turn、session、decline。

不要继续让前端自行选择任意 destination 或自行拼接规则。

## 5. 仍需完成的高优先级事项

### P0：完成前端审批决策

1. 扩展 `PermissionSuggestionRecord`：
   - `availableDecisions`
   - `proposedExecpolicyAmendment`
   - `proposedNetworkPolicyAmendments`
   - `networkApprovalContext`
   - `networkPort`
   - `oneShotOnly`
2. 扩展 `forebrainApi.actionsApprove` 请求类型，发送 decision/amendment/request_permissions response。
3. 删除 ChatView 的 exact/prefix 通用按钮和任意 destination selector。
4. 按 action 提供的 decisions 渲染按钮。
5. 更新中英文文案和 Vitest。
6. 为 HTTP decision endpoint 增加 Gateway 测试，至少覆盖 widened amendment 被拒绝、patch session 隔离和 request_permissions scope。

### P0：Windows sandbox 后端

当前 `internal/sandboxrt/dependencies.go` 对显式启用 Windows sandbox 返回 `windows_sandbox_unavailable`，项目中没有真正的 `backend_windows.go`。

需要继续实现或明确拆分：

- unelevated/elevated sandbox level。
- restricted user/token。
- filesystem ACL 和进程隔离。
- network restriction。
- private desktop 默认 true。
- proxy settings reconciliation。
- 对应 Windows 单元测试和交叉编译。

在 Windows 后端完成前，不能声称“完整 sandbox 语义已完成”。

### P1：命令策略持久化审计

当前 Forebrain Harness 仍使用 `state/permissions/*_settings.json` 保存通用 PermissionRule。基准实现使用 token 化 prefix rule，并有独立规则层级、冲突决策和 policy file 更新。

需要继续核对：

- 是否将持久命令规则改为原生 token 数组，而不是 `RuleContent` 的 `:*` 字符串编码。
- 多条规则、compound command 和 deny/prompt/allow 冲突的优先级。
- user/project/local 规则层级和默认规则文件路径。
- network rule 与 command rule 是否应使用同一持久策略机制。
- 禁止宽 prefix 的判断只应用于模型请求的 prefix；自动产生的完整命令 amendment 不应被错误过滤。

### P1：审批可用决策传播

`internal/gateway/approval_ws.go` 已生成 `available_decisions`，但 `internal/protocol/approval_events.go` 的 typed payload 尚未完整携带 decisions 和 proposed amendments。需要保证 Gateway WebSocket、HTTP action list、TUI 和 Web 前端看到同一组服务端决策。

### P1：平台验证

- 运行 macOS Seatbelt integration release gate。
- 在 Linux/Docker 环境运行 bwrap e2e。
- 执行 Windows 交叉编译。
- 检查 managed proxy 的 HTTP、CONNECT、SOCKS5 TCP/UDP 集成行为。

### P1：旧代码和命名清理

完成后执行全仓搜索，确认：

- 已删除的 sandbox 配置键不再出现。
- `addDirectories`/`removeDirectories` 不再出现。
- 旧的 exact/prefix 任意授权 UI 不再出现。
- 生产代码、注释和测试名中没有新增来源迁移措辞。
- CLI 只存在 `--yolo`，不存在另一个长 bypass flag。

## 6. 已执行的验证

最近一次成功的定向测试：

```bash
GOCACHE=/tmp/forebrain-go-cache go test ./internal/permissions ./internal/codetools ./internal/agentrun ./internal/gateway ./internal/clifacade ./internal/tui ./internal/protocol -run 'Permission|Approval|Prefix|Sandbox|YOLO' -count=1
```

结果：上述包全部通过。

最后一次 HTTP decision 修改后执行：

```bash
GOCACHE=/tmp/forebrain-go-cache go test ./internal/gateway -run '^$'
```

结果：编译通过，无测试运行。

交接文档生成前执行：

```bash
git diff --check
```

结果：通过。

尚未在最后一轮修改后完成：

- `go test ./... -count=1`
- macOS Seatbelt integration test
- Linux bwrap e2e
- Windows 交叉编译
- Frontend Vitest/typecheck/build

注意：此前在受限执行环境中运行完整 `internal/agentrun` 或 `internal/gateway` 测试时，个别 `httptest` 因不能绑定 localhost 失败；这类失败需要在允许本地监听的环境重跑，不能直接判定为代码失败。

## 7. 推荐接手顺序

1. 先运行 `git status --short` 和 `git diff --check`，确认没有新的中断改动。
2. 完成 Web 前端 decision UI 和 HTTP decision 测试。
3. 运行 permissions/codetools/agentrun/gateway/clifacade/tui/protocol 定向测试。
4. 审计命令策略持久化及 compound command 决策。
5. 实施 Windows sandbox 后端。
6. 运行 Darwin、Linux、Windows 平台验证。
7. 运行全量 Go 测试和前端测试。
8. 全仓搜索旧配置、旧 API、旧 UI 和禁止命名。
9. 最后人工 review 全部差异；除非用户明确要求，不提交。

## 8. 关键文件索引

配置与默认值：

- `internal/config/approval_policy.go`
- `internal/config/config.go`
- `internal/config/load.go`
- `internal/config/network_proxy.go`
- `internal/config/permission_profiles.go`
- `internal/config/sandbox_defaults_test.go`

权限和审批：

- `internal/permissions/types.go`
- `internal/permissions/store.go`
- `internal/permissions/engine.go`
- `internal/permissions/request_permissions.go`
- `internal/permissions/suggestions.go`
- `internal/permissions/command_prefix.go`
- `internal/permissions/shell_rule_matching.go`
- `internal/codetools/approval_update.go`
- `internal/codetools/request_permissions_tool.go`
- `internal/codetools/apply_patch.go`
- `internal/agentrun/tool_permission_middleware.go`

Gateway/TUI/Web：

- `internal/gateway/approval_ws.go`
- `internal/gateway/network_approval.go`
- `internal/gateway/api_extra.go`
- `internal/tui/approval_overlay.go`
- `internal/clifacade/chat_surface_approval.go`
- `frontend/src/lib/approvalSuggestions.ts`
- `frontend/src/lib/api.ts`
- `frontend/src/views/ChatView.vue`

Sandbox runtime：

- `internal/sandboxrt/manager.go`
- `internal/sandboxrt/violations.go`
- `internal/sandboxrt/backend_darwin.go`
- `internal/sandboxrt/seatbelt_policy_darwin.go`
- `internal/sandboxrt/backend_linux.go`
- `internal/sandboxrt/managed_network_proxy.go`
- `internal/sandboxrt/managed_network_policy.go`
- `internal/sandboxrt/dependencies.go`

## 9. 工作树注意事项

- 当前工作树有大量修改和新增文件，均未提交。
- `forebrain-harness.github.io` 显示为脏子模块；这是用户已有状态，必须保留。
- 不要用 `git reset --hard`、`git checkout --` 或批量覆盖恢复文件。
- 删除文件是本次清理的一部分，但应逐项 review，不要凭状态列表恢复。
- 当前没有创建 commit。
