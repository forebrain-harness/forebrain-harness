# 062：文档对齐现实——pdfium 硬依赖的构建指引 + 两处失实陈述 + PR 描述补记

> **执行者须知（零上下文）**：本计划自包含。逐步执行，每步先跑"验证"确认预期再进入下一步。
> 命中 STOP condition 就停下报告。**不建 worktree、不 commit**。
>
> **Drift check（先做）**：本计划写于 `git rev-parse --short HEAD` = `fa4af8a`（2026-10-16）。
> 所有锚点已逐一亲核（见 Current state）。执行前重核；对不上按 STOP 处理。

## Status

- **Priority**: P2（文档与现实矛盾：新 clone 者按 README 第一步就撞墙；两处失实陈述误导维护者）
- **Effort**: S
- **Risk**: LOW（纯文档 + 两处注释/一行 Makefile 修正）
- **Depends on**: none（若与 056 同期执行，README 的 Windows 说明以 056 落地后的脚本行为为准——
  本计划不写 Windows 细节，无冲突）
- **Category**: DX / Docs（审计发现 #13/#17/#19）
- **Planned at**: commit `fa4af8a`，2026-10-16

## Why this matters

1. **#13 构建指引与硬依赖矛盾**：`scripts/fetch-pdfium.sh` 缺 `pkg-config` 或 `jq` 直接 die，
   而裸 `go build`/`go vet`/`go test` 没有 pdfium 的 pkg-config 接线必失败（`Package pdfium was
   found…not in the pkg-config search path`）。但：
   - `README.md` Requirements（"Build from source" 段）只写 Go 1.26 CGO + Node 22 pnpm + tokenizers，
     **没有 pkg-config 与 jq**；
   - `README.md` 与 `CONTRIBUTING.md` 的 "For day-to-day Go work" 块仍是
     `scripts/fetch-libtokenizers.sh` + 裸 `go build -tags fts5 …` ——照抄必失败；
   - `CONTRIBUTING.md` Checks 段让贡献者裸跑 vet，而 `PKG_CONFIG_PATH` 只在 Makefile recipe 里导出，
     没有 `make vet` 之类的入口。
   - 顺带两处小疵（同根）：`Makefile` 的 `export PKG_CONFIG_PATH := $(PDFIUM_DIR):$(PKG_CONFIG_PATH)`
     在变量未设时导出**尾随空 entry**（`dir:`，pkg-config 语义为 cwd，等于把 cwd 悄悄加进搜索路径）；
     `scripts/package-release-archive.sh` 头注释列举档案内容漏了 `fonts/` 与 `libpdfium`。
2. **#17 两处失实陈述**：
   - `plans/README.md` 054 条目（"打包/CI"段，grep `check-go-install` 唯一现役命中）仍把
     `scripts/check-go-install.sh` 列入"均已接线"——该文件已随 6d1730e 删除，两个 go-install
     CI job 一并移除。（plans/040 与 docs/plan/OSS_AUTOMATION 里的引用是历史记录，不改。）
   - `docs/maintainers/RELEASING.md` "A released version is broken" 段说 "the Go module proxy
     and checksum database have recorded the version" 作为绝不可 re-tag 的理由——唯一让
     proxy/sumdb 记录该版本的 go-install 管线步骤已删，该前提对**尚无人 fetch 过**的版本不再成立。
3. **#19 提交 6d1730e 主题为字面量 "…"、无 body**，却承载"下架 go install 支持"整项决定（删
   ci.yml/release.yml 两个 job、check-go-install.sh、README/RELEASING 各一段）。修复方式不是改
   历史（分支本地提交不可变），而是**开 PR 时在描述里补记**——本计划把补记文案写进 plans/README
   的批次条目，开 PR 时直接抄。

## Current state（全部亲核于 fa4af8a）

