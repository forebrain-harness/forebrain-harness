# Plan 001：skill 来源分层统一——一张层表同时驱动扫描与分类，Origin 词汇下沉引擎，修正遮蔽标注与内置优先级

> **执行者须知**：按步骤顺序执行。每一步都要运行对应的验证命令，确认结果符合预期后再进入下一步。一旦出现"STOP 条件"中的任何情况，立即停下来报告，不要自行发挥。完成后更新 `docs/plan/SKILLS_PANEL_TABS/README.md` 中本计划的状态行。**禁止 `git commit`**：所有改动留在工作区，由 owner 手动提交。
>
> **漂移检查（第一步先做）**：`git diff --stat 88eb320 -- pkg/skill pkg/gateway/skills_download.go pkg/gateway/skills_download_test.go pkg/tui/chat_slash.go pkg/tui/commands_test.go frontend/src/lib/api.ts`
> 制定计划时，工作区里已有与本计划无关、尚未提交的并行改动：`pkg/tui/chat_slash.go` 删除了 `HandleDiffSlash`，`pkg/tui/commands_test.go` 增加了 replay timeline 测试，`run.go` / `run_test.go` 删除了 `/diff`。另外还有吉祥物相关改动。**本计划引用的行号以制定时的工作区为准（已包含这些改动）**，所以上述命令输出非空是正常的。请逐一对照下文"现状"里的摘录和实际代码（按函数名定位）；只要有一处对不上，就按 STOP 条件处理。开始前先执行 `git diff > /tmp/skills-001-before.diff` 留底。

## 状态

- **优先级**：P1
- **工作量**：M
- **风险**：MED（改变了两处运行时行为，均已获 owner 批准，见"为什么要做"）
- **依赖**：无
- **类别**：bug + tech-debt
- **制定于**：commit `88eb320`，2026-10-07

## 为什么要做

skill 的"来自哪一层"目前由**五套各自独立的规则**判定：引擎 `SourceForPathWithWorkspace` 按路径前缀判断，无法识别时兜底为 `local`；引擎还有一套 trust 标签；网关 `skillOrigin`；TUI 分组名；TUI `skillSourceLabel`。这些规则都和扫描器实际遍历的根列表脱节。结果是：跨工具的用户目录（`~/.agents|.claude|.codex/skills`）被叫作误导人的 "local"；内置 skill 被归成 "global"；`~/.codex/skills/.system` 下 Codex 自带的内置 skill 被扫进来。

另外还有两个 bug，均已在 owner 的真实环境中实测证实：

1. **遮蔽标注写反**：同名 skill 有多份副本时，`ShadowedBy` 被写到了胜出的那份上，`Shadows` 被写到了落选的那份上。TUI 因此告诉用户"项目里的 improve 被遮蔽了"，而运行时实际加载的恰恰是项目那份。网关的下载、编辑、删除也会因此定位到落选的副本；Web 上"遮蔽 N 个同名技能"的徽标则永远不会显示。
2. **内置优先级错误**：扫描 `$FOREBRAIN_HOME/skills` 根时，扫描器会进入 `.system`（`.` 的字典序排在字母之前）。这导致内置的 `frontend-design` 压过了用户自装的 `~/.forebrain/skills/frontend-design`，违背了"内置优先级最低"的约定（`pkg/skill/roots.go:79` 的注释）。

本计划完成后：只有一张有序的"层表"，扫描按它的顺序遍历，分类直接取 skill 被找到时所在根的层；词汇统一为 Web 已在使用的五层（owner 2026-10-07 拍板），两端共用；遮蔽标注与运行时实际加载的结果一致。

## 现状

### owner 拍板的词汇与规则（必须逐字遵守）

| Origin 值（JSON / 常量） | 显示名 `Label()` | 目录 |
|---|---|---|
| `project` | `Project` | `<项目根>/.forebrain/skills`、`/.agents/skills`、`/.claude/skills`、`/.codex/skills` |
| `agent` | `Agent` | `<workspaceRoot>/skills`（主代理自己的 workspace，默认 `$FOREBRAIN_HOME/workspace/skills`） |
| `shared` | `Shared` | `$FOREBRAIN_HOME/skills` |
| `cross-tool` | `Cross-tool` | `~/.agents/skills`、`~/.claude/skills`、`~/.codex/skills`（`~` 为 `os.UserHomeDir()`） |
| `builtin` | `Built-in` | `$FOREBRAIN_HOME/skills/.system` |

优先级从高到低就是上表的行序。扫描规则：**扫描器永不进入任何点目录**；`.system` 只作为独立的最低优先级根被扫描。

### 相关文件

