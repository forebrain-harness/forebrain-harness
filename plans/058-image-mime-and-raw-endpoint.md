# 058：图片 MIME 嗅探收紧 + raw 取图端点的会话归属校验

> **执行者须知（零上下文）**：本计划自包含。逐步执行，每步先跑"验证"确认预期再进入下一步。
> 命中 STOP condition 就停下报告。**不建 worktree、不 commit**。
>
> **Drift check（先做）**：本计划写于 `git rev-parse --short HEAD` = `fa4af8a`（2026-10-16）。
> 执行前核对 `pkg/llm/llm.go`（ImageMIMEType）、`pkg/gateway/{files_write,session_files}.go`
> 与下文摘录一致；对不上按 STOP 处理。

## Status

- **Priority**: P1（发送侧把非图片字节当图片发给 provider；服务侧让"只服务图片"的端点按扩展名
  放行任意文件）
- **Effort**: S/M
- **Risk**: LOW-MED（MIME 判定收紧影响所有图片入口；靠"嗅探对合法图片永远成功"保证无回归）
- **Depends on**: none（与 055/057 无执行依赖；同文件改动建议排在它们之后减少冲突）
- **Category**: Security / Correctness（审计发现 #6/#7）
- **Planned at**: commit `fa4af8a`，2026-10-16

## Why this matters

1. **`ImageMIMEType` 的扩展名回退放行非图片字节**（`pkg/llm/llm.go:110-142`）：嗅探只对
   `image/*` 家族的"其它图片类型"（bmp/tiff…）返回 `""`（这是 053 已修的一半），**非图片**
   嗅探结果（`text/plain`、`application/octet-stream`）仍回退到扩展名——文本字节起名 `foo.png`
   得到 `image/png`：
   - 发送侧：`read_file` 图片分支把它当 ≤3 MiB 的合法图片原样发给 provider → 不透明 400，
     浪费一个往返 + 转录里多 ~4/3 倍垃圾 base64；
   - 服务侧：`/api/workspace/file/raw` 的 415 门（`files_write.go:123-129`）靠 mime 白名单把关，
     扩展名回退让"只服务图片"（:78-80 自述）的承诺失效——任意工作区文件改个 `.png` 后缀即可被取。
2. **`resolveRawImagePath` 吞掉"会话不属本 agent"**（`pkg/gateway/files_write.go:146-158` +
   `session_files.go:43-50`）：`filesRoot` 对 foreign `session_id`（未拥有/不存在）返回
   `errFilesNotFound`，`resolveRawImagePath` 把 `errFilesNotFound` 当"无文件视图"吞掉后仍把
   整个 `activeWorkspaceRoot()` 加入服务根——**任意/伪造 session_id 都能按绝对路径取主代理
   workspace 的图片**，绕过了其它文件端点都有的会话归属校验。

## Current state（摘自本次亲自阅读的代码）

### pkg/llm/llm.go ImageMIMEType（:107-142）

```go
sniffed := http.DetectContentType(sniff)
switch sniffed {
case "image/jpeg", "image/png", "image/gif", "image/webp": return sniffed
}
if strings.HasPrefix(sniffed, "image/") { return "" }   // 只挡"其它图片类型"
// Non-image bytes (or a sniff we do not recognize): fall back to the
// extension, as before.                                    // ← 缺陷：文本/未知二进制也回退
switch strings.ToLower(filepath.Ext(path)) { case ".jpg", ".jpeg": return "image/jpeg" ... }
```

合法 JPEG/PNG/GIF/WEBP 的魔数都在 `http.DetectContentType` 的识别表内——**嗅探对四种可发送
格式的合法文件永远成功**，扩展名回退实际只服务"字节不是这四种"的情况。

调用方（执行时重新 grep 确认完整清单）：
- `pkg/llm/llm.go` `ImageFile`（:95，有完整 data）
- `pkg/tool/file_tools.go:90` `imageByName` 门 + `imageReadPart` 内部
- `pkg/mcp/result_processing.go:150`（`ImageMIMEType("", decoded)`——无路径，纯嗅探）
- `pkg/gateway/files_write.go:123`（raw 端点 415 门）

### pkg/gateway/files_write.go resolveRawImagePath（:146-170）

```go
if root, err := s.filesRoot(ctx, sessionID, projectID); err == nil {
	fileViewRoot = ...
} else if !errors.Is(err, errFilesNotFound) {
	return "", err                     // 只有非 errFilesNotFound 才报错；NotFound 被吞
}
var roots []string
for _, root := range []string{fileViewRoot, strings.TrimSpace(s.activeWorkspaceRoot())} { ... }
```

`pkg/gateway/session_files.go:43-50`：`sessionID != ""` 时先 `s.sessionOwned(ctx, sessionID)`，
`!owned → return "", errFilesNotFound`——与"项目不存在/无文件视图"是同一个错误值，调用方无法区分。

## 修复设计

### Step 1（#6）：嗅探全权（发送侧）

`ImageMIMEType` 去掉对**非图片嗅探结果**的扩展名回退——data 非空时嗅探就是唯一裁决：

