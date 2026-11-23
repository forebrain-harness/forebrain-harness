# Plan 144：技能工坊的版本对比显示行级 diff（统一 diff 样式）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发 "STOP conditions" 时停下报告，不要自行变通。
> **不要更新 `plans/README.md`**（评审方维护）。**不要 commit、不要 push**（owner 手动提交）。
>
> **前置（否则 STOP 1）**：计划 137 的改动已在当前工作区（主树 dev，未提交亦可）——下面四条都要有输出：
> ```bash
> test -f frontend/src/components/chat/DiffBlock.vue && echo ok-diffblock
> test -f frontend/src/lib/unifiedDiff.ts && echo ok-parser
> grep -n "export function parseUnifiedDiff" frontend/src/lib/unifiedDiff.ts
> grep -n "DiffBlock" frontend/src/components/workbench/ChangedFilesTab.vue
> ```
> 137 的文件此刻**未提交**：若你在 git worktree 里执行（基于 dev HEAD），这些文件不存在 → STOP 1，报告“需在主树 dev 工作区执行或等 owner 提交 137”。
>
> **Drift check（先跑）**：`git diff --stat 82ab168 -- pkg/skill/versions.go pkg/skill/service.go pkg/gateway/skill_versions.go`
> 应**无输出**（引擎/网关这三个文件在规划时是干净的）。有输出 → 对照下面“Current state”的摘录逐段核对，对不上 → STOP 2。
> 前端文件（`api.ts`、`WorkshopView.vue`、`locales/index.ts`、`skill-workshop.spec.ts`）规划时已带 135–138 的未提交改动，**按符号定位**，不按行号。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: LOW–MED（引擎只加不改：新方法 + 新字段，现有 `Diff` 输出逐字不变）
- **Depends on**: 137（`DiffBlock`、`parseUnifiedDiff`）
- **Category**: direction（补齐 137 的 STOP 3 搁置项）
- **Planned at**: dev `82ab168` + 工作区里 135–138 的未提交改动，2026-10-26

## Why this matters

owner 2026-10-25 定稿“diff 全站统一”：shell 卡改动文件、已编辑 / 已创建卡、审批预览、回合 diff、工作台“变更文件”、**技能工坊的对比**都用同一种 diff
（左侧 3px 色条 + 浅底色、一列很淡的行号、新建文件按普通代码显示）。原型 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 的入口
「版本对比」（`cmpModal()`，约 :6178）画的就是逐行增删。计划 137 执行时停在 STOP 3：工坊对比的数据只有每个文件的
`{path, status, added, removed}` 行数计数（`pkg/skill/versions.go` 的 `FileDiff`），**没有行级文本**，前端无从画 diff——现在弹窗右栏只写“+3 行 / −2 行”。
137 不许动引擎与网关，所以单立本计划：引擎给出单个文件的统一 diff 文本，网关透出，前端弹窗换成 137 的 `DiffBlock`。

## Current state

### 引擎 `pkg/skill/versions.go`（规划时干净）

- `:121-129` 文件级对比行：
  ```go
  // FileDiff is one file's place in a version-to-version comparison. Added and
  // Removed are line counts; a file whose pair is missing counts every line it
  // has.
  type FileDiff struct {
  	Path    string `json:"path"`
  	Status  string `json:"status"` // added | removed | modified
  	Added   int    `json:"added"`
  	Removed int    `json:"removed"`
  }
  ```
- `:540-582` `func (v VersionStore) Diff(name string, from, to string) ([]FileDiff, error)`：`snapshotDir` 解析 `"v3"` / `"draft"` →
  `readTreeLines` 读两侧整棵树（`map[相对路径(斜杠分隔)][]行]`）→ 对路径并集逐个判断：只在旧侧 = `removed`（`Removed: len(oldLines)`）、只在新侧 = `added`、
  两侧都有则 `lineDiffCounts`，0/0 跳过，否则 `modified`；按路径排序。核心片段：
  ```go
  	for path := range paths {
  		oldLines, hadOld := before[path]
  		newLines, hadNew := after[path]
  		switch {
  		case hadOld && !hadNew:
  			out = append(out, FileDiff{Path: path, Status: "removed", Removed: len(oldLines)})
  		case !hadOld && hadNew:
  			out = append(out, FileDiff{Path: path, Status: "added", Added: len(newLines)})
  		default:
  			added, removed := lineDiffCounts(oldLines, newLines)
  			if added == 0 && removed == 0 {
  				continue
  			}
  			out = append(out, FileDiff{Path: path, Status: "modified", Added: added, Removed: removed})
  		}
  	}
  ```
- `:984-1024` `readTreeLines(root)`：跳过点开头的目录与文件、跳过符号链接与非普通文件；键是 `filepath.ToSlash(rel)`。
- `:1027-1033` `splitLines(raw)`：`strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")`（行里**不含** `\n`）。
- `:1035-1039` `const lineDiffLimit = 20000`；`:1043` `lineDiffCounts` 是 LCS 计数，`len(old)*len(new) > lineDiffLimit*lineDiffLimit/64` 时退化为整文件计数。
- `:818-836` 同文件里哨兵错误的写法（照抄风格）：
  ```go
  var (
  	// ErrVersionStoreReadOnly is the refusal a write into .versions meets: ...
  	ErrVersionStoreReadOnly = errors.New("version records are read-only: ...")
  	...
  	ErrDraftNotPublished = errors.New("a draft has no published version to enable: publish it first")
  )
  ```
- 文件 import 现为：标准库一组 + `"github.com/forebrain-harness/forebrain-harness/pkg/home"` 一组。

### 服务 `pkg/skill/service.go:918-926`

