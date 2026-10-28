# 跑批任务接入指南（业务系统 → 网关）

本文面向要把自己的业务流程接到 Forebrain Harness 网关上的开发者。跑批（batch）项目是一条
**非交互式**的执行通道：业务系统通过 HTTP 建任务、通过 websocket 发起执行并接收流式输出，
中间没有人查看、没有人插话、模型也不会回头提问。

- 默认网关地址：`http://127.0.0.1:8787`（由 `gateway.http_addr` 配置）
- 所有 HTTP 接口前缀：`/api`（对话相关的 v1 接口是 `/api/v1/...`）
- websocket：`ws://<host>/ws/chat`

---

## 1. 概念

| 概念 | 说明 |
|---|---|
| **项目（project）** | 类型为 `batch` 的项目。它的根目录就是任务工作区的父目录；任务、执行、生成物都挂在它下面。一个 batch 项目用**信任（trust）**创建，因为跑批要在项目自己的技能与 MCP 上无人值守地运行。 |
| **任务（task）** | 一条业务任务，`id` 形如 `tsk-…`。任务保存**任务内容**（首条消息固定下来的原始指令）、标题、业务方自己的 `external_ref`、来源（`api`/`web`/`cron`/`channel`）。 |
| **执行（execution）** | 任务的一次运行：**一个执行 = 一个新会话 + 一个新工作区**。`seq` 从 1 开始递增，`session_id` 就是这次执行所用的会话 id。 |
| **任务工作区** | `<项目根>/tasks/<任务ID>/<序号>/`，内含 `inputs/`（业务方上传的附件，重启执行时从上一次复制过来）、`outputs/`（交付物）、`.work/`（中间文件）。**执行期间模型的工作目录就是它**。 |
| **生成物（artifact）** | 执行交付的文件。模型用 `present_files` 交付（`origin=model`）；若一次执行一个文件都没交付，引擎会把 `outputs/` 里的文件登记为 `origin=fallback`。 |

### 执行状态机

```
pending ──(首个回合开始)──▶ running ──┬──▶ completed   完成，不可再跑
   ▲                                  ├──▶ stopped     被停止，可在**原会话**续跑
   │                                  └──▶ failed      失败，只能"重新执行"
   └── 新建执行从这里开始
```

| 当前状态 | 收到一个回合请求 | 结果 |
|---|---|---|
| `pending`，任务还没有内容 | 有内容 / 有附件 | 首回合：记录任务内容，回合输入 = 消息内容 |
| `pending`，任务已有内容（新执行） | 空内容、无附件 | 首回合：回合输入 = 任务内容 |
| `pending`，任务已有内容 | 有内容或附件 | 拒绝 `batch_task_input_fixed` |
| `pending`，任务还没有内容 | 空内容、无附件 | 拒绝 `batch_task_input_missing` |
| `stopped` | 空内容、无附件 | 续跑：回合输入 = 引擎的"从停下的地方继续"提示 |
| `stopped` | 有内容或附件 | 拒绝 `batch_takes_no_input` |
| `running` | 任何 | 拒绝 `batch_execution_running` |
| `completed` / `failed` | 任何 | 拒绝 `batch_execution_finished`（请发起该任务的新执行） |

一旦回合跑起来：**该会话的输入队列关闭**（插话、排队一律拒绝）、模式固定为 agent
（`switch_mode` 返回 `batch_mode_fixed`）、内置斜杠命令不可用（`batch_command_not_allowed`，
技能自己的斜杠命令仍可用）、模型看不到 `user_interaction` / `enter_plan_mode` /
`exit_plan_mode` / `request_permissions` / 记忆类工具，也不注入记忆。用量上限命中时执行**直接
失败**（带结构化 `code` 与 `reset_at`），不会自动续跑。

---

## 2. 鉴权

网关在 `forebrain.yaml` 里配置：

```yaml
gateway:
  http_addr: 127.0.0.1:8787
  auth:
    mode: token            # none | token
    token: ${FOREBRAIN_GATEWAY_TOKEN}
```

`mode: token` 时，每个请求必须带下面任意一种凭据（决策点是
`pkg/gateway/controlplane.go` 的 `authorizeControlPlane`，HTTP 中间件与 websocket 握手用的是
同一个函数）：

