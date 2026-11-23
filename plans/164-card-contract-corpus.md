# Plan 164（设计/探路）：把「卡片契约」做成 Go 与网页共读的一份黄金语料

> **执行者须知**：这是**设计/探路计划**，不是"照着重构"的计划。产出是**一份可运行的最小原型 + 一份写下来的
> 决定**，不是全量迁移。每一步都给了停止线：做完 Step 3 就可以交回来评审，不要继续扩。
> 触发 "STOP conditions" 时停下报告。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "func BuildToolMeta" pkg/tool/format.go` 为 `1`；
> `grep -c "func ParseShellCard" pkg/tool/shell_card.go` 为 `1`；
> `grep -c "export function toolStepFromPayload" frontend/src/composables/useChatStream.ts` 为 `1`；
> `ls pkg/tool/testdata/header_actions.json` 存在。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/tool frontend/src/composables/useChatStream.ts` 必须**无输出**。

## Status

- **Priority**: P2（每条卡片规则今天都写两遍，139/154 已经各自为此付过一次代价）
- **Effort**: M（探路；原型 1 份语料 + 两侧各一个读取测试）
- **Risk**: LOW（只加测试与 testdata，不动生产代码路径）
- **Depends on**: 无（`plans/161` 改的是渲染 key，与本计划不冲突）
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: direction（测试基础设施 / 跨语言契约）

## Why this matters

这个仓库的"工具卡片"是**一份契约、两套实现**：

- 引擎侧算出卡片事实：`pkg/tool` 的 `BuildToolMeta`（`format.go:211`）、`DisplayHeaderParts`
  （`header_parts.go:25`）、`ParseShellCard`（`shell_card.go:42`）、`ParseFileDiff`（`shell_card.go:130`）；
- 终端直接用这些结构画卡；
- 网页把它们**重新推导一遍**：`toolStepFromPayload`（`useChatStream.ts:787`，"live" 载荷）、
  `toolStepFromRow`（`:854`，持久化行）、`subagentCallView`（`:2332`）、`previewFieldsFromMeta`（`:491`）、
  以及 `frontend/src/lib/approvalGates.ts`（自己再判一遍审批族顺序、自己数 diff 行）。

后果是可预测的：**同一条规则要在两个地方各修一次**。已经发生的两次：

1. `plans/154`：拒绝时用户写的那句话。引擎侧是 `ToolMeta.DenyFeedback`（`notification.go:338`，
   `json:"deny_feedback,omitempty"`），网页侧是 `denyFeedback?: string`（`useChatStream.ts:247`，
   `:794`/`:867` 读 `deny_feedback`）。投影漏了一个字段，网页就永远画不出第二行。
2. `plans/139`：子代理失败原因只说一次。终端是 `fanoutDrawnCallError`（`pkg/tui/reducer.go:888`），
   网页是 `drawnCallError`（`useChatStream.ts:2447`，注释自己写着 "Mirrors the terminal's
   fanoutDrawnCallError"）。两份实现、两个测试、靠人记住。

**而"共读一份黄金语料"在本仓已经是既有机制**（不是新发明）：

- `pkg/tool/testdata/header_actions.json`：Go 的 `TestHeaderActionsGolden`（`header_parts_test.go:154`）
  用 `UPDATE_HEADER_ACTIONS=1` 重写它，`frontend/src/locales/toolHeader.test.ts:7` 静态 import 同一份文件
  （注释解释了为什么不用 `node:fs`：这个包没有 `@types/node`）；
- `pkg/turn/testdata/composer_tokens.json` + `composer_slash_accept.json` → `frontend/src/lib/composerTokens.test.ts:8,11`，
  注释把意图写得很清楚：*"Reading that one file here is what makes the two surfaces one contract instead of two
  implementations that happen to agree today."*；
- `pkg/event/testdata/subagent_live_resume_events.json` → `frontend/src/lib/forebrainGatewayRuntime.test.ts:3`。

