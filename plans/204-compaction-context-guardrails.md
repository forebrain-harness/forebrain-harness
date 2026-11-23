# 204 — 压缩、上下文余量与护栏收口

- **基线**：`06f1bc57`（dev）。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / L / MED（压缩时机与上下文压力直接影响每一轮对话；远程压缩跨服务商是已知风险点）
- **依赖**：203 先落地（同改 `pkg/config/config.go`、`load.go`、`pkg/tui/config_panel_schema.go`、`pkg/process/config_db.go`）。之后是 205。
- **执行隔离**：基于 dev HEAD 的 worktree 或主树 in-tree；**零 commit**（owner 手动提交）。

> **执行者须知**：逐步执行，每步跑「验证」确认结果再进下一步。碰到 §8 STOP 条件就停下报告，不要自行发挥。完成后把 `plans/README.md` 里 204 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：`git diff --stat 06f1bc57 -- pkg/assembly pkg/state/usage.go pkg/run/compaction.go pkg/run/llm_middleware.go pkg/safety/store.go pkg/safety/permission_rules.go pkg/config/guardrails.go pkg/config/tools.go pkg/gateway/run_control.go frontend/src/lib/contextDebug.ts`。除 203 的改动外有输出，就逐条对照 §2；对不上就 STOP。

## 0. 为什么做、owner 裁决（执行期不得改）

C 端产品，配置必须精简，能固定的就固定；删掉的配置连同只为它存在的代码一起清掉。裁决全文在 `plans/191-web-config-ui.md` §8.9、§8.14–§8.18：

1. **删「阈值口径」**（`compact.model_auto_compact_token_limit_scope`）：自动压缩阈值只按全部词元计，`body_after_prefix` 分支全部删除。
2. **远程压缩不再是配置项**：`compact.remote_compaction`、`remote_compaction_v2` 删除；**对实现了 `llm.ContextCompactor` 的客户端固定开启**（今天只有 OpenAI Responses 客户端）；**只留 v2**，v1（`/responses/assembly`）删除；**下一次请求要发给别家服务商时，先在本地重新压缩一次**，生成可移植的检查点再发请求。
3. **自动压缩阈值改百分比**：新字段 `compact.auto_compact_percent`（整数，50–90，初始 90，超过 90 按 90 存），替换绝对词元数 `compact.model_auto_compact_token_limit`。
4. **提醒余量改剩余占比**：`agents.defaults.context_inject.warn_remaining_tokens` → `warn_remaining_percent`（整数 1–50，初始 12）：剩余可用词元低于「有效输入窗口」的这个百分比即为吃紧。
5. **拦截余量配置与语义整体删除**：上下文压力只剩 `ok` / `warn` 两级。
6. **分母统一**：压缩与提醒共用一个「有效输入窗口」函数（输入上限，没有则上下文窗口）；**模型窗口查不到时两边都用配置项「上下文窗口」`context_inject.model_context_tokens`（初始 200000）兜底**（今天压缩写死 200000）。
7. **输出护栏整体删除**（纯启发式正则、误伤大）：配置、规则、调用点都删；网关回答**始终流式**。保留 `incBlocked` / `guardrailBlockedTotal`（输入护栏在用）。
8. **删「检索片段上限」配置**（改固定常量 120000 字）、**删注入字符串匹配**（`ignore previous instructions` / `disregard the above`）。
9. 「不考虑老用户」＝不换算旧值；`fb_config` 旧字段的清理由 207 的迁移统一做（解码宽松，旧字段在 207 之前无害）。

## 1. 交付

- 配置：`CompactSection` 只剩 `Prompt`、`AutoCompactPercent`；`ContextInjectConfig` 去掉 `WarnRemainingTokens`、`BlockRemainingTokens`，加 `WarnRemainingPercent`；`GuardrailsConfig` 去掉 `Output`、`Retrieval`。
- 压缩：阈值 = 有效输入窗口 × 百分比；远程压缩按客户端能力固定开启（v2）；跨服务商前本地重压缩。
- 上下文压力两级；网页上下文调试面板去掉 `block`。
- 输出护栏与注入匹配删除；检索片段上限常量。
- TUI `/config` 描述符同步。

## 2. 现状事实（已对基线核实）

### 2.1 压缩配置（`pkg/config/config.go:68-95`）