- 请求头 `Authorization: Bearer <token>`
- 请求头 `X-API-Key: <token>`
- 网页登录后浏览器自动携带的会话 cookie —— **只对同源请求有效**，写操作还会校验 `Origin`

`<token>` 就是 `gateway.auth.token` 的值。凭据**不要**放在查询串里（会进访问日志与浏览器历史）。

```http
POST /api/v1/projects HTTP/1.1
Host: 127.0.0.1:8787
Authorization: Bearer <token>
Content-Type: application/json

{"name":"月度报表","root":"/srv/batch/monthly","trust":true,"project_type":"batch"}
```

```json
{
  "id": "prj_8f0f2b6a-1f6f-4c0e-9d0f-2f1f0b7f0a11",
  "name": "月度报表",
  "root": "/srv/batch/monthly",
  "project_type": "batch",
  "trust_recorded": true,
  "created_at": 1793000000,
  "updated_at": 1793000000
}
```

> 建 batch 项目必须带 `"trust": true`，否则返回 `409 {"error":"batch_requires_trust"}`。

websocket 握手用同一个 token（浏览器会自动带上 cookie；服务端之间的调用请显式带 header）：

```python
import websockets
headers = {"Authorization": "Bearer <token>"}
async with websockets.connect("ws://127.0.0.1:8787/ws/chat", extra_headers=headers) as ws:
    ...
```

---

## 3. 发起新任务

一次新任务分三步：**（可选）上传附件 → 建任务会话 → 连 websocket 发起首回合**。

### 3.1 上传附件（可选）

`POST /api/files`，`multipart/form-data`，字段：

| 字段 | 说明 |
|---|---|
| `session_id` | 附件所属的会话。**必须先有会话**，所以顺序是：先建任务会话（3.2），再上传附件，最后 `start_run`。 |
| `file` | 文件本体。 |

```http
POST /api/files HTTP/1.1
Authorization: Bearer <token>
Content-Type: multipart/form-data; boundary=----demo

------demo
Content-Disposition: form-data; name="session_id"

web-3c1f9a2e-77bb-4c33-8f9e-5e2f0b1c9a10
------demo
Content-Disposition: form-data; name="file"; filename="rows.csv"
Content-Type: text/csv

month,amount
1,10
------demo--
```

响应里 `file_id` 就是后面 `start_run` 附件数组要用的文件 id：

```json
{"file_id":"5a0c1f0e-2f4e-4a1e-9f6a-1c3c2b7d8e90"}
```

### 3.2 建任务会话

`POST /api/v1/projects/{project_id}/sessions`

| 字段 | 说明 |
|---|---|
| `title` | 可选。**新任务**时用作任务标题；空字符串表示由首条消息命名。 |
| `task_id` | 可选。**不带 = 新任务**；**带 = 该任务的一次新执行（重新执行）**。 |
| `external_ref` | 可选。业务方自己的单号，读接口原样回显，便于对账。 |
| `origin` | 可选。`api`（默认）或 `web`。 |

```http
POST /api/v1/projects/prj_8f0f2b6a-1f6f-4c0e-9d0f-2f1f0b7f0a11/sessions HTTP/1.1
Authorization: Bearer <token>
Content-Type: application/json

{"title":"10 月月度报表","external_ref":"BILL-2026-10"}
```

```json
{
  "id": "web-3c1f9a2e-77bb-4c33-8f9e-5e2f0b1c9a10",
  "title": "10 月月度报表",
  "project": {"id": "prj_8f0f…", "name": "月度报表", "project_type": "batch"},
  "task": {
    "id": "tsk_9b7c1d22-0a63-4e2f-8f11-6c2f9a0d3b44",
    "title": "10 月月度报表",
    "external_ref": "BILL-2026-10",
    "origin": "api"
  },
  "execution": {
    "seq": 1,
    "status": "pending",
    "workspace": "/srv/batch/monthly/tasks/tsk_9b7c1d22-0a63-4e2f-8f11-6c2f9a0d3b44/1",
    "workspace_rel": "tasks/tsk_9b7c1d22-0a63-4e2f-8f11-6c2f9a0d3b44/1"
  }
}
```