所以本计划要做的不是"发明机制"，而是**把同一机制扩到卡片**：一份 `pkg/tool/testdata/cards.json`，
Go 断言"引擎算出的卡片事实 == 语料"，网页断言"从同一份 payload 重建的卡片 == 语料"。
两边一旦分叉，两侧测试同时红。

## Current state（探路要用的全部事实）

| 契约面 | 引擎（Go） | 网页（TS） |
|---|---|---|
| 卡片头（四段） | `HeaderParts`/`DisplayHeaderParts`（`header_parts.go:17/25`）、`HeaderActions()`（`:413`） | `ToolHeader` 的 `parts`（`ai-elements-vue/packages/elements/src/tool/ToolHeader.vue`）+ `frontend/src/locales/toolHeader.ts` |
| 卡片事实 | `BuildToolMeta(StepEvent) ToolMeta`（`format.go:211`）；`ToolMeta` 定义在 `notification.go:292` | `toolStepFromPayload`（`:787`）/ `toolStepFromRow`（`:854`） |
| shell 卡 | `ParseShellCard(body) (ShellCard, bool)`（`shell_card.go:42`） | `ShellCardBody.vue`（吃 `ShellCard` 类型，`useChatStream.ts` 导出） |
| 文件 diff 卡 | `ParseFileDiff(body) (FileDiff, bool)`（`shell_card.go:130`） | `ToolCallCard.vue` 的 file-diff 分支 |
| 预览字段 | `ToolMeta` 上的 `preview_*` | `previewFieldsFromMeta`（`:491`） |
| 审批族/事实 | `pkg/safety/engine.go` 的求值顺序；`ToolMeta` 的 rationale/diff | `approvalGates.ts:81` 自己再实现一遍顺序 |
| "未运行"句子 | `tool.RejectionStoppedDisplayKey` | `REJECTION_STOPPED_KEY`（`useChatStream.ts:372`，注释要求**逐字节一致**） |

两侧现有的黄金断言（探路时要读的范例，不要重写它们）：

- Go：`pkg/tool/display_header_test.go:14`、`header_parts_test.go:18/82/154`、`shell_card_test.go:17/133`、
  `diff_blocks_test.go:38/85/129`、`format_test.go:490/518`、`notification_test.go:140/272/352`；
- TS：`frontend/src/composables/useChatStream.test.ts` 的 `describe('the engine header of a tool card')`
  与 `describe('shell card and file diff facts')`（读 live 载荷与持久化行两路）、
  `frontend/src/components/chat/ToolCallCard.test.ts:256/274/307/412`。

## Scope

**In scope（只加这些）**：
- `pkg/tool/testdata/cards.json`（**新建**，语料）
- `pkg/tool/cards_golden_test.go`（**新建**，Go 侧读取/断言 + `UPDATE_CARD_CORPUS=1` 重写）
- `frontend/src/lib/cardCorpus.test.ts`（**新建**，TS 侧读同一份文件并断言重建结果）

**Out of scope（这一期绝对不做）**：
- 改任何生产代码（`format.go`、`header_parts.go`、`shell_card.go`、`useChatStream.ts`、`approvalGates.ts` 都不动）
- 把已有测试迁移到语料（语料只做**新**的跨语言断言；已有测试原样保留，它们更细）
- 审批族顺序（`approvalGates.ts` 与 `pkg/safety/engine.go`）——那需要先在语料里定义"审批族"的语义，
  属于下一份计划，本期只在语料里放**已定型**的卡片事实
