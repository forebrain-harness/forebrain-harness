# 修复：子代理自动加载主会话回合选中的技能（improve 卡片凭空出现）

> 状态：已实现，待 owner 审阅提交（工作区改动，零 commit）
> 基线：`202ed4f2`（dev）；执行时主树 HEAD `5c6a7d34`，三个 in-scope 文件的 blob 与基线计划记录逐字节一致
> 改动面：`pkg/run/skills.go`、`pkg/run/subagent.go`、`pkg/run/subagent_test.go`

---

## 1. 现象

子代理只能按**自己这条消息**里出现的技能运行。主会话（或任何父回合）因为消息里写了 `/improve` 而激活的技能，
绝不能跟着 `subagent_run` / `subagent_continue` 等派发指令流进子代理的对话——既不画 Skill 卡，也不把那份
SKILL.md 全文塞进子代理的每个请求。

现场（owner 截图 + 数据库佐证）：主会话第一条用户消息是 `/improve execute <plan>`，该回合派发了
general-purpose 执行子代理；子代理视图中用户插话问「你一直在做什么？验证什么功能？」，随后冒出
`● Skill improve / Loaded from .claude/skills/improve/SKILL.md`，子代理自己的 reasoning 也写着
"Let me resume. The improve skill is loaded but I'm the executor for this plan"。

这不是显示层瑕疵：子代理的**请求上下文**里真的带着 improve 的全文与「The user selected the `improve` skill for
this request」这条元消息，等于把一份它没申请的工作规程塞给每一次请求（浪费 token、扭曲判断）。

## 2. 根因

技能的“显式选择”与“已加载内容”是**父回合上下文**上的两个值：

- `pkg/tui/chat_session.go`（gateway 同族：`pkg/gateway/server.go`、`pkg/process/run_executor.go`）
  `agBase = run.WithExplicitSkillSelection(agBase, skillName, skillPath)` —— 写在**这一回合的 ctx** 上。
- `pkg/run/skills.go` `preloadExplicitSkills` 读它并真的读盘加载（画 Skill 卡，`loadExplicitSkill`）；
  `WithExplicitSkillActivations` 把加载结果再挂回 ctx；`pkg/run/transcript.go` 在每次请求构造时把激活消息插到本轮用户消息前面。
- `pkg/run/orchestration_llm.go:167` `steerMessages` 对**消息自带**的选择做同样的加载（这是正当路径）。

问题在**子代理对话的框架层是从父回合 ctx 派生的**，两个键跟着一起过界：

```go
// prepareSubagentExecutionResolved —— 派发时子会话的 ctx
cctx, cancel := context.WithCancel(runCtx)      // runCtx == baseCtx == 父回合 ctx

// continueSubagentExecution —— 模型的 subagent_continue
hostCtx := subagentRecordContext(ctx, fac, record)   // ctx == 父回合 ctx（工具调用上下文）

// subagentExecContext —— 注释自称“每条派发路径构造子 ctx 的唯一地点”
hostCtx := prep.ctx                                  // 继承自父回合
```

于是 `subagent_run` / `subagent_fanout` / `subagent_send` / plan review / goal check / fork 首轮（都经
`subagentExecContext`）、以及模型的 `subagent_continue`（经 `subagentRecordContext`）全部继承父回合的选择 →
子代理 `run.Run` → `preloadExplicitSkills` 在自己的 worker 会话里再加载一次（Skill 卡 + 每请求激活消息）。

**同族语义已有正确实现**：`beginAttempt` 给每次子执行换掉主会话的 `TurnInputRuntime`
（教训见测试 `TestSubagentContextDoesNotInheritPrimarySteerRuntime`）；`asyncSubagentContext` 也是同一原则的另一种
写法（新 background ctx 只挑必须过界的少数事实）。本修复只是把同一原则补到技能这两个键上。

## 3. 修复

`pkg/run/skills.go` 新增一个边界助手：

