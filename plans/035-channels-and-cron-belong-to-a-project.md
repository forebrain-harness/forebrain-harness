# Plan 035：通道与定时任务属于项目——按项目配置通道、从通道进来的对话出生在项目根、定时任务必须带项目、旧的主代理级通道配置迁移时删掉

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> **web 界面 1:1 还原设计稿** `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`：在"本轮新增"里点"项目 · 定时任务""项目 · 新建定时任务""项目 · 通道"；原型函数 `cronBlock()`、`cronModal()`、`scheduleBuilder()`、`channelsBlock()`。
>
> **存储方案已定**：owner 2026-10-11 选定 D-035-1 方案 A（状态库新表 + `.env` 引用），见 Step 1。
>
> **Drift check（先跑）**：
> - 025、027、028 已完成：`grep -n "func (env \*Environment) RunnerForTurn" pkg/process/runner_pool.go` 有 1 行；`grep -n "ProjectID string" pkg/state/session_store.go` 能找到 `SessionBirth` 的字段。
> - `grep -n "Channels ChannelsSection" pkg/config/agents.go` 有 1 行。
>
> 不符 → STOP。

## Status

- **Priority**: P2
- **Effort**: L
- **Risk**: MED（通道凭据换存储位置；删除旧配置段）
- **Depends on**: 025、027、028
- **Category**: feature / data model
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11

## Why this matters

owner 2026-10-11：
- 通道和定时任务移到项目空间的标签页，菜单栏里的菜单项删掉；
- 不显示"所属项目"表单项，默认属于当前项目；新建定时任务用弹窗；
- 旧的主代理级通道配置迁移时删掉，不猜归属项目。本机 12 个通道全部未启用、没有凭据；
- 从通道进来的对话属于该项目，出生在项目根；同一个机器人账号只能属于一个项目；
- 设计文档：`docs/plan/SESSION_BIRTH_DIR_CONTINUATION_DESIGN.md` R7、2.6。

## Current state（已核实）

- **通道配置**：
  - 挂在主代理下：`AgentDefinition.Channels ChannelsSection`（`pkg/config/agents.go:19-23`），类型定义在 `pkg/config/channels.go:16` 起（WeCom、Telegram、Discord、Slack……）；
  - 读取：`pkg/config/channels.go:47`；写入：`:127`、`pkg/gateway/api_extra.go:4068`；
  - 校验：`pkg/config/load.go:493-505`、`:662`，只有主代理可以声明。
- **绑定与收消息**：
  - `s.Channels.BindAgent(ctx, cfg, agentID, workspaceRoot, bus)`（`pkg/gateway/channels.go:30`）；
  - 收到消息走 `channelBus.PublishInbound`（`pkg/gateway/server.go:434` 起）：会话 ID = `state.WrapChannelSession(ch, m.SessionID)`，`submitChannelTurn`（`channels.go:60` 起）里 `PersistUserTurn(..., EnsureSession: true)` 建会话，**不带项目**。
- **定时任务**：
  - `fb_cron_jobs.project_id` 可为空（`pkg/state/schema.sql:303`）；`state.CronJob.ProjectID`（`pkg/state/cron.go:26-27`）注释写着"empty is an agent-wide job"；
  - `CronService` 的新建输入 `ProjectID`（`pkg/process/cron_service.go:346-381`）；
  - gateway 接口 `handleCronJobs` / `handleCronJob` 等（`pkg/gateway/api_extra.go:4430` 起）；
  - 每次触发建会话的位置：`grep -n "fire\|session" pkg/process/cron_service.go pkg/turn/*cron* | head`。
- **前端路由**：
  - 主代理级 `/cron`、`/channels`（`frontend/src/router/index.ts:79-88`）与菜单栏菜单项；
  - 项目空间已有 `project-cron` 标签（`:75`），组件 `ProjectCron.vue`；
  - 通道页 `ChannelsView.vue`；
  - 定时任务页 `CronView.vue` 与设置里的 `CronSettingsTab.vue`。
- **本机**：`forebrain.yaml` 里主代理 `main` 下 12 个通道条目，全部 `enabled: false`、没有凭据字段；`fb_cron_jobs` 0 行。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| Go | `CGO_ENABLED=1 go test -tags fts5 ./pkg/config/... ./pkg/channel/... ./pkg/state/... ./pkg/process/... ./pkg/gateway/... ./pkg/architecture/... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/...`；`gofmt -l pkg` | 无输出 |
| 前端 | `cd frontend && pnpm test && npx vue-tsc --noEmit -p tsconfig.json` | 全绿 / 无输出 |
| 构建 | `make ui` | 成功 |

## Scope