- `pkg/skill/roots.go`：项目、用户目录清单与运行时根列表 `agentSkillRoots`（行 62-83）。
- `pkg/skill/hub.go`：管理视图 `Hub`。`NewForWorkspace`（行 24-47）**手抄了一遍**根列表；`SkillDTO`（行 67-75）；`ListEnabledDTO`（行 85-103）；`trustLabelForPathWithWorkspace`（行 126-137）；`ManagedSkill`（行 154-163）；`ListManaged`（行 165-198）。
- `pkg/skill/skilltrust.go`：`Source` 类型和按路径前缀的分类器（全文件删除）。
- `pkg/skill/discover.go`：唯一的扫描器 `scanSkillRoots` / `scanSkillDir`。
- `pkg/skill/skill.go:27-29`：`systemSkillsDirName` 常量。
- `pkg/skill/system.go:28`：内置 skill 的安装目录。
- `pkg/skill/state.go`：`Entry`（行 11-19）、`DiscoverForWorkspace`（行 148-187）、`annotateShadows`（行 189-211）。
- `pkg/skill/skillmeta.go`：`regEntry`（行 12-20）及 `RefreshForWorkspace`（行 50-106）。其中 `Source`、`ShadowedBy`、`Shadows` 三个字段在生产代码中无人读取（`DistinctSkillNames` 只读 `Name`、`SkillDir`）。
- `pkg/skill/service.go`：`InspectResult`（行 93-102）、`Inspect`（行 159-180）、`inspectActionsForSource`（行 455-462）。
- `pkg/gateway/skills_download.go`：`skillOrigin`（行 26-49）、`skillListEntry`（行 78-87）、`decorateSkillList`（行 88-124）、`locateSkillDir`（行 138-161，被行 271、338、400、625 调用）、删除处理中按层校验（行 400-416）。
- `pkg/tui/chat_slash.go`：`SkillListString`（行 203-231）、`ApplySkillSelection` 的详情输出（行 306）、`skillInstallReport`（行 473-475）、`skillSourceLabel` 与 `hasPathPrefix`（行 524-558，只服务于 `SkillListString`）、`sortSkillEntries` / `skillSourceRank` / `skillEntryCategory`（行 1678-1719）、`handleSkillToggle` 拼标签（行 1845-1858）。
- `frontend/src/lib/api.ts:969-995`：`SkillOrigin`、`SkillRecord`、`SkillInspectResponse` 类型。

### 关键摘录

`pkg/skill/roots.go:62-83`（运行时根列表，这就是"层表"的雏形）：

```go
func agentSkillRoots(home string, workspaceRoot string, trustedProjectRoots []string) []string {
	h := strings.TrimSpace(home)
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	out := make([]string, 0, len(projectSkillDirs)+len(userSkillDirs)+3)
	// 1. Trusted project-level skills (highest priority).
	out = append(out, trustedProjectRoots...)
	// 2. Workspace skills
	if workspaceRoot != "" {
		out = append(out, filepath.Join(workspaceRoot, "skills"))
	}
	// 3. User-installed skills
	if h != "" {
		out = append(out, filepath.Join(h, "skills"))
	}
	// 4. Cross-tool user skill dirs
	out = append(out, UserSkillRoots()...)
	// 5. Built-in system skills (lowest priority)
	if h != "" {
		out = append(out, filepath.Join(h, "skills", ".system"))
	}
	return out
}
```

`pkg/skill/hub.go:24-47`（手抄的第二份根列表）：

```go
func NewForWorkspace(home string, workspaceRoot string, projectRoot string) *Hub {
	h := &Hub{
		Home:          strings.TrimSpace(home),
		WorkspaceRoot: strings.TrimSpace(workspaceRoot),
		ProjectRoot:   strings.TrimSpace(projectRoot),
	}
	// ...（注释略）
	for _, root := range ProjectSkillRootsForDir(h.ProjectRoot) {
		h.AddRoot(root)
	}
	if strings.TrimSpace(workspaceRoot) != "" {
		h.AddRoot(filepath.Join(strings.TrimSpace(workspaceRoot), "skills"))
	}
	h.AddRoot(filepath.Join(home, "skills"))
	for _, root := range UserSkillRoots() {
		h.AddRoot(root)
	}
	h.AddRoot(filepath.Join(home, "skills", ".system"))
	return h
}
```

`pkg/skill/skilltrust.go:26-44`（与扫描脱节的分类器，兜底为 local）：

```go
func SourceForPathWithWorkspace(home, workspaceRoot, projectRoot, abs string) Source {
	abs = filepath.Clean(strings.TrimSpace(abs))
	if abs == "" {
		return SourceLocal
	}
	...
	switch {
	case home != "" && hasPathPrefix(abs, filepath.Join(home, "skills")):
		return SourceGlobal        // ← .system 也落在这里
	case workspaceRoot != "" && hasPathPrefix(abs, filepath.Join(workspaceRoot, "skills")):
		return SourceWorkspace
	case withinProjectSkillRoots(abs, projectRoot):
		return SourceProject
	default:
		return SourceLocal         // ← 跨工具用户目录全部落在这里
	}
}
```

`pkg/skill/discover.go:73-80`（点目录例外，导致优先级 bug 和 Codex 内置被扫入）：

```go
	for _, item := range items {
		if !item.IsDir() {
			continue
		}
		leaf := strings.TrimSpace(item.Name())
		if leaf == "" || (strings.HasPrefix(leaf, ".") && !strings.EqualFold(leaf, systemSkillsDirName)) {
			continue
		}
```

同文件行 36-37 的规则注释：`//   - Only directories, and never a dot-directory — except ".system", which is` / `//     where the built-in skills are installed.`

`discoveredSkill` 的 `Root` 字段（`discover.go:19-22`）记录的是 `scanSkillRoots` 中 `skillAbs(root)` 的结果（失败时为 `filepath.Clean(root)`，且 root 先经过 `strings.TrimSpace`），见 `discover.go:50-59`：

```go
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, err := skillAbs(root)
		if err != nil {
			absRoot = filepath.Clean(root)
		}
		scanSkillDir(absRoot, absRoot, "", 0, &out)
	}
```

`pkg/skill/state.go:201-209`（遮蔽方向写反；`idxs[0]` 是扫描顺序第一个，也就是运行时的胜者）：

```go
	for _, idxs := range byKey {
		if len(idxs) < 2 {
			continue
		}
		winner := idxs[0]
		for _, i := range idxs[1:] {
			entries[winner].ShadowedBy = appendUnique(entries[winner].ShadowedBy, entries[i].Path)
			entries[i].Shadows = appendUnique(entries[i].Shadows, entries[winner].Path)
		}
	}
```

