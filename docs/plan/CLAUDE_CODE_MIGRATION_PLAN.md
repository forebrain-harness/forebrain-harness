# Claude Code → Forebrain Harness 全量迁移方案（第一期）

日期：2026-09-13（决策修订：2026-09-13；实施完成：2026-09-15；2026-09-17 修订实施完成：2026-09-17）
状态：已实现（阶段 A 见 `project-level-mcp.md`；阶段 B 落地为 `pkg/migrate` + TUI 独占 `/migrate`；2026-09-17 修订的三条——项目级配置迁移进 forebrain 自己的文件、两源共享的续聊不变量、`/migrate` 源目录输入框与 `--home`——随 `CODEX_MIGRATION_PLAN.md` 一并落地）
范围：**阶段 A** 先在 forebrain 内建成项目级 MCP 语义与功能（完整闭环）；**阶段 B** 再把本机 `~/.claude` 的历史会话、历史消息、skills、MCP、记忆迁入 forebrain，并新增 `/migrate` 命令 + 单选 picker。Codex 源在第二期按同一套 `Source` 接口接入。

**修订（2026-09-17，用户拍板）——本节优先于文档其余部分：**

1. **项目级配置一律迁进 forebrain 自己的项目配置文件，forebrain 不读取 Claude Code 的项目级配置文件。** 因此：
   - §1.2 的「~~原生读取 `.mcp.json`~~ 已移入 scope」与 §3 表格里「还原生读取 `.mcp.json`」、§4.7 的「仓库 `.mcp.json` 不迁」、§10 决策 3 相应条目**全部作废**；
   - `<repo>/.mcp.json` 改为**迁移**进 `<repo>/.forebrain/mcp_servers.yaml`；
   - `<repo>/.claude/settings.json` 与 `settings.local.json` 的 `permissions.allow/deny/ask` 改为**迁移**进 `<repo>/.forebrain/safety.json`；
   - `pkg/mcp/project_config.go` 的 `ProjectMCPPaths` 删掉 `.mcp.json` 候选。
   完整规则、映射表与已知代价见 `CODEX_MIGRATION_PLAN.md` §4.9（那一节是两个源共用的，不要在这里再写一份）。
2. **迁移进来的会话必须能在 forebrain 里续聊**，不只是能回放。四条不变量与共享测试见 `CODEX_MIGRATION_PLAN.md` §1.2 —— Claude 源要先补齐并验证通过。
3. **`/migrate` 选源后必须提供源目录输入框**（非必填，留空用默认 `~/.claude`），见 `CODEX_MIGRATION_PLAN.md` §6.2。

实施期修订（2026-09-15，用户拍板）：
- **plans/ 纳入迁移**：`~/.claude/plans/*.md` 全量迁入 `<workspaceRoot>/plans/<ProjectKey>/`，按「plan 文件名（会话 slug）→ 会话 jsonl 的 slug/cwd → ProjectKey」归属；§1.2 原将 `plans/` 排除的决定作废。
- **AskUserQuestion 映射更正**：对应 forebrain 的 `user_interaction`（§4.3 原表的 `request_permissions` 作废）。

方案中的路径、数量、字段名全部来自本机实测，不是估计值。

---

## 0. 本次审计结论（2026-09-13）

对本文档逐条核对了代码与本机数据，发现 **12 个必须修正的问题**，已就地改进文中相应章节。按影响排序：

