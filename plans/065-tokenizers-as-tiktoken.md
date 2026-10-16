# Plan 065：6 个字节级 BPE 分词器改以 tiktoken 格式随包分发与加载（常驻内存降到约 1/5，计数快约 3 倍）

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> 先跑 `scripts/fetch-libtokenizers.sh`（把 tokenizers 静态库放进 `third_party/libtokenizers/`，`go test` 链接要用）。
>
> **Drift check（先跑）**：`git diff --stat fa4af8a -- pkg/llm/tokenizers.go pkg/llm/tokenizers_test.go assets/tokenizers pkg/llm/hftok/hftok.go`
> 应无输出。有输出 → 对照 "Current state" 摘录核对，不符 → STOP。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（换掉 6 个随包资产的格式；计数与原 tokenizer.json 有极小差异，见下）
- **Depends on**: 无（066、067、069 依赖本计划）
- **Category**: perf / resource
- **Planned at**: commit `fa4af8a` + 主树未提交改动（不涉及本计划文件），2026-10-16

## Why this matters

本地 token 估算按模型加载 HuggingFace 分词器（daulet/tokenizers 的 cgo 绑定）。实测：每个 tokenizer.json 加载后进程常驻内存增加 92–423 MB，
而默认分词器 gpt-o200k（gpt-oss 的 28 MB tokenizer.json）要 +340 MB，并且几乎每个进程都会加载它（`llm.EstimateText` 等默认估算到处在用）。
owner 本机用 glm-5.3 的 TUI 进程常驻 590 MB。同一套词表改用 tiktoken 格式（字节序列 → rank）加载，只需 +39–83 MB，计数还快约 3 倍。
owner 2026-10-16 拍板：**把能无损/近无损转换的 6 个改成 tiktoken 格式**，deepseek-v4、claude-legacy、gemma-4 结构上不能转，保持 tokenizer.json。

逐个独立进程实测（加载后常驻内存增量）与等价性（仓库 1268 个 `.go/.md/.ts/.vue` 文件、约 630 万–670 万 token，逐文件比对原 tokenizer.json 与转换后的 tiktoken 计数）：

| id | 原 tokenizer.json | tiktoken | 不一致文件数 | 累计差（token） |
|---|---|---|---|---|
| gpt-o200k | +340 MB | +57 MB | 0 | 0 |
| grok-2 | +239 MB | +39 MB | 0 | 0 |
| glm-5 | +251 MB | +46 MB | 4 | 37 |
| qwen-3.8 | +158 MB | +83 MB | 3 | 17 |
| minimax-m2 | +112 MB | +55 MB | 3 | 3 |
| mimo-v2.5 | +92 MB | +51 MB | 3 | 17 |

（对估算而言 < 0.001% 的差异可以忽略；原因是 HF 的 merges 优先级与 rank 顺序在极少数片段上不同。）

## Current state（全部已核实）

- 内置表 `pkg/llm/tokenizers.go:77-162` `builtinTokenizers`；结构体 `:53-71`：

```go
type TokenizerFile struct {
	Name   string
	SHA256 string
	Size   int64
}

type BuiltinTokenizer struct {
	ID    string
	Files []TokenizerFile // Files[0] is the model; a tiktoken model's Files[1] is its tokenizer_config.json
	// Tiktoken loads the pair with hftok.FromTiktoken(Files[0], Files[1], Pattern);
	// otherwise Files[0] is loaded with hftok.FromFile.
	Tiktoken      bool
	Pattern       string
	HuggingFace   string // repo id on HuggingFace, "" when absent
	ModelScope    string // repo id on ModelScope, "" when absent
	ModelPrefixes []string
	Providers     []string
}
```

  kimi-k3 已是 tiktoken 形式（`Files: tiktoken.model + tokenizer_config.json`, `Tiktoken: true`, `Pattern: kimiPattern`），**照它的写法**。
- 解析链：`resolveTokenizer`（`:512`）→ `loadBundledOrCache`（`:566`，缓存按 `Size+SHA256` 校验，缺了从 `<AssetsDir>/tokenizers/<id>/<file>.gz` 解压）→ `loadFromPaths`（`:664`，`Tiktoken` 走 `hftok.FromTiktoken`）。
  缓存与随包都不可用时 `downloadAndLoad`（`:683`）→ `downloadTokenizerFiles`（`:702`）按 hub 逐个下载 `bt.Files` 中的每个文件（URL：HF `<endpoint>/<repo>/resolve/main/<file>`，ModelScope `<endpoint>/models/<repo>/resolve/master/<file>`），校验 `Size+SHA256` 后原子写入缓存。
  启动时 `seedCacheFromBundle`（`:793`）按 `Size` 检查、缺则从随包解压。
