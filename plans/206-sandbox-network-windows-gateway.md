# 206 — 沙箱网络、Windows 与网关收口：删网络代理、Windows 固定受限令牌沙箱、对外地址自动识别、文件配置项再精简

- **基线**：`06f1bc57`（dev）。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / L / MED-HIGH（删约 3000 行沙箱网络代码；Windows 沙箱行为变化必须真机验收）
- **依赖**：205 先落地（同改 `pkg/config/config.go`、`load.go`、`pkg/tui/config_panel_schema.go`、`pkg/process/config_db.go`）。之后是 207。
- **执行隔离**：基于 dev HEAD 的 worktree 或主树 in-tree；**零 commit**（owner 手动提交）。

> **执行者须知**：逐步执行，每步跑「验证」确认结果再进下一步。碰到 §8 STOP 条件就停下报告。完成后把 `plans/README.md` 里 206 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：`git diff --stat 06f1bc57 -- pkg/safety pkg/config/network_proxy.go pkg/config/config.go pkg/gateway/controlplane.go pkg/gateway/channels.go pkg/gateway/serve_run.go pkg/gateway/approval.go pkg/tool/state.go pkg/tool/shell_tool.go pkg/turn/approval.go pkg/tui/chat_surface.go pkg/home/seed.go frontend/src/lib/approvalSuggestions.ts`。除 203–205 的改动外有输出，就逐条对照 §2；对不上就 STOP。

## 0. 为什么做、owner 裁决（执行期不得改）

C 端产品，能固定的就固定。裁决全文在 `plans/191-web-config-ui.md` §8.24、§8.25：

1. **网络代理（托管网络，`features.network_proxy`）配置、语义、代码全部删除，所有操作系统都不提供**。owner 原话：「沙箱允许联网的开关打开，就可以直连网络，不支持配置任何代理」。**保留**沙箱「允许联网」开关（`sandbox_workspace_write.network_access`，默认开：开＝直连，关＝断网），开关旁不加说明文案；**保留** `request_permissions` 的整体联网授权。
2. **`windows.sandbox` 删除，Windows 固定受限令牌沙箱（unelevated），elevated 代码删除**（专用账户、防火墙规则、全局串行锁）。「Windows 没配沙箱 → 无沙箱 / 退成只读」的分支删除。
3. **Windows 不显示「允许联网」、沙箱命令固定直连**：受限令牌做不到真断网（今天关掉只是把代理变量指向死地址），不给一个关了也拦不住的开关。按**运行沙箱的那台机器**判断（不是浏览器所在系统）。只读预设在 Windows 上同样能联网（原来也只是软断网）。
4. **Windows 跑批用受限令牌沙箱照常跑**（owner 2026-10-31 改，取代 plans/150 的 fail-closed 约定）：写入限制在任务目录，读不隔离（受限令牌做不到独占读集合）；不再失败。总原则：任何项目类型、任何系统、任何部署环境，沙箱有就用、没有就不用，见 `plans/215-sandbox-optional.md`。
5. **`gateway.public_url` 删除，改为自动识别**（跑批结束通知里「查看：」链接用）。
6. **文件配置项再精简**：`gateway.auth.mode` → 固定 token 鉴权（删无鉴权模式）；`windows.sandbox_private_desktop` → 固定开；`sandbox_workspace_write.go_cache_mode` → 固定 isolated；`exclude_tmpdir_env_var` / `exclude_slash_tmp` → 固定可写（沙箱里 `$TMPDIR` 与 `/tmp` 始终可写）。
7. 结果：forebrain.yaml 只剩 `gateway.http_addr`、`gateway.auth.token`、`credentials.chatgpt`（多库计划会再加数据库段）。
8. 「不考虑老用户」＝不换算；`fb_config` 旧字段的清理由 207 统一迁移。

## 1. 交付

- 删 `pkg/config/network_proxy.go`、`pkg/safety/managed_network.go` 及测试；按域名的联网审批全链删除。
- 删 `WindowsSection`；Windows 固定 unelevated；elevated 代码删除；Windows 上网络恒放行；跑批在受限令牌沙箱里照常跑（读不隔离）。
- `GatewayAuth.Mode` 删除；`Gateway.PublicURL` 删除；对外地址自动识别并存库。
- Go 缓存固定 isolated；TMPDIR 与 /tmp 固定可写。
- `/api/config` 返回 `sandbox_network_configurable`；TUI 面板在 Windows 上不列「允许联网」。

## 2. 现状事实（已对基线核实）

### 2.1 网络代理

