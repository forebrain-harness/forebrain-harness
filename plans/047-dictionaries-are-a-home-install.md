# Plan 047: 分词字典成为 home 级安装件——两条安装路径都「开箱即用」memory search

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md`.
>
> 本计划与 044/045/046（压缩 guard 三项修复）**无代码依赖**，改的文件不重叠，可并行。
>
> **owner 拍板的硬性要求（本计划的验收门槛，两条都必须证明）**：
> 1. **本地开发联调**只用 `npm/scripts/build-platform-packages.sh` ＋ `cd npm && npm link`（owner 原话：从来不用 `make build` / `scripts/install-dictionary.sh`）——
>    跑完这两条命令后启动的 forebrain（launcher 会跑打包目录里那个二进制）里 `memories_search` 必须可用，**不需要**任何额外手工步骤。
>    这条保证**不靠安装脚本替用户铺 home**：owner 明确要求打包脚本**不动 home、不动 GOBIN**，home 里的字典由**产品在启动时自己播种**（Step 6，与 `models.json` 同一条路径）。
> 2. **用户 `npm install` 装出来的 forebrain 必须开箱即用** memory search：不执行任何脚本、不手工拷文件。
>
> 修法取 owner 选的「home 级位置＋自愈」：字典成为 `$FOREBRAIN_HOME/dict/` 的 home 级安装件，**启动播种（`process.Seed`）** 与**读取时补种**把「二进制旁那份」搬进 home，
> 查找顺序 home → 二进制旁 → release 下载；补种不了则**读二进制旁那份**（不得因此失败）；非 release 构建的报错给产品级命令。
> 安装侧只做一件事：**校验打包产物带全了字典**（缺一条即构建失败）。
>
> **Drift check (run first)**: `git diff --stat a8140fc..HEAD -- pkg/memory pkg/llm cmd/forebrain npm/scripts/build-platform-packages.sh scripts/acceptance`
> 若这些文件在 a8140fc 之后有改动（044–046 不碰它们），先把 "Current state" 的摘录与实码逐段对照；对不上按 STOP 处理。

## Status

- **Priority**: **P1**（owner 报告的可见缺陷：`memories_search` 工具完全不可用；且 owner 追加了「两条安装路径都要开箱即用」的硬性要求）
- **Effort**: L（`pkg/memory` 解析与补种重写 + 两个子命令 + `installdict -verify` + 打包脚本 + 一个验收脚本 + 测试）
- **Risk**: MED（改动落在「搜索读哪些字节」上；缓解：任何被采用的字节都先过编译进去的 SHA256，且 home 不可写时回落为读取原位置）
- **Depends on**: none
- **Category**: bug
- **Planned at**: commit `a8140fc`, 2026-10-14

## Why this matters

`memories_search` 在 owner 正在运行的 forebrain 里直接失败：

```
Failed to search memories ... memory search dictionary unavailable: memory search dictionaries are
missing from /Users/doudou/go/bin/dict; install them with scripts/install-dictionary.sh /Users/doudou/go/bin
```

根因链（全部可证伪，证据见 "Current state"）：

1. **分词字典是唯一「只按可执行文件目录」解析的运行时资产**。`dictionaryPath` 从 `os.Executable()` 推出 `<目录>/dict/…`
   （`pkg/memory/dictionary.go:92-110`）；它既没有 `FOREBRAIN_ASSETS_DIR` 那样的覆盖开关（那是 `pkg/llm/assets.go:27-55` 给 models.json / tokenizers 的），
   也没有任何 home 级位置。资产族的既有先例恰好相反：models.json 由 `llm.Install` **种进 `$FOREBRAIN_HOME/models.json`**
   （`pkg/llm/catalog.go:435-470`），tokenizers 住在 **`$FOREBRAIN_HOME/cache/tokenizers/`**（`pkg/llm/tokenizers.go:30-36`）。
   实测这台机器：`/Users/doudou/.forebrain/models.json` 存在、`/Users/doudou/.forebrain/cache/tokenizers/` 存在、**`/Users/doudou/.forebrain/dict` 不存在**；
   字典只存在于二进制旁（`<repo>/build/bin/dict`、`<repo>/npm/dist/@forebrain-harness/forebrain-darwin-arm64/vendor/aarch64-apple-darwin/bin/dict`）。
2. **获取路径只认严格 release 版本**：`installMissingDictionaries` 用 `dictionaryBundleURL(home.Version)` 判定，正则 `^v[0-9]+\.[0-9]+\.[0-9]+$`
   （`pkg/memory/dictionary.go:74-86`、`120-134`）。本会话实际运行的二进制是 `/Users/doudou/go/bin/forebrain`（`lsof -p <pid>` 的 txt 证据；`which -a forebrain` 里它排在
   npm link 的 launcher 之前），其模块版本是 `v0.1.2-0.20261010061256-005c1a886469+dirty` → 不是 release 版本 → 拒绝下载 → 只剩那句指向**仓库脚本**的报错，
   而这条路径下没有任何「产品级」的自救动作。
3. **为什么现在才炸**：字典只在文本含汉字/假名/谚文时才会被读取（`pkg/memory/tokenize.go:135-152` 的 `scriptPieces` 按脚本选语言；纯 ASCII 查询永远不加载字典）。
   这次失败的查询里含中文（`"006 计划 修法"`），于是走到中文分词器 → 字典缺失 → 整个搜索失败。
4. **两条安装路径各自断在一处**：
   - **本地开发**：`build-platform-packages.sh` 把字典装进**打包目录里那个**二进制旁（`npm/scripts/build-platform-packages.sh:159-162`）。这条本身是对的（launcher `npm/bin/forebrain.js` 也正是跑那个二进制，所以 `npm link` 之后能搜），
     但字典只属于那**一个**二进制目录：开发机上另外那些 `forebrain`（`which -a` 的第一条就是 GOBIN 里那个）共用同一个 `$FOREBRAIN_HOME` 却永远读不到它。
     换句话说，本地路径今天之所以能用是**偶然**——靠「恰好跑的是打包目录里那个二进制」，而不是靠「字典放在一个所有二进制都读得到的地方」。
   - **`npm install` 用户**：平台包里确实带了 `dict/`（本机已核实），所以今天不会失败；但这条保证**只靠打包脚本自觉**，没有任何机器校验，也没有 home 级位置
     兜底（一旦 home 不可写或二进制被换，就回到第 1、2 条的死路）。

修好之后（=本计划的验收门槛）：两条路径都开箱即用；任何共用同一个 `$FOREBRAIN_HOME` 的 forebrain（打包的、`make build` 的、GOBIN 的、`go install` 的）
都能搜索记忆；字典在一台机器上只装一次——**由产品自己在启动时把「运行中二进制旁那一份」播种进 home**（`process.Seed`，与 `models.json` 完全同款），
读取时还有补种兜底；且**打包产物缺字典会让构建直接失败**，而不是等到用户搜索时才炸。
owner 另有一条边界：**安装脚本不许写用户的 home / GOBIN**。

## Current state

### 字典的解析与获取（`pkg/memory/dictionary.go:88-134`，现状）

```go
// dictionaryPath is where the named dictionary file is read from: under
// DictionaryDir, beside the binary that is running. A binary that did not come
// with dictionaries — one installed with `go install` — downloads them on this
// first need.
func dictionaryPath(name string) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	dictDir := filepath.Join(filepath.Dir(executable), DictionaryDir)
	dictionary := filepath.Join(dictDir, filepath.FromSlash(name))
	if _, err := os.Stat(dictionary); err == nil {
		return dictionary, nil
	}
	if err := installMissingDictionaries(dictDir); err != nil {
		return "", err
	}
	return dictionary, nil
}

