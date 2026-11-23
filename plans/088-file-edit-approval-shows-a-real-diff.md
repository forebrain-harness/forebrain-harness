# Plan 088: `edit_file` 审批卡显示引擎算出的真 diff（现在的"diff"是旧文全部 + 新文全部）

> **Executor instructions**: 按步骤执行，每步跑完「Verify」并确认输出符合预期再进入下一步。
> 命中「STOP conditions」任一条就停下来报告，不要即兴改动。全部完成后更新
> `plans/README.md` 里本计划那一行的状态（除非派发你的评审方说他维护索引）。
>
> **Drift check（先跑）**: `git diff --stat 8d3322c..HEAD -- pkg/event/turndiff.go pkg/tool/file_tools.go frontend/src/lib/approvalGates.ts`
> 若有变动，先把「Current state」的摘录与现码对照；对不上按 STOP 处理。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED
- **Depends on**: plans/087-approval-card-facts-read-the-wire.md（两者都改 `frontend/src/lib/approvalGates.ts`，串行执行：先 087）
- **Category**: bug
- **Planned at**: commit `8d3322c`, 2026-10-21

## Why this matters

`edit_file` 的审批卡用红绿底色把一段内容当 diff 呈现，用户可以据此批准一次文件改动；而 web 端那段"diff"其实是
**旧文本的全部行（每条标 -）后面接新文本的全部行（每条标 +）**，行数也按两段文本的行数算。改一个 500 行文件里的一行，
卡片给他 1000 行、只显示前 40 行（正好全是"删除"），另报"新增 500 行、删除 500 行"。终端侧同一张卡用的是
`event.Build` 生成的真 unified diff（`pkg/tui/overlays.go:1865`），所以两端对同一件事说的是两种话。
修完之后：web 卡片显示引擎算出的一处 hunk 与真实增删行数，终端与 web 同源。

## Current state

- `frontend/src/lib/approvalGates.ts:111-123` — 假的 diff：

```ts
/** The diff rows of a replacement with no unified diff to hand: all the old
 * lines, then all the new ones — the same shape the terminal's overlay falls
 * back to. */
function editDiff(oldText: string, newText: string): ['+' | '-', string][] {
  const rows: ['+' | '-', string][] = []
  for (const text of [oldText, newText]) {
    if (text === '') continue
    ...
    for (const line of lines) rows.push([text === oldText ? '-' : '+', line])
  }
  return rows
}
```

  注释里"the same shape the terminal's overlay falls back to"只在 `event.Build` **失败**时成立；
  终端正常情况下用的是真 diff（见下一处）。`file_edit` 的 facts 也用它算行数
  （`frontend/src/lib/approvalGates.ts:264-275`：`added: textLines(newText)`、`removed: textLines(oldText)`）。

- `frontend/src/components/chat/ApprovalGateCard.vue:104-105` — 卡片只画 `facts.diff` 的前 40 行：

```ts
// Forty rows is the most one approval asks anyone to read; past that the diff
// scrolls inside its box instead of growing the card.
const diffRows = computed(() => (gate.value.facts.diff ?? []).slice(0, 40))
```

- `pkg/tui/overlays.go:1863-1871` — 终端侧的正确实现（本计划的基准）：

```go
if in.OldString != in.NewString {
    summary, err := event.Build(filepath.Base(filePath), []byte(in.OldString), []byte(in.NewString))
    if err == nil && summary.UnifiedDiff != "" {
        diffText = summary.UnifiedDiff
    } else {
        diffText = in.OldString + "\n" + in.NewString
    }
}
```

- `pkg/event/turndiff.go:1-35` — `event.Build(path, before, after []byte) (Summary, error)`，
  `Summary{Path, Added, Deleted, UnifiedDiff}`；用 `github.com/pmezard/go-difflib`，`Context: 3`。
  `pkg/tool/workspace.go:12` 已经在同包里这么调用，照抄即可。
- `pkg/tool/file_tools.go:1042-1052` — `edit_file` 审批载荷的构造点（本计划在这里加字段）：

```go
payload := map[string]any{
	"file_path":          in.FilePath,
	"resolved_file_path": abs,
	"old_string":         in.OldString,
	"new_string":         in.NewString,
	"replace_all":        in.ReplaceAll,
}
markProtectedApproval(payload, protected)
id, ok, err := hook(ctx, "edit_file", payload)
```

