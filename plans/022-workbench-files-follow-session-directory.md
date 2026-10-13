# Plan 022（2026-10-11 按项目模型改写）：web 工作台的"工作目录"、`@` 补全、片段与 `@` 图片都以会话所属项目的根为准

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：
> - plan 025 已完成：`grep -n "func (s \*ProjectStore) FindOrCreateByRoot" pkg/state/project_store.go` 有 1 行。没有 → STOP。
> - `grep -n "func (s \*Server) resolveWorkspacePath\|func (s \*Server) mentionRoot" pkg/gateway/*.go`：各 1 行（`api_extra.go:303`、`mention.go:13`）。
> - `grep -n "resolver := turn.WorkspaceResolver{Root: s.mentionRoot()}" pkg/gateway/run_control.go`：1 行。
>
> 不符 → STOP。

## Status

- **Priority**: P1（owner 截图报告的可见问题）
- **Effort**: M
- **Risk**: MED（文件视图的根变了，越界检查必须同样严格）
- **Depends on**: 025（会话都有项目）。与 026/027 无代码重叠，可先做
- **Category**: bug
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11（取代 2026-10-10 版）

## Why this matters

owner 截图：一个 TUI 会话在 web 打开，工作台的文件树列出的是主代理工作区（`~/.forebrain/workspace` 里的 `memories`、`skills`、`SOUL.md`……），不是会话所在的目录。树下方提示"点击文件插入 @ 引用"，插入的路径指向主代理工作区，可模型的工具在会话目录里执行，引用对不上。

owner 2026-10-11 定的项目模型（`docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md` R1、2.5）：
- 每个会话属于一个项目，工作目录 = 项目根；
- 唯一例外是主代理级技能工坊会话（`source='workshop'`、没有项目），工作目录是主代理技能目录 `<主代理工作区>/skills`。

所以文件视图的根**只有两种**：项目根，或主代理技能目录。旧版计划里"cwd → 默认出生目录 → 主代理工作区"的回落链作废。

新对话还没发出第一条消息时没有会话，但输入框左上角已经选了项目（设计稿的项目胶囊）。这时文件视图按所选项目的根显示。

## Current state（已核实）

- 文件树与片段：
  - `resolveWorkspacePath(rel)`（`pkg/gateway/api_extra.go:303` 起）根取 `s.activeWorkspaceRoot()`（主代理工作区），越界检查用 `tool.ResolveWithinRoots(joined, []string{root})`；
  - 调用方 `handleWorkspaceTree`（`:316` 起）、`handleWorkspaceSnippet`（`:469` 起）。
- `@` 补全：`mentionRoot()`（`pkg/gateway/mention.go:13-22`）同样是主代理工作区，被以下位置使用：
  - `handleMentionSearch`（`:38`）；
  - `handleMentionAccept`（`:93`）；
  - 构造 web 一轮输入时解析 `@` 图片（`pkg/gateway/run_control.go:516`）。
- 以上请求都不带会话 ID。
- 前端：
  - `forebrainApi.workspaceTree(path)`（`frontend/src/lib/api.ts:1844`）、`mentionSearch(q)`（`:1369`）、`mentionAccept(payload)`（`:1375`）都不带会话；
  - `useWorkspaceTree()`（`frontend/src/composables/useWorkspaceTree.ts:45` 起）的状态是模块级的，按主代理共享，不区分会话；
  - 使用处：`WorkspaceTree.vue:51`、`WorkspaceRow.vue:72`、`ChatView.vue:655`（一轮结束后刷新）；
  - `ForebrainPromptTextarea.vue` 调 `mentionSearch` / `mentionAccept`，被对话页、工坊页、subagent 视图复用。
- 会话所属项目：`ProjectStore.ProjectForSession`（`pkg/state/project_store.go:374` 起）；会话来源：`fb_sessions.source`。
- 以下 `activeWorkspaceRoot()` 的用法**不改**（它们确实指主代理工作区）：主代理规则文件 `handleAgentRuleFiles` / `handleAgentRuleFile`、权限路径 `api_extra.go:793`。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/gateway/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/gateway/... ./pkg/state/...`；`gofmt -l pkg/gateway pkg/state` | 无输出 |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui` | 成功 |

