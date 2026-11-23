# 190 — TUI 配置面板：/config slash 命令 + 分段浏览/编辑 UI

- **基线**：`6f68cb21`（dev）＋ **前置：187、188 已落地**（fb_config 写路径与 Store 可用）。189 不强依赖（面板只写库，不关心 watcher 死活），但建议在 189 之后执行，语义演示一致。
- **状态**：TODO
- **依赖**：187、188。

## 0. 背景

owner 要求：迁移到数据库的每个配置段，TUI 端必须有 slash 命令面板/选项/输入框等 UI 供查看与修改。本计划交付一个 `/config` 面板，覆盖 16 个 DB 段的浏览与编辑；forebrain.yaml 剩余段（gateway/credentials）在面板里**只读展示**并标注"手编文件 + 重启生效"。

生效提示（owner 裁决语义）：面板每次保存成功后显示一行 `已写入数据库，下一 turn 生效`——不做任何立即应用。

## 1. 现状先例（执行器照这些模式写，不另起炉灶）

| 需要什么 | 先例 | 位置 |
|---|---|---|
| 面板/选择器组件骨架 | `tabbed_picker.go`（tab 化选择面板） | `pkg/tui/tabbed_picker.go` |
| 版本/列表行渲染、滚动 | `skills_versions.go`（skills 面板的列表/详情形态，plans/184/185 刚修过，勿回退其 deadline/不等待 probe 语义） | `pkg/tui/skills_versions.go` |
| slash 命令注册与参数形态 | 现有命令表（`case` 分支约 7 处主形态；`/memories` 是"打开设置面板"型先例，`chat_slash.go:1370` 附近） | `pkg/tui/chat_slash.go` |
| 写路径 | `ConfigStore.UpdateSection`（188 之后唯一合法写配置入口） | `pkg/state/config_store.go` |
| 段校验 | `validateLoadedRoot` + `rejectPlaintextConfigSecrets`（Store 内置，面板只管把表单值 marshal 成段 JSON） | `pkg/config/load.go:110-172`、`pkg/config/secrets.go:13` |

## 2. 面板设计

### 2.1 入口

`/config`（无参数）打开面板；`/config <段名>` 直接进该段。Esc 关闭。面板数据来源：`composeConfig` 的合成 Root（面板打开时读一次；面板内不做后台轮询——保存后本地更新显示）。

### 2.2 结构（三层）

1. **段列表页**：16 个 DB 段 + 2 个 YAML 只读段（gateway、credentials，标 `[YAML]` 徽标）。每行：段名、当前是否非默认（`*`）、一句话说明（中文）。
2. **段编辑页**：按段类型渲染控件——
   - 布尔（`features.*`、`memories.*` 各 `*bool`、`hooks` 开关等）→ 选项行 `[开/关/默认]`（三态：nil 语义必须可表达，见 `pkg/config/config.go:297-299` BoolPtr 注释）；
   - 枚举（`approval_policy.mode`、`sandbox_mode`、`approvals_reviewer`…）→ picker，选项=校验函数里的合法值集合（`load.go:111-129`），不许自由输入；
   - 数字（`network_retry_max`、`search_top_k`、compact 阈值…）→ 输入框，校验边界照搬 `validateLoadedRoot` 的规则（负数拒绝、clamp 值就地显示 clamp 后结果）;
   - 自由文本/路径（`extract_model`、hooks 命令、`credentials` 只读除外）→ 输入框；
   - 列表/表段（`agents` 的 providers/子代理、`permissions` profiles、`mcp_servers`、`hooks` 事件表）→ 行列表 + 行内编辑 + 增删行。`agents` 段是最大的一段（`pkg/config/agents.go:13-128`：defaults + Definitions map + 每 primary 的 providers/subagents/mcp/default_model），按 primary 分小节渲染。**凭据字段＝明文文本输入**（owner 2026-10-30 裁决：provider 行的 `api_key`、MCP 行的 `headers.*` 与 `oauth` 的 client_secret/access_token/refresh_token 都是明文直存直显，值直接来自段 JSON，不强制也不特判 `${ENV}`——用户手写引用则按引用显示）。
3. **保存**：`ctrl+s` 或保存项 → 段 marshal → `UpdateSection` → 成功提示 `已写入数据库，下一 turn 生效`；失败（校验/明文秘密）→ 面板内错误行显示原始错误（不吞）。

### 2.3 只读段

gateway/credentials 段渲染为只读文本视图 + 固定尾行：`此段只能手动编辑 forebrain.yaml，重启进程后生效`。

## 3. 步骤

1. **段元数据表**（新 `pkg/tui/config_panel_schema.go`）：每段 → 字段描述符（kind: bool3/enum/number/text/section-list；enum 的取值来源函数；数字的边界）。纯数据+函数，无渲染。
   验证：单测——16 段全覆盖，描述符与 `Root` 字段一一对应（反射比对字段名集合，防止后续加段漏登记）。
   - （更正 2026-10-30：本面板已在 195 之前落地——代码在 `pkg/tui/config_panel*.go`——所以 `peers` 段的描述符改由 195 §3.7 加：`enabled`、`inbound`、`claude_code`「与 Claude Code 互通」；`claude_code` 只存数据库、默认关闭，且**只在检测到本机装了 Claude Code 时显示**（owner 裁决）。本计划不再负责这一段。）
