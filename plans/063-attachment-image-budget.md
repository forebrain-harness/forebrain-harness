# 063：用户附件/粘贴路径统一走图片预算——附件不再是 base64 失控的后门

> **执行者须知（零上下文）**：本计划自包含。逐步执行，每步先跑"验证"确认预期再进入下一步。
> 命中 STOP condition 就停下报告。**不建 worktree、不 commit**。
>
> **Drift check（先做）**：本计划写于 `git rev-parse --short HEAD` = `fa4af8a`（2026-10-16）。
> 执行前核对 `pkg/llm/llm.go`（ImageFile/ImageMIMEType）与 `pkg/llm/image_scale.go`
> （ImagePartWithin/resolveImageBudget）与摘录一致；**若 055 已执行**，`image_scale.go` 行号会
> 漂移（多出 MaxImageDecodePixels 守卫），语义以函数名为准。对不上按 STOP 处理。

## Status

- **Priority**: P2（成本/缓存问题，非崩溃；但 40 MB 粘贴照片 = ~53 MB base64 每轮重发，
  正撞本项目"缓存命中率只升不降"的最高优先级）
- **Effort**: S/M
- **Risk**: MED（改变用户亲手粘贴内容的发送字节；必须保证小文件逐字节不变 + 阶梯确定性）
- **Depends on**: 建议在 055 之后（附件自动获得 055 的像素上限保护与 env 语义；无硬依赖——
  本计划传 0 预算自身即可解析 env+默认值）。**与 057 无执行依赖**（057 改工具行 rehydrate；
  本计划改 RoleUser 附件链路）。
- **Category**: Correctness / Cost（审计发现 #18）
- **Planned at**: commit `fa4af8a`，2026-10-16；owner 2026-10-16 批准入期（原"产品语义待拍板"已拍）

## Why this matters

图片进模型的三条工具路径（read_file/PDF/MCP）都有 3 MiB 预算 + 降采样阶梯，唯独**用户亲手给的
图片**（TUI 粘贴/拖拽、web 上传附件）走 `llm.ImageFile`——无预算、无降采样、原字节直发：

- `pkg/llm/llm.go:88` `ImageFile(path)`：读文件 → `ImageMIMEType` → 全量 base64。对比
  `ImagePartWithin`：预算、阶梯、48 MiB 上限一应俱全。
- 调用方：`pkg/turn/input_parts.go:47`（附件 part 构建）、`pkg/tui/run.go:2621,3134`（粘贴/拖拽）、
  `pkg/run/transcript.go:324`（**rehydrate**，RoleUser）、`pkg/gateway/run_control.go:578`（web 上传）。

后果：一张 40 MB 照片变 ~53 MB base64 进请求；它落在**用户消息**里——该会话**此后每一轮请求**
都原样重发，直到上下文压缩；wire 成本、token 计费、transcript 体积三头放大。这正是 3 MiB
预算要堵的成本，附件路径是漏掉的后门。

**owner 已拍板的产品语义**：附件与工具结果同契约——小文件（≤预算）**逐字节不变直发**（无损），
超预算走既有降采样阶梯，超过 48 MiB 解码上限给可执行错误（与 read_file 同文案语义）。env
`FOREBRAIN_IMAGE_MAX_BYTES` 同样对附件生效（本计划传 0 预算即得）。

## Current state（摘自本次亲自阅读的代码）

### pkg/llm/llm.go

```go
// :88 附近
func ImageFile(path string) (ContentPart, error) {
	data, err := os.ReadFile(path)
	if err != nil { return ContentPart{}, err }
	mimeType := ImageMIMEType(path, data)
	if mimeType == "" {
		return ContentPart{}, fmt.Errorf("ImageFile: unsupported image type %q for %q", mimeType, path)
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	return ImageBase64(mimeType, b64), nil
}
```

（`pkg/run/transcript.go:324` 的 rehydrate 对 RoleUser 用 `llm.ImageFile(absPath)`——`rehydrateMessageImages`
:308 的角色门只放 RoleUser；057 会把 RoleTool 分支改为 ImagePartWithin，本计划不碰那个分支。）

### pkg/llm/image_scale.go

`ImagePartWithin(path, mime, data, maxBytes)`：`maxBytes=0` → `resolveImageBudget`（env → 默认
3 MiB）；≤预算直发（`SourceBytes==SentBytes`、`Scaled=false`）；超预算走 2/3/4/6/8 阶梯 +
q75 兜底；>48 MiB 拒绝（"resize it first"）；**阶梯确定性**是函数文档明言的契约（"the same
input always produces the same part"）。

## 修复设计

### Step 1：ImageFile 内部改走预算契约

`pkg/llm/llm.go` `ImageFile` 重写（签名不变，调用方零改动）：

