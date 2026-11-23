# Plan 308: 技能推荐只在「有复发证据」时开口 —— 重写 offer 常驻准则与一次性提醒

> **Executor instructions**：按步骤执行，每步跑完验证命令并对照预期再往下。遇到「STOP 条件」任一条就停下报告，不要自行发挥。完成后不要改 `plans/`（索引由派发方维护）。
>
> **Drift check（开工第一步）**：
> ```sh
> git rev-parse --short HEAD                                    # 基线 09b44fe3（dev）；后代提交亦可，按内容核对
> grep -n 'offerGuidanceTemplate' pkg/skill/offer.go            # 期望 :30
> grep -rn 'Keeping finished work' pkg/ --include='*.go'        # 期望只有 offer.go 与 offer_test.go 两处
> grep -rn 'substantial tool-driven' pkg/ --include='*.go'      # 期望只有 skills.go 一处
> grep -n 'skillOfferMarker = ' pkg/run/skills.go               # 期望 "Skill worth keeping"
> ```
> 内容对不上即 STOP；行号漂移不算。

## Status

- **Priority**: P1（owner 2026-11-13 报障：「几乎每次长程任务后都会推荐『值得用 /skill-generator 固化成项目技能』，很多任务耗时长步骤多但其实是一次性的，没有沉淀价值。请定位根因并给出优化方案」）
- **Effort**: S
- **Risk**: LOW（只改两段提示词文本与对应 golden 测试；闸门逻辑、配置、工具面零改动）
- **Depends on**: none
- **Category**: bug fix（模型可见行为质量）
- **Planned at**: commit `09b44fe3`, 2026-11-13
- **Executed**: 2026-11-13 `execute` 变体 — worktree `.worktrees/fix-skill-offer-308`、分支 `fix-skill-offer-308`、commit `d0d74a07`，评审 APPROVE（全门禁由评审方在 worktree 亲跑复核绿）。**执行纠错**：本表定向测试原写法 `'TestRenderSkillOfferGuidance\|TestSkillOffer'` 在 Go regexp 里 `\|` 是转义的字面竖线、匹配零个测试（`ok [no tests to run]` 假绿，计划期的「已验证 PASS 0.447s/0.715s」即是该假绿）；执行与复核均改用 `'TestRenderSkillOfferGuidance|TestSkillOffer'`（19 PASS），下表已改正。

## Why this matters

`/skill-generator` 推荐今天几乎在每个长程任务收尾时出现，包括大量一次性任务（实现某个功能、跑一批计划、做一次迁移）。owner 的实际体验是噪音：推荐句本身还要占最终消息的篇幅，而接受它会把一次性编排写成无人再用的技能草稿。

根因**不是闸门 bug**，而是两层提示词都偏向「开口」，唯一的复发保护是一条不可执行的软判据：

1. **常驻准则**（每个会话都注入冻结 developer 块）：正面判据是「several ordered steps with hard-to-rediscover choices」——任何长任务都满足；「is likely to recur here」是裸预测，不要求任何证据；排除清单只列了 one-off debugging，**漏掉最大类假阳性：一次性多步工程任务**；提案句只要求说「重跑能省什么」，任何流程都能声称省时间。
2. **闸门 + 一次性提醒**：≥8 个工具结果 + ≥3 种工具（或失败恢复 + ≥5 个结果）即开闸，长任务必中；提醒锚定在后续每次迭代（包括写最终消息那一轮），措辞「has just done substantial tool-driven work, which **may be worth keeping**」是肯定式引导，判断又委托回第 1 层的弱判据。once-per-session 只 cap 单会话——每个长任务都是新会话，所以跨会话看就是「几乎每次都有」。

设计注释（`pkg/run/skills.go:653-659`）明写：闸门只数可数的信号，价值判断留给模型。**这个分工本身是定案，本计划不动它**；修的是它委托出去的那份判断标准——让「沉默」成为默认，让「复发」必须拿证据。