**In scope**：
- `pkg/config`（去掉主代理级通道，加配置迁移）；
- `pkg/state`：通道表与迁移，定时任务必须带项目；
- `pkg/channel` 的绑定入口；
- `pkg/gateway`（通道与定时任务接口、收消息建会话）；
- `pkg/process/cron_service.go`；
- 前端：项目空间的"定时任务""通道"标签；删除 `/cron`、`/channels` 路由与菜单项；设置里的定时任务标签按设计稿处理（见 Step 5）；
- 测试、`pkg/gateway/dist`。

**Out of scope**：
- 各通道协议本身的实现；
- 定时任务计划编辑器的内部（计划 013 已做，沿用 `ScheduleBuilder.vue`）。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：项目通道的存储（决定 D-035-1：owner 2026-10-11 选定方案 A）

| 方案 | 做法 | 优点 | 缺点 |
|---|---|---|---|
| **A（推荐）** | 状态库新表 `fb_project_channels(project_id REFERENCES fb_projects(id) ON DELETE CASCADE, kind TEXT, enabled INTEGER, config_json TEXT, account_key TEXT, PRIMARY KEY(project_id, kind))`。密钥字段不进库：写成 `${FOREBRAIN_CH_<项目短 id>_<字段>}` 引用，值写进 `~/.forebrain/.env`，与模型服务 API Key 同一套机制（`grep -rn "\.env" pkg/gateway/api_extra.go \| grep -i provider \| head`）。`account_key` 存账号身份（bot token 的哈希、app id 等），加唯一索引，保证一个账号只属于一个项目。 | 删除项目时自动级联；不进项目目录、不会被 git 提交；与"会话数据落库"一致 | `.env` 里要清理已删项目的变量 |
| B | `forebrain.yaml` 新增 `project_channels: {<项目 id>: ChannelsSection}` | 配置都在一个文件 | 项目 id 不可读；删项目要同步改 yaml；与库里的项目表两处真相 |
| C | 项目目录 `.forebrain/channels.yaml` | 跟着项目走 | 凭据可能被提交进 git，不可取 |

owner 选定方案 A，以下步骤按方案 A 写。

### Step 2：配置迁移（删掉旧的主代理级通道）

1. `AgentDefinition.Channels` 字段删除。加载配置时若文件里还有 `agents.definitions.<id>.channels`：
   1. 启动时一次性改写 `forebrain.yaml`，删掉这一段。改写方式照仓库现有的配置写回函数（`grep -rn "func.*SaveConfig\|func WriteRoot\|yaml.Marshal" pkg/config | head`），并先把原文件备份成 `forebrain.yaml.bak-<UTC 时间戳>`，仓库已有同名备份习惯：本机有 `forebrain.yaml.bak-20260921T045702Z`；
   2. 被删的条目里有 `enabled: true` 的，打一条 `slog.Warn`："通道 <名字> 已停用：通道改为按项目配置，请在项目空间的"通道"标签里重新配置"；gateway 启动横幅里也提示一次（计划 002 的横幅输出位置）。
2. 校验（`load.go:662`）里与主代理通道相关的分支删除；`config/channels.go` 里按主代理读写通道的函数删除，调用方改读项目通道（Step 3）。
3. 测试：
   - 带 `channels` 段的配置加载后文件里没有这段，备份存在；
   - 有启用条目时有警告日志；
   - 无此段时文件不被改写。

### Step 3：按项目绑定通道、进来的对话属于项目

1. `pkg/state` 加 `ProjectChannelStore`，提供：
   - 列出某主代理所有项目的已启用通道；
   - 读取、写入单个通道；
   - 写入时用 `account_key` 唯一约束拒绝重复账号，返回 `ErrChannelAccountTaken{Project}`。
2. gateway 启动和切换主代理时，`BindAgent` 改为 `BindProjects`：每个已启用的项目通道是一个独立实例，绑定时带上 `projectID`。
   - `pkg/channel` 的 `Registry` 若以通道类型为键、不支持同类型多实例（`grep -n "func (r \*Registry) BindAgent" -A30 pkg/channel/*.go`）：改成以 `(kind, projectID)` 为键，入站消息 `Inbound` 增加 `ProjectID`。
   - 改动若波及每个通道实现文件 → STOP，报告需要改动的范围。
3. `PublishInbound`：
   - 会话 ID 加上项目，形如 `state.WrapChannelSession(ch+":"+projectID, m.SessionID)`，避免两个项目的同名对端撞到同一个会话；
   - `submitChannelTurn` 建会话时用 `SessionBirth{Cwd: 项目根, ProjectID: projectID, Source: ...}`，对齐 028 Step 2；
   - 开始运行用 027 的 `RunnerForTurn`。
