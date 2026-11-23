# Plan 337: web 端 list_usable_models 分组专属卡（方案 A·chip·本地化空态）

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **原型闸门（先核）**: 设计原型已经 owner 视觉确认并冻结。实施前第一步核对
> 哈希（Step 0）；不符即 STOP，不得按记忆改原型或绕过确认。
>
> **Drift check (run first)**: baseline commit `9a3fba8c`（分支
> `execute-331-333` 尖；本计划与 334–336 零文件交集，基于主树 HEAD）. Run:
> ```bash
> git diff --stat 9a3fba8c..HEAD -- pkg/tool/format.go pkg/gateway/wsevents.go frontend/src/components/chat/ToolCallCard.vue frontend/src/composables/useChatStream.ts
> ```
> On a **content** mismatch against the excerpts below, STOP (moved line
> numbers alone are not drift).

## Status

- **Priority**: P3
- **Effort**: M
- **Risk**: LOW（纯展示层；引擎 LLM 面 JSON 契约不动）
- **Depends on**: none（与 334–336 零交集，可并行）；owner 原型确认已齐（见下）
- **Category**: direction / UX（2026-11-20 `improve branch` 审计 D2，owner 选定；设计稿已定稿转实施）
- **Planned at**: commit `9a3fba8c`, 2026-11-20（设计定稿同日）

## Why this matters

plan 332 给 TUI 的 `list_usable_models` 卡做了 owner 确认过的分组 Markdown
渲染（provider 标题＋每模型一行 label/档位/默认档，**id 绝不向用户显示**）。
web 端没有专属卡：roster 经 `tool.ReadableToolAnswer` 读成扁平行
（`glm-5.3 (zhipuai) — thinking: low, medium, high (default medium)`），
由通用 `ToolAnswerBody` 渲染。两端体验不对称。web 已有专属 roster 卡先例
——`PeerRosterCard`（peer_list）。本计划照该范式做 `UsableModelsCard`。

## 设计裁决（owner 2026-11-20，user_interaction 已回＋视觉确认）

- **Q1＝方案 A**：结构化 `UsableModelsCard.vue`，照 PeerRosterCard 范式；
  引擎侧给 `ReadableAnswer` 加展示侧 `Models []ReadableModel`，gateway 透传。
- **Q2＝id 零显示**：owner TUI 裁决（「id 只是发给 LLM 的派发参数，对用户
  无意义」）延伸到 web。**ReadableModel DTO 本身不含 id 字段**（最强保证）。
- **Q3＝chip（A2）**：档位做成 chip，默认档 brand 高亮＋「默认」标。
- **Q4＝本地化空态**：空 roster 显示本地化空态句＋引导（照 PeerRosterCard
  的 `chat.peerRosterEmpty` 先例），不显示 engine note 原句、不显示空骨架。
- **原型文件**：`docs/plan/usable-models-web-card-prototype.html`
- **冻结哈希（sha256）**：`90c1d241095da4a4133cb46f4ba74688a1cc67980edbee100f33219e672b559c`
  （owner 确认冻结；实施前 shasum 核对，视觉以 A2 帧为准）。

## Current state（已核实事实，基线 `9a3fba8c`）

### 1. 引擎 LLM 面 JSON 契约（不动）

`pkg/tool/format.go:4301-4319`：