- `README.md` "Build from source" 段：Requirements 段落 + `make build` 块 + "For day-to-day Go
  work, skip the UI rebuild:" 块（`scripts/fetch-libtokenizers.sh` / 裸 `CGO_ENABLED=1 go build -tags fts5` /
  `install-dictionary.sh` / `install-assets.sh`）。同段落无任何 pdfium/pkg-config/jq 字样。
- `CONTRIBUTING.md`：同构的 day-to-day 块；`## Checks` 段（"Run these before opening a pull
  request — CI runs the same set"）。
- `Makefile`：`PDFIUM_TRIPLE`/`PDFIUM_DIR` 定义与
  `export PKG_CONFIG_PATH := $(PDFIUM_DIR):$(PKG_CONFIG_PATH)`（连带 `CGO_CFLAGS`/`CGO_LDFLAGS`
  两行同样模式的拼接——空值时同样产生多余空 entry，但 cflags 里空 entry 无害，只修 pkg-config 行）。
- `scripts/package-release-archive.sh` 头注释："forebrain[.exe], the dict/ directory, models.json
  and the tokenizers/ directory beside it"。
- `plans/README.md` 054 条目"打包/CI"段：`…ci.yml/lsp-integration.yml/release.yml/security.yml/
  scripts/check-go-install.sh 均已接线…`。
- `docs/maintainers/RELEASING.md`："Never delete or move an already published tag: the Go module
  proxy and checksum database have recorded the version, and the archives and npm packages are
  already published against it."
- 注意：**FOREBRAIN.md 的 Commands 段已经是对的**（`make pdfium pdfium-fonts` 语境 + scratch
  home 测试规范）——不要重复改它；本计划只补 README/CONTRIBUTING 两个用户侧入口。

## 修复设计

### Step 1（#13）：README + CONTRIBUTING 构建指引

1. `README.md` Requirements 段补：`pkg-config` 与 `jq`（fetch-pdfium.sh 的两个硬依赖；tokenizers
   一句已有，补一句 pdfium——"and `scripts/fetch-pdfium.sh` (run by `make build`) fetches the
   PDFium library the PDF reader links; it needs `pkg-config` and `jq`"）。
2. README 与 CONTRIBUTING 的 day-to-day 块统一改为（保持两文件同构）：

```bash
scripts/fetch-libtokenizers.sh               # the tokenizers static library the binary links
make pdfium pdfium-fonts                     # PDFium + CJK fonts; exports PKG_CONFIG_PATH for the shell only inside make
CGO_ENABLED=1 PKG_CONFIG_PATH="$PWD/third_party/pdfium/$(go env GOOS)_$(go env GOARCH)" \
  go build -tags fts5 -o ./build/bin/forebrain ./cmd/forebrain
scripts/install-dictionary.sh ./build/bin   # memory search reads dict/ beside the binary
scripts/install-assets.sh ./build/bin       # models.json beside the binary
```

3. `CONTRIBUTING.md` Checks 段的 vet/test 命令补同样的 `PKG_CONFIG_PATH` 前缀（或注明"run
   `make vet`-style"——**二选一**：要么补前缀，要么本步顺带在 Makefile 加一个 `vet` target
   并让文档指向它；推荐前者，改动最小）。
4. `Makefile`：`export PKG_CONFIG_PATH := $(PDFIUM_DIR):$(PKG_CONFIG_PATH)` 改为条件拼接，
   未设时不产生尾随冒号：

```make
ifdef PKG_CONFIG_PATH
export PKG_CONFIG_PATH := $(PDFIUM_DIR):$(PKG_CONFIG_PATH)
else
export PKG_CONFIG_PATH := $(PDFIUM_DIR)
endif
```

5. `scripts/package-release-archive.sh` 头注释补 `fonts/` 与 `libpdfium`（对照
   `npm/scripts/build-platform-packages.sh` 实际打包内容写）。

**验证**：`make build` 仍过（本机已验证过的路径）；`make -n vet 2>/dev/null || true` 不适用则跳过；
`grep -n "pkg-config\|jq" README.md CONTRIBUTING.md` 有命中；`bash -n scripts/package-release-archive.sh`。