```go
// DiffSkill compares two snapshots of one skill — published versions by
// number ("v3") and the draft ("draft").
func (s *Service) DiffSkill(name, from, to string) ([]FileDiff, error) {
	store, _, ok := s.resolveVersionedSkill(name)
	if !ok {
		return nil, fmt.Errorf("skill %q not found", strings.TrimSpace(name))
	}
	return store.Diff(strings.TrimSpace(name), from, to)
}
```
`DiffSkill` 的唯一调用方是网关（`grep -rn "DiffSkill(" pkg` 只有 `pkg/gateway/skill_versions.go:239`）；TUI 没有技能对比视图。

### 网关 `pkg/gateway/skill_versions.go:211-250`（规划时干净）

`handleSkillDiffWith(launch, w, r)`：取 `from`、`to`（缺一个 → 400），`svc.DiffSkill` → 错误一律 400 → 返回
`{"name","from","to","files":[FileDiff…]}`。主代理路由 `skills.Get("/:name/diff", s.handleSkillDiff)`（`api_extra.go:148`）与项目路由
`projects.Get("/:id/skills/:name/diff", s.handleProjectSkillDiff)`（`api_extra.go:297`）都走它。同文件 `writeSkillDraftNotPublished`（约 :296）是
`errors.Is(err, skill.ErrDraftNotPublished)` 映射状态码的写法样例。文件已 import `errors`、`skill`。

### 前端

- `frontend/src/lib/api.ts`（符号 `export interface SkillFileDiff`，约 :1549；`skillDiff(` 约 :2793）：
  ```ts
  /** One file's line-level diff between two snapshots. */
  export interface SkillFileDiff {
    path: string
    status: 'added' | 'removed' | 'modified'
    added: number
    removed: number
  }
  ...
    skillDiff(name: string, from: string, to: string, projectId?: string) {
      const base = projectId ? `/v1/projects/${encodeURIComponent(projectId)}/skills` : '/skills'
      const query = new URLSearchParams({ from, to })
      return api.get<SkillDiffResponse>(`${base}/${encodeURIComponent(name)}/diff?${query.toString()}`).then((res) => res.data)
    },
  ```
  响应经共享拦截器 **深度 camelCase 改键**（`api.ts` 约 :1907 `response.data = toCamelCase(response.data)`）：网关的 `unified_diff` / `too_large`
  到前端是 `unifiedDiff` / `tooLarge`。只改键，不改值（文件路径是值，不受影响）。
- `frontend/src/components/workshop/CompareDialog.vue`（93 行）：左栏文件按钮（路径 + `+N −M`），右栏只写
  `t('workshop.diffAdded', { n })` / `t('workshop.diffRemoved', { n })`；脚本里有 `error` ref 但**模板从没渲染它**（列表请求失败时弹窗一片空白——本计划顺手修）。
  文件头注释明说“the diff route reports line counts per file, not the lines themselves”。
- `frontend/src/components/workshop/ChangesTab.vue:13`：每个改动文件一行，`v-if="base"` 时有“对比”按钮，
  `@click="emit('compare', { from: \`v${base}\`, to: 'draft' })"`——**不带文件**，打开后总是选中第一个文件。
- `frontend/src/views/WorkshopView.vue`（符号 `openCompare`、`<CompareDialog`）：
  ```ts
  const compareOpen = ref(false)
  const compareFrom = ref('')
  const compareTo = ref('')
  function openCompare(payload: { from: string; to: string }) {
    compareFrom.value = payload.from
    compareTo.value = payload.to
    compareOpen.value = true
  }
  ```
  ```vue
      <CompareDialog
        :open="compareOpen"
        :skill-name="activeSkill"
        :project-id="projectId"
        :from="compareFrom"
        :to="compareTo"
        @cancel="compareOpen = false"
      />
  ```
  `ChangesTab`（`@compare="openCompare"`）与 `VersionsTab`（`@compare="openCompare"`）都发 compare。
- 137 落地的组件（**照用，不改**）：`frontend/src/components/chat/DiffBlock.vue`（props `rows: DiffRow[]`、`plain?: boolean`、
  `maxHeight?: number | null`，null = 不限高、由外层滚动；根元素 `data-testid="diff-block"`，行 `.dr` + `.add` / `.del` / `.gap`，增删行带 sr-only
  “新增 / 删除”）；`frontend/src/lib/unifiedDiff.ts` 的 `parseUnifiedDiff(text): DiffRow[]`（丢弃第一个 `@@` 之前的头行，`@@ -a,b +c,d @@` 起算行号，
  两个 hunk 之间一行 `…`）。**照抄的用法样例**：`frontend/src/components/workbench/ChangedFilesTab.vue` 的对比弹窗——
  `<DiffBlock v-else :rows="diffRows" :max-height="null" class="chg-diff-block" />`，`diffRows = computed(() => (diffText.value ? parseUnifiedDiff(diffText.value) : []))`，
  以及它 `load()` 里用 `let generation = 0` 丢弃过期响应的写法。
- 原型「版本对比」的样式（`app-preview.html` :1319-1324，**1:1 照抄**，token 对应：`--line`→`--forebrain-divider`、`--subtle`→`--forebrain-bg-alt`、
  `--brand-soft`→`--forebrain-brand-soft`、`--brand`→`--forebrain-brand-1`、`--muted`→`--forebrain-muted-text`、`--text-2`→`--forebrain-text-2`、`--mono`→`--forebrain-mono-font`）：
  ```css
  .cmp { display: grid; grid-template-columns: 200px 1fr; gap: 0; border: 1px solid var(--line); border-radius: 8px; overflow: hidden; height: 300px; }
  .cmp .files { border-right: 1px solid var(--line); background: var(--subtle); padding: 4px; display: grid; gap: 1px; align-content: start; overflow: auto; }
  .cmp .files button { border: 0; background: transparent; text-align: left; padding: 6px 8px; border-radius: 6px; font-family: var(--mono); font-size: 11.5px; cursor: pointer; color: var(--text-2); display: flex; justify-content: space-between; gap: 6px; }
  .cmp .files button.on { background: var(--brand-soft); color: var(--brand); }
  .cmp .files button span { color: var(--muted); }
  .cmp .diff { border: 0; border-radius: 0; height: 100%; box-sizing: border-box; }
  ```
  原型的“工坊 · 变更文件”（:6371）：每行“对比”按钮带上该文件（`data-vcmpf="<技能>:<base>:draft|<文件>"`，打开即选中该文件），**新增的文件不给“对比”**。

