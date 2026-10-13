# Plan 041：主流模型分词器随包分发、缓存优先、缺失才联网下载；按模型选分词器；删除 `hftokenizer.go` 与 tiktoken-go

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **前置**：Plan 039（`assets/`、`llm.AssetsDir`、`llm.InstallAssets`、`install-assets.sh`）与 Plan 040（`pkg/llm/hftok`、`scripts/fetch-libtokenizers.sh`）**已在工作区落地**。
> 先跑 `scripts/fetch-libtokenizers.sh`。
>
> **Drift check（先跑）**：
> `grep -n 'func AssetsDir\|func InstallAssets\|assetEntries' pkg/llm/assets.go` 三者都在；`ls pkg/llm/hftok/hftok.go scripts/fetch-libtokenizers.sh` 都在；
> `git diff --stat 005c1a8 -- pkg/llm/tokestimate.go pkg/llm/hftokenizer.go pkg/config/tools.go pkg/config/load.go forebrain.yaml` 无输出。不符 → STOP。
> `pkg/run/runner.go` 主树有 owner 未提交改动（权限钩子 cwd、allowed roots、`RunContent` 的 ProjectRoot），本计划只改 `:1190-1202` 的 `llm.Configure` 块，其余不碰。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: MED（估算口径变化影响压缩触发点；新增大体积资源文件；运行时可能联网下载）
- **Depends on**: plans/039-models-json-ships-beside-binary.md、plans/040-link-hf-tokenizers-library.md
- **Category**: tech-debt / feature
- **Planned at**: commit `005c1a8` + 主树未提交改动，2026-10-11

## Why this matters

本地 token 估算决定自动压缩何时触发。现状：不管什么模型都用 tiktoken cl100k（日志 `tokestimate: model has no tiktoken map; using cl100k model=glm-5.3`），且进程全局只有一个估算器；
自研的 `pkg/llm/hftokenizer.go` 只支持 BPE/ByteLevel 子集、只能全局配一个路径——owner 要求删掉。tiktoken-go 还会在运行时从 openaipublic 下载 BPE 文件，内网/离线环境直接退化成 rune/4。
owner 2026-10-11 的决定（全部要落实）：
1. deepseek / glm / gpt / claude / kimi / qwen / minimax / grok / gemini / xiaomi 等主流模型的分词器文件**随 forebrain 一起发布**：提交进 git 仓库、**不嵌入二进制**、随 npm 包（以及发行归档、镜像）装在二进制旁，首次运行放进缓存目录——与 Plan 039 的 models.json 同一方案。
2. **先读缓存目录，缓存里没有才联网下载**；下载按 HuggingFace → ModelScope 的顺序（端点可配，国内可用 ModelScope 或镜像）。
3. 企业内网/离线环境：可在公网机器下载好后把缓存目录拷进内网使用——所以缓存目录必须是固定、人可读的布局（解压后的原文件）。
4. Claude、Grok 没有当代公开分词器，Gemini 没有公开分词器：**全部用近似文件**（Claude 用 Claude 2 时代的旧分词器，Grok 用 Grok-2 分词器的 HF 转换版，Gemini 用 Gemma 4 分词器）。

## Current state（全部已核实）

- 估算入口 `pkg/llm/tokestimate.go`：进程全局 `st`（tiktoken 状态）、`Configure(TokenEstimateOptions{Encoding, Model, TokenizerPath})`、`EstimateText`（先 `strings.TrimSpace`，空串 0；HF → tiktoken → `estimateHeuristicRuneDiv4`）、
  `EstimateMessages` / `EstimateMessage`（`:204-229`：文本 + 每个 tool call 的 `Name+Arguments` + `Compaction.EncryptedContent` + `AgentMessage` 内容 + 图片按 `ImageTokens=2000` + 每条 4 的框架开销）。**`EstimateMessage` 的组成规则不变**，只换底层计数器。
- 未配置时（所有不构造 Runner 的单测）`EstimateText` 走 rune/4：`st.tk == nil` → `activeTiktoken` 返回 heuristic。很多测试的期望值按 rune/4 写死（例如 `pkg/state/action_service_test.go` 的 "49 body tokens"），**这一点必须保持**。
- 唯一配置点 `pkg/run/runner.go:1190-1202`：

