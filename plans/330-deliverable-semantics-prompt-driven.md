# Plan 330: 交付物回归 prompt-driven——删除变更集补漏，batch 常扫 outputs/，提示词承载完整契约

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: baseline commit `1ff34cf8` (dev). Run both:
> ```bash
> git status --porcelain -- pkg/turn/deliverables.go pkg/turn/deliverables_test.go pkg/turn/submit.go pkg/turn/batch.go pkg/turn/batch_test.go pkg/state/rewind.go pkg/state/rewind_test.go pkg/state/artifact_store.go pkg/state/artifact_store_test.go pkg/agent/office_prompts.go pkg/agent/office_prompts_test.go pkg/agent/coding_delivery.go pkg/agent/coding_delivery_test.go pkg/agent/testdata/project_type_prompts.golden.json pkg/tool/artifacts.go docs/integration/batch-tasks.md pkg/architecture/testdata/sqlite_sql_snapshot.json
> git diff 1ff34cf8 -- pkg/turn/deliverables.go pkg/turn/submit.go pkg/turn/batch.go pkg/state/rewind.go pkg/state/artifact_store.go pkg/agent/office_prompts.go pkg/agent/coding_delivery.go pkg/tool/artifacts.go docs/integration/batch-tasks.md
> ```
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> **content** mismatch, treat it as a STOP condition (moved line numbers
> alone are not drift). Note: the main tree may carry another agent's
> uncommitted WIP in `pkg/tui/setup.go` / `pkg/tui/setup_test.go` — those are
> OUT of scope; ignore them. You work in an isolated worktree (see Git
> workflow), which is created from the committed HEAD and does not inherit
> that WIP.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（跨类型行为移除＋公开集成契约变更；全部由 owner 裁决驱动）
- **Depends on**: none（324 rev4 已落地于 `403609c6`，本计划是其兜底部分的裁决后修正）
- **Category**: bug + tech-debt（交付物暴露缺陷＋契约归位）
- **Planned at**: commit `1ff34cf8`, 2026-11-19

## Why this matters

324 rev4 给三个项目类型加了「保证级变更集补漏」：回合结束时引擎把本轮追踪到的
**所有**未交付变更补登为交付物（`origin=fallback`）。owner 2026-11-19 审计裁决
推翻了这一机制的前提：

1. office/batch 交付物面向**无技术背景用户**；中间产物、过程文件、代码脚本、
   安装的依赖都不是用户需要的（例外：有时用户需要 html/markdown——只有模型
   知道当次意图）。向用户展示不需要的交付物严重影响使用。
2. **哪些文件是交付物不得在 harness 层用启发式字符串匹配或文件类型判断实现。**
3. **batch 豁免**：它是服务端↔客户端集成项目，不直接面向终端用户，可以在
   harness 层加规则和约束。
4. 三类项目通用：**用户消息明确提到需要交付什么文件，必须严格按用户要求交付**
   （不多不少；类型默认语义仅在用户未明确指定时生效）。
5. 「根治 present 一部分漏一部分」的目标口径＝**不能漏掉用户需要的交付物**，
   不是登记全部变更。
6. **交付时机必须由 LLM 主动判断**：模型认为任务完成、收口、落地、可以交付时
   主动交付；harness 不得用机械时点替模型决定。
7. （已撤销）「batch completed 帧必须携带已生成交付物」——owner 明示撤销，
   不作为本计划需求。completed 帧携带模型已 present 的行是现状（gateway
   `writeMsg` 对 `wsRunEndOps` 三终态帧统一挂 `RunArtifactsOf`，
   server.go:1430-1432，已核实），无需改动。
8. **batch 交付时机也由 LLM 主动判断，与 office/coding 一致**（覆盖早先征询
   的「常扫」选择语境）：harness 不得替模型决定交付时机。
9. **需求有不合理之处必须纠正、提醒并给出建议**（owner 常设协作规则）。
   advisor 据此提醒：batch 无人值守（BatchTaskPrompt「nobody is watching」），
   删除全部 harness 收集会造成「completed＋空交付物」静默丢失，且
   completed/failed 执行拒绝续跑（docs/integration/batch-tasks.md:41,380），
   只能整任务重跑——不可恢复。outputs/ 扫描读的是 prompt 声明的协议位置
   （模型自己的交付决定），非裁决 2 禁止的启发式资格判断，且在回合结束后
   才跑、不替模型决定时机。**owner 最终裁决＝A：batch 保留协议位置兜底**
   （否决 B 零收集＋未交付即失败、B′ 零收集＋仅警告）。

实证缺陷（advisor 已用 `go test -overlay` 注入临时测试复现，三条全红）：
office 模型**遵守** prompt 把辅助脚本收进 `.work/`，补漏却把 `.work/helper.py`
登记成交付物（dot 目录排除只对 batch 生效）；office 中间文件 `extracted-raw.csv`
同样被登记；batch 工作区根目录的 `helper.py` 也被补登进集成结果。且 office/batch
项目通常**非 git**，脚本经 shell 生成的成品根本不进变更集追踪——补漏连真正的
漏项都补不到，补到的全是过程文件。

本计划落地 owner 最终裁定的目标形态（征询记录：coding 补漏＝移除；batch
兜底＝A 协议位置常扫——裁决 8 后 advisor 提醒无人值守静默丢失风险，owner
复裁保留）：

- **删除变更集补漏**（`collectTurnDeliverables` 及其全部支撑代码）——三类型。
- **batch**：outputs/ 扫描从「模型零交付才触发」改为**每次执行结束都扫**
  （sha 去重、≤200、dot 条目跳过、regular only 不变），单一协议位置机制同时
  覆盖零交付与部分交付漏项（含 shell 生成的成品）。
- **office/coding**：交付完全 prompt-driven——present_files 是唯一交付动作，
  时机与集合由模型判断；提示词补齐「用户明确指定优先」「交付物全集清点」
  「任务完成收口后主动交付」条款。
- 契约文档与过时注释同改。

**最终契约（owner 已裁，不再是开放问题）**：三类型的交付时机与集合都由模型
判断（present_files 是唯一 proactive 交付动作）；office/coding 零 harness
收集；batch 保留唯一的协议位置兜底——执行结束（模型已收口）后扫 outputs/
补登未 present 的文件。该兜底读的是 prompt 声明的协议位置（模型自己的交付
决定），不是裁决 2 禁止的启发式资格判断，也不替模型决定时机；它是裁决 3
豁免＋裁决 8 后 owner 复裁（A）的产物，防的是无人值守独有的静默丢失。
**不得**把兜底扩大到 outputs/ 之外，**不得**恢复变更集补漏，**不得**给
office/coding 加任何 harness 收集。

## Current state

基线 `dev@1ff34cf8`。以下摘录全部来自 advisor 亲读（行号为基线行号，漂移时
以内容定位）。

### 机制 1：变更集补漏（删除对象）

- `pkg/turn/deliverables.go`（整文件 306 行，删除）——头注释自述：
  ```go
  // The turn-end deliverable top-up (plan 324): the engine's guarantee that
  // every tracked change a turn made in its project is registered as that
  // turn's deliverable, whatever the model did or forgot to present.
  ```
  关键排除逻辑（:103-116）——dot 目录排除**仅 batch**，office 的 `.work/` 漏网：
  ```go
  func deliverablePathExcluded(rel string, batch bool) bool {
      if rel == "" { return true }
      for _, part := range strings.Split(rel, string(filepath.Separator)) {
          if part == ".git" { return true }
          if batch && strings.HasPrefix(part, ".") { return true }
      }
      return false
  }
  ```
  注册（:283-297）：对每个未交付路径 `AddArtifact(... Origin: state.ArtifactOriginFallback)`，
  **无任何类型/用途过滤**。文件内还有：`turnDeliverableTimeout`、
  `deliverableScopeSource`/`deliverableChangeSource`/`deliverableEventSource`/
  `deliverableArtifactStore` 四个接口、`deliverableScope`、`canonicalDeliverablePath`、
  `shellChangedPaths`、`collectTurnDeliverables`——全部随文件删除。
