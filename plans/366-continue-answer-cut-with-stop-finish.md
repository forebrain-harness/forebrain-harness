# 修复：上游谎报 `finish_reason:"stop"` 时，半句最终回答被当成完整答案

- 状态：待 owner 审批（plan-gated-bugfix 工作流；批准前零源码改动）
- 计划目录落盘（批准后）：`plans/366-continue-answer-cut-with-stop-finish.md` + `plans/README.md` 索引行
- 基线：`git rev-parse HEAD` = `4c387a94ad26658b33dba929544f5f4672036e92`，调查时 `git status --porcelain` 为空。
  **注意**：同树 agent forebrain-harness-db 已回话确认：其 11 份计划全部在独立 worktree 分支
  （exec-skills/exec-runtime/exec-363/exec-364/exec-final），**主树一棵文件未动**；唯一重叠是
  plan 364 改 `pkg/llm/google/agent_llm.go`（+其测试），已提交在 `exec-364`（`7ef6472d`，并入 exec-final）、
  **尚未进主树**。本计划在该文件只改 Execute 续写循环的 `ShouldContinue` 调用行（约 :164），
  不碰 buildRequest/toolResponse/allToolResults/trailingImageTurns 段——合并时冲突面仅该行级改动。
  drift check 以**内容**为准（行号漂移不算漂移）。

## 1. 目标（Why this matters）

owner 截图：wenquxing 项目 `/audit review-plan plans/001-002` 会话的最后一条助手消息停在
「…以及 001 已落地实现的代码质量（那属于执」——括号未闭合、句子未写完，回合却正常结束（`Worked for 14m 57s`），
没有任何「答案不完整」的提示。用户拿到的是半份评审结论。

harness 已有一整套「答案被切断 → 自动续写」恢复机制（`llm.OutputLimitRecovery`，四个协议共用），
但它**只信 provider 自报的切断理由**。上游把切断谎报成 `stop` 时，这套机制完全不触发，
半句答案被落库、被渲染、被当成完整回答。本计划让恢复机制在「provider 声称完整、但答案结构明显未闭合」时也触发。

owner 2026-11-23 裁决（本计划的承重输入）：
1. 修复方向＝复用既有续写机制 + 结构未闭合检测（不新造第二套恢复逻辑）。
2. **续写硬上限从 3 轮改为 100 轮**。
3. 续写耗尽时的用户提示改成**原因中立**措辞（不再断言「output limit 耗尽」）。
4. 允许用真实 dashscope key 做上游探针（已执行，见 §2.4）。

## 2. 根因（字节级证据链，全部已实跑）

### 2.1 现场

- 项目 `/Users/doudou/workspace/hundsun/guangfa/wenquxing`
- 会话 `cli-df0e594e-ac70-4f85-8017-0298b9474cd2`；截断消息 = `fb_messages.id=567541`
  （role=assistant，created 2026-11-23 12:54:54，run `7102893c-b841-4ecc-aef3-3f1d69a3abd4`）
- 模型行 `fb_llm_models.id='llm-legacy-57524805b7e0c258'`：provider=alibaba，
  model=`deepseek-v4.1-flash-1017`，base_url=`https://dashscope.aliyuncs.com/compatible-mode/v1`，
  api_path=`/chat/completions`，params_json=`{"reasoning":{"effort":"xhigh"}}`，catalog_provider 为空。
- DB：`~/.forebrain/state/forebrain.state.sqlite`（只读查询即可复核）。

### 2.2 因果链（每一环都有实证）

1. **上游 SSE 在句子中间结束并自称正常完成。**
   wire 级 dump `~/.forebrain/logs/2026-11-23/x/ff632a-000526.resp.gz`（300 chunks / 297 content deltas / 0 reasoning deltas）：
   倒数第三个 chunk `delta.content="代码质量（那"`，倒数第二个 `delta.content="属于执"`，
   随后 `finish_reason:"stop"`，随后 usage（completion_tokens=1072，text_tokens=1071），随后 `data: [DONE]`。
   dump 是响应体的字节序 tee：`pkg/telemetry/exchange.go:100-134`
   （"sees … the response exactly as it arrived"），`:311-317` 写响应头、`:338-355` 收尾。
2. **harness 没有丢任何字节。** 用 python 重组该 dump 的全部 content delta 得到 2075 chars / 3629 bytes，
   与 `fb_messages.content` **逐字节相等**；`parts[0].text` 也与之相等（同一条 SQL 取出比对）。
   ⇒ 不是落库截断、不是 parts 投影丢段、不是 TUI 渲染裁剪。
3. **不是 DB 字段长度。** `pkg/state/schema.sql` 的 `fb_messages.content/parts` 是 `TEXT`（SQLite 无长度限制）；
   `pkg/state/schema_mysql.sql:158-170` 是 `LONGTEXT`；该文件头 11-15 行已写明 owner 这条规则
   （id/key → VARCHAR(191)，prose/JSON → TEXT/LONGTEXT）。全库 VARCHAR 只用于 id/key/枚举。
4. **不是请求参数。** `ff632a-000526.req.gz`：`max_tokens=32000`（1072 ≪ 32000）、无 `stop` 序列、
   `stream=true`、`reasoning_effort="xhigh"`；338 条 messages，末条是 present_files 的 tool 结果，
   没有任何「收尾/停止」类注入指令（system/user 共 6 条，全在开头）。
5. **harness 侧缺陷（本计划修的那一环）**：`pkg/llm/output_limit.go:35-46`
   `NormalizeStopReason` 只把 `max_tokens|length|max_output_tokens` 归一成 `StopReasonOutputLimit`；
   `:76-81` `ShouldContinue` 在 `!OutputLimitReached(stopReason)` 时直接返回 false。
   于是 `stop` + 半句文本 → 不续写 → `pkg/run/orchestration_llm.go:857` 的耗尽提示分支
   （同样只看 `OutputLimitReached(res.StopReason)`）也不触发 → 半句答案静默成为最终回答。

### 2.3 排除的竞争性解释

| 候选 | 排除证据 |
|---|---|
| DB varchar/TEXT 截断 | content==parts==wire 字节；schema 已是 TEXT/LONGTEXT（§2.2.3） |
| TUI 渲染/行尾裁剪 | DB 里就是半句；截图与 DB 尾字一致 |
| max_tokens 截断 | 1072 ≪ 32000，且 finish_reason 不是 length |
| 上下文超限 | 同进程最大 prompt 466277 tokens 仍正常返回（catalog context=1000000） |
| harness SSE 解析丢 delta | wire 重组 == DB（§2.2.2） |
| `X-Dashscope-Finished: false` 是截断信号 | 473 个响应头全为 false（含 461 个正常 tool_calls），无区分度 |
| `reasoning_effort:"xhigh"` 诱发 | 探针 §2.4：同一请求 xhigh 重放两次都完整收尾 |
| 请求形状/回传 reasoning_content 差异 | 三条路由（api.deepseek.com / dashscope / qianwenaiapi）message shape 一致，都回传 assistant.reasoning_content |

### 2.4 上游探针（owner 已批准，已执行，可复跑）

重放 `ff632a-000526.req.gz` 的原始 body（key 从 `fb_llm_models.api_key` 取出、只经进程内变量传递，不打印不落盘）：

|  run | effort | chars | finish | completion_tokens | 尾部 |
|---|---|---|---|---|---|
| A | xhigh | 1772 | stop | 911 | 「…测完的取值谁拍板。」完整 |
| B | max | 1682 | stop | 888 | 「…由谁在测试环境执行。」完整 |
| C | xhigh | 1795 | stop | 951 | 「…容量验证由谁去做。」完整 |

⇒ **同一请求可完整收尾**：上游提前 EOS 是随机抖动，不是请求内容或 effort 的确定性后果。
harness 无法阻止上游切断，只能在边界上检测并续写——这正是本计划。

