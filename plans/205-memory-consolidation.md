# 205 — 记忆收口：一个开关、跑批不生成、外部上下文固定停用、删限流余量、增量提取

- **基线**：`06f1bc57`（dev）。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / L / MED（含两次状态库变更：跑批会话数据修正 + 新列；增量提取改了第一阶段提示词）
- **依赖**：204 先落地（同改 `pkg/config/config.go`、`load.go`、`pkg/tui/config_panel_schema.go`）。之后是 206。
- **执行隔离**：基于 dev HEAD 的 worktree 或主树 in-tree；**零 commit**（owner 手动提交）。

> **执行者须知**：逐步执行，每步跑「验证」确认结果再进下一步。碰到 §8 STOP 条件就停下报告。完成后把 `plans/README.md` 里 205 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：`git diff --stat 06f1bc57 -- pkg/memory pkg/run/memory_llm.go pkg/llm/openai/auth.go pkg/process/open.go pkg/process/config_apply.go pkg/state/schema.sql pkg/state/schema_migrations.go pkg/gateway/memories_api.go pkg/tui/chat_slash.go pkg/tui/overlays.go pkg/tui/commands.go`。除 203 / 204 的改动外有输出，就逐条对照 §2；对不上就 STOP。

## 0. 为什么做、owner 裁决（执行期不得改）

裁决全文在 `plans/191-web-config-ui.md` §8.9（7、8 条）、§8.11、§8.12、§8.21、§8.22：

1. **「使用记忆」「生成记忆」「专用工具」三个配置项删除**：`features.memories`（「记忆功能」）一个开关同时管读、写、专用工具。
2. **后台跑批项目不使用记忆、不可配置**：跑批会话**建出即 `memory_mode=disabled`**，单会话开关拒绝把它改成 `enabled`；状态库里已有的跑批会话改为 `disabled`，并撤回它们对已整合记忆的贡献。修根因，不在查询里加防御分支。
3. **「外部上下文时停用」固定开启、删配置**（`memories.disable_on_external_context`）：用过联网搜索、网页抓取、MCP 工具结果、OpenAI 内置联网搜索的会话一律不用于生成记忆，已参与整合的照旧触发撤回。owner 已知悉代价（安全优先）。
4. **记忆「限流余量」功能与配置删除**（`memories.min_rate_limit_remaining_percent`，连同对 ChatGPT 用量接口的请求）。
5. **记忆增量提取**（owner 选 A）：老会话再次提取时**只读上次之后的新消息**，并把上次的提取结果一起交给模型，输出合并后的完整新结果。
6. 状态库结构 / 数据变更**必须写有版本号的迁移**，迁移后老库与新建库一致。

## 1. 交付

- 配置：`MemorySection` 去掉 `UseMemories`、`GenerateMemories`、`DedicatedTools`、`DisableOnExternalContext`、`MinRateLimitRemainingPercent`。
- `pkg/memory/settings.go` 的三个判断收敛为 `Enabled`。
- 跑批会话出生即 `disabled`；`SetThreadMemoryMode` 拒绝跑批会话；数据迁移。
- 限流检查整条删除。
- `fb_memory_stage1_outputs.last_message_row_id` 新列 + 迁移；按行号增量读取；增量提示词。
- 网页 / TUI 的记忆设置三开关变一个（只做到能编译、能用；网页样式由 191 重做）。

## 2. 现状事实（已对基线核实）

### 2.1 配置与三个判断

`MemorySection`（`pkg/config/config.go:238-257`）与 `EffectiveMemories()`（273-291）：`DisableOnExternalContext` 默认 false、`GenerateMemories` / `UseMemories` / `DedicatedTools` 默认 true、`MinRateLimitRemainingPercent` 默认 25。归一化 `pkg/config/load.go:834,843` 一带。

`pkg/memory/settings.go:21-65`：

```go
func SessionModeForConfig(cfg *appcfg.Root) string {
	if GenerationEnabled(cfg) {
		return ThreadMemoryEnabled
	}
	return ThreadMemoryDisabled
}
func Enabled(cfg *appcfg.Root) bool { return cfg != nil && cfg.EffectiveFeatures().Memories }
func ReadPathEnabled(cfg *appcfg.Root) bool { return Enabled(cfg) && cfg.EffectiveMemories().UseMemories }
func DedicatedToolsEnabled(cfg *appcfg.Root) bool { return ReadPathEnabled(cfg) && cfg.EffectiveMemories().DedicatedTools }
func GenerationEnabled(cfg *appcfg.Root) bool { return Enabled(cfg) && cfg.EffectiveMemories().GenerateMemories }
```

