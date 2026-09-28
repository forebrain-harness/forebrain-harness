# Plan 004：缺 cgo 或 FTS5 构建的二进制，打开状态库时用一句话说明原因与正确的安装命令

> **执行者须知**：逐步执行，每一步都要跑"验证"命令并确认结果与预期一致后才进入下一步。
> 出现"STOP 条件"中的任何一种情况，立即停止并汇报，不要自行发挥。完成后把
> `docs/plan/OSS_AUTOMATION/README.md` 里本计划的状态改成 DONE。
>
> **提交（里程碑 M3）**：本地步骤全部通过后，按 `README.md` 全局规则 1 提交**一次**：标题见 README 的
> "里程碑与提交标题"表，正文按 `.gitmessage` 模板（Why / What / Prompt cache / Verification，外加
> `Refs: docs/plan/OSS_AUTOMATION/<本文件名>`），用 `git commit -s -F <message-file>`；只 `git add` 本计划"范围"内的文件。
> 不得 `--amend`、`rebase`、`push --force`。开工前确认 `git status --short` 干净、`git log -1` 是上一个里程碑。
>
> **真实仓库实测**：按 README 的关卡推送后，在 `git@github.com:forebrain-harness/forebrain-harness.git` 上完成本文件"真实仓库实测"一节，
> 证据写入 `LIVE_TEST_LOG.md`。实测通过前本计划状态只能是 `AWAITING PUSH`。
>
> **绝不能碰真实数据**：验证一律使用临时 `FOREBRAIN_HOME`。
>
> **漂移检查（先做）**：本计划编写时仓库还没有提交，M0 之后才有历史。开工前逐条核对"现状"中的摘录与实际文件一致，并用
> `git log --oneline -- <范围内路径>` 查看 M0 之后是否有别的提交改过这些文件；有且与摘录不符，就按 STOP 处理。

## 状态

| 项 | 值 |
| --- | --- |
| 优先级 | P2 |
| 工作量 | S |
| 风险 | LOW |
| 依赖 | 无（原依赖的 003 已随 CI 自维护推迟；触发打开状态库改用 `forebrain gateway start`） |
| 类别 | dx |
| 编写于 | 2026-09-28，基于未提交的工作区 |

## 为什么要做

`go install` 不会自动带 `-tags fts5`。用户所在的 Linux 机器如果没有 C 编译器，Go 还会默认
`CGO_ENABLED=0`。这两种情况都能编译成功（本机已验证：`CGO_ENABLED=0 go build ./cmd/forebrain` exit 0），
但运行时一打开状态库就会失败：
- 缺 FTS5：建表时报 `no such module: fts5`；
- 缺 cgo：go-sqlite3 的 stub 报 "Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work"。

两种报错都没有告诉用户该怎么做。owner 的决定是：文档写明安装命令，并且缺失时**用一句话**说明原因和
正确命令。

真正的事实来源是运行中的 SQLite 引擎本身（`sqlite_compileoption_used('ENABLE_FTS5')`），不是 build tag。
因此检查放在状态库的打开入口 `state.Open` / `state.OpenReadOnly`，对一个内存库做一次探测。

## 现状

- `pkg/state/db.go:20` `func Open(ctx, absSQLitePath string, opts *OpenOptions) (*sql.DB, error)`：
  `sql.Open(sqliteDriverName(), dsn)` → `PingContext` → 只读分支 `requireCurrentSchemaVersion`，或读写分支
  `migrateStateSchema`。
- `pkg/state/db.go:150` `func OpenReadOnly(absSQLitePath string) (*sql.DB, error)`。
- `pkg/state/db.go:81–87`：驱动注册为 `"forebrain_sqlite"`（`sql.Register("forebrain_sqlite", &sqlite3.SQLiteDriver{})`），
  `sqliteDriverName()` 返回它。
- `pkg/state` 已有 20 个生产文件，**不能新建文件**，只能改 `db.go`。
- 项目规则：面向用户的错误提示只写一句话。

## 设计

在 `pkg/state/db.go` 增加：

```go
// errSQLiteBuild is what a forebrain built without cgo or without SQLite's
// FTS5 module reports: memory search declares an FTS5 table, so such a build
// cannot open its state at all, and the way out is to rebuild it.
var errSQLiteBuild = errors.New("this forebrain was built without cgo or SQLite FTS5; reinstall it with: CGO_ENABLED=1 go install -tags fts5 github.com/forebrain-harness/forebrain-harness/cmd/forebrain@latest")

var (
	sqliteBuildOnce sync.Once
	sqliteBuildErr  error
)

// requireSQLiteBuild asks the linked SQLite engine itself whether it can
// serve the state schema. A cgo-less build links go-sqlite3's stub, whose
// every query fails; a build without the fts5 tag answers 0.
func requireSQLiteBuild(ctx context.Context) error {
	sqliteBuildOnce.Do(func() {
		db, err := sql.Open(sqliteDriverName(), ":memory:")
		if err != nil {
			sqliteBuildErr = errSQLiteBuild
			return
		}
		defer db.Close()
		var fts5 int
		if err := db.QueryRowContext(ctx, `SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&fts5); err != nil || fts5 != 1 {
			sqliteBuildErr = errSQLiteBuild
		}
	})
	return sqliteBuildErr
}
```

`Open` 和 `OpenReadOnly` 第一行调用它，出错时直接返回。

说明：`:memory:` 探测失败只可能是驱动本身不可用（stub 或构建损坏），与用户的文件、权限无关，所以统一
归因为构建问题是准确的，不属于"把任意错误改写成自编解释"。真实文件的打开错误仍然原样返回。

## 需要的命令

| 用途 | 命令 | 成功时 |
| --- | --- | --- |
| state 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state -count=1` | ok |
| 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 全部 ok |
| vet | `go vet ./...` | exit 0 |
| 包图 | `scripts/package-graph.sh` | 只有 `pkg/state` 的 loc 变化 |