### 架构约束（`pkg/architecture/cache_test.go`，违反即测试红）

- `sameLayerEdges["skill"] = {"safety"}`：`pkg/skill` **不能** import `pkg/event`（那里虽有 `event.Build` 能出统一 diff，但会新增同层依赖边 → `TestSameLayerEdgesAreEnumerated` 红）。
  第三方库不计：`github.com/pmezard/go-difflib` 已是 `go.mod` 直接依赖，`pkg/skill` 可以直接用。
- `TestImportsAreGroupedStdlibFirst`：import 只能是“标准库一组 + 其余一组”。difflib 放进 `pkg/home` 那一组，按字母序排在它后面。
- `TestTestFilesCorrespondToProductionFiles`：`X_test.go` 必须有 `X.go`。本计划只往已存在的 `versions_test.go`、`skill_versions_test.go` 加测试，新建的前端测试与组件同名。

## Commands you will need

| 用途 | 命令 | 成功时 |
|---|---|---|
| 引擎定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/skill -run '^TestDiff' -count=1 -v` | 看到每个用例的 `--- PASS:` 行（**必须看到 PASS 行**；名字写错时 go 只报 `no tests to run` 且 exit 0） |
| 网关定向测试 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run '^TestSkill(VersionRoutes\|Diff)' -count=1 -v` | 同上 |
| 架构 + 回归 | `FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture ./pkg/skill ./pkg/gateway -count=1` | 全 `ok` |
| 编译 / vet / gofmt | `CGO_ENABLED=1 go build -tags fts5 ./...`；`CGO_ENABLED=1 go vet -tags fts5 ./pkg/skill ./pkg/gateway`；`gofmt -l pkg/skill pkg/gateway` | exit 0 / exit 0 / 无输出 |
| 前端单测 | `cd frontend && pnpm vitest run src/components/workshop` | 全部 passed |
| 前端类型 | `cd frontend && pnpm typecheck` | 0 错误 |
| 前端全量 | `cd frontend && pnpm vitest run` | 与开工前基线相比无新增失败 |
| e2e | `FOREBRAIN_E2E_WORK="$(mktemp -d)" FOREBRAIN_E2E_GW_PORT=8861 FOREBRAIN_E2E_LLM_PORT=8862 FOREBRAIN_E2E_PROXY_PORT=8863 FOREBRAIN_E2E_SPEC="e2e/skill-workshop.spec.ts" scripts/acceptance/web_e2e.sh` | 该 spec 全部 passed（私有目录与端口：同树别的会话跑同一脚本会 `pkill` + 清共享目录） |

开工前先跑一次“前端全量”与“架构 + 回归”记下基线（工作区带着 135–138 的改动，若已有失败，记下名字，之后只看新增）。

## Scope

**改**：
- `pkg/skill/versions.go`：`FileDiff` 加三个字段；抽出 `compareFile`；新增 `DiffFile`、`unifiedLineDiff`、`unifiedRange`、`linesLookBinary`、`tooLargeToDiff`、`ErrDiffFileNotFound`、常量 `maxDiffTextBytes`。
- `pkg/skill/versions_test.go`：新增测试。
- `pkg/skill/service.go`：新增 `DiffSkillFile`。
- `pkg/gateway/skill_versions.go`：`handleSkillDiffWith` 认 `path` 查询参数。
- `pkg/gateway/skill_versions_test.go`：新增测试。
- `frontend/src/lib/api.ts`：`SkillFileDiff` 加字段、新增 `skillDiffFile`。
- `frontend/src/components/workshop/CompareDialog.vue`（重写右栏 + 样式按原型 + 渲染 error + `initialPath`）、`CompareDialog.test.ts`（新建）。
- `frontend/src/components/workshop/ChangesTab.vue`（对比带文件、新增文件不给对比）、`ChangesTab.test.ts`（新建）。
- `frontend/src/views/WorkshopView.vue`（`openCompare` 透传 `path`）。
- `frontend/src/locales/index.ts`（中英各加 3 条、删 2 条）。
- `frontend/e2e/skill-workshop.spec.ts`（对比断言加行级 diff）。

**不改**（看着相关也别碰）：
- `lineDiffCounts`、`statBetween`、`Diff` 的输出——版本记录里的 `stat` 与列表计数保持原样；`Diff` 只做“把 switch 挪进 `compareFile`”的纯重构，输出逐字不变（现有 `TestDiffBetweenVersionsAndDraft` 守着）。
- `pkg/event`（不 import，见架构约束）、`pkg/tool`、TUI。
- `DiffBlock.vue`、`unifiedDiff.ts`（137 的产物，照用）；`PublishDialog.vue`（原型里发布弹窗只列文件与计数，不画 diff）；`VersionsTab.vue`（它的对比按钮不带文件，保持）。
- `ChangesTab` 里原型有、实现没有的“编辑 / 撤销变更”按钮——**不在本计划**，别顺手加。
- 不新增路由：单文件对比走同一条 `/diff`，多一个 `path` 参数。

## Steps

### Step 1：引擎——单个文件的行级 diff（`pkg/skill/versions.go`）

1.1 `FileDiff` 改为（注释写全，字段顺序如下）：
```go
// FileDiff is one file's place in a version-to-version comparison. Added and
// Removed are line counts; a file whose pair is missing counts every line it
// has. UnifiedDiff is filled only by DiffFile — the compare view lists files
// from Diff and asks for the selected one's lines — and stays empty when
// Binary or TooLarge says why it has none.
type FileDiff struct {
	Path        string `json:"path"`
	Status      string `json:"status"` // added | removed | modified; DiffFile also answers unchanged
	Added       int    `json:"added"`
	Removed     int    `json:"removed"`
	UnifiedDiff string `json:"unified_diff,omitempty"`
	Binary      bool   `json:"binary,omitempty"`
	TooLarge    bool   `json:"too_large,omitempty"`
}
```
三个新字段都 `omitempty`，所以 `Diff` 列表的 JSON 与原来逐字一致。