建响应同时把 `inputs/`、`outputs/`、`.work/` 三个子目录建好。**重新执行**（带 `task_id`）时，
新执行的 `seq` 递增，并把上一次执行 `inputs/` 里的文件复制到新的 `inputs/`，会话标题变成
`<任务标题> #<序号>`。

错误：

- `400 {"error":"task_id_requires_batch_project"}` —— 非 batch 项目不接受 `task_id`
- `404 {"error":"task_not_found"}` —— `task_id` 不存在或不属于这个项目
- `409 {"error":"batch_project_uses_tasks"}` —— `POST /api/chat/sessions` 不能用来建 batch 会话
  （batch 会话必须由本接口创建，这样每个会话都有对应的执行）

### 3.3 连 websocket 发起首回合

```json
{"op":"bind_session","request_id":"req-1","session_id":"web-3c1f9a2e-77bb-4c33-8f9e-5e2f0b1c9a10"}
```
```json
{"op":"session_bound","request_id":"req-1","session_id":"web-3c1f9a2e-77bb-4c33-8f9e-5e2f0b1c9a10"}
```

```json
{
  "op": "start_run",
  "request_id": "req-2",
  "session_id": "web-3c1f9a2e-77bb-4c33-8f9e-5e2f0b1c9a10",
  "protocol_version": "2026-04-18",
  "message": {
    "content": "统计 rows.csv 每一列的缺失值，并输出 xlsx 报告到 outputs/",
    "attachments": ["5a0c1f0e-2f4e-4a1e-9f6a-1c3c2b7d8e90"]
  }
}
```

```json
{"op":"run_started","request_id":"req-2","run_id":"run-7f2a…","session_id":"web-3c1f…"}
```

要点：

- **新任务的首回合必须带内容**（附件只带文件、不带文字也可以）：这就是任务的输入，只记录一次。
- 附件会被**复制**到 `<工作区>/inputs/`，消息里给模型的路径是 `inputs/<文件名>`（重名会加
  ` (2)` 后缀）。
- `session_id` 非空时允许空内容 —— 那是"新执行沿用任务内容"和"停止后续跑"用的（见第 6 节）。
- 本连接一次只跑一个回合；并行跑多条任务请开多条连接（见第 8 节）。

---

## 4. 流式事件

客户端会收到两类帧：

**(a) 旧版实时帧**（`op` 直接说明事件）：`session_bound`、`run_started`、`first_token`、
`step`（工具卡：`data.kind` 为 `tool_started`/`tool_completed`）、`requires_action`（审批，
跑批不会出现）、`slash_reply`、`mode_changed`、`run_warning`、`run_completed`、
`run_cancelled`、`run_error`、`turn_withdrawn`、`turn_refused`、`pending_input_updated`。

**(b) 规范化事件帧**：`op` 固定为 `run_event`，事件本体在 `data` 里，`data.type` 是事件类型：

| `data.type` | 含义 |
|---|---|
| `turn_started` | 回合开始 |
| `assistant_delta` / `assistant_done` | 助手正文流式增量 / 结束 |
| `reasoning_delta` / `reasoning_done` | 推理内容（若模型提供） |
| `tool_call_started` / `tool_call_completed` / `tool_output_delta` | 工具调用生命周期 |
| `usage_delta` | token 用量增量 |
| `plan_updated` | 任务清单（`session_todo`）变化 |
| `turn_completed` / `turn_cancelled` / `turn_error` | 回合结束的三种方式 |
| `pending_input_updated` | 排队输入变化（跑批不会出现） |

### 结束帧

`run_completed` 是正常结束。三张结束帧（`run_completed` / `run_cancelled` / `run_error`）都带
**本次运行的生成物清单**：

```json
{
  "op": "run_completed",
  "request_id": "req-2",
  "run_id": "run-7f2a…",
  "session_id": "web-3c1f…",
  "data": {"elapsed_ms": 182734},
  "artifacts": [
    {
      "id": "art_1f7c…",
      "session_id": "web-3c1f…",
      "run_id": "run-7f2a…",
      "name": "missing-values.xlsx",
      "title": "缺失值报告",
      "path": "/srv/batch/monthly/tasks/tsk_9b7c…/1/outputs/missing-values.xlsx",
      "rel_path": "outputs/missing-values.xlsx",
      "mime": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
      "kind": "spreadsheet",
      "preview": "xlsx",
      "size": 12480,
      "sha256": "9f2c…",
      "origin": "model",
      "created_at": 1793000123
    }
  ]
}
```