```go
	te := llm.TokenEstimateOptions{}
	if r.AppCfg != nil {
		cfg := r.AppCfg.Agents.Defaults.TokenEstimate
		te = llm.TokenEstimateOptions{Encoding: cfg.Encoding, Model: cfg.Model, TokenizerPath: cfg.TokenizerPath}
	}
	if strings.TrimSpace(te.Encoding) == "" && strings.TrimSpace(te.Model) == "" {
		if len(cfg.LLMChain) > 0 && cfg.LLMChain[0] != nil {
			te.Model = strings.TrimSpace(cfg.LLMChain[0].Model)
		}
	}
	if err := llm.Configure(te); err != nil {
		return err
	}
```

  `r.Home` 是 `$FOREBRAIN_HOME`（单测里可能为空）。
- 配置结构 `pkg/config/tools.go:35-48` `TokenEstimateConfig{Encoding, Model, TokenizerPath}`（YAML `agents.defaults.token_estimate`），由 `pkg/config/agents.go:43` 挂载；YAML 解码**不严格**（未知键被忽略）。示例配置 `forebrain.yaml:385-395`。`pkg/config/secrets.go:99` 把 `token_estimate` 列为非密钥键（保留）。
- 要删除：`pkg/llm/hftokenizer.go`、`pkg/llm/hftokenizer_test.go`（`Label()` 只在这个测试文件里定义）、`pkg/llm/request_context_test.go` 里依赖 tiktoken 的 `TestConfigure...`（`:140-200` 一带两个测试）及 `waitForTokestimateSettled` / `resetTokestimateStateForTest`（`:201-227`）。`github.com/pkoukk/tiktoken-go` 与 `github.com/dlclark/regexp2` 只被这些文件使用。
  **保留** `pkg/llm/testdata/deepseek_v3_tokenizer.json`（`pkg/llm/hftok` 的测试在用）。
- Plan 040 在 `cmd/forebrain/main.go` 加了空白导入 `pkg/llm/hftok`——本计划让 `pkg/llm` 正式导入它后删掉那行。
- 分层：`pkg/llm` 第 0 层，不能导入 `pkg/home`、`pkg/config`；缓存目录与配置由 `pkg/run`（Runner）传入。`llm` 扇出预算 8（`pkg/llm/hftok` 属于 `llm` 自身，不计扇出）。
- **内置分词器表**（2026-10-11 逐个下载核实；sha256 针对**解压后的原文件**；HF 与 ModelScope 同一文件 sha256 一致，"—" 表示该 hub 没有）：