```go
sniffed := http.DetectContentType(sniff)
switch sniffed {
case "image/jpeg", "image/png", "image/gif", "image/webp":
	return sniffed
}
// The bytes are not one of the four formats a provider accepts — not a
// different image type (handled below), not anything else. The sniff is
// authoritative: a valid file of these formats always names itself by magic
// bytes, so the extension fallback could only ever relabel bytes that are
// not that image (text named .png, a truncated header), sending junk to the
// provider or past an image-only gate.
return ""
```

（即：`strings.HasPrefix(sniffed, "image/")` 分支与其余分支合并为统一 `return ""`；函数 doc 改写。
`data` 为空的调用方若存在——执行时 grep，预期没有——保留空 data 时按扩展名的旧行为，并在 doc 注明。）

配套：`pkg/tool/file_tools.go` `imageReadPart` 对 `mime == ""` 且 `imageByName == true` 的错误
文案（现状会走到 `ImagePartWithin` 的 `image type "" cannot be sent; convert it to png or jpeg
first`）改为点名"字节与扩展名不符"，例如：
`the bytes of %s are not a %s image the providers accept; the file may be misnamed or corrupted`。
（在 imageReadPart 入口判定即可，不要改 ImagePartWithin 的通用文案。）

**验证**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/tool ./pkg/mcp -count=1`。

### Step 2（#7）：raw 端点先过会话归属

`pkg/gateway/files_write.go` `resolveRawImagePath`：session_id 显式给出时先自查归属，不再依赖
`filesRoot` 的歧义错误：

```go
if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
	owned, err := s.sessionOwned(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if !owned {
		// A foreign session id must not widen into the agent workspace the
		// way a project-less session legitimately does: every other file
		// endpoint refuses it, and this one must too.
		return "", errFilesNotFound
	}
}
```

（放在 `filesRoot` 调用**之前**；随后 `filesRoot` 的 errFilesNotFound 继续表示"本会话无文件视图"
——那才是允许落到 workspace 根的合法情形：项目会话的 MCP 截图路径。）
`filesRoot`/`session_files.go` 本身不改（其它端点语义不动）。

**验证**：T2。

### Step 3：测试

- **T1**（`pkg/llm` 或 `pkg/tool` 测试文件，跟随既有风格）：
  - 文本字节 + 名为 `.png` → `ImageMIMEType` 返回 `""`；read_file 图片分支得到 Step 1 的新文案，
    **不**产生 image part；
  - BMP 字节 + `.png` 名 → `""`（既有 053 行为不回归）；
  - 合法 PNG 字节 + `.txt` 名 → `image/png`（嗅探为尊，与扩展名无关）；
  - 截断但魔数在的 PNG → `image/png`（保持现状：直发由 provider 裁决）。
- **T2**（`pkg/gateway/files_write_test.go`，跟随既有 34-132 的用例风格）：
  - foreign session_id + 指向 workspace 内合法图片的绝对路径 → **拒绝**（404，与其它文件端点
    一致），不再从 activeWorkspaceRoot 服务；
  - 本会话无项目（合法 workshop/无项目情形）+ workspace 内 MCP 截图绝对路径 → 仍可取（不回归）；
  - 文本字节 + `.png` 名 → 415（Step 1 修掉扩展名回退后 415 门自然挡住）。

## Test plan

见 Step 3（T1/T2 即全部新增测试；无其它测试面变化）。

## STOP conditions

1. grep 发现 `ImageMIMEType` 存在**空 data** 调用方（预期没有）——为其保留扩展名路径前停下报告。
2. 发现既有测试依赖"非图片嗅探 + 图片扩展名 → image/*"的行为——**已发生（2026-10-16 执行时）**：
   `pkg/llm/image_scale_test.go` `TestImageMIMETypeSniffsAndFallsBack` 的用例
   `{name: "text content, png ext", path: "fake.png", data: text, want: "image/png"}` 钉的正是
   本计划判定为缺陷的行为。**处置（评审者裁定，依据 owner 已两次批准嗅探全权语义）**：该用例
   的期望改为 `""`（名字与注释同步改），测试名如仍称 FallsBack 则改为 SniffAuthoritative 一类；
   其余用例不动。处置记录在本条并在批次最终报告中向 owner 明示。
3. `sessionOwned` 在 `Server` 上不存在或语义不同（预期存在：`session_files.go:44` 在用）——停下报告。

## Out of scope（不要做）

- `filesRoot` 错误类型的重构（歧义在调用方解决即可）。
- raw 端点的鉴权模型（单 token 本地优先威胁模型是既定前提）。
- 415/404 的状态码重设计；`workspaceFileRawMaxBytes` 调整。
- MCP 无效 base64 块（审计 #8）、缩略图 @error（#20 项 4）。

## 全量验证闸门

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/tool ./pkg/mcp ./pkg/gateway -count=1
CGO_ENABLED=1 go vet -tags fts5 ./...
gofmt -l cmd pkg third_party
```
