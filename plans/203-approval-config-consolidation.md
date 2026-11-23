# 203 — 审批与权限配置收口：审批预设取代审批策略与沙箱模式，固定三项、删复核人

- **基线**：`06f1bc57`（dev）。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / L / MED-HIGH（改审批与沙箱判定的输入，任何回归都直接表现为「该问的不问」或「不该问的问」）
- **依赖**：187、188、189 已落地（配置在状态库 `fb_config`，schema v24）。与 204–206 改同一批文件（`pkg/config/config.go`、`pkg/config/load.go`、`pkg/tui/config_panel_schema.go`、`pkg/process/config_db.go`），**串行执行**：203 → 204 → 205 → 206 → 207。
- **执行隔离**：基于 dev HEAD 的 worktree 或主树 in-tree；**零 commit**（owner 手动提交）。

> **执行者须知**：逐步执行，每步跑「验证」确认结果再进下一步。碰到 §8 STOP 条件就停下报告，不要自行发挥。完成后把 `plans/README.md` 里 203 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：`git diff --stat 06f1bc57 -- pkg/config pkg/safety/permission_types.go pkg/safety/sandbox_config.go pkg/safety/runtime.go pkg/run/permissions.go pkg/run/runner.go pkg/tool/shell_tool.go pkg/tool/registry.go pkg/gateway/api_extra.go`。有输出就逐条对照 §2 的摘录；对不上就 STOP。

## 0. 为什么做、owner 裁决（执行期不得改）

forebrain 面向 C 端（含不懂开发的白领），配置必须精简；能固定的行为就固定，不做开关。owner 2026-10-30～31 的裁决（全文在 `plans/191-web-config-ui.md` §8.5、§8.6、§8.10、§8.23）：

1. **`approval_policy`（审批策略）与 `sandbox_mode`（沙箱模式）彻底不再是配置项**——网页、终端、forebrain.yaml 都不能配。取而代之的唯一配置项是 **「审批预设」`approval_preset`**：`workspace_write`（默认）/ `full_access`（完全访问），**只存状态库 `fb_config`**，默认 `workspace_write`。
1a. **「只读」预设删除（owner 2026-10-31）**：无论有没有沙箱，审批预设只有「默认」「完全访问」两种；**删干净、清死代码**；**网页与终端一致**（设置页、网页会话选择器、终端 `/permissions` 都只有这两项）。注意：项目信任度未判定时把沙箱收紧到只读（`effectiveSandboxMode`）是**信任收紧**，不是预设，保留；`SandboxModeReadOnly` / `ProfileReadOnly` 因此仍在用。
2. **「默认」预设受项目信任度收紧**：未信任 / 未判定的项目照今天的推导收紧。
2b. **继承语义（owner 2026-10-31）**：设置页的审批预设＝**新建会话的默认审批模式**——建会话时把它拷贝进该会话的 `fb_session_permission_state` 行；之后用户在网页输入框的选择框（或终端 `/permissions`、shift+tab）改的只影响本会话；改设置页不影响已有会话。跑批会话不写这一行（预设对跑批无效，216）。
2a. **「完全访问」与 YOLO 等价，YOLO 只是它的别称（owner 2026-10-31）**：完全访问走 YOLO 的放行语义——除明确的**拒绝**规则外全部放行（「每次都问」规则也放行），**不受项目信任度限制**；`FOREBRAIN_YOLO` / `--yolo` 等于把有效预设强制为完全访问。照旧**只绕过权限审批**，不绕过必须由人拍板的事（退出计划、向用户提问；以及 194–201 将加的「同目录有其他代理时的危险 git」）。界面上一律叫「完全访问」。
3. **预设 id 三处一起改、统一下划线**：`auto`→`workspace_write`、`full-access`→`full_access`；`read-only` 随预设删除。三处＝会话级行 `fb_session_permission_state.preset`、`/api/permissions/session-preset` 与 `/api/permissions/approval-default` 的请求 / 应答、前端选项值。界面名不变。
4. **权限档案整块删除**：`permissions`（档案表）与 `default_permissions` 从配置结构删除；三个内置档案留作预设的内部实现。
5. **审批复核人 `approvals_reviewer`、自动复核策略 `auto_review` 配置删除**——「复核人」概念整体消失，审批就是交给人；**不引入任何「复核人」常量、不硬编码 `user`**。guardian（AI 复核）代码**只作为死代码留在仓库里**（不删）。**「本轮批准并严格自动复核」在网页和终端都不再出现**。
6. **命令改写（`tools.command_rewrite`）、命令执行审批（`features.exec_permission_approvals`）、权限申请工具（`features.request_permissions_tool`）删配置、固定开启**；跑批项目照旧不注册权限申请工具。
7. 「不考虑老用户」只意味着**不把旧值换算成新配置**；状态库里的旧行旧值必须用有版本号的迁移清理（owner 明确保留迁移规则）。本计划只迁移**会话级预设值**（第 3 条）；`fb_config` 里旧段行的清理由 207 统一做（解码是宽松的，见 §2.6，旧行在 207 之前无害）。
8. 计划与界面里**不提 Codex**；不显示英文配置键。