| id | 文件 | sha256 | 字节 | HuggingFace 仓库 | ModelScope 仓库 | 许可 | 覆盖（模型名前缀，忽略大小写） |
|---|---|---|---|---|---|---|---|
| `deepseek-v4` | `tokenizer.json` | `c90dfa01249db1be4245780a052ede752e1361c612ac6d08e2bdada7d599476b` | 6367257 | `deepseek-ai/DeepSeek-V4.1-Flash` | `deepseek-ai/DeepSeek-V4.1-Flash` | MIT | `deepseek`（V3.2/V4/V4.1 的 128000 个词表 id 完全相同） |
| `glm-5` | `tokenizer.json` | `19e773648cb4e65de8660ea6365e10acca112d42a854923df93db4a6f333a82d` | 20217442 | `zai-org/GLM-5.3` | `ZhipuAI/GLM-5.3` | GLM-5.3 License（MIT 式 + MaaS 条款） | `glm`（GLM-4.7 词表是其子集） |
| `gpt-o200k` | `tokenizer.json` | `0614fe83cadab421296e664e1f48f4261fa8fef6e03e63bb75c20f38e37d07d3` | 27868174 | `openai/gpt-oss-120b` | `openai-mirror/gpt-oss-120b` | Apache-2.0 | `gpt`、`chatgpt`、`o1`、`o3`、`o4`、`codex`；也是默认分词器 |
| `kimi-k3` | `tiktoken.model` | `b6c497a7469b33ced9c38afb1ad6e47f03f5e5dc05f15930799210ec050c5103` | 2795286 | `moonshotai/Kimi-K3` | `moonshotai/Kimi-K3` | Kimi K3 License（MIT 式 + MaaS 条款） | `kimi`、`moonshot` |
| `kimi-k3` | `tokenizer_config.json` | `5d0803c94db9cd78763499e0956c95fd5a225c14a727e5a6cf5db3f96f010a6e` | 3478 | 同上 | 同上 | 同上 | （tiktoken 加载需要的配置） |
| `qwen-3.8` | `tokenizer.json` | `0997f410c57a1f4e53b09e4be8f4a172d90edd9564368fb0847030937229b9f3` | 12809320 | `Qwen/Qwen3.8-27B` | `Qwen/Qwen3.8-27B` | Apache-2.0 | `qwen`、`qwq`、`deepseek-r1-distill-qwen`（后者须排在 `deepseek` 规则之前） |
| `minimax-m2` | `tokenizer.json` | `757622126525aeeb131756849d93298070ff3f0319c455ec8c5bb0f6b1cebbe8` | 9730160 | `MiniMaxAI/MiniMax-M2` | `MiniMax/MiniMax-M2` | MIT（附加条款：MAU>1 亿或 ARR>3000 万美元的产品需标注 "MiniMax M2"） | `minimax`（与 M2.7 文件相同；与 M3 的 200000 个词表 id 相同。**不用 M3 的文件**：M3 是 MiniMax Community License，商用受限） |
| `mimo-v2.5` | `tokenizer.json` | `633518aad78f9f61bae2ae420d621215754a4424c918b052cd8c22a3b59e99d2` | 7033572 | `XiaomiMiMo/MiMo-V2.5` | `XiaomiMiMo/MiMo-V2.5` | MIT | `mimo` |
| `gemma-4` | `tokenizer.json` | `cc8d3a0ce36466ccc1278bf987df5f71db1719b9ca6b4118264f45cb627bfe0f` | 32169626 | `google/gemma-4-31b-it` | `google/gemma-4-31b-it` | Apache-2.0 | `gemini`、`gemma`（Gemini 近似） |
| `claude-legacy` | `tokenizer.json` | `c241737df24b4e7f7c9af4fdcee29a0ca903dcb288a8b753bc346a3092911767` | 1774213 | `Xenova/claude-tokenizer` | `Xenova/claude-tokenizer` | MIT | `claude`（Claude 2 时代旧分词器，近似） |
| `grok-2` | `tokenizer.json` | `63e538a53de71acf0585603a7f1f62cc66c5e6fa5763d753d10ad071f5c5d4fe` | 17963143 | `alvarobartt/grok-2-tokenizer` | — | xAI Community License（Grok 2） | `grok`（官方 `xai-org/grok-2` 的 `tokenizer.tok.json` 的 HF 转换版；已核对 130944 个常规 token 的 id 与官方完全一致） |

  下载 URL：HF `<endpoint>/<repo>/resolve/main/<file>`（默认 endpoint `https://huggingface.co`）；ModelScope `<endpoint>/models/<repo>/resolve/master/<file>`（默认 `https://modelscope.cn`）。
  按提供方兜底（模型名没命中前缀时）：`deepseek`→deepseek-v4、`zhipuai`/`zai`→glm-5、`openai`→gpt-o200k、`moonshotai`→kimi-k3、`alibaba`/`dashscope`→qwen-3.8、`minimax`→minimax-m2、`xiaomi`→mimo-v2.5、`google`→gemma-4、`anthropic`→claude-legacy、`xai`→grok-2。
  Kimi 的切分正则（来自 `moonshotai/Kimi-K3` 的 `tokenization_kimi.py`，与 daulet 自带测试一致），用 `"|"` 连接这 8 段：

```text
[\p{Han}]+
[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}&&[^\p{Han}]]*[\p{Ll}\p{Lm}\p{Lo}\p{M}&&[^\p{Han}]]+(?i:'s|'t|'re|'ve|'m|'ll|'d)?
[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}&&[^\p{Han}]]+[\p{Ll}\p{Lm}\p{Lo}\p{M}&&[^\p{Han}]]*(?i:'s|'t|'re|'ve|'m|'ll|'d)?
\p{N}{1,3}
 ?[^\s\p{L}\p{N}]+[\r\n]*
\s*[\r\n]+
\s+(?!\S)
\s+
```

  （第 5 段开头是一个空格。）全部 11 个文件 gzip 后合计约 28 MB，解压后约 139 MB。本机实测这些文件都能被 `pkg/llm/hftok` 加载计数。
