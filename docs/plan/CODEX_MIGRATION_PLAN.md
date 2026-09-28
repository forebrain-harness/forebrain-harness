# Codex → Forebrain Harness 全量迁移方案（第二期）

日期：2026-09-17（实施完成：2026-09-17）
状态：已实现（`pkg/migrate` 的 codex.go/codex_parse.go/codex_assets.go + `/migrate` 的 codex 路径 + §4.9 项目级配置迁移 + §1.2 共享续聊不变量；§9.5 真实数据验收已于本机通过：80 会话 / 17 个压缩会话全部可续聊）
范围：把 `CODEX_HOME`（默认 `~/.codex`）的历史会话、历史消息、记忆、skills、MCP、plans、输入历史迁入 forebrain，落地 `/migrate` 的 **codex** 选项。迁移的类别与语义与 `CLAUDE_CODE_MIGRATION_PLAN.md` **逐项对齐**：Claude Code 迁什么，Codex 就迁什么；Claude Code 不迁什么，Codex 也不迁。

本期同时新增一条对**两个源都生效**的产品要求：**迁移过来的会话必须能在 forebrain 里续聊**（§1.2），以及 `/migrate` 选源后必须提供**源目录输入框**（§6.2）。

方案中的路径、数量、字段名全部来自 2026-09-17 本机实测，不是估计值。

---

## 0. 关键结论（先看这一节）

审计 `~/.codex` 的真实数据、Codex 二进制（0.153.4）内嵌的文档与提示词、官方配置文档，以及 forebrain 现有代码后，有 10 条结论直接决定了设计。按重要性排序。

> **方法论提醒（本方案曾经犯过的错）**：`~/.codex` 里没有某类数据，**不等于** Codex 没有这个功能。C8/C9/C10 三条最初就是从「本机没有」错误地推成「Codex 不支持」的；纠正它们靠的是 Codex 二进制里的内嵌文档与官方 `config-reference`，不是本机文件。凡是结论形如「Codex 没有 X」，都必须给出**来自 Codex 本体或官方文档**的证据，而不是一条 `find` 的空输出。

| # | 结论 | 证据 |
|---|---|---|
| C1 | **压缩摘要没有丢，也不需要丢。** Codex 的 `compacted.replacement_history` 里那条 `{type:"compaction", id, encrypted_content}` 正是 forebrain `llm.CompactionState` 承载的「不透明 Responses 压缩项」，注释明写 *EncryptedContent must be replayed verbatim*。原样搬运即可，续聊时由服务端解密；非 Responses provider 会忽略它，退化成明文保留段，两种情况续聊都成立 | `pkg/llm/llm.go:54-62`、`pkg/state/message_parts.go` `parseCompactionState`、`pkg/state/session_store.go` `listProjectedMessageRows` 的 `msg.Compaction != nil` 分支 |
| C2 | **窗口链是源端真值，不需要合成。** Claude 侧必须自己造 `WindowNumber`/`FirstWindowID`/`WindowID`（原方案 B2/B3）；Codex 的 `compacted` 记录 75/75 条自带 `window_number`、`window_id`、`previous_window_id`、`first_window_id`，`session_meta.context_window.window_id` 就是 `initial_window_id` | `compacted` payload keys 实测 |
| C3 | **思考内容基本不可迁，而且丢弃是正确的。** 10358 条 `reasoning` 里 10163 条既无 `summary` 也无明文，只有 `encrypted_content`；`item_completed/Reasoning` 的 `summary_text`/`raw_content` 1746 条全为空。同时 `state.ParseMessage` 对 `role=reasoning` 走 default 分支返回 `ok=false` —— **reasoning 行根本不进模型上下文**，所以把加密块搬进来既帮不到续聊，又是纯粹的死重量。明文来源只有 `event_msg.agent_reasoning`（268 条）与 195 条非空 `summary` | `pkg/state/message_parts.go` `ParseMessage`、本机统计 |
| C4 | **一个 rollout 里有两条并行的历史流，只能取一条。** `response_item`（模型侧线，81 个会话全有）与 `event_msg`/`item_completed`（UI 侧线，只有 19 个 `paginated` 会话有）。legacy 会话里 `agent_message` 与 `response_item message role=assistant` 条数**完全相等**（逐文件核对），同时渲染就是双份 | 逐文件 `ic=/resp_assistant=/ev_agent_message=` 对比 |
| C5 | **项目归属是一个字段，不是四级推断。** Claude 侧要靠 `~/.claude.json` → `cwd` → 反解目录名 → global 四级兜底（原方案 B5）；Codex 的 `session_meta.cwd` 81/81 条都有，`turn_context.cwd` 还会逐回合复述。四级解析在 Codex 侧退化成一级 | `session_meta` keys 实测 |
| C6 | **子代理是父子线程，而不是嵌套文件。** Claude 是 `<session>/subagents/agent-*.jsonl`；Codex 是 `state_5.sqlite` 的 `thread_spawn_edges(parent_thread_id, child_thread_id)`，子线程本身就是一个普通 rollout 文件。本机该表 0 行，所以这条只能靠夹具测试验证 | `state_5.sqlite` schema 与行数 |
| C7 | **记忆在 SQLite 里，且本机为空。** Codex 记忆是 `memories_1.sqlite` 的 `stage1_outputs(thread_id, raw_memory, rollout_summary, rollout_slug, generated_at)`，本机 **0 行**。全局指令文件 `~/.codex/AGENTS.md` **0 字节**。映射必须设计，但本机跑出来就是 0，验收只能靠夹具 | `memories_1.sqlite` 实测 |
| C8 | **Codex 有 plan 语义，但没有 plan 文件。** Plan Mode 是文档化的协作模式（配置键 `plan_mode_reasoning_effort`），产物是助手消息里的 `<proposed_plan>…</proposed_plan>` 块；`update_plan` 工具（5 次）是回合内 TODO。`~/.codex` 下没有 `plans/` 目录，`thread_artifacts` 也没有 plan 类型。所以迁移实现是**从迁入的助手消息里抽取 `<proposed_plan>` 块，写进 forebrain 的 plan 存储**（§4.10）—— 同一份计划资产换一个存储形态，属于迁移而非合成 | 二进制内嵌的 Plan Mode 提示词（`# Plan Mode (Conversational)`）、`thread_artifacts` schema |
| C9 | **Codex 有项目级 MCP。** 官方文档：*You can also add project-scoped overrides in `.codex/config.toml` files*，*Codex loads project-scoped config files only when you trust the project*，其中 `[mcp_servers.<id>]` **可用**。项目根由 `project_root_markers = [".git"]` 决定，即 `<project_root>/.codex/config.toml`。本机 0 个项目建了这个文件，但这不改变语义存在 | `learn.chatgpt.com/docs/config-file/config-reference`；二进制里的 `Project .codex/config.toml: settings for a trusted repository, including sandbox, MCP, hooks, model, and reasoning defaults`、`project_root_markers = [".git"]`、`Failed to read project config file`、`Overridden by project config:` |
| C10 | **Codex 不原生读仓库 `.mcp.json`。** 文档只在 `config.toml` 的 `[mcp_servers]` 下定义 MCP；二进制里的 `.mcp.json` 只出现在两处 —— 插件清单（`.codex-plugin/plugin.json` 旁的可选 `.mcp.json`）与 `external-agent-migration` 模块（Codex 自己从 Claude Code/Cursor 导入时读对方的文件）。所以「仓库 MCP 文件」这一类在 Codex 源下确实为空，理由是**该源不产生这种文件**，不是本机恰好没有 | 官方文档 + `ext/mcp/src/executor_plugin/discovery.rs`、`external-agent-migration/src/detect/mod.rs` 的字符串 |

另外两条与本期需求直接相关：

- **续聊不变量必须对两个源同时成立**（§1.2）。实测 Claude 侧 93 个 jsonl、Codex 侧 80 个 rollout 的工具调用与结果**全部配对**，所以今天不一定会炸；但「配对」是续聊能否成立的硬前提，必须写成解析器的不变量并测试，而不是依赖源端恰好干净。
- **源目录必须可输入**（§6.2）。`Selector` 已经有 `Input(label, defaultValue)`（`pkg/tui/channels.go:1768`），channels 向导里到处在用「预填默认值」的形态，正好满足「不能给空白输入框」的要求，无需新增 TUI 原语。

---

## 1. 目标、边界与完成定义

### 1.1 必须达到的产品结果

`/migrate` → 选择 **Codex** → 输入（或留空）源目录 → 选择会话范围 → 干跑预览 → 确认 → 执行 → 报告。完成后：

