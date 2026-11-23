# Plan 171：会话消息里的工具正文只发一次

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。完成后把 `plans/README.md` 里本计划的状态行改为 DONE
> （若评审方派发你并说明由其维护索引，则不改）。
> **禁止提交代码**（owner 手动提交）：不 commit、不 push、不开 PR、不建分支，改动直接留在工作区。
> 工作区里与本计划无关的未提交改动**不要碰、不要回滚、不要格式化**。
>
> **前置（否则 STOP 1）——锚点仍在**：
> `grep -c "Content:       t.Content" pkg/gateway/api_extra.go` 为 `1`；
> `grep -c "m.ShellCard = card" pkg/gateway/api_extra.go` 为 `1`；
> `grep -c "func shellCardFor" pkg/gateway/wsevents.go` 为 `1`；
> `grep -c "content: row.content ?? ''" frontend/src/composables/useChatStream.ts` 为 `1`。
>
> **Drift check（先跑）**：`git diff --stat 7865932 -- pkg/gateway/api_extra.go pkg/gateway/wsevents.go frontend/src/composables/useChatStream.ts` 必须**无输出**。
>
> **基线（Step 0 之前跑一次，记下失败清单；否则 STOP 2）**：
> `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` 与
> `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts src/views/ChatView.test.ts`。

## Status

- **Priority**: P2（历史越长、响应越胖；单条工具正文今天最多重复三份）
- **Effort**: M（先量、再删一份、再改两个前端消费点）
- **Risk**: LOW–MED（改的是网页读历史的 DTO；TUI 不经过它）
- **Depends on**: 无
- **Blocks**: 无
- **Planned at**: dev `7865932`，2026-10-26
- **Category**: perf（载荷）

## Why this matters

`GET /api/chat/sessions/:id/messages` 给每个工具行同时下发**三份同一段正文**的来源：

1. `Content` —— 模型面（model-facing）的工具结果，对 shell 来说就是 `{"stdout":…,"stderr":…,"exit_code":…}` 那份 JSON
   （`pkg/tool/shell_tool.go` 的 `resultPayload` 被 `json.Marshal` 后作为工具结果返回，落到 `llm.ToolResultMessage`）；
2. `PartsJSON` —— 展示面（display）的卡片正文（`tool.FormatToolStepResult`，上限 `DefaultMaxFormattedBody = 120_000`）；
3. `ShellCard` —— 由**第 2 份**解析出来的结构化事实，而它自己又带着 `stdout`/`stderr`
   （`pkg/tool/shell_card.go:13-24`：`ParseShellCard` 从 body 里把流文本再读一遍）。

网页真正用来画卡的是第 2、3 份（`ToolCallCard` → `ShellCardBody` 读 `card.stdout`/`card.stderr`）；
第 1 份是**给模型看的机器载荷**，网页除了"这一行有没有内容"这一个布尔判断外并不使用它（见下）。

一个 120 KB 的 shell 结果，在历史接口里就是约 200–350 KB 的 JSON；一屏十几条这种卡片、
进一次会话就下载一次。删掉第 1 份是最小、最安全的一刀：它既不参与卡片渲染，也不是"死字段"
（它仍在 transcript 与工具结果里），只是**不该出现在网页的历史 DTO 里**。

## Current state

DTO 与赋值（`pkg/gateway/api_extra.go:2037-2076` 的 `msg`、`:2123-2132` 的赋值、`:2170-2181` 的卡片）：

```go
	type msg struct {
		ID             string              `json:"id,omitempty"`
		RowID          int64               `json:"row_id,omitempty"`
		Role           string              `json:"role"`
		Content        string              `json:"content"`
		…
		PartsJSON    string `json:"parts_json,omitempty"`
		…
		ShellCard *tool.ShellCard `json:"shell_card,omitempty"`
		FileDiff  *tool.FileDiff  `json:"file_diff,omitempty"`
		…
	}
	…
			m := msg{
				ID:            t.MessageID,
				RowID:         t.RowID,
				Role:          t.Role,
				Content:       t.Content,
				…
			}
			…
			switch strings.ToLower(strings.TrimSpace(t.Role)) {
			case "tool":
				header, parts, body, toolName := storedToolRowHeaderParts(displayRoot(), t.PartsJSON, t.ToolMetaJSON, …)
				m.Header = header
				if parts.Action != "" {
					m.HeaderParts = &parts
				}
				if card := shellCardFor(toolName, body); card != nil {
					m.ShellCard = card
				}
				if diff := fileDiffFor(toolName, body); diff != nil {
					m.FileDiff = diff
				}
```