所有消费方都按字段名的字面意思理解：TUI `chat_slash.go:1729-1733`（`skillEntryDescription`）（`ShadowedBy` → "shadowed by a higher-priority copy"）、网关 `locateSkillDir`（`ShadowedBy` 为空 = "用户看到的那一行"）、前端 `api.ts:982` 注释（"`shadows`: Directories offering the same name that this row takes precedence over"）。运行时去重见 `pkg/skill/skillrt.go:101-121`：按扫描顺序，第一个出现的名字胜出。

`pkg/skill/skillmeta.go:94-99`（同样写反，而且这三个字段只在测试里被读）：

```go
			if prev, ok := by[k]; !ok {
				by[k] = e
			} else {
				prev.ShadowedBy = appendUniqueString(prev.ShadowedBy, skillDir)
				e.Shadows = appendUniqueString(e.Shadows, prev.SkillDir)
				by[k] = prev
			}
```

`pkg/gateway/skills_download.go:31-49`（网关自有词汇——本计划把它下沉为引擎词汇后删除）：

```go
func skillOrigin(homeDir string, item skill.SkillDTO) string {
	rootPath := strings.TrimSpace(item.RootPath)
	switch strings.TrimSpace(item.Source) {
	case string(skill.SourceProject):
		return "project"
	case string(skill.SourceWorkspace):
		return "agent"
	case string(skill.SourceLocal):
		return "cross-tool"
	case string(skill.SourceGlobal):
		if skillPathWithin(rootPath, filepath.Join(strings.TrimSpace(homeDir), "skills", ".system")) {
			return "builtin"
		}
		return "shared"
	default:
		return "shared"
	}
}
```

（`skillPathWithin` 在行 705 还有别的调用方，必须保留。）

`pkg/tui/chat_slash.go:1696-1719`：

```go
func skillSourceRank(source string) int {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "project":
		return 0
	case "workspace":
		return 1
	case "global", "user":
		return 2
	case "system":   // ← 引擎从不产出 "system"，这是死分支
		return 4
	default:
		return 3
	}
}

func skillEntryCategory(entry skill.Entry) string {
	source := strings.TrimSpace(entry.Source)
	if source == "" {
		source = "installed"
	}
	return strings.ToUpper(source[:1]) + source[1:] + " skills"
}
```

### 必须遵守的仓库约定

- 每个包都有 `doc.go`，包的职责变化时要同步更新（本计划不改变 `pkg/skill` 的职责，`doc.go` 不需要改）。
- 不加防御代码；死代码彻底删除；改动后的注释说明"为什么"，风格与周边一致（参考 `roots.go` 的注释写法）。
- 状态库没有持久化 skill 的 Source 值（已核实：`disabled.json` 以路径为键，`skills-lock.json` 里的 `source` 是安装来源，属于另一个概念），**不需要做任何迁移**。
- 模型可见的内容：skill 目录会按发现结果列出名称、描述和路径。本计划会让新会话中两处目录内容一次性变化：frontend-design 改为指向用户自装的那份；Codex 内置的 5 个 skill 不再出现。目录按会话冻结，同一会话内不会抖动，对 prompt 缓存命中率没有持续影响。

## 需要用到的命令

| 用途 | 命令 | 成功时的预期 |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| vet | `go vet ./pkg/skill ./pkg/gateway ./pkg/tui` | exit 0 |
| 测试（引擎） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill -count=1` | ok |
| 测试（网关） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` | ok |
| 测试（TUI，相关） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1 -run 'Skill\|MemoryImport\|SlashMenuPanel'` | ok |
| 测试（运行时） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/turn ./pkg/run -count=1` | ok |
| 包依赖图 | `scripts/package-graph.sh && git diff --exit-code pkg/architecture/testdata/graph.json` | exit 0（本计划不新增 import） |
| 前端测试 | `cd frontend && pnpm test` | 全部通过 |

制定计划时基线：`pkg/skill`、`pkg/gateway` 全绿；`pkg/tui -run 'Skill|SlashMenuPanel|StatusPanel|RichPicker|MemoryImport'` 全绿。

## 范围

**允许修改的文件**：

- `pkg/skill/origin.go`（新建）、`pkg/skill/origin_test.go`（新建）
- `pkg/skill/skilltrust.go`、`pkg/skill/skilltrust_test.go`（删除）
- `pkg/skill/roots.go`、`hub.go`、`discover.go`、`skill.go`、`system.go`、`state.go`、`skillmeta.go`、`service.go`
- `pkg/skill/hub_test.go`、`state_test.go`、`install_source_test.go`、`offline_test.go`、`service_test.go`、`discover_test.go`、`skillmeta_test.go`（只做因改名而编译不过的机械修改，以及下文点名的修改）
- `pkg/gateway/skills_download.go`、`pkg/gateway/skills_download_test.go`
- `pkg/tui/chat_slash.go`、`pkg/tui/commands_test.go`，以及 `pkg/tui` 下其他因为 `skill.Entry.Source` / `SkillDTO.Source` 改名而编译失败的测试文件（只做机械改名）
- `frontend/src/lib/api.ts`

**不在范围内（不要碰，即使看起来相关）**：

- `/skills` 面板的布局（tabs、两列）——那是计划 002。本计划只把 TUI 的分组名换成新词汇。
- `projectSkillDirs` / `userSkillDirs` 的目录清单本身：不增不减。
- `Service.Inspect(name)`、`runSkillNow` 按名字查找的行为（见 README"本期明确不做"）。
- `pkg/run/skills.go` 的写入守卫，以及 `Loader` / `CatalogEntries` / `mergedSkillParser`：它们只需要根路径，继续使用 `AgentSkillRoots()` 返回的 `[]string`，签名不变。
- `pkg/tui/render.go`、`render_test.go`、`setup.go`：里面有 owner 未提交的改动，本计划不需要动它们。
- `InstallResult.Source`（`service.go:68`）：这是"从哪里安装的"（例如 `openai/skills`），和层无关，保持原样。