1.2 把 `Diff` 循环里的 switch 抽成（放在 `Diff` 正下方）：
```go
// compareFile is one path's row in a comparison. Diff lists these and
// DiffFile answers one, so the counts the list shows and the counts an open
// file shows are the same numbers by construction. changed is false when the
// path reads the same on both sides.
func compareFile(path string, oldLines []string, hadOld bool, newLines []string, hadNew bool) (row FileDiff, changed bool) {
	switch {
	case hadOld && !hadNew:
		return FileDiff{Path: path, Status: "removed", Removed: len(oldLines)}, true
	case !hadOld && hadNew:
		return FileDiff{Path: path, Status: "added", Added: len(newLines)}, true
	}
	added, removed := lineDiffCounts(oldLines, newLines)
	if added == 0 && removed == 0 {
		return FileDiff{}, false
	}
	return FileDiff{Path: path, Status: "modified", Added: added, Removed: removed}, true
}
```
`Diff` 的循环体改为：
```go
	for path := range paths {
		oldLines, hadOld := before[path]
		newLines, hadNew := after[path]
		if row, changed := compareFile(path, oldLines, hadOld, newLines, hadNew); changed {
			out = append(out, row)
		}
	}
```
（路径来自两侧并集，不会出现两侧都没有的情况。）

1.3 在 `ErrDraftNotPublished` 所在的 `var (...)` 块末尾加：
```go
	// ErrDiffFileNotFound is the answer when a compare asks for a file that
	// neither snapshot holds — a path the listing never offered, or one that
	// a dot file, a symlink or a path outside the skill can only ever be.
	ErrDiffFileNotFound = errors.New("the file is in neither snapshot")
```

1.4 在 `compareFile` 下方新增 `DiffFile`：
```go
// DiffFile is Diff narrowed to one file, with that file's unified diff. path
// is a key of the same tree walk Diff compares — slash-separated, relative to
// the skill — and is only ever looked up there, never joined onto the
// filesystem, so a dot file, a symlink or a path that climbs out of the skill
// is simply absent. A path neither side holds is ErrDiffFileNotFound; a path
// that reads the same on both sides answers status "unchanged".
func (v VersionStore) DiffFile(name, from, to, path string) (FileDiff, error) {
	fromDir, err := v.snapshotDir(name, from)
	if err != nil {
		return FileDiff{}, err
	}
	toDir, err := v.snapshotDir(name, to)
	if err != nil {
		return FileDiff{}, err
	}
	before, err := readTreeLines(fromDir)
	if err != nil {
		return FileDiff{}, err
	}
	after, err := readTreeLines(toDir)
	if err != nil {
		return FileDiff{}, err
	}
	path = strings.TrimSpace(path)
	oldLines, hadOld := before[path]
	newLines, hadNew := after[path]
	if !hadOld && !hadNew {
		return FileDiff{}, fmt.Errorf("%w: %q", ErrDiffFileNotFound, path)
	}
	row, changed := compareFile(path, oldLines, hadOld, newLines, hadNew)
	if !changed {
		return FileDiff{Path: path, Status: "unchanged"}, nil
	}
	switch {
	case linesLookBinary(oldLines) || linesLookBinary(newLines):
		row.Binary = true
	case tooLargeToDiff(oldLines, newLines):
		row.TooLarge = true
	default:
		row.UnifiedDiff = unifiedLineDiff(path, oldLines, newLines)
	}
	return row, nil
}
```

