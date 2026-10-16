# Plan 067：分词器配置与解析语义收口——`hubs` 为空即默认、用 `download: false` 关闭下载、覆盖路径必须绝对、日志如实、失败可在重载后重试

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> 先跑 `scripts/fetch-libtokenizers.sh`。
>
> **前置**：Plan 065、066 已在工作区落地。
> **Drift check（先跑）**：`test -f pkg/llm/tiktoken_convert.go`；`grep -n 'countCacheCap' pkg/llm/tokenizers.go` 有输出；
> `grep -n 'a non-nil empty slice disables downloading' pkg/llm/tokestimate.go` 有 1 行；`grep -n 'hubs: \[\]' forebrain.yaml` 有 1 行。不符 → STOP。

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: plans/065-tokenizers-as-tiktoken.md、plans/066-memoize-token-counts.md（同改 `pkg/llm/tokenizers.go`，须串行）
- **Category**: bug / config
- **Planned at**: commit `fa4af8a` + 工作区（含 065、066），2026-10-16

## Why this matters

1. **`hubs: []` 的语义自相矛盾（已实测）**：仓库示例配置 `forebrain.yaml:388-390` 写着 `# Default: empty means huggingface then modelscope` 与 `hubs: []`，
   `pkg/config/tools.go` 的字段注释也说 "Empty means both"；但 `pkg/llm/tokestimate.go:15-17` 规定"非 nil 的空切片表示禁止下载"，而 `yaml.v3` 把 `hubs: []` 解码成非 nil 空切片——照示例配置的用户**下载被悄悄关掉**。
   反过来，配置经 `config.Save`（`yaml.Marshal` + `omitempty`）保存一次后 `hubs: []` 被丢掉，下次加载又变回"两个 hub"。同一份意图在保存前后语义相反。
2. **关闭下载时日志说"下载失败"**：`downloadAndLoad` 在没有任何 hub 时也打 `tokenizer download failed; using the rune/4 estimate`——既不是失败，也不一定回落到 rune/4（会先回落到默认分词器）。
3. **废弃配置键的告警每次 Runner 加载都打一遍**（`pkg/run/runner.go` 的 deprecated 循环），gateway 多个 runner、配置热重载都会刷屏。
4. **覆盖项的 `path` 可为相对路径**：gateway 一个进程服务多个项目、各自的工作目录不同，相对路径解析到哪里取决于进程目录，语义不明。
5. **本地解析失败会被记到进程结束**：`resolveTokenizer` 把"本地没有"记成 `resolved=true, est=nil`，`downloadOnce` 也已用掉；之后拷进缓存目录的文件（内网场景）或热重载重新开启的下载都要重启才生效。

内网/离线用户需要一个明确、保存前后不变的开关：`token_estimate.download: false`。

## Current state（全部已核实）

- `pkg/llm/tokestimate.go:9-28`：

```go
type TokenEstimateOptions struct {
	CacheDir string // $FOREBRAIN_HOME/cache/tokenizers; "" disables model tokenizers
	// Hubs is the download order. A nil slice means both HuggingFace and
	// ModelScope, in that order; a non-nil empty slice disables downloading.
	Hubs                []string
	HuggingFaceEndpoint string // "" → $HF_ENDPOINT → https://huggingface.co
	ModelScopeEndpoint  string // "" → https://modelscope.cn
	DefaultTokenizer    string // "" → gpt-o200k
	Overrides           []TokenizerOverride
}
```

  `Configure`（`:37` 起）：`hubs := c.Hubs; if hubs == nil { hubs = []string{hubHuggingFace, hubModelScope} }`，再逐个校验；之后记录设置并（`bulkSeedAllowed` 时）起 `go seedCacheFromBundle(settings)`。
- `pkg/llm/tokenizers.go`：`resolveTokenizer`（本地失败时 `slot.resolved = true; slot.est = nil` 后 `slot.downloadOnce.Do(func(){ go downloadAndLoad(settings, bt, slot) })`）；
  `downloadAndLoad` 失败时 `slog.Warn("tokenizer download failed; using the rune/4 estimate", ...)`；`downloadTokenizerFiles` 遍历 `settings.hubs`，没有 hub 时返回 `no hub served <id>`。
  `tokenizerSlot{mu, resolved, est, downloadOnce}`；`slotRegistry.slots map[string]*tokenizerSlot`；`resolveLocalPath` 的键为 `"local\x00"+path`。