```go
type usableModelsAnswer struct {
	Models []usableModelRow `json:"models"`
	Note   string           `json:"note"`
}
type usableModelRow struct {
	ID            string   `json:"id"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Efforts       []string `json:"efforts"`
	DefaultEffort string   `json:"default_effort"`
	Current       bool     `json:"current"`
	HandedOff     bool     `json:"handed_off"`
}
```

`ReadableToolAnswer` 分发（:4256）：`case name == "list_usable_models": return readUsableModels(body)`。

### 2. 展示侧 ReadableAnswer（`pkg/tool/format.go:4012-4024`）

```go
type ReadableAnswer struct {
	Rows []ReadableRow `json:"rows,omitempty"`
	Text string        `json:"text,omitempty"`
	Peers []ReadablePeer `json:"peers,omitempty"`
	Note string        `json:"note,omitempty"`
	Send *ReadablePeerSend `json:"send,omitempty"`
}
```

`readUsableModels`（:4327-4356）今天只填 `Text`（扁平行）＋`Note`：

```go
		var lines []string
		for _, row := range raw.Models {
			line := fmt.Sprintf("%s (%s)", strings.TrimSpace(row.Model), strings.TrimSpace(row.Provider))
			if len(row.Efforts) > 0 {
				line += " — thinking: " + strings.Join(row.Efforts, ", ")
				if strings.TrimSpace(row.DefaultEffort) != "" {
					line += " (default " + strings.TrimSpace(row.DefaultEffort) + ")"
				}
			}
			if row.Current { line += " (current)" }
			if row.HandedOff { line += " (handed off)" }
			lines = append(lines, line)
		}
		return ReadableAnswer{Text: strings.Join(lines, "\n"), Note: strings.TrimSpace(raw.Note)}, true
```

TUI 分组基准 `FormatUsableModelsMarkdown`（:4364，**不改**）：`**provider**`
标题（首见序）＋`- label — thinking: … (default …) *(current)* *(handed off)*`。

### 3. gateway 透传＋空判定（`pkg/gateway/wsevents.go:979-997`）

```go
func readableAnswerFor(toolName, body string, input map[string]any) *tool.ReadableAnswer {
	answer, ok := tool.ReadableToolAnswer(toolName, body, input)
	// A roster with nobody in it is still the card: its note is the answer.
	if !ok || (len(answer.Rows) == 0 && len(answer.Peers) == 0 && answer.Send == nil &&
		strings.TrimSpace(answer.Text) == "" && strings.TrimSpace(answer.Note) == "") {
		return nil
	}
	...
	return &answer
}
```

下发：`fields["readable_answer"] = answer`（:1052）。wire 是 snake_case JSON，
前端经 `toCamelCase`（`frontend/src/lib/case.ts`，camelcase-keys）转 camelCase。

### 4. 前端 TS DTO（`frontend/src/composables/useChatStream.ts:824-833`）

```ts
export interface ReadableAnswer {
  rows: ReadableRow[]
  text?: string
  peers?: ReadablePeer[]
  note?: string
  send?: ReadablePeerSend
}
```

`readableAnswerFrom`（:887）解析 camelCase raw（peers 段 :906 起是范式），
调用点 :1029（live completion）/:1104（stored row）。空判定尾段：
`if (rows.length === 0 && !text && peers.length === 0 && !note && !send) return undefined`。

### 5. 前端卡片接线（`frontend/src/components/chat/ToolCallCard.vue`）

peerRoster computed（:488-492）＋分支（:129-137）：

```ts
const peerRoster = computed(() => {
  const answer = props.step.readableAnswer
  return Boolean(answer && ((answer.peers?.length ?? 0) > 0 || answer.note))
})
```

```html
      <PeerRosterCard
        v-else-if="peerRoster && !step.error"
        :answer="step.readableAnswer!"
      />
      <PeerSendCard
        v-else-if="peerSend && !step.error"
        :send="step.readableAnswer!.send!"
      />
      <ToolAnswerBody
        v-else-if="step.readableAnswer && !step.error"
        :answer="step.readableAnswer"
      />
