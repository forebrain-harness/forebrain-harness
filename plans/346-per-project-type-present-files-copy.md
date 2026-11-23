# present_files 按项目类型分写描述 / 结果句，并删除单次文件数上限

计划编号：落地时写入 `plans/<编号>-per-project-type-present-files-copy.md`，编号取 `ls plans | rg '^[0-9]{3}-' | sort | tail -1` 的下一个（当前最大 345，不要预设 346 空闲），并在 `plans/README.md` 加索引行。

> **owner 裁决（本会话，四轮评审之后）**
> 1. coding 描述按 dev 现状写（不承诺也不否认未来引擎补登）。
> 2. 三类描述都写「用户点名要哪些就只交哪些」的优先句。
> 3. **删除 `files` 的 `maxItems=20` 上限，不设上限。**
> 4. `presentFilesToolNote`（结果句）本次一起按类型分写。
>
> 修订记录：第一轮冷读（步骤红态、Done 可查化、假绿门）→ 第二轮 glm-5.3（先复现步骤、测试计数 10→13、`stays frozen` 死锁；其「`FAKE_DUMP_DIR` 不存在」**不成立**——实现在 `.claude/skills/run-forebrain/fake_provider.py:526-537`，文档 `SKILL.md:118`，评审用 `rg` 未加 `--hidden` 才搜不到隐藏目录）→ 第三轮 deepseek-flash（`format_test.go:3450` 的同名断言、点名例外、复现文件名违规、dump 覆写与 wait 假阳性、PATCH 不改变运行中会话、缺漂移检查）→ 第四轮 deepseek-flash（跨包钉用了只存在于 peer 分支的动词 `present`（基线是 `deliver`）→ 改为动词中立的 `exactly those — no others and none missing`；`rg -c` 数出 8 行不是 5 → 改用 `^const` 只数声明；新增测试是 5 个故总数 15 不是 14；删上限只有 schema 半边证据 → 补 21 文件的行为测试；batch 第三句与点名句在"任务点名输入文件"时相撞 → 只保留 `.work/` 排除；batch 结果句的 "this list" 过度承诺 → 改 "the task's result"）。

## 1. 目标

`present_files` 的两个模型可见硬编码文本——工具描述（`pkg/tool/artifacts.go:40`）和结果句（`:204`）——只有一套办公口径，与 coding 项目「本轮改的每个文件（含源码/脚本）都交」的系统提示词直接矛盾；`files` 的 `maxItems=20` 又与描述里「Call it once … listing every deliverable」互相拆台（peer 的 `97b4158e` 就改了 22 个文件）。

修完后：**一条工具契约，三套按项目类型选定的措辞（描述 + 结果句），且不再有单次文件数上限。**

## 2. 根因（file:line，均已在当前工作区核实）

| 环节 | 位置 | 事实 |
| --- | --- | --- |
| 描述常量 | `pkg/tool/artifacts.go:40` | 枚举办公成品 + "Do not present intermediate scripts, scratch data or files you did not finish" |
| 结果句常量 | `pkg/tool/artifacts.go:204` | "The user now sees a download and preview card for each file."（batch 无 user） |
| 单次上限 | `pkg/tool/artifacts.go:199` | `jsonschema:"minItems=1,maxItems=20"` |
| 注册无差别 | `pkg/tool/registry.go:181-190`、`:314-317` | 注释 "every project type registers the tool with the same contract"；`:212` 传同一常量 |
| coding 规则 | `pkg/agent/coding_delivery.go:16` | 首句「用户点名 → deliver exactly those — no others and none missing — even if this turn changed more」；其次「every file this turn created or modified … source code, scripts … No kind of file is excluded: a helper script or a one-line source edit is delivered exactly like a report or an image」 |
| coding 注入点 | `pkg/run/runner.go:1218-1238` | office → `OfficeWorkPrompt`；batch → `BatchTaskPrompt`；**default → `CodingDeliveryPrompt`** |
| office 规则 | `pkg/agent/office_prompts.go:18`、`:48` | helper/中间数据放 `.work/`，"never next to the deliverables"；「never part of that set **unless the user explicitly asked for them**」 |
| batch 规则 | `pkg/agent/office_prompts.go:78`、`:85` | 「write every deliverable into outputs/; keep helper scripts and intermediate data in .work/ and do not present them. Never modify… inputs/」；「That list is the structured result the calling system receives」 |
| batch 结果契约 | `pkg/turn/batch.go:330-352`、`:461`、`pkg/gateway/channels.go:376` | `Artifacts` 进 `BatchExecutionEnd`；执行末补登记 `outputs/`（`Origin=fallback`）；通知据此列出交付物 |
| 类型归一 | `pkg/state/project_store.go:161-182`、`pkg/run/runner.go:3606-3611` | office/batch/其余→coding；进 `AgentToolRuntime` 前已归一化 |
| 会话类型不随 PATCH 变 | `pkg/process/runner_pool.go:856-874`（`withSessionType`） | 运行中的会话永远解析到自己的**出生类型**；PATCH 只影响此后新建的会话 |
| 复现实证 | 评审 worktree 的 scratch 测试 | 以 `ProjectType: coding` 构造，描述含 "intermediate scripts"、不含 "source code/outputs//.work/" → 红（本计划 Step 0.5 自建同样的红） |