`CompactSection{Prompt, ModelAutoCompactTokenLimit, ModelAutoCompactTokenLimitScope, RemoteCompaction *bool, RemoteCompactionV2 *bool}`；`UseRemoteCompaction()` 默认 false；`UseRemoteV2()` 默认 true。接线：`pkg/run/runner.go:1134-1152`（`RemoteCompaction`、`RemoteV1`、`ExplicitLimit`、`LimitScope` 两处）、`pkg/run/compaction.go:362-368`。

### 2.2 阈值计算（`pkg/state/usage.go:246-270`）

```go
	contextWindow := int(limits.ContextWindow)
	inputLimit := int(limits.InputTokenLimit)
	...
	if contextWindow <= 0 {
		contextWindow = 200000
	}
	...
	effective := inputLimit
	if effective <= 0 {
		effective = contextWindow
	}
	autoThreshold := int(float64(effective) * ProactiveCompactFraction)
	...
	if opts.ExplicitLimit > 0 && opts.ExplicitLimit < autoThreshold {
		autoThreshold = opts.ExplicitLimit
	}
```

`ProactiveCompactFraction` = 0.90。`usage.go:18-24` 有「调低阈值会降低缓存命中率」的注释（保留）。另一个读取点：`pkg/tui/chat_turn.go:1574` 一带。

### 2.3 口径（`pkg/assembly/service.go`）

`Service.LimitScope`（61 行）、`normalizedScope` 与 `body_after_prefix` 分支（约 245、266、415、875-876 行）、`modelChangeCompactReason(usage, s.LimitScope, …)`（244 行）；`pkg/state/usage.go:229` 一带为该口径服务的计量（「前缀之后的正文」）。

### 2.4 远程压缩（`pkg/assembly/service.go`）

- 354-358：`if remote, ok := client.(llm.ContextCompactor); ok && s.RemoteCompaction { mode := llm.CompactModeRemoteV1; if !s.RemoteV1 { mode = llm.CompactModeRemoteV2 } … }`
- 176-180：用前一个模型压缩失败且它是远程压缩客户端时，用当前模型重试；`localFallbackAfterRemoteFailure`（436 起）在远程失败时退回本地摘要；`shouldRetryWithCurrentModel`（836）。
- 228-262：换模型 / 换服务商时只记录前缀代际、**不强制压缩**（注释写明是保守姿态）。远程检查点是服务商私有的不透明条目，只能回放给同一家服务商（`config.go:75-80` 的注释）。
- `CompactModeRemoteV1` 与 `/responses/assembly` 路径在 `pkg/llm/openai/responses_llm.go:877` 一带的 `Compact` 实现里。

### 2.5 上下文压力（`pkg/assembly/hook.go:144-160`、`pkg/assembly/engine.go:156-170`）

hook.go 按 `WarnRemainingTokens` / `BlockRemainingTokens`（默认 25000 / 13000，`pkg/config/load.go:788-793` 归一化）判 `ok` / `warn` / `block`；兜底窗口用 `context_inject.model_context_tokens`。engine.go `contextPressureForInjectedContext`：注入内容达到自身上限时报 `block`。**没有任何代码因 `block` 真正拦请求**（只打标与显示）。显示：`frontend/src/lib/contextDebug.ts:77-88` `block` → `danger`；`ContextSignal` 类型含 `block`。字段定义：`pkg/config/tools.go:105` 一带。

### 2.6 输出护栏

- 配置：`GuardrailsOutputConfig`、`GuardrailsConfig.Output`（`pkg/config/guardrails.go:5-11`）。
- 解析：`Resolved.OutputEnabled` 与 `StrictDefaults` / `ResolveGuardrails` 对应行（`pkg/safety/store.go:1006,1022,1038-1040`）。
- 规则：`pkg/safety/permission_rules.go:537-690` 一带——`ApplyOutputRail`、`OutputRailEnabled`、`SanitizeOutbound`、`OutputBlockedError`、`grSysLeakRe` / `grJSURLRe` / `grOpaqueURLRe`、`grExtractURLs` / `grExtractHierarchicalURLs` / `grIsSchemeFirst` / `grIsSchemeChar` / `grTrimTrailingURLPunct`；`WithResultGuard`（452-454）。**保留** `incBlocked`、`guardrailBlockedTotal`（输入护栏在用，520、530 行）。
- 调用点：`pkg/run/llm_middleware.go:700-733`（`outputGuard`、`WithResultGuard("output-rail", …)`）；`SanitizeOutbound` 在 `pkg/gateway/server.go:606,669,767,2520`、`api_extra.go:4558`、`run_control.go:1036`、`channels.go:342`；`run_control.go:460-466` 「输出护栏开着就不流式」。

