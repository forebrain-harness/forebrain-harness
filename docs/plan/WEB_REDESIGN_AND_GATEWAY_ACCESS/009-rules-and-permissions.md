# 计划 009：规则指令与权限——主代理/项目规则文件的新建与编辑、权限规则页收口、审批三档预设（全局默认 + 会话切换）

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新 README 中本计划的状态行。**不要提交代码。**
>
> **漂移检查（先运行）**：
> `git diff --stat b92a1b1..HEAD -- frontend/src/views/PermissionsView.vue frontend/src/views/ChatView.vue frontend/src/views/SettingsView.vue pkg/gateway/api_extra.go pkg/safety/permission_types.go pkg/safety/runtime.go pkg/config/config.go pkg/config/approval_policy.go pkg/assembly/rules.go pkg/home/workspace_bootstrap.go`
> 计划 007 改过 `SettingsView.vue`（标签化）与 `api_extra.go`（路由注册）、008 建过项目空间壳。那是预期的。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：MED-HIGH（新增规则文件读写端点与会话级审批端点，都是权限相关面；靠 Go 单测 + 真机用例兜底，全部写路径走既有安全设施）
- **依赖**：计划 007（设置页标签结构、`/rules` 菜单位）、计划 008（项目空间壳的 rules/perm 标签挂点）
- **类别**：direction
- **基线**：提交 `b92a1b1`，2026-09-29

## 为什么要做

owner 要求（第 34 条）："左侧菜单栏加上主代理的规则指令和对应页面，UI与项目空间的规则指令保持一致，并且主代理和项目空间的规则指令页的规则文件都必须支持创建和编辑，只能创建forebrain harness支持的规则文件，显示下拉框让用户选择要创建的规则文件"；第 18 条："权限页的审批模式只对用户暴露tui侧/permissions里三种模式：Read Only、Default、Full Access，并要加上说明……可选值确定的必须改成选择框，能选的就不要让用户输入"；决策 D5 已定：**设置里设全局默认 + 对话输入框旁按会话切换**。预览 captions 的 `rules` 条（396 行）与 `permissions` 条（404 行）是页面内容合同。

## 现状

- **主代理规则文件**：`pkg/home/workspace_bootstrap.go:55` `orderedBootstrapFiles = ["AGENTS.md","SOUL.md","USER.md"]`，位于主代理工作区根；单文件字节预算 `:14` `DefaultMaxBytesPerFile = 12_000`（解析时超限截断，只是提示性上限）。目前没有任何读写这些文件的 HTTP 端点。
- **项目规则文件**：`pkg/assembly/rules.go:73` `ProjectForebrainFiles(wd, chain, maxBytes)`——读项目根的 `FOREBRAIN.md`，`chain=true` 时读根到 CWD 链上每一层的 `FOREBRAIN.md`（根目录优先）。同样没有 HTTP 端点。
- **权限规则 API 已存在**（`pkg/gateway/api_extra.go`）：
  - 路由 `:51-55`：`GET /api/permissions/rules`（`:445` `handlePermissionRules`，query `source`/`behavior`/`session_id`，行含 `source`/`behavior`/`tool_name`/`rule_content`）、`POST /api/permissions/evaluate`（`:505`）、`GET /api/permissions/explain`（`:533`）、`POST /api/permissions/updates`（`:555` `handlePermissionUpdate`）。
  - `safety.PermissionUpdate`（`permission_types.go:636`）：`type`（addRules/replaceRules/removeRules/setMode）、`destination`（session/localSettings/projectSettings，`:111-115`）、`behavior`（allow/deny/ask，`:28-30`）、`rules []PermissionRuleValue`（`:118`，`tool_name` + `rule_content` 模式推断/`command_prefix`/`command`）、`session_id`、`mode`。
  - 写入路径：`pkg/safety/runtime.go:268` `destinationPath`——localSettings 落 `<主代理工作区>/state/permissions/local_settings.json`；projectSettings 落 `<受信项目根>/.forebrain/safety.json`（不受信/无项目则拒绝）。handler 已做 `ExplainRefusedUpdate` 拒绝校验并 `RefreshSandboxRuntime`。