## 3. 修复设计

### 3.1 形状

`pkg/tool` **不能** import `pkg/agent`（`pkg/architecture` 的 `TestToolAndHookDoNotImportAgent`；层表 agent:0 / tool:2），所以文本留在 `pkg/tool/artifacts.go`，一个函数同时给出描述与结果句（**一个 switch、一个含义**）：

```go
const presentFilesToolDescriptionCoding = "…"
const presentFilesToolDescriptionOffice = "…"
const presentFilesToolDescriptionBatch  = "…"
const presentFilesToolNoteUser  = "The user now sees a download and preview card for each file."
const presentFilesToolNoteBatch = "The calling system receives the task's result, with a download and preview card for each file."

// presentFilesCopyFor is one project type's model-visible wording: the tool's
// description and the sentence its result carries. The behaviour behind the
// words is one contract in all three. The value arrives normalized
// (pkg/run/runner.go ProjectType), so the comparison matches batchProject's;
// keep the wording in step with agent.CodingDeliveryPrompt /
// agent.OfficeWorkPrompt / agent.BatchTaskPrompt — those prompts name this
// tool, and a mismatch is the defect this selector exists to remove.
func presentFilesCopyFor(projectType string) (description, note string) {
	switch projectType {
	case state.ProjectTypeOffice:
		return presentFilesToolDescriptionOffice, presentFilesToolNoteUser
	case state.ProjectTypeBatch:
		return presentFilesToolDescriptionBatch, presentFilesToolNoteBatch
	default:
		return presentFilesToolDescriptionCoding, presentFilesToolNoteUser
	}
}
```

- 每个常量**独占一行 `const`**（不要分组成 `const ( … )` 块），Done 判据按行数核对。
- 白名单 + 泛型 default（仓库既有裁决；`runner.go:1218`、`batchProject`（`registry.go:216-218`）同形）；**不调 `NormalizeProjectType`**，避免第二种归一化约定。
- `newPresentFilesTool` 签名不变：开头 `desc, note := presentFilesCopyFor(rt.ProjectType)`，`llm.NewTool(name, desc, …)`，payload 闭包用 `note`。`rt` 在构造处非 nil（`registry.go:185-186` 有守卫），不加 nil 防御；`artifacts.go:214` 既有的死防御不动。
- 编译期常量 → 工具表按会话恒稳（`pkg/agent/office_prompts.go:1-8` 的 prompt 缓存约束）。
- 无新增 import（`pkg/state` 已在 `:29` 导入）→ fan-out 预算与 `pkg/architecture/testdata/graph.json` 不变。

### 3.2 删除单次上限

`pkg/tool/artifacts.go:199`：`jsonschema:"minItems=1,maxItems=20"` → `jsonschema:"minItems=1"`。仓库已有同向先例（`docs/plan/USER_INTERACTION_SUBAGENT_ASK_FIX_PLAN.md` 删除 human 工具的 maxItems，测试写法见 `pkg/tool/human_test.go:392-412`「schema 不得声明 validator 不再强制/无法满足的上限」）。删掉后描述里的 "Call it once … listing every deliverable" 才真正可执行。

**如实记录的代价**（owner 已裁决，不加防御上限）：一次调用现在会对列出的每个文件 `stat` + 全量 sha256（`hashAndClassifyFile`），耗时与总字节成正比；列表本身受模型上下文约束。这是被接受的非功能取舍，不引入超时或分批逻辑。

### 3.3 三套文本（逐字，执行器照抄）

**coding**
> When the user's message names the files to deliver, present exactly those — no others and none missing — even if this turn changed more. Otherwise, in this project the work itself is the deliverable: every file this turn created or modified inside the project directory — source code, scripts, configuration, data, documents, images, audio or video, archives; no kind of file is excluded. Do not present a file this turn did not change, a deleted file, or a file created only to have something to present.

