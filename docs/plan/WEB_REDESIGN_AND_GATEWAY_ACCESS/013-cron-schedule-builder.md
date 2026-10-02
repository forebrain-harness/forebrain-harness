# 计划 013：定时任务计划编辑器——七种自定义模式、服务端预览 API、主代理与项目任务分离

> **执行者须知**：逐步执行，每一步都运行"验证"命令并确认结果后再进入下一步。出现"STOP 条件"中的任何情况，立即停止并报告。
> 完成后更新 README 中本计划的状态行。**不要提交代码。**
>
> **漂移检查（先运行）**：
> `git diff --stat b92a1b1..HEAD -- pkg/turn/schedule.go pkg/gateway/api_extra.go frontend/src/views/CronView.vue frontend/src/components/project/`
> 计划 007 改过 `api_extra.go` 路由注册行；008 建过项目空间壳（cron 占位标签）。那是预期的。

## 状态

- **优先级**：P1
- **工作量**：M
- **风险**：LOW-MED（调度解析器零改动，只加只读预览端点；前端是新表单）
- **依赖**：计划 007（`/cron` 菜单位）、计划 008（项目空间 cron 标签挂点）
- **类别**：feature
- **基线**：提交 `b92a1b1`，2026-09-29

## 为什么要做

owner 要求（第 31 条）："定时任务页面的计划的自定义选项没有设计完，用户如何自定义没有设计出来"。预览 captions 的 `cron` 条（400 行）是合同：计划选"自定义…"展开计划编辑器——一次、按间隔、每天、工作日、每周（可多选星期）、每月、Cron 表达式七种方式；下方实时显示"将保存为"的计划表达式和接下来的执行时间，**由服务端按实际调度规则计算**；投递通道只列出本主代理已启用的通道；主代理页列不绑定项目的任务，项目空间列绑定项目的任务。

## 现状

- **调度文法**（`pkg/turn/schedule.go`）：`ParseSchedule :95` 接受（按文档注释 `:79-93`）`in 30m`/`2026-03-15T09:00:00`（once）、`every 30m`/裸时长（间隔，下限 `MinScheduleInterval :75` = 1 分钟）、`daily at 7am`、`weekdays at 9am`、`every monday 9am`（**单星期名**，`parseCalendar :186` 只接受一个星期词，`weekdayNames :172` 表）、cron 表达式与 `@` 描述符（`cronParser` 定义于 `:77-78`）。`Schedule.Next(after) :47` 返回下次触发时间。
- **任务存储与项目绑定**：`pkg/state/cron.go:23` `CronJob`（含 `ProjectID`，空 = agent-wide）；`ListJobs :207`（按 agent）、`ListJobsForProject :221`（按项目，空串查 NULL）。`pkg/process/cron_service.go:141` `CronJobInput`（含 `ProjectID`）、`CreateJob :164`、`UpdateJob :220`、`RunJobNow :260`。
- **HTTP 层**（`pkg/gateway/api_extra.go`）：`cronJobRequest :3043`（含 `ProjectID`）；`cronScope :3069`（服务 + 当前主代理）；`handleCronJobs :3083`——GET **返回该主代理全部任务，不支持 query 过滤**（`:3090` 直接 `svc.ListJobs`）；POST/PUT/DELETE 走同一组 handler（`:3100+`）。没有预览端点。
- **通道**：`GET /api/channels`（`handleChannels :2898`，读当前主代理的通道定义，含启用状态）——投递通道下拉的数据源。`Deliver` 字段语义：空 = 只记录不投递。
- **前端**：`frontend/src/views/CronView.vue`（220 行）是简单表单（name/schedule/prompt 文本输入），无模式化编辑器、无预览、无项目过滤。
- README 全局规则（90-115 行）全部适用。

## 设计

### 1. 服务端预览 API（只读，零引擎改动）

