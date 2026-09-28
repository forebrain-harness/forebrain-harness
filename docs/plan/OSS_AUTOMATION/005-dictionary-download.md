# Plan 005：go install 装出的二进制在首次需要时下载分词词典（SHA256 校验）

> **执行者须知**：逐步执行，每一步都要跑"验证"命令并确认结果与预期一致后才进入下一步。
> 出现"STOP 条件"中的任何一种情况，立即停止并汇报，不要自行发挥。完成后把
> `docs/plan/OSS_AUTOMATION/README.md` 里本计划的状态改成 DONE。
>
> **提交（里程碑 M4）**：本地步骤全部通过后，按 `README.md` 全局规则 1 提交**一次**：标题见 README 的
> "里程碑与提交标题"表，正文按 `.gitmessage` 模板（Why / What / Prompt cache / Verification，外加
> `Refs: docs/plan/OSS_AUTOMATION/<本文件名>`），用 `git commit -s -F <message-file>`；只 `git add` 本计划"范围"内的文件。
> 不得 `--amend`、`rebase`、`push --force`。开工前确认 `git status --short` 干净、`git log -1` 是上一个里程碑。
>
> **真实仓库实测**：按 README 的关卡推送后，在 `git@github.com:forebrain-harness/forebrain-harness.git` 上完成本文件"真实仓库实测"一节，
> 证据写入 `LIVE_TEST_LOG.md`。实测通过前本计划状态只能是 `AWAITING PUSH`。
>
> **漂移检查（先做）**：本计划编写时仓库还没有提交，M0 之后才有历史。开工前逐条核对"现状"中的摘录与实际文件一致，并用
> `git log --oneline -- <范围内路径>` 查看 M0 之后是否有别的提交改过这些文件；有且与摘录不符，就按 STOP 处理。

## 状态

| 项 | 值 |
| --- | --- |
| 优先级 | P1（没有它，go install 用户的中文/日文记忆检索不可用） |
| 工作量 | M |
| 风险 | MED：新增网络下载与写盘路径 |
| 依赖 | 002（依靠 `home.Version` 在 go install 下给出正确版本） |
| 类别 | direction / dx |
| 编写于 | 2026-09-28，基于未提交的工作区 |

## 为什么要做

记忆检索的分词词典合计约 20 MB，owner 明确规定**不得嵌入二进制**，而是作为独立文件放在二进制旁边的
`dict/` 目录。npm 包和 `make build` 会把词典一起装好。`go install` 只装一个二进制，词典不会跟着来，于是这类
用户第一次遇到中文或日文文本时就会报 "memory search dictionary unavailable"。

owner 的决定是：词典在首次需要时，从该版本对应的 GitHub Release 下载，做 SHA256 校验后放到二进制旁的
`dict/`；npm 包继续内置词典。本计划实现下载端，并提供发版时生成下载包的脚本。上传由计划 008 完成。

校验值必须**编译进二进制**，并由测试证明它们与 go.mod 选定的词典模块逐字节一致。如果只拿同一个 Release
里的 SHA256SUMS 来比对，只能发现传输损坏，防不了篡改。

## 现状

- `pkg/memory/dictionary.go`（全文要点）：
  ```go
  const DictionaryDir = "dict"

  type DictionaryFile struct {
  	Name   string
  	Module string
  	Source string
  }

  const (
  	chineseSimplifiedWords  = "zh/s_1.txt"
  	chineseTraditionalWords = "zh/t_1.txt"
  	chineseStopWords        = "zh/stop_tokens.txt"
  	japaneseDictionary      = "ja/ipa.dict"
  )

  var DictionaryFiles = []DictionaryFile{
  	{Name: chineseSimplifiedWords, Module: "github.com/go-ego/gse", Source: "data/dict/zh/s_1.txt"},
  	{Name: chineseTraditionalWords, Module: "github.com/go-ego/gse", Source: "data/dict/zh/t_1.txt"},
  	{Name: chineseStopWords, Module: "github.com/go-ego/gse", Source: "data/dict/zh/stop_tokens.txt"},
  	{Name: japaneseDictionary, Module: "github.com/ikawaha/kagome-dict/ipa", Source: "ipa.dict"},
  }

  // dictionaryPath is where the named dictionary file is read from: under
  // DictionaryDir, beside the binary that is running.
  func dictionaryPath(name string) (string, error) {
  	executable, err := os.Executable()
  	...
  	executable, err = filepath.EvalSymlinks(executable)
  	...
  	return filepath.Join(filepath.Dir(executable), DictionaryDir, filepath.FromSlash(name)), nil
  }

  func InstallDictionary(binDir string) error // 用 `go mod download -json` 找模块目录再复制
  ```