- 词典先例（`pkg/memory/dictionary.go`）：sha256 编译进代码、下载解压到同盘临时目录校验通过才原子改名、`sync.Once` 保证一个进程只下载一次。照这个写。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 取库 | `scripts/fetch-libtokenizers.sh` | 成功 |
| llm 测试 | `CGO_ENABLED=1 go test -race -tags fts5 ./pkg/llm/... -count=1` | 全部 PASS |
| 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 全部 `ok` |
| 无网络 | `HTTPS_PROXY=http://127.0.0.1:9 HTTP_PROXY=http://127.0.0.1:9 CGO_ENABLED=1 go test -tags fts5 ./pkg/... -count=1`（Go 不代理回环地址，httptest 不受影响） | 与联网结果一致；本来就访问外网的既有测试（如 `pkg/tui` 的 `TestCharacterizationNetworkApproval`）除外，报告里列出 |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./...`；`gofmt -l cmd pkg` | 无输出 |
| 依赖 | `GOWORK=off go mod tidy && git diff go.mod` | `pkoukk/tiktoken-go`、`dlclark/regexp2` 被移除 |
| 构建 | `make build` | 成功；`build/bin/tokenizers/<id>/...gz` 齐全 |
| 包图 | `scripts/package-graph.sh` | 重新生成并连同提交 |

## Scope

**In scope**：
- 新建 `assets/tokenizers/`（11 个 `.gz`、各 id 目录下的 `LICENSE`、`assets/tokenizers/README.md`）
- `pkg/llm/assets.go`（`assetEntries` 加 `"tokenizers"`）、新建 `pkg/llm/tokenizers.go` 与 `pkg/llm/tokenizers_test.go`、重写 `pkg/llm/tokestimate.go`
- 删除 `pkg/llm/hftokenizer.go`、`pkg/llm/hftokenizer_test.go`；修 `pkg/llm/request_context_test.go`
- `cmd/forebrain/main.go`（删 040 的空白导入）
- `pkg/config/tools.go`、`pkg/config/load.go`（及其测试）、`forebrain.yaml`（示例配置）
- `pkg/run/runner.go:1190-1202`（只改这一块）
- `Dockerfile`（最终镜像 `COPY` tokenizers 目录）、`npm/scripts/build-platform-packages.sh` 与 `scripts/package-release-archive.sh` 的文件头注释
- `README.md`（新增"本地 token 估算与离线使用"一节）、`NOTICE`
- `go.mod` / `go.sum`、`pkg/architecture/testdata/graph.json`

**Out of scope**：
- 压缩判定改用会话模型的分词器（Plan 042）——本计划只提供 `EstimatorFor` 等 API，并让默认估算器换成分词器。
- 轮中/轮前判定逻辑、观测开销（`pkg/llm/observed.go`）、`pkg/state/usage.go`。
- 估算结果缓存（每次 LLM 调用对整段历史重新计数是既有开销），见 Maintenance notes。
- 提供 `forebrain tokenizers download` 一类命令：缓存目录布局固定，内网直接拷目录即可；需要时另立计划。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：准备分词器资产

在仓库外的临时目录按上表逐个下载（HF 不通就用 ModelScope；Grok 只有 HF），**逐个核对 sha256 与字节数**，不符 → STOP。然后：

```bash
mkdir -p assets/tokenizers/<id>
gzip -9 -n -c <下载的文件> > assets/tokenizers/<id>/<文件名>.gz   # 例如 tokenizer.json.gz、tiktoken.model.gz
```

每个 id 目录放一份 `LICENSE`：取源仓库的 `LICENSE`（HF：`<repo>/resolve/main/LICENSE`）；MiniMax 取 `https://raw.githubusercontent.com/MiniMax-AI/MiniMax-M2/main/LICENSE`；Grok 取 `xai-org/grok-2` 的 `LICENSE` 并在文件头注明"converted to tokenizer.json by alvarobartt/grok-2-tokenizer"；
源仓库没有 `LICENSE` 文件的（例如 Apache-2.0 只在模型卡里声明），写入该许可的标准全文，文件头注明出处与模型卡链接。写 `assets/tokenizers/README.md`：上表（id、文件、sha256、来源仓库、许可、覆盖的模型）、"如何更新"（下载 → 核对 → gzip -9 -n → 改 `pkg/llm/tokenizers.go` 的表 → 跑 `TestBundledTokenizerFilesMatchTable`）、"内网/离线"说明（见 Step 7）。
`.gitattributes` 加 `assets/tokenizers/**/*.gz binary`。

**Verify**：`for f in assets/tokenizers/*/*.gz; do gzip -dc "$f" | shasum -a 256; done` 与上表一致；`du -sh assets/tokenizers` 约 28 MB。

### Step 2：分词器注册表 `pkg/llm/tokenizers.go`

英文注释，风格照 `pkg/memory/dictionary.go`。要点：

1. 数据结构（包级 `var builtinTokenizers = []BuiltinTokenizer{...}`，用**变量**便于测试替换；按上表 11 个文件 10 个 id 填写，前缀规则顺序要保证 `deepseek-r1-distill-qwen` 先于 `deepseek`）：

```go
type TokenizerFile struct{ Name, SHA256 string; Size int64 }
type BuiltinTokenizer struct {
	ID          string
	Files       []TokenizerFile // Files[0] is the model; a tiktoken model's Files[1] is its tokenizer_config.json
	Tiktoken    bool            // load with hftok.FromTiktoken(Files[0], Files[1], Pattern)
	Pattern     string
	HuggingFace string          // repo id, "" when absent
	ModelScope  string
	ModelPrefixes []string
	Providers     []string
}
```

