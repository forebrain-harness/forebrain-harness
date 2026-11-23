# Plan 070：web 与终端同一语义——当前模型不支持图片时，消息保留、只拒绝请求；会话模型菜单标“支持图片”

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不建 worktree、不 commit、不 push、不 stash；改动留在当前工作区。
> 先跑 `scripts/fetch-libtokenizers.sh`；若 `go test` 报 `Package pdfium was not found in the pkg-config search path`，
> 先跑 `make pdfium pdfium-fonts`，并照 Makefile 导出 `PKG_CONFIG_PATH`。
>
> **前置（工作区里已有、未提交）**：本计划建立在 2026-10-17 已落在工作区的**终端侧修复**之上（见“Current state §1”）。
> **Drift check（先跑）**：以下 4 条都必须各有 1 行输出，否则 STOP：
> ```
> grep -n 'func ImageTurnRefusal(ctx context.Context, r \*Runner, sessionID string, attachesImage bool) string' pkg/run/image_input.go
> grep -n 'imageRefusal := run.ImageTurnRefusal(ctx, s.runner(), sessionID, len(submission.Attachments) > 0)' pkg/tui/chat_surface.go
> grep -n 'imageRefusal = s.Runner.ImageInputRefusal(ctx)' pkg/gateway/run_control.go
> grep -n "if (op === 'turn_withdrawn') {" frontend/src/composables/useChatStream.ts
> ```

## Status

- **Priority**: P1
- **Effort**: L（引擎 + 状态库 + 终端微调 + gateway + 前端 + 端到端用例）
- **Risk**: MED（改 gateway 的发送主路径与前端发送收尾；有完整的 Go/前端/浏览器三层测试兜底）
- **Depends on**: 工作区里 2026-10-17 的终端侧修复（未提交，非编号计划）；与其它编号计划无交集
- **Category**: bug / semantic unification
- **Planned at**: commit `1e5866a` + 工作区，2026-10-17

## Why this matters

owner 的要求（2026-10-17）：**用户消息任何情况下都不能丢；当前模型不支持图片时继续报错，只有切到支持图片的模型才能续聊，且图片不能丢。**
终端已经这样做了（消息落库、只拒绝请求、换模型后 `continue` 能看到原图）。web 还是旧语义，且有三个缺陷：

1. **语义不一致**：web 上带图消息遇到纯文本模型时被“退回输入框”（`turn_withdrawn`），不落库；按项目规则“TUI 语义是标准，gateway 必须统一到 TUI”，web 必须改成：**消息留在对话里并落库，下方给出提示，不发请求**。
2. **web 续聊会撞 400**：对话历史里已有图片、当前模型是纯文本时，web 发一句“继续”没有任何门禁，请求带着图片发出去，provider 直接 400。
3. **门禁判断错了模型**：`prepareWebTurnInput` 用的是 `s.Runner.ImageInputRefusal(ctx)`——既不是这个会话所在项目的 runner（`turnRunner`），也不看**会话自己选的模型**（web 的模型 chip 写的是会话自己的模型行，与主代理默认可以不同），会误拦或漏拦。

另外两件 owner 已拍板的事一起做：**会话模型菜单里标出“支持图片”的模型**；**提示不落库**（刷新后只剩消息本身，与终端一致）。
终端那条修复还漏了一个点，本计划一并补上：**拒绝路径在落库前没做跨进程“会话忙”检查**——另一端正在跑这个会话的 turn 时，被拒的消息会插进别人运行中的对话。

## 设计稿（验收依据，实施须 1:1 还原）

- 原型：https://claude.ai/artifact/KKfPW8dXWScuRdTzonsk2F（本地源文件 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`）。
  打开后在“本轮新增”里点 4 个按钮：**图片 · 模型不支持**、**图片 · 没换模型再发**、**图片 · 换模型**、**图片 · 换模型后继续**。
- 原型里对应的代码（实现照它的结构与文案，不要自己发挥）：`app-preview.html` 中的 `imgUser`、`imgRefused`、`imgTurns`、`modelChip`、`modelOpts` 函数，以及 CSS `.model-menu`、`.u .u-atts`。
- **owner 认可的文案（逐字使用）**：
  - 提示第一行：`{model} 不支持图片，消息已保留，未发送。`（`{model}` 只写模型名，如 `deepseek-v4-pro`，不带 provider）
  - 提示第二行（灰色小字）：`对话里有图片，换支持图片的模型后继续。` 后接链接按钮 `换模型`
  - 模型菜单里支持图片的模型右侧的绿色徽标：`支持图片`
  - 英文：`{model} does not accept images. The message is kept and was not sent.` / `This conversation has images. Switch to a model that accepts images, then continue.` / `Switch model` / `Accepts images`
- 行为：发送后**用户气泡保留**（附件 chip 照常显示）、**输入框清空**（不是退回输入框）、提示块紧跟在这条消息下面；每条被拒的消息各有一个提示块；点“换模型”打开输入框里的模型菜单；刷新页面后提示块消失、消息仍在。

## Current state（全部已核实）

### §1 终端侧（工作区里已有，未提交）——引擎函数已经存在

`pkg/run/image_input.go`（当前内容，行号为工作区现状）：

```go
26  func (r *Runner) ImageInputRefusal(ctx context.Context) string {        // read_file / MCP 工具用的门禁，句子以 "then read the image again" 结尾 —— 本计划不改它
...
45  func ImageTurnRefusal(ctx context.Context, r *Runner, sessionID string, attachesImage bool) string {
46  	name := imageInputRefusedModel(ctx, r)
47  	if name == "" {
48  		return ""
49  	}
50  	if !attachesImage && !sessionHistoryCarriesImage(ctx, r, sessionID) {
51  		return ""
52  	}
53  	return fmt.Sprintf("the session model %s does not accept image input and this conversation contains images; the message is kept, switch model with /model and continue", name)
54  }
...
81  func imageInputRefusedModel(ctx context.Context, r *Runner) string {
82  	if r == nil {
83  		return ""
84  	}
...
88  	sel, ok := SessionModelSelectionFromContext(ctx)
89  	if !ok {
90  		sel = PrimaryModelSelection(r)          // ← 只看 runner 默认，不看会话自己的模型行（缺陷 3 的引擎侧根源）
91  	}
...
97  	hit, ok := llm.Lookup(provider, model)
98  	if !ok {
99  		// Unknown model: fail open.
100 		return ""
101 	}
102 	if hit.SupportsAttachments {
103 		return ""
104 	}
105 	if provider != "" {
106 		return provider + "/" + model
107 	}
108 	return model
109 }
```

`sessionHistoryCarriesImage`（同文件 59–76 行）按请求同样的方式重建历史（`rehydrateImageReferences`）再找图片 part——**不要改它**。

终端调用点：
- `pkg/tui/chat_surface.go:171`：`imageRefusal := run.ImageTurnRefusal(ctx, s.runner(), sessionID, len(submission.Attachments) > 0)`，随后把 `imageRefusal` 传进 `dispatchUserTurnContent`。
- `pkg/tui/chat_session.go:1902`：`dispatchUserTurnContent(..., userPartsJSON string, imageRefusal string, goalObjective string, ...)`；
  `:2007` `if imageRefusal == "" {`（被拒时跳过预压缩）；`:2065-2072` 落库后 `if imageRefusal != "" { return errors.New(imageRefusal) }`。
  **缺口**：`:2055` 的 `turn.PersistUserTurn` 之前没有跨进程“会话忙”检查（正常路径的忙检查在后面 `Core.Submit` 建 run 时才发生，被拒路径走不到那里）。
- 终端测试：`pkg/tui/chat_surface_test.go` 的 `TestDispatchSurfaceTurnKeepsImageMessageRefusedByTextOnlyModel`（断言 `err.Error()` 含 `does not accept image input`）；
  `pkg/run/image_input_test.go` 的 `TestImageTurnRefusalCountsImagesAlreadyInTheConversation`（断言返回字符串）。

会话模型的正确解析在引擎里已有（`pkg/run/config.go:1684`，未导出方法）：