## 1. 交付

- 配置：新增 `Root.ApprovalPreset`（段 `approval_preset`，存 JSON 字符串）；`Root.ApprovalPolicy` / `Root.SandboxMode` 改为**运行时派生值**（`yaml:"-" json:"-"`，永不持久化）；删 `DefaultPermissions`、`Permissions`、`ApprovalsReviewer`、`AutoReview`、`Tools`、`Features.ExecPermissionApprovals`、`Features.RequestPermissionsTool`；`approval_policy.granular` 及其整条代码（只能由配置产生，删后不可达）。
- 判定：`safety.EffectiveConfig` 由「预设 + 信任度」派生审批模式与沙箱模式（§3.2 表）。
- 预设 id 改名、「只读」预设删除 + 会话级行的数据迁移。
- 设置页「审批默认」接口改为读写 `approval_preset` 一个段。
- `request_permissions` 的可选决定只剩三项；复核人分支删除；guardian 保留为死代码。
- 三项固定开启。
- TUI `/config` 描述符同步；前端选项值同步（只改值，不改界面）。

## 2. 现状事实（已对基线核实）

### 2.1 预设定义（`pkg/safety/permission_types.go:809-816`）

```go
const (
	PresetReadOnly   = "read-only"
	PresetDefault    = "auto"
	PresetFullAccess = "full-access"
)
```

`BuiltinApprovalPresets()`（同文件 841-865）：只读＝`ApprovalOnRequest`+`SandboxModeReadOnly`；默认＝`ApprovalOnRequest`+`SandboxModeWorkspaceWrite`；完全访问＝`ApprovalNever`+`SandboxModeDangerFullAccess`。`MatchApprovalPreset`（896-907）在 `cfg.DefaultPermissions` 非空时直接返回「不匹配」；`ApplyToConfig`（913-923）清 `DefaultPermissions` 再写 `SandboxMode`。注释里「mirror codex's builtin_approval_presets」须改写为不提外部产品。

### 2.2 不配置时的推导（`pkg/safety/sandbox_config.go:556-601`）

```go
func EffectiveConfig(cfg appcfg.Root, launch ProjectContext) appcfg.Root {
	cfg.ApprovalPolicy = effectiveApprovalPolicy(cfg.ApprovalPolicy, launch)
	if strings.TrimSpace(cfg.DefaultPermissions) != "" || cfg.SandboxMode != "" {
		return cfg
	}
	cfg.SandboxMode = effectiveSandboxMode(launch, detectPlatform(), cfg.Windows.Sandbox)
	return cfg
}
```

`effectiveSandboxMode`：信任已有定论（信任或拒绝）且非「Windows 未配沙箱」→ `workspace-write`，否则 `read-only`。`effectiveApprovalPolicy`：配置了就用；被拒绝信任 → `untrusted`；其余 → `on-request`。**今天配置的值不受信任度约束**（例如设置页存了 `danger-full-access` + `never`，在未信任项目里也照样生效）——本计划按裁决 2 收紧。

