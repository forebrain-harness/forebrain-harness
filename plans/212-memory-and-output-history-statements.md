# 212 — 记忆语句与全文检索三方言；工具输出历史在 MySQL / PostgreSQL 下进主库

- **基线**：`06f1bc57`（dev），并假定 203–211 已落地。
- **状态**：TODO
- **优先级 / 工作量 / 风险**：P1 / M / MED-HIGH（全文检索三家打分口径不同，相关度门槛要按方言校准）
- **依赖**：211 DONE。之后是 213。
- **设计**：`docs/plan/MULTI_DB_MULTI_REPLICA_DESIGN.md` §2.5、§2.6、§2.9；写法同 `plans/210-session-run-statements.md` §2。
- **执行隔离**：基于 dev HEAD 的 worktree 或主树 in-tree；**零 commit**（owner 手动提交）。

> **执行者须知**：先读 210 的 §2「写法」。碰到 §8 STOP 条件就停下报告。完成后把 `plans/README.md` 里 212 那一行的状态改为 DONE 并写一句结果。
>
> **Drift check（先跑）**：README 里 211 是 DONE；`go test ./pkg/architecture/ -run TestSQLiteStatementsUnchanged -count=1` 通过；`grep -n "fb_memory_fts" pkg/state/schema_mysql.sql pkg/state/schema_postgres.sql` 都有（209 建的）。

## 0. owner 裁决（执行期不得改）

1. **禁止任何 ORM**；**SQLite 的 SQL 一句不改**；MySQL、PostgreSQL 各写一套，运行时按库类型调用。
2. **全文检索用各库原生**：SQLite 照旧 FTS5；MySQL 内置 FULLTEXT（ngram）；PostgreSQL 内置 tsvector（`simple` + GIN），词在 Go 里切好，**不装扩展**。三家打分不同导致排序略有差异，owner 已知悉。
3. 多副本共享 home：共享存储上的 SQLite 文件不安全——工具输出历史在 MySQL / PG 模式下必须进主库。

## 1. 范围

| 文件 | 语句处 | 要点 |
|---|---|---|
| `pkg/memory/store.go` | 20 | upsert 1 处；4 个事务（209 已改 `dbx.BeginTx`）；1 处 SQLite 错误判断（209 已改 `dbx`） |
| `pkg/memory/jobs.go` | 22 | upsert 3 处；认领语句（`ClaimStage1JobsForStartup` 等，`jobs.go:149` 起） |
| `pkg/memory/ftsindex.go` | 7 | 全文检索，见 §2 |
| `pkg/tool/loaded_skills.go`（工具输出历史 `Store`） | 13 | 见 §3 |

## 2. 全文检索（`pkg/memory/ftsindex.go`）

### 2.1 现状

- 表（`pkg/state/schema.sql:561-567`）：`CREATE VIRTUAL TABLE fb_memory_fts USING fts5(terms, root UNINDEXED, path UNINDEXED, line_no UNINDEXED, tokenize='unicode61 remove_diacritics 2')`。`terms` 是 Go 切好、空格分隔的词（`searchTerms`）。
- 写入（`ftsindex.go:130-192`）：按文件维护一段**连续的 rowid 区间**——`fb_memory_index_files(first_rowid, line_count)` 记区间；删文件时 `DELETE FROM fb_memory_fts WHERE rowid >= ? AND rowid < ?`；新文件取 `SELECT IFNULL(MAX(rowid),0)+1 FROM fb_memory_fts` 作起点，逐行 `INSERT INTO fb_memory_fts(rowid, terms, root, path, line_no) VALUES(?,?,?,?,?)`（显式 rowid）。
- 查询（`ftsindex.go:218-220`）：

```go
`SELECT path, line_no, terms, bm25(fb_memory_fts) FROM fb_memory_fts
 WHERE fb_memory_fts MATCH ? AND root = ?`, expression, root
```

  `matchExpression`（260-279）：每个词加双引号、`OR` 连接。
