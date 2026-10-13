# Plan 039：models.json 不再嵌入二进制——随 npm 包 / 发行归档 / 镜像装在二进制旁，首次运行种入 `$FOREBRAIN_HOME/models.json`

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
> 文件移动用 `git mv`（只改索引里的路径，不算提交）。
>
> **Drift check（先跑）**：`git diff --stat 005c1a8 -- pkg/llm/embed.go pkg/llm/catalog.go pkg/process/runtime.go pkg/process/seed.go Makefile Dockerfile npm/scripts/build-platform-packages.sh scripts README.md CONTRIBUTING.md .claude/skills/run-forebrain`
> 应无输出。有输出 → 对照 "Current state" 摘录核对，不符 → STOP。

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED（改变一个运行时必需文件的分发方式；每个构建/打包入口都要跟上）
- **Depends on**: 无（041 依赖本计划建立的 `assets/` 目录与 `installassets` 安装器）
- **Category**: tech-debt / packaging
- **Planned at**: commit `005c1a8` + 主树未提交改动（不涉及本计划文件），2026-10-11

## Why this matters

owner 2026-10-11 决定：**禁止把运行时数据文件嵌入二进制**；models.json 必须改成和分词词典一样，随 npm 包分发、由安装放到二进制旁边。
现在 models.json 由 `//go:embed` 编进二进制，首次运行再写到 `$FOREBRAIN_HOME/models.json`。改完后二进制不再携带它；
它和词典一样由每个产出可运行二进制的入口（`make build`、npm 平台包、发行归档、容器镜像、测试驱动脚本）放到二进制旁，首次运行仍按原规则种入 `$FOREBRAIN_HOME/models.json`（已存在则永不覆盖）。
后续 Plan 041 的分词器文件走同一条路，所以本计划建立的 `assets/` 目录、`installassets` 安装器与"二进制旁资源目录"解析要设计成可扩展的。

## Current state（全部已核实）

- 嵌入：`pkg/llm/embed.go` 全文：

```go
package llm

import _ "embed"

//go:embed model_assets/models.json
var embeddedModelsJSON []byte

// EmbeddedModelsJSON returns the build-time embedded models.json catalog
// bytes. ...
func EmbeddedModelsJSON() []byte {
	return embeddedModelsJSON
}
```

- 种入：`pkg/llm/catalog.go:434-459` `Install(forebrainHome string) error`：目标 `$home/models.json` 已存在就返回；否则 `os.WriteFile(target, embeddedModelsJSON, 0o644)`。
  `InitGlobalCatalog(home string) error`（`:310` 起）先 `Install(home)` 再 `Resolve(home)`。
- 调用方：`pkg/process/runtime.go:103`（`llm.InitGlobalCatalog(home)`，失败只 `slog.Warn`）、`pkg/process/seed.go:21`（同）。两者都已导入 `pkg/home`（`runtime.go` 别名 `homepkg`，`seed.go` 名 `home`）。
- 版本号：`pkg/home/version.go` 的 `home.Version`，发行构建形如 `v0.3.1`，`go install` 构建为模块版本，源码构建为伪版本或 `(devel)`。
- **分层约束**：`pkg/llm` 是第 0 层（`pkg/architecture/cache_test.go` `packageLayer`），**不能导入** `pkg/home` / `pkg/config` 等任何上层包；版本号只能由调用方传入。`llm` 的扇出预算 `fanOutBudgets["llm"] = 8`。
- 词典先例（照着做）：`pkg/memory/dictionary.go`——文件不编进二进制，装在二进制旁的 `dict/`；运行时用 `os.Executable()` + `filepath.EvalSymlinks` 定位；`go install` 构建首次需要时从该版本的 GitHub release 下载（仅 `^v\d+\.\d+\.\d+$` 版本）；安装器 `cmd/installdict/main.go` + `scripts/install-dictionary.sh <bin-dir>`（脚本 `cd` 到仓库根执行 `go run ./cmd/installdict "$BIN_DIR"`）。
- 调用 `install-dictionary.sh` 的入口（本计划要在**每一处**紧跟着加装 assets）：
  - `Makefile:44`（`build` 目标）
  - `Dockerfile:26`（`RUN scripts/install-dictionary.sh /out`；最终镜像 `COPY --from=gobuild /out/dict /app/dict`）
  - `npm/scripts/build-platform-packages.sh:148`（以及文件头 `:1-7` 的布局说明）
  - `scripts/acceptance/driver.sh:36`、`scripts/acceptance/live_subagent.sh:67`、`scripts/acceptance/web_e2e.sh:79`
  - `.claude/skills/run-forebrain/driver.sh:37`、`.claude/skills/run-forebrain/SKILL.md:121`
  - 文档：`README.md:219`、`README.md:249`（表格）、`README.md:87-89`（go install 段落）、`CONTRIBUTING.md:36`
  - `scripts/package-release-archive.sh` 从 npm 平台包目录整体打包（`tar -C "$bin_dir" -czf ... .`），只需更新文件头注释。