1.5 在 `lineDiffCounts` 附近（`equalLines` 之后）新增：
```go
// maxDiffTextBytes bounds the text a compare draws: a side larger than this
// answers TooLarge rather than shipping megabytes of rows to a dialog.
const maxDiffTextBytes = 2 << 20

// linesLookBinary is git's own test for a binary file — a NUL byte — applied
// to the lines the tree walk already read.
func linesLookBinary(lines []string) bool {
	for _, line := range lines {
		if strings.IndexByte(line, 0) >= 0 {
			return true
		}
	}
	return false
}

// tooLargeToDiff reports whether a pair is past what a compare draws: the
// same table bound past which lineDiffCounts stops counting exactly, a side
// longer than lineDiffLimit lines (an added or removed file has no table to
// bound), or a side larger than maxDiffTextBytes.
func tooLargeToDiff(oldLines, newLines []string) bool {
	if len(oldLines) > lineDiffLimit || len(newLines) > lineDiffLimit {
		return true
	}
	if len(oldLines)*len(newLines) > lineDiffLimit*lineDiffLimit/64 {
		return true
	}
	return linesBytes(oldLines) > maxDiffTextBytes || linesBytes(newLines) > maxDiffTextBytes
}

func linesBytes(lines []string) int {
	n := 0
	for _, line := range lines {
		n += len(line) + 1
	}
	return n
}

// unifiedLineDiff writes the unified diff of one file with three lines of
// context, the shape the web's parseUnifiedDiff reads. The matcher runs with
// autojunk off: with it on, a file of 200 lines or more would treat its
// blank lines as junk and draw needlessly large hunks. Each line is written
// with its own newline, so a file's final newline never shows up as a
// phantom empty row.
func unifiedLineDiff(path string, oldLines, newLines []string) string {
	groups := difflib.NewMatcherWithJunk(oldLines, newLines, false, nil).GetGroupedOpCodes(3)
	if len(groups) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("--- a/" + path + "\n+++ b/" + path + "\n")
	for _, group := range groups {
		first, last := group[0], group[len(group)-1]
		fmt.Fprintf(&b, "@@ -%s +%s @@\n", unifiedRange(first.I1, last.I2), unifiedRange(first.J1, last.J2))
		for _, op := range group {
			if op.Tag == 'e' {
				for _, line := range oldLines[op.I1:op.I2] {
					b.WriteString(" " + line + "\n")
				}
				continue
			}
			if op.Tag == 'r' || op.Tag == 'd' {
				for _, line := range oldLines[op.I1:op.I2] {
					b.WriteString("-" + line + "\n")
				}
			}
			if op.Tag == 'r' || op.Tag == 'i' {
				for _, line := range newLines[op.J1:op.J2] {
					b.WriteString("+" + line + "\n")
				}
			}
		}
	}
	return b.String()
}

// unifiedRange is a hunk header's "start,length" (difflib keeps its own
// spelling unexported): one line is just its number, and an empty range
// names the line before it.
func unifiedRange(start, stop int) string {
	beginning := start + 1
	length := stop - start
	if length == 1 {
		return fmt.Sprintf("%d", beginning)
	}
	if length == 0 {
		beginning--
	}
	return fmt.Sprintf("%d,%d", beginning, length)
}
```
import 块改为（第二组按字母序）：
```go
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/pmezard/go-difflib/difflib"
```

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/skill` → exit 0；`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/skill -run '^TestDiffBetweenVersionsAndDraft$' -count=1 -v`
→ `--- PASS: TestDiffBetweenVersionsAndDraft`（证明重构没改 `Diff` 的输出）。

### Step 2：引擎测试（`pkg/skill/versions_test.go`，放在 `TestDiffBetweenVersionsAndDraft` 之后）

新增 `TestDiffFileCarriesTheLineDiff`，夹具**照抄** `TestDiffBetweenVersionsAndDraft` 的搭法（`writeVersionedSkill(t, root, "gateway-ops", "line one\nline two\nline three")`、
`old.txt = "kept\nremoved\n"`、Bootstrap、`EnsureDraft` 后写 SKILL.md / `new.txt`、删 `old.txt`），另外在草稿里再写：
`same.txt`（在 Bootstrap **之前**写进 `dir`，内容 `"same\n"`；草稿由 `EnsureDraft` 复制得到同样内容，不用再写）、`blob.bin`（草稿新增，内容 `"a\x00b\n"`）、
`big.txt`（草稿新增，`strings.Repeat("x\n", 20001)`）、`.hidden`（草稿新增，`"h\n"`）。逐条断言（规划时已用同一算法实算过，下面的文本是逐字期望值）：

| 调用 `store.DiffFile("gateway-ops", "v1", "draft", path)` | 期望 |
|---|---|
| `"SKILL.md"` | `Status == "modified"`、`Added == 3`、`Removed == 2`（与 `Diff` 列表同数），`UnifiedDiff` **逐字等于**下方文本 A |
| `"new.txt"` | `Status == "added"`、`Added == 1`、`UnifiedDiff == "--- a/new.txt\n+++ b/new.txt\n@@ -0,0 +1 @@\n+brand new\n"` |
| `"old.txt"` | `Status == "removed"`、`Removed == 2`、`UnifiedDiff == "--- a/old.txt\n+++ b/old.txt\n@@ -1,2 +0,0 @@\n-kept\n-removed\n"` |
| `"same.txt"` | `err == nil`、`Status == "unchanged"`、`UnifiedDiff == ""` |
| `"blob.bin"` | `Status == "added"`、`Binary == true`、`UnifiedDiff == ""` |
| `"big.txt"` | `Status == "added"`、`TooLarge == true`、`UnifiedDiff == ""` |
| `".hidden"`、`"../gateway-ops/SKILL.md"`、`"nope.md"` | `errors.Is(err, ErrDiffFileNotFound)` |
| `DiffFile("gateway-ops", "v9", "draft", "SKILL.md")` | `err != nil` 且**不是** `ErrDiffFileNotFound` |

文本 A（Go 字符串字面量）：
```go
"--- a/SKILL.md\n+++ b/SKILL.md\n@@ -1,8 +1,9 @@\n ---\n name: gateway-ops\n-description: test skill gateway-ops\n+description: d\n ---\n \n line one\n-line two\n+line two changed\n line three\n+line four\n"
```
再新增 `TestUnifiedLineDiffSplitsDistantChangesIntoHunks`：旧 = `l1`…`l20`，新 = 同样 20 行但第 2 行改 `L2`、第 18 行改 `L18`；
`unifiedLineDiff("f", old, new)` 逐字等于：
```go
"--- a/f\n+++ b/f\n@@ -1,5 +1,5 @@\n l1\n-l2\n+L2\n l3\n l4\n l5\n@@ -15,6 +15,6 @@\n l15\n l16\n l17\n-l18\n+L18\n l19\n l20\n"
```
（两个 hunk——前端会在中间画一行 `…`。）两个新测试都用自己的 `t.TempDir()`；**不要改** `TestDiffBetweenVersionsAndDraft`。
`versions_test.go` 是 `package skill`（内部测试），可以直接调未导出的 `unifiedLineDiff`。

**Verify**：`FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/skill -run '^Test(DiffFileCarriesTheLineDiff|UnifiedLineDiffSplitsDistantChangesIntoHunks|DiffBetweenVersionsAndDraft)$' -count=1 -v`
→ 三条 `--- PASS:`。若文本 A 或两段 hunk 的实测值与上面不同 → **STOP 3**（不要改期望去迁就实现，回报实测 `%q`）。

### Step 3：服务与网关

3.1 `pkg/skill/service.go`，`DiffSkill` 正下方：
```go
// DiffSkillFile is DiffSkill for one file, with its line diff.
func (s *Service) DiffSkillFile(name, from, to, path string) (FileDiff, error) {
	store, _, ok := s.resolveVersionedSkill(name)
	if !ok {
		return FileDiff{}, fmt.Errorf("skill %q not found", strings.TrimSpace(name))
	}
	return store.DiffFile(strings.TrimSpace(name), from, to, path)
}
```

3.2 `pkg/gateway/skill_versions.go` `handleSkillDiffWith`：在 `svc, err := s.skillLifecycleService(launch)` 成功之后、`files, err := svc.DiffSkill(...)` 之前插入：
```go
	// One file, with its lines: the compare view lists files from the plain
	// answer below and asks for the selected one here.
	if path := strings.TrimSpace(q.Get("path")); path != "" {
		file, err := svc.DiffSkillFile(name, from, to, path)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, skill.ErrDiffFileNotFound) {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": name,
			"from": from,
			"to":   to,
			"file": file,
		})
		return
	}
