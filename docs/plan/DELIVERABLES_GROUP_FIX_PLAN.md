# 修复交付物列表：同路径重复行 + 子代理 worktree 改动遗漏

> 本文件是会话计划（审批用）。实施第 0 步将其复制到仓库惯例位置
> `docs/plan/DELIVERABLES_GROUP_FIX_PLAN.md`（plan-gated-bugfix 技能要求），
> 并在实施结束后把验收记录回填到该副本。

## 0. 基线与漂移检查

- 基线：`dev` @ `921e9a1d`（`git rev-parse --short HEAD`），工作区干净（`git status --porcelain` 空）。
- 漂移检查（内容 mismatch 才 STOP，行号移动不算）：
  - `pkg/tui/reducer.go`：`func appendDeliverables(existing, rows []tool.Deliverable)` 存在且体内含 `if row.ID != "" && have.ID != ""`（ID 键去重）。
  - `frontend/src/lib/artifacts.ts`：`export function dedupeArtifacts(rows: ArtifactView[])` 存在且体内含 `if (seen.has(row.id)) continue`。
  - `pkg/agent/coding_delivery.go`：`CodingDeliveryPrompt` 含 `another agent's concurrent changes`。
  - `pkg/tui/reducer_test.go`：`TestDeliverablesGroupDedupKeyIsArtifactID` 存在。
- 构建环境：CGO 必需；直接 go 命令编译 pkg/pdf 按 CI 同款携带
  `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`
  （Makefile 只在 make 内设置；本机实测不带也能构建，但携带它与 CI 一致、最稳）；
  测试与构建一律 `-tags fts5`（漏 tag 报 `no such module: fts5` 是工具链错误，不是产品 bug）。
- 并发代理：实施开始时 `peer_list` 检查同树代理（当前 forebrain-harness-79
  在做 review-plan plans/339-342，与本 scope 无重叠）；若其 scope 与本计划
  in-scope 文件相交，先 peer_send 协调再动那些文件；不碰不是自己改的文件。

## 1. 目标（Why this matters）

owner 报告 TUI 一轮对话结束后的交付物列表两个缺陷（截图为证）：

1. **重复文件**：同一文件（plans/README.md +6 -4）在交付组里出现两行。
2. **子代理 worktree 改动遗漏**：主代理派发子代理在 worktree 里改/建的文件
   （execute 334 轮：pkg/agent/subagent_history.go、pkg/run/subagent.go、
   pkg/run/subagent_test.go，+108/-4）不在主代理交付组里，组里只有主代理自己
   编辑的 plans/README.md。owner 裁决：如确认是 bug，本期一并修复。

交付组是用户拿本轮产物的唯一入口（TUI 点击开文件、web 下载/预览），重复行
误导清点、遗漏行让用户拿不到子代理的劳动成果。两个缺陷都在交付组投影层，
存储与引擎注册行为本身正确，不动。

## 2. 根因（file:line 证据链 + 现场证据）

### Bug 1：组去重键用了「版本行 ID」而不是「文件路径」

现场证据（`~/.forebrain/state/forebrain.state.sqlite`，只读查询）：
截图对应 run `1eee25e8-383c-4ff8-af4e-17943112a76f` 的 `fb_artifacts` 共 4 行，
全部 `origin=model`：

| id | tool_call_id | rel_path | size | sha256(前12) | created_at |
|---|---|---|---|---|---|
| art-323fb85e | call_f73396aabe4c4ffb9 | plans/README.md | 869854 | a98855b9b071 | 1795162028 |
| art-661efdbe | call_ce38c882407447e99 | plans/README.md | 871486 | 86c420496b0e | 1795163183 |
| art-9e739eab | call_ce38c882407447e99 | .worktrees/fix-usable-models-empty-routing/…/ToolCallCard.vue | 28519 | 9baf290c75a3 | 1795163183 |
| art-f926ea9c | call_ce38c882407447e99 | …/ToolCallCard.test.ts | 29696 | 80ee5151f16a | 1795163183 |

→ 模型在同一 run 内**两次** present_files 同一路径（两个不同 tool call、
不同 sha/size = 两个版本行）。这是设计行为：present_files 每次呈现都
`AddArtifact` 新行（`pkg/tool/artifacts.go:285`），工具描述明说
"Presenting the same path again later records a new version"。

因果链：
1. run 终态帧携带该 run 的**全部**版本行：
   `ListArtifactsForRun`（`pkg/state/artifact_store.go:254`，按 delivery order
   返回所有行）→ `RunArtifactsOf`（`pkg/turn/events.go:134`）。
