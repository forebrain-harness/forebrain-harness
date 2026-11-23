# Plan 367: memories_* 工具的路径锚点歧义修复——首次调用即成功

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**:
> `git diff --stat 51add5d7..HEAD -- pkg/tool/session_tools.go pkg/tool/request_permissions_test.go pkg/memory/instruction.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition. 行号漂移不算漂移，以内容为准。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（改动贴着跨项目隔离不变量；测试闸门必须全绿，见 Step 4/5）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `51add5d7`, 2026-11-23
- **Executed**: `APPROVE`（2026-11-23，执行器 deepseek-v4.1-flash@xhigh，worktree
  `.worktrees/memories-path-anchor`／分支同名，从 `80bb73b4` 起，**改动未提交**，3 文件
  +282/−19）。评审亲跑：两包 `ok`（`pkg/tool` 17.1s／`pkg/memory` 5.1s）、六个新测试 PASS、
  `vet` 0、`gofmt` 净、两处 grep 归零、既有隔离守卫与 ad-hoc 兜底守卫原样绿；RED 由评审用
  `-overlay` 换回基线 `session_tools.go` 独立取得（5 个新测试按缺陷原样变红）。一轮 REVISE：
  Step 4 测试 4 的 list/search 断言由"成功才查泄漏"改为无条件要求报错。执行器申报偏离：
  `make libtokenizers pdfium pdfium-fonts` 因网络卡死未完成，改为从主检出只读复制同版本
  `third_party/`（gitignore，不进 diff）。
- **Landed**: `dc93b1ac`（dev，`git merge --ff-only` from `80bb73b4`，2026-11-23；提交信息
  `fix(memory): anchor tool paths and refuse traversal`，与 369 同一提交）。落地后门禁亲跑：
  `go build -tags fts5 ./...` ok、`pkg/memory` 11.4s／`pkg/tool` 25.2s／`pkg/migrate` 3.9s／
  `pkg/run` 50.3s／`pkg/turn` 5.5s 全 ok、`vet` 0、`gofmt` 净，`git diff` 对五个 in-scope 文件为空
  （landed == tested）。

## Why this matters（owner 报障 + 硬要求）

owner 截图：本项目会话里 `memories_search` 反复报错
`tool memories_search: path 'projects/-Users-doudou-workspace-unionj-cloud-forebrain-harness' was not found`。
模型把**项目 scope 目录名**当成了工具路径传进来——而工具的根**已经是**那个目录，
于是每次这样的调用都失败，记忆检索在会话开头就空转。

**owner 硬要求：必须提高首次调用成功率。** 因此本计划三管齐下：
1. **源头预防**：工具 schema 里 "relative to the memories root" 的措辞是歧义根源（见 §Current state 因果链），改成明确锚定"本项目记忆根"，让模型第一次就传对路径；
2. **调用内兜底**：即使模型仍传 `projects/<本项目key>/…`，同一次工具调用内部自动归一化并重试，**首次调用照样成功**（复用既有 `retryInAdHocNotes` 模式，它已为同类问题——裸笔记文件名——解决过一次）；
3. **失败可自愈**：兜底也救不了的错误，报错信息里直接告诉模型正确锚点，下一次调用能自我纠正。

## Current state（全部为 advisor 亲读核实，基线 `51add5d7`）

### 因果链（每一环都有证据）

1. **物理布局**：会话记忆根 = `<agent workspace>/memories/projects/<ProjectKey>`。
   `pkg/memory/scope.go:81-86`：
   ```go
   func (s Scope) dir() string {
       if s.Kind == ScopeProject {
           return "projects/" + s.Key
       }
       return "global"
   }
   ```
   `pkg/memory/scope.go:149`：`MemoryRoot: filepath.Join(r.workspaceRoot, MemoryDirName, filepath.FromSlash(scope.dir()))`。
   本机实例：`/Users/doudou/.forebrain/workspace/memories/projects/-Users-doudou-workspace-unionj-cloud-forebrain-harness`（已确认存在，内有 MEMORY.md、rollout_summaries/ 等）。
2. **工具后端根 = 项目 scope 根**：`pkg/tool/session_tools.go:1145-1167` `memoryBackend` 用
   `roots.Scope(memory.Scope{Kind: memory.ScopeProject, Key: runtime.ProjectKey}).MemoryRoot` 建
   `memory.NewScoped(projectRoot, globalRoot)`。`Backend.resolve`（`pkg/memory/backend.go:218-273`）
   把传入 path 逐段拼在 **Root（项目 scope 根）** 之下。
3. **报错点**：`pkg/memory/backend.go:482-485`（search；list/read 同构，:321-324 / :398-401）：
   ```go
   info, err := os.Lstat(start)
   if errors.Is(err, fs.ErrNotExist) {
       return SearchResponse{}, notFoundError(stringValue(request.Path))
   }
   ```
   `notFoundError`（:186）= `path '%s' was not found`，`ErrNotFound`（:184）可被 `errors.Is` 识别。
   传入 `projects/<key>` 时，Lstat 的是 `<scope根>/projects/<key>` —— 不存在，报错。
4. **模型为什么传这个路径（歧义源头）**：
   - 工具 schema 三处都说 **"relative to the memories root"**（`pkg/tool/session_tools.go:873/879/886`，原文见下）。而指令里展示给模型的 base path 是绝对路径 `…/memories/projects/<key>`（`pkg/memory/templates/read_path.md:9`：`Project memory at {{ base_path }}`）。模型把 "memories root" 锚定到绝对路径里的 `memories/` 组件，就产出 `projects/<key>/…`。search 的 Path 描述还写着 **"Omit to search all memory"**，进一步强化"根是多项目总库"的错误心智模型。
   - **排除竞争解释**：本项目真实记忆库里 `MEMORY.md` 与 `memory_summary.md` 对字符串
     `projects/-Users-doudou-workspace-unionj-cloud-forebrain-harness` 的 grep 计数均为 **0**
     （advisor 实跑）——模型不是从记忆内容里抄的路径，锚点只能来自指令/schema 措辞。
5. **既有兜底只覆盖裸文件名**：`pkg/tool/session_tools.go:1020-1054`：
   ```go
   func retryInAdHocNotes[T any](path string, response T, err error, call func(string) (T, error)) (string, T, error) {
       if !errors.Is(err, memory.ErrNotFound) {
           return path, response, err
       }
       for _, candidate := range adHocNoteFallbackPaths(path) {
           retried, retryErr := call(candidate)
           if retryErr == nil {
               return candidate, retried, nil
           }
       }
       return path, response, err
   }
   ```
   `adHocNoteFallbackPath`（:1048-1054）只对**不含斜杠的 `.md` 裸文件名**生成候选。
   `projects/<key>` 含斜杠 → 无候选 → 原样失败。三个调用点：list :941、read :969、search :994。
6. **报错无自愈信息**：返回给模型的只有 `path '…' was not found`，不含正确锚点，模型无从纠正，于是反复失败。

### 待改原文（逐字，供 drift 比对）

`pkg/tool/session_tools.go:872-887`：
```go
type memoriesListInput struct {
	Path       *string `json:"path,omitempty" jsonschema_description:"Directory or file path relative to the memories root, e.g. extensions/ad_hoc/notes for the ad-hoc notes. Omit to list the root."`
	...
}