| # | 严重度 | 问题 | 证据 |
|---|---|---|---|
| B1 | 阻断 | §4.2 把压缩摘要映射成 `role=system` 行。`pkg/state/message_sync.go:161 storedTranscriptMessages` 在重建模型上下文时**丢弃所有 system 行与边界行**，`appendMessage:86` 也拒收 system 行 —— 迁移进来的长会话续聊时**看不到自己的摘要**，「可续聊」这条验收会以一种不报错的方式失败 | `message_sync.go:86,161`、`compact_parts.go` |
| B2 | 阻断 | 压缩边界的目标结构写错了：forebrain 的边界是 `PartTypeContextCompactBoundary`（`context_compact_boundary`），其 `ReplacementHistory` 才是权威历史；文中用的 `PartTypeCompaction`（`compaction`）是另一个部件。且 `CompactBoundaryPart` 必填 `WindowNumber`/`FirstWindowID`/`WindowID`，源数据里没有，必须合成 | `pkg/state/compact_parts.go:10,31-43` |
| B3 | 正确性 | `compact_boundary_id` 是 **TEXT** 列（`DEFAULT ''`），文中写「无法解析时留 `0`」；另外迁移会话还需要 `initial_window_id`（`EnsureInitialWindowID`），否则压缩簿记不自洽 | `schema.sql` `fb_sessions`、`session_store.go:845,1480` |
| B4 | 阻断·验收 | §1.1 与 §2.1 把活数据的**瞬时快照当作验收标准**。今天重新实测：主会话 **99**（非 108）、体积 **178.2 MB**（非 199 MB）、输入历史 **925 条**（非 917）、记忆 **46 个**（非 45）。Claude Code 会自行清理历史，写死数字的验收必然失败 | 本机重新统计 |
| B5 | 语义缺口 | §4.8 规定项目路径「以 `~/.claude.json` 的 `projects` 键为准，不要反解目录名」。实测 7 个编码目录**全部带 memory/**，其中 **2 个在 `~/.claude.json` 里没有对应键** —— 按现规则这两个项目的记忆无处安放 | `~/.claude.json` vs `~/.claude/projects/` |
| B6 | 事实错误 | §2.1/§4.7 称「仓库 `.mcp.json`（`code-review-graph`）」。**本仓库没有 `.mcp.json`**；该文件在 `/Users/doudou/workspace/hundsun/guangfa/`，而该目录既不是 git 仓库也未被信任 | `git ls-files`、`find` |
| B7 | 事实错误 | §4.7 称 `code-review-graph`「列在 `enabledMcpjsonServers`」。实测该键为**空数组** —— 在源工具里它从未被批准启用。把未批准的条目迁成已启用条目，等于替用户做了一个它自己没做过的授权决定 | `~/.claude.json` |
| B8 | 架构 | §9 步骤 9 要在迁移 worker 内直接调 `Runner.ConsolidateMemoriesNow`。`pkg/migrate` 定为 layer 2，`pkg/run` 是 layer 3 —— **层违规**，`TestLayerCeiling` 会挂。合并必须由组合根以回调注入 | `pkg/architecture/cache_test.go:336` |
| B9 | 语义缺口 | 迁移会话没有**来源标记**。原生 TUI 会话 id 就是 `cli-<uuid>`（`state.NewID("cli")`），与 `cli-<claudeSessionId>` 形状完全一致 —— 迁移结果不可识别、不可回滚、报告也列不出来 | `pkg/state/id.go:68`、`pkg/tui/run.go:119` |
| B10 | 语义缺口 | 幂等只考虑了同一 primary agent。`Ensure` 的 `ON CONFLICT ... WHERE fb_sessions.agent_id=excluded.agent_id` 在会话已属于**另一个** primary agent 时静默不更新（`RowsAffected=0`），迁移会当成功继续写消息，而 `requireOwned` 又会拒读 | `session_store.go:101-113,131-149` |
| B11 | 一致性/边界 | 两处自相矛盾：§1.2 说「gateway/webchat 只回一行说明」，§5 却把 `AllowedSurfaces` 设成只有 `SurfaceTUI`。**已按决策定为：`/migrate` 是 TUI 独有，完全不暴露给 gateway 与 web**，§1.2 那句删掉。同时发现 `AllowedSurfaces` 本身**不构成边界**——`pkg/gateway/server.go:2209-2230` 把客户端传来的 `channel="tui"` 直接映射成 `turn.SurfaceTUI`，只靠 surface 列表挡不住 HTTP 客户端。必须再加一层：handler 接口只由 TUI 实现（`/mcp auth` 的既有做法），gateway 侧永不注册 | `pkg/turn/model.go:115-166`、`pkg/gateway/server.go:2209-2230` |
| B12 | 性能 | 最大单会话 39 MB，`printSessionResumeContext` 一次性取全量并在一个批次里交给 reducer 回放。自动压缩管的是**模型上下文**，管不了**渲染**：打开这种会话会长时间卡住。**已决策：完整历史必须可滚，只能改渲染方式、不能少渲染**（§7） | `pkg/tui/commands.go:69`、`pkg/tui/chat_surface.go:626` |

另有一条结构性问题：**§3（阶段 A）已被独立的可执行计划取代并在多处推翻**，两份文档不能并存两套规范。§3 现已改为指向那份文档，并列出被推翻的结论。

---

## 1. 目标、边界与完成定义

### 1.1 必须达到的产品结果

1. `/migrate` 打开 picker：**单选**列出检测到已安装的迁移源（Claude Code / Codex），默认高亮第一项；本机两个源都没装时不给空列表，直接提示"没有可迁移的 agent"。一次只迁移一个源。
2. 迁移完成后：

   | 能力 | 验收方式（相对断言，不写死数量——源端数据会自行变化，见 B4） |
   |---|---|
   | 历史会话 | 干跑报出的主会话数 == 实际写入数 + skipped 数；`/resume` 能按标题找到它们，标题取自源端 `ai-title` |
   | 历史消息 | resume 任一迁移会话，用户/助手/思考/工具调用与结果/图片完整回放，且顺序与源 jsonl 行序一致 |
   | 可续聊 | 在迁移会话里直接发下一条消息，上下文成立；**被压缩过的会话必须能看到自己的摘要**（B1/B2 的验收点）；超长会话由既有自动压缩兜底 |
   | 子代理 | 每个 `subagents/agent-*.jsonl` 都有一个 child session 可 resume 查看 |
   | Skills | `/skills` 能看到并加载已启用插件带来的 skill |
   | MCP | `/mcp` 按作用域列出迁入的条目并能实际连接；未通过门控或未确认的条目显示原因而不是消失 |
   | 记忆 | 进入对应项目作用域可 `/memories` 查看，迁移后已自动合并，后续会话能召回；**没有任何来源目录因为解析不出项目路径而被丢弃**（B5 的验收点） |
   | 输入历史 | 方向键上翻能翻到源端历史输入；新增条数 + 跳过条数 == 源文件行数 |
   | 可识别 | 迁移产物带来源标记，报告能把它们逐条列出（B9） |

3. 幂等：重复执行不产生重复数据，已迁条目计入 `skipped`。
4. 失败不脏库：单个会话失败只影响该会话并出现在报告里，已成功部分保留。
5. 迁移会话默认 `memory_mode=disabled`（决策 2）：199 MB 历史不进记忆管道，不烧 token、不污染 `MEMORY.md`。

### 1.2 NOT in scope

- Codex 源（第二期）。本期只落地 `Source` 接口 + picker 中的占位/禁用项。
- **`/migrate` 是 TUI 独有的命令**，不暴露给 gateway 与 web：不在它们的命令列表里出现，也无法从它们执行（见 §5 的两层保证）。
- ~~原生读取 `.mcp.json`~~ —— 已移入 scope：阶段 A 现在原生读取它，仓库里已有的该文件不再复制（§3、§4.7）。
- 主会话内 subagent 卡片的合成（见 §4.4 已知限制）。
- `file-history/`、`shell-snapshots/`、`session-env/`、`tasks/`、`jobs/`、`telemetry/`、`backups/`、`daemon/`、`downloads/`、`paste-cache/`、`stats-cache.json`：运行态与内部状态，forebrain 无对应语义。（~~`plans/`~~ 已于 2026-09-15 纳入 scope，见文首修订。）
- 仓库里的第三方指令文件（forebrain 只读 `AGENTS.md`/`FOREBRAIN.md`）——相邻问题，见 §7。

---

## 2. 实测现状

### 2.1 Claude Code 侧（`~/.claude`）

> **这是 2026-09-13 的快照，不是恒定值。** 源端会自行清理历史：同一天内主会话从 108 降到 99、体积从 199 MB 降到 178.2 MB、输入历史 917 → 925、记忆 45 → 46。下表用于估算规模与设计映射，**不得**被任何验收条件直接引用（见 B4）。


| 数据 | 位置 | 量级 |
|---|---|---|
| 主会话 | `projects/<encoded>/<sessionId>.jsonl` | 99 个（forebrain 85、chatibsagent-openclaw 5、superchat-docker 4、guangfa-openclaw 3、chatibs-audit-agent 1、chatibs-rag 1；另有 1 个项目目录只有 memory/、没有任何会话） |
| 子代理 | `projects/<encoded>/<sessionId>/subagents/agent-*.jsonl` + `.meta.json` | 23 个 |
| 体积 | `projects/` | 178.2 MB（单文件最大 39 MB） |
| 可见消息 | `user` / `assistant` / `system` / `attachment` 记录 | 65 359 条 |
| content block | `thinking` 8528、`tool_use` 17928、`tool_result` 17928、`text` 3927、`image` 73 | — |
| 工具调用分布 | Bash 14307、Edit 1539、Read 1537、Write 264、TaskUpdate 54、`mcp__codegraph__codegraph_explore` 45、ToolSearch 36、TaskCreate 32、AskUserQuestion 29、Agent 22、SendUserFile 16、WebSearch 13、WebFetch 12、Skill 8（共 22 种） | — |
| 大工具结果 | `tool_result.content` > 4000 字符 1817 条；引用 `tool-results/*.txt` 侧车 41 处 | — |
| 记忆 | `projects/<encoded>/memory/*.md`（frontmatter `name`/`description`/`metadata{node_type,pinned,originSessionId,modified}`） | 46 个，分布在**全部 7 个**项目目录下；其中 2 个目录在 `~/.claude.json` 里没有对应键（见 B5） |
| 输入历史 | `~/.claude/history.jsonl`（`display`/`timestamp`/`project`/`sessionId`） | 925 条 |
| MCP | `~/.claude.json` 顶层 `mcpServers`（`codegraph`，stdio）；`projects[<path>].mcpServers`（`caveman-shrink`，npx，出现在 2 个键下）；`/Users/doudou/workspace/hundsun/guangfa/.mcp.json`（`code-review-graph`，uvx，**不在本仓库**，且该目录非 git、未被信任）。`enabledMcpjsonServers`/`disabledMcpjsonServers` **两处均为空数组** | 用户级 1 + 每项目 1 + 独立项目文件 1（全部未批准） |
| Skills | `<repo>/.claude/skills/run-forebrain`；`~/.claude/skills` **不存在**；插件 `frontend-design@claude-plugins-official` 已启用（`plugins/cache/.../skills/frontend-design`） | 项目级 1 + 插件 1 |

其他已确认的格式事实（决定映射怎么写）：

- 记录类型共 17 种：`mode`/`permission-mode`/`atis-latch`/`bridge-session`/`file-history-snapshot`/`file-history-delta`/`user`/`assistant`/`attachment`/`system`/`ai-title`/`last-prompt`/`cost-state`/`queue-operation`/`agent-name`/`fork-context-ref`/`frame-link`。
- `user` 行：`message.content` 可能是字符串（932 例）或 block 数组；工具结果行额外带 `toolUseResult{stdout,stderr,interrupted,isImage,noOutputExpected}` 与 `sourceToolAssistantUUID`。
- `assistant` 行：`message.model` / `message.usage` / `requestId` / `effort`；`thinking` block 带 `signature`。
- 压缩：`system` + `subtype=compact_boundary`（`compactMetadata{trigger,preTokens,postTokens,durationMs,preservedSegment{headUuid,anchorUuid,tailUuid}}`），紧随其后是 `user` 行且 `isCompactSummary=true`，正文是续写摘要。
- 会话级元数据：`ai-title.aiTitle`（最后一次生效）、`cost-state`（`totalCostUSD`/`modelUsage`）、`cwd`/`gitBranch`（几乎每行都有）。

### 2.2 Forebrain Harness 侧落点

| 类别 | 落点 |
|---|---|
| 会话 | `fb_sessions`（`$FOREBRAIN_HOME/state/forebrain.state.sqlite`，见 `pkg/state/schema.sql`） |
| 消息 | `fb_messages`；transcript 读取按 `source='transcript'` 过滤（`pkg/state/session_store.go`） |
| 回放 | `/resume` → `ListAllMessages` → `pkg/tui/commands.go:printSessionResumeContext` → Reducer 结构化回放 |
| 记忆 | `<agent workspace>/memories/projects/<ProjectKey>/extensions/ad_hoc/notes/<ts>-<slug>.md`；`ProjectKey` 编码规则与 Claude 目录命名完全一致（项目根绝对路径把 `/` 换成 `-`） |
| Skills | `pkg/skill/roots.go`：**已原生发现** `<repo>/.claude/skills`（信任门控）、`~/.claude/skills`、`~/.codex/skills`、`~/.agents/skills`；用户级安装目录 `~/.forebrain/skills/` |
| 输入历史 | `<agent workspace>/state/cli-input-history.txt`（每行一条，`\n` 归一为空格） |
| 命令 | 注册表 `pkg/turn/slash.go:73`；分发 `pkg/turn/executor.go:55`；TUI 面板分支 `pkg/tui/run.go:applySlashOutcome`、`pkg/tui/chat_surface.go` |
| Picker | `pkg/tui/channels.go` `Selector`（`Select`/`MultiSelect`/`Confirm`/`SelectRich`）+ 可选能力接口 `PagedRichSelector`/`MemorySettingsSelector`/`InfoOverlaySelector` |
| 记忆合并 | `pkg/memory` `Pipeline.Run(ctx, currentThreadID)`（stage1 + stage2，覆盖项目作用域与 global）；`pkg/run/memory_llm.go:236 Runner.LaunchMemoryStartup(sessionID)` 是异步入口，`memory.Enabled(cfg)` 为假时静默 no-op |

### 2.3 Forebrain Harness 的 MCP 现状与缺口（阶段 A 的根因）

实测结论：**forebrain 今天只有"用户级"一个 MCP 作用域，没有项目级语义。**

- 配置源唯一：`$FOREBRAIN_HOME/forebrain.yaml` → `agents.defaults.mcp_servers[]`（`pkg/config/agents.go:35`，也是 `/mcp`、`/status`、`forebrain.yaml` 示例里唯一的形状）。
- 装配点三处，都是"直接取全局列表"：
  - `pkg/process/open.go:239`：`h.Deps = run.Deps{... MCPServers: cfgRoot.Agents.Defaults.MCPServers ...}`；
  - `pkg/process/config_reload.go:226`：`h.Runner.MCPServers = next.Agents.Defaults.MCPServers`；
  - `pkg/tui/chat_session.go:1875`：`deps.MCPServers = loaded.Agents.Defaults.MCPServers`。
- 子代理经 `pkg/run/factory.go:163 ownerMCPServers()` 继承 owner（主 agent）的列表 → 项目级 MCP 若要"完整"，必须落在 owner 的那一份列表上，而不是另起一份。
- 启动：`pkg/run/runner.go:1085` 遍历 `r.MCPServers` 调 `mcp.Start`，失败只 `slog.Error` 后跳过。
- `/mcp`（`pkg/tui/chat_slash.go:1045`、`startMCPLocalOAuth:1060`）与 `/status`（`chat_slash.go:123`）读的都是 `cfg.Agents.Defaults.MCPServers` → 即使加了项目级，这两个面也看不见。
- 分层 settings 机制（`pkg/safety` 的 `SourceSettings`/`SettingsProject`）目前**只**用于权限 profile 选择，实测只产出 `SettingsLocal`（`LocalConfigSources`），没有任何项目级配置文件被读取 → 不能指望它现成承载项目级 MCP。
- 信任基础设施已具备：`safety.Resolve(cwd) → Project{Root, VersionControlled}`、`safety.TrustLevel(home, project)`、`safety.ResolveEffectiveConfig`（`pkg/process/open.go:204` 已经在用，返回 `launchProject` 含 `TrustLevel`），以及项目级 skills 的门控先例 `skill.TrustedProjectSkillRoots`（要求 `VersionControlled && IsTrusted`）。

---

## 3. 阶段 A：项目语义与项目级 MCP

阶段 A 的规范**不在本文档**，以可执行计划为准：

`~/.forebrain/workspace/plans/-Users-doudou-workspace-unionj-cloud-forebrain-harness/project-level-mcp.md`

本节原先自带一份规范，评审后已被那份计划在多处推翻，两份文档不能并存两套规则。被推翻的结论，迁移侧必须按新结论对接：

| 本文档原结论 | 现结论 | 对迁移的影响 |
|---|---|---|
| 项目文件随配置热重载即时生效 | **有效列表在会话内冻结，只在新会话生效** | 迁移写完 MCP 后**不能**声称"已生效"，报告须写"下次启动生效" |
| 只读 `.forebrain/mcp_servers.yaml` | 还**原生读取** `.mcp.json` | 源端仓库里的 `.mcp.json` **不必再复制一份**到 `.forebrain/mcp_servers.yaml`，否则产生双份漂移（与 skills 的处理方式一致） |
| 项目条目与全局共用 OAuth 凭据 | 凭据**按作用域隔离** | 迁移的项目条目不会继承全局同名 server 的令牌，报告须提示需要重新授权 |
| 仓库信任即可加载项目 server | 还需要**逐条确认**，且项目条目**不读宿主环境变量** | 迁入的条目首次启动时会逐条询问；带 `${ENV}` 的条目在项目作用域下解析规则不同，迁移报告必须标出来 |
| 项目条目沿用全局审批模式 | 项目作用域的审批模式**只能更严**，最宽到 `prompt` | 源端条目里更宽松的设置在迁移后会被收紧，报告须说明 |
| 新代码落在 `pkg/config` | 落在 `pkg/mcp`/`pkg/process` | 每包 20 个生产文件的上限已经吃紧，迁移侧新增文件也要先算预算（见 §9 步骤 8） |

§2.3 仍然有效，它记录的是阶段 A 的根因。

## 4. 阶段 B：Claude → forebrain 映射设计

### 4.1 会话 → `fb_sessions`

| 字段 | 取值 |
|---|---|
| `id` | `cli-<claudeSessionId>`（决策 1：已确认） |
| `agent_id` | 当前 primary agent（由 `SessionStore` 注入，与 tenant 边界一致） |
| `title` | 最后一个 `ai-title.aiTitle`；为空取首条用户文本前 40 字符；仍为空则用 `id`（**不要**写 `New Session`，那会破坏 `title==id` 哨兵语义） |
| `created_at` / `updated_at` | 首/末记录的 `timestamp`（Unix 秒） |
| `cwd` / `git_branch` | 记录里的 `cwd` / `gitBranch`（最后一次非空值） |
| `memory_mode` | `disabled`（决策 2：已确认） |
| `memory_source` | `tui` |
| `message_count` | 实际插入行数 |
| `prompt_tokens` / `completion_tokens` / `cost` | 由 `cost-state.modelUsage` 与各 `assistant.message.usage` 聚合 |
| `parent_session_id` | `''`；子代理会话填主会话 id |
| `compact_boundary_id` | **TEXT 列**：写该会话最后一条压缩边界行的 row id 的十进制字符串；无边界时留 `''`（不是 `0`，B3） |
| `initial_window_id` | 走 `EnsureInitialWindowID` 生成一个；边界部件里的 window id 链必须与它一致（B3） |
| 来源标记 | 迁移产物必须可识别（B9）：`fb_sessions` 增一列 `origin TEXT NOT NULL DEFAULT ''`，迁移写 `origin='migrated'`。本地状态可丢弃，直接改 `schema.sql` 写终态，不写迁移脚本 |

两条必须照顾到的既有行为：

- `Ensure()` 只在**插入**时写 `cwd`/`git_branch`/`memory_mode`（`ON CONFLICT` 只更新 `updated_at`），写的是 store 自己的启动值。所以迁移路径是 `Ensure(id, id)` → 一个 `UPDATE` 写回上表每一项；`source` 一律 `transcript`（`transcript` 才是回放查询读的值，`tui` 只用来触发聚合触发器）。
- `Ensure()` 的冲突子句带 `WHERE fb_sessions.agent_id=excluded.agent_id`：会话 id 已属于**另一个** primary agent 时它静默不更新（`RowsAffected=0`），随后 `requireOwned` 又会拒读。**必须检查 `RowsAffected`**，为 0 且该 id 已存在时记为 `owned-by-another-agent` 跳过并写进报告，而不是继续往里写消息（B10）。

### 4.2 消息 → `fb_messages`

| Claude 记录 | 条件 | 目标行 |
|---|---|---|
| `user` | `message.content` 为字符串 | `role=user`，`content=文本` |
| `user` | `content[]` 含 `text` / `image` | `role=user`，`content=文本拼接`，`parts=[{type:text}, {type:image,source:{type:base64,media_type,data}}…]` |
| `user` | `content[]` 含 `tool_result` | `role=tool`，`content=结果文本`（侧车文件内容内联），`tool_step_id=<tool_use_id>`，`parts=[{type:tool_result_meta,tool_call_id},{type:tool_display,body,summary,tool_meta_json}]` |
| `user` | `isCompactSummary=true` | **不单独成行**：作为摘要并入下面那条边界行的 `ReplacementHistory`（B1/B2） |
| `user` | `isMeta=true` | `parts` 追加 `{type:is_meta}`（回放时按既有语义隐藏） |
| `assistant` | `content[]` 含 `thinking` | `role=reasoning`，`content=思考文本`（丢弃 `signature`） |
| `assistant` | `content[]` 含 `text` | `role=assistant`，`content=文本` |
| `assistant` | `content[]` 含 `tool_use` | `role=assistant`，`content=''`，`parts=[{type:tool_calls,tool_calls:[{id,tool_use_id,type:function,function:{name:映射名,arguments:原始 JSON}}]}]`，`tool_step_id=<tool_use.id>` |
| `assistant` | `message.model` / `message.usage` | `model` 列 / `usage_json` 列 |
| `system` | `subtype=compact_boundary` | 一条**边界行**，`parts` 用 `EncodeCompactBoundaryPart(CompactBoundaryPart{...})`（类型 `context_compact_boundary`），详见下方「压缩边界怎么映射」 |
| `system` | 其他 subtype | 丢弃；`turn_duration.durationMs` 折进该回合 assistant 行的 `worked_duration_ms` |
| `attachment` | 任意 | 丢弃（Claude 上下文附件，非用户可见历史） |

共同字段：

- `created_at`：记录 `timestamp`（RFC3339 → Unix 秒）；缺失时取上一条 +1 秒，保证单调。
- `message_id`：Claude 记录的 `uuid`（溯源，也是去重键）。
- `finished_at`：该行 `timestamp`；`source`：`transcript`。
- 顺序：**jsonl 行序即插入序**，`fb_messages.id` 单调——forebrain 回放按结构序（row id）而非 wall clock 排序，与 Claude 行序天然一致。

**压缩边界怎么映射（B1/B2，本节最容易做错的地方）**

forebrain 的压缩不是「一条 system 分隔行 + 后面继续接旧行」。权威历史存在边界行 `parts` 里的 `CompactBoundaryPart.ReplacementHistory`：

- `pkg/state/message_sync.go:161 storedTranscriptMessages` 在重建模型上下文时**丢弃每一条 `role=system` 行和每一条边界行**；
- `pkg/state/session_store.go:714 listProjectedMessageRows` 则**只**按 `ReplacementHistory` 重建投影；
- `appendMessage` 还会主动拒绝把摘要写成独立行（`IsCompactSummaryMessage`），因为那会让摘要叠加。

所以映射必须是：源端的 `compact_boundary` 记录 + 紧随其后的 `isCompactSummary=true` 用户行 → **一条**边界行，其 `CompactBoundaryPart`：

- `Trigger` 取 `compactMetadata.trigger`；`Strategy`/`SummarySource` 标为迁移来源；
- `ReplacementHistory` = 一条带 `state.CompactSummaryPrefix` 前缀的用户消息（正文为源端摘要）+ `preservedSegment`（`headUuid`/`anchorUuid`/`tailUuid`）圈出的那段消息；
- `WindowNumber` 从 1 递增，`FirstWindowID`/`WindowID`/`PreviousWindowID` 由迁移合成并串成链（源端没有这三个值），`FirstWindowID` 与会话的 `initial_window_id` 一致；
- 会话的 `compact_boundary_id` 指向**最后一条**边界行的 row id。

`preTokens`/`postTokens`/`durationMs` 没有对应字段，放进边界行的可见正文即可，不要为它们臆造部件类型。

大工具结果：`tool_result.content` 引用 `tool-results/<id>.txt` 时读取该文件内联，避免结果缺失。图片：base64 走 `parts` 的 `image` 分支（`pkg/state/message_parts.go:332` 已支持）直接内联，完整性优先，报告给出体积统计。

### 4.3 工具名映射与 `tool_meta_json`

| Claude | Forebrain Harness |
|---|---|
| `Bash` | `shell` |
| `Read` | `read_file` |
| `Write` | `write_file` |
| `Edit` | `edit_file` |
| `WebFetch` | `web_fetch` |
| `WebSearch` | `web_search` |
| `TaskCreate`/`TaskUpdate`/`TaskList` | `session_todo` |
| `AskUserQuestion` | `user_interaction`（实施期更正；原 `request_permissions` 作废） |
| `Task`/`Agent` | `intermediate_tool` |
| `mcp__<server>__<tool>` | 原样保留 |
| 其他（ToolSearch、Skill、SendUserFile、ListAgents、EnterPlanMode…） | 原样保留（按纯文本工具卡显示） |

`tool_meta_json` 写 `tool.ToolMeta`：`tool_name`（映射后）、`status="completed"`、`invocation`（如 `git status`）、`input`（原始 arguments 解析）、子代理行附 `agent_id`。`tool_display.body` 优先取 `toolUseResult.stdout`/`stderr`，否则用 `tool_result` 文本。

### 4.4 子代理会话

- 每个 `subagents/agent-<id>.jsonl` → 子会话 `cli-<sessionId>-<agentId>`，`parent_session_id` 为主会话 id，标题取 `.meta.json`，`cwd` 继承主会话；行映射同上。
- **已知限制（必须写进报告）**：主会话回放里**不会**出现可点击的 subagent 卡片。forebrain 的卡片来自 `fb_session_events`（run 事件流），Claude 的 jsonl 不携带这类事件，无法无损重建。子代理会话本身可 resume、可读。若必须补卡片，作为第二期可选项（按 `fork-context-ref`/`Agent` tool_use 合成事件）。

### 4.5 输入历史

- 读 `~/.claude/history.jsonl`，按 `timestamp` 升序把 `display`（`\n` → 空格，与 `normalizeRawInputHistoryLine` 同规则）追加到 `<agent workspace>/state/cli-input-history.txt`。
- 已存在的行跳过（幂等）；报告给出新增/跳过条数。
- forebrain 的输入历史是**按 agent workspace 一份**（非按项目），而 Claude 的是全局 + `project` 字段：本期忽略 `project` 直接合并，保持"一份历史"的现状语义。

### 4.6 Skills

| 来源 | 处理 |
|---|---|
| `<repo>/.claude/skills/*` | **无需迁移**：forebrain 已原生发现（信任门控），拷贝反而产生双份漂移 |
| `~/.claude/skills/*` | **无需迁移**：同样已原生发现（本机不存在） |
| 已启用插件带的 skills（`settings.json.enabledPlugins`） | 迁移：从 `plugins/cache/<marketplace>/<plugin>/<version>/skills/<name>/` 复制到 `~/.forebrain/skills/<name>/`，复用 `skill.InstallFromDir` |
| `plugins/marketplaces/**` 下未启用插件的 skills（本机 20+ 个） | **不迁**（未启用即未使用，全量拷入会污染 `/skills`） |

幂等：目标已存在时比对内容，报告 `up-to-date` / `skipped` / `diverged`（与 `/skills` 的 memory-skill import 同一套语义）。

### 4.7 MCP（依赖阶段 A）

> **2026-09-17 修订**：本节原来写「仓库 `.mcp.json` 不迁、forebrain 原生读取」，已作废。现在 `.mcp.json` **要迁**，且 forebrain **不再读取**它。两个源共用的完整规则见 `CODEX_MIGRATION_PLAN.md` §4.9。

| 来源 | 作用域 | 目标 |
|---|---|---|
| `~/.claude.json` 顶层 `mcpServers`（`codegraph`） | 用户级 | `forebrain.yaml` → `agents.defaults.mcp_servers[]`（global） |
| 仓库 `.mcp.json` | 项目级（团队共享） | **迁**：转写进 `<project>/.forebrain/mcp_servers.yaml`；`pkg/mcp` 同步删掉 `.mcp.json` 候选，`migrate` 的 `nativeMCPNotes` 整个删除 |
| `~/.claude.json` → `projects[<path>].mcpServers`（`caveman-shrink`） | 源端是本机私有 | `<project>/.forebrain/mcp_servers.yaml`，报告注明"源端是本机私有、迁移后成为仓库级文件，若不想提交请加入 .gitignore" |
| `<repo>/.claude/settings.json` / `settings.local.json` 的 `permissions` | 项目级 | `<project>/.forebrain/safety.json` 的 `rules`，工具名走 §4.3 的映射表 |

字段映射：`type: stdio` → `transport: stdio` + `command`/`args`/`env`；`type: http|sse` → `transport: streamable_http|sse` + `url`/`headers`。`env` 原样写入，报告对值脱敏。`normalizeTransport` 本来就接受 `stdio`/`http`/`sse`，不需要额外的转换表。

**批准状态必须一起迁（B7）**：实测 `enabledMcpjsonServers` 与 `disabledMcpjsonServers` **两处均为空数组**——也就是说源端那个 `.mcp.json` 条目**从未被批准启用**。迁移不得把它变成一个已启用条目：那等于替用户做了一个它自己没做过的授权决定。规则：

- 源端在 enabled 列表里 → 迁移时同时写入阶段 A 的逐条确认记录，标为已确认；
- 源端在 disabled 列表里 → 跳过，不写进任何文件；
- **两个列表都没有它**（本机的情况）→ 写入/识别，但**不预置确认**，留给用户在首次启动时逐条确认。报告写明"需要你确认后才会启动"。

写入方式：global 走 `forebrain.yaml` 的只追加 patch（先例 `config.PatchMemory`，写前备份 `forebrain.yaml.bak-<UTC ts>`）；project 走新建/追加 `.forebrain/mcp_servers.yaml`。

**与全局条目的关系**（阶段 A：项目覆盖全局）：迁移期若发现同名项已在 `forebrain.yaml` 全局列表中，照常写入项目文件，并在报告里单列一行"全局 `<name>` 将被项目文件覆盖"。因为阶段 A 已把凭据按作用域隔离，还要补一句"该条目不会继承全局同名 server 的授权，需要重新授权"。

**报告必须写清楚的三件事**：

1. 写进去 ≠ 已生效。阶段 A 的有效列表在会话内冻结，迁移写完的条目**下次启动才生效**。
2. 门控没过的条目。`code-review-graph` 的来源目录 `/Users/doudou/workspace/hundsun/guangfa` 既不是 git 根也未被信任（已核对 `~/.forebrain/state/workspace_trust.json`）——它既不会被原生读取，也不会因为迁移而生效。报告要写"需先让它成为版本控制项目并信任该目录"，而不是让用户以为 `/mcp` 里没有是 bug。
3. 作用域变化。源端本机私有的条目迁移后落进仓库级文件。

### 4.8 记忆（含自动合并）

- 源：`projects/<encoded>/memory/*.md`（带 YAML frontmatter）。
- 目标作用域：`memory.ProjectKey(项目真实路径)` → `memories/projects/<key>/`。
- **项目真实路径怎么定（B5）**：编码目录名不可直接反解（`/`→`-` 有损，真实路径里本来就带 `-`）。按以下顺序取第一个成立的：
  1. `~/.claude.json` 的 `projects` 键里有对应项 → 用它；
  2. 该目录下任一 `*.jsonl` 记录里的 `cwd` 字段（§2.1 已确认几乎每行都有）→ 用它，这是最可靠的来源；
  3. 反解目录名时穷举所有 `-`/`/` 的切分候选，取**在文件系统上真实存在**的那一个；
  4. 仍然定不下来 → **不丢弃**，写进 global 作用域并在报告里单列该目录，让用户自己归位。
  实测 7 个编码目录全部带 `memory/`，其中 2 个没有 `~/.claude.json` 键——只靠规则 1 会丢掉这两个项目的全部记忆。
- 作用域归并：Claude 目录可能来自仓库子目录（本机 `chatibs-audit-agent`），而 forebrain 的 `ProjectKey` 走 `ProjectRoot`（git root）→ 按 forebrain 规则归并到仓库作用域（期望行为）。
- 文件名：`<YYYY-MM-DDTHH-MM-SS>-<slug>.md`（UTC；时间取 `metadata.modified`，缺失用文件 mtime；slug 取 frontmatter `name` 并规整为 `[a-z0-9-]{1,80}`），满足 `validateNoteFilename`。
- 正文：剥掉 frontmatter，写 `# <name>` + `description` + 原正文 + 一行溯源（`origin: claude-code memory <file> (session <originSessionId>)`）。
- 写入：`memory.NewScoped(projectRoot, globalRoot).AddAdHocNote(AdHocNoteScopeProject, filename, note)`，复用路径拼接、symlink 防御与重名拒绝（已存在 → 跳过，幂等）。
- 全局记忆（若存在 `~/.claude/CLAUDE.md`）→ global scope 的 ad-hoc note；本机不存在，作为分支实现。
- **自动合并（决策 4：已确认）**：迁移结束后对受影响作用域跑一次合并。pending notes 每次只注入最新 20 条 / 1200 tokens（`pkg/memory/instruction.go`），本机 45 条记忆否则短期不可召回。
  - 实现：迁移 worker 内同步调用一次合并（新增一个薄封装 `Runner.ConsolidateMemoriesNow(ctx, sessionID) error` → 取 `memPipeline` → `Pipeline.Run`），使报告能给出**真实结果**而不是"已触发"；进度条显示 `consolidating memories…`。
  - `memory.Enabled(cfg)` 为假时无法合并 → 报告明确写"N 条记忆已落盘、将在开启 memories 后合并"，不假装成功。

---

## 5. `/migrate` 命令与 picker 交互（单选）

```
/migrate
 ├─ [SelectRich] 迁移源（单选，默认高亮第一项）
 │    · 只列出"检测到已安装"的源：
 │        Claude Code  ~/.claude 存在且 projects/ 有 jsonl（或 memory/mcp/skills 任一）
 │        Codex        ~/.codex 存在（第二期，描述里标注，选中即提示第二期支持）
 │    · 检测到 0 个源 → 不打开空 picker，直接回：
 │        "没有可迁移的 agent：本机未检测到 Claude Code（~/.claude）或 Codex（~/.codex）。"
 ├─ [SelectRich] 会话范围（单选）：全部项目 / 仅当前项目
 ├─ [Confirm] 干跑预览（会话数、消息行数、DB 预估增量、记忆条数、MCP 条目
 │            （含各条目将写入 global 还是 project）、skill 数、输入历史条数、
 │            以及"写入后不会立即生效"的条目清单）
 └─ 执行 → 进度（含 consolidating 阶段）→ 报告（InfoOverlaySelector，缺失时回落 transcript 卡片）
```

- 迁移内容**不做多选**：一次选一个源、一次跑完全部类别（会话+消息、记忆、skills、MCP、输入历史），符合"必须完整迁移"。需要裁剪时用 inline 参数。
- 命令行参数（`SupportsInlineArgs: true`，`ArgumentHint: "[claude|codex] [--dry-run] [--only sessions,memory,skills,mcp,history] [--project]"`）：
  - `/migrate` — 打开 picker（默认路径）
  - `/migrate claude --dry-run` — 只出预览，不写任何东西
  - `/migrate --only sessions --project` — 只迁当前项目会话（脚本化/可重复）
- 命令属性：
  - `Description`: `migrate sessions, memories, skills, and MCP servers from another agent`
  - `AllowedSurfaces`: 只有 `SurfaceTUI`。
  - **两层保证**（B11）：光靠 `AllowedSurfaces` 挡不住——`slashExecutionSurfaceForChannel` 会把客户端传来的 `channel="tui"` 映射成 `SurfaceTUI`，一个 HTTP 客户端自称 tui 就绕过了 surface 过滤。所以第二层是**能力注入**：`execMigrate` 只通过一个 `MigrateSlashHandler` 接口工作，该接口**只由 `pkg/tui` 实现**，gateway 的 slash 上下文永不注册它；拿不到实现就返回"迁移只能在终端里执行"。这与 `/mcp auth` 只在终端可用的既有做法是同一套机制（`MCPSlashOptions.Auth` 为 nil 时连用法提示都不列出）。
  - `defaultCategory` → `session`；`defaultActionKind` → `open-panel`
  - `runDisallowedCommands["migrate"] = true`：迁移会大量写 DB 并改配置，不允许与在跑的回合并发（与 `compact` 同一理由）

---

## 6. 幂等、事务、性能、进度、失败

- **幂等键**：会话 `cli-<sourceSessionId>`；消息 `(session_id, message_id)`（按会话一次查完已存在的 `message_id`，`idx_fb_messages_message_id` 已存在）；记忆按目标文件名；skill 按目标目录内容；MCP 按 `name`（global）与文件内条目（project）；输入历史按行文本。
- **跨 primary agent 的幂等**：会话 id 全局唯一但会话按 `agent_id` 分租户。同一份源数据在第二个 primary agent 下再迁一次会撞 id，`Ensure` 静默不更新（B10）。必须显式检测并记为 `owned-by-another-agent`，报告单列。
- **可识别**：迁移产物写 `fb_sessions.origin='migrated'`（B9）。原生 TUI 会话 id 本来就是 `cli-<uuid>`，光看 id 分不出来；没有这一列，迁移结果既列不出也退不回。
- **事务**：每会话一个事务，每 500 行 flush；单会话失败回滚该会话、记入报告、继续下一个。DB 已是 WAL，无需调参。
- **内存**：全程流式解析（逐行 `json.Decoder`），不把 199 MB 读进内存；单会话峰值受最大单文件 39 MB 约束。
- **规模预估**：约 6.5 万可见行 + 元数据行；DB 增量预计 300–600 MB（含 73 张 base64 图片与 1817 条 >4000 字符的工具结果）。干跑给预估，执行后报实际。
- **进度**：每完成一个会话回调一次（`i/N` + 累计行数），TUI 节流 200 ms 刷新；迁移在后台 goroutine 跑，经既有 notify 通道回传，不阻塞事件循环。
- **失败分类**：坏行 → 跳过并计数；整会话失败 → 报告文件路径 + 错误；配置写入失败 → 中止 MCP 部分但保留会话结果（各部分独立提交）。

---

## 7. 风险与对策

| 风险 | 对策 |
|---|---|
| 项目级 MCP 引入新的执行面（仓库文件决定启动什么命令） | 与项目级 skills 同一门控（`VersionControlled && IsTrusted`）。因为决策 6 让项目**覆盖**全局，门控是前提而非可选项 |
| 项目文件悄悄替换用户已配好的全局 server | 覆盖发生在**同名**时且只整条替换；被覆盖的全局条目在 `/mcp` 单列一节、`/v1/mcp/servers` 带 `scope` 与覆盖状态；迁移报告预先告知"全局 X 将被项目覆盖" |
| `forebrain.yaml` 被 `yaml.Marshal` 重排 | 本机该文件 0 行注释，重排只影响格式；仍先备份 `forebrain.yaml.bak-<ts>`，且只追加不改既有条目 |
| 项目级 MCP 写进"没被信任/非 VCS"的目录 → 用户以为坏了 | 报告显式列"已写入但不会立即生效"的条目与原因（本机 `code-review-graph` 就是这种情况）；`/mcp` 也显示被门控的文件路径 |
| 项目文件把本机私有的 server 变成仓库级文件（可能被提交） | 报告注明来源与"若不想提交请加入 .gitignore"（本机 `caveman-shrink`） |
| 记忆"迁了但召不回" | 迁移末尾自动合并（§4.8）；memories 关闭时报告实情 |
| 自动合并耗时/失败 | 在迁移 worker 内同步执行并显示进度；失败只计入报告，不回滚已迁移数据 |
| 超长会话 resume 撑爆上下文 | 既有 `maybeAutoCompactBeforeAppend` 兜底；压缩边界按 §4.2 尽量还原 |
| DB 体积膨胀 | 干跑预估 + 结果报告实际增量；不额外做图片外置（完整性优先） |
| 历史含密钥（记忆/工具输出） | 只在本机落库；报告对 `env` 值与疑似 secret 脱敏；不写日志 |
| 压缩过的会话迁移后续聊丢摘要 | 按 §4.2 的边界映射写 `ReplacementHistory`，并在验收里专门跑一条"被压缩过的会话续聊能看到摘要"（B1/B2） |
| 39 MB 单会话 resume 卡死渲染 | **完整历史必须可滚**，这是既有承诺：`SurfaceTranscriptMessages` 用 `ListAllMessages(ctx, sid, 0)`（limit=0），注释写明 "Resume replay shows the user their full scrollable history"。所以截断、只渲染尾部、"向上滚动再加载"这几类方案一律不采用，给它加 limit 更是直接破坏承诺。可调的只有**怎么渲染**：把一次性的整批回放拆成分批并在批次之间让出事件循环（保持 composer 可响应、给出回放进度），渲染结果按 width + theme 缓存以免 resize 重算。落地前先测出 39 MB 会话的实际耗时与内存，再决定批大小（B12） |
| 源端数据在开发期间自行变化 | 验收一律写成相对断言（干跑数 == 写入数 + 跳过数），不写死数量（B4） |
| 迁入的 MCP 条目被误认为已启用 | 按源端批准状态迁移；两个列表都没有的条目不预置确认，留给首次启动逐条确认（B7） |
| 迁移结果无法识别或回滚 | `fb_sessions.origin='migrated'`，报告逐条列出（B9） |
| 相邻问题（**本期不修，仅记录**） | ① forebrain 不读仓库里的第三方指令文件（只读 `AGENTS.md`/`FOREBRAIN.md`）；② 主会话内 subagent 卡片依赖 run 事件，无法从 Claude 格式无损重建 |

---

## 8. 测试与验收

**阶段 A**：测试与验收清单见 `project-level-mcp.md` 的 Verification 一节（含该文档没有的缓存验证：前缀字节稳定性 golden、重载不动 tool table、命中率前后对比）。

**阶段 B**
- `pkg/migrate` 单元测试（夹具 `testdata/`）：含 `tool_use`/`tool_result`（含侧车引用）/`thinking`/`image`/`compact_boundary`/`isCompactSummary`/子代理的 jsonl，断言角色、`parts` JSON 字节、`tool_call_id` 配对、`tool_meta_json`、时间戳单调、会话聚合字段。
- 幂等测试：同一夹具连跑两次，第二次 `migrated=0 / skipped=N`，库内行数不变。
- 回放测试：迁移后用 `ListAllMessages` 断言顺序与角色序列，并走一遍既有 replay 测试路径。
- MCP patch 测试：只追加、不改既有、冲突不覆盖、备份生成。
- 记忆测试：frontmatter 剥离、文件名通过 `validateNoteFilename`、已存在跳过、合并触发与否（memories 开关两态）。
- 架构门禁：`make test`（含 `pkg/architecture` 全套）；文件名规则（≤2 下划线、测试与生产文件同名）。
- 压缩边界测试（B1/B2）：夹具含 `compact_boundary` + `isCompactSummary`，断言只产出**一条**边界行、`ReplacementHistory` 含带 `CompactSummaryPrefix` 的摘要、window id 链自洽、`compact_boundary_id` 是该行 row id 的字符串、`storedTranscriptMessages` 之后模型上下文里**能看到摘要**。
- 跨 agent 幂等测试（B10）：同一 id 已属于另一个 agent 时记为 `owned-by-another-agent` 并跳过，不写任何消息。
- 项目路径解析测试（B5）：`~/.claude.json` 无键的目录能经由 jsonl 的 `cwd` 正确归位；四条规则都定不下来时落 global 并出现在报告里。
- 真实数据验收：`/migrate --dry-run` → `/migrate` → `/resume` 打开**最大的那个会话（39 MB）**，计时并确认**能一路滚到最早一条消息**（B12：完整历史可滚是硬要求，不是尽力而为）→ 在一个**被压缩过**的会话里续一句，确认上下文含摘要 → `/skills` 看到已启用插件带来的 skill → `/mcp` 看到各作用域与"待确认/需下次启动生效"的提示 → 新会话试召回记忆。TUI 交互用 `run-forebrain` skill 驱动截图确认。

---

## 9. 实施步骤

### 阶段 A：项目语义与项目级 MCP（先做）

步骤清单见 `project-level-mcp.md` 的 Tasks 一节。阶段 B 只依赖它的两个产物：**MCP 有效列表的装配入口**（步骤 8）与**逐条确认的存储格式**（步骤 3）。

### 阶段 B：迁移

6. **[M]** `pkg/migrate` 骨架：`doc.go`、`source.go`（`Source` 接口 + registry + 安装检测）、`claude.go`（探测 + 流式解析）、`plan.go`（干跑）。
   - 依赖：阶段 A（仅 MCP 部分依赖）；其余不依赖
7. **[M]** 会话与消息落库（§4.1–§4.3 + `toolmap.go`），含幂等与事务。
   - 依赖：步骤 6
8. **[M]** 架构门禁同步：`packageLayer` 加 `"migrate": 2`、`sameLayerEdges["migrate"] = {"memory","skill"}`、`fanOutBudgets` 加项、`TestLowerLayersDoNotImportSurfaces` 清单加 `migrate`、`pkg/migrate/doc.go`（`TestEveryPackageHasDocGo`）；新包的生产文件数从一开始就按 **≤20** 规划。**注意 `pkg/turn` 与 `pkg/tui` 均已满 20 个生产文件**，步骤 12 的命令链路只能改既有文件，不能新增文件。`make test` 通过。
   - 依赖：步骤 7
9. **[M]** 记忆迁移（§4.8，含四级项目路径解析）+ 自动合并。
   **合并不能由 `pkg/migrate` 直接调用**（B8）：`pkg/migrate` 是 layer 2，`pkg/run` 是 layer 3，直接 import 会被 `TestLayerCeiling` 拦下。做法是 `pkg/migrate` 声明一个 `Consolidate func(ctx, sessionID) error` 字段，由组合根（`pkg/process`）把 `Runner.ConsolidateMemoriesNow` 注进去；为 nil 时报告写"记忆已落盘、未合并"。
   - 依赖：步骤 7
10. **[S]** MCP 迁移（§4.7）：写 global、写 project（仓库 `.mcp.json` 已原生读取，不复制）、迁移批准状态、"被项目覆盖的全局条目"清单、"写入但需下次启动生效"清单、"门控未过"清单、"需重新授权"清单。
    - 依赖：阶段 A 的装配入口与确认存储；步骤 6
11. **[S]** Skills（§4.6）、输入历史（§4.5）、子代理会话（§4.4）。
    - 依赖：步骤 7
12. **[M]** `/migrate` 命令链路（§5）：`pkg/turn` 注册与 `Result.SelectMigration`、`pkg/tui` 的 `handleMigrate` picker 流程（单选 + 空选项提示）、进度、报告。
    - 依赖：步骤 6–11
13. **[M]** 测试与真实数据验收（§8），补齐夹具与幂等/回放测试。
    - 依赖：步骤 12
14. **[S]** 文档：`FOREBRAIN.md` 的 `/migrate` 一行说明；本方案状态更新为"已实现"。
    - 依赖：步骤 13

---

## 10. 已确认决策

1. **会话 id**：`cli-<claudeSessionId>`。
2. **迁移会话的记忆开关**：默认 `memory_mode=disabled`。
3. **项目级 MCP**：先在 forebrain 内建成项目语义与项目级 MCP（完整闭环，见 `project-level-mcp.md`），再迁移（§4.7）。该文档在多处推翻了本文原 §3 的结论，以它为准。
4. **记忆合并**：迁移后自动跑一次 consolidation（§4.8）。
5. **picker**：默认高亮第一个选项；**只支持单选**；一次迁移一个源；本机未检测到任何可迁移源时，选项为空并提示"没有可迁移的 agent"。
6. **同名冲突**：**项目覆盖全局**，按 `name` 整条替换；被覆盖的全局条目仍在 `/mcp` 与迁移报告里可见；信任门控因此成为安全前提。细则以 `project-level-mcp.md` 为准（那里还加了凭据隔离、逐条确认与审批收紧三条，本文原 §3 没有）。
7. **生效时机**：项目级 MCP 的变更只在新会话生效。迁移报告不得声称"已生效"。
8. **批准状态**：源端未批准的 MCP 条目迁移后也不预置确认（B7）。

## 11. 已知取舍（非阻塞，无需现在决定）

- Claude 的 `~/.claude.json` 项目条目在 Claude 里是**本机私有**，本方案把它写进**仓库级** `.forebrain/mcp_servers.yaml`。迁移报告会注明来源与"若不想提交请加入 .gitignore"。若将来要在 forebrain 里也保留"本机私有 × 项目"这第三层语义，需要在主配置里加一个 per-project 段并定义它与项目文件的优先级——那会扩大阶段 A 的配置面，本期不做。
- ~~`enabledMcpjsonServers`/`disabledMcpjsonServers` 在 forebrain 没有对应开关~~ —— 已过时：阶段 A 加了逐条确认，这两个列表现在有明确落点（§4.7 的三条规则）。
