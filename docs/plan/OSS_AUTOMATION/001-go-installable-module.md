# Plan 001：让模块可以被 `go install …@version` 安装（并入 silk fork，去掉 replace）

> **执行者须知**：逐步执行，每一步都要跑"验证"命令并确认结果与预期一致后才进入下一步。
> 出现"STOP 条件"中的任何一种情况，立即停止并汇报，不要自行发挥。完成后把
> `docs/plan/OSS_AUTOMATION/README.md` 里本计划的状态改成 DONE。
>
> **提交（里程碑 M1）**：本地步骤全部通过后，按 `README.md` 全局规则 1 提交**一次**：标题见 README 的
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
| 优先级 | P1（go install 的硬阻塞） |
| 工作量 | S |
| 风险 | LOW |
| 依赖 | 无 |
| 类别 | migration / dx |
| 编写于 | 2026-09-28，基于未提交的工作区 |

## 为什么要做

owner 要求本项目支持 `go install` 安装。但根 `go.mod` 里有一条 `replace` 指令：只要目标模块的
go.mod 含 `replace` 或 `exclude`，`go install <module>/cmd/forebrain@<version>` 就会拒绝安装，报错
"The go.mod file for the module providing named packages contains one or more replace directives"。
另外，`replace` 指向的 `third_party/silk` 自带 `go.mod`，属于嵌套模块，本来就不会被打进主模块的发布
zip。所以即便没有这条规则，安装时也拿不到这份代码。

根因修法是把这份 fork 变成主模块里的普通包：删掉它的 go.mod，改导入路径，去掉 `require` 和
`replace`。本计划同时新增一个脚本和一个 CI 任务，模拟用户从模块代理执行 `go install` 的全过程，
防止以后再引入会破坏 go install 的改动。

## 现状

- `go.mod:35`：`github.com/youthlin/silk v0.0.4`（require 块里的一行）
- `go.mod:112`：`replace github.com/youthlin/silk v0.0.4 => ./third_party/silk`
- `third_party/silk/go.mod`：
  ```
  module github.com/youthlin/silk

  go 1.20
  ```
- `third_party/silk/doc.go`（package silk）导入它自己的内部包：
  ```go
  import (
  	"io"

  	"github.com/youthlin/silk/internal"
  )
  ```
- `third_party/silk/internal/` 下只有 `decode.go`、`encode.go` 两个 Go 文件，外加大量 `.c/.h/.S`
  文件（cgo 编译）。
- 唯一的外部使用方是 `pkg/channel/silk_cgo.go`：
  ```go
  //go:build cgo

  package channel

  import (
  	"bytes"

  	"github.com/youthlin/silk"
  )
  ```
- `third_party/silk/FORK.md` 描述了这份 fork 的原因（Windows 下 `C.malloc(C.ulong(size))` 编译失败，
  改成 `C.size_t`），并写着"通过 root go.mod 的 replace 引用"。
- `pkg/architecture` 的结构测试只扫描 `pkg/`、`cmd/`、`internal/`（见
  `pkg/architecture/cache_test.go:1273` 的 `goFilesUnder`），`third_party/` 不受 20 文件上限、doc.go
  等规则约束。`scripts/package-graph.sh` 只列 `./pkg/...`。
- `Dockerfile` 先 `COPY go.mod go.sum ./` 再 `RUN go mod download`，然后 `COPY . .`。并入之后
  `go mod download` 不再需要这份本地代码，行为不变。

## 需要的命令

| 用途 | 命令 | 成功时 |
| --- | --- | --- |
| 编译 | `CGO_ENABLED=1 go build -tags fts5 ./...` | exit 0 |
| vet | `go vet ./...` | exit 0，无输出 |
| 测试（channel） | `CGO_ENABLED=1 go test -tags fts5 ./pkg/channel -count=1` | ok |
| 全量测试 | `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` | 全部 ok（已知网络抖动用例 `TestCharacterizationNetworkApproval` 单独重跑通过即可） |
| 格式 | `gofmt -l cmd pkg third_party` | 无输出 |
| 模块校验 | `go mod verify` | `all modules verified` |

## 范围

**允许修改**：
- `go.mod`、`go.sum`
- `third_party/silk/go.mod`（删除）
- `third_party/silk/doc.go`（只改 import 行）
- `third_party/silk/FORK.md`
- `pkg/channel/silk_cgo.go`（只改 import 行）
- `scripts/check-go-install.sh`（新建）
- `.github/workflows/ci.yml`（新增一个 job）

**不许碰**：
- `third_party/silk/internal/*` 的任何 C/Go 源码：这是上游代码，本计划只改模块边界。
- `pkg/channel/silk_nocgo.go`：无 cgo 的 stub，与本计划无关。
- 其他任何 `replace`/依赖升级：本计划只处理 silk 这一条。

## Git 工作流

在 `main` 上工作，本地步骤全部通过后按 README 全局规则 1 做里程碑提交 M1；推送按关卡 G1 进行。

## 步骤

### 步骤 1：删除嵌套模块文件