2. TUI 交付组 = live presented 行 ∪ 终态帧行，合并函数
   `appendDeliverables`（`pkg/tui/reducer.go:446-467`）**只按 artifact ID 去重**：
   ```go
   if row.ID != "" && have.ID != "" {
       if have.ID == row.ID { duplicate = true; break }
       continue
   }
   if have.RelPath == row.RelPath && have.Size == row.Size && have.Title == row.Title { ... }
   ```
   两版本 ID 不同 → 都保留。live 路径 `reducer.go:1655-1664`
   （`takePendingDeliverables()` ∪ `m.RunArtifacts.Artifacts`）、replay 路径
   `pkg/tui/commands.go:793-825`（`replayFrameDeliverables` 逐行调同一函数）都走它。
3. web 同病：`dedupeArtifacts`（`frontend/src/lib/artifacts.ts:390-399`）
   `if (seen.has(row.id)) continue`；`turnDeliverables`（:438-450）
   `dedupeArtifacts([...plan, ...presented, ...frame, ...fallback])`。
4. 组契约本身是「one row per file」——`pkg/tui/reducer.go:69-70` 注释：
   "FrameDeliverables is a turn's 'deliverables' group: one row per file the
   turn presented"。ID 是**版本行**身份，不是**文件**身份；键错了。
5. 既有测试 `TestDeliverablesGroupDedupKeyIsArtifactID`
   （`pkg/tui/reducer_test.go:6044-6067`）把错误行为钉成契约（断言两版本都保留），
   需随修复改写。

排除的竞争解释：
- live+replay 双画：组每 turn 只画一次（`takePendingDeliverables` 取走即清空，
  `reducer.go:528-536`；replay 是另一次加载），截图是**一个组内两行**，不是两个组。
- fallback/top-up 重登：4 行全 `origin=model`、两个不同 tool_call_id、sha 不同
  → 两次真实呈现，不是引擎补登。
- legacy 无 ID fallback 失配：行都带 ID，重复发生在两个**不同 ID** 之间。

### Bug 2：coding 交付契约提示词对「派发子代理 + worktree」自相矛盾

现场证据（同一状态库）：execute 334 轮 = session
`cli-1f6fddde-09c5-488e-b30d-2fe316233133` / run
`437e3e10-6f21-438a-ac1c-53e3653ace52`（created_at 1795151618）：
- `fb_artifacts` 该 run 仅 1 行：`art-cd0e575c` plans/README.md origin=model。
- 唯一 present_files 结果行：`presented` 仅 README.md；**无** present_files 失败行。
- 主代理 reasoning（fb_messages.parts carry）逐字：
  "Should I present files? This turn created/modified: plans/README.md (index
  update) in project dir. Per handover rule, every file this turn modified
  inside project directory is a deliverable. So call present_files with
  plans/README.md." → 清点只算自己的编辑。
- 执行器子代理会话 `main:cli-1f6fddde…:ed557bbf…:85033fea…` 的派发结果
  （fb_messages id 540973）含**显式一行**：
  `FILES CHANGED: pkg/agent/subagent_history.go, pkg/run/subagent.go,
  pkg/run/subagent_test.go (3 files, +108/−4 vs 9a3fba8c)`；主代理最终文本
  原样复述「+108/-4」→ **它读到了这份清晰清单，清点步骤仍把它丢了**。
- worktree 当时仍在（reasoning: "worktree remains"）；`.worktrees/` 在项目根内，
  present_files 校验可通过（bug 1 截图里 worktree 文件成功呈现为证）。

根因（证据证实的失败模式）：`pkg/agent/coding_delivery.go` 的
`CodingDeliveryPrompt` **所有权语义失败**——模型不把派发子代理的改动算作
"this turn modified"。reasoning 逐字引用的是第一段规则（"Per handover rule,
every file this turn modified inside project directory is a deliverable"），
清点只算自己直接写的编辑；通篇没有考虑过子代理文件再排除的推理。
契约文本有三处共同造成这个读法（修复 §3.2 三处都堵上）：
1. 第一段 "every file this turn created or modified" 未定义所有权——派发
   子代理的改动算不算 "this turn" 不明；第二段包含句 "the files your subagents
   produced (they report their paths back to you)" 太远且弱，没被清点步骤用上。
2. 排除句 "the project may carry older uncommitted work or **another agent's
   concurrent changes**, and those are not yours to deliver"——被派发子代理
   字面上就是 "another agent"，其 worktree 分支改动形似 "concurrent changes"，
   给「不算我的」提供现成理由（竞争解释之一，reasoning 未显式引用，不独断）。