### 2.5 发生率与检测器回测（决定检测规则的依据）

- 两天全量 dump：`api.deepseek.com` deepseek-flash 20 个 stop 答案 0 截断；
  dashscope deepseek-v4.1-flash-1017 14 个 stop 答案 2 个半句（000491/000526）；qwen4-max-preview 7 个 0 截断。
- **检测器回测（population 由查询口径定义，计数随活库增长——闸门是「7 命中 / 0 误报」，不是固定条数）**：
  对 `fb_messages` 中**全部**「assistant、visibility=visible、content 非空、parts 无 tool_calls」的消息
  （含 run_id 为空的子代理答案——检测器在每个 adapter 响应上跑，子代理与辅助调用都算；
  调查时 11001 条，评审复核时 11002/11050 条）跑候选规则：
  - 只看「末字符是汉字/无句末标点」→ 约 40 条命中、约 30 条完整 ⇒ 否决。
  - 全文括号/引号计数不平衡 → 23 条命中、约 20 条完整 ⇒ 否决。
  - 代码围栏奇偶 → 2 条命中、均完整 ⇒ 否决。
  - **R1 全文反向扫**（评审指出的形状）→ 8 条命中，含 **1 个误报 `115983`**：
    子代理报告（run_id 空，4909 chars），正文里有 ``grep count `勘误（2026-10-10` ``（代码 span 内的 `（` 无闭合），
    而答案以「…per instructions.\n```」完整收尾。全文扫会把这个好答案判成切断 ⇒ **R1 必须只扫最后一个非空行**。
  - **最终规则（§3.2：R1 末行扫＋配对 span 剔除 + R2 收紧 + gate1）→ 命中 7 条，全部确为半句截断，0 误报**：
    `567541`（本例）、`425091`、`297264`、`127630`、`449330`、`405201`、`405137`。
    探针：`用 \` 包裹代码`/`反引号是 \` 这个符号`/`用 \`（\` 转义`/`完整的一句话。`/`证明。\n\`\`\``/`115983` 尾段 均 no-cut；
    `…\`Submit(ctx`/`…\`添加`/`…\`MEMORY.md\` 的 \``/`…\`（那属于执`/`（见 \`x\` 说明` 均命中。
    最后一条是**实现判别探针**（评审第四轮）：它区分「只删 span」的正确实现与「丢掉首个 span 之前全部文本」
    的错误实现——后者对该形状返回 no-cut，而本例事故尾行与「用 `（` 转义」反例在两种实现下答案相同、钉不住它。
  回测脚本口径（执行者可复跑）：`sqlite3 -readonly -ascii` 取上述 population，python 按 §3.2 规则判定。
  （早期按「每 run 最后一条」取的 2617 条子集曾漏掉 115983 这类子代理答案，已作废。）

## 3. 修复设计

### 3.1 归一到既有语义，不新造机制

唯一状态机仍是 `llm.OutputLimitRecovery`（`pkg/llm/output_limit.go`），四个 adapter 的调用点不变形，
只把**死参数** `producedChars int`（函数体内从未使用）换成真正需要的 `fragment string`。
续写提示词 `Resume()`、轮数上限字段 `rounds`、`Exhausted()` 全部原样复用。

**Resume 提示词首句中性化（owner 2026-11-23 裁决，评审 #6）**：`outputLimitResumePrompt`（`:58`）首句
`Output token limit hit.` → `Your previous answer was cut off before it finished.`，其余逐字保留
（`Resume directly — no apology, no recap of what you were doing. Pick up mid-thought if that is where the cut happened. Break remaining work into smaller pieces.`）。
既有 3 处测试钉的是 `Resume directly` 子串（`output_limit_test.go:82`、`anthropic/agent_llm_test.go:726`、
`compat_llm_test.go:843`），不受影响。`outputLimitThinkingOnlyPrompt`（`:62`）**不动**——
它只在 fragment 为空（诚实 length 切断、思考耗尽）时使用，检测器对空文本不触发。

### 3.2 新增判据 `AnswerCutOff(text string) bool`（导出，供 `pkg/run` 的耗尽提示复用）

语义：**provider 声称答案完整，但文本停在一个从未闭合的结构里**。三条与门：

1. 去掉尾部空白后非空，且**末字符不在「允许的尾字符集」里**（回测实证；注意它含中段标点，
   是精度换召回的刻意取舍，见 §9）：
   `。.！!？?；;：:，,、）)」』》”’】…~✓|>*_-`
   （这一条把回测里仅有的 2 个误报 `333175`/`290062` 全部排除，且不排除任何真截断。）
2. 满足下列**任一**结构证据：
   - **R1 停在未闭合的中文括号/引号内（只扫最后一个非空行，先剔除配对代码 span）**：
     取最后一个非空行 `line`，**在 `line` 的副本上**删掉配对的反引号代码 span（`` `…` ``）——
     代码 span 是字面文本，里面的 `（` 不是 prose 结构（否则「用 `（` 转义」这类完整句会误报）；
     未配对的反引号不删，故「`（那属于执」这种切在 span 里的真截断仍命中。
     然后从该行行尾反向扫，遇到的第一个属于 `（【「『《“‘`（开）或 `）】」』》”’`（闭）的字符是**开**符号。
     **span 剔除只供 R1 使用；R2 必须用原始末行**（评审第三轮 #1：若把剔除后的行喂给 R2，
     反引号计数变偶、R2 永不触发，会丢掉 7 个正例里的 3 个）。
     正例覆盖两臂：R1＝`127630`/`297264`/`425091`/`567541`，R2＝`405137`/`405201`/`449330`。
     **不得全文扫**：全文扫会把「正文代码 span 里有未闭合 `（`、但答案以围栏完整收尾」的好答案判成切断
     （实证误报 `115983`，§2.5）。末行扫＋span 剔除在全量 population 上 7 命中 0 误报。
   - **R2 最后一行有未配对且「真正开了一个 span」的反引号**：取最后一个非空行；
     若该行整行只由反引号/`~`/空格组成（围栏行）则跳过不判；否则要求反引号数为奇数，
     **且最后一个反引号之后紧跟非空白字符或就是行尾**（奇数个时最后一个必未配对；
     后跟空白说明它是被引用的字符或闭合符，不是开 span——这一条排除「用 ` 包裹代码」这类完整句）。
3. （与门之外）**只在 provider 声称「正常完成」时启用**：
   `strings.ToLower(llm.NormalizeStopReason(reason))` ∈ {`stop`, `end_turn`, `completed`}。
   **必须 ToLower**：`NormalizeStopReason` 对非 output-limit 词汇原样返回（`output_limit.go:40`
   `return strings.TrimSpace(reason)`），而 Gemini 报的是大写 `"STOP"`
   （`google/agent_llm.go:880`、`google/stream.go:119`，夹具 `stream_test.go:86` 全为 `"STOP"`；
   `stream.go:110` 自己就用 `EqualFold` 处理大小写）——不 lowercase 则 google 协议的新检测永不启用。
   `length/max_tokens/max_output_tokens` 走既有路径；`content_filter`/`incomplete`/`stop_sequence`/
   `pause_turn`/`refusal`/`SAFETY` 等**不启用**（沿用 `output_limit.go:32-34` 的既有裁决：
   被内容过滤切断的答案重问是错的）。