## Git 工作流

- 不建分支、不提交、不推送（owner 手动提交）。完成后用 `git status --short` 列出改动的文件，供 owner 审阅。

## 步骤

### 第 1 步：新建 `pkg/skill/origin.go`——唯一的层表与词汇

新建文件，内容按下面的形状编写（注释可以润色，语义不能变）：

```go
package skill

import (
	"path/filepath"
	"strings"
)

// Origin is the layer a skill comes from. It is the layer of the root the
// scanner found the skill under — read from the same table the scanner walks
// (skillLayers) — so what a listing calls a skill and where it was loaded from
// can never disagree. Both surfaces show it through Label.
type Origin string

const (
	OriginProject   Origin = "project"    // <project>/.forebrain|.agents|.claude|.codex/skills
	OriginAgent     Origin = "agent"      // <workspace>/skills: the primary agent's own
	OriginShared    Origin = "shared"     // $FOREBRAIN_HOME/skills
	OriginCrossTool Origin = "cross-tool" // ~/.agents|.claude|.codex/skills, shared with other agents
	OriginBuiltin   Origin = "builtin"    // $FOREBRAIN_HOME/skills/.system
)

// Origins lists the layers highest priority first: the order the scanner
// walks them, and the order every surface lists them in.
func Origins() []Origin {
	return []Origin{OriginProject, OriginAgent, OriginShared, OriginCrossTool, OriginBuiltin}
}

// Label is the layer's display name.
func (o Origin) Label() string {
	switch o {
	case OriginProject:
		return "Project"
	case OriginAgent:
		return "Agent"
	case OriginShared:
		return "Shared"
	case OriginCrossTool:
		return "Cross-tool"
	case OriginBuiltin:
		return "Built-in"
	}
	return string(o)
}

// Rank is the layer's place in Origins(); an origin outside it sorts last.
func (o Origin) Rank() int {
	for i, known := range Origins() {
		if o == known {
			return i
		}
	}
	return len(Origins())
}

// Root is one directory the scanner walks, and the layer of every skill found
// under it.
type Root struct {
	Path   string
	Origin Origin
}

// RootPaths drops the layers, for callers that only walk or guard the
// directories.
func RootPaths(roots []Root) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		out = append(out, root.Path)
	}
	return out
}

// skillLayers is the one table of skill roots, highest priority first. The
// runtime walks it (through agentSkillRoots) and every listing classifies by
// it, so there is no second rule to drift from the first. projectRoots are the
// caller's: trusted ones for what the model may load, all of them for the
// management view.
func skillLayers(home string, workspaceRoot string, projectRoots []string) []Root {
	h := strings.TrimSpace(home)
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	out := make([]Root, 0, len(projectRoots)+len(userSkillDirs)+3)
	for _, root := range projectRoots {
		out = append(out, Root{Path: root, Origin: OriginProject})
	}
	if workspaceRoot != "" {
		out = append(out, Root{Path: filepath.Join(workspaceRoot, "skills"), Origin: OriginAgent})
	}
	if h != "" {
		out = append(out, Root{Path: filepath.Join(h, "skills"), Origin: OriginShared})
	}
	for _, root := range UserSkillRoots() {
		out = append(out, Root{Path: root, Origin: OriginCrossTool})
	}
	if h != "" {
		out = append(out, Root{Path: filepath.Join(h, "skills", systemSkillsDirName), Origin: OriginBuiltin})
	}
	return out
}

// scanRootKey is the spelling scanSkillRoots records a root under
// (discoveredSkill.Root), so a found skill can be matched back to its layer.
func scanRootKey(root string) string {
	root = strings.TrimSpace(root)
	if abs, err := skillAbs(root); err == nil {
		return abs
	}
	return filepath.Clean(root)
}

// layerOrigins maps each root's scan key to its layer. A directory listed
// twice (a project opened at the user's home makes ~/.claude/skills both a
// project and a cross-tool root) keeps the higher-priority layer, the one the
// scanner reaches it through first.
func layerOrigins(roots []Root) map[string]Origin {
	out := make(map[string]Origin, len(roots))
	for _, root := range roots {
		key := scanRootKey(root.Path)
		if _, ok := out[key]; !ok {
			out[key] = root.Origin
		}
	}
	return out
}
```

然后把 `discover.go:50-59` 中计算 `absRoot` 的那几行改为调用 `scanRootKey(root)`，保证两边用的是同一种写法：

```go
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		absRoot := scanRootKey(root)
		scanSkillDir(absRoot, absRoot, "", 0, &out)
	}
```

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/skill` → exit 0。

### 第 2 步：运行时根列表改为从层表派生；删除 Hub 的手抄列表

1. `pkg/skill/roots.go`：把 `agentSkillRoots` 的函数体整个替换为：

   ```go
   func agentSkillRoots(home string, workspaceRoot string, trustedProjectRoots []string) []string {
   	return RootPaths(skillLayers(home, workspaceRoot, trustedProjectRoots))
   }
   ```

   保留它上方 `AgentSkillRoots` 的注释和签名。

2. `pkg/skill/hub.go`：
   - `Hub` 结构体中，用 `Layers []Root` 替换 `Roots []string`，字段注释写："Layers is the table the hub lists, highest priority first."。删除 `AddRoot` 方法。
   - `NewForWorkspace`：用下面一行替换所有 `h.AddRoot(...)` 调用，并保留并更新原注释（"Same roots, same order as the runtime … the project roots are listed without the trust gate"）：

     ```go
     	h.Layers = skillLayers(h.Home, h.WorkspaceRoot, ProjectSkillRootsForDir(h.ProjectRoot))
     ```

3. `pkg/skill/hub_test.go`：
   - 把 `h := &Hub{Home: home}` 加上 `h.AddRoot(X)` 的组合，改成 `h := &Hub{Home: home, Layers: []Root{{Path: X, Origin: O}}}`。其中 X 为 `filepath.Join(home, "workspace", "skills")` 时 O 取 `OriginAgent`；X 为 `.../skills/.system` 时 O 取 `OriginBuiltin`。
   - `TestNewForHomeIncludesGlobalAndLegacySkillRoots` 里遍历 `h.Roots` 的地方改为遍历 `h.Layers` 并取 `.Path`。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/skill` → 此时 `ListManaged` 等还在引用 `h.Roots`，编译失败是**预期内**的，第 4 步会修好。只要报错仅涉及 `h.Roots`、`Source`、`Trust`，就继续下一步。