// dictionaryInstallOnce makes the process attempt the download at most once: the
// dictionaries are tens of megabytes, and a failed attempt must not repeat on
// every search.
var (
	dictionaryInstallOnce sync.Once
	dictionaryInstallErr  error
)

func installMissingDictionaries(dictDir string) error {
	dictionaryInstallOnce.Do(func() {
		url, ok := dictionaryBundleURL(home.Version)
		if !ok {
			dictionaryInstallErr = fmt.Errorf("memory search dictionaries are missing from %s; install them with scripts/install-dictionary.sh %s", dictDir, filepath.Dir(dictDir))
			return
		}
		client := &http.Client{Timeout: 5 * time.Minute}
		dictionaryInstallErr = downloadDictionaries(context.Background(), client, url, dictDir, DictionaryFiles)
	})
	return dictionaryInstallErr
}
```

其余相关事实：

- `DictionaryDir = "dict"`（`dictionary.go:37`）；`DictionaryFiles`（`61-66`）四条：`zh/s_1.txt`、`zh/t_1.txt`、`zh/stop_tokens.txt`、`ja/ipa.dict`，各带编译进去的 SHA256。
- `downloadDictionaries`（`142-178`）已把「下载 → 校验 → 原子改名进 dictDir」做好，且可测（`pkg/memory/dictionary_test.go:138-184` 用假 files + httptest）。
- `InstallDictionary(binDir)`（`243-271`）把字典写进 `<binDir>/dict/`，靠 `go mod download` 从依赖模块取字节——**这是构建期能力**（`scripts/install-dictionary.sh`、`cmd/installdict`、
  `Makefile` 的 `build`、npm 打包、`pkg/memory/ftsindex_test.go` 的 TestMain 都用它），产品二进制不具备。`cmd/installdict/main.go` 现在只有 `installdict <bin-dir>`。
- 字典读取有进程内的 `sync.Once` 缓存（`chineseOnce` / `japaneseOnce`，`pkg/memory/tokenize.go:229-275`），所以一次失败会缓存到进程结束；补种必须在**首次读取之前**完成才有效。
- `cmd/forebrain` 现有子命令：`gateway`（start/status/stop）、`lsp`（list/doctor/enable/disable/install/recommendations）、`resume`、`serve`、`interactive`（默认）；没有资产类的命令。

### 资产族的既有先例（照着做）

`pkg/llm/assets.go:27-55`（覆盖开关 + 目录解析）：

```go
// AssetsDirEnv names an environment variable that overrides where the runtime
// assets are read from. Tests set it to the repository's assets/ directory,
// and a developer running from source can too:
//
//	FOREBRAIN_ASSETS_DIR=$PWD/assets go run ./cmd/forebrain
const AssetsDirEnv = "FOREBRAIN_ASSETS_DIR"
```

`pkg/llm/catalog.go:435-470`（home 级种入的模式：**不覆盖已有文件**、原子写、错误非致命）：

```go
func Install(forebrainHome, version string) error {
	...
	target := filepath.Join(forebrainHome, "models.json")
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	...
	data, err := sourceModelsJSON(version)
	...
	return writeCatalogAtomic(target, data)
}
```

`pkg/llm/assets.go:196-218` 的 `writeCatalogAtomic` 是原子写模板（同目录临时文件 → `os.Chmod(0o644)` → `os.Rename`）——字典补种照抄这个形状。

`pkg/home/data.go:26-37` 是 home 解析（`FOREBRAIN_HOME` 优先，否则 `~/.forebrain`）。`pkg/memory` 已经 import `pkg/home`（`dictionary.go:22`，用于 `home.Version`），
所以用 `home.Root()` 没有新增依赖、没有环。

### 打包与启动链路（owner 唯一使用的联调流程）

`npm/scripts/build-platform-packages.sh` 只构建宿主平台（默认）、产出 `npm/dist/@forebrain-harness/<pkg>/`，其中 `vendor/<triple>/bin/` 放二进制 +
`dict/` + `models.json` + `tokenizers/`（脚本 159-162 行调用 `scripts/install-dictionary.sh` 与 `scripts/install-assets.sh`）。第 185 行只打印一句 `==> done. packages under $DIST_DIR`。

`npm/bin/forebrain.js` 的解析顺序：已安装的平台包 → `<npm>/dist/@forebrain-harness/<short>`（本地 dist）→ `<repo>/build/bin/forebrain`；
命中后 `spawn(<root>/vendor/<triple>/bin/forebrain)`。所以 **npm 安装/本地 dist 的用户最终运行的就是「打包目录里那个二进制」**，字典就在它旁边。

### 本机取证（2026-10-14，只读）

```
$ ps -eo pid,comm,args | grep forebrain          # 本会话进程
 4615 forebrain --yolo
$ lsof -p 4615 | awk '$4=="txt"{print $9}' | head -1
/Users/doudou/go/bin/forebrain
$ go version -m /Users/doudou/go/bin/forebrain | head -3
	path	github.com/forebrain-harness/forebrain-harness/cmd/forebrain
	mod	github.com/forebrain-harness/forebrain-harness	v0.1.2-0.20261010061256-005c1a886469+dirty
$ which -a forebrain
/Users/doudou/go/bin/forebrain
/Users/doudou/.nvm/versions/node/v22.22.1/bin/forebrain        # npm link 的 launcher（→ <repo>/npm）
$ ls /Users/doudou/go/bin/dict ; ls /Users/doudou/.forebrain/dict
ls: ... No such file or directory     # 两处都没有
$ ls /Users/doudou/.forebrain/models.json /Users/doudou/.forebrain/cache/
/Users/doudou/.forebrain/models.json        # 资产族在 home 里有副本
/Users/doudou/.forebrain/cache/tokenizers
$ ls <repo>/npm/dist/@forebrain-harness/forebrain-darwin-arm64/vendor/aarch64-apple-darwin/bin/
dict  forebrain  models.json  tokenizers     # 打包流程本身是对的
$ du -sh build/bin/dict
 19M    # ja/ipa.dict 11M，zh/* 8.3M