**office**
> When the user's message names the files to deliver, present exactly those — no others and none missing. Otherwise the deliverables are the finished work this turn produced: documents, spreadsheets, slides, PDFs, images, audio or video, HTML or Markdown pages, archives. Do not present helper scripts, intermediate data or scratch files (they belong in the project's .work/ folder) unless the user explicitly asked for them, or anything you have not finished.

**batch**
> When the task message names the files to deliver, present exactly those — no others and none missing. Otherwise the deliverables are the files in this execution's outputs/ folder (the task workspace), which the calling system reads as the task's result. Never present helper scripts or intermediate data from .work/, and never a file that is not final.

**共用收尾**（三份逐字相同，紧接领头句，中间一个空格）
> Call it once, after the files are final, listing every deliverable; each one gets a download and preview card. Paths must be inside the project directory — save or copy a deliverable there first if it was written elsewhere. Presenting the same path again later records a new version.

**结果句**：coding/office = `presentFilesToolNoteUser`（字节与今天相同）；batch = `presentFilesToolNoteBatch`。

为什么这样写（不要"优化"）：
- 点名优先句是三类提示词的**首句**（`coding_delivery.go:16`、`office_prompts.go:48`、`:85`），缺了它就是同一类缺陷的温和版。
- 收尾句三份字节相同 = 工具机制只有一个说法；它**不断言"只有你 present 的才会到用户手上"**，因此 dev 现状与未来 peer 补登合入后都成立。
- coding 领头句的 "every file this turn created or modified inside the project directory"、"no kind of file is excluded" 是 `CodingDeliveryPrompt` 的同名字面短语；peer 分支 `97b4158e` 的修订保留这两句。
- office 保留 `.work/` 归属与 "unless the user explicitly asked for them" 例外（不写得比 prompt 更严）。
- batch 的 `outputs/`、`.work/`、calling system 分别对齐 `office_prompts.go:78/85` 与 `pkg/turn/batch.go:330-352`。**刻意不写"永不 present inputs/"**：点名优先句会让它与"任务点名了某个输入"相撞；输入是否该交由任务消息决定，`outputs/` 是默认交付区已足够。
- batch 结果句说 "the task's result" 而非 "this list"：执行结束报告的分页上限（`batchArtifactListLimit`）与 fallback 补登（`Origin=fallback`）意味着调用系统拿到的未必等于这一次调用的列表，不能过度承诺。
- 收尾句的 "download and preview card" 对 `7z/rar/psd/…`（`preview: ""`，`:120-128`）只有下载——与现状同源，不要在别处再加"每个都能预览"。

### 3.4 行为变化矩阵

| 场景 | 旧行为 | 新行为 | 性质 |
| --- | --- | --- | --- |
| coding 会话读描述 | 只被教"文档类 + 别交脚本" | 点名优先 + 本轮每个文件都交（含源码/脚本） | **刻意修订（本 bug）** |
| office 会话 | 成品枚举 + 不交中间脚本/scratch，无 `.work/` 归属、无点名句、混入 batch 式 "system that started this task" | 点名优先 + 成品枚举 + `.work/` 归属 + "用户明确索要"例外 | 措辞修正，语义不变 |
| batch 会话 | office 文本（"the user"，无 outputs/.work 规则） | 点名优先 + outputs/ 交付区 + `.work/` 排除 + 调用系统读任务结果 | **刻意修订** |
| batch 结果句 | "The user now sees…" | "The calling system receives the task's result, with a download and preview card for each file." | **刻意修订** |
| coding/office 结果句 | 同句 | 字节不变 | 不变 |
| 单次 >20 个文件 | schema 上限 20，模型无法一次列全 | 无上限，一次列全 | **owner 裁决的刻意修订** |
| 未知/空项目类型（裸 runtime、测试） | office 文本 | coding 文本（=`NormalizeProjectType`/runner default 的既有语义） | 一致化 |
| 会话中途 PATCH 项目类型 | 运行中会话仍按出生类型（`withSessionType`） | 不变 | 既有行为（计划早先把此写错，已按证据改正） |
| 工具行为（校验/落库/卡片/主 agent-only/原子校验） | — | 零变化 | 不变 |
| `registry.go:190` `registerMeta` 短句 | "final files the user should receive (download and preview cards)" | 不动 | 非模型可见（只写 `event.ToolMeta`） |

## 4. Scope