```go
// ImageFile reads path and returns it as an image ContentPart under the same
// budget the tool paths use: a file within FOREBRAIN_IMAGE_MAX_BYTES (default
// 3 MiB) is sent byte-for-byte; a larger one is downscaled by the fixed
// ladder; one over the decode ceiling is refused with an actionable error.
// Attachments are user-authored, so the ladder's verbatim-first step is what
// keeps ordinary pastes and screenshots bit-identical to before.
func ImageFile(path string) (ContentPart, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ContentPart{}, err
	}
	mimeType := ImageMIMEType(path, data)
	if mimeType == "" {
		return ContentPart{}, fmt.Errorf("ImageFile: unsupported image type for %q; convert it to png or jpeg first", path)
	}
	part, _, err := ImagePartWithin(path, mimeType, data, 0)
	return part, err
}
```

（0 = env + 默认预算。`ImagePartWithin` 的错误文案已可执行；`mimeType == ""` 的旧文案里
`%q` 打印空串的怪相顺手修成上面的点名式——行为同旧：报错不发送。）

### Step 2：影响面逐点核查（执行时做，不是假设）

1. **live 与 rehydrate 同源**：`pkg/turn/input_parts.go` 与 TUI/gateway 的 live 构建走
   `ImageFile`；`pkg/run/transcript.go:324` 的 rehydrate 也走 `ImageFile`——**同一函数**，
   阶梯确定性保证 resume 后重建的 part 与 live 轮次逐字节一致。grep 确认没有第二个
   用户图片构建点绕过（`grep -rn "ImageBase64(" pkg/ --include=*.go | grep -v _test | grep -v image_scale.go`）。
2. **file_reference 持久化兼容**：用户消息的 parts_json 本就存 file_reference（不存 base64），
   本计划不改变持久化形状——`state/message_parts.go:478` 的 load 路径不变。
3. **降采样后的 surface 事实**：web 附件卡/TUI 附件行若显示图片字节数，其来源是上传时的
   元数据（非 part）——grep 前端与 TUI 对附件 size 的展示，确认没有把"发送字节数"当"文件
   字节数"展示的地方需要跟着改（预期没有；发现即列 STOP）。
4. **升级时的一次性前缀位移（如实记录，不修）**：升级前发送过 >3 MiB 附件的存量会话，
   升级后 rehydrate 会产出降采样 part（而非当年的原字节 part）→ 该会话的下一次请求对
   provider 缓存发生**一次**前缀位移。这是行为变更的必然代价，量级限于"有过超大附件且会
   继续"的会话。在 plans/README 对应条目记一句，不做兼容开关。

### Step 3：测试

- **T1（无损直发）** `pkg/llm`：≤3 MiB 的真实 PNG 经 `ImageFile` → part 的 base64 与
  `base64.StdEncoding.EncodeToString(原字节)` **完全相等**、MIME 相同（钉住"小文件逐字节
  不变"）。
- **T2（预算生效）**：>3 MiB 的可压缩 PNG（如纯色大图）→ `SentBytes < SourceBytes`、MIME 仍
  png；`t.Setenv("FOREBRAIN_IMAGE_MAX_BYTES", …)` 收紧后行为跟随（若 055 已执行则其 T4 已覆盖
  read_file 路径，本条只钉 ImageFile）。
- **T3（超上限拒绝）**：>48 MiB 输入 → 可执行错误（含 "resize"），不返回 part。
- **T4（live/rehydrate 一致）** `pkg/run`：既有附件 rehydrate 测试改造——对一张超预算图，
  断言 rehydrate 产物与首次 `ImageFile` 调用产物逐字段相等（确定性阶梯的端到端钉）。
- **T5（类型拒绝文案）**：非图片字节 + `.png` 名 → 错误点名 convert（与 058 的 T1 呼应；
  若 058 未执行，此处仅断言 mimeType=="" 分支报错，不依赖其文案）。

## STOP conditions

1. grep 发现绕过 `ImageFile` 的第二用户图片构建点（直接 `ImageBase64` 拼 part）——列出调用方，
   一并改造或停下报告（视复杂度）。
2. T4 失败且原因在阶梯不确定性——缓存前缀级问题，立即停下报告（与 057 的 STOP-2 同源）。
3. 发现 TUI/web 把"发送字节数"展示为"文件字节数"的面——列出位置让 owner 定改法，不要静默改 UI。

## Out of scope（不要做）

- 拒绝式上限调整（>48 MiB 的处理维持与 read_file 同契约；若 owner 想对附件放宽另议）。
- 附件的像素上限独立配置（沿用 055 的 MaxImageDecodePixels 单一预算）。
- web 上传的**客户端**预压缩（浏览器端 resize 是另一个产品面）。
- 057 的工具行改造、058 的 MIME 收紧（ImageMIMEType 本身不动——058 若已执行则本计划的
  `mimeType == ""` 分支自然受益）。

## 全量验证闸门

```bash
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/turn ./pkg/run ./pkg/tui ./pkg/gateway -count=1
CGO_ENABLED=1 go vet -tags fts5 ./...
gofmt -l cmd pkg third_party
```
