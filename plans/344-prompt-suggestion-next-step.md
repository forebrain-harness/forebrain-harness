# 344 — 空闲输入框的下一步推荐（prompt_suggestion），TUI + web 语义对齐

- **Status:** DONE / LANDED（owner 授权直接实施 + 落地；自评审通过；已 `git merge --ff-only` 回 `dev`，**未 push**）
- **Priority:** P2
- **Category:** feature / UX（严格对齐 Claude Code 2.1.322 的 prompt_suggestion 语义与算法）
- **Effort:** L
- **Risk:** MED（新增引擎组件 + 两端输入链路 + 配置 UI；不改既有回合/compaction/autoContinue 行为）
- **Planned at:** `4d65d015`（2026-11-21）
- **Implemented at:** worktree `.worktrees/344-prompt-suggestion`，分支 `344-prompt-suggestion`，基线 `4d65d015`
- **Landed at:** 2026-11-21，`dev` `1d717c84..372d4a1b`（ff-only，36 文件 +2078/−44，5 新文件），**未 push**（origin/dev 仍 `1d717c84`）。commit `372d4a1b` `feat(suggestion): idle-composer next-step ghost on TUI and web (plan 344)`。
  - **rebase 链**：先 rebase 到当时 dev `39569e96`（peer-letter+VSCode，零冲突）→ owner 告知 config 面板简化已提交并 push（dev 前进到 `1d717c84`）→ 再 rebase 到 `1d717c84`（**零冲突**，git 自动调和共享的 `TestConfigPanelPrototypeCopy`）。
  - **与 config 面板简化（`1d717c84`）的调和（已逐字核对）**：该提交移除 "File only" 页签 + gateway/credentials 两个 YAML-only 段 + YAMLOnly/showFileSection + "Configuration" 标题/副标题（−390 行，仅动 config_panel.go/schema.go/test.go）。合并后 `config_panel_schema.go` features 段保留我的 `prompt_suggestions` 字段 + Summary "Enable memory, language intelligence and next-step suggestions."，而 YAMLOnly/configTabFile/gateway/credentials 均已删；`config_panel_test.go` `TestConfigPanelPrototypeCopy` summaries map 含我的 features 文案、gateway/credentials 已删，fieldCopy 含 memories + prompt_suggestions 两条。双方改动完整共存。
  - **门证据**：①v4 全量串行（孤立基线 `4d65d015`）**39 ok／0 FAIL**、gofmt 净、vet exit 0（architecture/assembly/gateway/process/run/tui/turn 全 ok）。②v5 全量（最终态 `372d4a1b`）跑到 **24 ok／0 FAIL**（含 architecture/assembly/gateway/event/config/agent）时 **owner 手动停止并指示「跳过这步直接继续」**，故 process/run/state/tool/tui 全量未在最终态跑完。③最终态聚焦 sanity：`pkg/tui -run TestConfigPanel` ok 1.067s、`pkg/turn` 全量 ok 4.043s。④合并后主树复跑：`go build ./...` exit 0、`pkg/architecture` ok 2.056s、gofmt 净。⑤rebase 后合并态 web `typecheck` exit 0（config 面板提交未动 web，web 文件与先前 web_gates_v2 〔194 文件/1569 测试/build ✓〕一致）。
  - **如实留痕（未跑的门）**：最终态的**全量 go 串行**（owner 指示跳过）与**全量 web test/build**未在 `372d4a1b` 上重跑；真实模型回合的端到端幽灵浏览器验证未做（无 live 模型不画），以组件级单测 + push-only 事件单测 + gateway REST 单测 + 引擎单测 + 原型 Playwright 键盘校验覆盖。
- **Depends on:** 无
- **Prototype（web 实施门，已按 as-built 刷新）:** [344-prompt-suggestion-prototype.html](344-prompt-suggestion-prototype.html)
  - owner 已于 2026-11-21 确认原型并裁决语义项（见「已裁决」）；其后 owner 追加「严格对齐 CC 真实算法」「删除未采纳提醒」「开关入两端配置 UI」三项裁决，原型已据此刷新（v02：阈值 20、静默降频 1/10、无 notice、加权词数、配置开关场景）。
  - 冻结 sha256（刷新后）：
    - html `21877ec9c1301f571211e06b17261acde7195e3e5a82de4c25666c0a3065e816`
    - test `15470054322db5594f05dbe01119b0a6623dd1fe6634ce46f4f4b4cd099432b3`
  - 回归命令：`node plans/344-prompt-suggestion-prototype.test.cjs` → `passed: 16`、`browserErrors: []`。

