# Plan 304: 配置行的思考强度改完下一轮就生效（pin 带行版本，客户端缓存按版本键）

> **Executor instructions**：按步骤执行，每步跑完验证命令并对照预期再往下。遇到「STOP 条件」任一条就停下报告，不要自行发挥。完成后不要改 `plans/`（索引由派发方维护）。
>
> **Drift check（开工第一步）**：
> ```sh
> git rev-parse --short HEAD            # 基线 c680aaed（dev）；后代提交亦可，按内容核对
> grep -n 'cat.Model(agentType)' -A 22 pkg/run/subagent.go
> grep -n 'func (w \*subagentModelOverrideLLM) clientFor' -A 26 pkg/run/subagent.go
> grep -n 'func usableModelByID' -A 50 pkg/run/subagent.go
> grep -n 'func TestGoalCheckEffortFollowsTheRowDefaultUntilOverridden' -A 60 pkg/run/goal_test.go
> ```
> 期望：`cat.Model(agentType)` 分支只设 `pin.ModelID/Provider/Model/Effort`（**没有** `Entry`/`Version`）；`clientFor` 只有在 `override.Entry != nil` 时才把 `/v<Version>` 加进缓存键；`usableModelByID` 用 `usableModel{Row, Entry, HandedOff}` 的形状读行。内容对不上即 STOP。

## Status

- **Priority**: P1（owner 2026-11-12 裁决：「修缓存，让改动下一轮生效」）
- **Effort**: S–M（一处读行 + 一处缓存键/淘汰 + 两条测试）
- **Risk**: MED（改的是每次子代理派发的模型解析与客户端缓存）
- **Depends on**: `plans/290`（pin 带 Caps）、`plans/294`（续跑解析 pin）、`plans/299`（goal-evaluator 全局行）
- **Category**: bug fix

## Why this matters

299 的裁决是「默认强度**实时读模型行**」：库里 `agents.goal_evaluator.reasoning_effort` 存空即代表「跟随该模型行的 `default_effort`」。实测（299 执行报告 §6）这个语义在**长生命周期 runner** 里会失效：`cat.Model()` 钉出的 pin 不带 `Entry`/`Version`，而 `subagentModelOverrideLLM.clientFor` 的缓存键只有 `ModelID/Provider/Model`，于是模型页改了该行的 `default_effort` 后，同一 runner 的下一轮**仍用旧 client**，要等 `LoadConfig` / 换会话 / 换主模型才跟随。保存条文案「下一轮对话生效」因此是乐观的。

同一个键还挡住了「行被编辑（新 key/新 endpoint）后立刻生效」——模型参数那条路（`usableModelByID` → `Entry: &entry, Version: row.UpdatedAt`，`subagent.go:4464-4484`）已经做对了，缺的是配置行这条路。

## Current state（执行者需要的事实）

- pin 的两个来源：
  1. **模型参数 / 续跑**（已对）：`usableModelByID` → `SubagentModelOverride{…, Entry: &entry, Version: usable.Row.UpdatedAt}`（`:4464-4484`；续跑同形，`:2417-2437`）。
  2. **配置行**（缺口）：`:1986-2003` 的 `cat.Model(agentType)` 分支——只设 `ModelID/Provider/Model/Effort`，`Entry`/`Version` 都没有；`Caps` 由 `subagentExecContext`（`:4262`）补。
- `clientFor`（`:4557-4579`）：键 = `modelID/provider/model`，**仅当 `Entry != nil` 时**追加 `/v<Version>`；`build(override)` 在 `Entry != nil` 时直接用它构客户端（否则用本轮快照）。
- 行的毫秒版本：`state.LLMModel.UpdatedAt`；投影函数 `appcfg.ProviderFromModelRow`、`appcfg.ModelOverrideFromCaps`（290 已用）。
- `usableModelByID` 同时承担**授权**（`subagent_allowed` + 交接例外，293 的凭据规则）——它不能直接拿来给配置行用（配置行不经过派发授权，299 §6-⑦ 已定）。

## Scope

**In scope**：`pkg/run/subagent.go`（`cat.Model()` 分支、`clientFor` 的键与淘汰、把 `usableModelByID` 的读行投影抽成不带授权的窄函数）、`pkg/run/goal_test.go`/`subagent_test.go`（新测试）。
**Out of scope**：`SubagentCatalog.Model` 的签名（不要改）、授权规则、`usableModels` 列表语义、gateway/frontend、259–266 的模型表。

## Steps

### Step 1：抽出「按 id 读行投影」的窄函数（不改授权）

从 `usableModelByID` 里分出只做「按 id 找到行 → 投影 `Entry`/`Caps`/`Version`」的一段，命名例如 `configuredRowByID(fac, id) (entry appcfg.AgentLLMProviderConfig, caps *llm.ModelOverride, version int64, ok bool)`：
- **不做** `subagent_allowed` / 交接例外 / 凭据校验（那些留在 `usableModelByID` 里）；
- 投影复用 `ProviderFromModelRow` 与 `ModelOverrideFromCaps`，不复制字段映射；
- 行不存在或投影失败 → `ok=false`（调用方按「没有 Entry」回退到今天的行为）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → ok；`usableModelByID` 的行为与错误文本一字不变（既有测试如 `TestUsableModelsListAndDispatchAgree` 必须仍 PASS，断言不得放宽）。