**In scope**
- `pkg/tool/artifacts.go`：包注释 1-11 行；:32-40 常量区（三描述 + 两结果句）；:199 schema；:204 结果句常量；:209-212 构造；:260-267 payload 闭包取 `note`。
- `pkg/tool/registry.go`：`:181-183`、`:314-317` 两处注释。
- `pkg/tool/testdata/`：新增 `present_files_description_coding.golden`、`_office.golden`、`_batch.golden`；**删除** `present_files_description.golden`。
- `pkg/tool/artifacts_test.go`：golden 测试表驱动化；扩展 `TestPresentFilesRegisteredForEveryProjectType`（:339-360）；新增规则/结果句/无上限/默认值/跨包短语互钉测试。
- `pkg/agent/coding_delivery.go:11`：**纯注释一行**——"shared tool description stays frozen." 被本计划推翻，改为"the tool description is chosen per project type."。已核实 peer 分支 `97b4158e` **也保留**这句，故无论谁先落地都由实现方收掉；只改这一行，不动正文，改前 `peer_send` 告知。
- `plans/<编号>-…md` + `plans/README.md` 一行（落地时）。

**Out of scope**
- `pkg/agent/office_prompts.go`、`pkg/agent/coding_delivery.go` **正文**。
- `pkg/turn/*`、`pkg/gateway/*`、`frontend/*`（`frontend/src/lib/artifacts.test.ts:20` 的 note 是自包含 fixture，不读 Go 常量；已核实无 Go 测试断言结果句文本）。
- `registry.go:190` 的 event-meta 短句（非模型可见）。
- peer 的 coding 轮末补登（分支 `97b4158e`，基线 `8e08d693`，已提交未合并，合不合并由 owner 决定）。

**不能改的公共形状**：工具名 `present_files`、入参字段（`files[].path/title/description`）、`files` 的 `minItems=1`、错误文案、`Deliverable` 结构与全部下游。

## 5. 前置条件（Step 0，硬门）

**基线**：主树 `8e08d693`，工作区干净（已核）。**漂移检查**（内容不符即停，行号漂移不算）：

```bash
git -C . diff --stat 8e08d693 -- pkg/tool/artifacts.go pkg/tool/registry.go pkg/tool/artifacts_test.go pkg/tool/testdata pkg/agent/coding_delivery.go
# 必须为空
```

**peer 现状（peer 本人确认）**：源码已完成并提交到分支 `97b4158e`（基线 `8e08d693`），worktree `.worktrees/coding-deliverables-topup`，**未合并**，落不落 dev 由 owner 决定；他们只改 `artifacts.go` 包注释 1-11 行与 `coding_delivery.go`（注释 + prompt 正文），不动常量/golden/handler/schema，并明确不会改本计划的落点。文本冲突可能有，语义不冲突。

判定（按内容，不看 `git status`——peer 已提交，status 为空）：

```bash
git -C .worktrees/coding-deliverables-topup log --oneline dev..coding-deliverables-topup -- pkg/tool/artifacts.go pkg/agent/coding_delivery.go
git -C . log --oneline -3 -- pkg/tool/artifacts.go     # 主树是否已并入 peer 的工作
```

- **peer 未合并（当前状态）** → 按主树实施；合并时的文本冲突由合并方解决。
- **peer 已合并** → 重读 `artifacts.go` 顶部注释（逐条保留 peer 记的每条 harness 规则）并确认 `coding_delivery.go` 仍要求"present every created or modified file"；是 → §3.3 的 coding 文本照用。
- **peer 落地后改口成"present 完全可选"** → 停，按 §8.2 报 owner。

## 6. Steps

**Step 0.5 先复现（动代码之前；红态证据）**
- 在 `pkg/tool/artifacts_test.go` 末尾追加临时测试（**不要新建文件**：`pkg/architecture` 的 `TestFileNamesHaveAtMostTwoUnderscores` 限制文件名最多两个下划线，`present_files_conflict_repro_test.go` 有四个会直接红）：

```go
func TestReproCodingSessionIsTaughtTheOfficeRule(t *testing.T) {
	tl, err := newPresentFilesTool(NewState(t.TempDir()), &AgentToolRuntime{
		Sess: newStateSessionStoreForTest(t), ProjectType: state.ProjectTypeCoding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tl.Description(), "intermediate scripts") {
		t.Fatalf("coding session is taught the office rule:\n%s", tl.Description())
	}
}
```
- 运行：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestReproCodingSessionIsTaughtTheOfficeRule' -count=1 -v` → 期望 **FAIL**，打印含 "Do not present intermediate scripts, scratch data…" 的办公口径全文；把输出逐字记进完成报告。
- 该测试保留到 Step 1 之后再跑一次（**绿**）作为修复实证，然后 Step 2 删除（其断言已由永久测试覆盖）。

