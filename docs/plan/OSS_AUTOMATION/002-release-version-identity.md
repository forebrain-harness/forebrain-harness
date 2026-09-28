# Plan 002：统一版本号——VERSION 改为三段 semver，二进制在任何安装方式下都报告正确版本

> **执行者须知**：逐步执行，每一步都要跑"验证"命令并确认结果与预期一致后才进入下一步。
> 出现"STOP 条件"中的任何一种情况，立即停止并汇报，不要自行发挥。完成后把
> `docs/plan/OSS_AUTOMATION/README.md` 里本计划的状态改成 DONE。
>
> **提交（里程碑 M2）**：本地步骤全部通过后，按 `README.md` 全局规则 1 提交**一次**：标题见 README 的
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
| 优先级 | P1 |
| 工作量 | S |
| 风险 | LOW |
| 依赖 | 001（`scripts/check-go-install.sh` 在本计划里被扩展） |
| 类别 | migration / dx |
| 编写于 | 2026-09-28，基于未提交的工作区 |
| owner 决策 | **D1**（首个版本号与 0.x 策略）必须先确认，见 README；本计划按推荐值 `0.x` 编写 |

## 为什么要做

现在同一个项目有四个互相矛盾的版本号：`VERSION` 是 `1.0.0.0`（四段，npm 和 Go 都不认），
`pkg/home/version.go` 的默认值是 `v0.0.1`，Dockerfile 的默认值是 `v0.0.1`，`npm/package.json` 是
`0.1.0`。自动发版（release-please）和 npm 发布都要求唯一、合法的三段 semver。

更关键的是 `go install …@vX.Y.Z` 不会带 `-ldflags`，这样装出来的二进制会一直报告源码里写死的默认值。
而计划 005（词典下载）要靠版本号找到对应 Release 的下载地址，版本不对就会下载错文件。根因修法是让
二进制按下面的优先级取版本：链接时注入的值 → go 命令记录在二进制里的模块版本（`debug.ReadBuildInfo`）
→ `(devel)`。

## 现状

- `VERSION`：`1.0.0.0`
- `pkg/home/version.go`（全文）：
  ```go
  package home

  // Version is embedded at build time via:
  // go build -ldflags="-X github.com/forebrain-harness/forebrain-harness/pkg/home.Version=v0.0.1"
  var Version = "v0.0.1"
  ```
- 读取方：`cmd/forebrain/root.go:33`（`rootCmd.Version = home.Version`，`--version` 打印它）、
  `cmd/forebrain/interactive.go:125`（TUI banner）、`pkg/tui/chat_slash.go:111`（/status）。
- `Makefile`：
  ```make
  VERSION      := $(shell cat VERSION)
  LDFLAGS      := -s -w -X github.com/forebrain-harness/forebrain-harness/pkg/home.Version=$(VERSION)
  ...
  docker:
  	docker build -t forebrain:$(VERSION) .
  ...
  release:
  	FOREBRAIN_VERSION=$(VERSION) npm/scripts/build-platform-packages.sh
  ```
  注意：这里注入的值没有 `v` 前缀，与 `version.go` 的 `v0.0.1` 风格不一致。
- `Dockerfile:14`：`ARG FOREBRAIN_VERSION=v0.0.1`，第 24 行用它做 `-X …Version=${FOREBRAIN_VERSION}`。
- `npm/scripts/build-platform-packages.sh`：`FOREBRAIN_VERSION="${FOREBRAIN_VERSION:-0.1.0}"`，
  既用于 package.json 的 `"version"`，也用于 ldflags（没有 `v`）。
- `npm/package.json`：`"version": "0.1.0"`，`optionalDependencies` 里五个平台包也都是 `"0.1.0"`。
- 约定：Go 模块 tag 必须形如 `vX.Y.Z`；npm 版本形如 `X.Y.Z`。本项目统一规定
  **`home.Version` 一律带 `v`（与 git tag 相同），`VERSION` 文件与 npm 版本一律不带 `v`**。

