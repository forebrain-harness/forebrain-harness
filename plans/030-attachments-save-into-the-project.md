# Plan 030：附件"保存到"——web 上传（含粘贴、拖入的截图）存进项目里用户选的目录，每个项目记住上次的目录

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> **界面 1:1 还原设计稿** `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`：在"本轮新增"里点"附件 · 保存到"和"粘贴截图"；对应原型函数 `attachModal()`、`attDirs()`、`newDirSheet()`，CSS 前缀 `.att-`。
>
> **Drift check（先跑）**：
> - 025、029 已完成：`grep -n "func (s \*ProjectStore) FindOrCreateByRoot" pkg/state/project_store.go` 有 1 行；`grep -n "project_required" pkg/gateway/api_extra.go` 有结果。
> - `grep -n "func (s \*FileStore) CreateFromReader" pkg/state/file_service.go` 有 1 行。
>
> 不符 → STOP。

## Status

- **Priority**: P2
- **Effort**: M
- **Risk**: LOW–MED（新的写文件位置，越界检查要严）
- **Depends on**: 025、029
- **Category**: feature
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11

## Why this matters

owner 2026-10-10：

> 需要设计UI/UX，让用户选择或者新建目录上传，可以直接存在项目根目录下，也可以是项目根目录下的子目录，也可以新建目录并命名后选中该目录上传

owner 随后又定了两条：
- "和文件同一套，记住上次目录"：粘贴、拖入的截图走同一套"保存到"，每个项目记住上次选的目录；
- 附件和 agent 生成的交付物都在项目目录里，不在主代理工作区（R8）。

TUI 粘贴截图存到 `<项目根>/attachments/` 在 028 里做，本计划只做 web。

## Current state（已核实）

- 上传接口 `POST /api/files`（`pkg/gateway/api_extra.go:190`；`handleFiles` 在 `:833` 一带）：multipart，必须带 `session_id`（`:839-843`），并校验会话归属（`:844`）。
- 存储：
  - `FileStore.CreateFromReader`（`pkg/state/file_service.go:74` 起）把原件写到 `<主代理工作区>/files/<sha 前两位>/<uuid><扩展名>`，`storage_key` 记这个相对路径；
  - 解析出的文本在 `<工作区>/state/...`。
- `fb_files`（`pkg/state/schema.sql:230-246`）：`storage_backend CHECK IN ('local','s3')`，没有"项目内路径"一列。
- 模型拿到的文件引用经 `FileReferenceResolver`（`pkg/run/runner.go:150`；实现用 `grep -rn "FileReferenceResolver" pkg --include='*.go' | grep -v _test` 找）。下载接口 `handleFileDownload` 读 `storage_key`（`api_extra.go:921` 一带）。
- 前端：
  - `forebrainApi.filesUpload(file, sessionId?)`（`frontend/src/lib/api.ts:2152`）；
  - `ChatView.vue:1590-1610` 在发送时逐个上传。会话还没创建时先建会话，029 之后建会话必须带项目。
- 项目行没有"上次附件目录"字段。
- 本机 `fb_files` 0 行；老的上传文件属文件型数据，不搬（owner："以后再说"）。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/gateway/... ./pkg/run/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/state/... ./pkg/gateway/...`；`gofmt -l pkg` | 无输出 |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui` | 成功 |

## Scope

**In scope**：
- `pkg/state/{schema.sql,schema_migrations.go,file_service.go,project_store.go}`；
- `pkg/gateway/api_extra.go`（上传、下载、新建目录、目录树）；
- 文件引用解析实现；
- 前端：新组件 `AttachSaveDialog.vue`，复用 029 的 `NewFolderSheet.vue`；`ChatView.vue` 的上传流程；`api.ts`；locale；
- 测试、`pkg/gateway/dist`。

**Out of scope**：
- TUI（028）；
- 老文件迁移；
- S3 后端（项目内保存只用于本地后端；配置成 S3 时保持今天的行为，见 Step 2）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：库结构（迁移 vN+1，版本号顺延）

1. `fb_files` 加列 `project_path TEXT NOT NULL DEFAULT ''`，记项目根下的相对路径（`/` 分隔）。非空时原件位于 `<项目根>/<project_path>`，`storage_key` 留空。
2. `fb_projects` 加列 `last_attach_dir TEXT NOT NULL DEFAULT ''`，记项目根下的相对目录，空串表示项目根。
3. 迁移只加列，写法照 `migrateStateV3ToV4` 的幂等检查；`schema.sql` 同步。

### Step 2：上传进项目

1. `POST /api/files` 增加表单字段 `dir`（项目根下的相对目录，可为空）。处理：
   1. 会话所属项目的根（`ProjectForSession`）；拿不到 → 400 `project_required`（主代理级工坊会话也走这里，见 STOP 2）；
   2. `dir` 拒绝绝对路径和含 `..` 的段；`target = Join(root, dir)` 经 `tool.ResolveWithinRoots(target, []string{root})` 校验仍在项目内，防符号链接逃逸，越界 → 400；
   3. 目录不存在 → 400（新建目录走 Step 3 的接口，上传不隐式建目录）；
   4. 同名文件自动加后缀：`name (1).ext`、`name (2).ext`……；
   5. 写入方式与今天一样：先写 `.uploading` 临时文件再重命名，临时文件放在目标目录里，避免跨文件系统重命名；
   6. 写 `fb_files`：`project_path` 为最终相对路径，`storage_key=''`，`sha256` 等字段照旧；
   7. 写 `fb_projects.last_attach_dir = dir`。