---

## Why this matters

Claude Code 在回合结束、输入框空闲且为空时，主动预测用户下一句自然输入（"run the tests"），以灰色幽灵文本显示；按 Tab / → / End 填入草稿（不发送），用户确认后才发送；打字立即消散。这把"下一步该干什么"从用户回忆变成一次按键采纳，显著降低续作摩擦。本计划把该语义移植进 forebrain-harness，**TUI 与 web 共用同一引擎状态机**（"One engine, two surfaces"），两端行为逐条一致。

语义来源：本机 Claude Code 2.1.322 原生二进制逆向（prompt 常量、净化/过滤链、状态机、触发与抑制门、**退避算法**、按键、telemetry）。owner 明确要求**完全严格对齐 CC 真实语义和算法逻辑**，故退避不是 forebrain 自定的简化版，而是 CC 常量的逐条移植（见下）。

## 已裁决（owner 2026-11-21）

1. **web 原型确认**：Phase B 解锁。
2. **空框 Enter = 直发**（对齐 CC，telemetry `acceptMethod=enter`）：空框且幽灵显示时 Enter 直接发送推荐；Enter 本身就是确认。Tab / → / End 仍**只填草稿、不发送**。
3. **不显示按键提示行**：只有灰色幽灵文本，无 "Tab / → 采纳" 提示行；实施不加提示行渲染或 i18n。
4. **删除未采纳提醒**：不需要任何"连续 N 次未采纳"的提示/横幅/通知。降频是**静默**的。
5. **退避对齐 CC 真实语义**：连续 **20** 次未采纳 → 安装级全局 streak 达 20 → **静默降频至每 10 回合 1 次**（不是暂停）；采纳一次或重新开启开关即清零恢复每回合。（逆向确认本机 Qoder CN **无**此退避；owner 选择对齐 CC 而非 Qoder。）
6. **开关入两端配置 UI，本期一并落地**：`features.prompt_suggestions` 必须出现在 **web 设置页**与 **TUI `/config` 命令面板**，不只是 yaml。
7. **授权直接实施 + 落地**：在隔离 worktree 实施、跑全部门、自评审 diff，通过后 commit + rebase + `git merge --ff-only` 回 `dev`，**不 push**。

## 语义规格（逆向结论 → as-built 契约）

状态机（每会话一份，引擎 `pkg/turn` 持有）：`empty → generated → shown → accepted/ignored`，外加安装级全局退避。