## Current state（执行者需要的事实，全部经评审方亲自打开核实）

### 机制全貌

- `pkg/skill/offer.go`
  - `:11` `DefaultSkillOfferCommand = "skill-generator"`。
  - `:30-36` `offerGuidanceTemplate`（**本计划改这里**），当前全文：
    ```
    ## Keeping finished work as a skill

    Noticing work worth keeping is your call: %s writes the steps that just worked into the project's skills directory, so the next session starts with them.

    Propose it when the work took several ordered steps with hard-to-rediscover choices (a non-obvious flag, an isolation trick, a wait or retry condition) that someone starting over would trip on, and is likely to recur here. Not for one-off debugging, answering by reading code, the repository's documented standard commands, or work an existing skill covers.

    To propose, end the final message for that work with one sentence naming %s and what rerunning it would save. Do it at most once per session and never again if ignored; when the work is not worth keeping, say nothing about it.
    ```
  - `:43-53` `RenderSkillOfferGuidance(opts)`：纯函数，`Enabled=false` 渲染 `""`；两个 `%s` 都填命令名（带 `/` 前缀）。**签名与行为不动**。
- `pkg/skill/offer_test.go:8-27` `TestRenderSkillOfferGuidanceGolden` 逐字节钉上面的渲染结果。注释明写：「any byte change … re-bills every new session's cached prefix. Changing the text is a deliberate act that must land in this file.」同文件 `:29-48` 另两个测试（disabled 渲染空、命令名归一化）是 Contains 级断言，**不用动**。
- `pkg/run/skills.go`
  - `:85-91` `(*Runner).skillOfferGuidance()` 读 `EffectiveFeatures().SkillOffer` 调 `RenderSkillOfferGuidance`；渲染结果追加进每会话冻结的技能 catalog developer 块（`catalogForSession`，`:103-120`：字节按会话冻结，改文本只影响**新**会话）。
  - `:631` `skillOfferMarker = "Skill worth keeping"`——once-per-session 靠从历史里读回这个 marker（`:871-878` `skillOfferAlreadyInHistory`）。**marker 一个字节都不能改**。
  - `:641-651` 闸门阈值：breadth `skillOfferMinToolResults = 8` + `skillOfferMinDistinctTools = 3`；depth `skillOfferRecoveredMinToolResults = 5` + 同工具失败后成功。**不动**。
  - `:653-659` 设计注释（定案，本计划遵守）：「the gate counts what it can count … and never judges whether the work itself deserves a skill; that judgment stays with the model, which can still stay silent when the gate is open.」
  - `:703-718` `Execute`：开闸后把提醒锚定在固定边界，后续每次迭代都带（含收尾轮）。**不动**。
  - `:723-773` 7 条硬抑制（feature off / 无项目根 / generator 不可用 / 子代理 / generator 自运行 / 已提醒过 / plan mode）+ 闸门判定。**不动**。
  - `:795-804` `reminder()`（**本计划改这里**），当前全文：
    ```go
    return fmt.Sprintf(`%s - one-time note.

    This session has just done substantial tool-driven work, which may be worth keeping as a reusable project skill. When you finish this work, judge it against the skill criteria in your instructions: if the steps are worth keeping, propose that in a single sentence at the very end of your final message, naming /%s; if they are not, say nothing about it. This reminder applies to finishing the current work only - it appears once and is not a standing instruction for later turns.`,
        skillOfferMarker, command)
    ```
    两个 `%s` 依次是 marker、命令名。**动词数量与顺序不动**。
  - 接线点：`pkg/run/runner.go:1074` `wrapSkillOfferLLM(...)`（在 plan-mode wrapper 之外）。
