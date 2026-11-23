# 修复：订阅模型（volcengine kimi-k3.1-pro 等）被误判「不支持图片输入」而拒绝整轮

> **Executor instructions**: 按步骤执行；每步先跑验证命令确认预期结果再前进。
> 触发任何 STOP condition 立即停下报告，不要即兴发挥。改动留在工作区，**不 commit**。
>
> **Drift check（先跑）**:
> - `git rev-parse --short HEAD` → `c32d23d5`
> - `git status --short` → 只允许出现别人的未跟踪测试文件（如 `pkg/tui/zz_echo_repro_test.go`）；上列 in-scope 路径必须为空。
> - 抽查 `grep -n "func offerRowInput" -A 12 pkg/subscription/registry.go` 与 `grep -n "SupportsAttachments bool" -A 2 pkg/turn/models.go`，
>   内容须与本文「Current state」一致；**行号漂移不算偏差，内容不符 → STOP**。

## Status

- **Priority**: P1（owner 报障的用户可见 bug：合法多模态模型被拒绝整轮）
- **Effort**: S–M（含 owner 裁决并入的同类缺口）
- **Risk**: LOW（去掉伪造数据 + 一次确定性数据修复；行为矩阵已穷举）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `c32d23d5`, 2026-11-13（工作区基线，除他人未跟踪测试文件外无未提交改动；评审后按 owner 两项裁决修订）
- **执行方式**: owner 裁决的 plan-gated-bugfix 流程——批准后**直接在当前源码实施**（不用 worktree、不 commit）；
  实施前把本计划抄录到仓库 `docs/plan/SUBSCRIPTION_IMAGE_CAPABILITY_FABRICATION_PLAN.md`（gitignore 内，不入库），
  验收记录回填到该文件。

## Why this matters

owner 登录 Volcengine Ark（Agent Plan）并在 `/connect` 里添加了 `kimi-k3.1-pro`，发一条带图消息，整轮被拒：

```
the session model volcengine/kimi-k3.1-pro does not accept image input and this conversation contains images;
the message is kept, switch model with /model and continue
```

而 `kimi-k3.1-pro` 实际支持图片输入（`assets/models.json` 里 `moonshotai/kimi-k3.1-pro` 的
`modalities.input = [text, image, video]`、`attachment: true`）；同一模型的 Coding Plan 行（老版本写入、`cap_images` 为空）
不会被拒——差别只在库里那一列，说明是本地能力数据被伪造，不是厂商拒绝。

后果：用户被误导去换模型；图片在「模型支持图片」的前提下被永久挡在本地。同类模型不止一个（见现场证据表），
且同类缺口还有第二个生产者（OpenCode/ChatGPT 订阅的**本地不认识的新 id**，见 Step 2b）：任何一个列表新模型都会踩同一坑。

## 根因（file:line 证据链 + 现场证据）

### 链路

1. `/connect` 火山登录 → `Offers()` 列 plan 模型（厂商列表**只给 model id**，不带能力）：
   `pkg/subscription/volcengine.go:450-468` `volcengineModelOffers` →
   `SupportsImages: volcengineModelImages(model)`（`:463`）。
2. `volcengineModelImages`（`pkg/subscription/volcengine.go:513-524`）只在**本仓内置表**里查能力：

```go
func volcengineModelImages(model string) bool {
	provider, ok := appcfg.BuiltinProviderByID(volcengineID)
	if !ok {
		return false
	}
	for _, entry := range provider.Models {
		if entry.ID == model {
			return entry.Images
		}
	}
	return false            // ← 表里没这个名字 = 「不支持图片」，与「表里写了 Images:false」不可区分
}
```

   内置表 `pkg/config/provider_registry.go:237-248` 有 `kimi-k3`、没有 `kimi-k3.1-pro`：

```go
Models: []BuiltinModel{
	{ID: "doubao-seed-evolving", ContextWindow: 1024000, MaxOutput: 256000, Images: true},
	{ID: "doubao-seed-2.1-pro", ContextWindow: 256000, MaxOutput: 256000, Images: true},
	...
	{ID: "kimi-k3", ContextWindow: 1024000, MaxOutput: 128000, Images: true},
	{ID: "deepseek-v4-pro", ContextWindow: 1024000, MaxOutput: 384000},
	...
},
```

3. `offerRowInput` 把这个 `false` 当成**厂商声明**写进模型行（`pkg/subscription/registry.go:408-443`，关键两行 422-423）：

```go
	if offer.ContextWindow > 0 {
		window := offer.ContextWindow
		input.CapContextWindow = &window
	}
	images := offer.SupportsImages
	input.CapImages = &images          // ← 注释原话：「the image switch is always an answer, because "no images" is one」
```

4. `pkg/process/llm_models.go:753-756`（新建）/ `:710-713`（更新）把它落到 `fb_llm_models.cap_images`。
   注意 `CapContextWindow` 的契约（同文件 :639-644）恰好相反：「a caller that does not know the window names nil rather than 0」。
