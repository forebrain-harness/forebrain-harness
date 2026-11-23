# Plan 036：技能的草稿、发布与版本——引擎、存储、会话按版本加载、正式目录的改动自动进草稿、gateway 接口

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> 设计以 `docs/plan/SKILL_DRAFTS_AND_VERSIONS_DESIGN.md` 为准（owner 2026-10-11 已定语义与存储）。web 界面在 web 计划 020，TUI 面板在 037，本计划只做引擎与接口。
>
> **Drift check（先跑）**：
> - `grep -n 'strings.HasPrefix(leaf, ".")' pkg/skill/discover.go`：1 行（扫描已跳过点目录）。
> - `grep -n "func (s \*State) LoadedSkillWriteTarget" pkg/tool/loaded_skills.go`：1 行。
> - `grep -n "skill.LoadActivation(" pkg/run/skills.go pkg/turn/skills.go | wc -l`：3。
>
> 不符 → STOP。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（改写技能目录；会话读技能内容的位置变了）
- **Depends on**: 026（工具路径按项目根解析）
- **Category**: feature
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11

## Why this matters

owner 2026-10-11：

> 技能工坊这边需要设计草稿和发布这两个语义……需要给技能设计一套版本管理机制，跟草稿、已发布、有未发布的修改等语义结合起来设计

另外几条：
- 历史版本存在技能目录旁的隐藏目录；
- 进行中的会话继续用会话开始时的版本；
- TUI 也走草稿 → 发布，两端一致，在 `/skills` 面板里发布，不加子命令；
- 正式目录的改动自动进草稿：工作台"工作目录"树和会话里的文件工具，编辑、新建、删除正式目录里的文件都写进草稿，`.versions/` 不显示、不可写。

语义摘要（详见设计文档第 1 节）：
- 每个技能（主代理、项目、共享三层）有一串只增不改的版本 v1、v2……，外加最多一份草稿；
- 三种状态：
  - 草稿：还没有版本，不被加载、不能启用；
  - 已发布 vN；
  - 有未发布的修改：vN 加一份草稿，加载的仍是 vN。
- 所有改动只进草稿：工坊编辑、恢复为草稿、拉取上游更新。只有"发布"生成新版本并写进正式目录。发布后默认启用。
- 安装进来的技能安装即 v1。在线安装、发现安装的记下来源，上游有新提交时标"有更新"。
- 会话开始时冻结技能清单，并记下每个技能的版本，之后读这个版本的内容。

## Current state（已核实）

- **扫描**：`scanSkillDir`（`pkg/skill/discover.go:67` 起）跳过以 `.` 开头的目录（`:76-79`），`.versions/`、`.drafts/` 天然不会被当成技能，**但没有测试锁住这一点**。最多嵌套一层（`maxSkillNestingDepth = 1`，`:64-65`）。
- **会话的技能清单**：
  - 冻结在会话的提示状态里，键为 `skill_catalog`（`pkg/run/skills.go:27-29`）；
  - 清单条目 `CatalogEntry{Name, Description, SkillFile}`（`pkg/skill/catalog.go:120-146`）；
  - 会话里已加载技能的结构 `tool.LoadedSkill`（`pkg/tool/loaded_skills.go`），含 `RootDir`。
- **读技能内容的三处**：
  - `skill` 工具：`loadedSkillByName` → `filepath.Join(item.RootDir, "SKILL.md")` → `skill.LoadActivation`（`pkg/run/skills.go:224-228`）；
  - 显式选择：`pkg/run/skills.go:441`；
  - 斜杠技能命令：`pkg/turn/skills.go:52`。
- **写技能文件**：
  - 文件工具解析写入路径时，先 `st.LoadedSkillWriteTarget(path)`（`pkg/tool/file_tools.go:1368-1374`、`pkg/tool/loaded_skills.go:332-341`）；
  - 命中已加载技能就标为受保护写入，强制审批，原因见 `loadedSkillWriteReason`（`pkg/tool/permissions.go:96-98`）；
  - shell 触及技能目录同样要审批（`loaded_skills.go:343-354`）。