### Step 2：配置行 pin 带上行与版本

`:1986-2003` 分支里，拿到 `modelID` 后调用 Step 1 的函数并填 `pin.Entry`、`pin.Version`（`Caps` 保持现有路径）；`ok=false` 时保持今天的形状（无 Entry/Version、由快照建客户端）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestGoalCheck|TestSubagent' -count=1` → ok。

### Step 3：缓存键按行版本，且不无界增长

`clientFor` 的键已含版本；本步只补**淘汰**：为同一个 `modelID/provider/model` 插入新版本客户端时，删掉该 model 键下更早版本的条目（map 里每 model 至多一条），并在注释里写明理由（长生命周期 runner + 用户反复改设置）。

**Verify**：新用例——同一 pin 连取两次拿到同一 client（命中）；版本 +1 后取到**不同** client；再取旧版本不会把两条都留在 map 里（用测试可达的内部计数或断言 `len(w.clients)`）。

### Step 4：§5.3#8 升级为「不重载也跟随」

把 `TestGoalCheckEffortFollowsTheRowDefaultUntilOverridden`（`pkg/run/goal_test.go`）扩成三段：
1. 行 `default_effort=high`、配置 `reasoning_effort=""` → 派发带 `high`；
2. **不重建 runner**，把该行 `default_effort` 改成 `low`（直接写 store / 测试用的行表）→ 下一次派发带 `low`（这是本计划的核心断言，旧代码会红）；
3. 对照组：配置 `reasoning_effort="medium"` 时永远带 `medium`，行默认值再改也不动。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestGoalCheckEffortFollowsTheRowDefaultUntilOverridden' -count=1 -v` → PASS；**故意回退 Step 2**（pin 不填 Entry/Version）时该用例必须红——把这次红/绿的输出都写进报告。

### Step 5：回归

`CGO_ENABLED=1 go build -tags fts5 ./...`、`go vet -tags fts5 ./...`、`gofmt -l pkg cmd` 空、`CGO_ENABLED=1 go test -tags fts5 ./pkg/run ./pkg/config ./pkg/gateway ./pkg/architecture -count=1` → ok。

## Test plan（必须新增/改写）

1. `TestConfiguredRowPinCarriesTheRowsVersion`：配置行 pin 的 `Entry`/`Version` 等于该行的投影与 `UpdatedAt`；行不存在时无 Entry 且派发仍可用（回退）。
2. `TestSubagentClientFollowsARowEditWithoutAReload`：同一 runner 内行版本 +1 后，缓存给新 client（断言构建次数或 client 指针不同）。
3. `TestSubagentClientCacheKeepsOneClientPerModel`：连续 3 个版本后 map 里该 model 只剩一条。
4. Step 4 的三段式断言（扩写既有用例，不改名或改名都行，但断言强度只能升）。

## Done criteria

- [ ] 配置行 pin 带 `Entry` + `Version`；`ok=false` 时行为与今天一致
- [ ] 改行 `default_effort` 后**同一 runner 的下一轮**即生效；存了具体档位时不随行变化
- [ ] 缓存每 model 至多一条；版本变化构建新 client
- [ ] 授权/交接/凭据三条规则零改动（`grep -c 'subagent_allowed' pkg/run/subagent.go` 前后一致；`TestUsableModelsListAndDispatchAgree` PASS 且断言未放宽）
- [ ] `go build`/`vet`/`gofmt`/四个包测试干净；报告含「回退 Step 2 必红」的证据

## STOP conditions

- Step 1 的窄函数与 `usableModelByID` 无法共用投影（会迫使命名/字段各写一份）→ 停下报告。
- 行版本在 store 里不是单调的（`UpdatedAt` 不是毫秒时间戳或被复用）→ 停下报告。
- 配置行 pin 需要改 `SubagentCatalog.Model` 的签名才能拿到模型 id → 停下报告（299 已定该签名不动）。
- 核心断言（不重载也跟随）需要改快照采纳时机（`loadLocked`）才成立 → 停下报告，不要动 runner 装配。
- 出现「行不存在 → 派发失败」的新行为 → 停下报告（回退必须是静默的）。

## 非功能要求

- 性能：每次派发一次行读（不是每步、不是每工具调用）；缓存命中路径不加锁外 I/O。
- 资源：每条 model 至多一个 client；不新增 goroutine、不新增无超时等待。
- 兼容：没有 Entry 的旧 pin（297 的 ctx pin、续跑的老行）行为不变。
- Maintainability：缓存键与淘汰规则写在一处并注释；测试钉住「同一 model 至多一条」。