### 2.3 会话级预设（`pkg/run/permissions.go:956-1066`）

`injectSessionPermission` 每轮把 `fb_session_permission_state` 行应用到权限运行时；`full-access` 行只在 `launchProjectTrusted()` 时生效，否则按 `PresetDefault` 跑（行本身保留原值）。`presetStateFromRow` 同一道闸。

### 2.4 设置页「审批默认」（`pkg/gateway/api_extra.go:8066-8121`）

GET 用 `MatchApprovalPreset(mode, cfg)` 反推；PUT 依次写三段：`default_permissions` 清空、`sandbox_mode`、`approval_policy`。路由在 `api_extra.go:78-81`（另有 `/session-preset` 的 GET / POST）。

### 2.5 复核人与严格自动复核（`pkg/run/runner.go`）

- 554-597：`strictAutoReview` 分支（562-578）与 `autoReviewApprovalsEnabled` 分支（579-597）都调 `Guardian().Review`。
- 875-879：

```go
func (r *Runner) autoReviewApprovalsEnabled(policy safety.ApprovalPolicy) bool {
	if r == nil || r.AppCfg == nil || !strings.EqualFold(strings.TrimSpace(r.AppCfg.ApprovalsReviewer), "auto_review") {
		return false
	}
	return policy.Mode == safety.ApprovalOnRequest || policy.Mode == safety.ApprovalGranular
}
```

- guardian 客户端构造与 `SetGuardian`：`runner.go:1005-1016` 一带（`AutoReview.Policy` 作为附加策略传入）。
- `request_permissions` 的可选决定（`pkg/safety/permission_types.go:1017-1024`）：

```go
	case strings.EqualFold(strings.TrimSpace(actionKind), "request_permissions"):
		return []ApprovalDecisionOption{
			{Decision: DecisionGrantForTurn},
			{Decision: DecisionGrantForTurnStrictAutoReview},
			{Decision: DecisionGrantForSession},
			{Decision: DecisionDecline},
		}
```

- 显示端：TUI `pkg/tui/overlays.go:278`；网页 `frontend/src/lib/approvalSuggestions.ts:18,158`、`frontend/src/composables/usePendingActions.ts:498`。

### 2.6 配置解码是宽松的（`pkg/process/config_db.go:118-152`）

`composeSectionsFrom` 对每个 `fb_config` 行 `json.Unmarshal` 进对应 `Root` 字段：**多余字段被忽略**；行名不在 `rootConfigSectionFields` 里的直接跳过（`auth.*` 凭据行就是这样）。所以删字段 / 删段后旧行不会让启动失败，清理放到 207 的统一迁移。段表 `rootConfigSections`（`config_db.go:32-49`）与 `Root` 字段在 init 时互相校验，不一致即 panic——删 / 加段必须同步改这张表。

### 2.7 三个开关

- 命令改写：`ToolsSection` / `CommandRewriteSection` / `UseRewrite`（`pkg/config/config.go:205-226`）；读取点 `pkg/run/permissions.go:764`（`rewrittenShellPermissionInput`）、`pkg/tool/shell_tool.go:1979-1984`（`shellRewriteEnabled`）。
- 命令执行审批：`pkg/tool/shell_tool.go:1250-1258`——关着时 `with_additional_permissions` 直接报错，报错文案里含配置键。
- 权限申请工具：`pkg/tool/registry.go:161`（`requestPermissionsToolEnabled(rt) && !batchProject(rt)`）、`registry.go:209-211`；`shell_tool.go:1260-1262` `additionalPermissionsPreapproved` 的开关判断。
- 默认值：`EffectiveFeatures()`（`config.go:167-179`）里两项默认 `false`。

### 2.8 granular 只能由配置产生