### 第 3 步：扫描器永不进入点目录

1. `pkg/skill/discover.go:78` 改为：

   ```go
   		if leaf == "" || strings.HasPrefix(leaf, ".") {
   			continue
   		}
   ```

2. 同文件行 36-37 的规则注释改为：

   ```
   //   - Only directories, and never a dot-directory. The built-in skills live
   //     in one ($FOREBRAIN_HOME/skills/.system), and they are reached as a root
   //     of their own — the lowest-priority one — never from the shared root
   //     above it, so a skill the user installed always outranks a built-in one
   //     of the same name.
   ```

3. `pkg/skill/skill.go:27-29` 的 `systemSkillsDirName` 注释改为："systemSkillsDirName is the directory under $FOREBRAIN_HOME/skills the built-in skills are installed in. The scanner never descends into it from its parent; skillLayers lists it as a root of its own."

4. `pkg/skill/system.go:28` 改为 `dest := filepath.Join(forebrainHome, "skills", systemSkillsDirName)`。

5. 检查 `pkg/skill/discover_test.go:100-110`：它直接把 `filepath.Join(root, ".system")` 当作根来扫，在新规则下行为不变（`.system` 是根本身，扫描的是它的子目录），不需要修改。

**验证**：`grep -n 'systemSkillsDirName' pkg/skill/*.go` → 定义（`skill.go`）以及 `origin.go`、`system.go` 中的使用，`discover.go` 中不再出现。

### 第 4 步：`Entry` / `SkillDTO` / `ManagedSkill` / `InspectResult` 改用 `Origin`，并修正遮蔽方向

1. `pkg/skill/state.go`，`Entry`：
   - 删除 `Source string \`json:"source,omitempty"\``，在同一位置加上 `Origin Origin \`json:"origin,omitempty"\``。
   - 给两个遮蔽字段加注释（字段本身不改名）：

     ```go
     	// ShadowedBy is the higher-priority copy sharing this skill's name — the
     	// one the runtime loads instead of this one.
     	ShadowedBy []string `json:"shadowed_by,omitempty"`
     	// Shadows are the lower-priority copies sharing this skill's name that
     	// this one hides.
     	Shadows []string `json:"shadows,omitempty"`
     ```

2. `DiscoverForWorkspace`（行 148-187）：
   - 把 `roots := agentSkillRoots(home, workspaceRoot, TrustedProjectSkillRoots(home, projectRoot))` 改为：

     ```go
     	layers := skillLayers(home, workspaceRoot, TrustedProjectSkillRoots(home, projectRoot))
     	origins := layerOrigins(layers)
     ```

   - `scanSkillRoots(roots)` 改为 `scanSkillRoots(RootPaths(layers))`。
   - 构造 `Entry` 时，把 `Source: string(SourceForPathWithWorkspace(...))` 换成 `Origin: origins[found.Root],`。
   - 此后 `stateRoot` 只用于 `Load(stateRoot)`；如果编译器提示 `projectRoot` 等参数除这里以外再无用处，以编译器为准，不要为了消除警告去改函数签名。

3. `annotateShadows`（行 205-208）改为：

   ```go
   		winner := idxs[0]
   		for _, i := range idxs[1:] {
   			entries[winner].Shadows = appendUnique(entries[winner].Shadows, entries[i].Path)
   			entries[i].ShadowedBy = appendUnique(entries[i].ShadowedBy, entries[winner].Path)
   		}
   ```

   并在函数上方加注释："annotateShadows marks, for every name more than one root offers, the copy scanned first — the one the runtime loads (see mergedSkillParser.collect) — as shadowing the others."

4. `pkg/skill/hub.go`：
   - `SkillDTO`：删除 `Source` 和 `Trust` 两个字段，在 `RootPath` 之后加上 `Origin Origin \`json:"origin,omitempty"\``。
   - `ManagedSkill`：同样删除 `Source`、`Trust`，加上 `Origin Origin`。
   - `ListManaged`：在函数开头加上 `origins := layerOrigins(h.Layers)`；把 `scanSkillRoots(h.Roots)` 改为 `scanSkillRoots(RootPaths(h.Layers))`；构造 `ManagedSkill` 时删除 `Source:` 和 `Trust:` 两行，加上 `Origin: origins[found.Root],`。
   - `ListManagedDTO`：删除 `Source:`、`Trust:` 两行，加上 `Origin: item.Origin,`。
   - `ListEnabledDTO`：改为遍历 `h.ListManaged()`，跳过 `!item.Enabled` 的项，用 `item` 的字段（`Name`、`Description`、`AllowedTools`、`RootPath`、`Origin`、`Enabled: true`）构造 DTO。这与原先"`h.List()` 再按路径分类"的结果相同，只是分类改由层表决定。
   - 删除 `trustLabelForPathWithWorkspace`（行 126-137）。

