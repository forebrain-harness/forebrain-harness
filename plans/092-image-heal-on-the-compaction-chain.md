# Plan 092: 图像自愈覆盖压缩链，且坏图不再"假装可发送"

> **Executor instructions**: 按步骤执行，每步跑完「Verify」并确认输出符合预期再进入下一步。
> 命中「STOP conditions」任一条就停下来报告，不要即兴改动。全部完成后更新
> `plans/README.md` 里本计划那一行的状态（除非派发你的评审方说他维护索引）。
>
> **Drift check（先跑）**: `git diff --stat 8d3322c..HEAD -- pkg/run/runner.go pkg/run/llm_middleware.go pkg/llm/image_heal.go pkg/llm/image_scale.go`
> 若有变动，先把「Current state」的摘录与现码对照；对不上按 STOP 处理。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: MED
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `8d3322c`, 2026-10-21

## Why this matters

provider 拒绝一张图时，主请求链会自愈（把图降级成一句文本重发一次），但**压缩/总结链没有这一层**：会话里一旦出现一张
被拒的图，自动压缩与溢出后的强制压缩都会把同一张图原样再发给 provider、再被拒，压缩静默失败（只有一行 slog），
历史继续增长直到撞窗口——该会话变成"压不动又发不出"的死胡同。另外两条小缺陷同源：图片头解析失败时"每边像素上限"
静默失效（`(0,0)` 被当成"通过"），坏图被原样上送；末档重编码把缩放系数硬报成 8，让 `read_file` 之类回报的数字不对。
修完之后：含被拒图片的历史仍能压缩，坏图在本地就得到可行动的报错，诊断里的缩放系数是真实的。

**本计划不处理**（需 owner 先定去向）：provider 拒图时对用户完全静默（`pkg/llm/image_heal.go:127-141` 只有 slog，
而尺寸阶梯那条路径会发系统行 `pkg/tui/chat_surface.go:144-156`）。要让它可见，得先决定这条提示长在哪
（线程里的系统行 / 压缩卡那样的卡片 / 一次性提示），这属于产品裁决，见 `plans/README.md` 的"已审核但未计划的发现"。

## Current state

- `pkg/llm/image_heal.go:113-142` — 自愈本身（只在主链上，见下一处）：

```go
func (h *imageHealLLM) Execute(ctx context.Context, messages []Message, tools []*Tool) (*Result, error) {
	result, err := h.inner.Execute(ctx, messages, tools)
	if err == nil || !IsImageRejection(err) { return result, err }
	degraded, replaced := DegradeImages(messages, err.Error())
	if replaced == 0 { return result, err }
	slog.Warn("provider rejected an image; retrying with the image degraded", "replaced", replaced, "err", err)
	return h.inner.Execute(ctx, degraded, tools)
}
```

- `pkg/run/runner.go:982-989` — `summaryChain` 在 heal **之前**取走（`r.summaryLLM = summaryChain`）：

```go
	// Everything wrapped so far shapes the start of a request — system prompt,
	// injected context, the tools a typed subagent sees. A compaction's summary
	// request is sent through exactly this, so it starts byte for byte like the
	// conversation's own requests and reuses their cached prefix; ...
	summaryChain := llmClient
	r.summaryLLM = summaryChain
```

- `pkg/run/runner.go:1052-1058` — heal 是在这之后才包进主链的：

```go
llmClient = WrapRecoverableLLM(llmClient, ...)
```

  而 `pkg/run/llm_middleware.go:257-268` 的 `WrapRecoverableLLM` 里才有 `inner = llm.Use(inner, llm.NewImageHeal())`。
  压缩请求走的是 `runner.go:1052` 附近 `summaryChain.Execute(ctx, append(msgs, instruction), tools)`
  （`compactDeps` 的 `conversation` 闭包），因此永远拿不到 heal。
- `pkg/llm/image_scale.go:62-67` + `:96-101` — 头解析失败 ⇒ `(0,0)` ⇒ `withinSideLimit(0,0)` 为真 ⇒ 体积达标就 verbatim，
  而这条路径后面**没有** decode：

```go
// withinSideLimit reports whether encoded dimensions are ones a provider
// accepts. A header that does not parse (w or h 0) is not judged here: the
// decode that follows reports it.
func withinSideLimit(w, h int) bool { return w <= MaxImageSidePixels && h <= MaxImageSidePixels }
...
srcW, srcH := DecodeImageConfig(data)
res.SourceWidth, res.SourceHeight = srcW, srcH
if len(data) <= budget && withinSideLimit(srcW, srcH) {
	res.Width, res.Height = srcW, srcH
	res.Scale = 1
	return withSourcePath(ImageBase64(mime, ...), path), res, nil     // ← 没有 decode
}
```

- `pkg/llm/image_scale.go:167-186` — 末档重编码硬报 8（`lastImg` 可能是 2/3/4/6 档的产物）：

