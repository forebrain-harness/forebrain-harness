# 064：修 Windows 链接序——`-lntdll` 必须落在 `libtokenizers.a` 之后

> **执行者须知（零上下文）**：本计划自包含。逐步执行，每步先跑"验证"确认预期再进入下一步。
> 命中 STOP condition 就停下报告。**不建 worktree、不 commit**。
>
> **Drift check（先做）**：本计划写于 `git rev-parse --short HEAD` = `fa4af8a`（2026-10-16）+
> 工作区未提交改动（055/056 已实施：fetch-pdfium.sh 已带 windows 别名与 token 重试）。
> 执行前先读 `pkg/llm/hftok/link.go` 全文，与下文摘录对照；对不上按 STOP 处理。

## Status

- **Priority**: P1（056 修复后 Windows CI required job 仍会红——同一合并阻断链的下一环）
- **Effort**: S/M（一行级 cgo LDFLAGS 修改 + 容器红/绿验证）
- **Risk**: MED（动 cgo 链接旗标；必须用容器证明 Windows 修复且 darwin/linux 逐字节不变）
- **Depends on**: 056 已实施（本计划的复现容器复用其环境搭法）
- **Category**: Build / Correctness（056 执行期发现，owner 裁决本期根因修掉）
- **Planned at**: commit `fa4af8a` + 工作区（055/056 之后），2026-10-16

## Why this matters

056 的容器复现顺带暴露了一个被 `cannot find -lpdfium` 掩盖至今的**既有** Windows 链接缺陷：
GNU ld 从左到右解析——`pkg/llm/hftok/link.go` 的 `#cgo windows,amd64 LDFLAGS: … -lntdll` 排在
daulet 模块自己的 `-ltokenizers -lm -lstdc++ -lws2_32 -luserenv` **之前**；而 CI 用 Rust 从源码
构建的 `libtokenizers.a`（Rust ≥1.87 的 std）引用 27 个 NT 符号（`NtCreateFile`、
`RtlNtStatusToDosError`…），只有出现在它**之后**的 `-lntdll` 才能满足这些引用。结果：
`undefined reference to NtCreateFile…` ×27，链接失败。

**实证链**（056 执行者在 Debian mingw 容器内）：
1. 修 pdfium 后的裸链接：pdfium 零报错，仅剩 27 个 `Nt*`/`Rtl*` undefined——缺陷定位；
2. Debian mingw 的 `libntdll.a` 确实导出 `NtCreateFile`（`nm` 两处命中）——不是库缺失；
3. 仅追加环境变量 `CGO_LDFLAGS="-lntdll"`（追加在末尾）→ 同一链接成功、产出 PE、
   `objdump -p` 显示 `DLL Name: pdfium.dll`——顺序问题实锤。

056 之前看不见它，是因为 `-lpdfium` 找不到更早地掐断了链接。**不修这个，056 Step 4 推分支后
`windows build test (CGO)` 依旧红。**

## Current state（执行者要亲读的）

`pkg/llm/hftok/link.go`：携带 `#cgo windows,amd64 LDFLAGS: -L<libtokenizers dir> -lntdll`（还有
非 Windows 平台的对应旗标；**执行时读全文**，含 -L 路径的解析方式）。daulet
`tokenizers` 模块的 cgo 旗标（模块缓存内 `$(go env GOMODCACHE)/github.com/daulet/*`）：
`#cgo windows LDFLAGS: -ltokenizers -lm -lstdc++ -lws2_32 -luserenv`。观测到的实际链接序：
hftok 的旗标在前。

## 修复设计

### Step 0：容器复现（红）

复用 056 的容器环境搭法（golang:1.26-bookworm + `gcc-mingw-w64-x86-64` + `g++-mingw-w64-x86-64`
+ rustup + `rustup target add x86_64-pc-windows-gnu` + `scripts/fetch-libtokenizers.sh windows/amd64`
+ `scripts/fetch-pdfium.sh windows/amd64`），以只 import `pkg/llm/hftok` 的临时 main
（或直接 `go build ./pkg/llm/hftok`）做 `GOOS=windows CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc
go build`。**预期**：27 个 NT 符号 undefined（若直接全绿 → STOP-1）。