5. 读侧：`pkg/llm/catalog.go:559-565` `LookupRow` 用行覆盖 catalog，`applyModelOverride`（`:723-726`）
   `hit.SupportsAttachments = *override.SupportsImages`，并且**返回 `ok=true`**——哪怕 catalog/内置表都没有这个模型。
6. 闸门：`pkg/run/config.go:2556 modelHitFor` → `pkg/run/image_input.go:232-250 imageInputRefusedModel`
   `if !ok || hit.SupportsAttachments { fail open }` 不成立 → 拒绝；句式由 `ImageInputRefusedError.Error()`
   （`pkg/run/image_input.go:41-54`）产出，正是 owner 截图那一句。
   同一条数据还驱动工具侧的 `(*Runner).ImageInputRefusal`（`pkg/run/image_input.go:81-91`，`read_file` 读图时的
   "switch model with /model, then read the image again"）。

### 同类缺口的第二个生产者（owner 裁决：本期一并修）

`pkg/subscription/registry.go:360-376 offersFromRecords` 把订阅的 `turn.ModelRecord` 投影成 offer，
`SupportsImages: record.SupportsAttachments`。而 record 的 `SupportsAttachments` 在下面两条路径上同样是「没人问过」的零值：

- `pkg/turn/models.go:139-168 openCodeModelRecord`：`meta, known := opencode.LookupModelMeta(provider, id)`，
  `if !known { meta = opencode.DefaultModelMeta(id) }`，而 `DefaultModelMeta`
  （`pkg/llm/opencode/models_table.go:138-140`）只填 window/protocol，`Attachments` 留零 → record 得 false；
  `llm.Lookup` 也没命中时（models.json 没有这个 id）没人纠正它。
- `pkg/turn/models.go:285-296` ChatGPT record：`SupportsAttachments: info.SupportsAttachments()` 只看
  `input_modalities`，列表没给这个成员时同样是零值 false。

### 现场证据（owner 真实库 `~/.forebrain/state/forebrain.state.sqlite`）

`fb_llm_models` 里 volcengine 两套 plan 的同名模型：

| id | plan | model | cap_images | cap_context_window |
|---|---|---|---|---|
| `llm-4ccdc12a-…` | coding-plan | kimi-k3.1-pro | NULL | NULL（老版本写入，不拒图）|
| `llm-0511e1fc-…` | agent-plan | kimi-k3.1-pro | **0** | NULL |
| `llm-0d5cdc14-…` | agent-plan | kimi-k3 | 1 | 1024000 |
| `llm-ee766296-…` | agent-plan | glm-5.3 | 0 | 1024000 |
| `llm-9c62f76e-…` | agent-plan | glm-5.3-flash | **0** | NULL |
| `llm-b0020ff7-…` | agent-plan | kimi-k2.8-preview | **0** | NULL |
| `llm-69f5801a-…`/`llm-3c52362c-…` | agent-plan | deepseek-v4.1-flash / -economy | **0** | NULL |
| `llm-a4415c79-…`/`llm-ac085989-…`/`llm-318c9d11-…` | agent-plan | deepseek-latest / kimi-latest / glm-latest | **0** | NULL |

规律与代码一致：**内置表里点了名的模型拿到真值（kimi-k3=1、glm-5.3=0），没点名的模型一律伪造 0，且两个字段一起缺**
（旧写入路径只在内置表点名时写 window）——这个共现正是 Step 3 迁移的判据。
`fb_session_image_refusals` 全表为空（本会话没有厂商拒绝记录，排除「厂商已拒图」句式）。

### 真机复现（已做，可重跑）

1. 复制真实 state 库到临时 home，把 `agents.definitions.main.default_model` 指到 `llm-0511e1fc-…`、行 key 换成假值，
   并把最新一条 `fb_session_model_state` 指到同一行（新会话沿用「上次使用的模型」）：
   `update fb_session_model_state set model_id='llm-0511e1fc-5522-47cb-b67a-f55af0857036', provider='volcengine', model='kimi-k3.1-pro' where session_id=(select session_id from fb_session_model_state order by updated_at desc limit 1);`
   `FOREBRAIN_HOME=<临时home> build/bin/forebrain` 在临时项目里跑
   （`export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"; CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain`）。
2. 信任目录 → `C-v` 粘贴一张 PNG（composer 出现 `[Image #1]`）→ 输入 `describe this image` → Enter。

   观察（修复前）：`● error` + 与 owner 截图逐字相同的句子。
3. 把该行 `update fb_llm_models set cap_images=NULL where id='llm-0511e1fc-…'` → 同样操作：不再被拒，
   请求真的发出（假 key 收到 Ark 的 `HTTP 401 Unauthorized`）。

→ 伪造的 `cap_images=0` 是唯一触发条件；置空即放行。

### 已排除的其它解释

- **「厂商在本会话已经拒过图」**（`Observed=true` 句式）：句式是 "already refused an image in this conversation…"，
  且 `fb_session_image_refusals` 全表为空 → 排除。
