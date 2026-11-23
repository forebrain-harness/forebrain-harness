# 215 — 沙箱有就用、没有就不用：实测可用性、启动不再拒绝、跑批不强制沙箱、无沙箱时自动完全访问；终端 shift+tab 循环与完全访问 footer

- **基线**：`06f1bc57`（dev），并假定 203–208 已落地（203：预设只剩「默认」「完全访问」、完全访问＝YOLO；206：Windows 跑批；208：跑批读集合）。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / M / HIGH（无沙箱时整机按完全访问运行、且对用户不可见——owner 明确要求；实现必须精确，不能漏到有沙箱的机器上）
- **依赖**：203、206、208 DONE。214（部署）以本计划为前提。与 209–213 无文件交集，可并行。
- **执行隔离**：基于 dev HEAD 的 worktree 或主树 in-tree；**零 commit**（owner 手动提交）。

> **执行者须知**：逐步执行，每步跑「验证」确认结果再进下一步。碰到 §8 STOP 条件就停下报告。完成后把 `plans/README.md` 里 215 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：`git diff --stat 06f1bc57 -- pkg/safety/manager.go pkg/safety/process.go pkg/safety/runtime.go pkg/safety/engine.go pkg/tool/shell_tool.go pkg/process/open.go pkg/process/config_apply.go pkg/run/permissions.go`，除 203–208 的改动外有输出就对照 §2；对不上就 STOP。

## 0. owner 裁决（2026-10-31，执行期不得改）

1. **任何类型的项目、任何操作系统、任何部署环境下，沙箱都是有就用、没有就不用**；有没有沙箱**不是硬性条件**——不得因为沙箱不可用而拒绝启动或拒绝执行。
2. **有沙箱时**：审批预设只有「默认」「完全访问」两种（203），网页与终端一致。
3. **无沙箱时**：直接按**完全访问**运行（完全访问＝YOLO，203）。**配置项不出现**：设置页「审批与沙箱」整个标签、终端 `/permissions`、终端 `/config` 里的审批与沙箱项。**但要让用户知道当前是完全访问**（owner 同日补充）：
   - 网页对话输入框（**含技能工坊**的对话输入框）**保留审批预设选择框，固定选中「完全访问」、置灰、不能改**；
   - 终端 footer 显示完全访问的说明（§3.6）。
4. 无沙箱时**不看项目信任度**（一律完全访问）。
5. 除第 3 条的输入框选择框与终端 footer 外，不另加提示（运行状态、审批卡、横幅都不提沙箱）。
5a. **终端 footer 与 shift+tab 对齐 Claude Code 的语义、但文案自己写**（owner：「不要照搬 Claude Code 的文案」）：shift+tab 在几种模式之间**循环切换**；当前是完全访问时 footer 显示完全访问说明——**有沙箱时用户自己开启完全访问也要显示**。网页有沙箱时保持现状（输入框里的选择框本就显示当前预设）。
6. 照旧要人拍板的事不变：退出计划、向用户提问；194–201 将加的「同目录有其他代理时的危险 git」照旧弹审批。
7. **跑批**：有沙箱照旧按任务边界隔离；没有就无沙箱照常跑（仍免审批、仍不能申请额外权限）。Windows 跑批在受限令牌沙箱里照常跑（206）。
8. 「沙箱可用」要**实际试跑**判断：容器里常见「装了 bubblewrap 但被 seccomp / AppArmor 挡住」。

## 1. 交付

- 沙箱可用性实测（进程内缓存）。
- 启动时沙箱不可用不再拒绝启动。
- 无沙箱：有效预设强制为完全访问（不写库、不看信任度）；所有审批预设界面隐藏；不显示任何沙箱 / 完全访问提示。
- 跑批：删除「没有沙箱就不跑」的闸。
- 一个只读的能力字段供网页决定隐藏什么。

## 2. 现状事实（已对基线核实）

