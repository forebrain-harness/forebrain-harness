# 计划 010：技能生命周期——三页有效集合视图（继承只读）、在线/离线安装、zip 单条与批量下载、归属层启停与删除

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新 README 中本计划的状态行。**不要提交代码。**
>
> **漂移检查（先运行）**：
> `git diff --stat b92a1b1..HEAD -- pkg/skill/ pkg/gateway/api_extra.go frontend/src/views/SettingsView.vue frontend/src/components/settings/SkillsInstalledList.vue`
> 计划 007 改过 `api_extra.go` 路由注册行与 `SettingsView.vue`（标签化）、008 建过项目空间壳。那是预期的。

## 状态

- **优先级**：P1
- **工作量**：L
- **风险**：MED-HIGH（新增解压上传端点：必须防 zip-slip、解压炸弹、符号链接；安全测试是本计划验收主体）
- **依赖**：计划 007（`/skills` 菜单位、设置页"共享技能"标签挂点）、计划 008（项目空间 skills 标签挂点）
- **类别**：direction / feature
- **基线**：提交 `b92a1b1`，2026-09-29

## 为什么要做

owner 要求（第 29 条）："删掉'创建或编辑私有技能'，'安装到本主代理'改成'在线安装'，提示需访问公网，内网环境请使用离线安装，新增离线安装功能，支持上传zip、rar、tar.gz等压缩包安装，技能列表加下载按钮，下载zip压缩包，支持单条下载和多选批量下载"——设置页共享技能、主代理技能页、项目技能页三处都按此修改。第 30 条：主代理技能列表 = 本主代理 + 共享 + 内置；项目空间技能列表 = 项目 + 主代理 + 共享 + 内置（实际可加载集合）；继承行完全只读（决策 D9：每个技能只在归属层启停）。创建/编辑技能归技能工坊（计划 011），技能页不再有创建/编辑入口。

## 现状