```go
func (r *Runner) effectiveModelFor(ctx context.Context) (provider, model string) {
	if sel, ok := SessionModelSelectionFromContext(ctx); ok {
		return sel.Provider, sel.Model
	}
	if r != nil && r.Deps != nil && r.Deps.SessionStore != nil && ctx != nil {
		if sid := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx)); sid != "" {
			if row, hasRow, err := r.Deps.SessionStore.SessionModelSelection(ctx, sid); err == nil && hasRow {
				effort := row.Effort
				if resolved, err := ResolveSessionModelChoice(r, row.Provider, row.Model, &effort); err == nil {
					return resolved.Provider, resolved.Model
				}
			}
		}
	}
	return PrimaryModel(r)
}
```

### §2 状态库：忙碌判定只在建 run 时做

`pkg/state/runrt.go:226 CreateRun`：一条 `INSERT … WHERE NOT EXISTS(live primary run)`；插入 0 行时，用第二条查询（`:278-294`）判断阻塞者是“等审批 / 运行中”以及是否“另一端”，返回
`ErrSessionAwaitingApprovalElsewhere` / `ErrSessionAwaitingApproval` / `ErrSessionRunningElsewhere` / `ErrSessionRunning`（都 `errors.Is(err, ErrSessionBusy)`），查不到阻塞者时返回 `ErrSessionRunning`。
`state.SessionBusyCode(err)`（`:62`）给出稳定码。`SessionHasLiveRun`（`:1046`）只回 bool，不分类。**没有“只查不建”的分类接口**。

### §3 gateway：发送主路径（`pkg/gateway/server.go`，`HandleChatWS` 内）

顺序（行号为工作区现状）：
- `:1536-1537` `attachments := m.Message.Attachments` / `mentionImages := m.Message.MentionImages`
- `:1709` `turnRunner, turnRunnerErr := s.runnerForTurn(r.Context(), sid)`（失败即 `run_error` + 缓存，`continue`）
- `:1742` `cerr := s.autoCompactBeforeUserAppend(preflightCtx, sid, "webchat", content, rawContentForRetrieval)`（预压缩）
- 之后：UserPromptSubmit 钩子（可能改写 `content`）
- `:1847` `turnInput, attachErr := s.prepareWebTurnInput(r.Context(), sid, content, attachments, mentionImages)`；失败 → `turn_withdrawn`（带 `Error`）
- `:1859-1873` `s.RunRT.CreateRun(...)`；忙 → `turn_withdrawn`，`Data: {"code": state.SessionBusyCode(err)}`
- `:1927-1933` 落用户行：

```go
turn.PersistUserTurn(r.Context(), s.Sessions, turn.UserTurn{
	SessionID:  sid,
	RunID:      runID,
	ModelInput: input,
	RawInput:   firstNonBlank(rawContentForRetrieval, turnInput.display),
	PartsJSON:  turnInput.partsJSON,
})
```

`pkg/gateway/run_control.go:499 prepareWebTurnInput` 里的旧门禁（要删掉的部分）：

```go
501	// The image-input gate (Runner.ImageInputRefusal) is read once for the
...
505	imageRefusal := ""
506	if s.Runner != nil {
507		imageRefusal = s.Runner.ImageInputRefusal(ctx)
508	}
...
527		if strings.HasPrefix(mimeType, "image/") && strings.TrimSpace(imageRefusal) != "" {
528			return webTurnInput{}, errors.New(strings.TrimSpace(imageRefusal))
529		}
...
573		if strings.TrimSpace(imageRefusal) != "" {
574			return webTurnInput{}, errors.New(strings.TrimSpace(imageRefusal))
575		}
```

上传文件的元数据可只读查询：`s.Files.Get(ctx, s.Sessions.AgentID(), fid)` 返回 `*state.File`（`MediaType` 字段），`prepareWebTurnInput` 里同样用 `s.Sessions.AgentID()`。

WS 回复结构：`pkg/gateway/server.go:804 wsServerMsg{Op, RequestID, RunID, SessionID, TraceID, Text, Message, Data, Error, …}`；
同一 `request_id` 重发时用 `wsRequestCacheSet(requestCache, m.RequestID, msg, 2*time.Minute, time.Now())` 缓存的回复去重（`:1719` 的 `run_error` 分支是范例）。

### §4 会话模型选项（`pkg/gateway/api_extra.go`）

```go
1454 type sessionModelOption struct {
1455 	Provider string   `json:"provider"`
1456 	Model    string   `json:"model"`
1457 	Label    string   `json:"label"`
1458 	Efforts  []string `json:"efforts"`
1459 }
```

`sessionModelEfforts(records, canReason, provider, model)`（`:1467`）按 `rec.Provider` + `rec.APIModel` 匹配 catalog 记录——新字段照它的匹配规则取 `rec.SupportsAttachments`。
`records` 来自 `s.Core.ListModelCatalog`（含 models.json 与 ChatGPT 账号发现的模型；后者的图片能力来自 `input_modalities`）。`s.Core` 为 nil 时（单测夹具）records 为空，此时退到 catalog 查询。
`pkg/turn/models.go:321 type ModelChoice struct{ Label, Provider, Model, Name string; CanReason bool }`；`:350 ModelChoices` 里 `if hit, ok := llm.Lookup(provider, model); ok { … choice.CanReason = hit.CanReason }`。
现有测试：`pkg/gateway/api_extra_test.go:4501 TestChatSessionModelEndpoints`（配置 `zhipuai/glm-5.3-flash` + `deepseek/deepseek-chat`，断言 `require.Len(t, got.Options, 2)`）。
仓库 catalog 的相关事实（`assets/models.json`）：`zhipuai/glm-5.3-flash` attachment=true、`deepseek/deepseek-chat` attachment=true、`deepseek/deepseek-v4-pro` attachment=**false**、reasoning=true。

### §5 前端

- `frontend/src/lib/api.ts:913 export interface SessionModelOption { provider; model; label; efforts }`；所有 JSON 响应经 camelCase 拦截器（`api.ts:1407`），后端 `supports_images` 到前端是 `supportsImages`。WS 回调里的 payload 也是 camelCase（如 `payload.sessionId`）。
- `frontend/src/composables/useChatStream.ts`：
  - `:79 export interface ChatMessage { id?; role: 'user' | 'assistant' | 'notice'; content; … }`
  - `:4617-4629` 发送时乐观插入用户气泡（`userMessageId`，带 `attachments`）和占位助手消息（`assistantMessageId`，内容 `NOTHING_SAID_YET`）。
  - `slash_reply` 分支（`:4661` 起）是**范例**：把占位助手消息改成 `role: 'notice'` 留在对话里（刷新即消失），设 `eventState.fullAnswer`/`completedNormally`、`isStreaming=false`，`return { terminal: true }`。
  - `:4787 turn_withdrawn` 分支：删掉用户气泡、`withdrawn = true`，收尾时 `giveBack(...)` 把草稿放回输入框——**被拒不能走这里**。
  - 收尾（`:5024` 起）：`hasAssistantData` 为假且 run 未结束时删掉占位消息——新字段必须计入 `hasAssistantData`。
- `frontend/src/views/ChatView.vue`：
  - `:117-128` `msg.role === 'notice'` 渲染成卡片（slash 回复）；`:374-380` 忙碌提示用 `forebrain-err-block`（`CircleAlert` 图标 + 正文 + `<span class="forebrain-err-s">`）——**提示块照这个写**。
  - `:454-461` `<ModelChip v-if="sessionModelData" … @saved="loadSessionModel(sessionId ?? '')" />`
  - `:543` lucide 图标导入行；`:2195 .rewind-undo` 是对话里“文字链接按钮”的现成样式（品牌色、无边框、hover 下划线）。
- `frontend/src/components/chat/ModelChip.vue`：选项按钮 `<b><Check …/>{{ option.model }}</b><span>{{ option.provider }}</span>`，`data-testid="model-option-<model>"`；`open` 是内部 ref，**没有对外打开菜单的方法**；`onDocPointerDown` 在点到组件外时关菜单。
- 文案：`frontend/src/locales/index.ts`，中文块 `:1145 'chat.modelChipTitle'`、`:1165 'chat.messageBackInComposer'` 附近；英文块 `:2515`、`:2535` 附近。
- `frontend/src/views/ChatView.test.ts`：整模块 mock 了 `useChatStream`，`fake.messages` 可直接塞消息；`mountView()` 在 `:126`。