`run_error` 的 `data.detail` 是结构化的失败原因（`pkg/llm/explain.go` 的分类）：

```json
{
  "op": "run_error",
  "request_id": "req-2",
  "run_id": "run-7f2a…",
  "session_id": "web-3c1f…",
  "error": "usage limit reached — available again at 2026-10-28T02:09:41Z.",
  "data": {
    "code": "rate_limit_quota",
    "status": 429,
    "plan": "pro",
    "reset_at": "2026-10-28T02:09:41Z",
    "retry_after_seconds": 18000,
    "provider_message": "已达到 5 小时的使用上限"
  }
}
```

`code` 可能的值：`rate_limit_quota`、`rate_limit_throttle`、`context_window`、`credentials`、
`billing`、`unknown_model`、`timeout`、`provider_down`、`rejected`。

### 被引擎拒绝的回合

准入被拒绝时服务端回 `turn_withdrawn`，`data.code` 是稳定的错误码（客户端按码分支，不要按文案）：

```json
{
  "op": "turn_withdrawn",
  "request_id": "req-3",
  "session_id": "web-3c1f…",
  "error": "this task execution has already finished; start a new execution of the task",
  "data": {"code": "batch_execution_finished"}
}
```

---

## 5. 生成物

| 接口 | 用途 |
|---|---|
| `GET /api/chat/sessions/{session_id}/artifacts` | 该会话的生成物列表（按交付顺序分页） |
| `GET /api/chat/sessions/{session_id}/artifacts/{artifact_id}` | 单个生成物的元数据 |
| `GET /api/chat/sessions/{session_id}/artifacts/{artifact_id}/content?disposition=attachment` | 下载原文件 |
| `GET /api/chat/sessions/{session_id}/artifacts/{artifact_id}/preview.pdf` | 预览（渲染成 PDF，如可用） |
| `GET /api/chat/sessions/{session_id}/artifacts/{artifact_id}/entries` | 压缩包的条目列表 |

```http
GET /api/chat/sessions/web-3c1f…/artifacts HTTP/1.1
Authorization: Bearer <token>
```

```json
{
  "items": [
    {"id": "art_1f7c…", "name": "missing-values.xlsx", "kind": "spreadsheet",
     "rel_path": "outputs/missing-values.xlsx", "size": 12480, "origin": "model",
     "sha256": "9f2c…", "created_at": 1793000123}
  ],
  "total": 1
}
```

登记的是**文件路径**，不复制文件：文件后来被改动或删除，再次读取时状态会体现出来。若一次执行
一个生成物都没交付，引擎会把 `outputs/` 里的文件登记为 `origin=fallback`（跳过以 `.` 开头的
目录与文件，最多 200 个，不跟随软链）。

---

## 6. 停止、续跑、重新执行

### 停止

```json
{"op":"cancel_run","request_id":"req-4","run_id":"run-7f2a…","session_id":"web-3c1f…"}
```
```json
{"op":"run_cancelled","request_id":"req-4","run_id":"run-7f2a…","session_id":"web-3c1f…","message":"cancelled"}
```

被停止的执行状态是 `stopped`。

### 续跑（**原会话**）

对同一个 `session_id` 发一次**空内容**的 `start_run`：

```json
{"op":"start_run","request_id":"req-5","session_id":"web-3c1f…","protocol_version":"2026-04-18","message":{"content":""}}
```

引擎用固定的"从停下的地方继续"提示接着跑：检查已有的 `outputs/`、`.work/` 与之前的工具结果，
不重做已完成的工作，不问新输入。`completed` / `failed` 的执行拒绝续跑（`batch_execution_finished`）。

### 重新执行（**新会话**）

`POST /api/v1/projects/{project_id}/sessions` 带 `task_id` → 新的 `session_id`、`seq+1` 的工作区、
上一次的 `inputs/` 已被复制过去；然后对新 `session_id` 发**空内容**的 `start_run`（回合输入 =
任务内容）：

