# Plan 040：所有平台强制链接 HuggingFace tokenizers 静态库（daulet/tokenizers v1.27.0），建 `pkg/llm/hftok` 包装

> **执行者须知**：按步骤顺序执行，每步跑验证命令并确认预期结果后再进下一步。触发
> "STOP conditions" 时停下报告，不要即兴发挥。**不要更新 `plans/README.md`**（评审方维护索引）。
> **禁止提交代码**（owner 手动提交）：不建分支、不 commit、不 push、不 stash；改动留在工作区。
>
> **Drift check（先跑）**：`git diff --stat 005c1a8 -- go.mod go.sum Makefile Dockerfile .gitignore .github/workflows npm/scripts/build-platform-packages.sh scripts/check-go-install.sh scripts/acceptance .claude/skills/run-forebrain cmd/forebrain/main.go README.md CONTRIBUTING.md NOTICE`
> 若 Plan 039 已执行，Makefile/Dockerfile/README/CONTRIBUTING/npm 脚本/acceptance 脚本会有它加的 `install-assets.sh` 行——这是预期的，其余不符 → STOP。

## Status

- **Priority**: P1
- **Effort**: L
- **Risk**: HIGH（所有构建入口多一个原生静态库依赖；Windows 要在 CI 里用 Rust 从源码编；`go install` 不再开箱即用）
- **Depends on**: 无（Plan 041 依赖本计划）
- **Category**: dependency / build
- **Planned at**: commit `005c1a8` + 主树未提交改动（不涉及本计划文件），2026-10-11

## Why this matters