2. 估算器：

```go
type Estimator interface {
	Count(text string) int // tokens in text; text is used as given
	Name() string          // tokenizer id, "rune_div4" for the heuristic
}
```

   heuristic 估算器沿用现有 `estimateHeuristicRuneDiv4`。分词器估算器包一层 `*hftok.Tokenizer`。
3. 选择：`func TokenizerIDFor(provider, model string) string`——先看配置覆盖（模型名前缀，忽略大小写），再看内置前缀，再看提供方，最后默认 id（配置 `DefaultTokenizer`，空则 `gpt-o200k`）。模型名先取最后一个 `/` 之后的部分再小写（`deepseek/deepseek-v4-flash` 与 `deepseek-v4-flash` 同结果）。
4. 文件解析顺序（每个 id 一个 `sync.Mutex` + "已加载/已放弃本地"状态；已加载的实例进程内复用、永不 Close）：
   1. 缓存 `<CacheDir>/<id>/<file>`：存在且字节数与 sha256 都对 → 用。sha 不对 → `slog.Warn` 一次，当作缺失（下一步会覆盖它）。
   2. 随包资源 `<AssetsDir()>/tokenizers/<id>/<file>.gz`：存在 → 解压到 `<CacheDir>/<id>/` 下的临时文件，校验通过再 `os.Rename` 成正式文件（同盘原子），然后用。
   3. 都没有 → 若允许下载，**异步**下载（每个 id 每进程最多一次，`sync.Once` 式）：按 `Hubs` 顺序逐个 hub 尝试，`http.Client{Timeout: 10 * time.Minute}`，`io.LimitReader(Size+1)`，状态 200 且字节数、sha256 都对才原子写入缓存；全部失败 → `slog.Warn` 一次（列出每个 hub 的错误与缓存目录路径，提示可手工放入）。下载完成后下次 `EstimatorFor` 自然加载到。
   4. 本次拿不到 → 返回默认 id 的估算器（同样的解析，但不递归到默认之外）→ 仍拿不到 → heuristic。
   成功加载时 `slog.Info("tokenizer ready", "id", id, "source", "cache|bundle|download")` 一次。
5. 配置覆盖里的 `Path`（本地 tokenizer.json）：直接 `hftok.FromFile(path)`，不复制、不校验 sha；失败 → `slog.Warn` 一次并按"拿不到"处理。
6. **测试二进制保护**：包级 `var tokenizerFilesAllowed = !testing.Testing()`（关时：不读缓存、不读随包资源、不下载，一律 heuristic）与 `var bulkSeedAllowed = !testing.Testing()`（关时：`Configure` 不整批解压）。
   `testing.Testing()` 是 Go 1.21 起专为此设计的函数；文件注释写清 "a test binary keeps the rune/4 heuristic and never touches tokenizer files or the network unless the test opts in; llm's own tests flip these"。
   这样其他包里构造 Runner 的测试（会调 `Configure`）行为与今天一致、完全确定。llm 自己的测试打开 `tokenizerFilesAllowed`；不测下载的用例传 `Hubs: []string{}`（**非 nil 的空切片表示不下载**；nil 才表示默认两个 hub）。
7. 对外 API：

```go
func EstimatorFor(provider, model string) Estimator
func DefaultEstimator() Estimator
func EstimateTextWith(e Estimator, s string) int        // TrimSpace 后计数，与 EstimateText 相同规则
func EstimateMessageWith(e Estimator, msg Message) int  // 与 EstimateMessage 组成规则相同
func EstimateMessagesWith(e Estimator, msgs []Message) int
func SetEstimatorForTest(provider, model string, e Estimator) (restore func())
func ResetTokenizersForTest()
```

   `EstimateText` / `EstimateMessage` / `EstimateMessages` 保留原签名，改为委托 `DefaultEstimator()`。**未调用过 `Configure` 或 `CacheDir` 为空时，所有估算器都是 heuristic，且绝不读文件、绝不联网**——这保证现有单测的 rune/4 期望值不变。
   `SetEstimatorForTest` 的覆盖（按与 `TokenizerIDFor` 相同的规范化后的 provider/model 精确匹配）**优先于一切**，包括 `tokenizerFilesAllowed=false`——Plan 042 的测试靠它注入假估算器。

**Verify**：`go build -tags fts5 ./...` 通过。

### Step 3：重写 `Configure`，删旧实现

1. `pkg/llm/tokestimate.go`：删掉 tiktoken 状态、`configuredEncoding`、`warmEncoding`、`knownEncoding`、HF 分支；`TokenEstimateOptions` 改为：