`Settings` 结构（同文件开头）有 `UseMemories`、`GenerateMemories` JSON 字段，网关记忆设置接口与网页 `MemorySwitchesTab` 读写它；TUI `/memories` 设置浮层与 `UpdateMemorySettings`：`pkg/tui/overlays.go:4465`、`pkg/tui/commands.go:2218`、`pkg/tui/chat_slash.go:1380-1410`。

### 2.2 外部上下文（`pkg/memory/settings.go:83-105`）

```go
func MarkPollutedByExternalContext(ctx context.Context, cfg *appcfg.Root, store *Store, sessionID string) (bool, error) {
	if !Enabled(cfg) || !cfg.EffectiveMemories().DisableOnExternalContext || store == nil {
		return false, nil
	}
	...
```

调用来源：联网搜索 / 网页抓取（`pkg/tool/registry.go:152,158` 的 `SetContainsExternalContext(true)`）、MCP 工具结果（`pkg/run/runner.go:399`）、OpenAI 内置联网搜索（`pkg/llm/openai/responses_llm.go:1399`）。

### 2.3 跑批与记忆

- 读取侧已关：`pkg/tool/registry.go:104`、`pkg/tool/session_tools.go:82`、`pkg/run/runner.go:1064-1068`。
- 生成侧没关：新会话 `memory_mode` 取进程级默认（`pkg/process/open.go:493` → `SessionModeForConfig`；配置变更时 `pkg/process/config_apply.go:80` 同步默认），与项目类型无关。
- Stage 1 认领（`pkg/memory/jobs.go:149-160` `ClaimStage1JobsForStartup`）只按 `memory_mode`、`memory_source`、空闲时间、`source` 筛，不看 `fb_sessions.project_type`；Phase 2 选材（`pkg/memory/store.go:270` 一带）同样只看 `memory_mode`。
- 单会话开关：`Store.SetThreadMemoryMode`（`pkg/memory/store.go:112-131`），入口 TUI `chat_slash.go:1422`、网关 `memories_api.go:88`。
- `fb_sessions.project_type` 出生即定（`pkg/state/schema.sql:81`）。撤回做法参照 `MarkPollutedByExternalContext`：`HasSelectedStage1Output` → 标记 → 已被选中则 `EnqueueGlobalPhase2(ctx, scope, 0)`。

### 2.4 限流余量

`pkg/run/memory_llm.go:163-207` `memoryRateLimitGuard`（请求 `openai.UsageURL`，`pkg/llm/openai/auth.go:32`，唯一使用者）、注入点 `memory_llm.go:54-56`；流水线 `Pipeline.RateLimitGuard` 字段与 `acquireRateLimit`（`pkg/memory/pipeline.go:41,144-147`）、两处调用（89-91、121-123）、依赖注入（615-618、653-655）。

### 2.5 第一阶段提取（全量重读）

`pkg/memory/pipeline.go:235-262` `runStage1`：

```go
	messages, err := p.Transcripts.ListMemoryTranscriptMessages(ctx, claim.Thread.ThreadID, 0)
```

接口在 `pipeline.go:20`。判重按 `source_updated_at` / `last_success_watermark`（`jobs.go:206-219`）。截断：`pkg/run/memory_llm.go:282-295` `MiddleTokens` 首尾保留到 ≤32000 词元。无产出：`MarkStage1SucceededNoOutput`。表结构（`pkg/state/schema.sql:419-435`）：

```sql
CREATE TABLE fb_memory_stage1_outputs (
  thread_id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL,
  project_key TEXT NOT NULL CHECK (TRIM(project_key) <> ''),
  source_updated_at INTEGER NOT NULL,
  raw_memory TEXT NOT NULL,
  rollout_summary TEXT NOT NULL,
  rollout_slug TEXT,
  generated_at INTEGER NOT NULL,
  ...
) STRICT;
```