**Step 1 文本 + 选择函数 + schema（一次改完，避免中途编译红态）**
- `pkg/tool/artifacts.go`：按 §3.1/§3.2/§3.3 写三描述 + 两结果句 + `presentFilesCopyFor`；:199 去 `maxItems`；:212 用 `desc`；payload 闭包用 `note`；更新 :32-35、:202-208 的 doc；重写包注释 1-11 行（分类型表述，逐条保留当时文中的每条 harness 规则）。
- 验证：`gofmt -l pkg/tool` 无输出；`CGO_ENABLED=1 go build -tags fts5 ./pkg/tool` exit 0；Step 0.5 的复现测试转 **PASS**。

**Step 2 golden + 测试（一次改完，避免读不到旧 golden）**
- 新增三份 golden（常量逐字 + 一个结尾换行）；`rm pkg/tool/testdata/present_files_description.golden`；删除 Step 0.5 的临时测试。
- `pkg/tool/artifacts_test.go`：
  1. `TestPresentFilesDescriptionGolden` → `TestPresentFilesDescriptionPerProjectType`：表驱动 `{projectType, description, golden}`；`newPresentFilesTool(...)` 断言 `Description() == description`、`golden 去尾换行 == description`。
  2. 扩展 `TestPresentFilesRegisteredForEveryProjectType`：已注册工具的**描述**等于 `presentFilesCopyFor(tc.projectType)` 的描述（该测试的 runtime 没有 `Sess`，`*llm.Tool` 也没有结果句访问器——结果句只能在 handler payload 里断，见 #3）。
  3. 新增 `TestPresentFilesNoteNamesTheReceiver`：照 `TestPresentFilesRecordsEveryFile` 的形状（`sess.Ensure(ctx, "sess-art", …)` + `presentFilesTestCtx(t, root)`，否则落库 FOREIGN KEY 失败），三种项目类型各 `Handle` 一次，解析返回 JSON 的 `payload["note"]`：coding/office == `presentFilesToolNoteUser`，batch == `presentFilesToolNoteBatch`，且 batch 那句不含 `"The user"`。
  4. 新增 `TestPresentFilesDescriptionNamesEachTypesRule`：
     - 三份都含 `"present exactly those"`；
     - coding 含 `"source code, scripts"`、`"no kind of file is excluded"`，**不含** `"intermediate scripts"`；
     - office 含 `"helper scripts"`、`".work/"`、`"unless the user explicitly asked for them"`，**不含** `"source code, scripts"`；
     - batch 含 `"outputs/"`、`".work/"`、`"calling system"`；
     - 三份两两不等；三份都以共用收尾句结尾（`strings.HasSuffix(desc, presentFilesMechanicsSentence)`，该句在测试里定义为常量，生产代码不做拼接）。
  5. 新增 `TestPresentFilesSchemaHasNoFileCap`（照 `pkg/tool/human_test.go:392-412` 的写法，但这里的 `tl` 已是 `*llm.Tool`，直接 `tl.InputSchema()`）：`properties.files` **不得**出现 `maxItems`；`minItems` 仍为 1。
  6. 新增 `TestPresentFilesAcceptsPastTheOldCap`：建 21 个临时文件，一次 `present_files` 调用 → 21 条 `presented` 行、无错误（钉住"运行期也没有上限"，不只是 schema。**必须复用 `presentFilesTestCtx` 的 `sess-art`/`run-art`**，否则 FK 失败——评审首版就踩了这个）。
  7. 新增 `TestPresentFilesDescriptionDefaultsToCoding`：`ProjectType` 为 `""` 与 `"wiki"` 都得到 coding 描述与 user 结果句。
  8. 新增跨包短语互钉 `TestPresentFilesDescriptionKeepsThePromptPhrases`：import `pkg/agent`（**仅 `_test.go`**；`pkg/architecture` 三处遍历都以 `strings.HasSuffix(path, "_test.go")` 跳过测试文件，`pkg/agent` 只 import `pkg/llm`，无环）。断言**动词中立**的共享片段：三份描述都含 `"exactly those — no others and none missing"`（基线提示词写 `deliver exactly those…`，peer 合并后写 `present exactly those…`，这个片段两版都在）；再加 coding 描述与 `agent.CodingDeliveryPrompt` 共享 `"every file this turn created or modified"`、office 与 `agent.OfficeWorkPrompt` 共享 `".work/"`、batch 与 `agent.BatchTaskPrompt` 共享 `"outputs/"`。