```go
type TokenEstimateOptions struct {
	CacheDir            string   // $FOREBRAIN_HOME/cache/tokenizers; "" disables model tokenizers
	Hubs                []string // "huggingface", "modelscope"; nil means both, in that order
	HuggingFaceEndpoint string   // "" → $HF_ENDPOINT → https://huggingface.co
	ModelScopeEndpoint  string   // "" → https://modelscope.cn
	DefaultTokenizer    string   // "" → gpt-o200k
	Overrides           []TokenizerOverride
}
type TokenizerOverride struct{ Model, Tokenizer, Path string }
```

   `Configure` 记录选项（未知 hub / 未知 id → `slog.Warn` 并忽略该项），若 `bulkSeedAllowed` 且 `CacheDir` 非空，起一个 goroutine 对每个内置 id 只做"缓存校验字节数、缺失则从随包资源解压"（不联网、不加载）。
2. 删除 `pkg/llm/hftokenizer.go`、`pkg/llm/hftokenizer_test.go`；`pkg/llm/request_context_test.go` 删掉两个 tiktoken 测试与两个帮手，去掉 tiktoken 导入。
3. `pkg/llm/assets.go`：`assetEntries` 加 `"tokenizers"`。
4. `cmd/forebrain/main.go`：删掉 Plan 040 加的空白导入。
5. `GOWORK=off go mod tidy`。

**Verify**：`grep -rn 'tiktoken-go\|regexp2\|HFTokenizer\|TokenizerPath' --include='*.go' pkg cmd` 无输出（`pkg/config` 的废弃字段见 Step 4，名字不同）；`go build -tags fts5 ./...` 通过。

### Step 4：配置

1. `pkg/config/tools.go`：`TokenEstimateConfig` 改为

```go
type TokenEstimateConfig struct {
	Hubs                []string            `yaml:"hubs,omitempty" json:"hubs,omitempty"`
	HuggingFaceEndpoint string              `yaml:"huggingface_endpoint,omitempty" json:"huggingface_endpoint,omitempty"`
	ModelScopeEndpoint  string              `yaml:"modelscope_endpoint,omitempty" json:"modelscope_endpoint,omitempty"`
	DefaultTokenizer    string              `yaml:"default_tokenizer,omitempty" json:"default_tokenizer,omitempty"`
	Tokenizers          []TokenizerOverride `yaml:"tokenizers,omitempty" json:"tokenizers,omitempty"`
	// Deprecated: ignored since the estimator picks a tokenizer per model.
	Encoding      string `yaml:"encoding,omitempty" json:"encoding,omitempty"`
	Model         string `yaml:"model,omitempty" json:"model,omitempty"`
	TokenizerPath string `yaml:"tokenizer_path,omitempty" json:"tokenizer_path,omitempty"`
}
type TokenizerOverride struct {
	Model     string `yaml:"model" json:"model"`                             // model name prefix, case-insensitive
	Tokenizer string `yaml:"tokenizer,omitempty" json:"tokenizer,omitempty"` // a built-in tokenizer id
	Path      string `yaml:"path,omitempty" json:"path,omitempty"`           // a local tokenizer.json
}
```

   结构体文档注释重写（说明缓存目录、下载顺序、离线用法、内置 id 列表见 `assets/tokenizers/README.md`）。
2. `pkg/config/load.go` 校验（照同文件 `ModelAutoCompactTokenLimitScope` 的写法）：`hubs` 每项只能是 `huggingface`/`modelscope`；每个 `tokenizers` 项 `model` 非空，且 `tokenizer` 与 `path` 恰好一个非空。
3. `pkg/run/runner.go:1190-1202` 换成：`CacheDir` 只在 `strings.TrimSpace(r.Home) != ""` 时设为 `filepath.Join(r.Home, "cache", "tokenizers")`；其余字段从配置映射；三个废弃字段任一非空 → `slog.Warn("agents.defaults.token_estimate.<key> is deprecated and ignored; see token_estimate.tokenizers")`。删掉"取 LLMChain[0].Model"的逻辑。
4. `forebrain.yaml` 的 `token_estimate` 示例段按新字段重写（每个字段一行 Default/Example 注释，风格与该文件一致）。