### §6 端到端

`scripts/acceptance/web_e2e.sh` 用真 gateway 二进制 + `FOREBRAIN_STATIC_DIST` 指向临时构建的 UI（**从不碰 `pkg/gateway/dist`**）+ 假 provider；
假模式配置（`:121-155`）主代理链是 `deepseek/deepseek-chat`、`zhipuai/glm-5.3-flash`。`frontend/e2e/attachments-save.spec.ts:34-45` 是“附一张 PNG 并在‘保存到’弹窗确认”的范例；
`frontend/e2e/session-busy.spec.ts` 是“直接查状态库 sqlite”的范例；`frontend/e2e/chat-composer.spec.ts:26-31` 是点模型 chip 换模型的范例。真模型模式（`FOREBRAIN_E2E_REAL_LLM=1`）只配 zhipuai。

### 房规（`AGENTS.md` 与 owner 规则）

- 测试/脚本**不得继承环境里的 `FOREBRAIN_HOME`**（用 `t.TempDir()` / `t.Setenv`）。
- 不新增依赖；注释与命名风格照周边代码（解释“为什么”，不写“改了什么”）。
- **不要跑 `make ui` / 不要改 `pkg/gateway/dist`**（嵌入的 UI 由 owner 的发布流程重建）；`frontend/tsconfig*.tsbuildinfo` 被 git 跟踪：跑 `vue-tsc` 前后各看一次 `git status --short frontend/tsconfig.tsbuildinfo frontend/tsconfig.node.tsbuildinfo`，若由干净变为已修改，用 `git checkout -- <该文件>` 还原；若开始前就已修改，不要动它。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|------|------|----------|
| 依赖库 | `scripts/fetch-libtokenizers.sh` | exit 0 |
| run 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'ImageInputRefusal\|ImageTurnRefusal\|History' -count=1` | ok |
| state 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'CreateRun\|SessionBusy' -count=1` | ok |
| tui 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'DispatchSurfaceTurn' -count=1` | ok |
| gateway 包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | ok |
| 全量 Go | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 无 FAIL（已知偶发：gateway 的 `t.TempDir` 清理竞态，复跑一次通过即可，见 STOP） |
| vet / fmt | `go vet ./pkg/... ./cmd/...` ；`gofmt -l cmd pkg` | 无输出 |
| 架构 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | ok |
| 前端单测 | `cd frontend && pnpm test` | 全绿 |
| 前端类型 | `cd frontend && npx vue-tsc --noEmit -p tsconfig.json` | exit 0、无输出 |
| 浏览器验收 | `FOREBRAIN_E2E_SPEC=e2e/image-refusal.spec.ts scripts/acceptance/web_e2e.sh` | 末行 `web e2e: PASS` |
| 浏览器全套 | `scripts/acceptance/web_e2e.sh` | 末行 `web e2e: PASS` |

## Suggested executor toolkit

- `run-forebrain` 技能（`.claude/skills/run-forebrain/`）：Step 3 改完终端后，按技能里的 tmux 驱动方法复跑一次“纯文本模型 → 带图消息被拒且落库 → `/model` 切到支持图片的模型 → `continue` 收到原图”（假 provider，`FAKE_DUMP_DIR` 看请求体）。
- `frontend-design` 技能不需要：样式全部照设计稿与现有 `forebrain-err-block` / `forebrain-chip-menu`。

## Scope

**In scope**（只改这些文件）：
- `pkg/run/image_input.go`、`pkg/run/image_input_test.go`
- `pkg/state/runrt.go`、`pkg/state/runrt_test.go`
- `pkg/tui/chat_surface.go`、`pkg/tui/chat_session.go`、`pkg/tui/chat_surface_test.go`
- `pkg/turn/models.go`（只加 `ModelChoice.SupportsImages` 并在 `ModelChoices` 里赋值）
- `pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`
- `pkg/gateway/run_control.go`、`pkg/gateway/server.go`、`pkg/gateway/wsevents_test.go`
- `pkg/gateway/mention_test.go`（**2026-10-17 执行期补充**：`TestWebImageUploadRefusedOnTextOnlyModel` 钉住了 Step 5.1 要删掉的旧门禁行为——`prepareWebTurnInput` 在纯文本模型上返回错误。门禁移到调用方后该断言必然失败，按 Step 5.4 改成新语义；这是 Scope 漏项，不是放宽断言。）
- `frontend/src/lib/api.ts`（只加一个可选字段）
- `frontend/src/composables/useChatStream.ts`、`frontend/src/composables/useChatStream.test.ts`
- `frontend/src/components/chat/ModelChip.vue`、`frontend/src/components/chat/ModelChip.test.ts`（新建）
- `frontend/src/views/ChatView.vue`、`frontend/src/views/ChatView.test.ts`
- `frontend/src/locales/index.ts`（只加 4 个键 × 中英）
- `frontend/e2e/image-refusal.spec.ts`（新建）、`scripts/acceptance/web_e2e.sh`（只在假模式配置里加一个链条目）

**Out of scope**（不要碰）：
- `Runner.ImageInputRefusal` 的句子与行为（read_file / MCP 工具结果用它，测试钉着它的文案）。
- `sessionHistoryCarriesImage`、`rehydrateImageReferences`、`runnerFileResolver`、`turn.BuildSurfaceUserPartsJSON` 的 MIME 补齐——都是终端修复里已验证的部分。
- 终端 `/model` 选择器里的“支持图片”标记（owner 只要求 web；另立需求）。
- `pkg/gateway/channels.go`（Telegram/飞书等通道）——通道消息不带图片附件，不在本期。
- 把提示写进库（owner 决定不落库）。
- `pkg/gateway/dist/**`、`frontend/tsconfig*.tsbuildinfo`。
- 设计稿 `app-preview.html`（已由 owner 确认，不再改）。

## Steps

### Step 1：引擎——结构化的拒绝错误；按会话自己的模型判断

改 `pkg/run/image_input.go`：

1. 新增稳定码与错误类型（放在 `ImageTurnRefusal` 上方）：

```go
// ImageInputRefusedCode is the stable wire code of a turn refused because the
// session model cannot take an image the request would carry. A surface that
// renders the refusal in the viewer's language keys off it.
const ImageInputRefusedCode = "image_input_unsupported"

// ImageInputRefusedError is a turn refusal: the session model (Provider/Model)
// does not accept image input and the request would carry an image. The
// message it refused is kept; only the request is withheld.
type ImageInputRefusedError struct {
	Provider string
	Model    string
}

func (e *ImageInputRefusedError) Error() string {
	name := e.Model
	if e.Provider != "" {
		name = e.Provider + "/" + e.Model
	}
	return fmt.Sprintf("the session model %s does not accept image input and this conversation contains images; the message is kept, switch model with /model and continue", name)
}
```

2. `imageInputRefusedModel` 改为返回 `(provider, model string, refused bool)`，并且没有携带的会话选择时用 `r.effectiveModelFor(ctx)`（会话自己存的模型行 → runner 默认），不再用 `PrimaryModelSelection(r)`：

```go
func imageInputRefusedModel(ctx context.Context, r *Runner) (provider, model string, refused bool) {
	if r == nil {
		return "", "", false
	}
	// The turn carries the conversation's own model choice; outside a turn the
	// session's stored row decides (a web session may run on a model other
	// than the runner's default), then the runner's current selection.
	provider, model = r.effectiveModelFor(ctx)
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	if model == "" {
		return "", "", false
	}
	hit, ok := llm.Lookup(provider, model)
	if !ok || hit.SupportsAttachments {
		// Unknown model: fail open.
		return "", "", false
	}
	return provider, model, true
}
```

   `ImageInputRefusal` 改成用它拼原句（**句子逐字不变**）：

```go
func (r *Runner) ImageInputRefusal(ctx context.Context) string {
	provider, model, refused := imageInputRefusedModel(ctx, r)
	if !refused {
		return ""
	}
	name := model
	if provider != "" {
		name = provider + "/" + model
	}
	return fmt.Sprintf("the session model %s does not accept image input; switch model with /model, then read the image again", name)
}
```

3. `ImageTurnRefusal` 改为返回 `error`（nil = 放行），并在 ctx 没带会话 id 时补上，让 `effectiveModelFor` 读到这个会话的模型行：

```go
func ImageTurnRefusal(ctx context.Context, r *Runner, sessionID string, attachesImage bool) error {
	sid := strings.TrimSpace(sessionID)
	if sid != "" && strings.TrimSpace(llm.AgentSessionIDFromContext(ctx)) == "" {
		ctx = llm.WithAgentSessionID(ctx, sid)
	}
	provider, model, refused := imageInputRefusedModel(ctx, r)
	if !refused {
		return nil
	}
	if !attachesImage && !sessionHistoryCarriesImage(ctx, r, sid) {
		return nil
	}
	return &ImageInputRefusedError{Provider: provider, Model: model}
}
```

   保留 `ImageTurnRefusal` 上方原有的文档注释，把“returns a string”的意思改成“returns *ImageInputRefusedError, nil when allowed”。

4. 改 `pkg/run/image_input_test.go`：
   - `TestImageTurnRefusalCountsImagesAlreadyInTheConversation`：把 `got == ""` / `got != ""` 改成 `err == nil` / `err != nil`；“点名模型与 /model”的断言改用 `errors.As(err, &refused)` 后检查 `refused.Provider == "deepseek"`、`refused.Model == "deepseek-v4-pro"`、`strings.Contains(err.Error(), "/model")`。
   - 新增 `TestImageTurnRefusalJudgesTheSessionsOwnModel`：Runner 的配置链首项是**支持图片**的 `deepseek/deepseek-chat`（照 `TestImageInputRefusalFallsBackToPrimaryModel` 用 `newPrimaryModelRoot(primaryModelEntry(...), primaryModelEntry("deepseek", "deepseek-v4-pro", ...))` 配两条，并给 Runner 一个真 `SessionStore`，夹具照同文件 `TestImageTurnRefusalCountsImagesAlreadyInTheConversation`）；
     会话 `s1` 用 `SaveSessionModelSelection(ctx, "s1", state.SessionModelSelection{Provider: "deepseek", Model: "deepseek-v4-pro"})` 选纯文本模型；断言 `ImageTurnRefusal(context.Background(), r, "s1", true)` 返回 `*ImageInputRefusedError` 且 `Model == "deepseek-v4-pro"`；另一个未选模型的会话 `s2` 返回 nil（跟随 runner 默认、支持图片）。
     若 `ResolveSessionModelChoice` 在未 `Load` 的 Runner 上解析不了这条模型行（测试拿到 nil）→ 见 STOP 3。
   - 现有 `TestImageInputRefusalRejectsTextOnlyModels`、`TestImageInputRefusalAllowsUnknownModels`、`TestImageInputRefusalFallsBackToPrimaryModel` 必须不改断言照样通过。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'ImageInputRefusal|ImageTurnRefusal|History' -count=1` → ok；
`go build ./pkg/...` 此时会在 `pkg/tui` 报类型错误（`string` vs `error`）——这是预期，Step 3 修。

### Step 2：状态库——只查不建的“会话忙”分类

改 `pkg/state/runrt.go`：

1. 把 `CreateRun` 里插入 0 行后的那段分类（`:260 if n, _ := res.RowsAffected(); n == 0 {` 块内、`:271-295`：查阻塞者 status/owner/ownerLive，按 waiting/elsewhere 返回四个错误之一）原样抽成私有方法：

```go
// liveRunRefusal names the live primary run blocking a new turn of the
// session, as one of the ErrSessionBusy family; nil when there is none.
func (s *RunStore) liveRunRefusal(ctx context.Context, sessionID string, now time.Time) (refusal error, err error)
```

   查询 SQL 与判定逻辑**逐字搬移**，只把“查不到行（`sql.ErrNoRows`）”改为返回 `(nil, nil)`。
2. `CreateRun` 在插入 0 行时调用它：返回的 `refusal` 非 nil 就返回它；为 nil（阻塞者在两条语句之间结束）仍返回 `ErrSessionRunning`——与现行为一致。
3. 新增导出方法：

```go
// SessionBusy reports, without creating anything, whether a new turn of the
// session would be refused right now: the same refusal CreateRun returns, or
// nil when the session is free. A surface that keeps a message without
// starting a run (an image refusal) asks this first, so the message never
// lands in a conversation another surface is running.
func (s *RunStore) SessionBusy(ctx context.Context, sessionID string) error
```

   实现：`nil` 库或空 sid → 返回 nil；否则 `refusal, err := s.liveRunRefusal(ctx, sid, time.Now())`，`err != nil` 返回 `err`，否则返回 `refusal`。
4. 测试（`pkg/state/runrt_test.go`，照 `TestCreateRunNamesTheSurfaceHoldingTheSession` 与 `TestCreateRunRefusesASessionParkedOnApproval` 的夹具）：新增 `TestSessionBusyMatchesCreateRunWithoutCreating`：
   空会话 → nil；另一个 owner（持有租约）建了运行中的 run → `errors.Is(err, ErrSessionRunningElsewhere)`，且 `fb_runs` 行数不变；本 owner 自己有一个等审批的 run → `ErrSessionAwaitingApproval`；租约过期的运行中 run → nil。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'CreateRun|SessionBusy' -count=1` → ok（既有 `TestCreateRun*` 全部不改照样通过）。

### Step 3：终端——跟上错误类型，被拒前先查“会话忙”

1. `pkg/tui/chat_surface.go:171`：`imageRefusal` 现在是 `error`，调用点不变，只是类型变了。
2. `pkg/tui/chat_session.go`：
   - `dispatchUserTurnContent` 参数 `imageRefusal string` 改为 `imageRefusal error`；`:2007` 改为 `if imageRefusal == nil {`。
   - 在 `turn.PersistUserTurn`（`:2055`）**之前**插入：

```go
	if imageRefusal != nil && s.runSvc() != nil {
		// A refused message is kept without starting a run, so the run row's
		// own busy check never sees it: ask the store first, and refuse the
		// message the way a busy session refuses any message rather than write
		// it into a conversation another surface is running.
		if err := s.runSvc().SessionBusy(durable, sessionID); err != nil {
			return err
		}
	}
```

   - `:2065-2072` 改为 `if imageRefusal != nil { …原注释… return imageRefusal }`（删掉 `errors.New`；若 `errors` 包因此不再被引用，按编译器提示移除导入——但 `chat_session.go` 别处大概率仍在用）。
   - `pkg/tui/run.go` 不用改：返回的 `ErrSessionBusy` 家族错误已由 `run.go` 的 `errors.Is(err, statepkg.ErrSessionBusy)` 分支处理（撤回消息 + “session busy” 帧）；拒绝错误仍由 `renderer.PrintError(err)` 打印 `err.Error()`。
3. `pkg/tui/chat_surface_test.go`：
   - `TestDispatchSurfaceTurnKeepsImageMessageRefusedByTextOnlyModel`：两处 `require.Contains(t, err.Error(), "does not accept image input")` 之外，加 `var refused *run.ImageInputRefusedError; require.ErrorAs(t, err, &refused)`。
   - 新增 `TestDispatchSurfaceTurnDoesNotKeepARefusedMessageInABusySession`：夹具同上（`setAttachment(false)` 那段 catalog 替换照抄）；另一个 owner 持租约建运行中 run（照 `pkg/gateway/wsevents_test.go:1371-1377`，库用 `s.sqlDB()`，本会话的 RunStore 是 `s.runSvc()`）；
     带图发送 → `require.ErrorIs(t, err, state.ErrSessionBusy)`；`captureCharacterization(t, s, sid).Messages` 为空；`model.lastReceived()` 为空。

**Verify**：`go build ./pkg/...` 无错；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'DispatchSurfaceTurn' -count=1` → ok。
（可选但推荐：按 run-forebrain 技能真机复跑一次终端流程，确认行为与改动前一致。）

### Step 4：会话模型选项带“是否支持图片”

1. `pkg/turn/models.go`：`ModelChoice` 加字段（放在 `CanReason` 下面）：

```go
	// SupportsImages reports that the catalog knows this model accepts image
	// input. Unknown models report false: a picker marks only what it knows.
	SupportsImages bool
```

   `ModelChoices` 里在 `choice.CanReason = hit.CanReason` 下加 `choice.SupportsImages = hit.SupportsAttachments`。
2. `pkg/gateway/api_extra.go`：`sessionModelOption` 加 `SupportsImages bool \`json:"supports_images"\``；新增

```go
// sessionModelSupportsImages reports whether one configured option accepts
// image input: the shared catalog listing's record for it when there is one
// (models.json, or the ChatGPT account's input modalities), else the
// configured choice's own catalog entry.
func sessionModelSupportsImages(records []turn.ModelRecord, choice turn.ModelChoice) bool
```

   匹配规则与 `sessionModelEfforts` 相同（`strings.EqualFold(rec.Provider, provider) && strings.TrimSpace(rec.APIModel) == model`）；匹配到就返回 `rec.SupportsAttachments`，否则返回 `choice.SupportsImages`。在 `sessionModelOptions` 里给每个选项赋值。
3. `pkg/gateway/api_extra_test.go` 的 `TestChatSessionModelEndpoints`：配置链加第三条 `{Provider: "deepseek", Model: "deepseek-v4-pro", APIKey: "${FOREBRAIN_WEB_MODEL_TEST_KEY}", BaseURL: "http://c.test/v1"}`；
   `got.Options` 的匿名结构体加 `SupportsImages bool \`json:"supports_images"\``；把 `require.Len(t, got.Options, 2)` 改成 3；新增断言 `[true, true, false]` 依次对应 glm-5.3-flash、deepseek-chat、deepseek-v4-pro；
   `got.Options[2].Efforts` 等于 `[]string{"low","medium","high","xhigh"}`（v4-pro 可推理）。其余断言不动。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestChatSessionModelEndpoints' -count=1` → ok；
`CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -count=1` → ok。

### Step 5：gateway——被拒的消息落库、回 `turn_refused`，不建 run

1. `pkg/gateway/run_control.go`：
   - `prepareWebTurnInput` 删掉旧门禁（`:501-508` 的注释与 `imageRefusal` 变量、`:527-529`、`:573-575` 两处 `return … errors.New(imageRefusal)`）。其余逻辑一行不动。若 `errors` 包不再被本文件引用，移除导入。
     同时把函数上方文档注释里“an image … is refused”之类的句子删掉（若有）；门禁现在在调用方。
   - 新增（放在 `prepareWebTurnInput` 上方）：

```go
// webMessageAttachesImage reports whether the message shows the model an
// image of its own: an image the @ picker attached, or an upload stored as an
// image. It only reads what the uploads were recorded as; a file that cannot
// be made available is prepareWebTurnInput's to report.
func (s *Server) webMessageAttachesImage(ctx context.Context, uploads, mentionImages []string) bool {
	for _, picked := range mentionImages {
		if strings.TrimSpace(picked) != "" {
			return true
		}
	}
	if s.Files == nil || s.Sessions == nil {
		return false
	}
	for _, fid := range uploads {
		fid = strings.TrimSpace(fid)
		if fid == "" {
			continue
		}
		if rec, err := s.Files.Get(ctx, s.Sessions.AgentID(), fid); err == nil && rec != nil &&
			strings.HasPrefix(strings.TrimSpace(rec.MediaType), "image/") {
			return true
		}
	}
	return false
}
```

2. `pkg/gateway/server.go`（`HandleChatWS` 内）：
   - 在 `turnRunner` 解析成功之后（`:1722` 的 `continue` 之后、`runStart := time.Now()` 之前）计算门禁：

```go
		// The same gate the terminal applies (run.ImageTurnRefusal), judged on
		// this conversation's own runner and model: a text-only model refuses a
		// turn whose request would carry an image, attached now or already in
		// the conversation. The message is still kept; only the request is
		// withheld.
		imageRefusal := run.ImageTurnRefusal(r.Context(), turnRunner, sid, s.webMessageAttachesImage(r.Context(), attachments, mentionImages))
```

   - 预压缩只在未被拒时跑：把 `cerr := s.autoCompactBeforeUserAppend(preflightCtx, sid, "webchat", content, rawContentForRetrieval)` 改为

```go
		var cerr error
		if imageRefusal == nil {
			// A refused message sends no request, so there is nothing to make
			// room for; the next turn compacts as usual.
			cerr = s.autoCompactBeforeUserAppend(preflightCtx, sid, "webchat", content, rawContentForRetrieval)
		}
```

     其余预压缩/撤回逻辑不动。
   - 在 `prepareWebTurnInput` 成功之后（`:1851` 的 `}` 之后）、`runID := …`（`:1858`）之前插入被拒分支：

```go
		if imageRefusal != nil {
			// A refused message is kept without a run, so the run row's busy
			// check never sees it: a session another surface is running refuses
			// it the way it refuses any message, back to the composer.
			if s.RunRT != nil {
				if busy := s.RunRT.SessionBusy(r.Context(), sid); busy != nil {
					withdrawn := wsServerMsg{Op: "turn_withdrawn", RequestID: m.RequestID, SessionID: sid, Error: busy.Error()}
					if code := state.SessionBusyCode(busy); code != "" {
						withdrawn.Data = map[string]any{"code": code}
					}
					writeMsg(withdrawn)
					continue
				}
			}
			if s.Sessions != nil {
				if _, err := turn.PersistUserTurn(r.Context(), s.Sessions, turn.UserTurn{
					SessionID:  sid,
					ModelInput: turnInput.text,
					RawInput:   firstNonBlank(rawContentForRetrieval, turnInput.display),
					PartsJSON:  turnInput.partsJSON,
				}); err != nil {
					writeMsg(wsServerMsg{Op: "turn_withdrawn", RequestID: m.RequestID, SessionID: sid, Error: err.Error()})
					continue
				}
			}
			refused := wsServerMsg{Op: "turn_refused", RequestID: m.RequestID, SessionID: sid, Error: imageRefusal.Error()}
			var detail *run.ImageInputRefusedError
			if errors.As(imageRefusal, &detail) {
				refused.Data = map[string]any{"code": run.ImageInputRefusedCode, "provider": detail.Provider, "model": detail.Model}
			}
			writeMsg(refused)
			wsRequestCacheSet(requestCache, m.RequestID, refused, 2*time.Minute, time.Now())
			continue
		}
```

     （`turn.PersistUserTurn` 的签名是 `(int64, error)`；正常路径 `:1927` 不看返回值，被拒路径要看，因为写不进去就不能说“已保留”。）
3. 测试（`pkg/gateway/wsevents_test.go`，夹具照 `TestHandleChatWSSessionBusyWithdrawsTheMessage`，`:1362`）：
   - `TestHandleChatWSKeepsAMessageTheModelCannotTakeAndRefusesIt`：
     配置链首项 `deepseek/deepseek-v4-pro`（纯文本）；`runner := &run.Runner{Deps: &run.Deps{Home: home, AgentName: "main", AppCfg: &cfg, SessionStore: sessions}, FileResolver: run.PathResolver{}}`；`s := &Server{Home: home, Sessions: sessions, Runner: runner, RunRT: &state.RunStore{DB: db, Owner: "gateway-test"}}`；
     先用 `sessions.AppendStructuredMessage` 写一条带图片 `file_reference` 的用户行（写法照 `pkg/run/image_input_test.go` 的 `TestImageTurnRefusalCountsImagesAlreadyInTheConversation`：真 1×1 PNG 写进 `t.TempDir()`，`state.FileReferencePartJSON(img, "clip.png", "image/png")`）；
     发送纯文本 `continue` → 读到 `op == "turn_refused"`，**过程中不得出现** `run_started` / `run_error` / `run_completed` / `turn_withdrawn`；
     断言 `msg.Data == map[string]any{"code": "image_input_unsupported", "provider": "deepseek", "model": "deepseek-v4-pro"}`、`msg.Error` 含 `does not accept image input`；
     `sessions.ListAllMessages(ctx, sid, 0)` 有 2 行用户消息，第二行内容是 `continue`；`SELECT COUNT(*) FROM fb_runs` 为 0。
     再用**同一个 `request_id`** 重发一次：仍收到 `turn_refused`，库里仍是 2 行（缓存去重生效）。
   - `TestHandleChatWSRefusedImageMessageInABusySessionIsWithdrawn`：同上夹具，再加另一个 owner 的运行中 run（照 `:1371-1377`）；发送 `continue` → `turn_withdrawn`，`Data == {"code": "session_running_elsewhere"}`，库里仍只有那 1 行种子消息。
   - `TestWebMessageAttachesImage`（纯函数级）：`s.Files = &state.FileStore{DB: db, Home: t.TempDir(), Cfg: state.LoadConfigFromEnv()}`（照 `project_dirs_test.go:36`）；用 `s.Files.CreateFromReader(ctx, sid, "shot.png", bytes.NewReader(png), int64(len(png)), "image/png")` 与一个 `"report.pdf"`/`"application/pdf"` 记录；
     断言：mention 非空 → true；只有 png 上传 → true；只有 pdf 上传 → false；不存在的 id → false；全空 → false。

4. **（2026-10-17 执行期补充）** `pkg/gateway/mention_test.go` 的
   `TestWebImageUploadRefusedOnTextOnlyModel`（`:385-441`）断言的是旧语义：它要求
   `s.prepareWebTurnInput(ctx, "s1", "look", []string{photo.ID}, nil)` 在纯文本模型上返回错误。
   门禁挪到调用方后这条必然失败，按新语义重写（夹具、`pngTestHeader`、catalog 说明全部照旧）：

```go
// TestWebImageGateRefusesTheTurnNotTheAttachment pins the split the web now
// shares with the terminal: preparing a message attaches an image upload
// whatever the model is — the row it becomes keeps the picture, so switching
// model later still sends it — and the turn gate refuses only the request,
// naming the model and pointing at /model. A non-image upload is not the
// image gate's business.
func TestWebImageGateRefusesTheTurnNotTheAttachment(t *testing.T) {
	// …夹具与原用例逐字相同（home/workspace、state.OpenStateForTest、files、
	// sessions.Ensure("s1")、projects.Create + BindSession、report.pdf、photo.png、
	// cfg 里 deepseek-v4-pro 的 runner、s := &Server{Home, Files, Sessions, Projects, Runner}）…

	// The attachment step no longer judges the model: the picture is attached
	// and recorded as the reference a later request replays.
	prepared, err := s.prepareWebTurnInput(ctx, "s1", "look", []string{photo.ID}, nil)
	if err != nil {
		t.Fatalf("an image upload must still attach on a text-only model: %v", err)
	}
	if !strings.Contains(prepared.partsJSON, state.PartTypeFileReference) || !strings.Contains(prepared.partsJSON, "image/png") {
		t.Fatalf("prepared parts = %q, want the image kept as a file reference with its MIME type", prepared.partsJSON)
	}

	// The turn gate refuses the request, not the message.
	refusal := run.ImageTurnRefusal(ctx, runner, "s1", s.webMessageAttachesImage(ctx, []string{photo.ID}, nil))
	if refusal == nil {
		t.Fatal("expected the turn gate to refuse an image upload on a text-only model")
	}
	if !strings.Contains(refusal.Error(), "deepseek-v4-pro") || !strings.Contains(refusal.Error(), "/model") {
		t.Fatalf("refusal = %q, want it to name the model and point at /model", refusal)
	}
	var detail *run.ImageInputRefusedError
	if !errors.As(refusal, &detail) || detail.Provider != "deepseek" || detail.Model != "deepseek-v4-pro" {
		t.Fatalf("refusal = %#v, want the typed refusal naming deepseek/deepseek-v4-pro", refusal)
	}

	// A non-image upload attaches and is not the image gate's business.
	if _, err := s.prepareWebTurnInput(ctx, "s1", "look", []string{report.ID}, nil); err != nil {
		t.Fatalf("non-image upload must not be refused: %v", err)
	}
	if run.ImageTurnRefusal(ctx, runner, "s1", s.webMessageAttachesImage(ctx, []string{report.ID}, nil)) != nil {
		t.Fatal("a message carrying no image must not be refused by the image gate")
	}
}
```

   若文件缺少 `errors` 导入，按编译器提示加上（该文件已导入 `bytes`、`state`、`run`、`strings`）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → ok（如遇已知 `t.TempDir` 清理竞态的 flake，单独复跑失败用例一次；仍失败 → STOP）。

### Step 6：前端——模型菜单标“支持图片”，菜单可从外部打开

1. `frontend/src/lib/api.ts:913`：`SessionModelOption` 加 `supportsImages?: boolean`（加注释：catalog 已知支持图片时为 true）。
2. `frontend/src/components/chat/ModelChip.vue`：
   - 选项按钮的 `<b>` 里、模型名之后加 `<span v-if="option.supportsImages" class="forebrain-chip-menu-badge" data-testid="model-supports-images">{{ t('chat.modelAcceptsImages') }}</span>`。
     **1:1 还原设计稿（owner 2026-10-17 重申）**：设计稿里这一行是
     `<b>` 里依次是「选中时的 check 图标 + 模型名 + 徽标」，徽标取 `.badge.ok` 的底
     （`--success-soft` 背景 / `--success` 文字：本仓库是 `--forebrain-success-soft` / `--forebrain-success`），
     并叠加 `.model-menu .badge` 的 `margin-left:auto; height:18px; font-weight:500`
     （`<b>` 本身是 `display:flex; align-items:center; gap:6px`，所以 `margin-left:auto` 把徽标顶到最右）。
     完整徽标样式：`display:inline-flex; align-items:center; margin-left:auto; height:18px; padding:0 7px;
     border-radius:999px; font-size:11px; font-weight:500; white-space:nowrap;
     background:var(--forebrain-success-soft); color:var(--forebrain-success);`。
     **`frontend/src/assets/main.css` 不在 Scope 内**：样式写在 `ModelChip.vue` 的 `<style scoped>` 里，用
     `data-testid="model-supports-images"` 定位（scoped 属性会自动加上，不需要额外的 class 名约定）。
   - 暴露打开方法：`function openMenu() { if (!props.disabled && !switching.value) open.value = true }`，`defineExpose({ openMenu })`。
     注意 `onDocPointerDown` 会在外部 pointerdown 时关菜单：外部按钮在 **click** 里调用 `openMenu()`（pointerdown 先发生、click 后发生），所以能打开；不要改 `onDocPointerDown`。
3. 新建 `frontend/src/components/chat/ModelChip.test.ts`（照 `ComposerBar.test.ts` 的结构：`beforeAll(() => setLocale('zh'))`、`mount`）：
   - 选项 `[{provider:'deepseek', model:'deepseek-v4-pro', supportsImages:false,…}, {provider:'deepseek', model:'deepseek-v4-flash', supportsImages:true,…}]`；点 `[data-testid="model-chip"]` 打开；
     `[data-testid="model-option-deepseek-v4-flash"]` 内有 `支持图片`，`model-option-deepseek-v4-pro` 内没有 `[data-testid="model-supports-images"]`。
   - 通过 `wrapper.vm.openMenu()` 打开后菜单可见；`disabled: true` 时调用它菜单不出现。

**Verify**：`cd frontend && pnpm test -- ModelChip` → 全绿。

### Step 7：前端——`turn_refused`：气泡保留、提示块、不退回输入框

1. `frontend/src/composables/useChatStream.ts`：
   - `ChatMessage` 加字段（放在 `picked` 之后）：

```ts
  /** A message the session model could not take: the notice under it says
   * which model refused it. Live only — a reload draws the message alone,
   * the way the terminal does (the refusal is not stored). */
  imageRefusal?: { provider: string; model: string }
