# 计划 006：工作台的"工作区文件"改为懒加载目录树

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新 README 中本计划的状态行。**不要提交代码。**
>
> **漂移检查（先运行）**：
> `git diff --stat b92a1b1..HEAD -- pkg/gateway/api_extra.go pkg/gateway/notification_hook_test.go pkg/gateway/agents_api.go`
> 计划 001 会在 `api_extra.go` 的 `AttachExtraRoutes` 里加登录路由，那是预期的；`handleWorkspaceTree`、`handleWorkspaceSnippet` 若有变化，先与"现状"摘录核对。
> 前端部分依赖计划 005 新建的 `ChatWorkbench.vue`，先通读它的当前版本。

## 状态

- **优先级**：P1
- **工作量**：M
- **风险**：LOW（接口形状变化只有工作台一个调用方）
- **依赖**：计划 005（`ChatWorkbench.vue` 与默认收起的工作台）
- **类别**：direction / perf / security
- **基线**：提交 `b92a1b1`，2026-09-29

## 为什么要做

owner 原话："工作区文件改成目录树，默认展示第一层的目录和文件，点击目录节点展开，懒加载"。

现在的实现是**每次请求都递归遍历整个工作区**（`filepath.WalkDir`，只跳过 `.git`，`node_modules`、构建产物全在内），把成千上万条路径一次性返回，
前端再平铺成一个长列表；结果放在一个进程级的全局变量里缓存 2 秒。大仓库里这既慢又没法用。改成按目录列出一层、点开再取，
第一次打开工作台只取根目录一层，工作台收起时一个请求都不发。

顺带修复两处同类问题：读取片段的 `/api/workspace/snippet` 只做了字面上的路径检查，工作区里一个指向外部的符号链接就能让它读到工作区之外的文件；
现有测试 `TestGatewayWorkspaceAPIsUseActivePrimaryWorkspace` 对目录树的断言在两个工作区里都成立（两边都有 `note.md`），测不出"用的是当前主代理的工作区"。

## 现状

- `pkg/gateway/api_extra.go:34-41`：进程级缓存

  ```go
  type workspaceTreeCache struct {
  	mu      sync.Mutex
  	expires time.Time
  	root    string
  	records []map[string]any
  }

  var wsTreeCache workspaceTreeCache
  ```
- `pkg/gateway/api_extra.go:78-82`：`workspace := api.Group("/workspace")`，`workspace.Get("/tree", s.handleWorkspaceTree)`、`workspace.Get("/snippet", s.handleWorkspaceSnippet)` 等。
- `pkg/gateway/api_extra.go:242-293` `handleWorkspaceTree`：取 `s.activeWorkspaceRoot()`，命中缓存直接返回；否则 `_ = filepath.WalkDir(root, ...)` 递归收集 `{path, is_dir}`（跳过 `.git/`，忽略遍历错误），写缓存（2 秒），返回 `{"records": [...]}`。
- `pkg/gateway/api_extra.go:372-387` `handleWorkspaceSnippet` 的路径检查：

  ```go
  	root := s.activeWorkspaceRoot()
  	full := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
  	if rr, err := filepath.Rel(root, full); err != nil || strings.HasPrefix(rr, "..") {
  		http.Error(w, "invalid path", http.StatusBadRequest)
  		return
  	}
  	raw, err := os.ReadFile(full)
  ```
  只看字面路径，不解析符号链接；`HasPrefix(rr, "..")` 还会误拒名字以 `..` 开头的合法文件（例如 `..env.example`）。
- `pkg/gateway/agents_api.go:38-46` `activeWorkspaceRoot()`：当前主代理的工作区根目录。
- 可复用的路径约束：`pkg/tool/state.go:2478` `tool.ResolveWithinRoots(p string, roots []string) (string, error)`（转调 `pkg/home/pathguard.go:51`），
  会解析符号链接并确认真实路径仍在根目录内，越界时返回 `tool.ErrPathNotAllowed`。`pkg/gateway` 已经 import `pkg/tool`（`api_extra.go` 的 import 列表），**不要**新增对 `pkg/home` 的依赖。
- `pkg/gateway/notification_hook_test.go:212-234` `TestGatewayWorkspaceAPIsUseActivePrimaryWorkspace`：两个工作区各有一个 `note.md`，对目录树只断言 `"path":"note.md"`。
- 前端：`frontend/src/lib/api.ts:707-714` `WorkspaceTreeNode { path; isDir }`、`WorkspaceTreeResponse { records }`；`:1249-1253` `workspaceTree()` 无参数。
  计划 005 之后，工作台组件 `frontend/src/components/chat/ChatWorkbench.vue` 以 `workspaceTree`、`workspaceTreeLoading` 两个 props 平铺渲染，文件行点击发出 `insert-ref`；
  `ChatView.vue` 里的 `loadWorkspaceTree()`（原 `:699-709`）在 `onMounted` 时调用。