- 终端（`pkg/tui`）的渲染断言：终端已经直接消费引擎结构，交叉点由 Go 侧断言覆盖
- 任何 UI 改动

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| Go 侧语料测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run '^TestCardCorpus' -count=1 -v` | PASS |
| 重写语料 | `UPDATE_CARD_CORPUS=1 CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run '^TestCardCorpus$' -count=1` | 写文件、PASS |
| TS 侧语料测试 | `cd frontend && pnpm vitest run src/lib/cardCorpus.test.ts` | PASS |
| 类型检查 | `cd frontend && pnpm typecheck` | exit 0 |
| gofmt | `gofmt -l pkg/tool` | 无输出 |

## Steps

### Step 1：先写"用例清单"（半小时，不写代码）

在计划文件末尾的「原型记录」小节里写清**要覆盖哪几张卡、每张卡要断言哪几个字段**。起步只要 5 个用例：

1. `shell_local`：一条本机 shell，有 stdout/stderr/exit 0（断言：header 四段、`ShellCard` 的 streams 与
   `exit_code`、`stdout` 非空）。
2. `shell_failed`：exit 2，stderr 有内容（断言：`exit_code`、状态词、stderr 存在）。
3. `shell_truncated`：带 `output_retrieval.retrieve_id`（断言：截断事实与 retrieve_id 出现在卡片事实里）。
4. `file_edit`：`edit_file` 的一次修改，带 unified diff（断言：`FileDiff` 的文件名/增删行数/`unified_diff` 非空）。
5. `denied_with_words`：`Denied + DenyFeedback`（断言：`tool_meta.deny_feedback` 逐字等于语料里的那句话 ——
   这一条直接锁住 `plans/154`）。

每条用例只需要：一个**输入**（`StepEvent` 的最小构造，或一段 tool body 字符串）与**期望的卡片事实**。
输入用 Go 结构体写、由 `UPDATE_CARD_CORPUS=1` 序列化进 `cards.json`；**不要**把 Go 类型塞进 JSON，
JSON 里只放已经"形状化"的卡片事实（与网页 DTO 同形）。

### Step 2：Go 侧读取 + 重写

新建 `pkg/tool/cards_golden_test.go`，照 `header_parts_test.go:154` 的样式：

```go
// TestCardCorpus pins the card facts both surfaces draw from. The file is what
// the web's cardCorpus.test.ts reads, so a change on either side that the other
// cannot reproduce fails here.
func TestCardCorpus(t *testing.T) {
	golden := filepath.Join("testdata", "cards.json")
	cases := cardCorpusCases()             // 本文件里的 5 个输入
	got := make(map[string]any, len(cases))
	for name, c := range cases {
		got[name] = c.facts()              // 调 BuildToolMeta / ParseShellCard / ParseFileDiff / DisplayHeaderParts
	}
	if os.Getenv("UPDATE_CARD_CORPUS") == "1" {
		b, err := json.MarshalIndent(got, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(golden, append(b, '\n'), 0o644))
	}
	raw, err := os.ReadFile(golden)
	require.NoError(t, err, "run with UPDATE_CARD_CORPUS=1 to write the corpus")
	var want map[string]any
	require.NoError(t, json.Unmarshal(raw, &want))
	require.Equal(t, want, normalizeJSON(t, got))   // 统一数字/空切片，避免 0 vs 0.0 的假红
}
```

**要求**：json 里的字段名与网页 DTO 同名（`header`、`header_parts`、`tool_meta`、`shell_card`、`file_diff`）——
这正是网页从 `/ws/chat` 与 `/api/chat/sessions/:id/messages` 收到的形状。

**Verify**：`UPDATE_CARD_CORPUS=1 CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run '^TestCardCorpus$' -count=1`
→ 生成 `testdata/cards.json`；再跑一次不带 UPDATE → PASS。

### Step 3：TS 侧读同一份文件并重建

新建 `frontend/src/lib/cardCorpus.test.ts`，静态 import（与 `toolHeader.test.ts` 同一理由：没有
`@types/node`）：

```ts
import { describe, expect, it } from 'vitest'
import corpus from '../../../pkg/tool/testdata/cards.json'
import { toolStepFromPayload, toolStepFromRow } from '@/composables/useChatStream'