```

   - 在 `handleOp` 里、`turn_withdrawn` 分支之前加 `turn_refused` 分支（照 `slash_reply` 分支的写法）：

```ts
        if (op === 'turn_refused') {
          // The session model cannot take an image this turn would carry. The
          // message stays in the conversation — it is stored — and the
          // placeholder becomes the notice saying so; nothing goes back to the
          // composer. A reload shows the message alone, as the terminal does.
          const data = payload.data && typeof payload.data === 'object' ? payload.data as Record<string, unknown> : {}
          updateMessageById(assistantMessageId, () => ({
            id: assistantMessageId,
            role: 'notice' as const,
            content: '',
            imageRefusal: { provider: String(data.provider ?? '').trim(), model: String(data.model ?? '').trim() },
          }))
          eventState.completedNormally = true
          if (streamAbortController === myController) {
            isStreaming.value = false
          }
          return { terminal: true }
        }
```

     若 `data.model` 为空（旧后端），`model` 回退为 `String(payload.error ?? '')` 不合适——**保持空串**，由 ChatView 用“当前模型”兜底（见下）。
   - 收尾的 `hasAssistantData`（`:5025` 起）加一项 `Boolean(currentAssistant?.imageRefusal)`，否则占位会被删。
   - 确认 `turn_refused` **不**设 `withdrawn`，从而不会 `giveBack` 草稿。
2. `frontend/src/views/ChatView.vue`：
   - 在 `:117` 的 notice 卡片**之前**加被拒提示块（与忙碌提示同一组件写法）：

```vue
                  <div
                    v-if="msg.role === 'notice' && msg.imageRefusal"
                    class="forebrain-err-block"
                    role="alert"
                    data-testid="image-refused-notice"
                  >
                    <ImageOff class="size-3.5" aria-hidden="true" />
                    <div>
                      <b>{{ msg.imageRefusal.model || sessionModelData?.model || '' }}</b>
                      {{ t('chat.imageRefusedLine') }}
                      <span class="forebrain-err-s">
                        {{ t('chat.imageRefusedRecover') }}
                        <button type="button" class="rewind-undo" data-testid="image-refused-switch" @click="modelChipRef?.openMenu()">{{ t('chat.imageRefusedSwitch') }}</button>
                      </span>
                    </div>
                  </div>