## 设计

### 接口：`GET /api/workspace/tree?path=<相对目录>`

- `path` 可省略，缺省为工作区根目录；使用 `/` 分隔的相对路径。
- 解析：`full := filepath.Join(root, filepath.FromSlash(path))`，交给 `tool.ResolveWithinRoots(full, []string{root})`；
  返回 `tool.ErrPathNotAllowed` → 400，正文 `path is outside the workspace`（`http.Error`）。
- `os.Stat` 不存在 → 404，正文为错误原文；不是目录 → 400，正文 `not a directory`；`os.ReadDir` 出错 → 500，正文为错误原文。
- **只列这一层**，每项 `{"name": "<名称>", "path": "<相对工作区根的路径，/ 分隔>", "is_dir": <bool>}`：
  - 跳过名为 `.git` 的项（保持现有行为）；
  - 符号链接用 `os.Stat` 跟随后判断类型，并用 `tool.ResolveWithinRoots` 检查目标；目标在工作区外或已失效的链接**不列出**——目录树只展示工作区接口真能打开的东西；
  - 排序：目录在前、文件在后；同类按 `strings.ToLower(name)` 升序，相同时再按 `name` 原样升序（结果完全确定）。
- 响应：`{"path": "<规范化后的相对路径，根为空串>", "records": [...]}`，`records` 为空时返回 `[]` 而不是 `null`。
- **删除** `workspaceTreeCache`、`wsTreeCache` 与递归遍历：读一层目录很便宜，不需要缓存。

### 片段接口

`handleWorkspaceSnippet` 的路径检查改为同一个 `tool.ResolveWithinRoots(full, []string{root})`，越界返回 400 `path is outside the workspace`；其余逻辑不变。
两个处理函数共用一个小函数 `func (s *Server) resolveWorkspacePath(rel string) (root, full string, err error)`，写在 `api_extra.go`。

### 前端数据层

- `api.ts`：`WorkspaceTreeNode { name: string; path: string; isDir: boolean }`；`WorkspaceTreeResponse { path: string; records: WorkspaceTreeNode[] }`；
  `workspaceTree(path = '')`：`path` 非空时以查询参数 `path` 传递。
- 新文件 `frontend/src/composables/useWorkspaceTree.ts`（模块级单例，写法仿照 `usePrimaryAgents.ts`）：
  - 状态：`dirs: Map<string, { status: 'loading' | 'loaded' | 'error'; children: WorkspaceTreeNode[]; error: string }>`（键为目录路径，根为 `''`）、`expanded: Set<string>`；
  - `ensureRoot()`：根目录尚未加载时加载一次；
  - `toggle(path)`：展开/收起；展开时若该目录从未加载则加载，**已加载过的不重复请求**；
  - `refresh()`：重新加载根目录与所有已展开目录，保留展开状态；某个已展开目录返回 404 时把它从 `expanded` 移除；
  - `reset()`：清空全部状态；监听 `usePrimaryAgents()` 的 `refreshToken`，主代理切换时调用（目录属于租户）。
  - 状态放在模块里，工作台收起再打开时展开状态保留、不重新请求。

### 前端组件 `frontend/src/components/chat/WorkspaceTree.vue`

- 挂载时调用 `ensureRoot()`——工作台默认收起、组件不挂载，所以**页面加载时不发任何目录请求**，第一次打开工作台才取根目录一层。
- 结构：`<ul role="tree" :aria-label="t('chat.workspaceFiles')">`，每个节点 `<li role="treeitem" :aria-level :aria-expanded(目录才有)>`，子节点放在 `<ul role="group">`。
- 一行：左缩进 `8px + 深度 × 14px`；目录显示 `ChevronRight`（展开时旋转 90°）与 `Folder` / `FolderOpen` 图标，文件显示 `File` 图标；名称单行省略，`title` 为完整路径；
  悬停 `--forebrain-bg-alt`，键盘焦点行 1px `--forebrain-focus-border` 描边。
- 点击目录行：`toggle(path)`。点击文件行：发出 `insert-ref`（沿用现在"点击文件插入 @path 引用"的行为）。
- 子目录加载中：在其下方显示一行"加载中…"；加载失败：显示服务端原文与"重试"按钮（重试即重新加载该目录）；已加载但为空：显示一行"空目录"。
- 键盘（roving tabindex，只有一行 `tabindex="0"`）：`↑` / `↓` 在可见行间移动；`→` 在收起的目录上展开、在展开的目录上移到第一个子项；
  `←` 在展开的目录上收起、否则移到父目录；`Enter` / `Space` 在目录上开合、在文件上插入引用。