- `pkg/config/tools.go:51-71` `TokenEstimateConfig`（`Hubs` 注释 "Empty means both, in that order."）、`TokenizerOverride{Model, Tokenizer, Path}`；校验 `pkg/config/load.go:549-570` `validateTokenEstimate`（hub 允许 `""`）。
- `pkg/run/runner.go:1199-1229`：把配置映射成 `llm.TokenEstimateOptions`（`te.Hubs = cfg.Hubs`），三个废弃键非空时每次都 `slog.Warn`。
- 示例配置 `forebrain.yaml:385-407`（`token_estimate` 段）。
- 文档：`README.md` 的 "Token estimation and offline use" 一节（`:192-213`，**主树 README 有别人未提交的改动，但不在这一节**）；`assets/tokenizers/README.md` 的离线说明。
- 测试里用 `Hubs: []string{}` 关闭下载的有 4 处：`grep -rn 'Hubs: \[\]string{}' --include='*_test.go' pkg`。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 取库 | `scripts/fetch-libtokenizers.sh` | 成功 |
| 测试 | `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/llm/... ./pkg/config/ -count=1`；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm/... ./pkg/config/ ./pkg/run/`；`gofmt -l pkg` | 无输出 |
| 包图 | `scripts/package-graph.sh` | 重新生成并保留改动 |

## Scope

**In scope**：`pkg/llm/tokestimate.go`、`pkg/llm/tokenizers.go`、`pkg/llm/*_test.go`、`pkg/config/tools.go`、`pkg/config/load.go`、`pkg/config/*_test.go`、`pkg/run/runner.go`（**只改** `llm.TokenEstimateOptions` 映射那一块）、`forebrain.yaml`（只改 `token_estimate` 段）、
`README.md`（**只改** "Token estimation and offline use" 一节）、`assets/tokenizers/README.md`、`pkg/architecture/testdata/graph.json`。

**Out of scope**：估算口径（Plan 069）；`pkg/llm/hftok/*`；README 的其他部分。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：`llm` 选项语义

1. `TokenEstimateOptions`：`Hubs` 注释改为"nil 或空都表示 HuggingFace 再 ModelScope"；新增 `DisableDownload bool // never download a missing tokenizer (offline/intranet)`。`Configure` 里 `len(c.Hubs) == 0` 即用默认两个 hub；`DisableDownload` 记入设置。
2. `resolveTokenizer`：`DisableDownload` 为真时不启动下载，改为 `slog.Info` 一次（每个 id 每进程一次）："tokenizer <id> is neither in the cache nor beside the binary and downloads are disabled; estimating with the default tokenizer or rune/4"，附 `cache_dir`。
3. `downloadAndLoad` 失败告警文案改为 `tokenizer download failed; estimating with the default tokenizer or rune/4`。
4. 失败可重试：`tokenizerSlot` 加 `downloading bool`（启动下载 goroutine 前置 true、结束时置 false，均在 `slot.mu` 下）。`Configure` 记录新设置后，遍历 `slotRegistry`，删除 `resolved && est == nil && !downloading` 的 slot（包括 `local\x00` 前缀的覆盖路径 slot），注释写明：配置重载（例如开启下载、修正路径）或运维把文件拷进缓存后，下一次需要时会重新扫描/下载。
5. 把 4 处测试里的 `Hubs: []string{}` 改为 `DisableDownload: true`。

**Verify**：`grep -rn 'Hubs: \[\]string{}' pkg` 无输出；`CGO_ENABLED=1 go test -race -tags fts5 ./pkg/llm/... -count=1` 全绿。

### Step 2：配置

1. `pkg/config/tools.go`：`Hubs` 注释改为 "Empty means both, in that order — use Download: false to never download."；新增
   `Download *bool \`yaml:"download,omitempty" json:"download,omitempty"\`` 注释 "nil or true downloads a missing tokenizer; false never does (offline/intranet installs)."；`TokenizerOverride.Path` 注释加 "absolute"。结构体文档注释的离线段落提到 `download: false`。
2. `validateTokenEstimate`：hub 不再接受空字符串；`path` 非空时必须 `filepath.IsAbs`，否则报错 `agents.defaults.token_estimate.tokenizers[%d] (%s): path must be absolute`。
3. `pkg/run/runner.go` 映射：`te.Hubs = cfg.Hubs`（空切片现在即默认）；`te.DisableDownload = cfg.Download != nil && !*cfg.Download`。废弃键告警改为每个键每进程一次（包级 `sync.Map` 或 `map`+`sync.Mutex` 记录已告警的键）。
4. `forebrain.yaml` 的 `token_estimate` 段：`hubs` 注释保持 "empty means huggingface then modelscope"；在其后加

```yaml
      # Default: true.
      # Example: false never downloads a missing tokenizer (offline or intranet installs).
      download: true
```

   覆盖示例里的 `path` 改为绝对路径示例（`/opt/tokenizers/my-model/tokenizer.json`）。

**Verify**：`CGO_ENABLED=1 go test -race -tags fts5 ./pkg/config/ -count=1` 全绿；`CGO_ENABLED=1 go test -tags fts5 ./pkg/run/ -count=1` 全绿。

### Step 3：文档

- `README.md` 的 "Token estimation and offline use" 一节：配置说明加 `download`（"set `download: false` to never reach the network"）；离线段落末尾加一句"and set `download: false` there"；把 "The cache holds the decompressed original files" 改为 "The cache holds the decompressed files exactly as they ship"（065 之后部分 id 是转换后的 tiktoken 文件）。
- `assets/tokenizers/README.md` 离线说明同样加 `download: false`。

**Verify**：`grep -n 'download: false' README.md assets/tokenizers/README.md forebrain.yaml` 三个文件都有输出；`git diff --stat -- README.md` 只有这一节的行。

## Test plan

1. `pkg/llm`：`TestEmptyHubsMeansDefaultHubs`——`Configure(Hubs: []string{})` 后设置里的 hubs 为 `[huggingface modelscope]`。
2. `pkg/llm`：`TestDisableDownloadNeverRequests`——打开 `tokenizerFilesAllowed`，缓存与随包皆空，endpoint 指向计数的 `httptest` 服务器，`DisableDownload: true` → `EstimatorFor` 返回回落估算器，等待 1 s 后服务器计数为 0。
3. `pkg/llm`：`TestReconfigureRetriesFailedResolution`——第一次 `Configure`（随包为空、`DisableDownload: true`）后 `EstimatorFor` 回落；把测试条目的随包文件放好后再 `Configure` 一次，`EstimatorFor` 返回该 id。
4. `pkg/config`：`hubs: [""]`、`path: relative/tokenizer.json` 报错；`download: false` 解析为非 nil 的 false；`hubs: []` 加载后经 `Save` 再加载，`TokenEstimateConfig` 语义不变（下载开启、两个 hub）。
5. `pkg/run`：runner 映射单测（若已有 runner 配置映射测试则扩展，否则新增）——`download: false` → `DisableDownload == true`；`hubs: []` → `DisableDownload == false`。

## Done criteria

- [ ] Commands 表全部通过
- [ ] Test plan 5 组测试存在且 PASS
- [ ] `grep -n 'non-nil empty slice' pkg/llm/tokestimate.go` 无输出
- [ ] `git status --short` 中本计划的改动只涉及 Scope 列出的文件；`README.md` 的 diff 只在 "Token estimation and offline use" 一节

## STOP conditions

1. Drift check 不符（065/066 未落地）。
2. 找到依赖"空 hubs = 禁止下载"语义的生产代码（不只是测试）。
3. 改 README 那一节时与主树的未提交改动冲突（在 worktree 里看不到主树改动；若评审方合入时冲突会另行处理——执行者只需确保 diff 只在该节）。

## Maintenance notes

- 关闭下载只有 `download: false` 一种写法；`hubs` 只管顺序。
- 重载会清掉"失败"的 slot 但保留已加载的分词器；若以后支持热替换已加载的分词器，要处理估算器切换对观测开销的影响（见 Plan 069）。