- 打分方向：bm25 **越小越相关**（负数）；`pkg/memory/backend.go:562` `if match.score > MinIndexRelevance { continue }`，`MinIndexRelevance = -2.5`（849 行，注释说明是在真实语料上校准的）；排序 `backend.go:855-856` 升序。

### 2.2 MySQL / PG 结构（209 已建，这里核对）

- 普通表 `fb_memory_fts(row_id BIGINT PRIMARY KEY, terms LONGTEXT/TEXT, root, path, line_no)`——`row_id` **不自增**（代码显式指定区间）。
- MySQL：`FULLTEXT INDEX ft_terms (terms) WITH PARSER ngram`。
- PG：`terms_tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple', terms)) STORED` + `CREATE INDEX … USING GIN (terms_tsv)`。

### 2.3 语句改写

- 写入与删除：三联 `dbx.SQL`，MySQL / PG 用 `row_id` 代替 `rowid`；`IFNULL(MAX(rowid),0)+1` → MySQL `IFNULL(MAX(row_id),0)+1`、PG `COALESCE(MAX(row_id),0)+1`。
- 查询表达式：`matchExpression` 保持不变（SQLite 用）；新增 `matchExpressionMySQL(terms)`（BOOLEAN MODE：每个词用双引号包成短语、空格分隔，等价于「任一命中」；双引号与 `+-><()~*@` 等操作符字符剔除或转义）与 `matchExpressionPostgres(terms)`（`to_tsquery('simple', …)` 的参数：每个词转义成 `'词'`、用 ` | ` 连接；单引号、反斜杠、`&|!():*` 转义）。
- 查询语句：
  - MySQL：`SELECT path, line_no, terms, -MATCH(terms) AGAINST(? IN BOOLEAN MODE) AS score FROM fb_memory_fts WHERE MATCH(terms) AGAINST(? IN BOOLEAN MODE) AND root = ?`（表达式传两次）。
  - PG：`SELECT path, line_no, terms, -ts_rank(terms_tsv, to_tsquery('simple', $1)) AS score FROM fb_memory_fts WHERE terms_tsv @@ to_tsquery('simple', $1) AND root = $2`。
  - **取负号**，使三家都是「越小越相关」，`backend.go` 的排序与比较不用改。
- **单字词**（中文单字很常见）：MySQL ngram 默认 `ngram_token_size=2`，单字查不到。MySQL 分支对长度为 1 的词另加 `OR CONCAT(' ', terms, ' ') LIKE CONCAT('% ', ?, ' %')`（`terms` 是空格分隔的词串，这样是整词匹配），这类命中的分数取一个固定值（等于该方言门槛，见 2.4）。PG `simple` 对单字是正常词，无需处理。
- 最终是否命中仍由 Go 侧现有的整词比较决定（不改）。

### 2.4 相关度门槛按方言校准

- `MinIndexRelevance` 改为按方言取值的函数 `minIndexRelevance(d dbx.Dialect) float64`：SQLite 恒为 `-2.5`（不变）；MySQL / PG 的值用校准测试定。
- 校准测试（`pkg/memory/ftsindex_test.go`，设置 URL 时跑）：用 `ftsindex_test.go` 现有的语料与查询（235 行附近的用例），在 SQLite 上得出「通过门槛的行集合」作为期望；在 MySQL / PG 上找一个门槛，使通过的行集合与期望**完全一致**；把这个值写成常量并在注释里写明来源。做不到完全一致 → STOP，报告每个方言能达到的最好结果（差几行、哪几行）。

## 3. 工具输出历史（`pkg/tool/loaded_skills.go`）

