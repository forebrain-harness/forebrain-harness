# Plan 038：上下文占用只用 forebrain 自己请求的用量当锚点（迁移会话续聊第一句不再被误压缩）；压缩卡片显示判定用的数

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：`git diff --stat 005c1a8 -- pkg/state/usage.go pkg/assembly/service.go pkg/state/action_service_test.go pkg/assembly/service_test.go pkg/tui/commands_test.go`
> 应无输出（计划写成时这些文件与 `005c1a8` 相同）。有输出 → 对照下面 "Current state" 的摘录逐段核对，不符 → STOP。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW（只收窄"哪些行的用量可以当锚点"；原生会话行为不变）
- **Depends on**: 无（与 039–042 独立，可最先做）
- **Category**: bug
- **Planned at**: commit `005c1a8` + 主树未提交改动（不涉及本计划文件），2026-10-11

## Why this matters

owner 报告：从 Claude Code 迁移过来的长会话，用 deepseek-v4-flash（1M 窗口）续聊，发出第一句就触发了自动压缩，卡片显示 "398.2k → 3.3k"。
根因已在本机状态库与日志核实：轮前判定 `autoCompactDecision`（`pkg/assembly/service.go:208`）用 `state.TokenCountWithEstimation` 算占用，它以"最后一条带 `usage_json` 的 assistant 行"的服务端用量为锚点。
这个会话最后一条带用量的行是**迁移导入的** Claude Code 行（`fb_messages.id=119424`，model `claude-opus-5-5`，`input 2 + cache_creation 608 + cache_read 898076 + output 1403 = 900089`），
它量的是 Claude Code 自己那次请求：CC 的系统提示与工具 schema、257 个保留下来的加密 thinking 块（签名合计约 79 万字符，迁移没有搬、forebrain 也不会重放）、27 张图片、Anthropic 分词器。
900089 ≥ deepseek 1M × 0.9 = 900000，于是压缩；而 forebrain 自己的估算只有 398170（卡片上显示的那个数）。日志 `~/.forebrain/logs/error.log` 21:19:49 的 WARN 带着 `prompt_tokens=900089` 可作证。
同模型续聊同样有问题（thinking 块、CC 的系统提示都不在 forebrain 的请求里）。判据应当是"这条用量是不是 forebrain 自己发的请求产生的"，不是"模型是否相同"：
本机实测 15 次原生换模型，原生锚点在 13 次里比纯本地估算更接近真实值，所以原生行即使换了模型也继续当锚点。
另外卡片的 `tokens_before` 用的是 `llm.EstimateMessages`，判定用的却是锚点值，用户看到"398k 就压缩"无从理解；改为显示判定实际用的数。

## Current state（全部已核实）

- 迁移导入的行**不写 `run_id`**：`pkg/migrate/sessions.go:376-392` 的 `INSERT INTO fb_messages(session_id, role, content, created_at, message_id, parts, model, usage_json, tool_step_id, tool_meta_json)`，没有 `run_id` 列。
  本机库只读统计：`origin='native'` 会话里带用量的 assistant 行 12805 条，`run_id IS NULL` 的 0 条；`origin='migrated'` 会话里 `run_id IS NULL` 的 28089 条（全部是导入行），非空的 743 条（迁移后在 forebrain 里续聊产生的原生行）。
- 唯一写入用量的生产路径带 run：`pkg/turn/session.go:362-399` `appendAssistantRows` 调 `AppendMessageSequenceForRun(ctx, in.SessionID, in.RunID, ...)` / `AppendStructuredMessageForRun(...)`，用量来自 `state.MarshalTokenUsage(in.Result.LastResponseUsageCopy())`。
  其他不带 run 的追加（`pkg/run/goal.go:366`、`pkg/gateway/api_extra.go:3204`、`pkg/turn/session.go:449`、`pkg/tui/chat_turn.go:777`）传的 usage 都是 `""`。
- `state.Message.RunID` 由读取路径填好：`pkg/state/session_store.go` 的 `scanMessageWithRun`（`row.RunID = runID.String`）与 `messageFromStored`（`RunID: row.RunID`）。
- 锚点判定只有一个入口 `pkg/state/usage.go:258-271`：