- 当前 6 个 id 的随包文件是 `assets/tokenizers/<id>/tokenizer.json.gz`（+ `LICENSE`）。它们解压后的原文件（转换的**源**）：

| id | 源 sha256 | 源字节数 | HuggingFace | ModelScope |
|---|---|---|---|---|
| gpt-o200k | `0614fe83cadab421296e664e1f48f4261fa8fef6e03e63bb75c20f38e37d07d3` | 27868174 | `openai/gpt-oss-120b` | `openai-mirror/gpt-oss-120b` |
| glm-5 | `19e773648cb4e65de8660ea6365e10acca112d42a854923df93db4a6f333a82d` | 20217442 | `zai-org/GLM-5.3` | `ZhipuAI/GLM-5.3` |
| grok-2 | `63e538a53de71acf0585603a7f1f62cc66c5e6fa5763d753d10ad071f5c5d4fe` | 17963143 | `alvarobartt/grok-2-tokenizer` | — |
| qwen-3.8 | `0997f410c57a1f4e53b09e4be8f4a172d90edd9564368fb0847030937229b9f3` | 12809320 | `Qwen/Qwen3.8-27B` | `Qwen/Qwen3.8-27B` |
| minimax-m2 | `757622126525aeeb131756849d93298070ff3f0319c455ec8c5bb0f6b1cebbe8` | 9730160 | `MiniMaxAI/MiniMax-M2` | `MiniMax/MiniMax-M2` |
| mimo-v2.5 | `633518aad78f9f61bae2ae420d621215754a4424c918b052cd8c22a3b59e99d2` | 7033572 | `XiaomiMiMo/MiMo-V2.5` | `XiaomiMiMo/MiMo-V2.5` |

- 这 6 个源文件的结构（已核实）：`model.type == "BPE"`，词表 id 连续 `0..n-1`，`pre_tokenizer` 为 `Sequence[Split(Regex), ByteLevel(use_regex=false)]`；Split 的 `behavior` 为 `Isolated`/`invert=false`（minimax-m2 为 `Removed`/`invert=true`，语义同为"只保留正则匹配到的片段"，与 tiktoken 一致）。
  不能转的三个：deepseek-v4（三段 Split + normalizer 序列）、claude-legacy（NFKC + 无 Split 的 ByteLevel）、gemma-4（`byte_fallback=true` 的 SentencePiece 式 BPE）——**不动它们**。
- 转换规则（已用 Python 原型验证，产物与下表 sha256 一致）：
  1. 字节级字母表：GPT-2 的 `bytes_to_unicode`——字节 `!`..`~`、`¡`..`¬`、`®`..`ÿ` 映射到同码点字符，其余 68 个字节按升序映射到 `U+0100` 起的连续码点。反向映射把词表里每个 token 字符串还原为原始字节；遇到字母表外的字符 → 报错。
  2. `tiktoken.model`：按 id 升序，每行 `base64.StdEncoding(原始字节) + " " + 十进制 id + "\n"`。
  3. `tokenizer_config.json`：daulet 的 Rust 侧要求字段（`crates/tokenizers/src/lib.rs` 的 `TokenizerConfig`）：`added_tokens_decoder`（map，键为 id 的十进制字符串，值 `{content,lstrip,normalized,rstrip,single_word,special}`）、`bos_token`、`eos_token`、`model_max_length`（数字）、`tokenizer_class`（字符串）。
     取值：`added_tokens` 全部写入（`lstrip/normalized/rstrip/single_word` 为 false，`special` 为 true）；`bos_token` 与 `eos_token` 都取 id 最小的 added token 的 content；`model_max_length` 为 `1000000`；`tokenizer_class` 为 `"TiktokenTokenizer"`。缺任何一个字段 `FromTiktoken` 会报 `missing field`。
  4. 切分正则：取源 Split 的 `pattern.Regex` 原文。
- 转换产物 `tiktoken.model` 的 sha256 / 字节数（确定性，Go 实现必须逐字节一致）：