**Verify**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/config/... ./pkg/run/... -count=1` 全绿。

### Step 5：打包入口

`install-assets.sh` 已因 `assetEntries` 自动带上 `tokenizers/`。补：
- `Dockerfile` 最终阶段 `COPY --from=gobuild /out/tokenizers /app/tokenizers`。
- `npm/scripts/build-platform-packages.sh`、`scripts/package-release-archive.sh` 文件头布局注释加 `tokenizers/`。

**Verify**：`make build && ls build/bin/tokenizers/*/` 列出 10 个 id 目录；`FOREBRAIN_SKIP_UI_BUILD=1 npm/scripts/build-platform-packages.sh` 后对应 `vendor/*/bin/tokenizers/` 齐全。

### Step 6：真机冒烟（不需要模型）

```bash
T=$(mktemp -d)
FOREBRAIN_HOME=$T ./build/bin/forebrain <039 Step 4 用过的那个会走启动路径的子命令>
sleep 5; ls $T/cache/tokenizers/*/
```

预期：10 个 id 目录都已解压出原文件，`shasum -a 256` 与表一致。

下载路径要有真实估算调用才会触发，用 glm-5.3 跑一次真实单轮（`./build/bin/forebrain --help` 找非交互的单轮/one-shot 用法；`pkg/process/one_shot.go` 是其实现）：
新的隔离 home（**不要**用 `$TMPDIR`、不要碰真实 `~/.forebrain`），精简 `forebrain.yaml` 只配 `zhipuai` / `glm-5.3`（`base_url: https://open.bigmodel.cn/api/coding/paas/v4`，`api_key: ${ZHIPUAI_API_KEY}`），并设
`agents.defaults.token_estimate: {hubs: [modelscope], default_tokenizer: kimi-k3}`（kimi 文件最小；本计划里压缩判定还走默认估算器，按模型选是 042，所以用 `default_tokenizer` 触发）；把 `build/bin/tokenizers` 临时改名使随包文件不可用；
在启动脚本里注入密钥 `eval "$(grep '^ZHIPUAI_API_KEY=' ~/.forebrain/.env)"; export ZHIPUAI_API_KEY`（密钥不得出现在命令行或拷贝的文件里），问一句 "hello"。
预期：回合正常结束；日志出现 `tokenizer ready id=kimi-k3 source=download`（下载是异步的，若本轮结束时还没完成，再问一句）；`<home>/cache/tokenizers/kimi-k3/` 两个文件的 sha256 与表一致。完成后把 `build/bin/tokenizers` 改回原名。

**Verify**：两段预期都成立，在报告里写明命令与输出摘要。

### Step 7：文档与 NOTICE

1. `README.md` 新增一节 "Token estimation and offline use"：每个模型用哪个分词器（指向 `assets/tokenizers/README.md`）、缓存目录 `$FOREBRAIN_HOME/cache/tokenizers/<id>/<file>`、查找顺序（缓存 → 随包文件 → HuggingFace → ModelScope）、配置项、**内网/离线：在能联网的机器上运行一次 forebrain（或直接从发行包解压 `tokenizers/*.gz` 并 `gzip -d`），把整个 `cache/tokenizers` 目录拷到内网机器同一位置即可**。
2. `NOTICE`：追加"assets/tokenizers 下的分词器文件来自第三方，各自许可见对应目录的 LICENSE 与 assets/tokenizers/README.md"。

**Verify**：`grep -n 'cache/tokenizers' README.md assets/tokenizers/README.md` 有输出。

## Test plan

`pkg/llm/tokenizers_test.go`（需要时用 `t.Setenv(AssetsDirEnv, ...)`；除测试 3、9 外都先把 `tokenizerFilesAllowed` 设为 true，用 `t.Cleanup` 还原并调 `ResetTokenizersForTest`；不测下载的用例传 `Hubs: []string{}`）：

1. `TestBundledTokenizerFilesMatchTable`：对 `builtinTokenizers` 每个文件，`<repo>/assets/tokenizers/<id>/<file>.gz` 存在，解压后字节数与 sha256 等于表中值；id 唯一。
2. `TestTokenizerIDFor`：表驱动——`deepseek-v4-flash`、`deepseek/deepseek-v4-flash`→deepseek-v4；`deepseek-r1-distill-qwen-32b`→qwen-3.8；`glm-5.3`、`GLM-4.7`→glm-5；`gpt-5.6-sol`、`o3-mini`→gpt-o200k；`kimi-k3`→kimi-k3；`qwen3.8-max`、`qwq-32b`→qwen-3.8；`MiniMax-M3`→minimax-m2；`mimo-v2.5-pro`→mimo-v2.5；`gemini-3.7-flash`、`gemma-4-31b-it`→gemma-4；`claude-opus-5-5`→claude-legacy；`grok-4.6`→grok-2；`("anthropic","opus")`→claude-legacy；`("unknown","foo")`→gpt-o200k；配置覆盖 `{Model:"foo", Tokenizer:"qwen-3.8"}` 优先。
3. `TestUnconfiguredEstimatorIsHeuristic`：`ResetTokenizersForTest()` 后 `EstimateText(strings.Repeat("abcd",4))==4`，`EstimatorFor("deepseek","deepseek-v4-flash").Name()=="rune_div4"`。
4. `TestEstimatorSeedsCacheFromBundle`：把 `builtinTokenizers` 换成一个测试 id（文件用 `testdata/deepseek_v3_tokenizer.json`，sha256 现场计算），bundle 目录放它的 `.gz`；`Configure(CacheDir: tmp)` 后 `EstimatorFor` 返回该 id，缓存里出现解压后的原文件。
5. `TestTamperedCacheFileIsReplaced`：缓存里预置同名但内容不同的文件 → 被 bundle 覆盖，估算器可用。
6. `TestMissingFileDownloadsFromHub`：无 bundle；`httptest` 服务器在 `/<repo>/resolve/main/tokenizer.json` 返回文件；`Hubs: [huggingface]`、endpoint 指向服务器；轮询等到 `EstimatorFor(...).Name()` 变成测试 id（≤5 s）。
7. `TestDownloadFallsBackToModelScope`：HF 路径 404，ModelScope `/models/<repo>/resolve/master/tokenizer.json` 正常 → 成功。
8. `TestDownloadRejectsWrongChecksum`：服务器返回错内容 → 缓存无文件，估算器回落默认/heuristic。
9. `TestTestBinaryNeverTouchesTokenizerFiles`：默认开关下（不改 `tokenizerFilesAllowed`），`Configure` 给出缓存目录、bundle 目录有文件、Hubs 指向计数服务器 → `EstimatorFor` 仍是 `rune_div4`，缓存目录为空，服务器请求计数为 0。
10. `TestOverridePathLoadsLocalTokenizer`：覆盖 `{Model:"my-", Path: testdata 文件}` → `EstimatorFor("x","my-model").Count("hello")>0`。
11. `TestKimiTiktokenFromBundle`：用真实仓库资产（`AssetsDirEnv` 指向 `RepoAssetsDirForTest()`，`bulkSeedAllowed` 保持 false），`EstimatorFor("moonshotai","kimi-k3").Count("你好，世界")>0`。
12. `pkg/config` 加用例：非法 hub、override 同时给 tokenizer 与 path、override 缺 model → 校验报错。

## Done criteria

- [ ] Commands 表全部通过（含"无网络"一行）
- [ ] Test plan 12 组测试存在且 PASS（`-race`）
- [ ] `test ! -e pkg/llm/hftokenizer.go && test ! -e pkg/llm/hftokenizer_test.go`
- [ ] `grep -n 'tiktoken-go\|dlclark/regexp2' go.mod` 无输出；`grep -rn 'pkg/llm/hftok' cmd/forebrain` 无输出
- [ ] Step 6 冒烟两点成立
- [ ] `git status --short` 中本计划的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符，或 039/040 未落地。
2. 任一分词器文件的 sha256 / 字节数与表不符（上游改了文件）——不要自行换版本。
3. 某源仓库的许可与表中不同，或许可明确禁止再分发。
4. 改完后有现有测试（`pkg/llm` 以外）的期望值变化——按设计测试二进制里一律是 rune/4，出现变化说明 `tokenizerFilesAllowed` 的保护有漏洞；找出原因修保护，**不要改期望值**；修不好 → STOP。
5. 单个分词器加载后进程 RSS 增长超过 1 GB，或加载耗时超过 5 s。
6. 架构测试因 `pkg/llm` 导入上层包失败。

## Maintenance notes

- 加新家族 / 换新版本：按 `assets/tokenizers/README.md` 的"如何更新"做；**新 id 用新目录名**（例如 `deepseek-v5`），旧 id 不改内容——缓存里同名文件靠 sha256 校验，改内容会让所有用户重新解压/下载。
- 近似说明：Claude（旧分词器）、Grok（Grok-2）、Gemini（Gemma 4）、较老的 Qwen2.5/Qwen3（词表 151k，用 Qwen3.8 的 248k 词表近似）都只是近似；压缩判定的主要准确性来自服务端回报的原生用量锚点（Plan 038）。
- 每次 LLM 调用会对整段历史重新计数（既有开销；HF 分词器约 2 MB/s，与 tiktoken-go 同量级）。若长会话出现可感知延迟，可另立计划按消息内容哈希缓存计数。
- Plan 042 会把压缩判定与观测改成按会话模型选估算器；在那之前，压缩判定用的是默认分词器（gpt-o200k）。
- 评审重点：表中 sha256 与资产文件一致；测试二进制不联网；缓存目录布局与 README 描述一致；废弃配置键只告警不报错。