- 配置：`FeaturesSection.NetworkProxy`（`pkg/config/config.go:162`）、`pkg/config/network_proxy.go`（443 行，`EnabledValue` 在 38-40 行，默认关）；种子 `pkg/home/seed.go:99` 写 `network_proxy: false`。
- 实现：`pkg/safety/managed_network.go`（2519 行）；`ManagedNetworkCommand`（`pkg/safety/sandbox_config.go:161,202`）；`acquireManagedNetworkProxy`（`pkg/safety/manager.go:368` 一带）；三个后端里接代理的分支（`backend_darwin.go`、`backend_linux.go`、`backend_windows.go`）。
- 按域名的联网审批（只由代理触发）：`NetworkApproval*` 类型与决定（`sandbox_config.go:221-228`、`permission_types.go:587-615`）；`DecisionApplyNetworkPolicyAmendment` 一组可选决定（`permission_types.go:1005-1015`）；`tool.State` 的 `networkSessionDecisions` / `SetNetworkSessionDecision` / 提示钩子（`pkg/tool/state.go:102,125,181,255,382-420,1057`、`shell_tool.go:849-852`）；`turn` 的 `ParseNetworkApproval` / `NetworkResult` / `ValidateNetworkSessionUpdate`（`pkg/turn/approval.go:54,472,513,670-697,779-811,870-913,1867-1878`）；`run` 的 `permissionRequestNetworkTarget`（`pkg/run/runner.go:744-792`）；TUI `promptInlineNetworkApproval`（`pkg/tui/chat_surface.go:506-577`）；网关 `promptGatewayNetworkApproval` 与 `network_approval_context`（`pkg/gateway/approval.go:99-104,227-232,289-326`、`wsevents.go`、`pkg/event/approval_events.go`）；前端 `frontend/src/lib/approvalSuggestions.ts` 的联网建议；TUI 配置面板 `pkg/tui/config_panel_schema.go` 的网络代理描述符。
- **不属于代理、要保留**：`NetworkPermissionProfile`（`permission_types.go:524-560`，`request_permissions` 的整体联网授权）。
- 文字描述（`pkg/safety/process.go:533-545`）：

```go
func SandboxNetworkInWords(cfg *appcfg.Root) string {
	...
	switch {
	case !cfg.SandboxWorkspaceWrite.EffectiveNetworkAccess():
		return "commands cannot reach the internet; approval is required to."
	case cfg.Features.NetworkProxy.EnabledValue():
		return "commands reach the internet through Forebrain Harness's proxy, which asks before contacting a site it has not been allowed."
	default:
		return "commands can reach the internet."
	}
}
```

### 2.2 Windows

- 配置（`pkg/config/config.go:189-204`）：`WindowsSandboxMode`（`unelevated` / `elevated`）、`WindowsSection{Sandbox, SandboxPrivateDesktop}`、`UseSandboxPrivateDesktop()`；校验 `load.go:142-143`。
- 执行（`pkg/safety/backend_windows.go:82-139`）：未配 → `windows_sandbox_disabled`；unelevated + 托管网络 → `windows_managed_network_requires_elevated`；elevated 时 `windowsElevatedMu` 全局串行、专用账户（`windowsSandboxBaseToken`，141-240 行，`ForebrainSandboxOnline` / `ForebrainSandboxOffline`，`NetUserAdd` 需要管理员）、`reconcileWindowsOfflineFirewall`；断网且无托管网络时 `applyWindowsNoNetworkEnvironment`（1305-1325 行，只把代理变量指向 `127.0.0.1:9`）；`spawnWindowsSandboxProcess(…, cfg.Windows.UseSandboxPrivateDesktop(), mode == elevated, …)`（130-138 行）。
- 网络策略（`pkg/safety/managed_network.go:2460-2475` `windowsNetworkPolicyForRequest`）：`AllowNetwork` 且无托管网络 → 不限网；否则 `Offline`。
- 无沙箱分支：`pkg/safety/manager.go:64-67`（workspace-write 退只读）、`212-213`、`236-237`（`windows_sandbox_disabled`）；`pkg/safety/sandbox_config.go:577-584` `effectiveSandboxMode` 的 `windowsSandbox` 参数（203 保留了它，本计划删）；`pkg/safety/process.go:495-499` 两条说明。
- 跑批：`pkg/tool/shell_tool.go:558` 设 `ExclusiveReadablePaths: batch`；`pkg/safety/manager.go:508` 读它。以前 Windows 跑批靠 `windows_sandbox_disabled` 让命令失败。

### 2.3 网关

- 鉴权（`pkg/gateway/controlplane.go:84-106`）：`switch mode := gatewayAuthMode(cfg)` 有 `"none"` 分支直接放行；`gatewayAuthMode` 在 34-40 行；`/auth-mode` 等返回 `auth_mode`（`controlplane.go:366`、`api_extra.go:6497`）；启动横幅 `serve_run.go:236-245`；默认归一化 `load.go:634-636,786`。字段 `GatewayAuth.Mode`（`config.go:16-19`）。
- 对外地址（`pkg/gateway/channels.go:460-471`）：