| id | tiktoken.model sha256 | 字节数 |
|---|---|---|
| gpt-o200k | `446a9538cb6c348e3516120d7c08b09f57c36495e2acfffe59a5bf8b0cfb1a2d` | 3613922 |
| glm-5 | `d2a312b6d9fa24fc27bdea3387e65477c427214e2fb2372e5f3ae980ffaa3e1d` | 2668918 |
| grok-2 | `fa70b860aeca864eab97639d1f9912025a3868cf848185032d4b1c83deae5b92` | 2208402 |
| qwen-3.8 | `8dde380a6405e935f5de16a99eb61c824f3f814dd1ed298784c72babb7a03cdd` | 4579414 |
| minimax-m2 | `9b423908eab5445f88a72b26a283d848da80884fde9e0b8e5e7a4fe495313f1e` | 3557010 |
| mimo-v2.5 | `b2b1b8dfb5cc5f024bafc373121c6aba3f66f9a5a0269e243470a1de16a33186` | 2561218 |

  `tokenizer_config.json` 的 sha256 由 Go 实现生成后记录（JSON 由 `encoding/json.Marshal` 生成，map 键自动排序，确定性）。
- 6 个切分正则（Go 原始字符串，**与源文件逐字一致**；Step 2 会用转换器输出再核对一遍）：