网页侧对工具行的使用（`frontend/src/composables/useChatStream.ts:997-1018`）：工具行走
`toolStepFromRow(row, …)`，读的是 `toolStepId`/`toolMetaJson`/`partsJson`/`header`/`headerParts`/`shellCard`/`fileDiff`，
**不读 `row.content`**。

唯一间接用到 `content` 的两处（都在 `ChatView.vue`，且都只看"是否非空"）：

- `:2359` `if (String(msg.content ?? '').trim()) return true` —— `messageHasBubble(msg)` 的第一条；
- `:309` `MessageResponse v-else-if="String(msg.content ?? '').trim()"` —— 块为空时的兜底渲染。

对**工具行**来说，这两处的语义都该是"没有气泡"：卡片才是这一行的意思（`messageHasBubble` 的注释自己写着
"A row that only issued a tool call carries no text at all: its card is what the row means"）。
今天只是因为工具行的 `Content` 恰好非空，它们才走进"有气泡"的分支。

## Scope

**In scope（只改这些）**：
- `pkg/gateway/api_extra.go`（工具行不再填 `Content`；并给该分支加一行注释说明为什么）
- `pkg/gateway/api_extra_test.go`（新增 1 个测试）
- `frontend/src/composables/useChatStream.ts`（工具行不把 `row.content` 抄进 `ChatMessage`）
- `frontend/src/views/ChatView.test.ts`（新增 1 个挂载测试）

**Out of scope（看着相关也不要动）**：
- `PartsJSON` 与 `ShellCard` 的**二选一**：卡片正文（parts）与卡片事实（shell_card）都被渲染用到，
  砍任何一份都要重写 `ShellCardBody.vue` 的取数路径——那是另一个计划，本计划只砍"没人渲染"的那份。
- `pkg/state` 的存储格式、transcript、`/events`、WS 实时帧（**实时路径不动**：它走的是事件，不是这个 DTO）
- 单条消息接口（若有）与子代理历史接口：本计划只改**会话历史列表**这一条 DTO 的工具行
- `storedToolRowHeaderParts` / `shellCardFor` / `fileDiffFor` 的内部实现
- TUI（它直接读状态库，不经过这个 DTO）
- 分页（`:1996` 的 `ListAllMessages(ctx, sid, 0)` 无 LIMIT）——是同一族的另一个问题，见 Maintenance notes

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| Step 0 量体积 | 见 Step 0 脚本 | 打印 `content` / `parts_json` / `shell_card` 三者字节数 |
| 网关定向 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run '^TestChatMessagesOmitToolContent' -count=1 -v` | PASS |
| 网关回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | 与基线相比无新增 FAIL |
| 前端定向 | `cd frontend && pnpm vitest run src/views/ChatView.test.ts -t 'tool row'` | PASS |
| 前端回归 | `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts src/views/ChatView.test.ts` | 与基线相比无新增 FAIL |
| 类型/构建 | `cd frontend && pnpm typecheck`；`cd frontend && pnpm build` | exit 0 |

## Steps

### Step 0（强制）：先量，再决定

**没有这一步的结论就不要往下做。** 在一个**隔离 home** 里用假 provider 跑一条会产出大输出的 shell
（或直接用现成的会话库副本），把历史接口的响应按行统计：

```bash
# 用隔离 home，别碰 ~/.forebrain
export H="$(mktemp -d)"; FOREBRAIN_HOME="$H" ./build/bin/forebrain gateway &   # 或用既有 driver.sh 起
# 跑一条 shell：seq 1 20000 > /tmp/big.txt; cat /tmp/big.txt
curl -s "http://127.0.0.1:6060/api/chat/sessions/<sid>/messages" > /tmp/msgs.json
python3 - <<'PY'
import json
d=json.load(open('/tmp/msgs.json'))
rows=d.get('messages') or d
for r in rows:
    if r.get('role')!='tool': continue
    print(r.get('id'), 'content', len(json.dumps(r.get('content',''))),
          'parts', len(r.get('parts_json') or ''),
          'card', len(json.dumps(r.get('shell_card') or {})))