- **内置表本来就说「不支持」**：表里没有 `kimi-k3.1-pro`；同会话/同 plan 的 `kimi-k3` 可以发图 → 排除。
- **同名多行导致 pair 歧义**（`overrideForPairLocked` 要求唯一）：闸门走的是**按行**的 `LookupRow(rowID,…)`，
  与 pair 规则无关；上一步的 NULL 实验直接推翻了「是 pair 歧义」→ 排除。
- **协议/端点问题**：拒绝发生在请求发出之前（NULL 实验里请求才离开进程）→ 排除。

## 修复设计

**一句话**：订阅的列表（火山 plan 列表只给 id；OpenCode/ChatGPT 列表里本地不认识的新 id）根本没说过图片能力，
`offerRowInput` 不能替厂商声明「不支持图片」；已经写坏的库要按同一口径修回来。

### Step 1：`Offer` 增加「列表没说」标记

`pkg/subscription/registry.go` 的 `Offer`（`SupportsImages bool` 在 `:78`）：

```go
	ContextWindow  int64
	SupportsImages bool
	// ImagesUndeclared marks an offer whose listing said nothing about image
	// input: the row must not answer for it — the catalog answers, and the
	// image gate fails open on a model nobody characterized. Zero value keeps
	// every producer that does declare images writing its answer.
	ImagesUndeclared bool
```

*为什么不是 `SupportsImages *bool`*：`plugins/subscriptions/*`（闭源、gitignore）按 `bool` 读写这个字段
（`plugins/subscriptions/qodercn/plugin.go:193` `SupportsImages: record.IsVL`、其测试断言 `!first.SupportsImages`），
改类型会打断插件树编译；且本计划新增的 `ModelRecord.AttachmentsKnown` 已经能表达「未声明」。

### Step 2a：火山 offer 只在表点过名时声明

`pkg/subscription/volcengine.go`：`volcengineModelImages(model string) (images, declared bool)`
（命中内置表 → `(entry.Images, true)`；否则 `(false, false)`），`volcengineModelOffers` 填
`SupportsImages: images, ImagesUndeclared: !declared`，并更新 `:497-499` 的注释（表没点名 = 未知，不是「不支持」）。
静态兜底列表 `volcengineStaticOffers` 全部来自内置表 → `declared` 恒真，行为不变。

### Step 2b：record 区分「列表声明了」与「没人问过」（owner 裁决并入）

`pkg/turn/models.go` 的 `ModelRecord`（`:41-61`）在 `SupportsAttachments bool` 后加：

```go
	// AttachmentsKnown reports that SupportsAttachments is an answer some
	// listing gave. false means nobody characterized this model's input
	// modalities — an id neither models.json nor OpenCode's table knows, a
	// ChatGPT listing without input_modalities — and a subscription row must
	// then claim nothing about images, so the catalog answers and the image
	// gate fails open. Zero value is the safe direction on purpose.
	AttachmentsKnown bool `json:"attachments_known,omitempty"`
```

三处构造点：

- `openCodeModelRecord`（`:148-169`）：**签名加一个 `known bool`**（`meta` 本身区分不出「表里写了 false」与「表里没有」；
  `known` 由唯一调用点 `:128` 的 `meta, known := opencode.LookupModelMeta(provider, id)` 传入，`:131` 调用处一并改），
  初值 `AttachmentsKnown: known`；
  `llm.Lookup` 命中并覆盖能力时一并置 `record.AttachmentsKnown = true`。
- ChatGPT record（`:285-296`）：`AttachmentsKnown: len(info.InputModalities) > 0`。
- `modelRecordFromCatalog`（`:581-597`）：`AttachmentsKnown: true`（本 build 的 catalog 行就是它的声明，
  与内置表在 volcengine 上的口径一致）。

`pkg/subscription/registry.go` 的 `offersFromRecords`：`SupportsImages: record.SupportsAttachments, ImagesUndeclared: !record.AttachmentsKnown`。

### Step 3：写行时跳过伪造

`pkg/subscription/registry.go` `offerRowInput`：`CapImages` 只在 `!offer.ImagesUndeclared` 时写
（`CapContextWindow` 的 `> 0` 规则已存在，两处对齐同一口径），注释改成新契约。

### Step 4：state 迁移 v35→v36 修既有库

`pkg/state/schema_migrations.go`：`stateSchemaVersion` 35→36，`stateSchemaMigrations` 末尾追加
`{version: 36, apply: migrateStateV35ToV36, applyMySQL: …, applyPostgres: …}`，三个方言同一条 UPDATE
（SQLite 版在迁移事务里；MySQL/PG 版同形，参考 V34→V35 的变体写法与错误包装）：

```sql
UPDATE fb_llm_models SET cap_images = NULL
 WHERE lower(provider) = 'volcengine' AND trim(source) <> ''
   AND cap_images = 0 AND cap_context_window IS NULL
```

- **`cap_context_window IS NULL` 是伪造签名**（owner 裁决收窄）：旧写入路径用同一张内置表回答两个字段，
  表没点名时 window 也不写；因此「无 window + images=0」只可能是伪造。
  用户在**表内点名**模型（glm-5.3 等，行里有 window）上显式关掉的 0 **不会**被清。
