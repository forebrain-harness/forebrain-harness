# Plan 029：web 对话前必须选项目或文件夹——新对话页、项目胶囊与弹层、系统式文件夹对话框、信任提示，后端只接受带项目的会话

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> **界面必须 1:1 还原设计稿** `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`（owner 已确认的原型，https://claude.ai/artifact/KKfPW8dXWScuRdTzonsk2F ）：
> 用浏览器打开本地文件，页面顶部"本轮新增"里依次点：
> - "新对话 · 选项目弹层"
> - "新对话 · 选了新文件夹"
> - "浏览文件夹"
> - "信任提示"
>
> 照它的结构、文案、尺寸、颜色写组件。原型 HTML 里对应的函数是 `pjPill()`、`pjPop()`、`fbModal()`、`newDirSheet()`、`trustModal()`、`chat()` 的 `fresh` 分支和 `starters`，CSS 类前缀是 `.pj-`、`.fb-`、`.nc`、`.starter`、`.sheet`。
> 原型里的颜色、间距写在 CSS 变量里，前端已有同名或等价的设计 token（`frontend/src/assets/main.css`），按 token 对应，不要写死色值。
>
> **Drift check（先跑）**：
> - plan 025、027 已完成：`grep -n "func (s \*ProjectStore) FindOrCreateByRoot\|func ValidateProjectRoot" pkg/state/*.go` 有 2 行；`grep -n "session_has_no_project" pkg/gateway/*.go` 有结果。
> - `grep -n "func (s \*Server) handleChatSessionCreate" pkg/gateway/api_extra.go` 有 1 行。
>
> 不符 → STOP。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（新增本机目录浏览接口；改变新会话的创建方式）
- **Depends on**: 025、027（028 不依赖，可并行）
- **Category**: feature
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11

## Why this matters

owner 2026-10-11：

> 对话界面对话前必须选择一个项目或者文件夹，如果选择的文件夹自己没有关联的项目，则自动创建以该文件夹为根目录的项目

另外几条：
- 新对话页参考 ZCode 的布局：问候语、居中输入框、快捷入口，左上角项目胶囊默认选中上次用的项目；
- 文件夹选择要系统原生式交互，包括"返回上一层"；
- 未判断过信任的目录，发送前问一次，与 TUI 同一句。

设计文档：`docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md` R3、2.5、2.9。

## Current state（已核实）

- `POST /api/chat/sessions`（`pkg/gateway/api_extra.go:105`、`handleChatSessionCreate` `:996` 起）：
  - 只收 `title`、`source`（`''` 或 `'workshop'`）；
  - 建的会话**不带项目**，`cwd` 来自进程默认值（027 之后 gateway 默认值为空）。
- 项目会话：`POST /api/projects/:id/sessions`（`handleProjectSessionsCreate` `:5022` 起），出生目录为项目根（`:5043`）。
- 前端：
  - `forebrainApi.chatSessionCreate(title?, source?)`（`frontend/src/lib/api.ts:1311`）；
  - 调用方：
    - `useChatSessions.ts:50`；
    - `ChatView.vue:1601`、`:1672`：首条消息、上传附件时懒创建会话；
    - `WorkshopView.vue:258`。
- 新对话的空状态：`ConversationEmptyState`，标题是 `chat.emptyTitle`（"输入您的问题"，`ChatView.vue:99`）。没有项目选择、没有快捷入口。
- 信任：
  - `safety.TrustLevel(home, project)`（`pkg/safety/engine.go:1011` 一带）返回已信任 / 不信任 / 未判断；
  - `safety.MarkTrusted`（`:1044`）；
  - 写"不信任"的函数用 `grep -n "func Mark" pkg/safety/engine.go` 查。没有就在本计划里加 `MarkUntrusted`，写进 `UntrustedDirectories`。