- **发现与来源**：`pkg/skill/state.go:11` `Entry{Name, Description, Path, Source, Enabled, ShadowedBy, Shadows}`；`:148` `DiscoverForWorkspace(home, workspaceRoot, projectRoot)`。加载根集合 `pkg/skill/roots.go:62` `agentSkillRoots`（优先级从高到低）：受信项目技能根 → 主代理私有 `<工作区>/skills` → 共享 `<home>/skills` → 跨工具用户目录（`UserSkillRoots :87`）→ 内置 `<home>/skills/.system`。来源常量 `pkg/skill/skilltrust.go:10-14`：`global`/`workspace`/`project`/`local`——**内置与共享同为 `global`**，区分"内置"要按路径含 `<home>/skills/.system` 判定。项目技能过信任门（`roots.go:113` `TrustedProjectSkillRoots` → `safety.IsTrusted`）：不受信项目返回空。同名遮蔽由 `annotateShadows`（`state.go:189`）标注。
- **启停**：全量语义——`pkg/skill/state.go:93` `SetEnabledPathsForWorkspace`（提交的是完整 enabled 路径清单，存 `<主代理工作区>/state/skills/disabled.json`）；HTTP 层 `pkg/gateway/api_extra.go:2361` `handleSkillsToggle` 收 `{enabled_paths}` 后 `svc.SetEnabledPaths` 并返回最新列表。**启停状态按主代理工作区隔离**；项目维度的启停走同一机制加 `projectRoot`（`SetEnabledPathsForWorkspace` 第三参）。
- **安装**：`pkg/skill/service.go:262` `Install(InstallRequest{SourceRef, Skill, Name, Ref, DestScope})`；`:299` `inferSourceKind` 只认 `git_repo`、`skills_sh` 两种源（`:304-330` 分发）；`DestScope` ∈ `ScopeGlobal/ScopeWorkspace/ScopeProject`（`service.go:11-16`），安装目标目录随 scope。HTTP 入口 `api_extra.go:2402` `handleSkillsInstall`（`SourceRef` 必填 `:2435`）。**没有离线压缩包安装**。
- **创建/编辑**：`api_extra.go:2248` `handleSkillsCreate`、`:2283` `handleSkillsUpdate`——本计划从 UI 移除入口；端点保留给工坊（011）使用，**不在本计划删除**。
- **项目技能路由已存在**：`api_extra.go:2596-2642`（`handleProjectSkillsList` 等，经 `:2574` `projectSkillLaunch` 注入项目上下文）。
- **归档与路径安全设施已存在**：`pkg/home/pathguard.go`：`ValidateArchiveRelPath :36`（拒绝绝对路径与 `..` 穿越）、`ContainsTraversal :23`、`ResolveWithinRoots :51`（解析后的真实路径必须落在允许根内，含符号链接检查）；使用先例 `pkg/skill/hub.go:281`。
- **压缩能力**：Go 标准库 `archive/zip`、`archive/tar`、`compress/gzip` 可用；**rar 无标准库**，本计划明确不支持 rar（无第三方依赖），前端文件选择器只收 `.zip/.tar.gz/.tgz/.tar`，界面明示"rar 请先转成 zip"。
- **内置技能**：`pkg/skill/system.go:14` `go:embed system_assets/*`，`Install` 解压到 `<home>/skills/.system`（重复安装先清空再解压）。清单：caveman、context-restore、context-save、docx、frontend-design、image-edit、pdf、pptx、review、skill-generator、skill-workshop、xlsx。
- **前端**：**今天不存在 `frontend/src/views/SkillsView.vue`，也不存在 `/skills` 路由**——技能界面全部长在设置页里：`frontend/src/views/SettingsView.vue:29-64` 是"技能概览"卡与"Skills 管理"卡（含"创建/更新技能"表单 `settings.createOrUpdateSkill`、在线安装表单 `settings.installSkillPackage`、保存启用状态按钮），列表本身渲染在子组件 `frontend/src/components/settings/SkillsInstalledList.vue`。设置页没有"共享技能"标签（007 已留标签挂点）；项目空间 skills 标签是 008 的占位。**本计划新建 `/skills` 页面与视图文件，并把设置页里的旧技能卡拆掉**（"共享技能"进设置的新标签，"创建/更新技能"入口删除、归技能工坊）。
- README 全局规则（90-115 行）全部适用。

## 设计

### 1. 有效集合视图 API（扩展现有列表响应）

`GET /api/skills/` 与项目技能列表的响应条目在现有字段上补充（`handleSkillsListWith :2229` 与 `hub.go:67` `SkillDTO` 一侧）：
- `origin`：`project` | `agent` | `shared` | `builtin` | `cross-tool`——由 `Entry.Source` + 路径判定：`Source=project`→`project`；`Source=workspace`→`agent`；`Source=global` 且路径含 `<home>/skills/.system`→`builtin`；`Source=global` 且路径在 `SharedSkillsRoots`（`config.Summary` 的展示元数据）或 `<home>/skills`→`shared`；`Source=local`（跨工具用户目录）→`cross-tool`。
- `editable`（bool）= 该行属于当前页面的归属层（项目页=project；主代理页=agent；设置共享页=shared；内置永不）。继承行（origin 与页面归属层不同）一律只读：无启停开关、无删除按钮，UI 显示锁形图标（预览 `skills` 条）。
- `download_url`：`/api/skills/{name}/download`（见第 3 条）。

启停仍走现有 `POST /api/skills/toggle` 全量语义，前端只提交本页可编辑行的完整 enabled 集合（继承行保持发现时原值，不进入提交集的修改）。

### 2. 三个技能页