4. 出站投递（`server.go:412`、`cron_service.go:208`）按会话所属项目找到通道实例。

### Step 4：定时任务必须带项目

1. `state.CronJob.ProjectID` 改为必填：
   - `CreateCronJob` / `UpdateCronJob` 为空时返回错误；
   - 注释删掉"empty is an agent-wide job"；
   - 按主代理列任务的查询（`cron.go:239`）只保留按项目的版本。
   - 025 迁移已删除空项目的任务。
2. `CronService` 触发时建的会话带项目：`SessionBirth{Cwd: 项目根, ProjectID: ...}`；开始运行用 `RunnerForTurn`。项目根不存在时这次触发记为失败，原因是"目录已不存在"。
3. gateway 定时任务接口：
   - 路径改为 `/api/projects/:id/cron/jobs...`，或者保留现有路径但 `project_id` 必填；选前者并删除旧路径，与"属于项目"的 URL 结构一致，报告里写明；
   - 投递通道的候选只列本项目已启用的通道。

### Step 5：前端

1. 删掉菜单栏的"定时任务""通道"两项，以及 `/cron`、`/channels` 路由。`CronView.vue`、`ChannelsView.vue` 中项目空间用得上的部分并进项目标签，其余删除。
2. 项目空间标签：
   - "定时任务"照 `cronBlock()`：
     - 标题行"定时任务 · 属于本项目，在项目根目录执行。· 新建"；
     - 列表每项：名称、计划、暂停标记、"投递至…"、执行次数；
     - 操作：立即执行、暂停 / 恢复、执行记录、删除；
     - "新建"打开 `cronModal()` 弹窗：名称、计划（含"自定义…"展开 `ScheduleBuilder`）、任务描述、投递通道（只列本项目通道）。没有"所属项目"选项。
   - "通道"照 `channelsBlock()`：
     - 顶部一行"密钥已脱敏，不修改即保留原值。"和"刷新""保存"；
     - 每个通道一张卡片：名称 + 开关，开启后显示字段。
     - 账号已被别的项目占用 → 保存时提示"这个账号已属于项目 X"。
3. 设置里的 `CronSettingsTab.vue`：原型的设置页没有"定时任务"标签。若它只是主代理级任务列表 → 删除；若含全局调度开关等设置 → 停下问 owner 放哪里。
4. 页面结构遵守 owner 2026-10-11 的"无页面标题层、少嵌套"：标签内容第一行是"标题 + 一句说明 + 按钮"，列表直接跟在下面，不再套带标题的卡片。

### Step 6：测试

- 两个项目各配一个 Telegram 通道（不同 bot）；同一个 bot 配到第二个项目被拒绝。
- 用假通道（`pkg/channel` 的测试桩，`grep -n "fake\|stub" pkg/channel/*_test.go | head`）发一条入站消息，生成的会话 `project_id` 正确、`cwd` 为项目根；回复投递到同一个实例。
- 定时任务：
  - 不带项目创建被拒绝；
  - 触发时会话属于项目；
  - 项目根缺失时这次触发失败，失败原因写明。
- 删除项目连同它的通道行与定时任务（025 的 `Delete` 已删定时任务，这里补通道表的级联断言）。
- 前端：
  - 两个标签的列表与弹窗；
  - 菜单栏没有这两项；
  - 访问旧路由跳 404 或重定向（照仓库已有的旧路由处理：`grep -n "redirect" frontend/src/router/index.ts`）。

## Test plan

见 Step 6。

## Done criteria

- [ ] Commands 表全部通过；`make ui` 成功
- [ ] `grep -rn "Channels ChannelsSection\|agent-wide job" pkg --include='*.go'` 无输出
- [ ] 本机副本验证：把 `~/.forebrain/forebrain.yaml` 复制到隔离 home 启动一次，`channels` 段被删除、备份存在、没有启用条目所以无警告。**不要在真实 home 上跑**。
- [ ] 与原型三张截图并排对照，写进报告
- [ ] `git status --short` 中本计划新增的改动只涉及 Scope 列出的文件

## STOP conditions

1. `pkg/channel` 的注册表改成多实例要动每个通道实现。
2. 设置里的定时任务标签含有全局设置，不知道放哪里。

## Maintenance notes

- 新增通道类型时，要在 `account_key` 里给出它的账号身份（用什么字段判定"同一个机器人"），否则唯一约束形同虚设。
- 删除项目时，方案 A 下要顺带清理 `.env` 里该项目的变量。写在 `ProjectStore.Delete` 之后的 gateway 处理里，并补测试。