- `pkg/turn/submit.go` 四个调用点（删除 if 块及其专属注释）：
  - :255-263（approval 分支，仅 batchTurn）：
    ```go
    // The top-up runs only for a batch turn here: an interactive turn is
    // NOT over — its approval resume presents in the same turn — while a
    // batch approval_required is terminal, so its writes need collecting
    // now.
    if batchTurn {
        if pt, root := s.deliverableScope(context.WithoutCancel(ctx), req.SessionID); root != "" {
            s.collectTurnDeliverables(context.WithoutCancel(ctx), req.SessionID, runID, pt, root)
        }
    }
    ```
    其上方 :251-254 的 batch-approval-failed 策略注释**保留**（「A batch turn
    must never park on an approval… failed with the reason…」）。
  - :276-281（cancelled 分支）：
    ```go
    // The top-up runs before the execution is settled and before the
    // terminal event: whatever the surfaces read next — the batch end
    // report, the run-end frame — must already see every row.
    if pt, root := s.deliverableScope(context.WithoutCancel(ctx), req.SessionID); root != "" {
        s.collectTurnDeliverables(context.WithoutCancel(ctx), req.SessionID, runID, pt, root)
    }
    ```
  - :294-296（failed 分支）与 :315-317（completed 分支）：同款裸 if 块。
  - `batchTurn` 变量在 :120/:130/:151/:200 另有用途，**保留**。
- `pkg/turn/deliverables_test.go`（329 行，重写）：`newDeliverableEnv`/`write`/
  `recordChange`/`runArtifacts`/`rowsOf` helper 保留复用；`shellStepWithChangedPaths`
  与全部 `TestTurnTopUp*` 删除。钉住测试 `TestTurnTopUpBatchScratchExcluded`
  （:285）表内只有 `{batch,0}`/`{coding,2}`，注释声称「coding and office
  deliver any file of the project」——office 行为未被表钉住但代码路径与 coding
  相同（复现已证实）。

### 机制 2：batch outputs/ 扫描（保留，触发器改常扫）

- `pkg/turn/batch.go:334-350`（endBatchExecution 内，触发器替换对象）：
  ```go
  var artifacts []state.Artifact
  if s.batchArtifacts != nil {
      // The scan's trigger is "the model presented nothing" — the
      // model-origin count is zero — not "the conversation holds no rows":
      // a continue's earlier fallback rows must not suppress the scan of a
      // turn whose model delivered nothing again (plan 324, deliberate
      // change from the total==0 trigger).
      modelCount, err := s.batchArtifacts.CountArtifactsByOrigin(ctx, sid, state.ArtifactOriginModel)
      if err != nil {
          slog.ErrorContext(ctx, "batch artifacts read", "session", sid, "err", err)
      } else if modelCount == 0 {
          // Nothing was presented, so the outputs folder is the deliverable:
          // a task that produced files must not look like it produced none.
          s.collectBatchFallbackArtifacts(ctx, sid, runID, exec.Workspace)
      }
      artifacts, _, _ = s.batchArtifacts.ListArtifactsPage(ctx, sid, batchArtifactListLimit, 0)
  }
  ```
- `BatchArtifactStore` 接口（batch.go:61-71）：`CountArtifactsByOrigin` 条目
  （:64-67 含注释）删除；`ListArtifactsPage`/`AddArtifact`/`LatestArtifactByPath`
  保留（扫描仍在用）。
- `collectBatchFallbackArtifacts`（batch.go:399-474）**函数体不改**：仅
  `<workspace>/outputs/`、dot 条目跳过、regular only、`batchFallbackFileLimit=200`、
  session 级 per-path sha 去重、不跟随软链。`hashArtifactFile`（:478）不改。
- exec 读回失败路径（:308-323）无 workspace→不扫描，现状保持。
- batch workspace＝`<项目根>/tasks/<任务ID>/<序号>`（`state.TaskWorkspaceDir`，
  task_store.go:117），纯目录**非 git**；创建时建好 inputs/outputs/.work。

### 机制 3：present_files（不动）

- `pkg/tool/artifacts.go:205-320` handler：仅主代理、项目根内、regular 文件、
  **无类型过滤**——交付资格完全由模型决定，符合裁决。description 冻结于
  `pkg/tool/testdata/present_files_description.golden`，**不改**。
  `ClassifyArtifact`（:140）只是展示元数据（kind/mime/preview），不参与资格，
  **不改**。头注释（:1-8）过时，改（见 Step 7）。
- `origin=plan`（session_tools.go:561，exit_plan_mode 保留计划文件）不在问题域，
  **不改**。

### 提示词半边（修改对象）

- `pkg/agent/office_prompts.go`：
  - `OfficeWorkPrompt` :18 已有「keep helper scripts and intermediate data in
    a .work/ subfolder … do not show code to the user unless they ask for it」；
    :34 已有「.md or .html only when asked or when the result is meant to be
    read in a browser」——**保留不动**。
  - :48「# Hand over」段（整段替换，见 Step 6 逐字定稿）：
    ```
    When the files are final, call present_files once with every deliverable so the user gets a download and preview card for each; do not present helper scripts or scratch files. If the requested result is an answer rather than a file, answer directly and do not create a file merely to call present_files. Then reply briefly: what you produced, the key findings or decisions inside it, and anything the user still has to check or decide. Do not paste the whole document into the chat.
    ```
  - `BatchTaskPrompt` :85「# Outputs」bullet（整条替换）：
    ```
    - When the outputs are final and the task produced files, call present_files once with every deliverable. That list is the structured result the calling system receives. If the task requires only a response, do not create a file merely to call present_files.
    ```
  - :78 workspace bullet（outputs/ 协议、.work/ 收纳、never modify inputs/）
    **保留不动**——它是常扫的协议锚点。
  - 头注释 :6-7 过时（「their system prompt stays byte-for-byte the one they
    have always had」——coding 已有 CodingDeliveryPrompt），改。
- `pkg/agent/coding_delivery.go`：`CodingDeliveryPrompt`（:11-19）首段以
  「In this project the work itself is the deliverable: every file this turn
  created or modified…」开头；第二段含「Then call present_files once with
  every created or modified file, after the files are final.」。头注释 :8-10
  声称 top-up 保证完整性——过时，改。
- **提示词测试红线**（改措辞必须同时满足）：
  - `pkg/agent/office_prompts_test.go` `TestOfficePromptsSayNothingAboutCode`：
    office/batch 全部提示词 lowercase 不得含 `code review`/`diff`/`git`/
    `repository`/`codebase`/`refactor`（注意「digit」也含 git，禁用）。
  - `TestBatchPromptNeverAsksTheUser`：BatchTaskPrompt 必须保留
    「You cannot ask anyone anything.」逐字句。
  - `TestOfficePromptsNameTheDeliveryTool`：两提示词必须含 `present_files`。
  - `TestProjectTypePromptsGolden`：`pkg/agent/testdata/project_type_prompts.golden.json`
    与运行时常量 **reflect.DeepEqual 逐字节**（map[string]any，9 个 key）；
    测试永不重写 golden，改动必须同 diff 显式更新。
  - `pkg/agent/coding_delivery_test.go` `TestCodingDeliveryPromptCoversTheCompletenessRule`
    钉住五个短语：`every file this turn created or modified`、
    `No kind of file is excluded`、`A turn that changed no files answers directly`、
    `another agent's concurrent changes`、`A deleted file cannot be presented`；
    `TestCodingDeliveryPromptIsProviderNeutralAndNamesNoOtherTool` 禁词含
    `git`/`write_file`/`edit_file`/`read_file`/`apply_patch`/`subagent_run`/
    URL/厂商名等。
  - `pkg/run/runner_test.go:2799-2834` 用常量拼装期望提示词（自动跟随常量
    变化），并断言 office/batch 不含 CodingDeliveryPrompt——**无需改**。
  - `pkg/turn/batch_test.go:122` `TestBatchPromptsGolden` 钉的是
    BatchTaskContext/BatchContinuePrompt（testdata/batch_prompts.golden.json），
    与本计划提示词改动**无关**，不动。