### 2.6 迁移框架（`pkg/state/schema_migrations.go`）

`stateSchemaVersion`（21 行，基线 24；203 之后为 25）；`stateSchemaMigrations` 末尾登记；加列范例 `migrateStateV3ToV4`（74-87 行：先查 `pragma_table_info`，已有则跳过，再 `ALTER TABLE … ADD COLUMN`）。一致性测试 `TestStateMigrationMatchesFreshSchema`（`schema_migrations_test.go:98`）比对 `sqlite_master`——新列必须同时写进 `schema.sql`。

## 3. 设计

### 3.1 一个开关

`Enabled(cfg)` 是唯一判断。`ReadPathEnabled` / `GenerationEnabled` / `DedicatedToolsEnabled` 删除，调用点改为 `Enabled`；`InstructionOptionsFromConfig` 三项都取 `Enabled(cfg)`；`Settings` 去掉 `UseMemories` / `GenerateMemories`（接口只剩一个 `enabled`）。`SessionModeForConfig(cfg)` 改为 `SessionModeFor(cfg, projectType string)`：跑批一律 `disabled`，否则 `Enabled(cfg)` 决定。

### 3.2 跑批

- 建会话写 `memory_mode` 的地方（从 `open.go:493` 追到真正 INSERT `fb_sessions` 的函数）按项目类型取值。
- `SetThreadMemoryMode`：先查该会话 `project_type`，是 `batch` 且要设 `enabled` → 返回错误（中文：「后台跑批项目不使用记忆」）。
- 数据迁移（与 3.4 的加列放在**同一个**新版本里，单事务）：

```sql
UPDATE fb_sessions SET memory_mode='disabled' WHERE project_type='batch' AND memory_mode<>'disabled';
```

  并删除这些会话的 `fb_memory_stage1_outputs` 行。若其中有 `selected_for_phase2=1` 的，迁移**只能写库、不能调 Go 侧流水线**——在迁移里为对应作用域插入一条 Phase 2 待办（查 `EnqueueGlobalPhase2` 写的是哪张表、哪些列，照它的 SQL 写；找不到等价 SQL 就 STOP）。

### 3.3 外部上下文

`MarkPollutedByExternalContext` 去掉 `DisableOnExternalContext` 条件：`if !Enabled(cfg) || store == nil { return false, nil }`。

### 3.4 增量提取

- 新列 `last_message_row_id INTEGER`（可空），同时写进 `schema.sql` 与迁移（照 `migrateStateV3ToV4`）。旧行 NULL＝下一次全量提取一次，之后转增量。
- `SessionStore` 新增 `ListMemoryTranscriptMessagesAfter(ctx, sessionID string, afterRowID int64) ([]llm.Message, int64 /*last row id*/, error)`，复用 `listAllStoredMessages` 与 `messagesFromStoredRows`，同样跳过压缩边界行；全量读取也要能返回最后一条行号。
- `runStage1`：有上次行号 → 只读之后的消息；把上次的 `raw_memory`、`rollout_summary` 作为「已有提取结果」交给模型；输出替换原行，并写入新的 `last_message_row_id`。
- 提示词：第一阶段系统提示词（`stageOneSystem`）分全量与增量**两个常量**，增量版说明「下面先给出这段会话此前的提取结果，再给出此后新增的消息；输出合并后的完整结果」。提示词是模型可见文本，遵守仓库「provider-neutral、byte-stable」规则，配 golden 测试（照仓库已有的提示词 golden 测试放）。
- 截断：新增部分超限仍用 `MiddleTokens`；上次结果不截断。
- 边界：上次无产出 → 已有结果为空；新增部分为空 → 不调模型，直接推进行号与时间戳。

## 4. 步骤

每步结束都跑：`CGO_ENABLED=1 go build -tags fts5 ./... && go vet -tags fts5 ./pkg/...`（期望：无输出）。