- 测试里用嵌入目录的 11 处（全部要改）：`pkg/llm/request_context_test.go:254,328`、`pkg/config/agent_llm_test.go:12`、`pkg/tui/chat_session_test.go:11395`、`pkg/tui/setup_test.go:193,490`、`pkg/turn/models_test.go:120`、`pkg/turn/input_parts_test.go:652`、`pkg/architecture/cache_test.go:959`、`pkg/run/orchestration_llm_test.go:2083`、`pkg/gateway/channels_test.go:435`、`pkg/process/provider_test.go:12`。
  写法都是 `llm.Parse(llm.EmbeddedModelsJSON())`。另有 `pkg/llm/catalog_test.go:110` `TestInitGlobalCatalogSeedsAFreshHome` 依赖嵌入种入。
- 仓库根有 `go.mod`；架构测试里的 `findRepoRoot`（`pkg/architecture/cache_test.go:124`）是"从 cwd 往上找 go.mod"的写法，可照抄。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 全部 `ok` |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./...`；`gofmt -l cmd pkg` | 无输出 |
| 构建 + 安装 | `make build` | 成功，`build/bin/models.json` 存在 |
| 包图 | `scripts/package-graph.sh` 后 `git diff --stat -- pkg/architecture/testdata/graph.json` | 有变化即连同提交（CI 要求） |
| npm 包 | `FOREBRAIN_SKIP_UI_BUILD=1 npm/scripts/build-platform-packages.sh` | 成功，`npm/dist/@forebrain-harness/*/vendor/*/bin/models.json` 存在 |

## Scope

**In scope**：
- `git mv pkg/llm/model_assets/models.json assets/models.json`；删除 `pkg/llm/embed.go`
- `pkg/llm/catalog.go`、新建 `pkg/llm/assets.go`（及其测试）
- 新建 `cmd/installassets/main.go`、`scripts/install-assets.sh`
- `pkg/process/runtime.go`、`pkg/process/seed.go`（只改 `InitGlobalCatalog` 调用）
- Current state 列出的所有入口脚本、Dockerfile、Makefile、README/CONTRIBUTING、run-forebrain 技能文件
- 11 个测试文件 + `pkg/llm/catalog_test.go`；因 Seed/Open 拿不到目录而失败的测试包（加 `TestMain`，见 Step 5）
- `pkg/architecture/testdata/graph.json`（脚本重新生成）

**Out of scope**：
- `.github/workflows/*`：发行归档与 npm 包都从 `build-platform-packages.sh` 的产物来，不需要改工作流；`go install` 构建运行时自行下载（Step 2）。
- 已存在的 `$FOREBRAIN_HOME/models.json`：永不覆盖（行为不变），不写迁移。
- 分词器文件（Plan 041）。
- 系统技能（`skill.Install`）等其他内置资源：owner 本次只点名 models.json。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：移动文件、删掉嵌入、建"二进制旁资源目录"解析

1. `git mv pkg/llm/model_assets/models.json assets/models.json`；删除 `pkg/llm/embed.go`；`pkg/llm/model_assets/` 应为空目录并删除。
2. 新建 `pkg/llm/assets.go`（英文注释，风格照 `pkg/memory/dictionary.go`）：
   - `const AssetsDirEnv = "FOREBRAIN_ASSETS_DIR"`：设置时直接用它（测试与从源码 `go run` 用；仓库 `assets/` 的布局就是安装后的布局）。
   - `func AssetsDir() (string, error)`：env 优先；否则 `os.Executable()` → `filepath.EvalSymlinks` → `filepath.Dir`。
   - `var assetEntries = []string{"models.json"}`（注释说明：每个入口安装的就是这份清单；Plan 041 会加 `"tokenizers"`）。
   - `func InstallAssets(srcDir, binDir string) error`：把 `srcDir` 下 `assetEntries` 列出的每一项（文件或目录，目录递归）复制到 `binDir` 同名位置；目标已存在则覆盖（安装器每次构建都要拿到当前版本）；源缺失 → 返回错误。
   - `func RepoAssetsDirForTest() (string, error)`：从 `os.Getwd()` 往上找 `go.mod`，返回 `<root>/assets`；文档注释写明"only for tests run from inside the repository"。
   - `func RepoModelsJSONForTest() ([]byte, error)`：读 `RepoAssetsDirForTest()/models.json`。

**Verify**：`go build -tags fts5 ./...` 失败点只剩 `EmbeddedModelsJSON` / `embeddedModelsJSON` 的引用（Step 2、5 处理）；`grep -rn 'go:embed' pkg/llm` 无输出。

### Step 2：`Install` 改为从二进制旁种入，发行版缺文件时下载

改 `pkg/llm/catalog.go`：

- 签名改为 `Install(forebrainHome, version string) error`、`InitGlobalCatalog(home, version string) error`。
- 逻辑：目标 `$home/models.json` 已存在 → 返回 nil（永不覆盖，原注释保留）。否则：
  1. `AssetsDir()/models.json` 存在 → 读出、`Parse` 校验通过 → 原子写入（同目录临时文件 + `os.Rename`）。
  2. 不存在且 `version` 匹配 `^v[0-9]+\.[0-9]+\.[0-9]+$` → 从 `https://raw.githubusercontent.com/forebrain-harness/forebrain-harness/<version>/assets/models.json` 下载（`http.Client{Timeout: 2 * time.Minute}`，`io.LimitReader` 32 MiB，状态码必须 200，`Parse` 通过才写入，原子写）。URL 模板放一个**未导出包级变量**，测试可替换。
  3. 否则返回错误：`models.json is missing beside the binary (<dir>); install it with scripts/install-assets.sh <dir>`。
- `pkg/process/runtime.go:103` 与 `pkg/process/seed.go:21` 传入版本：`llm.InitGlobalCatalog(home, homepkg.Version)` / `llm.InitGlobalCatalog(root, home.Version)`。

**Verify**：`go build -tags fts5 ./...` 通过（测试文件除外）。

### Step 3：安装器与所有入口

1. 新建 `cmd/installassets/main.go`：仿 `cmd/installdict/main.go`，用法 `go run ./cmd/installassets <bin-dir>`，调用 `llm.InstallAssets("assets", os.Args[1])`（从仓库根运行；文件头注释说明"Every build that produces a binary someone runs installs them this way"）。
2. 新建 `scripts/install-assets.sh`：仿 `scripts/install-dictionary.sh`（同样的参数检查、`mkdir -p`、`cd "$ROOT_DIR" && go run ./cmd/installassets "$BIN_DIR"`），`chmod +x`。
3. 在 Current state 列出的每一处 `install-dictionary.sh` 调用后紧跟一行 `install-assets.sh`（同样的目录参数）：Makefile、npm 构建脚本、三个 acceptance 脚本、run-forebrain 的 `driver.sh` 与 `SKILL.md`。Makefile 的 echo 改成 "...with embedded UI, its dictionary and models.json"。
4. Dockerfile：构建阶段 `RUN scripts/install-dictionary.sh /out && scripts/install-assets.sh /out`；最终阶段加 `COPY --from=gobuild /out/models.json /app/models.json`。
5. 文档：`README.md:219` 与 `CONTRIBUTING.md:36` 的代码块加 `scripts/install-assets.sh ./build/bin   # models.json beside the binary`；`README.md:249` 表格改为 "`go build` + `scripts/install-dictionary.sh` + `scripts/install-assets.sh`"；
   `README.md:87-89` 段落补一句 go install 构建首次运行会从该版本的源码下载 models.json；`npm/scripts/build-platform-packages.sh` 文件头布局说明加 `vendor/{triple}/bin/models.json`；`scripts/package-release-archive.sh` 文件头注释改为 "forebrain[.exe], dict/ and models.json"。

**Verify**：`make build && ls build/bin/models.json` 存在；`grep -rn 'install-dictionary.sh' Makefile Dockerfile npm scripts .claude/skills/run-forebrain | wc -l` 与 `grep -rn 'install-assets.sh' Makefile Dockerfile npm scripts .claude/skills/run-forebrain | wc -l` 相等（`scripts/install-dictionary.sh`、`scripts/install-assets.sh` 自身与 `scripts/build-dict-bundle.sh` 除外——后者只打词典包，不加）。

### Step 4：真机冒烟

```bash
TMPH=$(mktemp -d); FOREBRAIN_HOME=$TMPH ./build/bin/forebrain --version; ls $TMPH/models.json
```

`--version` 若不初始化目录，改用一个会走 `process` 启动路径的无交互命令（例如 `./build/bin/forebrain gateway --help` 不行就用 `forebrain mcp list` 一类；用 `./build/bin/forebrain --help` 找一个不需要模型的子命令）。目标：`$TMPH/models.json` 被种入且与 `assets/models.json` 字节相同（`cmp`）。
再把 `build/bin/models.json` 改名后重跑：应打印/记录 "models.json is missing beside the binary"，进程不崩溃。

**Verify**：上面两点成立；记录用了哪个子命令。

### Step 5：测试

1. 11 处 `llm.Parse(llm.EmbeddedModelsJSON())` 改为先 `data, err := llm.RepoModelsJSONForTest()`（`err != nil` → `t.Fatal`）再 `llm.Parse(data)`；保持原有的 `if ... err == nil {` 结构或改成 fatal，取与原测试意图一致者。
2. `pkg/llm/catalog_test.go` `TestInitGlobalCatalogSeedsAFreshHome`：先 `t.Setenv(AssetsDirEnv, <RepoAssetsDirForTest()>)`，调用改为 `InitGlobalCatalog(home, "(devel)")`。
3. 跑全量测试。凡因 `Seed`/`process.Open` 拿不到目录而失败的包，在该包加（或扩充已有的）`TestMain`：`dir, err := llm.RepoAssetsDirForTest(); os.Setenv(llm.AssetsDirEnv, dir)`。**不要**在生产代码里为测试加回退。
4. 新增 Test plan 的测试。

**Verify**：Commands 表"全量测试"全绿。

## Test plan

新建 `pkg/llm/assets_test.go`（照 `pkg/llm/catalog_test.go` 的写法；有全局目录的测试记得用 `SetGlobalCatalogForTest` 还原）：

1. `TestInstallSeedsModelsJSONFromAssetsDir`：`t.Setenv(AssetsDirEnv, src)`，`src/models.json` 写一份最小合法目录（从 `catalog_test.go` 的样例取），`Install(home, "(devel)")` 后 `home/models.json` 与源字节相同。
2. `TestInstallNeverOverwritesExistingModelsJSON`：目标先写 `{"x":{}}`，`Install` 后内容不变。
3. `TestInstallWithoutAssetsOnDevBuildFails`：assets 目录为空、版本 `(devel)`，返回错误含 `install-assets.sh`，不发网络请求（URL 变量指向一个计数的 `httptest` 服务器，断言计数为 0）。
4. `TestInstallDownloadsModelsJSONForReleaseVersion`：assets 目录为空、版本 `v1.2.3`，`httptest` 服务器在 `/v1.2.3/assets/models.json` 返回合法目录 → 写入成功；另一用例返回非 JSON → 返回错误且目标文件不存在。
5. `TestInstallAssetsCopiesEntries`：`InstallAssets(src, bin)` 后 `bin/models.json` 存在；源缺 `models.json` 返回错误。

## Done criteria

- [ ] Commands 表全部通过
- [ ] `grep -rn 'go:embed' pkg/llm` 与 `grep -rn 'EmbeddedModelsJSON\|embeddedModelsJSON\|model_assets' pkg cmd scripts Makefile Dockerfile npm` 均无输出
- [ ] `test -f assets/models.json && test ! -e pkg/llm/model_assets`
- [ ] Step 4 冒烟两点成立
- [ ] Test plan 5 个测试存在且 PASS
- [ ] `git status --short` 中本计划的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符。
2. 有**生产代码**（非测试）读取 `EmbeddedModelsJSON` 或依赖目录在二进制里（例如某处在没有 home 的情况下解析嵌入目录）。
3. 某个测试包加了 `TestMain` 仍拿不到目录、或需要改生产代码才能通过。
4. 某个构建入口（脚本、Dockerfile）无法在不改 `.github/workflows/*` 的前提下装上 models.json。
5. 架构测试（`pkg/architecture`）因 `pkg/llm` 新增导入失败——说明误导入了上层包。

## Maintenance notes

- 二进制旁的资源目录现在有三样：`dict/`（词典）、`models.json`、以及 Plan 041 加的 `tokenizers/`。新增此类文件一律加进 `assetEntries` 并走 `install-assets.sh`，不要再用 `go:embed`（owner 2026-10-11：禁止嵌入二进制）。
- 更新内置模型目录：改 `assets/models.json`。已有用户的 `$FOREBRAIN_HOME/models.json` 不会被覆盖（与改动前相同）。
- `FOREBRAIN_ASSETS_DIR` 也方便从源码 `go run ./cmd/forebrain` 调试：`FOREBRAIN_ASSETS_DIR=$PWD/assets go run ...`。
- 评审重点：每个构建入口都装了 models.json；Docker 最终镜像里有 `/app/models.json`；`Install` 的原子写与"永不覆盖"。