删除 `third_party/silk/go.mod`。`third_party/silk/.gitignore` 是上游带来的，保留不动。

**验证**：`test ! -e third_party/silk/go.mod && echo gone` → 输出 `gone`

### 步骤 2：改写两处导入路径

- `third_party/silk/doc.go`：`"github.com/youthlin/silk/internal"` →
  `"github.com/forebrain-harness/forebrain-harness/third_party/silk/internal"`
- `pkg/channel/silk_cgo.go`：`"github.com/youthlin/silk"` →
  `"github.com/forebrain-harness/forebrain-harness/third_party/silk"`

包名不变（`silk` / `internal`），调用处代码不用改。

**验证**：`grep -rn "youthlin/silk" --include='*.go' .` → 无输出

### 步骤 3：去掉 require 与 replace，整理 go.mod

从 `go.mod` 删除 `github.com/youthlin/silk v0.0.4` 这一行 require，以及第 112 行的 replace。然后运行
`go mod tidy`。

**验证**：
- `grep -n "youthlin\|^replace\|^exclude" go.mod go.sum` → 无输出
- 对比 tidy 前后的 go.mod：除了删掉的这两行，没有其他行变化（先 `cp go.mod $TMPDIR/go.mod.before`，
  tidy 后 `diff $TMPDIR/go.mod.before go.mod` 只显示这两行）

### 步骤 4：编译、vet、测试

依次运行上表中的编译、vet、channel 测试、全量测试、gofmt、`go mod verify`。

**验证**：
- 全部符合"成功时"一列。
- `go list -tags fts5 -f '{{.GoFiles}}' ./pkg/channel | grep -q silk_cgo.go && echo cgo-silk` → `cgo-silk`
- `go list ./third_party/...` 输出两个包：
  `github.com/forebrain-harness/forebrain-harness/third_party/silk` 与 `.../third_party/silk/internal`

### 步骤 5：更新 FORK.md

把 "referenced from the root `go.mod` via: replace …" 这一段改成：这份 fork 是主模块里的普通包，
导入路径是 `github.com/forebrain-harness/forebrain-harness/third_party/silk`。保留"Why we fork"和
"Local changes vs upstream"两节。"Updating"一节改成：上游修复 Windows cgo 问题后，删除
`third_party/silk`，把 `pkg/channel/silk_cgo.go` 的导入换回上游路径，再 `go get` 上游版本；不要重新
引入 `replace`，因为它会让 `go install` 失效。全文用英文。

**验证**：`grep -n "replace" third_party/silk/FORK.md` → 只剩"不要重新引入 replace"那一句

### 步骤 6：新增 `scripts/check-go-install.sh`

脚本在本地搭一个 file:// 模块代理，照用户的方式执行 `go install`：在仓库外执行、带版本后缀、
经过代理、依赖从公共代理下载。这样可以证明当前工作区一旦打成 tag，就能被 go install 安装。

必须具备的行为（照写，注释用英文）：

```bash
#!/usr/bin/env bash
# Prove that `go install github.com/forebrain-harness/forebrain-harness/cmd/forebrain@<version>`
# works for the current tree the way a user runs it: outside any checkout, through a module
# proxy, with go.mod read as a dependency's go.mod (so replace/exclude directives are fatal).
#
#   scripts/check-go-install.sh
#
# The module zip is built from the files a clone would contain: tracked files plus untracked
# files that are not ignored.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
MODULE=github.com/forebrain-harness/forebrain-harness
VERSION=v0.0.0-installcheck
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
PROXY_DIR="$WORK/proxy/$MODULE/@v"
mkdir -p "$PROXY_DIR" "$WORK/bin" "$WORK/outside"

cd "$ROOT_DIR"
git ls-files -z --cached --others --exclude-standard > "$WORK/files"
python3 - "$WORK/files" "$PROXY_DIR/$VERSION.zip" "$MODULE@$VERSION" <<'PY'
import os, sys, zipfile
names = [n for n in open(sys.argv[1], 'rb').read().decode().split('\0') if n]
prefix = sys.argv[3] + '/'
with zipfile.ZipFile(sys.argv[2], 'w', zipfile.ZIP_DEFLATED) as z:
    for name in names:
        if os.path.islink(name) or not os.path.isfile(name):
            continue
        z.write(name, prefix + name)
PY
cp go.mod "$PROXY_DIR/$VERSION.mod"
printf '{"Version":"%s","Time":"2026-01-01T00:00:00Z"}\n' "$VERSION" > "$PROXY_DIR/$VERSION.info"
printf '%s\n' "$VERSION" > "$PROXY_DIR/list"

cd "$WORK/outside"
env -u GOFLAGS GOWORK=off GOBIN="$WORK/bin" CGO_ENABLED=1 \
  GOPROXY="file://$WORK/proxy,https://proxy.golang.org,direct" \
  GONOSUMDB="$MODULE" \
  go install -tags fts5 "$MODULE/cmd/forebrain@$VERSION"
"$WORK/bin/forebrain" --version
echo "go install $MODULE/cmd/forebrain@$VERSION: ok"
```