### 2.7 检索片段（`pkg/assembly/hook.go:88-109` → `pkg/safety/store.go:1060-1088` `EvaluateRetrieval`）

空段丢弃；超过 `RetrievalMaxChunk`（默认 120000，配置 `agents.defaults.guardrails.retrieval.max_chunk_runes`，`pkg/config/guardrails.go:6,23-26`、`load.go:803-805`、`store.go:1008,1023,1041-1043`）丢弃；含两句注入话术丢弃（原因 `injection_substring`）。

## 3. 设计

### 3.1 有效输入窗口（唯一实现）

在 `pkg/state/usage.go` 新增：

```go
// EffectiveInputWindow is the denominator both the auto-compaction threshold
// and the context-pressure warning are measured against: the model's input
// limit, else its context window, else the configured context window.
func EffectiveInputWindow(limits llm.Hit, fallbackContextTokens int) int
```

`fallbackContextTokens <= 0` 时用 200000。`CalculateTokenBudgetWithOptions` 与 hook.go 都调它；`TokenBudgetOptions` 去掉 `ExplicitLimit`、加 `AutoCompactPercent int`（0 视为 90；>90 视为 90；<50 视为 50）与 `FallbackContextTokens int`。阈值 `= EffectiveInputWindow × percent / 100`，至少 1。

### 3.2 远程压缩

- `Service.RemoteCompaction` / `RemoteV1` 字段删除；条件改为 `if remote, ok := client.(llm.ContextCompactor); ok {`，模式恒为 `llm.CompactModeRemoteV2`；`CompactModeRemoteV1` 与 v1 请求路径删除。
- **跨服务商前本地重压缩**：在自动压缩判定（2.4 的 228-262 段）里，当 `providerChanged` 且**会话最近一个检查点是远程压缩产生的**时，返回 `Should: true` 且标明原因（如 `remote_checkpoint_provider_switch`），由**当前模型做本地摘要**（不走 `ContextCompactor`）。检查点「是否远程」从持久化的检查点元数据判断（`compactMessages` 产出的 `strategy` / `source`，查 `persistCheckpoint` 写了什么）。
- 本地重压缩之后才发对话请求。

### 3.3 上下文压力

hook.go 只留：`remaining < EffectiveInputWindow × WarnRemainingPercent / 100` → `warn`，否则 `ok`。engine.go `contextPressureForInjectedContext` 的 `block` 档改报 `warn`（先确认无其它读者依赖 `block`）。前端 `ContextSignal` 去掉 `block`、删 `danger` 分支。

## 4. 步骤

每步结束都跑：`CGO_ENABLED=1 go build -tags fts5 ./... && go vet -tags fts5 ./pkg/...`（期望：无输出）。

1. **口径删除**：删 `ModelAutoCompactTokenLimitScope` 及校验、`LimitScope` 字段与全部 `body_after_prefix` 分支、usage.go 里只为它服务的计量。`grep -rn "body_after_prefix\|LimitScope\|limit_scope" pkg frontend/src` 无输出。
2. **百分比阈值 + 有效输入窗口**：按 3.1 实现；删 `ModelAutoCompactTokenLimit` 与 `ExplicitLimit`，加 `AutoCompactPercent`（`yaml/json:"auto_compact_percent,omitempty"`）；值域校验 50–90（`load.go` 校验处，>90 归一化为 90）。读取点：runner.go 两处、compaction.go、`pkg/tui/chat_turn.go:1574` 一带。
3. **提醒余量百分比 + 拦截删除**：删 `WarnRemainingTokens`、`BlockRemainingTokens` 及默认归一化；加 `WarnRemainingPercent`（1–50，空按 12）；按 3.3 改 hook.go / engine.go；兜底窗口统一走 `EffectiveInputWindow(…, cfg.Agents.Defaults.ContextInject.ModelContextTokens)`。
4. **前端上下文面板**：`contextDebug.ts` 去 `block`，更新 `contextDebug.test.ts`。
5. **远程压缩**：按 3.2 改；删 `RemoteCompaction`、`RemoteCompactionV2`、`UseRemoteCompaction`、`UseRemoteV2` 与两处接线；删 v1 模式与请求路径。实现「跨服务商前本地重压缩」。
6. **输出护栏删除**：按 2.6 清单删；网关 7 处 `SanitizeOutbound(x)` 改为直接用 `x`；`run_control.go` 不流式分支删除；`WithResultGuard` 若再无调用者一并删。`grep -rn "SanitizeOutbound\|OutputRail\|OutputEnabled\|grSysLeakRe\|withheld: output guardrails" pkg frontend/src` 只剩测试里要删的。
7. **检索片段**：删 `GuardrailsRetrievalConfig` 与 `RetrievalMaxChunk` 解析；`EvaluateRetrieval` 用包内常量 `retrievalMaxChunkRunes = 120000`，只做「空段丢弃、超长丢弃」；删注入匹配与 `injection_substring`。
8. **TUI 描述符**：删被删字段；加「自动压缩阈值」（50–90，单位 %）、「提醒余量」（1–50，单位 %）。`go test -tags fts5 ./pkg/tui/ -run ConfigPanel -count=1`。
9. **测试修整**：删只测已删功能的用例（口径、v1、输出护栏、注入匹配、拦截余量）；外部通道发送与网关流式的既有测试不再期待 `[withheld: output guardrails]`。
10. **包图**：`scripts/package-graph.sh`；`graph.json` 只应有 LOC 变化。