```
同时把函数上方注释补一句：`// With path set, the answer is that one file with its unified diff.`
不加 `path` 时行为与响应**逐字不变**。

3.3 `pkg/gateway/skill_versions_test.go` 新增 `TestSkillDiffServesOneFileWithItsLines`（照抄 `TestSkillVersionRoutesServeStatePublishDiffDownload` 的
`newSkillVersionsServer` + `Bootstrap` + `EnsureDraft` 搭法）。**SKILL.md 内容逐字用下面两段，别照抄那个测试的 v1 内容**——它写的是 `"---\name: …"`
（少了一个 `n`，frontmatter 行两侧不同，会让下面的 +1/−1 断言变成 +2/−2）：
v1 = `"---\nname: gateway-ops\ndescription: runbook\n---\n\nv1 body\n"`，草稿 = `"---\nname: gateway-ops\ndescription: runbook\n---\n\nv2 body\n"`。断言：
① `GET …/diff?from=v1&to=draft&path=SKILL.md` → 200，JSON 里 `file.status == "modified"`、`file.unified_diff` 含 `"-v1 body\n+v2 body\n"`、`file.added == 1`、`file.removed == 1`；
② `…&path=nope.md` → 404；③ `…&path=../x` → 404；④ 不带 `path` 的同一请求 → 200 且响应体**不含** `unified_diff`（列表不带文本）。
`versionRouteReq` 的 params 只给 `{"name": "gateway-ops"}`（`path` 走查询串）。

**Verify**：网关定向测试命令 → `--- PASS: TestSkillDiffServesOneFileWithItsLines` 与 `--- PASS: TestSkillVersionRoutesServeStatePublishDiffDownload`；
然后“架构 + 回归”命令 → `pkg/architecture`、`pkg/skill`、`pkg/gateway` 全 `ok`（相对基线无新增 FAIL）；`go vet` 与 `gofmt -l` 干净。

### Step 4：前端数据（`frontend/src/lib/api.ts`）

`SkillFileDiff` 改为：
```ts
/** One file's place in a comparison of two snapshots; the one-file answer
 *  (skillDiffFile) also carries the file's unified diff. */
export interface SkillFileDiff {
  path: string
  status: 'added' | 'removed' | 'modified' | 'unchanged'
  added: number
  removed: number
  unifiedDiff?: string
  /** Why a one-file answer has no unifiedDiff. */
  binary?: boolean
  tooLarge?: boolean
}
```
`skillDiff` 下方新增（同一写法）：
```ts
  /** One file of a comparison, with its unified diff (the compare dialog's right pane). */
  skillDiffFile(name: string, from: string, to: string, path: string, projectId?: string) {
    const base = projectId ? `/v1/projects/${encodeURIComponent(projectId)}/skills` : '/skills'
    const query = new URLSearchParams({ from, to, path })
    return api
      .get<{ name: string; from: string; to: string; file: SkillFileDiff }>(`${base}/${encodeURIComponent(name)}/diff?${query.toString()}`)
      .then((res) => res.data.file)
  },
```
**Verify**：`cd frontend && pnpm typecheck` → 0 错误。

### Step 5：对比弹窗（`CompareDialog.vue`）按原型重做右栏

- 新 prop `initialPath?: string`（默认 `''`）。列表加载完：`selected = files 里有 initialPath ? initialPath : files[0]?.path ?? ''`。
- 选中文件变化（含首次）时调 `forebrainApi.skillDiffFile(skillName, from, to, path, projectId)`；照 `ChangedFilesTab.vue` 的 `let generation = 0` 写法，
  只采纳最后一次请求的结果（快速连点两个文件时，先发的慢响应不能覆盖后选的文件）。弹窗关闭再打开、`from`/`to`/`initialPath` 变化时全部重置
  （现有 `watch(() => [props.open, props.from, props.to], …)` 的源数组加上 `props.initialPath`）。
- 模板结构（`data-testid` 照写，e2e 与单测要用）：
  ```vue
  <ForebrainModal :open="open" size="xl" :title="…原样…" @close="emit('cancel')">
    <p v-if="error" class="cmp-hint cmp-hint--error" data-testid="workshop-compare-error">{{ error }}</p>
    <div class="cmp" data-testid="workshop-compare">
      <div class="files">
        <button v-for="file in files" :key="file.path" type="button" :class="{ on: file.path === selected }"
                :data-compare-file="file.path" @click="selected = file.path">
          <span class="p">{{ file.path }}</span><span class="n">{{ diffCounts(file) }}</span>
        </button>
        <p v-if="loaded && !files.length && !error" class="cmp-hint">{{ t('workshop.noChanges') }}</p>
      </div>
      <div class="pane" data-testid="workshop-compare-pane">
        <p v-if="fileLoading" class="cmp-hint">{{ t('common.loading') }}</p>
        <p v-else-if="fileError" class="cmp-hint cmp-hint--error">{{ fileError }}</p>
        <template v-else-if="shown">
          <p v-if="shown.binary" class="cmp-hint">{{ t('workshop.diffBinary') }}</p>
          <p v-else-if="shown.tooLarge" class="cmp-hint">{{ t('workshop.diffTooLarge') }}</p>
          <p v-else-if="!shown.unifiedDiff" class="cmp-hint">{{ t('workshop.diffUnchanged') }}</p>
          <DiffBlock v-else :rows="parseUnifiedDiff(shown.unifiedDiff)" :plain="shown.status === 'added'" :max-height="null" />
        </template>
      </div>
    </div>
    <template #footer>…原样（关闭按钮）…</template>
  </ForebrainModal>
  ```
  （`shown` = 当前选中文件的单文件响应；`loaded` 在列表请求完成后为 true，避免加载中闪“没有改动”。）