实现放在 `pkg/llm/output_limit.go`（同文件同概念，零新增生产文件），只用 stdlib `strings`/`unicode/utf8`。
**span 剔除用 `strings.IndexByte` 配对循环，不引 `regexp`**（评审第三轮 #2；`AnswerCutOff` 在每个
无工具调用响应上跑，不做 per-call 编译）：从左找第一个 `` ` ``，再在其后找下一个 `` ` ``；
找到一对则丢弃该对及其内容、继续；找不到第二个则把剩余原文（含那个孤立反引号）原样保留并结束。
语义与 `` `[^`]*` `` 全局替换等价。

### 3.3 `ShouldContinue` 新签名与判定

```go
// 旧（pkg/llm/output_limit.go:76-81，producedChars 是死参数）
func (r *OutputLimitRecovery) ShouldContinue(stopReason string, producedChars, toolCalls int) bool {
	if toolCalls > 0 || !OutputLimitReached(stopReason) {
		return false
	}
	return r.rounds < OutputLimitContinuations
}

// 新
func (r *OutputLimitRecovery) ShouldContinue(stopReason, fragment string, toolCalls int) bool {
	if toolCalls > 0 {
		return false
	}
	if !OutputLimitReached(stopReason) && !claimsCompleteButCut(stopReason, fragment) {
		return false
	}
	return r.rounds < OutputLimitContinuations
}
```

`claimsCompleteButCut` = §3.2 的第 3 条白名单 && `AnswerCutOff(fragment)`（未导出包装，导出的是 `AnswerCutOff` 本身）。

### 3.4 轮数上限 3 → 100（owner 裁决）

`OutputLimitContinuations = 100`。注释必须改写：不再引用「Claude Code 用 3」作为理由，
改为记录 owner 2026-11-23 裁决与代价上界（每轮是一次完整请求；100 轮是硬上界，耗尽走 §3.5 提示）。

**代价上界（评审 #3，必须写明）**：每轮重发整段对话＋累积答案（`compat_llm.go:1274`）。
以本事故请求的 225289 prompt tokens 计，101 轮 ≈ **22.7M input tokens**、按 ~10s/轮 ≈ **17 分钟**
在一个 `Execute` 内（多数输入会缓存命中，实际费用远低于面值）。这是 owner 选定 100 时接受的代价。
**自救**：轮间没有显式 ctx 检查，但下一轮请求经 SDK 立即尊重已取消的 ctx——
取消最迟在当前轮结束时生效，不会挂死。Step 5 必须有「fake provider 持续切断 + 中途 cancel →
Execute  promptly 返回 context.Canceled」的测试钉住这一点。

### 3.5 耗尽提示：原因中立 + 覆盖谎报 stop

- 文案（`OutputLimitExhaustedNotice`）改为：
  `The answer was cut off before it finished and could not be continued further. Raise the model's output limit, lower its reasoning effort, or ask for a smaller piece of work.`
  （只去掉「by this model's output limit」这一因果断言，保留三条建议；同一概念一条文案，两种成因都准确。）
- 触发点 `pkg/run/orchestration_llm.go:857-879`：把条件从
  `if res != nil && llm.OutputLimitReached(res.StopReason)` 扩为
  `if res != nil && (llm.OutputLimitReached(res.StopReason) || llm.AnswerCutOff(text))`，
  其中 `text` 提前到条件之前计算（现有 `text` 在分支内 `:867-870` 计算，上移时**保留 `res.Message != nil` 守卫**）。
  该分支的外层守卫已保证 `len(res.Message.ToolCalls) == 0`（见 `:863-866` 注释），
  所以「工具调用前的半句 preamble」不会误触发；`AnswerCutOff("")==false` 保住空答案分支原语义。
  **thinking-only 守卫**：compat/responses 在「只有推理、没有正文」时把 `TextContent()` 包成
  ```` ```thinking…``` ```` 围栏（`compat_llm.go:1830-1831`、`responses_llm.go:1900-1901`），
  而 adapter 层续写判据用的是裸 content（空）——若不守卫，上游在思考阶段切断并谎报 stop 时
  会出现「从未尝试续写却显示 could not be continued further」。故新条件再加
  `&& !llm.ThinkingFenceOnly(*res.Message)`（现成 helper `pkg/llm/retry.go:737`），
  即完整条件（**nil 安全**：外层守卫允许 `res.Message == nil`，见 `:863-866` 注释）：
  ```go
  text := ""
  if res != nil && res.Message != nil {
      text = strings.TrimSpace(res.Message.TextContent())
  }
  cutByStructure := res != nil && res.Message != nil &&
      !llm.ThinkingFenceOnly(*res.Message) && llm.AnswerCutOff(text)
  if res != nil && (llm.OutputLimitReached(res.StopReason) || cutByStructure) {
  ```
  （`ThinkingFenceOnly` 收 `llm.Message` 值，`pkg/llm/retry.go:737`。）
- **不改各 adapter 的 `Result.StopReason`**（谎报 stop 时仍如实报 stop）：
  避免波及 `pkg/llm/anthropic/agent_llm.go:251` 的 `!llm.OutputLimitReached(stopReason)` 推理回放分支
  （上游提前 EOS 时推理块是完整的，应继续回放）。

### 3.6 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 刻意？ |
|---|---|---|---|
| provider 报 length/max_tokens，文本被切 | 续写 ≤3 轮 | 续写 ≤100 轮 | 是（owner 裁决） |
| provider 报 stop，文本停在未闭合中文括号/引号或开了未闭合反引号 span，末字符不在允许尾字符集 | 半句静默落库+渲染，回合正常结束 | 走同一 Resume 续写；续写文本追加进同一条助手消息（live 流式可见） | 是（本 bug 修复） |
| provider 报 stop，文本完整（含中文列表式无标点收尾、坏围栏、平衡括号、引用反引号字符） | 正常结束 | 正常结束（全量回测 0 误报，口径见 §2.5） | 不变 |
| provider 报 content_filter/incomplete/stop_sequence | 不续写 | 不续写 | 不变 |
| 响应带 tool calls | 不续写 | 不续写 | 不变 |
| 续写 100 轮仍被切 | （3 轮后）显示 output-limit 提示 | 显示原因中立提示；谎报 stop 的场景**首次**获得提示 | 是 |
| 空答案（thinking 耗尽） | OutputLimitEmptyAnswerError | 不变 | 不变 |
| stop + 只有推理围栏、无正文 | 无提示 | 无提示（ThinkingFenceOnly 守卫，不显示「续写耗尽」） | 不变 |

### 3.7 非功能核对

- **延迟/资源**：检测是 O(文本长度) 的纯字符串扫描，只在每个无工具调用的响应末尾跑一次；
  续写只在命中时发生，每轮一次请求，硬上界 100（owner 裁决的代价，已在 §3.4 注释与本矩阵写明）。
- **健壮性**：无新增等待/锁/ goroutine；`AnswerCutOff` 对空串、纯空白、超长文本均安全（反向扫到第一个括号字符即停）。
- **可维护性**：判据与恢复机制同文件；回测 7 正例 2 反例进单测钉住。
- **扩展性**：新协议 adapter 只要调 `ShouldContinue` 就自动获得该能力。
- **UX**：续写文本以流式追加，用户看到答案继续写完；耗尽时有一条准确提示。

### 3.8 辅助调用的继承面（评审 #4，刻意不新增门）

- **结构化输出调用不受影响**：compat 的 `executeChatCompletionUnaryParts` 在结构化路径
  `continueOnLimit=false`（`compat_llm.go:1548` 注释、`:1616` 守卫）；
  responses 的 `executeStructured` 走 `executeSync`（`responses_llm.go:898`），根本不进续写循环。
- **普通文本辅助调用（compaction 摘要、记忆管线等）与主对话共用 adapter，因此继承同一恢复**：
  一份被切断的摘要也会被续写（这本身是想要的），上界同样是 100 轮。
  不为此新增「内部调用豁免」门——那会造出第二套语义，且 owner 的 100 轮裁决是统一的。
  代价与 §3.4 同口径；若将来要豁免，走 `llm.WithMutedForegroundStream` 一类的既有内部调用标记另立裁决。

## 4. Scope