| 能力 | 验收方式（相对断言，不写死数量——源端数据会自行变化） |
|---|---|
| 历史会话 | 干跑报出的会话数 == 实际写入数 + skipped 数；`/resume` 能按标题找到它们 |
| 历史消息 | resume 任一迁移会话，用户/助手/工具调用与结果/图片完整回放，顺序与 rollout 行序一致 |
| **可续聊** | **在任一迁移会话里直接发下一条消息，模型上下文成立、不报错**；被压缩过的会话（本机 17 个）续聊时压缩项随请求原样回放；见 §1.2 的四条不变量 |
| 子代理 | `thread_spawn_edges` 的每条子线程都有一个 child session 可 resume，`parent_session_id` 指向父会话 |
| Skills | `/skills` 能看到已启用插件带来的 skill |
| MCP（用户级） | `/mcp` 列出迁入的条目；源端 `enabled=false` 的条目不出现在已启用集合里 |
| MCP（项目级） | `<root>/.codex/config.toml` 的 `[mcp_servers]`（含每工具 `approval_mode`）已写入 `<root>/.forebrain/mcp_servers.yaml`；新会话里 `/mcp` 按项目作用域看到它们；forebrain **不再读取** `.mcp.json` 与 `.codex/config.toml`（§4.9） |
| 项目级权限 | Claude 的 `permissions.allow/deny/ask` 已转成 `<root>/.forebrain/safety.json` 的规则；`/permissions` 能看到 |
| 记忆 | 迁入的记忆按项目作用域可 `/memories` 查看，迁移后已自动合并 |
| Plans | 迁入会话的助手消息里每个 `<proposed_plan>` 块都在 `<workspaceRoot>/plans/<ProjectKey>/` 下有一个对应的 `.md`，`/plan` 能读到 |
| 输入历史 | 方向键上翻能翻到源端历史输入；新增条数 + 跳过条数 == 源文件行数 |
| 可识别 | 迁移产物带 `fb_sessions.origin='migrated'`，报告逐条列出 |
| **源目录** | 输入框留空走 `~/.codex`（或 `$CODEX_HOME`）；填绝对路径则从该路径迁移；路径不合法时把底层错误原文给用户 |

其余与 Claude 侧一致：幂等（重复执行不产生重复数据）、失败不脏库（单会话事务）、迁移会话默认 `memory_mode=disabled`。

### 1.2 续聊不变量（本期新增，对 Claude 与 Codex **同时**生效）

「迁进来能看」和「迁进来能用」是两件事。回放只读 `fb_messages`，续聊却要把这些行重建成 `[]llm.Message` 发给 provider（`storedTranscriptMessages` / `listProjectedMessageRows`）。任何一条不成立，续聊就会在第一条消息上失败：

1. **工具调用必须配对。** 每个带 `tool_calls` 的 assistant 行，必须有一条 `role=tool` 且 `tool_call_id` 匹配的行跟随。源端因中断而留下的孤儿调用（Codex 有 64 次 `turn_aborted`），必须补一条 **cancelled 工具结果**行 —— 按既有规矩，未执行的调用用取消结果作答，**绝不能删掉调用行**来讨好 provider 的校验。这是对外部输入的防御，不是给自身逻辑打补丁。
2. **不得写入模型看不懂的行。** `ParseMessage` 只认 `user`/`assistant`/`tool`/`system` 与压缩项；`reasoning` 行返回 `ok=false`（展示用，不入上下文），`developer` 行没有分支。所以 Codex 的 `developer` 消息在导入时就丢弃，加密 reasoning 也不入库（C3）。
3. **首条必须是真实用户消息。** 丢掉开头的 `developer`/环境注入后，第一条落库行必须是 `role=user`。
4. **压缩会话的投影必须自洽。** 有 `compact_boundary_id` 时，上下文 = 该边界行的 `ReplacementHistory` + 边界行之后的全部行。所以 `ReplacementHistory` 必须完整（Codex 直接用源端 `replacement_history`），`compact_boundary_id` 必须指向**最后一条**边界行的 row id。

> **这四条同样要回头验 Claude 侧。** 本期把「迁移会话可续聊」做成 `pkg/migrate` 的共享不变量与共享测试，Claude 与 Codex 两个源跑同一套断言；Claude 侧若有缺口（例如孤儿工具调用没补取消结果），在本期一并修掉，而不是只给 Codex 做。

### 1.3 NOT in scope

与 Claude 侧保持同一条边界线：

- `~/.codex/config.toml` 的 `[projects."<path>"].trust_level`（10 个受信项目）、`rules/default.rules`、`shell_environment_policy`、`[features]`、`[desktop]`、`[tui.*]`、`[notice]`：**不迁**。Claude 侧同样没有迁 `settings.json` 的权限与信任配置，迁过来等于替用户做授权决定。项目级 `.codex/config.toml` 里除 `[mcp_servers]` 之外的段（sandbox、hooks、model、reasoning 默认值）同理不迁。
- 管理员托管配置（`/etc/codex/config.toml`、`managed_config.toml`）：机器/组织级策略，不属于用户资产。
- `logs_2.sqlite`(145MB)、`queue_1.sqlite`、`goals_1.sqlite`、`thread_history_1.sqlite`（`item_completed` 的投影）、`shell_snapshots/`、`computer-use/`、`cache/`、`ipc/`、`node_repl/`、`vendor_imports/`、`visualizations/`、`ambient-suggestions/`、`dictation-history/`、`.codex-global-state.json`、`auth.json`、`models_cache.json`、`installation_id`、`version.json`：运行态、凭据与内部状态，forebrain 无对应语义。
- `~/.codex/skills/*`：forebrain **已原生发现** `~/.codex/skills`（`pkg/skill/roots.go` 的 `userSkillDirs`），拷贝只会产生双份漂移。
- `~/.codex/skills/.system/*`（6 个 Codex 内置 skill）：不迁。它们是 Codex 自带的系统能力，对应 Claude 的内置工具，不是用户安装的资产；报告里列一行说明。
- 未启用插件的 skills（本机 `openai-templates` 20 个 + `plugin-management` 1 个）：不迁，与 Claude 侧「未启用即未使用」同规则。
- 主会话回放里的 subagent 卡片合成：与 Claude 侧同一条已知限制。

---

## 2. 实测现状：Codex 侧（`~/.codex`）

> **这是 2026-09-17 的快照，不是恒定值。** 与 Claude 侧同一条纪律：任何验收条件都不得直接引用下表的数字，只能写相对断言。

### 2.1 规模

| 数据 | 位置 | 量级 |
|---|---|---|
| 总体积 | `~/.codex` | 1.0 GB（其中 `plugins/` 329 MB、`sessions/` 294 MB、`logs_2.sqlite` 145 MB） |
| 会话 | `sessions/<YYYY>/<MM>/<DD>/rollout-<ISO>-<threadId>.jsonl` | 80 个文件，53 608 行；最大单文件 54.2 MB |
| 会话索引 | `state_5.sqlite` → `threads` | 80 行（id/title/cwd/git_*/history_mode/archived/memory_mode/tokens_used…） |
| 轻量索引 | `session_index.jsonl` | 35 行（`id`/`thread_name`/`updated_at`），是 `threads` 的子集 |
| 按 cwd 分布 | `threads.cwd` | forebrain 51、chatibs_audit_agent 10、ai_ibs 6、guangfa/openclaw 3、chatibsagent/openclaw 3、ppt 2、chatibs 2、招股说明书 1、chatibs_web 1、audit_copilot 1 |
| 输入历史 | `history.jsonl` | 375 行（`session_id`/`ts`/`text`） |
| 记忆 | `memories_1.sqlite` → `stage1_outputs` | **0 行** |
| 全局指令 | `AGENTS.md` | **0 字节** |
| MCP（用户级） | `<CODEX_HOME>/config.toml` → `[mcp_servers.*]` | 3 个：`node_repl`（未写 `enabled`，即启用）、`computer-use`（`enabled=false`）、`cua_repl`（`enabled=false`） |
| MCP（项目级） | `<project_root>/.codex/config.toml` → `[mcp_servers.*]` | 本机 0 个项目建了此文件（功能存在，见 C9） |
| Skills（用户级） | `skills/` | 根下 0 个普通 skill；`.system/` 下 6 个内置 |
| Skills（插件） | `plugins/cache/<marketplace>/<plugin>/<version>/skills/<name>/SKILL.md` | 共 42 个；**已启用插件带 21 个**，未启用插件带 21 个 |
| 已启用插件 | `config.toml` → `[plugins."<name>@<marketplace>"].enabled=true` | 11 个 |
| Plans | 助手消息里的 `<proposed_plan>` 块（Plan Mode 产物） | 本机 0 个真实块（唯一一处 `proposed_plan` 是工具输出里的源码文本） |
| 子代理 | `state_5.sqlite` → `thread_spawn_edges` | **0 行** |

### 2.2 rollout jsonl 的记录形态

每行一个 JSON，顶层恒为 `{"timestamp": <RFC3339>, "type": <kind>, "payload": {...}}`。

顶层 `type` 分布（全量）：`response_item` 32 773、`event_msg` 18 622、`token_usage_record` 1 442、`turn_context` 384、`world_state` 231、`session_meta` 81、`compacted` 75。

**`session_meta`**（每文件 1 条；有 1 个文件 2 条，即会话内发生过重开）：
```
session_id / id / timestamp / cwd / originator / cli_version / source /
thread_source / model_provider / base_instructions / history_mode /
context_window{window_id} / git{commit_hash,branch,repository_url} / memory_mode?
```

**`response_item`** 子类型：`reasoning` 10 358、`custom_tool_call` 9 675、`custom_tool_call_output` 9 675、`message` 2 359、`function_call` 352、`function_call_output` 352、`tool_search_call` 1、`tool_search_output` 1。

- `message`：`role` 为 `assistant` 1487 / `user` 486 / `developer` 386；`content[]` 的 `type` 为 `output_text` 1487 / `input_text` 1297 / `input_image` 47。`input_image.image_url` 是 `data:image/png;base64,...` 形式的 data URI。
- `custom_tool_call`：`{call_id, name, input, status, internal_chat_message_metadata_passthrough{turn_id}}`；`name` 为 `exec` 9632 / `apply_patch` 43。`exec` 的 `input` 是一段 **JS 脚本文本**（`const r = await tools.exec_command({cmd:"…", workdir:"…"}); text(r.output);`）。
- `custom_tool_call_output`：`{call_id, output:[{type:"input_text", text}]}`。
- `function_call` / `function_call_output`：`{call_id, name, arguments}` / `{call_id, output}`；`name` 为 `wait` 247 / `write_stdin` 62 / `exec_command` 31 / `run` 7 / `update_plan` 5。
- `reasoning`：`{id, summary:[], encrypted_content}`。summary 非空的只有 195 条（127×1 + 63×2 + 5×3）。