```

     **1:1 还原设计稿（owner 2026-10-17 重申）**：设计稿 `imgRefused()` 是
     `<div class="err-block">{image-off}<div><b>{model}</b> 不支持图片，消息已保留，未发送。<span class="s">对话里有图片，换支持图片的模型后继续。<button class="btn link" style="font-size:12px">换模型</button></span></div></div>`——
     模型名是**加粗的**，主句与灰色小字分两行，和现有 `.forebrain-err-block` / `.forebrain-err-s`
     完全同构（该类就是照这张设计稿的 `.err-block` 写的），所以只用这一套现成类，不要新造样式。
     因此**模型名不进 i18n 字符串**：`<b>` 包住模型名，`{{ t('chat.imageRefusedLine') }}` 只翻译主句
     （渲染出的整句与 owner 认的文案逐字一致：`deepseek-v4-pro 不支持图片，消息已保留，未发送。`）。
     `<b>` 后必须**恰好一个空格**再跟翻译文本——两者写在同一行，避免模板压缩空白导致纯文本断言变成 `deepseek-v4-pro不支持图片`。
     链接按钮用现有 `.rewind-undo`（12px、品牌色、无边框、hover 下划线），等价于设计稿的 `.btn.link`，不要新样式。

     并把原 notice 卡片的 `v-if="msg.role === 'notice'"` 改成 `v-else-if="msg.role === 'notice'"`。
   - `ModelChip` 上加 `ref="modelChipRef"`；`<script setup>` 里 `const modelChipRef = ref<InstanceType<typeof ModelChip> | null>(null)`；lucide 导入行（`:543`）加 `ImageOff`。
   - 不改其它渲染：用户气泡与附件 chip 照常显示（它们本来就在）。
3. `frontend/src/locales/index.ts`：中文块加

```ts
  'chat.imageRefusedLine': '不支持图片，消息已保留，未发送。',
  'chat.imageRefusedRecover': '对话里有图片，换支持图片的模型后继续。',
  'chat.imageRefusedSwitch': '换模型',
  'chat.modelAcceptsImages': '支持图片',