**In scope（唯一允许改的文件）**
1. `pkg/llm/output_limit.go` — 判据、签名、上限、文案
2. `pkg/llm/output_limit_test.go` — 签名更新 + 新判据用例
3. `pkg/llm/openai/compat_llm.go` — 2 处调用点（`:1271`、`:1616`）
4. `pkg/llm/openai/compat_llm_test.go` — 新增「stop 但半句」续写测试；`:857` 测试名/注释随上限更新
5. `pkg/llm/openai/responses_llm.go` — 1 处调用点（`:734`）
6. `pkg/llm/anthropic/agent_llm.go` — 1 处调用点（`:235`）
7. `pkg/llm/anthropic/agent_llm_test.go` — `TestAnthropicGivesUpAfterThree`（`:802`）：
   脚本只有 4 轮而 `anthropicLimitScriptServer` 脚本耗尽后的 fallback 是**无文本的 `end_turn`**（`:652-656`），
   新上限下客户端第 5 轮收到 end_turn、累积文本以「.」结尾 → 停在 5 个请求。
   改法：fallback 改为继续回 `max_tokens`（或按 `1+llm.OutputLimitContinuations` 动态生成脚本），
   `:821` 的 `want 4` 改用常量，测试名与 `:799-801` 注释的「Three」同步改。
8. `pkg/llm/google/agent_llm.go` — 1 处调用点（`:164`）⚠ 与 plan 364 同文件，先协调
9. `pkg/llm/google/agent_llm_test.go` — `:657` 硬编码 `"part2 part3 part4 "` 改为按 `1+llm.OutputLimitContinuations` 循环构造（计数断言 `:654` 已用常量）
10. `pkg/llm/google/stream_test.go` — `:354` 注释措辞 + `:366` 硬编码 `"part2 part3 part4 "` 同样动态构造
11. `pkg/run/orchestration_llm.go` — 耗尽提示条件（`:857-879`）
12. `pkg/run/orchestration_llm_test.go` — 新增「谎报 stop + 耗尽 → 提示」用例（先例 `:11870`）
13. `.claude/skills/run-forebrain/fake_provider.py` — 新增 `stopcut` 模式（验收基建，照 `serve_lengthcontinue` `:636-689` 的形状）
14. `.claude/skills/run-forebrain/SKILL.md` — 模式表新增一行
15. `plans/366-continue-answer-cut-with-stop-finish.md` + `plans/README.md` — 批准后落盘与索引

**Out of scope（看起来相关但明确不碰）**
- `pkg/state/schema*.sql`、`pkg/state/message_sync.go`：DB 不是根因，字段类型已符合 owner 规则。
- `pkg/tui/**`、`pkg/gateway/**`、`frontend/**`：渲染与投影无缺陷，续写文本经既有 delta 通道自然追加。
- `pkg/telemetry/exchange.go`：dump 机制正确，是取证工具。
- `pkg/llm/output_limit.go` 的 `Resume()` 其余句子与 `outputLimitThinkingOnlyPrompt`：不动
  （首句中性化已入 §3.1；不新增第三种提示词变体）。
- 模型行 `catalog_provider`/effort 白名单：探针已排除 effort 诱因；属相邻观察，只报告不修。
- plan 364 的 google 图片投影逻辑、其他 agent 在途的一切改动。

## 5. Steps（每步带验证）

环境前置（每个 shell 会话）：
```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
```

**Step 0a — 磁盘前置（当前已红，必须先做）**
实跑 `df -h /System/Volumes/Data`：调查时 3.1Gi，评审期间 peer 全量构建一度压到 **1.3Gi（100%）**，
低于本计划 §8 的 5Gi 门。按 `macos-disk-reclaim` 技能回收（只读盘点 → 清可再生缓存/历史日志）。
**跨会话协调（已与 forebrain-harness-db 约定）**：`go clean -cache` 前必须等对方
「我这边零 go 进程」的确认消息（对方曾以 13 个 go 进程跑全量套件，清缓存会使其链接 errno=28 假红）；
**该确认已于评审期间收到**（对方最后一批门禁全绿后发出），批准后 Step 0a 先 `df` 复核即可清。
peer 自行删除其 4 个已并入 exec-final 的工作树（~400MB），不属本计划动作；其 `exec-final`（~110M）合并后再回收。
`.worktrees/plan-review-2773413053`（评审模型探针，130M）谁都不动，等 owner 处置。
Docker.raw 的 prune/fstrim 需 owner 批准，不擅自做。回收后 `df` 复跑 ≥5Gi 才继续。

**磁盘现状与分工（tech lead 派发前已做，2026-11-23）**：只读盘点后先清
`~/Library/Caches/com.microsoft.VSCode.ShipIt`（1.6G，可再生更新包）→ 4.6Gi；
随后 peer 给出「零 go 进程」窗口，tech lead 执行 `go clean -cache`（11G）→ **16Gi 可用（92%）**，
§8 的 5Gi 门已满足，Step 0a 由 tech lead 完成，执行器只需 `df` 复核。
跳过：JetBrains 缓存（IDEA 正在跑 JPS 构建）、Google 缓存（Chrome 在跑）、gopls（LSP 在用）、
`~/.forebrain/logs`（含本计划取证用的 2026-11-22/23 dump，不删）。
**执行器不得自行 `go clean -cache`**（peer 收尾门禁还要用缓存；再清需 tech lead 协调窗口）。
peer 通报的低磁盘陷阱：全量套件 link 阶段 ENOSPC 会伪装成多包 `[build failed]`，
先查 `ls -d $TMPDIR/go-build*/go-link*`，必要时全量用 `-p 2` 压峰值。

**Step 0c — worktree 构建前置（ fresh worktree 无 gitignored 依赖）**
`third_party/libtokenizers/`、`third_party/pdfium/` 被 gitignore（`.gitignore:15/19`），
fresh worktree 里没有；`driver.sh cmd_build` 只跑 `go build`、**不会**替你 fetch
（漏了会触发 39MB @ ~15.8KB/s 的下载，约 41 分钟）。开工先 symlink 主树副本：
```bash
MAIN=/Users/doudou/workspace/unionj-cloud/forebrain-harness
ln -s "$MAIN/third_party/libtokenizers" third_party/libtokenizers
ln -s "$MAIN/third_party/pdfium"        third_party/pdfium
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
```
验证：`ls -l third_party/` 两条 symlink 在；`CGO_ENABLED=1 go build -tags fts5 ./pkg/llm` 通过。
（`assets/fonts/*.otf` 亦 gitignored，但本计划验收不渲染 PDF，缺它不影响；不要为它下载。）

**Step 0b — drift check（内容比对，行号漂移不算漂移）**
```bash
git rev-parse HEAD                       # 记录开工基线
grep -n 'func (r \*OutputLimitRecovery) ShouldContinue' pkg/llm/output_limit.go
grep -n 'OutputLimitContinuations = ' pkg/llm/output_limit.go
grep -c 'recovery.ShouldContinue' pkg/llm/openai/compat_llm.go pkg/llm/openai/responses_llm.go pkg/llm/anthropic/agent_llm.go pkg/llm/google/agent_llm.go
grep -n 'llm.OutputLimitReached(res.StopReason)' pkg/run/orchestration_llm.go
grep -n 'part1 part2 part3 part4' pkg/llm/openai/compat_llm_test.go
grep -n 'part2 part3 part4' pkg/llm/google/stream_test.go pkg/llm/google/agent_llm_test.go
grep -n 'MODE == "lengthcontinue"' .claude/skills/run-forebrain/fake_provider.py
```
预期：签名含 `producedChars, toolCalls int`；常量 `= 3`；调用计数 2/1/1/1；
orchestration 条件句、四处测试硬编码文本、fake provider 分派行均在。
任一**内容**不符（不是行号不符）→ STOP，先复核树状态与 peer 在途改动。