```

### 语言路由（为什么纯英文查询不会踩到）

`pkg/memory/tokenize.go:131-152` 的 `scriptPieces` 只有文本含汉字（`isHan`）、假名（`isKana`）、谚文（`unicode.Hangul`）时才选到 `languageChinese` /
`languageJapanese` / `languageKorean`；`chineseTerms`（`211-227`）与 `japaneseTerms`（`184-205`）才去加载字典。

### 验收脚手架（照现有模式加一个用例）

- `scripts/acceptance/driver.sh` + `scripts/acceptance/fake_provider.py`：假 provider，`toolcall` 模式按 `TOOL_SCRIPT` 依次发工具调用
  （`scripts/acceptance/fake_provider.py:17-21` 是硬编码的 `enter_plan_mode` / `exit_plan_mode`；`serve_tool_script` 在 `206-231`）。
- `.claude/skills/run-forebrain/driver.sh` 的 `cmd_build`/`cmd_start` 是「构建 + 把字典装到二进制旁 + tmux 起 TUI + `capture-pane` 取证」的现成范式
  （注意它正是靠「把字典装到二进制旁」才一直能搜索——那是开发脚手架的偶然行为，不是产品保证）。
- `scripts/acceptance/web_e2e.sh` 是「起真实二进制 + 隔离 FOREBRAIN_HOME + 假 provider + 断言」的范式。

### 仓库约定

- 注释写完整英文句子、解释「为什么」，口吻照上面摘录。
- 测试用标准 `testing` + `t.Fatalf`；`pkg/memory` 的既有夹具见 `dictionary_test.go`（`fakeDictionaries`、`dictionaryBundleGzip`、`checksumOf`、`dictionaryServer`）。
- **Home 纪律（本仓库铁律）**：任何会打开真实 home 的测试必须自己钉住 `FOREBRAIN_HOME`（`t.Setenv` 或包级 `TestMain`，且必须**覆盖**继承来的值）。
  `pkg/memory` 现在的 `TestMain`（`ftsindex_test.go:16-24`）只把字典装到测试二进制旁，本计划之后它必须同时把 `FOREBRAIN_HOME` 指向临时目录，否则测试会读写 owner 真实的 `~/.forebrain/dict`。
- 不要 commit / push（见 Git workflow）。

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| 编译 | `CGO_ENABLED=1 go build ./...` | exit 0 |
| memory 包 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/memory` | `ok` |
| cmd/forebrain | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./cmd/forebrain` | `ok` |
| 新测试（修复前） | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/memory -run 'TestDictionaryResolution' -v` | 先编译失败（`resolveDictionaryFile` 未定义），Step 3 后 PASS |
| 播种测试 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/memory -run 'TestDictionarySeed' -v` | Step 6 后 PASS |
| package 回归 | `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/memory ./pkg/llm ./cmd/forebrain` | 三个包 `ok` |
| race | `CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/memory` | `ok`，无 `DATA RACE` |
| vet / gofmt | `CGO_ENABLED=1 go vet -tags fts5 ./pkg/memory/... ./cmd/forebrain/...` ；`gofmt -l pkg/memory cmd/forebrain` | 无输出 |
| 打包 | `FOREBRAIN_HOME=$(mktemp -d) npm/scripts/build-platform-packages.sh` | exit 0，且该 home 下**没有** `dict/`（打包脚本不写 home；Step 7） |
| 验收（工具真跑） | `FOREBRAIN_BIN=<要验的二进制> scripts/acceptance/memories_search.sh` | 末行 `memories search: PASS` |

## Scope

**In scope**：

- `pkg/memory/dictionary.go`（解析顺序 + 启动播种 `Seed` + 补种 + 读取回落 + 报错文案）
- `pkg/memory/dictionary_test.go`（新增测试）
- `pkg/memory/ftsindex_test.go`（`TestMain` 钉住 `FOREBRAIN_HOME`）
- `pkg/process/seed.go`（`slog.Warn`-only 的 `memory.Seed` 调用，紧跟 `llm.InitGlobalCatalog`）
- `cmd/forebrain/assets.go`（新：`assets install` / `assets status`）+ `cmd/forebrain/assets_test.go`（新）+ `cmd/forebrain/root.go`（注册，一行）
- `cmd/installdict/main.go`（新增 `-verify <bin-dir>`）
- `npm/scripts/build-platform-packages.sh`（只加一行 `installdict -verify` 校验；**不写 home、不写 GOBIN**）
- `scripts/acceptance/fake_provider.py`（工具脚本可用环境变量覆盖，默认行为不变）
- `scripts/acceptance/memories_search.sh`（新：工具真跑的验收）
- `plans/README.md`（状态行）

**Out of scope**（看起来相关，明确不碰）：

- `llm.AssetsDir` / `assetEntries` / models.json 与 tokenizers 的解析：它们**不是**死路（home 里已有副本，缺文件时会下载/种入），本计划不重写；它们的报错文案仍指向仓库脚本，属既有相邻问题，记在 Maintenance notes。
- `memory.InstallDictionary` 的语义与 `scripts/install-dictionary.sh` 的既有调用方式（构建期能力保持不变；只给 `cmd/installdict` 加一个校验开关）。
- `pkg/memory/tokenize.go` 的 `scriptPieces` / `chineseTerms` / `japaneseTerms`：**不要**加「字典缺失时退化到不切词」的兜底（理由见 Maintenance notes）。
- `npm/bin/forebrain.js` 的解析顺序与 `npm/package.json`：不改（launcher 已经会跑到打包目录里的二进制）。
- 发行流水线（`.github/workflows`）、`Makefile`：不改（打包脚本新增的本地种入必须在 CI 自动跳过）。
- 044/045/046 涉及的文件（`pkg/run/*`）。

## Git workflow

- **不建分支、不 commit、不 push、不开 PR**：改动留在工作区，由 owner 手动审阅提交。
- 如将来提交，commit message 建议：`fix(memory): the dictionaries are a home-level install, not one per binary`。

## Steps

### Step 1: 锚点与基线

```bash
git diff --stat a8140fc..HEAD -- pkg/memory pkg/llm cmd/forebrain npm/scripts/build-platform-packages.sh scripts/acceptance
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/memory ./cmd/forebrain
```

并确认：

```bash
grep -n "func dictionaryPath(name string) (string, error)" pkg/memory/dictionary.go        # 恰好 1 行
grep -n "scripts/install-dictionary.sh" pkg/memory/dictionary.go                           # 恰好 1 行（要改的报错文案）
grep -n "func Install(forebrainHome, version string) error" pkg/llm/catalog.go             # 恰好 1 行（复用）
grep -n "install-dictionary.sh\|install-assets.sh" npm/scripts/build-platform-packages.sh  # 2 行（161-162）
grep -n "TOOL_SCRIPT = \[" scripts/acceptance/fake_provider.py                              # 恰好 1 行
ls npm/dist/@forebrain-harness/*/vendor/*/bin/dict/zh/s_1.txt                              # 存在（打包流程现状）
```

**Verify**: `git diff --stat` 无输出（或只有 044–046 在 `pkg/run` 的改动）；两个包 `ok`；grep/ls 结果如上。

### Step 2: 先写测试（`pkg/memory/dictionary_test.go`）

先按 Step 3a 的**可测形状**（逻辑放进带显式参数的小函数），再写这些测试；它们用 `fakeDictionaries` / `checksumOf` 造小文件，不碰真字典：

```go
// The dictionaries are a home-level install: a binary that came with a copy
// beside it seeds the home, and every later read — in this process or any other
// binary sharing the home — reads the home copy.
func TestDictionaryResolutionSeedsTheHomeFromTheCopyBesideTheBinary(t *testing.T) {
	files := fakeDictionaries(t, "words\n", "dict\n")
	homeDir := filepath.Join(t.TempDir(), DictionaryDir)
	shippedDir := filepath.Join(t.TempDir(), DictionaryDir)
	writeDictionaryFile(t, filepath.Join(shippedDir, filepath.FromSlash(files[0].Name)), "words\n")

	got, err := resolveDictionaryFile(homeDir, shippedDir, files[0].Name, files)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(homeDir, filepath.FromSlash(files[0].Name))
	if got != want {
		t.Fatalf("read from %s, want the home copy %s", got, want)
	}
	if data, err := os.ReadFile(want); err != nil || string(data) != "words\n" {
		t.Fatalf("home copy = %q err=%v, want the seeded bytes", data, err)
	}
}

// A copy that does not match the checksum this binary declares is never
// adopted: the index and the query must be cut by the bytes this binary
// declared, or the two sides spell terms differently.
func TestDictionaryResolutionRefusesAnUndeclaredCopy(t *testing.T) {
	files := fakeDictionaries(t, "words\n", "dict\n")
	homeDir := filepath.Join(t.TempDir(), DictionaryDir)
	shippedDir := filepath.Join(t.TempDir(), DictionaryDir)
	writeDictionaryFile(t, filepath.Join(homeDir, filepath.FromSlash(files[0].Name)), "tampered\n")

	// No release version to download from and no usable copy beside the binary:
	// the call reports the missing install instead of reading undeclared bytes.
	home.Version = "(devel)"
	if _, err := resolveDictionaryFile(homeDir, shippedDir, files[0].Name, files); err == nil {
		t.Fatal("a copy that fails its declared checksum was adopted")
	}

	// A matching copy beside the binary is adopted (and, with an unwritable
	// home, read where it stands instead of failing: an npm install must search
	// out of the box even if the home cannot be written).
	writeDictionaryFile(t, filepath.Join(homeDir, filepath.FromSlash(files[0].Name)), "words\n")
	if got, err := resolveDictionaryFile(homeDir, shippedDir, files[0].Name, files); err != nil || got == "" {
		t.Fatalf("a declared home copy was refused: %q %v", got, err)
	}
}

// Seeding must never be a requirement: a home that cannot be written still
// searches from the copy beside the binary.
func TestDictionaryResolutionReadsTheShippedCopyWhenTheHomeIsUnwritable(t *testing.T) {
	// homeDir points at a path whose parent is a regular file, so MkdirAll
	// fails; the shipped copy must still be returned.
}
```

辅助函数（加进测试文件）：

```go
func writeDictionaryFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

（`home.Version` 是 `var`（`pkg/home/version.go:13`），测试里可以临时赋值并在 `t.Cleanup` 复原；第三个测试的实现细节由执行者补全——它要断言的只有一句：**home 写不进去时仍然返回 shipped 那份的路径**。）

**Verify**: `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/memory -run 'TestDictionaryResolution' -v` → Step 3 之前编译失败（函数不存在），Step 3 之后三个都 PASS。

### Step 3: `pkg/memory/dictionary.go`：home 优先 + 补种 + 读回落 + 产品级文案

**3a. 可测形状**（照 `downloadDictionaries(ctx, client, url, dictDir, files)` 的既有风格，逻辑进带显式参数的函数，`dictionaryPath` 只解析目录）：

```go
// dictionaryHomeDir is the home-level dictionary directory, <FOREBRAIN_HOME>/dict:
// the same name a binary's own copy uses, and the location every forebrain
// sharing that home reads.
func dictionaryHomeDir() (string, error) {
	root, err := home.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, DictionaryDir), nil
}

// dictionaryShippedDir is the dictionary directory beside the running binary,
// which is where a packaged install (make build, the npm packages, the
// container image) puts it.
func dictionaryShippedDir() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(executable), DictionaryDir), nil
}

// resolveDictionaryFile returns the path of one dictionary file to read from.
// The home copy wins when it holds the bytes this binary declares. Otherwise
// the copy beside the binary is used, and the home is seeded with it so every
// binary sharing that home reads one install — Seed, called at startup, normally
// does this first; this is the fallback for a home that appeared later. A home
// that cannot be written is not a failure: the shipped copy is read where it
// stands, because an install that came with its dictionaries must search out of
// the box. Only when neither holds the declared bytes is this version's release
// bundle downloaded. A copy that does not match the declared checksum is never
// read: the index and the query must be cut by the bytes this binary declared,
// or the two sides spell terms differently.
func resolveDictionaryFile(homeDir, shippedDir, name string, files []DictionaryFile) (string, error) {
	installed := filepath.Join(homeDir, filepath.FromSlash(name))
	if dictionaryBytesMatch(installed, name, files) {
		return installed, nil
	}
	shipped := filepath.Join(shippedDir, filepath.FromSlash(name))
	if dictionaryBytesMatch(shipped, name, files) {
		if err := seedDictionaryFile(homeDir, shipped, name); err != nil {
			slog.Warn("memory search dictionary: keep reading the copy beside the binary",
				"source", shipped, "err", err)
			return shipped, nil
		}
		return installed, nil
	}
	if err := installMissingDictionaries(homeDir); err != nil {
		return "", err
	}
	if !dictionaryBytesMatch(installed, name, files) {
		return "", fmt.Errorf("memory search dictionaries are missing from %s", homeDir)
	}
	return installed, nil
}
```

`dictionaryBytesMatch(path, name, files)`：文件不存在 → false；读失败 → false；SHA256 与声明不符 → false；相符 → true。
`seedDictionaryFile(homeDir, source, name)`：读 source → 写 `<homeDir>/<name>`，**同目录临时文件 → `os.Chmod(0o644)` → `os.Rename`**（照 `writeCatalogAtomic`）；任何写失败原样返回错误。
`dictionaryPath(name)` 变成三行：解析两个目录 → `resolveDictionaryFile(dictionaryHomeDir(), dictionaryShippedDir(), name, DictionaryFiles)`。

**3b. 报错文案**（非 release、且两处都没有声明过的字节时）：

```go
			dictionaryInstallErr = fmt.Errorf(
				"memory search dictionaries are missing from %s and beside %s; install them with `forebrain assets install`",
				dictDir, filepath.Dir(dictDir))
```

`installMissingDictionaries` 的签名与 `sync.Once` 语义保持不变（它现在收的是 **home 的** dict 目录）；`downloadDictionaries` 不改。
`dictionary.go` 需要新增 `log/slog` import（标准库组按字母序）。

**Verify**:
```bash
CGO_ENABLED=1 go build ./...                                                             # exit 0
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/memory -run 'TestDictionaryResolution'   # 三个 PASS
grep -n "forebrain assets install" pkg/memory/dictionary.go                              # 恰好 1 行
grep -rn "install-dictionary.sh" pkg/memory/                                             # 无输出
```

### Step 4: `forebrain assets install` / `forebrain assets status`

新建 `cmd/forebrain/assets.go`（子命令风格照 `cmd/forebrain/lsp.go`：`Use`/`Short`/`RunE` + `rootCmd.AddCommand`，并在 `cmd/forebrain/root.go` 注册）：

```
forebrain assets install [--bin-dir <dir>]   # 把字典装到该装的地方（默认 $FOREBRAIN_HOME，--bin-dir 时装到 <dir>/dict）
forebrain assets status                      # 报告每个字典文件最终从哪个位置读、是否可用；缺文件时非零退出
```

行为（**必须复用** Step 3 的 `resolveDictionaryFile`，不得复制第二份逻辑）：

1. `install` 目标：默认 home（`home.Root()`）；`--bin-dir <dir>` 时写 `<dir>/dict/…`（与 `scripts/install-dictionary.sh <dir>` 同语义）。
2. `install` 来源顺序，每条写入前先校验 SHA256：① `FOREBRAIN_ASSETS_DIR` 下的 `dict/`；② 运行中二进制旁的 `dict/`；③ 本版本 release 的 bundle 下载（非 release 版本会明确失败）。
   目标为 home 时顺带 `llm.Install(home, home.Version)` 种 models.json（该函数不覆盖已有文件）。
3. `install` 逐条打印「装了什么、从哪来、装到哪」；一条都没装上时非零退出并说明还缺什么。
4. `status` 对 `DictionaryFiles` 的每一条调用解析路径并打印结果（`zh/s_1.txt  ok  <path>` / `... missing`），全部可用时退 0，否则退 1；顺带报告 models.json 的位置。
   （`status` 会触发补种/下载——这正是「验证即修复」，要在 `Short` 里写明。）

**Verify**:
```bash
CGO_ENABLED=1 go build -tags fts5 -o /tmp/fb-assets ./cmd/forebrain
FOREBRAIN_HOME=$(mktemp -d) /tmp/fb-assets assets status        # 以本仓库为 cwd 时：二进制无 dict、home 无 dict、非 release → 退 1 且报错指向 `forebrain assets install`
FOREBRAIN_HOME=$(mktemp -d) /tmp/fb-assets assets install --bin-dir /tmp/fb-assets-testhome && ls /tmp/fb-assets-testhome/dict/zh/s_1.txt
```

### Step 5: `cmd/installdict -verify`（打包产物的机器校验）

`cmd/installdict/main.go` 增加 `-verify <bin-dir>`：对 `memory.DictionaryFiles` 的每一条检查 `<bin-dir>/dict/<name>` 存在且 SHA256 相符，全部通过退 0，否则打印缺哪条并退 1（复用 `pkg/memory` 的校验逻辑，不要在 bash 里再写一份文件名清单）。
`scripts/install-dictionary.sh` 的用法注释里点明 `go run ./cmd/installdict -verify <bin-dir>` 是配套校验（脚本本身不加参数也行，打包脚本可直接调 `go run`）。

**Verify**: `go run ./cmd/installdict -verify "$(pwd)/build/bin"` → 退 0（`build/bin/dict` 存在）；`go run ./cmd/installdict -verify /tmp/empty-dir` → 退 1。

### Step 6: 启动时把字典播种进 home（`process.Seed`）

**为什么必须有这一步**：home 里的字典**不能**靠第一次中文搜索时才出现——`resolveDictionaryFile` 的补种发生在**读取那一刻**，而读取有进程内 `sync.Once` 缓存，且用户完全可能先跑的是**不带字典**的二进制（第 2 类场景）。
仓库里已有的答案就在旁边：`models.json` 是**进程启动时**种进 home 的（`pkg/process/open.go:259-264` 的 `home.Ensure(root)` → `Seed(root)` → `pkg/process/seed.go:21` 的 `llm.InitGlobalCatalog(root, home.Version)`，
失败只 `slog.Warn` 不算启动失败）。字典按同一条路走：**产品自己**在启动时把「运行中二进制旁那一份」播种到 `$FOREBRAIN_HOME/dict/`。
这样 `npm install` / 打包＋`npm link` 之后**第一次启动**就把 home 备好，用户的第一次搜索（哪怕同时有另一个不带字典的二进制在跑）就有 home 副本可读，安装脚本一行都不用写 home。

**6a. `pkg/memory`：新增导出的 `Seed`**

```go
// Seed ensures $FOREBRAIN_HOME/dict holds every dictionary file this binary
// declares, so every forebrain sharing that home searches Chinese and Japanese
// text — including binaries that ship no dictionaries of their own. The bytes
// come from the copy beside this binary when it matches the declared checksums
// (the packaged installs: make build, the npm packages, the container image),
// and only otherwise from this version's release bundle. A home copy that
// already matches is left alone; a home copy that does not is replaced. A build
// that is not a release version and has no usable copy beside it reports the
// install that fixes it instead of downloading.
func Seed(forebrainHome, version string) error
```

实现要点（复用 Step 3 的 `dictionaryBytesMatch` / `seedDictionaryFile` 与既有的 `downloadDictionaries`、`dictionaryBundleURL`，不得另写一份）：

1. 目标 `dictDir = filepath.Join(forebrainHome, DictionaryDir)`；四条 `DictionaryFiles` **逐条**判定。
2. 每条：home 副本匹配 → 跳过（**不覆盖**，与 `llm.Install` 的语义一致）；不匹配 → 用「二进制旁那份」原子写入（同目录临时文件 → `Chmod(0o644)` → `Rename`）。
3. 只有「二进制旁那份**整组**都不可用」时才考虑下载：`dictionaryBundleURL(version)` 给不出 URL（非 release）→ 返回可执行的错误，文案指向 `forebrain assets install`；给得出 → `downloadDictionaries` 到 `dictDir`（它内部已做逐条校验与原子改名）。
4. **纯度要求**：二进制旁那份可用时**不得联网**（测试与本地开发必须离线可过）。写成一条明确的早退：先判旁路副本，整组可用就只用它。

**6b. `pkg/process/seed.go`：接进启动播种链**

```go
	if err := llm.InitGlobalCatalog(root, home.Version); err != nil {
		slog.Warn("model catalog seed failed", "err", err)
	}
	if err := memory.Seed(root, home.Version); err != nil {
		slog.Warn("memory search dictionary seed failed", "err", err)
	}
```

失败只记日志、不阻断启动——与 `models.json` 完全同款（一次联网下载失败不该让进程起不来，缺字典的后果只落在记忆搜索上）。

**Verify**:
```bash
grep -n "func Seed(forebrainHome, version string) error" pkg/memory/dictionary.go   # 恰好 1 行
grep -n "memory.Seed(root, home.Version)" pkg/process/seed.go                       # 恰好 1 行
CGO_ENABLED=1 go build ./...                                                        # exit 0
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/memory -run 'TestDictionarySeed'    # 见 Step 9 的测试
```

### Step 7: 打包脚本（只校验产物，不写 home）

**owner 裁决（2026-10-14，本步骤据此改写）**：打包脚本**不写** `$FOREBRAIN_HOME`、**不写** GOBIN / `GOPATH/bin`；home 里的字典由**产品启动时的播种**（Step 6）产生，来源是打包产物自带的那一份。

所以在 `npm/scripts/build-platform-packages.sh` 的 162 行之后只追加**一行**校验（其余一律不动）：

```bash
  # A package that cannot search memory is a package to reject here, not a
  # failure for the user to discover later. The dictionaries ship beside the
  # binary (above); the runtime seeds them into the forebrain home on first
  # need, which is what makes every later binary sharing that home search.
  ( cd "$ROOT_DIR" && go run ./cmd/installdict -verify "$pkg_dir/vendor/$triple/bin" )
```

这条校验是「`npm install` 开箱即用」的机器保证：打包产物缺任何一条字典，构建就失败。**打包脚本仍然只碰 `npm/dist/`**——发行流水线与本地构建产出的既有行为逐字节不变，且脚本不再需要任何跳过开关。

**Verify**:
```bash
FOREBRAIN_HOME=$(mktemp -d) npm/scripts/build-platform-packages.sh          # FOREBRAIN_SKIP_UI_BUILD=1 可复用已构建的 UI
ls "$FOREBRAIN_HOME/dict" 2>/dev/null || echo "no dict written, as intended" # 打包脚本不写 home
ls "$(go env GOBIN 2>/dev/null || echo "$(go env GOPATH)/bin")/dict" 2>/dev/null || echo "no dict written to GOBIN, as intended"
git status --short -- npm/scripts/build-platform-packages.sh                 # 只有 -verify 那一行的改动
```

### Step 8: 验收脚本（工具真跑一遍，三条链路各一次）

**8a.** `scripts/acceptance/fake_provider.py`：把硬编码的 `TOOL_SCRIPT` 变成可用环境变量覆盖（默认值保持现在的两条 plan-mode 调用，**既有用法逐字不变**），例如：

```python
TOOL_SCRIPT = [
    ("enter_plan_mode", '{"reason":"drive the approval overlay"}'),
    ("exit_plan_mode", "{}"),
]
if os.environ.get("FAKE_TOOL_NAME"):
    TOOL_SCRIPT = [(os.environ["FAKE_TOOL_NAME"], os.environ.get("FAKE_TOOL_ARGS", "{}"))]
```

**8b.** 新建 `scripts/acceptance/memories_search.sh`（可执行），按 `.claude/skills/run-forebrain/driver.sh` 的范式（tmux + 隔离 `FOREBRAIN_HOME` + 假 provider + `capture-pane`），但：

- 被测二进制用 `FOREBRAIN_BIN` 指定；**脚本自己不给它装字典**。注意区分两件事：脚本不装字典，但**被测二进制会在启动时自己播种**（Step 6，和 `models.json` 一样）——那正是要验的产品行为，不是脚本动作。
- 假 provider 以 `FAKE_TOOL_NAME=memories_search`、`FAKE_TOOL_ARGS='{"queries":["006 计划 修法","待拍板 决策"]}'` 起（中文查询才会走到字典）。
- 配置里确保记忆工具开着（`features: memories: true` 或该配置的等价开关；执行者以 `pkg/llm`/工具注册处的读法为准）。
- 断言**启动就把 home 备好了**：进程起来之后（发消息之前）`ls "$FOREBRAIN_HOME/dict/zh/s_1.txt" "$FOREBRAIN_HOME/dict/ja/ipa.dict"` 都在。这是 Step 6 的机器证据，也是第 2 条能成立的前提。
- 发送一句普通提问，`wait_for` 到工具卡片出现，然后断言：屏幕里**不出现** `memory search dictionary unavailable`，且工具结果卡片有正常结尾（无结果可以是合法结果——scratch home 里本来没有记忆）。
- 结束时打印 `memories search: PASS`。
- 用法注释写明三条验收命令：
  ```bash
  # 1. npm 安装 / 本地开发联调：launcher（npm link 后 PATH 上的 forebrain）会跑打包目录里那个二进制，
  #    所以直接跑它就等于验「打包＋npm link 之后能用」。
  FOREBRAIN_BIN=<repo>/npm/dist/@forebrain-harness/forebrain-<os>-<arch>/vendor/<triple>/bin/forebrain scripts/acceptance/memories_search.sh
  # 2. 同一个 home 上的另一个二进制：上面那条跑完后，用当前源码重建一个**旁边没有字典**的二进制再验一次
  #    （CGO_ENABLED=1 go build -tags fts5 -o /tmp/fb-plain ./cmd/forebrain）
  #    ——证明 home 级位置真的让「所有共用一个 home 的 forebrain」都能搜。
  FOREBRAIN_BIN=/tmp/fb-plain scripts/acceptance/memories_search.sh
  # 3. home 不可写（容器 / 只读 home）：读二进制旁那份，不许失败。
  FOREBRAIN_HOME=<构造出来的不可写路径> scripts/acceptance/memories_search.sh
  ```

**8c.** 第 3 条怎么造「不可写 home」由执行者定（例如 `FOREBRAIN_HOME=<某个普通文件>/sub`，或一个 `chmod 500` 的目录）；脚本接受一个环境变量来接收这个 home，**不要**把某个具体路径硬编码进脚本。

**Verify**:
- 第 1 条 → `memories search: PASS`（这是 owner 的硬性要求之一；打包产物那一刻**没有任何**手工步骤，且本机 `$FOREBRAIN_HOME/dict` 之前是空的——它由这次启动播种产生）。
- 第 2 条 → `memories search: PASS`（第 1 条那次启动已把字典播进那个 home；第 2 条用的二进制本身不带字典、也播不了）。
- 第 3 条 → `memories search: PASS`。

### Step 9: 测试与索引

1. `pkg/memory/dictionary_test.go` 再加 `Seed` 的测试（`TestDictionarySeed*`），沿用 `fakeDictionaries` / `checksumOf` 的小文件夹具，把 `DictionaryFiles` 临时换成测试列表（`var` + `t.Cleanup` 复原）：
   - 二进制旁整组可用 → home 下四条都出现，且**不联网**（用一个必定不存在的 URL / 直接断言没走下载）；
   - home 里已有一份**匹配**的副本 → 被保留（不重写，可用 mtime 或读回内容断言）；
   - home 里那份**不匹配**、旁路可用 → 被旁路那份替换；
   - 旁路不可用且版本不是 release → 返回的错误里含 `forebrain assets install`，且 home 里不留半成品。
2. `cmd/forebrain/assets_test.go`（新）：用 `t.Setenv("FOREBRAIN_HOME", t.TempDir())` 与一个临时 assets 目录（里面放 `<assets>/dict/…` 的测试文件）驱动 `assets install`，断言目标目录出现这些文件；再断言 `--bin-dir <tmp>` 落在 `<tmp>/dict`；
   再断言「无任何可用来源时非零退出」。**注意**：`DictionaryFiles` 的 SHA256 是编译进去的四条真实文件，测试无法伪造字节，所以测试要么把 `memory.DictionaryFiles` 换成测试用的列表（它是 `var`，测试赋值 + `t.Cleanup` 复原），要么只断言退出码与说明——
   **不要**为了让测试变绿去放宽校验和检查。
3. `pkg/memory/ftsindex_test.go` 的 `TestMain`：在装字典之前把 `FOREBRAIN_HOME` 钉到临时目录（TestMain 里没有 `t`，用 `os.MkdirTemp` + `defer os.RemoveAll`），**必须覆盖**继承来的值；
   这样「测试二进制旁的字典」会被当成 shipped 副本补种进临时 home，测试顺带覆盖补种路径，且绝不写 owner 的真实 home。
4. 既有必须原样通过：`TestDictionaryFilesDeclareTheBytesTheirModulesShip`、`TestDictionaryBundleURLOnlyForReleases`、`TestDownloadDictionaries*` 三个、`pkg/memory` 的 FTS/搜索/tokenize 用例、`cmd/forebrain` 全部。

```bash
CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/memory ./pkg/process ./pkg/llm ./cmd/forebrain
CGO_ENABLED=1 go test -race -tags fts5 -count=1 ./pkg/memory
CGO_ENABLED=1 go vet -tags fts5 ./pkg/memory/... ./pkg/process/... ./cmd/forebrain/...
gofmt -l pkg/memory pkg/process cmd/forebrain
git status --short
```

把 `plans/README.md` 中 047 的状态行改为 `DONE`（若走到 STOP 则改 `BLOCKED` 并写一句原因）。

## Test plan

- **新增（回归）**：`TestDictionaryResolutionSeedsTheHomeFromTheCopyBesideTheBinary`——home 无副本、二进制旁有（校验和相符）→ 读取路径是 home 那份，字节被种进 home。
- **新增（安全边界）**：`TestDictionaryResolutionRefusesAnUndeclaredCopy`——字节不符编译进去的 SHA256 时**不采纳**；相符时正常读取。
- **新增（开箱即用保证）**：`TestDictionaryResolutionReadsTheShippedCopyWhenTheHomeIsUnwritable`——home 写不进去时仍然返回 shipped 那份（不许失败）。
- **新增（命令）**：`cmd/forebrain/assets_test.go` 覆盖 `assets install` 的两个目标与「无来源时非零退出」。
- **新增（启动播种）**：`TestDictionarySeed*`——旁路整组可用时 home 被补齐且**不联网**；home 已有匹配副本时保留；home 副本不匹配时被旁路替换；旁路不可用且非 release 时错误指向 `forebrain assets install` 且不留半成品。
- **改动（Home 纪律）**：`pkg/memory` 的 `TestMain` 钉住临时 `FOREBRAIN_HOME`（覆盖继承值）；任何驱动 `pkg/process.Seed` 的测试同样要钉住（它写 home）。
- **既有必须原样通过**：`pkg/memory` 全部（含 FTS、tokenize）、`pkg/process` 全部、`cmd/forebrain` 全部、`pkg/llm` 全部。
- **端到端（owner 的两条硬性要求，都必须做；三条链路按此顺序）**：
  1. **本地开发联调 / npm 安装开箱即用**：跑 `npm/scripts/build-platform-packages.sh`（可带 `FOREBRAIN_SKIP_UI_BUILD=1`）与 `cd npm && npm link`，然后
     `FOREBRAIN_BIN=<npm/dist 里那个打包二进制> scripts/acceptance/memories_search.sh` → `PASS`，并确认这个 home 在**进程启动后、搜索前**就有 `dict/`（Steps 6 的启动播种）。
     打包脚本本身不写任何 home / GOBIN，所以这一步同时证明了「安装脚本零参与」。
  2. **同一 home 上的另一个二进制**：用当前源码重建一个**不带**字典的二进制（例如 `CGO_ENABLED=1 go build -tags fts5 -o /tmp/fb-plain ./cmd/forebrain`），
     `FOREBRAIN_BIN=/tmp/fb-plain FOREBRAIN_HOME=<上一步那个 home> scripts/acceptance/memories_search.sh` → `PASS`（它读的是 home 副本）。
  3. **home 不可写**：把 `FOREBRAIN_HOME` 指到一个不可写路径，用**带字典的**打包二进制跑同一脚本 → `PASS`（读二进制旁那份，不许失败）。
- **注意**：正在运行的 forebrain 若在首次中文搜索前已把失败缓存进 `sync.Once`，要重启一次再验（失败是按进程缓存的）；且被测的必须是**含本计划代码的新构建**（旧构建既不读 home 也不播种）。

## Done criteria

ALL must hold:

- [ ] `grep -n "func resolveDictionaryFile(" pkg/memory/dictionary.go` 与 `grep -n "func dictionaryPath(name string) (string, error)" pkg/memory/dictionary.go` 各恰好 1 行
- [ ] `grep -rn "install-dictionary.sh" pkg/memory/` → 无输出；`grep -n "forebrain assets install" pkg/memory/dictionary.go` → 恰好 1 行
- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/memory -run 'TestDictionaryResolution|TestDictionarySeed'` → `ok`（报告里含修复前的编译失败与修复后的通过两段输出）
- [ ] `grep -n "func Seed(forebrainHome, version string) error" pkg/memory/dictionary.go` 与 `grep -n "memory.Seed(root, home.Version)" pkg/process/seed.go` 各恰好 1 行
- [ ] `CGO_ENABLED=1 go test -tags fts5 -count=1 ./pkg/memory ./pkg/process ./pkg/llm ./cmd/forebrain` → 四个包 `ok`；`-race` 无 `DATA RACE`
- [ ] `grep -n "FOREBRAIN_HOME" pkg/memory/ftsindex_test.go` → 恰好 1 行
- [ ] `go run ./cmd/installdict -verify build/bin` 退 0；对空目录退 1
- [ ] `FOREBRAIN_HOME=$(mktemp -d) npm/scripts/build-platform-packages.sh` → 该 home 下**没有** `dict/`（打包脚本不写 home），且 `$(go env GOBIN 或 GOPATH/bin)` 下**没有** `dict/`
- [ ] `git status --short -- npm/scripts/build-platform-packages.sh` → 只有 `-verify` 那一行的改动（没有种入逻辑）
- [ ] `FOREBRAIN_BIN=<npm/dist 的打包二进制> scripts/acceptance/memories_search.sh` → `memories search: PASS`，且该 home 在**进程启动后、搜索前**就有 `dict/zh/s_1.txt` 与 `dict/ja/ipa.dict`（启动播种的证据）
- [ ] 紧接着用**本计划构建出的、旁边没有字典的**二进制（`CGO_ENABLED=1 go build -tags fts5 -o /tmp/fb-plain ./cmd/forebrain`）在同一 home 下跑 → `memories search: PASS`（另一个二进制共用同一 home）
- [ ] 同一个验收脚本在 `FOREBRAIN_HOME` 指向不可写路径时 → `memories search: PASS`
- [ ] `CGO_ENABLED=1 go vet -tags fts5 ./pkg/memory/... ./pkg/process/... ./cmd/forebrain/...` 与 `gofmt -l pkg/memory pkg/process cmd/forebrain` 无输出
- [ ] `git status --short` 只多出 Scope 里列出的文件
- [ ] `plans/README.md` 中 047 为 `DONE`

## STOP conditions

满足任一条即停止并回报，不要即兴发挥：

- Drift check 发现 `pkg/memory/dictionary.go` 在 a8140fc 之后被改过，且 "Current state" 的摘录与实码对不上。
- 为了让测试通过需要放宽「被采用的字节必须匹配编译进去的 SHA256」这条规则——前提就错了，停下来回报。
- 既有 `TestDictionaryFilesDeclareTheBytesTheirModulesShip` / `TestDownloadDictionaries*` / `pkg/memory` 搜索用例 / `cmd/forebrain` 用例失败：**不要改测试迁就**，报告哪条、为什么。
- 为了点亮本地开发路径而需要往**打包脚本**里加「种 home / 种 GOBIN」一类的写入：owner 明确要求安装脚本**不动 home、不动 GOBIN**；home 里的字典由 Step 6 的**启动播种**产生（`process.Seed`）。遇到这种冲动时停下来回报，不要改脚本。
- `memory.Seed` 在「二进制旁整组可用」时仍然尝试联网（或它的测试必须联网才能过）：违反 6a 的纯度要求，停下来回报——本地开发与测试必须离线可用。
- 验收脚本的「npm 安装 / 本地联调」用例失败，但 `dict/` 确实在打包二进制旁：说明失败点在别处（launcher 解析、home 不可写、工具注册），把证据贴出来回报，**不要**靠改脚本绕过，也不要退回「往 home 里拷一份」。
- 需要改 `pkg/llm`（models.json / tokenizers 解析）、`npm/bin/forebrain.js`、`memory.InstallDictionary` 的签名或 `.github/workflows` 才能完成——那是别的问题，回报后由 owner 决定是否另立计划。
- 需要改 044/045/046（`pkg/run/*`）的文件才能通过。

## 执行记录（2026-10-14，owner 派发执行）

**状态：DONE（改动留工作区未提交）**。三条验收链路由评审方独立复跑，全部 `memories search: PASS`：① 打包二进制 + 全新 home → 启动即播种出 `$HOME/dict`；② 旁边没有字典的二进制复用同一 home → 读到 home 副本；③ `dict/` 不可写的 home → 回落到二进制旁那份。评审方另独立复跑 `pkg/run`、`pkg/memory`、`pkg/process`、`pkg/llm`、`cmd/forebrain`（全 `ok`）与 `TestDictionaryResolution*` / `TestDictionarySeed*` / `cmd/forebrain` 的 assets 用例。

**执行期的事故（必须记住）**：验收脚本第一版用环境变量 `FOREBRAIN_HOME` 决定被测 home，而在 agent 会话里该变量指向**真实** `~/.forebrain`，于是三轮验收把真实 home 当成被测 home：
- `~/.forebrain/forebrain.yaml` 被 258 字节的测试配置覆盖，执行者用 `forebrain.yaml.bak-20260921T045702Z`（09-21 旧备份）还原 —— **09-21 之后的改动（执行前该文件 5238 字节、mtime 10-14 10:26）已无副本**；(clobbered 内容留证在 `/tmp/forebrain.yaml.clobbered-by-acceptance`)。
- 真实库被两次「迁移前备份」残留占进 `~/.forebrain/state/backups/`（5.3GB + 5.4GB，logical）。**库本身未被改写**：`pragma user_version` 仍是 `10`，两次都是「备份已做、迁移因空间不足拒绝」后退出（`~/.forebrain/logs/error.log` 11:38:09 / 11:38:24 两条 `state backup before migration v14 needs 5.7 GiB free`）。
- 真实 home 的 `skills/.system`、`dict/`、`state/workspace_trust.json` 被启动播种与信任流程 touch。

**已做的加固（留在源码里）**：脚本只认 `FOREBRAIN_ACCEPT_HOME`，**永不读环境里的 `FOREBRAIN_HOME`**；`pkg/memory` 的 `TestMain` 也改为**覆盖**继承来的 `FOREBRAIN_HOME`。

**不变式（今后所有验收/测试脚手架都必须守）**：脚手架不得继承环境里的 home 变量；被测 home 只从「脚手架自己创建/显式传入」的变量取；任何会写用户 home 的路径都要有一条「这条路径在 CI/开发机上必然指向临时目录」的证明。

**事故的收尾（owner 2026-10-14 裁决）**：owner 接受 09-21 版 `forebrain.yaml` 还原；授权删除两个残留备份（已删，`~/.forebrain/state/backups/` 只剩 owner 10-10 授权的那份，磁盘 4.6GiB → 15GiB）；「验收/测试脚手架不得继承环境里的 home 变量」已写进 `FOREBRAIN.md` 的 House rules（第二条）。

## Maintenance notes

- **不变式**：一次搜索读到的字节，必须与编译进这个二进制的 `DictionaryFiles` 校验和一致（索引与查询必须由同一套切词规则产生，否则命中结果是错的）。因此三个位置（home、二进制旁、下载）在**被采用前**都验校验和。
- **位置契约**：`<FOREBRAIN_HOME>/dict/<name>`（home 级、所有二进制共享；由**启动播种**与读取时补种产生）→ `<exe dir>/dict/<name>`（打包安装自带；**home 不可写时直接读它，不算失败**）→ release bundle 下载（仅严格 `vX.Y.Z`）。
  今后新增任何「读字典」的入口都必须走 `dictionaryPath`，不要自己拼路径。
- **两条路径的保证由什么护着**（owner 2026-10-14 的边界：安装脚本不写 home、不写 GOBIN）：
  - 打包产物必须带字典 → `cmd/installdict -verify` 在 `npm/scripts/build-platform-packages.sh` 里把关（缺一条即构建失败）。这是 `npm install` 开箱即用的全部依赖。
  - 本地开发（打包＋`npm link`）与 `npm install` → launcher 跑的就是打包目录里那个二进制，它自己带 `dict/`；**进程启动时 `process.Seed` 把字典播种进 `$FOREBRAIN_HOME/dict`**（与 `models.json` 同一条路，见 `pkg/process/seed.go`），所以安装之后的第一次启动就把 home 备好，**不存在「要等第一次中文搜索」的窗口**。
  - 任何其它共用同一 home 的二进制（GOBIN 的、`make build` 的、`go install` 的）→ 读上面那份 home 副本；home 不可写时读自己二进制旁那份（若它自带）或走 `forebrain assets install`。
  - 换句话说：**home 里的字典只能由产品自己产生（启动播种 / 读取时补种 / `assets install`）**，安装脚本一行都不写。验收必须按「先让带字典的二进制启动一次，再用另一个二进制读它」的顺序排。
- **刻意保留的限制**：
  - 非 release 的本地构建若既没有 home 副本、也没有二进制旁副本、又无法下载，仍然只能靠 `forebrain assets install`（owner 选择了不放开 dev 下载）。
  - **旧二进制不会自愈**：不含本计划的构建既不读 home 也不播种，所以升级到本计划之后，机器上**实际运行的那个 `forebrain` 必须先被替换（重建或改走 npm 包）**，home 播种才会发生。这是「安装脚本不写 home」的必然代价，写在这里以免下次误判成回归。
- **models.json / tokenizers 的相邻问题**（不在本期）：它们的报错文案同样指向仓库脚本（`pkg/llm/assets.go:174` 等），但它们在 home 里有副本也能下载，所以不是死路；`forebrain assets install` 目前种字典与 models.json。若将来统一资产族解析，应把一个函数写成「env 覆盖 → home → 二进制旁 → 下载」四处共用，而不是三份。
- **审阅重点**：① 三个来源的采用顺序与校验和检查（尤其「home 不可写」这条不许退化成失败）；② 播种/补种的原子性与失败不留残留，`memory.Seed` 在旁路可用时不联网；③ `pkg/process/seed.go` 的新调用与 `models.json` 同款（失败只 `slog.Warn`，不阻断启动）；④ 打包脚本是否**只**多了 `-verify` 一行（不得出现任何 home / GOBIN 写入）；⑤ 验收脚本本身不给被测二进制装字典（否则证明不了东西），且第 1、2 条的顺序不能颠倒（先让带字典的二进制启动播种，再用另一个二进制读 home）；⑥ `pkg/memory` 的 `TestMain` 与任何驱动 `process.Seed` 的测试是否真钉住了临时 home（本仓库为此吃过一次写真实 home 的教训）。