`POST /api/cron/preview` `{schedule: string}` → `{raw, valid, kind, next: [ISO 时间 x3], error}`：
- 服务端 `turn.ParseSchedule(expr, time.Now())`；失败 → `{valid:false, error}`（200 响应，不是 4xx——预览是常态路径）；
- 成功：`kind` 映射 `ScheduleKind`（once/every/cron），循环 `Schedule.Next` 收集接下来 3 个时间（once 只有一个）。
- `raw` 是 `turn.Schedule.Raw`（`pkg/turn/schedule.go:26`）——**引擎原样保存用户输入的表达式文本，不做归一化**（该字段的注释即为此：让界面显示用户输入的内容而不是改写后的形式）。所以 `daily at 7am` 的 `raw` 仍是 `daily at 7am`，不是 `0 7 * * *`。界面上"将保存为：<raw>"因此等于回显客户端刚生成的表达式；它的价值是确认服务端解析成功且存的就是这一串。
- 响应不含任何租户状态，纯函数式；注册在 cron 路由组旁。

### 2. 任务列表按层过滤

`handleCronJobs` GET 支持 `?project_id=`：
- 缺省（主代理页）：返回 `ProjectID == ""` 的任务（服务端过滤 `ListJobs` 结果——**不**改 `ListJobs` SQL，保持存储层接口不动；行数预期为常驻任务量级，内存过滤足够，不做过早优化）；
- `?project_id=<id>`：校验项目归属后走 `ListJobsForProject`；
- 响应条目已含 `project_id`，前端据此显示"绑定项目：<名>"徽标。

### 3. 计划编辑器（`ScheduleBuilder.vue`，模式化表单）

七种模式（选择框切换），每种只暴露必要的输入，全部选择框/步进器，不让用户手写表达式：
| 模式 | 输入 | 生成表达式 |
| --- | --- | --- |
| 一次 | 日期时间选择器（本地时区） | ISO 本地时间串 |
| 一次（相对） | 数字 + 单位（分钟/小时） | `in 30m` |
| 按间隔 | 数字 + 单位（分钟/小时/天，步进校验 ≥1 分钟） | `every 2h` |
| 每天 | 时间选择器 | `daily at 7am`（`7:30am` 带分钟） |
| 工作日 | 时间选择器 | `weekdays at 9am` |
| 每周 | **星期多选**（一~日 checkbox）+ 时间 | 多选 >1：cron `0 9 * * 1,3,5`；恰好 1 个：`every monday 9am` 文法 |
| Cron | 文本输入（给高级用户） | 原样 |

任一输入变化 → 客户端拼出表达式 → 防抖 400ms 调 `POST /api/cron/preview` → 下方显示"将保存为：<raw>"与"接下来执行：<t1> <t2> <t3>"（本地时区渲染）；`valid:false` 显示错误原文（引擎的错误消息就是给用户看的）。

### 4. 两个页面

- **主代理定时任务页（`/cron`）**：`CronView.vue` 重写。列表 = agent-wide 任务（名称、计划、下次执行（预览 API 逐行取或列表响应扩展——见 STOP 条件的取舍说明：实现取"打开编辑器/悬停行时才调 preview"，列表页不为每行都打预览）、启用开关、已执行次数若响应有则显示）、"新建任务"打开编辑器弹层（含 prompt 多行输入、投递通道下拉 = `GET /api/channels` 已启用通道 + "只记录，不投递"空选项、repeat_limit 可选数字）、行内"立即执行"（`RunJobNow` 既有端点）、编辑、删除。页头"主代理"作用域徽标。
- **项目空间 cron 标签（`/projects/:id/cron`）**：替换 008 占位 `ProjectCronTab.vue`——同一编辑器组件与列表，数据走 `?project_id=`，创建/编辑请求带 `project_id`（后端 `cronJobRequest` 已支持）。页头"项目"作用域徽标。

## 需要的命令