## 需要的命令

| 用途 | 命令 | 成功时 |
| --- | --- | --- |
| home 测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/home -count=1` | ok |
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| vet | `go vet ./...` | exit 0 |
| 架构测试 | `CGO_ENABLED=1 go test -tags fts5 ./pkg/architecture -count=1` | ok |
| 包图 | `scripts/package-graph.sh` | 生成新的 `pkg/architecture/testdata/graph.json`（loc 变化属预期） |
| go install 模拟 | `scripts/check-go-install.sh` | exit 0 |

## 范围

**允许修改**：`VERSION`、`pkg/home/version.go`、`pkg/home/version_test.go`（新建）、`Makefile`、
`Dockerfile`、`npm/package.json`、`npm/scripts/build-platform-packages.sh`、
`scripts/check-release-versions.sh`（新建）、`scripts/check-go-install.sh`、
`.github/workflows/ci.yml`、`pkg/architecture/testdata/graph.json`（由脚本重新生成）。

**不许碰**：`pkg/home` 里除 `version.go` 以外的文件；任何读 `home.Version` 的调用方（它们的行为
不变，只是拿到的值变准了）。

## 步骤

### 步骤 1：VERSION 与 npm 版本归零到"尚未发布"

- `VERSION` 内容改为 `0.0.0`（单行，末尾换行）。
- `npm/package.json` 的 `"version"` 和 `optionalDependencies` 下五个值全部改为 `"0.0.0"`。

理由：首个正式版本由 release-please 生成（计划 008）。按 D1 的推荐，首发版本号通过提交信息里的
`Release-As: 0.1.0` 指定。在那之前，仓库如实表示"还没发布过"。

**验证**：`cat VERSION` → `0.0.0`；`jq -r '.version, (.optionalDependencies[])' npm/package.json | sort -u`
→ 只有一行 `0.0.0`

### 步骤 2：`pkg/home/version.go` 按优先级解析版本

整个文件替换为下面的形态（注释英文，保持项目"注释解释为什么"的风格）：

```go
package home

import "runtime/debug"

// Version is this build's version, spelled like its git tag ("v0.3.1").
//
// Release builds (make build, the npm packages, the container image) set it at
// link time with -ldflags "-X .../pkg/home.Version=vX.Y.Z". A `go install
// module/cmd/forebrain@vX.Y.Z` build has no link flags, so the version is the
// module version the go command recorded in the binary. A build from a
// checkout gets whatever the go command stamped for it — a pseudo-version, or
// "(devel)".
var Version = ""

func init() {
	info, ok := debug.ReadBuildInfo()
	Version = resolveVersion(Version, info, ok)
}