- **启动拒绝**：`pkg/process/open.go:367-371` 与 `pkg/process/config_apply.go:32-35` 调 `safety.NewManager().StartupCheck(...)`，失败即返回错误；`StartupCheck`（`pkg/safety/manager.go:245-255`）在非完全访问且 `Decide(cfg, ToolKindShell).UnavailableReason != ""` 时报错——**没装 bubblewrap 的 Linux 主机今天起不来**。
- **可用性只看命令在不在**：`pkg/safety/process.go:603-630` `backendUnavailableReason`（macOS 查 `sandbox-exec`，Linux / WSL 查 `bwrap` / `bubblewrap`，Windows 查 API）。
- **可用性写进权限运行时**：`pkg/safety/runtime.go:117,235` `store.SetSandboxAvailable(shellSandboxAvailable(cfg))`；`shellSandboxAvailable` 在 545-550 行。审批引擎在不可用时让每条 shell 命令都问（`pkg/safety/engine.go:540-545`）——本计划之后无沙箱一律完全访问，这条只在「有沙箱的机器上某次判定不可用」的边角里起作用，保留不动。
- **YOLO / 完全访问放行**：`pkg/safety/runtime.go:557-571` `applyRuntimePermissionOverrides`；203 之后完全访问与 YOLO 走同一分支，输入是「环境变量或本会话有效预设为完全访问」。
- **会话预设**：`pkg/run/permissions.go:912-1066`——`SessionPresetFor`（读行 / YOLO / 跑批）、`injectSessionPermission`（每轮把行应用到运行时；YOLO 时 `recordYoloPreset` 会**写一行 full_access**）。
- **跑批闸**：`pkg/tool/shell_tool.go:567-569`

```go
	if batch && !contained(safety.SandboxPermissionsUseDefault) {
		return "", fmt.Errorf("batch tasks need the OS sandbox, which is not available on this host; the command was not run")
	}
```

  `shell_tool.go:171`（跑批拒绝申请额外权限）与 575-580（跑批被沙箱拒绝时不弹出沙箱审批）保留。跑批预设显示 `PresetBatchSandbox`（`pkg/run/permissions.go:920-922`）。