### state 死代码（删除对象，调用方已 grep 核实）

- `pkg/state/rewind.go`：`runTreeCTE`/`runTreeCTEPostgres`（:225-244）、
  `FileChangesForRunTree`（:246-257）、`RunTreeIDs`（:259-289）、`runTreeIDsSQL`
  （:291-292）——生产调用方仅 deliverables.go。`queryFileChanges`（:294+）、
  `changedFileColumns`/`changedFileFromSQLite`（rewind 主路径共用）**保留**。
  测试：`rewind_test.go` `TestFileChangesForRunTree`（:174）、`TestRunTreeIDs`
  （:213）删除。
- `pkg/state/artifact_store.go`：`artifactCountByOriginSQL`（:151 起的 var 块）、
  `CountArtifactsByOrigin`（:320-338）、`SessionProjectScope`（:340-375）——
  生产调用方仅 batch.go 触发器与 deliverables.go。`projectSessionTypeSQL`
  （project_store.go:524，:1104 在用）与 `fileProjectRootSQL`（file_service.go:105，
  :518 在用）是**共享 SQL，保留**。`LatestArtifactByPath`（扫描在用）保留。
  测试：`artifact_store_test.go` `TestLatestArtifactByPathCountByOrigin`（:103）
  尾部 :137-150 的 count 断言删除、函数改名 `TestLatestArtifactByPath`；
  `TestSessionProjectScope`（:158-199）整删。
- SQL 快照守卫：`pkg/architecture/sqlite_sql_test.go` `TestSQLiteStatementsUnchanged`
  对快照**增删改都报错**；删除语句后必须再生：
  `go run ./scripts/sqlite-sql-snapshot`（scripts/sqlite-sql-snapshot/main.go）。
- fan-out 预算：`pkg/architecture/cache_test.go:219` `"turn": {current: 12, target: 12}`。
  deliverables.go 的 import（event/state/tool）在 batch.go/submit.go 仍有使用，
  预计 turn fan-out 不变；若 architecture 测试报实际值下降，按 ratchet 规则
  下调常量（这是预算表唯一允许的改动方向）。

### 契约文档（修改对象）

- `pkg/tool/artifacts.go:1-8` 头注释：
  ```go
  // A deliverable exists only when the model says so: present_files registers
  // each presented file in fb_artifacts, and every surface that shows what a
  // conversation delivered — the web card, the run's end frame, a batch task's
  // integrator — reads that table. Intermediate scripts and scratch files are
  // never artifacts because the model never presents them.
  ```
- `docs/integration/batch-tasks.md:21`（生成物定义行）与 :350-354（兜底说明段）
  向集成方发布了变更集补漏——逐字替换见 Step 7。

### 展示管线（不动）

`RunArtifactsOf`（pkg/turn/events.go:134）透传全部行（含 Origin）；TUI/web/
replay 不按 origin 过滤（grep 证实无消费点）——fallback 行与 model 行同等展示。
324 的 TUI 平价 machinery（Deliverable.ID、RunEndedMsg.RunArtifacts、reducer
轮末并集、replay renderReplayDeliverables）**全部保留**：它们展示存在的行，
本计划只是让不该存在的行不再产生。

### 复现证据（advisor 实跑，基线全红）

注入测试（overlay，仓库树零改动）断言「office `.work/helper.py`、office
`extracted-raw.csv`、batch 根目录 helper.py 不得被登记为交付物」：
```
go test -tags fts5 -overlay /tmp/office-scratch-repro/overlay.json ./pkg/turn -run TestRepro -v
--- FAIL: TestReproOfficeWorkScratchNotDelivered  (artifact RelPath:.work/helper.py Origin:fallback)
--- FAIL: TestReproOfficeIntermediateNotDelivered (artifact RelPath:extracted-raw.csv Origin:fallback)
--- FAIL: TestReproBatchRootScratchNotDelivered   (artifact RelPath:tasks/t1/1/helper.py Origin:fallback)
```
既有基线绿：`go test -tags fts5 ./pkg/turn -run 'TestTurnTopUp|TestBatchFallback|TestBatchScan|TestBatchPartial' -count=1` → ok。
Step 1 把该复现升级为永久回归测试（先红后绿）。

## Commands you will need

环境（每个 shell 会话先执行；house rule：测试一律 scratch home，禁污染真实
`~/.forebrain`；pkg/pdf cgo 需要 PKG_CONFIG_PATH，Makefile 只在 make 内设置）：

```bash
export CGO_ENABLED=1
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
export FOREBRAIN_HOME="$(mktemp -d)"
```

| Purpose | Command | Expected on success |
|---|---|---|
| 聚焦测试（turn） | `go test -tags fts5 ./pkg/turn -run '<pattern>' -count=1 -v` | 指定用例 PASS/FAIL 如步骤所述 |
| 包级测试 | `go test -tags fts5 ./pkg/turn ./pkg/state ./pkg/agent ./pkg/tool ./pkg/architecture -count=1` | 全部 ok |
| SQL 快照再生 | `go run ./scripts/sqlite-sql-snapshot` | exit 0，快照文件更新 |
| 格式 | `gofmt -l cmd pkg third_party plugins` | 无输出 |
| vet | `go vet -tags fts5 ./...` | exit 0 |
| 全量套件 | `go test -tags fts5 ./... -count=1` | 全部 ok（耗时长，tui 包约 3 分钟；用 ≥900s 超时） |
| 方言矩阵（pkg/state 改动必跑） | `scripts/test-db.sh up && scripts/test-db.sh all` | sqlite/mysql/postgres 全绿 |
| 矩阵收尾 | `scripts/test-db.sh down` | 容器清理 |

`-run` 多用例用裸 `|`（不要 `\|`；markdown 表格里不要放含 `|` 的命令——
上面聚焦测试命令已移出表格）。核对 `-v` 输出里确有 `--- PASS`/`--- FAIL`
对应用例，防「no tests to run」假绿。

## Scope

**In scope**（唯一允许修改的文件）:
- `pkg/turn/deliverables.go`（删除）
- `pkg/turn/deliverables_test.go`（重写为回归套件）
- `pkg/turn/submit.go`（删 4 个 top-up if 块及专属注释）
- `pkg/turn/batch.go`（常扫触发器＋接口条目＋注释）
- `pkg/turn/batch_test.go`（2 个测试重写＋1 个改名＋注释）
- `pkg/state/rewind.go`、`pkg/state/rewind_test.go`（死代码删除）
- `pkg/state/artifact_store.go`、`pkg/state/artifact_store_test.go`（死代码删除）
- `pkg/agent/office_prompts.go`、`pkg/agent/coding_delivery.go`（提示词＋头注释）
- `pkg/agent/coding_delivery_test.go`（新短语钉住）
- `pkg/agent/testdata/project_type_prompts.golden.json`（两 key 再生）
- `pkg/tool/artifacts.go`（**仅头注释**）
- `docs/integration/batch-tasks.md`（两处契约文案）
- `pkg/architecture/testdata/sqlite_sql_snapshot.json`（再生）
- `pkg/architecture/cache_test.go`（**仅当** fan-out 实际值下降时下调 turn 常量）
- `plans/README.md`（状态行；评审方维护时除外）

**Out of scope**（看似相关，严禁触碰）:
- `pkg/tui/**`、`frontend/**`、`pkg/gateway/**` 的展示代码——另一代理在主树
  有 setup.go WIP；展示管线无需改动（行少了，管线不变）。
- present_files handler、冻结 description 及其 golden、`ClassifyArtifact`、
  `origin=plan` 路径（session_tools.go）。
- `collectBatchFallbackArtifacts` 函数体、`hashArtifactFile`、
  `LatestArtifactByPath`、`ListRunEventsOfTypes`（replay 在用）、
  `shell_diff.go`、`BatchContinuePrompt`/`BatchTaskContext` 及其 golden。
- `projectSessionTypeSQL`/`fileProjectRootSQL`（共享 SQL）、`queryFileChanges`、
  rewind 主路径。
- `scripts/test-db.sh`、Makefile、任何配置/appcfg（无配置改动，不触发
  config_defaults 注册门禁）。