```

`UsableModelsCard` 分支插在 PeerSendCard 之后、ToolAnswerBody 之前。

### 6. 组件范式（`frontend/src/components/chat/PeerRosterCard.vue`）

props `{ answer: ReadableAnswer }`；`data-testid`；scoped style；卡片 body
`padding:9px 11px; font-size:13px; line-height:1.6; max-height:360px; overflow:auto`；
空态 `<p class="empty">{{ t('chat.peerRosterEmpty') }}</p>`。产品 token 见
`frontend/src/assets/main.css`（`--forebrain-*`）。

### 7. locales（`frontend/src/locales/index.ts`）

zh 段（:1779 起）/en 段（:4337 起）双语键，例：
`'chat.peerRosterEmpty': '没有可以联系的代理。'` / `'No agents can be messaged.'`。
卡头动词 zh 已存在：`'Listed usable models': '已列出可用模型'`
（`ai-elements-vue/packages/elements/src/tool/headerWords.ts:114`，sibling repo
在 harness 根 `ai-elements-vue/`，**不在本计划 scope**）。

### 8. vitest 范式（`frontend/src/components/chat/PeerRosterCard.test.ts`）

`mount(Component,{props:{answer}})`＋`beforeAll(()=>setLocale('zh'))`＋
`data-testid` 断言。

### 仓库约定（记忆裁决，必须遵守）

- 聊天卡片照 CardComponent 统一体系；专属卡照 PeerRosterCard 接线
  （computed＋v-else-if＋独立组件）；组件用 shadcn-vue 原生体系。
- 无页面标题层、少嵌套；新文案双语键。
- **no dashed markers**：原型已按产品样式画（冻结稿），实施 1:1 还原 A2 帧。

## Commands you will need

Go（每个新 shell 先）：

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
export FOREBRAIN_HOME="$(mktemp -d)"
```

| Purpose | Command | Expected |
|---------|---------|----------|
| 原型哈希 | `shasum -a 256 docs/plan/usable-models-web-card-prototype.html` | `90c1d241…b559c` |
| Go 聚焦 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestReadableToolAnswer -count=1 -v` | 先红后绿 |
| Go 包级 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/gateway ./pkg/architecture -count=1` | 三包 ok |
| Go lint/vet | `gofmt -l pkg/tool pkg/gateway`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool ./pkg/gateway` | 零输出／exit 0 |
| 前端 | `cd frontend && pnpm typecheck && pnpm test && pnpm build` | 全绿 |
| 真机 web | `scripts/acceptance/web_e2e.sh`（见 Step 8） | 卡片截图含分组/chip/无 id |

**前端环境前置**：fresh worktree 不含 gitignored 的 `ai-elements-vue/`
（pnpm workspace sibling，在 harness 根）与 `frontend/node_modules`。开工前
从主检出复制 `ai-elements-vue/` 到 worktree 根，并 `cd frontend && pnpm install`
（主树 `pnpm typecheck` 已验证 exit 0，工具链可用：node 22 / pnpm 10）。
若 sibling 缺失或 install 失败，STOP 报告，勿改 pnpm-workspace.yaml。

## Scope

**In scope**（只许改这些）:
- `pkg/tool/format.go`、`pkg/tool/format_test.go`
- `pkg/gateway/wsevents.go`、`pkg/gateway/wsevents_test.go`
- `frontend/src/composables/useChatStream.ts`（＋其既有测试文件，若有）
- `frontend/src/components/chat/UsableModelsCard.vue`（新）
- `frontend/src/components/chat/UsableModelsCard.test.ts`（新）
- `frontend/src/components/chat/ToolCallCard.vue`、`frontend/src/components/chat/ToolCallCard.test.ts`
- `frontend/src/locales/index.ts`

**Out of scope**（禁止触碰）:
- `list_usable_models` 引擎 LLM 面 JSON 契约（`usableModelsAnswer`/
  `usableModelRow`/`listUsableModels`）——331 owner 裁决，只能加展示侧 DTO。
- `pkg/run/subagent.go`（334–336 触面）、TUI 渲染（332 已定稿）、
  `FormatUsableModelsMarkdown`、`PeerRosterCard.vue` 本身、
  `ai-elements-vue/` sibling repo、原型 HTML（已冻结）。

## Git workflow

- worktree `.worktrees/337-web-usable-models-card`，分支
  `337-web-usable-models-card`，基于主树 HEAD `9a3fba8c`（与 334–336 零交集）。
- 每步一个 commit，消息照分支风格（例：
  `feat(tool): readable usable-models roster for the web card`）。
- 不 push、不合并、不动主树 checkout。

## Steps

### Step 0 — 原型哈希闸门

**Verify**: `shasum -a 256 docs/plan/usable-models-web-card-prototype.html`
→ `90c1d241095da4a4133cb46f4ba74688a1cc67980edbee100f33219e672b559c`。
不符 → STOP（原型被改，需 owner 重新确认）。打开原型，视觉以 **A2 帧**为准。

### Step 1 — 引擎展示侧 DTO＋reader（Go，先红后绿）

1. `pkg/tool/format.go` `ReadableAnswer`（:4012）`Send` 字段后加：

```go
	// Models is a list_usable_models answer's roster, in the answer's own
	// order, grouped by provider on the surface. The row id is the LLM's
	// dispatch argument, never a person's reading, so it is not here at all
	// (owner 2026-11-20, plan 337).
	Models []ReadableModel `json:"models,omitempty"`