- **界面入口**：网页设置「审批与沙箱」标签（`frontend/src/components/settings/SettingsApprovalTab.vue`）、会话选择器（`frontend/src/components/chat/ApprovalPresetPicker.vue`）、网关 `/api/permissions/approval-default`、`/api/permissions/session-preset`（`pkg/gateway/api_extra.go:78-81`）；终端 `/permissions` 命令与 `/config` 面板描述符（`pkg/tui/config_panel_schema.go`）。网关提示文案 `pkg/gateway/http_server.go:699-706`（「YOLO / danger-full-access is enabled…」）。运行状态里的沙箱状态：`pkg/safety/process.go:429-430,457`（「not enforced: …」）。
- **原型**：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html` 的「无沙箱」场景——设置子导航里没有「审批与沙箱」。

## 3. 设计

### 3.1 实测可用性

- `pkg/safety` 新增 `probeSandbox(platform) (reason string)`，进程内 `sync.Once` 缓存：
  - Linux / WSL：找到 `bwrap` 后**真跑一次**与正式执行同一组关键参数的最小命令（以 `buildBubblewrapArgsWithBridge` 的实际参数取最小子集：`--unshare-user --unshare-pid --die-with-parent --ro-bind / / --dev /dev --proc /proc -- true`），5 秒超时；失败 → 原因 `bubblewrap_unusable`。
  - macOS：`sandbox-exec -p '(version 1)(allow default)' /usr/bin/true`。
  - Windows：保持现有 API 检查。
- `backendUnavailableReason` 在命令存在之后再看 `probeSandbox` 的结果。
- 对外只暴露一个函数 `safety.SandboxAvailable() bool`（本进程、本机）。

### 3.2 启动不拒绝

删除 `open.go` 与 `config_apply.go` 的拒绝分支；不可用时只写一条**服务端日志**（运维日志，不是界面；`slog.Info`，带原因）。`StartupCheck` 若再无调用者就删除（清死代码）。

### 3.3 无沙箱＝强制完全访问

- 有效预设的唯一计算处（203 之后的 `SessionPresetFor` / `injectSessionPermission` / 全局预设读取）加最外层规则：**非跑批 且 `!safety.SandboxAvailable()` → 有效预设＝完全访问**，不读会话行、不读全局预设、不看信任度。
- **不写库**：这条强制不调用 `persistSessionPreset`、不触发 `recordYoloPreset`；库里存的全局预设与会话行原样保留（多台机器共享状态库，换到有沙箱的机器仍按库里的值）。
- 放行语义就是 203 的完全访问＝YOLO：除明确拒绝规则外全部放行；退出计划、提问、危险 git（194–201）照旧问人。
- 跑批不走这条（跑批有自己的免审批语义，见 3.5）。

### 3.4 界面全隐藏、不提示

- 网关在已有的会话 / 配置引导接口里加一个只读布尔 `approval_presets_visible`（＝`safety.SandboxAvailable()`；执行时选一个网页启动必读的接口，如 `/api/config` 的读取应答，206 已在那里加过 `sandbox_network_configurable`——两者合并为一个字段即可：无沙箱时两者都隐藏）。
- `approval_presets_visible=false` 时：
  - 网页：设置子导航不渲染「审批与沙箱」标签（直接访问该路由时跳到「外观」）；**对话输入框（会话页与技能工坊共用的输入框组件）里的审批预设选择框照常渲染，但固定为「完全访问」、`disabled`、只有这一项**（原型「无沙箱·输入框」场景，1:1 还原；不加说明文字或提示气泡）；新会话下拉里的预设项同样固定置灰；其它地方不出现「YOLO」「沙箱不可用」。
  - 网关：`/api/permissions/approval-default`、`/api/permissions/session-preset` 的写请求返回 404（读请求返回 404 亦可——以网页不再调用为准）；`http_server.go:699-706` 的提示**只在环境变量强制完全访问时**出现，无沙箱自动完全访问时不出现。
  - 运行状态：沙箱一行**不显示**（不显示「不可用」，也不显示原因）。
  - 终端：`/permissions` 不出现在命令菜单与帮助里，直接输入时按「未知命令」处理；`/config` 面板不列审批预设、沙箱可写工作区、允许联网；footer 按 §3.6 显示完全访问。
- 有沙箱时一切照 203。
- 原型已按此画（「无沙箱」场景）；实现 1:1 还原。

### 3.6 终端 footer 与 shift+tab（有沙箱、无沙箱都适用）

- 现状：shift+tab 只在普通 / 计划模式之间切换（`pkg/tui/run.go:5190-5215` `hotkeyTogglePlanMode`）；footer 文案 `plan mode (shift+tab to exit)`（`pkg/tui/render.go:3494`），由 `pkg/tui/reducer.go:3713-3715` 选段；完全访问在 footer 上不显示。跑批时 shift+tab 提示「The mode is fixed in batch projects.」（保留）。
- 新语义（对齐 Claude Code：shift+tab 循环、footer 标出当前模式）：
  - **有沙箱**：循环顺序 **默认 → 计划模式 → 完全访问 → 默认**。进入完全访问＝把本会话预设设为 `full_access`（与 `/permissions` 选择同一条写入路径，写会话行）；离开完全访问＝设为 `workspace_write`；进入 / 离开计划模式沿用 `statepkg.Switch` 现有逻辑。
  - **无沙箱**：预设固定完全访问，循环只在 **完全访问 ↔ 计划模式** 之间。
  - 用 `/permissions`、网页或 `FOREBRAIN_YOLO` 进入的完全访问，footer 同样显示。
- footer 文案（**自拟，不照搬 Claude Code**；owner 可改）：
  - 完全访问：`full access (shift+tab to cycle)`，用现有主题里的警示色（执行时找 footer 已有的颜色常量，不新造）。
  - 计划模式：`plan mode (shift+tab to cycle)`（原 `… to exit` 改为 `… to cycle`，因为现在是循环）。
  - 默认：不显示模式段（同现状）。
  - 计划模式与完全访问同时成立时（例如先 `/permissions` 选完全访问再进计划）只显示计划模式段。
- 改动点：`hotkeyTogglePlanMode` 改名为 `hotkeyCycleMode` 并实现上面的循环；footer 选段函数加完全访问段；相关测试（`input_events_test.go:78` 等）改名与改断言。

### 3.5 跑批

- 删 `shell_tool.go:567-569` 的闸；无沙箱时跑批命令照常经 `manager.RunCommand` 执行（`UseSandbox=false`）。
- 核实跑批的免审批短路覆盖无沙箱时引擎可能返回的 `Ask`（`grep -rn "ProjectTypeBatch" pkg/run pkg/tool` 找到短路位置），不覆盖就让它覆盖（明确拒绝规则照旧拒绝）。
- 仍不能申请额外权限。跑批在有沙箱时的读写边界、无沙箱 / 项目关闭沙箱时按完全访问运行、以及文件工具是否收窄，由 `plans/216-batch-project-semantics.md` 定（216 取代本条原先「文件工具仍按任务工作区收窄」的写法）。
- `PresetBatchSandbox` 显示名若含「沙箱」字样，改成不暗示沙箱的名字（如「跑批」）；常量名一并改。

## 4. 步骤

每步结束都跑：`CGO_ENABLED=1 go build -tags fts5 ./... && go vet -tags fts5 ./pkg/...` 与 `CGO_ENABLED=1 go test -tags fts5 ./pkg/safety/... ./pkg/tool/... ./pkg/process/... ./pkg/run/... ./pkg/gateway/... ./pkg/tui/... -count=1 -p 2`。

1. 3.1 实测可用性 + 单测（假 `bwrap` 脚本：存在但退出码非 0 → 不可用）；`SandboxAvailable()` 支持测试注入。
2. 3.2 启动不拒绝；删 `StartupCheck` 死代码。
3. 3.3 强制完全访问（不写库）。
4. 3.5 跑批。
5. 3.4 网关字段与接口；TUI 隐藏；网页隐藏（`cd frontend && pnpm typecheck && pnpm test`）。
6. 包图：`scripts/package-graph.sh`。

## 5. 测试（放在被测文件同名的 `_test.go` 里）

- `pkg/safety`：`probeSandbox` 三种结果（找不到、找到但跑不了、可用）。
- `pkg/process/open_test.go`：沙箱不可用时 `Open` 成功。
- `pkg/run/permissions_test.go`：沙箱不可用 + 会话行 `workspace_write` + 目录未信任 → 有效预设为完全访问；库里的行仍是 `workspace_write`（没被改写）；沙箱可用时同一会话按 `workspace_write`。
- `pkg/safety/runtime_test.go`：无沙箱时 `exit_plan_mode` 与用户提问类工具仍要人回答；明确拒绝规则仍拒绝。
- `pkg/tool/shell_tool_test.go`：跑批在沙箱不可用时照常执行、不产生审批请求；`with_additional_permissions` 仍被拒；跑批文件工具写任务目录外仍被拒。
- `pkg/gateway`：沙箱不可用时 `approval_presets_visible=false`、两个预设写接口 404、`http_server.go` 的提示不出现；设置 `FOREBRAIN_YOLO=1` 时提示照旧出现。
- `pkg/tui`：沙箱不可用时命令菜单里没有 `/permissions`、`/config` 面板没有审批与沙箱项；footer 显示 `full access (shift+tab to cycle)`；shift+tab 在完全访问与计划模式之间切换。有沙箱时：shift+tab 依次经过默认 → 计划模式 → 完全访问 → 默认，会话行随之写 `workspace_write` / `full_access`；用 `/permissions` 选完全访问后 footer 同样显示；跑批里 shift+tab 仍提示模式固定。
- 前端 vitest：`approval_presets_visible=false` 时设置导航无「审批与沙箱」；输入框组件里的预设选择框 `disabled`、值为「完全访问」、只有一项（会话页与技能工坊各断言一次）；页面文本里不含「YOLO」「沙箱不可用」。

## 6. 完成判据与验收

```bash
CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -p 2        # 全绿
go vet -tags fts5 ./...                                     # 无输出
cd frontend && pnpm typecheck && pnpm test                  # 全绿
grep -rn "batch tasks need the OS sandbox\|StartupCheck(" pkg --include='*.go'   # 无输出
```

**真机**（无沙箱时没有读写边界可验；真机只验**降级是否正确**，其中最关键的是第 1 条的探测——容器里常见「装了 bubblewrap 但被 seccomp 挡住」，判错就会每条命令都失败，只有真容器能证明）：
1. Docker（镜像里装了 bubblewrap、不挂 seccomp 放行配置）起网关：探测判为不可用；能启动；网页设置里没有「审批与沙箱」、对话输入框与技能工坊输入框里的预设选择框固定「完全访问」且置灰、运行状态不提沙箱；让模型执行 `ls`、写工作区外的文件 → 都不弹审批；让模型退出计划 → 仍弹确认；跑一个跑批任务能跑完。
2. 没装 bubblewrap 的 Ubuntu 主机 `forebrain gateway start` + 终端 `forebrain`：同上；终端里 `/permissions` 不在菜单、输入后提示未知命令；footer 显示完全访问说明，shift+tab 只在完全访问与计划模式之间切换（在 Terminal.app 里截图确认）。
3. 装好 bubblewrap 且允许用户命名空间的主机：预设两档都在；「默认」下 `ls` 不弹审批（沙箱里跑），写工作区外弹审批（回归）；终端 shift+tab 三档循环，进入完全访问时 footer 显示说明，网页刷新后会话选择框显示「完全访问」（同一会话行）。

没有对应环境的项：把步骤与预期结果写成「用户手动验收清单」，附在本计划末尾并抄进 README 的结果栏，**由用户手动验收**；其余完成判据全部通过即可标 DONE（注明「含用户手动验收项」）。

## 7. 维护注意

- 今后不要再写「沙箱不可用即失败」的逻辑。
- 新增任何审批 / 沙箱相关的界面元素，都要受 `approval_presets_visible` 控制。
- 部署文档（214）要写明「没有沙箱时按完全访问运行」——那是给运维看的部署说明，不是产品界面。

## 8. STOP 条件

- 有效预设的计算不止一处、无法加一个统一的最外层规则。
- 强制完全访问在某条路径上会写库（例如有地方把有效预设回写成会话行）。
- 跑批的免审批短路覆盖不了无沙箱时的 `Ask`，且修改它会影响交互项目。
- 实测探测在某平台上需要 root 或会留下进程 / 挂载。