### Step 1：修复（首选方案：ld 分组）

`pkg/llm/hftok/link.go` 的 windows 旗标改为把 tokenizers 一族与 ntdll 包进
`--start-group/--end-group`（组成员间顺序不再敏感；与 daulet 自己的旗标重复无害——归档重复
搜索是合法的）：

```
-Wl,--start-group -ltokenizers -lntdll -lm -lstdc++ -lws2_32 -luserenv -Wl,--end-group
```

（`-L` 路径保持原样在前；`-Wl,` 形式经本仓库 pdfium.pc 的 `-Wl,-rpath,…` 已证可过 cgo 旗标
白名单。）若 cgo 白名单拒绝 `--start-group`（STOP-2），退路 B：hftok 内**重复**追加
`-lntdll`（第二次出现在 daulet 旗标之后的前提是包序如此——先在容器里验证退路确实生效再落）；
退路 C：把 `-lntdll` 从 hftok 挪进一个专门 import daulet 的小 cgo 文件（依赖序天然靠后）。
**禁止**用 CI 环境变量（`CGO_LDFLAGS=-lntdll`）兜底——那会把修复藏在 workflow 里，本地
`GOOS=windows go build` 依旧红。

### Step 2：容器验证（绿）+ 平台无回归

1. 同容器：Step 0 的构建命令 → 成功；`objdump -p` 产物 import 表同时含 `pdfium.dll` 与
   `ntdll.dll`；27 个 undefined 归零。
2. 最好直接跑整树 `GOOS=windows CGO_ENABLED=1 CC=… go build -tags fts5 ./...`（即 CI windows
   job 的命令）。
3. 本机 darwin 无回归：`make build` 或既有 `go build ./...` + `go test -tags fts5 ./pkg/llm -count=1`；
   linux 交叉：`GOOS=linux CGO_ENABLED=1 go build -tags fts5 -o /dev/null ./cmd/forebrain`（若本机
   有交叉 CC；无则交给 CI，如实报告）。
4. 056 的 pdfium 探针仍绿（别名与分组互不干扰）。

### Step 3：CI 就绪说明

在 `plans/README.md` 不动（评审者维护）；本计划完成后 056 的 Step 4（推分支看 CI）才具备
变绿条件。报告里明确写"Windows CI 预计绿的两项前提：056 别名 + 064 分组"。

## Test plan

无 Go 单测（纯链接期问题，Go 层不可测）。验证=容器红/绿 + objdump import 表 + darwin 无回归，
全部机器可查。若加一条：`pkg/llm/hftok` 已有的任何构建期断言（如有）不回归。

## STOP conditions

1. Step 0 容器复现直接全绿（缺陷在 056 的搭法差异下不可再现）——报告环境差异，不要凭空改。
2. cgo 白名单拒绝 `-Wl,--start-group` 且退路 B/C 在容器内均无法证明生效——报告各退路的实测
   输出，由 owner 定。
3. `pkg/llm/hftok/link.go` 的实际内容与摘录结构差异大到方案需重设计（如旗标由 pkg-config
   间接注入）——停下报告结构。

## Out of scope（不要做）

- darwin/linux 链接旗标的任何改动（只在 windows 条件分支内动手）。
- 升级/替换 daulet tokenizers 模块或 Rust 工具链版本。
- CI workflow 的环境变量兜底（见 Step 1 的禁止项）。
- libtokenizers 构建脚本的改动。

## 全量验证闸门

```bash
# 容器（Windows 交叉）
GOOS=windows CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc \
  PKG_CONFIG_PATH=<repo>/third_party/pdfium/windows_amd64 \
  go build -tags fts5 ./...                       # 绿
# 本机（darwin 无回归）
FOREBRAIN_HOME="$(mktemp -d)" CGO_ENABLED=1 go test -tags fts5 ./pkg/llm -count=1
CGO_ENABLED=1 go vet -tags fts5 ./...
gofmt -l cmd pkg third_party                      # 为空
```