- **技能服务** `skill.Service`（`pkg/skill/service.go`）：`List`、`Inspect`、`Create`、`Update`、`Install`、`SetEnabledPaths`、`Refresh`……
  - 来源记录：`pkg/skill/origin.go`；
  - 在线（git）安装：`pkg/skill/git_install.go`；
  - 离线安装：`offline.go`。
- **内置技能**：`pkg/skill/system_assets/`，其中 `skill-workshop/SKILL.md`、`skill-generator/SKILL.md` 指导模型把技能写到哪里。内置技能只读、没有版本。
- **gateway 技能接口**：`api_extra.go:91` 一带的 `skills` 路由组；项目技能 `handleProjectSkills*`（`:3702-3774`）。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... ./pkg/tool/... ./pkg/run/... ./pkg/turn/... ./pkg/gateway/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg` | 无输出 |

## Scope

**In scope**：
- `pkg/skill`：新文件 `versions.go`，以及 `service.go`、`catalog.go`、`git_install.go`、`offline.go`；
- `pkg/run/skills.go`、`pkg/turn/skills.go`；
- `pkg/tool/{loaded_skills.go,file_tools.go,permissions.go}`；
- `pkg/skill/system_assets/{skill-workshop,skill-generator}/SKILL.md`；
- gateway 技能接口；
- 测试。

**Out of scope**：
- web 界面（web 020）、TUI 面板（037）；
- 内置技能（没有版本）；
- 工作台树隐藏 `.versions/`：本计划在树接口里做一行过滤，见 Step 6，界面在 web 017。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：存储与版本库（`pkg/skill/versions.go`）

布局（`<root>` 是某一层的技能目录：主代理 `<工作区>/skills`、项目 `<项目根>/.forebrain/skills`、共享 `~/.forebrain/skills`）：

```
<root>/<名称>/                     正式目录 = 当前版本的文件
<root>/.versions/<名称>/manifest.json
<root>/.versions/<名称>/v1/ v2/ …   每个版本一份完整拷贝
<root>/.drafts/<名称>/              草稿（最多一份）
```

`manifest.json`：

```json
{"name":"gateway-ops","current":3,"versions":[{"v":1,"at":"…","kind":"created|published|installed|updated","source":"","upstream_rev":"","note":"","stat":{"files":3,"added":86,"removed":0}}],"draft_base":3}
```

- `draft_base` 只在有草稿时存在；
- 一个技能还没有任何版本时（纯草稿），`manifest.json` 可以不存在；
- `stat` 是相对上一版的改动统计。

提供：

```go
type VersionStore struct{ Root string }
func (v VersionStore) State(name string) (SkillVersionState, error)          // current, versions, hasDraft, draftBase, update
func (v VersionStore) EnsureDraft(name string) (draftDir string, err error)    // 没有草稿就从当前版本复制一份
func (v VersionStore) Publish(name, note string) (newVersion int, err error)
func (v VersionStore) Discard(name string) error
func (v VersionStore) RestoreAsDraft(name string, version int) error
func (v VersionStore) Diff(name string, from, to string) ([]FileDiff, error)   // from/to: "v3" 或 "draft"
func (v VersionStore) ReadVersionFile(name string, version int, rel string) ([]byte, error)
func (v VersionStore) Bootstrap(name string, origin Origin) error             // 老技能补 v1
```

**发布要原子**：
1. 先完整写好 `.versions/<名称>/v<N+1>/` 和新的 `manifest.json`（写临时文件再 rename）；
2. 再把正式目录 rename 成 `<root>/.versions/<名称>/.old-<时间戳>`，把 `v<N+1>` 的拷贝 rename 成正式目录；
3. 最后删除草稿与 `.old-*`。

中途失败时正式目录保持原样，`manifest.json` 仍指向旧版本。写一个测试，用可替换的文件操作函数，在第 2 步注入失败。

复制目录时保留文件权限，不跟随指向技能目录之外的符号链接。离线安装已有同类校验，复用 `pkg/skill/offline.go` 的路径校验函数。

### Step 2：老技能补 v1；安装即 v1

1. `Service.Refresh` 里，对三层根目录下每个没有 `.versions/<名称>/` 的正式技能调 `Bootstrap`：
   - 记 v1：`kind` 由 `origin.go` 的来源记录推出（安装 / 新建）；
   - `source` 取安装来源；
   - `at` 取正式目录的修改时间。
   - 内置技能跳过。
2. `Install`（离线、在线、发现）完成后写 v1：`kind=installed`；在线安装记 `upstream_rev`（`git_install.go` 拿得到提交号，核对返回值）。
3. 上游更新检测：在线安装的技能，检查上游最新提交（复用 `git_install.go` 的远端读取能力），与 `manifest` 最新版本的 `upstream_rev` 比较，不同就在 `State` 里给出 `update{rev, date, files}`。
   - **只在列表接口被调用时、且距上次检查超过 1 小时才查**，结果缓存在内存里；
   - 失败就不标"有更新"，不报错。
4. `PullUpdate(name)`：把上游内容取进草稿。已有草稿时先确认，接口参数 `replace_draft`。版本记录的 `kind=updated` 在发布时写。

### Step 3：会话按版本加载

1. 冻结清单时（`skill_catalog` 的写入处，`grep -n "sessionPromptStateSkillCatalog" pkg/run/*.go`），每个条目多记 `Version int`，取该技能 `manifest.current`；内置技能为 0。
2. `tool.LoadedSkill` 加 `Version int`、`VersionsRoot string`（`<root>/.versions/<名称>`）。
3. 三处读内容（`run/skills.go:224-228`、`:441`、`turn/skills.go:52`）改为：
   - `Version > 0` 时读 `<VersionsRoot>/v<Version>/SKILL.md`；
   - 技能自带的资源文件路径（`listSkillResources`，`activation.go:117`）同样指向版本目录。
   - 版本目录被删（不应发生）→ 返回错误"技能 X 的 v<k> 已不存在"，不回落到正式目录。
4. 草稿技能（`current = 0`）不进清单：它没有正式目录，扫描本来就扫不到，补测试确认。

### Step 4：正式目录的改动自动进草稿（owner 2026-10-11）

1. `LoadedSkillWriteTarget` 之前加一层判断，`ResolveSkillDraftTarget(path)`：
   - 路径落在任一层技能根的 `<root>/<名称>/...` 内，名称不以 `.` 开头 → `EnsureDraft(名称)`，返回 `<root>/.drafts/<名称>/<相对路径>`；
   - 路径落在 `<root>/.versions/...` 内 → 拒绝，错误"版本记录不可修改"；
   - 路径落在 `<root>/.drafts/...` 内 → 原样。
2. 文件工具（`write_file`、`edit_file`、`apply_patch` 含删除和移动）用重定向后的路径。工具结果里追加一句："已写入技能 X 的草稿（发布后生效）：<草稿路径>"。`edit_file` 读旧内容时也读草稿（`EnsureDraft` 之后草稿里已有当前版本的内容）。
3. 受保护审批（`loadedSkillWriteReason`）只用于写**正式目录**。重定向之后写的是草稿，不再触发这项审批，按普通写入策略处理。shell 触及技能目录的审批（`LoadedSkillShellAccessReason`）保留，shell 无法重定向。
4. 删除整个技能目录（`apply_patch` 删除目录、或今后的树接口）→ 拒绝，错误"删除技能请在技能列表里操作"。
5. 草稿目录也遵守 033 的改动记录（`RecordFileChange`），"回到这里"能撤回草稿里的改动。

### Step 5：工坊提示词改成写草稿

`skill-workshop/SKILL.md`、`skill-generator/SKILL.md` 里讲"把技能写到哪里"的段落改为：
- 新技能写到 `<技能根>/.drafts/<名称>/`；
- 改已有技能也写草稿：直接写正式目录路径，系统会自动转进草稿，但提示词里说清楚写草稿；
- 写完告诉用户"草稿已保存，发布后生效"：web 在顶栏"发布"，TUI 在 `/skills` 面板里发布。

措辞照 `SKILL_DRAFTS_AND_VERSIONS_DESIGN.md` 3.6。这两个文件是内置资源，改后跑 `pkg/skill` 的内置资源测试。

### Step 6：gateway 接口

主代理层、项目层、共享层各一组，路径与现有技能接口同构：主代理 `/api/skills/:name/...`、项目 `/api/projects/:id/skills/:name/...`、共享沿用现有前缀，用 `grep -n "skills.Get\|skills.Post" pkg/gateway/api_extra.go` 对齐。

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `.../versions` | `State` 的 JSON：版本列表、当前版本、草稿、上游更新 |
| POST | `.../publish` `{note}` | 发布 → `{version}` |
| POST | `.../draft/discard` | 丢弃草稿 |
| POST | `.../versions/:v/restore` `{replace_draft}` | 恢复为草稿 |
| POST | `.../pull` `{replace_draft}` | 拉取上游到草稿 |
| GET | `.../diff?from=v3&to=draft` | 文件级 diff |
| GET | `.../versions/:v/download` | 下载某版本的 zip，复用现有 zip 下载 |

另外：
- 列表接口每行加 `version`、`state`（`draft`、`published` 或 `pending`）、`update`；
- 启用 / 停用接口对草稿技能返回 400"草稿不能启用"；
- 文件树接口（022 的 `handleWorkspaceTree`）在列出某个技能根时不列 `.versions`。

### Step 7：测试

- `pkg/skill`：
  - 扫描不把 `.versions`、`.drafts` 当技能；
  - `Bootstrap`；
  - 发布原子性（注入失败）；
  - 丢弃；恢复为草稿；
  - diff；
  - 安装即 v1；
  - 拉取上游：用本地 git 仓库当上游。
- `pkg/run`：会话冻结 v1 后发布 v2，同一会话里调用 `skill` 工具读到的是 v1 的内容，新会话读到 v2。
- `pkg/tool`：
  - 写正式目录的 `SKILL.md` 落在草稿里，正式目录不变，结果文字带"已写入草稿"；
  - 写 `.versions` 被拒；
  - 删除整个技能目录被拒；
  - 写草稿不触发受保护审批。
- gateway：各接口的成功与错误路径，以及草稿不能启用。
- 真机（glm-5.3，隔离 home）：
  1. 在项目里放一个已发布技能；
  2. 让模型改它的 `SKILL.md`，确认改动进了草稿；
  3. 同一会话里 `/技能名` 用的还是旧内容；
  4. 通过接口发布 v2，新会话用的是新内容。

## Test plan

见 Step 7。

## Done criteria

- [ ] Commands 表全部通过
- [ ] Step 7 列出的测试存在且 PASS；真机四步符合
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. 冻结的技能清单被写进了提示缓存的冻结前缀，加 `Version` 字段会让所有老会话的前缀失效：停下评估。只加在会话私有状态里、不影响前缀文本的话可以继续。
3. 在线安装拿不到上游提交号。
4. 共享层技能的根目录在多个主代理间共用，版本库并发写会冲突：停下报告，不要自己加锁方案。

## Maintenance notes

- 版本目录只增不改。任何"修复某个旧版本"的需求都应该是"恢复为草稿 → 改 → 发布"。
- `.versions/` 可以随项目被 git 提交（owner 定的），体积大的技能注意仓库大小。
- 新增会写技能目录的入口（例如 web 017 的树操作接口），必须走 `ResolveSkillDraftTarget`。