- **只清 `0`**：伪造值恒为 0；`1` 是表里真有的真值，不动。
- **只清订阅写入的行**（`source<>''`）：`source=''` 的行是用户手工添加/编辑的声明，一行都不动。
- 清完不是「失去判断」：内置表已由 `installBuiltinModelTables`（`pkg/process/config_db.go:253-271`）装进 catalog 的
  builtin 层，表里点过名的模型照旧回答（`glm-5.3`/`deepseek-v4-pro` 等继续被拒），只有表里没有的模型变成
  「未知 → 放行」——这正是 `ImageInputRefusal` 文档写明的 fail-open 契约（`pkg/run/image_input.go:69-80`：
  「refusing an endpoint that actually accepts images is worse than sending one request it rejects」）。
- 残留边界（已知并接受）：用户在**表没点名**的未知模型行（行里本来就没有 window）上显式关掉图片，会被清一次；
  之后用户的编辑不会再被清（迁移只跑一次）。

### 行为变化矩阵

| # | 场景 | 旧行为 | 新行为 | 性质 |
|---|---|---|---|---|
| 1 | 火山 plan 列表里的模型不在内置表（kimi-k3.1-pro / kimi-k2.8-preview / deepseek-v4.1-flash* / glm-5.3-flash / *-latest / doubao-seed-2.1-lite@coding） | 行 `cap_images=0`，图片整轮被拒（owner 的 bug） | 行不写该列，闸门 fail-open：请求带图发出；厂商若真不支持 → 400 + `NewImageHeal`（`pkg/llm/image_heal.go:117`）降级一次并把拒绝记进 `fb_session_image_refusals`（之后是 "already refused" 句式） | 刻意修订 |
| 2 | 火山 plan 列表里的模型在内置表且 `Images:true`（kimi-k3、doubao-seed-*、minimax-m3） | 行 `cap_images=1` | 不变 | 保持 |
| 3 | 火山 plan 列表里的模型在内置表且无 `Images`（glm-5.3、deepseek-v4-pro/flash） | 行 `cap_images=0`，拒绝 | 不变（仍写 0；被迁移清过的历史行也由 builtin 层答 false） | 保持 |
| 4 | 既有库（v35，含伪造 0）升级 | 拒绝 | 迁移清掉「images=0 且 window 为空」的火山订阅行；表内点名的仍拒，未点名的放行 | 刻意修订（本 bug 的现场修复） |
| 5 | 手工添加/编辑的行（`source=''`，含用户勾选的「支持的输入」） | 用户声明 | 完全不动 | 保持 |
| 6 | OpenCode / ChatGPT 的 record **声明了** modalities（models.json 命中、`input_modalities` 有值） | 写 `record.SupportsAttachments` | 不变（`AttachmentsKnown=true`） | 保持 |
| 6b | OpenCode 本地不认识的新 id / ChatGPT 列表缺 `input_modalities` | 写 `cap_images=0` → 同症状误拒 | `AttachmentsKnown=false` → 不写该列 → fail-open | 刻意修订（同类缺口） |
| 6c | 本地插件（闭源）直接构造 `Offer` | 按插件声明写 | 不变（`ImagesUndeclared` 零值 = 已声明） | 保持 |
| 7 | `read_file` 读图（工具侧闸门） | 同一份数据 → "switch model with /model, then read the image again" | 与 #1/#4/#6b 同步放行/保持 | 刻意的连带修订 |

## Scope

**In scope（只改这些文件）**：
- `pkg/subscription/registry.go`（`Offer.ImagesUndeclared`、`offerRowInput`、`offersFromRecords`）
- `pkg/subscription/volcengine.go`（`volcengineModelImages` 返回声明与否；`volcengineModelOffers` 透传）
- `pkg/turn/models.go`（`ModelRecord.AttachmentsKnown` + 三处构造点）
- `pkg/state/schema_migrations.go`（`stateSchemaVersion` 35→36 + `migrateStateV35ToV36{,MySQL,Postgres}`）
- 测试：`pkg/subscription/volcengine_test.go`、`pkg/subscription/registry_test.go`、
  `pkg/subscription/opencode_test.go`、`pkg/subscription/chatgpt_test.go`、`pkg/state/schema_migrations_test.go`；
  如补闸门回归再动 `pkg/run/image_input_test.go`

**Out of scope（明确不碰）**：
- `pkg/run/image_input.go`、`pkg/llm/catalog.go`：闸门与覆盖规则是对的，本次只修**数据来源**。
- `pkg/config/provider_registry.go` 的 volcengine Models 表：不做「把新模型补进表」的数据扩写（见 Maintenance notes）。
- `pkg/tui/*`、`pkg/gateway/*`：不改任何界面/接口形状（`Offer.SupportsImages` 仍是 bool；`ModelRecord` 新字段带 `omitempty`，
  现有 JSON 消费者不受影响）。
- `plugins/**`（闭源插件；另一会话 forebrain-harness-51 在改 qodercn transport）。
- `pkg/subscription/volcengine.go` 的 `Provision`（~170-217）——另一会话 forebrain-harness-e9 已声明该区域；只改
  `volcengineModelOffers` / `volcengineModelImages` 及邻近注释。