**Step 1 — 先复现（红态，不改任何源码）**
按 §6.1 加 `stopcut` 模式（这是验收基建，允许先加），用**当前未修复的二进制**跑：
```bash
export FOREBRAIN_RUN_WORK=$TMPDIR/forebrain-run-cut FOREBRAIN_RUN_SESSION=fb-cut FOREBRAIN_RUN_PORT=8763
D=.claude/skills/run-forebrain/driver.sh
$D build && $D reset && $D start stopcut
$D submit '给我评审结论'
$D wait 'Worked for' 30
$D messages; $D provider-log
```
预期（红）：provider-log 只有 **1** 条 turn REQUEST；DB 最后一条 assistant  content 以「（那属于执」结尾；屏幕无 incomplete 提示。
把这份输出存进计划的验收记录（红态证据）。

**Step 2 — `pkg/llm/output_limit.go` ＋ 五个调用点（一次落地，树始终可编译；评审 #7）**
按 §3.1–§3.5 改 `output_limit.go`：新增 `AnswerCutOff` + 未导出 `claimsCompleteButCut`（白名单 ToLower）；
`ShouldContinue` 换签名；`OutputLimitContinuations = 100`（注释改写）；`OutputLimitExhaustedNotice` 换文案；
`outputLimitResumePrompt` 首句中性化。**同一次改动**里把五个调用点换参数：
`compat_llm.go:1271/:1616` 传 `contentAcc.String()`；`responses_llm.go:734`、`anthropic/agent_llm.go:235`、
`google/agent_llm.go:164` 传 `answer.String()`（各调用点下一行的 `Resume(...)` 已在用同一表达式）。
验证：`gofmt -l pkg/llm/output_limit.go` 无输出；`CGO_ENABLED=1 go build -tags fts5 ./...` 通过；
`grep -rn 'ShouldContinue(' pkg --include='*.go' | grep -v _test` 无 `\.Len()` 残留。
（不得先改签名后改调用点分两步落地——中间态编译不过。）

**Step 3 —（已并入 Step 2，无独立改动）**

**Step 4 — `pkg/run/orchestration_llm.go` 耗尽提示**
按 §3.5 上移 `text` 并扩条件。
验证：`CGO_ENABLED=1 go build -tags fts5 ./pkg/run` 通过。

**Step 5 — 单测**
- `output_limit_test.go`：既有 4 处 `ShouldContinue` 调用改传字符串；新增表驱动用例：
  正例 7 条（§2.5 的真实尾段，逐字内联；R1 四条＋R2 三条两臂都要有）＋「切在未闭合 span 内」正例
  （`…\`（那属于执`）＋实现判别正例 `（见 \`x\` 说明`（钉住「只删 span、不删 span 前文本」，§2.5）；
  反例：`333175`/`290062` 尾段、中文列表无标点收尾、
  平衡括号、整行围栏收尾、`用 \` 包裹代码`、`反引号是 \` 这个符号`、`用 \`（\` 转义`、**`115983` 形状（正文代码 span
  含未闭合 `（`、答案以「…instructions.\n```」完整收尾）**、空串、纯空白、
  `content_filter`+半句、`stop_sequence`+半句、大写 `STOP`+完整答案、带 tool calls。
  轮数用例改用常量循环（已有）。`TestOutputLimitRecoveryResume` 增断言：首句==中性化新文案、
  仍含 `Resume directly`（既有子串断言不动）。
- **取消自救测试（评审 #3，第二轮收紧形状）**：`TestCompatStopCutCancelAbortsPromptly`：
  compat httptest fake 每轮都回半句+stop，**在第 2 轮的 handler 内部 cancel ctx**
  （或 handler 阻塞在 `<-ctx.Done()` 再由测试 cancel）——瞬时 fake 会在 ~26ms 内跑满 101 轮、
  外部 cancel 落不进去，测试空转（评审实测 requests=101 err=nil）。
  断言：`requests ≤ 3` 且 `errors.Is(err, context.Canceled)`（评审实测 3 requests / 133ms）。
- `compat_llm_test.go`：照 `TestLengthContinueCarriesOneAnswerPerRound`（`:756`）的 httptest SSE 形状新增
  `TestCompatStopFinishWithCutAnswerContinues`：request 1 发半句 + `finish_reason:"stop"`，
  request 2（断言其 body 含 `Resume directly — no apology`）发剩余半句 + stop；
  断言物理请求=2、`Result.Message.TextContent()`=两段拼接、`StopReason=="stop"`。
  另加反例：完整答案 + stop → 物理请求=1。
  `:857` 测试改名 `TestCompatLengthContinueUsesContinuationBudget`，注释同步；
  **`:914` 硬编码 `"part1 part2 part3 part4 "` 改为按 `1+llm.OutputLimitContinuations` 循环构造期望文本**。
- `anthropic/agent_llm_test.go`：按 §4 第 7 项改 fallback/脚本/计数/测试名（只改 `:821` 不够，会红在 5≠101）。
- `google/stream_test.go`：`:354` 注释措辞 + **`:366` 硬编码 `"part2 part3 part4 "` 动态构造**
  （default 分支 `:339-340` 每轮重复 part4，100 轮下重复 98 次）。
- `google/agent_llm_test.go`：**`:657` 同样硬编码 `"part2 part3 part4 "` 动态构造**（default 分支 `:640-641`）。
- google 协议新增「`"STOP"`（大写）+ 半句 → 续写」用例，钉住 §3.2 第 3 条的 ToLower（B2 回归）。
- `orchestration_llm_test.go`：照 `:11870` 先例新增「stop + 半句 + 续写耗尽 → 恰好一条 ResponseNoticeIncomplete 且文案==常量」。
验证：
```bash
CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... -run 'OutputLimit|AnswerCutOff|LengthContinue|StopFinish|StopCut|ContinuationBudget|GivesUp|MaxTokensContinues' -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'Incomplete|OutputLimit' -count=1
```
全绿；新行为测试在**未修复**代码上必须红——按 §6.3 的完整 overlay 集合证红
（只 overlay 单文件会编译失败，那是空闸门；编译失败不算红）。

**Step 6 — 包级回归 + 门禁**
```bash
gofmt -l cmd pkg third_party plugins          # 必须无输出
CGO_ENABLED=1 go vet -tags fts5 ./...
CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/... ./pkg/run ./pkg/architecture -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1 -timeout 30m
```
失败若落在本计划未改的文件 → 不修，peer_send 问 forebrain-harness-db（系统提示要求）。

**Step 7 — 真机验收（绿态 + 回归 + 反证 + 耗尽）**
见 §6.2 四场景，逐屏 `$D screen`/`$D messages`/`$D provider-log` 取证。

**Step 8 — 收口**
- 本文件补「验收记录」（红态/绿态/反证/耗尽 → 证据）。
- 落盘 `plans/366-continue-answer-cut-with-stop-finish.md`（自包含，含本文件全部承重内容），
  `plans/README.md` 表尾追加一行（格式照 `:2089` 的 364 行）：状态先 TODO，验收后改 DONE+日期。
- 清理：`$D stop`；`rm -rf $TMPDIR/forebrain-run-cut`（仅本计划自建目录）。
- 改动留工作区，**不 commit**（owner 手动提交）。

## 6. 验收基建与场景

### 6.1 fake provider 新增 `stopcut` 模式

`fake_provider.py`：在 `:584` 的 `lengthcontinue` 分派旁加
`if MODE == "stopcut" and self.serve_stopcut(body): return`，并照 `serve_lengthcontinue`（`:636-689`）写 `serve_stopcut`：
- **开头先挡背景请求**：`if not _is_turn_request(body): self.serve_background(body); return`
  （现成 helper `:432`，先例 `serve_multishell:1231`）。否则记忆管线/compaction 的背景请求也拿半句+stop，
  各自触发续写循环，`FAKE_STOPCUT_ALWAYS=1` 时每个背景调用跑 101 轮，场景计数必破。
  续写轮仍带工具表、是 turn 请求，不受影响。
- 请求 body 含 `Resume directly — no apology`（中性化前后都稳定的子串）→ 这是续写轮：
  默认发 `行阶段的事）。` + `finish="stop"`；`FAKE_STOPCUT_ALWAYS=1` 时改发同一段半句文本 + stop（用于耗尽场景）。
- 否则 → 发 `未评审范围：\`sql/x.sql\` 的 SQL 语义（那属于执` + `finish="stop"` + usage。
- 一律 `data: [DONE]` 收尾（与真实上游一致：切断也正常收尾，这正是本 bug 的形状）。
- **计数取证用 `FAKE_DUMP_DIR`**（SKILL.md 先例）：REQUEST 日志行（`:524`）只记 path/stream/mode，
  无 body 无 turn 标记，「恰好 N 条 turn REQUEST」必须按 dump 的 body 区分（含 Resume 标记的轮数）。