- `pkg/run/skills_test.go`
  - `:184-241` 三个 catalog 测试（zero-skills 也渲染、冻结块不重复渲染、off 时字节不变）只断言 `strings.Contains(got, "/skill-generator")`——新文本保留命令名，**不用动**。
  - `:607-870` 闸门/抑制/锚定测试（`TestSkillOfferGateOpensOnBreadth`、`...OnRecovery`、7 条 `Suppressed*`、`...AnchoredAcrossIterations`、`...BelowTheGate`）只数注入条数与 marker 存在性，**没有钉提醒逐字节的测试**——本计划新增一个。
  - `:540-565` `newOfferHarness(t)` 测试样板；`skillOfferLLM` 零值即可调 `reminder()`（只读 `commandName`，空则回退默认），新 golden 测试**不需要** harness。
- 开关：`pkg/config/config.go:149` `SkillOffer *bool yaml:"skill_offer"`，`:162` 默认 `true`；本仓 `forebrain.yaml:55-56` 显式 `skill_offer: true`（注释：「Shows the standing offer to capture a workflow as a skill」）。**默认值与配置都不动**。
- 全仓只有 `offer.go` + `offer_test.go` 含常驻准则文本、只有 `skills.go` 含提醒文本（已 grep 核实）；`pkg/architecture/testdata` 不含这两段文本（cache baseline 不涉及，已 grep 核实）；`docs/` 没有描述这两段文本的文档（已 grep 核实）。

### 必须遵守的仓库约定

- **模型可见文本 provider-neutral、字节稳定**（FOREBRAIN.md House rules）：不得出现模型/工具/厂商名；文本是编译期常量。本计划的两段新文本沿用原版的纯 ASCII 标点风格（` - `，不用 em-dash）。
- **测试先红后绿**：先改/加 golden 测试看到 FAIL，再改生产文本看到 PASS（本仓近期批次的既定做法）。
- **测试文件与生产文件同名对应**（`pkg/architecture` 强制）：新测试进既有 `pkg/run/skills_test.go`，不新建文件。
- **scratch home**（FOREBRAIN.md House rules）：任何 `go test` 都带 `FOREBRAIN_HOME="$(mktemp -d)"`，绝不继承环境里的真实 home。
- 注释风格：看 `offer_test.go:8-11` 与 `skills.go:627-630`——测试/常量上方写「为什么钉字节」的短注释，匹配它。

## Commands you will need

在本机已验证（2026-11-13，基线 `09b44fe3`；定向测试原记录的 0.447s/0.715s 是 `\|` 假绿、实为零测试，见 Status 的执行纠错——执行时已用修正正则真跑 19 PASS）。若 shell 里 `go` 不在 PATH（后台 shell 会遇到），先 `export PATH="/usr/local/go/bin:$PATH"`。