```

2. 加 `ReadableModel`（照 `ReadablePeer` 注释风格，**无 ID**）：

```go
// ReadableModel is one row of a list_usable_models roster, as a card shows it:
// the label a person reads, its provider, the thinking levels it can run at and
// the one it runs at by default, and the two flags naming the conversation's
// current row and an approved hand-off's row. The wire id is deliberately
// absent — a surface never shows it (owner 2026-11-20).
type ReadableModel struct {
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Efforts       []string `json:"efforts,omitempty"`
	DefaultEffort string   `json:"default_effort,omitempty"`
	Current       bool     `json:"current,omitempty"`
	HandedOff     bool     `json:"handed_off,omitempty"`
}
```

3. `readUsableModels`（:4327）在填 Text 的同时填 Models（保留 Text 作旧回放
兜底）：循环里 append `ReadableModel{Provider: strings.TrimSpace(row.Provider),
Model: strings.TrimSpace(row.Model), Efforts: row.Efforts,
DefaultEffort: strings.TrimSpace(row.DefaultEffort), Current: row.Current,
HandedOff: row.HandedOff}`；返回 `ReadableAnswer{Text: …, Note: …, Models: models}`。

4. 测试 `pkg/tool/format_test.go`（照 `TestReadableToolAnswerReadsThePeerTools`
:290）：`ReadableToolAnswer("list_usable_models", body, nil)` 断言
`answer.Models` 逐行 provider/model/efforts/defaultEffort/current/handedOff，
**断言任何字段都不含 id 值**（leak 清单）；空 roster（`{"models":[],"note":"…"}`）
→ Models 空、Note 在；旧格式兼容（Text 仍填）。

**Verify**: Go 聚焦命令 → 先红（Models 字段不存在）后绿。

### Step 2 — gateway 空判定认 Models（Go）

`pkg/gateway/wsevents.go:981-984` 空判定加 `len(answer.Models) == 0 &&`：

```go
	if !ok || (len(answer.Rows) == 0 && len(answer.Peers) == 0 && len(answer.Models) == 0 && answer.Send == nil &&
		strings.TrimSpace(answer.Text) == "" && strings.TrimSpace(answer.Note) == "") {
		return nil
	}
```

测试 `pkg/gateway/wsevents_test.go`：一个只有 Models 的 roster answer 不被
空判定丢弃（照既有 readableAnswerFor 测试范式）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → ok。

### Step 3 — 前端 TS DTO＋解析

`frontend/src/composables/useChatStream.ts`：

1. `ReadableAnswer`（:824）加 `models?: ReadableModel[]`。
2. 加接口（camelCase，无 id）：

```ts
/** One row of a list_usable_models roster (pkg/tool ReadableModel). The wire
 * id is absent on purpose — a surface never shows it (owner 2026-11-20). */