`driver.sh` 无需改（mode 作为 argv 透传，`:110`；help 枚举陈旧属 NIT，不修）；`SKILL.md` 模式表加一行说明。

### 6.2 四场景（隔离 home/端口，绝不污染真实 ~/.forebrain）

1. **复现→修复**：`stopcut`。绿态预期：provider-log 出现第 2 条 REQUEST 且其 body 含 Resume 提示词；
   DB 最后 assistant content == 半句+续写拼接（以「。」结尾）；屏幕显示完整句子。
2. **回归**：`reply '完整的一句话。'`。预期：1 条 REQUEST，无续写，无提示。
3. **反证（误报守卫）**：`reply` 文本取 `…已用编辑前后状态对照替代证明。`、`…证明。\n\`\`\``（整行围栏收尾）、
   `用 \` 包裹代码`、`反引号是 \` 这个符号`、`用 \`（\` 转义`、以及 `115983` 形状（正文含 `` `勘误（2026-10-10` ``、
   答案以「…per instructions.\n```」收尾）各跑一次。预期：均 1 条 turn REQUEST、不续写
   （R2 收紧条款、R1 末行扫与 span 剔除的回归守卫，§3.2）。任一触发续写 → STOP（§8）。
4. **耗尽**：`FAKE_STOPCUT_ALWAYS=1 $D start stopcut`。预期：`1+OutputLimitContinuations`（=101）条 REQUEST，
   屏幕出现原因中立提示（§3.5 文案），DB 消息可见、回合正常结束。

### 6.3 红态证明（不污染工作树）

新测试的红态用 `go test -overlay`，**overlay 集合必须覆盖全部签名耦合文件**：
HEAD 版的 `pkg/llm/output_limit.go` ＋ 四个 adapter ＋ `pkg/run/orchestration_llm.go` ＋ HEAD 版
`pkg/llm/output_limit_test.go`（它调用旧签名），存到 `$TMPDIR` 后以
`{"Replace":{…}}` 注入；只跑**不引用新 API** 的行为测试（compat `TestCompatStopFinishWithCutAnswerContinues`、
orchestration 的 notice 用例）→ 必须 FAIL（断言红，**编译失败不算红**，那是空闸门）；
去掉 overlay → PASS。仓库树零改动（先例：plans/README 2026-11-23 条目同款手法）。
`AnswerCutOff` 自身的单测引用新符号，不参与 overlay 红态证明。

## 7. Done criteria（机器可查）

1. `grep -c 'producedChars' pkg/llm/output_limit.go` = 0（死参数删净）。
2. `grep -n 'OutputLimitContinuations = 100' pkg/llm/output_limit.go` 命中 1。
3. `grep -rn 'recovery.ShouldContinue' pkg --include='*.go' | grep -v _test | grep -c '\.Len()'` = 0。
4. `grep -c 'by this model' pkg/llm/output_limit.go` = 0（旧因果断言删净）。
5. `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0；`gofmt -l cmd pkg third_party plugins` 空；`go vet -tags fts5 ./...` exit 0。
6. §5 Step 6 四条测试命令全绿（`-run` 门数 `--- PASS`，不信 exit code  alone）。
7. 真机四场景证据齐全（§6.2），红态证据在 Step 1 已留。
8. `git status --porcelain` 的改动集合 ⊆ §4 In scope。**plans/ 是被 git 跟踪的**
   （`git ls-files plans/` = 455；`.gitignore:76-77` 的 `# docs/plan/`/`# plans/` 是注释，
   `git check-ignore plans/366-x.md` exit 1）——计划文件会出现在 git status 里，属 in-scope，
   但仍**不 commit**（owner 手动提交）。
9. `plans/README.md` 的计划表里有 `366-continue-answer-cut-with-stop-finish` 索引行（是表行，不是 `wc -l` 行数），状态与验收一致。

## 8. STOP conditions

- Step 0 内容漂移（签名/常量/调用点计数不符）→ 停，先核树与 peer。
- `pkg/llm/google/agent_llm.go` 主树内容出现 plan 364 的 tool-result 投影改动（说明 exec-364 已并入主树）
  → 停，先与 peer 核对合并顺序；不得 revert/stash 对方改动。本计划只改 :164 调用行，正常应无冲突。
- 新测试在 overlay 旧版上不红 → 测试没钉住缺陷，停，重写测试。
- 真机场景 3 出现续写（误报）→ 停，检测规则需收紧，不得带误报上线。
- 全量回归红在本计划未改文件 → 停，peer_send 归因，不顺手修。
- 磁盘 `df -h /System/Volumes/Data` 可用 < 5Gi → 先按 macos-disk-reclaim 恢复再继续（本仓曾因此 errno=28）。

## 9. Maintenance notes

- 100 轮是 owner 裁决的硬上界：每轮一次完整请求，误报或上游持续切断时最多 100 次额外调用；
  耗尽提示是唯一的用户可见兜底。复核者若收紧上限，须重走 owner 裁决。
- `AnswerCutOff` 是**结构证据**而非语义理解：它只认「未闭合的中文括号/引号」「奇数反引号行」两类，
  且要求末字符不在允许尾字符集里。新增规则前必须先跑 §2.5 的回测口径（查询定义 population，
  闸门＝7 命中 0 误报，不是固定条数——库是活的），0 误报才可加入。