```json
{"op":"start_run","request_id":"req-6","session_id":"web-NEW…","protocol_version":"2026-04-18","message":{"content":""}}
```

> 失败（`failed`）的执行只能靠"重新执行"重试：用量上限命中不会自动续跑。

---

## 7. 查询与错误码

### 任务列表

`GET /api/v1/projects/{project_id}/tasks?page=1&limit=20&q=&status=`

- `q` 匹配标题 / `external_ref` / 任务 id
- `status` 按**最新一次执行**的状态过滤（`pending`/`running`/`stopped`/`completed`/`failed`）

```json
{
  "items": [
    {
      "id": "tsk_9b7c1d22-…",
      "project_id": "prj_8f0f…",
      "title": "10 月月度报表",
      "external_ref": "BILL-2026-10",
      "origin": "api",
      "origin_ref": "",
      "input_set": true,
      "created_at": 1793000000,
      "updated_at": 1793000123,
      "latest": {
        "seq": 1,
        "session_id": "web-3c1f…",
        "status": "completed",
        "started_at": 1793000010,
        "finished_at": 1793000123,
        "error_code": ""
      },
      "executions": 1,
      "artifacts": 1
    }
  ],
  "total": 1
}
```

### 任务详情与执行历史

`GET /api/v1/tasks/{task_id}`

```json
{
  "task": {
    "id": "tsk_9b7c1d22-…",
    "project_id": "prj_8f0f…",
    "title": "10 月月度报表",
    "external_ref": "BILL-2026-10",
    "origin": "api",
    "origin_ref": "",
    "input_set": true,
    "created_at": 1793000000,
    "updated_at": 1793000123
  },
  "executions": {
    "items": [
      {
        "seq": 1,
        "session_id": "web-3c1f…",
        "status": "completed",
        "workspace_rel": "tasks/tsk_9b7c1d22-…/1",
        "started_at": 1793000010,
        "finished_at": 1793000123,
        "error_code": "",
        "error_text": "",
        "artifacts": 1
      }
    ],
    "total": 1
  }
}
```

### 错误码表

**跑批准入（`turn_withdrawn` / `run_error` 的 `data.code`）**

| 码 | 含义 | 怎么办 |
|---|---|---|
| `batch_session_has_no_task` | 这个 batch 会话没有对应的执行行 | 用项目会话接口重新建任务/执行 |
| `batch_execution_running` | 这次执行已有回合在跑 | 等它结束，或 `cancel_run` 后重试 |
| `batch_execution_finished` | 已完成/已失败，不能再跑 | 发起该任务的新执行 |
| `batch_takes_no_input` | 停止的执行不接受新输入 | 空内容续跑；要跑新内容请新执行 |
| `batch_task_input_fixed` | 任务内容已固定，本次执行不接受自己的文字 | 空内容发起，或新执行 |
| `batch_task_input_missing` | 任务还没有内容，首回合必须带内容 | 带 `message.content` 再发 |
| `batch_command_not_allowed` | batch 会话不跑内置斜杠命令 | 去掉斜杠命令（技能命令可用） |
| `batch_mode_fixed` | 模式固定为 agent | 不要 `switch_mode` |
| `batch_admission_required` | 回合绕过了准入（引擎内部兜底） | 报告为缺陷，正常入口不会出现 |

**HTTP**

| 状态 / 码 | 含义 |
|---|---|
| `400 task_id_requires_batch_project` | 非 batch 项目带了 `task_id` |
| `400 task_origin_invalid` | `origin` 不是 `api` / `web` |
| `404 task_not_found` | 任务不存在或不属于本主代理 |
| `409 batch_project_uses_tasks` | batch 项目不能在 `/api/chat/sessions` 建会话 |
| `409 batch_requires_trust` | 建 batch 项目没带 `trust: true` |
| `409 batch_takes_no_input` | 对跑批会话的 `POST /api/runs/{run_id}/input`（插话/排队） |

**生成物（146）**：`artifact_not_found`（生成物不存在或不属于该会话）、
`artifact_missing`（登记的文件已不在磁盘上）。

**提供方**：见第 4 节 `run_error.data.detail.code` 表。

---

## 8. 并发