- **审批三档预设**：`pkg/safety/permission_types.go:805-812` 三个常量（read-only/auto/full-access）、`:838` `BuiltinApprovalPresets()`（含 Label 与 Description）、`:869` `DescriptionFor(cfg)`（补沙箱网络说明）、`:877` `ApprovalPresetByID`（ID 或 Label 均可解析）、`:900` `MatchApprovalPreset`（要求 `DefaultPermissions` 为空才匹配）、`:918` `ApplyToConfig`（只写 `cfg.SandboxMode` 并清空 `DefaultPermissions`；审批半边走权限存储）。
  - 全局持久化字段已存在：`pkg/config/config.go:118` `permissions.approval_policy`（`pkg/config/approval_policy.go:26`，mode：on-request/never/…）、`:123` `permissions.sandbox_mode`（read-only/workspace-write/danger-full-access，`:40-42`）。三档映射：read-only→`on-request`+`read-only`；auto(Default)→`on-request`+`workspace-write`；full-access→`never`+`danger-full-access`（即 `BuiltinApprovalPresets` 的 Approval/SandboxMode 对）。
  - TUI 会话级应用语义（参照，不改 TUI）：`pkg/tui/chat_session.go:2047` `ApplyPermissionPreset`——`preset.ApplyToConfig(cfg)` + `ApplyPermissionUpdate{Type: UpdateSetMode, Destination: session, SessionID}` + 刷新沙箱运行时。Web 没有对应端点。
- **前端**：`frontend/src/views/PermissionsView.vue`（240 行）已有规则表与"判定验证"；设置页的权限记忆区计划 007 已删除。审批模式目前是自由输入。没有 `/rules` 路由与规则文件编辑器。
- 工具选择框数据源：`GET /api/tools`（`api_extra.go:48`）。
- README 全局规则（90-115 行）全部适用。

## 设计

### 1. 规则文件 API（新建 `pkg/gateway/rules_api.go`）

| 端点 | 语义 |
| --- | --- |
| `GET /api/rules/agent` | 列主代理工作区根的三个引导文件：`{files:[{name, exists, size_bytes, updated_at}]}`，name ∈ AGENTS.md/SOUL.md/USER.md（白名单来自 `home.orderedBootstrapFiles` 语义，表在 gateway 内自持常量并加注释指向 `pkg/home`，避免导出循环依赖） |
| `GET /api/rules/agent/{name}` | 读单文件正文（text/plain）。`name` 必须在白名单内，否则 400；路径一律 `filepath.Join(workspaceRoot, name)`，`name` 含分隔符即拒绝（白名单已保证） |
| `PUT /api/rules/agent/{name}` | 覆写正文。请求体上限 64 KiB（远大于 12_000 字节的解析预算，保存成功但响应带 `warning: exceeds_assembly_budget`）；文件不存在时创建 |
| `GET /api/rules/project/{projectId}` | 列该项目的 `FOREBRAIN.md` 链：`{files:[{dir, exists, ...}]}`，`dir` 从项目根开始（`""` 表示根）——语义对应 `assembly.ProjectForebrainFiles(wd, true, budget)`，目录枚举用项目根下的一级子目录快照 + 根本身，**只列真实存在 FOREBRAIN.md 的目录加根**，另附 `{"create": [可选目录列表]}`（根 + 一级子目录，上限 50 个，超限提示用文件系统操作） |
| `GET /api/rules/project/{projectId}?dir=<子目录>` / `PUT` 同路径 | 读/写某一层的 `FOREBRAIN.md`；`dir` 白名单校验：空（根）或项目根下一级已存在子目录名；写入路径 `filepath.Join(projectRoot, dir, "FOREBRAIN.md")`，越界即 400 |

权限与租户：所有端点走主代理租户校验（`activePrimarySummary`）；project 端点校验该项目属于当前主代理（`projectStore.Get` 的 `AgentID`）。写操作不提交 git（项目文件是用户仓库的一部分，由用户自己管理版本）。

### 2. 主代理规则指令页（`/rules`）