| Purpose | Command | Expected on success |
|---|---|---|
| 环境准备 | `export PATH="/usr/local/go/bin:$PATH"; export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"` | exit 0（PKG_CONFIG_PATH 是 FOREBRAIN.md 对直跑 go 编译 pkg/pdf 的要求） |
| 定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/skill ./pkg/run -run 'TestRenderSkillOfferGuidance|TestSkillOffer' -count=1` | 两包均 `ok` |
| pkg/skill 全量 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/skill -count=1` | `ok` |
| 架构守卫 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |
| 构建 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| Vet | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/skill ./pkg/run` | exit 0 |
| 格式 | `gofmt -l pkg/skill pkg/run` | 无输出 |

注意：`pkg/run` **全量**套件在本机历史基线上有既有失败（见 plans/README.md 2026-11-11 批次记录）。本计划的回归门禁 = 上表定向测试 + `pkg/skill` 全量 + 架构守卫；如额外跑 `pkg/run` 全量，FAIL 名单须与基线逐条比对，不得把既有失败算到本计划头上，也不得顺手修。

## Scope

**In scope**（只许改这 4 个文件）：
- `pkg/skill/offer.go`（仅 `offerGuidanceTemplate` 常量文本）
- `pkg/skill/offer_test.go`（仅 `TestRenderSkillOfferGuidanceGolden` 的 `want` 字符串）
- `pkg/run/skills.go`（仅 `reminder()` 的格式字符串）
- `pkg/run/skills_test.go`（仅新增两个测试函数，见 Step 2）

**Out of scope**（看着相关也不许动）：
- 闸门阈值、7 条抑制、`analyzeSkillOfferSignals`、锚定逻辑（`skills.go:627-773`、`:806-878`）——「闸门只数数、模型判价值」是设计定案；调阈值对本投诉无效（一次性长任务的工具数远超任何合理阈值）。
- `skillOfferMarker` 常量——once-per-session 靠它读历史，改了等于放开重复提醒。
- `RenderSkillOfferGuidance` 的签名/行为、`features.skill_offer` 默认值、`forebrain.yaml`。
- `pkg/skill/system_assets/skill-generator/`、`skill-workshop/` 等技能资产——它们是被调用时的操作手册，不是常驻 nudges。
- 任何「跨会话冷却」「每项目次数上限」的新状态/新配置——那是另一个设计决策，本计划不做。
- `pkg/run` 全量套件里的既有失败。

## Git workflow

- 分支：`fix/skill-offer-recurrence-evidence`（若由 `execute` 变体派发，用派发方指定的隔离 worktree 与分支）。
- Commit 遵循 Conventional Commits + `.gitmessage`（`Why:` / `What:` / `Verification:` 只写真跑过的命令），`git commit -s`。建议标题：`fix(skill): the offer asks for evidence a workflow will recur`。
- 不 push、不开 PR、不合并，除非派发方明确指示。

## Steps

### Step 1：常驻准则——先更新 golden（红），再重写模板（绿）

1a. 把 `pkg/skill/offer_test.go:14-20` 的 `want` 字符串替换为：

```
## Keeping finished work as a skill

/skill-generator writes a finished procedure into the project's skills directory so the next session starts with it. Most finished work is not worth keeping: length, difficulty, and hard-won debugging are not reasons to propose.

Propose only when the same procedure will demonstrably be needed again (the user has asked for it before, it is a documented recurring process, or this session already had to repeat it) and it carries choices someone starting over would trip on (a non-obvious flag, an isolation trick, a wait or retry condition). A one-off task stays unsaved however many steps it took: implementing a feature, fixing a specific bug, a migration, or an elaborate orchestration run once for one batch of work. Also not for answering by reading code, the repository's documented standard commands, or work an existing skill covers.

To propose, end the final message for that work with one sentence naming /skill-generator and the evidence that it will recur. Do it at most once per session and never again if ignored; when in doubt, say nothing about it.
```

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/skill -run TestRenderSkillOfferGuidance -count=1` → **FAIL**，报「skill offer guidance drifted from the golden bytes」（这就是预期的红）。

1b. 把 `pkg/skill/offer.go:30-36` 的 `offerGuidanceTemplate` 替换为（注意两个 `%s` 的位置与原版一致：第一段、第三段各一）：

```go
const offerGuidanceTemplate = `## Keeping finished work as a skill

%s writes a finished procedure into the project's skills directory so the next session starts with it. Most finished work is not worth keeping: length, difficulty, and hard-won debugging are not reasons to propose.

Propose only when the same procedure will demonstrably be needed again (the user has asked for it before, it is a documented recurring process, or this session already had to repeat it) and it carries choices someone starting over would trip on (a non-obvious flag, an isolation trick, a wait or retry condition). A one-off task stays unsaved however many steps it took: implementing a feature, fixing a specific bug, a migration, or an elaborate orchestration run once for one batch of work. Also not for answering by reading code, the repository's documented standard commands, or work an existing skill covers.

To propose, end the final message for that work with one sentence naming %s and the evidence that it will recur. Do it at most once per session and never again if ignored; when in doubt, say nothing about it.`
```

常量上方的注释（`:25-29`）保持不变。`RenderSkillOfferGuidance` 一个字节不动。