2. **面板组件**：`pkg/tui/config_panel.go`，骨架抄 `tabbed_picker.go`；列表/详情滚动抄 `skills_versions.go`；中文文案。
   验证：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run TestConfigPanel -count=1`。
3. **slash 注册**：`/config` 进命令表（补全列表、帮助文案中文）。
   验证：`./pkg/tui` slash 相关既有测试不红。
4. **写路径接入**：session 暴露 `UpdateConfigSection(section string, raw json.RawMessage) error`（内部 `UpdateSection`）；保存提示行。
   验证：单测——写 `memories` 段后 `fb_config` 有值、forebrain.yaml 字节不变；非法值保存被拒且面板显示错误。
5. **真机验收（tmux + run-forebrain 驱动）**：面板开合、三态布尔切换、enum picker、agents 增删一个 provider、保存提示、Esc 关闭；`fb_config` 相应段变化；下个 turn 配置生效（例如改 `features.memories` 后 `/memories` 行为变化）。
6. **全量**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1`；`go vet ./...`。

## 4. 边界

- **不动**：既有 slash 的行为（/memories 等在 188 已写 DB，保留）；187/189 的同步与删除语义；`pkg/tui` 包边界（TUI 不得 import gateway/agentrun/clifacade/forkagent/supervisorrun——架构测试在 `pkg/architecture`，新文件只用 pkg/state、pkg/config、pkg/process 公共面）。
- **不做**：web 端（191）；面板内嵌 YAML 文件编辑器（明确拒绝——YAML 只能手编）。

## 5. 完成判据

1. 全量测试绿、`go vet` 无输出、包图无 diff。
2. 描述符覆盖测试绿（新增 Root 段不加描述符会红）。
3. 真机验收记录（tmux 截屏/按键序列存档）：§3.5 全项通过。
4. `forebrain.yaml` 在整个验收过程 mtime 不变。

## 6. 维护注意

- 段元数据表是唯一"加新段要记得改"的 TUI 侧清单，覆盖测试把它变成硬约束。
- 面板渲染长列表必须滚动不截断（owner 铁律）；禁渐变、禁粉色。

## 7. 逃生口

- 若 `agents` 段的行内编辑复杂度失控（providers 折叠/`replaces` 语义在 TUI 侧难以表达，见 `api_extra.go` W4 的合并算法）→ 允许首版 agents 段只读 + 提示"在 Web 端编辑"，其余 15 段可编辑；报告后由 owner 决定是否补。
- 若 `tabbed_picker` 骨架不适配三页结构 → 停，报告差异，等 owner 拍板新组件形态，不要私造大框架。

## 追加裁决（owner 2026-10-30，C 端精简）

`approval_policy`、`approvals_reviewer`、`auto_review`、`sandbox_mode`、`sandbox_workspace_write.go_cache_mode`、`sandbox_workspace_write.exclude_tmpdir_env_var`、`sandbox_workspace_write.exclude_slash_tmp`、`windows` **只在 forebrain.yaml 手配**，web 与 TUI 都不显示、程序不写、harness 只从 YAML 读；配置界面不出现英文配置键、不要「默认」选项（布尔两态、默认值预选）。对本计划的具体改动与待定项见 [191 §8.4](191-web-config-ui.md)——执行或续做本计划前先按它修订段集合。

**再追加（owner 2026-10-30，第三轮）**：`approval_policy` 与 `sandbox_mode` **彻底不再是配置项**（YAML 也不能配），由新增入库配置项「审批预设」（只读 / 默认 / 完全访问）取代，见 [191 §8.5](191-web-config-ui.md)；本计划的段集合、存量导入与描述符表须按它修订，含老用户迁移。

**第四轮（owner 2026-10-30）**：预设 id 统一 `read_only`/`workspace_write`/`full_access`；权限档案整块去掉；信任度只收紧；继续精简；「配置文件」编辑器保留并改写磁盘文件；状态库迁移规则保留——见 [191 §8.6](191-web-config-ui.md)。本计划受影响：描述符（删权限档案与两轮精简项，加「审批预设」，布尔两态）。

**配置来源定稿（owner 2026-10-31）**：原型保留的所有配置项只存数据库、不认文件；文件只放少数高级项——见 [191 §8.13](191-web-config-ui.md)。本计划受影响：描述符覆盖 191 §8.13「只存数据库」清单的全部项，白名单项不出现。

**追加（owner 2026-10-31）**：自动压缩阈值按百分比配置；存库配置项一律显示库里的值；初始化数据库时写入全部默认值——见 [191 §8.14](191-web-config-ui.md)。本计划受影响：面板显示库里存的值（库里一定有值），自动压缩阈值改为百分比 50–90。

**再精简（owner 2026-10-31）**：五项文件配置删除、改固定行为——见 [191 §8.24](191-web-config-ui.md)。本计划受影响：描述符与白名单里不再出现它们。

**再精简二（owner 2026-10-31）**：网络代理整块删除（所有系统）、Windows 沙箱与对外地址不再是配置——见 [191 §8.25](191-web-config-ui.md)。本计划受影响：`config_panel_schema.go` 删网络代理描述符；「允许联网」保留。
