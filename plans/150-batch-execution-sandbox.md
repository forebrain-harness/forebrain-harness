# Plan 150：跑批执行工作区的强制隔离——任何工具免审批，但 shell 必须在操作系统沙箱里、只能写本次执行的工作区、只能读工作区与共享技能；文件工具同样收窄；绝不越权重跑

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支、不建 worktree，改动直接留在 dev 工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）**：`149` 为 DONE；`grep -n "func TaskWorkspaceFromContext" pkg/tool/state.go` 有输出。
>
> **Drift check（先跑）**：`git diff --stat d9d0b4d -- pkg/tool/shell_tool.go pkg/tool/state.go pkg/tool/file_tools.go pkg/tool/search.go pkg/run/runner.go pkg/run/permissions.go pkg/safety/backend_linux.go pkg/safety/backend_darwin.go`
> 有输出是正常的。按"现状"里的函数名核对；含义变了即 STOP 1。
>
> **基线**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/safety -count=1`（900 s）。

## Status

- **Priority**: P1（跑批对外可用的前提）
- **Effort**: M–L
- **Risk**: HIGH（安全边界；改 shell 的沙箱请求组装与审批入口）
- **Depends on**: `149`
- **Category**: security + direction
- **Planned at**: dev `d9d0b4d` + 2026-10-27 复核工作区

## owner 裁定（2026-10-26）

1. 跑批"llm 调用任何工具都必须免审批"；"每一次执行任务都必须在项目工作区建一个单独的目录，作为任务工作区，任务工作区是一块隔离空间，必须在任务工作区里执行任务"。
2. 隔离强度（选项题）：**操作系统沙箱强制**——shell 进沙箱，只能写任务工作区，网络放开，越权直接拒绝（不询问）；
   "但是所属项目可以读取和使用的所有项目级、主代理级、全局的技能和 MCP 是该项目下所有任务可以共享的"。
3. 读范围（选项题）：**只读本工作区 + 技能**——可读：本次执行工作区、项目级 / 主代理级 / 全局技能目录、系统与运行时（python 等）；
   **不可读：项目根里其它文件（含其它任务的工作区）、forebrain 主目录**。任务之间数据互不可见。

## 设计（评审方定）

对一个跑批会话的回合（ctx 里有 `tool.TaskWorkspaceFromContext(ctx) != ""`，149 盖上的）：

| 面 | 规则 |
|---|---|
| 审批 | 永不询问：除"显式 deny 规则"外的一切工具调用直接放行（与 YOLO 同样的审批短路），**但不**像 YOLO 那样撤掉沙箱 |
| shell | 强制 OS 沙箱（无视 `FOREBRAIN_YOLO` 与配置里的 danger-full-access）；可写集合**只有**本次执行工作区（`ExclusiveWritablePaths`）；可读集合**只有**工作区、共享技能根与 OS 自带运行时白名单（`ExclusiveReadablePaths`，配置的 `/` 或额外 allow_read 不得扩大它）；`TMPDIR/TMP/TEMP` 指向 `<工作区>/.work/tmp`；网络放开；拒读：项目根、FOREBRAIN_HOME；沙箱不可用 → 命令直接失败（fail closed）；模型要求 `require_escalated` / `additional_permissions` → 直接拒绝；沙箱拒绝后**不走**"申请越权重跑" |
| 文件写（write/edit/apply_patch） | 目标必须在工作区内（`GuardWrite`），否则硬拒绝；`present_files` 是登记/读取交付文件，不另写交付文件，其输入仍按 146 的当前 `WorkingDir` 校验 |
| 文件读（read_file / 搜索） | 只允许工作区内与共享技能根内；OS 运行时由 shell 沙箱提供，不给交互式文件工具扩大读取范围 |
| MCP | 照常使用（MCP 进程由项目 runner 启动，不在任务沙箱里——owner：MCP 在项目所有任务间共享） |

## Current state

### 1. shell 工具（`pkg/tool/shell_tool.go`）

- `NewShellTool` 处理函数（约 `:88-270`）：校验 `in.SandboxPermissions`、`validateShellAdditionalPermissions` → `cfg := sessionConfig(ctx, st, rt)` → `maybeHandleApplyPatchCommand`（apply_patch 走文件工具）→ `rewriteShellCommand` →
  `wdAbs, err := WorkingDir(ctx)` → 评估 → `approvalRequired := skillAccess != "" || planGate == PlanModeShellAsk || shellAssessmentRequiresApproval(...)` → 需要时经 `st.ActionHook()` 要审批 →
  `runSandboxedShellCommand(ctx, st, rt, sandboxedShellRequest{command, timeoutMs, sandboxPermissions, additionalPermissions, prefixRule, profile, profileElevation, cwd: wdAbs, toolName, toolDescription, editDiff})`。
- `type sandboxedShellRequest struct`（约 `:255-282`）含 `exclusiveWritablePaths bool`（"this request's writable paths are the whole write policy"）。
- `runSandboxedShellCommand`（约 `:284-660`）：`manager := safety.NewManager()`；`runtimeCfg := manager.UpdateConfig(cfg, shellToolSettingsSources(rt, cfg), snap, req.cwd, req.cwd, os.TempDir(), st.AllowedRoots())`；
  `contained := func(sp) bool { return manager.DecideShellCommand(cfg, req.command, sp, req.profile, false).UseSandbox }`；
  依次算 `extraBinds`（可写附加）、`denyWrite`、`allowRead`（含 `rt.AdditionalReadRoots`、`st.LoadedSkillRoots()`）、`denyRead`、`allowNetwork`；
  `shellEnv := append(planModeShellEnv(ctx, rt), agentRuntimeEnv(rt)...)`；`commandRequest := func(sp, approvedNetwork) safety.CommandRequest {… AdditionalWritablePaths: extraBinds, ExclusiveWritablePaths: req.exclusiveWritablePaths, DeniedWritablePaths: denyWrite, AdditionalReadablePaths: allowRead, DeniedReadablePaths: denyRead, …, AllowNetwork: allowNetwork, …, Env: shellAttemptEnv(shellEnv, goCache.Env, contained(sp)), …}`；
  `res, decision, runErr := manager.RunCommand(ctx, cfg, commandRequest(effectiveSandboxPermissions, …))`；
  随后 `denied := decision.UseSandbox && safety.IsLikelySandboxDenied(decision, res)`，`if denied && … && sandboxRetryApprovalAllowed(snap.ApprovalPolicy) && !denyReadRestricted { … requestSandboxRetryApproval … }`——**这就是"沙箱拒绝后申请越权重跑"**。
- `ApplyFilesystemPolicy`（约 `:967-992`）给进程内文件工具装读写策略（按 runner，不按会话）。

### 2. OS 沙箱后端（`pkg/safety`）

- `CommandRequest`（`sandbox_config.go` 约 `:120-166`）：`AdditionalWritablePaths`、`ExclusiveWritablePaths`、`DeniedWritablePaths`、`AdditionalReadablePaths`、`DeniedReadablePaths`、`AllowNetwork`、`Env` 等。
- macOS `seatbeltProfile`（`backend_darwin.go` 约 `:76-130`）：每个可读路径一条 `(allow file-read* (require-all (subpath P) (require-not … denied within P)))`；每个可写路径一条读 + 一条写。
  **拒读父目录、单独放行子目录**在 seatbelt 下成立（子目录有自己的 allow 规则，父目录的 deny 只体现在"包含它的 allow 规则"的 require-not 里）。
- Linux `appendFilesystemPolicyArgs`（`backend_linux.go` 约 `:162-233`）：规则按路径深度从浅到深排序后逐条输出：读 → `--ro-bind`，写 → `--bind`，拒读目录 → `--tmpfs P --remount-ro P`。
- 后端本身已是白名单基础：macOS `seatbeltBasePolicy` 是 `(deny default)`，Linux 在没有根 `/` 读绑定时 `--tmpfs /`，`IncludePlatformDefaults` 才补 OS 运行时目录。
  但 `applyRuntimeConfigToRequest`（`pkg/safety/manager.go` 约 `:468-520`）会把配置/授权的 `AllowRead` 再并回请求，因此只在 shell 里删几个路径无法阻止 `allow_read: "/"` 重新放开全机；本计划新增 `ExclusiveReadablePaths` 就是为了在该合并点截断。
  **问题**：拒读的项目根被 tmpfs 覆盖并**立刻**只读重挂后，更深的工作区 `--bind`（及项目内技能目录 `--ro-bind`）需要在只读 tmpfs 里建挂载点，会失败。本计划修这一点。

### 3. 审批入口（`pkg/run/runner.go` `actionHook`，约 `:403-500`）

开头处理 `exit_plan_mode` 与已批准动作；之后对 apply_patch 有一段沙箱判定（"patch rejected: writing outside of the project"）；再 `decision = r.evaluateToolPermission(...)`、`r.applyMCPToolApprovalPolicy(...)`；
`if !exitPlanMode && decision.Behavior != safety.BehaviorDeny && r.yoloEnabled() { return "", false, nil }`（YOLO 的审批短路）。
`safety` 里"显式 deny"的判定范例：`explicitPermissionDeny(decision)`（`runtime.go` 约 `:585`，未导出）= `decision.Behavior == BehaviorDeny && decision.Matched != nil && decision.Matched.Behavior == BehaviorDeny`。

### 4. 文件工具的读写守卫（`pkg/tool`）

- `(*State).GuardWrite(ctx, absPath)`（`state.go` 约 `:1043-1053`）：目前只拦计划模式；调用点 `file_tools.go` 约 `:1576`、`:1598`（apply_patch 的目标与移动目标）、`:1877`，`permissions.go` 约 `:73`、`:99`。
- `(*State).ReadPathDenied(path)`（`state.go` 约 `:2559-2597`）按 runner 级 deny/allow；调用点 `file_tools.go` 约 `:287`（`if st.ReadPathDenied(abs) && !st.PathUnderLoadedSkillRoot(abs)`）、`search.go` 约 `:833`；
  `ReadDirPrunable(dir)`（约 `:2602`）供遍历剪枝。`PathUnderLoadedSkillRoot(abs)`、`LoadedSkillRoots()` 已存在。
- 会话预设展示：`(*Runner).SessionPresetFor(ctx, sid)`（`permissions.go` 约 `:902`）返回 `SessionPresetState{Current, Effective, Source}`。

## Commands you will need

| 目的 | 命令 | 成功时 |
|------|------|--------|
| 编译 | `go build ./...` | exit 0 |
| Linux 交叉 vet | `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/safety` | exit 0（若因其它包 cgo 失败，改为只 `go build` 该包） |
| 单测 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/safety -count=1` | ok |
| macOS 真机沙箱 | Step 6 | 见该步 |
| Linux 后端测试（有 Docker 时） | `docker run --rm -v "$PWD":/src -w /src golang:1.26 bash -lc 'apt-get update -qq && apt-get install -y -qq bubblewrap >/dev/null && CGO_ENABLED=0 go test ./pkg/safety -run "Linux\|Bwrap\|Mount" -count=1'` | ok；无 Docker 记 NOT RUN |