```go
func usageFromMessage(msg Message) (TokenUsage, bool) {
	if !strings.EqualFold(strings.TrimSpace(msg.Role), "assistant") {
		return TokenUsage{}, false
	}
	raw := strings.TrimSpace(msg.UsageJSON)
	if raw == "" {
		return TokenUsage{}, false
	}
	var usage TokenUsage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		return TokenUsage{}, false
	}
	return usage, usage.TokenCount() > 0
}
```

  它被三处使用（同文件）：`TokenCountFromLastAPIResponse`（`:163`，所有占用表盘：`pkg/run/config.go:1836` `ContextOccupancy`、`pkg/tui/commands.go:142`）、
  `TokenCountWithEstimation`（`:173`，轮前判定 `pkg/assembly/service.go:217` 与 `pkg/assembly/hook.go:136` 的 `ConversationTokens`）、`BodyAfterPrefixTokenCount`（`:203`）。只改 `usageFromMessage` 一处，三者一致生效。
- 卡片数字：`pkg/assembly/service.go:127-145`

```go
func (s Service) AutoCompactSession(ctx context.Context, sessionID, _ string) (Result, bool, error) {
	decision, err := s.autoCompactDecision(ctx, sessionID)
	if err != nil || !decision.Should {
		return Result{}, false, err
	}
	sid := normalizedSessionID(sessionID)
	messages, err := s.Sessions.ListTranscriptMessages(ctx, sid, 5000)
	if err != nil {
		return Result{}, false, err
	}
	ctx, run := s.beginCompaction(ctx, sid, "auto", llm.EstimateMessages(messages))
	res, err := s.compactSession(ctx, sid, messages, "auto", decision.Reason, decision.PreviousModel)
	if err != nil {
		run.fail(err)
		return Result{}, false, err
	}
	return run.finish(res), true, nil
}
```

  `compactMessages`（`:332` 起）里 `before := llm.EstimateMessages(messages)`，结果 `TokensBefore: before`（`:406`）。`run.finish(res)` 把 `res.TokensBefore` 写进持久化的 `context_compacted` 事件（`pkg/assembly/compact_lifecycle.go` `finish` → `CompactResultPayload`），回放也读这个事件，所以只要在 `finish` 之前改 `res.TokensBefore`，live 与回放一致。检查点边界行（`AppendCompactCheckpoint`）不存 `TokensBefore`，不受影响。
  `decision.Usage` 就是判定用的占用值（`autoCompactDecision` 每个 return 都带 `Usage: usage`）。
- 测试现状（会受影响）：
  - `pkg/state/action_service_test.go:1391-1445`：`assistantMessage` 帮手与 `TestTokenCountFromLastAPIResponseAndTailEstimate`、`TestTokenCountWithEstimationWalksBackAcrossSplitAssistantResponse`、`TestBodyAfterPrefixUsesFirstServerInputAsBaseline` 等用的 `Message{Role:"assistant", UsageJSON: ...}` **没有 RunID**。
  - `pkg/tui/commands_test.go:907`：`{Role: "assistant", Content: "old answer", UsageJSON: fullUsage}` 没有 RunID。
  - `pkg/assembly/service_test.go:408-460` `TestEmptyReplacementMustNotPersistDeadCheckpoint`：用 `AppendStructuredMessage`（无 run）写 `{"input_tokens":900000,...}` 让判定越线；改动后这些行不再是锚点，判定不越线，测试会走到 `t.Skip("no compaction happened; nothing to assert")` **悄悄变成跳过**——必须改成带 run 写入（Step 3）。
  - 用 `grep -rn 'UsageJSON\|usage_json\|input_tokens' pkg --include='*_test.go'` 找其余依赖"无 run 行当锚点"的测试，逐个判断。