- **主代理技能页（`/skills`，菜单一级项）**：`SkillsView.vue` **新建**（今天不存在）。列表 = 有效集合（项目上下文为空）：origin 徽标（主代理/共享/内置/跨工具）、描述、遮蔽标记（`ShadowedBy` 非空时显示"被 <路径> 遮蔽"）、启停开关（仅 `agent` 行）、下载勾选框 + 下载按钮（每行）与批量下载按钮、删除按钮（仅 `agent` 行，见第 4 条）。顶部操作："在线安装"（弹层：源地址输入 + 提示"在线安装需要访问公网；内网环境请使用离线安装"）、"离线安装"（弹层：文件选择 .zip/.tar.gz/.tgz/.tar + 目标层固定"本主代理"）、说明"创建和编辑技能在技能工坊"（链接到 `/workshop`，011 交付前显示为禁用态提示文字）。
- **设置 → 共享技能标签**（007 标签数组加 `shared-skills`）：同一列表组件，数据 = 有效集合中 origin ∈ {shared, builtin} 的行 + 启停只对 `shared` 行开放（`disabled.json` 同一机制；内置行无启停）。在线/离线安装弹层目标层固定"共享技能库"（`DestScope=global`）。
- **项目空间 skills 标签**（替换 008 占位 `ProjectSkillsTab.vue`）：数据走既有项目技能路由（`api_extra.go:2596`），列表 = 项目有效集合（含 agent/shared/builtin 继承行，全部只读），只有 `project` 行可启停/删除/安装。项目不受信时列表上方显示"信任此项目"引导（与 008 MCP 标签同一状态源）。

### 3. zip 下载（单条 + 批量）

- `GET /api/skills/{name}/download`：服务端定位该技能目录（当前租户有效集合内按名查找；重名取未遮蔽那条），用 `archive/zip` 打包为 `<name>.zip` 流式返回（`Content-Disposition: attachment`）。打包固定相对根为技能目录本身，条目路径 = 目录内相对路径。
- `POST /api/skills/download` `{names: [...]}`：多选批量，一个 zip 内每技能一个顶层目录 `<name>/...`；`names` 上限 50。任一名称找不到返回 400 并列出缺失项（整体失败，不做部分成功——避免用户拿到残缺包而不自知）。
- 安全：路径都出自服务端发现的 `Entry.Path`，不接用户路径；打包前校验每个文件 `ResolveWithinRoots(entry, [skillRoot])`，符号链接条目跳过并记录在响应头 `X-Skipped-Symlinks`（计数）。

### 4. 删除技能

- `DELETE /api/skills/{name}?scope=<agent|shared|project>`：只在归属层允许（D9）；服务端按 scope 定位该层根下同名技能目录，`os.RemoveAll`；删除前校验解析后的目录确实在该层根内（`ResolveWithinRoots`）。内置技能（`.system`）永不删除（403）。项目 scope 校验项目信任与归属。删除后返回最新列表。UI 确认框明示"将从磁盘删除技能目录"。

### 5. 离线安装（压缩包上传）

- `POST /api/skills/install/upload`（`multipart/form-data`）：`file`（≤ 64 MiB，`http.MaxBytesReader`）+ `dest_scope`（`workspace`|`global`|`project`，project 需项目上下文与信任）。
- 服务端流程（新函数放 `pkg/skill/offline.go`，service 挂方法）：
  1. 按扩展名/魔数分派：`.zip`→`archive/zip`；`.tar.gz`/`.tgz`→`gzip`+`tar`；`.tar`→`tar`；其余 415。
  2. 解包前预算：总解压字节数上限 256 MiB、条目数上限 20,000、单文件 ≤ 64 MiB——任一超限整体失败并清理（防解压炸弹）。
  3. 每个条目名过 `home.ValidateArchiveRelPath`；目标根取 `dest scope` 对应技能根；写入前 `ResolveWithinRoots(目标根+条目名, [目标根])`；拒绝符号链接条目（`zip` 的 mode 与 `tar` 的 `Symlink` 类型）。
  4. 解包到临时目录（同层 `.incoming-<rand>`），校验：解包结果里存在至少一个含 `SKILL.md` 的目录；然后把每个技能目录移入目标根（同名则整体失败，提示先删除或改名——不做覆盖语义，避免误删用户改动）。
  5. `Refresh` 后返回 `{installed: [names], skills: <最新列表>}`。