| 用途 | 命令 | 成功标志 |
| --- | --- | --- |
| Go 静态检查 | `go vet ./...` | 退出码 0 |
| Go 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... ./pkg/turn/... -count=1` | 全部 `ok` |
| 前端单测 | `cd frontend && corepack pnpm test` | 全部通过 |
| 真机验证 | `scripts/acceptance/web_e2e.sh` | 最后一行 `web e2e: PASS` |
| 缓存不变式 | `git diff --stat -- pkg/agent pkg/assembly pkg/llm pkg/turn/prompt* internal/agentrun` | 无输出 |

## 范围

**只允许修改**：
- `pkg/gateway/cron_preview.go`（新建：preview handler）、`pkg/gateway/cron_preview_test.go`（新建）、`pkg/gateway/api_extra.go`（`handleCronJobs` 的 GET 过滤 + 路由注册行）
- `frontend/src/views/CronView.vue`（重写）、`frontend/src/components/cron/ScheduleBuilder.vue`（新建）、`frontend/src/components/project/ProjectCronTab.vue`（替换 008 占位）
- `frontend/src/locales/index.ts`、`frontend/src/lib/api.ts`
- `frontend/e2e/cron-builder.spec.ts`（新建）

**不要碰**：
- `pkg/turn/schedule.go`、`pkg/process/cron_service.go`、`pkg/state/cron.go`（引擎与存储零改动；预览只是调用方）。
- 计划 009/010/012 的页面。

## 步骤

### 第 1 步：预览端点与列表过滤

实现设计第 1、2 条。`cron_preview_test.go` 覆盖：`every 30m` 返回 3 个递增时间且 kind=every；`in 30m` 返回 1 个；`daily at 7am` kind=cron（`parseCalendar` 归一为 cron 评估）且 next 合理、`raw` 仍是 `daily at 7am`；垃圾输入 `valid:false` 带 error；`?project_id=` 过滤正确、他人项目 404。
结构参照：解析器行为断言照 `pkg/turn/schedule_test.go:18` `TestParseAcceptsTheDocumentedScheduleShapes`（同一批文法），gateway handler 的写法照 `pkg/gateway/agents_api_test.go:20` `TestPrimaryAgentsAPIListsAndSwitches`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway/... -count=1` 全部 `ok`；`go vet ./...` 退出码 0。

### 第 2 步：编辑器与两页

实现设计第 3、4 条。

**验证**：`cd frontend && corepack pnpm test` 全部通过。

### 第 3 步：真机验证

新建 `frontend/e2e/cron-builder.spec.ts`：
1. `/cron` 新建任务：模式"每周"勾选一/三/五 + 09:00 → 预览显示 `0 9 * * 1,3,5` 与 3 个未来时间（断言都在未来且递增）；保存后列表出现。
2. 模式"一次（相对）"填 30 分钟 → 预览 `in 30m`；保存成功。
3. 间隔填 30 秒 → 预览报错（低于 1 分钟下限，错误原文可见），保存按钮禁用。
4. 编辑既有任务改投递通道为"只记录，不投递"，保存后行内通道列更新。
5. 立即执行：任务状态/最近执行出现变化（轮询列表 ≤ 30s）。
6. 项目空间 cron 标签：新建带 `project_id` 的任务成功；`/cron` 主代理列表**不含**它（层级断言）；项目列表含它；截图 `cron-project.png`。

**验证**：`scripts/acceptance/web_e2e.sh` → 最后一行 `web e2e: PASS`。

## 完成标准（全部满足）

- [x] 第 1-2 步的验证全部通过
- [x] `scripts/acceptance/web_e2e.sh` 最后一行 `web e2e: PASS`（64/64，2.6 分钟）
- [x] 计划表达式只由编辑器生成（列表/表单无裸 schedule 文本输入，Cron 模式除外）；"接下来执行"来自服务端
- [x] `git diff --stat -- pkg/turn/schedule.go pkg/process/cron_service.go pkg/state/cron.go` 无输出
- [x] `git status --short pkg/gateway/dist` 无输出；缓存不变式检查无输出
- [x] README 状态行已更新

## STOP 条件

- "现状"摘录与代码对不上（尤其 `parseCalendar` 单星期限制、`handleCronJobs` 现行为、`cronJobRequest` 字段）。
- 发现列表页必须为每行显示下次执行时间而当前响应没有该字段——先与 owner 确认是否扩展 `CronJob` 响应 DTO（涉及存储层接口取舍）。
- 需要修改引擎或存储层文件。

## 维护说明

- 调度文法以 `pkg/turn/schedule.go` 为唯一事实：编辑器只生成它已支持的子集，TUI 与 Web 共享同一解析器，两端行为天然一致。
- 多选星期生成 cron 而不是 `every <day>` 文法，是引擎"单星期名"限制的映射；引擎将来支持多星期名时可简化生成器。
- 预览端点是纯函数，无状态无缓存；引擎改动自动生效。