本地 token 估算目前只有 tiktoken cl100k（所有模型都用它）和一个自研的 tokenizer.json 解析器 `pkg/llm/hftokenizer.go`（只支持 BPE/ByteLevel 子集、进程全局一个、手工配路径）。owner 要求删掉自研解析器，换成成熟方案：
用 [daulet/tokenizers](https://github.com/daulet/tokenizers)（MIT，HuggingFace Rust `tokenizers` 的 Go 绑定，cgo + 静态库 `libtokenizers.a`），它能加载任何 `tokenizer.json`，也能加载 tiktoken 模型文件（Kimi 用）。
owner 2026-10-11 拍板：**所有平台强制链接**——Windows 在 CI 里用 Rust 从源码编静态库；`go install` 不再开箱即用（用户需先准备静态库并设 `CGO_LDFLAGS`），`release.yml` 的 go-install 任务与 `scripts/check-go-install.sh` 相应修改。
本计划只做"把库稳定地链进每一个构建 + 一个可测的 Go 包装"，不改估算逻辑（那是 Plan 041）。

## Current state（全部已核实）

- 仓库所有构建都要求 CGO（`Makefile:40-43`，`npm/scripts/build-platform-packages.sh:113-140`），已有 cgo 依赖（sqlite、silk）。发布 5 个目标：darwin/arm64、darwin/amd64（在 arm64 的 macos-14 上用 `CC="clang -arch x86_64"` 交叉编）、linux/amd64（ubuntu-22.04）、linux/arm64（ubuntu-22.04-arm）、windows/amd64（windows-latest，mingw gcc）。
- daulet/tokenizers 事实（2026-10-11 核实）：
  - 最新 tag `v1.27.0`。其 Go 源码 `tokenizer.go` 的 cgo 指令：

```go
/*
#cgo windows LDFLAGS: -ltokenizers -lm -lstdc++ -lws2_32 -luserenv
#cgo linux LDFLAGS: -ltokenizers -ldl -lm -lstdc++
#cgo darwin LDFLAGS: -ltokenizers -ldl -lm -lstdc++
#include <stdlib.h>
#include "tokenizers.h"
extern void tokenizers_version_1_26_0(void);
void (*tokenizers_version_check)(void) = &tokenizers_version_1_26_0;
*/
```

    即链接时要求静态库导出 `tokenizers_version_1_26_0`（Go 模块 v1.27.0 对应的 Rust crate 版本仍是 1.26.0）。
  - 预编译包（`https://github.com/daulet/tokenizers/releases/download/v1.27.0/<asset>`），sha256 与导出符号已实测：

| GOOS/GOARCH | asset | sha256 | 导出版本符号 |
|---|---|---|---|
| darwin/arm64 | `libtokenizers.darwin-arm64.tar.gz` | `fb84b8b2e349a5952767ffe80ccd862fc44084de47f3b0cc3f0b7c9d4e649cf7` | `tokenizers_version_1_26_0` |
| darwin/amd64 | `libtokenizers.darwin-x86_64.tar.gz` | `6239efe5a81fde8089ef2df8ae710366542b4e5deab6d8ecb74d7d1862db2ddb` | 同上 |
| linux/amd64 | `libtokenizers.linux-amd64.tar.gz` | `72556cdca798dd4ea7cdaba308e5f0d68a8cb93b67c96edf485b7a0edd7b07f4` | 同上 |
| linux/arm64 | `libtokenizers.linux-arm64.tar.gz` | `e96545ad05930c26f51f63d932ee6d3bbd32bbed149e102c5290d587a2293067` | 同上 |

    每个 tar 包里只有一个 `libtokenizers.a`（约 39–50 MB）。Linux 包由 `cross` 在老 glibc 镜像里编，ubuntu-22.04 可链。
  - **没有 Windows 预编译包**。daulet 的 `Makefile` / `.github/workflows/release.yml` 的编法：`cargo build --release -p tokenizers-ffi --target <triple>`，产物 `target/<triple>/release/libtokenizers_ffi.a`，改名为 `libtokenizers.a`。仓库带 `Cargo.lock`（用 `--locked`）。Windows 用 `x86_64-pc-windows-gnu`（与 Go 的 mingw 工具链一致）。
  - Go API（v1.27.0）：`FromFile(path)`、`FromBytes(data, opts...)`、`FromTiktoken(modelPath, configPath, pattern)`、`(*Tokenizer).EncodeWithOptions(str, addSpecialTokens, opts...) Encoding`（`Encoding.IDs []uint32`）、`Close()`。
    编码把 Go 字符串经 `C.CString` 传入，**遇到 `\x00` 会截断**。Rust 侧 `tokenizers_encode` 用 `ptr.cast::<UnifiedTokenizer>().as_ref()` 取共享引用，HF `Tokenizer::encode` 与 tiktoken-rs 都是 `&self`——同一个实例可并发编码（`Close` 不能与编码并发）。
- 可行性已在本机（darwin/arm64）用独立模块验证：在包装包里写 `#cgo darwin,arm64 LDFLAGS: -L${SRCDIR}/<相对路径>/third_party/libtokenizers/darwin_arm64`，不设任何环境变量即可 `go build` 链接成功；
  DeepSeek V4 / GLM-5.3 / gpt-oss / Qwen3.8 / MiniMax-M2 / MiMo-V2.5 / Gemma-4 / Claude 旧版 / Grok-2 转换版 tokenizer.json 与 Kimi tiktoken 模型都能加载计数；1.5 MB 文本约 0.8 s（tiktoken-go cl100k 同文本 0.6 s）。
  链接时 macOS 打印若干条 `ld: warning: object file (...libtokenizers.a[...]) was built for newer 'macOS' version (15.5) than being linked (15.0)`——只是警告。
- `.gitignore` 已忽略 `build`、`dist` 等；`third_party/` 目前只有 `silk/`（已提交）。CI 的 gofmt 检查包含 `third_party`（只检查 `.go`，`.a` 不受影响）。
- 构建/测试入口（每一处都要能拿到库）：`Makefile`（`build`、`test`）、`.github/workflows/ci.yml`（`Go mod vet build test` ubuntu 任务、`windows-tests`、`go-install`、`windows`、`macos` 含 darwin/amd64 交叉编）、
  `.github/workflows/release.yml`（`build` 矩阵 5 目标，均经 `npm/scripts/build-platform-packages.sh`；`go-install` 任务 `:213-232`）、`Dockerfile`（`gobuild` 阶段 `golang:1.27-bookworm`，`ARG TARGETARCH`）、
  `scripts/check-go-install.sh`、`scripts/acceptance/{driver,live_subagent,web_e2e}.sh`（各自 `go build`）、`.claude/skills/run-forebrain/driver.sh:~36`（`go build`）、文档 `README.md`（Requirements、`:82-89` go install 段落、`:205-250` 源码构建）、`CONTRIBUTING.md:25-45`。
- 分层：新包放 `pkg/llm/hftok`（属于 `llm` 顶层目录，第 0 层）；它**只能**导入标准库与 `github.com/daulet/tokenizers`，不得导入任何 `pkg/...`。
- 仓库已有测试数据 `pkg/llm/testdata/deepseek_v3_tokenizer.json`（7.8 MB，DeepSeek V3 的 tokenizer.json），本计划的测试用它。
- `go.work` 存在（`use .`）：`go get` 在工作区模式下可能报错，用 `GOWORK=off`。

## Commands you will need

| 用途 | 命令 | 成功预期 |
|---|---|---|
| 取库 | `scripts/fetch-libtokenizers.sh` | `third_party/libtokenizers/<goos>_<goarch>/libtokenizers.a` 存在；再跑一次秒退（已是该版本） |
| 包装测试 | `CGO_ENABLED=1 go test -race ./pkg/llm/hftok/ -count=1 -v` | 全部 PASS |
| 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 全部 `ok` |
| 构建 | `make build` | 成功；`build/bin/forebrain --version` 正常 |
| 交叉编（mac） | `scripts/fetch-libtokenizers.sh darwin/amd64 && CGO_ENABLED=1 GOARCH=amd64 CC="clang -arch x86_64" go build -tags fts5 -o /dev/null ./cmd/forebrain` | 成功 |
| vet / fmt | `CGO_ENABLED=1 go vet -tags fts5 ./...`；`gofmt -l cmd pkg third_party` | 无输出 |
| 包图 | `scripts/package-graph.sh` | 重新生成 graph.json 并连同提交 |
| workflow lint | 仓库有 `.github/workflows/lint-workflows.yml`，本地若有 `actionlint` 就跑 `actionlint`；没有则仔细比对 YAML 缩进 | 无错误 |

## Scope

**In scope**：
- 新建 `scripts/fetch-libtokenizers.sh`、`pkg/llm/hftok/{link.go,hftok.go,hftok_test.go}`
- `go.mod` / `go.sum`（加 `github.com/daulet/tokenizers v1.27.0`）
- `.gitignore`（加 `third_party/libtokenizers/`）
- `cmd/forebrain/main.go`（加一个空白导入，见 Step 3）
- `Makefile`、`Dockerfile`、`.github/workflows/ci.yml`、`.github/workflows/release.yml`、`npm/scripts/build-platform-packages.sh`、`scripts/check-go-install.sh`、`scripts/acceptance/{driver,live_subagent,web_e2e}.sh`、`.claude/skills/run-forebrain/{driver.sh,SKILL.md}`
- `README.md`、`CONTRIBUTING.md`、`NOTICE`
- `pkg/architecture/testdata/graph.json`（脚本重新生成）

**Out of scope**：
- `pkg/llm/tokestimate.go`、`pkg/llm/hftokenizer.go`、`pkg/config/*`：估算逻辑与删除旧解析器是 Plan 041。
- 把预编译 `.a` 提交进 git：**不提交**（gitignore），由脚本按固定版本 + sha256 拉取或现编。
- 自己编 darwin/linux 静态库：用官方预编译包；只有 Windows 现编。

## Git workflow

不建分支、不 commit、不 push、不 stash。

## Steps

### Step 1：取库脚本

新建 `scripts/fetch-libtokenizers.sh`（bash，`set -euo pipefail`，文件头注释写清用途、用法、产物位置、为什么 Windows 现编），`chmod +x`：

- 用法：`scripts/fetch-libtokenizers.sh [goos/goarch ...]`，无参数时取宿主 `$(go env GOOS)/$(go env GOARCH)`。
- 版本：从 `go.mod` 读 `github.com/daulet/tokenizers` 的版本（`awk '$1=="github.com/daulet/tokenizers"{print $2}' "$ROOT_DIR/go.mod"`），读不到 → 报错退出。
- 产物：`$ROOT_DIR/third_party/libtokenizers/<goos>_<goarch>/libtokenizers.a`，旁边写 `.version`（内容为版本号）；若 `.version` 已等于当前版本且 `.a` 存在 → 直接成功返回（幂等，CI 缓存命中时也走这里）。
- darwin/linux：按上表下载对应 asset 到临时目录（`curl -fsSL --retry 3`），用 `sha256sum` 或 `shasum -a 256`（哪个存在用哪个）校验**脚本内写死**的 sha256（以 "版本 + asset" 为键；当前版本不在表里 → 报错退出，提示更新表），解压出 `libtokenizers.a` 原子移动到产物位置。
- windows/amd64：需要 `git`、`cargo`；若有 `rustup` 先 `rustup target add x86_64-pc-windows-gnu`。`git clone --depth 1 --branch <版本> https://github.com/daulet/tokenizers` 到临时目录，`cargo build --release --locked -p tokenizers-ffi --target x86_64-pc-windows-gnu`，
  复制 `target/x86_64-pc-windows-gnu/release/libtokenizers_ffi.a` 为产物。在 Windows（Git Bash）上编时，若 cc 找不到 C/C++ 编译器，设 `CC_x86_64_pc_windows_gnu=gcc`、`CXX_x86_64_pc_windows_gnu=g++` 再试。
- 所有目标：若 `nm` 可用，确认产物导出 `tokenizers_version_` + Go 模块 `tokenizer.go` 里要求的版本号（从 `$(go env GOMODCACHE)/github.com/daulet/tokenizers@<版本>/tokenizer.go` 的 `extern void tokenizers_version_...` 一行取），不符 → 报错退出。
- `.gitignore` 加 `third_party/libtokenizers/`。

**Verify**：`scripts/fetch-libtokenizers.sh && ls -la third_party/libtokenizers/*/libtokenizers.a`；再跑一次立即成功；`git status --short third_party` 无输出。

### Step 2：加依赖与包装包

1. `GOWORK=off go get github.com/daulet/tokenizers@v1.27.0`（工作区模式不报错时也可不加 `GOWORK=off`）。
2. `pkg/llm/hftok/link.go`：只放 cgo 链接指令（英文注释说明：库由 `scripts/fetch-libtokenizers.sh` 放到仓库内固定位置，`${SRCDIR}` 让任何 `go build`/`go test` 无需环境变量就能找到；`go install` 从模块缓存构建时找不到，需要用户设 `CGO_LDFLAGS=-L<dir>`）：

```go
package hftok

/*
#cgo darwin,arm64 LDFLAGS: -L${SRCDIR}/../../../third_party/libtokenizers/darwin_arm64
#cgo darwin,amd64 LDFLAGS: -L${SRCDIR}/../../../third_party/libtokenizers/darwin_amd64
#cgo linux,amd64 LDFLAGS: -L${SRCDIR}/../../../third_party/libtokenizers/linux_amd64
#cgo linux,arm64 LDFLAGS: -L${SRCDIR}/../../../third_party/libtokenizers/linux_arm64
#cgo windows,amd64 LDFLAGS: -L${SRCDIR}/../../../third_party/libtokenizers/windows_amd64
*/
import "C"
```

3. `pkg/llm/hftok/hftok.go`：

```go
// Tokenizer counts tokens with a HuggingFace tokenizer or a tiktoken model
// through the tokenizers library. Count is safe for concurrent use; Close is not
// and must not race with Count.
type Tokenizer struct{ tk *tokenizers.Tokenizer }

func FromFile(path string) (*Tokenizer, error)
func FromBytes(data []byte) (*Tokenizer, error)
func FromTiktoken(modelPath, configPath, pattern string) (*Tokenizer, error)
func (t *Tokenizer) Count(text string) int
func (t *Tokenizer) Close() error
```

   `Count`：空串返回 0；用 `EncodeWithOptions(text, false)` 取 `len(IDs)`（不要用返回 token 字符串的 `Encode`，省分配）；文本含 `\x00` 时按 `\x00` 切段分别计数再加上分隔符个数（避免 `C.CString` 截断）。`nil` 接收者返回 0。
4. `cmd/forebrain/main.go` 加空白导入 `_ "github.com/forebrain-harness/forebrain-harness/pkg/llm/hftok"`，注释："Links the tokenizers library into every forebrain binary; plan 041 replaces this with pkg/llm's own import."——这样本计划里每个发布构建都真正链接这个库，CI 能验证。

**Verify**：`CGO_ENABLED=1 go build -tags fts5 ./cmd/forebrain` 成功；`go vet ./pkg/llm/hftok/` 无输出。

### Step 3：本地构建入口

1. `Makefile`：新增 `libtokenizers:` 目标运行 `scripts/fetch-libtokenizers.sh`；`build` 依赖改为 `build: ui libtokenizers`，`test` 依赖 `libtokenizers`；`.PHONY` 加上；文件头说明加一行。
2. `npm/scripts/build-platform-packages.sh`：`build_target` 里 `go build` 之前调用 `"$ROOT_DIR/scripts/fetch-libtokenizers.sh" "$target"`；文件头注释说明交叉编 windows 需要 Rust 的 `x86_64-pc-windows-gnu` 目标。
3. `scripts/acceptance/driver.sh`、`live_subagent.sh`、`web_e2e.sh`、`.claude/skills/run-forebrain/driver.sh`：各自 `go build` 之前加 `"$REPO/scripts/fetch-libtokenizers.sh"`；`SKILL.md:121` 的命令行前面加 `scripts/fetch-libtokenizers.sh &&`。
4. `Dockerfile` 的 `gobuild` 阶段，`COPY . .` 之后、`go build` 之前：`RUN scripts/fetch-libtokenizers.sh linux/${TARGETARCH}`（bookworm 镜像自带 curl、tar；缺什么在同一 `apt-get install` 行补上）。

**Verify**：`make build` 成功；`FOREBRAIN_SKIP_UI_BUILD=1 npm/scripts/build-platform-packages.sh` 成功；有 Docker 就跑 `make docker`（没有则在报告里写明未验证）。

### Step 4：CI 与发布

1. `.github/workflows/ci.yml`：
   - `Go mod vet build test`（ubuntu）：`go vet` 之前加 `- run: scripts/fetch-libtokenizers.sh`。
   - `macos`：加 `- run: scripts/fetch-libtokenizers.sh darwin/arm64 darwin/amd64`。
   - `windows` 与 `windows-tests`：加 `actions/cache@v4`（`path: third_party/libtokenizers/windows_amd64`，`key: libtokenizers-windows-amd64-${{ hashFiles('go.mod') }}`），再 `- run: rustup target add x86_64-pc-windows-gnu`（`shell: bash`），再 `- run: scripts/fetch-libtokenizers.sh windows/amd64`（`shell: bash`）。
   - `go-install`：不改 YAML（由 `check-go-install.sh` 处理，见 3）。
2. `.github/workflows/release.yml`：
   - `build` 矩阵：库由 `build-platform-packages.sh` 取；只需对 Windows 加同样的 cache 与 `rustup target add` 两步（`if: runner.os == 'Windows'`），放在 "Build the platform package" 之前。
   - `go-install` 任务（`:213-232`）：在 `go install` 之前从仓库检出里跑 `scripts/fetch-libtokenizers.sh` 并把 `CGO_LDFLAGS="-L$GITHUB_WORKSPACE/third_party/libtokenizers/linux_amd64"` 传给 `go install`（它已 `actions/checkout` 了该 tag）。
3. `scripts/check-go-install.sh`：在 `go install` 之前 `"$ROOT_DIR/scripts/fetch-libtokenizers.sh"`，并在 `env ... go install` 那行加 `CGO_LDFLAGS="-L$ROOT_DIR/third_party/libtokenizers/$(go env GOOS)_$(go env GOARCH)"`；文件头注释补一句"the tokenizers static library is not part of the module, so a user's go install needs CGO_LDFLAGS pointing at it"。

**Verify**：`scripts/check-go-install.sh` 本地通过（需要网络访问模块代理）；YAML 通过 workflow lint（或人工核对缩进）。

### Step 5：文档与 NOTICE

1. `README.md`：
   - `go install` 段落（`:82-89`）改为：先取静态库（给出从 GitHub release 下载对应平台 `libtokenizers.<os>-<arch>.tar.gz`、解压到某目录的说明，Windows 需用 Rust 自编——指向 `scripts/fetch-libtokenizers.sh`），再 `CGO_ENABLED=1 CGO_LDFLAGS="-L<dir>" go install -tags fts5 ...@latest`。
   - Requirements 一句加"首次构建会运行 `scripts/fetch-libtokenizers.sh` 下载 tokenizers 静态库（Windows 需要 Rust）"。
   - 源码构建代码块（`:217-220`）在 `go build` 前加 `scripts/fetch-libtokenizers.sh`。
2. `CONTRIBUTING.md:30-37` 同样补 `scripts/fetch-libtokenizers.sh`。
3. `NOTICE`：追加一段说明二进制静态链接了 daulet/tokenizers（MIT）及其 Rust 依赖 HuggingFace tokenizers（Apache-2.0）、tiktoken-rs（MIT）、Oniguruma（BSD-2-Clause）等，附项目链接。

**Verify**：`grep -n 'fetch-libtokenizers' README.md CONTRIBUTING.md` 有输出；`grep -n 'daulet/tokenizers' NOTICE` 有输出。

## Test plan

`pkg/llm/hftok/hftok_test.go`（`package hftok`，用 `testing`；测试数据 `../testdata/deepseek_v3_tokenizer.json`）：

1. `TestFromFileCountsDeepSeekV3`：`Count("Hello, world! 你好，世界！")` 大于 0 且小于字符数；`Count("")==0`。
2. `TestFromBytesMatchesFromFile`：同一文件两种加载方式对同一段中英混合 + 代码文本计数相等。
3. `TestCountSurvivesNulBytes`：`Count("a\x00b")` 等于 `Count("a")+Count("b")+1`，且不等于 `Count("a")`。
4. `TestCountIsSafeConcurrently`：8 个 goroutine 各 50 次对同一实例 `Count` 不同文本，结果与串行一致（以 `-race` 运行）。
5. `TestFromFileRejectsGarbage`：临时文件写 `not json`，返回错误不 panic。
6. `TestNilTokenizerCountsZero`。

## Done criteria

- [ ] Commands 表全部通过（交叉编那一行仅在 macOS 上要求）
- [ ] Test plan 6 个测试存在且在 `-race` 下 PASS
- [ ] `grep -n 'daulet/tokenizers v1.27.0' go.mod` 有输出；`git status --short third_party` 无输出（库未进 git）
- [ ] `grep -rn 'fetch-libtokenizers' Makefile Dockerfile .github/workflows npm scripts .claude/skills/run-forebrain` 覆盖 Current state 列出的每个入口
- [ ] `git status --short` 中本计划的改动只涉及 Scope 列出的文件

## STOP conditions

1. Drift check 不符（039 的 `install-assets.sh` 行除外）。
2. 任一预编译包 sha256 与上表不符，或导出符号不是 `tokenizers_version_1_26_0`（上游重发了包）。
3. Windows 静态库在 CI 里 `cargo build` 失败，或链接时报未定义符号：先用 `cargo rustc --release --locked -p tokenizers-ffi --target x86_64-pc-windows-gnu --crate-type staticlib -- --print native-static-libs` 拿到所需系统库，加到 `link.go` 的 `#cgo windows,amd64 LDFLAGS`（例如 `-lbcrypt -lntdll -ladvapi32 -lkernel32`）再试；仍失败两次 → STOP 报告完整错误。
4. `${SRCDIR}` 方案在某个入口（Docker、npm 交叉编、Windows）链接失败且无法用脚本解决。
5. 链接后发布构建的冒烟测试（`release.yml` 的 `--version`）在 macos-14 上失败（可能是静态库要求的最低 macOS 版本高于运行器）。
6. 加入库后 `go test ./...` 普遍变慢超过一倍，或二进制体积增加超过 60 MB。

## Maintenance notes

- 升级 daulet/tokenizers：改 `go.mod` 版本 → 在 `fetch-libtokenizers.sh` 的 sha256 表里加新版本四行（自己下载核对）→ 确认新版本 `tokenizer.go` 要求的版本符号与预编译包一致 → Windows 缓存键随 `go.mod` 变化自动失效。
- macOS 链接警告（静态库按 macOS 15.5 编译）目前无害；若有用户在较老的 macOS 上报崩溃，改为在 CI 里用 `MACOSX_DEPLOYMENT_TARGET` 从源码编 darwin 库（与 Windows 同法）。
- `go install` 用户必须自备静态库并设 `CGO_LDFLAGS`（owner 已接受）。
- 评审重点：sha256 表与版本来源（go.mod）一致；每个构建入口都在 `go build`/`go test` 前取库；Windows 缓存与 `rustup target add`；`Count` 的 NUL 处理与并发安全。