**`event_msg`** 子类型：`token_count` 10 407、`item_completed` 3 837、`patch_apply_end` 1 565、`agent_message` 1 247、`task_started` 315、`thread_settings_applied` 307、`agent_reasoning` 268、`task_complete` 251、`user_message` 250、`turn_aborted` 64、`context_compacted` 63、`thread_goal_updated` 28、`web_search_end` 20。

`item_completed.item.type`：`Reasoning` 1746、`CommandExecution` 1425、`FileChange` 328、`AgentMessage` 240、`UserMessage` 79、`ContextCompaction` 12、`ImageView` 4、`McpToolCall` 3。

**`compacted`**（75 条，分布在 17 个会话）：
```
message              // 75/75 长度为 0，摘要不在这里
replacement_history  // 75 条 {type:"compaction", id, encrypted_content} + 855 条 message
window_number / window_id / previous_window_id / first_window_id   // 75/75 齐全
compaction_response_id / guardian_history / latest_token_usage_record  // 各 12 条
```

**`token_usage_record`** / **`event_msg.token_count`**：`usage{input_tokens, cached_input_tokens, cache_write_input_tokens, output_tokens, reasoning_output_tokens, total_tokens}`，另有 `turn_token_usage` 与 `thread_token_usage` 累计值。

**`turn_context`**：`{turn_id, cwd, workspace_roots, approval_policy, sandbox_policy, permission_profile, …}`。**`world_state`**：环境快照（AGENTS.md、filesystem、environments）。两者都是运行态，不产生消息行。

### 2.3 两条历史流与 `history_mode`

| history_mode | 会话数 | `item_completed` | `event_msg.agent_message` | 结论 |
|---|---|---|---|---|
| `legacy` | 61 | 0（60 个文件）| 与 `response_item message role=assistant` **条数完全相等** | 两条流互为重复 |
| `paginated` | 20 | 有（19 个文件）| 0 | item 流是 UI 侧线，`response_item` 仍然完整 |

所以：**`response_item` 是唯一的脊柱**（两种模式下都完整、都有序、`call_id` 自带配对），`event_msg` 只贡献三样东西 —— `agent_reasoning`（legacy 的明文思考）、`turn_aborted`（中断标记）、`token_count`/`token_usage_record`（用量）。

**不做跨流 join。** `item_completed` 的 `CommandExecution.id`（`exec-<uuid>`）与 `custom_tool_call.call_id`（`call_<…>`）之间没有共享标识，只能靠 `turn_id` + 序号做位置匹配 —— 这种连接一旦源端改变事件顺序就会静默错位，而它换来的只是 argv 数组和 exit code 这类展示增强；`custom_tool_call_output.output[].text` 本身已经包含完整输出。**收益不抵风险，本期不做。**

### 2.4 Codex 功能核实（证据不来自本机数据）

本机数据只能说明「用过什么」，不能说明「支持什么」。下面这几条是从 Codex 本体与官方文档确认的，实施时若与观察冲突，以这里的来源为准并更新本节。

| 功能 | 是否支持 | 证据来源 |
|---|---|---|
| 项目级配置 `<project_root>/.codex/config.toml` | **支持** | 官方 `config-reference`：*You can also add project-scoped overrides in `.codex/config.toml` files*；二进制：`Project .codex/config.toml: settings for a trusted repository, including sandbox, MCP, hooks, model, and reasoning defaults` |
| 项目级 `[mcp_servers]` | **支持** | 同上，文档明列 MCP 属于项目可覆盖项 |
| 项目级配置的信任门控 | **有** | 官方：*Codex loads project-scoped config files only when you trust the project*；`config.toml` 的 `[projects."<path>"].trust_level` |
| 项目根的判定 | `project_root_markers = [".git"]`（可配） | 二进制内嵌的默认配置片段 |
| 项目配置**不可**覆盖的键 | `openai_base_url`、`chatgpt_base_url`、`apps_mcp_product_sku`、`model_provider(s)`、`notify`、`profile(s)`、`experimental_realtime_ws_base_url`、`otel` | 官方 `config-reference` |
| 仓库 `.mcp.json` 原生读取 | **不支持** | 官方文档只在 `config.toml` 下定义 MCP；二进制里 `.mcp.json` 仅出现于插件清单与 `external-agent-migration`（Codex 从 Claude/Cursor 导入时读对方的文件） |
| Plan Mode | **支持** | 二进制内嵌提示词 `# Plan Mode (Conversational)`；配置键 `plan_mode_reasoning_effort` |
| Plan 的持久化形态 | 仅助手消息里的 `<proposed_plan>` 块 | 无 `plans/` 目录；`thread_artifacts` 无 plan 类型；`Notification::PlanModePrompt` 是 TUI 提示 |
| 子代理 / 线程派生 | **支持** | `state_5.thread_spawn_edges`、遥测事件 `spawn_agent`/`wait_agent`/`close_agent`、`x-openai-subagent` 请求头 |
| 记忆 | **支持** | `memories_1.sqlite` schema、`agent_message.memory_citation` 字段、`threads.memory_mode` 列 |
| 管理员托管配置 | **支持**（不迁） | 二进制：`/etc/codex/config.toml`、`managed_config.toml`、`legacy-managed-config.toml` |

> Codex 自己也带一个 `external-agent-migration` 模块（从 Claude Code / Cursor 导入会话、MCP、hooks、skills、memory）。它是**反方向**的迁移，与本方案无关，但它证明了这些类别在 Codex 侧都有对应落点，可作为字段命名的参照。

---

## 3. 与 Claude Code 迁移的语义对齐表

这一节是本期的验收基准：左列是 Claude 方案已落地的语义，中列是 Codex 的对应物，右列是差异处理。**没有一个类别被新增，也没有一个类别被删减。**

| 类别 | Claude Code | Codex | 差异处理 |
|---|---|---|---|
| 会话 | `projects/<encoded>/<uuid>.jsonl` | `sessions/<Y>/<M>/<D>/rollout-*-<threadId>.jsonl` + `state_5.threads` | id 同样是 `cli-<sourceId>` |
| 标题 | 最后一条 `ai-title.aiTitle` | `state_5.threads.title` → `session_index.thread_name` → 首条用户文本前 40 字符 → id | 三级回退，语义与 Claude 的回退链一致 |
| 消息 | `user`/`assistant`/`system` 行 | `response_item`（§4.2） | — |
| 思考 | `thinking` block（明文） | 加密，仅 463 条有明文 | 只迁明文，其余不入库（C3），报告给出计数 |
| 工具 | `tool_use`/`tool_result` | `custom_tool_call`/`function_call` + `_output` | 工具名映射见 §4.3 |
| 压缩 | `compact_boundary` + `isCompactSummary`，窗口链需合成 | `compacted`，窗口链自带、摘要为不透明项 | Codex 更完整（C1/C2） |
| 子代理 | `subagents/agent-*.jsonl` + `.meta.json` | `thread_spawn_edges` 的子线程 | 同样写 `parent_session_id`（C6） |
| 记忆 | `projects/<encoded>/memory/*.md` | `memories_1.sqlite.stage1_outputs` | 同样落 ad-hoc note + 迁移后自动合并（C7） |
| 全局记忆 | `~/.claude/CLAUDE.md` → global scope | `~/.codex/AGENTS.md` → global scope | 两侧本机都为空；见 §4.7 的对齐说明 |
| Skills（用户级） | `~/.claude/skills` 原生发现，不迁 | `~/.codex/skills` 原生发现，不迁 | 完全一致 |
| Skills（项目级） | `<repo>/.claude/skills` 原生发现，不迁 | `<repo>/.codex/skills` 原生发现，不迁 | 完全一致 |
| Skills（插件） | 已启用插件的 skills → `~/.forebrain/skills/` | 同左，`[plugins."x@y"].enabled=true` | 完全一致 |
| MCP（用户级） | `~/.claude.json` 顶层 `mcpServers` | `config.toml` `[mcp_servers.*]` | 同样写 `forebrain.yaml` 全局列表 |
| MCP（项目级·本机私有） | `~/.claude.json` `projects[].mcpServers` → `.forebrain/mcp_servers.yaml` | **无此层**（Codex 的项目 MCP 是仓库文件，不是本机私有） | 恒为空 |
| MCP（项目级·仓库文件） | `.mcp.json` → `.forebrain/mcp_servers.yaml`（**原方案的「原生读取」已作废**） | `<project_root>/.codex/config.toml` 的 `[mcp_servers]` → 同一个文件 | 两个源同规则：复制进 forebrain 自己的项目配置，forebrain 不读源文件（§4.9） |
| 项目级权限 | `.claude/settings*.json` 的 `permissions` → `.forebrain/safety.json` | `sandbox_mode`/`approval_policy` 是 profile 语义，无对应 | Claude 侧迁，Codex 侧报告说明无对应（§4.9.2） |
| MCP 批准状态 | `enabledMcpjsonServers`/`disabled…` | `[mcp_servers.x].enabled` 布尔 | `false` → 跳过；缺省/`true` → 写入但不预置确认 |
| Plans | `~/.claude/plans/*.md` → plan 存储 | 助手消息里的 `<proposed_plan>` 块 | 抽取成 `.md` 写进**同一个** plan 存储（§4.10） |
| 输入历史 | `~/.claude/history.jsonl` `display`/`timestamp` | `~/.codex/history.jsonl` `text`/`ts` | 同一套合并与归一化 |
| 信任/权限配置 | 不迁 | 不迁 | 完全一致 |