- 324 计划文件本身（历史留痕，不回写）。

## Git workflow

项目惯例（owner 常设规则）：隔离 worktree＋exec 分支，主树 dev 零 commit；
合并由 owner 裁决（或批次预授权），ff-only，合并后 worktree 与分支必须删除。

```bash
git worktree add .worktrees/330-deliverable-semantics -b 330-deliverable-semantics dev
cd .worktrees/330-deliverable-semantics
git rev-parse --short HEAD   # 记录起始 SHA（Step 8 范围审计用；dev 可能已前进于计划基线）
```

- 主 worktree 路径以 `git worktree list` 第一行为准；worktree 必须建在主树的
  `.worktrees/` 下（git 已忽略该目录，禁加 .gitignore）。
- 每步或每逻辑单元一 commit；message 风格对照 git log（conventional commits）：
  例 `refactor(turn): deliverables are the model's call (plan 330)`、
  `feat(turn): batch scans outputs/ at every execution end (plan 330)`、
  `docs(agent): delivery prompts carry the explicit-request contract (plan 330)`。
- 不 push、不开 PR、不动主树工作区、不碰他代理未提交改动。
-  fresh worktree 若缺 native 依赖：`scripts/fetch-libtokenizers.sh`、
  `scripts/fetch-pdfium.sh`、`scripts/fetch-pdfium-fonts.sh`（make build 同款）。

## Steps

### Step 1: 回归测试先写先红（复现升级为永久钉住）

重写 `pkg/turn/deliverables_test.go`：保留 `newDeliverableEnv`/`write`/
`recordChange`/`runArtifacts`/`rowsOf`（逐字保留现实现），删除
`shellStepWithChangedPaths` 与全部 `TestTurnTopUp*`，新增 below 两个测试。
结构范例：`TestBatchApprovalRequiredRunsTheTopUp`（batch_test.go:736，runFunc
内 CreateRun＋started＋RecordFileChange 的写法）；`runFunc` 定义在
submit_test.go:67。

```go
// TestSubmitInteractiveTurnRegistersNoFallbackArtifacts pins the owner's
// 2026-11-19 contract: office and coding deliver only what the model
// presents, when the model judges the work complete. The engine registers
// nothing on its own at any terminal branch — a helper script the model
// kept out of the delivery set stays out, even though the ledger tracked
// the write.
func TestSubmitInteractiveTurnRegistersNoFallbackArtifacts(t *testing.T) {
	for _, pt := range []string{state.ProjectTypeOffice, state.ProjectTypeCoding} {
		for _, outcome := range []struct {
			name string
			err  error
		}{
			{"completed", nil},
			{"cancelled", context.Canceled},
			{"failed", errors.New("boom")},
		} {
			t.Run(pt+"/"+outcome.name, func(t *testing.T) {
				env := newDeliverableEnv(t, pt)
				rel := "src/main.go"
				if pt == state.ProjectTypeOffice {
					rel = ".work/helper.py"
				}
				path := env.write(t, rel, "package main\n")
				svc := New(
					WithSessionStore(env.sessions),
					WithRunEventStore(env.runs),
					WithBatchTasks(nil, env.sessions, nil),
					WithRunExecutor(runFunc(func(runCtx context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
						run, err := env.runs.CreateRun(runCtx, env.session, "work")
						if err != nil {
							return nil, err
						}
						started(run.ID)
						if err := env.sessions.RecordFileChange(runCtx, state.FileChange{
							SessionID: env.session, RunID: run.ID, Path: path,
						}); err != nil {
							return nil, err
						}
						return &agent.Result{}, outcome.err
					})),
				)
				out, err := svc.Submit(context.Background(), TurnRequest{SessionID: env.session}, nil)
				if err != nil && outcome.err == nil {
					t.Fatalf("submit: %v", err)
				}
				if rows := env.runArtifacts(t, out.RunID); len(rows) != 0 {
					t.Fatalf("%s %s turn registered engine-side artifacts: %+v", pt, outcome.name, rows)
				}
			})
		}
	}
}
```

注意：断言读 `out.RunID`（Submit 返回的、executor 经 started 创建的 run），
**不是** fixture 预建的 `env.runID`——top-up/交付行都挂在 executor 的 run 下。

```go
// TestSubmitKeepsModelPresentedArtifacts is the positive control: removing
// the engine-side collection must not touch what the model itself delivered.
func TestSubmitKeepsModelPresentedArtifacts(t *testing.T) {
	env := newDeliverableEnv(t, state.ProjectTypeOffice)
	deliverable := env.write(t, "2026Q3-report.docx", "final")
	scratch := env.write(t, ".work/helper.py", "print(1)\n")
	svc := New(
		WithSessionStore(env.sessions),
		WithRunEventStore(env.runs),
		WithBatchTasks(nil, env.sessions, nil),
		WithRunExecutor(runFunc(func(runCtx context.Context, _ TurnRequest, started func(string)) (*agent.Result, error) {
			run, err := env.runs.CreateRun(runCtx, env.session, "work")
			if err != nil {
				return nil, err
			}
			started(run.ID)
			for _, p := range []string{deliverable, scratch} {
				if err := env.sessions.RecordFileChange(runCtx, state.FileChange{
					SessionID: env.session, RunID: run.ID, Path: p,
				}); err != nil {
					return nil, err
				}
			}
			if _, err := env.sessions.AddArtifact(runCtx, state.Artifact{
				SessionID: env.session, RunID: run.ID, Path: deliverable,
				RelPath: "2026Q3-report.docx", Name: "2026Q3-report.docx",
				Kind: "document", SHA256: rowsOf(t, deliverable), Origin: state.ArtifactOriginModel,
			}); err != nil {
				return nil, err
			}
			return &agent.Result{}, nil
		})),
	)
	out, err := svc.Submit(context.Background(), TurnRequest{SessionID: env.session}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows := env.runArtifacts(t, out.RunID)
	if len(rows) != 1 || rows[0].Origin != state.ArtifactOriginModel || rows[0].Name != "2026Q3-report.docx" {
		t.Fatalf("artifacts = %+v, want exactly the model-presented deliverable", rows)
	}
}
```

import 需要：context、errors、testing、path/filepath（helper 用）、os（helper
用）、agent、state（现文件已有）；`event`/`json` 若仅被删除的
shellStepWithChangedPaths 使用则一并移除（gofmt/编译会指出）。

**Verify**:
```bash
go test -tags fts5 ./pkg/turn -run 'TestSubmitInteractiveTurnRegistersNoFallbackArtifacts|TestSubmitKeepsModelPresentedArtifacts' -count=1 -v
```
→ **必须红**：前者 6 个子用例因 engine-side artifacts 失败（当前 top-up 会
补登），后者因 scratch 行多出失败。若全绿＝top-up 已不存在或行为已变＝STOP
（drift）。包内其余测试此时不受影响（旧 TestTurnTopUp* 已随重写删除）。

### Step 2: batch 测试改写为先红（常扫契约）

`pkg/turn/batch_test.go`：