```go
// withoutExplicitSkills drops the skill a dispatching turn's message selected,
// and the activation already loaded from it, so a child conversation never runs
// on a skill the user gave another conversation.
func withoutExplicitSkills(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, explicitSkillSelectionsContextKey{}, []explicitSkillSelection(nil))
	return context.WithValue(ctx, explicitSkillActivationsContextKey{}, []explicitSkillActivation(nil))
}
```

两个读取器本来就是 `len(...) == 0` 判空，空切片就是“没有选择”，不需要新概念；`WithExplicitSkillSelections(ctx, nil)`
不做剥除（空输入直接返回原 ctx），所以这里必须直接写键值。

应用点就是两个既有注释声明的子对话框架构造器：

1. `subagentExecContext`：`hostCtx := withoutExplicitSkills(prep.ctx)`
   —— 覆盖 subagent_run / fanout / send / plan review / goal check / fork 首轮全部派发路径。
2. `subagentRecordContext`：`hostCtx := withoutExplicitSkills(ctx)`
   —— 覆盖模型的 `subagent_continue`（以及将来任何把父回合 ctx 传进来的续跑入口）。

**不在边界之后剥**：`subagentResumeAttempt` 是在 `beginAttempt` 之后**按消息**用 `WithExplicitSkillSelections`
重新种上选择的——用户在子代理视图打字 `/skill`、或中断续发带技能，仍照旧加载。同理 `steerMessages` 用的
`entry.SkillName`（消息自带）不受影响。

## 4. 行为变化矩阵

| 场景 | 旧行为 | 新行为 |
|---|---|---|
| 父回合消息里含 `/improve`（或任何 `/skill`），该回合 `subagent_run`/`fanout`/`send`/plan review/goal check 派发子代理 | 子代理继承选择 → 每个请求注入该 SKILL.md 全文 + Skill 卡（**现场 bug**） | 子代理不带；只有父会话自己有 |
| 同上，但子代理是 fork 首轮 | 同上 | 同上，不带 |
| 父回合含 `/skill`，模型调用 `subagent_continue` 续跑既有子代理 | 续跑请求带父回合技能 | 不带 |
| 用户在子代理视图输入 `/skill` | 加载 + Skill 卡 | **不变**（选择来自这条消息） |
| 用户在子代理视图打字后按 Esc 中断续发（`subagentResumeAttempt`，`mergeSteers`） | 加载 + Skill 卡 | **不变**（在边界之后种入） |
| 子代理执行中途 steer 带 `/skill`（`steerMessages`） | 加载 + Skill 卡 | **不变** |
| 主会话自己的 `/skill` 回合 | 加载 + Skill 卡 | **不变** |
| 主会话普通回合派发子代理 | 无一继承 | **不变** |

## 5. 测试

`pkg/run/subagent_test.go`：

- `TestSubagentDispatchDoesNotInheritTheDispatchTurnsSkill`（新）：派发门。父回合 ctx 带
  `WithExplicitSkillSelection` + `WithExplicitSkillActivation` 后走 `execGeneralSubagent`，
  用既有 `ctxCaptureSubagentExecutor` 断言子执行上下文里两个键都已剥空。
- `TestSubagentContinueDoesNotInheritTheDispatchTurnsSkill`（新）：续跑门。行为级断言：
  续跑期间无 skill step 事件（不画 Skill 卡）、没有任何请求带 `PROBE-SKILL-BODY-7f3a` 或
  「The user selected the」元消息，且续跑自己的消息确实进入了请求（证明这次执行真的发生了，不是空跑）。
- `TestForkContinuationLoadsTheSkillItsMessageSelected`（修订）：原用例把选择挂在**父 ctx** 上再调
  `continueSubagentExecution` —— 正是本 bug 的形状。改为经 `subagentResumeAttempt`（用户续发的真正所有者）
  种入选择，原断言（激活在该消息之前、末尾是这条消息、且不落库）逐条保留。

## 6. 维护契约

- **“这条消息选了哪个技能”属于消息，不属于对话。** 任何“把父回合 ctx 变成子对话框架”的地方都必须调用
  `withoutExplicitSkills`；新增此类入口要照此办理。