---

## 4. 映射设计

### 4.1 会话 → `fb_sessions`

| 字段 | 取值 |
|---|---|
| `id` | `cli-<threadId>`（与 Claude 侧同一形状） |
| `agent_id` | 当前 primary agent |
| `title` | `state_5.threads.title` → `session_index.thread_name` → 首条真实用户消息前 40 字符 → `id`（**不写 `New Session`**，那会破坏 `title==id` 哨兵语义） |
| `created_at` / `updated_at` | `session_meta.timestamp` / 末条记录 `timestamp`（Unix 秒） |
| `cwd` | `session_meta.cwd`（最后一次非空值，`turn_context.cwd` 可覆盖） |
| `git_branch` | `session_meta.git.branch` |
| `memory_mode` | `disabled` |
| `memory_source` | `tui` |
| `message_count` | 实际插入行数 |
| `prompt_tokens` / `completion_tokens` | 由 `token_usage_record.thread_token_usage` 取最后一条；缺失时累加 `event_msg.token_count.last_token_usage` |
| `cost` | `0` —— Codex 不记录美元成本，**不臆造** |
| `parent_session_id` | `''`；子线程填父会话 id |
| `compact_boundary_id` | 最后一条边界行的 row id 十进制字符串；无边界时 `''` |
| `initial_window_id` | **`session_meta.context_window.window_id` 源端真值**；缺失时才走 `EnsureInitialWindowID` |
| `origin` | `migrated` |

写入路径与 Claude 侧完全一致：`Ensure(id, id)` → 一个 `UPDATE` 回写上表；`source` 一律 `transcript`；写前用 `sessionOwner` 检查归属，已属于另一个 primary agent 时记 `owned-by-another-agent` 并跳过，绝不继续写消息。

**一个文件两条 `session_meta` 的情况**（本机 1 例）：以**第一条**为会话身份，第二条只用于刷新 `cwd`/`git`；不拆成两个会话 —— 它们共享同一个 `threadId`，拆开会撞 id。

### 4.2 消息 → `fb_messages`

脊柱是 `response_item`，按文件行序插入（forebrain 回放按 row id 排序，与行序天然一致）。

| 源记录 | 条件 | 目标行 |
|---|---|---|
| `message` | `role=user`，`content[]` 含 `input_text` | `role=user`，`content=文本拼接` |
| `message` | `role=user`，`content[]` 含 `input_image` | 追加 `parts=[{type:image,source:{type:base64,media_type,data}}]`（拆 data URI） |
| `message` | `role=user`，正文匹配注入前缀（见下） | 同上 + `parts` 追加 `{type:is_meta}` |
| `message` | `role=assistant` | `role=assistant`，`content=output_text 拼接` |
| `message` | `role=developer` | **丢弃**（§1.2 不变量 2） |
| `custom_tool_call` / `function_call` | 任意 | `role=assistant`，`content=''`，`parts=[{type:tool_calls,tool_calls:[{id:call_id,type:function,function:{name:映射名,arguments:规范化 JSON}}]}]`，`tool_step_id=call_id` |
| `custom_tool_call_output` / `function_call_output` | 任意 | `role=tool`，`content=输出文本`，`tool_step_id=call_id`，`parts=[{type:tool_result_meta,tool_call_id},{type:tool_display,body,summary,tool_meta_json}]` |
| `tool_search_call` / `tool_search_output` | 任意 | 同上，工具名保持 `tool_search` |
| `reasoning` | `summary` 非空 | `role=reasoning`，`content=summary 文本拼接` |
| `reasoning` | `summary` 为空（仅 `encrypted_content`） | **丢弃**（C3） |
| `event_msg.agent_reasoning` | 任意 | `role=reasoning`，`content=text`（legacy 会话的明文思考来源） |
| `event_msg.agent_message` / `user_message` / `item_completed` | 任意 | **丢弃**（与 `response_item` 重复，C4） |
| `compacted` | 任意 | 一条**边界行**，见下 |
| `turn_context` / `world_state` / `token_count` / `token_usage_record` / `task_*` / `patch_apply_end` / `thread_*` | 任意 | 不产生行；用量折进会话聚合，`turn_aborted` 折进该回合末条 assistant 行的可见正文 |

**被判定为注入的用户消息**（打 `is_meta`，与 Claude 的 `isMeta` 同语义）：正文以 `<environment_context>`、`<codex_internal_context`、`<turn_aborted>`、`<image name=`、`# AGENTS.md instructions for ` 开头的（本机分别 98 / 42 / 17 / 47 / 8 条）。它们仍进模型上下文（`IsMeta` 只是内部标记），但回放时按既有语义隐藏。

共同字段：`created_at` 取行的 `timestamp`（RFC3339 → Unix 秒），缺失时取上一条 +1 秒保证单调；`message_id` 取 `payload.id`（`msg_*`/`ctc_*`/`fc_*`/`rs_*`），这是去重键；`finished_at` 同 `created_at`；`source` 恒为 `transcript`。

**压缩边界怎么映射（本方案最省力也最容易做错的地方）**

Codex 的 `compacted` 与 forebrain 的 `CompactBoundaryPart` 几乎是同一个结构，逐字段搬运即可：

| forebrain 字段 | Codex 来源 |
|---|---|
| `Trigger` | 固定 `"codex"`（源端不区分 auto/manual） |
| `Strategy` / `SummarySource` | 标为迁移来源 |
| `ReplacementHistory` | `replacement_history` **逐条转换**：`{type:"compaction",…}` → `llm.Message{Compaction:&CompactionState{Type,ID,EncryptedContent}}`；`{type:"message",…}` → 普通 `llm.Message`（`developer` 条目同样丢弃） |
| `WindowNumber` | `window_number` |
| `WindowID` | `window_id` |
| `PreviousWindowID` | `previous_window_id` |
| `FirstWindowID` | `first_window_id`（与会话的 `initial_window_id` 一致） |

边界行的可见正文写 `compact boundary (migrated from codex) · window <n>`，不为 `compaction_response_id`/`guardian_history` 臆造部件类型。会话的 `compact_boundary_id` 指向**最后一条**边界行。

这样一来，续聊时 `listProjectedMessageRows` 重建出的上下文 = 源端保留段（含那条不透明压缩项）+ 边界之后的全部行，与 Codex 自己续聊时看到的历史等价。

**大输出**：`custom_tool_call_output.output[].text` 可能很长（最大单文件 54.2 MB 主要来自这里），全量内联，不截断 —— 完整性优先，报告给出体积统计。

### 4.3 工具名映射与 `tool_meta_json`

| Codex | Forebrain Harness |
|---|---|
| `exec` | `shell` |
| `exec_command` | `shell` |
| `run` | `shell` |
| `write_stdin` | `shell` |
| `wait` | `shell` |
| `apply_patch` | `edit_file` |
| `update_plan` | `session_todo` |
| `tool_search` | `tool_search` |
| `mcp__<server>__<tool>` / `McpToolCall` | 原样保留 |
| 其他 | 原样保留（按纯文本工具卡显示） |

`tool_meta_json` 写 `tool.ToolMeta`：`tool_name`（映射后）、`status="completed"`（`item.status=="failed"` 时写 `failed`）、`input`（解析后的 arguments）、`invocation`。

`invocation` 的取法，**不做 JS 解析**：
- `function_call` 系列：`arguments` 是规整 JSON，取 `cmd` 字段首行。
- `custom_tool_call` `name=exec`：`input` 是 JS 脚本文本 —— 取**脚本首行**作为 invocation，`input` 写 `{"script": <原文>}`。不去正则抠 `cmd:"…"`：那是猜测，脚本可以是任意 JS，猜错就等于在工具卡上显示一条用户从未执行过的命令。
- `apply_patch`：取补丁首个文件路径。

`tool_display.body` 取 `custom_tool_call_output.output[].text` 拼接（`function_call_output` 取 `output`）。

### 4.4 子代理会话

- `state_5.sqlite` → `thread_spawn_edges(parent_thread_id, child_thread_id, status)`：每条子线程 → 子会话 `cli-<childThreadId>`，`parent_session_id = cli-<parentThreadId>`，标题走 §4.1 的回退链，`cwd` 缺失时继承父会话。
- 子线程自身就是 `sessions/` 下的一个普通 rollout 文件，行映射与主会话完全相同。
- **导入顺序**：先父后子，保证写子会话时父行已存在。父线程缺失（rollout 已被清理）时，子会话按独立会话导入，`parent_session_id` 留空并在报告里单列。
- **已知限制**（与 Claude 侧同一条）：主会话回放里不会出现可点击的 subagent 卡片 —— 那些卡片来自 `fb_session_events` 的 run 事件流，rollout 不携带这类事件，无法无损重建。
- 本机 `thread_spawn_edges` 为 0 行，此路径**只能靠夹具测试验证**，不能靠真实数据验收。

### 4.5 读取 `state_5.sqlite` 的方式

标题、归档标记、父子线程这三样只有 `state_5.sqlite` 有，rollout 里没有，所以必须读它。但它是另一个正在运行的 App 的活库（带 `-wal`/`-shm`）：