## Scope

**In scope**：`pkg/tool/shell_tool.go`、`pkg/tool/shell_tool_test.go`、`pkg/tool/state.go`、`pkg/tool/state_test.go`、`pkg/tool/file_tools.go`、`pkg/tool/search.go`、
`pkg/run/runner.go`（只改 `actionHook`）、`pkg/run/permissions.go`（只改 `SessionPresetFor`）、`pkg/run/runner_test.go` 或 `permissions_test.go`、
`pkg/safety/sandbox_config.go`、`pkg/safety/manager.go`、`pkg/safety/backend_linux.go`、`pkg/safety/backend_linux_test.go`、`pkg/safety/backend_darwin.go`、`pkg/safety/backend_darwin_test.go`、`docs/integration/batch-tasks.md`（只加一节"安全与隔离"：上面"设计"表的内容 + MCP 不在任务沙箱里的提醒 + 沙箱不可用时命令失败）。

**Out of scope**：非跑批会话的任何行为（所有改动都以"ctx 里有任务工作区"为前提）；MCP 进程沙箱化；网页 / 终端上的预设显示（`152`、`153` 读本计划的 `SessionPresetFor` 结果）；`managed_network` 的策略。

## Steps

### Step 1：审批短路（`pkg/run/runner.go` `actionHook`）