`ApprovalPolicyGranular` / `ApprovalGranular` / `GranularApprovalConfig` 的读者：`pkg/config/approval_policy.go`（17 处）、`pkg/safety/runtime.go:460-471`、`pkg/run/runner.go:479,536-537,879,893`、`pkg/run/permissions.go:104`、`pkg/tool/shell_tool.go:1048-1049`、`pkg/safety/store.go:182,827`、`pkg/safety/engine.go:159`、`pkg/turn/permission.go:73`。

## 3. 设计

### 3.1 配置结构

```go
// pkg/config/config.go（Root 内）
ApprovalPreset ApprovalPresetID    `yaml:"approval_preset,omitempty" json:"approval_preset,omitempty"`
ApprovalPolicy ApprovalPolicyConfig `yaml:"-" json:"-"` // 运行时派生，永不持久化
SandboxMode    SandboxMode          `yaml:"-" json:"-"` // 同上
```

- `ApprovalPresetID` 是 `string` 类型，两个常量放在 `pkg/config`（`ApprovalPresetWorkspaceWrite = "workspace_write"`、`ApprovalPresetFullAccess = "full_access"`），`safety` 的 `PresetWorkspaceWrite` / `PresetFullAccess` 引用它们（`PresetDefault` 改名 `PresetWorkspaceWrite`，全仓替换；`PresetReadOnly` 删除）。
- 校验（`load.go` 的值域校验处，照 `approval_policy` 原来的写法）：空或两值之一（`read_only` 等其它值报错）；空在 `EffectiveConfig` 里按 `workspace_write` 处理（207 之后库里一定有值，空只出现在测试与过渡期）。
- `rootConfigSections`：删 `approval_policy`、`approvals_reviewer`、`auto_review`、`default_permissions`、`permissions`、`sandbox_mode`、`tools`；加 `approval_preset`。`windows` 由 206 删，本计划不动。

### 3.2 派生规则（`safety.EffectiveConfig`，唯一实现）

| 预设 \ 项目信任 | 已信任 | 已拒绝 | 未判定 |
|---|---|---|---|
| `workspace_write`（空同此） | on-request + workspace-write | untrusted + workspace-write | on-request + read-only |
| `full_access` | YOLO 放行 + danger-full-access | 同左 | 同左 |

- 「Windows 未配沙箱 → read-only」那一支本计划**照旧保留**（206 删）。
- `ResolveEffectiveConfig` 不变（信任查询失败照旧给只读）。
- **建会话即写会话行**：所有建会话的路径（网页新会话、终端启动 / `/new`、`/fork` 照旧复制原会话的行、定时任务、聊天渠道）在建会话的同一事务里写 `fb_session_permission_state(preset=当时的全局预设, source='default')`；跑批会话不写。数据迁移（与步骤 3 同一版本）：给已有的非跑批、且没有行的会话补一行 `workspace_write`、`source='default'`。运行时「没有行就跟随全局」的回退保留（防御用）。
- 会话级预设（2.3）照旧叠在其上，只换 id；**删掉会话级「完全访问只在受信任目录生效」的闸**（`pkg/run/permissions.go:960-968` `presetStateFromRow` 与 `1052-1056` `injectSessionPermission` 里把 `full-access` 收窄成默认的分支）。
- **完全访问＝YOLO 的实现**：权限运行时判定「是否按 YOLO 放行」的输入从「环境变量」扩为「环境变量 **或** 本会话有效预设为完全访问」——`applyRuntimePermissionOverrides(decision, yolo, …)`（`pkg/safety/runtime.go:557-571`）的 `yolo` 实参在所有调用处按这个口径计算（`Evaluate` / `Explain` 的调用点、`pkg/gateway/api_extra.go:917,949,8382` 等，以 `grep -rn "RuntimeYOLOEnabled()" pkg` 为准逐个改）；`dangerFullAccessAllowsDefaultDeny` 分支与函数删除（完全访问已走 YOLO 分支，它成了死代码）；`ApprovalNever` 把「每次都问」规则变成拒绝的转换对完全访问不再适用。`FOREBRAIN_YOLO` 的处理（`RuntimeYOLOEnabled`、`ApplyYOLO`、`recordYoloPreset`）改为「把有效预设强制为完全访问」。网关的提示文案（`pkg/gateway/http_server.go:699-706` 的「YOLO / danger-full-access is enabled…」）改成说「完全访问」，字段名可保留。