脚本要 `chmod +x`。

**验证**：`scripts/check-go-install.sh` → exit 0，最后一行是
`go install github.com/forebrain-harness/forebrain-harness/cmd/forebrain@v0.0.0-installcheck: ok`。

在做步骤 1–3 **之前**（在一份拷贝上，或者先跑一次再做步骤 1）对照一次：旧状态下脚本应失败，报错包含
`replace directives`。如果不方便还原，这一对照可以省略，但必须在汇报里说明省略了。

### 步骤 7：CI 新增 go-install 任务

在 `.github/workflows/ci.yml` 的 `jobs:` 下新增（与 `go:` 任务同级）：

```yaml
  go-install:
    name: go install from a module proxy
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache-dependency-path: go.sum
      - name: Install CGO deps
        run: sudo apt-get update && sudo apt-get install -y build-essential
      - run: scripts/check-go-install.sh
```

**验证**：`python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/ci.yml'))" && echo yaml-ok`
→ `yaml-ok`（本机没有 PyYAML 时改用 `ruby -ryaml -e 'YAML.load_file(".github/workflows/ci.yml")'`）

## 测试计划

- 不新增 Go 测试：导入路径变化由编译和 `pkg/channel` 里已有的
  `TestWeixinSilkDecoderWiredUnderCGO`（`pkg/channel/silk_cgo_test.go`）覆盖。
- `scripts/check-go-install.sh` 就是本计划的端到端测试，CI 上每个 PR 都会跑。

## 真实仓库实测（关卡 G1）

1. 推送后等待 main 上的 CI：`RUN=$(gh run list --repo forebrain-harness/forebrain-harness --workflow CI --branch main --limit 1 --json databaseId --jq '.[0].databaseId')`，`gh run watch "$RUN" --repo forebrain-harness/forebrain-harness --exit-status` → exit 0；再查
   `gh run view "$RUN" --repo forebrain-harness/forebrain-harness --json jobs --jq '.jobs[] | select(.name=="go install from a module proxy") | .conclusion'`
   → `success`。
2. 用户视角的真实 go install：proxy.golang.org 直接从 GitHub 取 M1 提交。
   ```bash
   SHA=$(git log --format=%H --grep '^build(deps): fold the silk fork' -1)
   cd "$(mktemp -d)" && env -u GOFLAGS GOWORK=off CGO_ENABLED=1 GOBIN="$PWD/bin" \
  go install -tags fts5 github.com/forebrain-harness/forebrain-harness/cmd/forebrain@"$SHA"
   ./bin/forebrain --version
   ```
   → exit 0，二进制可运行。
3. 记录：CI 运行链接、第 2 步的完整命令与输出。

失败时：用一个新的修正提交（遵守全局规则 1）修复，再推送、重测；不得改写 M1。

## 完成标准（全部满足）

- [ ] M1 已按规范提交（`git log -1 --format=%B <M1>` 含 Why/What/Prompt cache/Verification 与 `Signed-off-by`）
- [ ] 真实仓库实测第 1–2 步通过，并记录在 `LIVE_TEST_LOG.md`
- [ ] `grep -rn "youthlin" --include='*.go' --include='go.mod' --include='go.sum' .` 无输出
- [ ] `grep -n "^replace\|^exclude" go.mod` 无输出
- [ ] `CGO_ENABLED=1 go build -tags fts5 ./...` 与 `go vet ./...` exit 0
- [ ] `CGO_ENABLED=1 go test -tags fts5 ./... -count=1` 全部通过
- [ ] `scripts/check-go-install.sh` exit 0
- [ ] `git status --short` 里的改动全部在"允许修改"清单内
- [ ] README 索引中本计划状态为 DONE

## STOP 条件

- `go mod tidy` 除了 silk 两行之外还改动了别的依赖行。
- `go vet ./...` 对 `third_party/silk/...` 报告问题（以前它是嵌套模块，不在 `./...` 之内；如果现在报
  问题，要由 owner 决定修上游代码还是另做处理）。
- `scripts/check-go-install.sh` 失败，且报错与 replace 无关，例如 zip 校验失败、某个文件路径不合法、
  embed 找不到文件。把完整报错贴回来。
- 需要修改 `third_party/silk/internal` 下的任何文件才能编译通过。

## 维护说明

- 以后引入任何 `replace`/`exclude`，或者新增带 `go.mod` 的子目录，`go-install` CI 任务都会失败。这是
  有意为之：go install 是 owner 指定的两种安装方式之一。
- 并入之后，`deadcode` 这类工具会把 `third_party/silk` 纳入分析范围。它是上游 vendored 代码，其中的
  编码器（`encode.go`）在本项目没有调用方。后续计划跑 deadcode 时用
  `-filter 'forebrain-harness/(pkg|cmd)/'` 限定范围；要不要裁掉编码器，由 owner 另行决定。
- `go test ./...`、`go vet ./...` 现在包含 `third_party/silk`，CI 耗时会略增。
