---
name: plan-gated-bugfix
description: 修复 bug 的标准工作流：bug 报告 → enter_plan_mode → improve 计划文件（docs/plan/）→ exit_plan_mode 审批 → 当前源码实施 → -tags fts5 测试 + tmux 真机验收。收到 bug/缺陷报告、需要定位根因并彻底修复时使用。
---

# Plan-gated bugfix：审批门控的 bug 修复工作流

owner 指定的流程（原话）："bug → enter_plan_mode → improve 计划 → exit_plan_mode →
当前源码实施 → tmux 真机验收"。核心纪律：**计划未经 owner 批准，不得改任何源码**。

## 前置条件

- improve skill 可用（`<project>/.claude/skills/improve/`，含 `references/plan-template.md`）。
- CGO 工具链可用（CGO 是本仓库硬性要求：silk、sqlite、fts5）。
- tmux 可用（真机验收需要 pty）。
- 快速记忆检索：先搜项目 MEMORY.md（本仓库裁决多、检索词 tulkun/forebrain 都要试）。

## 流程步骤

### 1. 定位根因（动代码之前）

- 用 codegraph_explore + 定点 shell 取证，形成 **file:line 证据链**，并到现场验证
  （如用户 home 里的落盘文件、`<home>/logs/error.log`）——根因必须可证伪，不接受推测。
- 语义判据先找已有实现：本仓库大量"同一语义多处存在、唯独出错路径没用它"的 bug
  （例：first-setup 判定与 onboarding 门槛共用 `ValidateMainAgentLLMConfigured`）。
  优先把出错路径归一到已有语义，而不是新造判定。

### 2. enter_plan_mode 进入计划模式

- 收到 bug 后进入实施前，先 `enter_plan_mode`；计划模式内只做只读调查和写计划文件。
- 风险点、方案取舍类问题在计划模式内用 user_interaction 停下来问；明确的 bug 直接修。

### 3. 写 improve 计划文件

加载 improve skill，用其 **`plan <description>` 变体**（问题已明确，跳过全库审计，
只做定点调查），按其 `references/plan-template.md` 规范写**自包含**计划（执行者零上下文）。

落盘位置：`docs/plan/<大写蛇形>_PLAN.md`（本仓库惯例，**不是** improve 默认的 `plans/`；
参照既有计划如 `docs/plan/SKILL_MESSAGE_CARD_UI_PLAN.md` 的结构）。

必含章节：

1. 目标（Why this matters）
2. 根因（file:line 证据链 + 现场证据）
3. 修复设计（含**行为变化矩阵**：每个受影响场景 旧行为→新行为，标注哪些是刻意修订）
4. Scope（In scope / Out of scope——"看起来相关但明确不碰"的文件要列名）
5. Steps：每步带验证命令与预期输出
6. Done criteria（机器可查）
7. STOP conditions（对准本修复的真实风险，不写空话）
8. Maintenance notes

基线：仓库可能没有 git commit（`git rev-parse HEAD` 失败）——"Planned at" 记工作区状态，
drift 检查以计划内 file:line 对照代替 SHA diff。

### 4. exit_plan_mode 请求审批

- `exit_plan_mode` 提交计划给 owner；**批准前不得改任何源码**（本技能存在的原因：
  2026-09-28 会话曾只写计划就径直实施，被 owner 纠正缺审批门）。
- 计划被否：回步骤 2 修订计划，不得先斩后奏。

### 5. 批准后：直接在当前源码实施

- owner 裁决（原话"直接在当前项目源码上实施计划"）：**不用 git worktree、不用快照**；
  改动留工作区由 owner 审阅；计划文件本身禁止 commit。
- 与 improve skill 的 closing-the-loop worktree 隔离流程冲突时，以 owner 裁决为准。
- 实施中发现新 bug（含既有缺陷）：owner 裁决"必须定位根因并彻底修复"——并入当前计划
  继续修，并在计划文件补记；方案取舍类仍停下来问。
- 为不可达状态不写防御代码；覆盖取值用白名单收尾（仓库既有裁决）。