## 5. 测试（放在被测文件同名的 `_test.go` 里）

- `pkg/state/usage_test.go`：`EffectiveInputWindow` 三种来源；百分比 50 / 90 / 95（按 90）/ 30（按 50）；窗口查不到时用配置兜底而不是写死。
- `pkg/assembly/hook_test.go`：剩余占比 11% → `warn`、13% → `ok`；兜底窗口与压缩同源（同一个模型、同一个配置下两边分母相等）。
- `pkg/assembly/service_test.go`：
  - 实现 `ContextCompactor` 的假客户端：不配任何开关也走远程 v2；不实现的走本地摘要。
  - **「ChatGPT 上远程压缩 → 切到 DeepSeek 继续对话」**：先产生远程检查点，再以另一服务商的模型发下一轮 → 断言先发生一次本地压缩、对话请求里不含不透明条目。
  - 同服务商换模型、最近检查点是远程的 → 不强制本地重压缩。
- `pkg/safety/store_test.go`：`EvaluateRetrieval` 不再因两句话术丢弃；超过 120000 字丢弃。
- `pkg/gateway`：开过输出护栏的配置（字段已不存在）下回答仍流式；外部通道原文发送。
- 前端 vitest：`contextDebug` 无 `block` / `danger`。

## 6. 完成判据（机器可查）

```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/assembly/... ./pkg/run/... ./pkg/safety/... ./pkg/gateway/... ./pkg/tui/... ./pkg/llm/... ./pkg/config/... -count=1 -p 2   # 全 ok
go vet -tags fts5 ./...                                                     # 无输出
grep -rn "ModelAutoCompactTokenLimit\|RemoteCompaction\|CompactModeRemoteV1\|BlockRemaining\|WarnRemainingTokens\|SanitizeOutbound\|ApplyOutputRail\|RetrievalMaxChunk\|injection_substring\|body_after_prefix" pkg --include='*.go'   # 无输出
cd frontend && pnpm typecheck && pnpm test                                  # 全绿
```

全量回归：`CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -p 2`。

## 7. 维护注意

- 207 会把 `auto_compact_percent=90`、`warn_remaining_percent=12`、`model_context_tokens=200000` 写成建库默认值，并清理库里旧字段。
- 行为变化（写进 README 结果）：①用 OpenAI Responses 的会话从此自动走远程压缩；②回答在网关上始终流式；③注入话术不再拦截附加上下文。
- 网页（191）与 TUI 面板按百分比展示；「上下文窗口」的说明是「自动压缩与提醒都按这个值算」。

## 8. STOP 条件

- 持久化的检查点里**没有**能判断「是远程压缩产生的」字段（3.2 需要它）——报告现状与建议（例如在检查点元数据里加字段，那可能涉及状态库结构，须走迁移），不要自行改表。
- `contextPressureForInjectedContext` 的 `block` 有读者据此改变行为。
- 删输出护栏时发现外部通道有依赖它做的密钥脱敏（不只是 URL / 话术正则）。
- 删 v1 后 OpenAI 客户端的 v2 测试有失败。