5. `pkg/skill/service.go`：
   - `InspectResult`：删除 `Source`、`Trust`，加上 `Origin Origin \`json:"origin"\``。
   - `Inspect`：改为 `Origin: item.Origin,`，并且 `AvailableActions: inspectActionsForOrigin(item.Origin),`。
   - 把 `inspectActionsForSource` 改名为 `inspectActionsForOrigin(origin Origin)`，内部 `case "project":` 改为 `case OriginProject:`。

6. `pkg/skill/skillmeta.go`：
   - `regEntry`：删除 `Source`、`ShadowedBy`、`Shadows` 三个字段。
   - `RefreshForWorkspace`：删除构造 `regEntry` 时的 `Source:` 行；把行 94-99 的 `if/else` 简化为：

     ```go
     			if _, ok := by[k]; !ok {
     				by[k] = e
     			}
     ```

   - 如果 `appendUniqueString` 因此不再有调用方，删除它（以 `grep -n appendUniqueString pkg/skill/*.go` 为准）。

7. 删除 `pkg/skill/skilltrust.go` 和 `pkg/skill/skilltrust_test.go`。

8. 修复 `pkg/skill` 里因改名而失败的测试，**只做机械替换**：
   - `hub_test.go:36-41`：把对 `list[0].Source`、`list[0].Trust` 的断言替换为 `if list[0].Origin != OriginAgent { t.Fatalf("origin=%q want agent", list[0].Origin) }`。
   - `offline_test.go:99`：`entry.Source == string(SourceWorkspace)` 改为 `entry.Origin == OriginAgent`。
   - `service_test.go:219-220`：`installed.Metadata.Source != "project"` 改为 `installed.Metadata.Origin != OriginProject`（失败信息同步修改）。
   - `state_test.go:137-138`、`173-174`：`entry.Source != "project"` 改为 `entry.Origin != OriginProject`。
   - `install_source_test.go:34-36`：删除 `if entry.Source != "global" {...}` 这个块（`regEntry` 已经没有该字段；函数中其余断言保留）。
   - 其他同类编译错误按同样的映射处理：`"project"`→`OriginProject`，`"workspace"`→`OriginAgent`，`"global"`→ 路径在 `.system` 下取 `OriginBuiltin`、否则取 `OriginShared`，`"local"`→`OriginCrossTool`。

**验证**：
- `CGO_ENABLED=1 go build -tags fts5 ./pkg/skill` → exit 0
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill -count=1` → ok
- `grep -rn 'SourceForPath\|SourceLocal\|SourceGlobal\|SourceWorkspace\|SourceProject\|trustLabelFor' pkg/skill` → 无输出

### 第 5 步：网关改用引擎的 Origin，删除 `skillOrigin`

`pkg/gateway/skills_download.go`：

1. 删除 `skillOrigin`（行 26-49，连同它上方的注释）。保留 `skillPathWithin`（行 705 仍在使用）。
2. `skillListEntry`：删除 `Origin string \`json:"origin"\``——内嵌的 `skill.SkillDTO` 现在自带 `origin`，JSON 键名不变。把注释改为 "the engine's DTO plus what the page asking may do with it"。
3. `decorateSkillList`：删除 `origin := skillOrigin(s.Home, item)`；构造 `row` 时去掉 `Origin: origin,`，并把 `Editable: origin == owner` 改为 `Editable: string(item.Origin) == owner`。
4. 把 `locateSkillDir(svc, name) (string, bool)` 改为 `locateSkill(svc *skill.Service, name string) (skill.Entry, bool)`：逻辑不变（第 4 步修正之后，"`ShadowedBy` 为空的第一项"就是运行时的胜者，与列表里显示的那一行一致），只是返回整个 `entries[i]` 而不是 `.Path`。注释中关于 "the row the user sees is the unshadowed one" 的说法保留，它现在是对的。
5. 调用方：行 271、338、625 改为 `entry, ok := locateSkill(svc, name)`，原来使用 `dir` 的地方改用 `entry.Path`（可以写 `dir := entry.Path` 来减少改动）。
6. 删除处理（行 400-416）：

   ```go
   	entry, ok := locateSkill(svc, name)
   	if !ok { ...404 不变... }
   	dir := entry.Path
   	layer, allowed := skillDeleteLayer(scope, svc)
   	...
   	if entry.Origin == skill.OriginBuiltin { ...403 不变... }
   	if string(entry.Origin) != layer {
   		http.Error(w, fmt.Sprintf("this skill belongs to the %s layer; delete it there", entry.Origin), http.StatusForbidden)
   		return
   	}
   ```

7. `pkg/gateway/skills_download_test.go:125` 的精确子串断言改为新的字段顺序（`origin` 现在位于 `root_path` 之后、`enabled` 之前，且不再有 `source` / `trust`）：

   ```go
   	require.Contains(t, body, `"name":"agent-skill","description":"agent-skill skill","root_path":"`+filepath.Join(workspace, "skills", "agent-skill")+`","origin":"agent","enabled":true,"editable":true`)
   	require.NotContains(t, body, `"source":`)
   	require.NotContains(t, body, `"trust":`)
   ```

   如果实际 JSON 中 `editable` 与 `enabled` 之间还有别的字段导致子串不匹配，先打印 `body` 查看真实顺序，再按真实顺序修正这一个断言；**不要**为了凑断言去改结构体的字段顺序。