- 菜单数组加一项 `rules`（007 约定，`scroll-text` 图标，`meta.scope='agent'`）。
- 页面（`frontend/src/views/RulesView.vue`）：左侧文件列表（三个引导文件，未创建的灰显"未创建"），右侧 Markdown 编辑器（纯 textarea + 等宽字体，不渲染预览——预览页如此）；"新建规则文件"下拉只列**支持且尚未创建**的文件；保存按钮 PUT，成功后行内提示"保存后从新会话开始生效，不打断正在进行的会话"（预览文案）。
- 超预算提示：响应带 `warning` 时显示"文件超过单文件 12,000 字节的组装预算，超出部分不会进入上下文"。

### 3. 项目空间规则指令标签（`/projects/:id/rules`）

替换 008 的占位组件 `ProjectRulesTab.vue`：与 `/rules` 同一套布局（抽公共组件 `components/rules/RulesFileEditor.vue`），文件列表换成 FOREBRAIN.md 链；新建下拉列出可选目录（根/一级子目录）；页头"项目"作用域徽标。

### 4. 权限页收口（`/permissions`，主代理维度）

- 规则表：`GET /api/permissions/rules?source=localSettings`（只列本主代理 localSettings 的规则，带 `source` 徽标）。"添加规则"表单全部选择框 + 一个输入框：
  - 动作（behavior）：allow/deny/ask 下拉；
  - 工具（tool_name）：`GET /api/tools` 下拉 + "全部工具"选项（`tool_name` 传 `*` 时语义为通配——若后端 `PermissionRuleValue` 不接受 `*`，则 STOP 上报，见 STOP 条件）；
  - 规则内容：唯一输入框（`rule_content`）。
  - 保存 = `POST /api/permissions/updates` `{type:"addRules", destination:"localSettings", behavior, rules:[{tool_name, rule_content}]}`。
  - 删除规则 = `{type:"removeRules", destination:"localSettings", behavior, rules:[...]}`（行内按钮）。
- **判定验证**：现有 `/explain` + `/evaluate` 表单保留，输入改为工具下拉（同上数据源）+ 内容输入。
- 项目规则在项目空间的权限标签维护（见第 5 条），本页不放项目规则。
- 实现说明：不带 `session_id` 的 `/api/permissions/rules|explain|evaluate` 只按本主代理自己的设置（localSettings + 配置）作答，不再经过绑定 gateway 启动目录项目的 runner；`/api/permissions/updates` 的 localSettings 写入会同步给本主代理所有在跑的 runner，projectSettings 一律拒绝（交给第 5 条的项目端点）；带 `session_id` 的读写落在该会话自己的 runner 上。列表行带 `rule`（规则原样），删除时原样回传。

### 5. 项目空间权限标签（`/projects/:id/perm`）

替换 008 占位 `ProjectPermTab.vue`：规则表走 `GET /api/permissions/rules?source=projectSettings`；添加/删除同第 4 条但 `destination:"projectSettings"`（实现说明：已改为项目自己的端点 `GET /api/v1/projects/:id/permissions/rules`、`POST …/updates`、`GET …/explain`，按该项目根加载，写入后同步给该项目所有在跑的 runner——通用端点只认 gateway 启动目录那一个项目）（后端已有信任门：项目不受信时 handler 仍会接受请求但 `destinationPath` 拒绝——前端在项目未信任时禁用表单并显示"信任此项目"引导，与 008 的 MCP 信任按钮同一状态源）。"只按本项目的规则匹配"判定验证复用同一 `/explain` 表单组件，传项目会话的 `session_id`（项目空间会话列表里的最近会话，无会话则不带 `session_id`）。

### 6. 审批三档：全局默认（设置 → 审批默认标签）

007 已把设置页标签做成可扩展数组，本计划加 `approval` 标签（`SettingsApprovalTab.vue`）：
- 三张单选卡：Read Only / Default / Full Access，说明文案取自 `safety.BuiltinApprovalPresets` 的 Description 对应中文（预览 380-384 行的文案为准，双语两表）；
- 当前选中态用 `MatchApprovalPreset` 的服务端语义（见第 7 条 GET 响应的 `current`），不匹配三档时显示"自定义（由配置文件或其他来源设置）"且三张卡都不选中；
- 保存 = `PUT /api/permissions/approval-default` `{preset: "read-only"|"auto"|"full-access"}`。

### 7. 审批端点（新建，放 `pkg/gateway/rules_api.go` 同文件）

