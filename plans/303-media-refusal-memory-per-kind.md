# Plan 303: 原生媒体拒收按类型记忆（v35 迁移 + 读侧降级）

> **Executor instructions**：按步骤执行，每步跑完验证命令并对照预期再往下。遇到「STOP 条件」任一条就停下报告，不要自行发挥。完成后不要改 `plans/`（索引由派发方维护）。
>
> **Drift check（开工第一步）**：
> ```sh
> git rev-parse --short HEAD          # 基线 c680aaed（dev）；后代提交亦可，按内容核对
> grep -n 'stateSchemaVersion = ' pkg/state/schema_migrations.go
> grep -n 'CREATE TABLE fb_session_image_refusals' -A 8 pkg/state/schema.sql
> grep -n 'func recordedImageRefusal' pkg/run/image_input.go
> grep -n 'func NativeMediaKinds' pkg/run/native_media.go
> ```
> 期望：`stateSchemaVersion = 34`；表主键 `(session_id, provider, model)`；三个函数都在。内容对不上即 STOP，行号漂移不算。

## Status

- **Priority**: P1（owner 2026-11-12 裁决：加媒体类型列，按类型记忆拒收）
- **Effort**: M（三方言迁移 + 存储层 + 读侧降级 + 拒收识别）
- **Risk**: MED-HIGH（动 schema 与已存表；迁移必须可回滚地前滚且保住既有行）
- **Depends on**: `plans/269`（原生媒体，已 DONE）、`plans/284`–`286`（无关但同基线）
- **Category**: bug fix / data

## Why this matters

269 的 Step 6 按计划 STOP 了：`fb_session_image_refusals` 的主键是 `(session_id, provider, model)`，没有媒体类型维度。把「PDF 被拒」写进这一行，`ImageTurnRefusal` 会把它当成「图片被拒」（反过来也一样），所以当时没有实现记忆。现状后果：模型行声称支持某类媒体、provider 真拒时，**该回合直接报 provider 错误**，下一回合仍会重发原生部件，每次都失败。

owner 2026-11-12 裁决：**加媒体类型列**，按类型分别记忆与降级。这是唯一根因解（不加列的替代方案都会把两类拒收混在一起）。

## Current state（执行者需要的事实）

- 表（`pkg/state/schema.sql:234-241`，MySQL/PG 两份镜像在 `schema_mysql.sql` / `schema_postgres.sql`）：
  ```sql
  CREATE TABLE fb_session_image_refusals (
    session_id TEXT NOT NULL REFERENCES fb_sessions(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    model TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    observed_at INTEGER NOT NULL,
    PRIMARY KEY (session_id, provider, model)
  ) STRICT, WITHOUT ROWID;
  ```
- 迁移阶梯：`pkg/state/schema_migrations.go` 的 `stateSchemaVersion = 34`，每一步由 `migrateStateVNNToVMM{,MySQL,Postgres}` 三个函数组成，登记在表里（v33→v34 见 `:107-109`、实现 `:1732-1790`，`ALTER TABLE ... ADD COLUMN` 的形状照它写）。
  - **SQLite 不能改主键**：v35 的 SQLite 臂必须建新表（`fb_session_image_refusals_v35`）→ `INSERT … SELECT`（`media` 填 `'image'`）→ `DROP TABLE` 旧表 → `ALTER TABLE … RENAME TO` → 重建索引；整个过程在一个事务里。MySQL/PG 用 `ALTER TABLE ADD COLUMN media TEXT NOT NULL DEFAULT 'image'` + 换主键（MySQL `DROP PRIMARY KEY, ADD PRIMARY KEY (...)`；PG `DROP CONSTRAINT … , ADD PRIMARY KEY (...)`，约束名按 v34/292 的写法查实际名字）。
  - 三份 `schema*.sql` 同步改，`stateSchemaVersion` 改 35。