- 小标题行：`chat.workspaceFiles`（计划 005 已改为"工作区文件"）+ 右侧刷新图标按钮（`RotateCw`，`aria-label` 为 `workspaceTree.refresh`），点击调用 `refresh()`。
- 一轮对话结束时（`ChatView` 里已有的 `watch(isStreaming, ...)` 由真变假时）调用 `refresh()`，但只在根目录已加载过时才执行（工作台从未打开过就什么也不做）。
- 很大的目录（例如 `node_modules`）展开后完整列出，工作台区域自身滚动，不截断。
- 文案键（中英两表）：`workspaceTree.refresh`（刷新 / Refresh）、`workspaceTree.loading`（加载中… / Loading…）、`workspaceTree.emptyDir`（空目录 / Empty folder）、`workspaceTree.retry`（重试 / Retry）。

### 接线

- `ChatWorkbench.vue`：删除 `workspaceTree`、`workspaceTreeLoading` 两个 props 和平铺列表，改为渲染 `<WorkspaceTree @insert-ref="...">`，事件原样向上转发。
- `ChatView.vue`：删除 `WorkspaceNode` 类型、`workspaceTree`、`workspaceTreeLoading`、`loadWorkspaceTree()` 及其在 `onMounted` 中的调用；在流式结束的 `watch` 里加上 `refresh()`（见上）。

## 需要的命令

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| Go 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` | `ok` |
| Go 静态检查 | `go vet ./pkg/gateway/...` | 退出码 0 |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |

## 范围

**只允许修改**：
- `pkg/gateway/api_extra.go`、`pkg/gateway/api_extra_test.go`、`pkg/gateway/notification_hook_test.go`（仅 `TestGatewayWorkspaceAPIsUseActivePrimaryWorkspace`）
- `frontend/src/lib/api.ts`（目录树类型与方法）
- `frontend/src/composables/useWorkspaceTree.ts`、`frontend/src/composables/useWorkspaceTree.test.ts`（新建）
- `frontend/src/components/chat/WorkspaceTree.vue`、`frontend/src/components/chat/WorkspaceTree.test.ts`（新建）
- `frontend/src/components/chat/ChatWorkbench.vue`、`frontend/src/views/ChatView.vue`（仅目录树相关部分）、`frontend/src/locales/index.ts`
- `frontend/e2e/workspace-tree.spec.ts`（新建）

**不要碰**：
- `/api/workspace/mentions` 与 `@` 引用引擎（`pkg/turn` 的提及搜索）：它有自己的遍历与排序规则，不属于本计划。
- `pkg/home/pathguard.go`、`pkg/tool/state.go`：只调用，不修改。
- `.git` 的权限语义：这里只是目录树不显示它，与权限判断无关。

## 步骤

### 第 1 步：先写失败的真机用例

新建 `frontend/e2e/workspace-tree.spec.ts`。`signIn` 后先 `GET /api/agents/primary` 取当前主代理的 `workspaceRoot`，用 Node 的 `fs` 在其中建立测试夹具（用例结束时删除）：
`e2e-tree/docs/guide/intro.md`、`e2e-tree/docs/readme.md`、`e2e-tree/src/main.go`、`e2e-tree/top.txt`，以及符号链接 `e2e-tree/outside` → `os.tmpdir()`。
为了让断言只看夹具，用例通过展开 `e2e-tree` 目录进入测试区域。用例：

1. `no tree request until the workbench opens`：进入 `/` 并等待 `networkidle`，期间没有任何 `/api/workspace/tree` 请求；
2. `first open lists one level`：打开工作台 → 恰好发出 1 个不带 `path` 的 `/api/workspace/tree` 请求；树中出现 `e2e-tree`，不出现 `.git`；
3. `expanding loads once`：点击 `e2e-tree` → 发出 `?path=e2e-tree` 请求，子项依次为 `docs`、`src`、`top.txt`（目录在前），不出现 `outside`，也不出现 `intro.md`；
   收起再展开 `e2e-tree` → 没有新的请求；点击 `docs` → 出现 `guide`、`readme.md`；点击 `guide` → 出现 `intro.md`；
4. `file click inserts a reference`：点击 `top.txt` → 对话输入框中出现 `@e2e-tree/top.txt`；
5. `keyboard`：焦点在 `docs` 上按 `←` → `docs` 收起（`aria-expanded="false"`）；按 `→` → 展开；
6. `state survives closing the workbench`：收起工作台再打开 → `docs` 仍展开，且没有新的 `/api/workspace/tree` 请求；
7. 截图 `workspace-tree.png`（工作台打开、`e2e-tree/docs/guide` 展开）。

**验证**：`scripts/acceptance/web_e2e.sh` → 失败，失败断言来自 `workspace-tree.spec.ts`。

### 第 2 步：后端接口

按"接口"与"片段接口"两节实现，删除缓存与递归遍历。改写 `TestGatewayWorkspaceAPIsUseActivePrimaryWorkspace` 的目录树部分：
主工作区放 `main-only.md`，审阅工作区放 `review-only.md` 与 `docs/a.md`；根目录请求包含 `review-only.md` 与目录 `docs`，**不含** `main-only.md`，也**不含** `docs/a.md`；`?path=docs` 包含 `docs/a.md`。
在 `api_extra_test.go` 新增：

- `TestWorkspaceTreeListsOneLevelDirectoriesFirst`：`b.txt`、`A.txt`、`zdir/`、`adir/`、`.git/` → 顺序为 `adir`、`zdir`、`A.txt`、`b.txt`，不含 `.git`；
- `TestWorkspaceTreeRejectsPathsOutsideWorkspace`：`?path=../` → 400；
- `TestWorkspaceTreeHidesSymlinksLeavingWorkspace`：工作区内 `out` → 临时目录外部，列表不含 `out`，`?path=out` → 400；工作区内 `in` → 工作区内的目录，列表含 `in` 且 `is_dir` 为真；
- `TestWorkspaceTreeMissingAndFilePaths`：`?path=missing` → 404；`?path=A.txt` → 400；
- `TestWorkspaceTreeEmptyDirectory`：空目录 → `"records":[]`；
- `TestWorkspaceSnippetRejectsSymlinkEscape`：工作区内 `leak.txt` → 工作区外的文件，`/api/workspace/snippet?path=leak.txt` → 400；名为 `..notes.md` 的普通文件可以正常读取。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/ -run 'WorkspaceTree|WorkspaceSnippet|UseActivePrimaryWorkspace' -count=1` → `ok`；
`grep -n "wsTreeCache\|workspaceTreeCache\|WalkDir" pkg/gateway/api_extra.go` → 无输出。