1. **一个开关**：按 3.1 改；删 `UseMemories` / `GenerateMemories` / `DedicatedTools` 字段与默认值；改所有调用点（`grep -rn "ReadPathEnabled\|GenerationEnabled\|DedicatedToolsEnabled\|UseMemories\|GenerateMemories\|DedicatedTools" pkg frontend/src`）。
2. **外部上下文固定**：按 3.3；删 `DisableOnExternalContext` 字段、默认、归一化（`load.go:834`）。
3. **限流余量删除**：按 2.4 清单删；`memory_llm.go` 不再用到的 import 一并去掉；删 `openai.UsageURL`（先 `grep -rn UsageURL pkg` 确认唯一使用者）。
4. **跑批**：按 3.2 改建会话与 `SetThreadMemoryMode`；网关 `memories_api.go:88` 与 TUI `chat_slash.go:1422` 把错误原样展示给人。
5. **状态库迁移**：新版本 = 执行时 `stateSchemaVersion+1`，单个迁移函数里做 3.2 的数据修正 + 3.4 的加列；`schema.sql` 同步加列。
6. **增量提取**：按 3.4 实现。
7. **TUI 与网页**：TUI `/memories` 设置浮层只剩「记忆功能」一个开关；网页 `MemorySwitchesTab` 同样一个开关（最小改动，能编译、能保存；不做新样式）；TUI `/config` 描述符删被删字段（`go test -tags fts5 ./pkg/tui/ -run ConfigPanel -count=1`）。
8. **测试修整**：删只测已删功能的用例。
9. **包图**：`scripts/package-graph.sh`。

## 5. 测试（放在被测文件同名的 `_test.go` 里）

- `pkg/memory/settings_test.go`：记忆功能关 → 读、写、专用工具全关；开 → 全开；跑批项目 `SessionModeFor` 恒为 `disabled`；外部上下文不再看配置。
- `pkg/memory/store_test.go`：`SetThreadMemoryMode` 对跑批会话设 `enabled` 返回错误、设 `disabled` 成功。
- `pkg/memory/jobs_test.go`：开着记忆跑一个跑批任务，Stage 1 不认领它。
- `pkg/state/schema_migrations_test.go`：旧跑批会话迁移后为 `disabled`，其第一阶段结果被删、被选中过的作用域有 Phase 2 待办；新列存在、旧行为 NULL；`TestStateMigrationMatchesFreshSchema` 通过。
- `pkg/memory/pipeline_test.go`（已有 `staticTranscriptSource` 夹具，照它扩展）：
  - 先全量提取，再追加 3 条消息并闲置 → 第二次请求体只含这 3 条与上次结果（断言不含旧消息正文），结果替换原行、行号推进；
  - 旧行 `last_message_row_id IS NULL` → 走一次全量；
  - 新增为空 → 零模型调用；
  - 上次无产出 → 增量时已有结果为空。
- 提示词 golden 两份。

## 6. 完成判据（机器可查）

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/memory/... ./pkg/state/... ./pkg/run/... ./pkg/process/... ./pkg/gateway/... ./pkg/tui/... ./pkg/tool/... ./pkg/config/... -count=1 -p 2   # 全 ok
go vet -tags fts5 ./...                                                   # 无输出
grep -rn "UseMemories\|GenerateMemories\|DedicatedTools\|DisableOnExternalContext\|MinRateLimitRemainingPercent\|RateLimitGuard\|UsageURL" pkg --include='*.go'   # 无输出
cd frontend && pnpm typecheck && pnpm test                                # 全绿
```

**真机验收**（fake driver 驱动不了提取流程，按仓库约定用 glm-5.3，隔离 home，key 从 `.env` 注入）：一个长会话两次提取，记录两次的输入词元数，第二次只与新增量成正比。结果写进 README。

## 7. 维护注意

- 207 会清理 `fb_config` 里 `memories` 段的旧字段并补默认值。
- 网页记忆设置页由 191 按原型重做；本计划只保证接口与编译。
- 行为变化：①ChatGPT 订阅用户的后台记忆不再因额度紧张暂停；②常联网 / 重度用 MCP 的会话不再攒记忆；③跑批会话的旧记忆贡献被撤回一次。

## 8. STOP 条件

- 找不到在迁移里（纯 SQL）为作用域排 Phase 2 待办的等价写法。
- `listAllStoredMessages` 拿不到稳定的行号（例如行号会因压缩或重排变化）——报告，不要改用时间戳凑。
- 增量提取在 golden / 单测以外，发现第二阶段整合依赖第一阶段结果的某种「只追加」语义。
- 跑批会话的建会话路径不止一个，且有路径拿不到项目类型。
