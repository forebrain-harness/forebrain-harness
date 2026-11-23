# 207 — 配置收尾：段集合定稿、旧段旧字段迁移、建库写入全部默认值、文件白名单

- **基线**：`06f1bc57`（dev）。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / M / MED（改启动序里的导入与默认值；迁移改写每个配置段）
- **依赖**：203、204、205、206 全部落地（本计划清理它们删掉的字段，段集合以它们之后为准）。
- **执行隔离**：基于 dev HEAD 的 worktree 或主树 in-tree；**零 commit**（owner 手动提交）。

> **执行者须知**：逐步执行，每步跑「验证」确认结果再进下一步。碰到 §8 STOP 条件就停下报告。完成后把 `plans/README.md` 里 207 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：确认 203–206 在 README 里都是 DONE；`grep -n "rootConfigSections = " -A20 pkg/process/config_db.go` 的段表应为 §3.1 列出的集合，不一致就 STOP。

## 0. 为什么做、owner 裁决（执行期不得改）

裁决全文在 `plans/191-web-config-ui.md` §8.6 第 6 条、§8.7、§8.14、§8.20、§8.24、§8.25、§8.27：

1. **库里一定有值**：初始化数据库时把**全部**存库配置项的默认值写进 `fb_config`；界面一律显示库里存的值，不用「留空＝默认」的占位。「当前模型」「不启用」「内置抓取」这类取值存成**明确表示**。以后每新增一个存库配置项，都要同时登记默认值并让老库补齐。
2. **187 的「首次导入 + 重写 forebrain.yaml」保留**：触发条件不能再是「`fb_config` 全表为空」（建库即有默认值），改为**独立的导入标记**；顺序＝默认值 → 文件里的值覆盖 → 重写文件（留备份）。
3. **forebrain.yaml 只放三项**（「文件支持项」白名单）：`gateway.http_addr`、`gateway.auth.token`、`credentials.chatgpt`。重写文件只留它们；文件里写的其它键**静默忽略**（不报错、不中断启动）。多库计划会再加一个数据库段——白名单必须是**单一来源**，方便它追加。
4. **状态库迁移规则有效**：旧段行、旧字段要用有版本号的迁移清理干净；迁移后的老库与新建库一致。「不考虑老用户」＝不把旧值换算成新配置。

## 1. 交付

- 默认值登记表（单一来源）+ 启动时补齐缺失默认值（新库、老库同一条路径）。
- 一次数据迁移：删旧段行、剔除旧字段、把空值改成明确表示。
- 导入标记取代「全表为空」。
- 文件白名单单一来源 + 只读端点 + 文件解析只认白名单。

## 2. 现状事实（已对基线核实）

### 2.1 段表与解码（`pkg/process/config_db.go`）

- `rootConfigSections`（32-49 行，基线 16 段）与 `rootConfigSectionFields`（init 时反射校验，不一致即 panic）。
- `composeSectionsFrom`（118-152）：先读 YAML 得 `Root`，把所有存库段字段清零，再逐行 `json.Unmarshal` 库里的段——**多余字段忽略**；非 Root 行（`auth.*`）跳过。
- `MarshalRootSection`（232-249）：字段为零值时**不存行**。

### 2.2 首次导入（`pkg/process/config_import.go`）

- `importLegacyConfigIfNeeded`（39 起）：`sections, _ := store.LoadSections(ctx)`；`if len(sections) == 0 { importLegacyYAMLSections(...) }`——**全表为空才导入**。
- `importLegacyYAMLSections`（96 起）：逐段 `json.Marshal` YAML 里的非零段写库（`${ENV}` 引用按 `.env` 解析回明文）。
- `rewriteMinimalConfigFile`（249 起）：备份为 `forebrain.yaml.pre-db-<时间戳>`（已有备份不覆盖），重写为只含 `gateway` + `credentials` 的文件。
- 新建 home：`ensureStartupConfigFile`（`config_db.go:189-221`）写一个只有 `gateway` + `credentials` 的最小文件。

### 2.3 文件解析

`appcfg.LoadPersisted` / `Load`（`pkg/config/load.go:100-120`）把整个文件 `yaml.Unmarshal` 进 `Root`；存库段读进来后在 compose 时被清零，但**类型错误会在 Unmarshal 时就失败**（例如文件里写了 `memories: 5`），校验也可能对存库段的值报错。

### 2.4 空值即默认的字段（今天的约定）

原型（`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/config-db-settings-preview.html`）里：联网搜索服务 `''`＝不启用、网页抓取服务 `''`＝内置抓取、记忆抽取 / 整合模型 `''`＝当前模型；`*bool` / `*int` 的 `nil`＝默认值。

