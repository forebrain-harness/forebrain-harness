# 216 — 跑批项目语义收口：只读会话、无信任度、读写边界改「拒绝名单」、可开关的沙箱、永不问用户

- **基线**：`06f1bc57`（dev），并假定 203–208、215 已落地（215：沙箱有就用、无沙箱强制完全访问、跑批不再强制沙箱；203：完全访问＝YOLO）。
- **状态**：DONE（含用户手动验收项，见 §9；`plans/README.md` 的那一行按本次执行的指示未改）
- **优先级 / 工作量 / 风险**：P1 / L / HIGH（重写跑批的沙箱边界；放开了工作区外的只读——owner 明确要求并已知悉风险）
- **依赖**：215 DONE。之后才是 209（多库）——本计划的状态库迁移只写 SQLite；若执行时 209 已落地，迁移须同时写 MySQL / PostgreSQL 版本（照 209 的迁移框架）。
- **执行隔离**：基于 dev HEAD 的 worktree 或主树 in-tree；**零 commit**（owner 手动提交）。

> **执行者须知**：逐步执行，每步跑「验证」确认结果再进下一步。碰到 §8 STOP 条件就停下报告。完成后把 `plans/README.md` 里 216 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：README 里 215 是 DONE；`grep -n "func batchShellBoundary" -A8 pkg/tool/shell_tool.go` 与 §2.2 一致。

## 0. owner 裁决（2026-10-31，执行期不得改）

1. **会话来源**：跑批会话只能由业务系统接口或网页项目空间里「创建任务」产生。网页对话页不能新建跑批会话；终端不能建跑批会话、不能发用户消息。
2. **只读**：跑批会话在网页与终端都只能回放、查看历史消息和其它信息数据，不能续聊，只能做只读操作。**例外**：网页对话页现有的「停止 / 继续 / 重新执行」三个任务按钮**保留**。
3. **审批预设对跑批无效**（只对办公创作、创意编程项目有效）。跑批的审批语义：
   - **有沙箱且本项目启用沙箱**：免审批；
     - 任务自己的工作区：可读写；
     - 本项目可加载的全部技能目录（项目技能目录、当前主代理技能目录、全局共享技能目录）：只读；
     - **FOREBRAIN_HOME 内**除上述之外的全部子目录和文件：**拒绝读写**；
     - **项目根在 FOREBRAIN_HOME 外时**，项目目录内除任务工作区与项目技能目录外的全部目录和文件：**拒绝读写**；
     - **以上范围之外的整个文件系统：只读免审批，写全部拒绝**（npm / pip / go / yum / apt 等软件源的配置文件和目录因此可读不可写）。owner 明确**不加凭据拒读名单**（已知悉 `~/.ssh` 等可读、且允许联网的风险）；
     - 允许联网；
     - **不提供任何提权工具**；
     - 主代理 ⇄ 子代理可以沟通对话，主代理可用进程内全部 `subagent_*` 工具、可派发子代理（plans/202 的 `peer_list` / `peer_send` 同样可用）；
     - **永远不能问用户**：不提供 `user_interaction`、`enter_plan_mode`、`exit_plan_mode`。
   - **无沙箱，或本项目关闭了沙箱**：**完全访问**（＝YOLO）+ 同样永远不问用户、不提供上面三个工具、不提供提权工具。
4. **跑批项目没有项目信任度**：不设置、不显示、不参与任何判定。
5. **沙箱开关**（只在有沙箱的环境出现）：
   - 设置 → 审批与沙箱新增组「跑批项目」，表单项「启用沙箱」，**默认开**，作为新建跑批项目的默认值；
   - 跑批项目空间 → 设置显示「启用沙箱」，**新建项目时拷贝全局值**，用户可单独关闭（覆盖全局）；
   - 无沙箱环境：两处都不显示，配置无效（一律完全访问）。