1. `TestBatchPartialModelDeliveryScanSuppressedTopUpFillsGap`（:652）整体替换为
   （改名＋断言翻转；**presented 文件必须写在真实 workspace 的 outputs/ 下且
   model 行 SHA256 用 rowsOf 真哈希**，否则常扫会把同字节文件再登一行，测试
   钉不住去重）：
   ```go
   // TestBatchPartialModelDeliveryScanFillsOutputsGap pins the always-on
   // outputs scan (owner 2026-11-19): a turn that presented some and missed
   // some still delivers the rest of outputs/, while a tracked change
   // outside the protocol folder is never the engine's to deliver.
   func TestBatchPartialModelDeliveryScanFillsOutputsGap(t *testing.T) {
       ctx := context.Background()
       env := newTriggerEnv(t)
       presented := filepath.Join(env.exec.Workspace, "outputs", "summary.docx")
       if err := os.WriteFile(presented, []byte("the presented one"), 0o644); err != nil {
           t.Fatal(err)
       }
       missed := env.write(t, "tasks/t1/1/work/dataset.csv", "the missed one")
       if err := os.WriteFile(filepath.Join(env.exec.Workspace, "outputs", "untracked.xlsx"), []byte("x"), 0o644); err != nil {
           t.Fatal(err)
       }
       var ended []BatchExecutionEnd
       svc := env.service(func(_ context.Context, end BatchExecutionEnd) { ended = append(ended, end) })
       run, err := env.runs.CreateRun(ctx, env.exec.SessionID, "count rows")
       if err != nil {
           t.Fatal(err)
       }
       if _, err := env.sessions.AddArtifact(ctx, state.Artifact{
           SessionID: env.exec.SessionID, RunID: run.ID, Path: presented, RelPath: "outputs/summary.docx",
           Name: "summary.docx", Kind: "document", SHA256: rowsOf(t, presented), Origin: state.ArtifactOriginModel,
       }); err != nil {
           t.Fatal(err)
       }
       if err := env.sessions.RecordFileChange(ctx, state.FileChange{SessionID: env.exec.SessionID, RunID: run.ID, Path: missed}); err != nil {
           t.Fatal(err)
       }
       svc.endBatchExecution(ctx, TurnRequest{SessionID: env.exec.SessionID}, run.ID, state.TaskStatusCompleted, "", nil)
       files := env.sessionArtifacts(t)
       if row, ok := files["summary.docx"]; !ok || row.Origin != state.ArtifactOriginModel {
           t.Fatalf("the presented deliverable must stay the model's single row: %v", files)
       }
       if row, ok := files["untracked.xlsx"]; !ok || row.Origin != state.ArtifactOriginFallback {
           t.Fatalf("the missed output was not collected by the always-on scan: %v", files)
       }
       if _, ok := files["dataset.csv"]; ok {
           t.Fatalf("a tracked change outside outputs/ must not be delivered: %v", files)
       }
       if len(ended) != 1 || len(ended[0].Artifacts) != 2 {
           t.Fatalf("end report = %+v, want model row + fallback row", ended[0].Artifacts)
       }
   }
   ```
   当前代码下红因：modelCount>0 → 扫描被抑制 → untracked.xlsx 缺失。（旧测试
   对 collectTurnDeliverables 的直接调用随替换消失——Step 4 删函数时本文件已
   无引用，编译不断。）
2. `TestBatchApprovalRequiredRunsTheTopUp`（:736）改名
   `TestBatchApprovalRequiredStillScansOutputs`，断言翻转：`partial.csv`
   （work/ 下被追踪写入）**不得**出现，`report.xlsx` 必须出现，exec 仍
   failed/approval_required。其余 setup（runFunc 返回 WaitingError、Submit
   带 BatchInput）逐字保留。当前代码下红因：partial.csv 被 top-up 补登。
3. `TestBatchFallbackArtifactsOnlyWhenNothingWasDelivered`（:515）改名
   `TestBatchScanRegistersOutputsFiles`（函数体不动——它直接调用扫描函数，
   行为不变；旧名描述的触发器语义已废）。
4. `TestBatchScanFiresOnZeroModelDelivery`（:619）与
   `TestBatchContinueSecondTurnScansAndDedups`（:696）**断言不动**（常扫下仍
   成立），仅把前者注释里「pins the deliberate trigger change (plan 324)」
   更新为「the scan runs at every execution end; pre-existing fallback rows
   never suppress it and the end report carries every row」。
5. `triggerEnv` 注释（:536-537）「so the turn-end top-up resolves its scope
   the way a real one does」→「so the execution-end epilogue reads the same
   stores a real one does」。

**Verify**:
```bash
go test -tags fts5 ./pkg/turn -run 'TestBatchPartialModelDeliveryScanFillsOutputsGap|TestBatchApprovalRequiredStillScansOutputs' -count=1 -v
```
→ 两条**必须红**，红因如上。其余 batch 测试仍绿。

### Step 3: batch 触发器改常扫

`pkg/turn/batch.go`：
1. :334-350 替换为：
   ```go
   var artifacts []state.Artifact
   if s.batchArtifacts != nil {
       // The outputs folder is the protocol location the batch prompt names
       // (write every deliverable into outputs/). Every execution end reads
       // it — completed, failed, stopped, approval_required — so a turn that
       // presented some and missed some still delivers the rest, and a task
       // that produced files never looks like it produced none. The scan's
       // session-level sha check keeps an unchanged output from being
       // registered twice. Batch is an integration surface, not an end user:
       // this harness rule is the owner's explicit exemption (2026-11-19).
       s.collectBatchFallbackArtifacts(ctx, sid, runID, exec.Workspace)
       artifacts, _, _ = s.batchArtifacts.ListArtifactsPage(ctx, sid, batchArtifactListLimit, 0)
   }
   ```
2. `BatchArtifactStore` 接口删除 `CountArtifactsByOrigin` 条目及其注释
   （:64-67）；接口 doc（:59-60）「the batch epilogue needs to collect a
   task's deliverables」保持。
3. 本步**不动** state 实现（Step 5 删）——接口不再引用后，state 方法暂时
   无生产调用方，编译合法。

**Verify**:
```bash
go test -tags fts5 ./pkg/turn -run 'TestBatch' -count=1 -v
```
→ Step 2 两条转绿；TestBatchScan*/TestBatchContinue*/TestBatchScanRegistersOutputsFiles
全绿。`TestSubmitInteractiveTurn*` 仍红（top-up 还在，Step 4 删）。

### Step 4: 删除变更集补漏

1. 删除 `pkg/turn/deliverables.go` 整文件。
2. `pkg/turn/submit.go` 删除四处 top-up if 块及专属注释（Current state 已逐字
   列出）：approval 分支 :255-263（保留其上方 batch-approval-failed 策略注释）、
   cancelled 分支 :276-281、failed 分支 :294-296、completed 分支 :315-317。
   `batchTurn` 变量与其余逻辑不动；`context.WithoutCancel` 在 :229 等处仍在用，
   import 不变。
3. grep 确认 turn 包内无残留引用：
   ```bash
   grep -rn "collectTurnDeliverables\|deliverableScope\|turnDeliverableTimeout\|canonicalDeliverablePath\|deliverablePathExcluded\|shellChangedPaths" pkg/turn/
   ```
   → 无输出。

**Verify**:
```bash
go test -tags fts5 ./pkg/turn -count=1
```
→ 整包 ok：Step 1 回归套件转绿（6 子用例＋positive control），batch 全绿，
submit/approval/rewind 等既有测试不受影响。

### Step 5: state 死代码删除＋SQL 快照再生

1. `pkg/state/rewind.go`：删 `runTreeCTE`/`runTreeCTEPostgres` const 块
   （:225-244）、`FileChangesForRunTree`（:246-257）、`RunTreeIDs`（:259-289）、
   `runTreeIDsSQL` var（:291-292）。`queryFileChanges` 及其后全部保留。
2. `pkg/state/rewind_test.go`：删 `TestFileChangesForRunTree`（:174 起整函数）、
   `TestRunTreeIDs`（:213 起整函数）。
3. `pkg/state/artifact_store.go`：删 `artifactCountByOriginSQL` var 块（:151 起）、
   `CountArtifactsByOrigin`（:320-338）、`SessionProjectScope`（:340-375）。
   `LatestArtifactByPath` 及其前后保留。
4. `pkg/state/artifact_store_test.go`：`TestLatestArtifactByPathCountByOrigin`
   （:103）删除尾部 count 断言（:137-150，从 `model, err := s.CountArtifactsByOrigin`
   到函数收尾前），改名 `TestLatestArtifactByPath`；删 `TestSessionProjectScope`
   （:158-199 整函数）。
5. 再生快照并核对 diff 只含删除：
   ```bash
   go run ./scripts/sqlite-sql-snapshot
   git diff -- pkg/architecture/testdata/sqlite_sql_snapshot.json
   ```
   → diff 只有 `-` 行（被删语句：runTree 变更查询、runTreeIDs 查询、
   artifactCountByOrigin 查询，共 3 条唯一字面量），**无 `+` 行、无编辑行**。
   若出现新增/编辑＝STOP。注意：再生后 `grep run_tree` 快照仍会有 1 条命中
   （llm_attempts.go 的静态语句含 run_tree 片段，非删除对象）；runrt.go 的
   run_tree 内联字面量是动态拼装位点、本就不在快照。删除数以 diff 的 3 条
   `-` 为准，不以 grep 清零为准。
