# 220 — `PublishInbound` 的在途入站处理要有可等待的收口（Server 持 in-flight 计数 + quiesce 口子）

- **基线**：`9ea9f4a0`（dev 顶端；217 已并入）。
- **状态**：DONE（2026-11-04 执行；已 rebase 后 ff-only 合入 dev `7d505aa3`，worktree 与分支已清理）
- **优先级 / 工作量 / 风险**：P1 / M / MED（动产品生命周期；修法是标准的 WaitGroup + join，无超时/取消新语义）
- **依赖**：无（218 发现转出；owner 裁定「本期必须修复」）。与 219 同一 worktree 执行。
- **执行隔离**：worktree + `exec/219-220` 分支 + 增量提交；合并在 owner 批准后 ff-only。

> **Drift check（先跑）**：`git diff --stat 9ea9f4a0 -- pkg/gateway/server.go pkg/gateway/channels_test.go` 应无输出（在干净检出上）；`grep -c 'func (b channelBus) PublishInbound' pkg/gateway/server.go` 应为 **1**。任一不符就 STOP。

## 0. 为什么做（均已实测核实，2026-11-04）

`channelBus.PublishInbound`（`pkg/gateway/server.go:613`）把整条入站管线——slash 解析、turn 提交、hook transcript 写、出站回投、会话目录创建——放进一个**裸 `go func()`**，fire-and-forget，谁也无法等待它结束。`PublishInbound` 返回 nil 就等于服务器**承诺**处理这条消息，但没有任何收口保证承诺兑现完成。

实测（评审在 exec/218 worktree 亲跑，PG 腿 `-count=6` **6/6 确定性**，非偶发）：

```
ERROR channel hook transcript session=wc-…-u1 channel=wecom err="sql: database is closed"
--- FAIL: TestChannelMessageOnACodingProjectOpensNoTask
    testing.go:1464: TempDir RemoveAll cleanup: unlinkat …: directory not empty
```

每次都是同一对：goroutine 在测试结束后仍在跑——向已关闭的库写 hook transcript（错误日志 + transcript 半写），并向 `t.TempDir` 里的会话目录写文件，与 `RemoveAll` 竞速。harness（`batchChannelGateway`，channels_test.go:1281）用 `t.Cleanup(db.Close())` 拆库，正对着在途 goroutine。SQLite 负载下同型偶发（`TestBatchChannelMessageStartsATask`）；矩阵（217/218 的质量门）在三条腿上都撞过。

**产品面影响**：网关当前没有服务端停机序列（`/admin/shutdown` 只有客户端探针，服务端无 handler），所以生产暴露暂为潜伏——但任何未来的优雅关停都缺这个口子，且它让三库矩阵无法保持绿。这是「承诺了处理却不可等待」的生命周期缺口，不是测试写法问题。

## 1. 范围

**只动**：`pkg/gateway/server.go`（`Server` 结构 + `PublishInbound` + 新 quiesce 方法）；`pkg/gateway/channels_test.go` / `api_extra_test.go` 的 harness（cleanup 里调 quiesce）；可加一个确定性回归测试。

**不动**：`pkg/channel`（registry 不感知 gateway Server，分层不动）；`PublishInbound` 的业务逻辑与错误处理路径；其它 `go func()` 调用点。

## 2. 修法

1. `Server` 增加非导出字段 `inboundWG sync.WaitGroup`（放结构体何处、注释风格照邻近字段）。
2. `PublishInbound` 的 goroutine 外壳改为：

```go
if b.s != nil {
	b.s.inboundWG.Add(1)
}
go func() {
	defer func() {
		if b.s != nil {
			b.s.inboundWG.Done()
		}
	}()
	… 原函数体不动 …
}()
```

（`channelBus` 是值类型，计数必须落在 `*Server` 上；`b.s` 判空照 goroutine 体内既有写法。）

3. 新方法（命名照 Server 邻近方法风格，建议 `QuiesceInbound`）：doc comment 说明「等待所有已接受的入站消息处理完；关 DB/拆目录前必须先调它」。
4. harness：`batchChannelGateway`（channels_test.go:1281 一带）与 `rulesServer`（api_extra_test.go:3510 一带）在**注册 `db.Close` cleanup 之后**再注册 quiesce cleanup（t.Cleanup LIFO ⇒ quiesce 先于 db.Close 执行）。凡经这两个 harness 且触发 PublishInbound 的测试随之全愈。
5. 回归测试（确定性，不靠竞态）：发布一条已知会在会话目录/transcript 落盘的入站消息 → 立即 `QuiesceInbound()` → 断言落盘已存在（证明 quiesce 真的等到了 goroutine 尾部，而非空转）。

## 3. 验证（每条带 `FOREBRAIN_HOME="$(mktemp -d)"` + 仅 `PKG_CONFIG_PATH`；禁全局 CGO_LDFLAGS）

1. 原竞态翻绿：PG 腿 `go test -tags fts5 ./pkg/gateway -run 'TestChannelMessageOnACodingProjectOpensNoTask' -count=6` → 6/6 ok（改前 6/6 挂，评审实测）。
2. SQLite 同型偶发收口：`go test -tags fts5 ./pkg/gateway -run 'TestBatchChannelMessageStartsATask' -count=10` → ok。
3. 回归测试（§2.5）三库各跑 → ok。
4. `pkg/gateway` 全包三库（'' / mysql / postgres URL）→ ok（与 219 合并后这就是 218 §4.5 的矩阵腿）。
5. SQLite 全量 `./... -count=1 -p 2` 全绿；`go vet -tags fts5 ./pkg/...` 无输出。

## 4. 完成判据（机器可查）

§3 全绿；`git diff` 仅 §1 列出的文件；包图不变（无新 import 边）——`go test ./pkg/architecture/ -count=1` ok。

## 5. STOP 条件

- Drift check 不符。
- 发现 quiesce 需要超时/取消/传播 ctx 语义才能不挂死（说明在途管线有不可结束的等待——那是另一个产品缺口，停下上报，别硬加超时）。
- `TestSQLiteStatementsUnchanged` 或包图守卫变红。

## 6. 维护注意

- 未来做服务端优雅关停（`/admin/shutdown` 真 handler）时，`QuiesceInbound` 就是现成的第一步；在那之前它只被测试 harness 调用。
- 新写 channel 测试：凡是发布入站消息的 harness，cleanup 注册顺序都是 quiesce 在 db.Close 之前（LIFO）。
