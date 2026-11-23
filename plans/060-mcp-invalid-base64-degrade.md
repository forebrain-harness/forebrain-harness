# 060：MCP 图片块无效 base64 的降级——垃圾数据绝不作为文本到达模型

> **执行者须知（零上下文）**：本计划自包含。逐步执行，每步先跑"验证"确认预期再进入下一步。
> 命中 STOP condition 就停下报告。**不建 worktree、不 commit**。
>
> **Drift check（先做）**：本计划写于 `git rev-parse --short HEAD` = `fa4af8a`（2026-10-16）。
> 执行前核对 `pkg/mcp/result_processing.go` 与下文摘录一致；对不上按 STOP 处理。

## Status

- **Priority**: P2（需要不合规/故障 MCP server 触发；但一旦触发，违反的是分支核心保证）
- **Effort**: S
- **Risk**: LOW（只改 decode 失败分支；成功路径零改动）
- **Depends on**: none（与 057/058 无文件冲突的执行顺序随意；建议排在它们之后减少 rebase 面积）
- **Category**: Correctness（审计发现 #8）
- **Planned at**: commit `fa4af8a`，2026-10-16

## Why this matters

`extractResultImageParts` 的注释承诺（result_processing.go:96-98）："a block's data … must never
reach the model as text"。但 decode 失败走的是**裸 `continue`**：

```go
// pkg/mcp/result_processing.go:143-146
decoded, err := base64.StdEncoding.DecodeString(data)
if err != nil {
	continue                       // ← 块原样保留：type=image、data=<垃圾>，changed 不置位
}
```

后果链（全部已亲核）：该块未被替换 → `changed` 为 false（若无其它块改动）→ `:199-201`
`if !changed { return raw, nil, nil, nil }` 返回**原始 JSON**（base64 仍在）→ 即便其它块改了、
重打包的结果里这个 image 块也原样带着 `data` → `:236-237` 附近的 `resultJSONContainsImage`
发现结果 JSON 里还有 image 块 → 走**截断**而不是落盘——模型收到一段携带垃圾 base64 的截断
JSON 文本。一个发坏 base64 的 MCP server 就打穿了"pixels go to the model, text stays clean"的
设计。资源（resource/blob）路径有同构的双胞胎（`:345-348` 附近，`continue` 是既有代码）。

## Current state（摘自本次亲自阅读的代码）

- `:135-142` oversize 检查（`decodedSizeWithin`）失败时的**正确先例**：替换为
  `oversizeBlockLine(data)` 文本行 + `changed = true`——本计划照此风格。
- `:143-146` decode 失败：裸 `continue`（缺陷）。
- `:152-159` persist 失败：替换为 "could not be saved: …" 行 + `changed = true`（先例二）。
- `:176-196` wantParts 门的降级："saved but not sent"（先例三）。
- 资源路径 `:330-350` 附近：oversize 守卫（新引入）之后有同样的 decode `continue`（执行时
  精确定位；该 `continue` 早于本分支存在）。

## 修复设计

### Step 1：工具结果路径

`pkg/mcp/result_processing.go` decode 失败分支替换 `continue`：

```go
decoded, err := base64.StdEncoding.DecodeString(data)
if err != nil {
	// The block cannot be decoded, so it can be neither persisted nor sent.
	// Degrade to the same kind of text line the other failure paths leave,
	// so the envelope never carries the block's raw data as text (the
	// truncated-JSON path resultJSONContainsImage forces would).
	content[i] = map[string]any{
		"type": "text",
		"text": fmt.Sprintf("Binary content (%s, %d characters) could not be base64-decoded and was not sent.", fallbackUnknown(mimeTypeOf(m)), len(data)),
	}
	changed = true
	continue
}
```

注意：mimeType 在现行代码里于 decode **之后**读取（:147-151）。把 `mimeType, _ := m["mimeType"].(string)`
的读取提到 decode 之前（纯移动，不改语义），降级行里才能点名声明的 MIME。`fallbackUnknown` 是
同文件既有 helper。**不落盘**：解码不了的字节没有可取回的文件，降级行只报字符数。

### Step 2：资源路径双胞胎

资源/blob 路径的 decode 失败 `continue` 同样替换为降级行（文案对齐该路径既有的落盘行风格，
执行时以该路径的相邻文案为准），`changed` 语义对齐该函数自己的重打包逻辑。

### Step 3：测试

`pkg/mcp/result_processing_test.go`（跟随既有用例风格）：

- **T1**：单个 image 块，`data` 为非法 base64（如 `"!!!!not-base64!!!!"`）→ 返回的 envelope
  含 "could not be base64-decoded"、**不含** `"data"` 键的原始垃圾、`parts` 为空、无截断标记
  （`resultJSONContainsImage` 不再触发——结果 JSON 里没有 image 块了）。
- **T2**：混合结果：一个坏块 + 一个好块 → 好块照常落盘并产出 image part，坏块降级为文本行，
  文本 envelope 不丢（"one bad block never costs the call"的延伸覆盖）。
- **T3**：`wantParts=false`（拒收会话）+ 坏块 → 降级行照常出现，surface facts 不受影响。

## STOP conditions

1. 资源路径找不到同构 decode `continue`（结构已变）——报告实际结构，单独处理工具结果路径即可。
2. `resultJSONContainsImage` 的位置/语义与摘录不符——重核后仍不确定即停。
3. 降级行文案的既有先例（oversize/persist/saved-not-sent 三处）风格冲突无法对齐——列出三处
   现文案停下让 owner 选。

## Out of scope（不要做）

- URL-safe base64 的兼容解码（MCP 规范是标准 base64；server 发 URL-safe 属规范违背，降级即可）。
- 057 的 file_reference 改造、058 的 MIME 收紧（各自计划内）。
- 对 `decodedSizeWithin` 的任何改动。

## 全量验证闸门

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/mcp -count=1
CGO_ENABLED=1 go vet -tags fts5 ./...
gofmt -l cmd pkg third_party
```