```go
func batchNotifyLink(cfg *config.Root, projectID, taskID string) string {
	...
	base := strings.TrimRight(strings.TrimSpace(cfg.Gateway.PublicURL), "/")
	if base == "" || … {
		return ""
	}
	return base + "/projects/" + … + "/tasks/" + …
}
```

调用点 `channels.go:376`。`Origin` 校验 `requestOriginMatchesHost`（`controlplane.go:108-128`）：非 GET/HEAD/OPTIONS 的 cookie 请求，`Origin` 的 host 必须等于 `r.Host`。

### 2.4 Go 缓存与临时目录

- `pkg/tool/shell_tool.go:2025-2045` `resolveGoCacheAccess`：`GoCacheModeDisabled` / `GoCacheModeShared` / 默认 isolated。字段与 `EffectiveGoCacheMode` 在 `config.go` 的 `SandboxWorkspaceWrite` 里。
- TMPDIR / `/tmp`：`pkg/safety/process.go:522-526`、`backend_darwin.go:86-90`、`sandbox_config.go:331-332` 按 `ExcludeTmpdirEnvVar` / `ExcludeSlashTmp` 决定是否可写。

## 3. 设计

### 3.1 对外地址自动识别

- 记录点：控制面鉴权通过、且是 cookie 会话的写请求（`authorizeControlPlane` 里 `webSessionCookieValid` 为真、`requestOriginMatchesHost` 为真、方法非 GET/HEAD/OPTIONS、`Origin` 非空）之后，把 `Origin` 规范化（scheme + host[:port]，去路径）交给 `Server.noteWebOrigin`。
- 规则：值未变不写；本机地址（`localhost`、`127.0.0.0/8`、`::1`）**不覆盖**已存的非本机地址。
- 存储：`fb_config` 的一行**非 Root 段** `gateway.observed_origin`（值为 JSON 字符串），照 `auth.*` 行的方式用 `ConfigStore` 原始读写（`LoadCredential` / `SaveCredential` 那一组，或同形的新方法）；`composeSectionsFrom` 本来就跳过非 Root 行（`config_db.go:142-147`）。**207 的迁移不得删除这一行**（在 207 里登记）。
- `batchNotifyLink` 改为读这一行；没有 → 不带链接。

### 3.2 Windows 网络

- `/api/config` 的读取应答加 `sandbox_network_configurable`（`runtime.GOOS != "windows"`）；TUI `/config` 面板的「允许联网」行在 `runtime.GOOS == "windows"` 时不列。
- 运行时：Windows 上构造命令请求时 `AllowNetwork = true`；`windowsNetworkPolicy` 只剩「不限网」，`Offline` 字段与 `applyWindowsNoNetworkEnvironment` 删除；`SandboxNetworkInWords` 在 Windows 上恒为「commands can reach the internet.」。

### 3.3 Windows 跑批照常跑

Windows 后端遇到 `req.ExclusiveReadablePaths` 时：写入集合照常按任务边界限制（受限令牌只给任务目录写权限），独占读集合**不强制**（受限令牌做不到）；不返回错误。在 `SandboxReasonInWords` / 状态说明里如实写「Windows：跑批读不隔离」，供运行状态页展示。

## 4. 步骤

每步结束都跑：`CGO_ENABLED=1 go build -tags fts5 ./... && GOOS=windows go build ./... && GOOS=linux go build -tags fts5 ./pkg/safety/... && go vet -tags fts5 ./pkg/...`（期望：无输出；Windows 交叉编译不带 CGO，若仓库本就不支持 `GOOS=windows go build ./...` 全量，就只编 `./pkg/safety/... ./pkg/config/...` 并在 README 结果里注明）。