**验证**：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1` → ok
- `grep -n 'skillOrigin\|SourceForPath' pkg/gateway/*.go` → 无输出

### 第 6 步：TUI 改用新词汇，删除 TUI 自带的分类器

`pkg/tui/chat_slash.go`：

1. `SkillListString`（行 203-231）：把 `skillSourceLabel(...)` 的那个 `if` 块改为直接追加 `" [" + it.Origin.Label() + "]"`（`it` 是 `skill.SkillDTO`）。然后删除 `skillSourceLabel` 和 TUI 自己的 `hasPathPrefix`（行 524-558），并确认 `grep -n 'hasPathPrefix' pkg/tui/*.go` 无输出（该函数没有别的调用方）。如果 `filepath` 等 import 因此变成未使用，按编译器提示删除。
2. `ApplySkillSelection`（行 306）：格式串中的 `source: %s\ntrust: %s` 替换为 `origin: %s`，对应参数改为 `res.Origin.Label()`。
3. `skillInstallReport`（行 473-475）：把 `"source: "+res.Metadata.Source` 和 `"trust: "+res.Metadata.Trust` 两行替换为一行 `lines = append(lines, "origin: "+res.Metadata.Origin.Label())`。**行 466 的 `"source: "+res.Source` 不要动**（那是安装来源）。
4. `sortSkillEntries`：比较函数改为先比较 `out[i].Origin.Rank()`，再比较小写名称，最后比较 `out[i].Path`（让同名副本的顺序确定）。删除 `skillSourceRank`。
5. `skillEntryCategory` 的函数体改为 `return entry.Origin.Label()`，注释改为 "skillEntryCategory is the layer a skill is listed under, by its display name."。这样分组标题会从 "Project skills" 变成 "Project"——这是预期的，计划 002 会把这些分组变成 tab。
6. `handleSkillToggle`（行 1850-1852）：`item.Source` 改为 `item.Origin.Label()`（计划 002 会整体重写这个函数，这里只需要能编译）。
7. 测试夹具：`pkg/tui/commands_test.go:149-150` 改为 `Origin: skill.OriginProject` 和 `Origin: skill.OriginBuiltin`；`pkg/tui` 下其他因为 `skill.Entry{... Source: ...}` 或 `skill.SkillDTO{... Source/Trust ...}` 编译失败的测试，按第 4 步第 8 点的映射做机械替换。

**验证**：
- `CGO_ENABLED=1 go build -tags fts5 ./...` → exit 0
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1 -run 'Skill|MemoryImport|SlashMenuPanel'` → ok
- `grep -rn '\.Source\b' pkg/tui/chat_slash.go` → 只剩 `res.Source`（安装来源，`skillInstallReport` 里）

### 第 7 步：前端类型跟随

`frontend/src/lib/api.ts`：

- `SkillRecord`：删除 `source?: string` 和 `trust?: string`。`origin?: SkillOrigin` 保留，其注释保持不变。
- `SkillInspectResponse.skill`：删除 `source: string` 和 `trust: string`，加上 `origin: SkillOrigin`。

（已核实：没有组件读取 skill 的 `source` / `trust`，`skillInspect` 也没有调用方。）

**验证**：
- `grep -rn "\.source\b\|\.trust\b" frontend/src/components/skills frontend/src/views/SkillsView.vue frontend/src/components/project/ProjectSkills.vue frontend/src/components/settings/SettingsSharedSkillsTab.vue frontend/src/components/workshop` → 无输出
- `cd frontend && pnpm test` → 全部通过

### 第 8 步：新增测试，锁定"一条规则"

新建 `pkg/skill/origin_test.go`，写法参照 `pkg/skill/state_test.go:107-145`（用 `writeSkill` 辅助函数造 skill，用 `safety.MarkTrusted` 信任项目）。每个测试都先 `t.Setenv("HOME", t.TempDir())`，确保跨工具目录指向临时目录（macOS 和 Linux 上 `os.UserHomeDir()` 都读取 `$HOME`）。测试清单：

1. `TestSkillLayersAreTheOneOrderedTable`：用临时目录 home、ws 和 `ProjectSkillRootsForDir(repo)` 调用 `skillLayers`，断言得到的 `(Path, Origin)` 序列**完全等于**：4 个项目目录（project）→ `ws/skills`（agent）→ `home/skills`（shared）→ `$HOME/.agents/skills`、`$HOME/.claude/skills`、`$HOME/.codex/skills`（cross-tool）→ `home/skills/.system`（builtin）。同时断言 `agentSkillRoots(home, ws, roots)` 等于该序列的 `RootPaths`。
2. `TestDiscoverClassifiesByTheRootFound`：在已信任的项目里，往 4 个项目目录各放一个 skill，再在 `ws/skills`、`home/skills`、3 个跨工具目录、`home/skills/.system` 各放一个名字不同的 skill。断言 `DiscoverForWorkspace` 返回的每个 entry 的 `Origin` 都正确，且**没有任何 entry 的 Origin 为空字符串**。
3. `TestUserInstalledSkillOutranksBuiltin`：`home/skills/frontend-design` 和 `home/skills/.system/frontend-design` 同时存在。断言：
   - `DiscoverForWorkspace` 里 shared 那份的 `Shadows == [builtin 的路径]`，builtin 那份的 `ShadowedBy == [shared 的路径]`；
   - `(Loader{Roots: AgentSkillRoots(home, ws, safety.ProjectContext{}), StateRoot: ws}).Discover()` 返回 `[]DiscoveredSkill`。用 `Name == "frontend-design"` 找到那一项，断言 `CanonicalSkillPath(item.SkillFile) == CanonicalSkillPath(filepath.Join(home, "skills", "frontend-design", "SKILL.md"))`。两边都要规范化，因为 macOS 上 `t.TempDir()` 位于 `/var`，它是 `/private/var` 的符号链接。`Loader` 的写法参考 `pkg/turn/skills.go:18-21`。
4. `TestScannerNeverDescendsIntoDotDirectories`：`$HOME/.codex/skills/.system/imagegen` 和 `$HOME/.claude/skills/.hidden/x` 都放上合法的 SKILL.md，断言 `DiscoverForWorkspace` 结果中没有 `imagegen` 和 `x`。
5. `TestShadowAnnotationNamesTheLoadedCopy`：已信任项目的 `.claude/skills/improve` 与 `$HOME/.agents/skills/improve` 同时存在。断言项目那份 `Shadows == [cross-tool 路径]` 且 `ShadowedBy` 为空；cross-tool 那份 `ShadowedBy == [项目路径]`；`(Loader{Roots: agentSkillRoots(home, ws, TrustedProjectSkillRoots(home, repo)), StateRoot: ws}).Discover()` 中名为 improve 的那一项，其 `SkillFile` 位于项目的 `.claude/skills/improve` 下（比较方式同测试 3，用 `CanonicalSkillPath`）。
6. `TestHubListsTheSameLayersAsDiscovery`：同样的目录布局下，`NewForWorkspace(home, ws, repo).ListManaged()` 中每一项的 `Origin` 与 `DiscoverForWorkspace` 中同路径 entry 的 `Origin` 一致。

网关在 `pkg/gateway/skills_download_test.go` 中新增 `TestLocateSkillPicksTheLoadedCopy`：仿照同文件的 `TestSkillListCarriesOriginEditableAndDownloadURL`（行 109 起）的 `rulesServer` / `writeGatewaySkill` 写法，在 `home/skills` 和 `home/skills/.system` 中放同名 skill，断言 `locateSkill` 返回的 entry 路径在 `home/skills` 下、`Origin == skill.OriginShared`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/skill ./pkg/gateway -count=1 -run 'Layers|Classif|Outranks|DotDirector|Shadow|Hub|LocateSkill'` → ok，且新测试全部出现在 `-v` 输出中。

### 第 9 步：全量回归

依次运行"需要用到的命令"表里的所有命令（前端测试除外，第 7 步已跑过）。

**验证**：全部符合预期列。

## 测试计划

- 新增：`pkg/skill/origin_test.go` 中的 6 个测试，以及网关的 `TestLocateSkillPicksTheLoadedCopy`（见第 8 步）。
- 修改：第 4 步第 8 点、第 5 步第 7 点、第 6 步第 7 点列出的断言或夹具。
- 删除：`pkg/skill/skilltrust_test.go`（它测试的是被删除的分类器；同等的覆盖由 `TestDiscoverClassifiesByTheRootFound` 提供）。

## 完成标准

以下全部成立：

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` exit 0
- [ ] `go vet ./pkg/skill ./pkg/gateway ./pkg/tui` exit 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill ./pkg/gateway ./pkg/turn ./pkg/run -count=1` 全部 ok
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/tui -count=1` ok
- [ ] `cd frontend && pnpm test` 全部通过
- [ ] `grep -rn 'SourceForPath\|SourceLocal\|SourceGlobal\|SourceWorkspace\|SourceProject\|trustLabelFor\|skillOrigin(\|skillSourceLabel\|skillSourceRank' pkg` 无输出
- [ ] `test ! -e pkg/skill/skilltrust.go` 成功
- [ ] `grep -n 'HasPrefix(leaf, ".") && ' pkg/skill/discover.go` 无输出（点目录例外已删除）
- [ ] `scripts/package-graph.sh && git diff --exit-code pkg/architecture/testdata/graph.json` exit 0
- [ ] 相比 `/tmp/skills-001-before.diff`，新增的改动只出现在"范围"列出的文件里。对范围外、开始前就有改动的文件（例如 `pkg/tui/render.go`、`render_test.go`、`setup.go`、`pkg/turn/*`、`pkg/tool/*`），`git diff -- <文件>` 必须与留底中该文件的部分完全一致
- [ ] README 状态行已更新

## STOP 条件

遇到以下情况，停下来报告，不要自行处理：

- "现状"里的摘录与实际代码对不上（代码在制定计划之后有漂移）。
- 某一步的验证在一次合理修复后仍然失败。
- 有测试**刻意**断言了"`.system` 会从 `$FOREBRAIN_HOME/skills` 根下被扫描到"，或者断言了"内置副本胜过用户副本"。这与 owner 的决策冲突，需要 owner 确认后才能改。
- 发现任何持久化存储（SQLite、JSON 状态文件）里写入了 skill 的 `source` / `trust` 值。这样就需要迁移（owner 规则：改结构必须迁移旧库），本计划没有覆盖。
- `pkg/run`、`pkg/turn` 或其他包里有非测试代码读取 `SkillDTO.Source/Trust`、`Entry.Source` 或 `InspectResult.Source/Trust`，而本计划没有列出。
- 改动看起来需要动 `pkg/tui/render.go`、`render_test.go`、`setup.go`。

## 维护说明

- **新增 skill 目录**：只需改 `projectSkillDirs` / `userSkillDirs`；层归属由 `skillLayers` 自动得出。`TestSkillLayersAreTheOneOrderedTable` 会提醒同步更新期望序列。
- **审阅重点**：`annotateShadows` 方向修正后，网关 `locateSkill` 的取值会从"落选副本"变为"胜出副本"。这是修 bug，但会改变 Web 上"下载/编辑/删除同名 skill"实际作用的目录，审阅时要确认这些都指向列表中显示的那一行。
- **已知边界**：`annotateShadows` 不考虑启用状态。如果胜出的副本被禁用，运行时（`skillrt.go:101-104` 先跳过禁用项）会加载下一份，但标注仍然显示被禁用的那份在遮蔽。这是本计划之前就存在的语义，没有改动；若要处理，需要单独立项，并让网关 `locateSkill` 一起跟随。
- **符号链接**：现在层由"被扫描到的根"决定。例如项目 `.claude/skills/foo` 是一个指向 `~/.agents/skills/foo` 的链接时，它被归为 Project（因为它是经由项目根被找到的，且项目根优先级更高、先被扫描到）。这与"项目里的 skill 都归 Project"的要求一致。
