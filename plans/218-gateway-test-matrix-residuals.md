# 218 — `pkg/gateway` 测试三库矩阵收尾（217 之后的残余测试侧方言/生命周期问题）

- **基线**：`9ea9f4a0`（dev 顶端；217 已 rebase 后 ff-only 并入，主树终态双包测试 ok）。
- **状态**：DONE（2026-11-04 执行 + 评审 APPROVE（测试侧），分支 `exec/218` 5 笔，已合入 dev `7d505aa3`。矩阵「三次 ok」剩余两腿的根因经评审核实为两个真实产品缺口——A）`waitRecoveryScan` 的 DISTINCT 剥离漏 MySQL（213 的三库回归测试在未修复 dev 上对 MySQL 就是红的，评审亲测）；C）`PublishInbound` 裸 goroutine 无收口（评审 PG -count=6 6/6 确定性复现）——按 owner 裁定转出 219/220 本期必修，矩阵腿由其判据收口）
- **优先级 / 工作量 / 风险**：P2 / M / MED（全部是测试侧修法，但 55006 一类可能触及「网关测试的 server 生命周期收尾」，若发现产品缺干净停机口则 STOP 上报）
- **依赖**：217（25 条裸 `?` 已全部 `dbx.Rebind` 包装，`42601` 已清零）。
- **执行隔离**：worktree + `exec/218` 分支 + 增量提交；合并在 owner 批准后 ff-only。

> **Drift check（先跑）**：`git diff --stat <基线> -- pkg/turn pkg/gateway pkg/state/db.go` 应无输出（在干净检出上）；`grep -rnE '(SELECT|INSERT|UPDATE|DELETE)[^`"]*\?' --include='*_test.go' pkg/turn/ pkg/gateway/ | grep -v 'dbx.Rebind' | wc -l` 应为 **0**（217 已建立的度量：裸 SQL 行里没有不带 `dbx.Rebind` 的）。任一不符就 STOP。

## 0. 为什么做

217 把 `pkg/turn` / `pkg/gateway` 测试里的 25 条裸 `?` SQL 全部包进 `dbx.Rebind`，PG 上的 `SQLSTATE 42601` 从 **193 → 0**。但矩阵尚未「三次 ok」：PG 上 `pkg/gateway` 剩 **9 个失败（4 类）**，MySQL 上 `pkg/gateway` 有一簇失败（根因一条语句）。这些全部是**测试自身的方言/生命周期假设**，在基线上就被 42601 风暴掩盖（执行者在干净基线 `68700fb9` 上验证过：`pkg/gateway` 194 个失败 = 193×42601 + 1×23503，55006/LastInsertId 当时到不了那些代码路径）。217 按 §5 停下并转出本计划——修它们需要改测试 SQL 文本或断言，超出 217「SQL 一个字不改」的边界。

另注意：对 MySQL，`dbx.Rebind` 是恒等函数，所以 MySQL 侧的失败**与 217 的 diff 无关**，全部是既有问题。

## 1. 范围

**只动**：`pkg/gateway/**/*_test.go`（以及若 55006 类确需、测试 harness 里属于测试侧的 server 收尾代码——见 §3.4 边界）。

**不动**：产品代码（除非 §3.4 的 STOP 分支触发后 owner 另行裁决）；`pkg/turn`（PG/MySQL 已 ok）；其余包的测试。

## 2. 残余问题清单（2026-11-03 实测证据）

### 2.1 MySQL：`seedConfigRoot` 的 upsert 语法（一毒一大片）

`pkg/gateway/api_extra_test.go` `seedConfigRoot`（217 后约 `:213`）：

```go
INSERT INTO fb_config (section, value_json, updated_at_ms) VALUES(?,?,0)
ON CONFLICT(section) DO UPDATE SET value_json = excluded.value_json
```

`ON CONFLICT … excluded` 是 SQLite/PG 语法，**MySQL 8 不支持**（需要 `ON DUPLICATE KEY UPDATE value_json = VALUES(value_json)`）。所有经它播种配置的用例整簇失败：`TestPrimaryAgentsAPI*`、`TestAgentRoster*`、`TestConfigEndpointEditsTheFileOnDisk`、`TestHooksEndpointPagesTheFlattenedTable`、`TestProjectsCreate*`、`TestProjectDefaultTypeEndpoint`、`TestProjectPatchType*` 等（实测首屏 ≥19 个；MySQL 服务器中途死亡，完整清单要在修完这条后重跑确认）。

### 2.2 PG：`LastInsertId` 不被 pgx 支持（2 个失败）

`pkg/gateway/files_write_test.go:425`：`res.LastInsertId()` → `LastInsertId is not supported by this driver`。产品层自己的写法是 RETURNING（`pkg/state/session_store.go:2977` 一带），测试应对齐（见 §3.2）。

### 2.3 PG：FK 断言钉死 SQLite 措辞（1 个失败）

`pkg/gateway/wsevents_test.go:1399`：`require.Contains(t, msg.Error, "FOREIGN KEY")`——SQLite 报 `FOREIGN KEY constraint failed`（大写），PG 报 `violates foreign key constraint ...`（小写），PG 上 `Contains "FOREIGN KEY"` 失败（`SQLSTATE 23503` 的用例本体其实是对的，挂在校验文本）。

### 2.4 PG：scratch 库 drop 被后台连接占用（5 个失败，`SQLSTATE 55006`）