// 每条用例把语料里的事实**当成网关下发的载荷**喂给重建函数，断言重建出来的卡片 = 语料。
describe('the card corpus the engine and the web share', () => {
  for (const [name, entry] of Object.entries(corpus as Record<string, any>)) {
    it(`rebuilds ${name} from the payload the gateway sends`, () => {
      const live = toolStepFromPayload({ tool_meta: entry.tool_meta, header_parts: entry.header_parts, ...entry })
      expect(live.header).toBe(entry.header)
      expect(live.shellCard ?? null).toEqual(entry.shell_card ?? null)
      expect(live.fileDiff ?? null).toEqual(entry.file_diff ?? null)
    })
  }
})
```

（`toolStepFromPayload` 的真实入参形状以 `useChatStream.ts:787` 的定义为准；本步骤允许你把载荷字段
对齐一次，但**不要**为了迁就测试去改生产函数的签名——若对不上，按 STOP conditions 报告。）

**Verify**：`cd frontend && pnpm vitest run src/lib/cardCorpus.test.ts` → 全 PASS；
`cd frontend && pnpm typecheck` → exit 0。

### Step 4：写决定（不是代码）

在计划文件末尾补一节「原型结论」，至少回答：

1. 语料覆盖哪 5 条、**没**覆盖哪些（以及为什么：例如审批族顺序、子代理卡片需要跨事件时间线，
   不适合单个 payload）。
2. 新增一条卡片规则时，**哪一步必须同时改语料**——把它写成一句可以贴进 AGENTS/FOREBRAIN 的话。
3. 语料里**绝不能**出现的东西：任何真实密钥、真实用户路径、真实会话 id（全用合成值）。
4. 遗留问题清单（见下）。

**Step 4 之后停下，交回评审**。不要顺势把已有测试迁进来，也不要动 `approvalGates.ts`。

## Test plan

- Go：`pkg/tool/cards_golden_test.go` 的 `TestCardCorpus`（带 `UPDATE_CARD_CORPUS=1` 重写路径）。
- TS：`frontend/src/lib/cardCorpus.test.ts` 按语料逐条重建。
- 两个测试共享的只有 `pkg/tool/testdata/cards.json` 一个文件——与 `header_actions.json` 完全同构。

## Done criteria

机器可检查，全部满足：

- [ ] `pkg/tool/testdata/cards.json` 存在，5 条用例，字段名与网页 DTO 同名
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run '^TestCardCorpus' -count=1` PASS
- [ ] `cd frontend && pnpm vitest run src/lib/cardCorpus.test.ts` PASS
- [ ] `cd frontend && pnpm typecheck` exit 0
- [ ] `gofmt -l pkg/tool` 无输出
- [ ] `git status --porcelain` 只显示 Scope 里的三个新文件（外加本计划文件末尾的「原型记录/结论」小节）
- [ ] 语料里没有真实密钥/真实路径/真实会话 id（`grep -Ei "sk-|/Users/|/home/" pkg/tool/testdata/cards.json` 无输出）
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数 ≠ 1、`header_actions.json` 不在，或 drift check 有输出且被引用符号变了。
2. 网页的重建函数**需要**生产签名改动才能吃语料（例如它只接受 `tool_call_completed` 事件而不是载荷）：
   把它的真实入参打印出来（`console.log` 后删掉），报告后再定——那是"契约形状"的设计问题，要评审。
3. 引擎侧同一份输入在两次运行里给出不同的事实（时间戳、随机 id）：不要把它们塞进语料，
   先在报告里列出这些不稳定字段，语料只放稳定字段。
4. 你发现 `BuildToolMeta` 的某个字段已经**只有**网页在读（引擎侧无人使用）：单列出来，
   它可能是"契约已经漂了"的信号，属于下一份计划。
5. 你想顺手把 `approvalGates.ts` 的顺序判定也搬进语料：本期明确不做，先报告。

## Maintenance notes

- 语料是**两条实现之间的合同**，不是"第二种测试写法"。它的价值只在于"两侧同时红"；
  如果某天两侧测试都改成只读语料、不再断言引擎真实输出，它就退化成一份快照，必须重做。
- 新增卡片规则的正确顺序：先改引擎 → 跑 `UPDATE_CARD_CORPUS=1` → 让网页测试红 → 修网页 → 两个测试都绿。
  这和 `header_actions.json` 的既有流程一致。
- 语料会随规则变长。设一个上限（例如 40 条），超过就按"卡片家族"分包（shell / file / subagent / gate），
  不要让它变成一份没人读的大 JSON。

## 原型记录（Step 1，执行时落定）