## 3. 设计

### 3.1 段集合（203–206 之后应为）

`sandbox_workspace_write`、`compact`、`cron`、`agents`、`hooks`、`memories`、`features`、`lsp`、`approval_preset`。非 Root 行：`auth.chatgpt`、`auth.opencode`、`gateway.observed_origin`（206）、本计划新增的 `meta.legacy_yaml_imported`。若实际段表多出别的，STOP。

### 3.2 默认值登记表（单一来源）

- 在 `pkg/process` 新增 `config_defaults.go`：`func defaultStoredRoot() appcfg.Root`——构造一个**每个存库字段都显式赋值**的 `Root`：`*bool` 填具体值、`*int` 填具体值、字符串填明确表示（3.3）、列表填空切片、`compact.prompt` 填 `assembly.DefaultPrompt`、`agents.defaults.execution.max_parallel_subagents` 填 `max(1, runtime.NumCPU()-1)`。放 `pkg/process` 是因为它能引用 `assembly`（`pkg/config` 引用会成环）。
- `func ensureStoredConfigDefaults(ctx, store)`：对每个存库段，把默认值 JSON **深合并到已有 JSON 之下**（已有的键一律保留，只补缺失的键；数组与对象按「整体缺失才补」处理，不逐元素合并），有变化才 `UpdateSection`。幂等。
- 调用时机：`Open` 里迁移之后、首次导入之后、第一次 compose 之前（与 `importLegacyConfigIfNeeded` 同一处）。新库与老库走同一条路径，因此「老库补齐 = 新库默认」天然成立。
- **覆盖测试**：反射遍历 `Root` 的每个存库段，断言 `defaultStoredRoot()` 序列化后不存在零值的叶子（`omitempty` 被省略的键）——以后谁加字段不登记默认值，这个测试会红。

### 3.3 明确表示

| 字段 | 旧空值含义 | 存成 |
|---|---|---|
| `agents.defaults.web_search.provider` | 不启用 | `none` |
| `agents.defaults.web_fetch.provider` | 内置抓取 | `builtin` |
| `memories.extract_model`、`memories.consolidation_model` | 当前模型 | `current` |

读者（`grep -rn "WebSearch.Provider\|WebFetch.Provider\|ExtractModel\|ConsolidationModel" pkg`）把新值与旧空值同等对待；值域校验接受新值。执行时若发现更多「空即默认」的字符串字段，补进这张表并在 README 结果里列出——超过 3 个就 STOP 先报告。

### 3.4 数据迁移（一个新版本，`stateSchemaVersion+1`）

在迁移函数里用 Go 处理 JSON（迁移拿到的是 `*sql.Conn`，可以先 `SELECT section, value_json FROM fb_config` 再逐行 `UPDATE`），**清单写死在迁移里**（迁移不得引用会随代码变化的结构体）：

1. 删段行：`approval_policy`、`approvals_reviewer`、`auto_review`、`default_permissions`、`permissions`、`sandbox_mode`、`tools`、`windows`。
2. 剔除字段（键路径，按段内 JSON）：
   - `compact`：`model_auto_compact_token_limit`、`model_auto_compact_token_limit_scope`、`remote_compaction`、`remote_compaction_v2`
   - `memories`：`use_memories`、`generate_memories`、`dedicated_tools`、`disable_on_external_context`、`min_rate_limit_remaining_percent`
   - `features`：`exec_permission_approvals`、`request_permissions_tool`、`network_proxy`
   - `sandbox_workspace_write`：`go_cache_mode`、`exclude_tmpdir_env_var`、`exclude_slash_tmp`
   - `agents`：`defaults.context_inject.warn_remaining_tokens`、`defaults.context_inject.block_remaining_tokens`、`defaults.guardrails.output`、`defaults.guardrails.retrieval`；以及 `definitions.*` 下同名路径（若主代理定义里允许覆盖这些字段——执行时核实）
3. 3.3 表里的四个字段：值为 `""` 或缺失时写成明确表示。
4. **不碰**：`auth.*`、`gateway.observed_origin`、`meta.*`。
5. 测试：照 `pkg/state/schema_migrations_test.go` 的降版本夹具写法，插入含全部旧段旧字段的行 → 迁移 → 断言清单里的段行与字段都没了、其它字段原样、非 Root 行原样。

> 迁移只删；新字段的默认值（`auto_compact_percent`、`warn_remaining_percent`、`approval_preset` 等）由 3.2 的启动补齐写入，不在迁移里重复。

### 3.5 导入标记

