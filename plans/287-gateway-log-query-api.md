# Plan 287: 网关日志查询接口——按时间、级别、来源、项目、会话、类型查，一次调用的请求与响应单独取

> **执行者须知**：逐步执行，每步跑完验证命令、对照预期再往下。遇到「STOP 条件」任一条，停下报告，不要自行发挥。
> **禁止任何 git commit / push**（owner 手动审阅提交），改动只留在工作区。完成后更新 `plans/README.md` 里本计划那一行的状态；若派发你的评审方说明由它维护索引，则跳过。
>
> **Drift check（开工前只跑一次）**：
>
> ```sh
> SCOPE="pkg/gateway/list_page.go pkg/gateway/api_extra.go pkg/telemetry"
> git status --porcelain -- $SCOPE
> git diff 17b4633a -- pkg/gateway/list_page.go
> ls pkg/telemetry/eventquery.go 2>/dev/null
> ```
>
> 预期：`pkg/telemetry` 只有 284–286 的改动；`pkg/gateway/list_page.go` 无改动；第三条无输出。`pkg/gateway/api_extra.go` 有他人改动时，只核对下文「现状」引用的两段（路由注册处、`handleRuntimeInfo`）内容是否一致；不一致即 STOP。

## 状态

- **优先级**：P2
- **工作量**：M
- **风险**：MED（新增可读取对话内容的接口；读文件路径来自请求参数，须防目录穿越）
- **依赖**：`plans/284-…`、`plans/285-…`、`plans/286-…`（都必须已 DONE）
- **类别**：direction（新功能）
- **制定基线**：`17b4633a`，分支 `dev`，2026-11-11

## 为什么要做

284–286 之后，每条日志都带来源、进程、项目、会话、轮次、代理，每次模型调用有自己的请求与响应文件。但它们还只是磁盘上的文件：owner 要在网页上「按时间范围查 info/error/warn/debug、不同项目、不同会话、不同来源（终端与网关）」并能点开一次调用。本计划提供只读查询接口；288 是页面。

网关与 TUI 共用同一个 `FOREBRAIN_HOME`（多副本部署也共享 home，见 `deploy/k8s/manifests/pvc.yaml`），所以网关直接读 `<home>/logs` 就能看到所有进程的记录。

## 现状（执行者需要的事实，全部内联）

### 磁盘格式（284/286 定稿）

```
<home>/logs/<YYYY-MM-DD>/<source>-<host>-<pid>-<proc>.jsonl        一行一条记录，可能有续段 …-<proc>.<n>.jsonl
<home>/logs/<YYYY-MM-DD>/stderr-<source>-<host>-<pid>-<proc>.log   进程原始 stderr（无结构）
<home>/logs/<YYYY-MM-DD>/x/<id>.req.gz | .resp.gz | .args.gz | .result.gz
```

记录的键：`t`（RFC3339 毫秒带时区）、`lvl`（`DEBUG|INFO|WARN|ERROR`）、`kind`（`log|turn|llm|tool|http|setup|panic`）、`msg`、`project`、`session`、`worker`、`run`、`agent`、`at`、`err`、`attrs`（对象）、`blobs`（`{名字: {name, bytes, whole, omitted}}`）。`source`（`tui|gateway|cli`）、`host`、`pid`、`proc` 只在**文件名**里。日期目录按写入方本地日期命名。

`kind=llm` 的记录成对出现：`msg="llm request"` 与 `msg="llm response"`，`attrs.x` 相同；响应记录带 `status`、`outcome`、`dur_ms`、`ttfb_ms`、`max_gap_ms`、`req_bytes`、`resp_bytes`、`model`、`url`、`method`。报文文件内容是「起始行 + 头行 + 空行 + 体」的纯文本（gzip）。

记录里的 `project` 是**项目键**（`state.Project.ProjectKey`，由项目根路径算出），不是项目 ID（`state.Project.ID`，形如 `prj_project-…`）。

### 网关的既有约定