- `pkg/process/llm_models.go`（e9 在改 `UpsertLLMModel` 的身份判定）。
- 其它 provider 的能力列、`cap_pdf/cap_video/cap_audio`（provisioning 从不写它们，都是用户数据）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 编译 | `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"; CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| 定点测试（订阅） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription -run 'Volcengine\|ProvisionRows\|OpenCode\|ChatGPT' -count=1` | PASS |
| 定点测试（turn） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run 'ModelRecord\|ModelCatalog' -count=1` | PASS |
| 迁移测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'V35\|SchemaVersion' -count=1` | PASS |
| 包级回归 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription ./pkg/turn ./pkg/state ./pkg/run ./pkg/llm ./pkg/process -count=1` | PASS |
| vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/subscription ./pkg/turn ./pkg/state` | 无输出 |
| gofmt | `gofmt -l pkg/subscription pkg/turn pkg/state` | 无输出 |
| 真机构建 | `export PKG_CONFIG_PATH=…; CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain` | exit 0 |

## Steps

### Step 1：`Offer.ImagesUndeclared` + `offerRowInput` 不再替厂商声明

按「修复设计 Step 1 / Step 3」。写完先只跑编译，行为由后续步骤的测试钉。

**Verify**: `gofmt -l pkg/subscription` 无输出；`CGO_ENABLED=1 go build -tags fts5 ./pkg/subscription` exit 0。

### Step 2：火山 + record 两条数据来源

按「修复设计 Step 2a / Step 2b」。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription ./pkg/turn -count=1` → 预期**失败清单只包含**
`TestOpenCodeOffersLiveUnderTheGoInferenceRoot`、`TestChatGPTOffersCarryTheEndpointTheLoginFixes`（fixtures 没设
`AttachmentsKnown`，期望的 `Offer` 少了 `ImagesUndeclared: true`）——这是契约变更的预期表现。
在 Step 5 一并更新这两个用例；**除此之外的任何失败都是 STOP**。

### Step 3：state 迁移 v35→v36

按「修复设计 Step 4」。迁移函数头注释写清：只清 `0`、只清 `source<>''`、`window IS NULL` 是伪造签名、清完谁回答。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'SchemaVersion' -count=1` → PASS；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` → PASS（含既有迁移测试）。

### Step 4：新增迁移用例

见 Test plan #3。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'V35' -count=1` → PASS。