- 根校验：`state.ValidateProjectRoot`（028 Step 1 下沉；028 还没做就先在本计划做 028 的 Step 1）。找或建项目：`ProjectStore.FindOrCreateByRoot`（025）。
- 浏览器拿不到本机绝对路径，今天没有列目录的接口。项目创建表单只有手填路径（`frontend/src/views/ProjectsView.vue:48`）。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/state/... ./pkg/safety/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/gateway/...`；`gofmt -l pkg` | 无输出 |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui` | 成功 |
| web 验收 | `scripts/acceptance/web_e2e.sh` | 全部 PASS |

## Scope

**In scope**：
- gateway 新接口与 `handleChatSessionCreate`；
- `pkg/safety`（必要时加 `MarkUntrusted`）；
- 前端：
  - 新组件 `ProjectPill.vue`、`ProjectPopover.vue`、`FolderDialog.vue`、`NewFolderSheet.vue`、`TrustDialog.vue`（放 `frontend/src/components/chat/`，命名可按仓库习惯调整）；
  - `ChatView.vue` 的空状态与首条消息流程；
  - `useChatSessions.ts`、`api.ts`；
  - locale；
- 新建项目表单的"浏览…"改用同一个文件夹对话框；
- 测试、`pkg/gateway/dist`、`scripts/acceptance/web_e2e.sh` 的新用例。

**Out of scope**：
- 输入框的"＋"菜单、模式、推理强度（web 计划 016）；
- 附件"保存到"（030）；
- 工作台（022、web 017）；
- TUI（028）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：后端——列目录与新建文件夹

新文件 `pkg/gateway/host_dirs.go`，需鉴权（与其他 `/api` 路由同一中间件）：

1. `GET /api/host/dirs?path=<绝对路径>`：
   - `path` 为空时取用户主目录（`os.UserHomeDir()`）；
   - 规范化（`safety.CanonicalPath`）；必须是存在的目录，否则 404；
   - 只列**子目录**，不列文件。每项包含：
     - `name`；
     - `mtime`：秒；
     - `project`：该目录恰好是本主代理某个项目的根时给出 `{id, name}`，按精确根匹配；
     - `blocked`：该目录是 FOREBRAIN_HOME 或在它里面时为 `true`，原型里显示为"不可选"的锁形行；
   - 返回体还带：
     - `path`：规范化后的路径；
     - `parent`：到文件系统根为止，根的 `parent` 为空；
     - `ancestors`：从当前目录到根的路径列表，供"当前位置"下拉使用；
     - `places`：侧栏的固定位置，即主目录、桌面、文稿、下载，只给存在的；
   - 隐藏以 `.` 开头的目录：原型里只有 `.forebrain` 以锁形出现，其他点目录不显示。`blocked` 的那一行保留显示。
2. `POST /api/host/dirs`，body `{parent, name}`：
   - `name` 去空白后不能为空，不能含 `/`，不能是 `.` 或 `..`；
   - `parent` 不能在 FOREBRAIN_HOME 内；
   - 已存在 → 409 `{"error":"exists"}`，原型显示"已有同名文件夹"；
   - 成功 → 201 `{path}`。
3. 路由加进 `api_extra.go` 的路由表，`pkg/gateway/api_extra_test.go` 的路由镜像同步。

**Verify**：gateway 测试 `TestHostDirsListsOnlyDirectories`。在临时目录造：子目录 `a`、`b`、点目录 `.x`、文件 `f.txt`，另把一个子目录设为 FOREBRAIN_HOME。断言：
- 只列 `a`、`b` 和 blocked 的 home 目录；
- 某个子目录已是项目时，带 `project`；
- `POST` 新建成功；同名返回 409；在 home 内新建返回 400。

### Step 2：后端——会话只能带着项目创建

1. `handleChatSessionCreate` 的 body 增加 `project_id`、`folder` 两个字段，二选一：
   - `project_id`：必须属于本主代理，根必须存在（不存在 → 409 `{"error":"session_directory_missing"}`）；
   - `folder`：
     - 先 `state.ValidateProjectRoot`；
     - 再 `FindOrCreateByRoot`：名字取目录名，其余字段取默认值，与 TUI 同口径；
     - 新建的项目带回 `"created": true`。
   - 都没有：
     - `source='workshop'` → 照旧（主代理级工坊会话，R1 的唯一例外）；
     - 其他 → 400 `{"error":"project_required"}`。