1. **网络代理删除**：按 2.1 清单删配置、实现、三个后端分支、按域名审批全链（Go、TUI、网关、前端）、种子、描述符；`SandboxNetworkInWords` 只剩两句。`grep -rn "NetworkProxy\|network_proxy\|ManagedNetwork\|NetworkApproval\|NetworkPolicyAmendment\|networkSessionDecision" pkg frontend/src` 无输出（测试文件也要清掉）。
2. **Windows 固定 unelevated**：删 `WindowsSection` 与校验；`runWindowsSandboxCommand` 去掉模式判断与 elevated 分支；删 elevated 全部函数（账户、登录、防火墙、`windowsElevatedMu`）；`spawnWindowsSandboxProcess` 的 private desktop 参数恒为 true、elevated 参数删除；删 2.2 的无沙箱分支与 `effectiveSandboxMode` 的 `windowsSandbox` 参数；删 `process.go` 两条说明。
3. **Windows 网络恒放行**：按 3.2。
4. **Windows 跑批照常跑**：按 3.3。
5. **网关鉴权固定 token**：删 `GatewayAuth.Mode`、`gatewayAuthMode`、`"none"` 分支、`auth_mode` 返回字段（前端若读它：`grep -rn "auth_mode\|authMode" frontend/src`，同步删）、横幅里的模式、默认归一化。
6. **对外地址**：按 3.1；删 `Gateway.PublicURL`。
7. **Go 缓存与临时目录**：删 `GoCacheMode*` 与 `EffectiveGoCacheMode`，`resolveGoCacheAccess` 只留 isolated 路径；删 `ExcludeTmpdirEnvVar` / `ExcludeSlashTmp` 字段与三处条件，按「不排除」实现。
8. **TUI 描述符与段表**：删被删字段；`rootConfigSections` 删 `windows`；跑 `go test -tags fts5 ./pkg/tui/ -run ConfigPanel -count=1`。
9. **测试修整**：删只测已删功能的用例（`managed_network_test.go`、`network_proxy_test.go`、elevated、`auth.mode=none` 等）。
10. **包图**：`scripts/package-graph.sh`。

## 5. 测试（放在被测文件同名的 `_test.go` 里）

- `pkg/gateway/controlplane_test.go`：没有 token 时控制面一律拒绝（不再有无鉴权模式）。
- `pkg/gateway/channels_test.go`：
  - 一次带 `Origin: https://forebrain.corp.example` 的 cookie 写请求之后，跑批通知带 `https://forebrain.corp.example/projects/…/tasks/…`；
  - 再来一次 `Origin: http://127.0.0.1:8787` 的写请求，链接不变；
  - 从未有写请求 → 不带链接；
  - 重启（新建 Server、同一个库）后仍带链接。
- `pkg/safety`：`SandboxNetworkInWords` 两句；Windows 构建标签下的单测（`backend_windows_test.go` 若存在照它）：跑批请求照常执行、任务目录外写入失败；网络策略恒为不限网。
- `pkg/tool/shell_tool_test.go`：Go 项目里恒设置 isolated 缓存目录；沙箱可写集合恒包含 TMPDIR 与 `/tmp`。
- `pkg/gateway`：`/api/config` 应答含 `sandbox_network_configurable`。
- 前端 vitest：`approvalSuggestions` 不再有联网建议分支。

## 6. 完成判据（机器可查）

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/safety/... ./pkg/tool/... ./pkg/turn/... ./pkg/run/... ./pkg/gateway/... ./pkg/tui/... ./pkg/config/... ./pkg/home/... ./pkg/event/... -count=1 -p 2   # 全 ok
go vet -tags fts5 ./...                                                     # 无输出
test ! -e pkg/safety/managed_network.go && test ! -e pkg/config/network_proxy.go
grep -rn "NetworkProxy\|ManagedNetwork\|NetworkApproval\|WindowsSandboxElevated\|WindowsSection\|PublicURL\|GoCacheMode\|ExcludeSlashTmp\|ExcludeTmpdirEnvVar\|gatewayAuthMode" pkg frontend/src   # 无输出
cd frontend && pnpm typecheck && pnpm test                                  # 全绿
```

**Windows 真机验收**（必须做；仓库里没有 unelevated 在真机上跑过的证据）：工作区可写、工作区外不可写；`curl https://…`、`git clone`、`npm install` 能直连；多条命令并发不排队；跑批任务能跑完、任务目录外写入被拒；设置页与 TUI 面板看不到「允许联网」；库里把它改成关后命令照样能联网。没有 Windows 机器：没有对应环境的项：把步骤与预期结果写成「用户手动验收清单」，附在本计划末尾并抄进 README 的结果栏，**由用户手动验收**；其余完成判据全部通过即可标 DONE（注明「含用户手动验收项」）。

## 7. 维护注意

- 207 删 `fb_config` 里 `windows` 段行与 `features.network_proxy`、`gateway` 相关旧字段；**保留** `gateway.observed_origin` 行。
- 208（企业内网）会改子进程环境变量白名单与跑批读集合，与本计划的 Windows 分支互不影响。
- 网页审批卡里联网审批的显示代码随之删除；历史审批记录里带 `network_approval_context` 的行回看时按普通命令审批卡显示，卡片里出现原始 JSON 即 bug。

## 8. STOP 条件

- 删代理后，macOS / Linux 的沙箱「允许联网 = 开」不再能直连（说明有路径默认依赖代理）。
- `request_permissions` 的整体联网授权有代码依赖 `managed_network.go` 里的东西。
- Windows 后端删 elevated 后，unelevated 路径缺了只在 elevated 里初始化的东西。
- 发现非跑批的其它调用也设了 `ExclusiveReadablePaths`。