export interface ReadableModel {
  provider: string
  model: string
  efforts?: string[]
  defaultEffort?: string
  current?: boolean
  handedOff?: boolean
}
```

3. `readableAnswerFrom`（:887）照 peers 段（:906）加 models 解析：
`const models = Array.isArray(answer.models) ? answer.models.flatMap(...provider/model 非空才收，efforts/defaultEffort/current/handedOff 选择性带上...) : []`；
空判定尾段加 `&& models.length === 0`；返回对象加 `...(models.length ? { models } : {})`。

**Verify**: `cd frontend && pnpm typecheck` → exit 0。

### Step 4 — UsableModelsCard.vue（新，1:1 还原 A2）

`frontend/src/components/chat/UsableModelsCard.vue`，props `{ answer: ReadableAnswer }`，
照 PeerRosterCard 范式。视觉**严格照冻结原型 A2 帧**：

- 按 provider 首见序分组（computed groups）；每组 `.prov` 标题
  （12px/600/text-2）＋模型行。
- 模型行 `.l1`：`.nm`（13px/550/text）＋flags（current＝brand-soft pill
  「当前」；handedOff＝outline pill「交接」）。
- `.l2.chips`：每档位一个 `.chip`（mono/11px/bg-alt/divider border）；
  默认档 `.chip.def`（brand-soft/brand-1）＋`.dtag`「默认」。无 efforts 不画 l2。
- 空态（models 空）：`<p class="empty">{{ t('chat.usableModelsEmpty') }}<span class="hint">{{ t('chat.usableModelsEmptyHint') }}</span></p>`。
- body 同 PeerRosterCard：`padding:9px 11px;font-size:13px;line-height:1.6;max-height:360px;overflow:auto`。
- `data-testid`：`usable-models`/`usable-models-group`/`usable-models-row`/`usable-models-empty`。
- **id 零渲染**：组件不读任何 id（DTO 也没有）。

测试 `UsableModelsCard.test.ts`（照 PeerRosterCard.test.ts）：
分组顺序、chip 文本、默认档高亮（`.chip.def`）、current/handedOff pill、
无档位行无 l2、空态本地化句、**id 零泄露**（给 answer 塞一个含 id 的 raw 也
不渲染——DTO 无 id，断言 wrapper.text() 不含 id 值）、zh/en 双语。

**Verify**: `cd frontend && pnpm test -- UsableModelsCard` → 绿。

### Step 5 — ToolCallCard 接线

`frontend/src/components/chat/ToolCallCard.vue`：
1. import UsableModelsCard。
2. computed（照 peerRoster :488）：

```ts
/** A list_usable_models answer draws the roster; an empty roster still draws
 * its own localized empty state, which is why the note counts. */
const usableModels = computed(() => {
  const answer = props.step.readableAnswer
  return Boolean(answer && ((answer.models?.length ?? 0) > 0 || answer.note))
})
```

3. 分支插在 PeerSendCard 之后、ToolAnswerBody 之前：

```html
      <UsableModelsCard
        v-else-if="usableModels && !step.error"
        :answer="step.readableAnswer!"
      />