type memoriesReadInput struct {
	Path       string `json:"path" jsonschema_description:"File path relative to the memories root, e.g. extensions/ad_hoc/notes/<filename>.md for an ad-hoc note."`
	...
}

type memoriesSearchInput struct {
	Queries []string `json:"queries" ...`
	Path    *string  `json:"path,omitempty" jsonschema_description:"Directory or file path relative to the memories root to restrict the search to, e.g. extensions/ad_hoc/notes for the ad-hoc notes. Omit to search all memory."`
}
```

`pkg/tool/session_tools.go:346`（`memoriesAddAdHocNoteDescription` 末句）：
```go
... It is stored under " + memory.AdHocNotesDir + "/ in the chosen scope; pass the returned path, relative to the memories root, verbatim to memories_read."
```

`pkg/tool/session_tools.go:987-1001`（search handler 的兜底段；list/read 同构）：
```go
searchAt := func(path *string) (memory.SearchResponse, error) {
    return backend.Search(memory.SearchRequest{Queries: input.Queries, Path: path, TopK: topK})
}
response, err := searchAt(input.Path)
scope := memoryToolOptionalScope(input.Path, "all")
if input.Path != nil {
    var path string
    path, response, err = retryInAdHocNotes(*input.Path, response, err, func(candidate string) (memory.SearchResponse, error) {
        return searchAt(&candidate)
    })
    scope = memoryToolScope(path)
}
```

`pkg/memory/instruction.go:67-69`（`readPathToolGuidance`，锚定正确但缺反例）：
```go
const readPathToolGuidance = `Reading memory with the memories_* tools: ` + "`memories_read`" + `, ` + "`memories_list`" + ` and ` + "`memories_search`" + ` take a path relative to the
  project memory base path above (` + "`MEMORY.md`" + `, ` + "`rollout_summaries/<file>.md`" + `, ` + "`skills/<skill-name>/SKILL.md`" + `); prefix ` + "`global/`" + ` for the global scope. An absolute path inside either base path also resolves.
`
```
**注意**：`pkg/memory/instruction_test.go:76` 钉住了子串 `"relative to the\n  project memory base path above"`（含换行与两空格缩进）。改这段时**只能追加句子，不得重排这一行**。

### Reproduction（advisor 亲跑，先于任何改动，HEAD `80bb73b4`）

不改仓库即可复现：探针文件放在仓库外，用 `-overlay` 注入 `pkg/tool`（同包，直接复用
`memoryToolHandlers`／`testMemoryScopeRoot`／`memoryFiller`）。

```bash
export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"
CGO_ENABLED=1 go test -tags fts5 -overlay=/tmp/fb367-repro/overlay.json ./pkg/tool/ -run TestZZRepro367ScopePrefix -count=1 -v
```

overlay.json 内容：`{"Replace": {"/<abs>/pkg/tool/zz_repro367_test.go": "/tmp/fb367-repro/zz_repro367_test.go"}}`。

实测输出（把截图里的真实 key 换成本会话的 `testMemoryProjectKey`），三个工具全部原样失败：

```
SEARCH path="projects/-Test-project"            err=path 'projects/-Test-project' was not found
SEARCH path="memories/projects/-Test-project"   err=path 'memories/projects/-Test-project' was not found
READ   path="projects/-Test-project/MEMORY.md"  err=path 'projects/-Test-project/MEMORY.md' was not found
READ   path="memories/projects/-Test-project/MEMORY.md" err=path '…/MEMORY.md' was not found
LIST   err=path 'projects/-Test-project' was not found
HINT-VISIBLE err=path 'no/such-file.md' was not found      ← 报错不含任何锚点提示
```

即：因果链最后一环（§Current state 6"报错无自愈信息"）与截图一致，兜底确实不覆盖该形态。

**执行器先看 RED**：Step 4 的 6 个新测试就是本复现的 in-repo 形态——先只写测试、跑一遍、
确认按上面的报错 FAIL（其中 1/2/3/5 条各对应一个失败），再做 Step 1–3。

补充核实（advisor）：`pkg/memory/control.go`、`scope.go`、`pipeline.go` 里的 "memories root"
是内部注释与 CLI 报错，不面向模型；面向模型的 4 处正是 Step 1 要改的。`raw_memories.md` 与
个别 `rollout_summaries/*.md` 里确实出现该字符串，但它们不在注入面（注入的只有
`MEMORY.md` + `memory_summary.md`，两者 grep 计数均为 0），§Current state 的排除结论不变。

### 必须遵守的仓库不变量与约定

- **兜底改路径不绕过任何权限判定**：`pkg/safety/engine.go:432-434` 把 `memories_list`／`memories_read`／`memories_search` 按**工具名**整条免批（理由见 :426-431 注释），判定不看 `path` 实参；因此"先判定、后在工具内部归一化路径"不产生 check-then-use 错配。真正的 confinement 仍在 `Backend.resolve`（backend.go:218-273：拒 `..`、拒 dotfile、拒 symlink、绝对路径必须落在 root 内），兜底只是把候选路径交给同一套检查。
- **跨项目隔离是结构性的**（`pkg/memory/scope.go:26-32` 注释原文："every place that used to operate on 'the agent's memories' now operates on one Scope at a time, which is what makes cross-project leakage structurally impossible rather than a matter of prompt discipline"）。兜底**只允许剥掉本会话自己的 ProjectKey 前缀**；其他项目的 key 一律不生成候选、原样失败。守卫测试 `TestMemoryToolsCannotReachAnotherProjectsMemory`（`pkg/tool/request_permissions_test.go:1276-1327`）必须原样通过，**不得修改该测试**。（reviewer 已核：该测试的四条敌意路径——绝对路径、`../-Other-project/…`、`../../projects/-Other-project/…`、`global/../../-Other-project/…`——都不以 `projects/-Test-project` 或 `memories/projects/-Test-project` 开头，新兜底对它们不生成任何候选；即便构造 `projects/<本会话key>/../../<别的key>/…`，剥前缀后剩下的 `../..` 也会被 `resolve` :248-250 拒掉。）
- 兜底只对 `errors.Is(err, memory.ErrNotFound)` 触发；symlink/非 UTF-8/I/O 拒绝绝不重试（既有注释 :1017-1019 说明了理由：把刻意拒绝变成静默的错误成功）。
- 注释风格：本仓库注释解释 WHY（见 `retryInAdHocNotes` 与 `adHocNoteFallbackPaths` 的注释），新代码照此办理，简短。
- 测试样板：`memoryToolHandlers`（`pkg/tool/request_permissions_test.go:1060-1081`）建全套工具 handler；`testMemoryProjectKey = "-Test-project"`（:1028）；`testMemoryScopeRoot`（:1030-1037）；search 类测试的语料播种样板＝`TestMemorySearchReturnsTheConfiguredTopK`（:4687-4700）：30 行 `- deploy note N` + `strings.Repeat(memoryFiller, 200)`（`memoryFiller` :1056-1058，BM25 需要稀有词对照，否则全被相关性地板滤掉）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 前置（仅当 go test 报 pdfium/libtokenizers 缺失） | `make libtokenizers pdfium pdfium-fonts` | exit 0 |
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./pkg/tool/ ./pkg/memory/` | exit 0 |
| 定向测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ ./pkg/memory/ -count=1` | 两包 `ok` |
| 单测过滤 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'TestMemories|TestMemory|TestAdHocNote' -count=1 -v` | 全部 PASS |
| Vet | `go vet ./pkg/tool/ ./pkg/memory/` | exit 0 |
| 格式 | `gofmt -l pkg/tool pkg/memory` | 输出为空 |

（来自 Makefile:101-104 `test: libtokenizers pdfium pdfium-fonts` + `CGO_ENABLED=1 go test -tags fts5 ./... -count=1`；本计划只跑受影响两包。）

## Scope

**In scope**（只许改这些）:
- `pkg/tool/session_tools.go`
- `pkg/tool/request_permissions_test.go`（追加测试；既有测试一律不改）
- `pkg/memory/instruction.go`（仅 `readPathToolGuidance` 追加一句）

**Out of scope**（看着相关也不许碰）:
- `pkg/memory/backend.go` —— `ErrNotFound`/`notFoundError`/`resolve` 被 `pkg/migrate` 共用，报错措辞改动放在工具层，不动后端。
- `pkg/memory/scope.go`、`pkg/memory/templates/read_path.md` —— 布局与模板不动。
- `pkg/tool/request_permissions_test.go` 里**既有**测试，尤其 `TestMemoryToolsCannotReachAnotherProjectsMemory`、`TestAdHocNoteFallbackOnlyRetriesMissingPaths` —— 它们是隔离与"只重试缺失"的守卫，改它们等于拆闸门。
- `pkg/memory/instruction_test.go` —— 只允许追加句子使既有断言继续成立，不改测试。
- `pkg/migrate/**` —— 也用 `memory.NewScoped`，但不面向模型，不受本歧义影响。

## Git workflow

- 按仓库现状在 dev 工作区直接改（近期执行批次均如此，owner 自行提交）；**不要 commit/push**，除非操作者明确指示。
- 若被要求提交：conventional 风格，如 `fix(memory): anchor memories_* paths at the project scope root`。

## Steps

### Step 1: 消除 schema 措辞歧义（源头预防）

改 `pkg/tool/session_tools.go` 四处描述字符串：

1. :873 `memoriesListInput.Path` →
   `"Directory or file path relative to this project's memory root (the base path shown in the memory instructions), e.g. extensions/ad_hoc/notes for the ad-hoc notes. Never include a memories/ or projects/<key>/ prefix. Omit to list the root."`
2. :879 `memoriesReadInput.Path` →
   `"File path relative to this project's memory root (the base path shown in the memory instructions), e.g. extensions/ad_hoc/notes/<filename>.md for an ad-hoc note. Never include a memories/ or projects/<key>/ prefix."`
3. :886 `memoriesSearchInput.Path` →
   `"Directory or file path relative to this project's memory root to restrict the search to, e.g. extensions/ad_hoc/notes for the ad-hoc notes. Never include a memories/ or projects/<key>/ prefix. Omit to search this project's memory; prefix global/ to search the user's cross-project preferences."`
4. :346 `memoriesAddAdHocNoteDescription` 末句 `pass the returned path, relative to the memories root, verbatim to memories_read.` →
   `pass the returned path verbatim to memories_read.`（返回路径本就根相对，删掉歧义短语即可。）
   **不会踩既有测试**：`TestAdHocNoteDescriptionSaysToReadBackByReturnedPath`（:1004-1010）钉住三个子串——`memory.AdHocNotesDir + "/"`、`"pass the returned path"`、`"verbatim to memories_read"`；reviewer 逐字核过，删掉 `, relative to the memories root` 之后三者**全部仍在**（前者在 `It is stored under …` 那半句，后两者在新句里连读成立），该测试不必改。

再改 `pkg/memory/instruction.go` `readPathToolGuidance`：在 `An absolute path inside either base path also resolves.` 之后**追加**一句：
` Paths never include the memories/ or projects/<key>/ components of the base path.`
（保持 :76 测试钉住的换行与缩进原样。）

**Verify**:
- `grep -n "relative to the memories root\|search all memory" pkg/tool/session_tools.go` → 无匹配
- `CGO_ENABLED=1 go build -tags fts5 ./pkg/tool/ ./pkg/memory/` → exit 0
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/memory/ -count=1` → ok（instruction_test 措辞断言仍绿）

### Step 2: 本会话 scope 前缀的调用内兜底（首次调用仍成功）

在 `pkg/tool/session_tools.go` 的 `adHocNoteFallbackPath` 之后新增：

```go
// scopePrefixFallbackPaths maps a path the model anchored at the memories
// base (projects/<key>/… or memories/projects/<key>/…) back onto this
// session's own project root. Only the session's own key is stripped: another
// project's key must keep failing, or the per-project isolation becomes a
// matter of prompt discipline again.
func scopePrefixFallbackPaths(path, projectKey string) []string {
	if projectKey == "" {
		return nil
	}
	trimmed := strings.TrimPrefix(strings.TrimSpace(path), "./")
	var candidates []string
	for _, prefix := range []string{"projects/" + projectKey, "memories/projects/" + projectKey} {
		if trimmed == prefix {
			candidates = append(candidates, "")
		} else if rest, ok := strings.CutPrefix(trimmed, prefix+"/"); ok {
			candidates = append(candidates, rest)
		}
	}
	return candidates
}
```

把 `retryInAdHocNotes` 泛化为 `retryMemoryPath`：签名加 `projectKey string`（放在 `path` 之后），候选循环改为先 `adHocNoteFallbackPaths(path)` 后 `scopePrefixFallbackPaths(path, projectKey)`（例如 `for _, candidate := range append(adHocNoteFallbackPaths(path), scopePrefixFallbackPaths(path, projectKey)...)`；两个函数各自返回全新切片或 nil，此 append 无别名问题）。更新其 doc comment：不再只提 ad-hoc notes，说明两类锚点错误（裸笔记文件名、本会话 scope 前缀）都只在 `ErrNotFound` 时重试。三个调用点（list :941、read :969、search :994）传入 `runtime.ProjectKey`（`memoryBackend` 已保证此处 `runtime` 非 nil——:1146 `if runtime == nil || strings.TrimSpace(runtime.StateRoot()) == ""` 直接返回错误；在 `newDedicatedMemoryTools` 顶部 `backend, err := memoryBackend(runtime)` 成功后取 `projectKey := strings.TrimSpace(runtime.ProjectKey)` 备用）。

三处调用点**逐字**改写如下（只加 `projectKey` 一个实参，其余不动）：

```go
// list（:941，在 if input.Path != nil 块内）
path, response, err = retryMemoryPath(*input.Path, projectKey, response, err, listAt)
// read（:969，无条件调用，注意是 := 而不是 =）
path, response, err := retryMemoryPath(input.Path, projectKey, response, err, readAt)
// search（:994，在 if input.Path != nil 块内）
path, response, err = retryMemoryPath(*input.Path, projectKey, response, err, func(candidate string) (memory.SearchResponse, error) {
	return searchAt(&candidate)
})
```

**不要把三处"对齐"**：read 的兜底是**无条件**的（`input.Path` 是必填 string），list/search 的兜底在 `if input.Path != nil` 内（`Path` 是 `*string`，nil 意为"根/全库"，本就不该重试）。这个不对称是既有的、正确的；改成一致会引入行为变化。

**空 ProjectKey 必须保持 fail-closed**：`runtime.ProjectKey` 为空时（gateway/channel 无启动目录，见 `memoryBackend` 的 doc comment :1140-1144），`scopePrefixFallbackPaths` 返回 nil，`NewScoped("", globalRoot)` 的 primary root 为空、`resolve` 报 `memory root is empty`（backend.go:219-221，**不是** `ErrNotFound`，故兜底根本不触发）。既有测试 `request_permissions_test.go:1429`／`:1478` 正是构造无 ProjectKey 的 runtime，它们必须原样通过。

语义核对（不要改后端）：
- 候选 `""` 经 `Backend.resolve`（backend.go:226-228）落到根——list/search 的"根本身"语义，正确。
- search 的 retry 闭包 `searchAt(&candidate)` 对 `""` 同样成立。
- 重试成功时 response 回显实际使用的路径（backend 的 `response.Path = original` 语义），与既有 ad-hoc 兜底一致；telemetry `memoryToolScope(path)` 也随之报真实 scope 而非 `"other"`。

**Verify**:
- `CGO_ENABLED=1 go build -tags fts5 ./pkg/tool/` → exit 0
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'TestAdHocNote|TestMemoryTools' -count=1` → 全部 PASS（既有兜底与隔离守卫不动仍绿）

### Step 3: not-found 报错携带锚点提示（失败可自愈）

在 `pkg/tool/session_tools.go` 新增：

```go
// memoryNotFoundHint tells the model the correct anchor when a memory path
// missed, so one failure is enough to correct the next call instead of
// repeating the same wrong anchor.
func memoryNotFoundHint(err error) error {
	if !errors.Is(err, memory.ErrNotFound) {
		return err
	}
	return fmt.Errorf("%w; memory paths are relative to this project's memory root (e.g. MEMORY.md, rollout_summaries/<file>.md), never prefixed with memories/ or projects/<key>/; prefix global/ for the global scope", err)
}
```

在 list/read/search 三个 handler 的 `return response, err` 前把 err 过一遍 `memoryNotFoundHint`（只包这三个工具的返回；`memories_add_ad_hoc_note` 不收路径，不动）。用 `%w` 包装保持 `errors.Is(err, memory.ErrNotFound)` 成立。

三个 handler 各自只有**一个** `return response, err` 出口（list :945、read :971、search :1001）；前面的 `max_results`／`line_offset` 校验早退是 `fmt.Errorf` 且非 `ErrNotFound`，不必也不要包。

**提示确实会到模型眼前**（这是本步骤的全部价值，reviewer 已核实链路）：工具错误经 `pkg/run/orchestration_llm.go:2318` `fmt.Errorf("tool %s: %w", name, err)` 进 tool result，正是 owner 截图里 `tool memories_search: path '…' was not found` 那个前缀的来源；包装后模型看到的是 `tool memories_search: path 'projects/<key>' was not found; memory paths are relative to …`。

**Verify**: `CGO_ENABLED=1 go build -tags fts5 ./pkg/tool/` → exit 0；`CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'TestAdHocNoteIsReadableAfterWrite' -count=1` → PASS（它断言 miss 报错仍含模型原路径 `'missing-note.md'`，包装不破坏）。

### Step 4: 新测试（追加到 pkg/tool/request_permissions_test.go 既有 memory 测试区，:1327 之后）

全部用 `memoryToolHandlers(t, home)` + `testMemoryScopeRoot(t, home)` 样板；search 语料按 `TestMemorySearchReturnsTheConfiguredTopK` 播种——**:4690 的 `os.MkdirAll(root.MemoryRoot, 0o755)` 必须先跑**，否则 :4698 的 `os.WriteFile` 直接失败；正文＝30 行 `- deploy note N` + `strings.Repeat(memoryFiller, 200)`（:4693-4697）。

**handler 调用形状（照抄，别自己发明）**：`memoryToolHandlers`（:1060-1081）返回的是 `map[string]func(context.Context, string) (any, error)`——第二个实参是 **JSON 字符串**（Go 里用反引号原始字符串），返回值要类型断言。搜索语料播种后先建库再取 handler，顺序照 :4688-4700 → :1088。

```go
home := t.TempDir()
root := testMemoryScopeRoot(t, home)
if err := os.MkdirAll(root.MemoryRoot, 0o755); err != nil {
	t.Fatal(err)
}
// …播种 MEMORY.md…
handlers := memoryToolHandlers(t, home)
result, err := handlers["memories_search"](context.Background(), `{"queries":["deploy"],"path":"projects/`+testMemoryProjectKey+`"}`)
if err != nil {
	t.Fatalf("first call must succeed: %v", err)
}
response, ok := result.(memory.SearchResponse)
if !ok {
	t.Fatalf("response = %#v", result)
}
```

其余五个测试同形：`memories_read` 断言 `memory.ReadResponse`、`memories_list` 断言 `memory.ListResponse`。`testMemoryProjectKey` 是包级常量（:1028 `"-Test-project"`），**用它拼接，不要写字面量**，与 `memoryToolHandlers` 里 runtime 的 `ProjectKey` 保持同源。

1. `TestMemoriesSearchAcceptsOwnScopePrefix` —— 截图报障的回归：播种后
   `handlers["memories_search"](ctx, {"queries":["deploy"],"path":"projects/-Test-project"})`
   **首次调用即成功**，`len(Matches) > 0`；同法验证 `"memories/projects/-Test-project"` 与
   `"projects/-Test-project/MEMORY.md"` 两个变体。
2. `TestMemoriesReadAcceptsOwnScopePrefix` —— 写 `MEMORY.md`（内容 `alpha\n`），
   `memories_read` 传 `"projects/-Test-project/MEMORY.md"` 与 `"memories/projects/-Test-project/MEMORY.md"` 均读到 `alpha\n`，且 `response.Path` 回显归一化后的路径（`MEMORY.md`）。
3. `TestMemoriesListAcceptsOwnScopePrefix` —— `memories_list` 传 `"projects/-Test-project"` 返回含 `MEMORY.md` 的条目。
4. `TestMemoriesScopePrefixFallbackRefusesOtherProject` —— 按 :1282-1295 样板建本项目与 `-Other-project` 两个库（后者内容 `OTHER PROJECT SECRET\n`）；对 read/list/search 传 `"projects/-Other-project"`、`"projects/-Other-project/MEMORY.md"`、`"memories/projects/-Other-project/MEMORY.md"`：**全部报错**，且任何成功响应/匹配内容都不含 `OTHER PROJECT SECRET`。
5. `TestMemoriesNotFoundCarriesAnchorHint` —— `memories_read` 传 **`"no/such-file.md"`**（含斜杠 ⇒ `adHocNoteFallbackPath` :1050 的 `strings.ContainsAny(trimmed, "/\\")` 直接判否，无 ad-hoc 候选；又不以本会话 scope 前缀开头 ⇒ 无 scope 候选；两类兜底都不触发，报错原样上抛后被 `memoryNotFoundHint` 包装）：断言报错含 `relative to this project's memory root`、含模型原路径 `'no/such-file.md'`，且 `errors.Is(err, memory.ErrNotFound)` 为 true。
   （不要用 `"no-such-file.md"`：它是**裸 `.md` 文件名**，会先触发 ad-hoc 兜底生成两个候选、双双 miss 之后才走到包装——测试同样会绿，但覆盖的是另一条路径，与本测试的意图不符。裸名那条已由既有 `TestAdHocNoteIsReadableAfterWrite` :1123-1129 覆盖。）
6. `TestMemoriesPathDescriptionsAnchorAtProjectRoot` —— 三处路径描述活在 **struct tag** 里，不是包级常量，所以不能像 `TestAdHocNoteDescriptionSaysToReadBackByReturnedPath`（:1004-1010，它断言的是包级常量 `memoriesAddAdHocNoteDescription`）那样直接断言字符串。**不要为此把描述提成常量**（无谓扩大 diff）。用已有的 schema 访问器：
   - `llm.Tool.InputSchema() map[string]any`（`pkg/llm/tool.go:71`）返回反射生成、真正发给模型的 schema；`jsonschema_description` 在其中。
   - `strictMemoryArguments`（`session_tools.go:1112-1131`）是**校验中间件**（`Use` 只 append middleware，`pkg/llm/tool.go:148-151`），**不替换 schema**，故描述不会被它吃掉。
   - `memoryToolHandlers` 只保留 `tool.Handle`、丢掉了 `*llm.Tool`，所以本测试**不能**用它；直接调 `newDedicatedMemoryTools`，runtime 照 :1429 的形状 `memorySearchRuntime(t, &AgentToolRuntime{Home: home, WorkspaceRoot: home, Cfg: &appcfg.Root{}})`（本测试不查库，ProjectKey 可省）。
   - 断言：对 `memories_list`／`memories_read`／`memories_search` 三个 tool，`json.Marshal(tool.InputSchema())` 的结果含 `this project's memory root`、**不含** `relative to the memories root`；`memories_search` 的还含 `Omit to search this project's memory`、不含 `search all memory`。顺带断言 `tools` 里 `memories_add_ad_hoc_note` 的 `Description()`（`pkg/llm/tool.go:66`）不含 `relative to the memories root`。
   - 结构样板：`TestIntermediateToolSchemaHasContentField`（:1012-1025）——它就是 `json.Marshal(schema)` + `strings.Contains` 的正反双向断言写法。

**Verify**: `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ -run 'TestMemoriesSearchAcceptsOwnScopePrefix|TestMemoriesReadAcceptsOwnScopePrefix|TestMemoriesListAcceptsOwnScopePrefix|TestMemoriesScopePrefixFallbackRefusesOtherProject|TestMemoriesNotFoundCarriesAnchorHint|TestMemoriesPathDescriptionsAnchorAtProjectRoot' -count=1 -v` → 6 个全 PASS。

### Step 5: 全量回归（受影响两包）

**Verify**:
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ ./pkg/memory/ -count=1` → 两包 ok
- `go vet ./pkg/tool/ ./pkg/memory/` → exit 0
- `gofmt -l pkg/tool pkg/memory` → 空
- `git status --porcelain -- pkg/tool pkg/memory plans` → 只有 Scope 内三个文件（+ plans/ 下本计划与索引，若由你更新）
- **闸门必须带路径限定**（`-- pkg/tool pkg/memory plans`），不要用裸 `git status --porcelain`：工作区里**先于本计划**就存在与本计划无关的未跟踪文件（reviewer 于 HEAD `80bb73b4` 实测到 `pkg/tui/zz_repro_test.go`、`pkg/tui/zz_repro2_test.go`，owner 的临时 repro，时间戳晚于基线 `51add5d7`）。裸闸门会因它们恒红，而**它们不是你的改动、也不许你删/改/stash**——那是 owner 在做的事。看到 scope 外的既有脏文件：忽略，继续，别"顺手清理"。

## Test plan

见 Step 4（6 个新测试，全部列名）。结构样板：`TestAdHocNoteIsReadableAfterWrite`（:1086）与 `TestMemorySearchReturnsTheConfiguredTopK`（:4687）。既有守卫必须原样通过：`TestMemoryToolsCannotReachAnotherProjectsMemory`、`TestAdHocNoteFallbackOnlyRetriesMissingPaths`、`TestMemoryToolsAcceptAbsolutePathsInsideTheRoot`、`pkg/memory` 的 instruction 措辞断言。

## Done criteria（全部机器可检）

- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tool/ ./pkg/memory/ -count=1` 两包 ok
- [ ] Step 4 的 6 个新测试存在且 PASS
- [ ] `grep -n "relative to the memories root\|search all memory" pkg/tool/session_tools.go` 无匹配
- [ ] `grep -rn "retryInAdHocNotes" pkg/tool/` 无匹配（已泛化为 retryMemoryPath）。**必须带 `-r`**：`grep -n <pat> pkg/tool/` 对目录会打印 `grep: pkg/tool/: Is a directory` 并 **exit 2**——那不是"无匹配"，是命令写错了（reviewer 实跑确认）
- [ ] `go vet ./pkg/tool/ ./pkg/memory/` exit 0；`gofmt -l pkg/tool pkg/memory` 为空
- [ ] `git status --porcelain -- pkg/tool pkg/memory plans` 只含 in-scope 文件（+ plans/）；scope 外既有的未跟踪文件（见 Step 5）不计入、也不许动
- [ ] `plans/README.md` 本计划状态行已更新

## STOP conditions

- Current state 摘录与活代码不符（drift）→ 停，报告差异。
- `TestMemoryToolsCannotReachAnotherProjectsMemory` 或任何既有守卫测试变红 → **停，绝不改测试迁就实现**；报告哪个候选路径越了界。
- `go test` 报 `Package pdfium was not found in the pkg-config search path` 或 libtokenizers 缺失 → 先跑 `make libtokenizers pdfium pdfium-fonts`；仍失败则停，报告环境缺什么。
- 候选 `""` 被后端拒绝（与 §Step 2 语义核对不符）→ 停，报告实际报错，不要改 backend.go。
- 发现需要改 out-of-scope 文件才能完成 → 停，报告。

## Maintenance notes

- 兜底只认**本会话自己的** ProjectKey。将来若 `NewScoped` 增加新的 mount（如多项目只读挂载），不要顺手把剥前缀推广到别的 key——隔离不变量优先（scope.go:26-32）。
- 遥测信号：`forebrain.memory.tool.call` 里 `operation=search, scope=other, status=failed` 的占比应显著下降；`scope=other` 的失败若仍高，说明出现了新的错误锚点形态，届时按同一家族（措辞＋调用内兜底＋报错提示）处理，而不是放宽隔离。
- 兜底是"缺失才重试"：若 consolidation agent 真在项目根下建了 `projects/<own-key>/` 目录，原路径会命中该目录而不触发归一化——这是刻意保守，与 ad-hoc 兜底同理。
- 明确不做（避免过度设计）：不剥 `global/projects/…`、不处理裸 `memories/` 前缀、不改后端报错文案——截图只观察到 `projects/<key>` 形态，其余等遥测出现再说。**同源但未被兜底覆盖的一个形态**：模型若把 global base path 也锚错，会传 `memories/global/MEMORY.md`（`resolve` 在项目根下拼 `<root>/memories` → 不存在 → `ErrNotFound`）。它由 Step 3 的报错提示自愈，不由 Step 2 归一化；这是刻意的，遥测里若该形态成气候再按同一家族处理。
- **已知平台缺口（Windows）**：`scopePrefixFallbackPaths` 只认 `/` 分隔（`projects/<key>`），而既有 `adHocNoteFallbackPath` :1050 对 `/` 和 `\` 都判。Windows 上模型若传 `projects\<key>` 则无候选、只能靠 Step 3 提示自愈。**不要顺手加 `\`→`/` 归一化**：`resolve` 自己会 `filepath.FromSlash`，在工具层再转一遍会与后端的分隔符语义分叉；真要修就单独立项、连 `memoryToolScope` :1078 一起考虑。
- `pkg/safety/engine.go:426-431` 那段免批理由注释**已经过时**（写着 "no absolute paths"，而 `resolve` :231-241 明确接受 root 内的绝对路径，且 `TestMemoryToolsAcceptAbsolutePathsInsideTheRoot` :1244 钉住了该行为）。这是先于本计划的缺陷、**不在本计划 scope**；记在这里是因为它和本计划改的是同一套心智模型——reviewer 别拿那段注释当依据来判兜底是否越权，也别在本计划里顺手改它。
- `docs/plan/MEMORY_NOTE_OVERCAPTURE_FIX_PLAN.md:173` 逐字引用了本计划要删的旧措辞（`pass the returned path, relative to the memories root, …`）。那是**历史修复记录**，不是活文档，不要"同步更新"它——改了等于篡改历史记录。
- **先于本计划的跨项目越权（2026-11-23 执行时实测发现，不在本计划 scope，报告不修）**：
  `pkg/memory/backend.go` `resolve` 在某个组件 `ErrNotExist` 时用 `filepath.Join` 拼接剩余组件
  并提前返回，而 `filepath.Join` 会归一化 `..`，于是绕过同一循环里的 `..` 拒绝：
  `memories_read` 传 `zzz/../../../projects/-Other-project/MEMORY.md` 会读回**别的项目**的
  `MEMORY.md`（`go test -overlay` 探针实测返回 `OTHER PROJECT SECRET`；把基线
  `session_tools.go` 换进来同样命中，故与本计划的兜底无关，本计划也未扩大它——剥前缀后的
  候选以 `..` 开头仍被循环拒掉）。这推翻 `pkg/memory/scope.go:26-32` 的"结构上不可能"表述；
  修法方向是提前返回前逐段拒 `..`，需单独立项并由 owner 排优先级。既有守卫
  `TestMemoryToolsCannotReachAnotherProjectsMemory` 的四条敌意路径都以 `..` 或绝对路径开头，
  故一直没覆盖这个"不存在的首段 + 足够多 `..`"形态。