- 写带 run 的测试行：`state.RunStore{DB: db}` 的 `CreateRun(ctx, sessionID, inputText)`（`pkg/state/runrt.go:216`，会话行必须先存在——先 `Append` 一条用户消息；同一会话同时只能有一个 live run，整个测试用一个 run 即可），
  再用 `SessionStore.AppendStructuredMessageForRun(ctx, sid, run.ID, "assistant", ...)`（`pkg/state/session_store.go:1558`）。`fb_messages.run_id` 有外键（`_foreign_keys=1`），所以 run 必须真实存在。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 单测 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/assembly/... ./pkg/tui/... ./pkg/run/... ./pkg/turn/... -count=1` | 全部 `ok` |
| 无跳过 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/assembly/ -run TestEmptyReplacementMustNotPersistDeadCheckpoint -count=1 -v 2>&1 \| grep -c -- '--- SKIP'` | `0` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/... ./pkg/assembly/...`；`gofmt -l pkg` | 无输出 |
| 包图 | `scripts/package-graph.sh && git diff --stat -- pkg/architecture/testdata/graph.json` | 只有 LOC 数字变化（CI 的 "Verify package graph" 要求提交重新生成的 graph.json） |

## Scope

**In scope**：`pkg/state/usage.go`、`pkg/assembly/service.go`、上面列出的测试文件（以及 grep 找到的同类测试）、`pkg/architecture/testdata/graph.json`（脚本重新生成）。

**Out of scope**：
- `pkg/migrate/*`：不改导入逻辑，**不清除**旧库里迁移行的 `usage_json`（它们是历史记录，别处可能展示；本计划只让它们不当锚点）。也不写数据迁移。
- 轮中检查（`pkg/run/compaction.go`、`pkg/run/llm_middleware.go`）：它本来就用本地估算，不读锚点；按会话模型估算是 Plan 042。
- 占用表盘在迁移会话第一条原生回复之前显示"空窗口"（`TokenCountFromLastAPIResponse` 返回 0）——这是"还没有测量"的如实表达，不在本计划改；见 Maintenance notes。
- 轮中压缩卡片的 `tokens_before`（`TryCompactOnMessages` 路径）：不动。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：锚点只认带 run 的行

改 `pkg/state/usage.go` 的 `usageFromMessage`：在角色判断之后加

```go
	// Usage anchors occupancy only when forebrain itself sent the request it
	// measures. A row with no run was imported from another harness (Claude
	// Code, Codex): its usage counts that harness's system prompt and tools, its
	// preserved thinking blocks and its tokenizer — none of which forebrain's
	// next request carries. Such rows are estimated like any other row instead.
	if strings.TrimSpace(msg.RunID) == "" {
		return TokenUsage{}, false
	}
```

注释用英文、与同文件风格一致。不要改 `TokenCountWithEstimation` 的同 `MessageID` 回溯逻辑。

**Verify**：`go build -tags fts5 ./...` 通过；`grep -n 'msg.RunID' pkg/state/usage.go` 有 1 行。

### Step 2：卡片显示判定值

改 `AutoCompactSession`：`beginCompaction` 的第四个参数改为 `decision.Usage`；`compactSession` 成功后、`run.finish(res)` 之前加 `res.TokensBefore = decision.Usage`，并加一行英文注释说明"the card reports the occupancy that triggered the compaction, the same number the gauge reads"。
`ManualCompactSession` 与 `TryCompactOnMessages` 不改。

**Verify**：`grep -n 'decision.Usage' pkg/assembly/service.go` 至少 2 行（beginCompaction 与 TokensBefore）。

### Step 3：修测试夹具

1. `pkg/state/action_service_test.go`：所有用来当锚点的 assistant 行加 `RunID: "run-1"`（包括 `assistantMessage` 帮手）；原断言数值不变。
2. `pkg/tui/commands_test.go:907` 一带同样加 `RunID`；若该测试断言的是"表盘显示了这条用量"，加了 RunID 后数值不变。
3. `pkg/assembly/service_test.go` `TestEmptyReplacementMustNotPersistDeadCheckpoint`：先 `Append` 一条用户消息建会话，再用 `state.RunStore{DB: <同一个 db>}.CreateRun` 建一个 run，把循环里的 `AppendStructuredMessage(...)` 换成 `AppendStructuredMessageForRun(ctx, sid, run.ID, ...)`。`newCompactTestStore` 目前不返回 db——新增一个返回 `(*state.SessionStore, *sql.DB)` 的帮手（或改原帮手并修所有调用方，择其简），不要在测试里重复开库。
4. 跑 grep 找其余同类测试，同样处理。

**Verify**：Commands 表"单测"与"无跳过"两行。

### Step 4：新增回归测试

见 Test plan。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ ./pkg/assembly/ -run 'Imported|Migrated|TokensBefore' -count=1 -v` 新测试全部 PASS。

## Test plan

1. `pkg/state/action_service_test.go`（或 `usage_test.go`）新增 `TestImportedUsageIsNotAnAnchor`：
   turns = 用户行 + 一条**无 RunID**、`UsageJSON` 为 `{"input_tokens":2,"cache_read_input_tokens":898076,"cache_creation_input_tokens":608,"output_tokens":1403}` 的 assistant 行 + 一条用户行。
   断言 `TokenCountFromLastAPIResponse(turns) == 0`，且 `TokenCountWithEstimation(turns)` 等于对同一组行去掉 `UsageJSON` 后的值（即纯估算）、并远小于 900089。
2. 同文件 `TestNativeUsageAnchorsAfterImportedRows`：上面的 turns 末尾再加一条 `RunID:"run-1"`、用量 `{"input_tokens":5000,"output_tokens":100}` 的 assistant 行。断言 `TokenCountFromLastAPIResponse == 5100`，`TokenCountWithEstimation >= 5100`。
3. `TestBodyAfterPrefixIgnoresImportedBaseline`：第一条带用量的行无 RunID（`input_tokens` 很大），第二条带 RunID；断言基线取第二条（按 `BodyAfterPrefixTokenCount` 的定义算出期望值写死）。
4. `pkg/assembly/service_test.go` 新增 `TestMigratedSessionDoesNotCompactOnImportedUsage`：
   用 `AppendStructuredMessage`（无 run）写一条用户行与一条 assistant 行，assistant 用量 `input_tokens: 900089`；`PrimaryModel` 返回 `("test-provider","test-model")`（不在目录里 → 窗口按 200000 回退，阈值 180000）。
   断言 `ShouldAutoCompactSession` 返回 `false`。再建一个 run，用 `AppendStructuredMessageForRun` 追加一条 assistant 行用量 `input_tokens: 190000`；断言返回 `true`。
5. 同文件 `TestAutoCompactCardReportsDecisionUsage`：沿用 4 的第二段数据（会压缩），`Events` 用一个记录事件的 sink（参照同文件或 `pkg/testutil.EventRecorder` 的写法），调 `AutoCompactSession`；
   断言 `context_compacting` 与 `context_compacted` 两个事件 payload 的 `TokensBefore` 都等于 `ShouldAutoCompactSession` 返回的 usage，且返回的 `Result.TokensBefore` 同值。

## Done criteria

- [ ] Commands 表全部通过（含"无跳过"为 0）
- [ ] Test plan 的 5 个新测试存在且 PASS
- [ ] `grep -n 'RunID' pkg/state/usage.go` 恰有 Step 1 那一处
- [ ] `git status --short` 中本计划的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. 发现有生产代码在**不带 run** 的情况下写入非空 `usage_json`（`grep -rn 'MarshalTokenUsage\|usageJSON' pkg --include='*.go' | grep -v _test` 逐个看调用链）——说明原生行也可能没有 RunID，本计划的判据会误伤，停下报告。
3. 某个现有测试的语义就是"无 run 的行也必须当锚点"（不只是夹具缺字段），停下报告。
4. `TestEmptyReplacementMustNotPersistDeadCheckpoint` 改成带 run 后仍然 SKIP。

## Maintenance notes

- 判据是"行有没有 run"。以后若新增写用量的路径，**必须**带 run ID，否则那条用量会被当成外来数据忽略（只会让占用读成估算值，不会出错，但会丢掉服务端的准确测量）。
- 迁移会话在第一条原生回复之前，表盘（`ContextOccupancy`）读到 0，显示整个窗口可用；第一条原生回复后恢复正常。若 owner 认为需要，可另立计划让表盘在没有原生测量时显示估算值（会改变所有会话的表盘口径，需单独决策）。
- 原生换模型（例如 glm → deepseek）仍以旧模型的原生用量为锚点：本机实测这比纯本地估算更准，下一次响应回来即自动校正。
- 评审重点：`usageFromMessage` 的新条件；测试夹具只是补 RunID、没有改断言数值；`TestEmptyReplacementMustNotPersistDeadCheckpoint` 不再跳过。