- 载荷会被 `pkg/state` 的 actions 表原样存成 `payload_json`（`pkg/state/action_service.go:40-50`），
  所以新增字段的**体积要有上限**：卡片只画 40 行，就只送一处 hunk（首个 hunk，正文行 ≤ 40）。
- 已存在的历史 action 回放时没有新字段，前端必须能退回旧形态（见 Step 3）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 单包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/event -count=1` | `ok` |
| Go 相关包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/event ./pkg/tool ./pkg/tui -count=1` | 全 `ok` |
| vet | `go vet ./pkg/event ./pkg/tool` | 无输出 |
| 前端单测 | `cd frontend && pnpm test -- src/lib/approvalGates.test.ts src/components/chat/ApprovalCard.test.ts` | 全 passed |
| 前端全量 | `cd frontend && pnpm test` | 全 passed |
| 类型检查 | `cd frontend && pnpm exec vue-tsc --noEmit` | exit 0 |
| 包图 | `scripts/package-graph.sh` | 无变化（本计划不新增包依赖） |
| e2e | `FOREBRAIN_E2E_SPEC=e2e/approval-families.spec.ts scripts/acceptance/web_e2e.sh` | `web e2e: PASS` |

## Scope

**In scope**（只改这些文件）：
- `pkg/event/turndiff.go`（加一个 `FirstHunk` 助手）
- `pkg/event/turndiff_test.go`（新建或追加，视仓库是否已有同名文件）
- `pkg/tool/file_tools.go`（只改 `edit_file` 的载荷构造）
- `pkg/tool/file_tools_test.go`（加一条载荷断言）
- `frontend/src/lib/approvalGates.ts`
- `frontend/src/lib/approvalGates.test.ts`

**Out of scope**（不要碰）：
- `pkg/tui/overlays.go`：终端已正确，不要"统一"到新字段上（它按需现算，没有体积问题）。
- `apply_patch` 的事实行与 hunk：owner 已在 `plans/README.md` 083 执行记录里裁决为「N 个文件 · +a −b」，
  不要顺手改。
- `write_file` 的 diff 与行数：全新文件本来就是全部 `+`，`existing_lines` 已给出覆盖行数，无需改动。
- 任何载荷里的 `old_string`/`new_string` 原文：它们是工具真实入参，不要为省体积裁掉。
- `frontend/src/components/chat/ApprovalGateCard.vue` 的 40 行上限：保持。

## Git workflow

- 直接改当前 `dev` 工作区；**不 commit、不 push**，除非派发方明确要求。
- 若要求提交：`fix(web): …`，body 用 `Why:` / `What:` / `Verification:`，`git commit -s`。

## Steps

### Step 1: `pkg/event` 提供"首个 hunk"助手

在 `pkg/event/turndiff.go` 追加（保持包内无新依赖）：

```go
// FirstHunk returns the first hunk of a unified diff — its `@@` header plus at
// most maxBodyRows body lines — so a caller that can only show one hunk does not
// ship the whole patch. It returns "" when there is no hunk.
func FirstHunk(unified string, maxBodyRows int) string {
	if maxBodyRows <= 0 {
		return ""
	}
	lines := strings.Split(unified, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "@@") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	out := []string{lines[start]}
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "@@") {
			break
		}
		if len(out)-1 >= maxBodyRows {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
```

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/event -count=1` → `ok`（此时还没有新用例）。

### Step 2: 单测钉住 `FirstHunk`

在 `pkg/event/turndiff_test.go` 加（若该文件已存在则追加；风格参照同文件里 `TestBuild…` 的写法）：

- 一段 2 个 hunk 的 diff ⇒ 只返回第一个（结果里 `@@` 出现 1 次）；
- 正文行超过 `maxBodyRows` ⇒ 正文行数 == `maxBodyRows`；
- 没有 `@@` 的文本（`""`、`"a\nb"`）⇒ 返回 `""`；
- `maxBodyRows <= 0` ⇒ 返回 `""`。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/event -run TestFirstHunk -count=1` → `ok`。

### Step 3: `edit_file` 载荷带上引擎的行数与首个 hunk