**Verify**：同 1a 命令 → **PASS**（`ok ... pkg/skill`）。

### Step 2：一次性提醒——先加 golden（红），再重写 reminder()（绿）

2a. 在 `pkg/run/skills_test.go` 的 `TestSkillOfferReminderAnchoredAcrossIterations` 结束之后（约 `:857`）、`TestSkillOfferSuppressedForRunsBelowTheGate` 注释之前，新增：

```go
// TestSkillOfferReminderGolden pins the exact bytes of the offer reminder.
// The wording is the gate's judgment half: it must keep the marker (the
// once-per-session rule reads it back from history) and must not drift back
// to affirmative framing. Changing the text is a deliberate act that lands
// in this file.
func TestSkillOfferReminderGolden(t *testing.T) {
	got := (&skillOfferLLM{}).reminder()
	want := `Skill worth keeping - one-time note.

This run tripped the skill-offer gate, but the gate measures length, not value: most long runs are one-off work and call for silence. When you finish this work, judge it against the skill criteria in your instructions: propose /skill-generator in a single sentence at the very end of your final message only if you can name concrete evidence the same procedure will recur; otherwise say nothing about it. This reminder applies to finishing the current work only - it appears once and is not a standing instruction for later turns.`
	if got != want {
		t.Fatalf("skill offer reminder drifted from the golden bytes:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if strings.Contains(got, "%!") {
		t.Fatalf("format verb leaked into the rendered reminder: %s", got)
	}
}

func TestSkillOfferReminderCommandName(t *testing.T) {
	if got := (&skillOfferLLM{commandName: "custom-namer"}).reminder(); !strings.Contains(got, "/custom-namer") {
		t.Fatalf("custom command name missing: %q", got)
	}
}
```

（`strings` 已在该文件 import；`skillOfferLLM` 零值可调 `reminder()`，无需 harness。）

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/run -run 'TestSkillOfferReminder' -count=1` → **FAIL**（golden 漂移；CommandName 应 PASS）。注意 `-run 'TestSkillOfferReminder'` 也会匹配既有 `TestSkillOfferReminderAnchoredAcrossIterations`，它必须保持 PASS。

2b. 把 `pkg/run/skills.go:800-803` 的格式字符串替换为（`fmt.Sprintf` 调用与两个实参 `skillOfferMarker, command` 不动；`reminder()` 上方 `:791-794` 注释不动）：

```go
	return fmt.Sprintf(`%s - one-time note.

This run tripped the skill-offer gate, but the gate measures length, not value: most long runs are one-off work and call for silence. When you finish this work, judge it against the skill criteria in your instructions: propose /%s in a single sentence at the very end of your final message only if you can name concrete evidence the same procedure will recur; otherwise say nothing about it. This reminder applies to finishing the current work only - it appears once and is not a standing instruction for later turns.`,
		skillOfferMarker, command)