- 验证：`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestPresentFiles' -count=1 -v` → **不出现任何 `--- FAIL`**，且下列名字逐个 `--- PASS`：`TestPresentFilesDescriptionPerProjectType`、`TestPresentFilesRegisteredForEveryProjectType`、`TestPresentFilesNoteNamesTheReceiver`、`TestPresentFilesDescriptionNamesEachTypesRule`、`TestPresentFilesSchemaHasNoFileCap`、`TestPresentFilesAcceptsPastTheOldCap`、`TestPresentFilesDescriptionDefaultsToCoding`、`TestPresentFilesDescriptionKeepsThePromptPhrases`（**不写死总数**——基线 10 个（含被改名的那 1 个）+ 新增 6 = 16，但数字不是判据；`[no tests to run]` 属假绿）。

**Step 3 注释归正**
- `pkg/tool/registry.go`：`:181-183` 去掉 "with the same contract"；`:314-317` 说明改为"描述与结果句按此类型选择"。
- `pkg/agent/coding_delivery.go:11`：`shared tool description stays frozen.` → `the tool description is chosen per project type.`（纯注释；先 `peer_send` 告知）。
- 验证（**只查这三个文件**，`pkg/tool/format_test.go:3450` 也有 "the same contract"，属范围外）：
  `rg -n "same contract|stays frozen" pkg/tool/artifacts.go pkg/tool/registry.go pkg/agent/coding_delivery.go` → 0 行。

**Step 4 门禁（CI 口径，已核 `.github/workflows/ci.yml`）**
```bash
gofmt -l cmd pkg third_party plugins                      # 无输出
CGO_ENABLED=1 go vet ./...                                # CI 同款（无 tags）；exit 0
CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -count=1      # ok（基线 14.8s）
CGO_ENABLED=1 go test -tags fts5 ./pkg/agent -count=1     # ok（正文未动）
CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1  # ok（分层/fan-out/图未变）
```

**Step 5 真机验收：模型实际收到的描述（fake provider 请求落盘）**
- 机制：`FAKE_DUMP_DIR` 由 `.claude/skills/run-forebrain/fake_provider.py:526-537` 实现，把每个请求体写成 `request-%04d.json`（`SKILL.md:118`）。**每腿独立目录**（provider 的计数器是进程内的，`$D reset` 不清 dump 目录，同目录会被覆写）。

```bash
D=.claude/skills/run-forebrain/driver.sh
WORK="${FOREBRAIN_RUN_WORK:-${TMPDIR:-/tmp}/forebrain-run}"

# coding 腿（有 .git → DefaultProjectTypeForRoot 判 coding）
$D reset
mkdir -p "$WORK/proj" && git -C "$WORK/proj" init -q
export FAKE_DUMP_DIR="${TMPDIR:-/tmp}/fb-dump-coding" && rm -rf "$FAKE_DUMP_DIR"
$D start reply 'hi'
$D submit 'hi'
for i in $(seq 1 30); do ls "$FAKE_DUMP_DIR"/request-*.json >/dev/null 2>&1 && break; sleep 1; done   # 等请求落盘，不等 'hi'（那是自己的消息）
jq 'keys' "$FAKE_DUMP_DIR"/request-0001.json                       # 先看真实字段形状
jq -r 'select(.tools and (.tools|length)>0) | .tools[] | select((.function.name // .name)=="present_files") | (.function.description // .description)' "$FAKE_DUMP_DIR"/request-*.json | sort -u
```
  - 断言：输出**逐字**等于 `presentFilesToolDescriptionCoding`（标题命名请求可能 `tools` 为空，故扫描全部 dump、过滤空 `tools`）。
- office 腿：`$D reset` 后**不** `git init`，`FAKE_DUMP_DIR` 换成 `${TMPDIR:-/tmp}/fb-dump-office`，重复断言，输出等于 office 常量。
- batch 腿：TUI 不可达（由执行引擎跑），由 Step 2 单测覆盖；如实写明，不假装跑过。
- `$D stop`；不污染真实 `~/.forebrain`（driver 用 `$WORK/home`）。

**Step 6 收口**
- 写入 `plans/<编号>-per-project-type-present-files-copy.md`（状态行改"已实施并验证（日期）"，附 Step 0.5 红/绿输出与 Step 5 dump 证据），`plans/README.md` 加索引行。
- `git status --porcelain -- pkg/tool pkg/agent plans` → 只含 §4 In scope 路径。
- **不 commit**（改动留工作区等 owner 评审）。

## 7. Done criteria（机器可查）