6. 原型：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html` 的「审批与沙箱」（「跑批项目」组）与「跑批项目设置」场景；实现 1:1 还原。

## 1. 交付

- 跑批沙箱边界改为「拒绝名单 + 例外」模式（§3.1），三个后端各自实现。
- 跑批的包管理器缓存改写进任务工作区（否则 npm / pip / go 装依赖会因写主目录缓存被拒）。
- 无沙箱 / 关闭沙箱的跑批：完全访问。
- 工具集：确认并保证跑批无 `user_interaction`、`enter_plan_mode`、`exit_plan_mode`、`request_permissions`。
- 只读会话：网页与终端堵住所有会改会话的操作（三个任务按钮除外）。
- 跑批项目去掉信任度。
- 沙箱开关：全局配置项 + 项目列（迁移）+ 网页两处 + 运行时判定。

## 2. 现状事实（已对基线核实）

### 2.1 工具门控（`pkg/tool/registry.go`）

- 161：`request_permissions` 只在非跑批注册；384-386：`registerProductTools` 对跑批直接返回（不注册 `user_interaction`）；183：`present_files` 只在办公与跑批注册；218-220：`batchProject(rt)` 读 `rt.ProjectType`。
- `enter_plan_mode` / `exit_plan_mode` 的注册处要执行时找（`grep -rn "enter_plan_mode" pkg --include='*.go' | grep -v _test`，基线命中 `pkg/run/permissions.go:452`、`pkg/run/orchestration_llm.go`、`pkg/state/mode.go` 等），确认跑批会话不注册；办公项目的 agent 模式本就没有这两个工具（plans/145–153）。

### 2.2 跑批 shell 边界（`pkg/tool/shell_tool.go`）

```go
func batchShellBoundary(workspace string, skillRoots, denyRead []string, projectRoot, home string) batchShellPaths {
	ws := filepath.Clean(workspace)
	return batchShellPaths{
		writable: []string{ws},
		readable: MergeAllowedRootPaths(nil, append([]string{ws}, skillRoots...)),
		denied:   MergeAllowedRootPaths(denyRead, nonEmptyPaths(projectRoot, home)),
	}
}
```

调用处 467-479（`req.exclusiveWritablePaths = true`、`allowRead = boundary.readable`、`allowNetwork = true`）；558 行 `ExclusiveReadablePaths: batch`；`batchSandboxConfig`（782-790）强制 workspace-write。今天是「**允许名单**」：只读工作区 + 技能 + 平台运行时默认路径，其余拒读。215 之后无沙箱时不再拒绝执行。

### 2.3 只读会话

- 网页：`frontend/src/views/ChatView.vue:15-41` 跑批项目的对话页只给「新建任务 / 查看任务」（不能新建会话）；63-68 旧链接打开跑批会话为只读；`frontend/src/components/chat/BatchRunActions.vue` 三个任务按钮（plan 153 A11）。
- 终端：`pkg/tui/run.go:1762-1771` `batchReadOnlySession`；footer `batch · read-only`（`pkg/tui/render.go:3499`）；`pkg/tui/chat_session.go:328-370` `batchSession` / `BatchProject` / `LatestBatchExecution`（在跑批项目里启动时打开最近一次执行）；`run.go:5195` 跑批里 shift+tab 提示模式固定。
- 执行时要逐个核对会改会话的入口是否都挡住了（§3.4 清单）。

### 2.4 信任度

网页项目页面包屑显示信任徽标（`frontend/src/views/ProjectDetailView.vue:75-105`）；终端启动时的信任询问（`pkg/tui/workspace_trust.go`，「Trust and continue」）；网关 `/api/host/trust`。

### 2.5 项目表（`pkg/state/schema.sql:8-36`）

`fb_projects` 有 `project_type`（office / coding / batch，会话出生时拷贝）与 `task_notify_json`；没有沙箱开关列。

## 3. 设计

### 3.1 跑批沙箱边界：拒绝名单 + 例外

- 输入：`home`（FOREBRAIN_HOME）、`projectRoot`、`workspace`（任务工作区）、`skillRoots`（本项目可加载的技能目录：项目、当前主代理、全局共享——由现有技能加载逻辑给出，执行时找到返回这三类根的函数）。
- 输出（新结构，替代 `batchShellPaths` 的允许名单语义）：
  - `writable`：`[workspace]`（加 150 已有的工作区内临时目录）；
  - `deniedRoots`：`[home]`，若 `projectRoot` 不在 `home` 内再加 `[projectRoot]`；
  - `allowedUnderDenied`：`workspace`（读写）、`skillRoots` 中落在 `deniedRoots` 内的（只读）；项目根在 home 外时，项目技能目录（只读）；
  - 其余文件系统：只读。
  - 用户自己配置的 deny-read 照旧追加（收窄不放宽）。
- 后端实现：
  - Linux（bubblewrap）：`--ro-bind / /` → 对每个 `deniedRoot` 用 `--tmpfs` 盖掉 → 再 `--ro-bind` 回只读例外、`--bind` 回工作区。
  - macOS（seatbelt）：`(allow file-read*)` 全局 → `(deny file-read* file-write* (subpath denied))` → `(allow file-read* (subpath 只读例外))`、`(allow file-read* file-write* (subpath workspace))`（seatbelt 后写的规则优先，按此顺序生成）；写只允许工作区。
  - Windows（受限令牌，206）：写入只给工作区；读不隔离（已知限制，206 已定）。
  - 现有的 `ExclusiveReadablePaths` 若只为旧语义存在，删掉（清死代码）；新语义用新字段表达（如 `DeniedReadableRoots` + `ReadableExceptions`），三个后端都改。
- 文件工具（读写文件、搜索、列目录等）对跑批用**同一套规则**判定（工具层与沙箱层一致）：落在 `deniedRoots` 内且不在例外里 → 拒绝；工作区外写 → 拒绝。
- **缓存进工作区**：跑批命令的环境变量（只对跑批）设置 `XDG_CACHE_HOME=<ws>/.cache`、`npm_config_cache=<ws>/.cache/npm`、`PIP_CACHE_DIR=<ws>/.cache/pip`、`UV_CACHE_DIR=<ws>/.cache/uv`、`GOCACHE=<ws>/.cache/go-build`、`GOMODCACHE=<ws>/.cache/go-mod`、`GOPATH=<ws>/.cache/go`、`YARN_CACHE_FOLDER=<ws>/.cache/yarn`、`npm_config_store_dir`（pnpm）=`<ws>/.cache/pnpm`；`HOME` 不改。
- `plans/208-enterprise-intranet-offline.md` §3.2「跑批读集合加包管理器配置文件」被本节取代（这些文件在 FOREBRAIN_HOME 外，天然可读）——208 若已实现，删掉那个函数与接线（清死代码）；208 的镜像变量白名单（§3.1）保留。

### 3.2 无沙箱 / 关闭沙箱的跑批

- 判定（执行时，每次运行跑批命令）：`sandboxed := safety.SandboxAvailable() && project.BatchSandbox`。
- `sandboxed=false` → 完全访问（YOLO 放行语义，203）：命令不进沙箱、不受 3.1 边界约束；文件工具也不收窄。
- 两种情况下都：不注册 `user_interaction` / `enter_plan_mode` / `exit_plan_mode` / `request_permissions`；`with_additional_permissions` 照旧拒绝（`shell_tool.go:171`）；免审批。
- 215 §3.5 里「跑批文件工具仍按任务工作区收窄」被本节取代。

### 3.3 工具集

按 2.1 核对；跑批会话若注册了 `enter_plan_mode` / `exit_plan_mode`，在注册处加 `!batchProject(rt)`。`subagent_*`、`peer_*`（202）、`present_files` 保留。

### 3.4 只读会话

网页与终端对跑批会话，**只允许**：查看 / 回放消息与工具卡、查看任务信息与生成物、下载、复制、三个任务按钮（停止 / 继续 / 重新执行）。逐项堵住（前端不渲染入口 + 网关 / 引擎拒绝，两层都要）：

- 发消息、插话、撤回 / 回退（rewind）、编辑重发、分叉（fork）、压缩（/compact）、切模型、切模式、重命名、删除会话（任务的删除在项目空间做，不在这里）、单会话记忆开关。
- 终端：跑批会话里输入框不接受消息（现状核对）；会改会话的 slash 命令一律提示「跑批会话只读」；在跑批项目目录启动终端时不建新会话——打开最近一次执行（现状 `LatestBatchExecution`），没有执行就显示只读的空状态与一句「在网页项目空间创建任务」；`/new` 在跑批项目里拒绝。
- 网关：上述操作的接口与 WS 操作对跑批会话返回 4xx（`start_run` 只接受任务系统的执行与「继续」按钮——现有逻辑核对）。

### 3.5 跑批项目无信任度

- 网页：跑批项目不显示信任徽标、不提供信任 / 取消信任操作。
- 终端：在跑批项目目录启动时**不弹信任询问**（直接只读打开）。
- 运行时：跑批的任何判定不读信任度（3.2 的判定式里没有它；核对 `EffectiveConfig` 等路径不会因信任度收紧跑批）。

### 3.6 沙箱开关

- 全局：新增存库配置项 `batch.sandbox_default`（布尔，默认 `true`）——新 Root 段 `batch`；`rootConfigSections` 登记；207 的默认值登记表登记 `true`；值域校验；TUI `/config` 描述符（有沙箱时显示，分组「跑批项目」、项名「启用沙箱」）。
- 项目：`fb_projects` 加 `batch_sandbox INTEGER NOT NULL DEFAULT 1 CHECK (batch_sandbox IN (0, 1))`（`schema.sql` + 迁移，版本 `stateSchemaVersion+1`；照 `migrateStateV3ToV4` 的加列写法）。**新建项目或项目类型改为跑批时，拷贝当时的全局值**；之后改全局不影响已有项目。
- 网关：项目读取接口返回 `batch_sandbox`；项目更新接口接受它（只对跑批项目有意义；非跑批忽略）；全局值走现有配置写接口。无沙箱时两者照常存取但网页不显示（`approval_presets_visible=false`，215）。
- 网页：设置 → 审批与沙箱的「跑批项目」组（表单项「启用沙箱」，单字段组也显示项名）；跑批项目空间 → 设置的「启用沙箱」（原型两个场景，1:1 还原）。

## 4. 步骤

每步结束都跑：`CGO_ENABLED=1 go build -tags fts5 ./... && go vet -tags fts5 ./pkg/...` 与 `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/... ./pkg/safety/... ./pkg/run/... ./pkg/gateway/... ./pkg/tui/... ./pkg/state/... -count=1 -p 2`。

1. 3.6 的配置项与项目列（迁移 + 默认值 + 拷贝逻辑）。
2. 3.2 判定式与完全访问分支。
3. 3.1 边界（先 macOS seatbelt，再 Linux bubblewrap，再文件工具；缓存环境变量）。
4. 3.3 工具集核对。
5. 3.4 只读会话（网关 / 引擎 → 终端 → 网页）。
6. 3.5 信任度。
7. 网页两处开关（`cd frontend && pnpm typecheck && pnpm test`）。
8. 包图：`scripts/package-graph.sh`。

## 5. 测试

- `pkg/tool/shell_tool_test.go`：边界计算——项目根在 home 内 / 外两种；技能三类目录的只读例外；用户 deny-read 追加。
- macOS 真机（照 `pkg/safety` 已有 seatbelt 真机测试写法）：跑批命令 `cat /etc/hosts` 成功、`cat ~/.npmrc`（若存在）成功、`touch ~/x` 失败、`ls $FOREBRAIN_HOME/state` 失败、`cat <全局技能目录>/x/SKILL.md` 成功、`echo > <工作区>/a` 成功、`npm install <小包>` 成功且缓存落在 `<工作区>/.cache/npm`；项目根在 home 外时 `cat <项目根>/其它文件` 失败。Linux（bubblewrap）同一组用例。
- `pkg/run`：`sandboxed=false`（无沙箱或项目关闭）→ 跑批命令不进沙箱、免审批；跑批会话的工具列表不含 `user_interaction`、`enter_plan_mode`、`exit_plan_mode`、`request_permissions`，含 `subagent_*`。
- `pkg/state`：迁移加列、旧行为 1；新建跑批项目拷贝全局值（全局 false 时新项目为 0）；改全局后已有项目不变。
- `pkg/gateway`：跑批会话的发消息 / 插话 / 回退 / 分叉 / 压缩 / 重命名 / 删除接口返回 4xx；三个任务按钮的接口照常。
- `pkg/tui`：跑批项目里 `/new` 拒绝、改会话的 slash 命令拒绝、不弹信任询问。
- 前端 vitest：跑批项目不显示信任徽标；跑批会话页没有输入框、只有三个任务按钮；两处开关按 `approval_presets_visible` 显示 / 隐藏。

## 6. 完成判据（机器可查）

```bash
CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -p 2        # 全绿
go vet -tags fts5 ./...                                     # 无输出
cd frontend && pnpm typecheck && pnpm test                  # 全绿
```

真机（只验**有沙箱时的边界**——拒绝名单由 seatbelt / bubblewrap 在系统层执行，单元测试证明不了）：macOS 与 Linux 各跑一个真实跑批任务——装一个 npm 包（缓存落在工作区）、读系统文件、写工作区成功；读 FOREBRAIN_HOME 其余部分、写工作区外失败。**无沙箱 / 项目关闭沙箱时没有边界可验**，「按完全访问跑完」由 §5 的 `pkg/run` 单测覆盖，不做真机。没有对应环境的项：把步骤与预期结果写成「用户手动验收清单」，附在本计划末尾并抄进 README 的结果栏，**由用户手动验收**；其余完成判据全部通过即可标 DONE（注明「含用户手动验收项」）。

## 7. 维护注意

- 跑批相关的新工具默认不注册「问用户」与「提权」类工具。
- 技能加载目录的种类若将来增加（例如项目组级技能），3.1 的只读例外要同步加。
- 风险记录：跑批可读 FOREBRAIN_HOME 与项目以外的整个文件系统（含用户主目录里的凭据）且允许联网——owner 2026-10-31 明确接受，不加拒读名单。

## 8. STOP 条件

- 某个后端无法表达「拒绝某根目录、但放行其中若干子目录」。
- 找不到返回「本项目可加载的技能目录」三类根的单一函数，且拼装需要重复技能加载逻辑。
- 只读会话的某个改动入口无法在网关 / 引擎层拒绝（只能在前端藏起来）。
- 拷贝全局值的时机在「项目类型改为跑批」时没有合适的挂点。

## 9. 执行结果与用户手动验收清单（2026-11-01 执行）

### 9.1 已在真机 / 单测里验过的

- macOS（seatbelt，本机真跑）：`pkg/tool` 的 `TestBatchShellIsConfinedToTheTaskWorkspace` 用真实 seatbelt 跑通——
  读 `/etc/hosts` 成功、读工作区外的 `~/.npmrc` 类配置文件成功、写工作区外失败、读 `$FOREBRAIN_HOME/state/*` 失败、
  读本机技能目录 `<home>/skills/<name>/SKILL.md` 成功、写 `<工作区>/outputs/a.txt` 成功、读项目根其余文件失败、
  读同项目其它任务目录失败、`TMPDIR`/`HOME` 指向任务自己的工作区、全程 0 次审批询问。
- 无沙箱 / 项目关闭沙箱（`TestBatchShellWithTheSandboxOffRunsWithFullAccess`）：命令不进沙箱、工作区外可写、0 次审批；
  此时配置被强制为 `danger-full-access`，边界字段不生效（`manager.RunCommand` 的宿主路径不读它们）。
- 工具集（`TestBatchToolSetAsksNobodyAnything`）：跑批会话没有 `user_interaction` / `enter_plan_mode` /
  `exit_plan_mode` / `request_permissions`，有 `subagent_run`、`present_files` 与文件 / shell 工具。
- 只读会话：网关 9 个会改会话的接口返回 409 `batch_session_read_only`（`run_control_test.go`），读接口照旧；
  WS `switch_mode` / `start_run` 与引擎的跑批准入、`/api/chat/sessions` 新建、`/api/runs/:id/input|queued-input`
  的现有拒绝未改；单会话记忆开关由 `memory.Store.SetThreadMemoryMode` 拒绝；终端 `/new`、`/plan`、`/model`、
  `/compact` 由 `batchViewerSlashGate` 与 `dispatchBatchViewerLine` 两道门拒绝（既有测试）。
- 无信任度：终端启动不弹信任询问（`TestAskWorkspaceTrustSkipsBatchProjects`）；网页不显示信任徽标
  （`ProjectDetailView.test.ts`、`ProjectPopover.vue`）。
- 沙箱开关：全局 `batch.sandbox_default`（存储 + `/api/permissions/batch-sandbox-default` + 设置页「跑批项目」组）、
  项目列 `fb_projects.batch_sandbox`（v28→v29 迁移、新建/改为跑批时拷贝全局值）、项目设置页「启用沙箱」，
  两处都随 `approval_presets_visible` 隐藏。

### 9.2 用户手动验收清单（本机无法完成的环境）

**A. Linux 主机（bubblewrap）**

1. 准备：一台装了 `bubblewrap` 的 Linux（非容器，或有足够 capability 的容器），`FOREBRAIN_HOME` 为空目录，`go build -tags fts5 ./cmd/forebrain`。
2. 启动 `forebrain`，创建一个跑批项目（根目录选 `$HOME/work/batch-demo`），保持「启用沙箱」为开，在网页项目空间建一个任务，输入 `npm init -y && npm i left-pad@1.3.0 && echo ok > outputs/a.txt && cat /etc/hosts && cat ~/.npmrc`。
3. 预期：任务成功；`outputs/a.txt` 有 `ok`；`/etc/hosts` 打印出来；`~/.npmrc` 存在时可读、不存在时报 `cat: ...: No such file`；
   npm 的缓存落在 `<项目根>/tasks/<task id>/<seq>/.cache/npm`（`ls` 可验证），**不是** `~/.npm`。
4. 同一任务再跑：`ls $FOREBRAIN_HOME/state`、`cat <项目根>/其它文件`、`echo x > /tmp/escape`、`echo x > <项目根>/escape.txt`。
   预期：四条全部 `Operation not permitted` / 非 0 退出；`/tmp/escape` 与 `<项目根>/escape.txt` 都不存在。
5. 同一任务再跑：`cat $FOREBRAIN_HOME/skills/<任一技能>/SKILL.md`（把技能装到全局共享目录）。
   预期：成功打印（技能目录是边界内的只读例外）。
6. 关闭该项目的「启用沙箱」，再跑一次：`echo x > <项目根>/escape.txt`。
   预期：成功（完全访问），且任务日志里没有任何审批请求。

**B. Docker（`FOREBRAIN_SANDBOXRT_INTEGRATION` 那条路）**

1. 用 214 产出的镜像（带 bubblewrap、非 root）以最小 seccomp 启动，重复 A.2–A.6。
2. 预期同上；另外 `/proc/self/status` 的 `Seccomp` 非 0 时仍能跑（215 的可用性实测会判定沙箱可用/不可用，
   不可用时按完全访问跑，不阻塞任务）。

**C. Windows（受限令牌沙箱，206）**

1. 跑批任务执行 A.2 中的 `echo ok > outputs/a.txt`：预期成功。
2. 执行 `echo x > <项目根>/escape.txt`：预期失败（写只给工作区）。
3. 读 `$FOREBRAIN_HOME` 其余部分：**已知不隔离**（206 的决定），本计划沿用，不作为失败项。