- **先快照再读**：把 `state_5.sqlite`、`-wal`、`-shm` 复制到进程临时目录，用 `mode=ro` 打开副本。不碰原文件、不触发 WAL 恢复、不与 Codex 争锁。
- **读不到就降级，不失败**：库缺失/损坏/schema 不符时，标题退回「首条用户文本前 40 字符」，子代理关系退回「全部按独立会话导入」，报告写明降级原因与底层错误原文。
- `memories_1.sqlite` 同此处理。

### 4.6 记忆（含自动合并）

- 源：`memories_1.sqlite` → `stage1_outputs(thread_id, raw_memory, rollout_summary, rollout_slug, generated_at, source_updated_at)`。
- 作用域：`thread_id` → `threads.cwd` → `memory.ProjectKey(memory.ProjectRoot(cwd))` → `memories/projects/<key>/`。`thread_id` 在 `threads` 里查不到时落 global 作用域并在报告里单列（与 Claude 侧第 4 级兜底同语义）。
- 文件名：`<YYYY-MM-DDTHH-MM-SS>-<slug>.md`（UTC；时间取 `generated_at`，缺失用 `source_updated_at`；slug 取 `rollout_slug` 并规整为 `[a-z0-9-]{1,80}`，缺失时用 `thread_id` 前 8 位），满足 `validateNoteFilename`。
- 正文：`# <slug>` + `rollout_summary` 作为 description + `raw_memory` 正文 + 一行溯源（`origin: codex memory (thread <thread_id>)`）。
- 写入：`memory.NewScoped(...).AddAdHocNote(...)`，复用路径拼接、symlink 防御与重名拒绝（已存在 → 跳过，幂等）。
- 自动合并：迁移末尾对受影响作用域跑一次 `Consolidate` 回调（由组合根注入，`pkg/migrate` 不得直接 import `pkg/run`）；回调为 nil 或 memories 关闭时，报告如实写「已落盘、未合并」。
- **本机 0 行** —— 干跑与实跑都会报 0，这不是 bug。验收靠 §9 的夹具。

### 4.7 全局指令文件

`~/.codex/AGENTS.md` 是 Codex 的用户级全局指令，对应 `~/.claude/CLAUDE.md`。两边本机都为空（0 字节 / 不存在）。

Claude 方案 §4.8 把它写进了 scope，但实现里没有这条分支。**对齐要求二选一，本期选「两边都实现」**：非空时剥去 frontmatter，作为 global scope 的一条 ad-hoc note 写入，溯源行写明来源文件。Claude 侧的同名分支在本期一并补上 —— 只给 Codex 做会让两个源的行为再次分叉。

### 4.8 Skills

| 来源 | 处理 |
|---|---|
| `<repo>/.codex/skills/*` | **无需迁移**：`pkg/skill/roots.go` 的 `projectSkillDirs` 已含 `.codex/skills`（信任门控） |
| `~/.codex/skills/*` | **无需迁移**：`userSkillDirs` 已含 `~/.codex/skills` |
| `~/.codex/skills/.system/*`（6 个） | **不迁**：Codex 内置系统 skill，非用户资产；注意 forebrain 的原生发现也**不会**扫到它（发现的是 `~/.codex/skills` 本身，`.system` 的子目录深一层），报告写一行说明，避免用户以为是 bug |
| 已启用插件的 skills（本机 21 个） | **迁移**：`plugins/cache/<marketplace>/<plugin>/<最高版本>/skills/<name>/` → `~/.forebrain/skills/<name>/`，复用 `skill.InstallFromDir` |
| 未启用插件的 skills（本机 21 个） | **不迁** |

「已启用」的判定：`config.toml` 的 `[plugins."<name>@<marketplace>"]` 表里 `enabled = true`。版本目录取字典序最大者（与 Claude 侧同一规则）。

幂等：目标已存在时比对 `skill.DirectoryDigest`，报告 `up-to-date` / `diverged`（不覆盖）/ `installed`。

**没有 description 的 SKILL.md 一律不算 skill**，发现阶段就排除并计入报告，与 forebrain 既有规则一致。

### 4.9 项目级配置迁移（两个源共用的规则）

> **本节是 2026-09-17 的拍板结果，推翻了两份方案里原来的「原生读取、不复制」。**
>
> 决定：**Claude Code 与 Codex 的项目级配置里，凡是能复制或对齐到 forebrain 语义的配置项，一律迁移进 forebrain 自己的项目级配置文件；forebrain 不读取 Claude Code 和 Codex 的项目级配置文件。**
>
> 这条同时适用于 Codex 的 `<root>/.codex/config.toml` 和 Claude 的 `<root>/.mcp.json`，所以 `CLAUDE_CODE_MIGRATION_PLAN.md` §4.7 的「`.mcp.json` 不迁、原生读取」作废，本期一并改。

#### 4.9.1 forebrain 的项目级配置面

| 文件 | 承载 | 门控 |
|---|---|---|
| `<root>/.forebrain/mcp_servers.yaml` | 项目级 MCP（含每工具审批模式） | `VersionControlled && IsTrusted` + 逐条确认 + 审批模式最宽 `prompt` |
| `<root>/.forebrain/safety.json` | 项目级权限规则 `{rules:{allow\|deny\|ask:[PermissionRuleValue]}}` | 同上（`safety.destinationPath` 的 `DestinationProjectSettings`） |
| `<root>/.forebrain/skills/` | 项目级 skills（资产，非配置） | 同上 |
| `<root>/.forebrain/filters` | 工具输出过滤器 | 同上 |

forebrain 的 hooks 只有用户级（`forebrain.yaml` 的 `hooks:`），**没有项目级落点** —— 这决定了下表里 hooks 那几行只能标「无对应」。

#### 4.9.2 配置项映射表

| 源 | 源文件 | 配置项 | forebrain 落点 | 结论 |
|---|---|---|---|---|
| Codex | `<root>/.codex/config.toml` | `[mcp_servers.<n>]` 的 `command`/`args`/`env`/`cwd` | `.forebrain/mcp_servers.yaml` 的 `transport: stdio` + 同名字段 | **迁** |
| Codex | 同上 | `[mcp_servers.<n>]` 的 `url`/`headers`/`bearer_token_env_var` | `transport: streamable_http\|sse` + `url`/`headers` | **迁** |
| Codex | 同上 | `[mcp_servers.<n>.tools.<t>].approval_mode` | `tools.<t>.approval_mode` —— **取值字面完全相同**（`auto`/`prompt`/`writes`/`approve`，见 `pkg/config/agents.go:96-99`） | **迁（1:1）** |
| Codex | 同上 | `[mcp_servers.<n>].enabled = false` | — | **跳过**，报告单列「源端已禁用」 |
| Codex | 同上 | `[mcp_servers.<n>].startup_timeout_sec`、`oauth_client_id`、`oauth_client_registration` | 无对应字段 | 丢弃并在报告注明 |
| Codex | 同上 | `sandbox_mode` / `approval_policy` / `permission_profile` | `.forebrain/safety.json` 只存规则列表，不存 profile | **无对应**，报告说明 |
| Codex | 同上 | `hooks` / `model` / `model_reasoning_effort` | forebrain 无项目级落点 | **无对应**，报告说明 |
| Claude | `<root>/.mcp.json` | `mcpServers.<n>{type,command,args,env,url,headers}` | `.forebrain/mcp_servers.yaml` | **迁**（原方案的「原生读取」作废） |
| Claude | `<root>/.claude/settings.json` 与 `settings.local.json` | `permissions.allow` / `deny` / `ask` | `.forebrain/safety.json` 的 `rules`，逐条转成 `PermissionRuleValue{ToolName, RuleContent}`；工具名走既有映射（`Bash`→`shell`、`Read`→`read_file`…），`Bash(go test *)` → `{tool_name:"shell", rule_content:"go test *"}` | **迁** |
| Claude | 同上 | `enabledMcpjsonServers` / `disabledMcpjsonServers` / `enableAllProjectMcpServers` | 项目 MCP 的逐条确认存储（`mcp.ProjectConsents`）：enabled → 预置 allow；disabled → 不写入条目；`enableAllProjectMcpServers=true` → **不**预置全量 allow，仍逐条确认 | **迁（批准状态）** |
| Claude | 同上 | `hooks{PostToolUse,SessionStart,…}` | forebrain 无项目级落点 | **无对应**，报告说明 |
| Claude | 同上 | `env` / `model` / 其余键 | 无项目级落点 | **无对应** |
| 两者 | `<root>/.claude/skills`、`<root>/.codex/skills` | skills 目录 | `pkg/skill/roots.go` 已原生发现 | **不迁**（资产不是配置项，本决定不覆盖它） |

> `enableAllProjectMcpServers: true` 故意不转成「全部预置放行」：它在源端的含义是「这台机器上我一次性放行了这个项目的所有 .mcp.json 条目」，而 forebrain 的逐条确认是按 server 指纹记的。把它翻译成批量放行等于替用户对**将来**新增的条目也做了授权。只预置 `enabledMcpjsonServers` 里逐个点过名的那些。

#### 4.9.3 forebrain 侧的行为变更

`pkg/mcp/project_config.go` 的 `ProjectMCPPaths` 从两个候选缩为一个：

```
  candidates := []string{
      <root>/.forebrain/mcp_servers.yaml,
-     <root>/.mcp.json,
  }
```

连带影响（都不需要改调用方）：`pkg/process/mcp.go` 的三处 `LoadProjectMCPServers` 自动只读 forebrain 自己的文件；`pkg/migrate/assets.go` 的 `nativeMCPNotes` 整个删除（它的职责是解释「为什么没复制」，而现在要复制了），`presetEnabledConsents` 保留但改为在写入 `.forebrain/mcp_servers.yaml` 之后执行。

