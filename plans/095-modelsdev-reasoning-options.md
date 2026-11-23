# Plan 095: `models.json` 按上游最新数据整体刷新；推理强度档位**只**取它的 `reasoning_options`

> **Executor instructions**: 按步骤执行，每步跑完「Verify」并确认输出符合预期再进入下一步。
> 命中「STOP conditions」任一条就停下来报告，不要即兴改动。
> 全部完成后更新 `plans/README.md` 里本计划那一行的状态（除非派发你的评审方说他维护索引）。
>
> **Drift check（先跑）**:
> `git diff --stat 9459287..HEAD -- pkg/llm/catalog.go pkg/llm/assets.go pkg/llm/opencode/models_table.go pkg/llm/anthropic/agent_llm.go pkg/turn/models.go pkg/tui/setup.go pkg/gateway/api_extra.go pkg/run/config.go assets/models.json`
> 现码摘录写于 `9459287`（dev）。那批 opencode/effort 改动**已提交**为 `9459287`
> （`pkg/turn/models.go:189` 的 `ModelReasoningEfforts`/`openCodeEfforts`、
> `pkg/llm/opencode/models_table.go:59` 的 `Efforts` 列、`pkg/tui/setup.go:1149-1189`）——
> 所以 worktree 里这些符号**存在**，不需要再叠加未提交改动。输出必须为空；非空按 STOP 处理。
>
> **owner 指令（2026-10-21，五条）**：
> ① `models.json` **必须按上游最新数据整体更新**；
> ② **TUI 与 web 的推理强度选择器选项必须统一使用 `models.json` 的 `reasoning_options` 字段值**；
> ③ ChatGPT 例外：它的模型与档位来自登录后的账号后端，保留账号级数据（不编造静态条目）；
> ④ **必须保证用户能自定义修改 home 里的 `models.json` 做覆盖**；
> ⑤ **home 里没有 `models.json` 时 `Install` 必须写一份完整副本**（保证文件一定存在、方便用户改）；
> **已存在时一个字节都不写**——不升级、不改写、不备份、不迁移。
> 另有已定裁决：`deepseek/deepseek-v4-flash` 收图保留 override；先 review-plan 冷读复核再派发。
>
> **④+⑤ 是本计划的形态底线**：`$FOREBRAIN_HOME/models.json` 是一份**用户拥有的完整副本**，
> 只在缺失时由 `Install` 写入；载入时它是**覆盖层**（逐字段覆盖 shipped），
> 于是用户写的每一行永远生效，而 shipped 里用户那份没有的字段（本次新增的 `type`、
> `reasoning_options`）会自动补进合并结果——不需要改写用户的文件。

## Status

- **Priority**: P1（参考数据陈旧到会改变实际行为；强度档位在 5 处各写一份且与真实能力不符）
- **Effort**: L
- **Risk**: MED–HIGH（整体替换共享数据文件 + home `models.json` 语义变更（副本 → 覆盖层）+
  图片能力翻转 + 影响 provider 推断）
- **Depends on**: 无
- **Category**: data correctness / feature
- **Planned at**: `9459287`（dev），2026-10-21

## Why this matters

`assets/models.json`（安装到 `$FOREBRAIN_HOME/models.json`）是唯一参考数据：上下文窗口（压缩阈值）、
输出上限（`max_tokens`）、`reasoning`、`attachment`（`pkg/run.ImageInputRefusal` 的图片闸门）、
以及 `/model`、`/connect`、`/api/models` 的模型列表。

它现在是 2026-10-15 的快照。与今天上游 canonical 表对比：

| 项 | 现状 | 上游今天 |
|---|---|---|
| 条目 | 362 | **568**（本仓库每条都还在；**无删除**） |
| `attachment` | — | 17 条变化（12 条 `false→true`；**5 条 `true→false`**） |
| `limit` | — | 12 条（`qwen3.7-max` output ×2、`llama-4-scout` context 3.5M→10M…） |
| `reasoning_options` | **无此列** | 179 条（另 18 条为 `[]`） |
| `type`（区分非 chat） | 无 | 61 条 |

两处可直接观察的后果：

1. **非 chat 模型漏进选择器**：今天文件里的 30 条 image/speech/transcription/embedding 模型
   （`whisper-large-v3`、`gpt-image-1`、`veo-3.1`…）会出现在 TUI 模型页与 `/api/models` 里——
   本会话真机验收的 `/connect` → OpenAI 页面就列出了两个 Whisper。
2. **思考强度五处各写一份四档**（`pkg/turn/models.go:549` `ReasoningEffortLevels`、
   `pkg/tui/setup.go:1152` `reasoningEffortOptions`、`pkg/llm/opencode/models_table.go` 的 50 条手抄
   `Efforts`、`pkg/llm/anthropic/agent_llm.go:87` 的 switch、gateway 的 `sessionModelEfforts`），
   与真实能力不符：