- 史上同族事故：`TurnInputRuntime` 过界（修法 = 子执行换自己的 runtime）。判据一致：
  父回合的**私有执行状态**不得成为子对话的状态。
- 已知遗留（不在本期）：其它跨边界的父回合私有值（如 `tool.SystemAddendumFromContext`、goal 续跑输入）是否也该剥，
  需单独审计，不在本 bug 因果链内。

## Implementation Status

全部完成，零 commit，改动留工作区待 owner 审阅。

### 验证记录

**Step 0 红测试（修复前，`go test -tags fts5 ./pkg/run -run 'DoesNotInheritTheDispatchTurnsSkill'`）**

```
--- FAIL: TestSubagentDispatchDoesNotInheritTheDispatchTurnsSkill (0.01s)
    subagent_test.go:2307: child context still carries the dispatch turn's skill selection [{probe-skill …/probe-skill/SKILL.md}]: the subagent would load and inject a SKILL.md the user selected for another conversation
--- FAIL: TestSubagentContinueDoesNotInheritTheDispatchTurnsSkill (0.01s)
    subagent_test.go:2358: the continuation loaded the dispatch turn's skill (tool_call_started skill="probe-skill"): the subagent drew a Skill card for a skill its own message never selected
FAIL
```

**Step 1 修复后同一条命令** → `ok github.com/forebrain-harness/forebrain-harness/pkg/run 0.902s`。

**Step 2 / 3 包级回归**

- `go test -tags fts5 ./pkg/run -count=1` → `ok`（43.9s）
- `go test -tags fts5 ./pkg/run ./pkg/tui ./pkg/turn ./pkg/gateway -count=1` → 四包全 `ok`
  （`pkg/gateway` 已知基线红 `TestMCPServerWriteReturnsStoredCredentialsInPlaintext` 本轮由并行会话修好，未出现）
- `go vet ./...` 无输出；`gofmt -l pkg/run/skills.go pkg/run/subagent.go pkg/run/subagent_test.go` 无输出

**Step 4 真机 A/B**（tmux + fake provider + 隔离 `FOREBRAIN_HOME`，`ENABLE_SUBAGENT=1`，
模式 `subagent-steer`，隔离项目里放 `.claude/skills/probe/SKILL.md`（正文含唯一串 `PROBE-SKILL-BODY-7f3a`），
主视图提交 `/probe go`——技能命令 → 本回合 ctx 带选择 → 同回合派发 general-purpose 子代理）：

| | 主视图 | 子代理视图 | 子代理请求 dump |
|---|---|---|---|
| 修复前（`go build -overlay` 换回 HEAD 的 `skills.go`/`subagent.go`） | `● Skill probe └ Loaded from .claude/skills/probe/SKILL.md`（正当） | `● Skill probe └ Loaded from …`（**bug**） | `request-0001`(主) body=1 meta=1；`request-0002`(子) **body=1 meta=1** |
| 修复后 | 同上（正当） | 无 `Skill probe` 卡 | `request-0001`(主) body=1 meta=1；`request-0002`/`0003`(子) **body=0 meta=0** |

（`body` = `PROBE-SKILL-BODY-7f3a` 出现次数，`meta` = `The user selected the` 出现次数；主/子请求按
`You are a general-purpose subagent` 系统标记区分。）

**Step 4 回归**：在子代理视图里提交 `/probe 继续`（steer 带技能，边界之后种入）→ 子代理视图出现
`● Skill probe`，且该次子代理请求 dump `body=1 meta=1`——用户正当技能未被误伤。

**范围**：`git status --porcelain pkg/run/skills.go pkg/run/subagent.go pkg/run/subagent_test.go` 只有这三个文件
由本次改动产生；`pkg/tui`、`pkg/gateway`、`pkg/process` 与并行会话正在改的 `pkg/run/{config*.go,runner*.go}` 一字未动。

**说明**：真机 A/B 用的是 driver 的假 provider，其配置固定为 `deepseek/deepseek-chat`（模型名只出现在 footer 与标签上，
请求由本地假 provider 应答，不触达真实模型）。