```

测试 `ToolCallCard.test.ts`：一个 models roster step 选中 UsableModelsCard
（照既有 peerRoster 分支测试）；旧 Text-only step 仍走 ToolAnswerBody（回放兼容）。

**Verify**: `cd frontend && pnpm test -- ToolCallCard` → 绿。

### Step 6 — locales 双语键

`frontend/src/locales/index.ts` zh 段＋en 段加：

| key | zh | en |
|-----|----|----|
| `chat.usableModelsCurrent` | 当前 | current |
| `chat.usableModelsHandedOff` | 交接 | handed off |
| `chat.usableModelsDefault` | 默认 | default |
| `chat.usableModelsEmpty` | 没有可派发给子代理的模型。 | No model can be dispatched to a subagent. |
| `chat.usableModelsEmptyHint` | 省略 model 参数即可使用会话当前模型。 | Omit model to run on the conversation's model. |

（A2 帧档位 chip 无「思考」前缀，故不需 thinking 键；卡头动词
「已列出可用模型」已存在于 sibling headerWords，不动。）

**Verify**: `cd frontend && pnpm typecheck` → exit 0（键齐全）。

### Step 7 — 全门禁

**Verify**（逐条）:
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/gateway ./pkg/architecture -count=1` → 三包 ok
- `gofmt -l pkg/tool pkg/gateway` → 零输出；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/tool ./pkg/gateway` → exit 0
- `cd frontend && pnpm typecheck && pnpm test && pnpm build` → 全绿
- `git status --short` → 只有 in-scope 文件

### Step 8 — 真机 web 验收

起 gateway 跑一个 `list_usable_models` 调用，截图确认卡片：分组标题、
chip＋默认高亮、current/交接 pill、**无 id**、空 roster 本地化句。
配方见 `scripts/acceptance/web_e2e.sh`（记忆：`FOREBRAIN_GATEWAY_TOKEN`、
`FOREBRAIN_YOLO=1`、`FOREBRAIN_E2E_SPEC`；fake-provider harness 可驱动工具卡）。
截图连同绝对路径回报。若本环境无法起真机 web，STOP 报告该步未跑（勿伪造），
组件测试＋typecheck 仍是硬门禁；真机验收由 owner 补跑。

## Test plan

- Go：`format_test.go` Models 解析＋id leak 清单＋空 roster＋旧格式兼容；
  `wsevents_test.go` 空判定认 Models。
- 前端：`UsableModelsCard.test.ts`（分组/chip/默认/flags/空态/id 零泄露/双语）、
  `ToolCallCard.test.ts`（分支选择＋回放兼容）、`readableAnswerFrom` models 解析。
- 既有测试不得改断言迁就实现；PeerRosterCard/peer 卡测试原样通过。
- 回归集：Go `./pkg/tool ./pkg/gateway ./pkg/architecture`；前端全量 vitest。

## Done criteria

ALL must hold:

- [ ] Step 0 原型哈希核对相符（`90c1d241…b559c`）
- [ ] Go 三包 ok；gofmt 零输出；vet exit 0
- [ ] `cd frontend && pnpm typecheck && pnpm test && pnpm build` 全绿
- [ ] `grep -n 'ReadableModel' pkg/tool/format.go` ≥2 处（类型＋字段）；
      `grep -rn 'id' pkg/tool/format.go` 的 ReadableModel 块无 ID 字段
- [ ] `grep -n 'usableModels' frontend/src/components/chat/ToolCallCard.vue` ≥2 处
- [ ] `grep -c 'chat.usableModels' frontend/src/locales/index.ts` ≥10（zh+en 各 5）
- [ ] `git diff --name-only 9a3fba8c..HEAD` 只含 in-scope 文件
- [ ] 真机 web 截图（或 STOP 留痕该步未跑）
- [ ] `plans/README.md` 状态行更新（除非评审方维护索引）

## STOP conditions

Stop and report back (do not improvise) if:

- 原型哈希不符（被改）——需 owner 重新视觉确认，不得自行冻结。
- Current state 摘录与活体代码内容不符（漂移）。
- 发现 web 已有 usable-models 专属卡（事实漂移）——报告，不重复造。
- 方案 A 需要改引擎 LLM 面 JSON 契约才能拿到数据——STOP，契约是 331 owner
  裁决，只能加展示侧 DTO。
- 前端 sibling `ai-elements-vue/` 缺失或 pnpm install 失败——报告，勿改
  pnpm-workspace.yaml；真机 web 无法跑——报告该步未跑，勿伪造截图。
- 同一验证失败两次且无明确诊断。

## Maintenance notes

- 评审重点：**id 零泄露**（DTO 无 id＋组件断言，照 TUI leak 清单）；旧会话
  回放兼容（Text-only 走 ToolAnswerBody）；空 roster 本地化句；locales 双语齐全；
  视觉 1:1 冻结原型 A2（chip＋默认高亮＋flags）。
- 与 332 关系：TUI 卡是设计基准；若 owner 将来改 TUI 形态，web 卡同步评审
  （两端同源原则）。原型重冻结必须经 owner 确认（记忆红线，不得自行冻结）。
- 与 334–336 零交集，可并行派发；本计划不触 pkg/run。
- 数据流：引擎 JSON（不动）→ ReadableAnswer.Models（展示 DTO，无 id）→
  gateway 透传（空判定认 Models）→ toCamelCase → readableAnswerFrom →
  UsableModelsCard。新增第四种 roster 卡时照此链。