`project_config.go` 顶部的契约注释要同步改写：`.mcp.json` 的优先级规则、map 形状的确定性展开这两段随文件候选一起删掉；「项目条目整条替换全局同名条目」「审批模式最宽 prompt」「按作用域隔离凭据」三条不变。

#### 4.9.4 用户级 MCP（不受本决定影响）

| 源 | 条件 | 目标 |
|---|---|---|
| Codex `<CODEX_HOME>/config.toml` `[mcp_servers.<n>]` | 无 `enabled` 键或 `enabled = true` | `forebrain.yaml` → `agents.defaults.mcp_servers[]`，**不预置确认** |
| Codex 同上 | `enabled = false` | 跳过，报告单列 |
| Claude `~/.claude.json` 顶层 `mcpServers` | — | 同上 |

写入方式：只追加的 patch，写前备份 `forebrain.yaml.bak-<UTC ts>`，同名已存在则跳过并在报告单列。`env` 原样写入，报告对值脱敏。

#### 4.9.5 幂等、冲突与报告

- 幂等：目标 `.forebrain/mcp_servers.yaml` 已有同名条目 → 内容相同记 `up-to-date`，不同记 `diverged` 并**不覆盖**；`.forebrain/safety.json` 的规则按 `(tool_name, rule_content)` 去重后追加，已存在的记 `skipped`。
- 写前备份：两个目标文件都按 `<name>.bak-<UTC ts>` 备份后再写，与 `forebrain.yaml` 同规矩。
- 报告必须写清楚四件事：
  1. **源文件仍然存在，且源工具仍然读它** —— 复制之后两份会各自漂移，这是本决定已知并接受的代价；
  2. **写进去 ≠ 已生效**：项目级 MCP 的有效列表在会话内冻结，下次启动才生效，且首次启动会逐条确认；
  3. **门控未过的项目**：目录必须是版本控制项目且已被信任，否则写入的文件不会被加载（本机 `/Users/doudou/workspace/hundsun/guangfa` 就是这种情况 —— 它有全套 `.mcp.json` + `.claude/settings*.json` + `.codex/config.toml`，但**不是 git 仓库**，实测 `git rev-parse` 报 `not a git repository`）；
  4. **无对应的配置项清单**：逐条列出 hooks、sandbox/approval_policy、model 等没有迁移的项和原因，不要让它们静默消失。

### 4.10 Plans

Codex 有 plan 语义、没有 plan 文件（C8）：Plan Mode 的产物是助手消息里的 `<proposed_plan>…</proposed_plan>` 块。forebrain 的 plan 存储是 `<workspaceRoot>/plans/<ProjectKey>/<slug>.md`（`state.PlanDirForProject`），Claude 的 `~/.claude/plans/*.md` 就落在这里。

**迁移实现：从已迁入的助手消息里抽取 `<proposed_plan>` 块，写进同一个 plan 存储。**

- 扫描范围：本次迁移写入的 assistant 行的正文（`response_item message role=assistant` 的 `output_text`）。只认成对的 `<proposed_plan>` / `</proposed_plan>`，标签原样匹配（Codex 的提示词明确要求不得翻译或改名这两个标签）。
- 归属：块所在会话的 `cwd` → `memory.ProjectKey(memory.ProjectRoot(cwd))`；解析不出项目时落无作用域的 plans 根，与 Claude 侧同一兜底。
- 文件名：`<会话标题 slug>.md`；同一会话出现多个块时按出现序追加 `-2`、`-3`；跨会话重名时追加 thread id 前 8 位。slug 规整为 `[a-z0-9-]{1,80}`。
- 正文：块内原文 + 一行溯源（`origin: codex plan mode (thread <thread_id>)`）。
- 幂等与冲突：目标已存在且内容相同 → `up-to-date`；不同 → `diverged`，**不覆盖**；与 Claude 侧 `importPlans` 完全一致的三态语义。
- `--only plans` 依赖会话解析，所以该类别在实现上跑在会话导入之后，读的是同一次解析结果，不重复读盘。

**不把 `update_plan` 的 TODO 反向合成 plan 文件** —— 那是 forebrain 里 `session_todo` 的语义，已经作为工具调用行迁进去了；再落一份文件就是同一份数据的第二个副本。`<proposed_plan>` 不同：它是一份完整的计划文档，在 Codex 里没有别的存储形态，不抽出来就等于丢了。

### 4.11 输入历史

- 读 `<sourceRoot>/history.jsonl`（375 行，`{session_id, ts, text}`），按 `ts` 升序把 `text` 归一化（`\n` → 空格）后追加到 `<agent workspace>/state/cli-input-history.txt`。
- 已存在的行跳过（幂等）；报告给出新增/跳过条数，且 `新增 + 跳过 == 源文件非空行数`。
- 与 Claude 侧共用 `mergeInputHistory` 与 `normalizeHistoryLine`，只换取字段名（`text`/`ts` vs `display`/`timestamp`）。

---

## 5. 配置文件解析（TOML）

两处都要解析 TOML：`<CODEX_HOME>/config.toml`（用户级 MCP、已启用插件）与 `<project_root>/.codex/config.toml`（项目级 MCP，§4.9 决定 A 下由 `pkg/mcp` 读）。需要读出 `[mcp_servers.*]`（含嵌套 `[mcp_servers.x.env]`）、`[plugins."<name>@<marketplace>"]`、以及带引号且含 `/`、`@`、中文的表头。

仓库里已经有一个 TOML 子集解析器：`pkg/tool/serialize.go` 的 `tomlParser`（支持表头、键值、基本/字面/多行字符串、整数、布尔、数组、内联表），外层 `parseFilterTOML` 才是 filter 专用的。

**做法：把通用层提取成一个共享入口（如 `tool.ParseTOMLDocument(src) (map[string]any, error)`），`parseFilterTOML` 与 Codex 的配置读取都走它；必要时扩展它以支持引号表头。禁止在 `pkg/migrate` 里再写第二个 TOML 解析器。** `pkg/migrate` 已经 import `pkg/tool`（取 `ToolMeta`），不会增加扇出。

需要先验证并在必要时补齐的能力：`["/Users/... "]`、`["browser@openai-bundled"]` 这类引号表头，以及 `[projects."…中文…"]`。

---

## 6. `/migrate` 交互

### 6.1 流程

```
/migrate
 ├─ [SelectRich] 迁移源（单选，默认高亮第一项）
 │    · 只列出检测到已安装的源：Claude Code（~/.claude）/ Codex（~/.codex）
 │    · 0 个源 → 不开空 picker，直接提示"没有可迁移的 agent"
 ├─ [Input]     源目录（本期新增，见 §6.2）
 ├─ [SelectRich] 会话范围（单选）：全部项目 / 仅当前项目
 ├─ [Confirm]   干跑预览
 └─ 执行 → 进度（含 consolidating 阶段）→ 报告
```

### 6.2 源目录输入框（本期新增需求）

选定源之后、选择范围之前，弹一个**非必填**的单行输入框：

| 源 | label | 预填的默认值 |
|---|---|---|
| Claude Code | `Claude Code home — 留空用默认目录` | `$CLAUDE_HOME`，未设置则 `~/.claude` |
| Codex | `Codex home (CODEX_HOME) — 留空用默认目录` | `$CODEX_HOME`，未设置则 `~/.codex` |

- 用 `Selector.Input(label, defaultValue)`（`pkg/tui/channels.go:1768` 已有，channels 向导里就是这个用法）。**默认值必须预填在输入框里**，用户一眼能看到合法答案长什么样 —— 不能给空白输入框。
- 直接回车（留空）→ 用默认目录；填绝对路径 → 用该路径。
- 相对路径与 `~` 展开：`~` 展开为 home；相对路径按当前工作目录解析后转成绝对路径，并在预览里回显解析结果，让用户确认自己指到了哪。
- **校验**（在干跑之前做，失败就回到输入框）：目录必须存在，且含该源的标志物 —— Claude：`projects/` 或 `history.jsonl` 或 `settings.json`；Codex：`sessions/` 或 `history.jsonl` 或 `config.toml`。不通过时把底层错误原文直接给用户（例如 `stat /x/y: no such file or directory`），**不要在上面套一句自己编的解释或建议**。
- 校验通过后，把解析出的绝对路径写进干跑预览的抬头，报告里也带上，让"这次到底迁的是哪个目录"永远可追溯。

### 6.3 命令行参数

`ArgumentHint`: `[claude|codex] [--home <path>] [--dry-run] [--only sessions,memories,skills,plans,mcp,history] [--project]`

- `/migrate` — 打开 picker（默认路径）
- `/migrate codex --dry-run` — 用默认 `~/.codex` 只出预览
- `/migrate codex --home /Volumes/backup/.codex` — 从备份目录迁移
- `/migrate --only sessions --project` — 只迁当前项目会话

`--home` 与输入框是同一个字段的两个入口；给了 `--home` 就跳过输入框。

### 6.4 命令属性（保持不变）

`AllowedSurfaces` 只有 `SurfaceTUI`，且第二层保证仍在：`execMigrate` 只通过 `MigrateSlashHandler` 接口工作，该接口只由 `pkg/tui` 实现，gateway 永不注册。`runDisallowedCommands["migrate"] = true`。

---

## 7. 代码落点与架构约束

### 7.1 `pkg/migrate` 的改动