| 键 | 上游 `reasoning_options` | 今天 picker 给的 |
|---|---|---|
| `openai/gpt-5` | `minimal, low, medium, high` | low/medium/high/**xhigh**（不存在） |
| `openai/gpt-5.5` | `none, low, medium, high, xhigh` | 少了 `none` |
| `zhipuai/glm-5.3-flash`（e2e 主模型） | `low, high, max` | 四档 |
| `deepseek/deepseek-v4-pro` | `low, high, max` | 四档 |
| `deepseek/deepseek-chat` | `reasoning: false` | 四档（模型根本不出思考） |
| `zhipuai/glm-4.5` / `moonshotai/kimi-k2.6` | `toggle`（无档位） | 四档 |
| `anthropic/claude-sonnet-5` | `low, high, medium, xhigh, max` | 四档（`max` 被藏、`xhigh` 被丢） |

owner 指令 ② 的落法：**`models.json` 是唯一来源**，TUI 与 web 都只读它，兜底四档取消；
`opencode`/`opencode-go` 的档位今天来自手抄静态表，本次把这两个 provider 也**写进 `models.json`**，
静态表不再持有档位数据。

## 数据事实（全部实测，2026-10-21）

来源 `https://models.dev/catalog.json?type=all`（6,422,561 B）。它同时带 canonical `models`
（568 条，`"provider/model"` 键，正是本仓库文件的来源）与 `providers` 树（`reasoning_options`
只在这里）。`api.json?type=all` 没有 canonical 表，不采用。

派生结果（已试算）：

| 项 | 数值 |
|---|---|
| canonical 条目 | 568 |
| 注入 `opencode`（131）+ `opencode-go`（33） | 164（上游 100% 可合成：都带 `limit`/`modalities`/`reasoning`） |
| 总条目 | **732** |
| 带 `reasoning_options` | **308**（canonical 179 + 注入 129）；另 46 条为 `[]`（能推理、无档位） |
| 非 chat（带 `type`） | **63**（canonical 61 + 注入 2 个 jev 决策模型） |
| chat 条目 | 669 |
| 文件 | 500,131 → **940,643 B**（`indent=4, sort_keys=True, ensure_ascii=False`，无尾随换行） |

**严格口径的实际效果**（只认字段、无兜底）：669 个 chat 条目里，出强度页的 = 有非空 `effort` 值
且客户端能发；其余不出页。今天靠四档兜底"有档位"的模型里，**227 个会失去强度页**
（156 条上游没有该字段、57 条只有 toggle/budget、14 条是 `[]`）——这正是"不再给假选项"。

**5 条 `true→false` 的处置**（逐条，不整批接受）：

| 键 | 上游 | 本仓库 | 处置 |
|---|---|---|---|
| `deepseek/deepseek-v4-flash` | `false` | `true` + `modalities.input` 含 `image` | **保留 override**（owner 2026-10-14 实测：端点把该 id 路由到 deepseek-flash，后者收图；`catalog_test.go:189` 是报警器） |
| `deepseek/deepseek-chat` | `false` | `true` | 接受上游（行为变化见矩阵） |
| `deepseek/deepseek-reasoner` | `false` | `true` | 接受上游 |
| `meta/llama-3.3-70b-instruct` | `false` | `true` | 接受上游 |
| `openai/gpt-4` | `false` | `true` | 接受上游（本就只收文本） |

**`anthropic-sdk-go` 是 v1.80.0**（`go.mod:7`），其 `OutputConfigEffort` 已有
`low/medium/high/xhigh/max`。所以"Anthropic 只能发 low/medium/high/max"是过期认知：
`pkg/llm/anthropic/agent_llm.go:87-92` 的 switch 只是漏了 `xhigh`。本计划补齐它，
于是 **`xhigh` 不再需要从 picker 里隐藏**。

## 设计决策（5 条）

1. **单文件整体替换**：`assets/models.json` = 上游 canonical 568 条 + 注入的 opencode 164 条，
   键排序、`indent=4`，生成器 `scripts/update-models-asset.sh` 是唯一入口。
2. **非 chat 与登录型 provider 只作参考数据，不当聊天模型卖**：文件忠实记录（含 `type`），
   但 `turn.ListModelCatalog`（聊天投影）跳过 `type != ""` 与 `chatgpt`/`opencode`/`opencode-go`
   三类条目——后两者的目录来自登录（`listModelCatalogLive` 今天已按 provider 跳过静态表，
   这里把同一条规则下沉到投影层，避免无过滤查询重复列出）。
3. **强度档位只有一个来源**：`turn.ModelReasoningEfforts(provider, model)` 读
   `llm.ReasoningEfforts`（= `models.json` 的 `reasoning_options` 里 `type=="effort"` 的 `values`），
   再与「本 build 客户端真能发出去的档位」取交集。**取消四档兜底**：没有数据 = 不出页。
   opencode 的档位也走这条路径（静态表只保留 `Protocol`/`Free` 与「文件里没有该 id」时的窗口回退）。
4. **home 的 `models.json` 是用户拥有的完整副本，同时是覆盖层**（owner 指令 ④⑤）：
   - `Install` 只在文件**缺失**时写一份 shipped 的完整副本（原子写），保证文件一定存在、
     用户打开就能看到全部条目；
   - 文件**已存在**时 `Install` 一个字节都不写：不升级、不改写、不生成 `.bak`、不做迁移
     （没有 marker 文件，也没有 `.new` 文件）；
   - 载入时 `shipped（基底）← home（逐字段覆盖）`：home 里出现的键/字段一律赢，没出现的回落到
     shipped，条目值为 `null` ⇒ 从结果里删掉该模型。
   这个组合让三件事同时成立：用户改什么就是什么（他的值永远赢）、home 永远有完整可编辑的副本、
   而**用户那份文件里没有的字段**（老副本没有 `type` 与 `reasoning_options`）由 shipped 补上——
   所以本次的两个新能力会到达所有存量安装，且不需要碰他们的文件。
   代价（有意接受）：老 home 里**已有**的字段值是陈旧的（`limit`、`attachment` 等），
   除非用户删掉文件或删掉对应键，否则不会跟随上游更新。`FOREBRAIN.md` 要写明这一点。
5. **provider 推断与聊天投影都要认识"登录型 provider"**：注入后 102 个模型名与 canonical 同名
   （`glm-5.3`、`kimi-k3`、`gpt-5.5`…），`ProviderForAgentModel`（`pkg/run/config.go:239`）会把
   裸名判成歧义并返回空。让它跳过登录型 provider 即可保持今天的行为（它们今天不在表里，
   本来也返回空）。

## Current state（摘录均为现码）

- 解析只认四个字段（未知字段被忽略，所以加字段对老二进制无害）—— `pkg/llm/catalog.go:88`：

```go
type rawEntry struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CompHash   string `json:"comp_hash"`
	Reasoning  bool   `json:"reasoning"`
	Attachment bool   `json:"attachment"`
	Limit      struct{ Context, Input, Output int } `json:"limit"`
}
```

- `Parse`（`:102`）→ `Model`（`:37`）；`Lookup`（`:327`）→ `Hit`（`:25`）。两者都只有 `CanReason`。
- 启动装载 `InitGlobalCatalog`（`:311`），调用点 `pkg/process/runtime.go:104`、`seed.go:27`。
- 「永不覆盖」契约 —— `catalog.go:435-441`（注释原文：`Existing files are NEVER overwritten`）。
- 读「二进制旁边那份」的现成函数 —— `pkg/llm/assets.go:164` `sourceModelsJSON(version)`。
- 聊天投影唯一入口 —— `pkg/turn/models.go:412` `ListModelCatalog`（TUI `pkg/tui/setup.go:1052`
  与 gateway `/api/models` 都走它）。
- 强度入口 —— `pkg/turn/models.go:189` `ModelReasoningEfforts`；兜底清单 `:549`
  `ReasoningEffortLevels`；opencode 静态表 `pkg/llm/opencode/models_table.go`（`Efforts` 列在 `:59`）。
- Anthropic 白名单 —— `pkg/llm/anthropic/agent_llm.go:87-92`（`low/medium/high/max`，漏 `xhigh`）。
- provider 推断 —— `pkg/run/config.go:239` `ProviderForAgentModel`（歧义即返回空）。
- 两条 golden test 与上游冲突 —— `pkg/llm/catalog_test.go:189`、`:206`。

## Scope

In scope：

- `scripts/update-models-asset.sh`（新）、`assets/models.json`（整体重写，941KB）
- `pkg/llm/catalog.go`（`type`/`reasoning_options` 三态、`Chat()`、`Load` 的基底+覆盖层合并、
  `Load` 的基底+覆盖层合并）
- `pkg/llm/anthropic/agent_llm.go`（白名单加 `xhigh`，导出判定）
- `pkg/llm/opencode/models_table.go`（删 `Efforts` 列与 `EffortsForProtocol` 的 anthropic 白名单副本）
- `pkg/turn/models.go`（聊天投影过滤；`ModelReasoningEfforts` 单一来源；删四档兜底）
- `pkg/tui/setup.go`（删 `reasoningEffortOptions`）
- `pkg/run/config.go`（`ProviderForAgentModel` 跳过登录型 provider）
- 测试（含两条 golden test 保持绿、若干断言改真实值）、`FOREBRAIN.md`、`scripts/install-assets.sh` 注释

Out of scope：

- 不实现 `budget_tokens`（thinking budget 无发送路径）。
- 不新增非 chat 能力（图片生成/语音/embedding 无客户端）。
- 不改客户端分派（`pkg/run/config.go:400`）；不动 `pkg/gateway/dist/**`（**不要**跑 `make ui` / `pnpm build`）。
- ChatGPT 保持账号级数据（owner 裁决 ③）：`chatgptModelRecord` 的 `ReasoningEfforts` 来自后端，
  不改成读 `models.json`。

## Steps

### Step 1 `scripts/update-models-asset.sh`：整体刷新 + 注入 + override

新脚本（`bash` + `python3`）：

```bash
#!/usr/bin/env bash
# Regenerate assets/models.json from models.dev.
#
#   scripts/update-models-asset.sh [catalog.json]
#
# Without an argument it downloads https://models.dev/catalog.json?type=all
# (FOREBRAIN_MODELSDEV_CATALOG overrides with a local copy for an offline run).
#
# Three views are merged, because no single one is sufficient:
#   models                    canonical "provider/model" table: the entry set and
#                             the capability fields (reasoning/attachment/limit/type);
#   providers[opencode|opencode-go]
#                             the two login-backed surfaces are NOT in the canonical
#                             table; every one of their models is synthesizable into
#                             the canonical shape, so they are injected instead of
#                             being hand-maintained in pkg/llm/opencode;
#   providers[*]              reasoning_options exists ONLY here (the canonical table
#                             carries none), keyed by provider because the same model
#                             id has different levels per provider.
#
# Local overrides live in OVERRIDES and are the ONLY fields that may differ from
# upstream. Each one carries a dated reason.
```

要求：

1. 基表 = canonical + 注入的 opencode/opencode-go（合成字段：`id`、`name`、`family`、
   `attachment`、`reasoning`、`modalities`、`limit`、`type`(若有)）；
2. 注入 `reasoning_options`：按 provider 查树，`type=="effort"` 的 `values` 只保留
   `none|minimal|low|medium|high|xhigh|max`，丢弃其它值时打印
   `warning: dropped unknown effort %q for %s`（实测命中 `"default"` 与 `null`）；
3. `OVERRIDES`（初值即最终形态）：

```python
OVERRIDES = {
    # owner 2026-10-14: the DeepSeek endpoint routes this id to deepseek-flash,
    # which takes image input. Upstream's canonical row is coarser (its deepseek
    # provider lists four models, no routing detail), and pkg/run.ImageInputRefusal
    # reads exactly this field — taking upstream would make the owner's own default
    # model refuse images. Pinned by pkg/llm/catalog_test.go:189.
    "deepseek/deepseek-v4-flash": {"attachment": True, "modalities": {"input": ["text", "image"], "output": ["text"]}},
}
```

4. 输出 `indent=4, sort_keys=True, ensure_ascii=False`，无尾随换行；
5. 统计行：`entries=732 canonical=568 injected=164 with-options=308 empty=46 chat=669 non-chat=63 overrides=1 bytes=NNNNN`。

**Verify**：

- 连跑两次 → `shasum assets/models.json` 一致
- `python3 -c "import json;d=json.load(open('assets/models.json'));print(len(d))"` → `732`
- 键集合只增不减：`python3` 断言 `set(旧键) <= set(新键)`
- 四个样本：`openai/gpt-5` → `["minimal","low","medium","high"]`；`zhipuai/glm-4.5` →
  `[{"type":"toggle"}]`；`anthropic/claude-haiku-4-5` → `[]`；
  `opencode-go/glm-5.3` → `[{"type":"effort","values":["low","high","max"]}]`；
  `opencode/jev-1.13` 带 `"type":"decision"`；`deepseek/deepseek-v4-flash` 的 `attachment` 为 `true`
- `ls -l assets/models.json` → 约 941KB（< 1.1MB）

### Step 2 `pkg/llm/catalog.go`：`type` + `reasoning_options`（三态）+ `Chat()`

1. 新类型 `ReasoningOption{Type, Values, Min, Max}`；
2. `rawEntry` 增 `Type string` 与 `ReasoningOptions *[]ReasoningOption`（**指针**才能区分
   「有字段但空数组」与「没有字段」）；`Model` 增 `Kind`、`ReasoningOptions`、`ReasoningKnown`；
   `Hit` 同样三个；
3. `func (m Model) Chat() bool { return strings.TrimSpace(m.Kind) == "" }`；
4. `ReasoningEfforts(provider, model) (levels []string, known bool)`（方法与包级各一）：
   只取 `type=="effort"` 的 `values`，按文件内顺序合并去重，并再过滤一次已知集合
   （`models.json` 是可编辑文件，这是数据边界）；
5. `LoginBackedProvider(provider) bool`（`chatgpt`/`opencode`/`opencode-go`）放在这里，
   供 `pkg/turn` 与 `pkg/run` 共用——不要在三个包里各写一份。

**Verify**：`./pkg/llm/ -count=1` ok（既有 `TestParseAndLookup` 的老 fixture 无新字段 →
`ReasoningKnown=false`、`Kind=""`、`Chat()=true`）。

### Step 3 `pkg/llm`：`Install` 补齐缺失的副本；载入时 home 覆盖 shipped（owner 指令 ④⑤）

契约（一句话版：**只有缺失才写；存在就绝不碰；读的时候它覆盖 shipped**）：

```
Install(home):
    home/models.json 不存在  -> 原子写入 shipped 的完整副本（不写任何其他文件）
    home/models.json 已存在  -> 什么都不做（不升级、不改写、不备份、不迁移）

effective(provider, model) = shipped[provider/model] <-逐字段- home[provider/model]
    home 出现的键/字段一律赢；没出现的回落 shipped
    home 里该条目为 null => 从结果里删除这个模型
```

实现（`pkg/llm/catalog.go`）：

1. **`Load(basePath, overlayPath string) (*Catalog, error)`**：读 shipped，再读 home 覆盖层
   （不存在 = 无覆盖，不是错误）；用 `map[string]json.RawMessage` **逐字段**合并
   （顶层键合并、键内字段合并、`null` 删除键），合并后 `Parse` 成 `Catalog`。
   `Resolve(home)` 改走这条路径；缓存键包含覆盖层路径与其内容摘要（改文件即失效）。
2. **`Install`**（`catalog.go:441`）：保留现有「文件存在就返回 nil」的分支**原样不动**
   （这就是指令 ⑤ 的"已存在不碰"），只在缺失分支写完整副本。**删除**任何 marker / `.bak` /
   `.new` / 迁移逻辑 —— 本计划不给 home 增加第二个文件。
3. 注释写清：shipped = 数据；home = 用户拥有的完整副本，同时按字段覆盖 shipped。

**Verify**：

- `TestInstallWritesAFullCopyWhenMissing`：空 home → `Install` 后 home 文件与 shipped
  **逐字节相同**，且 home 目录里没有多出第二个文件（`os.ReadDir` 断言只有一个）；
- `TestInstallLeavesAnExistingFileByteIdentical`：home 放一份被裁剪/改过的文件
  （只留一个条目、改过 `attachment`）→ `Install` 后字节与 mtime 均不变；
- `TestOverlayOverridesSingleField`：home 只写 `{"openai/gpt-5":{"attachment":true}}` →
  `Lookup` 得 `SupportsAttachments=true`，`limit` / `reasoning_options` 仍取 shipped；
- `TestOverlayAddsAModelAndDeletesOne`：home 新增 shipped 没有的模型 → 可查；
  `{"openai/gpt-4": null}` → 不可查，其余不受影响；
- `TestOverlayAppliesToReasoningOptions`（指令 ② 的落点）：home 写
  `{"zhipuai/glm-5.3-flash":{"reasoning_options":[{"type":"effort","values":["high"]}]}}`
  → `llm.ReasoningEfforts` = `["high"]`；
- **`TestLegacyFullCopyGainsTheNewColumns`（存量安装的关键回归）**：home 放一份旧版完整副本
  （`assets/models.json` 删掉 `type` 与 `reasoning_options` 后写入，模拟升级前的 home）→
  载入后 `openai/gpt-5` 的 `reasoning_options`、`whisper-large-v3` 的 `type` 都来自 shipped，
  而用户那份的 `limit` / `attachment` 保持他自己的值；
- 改写既有 `TestInitGlobalCatalogSeedsAFreshHome`：断言「shipped + home 合并后 `Lookup` 有值」，
  文件存在性由前两条覆盖。

### Step 3b `pkg/process`：启动路径合并 shipped 与 home

`pkg/process/seed.go:27` 与 `runtime.go:104` 仍调 `InitGlobalCatalog(root, home.Version)`，
语义变成「确保 home 有副本（仅缺失时）+ 解析 shipped + 叠加 home 覆盖层」；
`pkg/process/seed.go` 的注释里「home-level install like models.json」要改成
「home 副本缺失时补齐；已存在不碰」（字典/字体仍是 home 级安装，语义不变）。

**Verify**：`./pkg/process/ -run 'Seed|Catalog'` ok。

### Step 4 `pkg/llm/anthropic`：补 `xhigh` 并导出判定

`agent_llm.go:87-92` 的 switch 改为 `AcceptsEffort(level string) bool`，集合
`low|medium|high|xhigh|max`（`anthropic-sdk-go v1.80.0` 的 `OutputConfigEffort` 全部支持，
`xhigh` 用 `anthropicapi.OutputConfigEffortXhigh`）；另导出 `EffortLevels() []string`。

**Verify**：`./pkg/llm/anthropic/` ok；`./pkg/llm/opencode/ -run
TestEffortsForProtocolMatchesTheAnthropicClient` ok（该测试驱动真实客户端读回
`output_config.effort`，现在必须对 `xhigh` 也返回 true）。

### Step 5 `pkg/llm/opencode`：静态表不再持有档位

- 删除 `ModelMeta.Efforts` 与 `EffortsForProtocol` 里那份 Anthropic 白名单副本；
  `EffortsForProtocol` 只剩两类规则：`chat/completions`/`responses` 原样、
  `google` 恒空（`clientBaseURL` 的 generateContent 客户端没有 effort 参数）。
- `LookupModelMeta` 只回答 `Protocol`/`Free`；窗口与图片能力改由 `models.json` 提供
  （`pkg/turn.openCodeModelRecord` 先查 `llm.Lookup`，查不到才用 `meta`），
  顺带修掉免费族窗口的陈旧值（表里 128k，上游 200k–1M）。
- 删 `TestModelTableEffortsMatchTheOfficialCatalog`，由 Step 10 的资产测试覆盖（更强：164 条全查）。

**Verify**：`./pkg/llm/opencode/ -count=1` ok（协议一致性测试保留）。

### Step 6 `pkg/turn/models.go`：聊天投影过滤 + 强度单一来源

1. `ListModelCatalog`（`:413`）跳过 `!m.Chat()` 与 `llm.LoginBackedProvider(m.Provider)` 的条目
   （一处过滤，TUI/`/api/models`/web 同时正确）；
2. `ModelReasoningEfforts`（`:189`）：

```go
func ModelReasoningEfforts(provider, model string) []string {
	levels, known := llm.ReasoningEfforts(provider, model)   // models.json 是唯一来源
	if !known {
		return nil                                            // 无数据 = 不出页（取消四档兜底）
	}
	return trimToClient(provider, model, levels)
}

func trimToClient(provider, model string, levels []string) []string {
	switch {
	case strings.EqualFold(provider, "anthropic") || strings.EqualFold(provider, "minimax"):
		return filter(levels, anthropic.AcceptsEffort)
	case opencode.ProtocolFor(provider, model) == opencode.ProtocolGoogle:
		return nil                                            // generateContent 客户端没有 effort 参数
	default:
		return append([]string(nil), levels...)               // openai 兼容 / responses 原样透传
	}
}
```

3. 删 `ReasoningEffortLevels`（`:549`）及其全部引用（**这是本计划的要求：唯一来源**）；
4. `openCodeModelRecord` 的 `ContextWindow`/`SupportsAttachments`/`CanReason` 改为先查
   `llm.Lookup`（`models.json` 现在有 opencode 条目），查不到才用静态表；
   `ReasoningEfforts` 由 `ModelReasoningEfforts` 统一给（不再调 `openCodeEfforts`）。

**Verify**：`./pkg/turn/ -run 'Reasoning|OpenCode|ModelChoices|ListModelCatalog'` ok；
新断言表（键都真实存在于刷新后的文件）：

| (provider, model) | `ModelReasoningEfforts` |
|---|---|
| `openai` / `gpt-5` | `minimal low medium high` |
| `openai` / `gpt-5.5` | `none low medium high xhigh` |
| `xai` / `grok-4.3` | `none low medium high xhigh` |
| `zhipuai` / `glm-5.3-flash` | `low high max` |
| `zhipuai` / `glm-4.5` | 空（只有 toggle） |
| `deepseek` / `deepseek-chat` | 空（`reasoning:false`） |
| `anthropic` / `claude-sonnet-5` | `low medium high xhigh max`（客户端现在能发 xhigh） |
| `anthropic` / `claude-haiku-4-5` | 空（只有 budget_tokens） |
| `alibaba` / `qwen3.8-max` | `low medium xhigh` |
| `opencode-go` / `glm-5.3` | `low high max` |
| `opencode-go` / `gemini-4-argon-contributor` | 空（google 协议） |
| `opencode` / `claude-sonnet-4-5` | 空（只有 budget_tokens） |
| 文件里没有的模型（如 `tencent/hy3` 不在 providers 树、也无 `reasoning_options`） | 空（**不再给四档**） |

以及 `ListModelCatalog` 新测试：`openai` 列表不含 `whisper-large-v3`/`gpt-image-1`、含 `gpt-5`；
无过滤查询不含任何 `opencode*` 条目。

### Step 7 `pkg/run/config.go`：provider 推断跳过登录型 provider

`ProviderForAgentModel`（`:239`）的 `llm.AllModels()` 扫描加
`if llm.LoginBackedProvider(candidate.Provider) { continue }`。

**Verify**：`./pkg/run/ -run 'ProviderForAgentModel|AgentModel'` ok；新断言：注入后
`glm-5.3`（同名的 opencode/opencode-go/zhipuai 三方）仍解析为 `zhipuai`（与今天一致）。

### Step 8 `pkg/tui/setup.go`：删本地四档清单

`promptReasoningEffort`（`:1169`）的 `record == nil` 分支改问 `turn.ModelReasoningEfforts`；
空 → `not_supported` 路径；非空 → `promptEffortChoice`（`medium` 不在列表时已会回落
`Provider default`）。删 `reasoningEffortOptions`（`:1152`）与注释（`:1149-1151`）。

**Verify**：`./pkg/tui/ -run 'OpenCode|Reasoning'` ok；新测试：`openai/gpt-5` 页面为
`minimal low medium high Provider default`；`zhipuai/glm-4.5` 不出页；`zhipuai/glm-5.3-flash`
为 `low high max Provider default`。

### Step 9 gateway：断言改真实值（代码不动）

`sessionModelEfforts`（`pkg/gateway/api_extra.go:1686`）已优先用 `choice.ReasoningEfforts`。
改写既有断言（今天写的是四档）：

- `api_extra_test.go:5302` zhipuai/glm-5.3-flash → `["low","high","max"]`
- `api_extra_test.go:5308` deepseek/deepseek-v4-pro → `["low","high","max"]`
- `api_extra_test.go:5303` deepseek/deepseek-chat 仍为空（`reasoning:false`）
- 新增：`PUT` 带 `effort:"xhigh"` 到 `zhipuai/glm-5.3-flash` → 400；`/api/models?provider=openai`
  不含 whisper。

**Verify**：`./pkg/gateway/ -run 'SessionModel|Models'` ok。

### Step 10 文档与资产自洽测试

- `FOREBRAIN.md`：加 `scripts/update-models-asset.sh` 一行（上游 `catalog.json?type=all`，
  含 opencode 注入与 `OVERRIDES` 表）。
- `FOREBRAIN.md` 用一小节写清两层契约（owner 指令 ④ 的可见面）：

  ```markdown
  ## Model data

  `assets/models.json` beside the binary is the catalog (refreshed with the build).
  `$FOREBRAIN_HOME/models.json` is an overlay you own: write only the keys and fields
  you want to change, and they win over the shipped row — a field you leave out falls
  back to it. `null` removes a model. Arrays (`reasoning_options`, `modalities.input`)
  replace whole, never merge element-wise.

      {"zhipuai/glm-5.3-flash": {"reasoning_options": [{"type": "effort", "values": ["high"]}]}}
      {"openai/gpt-4": null}
  ```

- `scripts/install-assets.sh` 头注释：补 `reasoning_options`/`type`；并改掉
  「runtime reads models.json from <bin-dir>」那句为「shipped base；home 那份是覆盖层」。
- 新测试 `TestModelAssetCoversUpstream`（`pkg/llm/assets_test.go`）：732 条、308 带 options、
  63 非 chat、无未知 effort 值、6 个样本值、`opencode-go/glm-5.3` 在文件里。

**Verify**：`go vet ./...` → 0；`./pkg/llm/ ./pkg/turn/ ./pkg/tui/ ./pkg/gateway/ ./pkg/run/ -count=1` 全 ok。

## 行为变化矩阵（写进交付说明）

| 场景 | 旧 | 新 | 性质 |
|---|---|---|---|
| 参考数据 | 362 条（10-15） | 732 条（canonical 568 + opencode 164） | 刻意（指令 ①） |
| 非 chat 模型出现在选择器 | 有（30 条：whisper/gpt-image/veo…） | 无（投影层过滤 63 条） | 缺陷修复 |
| `deepseek/deepseek-chat` 收图 | 允许 | 拒绝（上游修正） | 行为变化 |
| `deepseek-reasoner`、`llama-3.3-70b`、`gpt-4` 收图 | 允许 | 拒绝 | 上游修正 |
| `deepseek/deepseek-v4-flash` 收图 | 允许 | 仍允许（override） | 等价 |
| 12 条模型的上下文/输出上限 | 旧值 | 上游值 | 上游修正 |
| 强度档位来源 | 四档硬编码 ×5 处 | `models.json` 唯一来源（指令 ②） | 缺陷修复 |
| `zhipuai/glm-5.3-flash`、`deepseek-v4-pro` 档位 | 四档 | `low/high/max` | 缺陷修复 |
| `deepseek-chat`、`glm-4.5`、`kimi-k2.6` 档位 | 四档 | 无（不推理/无档位） | 缺陷修复 |
| Anthropic 协议模型档位 | 最多四档，`max` 常缺、`xhigh` 被丢 | 上游值（含 `xhigh`，客户端已支持） | 缺陷修复 |
| opencode / opencode-go 档位 | 50 条手抄静态表 | `models.json`（164 条全量） | 缺陷修复 |
| opencode 模型的上下文窗口/图片能力 | 静态表（128k、免费族 false） | `models.json`（200k–1M、VL 为 true） | 缺陷修复 |
| 文件里没有数据的推理模型 | 四档 | 无强度页 | 刻意（唯一来源） |
| ChatGPT 档位 | 账号后端返回 | 不变（owner 裁决 ③） | 等价 |
| home 的 `models.json` 语义 | 缺失时写一份副本；存在时不动；但**只当独立目录读** | 缺失时写完整副本（不变）；存在时**仍一个字节不写**；载入时按字段覆盖 shipped | 刻意（指令 ④⑤） |
| home 副本里**没有**的字段（老副本缺 `type`/`reasoning_options`） | 不存在这些能力 | 由 shipped 补上 → 非 chat 过滤与强度页对所有存量安装生效 | 缺陷修复（不碰用户文件） |
| home 副本里**已有**的字段（`limit`/`attachment` 等） | 陈旧，且永不更新 | 仍陈旧（用户的值优先）；删文件或删对应键即回到 shipped | 刻意（指令 ⑤） |
| 用户在 home 改 `attachment`/`limit`/`reasoning_options` | 能改，但上游刷新永远到不了 | 改了就赢，且升级后仍然赢 | 刻意（指令 ④） |
| 用户改过的 home 文件 | 永不被我们改写 | 永不被我们改写（`Install` 只在文件缺失时写入） | 等价 |
| 裸模型名推断 provider（`glm-5.3` 等） | `zhipuai` | 仍 `zhipuai`（登录型 provider 不参与推断） | 等价 |
| `VerifyProvider` 认识的 provider | 33 家族 | 更多（ai21、aion-labs…；opencode 由凭证判定放行） | 上游扩展 |

## Test plan

| 测试 | 文件 | 钉住什么 |
|---|---|---|
| `TestParseCarriesKindAndReasoningOptions` | `pkg/llm/catalog_test.go` | `type`→`Kind`；`[]` vs 缺字段；未知 effort 值被丢；`Chat()` |
| `TestReasoningEffortsMergesEffortsInOrder` | 同上 | 多描述按序合并去重（`alibaba/qwen3.8-max` 三合一） |
| `TestOverlayOverridesSingleField` / `TestOverlayAddsAModelAndDeletesOne` / `TestOverlayAppliesToReasoningOptions` | `pkg/llm/catalog_test.go` | 覆盖层逐字段生效、`null` 删除、用户改档位后 TUI 与 web 都看到他写的值 |
| `TestInstallWritesAFullCopyWhenMissing` / `TestInstallLeavesAnExistingFileByteIdentical` / `TestLegacyFullCopyGainsTheNewColumns` | 同上 | 缺失时才写完整副本（且只写这一个文件）；已存在字节与 mtime 零改动；旧副本自动获得 `type`/`reasoning_options` |
| `TestCatalogMarksTheOwnerDefaultAsMultimodal` / `…TextOnly…`（**保持绿**） | `:189` / `:206` | override 生效 |
| `TestModelAssetCoversUpstream` | `pkg/llm/assets_test.go` | 732/308/63 + 6 样本 + 无未知值 |
| `TestModelReasoningEffortsFromTheCatalog` | `pkg/turn/models_test.go` | Step 6 的表逐行 |
| `TestListModelCatalogSkipsNonChatAndLoginProviders` | 同上 | 投影过滤 |
| `TestChooseModelUsesTheModelsOwnEffortLevels`（改写） | `pkg/turn/model_test.go` | `/model` 真实档位；非法值被拒 |
| `TestProviderInferenceIgnoresLoginBackedProviders` | `pkg/run/config_test.go` | `glm-5.3` 仍 → `zhipuai` |
| `TestConfigureEffortPageUsesTheModelsLevels` | `pkg/tui/setup_test.go` | 首启/`/connect` 同源 |
| `TestSessionModelOptionsCarryUpstreamEfforts` | `pkg/gateway/api_extra_test.go` | chip + PUT 校验 + `/api/models` |

`pkg/turn`/`pkg/tui`/`pkg/gateway`/`pkg/run` 的用例统一用 `installRepoCatalog(t)`
（读 `assets/models.json` → `llm.Parse` → `llm.SetGlobalCatalogForTest`，`t.Cleanup` 复原），
不要各自拼 fixture。

## Done criteria（机器可查）

- `CGO_ENABLED=1 go build ./...`、`go vet ./...` 退出码 0
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` 全绿
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture/ -count=1` 绿；`scripts/package-graph.sh` 后
  `git status --porcelain pkg/architecture/testdata/graph.json` 为空
- `python3 -c "import json;print(len(json.load(open('assets/models.json'))))"` → `750`
  （计划时写的是 732：用计划用的 6,422,561 B 快照重跑仍得 732/568/63，上游在 2026-10-21 已增至
  6,492,393 B → 586 canonical；②「最新数据」优先，见 Review 的执行记录）
- `grep -c '"reasoning_options"' assets/models.json` ≥ 300
- `grep -rn "ReasoningEffortLevels\|reasoningEffortOptions" pkg/ --include=*.go` → 0 命中
- `grep -rn "Efforts \[\]string" pkg/llm/opencode/` → 0 命中（静态表不再持有档位）
- `scripts/update-models-asset.sh` 重跑后 `git diff --stat assets/models.json` 为空
- 两条 deepseek golden test 仍绿
- **home 副本契约（指令 ⑤）**：清空 home → 启动一次 → `models.json` 存在且与 `assets/models.json`
  逐字节相同、home 里没有第二个新增文件；再改它一行、启动一次 → 该行仍在（字节与 mtime 不变）
- **覆盖层可用（指令 ④）**：在临时 home 里写一份只含
  `{"zhipuai/glm-5.3-flash":{"reasoning_options":[{"type":"effort","values":["high"]}]}}` 的
  `models.json`，进程读到的档位只出现 `high`（单元测试 `TestOverlayAppliesToReasoningOptions`
  走 `InitGlobalCatalog` 的真实路径，网关/TUI 选项断言读同一合并结果）。
  **不用 `scripts/acceptance/web_e2e.sh`**：它会构建前端、写 `pkg/gateway/dist/**`，与本计划的
  Scope「不动 dist / 不跑 make ui」直接冲突（原稿这条自相矛盾，执行时按 Scope 处理）
- **副本契约（指令 ⑤）的真机面**：全新 home 首次启动后 `ls $FOREBRAIN_HOME/models.json*` 只有一个文件；
  改动它的一行后重启，`shasum` 不变（我们没碰它）

## STOP conditions

- 上游 `catalog.json` 结构变了（无顶层 `models`/`providers`）→ 停下报告。
- 刷新后 `set(旧键) ⊄ set(新键)` → 停下报告（本仓库不该丢模型）。
- 10 个主流 provider（openai/anthropic/google/deepseek/xai/mistral/moonshotai/zhipuai/minimax/
  alibaba）任一在上游整个消失 → 停下报告。
- 想让某条断言通过而去改 `pkg/llm/anthropic` 的可发集合（把它缩回四档）→ **不要改**：
  v1.80.0 支持 `xhigh`，缩回去等于给假 UI。反过来说，如果实测发现某 provider 拒收 `xhigh`，
  停下报告并在 `OVERRIDES` 里去掉该模型的那一档，不要改全局白名单。
- 想改 `TestCatalogMarksTheOwnerDefaultAsMultimodal` → **不要改**：改 `OVERRIDES` 表。
- **若实现需要改写已存在的 home `models.json`**（升级、迁移、生成 `.bak`/`.new`/marker）→ 停下报告：
  指令 ⑤ 要求"已存在就一个字节都不碰"。
- 若实现需要给 home 增加第二个文件（marker/基底/摘要）才能让老副本拿到新字段 → 停下报告：
  正确做法是覆盖层缺字段回落 shipped，不是加文件。
- 若发现某处仍把 home 的 `models.json` 当「完整副本」读（例如自己 `os.ReadFile` 后
  `Parse`，绕过覆盖层合并）→ 停下报告并列出该处，不要只修一半。
- 需要动 `pkg/gateway/dist/**` / `make ui` / 前端构建 → STOP。
- `pkg/tui` 需要 import `pkg/run`/`pkg/gateway` → STOP（架构测试禁止）。

## Maintenance notes

- **刷新只有 `scripts/update-models-asset.sh`**。上游天天变；重跑、看 diff、提交。
  需要本地修正就加 `OVERRIDES`（带日期与理由 + golden test）。目前只有一条（deepseek-v4-flash）。
- **两层的职责不可混**：`assets/models.json`（shipped，随二进制）= 数据；
  `$FOREBRAIN_HOME/models.json`（用户）= 覆盖。任何新消费方都必须经 `llm.Resolve`/`llm.Lookup`，
  不能自己去 `ReadFile(home/models.json)` 当成完整目录。
- **用户覆盖的语法**（写进 `models.json` 的注释性文档，见 Step 10）：
  只写想改的键与字段；`null` 删除一个模型；数组字段（`reasoning_options`、`modalities.input`）
  整段替换，不做元素合并。
- **单一来源不变量**：推理强度只来自 `models.json.reasoning_options`（shipped + 覆盖层合并后的
  结果）；新增表面必须调 `turn.ModelReasoningEfforts`。ChatGPT 的账号级数据是唯一例外（裁决 ③）。
- **三态**：`known && 空`（toggle/budget）≠ `!known`（无数据 → 不出页）。
- **「参考数据」vs「聊天模型」**：`llm.AllModels()` 忠实于合并结果；`turn.ListModelCatalog` 是聊天
  投影（过滤非 chat 与登录型 provider）。新增消费方要选对。
- opencode 的 `Protocol`/`Free` 仍在 `pkg/llm/opencode/models_table.go`（协议不是代码逻辑里的数据，
  是代码），与 `ProtocolFor` 的一致性由既有测试锁住。
- **绝不给 home 造第二个文件**：没有 marker、没有 `.bak`、没有 `.new`；`Install` 只在缺失时写。
  想让存量 home 拿到新数据，靠的是覆盖层（缺字段回落 shipped），不是改写用户文件。
- 仍未做：`budget_tokens` 发送路径；非 chat 能力运行时。

## Rollback

`git checkout -- assets/models.json`，回退 `pkg/llm`/`pkg/turn`/`pkg/tui`/`pkg/run` 的改动。
home 的 `models.json` 在任何回退里都不受影响（我们从不改写已存在的文件）；
删掉它会让下一次启动重新写入一份 shipped 的完整副本。

## Review（review-plan 冷读复核，2026-10-21）

**已核实（逐条 `sed -n` / 真实下载数据试算）**

- 全部 `file:line`：`catalog.go:25/37/88/102/311/327/435/441`、`assets.go:40/157/164`、
  `turn/models.go:189/413/549`、`tui/setup.go:1052/1149/1152/1169`、`anthropic/agent_llm.go:87`、
  `run/config.go:250/400`、`gateway/api_extra.go:1686/1735`、`catalog_test.go:189/206`、
  `go.mod:7`（`anthropic-sdk-go v1.80.0`）。
- 数据：362/568、added 206/removed 0、attachment 17（12F→T / 5T→F）、limit 12、
  canonical options 179（18 空）、注入 164（129 options / 28 空 / 7 无字段）、总 732、941KB、非 chat 63。
- **`anthropic-sdk-go v1.80.0` 已有 `OutputConfigEffortXhigh`** → 上一版的「Anthropic 只能发四档、
  要裁掉 xhigh」是过期结论，已改为补上客户端支持。
- 注入的 102 处同名模型会破坏 `ProviderForAgentModel` → 已加 Step 7（跳过登录型 provider，
  保持今天的返回值）。
- 非 chat 泄漏不是推测：本会话真机验收的 OpenAI 模型页确实列了两个 Whisper。

**本会话五轮里改掉的计划缺陷**

1. 只补字段不刷新条目集合（与指令 ① 冲突）→ 整体替换。
2. 派生第二个资产文件（被 owner 否决）→ 单文件。
3. `Install` 永不覆盖会让老 home 永远拿不到刷新 → 先改成 marker 升级。
4. 强度档位仍有四档兜底与 opencode 手抄表（与指令 ② 冲突）→ 取消兜底、opencode 注入文件、
   静态表不再持有档位。
5. Anthropic「只能发四档」的过期前提 → 补 `xhigh`。
6. **marker 升级仍是"要么升级、要么保留你的编辑"的互斥模型（与指令 ④ 冲突）**：
   hash 不匹配就整份不写 ⇒ 用户改过一次，此后所有上游刷新都被冻结在他那份旧快照上。
   → 改成**两层模型**（shipped 基底 + home 覆盖层，逐字段覆盖、`null` 删除）。
7. **上一稿又走过头：把 home 改成稀疏覆盖层、不再写副本**（与指令 ⑤ 冲突）。
   → 改成"缺失写完整副本、存在绝不碰"：副本仍在（用户打开就能改），覆盖仍按字段生效
   （老副本没有 `type`/`reasoning_options`，这两列自动从 shipped 补上，无需改写用户文件）。
   这一稿也顺带删掉了 marker/`.bak`/`.new`/迁移分支——不给 home 造第二个文件。

**已知代价（接受）**

- 仓库与安装各 +441KB（500KB → 941KB，只增在 shipped 一侧；home 不再有副本）。
- 一次性整体 diff（键已排序，之后只动变化条目）。
- 4 条模型图片能力翻转，`deepseek-chat` 会开始拒图（用户可见）。
- 227 个推理模型失去强度页（今天给的是假四档）。
- 存量 home 里**已有**的字段值是陈旧的（`limit`/`attachment`），只有删文件或删对应键才跟随上游；
  本次新增的 `type`/`reasoning_options` 因为旧副本里没有，会自动从 shipped 补上。
- `models.json` 的语义变了（完整副本 → 覆盖层），需要一行文档说明；用户若整份复制过一份旧
  目录到 home，那份文件会按「覆盖层」解释（字段级 pin），文档里给出清理方法（删文件即回到 shipped）。

**owner 裁决记录**：① 整体刷新（本会话）；② 强度只取 `models.json`（本会话）；
③ ChatGPT 保留账号级数据（本会话）；④ **home 的 `models.json` 必须是用户可改的**（本会话）；
⑤ **缺失时 `Install` 必须写一份完整副本；已存在就一个字节都不碰**（本会话）；
⑥ `deepseek-v4-flash` 保留 override（本会话）；⑦ 先 review-plan 再派发（本会话）。

## 执行记录（2026-10-21，verdict APPROVE）

**隔离方式**（owner 当次裁决）：先把工作区那批未提交的 opencode/effort 改动提交为
`9459287`（20 个文件，`chore: land provider catalog, opencode login and per-model effort plumbing on dev`），
再从该 HEAD 建 detached worktree `/tmp/forebrain-095-wt` 派发执行者。改动全部留在 worktree，
未 commit、未合入、未 push。

**上游漂移实测**（计划的 732 与今天的 750 是同一份代码）：

| 输入 | canonical | injected | 总条目 | 非 chat | 字节 |
|---|---|---|---|---|---|
| 计划用快照 `6,422,561 B` | 568 | 164 | **732** | 63 | 976,861 |
| 执行当天 `6,492,393 B` | 586 | 164 | **750** | 69 | 992,959 |

派生：带非空 options 的条目旧快照重跑得 **290**、今天 **291**（计划里的 308 偏高，按实测口径
以生成器的统计行为准）；两种输入下 `empty=46` 不变。

生成器两次重跑 `shasum` 相同（`23bf8bc7…`），与执行者产物逐字节一致。

**实际落地**：`scripts/update-models-asset.sh`（新）、`assets/models.json`（750 条）、
`pkg/llm/catalog.go`（`ReasoningOption`/`Kind`/`ReasoningKnown`/`Chat()`/`ReasoningEfforts`/
`LoginBackedProvider`/`Load(base,overlay)`/`Install` 文档）、`pkg/turn/models.go`（聊天投影过滤、
`ModelReasoningEfforts` 单一来源、`openCodeModelRecord` 先查 `models.json`）、
`pkg/llm/anthropic`（`AcceptsEffort`/`EffortLevels`，含 `xhigh`）、`pkg/llm/opencode`（删 `Efforts` 列）、
`pkg/tui/setup.go`（删本地四档）、`pkg/run/config.go`（推断跳过登录型 provider）、
`pkg/process/{seed,runtime}.go`（注释）、`FOREBRAIN.md`、`scripts/install-assets.sh`、
`pkg/architecture/testdata/graph.json`（重生成）与配套测试。

**已批准的偏差**（均有证据，记录在案）：

1. 条目 750 而非 732 —— 上游当日已增（上表）；`TestModelAssetCoversUpstream` 因此断言下限
   （≥700 / ≥280 非空 options / ≥60 非 chat）+ 13 条精确样本 + 无未知档位值，避免下次合法刷新把
   CI 打红；样本仍逐条钉死真实值。
2. `anthropic/claude-haiku-4-5` 的 `reasoning_options` 是 `[{"type":"budget_tokens","min":1024}]`
   而非计划 Step 1 写的 `[]`（计划 Step 6 的「只有 budget_tokens」才是对的）。
3. 注入行 `id` 用 `provider/model`（providers 树里是裸 id），否则 `openCodeModelRecord` 的surface
   判定失效。
4. `TestEffortsForProtocolMatchesTheAnthropicClient` 改名为
   `TestTheAnthropicClientSendsTheLevelsItAccepts`：Step 5 删掉白名单副本后原比较对象不存在，
   新测试改为钉「客户端真正写进 `output_config.effort` 的集合」。
5. 少量既有测试跟着真实数据改（都是本计划行为矩阵预测的变化）：`pkg/tui` 夹具补
   `reasoning_options`、ChatGPT 手工输入不再出强度页、`pkg/run` 图片用例默认模型
   `deepseek-chat`→`deepseek-v4-flash`（前者上游已转纯文本）、`TestInstall_ThenResolve`
   的「Install 前 Resolve 必失败」改为「用 shipped 基底作答」。

**review 发现并当轮修掉的两处缺陷**：

1. `Resolve` 把 `loadCached` 的错误吞成「models.json not found」——用户手改坏一行 JSON 会被告知
   文件不存在。改为 `errNoCatalog` 哨兵只覆盖「两边都没有」，其余错误原样返回，并给合并阶段的所有
   失败加上 `overlay over base` 的路径前缀（新增 `TestResolveReportsABrokenOverlayPrecisely`、
   `TestResolveRejectsANonObjectOverlayEntry`）。
2. `trimToSendable` 按 provider 名判断，而 `pkg/run` 是按协议分派的
   （`opencode.ProtocolAnthropic` 也走 Anthropic 客户端，客户端会静默丢掉不认识的档位）。
   改为「provider 或协议命中 Anthropic 都过 `anthropic.AcceptsEffort`」
   （新增 `TestAnthropicProtocolModelsOfferOnlySendableLevels`）。

**未做 / 遗留**：

- `budget_tokens` 仍无发送路径（计划 Out of scope）。
- 原稿「用 `web_e2e.sh` 真机验收覆盖层」与本计划「不跑 `make ui`/不动 dist」冲突，已在上面的
  done criteria 里改成引擎级证据；TUI 真机 `/model` 抽查可在落地后单独做。
- `pkg/gateway TestGatewayApproveCancelsAnInFlightPlanReview` 是**既有** flake
  （`TempDir RemoveAll … directory not empty`，重复跑约 25% 失败；该测试与网关生产代码都不在本计划
  diff 里），未在本期修，另案处理。