```go
if lastImg != nil {
	if encoded, err := encodeImage(lastImg, "image/jpeg", 75); err == nil {
		candidate := res
		...
		candidate.Scale = 8
```

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Go 单包（llm） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -count=1` | `ok` |
| Go 单包（run） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` | `ok` |
| 相关包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/run ./pkg/turn -count=1` | 全 `ok` |
| vet | `go vet ./pkg/llm ./pkg/run` | 无输出 |
| 包图 | `scripts/package-graph.sh` | 无变化 |

## Scope

**In scope**：
- `pkg/run/runner.go`（只改 `summaryChain` 的构造位置）
- `pkg/run/llm_middleware.go`（如需把 heal 的构造抽成一个可复用的 helper）
- `pkg/llm/image_scale.go`
- `pkg/llm/image_scale_test.go`、`pkg/llm/image_heal_test.go`
- `pkg/run/runner_test.go` 或 `pkg/run/compaction*_test.go`（哪个现有文件覆盖压缩链就用哪个）

**Out of scope**：
- `pkg/llm/image_heal.go` 的行为语义：降级只影响这一次请求、transcript 保留原图是**既有设计决定**
  （`pkg/run/llm_middleware.go:262-266` 注释），本计划不改；把这条提示变成用户可见是另一个计划（见上）。
- `pkg/run/transcript.go` 的 `rehydrateMessageImages`：尺寸阶梯那条路径已经正确。
- `pkg/turn/input_parts.go` 的 `UnsendableImageNote` 文案：不动。
- 压缩策略本身（阈值、checkpoint、guard）：不要碰。

## Git workflow

- 直接改当前 `dev` 工作区；**不 commit、不 push**，除非派发方明确要求。
- 若要求提交：`fix(run): …` 或 `fix(llm): …`，body 用 `Why:` / `What:` / `Verification:`，`git commit -s`。

## Steps

### Step 1: 压缩链也装 image heal

在 `pkg/run/llm_middleware.go` 抽出一个可复用的包装（避免两处各写一遍）：

```go
// withImageHeal is the one place the image-degradation retry is attached. The
// main chain and the summary chain both need it: a conversation that carries an
// image its provider refuses must still be able to compact, or its history grows
// until nothing fits.
func withImageHeal(inner llm.LLM) llm.LLM {
	if inner == nil { return nil }
	return llm.Use(inner, llm.NewImageHeal())
}
```

- `WrapRecoverableLLM` 里的 `inner = llm.Use(inner, llm.NewImageHeal())` 改成 `inner = withImageHeal(inner)`。
- `pkg/run/runner.go`：把 `summaryChain := llmClient` 改成 `summaryChain := withImageHeal(llmClient)`，
  并**保留**原有注释，追加一句说明为什么把它算作"不改变请求字节"的包装：

```go
	// ... the wrappers below only add reminders at the end of a turn's request and
	// recover a turn's call, and a summary wants neither. The image heal is the
	// one exception that is safe here: it only fires after the provider has
	// refused a request, and the retried copy is that call's alone — a summary
	// request that succeeds is byte for byte the one this chain built.
	summaryChain := withImageHeal(llmClient)
	r.summaryLLM = summaryChain