### 生成触发与抑制
- **触发**：回合**完成**或**失败**结束时异步生成（[pkg/turn/submit.go:292](../pkg/turn/submit.go#L292) failed、[:299](../pkg/turn/submit.go#L299) completed）。**等待审批（waiting）与取消（cancelled）不生成**（这两个分支不调用 `suggestions.turnEnded`）。
- **fork**：复用该会话下一条请求的完整缓存前缀（system + 注入上下文 + 历史 + 工具），仅追加一条固定 instruction，无工具、单轮、静音前台流。提供商命中缓存前缀，只为 instruction+输出计费。
- **抑制门**（[pkg/turn/suggestion.go:219](../pkg/turn/suggestion.go#L219) `suppressed`，任一命中不生成）：`!Enabled`、`req.Unattended`、`req.BatchInput != nil`、`req.ParentRunID != ""`（子代理）、surface 不在 `Surfaces` 列表。
- **显示门**（各端）：输入框空闲且为空、非运行、未消散、开关开启。降频只门控**生成**，不隐藏已画出的幽灵。

### 净化 + 过滤（assembly，CC 的 q / C / D 逐条移植）
[pkg/assembly/suggestion.go](../pkg/assembly/suggestion.go)：
- `SuggestionPrompt`（[:25](../pkg/assembly/suggestion.go#L25)）：**CC suggestion-mode 提示词逐字**（含 EXAMPLES、NEVER SUGGEST、silence  clauses、安全 clause、"Format: 2-12 words"、"Reply with ONLY the suggestion"）。provider-neutral、字节稳定。
- `NormalizeSuggestion`（[:120](../pkg/assembly/suggestion.go#L120)，CC 的 q）：trim；剥整体 `<tag>…</tag>` 包裹（同名配对、内层无第二闭合标签才剥，嵌套视为元文本留给过滤拒绝）；剥多语言标签前缀（"Suggestion:"/"提案："等）。**不剥括号/引号**——整体包裹是元文本，由过滤拒绝。
- `SuggestionRejection`（[:223](../pkg/assembly/suggestion.go#L223)，CC 的 D 链，顺序固定，任一拒绝 ⇒ 沉默）：`empty → done → meta_text → meta_wrapped → error_message → prefixed_label → too_few_words → too_many_words → too_long → multiple_sentences → has_formatting → evaluative → claude_voice`。
  - **加权词数**（[:155](../pkg/assembly/suggestion.go#L155)，CC 的 C）：每个空白词，无 Han/phonetic ⇒ 计 1；否则 Hangul/其它字母数字贡献 1、每个 Han 计 1/2、每个 phonetic（假名 + 泰/老/高棉/缅）计 1/4，求和**向上取整**。故 "跑一下测试"（5 汉字）= ceil(2.5) = **3 词**（不是按字 5）。
  - `too_few_words`：词数 < 2 时——`/` 开头（斜杠命令）放行；否则 CJK 原始计数 > 0 需 ≥ 2 个 CJK 字符；否则需单词白名单（yes/ok/commit/push…）。
  - `too_long`：**UTF-16 码元长度** ≥ 100（CC 用 JS `String.length`，星界字符计 2）。
  - `claude_voice`：助手口吻前缀；**"让我们 …" 例外**（用户口吻）。Go RE2 无 lookahead ⇒ `HasPrefix("让我") && !HasPrefix("让我们")`（[:279](../pkg/assembly/suggestion.go#L279)）。
- `Service.SuggestNextStep`（[:299](../pkg/assembly/suggestion.go#L299)）：`ConversationSummary(sessionID)` fork → `summarize(WithMutedForegroundStream(ctx), UserMessage(Text(SuggestionPrompt)))` → `NormalizeSuggestion(res.Message.TextContent())` → 拒绝则返回 `"", nil`（沉默是正常结果）。

### 按键与草稿
- **采纳**：Tab / → / End（web 三键；TUI 本期 Tab / →，End 见偏差）把推荐填入草稿、光标落末尾，**不发送**；幽灵消失。**采纳即清零**会话计数与全局 streak（CC 的 `onTabOnEmpty pvn(true)`），每回合生成立即恢复。
- **空框 Enter**：直发推荐（裁决 2）。非空 Enter 正常发送草稿。**TUI 安全护栏**：幽灵文本恰为 `exit`/`quit` 时**永不直发**（[pkg/tui/run.go:1042](../pkg/tui/run.go#L1042)），避免误退循环。
- **打字消散**：任何草稿编辑立即永久消散本条幽灵；清空回空**不**恢复。消散**不计为未采纳**，直到下一次提交才判定。
- **新回合清除**：`turnStarting` 清除当前幽灵并 `gen++`（使在途生成过期）。

### 采纳推断 + 退避（turn，CC 算法逐条移植）
[pkg/turn/suggestion.go](../pkg/turn/suggestion.go)。常量（[:32-36](../pkg/turn/suggestion.go#L32)）：`defaultSuggestionBackoffThreshold = 20`（CC r）、`defaultSuggestionThrottlePeriod = 10`（CC Sgi）、`defaultSuggestionBackoffGrace = 14*24h`（CC `_ = 1209600000ms`）。env 覆盖 `FOREBRAIN_ENABLE_PROMPT_SUGGESTION=true`（[:41](../pkg/turn/suggestion.go#L41)，CC `CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION`）。**CC 的 f=21 通知哨兵与 N3t() kill-switch 桩省略**：前者只去重本端不显示的一次性提示，后者恒 false。

- 每会话状态：`current`（待画幽灵）、`lastOffered`（消散/Tab 采纳后仍留存，供下次提交比对）、`unusedInARow`（CC `le.unusedInARow`）、`eligibleTurns`（CC `le.eligibleTurns`）、`dismissed`、`gen`。
- 安装级持久（`SuggestionBackoffStore` 端口，[:54](../pkg/turn/suggestion.go#L54)）：全局 `Streak`（CC `fe().promptSuggestionUnusedStreak`，跨会话/进程共享）+ `FirstStart`（宽限期基准）。由 `pkg/process` 注入 DB 实现，引擎不 import 存储。
- **`turnStarting`**（[:275](../pkg/turn/suggestion.go#L275)，CC `onSubmit pvn`）：若 `lastOffered != ""`，`EqualFold(TrimSpace(UserText), TrimSpace(lastOffered))` ⇒ accepted，否则 ignored；仅当确有推荐时才记结果。清 current/lastOffered/dismissed，`gen++`。
- **`recordOutcomeLocked`**（[:253](../pkg/turn/suggestion.go#L253)，CC `pvn`）：accepted ⇒ `unusedInARow=0` + 清全局 streak；ignored ⇒ 若全局 streak 已 ≥ 20 则冻结（不再累加），否则 `unusedInARow++`，达 20 ⇒ `unusedInARow=0` 且持久化全局 streak=20。
- **`backoffActiveLocked`**（[:226](../pkg/turn/suggestion.go#L226)，CC `v7r`）：全局 streak ≥ 20 **且** 无 env 覆盖 **且** 安装已过 14 天宽限（`FirstStart` 零值 = 未知 = 视为足够旧）。
- **`turnEnded`**（[:298](../pkg/turn/suggestion.go#L298)，CC `Bzo` 门）：退避活跃时 `eligibleTurns % 10 == 0` 才生成（随后 `eligibleTurns++`），即**每 10 回合 1 次**；**静默**——无 notice、无 paused 事件。生成异步，落地时校验 `gen` 未变且未消散/无 current，否则丢弃（防过期 goroutine 覆盖）。
- **`Accept`**（[:343](../pkg/turn/suggestion.go#L343)）：Tab/→/End 采纳 ⇒ 清 `unusedInARow`/`eligibleTurns`/全局 streak，立即恢复每回合。
- **`Dismiss`**（[:358](../pkg/turn/suggestion.go#L358)）：打字 ⇒ 隐藏 current、`dismissed=true`、`gen++`，阻止再生成直到下一回合边界；不动未采纳计数。
- **`setEnabled`**（[:137](../pkg/turn/suggestion.go#L137)，CC `bgi` 配置开关路径）：重新开启 ⇒ 清所有会话计数 + 全局 streak（全新开始）；关闭 ⇒ 不生成。配置同步走 `SetPromptSuggestionsEnabled`（[:111](../pkg/turn/suggestion.go#L111)）**移动门而不替换 suggester**，保留会话计数。

### 事件（push-only，仅一个 kind）
- **只有** `prompt_suggested{Suggestion}`（[pkg/event/run_events.go:81](../pkg/event/run_events.go#L81) 常量、[:358](../pkg/event/run_events.go#L358) payload）。**无 `prompt_suggestions_paused`**（裁决 4：静默）。
- 加入 `IsPushOnlyStreamEvent`（[:145](../pkg/event/run_events.go#L145)）⇒ 不落库、无 sequence、不 bind 回放。幽灵是瞬时 live 状态，重开页面不应看到旧推荐。
- web `applyPromptSuggestedEvent`（[useChatStream.ts:3715](../frontend/src/composables/useChatStream.ts#L3715)）`if (historical) return`：bind 回放的历史事件忽略，只 apply live。

### 开关 + 配置 UI（裁决 6，两端已落地）
- `features.prompt_suggestions`，默认 **true**（[pkg/config/config.go:148](../pkg/config/config.go#L148) `FeaturesSection.PromptSuggestions`、`EffectiveFeatures` 解析、`load.go` materialize、`process/config_defaults.go` 持久化）。
- **web 设置页**：「会话」标签新增 `ps` 组（[settingsSchema.ts:297](../frontend/src/lib/settingsSchema.ts#L297)，`titleLabel:true` 单开关，镜像 memory 组），字段 `features.prompt_suggestions` bool def true；文案 zh/en（`settings.groupPromptSuggestions`/`field`/`hint`）。tracked 原型副本 `frontend/src/lib/testdata/config-db-settings-preview.html` 同步（`settingsSchema.test.ts` 钉 `F('` 计数 = 34）。
- **TUI `/config`**：features 段新增 `prompt_suggestions` 三态开关（[config_panel_schema.go](../pkg/tui/config_panel_schema.go)，Label "Next-step suggestions"，Hint "Predict your next message as ghost text in the idle composer; Tab or Right takes it."，段落 Summary 更新）。原型副本契约测试 `TestConfigPanelPrototypeCopy` 钉值同步。
- 两端 `OnConfigReload` → `SetPromptSuggestionsEnabled(next.EffectiveFeatures().PromptSuggestions)`（web [serve_run.go:241](../pkg/gateway/serve_run.go#L241)、TUI [notify.go:1807](../pkg/tui/notify.go#L1807)）。

### REST 采纳/消散（web）
- `POST /prompt-suggestion {session_id, action: accept|dismiss}`（[api_extra.go:399](../pkg/gateway/api_extra.go#L399) → [run_control.go:2018](../pkg/gateway/run_control.go#L2018) `handlePromptSuggestion`）：会话归属校验（非本人 404）、缺 session_id 400、非 POST 405、未知 action 400；`Core.AcceptSuggestion`/`DismissSuggestion`。web `acceptSuggestion`/`dismissSuggestion`（useChatStream.ts:6146/6156）fire-and-forget 调用。

## 架构约束（as-built 已满足，棘轮不变）

- 分层 L0…L5；**L3 之间零同层边**；**fan-out 棘轮**（[pkg/architecture/cache_test.go](../pkg/architecture/cache_test.go)）：turn 12/12 满、assembly 9/8 已超 ⇒ **turn/assembly 均零新增内部 pkg import**。
- as-built：`pkg/turn/suggestion.go` 只 import context/os/strings/sync/time + 已有 `event`；`pkg/assembly/suggestion.go` 只 import context/math/regexp/strings/unicode/unicode/utf16 + 已有 `llm`。生成能力在 assembly（持 `ConversationSummary` fork），状态机在 turn（注入 `Generate` func + `Backoff` store），surface 用已在调的 `run.CompactionService(...)` 构造 Generate 闭包。`pkg/architecture -count=1` 绿证明零新增边。
- 消费者定义端口、surface/process 注入（`AutoContinueFunc`/`Compactor`/`RunExecutor` 同模式）：`PromptSuggestionFunc`、`SuggestionBackoffStore` 均由 surface/process 注入，引擎不 import DB。

## As-built 文件地图（均于 worktree 亲自核对）

**新增：**
- `pkg/assembly/suggestion.go` + `suggestion_test.go`（CC prompt、q 净化、C 加权词数、D 拒绝链、`SuggestNextStep`）。
- `pkg/turn/suggestion.go` + `suggestion_test.go`（状态机、严格 CC 退避、注入端口、With/Set/Accept/Dismiss/Current）。
- `pkg/process/suggestion_backoff.go`（DB 持久安装级 streak + firstStart，进程级 once；`Environment.SuggestionBackoff()`）。

**修改（引擎/配置/事件）：**
- `pkg/turn/submit.go`：`suggestions.turnStarting`（:104）、`turnEnded`（:292/:299）；Service 加 `suggestions` 字段。
- `pkg/turn/service.go`：suggester 字段/构造接线。
- `pkg/event/run_events.go`：`RunEventPromptSuggested` + payload + push-only switch。
- `pkg/config/config.go`、`load.go`、`pkg/process/config_defaults.go`、`open.go`：PromptSuggestions 门 + backoff store 接线。

**修改（TUI）：**
- `notify.go`：`WithPromptSuggestions`（:1753）、`promptSuggestionConfig`（:1838，Generate=CompactionService fork、Surfaces=SurfaceTUI、Backoff=Env store）、`OnConfigReload`（:1807）、事件→Msg（:349 `handlePromptSuggestionNotification`）、`suggestionController` 接口（:3132）。
- `run.go`：`suggestionGhost`（:3745）、`dismissSuggestion`（:3759）、`acceptSuggestionIntoDraft`（:3774，seedReader）、空框 Enter 直发 + quit 护栏（:1034-1045）、`hotkeyAcceptSuggestion`（:5802）、打字消散（:980-984）、新回合清除（:3618-3622）。
- `input_events.go`（Tab/→ 发采纳 hotkey :60）、`render.go`（`SuggestionGhost` :4461）、`reducer.go`（dim-245 幽灵 :4440）、`config_panel_schema.go`（features 开关）。

**修改（gateway/web）：**
- `serve_run.go`（安装 :236 + 配置同步 :241）、`run_control.go`（`promptSuggestionConfig` :1387 + REST :2018）、`api_extra.go`（路由 :399）。
- `forebrainGatewayRuntime.ts`（事件 union + parse）、`useChatStream.ts`（apply :3715、observer :3900、live :6636、accept/dismiss :6146/6156、reset 清、exports :7216）、`api.ts`（`promptSuggestion`）、`ChatView.vue`（传 suggestion + 按键/打字接线）、`ForebrainPromptTextarea.vue`（幽灵占位符 + Tab/→/End/Enter）、`locales/index.ts`、`settingsSchema.ts`（ps 组）、`testdata/config-db-settings-preview.html`。
- 测试：`run_control_test.go`、`useChatStream.test.ts`、`ForebrainPromptTextarea.test.ts`、`SettingsSessionTab.test.ts`、`settingsSchema.test.ts`、`ChatView.test.ts`、`config_panel_test.go`。

## Commands（验证门）

```bash
# go 在 PATH；CGO 必需；-tags fts5。磁盘紧张时全量用 -p 1 串行（峰值磁盘受限）。
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 -p 1 ./... -count=1
gofmt -l cmd pkg third_party plugins        # 必须无输出
CGO_ENABLED=1 go vet -tags fts5 ./...
CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/turn -run 'TestSuggestion' -v -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/assembly -run 'TestSuggestion' -v -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'TestConfigPanel' -count=1
# web
cd frontend && pnpm typecheck && pnpm test && pnpm build
# 原型回归（校验冻结 hash 后）
node plans/344-prompt-suggestion-prototype.test.cjs
```

## Scope

**In scope（已实施）：** 引擎状态机 + 严格 CC 退避 + assembly 生成/过滤 + 事件 + 配置门 + **两端配置 UI** + TUI 输入链路 + web 输入链路 + REST 采纳/消散 + 全部对应测试。

**Out of scope（明确不碰）：**
- 不改 compaction 既有行为/阈值/事件；不改 autoContinue 逻辑；不改 `ConversationSummarizer` 签名。
- 不给 turn/assembly/run 新增内部 pkg import；不新增 L3 同层边。
- 不加按键提示行（裁决 3）；不加任何未采纳提醒/暂停横幅/`prompt_suggestions_paused` 事件（裁决 4/5）。
- 不做 bind 回放/持久化幽灵（push-only by design）。
- 不 push；merge 回 `dev` 仅在 owner 授权下（已授权）。

## Test plan（as-built，全绿）

- `pkg/assembly/suggestion_test.go`（5 PASS）：`RejectionMatrix`（过滤矩阵逐行 accept/reason）、`NormalizeStripsWrappers`、`WordCountCJK`（加权：跑一下测试=3）、`ShapeFilters`、`SuggestNextStep`。
- `pkg/turn/suggestion_test.go`（10 PASS）：`SuppressedTurnsNeverGenerate`（child/batch/unattended/disabled/surface）、`GeneratesAndPublishes`（completed/failed 生成、waiting/cancelled 不）、`UnusedStreakThrottles`（20→1/10）、`AcceptResumesEveryTurn`、`TabAcceptResumes`、`ReEnableResumes`、`EnvOverrideBypassesBackoff`、`GracePeriodBlocksBackoff`、`DismissHidesAndBlocks`、`StaleGenerationDropped`（gen 守卫）。
- `pkg/gateway/run_control_test.go`：`TestPromptSuggestionRESTAcceptsAndDismisses`（accept 清 streak、dismiss 不动 streak、归属 404、缺参 400、405、nil-Core）。
- `pkg/tui/config_panel_test.go`：`TestConfigPanelPrototypeCopy`（features summary + prompt_suggestions label/hint 钉值）及其余 features 渲染/间距/可达测试。
- web：`ForebrainPromptTextarea.test.ts`（幽灵占位符仅空闲空时、运行中隐藏、Tab/→/End 填草稿不 submit、空 Enter 直发、非空 Enter 发草稿/Tab 不接受、运行中不采纳）；`useChatStream.test.ts`（push-only：live 设置/去重/turn_started 清/空白清、historical 忽略、accept/dismiss REST、reset 清）；`SettingsSessionTab.test.ts`（六组含 ps、titleLabel 行名）；`settingsSchema.test.ts`（F(' 计数 34）；`ChatView.test.ts`（mock 含 suggestion/accept/dismiss）。
- 原型回归：16 checks（含静默降频、重启用清零、加权词数、无 notice），作为语义对照基准。

## Done criteria（机器可检 + 证据）

1. 全量 `go test -tags fts5 -p 1 ./... -count=1` 全 PASS（复跑取干净 exit=0；首轮唯一失败 `TestConfigPanelPrototypeCopy` 已修，聚焦 TUI 复跑 ok）。
2. `gofmt -l cmd pkg third_party plugins` 无输出；`go vet -tags fts5 ./...` 干净（vet_exit=0）。
3. `./pkg/architecture -count=1` PASS（棘轮/同层边未变）。
4. 聚焦 turn（10 PASS）/assembly（5 PASS）`-run TestSuggestion -v` 无 FAIL。
5. web：`pnpm typecheck && pnpm test && pnpm build` 全绿（194 文件 / 1569 测试 / build ✓，web_gates_v2_exit=0）；原型 hash 匹配冻结值；原型回归 passed 16、browserErrors []。
6. 两端配置 UI 落地：web ps 组 + TUI /config features 开关，默认 true，配置同步移动门。
7. 视觉/键盘：原型回归在真实浏览器 textarea 验证 Tab/→/End 填草稿、空 Enter 直发、打字消散、静默降频、重启用恢复（截图 idle/accept/backoff/filters/enter/narrow）。**真实 web composer 的端到端幽灵需一次真实模型回合才会出现（无 live 模型时不画），故端到端幽灵以组件级单测 + push-only 事件单测覆盖，未做 live 模型浏览器验证——如实说明。**

## STOP conditions（遇到即停、报告）

- 冻结 hash 不匹配。
- 需给 turn/assembly/run 新增内部 import 或 L3 同层边 ⇒ 停（棘轮），报告替代设计。
- TUI raw reader 采纳后无法正确 reseed（光标/历史浏览错乱）⇒ 停。
- 过滤/净化与原型矩阵对不上且无法用规格解释 ⇒ 停。
- 任何既有 compaction/autoContinue/config-panel 测试变红 ⇒ 停（共享路径改坏）。

## Maintenance notes / 偏差

- **TUI End 延后**：raw reader 不解析 End（无 `[F`/`[OF`）；本期 TUI 采纳键 = Tab/→，web = Tab/→/End。后续给 reader 加 End 解析即可补齐（独立小改）。
- **无 focus 抑制生成**：CC 在窗口失焦/plan-mode/pending-permission 时抑制；本期仅靠 running/空框/消散/开关/退避门。失焦抑制可后续加。
- **无 cache-cold 经济门**：CC 有"缓存冷时不生成"省钱门；本期依赖 `ConversationSummary` 天然命中缓存前缀，不加额外门。
- **无 bind 回放**：幽灵 push-only，重开页面不显示旧推荐（by design）。
- **退避是 CC 常量逐条移植**（20/10/14 天/env 覆盖/全局 streak + 会话计数/静默），不是 forebrain 简化版。改常量只动 `pkg/turn/suggestion.go:32-36` + 测试。CC 的通知哨兵与 kill-switch 桩按裁决省略。
- **全局 streak 安装级共享**：`pkg/process/suggestion_backoff.go` DB 持久，跨会话/进程；TUI 与 web 注入同一 `Env.SuggestionBackoff()`，故两端共享同一退避状态（CC 语义）。`FirstStart` 首次读取落 now，宽限期从此计。
- **配置 UI 已落地**（裁决 6）：web 设置页 ps 组 + TUI /config features 开关；改文案需同步 `settingsSchema.test.ts`（F(' 计数）、`config-db-settings-preview.html`、`TestConfigPanelPrototypeCopy` 钉值、locales zh/en。
- **开关 re-arm**：`SetPromptSuggestionsEnabled(true)` 清全局 streak + 会话计数；配置同步移动门而不替换 suggester（保留计数）。
- 未来改 `ConversationSummarizer` 签名或 compaction 前缀 shaping 会直接影响 `SuggestNextStep` 的缓存命中与计费——review 重点。
- 幽灵文本是**模型输出**、跟随会话语言，**不走 i18n**；只有占位符/设置文案/状态走 i18n。别把幽灵塞进 locales。
- **TUI Enter quit 护栏**：幽灵为 exit/quit 时不直发，避免误退循环；web 无此护栏（web 无 quit 循环语义）。