- 路由注册在 `pkg/gateway/api_extra.go`，形如：
  ```go
  api.Get("/runtime", s.handleRuntimeInfo)
  ```
  这个 `api` 组已经套了登录校验；新路由加在这一行附近即可，不需要自己做鉴权。
- 所有分页列表走 `pkg/gateway/list_page.go`：`readListQuery(r)` 读 `q`/`sort`/`order`/`page`/`page_size`（默认 10、上限 `listMaxPageSize = 100`）；`pageOf(items, q)` 切页并在页码越界时落到最后一页；`writeListPage(w, key, rows, total, page, q, extra)` 输出 `{"<key>": rows, "total": n, "page": p, "page_size": s, …extra}`。**照用**。
- 处理器形状参照 `handleRuntimeInfo`（`pkg/gateway/api_extra.go`）：
  ```go
  func (s *Server) handleRuntimeInfo(w http.ResponseWriter, r *http.Request) {
  	if r.Method != http.MethodGet {
  		http.Error(w, "method", http.StatusMethodNotAllowed)
  		return
  	}
  	…
  	writeJSON(w, payload)
  }
  ```
- `s.Home` 是 home 目录；`s.Sessions` 是 `*state.SessionStore`；`s.projectStore()` 返回 `*state.ProjectStore`。
- **`pkg/gateway` 已有 34 个生产文件，正好是 `pkg/architecture` 的 `TestPackagesStayUnderThirtyProductionFiles` 的上限（常量 `maxProductionFilesPerPackage = 34`）。不能新建文件**：处理器写进 `pkg/gateway/list_page.go`（它本就是「列表端点」的家），测试写进 `pkg/gateway/list_page_test.go`（已存在则追加，不存在则新建——测试文件不计入上限，但必须与生产文件同名）。
- 测试脚手架：`pkg/gateway/api_extra_test.go` 的 `cronTestServer(t)` 造一个带临时 home 的 `*Server`，`cronRequest(t, s, method, target, body, handler)` 发请求并返回 `*httptest.ResponseRecorder`；`TestRuntimeInfoReportsHomeAndDirs` 是最小范例。

## 需要的命令