1. `rg -n 'presentFilesToolDescription\b|presentFilesToolNote\b' pkg/` → 0 行（旧单常量名消失）；`rg -c '^const presentFilesTool(Description|Note)' pkg/tool/artifacts.go` → 5（**必须一行一个 `const`，不要分组成 `const ( … )` 块**；宽模式 `^const presentFilesTool` 会连 `const presentFilesToolName` 一起数成 6）。
2. `rg -n 'maxItems' pkg/tool/artifacts.go` → 0 行。
3. `ls pkg/tool/testdata | rg present_files` → 恰三个 `_coding/_office/_batch.golden`，无旧名。
4. `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool -run 'TestPresentFiles' -count=1 -v` → **无 `--- FAIL`**，且逐个 `--- PASS`：`TestPresentFilesDescriptionPerProjectType`、`TestPresentFilesRegisteredForEveryProjectType`、`TestPresentFilesNoteNamesTheReceiver`、`TestPresentFilesDescriptionNamesEachTypesRule`、`TestPresentFilesSchemaHasNoFileCap`、`TestPresentFilesAcceptsPastTheOldCap`、`TestPresentFilesDescriptionDefaultsToCoding`、`TestPresentFilesDescriptionKeepsThePromptPhrases`（不写死总数：数字不是判据，名字才是）。
5. `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool ./pkg/agent ./pkg/architecture -count=1` → 三包 ok。
6. `gofmt -l cmd pkg third_party plugins` 无输出；`CGO_ENABLED=1 go vet ./...` exit 0。
7. Step 5 两份 dump 的 `present_files` 描述分别逐字等于 coding / office 常量。
8. `rg -n "same contract|stays frozen" pkg/tool/artifacts.go pkg/tool/registry.go pkg/agent/coding_delivery.go` → 0 行。
9. `git status --porcelain -- pkg/tool pkg/agent plans` → 只含 In scope 路径；`git diff --stat 8e08d693 -- pkg/turn pkg/gateway frontend` → 空。

## 8. STOP conditions

1. **漂移**：§5 的 `git diff --stat 8e08d693 -- <in-scope paths>` 非空 → 停，重新定位（内容不符即停；行号移动不算）。
2. **peer 占用未解除**：Step 0 判定显示 peer 仍在改 `artifacts.go`/`coding_delivery.go` 且未约定顺序 → 停，先协调；不得覆盖、stash、revert 别人的工作。
3. **peer 落地后语义改口**：coding 提示词不再要求"present every created or modified file" → 停，报 owner；§3.3 的 coding 句只在"仍要求全交"时成立。
4. **复现不红**：Step 0.5 的测试今天没红（coding 会话没拿到办公口径）→ 停，说明基线与 §2 不符，先重查。
5. **golden 与常量逐字不等且原因不明** → 停，不改 golden 去迎合常量。
6. **跨包互钉测试被 vet/architecture 拒绝**（预期不会）→ 停，报证据，不放宽断言、不改 `pkg/agent` 正文。
7. **发现第三处模型可见的"什么算交付物"文本** → 先报告，不擅自并入。
8. **包级回归出现与本改动无关的失败** → 报给对应 agent，不代修。

## 9. Maintenance notes

- **必须同步的三处手写文本**：`pkg/tool/artifacts.go` 三套描述/结果句 ↔ `pkg/agent/coding_delivery.go`、`office_prompts.go` 交付段。生产代码不能跨层 import，但两端各有短语钉（`pkg/agent/coding_delivery_test.go` 的完整性短语 + 本计划的跨包互钉测试）。
- **`pkg/agent/testdata/project_type_prompts.golden.json`** 钉了 office/batch 全量提示词（`pkg/agent/office_prompts_test.go:44` 读它）：本计划不动提示词故不受影响；将来改提示词必须同改该 golden。
- **无上限后**：一次调用按文件数线性做 `stat` + 全量 sha256；大列表的成本由模型上下文与文件总字节决定，刻意不加防御性上限（owner 裁决 2026-11-2x）。
- **新增第四种项目类型时**：`state.NormalizeProjectType`、`runner.go:1218` 与 `presentFilesCopyFor` 各加一处，否则新类型静默拿到 coding 措辞（`…DefaultsToCoding` 仍会通过，评审要主动查）。
- **工具行为与文字分离**：handler 不按类型分支——描述只教"交什么"，不教"怎么交"。
- **reviewer 重点**：① 三份描述与三类提示词逐字对读（含点名优先句）；② 收尾句三份字节相同；③ batch 的 `.work/` 排除与 office 的"用户明确索要"例外没有误写进 coding；④ `minItems=1` 仍在、`maxItems` 确已消失；⑤ 结果句只有 batch 变了。