在"已批准动作"分支之后、apply_patch 段之前插入：

```go
// A batch task runs unattended inside its task sandbox (plans/150): nobody is
// ever asked anything. An explicit deny rule still refuses; every other call
// proceeds, confined by the sandbox and the workspace guards, and is never
// escalated out of them.
if !exitPlanMode && strings.TrimSpace(tool.TaskWorkspaceFromContext(ctx)) != "" {
	if isApplyPatch {
		return "", false, nil // GuardWrite confines every target to the workspace
	}
	decision := r.evaluateToolPermission(tool.ConversationSessionIDFromContext(ctx), kind, pay)
	decision = r.applyMCPToolApprovalPolicy(ctx, kind, decision)
	if decision.Behavior == safety.BehaviorDeny && decision.Matched != nil && decision.Matched.Behavior == safety.BehaviorDeny {
		return "", false, formatPermissionDenyError(decision, kind, pay)
	}
	return "", false, nil
}
```

**Verify**：`go build ./...` → exit 0。

### Step 2：shell 的跑批分支

1. `sandboxedShellRequest` 加 `taskWorkspace string`（非空即跑批）。
2. `NewShellTool` 处理函数：在 `wdAbs` 算出之后、评估与审批之前：
   ```go
   if ws := strings.TrimSpace(TaskWorkspaceFromContext(ctx)); ws != "" {
   	if in.SandboxPermissions != safety.SandboxPermissionsUseDefault || in.AdditionalPermissions != nil || sandboxBypassApproved {
   		return "", fmt.Errorf("batch tasks run inside their task sandbox; additional or escalated permissions are not available")
   	}
   	editDiff := beginShellEditDiff(ctx, cmd, wdAbs, false)
   	defer editDiff.release()
   	return runSandboxedShellCommand(ctx, st, rt, sandboxedShellRequest{
   		command: cmd, timeoutMs: in.TimeoutMs, sandboxPermissions: safety.SandboxPermissionsUseDefault,
   		profile: safety.ProfileWorkspaceWrite, cwd: wdAbs, toolName: "shell", toolDescription: shellToolDescription,
   		editDiff: editDiff, taskWorkspace: ws,
   	})
   }
   ```
   （跳过全部审批逻辑；`codeIntelOf(rt).DidRunShell` 跑批无 LSP 需要，可省略。）注意：`sandboxBypassApproved` 在函数前段已算出；`maybeHandleApplyPatchCommand` 在此之前已处理 apply_patch（文件工具路径，受 Step 4 约束）。