| 用途 | 命令 | 成功时 |
|------|------|--------|
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | 退出码 0 |
| 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry ./pkg/gateway -count=1` | 全 `ok` |
| 架构门 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | `ok` |
| vet / 格式 | `go vet ./... && gofmt -l pkg cmd` | 无输出 |

## 范围

**In scope**：
- `pkg/telemetry/eventquery.go`、`pkg/telemetry/eventquery_test.go`（新建）
- `pkg/gateway/list_page.go`（追加三个处理器）、`pkg/gateway/list_page_test.go`
- `pkg/gateway/api_extra.go`（**只加三行路由注册**）
- `docs/deploy/README.md`（加「日志」一节）

**Out of scope**：
- `pkg/state` 与任何 SQL——不新增查询语句（会牵动三种方言与 `TestSQLiteStatementsUnchanged` 快照）。
- 写入侧（`eventlog.go`、`exchange.go`）——只读它们的产物。
- 实时推送 / WebSocket——owner 既有规则「不做实时推送」。
- 删除、导出、清空日志的接口——不做。
- `frontend/**`——288。

## 接口（定稿）

### `GET /api/logs`

查询参数：

| 参数 | 含义 | 默认 |
|------|------|------|
| `from`、`to` | RFC3339 时间；闭区间 | `to`=现在，`from`=`to` 减 1 小时 |
| `level` | 逗号分隔，取值 `DEBUG,INFO,WARN,ERROR` | `INFO,WARN,ERROR` |
| `source` | `tui` / `gateway` / `cli`；空=全部 | 空 |
| `project` | 项目 **ID**；网关换成项目键再查 | 空 |
| `session` | 会话 ID（匹配记录的 `session`） | 空 |
| `run` | 轮次 ID | 空 |
| `kind` | 记录类型；空=全部 | 空 |
| `q` | 不分大小写的子串，匹配整行原文 | 空 |
| `page`、`page_size` | 同 `list_page.go`；本端点默认 `page_size=50` | 1 / 50 |

响应（`writeListPage`，键 `records`）：

```json
{
  "records": [
    {"id":"2026-11-11/tui-mbp-dev-89572-a3f09c.jsonl:184220",
     "t":"2026-11-11T11:20:57.545+08:00","lvl":"WARN","kind":"llm","msg":"llm response",
     "source":"tui","host":"mbp-dev","pid":89572,"day":"2026-11-11",
     "project":"…","project_id":"prj_…","project_name":"forebrain-harness",
     "session":"cli-…","session_title":"Gemini 工具循环 400 排查",
     "run":"…","agent":"main","at":"pkg/telemetry/exchange.go:171","err":"",
     "attrs":{…},"blobs":{…}}
  ],
  "total": 22, "page": 1, "page_size": 50,
  "level_counts": {"DEBUG": 31, "INFO": 19, "WARN": 2, "ERROR": 1},
  "buckets": [{"t":"2026-11-11T11:17:57+08:00","debug":0,"info":2,"warn":0,"error":0}, … 共 60 个],
  "truncated": false
}
```

- 排序固定为时间倒序（同一毫秒按文件名、偏移倒序，保证翻页稳定）。
- `total`、`buckets` 统计的是**包含级别条件**的结果；`level_counts` 统计的是**去掉级别条件**后的各级数量（页面的级别按钮上要显示「点开会多出多少」）。`buckets` 把 `[from, to]` 均分 60 段。
- `id` = `<日期>/<文件名>:<行起始偏移>`，稳定、可用于前端选中行。
- `truncated=true` 表示扫描触到上限（见下），此时 `total` 是下限。
- 参数错误（时间解析失败、`from` 晚于 `to`、未知级别）→ 400 与一句英文原因。

### `GET /api/logs/exchange?day=<YYYY-MM-DD>&id=<调用 ID>`

把一次模型调用拼成一个对象：

```json
{
  "id":"a3f09c-000391","day":"2026-11-11",
  "request":  {"t":"…","method":"POST","url":"…","model":"…","bytes":1176204,"whole":true,"omitted":0,"blob":"a3f09c-000391.req.gz"},
  "response": {"t":"…","status":400,"outcome":"http_error","dur_ms":5195,"ttfb_ms":5190,"max_gap_ms":5190,"bytes":156,"blob":"a3f09c-000391.resp.gz"},
  "scope": {"source":"tui","host":"…","pid":82141,"project":"…","project_name":"…","session":"…","session_title":"…","run":"…","agent":"main","worker":""},
  "summary": {"text":"…","reasoning":"…","tool_calls":[{"name":"shell","arguments":"{…}"}],"finish_reason":"tool_calls","usage":{"input":75662,"output":937},"events":11},
  "concurrent": [{"id":"…","day":"…","session":"…","session_title":"…","agent":"…","t0":"…","t1":"…","status":200}]
}
```

- `response` 为 `null` 表示只有请求记录（调用还在进行，或进程在收尾前死了）。
- `summary` 由 `SummarizeResponse` 从响应报文解析（见下）；非 2xx 或解析不出任何内容时为 `null`。
- `concurrent`：同一天内与本次调用时间段有重叠的其它调用，最多 8 条，按开始时间排序——页面用它画「同一时刻的其他调用」。

### `GET /api/logs/blob?day=<YYYY-MM-DD>&name=<文件名>[&offset=&limit=][&download=1]`

- 解压后按字节返回一段：`offset` 默认 0，`limit` 默认 262144（256KB）、上限 1048576。响应头 `X-Log-Blob-Bytes: <解压后总字节数>`，`Content-Type: text/plain; charset=utf-8`。
- `download=1`：整份解压流式返回，`Content-Disposition: attachment; filename="<去掉 .gz 的名字>.txt"`。
- 文件还在写（流未结束）时解压到 `io.ErrUnexpectedEOF` 为止，已读到的部分照常返回——这正是「看卡住的流现在停在哪」的用法。
- **路径校验（必须）**：`day` 匹配 `^\d{4}-\d{2}-\d{2}$`；`name` 匹配 `^[A-Za-z0-9][A-Za-z0-9._-]*\.gz$` 且不含 `..`；拼出的路径经 `filepath.Clean` 后必须仍以 `<home>/logs/<day>/x/` 为前缀。任一不满足 → 400。不存在 → 404。

## 读取侧实现（`pkg/telemetry/eventquery.go`）

```go
type EventQuery struct {
	From, To time.Time
	Levels   []string // 空 = 全部
	Sources  []string // 空 = 全部
	Project, Session, Run, Kind, Text string
	Page, PageSize int
}
type EventRow struct {
	ID, Day, Source, Host, Proc string
	PID int
	Record
}
type EventBucket struct{ T time.Time; Debug, Info, Warn, Error int }
type EventPage struct {
	Rows []EventRow; Total, Page int
	LevelCounts map[string]int; Buckets []EventBucket; Truncated bool
}
func QueryEvents(ctx context.Context, root string, q EventQuery) (EventPage, error)
func ReadExchange(ctx context.Context, root, day, id string) (Exchange, error) // 结构对应上面的 JSON（不含名称补全）
func OpenBlob(root, day, name string) (io.ReadCloser, int64, error) // 解压流、解压后总字节数（-1 = 未知/仍在写）
func SummarizeResponse(body []byte) *ResponseSummary
```

> **2026-11-12 owner 追加（覆盖上面的「定稿」）**：`QueryEvents` 与 `ReadExchange` 接收 `context.Context`（扫描类读取同形），在每个**文件边界**检查取消/超时并在取消时返回 `ctx.Err()`；网关两个处理器传 `r.Context()`，取消时不写任何状态（客户端已走），其余错误码不变。`OpenBlob` 保持原样（返回 `io.ReadCloser`，调用方靠关闭与响应写失败收口）。已实现并验证：`TestQueryEventsStopsWhenTheCallerIsGone`、`TestReadExchangeStopsWhenTheCallerIsGone`；收益有界（扫描本就被 512 MiB / 50 万命中封顶，100 MB 实测 73–157 ms），但「调用方已走仍继续读盘」不该出现在 HTTP 读路径上。

`QueryEvents`：

1. 日期目录：从 `From` 的本地日期减 1 天到 `To` 的本地日期加 1 天（写入方时区可能与网关不同），只取名字是日期的目录。
2. 文件：目录下 `*.jsonl`；文件名按 `<source>-<host>-<pid>-<proc>[.<n>].jsonl` **从右往左**拆（主机名里可以有 `-`：最后一段是 `proc`，倒数第二段是 `pid`，第一段是 `source`，中间是 `host`）；`Sources` 不含该来源的文件整个跳过。文件修改时间早于 `From` 的也跳过（文件只追加，mtime 是最后一条记录的时间）。
3. 逐行扫描（`bufio.Reader`，单行上限 128KB，超长行跳过并计数）。先做便宜的字节级预筛，再解码：
   - `Session` 非空而行里不含 `"session":"<值>"` → 跳过；`Run`、`Kind`、`Project` 同理；
   - `Text` 非空而 `bytes.Contains(bytes.ToLower(line), lowerText)` 为假 → 跳过；
   - 取 `"t":"…"` 解析时间，不在 `[From, To]` → 跳过；
   - 到这里计入 `LevelCounts[lvl]`；级别不在 `Levels` → 跳过；
   - 计入 `Buckets`，并把 `(时间, 文件序号, 偏移)` 记入命中索引（每条 24 字节，**不保留行内容**）。
4. 命中索引按时间倒序排序；`Total = len(索引)`；切出本页；只对本页的条目回到文件 `Seek` 到偏移重新读那一行并完整解码。
5. 另：目录下的 `stderr-*.log` 若非空，各合成一条记录——`kind="stderr"`、`lvl="ERROR"`、时间取文件 mtime、`msg="process stderr output"`、`attrs={"bytes": 大小, "tail": 末尾 4KB}`——与其它记录一起参加筛选排序（这样运行时崩溃输出能在页面上按时间看到）。
6. 上限：累计扫描字节超过 `512MB` 或命中索引超过 `500,000` 条即停，`Truncated=true`。

`SummarizeResponse(body)`：`body` 是报文里空行之后的部分。按行取 `data: ` 之后的 JSON（非 SSE 的整段 JSON 当作一个事件），对每个事件按**形状**取值，四种协议都认，认不出的事件只计数：

| 形状 | 正文 | 思考 | 工具调用 | 结束原因 / 用量 |
|------|------|------|----------|-----------------|
| OpenAI 兼容 | `choices[0].delta.content` | `choices[0].delta.reasoning_content` 或 `.reasoning` | `choices[0].delta.tool_calls[]`（按 `index` 拼 `function.name` 与 `function.arguments`） | `choices[0].finish_reason`；`usage.prompt_tokens` / `completion_tokens` |
| Anthropic | `type=content_block_delta` 且 `delta.type=text_delta` → `delta.text` | `delta.type=thinking_delta` → `delta.thinking` | `content_block_start` 的 `content_block.type=tool_use` 取 `name`，`input_json_delta` 的 `delta.partial_json` 按 `index` 拼 | `message_delta` 的 `delta.stop_reason`；`usage.input_tokens` / `output_tokens` |
| Responses | `type=response.output_text.delta` → `delta` | `type=response.reasoning_summary_text.delta` → `delta` | `type=response.output_item.done` 且 `item.type=function_call` → `item.name` / `item.arguments` | `response.completed` 的 `response.status`；`response.usage.input_tokens` / `output_tokens` |
| Gemini | `candidates[0].content.parts[].text`（`thought` 不为真） | 同上但 `thought=true` | `parts[].functionCall` → `name`、`args`（序列化回 JSON） | `candidates[0].finishReason`；`usageMetadata.promptTokenCount` / `candidatesTokenCount` |

这是**展示用的尽力解析**，不替代适配器；任何解析错误都不报错，只是字段留空。注释里写明这一点。

## 步骤

### Step 1：`eventquery.go`——查询

实现 `EventQuery`、`EventRow`、`EventPage`、`QueryEvents`（含文件名解析、预筛、分桶、截断上限、stderr 合成记录）。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/telemetry/` → 退出码 0。

### Step 2：`ReadExchange`、`OpenBlob`、`SummarizeResponse`

`ReadExchange` 只扫 `day` 那一个目录（外加前一天：跨午夜的调用请求在前一天），预筛 `"x":"<id>"`。`concurrent` 用同一次扫描里所有 `kind=llm` 的记录按 `x` 配对后算重叠。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry -run 'TestQueryEvents|TestReadExchange|TestOpenBlob|TestSummarizeResponse' -count=1` → `ok`。

### Step 3：三个处理器

在 `pkg/gateway/list_page.go` 追加 `handleLogs`、`handleLogExchange`、`handleLogBlob`，`pkg/gateway/api_extra.go` 的 `api.Get("/runtime", …)` 下面加：

```go
	// The log page (plan 287): every process's records under <home>/logs, read
	// only. One call's request and response are fetched on their own.
	api.Get("/logs", s.handleLogs)
	api.Get("/logs/exchange", s.handleLogExchange)
	api.Get("/logs/blob", s.handleLogBlob)
```

名称补全（只对**本页**的记录做）：

- 项目：用 `s.projectStore()` 的列表方法取全部项目（`grep -n "^func (s \*ProjectStore) List" pkg/state/project_store.go` 选不带分页、返回全部的那个；找不到这样的方法即 STOP），建 `ProjectKey → {ID, Name}` 与 `ID → ProjectKey` 两张表。请求参数 `project`（ID）先换成键再交给 `QueryEvents`；ID 不存在 → 返回空页（不是 400）。
- 会话标题：本页去重后的会话 ID 逐个点查。用 `GET /api/chat/sessions/{id}` 的处理器取标题的**同一个 store 调用**（`grep -n "chat/sessions/{" pkg/gateway/api_extra.go` 找到处理器，照它那一行）。查不到的会话标题留空，不报错。`s.Sessions` 为 `nil` 时全部留空。
- 本端点 `page_size` 默认 50：`readListQuery` 默认 10，所以在请求没带 `page_size` 时把 `q.PageSize` 设成 50。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -run 'TestLogs' -count=1` → `ok`。

### Step 4：部署文档

`docs/deploy/README.md` 加一节「日志」：目录布局、三种来源、保留 7 天 / 1GiB、多副本各写各的文件、离线环境无额外依赖、`FOREBRAIN_LLM_HTTP_DEBUG=0` 只留元数据不留报文、终端里 `tail -f <文件> | jq -c '[.t,.lvl,.session,.msg]'` 的读法。

**验证**：`grep -n "jsonl" docs/deploy/README.md` → 至少 1 处。

### Step 5：全量回归

**验证**：「需要的命令」表逐条全绿。

## 测试计划

`pkg/telemetry/eventquery_test.go`（用 284 的 `OpenEventLog` + `Write` 造数据，`t.TempDir()`）：

- `TestQueryEventsFiltersByEveryDimension`：两个来源、两个项目、三个会话、四种级别、三种类型的记录；逐个维度断言 `Total` 与返回行的字段。
- `TestQueryEventsTimeRangeAcrossDays`：记录跨三天；范围取中间一天半 → 只返回范围内的，且读了相邻日期目录。
- `TestQueryEventsNewestFirstAndStablePaging`：200 条其中 50 条同一毫秒；翻 4 页拼起来恰好 200 条、无重复、时间不增。
- `TestQueryEventsLevelCountsIgnoreLevelFilter`：只要 ERROR 时 `LevelCounts` 仍给出四级各自数量，`Total` 只算 ERROR。
- `TestQueryEventsBucketsCoverTheRange`：60 个桶、起点 = `From`、各桶计数之和 = `Total`。
- `TestQueryEventsParsesHostWithDashes`：文件名 `gateway-gw-pod-7f9c-4117-51be02.jsonl` → `Source=gateway`、`Host=gw-pod-7f9c`、`PID=4117`、`Proc=51be02`；续段 `….1.jsonl` 同样。
- `TestQueryEventsTextIsCaseInsensitive`。
- `TestQueryEventsSkipsOverlongAndBrokenLines`：文件里夹一行非 JSON 与一行 200KB → 其它行照常返回，不报错。
- `TestQueryEventsSurfacesProcessStderr`：放一个非空 `stderr-tui-….log` → 多一条 `kind=stderr` 的记录，`attrs.tail` 是文件末尾。
- `TestQueryEventsStopsAtScanLimit`：把上限经包内变量调小 → `Truncated=true`。
- `TestReadExchangePairsRequestAndResponse`、`TestReadExchangeWithoutResponse`（`response` 为空）、`TestReadExchangeListsConcurrentCalls`（三次重叠调用、不同会话 → `concurrent` 有另外两条且会话正确）。
- `TestOpenBlobRejectsTraversal`：`name` 为 `../x.gz`、`a/b.gz`、`x.txt`，`day` 为 `..` → 全部报错，且没有打开任何文件。
- `TestOpenBlobReadsUnfinishedStream`：一个没写结尾的 gzip → 已有内容读得出。
- `TestSummarizeResponse`：表驱动，四种协议各一段最小 SSE 夹具（写在测试文件里的字符串常量）+ 一段非 SSE 的错误 JSON（→ `nil`）+ 一段乱码（→ `nil`，不 panic）。

`pkg/gateway/list_page_test.go`（照 `TestRuntimeInfoReportsHomeAndDirs`）：

- `TestLogsListsRecordsWithNames`：往 `s.Home/logs` 写记录、建一个项目与一个会话 → 响应里 `project_name`、`session_title` 正确，`page_size` 默认 50。
- `TestLogsProjectFilterTakesTheProjectID`。
- `TestLogsRejectsBadParameters`：`from` 晚于 `to`、`level=LOUD`、`from=abc` → 400。
- `TestLogsRejectsNonGet`：POST → 405。
- `TestLogExchangeAndBlob`：取一次调用的对象；`/api/logs/blob` 取 `offset=10&limit=20` 得到恰好 20 字节且 `X-Log-Blob-Bytes` 正确；`download=1` 带 `Content-Disposition`。
- `TestLogBlobRejectsTraversal`：`name=../../forebrain.yaml.gz` → 400。

## 完成判据（全部满足）

- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` 退出码 0；`go vet ./...`、`gofmt -l pkg cmd` 无输出
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/telemetry ./pkg/gateway -count=1` 全 `ok`，上面的测试都存在且通过
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` 为 `ok`（特别是文件数上限与 SQL 快照两条）
- [ ] `ls pkg/gateway/*.go | grep -v _test.go | wc -l` 仍是 34
- [ ] `git diff 17b4633a -- pkg/gateway/api_extra.go` 在本计划范围内只多出三行路由与其注释
- [ ] `git status --porcelain -- pkg/state` 无本计划引入的改动
- [ ] `plans/README.md` 状态行已更新

## STOP 条件

- 284–286 任一未完成，或磁盘格式与本文「现状」不符（先核对 `pkg/telemetry/eventlog.go` 的 `Record`）。
- 找不到「返回全部项目」的 store 方法，或找不到按 ID 取单个会话标题的既有调用——不要新增 SQL，回报。
- 需要在 `pkg/gateway` 新建生产文件。
- 路径校验的任一测试无法通过。

## 非功能要求

| 维度 | 风险 | 对策 | 验证 |
|------|------|------|------|
| 延迟 | 7 天范围全扫 | 字节级预筛在解码之前；按文件名跳过来源、按 mtime 跳过过旧文件；只对本页条目完整解码 | 加 `BenchmarkQueryEvents`（100MB 合成记录、带会话条件），结果写进执行报告，预期 < 1.5s |
| 内存 | 大范围命中很多 | 索引每条 24 字节、上限 50 万条（12MB）；不缓存行内容 | `TestQueryEventsStopsAtScanLimit` |
| 鲁棒 | 读到正在写的文件、半行 | 以换行为界，最后一个不完整的行跳过；坏行跳过 | `TestQueryEventsSkipsOverlongAndBrokenLines` |
| 鲁棒 | 清理任务删掉正在读的目录 | 打开失败的文件跳过，不让整次查询失败 | 代码评审 |
| 安全 | 报文含对话内容与系统提示词 | 只挂在已鉴权的 `api` 组下；凭据在写入侧已脱敏（286） | `TestLogsRejectsNonGet`、路由注册位置评审 |
| 安全 | 目录穿越 | `day` / `name` 白名单正则 + `Clean` 后前缀校验 | `TestOpenBlobRejectsTraversal`、`TestLogBlobRejectsTraversal` |
| 多副本 | 各副本读到的结果不同 | 所有副本读同一个共享 home；无进程内缓存 | 文档一节 |
| 可扩展 | 新记录类型、新协议 | 未知 `kind` 原样返回；`SummarizeResponse` 认不出的事件只计数 | `TestSummarizeResponse` 的乱码用例 |

## 维护注意

- 查询是「扫文件」而不是索引：记录量再涨一个数量级时，先考虑缩短默认时间范围或按会话分文件，而不是引入数据库（owner 已裁决日志不进状态库）。
- `SummarizeResponse` 是展示逻辑，适配器改了流格式时这里可能显示不全，但不会影响运行；发现显示不全就补一行夹具与取值规则。
- 评审重点：三个处理器是否都在已鉴权的组里；`OpenBlob` 的校验是否在任何文件系统调用之前；名称补全是否只对本页做（不要对全部命中做）。
- 已知未做（有意）：实时跟随、导出、按属性任意过滤（现在只有 `q` 子串）。