3. 全文无 worktree 字样，而 harness 自身推荐 worktree 派发
   （`GitWorktreePrompt` 的 `.worktrees/<name>` 约定）；子代理报告路径相对
   **它自己的 checkout**，主树同名副本本轮未改，模型即使想清点也缺定位规则。

→ 这是 harness owned 的**契约文本缺陷**，不是显示/存储 bug。owner 既有裁决
（2026-11-19，plan 330）：交付资格 prompt-driven、harness 禁启发式兜底
（`collectTurnDeliverables` 已删）→ 修复 = 澄清契约文本，**不是**加引擎自动注册。

## 3. 修复设计

### 3.1 Bug 1：交付组按文件路径去重（TUI + web 同改）

契约：**一行一文件**；同路径后到的行（新版本，或终态帧重复 presented 行）
**更新该行内容、保持首次呈现的位置**。各来源都按 delivery order 喂入，
"后到内容胜出"在 live/replay/reload 三路都收敛到最新版本。

TUI（`pkg/tui/reducer.go`）：
- 新增键函数（放在 `appendDeliverables` 旁）：
  ```go
  // deliverablePathKey is the file identity a deliverables group dedupes by:
  // the path the group draws (RelPath, falling back to Name), cleaned and
  // slashed. The artifact id identifies a version row — present_files records
  // a new row each time a path is presented — so it cannot key the group.
  func deliverablePathKey(d tool.Deliverable) string {
      p := strings.TrimSpace(d.RelPath)
      if p == "" { p = strings.TrimSpace(d.Name) }
      if p == "" { return "" }
      return filepath.ToSlash(filepath.Clean(p))
  }
  ```
  （`path/filepath` 已在该文件导入，:10。）
- `appendDeliverables` 改为：空键行照旧 append（画不出来的行，保持原行为）；
  否则线性找同键槽位，找到则 `existing[i] = row`（原位更新），找不到 append。
  删除 ID 比较与 RelPath+Size+Title composite fallback（路径键统一覆盖 legacy 行）。
- 同步改注释：`appendDeliverables`  doc（:439-445）、`frameDeliverable` doc
  （:469-478，"dedupes by it" 措辞）、live union 注释（:1648-1654，
  "deduped by artifact id" → 按路径一行一文件）。
- 不需要 cwd 相对化：present_files/batch 写入方 RelPath 恒为项目相对路径
  （`artifacts.go:279-284`、`batch.go:443-446`）；plan 行 RelPath=绝对 Path
  （`session_tools.go:548` 不设 RelPath，`frameDeliverable` fallback 到 Path），
  每 turn 唯一。同文件「绝对+相对」两种拼写共存不可达 → 不写防御代码（owner 裁决）。

web（`frontend/src/lib/artifacts.ts`）：
- `dedupeArtifacts(rows, root?)` 键 = **relPath 优先，回退显示标签**：
  ```ts
  const groupKey = (row: ArtifactView, root?: string): string => {
    const rel = String(row.relPath ?? '').trim().replace(/\\/g, '/')
    return rel || displayPathOf(row, root)
  }
  ```
  为什么 relPath 优先而不是纯 `displayPathOf`：presented 行只有 relPath，
  frame 行有 path+relPath。**root 未知时**（可达：状态库有 8 个 project_id
  为空的会话，`pkg/state/project_store.go:1127-1128` ProjectForSession 返回
  false → 会话信息 project=null → ChatView `sessionProject.value?.root`
  undefined）`displayPathOf` 不相对化绝对 path，纯标签键会把旧 ID 键本能
  合并的 presented+frame 同 ID 行拆成两行——那是本次改动引入的回归，必须堵。
  relPath 优先后：presented/frame/fallback 行恒带项目相对 relPath（写入方
  `pkg/tool/artifacts.go:279-284`、`pkg/turn/batch.go:443-446`），跨源同键，
  与 root 无关；plan 行无 relPath（`pkg/tool/session_tools.go:548` 不设），
  envelope 与 frame plan 行都带同一绝对 path → 回退标签键仍合并；legacy
  name-only 行回退 name。实现：
  ```ts
  export function dedupeArtifacts(rows: ArtifactView[], root?: string): ArtifactView[] {
    const indexByKey = new Map<string, number>()
    const out: ArtifactView[] = []
    for (const row of rows) {
      const key = groupKey(row, root)
      if (!key) { out.push(row); continue }
      const at = indexByKey.get(key)
      if (at === undefined) { indexByKey.set(key, out.length); out.push(row) }
      else { out[at] = row }
    }
    return out
  }
  ```