一条 websocket 连接**一次只跑一个回合**：主循环顺序处理消息，`start_run` 之前的
`RunRT.CreateRun` 会把"这个会话已有在跑的运行"作为唯一原子答案返回（拒绝帧 `turn_withdrawn`，
`data.code` 为 `session_busy` 系列）。因此：

- **并行跑多条任务**：开多条连接，每条连接 `bind_session` 到自己的会话（一个执行一个会话）。
- **同一个会话**不要并发发起回合：会话级的串行化（前台锁 + 运行行）由服务端保证，多余的请求
  会被拒绝而不是排队。
- 跑批会话的运行中输入是被关闭的（双保险：`POST /api/runs/{run_id}/input` 返回 `409
  batch_takes_no_input`），所以"等它跑完再发下一个"是唯一正确的并发姿势。

---

## 9. 安全与隔离

一条跑批任务在自己的**任务工作区**里跑，并且整回合**从不向任何人提问**。边界由操作系统
沙箱强制，不由提示词或审批弹窗保证。

| 面 | 规则 |
|---|---|
| 审批 | 永不询问：除"显式 deny 规则"外一律放行。**不**因为 `FOREBRAIN_YOLO` 或 `danger-full-access` 撤掉沙箱，这两者是对交互会话的选择。 |
| shell | 强制 OS 沙箱。可写集合**只有**任务工作区；可读集合**只有**任务工作区、项目/主代理/全局共享技能目录，以及 OS 自带运行时白名单。`TMPDIR`/`TMP`/`TEMP` 指向 `<任务工作区>/.work/tmp`，网络放开。 |
| 拒读 | 项目根（含其它任务的工作区、项目里的其它文件）与 Forebrain 主目录整体拒读。配置里的 `allow_read: "/"` 或任何额外授权**不会**扩大该集合。 |
| 文件工具 | `write_file`/`edit_file`/`apply_patch` 的目标必须在任务工作区内，否则直接拒绝；`read_file` 与搜索工具同 shell 的读范围。`present_files` 只登记已存在的交付文件，不额外写文件。 |
| 越权 | 模型要求 `require_escalated` 或 `additional_permissions` 时直接报错，命令不执行；沙箱拒绝后**不会**走"申请越权重跑"。 |
| 沙箱不可用 | 命令直接失败（fail closed），不会退化成裸机执行。 |
| 会话预设 | 固定显示 `batch-sandbox`（沙箱隔离 · 免审批），不是三个内置预设之一，网关与终端都不能改（`409 batch_preset_fixed`）。 |

**MCP 不在任务沙箱里**：MCP 服务器进程由项目 runner 启动，在项目下所有任务之间共享，
提示词与工具列表也共享。因此，若某个 MCP 自己能读写任意路径，它就是跑批隔离的旁路——
给跑批项目配 MCP 时必须评估这一点，只挂可信的、范围可控的服务器。

**可读的"系统与运行时"**由后端平台白名单落实（Linux `linuxPlatformDefaultReadRoots`，
macOS seatbelt 的库/二进制/设备白名单），不是"除项目根以外全放"。用户 home 的其它目录、
项目外的文档、其它执行目录都不可读。

---

## 10. 通道与通知

跑批项目也可以接入 IM 通道（飞书、Telegram、企业微信…，见项目的 `channels` 配置）。接上以后
通道是**任务入口**，也是**结果出口**：没有人再盯页面，所以每次运行结束都会主动把结果发回去。

### 10.1 通道发来的消息就是一条任务

- 该项目通道收到的**每条消息都开一条新任务**（`origin=channel`），并从第一条消息起跑一个
  **新执行**（新会话、新任务工作区）。它不接续上一条消息的对话。
- 消息文本即任务内容；通道本身不带附件，所以通道任务的输入只有文字。附件请走 3.1 的 HTTP 入口。
- **内置斜杠命令一律拒绝**：`/plan`、`/new` 之类是给交互会话用的，跑批没有可被它操纵的对话，
  通道会收到一句 `后台跑批项目只接收任务内容，不支持命令。`，不会建任务。
- 空消息（无文字的贴纸、事件回调）直接忽略。
- 收下之后先回一句**回执**，结果稍后单独送达：

  ```
  已接收任务：<任务标题>（<任务ID>），完成后通知。
  ```

