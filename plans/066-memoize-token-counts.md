# Plan 066：同一段文字只用分词器数一次——估算器按内容记忆计数；prefill 基线只在需要时才算

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> 先跑 `scripts/fetch-libtokenizers.sh`。
>
> **前置**：Plan 065 已在工作区落地（`pkg/llm/tiktoken_convert.go` 存在）。
> **Drift check（先跑）**：`test -f pkg/llm/tiktoken_convert.go`；`grep -n 'func (e \*tokenizerEstimator) Count' pkg/llm/tokenizers.go` 有 1 行；
> `grep -n 'func (g \*compactRunGuard) prefill(key string, currentTokens int) int' pkg/run/llm_middleware.go` 有 1 行。不符 → STOP。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW（只加缓存与惰性求值，计数结果不变）
- **Depends on**: plans/065-tokenizers-as-tiktoken.md
- **Category**: perf
- **Planned at**: commit `fa4af8a` + 工作区（含 065），2026-10-16

## Why this matters

轮中压缩检查点在**每一次** LLM 调用上对整段历史计数：`recoverableLLM.Execute` 先为 prefill 基线数一遍（即使 guard 已有基线、这个数用不上），
`compactIfNeeded → shouldCompactMessages` 再数一遍，发送前算 `sent` 又数一遍，外加两遍工具 schema。
实测（真实分词器，约 2.5 MB / 65 万 token 的历史）：一遍 0.27–0.38 s，即每次调用约 1 s 纯 CPU；一个 200 步的回合多出几分钟。
历史里绝大多数消息在相邻调用之间不变，按内容记忆计数后，后续调用只需要哈希（约每 MB 1 ms）而不是重新分词。

## Current state（全部已核实）

- 估算器 `pkg/llm/tokenizers.go`：

```go
// tokenizerEstimator wraps a loaded HuggingFace/tiktoken tokenizer.
type tokenizerEstimator struct {
	id string
	tk *hftok.Tokenizer
}

func (e *tokenizerEstimator) Count(text string) int { return e.tk.Count(text) }
func (e *tokenizerEstimator) Name() string          { return e.id }
```

  `heuristicEstimator`（rune/4）不需要记忆。所有调用方经 `EstimateTextWith` / `EstimateMessageWith` / `EstimateMessagesWith` 到达 `Count`，在 `Count` 里加记忆即可惠及全部路径（默认估算与按模型估算）。
  已加载的 `*tokenizerEstimator` 在进程内复用、永不关闭（`resolveTokenizer` 的 slot）。
- 轮中链路 `pkg/run/llm_middleware.go`：

```go
func (g *compactRunGuard) prefill(key string, currentTokens int) int {
	...
	if !entry.prefillSet {
		entry.prefillTokens = max(0, currentTokens)
		entry.prefillSet = true
		g.entries[key] = entry
	}
	return entry.prefillTokens
}
```

  调用点 `applyRunScopedCompact`：`prefillTokens := r.runGuard.prefill(key, llm.EstimateMessagesWith(est, messages))`——参数总被求值。
- 测试样例：`pkg/llm/tokenizers_test.go`（打开 `tokenizerFilesAllowed` 并 `ResetTokenizersForTest` 的写法）、`pkg/run/compaction_test.go`。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 取库 | `scripts/fetch-libtokenizers.sh` | 成功 |
| 测试 | `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/llm/... ./pkg/run/ -count=1` | 全部 `ok` |
| 基准 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/llm/ -run '^$' -bench BenchmarkEstimateMessagesRepeated -benchtime 3x` | 见 Step 3 |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm/... ./pkg/run/`；`gofmt -l pkg` | 无输出 |
| 包图 | `scripts/package-graph.sh` | 重新生成并保留改动 |

## Scope

**In scope**：`pkg/llm/tokenizers.go`、`pkg/llm/tokenizers_test.go`（或新建 `pkg/llm/count_cache_test.go`）、`pkg/run/llm_middleware.go`、`pkg/run/compaction_test.go`（如需）、`pkg/architecture/testdata/graph.json`。

**Out of scope**：估算口径（哪里用哪个估算器——Plan 069）；`heuristicEstimator`；`hftok` 包。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：估算器按内容记忆

在 `tokenizerEstimator` 上加一个有界的计数缓存：
- 只对 `len(text) >= 256` 字节的文本使用（短文本直接数更便宜）。
- 键：`struct{ sum uint64; n int }`，`sum` 为 `hash/fnv` 的 64 位 FNV-1a，`n` 为字节长度。
- 并发安全（`sync.Mutex` 保护的 `map`）；容量上限常量 `countCacheCap = 1 << 16`，超过时整表清空重建（注释写明为何不做 LRU：历史在相邻调用间整体重复，清空后一次调用即可回填）。
- 英文注释说明：键用 64 位哈希 + 长度，碰撞概率可忽略；值只是 token 数。

**Verify**：`go build ./pkg/llm/` 通过。

### Step 2：prefill 惰性

`compactRunGuard.prefill` 的第二个参数改为 `current func() int`，仅在 `!entry.prefillSet` 时调用；`applyRunScopedCompact` 传 `func() int { return llm.EstimateMessagesWith(est, messages) }`。其余调用点（若有）同样改。

**Verify**：`grep -n 'runGuard.prefill(' pkg/run/*.go` 的每个调用都传函数；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` 全绿。

### Step 3：基准

在 `pkg/llm` 加 `BenchmarkEstimateMessagesRepeated`：打开 `tokenizerFilesAllowed`，`AssetsDirEnv` 指向 `RepoAssetsDirForTest()`，`Configure(CacheDir: b.TempDir(), DisableDownload 或 Hubs: []string{}——以 Plan 067 落地前的 API 为准)`，取 `EstimatorFor("zhipuai","glm-5.3")`；
构造约 2 MB 的历史（2000 条消息，每条为一段中英混合代码文本重复若干次），先计时首遍，再计时第二遍，用 `b.ReportMetric` 报告两者毫秒数。

**Verify**：第二遍耗时 ≤ 首遍的 10%（报告里贴输出）。

## Test plan

1. `TestCountCacheReturnsSameCount`：同一估算器对同一长文本数两次结果相等，且等于直接 `tk.Count` 的结果；对 255 字节与 256 字节文本都正确。
2. `TestCountCacheBounded`：向缓存塞入超过 `countCacheCap` 个不同长文本后，内部 map 大小 ≤ `countCacheCap`（同包测试可直接读字段）。
3. `TestCountCacheConcurrent`：8 个 goroutine 并发对一组文本计数，结果与串行一致（`-race`）。
4. `pkg/run`：`TestPrefillEstimateIsLazy`——用一个计数被调用次数的函数调用 `prefill` 两次（同一 key），函数只被调用一次。

## Done criteria

- [ ] Commands 表全部通过
- [ ] Test plan 4 个测试存在且 PASS（`-race`）
- [ ] 基准第二遍 ≤ 首遍 10%，输出贴在报告里
- [ ] `git status --short` 中本计划的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符（065 未落地）。
2. 加缓存后任何现有计数断言变化。
3. 基准第二遍仍 > 首遍 30%。

## Maintenance notes

- 记忆在估算器实例上，跟着进程走；换估算器（模型不同）各有各的缓存。
- 若以后 `Count` 不再是纯函数（例如带参数），必须把参数并入键。