- **「0 误报」是语料界内结论，不是普适保证**。R2 的「后跟非空白」条款已排除
  「用 ` 包裹代码」「反引号是 ` 这个符号」；R1 的配对 span 剔除已排除「用 `（` 转义」（探针实测 no-cut）。
  残余可构造误报：完整答案以**行尾孤立反引号**收尾且该反引号是被引用的字符（如「分隔符是 `」）——
  语料未出现。**误报的真实代价不是「多一次调用」，而是续写文本被追加进一条本来完整的答案**（内容污染），
  外加一条错误的「答案不完整」提示；续写文本完整即停，不会跑满 100 轮。
  若 owner 要求绝对零误报，R2 需再收紧——那是新裁决，执行器不得自行做。
- 已知漏报（精度换召回的代价，不修）：①代码块**中间**被切（末非空行无反引号，R2=0）；
  ②恰好在 `，、：；` 等中段标点后切断（gate1 集含它们）；③闭括号后切断；
  ④英文答案半句截断（无中文括号、无反引号）；⑤未闭合的 `（「“` 在**上一行**、切断发生在下一行
  （末行扫看不到它）——全文扫与末行扫的差异仅 115983 一个误报，故此形状**未测到**真截断。
  以上回测语料里均未出现真截断；若将来出现，按同口径回测后再扩。
- gate1 的集合实为「允许的尾字符」而非严格句末标点（含中段标点），措辞与实现都按此理解。
- 上游诱因未定（探针证明是随机抖动）：若 dashscope 将来给出真实截断信号（header/finish_reason），
  优先归一到 `NormalizeStopReason`，而不是再加检测规则。
- 相邻观察（只报告不修）：模型行 `deepseek-v4.1-flash-1017` 因名字带 `-1017` 后缀且 catalog_provider 为空，
  匹配不到 catalog `deepseek/deepseek-v4.1-flash`，故无 effort 白名单、max_tokens 落默认 32000。
- 后续项（不在本计划 scope）：`Exhausted()` 无生产调用方（仅 `output_limit_test.go:55` 使用），
  属**先于本计划**的死件，按 owner 死代码规则另立清理，不在本计划顺手删。
  （Resume 首句中性化已由 owner 2026-11-23 裁决并入 §3.1，不再是后续项。）
- 同树 peer 通报的两条现场事实（只记录，不修）：①`pkg/tool` 全量偶发红＝plan 358 的 process ledger
  与 `t.TempDir()` RemoveAll 竞态，peer 已在 `e393f8ee` 修（尚未进主树）——本计划全量回归若撞这条先归因到 peer；
  ②既有缺陷 `pkg/safety/killProcessGroup`（`backend_unix.go:41-47`）只发一次 SIGKILL、全 `pkg/safety`
  无 `WaitDelay`，与信号同时 fork 的孙进程会持管道致 `cmd.Wait()` 永不返回——与本次截断无关；
  **owner 已令 peer 本期一并修复**（其子代理 Fix-wedge-WaitDelay 在途），本计划不碰 `pkg/safety`。
  给该修复留一条已核实的事实：`DefaultWaitTimeout`（`backend_unix.go:51`、`backend_windows.go:1116`）
  声明后**全仓零使用**（grep 仅命中声明与注释），按 owner 死代码规则属该修复应收敛的死常量。
  ③peer 通报的环境依赖红：`pkg/run` `TestCaptureScreenReturnsAPngAndItsPixels`（`capture_test.go:156`）
  断言 note 含 "image"，该词只在截屏超 3MiB 预算降采样时出现（取决于显示器分辨率）；
  `FOREBRAIN_IMAGE_MAX_BYTES=200000` 下绿；peer 正按根因修。**本计划全量若撞它：只报告、不修**
  （可用该 env 复跑佐证归因），不在 scope 内。

## 10. 评审回应（owner 指定 alibaba/deepseek-v4.1-flash-1017 冷读，2026-11-23）

逐条核实后全部采纳，无实质分歧；核实证据如下：

| # | 评审意见 | 处理 | 我的独立核实 |
|---|---|---|---|
| 1 | R1 全文扫误报「以围栏完整收尾、正文代码 span 含未闭合（」的答案 | 采纳：R1 只扫最后非空行（§3.2） | 11001 条 population 实跑：全文扫 8 命中含误报 `115983`；末行扫 7 命中 0 误报。`115983` 正文确含 `` `勘误（2026-10-10` ``、尾部「…per instructions.\n```」 |
| 2 | 回测 population 漏子代理/辅助答案 | 采纳：扩到全部 11001 条无工具调用 assistant 文本（§2.5） | 同上实跑；早期 2617 子集作废 |
| 3 | 100 轮无代价上界说明、无取消自救检查 | 采纳：§3.4 写明 22.7M tokens/~17min 上界；Step 5 加 cancel 测试 | 每轮重发整段对话见 `compat_llm.go:1274`；轮间无显式 ctx 检查但下轮请求即尊重取消 |
| 4 | 辅助文本调用继承续写、结构化调用是否安全未说明 | 采纳：新增 §3.8 | `compat_llm.go:1548/:1616` continueOnLimit=false；`responses_llm.go:898` executeStructured→executeSync，均不进续写循环 |
| 5 | plans/ 并非 gitignored | 采纳：§7.8 改正 | `git check-ignore plans/366-x.md` exit 1；`git ls-files plans/`=455；`.gitignore:76-77` 是注释 |
| 6 | Resume 首句对模型断言假成因 | 采纳：owner 裁决中性化首句（§3.1） | 3 处测试钉 `Resume directly` 子串，中性化后仍绿 |
| 7 | Step 2→3 中间态编译不过 | 采纳：合并为一步（Step 2） | 签名与调用点必须同改 |

评审另在 `.worktrees/plan-review-2773413053/` 留了红/绿探针（非本会话产物，不动不交付）；
本计划以 Step 1 的真机红态与 §6.3 的 overlay 红态为权威证据。

### 10.1 第二轮评审（同模型，修订稿复核）

七条旧发现全部确认已修；新发现逐条核实后采纳：