| 端点 | 语义 |
| --- | --- |
| `GET /api/permissions/approval-default` | 读 persistedConfig：`DefaultPermissions != ""` → `{current: null}`；否则按三档映射在 `approval_policy.Mode`+`sandbox_mode` 里匹配，命中返回 `{current: "<preset-id>", description: "<中文说明>"}`，不命中 `{current: null}` |
| `PUT /api/permissions/approval-default` | `{preset}` → `safety.ApprovalPresetByID` 解析（拒绝未知值）；按第"现状"节的映射写 `cfg.Permissions.ApprovalPolicy = NewApprovalPolicy(mode)`、`cfg.Permissions.SandboxMode = preset.SandboxMode`，并保证 `DefaultPermissions` 为空（与 `ApplyToConfig :918` 同一语义——命名字段会让预设失效，必须清掉）；`saveAndReload`。**这是全局配置**，写 forebrain.yaml，不属于任何主代理 |

### 8. 会话级审批切换（对话输入框旁）

- 新端点 `POST /api/permissions/session-preset` `{session_id, preset}`：镜像 TUI `ApplyPermissionPreset`——对该会话所在的 runner 应用 `preset.SessionUpdates(session_id)`：`setMode` 与 `setSandboxMode` 两条会话级更新，审批模式和沙箱模式都只记在权限存储里该会话名下，不改任何进程共享的配置。沙箱判定一律读 `safety.ConfigForSnapshot(cfg, 会话快照)`。响应 `{ok, description}`。**只影响该会话，不写任何文件**（TUI 语义：临时选择不落盘；配置重载不会撤销它，YOLO 优先于它）。`GET /api/permissions/session-preset?session_id=` 返回该会话当前档位。
- 前端：对话输入框旁"审批模式"下拉（005 已留位），三项 = 三档 Label，选中即调端点；新会话不预选（跟随全局默认）。会话切换时拉 `GET /api/permissions/rules?session_id=<id>` 里的 mode 信息或端点返回的当前态刷新显示（实现取最简，验收以"切换后行为生效"为准）。
- 说明文案：下拉每项 hover/展开显示 `DescriptionFor` 语义的中文说明（预览 380-384 行文案）。