6. 全仓 grep 确认死代码清零：
   ```bash
   grep -rn "FileChangesForRunTree\|RunTreeIDs\|CountArtifactsByOrigin\|SessionProjectScope\|runTreeCTE\|runTreeIDsSQL\|artifactCountByOriginSQL" pkg/ --include='*.go'
   ```
   → 无输出。

**Verify**:
```bash
go test -tags fts5 ./pkg/state ./pkg/architecture -count=1
```
→ 全 ok（含 TestSQLiteStatementsUnchanged、dialect coverage、fan-out 预算）。
若 fan-out 测试报 turn 实际 import 数 < 12：把 cache_test.go:219 的
`current: 12` 下调为实际值（ratchet 只降不升），并在 commit message 注明；
预计不会发生（event/state/tool 在 batch.go 仍在用）。

### Step 6: 提示词承载契约（逐字定稿）＋golden 同 diff

**措辞红线**：office/batch 提示词 lowercase 不得含 code review/diff/git/
repository/codebase/refactor；BatchTaskPrompt 保留「You cannot ask anyone
anything.」；coding 保留五个钉住短语、禁词表（含 git）。以下定稿已逐条核对。
**禁词是子串匹配**：`different` 含 `diff`、`digit` 含 `git`，同样打红
`TestOfficePromptsSayNothingAboutCode`——office 定稿用「pick another set」
而非「a different set」即为此（评审实测抓出并修正）。改措辞后必须机械复验：
`grep -icE "code review|diff|git|repository|codebase|refactor"` 对新文本为 0。

1. `pkg/agent/office_prompts.go` `OfficeWorkPrompt`「# Hand over」段（:48 整段）
   替换为（逐字，含标点）：
   ```
   When the user's message names the files to deliver, deliver exactly those — no others and none missing — even if your own judgement would pick another set. Otherwise the deliverables are the finished work this turn produced for the user. Before finishing, account for the full set: your own final files and the files your subagents produced (they report their paths back to you); when the set is not obvious, verify it against the filesystem instead of guessing. Helper scripts, intermediate data and scratch files are never part of that set unless the user explicitly asked for them. When you judge the task complete and the files final and verified, call present_files once with every deliverable so the user gets a download and preview card for each. If the requested result is an answer rather than a file, answer directly and do not create a file merely to call present_files. Then reply briefly: what you produced, the key findings or decisions inside it, and anything the user still has to check or decide. Do not paste the whole document into the chat.
   ```
2. 同文件 `BatchTaskPrompt`「# Outputs」的 present_files bullet（:85 整条）
   替换为：
   ```
   - When the task message names the files to deliver, deliver exactly those — no others and none missing. Before finishing, account for the full set of deliverables: your own final files in outputs/ and the files your subagents produced (they report their paths back to you); when the set is not obvious, verify it against the filesystem instead of guessing. When you judge the task complete and the outputs final and verified, call present_files once with every deliverable. That list is the structured result the calling system receives. If the task requires only a response, do not create a file merely to call present_files.
   ```
3. 同文件头注释 :6-7 替换为：
   ```go
   // Creative coding conversations do not use these constants: they carry
   // their own delivery rule (CodingDeliveryPrompt) on top of the coding base.
   ```
4. `pkg/agent/coding_delivery.go`：
   - 首段（:15）句首插入显式指定条款，原句首字母改小写衔接（其余逐字不动）：
     ```
     When the user's message names the files to deliver, deliver exactly those — no others and none missing — even if this turn changed more. Otherwise, in this project the work itself is the deliverable: every file this turn created or modified inside the project directory is a deliverable of the turn — source code, scripts, configuration, data, documents, images, audio, video, archives, any type, regardless of what changed or how small the change. No kind of file is excluded: a helper script or a one-line source edit is delivered exactly like a report or an image.
     ```
   - 第二段时机句替换：
     旧：`Then call present_files once with every created or modified file, after the files are final.`
     新：`When you judge the turn's work complete and the files final, call present_files once with every created or modified file.`
   - 头注释（:3-10）替换为：
     ```go
     // CodingDeliveryPrompt is the creative-coding conversation's delivery rule
     // (owner 2026-11-18, refined 2026-11-19): every file a turn created or
     // modified is that turn's deliverable — source code included, no kind
     // excluded — unless the user's message names a different set, which wins
     // exactly. The model lists them through present_files when it judges the
     // work complete; delivery timing and set are the model's judgement, and
     // the engine registers only what the model presents. Same prompt-driven
     // delivery office and batch use, with coding's own completeness rule; the
     // shared tool description stays frozen.
     ```
5. `pkg/agent/coding_delivery_test.go`：在
   `TestCodingDeliveryPromptCoversTheCompletenessRule` 的 want 表追加一行
   `"deliver exactly those"`（显式指定条款钉住）；另加时机短语
   `"When you judge the turn's work complete"`。其余测试不动。
6. golden 再生（**临时**测试，跑完即删；永久测试永不重写 golden 的纪律不变）。
   **必须用定向字节替换，不得整 map 重 marshal**：golden 的 key 是声明序
   （OfficeWorkPrompt, BatchTaskPrompt, OfficeNotesPromptSuffix, …）而非字母序，
   `json.MarshalIndent(map)` 会重排全部 key、把真实 diff 埋进噪声（评审实测
   确认键序非字母序；两个旧值的转义串在 raw 中各唯一出现一次，定向替换可行）。
   创建 `pkg/agent/zz_golden_regen_test.go`：
   ```go
   package agent

   import (
       "bytes"
       "encoding/json"
       "os"
       "path/filepath"
       "testing"
   )

   // TestZZRegenProjectTypePromptsGolden updates ONLY the two changed keys'
   // values in place, preserving the file's key order and formatting.
   func TestZZRegenProjectTypePromptsGolden(t *testing.T) {
       path := filepath.Join("testdata", "project_type_prompts.golden.json")
       raw, err := os.ReadFile(path)
       if err != nil {
           t.Fatal(err)
       }
       var m map[string]any
       if err := json.Unmarshal(raw, &m); err != nil {
           t.Fatal(err)
       }
       want := runtimeGoldenValues()
       for _, key := range []string{"OfficeWorkPrompt", "BatchTaskPrompt"} {
           oldBytes, err := json.Marshal(m[key])
           if err != nil {
               t.Fatal(err)
           }
           newBytes, err := json.Marshal(want[key])
           if err != nil {
               t.Fatal(err)
           }
           if bytes.Equal(oldBytes, newBytes) {
               continue
           }
           if bytes.Count(raw, oldBytes) != 1 {
               t.Fatalf("golden value for %s not uniquely locatable (%d matches); STOP and report", key, bytes.Count(raw, oldBytes))
           }
           raw = bytes.Replace(raw, oldBytes, newBytes, 1)
       }
       if err := os.WriteFile(path, raw, 0o644); err != nil {
           t.Fatal(err)
       }
   }
   ```
   `runtimeGoldenValues()` 在 office_prompts_test.go（同包）可直接用。
   ```bash
   go test -tags fts5 ./pkg/agent -run TestZZRegenProjectTypePromptsGolden -count=1
   rm pkg/agent/zz_golden_regen_test.go
   git diff -- pkg/agent/testdata/project_type_prompts.golden.json
   ```
   → diff 只涉及 OfficeWorkPrompt/BatchTaskPrompt 两个 key 的值，**键序与其余
   格式逐字节不变**。其他 key 变化或键序重排＝STOP。若定向替换报「not uniquely
   locatable」＝STOP 回报（golden  escaping 与 Go marshal 不一致），不要自行
   整文件重写。