PY
```

判定门：**若某条工具行的 `content` 字节数小于该行 `parts_json + shell_card` 的 1/4，本计划不划算 → 停下报告**
（把三列数字贴出来，评审另定）。正常预期是 `content` 与 `parts_json` 同量级、`shell_card` 再叠一份。

**Verify**：上面脚本打印出三列数字；把结论（做/不做）写进报告。

### Step 1：网关侧——工具行不再填 `Content`

`pkg/gateway/api_extra.go` 的赋值处（`:2123-2132`）把 `Content: t.Content` 从结构体字面量里**移出**，
改到 `switch` **之后**按角色补：

```go
			// A tool row's Content is the model-facing result — for a shell,
			// the JSON map with its stdout — and the page draws that row from
			// parts_json and shell_card instead. Sending it here repeated the
			// same bytes a second and third time, so the history DTO carries the
			// display copy only.
			if !strings.EqualFold(strings.TrimSpace(t.Role), "tool") {
				m.Content = t.Content
			}
```

（位置就在那个 `switch` 之后、`visible` 判定之前；`strings` 已 import。）

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run '^TestChatMessagesOmitToolContent$' -count=1 -v`
→ 需要先写这个测试（见 Test plan）；写之前它是 `no tests to run`。

### Step 2：网关测试

在 `pkg/gateway/api_extra_test.go` 里照同文件既有"造会话 + 调 handler"的写法（搜 `handleChatMessages`
或 `/messages` 相关用例作范例）新增：

```go
// The history DTO drops a tool row's model-facing content: the page draws that
// row from parts_json and shell_card, and the model payload would be a second
// and third copy of the same bytes.
func TestChatMessagesOmitToolContent(t *testing.T) {
	// … 造一条 assistant 行 + 一条 role=tool 的 transcript 行（content 非空、parts_json 非空）…
	// 调 handler，解出 messages
	// require: 该 tool 行的 Content == ""（JSON 里可以缺省）
	// require: 同一行的 PartsJSON 仍非空
	// require: 同一会话里的 user/assistant 行 Content 一字不差（防止误伤）
}
```

**Verify**：`… -run '^TestChatMessagesOmitToolContent$' -count=1 -v` → PASS。

### Step 3：网页侧——工具行不把 content 抄进消息

`frontend/src/composables/useChatStream.ts` 的工具行分支（`:997` 那个 `case 'tool':` 之前的
`out.push({ … content: row.content ?? '', … })` 路径）——工具行**不走** `out.push`，它走 `openTurn(...)` 后
`current.blocks = [...(current.blocks ?? []), { kind: 'tool', step: toolStepFromRow(row, …) }]`。
所以 `ChatMessage.content` 对工具行本来就是 `''`（`openTurn` 建的消息对象不带该行的 content），
**这一处很可能不需要改**——先确认：

**Verify**：`grep -n "case 'tool'" -A 12 frontend/src/composables/useChatStream.ts` 读一遍：
若工具行确实不写 `content`，Step 3 就是**空操作**，在报告里写明"无需改动"；
若写了（例如 `openTurn` 把 `row.content` 塞进消息），把它改成不塞。

### Step 4：网页测试——工具行仍画卡、且不再出现空气泡

`frontend/src/views/ChatView.test.ts` 新增。**先看三条脚手架事实**（照抄下面修正过的形状，不要凭记忆写）：

1. `fake` 是 `vi.hoisted(() => ({} as Record<string, unknown>))`（`:17`）——裸 `fake.messages.value =` 过不了
   `pnpm typecheck`，既有用例全部带 cast：`(fake as { messages: { value: unknown[] } }).messages.value = [...]`；
2. `mountView()` **默认 stubs `ToolCallCard: true`**（`:162`）——`.tool-card` 类不会渲染；既有断言卡片的用例
   （`:1282`、`:1358`）用的是 `findAllComponents({ name: 'ToolCallCard' })`（对 stub 同样有效）；
3. `mountView` 是 async（`:140`），既有用例都 `await mountView()`。