- 调用方只有两处，都在 `pkg/memory/tokenize.go`：`loadChineseSegmenter`（第 250 行）和
  `loadJapaneseAnalyzer`（第 283 行）。二者都已被 `sync.Once` 包住（`chineseOnce`/`japaneseOnce`），
  失败时经 `dictionaryError(err)` 包成 `memory search dictionary unavailable: %w`。
- 文件大小（实测）：`s_1.txt` 5.1 MB、`t_1.txt` 3.5 MB、`stop_tokens.txt` 9 KB、`ipa.dict` 11.7 MB。
- `pkg/memory` 有 19 个生产文件（上限 20），本计划不新建文件，全部写进 `dictionary.go`。
- `pkg/memory` 目前不导入 `pkg/home`。`pkg/home` 不导入 `pkg/memory`，因此不会形成环。新增这条依赖边
  会改变 `pkg/architecture/testdata/graph.json`，需要重新生成。
- 测试文件命名规则：`X_test.go` 必须与 `X.go` 同目录（`pkg/architecture/cache_test.go:1368`），所以测试
  只能写在 `pkg/memory/dictionary_test.go`。
- 版本号：计划 002 完成后，`home.Version` 对发布构建和 go install 构建都是 `vX.Y.Z`，本地构建是伪版本或 `(devel)`。

## 设计

1. `DictionaryFile` 增加 `SHA256 string`（小写十六进制），四个条目填入实测值。
2. 新增常量：
   ```go
   // dictionaryBundle is the release asset holding every DictionaryFiles entry,
   // laid out as under DictionaryDir.
   const dictionaryBundle = "forebrain-dict.tar.gz"
   const releaseDownloadURL = "https://github.com/forebrain-harness/forebrain-harness/releases/download"
   ```
3. `dictionaryBundleURL(version string) (string, bool)`：`version` 必须形如 `vX.Y.Z`（正则
   `^v[0-9]+\.[0-9]+\.[0-9]+$`；预发布、伪版本、`+dirty`、`(devel)` 都不行），返回
   `releaseDownloadURL + "/" + version + "/" + dictionaryBundle`。
4. `dictionaryPath(name)` 改为：先算出路径，文件存在就返回。不存在时调用 `installMissingDictionaries(dictDir)`
   （进程内用 `sync.Once` 保证只执行一次，结果缓存），成功后返回路径。
   - 版本不是正式发布版本时，返回一句话错误：
     `memory search dictionaries are missing from <dictDir>; install them with scripts/install-dictionary.sh <binDir>`。
   - 否则调用 `downloadDictionaries(ctx, client, url, dictDir, DictionaryFiles)`，client 为
     `&http.Client{Timeout: 5 * time.Minute}`（默认 transport 会读取 `HTTPS_PROXY`）。
5. `downloadDictionaries(ctx, client *http.Client, url, dictDir string, files []DictionaryFile) error`：
   - GET url。状态不是 200 时返回 `fmt.Errorf("download %s: %s", url, resp.Status)`。
   - 响应体套 `io.LimitReader(body, 128<<20)`，再经 gzip → tar 读取。
   - 只接受普通文件条目。条目名经 `path.Clean(strings.TrimPrefix(name, "./"))` 规范化后，必须**恰好等于**
     `files` 里某个 `Name`；否则返回 `unexpected file %q in %s`。这同时挡住了 `../` 路径穿越。
   - 每个文件先写进临时目录：`os.MkdirTemp(filepath.Dir(dictDir), "dict-download-*")`，与 `dictDir` 在同一
     文件系统，保证后续 rename 是原子的。边写边算 SHA256，与声明值不符时返回
     `%s does not match its published checksum`。
   - 读完后，`files` 中每一项都必须出现过，否则返回 `%s is missing from %s`。
   - 全部校验通过后，逐个 `os.MkdirAll` 父目录，并 `os.Rename` 到 `dictDir/<name>`。最后删除临时目录。
     失败路径同样删除临时目录（`defer os.RemoveAll(tmp)`）。
   - 任何校验失败时，`dictDir` 里都不应留下新文件。
   - 网络错误原样包一层返回：`fmt.Errorf("download memory search dictionaries from %s: %w", url, err)`。
     项目规则是给用户看底层原始报错，不要替换成自编解释。
6. 注释（英文）说明：为什么校验值编进二进制；为什么只在正式版本下载（本地构建没有对应 Release，
   应该用 install 脚本）。

## 需要的命令