### Step 2（#17）：两处失实

1. `plans/README.md` 054 条目"打包/CI"段：把 `scripts/check-go-install.sh 均已接线` 的列举改为
   如实陈述——`ci.yml/lsp-integration.yml/release.yml/security.yml 均已接线；go-install 相关
   job 与 scripts/check-go-install.sh 已随后续提交（6d1730e）随 go install 支持一并下架`。
   （只改这一处列举；054 条目的其余执行记录是历史，不动。）
2. `docs/maintainers/RELEASING.md` re-tag 段改写为不依赖已删管线的前提：

> Never delete or move an already published tag: once **any** user has fetched the module (or the
> archives and npm packages were published against it), the Go module proxy and checksum database
> may have recorded the version, and re-pointing it serves different bytes to different people.
> Fix forward with a new version (`fix: …` or `Release-As:` as needed).

   （保留"never"结论——发布产物已对外这一半前提仍然成立；只是不再断言"proxy 已记录"。）

**验证**：`grep -rn "check-go-install" README.md CONTRIBUTING.md docs/maintainers/ plans/README.md`
仅剩 plans/README 的历史性提及与 plans/040、docs/plan/OSS_AUTOMATION 历史文档（这两处 grep 确认）。

### Step 3（#19）：PR 描述补记文案

在 `plans/README.md` 的 2026-10-16 审计批次条目末尾追加一段（开本批改动的 PR 时抄进描述）：

> **PR 描述必须补记（来源：提交 6d1730e 主题为 "…"、无 body）**：本分支包含"go install 自
> PDFium 原生依赖起不再是受支持的安装方式"的决定——`go install` 无法携带/定位
> `third_party/pdfium` 的库与字体，因此 ci.yml/release.yml 的 go-install job、
> `scripts/check-go-install.sh`、README/RELEASING/bug_report 的相应段落一并移除；安装走
> Release 档案 / npm 包 / `make build`。

**验证**：文案落盘；`git log --format='%s' bd2b8b8..fa4af8a | grep -c '^\\.\\.\\.$'` = 1（对照
  该提交确实存在）。

## Test plan

纯文档 + Makefile 一行 + 脚本注释：无新增 Go 测试。验证即各 Step 的 grep/make/build 检查。
`Makefile` 改动跑一次 `make build` 确认无回归（pdfium 目标幂等，秒过）。

## STOP conditions

1. `Makefile` 的条件拼接与既有 `CGO_CFLAGS`/`CGO_LDFLAGS` 导出风格冲突导致 `make build` 红
   ——回退该行改动（尾随空 entry 无害化处理可延后），其余步骤照常，报告即可。
2. 054 条目的"打包/CI"段已被其它计划（056 等）改动导致文案纠缠——以 056 落地后的表述为准做
   最小修正，不回滚别人改动。
3. RELEASING 的重写与 release-please 自动化文档冲突（如同文件另有段落断言 proxy 记录）——
   一并对齐，仍冲突则停。

## Out of scope（不要做）

- FOREBRAIN.md（已正确）；docs/ 站点（docs site）的构建文档镜像（zh 镜像同段如有同样问题，
  **顺带对齐**——执行时 grep `fetch-libtokenizers` 于 docs/ 下确认，命中则同修，这是同一失实
  陈述的镜像而非扩权）。
- 恢复 go-install 管线或 proxy 预热步骤（产品决定已下架，#17 只修陈述）。
- 重写 6d1730e 历史（不可能也不必要）。

## 全量验证闸门

```bash
make build                                   # Makefile 改动无回归
grep -n "pkg-config" README.md CONTRIBUTING.md
CGO_ENABLED=1 go vet -tags fts5 ./...        # 文档计划自身不触代码，此行证明树仍绿
gofmt -l cmd pkg third_party                 # 为空
```