```

  注意 `r.summaryLLM = summaryChain` 必须仍然在 `withSkillCatalogLLM` 之后、`wrapPlanModeLLM` 之前的位置语义上——
  即 summary 仍然**不**带 plan-mode / skill-offer / 提醒类包装。

**Verify**: `go build ./pkg/run` → exit 0；`grep -n "withImageHeal" pkg/run/runner.go pkg/run/llm_middleware.go` → 两文件各命中。

### Step 2: 用测试钉住"含被拒图片的历史仍能压缩"

在覆盖压缩链的现有测试文件里（`grep -rln "summaryLLM\|TryCompactOnMessages" pkg/run/*_test.go`，取第一个命中的）加一条：

- 造一个 fake `llm.LLM`：第一次 `Execute` 返回 `llm` 里的图片拒绝错误（用 `pkg/llm/image_heal_test.go` 里既有的
  "how a provider rejection is recognised" 用例构造同一个错误类型——`grep -n "IsImageRejection" -A 6 pkg/llm/errors.go`），
  之后的调用返回固定文本；
- 让 runner 的 summary 链跑一次 `summaryChain.Execute`（或走 `compactSvc` 的 conversation 闭包），断言：
  - 最终拿到了结果（不是"压缩失败"）；
  - fake LLM 被调用 **两次**（第一次拒绝 + 一次降级重发）；
  - 第二次收到的 messages 里，image part 已变成文本（`strings.Contains(part.Text, "[image omitted")`）；
- 反向验证：把 Step 1 的 `withImageHeal` 从 summaryChain 上摘掉，这条用例必须变红。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run TestSummaryChainHealsRefusedImage -count=1` → `ok`。

### Step 3: 头解析失败不再"通过"

`pkg/llm/image_scale.go`：

```go
// withinSideLimit reports whether encoded dimensions are ones a provider
// accepts. Dimensions that did not parse (w or h 0) are not judged here, but
// they are also not a pass: the verbatim branch is only taken for an image
// whose header decoded, so a truncated or corrupt file reaches the decoder and
// gets an actionable error instead of a request the provider is certain to
// refuse.
func withinSideLimit(w, h int) bool {
	return w > 0 && h > 0 && w <= MaxImageSidePixels && h <= MaxImageSidePixels
}
```

（只改这一个函数即可：`verbatim` 分支的条件是 `len(data) <= budget && withinSideLimit(...)`，`(0,0)` 因此不再命中。）

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -count=1` → `ok`（若既有金样断言了旧行为，
按 Step 5 的用例把它改成"应当报错"，不要为了让旧断言过而放宽实现）。

### Step 4: 末档重编码报真实的缩放系数

`pkg/llm/image_scale.go`：在缩放循环里记住实际用到最后一档的系数（例如 `lastScale int`，与 `lastImg` 一起赋值），
末档重编码时 `candidate.Scale = lastScale`（`lastScale == 0` 时保持 8 的旧值作为兜底，但正常路径不应为 0）。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run TestImageScale -count=1` → `ok`。

### Step 5: 用例

1. `pkg/llm/image_scale_test.go`：
   - 一张**截断的 PNG**（取一张合法 PNG 的前 100 字节，尺寸远小于预算）⇒ `ImagePartWithin` 返回错误，
     且错误里出现 "cannot" 或 "decode" 之类的可行动词；**不得**返回 verbatim part；
   - 一张合法小图 ⇒ 仍走 verbatim（`res.Scale == 1`，`res.SentBytes == len(data)`）——钉住没有破坏正常路径；
   - 一张无论怎么缩都超预算的图（可用极小的 `maxBytes` 传入触发）⇒ 返回的 `Scale` 等于末档**实际**系数，
     不等于 8（构造时让它停在 2 或 4 档）。
2. Step 2 的 run 层用例。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/run -count=1` → 全 `ok`。

### Step 6: 全量回归

**Verify**:
1. `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/run ./pkg/turn -count=1` → 全 `ok`。
2. `go vet ./pkg/llm ./pkg/run` → 无输出。
3. `scripts/package-graph.sh` → 无 diff。
4. （可选，改动面小可跳过）`FOREBRAIN_E2E_SPEC=e2e/message-cards.spec.ts scripts/acceptance/web_e2e.sh` → PASS。

## Test plan

见 Step 2 / Step 5。参照：`pkg/llm/image_heal_test.go` 既有的"provider 拒绝被识别"用例与
`pkg/llm/image_scale_test.go` 既有的阶梯用例（`grep -n "func TestImagePartWithin\|func TestImageScale" pkg/llm/image_scale_test.go`）。
两条反向验证必须做（摘掉 heal / 恢复 `withinSideLimit` 旧实现，各自变红）。

## Done criteria

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm ./pkg/run ./pkg/turn -count=1` 全 `ok`
- [ ] `go vet ./pkg/llm ./pkg/run` 无输出
- [ ] `grep -n "summaryChain := withImageHeal" pkg/run/runner.go` → 命中
- [ ] `grep -n "w > 0 && h > 0" pkg/llm/image_scale.go` → 命中
- [ ] `grep -n "candidate.Scale = 8" pkg/llm/image_scale.go` → 只在兜底分支出现（或已完全消失）
- [ ] `scripts/package-graph.sh` 无 diff
- [ ] `git status --porcelain` 只含 Scope 列出的文件
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

- 存在一条钉住"summary 请求与主请求逐字节相同"的金样测试，加了 heal 之后失败——先确认失败原因；
  如果确实是"成功请求的字节变了"（而 heal 只应在失败后触发），说明包装位置错了，停下来报告。
- 压缩链的 `msgs` 在调用 `summaryChain.Execute` 时**不含** image part（那说明 rehydrate 只在别处发生），
  那么本计划的 Step 1/2 就没有观察点，停下来报告。
- `(0,0)` 的 verbatim 分支在别处还被依赖（例如某些 webp/异常格式靠它绕过 decode）——搜索后确认有依赖就停下来报告，
  不要直接放宽。
- 任一 Verify 连试两次仍失败。

## Maintenance notes

- **两条链都要 heal**：以后给主链加"失败后重试/降级"类包装时，问一句"压缩请求会不会踩同样的失败"；
  压缩请求与主请求共享前缀是硬约束（`pkg/run/runner.go:982-988` 的注释），所以只允许加"成功路径不改变字节"的包装。
- `withinSideLimit` 的语义现在是"尺寸已知且合法才通过"；新增格式支持（webp 等）时别把 0,0 当默认通过。
- 评审要点：heal 没有改变成功请求的字节；压缩重量级路径（`forceCompact` 溢出恢复）也走同一条 summaryChain；
  坏图报错文案可行动（说清是"截断/损坏"，而不是笼统的 decode 失败）。
- 后续（本计划不做）：provider 拒图对用户静默——需要先裁决提示长在哪（线程系统行 / 卡片 / 一次性提示），
  另立计划；同时那里还有"每一轮都要重发一次被拒的图（2× 成本）"的问题，可能要在会话级记住这张图已被拒。