- **rar 不支持**：无标准库、不引入第三方依赖；`inferSourceKind` 不动，在线安装（git_repo/skills_sh）路径零改动。

## 需要的命令

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| Go 静态检查 | `go vet ./...` | 退出码 0 |
| Go 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... ./pkg/gateway/... -count=1` | 全部 `ok` |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |
| 缓存不变式 | `git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun` | 无输出 |

## 范围

**只允许修改**：
- `pkg/skill/offline.go`（新建：压缩包解包安装）、`pkg/skill/offline_test.go`（新建，含 zip-slip/炸弹/symlink 用例）、`pkg/skill/service.go`（仅新增方法与现有方法上的 origin/download 辅助；`Install`/`Update`/`inferSourceKind` 不改）
- `pkg/gateway/skills_download.go`（新建：下载与删除 handler）、`pkg/gateway/api_extra.go`（`skills` 路由组追加注册行 + 列表响应补字段 + `:2248`/`:2283` 的 create/update 端点保留不动）
- `frontend/src/views/SkillsView.vue`（**新建**，今天不存在）、`frontend/src/components/skills/SkillsTable.vue`（新建，三页共用）、`frontend/src/components/skills/InstallDialogs.vue`（新建：在线/离线弹层）
- `frontend/src/views/SettingsView.vue`（拆掉技能概览/Skills 管理卡与创建、安装表单，只留一行标签数组改动）、删除 `frontend/src/components/settings/SkillsInstalledList.vue`（旧列表组件，被 `skills/SkillsTable.vue` 取代）
- `frontend/src/components/settings/SettingsSharedSkillsTab.vue`（新建）、`frontend/src/views/SettingsView.vue`（标签数组加一行）
- `frontend/src/components/project/ProjectSkillsTab.vue`（替换 008 占位）
- `frontend/src/App.vue`（菜单加 `skills` 项）、`frontend/src/router/index.ts`（`/skills` 一条，若 007 未留）、`frontend/src/locales/index.ts`、`frontend/src/lib/api.ts`
- `frontend/e2e/skills-lifecycle.spec.ts`（新建）

**不要碰**：
- `pkg/gateway/dist/**`；`pkg/tui/**`。
- `handleSkillsCreate`/`handleSkillsUpdate` 端点本体（011 工坊复用）；`inferSourceKind` 与在线安装实现。
- 计划 011 的工坊页面。

## 步骤

### 第 1 步：离线安装核心 + 安全测试

实现设计第 5 条（`pkg/skill/offline.go` + HTTP handler）。`offline_test.go` 必须覆盖：正常 zip/tar.gz 安装；条目名 `../evil`（zip-slip）拒绝；条目名绝对路径拒绝；zip 炸弹（高压缩比超 256 MiB）拒绝且临时目录无残留；符号链接条目拒绝；同名技能已存在时整体失败；无 SKILL.md 的包拒绝；rar/未知扩展 415。
结构参照：技能根与信任相关用例照 `pkg/skill/roots_test.go:158` `TestProjectAgentsSkillsRequiresTrust` 与 `pkg/skill/discover_test.go:27`；gateway 上传端点照 `pkg/gateway/api_extra_test.go` 里既有的 multipart 上传用法。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/skill/... -count=1` 全部 `ok`。

### 第 2 步：下载与删除端点

实现设计第 3、4 条，测试覆盖：单条 zip 内容含 SKILL.md；批量含多顶层目录；缺失名整体 400；内置技能删除 403；非归属层删除 403；解析越界拒绝。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/skill/... -count=1` 全部 `ok`；`go vet ./...` 退出码 0。

### 第 3 步：列表 origin 字段与三页 UI

实现设计第 1、2 条：新建 `/skills` 页面与 `skills/SkillsTable.vue`、`skills/InstallDialogs.vue`，设置页加"共享技能"标签（走 007 的可扩展标签数组），项目空间 skills 标签替换 008 的占位组件。最后拆掉设置页里的旧技能界面：删 `SettingsView.vue` 的技能概览卡、Skills 管理卡与"创建/更新技能""安装 skill 包"表单，删组件 `frontend/src/components/settings/SkillsInstalledList.vue`，并删掉随之无用的 `settings.createOrUpdateSkill`、`settings.installSkillPackage` 等文案键（中英两表）。

**验证**：`cd frontend && corepack pnpm test` 全部通过；`test ! -e frontend/src/components/settings/SkillsInstalledList.vue` 退出码 0；`grep -rn "SkillsInstalledList\|createOrUpdateSkill\|installSkillPackage" frontend/src` 无输出；人工核对：主代理页共享/内置行无开关，项目页继承行无开关，设置共享页内置行无开关。

### 第 4 步：真机验证

新建 `frontend/e2e/skills-lifecycle.spec.ts`：
1. `/skills`：列表含内置技能（origin=builtin，无启停开关、有下载按钮）；截图 `skills-agent.png`。
2. 离线安装：e2e 脚手架预先生成一个合法技能 zip，上传 → 列表出现（origin=agent，开关可切换）；切换开关后刷新仍保持。
3. 下载：点该行下载按钮，断言响应 `content-type` 为 zip 且文件名 `<name>.zip`；勾选两行批量下载成功。
4. 删除：删除刚安装的技能，列表消失；尝试删除内置技能被拒（界面显示禁止）。
5. 设置 → 共享技能：只显示 shared/builtin 行。
6. 项目空间 skills 标签：信任项目后，项目技能行可启停；agent/shared/builtin 行只读；截图 `skills-project.png`。
7. 在线安装弹层含公网提示文案；离线弹层文件选择器 `accept` 不含 `.rar` 且页面有"rar 请先转成 zip"提示。

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`。

## 完成标准（全部满足）

- [x] 第 1-3 步的验证全部通过
- [x] `scripts/acceptance/web_e2e.sh` 最后一行 `web e2e: PASS`（51/51，2.1 分钟）
- [x] 三个技能页都没有"创建或编辑技能"入口：`grep -rn "createOrUpdateSkill\|installSkillPackage" frontend/src` 无输出（旧键删除；工坊链接除外），且 `/skills`、设置共享技能标签、项目空间技能标签都只提供查看/启停/安装/下载/删除
- [x] 继承行在三层都只读（D9）
- [x] 安全用例（zip-slip/炸弹/symlink/越界删除）全部有自动化测试且通过
- [x] `git status --short pkg/gateway/dist` 无输出；缓存不变式检查无输出
- [x] README 状态行已更新

## STOP 条件

- "现状"摘录与代码对不上（尤其 `DiscoverForWorkspace` 根集合、`SetEnabledPaths` 全量语义、项目技能路由行为）。
- 发现启停状态需要项目维度独立的存储文件（现状 `disabled.json` 按 workspace 存、`projectRoot` 参与键）与 D9 冲突。
- 下载/删除需要改动 `pkg/safety` 信任模型。
- 需要修改"范围"之外的文件。

## 维护说明

- `origin` 分类规则集中在 gateway 一处 helper；`pkg/skill` 的 `Source` 仍是引擎事实，两者映射写在一个函数里。
- 离线安装的安全预算（大小/数量/符号链接）是产品安全边界，改动必须过安全测试。
- rar 的支持决策：等有可接受的纯 Go 依赖再议，当前明示不支持。
- 创建/编辑入口归工坊（011）；若工坊方案改变，本计划的"无创建入口"约束随之复核。