`pkg/tool/file_tools.go` 的 `edit_file` 分支（约 :1044），在 `markProtectedApproval` **之前**加：

```go
// The card shows one hunk and the engine's own line counts, so neither
// surface invents a diff (pkg/tui/overlays.go draws the same summary).
if summary, derr := event.Build(filepath.Base(abs), []byte(in.OldString), []byte(in.NewString)); derr == nil {
	payload["added_lines"] = summary.Added
	payload["removed_lines"] = summary.Deleted
	if hunk := event.FirstHunk(summary.UnifiedDiff, 40); hunk != "" {
		payload["unified_diff"] = hunk
	}
}
```

- 需要 `"github.com/forebrain-harness/forebrain-harness/pkg/event"` 与 `path/filepath` 两个 import（同包
  `pkg/tool/workspace.go` 已经这么用，照抄它的 import 写法）。
- 只有 `in.OldString != in.NewString` 时才值得算；相同字符串算出来的 diff 为空，`FirstHunk` 会返回 `""`，
  行数都是 0——可以不特判，但不要因此产出 `added_lines: 0` 之外的怪值。

**Verify**: `go vet ./pkg/tool` → 无输出；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1` → `ok`。

### Step 4: 载荷单测

`pkg/tool/file_tools_test.go` 加一条测试，直接调用 `edit_file` 的审批路径并捕获载荷，断言：

- `old_string: "a\nb\nc"`、`new_string: "a\nB\nc"` ⇒ `added_lines == 1`、`removed_lines == 1`，且
  `unified_diff` 含 `@@`、含一行以 `-b` 开头、一行以 `+B` 开头、以 ` b` 或 `  b` 上下文行形式出现的 `c`；
- 20 行互不相同的新文本 ⇒ `added_lines == 20` 且 `unified_diff` 的正文行数 ≤ 40。

审批路径的现有测试写法参照 `pkg/tool/request_permissions_test.go` 里挂 `st.ActionHook` 的用例
（`grep -n "ActionHook" pkg/tool/file_tools_test.go | head`，若已有就照抄那一段的钩子设置）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run TestEditFileApprovalPayloadCarriesEngineDiff -count=1` → `ok`。

### Step 5: 前端优先用引擎的 hunk 与行数

`frontend/src/lib/approvalGates.ts` 的 `file_edit` 分支改成（保留旧形态的退回，供历史 action 回放）：

```ts
case 'file_edit': {
  const oldText = String(payload.old_string ?? '')
  const newText = String(payload.new_string ?? '')
  const hunk = hunkDiff(String(payload.unified_diff ?? ''))
  const added = Number(payload.added_lines)
  const removed = Number(payload.removed_lines)
  const counts = Number.isFinite(added) && Number.isFinite(removed)
  return {
    family,
    path: String(payload.resolved_file_path ?? payload.file_path ?? ''),
    added: counts ? added : textLines(newText),
    removed: counts ? removed : textLines(oldText),
    facts: [t('chat.approvalFactsLines', { added: counts ? added : textLines(newText), removed: counts ? removed : textLines(oldText) })],
    diff: hunk.length ? hunk : editDiff(oldText, newText),
  }
}
```

新增 `hunkDiff(unified: string)`：按行切分，`@@` 开头的行作为 `['', line]`，`+` 开头（且不是 `+++`）为 `['+', line.slice(1)]`，
`-` 开头（且不是 `---`）为 `['-', line.slice(1)]`，其余为 `['', line]`；空输入返回 `[]`。
`editDiff` 保留但把注释改成事实：**只有**历史 action 不带 `unified_diff` 时才走这条退回路径。
（注意：现有 `patchHunk` 已实现同一套解析给 `apply_patch` 用；若签名能复用就复用它，别写第二份——
`apply_patch` 的 8 行上限与 `edit_file` 的 40 行上限由调用处传参，不要因此改 `apply_patch` 的输出。）

**Verify**: `cd frontend && pnpm exec vue-tsc --noEmit` → exit 0。

### Step 6: 前端用例

`frontend/src/lib/approvalGates.test.ts` 加：

- 带 `unified_diff`（含 `@@`）的 `edit_file` 载荷 ⇒ `facts.diff` 里 `@@` 行是上下文行，且存在 `-old` / `+new`，
  `facts.added === payload.added_lines`；