3. `runSandboxedShellCommand`：在函数开头 `cfg := sessionConfig(...)` 之后：
   ```go
   batch := strings.TrimSpace(req.taskWorkspace) != ""
   if batch {
   	cfg = batchSandboxConfig(cfg) // copy: SandboxMode workspace-write, DefaultPermissions "" — ignores YOLO and danger-full-access on purpose
   }
   ```
   `batchSandboxConfig` 为新私有函数（`cfg == nil` 时返回只设了 `SandboxMode` 的新 `appcfg.Root`）。
   在 `allowRead` / `denyRead` / `extraBinds` / `allowNetwork` 都算完之后、`timeout` 之前：
   ```go
   if batch {
   	ws := filepath.Clean(req.taskWorkspace)
   	loadedSkillRoots := st.LoadedSkillRoots()
   	projectRoot := strings.TrimSpace(ProjectRootFromContext(ctx))
   	home := ""
   	if rt != nil { home = strings.TrimSpace(rt.Home) }
   	fenced := nonEmptyPaths(projectRoot, home)          // nonEmptyPaths 已存在（约 :2003）
   	extraBinds = []string{ws}
   	req.exclusiveWritablePaths = true
   // Do not inherit allowRead or an ambient root grant: the batch request
   allowRead = MergeAllowedRootPaths(nil, append([]string{ws}, loadedSkillRoots...))
   denyRead = MergeAllowedRootPaths(denyRead, fenced)
   allowNetwork = true
   	goCache = goCacheAccess{}                            // 跑批不挂 Go 缓存
   }
   ```
   `loadedSkillRoots` 只取项目/主代理/全局技能目录（含已发现可加载技能根），不取记忆目录；运行时根的实现沿用 `runner.Load` 已有的 skillRoots/`st.LoadedSkillRoots()`，不能把 workspace 或 FOREBRAIN_HOME 整个目录并入。
   同时在 `commandRequest` 闭包构造的请求上：`batch` 时设 `ExclusiveReadablePaths: true`、`IncludePlatformDefaults: true`。后者只补后端已审核的 OS 运行时（Linux `linuxPlatformDefaultReadRoots`、macOS `writeSeatbeltMinimalPolicy` 的库/二进制/设备白名单），不补用户 home 或其它项目目录。
   不能简单把 `/tmp`、`/var/tmp` 加到 denyWrite：工作区本身可能合法位于这些目录下，父目录 deny 会误伤它。由 Step 2.5 让 Darwin 在 `ExclusiveWritablePaths` 时**不生成平台临时目录写规则**（读规则保留），而不是拒写整个父目录。