5 条用例 = 合成 `StepEvent` 输入 + 引擎推导的期望事实（语料里只有事实、没有 Go 类型）：

1. `shell_local`：`make`、stdout "hello"、exit 0。断言 header 四段（Ran/make/–/1.5s）、`shell_card.stdout`、
   `exit_code:0`、`display_body` 含 streams。
2. `shell_failed`：exit 2 + stderr。**修正**：引擎对 exit≠0 **不**设 `Error`（设了会渲染 error 卡而非
   shell 卡）；非零 exit 的失败事实就在 `shell_card` 里——这本身就是语料锁住的一条规则。
3. `shell_truncated`：`stdout_omitted_bytes`+`retrieve_id`。**修正**：计划原文的
   `output_retrieval.retrieve_id` 是模型侧 resultPayload 的键名；卡片事实走 completionOutput 的
   **顶层** `retrieve_id`（即 `ParseShellCard` 读回的形状），语料按真实形状构造。
4. `file_edit`：edit_file + turn_diff(+2/-1)。断言 `file_diff` 的 path/added/deleted/unified_diff、
   header "Edited a.go"。
5. `denied_with_words`：`DeniedToolStep(..., "use git clean instead")`。断言
   `tool_meta.deny_feedback` 逐字相等（锁 plans/154）、status "denied"、header action 实为
   **"Denied"**（执行前预判 "Ran"，语料锁真实输出）。

## 原型结论（Step 4，2026-10-26 执行批）

1. **覆盖**：上表 5 条。**未覆盖**：审批族顺序（`approvalGates.ts:81` vs `pkg/safety/engine.go`，需先在
   语料定义"审批族"语义，属下一份计划）；子代理卡片（跨事件时间线，单 payload 画不出）；`preview_*`
   图像预览字段（可进语料，本批未放）；read_file 的 `result_lines`/`result_offset`、skill 卡；running 态
   （`started_at_ms` 依赖 now，语料只收 settled 卡天然避开）；终端 `pkg/tui`（直接消费引擎结构，
   Go 侧断言已覆盖交叉点）。
2. **新增卡片规则时必须同时改语料**（可贴 AGENTS/FOREBRAIN）："改任何卡片事实（`BuildToolMeta`/
   `DisplayHeaderParts`/`ShellCard`/`FileDiff`/卡片 body 格式）后，必须 `UPDATE_CARD_CORPUS=1` 重写
   `pkg/tool/testdata/cards.json` 并让 `frontend/src/lib/cardCorpus.test.ts` 同步通过——语料是 Go 与
   网页的合同，只改一侧不改语料就是违约。"
3. **语料绝不能出现**：真实密钥、真实用户路径（`/Users/…`、`/home/…`）、真实会话/run/step id——全用
   合成值（本批：`/w`、`step-shell-ok`）。Done criteria 的 grep 已验证干净。
4. **开放问题**：
   - **网关组装层不在语料覆盖内**：facts() 在测试里复刻了网关组装（`server.go:435`
     `gatewayToolCompletedStepData` + `wsevents.go:900` `withStepHeader`/`toolEventPayloadWithHeader`）；
     若网关改组装（如不再发 `header_parts`），语料不红。后续可把组装提为共享 helper 或给网关层加断言。
   - **两处双侧形状差异显式写成文档（非漂移）**：① 网页 `ShellCard.exitCode` 的 "0 and absent are the
     same"（TS 侧把 0 折叠为缺失，Go 侧 `*int` 序列化为 `exit_code:0`）；② 网页对 `displayBody` 做
     `.trim()`，body 尾部空行在网页侧消失——断言 trim 后相等。
   - **`toolStepFromPayload` 真实入参**是 `(payload, completed, fallbackStepId)`，且前端在
     `normalizeRunEventPayload` 里对整个 wire payload 做 deep `toCamelCase` 后才喂给它；测试用
     `toCamelCase(entry)` 复刻 wire→page 真实路径，未改任何生产签名。
   - STOP 4 检查过：未发现 `BuildToolMeta` 字段只剩网页在读；STOP 5 未触碰 `approvalGates.ts`。