## Scope

**In scope**：
- `pkg/gateway/api_extra.go`（tree / snippet）、`pkg/gateway/mention.go`；
- `pkg/gateway/run_control.go`（只改 `:516` 那处 resolver 的根，以及给它传会话所需的参数）；
- 新文件 `pkg/gateway/session_files.go`（根解析）；
- 前端：`frontend/src/lib/api.ts`、`useWorkspaceTree.ts`、`WorkspaceTree.vue`、`WorkspaceRow.vue`、`ForebrainPromptTextarea.vue`，以及使用处传会话 ID / 项目 ID 的那几行；
- 相关测试；`pkg/gateway/dist`。

**Out of scope**：
- 工作台改版（"工作目录 / 变更文件"标签、树上的编辑删除，见 web 计划 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/017-*`）；
- 工具实际在哪执行（026/027）；
- TUI（它的 `@` 根就是进程目录 = 项目根）；
- `.versions/` 在树上隐藏（技能版本计划处理）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：文件视图的根只有一个解析函数

新建 `pkg/gateway/session_files.go`：

```go
// filesRoot is the root of a conversation's file view: the root of the project
// the session belongs to, or — for an agent-level skill-workshop session,
// the one session without a project — the agent's skills directory. A
// conversation not started yet names its project instead of a session.
// Every file view (tree, snippet, @ mentions, @ images) and nothing else
// resolves its root here.
func (s *Server) filesRoot(ctx context.Context, sessionID, projectID string) (string, error)
```

规则：
1. `sessionID` 非空：
   - 会话不属于本主代理（`s.sessionOwned`）→ 返回 `errFilesNotFound`；
   - 有项目 → 项目根；
   - 没有项目且 `source='workshop'` → `<主代理工作区>/skills`。主代理工作区用 `s.activeWorkspaceRoot()`，目录不存在就 `MkdirAll`；
   - 其余（迁移后不应存在）→ `errFilesNotFound`。
2. `sessionID` 为空、`projectID` 非空：项目属于本主代理就取它的根，否则返回 `errFilesNotFound`。
3. 两者都空 → `errFilesNotFound`。不再回落主代理工作区。
4. 根目录不存在 → 返回 `errFilesDirectoryMissing{Dir}`。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/gateway/...` 无输出。

### Step 2：接口按会话或项目取根

1. `resolveWorkspacePath(rel)` 改为 `resolveFilesPath(ctx, sessionID, projectID, rel)`：根取 `filesRoot`，越界检查不变。
2. `handleWorkspaceTree`、`handleWorkspaceSnippet`：读查询参数 `session_id` / `project_id`。
   - `errFilesNotFound` → 404；
   - `errFilesDirectoryMissing` → 404，正文 `目录已不存在：<dir>`。
3. `handleMentionSearch`（查询参数）、`handleMentionAccept`（请求体加可选 `session_id`、`project_id`）：根取 `filesRoot`，错误处理同上。
4. `run_control.go:516`：根改为这一轮所在会话的 `filesRoot(ctx, sid, "")`。所在函数拿不到会话 ID 就从调用方传入：先 `grep -n` 列出调用点，再逐一传参。
5. 删除 `mentionRoot()`。

**Verify**：
- `grep -n 'mentionRoot()' pkg/gateway/*.go` 无输出；
- `grep -n 'activeWorkspaceRoot()' pkg/gateway/*.go | grep -v _test` 只剩：定义处、规则文件两处、`:793`、`filesRoot` 里工坊那一处。

### Step 3：Go 测试

新增 `TestFilesRootFollowsTheSessionsProject`，HTTP 测试搭建照现有样板（`grep -n 'httptest.NewRecorder' pkg/gateway/*_test.go | head`）：

1. 准备：
   - 项目 P 的根为临时目录 A，含 `a.txt` 和子目录 `pkg/`；
   - 主代理工作区是另一个临时目录 W，含 `SOUL.md` 和 `skills/demo/SKILL.md`；
   - 会话 S1 属于 P；会话 S2 为 `source='workshop'`、没有项目。