- 建任务失败（项目不可用、目录写不进去等）回 `任务创建失败：<原因>`，并且不会留下半成品执行：
  已建的执行目录会被删除。任务内容由**首个回合**原子固定，所以创建阶段不会出现"输入已锁但
  执行没人跑"的 pending 行。

### 10.2 执行结束自动通知

执行结束（成功 / 失败 / 停止）时引擎回调网关，网关按任务来源选目标：

| 任务来源 | 通知发到 |
|---|---|
| `channel` | 回到发消息的那个群 / 人 |
| `api`、`web`、`cron` | 项目的通知目标列表 `task_notify`（可配多个；为空则不发） |

固定中文模板（缺失的字段连整行一起省略）：

```
【任务完成】<任务标题>                 ← 失败为【任务失败】，停止为【任务已停止】
任务 <任务ID> · 第 <序号> 次执行 · 用时 <1m23s>
结论：<最后一条助手回复的前 300 字，换行压成空格>
原因：<error_code>：<error_text 前 200 字>          ← 仅失败
生成物：<名称>（<大小>）、<名称>（<大小>）…        ← 最多 10 个，超出写"等 N 个"
查看：<gateway.public_url>/projects/<项目ID>/tasks/<任务ID>
```

- "用时"是执行自己的窗口（`started_at` → `finished_at`），没跑起来就没有这一段。
- 通知是**异步**投递（30s 超时），既不影响也不改变任务状态：投递失败只写日志并把原因记在
  执行行的 `notify_error` 上（`fb_task_executions.notify_error`），任务本身的 `status` 不变。
- 通知只发**一次**：引擎在结束那一刻生成，重发请用 6 节的续跑 / 重新执行。
- 跑批项目的**定时任务**不再走定时任务自己的"投递通道"（否则会和任务通知重复）：那里配置的
  通道会被忽略，跑批的结果一律由上面的通知送达。

### 10.3 配置通知目标（`task_notify`）

目标存在项目行上，`PATCH /api/v1/projects/{project_id}` 读写，形如：

```bash
curl -X PATCH http://127.0.0.1:8787/api/v1/projects/prj_xxx \
  -H "Authorization: Bearer $FOREBRAIN_GATEWAY_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"task_notify":[{"channel":"feishu","target":"oc_1a2b3c"},{"channel":"telegram","target":"-1001234567890"}]}'
```

- `channel` 必须是本网关能构建的通道种类（`pkg/channel` 的 `ChannelKinds`：`feishu`、`telegram`、
  `slack`、`wecom`、`dingtalk`、`webhook`…），`target` 是该通道自己认的收件方 id
  （飞书的 `chat:<id>` 或裸 `oc_…`、Telegram 的 chat id、webhook 的会话 id）。
  非法条目整条请求被拒：`400 {"error":"task_notify_invalid"}`。
- 去重（同 `channel`+`target` 只留一个）与上限 20 条由网关保证；`GET` 项目详情里恒为数组，
  没配就是 `[]`。
- 不知道该填哪个 id？`GET /api/v1/projects/{project_id}/channel-targets` 列出**这个项目**通道里
  出现过的群 / 人，供网页下拉选择：

  ```json
  {"items":[{"channel":"feishu","target":"oc_1a2b3c","label":"帮我看下这份表","last_seen":1761400000}]}
  ```

  来源有两处：从通道进来、开过任务的发送方（`label` 是任务标题），以及该项目通道开过的会话
  （`label` 是会话标题）。按 `(channel, target)` 去重、`last_seen` 倒序、最多 100 条。
- 只配 `task_notify` 不会让通道任务改用项目目标：通道来的任务永远回原群 / 人；`task_notify`
  只服务 `api`、`web`、`cron` 来的任务。

### 10.4 通知里的链接（`gateway.public_url`）

通知最后一行是任务页面地址，由可选配置项拼出：

```yaml
gateway:
  # 外部访问 web 的根地址，只用于拼通知里的链接，不影响监听地址。
  public_url: "https://forebrain.example.com"
```

没配置（默认）就不出"查看"这一行；配了但网关前面有反向代理，也只需要填外部可见的那个地址。