```go
// gpt-o200k 与 minimax-m2 相同
`[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]*[\p{Ll}\p{Lm}\p{Lo}\p{M}]+(?i:'s|'t|'re|'ve|'m|'ll|'d)?|[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]+[\p{Ll}\p{Lm}\p{Lo}\p{M}]*(?i:'s|'t|'re|'ve|'m|'ll|'d)?|\p{N}{1,3}| ?[^\s\p{L}\p{N}]+[\r\n/]*|\s*[\r\n]+|\s+(?!\S)|\s+`
// glm-5
`(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}{1,3}| ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+`
// grok-2 与 mimo-v2.5 相同
`(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?\p{L}+|\p{N}| ?[^\s\p{L}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+`
// qwen-3.8
`(?i:'s|'t|'re|'ve|'m|'ll|'d)|[^\r\n\p{L}\p{N}]?[\p{L}\p{M}]+|\p{N}| ?[^\s\p{L}\p{M}\p{N}]+[\r\n]*|\s*[\r\n]+|\s+(?!\S)|\s+`
```

- 分层：`pkg/llm` 第 0 层，只能用标准库与 `pkg/llm/hftok`。仓库的开发工具写成 `cmd/<name>/main.go`（例如 `cmd/installassets/main.go`），照它。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 取库 | `scripts/fetch-libtokenizers.sh` | 成功 |
| llm 测试 | `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/llm/... -count=1` | 全部 `ok` |
| 相关包 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/state/ ./pkg/assembly/ ./pkg/run/ -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/llm/... ./cmd/...`；`gofmt -l pkg cmd` | 无输出 |
| 包图 | `scripts/package-graph.sh` | 重新生成 `pkg/architecture/testdata/graph.json` 并保留改动 |

## Scope

**In scope**：`pkg/llm/tokenizers.go`、新建 `pkg/llm/tiktoken_convert.go` 与其测试、`pkg/llm/tokenizers_test.go`、新建 `cmd/tokenizer2tiktoken/main.go`、
`assets/tokenizers/{gpt-o200k,glm-5,grok-2,qwen-3.8,minimax-m2,mimo-v2.5}/`（换文件、更新 LICENSE 头注）、`assets/tokenizers/README.md`、`pkg/llm/testdata/`（新增小夹具）、`pkg/architecture/testdata/graph.json`。

**Out of scope**：deepseek-v4、claude-legacy、gemma-4、kimi-k3 的资产；`pkg/llm/hftok/*`（`link.go` 在主树有别人的未提交改动，不碰）；`README.md`（主树有别人的未提交改动，不碰）；估算记忆化（Plan 066）；配置语义（Plan 067）。

## Git workflow

不建分支、不 commit、不 push、不 stash。资产文件的删除/新增直接在工作区操作（`rm` / 新文件），不要 `git rm`/`git add`。

## Steps

### Step 1：转换函数

新建 `pkg/llm/tiktoken_convert.go`（英文注释，风格照 `tokenizers.go`）：

```go
// ConvertTokenizerJSONToTiktoken converts a HuggingFace byte-level BPE tokenizer.json
// into the tiktoken rank file and tokenizer_config.json hftok.FromTiktoken loads,
// plus the split pattern. It accepts only the shape that converts faithfully:
// a BPE model with contiguous ids, and a pre-tokenizer that is one Split regex
// followed by ByteLevel(use_regex=false).
func ConvertTokenizerJSONToTiktoken(data []byte) (model, config []byte, pattern string, err error)
```

按 Current state 的"转换规则"实现；不满足形状（非 BPE、id 不连续、pre_tokenizer 不是恰好 `[Split, ByteLevel]`、Split 不是 `Isolated/invert=false` 或 `Removed/invert=true`、`ByteLevel.use_regex` 不为 false、字母表外字符）→ 返回描述性错误。
config 用固定字段顺序的结构体 + `json.Marshal`（不缩进、不加换行）。

**Verify**：`go build ./pkg/llm/` 通过。

### Step 2：开发工具与资产

1. 新建 `cmd/tokenizer2tiktoken/main.go`：用法 `go run ./cmd/tokenizer2tiktoken <tokenizer.json> <out-dir>`，调用转换函数，写出 `<out-dir>/tiktoken.model` 与 `<out-dir>/tokenizer_config.json`，并打印 pattern 与两个文件的 sha256、字节数。文件头注释说明用途与"更新分词器时用它从源 tokenizer.json 重新生成"。
2. 对 6 个 id 逐个：`gzip -dc assets/tokenizers/<id>/tokenizer.json.gz > /tmp/src.json`，核对源 sha256 与 Current state 表一致（不符 → STOP），运行工具，核对 `tiktoken.model` 的 sha256/字节数与表一致（不符 → STOP）、打印的 pattern 与 Current state 的正则逐字一致（不符 → STOP），
   然后 `gzip -9 -n -c` 两个产物到 `assets/tokenizers/<id>/tiktoken.model.gz` 与 `tokenizer_config.json.gz`，删除该目录的 `tokenizer.json.gz`。记下每个 `tokenizer_config.json` 的 sha256 与字节数。
3. 每个 id 的 `LICENSE` 文件头加一行：`The files here are converted to the tiktoken format by cmd/tokenizer2tiktoken from <repo>'s tokenizer.json (sha256 <源 sha>); the license below applies to them.`（不要改许可正文）。

**Verify**：`ls assets/tokenizers/{gpt-o200k,glm-5,grok-2,qwen-3.8,minimax-m2,mimo-v2.5}/` 每个目录恰为 `LICENSE tiktoken.model.gz tokenizer_config.json.gz`；`du -sh assets/tokenizers` 明显小于改前（改前约 28 MB）。

### Step 3：内置表与下载回退

1. `TokenizerFile` 不变；`BuiltinTokenizer` 加字段 `Source *TokenizerFile // the hub file the Files are converted from; nil when Files are downloaded as-is`。
2. 6 个条目改为：`Files = {tiktoken.model, <sha>, <size>}, {tokenizer_config.json, <sha>, <size>}`、`Tiktoken: true`、`Pattern: <对应常量>`、`Source: &TokenizerFile{Name: "tokenizer.json", SHA256: <源 sha>, Size: <源字节数>}`；新增 4 个具名常量（`o200kPattern` 等，gpt/minimax 共用一个、grok/mimo 共用一个），放在 `kimiPattern` 旁。HF/ModelScope 仓库、前缀、提供方不变。
3. `downloadFromHub`：`bt.Source != nil` 时只下载 `Source`（同样的 URL 规则，校验源 `Size+SHA256`），调用转换函数，逐个校验产物与 `bt.Files` 的 `Size+SHA256`，全部通过才原子写入缓存（复用 `writeFileAtomic`）；`Source == nil` 维持原逻辑。
4. 旧缓存清理：`seedCacheFromBundle` 与 `loadBundledOrCache` 在处理 `Source != nil` 的 id 时，若 `<cacheDir>/<id>/tokenizer.json` 存在则删除（旧版本留下的 20–28 MB 文件；删除失败只 `slog.Debug`）。
5. 文件头注释与 `BuiltinTokenizer` 注释补一句：转换过的 id 随包与缓存里存的是 tiktoken 文件，hub 上只有源 tokenizer.json，下载回退在本地转换。

**Verify**：`CGO_ENABLED=1 go test -race -tags fts5 ./pkg/llm/... -count=1` 中 `TestBundledTokenizerFilesMatchTable` 通过（它校验随包 `.gz` 与表一致）。

### Step 4：`assets/tokenizers/README.md`

更新表格：6 个 id 的文件列改为 `tiktoken.model` + `tokenizer_config.json`，并注明源 tokenizer.json 的 sha256；"如何更新"一节改为：下载源 → 核对源 sha → `go run ./cmd/tokenizer2tiktoken` → gzip → 改表（`Files` 与 `Source`）→ 跑测试；
新增一段"为什么是 tiktoken 格式"：引用 Why this matters 的内存表与等价性数据，说明 deepseek-v4、claude-legacy、gemma-4 为何不转。

**Verify**：`grep -n 'tokenizer2tiktoken' assets/tokenizers/README.md` 有输出。

### Step 5：等价性与内存验收（不入库的临时程序）

在 `/tmp` 下写一个临时 Go 程序（`go run` 时用 `GOFLAGS=` 指到本模块，例如把文件放到 `$(mktemp -d)/main.go` 后在仓库根执行 `go run /tmp/xxx/main.go`），对每个转换过的 id：
- 用 `hftok.FromFile`（源 tokenizer.json，`git show fa4af8a:assets/tokenizers/<id>/tokenizer.json.gz | gzip -dc` 取出）与 `hftok.FromTiktoken`（新文件 + 常量 pattern）分别计数，遍历仓库 `pkg docs/plan plans frontend/src` 下所有 `.go .md .ts .vue` 文件（跳过 >2 MB），统计不一致文件数与累计差。
- 在**单独进程**里分别只加载一种形式，比较加载前后 `ps -o rss= -p <pid>` 的增量。

预期：不一致文件数与累计差不超过 Why this matters 表中的数（gpt-o200k、grok-2 必须为 0）；tiktoken 形式的 RSS 增量：gpt-o200k ≤ 100 MB、glm-5 ≤ 100 MB。把输出贴进报告。

## Test plan

`pkg/llm/tiktoken_convert_test.go`（照 `pkg/llm/tokenizers_test.go` 的写法）：
1. `TestConvertTokenizerJSONToTiktokenSmall`：`pkg/llm/testdata/` 新建一个**手写的小** tokenizer.json 夹具（BPE，约 260 个 token：256 个单字节 + 几个合并；`pre_tokenizer` 为 `[Split(Regex), ByteLevel(use_regex=false)]`；2 个 added token）。断言：model 行数 = 词表大小、第 0 行与第 n 行内容正确、config 含 5 个必需字段、pattern 等于夹具里的正则；两次调用输出逐字节相同。
2. 同一夹具用 `hftok.FromFile` 与 `hftok.FromTiktoken(转换产物)` 对几段中英文文本计数相等（在 `tokenizerFilesAllowed` 无关的层面直接用 hftok）。
3. `TestConvertRejectsUnsupportedShapes`：分别把夹具改成 `byte_fallback`/多段 Split/`use_regex=true`/id 不连续，均返回错误；`testdata/deepseek_v3_tokenizer.json`（三段 Split）返回错误。
4. `pkg/llm/tokenizers_test.go` 新增 `TestConvertedDownloadFallback`：把 `builtinTokenizers` 换成一个测试条目（`Source` 指向夹具的 sha/size、`Files` 为其转换产物的 sha/size、`Tiktoken: true`），无随包、`httptest` 在 `/<repo>/resolve/main/tokenizer.json` 提供夹具；打开 `tokenizerFilesAllowed` 后等待估算器名变为该 id（≤5 s），缓存里出现 `tiktoken.model` 与 `tokenizer_config.json` 且没有 `tokenizer.json`。再加一个用例：服务器返回内容被篡改的源 → 缓存无文件、估算器回落。
5. `TestStaleTokenizerJSONRemovedFromCache`：对一个 `Source != nil` 的测试条目，缓存目录预置 `tokenizer.json`，`seedCacheFromBundle`（`bulkSeedAllowed` 打开）后它被删除。

## Done criteria

- [ ] Commands 表全部通过
- [ ] Test plan 5 组测试存在且 PASS（`-race`）
- [ ] `ls assets/tokenizers/*/tokenizer.json.gz` 只列出 deepseek-v4、claude-legacy、gemma-4 三个
- [ ] Step 5 的等价性与内存输出满足预期并贴在报告里
- [ ] `git status --short` 中本计划的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. 任一源文件 sha256 或转换产物 `tiktoken.model` 的 sha256/字节数与表不符。
3. `hftok.FromTiktoken` 对任一转换产物加载失败，且不是 config 字段缺失这类可按"转换规则"补齐的问题。
4. Step 5 中 gpt-o200k 或 grok-2 出现任何不一致，或其余 4 个的累计差超过表中数值的 2 倍。
5. 需要改 `pkg/llm/hftok/*` 或 `README.md` 才能完成。

## Maintenance notes

- 更新这 6 个家族的分词器：只能经 `cmd/tokenizer2tiktoken` 从源 tokenizer.json 生成，`Files` 与 `Source` 一起改；源换了 sha，下载回退的转换结果也会变，所以两张 sha 必须同时更新。
- 新增家族时先看源 tokenizer.json 的形状：满足"单 Split + ByteLevel(use_regex=false) + 连续 id"就转 tiktoken，否则保持 tokenizer.json（并预期较高的内存）。
- 评审重点：转换函数对不支持形状的拒绝；下载回退在写缓存前同时校验源与产物；旧 `tokenizer.json` 只在 `Source != nil` 的 id 目录里删除。