- `turnDeliverables` 把已有的 `root` 传进 `dedupeArtifacts`；两处 doc 注释
  （"merged by id" / "Rows by id"）改为按路径、最新版本内容、首次位置。
- 回归测试必补：root=undefined，presented(relPath-only)+frame(path+relPath)
  同 ID → 1 行；root=undefined 同 relPath 两版本 → 1 行。

点击/下载语义不变：TUI 按路径开文件（`deliverableAtScreenRow`，
reducer.go:3113-3152）；web content 路由对任何 id 都读磁盘当前内容，保留
最新 id 使 size/sha 元数据与文件一致。

### 3.2 Bug 2：澄清 CodingDeliveryPrompt 第二段

`pkg/agent/coding_delivery.go:18` 第二段替换为（其余段字节不动）：

> Before finishing a turn that created or modified files, account for the full
> set: your own file writes and edits, the effects of every shell command you
> ran, and every file your subagents created or modified on your behalf. A
> subagent you dispatched works for you: its changes are this turn's changes,
> including files inside a worktree it worked in (a separate checkout under the
> project, such as .worktrees/<name>). Account for the paths it reports back;
> when its report is unclear, verify against that checkout instead of leaving
> its files out. Its report's paths are relative to that checkout — locate and
> present them under that worktree's own directory (for example
> .worktrees/<name>/…); the same paths in your main checkout are not the files
> this turn changed. When the set is not obvious, verify it against the
> filesystem — for example, files whose modification time falls inside this
> turn — instead of guessing. List only files this turn actually changed: the
> project may carry older uncommitted work or changes by agents you did not
> dispatch, and those are not yours to deliver. When you judge the turn's work
> complete and the files final, call present_files once with every created or
> modified file.

要点：① 所有权句直对已证实的失败模式——派发子代理的改动 = 本轮改动
（"its changes are this turn's changes"），显式命名 worktree 与 `.worktrees/<name>`；
② 报告不清时对该 checkout 核实，而不是漏掉；③ 报告路径相对子代理自己的
checkout，须在该 worktree 目录下定位/呈现，防止「开始清点却呈现主树未改副本」
的新失败模式；④ 排除句收窄为 "agents you did not dispatch"（未派发的并发代理），
语义不变、歧义消除。

约束（既有测试）：`TestCodingDeliveryPromptIsProviderNeutralAndNamesNoOtherTool`
（coding_delivery_test.go）禁小写子串 "git"、工具名、URL 等 → 新文案用
"worktree"/".worktrees/<name>"（不含 "git"），已逐词核对通过。
`TestCodingDeliveryPromptCoversTheCompletenessRule` 钉的
"another agent's concurrent changes" 随契约修订更新为 "agents you did not
dispatch"，并新增钉 "its changes are this turn's changes"、
"including files inside a worktree"、"relative to that checkout"。
`pkg/run/runner_test.go:2800` 按常量拼接断言，内容改不影响。

### 3.3 行为变化矩阵

| 场景 | 旧 | 新 | 刻意修订? |
|---|---|---|---|
| 同路径一轮内呈现两次（TUI live/replay、web live/reload） | 两行 | 一行（最新版本内容、首次位置） | 是·bug1 修复 |
| 同 ID 重复喂入 | 一行 | 一行 | 否 |
| legacy 无 ID 行同路径同 size/title | 一行 | 一行 | 否 |
| legacy 无 ID 行同路径不同 size/title | 两行 | 一行 | 是·同一文件标签相同，一行 |
| 不同目录同文件名 | 两行 | 两行 | 否 |
| presented ∪ frame 同文件、plan ∪ frame plan（root 已知/未知） | 一行 | 一行 | 否 |
| coding 轮派发 worktree 子代理 | 模型不把子代理改动算作本轮（所有权语义失败） | 契约显式：子代理改动=本轮改动，含 worktree 内 | 是·bug2 修复 |
| 子代理报告路径的呈现拼写 | 无规则（可能呈现主树未改副本） | 显式：在 worktree 目录下定位/呈现 | 是·bug2 配套 |
| 未派发代理的并发改动 | 排除 | 排除（措辞收窄） | 否·语义不变 |
| wire 终态帧 / fb_artifacts / 会话文件列表 | 携带全部版本 | 不变 | 否·集成方数据日志与版本历史是另一契约 |

## 4. Scope

