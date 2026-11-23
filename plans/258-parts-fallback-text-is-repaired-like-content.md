# Plan 258: parts 的兜底文本块与 content 列按同一种方式修复

> **Executor instructions**: 按步骤顺序执行，每步的验证命令通过后再做下一步。遇到
> 「STOP conditions」里的情况就停下来报告，不要自己发挥。**不要 commit / push / merge**：
> 改动留在工作区里，做完报告（仓库主人手动提交）。做完后把 `plans/README.md` 里本计划那一行
> 的状态改成 DONE，并附一句执行摘要——除非派你来的评审说索引由他维护。
>
> **Drift check (run first)**:
> `git diff --stat 236fc32a..HEAD -- pkg/state/usage.go pkg/state/usage_test.go`
> → 必须为空。不为空就把下面「Current state」里的摘录和实际代码逐段对照，对不上即 STOP。

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW（一行修复；文本合法时返回原字符串，不分配新内存，干净数据逐字节不变）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `236fc32a`, 2026-11-06
- **Finding**: 2026-11-06 分支审计（238–245 实现细节），发现 6（`introduced`：plan 242 的统一漏了一个分支）

## Why this matters

plan 242 要求消息正文（`content` 列）与结构化片段（`parts` 列）用**同一种**方式修复非法字节：
`dbx.ValidText` 把一串连续的非法字节**折叠**成一个 U+FFFD。如果文本没修复就交给 `json.Marshal`，
Go 会把**每个**非法字节各换成一个 U+FFFD。

242 在 `contentBlocksJSON` 的 text 分支修复了，但同一函数末尾的**兜底分支**（parts 里没有任何可存的块时，
用 `fallbackText` 生成一个 text 块）仍把原文交给 `json.Marshal`。走这个分支的调用方：

- `pkg/state/compact_parts.go:57,60,64`（`PartsJSONWithCompactPart`）：压缩边界行；
- `pkg/migrate/parse.go:535`：从 codex 会话导入的边界行，`detail` 来自外部文件，可能带非法字节。

后果：同一行的 content 显示 `a�b`，parts 显示 `a��b`，重载后模型拿到的与 content 不一致。影响很小，
但这正是 242 要消除的那种不一致。owner 的规则是同类问题全部修复。

## Current state

`pkg/state/usage.go:117-171`，`contentBlocksJSON`。text 分支已修复：

```go
		case llm.ContentTypeText:
			if strings.TrimSpace(part.Text) != "" {
				blocks = append(blocks, map[string]any{
					"type": "text",
					"text": dbx.ValidText(part.Text),
				})
			}
```

兜底分支（`:163-170`）**未修复**：

```go
	if len(blocks) == 0 {
		if text := strings.TrimSpace(fallbackText); text != "" {
			blocks = append(blocks, map[string]any{
				"type": "text",
				"text": fallbackText,
			})
		}
	}
	return blocks
}
```

`dbx.ValidText`（`pkg/state/dbx/text.go:37-42`）：文本合法且不含 NUL 时原样返回；否则先
`strings.ToValidUTF8(s, ReplacementText)` 再把 NUL 换成 `ReplacementText`（`"�"`）。

`usage.go` 已经 import 了 `pkg/state/dbx`（242 加的）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Focused | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'TestContentPartsFallbackIsRepairedLikeContent' -count=1 -v` | `PASS` |
| Package | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/migrate -count=1` | `ok` |
| House rule | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |

## Scope

**In scope**:
- `pkg/state/usage.go`：兜底分支一行
- `pkg/state/usage_test.go`：一个新测试

**Out of scope**:
- `dbx.ValidText`/`ValidJSONText`。
- `compact_parts.go`、`migrate/parse.go` 的调用方。修复在被调函数里做，调用方不用动。
- `insertMessage` 的 choke point。它对 parts 做的是 `ValidJSONText`，处理 NUL 转义，与这里不重复。

## Steps

### Step 1: 兜底分支同样修复

```go
	if len(blocks) == 0 {
		if text := strings.TrimSpace(fallbackText); text != "" {
			blocks = append(blocks, map[string]any{
				"type": "text",
				// Repaired the way the content column is (plan 242): left to
				// json.Marshal, every invalid byte would become its own U+FFFD
				// while the content row folds each run into one.
				"text": dbx.ValidText(fallbackText),
			})
		}
	}
```

**Verify**: `go build ./...` → exit 0

### Step 2: 测试

在 `pkg/state/usage_test.go` 末尾加：

```go
// TestContentPartsFallbackIsRepairedLikeContent pins plan 258: a row whose parts
// come from the fallback text (a compaction or an imported boundary) carries
// the same repaired text the content column does — one U+FFFD per run of
// invalid bytes, not one per byte.
func TestContentPartsFallbackIsRepairedLikeContent(t *testing.T) {
	raw := "a\xff\xfeb"
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(ContentPartsJSON(nil, raw)), &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %#v, want the one fallback text block", blocks)
	}
	got, _ := blocks[0]["text"].(string)
	if want := dbx.ValidText(raw); got != want {
		t.Fatalf("fallback text = %q, want %q (the content column's repair)", got, want)
	}
	clean := "plain text 中文"
	if err := json.Unmarshal([]byte(ContentPartsJSON(nil, clean)), &blocks); err != nil {
		t.Fatal(err)
	}
	if got, _ := blocks[0]["text"].(string); got != clean {
		t.Fatalf("clean fallback = %q, want it unchanged", got)
	}
}
```

文件缺 `encoding/json` 或 `pkg/state/dbx` 的 import 就补上。

**Verify**: `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'TestContentPartsFallbackIsRepairedLikeContent' -count=1 -v` → `PASS`

**负对照**：临时把 step 1 改回 `"text": fallbackText`，同一命令应 FAIL，`got` 为 `"a��b"`。观察后复原。

### Step 3: 回归

**Verify**：
- `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/migrate -count=1` → `ok`
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`

## Test plan

- 新增 1 个测试，带负对照。
- 保持绿：`pkg/state` 全包（尤其 242 并入 `message_sync_test.go` 的 6 条修复用例），以及 `pkg/migrate` 的 codex 导入用例。

## Done criteria

ALL must hold:

- [ ] `go build ./...`、`go vet ./...` exit 0
- [ ] `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/migrate -count=1` → `ok`
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` → `ok`
- [ ] `grep -n '"text": fallbackText' pkg/state/usage.go` → 无输出
- [ ] 负对照已观察到红并复原
- [ ] `git status --porcelain` 只有 `pkg/state/usage.go`、`pkg/state/usage_test.go`、`plans/README.md`
- [ ] `plans/README.md` 的 258 行已更新

## STOP conditions

- 某个现有测试断言兜底块的文本**逐字节等于**一个含非法字节的输入：报告该测试，不要改它的断言。这说明有人依赖未修复的原文。

## Maintenance notes

- 规则延续 242：**写进任何文本列的 JSON 里，凡是要和 content 对照的文本，都先过 `dbx.ValidText` 再 `json.Marshal`**。`contentBlocksJSON` 以后新增文本类块，比如 reasoning，也照此处理。