**Verify**:
```bash
go test -tags fts5 ./pkg/agent ./pkg/run -count=1
```
→ 全 ok：TestProjectTypePromptsGolden、TestOfficePromptsSayNothingAboutCode、
TestBatchPromptNeverAsksTheUser、TestOfficePromptsNameTheDeliveryTool、
TestCodingDeliveryPrompt*（含新短语）、runner_test 拼装断言。
任一禁词/钉住短语失败＝措辞偏离定稿，恢复定稿逐字文本，不要自行改写。

### Step 7: 契约注释与集成文档

1. `pkg/tool/artifacts.go` 头注释（:1-8）替换为：
   ```go
   // Deliverables: the present_files tool, the file classification every
   // artifact surface shares, and the card formatting for the tool's steps.
   //
   // A deliverable exists when the model says so: present_files registers each
   // presented file in fb_artifacts when the model judges the work complete and
   // hands it over, and every surface that shows what a conversation delivered —
   // the web card, the run's end frame, a batch task's integrator — reads that
   // table. Intermediate scripts and scratch files are never artifacts because
   // the model never presents them. Batch conversations carry one harness rule
   // on top (an integration surface, not an end user): at each execution end the
   // engine also registers the not-yet-delivered files left in the protocol
   // folder outputs/ (pkg/turn/batch.go).
   ```
   handler/description/ClassifyArtifact 一个字节都不动。
2. `docs/integration/batch-tasks.md:21` 生成物行替换为：
   ```
   | **生成物（artifact）** | 执行交付的文件。模型用 `present_files` 交付（`origin=model`），交付时机由模型判断任务完成、结果收口后主动发起。每次执行结束（含失败/停止/审批拒绝），引擎会把 `outputs/` 里尚未交付的文件登记为 `origin=fallback`（同一文件字节未变不重复登记）——这是面向集成方的兜底规则，保证产出了文件的执行不会看起来没产出。 |
   ```
3. 同文件 :350-354 段（「登记的是**文件路径**…补登为 `origin=fallback`。」）
   替换为：
   ```
   登记的是**文件路径**，不复制文件：文件后来被改动或删除，再次读取时状态会体现出来。**每次执行结束**
   （完成、失败、停止、审批拒绝），引擎都会把 `outputs/` 里的文件登记为 `origin=fallback`（跳过以 `.`
   开头的目录与文件，最多 200 个，不跟随软链；同一文件字节未变时不重复登记，模型已交付的同版本文件
   不会重复出现）。模型交付了一部分时，`outputs/` 里其余未交付的文件也会被补全。引擎不扫描
   `outputs/` 之外的变更：过程文件、脚本与中间数据不是交付物。
   ```
   文档其余部分（状态机、continue 提示、REST 形状）不动。

**Verify**:
```bash
go test -tags fts5 ./pkg/tool -count=1
grep -n "变更集\|部分交付也会被补全" docs/integration/batch-tasks.md
```
→ pkg/tool ok（含 present_files description golden 不变）；grep 无输出
（旧契约文案清零；:379 continue 段的「检查已有的 outputs/、.work/」是模型
指引，保留，不含这两个短语）。

### Step 8: 全量门禁＋范围审计

```bash
gofmt -l cmd pkg third_party plugins        # 无输出
go vet -tags fts5 ./...                      # exit 0
go test -tags fts5 ./... -count=1            # 全部 ok（≥900s 超时；scratch home）
scripts/test-db.sh up && scripts/test-db.sh all   # sqlite/mysql/postgres 全绿
scripts/test-db.sh down
git status --porcelain                       # 只有 Scope 内文件（未提交部分）
git diff --stat <起始SHA>..HEAD              # 逐文件对照 Scope 清单
```

**范围审计用 worktree 起始 SHA**（Git workflow 第一步记录的那个），**不要用
计划基线 `1ff34cf8`**：dev 在计划写就后可能已前进（他代理合并的 out-of-scope
提交，如 tui），基线全树 diff 会混入那些提交、造成假范围违规。in-scope 内容
漂移由计划开头的 drift check 负责（它只查 in-scope 路径）。

pkg/state 有改动→方言矩阵是 house rule 必跑项。docker 不可用＝STOP 回报
BLOCKED（不得静默跳过标 DONE）。全量套件若在他代理 WIP 文件（pkg/tui/setup*.go
不在你的 worktree 里；worktree 从 committed HEAD 建，理论上不带 WIP）或任何
非 in-scope 文件失败＝STOP 回报，不要修别人的代码。

## Test plan

- 新增（Step 1，pkg/turn/deliverables_test.go）：
  `TestSubmitInteractiveTurnRegistersNoFallbackArtifacts`（office/coding ×
  completed/cancelled/failed 共 6 子用例，复现的永久化）、
  `TestSubmitKeepsModelPresentedArtifacts`（positive control：模型交付行保留、
  scratch 不补登）。结构范例：batch_test.go 的 runFunc fixture。
- 重写（Step 2，pkg/turn/batch_test.go）：
  `TestBatchPartialModelDeliveryScanFillsOutputsGap`（部分交付＋常扫补全＋
  outputs/ 外不交付＋真 sha 去重）、
  `TestBatchApprovalRequiredStillScansOutputs`（审批拒绝终态仍扫 outputs/、
  work/ 追踪写入不交付）。
- 改名（行为不变）：`TestBatchScanRegistersOutputsFiles`。
- 删除：全部 `TestTurnTopUp*`（机制已废）、`TestFileChangesForRunTree`、
  `TestRunTreeIDs`、`TestSessionProjectScope`、
  `TestLatestArtifactByPathCountByOrigin` 的 count 尾部。
- 提示词：golden 两 key 再生＋coding 两个新短语钉住；禁词/never-ask/工具名
  测试必须原样通过。
- 回归面：pkg/turn 整包（submit/approval/batch/rewind）、pkg/state 整包、
  pkg/architecture（SQL 快照/dialect/fan-out）、pkg/agent、pkg/run、pkg/tool，
  最终全量＋方言矩阵。
- **评审核实不受影响、不得改动的既有测试**（executor 若见其红，STOP 回报，
  不要自行修改）：`TestSubmitBatchCancelStopsAndKeepsTheEpilogue`
  （batch_test.go:395——其「epilogue 跑在非取消 ctx」断言依赖 endBatch 既有
  的 detached context，本计划不动该行为；dot 规则断言 outputs/.work/x.py 与
  outputs/.hidden 被跳过，扫描函数体不变故仍成立）、
  `TestSubmitBatchUsageLimitFailsWithoutContinuation`（:462，无 artifact 断言）、
  `TestBatchScanFiresOnZeroModelDelivery`/`TestBatchContinueSecondTurnScansAndDedups`
  （常扫下断言仍成立，仅注释更新）、`recordingTaskStore`/`recordingArtifactStore`
  包装器（内嵌 SessionStore，接口收缩后仍满足；CountArtifactsByOrigin 随
  SessionStore 删除而自然消失，包装器无需改）。

## Done criteria

ALL must hold（机器可判）:

- [ ] `test ! -f pkg/turn/deliverables.go`
- [ ] `grep -rn "collectTurnDeliverables\|deliverableScope\|CountArtifactsByOrigin\|FileChangesForRunTree\|RunTreeIDs\|SessionProjectScope\|runTreeCTE\|artifactCountByOriginSQL" pkg/ --include='*.go'` 无输出
- [ ] `grep -c "deliver exactly those" pkg/agent/office_prompts.go pkg/agent/coding_delivery.go` 各 ≥1；`grep -c "When you judge the task complete" pkg/agent/office_prompts.go` ≥1
- [ ] `go test -tags fts5 ./pkg/turn ./pkg/state ./pkg/agent ./pkg/tool ./pkg/architecture ./pkg/run -count=1` 全 ok（scratch home）
- [ ] `go test -tags fts5 ./... -count=1` 全 ok
- [ ] `scripts/test-db.sh all` 三 dialect 全绿（或 BLOCKED 回报，不得 DONE）
- [ ] `gofmt -l cmd pkg third_party plugins` 无输出；`go vet -tags fts5 ./...` exit 0
- [ ] SQL 快照 diff 只含删除；golden diff 只含两个 key
- [ ] `git status --porcelain` 只有 Scope 内文件（plans/README.md 状态行除外）
- [ ] `git diff --stat <worktree起始SHA>..HEAD` 只有 Scope 内文件（不用计划基线做全树 diff）
- [ ] plans/README.md 本计划状态更新（评审方维护时除外）