### 第 3 步：前端数据层与组件

按"前端数据层""前端组件""接线"三节实现。单测：

- `useWorkspaceTree.test.ts`（mock `forebrainApi.workspaceTree`）：`ensureRoot()` 只请求一次；`toggle('docs')` 第一次请求 `docs`、收起再展开不再请求；`refresh()` 重新请求根与所有已展开目录；
  `refresh()` 时某目录返回 404 则从 `expanded` 移除；`reset()` 后状态清空；
- `WorkspaceTree.test.ts`（结构参照 `frontend/src/components/DiffView.test.ts`）：渲染根目录一层；点击目录展开并显示子项；点击文件发出 `insert-ref` 且参数为完整路径；
  子目录请求失败时显示错误原文与"重试"；`→` / `←` 键开合。

**验证**：`cd frontend && corepack pnpm test` → 全部通过；`grep -n "loadWorkspaceTree\|workspaceTreeLoading" frontend/src/views/ChatView.vue frontend/src/components/chat/ChatWorkbench.vue` → 无输出。

### 第 4 步：真机验证

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`；截图目录中有 `workspace-tree.png`，路径写进 README 本计划状态说明。

## 完成标准（全部满足）

- [x] gateway `ok`（2.9s）；`go vet ./...` 退出码 0
- [x] 无输出（缓存与递归遍历已删，agents_api 的缓存失效代码一并移除）
- [x] 186/186 通过（新增 useWorkspaceTree 5 + WorkspaceTree 5）
- [x] `web e2e: PASS`（25/25，含 workspace-tree 6 用例；截图 `workspace-tree.png`）
- [x] dist 与提示缓存检查无输出；graph.json 经 `scripts/package-graph.sh` 重新生成（diff 仅 LOC 计数 ±5 行，fan_in/fan_out 零变化，无新增包依赖）
- [x] README 状态行已更新

## STOP 条件

- "现状"摘录与代码对不上。
- 除工作台之外还有别的调用方依赖 `/api/workspace/tree` 的旧响应形状（全量平铺列表）——例如某个页面、脚本或文档。
- `tool.ResolveWithinRoots` 对工作区根目录本身（`path` 为空）返回错误，或在 macOS 的 `/var` → `/private/var` 这类系统级符号链接下误判越界。
- 需要修改"范围"之外的文件，或需要让 `pkg/gateway` 新增包依赖。

## 维护说明

- 目录树接口只列一层，任何"一次返回整棵树"的需求都应重新评估：大仓库里整树遍历的代价就是本计划要去掉的问题。
- 工作区相关接口（目录树、片段，以及以后新增的）都必须通过 `resolveWorkspacePath` 解析路径，不要再手写字面路径检查。
- 目录树状态按主代理隔离：新增会改变工作区根目录的操作时，要调用 `useWorkspaceTree().reset()`。