In scope（仅此八文件 + 计划副本）：
- `pkg/tui/reducer.go`、`pkg/tui/reducer_test.go`
- `frontend/src/lib/artifacts.ts`、`frontend/src/lib/artifacts.test.ts`
- `pkg/agent/coding_delivery.go`、`pkg/agent/coding_delivery_test.go`
- `pkg/tool/artifacts.go` —— **仅注释**：Deliverable.ID doc（:327-329，
  "the identity both surfaces dedupe a turn's group by…fall back to the
  composite key"）修复后 actively 误导，改为「ID 是版本行身份；组按文件路径
  去重」。逻辑与冻结的工具描述一字不动。
- `frontend/src/views/ChatView.vue` —— **仅注释**：deliverablesOf doc
  （~:1803 "merged by id"）改为按路径合并。逻辑不动。
- `docs/plan/DELIVERABLES_GROUP_FIX_PLAN.md`（本计划副本，第 0 步创建）

Out of scope（看起来相关但明确不碰）：
- `pkg/tool/artifacts.go` 的 present_files 逻辑：版本注册是设计行为；工具描述 owner 冻结。
- `pkg/state/artifact_store.go`：版本历史存储不动。
- `pkg/turn/events.go` `RunArtifactsOf` / gateway 终态帧：集成方结构化结果仍带
  全部版本行（数据日志，`pkg/gateway/wsevents_test.go:2322`
  TestRunEndFramesCarryArtifacts 明钉），surface 投影层收敛。
- `pkg/tui/commands.go` `replayFrameDeliverables`、`pkg/tui/render.go`
  `renderDeliverables`：走修好的 `appendDeliverables` / 画组内行，无需改。
  （commands.go:791 与 reducer.go 旧 union 注释里的 "turn-end top-up" 措辞已陈旧
  ——top-up 随 plan 330 删除，现存唯一 fallback 注册是 pkg/turn/batch.go:461；
  reducer.go 的 union 注释随本次重写顺手改对，commands.go 的属既有陈旧，
  不在本期扩散，收口报告一行带过。）
- `pkg/agent/office_prompts.go`（OfficeWorkPrompt/BatchTaskPrompt）：无该排除句，
  office 子代理提示词已明确要求报告路径，无此矛盾。
- web `ArtifactList.vue` / 会话文件页：版本列表是另一功能，保留版本。

## 5. Steps（每步带验证）

**Step 0 — 计划落仓库 + 漂移检查**
- 复制本文件到 `docs/plan/DELIVERABLES_GROUP_FIX_PLAN.md`。
- 跑 §0 漂移检查四条 grep；任一内容 mismatch → STOP。
- 验证：`git rev-parse --short HEAD` = 921e9a1d（或记录前进后的尖并重新核对摘录）。

**Step 1 — TUI 路径键去重 + 测试**
- 改 `pkg/tui/reducer.go`（§3.1）；改写
  `TestDeliverablesGroupDedupKeyIsArtifactID` → `TestDeliverablesGroupOneRowPerFile`：
  v1/v2 同路径不同 ID → 1 行且内容=v2；夹在其它文件间位置保持；同 ID 两次→1；
  legacy 无 ID 同路径→1；不同目录同文件名→2。
- 新增 replay 回归：`replayFrameDeliverables` 喂一个终态事件含同路径两版本行 → 1 行（最新）。
- 验证：
  ```bash
  export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
  CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -run 'Deliverable|Replay' -count=1
  ```
  预期：PASS（含既有 `TestDeliverablesGroupUnionsFrameArtifacts`、
  `TestDeliverablesGroupDedupesRows`、`TestDeliverablesGroupClosesTheTurnOnce`、
  `TestReplayDrawsTheGroup*`、`TestReplayKeepsOneGroupPerTurnAcrossRuns` 不红）。

**Step 2 — web 路径键去重 + 测试**
- 改 `frontend/src/lib/artifacts.ts`（§3.1）；`artifacts.test.ts` 新增：
  同路径两版本跨 presented/frame/fallback → 1 行、id=最新、位置=首次；
  不同目录同文件名 → 2 行；**root=undefined 时 presented(relPath-only)+
  frame(path+relPath) 同 ID → 1 行**（B1 回归）；root=undefined 同 relPath
  两版本 → 1 行。