4. 沙箱必须生效（fail closed）：在 `RunCommand` 之前加
   ```go
   if batch && !contained(safety.SandboxPermissionsUseDefault) {
   	return "", fmt.Errorf("batch tasks need the OS sandbox, which is not available on this host; the command was not run")
   }
   ```
5. 临时目录：`batch` 时 `os.MkdirAll(filepath.Join(ws, ".work", "tmp"), 0o755)`，`shellEnv` 末尾追加 `TMPDIR=`、`TMP=`、`TEMP=` 指向它。
6. 不越权重跑：`if denied && res.NetworkDenial == nil && ...` 那个条件开头加 `!batch &&`；跑批的沙箱拒绝原样作为工具结果返回（沿用现有 `sandboxDeniedCommandError` / 完成输出的写法）。
7. 网络审批：`batch` 时 `NetworkApproval` 传 nil、`ManagedNetwork` 不设（若 `commandRequest` 闭包里设了 managed network，batch 分支跳过它）。

**Verify**：`go build ./...` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → 无新增失败。

### Step 2.5：在权限合并处实现独占读集合

1. `pkg/safety/sandbox_config.go` 的 `CommandRequest` 在 `ExclusiveWritablePaths` 后加：
   ```go
   // ExclusiveReadablePaths declares AdditionalReadablePaths to be this
   // command's complete application-data read set. Configured allow-read roots
   // are not added on top. IncludePlatformDefaults and explicit deny rules are
   // independent: the first supplies the OS runtime, the second still narrows.
   ExclusiveReadablePaths bool
   ```
2. `pkg/safety/manager.go` `applyRuntimeConfigToRequest`：
   ```go
   readRoots := runtimeCfg.Filesystem.AllowRead
   if req.ExclusiveReadablePaths {
   	readRoots = nil
   }
   for _, path := range readRoots {
   	req.AdditionalReadablePaths = appendUniquePath(req.AdditionalReadablePaths, path)
   }
   ```
   后面的 DenyRead、DeniedReadablePatterns、IncludePlatformDefaults 处理不变。非跑批请求该位为零，行为逐字保持。
3. `pkg/safety/backend_darwin.go` 与 Linux 后端都使用该已合并请求；不需要新增“绕过 deny”的分支。
   - `seatbeltProfile` 只有 `!req.ExclusiveWritablePaths` 时才按 `ProfileWorkspaceWrite` 追加 `os.TempDir()` 和 `/tmp` 写根；`writeSeatbeltMinimalPolicy` 增加一个 `allowTempWrites bool` 参数，exclusive 时传 false，跳过该函数中 `/tmp`、`/private/tmp`、`/var/tmp`、`/private/var/tmp` 的 `file-write*` 行但保留读行。
   - 这同时保证：位于 `/tmp` 内的**本执行工作区**仍由它自己的精确 allow-write 规则可写，父级其它 `/tmp` 内容不可写；不能用父目录 denyWrite 代替。
   - Linux 没有 `/` 读绑定时保持 `--tmpfs /`，只挂运行时与工作区/技能根，写集合由 ExclusiveWritablePaths 原语义收窄。
