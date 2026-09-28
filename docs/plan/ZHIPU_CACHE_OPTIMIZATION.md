# 智谱 GLM 缓存命中率：根因定位与优化（86.1% → 96%）

> 数据来源：`~/.forebrain/logs/debug.log{,.1,.2}`（2026-09-13 23:12 ~ 2026-09-14 16:44）逐条请求与
> `usage.prompt_tokens_details.cached_tokens` 配对，共 **309 次 GLM 请求 / 13,349,292 prompt token**。
> 实测命中率 **86.14%**，与智谱后台「近 7 天 85.8%」一致，样本有效。
>
> 本文取代上一版结论。上一版把 86% 的浪费归给「智谱侧非确定性整段 miss」，那是在缺少请求级前缀指纹
> 时的推断；`pkg/llm/prefix_fingerprint.go` 上线后拿到了逐请求的分叉点，结论反转：
> **53 代前缀里 28 代是我们自己改前缀改出来的，只有 12 次是 provider 侧异常。**

---

## 0. 结论速览

| 分项 | miss token | 占比 | 归属 |
|---|---:|---:|---|
| ① 审批续跑时环境上下文消息被搬位置 | 456,124 | 24.7% | 我们 |
| ② 记忆指令（developer 块）会话中途重渲染 | 275,400 | 14.9% | 我们 |
| ③ 历史 assistant 的 reasoning 被后续消息决定是否保留 | 109,234 + | 5.9%+ | 我们 |
| ④ 记忆 Phase-2 的 55KB system 里嵌了项目路径 | ~130,000 | 7.0% | 我们 |
| ⑤ provider 侧异常（含③被误判的部分） | 223,656 | 12.1% | 混合 |
| ⑥ 真实冷启动 + 逐轮增量（不可消除） | ~656,000 | 35.4% | 结构 |
| **合计** | **1,850,348** | **100%** | |

按模型拆分：`glm-5.3` 89.69%（154 次 / 8.39M），`glm-5.3-flash` **80.14%**（155 次 / 4.96M）。
Flash 更差，因为记忆流水线与部分主会话跑在 Flash 上，而①②③正好集中在那几个会话。

主方程仍然成立：`命中率 ≈ 1 − Σ(每代前缀的最终 prompt) / Σ(所有请求 prompt)`。
**缩小 prompt 不提命中率，减少"代数"才提。**

---

## 1. 度量口径

智谱走 OpenAI Chat Completion 协议，隐式前缀缓存，计数在
`usage.prompt_tokens_details.cached_tokens`，`pkg/llm/usage.go:ParseCacheUsage` 已正确解析（SSE 亦覆盖）。

```
命中率 = Σ cached_tokens / Σ prompt_tokens
```

`pkg/llm/prefix_fingerprint.go` 对每个请求把 `[model, tools…, messages…]` 做累积哈希，
与该 cache key 上一条链比对，得出**第一处分叉的下标与字段名**，并落三类事件：

- `prompt_cache_break{fork_index, fork_label}` —— 我们改了前缀；
- `prompt_cache_anomaly` —— 链是 append-only 但 provider 没给缓存；
- `prompt_cache_stats{requests, generations, generations_per_request, cache_hit_rate}`。

今日汇总：**291 次请求 / 53 代 / G/N = 0.182 / breaks 28 / anomalies 12**。

最糟的一个会话 `cli-054bde6b`：55 次请求、**13 代**、11 次是我们自己的 break，命中率 **79.6%**。
同一台机器上没有 break 的 `cli-3d8b6582`：55 次请求、2 代，命中率 **92.7%**。差距全在代数上。

---

## 2. 四个根因（每条都有日志实证）

### ① 审批续跑把环境上下文消息从历史里搬走 —— 最大单项

`pkg/run/orchestration_llm.go:consumeResumeSnapshot` 原先把快照历史里所有
`<forebrain_environment_context>` 消息滤掉，再把「当前那条」追加到队尾：

```go
for _, message := range state.Session[:pendingIdx] {
    if !isPromptCacheEnvironmentMessage(message) { session = append(session, message) }
}
if environment, found := latestPromptCacheEnvironmentMessage(currentSession); found {
    session = append(session, environment)
}
```

环境上下文是**已经落盘的历史**，在 `messages[4]` 待着。审批续跑把它抽走 ⇒ 在 `messages[4]` 分叉；
下一个普通回合从 store 重建，它又回到 `messages[4]` ⇒ **再分叉一次**。一次审批 = 两代前缀。

日志实证（同一会话，请求头的消息序列在两种形态之间来回跳）：

```
16:16:56  … | 3:user:c3b67d | 4:user:f04fd9(env) | 5:assistant | …     ← 有 env
16:20:16  … | 3:user:c3b67d | 4:assistant        | 5:tool      | …     ← env 消失
```