- 不带 `unified_diff` 的旧载荷 ⇒ 退回现状（有 `-`/`+` 两类行），不抛异常；
- `added_lines: 0, removed_lines: 0` ⇒ 事实行说 0/0（不因 `Number('')` 变成 NaN 文本）。

**Verify**: `cd frontend && pnpm test -- src/lib/approvalGates.test.ts` → 全 passed。

### Step 7: 全量回归 + 反向验证

**Verify**:
1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/event ./pkg/tool ./pkg/tui -count=1` → 全 `ok`。
2. `go vet ./pkg/event ./pkg/tool` → 无输出。
3. `cd frontend && pnpm test` → 全 passed。
4. `cd frontend && pnpm exec vue-tsc --noEmit` → exit 0。
5. `scripts/package-graph.sh` → 无 diff（若报 graph 变化，说明 import 方向动错了，按 STOP 处理）。
6. 反向验证：临时把 Step 5 的 `hunk.length ? hunk : editDiff(...)` 改成只用 `editDiff(...)`，
   跑 `pnpm test -- src/lib/approvalGates.test.ts`，确认新用例变红，再改回。
7. `FOREBRAIN_E2E_SPEC=e2e/approval-families.spec.ts scripts/acceptance/web_e2e.sh` → `web e2e: PASS`。

## Test plan

- Go：`pkg/event/turndiff_test.go`（`FirstHunk` 四例）+ `pkg/tool/file_tools_test.go`（载荷两例）。
- 前端：`approvalGates.test.ts` 三例（Step 6）。
- 结构参照：`pkg/event/turndiff_test.go` 里既有的 `event.Build` 用例；前端参照 `describe('approvalFacts')` 的 table-driven 写法。
- 反向验证见 Step 7.6（必须做：证明用例真能抓住"假 diff"）。

## Done criteria

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/event ./pkg/tool ./pkg/tui -count=1` 全 `ok`
- [ ] `cd frontend && pnpm test` exit 0；`pnpm exec vue-tsc --noEmit` exit 0
- [ ] `grep -n "FirstHunk" pkg/event/turndiff.go pkg/tool/file_tools.go` → 两处各命中
- [ ] `grep -n "unified_diff" pkg/tool/file_tools.go frontend/src/lib/approvalGates.ts` → 两侧各命中
- [ ] `scripts/package-graph.sh` 无输出/无 diff
- [ ] `git status --porcelain` 只含 Scope 列出的文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

- `event.Build` 的签名或 `Summary` 字段与摘录不同（说明代码已演进）。
- 载荷体积评估明显变大（例如 `unified_diff` 超过约 6 KiB）：说明 `FirstHunk` 的上限没生效，先修上限，不要靠截断字符串。
- `pkg/tool` 里存在对 `edit_file` 载荷做严格解码（`DisallowUnknownFields`）的路径，新字段会让批准回放报错——
  停下来报告，不要加兼容层。
- e2e 的 `approval-families.spec.ts` 依赖旧 diff 形态而变红，且改断言会**放宽**它（例如把"含 `+`/`-` 行"
  改成"含任意行"）——这种情况下停下来报告，不要放宽断言。
- 任一 Verify 连试两次仍失败。

## Maintenance notes

- **diff 的真相在引擎**：以后要在 web 上画更多 diff（例如 `apply_patch` 的整段），扩 `unified_diff` 的载荷字段与
  `event.FirstHunk` 的行数上限，不要在 TS 里写第二套 diff 算法。
- 载荷多出的一处 hunk 会进 `fb_actions.payload_json`（历史命令回放会重放它）：卡片只画 40 行，所以上限必须保持在 40 行左右，
  别为了"完整"把整个 patch 塞进去。
- 评审要点：历史 action（无新字段）走退回路径时不得抛异常；`added_lines`/`removed_lines` 是引擎计数（不是文本行数）；
  `+++`/`---` 头行不能被当成 `+`/`-` 正文。
- 本计划**未**处理：`facts.path` 至今没有任何渲染点（文件族卡片不写文件名）——若 owner 要求卡片显示文件名，
  单独开工，别在本计划里顺手加。