2. 新会话用 028 Step 2 的 `SessionBirth{Cwd: 项目根, ProjectID: id}` 一次写好项目与目录。若 028 还没做 → 先 `EnsureAt` 再 `ProjectStore.BindSession`，与今天 `handleProjectSessionsCreate` 的写法一致（`api_extra.go:5022` 起），并在报告注明。
3. 返回体增加 `project`（`projectRow`）。
4. `handleProjectSessionsCreate` 保留（项目空间里用），改为与上面共用同一个内部函数。

**Verify**：gateway 测试：
- 不带项目 → 400；
- 带 `folder`（新目录）→ 建出项目和会话，会话 `cwd` 为该目录；
- 同一 `folder` 再建一次 → 复用同一个项目；
- `source='workshop'` 不带项目 → 成功。

### Step 3：后端——信任

1. `GET /api/host/trust?path=<绝对路径>` → `{"level":"trusted"|"untrusted"|"unknown"}`（`safety.TrustLevel`）。
2. `POST /api/host/trust` body `{path, trusted: bool}` → `MarkTrusted` 或 `MarkUntrusted`。
3. 项目列表行（`projectRow`）增加 `trust_level`、`missing`（根目录不存在）两个字段，供弹层显示"未判断信任""目录不存在"标记。

**Verify**：信任三态读写测试。

### Step 4：前端——新对话页

1. **空状态替换**：会话还没发出第一条消息时（今天的 `ConversationEmptyState` 分支）：
   - 改为原型 `chat()` 的 `fresh` 分支：居中区 `.nc`，问候语 `<h2>有什么可以帮你？</h2>`；
   - 下面是输入框，项目胶囊在输入框内左上角；
   - 再下面是快捷入口 `starters`，四个，文案与图标照原型 `var starters`（`app-preview.html:2275`）。
   - 原型里 `.nc` 的布局是底对齐（`justify-content: flex-end; padding-bottom: 96px`），目的是让弹层向上展开不被裁切，照抄。
2. **快捷入口的行为**（照原型的 `data-starter` 处理）：
   - "生成 FOREBRAIN.md" → 把 `/init` 填进输入框；
   - "先出计划再动手" → 切到计划模式。模式选择器是 web 016 的内容；016 还没做就只填 `/plan `；
   - "做一个技能" → 打开所选项目的技能工坊（新建）。工坊页按 web 020 还原前，跳到今天的工坊入口；
   - "设一个定时任务" → 打开所选项目空间的定时任务标签并弹出新建弹窗。035 之前跳到项目空间。
3. **项目胶囊 `ProjectPill`**：
   - 默认选中"上次用的项目"：本主代理最近更新的、属于某个项目的会话所在的项目。新接口不必加，在前端从会话列表取，没有就显示"选择项目"；
   - 点开弹层 `ProjectPopover`，结构照 `pjPop()`：
     - 搜索框；
     - 项目列表：上次用的排第一；目录不存在的项目禁用并标"目录不存在"；未判断信任的标"未判断信任"；当前选中的打勾；
     - 底部"打开文件夹…"。
   - 弹层**向上展开**。
4. **文件夹对话框 `FolderDialog`**：结构照 `fbModal()`，包括：
   - 后退 / 前进 / 上一层（⌫ 快捷键）按钮；
   - 当前位置下拉（`ancestors`）；
   - 搜索框（只在当前列表里筛选）；
   - 左侧"位置"（`places`）与"项目"（本主代理项目）；
   - 列表两列：名称、修改日期。是项目的目录带"项目"标记；`blocked` 的行显示锁图标和"不可选"、不可点；
   - 单击选中，双击或回车进入；
   - 底栏：左边"新建文件夹"，中间显示选中路径，右边"取消""选择"。
   - "新建文件夹"打开 `NewFolderSheet`（照 `newDirSheet()`）：输入名字、显示所在路径、"取消""创建"；同名时显示"已有同名文件夹"。创建后自动选中新目录。