| 用途 | 命令 | 成功时 |
| --- | --- | --- |
| 算校验值 | `D=$(mktemp -d) && scripts/install-dictionary.sh $D && (cd $D/dict && shasum -a 256 zh/s_1.txt zh/t_1.txt zh/stop_tokens.txt ja/ipa.dict)` | 4 行 hash |
| memory 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/memory -count=1` | ok |
| 架构测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | ok |
| 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 全部 ok |
| 包图 | `scripts/package-graph.sh` | 新增 memory→home 依赖边与 loc 变化 |

## 范围

**允许修改**：`pkg/memory/dictionary.go`、`pkg/memory/dictionary_test.go`（没有就新建）、`pkg/memory/doc.go`
（若它描述了词典来源，就补一句"go install 构建首次需要时下载"）、`scripts/build-dict-bundle.sh`（新建）、
`pkg/architecture/testdata/graph.json`。

**不许碰**：`pkg/memory/tokenize.go` 里的加载逻辑（它只经 `dictionaryPath` 取路径，本计划不需要改它）；
`InstallDictionary` 的行为（npm 包、make build、测试都依赖它）。

## 步骤

### 步骤 1：写入校验值

运行"算校验值"命令，把 4 个 hash 填进 `DictionaryFiles` 的 `SHA256` 字段。

**验证**：`grep -c 'SHA256: "' pkg/memory/dictionary.go` → `4`

### 步骤 2：测试守住校验值与模块内容一致

在 `dictionary_test.go` 新增 `TestDictionaryFilesDeclareTheBytesTheirModulesShip`：调用 `InstallDictionary(t.TempDir())`，
逐个计算 sha256，并与 `DictionaryFiles[i].SHA256` 比较。这条测试的意义在于：依赖升级（比如 Dependabot 升 gse）
改变词典内容时，CI 会失败，从而要求同步更新校验值。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/memory -run TestDictionaryFilesDeclare -count=1` → ok

### 步骤 3：实现 URL 判定与下载

按"设计" 3–6 实现。

**验证**：`CGO_ENABLED=1 go build -tags fts5 ./pkg/memory` exit 0

### 步骤 4：下载逻辑的单元测试

在 `dictionary_test.go` 用 `httptest.NewServer` 提供测试内构造的 tar.gz。构造两个小的假文件，
并传入与之匹配的 `[]DictionaryFile`：

- `TestDictionaryBundleURLOnlyForReleases`：`v1.2.3` → ok；`(devel)`、`v0.0.0-20260928-abcdef`、`v1.2.3-rc.1`、
  `v1.2.3+dirty`、`` → 不 ok。
- `TestDownloadDictionariesInstallsVerifiedFiles`：成功后两个文件内容正确，临时目录已删除
  （`filepath.Glob(filepath.Dir(dictDir)+"/dict-download-*")` 为空）。
- `TestDownloadDictionariesRejectsAChecksumMismatch`：返回错误，`dictDir` 下没有任何文件。
- `TestDownloadDictionariesRejectsUnexpectedEntries`：包里有 `../evil` 或未列出的文件时返回错误，
  `dictDir` 之外没有生成任何文件。