### 3.3 不可达代码

- granular：整条删除（2.8 全部读者；`approval_policy.go` 里 granular 的 YAML 编解码一并删；`ApprovalPolicyConfig` 只剩 `Mode`）。**先做 §4 步骤 1 的确认**。
- guardian：`autoReviewApprovalsEnabled` 及 579-597 分支删除；`strictAutoReview` 分支（562-578）与 `GuardianReviewer` 全部保留，加一行注释 `// auto_review 暂不使用（owner 2026-10-31），保留代码`；`SetGuardian` 接线保留，策略参数传空串。
- 权限档案：`pkg/config/permission_profiles.go` 里**用户档案表**（`PermissionProfiles` 类型、解析、合并、校验）删除；三个内置档案（`permission_profiles.go:499` 一带）若被预设实现使用则保留，否则一并删。`pkg/config/sandbox.go:31-44` `normalizeSandboxConfig` 的 `default_permissions` 分支删除。`pkg/tool/request_permissions.go`（17 处）、`pkg/safety/runtime.go`（19 处）读 `DefaultPermissions` / `Permissions` 的分支按「永远没有用户档案」化简。

## 4. 步骤

每步结束都跑：`CGO_ENABLED=1 go build -tags fts5 ./... && go vet -tags fts5 ./pkg/...`（期望：无输出）。

1. **确认 granular 不可由配置以外产生**：`grep -rn "ModeGranular\|ApprovalGranular\|\"granular\"" pkg --include='*.go' | grep -v _test`，逐个看来源。若有设置文件（`settings.json` 一类）或会话更新能把模式设成 granular → **STOP 报告**（那就不是死代码）。
2. **预设常量、id 改名、删「只读」**：按 3.1 加 `pkg/config` 常量；`safety` 常量引用它们；`PresetDefault` 全仓改名 `PresetWorkspaceWrite`；**删 `PresetReadOnly` 与 `BuiltinApprovalPresets()` 里的「Read Only」项**（`permission_types.go:841-848`）以及只为它存在的代码与测试；`ApprovalPresetByID` 对 `read_only` / `read-only` 返回不存在；注释去掉外部产品名。验证：`grep -rn "\"read-only\"\|\"full-access\"\|PresetDefault" pkg --include='*.go'` 只剩与预设无关的命中（如 `SandboxModeReadOnly = "read-only"` 是沙箱模式的值，**不改**）。
3. **会话级预设数据迁移**：在 `pkg/state/schema_migrations.go` 加 `migrateStateV<N-1>ToV<N>`（`N` = 执行时 `stateSchemaVersion+1`；照 `migrateStateV23ToV24` 的风格，单事务）：

   ```sql
   UPDATE fb_session_permission_state SET preset = CASE preset
     WHEN 'read-only' THEN 'workspace_write'
     WHEN 'auto' THEN 'workspace_write'
     WHEN 'full-access' THEN 'full_access'
     ELSE preset END;
   ```

   同步把 `stateSchemaVersion` 加 1、在 `stateSchemaMigrations` 末尾登记。测试：照 `pkg/state/schema_migrations_test.go` 里已有的「降版本夹具 → 迁移 → 断言」写法，插三行旧值 → 迁移后为 `workspace_write` / `workspace_write` / `full_access`（「只读」行改成默认、不删行——删行会让会话跟随全局预设，全局若是完全访问就等于放宽）；`TestStateMigrationMatchesFreshSchema` 照旧通过。