- `importLegacyConfigIfNeeded` 的触发改为：读 `meta.legacy_yaml_imported` 行；**没有标记**时——若库里已有任一 Root 段行（187 时代已导入过）→ 只写标记；否则 → 导入文件里的段，再写标记。有标记 → 什么都不做。
- 顺序保证「文件覆盖默认值」：导入在 3.2 补齐之前跑，补齐只填缺失键。

### 3.6 文件白名单（单一来源）

- `pkg/config` 新增 `FileSupportedKeys() []string`，返回 `gateway.http_addr`、`gateway.auth.token`、`credentials.chatgpt`；多库计划追加数据库段时只改这里。
- 新增文件专用结构 `type FileRoot struct { Gateway …; Credentials … }`（只含白名单字段）；`LoadPersisted` / `Load` 读 YAML 时**先解析进 `FileRoot`**（未知键自然忽略，存库段的类型错误也不会报），再拷进 `Root`。`yaml.Unmarshal` 用非严格模式。
- `rewriteMinimalConfigFile` 与 `ensureStartupConfigFile` 都直接 marshal `FileRoot`。
- 只读端点 `GET /api/config/file-schema` → `{"keys": [...]}`，给网页配置文件编辑器画「不存在」波浪线（191 用）；路由放在 `/api/config` 同组。
- 覆盖测试：`FileRoot` 的 yaml 叶子键集合 == `FileSupportedKeys()`。

## 4. 步骤

每步结束都跑：`CGO_ENABLED=1 go build -tags fts5 ./... && go vet -tags fts5 ./pkg/...`（期望：无输出）。

1. 核对段表（drift check）。
2. 3.3 明确表示：改读者与校验。
3. 3.2 默认值登记表与补齐；覆盖测试。
4. 3.4 数据迁移与测试。
5. 3.5 导入标记。
6. 3.6 文件白名单、`FileRoot`、端点、覆盖测试。
7. TUI `/config`：面板直接显示库里的值（库里一定有值后，「未设置」的显示分支若只为空值存在就删掉）；`go test -tags fts5 ./pkg/tui/ -run ConfigPanel -count=1`。
8. 包图：`scripts/package-graph.sh`。

## 5. 测试（放在被测文件同名的 `_test.go` 里）

- `pkg/process/config_defaults_test.go`：新建 home 打开后，每个存库段都有行、无零值叶子；`max_parallel_subagents == max(1, NumCPU-1)`；`compact.prompt == assembly.DefaultPrompt`；补齐幂等（第二次打开零写入，比 `updated_at_ms`）。
- `pkg/process/config_import_test.go`：
  - 旧文件里有 `compact.auto_compact_percent: 70`、无标记、库空 → 导入后为 70、其余为默认、文件被重写为只含白名单、有备份、写了标记；
  - 库里已有段行、无标记（187 时代）→ 不再导入、只写标记；
  - 有标记 → 文件里再写的存库段被忽略。
- `pkg/config/load_test.go`：文件里写 `memories: 5`、`approval_policy: never`、拼错的键 → 启动不报错，`Root` 里只有白名单字段来自文件。
- `pkg/state/schema_migrations_test.go`：3.4 第 5 条。
- `pkg/gateway`：`GET /api/config/file-schema` 返回三项。

## 6. 完成判据（机器可查）

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/process/... ./pkg/config/... ./pkg/state/... ./pkg/gateway/... ./pkg/tui/... -count=1 -p 2   # 全 ok
go vet -tags fts5 ./...                                                       # 无输出
```

全量回归：`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -p 2`。

**真机**：用一个 187 时代的旧 home 副本（隔离 FOREBRAIN_HOME）启动一次网关，`sqlite3 <home>/forebrain.state.sqlite "SELECT section FROM fb_config ORDER BY 1"` 只剩 3.1 的段与非 Root 行；`forebrain.yaml` 只剩白名单三项。

## 7. 维护注意

- **以后每加一个存库配置项**：在 `defaultStoredRoot()` 里给它显式默认值（覆盖测试会逼你做）；老库由启动补齐自动拿到，不需要另写迁移。删字段则要写迁移剔除。
- 多库计划会把 3.4 的迁移与 3.2 的补齐扩展到 MySQL / PostgreSQL；本计划只写 SQLite（SQL 语句保持 SQLite 语法）。
- 191 的网页页面直接展示库里的值；配置文件编辑器用 `/api/config/file-schema` 画波浪线。

## 8. STOP 条件

- 段表与 3.1 不一致。
- 3.3 之外「空即默认」的字符串字段超过 3 个。
- 主代理定义（`agents.definitions.*`）允许覆盖被删字段，但其结构与 `defaults` 不同、无法用同一路径清单剔除。
- 把文件解析改成 `FileRoot` 后，有启动路径（setup 向导、迁移命令 `pkg/migrate`）依赖从文件读存库段。