### Step 5：测试增改（见 Test plan #1/#2/#4/#5）

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription ./pkg/turn ./pkg/state ./pkg/run ./pkg/llm ./pkg/process -count=1` → 全 PASS。

### Step 6：真机验收（tmux + 隔离 home，必做）

构建带 fts5 的二进制。**不得污染真实 `~/.forebrain`**。

6a 复现→修复（伪造行被迁移修好，闸门放行）：
1. `SCRATCH=$TMPDIR/fbvolc-accept`；复制真实 state 库三件套到 `$SCRATCH/home/state/`，复制 `~/.forebrain/forebrain.yaml`。
2. 在副本上跑（`DB=$SCRATCH/home/state/forebrain.state.sqlite`）：
   ```sql
   update fb_llm_models set api_key='repro-dummy', cap_images=0, cap_context_window=NULL
    where id='llm-0511e1fc-5522-47cb-b67a-f55af0857036';
   update fb_session_model_state
      set model_id='llm-0511e1fc-5522-47cb-b67a-f55af0857036', provider='volcengine', model='kimi-k3.1-pro'
    where session_id=(select session_id from fb_session_model_state order by updated_at desc limit 1);
   ```
   （`fb_session_model_state` 列：`session_id, provider, model, effort, updated_at, model_id`，PK `session_id`；
   `agents.definitions.main.default_model` 也要指到该行，否则首启向导会因 key/模型不完整而出现。）
3. 启动：`tmux new-session -d -s fbv -x 140 -y 44 "cd $SCRATCH/project && FOREBRAIN_HOME=$SCRATCH/home ./build/bin/forebrain; echo EXIT_CODE=\$?; sleep 300"`；
   信任目录；`osascript -e 'set the clipboard to (read (POSIX file "<png>") as «class PNGf»)'` → `C-v` → 输入文本 → Enter。
4. 预期：**不再出现** `does not accept image input…`；请求真的发出（假 key → `HTTP 401 Unauthorized`），
   且 `sqlite3 $DB "select cap_images, cap_context_window from fb_llm_models where id='llm-0511e1fc-…'"` 为 `|`（两列皆空，迁移已清）。

6b 图片真的在请求里（协议内可见）：
1. 起假 provider：`FAKE_DUMP_DIR=$SCRATCH/dump nohup python3 scripts/acceptance/fake_provider.py reply 8791 FAKE_ANSWER >/dev/null 2>&1 &`。
2. 在 `$SCRATCH` 库里加一行 `provider='volcengine', model='kimi-k3.1-pro', base_url='http://127.0.0.1:8791', api_path='/chat/completions', source='volcengine', source_plan='agent-plan', cap_images=0, cap_context_window=NULL`，
   设为会话模型（同 6a 的 update），重跑 6a 的操作。
3. 预期：dump 里该请求的 `messages` 含 `image_url` 类型 part；屏幕出现 `FAKE_ANSWER`。

6c 真值不被放宽：
1. 把会话模型切到内置表点名的纯文本模型 `glm-5.3`（agent-plan 行，`cap_images` 置 NULL 也可）。
2. 同样贴图发送 → 预期仍是 `the session model volcengine/glm-5.3 does not accept image input…`（builtin 层仍在拒）。

6d 中断自救：6a 中途 `Esc` 取消、`Ctrl+C` 退出 → `EXIT_CODE=0`；同 home 再启动可直接进 TUI（不落向导死胡同）。

**Verify**: 逐屏 `tmux capture-pane -t fbv -p` 取证；`git status --short` 仍只含 in-scope 文件。

### Step 7：收口

- 实施前抄录的 `docs/plan/SUBSCRIPTION_IMAGE_CAPABILITY_FABRICATION_PLAN.md` 回填「验收记录」：场景→证据（含 6c 回归）。
- 清理：`tmux kill-session -t fbv`、`pkill -f fake_provider.py`、删 `$SCRATCH`。
- 报告：根因链、最小改动清单、验证证据、遗留限制。**不 commit**。

## Test plan

沿用仓库现有模式：`pkg/subscription/*_test.go` 用 `testEnv(t)` + 真 sqlite；
`pkg/state/schema_migrations_test.go` 用 `openStateDB(t)` + 降 `user_version`/`writeDialectSchemaVersion` + `migrateStateSchema`。

1. `pkg/subscription/volcengine_test.go` 新增 `TestVolcengineOffersDeclareOnlyWhatTheTableNames`：
   stub 的 agent plan 列表返回 `[{ModelID:"kimi-k3.1-pro"},{ModelID:"kimi-k3"}]` →
   `kimi-k3.1-pro` 的 `ImagesUndeclared == true`、`kimi-k3` 的 `ImagesUndeclared == false && SupportsImages == true`。
   结构照 `TestVolcengineOffersListThePersonalPlans`（`newVolcengineStub` + `seedVolcengineCredential`）。
2. `pkg/subscription/registry_test.go`：
   - 改 `TestProvisionRowsWritesTheListingsCapabilities`：加第三个 offer（`ImagesUndeclared: true`）→ 该行 `cap_images IS NULL`；
     文档注释改成新契约（只给 id 的列表什么都不写）。
   - 新增 `TestOffersFromRecordsDeclareOnlyWhatTheRecordKnows`：`offersFromRecords("x", []turn.ModelRecord{{APIModel:"declared", SupportsAttachments:true, AttachmentsKnown:true}, {APIModel:"unknown"}}, "https://x.test")`
     → 第一个 `ImagesUndeclared=false`，第二个 `ImagesUndeclared=true`。
3. `pkg/state/schema_migrations_test.go` 新增 `TestStateV35UpgradesToV36ClearsFabricatedVolcengineImages`：
   当前 schema 库里插四行 —— (a) `volcengine/source='volcengine'/cap_images=0/cap_context_window=NULL`（伪造，要清）、
   (b) `volcengine/source='volcengine'/cap_images=1/cap_context_window=NULL`（真值，保留）、
   (c) `volcengine/source='volcengine'/cap_images=0/cap_context_window=1024000`（用户显式关图，保留）、
   (d) `volcengine/source=''/cap_images=0/cap_context_window=NULL`（手工行，保留）——降版本到 35，跑 `migrateStateSchema`，
   断言 (a) NULL、(b) 1、(c) 0、(d) 0，且版本对齐。三种方言同一用例覆盖。
4. `pkg/turn` 的新用例（放 `pkg/turn/models_test.go`，照同文件既有 catalog 用例）：
   `openCodeModelRecord` 对「表与 catalog 都不认识的 id」给 `AttachmentsKnown=false`、对 catalog 命中的 id 给 `true`；
   ChatGPT record 在 `InputModalities` 为空/非空时分别 false/true。
5. 闸门回归（推荐）：`pkg/run/image_input_test.go` 加一条 —— 行 `cap_images=NULL` 且 catalog 无该模型时
   `ImageTurnRefusal` 返回 nil（fail-open）；`cap_images=0` 时返回 `*ImageInputRefusedError` 且 `Code() == ImageInputRefusedCode`。
   照同文件既有用例的 runner 构造，不新造夹具。
6. 更新两个契约变更的既有用例（Step 2 会先失败）：
   `pkg/subscription/opencode_test.go:86-112` 与 `pkg/subscription/chatgpt_test.go:120-140` ——
   fixture 里声明了能力的 record 加 `AttachmentsKnown: true`；未声明的 keep，期望的 `Offer` 加 `ImagesUndeclared: true`。

Verification: `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription ./pkg/turn ./pkg/state ./pkg/run -count=1` → 全 PASS。

## Done criteria

- [ ] `gofmt -l pkg/subscription pkg/turn pkg/state` 无输出；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/subscription ./pkg/turn ./pkg/state` 无输出
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/subscription ./pkg/turn ./pkg/state ./pkg/run ./pkg/llm ./pkg/process -count=1` 全 PASS
- [ ] `grep -n "ImagesUndeclared" pkg/subscription/*.go` 命中三处（字段定义、火山 offer、records 投影）
- [ ] `grep -n "AttachmentsKnown" pkg/turn/models.go` 命中定义 + 三处构造点
- [ ] `grep -n "stateSchemaVersion = 36" pkg/state/schema_migrations.go` 命中
- [ ] 真机 6a/6b/6c/6d 四场景取证通过（`capture-pane` 文本 + dump 证据留在报告里）
- [ ] `git status --short` 只含 Scope 名单内的文件（无 commit）

## STOP conditions

- in-scope 文件的「Current state」内容与现码不符（被并行会话改动过 / 已被别人修过）。
- Step 2 之后 `pkg/subscription`/`pkg/turn` 的失败**不止**那两个预期用例。
- 发现 `plugins/` 或 `pkg/tui` 因本改动编译失败 —— 说明 `Offer` 字段约定被改动过头，停下报告，不要顺手改插件/界面。
- 迁移在 MySQL/Postgres 分支上要求改 schema 列类型或索引（本条只应是纯 UPDATE）。
- 必须动 `pkg/run/image_input.go` 或 `pkg/llm/catalog.go` 才能让真机放过图片（说明根因判断有误）。
- 真机 6c（glm-5.3 仍被拒）失败 —— 说明 fail-open 被放大成「谁都不拒」，停下报告。
- 需要修改 `pkg/config/provider_registry.go` 才能过验收（本计划刻意不做数据扩写）。

## Maintenance notes

- 以后任何**只给 model id / 不声明模态**的列表（新订阅、插件的精简列表、本地不认识的新 id）都必须走
  `Offer.ImagesUndeclared` 或 `ModelRecord.AttachmentsKnown=false`；这是本仓唯一的「未知能力」表达方式，
  `offerRowInput` 不再替厂商声明。
- 内置表 `pkg/config/provider_registry.go` 仍是 volcengine 能力真值来源，但**只对点名过的模型**负责；
  表没点名 = 未知 = fail-open，不是「不支持」。OpenCode 的能力真值来自 models.json + `pkg/llm/opencode` 表。
- 本计划不做、留给后续的两项：
  (a) 把 plan 新模型（kimi-k3.1-pro / kimi-k2.8-preview / deepseek-v4.1-flash* / glm-5.3-flash / doubao-seed-2.1-lite）
  补进内置表；(b) 在 `Provision` 里用 `pkg/llm/modelinfo` 的 `ArkSource`
  （`pkg/llm/modelinfo/modelinfo.go:186+`，注释：plan 模型列表带 levels/token limits/input modalities）写入真值——
  后者让未知模型也有真值，代价是 provisioning 引入一次网络调用与假服务器测试。
- 未改的显示口径（pre-existing）：模型页「支持的输入」默认值、`/connect` web 的 `subscriptionOffer.supports_images` 字段
  （`pkg/gateway/subscriptions_api.go:176-190`）在未知模型上都显示/回传「否」，而闸门是 fail-open；
  TUI 的 offers 页从不渲染图片能力（`pkg/tui` 无 `SupportsImages` 引用），本次不动。
- 评审要点：确认迁移只清「images=0 且 window 为空且 source<>''」；确认 `ModelRecord.AttachmentsKnown` 零值=未声明没有
  反向放宽已声明模型的拒绝（矩阵 #3/#6 的回归用例就是这条的守门）。

## 验收记录（真机，2026-11-14）

环境：`.claude/skills/run-forebrain/driver.sh` 的隔离 home `$TMPDIR/fbvolc-accept`（tmux 会话 `fbv`），
fake provider 在 8791 端口并用 `FAKE_DUMP_DIR` 落请求体，二进制为工作区源码 `CGO_ENABLED=1 go build -tags fts5`
（`v0.1.2-…-c32d23d54625+dirty`）。**未触碰真实 `~/.forebrain`**。

**偏差（记录在案）**：本步骤原定「复制真实 state 库三件套」到临时 home。实测本机剩余磁盘 2.8GiB、真实库 5.9GiB
（`df -h ~` / `ls -la ~/.forebrain/state`），无法复制。改为在隔离 home 建库，写入与真实库逐字一致的行数据
（行 id、`source`、`source_plan`、`cap_images`、`cap_context_window` 全部照抄真实库；`api_key` 用 dummy、
`base_url` 指向本地 fake provider），再把 `PRAGMA user_version` 降到 35，让真实二进制执行 v36 迁移——
迁移的判据（provider / source / cap_images / cap_context_window）与真实库完全一致。

### 6a 伪造行被迁移修好，闸门放行

- 迁移前（`user_version=35`）：`llm-0511e1fc-5522-47cb-b67a-f55af0857036` volcengine/kimi-k3.1-pro/agent-plan
  `cap_images=0`、`cap_context_window=NULL`（与 owner 真实库一致）。
- 启动真实二进制后 `pragma user_version` = **36**；该行变为 `cap_images=NULL, cap_context_window=NULL`；
  陪跑行未被误伤：`glm-5.3`（0 + window 1024000）仍 `0/1024000`，`kimi-k3`（1 + window）仍 `1/1024000`。
- 同一会话（stored session model = 该行，footer `volcengine/kimi-k3.1-pro`）粘贴 PNG → composer 出现 `[Image #1]`
  → `describe this image` → Enter：屏幕出现 `● FAKE_ANSWER`，**不再出现** `does not accept image input…`，
  说明闸门放行且请求真的发出。

### 6b 图片在请求里（协议内可见）

`request-0001.json`：`model=kimi-k3.1-pro`、`stream=true`，`messages` 的 part 类型集合 = `['image_url','text']`；
provider 日志 `REQUEST /chat/completions stream=True mode=reply`。

### 6c 真值不被放宽

把该会话模型切到内置表点名的纯文本模型 `glm-5.3`，并把它的 `cap_images` 也置 NULL（模拟「迁移清过的历史行」）后
同样贴图发送，屏幕仍是：
`the session model volcengine/glm-5.3 does not accept image input and this conversation contains images; …`，
该轮 `FAKE_DUMP_DIR` 目录未生成、provider 日志 0 条 REQUEST —— 拒绝发生在请求发出之前，builtin 层照旧答 false。

### 6d 中断自救

- `stream` 模式提交一轮，出现首个 delta 后 `Esc`：屏幕 `cancelling run` + `Worked for 1s`（取消落地）。
- `Ctrl+C` 退出：屏幕 `EXIT=0`。
- 同一 home 再启动：直接进入 TUI（footer `volcengine/kimi-k3.1-pro`、composer 可用、`tui.err` 为空），
  没有向导死胡同。

清理：tmux 会话、provider 进程、`$TMPDIR/fbvolc-accept` 全部删除。

### 相邻缺陷：新会话首图的闸门判错模型（owner 裁决本期一并修复，已修）

**症状**：新建会话第一次发图时，预检闸门判的是配置默认模型，而不是该会话将要采用的「上次使用的模型」。
复现（修复前）：隔离 home 里把最新 `fb_session_model_state` 指向 volcengine/kimi-k3.1-pro 后**新建**会话发图，
footer 已显示 `volcengine/kimi-k3.1-pro`，但 `● error` 的句子是
`the session model deepseek/deepseek-chat does not accept image input…`，且没有任何请求发出。

**根因**：同一个问题「这个会话跑在哪个模型上」有两份实现。
`pkg/tui/chat_surface.go:231` / `pkg/gateway/server.go:2288` 的 `ImageTurnRefusal` 在
`run.injectSessionModelSelection` 之前跑；无 stored 行时会话的模型由 `RowLessSessionModel`
（未跑过 → `NewSessionModel`＝agent 最近使用的模型；旧会话 → 配置默认）决定，
而闸门走的 `(*Runner).effectiveModelFor` 少这一档，直接落到 `PrimaryModel(r)`（`pkg/run/config.go:2498`），
于是 pre-flight 读数与 turn entry 实际采用的模型不一致。

**修复（最小改动，一处语义）**：`pkg/run/config.go` 的
`effectiveModelFor`（`agentModelFor` 的主线程分支）与 `agentModelRowFor`（capability 按行读，二者必须一致）
在「stored 行」之后加 `RowLessSessionModel` 这一档，再回落到 runner 快照。
于是闸门、媒体协议、token 预算等所有 pre-flight 读数与 turn entry 用同一规则；代价是「尚无模型行」的会话
每次多两次带索引的查询，仅限首条消息落库之前。

**验证**：
- 新单测 `TestImageTurnRefusalJudgesARowLessSessionsModel`（修复前失败，失败句子正是
  `the session model deepseek/deepseek-v4-pro …`；修复后通过），双向都钉：最近使用模型多模态 → 放行；
  最近使用模型纯文本 → 拒绝且句子点名该模型。
- 既有 `TestImageTurnRefusalJudgesTheSessionsOwnModel` 的 s2 前提随之更新：「没有 stored 行且**已跑过**的旧会话
  跟配置默认」（原注释写的「跟 runner」已不是引擎的规则），并显式给 s2 写一条 assistant 行。
- 回归：`./pkg/run ./pkg/turn ./pkg/state ./pkg/subscription ./pkg/llm ./pkg/tui ./pkg/gateway ./pkg/process` 全 PASS。
- 真机（同一 driver 环境，第二个隔离 home）：
  1) 最新 `fb_session_model_state` 指向 volcengine/kimi-k3.1-pro（行 `cap_images=NULL`）→ **新建**会话贴图发送：
     screen `● FAKE_ANSWER_ROWLES`，dump `request-0001.json` `model=kimi-k3.1-pro`、parts `['image_url','text']`；
  2) 同一位置改指 volcengine/glm-5.3（纯文本）→ 新建会话贴图：仍是
     `the session model volcengine/glm-5.3 does not accept image input…`，且该轮 0 条请求（dump 目录未生成）。