```

   英文块加

```ts
  'chat.imageRefusedLine': 'does not accept images. The message is kept and was not sent.',
  'chat.imageRefusedRecover': 'This conversation has images. Switch to a model that accepts images, then continue.',
  'chat.imageRefusedSwitch': 'Switch model',
  'chat.modelAcceptsImages': 'Accepts images',
```

4. 测试：
   - `frontend/src/composables/useChatStream.test.ts`，照 `:640` 附近“hands a withdrawn message back …”的 FakeChatWebSocket 写法新增 `it('keeps a message the model cannot take and draws the refusal under it')`：
     带一个图片附件发送 → `socket.message({ op: 'turn_refused', request_id, session_id: 's-img', error: '…', data: { code: 'image_input_unsupported', provider: 'deepseek', model: 'deepseek-v4-pro' } })`；
     断言：用户气泡仍在且 `attachments` 不变；下一条消息 `role === 'notice'`、`imageRefusal.model === 'deepseek-v4-pro'`；`stream.takeReturnedDraft()` 为 `null`；`isStreaming` 为 false。
   - `frontend/src/views/ChatView.test.ts` 新增 `it('draws an image refusal under its message and opens the model menu from it')`：
     `fake.messages.value = [{id:'u1', role:'user', content:'这个 401 是哪一步报的？\n[Image #1]', attachments:[{path:'shot.png', name:'shot.png', mediaType:'image/png'}]}, {id:'n1', role:'notice', content:'', imageRefusal:{provider:'deepseek', model:'deepseek-v4-pro'}}]`；
     断言 `[data-testid="image-refused-notice"]` 文本含 `deepseek-v4-pro does not accept images`（该文件 `setLocale('en')`）；`ModelChip` 若因 `sessionModelData` 为空未渲染，就 `vi.spyOn(forebrainApi, 'sessionModel').mockResolvedValue({provider:'deepseek', model:'deepseek-v4-pro', effort:'', options:[…]})` 让它渲染，然后点 `[data-testid="image-refused-switch"]`，断言 `[data-testid="model-option-deepseek-v4-pro"]` 可见。

**Verify**：`cd frontend && pnpm test` → 全绿；`cd frontend && npx vue-tsc --noEmit -p tsconfig.json` → exit 0、无输出（按“房规”处理 tsbuildinfo）。

### Step 8：浏览器验收（真 gateway + 假 provider）

1. `scripts/acceptance/web_e2e.sh` 假模式配置（`:141-155` 的 `llm_providers`）末尾加第三条（注释说明用途）：

```yaml
      # A text-only model (catalog attachment: false) for the image-refusal
      # spec; the chain never reaches it while the first entry works.
      - provider: deepseek
        model: deepseek-v4-pro
        api_key: \${FAKE_LLM_KEY}
        base_url: http://127.0.0.1:$LLM_PORT