对应事件：`fork_label=messages[4]:user, prompt=71111, cached=0`、
`messages[4]:user, prompt=78194, cached=14400`、`messages[4]:assistant, prompt=98181, cached=14336`。
每次只剩 tools+system 的 1.2~1.4 万 token 命中，其余全额重计费。

**修复**：快照历史逐字重放，一个字节都不动；环境确实变了就在 `pending` 之前**追加**一条新的
（含 clear-context 续跑），前面全部继续命中。测试 `TestToolOrchestrationResumeReplaysHistoryVerbatim`。

### ② 记忆指令（developer 块）在会话中途重新渲染

`system[2]` 是 `memory.InjectDeveloperInstruction` 注入的「## Memory captured since the last consolidation」。
它排在整段对话之前，一变就把 tools + system + 所有历史轮次全部作废。今日它在同一会话内长出 6 个版本：

```
5731B → 6093B → 6525B → 7030B → 7840B → 7816B
```

对应 17 次 `fork_label=messages[1|2]:developer`，每次 `cached` 死死卡在 12,032（= tools+system[0..1]），
prompt 却是 61k~78k。

冻结机制本身是对的（`memoryInstructionLLM.instructionForSession`），但
`SessionStore.FreezeSessionPromptState` 在**会话行不存在**时会把「刚渲染出来的那份」原样返回，
调用方无法分辨「冻住了」和「没冻住」——于是每个请求都重渲染一次。

**修复**：`FreezeSessionPromptState` 增加 `frozen bool` 返回值；没冻住时调用方回退到
**进程级冻结**（`instructionForVariant` / `renderFrozen`），绝不返回一次性渲染。
技能目录（`pkg/run/skills.go:catalogForSession`）是同一类缺陷，一并修掉。
测试 `TestMemoryInstructionStaysByteStableWhenTheFreezeIsRefused`（未修时必然失败，已验证）。

### ③ 历史 assistant 消息的线上形态由「后面的消息」决定

`compatMessagesToOpenAI` 原先只给「最后一条带推理的 assistant」保留 `reasoning_content`。
于是模型每产出一轮新推理，**上一轮那条历史消息就被改写**，在它的下标处分叉：

```
15:02:01  … 8:assistant(+R269) 9:tool
15:02:18  … 8:assistant(     ) 9:tool 10:assistant(+R376) 11:tool   ← 第 8 条被改写
15:02:32  … 8:assistant(     ) … 10:assistant(    ) 12:assistant(+R180)  ← 第 10 条又被改写
```

更糟的是：指纹是在投影**之前**对 `[]llm.Message` 算的，这次改写对埋点**不可见**，
所以它被算进了「provider 异常」。`cli-dff79fe3` 连续 4 次 `cached` 卡在 13,824、prompt 从 38k 涨到 52k，
就是这么来的（共 122,393 token 被误记为 provider 的锅）。

**修复**：投影改成**逐消息纯函数** —— 第 i 条消息渲染成什么，只由第 i 条决定，
携带的推理内容一律保留。测试 `TestCompatMessageProjectionDoesNotDependOnLaterMessages`
直接断言「在尾部追加一轮之后，前面每一条的字节必须完全不变」。

> 代价：transcript 会带上历史推理内容。这些 token 全部走缓存读（约 1/4~1/10 价），
> 而换来的是前缀不再每步分叉——按项目铁律（命中率优先于体积）这笔交易是划算的。
> 若后续要压体积，只能选「全部不发」，绝不能再按「最后一条」这种依赖尾部的规则。

### ④ 记忆 Phase-2 的 55KB system 里嵌了项目路径

`pkg/memory/templates/consolidation_project.md`（54,669B）原先把 `{{ memory_root }}` 渲染进正文，
**第一处就在 offset 833**。于是每个项目、每一轮 Phase-2 都是一段全新的 55KB 前缀：

```
offset 833: …/memories/projects/-Users-doudou-workspace-unionj-cloud-forebrain-harness/) ← 10ffd8
offset 833: …/memories/projects/-Users-doudou-workspace-hundsun-…-audit_agent/) ← d18694
```

今日 5 个 Phase-2 前缀变体，其中 5 次「只发一个请求」的调用命中率 **0%**（12,560~12,608 token 全额）。

**修复**：指令与运行期数据沿缓存计费线切开 —— `ConsolidationPrompt{Instruction, Context}`。
模板里 `{{ memory_root }}` 换成固定字面量 `$MEMORY_ROOT`，扩展块也移出正文；
真实路径与扩展清单放进**开场 user 消息**。指令对所有项目、所有轮次逐字节一致，
第二轮起（任意项目）首个请求即可命中约 1.4 万 token。
测试 `TestConsolidationInstructionIsIdenticalAcrossProjects`。

---

## 3. 预期收益