- `TestDownloadDictionariesRequiresEveryFile`：少一个文件时返回错误。
- `TestDownloadDictionariesReportsTheHTTPStatus`：404 时错误里含 `404`。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/memory -run 'TestDictionaryBundleURL|TestDownloadDictionaries' -count=1 -v` → 6 个 PASS

### 步骤 5：已发布资产的核对测试（默认跳过）

新增 `TestReleasedDictionaryBundleMatchesDictionaryFiles`。环境变量 `FOREBRAIN_RELEASE_DICT_CHECK` 为空时
`t.Skip`；非空时，把它当作版本号，用 `dictionaryBundleURL` 得到真实地址，下载到 `t.TempDir()` 并校验
全部四个文件。计划 008 的发版工作流在上传资产后会设置这个变量来运行它。

**验证**：不设变量时，`go test -tags fts5 ./pkg/memory -run TestReleasedDictionaryBundle -v` 输出 SKIP

### 步骤 6：发版用的打包脚本

新建 `scripts/build-dict-bundle.sh <out-dir>`：
```bash
#!/usr/bin/env bash
# Build the forebrain-dict.tar.gz release asset: every memory.DictionaryFiles
# entry, laid out as under dict/ beside a binary. A binary installed with
# `go install` downloads it on first need (pkg/memory/dictionary.go).
set -euo pipefail
[ "$#" -eq 1 ] || { echo "usage: $0 <out-dir>" >&2; exit 2; }
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$1"; OUT="$(cd "$1" && pwd)"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
"$ROOT_DIR/scripts/install-dictionary.sh" "$TMP"
tar -C "$TMP/dict" -czf "$OUT/forebrain-dict.tar.gz" .
echo "$OUT/forebrain-dict.tar.gz"
```
`chmod +x`。

**验证**：`scripts/build-dict-bundle.sh $TMPDIR/b && tar -tzf $TMPDIR/b/forebrain-dict.tar.gz | sort` 列出四个文件
（带 `./` 前缀）。

### 步骤 7：打包脚本与下载端的格式一致

步骤 4 的测试用的是测试内构造的包，所以这里要确认脚本产物符合下载端的约定：条目名规范化后恰好是四个
`Name`，并且都是普通文件。

**验证**：`tar -tvzf $TMPDIR/b/forebrain-dict.tar.gz` 中普通文件（以 `-` 开头的行）恰好 4 个，去掉 `./` 前缀后
与 `DictionaryFiles` 的 `Name` 一一对应；目录条目（以 `d` 开头）会被下载端跳过，属正常。用真实 Release
做端到端核对由计划 008 的发版工作流完成（运行步骤 5 的测试）。

### 步骤 8：收尾

全量测试、vet、gofmt、包图（新增 `pkg/memory → pkg/home` 边）。

## 真实仓库实测（关卡 G3，首个 Release 发布之后）

1. 下载端对真实 Release 资产的核对（生产代码路径）：
   `FOREBRAIN_RELEASE_DICT_CHECK=v0.1.0 CGO_ENABLED=1 go test -tags fts5 ./pkg/memory -run TestReleasedDictionaryBundleMatchesDictionaryFiles -count=1 -v`
   → PASS。版本号按 D1 的答复替换。
2. 真实二进制首次下载，在**真实 TUI + 真实模型**下触发（词典只在第一次需要中文/日文分词时下载）：
   ```bash
   cd "$(mktemp -d)" && env -u GOFLAGS GOWORK=off CGO_ENABLED=1 GOBIN="$PWD/bin" \
     go install -tags fts5 github.com/forebrain-harness/forebrain-harness/cmd/forebrain@v0.1.0
   test ! -e bin/dict && echo no-dict-yet
   ```
   → `no-dict-yet`。然后：
   - 临时 `FOREBRAIN_HOME=$H`；只读地复制 `~/.forebrain/forebrain.yaml` 里主 agent 的 `llm_providers` 段进
     `$H/forebrain.yaml`。文件里应当只有 `${ENV}` 引用；**若出现明文密钥，STOP**，不要复制、不要复述。
     对应环境变量不在当前 shell 时也 STOP，请 owner 提供；
   - 若工具表里没有 `memories_search`，按 `pkg/memory/settings.go` 的 `DedicatedToolsEnabled` 在临时配置里打开它；
   - 按 `.claude/skills/run-forebrain/driver.sh` 的 tmux 方式，在临时项目目录里启动 `FOREBRAIN_HOME=$H bin/forebrain`
     （信任提示选 Trust），发送：
     `用 memories_search 搜索『记忆检索词典』，告诉我返回了几条结果`；
   - 等 run 结束后退出 TUI。
3. **验证**：
   - `bin/dict/zh/s_1.txt`、`t_1.txt`、`stop_tokens.txt` 已存在；
   - `shasum -a 256 bin/dict/zh/*.txt` 与 `DictionaryFiles` 声明的值一致；
   - 当时目录下没有残留的 `dict-download-*`。
   再用一段日文重复一次，`bin/dict/ja/ipa.dict` 出现。
4. 断网复测：删除 `bin/dict`，在无网络环境下（例如设置 `HTTPS_PROXY=http://127.0.0.1:9`）重跑第 2 步的 TUI 流程。
   错误信息应包含原始网络报错，并且只有一行。
5. 记录全部输出。

## 完成标准

- [ ] M4 已按规范提交
- [ ] 真实仓库实测第 1–4 步通过并记录（G3 之后）
- [ ] 步骤 2、4 的测试全部通过，步骤 5 的测试默认跳过
- [ ] `scripts/build-dict-bundle.sh` 产出含四个文件的 tar.gz
- [ ] `go test -tags fts5 ./pkg/architecture` 通过（依赖边合法）
- [ ] `pkg/memory` 仍为 19 个生产文件
- [ ] 改动全部在允许清单内

## STOP 条件

- `pkg/architecture` 的分层测试不允许 `pkg/memory` 导入 `pkg/home`。此时不要自己引入全局变量绕过，
  要汇报，由 owner 决定版本从哪里传入。
- `InstallDictionary` 算出的 hash 在两次运行之间不一致（说明模块下载不确定）。
- 需要改 `tokenize.go` 的加载流程才能接入。

## 维护说明

- 升级 `github.com/go-ego/gse` 或 `github.com/ikawaha/kagome-dict/ipa` 时，
  `TestDictionaryFilesDeclareTheBytesTheirModulesShip` 会让 CI 失败。维护者按步骤 1 重算校验值并提交修正
  （首发后的 gateway 自维护见 README 的"推迟的批次"）。
- 词典列表、打包内容、校验值、下载校验只有一个出处：`DictionaryFiles`。新增语言的词典时，只需加一条
  （含 SHA256），打包脚本和下载会自动覆盖。
- go install 用户的 `dict/` 在 `$GOBIN`（通常是 `~/go/bin/dict/`）下。这是 owner 选定的位置。