- 存储层：`pkg/state/session_store.go` 的 `ImageRefusal(ctx, sessionID, provider, model)` 与 `RecordImageRefusal(ctx, sessionID, provider, model, reason)`。**改名/加参是破坏性 API**：本次给它们加一个 `media string` 参数（或新增 `MediaRefusal` 一对并让旧的转发 `"image"`，二选一，保持全仓调用点一致）。
- 写侧（图片）：`pkg/llm/image_heal.go` 的 `IsImageRejection` + `WithImageRefusalObserver`，`pkg/run/image_input.go:205-240`（`withImageRefusalRecorder`）把拒收写库。
- 读侧（图片）：`pkg/run/image_input.go:112-135`（`ImageTurnRefusal`）、`:178-193`（`PlanReviewImageRefusal`）。
- 原生媒体：`pkg/run/native_media.go` 的 `NativeMediaKinds(ctx, r, sessionID) llm.NativeMediaKinds` 决定「这个回合哪些类型可以原生发」；`llm.NativeMediaKinds` 有三个 bool（PDF/Audio/Video），`llm.MediaKind` 有 `MediaKindPDF/Audio/Video`；图片不在其中（`ContentTypeImageBase64` 另一条路）。
- `pkg/run/llm_middleware.go` 已经装 `withImageHeal`；媒体拒收的观察者从这里接。

## Scope

**In scope**：`pkg/state/{schema.sql,schema_mysql.sql,schema_postgres.sql,schema_migrations.go,session_store.go}` 及其测试；`pkg/run/{image_input.go,native_media.go,llm_middleware.go}` 及测试；`pkg/llm/native_media.go`（若把拒收识别放这里）及其测试。
**Out of scope**：269 的编码器、`pkg/turn/input_parts.go` 的降级文案（已定稿，逐字节不动）、`pkg/gateway`/`pkg/tui` 的展示、任何 UI。

## Steps

### Step 1：拒收识别（保守，先测试后实现）

`pkg/llm` 加 `IsMediaRejection(err error, kind MediaKind) (reason string, ok bool)`：命中条件必须同时满足
1. 错误来自 provider 的 4xx（沿用 `IsImageRejection` 的判定入口/文本提取方式）；且
2. 文本点名该类型（`pdf`/`document`、`audio`/`input_audio`、`video`/`video_url`）**或**给出标准的不支持短语（`unsupported content type`、`invalid content type`、`does not support`、`only supports text`）。

拿不到的、含糊的、5xx/超时/限流一律不命中（宁可这回合失败，也不要把原生发送永久关掉）。每条规则一个表驱动测试，正例反例成对。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -run 'TestIsMediaRejection' -count=1` → ok。

### Step 2：schema v35 + 迁移

按 Current state 的形状改三份 schema 与 `schema_migrations.go`：`media TEXT NOT NULL DEFAULT 'image'` 进列与主键，`stateSchemaVersion = 35`，三个 `migrateStateV34ToV35*` 登记进表。既有行全部变成 `media='image'`（它们本来就是图片拒收）。

**Verify**（全绿才算过）：
- `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'Migration|Schema' -count=1` → ok；
- 新增迁移测试：在 v34 库上预置两行图片拒收 → 迁移到 35 → 两行仍在且 `media='image'` → 同一 `(session,provider,model)` 还能再插入 `media='pdf'` 一行；SQLite 用 `pragma_table_info` 断言列集合与主键顺序，MySQL/PG 用 `information_schema`（照 `schema_migrations_test.go:754-800` 的既有写法）；
- `scripts/test-db.sh up && scripts/test-db.sh all`（先 `docker start fb-mysql fb-pg`，脚本不会启动已停止的容器）→ exit 0。

### Step 3：存储层

给 `ImageRefusal`/`RecordImageRefusal` 加 `media`（或新增 `MediaRefusal`/`RecordMediaRefusal` 并让图片路径显式传 `"image"`）。SQL 一律走 `dbx.Rebind(dbx.DialectOf(handle), …)`（plan 301/248 的不变量）。空 media 视为 `"image"`？**不**——空值返回参数错误，避免静默落错类型。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` → ok；新增「同 pair 两种 media 各存一行、读回各自 reason」的用例。