## STOP conditions

Stop and report back (do not improvise) if:

- Current state 摘录与活体代码**内容**不符（行号漂移不算）。
- Step 1/Step 2 的测试没有按 stated 红因变红（机制已被他人改动＝drift）。
- 快照再生 diff 出现新增或编辑行（只允许删除）。
- golden 再生波及两个 key 以外的值，或禁词/钉住短语测试失败且恢复定稿后仍失败。
- 常扫出现计划外行为（如 exec 读回失败路径需要新逻辑、扫描把 inputs/ 或
  .work/ 卷入）——函数体本不应改动。
- 方言矩阵 docker 不可用（标 BLOCKED，不得静默跳过）。
- 全量套件在非 in-scope 文件失败（他代理 WIP 或既有缺陷）——回报，不修。
- 需要触碰 Out of scope 文件才能完成任何步骤。
- owner 对最终契约（裁决 8＋A 协议位置兜底）另有裁决。
- 关键假设「office/batch 项目通常非 git，shell 成品不进变更集」被证伪到影响
  设计（例如发现 batch workspace 实际 git-init）——回报证据，不要自行加追踪。

## Maintenance notes

- **契约来源**：owner 2026-11-19 系列裁决（六条＋撤销 completed 帧硬保证＋
  batch 时机一致＋协作规则，最终征询复裁 A；见 Why this matters 逐条引用；
  项目记忆 ad-hoc notes 2026-11-19T01-36-11 / 01-38-37 / 01-43-15 / 01-43-22 /
  01-54-00 / 02-11-35 / 02-13-47，全局协作规则 global/…02-11-29）。今后任何
  交付物改动先对照：prompt-driven（三类型时机与集合由模型判断）、无 harness
  启发式资格判断、用户明确指定优先、不漏用户需要的、batch 仅 outputs/ 协议
  位置兜底（A，不得扩大）。
- **相邻缺陷（本期不做，owner 已知情）**：`AbandonedRunReaper`（turn/events.go:480）
  只结算孤儿 run、从不结算 batch execution 行（任务 API 永远 running）也不发
  batch 通知——与交付物无关的独立缺口（324 诚实极限清单「reaper 结算无
  epilogue」同源）。需要时另立计划，不在本计划范围。
- **评审重点**：提示词字节（定稿逐字）、golden diff 范围、快照 diff 只删、
  batch 触发器注释与裁决 3 的对应、submit.go 四块删除后 batchTurn 其余用途
  完好、Step 1 断言用 out.RunID 而非 fixture runID。
- **交互类型完整性从此依赖 prompt 遵从**：模型仍可能漏 present（无 harness
  网是设计选择，不是缺陷）；用户可追问，模型可在后续回合补交（present_files
  同路径再交付＝新版本）。若未来 office 引入协议交付目录（如 outputs/），
  可仿 batch 加豁免规则——需 owner 裁决，不得自行加。
- **batch 常扫成本**：每次执行结束哈希 outputs/ ≤200 文件（全文件 sha）；
  无人值守场景不敏感。若未来 outputs/ 出现超大文件导致收尾变慢，考虑给
  hashArtifactFile 加尺寸上限——另立项，不在本计划。
- 324 的展示平价 machinery（Deliverable.ID/RunArtifacts/reducer/replay）保留；
  它们对行来源无假设，fallback 行减少不需要 surface 改动。
- 与 324 rev4 的关系：本计划是其兜底部分的裁决后修正，324 的其余成果
  （present_files 全类型注册、coding 提示词、TUI 平价、shell_diff 失败分支
  ChangedFiles）不受影响；324 计划文件是历史留痕，不回写。

## 评审修订记录（2026-11-19 review-plan：advisor 自审＋独立冷读）

**评审方式**：advisor 亲跑计划 Step 1 复现（overlay 注入计划原文测试，
6 子用例＋positive control 共 7 条全红，红因与计划陈述逐字吻合）＋机械校验
（提示词禁词/钉住短语、快照删除数、golden 键序、脚本子命令、选项存在性）；
独立冷读子代理（plan 型，零上下文）逐锚点核实，并**用 overlay 模拟 Steps 1–4
终态跑整包 pkg/turn 全绿**、实证 office 禁词缺陷（修前 FAIL「contains diff」/
修后 ok）、coding 定稿全绿、快照再生与现树同步。冷读子代理在收尾汇总前撞
模型配额（HTTP 429）；其已完成的 7/8 项验证证据由 advisor 从运行轨迹直接
取证并入本记录，剩余残留检查由 advisor 完成。**源码零改动**。

**发现与修正（全部已就地修入计划）**：

1. **MUST-FIX｜Step 6 office 定稿禁词**：原稿「pick a different set」的
   `different` 含子串 `diff`，会打红 `TestOfficePromptsSayNothingAboutCode`
   （子串匹配）。advisor 机械 grep 与冷读 overlay 实跑独立确认。已改
   「pick another set」；措辞红线加子串陷阱警示（different⊃diff、digit⊃git）
   与改后机械复验命令。
2. **SHOULD-FIX｜Step 8 范围审计基线陷阱**：dev 已前进（1ff34cf8→29ad88da，
   他代理 tui 合并），worktree 对计划基线做全树 diff 会混入 out-of-scope 提交
   造成假范围违规。已改：Git workflow 第一步记录 worktree 起始 SHA，Step 8
   与 Done criteria 用起始 SHA；in-scope 内容漂移仍由开头 drift check 负责。
3. **SHOULD-FIX｜Step 6 golden 再生键序重排**：golden key 为声明序非字母序
   （实测确认），整 map `MarshalIndent` 会重排全部 key、埋没真实 diff。已改
   **定向字节替换**（两个旧值转义串在 raw 中各唯一出现一次，实测验证），键序
   与其余格式逐字节不变；不唯一即 STOP，不得整文件重写。
4. **CLARIFICATION｜Step 5 快照 run_tree 残留**：再生后 `grep run_tree` 快照
   仍 1 条命中（llm_attempts.go 静态语句含 run_tree 片段，非删除对象）；
   runrt.go 的 run_tree 内联字面量是动态拼装位点、本就不在快照。删除数以
   diff 的 3 条 `-` 为准（核实：2 条 run_tree 前缀复合语句＋1 条
   count-by-origin；快照总 446 条）。
5. **CLARIFICATION｜Test plan 补「评审核实不受影响」清单**：
   `TestSubmitBatchCancelStopsAndKeepsTheEpilogue`（:395，detached-ctx 断言
   依赖 endBatch 既有行为，扫描函数体不变）、
   `TestSubmitBatchUsageLimitFailsWithoutContinuation`（:462，无 artifact
   断言）、`recordingTaskStore`/`recordingArtifactStore`（内嵌 SessionStore，
   接口收缩后仍满足）——冷读模拟终态整包绿证实；executor 见其红应 STOP 回报
   而非自改。

**核实通过（不构成发现）**：Current state 全部锚点内容吻合（in-scope 零漂移，
HEAD 29ad88da）；Step 1/2 内联测试代码可编译可运行（helper/option/类型/
out.RunID 语义均核实）；步骤间编译连续；被删符号全仓（pkg/cmd/docs/frontend/
plugins/scripts）无漏网调用方；旧 office 措辞（"do not present helper
scripts"/"When the files are final"/"When the outputs are final"）无任何测试
钉住；coding 五钉住短语＋两新短语均在定稿、禁词 0 命中；batch never-ask 句
在位；`scripts/test-db.sh` up/all/down 存在；`WithRunExecutor`
（service.go:198）；方言覆盖测试无数量下限（删除安全）。

**评审结论**：计划可由零上下文弱执行器按文执行——先红后绿复现、Steps 1–4
终态整包绿、提示词/golden/快照门禁均经实证。Status 仍 TODO，待 owner execute。