### 6. 测试验证

```bash
CGO_ENABLED=1 go build ./...                                   # 编译
CGO_ENABLED=1 go test -tags fts5 ./pkg/<改动的包> -run '<用例>' -count=1   # 定点
CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1        # 包级回归
go vet ./...                                                    # CI 同款
```

- 受行为修订影响的既有断言：随契约更新断言并注明理由；为 bug 前置状态补**直接回归测试**
  （例：`TestNeedsFirstSetupTrueAfterHomeEnsureSeedsSkeleton` 先触发播种再断言判定）。
- 注意 `process.Resolve()` 有进程级缓存：测试里 `process.ResetResolve()`（t.Cleanup 同样）。

### 7. tmux 真机验收（必做，测试绿不等于真机可用）

**构建必须带 `-tags fts5`**（Makefile `GOFLAGS := -trimpath -tags fts5`）。漏 tag 的失败
模式：TUI 报 `open database: migrate state schema: no such module: fts5` 退出——这是
验收工具链错误，**不是产品 bug**，别去"修"它。

```bash
go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
tmux new-session -d -s fbt -x 120 -y 42 \
  'cd <隔离项目目录> && FOREBRAIN_HOME=<隔离临时home> ./build/bin/forebrain; echo EXIT_CODE=$?; sleep 300'
tmux send-keys -t fbt <按键>    # ↑/↓/Enter/j/k 驱动向导与选择器
tmux capture-pane -t fbt -p     # 取证屏幕
```

四类场景模板（按 bug 裁剪）：

1. **复现→修复**：构造 bug 前置状态，驱动完整用户路径，断言新行为（逐屏 capture 取证）。
2. **回归**：正常配置路径行为不变（如二次启动直达 TUI）。
3. **中断自救**：用户中途退出（Esc/Ctrl+C）→ EXIT_CODE 干净 → 再启动可恢复，
   不落入死胡同。
4. **非 TTY 行为不变**：`forebrain gateway start </dev/null` 等无终端路径仍报诊断、不挂起。

注意：交互式启动的第一个屏幕是 cwd 的 workspace trust 页（在项目目录下跑才会出现预期流程）；
验收用临时 `FOREBRAIN_HOME` 与临时项目目录，**不得污染真实 `~/.forebrain*`**。

### 8. 收口

- 计划文件回填：状态行改"已实施并真机验收通过（日期）"，新增"验收记录"章节
  （场景→证据，含验收中踩过的工具链坑）。
- 清理验收环境（tmux kill-session、临时目录）。
- 报告：根因链、最小改动清单、验证证据、遗留限制；改动留工作区不 commit。
- 若用户消息中沉淀了新的工作方式裁决，用 memories_add_ad_hoc_note 记忆。

## 失败模式与规避

| 坑 | 规避 |
|---|---|
| 只写计划就实施（漏审批门） | 步骤 4 是硬门：批准前零源码改动 |
| 二进制漏 `-tags fts5` | 步骤 7 的构建命令已带；见到 `no such module: fts5` 先查构建 tag |
| 验收污染真实 home | 一律临时 `FOREBRAIN_HOME` + 临时项目目录，结束清理 |
| 计划不自包含（"如前所述"） | 执行者没看过本会话；file:line + 代码摘录内联进计划 |
| 测试受 `process.Resolve` 缓存污染 | 测试开头 `process.ResetResolve()` |
| 用 worktree/快照实施 | owner 已否决；直接当前源码改 |
| 治标补丁（症状看不见） | owner 只收根因修复；定位不到根因就继续查，不升级为防御代码 |

## 成功判据

- 根因有 file:line 证据链 + 现场证据，修复归一到既有语义而非新造判定。
- 计划文件完整、经 `exit_plan_mode` 批准、事后回填验收记录。
- 定点测试 + 包级回归 + `go vet` 全绿；tmux 真机四场景（按 bug 裁剪）取证通过。
- `git status` 改动范围与计划 Scope 一致，留在工作区未 commit。