### Step 4：写侧接线 + 读侧降级

- 写侧：`pkg/run` 在媒体请求失败且 `IsMediaRejection` 命中时，把 `(session, provider, model, kind, reason)` 写库（复用 `withImageRefusalRecorder` 的形状；写库用独立 context，不阻塞重试/收尾）。**不要**在本回合自动重发（那是另一个决定）；本回合仍以错误结束，下一回合才降级。
- 读侧：`run.NativeMediaKinds` 读一次本会话的拒收记录，把命中的 kind 从结果里去掉（`llm.NativeMediaKinds` 的对应 bool 置 false），于是下一回合 269 的装配自动走既有降级文字，**不新增文案**。查询失败时按「没记过」处理并记一条 WARN（不能因为读日志失败而阻断回合）。
- 图片那条路（`ImageTurnRefusal`/`PlanReviewImageRefusal`）改传 `media="image"`，行为逐字节不变。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/run -count=1` → ok；新增用例：拒收 pdf 后 `NativeMediaKinds` 只剩 audio/video（图片不受影响）；拒收图片不影响 pdf；读库失败时三 kind 全在。

### Step 5：全量回归

`CGO_ENABLED=1 go build -tags fts5 ./...`、`go vet -tags fts5 ./...`、`gofmt -l pkg cmd` 空、`CGO_ENABLED=1 go test -tags fts5 ./pkg/state ./pkg/run ./pkg/llm ./pkg/architecture -count=1` → 全 ok。

## Test plan（必须新增）

1. `pkg/llm`：`IsMediaRejection` 正反例表（每 kind ≥2 正例：点名类型 / 标准短语；反例：5xx、超时、含糊 400、文本里出现 "video" 但与拒收无关）。
2. `pkg/state`：v34→v35 迁移三方言（同一测试跑矩阵）；同 pair 多 media 行共存；`media` 为空报错。
3. `pkg/run`：写侧 observer 落库一次（失败不重发）；读侧按 kind 降级；跨 kind 不串；读库失败不阻断。

## Done criteria

- [ ] `stateSchemaVersion == 35`；三份 schema 与三方言迁移一致；`scripts/test-db.sh all` exit 0
- [ ] 迁移后既有行 `media='image'`，且同 pair 可插入 pdf/audio/video 行
- [ ] `grep -rn 'fb_session_image_refusals' pkg --include='*.go'` 的每个写点都带 media；图片路径传 `"image"`
- [ ] 拒收 pdf 后下一回合该会话不再原生发 pdf（装配降级到既有文字），图片能力不受影响
- [ ] 上述三组新测试全 PASS；`go build`/`vet`/`gofmt` 干净
- [ ] 报告给出迁移前后的真实 SQL/列/主键证据

## STOP conditions

- 发现 SQLite 无法在不丢行的情况下重建该表（例如有外键引用它）→ 停下报告。
- 既有行无法确定类型（不是图片拒收）→ 停下报告，不要猜。
- 需要改 `fb_session_image_refusals` 之外的任何表 → 停下报告。
- 拒收识别需要 provider 返回体结构（而不只是文本）才能判定 → 停下报告，不要加新的错误类型注入。
- `scripts/test-db.sh all` 在未改动基线上也红 → 记录并停下，不要修既有红。

## 非功能要求

- 迁移：单事务、幂等判断按既有阶梯（版本号决定）；失败要能停在 v34（不半途改名）。
- 性能：读侧每回合一次查询（按 PK 命中，三条以内），不得进入每 part 循环；写侧一次插入。
- 可靠性：写/读失败都不改变回合成败语义；不新增无超时的等待。
- Maintainability：媒体类型字符串与 `llm.MediaKind` 同源，禁止在 `pkg/state` 里复制一份新枚举。