// resolveVersion prefers the link-time version, then the recorded module
// version, and reports "(devel)" when neither exists.
func resolveVersion(linked string, info *debug.BuildInfo, ok bool) string {
	if linked != "" {
		return linked
	}
	if ok && info != nil && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
```

注意：`-X` 只能改写"初始值是字符串常量"的包级变量，`var Version = ""` 满足这个条件。

**验证**：`CGO_ENABLED=1 go build -tags fts5 -o $TMPDIR/fb ./cmd/forebrain && $TMPDIR/fb --version`
→ 打印 go 命令为本地构建记录的版本（Go 1.24+ 在 git 仓库里会记伪版本；仓库还没有提交时是
`(devel)`）。不应该是 `v0.0.1`。

### 步骤 3：为 `resolveVersion` 写单元测试

新建 `pkg/home/version_test.go`（测试文件名必须与被测文件对应，这是 `pkg/architecture` 的规则）。
用表驱动覆盖：

1. `linked="v1.2.3"`、info 带 `Main.Version="v9.9.9"` → `v1.2.3`（链接值优先）
2. `linked=""`、info `Main.Version="v0.4.0"` → `v0.4.0`（go install 场景）
3. `linked=""`、`ok=false` → `(devel)`
4. `linked=""`、info `Main.Version=""` → `(devel)`

参照 `pkg/home` 里已有的 `*_test.go` 的风格（`testing` 标准库，不引入新断言库）。

**验证**：`CGO_ENABLED=1 go test -tags fts5 ./pkg/home -run TestResolveVersion -count=1 -v` → 4 个子测试 PASS

### 步骤 4：统一构建入口的注入值（都带 `v`）

- `Makefile`：`LDFLAGS` 改为 `-X …/pkg/home.Version=v$(VERSION)`；`docker:` 目标改为
  `docker build --build-arg FOREBRAIN_VERSION=v$(VERSION) -t forebrain:$(VERSION) .`
- `Dockerfile:14`：`ARG FOREBRAIN_VERSION=`（默认空）。第 24 行不动：注入空串时 `Version` 回退到
  build info。这里没有 `.git`，所以结果是 `(devel)`，如实说明这不是发布构建。
- `npm/scripts/build-platform-packages.sh`：
  - 默认值改为从 VERSION 读：`FOREBRAIN_VERSION="${FOREBRAIN_VERSION:-$(tr -d '[:space:]' < "$ROOT_DIR/VERSION")}"`。
    这一行必须放在 `ROOT_DIR` 定义之后。
  - ldflags 改为 `-X …/pkg/home.Version=v$FOREBRAIN_VERSION`。package.json 的 `"version"` 仍然用
    不带 v 的 `$FOREBRAIN_VERSION`。
  - 更新脚本头部注释里 `FOREBRAIN_VERSION` 的说明（默认值来自 VERSION 文件）。

**验证**：
- `make -n build | grep -o 'Version=v0.0.0'` → `Version=v0.0.0`
- `grep -n 'Version=v\$FOREBRAIN_VERSION' npm/scripts/build-platform-packages.sh` → 1 行

### 步骤 5：新增 `scripts/check-release-versions.sh`，并接入 CI

脚本检查一个不变量：`VERSION` 是 `X.Y.Z`，npm launcher 的 `version` 和五个 `optionalDependencies`
的值都等于它，且 `optionalDependencies` 的五个包名与 `npm/scripts/build-platform-packages.sh` 里
`pkg_name_for` 产出的名字一致。任何一项不满足就用一句话报错，exit 1。实现用 `bash` + `jq`（CI 的
ubuntu 镜像自带 jq）。

```bash
#!/usr/bin/env bash
# Check that every place a release version is written agrees with VERSION.
# release-please rewrites them together (release-please-config.json); this
# catches a file it was not told about.
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
version="$(tr -d '[:space:]' < "$ROOT_DIR/VERSION")"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "VERSION must be X.Y.Z, found '$version'" >&2; exit 1; }
pkg="$ROOT_DIR/npm/package.json"
[ "$(jq -r .version "$pkg")" = "$version" ] || { echo "npm/package.json version is not $version" >&2; exit 1; }
want='["@forebrain-harness/forebrain-darwin-arm64","@forebrain-harness/forebrain-darwin-x64","@forebrain-harness/forebrain-linux-arm64","@forebrain-harness/forebrain-linux-x64","@forebrain-harness/forebrain-win32-x64"]'
[ "$(jq -c '.optionalDependencies | keys' "$pkg")" = "$want" ] || { echo "npm/package.json optionalDependencies must list exactly the five platform packages" >&2; exit 1; }
bad="$(jq -r --arg v "$version" '.optionalDependencies | to_entries[] | select(.value != $v) | .key' "$pkg")"
[ -z "$bad" ] || { echo "npm/package.json optionalDependencies not at $version: $bad" >&2; exit 1; }
echo "release versions agree: $version"
```

在 `ci.yml` 的 `go:` 任务里、`go mod verify` 之后加一步：`- run: scripts/check-release-versions.sh`。

**验证**：`scripts/check-release-versions.sh` → `release versions agree: 0.0.0`；临时把 npm/package.json
的某个 optionalDependency 改成 `0.0.1`，再跑一次 → exit 1 并打印一句话。改回来后再跑一次，确认恢复。

### 步骤 6：go install 模拟要验证版本

在 `scripts/check-go-install.sh` 里，把 `"$WORK/bin/forebrain" --version` 这一行改为比较输出：

```bash
got="$("$WORK/bin/forebrain" --version)"
[ "$got" = "$VERSION" ] || { echo "go-installed forebrain reports '$got', want '$VERSION'" >&2; exit 1; }
```

**验证**：`scripts/check-go-install.sh` → exit 0。这证明 go install 装出的二进制报告的就是它的模块版本。

### 步骤 7：包图与全量检查

运行 `scripts/package-graph.sh`（`pkg/home` 的 loc 变化会反映到 graph.json，属预期），再跑编译、vet、
架构测试。

**验证**：上表全部符合；`diff` 新旧 graph.json 只有 `pkg/home` 的 `loc` 数字变化（先
`cp pkg/architecture/testdata/graph.json $TMPDIR/graph.before`）

## 真实仓库实测（关卡 G1）

1. main 上 CI 的 `Go mod vet build test` job 成功，其中包含 `scripts/check-release-versions.sh` 这一步：
   `gh run view "$RUN" --repo forebrain-harness/forebrain-harness --log | grep 'release versions agree: 0.0.0'` 有输出。
2. 真实 go install 报告的版本与模块版本一致：
   ```bash
   SHA=$(git log --format=%H --grep '^build: report one semver version' -1)
   cd "$(mktemp -d)" && env -u GOFLAGS GOWORK=off CGO_ENABLED=1 GOBIN="$PWD/bin" \
  go install -tags fts5 github.com/forebrain-harness/forebrain-harness/cmd/forebrain@"$SHA"
   want=$(env -u GOFLAGS GOWORK=off go list -m -json github.com/forebrain-harness/forebrain-harness@"$SHA" | jq -r .Version)
   [ "$(./bin/forebrain --version)" = "$want" ] && echo version-ok
   ```
   → `version-ok`，这时报告的是伪版本，例如 `v0.0.0-2026…-<sha12>`。
3. 记录命令与输出。

## 完成标准

- [ ] M2 已按规范提交
- [ ] 真实仓库实测第 1–2 步通过并记录
- [ ] `cat VERSION` 为 `0.0.0`；`scripts/check-release-versions.sh` exit 0
- [ ] `grep -rn '"v0.0.1"\|=v0.0.1' pkg cmd Dockerfile Makefile npm` 无输出
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./pkg/home ./pkg/architecture -count=1` 通过
- [ ] `scripts/check-go-install.sh` exit 0（版本比对通过）
- [ ] 改动全部在允许清单内

## STOP 条件

- `debug.ReadBuildInfo()` 在 go install 模拟里给出的 `Main.Version` 不是 `v0.0.0-installcheck`。
- 有测试或代码依赖 `home.Version == "v0.0.1"` 这个具体值（先 `grep -rn "v0.0.1" --include='*_test.go' .`；
  有命中就 STOP，汇报命中位置）。
- D1 还没有答复，或者答复不是 0.x：这时步骤 1 的数值和 README 中的首发说明要按 owner 的答复改写，
  先汇报再执行。

## 维护说明

- 以后 release-please（计划 008）会同时改写 `VERSION` 和 `npm/package.json` 的六处版本；
  `check-release-versions.sh` 用来防止两边不一致。
- 新增平台包时，要同时改 `build-platform-packages.sh`、`npm/package.json`、
  `check-release-versions.sh` 里的 `want` 列表，以及计划 008 的 release-please extra-files。