5. **选了文件夹之后**：
   - 文件夹已是项目的根 → 胶囊显示该项目；
   - 否则胶囊显示文件夹名和"新项目"标记。
   - 这一步**不**建项目，发第一条消息时才建（Step 5）。
6. **信任提示 `TrustDialog`**：结构和文案照 `trustModal()`：
   - 标题"信任这个文件夹吗？"；
   - 说明句；
   - 路径、项目（"将创建「名字」"或已有项目名）；
   - 提示"只问一次，终端与网页共用。"；
   - 按钮"取消""信任并继续"。
7. 去掉"不在项目中工作"的入口（如有）。对话抽屉里的"新聊天"进入新对话页，并沿用上次的项目。

### Step 5：前端——发第一条消息

`ChatView.vue:1601`、`:1672` 的懒创建改为：

1. 没有选项目或文件夹 → 不发送。胶囊抖动一次（原型无此效果，就用 `aria-invalid` 加焦点到胶囊），输入框保留内容。
2. 选中的目录 `trust_level == 'unknown'` → 先弹信任提示：
   - "信任并继续" → `POST /api/host/trust` 后继续；
   - "取消" → 不发送，内容保留。
3. `chatSessionCreate({projectId} | {folder})` 建会话。返回 `created: true` 时，在消息流顶部显示一行 `.sys-line`：图标 + "已创建项目 <名字>"，照原型 `newThread()`。
4. 收到 409 `session_directory_missing` → 用今天的错误提示组件显示"这个对话所在的目录已不存在：{dir}。无法继续。"。
5. `api.ts` 的 `chatSessionCreate` 改签名：`chatSessionCreate(opts: {title?, source?, projectId?, folder?})`。所有调用方都要改，`WorkshopView.vue:258` 的工坊入口传项目，主代理级工坊不传。

### Step 6：新建项目表单的"浏览…"

`ProjectsView.vue` 的新建项目表单：路径输入框旁加"浏览…"按钮，打开同一个 `FolderDialog`，选中后填入路径。表单本身改成弹窗、目录已是项目时的提示，属于 web 计划 018 的范围；那里会复用这个对话框。本步只把对话框接上。

### Step 7：测试

- 前端单测：
  - `ProjectPopover`：排序、禁用、标记、搜索；
  - `FolderDialog`：后退 / 前进 / 上一层、双击进入、blocked 不可选、新建文件夹成功与重名；
  - `TrustDialog` 的两个按钮。
  - `ChatView` 首条消息的四条分支：
    - 无项目不发；
    - 未判断信任先问，取消后不发；
    - 新文件夹建项目，显示"已创建项目"；
    - 目录缺失时报错。

  用 `vi.spyOn(forebrainApi, ...)` 桩接口，照 `ChatView.test.ts` 现有写法。
- `scripts/acceptance/web_e2e.sh`：加一条真浏览器用例。登录后在新对话页打开文件夹对话框，进入临时目录，新建子目录并选中，发消息，确认弹出信任提示；信任后会话建成，消息流顶部有"已创建项目"。临时目录在脚本里创建、结束时删除。

## Test plan

见 Step 1–3 的 Verify 与 Step 7。

## Done criteria

- [ ] Commands 表全部通过；`make ui` 成功
- [ ] 不带项目的 `POST /api/chat/sessions`（非工坊）返回 400
- [ ] e2e 新用例 PASS
- [ ] 截图对照：在 1440×900 下把实现页面与原型的四个跳转状态各截一张，并排放进报告。结构、文案、间距一致；不一致处逐条列出原因
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. gateway 的鉴权中间件不能覆盖新路由（新接口必须鉴权）。
3. 原型与本计划的文字描述不一致：以原型为准；拿不准时停下问。
4. 现有前端测试断言"新对话可以不带项目"并注明是 owner 决定。

## Maintenance notes

- `/api/host/dirs` 能列出本机任意目录名（不含文件），只在鉴权后可用；以后若 gateway 要对局域网开放，这个接口要重新评估。
- "上次用的项目"取自最近会话。以后要跨设备一致，可以改存库（设计文档 2.7 也建议附件目录存库）。