```

2. 新建 `frontend/e2e/image-refusal.spec.ts`：
   - `test.skip(process.env.E2E_REAL_LLM === '1', 'needs the fake chain with a text-only model')`；`page.addInitScript(() => localStorage.setItem('forebrain-locale', 'zh'))`；`signIn`；`seedProjectSession`；`page.goto('/?session=…')`。
   - 点模型 chip，选 `[data-testid="model-option-deepseek-v4-pro"]`（照 `chat-composer.spec.ts:29-31`）；菜单里 `deepseek-chat` 那一项含 `支持图片`，`deepseek-v4-pro` 那一项不含。
   - 照 `attachments-save.spec.ts:34-45` 附一张 PNG（在“保存到”弹窗直接点 `[data-testid="attach-confirm"]`），输入 `这个 401 是哪一步报的？` 发送。
   - 断言：出现 `[data-testid="image-refused-notice"]`，文本含 `deepseek-v4-pro 不支持图片，消息已保留，未发送。`；用户气泡与附件 chip 仍在；输入框为空。截图 `shot(page, '070-image-refused')`。
   - 再发送 `继续` → 出现第二个提示块；截图 `070-image-refused-again`。
   - 点最后一个提示块里的 `[data-testid="image-refused-switch"]` → 模型菜单打开；选 `deepseek-chat` → 再发 `继续` → `expectAssistantReply(page)`（`support.ts:109`）。截图 `070-image-after-switch`。
   - 刷新页面（`page.reload({ waitUntil: 'networkidle' })`）→ `[data-testid="image-refused-notice"]` 数量为 0，三条用户消息都在。
   - 查库（照 `session-busy.spec.ts` 的 `stateDb()` 定位库文件，用 `execFileSync('sqlite3', [db, sql])` 读）：
     `SELECT content, parts FROM fb_messages WHERE session_id='<会话>' AND role='user' AND visibility='visible' ORDER BY id`。
     库里可能还有环境上下文这类 `role='user'` 的元数据行，所以**只看 content 以三句原话开头的行**：恰好 3 行，依次是 `这个 401 是哪一步报的？…`、`继续`、`继续`；
     第一行的 `parts` 含 `"type":"file_reference"` 与 `"mime_type":"image/png"`。
3. 跑：`FOREBRAIN_E2E_SPEC=e2e/image-refusal.spec.ts scripts/acceptance/web_e2e.sh` → `web e2e: PASS`；再跑全套 `scripts/acceptance/web_e2e.sh` → `web e2e: PASS`（确认新加的链条目没有打破其它用例）。

**Verify**：两次运行末行都是 `web e2e: PASS`；`$TMPDIR/forebrain-web-e2e/shots/` 下有 3 张 `070-*.png`，与设计稿“图片 · 模型不支持 / 没换模型再发 / 换模型后继续”三个状态一致。

### Step 9：全量闸门

依次跑：全量 Go、vet/fmt、架构、前端单测与类型（见 Commands）。

**Verify**：全部按预期；`git status --short` 只列出 Scope 内的文件（外加开工前就已修改的文件，不要动它们）。

## Test plan

| 层 | 文件 | 用例 |
|----|------|------|
| 引擎 | `pkg/run/image_input_test.go` | 错误类型与码；**会话自己的模型行**决定门禁（runner 默认支持图片、会话选了纯文本 → 拒绝；未选的会话 → 放行）；原有三条不改断言 |
| 状态库 | `pkg/state/runrt_test.go` | `SessionBusy`：空闲 nil / 另一端运行中 / 本端等审批 / 租约过期 nil；不建行；既有 `CreateRun*` 全过 |
| 终端 | `pkg/tui/chat_surface_test.go` | 被拒错误可 `errors.As`；另一端运行中时带图消息按“忙”撤回、不落库、不发请求 |
| gateway | `pkg/gateway/api_extra_test.go` | `supports_images`：true/true/false |
| gateway | `pkg/gateway/wsevents_test.go` | 历史有图 + 纯文本模型：`turn_refused`（码/provider/model）、消息落库、无 run、同 request_id 重发不重复落库；会话忙：`turn_withdrawn` + 忙码、不落库；`webMessageAttachesImage` 五种输入 |
| 前端 | `ModelChip.test.ts`（新） | 徽标只出现在支持图片的模型；`openMenu()` 打开、disabled 时不开 |
| 前端 | `useChatStream.test.ts` | `turn_refused`：气泡与附件保留、notice 带 `imageRefusal`、不退回草稿、停止流式 |
| 前端 | `ChatView.test.ts` | 提示块文案（en）；“换模型”打开模型菜单 |
| 浏览器 | `frontend/e2e/image-refusal.spec.ts`（新） | 设计稿 4 个状态的完整走查 + 刷新后提示消失、消息仍在 + 库里图片引用带 MIME |

## Done criteria

- [ ] `grep -n 'func ImageTurnRefusal(ctx context.Context, r \*Runner, sessionID string, attachesImage bool) error' pkg/run/image_input.go` 有 1 行
- [ ] `grep -n 'ImageInputRefusal' pkg/gateway/run_control.go` 无输出（gateway 不再用工具门禁拦消息）
- [ ] `grep -n '"turn_refused"' pkg/gateway/server.go` 有 1 行；`grep -n "'turn_refused'" frontend/src/composables/useChatStream.ts` 有 1 行
- [ ] `grep -n 'func (s \*RunStore) SessionBusy' pkg/state/runrt.go` 有 1 行
- [ ] `grep -n 'supports_images' pkg/gateway/api_extra.go` 有 1 行
- [ ] 全量 Go 测试无 FAIL；`go vet` 与 `gofmt -l cmd pkg` 无输出；`pkg/architecture` ok
- [ ] `cd frontend && pnpm test` 全绿；`npx vue-tsc --noEmit -p tsconfig.json` exit 0
- [ ] `FOREBRAIN_E2E_SPEC=e2e/image-refusal.spec.ts scripts/acceptance/web_e2e.sh` 与全套 `scripts/acceptance/web_e2e.sh` 末行都是 `web e2e: PASS`
- [ ] 改动只在 Scope 列出的文件内；`pkg/gateway/dist/**` 无改动；没有 commit

## STOP conditions

停下报告，不要即兴发挥：

1. Drift check 任一条没有输出（终端侧修复不在工作区或已被改动）。
2. Step 1 改用 `effectiveModelFor` 后，`TestImageInputRefusalFallsBackToPrimaryModel` 或 `pkg/tool` / `pkg/mcp` 里与图片门禁相关的既有测试失败——说明有调用方依赖“只看 runner 默认”，需要 owner 判定。
3. `ResolveSessionModelChoice` 对未 `Load` 的 Runner 解析不了会话模型行，导致 Step 1 新测试拿不到拒绝（不要为了让测试过而去 `Load` 真 provider 或改 `effectiveModelFor`）。
4. Step 2 抽出 `liveRunRefusal` 后任何既有 `TestCreateRun*` 失败。
5. gateway 的 `wsevents_test` 新用例里出现 `run_started`（说明被拒分支没在建 run 之前拦住），或同 request_id 重发产生了第二行。
6. 浏览器全套里**别的** spec 因新增的第三个链条目失败（例如有用例断言模型选项个数）——报告是哪条，不要去改别的 spec。
7. 需要改 Scope 之外的文件才能完成（`main.css` 除外的样式需求按 Step 6 写进组件 scoped 样式）。
8. 已知 flake（gateway 的 `t.TempDir` 清理竞态）以外的失败，在合理修正后复跑两次仍失败。

## Maintenance notes

- **门禁唯一入口**：整轮消息的图片门禁只有 `run.ImageTurnRefusal`（终端、web 共用）；工具读图的门禁是 `Runner.ImageInputRefusal`（read_file / MCP）。以后新增能带图的入口（通道、定时任务的附件等）必须走前者，并照本计划“先查会话忙、再落库、再拒绝请求”的顺序。
- **模型判断**：两个门禁现在都按会话自己的模型（`effectiveModelFor`）判断；catalog 未知的模型一律放行（fail-open），菜单也不标“支持图片”。catalog 标错时修数据（`assets/models.json`），不要改门禁。
- **提示不落库**是 owner 的决定：刷新/重开后只剩消息。若将来要落库，需要新的行类型与回放规则，两端一起做。
- **协议**：新增 WS op `turn_refused`（`data.code = "image_input_unsupported"`, `provider`, `model`）。前后端随同一个包发布，`wsProtocolVersion` 不变；评审时确认没有其它客户端依赖“被拒即 `turn_withdrawn`”。
- 评审重点：gateway 被拒分支在 `CreateRun` 之前、且在 `prepareWebTurnInput` 之后（附件不可用时仍按原来退回输入框）；`hasAssistantData` 计入 `imageRefusal`；终端与 web 的忙检查都在落库之前。
- 未纳入：终端 `/model` 选择器标“支持图片”；通道消息的图片门禁。