4. **配置结构**：按 3.1 改 `Root`；删 §1 列出的字段与类型；`rootConfigSections` 同步；`load.go` 删对应默认 / 校验 / 归一化（`load.go:104,128,132-135,627,900` 一带的 `approval_policy` / `approvals_reviewer` 处理），加 `approval_preset` 值域校验。编译错误逐个按 §3 化简，不要留「永远为假」的分支。
5. **派生**：按 3.2 重写 `EffectiveConfig`、`effectiveApprovalPolicy`、`effectiveSandboxMode`（后者签名保留 `windowsSandbox` 参数，206 删）。表驱动单测覆盖 2×3 六格 + 预设为空。
6. **设置页接口**：`handleApprovalDefaultGet` 返回 `cfg.ApprovalPreset`（空按 `workspace_write`）；`handleApprovalDefaultPut` 只写一个段：`s.withConfigSection(ctx, "approval_preset", func(cfg *config.Root) error { cfg.ApprovalPreset = …; return nil })`。`MatchApprovalPreset` 若再无读者就删除；`ApplyToConfig` 改为写 `ApprovalPreset`（若只剩 YOLO 一个读者，照 YOLO 的需要保留最小形态）。
7. **复核人删除 / strict 选项去掉**：按 3.3 处理 runner.go；`permission_types.go:1017-1024` 删 `DecisionGrantForTurnStrictAutoReview` 一行；后端收到该决定时按「不在可选项内」拒绝（找到决定校验处，确认它对不在列表里的决定返回错误；没有就加）。`DecisionGrantForTurnStrictAutoReview` 常量与 TUI / 网页的显示代码保留为死代码。
8. **三项固定开启**：删 `ToolsSection` 等；`rewrittenShellPermissionInput` 与 `shellRewriteEnabled` 去掉开关判断（恒改写）；`execPermissionApprovalsEnabled` 删除，`with_additional_permissions` 不再因开关报错；`requestPermissionsToolEnabled` 删除，`registry.go:161` 改为 `if !batchProject(rt) {`；`additionalPermissionsPreapproved` 去掉开关判断；`EffectiveFeatures` / `EffectiveFeaturesConfig` 删两字段。
9. **TUI `/config` 描述符**：`pkg/tui/config_panel_schema.go` 删被删字段的描述符，加「审批预设」（单选两项，中文名默认 / 完全访问，默认预选「默认」）。跑 `go test -tags fts5 ./pkg/tui/ -run 'ConfigPanel' -count=1`（覆盖测试会指出缺漏）。
9a. **完全访问＝YOLO**：按 3.2 末两条实现；删 `dangerFullAccessAllowsDefaultDeny`；删会话级信任闸。
10. **TUI `/permissions` 与网页会话选择器**：换 id，并**删掉「只读」选项**——终端 `/permissions`、网页会话选择器、设置页三处都只剩两项（网页与终端一致）；删 `frontend/src/locales/index.ts` 里只为「只读」存在的文案（`approval.readOnly`、`approval.readOnlyDescription` 等），改 `ApprovalPresetPicker.test.ts`。前端：`frontend/src/components/chat/ApprovalPresetPicker.vue:67-69`、`frontend/src/components/settings/SettingsApprovalTab.vue:68` 一带的 `value`；`grep -rn "'auto'\|'read-only'\|'full-access'" frontend/src` 逐个确认是否是预设 id。
11. **测试修整**：删 / 改依赖 `approvals_reviewer=auto_review`、strict 选项、granular、权限档案、三个开关的用例；guardian 自身单测保留。
12. **包图**：`scripts/package-graph.sh`，`git diff --stat pkg/architecture/testdata/graph.json` 只应有 LOC 变化。

## 5. 测试（新增 / 改写，放在被测文件同名的 `_test.go` 里——仓库约定）