现状 8 个生产文件（`doc.go`、`source.go`、`claude.go`、`parse.go`、`toolmap.go`、`sessions.go`、`assets.go`、`plan.go`），上限 20。

| 文件 | 性质 | 内容 |
|---|---|---|
| `codex.go` | 新增 | 源探测、rollout 目录遍历、`state_5`/`memories_1` 快照读取、`config.toml` 读取 |
| `codex_parse.go` | 新增 | rollout jsonl → `parsedSession`（流式、逐行） |
| `codex_assets.go` | 新增 | 记忆、skills、MCP、输入历史的 Codex 侧取数（写入复用既有实现） |
| `source.go` | 改 | `SourceInfo.Available=true` for Codex；`DetectSources` 接受 `sourceRoot` 覆盖；`Options` 增 `SourceRoot string` |
| `plan.go` | 改 | `RunSource`/`PlanSource` 分发到 `RunCodex`/`PlanCodex`；`Report.Text()` 的抬头按源渲染 |
| `parse.go` | 改 | 抽出与源无关的部分（窗口链、行追加、标题回退、`parsedRow`），Codex 解析器复用 |
| `sessions.go` | 改 | `importSessions` 的会话遍历抽成源无关的形态；新增续聊不变量校验（§1.2） |
| `toolmap.go` | 改 | 增加 Codex 工具名表；`invocationLine` 增加 Codex 分支 |
| `doc.go` | 改 | 文件清单与分层说明同步 |

**新增 3 个生产文件 → 11 个，仍在 20 以内。**

`pkg/migrate` 之外还要改两处：

| 文件 | 改动 | 约束 |
|---|---|---|
| `pkg/mcp/project_config.go` | 候选列表**删掉** `.mcp.json`，只留 `.forebrain/mcp_servers.yaml`；顶部契约注释同步改写（§4.9.3） | `pkg/mcp` 现有 17 个生产文件，**只改不增** |
| `pkg/tool/serialize.go` | 把 `tomlParser` 的通用层提成共享入口（§5） | 只改不增；`parseFilterTOML` 改为复用它 |

`pkg/turn` 与 `pkg/tui` **均已满 20 个生产文件**，所以 §6.2 的输入框只能改既有文件（`pkg/tui/commands.go` 的 `handleMigrate`、`pkg/tui/chat_slash.go` 的 `ParseMigrateArgs`/`buildMigrateOptions`），**不得新增文件**。

### 7.2 不得突破的门禁

- **扇出不增**：`fanOutBudgets["migrate"] = {current: 8, target: 8}`，只统计 `github.com/forebrain-harness/forebrain-harness/pkg/*` 的 import。Codex 侧需要的 `state`/`config`/`llm`/`memory`/`skill`/`mcp`/`safety`/`tool` 都已在这 8 个之内，**不得引入第 9 个**。SQLite 驱动与 TOML 解析分别走既有 `database/sql` 注册与 `pkg/tool`，不新增 forebrain 包依赖。
- **层高不破**：`pkg/migrate` 是 layer 2，不得 import `pkg/run`/`pkg/process`；记忆合并继续走注入的 `Options.Consolidate` 回调。
- **文件名规则**：≤2 个下划线，测试文件与生产文件同名。
- **缓存**：迁移不参与 prompt 组装，不影响输入 token 缓存命中率。但 §4.8 迁入的 skill 会改变工具表 —— 与既有规则一致，**skill 变更只在新会话生效**，报告必须写明，不得在当前会话里重建工具表。

---

## 8. 幂等、事务、性能、进度、失败

- **幂等键**：会话 `cli-<threadId>`；消息 `(session_id, message_id)`；记忆按目标文件名；skill 按目录内容摘要；MCP 按 `name`；输入历史按行文本。
- **跨 primary agent**：同一份源数据在第二个 primary agent 下再迁一次会撞 id，`Ensure` 会静默不更新。必须显式检测并记为 `owned-by-another-agent`。
- **事务**：每会话一个事务，每 500 行 flush；单会话失败回滚该会话、记入报告、继续下一个。
- **内存**：全程流式解析（逐行 decoder），不把 294 MB 读进内存；单会话峰值受最大单文件 54.2 MB 约束 —— 比 Claude 侧的 39 MB 更大，**批大小必须实测后确定**。
- **规模预估**：约 3.6 万条可产生行的记录（`response_item` 32 773 中扣除 10 163 条加密 reasoning 与 386 条 developer，加上 268 条明文思考）；DB 增量按 `sourceBytes * 1.25` 预估，干跑给预估、执行后报实际。
- **进度**：每完成一个会话回调一次（`i/N` + 累计行数），TUI 节流 200 ms；迁移在后台 goroutine 跑，不阻塞事件循环。
- **失败分类**：坏行 → 跳过并计数；整会话失败 → 报告文件路径 + 错误原文；配置写入失败 → 中止 MCP 部分但保留会话结果。

---

## 9. 测试与验收

### 9.1 单元测试（夹具 `pkg/migrate/testdata/codex/`）

夹具必须覆盖：`session_meta`（含 `context_window.window_id` 与双 `session_meta`）、`response_item` 的全部 8 个子类型、`developer` 消息、`input_image` data URI、加密 reasoning 与明文 `agent_reasoning`、`compacted`（含 `compaction` 与 `message` 两种 `replacement_history` 条目）、`turn_aborted`、legacy 与 paginated 两种 `history_mode`、孤儿工具调用。

断言：角色序列、`parts` JSON 字节、`tool_call_id` 配对、`tool_meta_json`、时间戳单调、会话聚合字段、窗口链取自源端真值。

### 9.2 续聊不变量测试（跨源共享，本期核心）

对 Claude 与 Codex **两套夹具**跑同一组断言：

1. 导入后 `storedTranscriptMessages` 的结果里，每个 `ToolCalls` 非空的 assistant 消息，其后必有 `ToolCallID` 匹配的 tool 消息。
2. 孤儿调用被补上了 cancelled 结果行，且**调用行仍在**。
3. 结果集里不含 `role=reasoning` / `role=developer` 残留。
4. 首条是 `role=user`。
5. 压缩会话：`listProjectedMessageRows` 返回的序列 = `ReplacementHistory` + 边界后的行；Codex 夹具里那条 `Compaction` 项以 `EncryptedContent` 原样出现。
6. 把结果喂给 provider 适配器的请求构造函数（不发网络），断言不返回错误 —— 这是"能续聊"最接近端到端的离线断言。

### 9.3 幂等与回放测试

同一夹具连跑两次，第二次 `migrated=0 / skipped=N`，库内行数不变；迁移后用 `ListAllMessages` 断言顺序与角色序列。

### 9.4 其他

- 用户级 MCP patch 测试：只追加、不改既有、`enabled=false` 跳过、备份生成。
- 项目级 MCP 迁移测试：`<root>/.codex/config.toml` 与 `<root>/.mcp.json` 都被转写进 `<root>/.forebrain/mcp_servers.yaml`；每工具 `approval_mode` 原值保留；`enabled=false` 的条目不出现；同名已存在时 `up-to-date`/`diverged` 两态且不覆盖；写前生成备份。
- **forebrain 不再读源文件**的回归测试：只放一个 `.mcp.json`、不放 `.forebrain/mcp_servers.yaml` 时，`mcp.LoadProjectMCPServers` 返回空；`ProjectMCPPaths` 只剩一个候选。
- 项目级权限迁移测试：`permissions.allow` 的 `Bash(go test *)` 转成 `{tool_name:"shell", rule_content:"go test *"}`；`deny`/`ask` 同理；重复规则去重记 `skipped`；`enableAllProjectMcpServers=true` **不**产生批量放行。
- Plans 抽取测试：成对标签、单会话多块编号、跨会话重名、归属到正确 ProjectKey、`up-to-date`/`diverged` 三态、无块时产出空集合。
- 记忆测试：`stage1_outputs` → note 文件名通过 `validateNoteFilename`、已存在跳过、合并触发与否两态。
- 源目录测试：留空走默认、绝对路径生效、`~` 展开、不存在的目录返回底层错误原文、缺标志物被拒。
- `state_5.sqlite` 降级测试：库缺失/损坏时标题回退、子代理回退、报告写明。
- 架构门禁：`make test`（含 `pkg/architecture` 全套）。

### 9.5 真实数据验收

`/migrate` → 选 Codex → 留空回车 → 全部项目 → 干跑 → 执行，然后：

1. `/resume` 打开**最大的那个会话**（54.2 MB），计时并确认**能一路滚到最早一条消息**（完整历史可滚是硬要求）。
2. 在一个**被压缩过**的会话（本机 17 个之一）里续一句，确认不报错、上下文成立。
3. 在一个**普通**会话里续一句。
4. `/skills` 看到已启用插件带来的 skill（新会话里）。
5. `/mcp` 看到 `node_repl`，且 `computer-use`/`cua_repl` 不在已启用集合里。
6. **项目级配置**：`/Users/doudou/workspace/hundsun/guangfa` 是现成的完整样本 —— 同时有 `.mcp.json`、`.claude/settings.json`（hooks）、`.claude/settings.local.json`（permissions + `enabledMcpjsonServers`）、`.codex/config.toml`（含 7 个工具的 `approval_mode`）。对它跑 `--project` 迁移，确认：`.forebrain/mcp_servers.yaml` 里出现 `code-review-graph` 且 7 个工具的 `approval_mode = approve` 原样保留；`.forebrain/safety.json` 里出现那 5 条 `Bash(...)` 规则；报告列出 hooks 无对应；报告写明该目录**不是 git 仓库**（实测 `git rev-parse` 报 `not a git repository`）因此写入的文件不会被加载。
   再在本仓库（是 git 且已信任）手工建一个 `.codex/config.toml` 验证生效路径，并确认删掉 `.forebrain/mcp_servers.yaml` 后 `/mcp` 里项目条目消失 —— 证明 forebrain 确实不再读源文件。