4. `pkg/safety/backend_darwin_test.go`：`TestBatchSeatbeltHasNoWholeHomeOrTmpWrite`，请求只含 workspace/skill + 两个 Exclusive 位，断言 profile 不含 `/Users` 的 allow、不含系统临时目录写规则，含 OS runtime allow 与 workspace 写。
   再加 `TestBatchSeatbeltWorkspaceUnderTmpRemainsWritable`：workspace 取 `/tmp/<test>/tasks/T/1`，断言它有精确写规则，父 `/tmp` 没有写规则。非 exclusive 的既有 profile 仍保留系统临时目录写规则。
   `pkg/safety/manager_test.go`（如已有该同名文件则加入；否则把该纯函数测试放 `runtime_test.go`，不新建超预算文件）：`TestExclusiveReadablePathsDoesNotInheritAllowRead`——配置 AllowRead 含 `/`，独占请求合并后没有 `/`，普通请求仍保留 `/`。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/safety -run 'ExclusiveReadable|BatchSeatbelt' -count=1` → ok；`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/safety` → exit 0。

### Step 3：Linux 后端支持"拒读父目录、放行子目录"

`pkg/safety/backend_linux.go` `appendFilesystemPolicyArgs`：输出循环里遇到 `filesystemMountDeny` 的**目录**时，若 `normalized` 中存在别的规则路径**严格位于**它之下，则只输出 `--tmpfs P`，把 `--remount-ro P` 记进一个延后列表；循环结束后按记录顺序追加这些 `--remount-ro`。
没有嵌套放行的拒读目录保持原样（立即 `--remount-ro`）。注释说明原因（bwrap 需要在 tmpfs 里为嵌套挂载建挂载点，只读后建不了；嵌套的 bind 是独立挂载，父 tmpfs 之后改只读不影响它们）。

**Verify**：`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go vet ./pkg/safety` → exit 0；Linux 测试见 Test plan 4。

### Step 4：文件工具按工作区收窄（`pkg/tool/state.go`）

1. `GuardWrite`：在计划模式判断之后加
   ```go
   if ws := strings.TrimSpace(TaskWorkspaceFromContext(ctx)); ws != "" && !pathWithinWorkspace(absPath, ws) {
   	return fmt.Errorf("batch task: writes are confined to the task workspace %s", ws)
   }
   ```
   `pathWithinWorkspace` 对两边都 `filepath.Clean` + 尝试 `EvalSymlinks`（目标不存在则对其父目录求值再拼回文件名），再用现有的 `isWithinDir` 判断——防止经软链写出去。
2. 新增 `func (s *State) ReadPathDeniedIn(ctx context.Context, path string) bool`：ctx 有任务工作区时，返回 `!(within(ws) || s.PathUnderLoadedSkillRoot(p))`（`p` 同样 Abs/Clean/EvalSymlinks）；否则 `return s.ReadPathDenied(path)`。
   同理 `ReadDirPrunableIn(ctx, dir)`：跑批时目录既不在工作区内、也不是工作区或任一技能根的祖先 → 可剪枝。
3. 调用点替换：`file_tools.go` 约 `:287` 改用 `st.ReadPathDeniedIn(ctx, abs)`（保留 `&& !st.PathUnderLoadedSkillRoot(abs)`）；`search.go` 约 `:833` 与该文件里所有 `ReadDirPrunable(` 调用改用 `…In(ctx, …)`（若该处拿不到 ctx，向上把 ctx 传下来——仅限 `search.go` 内的私有函数；涉及导出签名则 STOP 3）。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → 无新增失败。

### Step 5：会话预设如实显示（`pkg/run/permissions.go` `SessionPresetFor`）

最前面（在 YOLO 判断之前）：本 runner `r.ProjectType() == state.ProjectTypeBatch` 时返回
`SessionPresetState{Current: "batch-sandbox", Effective: "batch-sandbox", Source: "project_type"}`，并加常量 `PresetBatchSandbox = "batch-sandbox"`（放在 `pkg/run`，注释：跑批固定"沙箱隔离 · 免审批"，不是三个内置预设之一，界面只读显示）。
网关 / 终端改预设的入口对跑批会话应拒绝——`grep -rn "SessionUpdates(\|ApplyPermissionUpdate(" pkg/gateway pkg/tui | grep -v _test` 找到"用户选预设"的处理函数，各加：会话是 batch → 409 / 提示 `batch_preset_fixed`。

**Verify**：`go build ./...` → exit 0。

### Step 6：macOS 真机沙箱验收（必须做；本机是 macOS）

写一个**真跑 seatbelt** 的测试 `TestBatchShellIsConfinedToTheTaskWorkspace`（放 `pkg/tool/shell_tool_test.go`，`runtime.GOOS != "darwin"` 时 `t.Skip`，`testing.Short()` 时 skip）：
临时目录造 `<proj>/tasks/T/1/{inputs,outputs,.work}`、`<proj>/tasks/T2/1/secret.txt`、`<proj>/shared.txt`、`<home>/skills/demo/SKILL.md`（并让 `st` 的已加载技能根包含 `<home>/skills/demo`——找现有测试设置 `ReplaceLoadedSkillCatalog` 的写法）；
另在 `<home>/other/private.txt` 与临时目录的 `<outside>/private.txt` 写非项目秘密，配置/权限快照故意带 `allow_read: ["/"]` 与 YOLO，证明独占读集合不被扩大。
ctx：`WithProjectRoot(<proj>)`、`WithTaskWorkspace(<proj>/tasks/T/1)`；`rt.Home = <home>`。依次执行 shell 工具：
- `echo ok > outputs/a.txt` → 成功，文件存在；
- `echo x > ../../T2/1/pwn.txt` → 失败（沙箱拒绝），文件不存在；
- `cat ../../T2/1/secret.txt` → 失败；`cat ../../../shared.txt` → 失败；
- `cat <home>/skills/demo/SKILL.md` → 成功；`cat <home>/other/private.txt`、`cat <outside>/private.txt` → 失败（不能因 allow_read `/` 而成功）；`cat /etc/hosts` → 成功（OS 运行时白名单）；
- `python3 -c "import tempfile,os;print(tempfile.gettempdir())"` → 输出在 `<ws>/.work/tmp` 下（无 python3 则跳过此项）；另写工作区外的 `/tmp/batch-escape` → 失败（若工作区位于 `/tmp` 下，则额外断言其自身 `outputs/` 仍可写）；
- 工具入参 `sandbox_permissions: "require_escalated"` → 立即报错，命令未执行；
- 进程环境 `FOREBRAIN_YOLO=1` 时第 2 条仍失败（`t.Setenv`）。
同时断言整个过程**没有**调用 action hook（给 `st` 装一个计数的假 hook）。

### Step 7：测试（见 Test plan）

## Test plan

1. `pkg/run`：`TestBatchActionHookNeverAsks`（跑批 ctx 下 shell / web_fetch / mcp 类调用 `actionHook` 返回不需要审批；带显式 deny 规则的调用返回 deny 错误）；`TestBatchSessionPresetIsFixed`。
2. `pkg/tool/state_test.go`：`TestGuardWriteConfinesBatchWrites`（工作区内 OK；工作区外、`..` 穿越、指向外部的软链 → 错误；无任务工作区 ctx 时行为不变）；
   `TestReadPathDeniedInForBatch`（工作区内 / 技能根内放行；项目根其它文件、兄弟任务目录、FOREBRAIN_HOME 拒绝；非跑批 ctx 回落 `ReadPathDenied`）。
3. `pkg/tool/shell_tool_test.go`：Step 6 的真机测试；另加不依赖 seatbelt 的 `TestBatchShellRequestShape`——把 `manager.RunCommand` 换成可注入的假实现（先看文件里现有测试是否已有注入点；没有就只测 `batchSandboxConfig` 与 safety 的请求合并纯函数），断言请求的 `AdditionalWritablePaths == [ws]`、`ExclusiveWritablePaths`、`ExclusiveReadablePaths`、`IncludePlatformDefaults`、`DeniedReadablePaths` 含项目根与 home、`AdditionalReadablePaths` 只含工作区/技能根且不含 `/` 或 home 下非技能路径、`AllowNetwork`。
4. `pkg/safety/backend_linux_test.go`：`TestDeniedDirWithNestedBindDefersRemount`（规则：拒读 `/p`、写 `/p/tasks/T/1`、读 `/p/.agents/skills` → 参数里 `--tmpfs /p` 出现在两个嵌套 bind 之前，`--remount-ro /p` 出现在它们之后）；`TestDeniedDirWithoutNestedRuleRemountsImmediately`。
   （只能在 Linux 上跑：用 Commands 表里的 Docker 命令；无 Docker → NOT RUN，交给 CI。测试里的路径用 `t.TempDir()` 造真实目录，因为函数会 `os.Stat`。）

## Done criteria

- [ ] `go build ./...`、Linux 交叉 vet exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/run ./pkg/safety ./pkg/architecture -count=1` 无新增失败；Test plan 1–3 新测试存在且通过（Step 6 的真机测试在本机 macOS **必须**通过，不得 skip）
- [ ] Linux 后端测试：PASS 或 NOT RUN（写明原因）
- [ ] 非跑批回归：`grep -n "taskWorkspace\|TaskWorkspaceFromContext" pkg/tool/shell_tool.go pkg/tool/state.go pkg/run/runner.go` 的每一处都在"任务工作区非空"的条件内（评审方逐条看）
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

1. 前置不满足或"现状"对不上。
2. Step 6 的真机测试里任何一条"应失败"的命令成功了（隔离被打穿）——记录 seatbelt 生成的 profile（`seatbeltProfile` 的返回值）停下，不要放宽测试。
3. 搜索工具的剪枝函数是导出 API 且有包外调用方，改签名会波及别的包。
4. `safety` 的 Windows 后端在跑批下无法做到同等隔离——不要实现 Windows 跑批隔离，确认 Step 2.4 的 fail-closed 在 Windows 上让命令直接失败即可；若 fail-closed 判定在 Windows 上返回"已沙箱"却不能保证工作区只写，停下报告。

## Maintenance notes

- 跑批的隔离只对 shell 子进程与进程内文件工具生效；**MCP 服务器进程不在任务沙箱里**（owner：MCP 在项目所有任务间共享）。若某个 MCP 能读写任意路径，它就是跑批的旁路——给跑批项目配 MCP 时要评估这一点（写进 `docs/integration/batch-tasks.md` 的"安全"一节，由本计划补一段）。
- "系统与运行时可读"由**后端已有的平台白名单**落实，不再允许除项目根与 FOREBRAIN_HOME 外的任意主机路径：Linux 使用 `linuxPlatformDefaultReadRoots`，macOS 使用 `seatbeltPlatformReadSubpaths`/minimal policy 的库、二进制、设备与系统配置。
  用户 home 的其它目录、项目外文档、其它执行目录均不可读；`ExclusiveReadablePaths` 防止配置或授权把该集合扩大。新增运行时根必须进入平台白名单审查与对抗测试，不能在 shell 临时拼接。
- 跑批无视 `FOREBRAIN_YOLO`：YOLO 是操作者对**交互会话**的选择，不能打穿跑批的隔离承诺。

**后续裁决（owner 2026-10-31）**：`windows.sandbox` 删除、Windows 固定受限令牌沙箱——以前靠 `windows_sandbox_disabled` 让 Windows 跑批命令失败的路径随之消失；本计划的「Windows 跑批 fail-closed」改由 191 §8.25 第 2 条要求的显式判定保证。网络代理整块删除，本计划步骤 7 的「batch 分支跳过 managed network」随之作废。

**再改（owner 2026-10-31，取代本计划「跑批强制 OS 沙箱、不可用即失败」）**：任何项目类型、任何操作系统、任何部署环境，沙箱**有就用、没有就不用**——跑批有沙箱照旧按任务边界隔离，没有就无沙箱照常跑（仍免审批、仍不越权）；Windows 跑批在受限令牌沙箱里照常跑（读不隔离）。实施见 `plans/215-sandbox-optional.md` 与 206 §3.3。`shell_tool.go:567-569` 的「batch tasks need the OS sandbox」报错由 215 删除。