- `pkg/safety/sandbox_config_test.go`：六格派生表 + 空预设。
- `pkg/state/schema_migrations_test.go`：预设值迁移（「只读」→ `workspace_write`）。
- `pkg/safety/permission_types_test.go`：`BuiltinApprovalPresets()` 恰为「默认」「完全访问」两项；`ApprovalPresetByID("read_only")` 不存在。
- `pkg/safety/runtime_test.go`：完全访问下「每次都问」规则命中的工具被放行、明确拒绝规则仍拒绝——与设置 `FOREBRAIN_YOLO=1` 时逐条结果相同（同一组用例跑两遍比对）；未信任 / 已拒绝目录里完全访问照样放行；`exit_plan_mode` 与用户提问类工具在完全访问下仍要人回答。
- `pkg/run`：会话行存 `full_access`、目录未信任 → 按完全访问执行（不再收窄）。
- `pkg/gateway`：`approval-default` 与 `session-preset` 收到 `read_only` 返回 4xx。
- TUI：`/permissions` 选择器恰两项；前端 vitest：会话选择器与设置页恰两项。
- `pkg/safety/permission_types_test.go`：`request_permissions` 的可选决定恰为「本轮批准 / 本会话批准 / 拒绝」三项。
- `pkg/tui`：`request_permissions` 审批浮层选项行没有严格自动复核（照 `overlays.go` 现有审批浮层测试加断言）。
- `frontend`：`approvalSuggestions` 对 `request_permissions` 不产出 `grant_for_turn_with_strict_auto_review`（vitest）。
- `pkg/gateway`：提交 `grant_for_turn_with_strict_auto_review` 返回 4xx；`PUT /api/permissions/approval-default` 只写 `approval_preset` 一行（断言 `fb_config` 里没有 `sandbox_mode` / `approval_policy` 行被写）。
- `pkg/tool`：`with_additional_permissions` 不再因开关报错；非跑批注册了 `request_permissions`、跑批没有；跑批里 `with_additional_permissions` 放不宽边界（`batchShellBoundary` 整体替换，`shell_tool.go:467-479`）。
- `pkg/run`：送审的 shell 命令一律是改写后的命令。

## 6. 完成判据（机器可查）

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/config/... ./pkg/safety/... ./pkg/run/... ./pkg/tool/... ./pkg/gateway/... ./pkg/tui/... ./pkg/state/... ./pkg/process/... -count=1 -p 2   # 全 ok
go vet -tags fts5 ./...                                                   # 无输出
grep -rn "ApprovalsReviewer\|AutoReview \|DefaultPermissions\|CommandRewrite\|ExecPermissionApprovals\|RequestPermissionsTool\|Granular\|PresetReadOnly" pkg --include='*.go' | grep -v _test   # 无输出（guardian 自身的类型名不在此列）
grep -rn "\"approval_policy\"\|\"sandbox_mode\"\|\"approvals_reviewer\"\|\"auto_review\"\|\"default_permissions\"" pkg/process/config_db.go   # 无输出
cd frontend && pnpm typecheck && pnpm test                                # 全绿
```

全量回归：`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -p 2`。

## 7. 维护注意

- 205 / 206 / 207 都会再改 `Root` 与段表；204 起每个计划开工前重跑 drift check。
- 行为变化（写进 README 结果）：①「完全访问」＝YOLO：不再受项目信任度限制，「每次都问」规则也放行；②工具列表多出 `request_permissions`（非跑批），提示词缓存前缀随之位移一次；③命令执行时可临时申请沙箱外权限（走审批）。
- 207 负责把 `fb_config` 里的旧段行删掉、`approval_preset` 写入默认值。

## 8. STOP 条件

- 步骤 1 发现 granular 有配置以外的来源。
- 派生改动后，`pkg/safety` / `pkg/run` 既有的审批测试有用例按新表**放宽**了（某个原本要问的现在不问）——列出用例停下。
- 网页或终端除选项值外还有地方硬编码了旧 id 且语义不明。
- guardian 的保留代码因删分支导致编译不过、必须删 guardian 本体才能通过。

**追加（owner 2026-10-31）**：设置页的配置项都是**全局默认值**，主代理或项目自己的配置可以覆盖全局默认值；审批预设按「新建会话时拷贝」实现（见 §0 第 2b 条）。