```

**Verify**：同 2a 命令 → **PASS**（3 个测试全过）。

### Step 3：回归与静态门禁

```sh
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/skill ./pkg/run -run 'TestRenderSkillOfferGuidance|TestSkillOffer' -count=1
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/skill ./pkg/architecture -count=1
CGO_ENABLED=1 go build -tags fts5 ./...
CGO_ENABLED=1 go vet -tags fts5 ./pkg/skill ./pkg/run
gofmt -l pkg/skill pkg/run
grep -rn 'may be worth keeping' pkg/ --include='*.go'        # 期望无匹配
grep -rn 'Noticing work worth keeping' pkg/ --include='*.go' # 期望无匹配
git status --short                                          # 期望只有 4 个 in-scope 文件
```

**Verify**：测试两包均 `ok`；build/vet exit 0；gofmt 无输出；两条 grep 无匹配；git status 只有 `pkg/skill/offer.go`、`pkg/skill/offer_test.go`、`pkg/run/skills.go`、`pkg/run/skills_test.go`。

## Test plan

- **更新** `TestRenderSkillOfferGuidanceGolden`（`pkg/skill/offer_test.go`）：钉新常驻准则逐字节。样板就是它自己。
- **新增** `TestSkillOfferReminderGolden` + `TestSkillOfferReminderCommandName`（`pkg/run/skills_test.go`）：钉新提醒逐字节 + 自定义命令名。结构样板：`pkg/skill/offer_test.go` 的 golden 与 CommandName 两测试。
- **保持不变且必须全过**：闸门 breadth/recovery 边界（8/3、5/恢复）、7 条抑制、锚定不重复、catalog 三测试（zero-skills / 冻结 / off 字节不变）、disabled 渲染空、命令名归一化。它们断言的是机制不是文本，机制零改动所以不应需要任何修改——**若它们红了，说明你改到了不该改的地方，STOP**。
- 验证：Step 3 第一条命令，全部 PASS（含 2 个新测试）。

## Done criteria

- [ ] Step 3 全部命令达到预期（定向测试 ok、pkg/skill 与 pkg/architecture 全量 ok、build/vet exit 0、gofmt 空、两条旧文本 grep 无匹配）
- [ ] 两个 golden 测试钉的是本计划给出的新文本，逐字节一致（不是「差不多」）
- [ ] `git diff` 只触及 4 个 in-scope 文件；`offer.go` 里只有常量文本变化，`skills.go` 里只有 `reminder()` 格式字符串变化
- [ ] `skillOfferMarker`、闸门阈值、抑制逻辑、`RenderSkillOfferGuidance` 函数体、配置默认值零改动（`git diff` 逐行核对）
- [ ] 先红后绿证据留在执行报告里（1a、2a 的 FAIL 输出各一段）

## STOP conditions

停下报告、不要自行发挥，如果：

- Drift check 或 Current state 摘录与现场代码对不上（代码已漂移）。
- grep 发现这两段文本在第三个文件里还有钉死的副本（计划假设只有两处）。
- 任一验证命令合理修复后仍连续两次失败。
- 你觉得必须改闸门逻辑、marker、配置或技能资产才能达成目标——那是范围外，报告而不是动手。
- 既有机制测试（闸门/抑制/锚定/catalog）变红——说明改错了地方。
- 额外跑 `pkg/run` 全量时出现超出历史基线的失败——报告名单，不顺手修。

## Maintenance notes

- **前缀缓存**：常驻准则字节变化会让**新**会话的缓存前缀重计费一次——golden 注释明写这是 deliberate act，本计划就是那次 deliberate act；既有会话按 `catalogForSession` 冻结旧字节直到会话结束。不要把这次变化拆进多个提交反复触发。
- **marker 不可变**：`Skill worth keeping` 被 `skillOfferAlreadyInHistory` 从持久化历史里读回；改它等于对老会话放开重复提醒。未来改提醒文本可以，改 marker 必须连带迁移历史检测。
- 未来任何对这两段文本的修改都必须同步更新两个 golden（`offer_test.go`、`skills_test.go`），评审时逐字比对。
- **如果落地后仍然过度推荐**：下一根杠杆是闸门设计（例如引入「同类流程再次出现」的可数信号），不是调阈值；先收集真实误推荐案例再立项。本计划有意不动闸门——「闸门只数数、模型判价值」是 `skills.go:653-659` 的设计定案。
- **现成逃生门**：`features.skill_offer: false` 整体关闭推荐（`/skill-generator` 命令本身仍可用），配置见 `pkg/config/config.go:149/162` 与 `forebrain.yaml:55-56`。owner 若想立即静音可改配置，无需等本计划。
- 真机验收（可选，owner 手动）：用一个一次性多步任务（如「实现某功能并跑测试」）确认收尾不再出现推荐句；用一个真正复发的流程（如第二次执行同一套发布步骤）确认仍会推荐。