```ts
it('draws a tool row from its card alone, with no bubble for the model payload', async () => {
  ;(fake as { messages: { value: unknown[] } }).messages.value = [{
    id: 'm1', role: 'assistant', content: '',
    blocks: [{ kind: 'tool', step: { stepId: 's1', toolName: 'shell', summary: '', header: 'Ran cat big.txt' } }],
  }]
  const wrapper = await mountView()
  await flushPromises()
  expect(wrapper.findAllComponents({ name: 'ToolCallCard' })).toHaveLength(1)
  expect(wrapper.text()).toContain('Ran cat big.txt')
  // 工具行不因为"有 content"而多出一个消息气泡
  expect(wrapper.findAll('[data-testid="message-copy"]')).toHaveLength(0)
})
```

（若 `message-copy` 的选择器在本文件里已有更稳的用法，跟着用；关键断言是"卡片在、气泡不在"。）

**Verify**：`cd frontend && pnpm vitest run src/views/ChatView.test.ts -t 'tool row'` → PASS；
`cd frontend && pnpm typecheck` → exit 0。

### Step 5：双向回归

**Verify**：
`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` 与
`cd frontend && pnpm vitest run src/composables/useChatStream.test.ts src/views/ChatView.test.ts` 与
`cd frontend && pnpm build` → 与基线相比无新增 FAIL / exit 0。

## Test plan

- 网关：`TestChatMessagesOmitToolContent` —— tool 行 `content` 空、`parts_json` 仍在、user/assistant 行不受影响。
- 网页：`draws a tool row from its card alone…` —— 卡片在、气泡不在。
- Step 0 的度量脚本不进仓库（是一次性诊断），但它的三列数字要写进报告。

## Done criteria

机器可检查，全部满足：

- [ ] Step 0 的三列数字写在报告里，且结论是"做"
- [ ] `TestChatMessagesOmitToolContent` PASS
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` 与基线相比无新增 FAIL
- [ ] `cd frontend && pnpm vitest run src/composables/useChatStream.test.ts src/views/ChatView.test.ts` 与基线相比无新增 FAIL
- [ ] `cd frontend && pnpm typecheck` 与 `pnpm build` 均 exit 0
- [ ] `grep -c "if !strings.EqualFold(strings.TrimSpace(t.Role), \"tool\")" pkg/gateway/api_extra.go` 为 `1`
- [ ] `git status --porcelain` 只显示 Scope 里的文件（外加 `pkg/gateway/dist/` 的构建产物）
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

停下报告，不要即兴发挥，如果：

1. 前置锚点的任一 grep 计数与标注不符，或 drift check 有输出且"Current state"摘录与现场代码对不上。
2. 基线命令本身跑不起来（编译失败/环境错误），拿不到失败清单。
3. Step 0 的度量显示 `content` 并不比 `parts_json` 大（比值 ≥ 1/4）：本计划不划算，**停下**，把数字贴出来。
4. 你在 Step 4 发现工具行的气泡其实是**有意**的（有设计稿或注释支持"工具行也显示模型载荷"）：
   那说明这条 DTO 字段有读者，报告后停手——不要为了省字节改掉一条有意的界面行为。
5. `pnpm build` 把 `pkg/gateway/dist/` 改成一大堆新哈希文件：那是既有约定（`make ui` 的产物），
   **不要回滚**，但要按文末"需要你知道"的做法在报告里单列。
6. 你想顺手给 `ListAllMessages(ctx, sid, 0)` 加分页：**不要**（那是另一个计划，见下）。

## Maintenance notes

- 这条 DTO 的取舍原则：**网页画卡需要什么就发什么**。模型面载荷（`Content`）属于 transcript，
  不属于历史列表；将来若有人要在网页显示"模型收到了什么"，应该新开一个显式开关，而不是默认带上。
- 同一族的已知问题（不在本计划）：`pkg/gateway/api_extra.go:1996` 的 `ListAllMessages(ctx, sid, 0)`
  先把整条会话全量载入再按 `limit` 截断——历史接口没有真正的分页。它比本条更影响首次打开长会话，
  但改它要动 DTO 的游标语义，属独立计划。
- `ShellCard` 与 `PartsJSON` 的重复仍在（同一份 stdout）。要真正只发一份，得让 `ShellCardBody.vue`
  从 parts 的 body 里取流文本（需要在前端复刻 `ParseShellCard` 的分段规则）——不划算，除非卡片正文
  开始显著超过 120 KB 的上限。