2. 配置为 S3 后端时：忽略 `dir`，走今天的路径。在代码注释和报告里写明。
3. `FileStore` 增加读取原件路径的方法 `OriginalPath(f File, projectRoot string) string`：`project_path` 非空用项目根拼接，否则用今天的 `filesDir()/storage_key`。下载接口、解析任务、文件引用解析器都改用它，用 `grep -rn "filesDir()\|StorageKey" pkg --include='*.go' | grep -v _test` 找全。
4. 文件引用给模型的文字：`project_path` 非空时，把**项目内相对路径**告诉模型，让模型用文件工具在项目根读到同一个文件。今天给的是什么，看解析器实现：
   - 给的是 `<files/...>` 路径 → 改成项目内路径；
   - 给的是内联内容 → 保持内联，另附一句路径。

### Step 3：目录选择需要的接口

1. `GET /api/projects/:id/dirs?path=<相对目录>`：只列子目录（名字、是否有子目录），不列文件；越界检查同 Step 2。也可以复用 022 的 `filesRoot` 加 `dirs_only=1` 参数，二选一，在报告写明。
2. `POST /api/projects/:id/dirs`，body `{parent, name}`：在项目内新建目录。名字规则同 029 的 `POST /api/host/dirs`；同名 → 409。
3. `projectRow` 增加 `last_attach_dir`。

**Verify**：gateway 测试：
- 上传到 `docs/` → 文件出现在 `<root>/docs/`，`fb_files.project_path = "docs/x.png"`，`last_attach_dir = "docs"`；
- 重名自动加 ` (1)`；
- `dir="../x"` → 400；
- 指向项目外的符号链接目录 → 400；
- 新建目录成功；重名返回 409；
- 下载接口读到项目里的原件。

### Step 4：前端"保存到"

1. `AttachSaveDialog.vue` 结构照原型 `attachModal()`：
   - 标题"保存到"；
   - 待上传文件列表：图标、文件名、大小，截图标"从剪贴板粘贴"；
   - 一行：左边"项目 <名字>"，右边"新建文件夹"按钮；
   - 目录树：只有目录，根是"项目根目录"；
     - 默认选中项目的 `last_attach_dir`，首次为根；
     - 上次的目录带"上次"标记；
     - 点目录展开（懒加载 Step 3 的接口）并选中；
   - 底栏：左边"保存到 <目录>/"，右边"取消""保存并上传"。
   - "新建文件夹"打开 `NewFolderSheet`：在当前选中目录下新建，成功后自动选中。
2. 触发时机：用户在输入框加附件（选文件、拖入、粘贴图片）的那一刻弹出，而不是发送时。
   - 确认后立即上传，附件显示在输入框附件条里；
   - 取消则不加入。
   - 还没有会话时先建会话：用 029 的 `chatSessionCreate({projectId|folder})`。还没选项目 → 先提示选项目（与 029 Step 5 第 1 点同一处理）。
3. `ChatView.vue:1590-1610` 发送时不再逐个上传，附件在加入时已上传。保持 `filesUpload` 的错误提示文案。
4. `api.ts`：`filesUpload(file, sessionId, dir)`、`projectDirs(id, path)`、`projectDirCreate(id, parent, name)`。

### Step 5：测试

- 前端：
  - `AttachSaveDialog`：默认选中上次的目录、懒加载展开、新建后自动选中、取消不上传；
  - `ChatView`：粘贴图片弹出对话框，确认后附件条出现该文件，`filesUpload` 收到 `dir`。
- `scripts/acceptance/web_e2e.sh`：
  - 加一条用例，在项目里上传到新建目录 `inbox/`，确认文件出现在磁盘上；
  - 第二次上传默认选中 `inbox`。

## Test plan

见 Step 3 Verify、Step 5。

## Done criteria

- [ ] Commands 表全部通过；`make ui` 成功
- [ ] e2e 新用例 PASS
- [ ] 与原型"附件 · 保存到""粘贴截图"两张截图并排对照，写进报告
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. 主代理级工坊会话（没有项目）也需要上传附件，而本计划没有它的保存位置：停下问 owner。建议存进技能目录下的当前草稿。
3. 文件引用解析器把原件路径写进了冻结的提示前缀（改动会破坏提示缓存）。

## Maintenance notes

- 项目内的附件就是普通文件：用户在磁盘上删了它，`fb_files` 的记录仍在，下载会 404。这是预期行为，不要为此加兜底。
- `last_attach_dir` 指向的目录被删了，对话框打开时回落到项目根。这一条属于界面状态，不算防御性补丁。