| 项 | 削减的 miss |
|---|---:|
| ① 环境上下文不再搬位置 | 456,124 |
| ② 记忆指令真正冻结 | 275,400 |
| ③ 推理投影逐消息化（含被误判为 anomaly 的部分） | ~221,000 |
| ④ Phase-2 指令跨项目复用 | ~130,000 |
| **合计** | **~1,082,000** |

代入今日流量：miss 1,850,348 → **约 768,000**，命中率 **86.14% → ~94.3%**；
再算上④让 Phase-2 每轮的冷启动从 14k 降到 1k 左右、以及②消除的重复代数，
按「每代最终 prompt 之和」重算地板值约 55~60 万 token，**稳态落在 95.5% ~ 96.5%**。

另外两件不是代码缺陷、而是取舍的事，已按你的决定落地：

### ⑤ 记忆流水线固定到单一模型

`memories` 段原先没配模型，`resolveMemoryLLMProvider` 回落到「该 agent 的第一个 provider」，
于是同一个 55KB 前缀（`d18694`）今日在 `glm-5.3` 与 `glm-5.3-flash` 上各跑了一遍。
智谱按模型分缓存，等于白付两次冷启动。已在 `~/.forebrain/forebrain.yaml` 写死：

```yaml
memories:
  extract_model: glm-5.3-flash
  consolidation_model: glm-5.3-flash
```

### ⑥ 后台流水线避开前台回合

`Runner.RunContent` 现在计数在飞的用户回合，记忆流水线的每一个 LLM 请求（Stage-1 抽取、
Phase-2 整合）都包在 `backgroundMemoryLLM` 里：前台有回合在飞、或刚结束不到 3 秒，就等；
等待上限 2 分钟，一直不空闲也照发，免得记忆永远写不进去。

理由是共享资源：智谱的隐式缓存按账号共享，后台那两段 30KB/55KB 的前缀和用户的对话毫无关系，
两者同时在缓存里就会互相挤占。今日 12 次 anomaly 有多次紧挨着这种交错，
`cli-dff79fe3` 连续 4 次 `cached` 卡在 13,824 就发生在 Stage-1 扇出之后。

代价：记忆写入会晚几秒到几十秒完成。这是拿后台时延换前台命中率，方向与项目铁律一致。

---

## 4. 不变量与回归守护

这次的四个缺陷是同一条不变量的四种破法，写下来以便 review 时直接对照：

> **已经发出去的第 k 条消息，必须永远停在第 k 位，且字节不变。**
> 推论：一条消息渲染成什么，只能由它自己决定，不能由它后面的消息决定；
> 排在对话之前的内容（tools / system / developer 块）必须按会话冻结；
> 需要新鲜度就在**队尾追加**，永远不要回头改写。

守护它的测试：

- `pkg/llm/openai`：`TestCompatMessageProjectionDoesNotDependOnLaterMessages`、
  `TestToolsSerializationIsDeterministicAndOrderSensitive`
- `pkg/run`：`TestToolOrchestrationResumeReplaysHistoryVerbatim`、
  `TestMemoryInstructionStaysByteStableWithinASession`、
  `TestMemoryInstructionStaysByteStableWhenTheFreezeIsRefused`、`TestPromptPrefixBlockOrderStable`
- `pkg/memory`：`TestConsolidationInstructionIsIdenticalAcrossProjects`
- `pkg/state`：`TestSessionPromptStateFreezesTheFirstValue`、`TestSessionPromptStateNeedsASession`
- `pkg/run`（后台避让）：`TestBackgroundMemoryRequestWaitsForTheForegroundToGoQuiet`、
  `TestBackgroundMemoryRequestGivesUpWaitingEventually`

**验证方式**：换上新二进制跑一天，再用同一套脚本对 `~/.forebrain/logs` 复算
`Σcached/Σprompt` 与 `prompt_cache_stats` 的 `generations_per_request`。
`breaks` 必须降到接近 0；任何一次 `fork_label` 指向 `developer` 或 `messages[<10]` 都是回归。

---

## 5. 智谱隐式缓存的行为（实测，继续有效）

| # | 结论 |
|---|---|
| 1 | 隐式、自动、按内容前缀匹配，无需任何参数；无显式缓存 API |
| 2 | 序列化顺序 `tools → system → messages`，改 tools 数组（含顺序）即整段失效 |
| 3 | 块粒度 64 token，`cached` 恒为 64 的倍数且向下取整；无最小长度门槛 |
| 4 | `temperature` / `max_tokens` / `tool_choice` / `user` / `stream` 变化不影响命中 |
| 5 | 中间消息被改 ⇒ 只失效改动点之后的部分 |
| 6 | **跨会话复用有效**：tools+system 字节一致时，新会话首个请求即可命中整段（今日实测 14,782/14,336 = 97%）|
| 7 | TTL ≥ 20 分钟 |
| 8 | 存在约 5% 的非确定性整段 miss —— 真实存在，但远不是主因 |