| # | 意见 | 处理 | 核实 |
|---|---|---|---|
| 1 | 取消测试用瞬时 fake 会空转（101 轮 ~26ms 跑满、外部 cancel 落不进） | 采纳：handler 内第 2 轮 cancel／阻塞 `<-ctx.Done()`，断言 requests≤3 + errors.Is（Step 5） | 评审实测两种形状：瞬时 101/nil（空转）、延迟 3/133ms/Canceled（真） |
| 2 | §3.6 矩阵残留已作废的 2617 | 采纳：改「全量回测 0 误报，口径见 §2.5」 | 承重章节（§2.5/§3.6/§9）已无 2617；仅历史记录保留作废说明 |
| 3 | 固定 population 条数当闸门（活库会增长） | 采纳：闸门＝查询口径＋7 命中 0 误报 | 我自己复跑时库已从 11001 长到 11002 |
| 4 | 末行 R1 新漏报：未闭合括号在上一行 | 采纳：§9 漏报⑤「未测到」，不修 | 全文扫与末行扫差异仅 115983 一个误报 |
| 5 | 误报代价被低估（续写文本追加进完整答案）；R1 不剔代码 span 致「用 `（` 转义」误报 | 采纳：§9 改写代价；**R1 增加配对 span 剔除**（§3.2） | 复跑全量 population：7 命中 0 误报不变；「用 `（` 转义」no-cut、「`（那属于执」仍命中 |
| 6 | 「README 有 366 行」歧义 | 采纳：§7.9 改为「366-… 索引行」 | — |

无实质分歧。第二轮后无待 owner 裁决项（Resume 首句中性化已在第一轮后裁决）。

### 10.2 第三轮评审（同模型，span 剔除稿复核）

六条旧发现确认已修（含活库复跑 7 命中/11053、9/9 探针表相符）；新发现采纳：

| # | 意见 | 处理 | 核实 |
|---|---|---|---|
| 1 | §3.2 未写明 span 剔除只供 R1；喂给 R2 会丢 3/7 正例 | 采纳：§3.2 明写「R1 用副本、R2 用原始末行」＋两臂正例划分 | 我的回测实现本就是 r1 用 span.sub、r2 用原始末行；7 正例中 R1=4、R2=3 |
| 2 | 「只用 strings/utf8」与正则处方矛盾 | 采纳：改用 `strings.IndexByte` 配对循环（语义等价 `` `[^`]*` `` 全局替换），不引 regexp、不做 per-call 编译 | 循环语义逐条写明于 §3.2 |
| 3 | 补 `DefaultWaitTimeout` 死常量事实供后续修复 | 采纳：§9 记录 | grep 全仓仅命中 `backend_unix.go:51`/`backend_windows.go:1116` 声明与注释，零使用 |

另：peer 已受 owner 令本期修复 `pkg/safety` 的 Wait/WaitDelay 楔死（子代理在途），本计划 scope 不含 `pkg/safety`。
第三轮后无待 owner 裁决项。

### 10.3 第四轮评审（同模型）：verdict = approve

三条旧发现确认已修（span 剔除 R1-only、IndexByte 循环语义等价、DefaultWaitTimeout 事实），
并额外核实白名单覆盖四协议（responses `completed`、compat `stop`、anthropic `end_turn`、google `STOP`）
与 thinking-only 围栏两处引用。唯一改进项（非缺陷）已采纳：
正例表加 `（见 \`x\` 说明`——判别「只删 span」与「丢 span 前文本」两种实现（我独立复跑：True/False/True 相符）。
磁盘 1.4GiB 仍红，由 Step 0a 的 peer 协调条款在批准后解除（零 go 进程确认已收到），计划无需为此改动。
第四轮后无待 owner 裁决项，评审无遗留 material problem。

## Implementation Status

- **Verdict: APPROVE**（tech lead 独立复核，2026-11-23）。
- **Worktree / branch / commit**：`.worktrees/fix-cut-answer-stop-finish`，分支 `fix-cut-answer-stop-finish`，
  commit `00e451a8`（基线 `4c387a94`）。**未合并、未 push**——合并是 owner 的决定。主树保持 `4c387a94` 干净。
- **执行器**：qodercn/dfmodel（owner 指定），一次派发即 COMPLETE，无 REVISE 轮。
- **磁盘**：tech lead 在 peer「零 go 进程」窗口清 VSCode ShipIt（1.6G）＋ go-build（11G）→ 16Gi；执行器复核 15Gi/13Gi。

### 步骤完成情况
- Step 0a 磁盘：tech lead 完成；执行器只复核（未自行清缓存）。
- Step 0c symlinks、Step 0b drift：done（内容全匹配；google/agent_llm.go 与 HEAD 逐字节同 ⇒ plan 364 未进主树）。
- Step 1 红态：done（未修复二进制：1 条 REQUEST、0 条 resume dump、DB 尾「（那属于执」、屏幕无提示）。
- Step 2/4/5：done（签名+五调用点一次落地；orchestration 条件；单测全绿＋§6.3 overlay 红态为断言红而非编译红）。
- Step 6：done（gofmt 净、vet 0、聚焦 9 包 ok、全量 37 ok 0 FAIL）。
- Step 7：done（四场景，执行器取证＋tech lead 复跑，见下）。
- Step 8：清理 done；plans/366 与 README 索引由 reviewer 落盘（本文件同步副本）。

### 偏差与判断（均已登记、均在 scope 内或属环境）
1. `AnswerCutOff` 额外 import `unicode`（`unicode.IsSpace` 判「反引号后是空白＝引用字符」），仍无 regexp。
2. 末非空行先 TrimRight 空白，使「行尾」与读者所见一致。
3. 新 orchestration 测试命名 `TestFragmentClaimedCompleteEndsTurnWithIncompleteNotice`，使 Step 6 的 `-run 'Incomplete|OutputLimit'` 能选中它。
4. 第二个 anthropic 测试 `TestAnthropicExhaustedCutCarriesNoThinking` 同因（4 轮脚本撞旧 3 轮预算）按 §4 第 7 项同款修法动态生成轮数；共享 script server 的 fallback 未动。
5. fresh worktree 的 `pkg/gateway/dist` 只有 .gitkeep（gitignored 构建产物），从主树既有 build 复制种子使 `pkg/architecture` 可跑；git status 不受影响、未触产品文件。
6. 未加 anthropic 专属「stop+半句」行为测试：协议无关的 `TestShouldContinueOnClaimedCompleteStop` 已覆盖 `end_turn`，计划只点名 google 的 `STOP` 行为测试（已加）。
7. fake provider 的 stopcut 说明放在常量块注释＋方法 docstring＋SKILL.md 行（lengthcontinue 亦无独立 docstring 模式条目）。
8. 全量未用 `-p 2`（无 ENOSPC/go-link 症状）；peer 在修的 capture 环境红本次未出现。

### Tech lead 亲跑复核（不信任报告，全部自跑）
- 判据 1–4：`producedChars`=0；`OutputLimitContinuations = 100` 单命中（:200）；生产调用点 `.Len()`=0；`by this model`=0。
- 判据 5：worktree 内 `gofmt -l cmd pkg third_party plugins` 空；`go build -tags fts5 ./...` OK；`go vet -tags fts5 ./...` 0。
- 判据 6：`-run 'OutputLimit|AnswerCutOff|LengthContinue|StopFinish|StopCut|ContinuationBudget|GivesUp|MaxTokensContinues'` → pkg/llm 等 7 包 ok；
  `./pkg/run -run 'Incomplete|OutputLimit'` → 4 条 --- PASS；`./pkg/llm/... ./pkg/run ./pkg/architecture` → 9 包 ok；
  `./pkg/... ./cmd/forebrain -timeout 30m` → **37 ok、0 FAIL**。
- 判据 7（真机，隔离 home/端口 8791）：
  ① stopcut → 2 REQUEST、1 条 resume-marked dump、DB「…（那属于执行阶段的事）。」、屏幕完整句、无提示；
  ② 反证 A（115983 形状）与 B（`用 \`（\` 转义`）→ 各 1 REQUEST、0 resume、DB 文本不变；
  ③ 耗尽 FAKE_STOPCUT_ALWAYS=1 → 101 REQUEST / 100 resume-marked、屏幕「Response incomplete」＋原因中立文案、回合正常结束。
- 判据 8：`git diff --stat 4c387a94..HEAD` = 14 文件，全在 §4 第 1–14 项；worktree `status --porcelain` 空；主树 `4c387a94` 干净。
- 测试审计：7 条真实正例逐字＋2 条实现判别正例＋115983/引用反引号/配对 span 反例；白名单表含 STOP 大写与 filter/refusal 排除；
  cancel 测试在 handler 第 2 轮内 cancel、断言 requests≤3＋errors.Is；notice 测试断言恰好一条且文案==常量、fragment 保留。
- 判据 9：plans/366 索引行由 reviewer 落盘（见 plans/README.md）。

### 未完成 / 留给 owner
- ~~合并 `fix-cut-answer-stop-finish`（`00e451a8`）回 dev：owner 决定~~ **已执行（owner 2026-11-23 令「合并回主树dev分支」）**：
  主树 dev `4c387a94` → `00e451a8` **ff-only**，`git diff 00e451a8 HEAD` 空（landed==tested）。
  随后 peer 依 owner 同令把 `exec-final`（21 提交，重基后 `653f3785`）ff-only 落 dev ⇒ **dev 现 `653f3785`**；
  google/agent_llm.go 与 agent_llm_test.go 两处冲突面**自动合并**成功（peer 重基时验证）。
- **合并后主树门禁（tech lead 亲跑，dev `653f3785`）**：`gofmt -l` 净、`go vet` 0、
  `go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1 -p 2 -timeout 40m` → **37 ok / 0 FAIL**。
  此前聚焦门（pkg/llm/…+run+architecture 9 包 ok）在 `00e451a8` 上已绿。
- **索引行协调**：peer ff 落地被我的未提交 README 行挡住 → 按 peer 选项 2 `git stash push -- plans/README.md`，
  落地后 `stash pop` **自动合并成功**（366 行 :2091，peer 353–364 行 intact），stash 已 drop。
- **worktree 清理**：`fix-cut-answer-stop-finish` worktree＋分支在 FULLY_MERGED 验证（diff dev..branch=0、status=0）后删除。
- 评审模型遗留探针 worktree（plan-review-2773413053，detached）：owner 处置，本会话未动。
- peer 在其重基分支删除的 3 个先存死符号（catalog_test.go `requireRepoAssets`、:1088 `request`、
  compat_media_test.go `compatTestPDF`）已随 `653f3785` 进 dev；与本修复无因果，全量绿证明无连坐红。
- 主树未提交仅剩 `plans/366-…md`（untracked）＋ `plans/README.md`（366 索引行），留 owner 手动提交。