- 现状：`OpenStore(path)`（601-620 行）打开**独立的 SQLite 文件** `<stateRoot>/state/outfilter/history.db`（调用处 `pkg/tool/shell_output.go:501`），自带 `historySchemaVersion = 1` 与 `PRAGMA user_version` 迁移（`loaded_skills.go:620-720`），表 `output_records`。
- 改法：
  - SQLite 模式：完全照旧（独立文件、原语句、原迁移）。
  - MySQL / PG 模式：`output_records` 建在**主库**里——在 `schema_mysql.sql` / `schema_postgres.sql` 加这张表（列与 `historySchemaSQL` 一致，`id` 为自增主键），并给主库加一次三方言迁移（版本 `stateSchemaVersion+1`；SQLite 方言的这次迁移是**空操作**，因为 SQLite 模式下这张表不在主库）。
  - `OpenStore` 改为 `OpenStoreFor(stateRoot string, mainDB *sql.DB)`：主库是 MySQL / PG → 直接用主库、不建文件；否则照旧打开文件。`shell_output.go:501` 的调用处拿到主库句柄（从 `AgentToolRuntime` 或进程环境取，执行时找最近的可达处；找不到干净的传递路径就 STOP）。
  - 13 处语句三联翻译（`PRAGMA` / `sqlite_master` 只在 SQLite 的文件迁移里，不翻译）。

## 4. 步骤

每步结束都跑：

```bash
CGO_ENABLED=1 go build -tags fts5 ./... && go vet -tags fts5 ./pkg/...
go test ./pkg/architecture/ -run TestSQLiteStatementsUnchanged -count=1
CGO_ENABLED=1 go test -tags fts5 ./pkg/memory/ ./pkg/tool/ -count=1 -p 2
```

1. `pkg/memory/store.go`、`jobs.go`。
2. `ftsindex.go` 写入 / 删除。
3. `ftsindex.go` 查询 + 两个表达式函数 + 单字词处理。
4. 门槛校准（2.4）。
5. 工具输出历史（§3）。
6. 三库矩阵（§6）。

## 5. 测试

- `pkg/memory/ftsindex_test.go`：表达式函数对特殊字符的转义（三方言各一组）；单字词在 MySQL 上能命中；校准测试。
- `pkg/tool`：MySQL / PG 模式下不产生 `history.db` 文件、记录写进主库；SQLite 模式照旧。
- `pkg/state/schema_migrations_test.go`：新迁移在 SQLite 上是空操作、在 MySQL / PG 上建出 `output_records`；结构一致性测试把 `output_records` 列为「仅 MySQL / PG 主库有」的已知差异。

## 6. 完成判据（机器可查）

```bash
for url in 'mysql://root:fb@127.0.0.1:13306/fbt' 'postgres://postgres:fb@127.0.0.1:15432/postgres?sslmode=disable'; do
  FOREBRAIN_TEST_DATABASE_URL="$url" CGO_ENABLED=1 go test -tags fts5 ./pkg/state/... ./pkg/memory/... ./pkg/tool/... ./pkg/process/... ./pkg/migrate/... -count=1 -p 2
done
CGO_ENABLED=1 go test -tags fts5 ./... -count=1 -p 2        # SQLite 全量全绿
go test ./pkg/architecture/ -run TestSQLiteStatementsUnchanged -count=1
grep -rn "dbx.SQL{" pkg --include='*.go' | grep -v _test | wc -l   # 写进 README 结果
```

至此**所有**库测试在三种库上全绿（210、211 遗留的暂时失败必须清零）。

## 7. 维护注意

- 记忆搜索的门槛今后若在 SQLite 上重新校准，MySQL / PG 的门槛也要重跑校准测试。
- 网关全量测试在 MySQL / PG 上的覆盖由 213 的多副本集成测试补齐。

## 8. STOP 条件

- 2.4 的校准做不到与 SQLite 完全一致。
- MySQL 的 ngram 全文索引在 `utf8mb4_bin` 排序规则下建不出来或查询行为异常（例如大小写），且换列级排序规则也解决不了。
- 工具输出历史的调用处拿不到主库句柄。