- 样式（`<style scoped>`，把原来的 Tailwind 类换成这些；值照原型 :1319-1324，token 见 Current state 对照）：
  ```css
  .cmp { display: grid; grid-template-columns: 200px minmax(0, 1fr); border: 1px solid var(--forebrain-divider); border-radius: 8px; overflow: hidden; height: 300px; }
  .files { border-right: 1px solid var(--forebrain-divider); background: var(--forebrain-bg-alt); padding: 4px; display: grid; gap: 1px; align-content: start; overflow: auto; }
  .files button { border: 0; background: transparent; text-align: left; padding: 6px 8px; border-radius: 6px; font-family: var(--forebrain-mono-font); font-size: 11.5px; cursor: pointer; color: var(--forebrain-text-2); display: flex; justify-content: space-between; gap: 6px; }
  .files button .p { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .files button .n { flex-shrink: 0; color: var(--forebrain-muted-text); }
  .files button.on { background: var(--forebrain-brand-soft); color: var(--forebrain-brand-1); }
  .pane { min-width: 0; height: 100%; overflow: auto; box-sizing: border-box; }
  /* The pane's own frame is the border (the prototype's .cmp .diff { border: 0 }). */
  .pane :deep(.fc-b) { border-top: 0; min-height: 100%; box-sizing: border-box; }
  .cmp-hint { padding: 8px 10px; font-size: 12px; color: var(--forebrain-muted-text); }
  .cmp-hint--error { color: var(--forebrain-danger); }
  ```
  `--forebrain-mono-font` 若在 `frontend/src/assets/main.css` 里不存在，照 `DiffBlock.vue` 用的同名变量写（它就是 `var(--forebrain-mono-font)`）。
- 文件头注释改写：去掉“reports line counts per file, not the lines themselves”，写明“左栏列表来自 `skillDiff`，右栏按选中文件取 `skillDiffFile`，
  用全站唯一的 diff 行组件 `DiffBlock` 画（owner 2026-10-25 diff 全站统一）；新建文件 `plain`”。
- import：`DiffBlock from '@/components/chat/DiffBlock.vue'`、`{ parseUnifiedDiff } from '@/lib/unifiedDiff'`。
- locales（`frontend/src/locales/index.ts`，中文块与英文块各自 `workshop.diffAdded` 所在位置）：**删** `workshop.diffAdded`、`workshop.diffRemoved`
  （删前 `grep -rn "workshop.diffAdded\|workshop.diffRemoved" frontend/src` 只应命中 CompareDialog 与 locales；有别处使用 → 保留不删）；**加**：
  | 键 | 中文 | English |
  |---|---|---|
  | `workshop.diffBinary` | `二进制文件，不显示内容` | `Binary file, not shown` |
  | `workshop.diffTooLarge` | `文件太大，不显示差异` | `Too large to show` |
  | `workshop.diffUnchanged` | `内容相同` | `No differences` |

**Verify**：新建 `frontend/src/components/workshop/CompareDialog.test.ts`（照 `PublishDialog.test.ts` 的写法：`setLocale('zh')`、`vi.spyOn(forebrainApi, …)`、
`InlineTeleport`、`flush()`），用例：
1. 列表 `[SKILL.md modified +3 −2, new.txt added +1]`，`skillDiffFile` 对 SKILL.md 返回带 Step 2 文本 A 的 `unifiedDiff` → 右栏有 `[data-testid="diff-block"]`、
   `.dr.add` 3 行、`.dr.del` 2 行；`skillDiffFile` 第一次调用的第 4 个参数是 `'SKILL.md'`。
2. 点 `[data-compare-file="new.txt"]`（返回 `status: 'added'` + `"@@ -0,0 +1 @@\n+brand new\n"`）→ `.dr.add` 为 0（`plain`），正文含 `brand new`。
3. `initialPath: 'new.txt'` 挂载 → 第一次 `skillDiffFile` 请求的就是 `new.txt`，左栏 `new.txt` 按钮有 `.on`。
4. `binary: true` → 右栏文字 `二进制文件，不显示内容`；`tooLarge: true` → `文件太大，不显示差异`；`status: 'unchanged'` → `内容相同`；三种都没有 `diff-block`。
5. 竞态：SKILL.md 的请求挂起（手动 resolve 的 Promise），先点 new.txt 并让它返回，再 resolve SKILL.md → 右栏仍是 new.txt 的内容。
6. `skillDiff` reject → `[data-testid="workshop-compare-error"]` 显示错误文字。
`cd frontend && pnpm vitest run src/components/workshop/CompareDialog.test.ts` → 6 passed；`pnpm typecheck` → 0。

### Step 6：入口带上文件（`ChangesTab.vue`、`WorkshopView.vue`）

- `ChangesTab.vue`：emit 类型改为 `compare: [payload: { from: string; to: string; path?: string }]`；对比按钮改为
  `v-if="base && file.status !== 'added'"`（原型：新增的文件不给对比）、加 `data-testid="workshop-change-compare"`、
  `@click="emit('compare', { from: \`v${base}\`, to: 'draft', path: file.path })"`。
- `WorkshopView.vue`：加 `const compareInitialPath = ref('')`；`openCompare(payload: { from: string; to: string; path?: string })` 里
  `compareInitialPath.value = payload.path ?? ''`；`<CompareDialog … :initial-path="compareInitialPath" …>`。`VersionsTab` 的两个 compare 不带 `path`，行为不变（选第一个文件）。