## 10. 附：本计划不解决的问题

- batch 结果句保留 "download and preview card"：这句话对模型的作用是"交付已落地、别再重复"，而 batch 会话的产物在 web 里同样以卡片呈现（`frontend/src/lib/artifacts.ts` 从不读 `note`，故对 UI 零影响）；只是把接收方从 user 换成 calling system。
- `registry.go:190` 的 event-meta 短句对新口径仍不够准，非模型可见，留作后续。
- batch 收尾句的 "inside the project directory" 与 prompt 的 "task workspace" 是同一对象的两种叫法（`artifacts.go:247` 的报错同源），batch 领头句已用括号点明，未改共用收尾句。
- coding 的引擎补登（peer 分支 `97b4158e`）是否合入由 owner 裁决；本计划只保证文本在合入前后都不说谎。

## Implementation Status

- **状态**：DONE（APPROVE，2026-11-21；分支未合并，合并由 owner 决定）
- **Worktree / 分支**：`.worktrees/present-files-copy` / `present-files-copy`
- **提交**：`a77109ae`（初版，基线 `8e08d693`）→ rebase 后 `a207700f`（基线 `97b4158e`，单一提交）
- **改动**：8 文件 +301/−31 —— `pkg/tool/artifacts.go`（三描述 + 两结果句 + `presentFilesCopyFor`；`minItems=1` 去上限；包注释=peer 全文 + 选择器段）、`pkg/tool/registry.go`（两处注释）、`pkg/tool/artifacts_test.go`（表驱动 golden + 注册断言 + 6 个新测试）、三份新 golden、删旧 golden、`pkg/agent/coding_delivery.go`（仅头注释末句）。
- **偏差（均评审接受）**
  1. **执行期间 dev 前进**：peer 的 `97b4158e`（coding 轮末补登）在实施后 ff 合入主树。评审要求 rebase；冲突只在两处注释，按「peer 的每条规则保留、本计划只增量」解决：`artifacts.go` 顶部保留 batch + coding 两条 harness 规则，新增独立的「措辞按类型选择」段；`coding_delivery.go` 保留 engine-backed 头注释与 prompt 正文（含 `present exactly those` 与引擎补登句），只把 `The shared tool description stays frozen.` 改为 `The tool description is chosen per project type.`。
  2. **步骤顺序**：Step 1 之后先做 Step 2 第 1 项（golden + 表驱动重命名），让 Step 0.5 的复现测试能跑出绿态证据，再补齐其余新测试并删除临时测试。
  3. **`peer_send` 未由执行器发出**（其会话无 peer 对端）；与 peer 的协调由评审方完成。
  4. **环境**：worktree 缺 gitignored 构建依赖（`third_party/libtokenizers`、`third_party/pdfium`、`assets/fonts`、`pkg/gateway/dist`），由执行器安装/复制，均未进提交；`pkg/architecture` 首轮红（`TestWebBuildLoadsNoExternalAsset`）由此而来，补齐后绿。
- **评审方亲跑的验证（rebase 后 `a207700f`）**
  - `rg` 旧常量名 0 行；`^const presentFilesTool(Description|Note)` = 5；`maxItems` 0 行；三份 golden 就位、旧 golden 消失。
  - `go test -tags fts5 ./pkg/tool -count=1` → ok（16 个 `TestPresentFiles*` 全 PASS，含点名的 8 个）；`./pkg/agent`、`./pkg/architecture` → ok。
  - `gofmt -l cmd pkg third_party plugins` 无输出；`CGO_ENABLED=1 go vet ./...` exit 0。
  - 三份描述与计划文本**逐字相等**（脚本比对：coding/office/batch 全部 MATCH）；两句结果句逐字一致。
  - 真机（评审方独立重跑 coding 腿）：`FAKE_DUMP_DIR` 落盘的 `present_files` 描述与 `presentFilesToolDescriptionCoding` 逐字节相等（789 vs 789，sha256 一致）；执行器另跑的 office 腿同样逐字相等（743 vs 743），两腿 dump 的 `files` schema 均为 `{"minItems":1,…}`、无 `maxItems`。
  - scope：`git diff --stat 97b4158e..HEAD` 只有 8 个 in-scope 文件；`pkg/turn`、`pkg/gateway`、`frontend` 零改动。
- **未做 / 遗留**：batch 腿 TUI 不可达，按计划由单测覆盖（不假装跑过）；`registry.go:190` 的 event-meta 短句与「project directory vs task workspace」两处按 §10 刻意留白；分支未 push/未合并。