## 范围

**允许修改**：`pkg/state/db.go`、`pkg/state/db_test.go`（没有就新建，测试文件名要与被测文件对应）、
`pkg/architecture/testdata/graph.json`。

**不许碰**：`migrateStateSchema` 及 schema 相关代码；`cmd/forebrain` 的错误打印路径（它原样打印返回的错误，
正好是一句话）。

## 步骤

### 步骤 1：实现探测

按"设计"修改 `pkg/state/db.go`。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/state` exit 0

### 步骤 2：测试

在 `pkg/state/db_test.go` 新增：
- `TestSQLiteBuildServesTheStateSchema`：带 `-tags fts5` 时 `requireSQLiteBuild(ctx)` 返回 nil。
- `TestSQLiteBuildErrorIsOneSentenceWithTheInstallCommand`：`errSQLiteBuild.Error()` 包含
  `CGO_ENABLED=1 go install -tags fts5 github.com/forebrain-harness/forebrain-harness/cmd/forebrain@latest`，
  并且只含一个句末标点。按"句号后跟空格"或换行来判断，不计 URL 里的点。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/state -run 'TestSQLiteBuild' -count=1 -v` → 2 个 PASS

### 步骤 3：实机验证两种坏构建

用 `forebrain gateway start` 触发打开状态库：它不需要终端，且只要配置有效就会在绑定服务前打开状态库
（`cmd/forebrain/serve.go` 的 `ensureGatewayStartSetup` 只检查配置是否完整；配置不完整时它报的是配置错误，
到不了状态库）。配置用假 provider，`api_key` 必须写成 `${FAKE_LLM_KEY}` 引用（明文密钥会在启动时被拒绝），
`base_url` 指向一个没人监听的端口即可——流程到不了模型调用。

```bash
H=$(mktemp -d); P=$(mktemp -d)
CGO_ENABLED=1 go build -o $TMPDIR/fb-nofts5 ./cmd/forebrain          # 故意不带 -tags fts5
CGO_ENABLED=0 go build -tags fts5 -o $TMPDIR/fb-nocgo ./cmd/forebrain # 故意关 cgo
cat > "$H/forebrain.yaml" <<'YAML'
gateway:
  port: 6071
  auth:
    mode: none
agents:
  definitions:
    main:
      primary: true
      enable_subagent: false
      llm_providers:
      - provider: deepseek
        model: deepseek-chat
        api_key: ${FAKE_LLM_KEY}
        base_url: http://127.0.0.1:59999
YAML
for b in fb-nofts5 fb-nocgo; do
  (cd $P && FAKE_LLM_KEY=sk-local-fake FOREBRAIN_HOME=$H $TMPDIR/$b gateway start &
   pid=$!; sleep 20; kill $pid 2>/dev/null; wait $pid; echo "exit=$?")
done
```

说明：坏构建在打开状态库时就退出，`sleep 20; kill` 只是兜底（防止检查未触发时 gateway 一直运行）；macOS 没有
`timeout` 命令，所以用后台进程 + kill。

**验证**：两个二进制的 stderr 都恰好是一行 `this forebrain was built without cgo or SQLite FTS5; reinstall it with: …`，
`exit=1`。

### 步骤 4：收尾

全量测试、vet、`gofmt -l pkg`、包图。

## 真实仓库实测（关卡 G1）

用户最常犯的两种错误，直接用真实模块路径复现：
```bash
SHA=$(git log --format=%H --grep '^fix(state): explain a build without cgo' -1)
cd "$(mktemp -d)"
env -u GOFLAGS GOWORK=off CGO_ENABLED=1 GOBIN="$PWD/nofts5" go install github.com/forebrain-harness/forebrain-harness/cmd/forebrain@"$SHA"
env -u GOFLAGS GOWORK=off CGO_ENABLED=0 GOBIN="$PWD/nocgo" go install -tags fts5 github.com/forebrain-harness/forebrain-harness/cmd/forebrain@"$SHA"
```
对两个二进制，分别用临时 `FOREBRAIN_HOME` 加本地步骤 3 那份 fake provider 配置，执行 `gateway start`。

**验证**：两者的 stderr 都恰好是那一句 `this forebrain was built without cgo or SQLite FTS5; reinstall it with: …`，退出码 1。
记录两段输出。

## 完成标准

- [ ] M3 已按规范提交
- [ ] 真实仓库实测通过并记录
- [ ] 两个新测试通过；全量测试通过
- [ ] 步骤 3 两种坏构建都输出同一句话并以 1 退出（汇报里贴出原文）
- [ ] 改动全部在允许清单内

## STOP 条件

- 坏构建在走到 `state.Open` 之前就以别的方式失败，而且失败信息同样没说明原因。例如在其他包里先打开了
  SQLite。这时要汇报具体位置，由 owner 决定检查点要不要上移到进程启动处。
- `sqlite_compileoption_used` 在带 fts5 的构建里返回 0。说明对驱动的假设不成立。

## 维护说明

- 如果以后改用系统 SQLite（go-sqlite3 的 `libsqlite3` tag），探测依然正确，因为它问的是实际链接的引擎。
- 安装命令的唯一出处是 `errSQLiteBuild` 和 README 的 go install 小节（计划 012）。改模块路径时两处都要改。