**Verify**：新建 `frontend/src/components/workshop/ChangesTab.test.ts`（mock `skillVersions` 返回 `{ state: { draftBase: 1, hasDraft: true, draftDir: '/d' } }`、
`skillDiff` 返回 `[SKILL.md modified, new.txt added]`）：① 只有 1 个 `[data-testid="workshop-change-compare"]`（SKILL.md 那行）；② 点它 →
`wrapper.emitted('compare')[0][0]` 深等于 `{ from: 'v1', to: 'draft', path: 'SKILL.md' }`。
`cd frontend && pnpm vitest run src/components/workshop` → 全部 passed；`pnpm typecheck` → 0；`pnpm vitest run` → 相对基线无新增失败。

### Step 7：e2e（`frontend/e2e/skill-workshop.spec.ts`）

在用例 `a draft shows unpublished changes and the versions tab compares` 里，现有的
`await expect(page.locator('[role="dialog"]')).toContainText('SKILL.md')` 之后、`shot(...)` 之前追加（该用例的草稿把
`description: e2e offline install fixture` 改成 `…, draft` 并在文末加了空行与 `more`；规划时实算的 hunk 是 `@@ -1,6 +1,8 @@`，1 删 3 增）：
```ts
    // The compare draws the selected file's lines in the one diff style
    // (plan 144): removed and added rows, not a line count.
    const pane = page.locator('[data-testid="workshop-compare-pane"]')
    await expect(pane.locator('[data-testid="diff-block"]')).toBeVisible()
    await expect(pane.locator('.dr.del')).toHaveCount(1)
    await expect(pane.locator('.dr.del')).toContainText('e2e offline install fixture')
    await expect(pane.locator('.dr.add')).toHaveCount(3)
    await expect(pane.locator('.dr.add').last()).toContainText('more')
```
**Verify**：e2e 命令（私有目录与端口）→ `skill-workshop.spec.ts` 全部 passed（需要 fixture 的用例在脚本里会拿到 `E2E_SKILL_ZIP`；若显示 skipped，确认是用脚本跑的）。
报告附一张对比弹窗截图（该用例的 `shot(page, 'workshop-draft')` 产物），与原型「版本对比」并排说明差异。

## Test plan（汇总）

- Go：`TestDiffFileCarriesTheLineDiff`、`TestUnifiedLineDiffSplitsDistantChangesIntoHunks`（新）+ `TestDiffBetweenVersionsAndDraft`（原样通过 = 重构无副作用）；
  `TestSkillDiffServesOneFileWithItsLines`（新）+ `TestSkillVersionRoutesServeStatePublishDiffDownload`（原样通过）；`pkg/architecture` 全绿（无新依赖边、import 分组正确）。
- 前端：`CompareDialog.test.ts`（6 例）、`ChangesTab.test.ts`（2 例）新建；全量 vitest 无新增失败。
- e2e：`skill-workshop.spec.ts` 的对比用例断言行级 diff。

## Done criteria

- [ ] `git diff --stat -- pkg/skill pkg/gateway` 只涉及 `versions.go`、`versions_test.go`、`service.go`、`skill_versions.go`、`skill_versions_test.go`
- [ ] 上面列出的 Go 新旧测试都有 `--- PASS:` 行；`go test ./pkg/architecture ./pkg/skill ./pkg/gateway` 全 `ok`；`go vet`、`gofmt -l` 干净
- [ ] `grep -n "pkg/event" pkg/skill/*.go` 无输出（没有引入同层依赖）
- [ ] `grep -rn "workshop.diffAdded\|workshop.diffRemoved" frontend/src` 无输出
- [ ] `grep -n "line counts per file, not the lines" frontend/src/components/workshop/CompareDialog.vue` 无输出
- [ ] `pnpm typecheck` 0；`pnpm vitest run` 相对基线无新增失败；`skill-workshop.spec.ts` 全部 passed
- [ ] 报告附对比弹窗截图与原型对照

## STOP conditions

1. 前置不满足（137 的 `DiffBlock` / `parseUnifiedDiff` 不在工作区）。
2. Drift check 有输出且摘录对不上（`Diff` / `FileDiff` / `handleSkillDiffWith` 已被别人改过）。
3. Step 2 的逐字期望文本与实测不同——回报实测的 `%q`，不要改期望。
4. `pkg/architecture` 因本计划变红且原因不是“漏了 import 分组”（例如 fan-out 预算或同层边）——回报测试输出，不要改 `cache_test.go`。
5. `TestDiffBetweenVersionsAndDraft` 在 Step 1 重构后变红——说明重构改了 `Diff` 的输出，回退重构并回报。
6. 某一步验证连续两次修正后仍失败。

## Maintenance notes

- 计数与文本来自两套算法：列表与版本记录的 `+N −M` 是 `lineDiffCounts`（LCS，最小），右栏的行来自 difflib（autojunk 关）。两者在常见编辑上一致
  （本计划的夹具全部一致），极少数重复行很多的文件里 difflib 可能多画一两行增删。若 owner 将来要求“行数与画出来的行严格一致”，
  正确做法是把 `lineDiffCounts` 也改为从同一组 opcodes 计数（会改变今后发布记录的 `stat`），不要在前端改数。
- `DiffFile` 只按 `readTreeLines` 的键查找，从不把请求里的 `path` 拼成文件系统路径——以后若改成直接读单个文件以省 IO，必须补上
  `home.ValidateArchiveRelPath` + `home.ResolveWithinRoots`（照 `ReadVersionFile`）并保留“点文件 / 符号链接视为不存在”的语义。
- `maxDiffTextBytes`、`lineDiffLimit` 改动时同步 `TestDiffFileCarriesTheLineDiff` 里 `big.txt` 的行数。
- 二进制文件在**列表**里的计数仍是按“行”切出来的近似值（存量行为，本计划未动）。
- 工坊“变更文件”页相对原型还缺“编辑 / 撤销变更”两个按钮（存量差异，不在本计划）。