- 既有会红的用例（随契约修订 fixture，注明理由）：
  `artifacts.test.ts:170-181`「puts the kept plan first, once, even when the
  frame lists it too」——其 frame p1 fixture 只有 name，而 plan envelope 带
  path（fixture 为相对 'plans/年度审计计划.md'），键不一致会拆成两行。
  修法：frame plan 行 fixture 带上与 envelope 相同的 path（生产帧 plan 行
  恒带 Path，`pkg/tool/session_tools.go:551`），origin 'plan' 断言保持。
  「merges every source by id」（:141）fixture 的 relPath==name，天然仍绿，不动。
- 验证：`cd frontend && pnpm vitest run src/lib/artifacts.test.ts && pnpm typecheck`
  预期：全绿。

**Step 3 — 提示词澄清 + 测试**
- 改 `pkg/agent/coding_delivery.go` 第二段（§3.2 全文）；更新
  `coding_delivery_test.go` 完整性断言（换排除句钉、加三条新钉）。
- 验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/agent -run 'CodingDelivery' -count=1`
  预期：PASS，中性测试不红（新文案无禁词）。

**Step 4 — 包级回归 + CI 同款门禁**
- ```bash
  CGO_ENABLED=1 go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1
  gofmt -l cmd pkg third_party plugins        # 须零输出
  CGO_ENABLED=1 go vet -tags fts5 ./...
  cd frontend && pnpm test && pnpm typecheck
  ```
  预期：全绿。受行为修订影响的既有断言只允许 §3.3 矩阵列出的那些，随契约更新并注明理由。

**Step 5 — tmux 真机验收 bug 1（确定性）**
- 构建：`CGO_ENABLED=1 go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain`。
- 加载 `run-forebrain` 技能（`.claude/skills/run-forebrain/`，fake_provider.py /
  model_seed.sh / driver.sh），按其 fake-provider 配方在隔离 tmux 会话
  （`FOREBRAIN_HOME=$(mktemp -d)` + 临时项目目录）驱动一个活体轮，脚本序列：
  `[present_files(P), shell(改 P 内容), present_files(P), present_files(Q)]`
  然后结束轮——两次呈现之间插一个 shell 写，造出真实不同字节版本。
  `tmux capture-pane -p` 取证：交付组 P、Q **各恰好一行**。
  （TUI 行只画路径不画 size，「最新内容胜出」由 Step 1 单测钉；真机钉一行一文件。）
- replay 面：退出该 scratch 会话后 resume 同一会话（免手 seed 事件表），
  capture 取证组内仍每文件一行。
- 回归面：正常单呈现轮组行为不变；Esc 中断轮 EXIT_CODE 干净。
- 不得污染真实 `~/.forebrain*`。

**Step 6 — tmux 真机验收 bug 2（真实模型探针，best-effort）**
- scratch home 复制真实 home 的 forebrain.yaml/auth.json；状态库用
  `sqlite3 "file:$HOME/.forebrain/state/forebrain.state.sqlite?mode=ro" ".backup '<scratch>/state/forebrain.state.sqlite'"`
  （WAL 下裸拷 .sqlite 会丢已提交帧）。只读复制，结束删除；密钥不落任何
  markdown/计划文件。临时项目目录里给一个会触发 worktree 派发的小任务
  （派子代理在 worktree 建一个文件后收轮），观察交付组是否含该 worktree 文件
  （且拼写在 .worktrees/<name>/ 下）。用与失败轮同族的模型（fb_llm_models 现配）。
- 若仍漏：最多一轮文案迭代再试；仍漏 → STOP 并报告（prompt-driven 机制下
  合规是模型判断，不无限循环）。若环境无可用 provider → 如实记录限制，
  以金标测试 + §2 因果证据为验收，留 owner 下一真实轮观察。

**Step 7 — 收口**
- 计划副本回填「验收记录」（场景→证据，含工具链坑）；状态行改「已实施并真机验收通过（日期）」。
- `tmux kill-session`、删临时目录；`git status` 改动 ⊆ §4 scope；改动留工作区**不 commit**。
- 报告：根因链、最小改动清单、验证证据、遗留限制。

## 6. Done criteria（机器可查）

- [ ] §0 漂移检查通过；`git status --porcelain` 改动文件集 ⊆ §4 in-scope。
- [ ] `grep -n 'row.ID != ""' pkg/tui/reducer.go` 零命中（ID 键已除）。
- [ ] `go test -tags fts5 ./pkg/tui ./pkg/agent -count=1` PASS；
      `TestDeliverablesGroupOneRowPerFile` 与 replay 版本回归存在且 PASS。
- [ ] `pnpm vitest run src/lib/artifacts.test.ts` PASS（含 root=undefined 同 ID/同路径合并回归）；`pnpm typecheck` exit 0。
- [ ] `gofmt -l cmd pkg third_party plugins` 零输出；`go vet -tags fts5 ./...` exit 0。
- [ ] `go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` PASS；`pnpm test` PASS。
- [ ] tmux capture 证据：bug1 活体+replay 组内同路径一行；bug2 探针结果（或限制说明）已记录。
- [ ] 计划副本含验收记录章节；无 commit。

## 7. STOP conditions

- 漂移检查内容 mismatch（摘录对不上活代码）→ 停，重新定位。
- 定点测试修复后连续两轮仍红 → 停，重评设计而非继续补丁。
- 真机探针需要改 out-of-scope 文件（如工具描述、引擎注册）才能通过 → 停，问 owner。
- bug2 探针一轮文案迭代后仍漏 → 停，报告证据与限制，不无限迭代。
- 见 `no such module: fts5` → 先查构建 tag（工具链），不当产品 bug 修。
- 发现需要动 wire 帧/版本存储语义 → 停（那是集成方契约，超出本期）。

## 8. Maintenance notes

- 交付组键自此是**文件路径**（web relPath 优先、回退显示标签）；未来若要在组里
  展示版本历史，不得恢复 ID 键（ID=版本行身份）。reviewer 重点：原位更新语义
  （最新内容、首次位置）两面一致；web 键在 root 未知时仍合并同 relPath 行。
- wire 终态帧与 fb_artifacts 仍带全部版本（集成方/版本列表契约），surface 收敛；
  两层职责不要合并。content 路由对任何 id 都读磁盘当前内容
  （pkg/gateway/session_files.go:373-380 os.Open(a.Path)），保留最新 id 的价值
  在 size/sha 元数据一致，不在下载内容。
- 提示词常量改动使 cached prompt prefix 一次性失效（预期，office_prompts.go
  注释所述的 byte-stable 指运行期不变）。
- bug2 是契约文本修复，合规仍属模型判断（owner prompt-driven 裁决）；
  后续真实轮若再漏，先取 reasoning trace 对照本契约再定性。
- 既有陈旧注释（commands.go:791 "turn-end top-up"）不在本期扩散，已在本计划
  §4 记录；后续触及该文件时顺手改对。

## Implementation Status

**VERDICT: APPROVE**（tech lead 独立复跑全部判据后给出；合并由 owner 决定）。

- **Worktree / 分支**：`.worktrees/fix-deliverables-group`，分支 `fix-deliverables-group`，
  基线 dev @ `921e9a1d`，执行器 commit `e47d605d`
  （"fix(deliverables): one group row per file, and a subagent's worktree counts"）。
- **执行器**：deepseek-flash @ max（owner 从 API 拒名的 deepseek-v4.1-flash 行改派；
  首次派发在 API 层 400 失败、零改动，经 subagent_continue 换模型重跑）。
- **计划任务状态**：Step 0–7 全部 done（Step 7 的计划回填由 reviewer 完成，即本节）。

### Reviewer 亲自复跑的验证（均在 executor worktree 内）

| 判据 | 结果 |
|---|---|
| Scope：`git diff --stat 921e9a1d..HEAD` | 恰好 8 个 in-scope 文件；`status --porcelain` 空 |
| `grep 'row.ID != ""' pkg/tui/reducer.go` | 0 命中（ID 键已除） |
| 全 diff 审读 | TUI 路径键+原位更新、web relPath 优先键、提示词第二段逐字、artifacts.go/ChatView.vue 仅注释——均与计划一致 |
| `go test -tags fts5 ./pkg/tui ./pkg/agent -count=1` | ok（tui 197.2s / agent 0.47s） |
| `gofmt -l cmd pkg third_party plugins` | 零输出 |
| `go vet -tags fts5 ./...` | exit 0 |
| `pnpm vitest run src/lib/artifacts.test.ts` | 33/33 pass |
| `pnpm typecheck` | exit 0 |
| `go test -tags fts5 ./pkg/... ./cmd/forebrain -count=1` | 36 包 ok，无 FAIL |
| `pnpm test` | 194 文件 / 1557 测试 pass |
| tmux bug1 **活体**（reviewer 独立隔离实例 fb-review:8765） | 脚本 [shell 建 notes/other, present(notes), shell 改写 notes, present(notes), present(other)] → `fb_artifacts` 3 行（notes 11B `3ca9ab241c6f`、notes 18B `1d2982aeeefc`、other 10B `c67a90dff339`，全 origin=model）= 重复前置真实存在；活体组 `¶ notes.md`、`¶ other.md` **各一行**；Worked for 1s |
| tmux bug1 **replay**（resume `cli-5f41b9eb-d66e-4531-af2d-bbc7a1058e20`） | 组 `¶ notes.md`、`¶ other.md` 各一行 |

### 新测试审计（断言有意义）

`TestDeliverablesGroupOneRowPerFile`（最新内容占首位、republished 一行、legacy 无 ID
按路径合并取最新、同名两目录两行）、`TestReplayFrameDeliverablesOneRowPerFile`
（终态事件两版本→一行最新）、web 5 例（含截图真实尺寸 869854/871486、跨源最新胜出、
**root=undefined 同 ID/同 relPath 合并** B1 回归）。执行器做过红证：还原旧 reducer
两新测试 FAIL 打印 3 行，还原修复后绿。

### 记录在案的偏差（均经 merit 判断接受）

1. **Step 6 DB 备份替换**：计划要求 `sqlite3 .backup` 真实状态库；执行器实测库 7.1GB、
   磁盘仅 5.7GiB 空闲，改用全新 scratch 库＋按显式列复制唯一真实模型行
   （meituan/LongCat-2.5，即失败轮模型）＋真实 auth.json。探针仍对真实 provider 真跑：
   `fb_artifacts` 恰一行 `.worktrees/probe-wt/hello.txt`（21B origin=model），组画
   `≡ .worktrees/probe-wt/hello.txt`（拼写在 worktree 目录下），reasoning 引用了所有权句。
   服务计划意图、在 scope 内、已说明 → 接受。**该探针为执行器所跑，reviewer 未独立重跑**
   （best-effort 判据）；reviewer 独立核实了提示词 diff 逐字与金标测试 3 PASS（中性测试绿）。
2. **docs/plan 被 gitignore**（`.gitignore:76`）：计划副本在 worktree 磁盘存在但**不在**
   commit `e47d605d` 内，不随分支旅行。reviewer 已在主树 `docs/plan/DELIVERABLES_GROUP_FIX_PLAN.md`
   落耐久副本（含本 Implementation Status）。
3. **pkg/architecture web 资产守卫**：fresh worktree 无 `pkg/gateway/dist` 时
   `TestWebBuildLoadsNoExternalAsset` 红；执行器 `pnpm build` 后 ok，未改任何源码/测试 →
   环境 provisioning，非代码缺陷。
4. **Step 5 回归/Esc 面**（单呈现轮组一行、Esc 撤回干净 EXIT=0）为执行器所跑并报告，
   reviewer 未独立重跑该两面；活体+replay 核心判据 reviewer 已独立复跑。

### 合并记录（owner 裁决「合并回主树dev分支」后执行）

- rebase：`fix-deliverables-group` 由 `e47d605d`（基于 `921e9a1d`）rebase 到前进的
  dev `506ee51f`，零冲突 → 后继 commit **`4003bcb7`**。
- rebase 后全门禁复跑（worktree 内）：`gofmt -l` 零、`go vet -tags fts5` 0、
  `go test -tags fts5 ./pkg/... ./cmd/forebrain` 36 包 ok 无 FAIL、`pnpm test` 1557 pass、
  `pnpm typecheck` 0。
- ff-only：主树 dev `506ee51f` → **`4003bcb7`**，`git diff --stat 506ee51f..dev` 恰 8 文件
  +270/−76，主树工作区干净。未 push。
- worktree/分支：owner 清理令下由 ce 按已核实配方移除（`git worktree remove` +
  `git branch -d`；依据：分支与 dev 同尖 `4003bcb7`、worktree 无已跟踪改动、主树计划副本
  严格覆盖 worktree 版）。dev 尖保持 `4003bcb7`、未 push；ignored 的 node_modules/third_party/build
  可再生，一并丢弃。reviewer 核实无未落盘产物。

### 未做 / 遗留

- 合并与清理均已完成。dev 尖 `4003bcb7`，主树工作区干净，`fix-deliverables-group`
  分支/worktree 已移除。`.worktrees/plan-review-684354609`（detached `aace268f`，含 2 个
  未跟踪测试文件 orchestration_message_review_test.go / edit_match_review_test.go）非本批产物，
  ce 未动、待 owner 单独发话。
- 既有陈旧注释 `pkg/tui/commands.go:791` "turn-end top-up" 按 §4 不在本期扩散，留待后续触及该文件时改对。
- bug2 属契约文本修复，合规仍属模型判断（owner prompt-driven 裁决）；后续真实轮若再漏，
  先取 reasoning trace 对照本契约再定性。