2. 断言：
   - `GET /api/workspace/tree?session_id=S1` 列出 `a.txt`、`pkg`，不含 `SOUL.md`；
   - `...&path=../` → 400（越界）；
   - `GET /api/workspace/tree?project_id=P`（无会话）结果同上；
   - `GET /api/workspace/tree?session_id=S2` 列出 `demo`；
   - `GET /api/workspace/mentions?session_id=S1&q=a.t` 的候选含 `a.txt`；
   - 删除 A 后再请求 tree → 404，正文含"目录已不存在"；
   - 什么参数都不带 → 404；
   - 会话属于另一个主代理 → 404。

### Step 4：前端带上会话或项目

1. `api.ts`：`workspaceTree(path, scope)`、`mentionSearch(q, scope)`、`mentionAccept({..., ...scope})`。`scope` 是 `{ sessionId?: string; projectId?: string }`，按该文件现有写法转成 `session_id` / `project_id`。
2. `useWorkspaceTree.ts`：
   - 状态按"根的身份"分开：模块级的 `dirs` / `expanded` 改为 `Map<key, {...}>`，`key` 为 `s:<会话 id>` 或 `p:<项目 id>`；
   - 函数签名改为 `useWorkspaceTree(scope: Ref<{sessionId?: string; projectId?: string}>)`；
   - 切换主代理时整个 Map 清空，保留今天的语义；
   - 新对话发出第一条消息、拿到会话 ID 之后，`p:<项目>` 那份状态迁到 `s:<会话>`，避免树闪一下。
3. `WorkspaceTree.vue` / `WorkspaceRow.vue` 接收 `scope` prop。`ChatView.vue`：
   - 有会话时传会话 ID；
   - 新对话传输入框里选中的项目 ID（项目胶囊的状态；若 029 还没做，就传当前会话所属项目，没有就不渲染树）。
4. `ForebrainPromptTextarea.vue` 加可选 prop `scope`，传给补全请求。使用处传值：
   - ChatView：同上；
   - WorkshopView：工坊会话 ID；
   - subagent 视图：父会话 ID。

**Verify**：`cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` 全绿 / 无输出。

### Step 5：前端测试

- `useWorkspaceTree.test.ts`（没有就新建）：
  - 两个会话各自加载，互不覆盖；
  - 切换主代理会清空；
  - 新对话的 `p:` 状态在拿到会话 ID 后迁到 `s:`；
  - 用 `vi.spyOn(forebrainApi, 'workspaceTree')` 断言请求带了正确的参数。
- `ForebrainPromptTextarea` 的既有测试（`grep -rln 'mentionSearch' frontend/src --include=*.test.ts`）补一条：传入 `scope` 时 `mentionSearch` 收到它。

### Step 6：构建与真机

1. `make ui`。
2. 起 gateway（隔离 home，写法照 `plans/008` 末尾的验收记录），在 FOREBRAIN_HOME 外的目录 D 建项目和会话。
3. 浏览器里打开该会话的工作台，文件树是 D 的内容，不含 `SOUL.md` / `memories`；`@` 补全候选来自 D。
4. 结束后停进程、删临时目录。

## Test plan

见 Step 3、5、6。

## Done criteria

- [ ] Commands 表全部通过；`make ui` 成功
- [ ] `TestFilesRootFollowsTheSessionsProject` 与前端用例 PASS
- [ ] Step 6 真机：树与 `@` 补全都来自项目根
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. `run_control.go:516` 所在函数的调用链拿不到会话 ID（例如在通道或定时任务路径里被调用），而且需要改动的文件超出 Scope。
3. 现有测试断言"文件树是主代理工作区"且注明是 owner 的设计决定。

## Maintenance notes

- 新增任何"会话的文件"类接口，根一律走 `filesRoot`，不要再写 `activeWorkspaceRoot()`。
- web 计划 017 在这套根之上加文件的新建、改名、删除、写入。新接口的越界检查与本计划的写法相同。