`drop scratch database fbt_…: being accessed by other users`，出现在 `skills_download_test.go:333`、`api_extra_test.go:1583` 一带的 cleanup。drop 助手在 `pkg/state/db.go:302/320`。归因：cron/心跳 ticker 等后台循环比测试活得久，drop 时仍握着连接。另有 1 个 `TempDir RemoveAll: directory not empty`（channels 测试 cleanup，同类竞态：后台进程还在写目录）。

## 3. 修法方向

1. **seedConfigRoot（§2.1）**：改成按方言分派——测试里用 `dbx.SQL{SQLite: 原句, MySQL: "INSERT … ON DUPLICATE KEY UPDATE value_json = VALUES(value_json)", Postgres: 原句}` 或等价的方言分支；语义不变（upsert fb_config 按 section）。这是**允许改测试 SQL 文本**的计划——217 的禁改令只属于 217。
2. **LastInsertId（§2.2）**：`userRowWithAttachments` 改为方言无关的取 id 方式：`INSERT … RETURNING id`（PG）＋ SQLite/MySQL 各自可用形式，或插入后按业务键查回。照抄产品层 RETURNING 范式，不要发明新写法。
3. **FK 断言（§2.3）**：断言接受两种引擎的措辞（如 `Contains "foreign key"`（大小写不敏感）或按方言给子串）——**不是放宽为不校验**，仍然必须证明失败原因是 FK。
4. **55006/TempDir（§2.4）**：先查 gateway 测试 harness 的 server 生命周期——t.Cleanup 里 DB drop 先于 server/ticker 停止就会竞态。优先**测试侧**修法（cleanup 顺序、显式 shutdown 等待）。若发现产品没有干净停掉这些 ticker 的口子（需要改产品代码才能保证 drop 前无连接），**STOP 上报**，那是一个产品缺口，另立计划。
5. 每修一类跑一次该类用例的单测（`-run` 定向），再进下一类。

## 4. 步骤与验证

1. §2.1 seedConfigRoot → `FOREBRAIN_HOME="$(mktemp -d)" FOREBRAIN_TEST_DATABASE_URL="mysql://root:fb@127.0.0.1:13306/fbt" CGO_ENABLED=1 go test -tags fts5 ./pkg/gateway -count=1 -run 'TestPrimaryAgentsAPIListsAndSwitches|TestProjectsCreateValidatesRoot'` → ok；SQLite 同用例 → ok。
2. §2.2 LastInsertId → PG 跑 `files_write` 相关用例 → ok。
3. §2.3 FK 断言 → PG 跑 `wsevents` 相关用例 → ok。
4. §2.4 55006/TempDir → PG 全包重跑，55006 清零。
5. **三库矩阵**（217 §6 #3 原样）：
   ```bash
   for url in '' 'mysql://root:fb@127.0.0.1:13306/fbt' 'postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable'; do
     FOREBRAIN_HOME="$(mktemp -d)" FOREBRAIN_TEST_DATABASE_URL="$url" CGO_ENABLED=1 \
       go test -tags fts5 ./pkg/turn ./pkg/gateway -count=1
   done   # → 三次 ok
   ```
6. 收口：SQLite 全量 `./... -count=1 -p 2` 全绿；`go vet -tags fts5 ./pkg/...` 无输出；`go test ./pkg/architecture/ -run 'TestSQLiteStatementsUnchanged' -count=1` ok。

## 5. 完成判据（机器可查）

```bash
# 1) 三库矩阵三次 ok（见 §4.5）
# 2) SQLite 全量全绿；vet 干净；快照守卫 ok（§4.6）
# 3) 裸占位符度量保持 0
grep -rnE '(SELECT|INSERT|UPDATE|DELETE)[^`"]*\?' --include='*_test.go' pkg/turn/ pkg/gateway/ | grep -v 'dbx.Rebind' | wc -l   # → 0
# 4) 无 t.Skip 新增、断言不得放宽为空校验
```

## 6. STOP 条件

- §3.4 需要**产品代码**改动才能让 drop 前无后台连接（产品缺停机口）。
- 修完 §2.1 后 MySQL 上出现**新类别**失败（非 seedConfigRoot 播种路径的）且无法用测试侧假设解释——逐条记录后停下。
- SQLite 全量出现新失败。
- 三库任一引擎的服务器起不来（环境问题，上报而非绕过）。

## 7. 环境注意（217 评审实测）

- 矩阵容器：`fb-mysql`（13306）/`fb-pg`（15432），`./scripts/test-db.sh up` 幂等拉起；**2026-11-03 评审期间宿主磁盘打满后 fb-mysql 死亡、Docker daemon 挂死（`docker ps` 超时）**——执行前先确认 `docker ps` 能返回，必要时 owner 重启 Docker Desktop。
- fresh worktree 跑测试的 pdfium 接线：只 `export PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)"`（pc 文件自带 rpath）；**不要**全局 export `CGO_LDFLAGS="-L… -lpdfium"`——它会给不依赖 pdfium 的 cgo 测试二进制塞进无 rpath 的 dylib 引用，运行时 `dyld: no LC_RPATH's found`。cgo 编译缓存会烙下 worktree 绝对路径，指向已删 worktree 时 `go clean -cache` 后带正确 PKG_CONFIG_PATH 重编。
- 磁盘紧张（<2Gi）时先 `go clean -cache`，代价是全量重建。

## 8. 维护注意

- `pkg/turn` 已三库全绿（217 验证），别在 218 里碰它。
- 修完后 `pkg/turn` + `pkg/gateway` 与 `pkg/state` 等包一样进矩阵常跑——新测试写 SQL 一律 `dbx.Rebind(dbx.DialectOf(db), …)`；需要方言分支的语句用 `dbx.SQL` 三联，别再造第二种写法。