## 需要的命令

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| Go 静态检查 | `go vet ./...` | 退出码 0 |
| Go 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/safety/... -count=1` | 全部 `ok` |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 前端构建 | `cd frontend && corepack pnpm build --outDir "$TMPDIR/fb-webui" --emptyOutDir` | 退出码 0 |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |
| 缓存不变式 | `git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun` | 无输出 |

## 范围

**只允许修改**：
- `pkg/gateway/rules_api.go`（新建：规则文件端点 + 审批默认端点 + 会话预设端点）、`pkg/gateway/rules_api_test.go`（新建）、`pkg/gateway/api_extra.go`（仅 `:51-55` 旁的路由注册行）
- `frontend/src/views/RulesView.vue`（新建）、`frontend/src/views/PermissionsView.vue`（表单收口）
- `frontend/src/components/rules/RulesFileEditor.vue`（新建）、`frontend/src/components/project/ProjectRulesTab.vue`、`ProjectPermTab.vue`（替换 008 占位）
- `frontend/src/components/settings/SettingsApprovalTab.vue`（新建）、`frontend/src/views/SettingsView.vue`（仅标签数组加一行）
- `frontend/src/components/chat/ApprovalPresetPicker.vue`（新建，输入框旁下拉）、`frontend/src/views/ChatView.vue`（挂接该组件，不动对话流逻辑）
- `frontend/src/router/index.ts`（`/rules` 一条）、`frontend/src/App.vue`（菜单加 `rules` 项）、`frontend/src/locales/index.ts`、`frontend/src/lib/api.ts`（新端点封装）
- `frontend/e2e/rules-permissions.spec.ts`（新建）

**不要碰**：
- `pkg/tui/**`（`ApplyPermissionPreset` 只是参照）；`pkg/safety/**`、`pkg/home/**`、`pkg/assembly/**`（只读其常量与语义，不修改）。
- 计划 010/012/013 的页面；设置页其他标签内容。

## 步骤

### 第 1 步：规则文件端点

实现设计第 1 条，`rules_api_test.go` 覆盖：白名单外 name 400；读不存在文件 `exists:false`；PUT 后 GET 一致；PUT 超 12_000 字节返回 warning；project `dir` 越界（`../`、多级、不存在）400；其他主代理的项目 404。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` 全部 `ok`；`go vet ./...` 退出码 0。

### 第 2 步：审批端点

实现设计第 7、8 条，测试覆盖：GET 在 `DefaultPermissions` 非空时 `current:null`；PUT `auto` 后 yaml 里 `approval_policy: on-request` 且 `sandbox_mode: workspace-write`；PUT 未知 preset 400；session-preset 缺 `session_id` 400；session-preset 后 GET `/api/permissions/rules?session_id=` 的 mode 反映新档位。

**验证**：同第 1 步命令。

### 第 3 步：规则指令两页

实现设计第 2、3 条（含公共编辑器组件、菜单项、路由）。

**验证**：`cd frontend && corepack pnpm test` 全部通过；`grep -n "rules" frontend/src/router/index.ts` 有输出。

### 第 4 步：权限页收口与项目权限标签

实现设计第 4、5 条。

**验证**：`cd frontend && corepack pnpm test` 全部通过。

### 第 5 步：审批默认标签与会话切换

实现设计第 6 条与第 8 条前端部分。

**验证**：`cd frontend && corepack pnpm test` 全部通过。

### 第 6 步：真机验证

新建 `frontend/e2e/rules-permissions.spec.ts`：
1. `/rules`：列表显示三个文件（AGENTS.md/SOUL.md 存在与否以脚手架 home 为准）；新建下拉只列未创建文件；创建 USER.md、输入内容、保存、刷新后内容一致；截图 `rules-agent.png`。
2. 项目空间 rules 标签：项目根创建 FOREBRAIN.md 并保存；在一级子目录再建一个，列表出现两层。
3. `/permissions`：添加 allow 规则（工具=read_file，内容=`/tmp/e2e-*`），表格出现该行；"判定验证"输入 read_file + `/tmp/e2e/x`，explain 结果命中该规则。
4. 设置 → 审批默认：选 Full Access 保存；`GET forebrain.yaml`（经设置 → 配置文件标签）可见 `sandbox_mode: danger-full-access`；重新打开标签选中态正确。
5. 对话页：输入框旁下拉切 Read Only，随后触发一次写文件工具调用应出现审批请求（走既有审批流）；切回 Default 后同一调用直接通过。
6. 项目空间 perm 标签：未信任项目表单禁用；信任后添加 deny 规则成功。

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`。

## 完成标准（全部满足）

- [x] 第 1-5 步验证全部通过
- [x] `web e2e: PASS`（43/43，rules-permissions 6 用例）
- [x] 动作/工具/审批档位全部选择框；自由输入仅规则内容与判定内容
- [x] `git diff --stat -- pkg/tui` 仅含计划 005 的 PlanProgressOf 转发（本计划零触碰）
- [x] 均无输出
- [x] README 状态行已更新

## STOP 条件

- "现状"摘录与代码对不上（尤其 `PermissionUpdate` 字段、`destinationPath` 行为、`ApprovalPolicyConfig` 序列化语义）。
- `PermissionRuleValue` 不接受通配工具名而权限页需要"全部工具"选项——需要先决定是逐工具展开还是扩展引擎语义，交 owner 拍板。
- 会话级预设无法在 gateway 侧拿到该会话的运行配置（runner 未挂到 session）——报告实际挂接点，owner 拍板实现位置。
- 需要修改 `pkg/safety`、`pkg/home`、`pkg/assembly` 或"范围"之外的其他文件。

## 维护说明

- 规则文件白名单是产品决定：主代理三件套来自 `pkg/home/workspace_bootstrap.go:55`，项目层只有 `FOREBRAIN.md`。支持面变化时先改 `pkg/home`/`pkg/assembly`，再同步 gateway 白名单常量与前端下拉。
- 审批三档的 ID、Label、说明以 `safety.BuiltinApprovalPresets` 为唯一事实；前端文案是它的中文翻译，引擎改了说明必须同步。
- 会话级切换永不写盘；要改默认走设置页。