7. **Plans**：在一个含 `<proposed_plan>` 块的会话上验证抽取（本机没有真实块，需用夹具或手工构造一个 rollout 片段）。
8. 上翻输入历史能翻到 Codex 的历史输入。
9. 再跑一次 `/migrate codex`，确认全部计入 skipped，库内行数不变。
10. 回头对**已迁移的 Claude 会话**做第 2、3 步 —— §1.2 要求两个源都成立。

TUI 交互用 `run-forebrain` skill 驱动截图确认。

---

## 10. 实施步骤

1. **[S]** 源目录输入框与 `--home`：`Options.SourceRoot`、`DetectSources` 接受覆盖、`claudeRoot()` 参数化、`handleMigrate` 加 `Input` 步骤、`ParseMigrateArgs` 加 `--home`、路径校验与错误原文透出。**先做这一步**，因为后续所有 Codex 取数都要走它。
   - 依赖：无
2. **[M]** 续聊不变量：在 `pkg/migrate` 落地 §1.2 的四条（含孤儿调用补 cancelled 结果），对 **Claude 源**先补齐并测试通过。
   - 依赖：无
3. **[M]** `pkg/migrate` 骨架重构：把 `parse.go`/`sessions.go` 里与源无关的部分抽出来，`plan.go` 的分发改为真正的两路。
   - 依赖：步骤 2
4. **[L]** `codex.go` + `codex_parse.go`：探测、遍历、`state_5`/`memories_1` 快照读取、rollout 流式解析（§4.1–§4.3）。
   - 依赖：步骤 1、3
5. **[M]** 压缩边界与窗口链（§4.2 的边界小节），含 `CompactionState` 原样搬运。
   - 依赖：步骤 4
6. **[S]** 子代理（§4.4）：`thread_spawn_edges` 读取、先父后子、父缺失降级。
   - 依赖：步骤 4
7. **[S]** TOML 共享解析（§5）：提取 `pkg/tool` 的通用入口并按需支持引号表头。
   - 依赖：无
8. **[M]** `codex_assets.go`：记忆（§4.6）、全局 AGENTS.md（§4.7，含 Claude 侧同名分支）、用户级 MCP（§4.9）、skills（§4.8）、输入历史（§4.11）。
   - 依赖：步骤 1、7
9. **[M]** **项目级配置迁移**（§4.9，两个源共用）：
   a. `pkg/mcp/project_config.go` 的候选列表删掉 `.mcp.json`，契约注释同步改写；
   b. Codex `<root>/.codex/config.toml` 的 `[mcp_servers]`（含 `tools.<t>.approval_mode`）→ `.forebrain/mcp_servers.yaml`，复用步骤 7 的 TOML 入口与既有的 `writeProjectMCPDoc`；
   c. Claude `<root>/.mcp.json` → 同一个文件，删除 `nativeMCPNotes`；
   d. Claude `permissions.allow/deny/ask` → `.forebrain/safety.json`，工具名走既有映射表；
   e. `enabledMcpjsonServers` 的批准状态经 `presetEnabledConsents` 预置，改到写文件之后执行；`enableAllProjectMcpServers` 不转成批量放行。
   **不新增 `pkg/mcp` 的生产文件。**
   - 依赖：步骤 7
10. **[M]** **Plans 抽取**（§4.10）：从已迁入的 assistant 行正文抽 `<proposed_plan>` 块，按会话 cwd 归属，写入 `state.PlanDirForProject`，三态幂等。
    - 依赖：步骤 4
11. **[S]** 报告与预览：抬头按源渲染、带上解析后的源目录、Codex 特有的说明行（加密思考计数、`.system` skill 说明、项目级 MCP 原生识别、MCP 下次启动生效）。
    - 依赖：步骤 4–10
12. **[M]** 测试补齐（§9.1–§9.4）与架构门禁通过。
    - 依赖：步骤 11
13. **[S]** 真实数据验收（§9.5）。
    - 依赖：步骤 12
14. **[S]** 文档：`FOREBRAIN.md` 的 `/migrate` 说明补上 codex 与 `--home`；`/mcp` 的项目文件清单补上 `.codex/config.toml`；本方案状态更新。
    - 依赖：步骤 13

---

## 11. 风险与对策

| 风险 | 对策 |
|---|---|
| 迁进来的会话看得见但续不了 | §1.2 的四条不变量 + §9.2 的跨源共享测试；provider 请求构造做离线断言 |
| Claude 侧已迁会话同样续不了 | 步骤 2 先在 Claude 源上补齐并验证，再做 Codex |
| 读 `state_5.sqlite` 与运行中的 Codex 争锁 | 先复制再以 `mode=ro` 打开副本；失败即降级并报告，不失败整个迁移 |
| 加密 reasoning 被误当成可迁内容 | `ParseMessage` 已证明 reasoning 不入上下文；丢弃并在报告给出计数，不写占位行 |
| 两条历史流同时渲染导致消息翻倍 | `response_item` 为唯一脊柱；`event_msg` 只取 3 类；测试用 legacy 夹具断言 assistant 行数 == 源端 `message role=assistant` 数 |
| `exec` 的 JS 脚本被正则误抠成命令 | 不做 JS 解析，invocation 取脚本首行，`input` 存原文 |
| 54.2 MB 单会话 resume 卡死渲染 | 完整历史必须可滚；只能改渲染方式（分批让出事件循环 + 按 width/theme 缓存），不能截断或加 limit |
| 源目录填错导致迁了不相干的数据 | 标志物校验 + 预览抬头回显绝对路径 + 报告留痕 |
| 源端数据在开发期间自行变化 | 验收一律相对断言（干跑数 == 写入数 + 跳过数） |
| 历史含密钥（工具输出/env） | 只在本机落库；报告对 `env` 值与疑似 secret 脱敏；不写日志 |
| 本机无记忆、无子代理、无项目级 MCP、无真实 `<proposed_plan>`，覆盖不到 | 这四条只能靠夹具或手工构造验收（§9.5 第 6、7 步要求手工建一个项目配置和一个 plan 块），真实数据跑出 0 **不构成通过依据** |
| **再次把「本机没有数据」误当成「Codex 不支持」** | 凡写下「Codex 没有 X」，必须在 §2.4 留一条来自 Codex 二进制或官方文档的证据；只有 `find` 空输出不算。评审时逐条对照 §2.4 |
| 复制后源文件与 forebrain 文件各自漂移 | 这是 2026-09-17 拍板已知并接受的代价（§4.9）。缓解：报告显式写明「源文件仍在且源工具仍读它」；写前备份；同名内容不同时记 `diverged` 并**不覆盖**，让漂移可见而不是被悄悄抹掉 |
| 停止读取 `.mcp.json` 让既有用户的项目 MCP 突然消失 | 本地状态可丢弃、不写兼容层是既定规矩，但这条影响的是**用户仓库里的文件**。缓解：`/mcp` 在发现项目根存在 `.mcp.json` 或 `.codex/config.toml` 而没有 `.forebrain/mcp_servers.yaml` 时，给一行提示「检测到其它 agent 的项目 MCP 配置，`/migrate` 可以把它们迁进来」；这是提示，不是回退读取 |
| 迁入的项目 MCP 引入新的执行面（仓库文件决定启动什么命令） | 沿用既有门控：`VersionControlled && IsTrusted` + 逐条确认 + 项目作用域审批只能更严。注意迁移**写文件不受门控限制**（写到未信任目录也允许），但**加载受**，报告必须写明 |
| 把 `enableAllProjectMcpServers` 翻译成批量放行 | 明令禁止（§4.9.2）：只预置源端逐个点过名的条目 |
| 迁入的 skill 被期望当场可用 | 报告写明 skill 变更只在新会话生效 |

---

## 12. 已拍板决定（2026-09-17）

1. **项目级配置一律迁进 forebrain 自己的项目配置文件，forebrain 不读取 Claude Code 与 Codex 的项目级配置文件。**（§4.9）
   - 覆盖 Codex 的 `<root>/.codex/config.toml` 与 Claude 的 `<root>/.mcp.json`、`.claude/settings*.json`。
   - `CLAUDE_CODE_MIGRATION_PLAN.md` §4.7 的「`.mcp.json` 不迁、原生读取」与决策 3 相应条目作废。
   - 已知代价：源文件与 forebrain 文件此后各自漂移；报告必须显式告知。
   - 被否决的替代方案：把 `.codex/config.toml` 加进 `pkg/mcp` 的原生候选（原 §4.9 决定 A）。

2. **加密 reasoning 只迁明文（463 条），其余 9895 条不入库。**（§4.2）
   依据：`state.ParseMessage` 对 `role=reasoning` 返回 `ok=false`，reasoning 根本不进模型上下文，搬密文进来既帮不到续聊也是死重量。报告给出「N 条思考因源端加密未迁移」。

3. **`compacted` 的 `guardian_history` 与 `compaction_response_id` 不迁。**（§4.2）
   依据：`pkg/llm/openai/responses_llm.go:642-652` 回放压缩项时只发 `type`/`id`/`encrypted_content` 三个字段，这两个字段没有任何消费者；forebrain 的 `guardian` 是无关概念（审批复核子代理）。迁进来需要新造一个永远不会被读取的部件类型。
